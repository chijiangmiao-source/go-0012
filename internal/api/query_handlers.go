package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
)

func (s *Server) liveTask(w http.ResponseWriter, r *http.Request) {
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
	coverage, _ := s.coverageForTask(r, id, task.ActiveSectorSetVersion)
	writeJSON(w, http.StatusOK, Response{Data: map[string]any{"task": task, "coverage_basis_points": coverage}, Version: task.Version, RequestID: requestID(r)})
}

func (s *Server) coverageStatistic(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	taskID, version := int64(queryInt(r, "task_id", 0)), int64(queryInt(r, "sector_set_version", 0))
	coverage, err := s.coverageForTask(r, taskID, version)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: map[string]any{"task_id": taskID, "sector_set_version": version, "coverage_basis_points": coverage}, RequestID: requestID(r)})
}

func (s *Server) coverageForTask(r *http.Request, taskID, version int64) (int, error) {
	h := s.storeHandle()
	if h == nil {
		return 0, nil
	}
	if version == 0 && taskID != 0 {
		_ = h.QueryRow(r.Context(), "SELECT COALESCE(active_sector_set_version, 0) FROM search_tasks WHERE id = ?", taskID).Scan(&version)
	}
	rows, err := h.Query(r.Context(), `SELECT area_square_nm, coverage_basis_points FROM search_sectors WHERE (? = 0 OR task_id = ?) AND (? = 0 OR sector_set_version = ?)`, taskID, taskID, version, version)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var parts []audit.CoveragePart
	for rows.Next() {
		var part audit.CoveragePart
		if err := rows.Scan(&part.AreaSquareNM, &part.CoverageBasisPoints); err != nil {
			return 0, err
		}
		parts = append(parts, part)
	}
	return audit.WeightedCoverageBasisPoints(parts), rows.Err()
}

func (s *Server) responseTimeStatistic(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	rows, err := s.storeHandle().Query(r.Context(), `SELECT t.submitted_at, MIN(a.actual_enter_at) FROM search_tasks t LEFT JOIN assignments a ON a.task_id=t.id GROUP BY t.id`)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	var total, count int64
	for rows.Next() {
		var submitted, entered sql.NullString
		if err := rows.Scan(&submitted, &entered); err != nil {
			writeError(w, r, err)
			return
		}
		if submitted.Valid && entered.Valid {
			a, _ := time.Parse(time.RFC3339Nano, submitted.String)
			b, _ := time.Parse(time.RFC3339Nano, entered.String)
			if b.After(a) {
				total += int64(b.Sub(a).Seconds())
				count++
			}
		}
	}
	avg := int64(0)
	if count > 0 {
		avg = total / count
	}
	writeJSON(w, http.StatusOK, Response{Data: map[string]any{"average_response_seconds": avg, "sample_count": count}, RequestID: requestID(r)})
}

func (s *Server) terminationReasonsStatistic(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	rows, err := s.storeHandle().Query(r.Context(), `SELECT termination_reason, COUNT(*) FROM search_tasks WHERE status='terminated' GROUP BY termination_reason ORDER BY termination_reason`)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	result := map[string]int{}
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			writeError(w, r, err)
			return
		}
		result[reason] = count
	}
	writeJSON(w, http.StatusOK, Response{Data: result, RequestID: requestID(r)})
}

func (s *Server) vesselUtilizationStatistic(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	from, to := parseWindow(r)
	rows, err := s.storeHandle().Query(r.Context(), `SELECT vessel_id, start_at, end_at FROM assignments WHERE status IN ('confirmed','claimed','executing','closed') AND start_at < ? AND end_at > ?`, to.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	busy := map[int64]float64{}
	for rows.Next() {
		var id int64
		var a, b string
		if err := rows.Scan(&id, &a, &b); err != nil {
			writeError(w, r, err)
			return
		}
		start, _ := time.Parse(time.RFC3339Nano, a)
		end, _ := time.Parse(time.RFC3339Nano, b)
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if end.After(start) {
			busy[id] += end.Sub(start).Seconds()
		}
	}
	out := map[int64]float64{}
	for id, seconds := range busy {
		out[id] = seconds / to.Sub(from).Seconds()
	}
	writeJSON(w, http.StatusOK, Response{Data: out, RequestID: requestID(r)})
}

func parseWindow(r *http.Request) (time.Time, time.Time) {
	from, to := time.Now().UTC().Add(-24*time.Hour), time.Now().UTC()
	if raw := r.URL.Query().Get("from"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			from = parsed.UTC()
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			to = parsed.UTC()
		}
	}
	return from, to
}

func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	s.markNotification(w, r, "read_at")
}

func (s *Server) resolveNotification(w http.ResponseWriter, r *http.Request) {
	s.markNotification(w, r, "resolved_at")
}

func (s *Server) markNotification(w http.ResponseWriter, r *http.Request, column string) {
	if _, ok := requireRole(w, r, domain.ActionHandleNotification); !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.storeHandle().Exec(r.Context(), "UPDATE notifications SET "+column+" = COALESCE("+column+", ?) WHERE id = ?", time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		writeError(w, r, domain.NewError(domain.CodeNotFound, "notification does not exist"))
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: map[string]any{"id": id, "status": column}, RequestID: requestID(r)})
}

func (s *Server) listPersistentNotifications(r *http.Request) ([]map[string]any, error) {
	rows, err := s.storeHandle().Query(r.Context(), `SELECT id, COALESCE(task_id,0), COALESCE(recipient_role,''), COALESCE(recipient_id,''), type, dedupe_key, title, payload_json, COALESCE(read_at,''), COALESCE(resolved_at,''), created_at FROM notifications ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, taskID int64
		var role, recipient, typ, key, title, payload, readAt, resolvedAt, created string
		if err := rows.Scan(&id, &taskID, &role, &recipient, &typ, &key, &title, &payload, &readAt, &resolvedAt, &created); err != nil {
			return nil, err
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(payload), &body)
		out = append(out, map[string]any{"id": id, "task_id": taskID, "recipient_role": role, "recipient_id": recipient, "type": typ, "dedupe_key": key, "title": title, "payload": body, "read_at": readAt, "resolved_at": resolvedAt, "created_at": created})
	}
	return out, rows.Err()
}
