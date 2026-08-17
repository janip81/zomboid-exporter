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
	Injuries         int
	Deaths           int
	IsCurrentlyAlive bool
	DistanceWalkedKm float64
	DistanceDrivenKm float64
	Drinks           int
	AlcoholMl        float64
	PillsTaken       int
	BooksRead        int
	IndoorHours      float64
	OutdoorHours     float64
}

// fetchCuratorPlayerStats reads LIFETIME totals (summed across every
// recorded character) from characters' per-life aggregate columns
// (character-aggregate-stats.md, exporter side) rather than scanning the
// full events table on every Curator request -- a player may have only a
// handful of character rows, so summing them is effectively free by
// comparison. curator-aggregate-stats-live-test-review.md's
// CURATOR-AGG-LIVE-2: exposes the FULL aggregate set (not just kills/
// injuries) in one query, so a healthy LLM has an authoritative Known
// Fact for any classified SELF_STATS metric instead of having to invent
// one. ZombieKills/Injuries are exact COUNT(*)-of-events semantics (see
// the exporter's aggregateDeltaForEvent), not the Lua-side running
// total, so they remain correct across a corrupted/imprecise Lua steamId
// or a player with many lives.
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

	err = db.QueryRow(ctx, `
		SELECT COALESCE(SUM(zombie_kills), 0), COALESCE(SUM(injuries), 0),
		       COALESCE(SUM(distance_walked_km), 0), COALESCE(SUM(distance_driven_km), 0),
		       COALESCE(SUM(drinks), 0), COALESCE(SUM(alcohol_ml), 0),
		       COALESCE(SUM(pills_taken), 0), COALESCE(SUM(books_read), 0),
		       COALESCE(SUM(indoor_hours), 0), COALESCE(SUM(outdoor_hours), 0)
		FROM characters WHERE steam_id = $1
	`, steamID).Scan(&stats.ZombieKills, &stats.Injuries, &stats.DistanceWalkedKm, &stats.DistanceDrivenKm,
		&stats.Drinks, &stats.AlcoholMl, &stats.PillsTaken, &stats.BooksRead, &stats.IndoorHours, &stats.OutdoorHours)
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

// curatorCharacterStats is the same aggregate shape as curatorPlayerStats
// but for exactly ONE character row, not summed across a player's whole
// history -- used for "current_life" scope (CURATOR-AGG-LIVE-4).
type curatorCharacterStats struct {
	ZombieKills      int
	Injuries         int
	DistanceWalkedKm float64
	DistanceDrivenKm float64
	Drinks           int
	AlcoholMl        float64
	PillsTaken       int
	BooksRead        int
	IndoorHours      float64
	OutdoorHours     float64
}

// fetchCuratorLatestCharacterStats returns the aggregate stats for
// steamID's most-recently-recorded character (by character_number, alive
// or dead). CURATOR-AGG-LIVE-4 is explicit that this is "latest recorded
// character," a deliberately weaker claim than "currently selected
// character" -- is_alive=true alone doesn't prove a character is the one
// actually being played (a player can have more than one alive
// character). ok=false means the steamID has no character rows at all.
func fetchCuratorLatestCharacterStats(ctx context.Context, db *pgxpool.Pool, steamID string) (stats curatorCharacterStats, ok bool, err error) {
	err = db.QueryRow(ctx, `
		SELECT zombie_kills, injuries, distance_walked_km, distance_driven_km,
		       drinks, alcohol_ml, pills_taken, books_read, indoor_hours, outdoor_hours
		FROM characters WHERE steam_id = $1
		ORDER BY character_number DESC LIMIT 1
	`, steamID).Scan(&stats.ZombieKills, &stats.Injuries, &stats.DistanceWalkedKm, &stats.DistanceDrivenKm,
		&stats.Drinks, &stats.AlcoholMl, &stats.PillsTaken, &stats.BooksRead, &stats.IndoorHours, &stats.OutdoorHours)
	if errors.Is(err, pgx.ErrNoRows) {
		return curatorCharacterStats{}, false, nil
	}
	if err != nil {
		return curatorCharacterStats{}, false, err
	}
	return stats, true, nil
}

// curatorStatFact is one deterministic numeric answer to a classified
// SELF_STATS question -- curator-aggregate-stats-live-test-review.md's
// CURATOR-AGG-LIVE-2/3: KnownFact is injected into the LLM's context
// (an unambiguous authoritative number so it never has to invent one),
// and FallbackSentence is sent AS THE REPLY when no LLM is available --
// "LLM optional, never foundational" means a recognized stat question
// must still get the real number, not a generic canned line, when Groq/
// OpenRouter/local providers are unavailable or rate-limited.
type curatorStatFact struct {
	KnownFact        string
	FallbackSentence string
	Resolved         bool
}

