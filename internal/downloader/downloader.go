// Package downloader drives yt-dlp.exe through a sequential queue, captures
// metadata into the library, and reports progress to the UI.
package downloader

import (
	"bufio"
	"encoding/json"
	"hash/fnv"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"blush/internal/library"
)

// Job is one queued download, as shown in the UI.
type Job struct {
	ID      string  `json:"id"`
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Status  string  `json:"status"` // queued | downloading | done | duplicate | error | removed
	Percent float64 `json:"percent"`
	Speed   string  `json:"speed"`
	ETA     string  `json:"eta"`
	Count   int     `json:"count"`
	Error   string  `json:"error"`
}

// Config holds the tool paths and destination for downloads.
type Config struct {
	YtDlp      string
	FfmpegDir  string
	MediaRoot  string
	StateDir   string // .keepsake: where the sync cache (and other state) lives
	Archive    string
	CookieSpec string // "" | "browser:<name>" | "file:<path>"
}

// Downloader runs one job at a time on a background worker.
type Downloader struct {
	cfg   Config
	db    *library.DB
	emit  func(event string, data any)
	jobs  map[string]*Job
	order []string
	wake  chan struct{} // buffered(1): nudges the worker that new work may exist
	seq   uint64        // monotonic id source (guarded by mu)
	mu    sync.Mutex

	enumMu    sync.Mutex
	enumCache map[string]*SyncList // sync URL -> saved enumeration (persisted)
}

// SyncList is a saved enumeration of a remote URL (channel / favorites / list),
// with the metadata the Sync page shows: a friendly title, what kind it is, and
// when it was last fetched.
type SyncList struct {
	URL       string       `json:"url"`
	Title     string       `json:"title"`
	Kind      string       `json:"kind"` // favorites | pornstar | model | channel | user | list
	FetchedAt string       `json:"fetchedAt"`
	Items     []RemoteItem `json:"items"`
}

// SyncSummary is the at-a-glance card for a saved sync (counts computed live
// against the download archive).
type SyncSummary struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	FetchedAt string `json:"fetchedAt"`
	Count     int    `json:"count"`
	Owned     int    `json:"owned"`
	New       int    `json:"new"`
}

func New(cfg Config, db *library.DB, emit func(string, any)) *Downloader {
	d := &Downloader{
		cfg:  cfg,
		db:   db,
		emit: emit,
		jobs: map[string]*Job{},
		wake: make(chan struct{}, 1),
	}
	go d.worker()
	return d
}

// SetCookieSpec updates the auth used for future downloads.
func (d *Downloader) SetCookieSpec(spec string) {
	d.mu.Lock()
	d.cfg.CookieSpec = spec
	d.mu.Unlock()
}

// RemoteItem is one video in a remote list (model catalogue / favorites).
type RemoteItem struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	ID    string `json:"id"`
	Owned bool   `json:"owned"` // already in your library/archive
}

// env returns the child environment with the yt-dlp folder prepended to PATH,
// so yt-dlp can find sidecar tools that live beside it (e.g. phantomjs.exe,
// which Pornhub's listing pages require to solve their JS challenge).
// PYTHONUTF8=1 forces yt-dlp's piped output to UTF-8: without it, Windows
// encodes --print paths in the ANSI code page, mangling emoji/non-Latin
// filenames so the [[DONE]] path never matches the file on disk and the
// download is saved but never catalogued.
func (d *Downloader) env() []string {
	dir := filepath.Dir(d.cfg.YtDlp)
	var out []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			out = append(out, e)
		}
	}
	out = append(out, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	return append(out, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (d *Downloader) cookieArgs() []string {
	d.mu.Lock()
	spec := d.cfg.CookieSpec
	d.mu.Unlock()
	switch {
	case strings.HasPrefix(spec, "file:"):
		p := strings.TrimPrefix(spec, "file:")
		if validCookieFile(p) {
			return []string{"--cookies", p}
		}
		return nil // missing/corrupt cookie file — download without it rather than failing
	case strings.HasPrefix(spec, "browser:"):
		return []string{"--cookies-from-browser", strings.TrimPrefix(spec, "browser:")}
	}
	return nil
}

// validCookieFile reports whether path is a usable Netscape cookie file. A
// zeroed/corrupt or headerless file is rejected so it can't break every download.
func validCookieFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	s := string(data)
	if strings.IndexByte(s, 0) >= 0 { // null bytes = corruption
		return false
	}
	if strings.Contains(s, "# Netscape HTTP Cookie File") || strings.Contains(s, "# HTTP Cookie File") {
		return true
	}
	for _, ln := range strings.Split(s, "\n") { // or at least one real cookie line
		if !strings.HasPrefix(ln, "#") && strings.Count(ln, "\t") >= 6 {
			return true
		}
	}
	return false
}

// Enumerate lists the videos in a model/playlist/favorites URL and flags which
// you already own. Results are cached per-URL (persisted in the vault) so the
// slow yt-dlp fetch is skipped on revisits; pass refresh=true to re-fetch.
func (d *Downloader) Enumerate(listURL string, refresh bool) ([]RemoteItem, error) {
	listURL = strings.TrimSpace(listURL)
	if listURL == "" {
		return nil, nil
	}
	if !refresh {
		if cached := d.cachedEnum(listURL); len(cached) > 0 {
			return d.withOwned(cached), nil // recompute "owned" against the current archive
		}
	}

	args := []string{"--ignore-config", "--encoding", "utf-8", "--flat-playlist", "--no-warnings",
		"--print", "%(url)s %(title)s"}
	args = append(args, d.cookieArgs()...)
	args = append(args, listURL)

	runOnce := func() []byte {
		c := exec.Command(d.cfg.YtDlp, args...)
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		c.Env = d.env()
		o, _ := c.Output()
		return o
	}
	out := runOnce()
	if len(strings.TrimSpace(string(out))) == 0 {
		time.Sleep(1500 * time.Millisecond) // transient anti-bot block — retry once
		out = runOnce()
	}
	var items []RemoteItem
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		url, title := line, ""
		if sp := strings.IndexByte(line, ' '); sp >= 0 {
			url, title = line[:sp], strings.TrimSpace(line[sp+1:])
		}
		items = append(items, RemoteItem{URL: url, Title: title, ID: viewkey(url)})
	}
	if len(items) > 0 {
		d.storeEnum(listURL, items) // only cache a successful fetch
	}
	return d.withOwned(items), nil
}

