package core

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trove/internal/library"
)

// Flat-layout migration: moves every catalogued file from the legacy
// <Site>/<Uploader>/ tree into <root>/media as "<site>-<id>.<ext>", parks
// .info.json sidecars in .trove/meta and thumbnails in .trove/thumbs,
// and rewrites the stored paths. Everything is a same-volume rename — no data
// is copied and nothing is ever deleted. Each executed step is journaled so a
// crashed run can re-run to completion (idempotent) or be rolled back.

// flatMove is one rename, in root-relative terms.
type flatMove struct {
	OldRel string `json:"old"`
	NewRel string `json:"new"`
}

// flatRow is the migration unit for one catalogue row: its renames plus the
// database values before and after (kept so a rollback can restore both).
type flatRow struct {
	Kind         string            `json:"kind"` // "video" | "photo"
	Site         string            `json:"site,omitempty"`
	ID           string            `json:"id"`
	Moves        []flatMove        `json:"moves"`
	SynthSidecar string            `json:"synthSidecar,omitempty"` // rel path of a sidecar this run creates
	synthBody    []byte            // not journaled; rebuilt from DBOld on rollback (file is just deleted)
	DBOld        map[string]string `json:"dbOld"`
	DBNew        map[string]string `json:"dbNew"`
}

// coverFix is a model_info.cover rewrite (the cover pointed at a moved file).
type coverFix struct {
	Model  string `json:"model"`
	OldRel string `json:"old"`
	NewRel string `json:"new"`
}

// FlatPlan is a computed, not-yet-executed migration.
type FlatPlan struct {
	Root       string     `json:"root"`
	Rows       []flatRow  `json:"rows"`
	Covers     []coverFix `json:"covers"`
	Issues     []string   `json:"issues"`  // fatal: apply refuses while any exist
	Missing    []string   `json:"missing"` // rows left untouched: file not on disk
	Skipped    int        `json:"skipped"` // rows already in flat form
	Orphans    []string   `json:"orphans"` // files on disk no catalogue row references
	Duplicates []string   `json:"duplicates"` // extra catalogue rows sharing another row's file (repointed, not moved)
}

var thumbExts = []string{".jpg", ".jpeg", ".webp", ".png"}

// inFlatHome reports whether a stored (relative) path already lives in the
// flat media/ folder. Files there are never renamed by the migration.
func inFlatHome(rel string) bool {
	return strings.HasPrefix(strings.ToLower(rel), strings.ToLower(library.MediaDirName)+string(filepath.Separator))
}

