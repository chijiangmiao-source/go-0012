package mission

import (
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
)

type TaskStatus string

const (
	StatusDraft      TaskStatus = "draft"
	StatusPending    TaskStatus = "pending_schedule"
	StatusSearching  TaskStatus = "searching"
	StatusPaused     TaskStatus = "paused"
	StatusFound      TaskStatus = "found"
	StatusTerminated TaskStatus = "terminated"
)

type Buoy struct {
	ID                  int64           `json:"id"`
	BuoyNo              string          `json:"buoy_no"`
	DeviceType          string          `json:"device_type"`
	LastCommunicationAt time.Time       `json:"last_communication_at"`
	LastPosition        domain.Position `json:"last_position"`
	BatteryBasisPoints  int             `json:"battery_basis_points"`
	LostReason          string          `json:"lost_reason"`
	Version             int64           `json:"version"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type Task struct {
	ID                     int64            `json:"id"`
	BuoyID                 int64            `json:"buoy_id"`
	Status                 TaskStatus       `json:"status"`
	SubmittedAt            *time.Time       `json:"submitted_at,omitempty"`
	FoundAt                *time.Time       `json:"found_at,omitempty"`
	FoundPosition          *domain.Position `json:"found_position,omitempty"`
	TerminationReason      string           `json:"termination_reason,omitempty"`
	ActiveSectorSetVersion int64            `json:"active_sector_set_version,omitempty"`
	Version                int64            `json:"version"`
	CreatedBy              string           `json:"created_by"`
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

type TransitionRequest struct {
	ExpectedVersion   int64            `json:"expected_version"`
	TargetStatus      TaskStatus       `json:"target_status"`
	TerminationReason string           `json:"termination_reason,omitempty"`
	FoundAt           *time.Time       `json:"found_at,omitempty"`
	FoundPosition     *domain.Position `json:"found_position,omitempty"`
	Actor             domain.Actor     `json:"actor"`
}
