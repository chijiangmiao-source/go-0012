package api_test

import (
	"context"
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

func TestHealthReadyAndRoleBoundary(t *testing.T) {
	handle, err := store.Open(context.Background(), store.Config{SQLitePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	timeline := audit.NewTimeline()
	clock := domain.FixedClock{At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	server := api.NewServer(api.Dependencies{
		Store:         handle,
		Missions:      mission.NewService(clock, timeline),
		Drift:         drift.NewEngine(),
		Scheduler:     fleet.NewScheduler(),
		Execution:     execution.NewService(clock, timeline),
		Timeline:      timeline,
		Notifications: audit.NewNotificationCenter(),
		Replans:       execution.NewReplanStore(),
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
	req.Header.Set("X-Actor-ID", "operator-1")
	req.Header.Set("X-Role", string(domain.RoleOperator))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator task list status = %d, want 403", rec.Code)
	}
}
