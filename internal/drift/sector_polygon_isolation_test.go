package drift_test

import (
	"reflect"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
)

func TestModel_FanV1SectorPolygonsAreIndependent(t *testing.T) {
	cases := []struct {
		name  string
		check func(*testing.T, drift.SectorSet)
	}{
		{
			name: "directions retain distinct geometry",
			check: func(t *testing.T, set drift.SectorSet) {
				for i := range set.Sectors {
					for j := i + 1; j < len(set.Sectors); j++ {
						if reflect.DeepEqual(set.Sectors[i].Polygon, set.Sectors[j].Polygon) {
							t.Fatalf("%s and %s unexpectedly have the same polygon: %#v", set.Sectors[i].Name, set.Sectors[j].Name, set.Sectors[i].Polygon)
						}
					}
				}
			},
		},
		{
			name: "mutation remains local to one sector",
			check: func(t *testing.T, set drift.SectorSet) {
				untouched := make([][]domain.Position, len(set.Sectors)-1)
				for i := 1; i < len(set.Sectors); i++ {
					untouched[i-1] = append([]domain.Position(nil), set.Sectors[i].Polygon...)
				}

				set.Sectors[0].Polygon[1].Latitude += 1
				set.Sectors[0].Polygon[1].Longitude -= 1

				for i := 1; i < len(set.Sectors); i++ {
					if !reflect.DeepEqual(set.Sectors[i].Polygon, untouched[i-1]) {
						t.Fatalf("mutating %s changed %s polygon: got %#v, want %#v", set.Sectors[0].Name, set.Sectors[i].Name, set.Sectors[i].Polygon, untouched[i-1])
					}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := drift.NewEngine().Generate(drift.Input{
				TaskID:              7,
				SnapshotID:          11,
				SectorSetVersion:    1,
				LastCommunicationAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				EffectiveAt:         time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC),
				LastPosition:        domain.Position{Latitude: 30, Longitude: 122},
				DirectionDegrees:    90,
				SpeedKnots:          2,
				UncertaintyNM:       1,
			})
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if len(set.Sectors) != 4 {
				t.Fatalf("sector count = %d, want 4", len(set.Sectors))
			}
			tc.check(t, set)
		})
	}
}
