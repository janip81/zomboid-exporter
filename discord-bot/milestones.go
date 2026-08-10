package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type milestone struct {
	ID          int64
	Name        string
	Kind        string
	Field       string
	FilterField string
	FilterValue string
	Tier        string
	Message     string
}

// seedMilestone is one row for seedMilestones -- kept as a plain struct
// (not loaded from Postgres) since these are starter definitions checked
// into git, not runtime data; day-to-day milestone tuning happens by
// editing the discordbot_milestones table directly (or, later, a web UI),
// not by redeploying the bot.
//
// Kind picks evaluation strategy -- see the comment above
// discordbot_milestones in schema_postgres.sql for the full explanation:
//   - "field" (default, zero value): Field's own value on the triggering
//     event must already be >= Threshold.
//   - "count": COUNT(*) of past events of EventType (optionally narrowed
//     to FilterField=FilterValue) must be >= Threshold.
//   - "sum": same narrowing as "count", but SUMs Field across those rows.
//
// FilterValue may be a comma-separated list of raw values that all count
// toward the same milestone (e.g. every alcoholic fluid type).
type seedMilestone struct {
	Name        string
	EventType   string
	Kind        string
	Field       string
	FilterField string
	FilterValue string
	Threshold   float64
	Tier        string
	Message     string
}

// Alcoholic fluid types, confirmed against the live server's own
// fluids_Alcoholic.txt (module Base) -- every fluid that script file
// classifies as alcoholic, used for the "N alcoholic drinks total"
// milestones so they're not stuck only counting beer/wine/whiskey.
const alcoholicFluids = "Beer,Brandy,Champagne,Cider,CoffeeLiqueur,Curacao,Gin,Grenadine,Mead,Port,Rum,Scotch,Sherry,Tequila,Vermouth,Vodka,Whiskey,Wine"

// Soda-family fluid types, confirmed against fluids_Beverages.txt.
const sodaFluids = "Cola,ColaDiet,GingerAle,SodaBlueberry,SodaBubblegum,SodaPop,SodaLime,SodaGrape,SodaPineapple,SodaStrewberry"

