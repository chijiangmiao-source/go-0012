package domain

import "errors"

const (
	CodeValidation          = "validation_failed"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeStaleVersion        = "stale_version"
	CodeInvalidTransition   = "invalid_transition"
	CodeActiveTaskExists    = "active_task_exists"
	CodeSectorClaimed       = "sector_claimed"
	CodeIdempotencyMismatch = "idempotency_mismatch"
	CodeScheduleOverlap     = "schedule_overlap"
	CodeStorageUnavailable  = "storage_unavailable"
)

type AppError struct {
	Code           string         `json:"code"`
	Message        string         `json:"message"`
	Details        map[string]any `json:"details,omitempty"`
	CurrentVersion *int64         `json:"current_version,omitempty"`
}

func (e *AppError) Error() string {
	return e.Code + ": " + e.Message
}

func NewError(code, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

func VersionError(code, message string, current int64) *AppError {
	return &AppError{Code: code, Message: message, CurrentVersion: &current}
}

func IsCode(err error, code string) bool {
	var app *AppError
	return errors.As(err, &app) && app.Code == code
}
