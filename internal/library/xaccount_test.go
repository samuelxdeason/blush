package library

import (
	"path/filepath"
	"testing"
)

// TestXHandleFromURL: profile links and status URLs both yield the handle;
// site pages and non-X URLs don't.
func TestXHandleFromURL(t *testing.T) {
	cases := map[string]string{
		"https://x.com/NikkiiRyder":                             "nikkiiryder",
		"https://x.com/NikkiiRyder/status/2089706/video/1":      "nikkiiryder",
		"https://twitter.com/Some_User123?s=21":                 "some_user123",
		"https://x.com/i/status/123":                            "",
		"https://x.com/search?q=hi":                             "",
		"https://onlyfans.com/nikkiiryder":                      "",
		"https://www.pornhub.com/pornstar/emma-hix":             "",
	}
	for u, want := range cases {
		if got := XHandleFromURL(u); got != want {
			t.Errorf("XHandleFromURL(%q) = %q, want %q", u, got, want)
		}
	}
}

// TestVerifiedXAccountFlow: a saved profile link claims an X account; Upsert
// then auto-assigns new unassigned videos from that account, and
// AssignXHandle claims a pre-existing unsorted backlog.
func TestVerifiedXAccountFlow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A pre-existing unsorted video from the account (before the link is saved).
	if err := db.Upsert(Video{ID: "old1", Site: "Twitter",
		WebpageURL: "https://x.com/NikkiiRyder/status/1/video/1"}); err != nil {
		t.Fatal(err)
	}

	// Save her profile with the X link — the verification.
	if err := db.SaveModelInfo("Nikki Ryder", "", "", []ModelLink{{Label: "X", URL: "https://x.com/NikkiiRyder"}}); err != nil {
		t.Fatal(err)
	}
	xacct := Account{Platform: "x", Handle: "nikkiiryder"}
	if owner, ok := db.AccountOwner(xacct); !ok || owner != "Nikki Ryder" {
		t.Fatalf("AccountOwner = %q, %v", owner, ok)
	}

	// Backlog: the old video shows as unsorted-from-handle, and claiming works.
	vids, err := db.UnsortedFromAccount(xacct)
	if err != nil || len(vids) != 1 {
		t.Fatalf("UnsortedFromAccount = %d videos, err %v", len(vids), err)
	}
	n, err := db.AssignAccount(xacct, "Nikki Ryder")
	if err != nil || n != 1 {
		t.Fatalf("AssignAccount = %d, err %v", n, err)
	}

	// Future: a NEW unassigned video from the account is auto-assigned on Upsert.
	if err := db.Upsert(Video{ID: "new1", Site: "Twitter",
		WebpageURL: "https://x.com/nikkiiryder/status/2/video/1"}); err != nil {
		t.Fatal(err)
	}
	// A repost by someone else stays unsorted.
	if err := db.Upsert(Video{ID: "other1", Site: "Twitter",
		WebpageURL: "https://x.com/SomeReposter/status/3/video/1"}); err != nil {
		t.Fatal(err)
	}

	byModel, err := db.VideosByModel("Nikki Ryder")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, v := range byModel {
		ids[v.ID] = true
	}
	if !ids["old1"] || !ids["new1"] {
		t.Errorf("expected old1 (claimed) + new1 (auto-assigned) under Nikki Ryder, got %v", ids)
	}
	if ids["other1"] {
		t.Errorf("reposter's video must stay unsorted")
	}
}

// TestPornhubVerifiedAccount: a PH pornstar link claims the uploader handle;
// new unassigned PH videos from that uploader auto-assign, others don't.
func TestPornhubVerifiedAccount(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if a, ok := AccountFromURL("https://www.pornhub.com/pornstar/emma-hix/videos/upload"); !ok || a != (Account{Platform: "pornhub", Handle: "emma-hix"}) {
		t.Fatalf("AccountFromURL = %+v, %v", a, ok)
	}
	if err := db.SaveModelInfo("Emma Hix", "", "", []ModelLink{{Label: "PH", URL: "https://www.pornhub.com/pornstar/emma-hix"}}); err != nil {
		t.Fatal(err)
	}
	_ = db.Upsert(Video{ID: "ph1", Site: "PornHub", Uploader: "Emma Hix",
		WebpageURL: "https://www.pornhub.com/view_video.php?viewkey=ph1"})
	_ = db.Upsert(Video{ID: "ph2", Site: "PornHub", Uploader: "Someone Else",
		WebpageURL: "https://www.pornhub.com/view_video.php?viewkey=ph2"})

	got, err := db.VideosByModel("Emma Hix")
	if err != nil || len(got) != 1 || got[0].ID != "ph1" {
		t.Fatalf("expected exactly ph1 under Emma Hix, got %d (err %v)", len(got), err)
	}
}

// TestFeatured: appears-in is independent of ownership and queryable.
func TestFeatured(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.Upsert(Video{ID: "v1", Site: "PornHub", Uploader: "Luke Cooper", Models: []string{"Luke Cooper"}})
	if err := db.AddFeatured("PornHub", "v1", "Cas Summer"); err != nil {
		t.Fatal(err)
	}
	_ = db.AddFeatured("PornHub", "v1", "Cas Summer") // dedupe
	feats, err := db.VideosFeaturing("Cas Summer")
	if err != nil || len(feats) != 1 || len(feats[0].Featured) != 1 {
		t.Fatalf("VideosFeaturing = %d videos (featured %v), err %v", len(feats), feats, err)
	}
	owned, _ := db.VideosByModel("Cas Summer")
	if len(owned) != 0 {
		t.Errorf("featured person must NOT appear in their owned/uploads grid")
	}
}
