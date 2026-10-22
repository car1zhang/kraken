package api

// fleet_contract_test.go — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice G4 (API: the request gate, pairing, the node
// and nodes routes, visibility, the merged board, the wire shapes). Authored
// from artifacts/50-contract.md alone; no implementation file was read. LOGIC
// dimension: stated invariants and happy paths, one strict answer each.
// Refusals, rate-limit/lockout windows, nonce expiry, bad bodies, 403/404
// arms and mirror refusals are athena-edge's.
//
// Every test is TestContractFleet…; every package-level identifier is
// prefixed contractFleet.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractFleetEnv wires a scratch store (HOME sandboxed) and pins the env the
// node identity reads: an explicit KRAKEN_TAILSCALE naming a file that does
// not exist (so no test ever execs the machine's real tailscale — NodeSelf
// then degrades to an empty stable id), a fixed device label and operator,
// and the fleet knob unset.
func contractFleetEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("KRAKEN_TAILSCALE", filepath.Join(t.TempDir(), "no-such-tailscale"))
	t.Setenv("KRAKEN_DEVICE_NAME", "contract-node")
	t.Setenv("KRAKEN_OPERATOR", "Carl")
	t.Setenv("KRAKEN_NODES", "")
	os.Unsetenv("KRAKEN_NODES")
	return st
}

func contractFleetMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

// contractFleetDo serves one request. remote "" keeps httptest's default.
func contractFleetDo(h http.Handler, method, target, body, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func contractFleetJSON(t *testing.T, label string, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: body is not a JSON object: %v (%s)", label, err, b)
	}
	return m
}

// contractFleetSeedJob seeds a job (seedJobText via writeTestJob) and, when
// vis is non-empty, lands visibility through jobs.WriteJobFields — the writer
// the contract names (G3 `WriteJobFields(j, "visibility", v)`).
func contractFleetSeedJob(t *testing.T, slug, extra, vis string) {
	t.Helper()
	writeTestJob(t, slug, extra)
	if vis == "" {
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("seed %s vanished", slug)
	}
	if err := jobs.WriteJobFields(j, "visibility", vis); err != nil {
		t.Fatalf("seed %s visibility: %v", slug, err)
	}
}

// contractFleetSwapGate replaces defaultGateDeps for the test.
func contractFleetSwapGate(t *testing.T, d gateDeps) {
	t.Helper()
	orig := defaultGateDeps
	defaultGateDeps = d
	t.Cleanup(func() { defaultGateDeps = orig })
}

var contractFleetPhone = core.TailnetIdentity{ID: "nea6fba5q821CNTRL", Name: "iphone173.tail072ad8.ts.net", Login: "carl@github"}

const contractFleetPhoneIP = "100.101.102.103"

// contractFleetGateDeps: nothing is local; the store resolves tokens; the one
// tailnet device is the phone at contractFleetPhoneIP.
func contractFleetGateDeps(st *store.Store) gateDeps {
	return gateDeps{
		localOrigin: func(string) bool { return false },
		lookupToken: func(hash string) (*store.PairDevice, bool) {
			d, err := st.GetPairDeviceByHash(hash)
			if err != nil {
				return nil, false
			}
			return d, true
		},
		tailnetDevice: func(ip string) (core.TailnetIdentity, bool) {
			if ip == contractFleetPhoneIP {
				return contractFleetPhone, true
			}
			return core.TailnetIdentity{}, false
		},
	}
}

// ---------------------------------------------------------------------------
// wire specs (fieldSpec/assertShape from contract_test.go)
// ---------------------------------------------------------------------------

var contractFleetEnvelopeSpec = map[string]fieldSpec{
	"node_id":  {kind: wireString, required: true},
	"label":    {kind: wireString, required: true},
	"operator": {kind: wireString, required: true},
	"dns_name": {kind: wireString, required: true},
	"ips":      {kind: wireArray, required: true},
	"port":     {kind: wireNumber, required: true},
	"build":    {kind: wireString, required: true},
	"contract": {kind: wireNumber, required: true},
}

var contractFleetNodeJobsSpec = map[string]fieldSpec{
	"node": {kind: wireObject, required: true},
	"jobs": {kind: wireArray, required: true},
}

var contractFleetWebNodeSpec = map[string]fieldSpec{
	"node_id":  {kind: wireString, required: true},
	"label":    {kind: wireString, required: true},
	"operator": {kind: wireString, required: true},
	"dns_name": {kind: wireString, required: true},
	"ip":       {kind: wireString, required: true},
	"port":     {kind: wireNumber, required: true},
	"build":    {kind: wireString, required: true},
	"contract": {kind: wireNumber, required: true},
	"online":   {kind: wireBool, required: true},
	"last_ok":  {kind: wireNumber, required: true},
	"last_err": {kind: wireString},
	"jobs":     {kind: wireArray},
}

var contractFleetNodesSpec = map[string]fieldSpec{
	"nodes": {kind: wireArray, required: true},
}

var contractFleetOwnerSpec = map[string]fieldSpec{
	"node_id":  {kind: wireString, required: true},
	"label":    {kind: wireString, required: true},
	"operator": {kind: wireString, required: true},
	"online":   {kind: wireBool, required: true},
}

var contractFleetChallengeSpec = map[string]fieldSpec{
	"node_id": {kind: wireString, required: true},
	"label":   {kind: wireString, required: true},
	"salt":    {kind: wireString, required: true},
	"iters":   {kind: wireNumber, required: true},
	"nonce":   {kind: wireString, required: true},
}

var contractFleetPairReplySpec = map[string]fieldSpec{
	"token":   {kind: wireString, required: true},
	"node_id": {kind: wireString, required: true},
	"label":   {kind: wireString, required: true},
}

var contractFleetPasswordReplySpec = map[string]fieldSpec{
	"ok":      {kind: wireBool, required: true},
	"revoked": {kind: wireNumber, required: true},
}

var contractFleetDevicesSpec = map[string]fieldSpec{
	"devices": {kind: wireArray, required: true},
}

var contractFleetDeviceSpec = map[string]fieldSpec{
	"id":           {kind: wireNumber, required: true},
	"label":        {kind: wireString, required: true},
	"tailnet_name": {kind: wireString, required: true},
	"created_at":   {kind: wireNumber, required: true},
	"last_seen_at": {kind: wireNumber, required: true},
	"last_ip":      {kind: wireString, required: true},
}

var contractFleetNodeJobSpec = map[string]fieldSpec{
	"job":       {kind: wireObject, required: true},
	"artifacts": {kind: wireArray, required: true},
	"question":  {kind: wireObject},
}

var contractFleetOKSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

// ---------------------------------------------------------------------------
// contract version
// ---------------------------------------------------------------------------

// Contract preamble + G4 `apiContract`: "apiContract becomes 7"; G3
// `NodeContract`: "kept equal to api.apiContract by a test in api".
func TestContractFleetContractIsSeven(t *testing.T) {
	if apiContract != 7 {
		t.Errorf("apiContract = %d, want 7", apiContract)
	}
	if jobs.NodeContract != apiContract {
		t.Errorf("jobs.NodeContract = %d, apiContract = %d; the contract keeps them equal", jobs.NodeContract, apiContract)
	}
}

// ---------------------------------------------------------------------------
// gate: tiers, token extraction, classify, policy
// ---------------------------------------------------------------------------

// Contract G4 `Tier`: "Ordered: a request passes a route when tier >= floor."
func TestContractFleetTierOrder(t *testing.T) {
	if !(TierAnon < TierNode && TierNode < TierPaired && TierPaired < TierOwner) {
		t.Errorf("tiers out of order: anon=%d node=%d paired=%d owner=%d", TierAnon, TierNode, TierPaired, TierOwner)
	}
	if TierAnon != 0 {
		t.Errorf("TierAnon = %d, want iota 0", TierAnon)
	}
}

