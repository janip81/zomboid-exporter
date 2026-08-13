package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema_postgres.sql
var schemaSQL string

// pgStore owns the Postgres connection and a small in-memory cache of each
// player's currently-alive character, so "skills"/"level_changed" lines
// (which carry no character identifier of their own) can be attributed to
// the right character row without a query on every line.
type pgStore struct {
	pool                *pgxpool.Pool
	serverName          string
	activeCharBySteamID map[string]int64
	steamIDByUsername   map[string]string
}

func newPgStore(ctx context.Context, dsn, serverName string) (*pgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, err
	}
	s := &pgStore{pool: pool, serverName: serverName, activeCharBySteamID: make(map[string]int64), steamIDByUsername: make(map[string]string)}
	if err := s.migrateServerColumn(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.migrateFileOffsetKeys(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.migrateEventsSteamIDNullable(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.loadActiveCharacters(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// migrateServerColumn backfills the server column on databases that
// predate it -- CREATE TABLE IF NOT EXISTS above is a no-op against an
// already-existing table, so this covers upgrading an existing
// deployment in place. Every row gets this instance's --server-name;
// harmless no-op on a fresh database (schemaSQL already created the
// column NOT NULL, so ADD COLUMN IF NOT EXISTS and the UPDATE both
// affect zero rows).
func (s *pgStore) migrateServerColumn(ctx context.Context) error {
	for _, tbl := range []string{"players", "characters", "events"} {
		if _, err := s.pool.Exec(ctx, `ALTER TABLE `+tbl+` ADD COLUMN IF NOT EXISTS server TEXT`); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE `+tbl+` SET server = $1 WHERE server IS NULL`, s.serverName); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `ALTER TABLE `+tbl+` ALTER COLUMN server SET NOT NULL`); err != nil {
			return err
		}
	}
	return nil
}

// migrateFileOffsetKeys converts processed_files rows keyed by full
// absolute path (the original scheme) to basename-only keys, which
// pollOnce/pollExporterOnce now use so a checkpoint survives PZ moving a
// session's log file from Logs/<name>.txt to Logs/logs_YYYY-MM-DD/
// <name>.txt once the next server start archives it -- see listPerkLogs'
// comment in perklog.go for the full story. Without this, every
// already-checkpointed file would look brand new under the new key
// scheme and get fully re-ingested, duplicating its entire event
// history. Harmless no-op once every row is already basename-keyed (no
// row contains a path separator).
func (s *pgStore) migrateFileOffsetKeys(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT file_path, byte_offset FROM processed_files WHERE file_path LIKE '%/%'`)
	if err != nil {
		return err
	}
	type kv struct {
		oldKey string
		offset int64
	}
	var toMigrate []kv
	for rows.Next() {
		var k kv
		if err := rows.Scan(&k.oldKey, &k.offset); err != nil {
			rows.Close()
			return err
		}
		toMigrate = append(toMigrate, k)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, k := range toMigrate {
		newKey := filepath.Base(k.oldKey)
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO processed_files (file_path, byte_offset)
			VALUES ($1, $2)
			ON CONFLICT (file_path) DO UPDATE SET byte_offset = GREATEST(processed_files.byte_offset, EXCLUDED.byte_offset)
		`, newKey, k.offset); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM processed_files WHERE file_path = $1`, k.oldKey); err != nil {
			return err
		}
	}
	return nil
}

// migrateEventsSteamIDNullable drops the NOT NULL constraint on
// events.steam_id for databases that predate world_stats -- the first
// event type with no player attached at all (see handleExporterEvent's
// player-less branch). Idempotent: DROP NOT NULL on an already-nullable
// column is a harmless no-op in Postgres, no error.
func (s *pgStore) migrateEventsSteamIDNullable(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `ALTER TABLE events ALTER COLUMN steam_id DROP NOT NULL`)
	return err
}

func (s *pgStore) Close() {
	s.pool.Close()
}

func (s *pgStore) loadActiveCharacters(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT steam_id, id FROM characters WHERE is_alive`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var steamID string
		var id int64
		if err := rows.Scan(&steamID, &id); err != nil {
			return err
		}
		s.activeCharBySteamID[steamID] = id
	}
	return rows.Err()
}

func (s *pgStore) upsertPlayer(ctx context.Context, steamID, username string, seenAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO players (steam_id, last_username, first_seen, last_seen, server)
		VALUES ($1, $2, $3, $3, $4)
		ON CONFLICT (steam_id) DO UPDATE
		SET last_username = EXCLUDED.last_username,
		    last_seen = EXCLUDED.last_seen,
		    server = EXCLUDED.server
		WHERE players.last_seen < EXCLUDED.last_seen
	`, steamID, username, seenAt, s.serverName)
	return err
}

// activeCharacter returns the id of the steamID's currently-alive
// character, creating one (character_number 1) if none is known yet --
// covers the case where the exporter starts mid-session, after a
// character already existed in-game.
func (s *pgStore) activeCharacter(ctx context.Context, steamID string, at time.Time) (int64, error) {
	if id, ok := s.activeCharBySteamID[steamID]; ok {
		return id, nil
	}

	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM characters WHERE steam_id = $1 AND is_alive
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&id)
	if err == nil {
		s.activeCharBySteamID[steamID] = id
		return id, nil
	}

	// No alive character on record -- create character_number 1 (or next
	// free number) so later events have somewhere to attach.
	var nextNum int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(character_number), 0) + 1 FROM characters WHERE steam_id = $1
	`, steamID).Scan(&nextNum); err != nil {
		return 0, err
	}
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO characters (steam_id, character_number, created_at, is_alive, server)
		VALUES ($1, $2, $3, TRUE, $4)
		RETURNING id
	`, steamID, nextNum, at, s.serverName).Scan(&id); err != nil {
		return 0, err
	}
	s.activeCharBySteamID[steamID] = id
	return id, nil
}

