package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
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
	db         *sql.DB
	serverName string

	// mu protects everything below -- see pgStore's copy of this comment;
	// the same three independently-ticking goroutines share this store.
	mu                  sync.Mutex
	activeCharBySteamID map[string]int64
	steamIDByUsername   map[string]string
	pendingByUsername   map[string][]pendingExporterEvent
	pendingTotal        int
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
	s := &sqliteStore{
		db:                  db,
		serverName:          serverName,
		activeCharBySteamID: make(map[string]int64),
		steamIDByUsername:   make(map[string]string),
		pendingByUsername:   make(map[string][]pendingExporterEvent),
	}
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
	if err := s.migrateTWRJobAttemptsSteamID(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateCharacterStatsColumns(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.loadActiveCharacters(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.preloadCanonicalSteamIDs(ctx); err != nil {
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

// migrateTWRJobAttemptsSteamID adds the steam_id and artifact_key
// columns (2026-08-14, for the quest engine's signal producers -- see
// pgStore.handleTWRJobResult's comment) to databases created before
// they existed. SQLite has no ADD COLUMN IF NOT EXISTS, hence the
// hasColumn() check first, same pattern as migrateServerColumn.
func (s *sqliteStore) migrateTWRJobAttemptsSteamID(ctx context.Context) error {
	for _, col := range []string{"steam_id", "artifact_key"} {
		has, err := s.hasColumn(ctx, "twr_job_attempts", col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE twr_job_attempts ADD COLUMN `+col+` TEXT`); err != nil {
			return err
		}
	}
	return nil
}

// migrateCharacterStatsColumns adds the per-life aggregate columns to
// characters -- see schema_stats_postgres.sql's comment on the same
// columns (character-aggregate-stats.md). Postgres declares these inline
// via ADD COLUMN IF NOT EXISTS in schema_stats_postgres.sql; SQLite has
// no such clause, hence the hasColumn() check, same pattern as
// migrateTWRJobAttemptsSteamID above.
func (s *sqliteStore) migrateCharacterStatsColumns(ctx context.Context) error {
	cols := []struct{ name, ddl string }{
		{"zombie_kills", `ALTER TABLE characters ADD COLUMN zombie_kills INTEGER NOT NULL DEFAULT 0`},
		{"injuries", `ALTER TABLE characters ADD COLUMN injuries INTEGER NOT NULL DEFAULT 0`},
		{"distance_walked_km", `ALTER TABLE characters ADD COLUMN distance_walked_km REAL NOT NULL DEFAULT 0`},
		{"distance_driven_km", `ALTER TABLE characters ADD COLUMN distance_driven_km REAL NOT NULL DEFAULT 0`},
		{"drinks", `ALTER TABLE characters ADD COLUMN drinks INTEGER NOT NULL DEFAULT 0`},
		{"alcohol_ml", `ALTER TABLE characters ADD COLUMN alcohol_ml REAL NOT NULL DEFAULT 0`},
		{"pills_taken", `ALTER TABLE characters ADD COLUMN pills_taken INTEGER NOT NULL DEFAULT 0`},
		{"books_read", `ALTER TABLE characters ADD COLUMN books_read INTEGER NOT NULL DEFAULT 0`},
		{"vehicle_collisions", `ALTER TABLE characters ADD COLUMN vehicle_collisions INTEGER NOT NULL DEFAULT 0`},
		{"indoor_hours", `ALTER TABLE characters ADD COLUMN indoor_hours REAL NOT NULL DEFAULT 0`},
		{"outdoor_hours", `ALTER TABLE characters ADD COLUMN outdoor_hours REAL NOT NULL DEFAULT 0`},
		{"last_event_at", `ALTER TABLE characters ADD COLUMN last_event_at TEXT`},
		{"stats_finalized", `ALTER TABLE characters ADD COLUMN stats_finalized INTEGER NOT NULL DEFAULT 0`},
		{"stats_finalized_at", `ALTER TABLE characters ADD COLUMN stats_finalized_at TEXT`},
		{"stats_revision", `ALTER TABLE characters ADD COLUMN stats_revision INTEGER NOT NULL DEFAULT 1`},
	}
	for _, c := range cols {
		has, err := s.hasColumn(ctx, "characters", c.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.ExecContext(ctx, c.ddl); err != nil {
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
		s.mu.Lock()
		s.activeCharBySteamID[steamID] = id
		s.mu.Unlock()
	}
	return rows.Err()
}

// preloadCanonicalSteamIDs mirrors pgStore.preloadCanonicalSteamIDs -- see
// its comment for the full rationale.
func (s *sqliteStore) preloadCanonicalSteamIDs(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT last_username, MAX(steam_id)
		FROM players
		WHERE server = ?
		GROUP BY last_username
		HAVING COUNT(DISTINCT steam_id) = 1
	`, s.serverName)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var username, steamID string
		if err := rows.Scan(&username, &steamID); err != nil {
			return err
		}
		s.steamIDByUsername[username] = steamID
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
	s.mu.Lock()
	id, ok := s.activeCharBySteamID[steamID]
	s.mu.Unlock()
	if ok {
		return id, nil
	}

	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM characters WHERE steam_id = ? AND is_alive = 1
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&id)
	if err == nil {
		s.mu.Lock()
		s.activeCharBySteamID[steamID] = id
		s.mu.Unlock()
		return id, nil
	}

	// No alive character on record but this steamID may already HAVE
	// character history -- see pgStore.activeCharacter's comment for the
	// full rationale (a stray/late event must reuse the most recent
	// existing character rather than fabricate a phantom new "alive" one).
	if err := s.db.QueryRowContext(ctx, `
		SELECT id FROM characters WHERE steam_id = ?
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&id); err == nil {
		s.mu.Lock()
		s.activeCharBySteamID[steamID] = id
		s.mu.Unlock()
		return id, nil
	}

	// Genuinely no character history at all yet -- true cold start.
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
	s.mu.Lock()
	s.activeCharBySteamID[steamID] = id
	s.mu.Unlock()
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
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	// character-aggregate-stats.md's finalization trigger 1 -- see
	// pgStore.handleCreatedPlayer's comment for the full rationale.
	if err := s.finalizeDeadCharacters(ctx, ev.SteamID, ev.Timestamp); err != nil {
		slog.Warn("finalize previous character failed", "err", err)
	}
	var nextNum int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(character_number), 0) + 1 FROM characters WHERE steam_id = ?
	`, ev.SteamID).Scan(&nextNum); err != nil {
		slog.Warn("next character_number lookup failed", "err", err)
		return
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO characters (steam_id, character_number, created_at, is_alive, server, stats_revision)
		VALUES (?, ?, ?, 1, ?, ?)
	`, ev.SteamID, nextNum, iso(ev.Timestamp), s.serverName, currentStatsRevision)
	if err != nil {
		slog.Warn("insert character failed", "err", err)
		return
	}
	charID, err := res.LastInsertId()
	if err != nil {
		slog.Warn("LastInsertId failed", "err", err)
		return
	}
	s.mu.Lock()
	s.activeCharBySteamID[ev.SteamID] = charID
	s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('created_player', ?, ?, ?, '{}', ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), s.serverName)
	if err != nil {
		slog.Warn("insert created_player event failed", "err", err)
	}
}

func (s *sqliteStore) handleDied(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
	s.mu.Lock()
	delete(s.activeCharBySteamID, ev.SteamID)
	s.mu.Unlock()
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
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
// rememberSteamID/canonicalSteamID -- see pgStore's copy of this comment
// for the full rationale (Lua-side SteamID64 precision loss, the hard
// rule against ever trusting a Lua-derived value as a fallback, and the
// pending-event queue this now flushes).
func (s *sqliteStore) rememberSteamID(ctx context.Context, username, steamID string) {
	if username == "" || steamID == "" {
		return
	}
	s.mu.Lock()
	s.steamIDByUsername[username] = steamID
	pending := s.pendingByUsername[username]
	delete(s.pendingByUsername, username)
	s.pendingTotal -= len(pending)
	s.mu.Unlock()

	for _, p := range pending {
		s.ingestExporterEvent(ctx, p.ev, steamID)
	}
}

func (s *sqliteStore) canonicalSteamID(username string) (steamID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.steamIDByUsername[username]
	return id, ok
}

// enqueuePendingExporterEvent mirrors pgStore.enqueuePendingExporterEvent
// -- see its comment for the full rationale.
func (s *sqliteStore) enqueuePendingExporterEvent(ev *exporterEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredPendingLocked()

	if len(s.pendingByUsername[ev.Username]) >= pendingEventsPerUserMax || s.pendingTotal >= pendingEventsGlobalMax {
		slog.Warn("dropping ExporterLog event: pending-canonical-SteamID queue full", "type", ev.EventType, "username", ev.Username)
		return
	}
	s.pendingByUsername[ev.Username] = append(s.pendingByUsername[ev.Username], pendingExporterEvent{ev: ev, queuedAt: time.Now()})
	s.pendingTotal++
}

// evictExpiredPendingLocked mirrors pgStore.evictExpiredPendingLocked --
// see its comment for the full rationale. Caller must hold s.mu.
func (s *sqliteStore) evictExpiredPendingLocked() {
	now := time.Now()
	for username, pending := range s.pendingByUsername {
		kept := pending[:0]
		for _, p := range pending {
			if now.Sub(p.queuedAt) > pendingEventTTL {
				slog.Warn("dropping ExporterLog event: no canonical SteamID within TTL", "type", p.ev.EventType, "username", username, "ttl", pendingEventTTL)
				s.pendingTotal--
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == 0 {
			delete(s.pendingByUsername, username)
		} else {
			s.pendingByUsername[username] = kept
		}
	}
}

// handleExporterEvent persists a parsed ExporterLog.txt line generically:
// event_type is whatever the Lua mod's "type" field says, and the full
// decoded payload is kept verbatim in details -- see exporterlog.go.
func (s *sqliteStore) handleExporterEvent(ctx context.Context, ev *exporterEvent) {
	// Player-less system event (e.g. world_stats) -- see
	// pgStore.handleExporterEvent's comment on the same check for the
	// full rationale.
	if ev.Username == "" && ev.SteamID == "" {
		details := detailsJSON(ev.Fields)
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
			VALUES (?, NULL, NULL, ?, ?, ?)
		`, ev.EventType, iso(ev.Timestamp), details, s.serverName); err != nil {
			slog.Warn("insert player-less exporter event failed", "type", ev.EventType, "err", err)
		}
		return
	}

	// steamid64-canonicalization-and-lua-precision.md's hard rule --
	// see pgStore.handleExporterEvent's comment for the full rationale.
	steamID, ok := s.canonicalSteamID(ev.Username)
	if !ok {
		s.enqueuePendingExporterEvent(ev)
		return
	}
	s.ingestExporterEvent(ctx, ev, steamID)
}

// ingestExporterEvent mirrors pgStore.ingestExporterEvent -- see its
// comment for the full rationale.
func (s *sqliteStore) ingestExporterEvent(ctx context.Context, ev *exporterEvent, steamID string) {
	details := detailsJSON(canonicalizeExporterFields(ev.Fields, steamID))

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
		return
	}
	if err := s.applyCharacterStatDelta(ctx, charID, aggregateDeltaForEvent(ev.EventType, ev.Fields), ev.Timestamp); err != nil {
		slog.Warn("apply character stat delta failed", "type", ev.EventType, "err", err)
	}
}

// applyCharacterStatDelta mirrors pgStore.applyCharacterStatDelta -- see
// its comment for the full rationale. SQLite has no GREATEST(); ISO-8601
// TEXT timestamps compare correctly lexicographically, so a CASE
// expression does the same job without the NULL-propagation trap SQLite's
// own scalar max(x,y) has (it returns NULL if EITHER argument is NULL,
// unlike Postgres' GREATEST which ignores NULLs).
func (s *sqliteStore) applyCharacterStatDelta(ctx context.Context, charID int64, d characterStatDelta, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE characters
		SET zombie_kills = zombie_kills + ?,
		    injuries = injuries + ?,
		    distance_walked_km = distance_walked_km + ?,
		    distance_driven_km = distance_driven_km + ?,
		    drinks = drinks + ?,
		    alcohol_ml = alcohol_ml + ?,
		    pills_taken = pills_taken + ?,
		    books_read = books_read + ?,
		    indoor_hours = indoor_hours + ?,
		    outdoor_hours = outdoor_hours + ?,
		    last_event_at = CASE WHEN last_event_at IS NULL OR ? > last_event_at THEN ? ELSE last_event_at END
		WHERE id = ? AND stats_finalized = 0
	`, d.ZombieKills, d.Injuries, d.DistanceWalkedKm, d.DistanceDrivenKm,
		d.Drinks, d.AlcoholMl, d.PillsTaken, d.BooksRead, d.IndoorHours, d.OutdoorHours,
		iso(at), iso(at), charID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 || len(d.Breakdown) == 0 {
		return nil
	}
	for _, b := range d.Breakdown {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO character_stat_breakdown (character_id, category, value_key, value, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (character_id, category, value_key)
			DO UPDATE SET value = character_stat_breakdown.value + excluded.value, updated_at = excluded.updated_at
		`, charID, b.Category, b.ValueKey, b.Value, iso(at)); err != nil {
			return err
		}
	}
	return nil
}

