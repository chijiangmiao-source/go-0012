package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/api"
	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/mission"
	"offshore-buoy-drift-search-loop/internal/store"
)

func TestModel_TransitionPersistenceIsAtomic(t *testing.T) {
	cases := []struct {
		name              string
		rejectEventInsert bool
		wantHTTPStatus    int
		wantTaskStatus    mission.TaskStatus
		wantTaskVersion   int64
		wantEventSequence int64
		wantEventTypes    []string
	}{
		{
			name:              "event append failure rolls back task state version and sequence",
			rejectEventInsert: true,
			wantHTTPStatus:    http.StatusInternalServerError,
			wantTaskStatus:    mission.StatusDraft,
			wantTaskVersion:   1,
			wantEventSequence: 1,
			wantEventTypes:    []string{audit.EventTaskCreated},
		},
		{
			name:              "success commits incremented version and one continuous transition event",
			wantHTTPStatus:    http.StatusOK,
			wantTaskStatus:    mission.StatusPending,
			wantTaskVersion:   2,
			wantEventSequence: 2,
			wantEventTypes:    []string{audit.EventTaskCreated, audit.EventTaskTransitioned},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "acceptance.db")
			handle, err := store.Open(ctx, store.Config{SQLitePath: dbPath})
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = handle.Close() })

			clock := domain.FixedClock{At: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}
			timeline := audit.NewPersistentTimeline(handle)
			missions := mission.NewPersistentService(clock, timeline, handle)
			actor := domain.Actor{ID: "commander-atomic", Role: domain.RoleCommander}
			buoy, err := missions.CreateBuoy(mission.Buoy{
				BuoyNo:              "ATOMIC-001",
				DeviceType:          "ais-buoy",
				LastCommunicationAt: clock.Now().Add(-time.Hour),
				LastPosition:        domain.Position{Latitude: 30, Longitude: 122},
				BatteryBasisPoints:  8000,
				LostReason:          "no_signal",
			})
			if err != nil {
				t.Fatalf("create buoy: %v", err)
			}
			task, err := missions.CreateTask(buoy.ID, actor)
			if err != nil {
				t.Fatalf("create task: %v", err)
			}

			if tc.rejectEventInsert {
				_, err = handle.DB().ExecContext(ctx, `CREATE TRIGGER reject_transition_event
					BEFORE INSERT ON task_events
					WHEN NEW.event_type = 'task_transitioned'
					BEGIN
						SELECT RAISE(ABORT, 'injected transition audit failure');
					END`)
				if err != nil {
					t.Fatalf("install audit failure trigger: %v", err)
				}
			}

			server := api.NewServer(api.Dependencies{
				Store:    handle,
				Missions: missions,
				Timeline: timeline,
			})
			body := bytes.NewBufferString(fmt.Sprintf(`{"expected_version":%d,"target_status":"pending_schedule"}`, task.Version))
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/tasks/%d/transitions", task.ID), body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Actor-ID", actor.ID)
			req.Header.Set("X-Role", string(actor.Role))
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != tc.wantHTTPStatus {
				t.Fatalf("transition status = %d, want %d; body=%s", rec.Code, tc.wantHTTPStatus, rec.Body.String())
			}

			if rec.Code == http.StatusOK {
				var response struct {
					Data    mission.Task `json:"data"`
					Version int64        `json:"version"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode transition response: %v", err)
				}
				if response.Version != tc.wantTaskVersion || response.Data.Version != tc.wantTaskVersion {
					t.Fatalf("response versions = header %d, task %d; want %d", response.Version, response.Data.Version, tc.wantTaskVersion)
				}
			}

			stored, err := missions.GetTask(task.ID)
			if err != nil {
				t.Fatalf("reload task: %v", err)
			}
			if stored.Status != tc.wantTaskStatus || stored.Version != tc.wantTaskVersion {
				t.Fatalf("stored task = status %q version %d, want status %q version %d", stored.Status, stored.Version, tc.wantTaskStatus, tc.wantTaskVersion)
			}

			var sequence int64
			if err := handle.QueryRow(ctx, "SELECT event_sequence FROM search_tasks WHERE id = ?", task.ID).Scan(&sequence); err != nil {
				t.Fatalf("read event sequence: %v", err)
			}
			if sequence != tc.wantEventSequence {
				t.Fatalf("event_sequence = %d, want %d", sequence, tc.wantEventSequence)
			}

			events, err := timeline.ListPersistent(ctx, audit.EventFilter{TaskID: task.ID, Page: 1, PageSize: 20})
			if err != nil {
				t.Fatalf("list events: %v", err)
			}
			if len(events) != len(tc.wantEventTypes) {
				t.Fatalf("event count = %d, want %d", len(events), len(tc.wantEventTypes))
			}
			for i, event := range events {
				if event.Sequence != int64(i+1) || event.Type != tc.wantEventTypes[i] {
					t.Fatalf("event %d = sequence %d type %q, want sequence %d type %q", i, event.Sequence, event.Type, i+1, tc.wantEventTypes[i])
				}
			}
		})
	}
}
