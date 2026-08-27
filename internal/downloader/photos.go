// Photo-gallery downloading: pull every image in a web gallery page into the
// vault, catalogued under a model + album. Two engines, tried in order:
//
//  1. gallery-dl (the yt-dlp of image sites) when installed — it knows
//     hundreds of sites' layouts and solves e.g. Pornhub's JS-guarded albums
//     (with the vault's cookies.txt).
//  2. A generic scrape of the page: collect links that point at real image
//     files, download them with a browser UA + the page as Referer, and keep
//     everything that is actually a substantial image (thumbnails and site
//     chrome get filtered out).
package downloader

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"trove/internal/library"
)

const photoUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// ImportPhotosFromURL downloads a web gallery into model's album in the
// background; progress arrives on the existing "import" event stream.
func (d *Downloader) ImportPhotosFromURL(pageURL, model, album string) {
	go d.importPhotosURLWork(pageURL, model, album)
}

func (d *Downloader) importPhotosURLWork(pageURL, model, album string) {
	pageURL = strings.TrimSpace(pageURL)
	if album = strings.TrimSpace(album); album == "" {
		if u, err := url.Parse(pageURL); err == nil {
			album = strings.TrimPrefix(u.Hostname(), "www.")
		}
	}
	urls := d.galleryDLURLs(pageURL)
	if len(urls) == 0 {
		urls = scrapeGalleryImages(pageURL)
	}
	total := len(urls)
	if total == 0 {
		d.emit("import", map[string]any{"done": 0, "total": 0, "added": 0, "finished": true})
		return
	}
	client := &http.Client{Timeout: 90 * time.Second}
	added := 0
	for i, u := range urls {
		if d.downloadPhotoOne(client, u, pageURL, model, album) {
			added++
		}
		d.emit("import", map[string]any{"done": i + 1, "total": total, "name": photoBaseName(u)})
	}
	d.emit("import", map[string]any{"done": total, "total": total, "added": added, "finished": true})
}

// galleryDLURLs asks gallery-dl for the gallery's direct image URLs. Returns
// nil when gallery-dl is missing or doesn't understand the site.
func (d *Downloader) galleryDLURLs(pageURL string) []string {
	var argv []string
	if p, err := exec.LookPath("gallery-dl"); err == nil {
		argv = []string{p}
	} else if py, err := exec.LookPath("python"); err == nil {
		argv = []string{py, "-m", "gallery_dl"}
	} else {
		return nil
	}
	if spec := d.cfg.CookieSpec; strings.HasPrefix(spec, "file:") {
		argv = append(argv, "--cookies", strings.TrimPrefix(spec, "file:"))
	}
	argv = append(argv, "-g", pageURL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := c.Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	var urls []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "| "))
		if strings.HasPrefix(ln, "http://") || strings.HasPrefix(ln, "https://") {
			urls = append(urls, ln)
		}
	}
	return urls
}

// imageLinkRe matches href/src/data-src attribute values.
var imageLinkRe = regexp.MustCompile(`(?:href|src|data-src)\s*=\s*"([^"]+)"`)

// looksLikeImage: URL path ends in an image extension (query strings and
// trailing slashes allowed — e.g. xxbrits' /get_image/…/file.jpg/?token=…).
var looksLikeImage = regexp.MustCompile(`(?i)\.(jpe?g|png|webp|gif)([/?#]|$)`)

// junkImage: site chrome and thumbnails, not gallery content.
var junkImage = regexp.MustCompile(`(?i)logo|sprite|avatar|/preview/|thumb|icon|emoji|banner|/static/|placeholder`)

// scrapeGalleryImages fetches the page and returns the plausible full-size
// image URLs it links to.
func scrapeGalleryImages(pageURL string) []string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: 45 * time.Second}
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", photoUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	seen := map[string]bool{}
	var out []string
	for _, m := range imageLinkRe.FindAllStringSubmatch(string(body), -1) {
		raw := strings.TrimSpace(m[1])
		if raw == "" || strings.HasPrefix(raw, "data:") {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref).String()
		if !looksLikeImage.MatchString(abs) || junkImage.MatchString(abs) || seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
		if len(out) >= 500 {
			break
		}
	}
	return out
}

// downloadPhotoOne fetches one image and catalogues it. Small responses are
// discarded — a real gallery photo is never a 10KB thumbnail.
func (d *Downloader) downloadPhotoOne(client *http.Client, imgURL, referer, model, album string) bool {
	req, err := http.NewRequest("GET", imgURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", photoUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != 200 || (ct != "" && !strings.HasPrefix(ct, "image/")) {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil || len(data) < 25<<10 {
		return false
	}
	ext := extFromImage(ct, imgURL)
	id := "photo_" + hashStr(imgURL)
	dest := d.writeVaultFile(library.FlatBase("photo", id)+ext, data)
	if dest == "" {
		return false
	}
	return d.db.AddPhoto(library.Photo{
		ID:       id,
		Model:    model,
		Album:    album,
		Filepath: dest,
		Filename: photoBaseName(imgURL),
		Added:    time.Now().Format("2006-01-02 15:04:05"),
	}) == nil
}

// writeVaultFile stores data in the media folder (no-op if already present).
func (d *Downloader) writeVaultFile(name string, data []byte) string {
	dir := d.mediaDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	dest := filepath.Join(dir, name)
	if !fileExists(dest) {
		if os.WriteFile(dest, data, 0o644) != nil {
			return ""
		}
	}
	return dest
}

func extFromImage(contentType, imgURL string) string {
	switch {
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	case strings.Contains(contentType, "jpeg"):
		return ".jpg"
	}
	if m := looksLikeImage.FindStringSubmatch(imgURL); m != nil {
		e := strings.ToLower(m[1])
		if e == "jpeg" {
			e = "jpg"
		}
		return "." + e
	}
	return ".jpg"
}

// photoBaseName extracts a readable filename from an image URL.
func photoBaseName(imgURL string) string {
	u, err := url.Parse(imgURL)
	if err != nil {
		return "photo"
	}
	p := strings.TrimSuffix(u.Path, "/")
	// xxbrits-style ".../file.jpg/?token": the extension segment may not be last
	for _, seg := range []string{path.Base(p), path.Base(path.Dir(p))} {
		if looksLikeImage.MatchString(seg) {
			return seg
		}
	}
	if b := path.Base(p); b != "" && b != "." {
		return b
	}
	return "photo"
}