// ---- sync-list cache (so re-syncing a channel/favorites is instant) ----

func (d *Downloader) syncCachePath() string {
	dir := d.cfg.StateDir
	if dir == "" {
		dir = d.cfg.MediaRoot // back-compat (e.g. tests that don't set StateDir)
	}
	return filepath.Join(dir, "sync_cache.json")
}

func (d *Downloader) loadEnumCache() {
	d.enumMu.Lock()
	defer d.enumMu.Unlock()
	if d.enumCache != nil {
		return
	}
	d.enumCache = map[string]*SyncList{}
	data, err := os.ReadFile(d.syncCachePath())
	if err != nil {
		return
	}
	if json.Unmarshal(data, &d.enumCache) == nil {
		return
	}
	// Migrate the old format (map[url][]RemoteItem) into the richer SyncList.
	d.enumCache = map[string]*SyncList{}
	var old map[string][]RemoteItem
	if json.Unmarshal(data, &old) == nil {
		for u, items := range old {
			d.enumCache[u] = &SyncList{URL: u, Title: deriveTitle(u), Kind: deriveKind(u), Items: items}
		}
	}
}

func (d *Downloader) cachedEnum(url string) []RemoteItem {
	d.loadEnumCache()
	d.enumMu.Lock()
	defer d.enumMu.Unlock()
	if sl := d.enumCache[url]; sl != nil {
		return sl.Items
	}
	return nil
}

func (d *Downloader) storeEnum(u string, items []RemoteItem) {
	d.loadEnumCache()
	d.enumMu.Lock()
	d.enumCache[u] = &SyncList{
		URL: u, Title: deriveTitle(u), Kind: deriveKind(u),
		FetchedAt: time.Now().Format("2006-01-02 15:04:05"), Items: items,
	}
	data, _ := json.MarshalIndent(d.enumCache, "", " ")
	d.enumMu.Unlock()
	_ = os.WriteFile(d.syncCachePath(), data, 0o644)
}

