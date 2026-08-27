package api

import (
	"encoding/json"
	"net/http"

	"offshore-buoy-drift-search-loop/internal/domain"
)

type Response struct {
	Data      any    `json:"data,omitempty"`
	Version   int64  `json:"version,omitempty"`
	RequestID string `json:"request_id"`
}

type ErrorResponse struct {
	Code           string         `json:"code"`
	Message        string         `json:"message"`
	Details        map[string]any `json:"details,omitempty"`
	CurrentVersion *int64         `json:"current_version,omitempty"`
	RequestID      string         `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	response := ErrorResponse{Code: "internal_error", Message: "unexpected error", RequestID: requestID(r)}
	if app, ok := err.(*domain.AppError); ok {
		response.Code = app.Code
		response.Message = app.Message
		response.Details = app.Details
		response.CurrentVersion = app.CurrentVersion
		status = statusFor(app.Code)
	}
	writeJSON(w, status, response)
}

func statusFor(code string) int {
	switch code {
	case domain.CodeValidation:
		return http.StatusBadRequest
	case domain.CodeForbidden:
		return http.StatusForbidden
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeStaleVersion, domain.CodeInvalidTransition, domain.CodeActiveTaskExists, domain.CodeSectorClaimed, domain.CodeIdempotencyMismatch, domain.CodeScheduleOverlap:
		return http.StatusConflict
	case domain.CodeStorageUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-ID"); id != "" {
		return id
	}
	return "request-local"
}
