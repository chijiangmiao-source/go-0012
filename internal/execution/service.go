package execution

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/store"
)

type EventRecorder interface {
	Append(taskID int64, input audit.EventInput) (audit.Event, error)
}

type idempotencyRecord struct {
	Hash   string
	Result ProgressResult
}

type Service struct {
	mu          sync.Mutex
	clock       domain.Clock
	events      EventRecorder
	assignments map[int64]Assignment
	idempotency map[string]idempotencyRecord
	db          *store.Handle
}

func NewService(clock domain.Clock, events EventRecorder) *Service {
	return &Service{
		clock:       clock,
		events:      events,
		assignments: make(map[int64]Assignment),
		idempotency: make(map[string]idempotencyRecord),
	}
}

func NewPersistentService(clock domain.Clock, events EventRecorder, db *store.Handle) *Service {
	service := NewService(clock, events)
	service.db = db
	return service
}

func (s *Service) AddAssignment(assignment Assignment) Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if assignment.Version == 0 {
		assignment.Version = 1
	}
	if assignment.Status == "" {
		assignment.Status = AssignmentConfirmed
	}
	s.assignments[assignment.ID] = assignment
	return assignment
}

func (s *Service) Claim(assignmentID, vesselID, expectedVersion int64, actor domain.Actor) (Assignment, error) {
	if s.db != nil {
		return s.claimSQL(context.Background(), assignmentID, vesselID, expectedVersion, actor)
	}
	s.mu.Lock()
	assignment, ok := s.assignments[assignmentID]
	if !ok {
		s.mu.Unlock()
		return Assignment{}, domain.NewError(domain.CodeNotFound, "assignment does not exist")
	}
	if assignment.Status != AssignmentConfirmed || assignment.VesselID != vesselID {
		s.mu.Unlock()
		return Assignment{}, domain.NewError(domain.CodeSectorClaimed, "assignment cannot be claimed")
	}
	if assignment.Version != expectedVersion {
		s.mu.Unlock()
		return Assignment{}, domain.VersionError(domain.CodeStaleVersion, "assignment version does not match expected_version", assignment.Version)
	}
	now := s.clock.Now()
	assignment.Status = AssignmentClaimed
	assignment.Version++
	assignment.ClaimedAt = &now
	s.assignments[assignmentID] = assignment
	s.mu.Unlock()

	if s.events != nil {
		_, err := s.events.Append(assignment.TaskID, audit.EventInput{
			Type:     audit.EventAssignmentClaimed,
			Actor:    actor,
			VesselID: vesselID,
			Occurred: now,
			Payload:  map[string]any{"assignment_id": assignmentID},
		})
		if err != nil {
			return Assignment{}, err
		}
	}
	return assignment, nil
}

func (s *Service) ReportProgress(assignmentID, vesselID, expectedVersion int64, key string, deltaBasisPoints int, actor domain.Actor) (ProgressResult, error) {
	if s.db != nil {
		return s.progressSQL(context.Background(), assignmentID, vesselID, expectedVersion, key, deltaBasisPoints, actor)
	}
	if key == "" {
		return ProgressResult{}, domain.NewError(domain.CodeValidation, "Idempotency-Key is required")
	}
	if deltaBasisPoints < 1 || deltaBasisPoints > 10_000 {
		return ProgressResult{}, domain.NewError(domain.CodeValidation, "coverage delta must be within 1..10000 basis points")
	}
	scope := idempotencyScope(assignmentID, vesselID, "progress", key)
	hash := requestHash(map[string]any{
		"assignment_id": assignmentID,
		"vessel_id":     vesselID,
		"expected":      expectedVersion,
		"delta":         deltaBasisPoints,
	})

	s.mu.Lock()
	if record, ok := s.idempotency[scope]; ok {
		s.mu.Unlock()
		if record.Hash != hash {
			return ProgressResult{}, domain.NewError(domain.CodeIdempotencyMismatch, "same idempotency key was used with different progress payload")
		}
		return record.Result, nil
	}
	assignment, ok := s.assignments[assignmentID]
	if !ok {
		s.mu.Unlock()
		return ProgressResult{}, domain.NewError(domain.CodeNotFound, "assignment does not exist")
	}
	if assignment.Version != expectedVersion {
		s.mu.Unlock()
		return ProgressResult{}, domain.VersionError(domain.CodeStaleVersion, "assignment version does not match expected_version", assignment.Version)
	}
	if assignment.VesselID != vesselID {
		s.mu.Unlock()
		return ProgressResult{}, domain.NewError(domain.CodeForbidden, "assignment belongs to another vessel")
	}
	assignment.CoverageBasisPoints += deltaBasisPoints
	if assignment.CoverageBasisPoints > 10_000 {
		assignment.CoverageBasisPoints = 10_000
	}
	assignment.Version++
	s.assignments[assignmentID] = assignment
	result := ProgressResult{
		AssignmentID:        assignment.ID,
		CoverageBasisPoints: assignment.CoverageBasisPoints,
		Version:             assignment.Version,
	}
	s.idempotency[scope] = idempotencyRecord{Hash: hash, Result: result}
	s.mu.Unlock()

	if s.events != nil {
		_, err := s.events.Append(assignment.TaskID, audit.EventInput{
			Type:     audit.EventProgressReported,
			Actor:    actor,
			VesselID: vesselID,
			Occurred: s.clock.Now(),
			Payload:  map[string]any{"assignment_id": assignmentID, "delta_basis_points": deltaBasisPoints},
		})
		if err != nil {
			return ProgressResult{}, err
		}
	}
	return result, nil
}

