package store

import "time"

type Config struct {
	SQLitePath       string
	HeartbeatTimeout time.Duration
	InspectionPeriod time.Duration
}

func DefaultConfig() Config {
	return Config{
		SQLitePath:       "offshore-buoy.db",
		HeartbeatTimeout: 5 * time.Minute,
		InspectionPeriod: time.Minute,
	}
}
