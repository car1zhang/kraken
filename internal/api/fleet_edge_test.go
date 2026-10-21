package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// fleet_edge_test.go — the EDGE dimension of 50-contract.md slice G4 for
// package api: the gate's fail-closed classifications and refusals, the
// policy table's prefix and default rules, nonce single-use / expiry / cap,
// the attempt rate limit and lockout, the password route's refusals, every
// node route's 403/404/409 refusals, the proxy's unknown-node, relayed-error,
// transport-error and timeout paths, and the empty-list encodings. Written
// blind from the contract: gate.go, pair.go, nodes.go, jobvisibility.go,
// board.go and register.go were not read. Happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeFleet`. The nonce store
// and the limiter are process-global, so every test that reaches them uses
// its own source IP.

func edgeFleetEnv(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("KRAKEN_NODES", "")
	t.Setenv("KRAKEN_DEVICE_NAME", "edge-node")
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	return useTestStore(t)
}

// edgeFleetJob creates a job and, when vis is non-empty, lands that
// visibility through the contract's writer.
func edgeFleetJob(t *testing.T, st *store.Store, slug, vis string) {
	t.Helper()
	if _, err := st.CreateJob(&store.Job{Slug: slug, Repo: "monolith", Body: "# Spec\n", Run: 1}); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	if vis == "" {
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not loadable", slug)
	}
	if err := jobs.WriteJobFields(j, "visibility", vis); err != nil {
		t.Fatalf("WriteJobFields visibility: %v", err)
	}
}

func edgeFleetReq(method, target string, body any, remote string) *http.Request {
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case []byte:
		rdr = bytes.NewReader(b)
	case string:
		rdr = bytes.NewReader([]byte(b))
	default:
		raw, _ := json.Marshal(b)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	return req
}

func edgeFleetDo(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// edgeFleetMux is the REAL mux: the mirror gate is a property of the route,
// applied by Register's mirrorGated wrapper, so mirror refusals are driven
// through it rather than through the bare handler.
func edgeFleetMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgeFleetServe(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func edgeFleetErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}
	s, _ := m["error"].(string)
	return s
}

func edgeFleetKeys(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object: %q", rec.Body.String())
	}
	return m
}

// edgeFleetLogsContain reports whether any file under the package's
// quarantined event-log dir contains needle.
func edgeFleetLogsContain(t *testing.T, needle string) bool {
	t.Helper()
	dir := os.Getenv("KRAKEN_LOGS_DIR")
	if dir == "" {
		t.Fatalf("KRAKEN_LOGS_DIR unset; TestMain should set it")
	}
	found := false
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && bytes.Contains(b, []byte(needle)) {
			found = true
		}
		return nil
	})
	return found
}

func edgeFleetJobRow(t *testing.T, st *store.Store, slug string) *store.Job {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug %s: %v", slug, err)
	}
	return j
}

// ---------------------------------------------------------------------------
// tokens and classification
// ---------------------------------------------------------------------------

// `tokenHash`: "Lowercase hex SHA-256." — including the empty token.
func TestEdgeFleetTokenHashLowercaseHex(t *testing.T) {
	for _, tok := range []string{"", "ABCdef", "tok"} {
		sum := sha256.Sum256([]byte(tok))
		want := hex.EncodeToString(sum[:])
		if got := tokenHash(tok); got != want {
			t.Errorf("tokenHash(%q) = %q, want %q", tok, got, want)
		}
	}
}

// `pairTokenFromRequest`: "`Authorization: Bearer <t>` wins; else the
// `kraken_pair` cookie value; else `""`." — a non-Bearer Authorization
// header does not shadow the cookie.
func TestEdgeFleetPairTokenFromRequestPrecedence(t *testing.T) {
	both := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	both.Header.Set("Authorization", "Bearer from-header")
	both.AddCookie(&http.Cookie{Name: "kraken_pair", Value: "from-cookie"})
	if got := pairTokenFromRequest(both); got != "from-header" {
		t.Errorf("header+cookie: %q, want from-header", got)
	}
	basic := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	basic.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	basic.AddCookie(&http.Cookie{Name: "kraken_pair", Value: "from-cookie"})
	if got := pairTokenFromRequest(basic); got != "from-cookie" {
		t.Errorf("Basic header + cookie: %q, want from-cookie", got)
	}
	other := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	other.AddCookie(&http.Cookie{Name: "some_other_cookie", Value: "nope"})
	if got := pairTokenFromRequest(other); got != "" {
		t.Errorf("no header, unrelated cookie: %q, want \"\"", got)
	}
}

func edgeFleetAnyTailnet(ip string) (core.TailnetIdentity, bool) {
	return core.TailnetIdentity{ID: "nANY", Name: "anything.tail072ad8.ts.net", Login: "x@y"}, true
}

// `classify`: "An unparseable `RemoteAddr` is anon." — even with a tailnet
// lookup that would accept any address.
func TestEdgeFleetClassifyUnparseableRemoteAddrIsAnon(t *testing.T) {
	deps := gateDeps{
		localOrigin:   func(string) bool { return false },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: edgeFleetAnyTailnet,
	}
	for _, addr := range []string{"garbage", "", "[::1", "100.64.1.1:port:extra"} {
		req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
		req.RemoteAddr = addr
		if tier, _ := classify(req, deps); tier != TierAnon {
			t.Errorf("RemoteAddr %q: tier %v, want TierAnon", addr, tier)
		}
	}
	// amended: "anon even when a valid token rides the request"
	withToken := deps
	withToken.lookupToken = func(string) (*store.PairDevice, bool) {
		return &store.PairDevice{ID: 1, Label: "iPhone", TailnetName: "iphone173"}, true
	}
	for _, addr := range []string{"garbage", "", "[::1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
		req.RemoteAddr = addr
		req.Header.Set("Authorization", "Bearer valid-token")
		if tier, _ := classify(req, withToken); tier != TierAnon {
			t.Errorf("RemoteAddr %q with a valid token: tier %v, want TierAnon", addr, tier)
		}
	}
}

