package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"media-vault/internal/downloader"
	"media-vault/internal/library"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/133.0 Safari/537.36"

var (
	ogImageRe = regexp.MustCompile(`<meta\s+property="og:image"\s+content="([^"]+)"`)
	ogCache   = map[string]string{}
	ogMu      sync.Mutex
)

// ogImage fetches a page's og:image URL, reading only the document head and
// caching the result. Pornhub's anti-bot 403s plain requests, so we send
// browser headers + the logged-in cookies (same as a real browser would).
func (a *App) ogImage(pageURL string) string {
	ogMu.Lock()
	if v, ok := ogCache[pageURL]; ok {
		ogMu.Unlock()
		return v
	}
	ogMu.Unlock()

	req, _ := http.NewRequest("GET", pageURL, nil)
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if ck := a.siteCookieHeader("pornhub"); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	img := ""
	if resp, err := http.DefaultClient.Do(req); err == nil {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if m := ogImageRe.FindStringSubmatch(string(head)); len(m) > 1 {
			img = m[1]
		}
	}
	if img != "" { // don't cache transient failures
		ogMu.Lock()
		ogCache[pageURL] = img
		ogMu.Unlock()
	}
	return img
}

// siteCookieHeader builds a "name=value; …" Cookie header from the vault's
// cookies.txt for the given site substring (e.g. "pornhub").
func (a *App) siteCookieHeader(site string) string {
	data, _ := os.ReadFile(a.cookiesPath())
	var parts []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(ln, "#") || strings.TrimSpace(ln) == "" {
			continue
		}
		f := strings.Split(strings.TrimRight(ln, "\r"), "\t")
		if len(f) >= 7 && strings.Contains(f[0], site) {
			parts = append(parts, f[5]+"="+f[6])
		}
	}
	return strings.Join(parts, "; ")
}

// defaultMediaRoot is used until the user picks a location in Settings.
const defaultMediaRoot = `D:\MediaVault`

// appConfig persists settings (the media root) OUTSIDE the vault, so the vault
// itself can move freely to another drive.
type appConfig struct {
	MediaRoot string `json:"mediaRoot"`
}

func configPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "MediaVault", "config.json")
}

func loadConfig() appConfig {
	var c appConfig
	if data, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if strings.TrimSpace(c.MediaRoot) == "" {
		c.MediaRoot = defaultMediaRoot
	}
	return c
}

func saveConfig(c appConfig) {
	_ = os.MkdirAll(filepath.Dir(configPath()), 0o755)
	if data, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(configPath(), data, 0o644)
	}
}

// App is the single object the UI talks to. Its exported methods are callable
// directly from the frontend (no network/API in between).
type App struct {
	ctx       context.Context
	db        *library.DB
	dl        *downloader.Downloader
	mediaRoot string
}

func NewApp() *App { return &App{mediaRoot: loadConfig().MediaRoot} }

// ---- settings + storage (called from the UI) ---------------------------

// MediaRootPath returns the current vault location.
func (a *App) MediaRootPath() string { return a.mediaRoot }

// ChooseMediaRoot opens a folder picker, saves the chosen location, and returns
// it. Takes effect on next launch (call RestartApp).
func (a *App) ChooseMediaRoot() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose your Media Vault folder",
	})
	if err != nil || strings.TrimSpace(dir) == "" {
		return a.mediaRoot
	}
	saveConfig(appConfig{MediaRoot: dir})
	return dir
}

// RestartApp relaunches the app (after a short delay so this instance fully
// exits and releases its window profile first).
func (a *App) RestartApp() {
	exe, _ := os.Executable()
	_ = exec.Command("cmd", "/c",
		fmt.Sprintf(`timeout /t 2 /nobreak >nul & start "" "%s"`, exe)).Start()
	runtime.Quit(a.ctx)
}

// Stats returns the storage breakdown for the Settings page.
func (a *App) Stats() (library.Stats, error) { return a.db.Stats() }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	_ = os.MkdirAll(a.mediaRoot, 0o755)

	db, err := library.Open(filepath.Join(a.mediaRoot, "library.db"), a.mediaRoot)
	if err != nil {
		runtime.LogError(ctx, "open library: "+err.Error())
		return
	}
	a.db = db

	// First run: import the existing library.json once.
	if n, _ := db.Count(); n == 0 {
		jsonPath := filepath.Join(a.mediaRoot, "library.json")
		if _, statErr := os.Stat(jsonPath); statErr == nil {
			if imported, mErr := db.MigrateFromJSON(jsonPath); mErr == nil {
				runtime.LogInfo(ctx, "migrated library.json: "+itoa(imported))
			}
		}
	}

	emit := func(event string, data any) { runtime.EventsEmit(ctx, event, data) }
	a.dl = downloader.New(downloader.Config{
		YtDlp:     ytdlpPath(),
		FfmpegDir: ffmpegDir(),
		MediaRoot: a.mediaRoot,
		Archive:   filepath.Join(a.mediaRoot, "downloaded.archive"),
	}, db, emit)

	// Auto-use a cookies.txt sitting in the vault, so X/protected downloads work
	// without any setup once cookies are connected.
	if _, err := os.Stat(a.cookiesPath()); err == nil {
		a.dl.SetCookieSpec("file:" + a.cookiesPath())
	}
}

func (a *App) cookiesPath() string { return filepath.Join(a.mediaRoot, "cookies.txt") }

// CookieStatus reports which services have login cookies connected.
type CookieStatus struct {
	X       bool `json:"x"`
	Pornhub bool `json:"pornhub"`
}

