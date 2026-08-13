package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure Go, CGO-free -- keeps the distroless/CGO_ENABLED=0 build intact
)

//go:embed schema_sqlite.sql
var schemaSQLite string

// sqliteStore is the zero-external-dependency default: a single file on
// disk, no separate database server required. Same event-handling logic
// as pgStore, translated to SQLite's dialect (no JSONB, TIMESTAMPTZ, or
// native BOOLEAN -- see schema_sqlite.sql for the details).
type sqliteStore struct {
	db                  *sql.DB
	serverName          string
	activeCharBySteamID map[string]int64
	steamIDByUsername   map[string]string
}

func newSQLiteStore(ctx context.Context, path, serverName string) (*sqliteStore, error) {
	// _pragma params apply on every new connection modernc.org/sqlite opens.
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// SQLite allows only one writer at a time; a single connection avoids
	// SQLITE_BUSY errors under our own concurrent access pattern instead of
	// relying on busy-timeout retries.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schemaSQLite); err != nil {
		db.Close()
		return nil, err
	}
	s := &sqliteStore{db: db, serverName: serverName, activeCharBySteamID: make(map[string]int64), steamIDByUsername: make(map[string]string)}
	if err := s.migrateServerColumn(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateFileOffsetKeys(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateEventsSteamIDNullable(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.loadActiveCharacters(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrateServerColumn backfills the server column on databases that
// predate it -- see pgStore.migrateServerColumn's comment for the full
// rationale. SQLite has no "ADD COLUMN IF NOT EXISTS" or "ALTER COLUMN
// SET NOT NULL", so existence is checked via PRAGMA table_info first and
// the column stays nullable-in-principle (schemaSQL already defaults it
// to ” for fresh installs; the Go code always supplies it going
// forward).
func (s *sqliteStore) migrateServerColumn(ctx context.Context) error {
	for _, tbl := range []string{"players", "characters", "events"} {
		has, err := s.hasColumn(ctx, tbl, "server")
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE `+tbl+` ADD COLUMN server TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE `+tbl+` SET server = ? WHERE server = ''`, s.serverName); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateFileOffsetKeys converts processed_files rows keyed by full
// absolute path to basename-only keys -- see pgStore.migrateFileOffsetKeys
// for the full rationale (PZ moves a session's log file to a different
// path once archived by the next restart; basename is the only thing
// stable across that move). Harmless no-op once every row is already
// basename-keyed.
func (s *sqliteStore) migrateFileOffsetKeys(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path, byte_offset FROM processed_files WHERE file_path LIKE '%/%'`)
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
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO processed_files (file_path, byte_offset)
			VALUES (?, ?)
			ON CONFLICT (file_path) DO UPDATE SET byte_offset = MAX(processed_files.byte_offset, excluded.byte_offset)
		`, newKey, k.offset); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM processed_files WHERE file_path = ?`, k.oldKey); err != nil {
			return err
		}
	}
	return nil
}

// columnNotNull reports whether the given column is currently declared
// NOT NULL, via the same PRAGMA table_info(...) query hasColumn uses.
func (s *sqliteStore) columnNotNull(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return notNull != 0, nil
		}
	}
	return false, rows.Err()
}

// migrateEventsSteamIDNullable rebuilds the events table without the
// NOT NULL constraint on steam_id, for databases that predate
// world_stats -- see pgStore.migrateEventsSteamIDNullable's comment for
// the full rationale (a system-level event like world_stats has no
// player attached). SQLite has no ALTER COLUMN at all, so this is the
// standard SQLite pattern for changing a column constraint: create a
// new table with the desired schema, copy every row across, drop the
// old table, rename the new one into place. Skipped entirely (harmless
// no-op) once steam_id is already nullable, so this never runs more
// than once against a given database.
func (s *sqliteStore) migrateEventsSteamIDNullable(ctx context.Context) error {
	notNull, err := s.columnNotNull(ctx, "events", "steam_id")
	if err != nil {
		return err
	}
	if !notNull {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE events_new (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type   TEXT NOT NULL,
			steam_id     TEXT REFERENCES players(steam_id),
			character_id INTEGER REFERENCES characters(id),
			occurred_at  TEXT NOT NULL,
			details      TEXT NOT NULL DEFAULT '{}',
			server       TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events_new (id, event_type, steam_id, character_id, occurred_at, details, server)
		SELECT id, event_type, steam_id, character_id, occurred_at, details, server FROM events
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE events`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE events_new RENAME TO events`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_events_type_time ON events (event_type, occurred_at DESC)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_events_steam_id ON events (steam_id, occurred_at DESC)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) Close() {
	s.db.Close()
}

func iso(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *sqliteStore) loadActiveCharacters(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT steam_id, id FROM characters WHERE is_alive = 1`)
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

func (s *sqliteStore) upsertPlayer(ctx context.Context, steamID, username string, seenAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO players (steam_id, last_username, first_seen, last_seen, server)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (steam_id) DO UPDATE
		SET last_username = excluded.last_username,
		    last_seen = excluded.last_seen,
		    server = excluded.server
		WHERE players.last_seen < excluded.last_seen
	`, steamID, username, iso(seenAt), iso(seenAt), s.serverName)
	return err
}

func (s *sqliteStore) activeCharacter(ctx context.Context, steamID string, at time.Time) (int64, error) {
	if id, ok := s.activeCharBySteamID[steamID]; ok {
		return id, nil
	}

	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM characters WHERE steam_id = ? AND is_alive = 1
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&id)
	if err == nil {
		s.activeCharBySteamID[steamID] = id
		return id, nil
	}

	var nextNum int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(character_number), 0) + 1 FROM characters WHERE steam_id = ?
	`, steamID).Scan(&nextNum); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO characters (steam_id, character_number, created_at, is_alive, server)
		VALUES (?, ?, ?, 1, ?)
	`, steamID, nextNum, iso(at), s.serverName)
	if err != nil {
		return 0, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, err
	}
	s.activeCharBySteamID[steamID] = id
	return id, nil
}

func detailsJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (s *sqliteStore) handleLogin(ctx context.Context, ev *perkEvent) {
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
	details := detailsJSON(map[string]any{"hours_survived": ev.HoursSurvived, "x": ev.X, "y": ev.Y, "z": ev.Z})
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('login', ?, ?, ?, ?, ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), details, s.serverName)
	if err != nil {
		slog.Warn("insert login event failed", "err", err)
	}
}

func (s *sqliteStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	var nextNum int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(character_number), 0) + 1 FROM characters WHERE steam_id = ?
	`, ev.SteamID).Scan(&nextNum); err != nil {
		slog.Warn("next character_number lookup failed", "err", err)
		return
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO characters (steam_id, character_number, created_at, is_alive, server)
		VALUES (?, ?, ?, 1, ?)
	`, ev.SteamID, nextNum, iso(ev.Timestamp), s.serverName)
	if err != nil {
		slog.Warn("insert character failed", "err", err)
		return
	}
	charID, err := res.LastInsertId()
	if err != nil {
		slog.Warn("LastInsertId failed", "err", err)
		return
	}
	s.activeCharBySteamID[ev.SteamID] = charID
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('created_player', ?, ?, ?, '{}', ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), s.serverName)
	if err != nil {
		slog.Warn("insert created_player event failed", "err", err)
	}
}

func (s *sqliteStore) handleDied(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE characters
		SET is_alive = 0,
		    died_at = ?,
		    hours_survived_at_death = ?,
		    death_x = ?, death_y = ?, death_z = ?
		WHERE id = ?
	`, iso(ev.Timestamp), ev.HoursSurvived, ev.X, ev.Y, ev.Z, charID)
	if err != nil {
		slog.Warn("update character died failed", "err", err)
		return
	}
	delete(s.activeCharBySteamID, ev.SteamID)
	details := detailsJSON(map[string]any{"hours_survived": ev.HoursSurvived, "x": ev.X, "y": ev.Y, "z": ev.Z})
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('died', ?, ?, ?, ?, ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), details, s.serverName)
	if err != nil {
		slog.Warn("insert died event failed", "err", err)
	}
}

func (s *sqliteStore) handleLevelChanged(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO skill_snapshots (character_id, captured_at, skill_name, level)
		VALUES (?, ?, ?, ?)
	`, charID, iso(ev.Timestamp), ev.SkillName, ev.SkillLevel)
	if err != nil {
		slog.Warn("insert skill_snapshot (levelup) failed", "err", err)
	}
	details := detailsJSON(map[string]any{"skill": ev.SkillName, "level": ev.SkillLevel})
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('level_changed', ?, ?, ?, ?, ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), details, s.serverName)
	if err != nil {
		slog.Warn("insert level_changed event failed", "err", err)
	}
}

