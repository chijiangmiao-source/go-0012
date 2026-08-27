package drift

import (
	"math"

	"offshore-buoy-drift-search-loop/internal/domain"
)

var sectorDefinitions = []struct {
	name   string
	offset float64
}{
	{name: "downstream", offset: -45},
	{name: "clockwise", offset: 45},
	{name: "counterclockwise", offset: -135},
	{name: "upstream", offset: 135},
}

func buildSectors(center domain.Position, direction, radiusNM float64) []Sector {
	sectors := make([]Sector, 0, 4)
	for i, def := range sectorDefinitions {
		start := direction + def.offset
		ring := []domain.Position{center}
		for step := 0; step <= 90; step += 15 {
			ring = append(ring, bearingPoint(center, start+float64(step), radiusNM).Rounded6())
		}
		ring = append(ring, center)
		sectors = append(sectors, Sector{
			Number:       i + 1,
			Priority:     i + 1,
			Name:         def.name,
			Polygon:      ring,
			AreaSquareNM: round6(math.Pi * radiusNM * radiusNM / 4),
			Centroid:     centroid(ring).Rounded6(),
			Version:      1,
		})
	}
	return sectors
}

func move(origin domain.Position, northNM, eastNM float64) domain.Position {
	lat := origin.Latitude + northNM/60
	lon := origin.Longitude + eastNM/(60*math.Cos(origin.Latitude*math.Pi/180))
	return domain.Position{Latitude: lat, Longitude: lon}
}

func bearingPoint(origin domain.Position, bearingDegrees, distanceNM float64) domain.Position {
	rad := bearingDegrees * math.Pi / 180
	north := distanceNM * math.Cos(rad)
	east := distanceNM * math.Sin(rad)
	return move(origin, north, east)
}

func centroid(points []domain.Position) domain.Position {
	var lat float64
	var lon float64
	for _, point := range points {
		lat += point.Latitude
		lon += point.Longitude
	}
	count := float64(len(points))
	return domain.Position{Latitude: lat / count, Longitude: lon / count}
}
