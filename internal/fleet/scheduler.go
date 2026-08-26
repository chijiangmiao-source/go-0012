package fleet

import (
	"math"
	"sort"
	"time"

	"offshore-buoy-drift-search-loop/internal/drift"
)

type Scheduler struct{}

func NewScheduler() Scheduler {
	return Scheduler{}
}

func (Scheduler) Generate(taskID int64, sectorSetVersion int64, sectors []drift.Sector, vessels []Vessel, generatedAt time.Time) Plan {
	ordered := append([]drift.Sector(nil), sectors...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].Number < ordered[j].Number
		}
		return ordered[i].Priority < ordered[j].Priority
	})

	remaining := make(map[int64]int64, len(vessels))
	for _, vessel := range vessels {
		remaining[vessel.ID] = vessel.EnduranceSeconds
	}

	plan := Plan{
		TaskID:              taskID,
		SectorSetVersion:    sectorSetVersion,
		GeneratedAt:         generatedAt.UTC(),
		UnassignableReasons: make(map[int]string),
	}
	for _, sector := range ordered {
		candidate, ok, reason := chooseCandidate(sector, vessels, remaining)
		if !ok {
			plan.UnassignableReasons[sector.Number] = reason
			continue
		}
		distance := HaversineNM(candidate.Position, sector.Centroid)
		etaSeconds := int64(math.Ceil(distance / candidate.SpeedKnots * 3600))
		workSeconds := int64(math.Ceil(sector.AreaSquareNM / (candidate.SpeedKnots * 0.5) * 3600))
		start := generatedAt.UTC().Add(time.Duration(etaSeconds) * time.Second)
		end := start.Add(time.Duration(workSeconds) * time.Second)
		needed := etaSeconds*2 + workSeconds
		remaining[candidate.ID] -= needed
		plan.Assignments = append(plan.Assignments, Assignment{
			SectorNumber: sector.Number,
			VesselID:     candidate.ID,
			VesselNo:     candidate.VesselNo,
			StartAt:      start,
			EndAt:        end,
			Score:        score(candidate, etaSeconds, needed, remaining[candidate.ID]+needed),
		})
	}
	return plan
}

func chooseCandidate(sector drift.Sector, vessels []Vessel, remaining map[int64]int64) (Vessel, bool, string) {
	var best Vessel
	var bestScore int64
	var chosen bool
	var lastReason string
	for _, vessel := range vessels {
		etaSeconds, needed, reason, ok := capability(sector, vessel, remaining[vessel.ID])
		if !ok {
			lastReason = reason
			continue
		}
		candidateScore := score(vessel, etaSeconds, needed, remaining[vessel.ID])
		if !chosen || candidateScore < bestScore || candidateScore == bestScore && vessel.VesselNo < best.VesselNo {
			best = vessel
			bestScore = candidateScore
			chosen = true
		}
	}
	if !chosen && lastReason == "" {
		lastReason = "no_capable_vessel"
	}
	return best, chosen, lastReason
}

func capability(sector drift.Sector, vessel Vessel, remainingSeconds int64) (int64, int64, string, bool) {
	if !vessel.Online {
		return 0, 0, "vessel_offline", false
	}
	if vessel.SpeedKnots <= 0 || remainingSeconds <= 0 || vessel.MaxOperationNauticalMiles <= 0 {
		return 0, 0, "capability_incomplete", false
	}
	distance := HaversineNM(vessel.Position, sector.Centroid)
	if 2*distance > vessel.MaxOperationNauticalMiles {
		return 0, 0, "operation_distance_exceeded", false
	}
	etaSeconds := int64(math.Ceil(distance / vessel.SpeedKnots * 3600))
	workSeconds := int64(math.Ceil(sector.AreaSquareNM / (vessel.SpeedKnots * 0.5) * 3600))
	needed := etaSeconds*2 + workSeconds
	if needed > remainingSeconds {
		return 0, 0, "endurance_insufficient", false
	}
	return etaSeconds, needed, "", true
}

func score(vessel Vessel, etaSeconds, neededSeconds, remainingSeconds int64) int64 {
	endurance := remainingSeconds
	if endurance <= 0 {
		endurance = 1
	}
	return etaSeconds + int64(vessel.ActiveLoad)*1800 + int64(math.Ceil(3600*float64(neededSeconds)/float64(endurance)))
}