// seedMilestones inserts starter milestone definitions if they don't
// already exist (idempotent -- ON CONFLICT against the unique
// discordbot_milestones_uniq constraint). See ideas/milestones.md for the
// full candidate list this is drawn from.
//
// Deliberately NOT seeded here, because the underlying event data can't
// support them yet (would either never fire or fire on the wrong thing):
//   - Canned food / chips / candy counts: `eat` events carry the item's
//     raw fullType, but there's no confirmed item->category tagging
//     (Tags/Category) to tell "a can of beans" from "a bag of chips" --
//     would need real item-taxonomy research first, not a guess.
//   - Energy drinks: no "EnergyDrink"-family fluid exists in this game
//     version's fluids_Beverages.txt at all (checked directly against
//     the live server) -- would never fire.
//   - "Survive N days": no event carries a live, continuously-updated
//     survival-time-this-life field (PerkLog's HoursSurvived only lands
//     on death/login/level-change, not a running total mid-life).
//   - "Go N hours without sleeping": inverse of Sleeping.lua's tracked
//     streak, which isn't built -- Sleeping.lua only records completed
//     sleep sessions, not an ongoing awake streak.
//   - Vehicle crashes: explicitly flagged future-only in milestones.md,
//     no crash detection exists yet.
func seedMilestones(ctx context.Context, db *pgxpool.Pool) error {
	const kindField = "field"
	const kindCount = "count"
	const kindSum = "sum"

	seeds := []seedMilestone{
		// --- Kills (existing running-total field) ---
		{"First Kill", "kill", kindField, "zombieKills", "", "", 1, "common", "Subject has discovered violence. Promising."},
		{"10 Zombies Killed", "kill", kindField, "zombieKills", "", "", 10, "common", "Initial population control measures successful."},
		{"100 Zombies Killed", "kill", kindField, "zombieKills", "", "", 100, "common", "Triple digits. The local population continues to decline."},
		{"500 Zombies Killed", "kill", kindField, "zombieKills", "", "", 500, "common", "The sample population is shrinking at an inconvenient rate."},
		{"1,000 Zombies Killed", "kill", kindField, "zombieKills", "", "", 1000, "uncommon", "At this point, classification as 'survivor' may be inaccurate."},
		{"5,000 Zombies Killed", "kill", kindField, "zombieKills", "", "", 5000, "rare", "The Curator is beginning to run short of infected test subjects."},
		{"10,000 Zombies Killed", "kill", kindField, "zombieKills", "", "", 10000, "legendary", "The experiment was intended to study the infected, not eradicate them."},
		{"First Bare-Handed Kill", "kill", kindCount, "", "killMethod", "unarmed", 1, "uncommon", "Primitive methods remain surprisingly effective."},
		{"100 Bare-Handed Kills", "kill", kindCount, "", "killMethod", "unarmed", 100, "uncommon", "Footwear durability testing continues."},
		{"500 Bare-Handed Kills", "kill", kindCount, "", "killMethod", "unarmed", 500, "rare", "The boots have now killed more infected than most survivors."},

		// --- Environment streaks (existing running-total field) ---
		{"6h Outdoors Straight", "outdoor_streak", kindField, "hours", "", "", 6, "common", "Extended environmental exposure recorded."},
		{"24h Outdoors Straight", "outdoor_streak", kindField, "hours", "", "", 24, "uncommon", "Subject appears to have misplaced the concept of shelter."},
		{"72h Outdoors Straight", "outdoor_streak", kindField, "hours", "", "", 72, "rare", "Walls remain available. The subject remains uninterested."},
		{"6h Indoors Straight", "indoor_streak", kindField, "hours", "", "", 6, "common", "Field activity has temporarily ceased."},
		{"24h Indoors Straight", "indoor_streak", kindField, "hours", "", "", 24, "common", "Subject has discovered the safest strategy: refusing to participate."},
		{"72h Indoors Straight", "indoor_streak", kindField, "hours", "", "", 72, "uncommon", "The Curator would like to remind the subject that there is, allegedly, an outside."},
		{"7 Days Indoors Straight", "indoor_streak", kindField, "hours", "", "", 168, "rare", "At this point I am classifying the building as part of the subject."},

		// --- Deaths (count of `death` events) ---
		{"First Death", "death", kindCount, "", "", "", 1, "common", "Control group reduced by one."},
		{"5 Deaths", "death", kindCount, "", "", "", 5, "common", "Subject replacement frequency is increasing."},
		{"10 Deaths", "death", kindCount, "", "", "", 10, "uncommon", "The Curator has begun reusing the paperwork."},
		{"25 Deaths", "death", kindCount, "", "", "", 25, "rare", "Death is becoming less an event and more a scheduling issue."},
		{"50 Deaths", "death", kindCount, "", "", "", 50, "rare", "The Curator has stopped learning the replacement subjects' names."},
		{"100 Deaths", "death", kindCount, "", "", "", 100, "legendary", "I am beginning to suspect you misunderstand the objective."},

		// --- Drinking, by fluid type (count of `drink` events) ---
		{"First Beer", "drink", kindCount, "", "fluid", "Beer", 1, "common", "Subject has discovered liquid morale."},
		{"10 Beers", "drink", kindCount, "", "fluid", "Beer", 10, "common", "Alcohol consumption is no longer statistically insignificant."},
		{"50 Beers", "drink", kindCount, "", "fluid", "Beer", 50, "common", "Hydration protocol has become increasingly German."},
		{"100 Beers", "drink", kindCount, "", "fluid", "Beer", 100, "uncommon", "Hydration strategy remains scientifically questionable."},
		{"250 Beers", "drink", kindCount, "", "fluid", "Beer", 250, "rare", "At this point, the brewery should probably be listed as a strategic resource."},
		{"500 Beers", "drink", kindCount, "", "fluid", "Beer", 500, "legendary", "The liver has submitted a formal complaint."},
		{"First Bottle of Wine", "drink", kindCount, "", "fluid", "Wine", 1, "common", "Subject has elected to face the apocalypse with sophistication."},
		{"25 Bottles of Wine", "drink", kindCount, "", "fluid", "Wine", 25, "uncommon", "The apocalypse has developed tasting notes."},
		{"100 Bottles of Wine", "drink", kindCount, "", "fluid", "Wine", 100, "rare", "Cellar management has somehow become a survival skill."},
		{"First Whisky", "drink", kindCount, "", "fluid", "Whiskey", 1, "common", "Kentucky integration proceeding as expected."},
		{"25 Whisky", "drink", kindCount, "", "fluid", "Whiskey", 25, "uncommon", "Subject appears committed to respecting local culture."},
		{"100 Whisky", "drink", kindCount, "", "fluid", "Whiskey", 100, "rare", "The Curator has stopped distinguishing survival supplies from the liquor cabinet."},
		{"100 Alcoholic Drinks Total", "drink", kindCount, "", "fluid", alcoholicFluids, 100, "common", "Ethanol exposure has exceeded incidental levels."},
		{"500 Alcoholic Drinks Total", "drink", kindCount, "", "fluid", alcoholicFluids, 500, "uncommon", "The subject's blood may now qualify as a disinfectant."},
		{"1,000 Alcoholic Drinks Total", "drink", kindCount, "", "fluid", alcoholicFluids, 1000, "legendary", "I am no longer certain the infection is the primary health concern."},

		// --- Non-alcoholic drinking, by fluid type (count of `drink` events) ---
		{"First Coffee", "drink", kindCount, "", "fluid", "Coffee", 1, "common", "Caffeine dependency initialization complete."},
		{"100 Coffees", "drink", kindCount, "", "fluid", "Coffee", 100, "uncommon", "Sleep has apparently been classified as an enemy."},
		{"500 Coffees", "drink", kindCount, "", "fluid", "Coffee", 500, "rare", "Heart rate data has become... energetic."},
		{"First Soda", "drink", kindCount, "", "fluid", sodaFluids, 1, "common", "Carbonated survival strategy detected."},
		{"100 Sodas", "drink", kindCount, "", "fluid", sodaFluids, 100, "common", "Water continues to be treated as optional."},
		{"500 Sodas", "drink", kindCount, "", "fluid", sodaFluids, 500, "uncommon", "Subject appears to be sponsored by the vending machine industry."},
		{"1,000 Sodas", "drink", kindCount, "", "fluid", sodaFluids, 1000, "rare", "The Curator has confirmed that blood is not supposed to fizz."},
		{"100 Units of Water", "drink", kindCount, "", "fluid", "Water", 100, "common", "Actual hydration detected. Remarkable."},
		{"1,000 Units of Water", "drink", kindCount, "", "fluid", "Water", 1000, "uncommon", "The subject has finally discovered the intended use of water."},

		// --- Smoking (count of `smoke` events) ---
		{"First Cigarette", "smoke", kindCount, "", "", "", 1, "common", "Respiratory degradation initiated voluntarily."},
		{"100 Cigarettes Smoked", "smoke", kindCount, "", "", "", 100, "common", "Respiratory testing appears to be progressing voluntarily."},
		{"500 Cigarettes Smoked", "smoke", kindCount, "", "", "", 500, "uncommon", "The lungs continue to file objections."},
		{"1,000 Cigarettes Smoked", "smoke", kindCount, "", "", "", 1000, "uncommon", "At this stage, the zombies may actually be the healthier specimens."},
		{"5,000 Cigarettes Smoked", "smoke", kindCount, "", "", "", 5000, "rare", "The Curator briefly mistook the subject for a chimney."},

		// --- Pills, total (count of `pill` events) ---
		{"First Pill", "pill", kindCount, "", "", "", 1, "common", "Pharmaceutical intervention recorded."},
		{"10 Pills", "pill", kindCount, "", "", "", 10, "common", "Subject has begun exploring modern medicine."},
		{"50 Pills", "pill", kindCount, "", "", "", 50, "common", "The medicine cabinet is becoming an ecosystem."},
		{"100 Pills", "pill", kindCount, "", "", "", 100, "uncommon", "Dosage history is becoming difficult to summarize."},
		{"500 Pills", "pill", kindCount, "", "", "", 500, "rare", "The Curator would like to clarify that 'take as needed' was not a challenge."},
		{"1,000 Pills", "pill", kindCount, "", "", "", 1000, "legendary", "I have reviewed the pharmaceutical records. I have questions."},

		// --- Pills, by type (count of `pill` events, item fullType) ---
		{"First Painkiller", "pill", kindCount, "", "item", "Base.Pills", 1, "common", "Pain acknowledged. Temporarily."},
		{"50 Painkillers", "pill", kindCount, "", "item", "Base.Pills", 50, "uncommon", "Subject continues to treat structural damage as an inconvenience."},
		{"250 Painkillers", "pill", kindCount, "", "item", "Base.Pills", 250, "rare", "Pain management has evolved into pain diplomacy."},
		{"First Sleeping Tablet", "pill", kindCount, "", "item", "Base.PillsSleepingTablets", 1, "common", "Consciousness voluntarily suspended."},
		{"25 Sleeping Tablets", "pill", kindCount, "", "item", "Base.PillsSleepingTablets", 25, "uncommon", "Subject continues to outsource sleep to chemistry."},
		{"100 Sleeping Tablets", "pill", kindCount, "", "item", "Base.PillsSleepingTablets", 100, "rare", "The distinction between sleeping and hibernation is becoming academic."},
		{"First Antidepressant", "pill", kindCount, "", "item", "Base.PillsAntiDep", 1, "common", "Mood stabilization protocol initiated."},
		{"50 Antidepressants", "pill", kindCount, "", "item", "Base.PillsAntiDep", 50, "uncommon", "Psychological maintenance remains ongoing."},
		{"250 Antidepressants", "pill", kindCount, "", "item", "Base.PillsAntiDep", 250, "rare", "The apocalypse persists. So does the prescription."},
		{"First Vitamin", "pill", kindCount, "", "item", "Base.PillsVitamins", 1, "common", "Preventative healthcare. Unexpected."},
		{"100 Vitamins", "pill", kindCount, "", "item", "Base.PillsVitamins", 100, "uncommon", "Nutritional responsibility has appeared far too late to be convincing."},

		// --- Food (count of `eat` events -- only the trivial "first" is
		// safe without item-taxonomy work, see the skip note above) ---
		{"First Food Eaten", "eat", kindCount, "", "", "", 1, "common", "Subject has remembered the metabolic requirement for food."},

		// --- Injuries, total (count of `injury` events) ---
		{"First Injury", "injury", kindCount, "", "", "", 1, "common", "Tissue damage recorded. The experiment proceeds."},
		{"10 Injuries", "injury", kindCount, "", "", "", 10, "common", "Subject continues to learn primarily through impact."},
		{"50 Injuries", "injury", kindCount, "", "", "", 50, "common", "Survival strategy remains physically expensive."},
		{"100 Injuries", "injury", kindCount, "", "", "", 100, "uncommon", "Subject appears fundamentally incompatible with sharp objects."},
		{"250 Injuries", "injury", kindCount, "", "", "", 250, "rare", "The Curator has stopped closing the medical file."},

		// --- Injuries, by type (count of `injury` events, injury field) ---
		{"First Scratch", "injury", kindCount, "", "injury", "scratch", 1, "common", "Minor breach of exterior containment."},
		{"50 Scratches", "injury", kindCount, "", "injury", "scratch", 50, "common", "Skin integrity remains an unresolved engineering problem."},
		{"100 Scratches", "injury", kindCount, "", "injury", "scratch", 100, "uncommon", "The subject continues to use skin as protective equipment."},
		{"First Laceration", "injury", kindCount, "", "injury", "cut", 1, "common", "Exterior damage exceeds recommended tolerances."},
		{"25 Lacerations", "injury", kindCount, "", "injury", "cut", 25, "uncommon", "The subject appears to be losing an argument with sharp objects."},
		{"100 Lacerations", "injury", kindCount, "", "injury", "cut", 100, "rare", "At this point, stitching should qualify as a primary skill."},
		{"First Bite Survived", "injury", kindCount, "", "injury", "bite", 1, "uncommon", "Bite recorded. Prognosis withheld."},
		{"10 Bites", "injury", kindCount, "", "injury", "bite", 10, "rare", "The subject continues to treat teeth as an occupational hazard."},
		{"First Burn", "injury", kindCount, "", "injury", "burn", 1, "common", "Thermal damage recorded."},
		{"25 Burns", "injury", kindCount, "", "injury", "burn", 25, "uncommon", "Fire safety training appears to have been ignored."},
		{"First Fracture", "injury", kindCount, "", "injury", "fracture", 1, "common", "Structural damage detected."},
		{"10 Fractures", "injury", kindCount, "", "injury", "fracture", 10, "uncommon", "Skeletal reliability remains below specification."},
		{"25 Fractures", "injury", kindCount, "", "injury", "fracture", 25, "rare", "The skeleton has requested reassignment."},

		// --- Driving distance (sum of `driving_distance`'s per-flush km chunks) ---
		{"Drive 10 km", "driving_distance", kindSum, "km", "", "", 10, "common", "Motorized mobility confirmed."},
		{"Drive 100 km", "driving_distance", kindSum, "km", "", "", 100, "common", "Mobility trial successful."},
		{"Drive 500 km", "driving_distance", kindSum, "km", "", "", 500, "common", "Subject continues to use Kentucky as a racetrack."},
		{"Drive 1,000 km", "driving_distance", kindSum, "km", "", "", 1000, "uncommon", "Subject has apparently forgotten this is Kentucky, not Euro Truck Simulator."},
		{"Drive 5,000 km", "driving_distance", kindSum, "km", "", "", 5000, "rare", "The odometer has survived longer than several test subjects."},

		// --- Walking / running distance (sum of `movement_distance`'s
		// per-flush km chunks, filtered by movement state; md's thresholds
		// are in miles, converted to km at 1 mi = 1.60934 km since km is
		// what the event payload actually carries) ---
		{"Walk 10 Miles", "movement_distance", kindSum, "km", "movement", "walk", 16.0934, "common", "Locomotion remains functional."},
		{"Walk 100 Miles", "movement_distance", kindSum, "km", "movement", "walk", 160.934, "common", "Shoes remain an underappreciated variable."},
		{"Walk 500 Miles", "movement_distance", kindSum, "km", "movement", "walk", 804.67, "rare", "'And I would walk 500 miles, and I would walk 500 more...' The Curator regrets having this song in the archive."},
		{"Walk 1,000 Miles", "movement_distance", kindSum, "km", "movement", "walk", 1609.34, "rare", "Yes. They actually walked 500 more."},
		{"Run 10 Miles", "movement_distance", kindSum, "km", "movement", "run", 16.0934, "common", "Flight response confirmed."},
		{"Run 100 Miles", "movement_distance", kindSum, "km", "movement", "run", 160.934, "common", "Flight response remains highly developed."},
		{"Run 500 Miles", "movement_distance", kindSum, "km", "movement", "run", 804.67, "uncommon", "The infected appear to have provided an effective fitness program."},
		{"Run 1,000 Miles", "movement_distance", kindSum, "km", "movement", "run", 1609.34, "rare", "The subject has apparently replaced transportation with panic."},
	}
	for _, s := range seeds {
		if _, err := db.Exec(ctx, `
			INSERT INTO discordbot_milestones (name, event_type, kind, field, filter_field, filter_value, threshold, tier, message)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT ON CONSTRAINT discordbot_milestones_uniq DO NOTHING
		`, s.Name, s.EventType, s.Kind, s.Field, s.FilterField, s.FilterValue, s.Threshold, s.Tier, s.Message); err != nil {
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
		SELECT m.id, m.name, m.kind, m.field, m.filter_field, m.filter_value, m.threshold, m.tier, m.message
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
		if err := rows.Scan(&c.m.ID, &c.m.Name, &c.m.Kind, &c.m.Field, &c.m.FilterField, &c.m.FilterValue, &c.threshold, &c.m.Tier, &c.m.Message); err != nil {
			slog.Error("failed to scan milestone row", "err", err)
			continue
		}
		candidates = append(candidates, c)
	}
	rows.Close() // release the cursor before the queries/INSERTs below reuse the pool

	// filterMatches checks whether THIS incoming event even applies to a
	// count/sum-kind milestone's filter before spending a query on it --
	// e.g. no point running the "100 beers" COUNT query on a water-drink
	// event. filterValue may be a comma-separated list (categories like
	// "every alcoholic fluid type").
	filterMatches := func(m milestone) bool {
		if m.FilterField == "" {
			return true
		}
		v, ok := fields[m.FilterField]
		if !ok {
			return false
		}
		got := fmt.Sprint(v)
		for _, want := range strings.Split(m.FilterValue, ",") {
			if got == want {
				return true
			}
		}
		return false
	}

	var hits []milestone
	for _, c := range candidates {
		reached := false
		switch c.m.Kind {
		case "count", "sum":
			if !filterMatches(c.m) {
				continue
			}
			var got float64
			var err error
			if c.m.Kind == "count" {
				var n int64
				err = db.QueryRow(ctx, `
					SELECT COUNT(*) FROM events
					WHERE steam_id = $1 AND event_type = $2
					  AND ($3 = '' OR details->>$3 = ANY(string_to_array($4, ',')))
				`, steamID, eventType, c.m.FilterField, c.m.FilterValue).Scan(&n)
				got = float64(n)
			} else {
				err = db.QueryRow(ctx, `
					SELECT COALESCE(SUM((details->>$5)::numeric), 0) FROM events
					WHERE steam_id = $1 AND event_type = $2
					  AND ($3 = '' OR details->>$3 = ANY(string_to_array($4, ',')))
				`, steamID, eventType, c.m.FilterField, c.m.FilterValue, c.m.Field).Scan(&got)
			}
			if err != nil {
				slog.Error("failed to evaluate count/sum milestone", "milestoneID", c.m.ID, "kind", c.m.Kind, "err", err)
				continue
			}
			reached = got >= c.threshold
		default: // "field" (also the fallback for any legacy blank kind)
			// JSON numbers decode into map[string]any as float64.
			val, ok := fields[c.m.Field].(float64)
			reached = ok && val >= c.threshold
		}
		if !reached {
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
