-- zomboid-exporter Postgres schema. Applied automatically on startup
-- (CREATE ... IF NOT EXISTS, safe to run every boot).

CREATE TABLE IF NOT EXISTS players (
    steam_id      TEXT PRIMARY KEY,
    last_username TEXT NOT NULL,
    first_seen    TIMESTAMPTZ NOT NULL,
    last_seen     TIMESTAMPTZ NOT NULL
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

CREATE TABLE IF NOT EXISTS events (
    id           BIGSERIAL PRIMARY KEY,
    event_type   TEXT NOT NULL, -- login, died, created_player, level_changed
    steam_id     TEXT NOT NULL REFERENCES players(steam_id),
    character_id BIGINT REFERENCES characters(id),
    occurred_at  TIMESTAMPTZ NOT NULL,
    details      JSONB NOT NULL DEFAULT '{}'::jsonb
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