// SyncedLists returns saved syncs with live owned/new counts, newest fetch first.
func (d *Downloader) SyncedLists() []SyncSummary {
	d.loadEnumCache()
	owned := d.archiveSet()
	d.enumMu.Lock()
	defer d.enumMu.Unlock()
	out := make([]SyncSummary, 0, len(d.enumCache))
	for _, sl := range d.enumCache {
		o, n := 0, 0
		for _, it := range sl.Items {
			if it.ID != "" && owned["pornhub "+it.ID] {
				o++
			} else {
				n++
			}
		}
		out = append(out, SyncSummary{
			URL: sl.URL, Title: sl.Title, Kind: sl.Kind, FetchedAt: sl.FetchedAt,
			Count: len(sl.Items), Owned: o, New: n,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FetchedAt > out[j].FetchedAt })
	return out
}

// RemoveSync forgets a saved sync.
func (d *Downloader) RemoveSync(u string) {
	d.loadEnumCache()
	d.enumMu.Lock()
	delete(d.enumCache, u)
	data, _ := json.MarshalIndent(d.enumCache, "", " ")
	d.enumMu.Unlock()
	_ = os.WriteFile(d.syncCachePath(), data, 0o644)
}

func deriveKind(u string) string {
	lu := strings.ToLower(u)
	switch {
	case strings.Contains(lu, "favorit"):
		return "favorites"
	case strings.Contains(lu, "/pornstar/"):
		return "pornstar"
	case strings.Contains(lu, "/model/"):
		return "model"
	case strings.Contains(lu, "/channel"):
		return "channel"
	case strings.Contains(lu, "/users/"):
		return "user"
	}
	return "list"
}

var syncSlugRe = regexp.MustCompile(`(?i)/(?:model|pornstar|channels?|users)/([^/?#]+)`)

// deriveTitle makes a friendly name from a sync URL (the performer/channel, or
// "<Name> — favorites" for a favorites list).
func deriveTitle(u string) string {
	m := syncSlugRe.FindStringSubmatch(u)
	if strings.Contains(strings.ToLower(u), "favorit") {
		if len(m) > 1 {
			return prettySlug(m[1]) + " — favorites"
		}
		return "Favorites"
	}
	if len(m) > 1 {
		return prettySlug(m[1])
	}
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func prettySlug(s string) string {
	if dec, err := url.QueryUnescape(s); err == nil {
		s = dec
	}
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// withOwned returns a copy of items with the Owned flag set from the current archive.
func (d *Downloader) withOwned(items []RemoteItem) []RemoteItem {
	owned := d.archiveSet()
	out := make([]RemoteItem, len(items))
	for i, it := range items {
		it.Owned = it.ID != "" && owned["pornhub "+it.ID]
		out[i] = it
	}
	return out
}

// SyncedURLs returns the URLs that have a cached enumeration (for a "saved syncs" list).
func (d *Downloader) SyncedURLs() []string {
	d.loadEnumCache()
	d.enumMu.Lock()
	defer d.enumMu.Unlock()
	out := make([]string, 0, len(d.enumCache))
	for u := range d.enumCache {
		out = append(out, u)
	}
	return out
}

func (d *Downloader) archiveSet() map[string]bool {
	m := map[string]bool{}
	data, err := os.ReadFile(d.cfg.Archive)
	if err != nil {
		return m
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = true
		}
	}
	return m
}

func viewkey(u string) string {
	if i := strings.Index(u, "viewkey="); i >= 0 {
		s := u[i+len("viewkey="):]
		if j := strings.IndexAny(s, "&#"); j >= 0 {
			s = s[:j]
		}
		return s
	}
	return ""
}

// nextID returns a process-unique job id. Caller must hold d.mu (a monotonic
// counter avoids the nanosecond-collision bug under parallel enqueues).
func (d *Downloader) nextID() string {
	d.seq++
	return strconv.FormatUint(d.seq, 36)
}

// notify wakes the worker without ever blocking the caller.
func (d *Downloader) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// addLocked queues url unless it's already in flight. Caller holds d.mu.
func (d *Downloader) addLocked(url string) (string, bool) {
	for _, jid := range d.order {
		if j := d.jobs[jid]; j != nil && j.URL == url && (j.Status == "queued" || j.Status == "downloading") {
			return jid, false // duplicate — already queued or downloading
		}
	}
	id := d.nextID()
	d.jobs[id] = &Job{ID: id, URL: url, Title: url, Status: "queued"}
	d.order = append(d.order, id)
	return id, true
}

// Enqueue adds a single URL (deduped against in-flight jobs) and returns its id.
func (d *Downloader) Enqueue(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	d.mu.Lock()
	id, added := d.addLocked(url)
	d.mu.Unlock()
	if added {
		d.notify()
		d.emitQueue()
	}
	return id
}

// EnqueueMany adds many URLs at once (deduped). Returns how many were newly queued.
func (d *Downloader) EnqueueMany(urls []string) int {
	added := 0
	d.mu.Lock()
	for _, u := range urls {
		if u = strings.TrimSpace(u); u != "" {
			if _, ok := d.addLocked(u); ok {
				added++
			}
		}
	}
	d.mu.Unlock()
	if added > 0 {
		d.notify()
		d.emitQueue()
	}
	return added
}

func (d *Downloader) RemoveJob(id string) {
	d.mu.Lock()
	if j, ok := d.jobs[id]; ok && j.Status == "queued" {
		j.Status = "removed"
		d.order = removeStr(d.order, id)
	}
	d.mu.Unlock()
	d.emitQueue()
}

func (d *Downloader) ClearFinished() {
	d.mu.Lock()
	kept := d.order[:0:0]
	for _, id := range d.order {
		if s := d.jobs[id].Status; s == "queued" || s == "downloading" {
			kept = append(kept, id)
		}
	}
	d.order = kept
	d.mu.Unlock()
	d.emitQueue()
}

// Snapshot returns the queue for the UI.
func (d *Downloader) Snapshot() []Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Job, 0, len(d.order))
	for _, id := range d.order {
		out = append(out, *d.jobs[id])
	}
	return out
}

func (d *Downloader) emitQueue() {
	if d.emit != nil {
		d.emit("queue", d.Snapshot())
	}
}

func (d *Downloader) worker() {
	for {
		d.mu.Lock()
		var next *Job
		for _, jid := range d.order { // first still-queued job, in order
			if j := d.jobs[jid]; j != nil && j.Status == "queued" {
				next = j
				break
			}
		}
		d.mu.Unlock()
		if next == nil {
			<-d.wake // idle — wait for new work
			continue
		}
		d.run(next)
	}
}

