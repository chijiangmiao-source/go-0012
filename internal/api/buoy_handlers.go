package api

import (
	"encoding/json"
	"net/http"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/mission"
)

func (s *Server) listBuoys(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	h := s.storeHandle()
	rows, err := h.Query(r.Context(), `SELECT id, buoy_no, device_type, last_communication_at, last_latitude, last_longitude, battery_basis_points, lost_reason, version, created_at, updated_at FROM buoys ORDER BY buoy_no`)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	var out []mission.Buoy
	for rows.Next() {
		var b mission.Buoy
		var last, created, updated string
		if err := rows.Scan(&b.ID, &b.BuoyNo, &b.DeviceType, &last, &b.LastPosition.Latitude, &b.LastPosition.Longitude, &b.BatteryBasisPoints, &b.LostReason, &b.Version, &created, &updated); err != nil {
			writeError(w, r, err)
			return
		}
		b.LastCommunicationAt, _ = time.Parse(time.RFC3339Nano, last)
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, Response{Data: out, RequestID: requestID(r)})
}

func (s *Server) getBuoy(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionReadAudit); !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	buoy, err := s.deps.Missions.GetBuoy(id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{Data: buoy, Version: buoy.Version, RequestID: requestID(r)})
}

func (s *Server) patchBuoy(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireRole(w, r, domain.ActionMaintainMission); !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input mission.Buoy
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid buoy patch body"))
		return
	}
	if err := input.LastPosition.Validate(); err != nil {
		writeError(w, r, err)
		return
	}
	current, err := s.deps.Missions.GetBuoy(id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if input.Version != current.Version {
		writeError(w, r, domain.VersionError(domain.CodeStaleVersion, "buoy version does not match expected version", current.Version))
		return
	}
	if input.LastCommunicationAt.IsZero() {
		input.LastCommunicationAt = current.LastCommunicationAt
	}
	if input.DeviceType == "" {
		input.DeviceType = current.DeviceType
	}
	h := s.storeHandle()
	res, err := h.Exec(r.Context(), `UPDATE buoys SET device_type=?, last_communication_at=?, last_latitude=?, last_longitude=?, battery_basis_points=?, lost_reason=?, version=version+1, updated_at=? WHERE id=? AND version=?`, input.DeviceType, domain.UTC(input.LastCommunicationAt).Format(time.RFC3339Nano), input.LastPosition.Latitude, input.LastPosition.Longitude, input.BatteryBasisPoints, input.LostReason, time.Now().UTC().Format(time.RFC3339Nano), id, input.Version)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		writeError(w, r, domain.VersionError(domain.CodeStaleVersion, "buoy version does not match expected version", current.Version))
		return
	}
	updated, _ := s.deps.Missions.GetBuoy(id)
	writeJSON(w, http.StatusOK, Response{Data: updated, Version: updated.Version, RequestID: requestID(r)})
}
