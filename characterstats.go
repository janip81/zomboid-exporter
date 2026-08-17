package main

import (
	"context"
	"log/slog"
	"time"
)

// currentStatsRevision identifies which aggregation ruleset produced a
// character's stored aggregate columns -- character-aggregate-stats.md's
// "stats_revision so a future algorithm/schema change can explicitly
// rebuild old lives". Bump this only when aggregateDeltaForEvent's rules
// change in a way that would produce different totals for the same raw
// events; reconciliation re-stamps a row to the current revision whenever
// it recomputes it.
const currentStatsRevision = 1

// characterStatGraceWindow is how long a dead, not-yet-finalized
// character waits for late/stray telemetry before stats aggregation locks
// permanently -- character-aggregate-stats.md's "Death lifecycle: do not
// freeze immediately". Confirmed live (the phantom-character bug this
// design directly follows from): a trailing event can land 1-2 seconds
// after death, and the real respawn's created_player event can take
// 20-30+ seconds.
const characterStatGraceWindow = 90 * time.Second

// alcoholicFluids mirrors discord-bot/milestones.go's list -- duplicated
// rather than imported since discord-bot is a separate Go module.
// Confirmed against the live server's fluids_Alcoholic.txt.
var alcoholicFluids = map[string]bool{
	"Beer": true, "Brandy": true, "Champagne": true, "Cider": true,
	"CoffeeLiqueur": true, "Curacao": true, "Gin": true, "Grenadine": true,
	"Mead": true, "Port": true, "Rum": true, "Scotch": true, "Sherry": true,
	"Tequila": true, "Vermouth": true, "Vodka": true, "Whiskey": true, "Wine": true,
}

// characterStatDelta is what one player-scoped event contributes to its
// character's aggregate columns. Computed once by aggregateDeltaForEvent
// and reused for BOTH incremental live ingestion (ingestExporterEvent)
// and from-scratch reconciliation (reconcileCharacterStats), so the two
// paths can never drift apart from each other -- one ruleset, two
// callers.
type characterStatDelta struct {
	ZombieKills      int64
	Injuries         int64
	DistanceWalkedKm float64
	DistanceDrivenKm float64
	Drinks           int64
	AlcoholMl        float64
	PillsTaken       int64
	BooksRead        int64
	IndoorHours      float64
	OutdoorHours     float64
	Breakdown        []statBreakdownDelta
}

// statBreakdownDelta is one character_stat_breakdown (category, value_key)
// increment implied by a single event.
type statBreakdownDelta struct {
	Category string
	ValueKey string
	Value    float64
}

func fieldString(fields map[string]any, key string) string {
	s, _ := fields[key].(string)
	return s
}

func fieldFloat(fields map[string]any, key string) (float64, bool) {
	v, ok := fields[key].(float64) // encoding/json decodes all JSON numbers as float64
	return v, ok
}

func fieldBool(fields map[string]any, key string) bool {
	b, _ := fields[key].(bool)
	return b
}

