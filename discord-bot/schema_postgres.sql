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
-- event_type/field/threshold decide when it fires (field's value in the
-- MQTT event payload >= threshold); name is a short human-readable label
-- (for a future web UI to list/edit these, not shown to players) separate
-- from message, the actual in-character flavor text that gets posted.
CREATE TABLE IF NOT EXISTS discordbot_milestones (
    id         SERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    event_type TEXT NOT NULL,
    field      TEXT NOT NULL,
    threshold  BIGINT NOT NULL,
    tier       TEXT NOT NULL,
    message    TEXT NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (event_type, field, threshold)
);

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
