package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pending-ExporterLog-event queue tuning -- see rememberSteamID/
// canonicalSteamID's comment and steamid64-canonicalization-and-lua-
// precision.md. pendingEventTTL is generous relative to the 3s poll
// interval (perklog.go/connections.go/exporterlog.go) since a session's
// native connections.txt/PerkLog.txt line for a brand-new player can lag
// behind their first in-game action by a few polls.
const (
	pendingEventTTL         = 45 * time.Second
	pendingEventsPerUserMax = 20
	pendingEventsGlobalMax  = 500
)

// pendingExporterEvent is a player-scoped ExporterLog event held in memory
// because no canonical (native-sourced) SteamID64 is known yet for its
// username -- see steamid64-canonicalization-and-lua-precision.md's hard
// rule that a Lua/Kahlua-derived SteamID64 must never create durable
// player identity.
type pendingExporterEvent struct {
	ev       *exporterEvent
	queuedAt time.Time
}

//go:embed schema_stats_postgres.sql
var schemaStatsSQL string

// schema_twr_postgres.sql is applied ONLY when twrEnabled is true (see
// newPgStore) -- split from the stats schema 2026-08-14 per
// zomboid-exporter-ideas/antagonist/design/exporter-twr-feature-
// boundary.md: a plain stats-only deployment must never get
// twr_campaigns/twr_quest_instances/twr_jobs/etc. just because Postgres
// happens to be configured for player/death/skill history.
//
//go:embed schema_twr_postgres.sql
var schemaTWRSQL string

// pgStore owns the Postgres connection and a small in-memory cache of each
// player's currently-alive character, so "skills"/"level_changed" lines
// (which carry no character identifier of their own) can be attributed to
// the right character row without a query on every line.
type pgStore struct {
	pool       *pgxpool.Pool
	serverName string

	// mu protects everything below -- all three maps are read/written
	// from three independently-ticking goroutines (runPerkLogPipeline,
	// runExporterLogPipeline, runConnectionsPipeline in main.go all share
	// this one *pgStore), which plain unsynchronized map access does not
	// support safely.
	mu                  sync.Mutex
	activeCharBySteamID map[string]int64
	steamIDByUsername   map[string]string
	pendingByUsername   map[string][]pendingExporterEvent
	pendingTotal        int
}

func newPgStore(ctx context.Context, dsn, serverName string, twrEnabled bool) (*pgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, schemaStatsSQL); err != nil {
		pool.Close()
		return nil, err
	}
	if twrEnabled {
		if _, err := pool.Exec(ctx, schemaTWRSQL); err != nil {
			pool.Close()
			return nil, err
		}
	}
	s := &pgStore{
		pool:                pool,
		serverName:          serverName,
		activeCharBySteamID: make(map[string]int64),
		steamIDByUsername:   make(map[string]string),
		pendingByUsername:   make(map[string][]pendingExporterEvent),
	}
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
	if err := s.preloadCanonicalSteamIDs(ctx); err != nil {
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
		s.mu.Lock()
		s.activeCharBySteamID[steamID] = id
		s.mu.Unlock()
	}
	return rows.Err()
}

