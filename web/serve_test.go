package web

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

func ipNet(ip string, bits int) *net.IPNet {
	return &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(bits, 32)}
}

func TestTailscaleIP(t *testing.T) {
	// a hit sits alongside ordinary LAN/loopback addrs, as a real interface
	// listing would have
	addrs := []net.Addr{
		ipNet("127.0.0.1", 8),
		ipNet("192.168.1.5", 24),
		ipNet("100.101.102.103", 10),
	}
	ip, ok := tailscaleIP(addrs)
	if !ok || ip != "100.101.102.103" {
		t.Fatalf("got %q, %v", ip, ok)
	}

	// 100.128.0.0/10 is the next block up — outside tailscale's CGNAT range
	// (100.64.0.0/10 ends at 100.127.255.255)
	if _, ok := tailscaleIP([]net.Addr{ipNet("100.128.0.1", 16)}); ok {
		t.Errorf("100.128.0.1 must not match")
	}
	// one address below the range
	if _, ok := tailscaleIP([]net.Addr{ipNet("100.63.255.255", 16)}); ok {
		t.Errorf("100.63.255.255 must not match")
	}
	// IPv6-only and loopback addrs are ignored, not mistaken for a match
	if _, ok := tailscaleIP([]net.Addr{
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		ipNet("127.0.0.1", 8),
	}); ok {
		t.Errorf("loopback/IPv6 addrs must not match")
	}
	// miss case: nothing in range at all
	if _, ok := tailscaleIP([]net.Addr{ipNet("10.0.0.5", 8), ipNet("192.168.0.1", 24)}); ok {
		t.Errorf("non-tailnet addrs must not match")
	}
	// second octet in 64-127 but first octet not 100 — a corporate-VPN shape
	// (10.100.x.x) that must never bind the API
	if _, ok := tailscaleIP([]net.Addr{ipNet("10.100.0.5", 8)}); ok {
		t.Errorf("10.100.0.5 must not match")
	}
	if _, ok := tailscaleIP([]net.Addr{ipNet("172.64.1.1", 16)}); ok {
		t.Errorf("172.64.1.1 must not match")
	}
	if _, ok := tailscaleIP(nil); ok {
		t.Errorf("empty addr list must miss")
	}
}

func TestArtifactHandlerNoStore(t *testing.T) {
	// Artifacts are rewritten in place by agents; every response — including
	// 404s — must carry no-store so browsers never serve a stale copy.
	st := useTestStore(t)
	for _, path := range []string{
		"/api/artifact/no-such-job-xyz/cell/report.html", // unresolvable slug
		"/api/artifact/malformed",                        // too few parts
	} {
		rec := httptest.NewRecorder()
		artifactHandler(rec, httptest.NewRequest("GET", path, nil))
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want %q", path, got, "no-store")
		}
		if rec.Code != 404 {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}

	// 200 path — pins that http.ServeFile leaves the header intact on a
	// successfully served (cell-home) artifact.
	if _, err := st.CreateJob(&store.Job{Slug: "nscache", Repo: "x"}); err != nil {
		t.Fatal(err)
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "nscache")
	if j == nil {
		t.Fatal("job nscache not loadable")
	}
	root := jobs.CellArtifacts(j)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/nscache/cell/report.html", nil))
	if rec.Code != 200 {
		t.Fatalf("served artifact: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("served artifact: Cache-Control = %q, want %q", got, "no-store")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("served artifact: Content-Type = %q, want text/html", got)
	}
}

func TestArtifactContentType(t *testing.T) {
	cases := map[string]string{
		"a/report.html": "text/html; charset=utf-8",
		"a/page.HTM":    "text/html; charset=utf-8",
		"a/d.svg":       "image/svg+xml",
		"a/doc.pdf":     "application/pdf",
		"a/doc.PDF":     "application/pdf",
		"a/img.png":     "image/png",
		"a/img.jpeg":    "image/jpeg",
		"a/img.jpg":     "image/jpeg",
		"a/anim.gif":    "image/gif",
		"a/img.webp":    "image/webp",
		"a/notes.md":    "text/plain; charset=utf-8",
		"a/x.log":       "text/plain; charset=utf-8",
		"a/no-ext":      "text/plain; charset=utf-8",
	}
	for p, want := range cases {
		if got := artifactContentType(p); got != want {
			t.Errorf("artifactContentType(%q) = %q, want %q", p, got, want)
		}
	}
}
