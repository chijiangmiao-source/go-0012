package domain

import (
	"math"
	"time"
)

type Role string

const (
	RoleCommander Role = "commander"
	RoleOperator  Role = "operator"
	RoleAuditor   Role = "auditor"
)

const (
	ActionMaintainMission    = "maintain_mission"
	ActionOperateExecution   = "operate_execution"
	ActionReadAudit          = "read_audit"
	ActionHandleNotification = "handle_notification"
)

func (r Role) Can(action string) bool {
	switch action {
	case ActionMaintainMission:
		return r == RoleCommander
	case ActionOperateExecution:
		return r == RoleOperator
	case ActionReadAudit:
		return r == RoleCommander || r == RoleAuditor
	case ActionHandleNotification:
		return r == RoleCommander || r == RoleOperator || r == RoleAuditor
	default:
		return false
	}
}

type Actor struct {
	ID   string `json:"actor_id"`
	Role Role   `json:"role"`
}

type Position struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (p Position) Validate() error {
	if p.Latitude < -90 || p.Latitude > 90 {
		return NewError(CodeValidation, "latitude must be between -90 and 90")
	}
	if p.Longitude < -180 || p.Longitude > 180 {
		return NewError(CodeValidation, "longitude must be between -180 and 180")
	}
	return nil
}

func (p Position) Rounded6() Position {
	return Position{
		Latitude:  math.Round(p.Latitude*1_000_000) / 1_000_000,
		Longitude: math.Round(p.Longitude*1_000_000) / 1_000_000,
	}
}

func UTC(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}
