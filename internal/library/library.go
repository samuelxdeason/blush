// Package library is the catalogue: a SQLite-backed store of every downloaded
// video, with queries for the model list and per-model pages the viewer needs.
package library

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite" (no CGO)
)

// Video is one catalogue entry. JSON tags match the legacy library.json so we
// can migrate the existing collection straight in.
type Video struct {
	ID           string   `json:"id"`
	Site         string   `json:"site"`
	Title        string   `json:"title"`
	Uploader     string   `json:"uploader"`
	Models       []string `json:"models"` // editable grouping(s); empty = Unassigned
	Duration     *int     `json:"duration"`
	Width        *int     `json:"width"`
	Height       *int     `json:"height"`
	Ext          string   `json:"ext"`
	Filepath     string   `json:"filepath"`
	Filename     string   `json:"filename"`
	Thumbnail    string   `json:"thumbnail"`
	ThumbnailURL string   `json:"thumbnail_url"`
	WebpageURL   string   `json:"webpage_url"`
	UploadDate   string   `json:"upload_date"`
	ViewCount    *int     `json:"view_count"`
	LikeCount    *int     `json:"like_count"`
	Tags         []string `json:"tags"`
	Categories   []string `json:"categories"`
	Description  string   `json:"description"`
	Filesize     *int64   `json:"filesize"`
	Added        string   `json:"added"`
	WatchedAt    string   `json:"watched_at"`
	Favorite     bool     `json:"favorite"`
	Labels       []string `json:"labels"` // user categories/tags
}

// Model summarises one (editable) model grouping for the unified library.
type Model struct {
	Name         string `json:"name"`  // "" = Unassigned
	Count        int    `json:"count"`
	TotalSeconds int    `json:"totalSeconds"`
	Bytes        int64  `json:"bytes"`
	Sites        string `json:"sites"` // comma-joined distinct sources, e.g. "PornHub,Twitter"
	Thumbnail    string `json:"thumbnail"`
}

// ModelLink is a labelled URL on a model's profile (e.g. OnlyFans → …).
type ModelLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ModelInfo is a model's editable profile.
type ModelInfo struct {
	Name  string      `json:"name"`
	Bio   string      `json:"bio"`
	Links []ModelLink `json:"links"`
	Cover string      `json:"cover"` // absolute path to a chosen cover image
}

// Photo is an image attached to a model's page.
type Photo struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Filepath string `json:"filepath"`
	Filename string `json:"filename"`
	Added    string `json:"added"`
}

// SiteStat is the storage footprint of one source.
type SiteStat struct {
	Site  string `json:"site"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// Stats is the overall storage breakdown for the Settings page.
type Stats struct {
	TotalBytes int64      `json:"totalBytes"`
	VideoCount int        `json:"videoCount"`
	ModelCount int        `json:"modelCount"`
	Sites      []SiteStat `json:"sites"`
}

// DB wraps the SQLite catalogue. Paths are stored RELATIVE to root so the whole
// vault (db + media) can move to an external drive / different drive letter.
type DB struct {
	sql  *sql.DB
	root string
}

const schema = `
CREATE TABLE IF NOT EXISTS videos (
  id            TEXT NOT NULL,
  site          TEXT NOT NULL,
  title         TEXT,
  uploader      TEXT,
  model         TEXT,
  duration      INTEGER,
  width         INTEGER,
  height        INTEGER,
  ext           TEXT,
  filepath      TEXT,
  filename      TEXT,
  thumbnail     TEXT,
  thumbnail_url TEXT,
  webpage_url   TEXT,
  upload_date   TEXT,
  view_count    INTEGER,
  like_count    INTEGER,
  tags          TEXT,
  categories    TEXT,
  description   TEXT,
  filesize      INTEGER,
  added         TEXT,
  watched_at    TEXT,
  favorite      INTEGER DEFAULT 0,
  labels        TEXT,
  PRIMARY KEY (site, id)
);
CREATE INDEX IF NOT EXISTS idx_videos_model ON videos(site, uploader);
CREATE INDEX IF NOT EXISTS idx_videos_added ON videos(added);
CREATE TABLE IF NOT EXISTS photos (
  id        TEXT PRIMARY KEY,
  model     TEXT NOT NULL,
  filepath  TEXT,
  filename  TEXT,
  added     TEXT
);
CREATE INDEX IF NOT EXISTS idx_photos_model ON photos(model);
CREATE TABLE IF NOT EXISTS model_info (
  name    TEXT PRIMARY KEY,
  bio     TEXT,
  links   TEXT,
  cover   TEXT,
  updated TEXT
);`

// Open creates/opens the catalogue at path. root is the media root that stored
// relative paths resolve against.
func Open(path, root string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // single writer; avoids self-contention
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000", // wait, don't error, on a momentary lock
		"PRAGMA journal_mode=WAL",  // readers don't block the writer
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	if _, err := sqlDB.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Older DBs predate these columns — add if missing (ignore "duplicate column").
	_, _ = sqlDB.Exec(`ALTER TABLE videos ADD COLUMN watched_at TEXT`)
	_, _ = sqlDB.Exec(`ALTER TABLE videos ADD COLUMN model TEXT`)
	_, _ = sqlDB.Exec(`ALTER TABLE videos ADD COLUMN favorite INTEGER DEFAULT 0`)
	_, _ = sqlDB.Exec(`ALTER TABLE videos ADD COLUMN labels TEXT`)
	// Backfill: existing rows adopt their uploader as the model (one-time; only
	// touches rows where model is still NULL, i.e. right after the column is added).
	_, _ = sqlDB.Exec(`UPDATE videos SET model = uploader WHERE model IS NULL`)
	_, _ = sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_videos_modelname ON videos(model)`)
	db := &DB{sql: sqlDB, root: root}
	db.relativize()           // convert any legacy absolute paths to relative (idempotent)
	db.migrateModelsToArray() // single-value model -> JSON array (idempotent)
	return db, nil
}

