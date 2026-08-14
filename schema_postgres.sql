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
    -- NOTE: "accepted" (quest-engine transport-acceptance receipts)
    -- deliberately does NOT go through this table -- see twr_jobs.
    -- accepted_at's comment (CGPT-G1-P3-01).
    error_code       TEXT,
    error_detail     TEXT,
    placed_count     INT,
    requested_count  INT,
    occurred_at      TIMESTAMPTZ NOT NULL,
    server           TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_twr_job_attempts_job_id ON twr_job_attempts (job_id, attempt_no);
CREATE INDEX IF NOT EXISTS idx_twr_job_attempts_time ON twr_job_attempts (occurred_at DESC);

-- Added 2026-08-14 for the quest engine (Gate 1): a signal like
-- "player X watched recording Y" needs a structured player identity to
-- become a twr_signal, but no field carried one until now -- callers
-- like RecordedMedia.pollDeviceMedia previously only had the player's
-- username buried in free-text targetSummary. Nullable/additive -- ALTER
-- ... ADD COLUMN IF NOT EXISTS is safe to run against rows that predate
-- this column (see migrateServerColumn/migrateFileOffsetKeys for the
-- established pattern of additive migrations in this codebase).
ALTER TABLE twr_job_attempts ADD COLUMN IF NOT EXISTS steam_id TEXT;

-- Added 2026-08-14 alongside steam_id, same reasoning: twr_world_artifacts
-- only ever gets a row when a result carries a full x/y/z placement, but
-- a signal-only "applied" result (e.g. recorded_media_viewed -- a
-- discovery, not a spatial placement) still has a meaningful
-- artifactKey (e.g. a contentId) with nowhere durable to land before
-- this column existed.
ALTER TABLE twr_job_attempts ADD COLUMN IF NOT EXISTS artifact_key TEXT;

-- Idempotency (spawn-result-tracking.md review Q5): a crash window
-- exists where a commit succeeds but the exporter dies before
-- persisting the new processed_files offset, causing the same log
-- line to be replayed after restart. A UNIQUE INDEX (not a table
-- CONSTRAINT -- Postgres has no ADD CONSTRAINT IF NOT EXISTS, but
-- CREATE UNIQUE INDEX IF NOT EXISTS works and ON CONFLICT can target
-- it identically) lets handleTWRJobResult's INSERT ... ON CONFLICT DO
-- NOTHING absorb that replay instead of creating a duplicate row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_twr_job_attempts_unique ON twr_job_attempts (server, job_id, attempt_no);

-- Confirmed successful world results ONLY -- a failed/errored attempt
-- above must never create a row here (see handleTWRJobResult). This is
-- the durable answer to "was artifact X actually placed, and where" --
-- applied_at means the game confirmed creation succeeded at that
-- moment, it does NOT promise the object is still there now (a player
-- may pick up/move/destroy it afterward -- that's normal gameplay, not
-- tracked here).
-- artifact_type describes the placed THING itself (e.g. an item
-- module string like "Base.Twigs") -- NOT what it was placed into.
-- target_summary describes the container/object it landed in. These
-- were conflated in the first pass (review Q3): artifact_type held
-- "container", which is target information, not artifact information.
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

