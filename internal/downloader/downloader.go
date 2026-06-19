// Package downloader drives yt-dlp.exe through a sequential queue, captures
// metadata into the library, and reports progress to the UI.
package downloader

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"media-vault/internal/library"
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
	queue chan string
	mu    sync.Mutex
}

func New(cfg Config, db *library.DB, emit func(string, any)) *Downloader {
	d := &Downloader{
		cfg:   cfg,
		db:    db,
		emit:  emit,
		jobs:  map[string]*Job{},
		queue: make(chan string, 256),
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
func (d *Downloader) env() []string {
	dir := filepath.Dir(d.cfg.YtDlp)
	var out []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			out = append(out, e)
		}
	}
	return append(out, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (d *Downloader) cookieArgs() []string {
	d.mu.Lock()
	spec := d.cfg.CookieSpec
	d.mu.Unlock()
	switch {
	case strings.HasPrefix(spec, "file:"):
		return []string{"--cookies", strings.TrimPrefix(spec, "file:")}
	case strings.HasPrefix(spec, "browser:"):
		return []string{"--cookies-from-browser", strings.TrimPrefix(spec, "browser:")}
	}
	return nil
}

// Enumerate lists the videos in a model/playlist/favorites URL (fast, metadata
// only) and flags which you already own.
func (d *Downloader) Enumerate(listURL string) ([]RemoteItem, error) {
	listURL = strings.TrimSpace(listURL)
	if listURL == "" {
		return nil, nil
	}
	args := []string{"--ignore-config", "--flat-playlist", "--no-warnings",
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
	owned := d.archiveSet()
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
		id := viewkey(url)
		items = append(items, RemoteItem{
			URL: url, Title: title, ID: id,
			Owned: id != "" && owned["pornhub "+id],
		})
	}
	return items, nil
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

func newID() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

// Enqueue adds a URL to the queue and returns its job id.
func (d *Downloader) Enqueue(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	id := newID()
	d.mu.Lock()
	d.jobs[id] = &Job{ID: id, URL: url, Title: url, Status: "queued"}
	d.order = append(d.order, id)
	d.mu.Unlock()
	d.queue <- id
	d.emitQueue()
	return id
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
	for id := range d.queue {
		d.mu.Lock()
		j, ok := d.jobs[id]
		ready := ok && j.Status == "queued"
		d.mu.Unlock()
		if ready {
			d.run(j)
		}
	}
}

func (d *Downloader) run(j *Job) {
	d.mu.Lock()
	j.Status = "downloading"
	cfg := d.cfg
	d.mu.Unlock()
	d.emitQueue()

	outtmpl := filepath.Join(cfg.MediaRoot,
		"%(extractor_key)s", "%(uploader,uploader_id)s",
		"%(uploader,uploader_id)s [%(id)s].%(ext)s")

	args := []string{
		"--ignore-config",
		"-f", "bestvideo+bestaudio/best",
		"--merge-output-format", "mp4",
		"-o", outtmpl,
		"--download-archive", cfg.Archive,
		"--write-info-json",
		"--write-thumbnail",
		"--no-warnings", "--newline", "--no-simulate",
		"--progress-template",
		"download:[[PROG]]%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s",
		"--print", "after_move:[[DONE]]%(filepath)s",
	}
	if cfg.FfmpegDir != "" {
		args = append(args, "--ffmpeg-location", cfg.FfmpegDir)
	}
	switch {
	case strings.HasPrefix(cfg.CookieSpec, "browser:"):
		args = append(args, "--cookies-from-browser", strings.TrimPrefix(cfg.CookieSpec, "browser:"))
	case strings.HasPrefix(cfg.CookieSpec, "file:"):
		args = append(args, "--cookies", strings.TrimPrefix(cfg.CookieSpec, "file:"))
	}
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
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "[[PROG]]"):
			d.applyProgress(j, line[strings.Index(line, "[[PROG]]")+8:])
		case strings.Contains(line, "[[DONE]]"):
			saved = append(saved, strings.TrimSpace(line[strings.Index(line, "[[DONE]]")+8:]))
		case strings.Contains(line, "ERROR:"):
			lastErr = line // errors still print even in --print/quiet mode
		}
	}
	d.finish(j, saved, cmd.Wait(), lastErr)
}

func (d *Downloader) applyProgress(j *Job, payload string) {
	parts := strings.SplitN(payload, "|", 3)
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

// ingest reads the sidecar metadata for a downloaded file and stores it.
func (d *Downloader) ingest(filepathStr string) (library.Video, bool) {
	stem := strings.TrimSuffix(filepathStr, filepath.Ext(filepathStr))
	info := ytInfo{}
	if data, err := os.ReadFile(stem + ".info.json"); err == nil {
		_ = json.Unmarshal(data, &info)
	}
	if info.ID == "" {
		return library.Video{}, false
	}

	uploader := firstNonEmpty(info.Uploader, info.UploaderID, info.Channel)
	thumb := findThumb(stem)
	if thumb == "" {
		thumb = d.makeThumb(filepathStr, stem, info.Duration) // ffmpeg fallback
	}
	v := library.Video{
		ID:           info.ID,
		Site:         info.ExtractorKey,
		Title:        firstNonEmpty(info.Title, uploader),
		Uploader:     uploader,
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
