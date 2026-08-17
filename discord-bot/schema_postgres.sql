-- discord-bot's own Postgres schema. Applied automatically on startup
-- (CREATE ... IF NOT EXISTS, safe to run every boot) against the same
-- database the exporter writes events to -- but owned and embedded here,
-- not in the exporter image. If nobody enables the bot, the exporter
-- never creates or needs these tables; if the bot's schema changes, only
-- the bot needs rebuilding/restarting, not the whole game server pod.

-- Bot's own command-authorization state, keyed by Discord user ID (NOT a
-- Steam ID/player -- a Discord user isn't necessarily a PZ player, and
-- vice versa). role is 'admin', 'moderator', or 'blocked'; any user with
-- no row here gets default public-command access. Deliberately DB-backed
-- rather than a git-editable ConfigMap: blocking a spammer needs to take
-- effect on their very next command, not after an edit+push+ArgoCD
-- sync+pod restart cycle.
CREATE TABLE IF NOT EXISTS discordbot_user_roles (
    discord_user_id TEXT PRIMARY KEY,
    role            TEXT NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    updated_by      TEXT NOT NULL
);

-- Milestone definitions ("The Curator" persona -- see ideas/milestones.md).
-- name is a short human-readable label (for a future web UI to list/edit
-- these, not shown to players) separate from message, the actual
-- in-character flavor text that gets posted.
--
-- `kind` picks how the threshold is evaluated (see checkMilestones in
-- milestones.go):
--   'field' -- the triggering event's OWN field must already be >=
--              threshold (e.g. kill's running zombieKills, or
--              indoor_streak's running hours). Cheapest, but only works
--              when the Lua tracker already emits a running total.
--   'count' -- COUNT(*) of every past events row of this event_type
--              (optionally filtered to filter_field=filter_value, e.g.
--              fluid=Beer) must be >= threshold. Needs no Lua changes --
--              reuses whatever's already landing in the shared `events`
--              table one row per action.
--   'sum'   -- same as 'count' but SUMs `field` across those matching
--              rows instead of counting them (e.g. summing driving
--              distance's per-flush `km` chunks into a running total).
-- filter_value may be a comma-separated list (matched via ANY) so one
-- row can cover a category spanning several raw values (e.g. every
-- alcoholic fluid type for a "1000 alcoholic drinks" milestone).
CREATE TABLE IF NOT EXISTS discordbot_milestones (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'field',
    field        TEXT NOT NULL,
    filter_field TEXT NOT NULL DEFAULT '',
    filter_value TEXT NOT NULL DEFAULT '',
    threshold    DOUBLE PRECISION NOT NULL,
    tier         TEXT NOT NULL,
    message      TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT true
);

-- Migration for installs that already had the pre-'kind' schema (columns
-- added here are no-ops once applied; ALTER COLUMN TYPE is also a no-op
-- once threshold is already DOUBLE PRECISION).
ALTER TABLE discordbot_milestones ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'field';
ALTER TABLE discordbot_milestones ADD COLUMN IF NOT EXISTS filter_field TEXT NOT NULL DEFAULT '';
ALTER TABLE discordbot_milestones ADD COLUMN IF NOT EXISTS filter_value TEXT NOT NULL DEFAULT '';
ALTER TABLE discordbot_milestones ALTER COLUMN threshold TYPE DOUBLE PRECISION;

DO $$
BEGIN
    ALTER TABLE discordbot_milestones DROP CONSTRAINT IF EXISTS discordbot_milestones_event_type_field_threshold_key;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'discordbot_milestones_uniq'
    ) THEN
        ALTER TABLE discordbot_milestones ADD CONSTRAINT discordbot_milestones_uniq
            UNIQUE (event_type, kind, field, filter_field, filter_value, threshold);
    END IF;
END $$;

-- Supports the 'count'/'sum' evaluation kinds above: every check filters
-- to one player's own rows of one event_type first, so this index keeps
-- it an index lookup instead of a growing sequential scan as the shared
-- `events` table accumulates history across every player.
CREATE INDEX IF NOT EXISTS idx_events_steamid_type ON events (steam_id, event_type);

-- Which (milestone, player) pairs have already fired, so each milestone
-- announces at most once per player. steam_id, not discord_user_id --
-- milestones are about in-game achievements, tied to the PZ player, not
-- whichever Discord account happens to be watching.
CREATE TABLE IF NOT EXISTS discordbot_milestone_hits (
    milestone_id BIGINT NOT NULL REFERENCES discordbot_milestones(id),
    steam_id     TEXT NOT NULL,
    hit_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (milestone_id, steam_id)
);

-- Curator LLM feature ----------------------------------------------------
-- See zomboid-exporter-ideas/curator-llm-integration.md,
-- curator-llm-provider.md, curator-llm-provider-db-config.md,
-- curator-natural-trigger-and-identity.md, curator-reply-routing.md for
-- the full design this schema implements.

-- Durable Discord user -> Steam player identity link. Populated two ways
-- (curator-player-auto-linking.md's AUTO-LINK-1): automatically, the first
-- time curator_context.go's resolveCuratorIdentity() gets a unique exact
-- Discord nickname/display-name/account-name match against a known PZ
-- username (link_source = auto_nickname/auto_display_name/
-- auto_account_username); or manually, via an admin override
-- (link_source = admin) for cases that don't auto-resolve (renamed
-- accounts, Discord/PZ names that genuinely differ, name collisions). An
-- existing row here -- auto or admin -- always wins over a fresh
-- name-derived match; it is never silently replaced (AUTO-LINK-5/6).
CREATE TABLE IF NOT EXISTS discordbot_player_links (
    discord_user_id TEXT PRIMARY KEY,
    steam_id        TEXT NOT NULL,
    linked_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    linked_by       TEXT NOT NULL
);

ALTER TABLE discordbot_player_links
    ADD COLUMN IF NOT EXISTS link_source TEXT NOT NULL DEFAULT 'manual',
    ADD COLUMN IF NOT EXISTS matched_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_verified_at TIMESTAMPTZ;

-- One SteamID should have at most one Discord link (AUTO-LINK-2) -- both
-- tables were confirmed empty before this index was added, so no preflight
-- dedupe was needed; if this ever fails on a fresh environment, dedupe
-- discordbot_player_links by steam_id before retrying.
CREATE UNIQUE INDEX IF NOT EXISTS discordbot_player_links_steam_id_uq
    ON discordbot_player_links (steam_id);

-- Curator LLM provider configuration. DB-backed (not YAML/flags) so
-- priority/enabled/model can be changed with one UPDATE, no redeploy --
-- same reasoning as discordbot_milestones/discordbot_user_roles above.
--
-- SECURITY (CGPT-050, see curator-llm-provider-db-config.md): this table
-- deliberately does NOT store a base_url or an env-var name to read. Both
-- `adapter` (how to speak to the provider) and `credential_slot` (which
-- credential to use) are resolved against a FIXED allowlist defined in
-- Go code (curator_llm.go), never passed to os.Getenv() directly -- a raw
-- DB-provided env-var name would let a Postgres writer redirect an
-- unrelated pod secret (e.g. DISCORD_TOKEN) to an arbitrary endpoint.
-- An unknown adapter/credential_slot value must fail closed as
-- "misconfigured", not silently resolve to something.
--
-- No rows are seeded by the bot itself (unlike discordbot_milestones,
-- which is the bot's own content) -- each row implies a real account/API
-- key already exists somewhere, so providers are added via a one-time
-- psql insert per deployment, same reasoning as bootstrapAdmins taking
-- explicit IDs from a flag rather than seeding fake admins.
CREATE TABLE IF NOT EXISTS discordbot_llm_providers (
    name            TEXT PRIMARY KEY,
    adapter         TEXT NOT NULL,
    credential_slot TEXT NOT NULL,
    priority        INTEGER NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    model           TEXT NOT NULL,
    allow_paid      BOOLEAN NOT NULL DEFAULT false,
    extra_config    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
