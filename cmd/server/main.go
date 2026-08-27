package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	if len(os.Args) < 2 || os.Args[1] != "server" {
		log.Fatal("usage: offshore-buoy server [-addr :8080] [-sqlite offshore-buoy.db]")
	}

	cfg := store.DefaultConfig()
	flags := flag.NewFlagSet("server", flag.ExitOnError)
	addr := flags.String("addr", ":8080", "HTTP listen address")
	flags.StringVar(&cfg.SQLitePath, "sqlite", cfg.SQLitePath, "SQLite database file path")
	flags.DurationVar(&cfg.HeartbeatTimeout, "heartbeat-timeout", cfg.HeartbeatTimeout, "offline heartbeat threshold")
	flags.DurationVar(&cfg.InspectionPeriod, "inspection-period", cfg.InspectionPeriod, "background inspection period")
	if err := flags.Parse(os.Args[2:]); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	handle, err := store.Open(ctx, cfg)
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

	// Background inspection marks vessels offline once their heartbeats exceed
	// the configured timeout. It runs for the lifetime of the service so the
	// periodic sweep — not just the one-time startup recovery — keeps GET
	// /v1/vessels and automatic scheduling consistent with reality.
	inspectCtx, stopInspect := context.WithCancel(context.Background())
	_, waitInspect := handle.Inspect(inspectCtx, func() time.Time { return time.Now().UTC() }, cfg.InspectionPeriod)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("offshore buoy search service listening on %s", *addr)
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		stopInspect()
		waitInspect()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server exited: %v", err)
		}
	case <-sigCh:
		log.Printf("shutdown signal received, draining background inspection and HTTP server")
		stopInspect()
		waitInspect()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
		cancel()
	}
	_ = handle.Close()
}
