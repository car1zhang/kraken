package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// ownerkey_test.go — the owner tier needs a local address AND the owner key
// (jobs/ownerkey.go), and /api/owner hands a browser on this Mac the key as a
// cookie.

const testOwnerKey = "owner-key-for-tests-0123456789abcdefghijklmn"

// ownerKeyHdr is the header an owner-tier test request carries.
var ownerKeyHdr = map[string]string{jobs.OwnerKeyHeader: testOwnerKey}

// ownerTestDeps: local is the address check's answer; the key on disk is
// testOwnerKey; "tok" is a paired device's token; 100.x is a tailnet device.
func ownerTestDeps(local bool) gateDeps {
	return gateDeps{
		localOrigin: func(string) bool { return local },
		ownerKey:    func() string { return testOwnerKey },
		lookupToken: func(hash string) (*store.PairDevice, bool) {
			if hash == tokenHash("tok") {
				return &store.PairDevice{ID: 3, TailnetName: "phone"}, true
			}
			return nil, false
		},
		tailnetDevice: func(ip string) (core.TailnetIdentity, bool) {
			if strings.HasPrefix(ip, "100.") {
				return core.TailnetIdentity{Name: "tailnet-mac"}, true
			}
			return core.TailnetIdentity{}, false
		},
	}
}

func ownerReq(remote string, mutate func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	r.RemoteAddr = remote
	if mutate != nil {
		mutate(r)
	}
	return r
}

func withHeader(k string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(jobs.OwnerKeyHeader, k) }
}

func withCookie(k string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: jobs.OwnerCookieName, Value: k}) }
}

func TestOwnerTierNeedsAddressAndKey(t *testing.T) {
	cases := []struct {
		name   string
		local  bool
		remote string
		mutate func(*http.Request)
		want   Tier
	}{
		{"local with the key in the header", true, "127.0.0.1:5000", withHeader(testOwnerKey), TierOwner},
		{"local with the key in the cookie", true, "127.0.0.1:5000", withCookie(testOwnerKey), TierOwner},
		// a Docker container reaching loopback: local address, no key
		{"local without a key", true, "127.0.0.1:5000", nil, TierAnon},
		{"local with a wrong key", true, "127.0.0.1:5000", withHeader(testOwnerKey + "x"), TierAnon},
		{"local with a wrong cookie", true, "127.0.0.1:5000", withCookie("nope"), TierAnon},
		{"local with an empty key header", true, "127.0.0.1:5000", withHeader(""), TierAnon},
		// a keyless local caller falls through the rest of the ladder
		{"local without a key, paired token", true, "127.0.0.1:5000", func(r *http.Request) { r.Header.Set("Authorization", "Bearer tok") }, TierPaired},
		{"own tailnet address without a key", true, "100.64.0.9:5000", nil, TierNode},
		// the key is no use from anywhere else
		{"remote with the key", false, "100.64.0.9:5000", withHeader(testOwnerKey), TierNode},
		{"remote anon with the key", false, "203.0.113.4:5000", withCookie(testOwnerKey), TierAnon},
	}
	for _, c := range cases {
		tier, _ := classify(ownerReq(c.remote, c.mutate), ownerTestDeps(c.local))
		if tier != c.want {
			t.Errorf("%s: tier %s, want %s", c.name, tierName(tier), tierName(c.want))
		}
	}
}

// No key on disk ("" — kraken serve never wrote one) means nobody is owner,
// not that an empty key matches.
func TestOwnerTierWithNoKeyOnDisk(t *testing.T) {
	deps := ownerTestDeps(true)
	deps.ownerKey = func() string { return "" }
	for _, m := range []func(*http.Request){nil, withHeader(""), withCookie("")} {
		if tier, _ := classify(ownerReq("127.0.0.1:5000", m), deps); tier == TierOwner {
			t.Error("no key on disk: a local caller was classified owner")
		}
	}
	deps.ownerKey = nil
	if tier, _ := classify(ownerReq("127.0.0.1:5000", withHeader(testOwnerKey)), deps); tier == TierOwner {
		t.Error("nil ownerKey lookup: a local caller was classified owner")
	}
}

