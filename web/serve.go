package web

import (
	"bytes"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kraken/api"
	"kraken/jobs"
	"kraken/util"
)

// Options are k web's already-parsed flags — cmd_web.go owns the flag
// strings/usage text, so this is just what's left after parsing.
type Options struct {
	Port         string
	Tailnet      bool
	ExplicitAddr string
	// DisableSignalHandler skips Serve's own SIGTERM/SIGINT goroutine below.
	// kraken serve (cmd_serve.go) registers its own shutdown handler — stop
	// ticks, close the store, exit exactly once — and sets this so the two
	// handlers don't race each other for which os.Exit(0) call wins; k web
	// (cmd_web.go) leaves it false and keeps Serve's simple log-and-exit
	// behavior unchanged.
	DisableSignalHandler bool
	// Listener, when set, is used instead of Serve binding its own (E5): a
	// caller that needs to know binding succeeded BEFORE starting other
	// side effects (kraken serve — cmd_serve.go net.Listens first, then
	// only starts the scheduler tick goroutine and writes daemon.json once
	// that succeeds) calls net.Listen itself via the exact same address
	// ResolveAddr computes, and hands the listener in here. Nil (the k web
	// default) preserves the old ListenAndServe-does-everything behavior.
	Listener net.Listener
}

// ResolveAddr computes the host:port Serve would bind for opts, applying
// the same Tailnet/ExplicitAddr precedence (ExplicitAddr wins over
// Tailnet/Port) Serve uses internally. Exported so a caller that wants to
// net.Listen itself before Serve runs (kraken serve, via Options.Listener
// above) binds the exact same address Serve would have.
func ResolveAddr(opts Options) (string, error) {
	addr := "127.0.0.1:" + opts.Port
	if opts.Tailnet {
		ip, err := detectTailscaleIP()
		if err != nil {
			return "", err
		}
		addr = ip + ":" + opts.Port
	}
	if opts.ExplicitAddr != "" {
		addr = opts.ExplicitAddr // overrides --tailnet/-p, per usage
	}
	return addr, nil
}

// Serve builds the mux (embedded static assets + the api package's routes +
// artifact serving) and blocks serving it. Binds its own listener at
// ResolveAddr(opts) unless opts.Listener is already set.
func Serve(opts Options) error {
	addr, err := ResolveAddr(opts)
	if err != nil {
		return err
	}
	ln := opts.Listener
	if ln == nil {
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %v", addr, err)
		}
	}
	static, err := fs.Sub(webAssets, "static")
	if err != nil {
		return fmt.Errorf("embedded assets: %v", err)
	}
	mux := http.NewServeMux()
	// no-cache: embed.FS files carry a zero mod time, so FileServer sends no
	// Last-Modified/ETag validators — browsers fall back to heuristic caching
	// and keep serving a stale SPA across binary upgrades. Force revalidation
	// (a full refetch, given no validators); the assets are tiny and local.
	staticFiles := http.FileServer(http.FS(static))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		staticFiles.ServeHTTP(w, r)
	}))
	api.Register(mux)
	mux.HandleFunc("/api/artifact/", artifactHandler)
	// GET /api/upload/<slug>/<name> — the POST side lives at the exact path
	// "/api/upload" (api.Register); a ServeMux exact pattern and a subtree
	// pattern ("/api/upload/", trailing slash) never shadow each other, so
	// the two coexist without conflict.
	mux.HandleFunc("/api/upload/", uploadGetHandler)
	fmt.Printf("kraken web ui → http://%s (ctrl-c to stop)\n", addr)
	util.LogEvent("web", "info", "web-start", "web server up on http://"+addr)
	if !opts.DisableSignalHandler {
		// ctrl-c / SIGTERM: log the shutdown edge before dying — the unified
		// event log should show every server lifecycle boundary. Skipped
		// when a caller (kraken serve) owns its own shutdown sequence — see
		// Options.DisableSignalHandler.
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			s := <-sigc
			util.LogEvent("web", "info", "web-stop", fmt.Sprintf("web server on %s stopping (%v)", addr, s))
			os.Exit(0)
		}()
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		util.LogEvent("web", "error", "web-error", "web server: "+err.Error())
		return fmt.Errorf("web server: %v", err)
	}
	return nil
}