-- ═══════════════════════════════════════════════════════════════════
-- Quest engine Gate 1 (2026-08-14) -- see zomboid-exporter-ideas/
-- antagonist/quest-db/ for the full design docs this schema is trimmed
-- from. Deliberately scoped to the smallest real shape that can drive
-- the existing dummy key->VHS->location->sleep fixture
-- (antagonist/quest-db/quest-fixtures/dummy-key-vhs-location-sleep.md,
-- KVLS-1..9) end to end with zero debug-menu orchestration. Explicitly
-- NOT built here (see the full design docs if/when actually needed):
-- twr_quest_participants, the location catalog
-- (twr_locations/twr_location_keys/twr_map_locations -- the fixture
-- doc's own "Level 1" scoping is to hardcode one fixed test location in
-- action/condition params instead), the entire seed-content system, the
-- world_id/map_profile_id/server_instance_id multi-world isolation
-- layer (one implicit world per server for now), and twr_runtime_values
-- (test_key_id lives directly in a job's action_params snapshot).
-- ═══════════════════════════════════════════════════════════════════

-- PART A -- authored/versioned quest definitions. Treat a definition
-- version as immutable once it has active instances (quest-database-
-- design.md) -- authoring changes go to a new version row; existing
-- instances stay pinned to whatever version they started on.
CREATE TABLE IF NOT EXISTS twr_quest_definitions (
    id           BIGSERIAL PRIMARY KEY,
    quest_key    TEXT NOT NULL,
    version      INT NOT NULL DEFAULT 1,
    status       TEXT NOT NULL DEFAULT 'draft', -- draft | active | retired
    scope_kind   TEXT NOT NULL, -- world | player | group
    start_policy TEXT NOT NULL, -- manual | campaign_start | signal | dependency
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (quest_key, version)
);

CREATE TABLE IF NOT EXISTS twr_step_definitions (
    id                   BIGSERIAL PRIMARY KEY,
    quest_definition_id  BIGINT NOT NULL REFERENCES twr_quest_definitions(id),
    step_key             TEXT NOT NULL,
    trigger_type         TEXT NOT NULL,
    trigger_params       JSONB NOT NULL DEFAULT '{}'::jsonb,
    completion_policy    TEXT NOT NULL DEFAULT 'all_actions', -- all_actions | any_action | trigger_only
    repeat_policy        TEXT NOT NULL DEFAULT 'once', -- once | bounded | cooldown | recurring
    max_completions      INT,
    cooldown_seconds     INT,
    metadata             JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (quest_definition_id, step_key)
);

CREATE INDEX IF NOT EXISTS idx_twr_step_definitions_quest ON twr_step_definitions (quest_definition_id);

-- Evaluated AND-only (every row for a step must pass) -- v1 deliberately
-- has no OR/nested boolean logic, deferred until a real quest proves it
-- necessary (quest-database-design.md's explicit scoping).
CREATE TABLE IF NOT EXISTS twr_step_conditions (
    id                  BIGSERIAL PRIMARY KEY,
    step_definition_id  BIGINT NOT NULL REFERENCES twr_step_definitions(id),
    condition_key       TEXT NOT NULL,
    position            INT NOT NULL DEFAULT 0,
    condition_type      TEXT NOT NULL,
    params              JSONB NOT NULL DEFAULT '{}'::jsonb,
    negate              BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_twr_step_conditions_step ON twr_step_conditions (step_definition_id, position);

-- Each action definition becomes one durable twr_jobs row (with a
-- snapshot of params) the moment its step triggers, so a later edit to
-- this definition never silently changes an already-created job's
-- behavior (quest-database-design.md).
CREATE TABLE IF NOT EXISTS twr_step_actions (
    id                  BIGSERIAL PRIMARY KEY,
    step_definition_id  BIGINT NOT NULL REFERENCES twr_step_definitions(id),
    action_key          TEXT NOT NULL,
    position            INT NOT NULL DEFAULT 0,
    action_type         TEXT NOT NULL,
    params              JSONB NOT NULL DEFAULT '{}'::jsonb,
    required            BOOLEAN NOT NULL DEFAULT TRUE,
    failure_policy      TEXT NOT NULL DEFAULT 'retry' -- retry | fail_step | continue
);

CREATE INDEX IF NOT EXISTS idx_twr_step_actions_step ON twr_step_actions (step_definition_id, position);

-- v1 only ever needs outcome_key='success' -- reserving the graph shape
-- now rather than building branching logic before a real quest needs it.
CREATE TABLE IF NOT EXISTS twr_quest_edges (
    id                   BIGSERIAL PRIMARY KEY,
    quest_definition_id  BIGINT NOT NULL REFERENCES twr_quest_definitions(id),
    from_step_key        TEXT NOT NULL,
    outcome_key          TEXT NOT NULL DEFAULT 'success',
    to_step_key          TEXT NOT NULL,
    position             INT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_twr_quest_edges_quest ON twr_quest_edges (quest_definition_id, from_step_key);

-- PART B -- runtime quest state. Deliberately does not duplicate any
-- authored trigger/condition/action data -- always points back to an
-- immutable definition version.
CREATE TABLE IF NOT EXISTS twr_campaigns (
    id           BIGSERIAL PRIMARY KEY,
    campaign_key TEXT NOT NULL,
    state        TEXT NOT NULL DEFAULT 'inactive', -- inactive | active | paused | completed
    seed         BIGINT,
    phase        TEXT,
    started_at   TIMESTAMPTZ,
    paused_at    TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_key)
);

CREATE TABLE IF NOT EXISTS twr_quest_instances (
    id                    BIGSERIAL PRIMARY KEY,
    campaign_id           BIGINT NOT NULL REFERENCES twr_campaigns(id),
    quest_definition_id   BIGINT NOT NULL REFERENCES twr_quest_definitions(id),
    state                 TEXT NOT NULL DEFAULT 'pending', -- pending | active | completed | failed | cancelled
    scope_kind            TEXT NOT NULL,
    scope_ref             TEXT,
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_twr_quest_instances_campaign ON twr_quest_instances (campaign_id);
CREATE INDEX IF NOT EXISTS idx_twr_quest_instances_state ON twr_quest_instances (state);

-- Lifecycle: locked -> armed -> triggered -> resolving (only when the
-- step has actions/jobs) -> completed. A trigger must never create a
-- second batch of jobs while already triggered/resolving.
CREATE TABLE IF NOT EXISTS twr_step_instances (
    id                     BIGSERIAL PRIMARY KEY,
    quest_instance_id      BIGINT NOT NULL REFERENCES twr_quest_instances(id),
    step_definition_id     BIGINT NOT NULL REFERENCES twr_step_definitions(id),
    state                  TEXT NOT NULL DEFAULT 'locked', -- locked | armed | triggered | resolving | completed | failed | cancelled
    completion_count       INT NOT NULL DEFAULT 0,
    armed_at               TIMESTAMPTZ,
    triggered_at           TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ,
    triggered_by_steam_id  TEXT,
    trigger_signal_id      BIGINT,
    last_error             TEXT
);

CREATE INDEX IF NOT EXISTS idx_twr_step_instances_quest ON twr_step_instances (quest_instance_id);
CREATE INDEX IF NOT EXISTS idx_twr_step_instances_armed ON twr_step_instances (state) WHERE state = 'armed';

-- PART C -- normalized trigger-input signals. Upstream of job creation
-- -- not to be confused with twr_job_attempts (downstream of job
-- EXECUTION). Example translation from quest-database-design.md:
-- "ExporterLog raw sleep event -> backend normalizes -> twr_signal ->
-- evaluator checks ARMED steps with matching trigger_type". Not every
-- trigger has a raw gameplay event behind it (scheduled/admin signals
-- have none), which is why this is its own table rather than the
-- evaluator reading `events` directly.
CREATE TABLE IF NOT EXISTS twr_signals (
    id          BIGSERIAL PRIMARY KEY,
    signal_type TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    steam_id    TEXT,
    source_type TEXT NOT NULL, -- exporter_event | twr_event | scheduler | admin
    source_ref  TEXT,
    dedupe_key  TEXT,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_twr_signals_type_time ON twr_signals (signal_type, occurred_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_twr_signals_dedupe ON twr_signals (dedupe_key) WHERE dedupe_key IS NOT NULL;

-- Per-source cursor bookkeeping for the quest engine's Go-side signal
-- producers (questengine.go) and evaluator -- same "how far have I
-- read" purpose as processed_files, just keyed by a small fixed set of
-- named sources (e.g. "sleep_events", "media_playback_attempts",
-- "evaluator") instead of a file path, since these poll DB tables
-- (events, twr_job_attempts, twr_signals) rather than log files.
CREATE TABLE IF NOT EXISTS twr_signal_cursors (
    source  TEXT PRIMARY KEY,
    last_id BIGINT NOT NULL DEFAULT 0
);

-- PART D -- durable action jobs, the piece that makes any of the above
-- real. Status vocabulary (quest-database-design.md sec.14): QUEUED ->
-- DISPATCHED -> WAITING_WORLD -> APPLYING -> APPLIED, with RETRYABLE /
-- FAILED_FINAL as failure branches and CANCELLED as an explicit
-- terminal. WAITING_WORLD is a normal state (target chunk not loaded
-- yet), never treated as a failure.
--
-- id is BIGSERIAL and doubles as the stable job_id string handed to Lua
-- and echoed back in TWR.Emit.jobResult's jobId field -- this is why
-- twr_job_attempts/twr_world_artifacts (both already live, untouched by
-- this migration) don't need an FK added: job_id there stays the plain
-- TEXT reference it always was, just populated with real quest-engine
-- ids now instead of ad-hoc "debug-<timestamp>" strings.
CREATE TABLE IF NOT EXISTS twr_jobs (
    id                    BIGSERIAL PRIMARY KEY,
    campaign_id           BIGINT REFERENCES twr_campaigns(id),
    quest_instance_id     BIGINT REFERENCES twr_quest_instances(id),
    step_instance_id      BIGINT REFERENCES twr_step_instances(id),
    action_definition_id  BIGINT REFERENCES twr_step_actions(id),
    action_type           TEXT NOT NULL,
    action_params         JSONB NOT NULL DEFAULT '{}'::jsonb, -- snapshot at creation time
    status                TEXT NOT NULL DEFAULT 'QUEUED',
    idempotency_key       TEXT,
    attempt_count         INT NOT NULL DEFAULT 0,
    max_attempts          INT,
    last_error_code       TEXT,
    last_error_detail     JSONB,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at         TIMESTAMPTZ,
    accepted_at           TIMESTAMPTZ,
    applied_at            TIMESTAMPTZ,
    cancelled_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_twr_jobs_pending ON twr_jobs (status) WHERE status NOT IN ('APPLIED', 'FAILED_FINAL', 'CANCELLED');
CREATE INDEX IF NOT EXISTS idx_twr_jobs_step_instance ON twr_jobs (step_instance_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_twr_jobs_idempotency ON twr_jobs (idempotency_key) WHERE idempotency_key IS NOT NULL;

-- accepted_at added 2026-08-14 (ChatGPT review CGPT-G1-P3-01, after
-- twr_jobs already existed -- ALTER is needed alongside the CREATE
-- TABLE above for databases where it's already been created). A
-- transport-acceptance receipt ("Lua durably recorded this job") is
-- NOT a final application outcome, and must never be written into
-- twr_job_attempts: that table's existing UNIQUE index is
-- (server, job_id, attempt_no), and TWR.Emit.jobResult's own contract
-- is "one row per final outcome" -- an accepted receipt sharing the
-- same (job_id, attempt_no=1) as the eventual applied/final_error
-- outcome would collide on ON CONFLICT DO NOTHING and silently discard
-- the real result, permanently stranding the job. See
-- handleTWRJobAccepted (store_postgres.go) -- an accepted receipt
-- updates twr_jobs directly instead.
ALTER TABLE twr_jobs ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ;

-- PART E -- discovery, deliberately separate from job application.
-- twr_world_artifacts.applied_at means "the game confirmed creation
-- succeeded", NOT "a player has found/seen it" -- do not overload one
-- for the other.
CREATE TABLE IF NOT EXISTS twr_discoveries (
    id                 BIGSERIAL PRIMARY KEY,
    campaign_id        BIGINT REFERENCES twr_campaigns(id),
    quest_instance_id  BIGINT REFERENCES twr_quest_instances(id),
    artifact_id        BIGINT REFERENCES twr_world_artifacts(id),
    discovery_type     TEXT NOT NULL,
    steam_id           TEXT,
    occurred_at        TIMESTAMPTZ NOT NULL,
    signal_id          BIGINT REFERENCES twr_signals(id),
    metadata           JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_twr_discoveries_quest_instance ON twr_discoveries (quest_instance_id);