// finalizeDeadCharacters mirrors pgStore.finalizeDeadCharacters.
func (s *sqliteStore) finalizeDeadCharacters(ctx context.Context, steamID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE characters
		SET stats_finalized = 1, stats_finalized_at = ?, stats_revision = ?
		WHERE steam_id = ? AND is_alive = 0 AND stats_finalized = 0
	`, iso(at), currentStatsRevision, steamID)
	return err
}

// finalizeStaleCharacters implements the eventStore interface -- see
// pgStore.finalizeStaleCharacters's comment.
func (s *sqliteStore) finalizeStaleCharacters(ctx context.Context, graceWindow time.Duration) (int64, error) {
	cutoff := iso(time.Now().Add(-graceWindow))
	res, err := s.db.ExecContext(ctx, `
		UPDATE characters
		SET stats_finalized = 1, stats_finalized_at = ?, stats_revision = ?
		WHERE is_alive = 0 AND stats_finalized = 0
		  AND COALESCE(last_event_at, died_at) < ?
	`, iso(time.Now()), currentStatsRevision, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// reconcileAllCharacterStats implements the eventStore interface -- see
// pgStore.reconcileAllCharacterStats's comment.
func (s *sqliteStore) reconcileAllCharacterStats(ctx context.Context) (checked int, repaired int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM characters`)
	if err != nil {
		return 0, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	rows.Close()

	for _, id := range ids {
		changed, rErr := s.reconcileCharacterStats(ctx, id)
		if rErr != nil {
			slog.Warn("reconcile character stats failed", "characterID", id, "err", rErr)
			continue
		}
		checked++
		if changed {
			repaired++
		}
	}
	return checked, repaired, nil
}