// Contract G4 `tokenHash`: "Lowercase hex SHA-256." Vector computed with
// Python hashlib.sha256(b"tok-abc").hexdigest().
func TestContractFleetTokenHash(t *testing.T) {
	const want = "0b9f31c5403adf5a5676eafcf2ed391dec857be4b8d54e9ae0923a3d10f86e47"
	if got := tokenHash("tok-abc"); got != want {
		t.Errorf("tokenHash(tok-abc) = %q, want %q", got, want)
	}
}

// Contract G4 `pairTokenFromRequest`: "Authorization: Bearer <t> wins; else
// the kraken_pair cookie value; else ""."
func TestContractFleetPairTokenFromRequest(t *testing.T) {
	both := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	both.Header.Set("Authorization", "Bearer header-token")
	both.AddCookie(&http.Cookie{Name: "kraken_pair", Value: "cookie-token"})
	if got := pairTokenFromRequest(both); got != "header-token" {
		t.Errorf("header+cookie: got %q, want header-token", got)
	}
	cookie := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	cookie.AddCookie(&http.Cookie{Name: "kraken_pair", Value: "cookie-token"})
	if got := pairTokenFromRequest(cookie); got != "cookie-token" {
		t.Errorf("cookie only: got %q, want cookie-token", got)
	}
	none := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	if got := pairTokenFromRequest(none); got != "" {
		t.Errorf("no credential: got %q, want \"\"", got)
	}
}

// Contract G4 `classify`: owner when localOrigin; else paired when the token's
// hash finds a row (identity Name = TailnetName, Login = ""); else node when
// tailnetDevice(ip) is true, with that identity; else anon.
func TestContractFleetClassifyTiers(t *testing.T) {
	const token = "tok-abc"
	var lookedUp []string
	var deviceIPs []string
	deps := func(local, tokenOK, tailnet bool) gateDeps {
		return gateDeps{
			localOrigin: func(string) bool { return local },
			lookupToken: func(hash string) (*store.PairDevice, bool) {
				lookedUp = append(lookedUp, hash)
				if !tokenOK {
					return nil, false
				}
				return &store.PairDevice{ID: 9, Label: "Carl's iPhone", TailnetName: "iphone173", TokenHash: hash}, true
			},
			tailnetDevice: func(ip string) (core.TailnetIdentity, bool) {
				deviceIPs = append(deviceIPs, ip)
				if !tailnet {
					return core.TailnetIdentity{}, false
				}
				return contractFleetPhone, true
			},
		}
	}
	req := func(withToken bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/board", nil)
		r.RemoteAddr = contractFleetPhoneIP + ":51234"
		if withToken {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return r
	}

	if tier, _ := classify(req(true), deps(true, true, true)); tier != TierOwner {
		t.Errorf("local origin: tier %d, want owner", tier)
	}

	lookedUp = nil
	tier, id := classify(req(true), deps(false, true, true))
	if tier != TierPaired {
		t.Errorf("valid token from a tailnet IP: tier %d, want paired", tier)
	}
	if id.Name != "iphone173" || id.Login != "" {
		t.Errorf("paired identity = %+v, want Name=TailnetName iphone173, Login empty", id)
	}
	if len(lookedUp) == 0 || lookedUp[0] != tokenHash(token) {
		t.Errorf("lookupToken got %q, want the token's hash %q", lookedUp, tokenHash(token))
	}

	deviceIPs = nil
	tier, id = classify(req(false), deps(false, false, true))
	if tier != TierNode {
		t.Errorf("tailnet device, no token: tier %d, want node", tier)
	}
	if id != contractFleetPhone {
		t.Errorf("node identity = %+v, want %+v", id, contractFleetPhone)
	}
	if len(deviceIPs) == 0 || deviceIPs[len(deviceIPs)-1] != contractFleetPhoneIP {
		t.Errorf("tailnetDevice asked about %q, want the RemoteAddr host %s", deviceIPs, contractFleetPhoneIP)
	}

	// a token the store does not know falls through to the node tier
	tier, _ = classify(req(true), deps(false, false, true))
	if tier != TierNode {
		t.Errorf("unknown token from a tailnet device: tier %d, want node", tier)
	}

	tier, id = classify(req(false), deps(false, false, false))
	if tier != TierAnon {
		t.Errorf("stranger: tier %d, want anon", tier)
	}
	if id != (core.TailnetIdentity{}) {
		t.Errorf("anon identity = %+v, want zero", id)
	}
}

// Contract G4 `routePolicy`: the exact named entries.
func TestContractFleetRoutePolicyNamedEntries(t *testing.T) {
	want := map[string]Tier{
		"/api/health":         TierAnon,
		"/api/node":           TierNode,
		"/api/node/jobs":      TierNode,
		"/api/node/job":       TierNode,
		"/api/node/job/raw":   TierNode,
		"/api/node/read":      TierNode,
		"/api/node/announce":  TierNode,
		"/api/node/action":    TierNode,
		"/api/pair/challenge": TierNode,
		"/api/pair":           TierNode,
		"/api/pair/password":  TierOwner,
		"/api/pair/devices":   TierOwner,
		"/api/editor/open":    TierOwner,
		"/api/pair/revoke":    TierPaired,
		"/api/artifact/":      TierPaired,
		"/api/upload/":        TierPaired,
		"/api/board":          TierPaired,
		"/api/nodes":          TierNode, // AMENDED 14:25: an unpaired phone fans out from it
		"/api/nodes/action":   TierPaired,
		"/api/nodes/job/raw":  TierPaired,
		"/api/nodes/read":     TierPaired,
		"/api/job/visibility": TierPaired,
	}
	for p, w := range want {
		got, ok := routePolicy[p]
		if !ok {
			t.Errorf("routePolicy has no entry for %s", p)
			continue
		}
		if got != w {
			t.Errorf("routePolicy[%s] = %d, want %d", p, got, w)
		}
	}
}

// Contract G4 `RoutePatterns`: "Every pattern Register registers plus
// /api/artifact/ and /api/upload/; a test asserts each has an explicit
// routePolicy entry." Every other registered pattern is TierPaired unless
// named otherwise.
func TestContractFleetEveryRoutePatternHasPolicy(t *testing.T) {
	pats := RoutePatterns()
	set := map[string]bool{}
	for _, p := range pats {
		set[p] = true
		if _, ok := routePolicy[p]; !ok {
			t.Errorf("pattern %s has no explicit routePolicy entry (shipped open by omission)", p)
		}
	}
	for _, must := range []string{"/api/artifact/", "/api/upload/", "/api/health", "/api/board",
		"/api/node", "/api/node/jobs", "/api/node/action", "/api/nodes", "/api/nodes/action",
		"/api/pair", "/api/pair/challenge", "/api/pair/password", "/api/pair/devices", "/api/pair/revoke",
		"/api/job/visibility"} {
		if !set[must] {
			t.Errorf("RoutePatterns lacks %s", must)
		}
	}
}

// Contract G4 `policyFor`: exact entry; else longest registered prefix entry;
// else any /api/ path → TierOwner (fail closed); else (SPA shell, /assets/) →
// TierNode.
func TestContractFleetPolicyFor(t *testing.T) {
	for _, c := range []struct {
		path string
		want Tier
	}{
		{"/api/health", TierAnon},
		{"/api/node/jobs", TierNode},
		{"/api/pair/password", TierOwner},
		{"/api/board", TierPaired},
		{"/api/artifact/demo-job/00-overview.md", TierPaired},
		{"/api/upload/demo-job/img-001.png", TierPaired},
		{"/api/never-registered-route", TierOwner},
		{"/", TierNode},
		{"/assets/index-abc123.js", TierNode},
	} {
		if got := policyFor(c.path); got != c.want {
			t.Errorf("policyFor(%s) = %d, want %d", c.path, got, c.want)
		}
	}
}

// Contract G4 `Gate`: classifies, stores tier and identity on the context
// (tierOf, identityOf), answers 401 {"error":"unauthorized"} when tier <
// policyFor(path), otherwise serves h.
func TestContractFleetGateServesAtOrAboveFloor(t *testing.T) {
	st := contractFleetEnv(t)
	token := "paired-token-xyz"
	if _, err := st.AddPairDevice(&store.PairDevice{Label: "Carl's iPhone", TailnetName: "iphone173",
		TokenHash: tokenHash(token), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	contractFleetSwapGate(t, contractFleetGateDeps(st))

	type seen struct {
		tier Tier
		id   core.TailnetIdentity
	}
	var mu sync.Mutex
	var got []seen
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{tierOf(r), identityOf(r)})
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	})
	gate := Gate(inner)
	stranger := "203.0.113.9:4000"
	phone := contractFleetPhoneIP + ":51234"
	bearer := map[string]string{"Authorization": "Bearer " + token}

	cases := []struct {
		name     string
		path     string
		remote   string
		hdr      map[string]string
		served   bool
		wantTier Tier
	}{
		{"anon reaches health", "/api/health", stranger, nil, true, TierAnon},
		{"anon refused on board", "/api/board", stranger, nil, false, 0},
		{"node reaches node jobs", "/api/node/jobs", phone, nil, true, TierNode},
		{"node reaches pair challenge", "/api/pair/challenge", phone, nil, true, TierNode},
		{"node reaches the SPA shell", "/", phone, nil, true, TierNode},
		{"node refused on board", "/api/board", phone, nil, false, 0},
		{"paired reaches board", "/api/board", phone, bearer, true, TierPaired},
		{"paired reaches the registry", "/api/nodes", phone, bearer, true, TierPaired},
		{"node reaches the registry (amended)", "/api/nodes", phone, nil, true, TierNode},
		{"node refused on the nodes action proxy", "/api/nodes/action", phone, nil, false, 0},
		{"paired reaches the nodes action proxy", "/api/nodes/action", phone, bearer, true, TierPaired},
		{"paired refused on owner route", "/api/pair/devices", phone, bearer, false, 0},
	}
	for _, c := range cases {
		mu.Lock()
		got = nil
		mu.Unlock()
		rec := contractFleetDo(gate, http.MethodGet, c.path, "", c.remote, c.hdr)
		if c.served {
			if rec.Code != http.StatusTeapot || len(got) != 1 {
				t.Errorf("%s: status %d, inner calls %d; want served", c.name, rec.Code, len(got))
				continue
			}
			if got[0].tier != c.wantTier {
				t.Errorf("%s: tierOf = %d, want %d", c.name, got[0].tier, c.wantTier)
			}
			if c.wantTier == TierNode && got[0].id != contractFleetPhone {
				t.Errorf("%s: identityOf = %+v, want the phone", c.name, got[0].id)
			}
		} else {
			if rec.Code != http.StatusUnauthorized || len(got) != 0 {
				t.Errorf("%s: status %d, inner calls %d; want 401 and not served", c.name, rec.Code, len(got))
				continue
			}
			m := contractFleetJSON(t, c.name, rec.Body.Bytes())
			if !reflect.DeepEqual(m, map[string]any{"error": "unauthorized"}) {
				t.Errorf("%s: 401 body = %v, want {\"error\":\"unauthorized\"}", c.name, m)
			}
		}
	}

	// owner: a local origin reaches the owner-only route
	contractFleetSwapGate(t, gateDeps{
		localOrigin:   func(string) bool { return true },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	})
	gate = Gate(inner)
	got = nil
	rec := contractFleetDo(gate, http.MethodGet, "/api/pair/devices", "", "127.0.0.1:5000", nil)
	if rec.Code != http.StatusTeapot || len(got) != 1 || got[0].tier != TierOwner {
		t.Errorf("owner on /api/pair/devices: status %d, seen %+v; want served at owner", rec.Code, got)
	}
}