func (s *pgStore) handleLogin(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('login', $1, $2, $3, jsonb_build_object('hours_survived', $4::float8, 'x', $5::int, 'y', $6::int, 'z', $7::int), $8)
	`, ev.SteamID, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z, s.serverName)
	if err != nil {
		slog.Warn("insert login event failed", "err", err)
	}
}

func (s *pgStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	var nextNum int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(character_number), 0) + 1 FROM characters WHERE steam_id = $1
	`, ev.SteamID).Scan(&nextNum); err != nil {
		slog.Warn("next character_number lookup failed", "err", err)
		return
	}
	var charID int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO characters (steam_id, character_number, created_at, is_alive, server)
		VALUES ($1, $2, $3, TRUE, $4)
		RETURNING id
	`, ev.SteamID, nextNum, ev.Timestamp, s.serverName).Scan(&charID)
	if err != nil {
		slog.Warn("insert character failed", "err", err)
		return
	}
	s.activeCharBySteamID[ev.SteamID] = charID
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('created_player', $1, $2, $3, '{}'::jsonb, $4)
	`, ev.SteamID, charID, ev.Timestamp, s.serverName)
	if err != nil {
		slog.Warn("insert created_player event failed", "err", err)
	}
}

func (s *pgStore) handleDied(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE characters
		SET is_alive = FALSE,
		    died_at = $2,
		    hours_survived_at_death = $3,
		    death_x = $4, death_y = $5, death_z = $6
		WHERE id = $1
	`, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z)
	if err != nil {
		slog.Warn("update character died failed", "err", err)
		return
	}
	delete(s.activeCharBySteamID, ev.SteamID)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('died', $1, $2, $3, jsonb_build_object('hours_survived', $4::float8, 'x', $5::int, 'y', $6::int, 'z', $7::int), $8)
	`, ev.SteamID, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z, s.serverName)
	if err != nil {
		slog.Warn("insert died event failed", "err", err)
	}
}

func (s *pgStore) handleLevelChanged(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO skill_snapshots (character_id, captured_at, skill_name, level)
		VALUES ($1, $2, $3, $4)
	`, charID, ev.Timestamp, ev.SkillName, ev.SkillLevel)
	if err != nil {
		slog.Warn("insert skill_snapshot (levelup) failed", "err", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('level_changed', $1, $2, $3, jsonb_build_object('skill', $4::text, 'level', $5::int), $6)
	`, ev.SteamID, charID, ev.Timestamp, ev.SkillName, ev.SkillLevel, s.serverName)
	if err != nil {
		slog.Warn("insert level_changed event failed", "err", err)
	}
}

