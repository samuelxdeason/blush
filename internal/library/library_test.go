package library

import (
	"path/filepath"
	"testing"
)

func TestMigrateRealLibrary(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), `C:\MediaVault`)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	n, err := db.MigrateFromJSON(`C:\MediaVault\library.json`)
	if err != nil {
		t.Fatal(err)
	}
	count, _ := db.Count()
	models, err := db.Models()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("imported=%d  rows=%d  models=%d", n, count, len(models))
	if n < 70 {
		t.Fatalf("expected ~76 videos imported, got %d", n)
	}
	if len(models) < 50 {
		t.Fatalf("expected ~56 models, got %d", len(models))
	}
	for i, m := range models {
		if i >= 3 {
			break
		}
		t.Logf("  top model: %s / %s — %d videos, %ds, hasThumb=%v",
			m.Site, m.Uploader, m.Count, m.TotalSeconds, m.Thumbnail != "")
	}
}
