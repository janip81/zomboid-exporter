package main

import (
	"context"
	_ "embed"
	"log/slog"
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
	activeCharBySteamID map[string]int64
}

func newPgStore(ctx context.Context, dsn string) (*pgStore, error) {
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
	s := &pgStore{pool: pool, activeCharBySteamID: make(map[string]int64)}
	if err := s.loadActiveCharacters(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
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
		INSERT INTO players (steam_id, last_username, first_seen, last_seen)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (steam_id) DO UPDATE
		SET last_username = EXCLUDED.last_username,
		    last_seen = EXCLUDED.last_seen
		WHERE players.last_seen < EXCLUDED.last_seen
	`, steamID, username, seenAt)
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
		INSERT INTO characters (steam_id, character_number, created_at, is_alive)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id
	`, steamID, nextNum, at).Scan(&id); err != nil {
		return 0, err
	}
	s.activeCharBySteamID[steamID] = id
	return id, nil
}

func (s *pgStore) handleLogin(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('login', $1, $2, $3, jsonb_build_object('hours_survived', $4::float8, 'x', $5, 'y', $6, 'z', $7))
	`, ev.SteamID, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z)
	if err != nil {
		slog.Warn("insert login event failed", "err", err)
	}
}

func (s *pgStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO characters (steam_id, character_number, created_at, is_alive)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id
	`, ev.SteamID, nextNum, ev.Timestamp).Scan(&charID)
	if err != nil {
		slog.Warn("insert character failed", "err", err)
		return
	}
	s.activeCharBySteamID[ev.SteamID] = charID
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('created_player', $1, $2, $3, '{}'::jsonb)
	`, ev.SteamID, charID, ev.Timestamp)
	if err != nil {
		slog.Warn("insert created_player event failed", "err", err)
	}
}

func (s *pgStore) handleDied(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('died', $1, $2, $3, jsonb_build_object('hours_survived', $4::float8, 'x', $5, 'y', $6, 'z', $7))
	`, ev.SteamID, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z)
	if err != nil {
		slog.Warn("insert died event failed", "err", err)
	}
}

func (s *pgStore) handleLevelChanged(ctx context.Context, ev *perkEvent) {
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
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details)
		VALUES ('level_changed', $1, $2, $3, jsonb_build_object('skill', $4::text, 'level', $5::int))
	`, ev.SteamID, charID, ev.Timestamp, ev.SkillName, ev.SkillLevel)
	if err != nil {
		slog.Warn("insert level_changed event failed", "err", err)
	}
}

func (s *pgStore) handleSkills(ctx context.Context, ev *perkEvent) {
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