// ---------------------------------------------------------------------------
// pairing primitives
// ---------------------------------------------------------------------------

// Contract G4 constants and seams.
func TestContractFleetPairConstants(t *testing.T) {
	if pairIters != 600000 || pairMinPassword != 12 || pairMaxNonces != 20 || pairRateMax != 5 || pairLockFailures != 10 {
		t.Errorf("pair consts = iters %d min %d nonces %d rate %d lock %d; want 600000 12 20 5 10",
			pairIters, pairMinPassword, pairMaxNonces, pairRateMax, pairLockFailures)
	}
	if pairNonceTTL != time.Minute || pairLockout != 10*time.Minute {
		t.Errorf("pairNonceTTL = %v, pairLockout = %v; want 1m, 10m", pairNonceTTL, pairLockout)
	}
	if pairVerifierKey != "pair:verifier" {
		t.Errorf("pairVerifierKey = %q, want pair:verifier", pairVerifierKey)
	}
}

// Python reference (hashlib.pbkdf2_hmac('sha256', b'octopus-garden-2026',
// bytes.fromhex('7f1c3a5b9d2e4f60718293a4b5c6d7e8'), 1000)), computed by this
// author; the amended contract (14:25) carries the same executed vector
// (cbb90fef…594f).
const (
	contractFleetPassword = "octopus-garden-2026"
	contractFleetSaltHex  = "7f1c3a5b9d2e4f60718293a4b5c6d7e8"
	contractFleetKHex     = "cbb90feff1015a0c2eb8d634252cc4cccef587efde7351a75e99138631c6594f"
	contractFleetNonceHex = "00112233445566778899aabbccddeeff"
	// hmac.new(K, nonce + b'nea6fba5q821CNTRL' + b'carl-iphone', sha256)
	contractFleetProofHex = "a0ac8b885181a4718e66c9949f68645e2ef0db3d75eeff027589e4248eccefe4"
)

func contractFleetHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Contract G4 `deriveVerifier`: "pbkdf2.Key(sha256.New, password, salt, iters,
// 32)".
func TestContractFleetDeriveVerifierVector(t *testing.T) {
	got := deriveVerifier(contractFleetPassword, contractFleetHex(t, contractFleetSaltHex), 1000)
	if hex.EncodeToString(got) != contractFleetKHex {
		t.Errorf("deriveVerifier = %x, want %s (Python hashlib)", got, contractFleetKHex)
	}
}

// Contract G4 `pairProof`: "HMAC-SHA256(k, nonce ‖ []byte(nodeID) ‖
// []byte(deviceLabel)), raw concatenation, 32 bytes out."
func TestContractFleetPairProofVector(t *testing.T) {
	k := contractFleetHex(t, contractFleetKHex)
	got := pairProof(k, contractFleetHex(t, contractFleetNonceHex), "nea6fba5q821CNTRL", "carl-iphone")
	if hex.EncodeToString(got) != contractFleetProofHex {
		t.Errorf("pairProof = %x, want %s (Python hmac)", got, contractFleetProofHex)
	}
	if len(got) != 32 {
		t.Errorf("proof is %d bytes, want 32", len(got))
	}
}

// Contract G4 `nonceStore`: "16 random bytes; … consume is true once per
// nonce and false for a second use". (Expiry and cap are athena-edge's.)
func TestContractFleetNonceSingleUse(t *testing.T) {
	n := &nonceStore{}
	a, ok := n.issue("100.101.102.103")
	if !ok || len(a) != 16 {
		t.Fatalf("issue = (%d bytes, %v), want (16, true)", len(a), ok)
	}
	b, ok := n.issue("100.101.102.103")
	if !ok || bytes.Equal(a, b) {
		t.Fatalf("second issue = (%x, %v); want a distinct fresh nonce", b, ok)
	}
	if !n.consume(a) {
		t.Error("first consume of an issued nonce = false, want true")
	}
	if n.consume(a) {
		t.Error("second consume of the same nonce = true, want false")
	}
	if !n.consume(b) {
		t.Error("consuming one nonce spent another")
	}
}

