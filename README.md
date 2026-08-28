# Trove

Self-hosted library for media you save from anywhere. A headless Go server
(`troved`) owns the catalogue and serves the HTTP API, SSE events, media, and
the web UI (installable as a PWA); the Wails desktop app is a thin client over
the same engine.

## Running

- **Server:** `go build -o troved.exe ./cmd/troved`, then `troved -addr 0.0.0.0:8899 -ui frontend/dist`
  (or use `start-trove.cmd`, which prints your LAN URL for phones).
- **Desktop app:** `wails dev` for live development, `wails build` for a
  redistributable build (see `wails.json`).

The vault location comes from `-root`, `$TROVE_ROOT`, or the saved config
(`%APPDATA%\Trove\config.json`). App state (catalogue db, sidecars, thumbs)
lives in a hidden `.trove/` folder inside the vault; media files live under
`media/`.