func (d *Downloader) run(j *Job) {
	d.mu.Lock()
	j.Status = "downloading"
	cfg := d.cfg
	d.mu.Unlock()
	d.emitQueue()

	// Flat layout: files land directly in media/ named by source+id (pure
	// machine identity — models/titles live only in the catalogue). ingest
	// canonicalises the name and parks the sidecars under .keepsake after
	// the download finishes.
	outtmpl := filepath.Join(cfg.MediaRoot, library.MediaDirName,
		"%(extractor_key)s-%(id)s.%(ext)s")

	args := []string{
		"--ignore-config",
		// The PyInstaller-frozen yt-dlp.exe ignores PYTHONUTF8/PYTHONIOENCODING,
		// so piped output falls back to the ANSI code page and silently drops
		// emoji/non-Latin chars from printed paths — breaking ingest. --encoding
		// is honored by the frozen exe and keeps [[DONE]] paths byte-accurate.
		"--encoding", "utf-8",
		"-f", "bestvideo+bestaudio/best",
		"--merge-output-format", "mp4",
		"--postprocessor-args", "Merger:-movflags +faststart", // moov at front → instant seek

		"-o", outtmpl,
		"--download-archive", cfg.Archive,
		"--write-info-json",
		"--write-thumbnail",
		"--no-warnings", "--newline", "--no-simulate",
		"--socket-timeout", "30", // abort a dead socket instead of hanging forever
		"--retries", "10", "--fragment-retries", "10", // ride out transient stalls
		"--no-continue", // tokened CDNs (go.porn) don't resume cleanly — a stale .part hangs forever; always fetch fresh
		"--progress-template",
		"download:[[PROG]]%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s|%(progress.downloaded_bytes)s",
		"--print", "after_move:[[DONE]]%(filepath)s",
	}
	if cfg.FfmpegDir != "" {
		args = append(args, "--ffmpeg-location", cfg.FfmpegDir)
	}
	args = append(args, d.cookieArgs()...) // validated; skips a missing/corrupt cookie file
	args = append(args, j.URL)

	cmd := exec.Command(cfg.YtDlp, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = d.env()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout // fold stderr into the same stream for scanning

	var saved []string
	var lastErr string
	if err := cmd.Start(); err != nil {
		d.finish(j, nil, err, "Couldn't start yt-dlp: "+err.Error())
		return
	}

	// Stall watchdog: if the download makes no byte progress for stallTimeout,
	// kill yt-dlp (and its child) and move on — so one dead connection can't
	// freeze the whole queue. Any non-progress output (retries, merge, post-
	// processing) counts as activity, so a working merge is never killed.
	const stallTimeout = 2 * time.Minute
	var pmu sync.Mutex
	var lastBytes int64 = -1
	lastActive := time.Now()
	stalled := false
	watchDone := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-t.C:
				pmu.Lock()
				idle := time.Since(lastActive)
				pmu.Unlock()
				if idle > stallTimeout {
					pmu.Lock()
					stalled = true
					pmu.Unlock()
					killTree(cmd)
					return
				}
			}
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "[[PROG]]"); i >= 0 {
			payload := line[i+8:]
			d.applyProgress(j, payload)
			if b := progBytes(payload); b > lastBytes { // real download progress only
				pmu.Lock()
				lastBytes = b
				lastActive = time.Now()
				pmu.Unlock()
			}
			continue
		}
		// any non-progress line = activity (retries, post-processing, merge)
		pmu.Lock()
		lastActive = time.Now()
		pmu.Unlock()
		switch {
		case strings.Contains(line, "[[DONE]]"):
			saved = append(saved, strings.TrimSpace(line[strings.Index(line, "[[DONE]]")+8:]))
		case strings.Contains(line, "ERROR:"):
			lastErr = line // errors still print even in --print/quiet mode
		}
	}
	close(watchDone)
	runErr := cmd.Wait()
	pmu.Lock()
	wasStalled := stalled
	pmu.Unlock()
	if wasStalled && lastErr == "" {
		lastErr = "ERROR: Stalled — no data for over 2 minutes; skipped to keep the queue moving."
	}
	d.finish(j, saved, runErr, lastErr)
}

// progBytes extracts downloaded_bytes (the 4th field) from a progress payload.
func progBytes(payload string) int64 {
	parts := strings.Split(payload, "|")
	if len(parts) < 4 {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// killTree force-kills a process and its children. yt-dlp's PyInstaller bootloader
// spawns a child that must also die, or the download keeps running orphaned.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	k := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	k.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = k.Run()
}

func (d *Downloader) applyProgress(j *Job, payload string) {
	parts := strings.SplitN(payload, "|", 4) // 4th field (downloaded_bytes) used by the watchdog
	d.mu.Lock()
	if len(parts) > 0 {
		if p, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[0]), "%"), 64); err == nil {
			j.Percent = p
		}
	}
	if len(parts) > 1 {
		j.Speed = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		j.ETA = strings.TrimSpace(parts[2])
	}
	prog := Job{ID: j.ID, Percent: j.Percent, Speed: j.Speed, ETA: j.ETA}
	d.mu.Unlock()
	if d.emit != nil {
		d.emit("progress", prog)
	}
}

func (d *Downloader) finish(j *Job, saved []string, runErr error, lastErr string) {
	count := 0
	var firstTitle string
	for _, fp := range saved {
		if v, ok := d.ingest(fp); ok {
			count++
			if firstTitle == "" {
				firstTitle = v.Title
			}
		}
	}
	d.mu.Lock()
	switch {
	case count > 0:
		j.Status, j.Count, j.Percent = "done", count, 100
		if firstTitle != "" {
			j.Title = firstTitle
		}
	case runErr == nil:
		// yt-dlp exited cleanly but saved nothing -> already in the archive.
		j.Status = "duplicate"
	default:
		j.Status, j.Error = "error", cleanErr(lastErr)
	}
	d.mu.Unlock()
	d.emitQueue()
}

