// Package library is the catalogue: a SQLite-backed store of every downloaded
// video, with queries for the model list and per-model pages the viewer needs.
package library

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite" (no CGO)
)

// Video is one catalogue entry. JSON tags match the legacy library.json so we
// can migrate the existing collection straight in.
type Video struct {
	ID           string   `json:"id"`
	Site         string   `json:"site"`
	Title        string   `json:"title"`
	Uploader     string   `json:"uploader"`
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
}

// Model summarises one uploader for the model list.
type Model struct {
	Site         string `json:"site"`
	Uploader     string `json:"uploader"`
	Count        int    `json:"count"`
	TotalSeconds int    `json:"totalSeconds"`
	Bytes        int64  `json:"bytes"`
	Thumbnail    string `json:"thumbnail"` // representative cover
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
  PRIMARY KEY (site, id)
);
CREATE INDEX IF NOT EXISTS idx_videos_model ON videos(site, uploader);
CREATE INDEX IF NOT EXISTS idx_videos_added ON videos(added);`

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
	// Older DBs predate watched_at — add it if missing (ignore "duplicate column").
	_, _ = sqlDB.Exec(`ALTER TABLE videos ADD COLUMN watched_at TEXT`)
	db := &DB{sql: sqlDB, root: root}
	db.relativize() // convert any legacy absolute paths to relative (idempotent)
	return db, nil
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
	_, err := db.sql.Exec(`
INSERT INTO videos (id,site,title,uploader,duration,width,height,ext,filepath,filename,
  thumbnail,thumbnail_url,webpage_url,upload_date,view_count,like_count,tags,categories,
  description,filesize,added)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(site,id) DO UPDATE SET
  title=excluded.title, uploader=excluded.uploader, duration=excluded.duration,
  width=excluded.width, height=excluded.height, ext=excluded.ext, filepath=excluded.filepath,
  filename=excluded.filename, thumbnail=excluded.thumbnail, thumbnail_url=excluded.thumbnail_url,
  webpage_url=excluded.webpage_url, upload_date=excluded.upload_date, view_count=excluded.view_count,
  like_count=excluded.like_count, tags=excluded.tags, categories=excluded.categories,
  description=excluded.description, filesize=excluded.filesize, added=excluded.added`,
		v.ID, v.Site, v.Title, v.Uploader, ptr(v.Duration), ptr(v.Width), ptr(v.Height),
		v.Ext, db.rel(v.Filepath), v.Filename, db.rel(v.Thumbnail), v.ThumbnailURL, v.WebpageURL,
		v.UploadDate, ptr(v.ViewCount), ptr(v.LikeCount), jsonArr(v.Tags),
		jsonArr(v.Categories), v.Description, ptr(v.Filesize), v.Added)
	return err
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

// Models returns the model list, ordered by video count.
func (db *DB) Models() ([]Model, error) {
	rows, err := db.sql.Query(`
SELECT site, uploader, COUNT(*) AS c, COALESCE(SUM(duration),0), COALESCE(SUM(filesize),0),
       (SELECT thumbnail FROM videos v2
        WHERE v2.site=v.site AND v2.uploader=v.uploader AND thumbnail<>'' LIMIT 1)
FROM videos v GROUP BY site, uploader ORDER BY c DESC, uploader`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var thumb sql.NullString
		if err := rows.Scan(&m.Site, &m.Uploader, &m.Count, &m.TotalSeconds, &m.Bytes, &thumb); err != nil {
			return nil, err
		}
		m.Thumbnail = db.abs(thumb.String)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Stats returns the overall storage breakdown.
func (db *DB) Stats() (Stats, error) {
	var s Stats
	err := db.sql.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(filesize),0), COUNT(DISTINCT site||'/'||uploader) FROM videos`,
	).Scan(&s.VideoCount, &s.TotalBytes, &s.ModelCount)
	if err != nil {
		return s, err
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

const cols = `id,site,title,uploader,duration,width,height,ext,filepath,filename,` +
	`thumbnail,thumbnail_url,webpage_url,upload_date,view_count,like_count,tags,` +
	`categories,description,filesize,added,watched_at`

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
	if err := rows.Scan(&v.ID, &v.Site, &v.Title, &v.Uploader, &dur, &w, &h, &v.Ext,
		&v.Filepath, &v.Filename, &v.Thumbnail, &v.ThumbnailURL, &v.WebpageURL,
		&v.UploadDate, &vc, &lc, &tags, &cats, &v.Description, &fs, &v.Added, &watched); err != nil {
		return v, err
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

// Videos returns every video for one model, newest first.
func (db *DB) Videos(site, uploader string) ([]Video, error) {
	return db.query(`WHERE site=? AND uploader=? ORDER BY added DESC, id`, site, uploader)
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

// Search returns videos whose title or model matches a query (blank = all).
func (db *DB) Search(q string) ([]Video, error) {
	like := "%" + q + "%"
	return db.query(`WHERE title LIKE ? OR uploader LIKE ? ORDER BY added DESC LIMIT 500`, like, like)
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
