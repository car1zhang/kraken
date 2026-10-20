package core

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fleet_edge_test.go — the EDGE dimension of 50-contract.md slice G1 (the
// tailscale adapter) for package core: the parser's odd-shaped entries and
// malformed input, the launcher ladder's refusals, the whois cache's handling
// of the not-found answer and of other errors, and the 100.64.0.0/10
// boundaries. Written blind from the contract: internal/core/tailscale.go was
// not read. Happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeFleet`.

// edgeFleetStatus is a `tailscale status --json` document with a self, three
// peers and one user. Peer "v6only" has only an IPv6 address, peer "noips"
// has no TailscaleIPs key at all, peer "orphan" names a UserID the User map
// lacks, and peer "v6first" lists IPv6 before its IPv4.
const edgeFleetStatus = `{
  "Self": {"ID": "nSELF1CNTRL", "PublicKey": "nodekey:self", "HostName": "carls-mac",
           "DNSName": "carls-mac.tail072ad8.ts.net.", "OS": "macOS", "UserID": 11,
           "TailscaleIPs": ["100.90.94.3", "fd7a:115c:a1e0::3"], "Online": true},
  "Peer": {
    "nodekey:a": {"ID": "nV6ONLY", "PublicKey": "nodekey:a", "HostName": "v6only",
                  "DNSName": "v6only.tail072ad8.ts.net.", "OS": "linux", "UserID": 11,
                  "TailscaleIPs": ["fd7a:115c:a1e0::9"], "Online": true},
    "nodekey:b": {"ID": "nNOIPS", "PublicKey": "nodekey:b", "HostName": "noips",
                  "DNSName": "noips.tail072ad8.ts.net.", "OS": "iOS", "UserID": 11,
                  "Online": false},
    "nodekey:c": {"ID": "nORPHAN", "PublicKey": "nodekey:c", "HostName": "orphan",
                  "DNSName": "orphan.tail072ad8.ts.net.", "OS": "windows", "UserID": 999,
                  "TailscaleIPs": ["100.100.1.1"], "Online": true},
    "nodekey:d": {"ID": "nV6FIRST", "PublicKey": "nodekey:d", "HostName": "v6first",
                  "DNSName": "v6first.tail072ad8.ts.net.", "OS": "macOS", "UserID": 11,
                  "TailscaleIPs": ["fd7a:115c:a1e0::7", "100.101.2.2"], "Online": true}
  },
  "User": {"11": {"ID": 11, "LoginName": "carl@probook.ai", "DisplayName": "Carl"}},
  "MagicDNSSuffix": "tail072ad8.ts.net"
}`

func edgeFleetPeer(t *testing.T, st TailnetStatus, id string) TailnetDevice {
	t.Helper()
	for _, p := range st.Peers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("peer %s missing from parsed status (peers: %+v)", id, st.Peers)
	return TailnetDevice{}
}

// "An entry lacking IPv4 still parses, with `IP == ""`." — both an IPv6-only
// list and an absent TailscaleIPs key.
func TestEdgeFleetParseStatusPeerWithoutIPv4(t *testing.T) {
	st, err := ParseTailscaleStatus([]byte(edgeFleetStatus))
	if err != nil {
		t.Fatalf("ParseTailscaleStatus: %v", err)
	}
	if got := edgeFleetPeer(t, st, "nV6ONLY").IP; got != "" {
		t.Errorf("IPv6-only peer IP = %q, want \"\"", got)
	}
	if got := edgeFleetPeer(t, st, "nNOIPS").IP; got != "" {
		t.Errorf("peer with no TailscaleIPs IP = %q, want \"\"", got)
	}
}

// "`IP` is the device's first IPv4" — an IPv6 address listed first is skipped.
func TestEdgeFleetParseStatusFirstIPv4SkipsLeadingIPv6(t *testing.T) {
	st, err := ParseTailscaleStatus([]byte(edgeFleetStatus))
	if err != nil {
		t.Fatalf("ParseTailscaleStatus: %v", err)
	}
	if got := edgeFleetPeer(t, st, "nV6FIRST").IP; got != "100.101.2.2" {
		t.Errorf("v6-first peer IP = %q, want 100.101.2.2", got)
	}
	if st.Self.IP != "100.90.94.3" {
		t.Errorf("Self.IP = %q, want 100.90.94.3", st.Self.IP)
	}
}

// "`Login` is the owning user's `LoginName` resolved through the status
// JSON's `User` map by the device's `UserID`." A UserID the map lacks resolves
// to nothing (silence — literal reading: ""), and the peer still parses.
func TestEdgeFleetParseStatusUnknownUserIDHasEmptyLogin(t *testing.T) {
	st, err := ParseTailscaleStatus([]byte(edgeFleetStatus))
	if err != nil {
		t.Fatalf("ParseTailscaleStatus: %v", err)
	}
	if got := edgeFleetPeer(t, st, "nORPHAN").Login; got != "" {
		t.Errorf("orphan peer Login = %q, want \"\"", got)
	}
}

// "`Self.Self` is true; no peer has `Self` true." with peers that are offline,
// IP-less and user-less.
func TestEdgeFleetParseStatusNoPeerIsSelf(t *testing.T) {
	st, err := ParseTailscaleStatus([]byte(edgeFleetStatus))
	if err != nil {
		t.Fatalf("ParseTailscaleStatus: %v", err)
	}
	if !st.Self.Self {
		t.Errorf("Self.Self = false")
	}
	if len(st.Peers) != 4 {
		t.Fatalf("parsed %d peers, want 4", len(st.Peers))
	}
	for _, p := range st.Peers {
		if p.Self {
			t.Errorf("peer %s has Self = true", p.ID)
		}
	}
}

// "Malformed JSON is an error."
func TestEdgeFleetParseStatusMalformedJSON(t *testing.T) {
	for _, bad := range []string{``, `{`, `not json`, `{"Self":`, `{"Peer": [}`} {
		if _, err := ParseTailscaleStatus([]byte(bad)); err == nil {
			t.Errorf("ParseTailscaleStatus(%q) returned no error", bad)
		}
	}
}

// `Node.Name` → `Name` (trailing dot stripped): a name with no trailing dot
// is left exactly as it is (nothing to strip).
func TestEdgeFleetParseWhoisNameWithoutTrailingDot(t *testing.T) {
	b := []byte(`{"Node": {"ID": 5, "StableID": "nIPHONE", "Name": "iphone173.tail072ad8.ts.net"},
	              "UserProfile": {"ID": 11, "LoginName": "carl@probook.ai"}}`)
	id, err := ParseTailscaleWhois(b)
	if err != nil {
		t.Fatalf("ParseTailscaleWhois: %v", err)
	}
	if id.Name != "iphone173.tail072ad8.ts.net" {
		t.Errorf("Name = %q, want unchanged", id.Name)
	}
}

// `Label()`: "`""` when `Name` is empty"; a dotless name is its own first label.
func TestEdgeFleetIdentityLabelEmptyAndDotless(t *testing.T) {
	if got := (TailnetIdentity{}).Label(); got != "" {
		t.Errorf("empty Name Label() = %q, want \"\"", got)
	}
	if got := (TailnetIdentity{Name: "studio"}).Label(); got != "studio" {
		t.Errorf("dotless Name Label() = %q, want studio", got)
	}
}

// edgeFleetSwapRun replaces runTailscale for one test and counts calls.
func edgeFleetSwapRun(t *testing.T, fn func(args ...string) ([]byte, error)) *int32 {
	t.Helper()
	var n int32
	old := runTailscale
	runTailscale = func(args ...string) ([]byte, error) {
		atomic.AddInt32(&n, 1)
		return fn(args...)
	}
	t.Cleanup(func() { runTailscale = old })
	return &n
}

// edgeFleetSwapClock pins whoisNow to a settable instant.
func edgeFleetSwapClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	old := whoisNow
	whoisNow = func() time.Time { return now }
	t.Cleanup(func() { whoisNow = old })
	return &now
}

