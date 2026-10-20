package core

// fleet_contract_test.go — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice G1 (Tailscale adapter). Authored from
// artifacts/50-contract.md alone; no implementation file was read. LOGIC
// dimension: stated invariants and happy paths. Error paths, boundaries and
// refusals (malformed JSON, `peer not found`, missing launchers, the
// 100.64/10 edges) are athena-edge's.
//
// Every test is TestContractFleet…; every package-level helper contractFleet….

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// contractFleetStatusJSON is a recorded-shape `tailscale status --json`:
// this Mac as Self plus an iPhone peer and a second Mac peer. The iPhone's
// TailscaleIPs list its IPv6 FIRST so "first IPv4" is distinguishable from
// "first address". User map is keyed by numeric user id (as a JSON string).
const contractFleetStatusJSON = `{
  "Version": "1.80.0",
  "BackendState": "Running",
  "MagicDNSSuffix": "tail072ad8.ts.net",
  "Self": {
    "ID": "nSELFstable11CNTRL",
    "PublicKey": "nodekey:self",
    "HostName": "Carls-MacBook-Bro",
    "DNSName": "carls-macbook-bro.tail072ad8.ts.net.",
    "OS": "macOS",
    "UserID": 1001,
    "TailscaleIPs": ["100.90.94.3", "fd7a:115c:a1e0::1"],
    "Online": true
  },
  "Peer": {
    "nodekey:aaaa": {
      "ID": "nea6fba5q821CNTRL",
      "PublicKey": "nodekey:aaaa",
      "HostName": "iPhone 17",
      "DNSName": "iphone173.tail072ad8.ts.net.",
      "OS": "iOS",
      "UserID": 1001,
      "TailscaleIPs": ["fd7a:115c:a1e0::2", "100.101.102.103"],
      "Online": false
    },
    "nodekey:bbbb": {
      "ID": "nSTUDIOmini77CNTRL",
      "PublicKey": "nodekey:bbbb",
      "HostName": "studio-mini",
      "DNSName": "studio-mini.tail072ad8.ts.net.",
      "OS": "macOS",
      "UserID": 2002,
      "TailscaleIPs": ["100.70.1.2"],
      "Online": true
    }
  },
  "User": {
    "1001": {"ID": 1001, "LoginName": "carl@github", "DisplayName": "Carl"},
    "2002": {"ID": 2002, "LoginName": "jess@github", "DisplayName": "Jess"}
  }
}`

const contractFleetWhoisJSON = `{
  "Node": {
    "ID": 12345,
    "StableID": "nea6fba5q821CNTRL",
    "Name": "iphone173.tail072ad8.ts.net.",
    "Addresses": ["100.101.102.103/32"]
  },
  "UserProfile": {
    "ID": 1001,
    "LoginName": "carl@github",
    "DisplayName": "Carl"
  }
}`

// contractFleetPeer finds a peer by stable id; the contract does not fix the
// ORDER of Peers (it is parsed from a JSON map), so lookups are by ID.
func contractFleetPeer(t *testing.T, st TailnetStatus, id string) TailnetDevice {
	t.Helper()
	for _, p := range st.Peers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("peer %s not found among %d peers", id, len(st.Peers))
	return TailnetDevice{}
}

// contractFleetSwapRun swaps the runTailscale seam for the test's lifetime.
func contractFleetSwapRun(t *testing.T, fn func(args ...string) ([]byte, error)) {
	t.Helper()
	orig := runTailscale
	runTailscale = fn
	t.Cleanup(func() { runTailscale = orig })
}

// --- ParseTailscaleStatus -------------------------------------------------

