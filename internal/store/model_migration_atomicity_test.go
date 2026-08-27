package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestModel_MigrationFailureIsRetriedAtomically(t *testing.T) {
	tests := []struct {
		name       string
		prepareSQL string
		removeSQL  string
		migration  Migration
		objects    []string
	}{
		{
			name:       "conflicting table after an earlier schema change",
			prepareSQL: `CREATE TABLE occupied_name (id INTEGER PRIMARY KEY)`,
			removeSQL:  `DROP TABLE occupied_name`,
			migration: Migration{
				Version: "test_table_conflict",
				SQL: `
CREATE TABLE created_before_conflict (id INTEGER PRIMARY KEY);
CREATE TABLE occupied_name (id INTEGER PRIMARY KEY, value TEXT NOT NULL);`,
			},
			objects: []string{"created_before_conflict", "occupied_name"},
		},
		{
			name:       "conflicting index after an earlier schema change",
			prepareSQL: `CREATE TABLE index_owner (id INTEGER); CREATE INDEX occupied_index ON index_owner(id)`,
			removeSQL:  `DROP INDEX occupied_index`,
			migration: Migration{
				Version: "test_index_conflict",
				SQL: `
CREATE TABLE created_before_index_conflict (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
CREATE INDEX occupied_index ON created_before_index_conflict(value);`,
			},
			objects: []string{"created_before_index_conflict", "occupied_index"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatalf("open sqlite database: %v", err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)

			if _, err := db.ExecContext(ctx, tt.prepareSQL); err != nil {
				t.Fatalf("prepare conflicting object: %v", err)
			}
			h := &Handle{db: db, migrations: []Migration{tt.migration}}

			if err := h.applyMigrations(ctx); err == nil {
				t.Fatal("migration unexpectedly succeeded while a conflicting object exists")
			}

			var markerCount int
			if err := db.QueryRowContext(ctx,
				`SELECT count(*) FROM schema_migrations WHERE version = ?`, tt.migration.Version,
			).Scan(&markerCount); err != nil {
				t.Fatalf("read migration marker after failure: %v", err)
			}
			if markerCount != 0 {
				t.Fatalf("failed migration left %d completion marker(s), want 0", markerCount)
			}

			var partialCount int
			if err := db.QueryRowContext(ctx,
				`SELECT count(*) FROM sqlite_master WHERE name = ?`, tt.objects[0],
			).Scan(&partialCount); err != nil {
				t.Fatalf("inspect partially created schema: %v", err)
			}
			if partialCount != 0 {
				t.Fatalf("failed migration left schema object %q behind", tt.objects[0])
			}

			if _, err := db.ExecContext(ctx, tt.removeSQL); err != nil {
				t.Fatalf("remove conflicting object: %v", err)
			}
			if err := h.applyMigrations(ctx); err != nil {
				t.Fatalf("retry migration after removing conflict: %v", err)
			}
			if err := h.applyMigrations(ctx); err != nil {
				t.Fatalf("reapply completed migration: %v", err)
			}

			if err := db.QueryRowContext(ctx,
				`SELECT count(*) FROM schema_migrations WHERE version = ?`, tt.migration.Version,
			).Scan(&markerCount); err != nil {
				t.Fatalf("read migration marker after retry: %v", err)
			}
			if markerCount != 1 {
				t.Fatalf("successful retry has %d completion marker(s), want 1", markerCount)
			}
			for _, object := range tt.objects {
				var count int
				if err := db.QueryRowContext(ctx,
					`SELECT count(*) FROM sqlite_master WHERE name = ?`, object,
				).Scan(&count); err != nil {
					t.Fatalf("inspect schema object %q after retry: %v", object, err)
				}
				if count != 1 {
					t.Errorf("successful retry created %d schema objects named %q, want 1", count, object)
				}
			}
		})
	}
}
