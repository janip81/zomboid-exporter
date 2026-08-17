package main

import (
	"context"
	"testing"
	"time"
)

// Regression for the live "two simultaneously alive characters" bug:
// an injury event landing 1.4s after a death, before the real respawn's
// created_player event, previously fabricated a phantom new is_alive=TRUE
// character instead of reusing existing history.
func TestActiveCharacter_StrayEventAfterDeathReusesExistingCharacter(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"
	t0 := time.Now()

	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0})
	firstCharID := countRows(t, s, `SELECT id FROM characters WHERE steam_id = ? ORDER BY character_number DESC LIMIT 1`, sid)

	s.handleDied(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0.Add(2 * time.Second)})

	// A stray/late event (e.g. a lagging "injury" line) arrives before
	// the real respawn's created_player event -- must NOT create a new
	// character.
	charID, err := s.activeCharacter(ctx, sid, t0.Add(2*time.Second+1400*time.Millisecond))
	if err != nil {
		t.Fatalf("activeCharacter: %v", err)
	}
	if int(charID) != firstCharID {
		t.Errorf("stray post-death event attached to character %d, want the existing dead character %d", charID, firstCharID)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ?`, sid); n != 1 {
		t.Fatalf("expected still exactly 1 character row after the stray event, got %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ? AND is_alive = 1`, sid); n != 0 {
		t.Errorf("the reused character must still be marked dead (is_alive=0), got %d alive rows", n)
	}

	// The genuine respawn arrives later -- creates a real second
	// character, and from this point on exactly one row is alive.
	s.handleCreatedPlayer(ctx, &perkEvent{SteamID: sid, Username: "P", Timestamp: t0.Add(30 * time.Second)})

	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ?`, sid); n != 2 {
		t.Fatalf("expected exactly 2 character rows after the real respawn, got %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ? AND is_alive = 1`, sid); n != 1 {
		t.Errorf("expected exactly ONE alive character after the real respawn, got %d -- this is the original live bug", n)
	}
}

// True cold-start (the case this fallback exists for) is unaffected: a
// steamID with NO character history at all still gets a placeholder
// created so the first event has somewhere to attach.
func TestActiveCharacter_TrueColdStartStillCreatesPlaceholder(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	const sid = "76561197965988309"

	if err := s.upsertPlayer(ctx, sid, "P", time.Now()); err != nil {
		t.Fatalf("upsertPlayer: %v", err)
	}
	charID, err := s.activeCharacter(ctx, sid, time.Now())
	if err != nil {
		t.Fatalf("activeCharacter: %v", err)
	}
	if charID == 0 {
		t.Fatal("expected a placeholder character to be created for a genuinely unseen steamID")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ? AND is_alive = 1`, sid); n != 1 {
		t.Errorf("expected exactly 1 alive placeholder character, got %d", n)
	}
}
