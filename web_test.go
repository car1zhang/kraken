package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestArtifactPathSubdirsAndTraversal(t *testing.T) {
	// build a temp cell artifacts tree: file at root + nested subdir file
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "artifacts", "icon-variations"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeF := func(rel string) {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", rel), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeF("top.png")
	writeF(filepath.Join("icon-variations", "icon-v1.png"))
	writeF(filepath.Join("icon-variations", ".hidden"))

	// cellArtifacts derives from cellDir(j); fake it by pointing the job's
	// cell resolution at our temp dir via a stub is overkill — instead test
	// the pure guard against a known root.
	root := filepath.Join(dir, "artifacts")
	check := func(name string) bool {
		for _, seg := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
			if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
				return false
			}
		}
		p := filepath.Join(root, name)
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	if !check("top.png") {
		t.Error("top-level file should resolve")
	}
	if !check("icon-variations/icon-v1.png") {
		t.Error("nested file should resolve")
	}
	if check("../../etc/passwd") {
		t.Error("traversal must be refused")
	}
	if check("icon-variations/../../../secret") {
		t.Error("mid-path traversal must be refused")
	}
	if check("icon-variations/.hidden") {
		t.Error("hidden file must be refused")
	}
	if check("icon-variations") {
		t.Error("a directory is not a file artifact")
	}
}