// `classify`: "Else paired when … `deps.lookupToken(tokenHash(t))` finds a
// row … Else node when `deps.tailnetDevice(ip)` is true, with that
// identity." — a revoked (unknown) token from a tailnet IP is node, carrying
// the tailnet identity rather than any paired one.
func TestEdgeFleetClassifyRevokedTokenFallsToNode(t *testing.T) {
	want := core.TailnetIdentity{ID: "nPHONE", Name: "iphone173.tail072ad8.ts.net", Login: "carl@probook.ai"}
	var looked []string
	deps := gateDeps{
		localOrigin: func(string) bool { return false },
		lookupToken: func(h string) (*store.PairDevice, bool) { looked = append(looked, h); return nil, false },
		tailnetDevice: func(ip string) (core.TailnetIdentity, bool) {
			return want, ip == "100.80.1.1"
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	req.RemoteAddr = "100.80.1.1:51234"
	req.Header.Set("Authorization", "Bearer revoked-token")
	tier, id := classify(req, deps)
	if tier != TierNode {
		t.Fatalf("revoked token from a tailnet IP: tier %v, want TierNode", tier)
	}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if len(looked) == 0 || looked[0] != tokenHash("revoked-token") {
		t.Errorf("lookupToken saw %v, want the token's hash", looked)
	}
}

// `classify`: "Else anon." — an unknown token from a non-tailnet,
// non-local address.
func TestEdgeFleetClassifyUnknownTokenOffTailnetIsAnon(t *testing.T) {
	deps := gateDeps{
		localOrigin:   func(string) bool { return false },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	}
	req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	req.Header.Set("Authorization", "Bearer guessed")
	if tier, _ := classify(req, deps); tier != TierAnon {
		t.Fatalf("tier %v, want TierAnon", tier)
	}
}

// `policyFor`: "The exact entry for `path`; else the longest registered
// prefix entry (patterns ending in `/`); else, for any path under `/api/`,
// `TierOwner` (fail closed); else (the SPA shell and `/assets/`)
// `TierNode`."
func TestEdgeFleetPolicyForFallbacks(t *testing.T) {
	cases := []struct {
		path string
		want Tier
	}{
		{"/api/definitely-not-a-route", TierOwner},
		{"/api/pair/password/extra", TierOwner},
		{"/api/artifact/some-job/00-overview.md", TierPaired},
		{"/api/artifact/some-job/deep/nested/file.png", TierPaired},
		{"/api/upload/some-job-12/img-001.png", TierPaired},
		{"/", TierNode},
		{"/assets/index-abc123.js", TierNode},
		{"/api/health", TierAnon},
		{"/api/pair/revoke", TierPaired},
		{"/api/pair/devices", TierOwner},
		{"/api/editor/open", TierOwner},
		// AMENDED: the registry read is node tier, the three proxies stay paired
		{"/api/nodes", TierNode},
		{"/api/nodes/action", TierPaired},
		{"/api/nodes/job/raw", TierPaired},
		{"/api/nodes/read", TierPaired},
	}
	for _, c := range cases {
		if got := policyFor(c.path); got != c.want {
			t.Errorf("policyFor(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Gate
// ---------------------------------------------------------------------------

func edgeFleetSwapGate(t *testing.T, d gateDeps) {
	t.Helper()
	old := defaultGateDeps
	defaultGateDeps = d
	t.Cleanup(func() { defaultGateDeps = old })
}

// edgeFleetGateDeps: never local; tokens looked up in the real store;
// 100.x callers are tailnet devices.
func edgeFleetGateDeps(st *store.Store, local bool) gateDeps {
	return gateDeps{
		localOrigin: func(string) bool { return local },
		lookupToken: func(h string) (*store.PairDevice, bool) {
			d, err := st.GetPairDeviceByHash(h)
			if err != nil {
				return nil, false
			}
			return d, true
		},
		tailnetDevice: func(ip string) (core.TailnetIdentity, bool) {
			if strings.HasPrefix(ip, "100.") {
				return core.TailnetIdentity{ID: "nT", Name: "tailnet-dev.tail072ad8.ts.net", Login: "jess@x.io"}, true
			}
			return core.TailnetIdentity{}, false
		},
	}
}

type edgeFleetInner struct{ calls int }

func (e *edgeFleetInner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.calls++
	w.WriteHeader(http.StatusTeapot)
}

func edgeFleetAssertRefused(t *testing.T, label string, rec *httptest.ResponseRecorder, inner *edgeFleetInner) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("%s: status %d, want 401", label, rec.Code)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Errorf("%s: 401 body not JSON: %q", label, rec.Body.String())
		return
	}
	if len(m) != 1 || m["error"] != "unauthorized" {
		t.Errorf("%s: 401 body = %s, want {\"error\":\"unauthorized\"}", label, rec.Body.String())
	}
	if inner.calls != 0 {
		t.Errorf("%s: refused request still reached the handler", label)
	}
}

// Q: "owner-only routes are 401 to a paired caller", and an unlisted
// `/api/` path is owner-floored (fail closed) for a paired caller too.
func TestEdgeFleetGateOwnerRoutesRefusePairedCaller(t *testing.T) {
	st := edgeFleetEnv(t)
	const tok = "edge-paired-token-owner-routes"
	if _, err := st.AddPairDevice(&store.PairDevice{Label: "iPhone", TailnetName: "iphone173", TokenHash: tokenHash(tok), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	edgeFleetSwapGate(t, edgeFleetGateDeps(st, false))
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/pair/password"},
		{http.MethodGet, "/api/pair/devices"},
		{http.MethodPost, "/api/editor/open"},
		{http.MethodGet, "/api/edge-unlisted-route"},
	} {
		inner := &edgeFleetInner{}
		req := edgeFleetReq(c.method, c.path, nil, "100.81.0.1:5000")
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		Gate(inner).ServeHTTP(rec, req)
		edgeFleetAssertRefused(t, c.method+" "+c.path, rec, inner)
	}
}

// Node tier is below paired: `/api/board`, `/api/pair/revoke` and the three
// `/api/nodes/*` proxies (all
// `TierPaired`) refuse a tailnet caller with no token, and with a revoked one.
func TestEdgeFleetGateNodeTierRefusedOnPairedRoutes(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSwapGate(t, edgeFleetGateDeps(st, false))
	for _, tok := range []string{"", "edge-revoked-token"} {
		for _, path := range []string{"/api/board", "/api/pair/revoke", "/api/nodes/action", "/api/nodes/job/raw", "/api/nodes/read"} {
			inner := &edgeFleetInner{}
			req := edgeFleetReq(http.MethodGet, path, nil, "100.82.0.1:5000")
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			Gate(inner).ServeHTTP(rec, req)
			edgeFleetAssertRefused(t, "token="+tok+" "+path, rec, inner)
		}
	}
}

// Anon (`/api/health` only): the SPA shell, assets and the node routes are
// all refused to a non-tailnet, non-local caller; health is served.
func TestEdgeFleetGateAnonReachesOnlyHealth(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSwapGate(t, edgeFleetGateDeps(st, false))
	for _, path := range []string{"/", "/assets/index.js", "/api/node", "/api/node/jobs", "/api/pair/challenge", "/api/board"} {
		inner := &edgeFleetInner{}
		rec := httptest.NewRecorder()
		Gate(inner).ServeHTTP(rec, edgeFleetReq(http.MethodGet, path, nil, "203.0.113.20:5000"))
		edgeFleetAssertRefused(t, "anon "+path, rec, inner)
	}
	inner := &edgeFleetInner{}
	rec := httptest.NewRecorder()
	Gate(inner).ServeHTTP(rec, edgeFleetReq(http.MethodGet, "/api/health", nil, "203.0.113.20:5000"))
	if inner.calls != 1 || rec.Code != http.StatusTeapot {
		t.Errorf("anon /api/health: handler calls %d, status %d; want served", inner.calls, rec.Code)
	}
}

// "no credential value (password, verifier, token) ever reaches a log line …
// or an error string" and Gate "Logs a refusal … never the token".
func TestEdgeFleetGateRefusalNeverLogsToken(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSwapGate(t, edgeFleetGateDeps(st, false))
	const tok = "edgeFleetSecretTokenQ7v9ZpX2"
	inner := &edgeFleetInner{}
	req := edgeFleetReq(http.MethodGet, "/api/board", nil, "203.0.113.21:5000")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	Gate(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), tok) {
		t.Errorf("401 body echoes the token")
	}
	if edgeFleetLogsContain(t, tok) {
		t.Errorf("the event log contains the bearer token")
	}
}

// ---------------------------------------------------------------------------
// nonceStore and pairLimiter (zero values — the contract names no
// constructor; disclosed)
// ---------------------------------------------------------------------------

func edgeFleetSwapPairNow(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	old := pairNow
	pairNow = func() time.Time { return now }
	t.Cleanup(func() { pairNow = old })
	return &now
}

var edgeFleetT0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// `consume`: "true once per nonce and false for a second use, an unknown
// nonce, or a nonce older than `pairNonceTTL`".
func TestEdgeFleetNonceSingleUseAndUnknown(t *testing.T) {
	edgeFleetSwapPairNow(t, edgeFleetT0)
	n := &nonceStore{}
	nonce, ok := n.issue("100.83.0.1")
	if !ok {
		t.Fatalf("issue refused on an empty store")
	}
	if !n.consume(nonce) {
		t.Fatalf("first consume = false")
	}
	if n.consume(nonce) {
		t.Errorf("second consume of the same nonce = true")
	}
	if n.consume(make([]byte, 16)) {
		t.Errorf("consume of an unknown nonce = true")
	}
	if n.consume(nil) {
		t.Errorf("consume(nil) = true")
	}
}

// Expiry: past `pairNonceTTL` the nonce is refused. At exactly the TTL it is
// not yet OLDER than it (literal reading of "older than"; disclosed).
func TestEdgeFleetNonceExpiry(t *testing.T) {
	clock := edgeFleetSwapPairNow(t, edgeFleetT0)
	n := &nonceStore{}
	stale, _ := n.issue("100.83.0.2")
	edge, _ := n.issue("100.83.0.2")

	*clock = edgeFleetT0.Add(pairNonceTTL)
	if !n.consume(edge) {
		t.Errorf("nonce exactly pairNonceTTL old was refused")
	}
	*clock = edgeFleetT0.Add(pairNonceTTL + time.Millisecond)
	if n.consume(stale) {
		t.Errorf("nonce older than pairNonceTTL was accepted")
	}
}

// `issue` "refuses (false) when `ip` already holds `pairMaxNonces` live
// nonces" — per ip, and only LIVE ones count.
func TestEdgeFleetNoncePerIPCap(t *testing.T) {
	clock := edgeFleetSwapPairNow(t, edgeFleetT0)
	n := &nonceStore{}
	for i := 0; i < pairMaxNonces; i++ {
		if _, ok := n.issue("100.83.0.3"); !ok {
			t.Fatalf("issue %d refused below the cap", i+1)
		}
	}
	if _, ok := n.issue("100.83.0.3"); ok {
		t.Errorf("issue %d accepted past pairMaxNonces", pairMaxNonces+1)
	}
	if _, ok := n.issue("100.83.0.4"); !ok {
		t.Errorf("a different ip was refused by another ip's cap")
	}
	*clock = edgeFleetT0.Add(pairNonceTTL + time.Second)
	if _, ok := n.issue("100.83.0.3"); !ok {
		t.Errorf("issue still refused after every held nonce expired")
	}
}

// `allow` "is false for the 6th call within one minute from one ip" — per ip,
// and the window passes.
func TestEdgeFleetLimiterSixthCallRefused(t *testing.T) {
	clock := edgeFleetSwapPairNow(t, edgeFleetT0)
	l := &pairLimiter{}
	for i := 1; i <= pairRateMax; i++ {
		if !l.allow("100.84.0.1") {
			t.Fatalf("call %d refused", i)
		}
	}
	if l.allow("100.84.0.1") {
		t.Errorf("call %d within a minute allowed", pairRateMax+1)
	}
	if !l.allow("100.84.0.2") {
		t.Errorf("another ip was rate-limited")
	}
	*clock = edgeFleetT0.Add(61 * time.Second)
	if !l.allow("100.84.0.1") {
		t.Errorf("still refused after the minute passed")
	}
}

// "and false for `pairLockout` after the 10th `fail` from that ip" — nine
// failures do not lock; the tenth does, outlasting the one-minute window,
// and the lockout ends.
func TestEdgeFleetLimiterLockoutAfterTenthFailure(t *testing.T) {
	clock := edgeFleetSwapPairNow(t, edgeFleetT0)
	l := &pairLimiter{}
	const ip = "100.84.0.3"
	for i := 0; i < pairLockFailures-1; i++ {
		l.fail(ip)
	}
	if !l.allow(ip) {
		t.Fatalf("locked after %d failures", pairLockFailures-1)
	}
	l.fail(ip)
	if l.allow(ip) {
		t.Errorf("allowed right after the %dth failure", pairLockFailures)
	}
	*clock = edgeFleetT0.Add(2 * time.Minute)
	if l.allow(ip) {
		t.Errorf("lockout ended with the one-minute window")
	}
	*clock = edgeFleetT0.Add(pairLockout + time.Second)
	if !l.allow(ip) {
		t.Errorf("still locked after pairLockout")
	}
}

// ---------------------------------------------------------------------------
// password, challenge, attempt
// ---------------------------------------------------------------------------

func edgeFleetSetPassword(t *testing.T, pw string) *httptest.ResponseRecorder {
	t.Helper()
	return edgeFleetDo(pairPasswordHandler, edgeFleetReq(http.MethodPost, "/api/pair/password", map[string]any{"password": pw}, "127.0.0.1:40000"))
}

func edgeFleetChallenge(remote string) *httptest.ResponseRecorder {
	return edgeFleetDo(pairChallengeHandler, edgeFleetReq(http.MethodGet, "/api/pair/challenge", nil, remote))
}

// "Password non-empty and shorter than 12 runes → `400 {"error":"password must
// be at least 12 characters"}`" — runes, not bytes; nothing is written.
func TestEdgeFleetPairPasswordTooShort(t *testing.T) {
	edgeFleetEnv(t)
	for _, pw := range []string{"x", "12345678901", strings.Repeat("é", 11), strings.Repeat("🐙", 11)} {
		rec := edgeFleetSetPassword(t, pw)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("password of %d runes: status %d, want 400", len([]rune(pw)), rec.Code)
			continue
		}
		if got := edgeFleetErr(t, rec); got != "password must be at least 12 characters" {
			t.Errorf("password of %d runes: error %q", len([]rune(pw)), got)
		}
		if strings.Contains(rec.Body.String(), pw) {
			t.Errorf("400 body echoes the password")
		}
	}
	if rec := edgeFleetChallenge("100.85.0.1:1"); rec.Code != http.StatusConflict {
		t.Errorf("a refused password still set a verifier: challenge status %d, want 409", rec.Code)
	}
}

// Exactly 12 runes (24 bytes of multi-byte runes) is long enough.
func TestEdgeFleetPairPasswordTwelveRunesAccepted(t *testing.T) {
	edgeFleetEnv(t)
	rec := edgeFleetSetPassword(t, strings.Repeat("é", 12))
	if rec.Code != http.StatusOK {
		t.Fatalf("12-rune password: status %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if rec := edgeFleetChallenge("100.85.0.2:1"); rec.Code != http.StatusOK {
		t.Errorf("after a 12-rune password: challenge status %d, want 200", rec.Code)
	}
}

// "Empty: deletes the kv row and revokes all; same reply." — and challenge
// then answers 409 again.
func TestEdgeFleetPairPasswordEmptyClearsAndRevokes(t *testing.T) {
	st := edgeFleetEnv(t)
	if rec := edgeFleetSetPassword(t, "octopus-garden-2026"); rec.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	for _, h := range []string{"e1", "e2"} {
		if _, err := st.AddPairDevice(&store.PairDevice{Label: h, TokenHash: h, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	rec := edgeFleetSetPassword(t, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d (%s)", rec.Code, rec.Body.String())
	}
	var reply struct {
		OK      bool `json:"ok"`
		Revoked int  `json:"revoked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || !reply.OK || reply.Revoked != 2 {
		t.Errorf("clear reply = %s, want {\"ok\":true,\"revoked\":2}", rec.Body.String())
	}
	if rows, _ := st.ListPairDevices(); len(rows) != 0 {
		t.Errorf("clear left %d paired devices", len(rows))
	}
	if rec := edgeFleetChallenge("100.85.0.3:1"); rec.Code != http.StatusConflict {
		t.Errorf("after clear: challenge status %d, want 409", rec.Code)
	}
	// clearing an already-clear password is the same reply with 0 revoked
	rec = edgeFleetSetPassword(t, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); rec.Code != http.StatusOK || err != nil || !reply.OK || reply.Revoked != 0 {
		t.Errorf("second clear = %d %s, want 200 {\"ok\":true,\"revoked\":0}", rec.Code, rec.Body.String())
	}
}

// "Mirror-gated 403." — and nothing is written; a non-POST is refused.
func TestEdgeFleetPairPasswordMirrorAndMethod(t *testing.T) {
	edgeFleetEnv(t)
	mux := edgeFleetMux(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	if rec := edgeFleetServe(mux, edgeFleetReq(http.MethodPost, "/api/pair/password", map[string]any{"password": "octopus-garden-2026"}, "127.0.0.1:40000")); rec.Code != http.StatusForbidden {
		t.Errorf("mirror: status %d, want 403", rec.Code)
	}
	SetMirrorMode(false)
	if rec := edgeFleetChallenge("100.85.0.4:1"); rec.Code != http.StatusConflict {
		t.Errorf("mirror refusal still wrote a verifier: challenge %d", rec.Code)
	}
	rec := edgeFleetDo(pairPasswordHandler, edgeFleetReq(http.MethodGet, "/api/pair/password", nil, "127.0.0.1:1"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
}

// "No verifier → `409 {"error":"no pairing password set"}`."
func TestEdgeFleetPairChallengeNoVerifier(t *testing.T) {
	edgeFleetEnv(t)
	rec := edgeFleetChallenge("100.85.0.5:1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != "no pairing password set" {
		t.Errorf("error %q", got)
	}
}

// "nonce cap exceeded → `429`" — the 21st live challenge from one ip.
func TestEdgeFleetPairChallengeNonceCap(t *testing.T) {
	edgeFleetEnv(t)
	edgeFleetSwapPairNow(t, edgeFleetT0)
	if rec := edgeFleetSetPassword(t, "octopus-garden-2026"); rec.Code != http.StatusOK {
		t.Fatalf("set: %d", rec.Code)
	}
	for i := 0; i < pairMaxNonces; i++ {
		if rec := edgeFleetChallenge("100.85.1.1:1"); rec.Code != http.StatusOK {
			t.Fatalf("challenge %d: status %d", i+1, rec.Code)
		}
	}
	if rec := edgeFleetChallenge("100.85.1.1:1"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("challenge %d: status %d, want 429", pairMaxNonces+1, rec.Code)
	}
}

func edgeFleetAttempt(remote string, body any) *httptest.ResponseRecorder {
	return edgeFleetDo(pairAttemptHandler, edgeFleetReq(http.MethodPost, "/api/pair", body, remote))
}

var edgeFleetBogusNonce = base64.RawURLEncoding.EncodeToString(make([]byte, 16))

// "Rate limit first: `!allow(ip)` → `429 {"error":"too many attempts"}`. No
// verifier → 409." — the limit is checked before the verifier, so the sixth
// attempt is 429 even with no password set.
func TestEdgeFleetPairAttemptRateLimitBeforeVerifier(t *testing.T) {
	edgeFleetEnv(t)
	edgeFleetSwapPairNow(t, edgeFleetT0)
	body := map[string]any{"nonce": edgeFleetBogusNonce, "proof": "AAAA", "device_label": "iPhone"}
	for i := 1; i <= pairRateMax; i++ {
		if rec := edgeFleetAttempt("100.86.0.1:1", body); rec.Code != http.StatusConflict {
			t.Fatalf("attempt %d with no verifier: status %d, want 409", i, rec.Code)
		}
	}
	rec := edgeFleetAttempt("100.86.0.1:1", body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: status %d, want 429", pairRateMax+1, rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != "too many attempts" {
		t.Errorf("429 error %q", got)
	}
}

// "`consume(nonce)` false → `401 {"error":"no match"}` (and `fail(ip)`)" —
// ten such failures across two minutes lock the ip out, the lockout
// outlasts the rate window, and it ends after `pairLockout`.
func TestEdgeFleetPairAttemptLockout(t *testing.T) {
	edgeFleetEnv(t)
	clock := edgeFleetSwapPairNow(t, edgeFleetT0)
	if rec := edgeFleetSetPassword(t, "octopus-garden-2026"); rec.Code != http.StatusOK {
		t.Fatalf("set: %d", rec.Code)
	}
	const ip = "100.86.0.2:1"
	body := map[string]any{"nonce": edgeFleetBogusNonce, "proof": "AAAA", "device_label": "iPhone"}
	for round := 0; round < 2; round++ {
		*clock = edgeFleetT0.Add(time.Duration(round) * 61 * time.Second)
		for i := 0; i < pairRateMax; i++ {
			rec := edgeFleetAttempt(ip, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("round %d attempt %d: status %d, want 401", round, i+1, rec.Code)
			}
			if got := edgeFleetErr(t, rec); got != "no match" {
				t.Fatalf("round %d attempt %d: error %q, want no match", round, i+1, got)
			}
		}
	}
	tenth := edgeFleetT0.Add(61 * time.Second)
	*clock = tenth.Add(2 * time.Minute)
	if rec := edgeFleetAttempt(ip, body); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 failures: status %d, want 429", rec.Code)
	}
	*clock = tenth.Add(pairLockout + time.Minute)
	if rec := edgeFleetAttempt(ip, body); rec.Code != http.StatusUnauthorized {
		t.Errorf("after the lockout: status %d, want 401", rec.Code)
	}
}

type edgeFleetChal struct {
	NodeID string `json:"node_id"`
	Label  string `json:"label"`
	Salt   string `json:"salt"`
	Iters  int    `json:"iters"`
	Nonce  string `json:"nonce"`
}

func edgeFleetGetChal(t *testing.T, remote string) edgeFleetChal {
	t.Helper()
	rec := edgeFleetChallenge(remote)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body.String())
	}
	var c edgeFleetChal
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// edgeFleetProof computes the phone's proof, base64 RawURL like `nonce`
// (fixed by the amended pairAttemptHandler row).
func edgeFleetProof(t *testing.T, c edgeFleetChal, pw, label string) string {
	t.Helper()
	salt, err := base64.RawURLEncoding.DecodeString(c.Salt)
	if err != nil {
		t.Fatalf("salt not base64 RawURL: %v", err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(c.Nonce)
	if err != nil {
		t.Fatalf("nonce not base64 RawURL: %v", err)
	}
	k := deriveVerifier(pw, salt, c.Iters)
	return base64.RawURLEncoding.EncodeToString(pairProof(k, nonce, c.NodeID, label))
}

// A nonce spent on a mismatched proof cannot be reused by the correct one,
// and neither attempt adds a device.
func TestEdgeFleetPairAttemptNonceSpentByMismatch(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSwapPairNow(t, edgeFleetT0)
	const pw = "octopus-garden-2026"
	if rec := edgeFleetSetPassword(t, pw); rec.Code != http.StatusOK {
		t.Fatalf("set: %d", rec.Code)
	}
	const ip = "100.86.0.3:1"
	c := edgeFleetGetChal(t, ip)
	wrong := edgeFleetProof(t, c, "not-the-password-at-all", "iPhone")
	if rec := edgeFleetAttempt(ip, map[string]any{"nonce": c.Nonce, "proof": wrong, "device_label": "iPhone"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong proof: status %d, want 401", rec.Code)
	}
	right := edgeFleetProof(t, c, pw, "iPhone")
	if rec := edgeFleetAttempt(ip, map[string]any{"nonce": c.Nonce, "proof": right, "device_label": "iPhone"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("correct proof on a spent nonce: status %d, want 401", rec.Code)
	}
	// a proof bound to another device label does not match either
	c2 := edgeFleetGetChal(t, ip)
	other := edgeFleetProof(t, c2, pw, "iPad")
	if rec := edgeFleetAttempt(ip, map[string]any{"nonce": c2.Nonce, "proof": other, "device_label": "iPhone"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("proof for another label: status %d, want 401", rec.Code)
	}
	if rows, _ := st.ListPairDevices(); len(rows) != 0 {
		t.Errorf("refused attempts added %d devices", len(rows))
	}
}

// "the token appears in no log" — after a successful pairing.
func TestEdgeFleetPairAttemptTokenNeverLogged(t *testing.T) {
	edgeFleetEnv(t)
	edgeFleetSwapPairNow(t, edgeFleetT0)
	const pw = "octopus-garden-2026"
	if rec := edgeFleetSetPassword(t, pw); rec.Code != http.StatusOK {
		t.Fatalf("set: %d", rec.Code)
	}
	const ip = "100.86.0.4:1"
	c := edgeFleetGetChal(t, ip)
	rec := edgeFleetAttempt(ip, map[string]any{"nonce": c.Nonce, "proof": edgeFleetProof(t, c, pw, "Carl's iPhone"), "device_label": "Carl's iPhone"})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Token == "" {
		t.Fatalf("pair reply carries no token: %s", rec.Body.String())
	}
	if edgeFleetLogsContain(t, res.Token) {
		t.Errorf("the minted token reached the event log")
	}
	if edgeFleetLogsContain(t, pw) {
		t.Errorf("the password reached the event log")
	}
}

// Amended: "`Label: device_label truncated to 120 runes
// (`core.TruncateOneLine(label, 120)`, never refused)`" — a 300-rune label
// pairs, and the stored label is exactly the truncation. The proof binds the
// label as SENT.
func TestEdgeFleetPairAttemptLongLabelTruncatedNotRefused(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSwapPairNow(t, edgeFleetT0)
	const pw = "octopus-garden-2026"
	if rec := edgeFleetSetPassword(t, pw); rec.Code != http.StatusOK {
		t.Fatalf("set: %d", rec.Code)
	}
	const ip = "100.86.0.5:1"
	long := strings.Repeat("ü", 300)
	c := edgeFleetGetChal(t, ip)
	rec := edgeFleetAttempt(ip, map[string]any{"nonce": c.Nonce, "proof": edgeFleetProof(t, c, pw, long), "device_label": long})
	if rec.Code != http.StatusOK {
		t.Fatalf("300-rune label: status %d (%s), want 200", rec.Code, rec.Body.String())
	}
	rows, err := st.ListPairDevices()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListPairDevices = %d rows, %v", len(rows), err)
	}
	if want := core.TruncateOneLine(long, 120); rows[0].Label != want {
		t.Errorf("stored label = %d runes, want core.TruncateOneLine(label, 120) (%d runes)", len([]rune(rows[0].Label)), len([]rune(want)))
	}
}

// ---------------------------------------------------------------------------
// revoke and list
// ---------------------------------------------------------------------------

// "Paired caller WITH an `id` of another device → `401`." — and that device
// survives.
func TestEdgeFleetPairRevokePairedCannotRevokeOthers(t *testing.T) {
	st := edgeFleetEnv(t)
	const tok = "edge-revoke-caller-token"
	if _, err := st.AddPairDevice(&store.PairDevice{Label: "mine", TokenHash: tokenHash(tok), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	otherID, err := st.AddPairDevice(&store.PairDevice{Label: "theirs", TokenHash: "other-hash", CreatedAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	edgeFleetSwapGate(t, edgeFleetGateDeps(st, false))
	req := edgeFleetReq(http.MethodPost, "/api/pair/revoke", map[string]any{"id": otherID}, "100.87.0.1:1")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	Gate(http.HandlerFunc(pairRevokeHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("paired caller revoking another id: status %d, want 401", rec.Code)
	}
	if rows, _ := st.ListPairDevices(); len(rows) != 2 {
		t.Errorf("devices after refused revoke = %d, want 2", len(rows))
	}
}

// "Owner caller with `id`: `DeletePairDevice(id)`, `404` when none."
func TestEdgeFleetPairRevokeOwnerUnknownID(t *testing.T) {
	st := edgeFleetEnv(t)
	if _, err := st.AddPairDevice(&store.PairDevice{Label: "keep", TokenHash: "keep-hash", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	edgeFleetSwapGate(t, edgeFleetGateDeps(st, true))
	rec := httptest.NewRecorder()
	Gate(http.HandlerFunc(pairRevokeHandler)).ServeHTTP(rec, edgeFleetReq(http.MethodPost, "/api/pair/revoke", map[string]any{"id": 987654}, "127.0.0.1:1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("owner revoking an unknown id: status %d, want 404", rec.Code)
	}
	if rows, _ := st.ListPairDevices(); len(rows) != 1 {
		t.Errorf("devices after unknown-id revoke = %d, want 1", len(rows))
	}
}

// "`devices` is `[]` when none."
func TestEdgeFleetPairDevicesEmptyArray(t *testing.T) {
	edgeFleetEnv(t)
	rec := edgeFleetDo(pairDevicesHandler, edgeFleetReq(http.MethodGet, "/api/pair/devices", nil, "127.0.0.1:1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := string(edgeFleetKeys(t, rec)["devices"]); got != "[]" {
		t.Errorf(`"devices" = %s, want []`, got)
	}
}

// ---------------------------------------------------------------------------
// visibility route
// ---------------------------------------------------------------------------

func edgeFleetVis(slug, vis string) *http.Request {
	return edgeFleetReq(http.MethodPost, "/api/job/visibility", map[string]any{"slug": slug, "visibility": vis}, "127.0.0.1:1")
}

// "`!ValidVisibility` → `400 {"error":"unknown visibility <v>"}`" — and the
// stored value is untouched.
func TestEdgeFleetJobVisibilityRefusesUnknownValue(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "vis-job", "public")
	rec := edgeFleetDo(webJobVisibility, edgeFleetVis("vis-job", "Public"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Public: status %d, want 400", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != "unknown visibility Public" {
		t.Errorf("error %q, want %q", got, "unknown visibility Public")
	}
	for _, v := range []string{"", "everyone", "read-only"} {
		if rec := edgeFleetDo(webJobVisibility, edgeFleetVis("vis-job", v)); rec.Code != http.StatusBadRequest {
			t.Errorf("visibility %q: status %d, want 400", v, rec.Code)
		}
	}
	if got := edgeFleetJobRow(t, st, "vis-job").Visibility; got != "public" {
		t.Errorf("stored visibility = %q after refusals, want public", got)
	}
}

// "unknown slug 404"; "method 405".
func TestEdgeFleetJobVisibilityUnknownSlugAndMethod(t *testing.T) {
	edgeFleetEnv(t)
	if rec := edgeFleetDo(webJobVisibility, edgeFleetVis("ghost-job", "private")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: status %d, want 404", rec.Code)
	}
	rec := edgeFleetDo(webJobVisibility, edgeFleetReq(http.MethodGet, "/api/job/visibility?slug=x", nil, "127.0.0.1:1"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// node routes
// ---------------------------------------------------------------------------

// `nodeSelfHandler`: "`!FleetEnabled()` → `404 {"error":"fleet off"}`".
func TestEdgeFleetNodeSelfFleetOff(t *testing.T) {
	edgeFleetEnv(t)
	t.Setenv("KRAKEN_NODES", " Off ")
	rec := edgeFleetDo(nodeSelfHandler, edgeFleetReq(http.MethodGet, "/api/node", nil, "100.88.0.1:1"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != "fleet off" {
		t.Errorf("error %q, want fleet off", got)
	}
}

// `nodeJobsHandler`: "`!FleetEnabled()` → `jobs: []`" — even with a public
// job on the board.
func TestEdgeFleetNodeJobsFleetOffIsEmpty(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "shared-job", "public")
	t.Setenv("KRAKEN_NODES", "off")
	rec := edgeFleetDo(nodeJobsHandler, edgeFleetReq(http.MethodGet, "/api/node/jobs", nil, "100.88.0.2:1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := string(edgeFleetKeys(t, rec)["jobs"]); got != "[]" {
		t.Errorf(`fleet off: "jobs" = %s, want []`, got)
	}
}

// Only private and archived jobs: the list is `[]`, never null, never the
// withheld cards.
func TestEdgeFleetNodeJobsWithholdsPrivateAndArchived(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "secret-job", "private")
	edgeFleetJob(t, st, "old-job", "public")
	if _, err := st.ArchiveJob(edgeFleetJobRow(t, st, "old-job").ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	rec := edgeFleetDo(nodeJobsHandler, edgeFleetReq(http.MethodGet, "/api/node/jobs", nil, "100.88.0.3:1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	keys := edgeFleetKeys(t, rec)
	if got := string(keys["jobs"]); got != "[]" {
		t.Errorf(`"jobs" = %s, want []`, got)
	}
	if _, ok := keys["node"]; !ok {
		t.Errorf(`reply has no "node" envelope`)
	}
}

// `nodeJobHandler`: "private, archived or unknown → `404 {"error":"no such
// job"}` (identical text for all three)".
func TestEdgeFleetNodeJobPrivateArchivedUnknownIndistinguishable(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "secret-job", "private")
	edgeFleetJob(t, st, "old-job", "public")
	if _, err := st.ArchiveJob(edgeFleetJobRow(t, st, "old-job").ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	var bodies []string
	for _, slug := range []string{"secret-job", "old-job", "never-existed"} {
		rec := edgeFleetDo(nodeJobHandler, edgeFleetReq(http.MethodGet, "/api/node/job?slug="+slug, nil, "100.88.0.4:1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", slug, rec.Code)
		}
		if got := edgeFleetErr(t, rec); got != "no such job" {
			t.Errorf("%s: error %q, want no such job", slug, got)
		}
		bodies = append(bodies, strings.TrimSpace(rec.Body.String()))
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("404 bodies differ: %q", bodies)
	}
}

// raw and read (round 2, remote parity): "readonly and public answer; private
// and unknown → 404 no such job" — a job with no visibility line reads as
// readonly. Before round 2 a readonly job answered 403 `not public` here;
// the operator's steer opened every READ to readonly cards, so raw answers
// the local body and read reaches its own 409 (these jobs have no live zeus).
// Corrected by the lead as an obsoleted pin.
func TestEdgeFleetNodeRawAndReadAnswerReadonlyRefusePrivate(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	edgeFleetJob(t, st, "default-job", "")
	edgeFleetJob(t, st, "secret-job", "private")
	for name, h := range map[string]http.HandlerFunc{"/api/node/job/raw": nodeJobRawHandler, "/api/node/read": nodeReadHandler} {
		for _, slug := range []string{"ro-job", "default-job"} {
			rec := edgeFleetDo(h, edgeFleetReq(http.MethodGet, name+"?slug="+slug, nil, "100.88.0.5:1"))
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
				t.Errorf("%s %s: status %d, want a readonly job answered (not 403/404)", name, slug, rec.Code)
				continue
			}
			if name == "/api/node/job/raw" && rec.Code != http.StatusOK {
				t.Errorf("%s %s: status %d, want 200", name, slug, rec.Code)
			}
			if name == "/api/node/read" && rec.Code != http.StatusConflict {
				t.Errorf("%s %s: status %d, want 409 (no live zeus)", name, slug, rec.Code)
			}
		}
		for _, slug := range []string{"secret-job", "never-existed"} {
			if rec := edgeFleetDo(h, edgeFleetReq(http.MethodGet, name+"?slug="+slug, nil, "100.88.0.5:1")); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", name, slug, rec.Code)
			}
		}
	}
}

// `nodeReadHandler`: "no live zeus → `409`" — a public draft has no cell.
func TestEdgeFleetNodeReadPublicWithoutZeusIs409(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "pub-draft", "public")
	rec := edgeFleetDo(nodeReadHandler, edgeFleetReq(http.MethodGet, "/api/node/read?slug=pub-draft&n=5", nil, "100.88.0.6:1"))
	if rec.Code != http.StatusConflict {
		t.Errorf("status %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

func edgeFleetEnvelope(id string, ips []string) jobs.NodeEnvelope {
	return jobs.NodeEnvelope{ID: id, Label: "studio-mini", Operator: "Jess", DNSName: "studio-mini.tail072ad8.ts.net", IPs: ips, Port: 7777, Build: "abc", Contract: 7}
}

// `nodeAnnounceHandler`: "Empty `node_id` → 400." — and nothing is upserted.
func TestEdgeFleetNodeAnnounceEmptyID(t *testing.T) {
	st := edgeFleetEnv(t)
	rec := edgeFleetDo(nodeAnnounceHandler, edgeFleetReq(http.MethodPost, "/api/node/announce", edgeFleetEnvelope("", []string{"100.89.0.1"}), "100.89.0.1:1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
	if rows, _ := st.ListNodes(); len(rows) != 0 {
		t.Errorf("an empty node_id upserted %d rows", len(rows))
	}
}

// AMENDED (review round 1, B1): "the row's `IP` is always the caller's own
// address; the body's `IPs` are ... never used as its address", and the caller's
// identity must name the announced id — a body-listed tailnet IP is ignored
// even when it would have been accepted before. Corrected by the lead as a
// test bug against the amended text (the old row was the defect).
func TestEdgeFleetNodeAnnounceIPSelection(t *testing.T) {
	st := edgeFleetEnv(t)
	cases := []struct {
		id, remote string
		ips        []string
		want       string
	}{
		{"nA", "100.89.1.1:1", []string{"192.168.1.5", "fd7a:115c:a1e0::5", "100.89.2.2", "100.89.3.3"}, "100.89.1.1"},
		{"nB", "100.89.1.2:1", []string{"10.0.0.4", "100.128.0.1"}, "100.89.1.2"},
		{"nC", "100.89.1.3:1", nil, "100.89.1.3"},
	}
	for _, c := range cases {
		req := edgeFleetReq(http.MethodPost, "/api/node/announce", edgeFleetEnvelope(c.id, c.ips), c.remote)
		ctx := context.WithValue(req.Context(), gateTierKey, TierNode) // gateInfo reads the identity only beside a tier
		req = req.WithContext(context.WithValue(ctx, gateIdentityKey, core.TailnetIdentity{ID: c.id}))
		rec := edgeFleetDo(nodeAnnounceHandler, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d (%s)", c.id, rec.Code, rec.Body.String())
			continue
		}
		n, err := st.GetNode(c.id)
		if err != nil {
			t.Errorf("%s: GetNode: %v", c.id, err)
			continue
		}
		if n.IP != c.want {
			t.Errorf("%s: row IP %q, want %q", c.id, n.IP, c.want)
		}
	}
}

// Announce is "Mirror-gated (it writes)".
func TestEdgeFleetNodeAnnounceMirror(t *testing.T) {
	st := edgeFleetEnv(t)
	mux := edgeFleetMux(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	rec := edgeFleetServe(mux, edgeFleetReq(http.MethodPost, "/api/node/announce", edgeFleetEnvelope("nM", []string{"100.89.4.4"}), "100.89.4.4:1"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("mirror: status %d, want 403", rec.Code)
	}
	if rows, _ := st.ListNodes(); len(rows) != 0 {
		t.Errorf("mirror announce upserted %d rows", len(rows))
	}
}

func edgeFleetAction(slug, action string, payload map[string]any) *http.Request {
	if payload == nil {
		payload = map[string]any{}
	}
	return edgeFleetReq(http.MethodPost, "/api/node/action", map[string]any{
		"slug": slug, "action": action, "payload": payload, "from": map[string]any{"cell": ""},
	}, "100.90.0.1:1")
}

// `nodeActionHandler` (amended): "a `readonly` job … → `403 {"error":"not
// public"}`" — explicit readonly and the default alike, and nothing changes.
func TestEdgeFleetNodeActionNonPublicRefusedUnchanged(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	edgeFleetJob(t, st, "default-job", "")
	for _, slug := range []string{"ro-job", "default-job"} {
		before := edgeFleetJobRow(t, st, slug)
		for _, a := range []struct {
			action  string
			payload map[string]any
		}{
			{"done", nil},
			{"color", map[string]any{"color": "red"}},
			{"archive", nil},
			{"steer", map[string]any{"text": "hello"}},
		} {
			rec := edgeFleetDo(nodeActionHandler, edgeFleetAction(slug, a.action, a.payload))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, want 403", slug, a.action, rec.Code)
				continue
			}
			if got := edgeFleetErr(t, rec); got != "not public" {
				t.Errorf("%s %s: error %q, want not public", slug, a.action, got)
			}
		}
		after, err := st.GetJobBySlug(slug)
		if err != nil {
			t.Fatalf("%s vanished after refused actions: %v", slug, err)
		}
		if after.Rev != before.Rev || after.State != before.State || after.Color != before.Color {
			t.Errorf("%s changed under refused actions: rev %d→%d state %q→%q color %q→%q",
				slug, before.Rev, after.Rev, before.State, after.State, before.Color, after.Color)
		}
	}
}

// `nodeActionHandler` (amended): "Unknown slug, archived slug, or a PRIVATE
// job → `404 {"error":"no such job"}` (… private is indistinguishable from
// absent on every node-tier route …)" — identical bodies, and the private
// job is untouched.
func TestEdgeFleetNodeActionPrivateArchivedUnknownAre404(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "secret-job", "private")
	edgeFleetJob(t, st, "old-job", "public")
	if _, err := st.ArchiveJob(edgeFleetJobRow(t, st, "old-job").ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	before := edgeFleetJobRow(t, st, "secret-job")
	var bodies []string
	for _, slug := range []string{"secret-job", "old-job", "never-existed"} {
		for _, a := range []struct {
			action  string
			payload map[string]any
		}{
			{"done", nil},
			{"color", map[string]any{"color": "red"}},
			{"steer", map[string]any{"text": "hello"}},
			{"bogus", nil},
		} {
			rec := edgeFleetDo(nodeActionHandler, edgeFleetAction(slug, a.action, a.payload))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", slug, a.action, rec.Code)
				continue
			}
			if got := edgeFleetErr(t, rec); got != "no such job" {
				t.Errorf("%s %s: error %q, want no such job", slug, a.action, got)
			}
			bodies = append(bodies, strings.TrimSpace(rec.Body.String()))
		}
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Errorf("404 bodies differ: %q", bodies)
			break
		}
	}
	after := edgeFleetJobRow(t, st, "secret-job")
	if after.Rev != before.Rev || after.State != before.State || after.Color != before.Color {
		t.Errorf("private job changed under refused actions: rev %d→%d state %q→%q color %q→%q",
			before.Rev, after.Rev, before.State, after.State, before.Color, after.Color)
	}
}

// "Unknown slug → 404; readonly → 403; `action` not in … → 400" —
// in that order, and a refused unknown action changes nothing.
func TestEdgeFleetNodeActionRefusalOrder(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "pub-job", "public")
	edgeFleetJob(t, st, "ro-job", "readonly")
	if rec := edgeFleetDo(nodeActionHandler, edgeFleetAction("never-existed", "bogus", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug + unknown action: status %d, want 404", rec.Code)
	}
	if rec := edgeFleetDo(nodeActionHandler, edgeFleetAction("ro-job", "bogus", nil)); rec.Code != http.StatusForbidden {
		t.Errorf("readonly + unknown action: status %d, want 403", rec.Code)
	}
	before := edgeFleetJobRow(t, st, "pub-job")
	for _, a := range []string{"bogus", "", "Steer", "delete", "reactivate", "unarchive"} {
		if rec := edgeFleetDo(nodeActionHandler, edgeFleetAction("pub-job", a, nil)); rec.Code != http.StatusBadRequest {
			t.Errorf("public + action %q: status %d, want 400", a, rec.Code)
		}
	}
	if after := edgeFleetJobRow(t, st, "pub-job"); after.Rev != before.Rev || after.State != before.State {
		t.Errorf("unknown actions changed the job: rev %d→%d state %q→%q", before.Rev, after.Rev, before.State, after.State)
	}
}

// "Mirror-gated like the local actions." — a public job's colour is not
// written from a mirror.
func TestEdgeFleetNodeActionMirror(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "pub-job", "public")
	mux := edgeFleetMux(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	rec := edgeFleetServe(mux, edgeFleetAction("pub-job", "color", map[string]any{"color": "red"}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("mirror: status %d, want 403", rec.Code)
	}
	if got := edgeFleetJobRow(t, st, "pub-job").Color; got != "" {
		t.Errorf("mirror action wrote color %q", got)
	}
}

// ---------------------------------------------------------------------------
// proxy routes
// ---------------------------------------------------------------------------

func edgeFleetSeedRemote(t *testing.T, st *store.Store, id, rawURL string) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNode(&store.NodeRow{ID: id, Label: id, Operator: "Jess", IP: host, Port: port, Contract: 7, FirstSeen: 1, LastSeen: 1, Online: true}); err != nil {
		t.Fatal(err)
	}
}

func edgeFleetProxyAction(node string) *http.Request {
	return edgeFleetReq(http.MethodPost, "/api/nodes/action", map[string]any{
		"node": node, "slug": "x", "action": "steer", "payload": map[string]any{"text": "hi"},
	}, "127.0.0.1:1")
}

// "Unknown `node` → 404." — for the action proxy and both read relays. The
// `node` field is read as the registry stable id (disclosed silence).
func TestEdgeFleetProxyUnknownNode(t *testing.T) {
	edgeFleetEnv(t)
	if rec := edgeFleetDo(nodesActionHandler, edgeFleetProxyAction("ghost-node")); rec.Code != http.StatusNotFound {
		t.Errorf("/api/nodes/action unknown node: status %d, want 404", rec.Code)
	}
	if rec := edgeFleetDo(nodesJobRawHandler, edgeFleetReq(http.MethodGet, "/api/nodes/job/raw?node=ghost-node&slug=x", nil, "127.0.0.1:1")); rec.Code != http.StatusNotFound {
		t.Errorf("/api/nodes/job/raw unknown node: status %d, want 404", rec.Code)
	}
	if rec := edgeFleetDo(nodesReadHandler, edgeFleetReq(http.MethodGet, "/api/nodes/read?node=ghost-node&slug=x&n=5", nil, "127.0.0.1:1")); rec.Code != http.StatusNotFound {
		t.Errorf("/api/nodes/read unknown node: status %d, want 404", rec.Code)
	}
}

// "relays the remote status and body" — the sibling's 403 arrives verbatim.
func TestEdgeFleetProxyRelaysRemoteRefusal(t *testing.T) {
	st := edgeFleetEnv(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"not public"}`))
	}))
	defer remote.Close()
	edgeFleetSeedRemote(t, st, "nRELAY", remote.URL)
	rec := edgeFleetDo(nodesActionHandler, edgeFleetProxyAction("nRELAY"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want the remote's 403", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != "not public" {
		t.Errorf("relayed error %q, want not public", got)
	}
}

// "a transport error → `502 {"error": "<err>"}`" — a sibling whose port
// refuses connections.
func TestEdgeFleetProxyTransportErrorIs502(t *testing.T) {
	st := edgeFleetEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	edgeFleetSeedRemote(t, st, "nDOWN", "http://"+addr)
	rec := edgeFleetDo(nodesActionHandler, edgeFleetProxyAction("nDOWN"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got == "" {
		t.Errorf("502 carries no error text")
	}
}

// "timeout 5 s" — a sibling that never answers is a transport error within
// the timeout, never a hang.
func TestEdgeFleetProxyTimeoutIs502(t *testing.T) {
	st := edgeFleetEnv(t)
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(20 * time.Second):
		}
	}))
	defer remote.Close()
	defer close(release)
	edgeFleetSeedRemote(t, st, "nHANG", remote.URL)
	start := time.Now()
	rec := edgeFleetDo(nodesActionHandler, edgeFleetProxyAction("nHANG"))
	elapsed := time.Since(start)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
	if elapsed > 8*time.Second {
		t.Errorf("proxy took %v against a hung sibling, want ~5s", elapsed)
	}
}

// ---------------------------------------------------------------------------
// registry on the wire
// ---------------------------------------------------------------------------

// `nodesHandler`: "`[]` when none."
func TestEdgeFleetNodesEmptyArray(t *testing.T) {
	edgeFleetEnv(t)
	rec := edgeFleetDo(nodesHandler, edgeFleetReq(http.MethodGet, "/api/nodes", nil, "127.0.0.1:1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := string(edgeFleetKeys(t, rec)["nodes"]); got != "[]" {
		t.Errorf(`"nodes" = %s, want []`, got)
	}
}

// `remoteCards`: rows with empty `JobsJSON` contribute nothing, and "an
// undecodable `jobs_json` contributes nothing" (including a JSON object where
// an array belongs) — without costing the good row its cards.
func TestEdgeFleetRemoteCardsSkipsEmptyAndUndecodable(t *testing.T) {
	if got := remoteCards(nil); len(got) != 0 {
		t.Errorf("remoteCards(nil) = %d cards", len(got))
	}
	rows := []*store.NodeRow{
		{ID: "nEMPTY", Label: "empty", JobsJSON: ""},
		{ID: "nBAD", Label: "bad", JobsJSON: "not json"},
		{ID: "nOBJ", Label: "obj", JobsJSON: `{"slug":"x"}`},
		{ID: "nGOOD", Label: "studio-mini", Operator: "Jess", Online: false,
			JobsJSON: `[{"slug":"dream-2026-10-20","repo":"kraken","state":"working","live":true,"mtime":1,"visibility":"readonly"}]`},
	}
	cards := remoteCards(rows)
	if len(cards) != 1 {
		t.Fatalf("remoteCards = %d cards, want 1 (only the decodable row)", len(cards))
	}
	if cards[0].Owner == nil || cards[0].Owner.NodeID != "nGOOD" || cards[0].Owner.Online {
		t.Errorf("card owner = %+v, want nGOOD offline", cards[0].Owner)
	}
}
