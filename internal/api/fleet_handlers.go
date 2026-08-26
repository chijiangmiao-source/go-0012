package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/fleet"
)

func (s *Server) createVessel(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionOperateExecution); !ok {
		return
	}
	var input fleet.Vessel
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid vessel request body"))
		return
	}
	if input.VesselNo == "" || input.SpeedKnots <= 0 || input.EnduranceSeconds <= 0 || input.MaxOperationNauticalMiles <= 0 {
		writeError(w, r, domain.NewError(domain.CodeValidation, "vessel_no, speed, endurance and max distance are required"))
		return
	}
	if err := input.Position.Validate(); err != nil {
		writeError(w, r, err)
		return
	}
	now := time.Now().UTC()
	if input.PositionAt.IsZero() {
		input.PositionAt = now
	}
	if input.LastHeartbeatAt.IsZero() {
		input.LastHeartbeatAt = now
	}
	input.Online = true
	input.Version = 1
	h := s.storeHandle()
	if h == nil {
		writeError(w, r, domain.NewError(domain.CodeStorageUnavailable, "persistent store is required"))
		return
	}
	err := h.QueryRow(r.Context(), `INSERT INTO vessels(vessel_no, latitude, longitude, position_at, speed_milliknots, endurance_seconds, max_operation_millinautical_miles, online_status, last_heartbeat_at, active_load, version, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, 'online', ?, ?, 1, ?, ?) RETURNING id`, input.VesselNo, input.Position.Latitude, input.Position.Longitude, domain.UTC(input.PositionAt).Format(time.RFC3339Nano), int64(input.SpeedKnots*1000), input.EnduranceSeconds, int64(input.MaxOperationNauticalMiles*1000), domain.UTC(input.LastHeartbeatAt).Format(time.RFC3339Nano), input.ActiveLoad, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Scan(&input.ID)
	if err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "vessel_no must be unique"))
		return
	}
	writeJSON(w, http.StatusCreated, Response{Data: input, Version: input.Version, RequestID: requestID(r)})
}

func (s *Server) listVessels(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	vessels, err := s.loadVessels(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: vessels, RequestID: requestID(r)})
}

func (s *Server) heartbeatVessel(w http.ResponseWriter, r *http.Request) {
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
		Position         *domain.Position `json:"position"`
		PositionAt       time.Time        `json:"position_at"`
		EnduranceSeconds *int64           `json:"endurance_seconds"`
		ReportedAt       time.Time        `json:"reported_at"`
		ExpectedVersion  int64            `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid heartbeat body"))
		return
	}
	if input.ReportedAt.IsZero() {
		input.ReportedAt = time.Now().UTC()
	}
	if input.Position != nil {
		if err := input.Position.Validate(); err != nil {
			writeError(w, r, err)
			return
		}
	}
	h := s.storeHandle()
	if h == nil {
		writeError(w, r, domain.NewError(domain.CodeStorageUnavailable, "persistent store is required"))
		return
	}
	var currentVersion int64
	var currentPositionAt sql.NullString
	if err := h.QueryRow(r.Context(), "SELECT version, position_at FROM vessels WHERE id = ?", id).Scan(&currentVersion, &currentPositionAt); err != nil {
		writeError(w, r, err)
		return
	}
	if input.ExpectedVersion != 0 && currentVersion != input.ExpectedVersion {
		writeError(w, r, domain.VersionError(domain.CodeStaleVersion, "vessel version does not match expected_version", currentVersion))
		return
	}
	reported := domain.UTC(input.ReportedAt)
	if currentPositionAt.Valid && input.Position != nil {
		last, _ := time.Parse(time.RFC3339Nano, currentPositionAt.String)
		if !input.PositionAt.IsZero() && domain.UTC(input.PositionAt).Before(last) {
			input.Position = nil
		}
	}
	lat, lon, posAt := any(nil), any(nil), any(nil)
	if input.Position != nil {
		lat, lon = input.Position.Latitude, input.Position.Longitude
		if input.PositionAt.IsZero() {
			input.PositionAt = reported
		}
		posAt = domain.UTC(input.PositionAt).Format(time.RFC3339Nano)
	}
	endurance := any(nil)
	if input.EnduranceSeconds != nil {
		endurance = *input.EnduranceSeconds
	}
	_, err = h.Exec(r.Context(), `UPDATE vessels SET latitude = COALESCE(?, latitude), longitude = COALESCE(?, longitude), position_at = COALESCE(?, position_at), endurance_seconds = COALESCE(?, endurance_seconds), online_status = 'online', last_heartbeat_at = ?, version = version + 1, updated_at = ? WHERE id = ?`, lat, lon, posAt, endurance, reported.Format(time.RFC3339Nano), reported.Format(time.RFC3339Nano), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	vessels, _ := s.loadVessels(r.Context())
	for _, v := range vessels {
		if v.ID == id {
			if s.deps.Timeline != nil {
				_ = actor
			}
			writeJSON(w, http.StatusOK, Response{Data: v, Version: v.Version, RequestID: requestID(r)})
			return
		}
	}
	writeError(w, r, domain.NewError(domain.CodeNotFound, "vessel does not exist"))
}

func (s *Server) loadVessels(ctx context.Context) ([]fleet.Vessel, error) {
	h := s.storeHandle()
	rows, err := h.Query(ctx, `SELECT id, vessel_no, COALESCE(latitude,0), COALESCE(longitude,0), COALESCE(position_at,''), speed_milliknots, endurance_seconds, max_operation_millinautical_miles, online_status, COALESCE(last_heartbeat_at,''), active_load, version FROM vessels ORDER BY vessel_no ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fleet.Vessel
	for rows.Next() {
		var v fleet.Vessel
		var posAt, hbAt, status string
		var speed, maxDist int64
		if err := rows.Scan(&v.ID, &v.VesselNo, &v.Position.Latitude, &v.Position.Longitude, &posAt, &speed, &v.EnduranceSeconds, &maxDist, &status, &hbAt, &v.ActiveLoad, &v.Version); err != nil {
			return nil, err
		}
		v.SpeedKnots = float64(speed) / 1000
		v.MaxOperationNauticalMiles = float64(maxDist) / 1000
		v.Online = status == "online"
		if posAt != "" {
			v.PositionAt, _ = time.Parse(time.RFC3339Nano, posAt)
		}
		if hbAt != "" {
			v.LastHeartbeatAt, _ = time.Parse(time.RFC3339Nano, hbAt)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Server) loadSectors(ctx context.Context, taskID, version int64) ([]drift.Sector, map[int]int64, error) {
	h := s.storeHandle()
	rows, err := h.Query(ctx, `SELECT id, number, priority, name, polygon_json, area_square_nm, centroid_latitude, centroid_longitude, coverage_basis_points, version FROM search_sectors WHERE task_id = ? AND sector_set_version = ? ORDER BY priority, number`, taskID, version)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var sectors []drift.Sector
	ids := make(map[int]int64)
	for rows.Next() {
		var sec drift.Sector
		var polygon string
		var id int64
		if err := rows.Scan(&id, &sec.Number, &sec.Priority, &sec.Name, &polygon, &sec.AreaSquareNM, &sec.Centroid.Latitude, &sec.Centroid.Longitude, &sec.CoverageBasisPoints, &sec.Version); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(polygon), &sec.Polygon)
		sectors = append(sectors, sec)
		ids[sec.Number] = id
	}
	return sectors, ids, rows.Err()
}
