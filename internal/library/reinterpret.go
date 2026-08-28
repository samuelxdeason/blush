// Reinterpretation: re-reads every manual person assignment through the
// accounts layer. The rules, agreed explicitly:
//
//   - A person on a video whose connected account OWNS it        -> correct, keep.
//   - A person on a video who is in the platform-asserted CAST   -> they appear
//     in it, they don't own it: move to the featured set.
//   - A person on a video they neither own nor appear in:
//       - X: posted by an account that isn't theirs -> they appear in it;
//         the video itself goes Unsorted (reposters never become people).
//       - Pornhub: either an old mistake or a deliberate save    -> review queue.
//       - Local / account-less sites: a deliberate save          -> keep.
//   - A person whose connected account is in a video's cast but isn't linked
//     to the video at all -> added to featured automatically (platform-asserted,
//     no prompt needed).
//
// Manual assignment NEVER implies account ownership; nothing here writes to
// the accounts table.
package library

import (
	"strings"
)

const savedConfirmedSchema = `
CREATE TABLE IF NOT EXISTS saved_confirmed (
  site     TEXT NOT NULL,
  video_id TEXT NOT NULL,
  person   TEXT NOT NULL,
  PRIMARY KEY (site, video_id, person)
);`

// ReinterpretAction is one proposed (or applied) change.
type ReinterpretAction struct {
	Video  Video  `json:"video"`
	Person string `json:"person"`
	Reason string `json:"reason"`
}

// ReinterpretPlan is everything the accounts layer wants to change.
type ReinterpretPlan struct {
	ToFeatured   []ReinterpretAction `json:"toFeatured"`   // auto: assigned but only appears (cast-verified)
	AutoFeatured []ReinterpretAction `json:"autoFeatured"` // auto: cast-connected person not yet linked
	Review       []ReinterpretAction `json:"review"`       // needs a human: saved on a PH video they neither own nor appear in
}

// accountPerson resolves an account to its connected person.
func (db *DB) accountPerson(a Account) (string, bool) {
	if a.Handle == "" {
		return "", false
	}
	var person string
	err := db.sql.QueryRow(`SELECT COALESCE(person,'') FROM accounts WHERE platform=? AND handle=?`,
		a.Platform, a.Handle).Scan(&person)
	if err != nil || person == "" {
		return "", false
	}
	return person, true
}

// ownerPerson resolves the person connected to a video's owner account.
func (db *DB) ownerPerson(v Video) (string, bool) {
	acct, ok := videoAccount(v)
	if !ok {
		return "", false
	}
	return db.accountPerson(acct)
}

