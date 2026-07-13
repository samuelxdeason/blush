package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"media-vault/internal/core"
	"media-vault/internal/downloader"
	"media-vault/internal/library"
	"media-vault/internal/server"
)

// App is the thin desktop shell. The catalogue, downloads, and media serving all
// live in core and are exposed over an in-process HTTP server that the web UI
// talks to (same code path as the headless daemon). App keeps only the operations
// that need the native OS: file dialogs, revealing files, and relaunching.
type App struct {
	ctx      context.Context
	core     *core.Core
	apiBase  string // http://127.0.0.1:<port>, served in-process for the web UI
	mediaRot string // resolved vault path
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.mediaRot = core.ResolveRoot("")

	hub := server.NewHub()
	c, err := core.New(a.mediaRot, hub.Broadcast)
	if err != nil {
		runtime.LogError(ctx, "open vault: "+err.Error())
		return
	}
	a.core = c

	// Serve the API + media + events on a random localhost port; the web UI (loaded
	// by Wails from embedded assets) calls this base. ui is nil — Wails serves the UI.
	srv := server.New(c, hub, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		runtime.LogError(ctx, "listen: "+err.Error())
		return
	}
	a.apiBase = fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	go func() { _ = http.Serve(ln, srv.Handler()) }()

	// Drag-and-drop: forward dropped local paths to the UI (via the same SSE hub
	// as every other event), which imports them.
	runtime.OnFileDrop(ctx, func(_, _ int, paths []string) {
		hub.Broadcast("filedrop", paths)
	})
}

// ---- bindings the web UI calls only when running inside the desktop shell ----

// APIBase is the base URL of the in-process server (the web UI's data path).
func (a *App) APIBase() string { return a.apiBase }

// ChooseMediaRoot opens a folder picker and saves the choice (next-launch).
func (a *App) ChooseMediaRoot() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose your Media Vault folder"})
	if err != nil || strings.TrimSpace(dir) == "" {
		return a.mediaRot
	}
	_ = core.SaveRoot(dir)
	return dir
}

// RestartApp relaunches the app after a short delay (so the window profile frees).
func (a *App) RestartApp() {
	exe, _ := os.Executable()
	_ = exec.Command("cmd", "/c",
		fmt.Sprintf(`timeout /t 2 /nobreak >nul & start "" "%s"`, exe)).Start()
	runtime.Quit(a.ctx)
}

// ConnectCookies imports a cookies.txt via a file dialog and merges it into the vault.
func (a *App) ConnectCookies() core.CookieStatus {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Choose a cookies.txt exported from x.com or pornhub.com",
		Filters: []runtime.FileFilter{{DisplayName: "Cookies file (*.txt)", Pattern: "*.txt"}},
	})
	if err != nil || path == "" {
		return a.core.CookieStatus()
	}
	return a.core.MergeCookiesFromFile(path)
}

// ImportFilesDialog opens a multi-file picker and imports the chosen media.
func (a *App) ImportFilesDialog(model string) {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Choose videos to import",
		Filters: []runtime.FileFilter{{DisplayName: "Videos", Pattern: "*.mp4;*.mkv;*.webm;*.mov;*.m4v;*.avi;*.ts"}},
	})
	if err == nil && len(paths) > 0 {
		a.core.Import(paths, model)
	}
}

// ImportFolderDialog opens a folder picker; the folder name becomes the model.
func (a *App) ImportFolderDialog() {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose a folder of videos to import"})
	if err == nil && dir != "" {
		a.core.Import([]string{dir}, "")
	}
}

// ImportPhotosDialog opens an image picker and attaches the chosen photos to model.
func (a *App) ImportPhotosDialog(model string) {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Choose photos to add",
		Filters: []runtime.FileFilter{{DisplayName: "Images", Pattern: "*.jpg;*.jpeg;*.png;*.webp;*.gif;*.bmp"}},
	})
	if err == nil && len(paths) > 0 {
		a.core.Import(paths, model)
	}
}

// Import catalogues local file/folder paths under model (used by drag-and-drop).
func (a *App) Import(paths []string, model string) { a.core.Import(paths, model) }

// boundTypes exists only so Wails emits TypeScript definitions for the models the
// web UI consumes over HTTP. Wails can't see the HTTP API, so without this the
// generated models.ts would omit these types. Never called at runtime.
type boundTypes struct {
	Video      library.Video      `json:"video"`
	Model      library.Model      `json:"model"`
	Photo      library.Photo      `json:"photo"`
	ModelInfo  library.ModelInfo  `json:"modelInfo"`
	LabelCount library.LabelCount `json:"labelCount"`
	Stats      library.Stats      `json:"stats"`
	Collection library.Collection `json:"collection"`
	Job        downloader.Job     `json:"job"`
	RemoteItem downloader.RemoteItem `json:"remoteItem"`
}

// Types is a binding stub that keeps boundTypes (and its field types) in the
// generated TypeScript. Never call it.
func (a *App) Types() boundTypes { return boundTypes{} }

// OpenFolder reveals a file or folder in Explorer.
func (a *App) OpenFolder(path string) {
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		path = filepath.Dir(path)
	}
	_ = exec.Command("explorer", path).Start()
}
