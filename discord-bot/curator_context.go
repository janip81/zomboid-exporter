package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resolvedIdentity is the outcome of resolveCuratorIdentity -- Resolved
// is false when nothing matched, which is a normal, expected result
// (curator-natural-trigger-and-identity.md: "no player identity is
// guessed"), not an error.
type resolvedIdentity struct {
	SteamID  string
	Username string
	Source   string // "link", "nickname", "display_name", "account_username"
	Resolved bool
}

// resolveCuratorIdentity implements curator-natural-trigger-and-identity.md's
// priority order:
//  1. an explicit discordbot_player_links row always wins;
//  2. otherwise, try each candidateName in the given order (caller
//     passes them nickname-first, then display name, then account
//     username) against players.last_username, exact case-insensitive
//     match. Never fuzzy-matches (`Jan` must not match `Jani`) --
//     if more than one player shares a candidate name, that candidate
//     is treated as unresolved (fail closed) and the NEXT candidate
//     name is still tried, since a different candidate might still be
//     unambiguous.
func resolveCuratorIdentity(ctx context.Context, db *pgxpool.Pool, discordUserID string, candidateNames []string) (resolvedIdentity, error) {
	var linkedSteamID string
	err := db.QueryRow(ctx, "SELECT steam_id FROM discordbot_player_links WHERE discord_user_id = $1", discordUserID).Scan(&linkedSteamID)
	switch {
	case err == nil:
		username, uErr := lookupUsername(ctx, db, linkedSteamID)
		if uErr != nil {
			return resolvedIdentity{}, uErr
		}
		return resolvedIdentity{SteamID: linkedSteamID, Username: username, Source: "link", Resolved: true}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return resolvedIdentity{}, err
	}

	sources := []string{"nickname", "display_name", "account_username"}
	for idx, name := range candidateNames {
		if name == "" {
			continue
		}
		steamID, username, matchCount, qErr := lookupUniquePlayerByName(ctx, db, name)
		if qErr != nil {
			return resolvedIdentity{}, qErr
		}
		if matchCount == 1 {
			source := "account_username"
			if idx < len(sources) {
				source = sources[idx]
			}
			return resolvedIdentity{SteamID: steamID, Username: username, Source: source, Resolved: true}, nil
		}
		// matchCount == 0 (no player by this name) or > 1 (ambiguous) --
		// both fail closed for THIS candidate; still try the next one.
	}
	return resolvedIdentity{}, nil
}

func lookupUsername(ctx context.Context, db *pgxpool.Pool, steamID string) (string, error) {
	var username string
	err := db.QueryRow(ctx, "SELECT last_username FROM players WHERE steam_id = $1", steamID).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return username, err
}

func lookupUniquePlayerByName(ctx context.Context, db *pgxpool.Pool, name string) (steamID, username string, matchCount int, err error) {
	rows, err := db.Query(ctx, "SELECT steam_id, last_username FROM players WHERE lower(last_username) = lower($1)", name)
	if err != nil {
		return "", "", 0, err
	}
	defer rows.Close()
	for rows.Next() {
		matchCount++
		// Scan every row (a second match already means "ambiguous", but
		// the driver still requires each row to be consumed) -- only the
		// first scanned pair is kept/returned.
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return "", "", 0, err
		}
		if matchCount == 1 {
			steamID, username = id, name
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", 0, err
	}
	if matchCount != 1 {
		return "", "", matchCount, nil
	}
	return steamID, username, matchCount, nil
}

// curatorPlayerStats is intentionally small -- curator-llm-provider-db-config-chatgpt-review.md's
// "Minimize data sent to free third-party providers" note: only
// semantic, already-safe facts a Curator reply might plausibly need,
// never raw IDs/SteamID64/DB keys.
type curatorPlayerStats struct {
	Username         string
	FirstSeen        time.Time
	LastSeen         time.Time
	ZombieKills      int
	Deaths           int
	IsCurrentlyAlive bool
}

func fetchCuratorPlayerStats(ctx context.Context, db *pgxpool.Pool, steamID string) (curatorPlayerStats, error) {
	var stats curatorPlayerStats
	err := db.QueryRow(ctx, "SELECT last_username, first_seen, last_seen FROM players WHERE steam_id = $1", steamID).
		Scan(&stats.Username, &stats.FirstSeen, &stats.LastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return curatorPlayerStats{}, nil
	}
	if err != nil {
		return curatorPlayerStats{}, err
	}

	// Running zombieKills total is the event's OWN field (kindField
	// pattern, see milestones.go) -- take the max ever reported, not a
	// sum, since each kill event already carries a running total.
	err = db.QueryRow(ctx, `
		SELECT COALESCE(MAX((details->>'zombieKills')::int), 0)
		FROM events WHERE steam_id = $1 AND event_type = 'kill'
	`, steamID).Scan(&stats.ZombieKills)
	if err != nil {
		return curatorPlayerStats{}, err
	}

	err = db.QueryRow(ctx, "SELECT COUNT(*) FROM characters WHERE steam_id = $1 AND died_at IS NOT NULL", steamID).Scan(&stats.Deaths)
	if err != nil {
		return curatorPlayerStats{}, err
	}

	err = db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM characters WHERE steam_id = $1 AND is_alive)", steamID).Scan(&stats.IsCurrentlyAlive)
	if err != nil {
		return curatorPlayerStats{}, err
	}

	return stats, nil
}

// renderCuratorContext turns bounded stats into plain-text facts for the
// prompt -- semantic sentences, not raw JSON/IDs, per CGPT-050's "minimize
// data sent to free third-party providers" note. identity.Resolved=false
// means the requesting Discord user could not be matched to any PZ
// player; the LLM must not be given a guessed identity in that case.
func renderCuratorContext(identity resolvedIdentity, stats curatorPlayerStats) string {
	if !identity.Resolved {
		return "The speaker's identity as a specific survivor could not be confirmed. Do not assume which player, if any, they are."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Speaker is the survivor known as %s.\n", identity.Username)
	if !stats.FirstSeen.IsZero() {
		fmt.Fprintf(&b, "First observed: %s. Last observed: %s.\n", stats.FirstSeen.Format("2006-01-02"), stats.LastSeen.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "Zombies eliminated: %d.\n", stats.ZombieKills)
	fmt.Fprintf(&b, "Deaths recorded: %d.\n", stats.Deaths)
	if stats.IsCurrentlyAlive {
		b.WriteString("Currently alive.\n")
	} else {
		b.WriteString("Currently deceased or no active character.\n")
	}
	return b.String()
}
