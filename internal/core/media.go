package core

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/133.0 Safari/537.36"

var ogImageRe = regexp.MustCompile(`<meta\s+property="og:image"\s+content="([^"]+)"`)

func (c *Core) cookiesPath() string { return filepath.Join(c.stateDir, "cookies.txt") }

// CookieStatus reports which services have login cookies connected.
type CookieStatus struct {
	X       bool `json:"x"`
	Pornhub bool `json:"pornhub"`
}

func (c *Core) CookieStatus() CookieStatus {
	data, _ := os.ReadFile(c.cookiesPath())
	s := string(data)
	return CookieStatus{
		X:       strings.Contains(s, "x.com") || strings.Contains(s, "twitter.com"),
		Pornhub: strings.Contains(s, "pornhub.com"),
	}
}

// MergeCookiesFromData merges an incoming Netscape cookie file into the vault's
// cookies.txt (de-duped by domain+name, incoming wins) and re-points the
// downloader at the merged file. Returns the new status.
func (c *Core) MergeCookiesFromData(incoming string) CookieStatus {
	existing, _ := os.ReadFile(c.cookiesPath())
	merged := mergeCookies(string(existing), incoming)
	// Write atomically (temp + rename) so an interrupted write can't leave a
	// zeroed/corrupt cookies.txt that breaks every download.
	tmp := c.cookiesPath() + ".tmp"
	if os.WriteFile(tmp, []byte(merged), 0o644) == nil && os.Rename(tmp, c.cookiesPath()) == nil {
		c.dl.SetCookieSpec("file:" + c.cookiesPath())
	}
	return c.CookieStatus()
}

// MergeCookiesFromFile is MergeCookiesFromData reading from a path (desktop dialog).
func (c *Core) MergeCookiesFromFile(path string) CookieStatus {
	data, err := os.ReadFile(path)
	if err != nil {
		return c.CookieStatus()
	}
	return c.MergeCookiesFromData(string(data))
}

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
	add(incoming)
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	for _, k := range order {
		b.WriteString(seen[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// siteCookieHeader builds a "name=value; …" Cookie header from the vault's
// cookies.txt for the given site substring (e.g. "pornhub").
func (c *Core) siteCookieHeader(site string) string {
	data, _ := os.ReadFile(c.cookiesPath())
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

// ServeMedia streams a file under the media root with full HTTP range support
// (so video scrubbing works without buffering the whole file). The path comes
// from the ?p= query and must resolve inside the media root.
func (c *Core) ServeMedia(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	if p == "" {
		http.NotFound(w, r)
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil || !strings.HasPrefix(strings.ToLower(abs), strings.ToLower(c.mediaRoot)) {
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
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	// Set the Content-Type explicitly: ServeContent's fallback consults the OS
	// mime registry, which some devices/installs map wrongly — and a video served
	// as application/octet-stream won't play on phones at all.
	switch ext := strings.ToLower(filepath.Ext(abs)); ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	case ".mp4", ".m4v":
		w.Header().Set("Content-Type", "video/mp4")
	case ".webm":
		w.Header().Set("Content-Type", "video/webm")
	case ".mov":
		w.Header().Set("Content-Type", "video/quicktime")
	case ".mkv":
		w.Header().Set("Content-Type", "video/x-matroska")
	}
	http.ServeContent(w, r, filepath.Base(abs), st.ModTime(), f)
}

// ServeRemoteThumb proxies a Pornhub video's poster image (via its og:image) so
// the Sync grid can show thumbnails that always load and get cached.
func (c *Core) ServeRemoteThumb(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get("v")
	if v == "" || !strings.Contains(strings.ToLower(v), "pornhub.com") {
		http.NotFound(w, r)
		return
	}
	img := c.ogImage(v)
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

// ogImage fetches a page's og:image URL, reading only the document head and
// caching the result (Pornhub 403s plain requests, so send browser headers +
// the logged-in cookies).
func (c *Core) ogImage(pageURL string) string {
	c.ogMu.Lock()
	if v, ok := c.ogCache[pageURL]; ok {
		c.ogMu.Unlock()
		return v
	}
	c.ogMu.Unlock()

	req, _ := http.NewRequest("GET", pageURL, nil)
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if ck := c.siteCookieHeader("pornhub"); ck != "" {
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
	if img != "" {
		c.ogMu.Lock()
		c.ogCache[pageURL] = img
		c.ogMu.Unlock()
	}
	return img
}

// ImportUpload saves an uploaded file into the media root under model and
// catalogues it (the headless equivalent of the desktop import dialogs).
func (c *Core) ImportUpload(filename, model string, r io.Reader) error {
	folder := strings.TrimSpace(model)
	if folder == "" {
		folder = "Unassigned"
	}
	destDir := filepath.Join(c.mediaRoot, "Local", sanitize(folder), "uploads")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(destDir, filepath.Base(filename))
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	out.Close()
	c.Import([]string{dest}, model) // reuse the catalogue/ingest path
	return nil
}

func sanitize(s string) string {
	s = strings.TrimSpace(s)
	for _, ch := range `<>:"/\|?*` {
		s = strings.ReplaceAll(s, string(ch), "_")
	}
	return s
}
