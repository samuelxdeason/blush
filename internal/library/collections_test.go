package library

import (
	"path/filepath"
	"testing"
)

// seedVideo inserts a minimal video for collection tests.
func seedVideo(t *testing.T, db *DB, site, id, model string) {
	t.Helper()
	if err := db.Upsert(Video{
		ID: id, Site: site, Title: id, Uploader: model,
		Models: []string{model}, Added: "2026-01-01 00:00:00",
	}); err != nil {
		t.Fatalf("seed %s/%s: %v", site, id, err)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestHiddenCollectionExcludedFromDefaultViews(t *testing.T) {
	db := openTestDB(t)
	seedVideo(t, db, "PornHub", "v1", "Alice") // will go in a hidden collection
	seedVideo(t, db, "Local", "v2", "Bob")     // stays visible

	cid, err := db.CreateCollection("Private", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddToCollection(cid, "PornHub", "v1"); err != nil {
		t.Fatal(err)
	}

	// Default feed must hide v1 but keep v2.
	recent, err := db.RecentlyDownloaded(100)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(recent); len(got) != 1 || got[0] != "v2" {
		t.Fatalf("RecentlyDownloaded = %v, want [v2]", got)
	}

	// Search must also hide v1.
	if found, _ := db.Search("v1"); len(found) != 0 {
		t.Fatalf("Search found hidden video: %v", ids(found))
	}

	// The model grid must drop Alice (she has only the hidden video).
	models, err := db.Models()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.Name == "Alice" {
			t.Fatalf("Models() still lists Alice from a hidden collection")
		}
	}

	// Opening the collection itself must show v1.
	members, err := db.VideosByCollection(cid)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(members); len(got) != 1 || got[0] != "v1" {
		t.Fatalf("VideosByCollection = %v, want [v1]", got)
	}

	// Unhiding restores it to the default feed.
	if err := db.SetCollectionHidden(cid, false); err != nil {
		t.Fatal(err)
	}
	if recent, _ := db.RecentlyDownloaded(100); len(recent) != 2 {
		t.Fatalf("after unhide, RecentlyDownloaded = %v, want 2", ids(recent))
	}
}

func TestCollectionMembershipAndDelete(t *testing.T) {
	db := openTestDB(t)
	seedVideo(t, db, "Local", "v1", "Bob")

	cid, _ := db.CreateCollection("Faves", false)
	_ = db.AddToCollection(cid, "Local", "v1")
	_ = db.AddToCollection(cid, "Local", "v1") // idempotent

	if cs, _ := db.CollectionsForVideo("Local", "v1"); len(cs) != 1 || cs[0] != cid {
		t.Fatalf("CollectionsForVideo = %v, want [%d]", cs, cid)
	}
	if list, _ := db.Collections(); len(list) != 1 || list[0].Count != 1 {
		t.Fatalf("Collections count wrong: %+v", list)
	}

	// Deleting the collection leaves the video in the library.
	if err := db.DeleteCollection(cid); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.Collections(); len(list) != 0 {
		t.Fatalf("collection not deleted: %+v", list)
	}
	if n, _ := db.Count(); n != 1 {
		t.Fatalf("video should survive collection delete, Count = %d", n)
	}
}

func TestRemoveModelFromAll(t *testing.T) {
	db := openTestDB(t)
	seedVideo(t, db, "Local", "v1", "Junk") // only "Junk"
	// v2 belongs to both Junk and Alice
	if err := db.Upsert(Video{ID: "v2", Site: "Local", Models: []string{"Junk", "Alice"}, Added: "2026-01-01 00:00:00"}); err != nil {
		t.Fatal(err)
	}
	seedVideo(t, db, "Local", "v3", "Alice") // only Alice — must be untouched

	if err := db.RemoveModelFromAll("Junk"); err != nil {
		t.Fatal(err)
	}

	has := func(vs []Video, id string) bool {
		for _, v := range vs {
			if v.ID == id {
				return true
			}
		}
		return false
	}
	un, _ := db.VideosByModel("") // Unassigned
	if !has(un, "v1") {
		t.Fatalf("v1 (only Junk) should be Unassigned, got %v", ids(un))
	}
	al, _ := db.VideosByModel("Alice")
	if !has(al, "v2") || !has(al, "v3") {
		t.Fatalf("Alice should keep v2 and v3, got %v", ids(al))
	}
	for _, m := range mustModels(t, db) {
		if m.Name == "Junk" {
			t.Fatal("Junk model should be gone after RemoveModelFromAll")
		}
	}
}

func mustModels(t *testing.T, db *DB) []Model {
	t.Helper()
	ms, err := db.Models()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func ids(vs []Video) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}
