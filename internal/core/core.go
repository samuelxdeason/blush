// Package core is the host-independent engine of Media Vault: the catalogue,
// the download queue, media serving, and config. It has no dependency on Wails
// or HTTP, so it can be driven by the desktop app, the headless daemon, or tests.
package core

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"media-vault/internal/downloader"
	"media-vault/internal/library"
)

// Core holds the live engine: the catalogue DB and the download queue. Hosts
// (desktop / daemon) inject an emit func that delivers events to their clients.
type Core struct {
	db        *library.DB
	dl        *downloader.Downloader
	mediaRoot string
	stateDir  string // .keepsake: catalogue, archive, cookies, caches (separate from media)
	emit      func(event string, data any)

	ogCache map[string]string
	ogMu    sync.Mutex
}

// New opens the catalogue at mediaRoot, starts the downloader, and runs the
// one-time library.json migration. emit may be nil (events are then dropped).
func New(mediaRoot string, emit func(event string, data any)) (*Core, error) {
	if emit == nil {
		emit = func(string, any) {}
	}
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		return nil, err
	}
	// Move legacy state files (db/archive/cookies/cache) out of the media root and
	// into .keepsake so the vault root holds only media. One-time + idempotent.
	migrateState(mediaRoot)
	sdir := stateDir(mediaRoot)

	// Pick the catalogue: prefer the migrated .keepsake copy, but fall back to a
	// root-level db that couldn't be moved (e.g. still open by another instance)
	// so we never fork a second, divergent catalogue. Paths stay RELATIVE to
	// mediaRoot, so the db location doesn't affect how media resolves.
	dbPath, freshInstall := resolveDBPath(mediaRoot, sdir)
	db, err := library.Open(dbPath, mediaRoot)
	if err != nil {
		return nil, err
	}

	c := &Core{db: db, mediaRoot: mediaRoot, stateDir: sdir, emit: emit, ogCache: map[string]string{}}

	// First run: import a legacy library.json — but ONLY on a truly fresh install
	// (no catalogue at either location). Importing it over a fresh-but-not-first
	// db would resurrect stale paths from before the vault was last moved.
	if freshInstall {
		if n, _ := db.Count(); n == 0 {
			jsonPath := filepath.Join(mediaRoot, "library.json")
			if _, statErr := os.Stat(jsonPath); statErr == nil {
				_, _ = db.MigrateFromJSON(jsonPath)
			}
		}
	}

	c.dl = downloader.New(downloader.Config{
		YtDlp:     ytdlpPath(),
		FfmpegDir: ffmpegDir(),
		MediaRoot: mediaRoot,
		StateDir:  sdir,
		Archive:   filepath.Join(sdir, "downloaded.archive"),
	}, db, emit)

	// Auto-use a cookies.txt sitting in the vault.
	if _, err := os.Stat(c.cookiesPath()); err == nil {
		c.dl.SetCookieSpec("file:" + c.cookiesPath())
	}
	return c, nil
}

// Close releases the catalogue.
func (c *Core) Close() error { return c.db.Close() }

// MediaRoot is the current vault location.
func (c *Core) MediaRoot() string { return c.mediaRoot }

// ---- catalogue (read) --------------------------------------------------

func (c *Core) Models() ([]library.Model, error)                    { return c.db.Models() }
func (c *Core) VideosByModel(model string) ([]library.Video, error) { return c.db.VideosByModel(model) }
func (c *Core) VideosBySite(site string) ([]library.Video, error)   { return c.db.VideosBySite(site) }
func (c *Core) Search(q string) ([]library.Video, error)            { return c.db.Search(q) }
func (c *Core) AllVideos(limit, offset int, sort, site string, fav bool) ([]library.Video, error) {
	return c.db.AllVideos(limit, offset, sort, site, fav)
}
func (c *Core) RecentlyDownloaded() ([]library.Video, error)        { return c.db.RecentlyDownloaded(200) }
func (c *Core) RecentlyWatched() ([]library.Video, error)           { return c.db.RecentlyWatched(200) }
func (c *Core) Favorites() ([]library.Video, error)                 { return c.db.Favorites() }
func (c *Core) AllLabels() ([]string, error)                        { return c.db.AllLabels() }
func (c *Core) LabelCounts() ([]library.LabelCount, error)          { return c.db.LabelCounts() }
func (c *Core) VideosByLabel(label string) ([]library.Video, error) { return c.db.VideosByLabel(label) }
func (c *Core) PhotosByModel(model string) ([]library.Photo, error) { return c.db.PhotosByModel(model) }
func (c *Core) Stats() (library.Stats, error)                       { return c.db.Stats() }

