package api

import (
	"context"
	"net/http"

	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/fleet"
	"offshore-buoy-drift-search-loop/internal/mission"
)

type ReadinessProbe interface {
	Ready(context.Context) error
}

type Dependencies struct {
	Store         ReadinessProbe
	Missions      *mission.Service
	Drift         drift.Engine
	Scheduler     fleet.Scheduler
	Execution     *execution.Service
	Timeline      *audit.Timeline
	Notifications *audit.NotificationCenter
	Replans       *execution.ReplanStore
}

type Server struct {
	deps Dependencies
	mux  *http.ServeMux
}

func NewServer(deps Dependencies) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
