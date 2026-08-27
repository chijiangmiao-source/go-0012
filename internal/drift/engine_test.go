package drift_test

import (
	"reflect"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
)

func testInput(version int64) drift.Input {
	return drift.Input{
		TaskID:              7,
		SnapshotID:          11,
		SectorSetVersion:    version,
		LastCommunicationAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EffectiveAt:         time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC),
		LastPosition:        domain.Position{Latitude: 30, Longitude: 122},
		DirectionDegrees:    90,
		SpeedKnots:          2,
		UncertaintyNM:       1,
	}
}

func TestFanV1DeterministicFixture(t *testing.T) {
	set, err := drift.NewEngine().Generate(testInput(1))
	if err != nil {
		t.Fatal(err)
	}
	if set.Algorithm != drift.AlgorithmFanV1 {
		t.Fatalf("algorithm = %s", set.Algorithm)
	}
	if set.PredictedCenter != (domain.Position{Latitude: 30, Longitude: 122.07698}) {
		t.Fatalf("center = %#v", set.PredictedCenter)
	}
	if set.DriftDistanceNM != 4 || set.EffectiveRadiusNM != 1.4 {
		t.Fatalf("drift/radius = %v/%v", set.DriftDistanceNM, set.EffectiveRadiusNM)
	}
	if len(set.Sectors) != 4 {
		t.Fatalf("sector count = %d", len(set.Sectors))
	}
	for i, sector := range set.Sectors {
		if sector.Number != i+1 || sector.Priority != i+1 {
			t.Fatalf("sector ordering = number %d priority %d at %d", sector.Number, sector.Priority, i)
		}
		if len(sector.Polygon) != 9 {
			t.Fatalf("sector %d polygon points = %d", sector.Number, len(sector.Polygon))
		}
	}
}

func TestRepeatedGenerationKeepsDigestAndGeometryWithNewVersion(t *testing.T) {
	first, err := drift.NewEngine().Generate(testInput(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := drift.NewEngine().Generate(testInput(2))
	if err != nil {
		t.Fatal(err)
	}
	if first.Version == second.Version {
		t.Fatalf("versions should differ: %d", first.Version)
	}
	if first.InputDigest != second.InputDigest {
		t.Fatalf("digest changed: %s != %s", first.InputDigest, second.InputDigest)
	}
	if !reflect.DeepEqual(first.Sectors, second.Sectors) {
		t.Fatal("geometry changed for identical input")
	}
}

func TestFanV1RejectsInvalidInputs(t *testing.T) {
	cases := map[string]func(drift.Input) drift.Input{
		"early_effective": func(in drift.Input) drift.Input {
			in.EffectiveAt = in.LastCommunicationAt.Add(-time.Second)
			return in
		},
		"polar_latitude": func(in drift.Input) drift.Input {
			in.LastPosition.Latitude = 86
			return in
		},
		"bad_direction": func(in drift.Input) drift.Input {
			in.DirectionDegrees = 360
			return in
		},
		"zero_speed": func(in drift.Input) drift.Input {
			in.SpeedKnots = 0
			return in
		},
		"zero_uncertainty": func(in drift.Input) drift.Input {
			in.UncertaintyNM = 0
			return in
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := drift.NewEngine().Generate(mutate(testInput(1))); !domain.IsCode(err, domain.CodeValidation) {
				t.Fatalf("error = %v, want validation_failed", err)
			}
		})
	}
}