func (c *Core) GetModelInfo(name string) (library.ModelInfo, error) { return c.db.GetModelInfo(name) }

// ---- catalogue (write) -------------------------------------------------

func (c *Core) SetModels(site, id string, models []string) error { return c.db.SetModels(site, id, models) }
func (c *Core) RemoveModelFromAll(name string) error             { return c.db.RemoveModelFromAll(name) }
func (c *Core) SetTitle(site, id, title string) error            { return c.db.SetTitle(site, id, title) }
func (c *Core) SetFavorite(site, id string, fav bool) error      { return c.db.SetFavorite(site, id, fav) }
func (c *Core) SetLabels(site, id string, labels []string) error { return c.db.SetLabels(site, id, labels) }
func (c *Core) SetModelCover(name, cover string) error           { return c.db.SetModelCover(name, cover) }

func (c *Core) SaveModelInfo(name, bio string, links []library.ModelLink) error {
	return c.db.SaveModelInfo(name, bio, links)
}

func (c *Core) MarkWatched(site, id string) {
	_ = c.db.MarkWatched(site, id, time.Now().Format("2006-01-02 15:04:05"))
}

func (c *Core) SetPosition(site, id string, position, duration float64) error {
	return c.db.SetPosition(site, id, position, duration)
}
func (c *Core) ContinueWatching() ([]library.Video, error) { return c.db.ContinueWatching(60) }

// ---- collections -------------------------------------------------------

func (c *Core) Collections() ([]library.Collection, error)        { return c.db.Collections() }
func (c *Core) RenameCollection(id int64, name string) error      { return c.db.RenameCollection(id, name) }
func (c *Core) DeleteCollection(id int64) error                   { return c.db.DeleteCollection(id) }
func (c *Core) VideosByCollection(id int64) ([]library.Video, error) { return c.db.VideosByCollection(id) }

func (c *Core) CreateCollection(name string, hidden bool) (int64, error) {
	return c.db.CreateCollection(name, hidden)
}
func (c *Core) SetCollectionHidden(id int64, hidden bool) error { return c.db.SetCollectionHidden(id, hidden) }
func (c *Core) SetCollectionLocked(id int64, locked bool) error { return c.db.SetCollectionLocked(id, locked) }
func (c *Core) AddToCollection(id int64, site, videoID string) error {
	return c.db.AddToCollection(id, site, videoID)
}
func (c *Core) RemoveFromCollection(id int64, site, videoID string) error {
	return c.db.RemoveFromCollection(id, site, videoID)
}
func (c *Core) CollectionsForVideo(site, videoID string) ([]int64, error) {
	return c.db.CollectionsForVideo(site, videoID)
}

// ---- downloads ---------------------------------------------------------

func (c *Core) Enqueue(url string) string        { return c.dl.Enqueue(url) }
func (c *Core) EnqueueMany(urls []string) int    { return c.dl.EnqueueMany(urls) }
func (c *Core) SyncedURLs() []string             { return c.dl.SyncedURLs() }
func (c *Core) SyncedLists() []downloader.SyncSummary { return c.dl.SyncedLists() }
func (c *Core) RemoveSync(url string)            { c.dl.RemoveSync(url) }
func (c *Core) Enumerate(url string, refresh bool) ([]downloader.RemoteItem, error) {
	return c.dl.Enumerate(url, refresh)
}
func (c *Core) Queue() []downloader.Job                              { return c.dl.Snapshot() }
func (c *Core) RemoveJob(id string)                                  { c.dl.RemoveJob(id) }
func (c *Core) ClearFinished()                                       { c.dl.ClearFinished() }
func (c *Core) SetCookieSpec(spec string)                            { c.dl.SetCookieSpec(spec) }

// Import copies local files/folders into the library under model.
func (c *Core) Import(paths []string, model string) { c.dl.Import(paths, model) }

