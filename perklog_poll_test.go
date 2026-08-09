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
	offsets        map[string]int64
	logins         int
	skills         int
	exporterEvents int
}

func newFakeStore() *fakeStore {
	return &fakeStore{offsets: make(map[string]int64)}
}

func (f *fakeStore) handleLogin(ctx context.Context, ev *perkEvent)             { f.logins++ }
func (f *fakeStore) handleCreatedPlayer(ctx context.Context, ev *perkEvent)     {}
func (f *fakeStore) handleDied(ctx context.Context, ev *perkEvent)              {}
func (f *fakeStore) handleLevelChanged(ctx context.Context, ev *perkEvent)      {}
func (f *fakeStore) handleSkills(ctx context.Context, ev *perkEvent)            { f.skills++ }
func (f *fakeStore) handleExporterEvent(ctx context.Context, ev *exporterEvent) { f.exporterEvents++ }
func (f *fakeStore) Close()                                                     {}

func (f *fakeStore) getFileOffset(ctx context.Context, path string) (int64, error) {
	return f.offsets[path], nil
}

func (f *fakeStore) setFileOffset(ctx context.Context, path string, offset int64) error {
	f.offsets[path] = offset
	return nil
}

// dispatch mirrors what main.go's runPerkLogPipeline does with each parsed
// event: route it to the matching store method by kind. pollOnce itself
// only reads/parses/checkpoints -- it has no opinion on what an event
// means, that's entirely the caller's onEvent callback, same as here.
func dispatch(ctx context.Context, store eventStore, ev *perkEvent) {
	switch ev.Kind {
	case "login":
		store.handleLogin(ctx, ev)
	case "died":
		store.handleDied(ctx, ev)
	case "created_player":
		store.handleCreatedPlayer(ctx, ev)
	case "level_changed":
		store.handleLevelChanged(ctx, ev)
	case "skills":
		store.handleSkills(ctx, ev)
	}
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

	ctx := context.Background()
	store := newFakeStore()
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev); dispatch(ctx, store, ev) }

	done := make(map[string]bool)
	pollOnce(ctx, dir, store, done, onEvent)

	if store.logins != 1 || store.skills != 1 {
		t.Fatalf("expected 1 login + 1 skills event, got logins=%d skills=%d", store.logins, store.skills)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events total, got %d", len(events))
	}
	info, _ := os.Stat(logPath)
	if store.offsets[filepath.Base(logPath)] != info.Size() {
		t.Fatalf("offset should equal file size after full read: got %d want %d", store.offsets[filepath.Base(logPath)], info.Size())
	}

	// Second poll, no new content -- must be a complete no-op.
	pollOnce(ctx, dir, store, done, onEvent)
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
	ctx := context.Background()
	store := newFakeStore()
	store.offsets[filepath.Base(logPath)] = int64(len(loginLine))

	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev); dispatch(ctx, store, ev) }
	pollOnce(ctx, dir, store, make(map[string]bool), onEvent)

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

	ctx := context.Background()
	store := newFakeStore()
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev); dispatch(ctx, store, ev) }
	pollOnce(ctx, dir, store, make(map[string]bool), onEvent)

	if len(events) != 1 {
		t.Fatalf("expected only the complete login line to be processed, got %d events", len(events))
	}
	if store.offsets[filepath.Base(logPath)] != int64(len(loginLine)) {
		t.Fatalf("offset should stop right after the last complete line: got %d want %d", store.offsets[filepath.Base(logPath)], len(loginLine))
	}

	// Now "finish" the write with a newline + Hours Survived, and poll again.
	if err := os.WriteFile(logPath, []byte(partial+`[Hours Survived: 600].`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pollOnce(ctx, dir, store, make(map[string]bool), onEvent)
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

	ctx := context.Background()
	store := newFakeStore()
	done := make(map[string]bool)
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev); dispatch(ctx, store, ev) }
	pollOnce(ctx, dir, store, done, onEvent)

	if !done[filepath.Base(oldPath)] {
		t.Fatal("old (non-newest) fully-read file should be marked done")
	}
	if done[filepath.Base(newPath)] {
		t.Fatal("newest file must never be marked done -- it can still grow")
	}
	if len(events) != 2 {
		t.Fatalf("expected both files' login lines processed once, got %d events", len(events))
	}

	// Remove the old file entirely (simulating log rotation/cleanup) and
	// confirm a subsequent poll doesn't error or reprocess -- it's skipped
	// via the in-memory done set before ever touching the filesystem again.
	os.Remove(oldPath)
	pollOnce(ctx, dir, store, done, onEvent)
	if len(events) != 2 {
		t.Fatalf("expected no new events after old file removal, got %d total", len(events))
	}
}

func TestListPerkLogs_FindsBothFlatAndArchivedFiles(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "Logs")
	archivedDir := filepath.Join(logsDir, "logs_2026-08-06")
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The currently-running session's file sits flat in Logs/ (PZ hasn't
	// archived it yet -- that only happens on the *next* server start).
	flatPath := filepath.Join(logsDir, "2026-08-06_10-00_PerkLog.txt")
	archivedPath := filepath.Join(archivedDir, "2026-08-06_08-00_PerkLog.txt")
	if err := os.WriteFile(flatPath, []byte(loginLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivedPath, []byte(loginLine), 0o644); err != nil {
		t.Fatal(err)
	}

	files := listPerkLogs(dir)
	if len(files) != 2 {
		t.Fatalf("expected both the flat (live) and archived file to be found, got %d: %v", len(files), files)
	}
	if filepath.Base(files[0]) != "2026-08-06_08-00_PerkLog.txt" || filepath.Base(files[1]) != "2026-08-06_10-00_PerkLog.txt" {
		t.Fatalf("expected chronological order by basename regardless of location, got %v", files)
	}
}

func TestPollOnce_CheckpointSurvivesArchiveMove(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "Logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "2026-08-06_08-04_PerkLog.txt"
	flatPath := filepath.Join(logsDir, name)
	if err := os.WriteFile(flatPath, []byte(loginLine), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	var events []*perkEvent
	onEvent := func(ev *perkEvent) { events = append(events, ev); dispatch(ctx, store, ev) }

	// First poll: the session is still running, file is flat -- gets read in full.
	pollOnce(ctx, dir, store, make(map[string]bool), onEvent)
	if store.logins != 1 {
		t.Fatalf("expected 1 login from the flat file, got %d", store.logins)
	}

	// Simulate PZ archiving it on the next server start: same basename,
	// moved under Logs/logs_YYYY-MM-DD/.
	archivedDir := filepath.Join(logsDir, "logs_2026-08-06")
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(flatPath, filepath.Join(archivedDir, name)); err != nil {
		t.Fatal(err)
	}

	// A fresh poll (fresh done map, as if the exporter just restarted)
	// must NOT re-process the login line now that the file lives at a
	// different path -- only the basename-keyed checkpoint should matter.
	pollOnce(ctx, dir, store, make(map[string]bool), onEvent)
	if store.logins != 1 {
		t.Fatalf("checkpoint should have survived the archive move, got logins=%d (content was re-processed)", store.logins)
	}
}
