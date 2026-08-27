// Package server exposes the Media Vault core engine over HTTP: a JSON API for
// the catalogue and downloads, an SSE stream for live events, range-served media,
// and (optionally) the embedded web UI. The desktop app and the headless daemon
// both build their HTTP surface from here.
package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"

	"trove/internal/core"
	"trove/internal/library"
)

// Server wires the core engine and the event hub onto an http.Handler.
type Server struct {
	core *core.Core
	hub  *Hub
	mux  *http.ServeMux
}

// New builds the API + SSE + media handler. If ui is non-nil it is also served
// as the web app at "/" (used by the headless daemon; the desktop app serves the
// UI through Wails instead and passes nil).
func New(c *core.Core, hub *Hub, ui fs.FS) *Server {
	s := &Server{core: c, hub: hub, mux: http.NewServeMux()}
	s.routes(ui)
	return s
}

// Handler returns the API handler wrapped in permissive CORS, so the desktop
// webview (a different origin than the in-process server) and LAN browsers can
// call it. Everything served is local, so a wildcard origin is acceptable.
// TROVE_HTTP_LOG=1 additionally logs every request (with client, Range, and
// User-Agent) — for diagnosing what a phone actually asks for.
func (s *Server) Handler() http.Handler {
	logAll := os.Getenv("TROVE_HTTP_LOG") == "1"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logAll && r.URL.Path != "/api/events" {
			ua := r.UserAgent()
			if len(ua) > 60 {
				ua = ua[:60]
			}
			q := r.URL.RawQuery
			if len(q) > 90 {
				q = q[:90]
			}
			log.Printf("HTTP %s %s %s?%s range=%q ua=%q", r.RemoteAddr, r.Method, r.URL.Path, q, r.Header.Get("Range"), ua)
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) routes(ui fs.FS) {
	m := s.mux

	// --- events + media ---
	m.Handle("GET /api/events", s.hub)
	m.HandleFunc("GET /media", s.core.ServeMedia)

	// Client-side diagnostics beacon: the feed posts its render state here so
	// devices without a devtools console (phones) can be debugged from the
	// server log. Logged only when TROVE_HTTP_LOG=1.
	m.HandleFunc("POST /api/clientlog", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("TROVE_HTTP_LOG") == "1" {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			log.Printf("CLIENT %s %s", r.RemoteAddr, b)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("GET /rthumb", s.core.ServeRemoteThumb)

	// --- catalogue reads ---
	m.HandleFunc("GET /api/models", j(func(_ *http.Request) (any, error) { return s.core.Models() }))
	m.HandleFunc("GET /api/labels", j(func(_ *http.Request) (any, error) { return s.core.AllLabels() }))
	m.HandleFunc("GET /api/labelcounts", j(func(_ *http.Request) (any, error) { return s.core.LabelCounts() }))
	m.HandleFunc("GET /api/favorites", j(func(_ *http.Request) (any, error) { return s.core.Favorites() }))
	m.HandleFunc("GET /api/recent", j(func(_ *http.Request) (any, error) { return s.core.RecentlyDownloaded() }))
	m.HandleFunc("GET /api/watched", j(func(_ *http.Request) (any, error) { return s.core.RecentlyWatched() }))
	m.HandleFunc("GET /api/continue", j(func(_ *http.Request) (any, error) { return s.core.ContinueWatching() }))
	m.HandleFunc("GET /api/stats", j(func(_ *http.Request) (any, error) { return s.core.Stats() }))
	m.HandleFunc("GET /api/cookiestatus", j(func(_ *http.Request) (any, error) { return s.core.CookieStatus(), nil }))
	m.HandleFunc("GET /api/mediaroot", j(func(_ *http.Request) (any, error) { return s.core.MediaRoot(), nil }))
	m.HandleFunc("GET /api/collections", j(func(_ *http.Request) (any, error) { return s.core.Collections() }))
	m.HandleFunc("GET /api/queue", j(func(_ *http.Request) (any, error) { return s.core.Queue(), nil }))

	m.HandleFunc("GET /api/videos", j(func(r *http.Request) (any, error) {
		limit := int(qInt(r, "limit"))
		if limit <= 0 || limit > 500 {
			limit = 200
		}
		return s.core.AllVideos(limit, int(qInt(r, "offset")), q(r, "sort"), q(r, "site"), q(r, "fav") == "1", qInt(r, "seed"))
	}))
	m.HandleFunc("GET /api/videos/by-model", j(func(r *http.Request) (any, error) { return s.core.VideosByModel(q(r, "model")) }))
	m.HandleFunc("GET /api/videos/by-site", j(func(r *http.Request) (any, error) { return s.core.VideosBySite(q(r, "site")) }))
	m.HandleFunc("GET /api/videos/by-label", j(func(r *http.Request) (any, error) { return s.core.VideosByLabel(q(r, "label")) }))
	m.HandleFunc("GET /api/videos/by-collection", j(func(r *http.Request) (any, error) { return s.core.VideosByCollection(qInt(r, "id")) }))
	m.HandleFunc("GET /api/search", j(func(r *http.Request) (any, error) { return s.core.Search(q(r, "q")) }))
	m.HandleFunc("GET /api/photos", j(func(r *http.Request) (any, error) { return s.core.PhotosByModel(q(r, "model")) }))
	m.HandleFunc("GET /api/modelinfo", j(func(r *http.Request) (any, error) { return s.core.GetModelInfo(q(r, "name")) }))
	m.HandleFunc("GET /api/enumerate", j(func(r *http.Request) (any, error) { return s.core.Enumerate(q(r, "url"), q(r, "refresh") == "1") }))
	m.HandleFunc("GET /api/synced", j(func(_ *http.Request) (any, error) { return s.core.SyncedURLs(), nil }))
	m.HandleFunc("GET /api/synced/lists", j(func(_ *http.Request) (any, error) { return s.core.SyncedLists(), nil }))
	m.HandleFunc("GET /api/collections-for-video", j(func(r *http.Request) (any, error) { return s.core.CollectionsForVideo(q(r, "site"), q(r, "id")) }))

	// --- catalogue writes / actions ---
	post(m, "/api/markwatched", func(b body) (any, error) { s.core.MarkWatched(b.Site, b.ID); return ok, nil })
	post(m, "/api/position", func(b body) (any, error) { return ok, s.core.SetPosition(b.Site, b.ID, b.Position, b.Duration) })
	post(m, "/api/setmodels", func(b body) (any, error) { return ok, s.core.SetModels(b.Site, b.ID, b.Models) })
	post(m, "/api/models/unassign", func(b body) (any, error) { return ok, s.core.RemoveModelFromAll(b.Name) })
	post(m, "/api/settitle", func(b body) (any, error) { return ok, s.core.SetTitle(b.Site, b.ID, b.Title) })
	post(m, "/api/setfavorite", func(b body) (any, error) { return ok, s.core.SetFavorite(b.Site, b.ID, b.Fav) })
	post(m, "/api/setlabels", func(b body) (any, error) { return ok, s.core.SetLabels(b.Site, b.ID, b.Labels) })
	post(m, "/api/savemodelinfo", func(b body) (any, error) { return ok, s.core.SaveModelInfo(b.Name, b.Nickname, b.Bio, b.Links) })
	post(m, "/api/model/rename", func(b body) (any, error) { return ok, s.core.RenameModel(b.Name, b.NewName) })
	post(m, "/api/photos/from-url", func(b body) (any, error) { s.core.ImportPhotosFromURL(b.URL, b.Model, b.Name); return ok, nil })
	post(m, "/api/setmodelcover", func(b body) (any, error) { return ok, s.core.SetModelCover(b.Name, b.Cover) })
	post(m, "/api/avatar/url", func(b body) (any, error) { return ok, s.core.SetAvatarFromURL(b.Name, b.URL) })
	post(m, "/api/avatar/fetch", func(b body) (any, error) { set, err := s.core.FetchAvatarFor(b.Name); return map[string]bool{"set": set}, err })
	post(m, "/api/avatars/fetch-all", func(_ body) (any, error) { s.core.FetchAllAvatars(); return ok, nil })
	post(m, "/api/import", func(b body) (any, error) { s.core.Import(b.Paths, b.Model); return ok, nil })
	post(m, "/api/rebuild", func(_ body) (any, error) {
		n, err := s.core.RebuildFromDisk()
		return map[string]int{"count": n}, err
	})
	post(m, "/api/backup", func(_ body) (any, error) {
		p, err := s.core.BackupCatalogue()
		return map[string]string{"path": p}, err
	})
	post(m, "/api/optimize", func(_ body) (any, error) { s.core.OptimizeStreaming(); return ok, nil })

	// --- downloads ---
	post(m, "/api/enqueue", func(b body) (any, error) { return s.core.Enqueue(b.URL), nil })
	post(m, "/api/enqueue/many", func(b body) (any, error) { return map[string]int{"added": s.core.EnqueueMany(b.URLs)}, nil })
	post(m, "/api/job/remove", func(b body) (any, error) { s.core.RemoveJob(b.ID); return ok, nil })
	post(m, "/api/clearfinished", func(_ body) (any, error) { s.core.ClearFinished(); return ok, nil })
	post(m, "/api/synced/remove", func(b body) (any, error) { s.core.RemoveSync(b.URL); return ok, nil })

	// --- collections ---
	post(m, "/api/collections/create", func(b body) (any, error) {
		id, err := s.core.CreateCollection(b.Name, b.Hidden)
		return map[string]int64{"id": id}, err
	})
	post(m, "/api/collections/rename", func(b body) (any, error) { return ok, s.core.RenameCollection(b.ID64, b.Name) })
	post(m, "/api/collections/hidden", func(b body) (any, error) { return ok, s.core.SetCollectionHidden(b.ID64, b.Hidden) })
	post(m, "/api/collections/locked", func(b body) (any, error) { return ok, s.core.SetCollectionLocked(b.ID64, b.Locked) })
	post(m, "/api/collections/delete", func(b body) (any, error) { return ok, s.core.DeleteCollection(b.ID64) })
	post(m, "/api/collections/add", func(b body) (any, error) { return ok, s.core.AddToCollection(b.ID64, b.Site, b.VideoID) })
	post(m, "/api/collections/remove", func(b body) (any, error) { return ok, s.core.RemoveFromCollection(b.ID64, b.Site, b.VideoID) })

	// --- uploads (headless equivalents of the desktop file dialogs) ---
	m.HandleFunc("POST /api/upload", s.handleUpload)
	m.HandleFunc("POST /api/avatar/upload", s.handleAvatarUpload)
	m.HandleFunc("POST /api/cookies/upload", s.handleCookieUpload)

	// --- web UI (headless only) ---
	if ui != nil {
		m.Handle("/", spaFileServer(ui))
	}
}

// body is the union of every POST payload; each handler reads the fields it needs.
type body struct {
	URL     string             `json:"url"`
	Site    string             `json:"site"`
	ID      string             `json:"id"`
	VideoID string             `json:"videoId"`
	Title   string             `json:"title"`
	Name    string             `json:"name"`
	Bio     string             `json:"bio"`
	Cover   string             `json:"cover"`
	Model   string             `json:"model"`
	Fav      bool              `json:"fav"`
	Position float64           `json:"position"`
	Duration float64           `json:"duration"`
	Hidden  bool               `json:"hidden"`
	Locked  bool               `json:"locked"`
	ID64    int64               `json:"id64"`
	Models  []string            `json:"models"`
	Labels  []string            `json:"labels"`
	Paths   []string            `json:"paths"`
	URLs    []string            `json:"urls"`
	Links   []library.ModelLink `json:"links"`
	Nickname string             `json:"nickname"`
	NewName  string             `json:"newName"`
}

var ok = map[string]bool{"ok": true}

// j adapts a read handler that returns (value, error) into an http.HandlerFunc.
func j(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		writeJSON(w, v, err)
	}
}

// post registers a JSON-body POST handler.
func post(m *http.ServeMux, path string, fn func(body) (any, error)) {
	m.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		var b body
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&b) // empty body is fine
		}
		v, err := fn(b)
		writeJSON(w, v, err)
	})
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if v == nil {
		v = ok
	}
	_ = json.NewEncoder(w).Encode(v)
}

func q(r *http.Request, key string) string { return r.URL.Query().Get(key) }

func qInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}

// handleUpload accepts a multipart file plus an optional model field and imports it.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	model := r.FormValue("model")
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	if err := s.core.ImportUpload(hdr.Filename, model, file); err != nil {
		writeJSON(w, nil, err)
		return
	}
	writeJSON(w, ok, nil)
}

// handleAvatarUpload accepts a multipart image + model name and sets it as the avatar.
func (s *Server) handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	if err := s.core.SetAvatarFromData(name, hdr.Filename, file); err != nil {
		writeJSON(w, nil, err)
		return
	}
	writeJSON(w, ok, nil)
}

// handleCookieUpload merges an uploaded cookies.txt into the vault.
func (s *Server) handleCookieUpload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.core.MergeCookiesFromData(string(data)), nil)
}

// spaFileServer serves static files from ui, falling back to index.html for
// client-side routes (single-page app).
func spaFileServer(ui fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(ui, trimLeadingSlash(r.URL.Path)); err != nil && r.URL.Path != "/" {
			r2 := *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "" {
		return "."
	}
	return p
}