// uploadGetHandler serves GET /api/upload/<slug>/<name>: store mode reads
// straight from the uploads table (api.UploadBytes), falling back to
// the materialized file (api.UploadPath) — always safe, since
// jobs.SaveUpload writes both regardless of storage mode. A named function
// (not an inline closure in Serve) so it's directly unit-testable.
func uploadGetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/upload/"), "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	if data, mimeType, ok := api.UploadBytes(parts[0], parts[1]); ok {
		if mimeType == "" {
			mimeType = artifactContentType(parts[1])
		}
		w.Header().Set("Content-Type", mimeType)
		http.ServeContent(w, r, parts[1], time.Time{}, bytes.NewReader(data))
		return
	}
	p, err := api.UploadPath(parts[0], parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", artifactContentType(p))
	http.ServeFile(w, r, p)
}

// artifactHandler serves /api/artifact/<slug>/<cell|pluto|archive>/<name>.
// Agents rewrite artifact files in place between views, so responses are
// no-store: without it, ServeFile's Last-Modified triggers browser heuristic
// caching (~10% of file age with no Cache-Control) and the board shows stale
// artifacts long after the file changed on disk.
func artifactHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/artifact/"), "/", 3)
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	// the home decides the namespace the job resolves in: an archived
	// "foo-2" and an active job slugged "foo-2" are different files
	var j *jobs.Job
	if parts[1] == "archive" {
		j = jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), parts[0])
	} else {
		j = jobs.JobBySlug(jobs.LoadJobs(), parts[0])
	}
	if j == nil {
		http.NotFound(w, r)
		return
	}
	// Q12: a live cell always serves straight from the cell dir ("cell"
	// home skips this branch entirely); a store-backed done/archived job's
	// "pluto"/"archive" homes serve from the DB instead of a preserved file
	// copy. ArtifactBytes returns ok=false whenever no store is wired or the
	// path isn't in the table, so this always falls through to the
	// filesystem lookup below when appropriate.
	if parts[1] != "cell" {
		if data, mimeType, ok := api.ArtifactBytes(j, parts[1], parts[2]); ok {
			w.Header().Set("Content-Type", mimeType)
			http.ServeContent(w, r, parts[2], time.Time{}, bytes.NewReader(data))
			return
		}
	}
	p, ok := api.ArtifactPath(j, parts[1], parts[2])
	if !ok {
		http.NotFound(w, r)
		return
	}
	// html deliverables render as pages (local, agent-authored content —
	// same trust as opening the file); binary formats viewers can render
	// get their real type; everything else stays plain text for the
	// in-board markdown viewer
	w.Header().Set("Content-Type", artifactContentType(p))
	http.ServeFile(w, r, p)
}

// artifactContentType picks the served type by extension: renderable pages
// and binary formats (pdf, images) get their real type so phone/browser
// viewers render them; everything else is plain text for the markdown viewer.
func artifactContentType(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "text/plain; charset=utf-8"
	}
}

// tailscaleIP picks the first IPv4 address in addrs that falls in
// tailscale's CGNAT range, 100.64.0.0/10 (first octet 100, second 64-127).
// A pure function over an address list so it's testable without touching
// real interfaces.
func tailscaleIP(addrs []net.Addr) (string, bool) {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil {
			continue // loopback/IPv6-only addrs never match a CGNAT v4 range
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return ip4.String(), true
		}
	}
	return "", false
}

// detectTailscaleIP gathers every local interface address and picks the
// tailnet one — the thin, non-testable caller around tailscaleIP.
func detectTailscaleIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("enumerate network interfaces: %v", err)
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		a, err := iface.Addrs()
		if err != nil {
			continue // interface came and went; not fatal to the scan
		}
		addrs = append(addrs, a...)
	}
	ip, ok := tailscaleIP(addrs)
	if !ok {
		return "", fmt.Errorf("no tailscale interface (100.64.0.0/10) found — is tailscale up?")
	}
	return ip, nil
}