// The gate refuses a keyless local caller with a reason the board page can
// show ("owner key required"); everyone else keeps the plain "unauthorized".
func TestGateRefusesKeylessLocalCaller(t *testing.T) {
	served := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served = true })

	contractFleetSwapGate(t, ownerTestDeps(true))
	rec := contractFleetDo(Gate(inner), http.MethodGet, "/api/board", "", "127.0.0.1:5000", nil)
	if rec.Code != http.StatusUnauthorized || served {
		t.Fatalf("keyless local /api/board: status %d served %v, want 401 not served", rec.Code, served)
	}
	if got := contractFleetJSON(t, "keyless", rec.Body.Bytes())["error"]; got != ownerKeyRequired {
		t.Errorf("keyless local refusal error = %v, want %q", got, ownerKeyRequired)
	}
	rec = contractFleetDo(Gate(inner), http.MethodGet, "/api/editor/open", "", "127.0.0.1:5000", map[string]string{jobs.OwnerKeyHeader: testOwnerKey})
	if !served {
		t.Errorf("keyed local /api/editor/open: status %d, want served", rec.Code)
	}

	served = false
	contractFleetSwapGate(t, ownerTestDeps(false))
	rec = contractFleetDo(Gate(inner), http.MethodGet, "/api/board", "", "203.0.113.4:5000", map[string]string{jobs.OwnerKeyHeader: testOwnerKey})
	if rec.Code != http.StatusUnauthorized || served {
		t.Fatalf("remote /api/board with the key: status %d served %v, want 401", rec.Code, served)
	}
	if got := contractFleetJSON(t, "remote", rec.Body.Bytes())["error"]; got != "unauthorized" {
		t.Errorf("remote refusal error = %v, want unauthorized", got)
	}
}

// GET /api/owner?key= from this Mac with the right key sets the kraken_owner
// cookie and redirects to the board with the key out of the URL; anything
// else sets nothing.
func TestOwnerRouteSetsTheCookie(t *testing.T) {
	if got := policyFor("/api/owner"); got != TierAnon {
		t.Errorf("policyFor(/api/owner) = %s, want anon (the key check is inside)", tierName(got))
	}
	mux := http.NewServeMux()
	registerRoutes(mux)
	h := Gate(mux)

	contractFleetSwapGate(t, ownerTestDeps(true))
	rec := contractFleetDo(h, http.MethodGet, "/api/owner?key="+testOwnerKey, "", "127.0.0.1:5000", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("good key: status %d Location %q, want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == jobs.OwnerCookieName {
			c = ck
		}
	}
	if c == nil {
		t.Fatal("good key: no kraken_owner cookie set")
	}
	if c.Value != testOwnerKey || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 {
		t.Errorf("cookie = %+v, want the key, HttpOnly, SameSite=Strict, Path=/, persistent", c)
	}

	// the cookie it set makes the next request owner
	rec2 := contractFleetDo(h, http.MethodGet, "/api/health", "", "127.0.0.1:5000", map[string]string{"Cookie": c.String()})
	if rec2.Code != http.StatusOK {
		t.Errorf("request with the set cookie: status %d", rec2.Code)
	}
	if tier, _ := classify(ownerReq("127.0.0.1:5000", withCookie(c.Value)), ownerTestDeps(true)); tier != TierOwner {
		t.Errorf("set cookie classifies %s, want owner", tierName(tier))
	}

	refused := func(label, target, remote string, local bool) {
		t.Helper()
		contractFleetSwapGate(t, ownerTestDeps(local))
		rec := contractFleetDo(h, http.MethodGet, target, "", remote, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", label, rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: set cookies %v, want none", label, rec.Result().Cookies())
		}
	}
	refused("wrong key", "/api/owner?key=nope", "127.0.0.1:5000", true)
	refused("no key", "/api/owner", "127.0.0.1:5000", true)
	refused("good key from another machine", "/api/owner?key="+testOwnerKey, "100.64.0.9:5000", false)

	contractFleetSwapGate(t, ownerTestDeps(true))
	rec = contractFleetDo(h, http.MethodPost, "/api/owner?key="+testOwnerKey, "{}", "127.0.0.1:5000", nil)
	if rec.Code != http.StatusMethodNotAllowed || len(rec.Result().Cookies()) != 0 {
		t.Errorf("POST /api/owner: status %d cookies %v, want 405 and none", rec.Code, rec.Result().Cookies())
	}
}

// A browser on this Mac without the owner key still gets the page shell (the
// static files outside /api/), so the board loads and shows how to get the
// key instead of a raw JSON refusal. Every /api/ call still needs the key, and
// a caller that is not local gets nothing more than before.
func TestGateServesTheShellToAKeylessLocalBrowser(t *testing.T) {
	served := 0
	h := Gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }))

	contractFleetSwapGate(t, ownerTestDeps(true))
	for _, p := range []string{"/", "/assets/main-abc.js"} {
		if rec := contractFleetDo(h, http.MethodGet, p, "", "127.0.0.1:5000", nil); rec.Code != http.StatusOK {
			t.Errorf("keyless local %s: status %d, want the shell served", p, rec.Code)
		}
	}
	if served != 2 {
		t.Errorf("shell served %d times, want 2", served)
	}
	if rec := contractFleetDo(h, http.MethodGet, "/api/board", "", "127.0.0.1:5000", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("keyless local /api/board: status %d, want 401", rec.Code)
	}

	contractFleetSwapGate(t, ownerTestDeps(false))
	served = 0
	if rec := contractFleetDo(h, http.MethodGet, "/", "", "203.0.113.4:5000", nil); rec.Code != http.StatusUnauthorized || served != 0 {
		t.Errorf("remote anon /: status %d served %d, want 401 and not served", rec.Code, served)
	}
}
