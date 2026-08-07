package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeStore is an in-memory eventStore for testing the poll/offset logic
// in isolation, without a real database.
type fakeStore struct {
	offsets map[string]int64
	logins  int
	skills  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{offsets: make(map[string]int64)}
}

func (f *fakeStore) handleLogin(ctx context.Context, ev *perkEvent)         { f.logins++ }
func (f *fakeStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent) {}
func (f *fakeStore) handleDied(ctx context.Context, ev *perkEvent)          {}
func (f *fakeStore) handleLevelChanged(ctx context.Context, ev *perkEvent)  {}
func (f *fakeStore) handleSkills(ctx context.Context, ev *perkEvent)        { f.skills++ }
func (f *fakeStore) Close()                                                 {}

func (f *fakeStore) getFileOffset(ctx context.Context, path string) (int64, error) {
	return f.offsets[path], nil
}

func (f *fakeStore) setFileOffset(ctx context.Context, path string, offset int64) error {
	f.offsets[path] = offset
	return nil
}

const loginLine = `[06-08-26 08:34:59.194] [76561197965988309][Edd1e360][6764,5380,0][Login][Hours Survived: 472].` + "\n"
const skillLine = `[06-08-26 08:34:59.195] [76561197965988309][Edd1e360][6764,5380,0][Cooking=0, Fitness=5][Hours Survived: 472].` + "\n"

func TestPollOnce_CatchesUpFreshAndSkipsWhenNoNewContent(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-06")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-06_08-04_PerkLog.txt")
	if err := os.WriteFile(logPath, []byte(loginLine+skillLine), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev) }

	done := make(map[string]bool)
	pollOnce(context.Background(), dir, store, done, onEvent)

	if store.logins != 1 || store.skills != 1 {
		t.Fatalf("expected 1 login + 1 skills event, got logins=%d skills=%d", store.logins, store.skills)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events total, got %d", len(events))
	}
	info, _ := os.Stat(logPath)
	if store.offsets[logPath] != info.Size() {
		t.Fatalf("offset should equal file size after full read: got %d want %d", store.offsets[logPath], info.Size())
	}

	// Second poll, no new content -- must be a complete no-op.
	pollOnce(context.Background(), dir, store, done, onEvent)
	if store.logins != 1 || store.skills != 1 {
		t.Fatalf("second poll re-processed content: logins=%d skills=%d", store.logins, store.skills)
	}
}

func TestPollOnce_RestartResumesFromPersistedOffset(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-06")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-06_08-04_PerkLog.txt")
	if err := os.WriteFile(logPath, []byte(loginLine+skillLine), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate a fresh process (empty in-memory done set) that already has
	// a persisted checkpoint from a previous run past the login line but
	// not the skill line -- only the skill line should be (re-)processed.
	store := newFakeStore()
	store.offsets[logPath] = int64(len(loginLine))

	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev) }
	pollOnce(context.Background(), dir, store, make(map[string]bool), onEvent)

	if store.logins != 0 {
		t.Fatalf("login line before the checkpoint must not be reprocessed, got logins=%d", store.logins)
	}
	if store.skills != 1 {
		t.Fatalf("expected the skill line past the checkpoint to be processed, got skills=%d", store.skills)
	}
}

func TestPollOnce_PartialTrailingLineNotConsumed(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-06")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-06_08-04_PerkLog.txt")
	partial := loginLine + `[06-08-26 08:35:00.000] [76561197965988309][Edd1e360][0,0,0][Died]` // no trailing newline, mid-write
	if err := os.WriteFile(logPath, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev) }
	pollOnce(context.Background(), dir, store, make(map[string]bool), onEvent)

	if len(events) != 1 {
		t.Fatalf("expected only the complete login line to be processed, got %d events", len(events))
	}
	if store.offsets[logPath] != int64(len(loginLine)) {
		t.Fatalf("offset should stop right after the last complete line: got %d want %d", store.offsets[logPath], len(loginLine))
	}

	// Now "finish" the write with a newline + Hours Survived, and poll again.
	if err := os.WriteFile(logPath, []byte(partial+`[Hours Survived: 600].`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pollOnce(context.Background(), dir, store, make(map[string]bool), onEvent)
	if len(events) != 2 {
		t.Fatalf("expected the completed line to be picked up on the next poll, got %d events total", len(events))
	}
}

func TestPollOnce_OldFileMarkedDoneAndNeverRereadEvenIfDeleted(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-06")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(logDir, "2026-08-06_00-00_PerkLog.txt")
	newPath := filepath.Join(logDir, "2026-08-06_08-04_PerkLog.txt")
	if err := os.WriteFile(oldPath, []byte(loginLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte(loginLine), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	done := make(map[string]bool)
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev) }
	pollOnce(context.Background(), dir, store, done, onEvent)

	if !done[oldPath] {
		t.Fatal("old (non-newest) fully-read file should be marked done")
	}
	if done[newPath] {
		t.Fatal("newest file must never be marked done -- it can still grow")
	}
	if len(events) != 2 {
		t.Fatalf("expected both files' login lines processed once, got %d events", len(events))
	}

	// Remove the old file entirely (simulating log rotation/cleanup) and
	// confirm a subsequent poll doesn't error or reprocess -- it's skipped
	// via the in-memory done set before ever touching the filesystem again.
	os.Remove(oldPath)
	pollOnce(context.Background(), dir, store, done, onEvent)
	if len(events) != 2 {
		t.Fatalf("expected no new events after old file removal, got %d total", len(events))
	}
}