func (s *Service) claimSQL(ctx context.Context, assignmentID, vesselID, expectedVersion int64, actor domain.Actor) (Assignment, error) {
	var assignment Assignment
	err := s.db.WithinTx(ctx, func(txCtx context.Context) error {
		a, err := s.loadAssignment(txCtx, assignmentID)
		if err != nil {
			return err
		}
		if a.Status != AssignmentConfirmed || a.VesselID != vesselID {
			return domain.NewError(domain.CodeSectorClaimed, "assignment cannot be claimed")
		}
		if a.Version != expectedVersion {
			return domain.VersionError(domain.CodeStaleVersion, "assignment version does not match expected_version", a.Version)
		}
		now := s.clock.Now()
		res, err := s.db.Exec(txCtx, `UPDATE assignments SET status = 'claimed', claimed_by = ?, claimed_at = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND status = 'confirmed' AND vessel_id = ?`, actor.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), assignmentID, expectedVersion, vesselID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected != 1 {
			return domain.NewError(domain.CodeSectorClaimed, "assignment cannot be claimed")
		}
		if _, err := s.db.Exec(txCtx, `UPDATE search_sectors SET claimed_status = 'claimed', version = version + 1 WHERE id = ?`, a.SectorID); err != nil {
			return err
		}
		assignment, err = s.loadAssignment(txCtx, assignmentID)
		return err
	})
	if err != nil {
		return Assignment{}, err
	}
	if s.events != nil {
		_, err = s.events.Append(assignment.TaskID, audit.EventInput{Type: audit.EventAssignmentClaimed, Actor: actor, VesselID: vesselID, Occurred: s.clock.Now(), Payload: map[string]any{"assignment_id": assignmentID}})
	}
	return assignment, err
}

