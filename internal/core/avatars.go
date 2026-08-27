package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// avatarsDir holds model profile pictures, kept with the rest of the vault state.
func (c *Core) avatarsDir() string { return filepath.Join(c.stateDir, "avatars") }

// saveAvatar writes image bytes into .trove/avatars and points the model's
// cover at them. The filename carries a timestamp so the path changes on every
// update — otherwise the browser's immutable image cache would show the old one.
func (c *Core) saveAvatar(name string, data []byte, ext string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("model name required")
	}
	if len(data) == 0 {
		return fmt.Errorf("empty image")
	}
	if ext == "" {
		ext = ".jpg"
	}
	dir := c.avatarsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Drop any previous avatar(s) for this model so they don't pile up.
	if old, _ := filepath.Glob(filepath.Join(dir, sanitize(name)+"-*")); old != nil {
		for _, f := range old {
			_ = os.Remove(f)
		}
	}
	p := filepath.Join(dir, sanitize(name)+"-"+strconv.FormatInt(time.Now().Unix(), 36)+ext)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	return c.db.SetModelCover(name, p)
}

// SetAvatarFromURL downloads an image and sets it as the model's avatar.
func (c *Core) SetAvatarFromURL(name, url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("url required")
	}
	data, ext, err := c.downloadImage(url)
	if err != nil {
		return err
	}
	return c.saveAvatar(name, data, ext)
}

// SetAvatarFromData stores an uploaded image as the model's avatar.
func (c *Core) SetAvatarFromData(name, filename string, r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".jpg"
	}
	return c.saveAvatar(name, data, ext)
}

// FetchAvatarFor finds a model's Pornhub profile page and sets their avatar from
// its og:image. Returns whether an avatar was actually set.
func (c *Core) FetchAvatarFor(name string) (bool, error) {
	for _, prof := range c.pornhubProfileCandidates(name) {
		img := c.ogImage(prof)
		if img == "" {
			continue
		}
		data, ext, err := c.downloadImage(img)
		if err != nil || len(data) == 0 {
			continue
		}
		if err := c.saveAvatar(name, data, ext); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// FetchAllAvatars fetches Pornhub avatars in the background for every model that
// doesn't already have a custom one, emitting "avatar" progress events.
func (c *Core) FetchAllAvatars() {
	go func() {
		models, err := c.db.Models()
		if err != nil {
			return
		}
		var todo []string
		for _, m := range models {
			if m.Name == "" {
				continue
			}
			info, _ := c.db.GetModelInfo(m.Name)
			if strings.TrimSpace(info.Cover) != "" {
				continue // keep avatars the user (or a prior run) already set
			}
			todo = append(todo, m.Name)
		}
		total := len(todo)
		added := 0
		for i, name := range todo {
			if ok, _ := c.FetchAvatarFor(name); ok {
				added++
			}
			c.emit("avatar", map[string]any{"done": i + 1, "total": total, "name": name, "added": added})
			time.Sleep(400 * time.Millisecond) // be gentle on Pornhub
		}
		c.emit("avatar", map[string]any{"done": total, "total": total, "added": added, "finished": true})
	}()
}

// pornhubProfileCandidates builds likely Pornhub profile URLs for a model, from a
// representative video's uploader_id (.info.json sidecar) plus the name as slug.
func (c *Core) pornhubProfileCandidates(name string) []string {
	const base = "https://www.pornhub.com"
	var uid string
	if vids, err := c.db.VideosByModel(name); err == nil {
		for _, v := range vids {
			if v.Site != "PornHub" {
				continue
			}
			stem := strings.TrimSuffix(v.Filepath, filepath.Ext(v.Filepath))
			if id := readUploaderID(stem + ".info.json"); id != "" {
				uid = id
				break
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	if uid != "" {
		if strings.HasPrefix(uid, "/") {
			add(base + uid)
		} else {
			add(base + "/pornstar/" + uid)
			add(base + "/model/" + uid)
			add(base + "/channels/" + uid)
		}
	}
	if slug := slugify(name); slug != "" {
		add(base + "/pornstar/" + slug)
		add(base + "/model/" + slug)
	}
	return out
}

func readUploaderID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var d struct {
		UploaderID string `json:"uploader_id"`
	}
	_ = json.Unmarshal(data, &d)
	return strings.TrimSpace(d.UploaderID)
}

// slugify turns a display name into a URL slug (lowercase, words joined by "-").
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_':
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// downloadImage fetches an image with browser headers (and Pornhub cookies for
// Pornhub URLs, which 403 plain requests).
func (c *Core) downloadImage(url string) ([]byte, string, error) {
	req, _ := http.NewRequest("GET", strings.TrimSpace(url), nil)
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*")
	if strings.Contains(strings.ToLower(url), "pornhub") {
		req.Header.Set("Referer", "https://www.pornhub.com/")
		if ck := c.siteCookieHeader("pornhub"); ck != "" {
			req.Header.Set("Cookie", ck)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image fetch failed: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, "", err
	}
	ext := ".jpg"
	switch ct := resp.Header.Get("Content-Type"); {
	case strings.Contains(ct, "png"):
		ext = ".png"
	case strings.Contains(ct, "webp"):
		ext = ".webp"
	case strings.Contains(ct, "gif"):
		ext = ".gif"
	}
	return data, ext, nil
}