// migrateModelsToArray wraps any legacy plain-string model in a JSON array, so
// a video can hold multiple models.
func (db *DB) migrateModelsToArray() {
	rows, err := db.sql.Query(`SELECT rowid, model FROM videos WHERE model IS NOT NULL AND model<>'' AND model NOT LIKE '[%'`)
	if err != nil {
		return
	}
	type item struct {
		rowid int64
		val   string
	}
	var items []item
	for rows.Next() {
		var r int64
		var m string
		if rows.Scan(&r, &m) == nil {
			items = append(items, item{r, m})
		}
	}
	rows.Close()
	for _, it := range items {
		b, _ := json.Marshal([]string{it.val})
		_, _ = db.sql.Exec(`UPDATE videos SET model=? WHERE rowid=?`, string(b), it.rowid)
	}
}

// parseModels reads the model column (JSON array, or legacy single value).
func parseModels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if json.Unmarshal([]byte(raw), &arr) == nil {
			return arr
		}
		return nil
	}
	return []string{raw}
}

func (db *DB) Close() error { return db.sql.Close() }

// abs resolves a stored (relative) path against the media root.
func (db *DB) abs(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(db.root, p)
}

// rel makes an absolute path relative to the media root for storage.
func (db *DB) rel(p string) string {
	if p == "" {
		return p
	}
	if r, err := filepath.Rel(db.root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// relativize rewrites any legacy absolute paths under root to relative form.
func (db *DB) relativize() {
	prefix := db.root + string(filepath.Separator)
	start := len(prefix) + 1 // SQLite substr is 1-based
	for _, col := range []string{"filepath", "thumbnail"} {
		_, _ = db.sql.Exec(
			`UPDATE videos SET `+col+` = substr(`+col+`, ?) WHERE `+col+` LIKE ?`,
			start, prefix+"%")
	}
}

func ptr[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func jsonArr(a []string) string {
	if a == nil {
		a = []string{}
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// Upsert inserts or replaces a video (keyed by site+id).
func (db *DB) Upsert(v Video) error {
	// favorite + labels are user data: set on first insert, never overwritten on
	// re-download (no entry in the ON CONFLICT SET).
	_, err := db.sql.Exec(`
INSERT INTO videos (id,site,title,uploader,model,duration,width,height,ext,filepath,filename,
  thumbnail,thumbnail_url,webpage_url,upload_date,view_count,like_count,tags,categories,
  description,filesize,added,favorite,labels)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(site,id) DO UPDATE SET
  title=excluded.title, uploader=excluded.uploader, duration=excluded.duration,
  width=excluded.width, height=excluded.height, ext=excluded.ext, filepath=excluded.filepath,
  filename=excluded.filename, thumbnail=excluded.thumbnail, thumbnail_url=excluded.thumbnail_url,
  webpage_url=excluded.webpage_url, upload_date=excluded.upload_date, view_count=excluded.view_count,
  like_count=excluded.like_count, tags=excluded.tags, categories=excluded.categories,
  description=excluded.description, filesize=excluded.filesize, added=excluded.added`,
		v.ID, v.Site, v.Title, v.Uploader, jsonArr(v.Models), ptr(v.Duration), ptr(v.Width), ptr(v.Height),
		v.Ext, db.rel(v.Filepath), v.Filename, db.rel(v.Thumbnail), v.ThumbnailURL, v.WebpageURL,
		v.UploadDate, ptr(v.ViewCount), ptr(v.LikeCount), jsonArr(v.Tags),
		jsonArr(v.Categories), v.Description, ptr(v.Filesize), v.Added, b2i(v.Favorite), jsonArr(v.Labels))
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetFavorite toggles/sets a video's like state.
func (db *DB) SetFavorite(site, id string, fav bool) error {
	_, err := db.sql.Exec(`UPDATE videos SET favorite=? WHERE site=? AND id=?`, b2i(fav), site, id)
	return err
}

// SetLabels replaces a video's user categories.
func (db *DB) SetLabels(site, id string, labels []string) error {
	_, err := db.sql.Exec(`UPDATE videos SET labels=? WHERE site=? AND id=?`, jsonArr(labels), site, id)
	return err
}

// AllLabels returns every distinct category in use, sorted (for autocomplete).
func (db *DB) AllLabels() ([]string, error) {
	rows, err := db.sql.Query(`SELECT labels FROM videos WHERE labels IS NOT NULL AND labels<>'' AND labels<>'[]'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var arr []string
		if json.Unmarshal([]byte(raw), &arr) == nil {
			for _, l := range arr {
				if l = strings.TrimSpace(l); l != "" {
					set[l] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// Favorites returns liked videos, newest first.
func (db *DB) Favorites() ([]Video, error) {
	return db.query(`WHERE favorite=1 ORDER BY added DESC LIMIT 2000`)
}

// LabelCount is a category and how many videos use it.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// LabelCounts returns every category with its video count (most-used first).
func (db *DB) LabelCounts() ([]LabelCount, error) {
	rows, err := db.sql.Query(`SELECT labels FROM videos WHERE labels IS NOT NULL AND labels<>'' AND labels<>'[]'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tally := map[string]int{}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var arr []string
		if json.Unmarshal([]byte(raw), &arr) == nil {
			for _, l := range arr {
				if l = strings.TrimSpace(l); l != "" {
					tally[l]++
				}
			}
		}
	}
	out := make([]LabelCount, 0, len(tally))
	for l, c := range tally {
		out = append(out, LabelCount{l, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out, rows.Err()
}

// VideosByLabel returns videos tagged with a category, newest first.
func (db *DB) VideosByLabel(label string) ([]Video, error) {
	b, _ := json.Marshal(label) // match the quoted token inside the JSON array
	return db.query(`WHERE labels LIKE ? ORDER BY added DESC LIMIT 2000`, "%"+string(b)+"%")
}

// ---- model profiles ----------------------------------------------------

// GetModelInfo returns a model's profile (empty if none saved yet).
func (db *DB) GetModelInfo(name string) (ModelInfo, error) {
	info := ModelInfo{Name: name, Links: []ModelLink{}}
	var bio, links, cover sql.NullString
	err := db.sql.QueryRow(`SELECT bio,links,cover FROM model_info WHERE name=?`, name).Scan(&bio, &links, &cover)
	if err == sql.ErrNoRows {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	info.Bio = bio.String
	if links.String != "" {
		_ = json.Unmarshal([]byte(links.String), &info.Links)
	}
	info.Cover = db.abs(cover.String)
	return info, nil
}

// SaveModelInfo upserts bio + links (preserves any existing cover).
func (db *DB) SaveModelInfo(name, bio string, links []ModelLink) error {
	_, _ = db.sql.Exec(`INSERT OR IGNORE INTO model_info(name) VALUES(?)`, name)
	b, _ := json.Marshal(links)
	_, err := db.sql.Exec(`UPDATE model_info SET bio=?, links=?, updated=? WHERE name=?`,
		bio, string(b), time.Now().Format("2006-01-02 15:04:05"), name)
	return err
}

// SetModelCover sets a model's cover image (preserves bio + links).
func (db *DB) SetModelCover(name, coverAbs string) error {
	_, _ = db.sql.Exec(`INSERT OR IGNORE INTO model_info(name) VALUES(?)`, name)
	_, err := db.sql.Exec(`UPDATE model_info SET cover=?, updated=? WHERE name=?`,
		db.rel(coverAbs), time.Now().Format("2006-01-02 15:04:05"), name)
	return err
}

// SetModels reassigns a video's model set (empty = Unassigned).
func (db *DB) SetModels(site, id string, models []string) error {
	_, err := db.sql.Exec(`UPDATE videos SET model=? WHERE site=? AND id=?`, jsonArr(models), site, id)
	return err
}

// SetTitle renames a video.
func (db *DB) SetTitle(site, id, title string) error {
	_, err := db.sql.Exec(`UPDATE videos SET title=? WHERE site=? AND id=?`, title, site, id)
	return err
}

// AddPhoto inserts/updates a photo (paths stored relative to root).
func (db *DB) AddPhoto(p Photo) error {
	_, err := db.sql.Exec(`
INSERT INTO photos (id,model,filepath,filename,added) VALUES (?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET model=excluded.model, filepath=excluded.filepath,
  filename=excluded.filename`,
		p.ID, p.Model, db.rel(p.Filepath), p.Filename, p.Added)
	return err
}

// PhotosByModel returns a model's photos, newest first (paths absolute).
func (db *DB) PhotosByModel(model string) ([]Photo, error) {
	rows, err := db.sql.Query(
		`SELECT id,model,filepath,filename,added FROM photos WHERE COALESCE(model,'')=? ORDER BY added DESC`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Photo
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.ID, &p.Model, &p.Filepath, &p.Filename, &p.Added); err != nil {
			return nil, err
		}
		p.Filepath = db.abs(p.Filepath)
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkWatched stamps a video as watched now (for the Recently Watched view).
func (db *DB) MarkWatched(site, id, when string) error {
	_, err := db.sql.Exec(`UPDATE videos SET watched_at=? WHERE site=? AND id=?`, when, site, id)
	return err
}

// Count returns the number of catalogued videos.
func (db *DB) Count() (int, error) {
	var n int
	err := db.sql.QueryRow(`SELECT COUNT(*) FROM videos`).Scan(&n)
	return n, err
}

// Models returns the unified model list, aggregated across each video's model
// set (a video can belong to several). Ordered by video count; "" = Unassigned.
func (db *DB) Models() ([]Model, error) {
	rows, err := db.sql.Query(`SELECT COALESCE(model,''), site, COALESCE(duration,0), COALESCE(filesize,0), COALESCE(thumbnail,'') FROM videos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		count    int
		seconds  int
		bytes    int64
		sites    map[string]bool
		thumb    string
	}
	acc := map[string]*agg{}
	order := []string{}
	bump := func(name, site string, dur int, sz int64, thumb string) {
		a := acc[name]
		if a == nil {
			a = &agg{sites: map[string]bool{}}
			acc[name] = a
			order = append(order, name)
		}
		a.count++
		a.seconds += dur
		a.bytes += sz
		if site != "" {
			a.sites[site] = true
		}
		if a.thumb == "" && thumb != "" {
			a.thumb = thumb
		}
	}
	for rows.Next() {
		var modelRaw, site, thumb string
		var dur int
		var sz int64
		if err := rows.Scan(&modelRaw, &site, &dur, &sz, &thumb); err != nil {
			return nil, err
		}
		ms := parseModels(modelRaw)
		if len(ms) == 0 {
			bump("", site, dur, sz, thumb) // Unassigned
			continue
		}
		for _, m := range ms {
			bump(m, site, dur, sz, thumb)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// cover overrides
	covers := map[string]string{}
	if crows, e := db.sql.Query(`SELECT name, COALESCE(cover,'') FROM model_info`); e == nil {
		for crows.Next() {
			var n, c string
			if crows.Scan(&n, &c) == nil && c != "" {
				covers[n] = c
			}
		}
		crows.Close()
	}

	out := make([]Model, 0, len(order))
	for _, name := range order {
		a := acc[name]
		sites := make([]string, 0, len(a.sites))
		for s := range a.sites {
			sites = append(sites, s)
		}
		sort.Strings(sites)
		thumb := a.thumb
		if c, ok := covers[name]; ok {
			thumb = c
		}
		out = append(out, Model{
			Name: name, Count: a.count, TotalSeconds: a.seconds, Bytes: a.bytes,
			Sites: strings.Join(sites, ","), Thumbnail: db.abs(thumb),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// Stats returns the overall storage breakdown.
func (db *DB) Stats() (Stats, error) {
	var s Stats
	err := db.sql.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(filesize),0) FROM videos`,
	).Scan(&s.VideoCount, &s.TotalBytes)
	if err != nil {
		return s, err
	}
	if ms, mErr := db.Models(); mErr == nil {
		s.ModelCount = len(ms)
	}
	rows, err := db.sql.Query(
		`SELECT site, COUNT(*), COALESCE(SUM(filesize),0) FROM videos GROUP BY site ORDER BY SUM(filesize) DESC`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var ss SiteStat
		if err := rows.Scan(&ss.Site, &ss.Count, &ss.Bytes); err != nil {
			return s, err
		}
		s.Sites = append(s.Sites, ss)
	}
	return s, rows.Err()
}

const cols = `id,site,title,uploader,model,duration,width,height,ext,filepath,filename,` +
	`thumbnail,thumbnail_url,webpage_url,upload_date,view_count,like_count,tags,` +
	`categories,description,filesize,added,watched_at,favorite,labels`

func nint(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func scanVideo(rows *sql.Rows) (Video, error) {
	var v Video
	var dur, w, h, vc, lc, fs sql.NullInt64
	var tags, cats string
	var watched sql.NullString
	var model, labels sql.NullString
	var fav sql.NullInt64
	if err := rows.Scan(&v.ID, &v.Site, &v.Title, &v.Uploader, &model, &dur, &w, &h, &v.Ext,
		&v.Filepath, &v.Filename, &v.Thumbnail, &v.ThumbnailURL, &v.WebpageURL,
		&v.UploadDate, &vc, &lc, &tags, &cats, &v.Description, &fs, &v.Added, &watched, &fav, &labels); err != nil {
		return v, err
	}
	v.Models = parseModels(model.String)
	v.Favorite = fav.Int64 == 1
	if labels.String != "" {
		_ = json.Unmarshal([]byte(labels.String), &v.Labels)
	}
	v.Duration, v.Width, v.Height, v.ViewCount, v.LikeCount = nint(dur), nint(w), nint(h), nint(vc), nint(lc)
	if fs.Valid {
		v.Filesize = &fs.Int64
	}
	v.WatchedAt = watched.String
	_ = json.Unmarshal([]byte(tags), &v.Tags)
	_ = json.Unmarshal([]byte(cats), &v.Categories)
	return v, nil
}

func (db *DB) query(where string, args ...any) ([]Video, error) {
	rows, err := db.sql.Query(`SELECT `+cols+` FROM videos `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Video
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		v.Filepath = db.abs(v.Filepath)   // resolve stored relative paths
		v.Thumbnail = db.abs(v.Thumbnail) // to absolute for the UI
		out = append(out, v)
	}
	return out, rows.Err()
}

// VideosByModel returns every video that includes a model ("" = Unassigned).
func (db *DB) VideosByModel(name string) ([]Video, error) {
	if name == "" {
		return db.query(`WHERE model IS NULL OR model='' OR model='[]' ORDER BY added DESC, id`)
	}
	b, _ := json.Marshal(name) // match the quoted token in the JSON array
	return db.query(`WHERE model LIKE ? ORDER BY added DESC, id`, "%"+string(b)+"%")
}

// VideosBySite returns every video for one source, newest first (the flat feed).
func (db *DB) VideosBySite(site string) ([]Video, error) {
	return db.query(`WHERE site=? ORDER BY added DESC, id LIMIT 2000`, site)
}

// RecentlyDownloaded returns the newest additions across all sources.
func (db *DB) RecentlyDownloaded(limit int) ([]Video, error) {
	return db.query(`ORDER BY added DESC LIMIT ?`, limit)
}

// RecentlyWatched returns videos most recently opened in the player.
func (db *DB) RecentlyWatched(limit int) ([]Video, error) {
	return db.query(`WHERE watched_at IS NOT NULL AND watched_at<>'' ORDER BY watched_at DESC LIMIT ?`, limit)
}

// Search returns videos whose title, model, or uploader matches (blank = all).
func (db *DB) Search(q string) ([]Video, error) {
	like := "%" + q + "%"
	return db.query(`WHERE title LIKE ? OR model LIKE ? OR uploader LIKE ? ORDER BY added DESC LIMIT 500`, like, like, like)
}

// MigrateFromJSON imports a legacy library.json file, returning rows imported.
func (db *DB) MigrateFromJSON(jsonPath string) (int, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return 0, err
	}
	var vids []Video
	if err := json.Unmarshal(data, &vids); err != nil {
		return 0, fmt.Errorf("parse %s: %w", jsonPath, err)
	}
	n := 0
	for _, v := range vids {
		if err := db.Upsert(v); err == nil {
			n++ // skip the occasional bad row rather than abandoning the rest
		}
	}
	return n, nil
}