func (s *Service) progressSQL(ctx context.Context, assignmentID, vesselID, expectedVersion int64, key string, deltaBasisPoints int, actor domain.Actor) (ProgressResult, error) {
	if key == "" {
		return ProgressResult{}, domain.NewError(domain.CodeValidation, "Idempotency-Key is required")
	}
	if deltaBasisPoints < 1 || deltaBasisPoints > 10_000 {
		return ProgressResult{}, domain.NewError(domain.CodeValidation, "coverage delta must be within 1..10000 basis points")
	}
	hash := requestHash(map[string]any{"assignment_id": assignmentID, "vessel_id": vesselID, "expected": expectedVersion, "delta": deltaBasisPoints})
	var result ProgressResult
	err := s.db.WithinTx(ctx, func(txCtx context.Context) error {
		a, err := s.loadAssignment(txCtx, assignmentID)
		if err != nil {
			return err
		}
		if a.Version != expectedVersion {
			return domain.VersionError(domain.CodeStaleVersion, "assignment version does not match expected_version", a.Version)
		}
		var storedHash, response string
		err = s.db.QueryRow(txCtx, `SELECT request_digest, response_json FROM idempotency_records WHERE task_id = ? AND vessel_id = ? AND operation = 'progress' AND idempotency_key = ?`, a.TaskID, vesselID, key).Scan(&storedHash, &response)
		if err == nil {
			if storedHash != hash {
				return domain.NewError(domain.CodeIdempotencyMismatch, "same idempotency key was used with different progress payload")
			}
			return json.Unmarshal([]byte(response), &result)
		}
		if err != sql.ErrNoRows {
			return err
		}
		if a.VesselID != vesselID {
			return domain.NewError(domain.CodeForbidden, "assignment belongs to another vessel")
		}
		var currentCoverage int
		if err := s.db.QueryRow(txCtx, "SELECT coverage_basis_points FROM search_sectors WHERE id = ?", a.SectorID).Scan(&currentCoverage); err != nil {
			return err
		}
		nextCoverage := currentCoverage + deltaBasisPoints
		if nextCoverage > 10_000 {
			nextCoverage = 10_000
		}
		res, err := s.db.Exec(txCtx, `UPDATE assignments SET version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, s.clock.Now().Format(time.RFC3339Nano), assignmentID, expectedVersion)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected != 1 {
			return domain.VersionError(domain.CodeStaleVersion, "assignment version does not match expected_version", a.Version)
		}
		if _, err := s.db.Exec(txCtx, `UPDATE search_sectors SET coverage_basis_points = ?, version = version + 1 WHERE id = ?`, nextCoverage, a.SectorID); err != nil {
			return err
		}
		result = ProgressResult{AssignmentID: assignmentID, CoverageBasisPoints: nextCoverage, Version: expectedVersion + 1}
		raw, _ := json.Marshal(result)
		if _, err := s.db.Exec(txCtx, `INSERT INTO idempotency_records(task_id, vessel_id, operation, idempotency_key, request_digest, response_status, response_json, created_at)
VALUES(?, ?, 'progress', ?, ?, 200, ?, ?)`, a.TaskID, vesselID, key, hash, string(raw), s.clock.Now().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := s.db.Exec(txCtx, `INSERT INTO execution_reports(assignment_id, task_id, vessel_id, report_type, payload_json, request_digest, created_at)
VALUES(?, ?, ?, 'progress', ?, ?, ?)`, assignmentID, a.TaskID, vesselID, string(raw), hash, s.clock.Now().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ProgressResult{}, err
	}
	if s.events != nil {
		a, _ := s.loadAssignment(context.Background(), assignmentID)
		_, err = s.events.Append(a.TaskID, audit.EventInput{Type: audit.EventProgressReported, Actor: actor, VesselID: vesselID, Occurred: s.clock.Now(), Payload: map[string]any{"assignment_id": assignmentID, "delta_basis_points": deltaBasisPoints}})
	}
	return result, err
}

func (s *Service) loadAssignment(ctx context.Context, id int64) (Assignment, error) {
	var a Assignment
	var claimed sql.NullString
	err := s.db.QueryRow(ctx, `SELECT id, task_id, sector_id, sector_set_version, vessel_id, status, version, claimed_at
FROM assignments WHERE id = ?`, id).Scan(&a.ID, &a.TaskID, &a.SectorID, &a.SectorSetVersion, &a.VesselID, &a.Status, &a.Version, &claimed)
	if err == sql.ErrNoRows {
		return Assignment{}, domain.NewError(domain.CodeNotFound, "assignment does not exist")
	}
	if err != nil {
		return Assignment{}, err
	}
	if claimed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, claimed.String)
		t = t.UTC()
		a.ClaimedAt = &t
	}
	var coverage int
	_ = s.db.QueryRow(ctx, "SELECT coverage_basis_points FROM search_sectors WHERE id = ?", a.SectorID).Scan(&coverage)
	a.CoverageBasisPoints = coverage
	return a, nil
}

func idempotencyScope(assignmentID, vesselID int64, operation, key string) string {
	return operation + ":" + key + ":" + jsonNumber(assignmentID) + ":" + jsonNumber(vesselID)
}

func requestHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func jsonNumber(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
