package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
)

const AlgorithmFanV1 = "fan-v1"

type Input struct {
	TaskID              int64           `json:"task_id"`
	SnapshotID          int64           `json:"snapshot_id"`
	SectorSetVersion    int64           `json:"sector_set_version"`
	LastCommunicationAt time.Time       `json:"last_communication_at"`
	EffectiveAt         time.Time       `json:"effective_at"`
	LastPosition        domain.Position `json:"last_position"`
	DirectionDegrees    float64         `json:"direction_degrees"`
	SpeedKnots          float64         `json:"speed_knots"`
	UncertaintyNM       float64         `json:"uncertainty_nm"`
}

type SectorSet struct {
	TaskID            int64           `json:"task_id"`
	SnapshotID        int64           `json:"snapshot_id"`
	Version           int64           `json:"version"`
	Algorithm         string          `json:"algorithm"`
	InputDigest       string          `json:"input_digest"`
	PredictedCenter   domain.Position `json:"predicted_center"`
	DriftDistanceNM   float64         `json:"drift_distance_nm"`
	EffectiveRadiusNM float64         `json:"effective_radius_nm"`
	Sectors           []Sector        `json:"sectors"`
}

type Sector struct {
	Number              int               `json:"number"`
	Priority            int               `json:"priority"`
	Name                string            `json:"name"`
	Polygon             []domain.Position `json:"polygon"`
	AreaSquareNM        float64           `json:"area_square_nm"`
	Centroid            domain.Position   `json:"centroid"`
	CoverageBasisPoints int               `json:"coverage_basis_points"`
	Version             int64             `json:"version"`
}

type Engine struct{}

func NewEngine() Engine {
	return Engine{}
}

func (Engine) Generate(input Input) (SectorSet, error) {
	if input.SectorSetVersion <= 0 {
		input.SectorSetVersion = 1
	}
	if err := validate(input); err != nil {
		return SectorSet{}, err
	}

	last := domain.UTC(input.LastCommunicationAt)
	effective := domain.UTC(input.EffectiveAt)
	hours := effective.Sub(last).Hours()
	driftNM := input.SpeedKnots * hours
	directionRad := input.DirectionDegrees * math.Pi / 180
	northNM := driftNM * math.Cos(directionRad)
	eastNM := driftNM * math.Sin(directionRad)
	center := move(input.LastPosition, northNM, eastNM).Rounded6()
	radius := round6(input.UncertaintyNM + 0.1*driftNM)

	normalized := canonicalInput(input, center, driftNM, radius)
	raw, _ := json.Marshal(normalized)
	sum := sha256.Sum256(raw)

	set := SectorSet{
		TaskID:            input.TaskID,
		SnapshotID:        input.SnapshotID,
		Version:           input.SectorSetVersion,
		Algorithm:         AlgorithmFanV1,
		InputDigest:       hex.EncodeToString(sum[:]),
		PredictedCenter:   center,
		DriftDistanceNM:   round6(driftNM),
		EffectiveRadiusNM: radius,
		Sectors:           buildSectors(center, input.DirectionDegrees, radius),
	}
	return set, nil
}

func validate(input Input) error {
	if err := input.LastPosition.Validate(); err != nil {
		return err
	}
	if input.LastPosition.Latitude < -85 || input.LastPosition.Latitude > 85 {
		return domain.NewError(domain.CodeValidation, "fan-v1 input latitude must be within [-85,85]")
	}
	if !domain.UTC(input.EffectiveAt).After(domain.UTC(input.LastCommunicationAt)) {
		return domain.NewError(domain.CodeValidation, "snapshot effective time must be after last communication time")
	}
	if input.DirectionDegrees < 0 || input.DirectionDegrees >= 360 {
		return domain.NewError(domain.CodeValidation, "direction must be in [0,360)")
	}
	if input.SpeedKnots <= 0 {
		return domain.NewError(domain.CodeValidation, "speed must be positive")
	}
	if input.UncertaintyNM <= 0 {
		return domain.NewError(domain.CodeValidation, "uncertainty radius must be positive")
	}
	return nil
}

func canonicalInput(input Input, center domain.Position, driftNM, radiusNM float64) map[string]any {
	return map[string]any{
		"algorithm":              AlgorithmFanV1,
		"snapshot_id":            input.SnapshotID,
		"last_communication_at":  domain.UTC(input.LastCommunicationAt).Format(time.RFC3339Nano),
		"effective_at":           domain.UTC(input.EffectiveAt).Format(time.RFC3339Nano),
		"last_position":          input.LastPosition.Rounded6(),
		"direction_millidegrees": int64(math.Round(input.DirectionDegrees * 1000)),
		"speed_milliknots":       int64(math.Round(input.SpeedKnots * 1000)),
		"uncertainty_millinm":    int64(math.Round(input.UncertaintyNM * 1000)),
		"predicted_center":       center,
		"drift_nm":               round6(driftNM),
		"radius_nm":              radiusNM,
	}
}

func round6(v float64) float64 {
	return math.Round(v*1_000_000) / 1_000_000
}
