package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// steamid64-canonicalization-and-lua-precision.md regression tests --
// DB-integration style (against a real, temp-file sqliteStore) since the
// property under test is persistence + the interaction between the
// in-memory cache/queue and the DB, not just string matching.

func newTestSQLiteStore(t *testing.T) *sqliteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := newSQLiteStore(context.Background(), path, "test-server")
	if err != nil {
		t.Fatalf("newSQLiteStore: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func countRows(t *testing.T, s *sqliteStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// realSteamID/luaCorruptedSteamID are the exact values from the live
// Edd1e360 incident this doc documents.
const (
	realSteamID         = "76561197965988309"
	luaCorruptedSteamID = "76561197965988300"
)

func TestSteamID_ColdCache_NeverCreatesRowUnderLuaID(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	ev := &exporterEvent{
		Timestamp: time.Now(),
		EventType: "kill",
		SteamID:   luaCorruptedSteamID, // Lua-derived, corrupted
		Username:  "Edd1e360",
		Fields:    map[string]any{"steamId": luaCorruptedSteamID, "username": "Edd1e360"},
	}
	s.handleExporterEvent(ctx, ev)

	if n := countRows(t, s, `SELECT COUNT(*) FROM players WHERE steam_id = ?`, luaCorruptedSteamID); n != 0 {
		t.Errorf("expected NO players row under the lossy Lua steamId, got %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM characters WHERE steam_id = ?`, luaCorruptedSteamID); n != 0 {
		t.Errorf("expected NO character under the lossy Lua steamId, got %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM events WHERE steam_id = ?`, luaCorruptedSteamID); n != 0 {
		t.Errorf("expected NO accepted event under the lossy Lua steamId, got %d", n)
	}

	s.mu.Lock()
	pending := len(s.pendingByUsername["Edd1e360"])
	s.mu.Unlock()
	if pending != 1 {
		t.Errorf("expected the event to be queued pending a canonical SteamID, got %d pending", pending)
	}
}

func TestSteamID_DeferredFlush_UsesCanonicalIDAndPreservesLuaValue(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	ev := &exporterEvent{
		Timestamp: time.Now(),
		EventType: "kill",
		SteamID:   luaCorruptedSteamID,
		Username:  "Edd1e360",
		Fields:    map[string]any{"steamId": luaCorruptedSteamID, "username": "Edd1e360", "zombieKills": 2},
	}
	s.handleExporterEvent(ctx, ev) // queued, no canonical mapping yet

	// Native-sourced (PerkLog/connections) event arrives, learns the
	// exact SteamID64, and should flush the queued kill event.
	s.rememberSteamID(ctx, "Edd1e360", realSteamID)

	if n := countRows(t, s, `SELECT COUNT(*) FROM events WHERE steam_id = ? AND event_type = 'kill'`, realSteamID); n != 1 {
		t.Fatalf("expected exactly 1 flushed kill event under the canonical steamID, got %d", n)
	}
	var details string
	if err := s.db.QueryRow(`SELECT details FROM events WHERE steam_id = ? AND event_type = 'kill'`, realSteamID).Scan(&details); err != nil {
		t.Fatalf("query flushed event details: %v", err)
	}
	if want := `"steamId":"` + realSteamID + `"`; !strings.Contains(details, want) {
		t.Errorf("flushed event details missing canonical steamId: %s", details)
	}
	if want := `"_luaSteamId":"` + luaCorruptedSteamID + `"`; !strings.Contains(details, want) {
		t.Errorf("flushed event details missing diagnostic _luaSteamId: %s", details)
	}

	s.mu.Lock()
	pending := len(s.pendingByUsername["Edd1e360"])
	s.mu.Unlock()
	if pending != 0 {
		t.Errorf("expected the pending queue for this username to be empty after flush, got %d", pending)
	}
}

func TestSteamID_WarmCache_IgnoresLuaValueOnceCanonicalKnown(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	s.rememberSteamID(ctx, "Edd1e360", realSteamID)

	ev := &exporterEvent{
		Timestamp: time.Now(),
		EventType: "kill",
		SteamID:   luaCorruptedSteamID, // still corrupted at the source
		Username:  "Edd1e360",
		Fields:    map[string]any{"steamId": luaCorruptedSteamID, "username": "Edd1e360"},
	}
	s.handleExporterEvent(ctx, ev)

	if n := countRows(t, s, `SELECT COUNT(*) FROM events WHERE steam_id = ?`, realSteamID); n != 1 {
		t.Errorf("expected the event to ingest immediately under the canonical steamID, got %d matching rows", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM players WHERE steam_id = ?`, luaCorruptedSteamID); n != 0 {
		t.Errorf("expected NO players row under the lossy Lua steamId even with a warm cache, got %d", n)
	}
}

func TestSteamID_StartupPreload_OnlyLoadsUnambiguousUsernames(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Unambiguous: exactly one steam_id for "Solo".
	if err := s.upsertPlayer(ctx, "111", "Solo", time.Now()); err != nil {
		t.Fatalf("seed Solo: %v", err)
	}
	// Ambiguous (the live Edd1e360 case): two different steam_ids share
	// one username -- must NOT be guessed at during preload.
	if err := s.upsertPlayer(ctx, realSteamID, "Edd1e360", time.Now()); err != nil {
		t.Fatalf("seed Edd1e360 real: %v", err)
	}
	if err := s.upsertPlayer(ctx, luaCorruptedSteamID, "Edd1e360", time.Now()); err != nil {
		t.Fatalf("seed Edd1e360 corrupted: %v", err)
	}

	// Fresh in-memory cache, as if the exporter just restarted.
	s.mu.Lock()
	s.steamIDByUsername = make(map[string]string)
	s.mu.Unlock()

	if err := s.preloadCanonicalSteamIDs(ctx); err != nil {
		t.Fatalf("preloadCanonicalSteamIDs: %v", err)
	}

	if id, ok := s.canonicalSteamID("Solo"); !ok || id != "111" {
		t.Errorf("expected Solo to preload as unambiguous (111), got id=%q ok=%v", id, ok)
	}
	if _, ok := s.canonicalSteamID("Edd1e360"); ok {
		t.Error("expected the ambiguous Edd1e360 username to NOT be preloaded")
	}
}
