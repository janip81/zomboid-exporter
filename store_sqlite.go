package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"log/slog"
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
	activeCharBySteamID map[string]int64
}

func newSQLiteStore(ctx context.Context, path string) (*sqliteStore, error) {
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
	s := &sqliteStore{db: db, activeCharBySteamID: make(map[string]int64)}
	if err := s.loadActiveCharacters(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
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
		INSERT INTO players (steam_id, last_username, first_seen, last_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (steam_id) DO UPDATE
		SET last_username = excluded.last_username,
		    last_seen = excluded.last_seen
		WHERE players.last_seen < excluded.last_seen
	`, steamID, username, iso(seenAt), iso(seenAt))
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
		INSERT INTO characters (steam_id, character_number, created_at, is_alive)
		VALUES (?, ?, ?, 1)
	`, steamID, nextNum, iso(at))
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('login', ?, ?, ?, ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), details)
	if err != nil {
		slog.Warn("insert login event failed", "err", err)
	}
}

func (s *sqliteStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO characters (steam_id, character_number, created_at, is_alive)
		VALUES (?, ?, ?, 1)
	`, ev.SteamID, nextNum, iso(ev.Timestamp))
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('created_player', ?, ?, ?, '{}')
	`, ev.SteamID, charID, iso(ev.Timestamp))
	if err != nil {
		slog.Warn("insert created_player event failed", "err", err)
	}
}

func (s *sqliteStore) handleDied(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('died', ?, ?, ?, ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), details)
	if err != nil {
		slog.Warn("insert died event failed", "err", err)
	}
}

func (s *sqliteStore) handleLevelChanged(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('level_changed', ?, ?, ?, ?)
	`, ev.SteamID, charID, iso(ev.Timestamp), details)
	if err != nil {
		slog.Warn("insert level_changed event failed", "err", err)
	}
}

func (s *sqliteStore) handleSkills(ctx context.Context, ev *perkEvent) {
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
