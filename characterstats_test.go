package main

import "testing"

func TestAggregateDeltaForEvent_Kill(t *testing.T) {
	d := aggregateDeltaForEvent("kill", map[string]any{
		"zombieKills": 501.0, // player-lifetime running total -- must NOT be read directly
		"killMethod":  "melee",
		"weapon":      "Base.Axe",
	})
	if d.ZombieKills != 1 {
		t.Errorf("ZombieKills = %d, want 1 (COUNT semantics, not the running total)", d.ZombieKills)
	}
	if len(d.Breakdown) != 2 {
		t.Errorf("Breakdown = %v, want 2 entries (kill_method + kill_weapon)", d.Breakdown)
	}
}

func TestAggregateDeltaForEvent_Injury(t *testing.T) {
	d := aggregateDeltaForEvent("injury", map[string]any{"injury": "laceration", "bodyPart": "Left Forearm"})
	if d.Injuries != 1 {
		t.Errorf("Injuries = %d, want 1", d.Injuries)
	}
	if len(d.Breakdown) != 2 {
		t.Errorf("Breakdown = %v, want 2 entries", d.Breakdown)
	}
}

func TestAggregateDeltaForEvent_MovementDistance_IsPerFlushDelta(t *testing.T) {
	d := aggregateDeltaForEvent("movement_distance", map[string]any{"km": 0.15, "movement": "walk"})
	if d.DistanceWalkedKm != 0.15 {
		t.Errorf("DistanceWalkedKm = %v, want 0.15 (summed as a delta, not overwritten)", d.DistanceWalkedKm)
	}
}

func TestAggregateDeltaForEvent_DrivingDistance(t *testing.T) {
	d := aggregateDeltaForEvent("driving_distance", map[string]any{"km": 3.2, "vehicle": "Base.PickUpTruck"})
	if d.DistanceDrivenKm != 3.2 {
		t.Errorf("DistanceDrivenKm = %v, want 3.2", d.DistanceDrivenKm)
	}
	if len(d.Breakdown) != 1 || d.Breakdown[0].Category != "drive_vehicle" {
		t.Errorf("Breakdown = %v, want one drive_vehicle entry", d.Breakdown)
	}
}

func TestAggregateDeltaForEvent_Drink_AlcoholicFluidComputesMl(t *testing.T) {
	d := aggregateDeltaForEvent("drink", map[string]any{"fluid": "Beer", "liters": 0.33, "item": "Base.BeerBottle"})
	if d.Drinks != 1 {
		t.Errorf("Drinks = %d, want 1", d.Drinks)
	}
	if d.AlcoholicDrinks != 1 {
		t.Errorf("AlcoholicDrinks = %d, want 1", d.AlcoholicDrinks)
	}
	if d.AlcoholMl != 330 {
		t.Errorf("AlcoholMl = %v, want 330 (0.33L * 1000)", d.AlcoholMl)
	}
}

func TestAggregateDeltaForEvent_Drink_NonAlcoholicFluidNoAlcohol(t *testing.T) {
	d := aggregateDeltaForEvent("drink", map[string]any{"fluid": "Water", "liters": 0.5})
	if d.Drinks != 1 {
		t.Errorf("Drinks = %d, want 1", d.Drinks)
	}
	if d.AlcoholicDrinks != 0 {
		t.Errorf("AlcoholicDrinks = %d, want 0 for a non-alcoholic fluid", d.AlcoholicDrinks)
	}
	if d.AlcoholMl != 0 {
		t.Errorf("AlcoholMl = %v, want 0 for a non-alcoholic fluid", d.AlcoholMl)
	}
}

