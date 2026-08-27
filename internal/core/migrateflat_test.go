package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trove/internal/library"
)

// buildLegacyVault creates a miniature pre-migration vault: a PornHub download
// with sidecar + thumbnail, a Local import with neither, a model photo, a
// model cover pointing at that photo, and one stray (uncatalogued) file.
func buildLegacyVault(t *testing.T) (root string, db *library.DB) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	phVideo := write(`PornHub\Alice\Alice [ph1].mp4`, "video-ph1")
	write(`PornHub\Alice\Alice [ph1].info.json`, `{"id":"ph1","extractor_key":"PornHub"}`)
	phThumb := write(`PornHub\Alice\Alice [ph1].jpg`, "thumb-ph1")
	localVideo := write(`Local\Bob\clip.mp4`, "video-local")
	// A second catalogue row for the SAME file (the pre-existing duplicate-id
	// situation a drive move + rebuild produces).
	photo := write(`Local\Bob\photos\pic.jpg`, "photo-bob")
	write(`PornHub\stray.txt`, "not catalogued")

	if err := os.MkdirAll(stateDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	db, err = library.Open(filepath.Join(stateDir(root), "library.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	dur := 120
	if err := db.Upsert(library.Video{
		ID: "ph1", Site: "PornHub", Title: "First", Uploader: "Alice",
		Models: []string{"Alice"}, Duration: &dur, Ext: "mp4",
		Filepath: phVideo, Filename: filepath.Base(phVideo), Thumbnail: phThumb,
		Added: "2026-01-01 00:00:00",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Upsert(library.Video{
		ID: "local_abc123", Site: "Local", Title: "Clip", Uploader: "Bob",
		Models: []string{"Bob"}, Ext: "mp4",
		Filepath: localVideo, Filename: "clip.mp4",
		Added: "2026-01-02 00:00:00",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Upsert(library.Video{
		ID: "local_dupe99", Site: "Local", Title: "Clip", Uploader: "Bob",
		Models: []string{"Bob"}, Ext: "mp4",
		Filepath: localVideo, Filename: "clip.mp4",
		Added: "2026-01-02 00:00:00",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPhoto(library.Photo{
		ID: "photo_def456", Model: "Bob", Filepath: photo, Filename: "pic.jpg",
		Added: "2026-01-03 00:00:00",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetModelCover("Bob", photo); err != nil {
		t.Fatal(err)
	}
	return root, db
}

func TestFlatMigrationPlanApplyRollback(t *testing.T) {
	root, db := buildLegacyVault(t)

	plan, err := PlanFlatMigration(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Issues) != 0 {
		t.Fatalf("unexpected issues: %v", plan.Issues)
	}
	if len(plan.Rows) != 4 { // 2 videos + 1 duplicate repoint + 1 photo
		t.Fatalf("expected 4 rows to migrate, got %d", len(plan.Rows))
	}
	if len(plan.Duplicates) != 1 {
		t.Fatalf("expected 1 duplicate repoint, got %v", plan.Duplicates)
	}
	if len(plan.Covers) != 1 {
		t.Fatalf("expected 1 cover fix, got %d", len(plan.Covers))
	}
	foundStray := false
	for _, o := range plan.Orphans {
		if strings.HasSuffix(o, "stray.txt") {
			foundStray = true
		}
	}
	if !foundStray {
		t.Fatalf("stray.txt should be reported as an orphan, got %v", plan.Orphans)
	}

	journal, err := ApplyFlatMigration(db, root, plan, t.Logf)
	if err != nil {
		t.Fatal(err)
	}

	// Files landed at their flat homes, content intact.
	checkContent := func(rel, want string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if string(b) != want {
			t.Fatalf("%s content = %q, want %q", rel, b, want)
		}
	}
	checkContent(`media\pornhub-ph1.mp4`, "video-ph1")
	checkContent(`media\local-abc123.mp4`, "video-local")
	checkContent(`media\photo-def456.jpg`, "photo-bob")
	checkContent(`.trove\meta\pornhub-ph1.info.json`, `{"id":"ph1","extractor_key":"PornHub"}`)
	checkContent(`.trove\thumbs\pornhub-ph1.jpg`, "thumb-ph1")

	// The Local video got a synthetic sidecar carrying its id.
	scB, err := os.ReadFile(filepath.Join(root, `.trove\meta\local-abc123.info.json`))
	if err != nil {
		t.Fatalf("synthetic sidecar: %v", err)
	}
	var sc map[string]any
	if err := json.Unmarshal(scB, &sc); err != nil || sc["id"] != "local_abc123" || sc["extractor_key"] != "Local" {
		t.Fatalf("synthetic sidecar wrong: %s (err %v)", scB, err)
	}

	// The stray file was left exactly where it was.
	if _, err := os.Stat(filepath.Join(root, `PornHub\stray.txt`)); err != nil {
		t.Fatalf("orphan must not be touched: %v", err)
	}
	// Emptied legacy folders are pruned; the orphan's folder survives.
	if _, err := os.Stat(filepath.Join(root, `Local`)); !os.IsNotExist(err) {
		t.Fatalf("emptied Local tree should be pruned, err=%v", err)
	}

	// Catalogue points at the new homes.
	vids, err := db.RawVideos()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]library.RawVideo{}
	for _, v := range vids {
		byID[v.ID] = v
	}
	if fp := byID["ph1"].Filepath; fp != filepath.Join("media", "pornhub-ph1.mp4") {
		t.Fatalf("ph1 filepath = %q", fp)
	}
	if th := byID["ph1"].Thumbnail; th != filepath.Join(".trove", "thumbs", "pornhub-ph1.jpg") {
		t.Fatalf("ph1 thumbnail = %q", th)
	}
	if fp := byID["local_abc123"].Filepath; fp != filepath.Join("media", "local-abc123.mp4") {
		t.Fatalf("local filepath = %q", fp)
	}
	// The duplicate row shares the primary's file — no second copy, no move.
	if fp := byID["local_dupe99"].Filepath; fp != filepath.Join("media", "local-abc123.mp4") {
		t.Fatalf("duplicate row should point at the primary's flat file, got %q", fp)
	}
	if _, err := os.Stat(filepath.Join(root, "media", "local-dupe99.mp4")); !os.IsNotExist(err) {
		t.Fatalf("duplicate row must not create a second file, err=%v", err)
	}
	covers, _ := db.RawModelCovers()
	if cov := covers["Bob"]; cov != filepath.Join("media", "photo-def456.jpg") {
		t.Fatalf("cover = %q", cov)
	}
	// User data survived the path rewrite.
	models, err := db.Models()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, m := range models {
		names[m.Name] = true
	}
	if !names["Alice"] || !names["Bob"] {
		t.Fatalf("model assignments lost: %v", names)
	}

	// Re-running the plan finds nothing left to do (idempotent).
	plan2, err := PlanFlatMigration(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.Rows) != 0 || len(plan2.Covers) != 0 {
		t.Fatalf("second plan should be empty, got %d rows %d covers", len(plan2.Rows), len(plan2.Covers))
	}
	if plan2.Skipped != 4 { // 3 moved rows + the repointed duplicate
		t.Fatalf("second plan skipped = %d, want 4", plan2.Skipped)
	}

	// Rollback restores files and catalogue exactly.
	if err := RollbackFlatMigration(db, root, journal, t.Logf); err != nil {
		t.Fatal(err)
	}
	checkContent(`PornHub\Alice\Alice [ph1].mp4`, "video-ph1")
	checkContent(`PornHub\Alice\Alice [ph1].info.json`, `{"id":"ph1","extractor_key":"PornHub"}`)
	checkContent(`PornHub\Alice\Alice [ph1].jpg`, "thumb-ph1")
	checkContent(`Local\Bob\clip.mp4`, "video-local")
	checkContent(`Local\Bob\photos\pic.jpg`, "photo-bob")
	if _, err := os.Stat(filepath.Join(root, `.trove\meta\local-abc123.info.json`)); !os.IsNotExist(err) {
		t.Fatalf("synthetic sidecar should be removed on rollback, err=%v", err)
	}
	vids2, _ := db.RawVideos()
	for _, v := range vids2 {
		if strings.Contains(v.Filepath, "media") {
			t.Fatalf("rollback left flat path in catalogue: %+v", v)
		}
	}
	covers2, _ := db.RawModelCovers()
	if cov := covers2["Bob"]; !strings.Contains(cov, "pic.jpg") {
		t.Fatalf("cover not rolled back: %q", cov)
	}
}

// TestFlatMigrationCrashResume simulates a crash mid-row (files renamed, DB
// not yet updated): a fresh plan+apply must heal the row, not lose it.
func TestFlatMigrationCrashResume(t *testing.T) {
	root, db := buildLegacyVault(t)

	// Simulate the crash window: the ph1 files were renamed but the DB and
	// journal never saw it.
	mv := func(oldRel, newRel string) {
		t.Helper()
		p := filepath.Join(root, newRel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, oldRel), p); err != nil {
			t.Fatal(err)
		}
	}
	mv(`PornHub\Alice\Alice [ph1].mp4`, `media\pornhub-ph1.mp4`)
	mv(`PornHub\Alice\Alice [ph1].info.json`, `.trove\meta\pornhub-ph1.info.json`)
	mv(`PornHub\Alice\Alice [ph1].jpg`, `.trove\thumbs\pornhub-ph1.jpg`)

	plan, err := PlanFlatMigration(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Missing) != 0 {
		t.Fatalf("half-moved row must not be treated as missing: %v", plan.Missing)
	}
	if _, err := ApplyFlatMigration(db, root, plan, t.Logf); err != nil {
		t.Fatal(err)
	}
	vids, _ := db.RawVideos()
	for _, v := range vids {
		if v.ID == "ph1" && v.Filepath != filepath.Join("media", "pornhub-ph1.mp4") {
			t.Fatalf("ph1 not healed: %q", v.Filepath)
		}
	}
}

func TestFlatBase(t *testing.T) {
	cases := []struct{ site, id, want string }{
		{"PornHub", "ph63451952e0052", "pornhub-ph63451952e0052"},
		{"Local", "local_9f3a2b1c", "local-9f3a2b1c"},
		{"photo", "photo_def456", "photo-def456"},
		{"Porn4Fans", "AbC-12.3", "porn4fans-AbC-12.3"},
		{"Twitter", `we/ird\id`, "twitter-we_ird_id"},
		{"", "", "unknown-unknown"},
	}
	for _, c := range cases {
		if got := library.FlatBase(c.site, c.id); got != c.want {
			t.Errorf("FlatBase(%q,%q) = %q, want %q", c.site, c.id, got, c.want)
		}
	}
}
