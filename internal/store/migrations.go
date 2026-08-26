package store

import "embed"

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Migration struct {
	Version string
	SQL     string
}

func Migrations() []Migration {
	raw, err := migrationFiles.ReadFile("migrations/001_foundation.sql")
	if err != nil {
		return nil
	}
	return []Migration{{Version: "001_foundation", SQL: string(raw)}}
}
