package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/store"
)

const (
	EventTaskCreated         = "task_created"
	EventTaskTransitioned    = "task_transitioned"
	EventTransitionRejected  = "transition_rejected"
	EventPredictionGenerated = "prediction_generated"
	EventAssignmentClaimed   = "assignment_claimed"
	EventProgressReported    = "progress_reported"
	EventPositionReported    = "position_reported"
	EventSectorEntered       = "sector_entered"
	EventSightingReported    = "sighting_reported"
	EventAssignmentExited    = "assignment_exited"
	EventHandoffRequested    = "handoff_requested"
	EventPlanConfirmed       = "schedule_plan_confirmed"
	EventPlanRejected        = "schedule_plan_rejected"
	EventNotificationCreated = "notification_created"
	EventConclusionRecorded  = "conclusion_recorded"
)

type EventInput struct {
	Type     string
	Actor    domain.Actor
	VesselID int64
	Occurred time.Time
	Payload  map[string]any
}

type Event struct {
	TaskID   int64          `json:"task_id"`
	Sequence int64          `json:"sequence"`
	Type     string         `json:"event_type"`
	Actor    domain.Actor   `json:"actor"`
	VesselID int64          `json:"vessel_id,omitempty"`
	Occurred time.Time      `json:"occurred_at"`
	Payload  map[string]any `json:"payload,omitempty"`
}

type Timeline struct {
	mu     sync.Mutex
	events map[int64][]Event
	db     *store.Handle
}

func NewTimeline() *Timeline {
	return &Timeline{events: make(map[int64][]Event)}
}

func NewPersistentTimeline(db *store.Handle) *Timeline {
	return &Timeline{events: make(map[int64][]Event), db: db}
}

func (t *Timeline) Append(taskID int64, input EventInput) (Event, error) {
	return t.AppendInTx(context.Background(), taskID, input)
}

// AppendInTx appends an event, joining the caller's transaction when ctx
// already carries one (see store.WithinTx). This keeps the audit event and
// the business write in the same commit, so a failed event append rolls back
// the business result and a replayed idempotent retry never re-appends.
func (t *Timeline) AppendInTx(ctx context.Context, taskID int64, input EventInput) (Event, error) {
	if t.db != nil {
		return t.appendPersistent(ctx, taskID, input)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	sequence := int64(len(t.events[taskID]) + 1)
	event := Event{
		TaskID:   taskID,
		Sequence: sequence,
		Type:     input.Type,
		Actor:    input.Actor,
		VesselID: input.VesselID,
		Occurred: domain.UTC(input.Occurred),
		Payload:  input.Payload,
	}
	t.events[taskID] = append(t.events[taskID], event)
	return event, nil
}

func (t *Timeline) List(taskID int64, eventType string) []Event {
	if t.db != nil {
		events, err := t.ListPersistent(context.Background(), EventFilter{TaskID: taskID, EventType: eventType, Page: 1, PageSize: 1000})
		if err == nil {
			return events
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	source := t.events[taskID]
	out := make([]Event, 0, len(source))
	for _, event := range source {
		if eventType == "" || event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

type EventFilter struct {
	TaskID    int64
	EventType string
	VesselID  int64
	From      *time.Time
	To        *time.Time
	Page      int
	PageSize  int
}

func (t *Timeline) appendPersistent(ctx context.Context, taskID int64, input EventInput) (Event, error) {
	var event Event
	err := t.db.WithinTx(ctx, func(txCtx context.Context) error {
		var seq int64
		if err := t.db.QueryRow(txCtx, "SELECT event_sequence + 1 FROM search_tasks WHERE id = ?", taskID).Scan(&seq); err != nil {
			if err == sql.ErrNoRows {
				return domain.NewError(domain.CodeNotFound, "task does not exist")
			}
			return err
		}
		raw, err := json.Marshal(input.Payload)
		if err != nil {
			return err
		}
		occurred := domain.UTC(input.Occurred)
		if occurred.IsZero() {
			occurred = time.Now().UTC()
		}
		if _, err := t.db.Exec(txCtx, "UPDATE search_tasks SET event_sequence = ? WHERE id = ?", seq, taskID); err != nil {
			return err
		}
		if _, err := t.db.Exec(txCtx, `INSERT INTO task_events(task_id, sequence, event_type, actor_id, actor_role, vessel_id, occurred_at, payload_json)
VALUES(?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?)`, taskID, seq, input.Type, input.Actor.ID, string(input.Actor.Role), input.VesselID, occurred.Format(time.RFC3339Nano), string(raw)); err != nil {
			return err
		}
		event = Event{TaskID: taskID, Sequence: seq, Type: input.Type, Actor: input.Actor, VesselID: input.VesselID, Occurred: occurred, Payload: input.Payload}
		return nil
	})
	return event, err
}

func (t *Timeline) ListPersistent(ctx context.Context, filter EventFilter) ([]Event, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 || filter.PageSize > 200 {
		filter.PageSize = 50
	}
	query := `SELECT task_id, sequence, event_type, actor_id, actor_role, COALESCE(vessel_id, 0), occurred_at, payload_json
FROM task_events WHERE task_id = ?`
	args := []any{filter.TaskID}
	if filter.EventType != "" {
		query += " AND event_type = ?"
		args = append(args, filter.EventType)
	}
	if filter.VesselID > 0 {
		query += " AND vessel_id = ?"
		args = append(args, filter.VesselID)
	}
	if filter.From != nil {
		query += " AND occurred_at >= ?"
		args = append(args, domain.UTC(*filter.From).Format(time.RFC3339Nano))
	}
	if filter.To != nil {
		query += " AND occurred_at < ?"
		args = append(args, domain.UTC(*filter.To).Format(time.RFC3339Nano))
	}
	query += " ORDER BY sequence ASC LIMIT ? OFFSET ?"
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := t.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var event Event
		var role, at, payload string
		if err := rows.Scan(&event.TaskID, &event.Sequence, &event.Type, &event.Actor.ID, &role, &event.VesselID, &at, &payload); err != nil {
			return nil, err
		}
		event.Actor.Role = domain.Role(role)
		event.Occurred, _ = time.Parse(time.RFC3339Nano, at)
		_ = json.Unmarshal([]byte(payload), &event.Payload)
		out = append(out, event)
	}
	return out, rows.Err()
}