func (s *pgStore) handleSkills(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	var batch pgx.Batch
	for name, level := range ev.Skills {
		batch.Queue(`
			INSERT INTO skill_snapshots (character_id, captured_at, skill_name, level)
			VALUES ($1, $2, $3, $4)
		`, charID, ev.Timestamp, name, level)
	}
	br := s.pool.SendBatch(ctx, &batch)
	if err := br.Close(); err != nil {
		slog.Warn("insert skill_snapshots (dump) failed", "err", err)
	}
}

// rememberSteamID/canonicalSteamID: player:getSteamID() in Lua loses
// precision for a real SteamID64 (Lua numbers are doubles, exact only
// to 2^53 -- a SteamID64 is ~7.6e16), confirmed live to produce a
// different wrong digit string depending on when it's called. PerkLog.
// txt and connections.txt are both written natively by the Java engine
// and never pass through that lossy conversion, so their steam_id is
// always exact -- rememberSteamID is called from every handler sourced
// from those two files to build a username -> correct-steam_id cache,
// and handleExporterEvent (Lua-mod-sourced, unreliable steamId) prefers
// that cache over trusting its own event's steamId.
func (s *pgStore) rememberSteamID(username, steamID string) {
	if username != "" && steamID != "" {
		s.steamIDByUsername[username] = steamID
	}
}

func (s *pgStore) canonicalSteamID(username, fallback string) string {
	if id, ok := s.steamIDByUsername[username]; ok {
		return id
	}
	return fallback
}

// handleExporterEvent persists a parsed ExporterLog.txt line generically:
// event_type is whatever the Lua mod's "type" field says, and the full
// decoded payload is kept verbatim in details -- see exporterlog.go.
func (s *pgStore) handleExporterEvent(ctx context.Context, ev *exporterEvent) {
	details, err := json.Marshal(ev.Fields)
	if err != nil {
		slog.Warn("marshal ExporterLog details failed", "type", ev.EventType, "err", err)
		return
	}

	// Player-less system event (e.g. world_stats -- the first event
	// type this mod ever emits with no player attached at all).
	// Confirmed by BOTH username and steamId being empty: every real
	// player-scoped event always carries a username even on the rare
	// occasion steamId resolution itself fails, so that failure case
	// still falls through to the drop-with-warning path below,
	// unchanged. No player to upsert or active-character lookup to do
	// -- steam_id/character_id are just NULL (see
	// migrateEventsSteamIDNullable for why that's a safe column type).
	if ev.Username == "" && ev.SteamID == "" {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
			VALUES ($1, NULL, NULL, $2, $3::jsonb, $4)
		`, ev.EventType, ev.Timestamp, string(details), s.serverName); err != nil {
			slog.Warn("insert player-less exporter event failed", "type", ev.EventType, "err", err)
		}
		return
	}

	steamID := s.canonicalSteamID(ev.Username, ev.SteamID)
	if steamID == "" {
		slog.Warn("dropping ExporterLog event with no steamId", "type", ev.EventType, "username", ev.Username)
		return
	}
	if err := s.upsertPlayer(ctx, steamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	charID, err := s.activeCharacter(ctx, steamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
	`, ev.EventType, steamID, charID, ev.Timestamp, string(details), s.serverName); err != nil {
		slog.Warn("insert exporter event failed", "type", ev.EventType, "err", err)
	}
}

