package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/fleet"
	"offshore-buoy-drift-search-loop/internal/mission"
)

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Response{Data: map[string]string{"status": "ok"}, RequestID: requestID(r)})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, r, domain.NewError(domain.CodeStorageUnavailable, "store is not configured"))
		return
	}
	if err := s.deps.Store.Ready(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: map[string]string{"status": "ready"}, RequestID: requestID(r)})
}

func (s *Server) createBuoy(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionMaintainMission); !ok {
		return
	}
	var input mission.Buoy
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid buoy request body"))
		return
	}
	buoy, err := s.deps.Missions.CreateBuoy(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, Response{Data: buoy, Version: buoy.Version, RequestID: requestID(r)})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionMaintainMission)
	if !ok {
		return
	}
	var input struct {
		BuoyID int64 `json:"buoy_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid task request body"))
		return
	}
	task, err := s.deps.Missions.CreateTask(input.BuoyID, actor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, Response{Data: task, Version: task.Version, RequestID: requestID(r)})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	if s.storeHandle() != nil {
		tasks, err := s.deps.Missions.ListTasksFiltered(mission.ListFilter{
			Status: r.URL.Query().Get("status"),
			BuoyNo: r.URL.Query().Get("buoy_no"),
			Page:   queryInt(r, "page", 1),
			Size:   queryInt(r, "page_size", 50),
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, Response{Data: tasks, RequestID: requestID(r)})
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: s.deps.Missions.ListTasks(), RequestID: requestID(r)})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	task, err := s.deps.Missions.GetTask(id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: task, Version: task.Version, RequestID: requestID(r)})
}

func (s *Server) transitionTask(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionMaintainMission)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req mission.TransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid transition request body"))
		return
	}
	req.Actor = actor
	task, err := s.deps.Missions.Transition(id, req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: task, Version: task.Version, RequestID: requestID(r)})
}

func (s *Server) generateSectorSet(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionMaintainMission)
	if !ok {
		return
	}
	taskID, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input drift.Input
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid sector set request body"))
		return
	}
	input.TaskID = taskID
	if h := s.storeHandle(); h != nil {
		if input.SnapshotID != 0 && (input.EffectiveAt.IsZero() || input.DirectionDegrees == 0 || input.SpeedKnots == 0 || input.UncertaintyNM == 0) {
			var effective string
			var dir, speed, uncertainty int64
			if err := h.QueryRow(r.Context(), `SELECT effective_at, direction_millidegrees, speed_milliknots, uncertainty_millinautical_miles FROM current_snapshots WHERE id = ? AND task_id = ?`, input.SnapshotID, taskID).Scan(&effective, &dir, &speed, &uncertainty); err != nil {
				writeError(w, r, err)
				return
			}
			input.EffectiveAt, _ = time.Parse(time.RFC3339Nano, effective)
			input.DirectionDegrees = float64(dir) / 1000
			input.SpeedKnots = float64(speed) / 1000
			input.UncertaintyNM = float64(uncertainty) / 1000
		}
		if input.LastCommunicationAt.IsZero() || input.LastPosition == (domain.Position{}) {
			var lastAt string
			if err := h.QueryRow(r.Context(), `SELECT b.last_communication_at, b.last_latitude, b.last_longitude FROM search_tasks t JOIN buoys b ON b.id = t.buoy_id WHERE t.id = ?`, taskID).Scan(&lastAt, &input.LastPosition.Latitude, &input.LastPosition.Longitude); err != nil {
				writeError(w, r, err)
				return
			}
			input.LastCommunicationAt, _ = time.Parse(time.RFC3339Nano, lastAt)
		}
		if input.SectorSetVersion == 0 {
			if err := h.QueryRow(r.Context(), `SELECT COALESCE(MAX(version), 0) + 1 FROM sector_sets WHERE task_id = ?`, taskID).Scan(&input.SectorSetVersion); err != nil {
				writeError(w, r, err)
				return
			}
		}
	}
	set, err := s.deps.Drift.Generate(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.persistSectorSet(r.Context(), actor, set); err != nil {
		writeError(w, r, err)
		return
	}
	if s.deps.Timeline != nil {
		_, _ = s.deps.Timeline.Append(taskID, audit.EventInput{
			Type:     audit.EventPredictionGenerated,
			Actor:    actor,
			Occurred: time.Now().UTC(),
			Payload:  map[string]any{"sector_set_version": set.Version, "input_digest": set.InputDigest},
		})
	}
	if s.deps.Replans != nil && set.Version > 1 {
		s.deps.Replans.Suggest(execution.ReplanSuggestion{
			TaskID:               taskID,
			Cause:                execution.ReplanPredictionChanged,
			ToSectorSetVersion:   set.Version,
			FromSectorSetVersion: set.Version - 1,
		})
	}
	writeJSON(w, http.StatusCreated, Response{Data: set, Version: set.Version, RequestID: requestID(r)})
}

func (s *Server) generateSchedulePlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionMaintainMission); !ok {
		return
	}
	taskID, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input struct {
		SectorSetVersion int64          `json:"sector_set_version"`
		Sectors          []drift.Sector `json:"sectors"`
		Vessels          []fleet.Vessel `json:"vessels"`
		GeneratedAt      time.Time      `json:"generated_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid schedule plan request body"))
		return
	}
	if input.GeneratedAt.IsZero() {
		input.GeneratedAt = time.Now().UTC()
	}
	if s.storeHandle() != nil && input.SectorSetVersion != 0 && len(input.Sectors) == 0 && len(input.Vessels) == 0 {
		plan, err := s.generatePersistentSchedulePlan(r, taskID, input.SectorSetVersion, input.GeneratedAt)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, Response{Data: plan, Version: plan.Version, RequestID: requestID(r)})
		return
	}
	plan := s.deps.Scheduler.Generate(taskID, input.SectorSetVersion, input.Sectors, input.Vessels, input.GeneratedAt)
	writeJSON(w, http.StatusCreated, Response{Data: plan, Version: input.SectorSetVersion, RequestID: requestID(r)})
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.storeHandle() != nil {
		filter := audit.EventFilter{
			TaskID:    id,
			EventType: r.URL.Query().Get("event_type"),
			VesselID:  int64(queryInt(r, "vessel_id", 0)),
			Page:      queryInt(r, "page", 1),
			PageSize:  queryInt(r, "page_size", 50),
		}
		if raw := r.URL.Query().Get("from"); raw != "" {
			if t, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
				filter.From = &t
			}
		}
		if raw := r.URL.Query().Get("to"); raw != "" {
			if t, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
				filter.To = &t
			}
		}
		events, err := s.deps.Timeline.ListPersistent(r.Context(), filter)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, Response{Data: events, RequestID: requestID(r)})
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: s.deps.Timeline.List(id, r.URL.Query().Get("event_type")), RequestID: requestID(r)})
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionHandleNotification); !ok {
		return
	}
	if s.storeHandle() != nil {
		items, err := s.listPersistentNotifications(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, Response{Data: items, RequestID: requestID(r)})
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: s.deps.Notifications.List(), RequestID: requestID(r)})
}

