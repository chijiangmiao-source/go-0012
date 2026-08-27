package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"offshore-buoy-drift-search-loop/internal/api"
	"offshore-buoy-drift-search-loop/internal/audit"
	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/execution"
	"offshore-buoy-drift-search-loop/internal/fleet"
	"offshore-buoy-drift-search-loop/internal/mission"
	"offshore-buoy-drift-search-loop/internal/store"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "server" {
		log.Fatal("usage: offshore-buoy server [-addr :8080] [-sqlite offshore-buoy.db]")
	}
	if len(args) > 0 {
		args = args[1:]
	}

	cfg := store.DefaultConfig()
	flags := flag.NewFlagSet("server", flag.ExitOnError)
	addr := flags.String("addr", ":8080", "HTTP listen address")
	flags.StringVar(&cfg.SQLitePath, "sqlite", cfg.SQLitePath, "SQLite database file path")
	flags.DurationVar(&cfg.HeartbeatTimeout, "heartbeat-timeout", cfg.HeartbeatTimeout, "offline heartbeat threshold")
	flags.DurationVar(&cfg.InspectionPeriod, "inspection-period", cfg.InspectionPeriod, "background inspection period")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}

	handle, err := store.Open(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}
	timeline := audit.NewPersistentTimeline(handle)
	notifications := audit.NewNotificationCenter()
	clock := domain.SystemClock{}
	server := api.NewServer(api.Dependencies{
		Store:         handle,
		Missions:      mission.NewPersistentService(clock, timeline, handle),
		Drift:         drift.NewEngine(),
		Scheduler:     fleet.NewScheduler(),
		Execution:     execution.NewPersistentService(clock, timeline, handle),
		Timeline:      timeline,
		Notifications: notifications,
		Replans:       execution.NewReplanStore(),
	})

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("offshore buoy search service listening on %s", *addr)
	log.Fatal(httpServer.ListenAndServe())
}