// PlanFlatMigration inspects the vault and computes every rename and DB
// rewrite the migration would perform, plus anything that should stop it.
func PlanFlatMigration(db *library.DB, root string) (*FlatPlan, error) {
	plan := &FlatPlan{Root: root}
	metaRel := filepath.Join(stateDirName, library.MetaDirName)
	thumbsRel := filepath.Join(stateDirName, library.ThumbsDirName)

	// referenced tracks (lowercased, rel) every file the catalogue accounts
	// for — old locations and new — so the orphan scan reports the rest.
	referenced := map[string]bool{}
	taken := map[string]string{} // lower(new rel) -> "site/id" (collision check)
	oldToNew := map[string]string{}
	ref := func(rel string) { referenced[strings.ToLower(rel)] = true }

	// oldOwner: lower(old rel) -> the DBNew values of the row that moves that
	// file. A later row with the same stored filepath is a pre-existing
	// duplicate catalogue entry (Local ids are path hashes, so a vault that
	// changed drives grew a second id per file on rebuild). Those rows move
	// nothing — they're repointed at the same flat file.
	type dupRef struct{ fp, fn, th string }
	oldOwner := map[string]dupRef{}

	claim := func(newRel, who string) bool {
		k := strings.ToLower(newRel)
		if prev, dup := taken[k]; dup {
			plan.Issues = append(plan.Issues, fmt.Sprintf("name collision: %s and %s both map to %s", prev, who, newRel))
			return false
		}
		taken[k] = who
		return true
	}

	vids, err := db.RawVideos()
	if err != nil {
		return nil, err
	}
	for _, v := range vids {
		who := v.Site + "/" + v.ID
		if v.Filepath == "" {
			plan.Missing = append(plan.Missing, who+" (no filepath recorded)")
			continue
		}
		if filepath.IsAbs(v.Filepath) {
			// Stored absolute = the file lives outside the root; leave it alone.
			plan.Missing = append(plan.Missing, who+" (outside vault: "+v.Filepath+")")
			continue
		}
		oldRel := filepath.Clean(v.Filepath)
		oldAbs := filepath.Join(root, oldRel)
		ext := strings.ToLower(filepath.Ext(oldRel))
		base := library.FlatBase(v.Site, v.ID)
		newRel := filepath.Join(library.MediaDirName, base+ext)
		if prior, dup := oldOwner[strings.ToLower(oldRel)]; dup {
			if prior.fp == v.Filepath {
				plan.Skipped++ // repointed by an earlier run — nothing to change
				continue
			}
			th := v.Thumbnail
			if nn, ok := oldToNew[strings.ToLower(filepath.Clean(v.Thumbnail))]; ok && v.Thumbnail != "" {
				th = nn
			} else if th == "" {
				th = prior.th
			}
			plan.Rows = append(plan.Rows, flatRow{
				Kind: "video", Site: v.Site, ID: v.ID,
				DBOld: map[string]string{"filepath": v.Filepath, "filename": v.Filename, "thumbnail": v.Thumbnail},
				DBNew: map[string]string{"filepath": prior.fp, "filename": prior.fn, "thumbnail": th},
			})
			plan.Duplicates = append(plan.Duplicates, who+" shares "+oldRel)
			continue
		}
		if inFlatHome(oldRel) && !strings.EqualFold(oldRel, newRel) {
			// Already in media/ under another row's name: a duplicate that was
			// repointed on an earlier run. Files in media/ are never renamed.
			plan.Skipped++
			ref(oldRel)
			continue
		}
		if _, err := os.Stat(oldAbs); err != nil {
			// Not at the old path — but if it's already at the flat path, this is
			// a crashed run's half-done row: keep it so apply heals the DB.
			if _, err2 := os.Stat(filepath.Join(root, newRel)); err2 != nil {
				plan.Missing = append(plan.Missing, who+" ("+oldRel+" not on disk)")
				continue
			}
		}
		oldStemRel := strings.TrimSuffix(oldRel, filepath.Ext(oldRel))

		if strings.EqualFold(oldRel, newRel) {
			plan.Skipped++
			ref(newRel)
			ref(filepath.Join(metaRel, base+".info.json"))
			for _, te := range thumbExts {
				ref(filepath.Join(thumbsRel, base+te))
			}
			claim(newRel, who)
			oldOwner[strings.ToLower(oldRel)] = dupRef{fp: v.Filepath, fn: v.Filename, th: v.Thumbnail}
			continue
		}
		if !claim(newRel, who) {
			continue
		}

		row := flatRow{Kind: "video", Site: v.Site, ID: v.ID}
		row.Moves = append(row.Moves, flatMove{oldRel, newRel})
		ref(oldRel)
		ref(newRel)
		oldToNew[strings.ToLower(oldRel)] = newRel

		// Sidecar: move it, or synthesize one so rebuild-from-disk can always
		// restore this row (Local imports have no yt-dlp sidecar).
		scOld := oldStemRel + ".info.json"
		scNew := filepath.Join(metaRel, base+".info.json")
		_, scOldErr := os.Stat(filepath.Join(root, scOld))
		_, scNewErr := os.Stat(filepath.Join(root, scNew))
		if scOldErr == nil || scNewErr == nil { // present at either end (crash heal)
			row.Moves = append(row.Moves, flatMove{scOld, scNew})
			ref(scOld)
		} else {
			row.SynthSidecar = scNew
			row.synthBody = synthSidecar(v)
		}
		ref(scNew)

		// Thumbnails: every stem-adjacent image comes along.
		newThumb := ""
		dbThumb := filepath.Clean(v.Thumbnail)
		for _, te := range thumbExts {
			tOld := oldStemRel + te
			tNew := filepath.Join(thumbsRel, base+te)
			_, tOldErr := os.Stat(filepath.Join(root, tOld))
			_, tNewErr := os.Stat(filepath.Join(root, tNew))
			if tOldErr != nil && tNewErr != nil {
				continue
			}
			row.Moves = append(row.Moves, flatMove{tOld, tNew})
			ref(tOld)
			ref(tNew)
			oldToNew[strings.ToLower(tOld)] = tNew
			if v.Thumbnail != "" && strings.EqualFold(dbThumb, tOld) {
				newThumb = tNew
			} else if newThumb == "" {
				newThumb = tNew
			}
		}
		if newThumb == "" {
			newThumb = v.Thumbnail // nothing moved; keep whatever was recorded
		}

		row.DBOld = map[string]string{"filepath": v.Filepath, "filename": v.Filename, "thumbnail": v.Thumbnail}
		row.DBNew = map[string]string{"filepath": newRel, "filename": base + ext, "thumbnail": newThumb}
		plan.Rows = append(plan.Rows, row)
		oldOwner[strings.ToLower(oldRel)] = dupRef{fp: newRel, fn: base + ext, th: newThumb}
	}

	photos, err := db.RawPhotos()
	if err != nil {
		return nil, err
	}
	for _, p := range photos {
		who := "photo/" + p.ID
		if p.Filepath == "" || filepath.IsAbs(p.Filepath) {
			plan.Missing = append(plan.Missing, who+" (no relative filepath)")
			continue
		}
		oldRel := filepath.Clean(p.Filepath)
		ext := strings.ToLower(filepath.Ext(oldRel))
		base := library.FlatBase("photo", p.ID)
		newRel := filepath.Join(library.MediaDirName, base+ext)
		if prior, dup := oldOwner[strings.ToLower(oldRel)]; dup {
			if prior.fp == p.Filepath {
				plan.Skipped++
				continue
			}
			plan.Rows = append(plan.Rows, flatRow{
				Kind: "photo", ID: p.ID,
				DBOld: map[string]string{"filepath": p.Filepath, "filename": p.Filename},
				DBNew: map[string]string{"filepath": prior.fp, "filename": prior.fn},
			})
			plan.Duplicates = append(plan.Duplicates, who+" shares "+oldRel)
			continue
		}
		if inFlatHome(oldRel) && !strings.EqualFold(oldRel, newRel) {
			plan.Skipped++
			ref(oldRel)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, oldRel)); err != nil {
			if _, err2 := os.Stat(filepath.Join(root, newRel)); err2 != nil {
				plan.Missing = append(plan.Missing, who+" ("+oldRel+" not on disk)")
				continue
			}
		}
		if strings.EqualFold(oldRel, newRel) {
			plan.Skipped++
			ref(newRel)
			claim(newRel, who)
			oldOwner[strings.ToLower(oldRel)] = dupRef{fp: p.Filepath, fn: p.Filename}
			continue
		}
		if !claim(newRel, who) {
			continue
		}
		ref(oldRel)
		ref(newRel)
		oldToNew[strings.ToLower(oldRel)] = newRel
		plan.Rows = append(plan.Rows, flatRow{
			Kind: "photo", ID: p.ID,
			Moves: []flatMove{{oldRel, newRel}},
			DBOld: map[string]string{"filepath": p.Filepath, "filename": p.Filename},
			DBNew: map[string]string{"filepath": newRel, "filename": base + ext},
		})
		oldOwner[strings.ToLower(oldRel)] = dupRef{fp: newRel, fn: base + ext}
	}

	// Covers that point at a file we're moving follow it.
	covers, err := db.RawModelCovers()
	if err != nil {
		return nil, err
	}
	for name, cov := range covers {
		if nn, ok := oldToNew[strings.ToLower(filepath.Clean(cov))]; ok {
			plan.Covers = append(plan.Covers, coverFix{Model: name, OldRel: cov, NewRel: nn})
		}
	}

	// Orphan scan: media-looking files under the root that no row references.
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == stateDirName {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if !referenced[strings.ToLower(rel)] {
			plan.Orphans = append(plan.Orphans, rel)
		}
		return nil
	})
	sort.Strings(plan.Orphans)
	return plan, nil
}

