package mission

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/store"
)

type EventRecorder interface {
	Append(taskID int64, input audit.EventInput) (audit.Event, error)
	AppendTx(ctx context.Context, taskID int64, input audit.EventInput) (audit.Event, error)
}

type Service struct {
	mu        sync.Mutex
	clock     domain.Clock
	events    EventRecorder
	nextBuoy  int64
	nextTask  int64
	buoys     map[int64]Buoy
	buoyNos   map[string]int64
	tasks     map[int64]Task
	activeFor map[int64]int64
	db        *store.Handle
}

func NewService(clock domain.Clock, events EventRecorder) *Service {
	return &Service{
		clock:     clock,
		events:    events,
		nextBuoy:  1,
		nextTask:  1,
		buoys:     make(map[int64]Buoy),
		buoyNos:   make(map[string]int64),
		tasks:     make(map[int64]Task),
		activeFor: make(map[int64]int64),
	}
}

func NewPersistentService(clock domain.Clock, events EventRecorder, db *store.Handle) *Service {
	service := NewService(clock, events)
	service.db = db
	return service
}

func (s *Service) CreateBuoy(input Buoy) (Buoy, error) {
	if s.db != nil {
		return s.createBuoySQL(context.Background(), input)
	}
	if input.BuoyNo == "" {
		return Buoy{}, domain.NewError(domain.CodeValidation, "buoy_no is required")
	}
	if err := input.LastPosition.Validate(); err != nil {
		return Buoy{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.buoyNos[input.BuoyNo]; exists {
		return Buoy{}, domain.NewError(domain.CodeValidation, "buoy_no must be unique")
	}
	now := s.clock.Now()
	input.ID = s.nextBuoy
	input.Version = 1
	input.LastCommunicationAt = domain.UTC(input.LastCommunicationAt)
	input.CreatedAt = now
	input.UpdatedAt = now
	s.nextBuoy++
	s.buoys[input.ID] = input
	s.buoyNos[input.BuoyNo] = input.ID
	return input, nil
}

func (s *Service) GetBuoy(id int64) (Buoy, error) {
	if s.db != nil {
		return s.getBuoySQL(context.Background(), id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	buoy, ok := s.buoys[id]
	if !ok {
		return Buoy{}, domain.NewError(domain.CodeNotFound, "buoy does not exist")
	}
	return buoy, nil
}

func (s *Service) CreateTask(buoyID int64, actor domain.Actor) (Task, error) {
	if s.db != nil {
		return s.createTaskSQL(context.Background(), buoyID, actor)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.buoys[buoyID]; !ok {
		return Task{}, domain.NewError(domain.CodeNotFound, "buoy does not exist")
	}
	if existingID := s.activeFor[buoyID]; existingID != 0 {
		existing := s.tasks[existingID]
		if !IsTerminal(existing.Status) {
			return Task{}, domain.NewError(domain.CodeActiveTaskExists, "buoy already has an active task")
		}
	}
	now := s.clock.Now()
	task := Task{
		ID:        s.nextTask,
		BuoyID:    buoyID,
		Status:    StatusDraft,
		Version:   1,
		CreatedBy: actor.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.nextTask++
	s.tasks[task.ID] = task
	s.activeFor[buoyID] = task.ID
	if s.events != nil {
		_, _ = s.events.Append(task.ID, audit.EventInput{
			Type:     audit.EventTaskCreated,
			Actor:    actor,
			Occurred: now,
			Payload:  map[string]any{"status": task.Status},
		})
	}
	return task, nil
}

func (s *Service) GetTask(id int64) (Task, error) {
	if s.db != nil {
		return s.getTaskSQL(context.Background(), id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return Task{}, domain.NewError(domain.CodeNotFound, "task does not exist")
	}
	return task, nil
}

func (s *Service) ListTasks() []Task {
	if s.db != nil {
		tasks, err := s.listTasksSQL(context.Background(), ListFilter{})
		if err == nil {
			return tasks
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		out = append(out, task)
	}
	return out
}

func (s *Service) ListTasksFiltered(filter ListFilter) ([]Task, error) {
	if s.db != nil {
		return s.listTasksSQL(context.Background(), filter)
	}
	tasks := s.ListTasks()
	out := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if filter.Status != "" && string(task.Status) != filter.Status {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *Service) Transition(taskID int64, req TransitionRequest) (Task, error) {
	if s.db != nil {
		return s.transitionSQL(context.Background(), taskID, req)
	}
	s.mu.Lock()
	task, ok := s.tasks[taskID]
	if !ok {
		s.mu.Unlock()
		return Task{}, domain.NewError(domain.CodeNotFound, "task does not exist")
	}
	if err := ValidateTransition(task, req); err != nil {
		s.mu.Unlock()
		if domain.IsCode(err, domain.CodeInvalidTransition) && s.events != nil {
			_, auditErr := s.events.Append(taskID, audit.EventInput{
				Type:     audit.EventTransitionRejected,
				Actor:    req.Actor,
				Occurred: s.clock.Now(),
				Payload: map[string]any{
					"from_status": task.Status,
					"to_status":   req.TargetStatus,
					"reason":      err.Error(),
				},
			})
			if auditErr != nil {
				return Task{}, domain.NewError("audit_unavailable", "transition rejection could not be audited")
			}
		}
		return Task{}, err
	}

	now := s.clock.Now()
	task.Status = req.TargetStatus
	task.Version++
	task.UpdatedAt = now
	if req.TargetStatus == StatusPending {
		submitted := now
		task.SubmittedAt = &submitted
	}
	if req.TargetStatus == StatusTerminated {
		task.TerminationReason = req.TerminationReason
		delete(s.activeFor, task.BuoyID)
	}
	if req.TargetStatus == StatusFound {
		foundAt := domain.UTC(*req.FoundAt)
		foundPosition := req.FoundPosition.Rounded6()
		task.FoundAt = &foundAt
		task.FoundPosition = &foundPosition
		delete(s.activeFor, task.BuoyID)
	}
	s.tasks[taskID] = task
	s.mu.Unlock()

	if s.events != nil {
		_, err := s.events.Append(taskID, audit.EventInput{
			Type:     audit.EventTaskTransitioned,
			Actor:    req.Actor,
			Occurred: now,
			Payload:  map[string]any{"status": task.Status},
		})
		if err != nil {
			return Task{}, err
		}
	}
	return task, nil
}

type ListFilter struct {
	Status string
	BuoyNo string
	Page   int
	Size   int
}

func (s *Service) createBuoySQL(ctx context.Context, input Buoy) (Buoy, error) {
	if input.BuoyNo == "" {
		return Buoy{}, domain.NewError(domain.CodeValidation, "buoy_no is required")
	}
	if err := input.LastPosition.Validate(); err != nil {
		return Buoy{}, err
	}
	now := s.clock.Now()
	input.LastCommunicationAt = domain.UTC(input.LastCommunicationAt)
	input.CreatedAt = now
	input.UpdatedAt = now
	input.Version = 1
	err := s.db.QueryRow(ctx, `INSERT INTO buoys(buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, 1, ?, ?) RETURNING id`, input.BuoyNo, input.DeviceType, input.LastCommunicationAt.Format(time.RFC3339Nano), input.LastPosition.Latitude, input.LastPosition.Longitude, input.BatteryBasisPoints, input.LostReason, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Scan(&input.ID)
	if err != nil {
		return Buoy{}, domain.NewError(domain.CodeValidation, "buoy_no must be unique")
	}
	return input, nil
}

func (s *Service) getBuoySQL(ctx context.Context, id int64) (Buoy, error) {
	var b Buoy
	var at, created, updated string
	err := s.db.QueryRow(ctx, `SELECT id, buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at
FROM buoys WHERE id = ?`, id).Scan(&b.ID, &b.BuoyNo, &b.DeviceType, &at, &b.LastPosition.Latitude, &b.LastPosition.Longitude, &b.BatteryBasisPoints, &b.LostReason, &b.Version, &created, &updated)
	if err == sql.ErrNoRows {
		return Buoy{}, domain.NewError(domain.CodeNotFound, "buoy does not exist")
	}
	if err != nil {
		return Buoy{}, err
	}
	b.LastCommunicationAt = parseTime(at)
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return b, nil
}

func (s *Service) createTaskSQL(ctx context.Context, buoyID int64, actor domain.Actor) (Task, error) {
	var task Task
	err := s.db.WithinTx(ctx, func(txCtx context.Context) error {
		var exists int
		if err := s.db.QueryRow(txCtx, "SELECT COUNT(*) FROM buoys WHERE id = ?", buoyID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return domain.NewError(domain.CodeNotFound, "buoy does not exist")
		}
		var active int
		if err := s.db.QueryRow(txCtx, "SELECT COUNT(*) FROM search_tasks WHERE buoy_id = ? AND status NOT IN ('found','terminated')", buoyID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return domain.NewError(domain.CodeActiveTaskExists, "buoy already has an active task")
		}
		now := s.clock.Now()
		task = Task{BuoyID: buoyID, Status: StatusDraft, Version: 1, CreatedBy: actor.ID, CreatedAt: now, UpdatedAt: now}
		return s.db.QueryRow(txCtx, `INSERT INTO search_tasks(buoy_id, status, version, event_sequence, created_by, created_at, updated_at)
VALUES(?, ?, 1, 0, ?, ?, ?) RETURNING id`, buoyID, string(StatusDraft), actor.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Scan(&task.ID)
	})
	if err != nil {
		return Task{}, err
	}
	if s.events != nil {
		_, _ = s.events.Append(task.ID, audit.EventInput{Type: audit.EventTaskCreated, Actor: actor, Occurred: task.CreatedAt, Payload: map[string]any{"status": task.Status}})
	}
	return task, nil
}

func (s *Service) getTaskSQL(ctx context.Context, id int64) (Task, error) {
	var t Task
	var submitted, found, created, updated sql.NullString
	var foundLat, foundLon sql.NullFloat64
	err := s.db.QueryRow(ctx, `SELECT id, buoy_id, status, submitted_at, found_at, found_latitude, found_longitude, COALESCE(termination_reason, ''), COALESCE(active_sector_set_version, 0), version, created_by, created_at, updated_at
FROM search_tasks WHERE id = ?`, id).Scan(&t.ID, &t.BuoyID, &t.Status, &submitted, &found, &foundLat, &foundLon, &t.TerminationReason, &t.ActiveSectorSetVersion, &t.Version, &t.CreatedBy, &created, &updated)
	if err == sql.ErrNoRows {
		return Task{}, domain.NewError(domain.CodeNotFound, "task does not exist")
	}
	if err != nil {
		return Task{}, err
	}
	if submitted.Valid {
		v := parseTime(submitted.String)
		t.SubmittedAt = &v
	}
	if found.Valid {
		v := parseTime(found.String)
		t.FoundAt = &v
		if foundLat.Valid && foundLon.Valid {
			pos := domain.Position{Latitude: foundLat.Float64, Longitude: foundLon.Float64}
			t.FoundPosition = &pos
		}
	}
	t.CreatedAt = parseTime(created.String)
	t.UpdatedAt = parseTime(updated.String)
	return t, nil
}

func (s *Service) listTasksSQL(ctx context.Context, filter ListFilter) ([]Task, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.Size <= 0 || filter.Size > 100 {
		filter.Size = 50
	}
	query := `SELECT t.id FROM search_tasks t JOIN buoys b ON b.id = t.buoy_id WHERE 1=1`
	args := []any{}
	if filter.Status != "" {
		query += " AND t.status = ?"
		args = append(args, filter.Status)
	}
	if filter.BuoyNo != "" {
		query += " AND b.buoy_no = ?"
		args = append(args, filter.BuoyNo)
	}
	query += " ORDER BY t.created_at DESC, t.id DESC LIMIT ? OFFSET ?"
	args = append(args, filter.Size, (filter.Page-1)*filter.Size)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		task, err := s.getTaskSQL(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Service) transitionSQL(ctx context.Context, taskID int64, req TransitionRequest) (Task, error) {
	task, err := s.getTaskSQL(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	if err := ValidateTransition(task, req); err != nil {
		if domain.IsCode(err, domain.CodeInvalidTransition) && s.events != nil {
			_, auditErr := s.events.Append(taskID, audit.EventInput{Type: audit.EventTransitionRejected, Actor: req.Actor, Occurred: s.clock.Now(), Payload: map[string]any{"from_status": task.Status, "to_status": req.TargetStatus, "reason": err.Error()}})
			if auditErr != nil {
				return Task{}, domain.NewError("audit_unavailable", "transition rejection could not be audited")
			}
		}
		return Task{}, err
	}
	now := s.clock.Now()
	var submitted any
	if req.TargetStatus == StatusPending {
		v := now.Format(time.RFC3339Nano)
		submitted = v
	} else if task.SubmittedAt != nil {
		submitted = task.SubmittedAt.Format(time.RFC3339Nano)
	}
	var foundAt, foundLat, foundLon any
	if req.TargetStatus == StatusFound {
		v := domain.UTC(*req.FoundAt)
		pos := req.FoundPosition.Rounded6()
		foundAt = v.Format(time.RFC3339Nano)
		foundLat = pos.Latitude
		foundLon = pos.Longitude
	}
	term := task.TerminationReason
	if req.TargetStatus == StatusTerminated {
		term = req.TerminationReason
	}
	var updated Task
	err = s.db.WithinTx(ctx, func(txCtx context.Context) error {
		res, execErr := s.db.Exec(txCtx, `UPDATE search_tasks SET status = ?, submitted_at = COALESCE(?, submitted_at), found_at = ?, found_latitude = ?, found_longitude = ?, termination_reason = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, string(req.TargetStatus), submitted, foundAt, foundLat, foundLon, nullString(term), now.Format(time.RFC3339Nano), taskID, req.ExpectedVersion)
		if execErr != nil {
			return execErr
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			current, _ := s.getTaskSQL(txCtx, taskID)
			return domain.VersionError(domain.CodeStaleVersion, "task version does not match expected_version", current.Version)
		}
		latest, getErr := s.getTaskSQL(txCtx, taskID)
		if getErr != nil {
			return getErr
		}
		if s.events != nil {
			if _, appendErr := s.events.AppendTx(txCtx, taskID, audit.EventInput{Type: audit.EventTaskTransitioned, Actor: req.Actor, Occurred: now, Payload: map[string]any{"status": latest.Status}}); appendErr != nil {
				return appendErr
			}
		}
		updated = latest
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return updated, nil
}

func parseTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t.UTC()
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func TerminateRequest(expected int64, reason string, actor domain.Actor) TransitionRequest {
	return TransitionRequest{
		ExpectedVersion:   expected,
		TargetStatus:      StatusTerminated,
		TerminationReason: reason,
		Actor:             actor,
	}
}

func FoundRequest(expected int64, at time.Time, position domain.Position, actor domain.Actor) TransitionRequest {
	return TransitionRequest{
		ExpectedVersion: expected,
		TargetStatus:    StatusFound,
		FoundAt:         &at,
		FoundPosition:   &position,
		Actor:           actor,
	}
}