func (s *sqliteStore) handleSkills(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	charID, err := s.activeCharacter(ctx, ev.SteamID, ev.Timestamp)
	if err != nil {
		slog.Warn("activeCharacter failed", "err", err)
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("begin tx failed", "err", err)
		return
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO skill_snapshots (character_id, captured_at, skill_name, level)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		slog.Warn("prepare failed", "err", err)
		tx.Rollback()
		return
	}
	ts := iso(ev.Timestamp)
	for name, level := range ev.Skills {
		if _, err := stmt.ExecContext(ctx, charID, ts, name, level); err != nil {
			slog.Warn("insert skill_snapshot (dump) failed", "err", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		slog.Warn("commit skill_snapshots (dump) failed", "err", err)
	}
}

// rememberSteamID/canonicalSteamID -- see pgStore's copy of this comment
// for the full rationale (Lua-side SteamID64 precision loss).
func (s *sqliteStore) rememberSteamID(username, steamID string) {
	if username != "" && steamID != "" {
		s.steamIDByUsername[username] = steamID
	}
}

func (s *sqliteStore) canonicalSteamID(username, fallback string) string {
	if id, ok := s.steamIDByUsername[username]; ok {
		return id
	}
	return fallback
}

// handleExporterEvent persists a parsed ExporterLog.txt line generically:
// event_type is whatever the Lua mod's "type" field says, and the full
// decoded payload is kept verbatim in details -- see exporterlog.go.
func (s *sqliteStore) handleExporterEvent(ctx context.Context, ev *exporterEvent) {
	details := detailsJSON(ev.Fields)

	// Player-less system event (e.g. world_stats) -- see
	// pgStore.handleExporterEvent's comment on the same check for the
	// full rationale.
	if ev.Username == "" && ev.SteamID == "" {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
			VALUES (?, NULL, NULL, ?, ?, ?)
		`, ev.EventType, iso(ev.Timestamp), details, s.serverName); err != nil {
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
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES (?, ?, ?, ?, ?, ?)
	`, ev.EventType, steamID, charID, iso(ev.Timestamp), details, s.serverName); err != nil {
		slog.Warn("insert exporter event failed", "type", ev.EventType, "err", err)
	}
}

// handleSessionEvent persists a parsed connections.txt session_start/
// session_end line -- see connections.go. Not character-scoped (a
// session spans logins/deaths/new characters), so character_id is
// always NULL here, unlike the character-linked handlers above.
func (s *sqliteStore) handleSessionEvent(ctx context.Context, ev *sessionEvent) {
	s.rememberSteamID(ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES (?, ?, NULL, ?, '{}', ?)
	`, ev.Kind, ev.SteamID, iso(ev.Timestamp), s.serverName)
	if err != nil {
		slog.Warn("insert session event failed", "kind", ev.Kind, "err", err)
	}
}

// nullIfEmpty stores an empty string as NULL instead of "" -- database/sql
// stores Go's "" literally otherwise, unlike Postgres' NULLIF pattern
// pgStore's version of this handler uses.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// handleTWRJobResult mirrors pgStore.handleTWRJobResult -- see its
// comment for the full design rationale.
// handleTWRJobResult mirrors pgStore.handleTWRJobResult -- see its
// comment for the full design rationale (durability contract, review
// Q3/Q4/Q5/Q6).
func (s *sqliteStore) handleTWRJobResult(ctx context.Context, ev *twrEvent) error {
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

	var placedVal, requestedVal any
	if hasPlaced {
		placedVal = placed
	}
	if hasRequested {
		requestedVal = requested
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck -- no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO twr_job_attempts (job_id, attempt_no, idempotency_key, action_type, mechanic, result, error_code, error_detail, placed_count, requested_count, occurred_at, server)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server, job_id, attempt_no) DO NOTHING
	`, jobID, attemptNo, nullIfEmpty(twrStringField(f, "idempotencyKey")), twrStringField(f, "actionType"), twrStringField(f, "mechanic"), result,
		nullIfEmpty(twrStringField(f, "errorCode")), nullIfEmpty(twrStringField(f, "errorDetail")), placedVal, requestedVal, iso(ev.Timestamp), s.serverName); err != nil {
		return err
	}

	if result == "applied" {
		artifactKey := twrStringField(f, "artifactKey")
		x, hasX := twrIntField(f, "x")
		y, hasY := twrIntField(f, "y")
		z, hasZ := twrIntField(f, "z")
		if artifactKey != "" && hasX && hasY && hasZ {
			artifactType := twrStringField(f, "artifactType")
			if artifactType == "" {
				artifactType = twrStringField(f, "targetType")
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO twr_world_artifacts (artifact_key, job_id, artifact_type, x, y, z, target_summary, applied_at, server)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (artifact_key, server) DO NOTHING
			`, artifactKey, jobID, artifactType, x, y, z, nullIfEmpty(twrStringField(f, "targetSummary")), iso(ev.Timestamp), s.serverName); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) getFileOffset(ctx context.Context, path string) (int64, error) {
	var offset int64
	err := s.db.QueryRowContext(ctx, `SELECT byte_offset FROM processed_files WHERE file_path = ?`, path).Scan(&offset)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return offset, err
}

func (s *sqliteStore) setFileOffset(ctx context.Context, path string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO processed_files (file_path, byte_offset)
		VALUES (?, ?)
		ON CONFLICT (file_path) DO UPDATE SET byte_offset = excluded.byte_offset
	`, path, offset)
	return err
}
