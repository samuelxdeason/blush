// Command mediavaultd is the headless Media Vault server: it runs the core
// engine and exposes the HTTP API, SSE events, range-served media, and the web
// UI on one port — suitable for running on a NAS or any always-on machine.
package main

import (
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"media-vault/internal/core"
	"media-vault/internal/server"
)

func main() {
	// Subcommand: `mediavaultd migrate-flat` converts the vault to the flat
	// media/ layout (dry run unless --apply). Stop the daemon before applying.
	if len(os.Args) > 1 && os.Args[1] == "migrate-flat" {
		fs := flag.NewFlagSet("migrate-flat", flag.ExitOnError)
		root := fs.String("root", "", "media vault directory (else $MEDIAVAULT_ROOT, saved config, or a default under home)")
		apply := fs.Bool("apply", false, "execute the migration (default is a dry run that only writes a manifest)")
		rollback := fs.String("rollback", "", "journal file from a previous --apply to roll back")
		_ = fs.Parse(os.Args[2:])
		if err := core.RunFlatMigration(core.ResolveRoot(*root), *apply, *rollback); err != nil {
			log.Fatal(err)
		}
		return
	}

	addr := flag.String("addr", ":8899", "listen address (e.g. 0.0.0.0:8899 to expose on the LAN). Avoid 8787 — Plex uses it.")
	root := flag.String("root", "", "media vault directory (else $MEDIAVAULT_ROOT, saved config, or a default under home)")
	uiDir := flag.String("ui", "frontend/dist", "directory of the built web UI to serve (empty to disable)")
	flag.Parse()

	// On Windows, Go's listener uses SO_REUSEADDR and will SILENTLY bind a port
	// another program already owns (e.g. Plex on 8787) — then connections get
	// delivered to that other program and clients see "connection reset". Detect
	// the collision up front and fail with a clear message instead.
	if portInUse(*addr) {
		log.Fatalf("port %q is already in use by another program (Plex uses 8787) — choose a free port, e.g. -addr :9000", *addr)
	}

	resolved := core.ResolveRoot(*root)
	hub := server.NewHub()
	c, err := core.New(resolved, hub.Broadcast)
	if err != nil {
		log.Fatalf("open vault %s: %v", resolved, err)
	}
	defer c.Close()

	var ui fs.FS
	if *uiDir != "" {
		if st, statErr := os.Stat(*uiDir); statErr == nil && st.IsDir() {
			ui = os.DirFS(*uiDir)
		} else {
			log.Printf("web UI dir %q not found — serving API only", *uiDir)
		}
	}

	srv := server.New(c, hub, ui)
	log.Printf("nudes.xxx daemon listening on %s  (vault: %s)", *addr, resolved)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// portInUse reports whether something already answers on addr's host:port. Used
// to catch the silent same-port collision Windows allows (see caller).
func portInUse(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
