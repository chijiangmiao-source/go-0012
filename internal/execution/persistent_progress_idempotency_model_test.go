package execution_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/store"
)

func TestModel_PersistentProgressIdempotency(t *testing.T) {
	tests := []struct {
		name       string
		retryKey   string
		retryDelta int
		wantCode   string
	}{
		{name: "identical retry replays first success", retryKey: "progress-key", retryDelta: 250},
		{name: "same key with different digest is rejected", retryKey: "progress-key", retryDelta: 251, wantCode: domain.CodeIdempotencyMismatch},
		{name: "new key with old version remains stale", retryKey: "another-key", retryDelta: 250, wantCode: domain.CodeStaleVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, store.Config{SQLitePath: ":memory:"})
			if err != nil {
				t.Fatalf("open persistent store: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			_, err = db.DB().ExecContext(ctx, `
INSERT INTO buoys(id, buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at)
VALUES(1, 'B-1', 'marker', '2026-01-02T03:04:05Z', 1, 2, 9000, 'drift', 1, '2026-01-02T03:04:05Z', '2026-01-02T03:04:05Z');
INSERT INTO search_tasks(id, buoy_id, status, active_sector_set_version, version, created_by, created_at, updated_at)
VALUES(10, 1, 'searching', 1, 1, 'operator-1', '2026-01-02T03:04:05Z', '2026-01-02T03:04:05Z');
INSERT INTO current_snapshots(id, task_id, effective_at, direction_millidegrees, speed_milliknots, uncertainty_millinautical_miles, created_by, created_at)
VALUES(11, 10, '2026-01-02T03:04:05Z', 0, 0, 0, 'operator-1', '2026-01-02T03:04:05Z');
INSERT INTO sector_sets(id, task_id, version, snapshot_id, algorithm_version, normalized_input_json, input_digest, predicted_latitude, predicted_longitude, drift_distance_nm, effective_radius_nm, created_at)
VALUES(12, 10, 1, 11, 'v1', '{}', 'digest', 1, 2, 0, 1, '2026-01-02T03:04:05Z');
INSERT INTO search_sectors(id, task_id, sector_set_id, sector_set_version, number, priority, name, polygon_json, area_square_nm, centroid_latitude, centroid_longitude, coverage_basis_points, claimed_status, version)
VALUES(20, 10, 12, 1, 1, 1, 'sector-1', '[]', 1, 1, 2, 0, 'open', 1);
INSERT INTO vessels(id, vessel_no, speed_milliknots, endurance_seconds, max_operation_millinautical_miles, online_status, active_load, version, created_at, updated_at)
VALUES(30, 'V-1', 1000, 3600, 10000, 'online', 0, 1, '2026-01-02T03:04:05Z', '2026-01-02T03:04:05Z');
INSERT INTO assignments(id, task_id, sector_id, sector_number, sector_set_version, vessel_id, start_at, end_at, score, status, version, created_at, updated_at)
VALUES(40, 10, 20, 1, 1, 30, '2026-01-02T03:04:05Z', '2026-01-02T04:04:05Z', 100, 'confirmed', 1, '2026-01-02T03:04:05Z', '2026-01-02T03:04:05Z');`)
			if err != nil {
				t.Fatalf("seed persistent assignment: %v", err)
			}

			clock := domain.FixedClock{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
			service := execution.NewPersistentService(clock, nil, db)
			actor := domain.Actor{ID: "operator-1", Role: domain.RoleOperator}
			first, err := service.ReportProgress(40, 30, 1, "progress-key", 250, actor)
			if err != nil {
				t.Fatalf("first progress report: %v", err)
			}

			retry, retryErr := service.ReportProgress(40, 30, 1, tt.retryKey, tt.retryDelta, actor)
			if tt.wantCode == "" {
				if retryErr != nil {
					t.Fatalf("identical retry returned error: %v", retryErr)
				}
				if retry != first {
					t.Fatalf("replayed response = %#v, want first response %#v", retry, first)
				}
			} else if !domain.IsCode(retryErr, tt.wantCode) {
				t.Fatalf("retry error = %v, want code %q", retryErr, tt.wantCode)
			}

			var coverage int
			var assignmentVersion int64
			var reportCount, recordCount, responseStatus int
			var responseJSON string
			if err := db.DB().QueryRowContext(ctx, "SELECT coverage_basis_points FROM search_sectors WHERE id = 20").Scan(&coverage); err != nil {
				t.Fatalf("read coverage: %v", err)
			}
			if err := db.DB().QueryRowContext(ctx, "SELECT version FROM assignments WHERE id = 40").Scan(&assignmentVersion); err != nil {
				t.Fatalf("read assignment version: %v", err)
			}
			if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_reports WHERE assignment_id = 40").Scan(&reportCount); err != nil {
				t.Fatalf("count execution reports: %v", err)
			}
			if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM idempotency_records WHERE task_id = 10 AND vessel_id = 30 AND operation = 'progress'").Scan(&recordCount); err != nil {
				t.Fatalf("count idempotency records: %v", err)
			}
			if err := db.DB().QueryRowContext(ctx, "SELECT response_status, response_json FROM idempotency_records WHERE task_id = 10 AND vessel_id = 30 AND operation = 'progress' AND idempotency_key = 'progress-key'").Scan(&responseStatus, &responseJSON); err != nil {
				t.Fatalf("read stored response: %v", err)
			}
			var stored execution.ProgressResult
			if err := json.Unmarshal([]byte(responseJSON), &stored); err != nil {
				t.Fatalf("decode stored response: %v", err)
			}
			if coverage != 250 || assignmentVersion != 2 || reportCount != 1 || recordCount != 1 {
				t.Fatalf("persistent state after retry: coverage=%d version=%d reports=%d records=%d, want 250/2/1/1", coverage, assignmentVersion, reportCount, recordCount)
			}
			if responseStatus != 200 || stored != first {
				t.Fatalf("stored response: status=%d body=%#v, want 200 and %#v", responseStatus, stored, first)
			}
		})
	}
}
