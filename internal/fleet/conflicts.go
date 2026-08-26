package fleet

import (
	"math"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
)

func Overlaps(start, end, otherStart, otherEnd time.Time) bool {
	return start.Before(otherEnd) && otherStart.Before(end)
}

func ConfirmNoOverlap(candidate Assignment, existing []Assignment) error {
	for _, assignment := range existing {
		if assignment.VesselID == candidate.VesselID && Overlaps(candidate.StartAt, candidate.EndAt, assignment.StartAt, assignment.EndAt) {
			return domain.NewError(domain.CodeScheduleOverlap, "assignment time interval overlaps an existing assignment")
		}
	}
	return nil
}

func HaversineNM(a, b domain.Position) float64 {
	const earthRadiusNM = 3440.065
	lat1 := a.Latitude * math.Pi / 180
	lat2 := b.Latitude * math.Pi / 180
	dLat := (b.Latitude - a.Latitude) * math.Pi / 180
	dLon := (b.Longitude - a.Longitude) * math.Pi / 180
	sinLat := math.Sin(dLat / 2)
	sinLon := math.Sin(dLon / 2)
	h := sinLat*sinLat + math.Cos(lat1)*math.Cos(lat2)*sinLon*sinLon
	return 2 * earthRadiusNM * math.Asin(math.Sqrt(h))
}
