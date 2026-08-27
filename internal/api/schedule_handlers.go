package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/fleet"
	"offshore-buoy-drift-search-loop/internal/mission"
)

type storedPlan struct {
	ID               int64              `json:"id"`
	TaskID           int64              `json:"task_id"`
	SectorSetVersion int64              `json:"sector_set_version"`
	Status           string             `json:"status"`
	GeneratedAt      time.Time          `json:"generated_at"`
	Assignments      []fleet.Assignment `json:"assignments"`
	Reasons          map[int]string     `json:"unassignable_reasons,omitempty"`
	Version          int64              `json:"version"`
}

func (s *Server) generatePersistentSchedulePlan(r *http.Request, taskID int64, sectorSetVersion int64, generatedAt time.Time) (storedPlan, error) {
	sectors, sectorIDs, err := s.loadSectors(r.Context(), taskID, sectorSetVersion)
	if err != nil {
		return storedPlan{}, err
	}
	vessels, err := s.loadVessels(r.Context())
	if err != nil {
		return storedPlan{}, err
	}
	task, err := s.deps.Missions.GetTask(taskID)
	if err != nil {
		return storedPlan{}, err
	}
	plan := s.deps.Scheduler.Generate(taskID, sectorSetVersion, sectors, vessels, generatedAt)
	out := storedPlan{TaskID: taskID, SectorSetVersion: sectorSetVersion, Status: "draft", GeneratedAt: generatedAt.UTC(), Assignments: plan.Assignments, Reasons: plan.UnassignableReasons, Version: 1}
	h := s.storeHandle()
	err = h.WithinTx(r.Context(), func(ctx context.Context) error {
		if err := h.QueryRow(ctx, `INSERT INTO schedule_plans(task_id, sector_set_version, plan_type, status, generated_at, expected_task_version, version, created_at, updated_at)
VALUES(?, ?, 'auto', 'draft', ?, ?, 1, ?, ?) RETURNING id`, taskID, sectorSetVersion, generatedAt.UTC().Format(time.RFC3339Nano), task.Version, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&out.ID); err != nil {
			return err
		}
		for _, row := range plan.Assignments {
			sectorID := sectorIDs[row.SectorNumber]
			if _, err := h.Exec(ctx, `INSERT INTO assignments(task_id, plan_id, sector_id, sector_number, sector_set_version, vessel_id, start_at, end_at, score, status, version, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'proposed', 1, ?, ?)`, taskID, out.ID, sectorID, row.SectorNumber, sectorSetVersion, row.VesselID, row.StartAt.Format(time.RFC3339Nano), row.EndAt.Format(time.RFC3339Nano), row.Score, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (s *Server) confirmSchedulePlan(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionMaintainMission)
	if !ok {
		return
	}
	planID, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input struct {
		ExpectedPlanVersion int64  `json:"expected_plan_version"`
		ExpectedTaskVersion int64  `json:"expected_task_version"`
		Reason              string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid confirmation body"))
		return
	}
	h := s.storeHandle()
	err = h.WithinTx(r.Context(), func(ctx context.Context) error {
		var taskID, sectorVersion, planVersion, expectedTask int64
		var status string
		if err := h.QueryRow(ctx, `SELECT task_id, sector_set_version, status, expected_task_version, version FROM schedule_plans WHERE id = ?`, planID).Scan(&taskID, &sectorVersion, &status, &expectedTask, &planVersion); err != nil {
			return err
		}
		if status != "draft" && status != "adjusted" {
			return domain.NewError(domain.CodeInvalidTransition, "schedule plan is already decided")
		}
		if input.ExpectedPlanVersion != 0 && input.ExpectedPlanVersion != planVersion {
			return domain.VersionError(domain.CodeStaleVersion, "plan version does not match expected_plan_version", planVersion)
		}
		var taskStatus mission.TaskStatus
		var taskVersion int64
		if err := h.QueryRow(ctx, "SELECT status, version FROM search_tasks WHERE id = ?", taskID).Scan(&taskStatus, &taskVersion); err != nil {
			return err
		}
		wantTask := input.ExpectedTaskVersion
		if wantTask == 0 {
			wantTask = expectedTask
		}
		if taskVersion != wantTask {
			return domain.VersionError(domain.CodeStaleVersion, "task version does not match expected_task_version", taskVersion)
		}
		if taskStatus != mission.StatusPending && taskStatus != mission.StatusSearching && taskStatus != mission.StatusPaused {
			return domain.NewError(domain.CodeInvalidTransition, "task cannot accept a confirmed schedule in current status")
		}
		rows, err := h.Query(ctx, `SELECT id, vessel_id, start_at, end_at FROM assignments WHERE plan_id = ? AND status = 'proposed'`, planID)
		if err != nil {
			return err
		}
		type candidate struct {
			id, vesselID int64
			start, end   string
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.vesselID, &c.start, &c.end); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		rows.Close()
		for _, c := range candidates {
			var conflicts int
			if err := h.QueryRow(ctx, `SELECT COUNT(*) FROM assignments WHERE vessel_id = ? AND status IN ('confirmed','claimed','executing') AND start_at < ? AND end_at > ?`, c.vesselID, c.end, c.start).Scan(&conflicts); err != nil {
				return err
			}
			if conflicts > 0 {
				return &scheduleOverlapError{taskID: taskID, vesselID: c.vesselID, assignmentID: c.id}
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := h.Exec(ctx, `UPDATE assignments SET status = 'confirmed', version = version + 1, updated_at = ? WHERE plan_id = ? AND status = 'proposed'`, now, planID); err != nil {
			return err
		}
		if _, err := h.Exec(ctx, `UPDATE schedule_plans SET status = 'confirmed', decided_by = ?, decision_reason = ?, version = version + 1, updated_at = ? WHERE id = ?`, actor.ID, input.Reason, now, planID); err != nil {
			return err
		}
		if taskStatus == mission.StatusPending {
			if _, err := h.Exec(ctx, `UPDATE search_tasks SET status = 'searching', version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, now, taskID, wantTask); err != nil {
				return err
			}
		}
		_, _ = sectorVersion, planVersion
		return nil
	})
	if err != nil {
		var overlap *scheduleOverlapError
		if errors.As(err, &overlap) {
			if auditErr := s.persistScheduleConflictNotice(r, overlap); auditErr != nil {
				writeError(w, r, auditErr)
				return
			}
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: map[string]any{"plan_id": planID, "status": "confirmed"}, RequestID: requestID(r)})
}

// scheduleOverlapError carries the assignment whose confirmation overlapped an
// existing active assignment. Returning it from the confirmation transaction
// rolls back the partial confirmation while still letting the caller persist the
// rejection audit trail in a separate short transaction afterwards.
type scheduleOverlapError struct {
	taskID       int64
	vesselID     int64
	assignmentID int64
}

func (e *scheduleOverlapError) Error() string {
	return "confirmed assignment overlaps another active assignment"
}

// persistScheduleConflictNotice records the assignment_conflict notification and
// the corresponding timeline audit event in a single short transaction that is
// independent of the rolled-back confirmation. The plan itself stays untouched
// because the overlap check runs before any confirmation writes. If this audit
// transaction also fails, the rejection cannot be tracked and the spec requires
// surfacing 503 audit_unavailable rather than masquerading as an ordinary
// business conflict.
func (s *Server) persistScheduleConflictNotice(r *http.Request, overlap *scheduleOverlapError) error {
	h := s.storeHandle()
	actor := actorFromRequest(r)
	now := time.Now().UTC()
	dedupeKey := "assignment_conflict:" + strconv.FormatInt(overlap.taskID, 10) + ":" + strconv.FormatInt(overlap.vesselID, 10)
	payload, _ := json.Marshal(map[string]any{"vessel_id": overlap.vesselID, "assignment_id": overlap.assignmentID})
	return h.WithinTx(r.Context(), func(ctx context.Context) error {
		if _, err := h.Exec(ctx, `INSERT OR IGNORE INTO notifications(task_id, recipient_role, type, dedupe_key, title, payload_json, created_at)
VALUES(?, 'commander', 'assignment_conflict', ?, '分配时间冲突', ?, ?)`, overlap.taskID, dedupeKey, string(payload), now.Format(time.RFC3339Nano)); err != nil {
			return domain.NewError(domain.CodeAuditUnavailable, "assignment conflict notice could not be audited")
		}
		var seq int64
		if err := h.QueryRow(ctx, "SELECT event_sequence + 1 FROM search_tasks WHERE id = ?", overlap.taskID).Scan(&seq); err != nil {
			return domain.NewError(domain.CodeAuditUnavailable, "assignment conflict notice could not be audited")
		}
		eventPayload, _ := json.Marshal(map[string]any{"vessel_id": overlap.vesselID, "assignment_id": overlap.assignmentID, "reason": "assignment_conflict"})
		if _, err := h.Exec(ctx, `UPDATE search_tasks SET event_sequence = ? WHERE id = ?`, seq, overlap.taskID); err != nil {
			return domain.NewError(domain.CodeAuditUnavailable, "assignment conflict notice could not be audited")
		}
		if _, err := h.Exec(ctx, `INSERT INTO task_events(task_id, sequence, event_type, actor_id, actor_role, vessel_id, occurred_at, payload_json)
VALUES(?, ?, 'assignment_conflict', ?, ?, NULLIF(?, 0), ?, ?)`, overlap.taskID, seq, actor.ID, string(actor.Role), overlap.vesselID, now.Format(time.RFC3339Nano), string(eventPayload)); err != nil {
			return domain.NewError(domain.CodeAuditUnavailable, "assignment conflict notice could not be audited")
		}
		return nil
	})
}

func (s *Server) rejectSchedulePlan(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionMaintainMission)
	if !ok {
		return
	}
	planID, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	if input.Reason == "" {
		writeError(w, r, domain.NewError(domain.CodeValidation, "reject reason is required"))
		return
	}
	h := s.storeHandle()
	_, err = h.Exec(r.Context(), `UPDATE schedule_plans SET status = 'rejected', decided_by = ?, decision_reason = ?, version = version + 1, updated_at = ? WHERE id = ? AND status IN ('draft','adjusted')`, actor.ID, input.Reason, time.Now().UTC().Format(time.RFC3339Nano), planID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: map[string]any{"plan_id": planID, "status": "rejected"}, RequestID: requestID(r)})
}