// cleanErr turns a raw yt-dlp "ERROR: …" line into something readable.
func cleanErr(line string) string {
	if i := strings.Index(line, "ERROR:"); i >= 0 {
		if msg := strings.TrimSpace(line[i+len("ERROR:"):]); msg != "" {
			return msg
		}
	}
	return "Download failed — check the URL, or set login cookies for protected posts."
}

// ytInfo mirrors the fields we want from a yt-dlp .info.json sidecar.
type ytInfo struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Uploader     string   `json:"uploader"`
	UploaderID   string   `json:"uploader_id"`
	Channel      string   `json:"channel"`
	Duration     *float64 `json:"duration"`
	Width        *int     `json:"width"`
	Height       *int     `json:"height"`
	Ext          string   `json:"ext"`
	Thumbnail    string   `json:"thumbnail"`
	WebpageURL   string   `json:"webpage_url"`
	ExtractorKey string   `json:"extractor_key"`
	UploadDate   string   `json:"upload_date"`
	ViewCount    *int     `json:"view_count"`
	LikeCount    *int     `json:"like_count"`
	Tags         []string `json:"tags"`
	Categories   []string `json:"categories"`
	Description  string   `json:"description"`
}

// ---- flat layout paths ---------------------------------------------------

func (d *Downloader) mediaDir() string {
	return filepath.Join(d.cfg.MediaRoot, library.MediaDirName)
}

func (d *Downloader) stateDirOrDefault() string {
	if d.cfg.StateDir != "" {
		return d.cfg.StateDir
	}
	return filepath.Join(d.cfg.MediaRoot, stateDirBase)
}

func (d *Downloader) metaDir() string {
	return filepath.Join(d.stateDirOrDefault(), library.MetaDirName)
}

func (d *Downloader) thumbsDir() string {
	return filepath.Join(d.stateDirOrDefault(), library.ThumbsDirName)
}

