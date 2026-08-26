package mission

import "offshore-buoy-drift-search-loop/internal/domain"

func IsTerminal(status TaskStatus) bool {
	return status == StatusFound || status == StatusTerminated
}

func CanTransition(from, to TaskStatus) bool {
	allowed := map[TaskStatus][]TaskStatus{
		StatusDraft:     {StatusPending, StatusTerminated},
		StatusPending:   {StatusSearching, StatusTerminated},
		StatusSearching: {StatusPaused, StatusFound, StatusTerminated},
		StatusPaused:    {StatusSearching, StatusFound, StatusTerminated},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

func ValidateTransition(task Task, req TransitionRequest) error {
	if task.Version != req.ExpectedVersion {
		return domain.VersionError(domain.CodeStaleVersion, "task version does not match expected_version", task.Version)
	}
	if !CanTransition(task.Status, req.TargetStatus) {
		return domain.NewError(domain.CodeInvalidTransition, "task status transition is not allowed")
	}
	switch req.TargetStatus {
	case StatusTerminated:
		if req.TerminationReason == "" {
			return domain.NewError(domain.CodeValidation, "termination reason is required")
		}
	case StatusFound:
		if req.FoundAt == nil || req.FoundPosition == nil {
			return domain.NewError(domain.CodeValidation, "found time and position are required")
		}
		if err := req.FoundPosition.Validate(); err != nil {
			return err
		}
	}
	return nil
}
