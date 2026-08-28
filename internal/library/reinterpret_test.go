package library

import (
	"path/filepath"
	"testing"
)

// TestReinterpret covers the agreed rules end to end: cast-verified demotion,
// deliberate X saves untouched, auto-featuring of cast-connected people,
// review queue for unexplained PH saves, and keep-decisions persisting.
func TestReinterpret(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The Arabella case: she owns it (canonical id), Alex Adams was assigned
	// but is only cast.
	_ = db.Upsert(Video{ID: "a1", Site: "PornHub", Uploader: "ArabellaRose",
		UploaderID: "pornstar/arabella-rose", Cast: []string{"Alex Adams", "Arabella Rose"},
		Models: []string{"Arabella Rose", "Alex Adams"}})
	_ = db.ConnectAccount("pornhub", "arabella-rose", "Arabella Rose")

	// An X repost saved to its subject: under the appears-in model she is
	// featured, and the video goes Unsorted (reposters never become people).
	_ = db.Upsert(Video{ID: "x1", Site: "Twitter", Models: []string{"Sophie Rain"},
		WebpageURL: "https://x.com/SomeReposter/status/9/video/1"})

	// A PH video with cast including a connected person not linked at all.
	_ = db.ConnectAccount("pornhub", "eva-elfie", "Eva Elfie") // account exists from ingest below
	_ = db.Upsert(Video{ID: "c1", Site: "PornHub", Uploader: "VIXEN", UploaderID: "channels/vixen",
		Cast: []string{"Eva Elfie"}, Models: []string{"VIXEN"}})
	_ = db.ConnectAccount("pornhub", "eva-elfie", "Eva Elfie") // ensure connected after account upsert

	// A PH save with no cast evidence -> review.
	_ = db.Upsert(Video{ID: "r1", Site: "PornHub", Uploader: "RandomChannel",
		UploaderID: "channels/randomchannel", Models: []string{"Cas Summer"}})

	plan, err := db.BuildReinterpretPlan()
	if err != nil {
		t.Fatal(err)
	}
	find := func(list []ReinterpretAction, id, person string) bool {
		for _, a := range list {
			if a.Video.ID == id && a.Person == person {
				return true
			}
		}
		return false
	}
	if !find(plan.ToFeatured, "a1", "Alex Adams") {
		t.Errorf("Alex Adams should be demoted on a1: %+v", plan.ToFeatured)
	}
	if find(plan.ToFeatured, "a1", "Arabella Rose") || find(plan.Review, "a1", "Arabella Rose") {
		t.Errorf("Arabella owns a1 — must not be touched")
	}
	if !find(plan.ToFeatured, "x1", "Sophie Rain") {
		t.Errorf("X save on a non-owned account should become appears-in")
	}
	if !find(plan.Review, "r1", "Cas Summer") {
		t.Errorf("unexplained PH save should be reviewed: %+v", plan.Review)
	}

	// Apply and verify.
	stats, err := db.ApplyReinterpretPlan()
	if err != nil {
		t.Fatal(err)
	}
	if stats["moved-to-featured"] < 1 {
		t.Fatalf("expected moves, got %v", stats)
	}
	a1, _ := db.query(`WHERE site='PornHub' AND id='a1'`)
	if len(a1) != 1 || !containsFold(a1[0].Models, "Arabella Rose") || containsFold(a1[0].Models, "Alex Adams") || !containsFold(a1[0].Featured, "Alex Adams") {
		t.Errorf("a1 after apply: models=%v featured=%v", a1[0].Models, a1[0].Featured)
	}
	c1, _ := db.query(`WHERE site='PornHub' AND id='c1'`)
	if !containsFold(c1[0].Featured, "Eva Elfie") {
		t.Errorf("Eva Elfie should be auto-featured on c1: %v", c1[0].Featured)
	}
	x1, _ := db.query(`WHERE site='Twitter' AND id='x1'`)
	if len(x1[0].Models) != 0 || !containsFold(x1[0].Featured, "Sophie Rain") {
		t.Errorf("x1 after apply: models=%v featured=%v (want Unsorted + featured)", x1[0].Models, x1[0].Featured)
	}

	// Keep-decision: review item confirmed as saved stops appearing.
	if err := db.ConfirmSaved("PornHub", "r1", "Cas Summer"); err != nil {
		t.Fatal(err)
	}
	plan2, _ := db.BuildReinterpretPlan()
	if find(plan2.Review, "r1", "Cas Summer") {
		t.Errorf("confirmed save must leave the review queue")
	}

	// DemoteToFeatured resolves the other way.
	_ = db.Upsert(Video{ID: "r2", Site: "PornHub", Uploader: "OtherChan",
		UploaderID: "channels/otherchan", Models: []string{"Cas Summer"}})
	if err := db.DemoteToFeatured("PornHub", "r2", "Cas Summer"); err != nil {
		t.Fatal(err)
	}
	r2, _ := db.query(`WHERE site='PornHub' AND id='r2'`)
	if !containsFold(r2[0].Featured, "Cas Summer") || containsFold(r2[0].Models, "Cas Summer") || len(r2[0].Models) == 0 {
		t.Errorf("r2 after demote: models=%v featured=%v", r2[0].Models, r2[0].Featured)
	}
}

// TestIngestAutoFeatured: new downloads auto-feature cast-connected people.
func TestIngestAutoFeatured(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Pre-connect Eva to her pornstar account.
	_ = db.UpsertAccount(AccountInfo{Platform: "pornhub", Handle: "eva-elfie", Source: "manual"})
	_ = db.ConnectAccount("pornhub", "eva-elfie", "Eva Elfie")
	_ = db.Upsert(Video{ID: "n1", Site: "PornHub", Uploader: "WOWGIRLS", UploaderID: "channels/wowgirls",
		Cast: []string{"Eva Elfie"}, Models: []string{"WOWGIRLS"}})
	got, _ := db.query(`WHERE site='PornHub' AND id='n1'`)
	if len(got) != 1 || !containsFold(got[0].Featured, "Eva Elfie") {
		t.Fatalf("ingest should auto-feature Eva: featured=%v", got[0].Featured)
	}
}