// Contract G1 `TailnetStatus`/`TailnetDevice`/`ParseTailscaleStatus`:
// "Reads the `tailscale status --json` shape: Self, Peer (a map keyed by node
// key), User (a map keyed by numeric user id), MagicDNSSuffix. Device ID is
// the status entry's ID … Online is the entry's Online bool."
// "DNSName carries no trailing dot; IP is the device's first IPv4; Login is
// the owning user's LoginName resolved through the status JSON's User map by
// the device's UserID." "Self.Self is true; no peer has Self true."
func TestContractFleetParseStatusSelf(t *testing.T) {
	st, err := ParseTailscaleStatus([]byte(contractFleetStatusJSON))
	if err != nil {
		t.Fatalf("ParseTailscaleStatus: %v", err)
	}
	want := TailnetDevice{
		ID:       "nSELFstable11CNTRL",
		HostName: "Carls-MacBook-Bro",
		DNSName:  "carls-macbook-bro.tail072ad8.ts.net",
		IP:       "100.90.94.3",
		OS:       "macOS",
		Login:    "carl@github",
		Online:   true,
		Self:     true,
	}
	if !reflect.DeepEqual(st.Self, want) {
		t.Errorf("Self = %+v\nwant  %+v", st.Self, want)
	}
	if st.MagicDNSSuffix != "tail072ad8.ts.net" {
		t.Errorf("MagicDNSSuffix = %q, want tail072ad8.ts.net", st.MagicDNSSuffix)
	}
}

func TestContractFleetParseStatusPeers(t *testing.T) {
	st, err := ParseTailscaleStatus([]byte(contractFleetStatusJSON))
	if err != nil {
		t.Fatalf("ParseTailscaleStatus: %v", err)
	}
	if len(st.Peers) != 2 {
		t.Fatalf("len(Peers) = %d, want 2", len(st.Peers))
	}
	phone := contractFleetPeer(t, st, "nea6fba5q821CNTRL")
	wantPhone := TailnetDevice{
		ID:       "nea6fba5q821CNTRL",
		HostName: "iPhone 17",
		DNSName:  "iphone173.tail072ad8.ts.net",
		IP:       "100.101.102.103", // first IPv4, not the leading IPv6
		OS:       "iOS",
		Login:    "carl@github",
		Online:   false,
		Self:     false,
	}
	if !reflect.DeepEqual(phone, wantPhone) {
		t.Errorf("iPhone peer = %+v\nwant         %+v", phone, wantPhone)
	}
	mini := contractFleetPeer(t, st, "nSTUDIOmini77CNTRL")
	wantMini := TailnetDevice{
		ID:       "nSTUDIOmini77CNTRL",
		HostName: "studio-mini",
		DNSName:  "studio-mini.tail072ad8.ts.net",
		IP:       "100.70.1.2",
		OS:       "macOS",
		Login:    "jess@github", // resolved through User["2002"]
		Online:   true,
		Self:     false,
	}
	if !reflect.DeepEqual(mini, wantMini) {
		t.Errorf("studio peer = %+v\nwant        %+v", mini, wantMini)
	}
	for _, p := range st.Peers {
		if p.Self {
			t.Errorf("peer %s has Self true; no peer may", p.ID)
		}
	}
}

// --- ParseTailscaleWhois / Label -----------------------------------------

// Contract G1 `ParseTailscaleWhois`: "Node.StableID → ID, Node.Name → Name
// (trailing dot stripped), UserProfile.LoginName → Login."
func TestContractFleetParseWhois(t *testing.T) {
	id, err := ParseTailscaleWhois([]byte(contractFleetWhoisJSON))
	if err != nil {
		t.Fatalf("ParseTailscaleWhois: %v", err)
	}
	want := TailnetIdentity{ID: "nea6fba5q821CNTRL", Name: "iphone173.tail072ad8.ts.net", Login: "carl@github"}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
}

// Contract G1 `(TailnetIdentity).Label()`: "The first DNS label of Name
// (iphone173); "" when Name is empty."
func TestContractFleetIdentityLabel(t *testing.T) {
	if got := (TailnetIdentity{Name: "iphone173.tail072ad8.ts.net"}).Label(); got != "iphone173" {
		t.Errorf("Label() = %q, want iphone173", got)
	}
	if got := (TailnetIdentity{Name: "studio-mini.tail072ad8.ts.net", Login: "x"}).Label(); got != "studio-mini" {
		t.Errorf("Label() = %q, want studio-mini", got)
	}
	if got := (TailnetIdentity{}).Label(); got != "" {
		t.Errorf("Label() of empty Name = %q, want \"\"", got)
	}
}

// --- exec wrappers over the runTailscale seam ----------------------------