// reconcileCharacterStats mirrors pgStore.reconcileCharacterStats -- see
// its comment for the full rationale.
func (s *sqliteStore) reconcileCharacterStats(ctx context.Context, characterID int64) (changed bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_type, details FROM events WHERE character_id = ?`, characterID)
	if err != nil {
		return false, err
	}
	var total characterStatDelta
	breakdown := map[[2]string]float64{}
	for rows.Next() {
		var eventType, details string
		if err := rows.Scan(&eventType, &details); err != nil {
			rows.Close()
			return false, err
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(details), &fields); err != nil {
			continue
		}
		d := aggregateDeltaForEvent(eventType, fields)
		total.ZombieKills += d.ZombieKills
		total.Injuries += d.Injuries
		total.DistanceWalkedKm += d.DistanceWalkedKm
		total.DistanceDrivenKm += d.DistanceDrivenKm
		total.Drinks += d.Drinks
		total.AlcoholMl += d.AlcoholMl
		total.PillsTaken += d.PillsTaken
		total.BooksRead += d.BooksRead
		total.IndoorHours += d.IndoorHours
		total.OutdoorHours += d.OutdoorHours
		for _, b := range d.Breakdown {
			breakdown[[2]string{b.Category, b.ValueKey}] += b.Value
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()

	var stored characterStatDelta
	if err := s.db.QueryRowContext(ctx, `
		SELECT zombie_kills, injuries, distance_walked_km, distance_driven_km,
		       drinks, alcohol_ml, pills_taken, books_read, indoor_hours, outdoor_hours
		FROM characters WHERE id = ?
	`, characterID).Scan(&stored.ZombieKills, &stored.Injuries, &stored.DistanceWalkedKm, &stored.DistanceDrivenKm,
		&stored.Drinks, &stored.AlcoholMl, &stored.PillsTaken, &stored.BooksRead, &stored.IndoorHours, &stored.OutdoorHours); err != nil {
		return false, err
	}

	if statAggregatesEqual(stored, total) {
		return false, nil
	}

	slog.Warn("character stats reconciliation found drift, repairing",
		"characterID", characterID,
		"storedKills", stored.ZombieKills, "recomputedKills", total.ZombieKills,
		"storedInjuries", stored.Injuries, "recomputedInjuries", total.Injuries)

	if _, err := s.db.ExecContext(ctx, `
		UPDATE characters
		SET zombie_kills = ?, injuries = ?, distance_walked_km = ?, distance_driven_km = ?,
		    drinks = ?, alcohol_ml = ?, pills_taken = ?, books_read = ?,
		    indoor_hours = ?, outdoor_hours = ?, stats_revision = ?
		WHERE id = ?
	`, total.ZombieKills, total.Injuries, total.DistanceWalkedKm, total.DistanceDrivenKm,
		total.Drinks, total.AlcoholMl, total.PillsTaken, total.BooksRead, total.IndoorHours, total.OutdoorHours,
		currentStatsRevision, characterID); err != nil {
		return false, err
	}
	now := iso(time.Now())
	for k, v := range breakdown {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO character_stat_breakdown (character_id, category, value_key, value, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (character_id, category, value_key)
			DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		`, characterID, k[0], k[1], v, now); err != nil {
			return true, err
		}
	}
	return true, nil
}