// BackupCatalogue checkpoints and copies the catalogue db to .keepsake/backups
// with a timestamped name, returning the backup path. Your media isn't touched.
func (c *Core) BackupCatalogue() (string, error) {
	_ = c.db.Checkpoint()
	dir := filepath.Join(c.stateDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "library-"+time.Now().Format("20060102-150405")+".db")
	if err := copyFileCore(filepath.Join(c.stateDir, "library.db"), dst); err != nil {
		return "", err
	}
	return dst, nil
}

func copyFileCore(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// RebuildFromDisk re-catalogues the vault by scanning media + .info.json sidecars.
// It restores the library if the DB is lost and re-points filepaths after files
// are moved, without discarding user data (models/favorites/labels survive).
func (c *Core) RebuildFromDisk() (int, error) { return c.dl.RebuildFromDisk() }

// ---- tool discovery ----------------------------------------------------

func ytdlpPath() string {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	for _, c := range []string{
		filepath.Join(dir, "yt-dlp.exe"),
		filepath.Join(dir, "yt-dlp"),
		filepath.Join(dir, "resources", "yt-dlp.exe"),
		filepath.Join("resources", "yt-dlp.exe"),
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p
	}
	return "yt-dlp"
}

func ffmpegDir() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return filepath.Dir(p)
	}
	pattern := filepath.Join(os.Getenv("LOCALAPPDATA"),
		`Microsoft\WinGet\Packages`, "*FFmpeg*", "*", "bin", "ffmpeg.exe")
	if m, _ := filepath.Glob(pattern); len(m) > 0 {
		return filepath.Dir(m[0])
	}
	return ""
}

// ---- config ------------------------------------------------------------

const defaultRootName = "MediaVault"

// stateDirName is the hidden subfolder holding app state (catalogue, archive,
// cookies, caches) so the vault root contains only media folders.
const stateDirName = ".keepsake"

func stateDir(mediaRoot string) string { return filepath.Join(mediaRoot, stateDirName) }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// resolveDBPath returns the catalogue path to open and whether this is a fresh
// install. It prefers the migrated .keepsake copy; if the db is still at the
// root (migrateState couldn't move it because another instance holds it open),
// it opens that same file in place rather than creating a divergent second
// catalogue. freshInstall is true only when no db exists at either location.
func resolveDBPath(mediaRoot, sdir string) (path string, freshInstall bool) {
	newp := filepath.Join(sdir, "library.db")
	oldp := filepath.Join(mediaRoot, "library.db")
	switch {
	case fileExists(newp):
		return newp, false
	case fileExists(oldp):
		return oldp, false // locked/un-moved — use it where it is
	default:
		return newp, true
	}
}

// migrateState moves the state files that used to sit in the media root into
// .keepsake. It runs once: on later launches the files are already there and
// it's a no-op. Media files are never touched. WAL/SHM sidecars move with the
// db so SQLite can still recover an unflushed write-ahead log.
func migrateState(mediaRoot string) {
	dir := stateDir(mediaRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	hideDir(dir)
	for _, name := range []string{
		"library.db", "library.db-wal", "library.db-shm",
		"downloaded.archive", "cookies.txt", "sync_cache.json",
	} {
		newp := filepath.Join(dir, name)
		if _, err := os.Stat(newp); err == nil {
			continue // already migrated — don't clobber
		}
		oldp := filepath.Join(mediaRoot, name)
		if _, err := os.Stat(oldp); err == nil {
			_ = os.Rename(oldp, newp)
		}
	}
}

type appConfig struct {
	MediaRoot string `json:"mediaRoot"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "MediaVault", "config.json")
}

// ResolveRoot picks the media root: explicit override wins, then $MEDIAVAULT_ROOT,
// then the saved config, then a per-OS default under the user's home.
func ResolveRoot(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if env := strings.TrimSpace(os.Getenv("MEDIAVAULT_ROOT")); env != "" {
		return env
	}
	var c appConfig
	if data, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if strings.TrimSpace(c.MediaRoot) != "" {
		return c.MediaRoot
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, defaultRootName)
}

// SaveRoot persists the chosen media root (takes effect next launch).
func SaveRoot(root string) error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(appConfig{MediaRoot: root}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0o644)
}