// inMediaDir reports whether p sits inside the flat media/ folder (as opposed
// to the legacy <Site>/<Uploader>/ tree).
func (d *Downloader) inMediaDir(p string) bool {
	rel, err := filepath.Rel(d.mediaDir(), p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ingest reads the sidecar metadata for a downloaded file and stores it. The
// sidecar sits beside the file (fresh download, legacy tree) or under
// .keepsake/meta (flat layout).
func (d *Downloader) ingest(filepathStr string) (library.Video, bool) {
	stem := strings.TrimSuffix(filepathStr, filepath.Ext(filepathStr))
	sidecar := stem + ".info.json"
	if !fileExists(sidecar) && d.inMediaDir(filepathStr) {
		if alt := filepath.Join(d.metaDir(), filepath.Base(stem)+".info.json"); fileExists(alt) {
			sidecar = alt
		}
	}
	return d.ingestFrom(filepathStr, sidecar)
}

// ingestFrom catalogues a media file using an explicit sidecar path.
func (d *Downloader) ingestFrom(filepathStr, sidecar string) (library.Video, bool) {
	info := ytInfo{}
	if data, err := os.ReadFile(sidecar); err == nil {
		_ = json.Unmarshal(data, &info)
	}
	if info.ID == "" {
		return library.Video{}, false
	}

	uploader := firstNonEmpty(info.Uploader, info.UploaderID, info.Channel)
	// Pornhub's uploader is reliably the performer, so default the model to it.
	// X/Twitter is a repost firehose, so leave it Unassigned for manual sorting.
	var models []string
	if info.ExtractorKey != "Twitter" && uploader != "" {
		models = []string{uploader}
	}
	var thumb string
	if d.inMediaDir(filepathStr) {
		filepathStr, thumb = d.canonicalizeFlat(filepathStr, sidecar, info)
	} else {
		stem := strings.TrimSuffix(filepathStr, filepath.Ext(filepathStr))
		thumb = findThumb(stem)
		if thumb == "" {
			thumb = d.makeThumb(filepathStr, stem, info.Duration) // ffmpeg fallback
		}
	}
	v := library.Video{
		ID:           info.ID,
		Site:         info.ExtractorKey,
		Title:        firstNonEmpty(info.Title, uploader),
		Uploader:     uploader,
		Models:       models,
		Width:        info.Width,
		Height:       info.Height,
		Ext:          strings.TrimPrefix(filepath.Ext(filepathStr), "."),
		Filepath:     filepathStr,
		Filename:     filepath.Base(filepathStr),
		Thumbnail:    thumb,
		ThumbnailURL: info.Thumbnail,
		WebpageURL:   info.WebpageURL,
		UploadDate:   info.UploadDate,
		ViewCount:    info.ViewCount,
		LikeCount:    info.LikeCount,
		Tags:         info.Tags,
		Categories:   info.Categories,
		Description:  info.Description,
		Added:        time.Now().Format("2006-01-02 15:04:05"),
	}
	if info.Duration != nil {
		dur := int(*info.Duration)
		v.Duration = &dur
	}
	if st, err := os.Stat(filepathStr); err == nil {
		sz := st.Size()
		v.Filesize = &sz
	}
	if err := d.db.Upsert(v); err != nil {
		return library.Video{}, false
	}
	return v, true
}

// canonicalizeFlat finalises a file living in media/: renames it to the
// canonical "<site>-<id>.<ext>" (yt-dlp's own naming can differ in case or
// sanitisation), moves the .info.json to .keepsake/meta and the thumbnail to
// .keepsake/thumbs, and returns the final video + thumbnail paths.
func (d *Downloader) canonicalizeFlat(fp, sidecar string, info ytInfo) (video, thumb string) {
	origStem := strings.TrimSuffix(fp, filepath.Ext(fp))
	ext := strings.ToLower(filepath.Ext(fp))
	base := library.FlatBase(info.ExtractorKey, info.ID)

	if want := filepath.Join(d.mediaDir(), base+ext); !strings.EqualFold(fp, want) {
		if !fileExists(want) && os.Rename(fp, want) == nil {
			fp = want
		}
	}

	if fileExists(sidecar) {
		want := filepath.Join(d.metaDir(), base+".info.json")
		if !strings.EqualFold(sidecar, want) {
			_ = os.MkdirAll(d.metaDir(), 0o755)
			if fileExists(want) {
				_ = os.Remove(sidecar) // same sidecar written twice; keep the parked copy
			} else {
				_ = os.Rename(sidecar, want)
			}
		}
	}

	_ = os.MkdirAll(d.thumbsDir(), 0o755)
	for _, e := range []string{".jpg", ".jpeg", ".webp", ".png"} {
		src := origStem + e
		dst := filepath.Join(d.thumbsDir(), base+e)
		if fileExists(src) && !strings.EqualFold(src, dst) {
			if fileExists(dst) {
				_ = os.Remove(src)
			} else {
				_ = os.Rename(src, dst)
			}
		}
		if thumb == "" && fileExists(dst) {
			thumb = dst
		}
	}
	if thumb == "" {
		thumb = d.makeThumb(fp, filepath.Join(d.thumbsDir(), base), info.Duration)
	}
	return fp, thumb
}

func findThumb(stem string) string {
	for _, ext := range []string{".jpg", ".jpeg", ".webp", ".png"} {
		if _, err := os.Stat(stem + ext); err == nil {
			return stem + ext
		}
	}
	return ""
}

// makeThumb extracts a poster frame from the video with ffmpeg when the site
// didn't give us a thumbnail. Returns the .jpg path, or "" on failure.
func (d *Downloader) makeThumb(video, stem string, dur *float64) string {
	ff := "ffmpeg"
	if d.cfg.FfmpegDir != "" {
		ff = filepath.Join(d.cfg.FfmpegDir, "ffmpeg.exe")
	}
	ts := 8
	if dur != nil && *dur > 0 {
		ts = int(*dur * 0.25)
	}
	if ts < 1 {
		ts = 1
	}
	out := stem + ".jpg"
	cmd := exec.Command(ff, "-y", "-ss", strconv.Itoa(ts), "-i", video,
		"-frames:v", "1", "-q:v", "3", "-vf", "scale=640:-1", out)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return ""
	}
	if _, err := os.Stat(out); err == nil {
		return out
	}
	return ""
}

// ---- importing local files --------------------------------------------

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".webm": true, ".mov": true, ".m4v": true, ".avi": true, ".ts": true,
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".bmp": true,
}

var (
	reRes   = regexp.MustCompile(`(?i)[_-]\d{3,4}p$`)
	reHash  = regexp.MustCompile(`(?i)[-_][0-9a-f]{6,12}$`)
	reSpace = regexp.MustCompile(`\s+`)
)

// cleanTitle turns a download-site filename into a readable title.
func cleanTitle(filename string) string {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	stem = reRes.ReplaceAllString(stem, "")  // drop trailing _720p / -1080p
	stem = reHash.ReplaceAllString(stem, "") // drop trailing -3c7bd96 hash
	stem = strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(stem)
	stem = strings.TrimSpace(reSpace.ReplaceAllString(stem, " "))
	words := strings.Fields(stem)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	t := strings.Join(words, " ")
	if t == "" {
		return filename
	}
	return t
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	for _, ch := range `<>:"/\|?*` {
		s = strings.ReplaceAll(s, string(ch), "_")
	}
	return s
}

func hashStr(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(s)))
	return strconv.FormatUint(uint64(h.Sum32()), 16)
}

func copyFile(src, dst string) error {
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
	cerr := out.Close()
	if err != nil {
		return err
	}
	return cerr
}

func (d *Downloader) ffprobeInfo(path string) (dur, w, h *int) {
	ff := "ffprobe"
	if d.cfg.FfmpegDir != "" {
		ff = filepath.Join(d.cfg.FfmpegDir, "ffprobe.exe")
	}
	cmd := exec.Command(ff, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height:format=duration", "-of", "json", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, nil
	}
	var probe struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &probe) != nil {
		return nil, nil, nil
	}
	if len(probe.Streams) > 0 {
		wv, hv := probe.Streams[0].Width, probe.Streams[0].Height
		if wv > 0 {
			w = &wv
		}
		if hv > 0 {
			h = &hv
		}
	}
	if f, e := strconv.ParseFloat(probe.Format.Duration, 64); e == nil && f > 0 {
		di := int(f)
		dur = &di
	}
	return dur, w, h
}

