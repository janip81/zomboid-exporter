-- zomboid-exporter Postgres schema -- generic Project Zomboid stats/
-- telemetry tables (PerkLog.txt + ExporterLog.txt). Applied on every
-- startup regardless of --twr-enabled (CREATE ... IF NOT EXISTS, safe
-- to run every boot). See schema_twr_postgres.sql for the separate,
-- opt-in ThoseWhoRemain quest-engine schema -- split 2026-08-14 per
-- zomboid-exporter-ideas/antagonist/design/exporter-twr-feature-
-- boundary.md: a plain stats-only deployment must never get TWR tables
-- just because Postgres happens to be configured.

-- server labels every row with the --server-name this exporter instance
-- was started with. Not part of any uniqueness constraint (steam_id
-- alone is still the players PK) -- it's a label for filtering/future
-- multi-server support, not full per-server isolation. On an existing
-- database missing this column, newPgStore's migrateServerColumn adds
-- and backfills it (CREATE TABLE IF NOT EXISTS below is a no-op there).
CREATE TABLE IF NOT EXISTS players (
    steam_id      TEXT PRIMARY KEY,
    last_username TEXT NOT NULL,
    first_seen    TIMESTAMPTZ NOT NULL,
    last_seen     TIMESTAMPTZ NOT NULL,
    server        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS characters (
    id                      BIGSERIAL PRIMARY KEY,
    steam_id                TEXT NOT NULL REFERENCES players(steam_id),
    character_number        INT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL,
    died_at                 TIMESTAMPTZ,
    hours_survived_at_death DOUBLE PRECISION,
    death_x                 INT,
    death_y                 INT,
    death_z                 INT,
    is_alive                BOOLEAN NOT NULL DEFAULT TRUE,
    server                  TEXT NOT NULL,
    UNIQUE (steam_id, character_number)
);

CREATE INDEX IF NOT EXISTS idx_characters_steam_id_alive
    ON characters (steam_id) WHERE is_alive;

-- Per-life aggregate stats, incrementally derived from raw ExporterLog
-- events as they're ingested -- see character-aggregate-stats.md. Raw
-- events remain the authoritative source; these columns exist so Curator/
-- stat queries don't have to rescan the full events table every time.
-- vehicle_collisions has no source event yet (no crash-detection tracker
-- exists in the Lua mod) -- the column is reserved and always 0 until one
-- is added.
ALTER TABLE characters ADD COLUMN IF NOT EXISTS zombie_kills BIGINT NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS injuries BIGINT NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS distance_walked_km DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS distance_driven_km DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS drinks BIGINT NOT NULL DEFAULT 0;
-- Companion to alcohol_ml (curator-llm-semantic-stat-resolution.md): a
-- plain COUNT of alcoholic drink events, reliable regardless of whether
-- that drink's volume was reported (alcohol_ml under-counts on drink
-- paths with no "liters" field). This is the metric "who drinks the
-- most / who's the drunk" should leaderboard on, not alcohol_ml.
ALTER TABLE characters ADD COLUMN IF NOT EXISTS alcoholic_drinks BIGINT NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS alcohol_ml DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS pills_taken BIGINT NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS books_read BIGINT NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS vehicle_collisions BIGINT NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS indoor_hours DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS outdoor_hours DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS last_event_at TIMESTAMPTZ;
-- stats_finalized=false is the live-aggregation lock (character-aggregate-
-- stats.md: "Do not use is_alive=false itself as the aggregation lock:
-- telemetry can arrive just after the death record" -- confirmed live by
-- the phantom-character bug this design directly follows from). Normal
-- incremental updates only ever touch rows where this is still false;
-- only finalizeDeadCharacters/finalizeStaleCharacters set it true, and
-- only reconciliation may still repair a row after that.
ALTER TABLE characters ADD COLUMN IF NOT EXISTS stats_finalized BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS stats_finalized_at TIMESTAMPTZ;
-- Which aggregation ruleset produced the stored values -- lets a future
-- rule change (e.g. a corrected delta/total interpretation) selectively
-- rebuild old lives instead of silently mixing two incompatible rulesets.
ALTER TABLE characters ADD COLUMN IF NOT EXISTS stats_revision INTEGER NOT NULL DEFAULT 1;

-- Dynamic per-item breakdown (favorite drink, most-used weapon, etc.) --
-- deliberately NOT columns on characters, since the item vocabulary is
-- open-ended (character-aggregate-stats.md: "Do not turn dynamic
-- categories into columns").
CREATE TABLE IF NOT EXISTS character_stat_breakdown (
    character_id BIGINT NOT NULL REFERENCES characters(id),
    category     TEXT NOT NULL,
    value_key    TEXT NOT NULL,
    value        DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (character_id, category, value_key)
);

CREATE TABLE IF NOT EXISTS skill_snapshots (
    id           BIGSERIAL PRIMARY KEY,
    character_id BIGINT NOT NULL REFERENCES characters(id),
    captured_at  TIMESTAMPTZ NOT NULL,
    skill_name   TEXT NOT NULL,
    level        INT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_skill_snapshots_character
    ON skill_snapshots (character_id, skill_name, captured_at DESC);

-- Generic event log: both PerkLog.txt (login, died, created_player,
-- level_changed) and ExporterLog.txt (kill, movement_distance,
-- driving_distance, enter_vehicle, exit_vehicle, eat, drink, pill,
-- read, and any future Lua-mod-added stat) land here under their own
-- event_type, with type-specific data in details. A new ExporterLog
-- stat never needs a schema change -- see handleExporterEvent.
-- steam_id is nullable -- most events are player-scoped and always set
-- it, but a system-level event with no player attached (e.g. the Lua
-- mod's periodic world_stats snapshot) legitimately has none. NULL
-- always satisfies a FOREIGN KEY constraint regardless of what rows
-- exist in players, so this doesn't weaken the reference for the
-- normal player-scoped case.
CREATE TABLE IF NOT EXISTS events (
    id           BIGSERIAL PRIMARY KEY,
    event_type   TEXT NOT NULL,
    steam_id     TEXT REFERENCES players(steam_id),
    character_id BIGINT REFERENCES characters(id),
    occurred_at  TIMESTAMPTZ NOT NULL,
    details      JSONB NOT NULL DEFAULT '{}'::jsonb,
    server       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_type_time ON events (event_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_steam_id ON events (steam_id, occurred_at DESC);

-- Tracks how far into each PerkLog.txt file has been read, so restarts
-- (and the very first run, backfilling every historical file) never
-- re-process or skip content -- see pollPerkLogsWithHistory in perklog.go.
-- Also shared by the TWR log pollers (twrlog.go) when --twr-enabled is
-- set -- same path-checkpoint purpose, different log files, no reason
-- for a second table.
CREATE TABLE IF NOT EXISTS processed_files (
    file_path   TEXT PRIMARY KEY,
    byte_offset BIGINT NOT NULL
);

-- discord-bot (a separate binary/module in discord-bot/, connecting to
-- this same database) owns and applies its own tables via its own
-- go:embed'd schema_postgres.sql -- not here. If nobody enables the bot,
-- the exporter never creates or needs those tables; if the bot's schema
-- changes, only the bot needs rebuilding/restarting, not the game server
-- pod (this table briefly lived here on 2026-08-10 -- moving it out
-- after hitting exactly that problem).
