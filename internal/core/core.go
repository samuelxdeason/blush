// Package core is the host-independent engine of Trove: the catalogue,
// the download queue, media serving, and config. It has no dependency on Wails
// or HTTP, so it can be driven by the desktop app, the headless daemon, or tests.
package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"trove/internal/downloader"
	"trove/internal/library"
)

// Core holds the live engine: the catalogue DB and the download queue. Hosts
// (desktop / daemon) inject an emit func that delivers events to their clients.
type Core struct {
	db        *library.DB
	dl        *downloader.Downloader
	mediaRoot string
	stateDir  string // .trove: catalogue, archive, cookies, caches (separate from media)
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
	// into .trove so the vault root holds only media. One-time + idempotent.
	migrateState(mediaRoot)
	sdir := stateDir(mediaRoot)

	// Pick the catalogue: prefer the migrated .trove copy, but fall back to a
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
func (c *Core) AllVideos(limit, offset int, sort, site string, fav bool, seed int64) ([]library.Video, error) {
	return c.db.AllVideos(limit, offset, sort, site, fav, seed)
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

func (c *Core) SaveModelInfo(name, nickname, bio string, links []library.ModelLink) error {
	return c.db.SaveModelInfo(name, nickname, bio, links)
}

// RenameModel renames a person across videos, photos, and their profile.
func (c *Core) RenameModel(from, to string) error { return c.db.RenameModel(from, to) }

// AccountMatch is one verified platform account on a person's profile, with
// how many Unsorted videos from that account are waiting to be claimed.
type AccountMatch struct {
	Platform      string `json:"platform"`
	Handle        string `json:"handle"`
	UnsortedCount int    `json:"unsortedCount"`
}

// AccountMatches scans a person's profile links for platform accounts and
// counts the Unsorted videos downloaded from each.
func (c *Core) AccountMatches(name string) ([]AccountMatch, error) {
	info, err := c.db.GetModelInfo(name)
	if err != nil {
		return nil, err
	}
	seen := map[library.Account]bool{}
	out := []AccountMatch{}
	for _, l := range info.Links {
		a, ok := library.AccountFromURL(l.URL)
		if !ok || seen[a] {
			continue
		}
		seen[a] = true
		vids, err := c.db.UnsortedFromAccount(a)
		if err != nil {
			continue
		}
		out = append(out, AccountMatch{Platform: a.Platform, Handle: a.Handle, UnsortedCount: len(vids)})
	}
	return out, nil
}

// ClaimAccount assigns person to every Unsorted video from the account.
func (c *Core) ClaimAccount(name, platform, handle string) (int, error) {
	return c.db.AssignAccount(library.Account{Platform: platform, Handle: handle}, name)
}

// ---- appears-in (featured) ---------------------------------------------

func (c *Core) SetFeatured(site, id string, people []string) error {
	return c.db.SetFeatured(site, id, people)
}
func (c *Core) VideosFeaturing(name string) ([]library.Video, error) {
	return c.db.VideosFeaturing(name)
}
func (c *Core) AddFeatured(site, id, person string) error { return c.db.AddFeatured(site, id, person) }

// ---- platform accounts --------------------------------------------------

func (c *Core) Accounts() ([]library.AccountInfo, error) { return c.db.Accounts() }
func (c *Core) AccountsForPerson(name string) ([]library.AccountInfo, error) {
	return c.db.AccountsForPerson(name)
}
func (c *Core) ConnectAccount(platform, handle, person string) error {
	return c.db.ConnectAccount(platform, handle, person)
}

// AccountWithCount pairs an account with how many videos it owns.
type AccountWithCount struct {
	library.AccountInfo
	VideoCount int `json:"videoCount"`
}

// AccountsWithCounts lists every account with its owned-video tally.
func (c *Core) AccountsWithCounts() ([]AccountWithCount, error) {
	accts, err := c.db.Accounts()
	if err != nil {
		return nil, err
	}
	counts, err := c.db.AccountVideoCounts()
	if err != nil {
		return nil, err
	}
	out := make([]AccountWithCount, 0, len(accts))
	for _, a := range accts {
		out = append(out, AccountWithCount{AccountInfo: a,
			VideoCount: counts[library.Account{Platform: a.Platform, Handle: a.Handle}]})
	}
	return out, nil
}

// VideosUploadedBy / VideosSavedBy split a person's videos into what their
// connected accounts own vs what was deliberately saved to them.
func (c *Core) VideosUploadedBy(name string) ([]library.Video, error) {
	return c.db.VideosUploadedBy(name)
}
func (c *Core) VideosSavedBy(name string) ([]library.Video, error) { return c.db.VideosSavedBy(name) }

// CreateAccount defines an account by hand (no downloads needed) and
// optionally connects it to a person. A profile URL alone is enough — the
// platform and handle are recognised from it.
func (c *Core) CreateAccount(a library.AccountInfo) error {
	if a.Handle == "" && a.URL != "" {
		if parsed, ok := library.AccountFromURL(a.URL); ok {
			a.Platform, a.Handle = parsed.Platform, parsed.Handle
		}
	}
	a.Source = "manual"
	if err := c.db.UpsertAccount(a); err != nil {
		return err
	}
	if a.Person != "" {
		return c.db.ConnectAccount(a.Platform, a.Handle, a.Person)
	}
	return nil
}

// AdoptAccount gives an account a person parent in one step: connect it,
// make sure the person exists (even with no media yet), claim the Unsorted
// videos the account owns, and re-run cast resolution so appears-in fills in.
func (c *Core) AdoptAccount(platform, handle, person string) (map[string]int, error) {
	person = strings.TrimSpace(person)
	handle = strings.ToLower(strings.TrimSpace(handle))
	if person == "" || handle == "" || platform == "" {
		return nil, fmt.Errorf("platform, handle and person are all required")
	}
	_ = c.db.UpsertAccount(library.AccountInfo{Platform: platform, Handle: handle, Source: "manual"})
	if err := c.db.ConnectAccount(platform, handle, person); err != nil {
		return nil, err
	}
	info, _ := c.db.GetModelInfo(person)
	_ = c.db.SaveModelInfo(person, info.Nickname, info.Bio, info.Links) // person exists even with zero media
	claimed, _ := c.db.AssignAccount(library.Account{Platform: platform, Handle: handle}, person)
	stats, err := c.db.ApplyReinterpretPlan()
	if err != nil {
		stats = map[string]int{}
	}
	stats["claimed"] = claimed
	return stats, nil
}

// BackfillAccounts rebuilds the accounts table from trusted links, sidecar
// metadata, and video URLs. Idempotent; safe to re-run.
func (c *Core) BackfillAccounts() (map[string]int, error) {
	return c.db.BackfillAccounts(func(site, id string) (string, []string, bool) {
		data, err := os.ReadFile(filepath.Join(c.stateDir, library.MetaDirName, library.FlatBase(site, id)+".info.json"))
		if err != nil {
			return "", nil, false
		}
		var meta struct {
			UploaderID string   `json:"uploader_id"`
			Cast       []string `json:"cast"`
		}
		if json.Unmarshal(data, &meta) != nil {
			return "", nil, false
		}
		return meta.UploaderID, meta.Cast, true
	})
}

// Reinterpretation: propose/apply the accounts-based re-read of every manual
// person assignment (see library.BuildReinterpretPlan for the rules).
func (c *Core) ReinterpretPlan() (library.ReinterpretPlan, error) { return c.db.BuildReinterpretPlan() }
func (c *Core) ReinterpretApply() (map[string]int, error)         { return c.db.ApplyReinterpretPlan() }
func (c *Core) ConfirmSaved(site, id, person string) error        { return c.db.ConfirmSaved(site, id, person) }
func (c *Core) DemoteToFeatured(site, id, person string) error    { return c.db.DemoteToFeatured(site, id, person) }

// CastSuggestions finds videos whose downloaded metadata (the .info.json
// sidecars) lists the person in the cast, but which aren't yet linked to them
// — candidates for their "Appears in" section. Suggestion only; nothing is
// written until the user accepts.
func (c *Core) CastSuggestions(name string) ([]library.Video, error) {
	info, _ := c.db.GetModelInfo(name)
	aliases := map[string]bool{strings.ToLower(strings.TrimSpace(name)): true}
	if n := strings.ToLower(strings.TrimSpace(info.Nickname)); n != "" {
		aliases[n] = true
	}
	vids, err := c.db.AllVideos(50000, 0, "newest", "", false, 0)
	if err != nil {
		return nil, err
	}
	var out []library.Video
	for _, v := range vids {
		if hasPersonFold(v.Models, name) || hasPersonFold(v.Featured, name) {
			continue
		}
		metaPath := filepath.Join(c.stateDir, library.MetaDirName, library.FlatBase(v.Site, v.ID)+".info.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta struct {
			Cast []string `json:"cast"`
		}
		if json.Unmarshal(data, &meta) != nil {
			continue
		}
		for _, member := range meta.Cast {
			if aliases[strings.ToLower(strings.TrimSpace(member))] {
				out = append(out, v)
				break
			}
		}
		if len(out) >= 200 {
			break
		}
	}
	return out, nil
}

func hasPersonFold(list []string, name string) bool {
	for _, x := range list {
		if strings.EqualFold(x, name) {
			return true
		}
	}
	return false
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

// ImportPhotosFromURL downloads a web gallery's photos into model's album
// (async; progress on the "import" event stream).
func (c *Core) ImportPhotosFromURL(url, model, album string) {
	c.dl.ImportPhotosFromURL(url, model, album)
}

// BackupCatalogue checkpoints and copies the catalogue db to .trove/backups
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

// ---- ingestion (for the external fetcher) -------------------------------

// UpsertVideo catalogues a video that an external tool (trove-fetch) has
// already placed in the vault. Paths may be absolute; they are stored
// relative to the media root.
func (c *Core) UpsertVideo(v library.Video) error {
	err := c.db.Upsert(v)
	if err == nil {
		c.emit("library", map[string]any{"site": v.Site, "id": v.ID})
	}
	return err
}

// AddPhoto catalogues a photo an external tool has placed in the vault.
func (c *Core) AddPhoto(p library.Photo) error { return c.db.AddPhoto(p) }

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

const defaultRootName = "Trove"

// stateDirName is the hidden subfolder holding app state (catalogue, archive,
// cookies, caches) so the vault root contains only media folders.
const stateDirName = ".trove"

// legacyStateDirNames are the state folder's pre-rename names (Keepsake and
// blush.xxx eras); migrateState renames the first one found in place on the
// first launch after the update.
var legacyStateDirNames = []string{".xxx", ".keepsake"}

func stateDir(mediaRoot string) string { return filepath.Join(mediaRoot, stateDirName) }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// resolveDBPath returns the catalogue path to open and whether this is a fresh
// install. It prefers the migrated .trove copy; if the db is still at the
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
// .trove. It runs once: on later launches the files are already there and
// it's a no-op. Media files are never touched. WAL/SHM sidecars move with the
// db so SQLite can still recover an unflushed write-ahead log.
func migrateState(mediaRoot string) {
	dir := stateDir(mediaRoot)
	// One-time: the state dir has been renamed with the app (.keepsake -> .xxx
	// -> .trove). Rename it wholesale (db, wal, archive, meta/, thumbs/ all come
	// along); stored legacy-prefix paths are rewritten by library.Open.
	if _, err := os.Stat(dir); err != nil {
		for _, legacy := range legacyStateDirNames {
			if oldDir := filepath.Join(mediaRoot, legacy); fileExists(oldDir) {
				_ = os.Rename(oldDir, dir)
				break
			}
		}
	}
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
	return filepath.Join(dir, "Trove", "config.json")
}

// legacyConfigPath is where the pre-rename app (MediaVault/blush era) kept its
// config; ResolveRoot falls back to it until a SaveRoot writes the new file.
func legacyConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "MediaVault", "config.json")
}

// ResolveRoot picks the media root: explicit override wins, then $TROVE_ROOT
// (or the legacy $MEDIAVAULT_ROOT), then the saved config, then a per-OS
// default under the user's home.
func ResolveRoot(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	for _, key := range []string{"TROVE_ROOT", "MEDIAVAULT_ROOT"} {
		if env := strings.TrimSpace(os.Getenv(key)); env != "" {
			return env
		}
	}
	var c appConfig
	data, err := os.ReadFile(configPath())
	if err != nil {
		data, err = os.ReadFile(legacyConfigPath())
	}
	if err == nil {
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