// Import copies/catalogues local video files (and folders) into the library
// under the given model. Folders default the model to the folder name.
func (d *Downloader) Import(paths []string, model string) {
	go d.importWork(paths, model)
}

func (d *Downloader) importWork(paths []string, model string) {
	type task struct{ path, model, kind string }
	var tasks []task
	add := func(fp, m string) {
		ext := strings.ToLower(filepath.Ext(fp))
		if videoExts[ext] {
			tasks = append(tasks, task{fp, m, "video"})
		} else if imageExts[ext] {
			tasks = append(tasks, task{fp, m, "photo"})
		}
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			m := model
			if strings.TrimSpace(m) == "" {
				m = filepath.Base(p) // a dropped folder names the model
			}
			_ = filepath.WalkDir(p, func(fp string, e fs.DirEntry, err error) error {
				if err == nil && !e.IsDir() {
					add(fp, m)
				}
				return nil
			})
		} else {
			add(p, model)
		}
	}
	total := len(tasks)
	added := 0
	for i, t := range tasks {
		ok := false
		if t.kind == "video" {
			ok = d.importOne(t.path, t.model)
		} else {
			ok = d.importPhotoOne(t.path, t.model)
		}
		if ok {
			added++
		}
		d.emit("import", map[string]any{"done": i + 1, "total": total, "name": filepath.Base(t.path)})
	}
	d.emit("import", map[string]any{"done": total, "total": total, "added": added, "finished": true})
}

// localIdentity derives a stable catalogue id for an imported file. It hashes
// the LEGACY Local/<model>/<file> path the file would have had before the flat
// layout, so re-imports — and rows created before the migration — land on the
// same row and keep the user's edits.
func (d *Downloader) localIdentity(prefix, subdir, folder, base string) string {
	parts := []string{d.cfg.MediaRoot, "Local", sanitizeName(folder)}
	if subdir != "" {
		parts = append(parts, subdir)
	}
	parts = append(parts, base)
	return prefix + hashStr(filepath.Join(parts...))
}

func (d *Downloader) importPhotoOne(src, model string) bool {
	base := filepath.Base(src)
	folder := model
	if strings.TrimSpace(folder) == "" {
		folder = "Unassigned"
	}
	id := d.localIdentity("photo_", "photos", folder, base)
	dest := filepath.Join(d.mediaDir(), library.FlatBase("photo", id)+strings.ToLower(filepath.Ext(base)))
	if strings.EqualFold(src, dest) {
		dest = src
	} else {
		if err := os.MkdirAll(d.mediaDir(), 0o755); err != nil {
			return false
		}
		if !fileExists(dest) {
			if copyFile(src, dest) != nil {
				return false
			}
		}
	}
	return d.db.AddPhoto(library.Photo{
		ID:       id,
		Model:    model,
		Filepath: dest,
		Filename: base,
		Added:    time.Now().Format("2006-01-02 15:04:05"),
	}) == nil
}

func (d *Downloader) importOne(src, model string) bool {
	base := filepath.Base(src)
	folder := model
	if strings.TrimSpace(folder) == "" {
		folder = "Unassigned"
	}
	id := d.localIdentity("local_", "", folder, base)
	flatBase := library.FlatBase("Local", id)
	dest := filepath.Join(d.mediaDir(), flatBase+strings.ToLower(filepath.Ext(base)))
	if strings.EqualFold(src, dest) {
		dest = src
	} else {
		if err := os.MkdirAll(d.mediaDir(), 0o755); err != nil {
			return false
		}
		if !fileExists(dest) { // don't recopy if already there
			if copyFile(src, dest) != nil {
				return false
			}
		}
	}

	dur, w, h := d.ffprobeInfo(dest)
	_ = os.MkdirAll(d.thumbsDir(), 0o755)
	thumbStem := filepath.Join(d.thumbsDir(), flatBase)
	thumb := findThumb(thumbStem)
	if thumb == "" {
		var df *float64
		if dur != nil {
			f := float64(*dur)
			df = &f
		}
		thumb = d.makeThumb(dest, thumbStem, df)
	}
	// Synthetic sidecar so RebuildFromDisk can restore this row from disk alone.
	d.writeLocalSidecar(flatBase, id, cleanTitle(base), model, dur, w, h)
	var size *int64
	if st, err := os.Stat(dest); err == nil {
		s := st.Size()
		size = &s
	}
	var models []string
	if strings.TrimSpace(model) != "" {
		models = []string{model}
	}
	v := library.Video{
		ID:       id,
		Site:     "Local",
		Title:    cleanTitle(base),
		Uploader: model,
		Models:   models,
		Duration: dur, Width: w, Height: h,
		Ext:      strings.TrimPrefix(strings.ToLower(filepath.Ext(dest)), "."),
		Filepath: dest, Filename: filepath.Base(dest),
		Thumbnail: thumb,
		Filesize:  size,
		Added:     time.Now().Format("2006-01-02 15:04:05"),
	}
	return d.db.Upsert(v) == nil
}

