// Platform accounts: the identities that actually own videos. Accounts are
// created by downloads (every video upserts its uploader; Pornhub also
// asserts cast members), imported from a person's trusted profile links, or
// defined manually. People CONNECT to accounts — videos never attach to a
// person's name directly, so renaming a person can't orphan anything.
package library

import (
	"encoding/json"
	"strings"
	"time"
)

// AccountInfo is one platform identity row.
type AccountInfo struct {
	Platform    string `json:"platform"` // "pornhub" | "x" | "redgifs" | "onlyfans" | "fansly" | …
	Handle      string `json:"handle"`   // canonical lowercase handle/slug
	Kind        string `json:"kind"`     // pornhub: "pornstar" | "model" | "channel" | "users"; else ""
	DisplayName string `json:"displayName"`
	URL         string `json:"url"`
	Person      string `json:"person"` // connected person ("" = unconnected)
	Source      string `json:"source"` // "download" | "link" | "manual"
	FirstSeen   string `json:"firstSeen"`
}

const accountsSchema = `
CREATE TABLE IF NOT EXISTS accounts (
  platform     TEXT NOT NULL,
  handle       TEXT NOT NULL,
  kind         TEXT DEFAULT '',
  display_name TEXT DEFAULT '',
  url          TEXT DEFAULT '',
  person       TEXT DEFAULT '',
  source       TEXT DEFAULT '',
  first_seen   TEXT DEFAULT '',
  PRIMARY KEY (platform, handle)
);
CREATE INDEX IF NOT EXISTS idx_accounts_person ON accounts(person);`

// ParsePHUploaderID splits Pornhub's uploader_id ("pornstar/arabella-rose",
// "/users/foo", bare "myanny" for model channels) into kind + handle.
func ParsePHUploaderID(raw string) (kind, handle string) {
	raw = strings.Trim(strings.TrimSpace(strings.ToLower(raw)), "/")
	if raw == "" {
		return "", ""
	}
	parts := strings.Split(raw, "/")
	if len(parts) >= 2 {
		k := parts[0]
		if k == "channels" {
			k = "channel"
		}
		return k, parts[1]
	}
	return "model", parts[0]
}

// UpsertAccount records an account sighting. Descriptive fields fill in when
// the row is new or previously blank; the person connection is NEVER touched
// here (only ConnectAccount changes it).
func (db *DB) UpsertAccount(a AccountInfo) error {
	a.Handle = strings.ToLower(strings.TrimSpace(a.Handle))
	if a.Platform == "" || a.Handle == "" {
		return nil
	}
	if a.FirstSeen == "" {
		a.FirstSeen = time.Now().Format("2006-01-02 15:04:05")
	}
	_, err := db.sql.Exec(`
INSERT INTO accounts (platform, handle, kind, display_name, url, person, source, first_seen)
VALUES (?,?,?,?,?,'',?,?)
ON CONFLICT(platform, handle) DO UPDATE SET
  kind         = CASE WHEN accounts.kind=''         THEN excluded.kind         ELSE accounts.kind END,
  display_name = CASE WHEN accounts.display_name='' THEN excluded.display_name ELSE accounts.display_name END,
  url          = CASE WHEN accounts.url=''          THEN excluded.url          ELSE accounts.url END`,
		a.Platform, a.Handle, a.Kind, a.DisplayName, a.URL, a.Source, a.FirstSeen)
	return err
}

// ConnectAccount sets (or clears, person="") the person an account belongs to.
func (db *DB) ConnectAccount(platform, handle, person string) error {
	_, err := db.sql.Exec(`UPDATE accounts SET person=? WHERE platform=? AND handle=?`,
		strings.TrimSpace(person), platform, strings.ToLower(strings.TrimSpace(handle)))
	return err
}

