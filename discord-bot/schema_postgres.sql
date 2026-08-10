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
