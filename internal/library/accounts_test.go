package library

import (
	"path/filepath"
	"testing"
)

func TestParsePHUploaderID(t *testing.T) {
	cases := map[string][2]string{
		"pornstar/arabella-rose":  {"pornstar", "arabella-rose"},
		"/pornstar/arabella-rose": {"pornstar", "arabella-rose"},
		"users/somebody":          {"users", "somebody"},
		"channels/vixen":          {"channel", "vixen"},
		"myanny":                  {"model", "myanny"},
		"":                        {"", ""},
	}
	for raw, want := range cases {
		k, h := ParsePHUploaderID(raw)
		if k != want[0] || h != want[1] {
			t.Errorf("ParsePHUploaderID(%q) = %q,%q want %q,%q", raw, k, h, want[0], want[1])
		}
	}
}

// TestAccountsFromIngest: an Upsert records the platform-asserted accounts
// (owner + cast) and keeps uploader_id/cast on the video row.
func TestAccountsFromIngest(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Upsert(Video{ID: "v1", Site: "PornHub", Uploader: "ArabellaRose",
		UploaderID: "pornstar/arabella-rose", Cast: []string{"Alex Adams", "Arabella Rose"},
		WebpageURL: "https://www.pornhub.com/view_video.php?viewkey=v1"}); err != nil {
		t.Fatal(err)
	}
	accts, err := db.Accounts()
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]AccountInfo{}
	for _, a := range accts {
		byKey[a.Platform+"/"+a.Handle] = a
	}
	if a, ok := byKey["pornhub/arabella-rose"]; !ok || a.Kind != "pornstar" || a.DisplayName != "ArabellaRose" {
		t.Fatalf("owner account wrong: %+v (ok=%v)", a, ok)
	}
	if _, ok := byKey["pornhub/alex-adams"]; !ok {
		t.Fatalf("cast account missing: have %v", byKey)
	}

	// Round-trip: the video row keeps the canonical id + cast.
	vids, _ := db.query(`WHERE site='PornHub' AND id='v1'`)
	if len(vids) != 1 || vids[0].UploaderID != "pornstar/arabella-rose" || len(vids[0].Cast) != 2 {
		t.Fatalf("video row round-trip: %+v", vids)
	}
}

// TestBornLinking: backfill connects a download-created account to the person
// that was auto-created from it, and imports trusted links pre-connected.
func TestBornLinking(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A person with a trusted OnlyFans link, and a video whose uploader
	// auto-created a person named after the account's display name.
	_ = db.SaveModelInfo("Nikki Ryder", "", "", []ModelLink{{Label: "OF", URL: "https://onlyfans.com/nikkiiryder"}})
	_ = db.Upsert(Video{ID: "p1", Site: "PornHub", Uploader: "ArabellaRose",
		UploaderID: "pornstar/arabella-rose", Models: []string{"Arabella Rose"},
		WebpageURL: "https://www.pornhub.com/view_video.php?viewkey=p1"})

	stats, err := db.BackfillAccounts(nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats["from-links"] != 1 {
		t.Errorf("trusted link not imported: %v", stats)
	}
	accts, _ := db.AccountsForPerson("Arabella Rose")
	found := false
	for _, a := range accts {
		if a.Platform == "pornhub" && a.Handle == "arabella-rose" {
			found = true
		}
	}
	if !found {
		t.Errorf("born-linking failed: person's accounts = %v (stats %v)", accts, stats)
	}
	ofAccts, _ := db.AccountsForPerson("Nikki Ryder")
	if len(ofAccts) != 1 || ofAccts[0].Platform != "onlyfans" || ofAccts[0].Handle != "nikkiiryder" {
		t.Errorf("trusted OF link account wrong: %v", ofAccts)
	}
}