// preloadCanonicalSteamIDs seeds steamIDByUsername from existing DB
// history at startup, per steamid64-canonicalization-and-lua-
// precision.md's "Preload canonical cache at startup" -- without this,
// the exporter begins every restart with an empty identity cache even
// for players it has known about for weeks, widening the cold-start
// window where a Lua-derived SteamID64 would otherwise have to be
// queued/dropped. Only genuinely unambiguous usernames are loaded
// (COUNT(DISTINCT steam_id) = 1): an existing dirty duplicate (two
// SteamIDs sharing a username) is NOT guessed at here -- that requires
// an explicit reconciliation, not a preload heuristic.
func (s *pgStore) preloadCanonicalSteamIDs(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT last_username, MAX(steam_id)
		FROM players
		WHERE server = $1
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
	s.mu.Lock()
	id, ok := s.activeCharBySteamID[steamID]
	s.mu.Unlock()
	if ok {
		return id, nil
	}

	err := s.pool.QueryRow(ctx, `
		SELECT id FROM characters WHERE steam_id = $1 AND is_alive
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&id)
	if err == nil {
		s.mu.Lock()
		s.activeCharBySteamID[steamID] = id
		s.mu.Unlock()
		return id, nil
	}

	// No alive character on record. A stray/late event for a steamID
	// that already HAS character history (e.g. an "injury" line that
	// arrives a moment after a "died" line, before the real respawn's
	// created_player event lands) must NOT fabricate a brand-new "alive"
	// character just to have somewhere to attach -- confirmed live: an
	// injury event 1.4s after a death created exactly such a phantom,
	// and the genuine respawn 27s later created a second, real
	// character, leaving two simultaneously is_alive=TRUE rows for one
	// player. Reuse the most recent existing character instead; the
	// real respawn's handleCreatedPlayer will overwrite the cache with
	// the correct new character as soon as it actually arrives.
	if err := s.pool.QueryRow(ctx, `
		SELECT id FROM characters WHERE steam_id = $1
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&id); err == nil {
		s.mu.Lock()
		s.activeCharBySteamID[steamID] = id
		s.mu.Unlock()
		return id, nil
	}

	// Genuinely no character history at all yet -- the true cold-start
	// case this fallback exists for (exporter starts mid-session, after
	// a character already existed in-game). Create character_number 1
	// (or next free number) so later events have somewhere to attach.
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
	s.mu.Lock()
	s.activeCharBySteamID[steamID] = id
	s.mu.Unlock()
	return id, nil
}

func (s *pgStore) handleLogin(ctx context.Context, ev *perkEvent) {
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
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('login', $1, $2, $3, jsonb_build_object('hours_survived', $4::float8, 'x', $5::int, 'y', $6::int, 'z', $7::int), $8)
	`, ev.SteamID, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z, s.serverName)
	if err != nil {
		slog.Warn("insert login event failed", "err", err)
	}
}

func (s *pgStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
	if err := s.upsertPlayer(ctx, ev.SteamID, ev.Username, ev.Timestamp); err != nil {
		slog.Warn("upsertPlayer failed", "err", err)
		return
	}
	// character-aggregate-stats.md's finalization trigger 1: a genuine
	// new life is the strongest possible signal the previous dead one is
	// over -- finalize it now rather than waiting out the grace-window
	// sweep.
	if err := s.finalizeDeadCharacters(ctx, ev.SteamID, ev.Timestamp); err != nil {
		slog.Warn("finalize previous character failed", "err", err)
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
		INSERT INTO characters (steam_id, character_number, created_at, is_alive, server, stats_revision)
		VALUES ($1, $2, $3, TRUE, $4, $5)
		RETURNING id
	`, ev.SteamID, nextNum, ev.Timestamp, s.serverName, currentStatsRevision).Scan(&charID)
	if err != nil {
		slog.Warn("insert character failed", "err", err)
		return
	}
	s.mu.Lock()
	s.activeCharBySteamID[ev.SteamID] = charID
	s.mu.Unlock()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('created_player', $1, $2, $3, '{}'::jsonb, $4)
	`, ev.SteamID, charID, ev.Timestamp, s.serverName)
	if err != nil {
		slog.Warn("insert created_player event failed", "err", err)
	}
}

func (s *pgStore) handleDied(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
	s.mu.Lock()
	delete(s.activeCharBySteamID, ev.SteamID)
	s.mu.Unlock()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
		VALUES ('died', $1, $2, $3, jsonb_build_object('hours_survived', $4::float8, 'x', $5::int, 'y', $6::int, 'z', $7::int), $8)
	`, ev.SteamID, charID, ev.Timestamp, ev.HoursSurvived, ev.X, ev.Y, ev.Z, s.serverName)
	if err != nil {
		slog.Warn("insert died event failed", "err", err)
	}
}