// Contract G1 `TailscaleStatus`: "runTailscale("status","--json") →
// ParseTailscaleStatus."
func TestContractFleetTailscaleStatusRunsStatusJSON(t *testing.T) {
	var got [][]string
	contractFleetSwapRun(t, func(args ...string) ([]byte, error) {
		got = append(got, append([]string(nil), args...))
		return []byte(contractFleetStatusJSON), nil
	})
	st, err := TailscaleStatus()
	if err != nil {
		t.Fatalf("TailscaleStatus: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], []string{"status", "--json"}) {
		t.Fatalf("runTailscale calls = %q, want exactly [[status --json]]", got)
	}
	if st.Self.ID != "nSELFstable11CNTRL" || len(st.Peers) != 2 {
		t.Errorf("status not parsed through: self %q, %d peers", st.Self.ID, len(st.Peers))
	}
}

// Contract G1 `TailscaleWhois`: "runTailscale("whois","--json",ip) →
// ParseTailscaleWhois".
func TestContractFleetTailscaleWhoisRunsWhoisJSON(t *testing.T) {
	var got [][]string
	contractFleetSwapRun(t, func(args ...string) ([]byte, error) {
		got = append(got, append([]string(nil), args...))
		return []byte(contractFleetWhoisJSON), nil
	})
	id, err := TailscaleWhois("100.101.102.103")
	if err != nil {
		t.Fatalf("TailscaleWhois: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], []string{"whois", "--json", "100.101.102.103"}) {
		t.Fatalf("runTailscale calls = %q, want exactly [[whois --json 100.101.102.103]]", got)
	}
	if id.ID != "nea6fba5q821CNTRL" || id.Name != "iphone173.tail072ad8.ts.net" || id.Login != "carl@github" {
		t.Errorf("whois not parsed through: %+v", id)
	}
}

// --- WhoisCached ---------------------------------------------------------

// Contract G1 `WhoisCached`: "Caches successes … per IP for whoisCacheTTL
// (var whoisCacheTTL = 5 * time.Minute); within the window a repeat IP causes
// no second runTailscale call; after it, one. … var whoisNow = time.Now is the
// clock seam." (The ErrTailnetUnknown-caching and uncached-error halves are
// athena-edge's.) IPs are unique to this test so a cache shared across the
// package's tests cannot pre-satisfy it.
func TestContractFleetWhoisCachedHitsWithinTTLAndRefetchesAfter(t *testing.T) {
	if whoisCacheTTL != 5*time.Minute {
		t.Errorf("whoisCacheTTL = %v, want 5m", whoisCacheTTL)
	}
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	clock := base
	origNow := whoisNow
	whoisNow = func() time.Time { return clock }
	t.Cleanup(func() { whoisNow = origNow })

	var mu sync.Mutex
	calls := map[string]int{}
	contractFleetSwapRun(t, func(args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[args[len(args)-1]]++
		return []byte(contractFleetWhoisJSON), nil
	})

	const ip = "100.77.201.11"
	for i := 0; i < 3; i++ {
		id, err := WhoisCached(ip)
		if err != nil {
			t.Fatalf("WhoisCached #%d: %v", i+1, err)
		}
		if id.ID != "nea6fba5q821CNTRL" {
			t.Fatalf("WhoisCached #%d returned %+v", i+1, id)
		}
	}
	if calls[ip] != 1 {
		t.Fatalf("three lookups inside the window ran tailscale %d times, want 1", calls[ip])
	}

	clock = base.Add(4 * time.Minute)
	if _, err := WhoisCached(ip); err != nil {
		t.Fatal(err)
	}
	if calls[ip] != 1 {
		t.Fatalf("a lookup 4m in re-ran tailscale (%d calls), want the cached answer", calls[ip])
	}

	clock = base.Add(6 * time.Minute)
	if _, err := WhoisCached(ip); err != nil {
		t.Fatal(err)
	}
	if calls[ip] != 2 {
		t.Fatalf("a lookup 6m after the first ran tailscale %d times total, want exactly 2", calls[ip])
	}

	// the cache is per IP: a different address is its own miss
	const other = "100.77.201.12"
	if _, err := WhoisCached(other); err != nil {
		t.Fatal(err)
	}
	if calls[other] != 1 {
		t.Errorf("a second IP ran tailscale %d times, want 1 (per-IP cache)", calls[other])
	}
}

// --- ResolveTailscale ----------------------------------------------------

// contractFleetExe writes an executable stub at dir/name and returns its path.
func contractFleetExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Contract G1 `ResolveTailscale`: "KRAKEN_TAILSCALE set → it is the ONLY
// candidate (a bare name is PATH-looked-up, anything else used as given …)
// … Returns an absolute path."
func TestContractFleetResolveTailscaleExplicitAbsolute(t *testing.T) {
	dir := t.TempDir()
	stub := contractFleetExe(t, dir, "my-tailscale")
	t.Setenv("KRAKEN_TAILSCALE", stub)
	got, err := ResolveTailscale()
	if err != nil {
		t.Fatalf("ResolveTailscale: %v", err)
	}
	if got != stub {
		t.Errorf("ResolveTailscale = %q, want the explicit %q", got, stub)
	}
}

func TestContractFleetResolveTailscaleExplicitBareNameUsesPATH(t *testing.T) {
	dir := t.TempDir()
	stub := contractFleetExe(t, dir, "ts-launcher-stub")
	t.Setenv("PATH", dir)
	t.Setenv("KRAKEN_TAILSCALE", "ts-launcher-stub")
	got, err := ResolveTailscale()
	if err != nil {
		t.Fatalf("ResolveTailscale: %v", err)
	}
	if got != stub {
		t.Errorf("ResolveTailscale = %q, want PATH lookup %q", got, stub)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("ResolveTailscale returned a relative path %q", got)
	}
}

// "unset → tailscale on PATH → /usr/local/bin/tailscale → …": PATH is the
// first rung, so a PATH tailscale wins even on a machine that also has the
// /usr/local/bin one.
func TestContractFleetResolveTailscaleUnsetPrefersPATH(t *testing.T) {
	dir := t.TempDir()
	stub := contractFleetExe(t, dir, "tailscale")
	t.Setenv("PATH", dir)
	t.Setenv("KRAKEN_TAILSCALE", "")
	os.Unsetenv("KRAKEN_TAILSCALE")
	got, err := ResolveTailscale()
	if err != nil {
		t.Fatalf("ResolveTailscale: %v", err)
	}
	if got != stub {
		t.Errorf("ResolveTailscale = %q, want PATH's %q", got, stub)
	}
}

// --- TailscaleIP / IsTailnetIP ------------------------------------------

// Contract G1 `TailscaleIP`: "first IPv4 in 100.64.0.0/10".
func TestContractFleetTailscaleIPPicksFirstTailnetV4(t *testing.T) {
	mk := func(s string) net.Addr {
		ip, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	addrs := []net.Addr{mk("127.0.0.1/8"), mk("192.168.1.20/24"), mk("100.90.94.3/32"), mk("100.70.1.2/32")}
	ip, ok := TailscaleIP(addrs)
	if !ok || ip != "100.90.94.3" {
		t.Errorf("TailscaleIP = (%q, %v), want (100.90.94.3, true)", ip, ok)
	}
}

// Contract G1 `IsTailnetIP`: "True for 100.64.0.0/10 only." (The range edges
// are athena-edge's.)
func TestContractFleetIsTailnetIP(t *testing.T) {
	for _, c := range []struct {
		ip   string
		want bool
	}{
		{"100.90.94.3", true},
		{"100.101.102.103", true},
		{"192.168.1.2", false},
		{"8.8.8.8", false},
	} {
		if got := IsTailnetIP(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("IsTailnetIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// Contract G1: ErrNoTailscale and ErrTailnetUnknown are distinct sentinel
// errors (callers branch on errors.Is between them).
func TestContractFleetSentinelsDistinct(t *testing.T) {
	if ErrNoTailscale == nil || ErrTailnetUnknown == nil {
		t.Fatal("both sentinels must be non-nil errors")
	}
	if errors.Is(ErrNoTailscale, ErrTailnetUnknown) || errors.Is(ErrTailnetUnknown, ErrNoTailscale) {
		t.Error("ErrNoTailscale and ErrTailnetUnknown must be distinct")
	}
}
