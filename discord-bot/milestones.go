package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

type milestone struct {
	ID      int64
	Name    string
	Field   string
	Tier    string
	Message string
}

// seedMilestones inserts starter milestone definitions if they don't
// already exist (idempotent -- ON CONFLICT against the unique
// (event_type, field, threshold) constraint). Starting with just one to
// validate the schema/evaluation design before building out the rest --
// see ideas/milestones.md for the full candidate list.
func seedMilestones(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `
		INSERT INTO discordbot_milestones (name, event_type, field, threshold, tier, message)
		VALUES ('First Kill', 'kill', 'zombieKills', 1, 'common', 'Subject has discovered violence. Promising.')
		ON CONFLICT (event_type, field, threshold) DO NOTHING
	`)
	return err
}

// checkMilestones returns every enabled milestone for eventType whose
// threshold field has been reached in this event's payload, that steamID
// hasn't already hit -- recording each one as hit (discordbot_milestone_hits)
// before returning it, so a milestone fires at most once per player even
// if this event type is seen again with the same or a higher value.
func checkMilestones(ctx context.Context, db *pgxpool.Pool, eventType, steamID string, fields map[string]any) []milestone {
	if db == nil || steamID == "" {
		return nil
	}

	rows, err := db.Query(ctx, `
		SELECT m.id, m.name, m.field, m.threshold, m.tier, m.message
		FROM discordbot_milestones m
		WHERE m.enabled AND m.event_type = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM discordbot_milestone_hits h
		      WHERE h.milestone_id = m.id AND h.steam_id = $2
		  )
	`, eventType, steamID)
	if err != nil {
		slog.Error("failed to query milestones", "eventType", eventType, "err", err)
		return nil
	}

	type candidate struct {
		m         milestone
		threshold float64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.m.ID, &c.m.Name, &c.m.Field, &c.threshold, &c.m.Tier, &c.m.Message); err != nil {
			slog.Error("failed to scan milestone row", "err", err)
			continue
		}
		candidates = append(candidates, c)
	}
	rows.Close() // release the cursor before the INSERTs below reuse the pool

	var hits []milestone
	for _, c := range candidates {
		// JSON numbers decode into map[string]any as float64.
		val, ok := fields[c.m.Field].(float64)
		if !ok || val < c.threshold {
			continue
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO discordbot_milestone_hits (milestone_id, steam_id, hit_at)
			VALUES ($1, $2, now())
			ON CONFLICT DO NOTHING
		`, c.m.ID, steamID); err != nil {
			slog.Error("failed to record milestone hit", "milestoneID", c.m.ID, "steamID", steamID, "err", err)
			continue
		}
		hits = append(hits, c.m)
	}
	return hits
}