// aggregateDeltaForEvent implements character-aggregate-stats.md's
// per-event-type semantics. Some ExporterLog fields are per-event deltas
// (e.g. movement_distance's km, reset to 0 after each flush); some are
// running totals that must NOT be summed (e.g. kill's zombieKills, a
// player-lifetime counter that never resets on death, or indoor_streak's
// hours on non-final heartbeats). Blindly +1/SUM-ing the wrong kind
// silently corrupts every aggregate derived from it -- see the design
// doc's own warning and the research this implementation is grounded in.
func aggregateDeltaForEvent(eventType string, fields map[string]any) characterStatDelta {
	var d characterStatDelta
	switch eventType {
	case "kill":
		// zombieKills is a player-LIFETIME running total (Kills.lua's
		// baseline is seeded to the player's real kill count on first
		// observation and never resets on death) -- it is NOT a
		// per-life value. COUNT(*) of kill events (+1 here) is the
		// correct per-character measure: exactly one kill event is
		// emitted per kill.
		d.ZombieKills = 1
		if method := fieldString(fields, "killMethod"); method != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"kill_method", method, 1})
		}
		if weapon := fieldString(fields, "weapon"); weapon != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"kill_weapon", weapon, 1})
		}
		if vehicle := fieldString(fields, "vehicle"); vehicle != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"kill_vehicle", vehicle, 1})
		}

	case "injury":
		// Edge-triggered on the 0->positive transition of a body part's
		// injury timer; two injuries in the same EveryOneMinute tick
		// produce two genuinely distinct lines (different body part
		// and/or injury type), never a duplicate of the same one. +1
		// per event is correct.
		d.Injuries = 1
		if injury := fieldString(fields, "injury"); injury != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"injury_type", injury, 1})
		}
		if part := fieldString(fields, "bodyPart"); part != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"injury_bodypart", part, 1})
		}

	case "movement_distance":
		// km is a per-flush delta -- Movement.lua's accumulator resets
		// to 0 immediately after each emit -- so this SUMs. All three
		// modes (walk/run/sprint) count toward the single walked-
		// distance column; the mode itself is preserved separately in
		// the breakdown table.
		if km, ok := fieldFloat(fields, "km"); ok {
			d.DistanceWalkedKm = km
			if mode := fieldString(fields, "movement"); mode != "" {
				d.Breakdown = append(d.Breakdown, statBreakdownDelta{"movement_mode", mode, km})
			}
		}

	case "driving_distance":
		if km, ok := fieldFloat(fields, "km"); ok {
			d.DistanceDrivenKm = km
			if vehicle := fieldString(fields, "vehicle"); vehicle != "" {
				d.Breakdown = append(d.Breakdown, statBreakdownDelta{"drive_vehicle", vehicle, km})
			}
		}

	case "drink":
		d.Drinks = 1
		if fluid := fieldString(fields, "fluid"); fluid != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"drink_fluid", fluid, 1})
			// No alcohol-content/ABV field exists in the payload at all
			// -- this is necessarily an approximation (liters * 1000ml),
			// and ISDrinkFromBottle events carry no "liters" field, so
			// that path systematically under-counts. Documented
			// limitation, not a bug: there is no better source data.
			if alcoholicFluids[fluid] {
				if liters, ok := fieldFloat(fields, "liters"); ok {
					d.AlcoholMl = liters * 1000
				}
			}
		}
		if item := fieldString(fields, "item"); item != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"drink_item", item, 1})
		}

	case "pill":
		d.PillsTaken = 1
		if item := fieldString(fields, "item"); item != "" {
			d.Breakdown = append(d.Breakdown, statBreakdownDelta{"pill_item", item, 1})
		}

	case "read":
		// "read" fires for both skill books (pageStart/pageEnd/
		// completed, one event per READING SESSION not per finished
		// book) and ordinary literature (amount=1, always a complete
		// read). Counting every skill-book session would over-count;
		// only a session that actually finished the book (completed)
		// or a literature read (amount present) counts as one book.
		completed := fieldBool(fields, "completed")
		_, hasAmount := fields["amount"]
		if completed || hasAmount {
			d.BooksRead = 1
			if item := fieldString(fields, "item"); item != "" {
				d.Breakdown = append(d.Breakdown, statBreakdownDelta{"read_item", item, 1})
			}
		}

	case "indoor_streak":
		// hours is the running total of the CURRENT open streak on every
		// hourly heartbeat (final=false) and the exact final duration
		// when the streak closes (final=true). Summing every row would
		// massively over-count (a 5-hour streak emits ~15 cumulative
		// hours across its heartbeats before closing) -- only
		// final=true rows contribute.
		if fieldBool(fields, "final") {
			if hours, ok := fieldFloat(fields, "hours"); ok {
				d.IndoorHours = hours
			}
		}

	case "outdoor_streak":
		if fieldBool(fields, "final") {
			if hours, ok := fieldFloat(fields, "hours"); ok {
				d.OutdoorHours = hours
			}
		}

	// vehicle_collisions has no source event -- no crash-detection
	// tracker exists in the Lua mod (see the ROADMAP/milestones.go's own
	// "explicitly flagged future-only" note). Nothing to switch on here
	// until one is added.
	default:
	}
	return d
}

// statAggregatesEqual compares two characterStatDelta values on their
// numeric fields only (Breakdown is intentionally ignored -- it isn't
// populated on values read back from the characters row). Floats use a
// small epsilon since they're sums of already-rounded (2 dp) chunks.
func statAggregatesEqual(a, b characterStatDelta) bool {
	const epsilon = 1e-6
	closeEnough := func(x, y float64) bool {
		diff := x - y
		if diff < 0 {
			diff = -diff
		}
		return diff < epsilon
	}
	return a.ZombieKills == b.ZombieKills &&
		a.Injuries == b.Injuries &&
		a.Drinks == b.Drinks &&
		a.PillsTaken == b.PillsTaken &&
		a.BooksRead == b.BooksRead &&
		closeEnough(a.DistanceWalkedKm, b.DistanceWalkedKm) &&
		closeEnough(a.DistanceDrivenKm, b.DistanceDrivenKm) &&
		closeEnough(a.AlcoholMl, b.AlcoholMl) &&
		closeEnough(a.IndoorHours, b.IndoorHours) &&
		closeEnough(a.OutdoorHours, b.OutdoorHours)
}

// runCharacterFinalizationPipeline periodically closes out dead
// characters that have sat past characterStatGraceWindow with no new
// telemetry -- the time-based finalization trigger from character-
// aggregate-stats.md (the other trigger, a fresh created_player event,
// fires synchronously inside handleCreatedPlayer). A shorter tick than
// the grace window itself keeps the actual finalization latency close to
// the window regardless of tick alignment.
func runCharacterFinalizationPipeline(ctx context.Context, db eventStore) {
	if db == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := db.finalizeStaleCharacters(ctx, characterStatGraceWindow)
			if err != nil {
				slog.Warn("character stats finalization sweep failed", "err", err)
				continue
			}
			if n > 0 {
				slog.Info("character stats finalization sweep", "finalized", n)
			}
		}
	}
}

// runCharacterReconciliationPipeline periodically recomputes finalized
// characters' aggregate stats from raw events and repairs any drift --
// character-aggregate-stats.md's "nightly reconciliation / rebuild safety
// net". Raw events remain authoritative; this is the deterministic
// recoverability path if aggregation logic, ingestion, or the stored
// values themselves ever drift.
func runCharacterReconciliationPipeline(ctx context.Context, db eventStore) {
	if db == nil {
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, repaired, err := db.reconcileFinalizedCharacterStats(ctx)
			if err != nil {
				slog.Warn("character stats reconciliation failed", "err", err)
				continue
			}
			slog.Info("character stats reconciliation complete", "checked", n, "repaired", repaired)
		}
	}
}
