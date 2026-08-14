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

	// handleSessionEvent persists a parsed connections.txt line
	// (session_start/session_end, from the native fully-connected/
	// receive-disconnect records) -- see connections.go.
	handleSessionEvent(ctx context.Context, ev *sessionEvent)

	// handleTWRJobResult persists a parsed ThoseWhoRemainLog.txt
	// "twr_job_result" line -- a ThoseWhoRemain mod world-mutation
	// job's outcome (applied/retryable_error/final_error/
	// deferred_world) -- into twr_job_attempts, and for a successful
	// "applied" result, also into twr_world_artifacts (one
	// transaction -- see spawn-result-tracking.md review Q6). See
	// twrlog.go for parsing and schema_postgres.sql's twr_job_attempts
	// comment for the design rationale.
	//
	// Returns an error on any durable-write failure (review Q4) -- the
	// caller (pollTWROnce) must NOT advance the file offset past an
	// event that failed to commit, or a transient DB outage would
	// silently and permanently lose a control-plane audit record. A
	// malformed/duplicate event that's expected and fine to skip
	// (idempotent replay, ON CONFLICT DO NOTHING) still returns nil.
	handleTWRJobResult(ctx context.Context, ev *twrEvent) error

	// handleTWRJobAccepted persists a parsed ThoseWhoRemainLog.txt
	// "twr_job_result" line whose result="accepted" -- a quest-engine
	// transport-acceptance receipt (Lua durably recorded this dispatch
	// into PendingActions/SGOS), NOT a final application outcome. Kept
	// entirely separate from handleTWRJobResult/twr_job_attempts on
	// purpose (CGPT-G1-P3-01, 2026-08-14): that table's unique index is
	// (server, job_id, attempt_no), and an accepted receipt sharing the
	// same identity as the eventual applied/final_error outcome would
	// collide on ON CONFLICT DO NOTHING and silently discard the real
	// result. pgStore updates twr_jobs.status/accepted_at directly;
	// sqliteStore is a no-op (the quest engine is Postgres-only -- see
	// questengine.go's header).
	handleTWRJobAccepted(ctx context.Context, ev *twrEvent) error

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
