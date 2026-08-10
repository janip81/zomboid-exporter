-- zomboid-exporter Postgres schema. Applied automatically on startup
-- (CREATE ... IF NOT EXISTS, safe to run every boot).

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