// edgeFleetPeerNotFound is the CLI's not-found answer: non-zero exit, stderr
// "peer not found". The seam's representation of stderr is a contract
// silence, so the fake carries the text in every place an exec wrapper can
// put it — the returned bytes, the error string, and an *exec.ExitError's
// Stderr — and the assertions stay strict.
func edgeFleetPeerNotFound(args ...string) ([]byte, error) {
	ee := &exec.ExitError{Stderr: []byte("peer not found\n")}
	return []byte("peer not found\n"), fmt.Errorf("tailscale %v: exit status 1: peer not found: %w", args, ee)
}

// `TailscaleWhois`: "the not-found case maps to `ErrTailnetUnknown`."
func TestEdgeFleetWhoisNotFoundIsErrTailnetUnknown(t *testing.T) {
	edgeFleetSwapRun(t, edgeFleetPeerNotFound)
	_, err := TailscaleWhois("100.120.7.1")
	if !errors.Is(err, ErrTailnetUnknown) {
		t.Fatalf("TailscaleWhois(not found) err = %v, want ErrTailnetUnknown", err)
	}
}

// `WhoisCached`: "Caches successes AND `ErrTailnetUnknown` per IP for
// `whoisCacheTTL`; within the window a repeat IP causes no second
// `runTailscale` call; after it, one."
func TestEdgeFleetWhoisCachedCachesNotFound(t *testing.T) {
	n := edgeFleetSwapRun(t, edgeFleetPeerNotFound)
	clock := edgeFleetSwapClock(t, time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	const ip = "100.120.7.2"

	if _, err := WhoisCached(ip); !errors.Is(err, ErrTailnetUnknown) {
		t.Fatalf("first WhoisCached err = %v, want ErrTailnetUnknown", err)
	}
	*clock = clock.Add(whoisCacheTTL - time.Second)
	if _, err := WhoisCached(ip); !errors.Is(err, ErrTailnetUnknown) {
		t.Fatalf("cached WhoisCached err = %v, want ErrTailnetUnknown", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Fatalf("runTailscale called %d times within the window, want 1", got)
	}
	*clock = clock.Add(2 * time.Second) // now past the TTL from the first call
	if _, err := WhoisCached(ip); !errors.Is(err, ErrTailnetUnknown) {
		t.Fatalf("post-TTL WhoisCached err = %v, want ErrTailnetUnknown", err)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Fatalf("runTailscale called %d times after the window, want 2", got)
	}
}

// AMENDED (review round 1): "Other errors (daemon down, timeout) are cached for
// the shorter `whoisErrTTL`" — within that window a repeat IP costs no second
// exec; past it, one. Corrected by the lead as a test bug against the amended
// text (the old row, "not cached", let a wedged tailscaled cost one exec per
// tailnet request).
func TestEdgeFleetWhoisCachedCachesOtherErrorsBriefly(t *testing.T) {
	n := edgeFleetSwapRun(t, func(args ...string) ([]byte, error) {
		return nil, errors.New("failed to connect to local tailscaled; it doesn't appear to be running")
	})
	clock := edgeFleetSwapClock(t, time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	const ip = "100.120.7.3"

	_, err1 := WhoisCached(ip)
	_, err2 := WhoisCached(ip)
	if err1 == nil || err2 == nil {
		t.Fatalf("WhoisCached returned nil error for a failing exec: %v, %v", err1, err2)
	}
	if errors.Is(err1, ErrTailnetUnknown) {
		t.Errorf("a daemon-down error was reported as ErrTailnetUnknown")
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Fatalf("runTailscale called %d times within whoisErrTTL, want 1 (the failure is cached briefly)", got)
	}
	*clock = clock.Add(whoisErrTTL + time.Second) // past the short error TTL, well inside the 5-minute one
	if _, err := WhoisCached(ip); err == nil {
		t.Fatal("WhoisCached returned nil error for a still-failing exec")
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Fatalf("runTailscale called %d times after whoisErrTTL, want 2 (an error is not cached for whoisCacheTTL)", got)
	}
}

// The cache is "per IP": a cached answer for one address never answers another.
func TestEdgeFleetWhoisCachedIsPerIP(t *testing.T) {
	n := edgeFleetSwapRun(t, edgeFleetPeerNotFound)
	edgeFleetSwapClock(t, time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	WhoisCached("100.120.7.4")
	WhoisCached("100.120.7.5")
	if got := atomic.LoadInt32(n); got != 2 {
		t.Fatalf("runTailscale called %d times for two distinct IPs, want 2", got)
	}
}

// edgeFleetFakeLauncher writes an executable named name into a fresh dir and
// returns the dir.
func edgeFleetFakeLauncher(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// `ResolveTailscale`: "`KRAKEN_TAILSCALE` set → it is the ONLY candidate
// (… missing → `ErrNoTailscale`, never a fallthrough)" — a working
// `tailscale` on PATH must not rescue a missing explicit path.
func TestEdgeFleetResolveExplicitMissingPathNeverFallsThrough(t *testing.T) {
	dir := edgeFleetFakeLauncher(t, "tailscale")
	t.Setenv("PATH", dir)
	t.Setenv("KRAKEN_TAILSCALE", filepath.Join(t.TempDir(), "no-such-tailscale"))
	got, err := ResolveTailscale()
	if !errors.Is(err, ErrNoTailscale) {
		t.Fatalf("ResolveTailscale = (%q, %v), want ErrNoTailscale", got, err)
	}
}

// Same rule for a bare name: it is PATH-looked-up, and absent from PATH it
// is ErrNoTailscale even though `tailscale` itself is on PATH.
func TestEdgeFleetResolveExplicitBareNameMissingFromPath(t *testing.T) {
	dir := edgeFleetFakeLauncher(t, "tailscale")
	t.Setenv("PATH", dir)
	t.Setenv("KRAKEN_TAILSCALE", "tailscale-edge-absent")
	got, err := ResolveTailscale()
	if !errors.Is(err, ErrNoTailscale) {
		t.Fatalf("ResolveTailscale = (%q, %v), want ErrNoTailscale", got, err)
	}
}

// "a bare name is PATH-looked-up … Returns an absolute path."
func TestEdgeFleetResolveExplicitBareNameResolvesAbsolute(t *testing.T) {
	dir := edgeFleetFakeLauncher(t, "ts-edge")
	t.Setenv("PATH", dir)
	t.Setenv("KRAKEN_TAILSCALE", "ts-edge")
	got, err := ResolveTailscale()
	if err != nil {
		t.Fatalf("ResolveTailscale: %v", err)
	}
	if want := filepath.Join(dir, "ts-edge"); got != want {
		t.Fatalf("ResolveTailscale = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveTailscale returned a relative path %q", got)
	}
}

// `IsTailnetIP`: "True for 100.64.0.0/10 only." — both edges of the block,
// one address either side of it, IPv6 (including tailscale's own ULA range),
// and nil.
func TestEdgeFleetIsTailnetIPBoundaries(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"100.64.0.0", true},
		{"100.127.255.255", true},
		{"100.63.255.255", false},
		{"100.128.0.0", false},
		{"100.128.0.1", false},
		{"10.0.0.1", false},
		{"192.168.1.2", false},
		{"127.0.0.1", false},
		{"fd7a:115c:a1e0::1", false},
		{"::ffff:10.0.0.1", false},
	}
	for _, c := range cases {
		if got := IsTailnetIP(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("IsTailnetIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
	if IsTailnetIP(nil) {
		t.Errorf("IsTailnetIP(nil) = true")
	}
}

// `TailscaleIP`: "first IPv4 in 100.64.0.0/10" — no addresses, and only
// non-tailnet addresses, both answer ("", false).
func TestEdgeFleetTailscaleIPNoneQualifies(t *testing.T) {
	if ip, ok := TailscaleIP(nil); ok || ip != "" {
		t.Errorf("TailscaleIP(nil) = (%q, %v), want (\"\", false)", ip, ok)
	}
	var addrs []net.Addr
	for _, cidr := range []string{"127.0.0.1/8", "192.168.1.2/24", "fd7a:115c:a1e0::3/128", "100.128.0.1/32"} {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		addrs = append(addrs, n)
	}
	if ip, ok := TailscaleIP(addrs); ok || ip != "" {
		t.Errorf("TailscaleIP(non-tailnet addrs) = (%q, %v), want (\"\", false)", ip, ok)
	}
}
