package execution_test

import (
	"sync"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/execution"
)

func newExecutionFixture() (*execution.Service, domain.Actor) {
	timeline := audit.NewTimeline()
	clock := domain.FixedClock{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	service := execution.NewService(clock, timeline)
	actor := domain.Actor{ID: "operator-1", Role: domain.RoleOperator}
	return service, actor
}

func TestProgressIdempotencyReturnsFirstResult(t *testing.T) {
	service, actor := newExecutionFixture()
	service.AddAssignment(execution.Assignment{ID: 1, TaskID: 10, SectorID: 20, VesselID: 30})
	first, err := service.ReportProgress(1, 30, 1, "key-1", 250, actor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReportProgress(1, 30, 1, "key-1", 250, actor)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || second.CoverageBasisPoints != 250 {
		t.Fatalf("idempotent results = %#v and %#v", first, second)
	}
}

func TestProgressIdempotencyMismatch(t *testing.T) {
	service, actor := newExecutionFixture()
	service.AddAssignment(execution.Assignment{ID: 1, TaskID: 10, SectorID: 20, VesselID: 30})
	if _, err := service.ReportProgress(1, 30, 1, "key-1", 250, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReportProgress(1, 30, 1, "key-1", 251, actor); !domain.IsCode(err, domain.CodeIdempotencyMismatch) {
		t.Fatalf("mismatch error = %v, want idempotency_mismatch", err)
	}
}

func TestConcurrentClaimAllowsExactlyOneWinner(t *testing.T) {
	service, actor := newExecutionFixture()
	service.AddAssignment(execution.Assignment{ID: 2, TaskID: 10, SectorID: 20, VesselID: 30})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Claim(2, 30, 1, actor)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	success := 0
	claimed := 0
	for err := range errs {
		if err == nil {
			success++
			continue
		}
		if domain.IsCode(err, domain.CodeSectorClaimed) {
			claimed++
		}
	}
	if success != 1 || claimed != 1 {
		t.Fatalf("success=%d sector_claimed=%d, want 1/1", success, claimed)
	}
}
