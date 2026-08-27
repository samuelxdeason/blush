package core

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateState moves legacy root-level state files into .keepsake, leaves
// media alone, and is safe to run again.
func TestMigrateState(t *testing.T) {
	root := t.TempDir()
	seed := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seed("library.db", "db")
	seed("downloaded.archive", "pornhub 123")
	seed("cookies.txt", "# Netscape HTTP Cookie File")
	seed("sync_cache.json", "{}")
	// A media file that must NOT move.
	mediaDir := filepath.Join(root, "PornHub", "Someone")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaFile := filepath.Join(mediaDir, "clip [x].mp4")
	if err := os.WriteFile(mediaFile, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	migrateState(root)

	dir := stateDir(root)
	for _, name := range []string{"library.db", "downloaded.archive", "cookies.txt", "sync_cache.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should be in .keepsake: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s should no longer be at the root", name)
		}
	}
	if _, err := os.Stat(mediaFile); err != nil {
		t.Errorf("media file must be untouched: %v", err)
	}

	// Idempotent: a second run with the files already migrated is a no-op.
	migrateState(root)
	if got, err := os.ReadFile(filepath.Join(dir, "downloaded.archive")); err != nil || string(got) != "pornhub 123" {
		t.Errorf("second run corrupted state: %q err=%v", got, err)
	}
}

// TestMigrateStateRenamesLegacyDir: a .keepsake folder from the Keepsake era is
// renamed wholesale to .xxx, contents intact.
func TestMigrateStateRenamesLegacyDir(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, legacyStateDirName)
	if err := os.MkdirAll(filepath.Join(old, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "library.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	migrateState(root)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf(".keepsake should be gone after the rename, err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(stateDir(root), "library.db")); err != nil || string(got) != "db" {
		t.Errorf("db should have moved with the folder: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir(root), "meta")); err != nil {
		t.Errorf("subfolders should move with the folder: %v", err)
	}
}