// Contract G4 `pairLimiter`: "allow is false for the 6th call within one
// minute from one ip" — the first five pass, and another ip is unaffected.
func TestContractFleetLimiterAllowsFivePerMinute(t *testing.T) {
	orig := pairNow
	fixed := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	pairNow = func() time.Time { return fixed }
	t.Cleanup(func() { pairNow = orig })

	l := &pairLimiter{}
	for i := 1; i <= 5; i++ {
		if !l.allow("100.70.0.9") {
			t.Fatalf("call %d refused; the first five within a minute pass", i)
		}
	}
	if l.allow("100.70.0.9") {
		t.Error("6th call within one minute allowed")
	}
	if !l.allow("100.70.0.10") {
		t.Error("another ip was limited by the first ip's calls")
	}
}

// ---------------------------------------------------------------------------
// pairing handlers (through the gate, so the tier and identity are real)
// ---------------------------------------------------------------------------

func contractFleetReadVerifier(t *testing.T, st *store.Store) pairVerifier {
	t.Helper()
	raw, err := st.GetKV(pairVerifierKey)
	if err != nil {
		t.Fatalf("GetKV(%s): %v", pairVerifierKey, err)
	}
	var v pairVerifier
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("verifier is not pairVerifier JSON: %v (%s)", err, raw)
	}
	return v
}

// Contract G4 `pairPasswordHandler`: "writes a fresh salt (16 bytes) and
// pairVerifier to kv, then DeleteAllPairDevices; replies 200
// {"ok":true,"revoked":<n>}. Empty: deletes the kv row and revokes all; same
// reply." + `pairVerifier` ("Salt, K are base64 RawURL").
func TestContractFleetPairPasswordSetsVerifierAndRevokes(t *testing.T) {
	st := contractFleetEnv(t)
	owner := gateDeps{
		localOrigin:   func(string) bool { return true },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	}
	contractFleetSwapGate(t, owner)
	h := Gate(contractFleetMux(t))
	for i, hsh := range []string{"h-one", "h-two"} {
		if _, err := st.AddPairDevice(&store.PairDevice{Label: hsh, TokenHash: hsh, CreatedAt: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	const pw = "correct-horse-battery"
	rec := contractFleetDo(h, http.MethodPost, "/api/pair/password", `{"password":"`+pw+`"}`, "127.0.0.1:5000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("set password: status %d body %s", rec.Code, rec.Body)
	}
	assertShape(t, "POST /api/pair/password", rec.Body.Bytes(), contractFleetPasswordReplySpec)
	m := contractFleetJSON(t, "set", rec.Body.Bytes())
	if m["ok"] != true || m["revoked"] != float64(2) {
		t.Errorf("reply = %v, want ok true, revoked 2", m)
	}
	if devs, _ := st.ListPairDevices(); len(devs) != 0 {
		t.Errorf("devices survived a password set: %+v", devs)
	}

	v := contractFleetReadVerifier(t, st)
	if v.Iters != pairIters {
		t.Errorf("verifier iters = %d, want %d", v.Iters, pairIters)
	}
	salt, err := base64.RawURLEncoding.DecodeString(v.Salt)
	if err != nil || len(salt) != 16 {
		t.Fatalf("verifier salt %q: %d bytes, err %v; want 16 RawURL bytes", v.Salt, len(salt), err)
	}
	k, err := base64.RawURLEncoding.DecodeString(v.K)
	if err != nil {
		t.Fatalf("verifier K is not RawURL: %v", err)
	}
	if !bytes.Equal(k, deriveVerifier(pw, salt, pairIters)) {
		t.Error("stored K != deriveVerifier(password, salt, pairIters)")
	}

	// a second set mints a FRESH salt
	rec = contractFleetDo(h, http.MethodPost, "/api/pair/password", `{"password":"`+pw+`"}`, "127.0.0.1:5000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-set: status %d", rec.Code)
	}
	if v2 := contractFleetReadVerifier(t, st); v2.Salt == v.Salt {
		t.Error("re-setting the password reused the salt; the contract says a fresh one")
	}

	// empty clears the verifier and revokes all
	if _, err := st.AddPairDevice(&store.PairDevice{Label: "late", TokenHash: "h-late", CreatedAt: 9}); err != nil {
		t.Fatal(err)
	}
	rec = contractFleetDo(h, http.MethodPost, "/api/pair/password", `{"password":""}`, "127.0.0.1:5000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d body %s", rec.Code, rec.Body)
	}
	m = contractFleetJSON(t, "clear", rec.Body.Bytes())
	if m["ok"] != true || m["revoked"] != float64(1) {
		t.Errorf("clear reply = %v, want ok true, revoked 1", m)
	}
	if _, err := st.GetKV(pairVerifierKey); err == nil {
		t.Error("the verifier kv row survived a clear")
	}
	if devs, _ := st.ListPairDevices(); len(devs) != 0 {
		t.Errorf("devices survived a clear: %+v", devs)
	}
}

// The whole Go-side pairing round: owner sets a password; the phone (node
// tier) fetches a challenge, proves, receives a token whose HASH (only) is
// stored with its device label and tailnet label; the token then classifies
// paired; the phone revokes itself; the row is gone.
// Contract G4 `pairChallengeHandler`, `pairAttemptHandler`,
// `pairRevokeHandler` (paired caller with no id), `classify`.
func TestContractFleetPairRoundTrip(t *testing.T) {
	st := contractFleetEnv(t)
	const pw = "correct-horse-battery"

	// owner sets the password
	contractFleetSwapGate(t, gateDeps{
		localOrigin:   func(string) bool { return true },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	})
	h := Gate(contractFleetMux(t))
	if rec := contractFleetDo(h, http.MethodPost, "/api/pair/password", `{"password":"`+pw+`"}`, "127.0.0.1:5000", nil); rec.Code != http.StatusOK {
		t.Fatalf("set password: %d %s", rec.Code, rec.Body)
	}
	v := contractFleetReadVerifier(t, st)

	// the phone, at the node tier
	contractFleetSwapGate(t, contractFleetGateDeps(st))
	h = Gate(contractFleetMux(t))
	phone := contractFleetPhoneIP + ":51234"

	rec := contractFleetDo(h, http.MethodGet, "/api/pair/challenge", "", phone, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "GET /api/pair/challenge", rec.Body.Bytes(), contractFleetChallengeSpec)
	var ch struct {
		NodeID string `json:"node_id"`
		Label  string `json:"label"`
		Salt   string `json:"salt"`
		Iters  int    `json:"iters"`
		Nonce  string `json:"nonce"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Salt != v.Salt || ch.Iters != pairIters {
		t.Errorf("challenge salt/iters = %q/%d, want the verifier's %q/%d", ch.Salt, ch.Iters, v.Salt, pairIters)
	}
	self := jobs.NodeSelf(7777)
	if ch.NodeID != self.ID || ch.Label != self.Label {
		t.Errorf("challenge node_id/label = %q/%q, want this node's %q/%q", ch.NodeID, ch.Label, self.ID, self.Label)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(ch.Nonce)
	if err != nil || len(nonce) != 16 {
		t.Fatalf("challenge nonce %q: %d bytes, err %v; want 16 RawURL bytes", ch.Nonce, len(nonce), err)
	}

	// the phone computes its proof independently (PBKDF2 + HMAC, stdlib)
	salt, _ := base64.RawURLEncoding.DecodeString(ch.Salt)
	k := deriveVerifier(pw, salt, ch.Iters)
	mac := hmac.New(sha256.New, k)
	mac.Write(nonce)
	mac.Write([]byte(ch.NodeID))
	mac.Write([]byte("Carl's iPhone"))
	proof := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	body, _ := json.Marshal(map[string]string{"nonce": ch.Nonce, "proof": proof, "device_label": "Carl's iPhone"})
	rec = contractFleetDo(h, http.MethodPost, "/api/pair", string(body), phone, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "POST /api/pair", rec.Body.Bytes(), contractFleetPairReplySpec)
	var res struct {
		Token  string `json:"token"`
		NodeID string `json:"node_id"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.NodeID != ch.NodeID || res.Label != ch.Label {
		t.Errorf("attempt reply node %q/%q, want the challenge's %q/%q", res.NodeID, res.Label, ch.NodeID, ch.Label)
	}
	raw, err := base64.RawURLEncoding.DecodeString(res.Token)
	if err != nil || len(raw) != 32 {
		t.Errorf("token %q: %d bytes, err %v; want 32 random bytes RawURL", res.Token, len(raw), err)
	}
	dev, err := st.GetPairDeviceByHash(tokenHash(res.Token))
	if err != nil {
		t.Fatalf("no pair_devices row under the token's hash: %v", err)
	}
	if dev.Label != "Carl's iPhone" || dev.TailnetName != "iphone173" || dev.TokenHash != tokenHash(res.Token) {
		t.Errorf("stored device = %+v, want label Carl's iPhone, tailnet iphone173, hash of the token", dev)
	}
	if dev.CreatedAt == 0 {
		t.Error("stored device has no CreatedAt")
	}
	if strings.Contains(dev.TokenHash, res.Token) || strings.Contains(dev.Label, res.Token) {
		t.Error("the plaintext token was stored")
	}

	// the token now classifies paired
	r := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	r.RemoteAddr = phone
	r.Header.Set("Authorization", "Bearer "+res.Token)
	if tier, _ := classify(r, contractFleetGateDeps(st)); tier != TierPaired {
		t.Errorf("minted token classifies as %d, want paired", tier)
	}

	// self-revoke: a paired caller with no id deletes its own row
	rec = contractFleetDo(h, http.MethodPost, "/api/pair/revoke", `{}`, phone, map[string]string{"Authorization": "Bearer " + res.Token})
	if rec.Code != http.StatusOK {
		t.Fatalf("self revoke: %d %s", rec.Code, rec.Body)
	}
	if m := contractFleetJSON(t, "revoke", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"ok": true}) {
		t.Errorf("revoke reply = %v, want {ok:true}", m)
	}
	if _, err := st.GetPairDeviceByHash(tokenHash(res.Token)); err == nil {
		t.Error("the revoked device's row survives")
	}
	if tier, _ := classify(r, contractFleetGateDeps(st)); tier != TierNode {
		t.Errorf("revoked token now classifies as %d, want node", tier)
	}
}

// Contract G4 `pairRevokeHandler`: "Owner caller with id: DeletePairDevice(id)"
// + `pairDevicesHandler`: "200 {"devices":[{"id","label","tailnet_name",
// "created_at","last_seen_at","last_ip"}]}; devices is [] when none."
func TestContractFleetOwnerListsAndRevokesDevices(t *testing.T) {
	st := contractFleetEnv(t)
	contractFleetSwapGate(t, gateDeps{
		localOrigin:   func(string) bool { return true },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	})
	h := Gate(contractFleetMux(t))
	local := "127.0.0.1:5000"

	rec := contractFleetDo(h, http.MethodGet, "/api/pair/devices", "", local, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("devices: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "GET /api/pair/devices (empty)", rec.Body.Bytes(), contractFleetDevicesSpec)
	if !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), `"devices":[]`) {
		t.Errorf("empty devices body = %s, want devices: []", rec.Body)
	}

	keep, _ := st.AddPairDevice(&store.PairDevice{Label: "iPad", TailnetName: "ipad-pro", TokenHash: "h-ipad", CreatedAt: 100})
	drop, _ := st.AddPairDevice(&store.PairDevice{Label: "iPhone", TailnetName: "iphone173", TokenHash: "h-phone", CreatedAt: 200})
	if err := st.TouchPairDevice(drop, 300, "100.101.102.103"); err != nil {
		t.Fatal(err)
	}
	rec = contractFleetDo(h, http.MethodGet, "/api/pair/devices", "", local, nil)
	var page struct {
		Devices []json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(page.Devices))
	}
	for _, d := range page.Devices {
		assertShape(t, "pair device row", d, contractFleetDeviceSpec)
	}
	second := contractFleetJSON(t, "device", page.Devices[1])
	want := map[string]any{"id": float64(drop), "label": "iPhone", "tailnet_name": "iphone173",
		"created_at": float64(200), "last_seen_at": float64(300), "last_ip": "100.101.102.103"}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("second device = %v, want %v (CreatedAt ascending)", second, want)
	}

	rec = contractFleetDo(h, http.MethodPost, "/api/pair/revoke", `{"id":`+strconv.FormatInt(drop, 10)+`}`, local, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner revoke: %d %s", rec.Code, rec.Body)
	}
	left, _ := st.ListPairDevices()
	if len(left) != 1 || left[0].ID != keep {
		t.Errorf("after revoking %d: %+v, want only %d", drop, left, keep)
	}
}