// handleSessionEvent persists a parsed connections.txt session_start/
// session_end line -- see connections.go. Not character-scoped (a
// session spans logins/deaths/new characters), so character_id is
// always NULL here, unlike the character-linked handlers above.
func (s *sqliteStore) handleSessionEvent(ctx context.Context, ev *sessionEvent) {
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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

	artifactKey := twrStringField(f, "artifactKey")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO twr_job_attempts (job_id, attempt_no, idempotency_key, action_type, mechanic, result, error_code, error_detail, placed_count, requested_count, occurred_at, server, steam_id, artifact_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server, job_id, attempt_no) DO NOTHING
	`, jobID, attemptNo, nullIfEmpty(twrStringField(f, "idempotencyKey")), twrStringField(f, "actionType"), twrStringField(f, "mechanic"), result,
		nullIfEmpty(twrStringField(f, "errorCode")), nullIfEmpty(twrStringField(f, "errorDetail")), placedVal, requestedVal, iso(ev.Timestamp), s.serverName, nullIfEmpty(twrStringField(f, "steamId")), nullIfEmpty(artifactKey)); err != nil {
		return err
	}

	if result == "applied" {
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

// handleTWRJobAccepted is a no-op -- see eventstore.go's interface
// comment. The quest engine (questengine.go) is Postgres-only by
// design; SQLite never has a twr_jobs row for this receipt to update
// against, so there's nothing to do here.
func (s *sqliteStore) handleTWRJobAccepted(ctx context.Context, ev *twrEvent) error {
	return nil
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