// handleSessionEvent persists a parsed connections.txt session_start/
// session_end line -- see connections.go. Not character-scoped (a
// session spans logins/deaths/new characters), so character_id is
// always NULL here, unlike the character-linked handlers above.
func (s *pgStore) handleSessionEvent(ctx context.Context, ev *sessionEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ($1, $2, NULL, $3, '{}'::jsonb, $4)
	`, ev.Kind, ev.SteamID, ev.Timestamp, s.serverName)
	if err != nil {
		slog.Warn("insert session event failed", "kind", ev.Kind, "err", err)
	}
}

// handleTWRJobResult persists one twr_job_result line into
// twr_job_attempts (always) and, only for a genuinely successful
// "applied" result, twr_world_artifacts too -- see
// spawn-result-tracking.md's "a failed spawn must never create a fake
// artifact row" rule. ON CONFLICT DO NOTHING on the artifact insert
// keeps this idempotent-safe if the same line is ever reprocessed
// (checkpoint edge case), rather than erroring on the UNIQUE
// (artifact_key, server) constraint.
// handleTWRJobResult -- see eventstore.go's interface comment for the
// durability contract (review Q4) this implements: any returned error
// means the caller must NOT advance past this line, so retry it later
// instead of losing the audit record. A malformed event (no jobId) is
// NOT a transient failure -- it can never succeed on retry -- so that
// case is logged and skipped (nil error), same treatment as
// twrlog.go's own malformed-line handling.
func (s *pgStore) handleTWRJobResult(ctx context.Context, ev *twrEvent) error {
	f := ev.Fields
	jobID := twrStringField(f, "jobId")
	if jobID == "" {
		slog.Warn("dropping twr_job_result with no jobId")
		return nil
	}
	attemptNo, _ := twrIntField(f, "attemptNo")
	if attemptNo == 0 {
		attemptNo = 1
	}
	result := twrStringField(f, "result")
	placed, hasPlaced := twrIntField(f, "placed")
	requested, hasRequested := twrIntField(f, "requested")

	var placedPtr, requestedPtr *int
	if hasPlaced {
		placedPtr = &placed
	}
	if hasRequested {
		requestedPtr = &requested
	}

	// Review Q6: attempt + artifact are one logical result, both or
	// neither commits -- a partial write (attempt row present, artifact
	// row missing because the second INSERT failed) would be worse than
	// either the checkpoint retry or a clean drop.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- no-op after a successful Commit

	// Review Q5: ON CONFLICT DO NOTHING against idx_twr_job_attempts_unique
	// absorbs the crash-replay window (commit succeeds, offset persist
	// doesn't, same line reprocessed after restart) without erroring.
	if _, err := tx.Exec(ctx, `
		INSERT INTO twr_job_attempts (job_id, attempt_no, idempotency_key, action_type, mechanic, result, error_code, error_detail, placed_count, requested_count, occurred_at, server)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, $12)
		ON CONFLICT (server, job_id, attempt_no) DO NOTHING
	`, jobID, attemptNo, twrStringField(f, "idempotencyKey"), twrStringField(f, "actionType"), twrStringField(f, "mechanic"), result,
		twrStringField(f, "errorCode"), twrStringField(f, "errorDetail"), placedPtr, requestedPtr, ev.Timestamp, s.serverName); err != nil {
		return err
	}

	if result == "applied" {
		artifactKey := twrStringField(f, "artifactKey")
		x, hasX := twrIntField(f, "x")
		y, hasY := twrIntField(f, "y")
		z, hasZ := twrIntField(f, "z")
		if artifactKey != "" && hasX && hasY && hasZ {
			// Review Q3: artifactType (the placed thing, e.g.
			// "Base.Twigs") -> artifact_type; targetSummary (what it
			// was placed into) -> target_summary. Falls back to
			// targetType if the caller hasn't been updated to the
			// split fields yet, rather than silently storing nothing.
			artifactType := twrStringField(f, "artifactType")
			if artifactType == "" {
				artifactType = twrStringField(f, "targetType")
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO twr_world_artifacts (artifact_key, job_id, artifact_type, x, y, z, target_summary, applied_at, server)
				VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9)
				ON CONFLICT (artifact_key, server) DO NOTHING
			`, artifactKey, jobID, artifactType, x, y, z, twrStringField(f, "targetSummary"), ev.Timestamp, s.serverName); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

func (s *pgStore) getFileOffset(ctx context.Context, path string) (int64, error) {
	var offset int64
	err := s.pool.QueryRow(ctx, `SELECT byte_offset FROM processed_files WHERE file_path = $1`, path).Scan(&offset)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return offset, err
}

func (s *pgStore) setFileOffset(ctx context.Context, path string, offset int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO processed_files (file_path, byte_offset)
		VALUES ($1, $2)
		ON CONFLICT (file_path) DO UPDATE SET byte_offset = EXCLUDED.byte_offset
	`, path, offset)
	return err
}
