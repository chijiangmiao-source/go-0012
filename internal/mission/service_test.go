package mission_test

import (
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/mission"
)

func newMissionFixture(t *testing.T) (*mission.Service, *audit.Timeline, mission.Buoy, domain.Actor) {
	t.Helper()
	timeline := audit.NewTimeline()
	clock := domain.FixedClock{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	service := mission.NewService(clock, timeline)
	actor := domain.Actor{ID: "commander-1", Role: domain.RoleCommander}
	buoy, err := service.CreateBuoy(mission.Buoy{
		BuoyNo:              "B-001",
		DeviceType:          "ais-buoy",
		LastCommunicationAt: clock.Now().Add(-time.Hour),
		LastPosition:        domain.Position{Latitude: 30, Longitude: 122},
		BatteryBasisPoints:  7500,
		LostReason:          "no_signal",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, timeline, buoy, actor
}

func TestActiveTaskConstraintEndsAtTerminalStatus(t *testing.T) {
	service, _, buoy, actor := newMissionFixture(t)
	first, err := service.CreateTask(buoy.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(buoy.ID, actor); !domain.IsCode(err, domain.CodeActiveTaskExists) {
		t.Fatalf("second active task error = %v, want %s", err, domain.CodeActiveTaskExists)
	}
	if _, err := service.Transition(first.ID, mission.TerminateRequest(first.Version, "weather_closed", actor)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(buoy.ID, actor); err != nil {
		t.Fatalf("new task after terminal status: %v", err)
	}
}

func TestLegalStatusPathAndTerminalCannotLeave(t *testing.T) {
	service, _, buoy, actor := newMissionFixture(t)
	task, err := service.CreateTask(buoy.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []mission.TaskStatus{mission.StatusPending, mission.StatusSearching, mission.StatusPaused, mission.StatusSearching} {
		task, err = service.Transition(task.ID, mission.TransitionRequest{ExpectedVersion: task.Version, TargetStatus: next, Actor: actor})
		if err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	foundAt := time.Date(2026, 1, 2, 5, 0, 0, 0, time.UTC)
	task, err = service.Transition(task.ID, mission.FoundRequest(task.Version, foundAt, domain.Position{Latitude: 30.1, Longitude: 122.1}, actor))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(task.ID, mission.TransitionRequest{ExpectedVersion: task.Version, TargetStatus: mission.StatusSearching, Actor: actor}); !domain.IsCode(err, domain.CodeInvalidTransition) {
		t.Fatalf("terminal transition error = %v, want %s", err, domain.CodeInvalidTransition)
	}
}

func TestInvalidTransitionAuditedWithoutVersionChange(t *testing.T) {
	service, timeline, buoy, actor := newMissionFixture(t)
	task, err := service.CreateTask(buoy.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(task.ID, mission.TransitionRequest{ExpectedVersion: task.Version, TargetStatus: mission.StatusSearching, Actor: actor}); !domain.IsCode(err, domain.CodeInvalidTransition) {
		t.Fatalf("transition error = %v, want %s", err, domain.CodeInvalidTransition)
	}
	stored, err := service.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != task.Version {
		t.Fatalf("version changed on rejected transition: got %d want %d", stored.Version, task.Version)
	}
	events := timeline.List(task.ID, audit.EventTransitionRejected)
	if len(events) != 1 {
		t.Fatalf("rejected events = %d, want 1", len(events))
	}
}

func TestStaleTaskVersionRejected(t *testing.T) {
	service, _, buoy, actor := newMissionFixture(t)
	task, err := service.CreateTask(buoy.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(task.ID, mission.TransitionRequest{ExpectedVersion: task.Version, TargetStatus: mission.StatusPending, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(task.ID, mission.TerminateRequest(task.Version, "old_client", actor)); !domain.IsCode(err, domain.CodeStaleVersion) {
		t.Fatalf("stale transition error = %v, want %s", err, domain.CodeStaleVersion)
	}
}