// inCast reports whether a person is platform-asserted cast on the video —
// either via their connected pornhub account or by canonical name match.
func (db *DB) inCast(v Video, person string) bool {
	pSlug := HandleSlug(person)
	for _, member := range v.Cast {
		h := HandleSlug(member)
		if h == "" {
			continue
		}
		if h == pSlug || strings.EqualFold(member, person) {
			return true
		}
		if owner, ok := db.accountPerson(Account{Platform: "pornhub", Handle: h}); ok && strings.EqualFold(owner, person) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// ownsVideo reports whether the person's connected accounts own the video.
func (db *DB) ownsVideo(v Video, person string) bool {
	if owner, ok := db.ownerPerson(v); ok && strings.EqualFold(owner, person) {
		return true
	}
	// Fallback for accounts not yet connected: the person the video's uploader
	// string maps to (the historical backfill rule).
	acct, ok := videoAccount(v)
	return ok && (HandleSlug(person) == acct.Handle || strings.EqualFold(person, v.Uploader))
}

func (db *DB) savedConfirmed(site, id, person string) bool {
	var n int
	_ = db.sql.QueryRow(`SELECT COUNT(*) FROM saved_confirmed WHERE site=? AND video_id=? AND person=?`,
		site, id, person).Scan(&n)
	return n > 0
}

// ConfirmSaved records a human decision: this person is deliberately saved on
// this video — stop proposing it for review.
func (db *DB) ConfirmSaved(site, id, person string) error {
	_, err := db.sql.Exec(`INSERT OR IGNORE INTO saved_confirmed (site, video_id, person) VALUES (?,?,?)`,
		site, id, person)
	return err
}


// fallbackOwner picks the owner slot for a video whose assigned people were
// all demoted: the connected person of its owner account, else (non-X only)
// the uploader display name. X videos go Unsorted instead — reposter accounts
// never become people.
func (db *DB) fallbackOwner(v Video) []string {
	if owner, ok := db.ownerPerson(v); ok {
		return []string{owner}
	}
	if !strings.EqualFold(v.Site, "Twitter") && strings.TrimSpace(v.Uploader) != "" {
		return []string{v.Uploader}
	}
	return []string{}
}

// DemoteToFeatured resolves a review item as "they only appear in it": the
// person leaves the owner slot (owner reassigned if emptied) and joins the
// featured set.
func (db *DB) DemoteToFeatured(site, id, person string) error {
	vids, err := db.query(`WHERE site=? AND id=?`, site, id)
	if err != nil || len(vids) == 0 {
		return err
	}
	v := vids[0]
	keep := make([]string, 0, len(v.Models))
	for _, m := range v.Models {
		if !strings.EqualFold(m, person) {
			keep = append(keep, m)
		}
	}
	if len(keep) == 0 {
		keep = db.fallbackOwner(v)
	}
	if err := db.SetModels(site, id, keep); err != nil {
		return err
	}
	return db.AddFeatured(site, id, person)
}

// BuildReinterpretPlan scans the whole library and proposes changes.
func (db *DB) BuildReinterpretPlan() (ReinterpretPlan, error) {
	plan := ReinterpretPlan{ToFeatured: []ReinterpretAction{}, AutoFeatured: []ReinterpretAction{}, Review: []ReinterpretAction{}}
	vids, err := db.query(`WHERE 1=1 ORDER BY added DESC`)
	if err != nil {
		return plan, err
	}
	for _, v := range vids {
		for _, p := range v.Models {
			if db.ownsVideo(v, p) {
				continue // correct upload/owner assignment
			}
			switch {
			case db.inCast(v, p):
				plan.ToFeatured = append(plan.ToFeatured, ReinterpretAction{Video: v, Person: p,
					Reason: "in the video's cast — appears in it, doesn't own it"})
			case strings.EqualFold(v.Site, "Twitter"):
				// Posted by an account that isn't theirs: they appear in it.
				// (Their own account would make it an upload — connect it.)
				plan.ToFeatured = append(plan.ToFeatured, ReinterpretAction{Video: v, Person: p,
					Reason: "posted by an X account that isn't theirs"})
			case strings.EqualFold(v.Site, "PornHub") && !db.savedConfirmed(v.Site, v.ID, p):
				plan.Review = append(plan.Review, ReinterpretAction{Video: v, Person: p,
					Reason: "saved on a video they neither uploaded nor appear in"})
			}
			// Local and other account-less sites: deliberate save — honored silently.
		}
		// Cast-connected people not yet linked to the video at all.
		for _, member := range v.Cast {
			h := HandleSlug(member)
			if h == "" {
				continue
			}
			person, ok := db.accountPerson(Account{Platform: "pornhub", Handle: h})
			if !ok {
				continue
			}
			if containsFold(v.Models, person) || containsFold(v.Featured, person) {
				continue
			}
			plan.AutoFeatured = append(plan.AutoFeatured, ReinterpretAction{Video: v, Person: person,
				Reason: "their account is in the video's cast"})
		}
	}
	return plan, nil
}

// ApplyReinterpretPlan performs the automatic parts of the plan (review items
// are never touched here). Returns counts.
func (db *DB) ApplyReinterpretPlan() (map[string]int, error) {
	plan, err := db.BuildReinterpretPlan()
	if err != nil {
		return nil, err
	}
	stats := map[string]int{}
	// Group per video so multi-person demotions land in one write (otherwise
	// stale per-action snapshots re-add each other and need extra passes).
	type vidKey struct{ site, id string }
	demote := map[vidKey][]string{}
	byVid := map[vidKey]Video{}
	for _, act := range plan.ToFeatured {
		k := vidKey{act.Video.Site, act.Video.ID}
		demote[k] = append(demote[k], act.Person)
		byVid[k] = act.Video
	}
	for k, people := range demote {
		v := byVid[k]
		keep := make([]string, 0, len(v.Models))
		for _, m := range v.Models {
			if !containsFold(people, m) {
				keep = append(keep, m)
			}
		}
		if len(keep) == 0 {
			keep = db.fallbackOwner(v)
		}
		if err := db.SetModels(v.Site, v.ID, keep); err != nil {
			continue
		}
		for _, p := range people {
			_ = db.AddFeatured(v.Site, v.ID, p)
			stats["moved-to-featured"]++
		}
	}
	for _, act := range plan.AutoFeatured {
		if err := db.AddFeatured(act.Video.Site, act.Video.ID, act.Person); err == nil {
			stats["auto-featured"]++
		}
	}
	stats["review-remaining"] = len(plan.Review)
	return stats, nil
}
