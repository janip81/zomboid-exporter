package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// userRole gates access to tiered slash commands. Stored in Postgres
// (discordbot_user_roles, see schema_postgres.sql) rather than a
// git-editable ConfigMap: blocking a spammer needs to take effect on
// their very next command, not after an edit+push+ArgoCD sync+restart
// cycle.
type userRole string

const (
	roleAdmin     userRole = "admin"
	roleModerator userRole = "moderator"
	roleBlocked   userRole = "blocked"
)

// getUserRole returns "" (no special role -- default public-command
// access) for a user with no row, not an error.
func getUserRole(ctx context.Context, db *pgxpool.Pool, discordUserID string) (userRole, error) {
	var role string
	err := db.QueryRow(ctx, "SELECT role FROM discordbot_user_roles WHERE discord_user_id = $1", discordUserID).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return userRole(role), nil
}

func setUserRole(ctx context.Context, db *pgxpool.Pool, targetID string, role userRole, actorID string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO discordbot_user_roles (discord_user_id, role, updated_at, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (discord_user_id) DO UPDATE SET role = $2, updated_at = $3, updated_by = $4
	`, targetID, string(role), time.Now(), actorID)
	return err
}

func clearUserRole(ctx context.Context, db *pgxpool.Pool, targetID string) error {
	_, err := db.Exec(ctx, "DELETE FROM discordbot_user_roles WHERE discord_user_id = $1", targetID)
	return err
}

// bootstrapAdmins seeds the given IDs as admin, but only if the table is
// completely empty -- solves the chicken-and-egg problem of "nobody can
// grant admin without already being admin" exactly once, on true first
// boot (or after a full data wipe). Runs unconditionally every startup
// otherwise, it would silently re-promote anyone later demoted via
// /block or a manual role change, undermining that decision.
func bootstrapAdmins(ctx context.Context, db *pgxpool.Pool, ids []string) error {
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM discordbot_user_roles").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := setUserRole(ctx, db, id, roleAdmin, "bootstrap"); err != nil {
			return err
		}
	}
	return nil
}
