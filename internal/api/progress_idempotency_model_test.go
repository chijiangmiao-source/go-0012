package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/api"
	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/store"
)

func TestModel_PersistentProgressRetryHasNoDuplicateEffects(t *testing.T) {
	type progressResponse struct {
		Data    execution.ProgressResult `json:"data"`
		Version int64                    `json:"version"`
	}
	type eventsResponse struct {
		Data []audit.Event `json:"data"`
	}
	type request struct {
		key             string
		expectedVersion int64
		delta           int
	}

	cases := []struct {
		name                  string
		requests              []request
		wantResults           []execution.ProgressResult
		wantAssignmentVersion int64
		wantSectorVersion     int64
		wantCoverage          int
		wantReports           int
		wantEvents            int
	}{
		{
			name: "lost response retried with the same key",
			requests: []request{
				{key: "progress-1", expectedVersion: 1, delta: 250},
				{key: "progress-1", expectedVersion: 1, delta: 250},
			},
			wantResults: []execution.ProgressResult{
				{AssignmentID: 400, CoverageBasisPoints: 250, Version: 2},
				{AssignmentID: 400, CoverageBasisPoints: 250, Version: 2},
			},
			wantAssignmentVersion: 2,
			wantSectorVersion:     2,
			wantCoverage:          250,
			wantReports:           1,
			wantEvents:            1,
		},
		{
			name: "a different key records new progress",
			requests: []request{
				{key: "progress-1", expectedVersion: 1, delta: 250},
				{key: "progress-2", expectedVersion: 2, delta: 125},
			},
			wantResults: []execution.ProgressResult{
				{AssignmentID: 400, CoverageBasisPoints: 250, Version: 2},
				{AssignmentID: 400, CoverageBasisPoints: 375, Version: 3},
			},
			wantAssignmentVersion: 3,
			wantSectorVersion:     3,
			wantCoverage:          375,
			wantReports:           2,
			wantEvents:            2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, store.Config{SQLitePath: ":memory:"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })

			now := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
			stamp := now.Format(time.RFC3339Nano)
			seed := []struct {
				query string
				args  []any
			}{
				{`INSERT INTO buoys(id, buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at) VALUES(100, 'B-100', 'marker', ?, 1, 2, 9000, 'adrift', 1, ?, ?)`, []any{stamp, stamp, stamp}},
				{`INSERT INTO search_tasks(id, buoy_id, status, active_sector_set_version, version, created_by, created_at, updated_at) VALUES(200, 100, 'searching', 1, 1, 'commander-1', ?, ?)`, []any{stamp, stamp}},
				{`INSERT INTO current_snapshots(id, task_id, effective_at, direction_millidegrees, speed_milliknots, uncertainty_millinautical_miles, created_by, created_at) VALUES(210, 200, ?, 90000, 1000, 500, 'commander-1', ?)`, []any{stamp, stamp}},
				{`INSERT INTO sector_sets(id, task_id, version, snapshot_id, algorithm_version, normalized_input_json, input_digest, predicted_latitude, predicted_longitude, drift_distance_nm, effective_radius_nm, created_at) VALUES(220, 200, 1, 210, 'v1', '{}', 'digest', 1, 2, 3, 4, ?)`, []any{stamp}},
				{`INSERT INTO search_sectors(id, task_id, sector_set_id, sector_set_version, number, priority, name, polygon_json, area_square_nm, centroid_latitude, centroid_longitude, coverage_basis_points, claimed_status, version) VALUES(300, 200, 220, 1, 1, 1, 'S-1', '{}', 1, 1, 2, 0, 'claimed', 1)`, nil},
				{`INSERT INTO vessels(id, vessel_no, speed_milliknots, endurance_seconds, max_operation_millinautical_miles, online_status, active_load, version, created_at, updated_at) VALUES(310, 'V-310', 1000, 3600, 10000, 'online', 1, 1, ?, ?)`, []any{stamp, stamp}},
				{`INSERT INTO assignments(id, task_id, sector_id, sector_number, sector_set_version, vessel_id, start_at, end_at, score, status, version, created_at, updated_at) VALUES(400, 200, 300, 1, 1, 310, ?, ?, 100, 'executing', 1, ?, ?)`, []any{stamp, now.Add(time.Hour).Format(time.RFC3339Nano), stamp, stamp}},
			}
			for _, row := range seed {
				if _, err := db.DB().ExecContext(ctx, row.query, row.args...); err != nil {
					t.Fatalf("seed database: %v", err)
				}
			}

			timeline := audit.NewPersistentTimeline(db)
			clock := domain.FixedClock{At: now}
			server := api.NewServer(api.Dependencies{
				Store:     db,
				Execution: execution.NewPersistentService(clock, timeline, db),
				Timeline:  timeline,
			})

			for i, input := range tc.requests {
				body := []byte(fmt.Sprintf(`{"vessel_id":310,"expected_version":%d,"delta_basis_points":%d}`, input.expectedVersion, input.delta))
				req := httptest.NewRequest(http.MethodPost, "/v1/assignments/400/progress", bytes.NewReader(body))
				req.Header.Set("X-Actor-ID", "operator-1")
				req.Header.Set("X-Role", string(domain.RoleOperator))
				req.Header.Set("Idempotency-Key", input.key)
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("request %d status = %d, body = %s", i+1, rec.Code, rec.Body.String())
				}
				var response progressResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode request %d response: %v", i+1, err)
				}
				if response.Data != tc.wantResults[i] || response.Version != tc.wantResults[i].Version {
					t.Errorf("request %d result = %#v (envelope version %d), want %#v", i+1, response.Data, response.Version, tc.wantResults[i])
				}
			}

			var assignmentRows, assignmentVersion int
			if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*), MAX(version) FROM assignments WHERE id = 400`).Scan(&assignmentRows, &assignmentVersion); err != nil {
				t.Fatal(err)
			}
			var sectorRows, coverage, sectorVersion int
			if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*), MAX(coverage_basis_points), MAX(version) FROM search_sectors WHERE id = 300`).Scan(&sectorRows, &coverage, &sectorVersion); err != nil {
				t.Fatal(err)
			}
			var reportCount, eventCount int
			if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_reports WHERE assignment_id = 400 AND report_type = 'progress'`).Scan(&reportCount); err != nil {
				t.Fatal(err)
			}
			if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = 200 AND event_type = 'progress_reported'`).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			if assignmentRows != 1 || assignmentVersion != int(tc.wantAssignmentVersion) || sectorRows != 1 || coverage != tc.wantCoverage || sectorVersion != int(tc.wantSectorVersion) || reportCount != tc.wantReports || eventCount != tc.wantEvents {
				t.Errorf("persisted effects: assignments=(rows %d, version %d), sector=(rows %d, coverage %d, version %d), reports=%d, events=%d", assignmentRows, assignmentVersion, sectorRows, coverage, sectorVersion, reportCount, eventCount)
			}

			req := httptest.NewRequest(http.MethodGet, "/v1/tasks/200/events?event_type=progress_reported", nil)
			req.Header.Set("X-Actor-ID", "auditor-1")
			req.Header.Set("X-Role", string(domain.RoleAuditor))
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("events status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var response eventsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode events response: %v", err)
			}
			if len(response.Data) != tc.wantEvents {
				t.Errorf("GET progress_reported events = %d, want %d", len(response.Data), tc.wantEvents)
			}
		})
	}
}
