package fleet

import (
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
)

type Vessel struct {
	ID                        int64           `json:"id"`
	VesselNo                  string          `json:"vessel_no"`
	Position                  domain.Position `json:"position"`
	PositionAt                time.Time       `json:"position_at"`
	SpeedKnots                float64         `json:"speed_knots"`
	EnduranceSeconds          int64           `json:"endurance_seconds"`
	MaxOperationNauticalMiles float64         `json:"max_operation_nautical_miles"`
	Online                    bool            `json:"online"`
	LastHeartbeatAt           time.Time       `json:"last_heartbeat_at"`
	ActiveLoad                int             `json:"active_load"`
	Version                   int64           `json:"version"`
}

type Assignment struct {
	SectorNumber int       `json:"sector_number"`
	VesselID     int64     `json:"vessel_id"`
	VesselNo     string    `json:"vessel_no"`
	StartAt      time.Time `json:"start_at"`
	EndAt        time.Time `json:"end_at"`
	Score        int64     `json:"score"`
}

type Plan struct {
	TaskID              int64          `json:"task_id"`
	SectorSetVersion    int64          `json:"sector_set_version"`
	GeneratedAt         time.Time      `json:"generated_at"`
	Assignments         []Assignment   `json:"assignments"`
	UnassignableReasons map[int]string `json:"unassignable_reasons,omitempty"`
}