// synthSidecar builds a minimal yt-dlp-shaped .info.json from a catalogue row,
// carrying exactly the fields ingest reads, so rebuild-from-disk can restore
// the row (same id, same site) from disk alone.
func synthSidecar(v library.RawVideo) []byte {
	m := map[string]any{
		"id":            v.ID,
		"extractor_key": v.Site,
		"title":         v.Title,
		"uploader":      v.Uploader,
	}
	if v.Duration != nil {
		m["duration"] = *v.Duration
	}
	if v.Width != nil {
		m["width"] = *v.Width
	}
	if v.Height != nil {
		m["height"] = *v.Height
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

// journalRecord is one line of the apply journal (one catalogue row, or one
// cover fix), written only after every action in it has succeeded.
type journalRecord struct {
	Row   *flatRow  `json:"row,omitempty"`
	Cover *coverFix `json:"cover,omitempty"`
}

func flatMigrationDir(root string) string {
	return filepath.Join(stateDir(root), "flat-migration")
}

// ApplyFlatMigration executes a plan: DB snapshot first, then per row — rename
// files, rewrite the row's paths, append to the journal. Renames are
// self-healing (a re-run after a crash skips work already done). Returns the
// journal path.
func ApplyFlatMigration(db *library.DB, root string, plan *FlatPlan, logf func(string, ...any)) (string, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if len(plan.Issues) > 0 {
		return "", fmt.Errorf("refusing to apply: %d issue(s) — first: %s", len(plan.Issues), plan.Issues[0])
	}
	if err := db.ProbeExclusive(); err != nil {
		return "", fmt.Errorf("catalogue is busy (is the daemon still running?): %w", err)
	}

	mdir := flatMigrationDir(root)
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")

	// Snapshot the catalogue before the first write.
	_ = db.Checkpoint()
	snap := filepath.Join(mdir, "library-pre-flat-"+stamp+".db")
	if err := copyFileCore(filepath.Join(stateDir(root), "library.db"), snap); err != nil {
		return "", fmt.Errorf("catalogue snapshot: %w", err)
	}
	logf("catalogue snapshot: %s", snap)

	journalPath := filepath.Join(mdir, "journal-"+stamp+".jsonl")
	jf, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer jf.Close()
	writeRec := func(rec journalRecord) error {
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		_, err = jf.Write(append(b, '\n'))
		return err
	}

	done := 0
	for i := range plan.Rows {
		row := &plan.Rows[i]
		for _, mv := range row.Moves {
			if err := healRename(root, mv.OldRel, mv.NewRel); err != nil {
				return journalPath, fmt.Errorf("%s %s/%s: %w (journal: %s)", row.Kind, row.Site, row.ID, err, journalPath)
			}
		}
		if row.SynthSidecar != "" {
			p := filepath.Join(root, row.SynthSidecar)
			if _, err := os.Stat(p); err != nil {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return journalPath, err
				}
				if err := os.WriteFile(p, row.synthBody, 0o644); err != nil {
					return journalPath, err
				}
			}
		}
		var dbErr error
		if row.Kind == "video" {
			dbErr = db.SetVideoStoredPaths(row.Site, row.ID, row.DBNew["filepath"], row.DBNew["filename"], row.DBNew["thumbnail"])
		} else {
			dbErr = db.SetPhotoStoredPath(row.ID, row.DBNew["filepath"], row.DBNew["filename"])
		}
		if dbErr != nil {
			return journalPath, fmt.Errorf("catalogue update for %s/%s: %w", row.Site, row.ID, dbErr)
		}
		if err := writeRec(journalRecord{Row: row}); err != nil {
			return journalPath, err
		}
		done++
		if done%200 == 0 {
			logf("  %d / %d rows migrated…", done, len(plan.Rows))
		}
	}
	for i := range plan.Covers {
		c := &plan.Covers[i]
		if err := db.SetModelCoverStored(c.Model, c.NewRel); err != nil {
			return journalPath, err
		}
		if err := writeRec(journalRecord{Cover: c}); err != nil {
			return journalPath, err
		}
	}
	_ = db.Checkpoint()

	pruneEmptyDirs(root, logf)
	logf("migrated %d rows, %d cover fixes; journal: %s", done, len(plan.Covers), journalPath)
	return journalPath, nil
}

// healRename moves root/oldRel to root/newRel, treating "already moved" as
// success so an interrupted run can simply be re-run.
func healRename(root, oldRel, newRel string) error {
	oldAbs := filepath.Join(root, oldRel)
	newAbs := filepath.Join(root, newRel)
	_, oldErr := os.Stat(oldAbs)
	_, newErr := os.Stat(newAbs)
	switch {
	case oldErr != nil && newErr == nil:
		return nil // already moved (previous run)
	case oldErr != nil:
		return fmt.Errorf("%s missing (and %s not present)", oldRel, newRel)
	case newErr == nil:
		return fmt.Errorf("both %s and %s exist — refusing to overwrite", oldRel, newRel)
	}
	if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return err
	}
	return os.Rename(oldAbs, newAbs)
}