// writeLocalSidecar writes a minimal .info.json for a local import, carrying
// exactly the fields ingest reads, so a rebuild keeps the same id and model.
func (d *Downloader) writeLocalSidecar(flatBase, id, title, uploader string, dur, w, h *int) {
	if err := os.MkdirAll(d.metaDir(), 0o755); err != nil {
		return
	}
	m := map[string]any{
		"id":            id,
		"extractor_key": "Local",
		"title":         title,
		"uploader":      uploader,
	}
	if dur != nil {
		m["duration"] = *dur
	}
	if w != nil {
		m["width"] = *w
	}
	if h != nil {
		m["height"] = *h
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(filepath.Join(d.metaDir(), flatBase+".info.json"), b, 0o644)
}

// ---- rebuild catalogue from disk --------------------------------------

// RebuildFromDisk re-catalogues the vault by scanning it: every yt-dlp download
// is re-ingested from its .info.json sidecar, and every video sitting directly
// in Local/<model>/ is catalogued in place. Upsert keeps user data (model
// assignments, favorites, labels) on rows that already exist, and collection
// membership is keyed by site+id, so nothing the user set is lost. This both
// rebuilds the library if the DB is gone and re-points filepaths after a manual
// reorganisation. Returns how many media files were catalogued.
func (d *Downloader) RebuildFromDisk() (int, error) {
	root := d.cfg.MediaRoot
	if root == "" {
		return 0, nil
	}
	n := 0
	sep := string(filepath.Separator)

	// 0) Flat layout: sidecars live in .keepsake/meta, media in media/. This
	// covers everything the flat migration (or a post-migration download or
	// import) produced — including Local files via their synthetic sidecars.
	if entries, err := os.ReadDir(d.metaDir()); err == nil {
		const suffix = ".info.json"
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), suffix) {
				continue
			}
			stem := filepath.Join(d.mediaDir(), name[:len(name)-len(suffix)])
			if media := findMediaFile(stem); media != "" {
				if _, ok := d.ingestFrom(media, filepath.Join(d.metaDir(), name)); ok {
					n++
				}
			}
		}
	}

	// 1) Legacy tree: ingest the media beside every .info.json sidecar.
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == stateDirBase { // skip .keepsake
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".info.json") {
			return nil
		}
		stem := strings.TrimSuffix(p, ".info.json")
		if media := findMediaFile(stem); media != "" {
			if _, ok := d.ingest(media); ok {
				n++
			}
		}
		return nil
	})

	// 2) Local imports (no sidecar): catalogue videos directly in Local/<model>/.
	// We deliberately don't descend into uploads/ (those were copied up to the
	// model folder on import) to avoid cataloguing a file twice.
	localRoot := filepath.Join(root, "Local")
	_ = filepath.WalkDir(localRoot, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(localRoot, p)
		if e.IsDir() {
			if rel != "." && strings.Contains(rel, sep) {
				return fs.SkipDir // deeper than Local/<model> (uploads/, photos/)
			}
			return nil
		}
		if !videoExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		parts := strings.Split(rel, sep)
		if len(parts) != 2 { // must be exactly Local/<model>/<file>
			return nil
		}
		stem := strings.TrimSuffix(p, filepath.Ext(p))
		if _, err := os.Stat(stem + ".info.json"); err == nil {
			return nil // already ingested via the sidecar pass
		}
		model := parts[0]
		if model == "Unassigned" {
			model = ""
		}
		if d.catalogInPlace(p, model) {
			n++
		}
		return nil
	})

	d.emitQueue()
	return n, nil
}

// stateDirBase mirrors core's .xxx state folder name so a rebuild scan skips it.
const stateDirBase = ".xxx"

// findMediaFile returns the video file sharing stem (the part before .info.json).
func findMediaFile(stem string) string {
	for ext := range videoExts {
		if _, err := os.Stat(stem + ext); err == nil {
			return stem + ext
		}
	}
	return ""
}

// catalogInPlace records a Local video exactly where it sits (no copy). The id
// matches importOne's (hash of the final path), so re-running upserts the same
// row and preserves the user's model/favorite/label edits.
func (d *Downloader) catalogInPlace(path, model string) bool {
	dur, w, h := d.ffprobeInfo(path)
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	thumb := findThumb(stem)
	if thumb == "" {
		var df *float64
		if dur != nil {
			f := float64(*dur)
			df = &f
		}
		thumb = d.makeThumb(path, stem, df)
	}
	var size *int64
	if st, err := os.Stat(path); err == nil {
		s := st.Size()
		size = &s
	}
	var models []string
	if strings.TrimSpace(model) != "" {
		models = []string{model}
	}
	v := library.Video{
		ID:       "local_" + hashStr(path),
		Site:     "Local",
		Title:    cleanTitle(filepath.Base(path)),
		Uploader: model,
		Models:   models,
		Duration: dur, Width: w, Height: h,
		Ext:       strings.TrimPrefix(filepath.Ext(path), "."),
		Filepath:  path,
		Filename:  filepath.Base(path),
		Thumbnail: thumb,
		Filesize:  size,
		Added:     time.Now().Format("2006-01-02 15:04:05"),
	}
	return d.db.Upsert(v) == nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func removeStr(s []string, v string) []string {
	for i, x := range s {
		if x == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}