// ---------------------------------------------------------------------------
// visibility route
// ---------------------------------------------------------------------------

// Contract G4 `webJobVisibility`: "WriteJobFields(j,"visibility",v), …,
// 200 {"ok":true}. Works on a live (working/blocked) job."
func TestContractFleetJobVisibilityRoute(t *testing.T) {
	contractFleetEnv(t)
	writeTestJob(t, "live-vis", "state: working\n")
	mux := contractFleetMux(t)
	for _, v := range []string{"public", "private", "readonly"} {
		rec := contractFleetDo(mux, http.MethodPost, "/api/job/visibility", `{"slug":"live-vis","visibility":"`+v+`"}`, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("visibility %s: %d %s", v, rec.Code, rec.Body)
		}
		if m := contractFleetJSON(t, v, rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"ok": true}) {
			t.Errorf("visibility %s reply = %v, want {ok:true}", v, m)
		}
		j := jobs.JobBySlug(jobs.LoadJobs(), "live-vis")
		if j == nil || j.Visibility != v {
			t.Errorf("after setting %s the job reads %+v", v, j)
		}
		if j != nil && j.State != "working" {
			t.Errorf("visibility write moved the live job to %q", j.State)
		}
	}
}

// ---------------------------------------------------------------------------
// wire: webJob.visibility / owner, webBoard.nodes, webNode, remoteCards
// ---------------------------------------------------------------------------

// Contract G4 `webJob.Owner`, `webJob.Visibility`: "Visibility is always
// present … Owner is absent on every local card." + `webJobOwner` tags.
func TestContractFleetWebJobOwnerAndVisibilityTags(t *testing.T) {
	b, err := json.Marshal(webJob{Visibility: "readonly"})
	if err != nil {
		t.Fatal(err)
	}
	m := contractFleetJSON(t, "local webJob", b)
	if m["visibility"] != "readonly" {
		t.Errorf("local card visibility = %#v, want readonly", m["visibility"])
	}
	if _, ok := m["owner"]; ok {
		t.Error("a card with nil Owner carries an owner key")
	}

	b, _ = json.Marshal(webJob{Visibility: "public", Owner: &webJobOwner{NodeID: "n-b", Label: "studio-mini", Operator: "Jess", Online: true}})
	m = contractFleetJSON(t, "owned webJob", b)
	own, ok := m["owner"].(map[string]any)
	if !ok {
		t.Fatalf("owned card owner = %#v, want an object", m["owner"])
	}
	ob, _ := json.Marshal(own)
	assertShape(t, "webJob.owner", ob, contractFleetOwnerSpec)
	if !reflect.DeepEqual(own, map[string]any{"node_id": "n-b", "label": "studio-mini", "operator": "Jess", "online": true}) {
		t.Errorf("owner = %v", own)
	}
}

