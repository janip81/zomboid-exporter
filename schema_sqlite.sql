-- SQLite variant of the same schema as schema_postgres.sql. Kept as a
-- separate file (rather than one dialect-conditional schema) because the
-- column types genuinely differ enough (no TIMESTAMPTZ/BOOLEAN/JSONB in
-- SQLite) that a shared file would need more dialect-branching than it's
-- worth. Applied automatically on startup (CREATE ... IF NOT EXISTS, safe
-- to run every boot). Timestamps are stored as ISO-8601 UTC TEXT, which
-- sorts correctly lexicographically.

-- server labels every row with the --server-name this exporter instance
-- was started with -- see schema_postgres.sql's players comment for
-- the full rationale (label-only, not a uniqueness key).
CREATE TABLE IF NOT EXISTS players (
    steam_id      TEXT PRIMARY KEY,
    last_username TEXT NOT NULL,
    first_seen    TEXT NOT NULL,
    last_seen     TEXT NOT NULL,
    server        TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS characters (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    steam_id                TEXT NOT NULL REFERENCES players(steam_id),
    character_number        INTEGER NOT NULL,
    created_at              TEXT NOT NULL,
    died_at                 TEXT,
    hours_survived_at_death REAL,
    death_x                 INTEGER,
    death_y                 INTEGER,
    death_z                 INTEGER,
    is_alive                INTEGER NOT NULL DEFAULT 1,
    server                  TEXT NOT NULL DEFAULT '',
    UNIQUE (steam_id, character_number)
);

CREATE INDEX IF NOT EXISTS idx_characters_steam_id_alive
    ON characters (steam_id) WHERE is_alive = 1;

CREATE TABLE IF NOT EXISTS skill_snapshots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    character_id INTEGER NOT NULL REFERENCES characters(id),
    captured_at  TEXT NOT NULL,
    skill_name   TEXT NOT NULL,
    level        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_skill_snapshots_character
    ON skill_snapshots (character_id, skill_name, captured_at DESC);

-- Generic event log: both PerkLog.txt (login, died, created_player,
-- level_changed) and ExporterLog.txt (kill, movement_distance,
-- driving_distance, enter_vehicle, exit_vehicle, eat, drink, pill,
-- read, and any future Lua-mod-added stat) land here under their own
-- event_type, with type-specific data in details. A new ExporterLog
-- stat never needs a schema change -- see handleExporterEvent.
-- steam_id is nullable -- see schema_postgres.sql's comment on the same
-- column for why (a system-level event like world_stats has no player
-- attached; NULL always satisfies a FOREIGN KEY regardless).
CREATE TABLE IF NOT EXISTS events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type   TEXT NOT NULL,
    steam_id     TEXT REFERENCES players(steam_id),
    character_id INTEGER REFERENCES characters(id),
    occurred_at  TEXT NOT NULL,
    details      TEXT NOT NULL DEFAULT '{}', -- JSON, as a plain string (no native JSON type in SQLite)
    server       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_events_type_time ON events (event_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_steam_id ON events (steam_id, occurred_at DESC);

-- Tracks how far into each PerkLog.txt file has been read, so restarts
-- (and the very first run, backfilling every historical file) never
-- re-process or skip content -- see pollPerkLogsWithHistory in perklog.go.
CREATE TABLE IF NOT EXISTS processed_files (
    file_path   TEXT PRIMARY KEY,
    byte_offset INTEGER NOT NULL
);

-- ThoseWhoRemain (TWR) mod result tracking -- SQLite mirror of
-- schema_postgres.sql's twr_job_attempts/twr_world_artifacts, see that
-- file's comments for the full rationale.
CREATE TABLE IF NOT EXISTS twr_job_attempts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id           TEXT NOT NULL,
    attempt_no       INTEGER NOT NULL DEFAULT 1,
    idempotency_key  TEXT,
    action_type      TEXT NOT NULL,
    mechanic         TEXT NOT NULL,
    result           TEXT NOT NULL,
    error_code       TEXT,
    error_detail     TEXT,
    placed_count     INTEGER,
    requested_count  INTEGER,
    occurred_at      TEXT NOT NULL,
    server           TEXT NOT NULL DEFAULT '',
    steam_id         TEXT,
    artifact_key     TEXT
);

CREATE INDEX IF NOT EXISTS idx_twr_job_attempts_job_id ON twr_job_attempts (job_id, attempt_no);
CREATE INDEX IF NOT EXISTS idx_twr_job_attempts_time ON twr_job_attempts (occurred_at DESC);

-- Idempotency -- see schema_postgres.sql's comment on the same index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_twr_job_attempts_unique ON twr_job_attempts (server, job_id, attempt_no);

CREATE TABLE IF NOT EXISTS twr_world_artifacts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    artifact_key     TEXT NOT NULL,
    job_id           TEXT NOT NULL,
    artifact_type    TEXT NOT NULL,
    x                INTEGER NOT NULL,
    y                INTEGER NOT NULL,
    z                INTEGER NOT NULL,
    target_summary   TEXT,
    applied_at       TEXT NOT NULL,
    server           TEXT NOT NULL DEFAULT '',
    UNIQUE (artifact_key, server)
);

CREATE INDEX IF NOT EXISTS idx_twr_world_artifacts_job_id ON twr_world_artifacts (job_id);

-- discord-bot owns and applies its own tables via its own schema file --
-- not here. See discord-bot/schema_postgres.sql's comment for why (also
-- moot in practice since discord-bot currently only supports a Postgres
-- DSN, no --sqlite-path).
