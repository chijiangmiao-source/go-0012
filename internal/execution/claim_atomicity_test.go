package execution_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/store"
)

func TestModel_ClaimPersistsStateAndAuditAtomically(t *testing.T) {
	tests := []struct {
		name                  string
		rejectTimelineEvent   bool
		wantError             bool
		wantAssignmentStatus  string
		wantAssignmentVersion int64
		wantSectorStatus      string
		wantSectorVersion     int64
		wantEventSequence     int64
		wantClaimedEvents     int
	}{
		{
			name:                  "successful claim commits assignment sector and event",
			wantAssignmentStatus:  "claimed",
			wantAssignmentVersion: 2,
			wantSectorStatus:      "claimed",
			wantSectorVersion:     2,
			wantEventSequence:     1,
			wantClaimedEvents:     1,
		},
		{
			name:                  "timeline failure rolls back assignment and sector",
			rejectTimelineEvent:   true,
			wantError:             true,
			wantAssignmentStatus:  "confirmed",
			wantAssignmentVersion: 1,
			wantSectorStatus:      "open",
			wantSectorVersion:     1,
			wantEventSequence:     0,
			wantClaimedEvents:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "claim.db")
			handle, err := store.Open(ctx, store.Config{SQLitePath: dbPath})
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = handle.Close() })

			now := "2026-01-02T03:04:05Z"
			seed := []struct {
				query string
				args  []any
			}{
				{`INSERT INTO buoys(id, buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at)
VALUES(1, 'B-1', 'beacon', ?, 1, 2, 9000, 'adrift', 1, ?, ?)`, []any{now, now, now}},
				{`INSERT INTO search_tasks(id, buoy_id, status, active_sector_set_version, version, event_sequence, created_by, created_at, updated_at)
VALUES(10, 1, 'searching', 1, 1, 0, 'commander-1', ?, ?)`, []any{now, now}},
				{`INSERT INTO current_snapshots(id, task_id, effective_at, direction_millidegrees, speed_milliknots, uncertainty_millinautical_miles, created_by, created_at)
VALUES(11, 10, ?, 0, 0, 0, 'commander-1', ?)`, []any{now, now}},
				{`INSERT INTO sector_sets(id, task_id, version, snapshot_id, algorithm_version, normalized_input_json, input_digest, predicted_latitude, predicted_longitude, drift_distance_nm, effective_radius_nm, created_at)
VALUES(12, 10, 1, 11, 'v1', '{}', 'digest', 1, 2, 0, 1, ?)`, []any{now}},
				{`INSERT INTO search_sectors(id, task_id, sector_set_id, sector_set_version, number, priority, name, polygon_json, area_square_nm, centroid_latitude, centroid_longitude, coverage_basis_points, claimed_status, version)
VALUES(20, 10, 12, 1, 1, 1, 'S-1', '[]', 1, 1, 2, 0, 'open', 1)`, nil},
				{`INSERT INTO vessels(id, vessel_no, speed_milliknots, endurance_seconds, max_operation_millinautical_miles, online_status, active_load, version, created_at, updated_at)
VALUES(30, 'V-1', 1000, 3600, 10000, 'online', 0, 1, ?, ?)`, []any{now, now}},
				{`INSERT INTO assignments(id, task_id, sector_id, sector_number, sector_set_version, vessel_id, start_at, end_at, score, status, version, created_at, updated_at)
VALUES(40, 10, 20, 1, 1, 30, ?, ?, 100, 'confirmed', 1, ?, ?)`, []any{now, now, now, now}},
			}
			for _, statement := range seed {
				if _, err := handle.Exec(ctx, statement.query, statement.args...); err != nil {
					t.Fatalf("seed database: %v", err)
				}
			}
			if tt.rejectTimelineEvent {
				_, err := handle.Exec(ctx, `CREATE TRIGGER reject_assignment_claimed
BEFORE INSERT ON task_events WHEN NEW.event_type = 'assignment_claimed'
BEGIN SELECT RAISE(ABORT, 'forced timeline failure'); END`)
				if err != nil {
					t.Fatalf("install failure trigger: %v", err)
				}
			}

			clock := domain.FixedClock{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
			timeline := audit.NewPersistentTimeline(handle)
			service := execution.NewPersistentService(clock, timeline, handle)
			_, claimErr := service.Claim(40, 30, 1, domain.Actor{ID: "operator-1", Role: domain.RoleOperator})
			if (claimErr != nil) != tt.wantError {
				t.Fatalf("Claim() error = %v, wantError %v", claimErr, tt.wantError)
			}

			var assignmentStatus string
			var assignmentVersion int64
			if err := handle.QueryRow(ctx, "SELECT status, version FROM assignments WHERE id = 40").Scan(&assignmentStatus, &assignmentVersion); err != nil {
				t.Fatalf("read assignment: %v", err)
			}
			if assignmentStatus != tt.wantAssignmentStatus || assignmentVersion != tt.wantAssignmentVersion {
				t.Errorf("assignment state = (%s, %d), want (%s, %d)", assignmentStatus, assignmentVersion, tt.wantAssignmentStatus, tt.wantAssignmentVersion)
			}

			var sectorStatus string
			var sectorVersion int64
			if err := handle.QueryRow(ctx, "SELECT claimed_status, version FROM search_sectors WHERE id = 20").Scan(&sectorStatus, &sectorVersion); err != nil {
				t.Fatalf("read sector: %v", err)
			}
			if sectorStatus != tt.wantSectorStatus || sectorVersion != tt.wantSectorVersion {
				t.Errorf("sector state = (%s, %d), want (%s, %d)", sectorStatus, sectorVersion, tt.wantSectorStatus, tt.wantSectorVersion)
			}

			var eventSequence int64
			if err := handle.QueryRow(ctx, "SELECT event_sequence FROM search_tasks WHERE id = 10").Scan(&eventSequence); err != nil {
				t.Fatalf("read task sequence: %v", err)
			}
			var claimedEvents int
			if err := handle.QueryRow(ctx, "SELECT COUNT(*) FROM task_events WHERE task_id = 10 AND event_type = 'assignment_claimed'").Scan(&claimedEvents); err != nil {
				t.Fatalf("count claim events: %v", err)
			}
			if eventSequence != tt.wantEventSequence || claimedEvents != tt.wantClaimedEvents {
				t.Errorf("audit state = (sequence %d, claimed events %d), want (%d, %d)", eventSequence, claimedEvents, tt.wantEventSequence, tt.wantClaimedEvents)
			}
		})
	}
}
