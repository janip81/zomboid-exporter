package main

import "context"

// eventStore persists parsed PerkLog events for later querying (player
// history, leaderboards, a public stats site, etc.). It's entirely
// optional -- main.go runs fine with a nil eventStore, driving only the
// Prometheus counters. Two implementations: pgStore (external Postgres,
// e.g. an existing CNPG/RDS/whatever instance) and sqliteStore (embedded,
// a single file, zero external dependency -- the default for anyone who
// doesn't already run Postgres).
type eventStore interface {
	handleLogin(ctx context.Context, ev *perkEvent)
	handleCreatedPlayer(ctx context.Context, ev *perkEvent)
	handleDied(ctx context.Context, ev *perkEvent)
	handleLevelChanged(ctx context.Context, ev *perkEvent)
	handleSkills(ctx context.Context, ev *perkEvent)

	// handleExporterEvent persists a parsed ExporterLog.txt line (kill,
	// movement_distance, driving_distance, enter_vehicle, exit_vehicle,
	// eat, drink, pill, read, and any future stat the Lua mod adds) into
	// the same generic events table PerkLog events use -- see
	// exporterlog.go for parsing and schema_postgres.sql's events
	// comment for the table's rationale.
	handleExporterEvent(ctx context.Context, ev *exporterEvent)

	// getFileOffset/setFileOffset track how far into each PerkLog.txt file
	// has been read, keyed by absolute path. This is what makes history
	// gap-free across exporter restarts (including the very first run,
	// which backfills every historical file from offset 0) without any
	// manual backfill step -- every poll cycle just asks "is there new
	// content past my last checkpoint?" for every file that still exists,
	// old and current alike.
	getFileOffset(ctx context.Context, path string) (int64, error)
	setFileOffset(ctx context.Context, path string, offset int64) error

	Close()
}