// pruneEmptyDirs removes directories the migration emptied out (deepest first;
// os.Remove refuses non-empty dirs, so this can never touch content).
func pruneEmptyDirs(root string, logf func(string, ...any)) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return nil
		}
		if e.Name() == stateDirName {
			return fs.SkipDir
		}
		if p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	removed := 0
	for _, d := range dirs {
		if os.Remove(d) == nil {
			removed++
		}
	}
	if removed > 0 {
		logf("removed %d empty folders", removed)
	}
}

// RollbackFlatMigration replays a journal backwards: files return to their old
// paths and the catalogue columns get their old values back.
func RollbackFlatMigration(db *library.DB, root, journalPath string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		return err
	}
	var recs []journalRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec journalRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return fmt.Errorf("corrupt journal line: %w", err)
		}
		recs = append(recs, rec)
	}
	for i := len(recs) - 1; i >= 0; i-- {
		rec := recs[i]
		if rec.Cover != nil {
			if err := db.SetModelCoverStored(rec.Cover.Model, rec.Cover.OldRel); err != nil {
				return err
			}
			continue
		}
		row := rec.Row
		if row == nil {
			continue
		}
		for j := len(row.Moves) - 1; j >= 0; j-- {
			mv := row.Moves[j]
			if err := healRename(root, mv.NewRel, mv.OldRel); err != nil {
				return fmt.Errorf("rollback %s/%s: %w", row.Site, row.ID, err)
			}
		}
		if row.SynthSidecar != "" {
			_ = os.Remove(filepath.Join(root, row.SynthSidecar))
		}
		var dbErr error
		if row.Kind == "video" {
			dbErr = db.SetVideoStoredPaths(row.Site, row.ID, row.DBOld["filepath"], row.DBOld["filename"], row.DBOld["thumbnail"])
		} else {
			dbErr = db.SetPhotoStoredPath(row.ID, row.DBOld["filepath"], row.DBOld["filename"])
		}
		if dbErr != nil {
			return dbErr
		}
	}
	_ = db.Checkpoint()
	logf("rolled back %d journal records", len(recs))
	return nil
}

