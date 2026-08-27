package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/api"
	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/fleet"
	"offshore-buoy-drift-search-loop/internal/mission"
	"offshore-buoy-drift-search-loop/internal/store"
)

func TestModel_ScheduleConflictRejectionIsPersistedAtomically(t *testing.T) {
	type testCase struct {
		name                 string
		overlap              bool
		confirmAttempts      int
		wantPlanStatus       string
		wantAssignmentStatus string
		wantTaskStatus       string
		wantNotices          int
		wantEvents           int
	}
	cases := []testCase{
		{
			name:                 "overlap rejection remains queryable and repeated notice is deduplicated",
			overlap:              true,
			confirmAttempts:      2,
			wantPlanStatus:       "draft",
			wantAssignmentStatus: "proposed",
			wantTaskStatus:       "pending_schedule",
			wantNotices:          1,
			wantEvents:           2,
		},
		{
			name:                 "non-overlapping plan confirms all assignments atomically",
			overlap:              false,
			confirmAttempts:      1,
			wantPlanStatus:       "confirmed",
			wantAssignmentStatus: "confirmed",
			wantTaskStatus:       "searching",
			wantNotices:          0,
			wantEvents:           0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			handle, err := store.Open(ctx, store.Config{SQLitePath: ":memory:"})
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = handle.Close() })

			fixed := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
			timeline := audit.NewPersistentTimeline(handle)
			server := api.NewServer(api.Dependencies{
				Store:         handle,
				Missions:      mission.NewPersistentService(domain.FixedClock{At: fixed}, timeline, handle),
				Drift:         drift.NewEngine(),
				Scheduler:     fleet.NewScheduler(),
				Execution:     execution.NewPersistentService(domain.FixedClock{At: fixed}, timeline, handle),
				Timeline:      timeline,
				Notifications: audit.NewNotificationCenter(),
				Replans:       execution.NewReplanStore(),
			})

			now := fixed.Format(time.RFC3339Nano)
			start := fixed.Add(2 * time.Hour).Format(time.RFC3339Nano)
			end := fixed.Add(3 * time.Hour).Format(time.RFC3339Nano)
			statements := []struct {
				query string
				args  []any
			}{
				{`INSERT INTO buoys(id, buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at) VALUES(1, 'B-1', 'marker', ?, 20, 110, 9000, 'drift', 1, ?, ?)`, []any{now, now, now}},
				{`INSERT INTO search_tasks(id, buoy_id, status, submitted_at, active_sector_set_version, version, event_sequence, created_by, created_at, updated_at) VALUES(1, 1, 'pending_schedule', ?, 1, 7, 0, 'commander-1', ?, ?)`, []any{now, now, now}},
				{`INSERT INTO current_snapshots(id, task_id, effective_at, direction_millidegrees, speed_milliknots, uncertainty_millinautical_miles, created_by, created_at) VALUES(1, 1, ?, 90000, 1000, 500, 'commander-1', ?)`, []any{now, now}},
				{`INSERT INTO sector_sets(id, task_id, version, snapshot_id, algorithm_version, normalized_input_json, input_digest, predicted_latitude, predicted_longitude, drift_distance_nm, effective_radius_nm, created_at) VALUES(1, 1, 1, 1, 'v1', '{}', 'digest', 20, 110, 1, 2, ?)`, []any{now}},
				{`INSERT INTO search_sectors(id, task_id, sector_set_id, sector_set_version, number, priority, name, polygon_json, area_square_nm, centroid_latitude, centroid_longitude, version) VALUES(1, 1, 1, 1, 1, 1, 'one', '[]', 1, 20, 110, 1), (2, 1, 1, 1, 2, 2, 'two', '[]', 1, 20, 111, 1)`, nil},
				{`INSERT INTO vessels(id, vessel_no, speed_milliknots, endurance_seconds, max_operation_millinautical_miles, online_status, active_load, version, created_at, updated_at) VALUES(10, 'V-10', 10000, 86400, 100000, 'online', 0, 1, ?, ?), (20, 'V-20', 10000, 86400, 100000, 'online', 0, 1, ?, ?)`, []any{now, now, now, now}},
				{`INSERT INTO schedule_plans(id, task_id, sector_set_version, plan_type, status, generated_at, expected_task_version, version, created_at, updated_at) VALUES(50, 1, 1, 'auto', 'draft', ?, 7, 3, ?, ?)`, []any{now, now, now}},
				{`INSERT INTO assignments(id, task_id, plan_id, sector_id, sector_number, sector_set_version, vessel_id, start_at, end_at, score, status, version, created_at, updated_at) VALUES(201, 1, 50, 1, 1, 1, 10, ?, ?, 100, 'proposed', 1, ?, ?), (202, 1, 50, 2, 2, 1, 20, ?, ?, 90, 'proposed', 1, ?, ?)`, []any{start, end, now, now, fixed.Add(4 * time.Hour).Format(time.RFC3339Nano), fixed.Add(5 * time.Hour).Format(time.RFC3339Nano), now, now}},
			}
			if tc.overlap {
				statements = append(statements, struct {
					query string
					args  []any
				}{`INSERT INTO assignments(id, task_id, sector_id, sector_number, sector_set_version, vessel_id, start_at, end_at, score, status, version, created_at, updated_at) VALUES(100, 1, 1, 1, 1, 10, ?, ?, 100, 'confirmed', 1, ?, ?)`, []any{fixed.Add(150 * time.Minute).Format(time.RFC3339Nano), fixed.Add(210 * time.Minute).Format(time.RFC3339Nano), now, now}})
			}
			for _, stmt := range statements {
				if _, err := handle.DB().ExecContext(ctx, stmt.query, stmt.args...); err != nil {
					t.Fatalf("seed database: %v", err)
				}
			}

			for attempt := 0; attempt < tc.confirmAttempts; attempt++ {
				body := bytes.NewBufferString(`{"expected_plan_version":3,"expected_task_version":7,"reason":"approved"}`)
				req := httptest.NewRequest(http.MethodPost, "/v1/schedule-plans/50/confirm", body)
				req.Header.Set("X-Actor-ID", "commander-1")
				req.Header.Set("X-Role", "commander")
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)
				if tc.overlap {
					if rec.Code != http.StatusConflict && rec.Code != http.StatusInternalServerError {
						t.Fatalf("confirm attempt %d status = %d, want conflict rejection", attempt+1, rec.Code)
					}
				} else if rec.Code != http.StatusOK {
					t.Fatalf("confirm status = %d, want 200; body=%s", rec.Code, rec.Body.String())
				}
			}

			var planStatus, taskStatus string
			var planVersion, taskVersion int
			if err := handle.DB().QueryRowContext(ctx, `SELECT status, version FROM schedule_plans WHERE id=50`).Scan(&planStatus, &planVersion); err != nil {
				t.Fatal(err)
			}
			if err := handle.DB().QueryRowContext(ctx, `SELECT status, version FROM search_tasks WHERE id=1`).Scan(&taskStatus, &taskVersion); err != nil {
				t.Fatal(err)
			}
			if planStatus != tc.wantPlanStatus || taskStatus != tc.wantTaskStatus {
				t.Fatalf("plan/task status = %s/%s, want %s/%s", planStatus, taskStatus, tc.wantPlanStatus, tc.wantTaskStatus)
			}
			wantPlanVersion, wantTaskVersion := 3, 7
			if !tc.overlap {
				wantPlanVersion, wantTaskVersion = 4, 8
			}
			if planVersion != wantPlanVersion || taskVersion != wantTaskVersion {
				t.Fatalf("plan/task version = %d/%d, want %d/%d", planVersion, taskVersion, wantPlanVersion, wantTaskVersion)
			}
			for _, assignmentID := range []int64{201, 202} {
				var status string
				var version int
				if err := handle.DB().QueryRowContext(ctx, `SELECT status, version FROM assignments WHERE id=?`, assignmentID).Scan(&status, &version); err != nil {
					t.Fatal(err)
				}
				wantVersion := 1
				if !tc.overlap {
					wantVersion = 2
				}
				if status != tc.wantAssignmentStatus || version != wantVersion {
					t.Errorf("assignment %d status/version = %s/%d, want %s/%d", assignmentID, status, version, tc.wantAssignmentStatus, wantVersion)
				}
			}

			get := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("X-Actor-ID", "auditor-1")
				req.Header.Set("X-Role", "auditor")
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s status = %d; body=%s", path, rec.Code, rec.Body.String())
				}
				return rec
			}

			var notices struct {
				Data []struct {
					TaskID    int64          `json:"task_id"`
					Type      string         `json:"type"`
					DedupeKey string         `json:"dedupe_key"`
					Payload   map[string]any `json:"payload"`
				} `json:"data"`
			}
			if err := json.Unmarshal(get("/v1/notifications").Body.Bytes(), &notices); err != nil {
				t.Fatal(err)
			}
			if len(notices.Data) != tc.wantNotices {
				t.Fatalf("public notifications count = %d, want %d", len(notices.Data), tc.wantNotices)
			}
			if tc.wantNotices > 0 {
				n := notices.Data[0]
				if n.TaskID != 1 || n.Type != "assignment_conflict" || n.DedupeKey != "assignment_conflict:1:10" || n.Payload["assignment_id"] != float64(201) {
					t.Fatalf("unexpected conflict notification: %+v", n)
				}
			}

			var events struct {
				Data []struct {
					Type    string         `json:"event_type"`
					Payload map[string]any `json:"payload"`
				} `json:"data"`
			}
			if err := json.Unmarshal(get("/v1/tasks/1/events?event_type=assignment_conflict").Body.Bytes(), &events); err != nil {
				t.Fatal(err)
			}
			if len(events.Data) != tc.wantEvents {
				t.Fatalf("public rejection events count = %d, want %d", len(events.Data), tc.wantEvents)
			}
			for i, event := range events.Data {
				if event.Type != "assignment_conflict" || event.Payload["reason"] != "assignment_conflict" || event.Payload["assignment_id"] != float64(201) {
					t.Errorf("event %d does not describe the rejected assignment: %+v", i, event)
				}
			}

			var review struct {
				Data struct {
					UnresolvedAlerts int `json:"unresolved_alerts"`
					Events           []struct {
						Type string `json:"event_type"`
					} `json:"events"`
				} `json:"data"`
			}
			if err := json.Unmarshal(get("/v1/tasks/1/review").Body.Bytes(), &review); err != nil {
				t.Fatal(err)
			}
			if review.Data.UnresolvedAlerts != tc.wantNotices || len(review.Data.Events) != tc.wantEvents {
				t.Fatalf("review alerts/events = %d/%d, want %d/%d", review.Data.UnresolvedAlerts, len(review.Data.Events), tc.wantNotices, tc.wantEvents)
			}
		})
	}
}
