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

// seedMilestone is one row for seedMilestones -- kept as a plain struct
// (not loaded from Postgres) since these are starter definitions checked
// into git, not runtime data; day-to-day milestone tuning happens by
// editing the discordbot_milestones table directly (or, later, a web UI),
// not by redeploying the bot.
type seedMilestone struct {
	Name      string
	EventType string
	Field     string
	Threshold float64
	Tier      string
	Message   string
}

// seedMilestones inserts starter milestone definitions if they don't
// already exist (idempotent -- ON CONFLICT against the unique
// (event_type, field, threshold) constraint). See ideas/milestones.md
// for the full candidate list this is drawn from.
func seedMilestones(ctx context.Context, db *pgxpool.Pool) error {
	seeds := []seedMilestone{
		{"First Kill", "kill", "zombieKills", 1, "common", "Subject has discovered violence. Promising."},
		{"6h Outdoors Straight", "outdoor_streak", "hours", 6, "common", "Extended environmental exposure recorded."},
		{"24h Outdoors Straight", "outdoor_streak", "hours", 24, "uncommon", "Subject appears to have misplaced the concept of shelter."},
		{"72h Outdoors Straight", "outdoor_streak", "hours", 72, "rare", "Walls remain available. The subject remains uninterested."},
		{"6h Indoors Straight", "indoor_streak", "hours", 6, "common", "Field activity has temporarily ceased."},
		{"24h Indoors Straight", "indoor_streak", "hours", 24, "common", "Subject has discovered the safest strategy: refusing to participate."},
		{"72h Indoors Straight", "indoor_streak", "hours", 72, "uncommon", "The Curator would like to remind the subject that there is, allegedly, an outside."},
		{"7 Days Indoors Straight", "indoor_streak", "hours", 168, "rare", "At this point I am classifying the building as part of the subject."},
	}
	for _, s := range seeds {
		if _, err := db.Exec(ctx, `
			INSERT INTO discordbot_milestones (name, event_type, field, threshold, tier, message)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (event_type, field, threshold) DO NOTHING
		`, s.Name, s.EventType, s.Field, s.Threshold, s.Tier, s.Message); err != nil {
			return err
		}
	}
	return nil
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
