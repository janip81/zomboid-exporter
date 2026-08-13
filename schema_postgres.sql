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

-- ThoseWhoRemain (TWR) mod result tracking -- see
-- zomboid-exporter-ideas/antagonist/spawn-result-tracking.md for the
-- design decision this implements. Separate from `events` above
-- deliberately: these are control-plane/audit records of world
-- mutations the mod attempted, not gameplay telemetry, and unlike
-- `events` they're written by a *different* mod
-- (ThoseWhoRemainLog.txt, not ExporterLog.txt -- see twrlog.go).
--
-- job_id has no FK to a twr_jobs table on purpose: no real DB-driven
-- job dispatcher exists yet (TWR.Debug's menu calls mechanics
-- directly), so job_id is currently just an ad-hoc string the Lua side
-- mints per debug-triggered call (e.g. "debug-<timestamp>"). A real
-- twr_jobs table can be added later without migrating this one --
-- job_id stays a plain TEXT reference either way, per the design doc's
-- own "job bridge is a separate, unproven transport" caveat.
--
-- Append-only: one row per meaningful outcome (SUCCESS / a real error
-- code / deferred_world), never per internal probe, container-scan
-- candidate, or widening-radius retry attempt -- see the design doc's
-- "success-first logging" rule. This is what lets an admin answer "was
-- this ever actually attempted, and what happened" without raw log
-- access.
CREATE TABLE IF NOT EXISTS twr_job_attempts (
    id               BIGSERIAL PRIMARY KEY,
    job_id           TEXT NOT NULL,
    attempt_no       INT NOT NULL DEFAULT 1,
    idempotency_key  TEXT,
    action_type      TEXT NOT NULL,
    mechanic         TEXT NOT NULL,
    result           TEXT NOT NULL, -- applied | deferred_world | retryable_error | final_error
    error_code       TEXT,
    error_detail     TEXT,
    placed_count     INT,
    requested_count  INT,
    occurred_at      TIMESTAMPTZ NOT NULL,
    server           TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_twr_job_attempts_job_id ON twr_job_attempts (job_id, attempt_no);
CREATE INDEX IF NOT EXISTS idx_twr_job_attempts_time ON twr_job_attempts (occurred_at DESC);

-- Confirmed successful world results ONLY -- a failed/errored attempt
-- above must never create a row here (see handleTWRJobResult). This is
-- the durable answer to "was artifact X actually placed, and where" --
-- applied_at means the game confirmed creation succeeded at that
-- moment, it does NOT promise the object is still there now (a player
-- may pick up/move/destroy it afterward -- that's normal gameplay, not
-- tracked here).
CREATE TABLE IF NOT EXISTS twr_world_artifacts (
    id               BIGSERIAL PRIMARY KEY,
    artifact_key     TEXT NOT NULL,
    job_id           TEXT NOT NULL,
    artifact_type    TEXT NOT NULL,
    x                INT NOT NULL,
    y                INT NOT NULL,
    z                INT NOT NULL,
    target_summary   TEXT,
    applied_at       TIMESTAMPTZ NOT NULL,
    server           TEXT NOT NULL,
    UNIQUE (artifact_key, server)
);

CREATE INDEX IF NOT EXISTS idx_twr_world_artifacts_job_id ON twr_world_artifacts (job_id);

-- discord-bot (a separate binary/module in discord-bot/, connecting to
-- this same database) owns and applies its own tables via its own
-- go:embed'd schema_postgres.sql -- not here. If nobody enables the bot,
-- the exporter never creates or needs those tables; if the bot's schema
-- changes, only the bot needs rebuilding/restarting, not the game server
-- pod (this table briefly lived here on 2026-08-10 -- moving it out
-- after hitting exactly that problem).
