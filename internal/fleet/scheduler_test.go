package fleet_test

import (
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/fleet"
)

func TestSchedulerStableTieBreakByVesselNo(t *testing.T) {
	sector := drift.Sector{Number: 2, Priority: 1, Centroid: domain.Position{Latitude: 30, Longitude: 122}, AreaSquareNM: 1}
	vessels := []fleet.Vessel{
		{ID: 2, VesselNo: "VES-B", Position: sector.Centroid, SpeedKnots: 10, EnduranceSeconds: 36000, MaxOperationNauticalMiles: 100, Online: true},
		{ID: 1, VesselNo: "VES-A", Position: sector.Centroid, SpeedKnots: 10, EnduranceSeconds: 36000, MaxOperationNauticalMiles: 100, Online: true},
	}
	plan := fleet.NewScheduler().Generate(9, 3, []drift.Sector{sector}, vessels, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(plan.Assignments) != 1 {
		t.Fatalf("assignments = %d", len(plan.Assignments))
	}
	if plan.Assignments[0].VesselNo != "VES-A" {
		t.Fatalf("chosen vessel = %s, want VES-A", plan.Assignments[0].VesselNo)
	}
}

func TestSchedulerExcludesIncapableVessels(t *testing.T) {
	sector := drift.Sector{Number: 1, Priority: 1, Centroid: domain.Position{Latitude: 30, Longitude: 122}, AreaSquareNM: 500}
	vessels := []fleet.Vessel{
		{ID: 1, VesselNo: "OFF", SpeedKnots: 10, EnduranceSeconds: 36000, MaxOperationNauticalMiles: 100, Online: false},
		{ID: 2, VesselNo: "FAR", Position: domain.Position{Latitude: 30, Longitude: 120}, SpeedKnots: 10, EnduranceSeconds: 36000, MaxOperationNauticalMiles: 10, Online: true},
		{ID: 3, VesselNo: "LOW", Position: sector.Centroid, SpeedKnots: 10, EnduranceSeconds: 10, MaxOperationNauticalMiles: 100, Online: true},
	}
	plan := fleet.NewScheduler().Generate(9, 3, []drift.Sector{sector}, vessels, time.Now())
	if len(plan.Assignments) != 0 {
		t.Fatalf("assignments = %d, want 0", len(plan.Assignments))
	}
	if plan.UnassignableReasons[1] == "" {
		t.Fatal("expected an unassignable reason for sector 1")
	}
}

func TestAssignmentOverlapUsesLeftClosedRightOpenIntervals(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	existing := []fleet.Assignment{{VesselID: 1, StartAt: base, EndAt: base.Add(time.Hour)}}
	touching := fleet.Assignment{VesselID: 1, StartAt: base.Add(time.Hour), EndAt: base.Add(2 * time.Hour)}
	if err := fleet.ConfirmNoOverlap(touching, existing); err != nil {
		t.Fatalf("touching intervals should not overlap: %v", err)
	}
	overlap := fleet.Assignment{VesselID: 1, StartAt: base.Add(30 * time.Minute), EndAt: base.Add(90 * time.Minute)}
	if err := fleet.ConfirmNoOverlap(overlap, existing); !domain.IsCode(err, domain.CodeScheduleOverlap) {
		t.Fatalf("overlap error = %v, want schedule_overlap", err)
	}
}
