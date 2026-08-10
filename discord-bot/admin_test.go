package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAdminUserIDs_ParsesChartRenderedShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin-user-ids.json")
	// Exact shape the Helm chart's configmap-discord-bot.yaml renders for
	// 19-digit Discord snowflake IDs -- see the "toJson loses precision"
	// bug fixed there.
	if err := os.WriteFile(path, []byte(`["111111111111111111","222222222222222222"]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := loadAdminUserIDs(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ids["111111111111111111"] || !ids["222222222222222222"] {
		t.Fatalf("expected both IDs present, got %v", ids)
	}
	if len(ids) != 2 {
		t.Fatalf("expected exactly 2 IDs, got %d", len(ids))
	}
}

func TestLoadAdminUserIDs_MissingFileIsEmptyNotError(t *testing.T) {
	ids, err := loadAdminUserIDs(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file should not be an error, got: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty set, got %v", ids)
	}
}

func TestLoadAdminUserIDs_EmptyArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin-user-ids.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := loadAdminUserIDs(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty set, got %v", ids)
	}
}