// RunFlatMigration is the CLI entry point behind `troved migrate-flat`.
// Dry run by default: prints the plan and writes a manifest. --apply executes;
// --rollback replays a journal backwards.
func RunFlatMigration(root string, apply bool, rollbackJournal string) error {
	sdir := stateDir(root)
	dbPath, fresh := resolveDBPath(root, sdir)
	if fresh {
		return fmt.Errorf("no catalogue found under %s — nothing to migrate", root)
	}
	db, err := library.Open(dbPath, root)
	if err != nil {
		return err
	}
	defer db.Close()
	logf := func(format string, a ...any) { fmt.Printf(format+"\n", a...) }

	if rollbackJournal != "" {
		logf("rolling back flat migration from %s …", rollbackJournal)
		return RollbackFlatMigration(db, root, rollbackJournal, logf)
	}

	plan, err := PlanFlatMigration(db, root)
	if err != nil {
		return err
	}
	logf("vault: %s", root)
	logf("plan: %d rows to migrate (%d of them duplicate entries sharing another row's file — repointed, not moved), %d already flat, %d missing/skipped, %d cover fixes, %d orphan files, %d issues",
		len(plan.Rows), len(plan.Duplicates), plan.Skipped, len(plan.Missing), len(plan.Covers), len(plan.Orphans), len(plan.Issues))
	for _, s := range plan.Issues {
		logf("  ISSUE: %s", s)
	}
	for i, s := range plan.Missing {
		if i == 10 {
			logf("  … and %d more (see manifest)", len(plan.Missing)-10)
			break
		}
		logf("  missing: %s", s)
	}

	// The manifest records the full plan (including every orphan) for review.
	if err := os.MkdirAll(flatMigrationDir(root), 0o755); err != nil {
		return err
	}
	manifest := filepath.Join(flatMigrationDir(root), "manifest-"+time.Now().Format("20060102-150405")+".json")
	if b, err := json.MarshalIndent(plan, "", "  "); err == nil {
		if err := os.WriteFile(manifest, b, 0o644); err != nil {
			return err
		}
		logf("manifest: %s", manifest)
	}

	if !apply {
		logf("dry run only — nothing was moved. Re-run with --apply to execute.")
		return nil
	}
	if len(plan.Rows) == 0 && len(plan.Covers) == 0 {
		logf("nothing to do.")
		return nil
	}
	logf("applying…")
	journal, err := ApplyFlatMigration(db, root, plan, logf)
	if err != nil {
		return fmt.Errorf("%w\nThe run is resumable: re-run `troved migrate-flat --apply`, or roll back with --rollback %s", err, journal)
	}
	logf("done. Roll back anytime with: troved migrate-flat --rollback %s", journal)
	return nil
}
