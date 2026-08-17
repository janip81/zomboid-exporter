package main

import (
	"context"
	"testing"
	"time"
)

// character-aggregate-stats.md acceptance tests (AGG-1..6), DB-integration
// style against a real temp-file sqliteStore -- same pattern as
// store_sqlite_steamid_test.go / store_sqlite_activecharacter_test.go.

func killEvent(sid, username string, at time.Time) *exporterEvent {
	return &exporterEvent{
		Timestamp: at, EventType: "kill", SteamID: sid, Username: username,
		Fields: map[string]any{"steamId": sid, "username": username, "zombieKills": 500.0, "killMethod": "melee"},
	}
}

func injuryEvent(sid, username string, at time.Time) *exporterEvent {
	return &exporterEvent{
		Timestamp: at, EventType: "injury", SteamID: sid, Username: username,
		Fields: map[string]any{"steamId": sid, "username": username, "injury": "scratch", "bodyPart": "Left Arm"},
	}
}

// AGG-1: a kill event attributed to an alive character updates
// zombie_kills and last_event_at.
func TestCharacterStats_AGG1_NormalAliveCharacter(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"
	t0 := time.Now()

	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0})
	s.handleExporterEvent(ctx, killEvent(sid, "P", t0.Add(1*time.Second)))

	var kills int64
	var lastEventAt *string
	if err := s.db.QueryRow(`SELECT zombie_kills, last_event_at FROM characters WHERE steam_id = ?`, sid).Scan(&kills, &lastEventAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if kills != 1 {
		t.Errorf("zombie_kills = %d, want 1", kills)
	}
	if lastEventAt == nil || *lastEventAt == "" {
		t.Error("expected last_event_at to be set")
	}
}

// AGG-2: death with late telemetry -- the trailing injury still
// aggregates onto the dead character (not instantly frozen), no phantom
// character is created (the earlier activeCharacter fix), and the real
// respawn finalizes the previous life.
func TestCharacterStats_AGG2_DeathWithLateTelemetry(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"
	t0 := time.Now()

	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0})
	s.handleDied(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0.Add(50 * time.Second)})
	// Trailing injury 1s after death.
	s.handleExporterEvent(ctx, injuryEvent(sid, "P", t0.Add(51*time.Second)))

	var charAID int64
	var injuries int64
	var finalized bool
	if err := s.db.QueryRow(`SELECT id, injuries, stats_finalized FROM characters WHERE steam_id = ? ORDER BY character_number DESC LIMIT 1`, sid).Scan(&charAID, &injuries, &finalized); err != nil {
		t.Fatalf("query: %v", err)
	}
	if injuries != 1 {
		t.Errorf("A.injuries = %d, want 1 (trailing event must still aggregate)", injuries)
	}
	if finalized {
		t.Error("A must not be finalized instantly on death")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ?`, sid); n != 1 {
		t.Fatalf("expected still exactly 1 character (no phantom), got %d", n)
	}

	// Real respawn 30s later.
	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0.Add(80 * time.Second)})

	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ?`, sid); n != 2 {
		t.Fatalf("expected exactly 2 characters after the real respawn, got %d", n)
	}
	var aFinalized bool
	if err := s.db.QueryRow(`SELECT stats_finalized FROM characters WHERE id = ?`, charAID).Scan(&aFinalized); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !aFinalized {
		t.Error("A must be finalized once the real respawn's created_player event lands")
	}
}

// AGG-3: once finalized, normal live aggregation no longer mutates the
// character.
func TestCharacterStats_AGG3_FinalizedCharacterIgnoresNewAggregation(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"
	t0 := time.Now()

	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0})
	s.handleExporterEvent(ctx, killEvent(sid, "P", t0.Add(1*time.Second)))
	s.handleDied(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0.Add(2 * time.Second)})
	if err := s.finalizeDeadCharacters(ctx, sid, t0.Add(3*time.Second)); err != nil {
		t.Fatalf("finalizeDeadCharacters: %v", err)
	}

	// A late event for the same steamID, still resolving to the
	// finalized character (activeCharacter's dead-reuse path).
	s.handleExporterEvent(ctx, killEvent(sid, "P", t0.Add(10*time.Second)))

	var kills int64
	if err := s.db.QueryRow(`SELECT zombie_kills FROM characters WHERE steam_id = ?`, sid).Scan(&kills); err != nil {
		t.Fatalf("query: %v", err)
	}
	if kills != 1 {
		t.Errorf("zombie_kills = %d, want still 1 -- a finalized character must ignore new aggregation", kills)
	}
}

// AGG-4: reconciliation repairs drift between stored and recomputed
// values.
func TestCharacterStats_AGG4_ReconciliationRepairsDrift(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"
	t0 := time.Now()

	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0})
	s.handleExporterEvent(ctx, killEvent(sid, "P", t0.Add(1*time.Second)))
	s.handleExporterEvent(ctx, killEvent(sid, "P", t0.Add(2*time.Second)))

	var charID int64
	if err := s.db.QueryRow(`SELECT id FROM characters WHERE steam_id = ?`, sid).Scan(&charID); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Corrupt the stored value directly, simulating drift.
	if _, err := s.db.Exec(`UPDATE characters SET zombie_kills = 99 WHERE id = ?`, charID); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	changed, err := s.reconcileCharacterStats(ctx, charID)
	if err != nil {
		t.Fatalf("reconcileCharacterStats: %v", err)
	}
	if !changed {
		t.Fatal("expected reconciliation to report a change")
	}
	var kills int64
	if err := s.db.QueryRow(`SELECT zombie_kills FROM characters WHERE id = ?`, charID).Scan(&kills); err != nil {
		t.Fatalf("query: %v", err)
	}
	if kills != 2 {
		t.Errorf("zombie_kills after reconciliation = %d, want 2 (repaired from the 2 real kill events)", kills)
	}
}

// AGG-5: lifetime totals are a cheap SUM across a player's character rows.
func TestCharacterStats_AGG5_LifetimeTotalsSumAcrossCharacters(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"

	if err := s.upsertPlayer(ctx, sid, "P", time.Now()); err != nil {
		t.Fatalf("upsertPlayer: %v", err)
	}
	for i, kills := range []int64{10, 20, 7} {
		if _, err := s.db.Exec(`
			INSERT INTO characters (steam_id, character_number, created_at, is_alive, server, zombie_kills)
			VALUES (?, ?, ?, 0, ?, ?)
		`, sid, i+1, iso(time.Now()), s.serverName, kills); err != nil {
			t.Fatalf("seed character %d: %v", i, err)
		}
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(zombie_kills), 0) FROM characters WHERE steam_id = ?`, sid).Scan(&total); err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 37 {
		t.Errorf("lifetime total = %d, want 37", total)
	}
}

// AGG-6: the schema accepts multiple simultaneously-alive characters for
// one SteamID without any constraint violation (character-aggregate-
// stats.md explicitly forbids auto-enforcing one-alive-per-player).
func TestCharacterStats_AGG6_SchemaAcceptsMultipleAliveCharacters(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"

	if err := s.upsertPlayer(ctx, sid, "P", time.Now()); err != nil {
		t.Fatalf("upsertPlayer: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := s.db.Exec(`
			INSERT INTO characters (steam_id, character_number, created_at, is_alive, server)
			VALUES (?, ?, ?, 1, ?)
		`, sid, i, iso(time.Now()), s.serverName); err != nil {
			t.Fatalf("insert alive character %d: %v", i, err)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ? AND is_alive = 1`, sid); n != 2 {
		t.Errorf("expected 2 simultaneously alive characters to be accepted, got %d", n)
	}
}