// Contract G4 `webNode` ("Jobs is populated only by GET /api/nodes, never on
// the board") + `webBoard.Nodes` ("Absent when the registry is empty").
func TestContractFleetWebBoardNodesWire(t *testing.T) {
	base := webBoard{Daemon: jobs.DaemonStatus{PID: 1}, Jobs: []webJob{}, Contract: apiContract, Goals: newWebGoals()}
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := contractFleetJSON(t, "board", b)["nodes"]; ok {
		t.Error("a board with no registry rows carries a nodes key")
	}

	withNodes := base
	withNodes.Nodes = []webNode{{NodeID: "n-b", Label: "studio-mini", Operator: "Jess", DNSName: "studio-mini.tail072ad8.ts.net",
		IP: "100.70.1.2", Port: 7777, Build: "abc", Contract: 7, Online: true, LastOK: 1_760_000_000_000}}
	b, err = json.Marshal(withNodes)
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]fieldSpec{}
	for k, v := range webBoardSpec {
		spec[k] = v
	}
	spec["nodes"] = fieldSpec{kind: wireArray}
	assertShape(t, "webBoard with nodes", b, spec)
	var board struct {
		Nodes    []json.RawMessage `json:"nodes"`
		Contract int               `json:"contract"`
	}
	if err := json.Unmarshal(b, &board); err != nil {
		t.Fatal(err)
	}
	if board.Contract != 7 || len(board.Nodes) != 1 {
		t.Fatalf("board contract %d, %d nodes; want 7 and 1", board.Contract, len(board.Nodes))
	}
	assertShape(t, "webBoard.nodes[0]", board.Nodes[0], contractFleetWebNodeSpec)
	if _, ok := contractFleetJSON(t, "node", board.Nodes[0])["jobs"]; ok {
		t.Error("a board node with nil Jobs carries a jobs key")
	}
}

// contractFleetCardSlugs returns each card's (slug, owner node_id) as
// "node/slug" (or "slug" when unowned), via the wire form.
func contractFleetCardKeys(t *testing.T, cards []webJob) []string {
	t.Helper()
	var out []string
	for _, c := range cards {
		b, _ := json.Marshal(c)
		m := contractFleetJSON(t, "card", b)
		key, _ := m["slug"].(string)
		if own, ok := m["owner"].(map[string]any); ok {
			key = own["node_id"].(string) + "/" + key
		}
		out = append(out, key)
	}
	return out
}

// Contract G4 `remoteCards`: "For each row with non-empty JobsJSON, decodes the
// array into webJobs and sets Owner from the row (node_id, label, operator,
// online)". Two same-slug cards from two owners are two cards.
func TestContractFleetRemoteCards(t *testing.T) {
	rows := []*store.NodeRow{
		{ID: "n-b", Label: "studio-mini", Operator: "Jess", Online: true,
			JobsJSON: `[{"slug":"dream-2026-10-20","state":"done","visibility":"readonly"},{"slug":"tech-comms","state":"working","visibility":"public"}]`},
		{ID: "n-c", Label: "kitchen-mac", Operator: "Sam", Online: false,
			JobsJSON: `[{"slug":"dream-2026-10-20","state":"done","visibility":"readonly"}]`},
		{ID: "n-d", Label: "never-polled", Operator: "Ann", Online: true, JobsJSON: ""},
	}
	cards := remoteCards(rows)
	keys := contractFleetCardKeys(t, cards)
	want := []string{"n-b/dream-2026-10-20", "n-b/tech-comms", "n-c/dream-2026-10-20"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("remoteCards keys = %v, want %v", keys, want)
	}
	for i, c := range cards {
		if c.Owner == nil {
			t.Fatalf("card %d has no owner", i)
		}
	}
	if *cards[0].Owner != (webJobOwner{NodeID: "n-b", Label: "studio-mini", Operator: "Jess", Online: true}) {
		t.Errorf("card 0 owner = %+v", *cards[0].Owner)
	}
	if *cards[2].Owner != (webJobOwner{NodeID: "n-c", Label: "kitchen-mac", Operator: "Sam", Online: false}) {
		t.Errorf("card 2 owner = %+v", *cards[2].Owner)
	}
	if cards[1].Visibility != "public" || cards[0].Visibility != "readonly" {
		t.Errorf("card visibility not carried: %q %q", cards[0].Visibility, cards[1].Visibility)
	}
}