func (s *Server) reviewTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	task, err := s.deps.Missions.GetTask(id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	coverage := audit.WeightedCoverageBasisPoints(nil)
	if s.storeHandle() != nil {
		coverage, _ = s.coverageForTask(r, id, task.ActiveSectorSetVersion)
	}
	alerts := s.deps.Notifications.UnresolvedCount()
	if h := s.storeHandle(); h != nil {
		_ = h.QueryRow(r.Context(), "SELECT COUNT(*) FROM notifications WHERE resolved_at IS NULL AND (task_id = ? OR task_id IS NULL)", id).Scan(&alerts)
	}
	summary := audit.ReviewSummary{
		TaskID:              id,
		ActiveSectorVersion: task.ActiveSectorSetVersion,
		CoverageBasisPoints: coverage,
		Events:              s.deps.Timeline.List(id, ""),
		UnresolvedAlerts:    alerts,
	}
	writeJSON(w, http.StatusOK, Response{Data: summary, Version: task.Version, RequestID: requestID(r)})
}

func (s *Server) claimAssignment(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionOperateExecution)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input struct {
		VesselID        int64 `json:"vessel_id"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid claim request body"))
		return
	}
	assignment, err := s.deps.Execution.Claim(id, input.VesselID, input.ExpectedVersion, actor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: assignment, Version: assignment.Version, RequestID: requestID(r)})
}

func (s *Server) reportProgress(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionOperateExecution)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input struct {
		VesselID         int64 `json:"vessel_id"`
		ExpectedVersion  int64 `json:"expected_version"`
		DeltaBasisPoints int   `json:"delta_basis_points"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid progress request body"))
		return
	}
	result, err := s.deps.Execution.ReportProgress(id, input.VesselID, input.ExpectedVersion, r.Header.Get("Idempotency-Key"), input.DeltaBasisPoints, actor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: result, Version: result.Version, RequestID: requestID(r)})
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewError(domain.CodeValidation, "path id must be a positive integer")
	}
	return id, nil
}
