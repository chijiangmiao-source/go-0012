package execution

import "sync"

type ReplanCause string

const (
	ReplanPredictionChanged ReplanCause = "prediction_changed"
	ReplanVesselOffline     ReplanCause = "vessel_offline"
	ReplanEnduranceLow      ReplanCause = "endurance_low"
	ReplanNoHandoffReceiver ReplanCause = "handoff_no_receiver"
)

type ReplanSuggestion struct {
	TaskID               int64       `json:"task_id"`
	Cause                ReplanCause `json:"cause"`
	VesselID             int64       `json:"vessel_id,omitempty"`
	AssignmentID         int64       `json:"assignment_id,omitempty"`
	FromSectorSetVersion int64       `json:"from_sector_set_version,omitempty"`
	ToSectorSetVersion   int64       `json:"to_sector_set_version,omitempty"`
	DedupeKey            string      `json:"dedupe_key"`
	Resolved             bool        `json:"resolved"`
}

type ReplanStore struct {
	mu          sync.Mutex
	suggestions map[string]ReplanSuggestion
}

func NewReplanStore() *ReplanStore {
	return &ReplanStore{suggestions: make(map[string]ReplanSuggestion)}
}

func (s *ReplanStore) Suggest(input ReplanSuggestion) ReplanSuggestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.DedupeKey == "" {
		input.DedupeKey = input.Cause.Key(input.TaskID, input.VesselID, input.AssignmentID, input.ToSectorSetVersion)
	}
	if existing, ok := s.suggestions[input.DedupeKey]; ok && !existing.Resolved {
		return existing
	}
	s.suggestions[input.DedupeKey] = input
	return input
}

func (s *ReplanStore) List() []ReplanSuggestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReplanSuggestion, 0, len(s.suggestions))
	for _, suggestion := range s.suggestions {
		out = append(out, suggestion)
	}
	return out
}

func (c ReplanCause) Key(taskID, vesselID, assignmentID, version int64) string {
	return string(c) + ":" + jsonNumber(taskID) + ":" + jsonNumber(vesselID) + ":" + jsonNumber(assignmentID) + ":" + jsonNumber(version)
}