// statMetricValue extracts the one number/unit a given metric asks for
// from an aggregate row -- shared by the lifetime and current-life paths
// so the two can't drift in which field maps to which metric.
func statMetricValue(metric curatorStatMetric, kills, injuries, drinks, pills, books int, distanceWalkedKm, distanceDrivenKm, alcoholMl, indoorHours, outdoorHours float64) (label, formatted string, ok bool) {
	switch metric {
	case statMetricKills:
		return "Zombies eliminated", fmt.Sprintf("%d", kills), true
	case statMetricDeaths:
		return "", "", false // deaths is lifecycle-derived (died_at count), not a character aggregate column -- see resolveCuratorStatFact
	case statMetricInjuries:
		return "Injuries recorded", fmt.Sprintf("%d", injuries), true
	case statMetricWalkDistance:
		return "Distance walked", fmt.Sprintf("%.2f km", distanceWalkedKm), true
	case statMetricDriveDistance:
		return "Distance driven", fmt.Sprintf("%.2f km", distanceDrivenKm), true
	case statMetricDrinks:
		return "Drinks consumed", fmt.Sprintf("%d", drinks), true
	case statMetricAlcohol:
		return "Alcohol consumed", fmt.Sprintf("%.0f ml", alcoholMl), true
	case statMetricPills:
		return "Pills taken", fmt.Sprintf("%d", pills), true
	case statMetricBooks:
		return "Books read", fmt.Sprintf("%d", books), true
	case statMetricIndoorTime:
		return "Time spent indoors", fmt.Sprintf("%.2f hours", indoorHours), true
	case statMetricOutdoorTime:
		return "Time spent outdoors", fmt.Sprintf("%.2f hours", outdoorHours), true
	default:
		return "", "", false
	}
}

// resolveCuratorStatFact resolves the speaker's identity and computes
// the deterministic fact for one classified SELF_STATS metric/scope --
// see curatorStatFact's comment for how the result is used on both the
// LLM and no-LLM paths. Resolved=false whenever identity can't be
// confirmed or the metric/scope combination has nothing to report
// (deaths has no current_life meaning -- a "life" that ends in death
// isn't itself re-counted per-life); callers fall back to the existing
// generic context/canned paths in that case.
func resolveCuratorStatFact(ctx context.Context, db *pgxpool.Pool, discordUserID string, candidateNames []string, metric curatorStatMetric, scope curatorStatScope) curatorStatFact {
	if db == nil || metric == statMetricGeneral {
		return curatorStatFact{}
	}
	identity, err := resolveCuratorIdentity(ctx, db, discordUserID, candidateNames)
	if err != nil {
		slog.Error("curator: identity resolution failed (stat fact)", "err", err)
	}
	if !identity.Resolved {
		return curatorStatFact{}
	}

	scopeLabel := "lifetime"
	var label, formatted string
	var ok bool
	switch {
	case metric == statMetricDeaths:
		var deaths int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM characters WHERE steam_id = $1 AND died_at IS NOT NULL", identity.SteamID).Scan(&deaths); err != nil {
			slog.Error("curator: fetch deaths failed (stat fact)", "err", err)
			return curatorStatFact{}
		}
		label, formatted, ok = "Deaths recorded", fmt.Sprintf("%d", deaths), true
	case scope == statScopeCurrentLife:
		scopeLabel = "this life"
		cs, found, err := fetchCuratorLatestCharacterStats(ctx, db, identity.SteamID)
		if err != nil {
			slog.Error("curator: fetch latest character stats failed (stat fact)", "err", err)
			return curatorStatFact{}
		}
		if !found {
			return curatorStatFact{}
		}
		label, formatted, ok = statMetricValue(metric, cs.ZombieKills, cs.Injuries, cs.Drinks, cs.PillsTaken, cs.BooksRead,
			cs.DistanceWalkedKm, cs.DistanceDrivenKm, cs.AlcoholMl, cs.IndoorHours, cs.OutdoorHours)
	default:
		ps, err := fetchCuratorPlayerStats(ctx, db, identity.SteamID)
		if err != nil {
			slog.Error("curator: fetch player stats failed (stat fact)", "err", err)
			return curatorStatFact{}
		}
		label, formatted, ok = statMetricValue(metric, ps.ZombieKills, ps.Injuries, ps.Drinks, ps.PillsTaken, ps.BooksRead,
			ps.DistanceWalkedKm, ps.DistanceDrivenKm, ps.AlcoholMl, ps.IndoorHours, ps.OutdoorHours)
	}
	if !ok {
		return curatorStatFact{}
	}

	return curatorStatFact{
		KnownFact:        fmt.Sprintf("%s (%s): %s.", label, scopeLabel, formatted),
		FallbackSentence: fmt.Sprintf("%s (%s): %s.", label, scopeLabel, formatted),
		Resolved:         true,
	}
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
	fmt.Fprintf(&b, "Injuries sustained: %d.\n", stats.Injuries)
	fmt.Fprintf(&b, "Deaths recorded: %d.\n", stats.Deaths)
	if stats.IsCurrentlyAlive {
		b.WriteString("Currently alive.\n")
	} else {
		b.WriteString("Currently deceased or no active character.\n")
	}
	return b.String()
}
