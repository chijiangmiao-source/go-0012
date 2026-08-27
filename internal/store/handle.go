package store

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"offshore-buoy-drift-search-loop/internal/domain"
)

type Handle struct {
	config     Config
	db         *sql.DB
	migrations []Migration
}

func Open(ctx context.Context, cfg Config) (*Handle, error) {
	if cfg.SQLitePath == "" {
		return nil, domain.NewError(domain.CodeStorageUnavailable, "sqlite path is required")
	}
	if cfg.SQLitePath != ":memory:" {
		dir := filepath.Dir(cfg.SQLitePath)
		if dir != "." {
			if _, err := os.Stat(dir); err != nil {
				return nil, err
			}
		}
	}
	db, err := sql.Open("sqlite", cfg.SQLitePath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	h := &Handle{config: cfg, db: db, migrations: Migrations()}
	if err := h.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := h.applyMigrations(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := h.Recover(ctx, time.Now().UTC()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return h, nil
}

func (h *Handle) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, stmt := range pragmas {
		if _, err := h.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handle) applyMigrations(ctx context.Context) error {
	for _, migration := range h.migrations {
		if _, err := h.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
			return err
		}
		var exists string
		err := h.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE version = ?", migration.Version).Scan(&exists)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, migration.SQL); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?, ?)", migration.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handle) Ready(ctx context.Context) error {
	if h == nil || len(h.migrations) == 0 {
		return domain.NewError(domain.CodeStorageUnavailable, "migrations are not loaded")
	}
	if err := h.db.PingContext(ctx); err != nil {
		return domain.NewError(domain.CodeStorageUnavailable, "database is not reachable")
	}
	var count int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return err
	}
	return nil
}

func (h *Handle) Config() Config {
	return h.config
}

func (h *Handle) DB() *sql.DB {
	return h.db
}

func (h *Handle) Close() error {
	if h == nil || h.db == nil {
		return nil
	}
	return h.db.Close()
}

func (h *Handle) Recover(ctx context.Context, now time.Time) error {
	threshold := now.UTC().Add(-h.config.HeartbeatTimeout).Format(time.RFC3339Nano)
	_, err := h.db.ExecContext(ctx, `
UPDATE vessels
SET online_status = 'offline', version = version + 1
WHERE online_status = 'online' AND last_heartbeat_at IS NOT NULL AND last_heartbeat_at <= ?`, threshold)
	if err != nil && isNoSuchTable(err) {
		return nil
	}
	return err
}

// SweepOffline marks vessels whose heartbeats predate the configured timeout as
// offline. It is the single shared inspection routine used by startup recovery
// and by the background inspector, so periodic sweeps apply the exact same
// conditional update (last_heartbeat_at <= check time minus threshold) that
// avoids racing a fresh heartbeat.
func (h *Handle) SweepOffline(ctx context.Context, now time.Time) error {
	return h.Recover(ctx, now)
}

// Inspect launches a background goroutine that re-applies SweepOffline on the
// configured InspectionPeriod for as long as ctx is alive. It returns a stop
// function that cancels the loop and a wait function that blocks until the loop
// has fully drained. Each sweep failure is logged and left for the next round;
// the loop never aborts the HTTP service.
func (h *Handle) Inspect(ctx context.Context, now func() time.Time, period time.Duration) (stop, wait func()) {
	ticker := time.NewTicker(period)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.SweepOffline(ctx, now()); err != nil && !isNoSuchTable(err) {
					log.Printf("vessel offline inspection sweep failed: %v", err)
				}
			}
		}
	}()
	return func() {
		ticker.Stop()
	}, func() {
		<-done
	}
}

func isNoSuchTable(err error) bool {
	return err != nil && (err.Error() == "SQL logic error: no such table: vessels (1)" || err.Error() == "no such table: vessels")
}