func (s *pgStore) handleLevelChanged(ctx context.Context, ev *perkEvent) {
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
// precision for a real SteamID64 (Lua numbers are doubles, exact only to
// 2^53 -- a SteamID64 is ~7.6e16). PerkLog.txt and connections.txt are
// both written natively by the Java engine and never pass through that
// lossy conversion, so their steam_id is always exact -- rememberSteamID
// is called from every handler sourced from those two files to build a
// username -> correct-steam_id cache. Per
// steamid64-canonicalization-and-lua-precision.md's hard rule, a
// Lua/Kahlua-derived SteamID64 (handleExporterEvent's ev.SteamID) must
// NEVER be trusted as a fallback when the cache doesn't have an answer
// yet -- doing so previously let a corrupted value create a permanent,
// wrong players row during the cold-start window before this cache
// warms up. Instead, rememberSteamID flushes any ExporterLog events that
// were queued for this username while waiting (see
// enqueuePendingExporterEvent).
func (s *pgStore) rememberSteamID(ctx context.Context, username, steamID string) {
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

// canonicalSteamID returns the native-sourced SteamID64 for username, if
// one is known yet. ok=false means "not yet known" -- callers must queue
// or drop, never substitute a Lua-derived value (see rememberSteamID's
// comment).
func (s *pgStore) canonicalSteamID(username string) (steamID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.steamIDByUsername[username]
	return id, ok
}

// enqueuePendingExporterEvent holds a player-scoped ExporterLog event in
// memory until rememberSteamID learns this username's canonical SteamID
// (or the TTL expires and it's dropped with a warning -- see
// steamid64-canonicalization-and-lua-precision.md's "Required ingestion
// behavior"). Bounded both per-username and globally so a flood of events
// for usernames that never get a canonical mapping (e.g. a bot account,
// or a genuinely broken PerkLog/connections pipeline) can't grow this
// queue without limit.
func (s *pgStore) enqueuePendingExporterEvent(ev *exporterEvent) {
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

// evictExpiredPendingLocked drops pending events older than
// pendingEventTTL, loudly (steamid64-canonicalization-and-lua-
// precision.md: "log expiration/drop loudly ... never fall back to Lua
// SteamID when TTL expires"). Caller must hold s.mu.
func (s *pgStore) evictExpiredPendingLocked() {
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
func (s *pgStore) handleExporterEvent(ctx context.Context, ev *exporterEvent) {
	// Player-less system event (e.g. world_stats -- the first event
	// type this mod ever emits with no player attached at all).
	// Confirmed by BOTH username and steamId being empty: every real
	// player-scoped event always carries a username even on the rare
	// occasion steamId resolution itself fails, so that failure case
	// still falls through to the queue-or-drop path below, unchanged.
	// No player to upsert or active-character lookup to do -- steam_id/
	// character_id are just NULL (see migrateEventsSteamIDNullable for
	// why that's a safe column type).
	if ev.Username == "" && ev.SteamID == "" {
		details, err := json.Marshal(ev.Fields)
		if err != nil {
			slog.Warn("marshal ExporterLog details failed", "type", ev.EventType, "err", err)
			return
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO events (event_type, steam_id, character_id, occurred_at, details, server)
			VALUES ($1, NULL, NULL, $2, $3::jsonb, $4)
		`, ev.EventType, ev.Timestamp, string(details), s.serverName); err != nil {
			slog.Warn("insert player-less exporter event failed", "type", ev.EventType, "err", err)
		}
		return
	}

	// steamid64-canonicalization-and-lua-precision.md's hard rule:
	// ev.SteamID (Lua/Kahlua-derived) is diagnostic-only and must never
	// create or select durable player identity. Only a canonical,
	// native-sourced mapping may be used -- if one isn't known yet, queue
	// the event instead of trusting ev.SteamID as a fallback.
	steamID, ok := s.canonicalSteamID(ev.Username)
	if !ok {
		s.enqueuePendingExporterEvent(ev)
		return
	}
	s.ingestExporterEvent(ctx, ev, steamID)
}

// ingestExporterEvent does the actual player upsert + character
// resolution + event insert for a Lua ExporterLog event, given an
// already-canonical steamID -- called either directly from
// handleExporterEvent (cache hit) or later from rememberSteamID's flush
// of a previously-queued event (cache was just populated). Canonicalizes
// the stored details JSON too (steamid64-canonicalization-and-lua-
// precision.md's "Canonicalize the event JSON too"): the corrupted
// Lua-derived value, if present and different, is preserved under
// "_luaSteamId" for diagnostics rather than left under the normal
// "steamId" key where it could be mistaken for authoritative identity.
func (s *pgStore) ingestExporterEvent(ctx context.Context, ev *exporterEvent, steamID string) {
	details, err := json.Marshal(canonicalizeExporterFields(ev.Fields, steamID))
	if err != nil {
		slog.Warn("marshal ExporterLog details failed", "type", ev.EventType, "err", err)
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
		return
	}
	if err := s.applyCharacterStatDelta(ctx, charID, aggregateDeltaForEvent(ev.EventType, ev.Fields), ev.Timestamp); err != nil {
		slog.Warn("apply character stat delta failed", "type", ev.EventType, "err", err)
	}
}

// applyCharacterStatDelta adds d's contribution to charID's aggregate
// columns and last_event_at, and records any breakdown entries -- but
// only if the character isn't finalized yet (character-aggregate-
// stats.md: "Normal live updates must include WHERE stats_finalized =
// false"). A late event for an already-finalized character is silently
// a no-op here; reconciliation is the only path allowed to still touch
// it.
func (s *pgStore) applyCharacterStatDelta(ctx context.Context, charID int64, d characterStatDelta, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE characters
		SET zombie_kills = zombie_kills + $2,
		    injuries = injuries + $3,
		    distance_walked_km = distance_walked_km + $4,
		    distance_driven_km = distance_driven_km + $5,
		    drinks = drinks + $6,
		    alcohol_ml = alcohol_ml + $7,
		    pills_taken = pills_taken + $8,
		    books_read = books_read + $9,
		    indoor_hours = indoor_hours + $10,
		    outdoor_hours = outdoor_hours + $11,
		    last_event_at = GREATEST(last_event_at, $12)
		WHERE id = $1 AND stats_finalized = false
	`, charID, d.ZombieKills, d.Injuries, d.DistanceWalkedKm, d.DistanceDrivenKm,
		d.Drinks, d.AlcoholMl, d.PillsTaken, d.BooksRead, d.IndoorHours, d.OutdoorHours, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 || len(d.Breakdown) == 0 {
		return nil
	}
	for _, b := range d.Breakdown {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO character_stat_breakdown (character_id, category, value_key, value, updated_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (character_id, category, value_key)
			DO UPDATE SET value = character_stat_breakdown.value + EXCLUDED.value, updated_at = EXCLUDED.updated_at
		`, charID, b.Category, b.ValueKey, b.Value, at); err != nil {
			return err
		}
	}
	return nil
}

// finalizeDeadCharacters locks live aggregation for every dead,
// not-yet-finalized character belonging to steamID -- see
// handleCreatedPlayer's call site (finalization trigger 1).
func (s *pgStore) finalizeDeadCharacters(ctx context.Context, steamID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE characters
		SET stats_finalized = true, stats_finalized_at = $2, stats_revision = $3
		WHERE steam_id = $1 AND is_alive = false AND stats_finalized = false
	`, steamID, at, currentStatsRevision)
	return err
}

// finalizeStaleCharacters implements the eventStore interface -- see its
// comment. graceWindow's cutoff is measured from last_event_at, falling
// back to died_at for a character that received no further telemetry at
// all after death.
func (s *pgStore) finalizeStaleCharacters(ctx context.Context, graceWindow time.Duration) (int64, error) {
	cutoff := time.Now().Add(-graceWindow)
	tag, err := s.pool.Exec(ctx, `
		UPDATE characters
		SET stats_finalized = true, stats_finalized_at = now(), stats_revision = $2
		WHERE is_alive = false AND stats_finalized = false
		  AND COALESCE(last_event_at, died_at) < $1
	`, cutoff, currentStatsRevision)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// reconcileFinalizedCharacterStats implements the eventStore interface --
// see its comment. Recomputes every finalized character's aggregates from
// its own raw events (using the exact same aggregateDeltaForEvent rules
// live ingestion uses) and repairs the stored row if it drifted.
func (s *pgStore) reconcileFinalizedCharacterStats(ctx context.Context) (checked int, repaired int, err error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM characters WHERE stats_finalized = true`)
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

// reconcileCharacterStats recomputes characterID's aggregate columns from
// its raw events and, if the recomputed totals differ from what's
// stored, repairs the row (and its breakdown rows) and logs the
// correction -- character-aggregate-stats.md's AGG-4. stats_finalized is
// intentionally left untouched: reconciliation repairs data, it does not
// reopen a life to further live aggregation.
func (s *pgStore) reconcileCharacterStats(ctx context.Context, characterID int64) (changed bool, err error) {
	rows, err := s.pool.Query(ctx, `SELECT event_type, details FROM events WHERE character_id = $1`, characterID)
	if err != nil {
		return false, err
	}
	var total characterStatDelta
	breakdown := map[[2]string]float64{}
	for rows.Next() {
		var eventType string
		var detailsJSON []byte
		if err := rows.Scan(&eventType, &detailsJSON); err != nil {
			rows.Close()
			return false, err
		}
		var fields map[string]any
		if err := json.Unmarshal(detailsJSON, &fields); err != nil {
			continue // malformed/legacy details -- skip rather than fail the whole reconciliation
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
	if err := s.pool.QueryRow(ctx, `
		SELECT zombie_kills, injuries, distance_walked_km, distance_driven_km,
		       drinks, alcohol_ml, pills_taken, books_read, indoor_hours, outdoor_hours
		FROM characters WHERE id = $1
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

	if _, err := s.pool.Exec(ctx, `
		UPDATE characters
		SET zombie_kills = $2, injuries = $3, distance_walked_km = $4, distance_driven_km = $5,
		    drinks = $6, alcohol_ml = $7, pills_taken = $8, books_read = $9,
		    indoor_hours = $10, outdoor_hours = $11, stats_revision = $12
		WHERE id = $1
	`, characterID, total.ZombieKills, total.Injuries, total.DistanceWalkedKm, total.DistanceDrivenKm,
		total.Drinks, total.AlcoholMl, total.PillsTaken, total.BooksRead, total.IndoorHours, total.OutdoorHours, currentStatsRevision); err != nil {
		return false, err
	}
	now := time.Now()
	for k, v := range breakdown {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO character_stat_breakdown (character_id, category, value_key, value, updated_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (character_id, category, value_key)
			DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
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
func (s *pgStore) handleSessionEvent(ctx context.Context, ev *sessionEvent) {
	s.rememberSteamID(ctx, ev.Username, ev.SteamID)
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
	// steamId (added 2026-08-14 for the quest engine): optional, only
	// set by callers that identify a specific player (e.g.
	// RecordedMedia.pollDeviceMedia) -- most existing callers omit it,
	// which is fine, NULLIF('') keeps the column NULL for those.
	//
	// artifactKey is now ALSO persisted here unconditionally (not just
	// inside the "applied" branch below), added alongside steamId: a
	// signal-only "applied" result with no x/y/z (e.g.
	// recorded_media_viewed -- a discovery, not a spatial placement)
	// previously had nowhere durable for its artifactKey to land, since
	// twr_world_artifacts requires a full x/y/z placement.
	artifactKey := twrStringField(f, "artifactKey")
	if _, err := tx.Exec(ctx, `
		INSERT INTO twr_job_attempts (job_id, attempt_no, idempotency_key, action_type, mechanic, result, error_code, error_detail, placed_count, requested_count, occurred_at, server, steam_id, artifact_key)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, $12, NULLIF($13, ''), NULLIF($14, ''))
		ON CONFLICT (server, job_id, attempt_no) DO NOTHING
	`, jobID, attemptNo, twrStringField(f, "idempotencyKey"), twrStringField(f, "actionType"), twrStringField(f, "mechanic"), result,
		twrStringField(f, "errorCode"), twrStringField(f, "errorDetail"), placedPtr, requestedPtr, ev.Timestamp, s.serverName, twrStringField(f, "steamId"), artifactKey); err != nil {
		return err
	}

	if result == "applied" {
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

// handleTWRJobAccepted -- see eventstore.go's interface comment for why
// this is deliberately separate from handleTWRJobResult/
// twr_job_attempts (CGPT-G1-P3-01). jobId is the TEXT form of a real
// twr_jobs.id (BIGSERIAL) -- a malformed or non-numeric jobId (e.g. an
// old ad-hoc "debug-<timestamp>" identity from a mechanic not yet
// driven by the quest engine) simply matches zero rows below and is a
// silent no-op, not an error; nothing in this codebase requires every
// TWR job to be quest-engine-issued.
func (s *pgStore) handleTWRJobAccepted(ctx context.Context, ev *twrEvent) error {
	f := ev.Fields
	jobID := twrStringField(f, "jobId")
	if jobID == "" {
		slog.Warn("dropping twr_job_result accepted receipt with no jobId")
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE twr_jobs
		SET status = 'WAITING_WORLD', accepted_at = COALESCE(accepted_at, $2)
		WHERE id::text = $1 AND status = 'DISPATCHED'
	`, jobID, ev.Timestamp)
	return err
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
