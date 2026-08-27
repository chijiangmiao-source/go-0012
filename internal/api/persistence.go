package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/store"
)

func (s *Server) storeHandle() *store.Handle {
	h, _ := s.deps.Store.(*store.Handle)
	return h
}

type snapshotDTO struct {
	ID               int64     `json:"id"`
	TaskID           int64     `json:"task_id"`
	EffectiveAt      time.Time `json:"effective_at"`
	DirectionDegrees float64   `json:"direction_degrees"`
	SpeedKnots       float64   `json:"speed_knots"`
	UncertaintyNM    float64   `json:"uncertainty_nm"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireRole(w, r, domain.ActionMaintainMission)
	if !ok {
		return
	}
	taskID, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input snapshotDTO
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewError(domain.CodeValidation, "invalid snapshot request body"))
		return
	}
	if input.DirectionDegrees < 0 || input.DirectionDegrees >= 360 || input.SpeedKnots <= 0 || input.UncertaintyNM <= 0 {
		writeError(w, r, domain.NewError(domain.CodeValidation, "snapshot direction, speed and uncertainty are invalid"))
		return
	}
	h := s.storeHandle()
	if h == nil {
		writeError(w, r, domain.NewError(domain.CodeStorageUnavailable, "persistent store is required"))
		return
	}
	now := time.Now().UTC()
	input.TaskID = taskID
	input.CreatedBy = actor.ID
	input.CreatedAt = now
	err = h.QueryRow(r.Context(), `INSERT INTO current_snapshots(task_id, effective_at, direction_millidegrees, speed_milliknots, uncertainty_millinautical_miles, created_by, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?) RETURNING id`, taskID, domain.UTC(input.EffectiveAt).Format(time.RFC3339Nano), int64(math.Round(input.DirectionDegrees*1000)), int64(math.Round(input.SpeedKnots*1000)), int64(math.Round(input.UncertaintyNM*1000)), actor.ID, now.Format(time.RFC3339Nano)).Scan(&input.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, Response{Data: input, Version: 1, RequestID: requestID(r)})
}

func (s *Server) persistSectorSet(ctx context.Context, actor domain.Actor, set drift.SectorSet) error {
	h := s.storeHandle()
	if h == nil {
		return nil
	}
	return h.WithinTx(ctx, func(txCtx context.Context) error {
		raw, _ := json.Marshal(set)
		var setID int64
		if err := h.QueryRow(txCtx, `INSERT INTO sector_sets(task_id, version, snapshot_id, algorithm_version, normalized_input_json, input_digest, predicted_latitude, predicted_longitude, drift_distance_nm, effective_radius_nm, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, set.TaskID, set.Version, set.SnapshotID, set.Algorithm, string(raw), set.InputDigest, set.PredictedCenter.Latitude, set.PredictedCenter.Longitude, set.DriftDistanceNM, set.EffectiveRadiusNM, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&setID); err != nil {
			return err
		}
		for _, sector := range set.Sectors {
			poly, _ := json.Marshal(sector.Polygon)
			if _, err := h.Exec(txCtx, `INSERT INTO search_sectors(task_id, sector_set_id, sector_set_version, number, priority, name, polygon_json, area_square_nm, centroid_latitude, centroid_longitude, coverage_basis_points, claimed_status, version)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`, set.TaskID, setID, set.Version, sector.Number, sector.Priority, sector.Name, string(poly), sector.AreaSquareNM, sector.Centroid.Latitude, sector.Centroid.Longitude, sector.CoverageBasisPoints, 1); err != nil {
				return err
			}
		}
		if _, err := h.Exec(txCtx, "UPDATE search_tasks SET active_sector_set_version = ?, updated_at = ? WHERE id = ?", set.Version, time.Now().UTC().Format(time.RFC3339Nano), set.TaskID); err != nil {
			return err
		}
		if set.Version > 1 {
			return insertReplanAndNotification(txCtx, h, execution.ReplanSuggestion{TaskID: set.TaskID, Cause: execution.ReplanPredictionChanged, FromSectorSetVersion: set.Version - 1, ToSectorSetVersion: set.Version}, "预测版本已更新")
		}
		_ = actor
		return nil
	})
}

func insertReplanAndNotification(ctx context.Context, h *store.Handle, suggestion execution.ReplanSuggestion, title string) error {
	key := suggestion.Cause.Key(suggestion.TaskID, suggestion.VesselID, suggestion.AssignmentID, suggestion.ToSectorSetVersion)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := h.Exec(ctx, `INSERT OR IGNORE INTO replan_suggestions(task_id, cause, vessel_id, assignment_id, from_sector_set_version, to_sector_set_version, dedupe_key, status, created_at)
VALUES(?, ?, NULLIF(?,0), NULLIF(?,0), NULLIF(?,0), NULLIF(?,0), ?, 'open', ?)`, suggestion.TaskID, suggestion.Cause, suggestion.VesselID, suggestion.AssignmentID, suggestion.FromSectorSetVersion, suggestion.ToSectorSetVersion, key, now)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(suggestion)
	_, err = h.Exec(ctx, `INSERT OR IGNORE INTO notifications(task_id, recipient_role, type, dedupe_key, title, payload_json, created_at)
VALUES(?, 'commander', 'replan', ?, ?, ?, ?)`, suggestion.TaskID, "notification:"+key, title, string(payload), now)
	return err
}

func queryInt(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