// Accounts returns every account, connected first, then by platform/handle.
func (db *DB) Accounts() ([]AccountInfo, error) {
	rows, err := db.sql.Query(`
SELECT platform, handle, COALESCE(kind,''), COALESCE(display_name,''), COALESCE(url,''),
       COALESCE(person,''), COALESCE(source,''), COALESCE(first_seen,'')
FROM accounts ORDER BY person<>'' DESC, platform, handle`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountInfo
	for rows.Next() {
		var a AccountInfo
		if err := rows.Scan(&a.Platform, &a.Handle, &a.Kind, &a.DisplayName, &a.URL, &a.Person, &a.Source, &a.FirstSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AccountsForPerson returns the accounts connected to one person.
func (db *DB) AccountsForPerson(name string) ([]AccountInfo, error) {
	all, err := db.Accounts()
	if err != nil {
		return nil, err
	}
	out := []AccountInfo{}
	for _, a := range all {
		if strings.EqualFold(a.Person, name) {
			out = append(out, a)
		}
	}
	return out, nil
}

// VideosUploadedBy returns videos owned by any of the person's connected
// accounts — the platform-derived "their uploads" set, independent of the
// legacy model strings.
func (db *DB) VideosUploadedBy(person string) ([]Video, error) {
	accts, err := db.AccountsForPerson(person)
	if err != nil {
		return nil, err
	}
	own := map[Account]bool{}
	for _, a := range accts {
		own[Account{Platform: a.Platform, Handle: a.Handle}] = true
	}
	if len(own) == 0 {
		return []Video{}, nil
	}
	vids, err := db.query(`WHERE ` + notHidden + ` ORDER BY added DESC, id`)
	if err != nil {
		return nil, err
	}
	out := []Video{}
	for _, v := range vids {
		if a, ok := videoAccount(v); ok && own[a] {
			out = append(out, v)
		}
	}
	return out, nil
}

// VideosSavedBy returns videos manually filed under the person that their
// accounts do NOT own — deliberate saves (reposts, local imports).
func (db *DB) VideosSavedBy(person string) ([]Video, error) {
	assigned, err := db.VideosByModel(person)
	if err != nil {
		return nil, err
	}
	accts, err := db.AccountsForPerson(person)
	if err != nil {
		return nil, err
	}
	own := map[Account]bool{}
	for _, a := range accts {
		own[Account{Platform: a.Platform, Handle: a.Handle}] = true
	}
	out := []Video{}
	for _, v := range assigned {
		if a, ok := videoAccount(v); ok && own[a] {
			continue // that's an upload
		}
		out = append(out, v)
	}
	return out, nil
}

// AccountVideoCounts tallies how many videos each account owns.
func (db *DB) AccountVideoCounts() (map[Account]int, error) {
	vids, err := db.query(`WHERE 1=1`)
	if err != nil {
		return nil, err
	}
	counts := map[Account]int{}
	for _, v := range vids {
		if a, ok := videoAccount(v); ok {
			counts[a]++
		}
	}
	return counts, nil
}

// upsertVideoAccounts records the accounts a video asserts: its uploader as
// owner, and (Pornhub) each cast member. Called from Upsert on every ingest.
func (db *DB) upsertVideoAccounts(v Video) {
	switch {
	case strings.EqualFold(v.Site, "PornHub"):
		if kind, handle := ParsePHUploaderID(v.UploaderID); handle != "" {
			url := "https://www.pornhub.com/" + kind + "/" + handle
			if kind == "model" {
				url = "https://www.pornhub.com/model/" + handle
			}
			_ = db.UpsertAccount(AccountInfo{Platform: "pornhub", Handle: handle, Kind: kind,
				DisplayName: v.Uploader, URL: url, Source: "download"})
		} else if h := HandleSlug(v.Uploader); h != "" {
			_ = db.UpsertAccount(AccountInfo{Platform: "pornhub", Handle: h,
				DisplayName: v.Uploader, Source: "download"})
		}
		for _, member := range v.Cast {
			if h := HandleSlug(member); h != "" {
				_ = db.UpsertAccount(AccountInfo{Platform: "pornhub", Handle: h, Kind: "pornstar",
					DisplayName: member, URL: "https://www.pornhub.com/pornstar/" + h, Source: "download"})
			}
		}
	case strings.EqualFold(v.Site, "Twitter"):
		if h := XHandleFromURL(v.WebpageURL); h != "" {
			_ = db.UpsertAccount(AccountInfo{Platform: "x", Handle: h,
				DisplayName: v.Uploader, URL: "https://x.com/" + h, Source: "download"})
		}
	case strings.EqualFold(v.Site, "RedGifs"):
		if h := HandleSlug(v.Uploader); h != "" {
			_ = db.UpsertAccount(AccountInfo{Platform: "redgifs", Handle: h,
				DisplayName: v.Uploader, URL: "https://www.redgifs.com/users/" + h, Source: "download"})
		}
	}
}

// BackfillAccounts builds the accounts table from everything we already have:
// trusted profile links (arrive pre-connected — the user curated them), the
// Pornhub sidecars' canonical uploader_id + cast (also copied onto the video
// rows), X handles from URLs, and RedGifs uploaders. Finally, download-created
// accounts get "born-linked" to the person that was auto-created from them
// (exact display-name or slug match, only when unambiguous).
func (db *DB) BackfillAccounts(readSidecar func(site, id string) (uploaderID string, cast []string, ok bool)) (map[string]int, error) {
	stats := map[string]int{}

	// 1. Trusted links -> connected accounts.
	if rows, err := db.sql.Query(`SELECT name, COALESCE(links,'') FROM model_info WHERE links IS NOT NULL AND links<>''`); err == nil {
		type link struct{ name, url string }
		var todo []link
		for rows.Next() {
			var name, raw string
			if rows.Scan(&name, &raw) != nil {
				continue
			}
			var links []ModelLink
			_ = json.Unmarshal([]byte(raw), &links)
			for _, l := range links {
				todo = append(todo, link{name, l.URL})
			}
		}
		rows.Close()
		for _, l := range todo {
			if a, ok := AccountFromURL(l.url); ok {
				_ = db.UpsertAccount(AccountInfo{Platform: a.Platform, Handle: a.Handle, URL: l.url, Source: "link"})
				_ = db.ConnectAccount(a.Platform, a.Handle, l.name)
				stats["from-links"]++
			}
		}
	}

	// 2. Videos -> owner accounts (+ uploader_id/cast copied onto the row).
	vids, err := db.query(`WHERE 1=1 ORDER BY added`)
	if err != nil {
		return stats, err
	}
	for _, v := range vids {
		switch {
		case strings.EqualFold(v.Site, "PornHub"):
			if v.UploaderID == "" && readSidecar != nil {
				if uid, cast, ok := readSidecar(v.Site, v.ID); ok {
					v.UploaderID, v.Cast = uid, cast
					_, _ = db.sql.Exec(`UPDATE videos SET uploader_id=?, cast=? WHERE site=? AND id=?`,
						uid, jsonArr(cast), v.Site, v.ID)
					stats["sidecar-filled"]++
				}
			}
		case strings.EqualFold(v.Site, "Twitter"):
			if h := XHandleFromURL(v.WebpageURL); h != "" && v.UploaderID == "" {
				v.UploaderID = h
				_, _ = db.sql.Exec(`UPDATE videos SET uploader_id=? WHERE site=? AND id=?`, h, v.Site, v.ID)
			}
		}
		before := stats["accounts"]
		db.upsertVideoAccounts(v)
		_ = before
		stats["videos-scanned"]++
	}

	// 3. Born-linking: connect download accounts to the person auto-created
	// from them, when there is exactly one unambiguous name match.
	accounts, err := db.Accounts()
	if err != nil {
		return stats, err
	}
	people, err := db.Models()
	if err != nil {
		return stats, err
	}
	for _, a := range accounts {
		if a.Person != "" {
			continue
		}
		var matches []string
		for _, p := range people {
			if p.Name == "" {
				continue
			}
			if strings.EqualFold(p.Name, a.DisplayName) || HandleSlug(p.Name) == a.Handle {
				matches = append(matches, p.Name)
			}
		}
		if len(matches) == 1 {
			_ = db.ConnectAccount(a.Platform, a.Handle, matches[0])
			stats["born-linked"]++
		} else if len(matches) > 1 {
			stats["ambiguous"]++
		}
	}
	if all, err := db.Accounts(); err == nil {
		stats["accounts-total"] = len(all)
	}
	return stats, nil
}