func (a *App) CookieStatus() CookieStatus {
	data, _ := os.ReadFile(a.cookiesPath())
	s := string(data)
	return CookieStatus{
		X:       strings.Contains(s, "x.com") || strings.Contains(s, "twitter.com"),
		Pornhub: strings.Contains(s, "pornhub.com"),
	}
}

// ConnectCookies imports a cookies.txt and MERGES it into the vault's cookie
// file, so X and Pornhub logins can coexist in one auto-loaded file.
func (a *App) ConnectCookies() CookieStatus {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Choose a cookies.txt exported from x.com or pornhub.com",
		Filters: []runtime.FileFilter{{DisplayName: "Cookies file (*.txt)", Pattern: "*.txt"}},
	})
	if err != nil || path == "" {
		return a.CookieStatus()
	}
	if data, e := os.ReadFile(path); e == nil {
		existing, _ := os.ReadFile(a.cookiesPath())
		merged := mergeCookies(string(existing), string(data))
		if os.WriteFile(a.cookiesPath(), []byte(merged), 0o644) == nil {
			a.dl.SetCookieSpec("file:" + a.cookiesPath())
		}
	}
	return a.CookieStatus()
}

// mergeCookies combines two Netscape cookie files, de-duped by domain+name with
// the incoming file winning.
func mergeCookies(existing, incoming string) string {
	type key struct{ domain, name string }
	seen := map[key]string{}
	var order []key
	add := func(text string) {
		for _, ln := range strings.Split(text, "\n") {
			t := strings.TrimRight(ln, "\r")
			if s := strings.TrimSpace(t); s == "" || strings.HasPrefix(s, "#") {
				continue
			}
			f := strings.Split(t, "\t")
			if len(f) < 7 {
				continue
			}
			k := key{f[0], f[5]}
			if _, ok := seen[k]; !ok {
				order = append(order, k)
			}
			seen[k] = t
		}
	}
	add(existing)
	add(incoming) // incoming overwrites same-key cookies
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	for _, k := range order {
		b.WriteString(seen[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// ---- bound methods (called from the UI) --------------------------------

func (a *App) Enqueue(url string) string                          { return a.dl.Enqueue(url) }
func (a *App) Enumerate(url string) ([]downloader.RemoteItem, error) { return a.dl.Enumerate(url) }
func (a *App) Queue() []downloader.Job                             { return a.dl.Snapshot() }
func (a *App) RemoveJob(id string)       { a.dl.RemoveJob(id) }
func (a *App) ClearFinished()            { a.dl.ClearFinished() }
func (a *App) SetCookieSpec(spec string) { a.dl.SetCookieSpec(spec) }

func (a *App) Models() ([]library.Model, error)                      { return a.db.Models() }
func (a *App) Videos(site, uploader string) ([]library.Video, error) { return a.db.Videos(site, uploader) }
func (a *App) VideosBySite(site string) ([]library.Video, error)     { return a.db.VideosBySite(site) }
func (a *App) Search(q string) ([]library.Video, error)              { return a.db.Search(q) }
func (a *App) RecentlyDownloaded() ([]library.Video, error)          { return a.db.RecentlyDownloaded(200) }
func (a *App) RecentlyWatched() ([]library.Video, error)             { return a.db.RecentlyWatched(200) }

// MarkWatched records that a video was just opened (Recently Watched).
func (a *App) MarkWatched(site, id string) {
	_ = a.db.MarkWatched(site, id, time.Now().Format("2006-01-02 15:04:05"))
}

// OpenFolder reveals a file or folder in Explorer.
func (a *App) OpenFolder(path string) {
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		path = filepath.Dir(path)
	}
	_ = exec.Command("explorer", path).Start()
}

// ---- in-process media handler (seekable local playback) ----------------

// mediaHandler serves files under the media root to the webview, with HTTP
// range support so video scrubbing works. Nothing is exposed off-machine.
func (a *App) mediaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rthumb" {
			a.serveRemoteThumb(w, r)
			return
		}
		p := r.URL.Query().Get("p")
		if p == "" {
			http.NotFound(w, r)
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil || !strings.HasPrefix(strings.ToLower(abs), strings.ToLower(a.mediaRoot)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Thumbnails never change once written — let the webview cache them so
		// revisiting a grid is instant instead of re-fetching every image.
		switch strings.ToLower(filepath.Ext(abs)) {
		case ".jpg", ".jpeg", ".png", ".webp":
			w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		}
		http.ServeContent(w, r, filepath.Base(abs), st.ModTime(), f)
	})
}

// serveRemoteThumb proxies a Pornhub video's poster image (via its og:image)
// so the Sync grid can show thumbnails that always load and get cached.
func (a *App) serveRemoteThumb(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get("v")
	if v == "" || !strings.Contains(strings.ToLower(v), "pornhub.com") {
		http.NotFound(w, r)
		return
	}
	img := a.ogImage(v)
	if img == "" {
		http.NotFound(w, r)
		return
	}
	req, _ := http.NewRequest("GET", img, nil)
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,*/*")
	req.Header.Set("Referer", "https://www.pornhub.com/")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = io.Copy(w, resp.Body)
}

// ---- tool discovery -----------------------------------------------------

func ytdlpPath() string {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	for _, c := range []string{
		filepath.Join(dir, "yt-dlp.exe"),
		filepath.Join(dir, "resources", "yt-dlp.exe"),
		filepath.Join("resources", "yt-dlp.exe"), // wails dev: cwd = project root
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p
	}
	return "yt-dlp.exe"
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
