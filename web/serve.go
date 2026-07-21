package web

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"kraken/api"
	"kraken/jobs"
)

// Options are k web's already-parsed flags — cmd_web.go owns the flag
// strings/usage text, so this is just what's left after parsing.
type Options struct {
	Port         string
	Tailnet      bool
	ExplicitAddr string
}

// Serve builds the mux (embedded static assets + the api package's routes +
// artifact serving) and blocks on ListenAndServe.
func Serve(opts Options) error {
	addr := "127.0.0.1:" + opts.Port
	if opts.Tailnet {
		ip, err := detectTailscaleIP()
		if err != nil {
			return err
		}
		addr = ip + ":" + opts.Port
	}
	if opts.ExplicitAddr != "" {
		addr = opts.ExplicitAddr // overrides --tailnet/-p, per usage
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
	// /api/artifact/<slug>/<cell|pluto>/<name>
	mux.HandleFunc("/api/artifact/", func(w http.ResponseWriter, r *http.Request) {
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
	})
	fmt.Printf("kraken web ui → http://%s (ctrl-c to stop)\n", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		return fmt.Errorf("web server: %v", err)
	}
	return nil
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
