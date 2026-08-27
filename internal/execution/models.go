package execution

import "time"

type AssignmentStatus string

const (
	AssignmentConfirmed AssignmentStatus = "confirmed"
	AssignmentClaimed   AssignmentStatus = "claimed"
	AssignmentExecuting AssignmentStatus = "executing"
	AssignmentClosed    AssignmentStatus = "closed"
)

type Assignment struct {
	ID                  int64            `json:"id"`
	TaskID              int64            `json:"task_id"`
	SectorID            int64            `json:"sector_id"`
	SectorSetVersion    int64            `json:"sector_set_version"`
	VesselID            int64            `json:"vessel_id"`
	Status              AssignmentStatus `json:"status"`
	CoverageBasisPoints int              `json:"coverage_basis_points"`
	Version             int64            `json:"version"`
	ClaimedAt           *time.Time       `json:"claimed_at,omitempty"`
}

type ProgressResult struct {
	AssignmentID        int64 `json:"assignment_id"`
	CoverageBasisPoints int   `json:"coverage_basis_points"`
	Version             int64 `json:"version"`
}