// Contract G4 `buildWebBoard`: "Appends remoteCards(ListNodes()) after the
// local and archived cards and fills Nodes; with an empty registry the payload
// has no nodes key and no card with owner." (buildWebBoard drags in
// tmux/process discovery; on a machine without tmux it degrades to no peers.)
func TestContractFleetBuildWebBoardMergesRegistry(t *testing.T) {
	st := contractFleetEnv(t)
	contractFleetSeedJob(t, "local-one", "", "")

	empty := buildWebBoard()
	if empty.Nodes != nil && len(empty.Nodes) != 0 {
		t.Errorf("empty registry: board Nodes = %+v, want none", empty.Nodes)
	}
	for _, c := range empty.Jobs {
		if c.Owner != nil {
			t.Errorf("empty registry: a card carries owner %+v", c.Owner)
		}
	}
	if eb, _ := json.Marshal(empty); strings.Contains(string(eb), `"nodes"`) {
		t.Error("empty registry: payload carries a nodes key")
	}

	if err := st.UpsertNode(&store.NodeRow{ID: "n-b", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IP: "100.70.1.2", Port: 7777, Build: "b", Contract: 7,
		FirstSeen: 1, LastSeen: 1, Online: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodePoll("n-b", true, "", `[{"slug":"local-one","state":"working","visibility":"public"}]`, 2); err != nil {
		t.Fatal(err)
	}
	board := buildWebBoard()
	if board.Contract != 7 {
		t.Errorf("board contract = %d, want 7", board.Contract)
	}
	if len(board.Nodes) != 1 || board.Nodes[0].NodeID != "n-b" || !board.Nodes[0].Online || board.Nodes[0].Label != "studio-mini" {
		t.Errorf("board Nodes = %+v, want the one registry row", board.Nodes)
	}
	if len(board.Nodes) == 1 && len(board.Nodes[0].Jobs) != 0 {
		t.Error("board node carries jobs; Jobs is populated only by GET /api/nodes")
	}
	keys := contractFleetCardKeys(t, board.Jobs)
	var local, remote int
	for _, k := range keys {
		switch k {
		case "local-one":
			local++
		case "n-b/local-one":
			remote++
		}
	}
	if local != 1 || remote != 1 {
		t.Errorf("merged board keys = %v; want the local card AND the same-slug remote card", keys)
	}
	if len(keys) > 0 && keys[len(keys)-1] != "n-b/local-one" {
		t.Errorf("remote card is not appended after the local cards: %v", keys)
	}
}

// ---------------------------------------------------------------------------
// node routes (node tier)
// ---------------------------------------------------------------------------

// Contract G4 `nodeSelfHandler`: "else the NodeEnvelope JSON."
func TestContractFleetNodeSelfRoute(t *testing.T) {
	contractFleetEnv(t)
	rec := contractFleetDo(contractFleetMux(t), http.MethodGet, "/api/node", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/node: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "GET /api/node", rec.Body.Bytes(), contractFleetEnvelopeSpec)
	var env jobs.NodeEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Label != "contract-node" || env.Operator != jobs.Operator() || env.Contract != 7 {
		t.Errorf("envelope = %+v, want label contract-node, operator %q, contract 7", env, jobs.Operator())
	}
}

// contractFleetNodeJobs fetches /api/node/jobs and returns slug→visibility.
func contractFleetNodeJobs(t *testing.T, mux http.Handler) map[string]string {
	t.Helper()
	rec := contractFleetDo(mux, http.MethodGet, "/api/node/jobs", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/node/jobs: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "GET /api/node/jobs", rec.Body.Bytes(), contractFleetNodeJobsSpec)
	var reply struct {
		Node json.RawMessage  `json:"node"`
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	assertShape(t, "GET /api/node/jobs .node", reply.Node, contractFleetEnvelopeSpec)
	out := map[string]string{}
	for _, j := range reply.Jobs {
		slug, _ := j["slug"].(string)
		vis, ok := j["visibility"].(string)
		if !ok {
			t.Errorf("card %s lacks visibility", slug)
		}
		if _, ok := j["owner"]; ok {
			t.Errorf("node card %s carries an owner", slug)
		}
		out[slug] = vis
	}
	return out
}

// Contract G4 `nodeJobsHandler`: "jobs are the LOCAL non-archived cards …
// whose EffectiveVisibility() is readonly or public; !FleetEnabled() →
// jobs: []."
func TestContractFleetNodeJobsListsReadonlyAndPublic(t *testing.T) {
	contractFleetEnv(t)
	contractFleetSeedJob(t, "pub-working", "state: working\n", "public")
	contractFleetSeedJob(t, "default-draft", "", "")
	contractFleetSeedJob(t, "ro-done", "state: done\n", "readonly")
	contractFleetSeedJob(t, "priv-blocked", "state: blocked\n", "private")
	mux := contractFleetMux(t)

	got := contractFleetNodeJobs(t, mux)
	want := map[string]string{"pub-working": "public", "default-draft": "readonly", "ro-done": "readonly"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("node jobs = %v, want %v (private withheld; absent visibility reads readonly)", got, want)
	}

	t.Setenv("KRAKEN_NODES", "off")
	if got := contractFleetNodeJobs(t, mux); len(got) != 0 {
		t.Errorf("KRAKEN_NODES=off: node jobs = %v, want []", got)
	}
}

// Contract G4 `nodeJobHandler`: "{"job": webJob, "artifacts": [names],
// "question": webQuestion|absent}".
func TestContractFleetNodeJobDetail(t *testing.T) {
	contractFleetEnv(t)
	contractFleetSeedJob(t, "pub-detail", "state: working\n", "public")
	contractFleetSeedJob(t, "ro-detail", "", "")
	mux := contractFleetMux(t)
	for slug, vis := range map[string]string{"pub-detail": "public", "ro-detail": "readonly"} {
		rec := contractFleetDo(mux, http.MethodGet, "/api/node/job?slug="+slug, "", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/node/job %s: %d %s", slug, rec.Code, rec.Body)
		}
		assertShape(t, "GET /api/node/job", rec.Body.Bytes(), contractFleetNodeJobSpec)
		m := contractFleetJSON(t, slug, rec.Body.Bytes())
		job := m["job"].(map[string]any)
		if job["slug"] != slug || job["visibility"] != vis {
			t.Errorf("%s: job slug/visibility = %v/%v", slug, job["slug"], job["visibility"])
		}
		if _, ok := m["question"]; ok {
			t.Errorf("%s: question present with nothing pending", slug)
		}
	}
}

// Contract G4 `nodeJobRawHandler`: "Same body as GET /api/job/raw for a PUBLIC
// job".
func TestContractFleetNodeJobRawMatchesLocalRaw(t *testing.T) {
	contractFleetEnv(t)
	contractFleetSeedJob(t, "pub-raw", "state: working\ncolor: cyan\n", "public")
	mux := contractFleetMux(t)
	local := contractFleetDo(mux, http.MethodGet, "/api/job/raw?slug=pub-raw", "", "", nil)
	remote := contractFleetDo(mux, http.MethodGet, "/api/node/job/raw?slug=pub-raw", "", "", nil)
	if local.Code != http.StatusOK || remote.Code != http.StatusOK {
		t.Fatalf("raw: local %d, node %d (%s)", local.Code, remote.Code, remote.Body)
	}
	if !bytes.Equal(local.Body.Bytes(), remote.Body.Bytes()) {
		t.Errorf("node raw body differs from local raw:\nlocal %s\nnode  %s", local.Body, remote.Body)
	}
}

// Contract G4 `nodeAnnounceHandler` (AMENDED, review round 1, B1): "the row is
// bound to the CALLER's tailnet identity … `identityOf(r).ID` must … equal
// `env.ID` … Upserts RowFromEnvelope(env, callerIP(r), now) … Replies the self
// envelope 200." The identity is injected through the in-package context key
// the amendment names; corrected by the lead as a test bug against that text.
func TestContractFleetNodeAnnounceUpserts(t *testing.T) {
	st := contractFleetEnv(t)
	env := jobs.NodeEnvelope{ID: "n-b", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IPs: []string{"fd7a:115c:a1e0::5", "100.70.1.2"},
		Port: 7777, Build: "def", Contract: 7}
	body, _ := json.Marshal(env)
	identified := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gateInfo reads the identity only beside a tier, as the gate stores them.
		ctx := context.WithValue(r.Context(), gateTierKey, TierNode)
		ctx = context.WithValue(ctx, gateIdentityKey, core.TailnetIdentity{ID: "n-b"})
		contractFleetMux(t).ServeHTTP(w, r.WithContext(ctx))
	})
	rec := contractFleetDo(identified, http.MethodPost, "/api/node/announce", string(body), "100.70.1.2:40000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("announce: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "POST /api/node/announce reply", rec.Body.Bytes(), contractFleetEnvelopeSpec)
	var self jobs.NodeEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &self); err != nil {
		t.Fatal(err)
	}
	if self.Label != "contract-node" || self.Contract != 7 {
		t.Errorf("announce reply = %+v, want this node's envelope", self)
	}
	row, err := st.GetNode("n-b")
	if err != nil {
		t.Fatalf("announce did not upsert: %v", err)
	}
	if row.IP != "100.70.1.2" || row.Label != "studio-mini" || row.Operator != "Jess" || row.Port != 7777 ||
		row.Build != "def" || row.Contract != 7 || !row.Online || row.DNSName != "studio-mini.tail072ad8.ts.net" {
		t.Errorf("upserted row = %+v", row)
	}
	if row.FirstSeen == 0 || row.FirstSeen != row.LastSeen {
		t.Errorf("FirstSeen/LastSeen = %d/%d, want both now", row.FirstSeen, row.LastSeen)
	}
}

// Contract G4 `nodeActionHandler`: "color → the webJobColor handler with a
// synthesized {"slug","color"} body … Replies exactly what the local path
// replies (… the handler's own body for color/save)." Steer/question/kill need
// a live tmux cell and have no seam here (disclosed).
func TestContractFleetNodeActionColorDispatchesLocally(t *testing.T) {
	contractFleetEnv(t)
	contractFleetSeedJob(t, "pub-color", "state: working\n", "public")
	writeTestJob(t, "twin-color", "state: working\n")
	mux := contractFleetMux(t)

	local := contractFleetDo(mux, http.MethodPost, "/api/job/color", `{"slug":"twin-color","color":"purple"}`, "", nil)
	remote := contractFleetDo(mux, http.MethodPost, "/api/node/action",
		`{"slug":"pub-color","action":"color","payload":{"color":"purple"},"from":{"cell":""}}`, contractFleetPhoneIP+":5000", nil)
	if remote.Code != http.StatusOK {
		t.Fatalf("node action color: %d %s", remote.Code, remote.Body)
	}
	if remote.Code != local.Code || !bytes.Equal(bytes.TrimSpace(remote.Body.Bytes()), bytes.TrimSpace(local.Body.Bytes())) {
		t.Errorf("node action reply %d %s; local color reply %d %s — must be identical",
			remote.Code, remote.Body, local.Code, local.Body)
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "pub-color")
	if j == nil || j.Color != "purple" {
		t.Errorf("remote colour did not land: %+v", j)
	}
}

// ---------------------------------------------------------------------------
// nodes routes (owner/paired) — the registry and the proxy
// ---------------------------------------------------------------------------