func TestAggregateDeltaForEvent_Drink_MissingLitersStillCountsAlcoholicDrink(t *testing.T) {
	// ISDrinkFromBottle carries no "liters" field at all -- alcohol_ml
	// under-counts here, but alcoholic_drinks (a plain event count) must
	// not, since it doesn't depend on volume being reported.
	d := aggregateDeltaForEvent("drink", map[string]any{"fluid": "Vodka", "item": "Base.WhiskeyFull"})
	if d.Drinks != 1 {
		t.Errorf("Drinks = %d, want 1", d.Drinks)
	}
	if d.AlcoholicDrinks != 1 {
		t.Errorf("AlcoholicDrinks = %d, want 1 even without a liters field", d.AlcoholicDrinks)
	}
	if d.AlcoholMl != 0 {
		t.Errorf("AlcoholMl = %v, want 0 when liters is absent", d.AlcoholMl)
	}
}

func TestAggregateDeltaForEvent_Pill(t *testing.T) {
	d := aggregateDeltaForEvent("pill", map[string]any{"item": "Base.Pills"})
	if d.PillsTaken != 1 {
		t.Errorf("PillsTaken = %d, want 1", d.PillsTaken)
	}
}

func TestAggregateDeltaForEvent_Read_CompletedSkillBookCounts(t *testing.T) {
	d := aggregateDeltaForEvent("read", map[string]any{"item": "Base.SkillBook", "completed": true, "pagesRead": 12.0})
	if d.BooksRead != 1 {
		t.Errorf("BooksRead = %d, want 1 for a completed skill book", d.BooksRead)
	}
}

func TestAggregateDeltaForEvent_Read_PartialSkillBookDoesNotCount(t *testing.T) {
	d := aggregateDeltaForEvent("read", map[string]any{"item": "Base.SkillBook", "completed": false, "pagesRead": 3.0})
	if d.BooksRead != 0 {
		t.Errorf("BooksRead = %d, want 0 for a partial (not completed) reading session", d.BooksRead)
	}
}

func TestAggregateDeltaForEvent_Read_LiteratureCounts(t *testing.T) {
	d := aggregateDeltaForEvent("read", map[string]any{"item": "Base.ComicBook", "amount": 1.0})
	if d.BooksRead != 1 {
		t.Errorf("BooksRead = %d, want 1 for ordinary literature (amount present)", d.BooksRead)
	}
}

func TestAggregateDeltaForEvent_IndoorStreak_OnlyFinalCounts(t *testing.T) {
	heartbeat := aggregateDeltaForEvent("indoor_streak", map[string]any{"hours": 3.0, "final": false})
	if heartbeat.IndoorHours != 0 {
		t.Errorf("IndoorHours = %v, want 0 for a non-final (running-total) heartbeat", heartbeat.IndoorHours)
	}
	final := aggregateDeltaForEvent("indoor_streak", map[string]any{"hours": 5.0, "final": true})
	if final.IndoorHours != 5.0 {
		t.Errorf("IndoorHours = %v, want 5.0 for the final row", final.IndoorHours)
	}
}

func TestAggregateDeltaForEvent_OutdoorStreak_OnlyFinalCounts(t *testing.T) {
	final := aggregateDeltaForEvent("outdoor_streak", map[string]any{"hours": 2.5, "final": true})
	if final.OutdoorHours != 2.5 {
		t.Errorf("OutdoorHours = %v, want 2.5", final.OutdoorHours)
	}
}

func TestAggregateDeltaForEvent_UnrelatedEventTypeIsAllZero(t *testing.T) {
	d := aggregateDeltaForEvent("enter_vehicle", map[string]any{"vehicle": "Base.Car"})
	zero := characterStatDelta{}
	if !statAggregatesEqual(d, zero) || len(d.Breakdown) != 0 {
		t.Errorf("expected an all-zero delta for an event type with no aggregate impact, got %+v", d)
	}
}

func TestStatAggregatesEqual(t *testing.T) {
	a := characterStatDelta{ZombieKills: 5, DistanceWalkedKm: 1.230000001}
	b := characterStatDelta{ZombieKills: 5, DistanceWalkedKm: 1.23}
	if !statAggregatesEqual(a, b) {
		t.Error("expected floats within epsilon to compare equal")
	}
	c := characterStatDelta{ZombieKills: 6, DistanceWalkedKm: 1.23}
	if statAggregatesEqual(a, c) {
		t.Error("expected differing int fields to compare unequal")
	}
}
