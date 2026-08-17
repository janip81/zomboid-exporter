package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
		identity := resolvedIdentity{SteamID: linkedSteamID, Username: username, Source: "link", Resolved: true}
		logCuratorIdentityResolution(discordUserID, identity, false)
		return identity, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return resolvedIdentity{}, err
	}

	// curator-player-auto-linking.md's AUTO-LINK-4: candidates are tried
	// in this fixed priority order, and the loop stops at the first
	// unique match -- lower-priority candidates are never even compared
	// once one resolves.
	sources := []string{"nickname", "display_name", "account_username"}
	linkSources := []string{"auto_nickname", "auto_display_name", "auto_account_username"}
	for idx, name := range candidateNames {
		if name == "" {
			continue
		}
		steamID, username, matchCount, qErr := lookupUniquePlayerByName(ctx, db, name)
		if qErr != nil {
			return resolvedIdentity{}, qErr
		}
		if matchCount != 1 {
			// 0 (no player by this name) or >1 (ambiguous, e.g. a
			// duplicate/corrupted steam_id row sharing this username) --
			// both fail closed for THIS candidate; still try the next one
			// (AUTO-LINK-3: no fuzzy matching, ever).
			continue
		}
		source := "account_username"
		linkSource := "auto_account_username"
		if idx < len(sources) {
			source, linkSource = sources[idx], linkSources[idx]
		}

		// AUTO-LINK-1/2/6: persist the first unique match as a durable
		// link, fail-closed on any conflict (a concurrent auto-link, an
		// admin link created in between, or -- if the one-Discord-user-
		// per-SteamID invariant is already enforced by the DB's unique
		// index -- this SteamID already belonging to a different Discord
		// user). Never silently steal/overwrite an existing link.
		effectiveSteamID, created, pErr := persistAutoLink(ctx, db, discordUserID, steamID, linkSource, name)
		if pErr != nil {
			return resolvedIdentity{}, pErr
		}
		if effectiveSteamID == "" {
			// This SteamID is already linked to a DIFFERENT Discord user --
			// remain unresolved for this candidate rather than using a
			// match this Discord user isn't durably entitled to.
			logCuratorIdentityResolution(discordUserID, resolvedIdentity{}, false)
			continue
		}
		if effectiveSteamID != steamID {
			// Lost a race to a concurrently-created link for THIS Discord
			// user (AUTO-LINK-5: an existing durable link always wins) --
			// use whatever is now actually on record instead of our own
			// name-derived match.
			effUsername, uErr := lookupUsername(ctx, db, effectiveSteamID)
			if uErr != nil {
				return resolvedIdentity{}, uErr
			}
			identity := resolvedIdentity{SteamID: effectiveSteamID, Username: effUsername, Source: "link", Resolved: true}
			logCuratorIdentityResolution(discordUserID, identity, false)
			return identity, nil
		}

		identity := resolvedIdentity{SteamID: steamID, Username: username, Source: source, Resolved: true}
		logCuratorIdentityResolution(discordUserID, identity, created)
		return identity, nil
	}
	logCuratorIdentityResolution(discordUserID, resolvedIdentity{}, false)
	return resolvedIdentity{}, nil
}

// persistAutoLink durably links discordUserID -> steamID the first time a
// unique exact name match resolves one (AUTO-LINK-1). ON CONFLICT DO
// NOTHING with no target covers a conflict on EITHER unique constraint --
// discord_user_id's primary key or steam_id's unique index -- so this
// never overwrites an existing row either way (AUTO-LINK-2/6). The
// read-back after the insert is the verification step AUTO-LINK-6 calls
// for: it distinguishes "we created it" / "a concurrent identical link
// already exists" (both fine, same effective mapping) from "a DIFFERENT
// link already exists for this Discord user" (use that instead) from "this
// SteamID is already claimed by a different Discord user" (returns "",
// forcing the caller to treat this candidate as unresolved).
func persistAutoLink(ctx context.Context, db *pgxpool.Pool, discordUserID, steamID, linkSource, matchedName string) (effectiveSteamID string, created bool, err error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO discordbot_player_links (discord_user_id, steam_id, linked_by, link_source, matched_name, last_verified_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT DO NOTHING
	`, discordUserID, steamID, "auto", linkSource, matchedName)
	if err != nil {
		return "", false, err
	}
	weInserted := tag.RowsAffected() == 1

	var actual string
	qErr := db.QueryRow(ctx, "SELECT steam_id FROM discordbot_player_links WHERE discord_user_id = $1", discordUserID).Scan(&actual)
	if errors.Is(qErr, pgx.ErrNoRows) {
		// Our own row wasn't created (the steam_id unique index rejected
		// it) and no other row exists for this discord user either --
		// steamID is already claimed by someone else.
		return "", false, nil
	}
	if qErr != nil {
		return "", false, qErr
	}
	return actual, weInserted && actual == steamID, nil
}

// logCuratorIdentityResolution is server-side-only observability
// (AUTO-LINK-9) -- steamID/discordUserID never leave the server logs;
// only semantic facts (never raw IDs) are ever sent to the LLM or Discord,
// unchanged by this logging (AUTO-LINK-8, renderCuratorContext below).
func logCuratorIdentityResolution(discordUserID string, identity resolvedIdentity, autoLinkCreated bool) {
	slog.Info("curator: identity resolution",
		"discordUserID", discordUserID,
		"resolved", identity.Resolved,
		"source", identity.Source,
		"steamID", identity.SteamID,
		"username", identity.Username,
		"autoLinkCreated", autoLinkCreated,
	)
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