// Contract G4 `nodesHandler`: "{"nodes":[webNode…]} with jobs filled from
// jobs_json; [] when none." (Order follows ListNodes: online first.)
func TestContractFleetNodesRoute(t *testing.T) {
	st := contractFleetEnv(t)
	mux := contractFleetMux(t)
	rec := contractFleetDo(mux, http.MethodGet, "/api/nodes", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/nodes: %d %s", rec.Code, rec.Body)
	}
	assertShape(t, "GET /api/nodes (empty)", rec.Body.Bytes(), contractFleetNodesSpec)
	if !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), `"nodes":[]`) {
		t.Errorf("empty registry body = %s, want nodes: []", rec.Body)
	}

	for _, r := range []*store.NodeRow{
		{ID: "n-off", Label: "attic-mac", Operator: "Sam", DNSName: "attic.tail072ad8.ts.net", IP: "100.70.1.9", Port: 7777, Build: "a", Contract: 7, FirstSeen: 1, LastSeen: 1, Online: false},
		{ID: "n-b", Label: "studio-mini", Operator: "Jess", DNSName: "studio-mini.tail072ad8.ts.net", IP: "100.70.1.2", Port: 7777, Build: "b", Contract: 7, FirstSeen: 1, LastSeen: 1, Online: true},
	} {
		if err := st.UpsertNode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetNodePoll("n-b", true, "", `[{"slug":"tech-comms","state":"working","visibility":"public"}]`, 5_000); err != nil {
		t.Fatal(err)
	}
	rec = contractFleetDo(mux, http.MethodGet, "/api/nodes", "", "", nil)
	var page struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(page.Nodes))
	}
	for _, n := range page.Nodes {
		assertShape(t, "GET /api/nodes node", n, contractFleetWebNodeSpec)
	}
	first := contractFleetJSON(t, "first", page.Nodes[0])
	if first["node_id"] != "n-b" || first["online"] != true || first["last_ok"] != float64(5_000) {
		t.Errorf("first node = %v, want the online polled n-b", first)
	}
	jobsArr, ok := first["jobs"].([]any)
	if !ok || len(jobsArr) != 1 || jobsArr[0].(map[string]any)["slug"] != "tech-comms" {
		t.Errorf("n-b jobs = %#v, want its polled card list", first["jobs"])
	}
	second := contractFleetJSON(t, "second", page.Nodes[1])
	if second["node_id"] != "n-off" {
		t.Errorf("second node = %v, want n-off", second["node_id"])
	}
	if _, ok := second["jobs"]; ok {
		t.Error("a never-polled node carries jobs")
	}
}

// contractFleetRemote is a fake sibling kraken recording what the proxy sent.
type contractFleetRemote struct {
	mu     sync.Mutex
	method string
	path   string
	query  map[string]string
	body   []byte
	status int
	reply  string
}

func (f *contractFleetRemote) start(t *testing.T) (*httptest.Server, string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.method, f.path, f.body = r.Method, r.URL.Path, b
		f.query = map[string]string{}
		for k := range r.URL.Query() {
			f.query[k] = r.URL.Query().Get(k)
		}
		status, reply := f.status, f.reply
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	host, portS, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portS)
	return srv, host, port
}

func contractFleetRegisterRemote(t *testing.T, st *store.Store, id, ip string, port int) {
	t.Helper()
	if err := st.UpsertNode(&store.NodeRow{ID: id, Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IP: ip, Port: port, Build: "b", Contract: 7,
		FirstSeen: 1, LastSeen: 1, Online: true}); err != nil {
		t.Fatal(err)
	}
}

// Contract G4 `nodesActionHandler`: "Forwards as POST http://<ip>:<port>/api/
// node/action with from.cell taken from the request's X-Kraken-From-Cell
// header …; relays the remote status and body".
func TestContractFleetNodesActionProxies(t *testing.T) {
	st := contractFleetEnv(t)
	remote := &contractFleetRemote{status: http.StatusForbidden, reply: `{"error":"not public"}`}
	_, host, port := remote.start(t)
	contractFleetRegisterRemote(t, st, "n-b", host, port)
	mux := contractFleetMux(t)

	rec := contractFleetDo(mux, http.MethodPost, "/api/nodes/action",
		`{"node":"n-b","slug":"tech-comms","action":"steer","payload":{"text":"hello"}}`, "",
		map[string]string{"X-Kraken-From-Cell": "agent-cell"})
	if remote.method != http.MethodPost || remote.path != "/api/node/action" {
		t.Fatalf("remote saw %s %s, want POST /api/node/action", remote.method, remote.path)
	}
	var fwd struct {
		Slug    string          `json:"slug"`
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
		From    struct {
			Cell string `json:"cell"`
		} `json:"from"`
	}
	if err := json.Unmarshal(remote.body, &fwd); err != nil {
		t.Fatalf("forwarded body not JSON: %v (%s)", err, remote.body)
	}
	if fwd.Slug != "tech-comms" || fwd.Action != "steer" || fwd.From.Cell != "agent-cell" {
		t.Errorf("forwarded = %+v, want slug tech-comms, action steer, from.cell agent-cell", fwd)
	}
	var payload map[string]any
	if err := json.Unmarshal(fwd.Payload, &payload); err != nil || !reflect.DeepEqual(payload, map[string]any{"text": "hello"}) {
		t.Errorf("forwarded payload = %s, want {\"text\":\"hello\"}", fwd.Payload)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("relayed status %d, want the remote's 403", rec.Code)
	}
	if m := contractFleetJSON(t, "relay", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"error": "not public"}) {
		t.Errorf("relayed body = %v, want the remote's {error: not public}", m)
	}

	// success relays too, and without the header from.cell is empty
	remote.mu.Lock()
	remote.status, remote.reply = http.StatusOK, `{"ok":true}`
	remote.mu.Unlock()
	rec = contractFleetDo(mux, http.MethodPost, "/api/nodes/action",
		`{"node":"n-b","slug":"tech-comms","action":"done","payload":{}}`, "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("relayed status %d, want 200", rec.Code)
	}
	assertShape(t, "relayed ok", rec.Body.Bytes(), contractFleetOKSpec)
	if err := json.Unmarshal(remote.body, &fwd); err != nil {
		t.Fatal(err)
	}
	if fwd.Action != "done" || fwd.From.Cell != "" {
		t.Errorf("second forward = %+v, want action done and an empty from.cell", fwd)
	}
}

// Contract G4 `nodesJobRawHandler`, `nodesReadHandler`: "Same relay shape to
// the remote /api/node/job/raw and /api/node/read."
func TestContractFleetNodesReadAndRawProxy(t *testing.T) {
	st := contractFleetEnv(t)
	remote := &contractFleetRemote{status: http.StatusOK}
	_, host, port := remote.start(t)
	contractFleetRegisterRemote(t, st, "n-b", host, port)
	mux := contractFleetMux(t)

	remote.reply = `{"blocks":[{"kind":"asst","text":"working on it"}],"counted":1}`
	rec := contractFleetDo(mux, http.MethodGet, "/api/nodes/read?node=n-b&slug=tech-comms&n=3", "", "", nil)
	if remote.method != http.MethodGet || remote.path != "/api/node/read" {
		t.Fatalf("remote saw %s %s, want GET /api/node/read", remote.method, remote.path)
	}
	if remote.query["slug"] != "tech-comms" || remote.query["n"] != "3" {
		t.Errorf("forwarded query = %v, want slug=tech-comms n=3", remote.query)
	}
	if rec.Code != http.StatusOK || !reflect.DeepEqual(contractFleetJSON(t, "read", rec.Body.Bytes()),
		contractFleetJSON(t, "remote", []byte(remote.reply))) {
		t.Errorf("relayed read %d %s, want the remote's body", rec.Code, rec.Body)
	}

	remote.reply = `{"text":"---\njob: tech-comms\n---\n"}`
	rec = contractFleetDo(mux, http.MethodGet, "/api/nodes/job/raw?node=n-b&slug=tech-comms", "", "", nil)
	if remote.method != http.MethodGet || remote.path != "/api/node/job/raw" || remote.query["slug"] != "tech-comms" {
		t.Fatalf("remote saw %s %s %v, want GET /api/node/job/raw?slug=tech-comms", remote.method, remote.path, remote.query)
	}
	if rec.Code != http.StatusOK || !reflect.DeepEqual(contractFleetJSON(t, "raw", rec.Body.Bytes()),
		contractFleetJSON(t, "remote", []byte(remote.reply))) {
		t.Errorf("relayed raw %d %s, want the remote's body", rec.Code, rec.Body)
	}
}
