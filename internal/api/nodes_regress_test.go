package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// Regression pins for defects fixed in review rounds 1-2 (not spec tests):
// an announce bound to the body rather than the caller, a relay that echoed a
// sibling's Content-Type, and a board (or /api/nodes) that rendered a stale
// registry with the fleet switched off or repeated fields a sibling should not
// have served. Remote-parity review rounds 1-3 added six: artifact replies
// served without a sandbox CSP, a live-cell artifact read that followed
// symlinks, duplicate artifact names passed through from a sibling, a
// non-JSON POST the gate ran as owner, a cross-origin multipart POST to the
// uploads, and an artifact over the relay cap read whole on its owner.

// regressRequest is a request carrying a gate classification, as Gate would
// attach it.
func regressRequest(method, path, body string, tier Tier, id core.TailnetIdentity) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "100.64.1.2:4242"
	ctx := context.WithValue(r.Context(), gateTierKey, tier)
	ctx = context.WithValue(ctx, gateIdentityKey, id)
	return r.WithContext(ctx)
}

func TestNodesRegressAnnounceRefusesAnIDTheCallerDoesNotHold(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "")
	orig := nodeSelf
	nodeSelf = func() jobs.NodeEnvelope { return jobs.NodeEnvelope{ID: "self-id", IPs: []string{}} }
	t.Cleanup(func() { nodeSelf = orig })

	body := `{"node_id":"victim-id","label":"x","port":7777}`
	rec := httptest.NewRecorder()
	nodeAnnounceHandler(rec, regressRequest(http.MethodPost, "/api/node/announce", body, TierNode, core.TailnetIdentity{ID: "caller-id"}))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "node id does not match caller") {
		t.Fatalf("announce of another node's id = %d %s, want 403 node id does not match caller", rec.Code, rec.Body.String())
	}
	if _, err := st.GetNode("victim-id"); err == nil {
		t.Fatal("a refused announce still recorded a row")
	}

	// the caller's own id is recorded, under the caller's address
	rec = httptest.NewRecorder()
	nodeAnnounceHandler(rec, regressRequest(http.MethodPost, "/api/node/announce",
		`{"node_id":"caller-id","label":"x","port":7777,"ips":["100.99.99.99"]}`, TierNode, core.TailnetIdentity{ID: "caller-id"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("own-id announce = %d %s, want 200", rec.Code, rec.Body.String())
	}
	n, err := st.GetNode("caller-id")
	if err != nil || n.IP != "100.64.1.2" {
		t.Fatalf("recorded row = %+v, %v; want ip 100.64.1.2 (the caller's, not the body's)", n, err)
	}
}

func TestNodesRegressRelayForcesJSON(t *testing.T) {
	orig := nodesProxyDo
	t.Cleanup(func() { nodesProxyDo = orig })
	node := &store.NodeRow{ID: "n1", Label: "studio"}
	req := httptest.NewRequest(http.MethodGet, "http://100.64.1.3:7777/api/node/read", nil)

	reply := func(status int, ct, body string) {
		nodesProxyDo = func(*http.Request, time.Duration) (*http.Response, error) {
			h := http.Header{}
			h.Set("Content-Type", ct)
			return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	}

	reply(http.StatusOK, "text/html", `{"ok":true}`)
	rec := httptest.NewRecorder()
	relay(rec, node, req, time.Second)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("relayed JSON = %d, CT %q, nosniff %q; want 200 application/json nosniff",
			rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("X-Content-Type-Options"))
	}

	reply(http.StatusOK, "text/html", `<script>alert(1)</script>`)
	rec = httptest.NewRecorder()
	relay(rec, node, req, time.Second)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "studio answered non-JSON") {
		t.Fatalf("non-JSON body = %d %s, want 502 naming the sibling", rec.Code, rec.Body.String())
	}

	reply(http.StatusUnauthorized, "application/json", `{"error":"unauthorized"}`)
	rec = httptest.NewRecorder()
	relay(rec, node, req, time.Second)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "did not recognise this node") {
		t.Fatalf("sibling 401 = %d %s, want 502", rec.Code, rec.Body.String())
	}
}

func TestNodesRegressFleetOffShowsNoRemoteCards(t *testing.T) {
	st := useTestStore(t)
	if err := st.UpsertNode(&store.NodeRow{ID: "n1", Label: "studio", Online: true, LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodePoll("n1", true, "", `[{"slug":"a","visibility":"public"}]`, 2); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KRAKEN_NODES", "")
	if cards, nodes := fleetBoard(st); len(cards) != 1 || len(nodes) != 1 || cards[0].Owner == nil {
		t.Fatalf("fleet on: %d cards, %d nodes; want 1 owned card and 1 node", len(cards), len(nodes))
	}

	// /api/nodes with the fleet on re-encodes the stored list through the
	// strip: a smuggled account binding never reaches the client (round 2
	// opened threads and the other read keys to siblings; account stays home)
	if err := st.SetNodePoll("n1", true, "", `[{"slug":"a","visibility":"public","account":{"slug":"x","email":"x@y"}},"bad"]`, 3); err != nil {
		t.Fatal(err)
	}
	onRec := httptest.NewRecorder()
	nodesHandler(onRec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	if body := onRec.Body.String(); strings.Contains(body, "account") || !strings.Contains(body, `"slug":"a"`) {
		t.Fatalf("GET /api/nodes fleet on = %s, want card a with no account key", body)
	}

	t.Setenv("KRAKEN_NODES", "off")
	if cards, nodes := fleetBoard(st); len(cards) != 0 || len(nodes) != 0 {
		t.Fatalf("fleet off: %d cards, %d nodes; want none", len(cards), len(nodes))
	}
	rec := httptest.NewRecorder()
	nodesHandler(rec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	var resp struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Nodes == nil || len(resp.Nodes) != 0 {
		t.Fatalf("GET /api/nodes with fleet off = %s, want {\"nodes\":[]}", rec.Body.String())
	}
}

// regressCSP is the sandbox an artifact reply for another node carries when
// the requested name is html or svg; any other name carries no CSP.
const regressCSP = "sandbox"

// regressInert checks nosniff and no-store, and that the CSP is exactly
// wantCSP ("" meaning absent).
func regressInert(t *testing.T, what string, rec *httptest.ResponseRecorder, wantCSP string) {
	t.Helper()
	h := rec.Header()
	if h.Get("Content-Security-Policy") != wantCSP || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("%s (%d): CSP %q nosniff %q cache %q; want %q, nosniff, no-store", what, rec.Code,
			h.Get("Content-Security-Policy"), h.Get("X-Content-Type-Options"), h.Get("Cache-Control"), wantCSP)
	}
}

func TestNodesRegressRelayBytesSandboxesEveryReply(t *testing.T) {
	orig := nodesProxyDo
	t.Cleanup(func() { nodesProxyDo = orig })
	node := &store.NodeRow{ID: "n1", Label: "studio"}
	req := httptest.NewRequest(http.MethodGet, "http://100.64.1.3:7777/api/node/artifact?slug=a&name=x.html", nil)
	cases := []struct {
		name   string
		status int
		body   string
		err    error
		want   int
	}{
		{"html 200", http.StatusOK, `<script>parent.document.title='x'</script>`, nil, http.StatusOK},
		{"sibling 404", http.StatusNotFound, `{"error":"no such artifact"}`, nil, http.StatusNotFound},
		{"sibling 401", http.StatusUnauthorized, `{"error":"unauthorized"}`, nil, http.StatusBadGateway},
		{"non-JSON refusal", http.StatusInternalServerError, `<b>oops</b>`, nil, http.StatusBadGateway},
		{"transport error", 0, "", io.ErrUnexpectedEOF, http.StatusBadGateway},
	}
	for _, c := range cases {
		nodesProxyDo = func(*http.Request, time.Duration) (*http.Response, error) {
			if c.err != nil {
				return nil, c.err
			}
			return &http.Response{StatusCode: c.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(c.body))}, nil
		}
		rec := httptest.NewRecorder()
		relayBytes(rec, node, req, time.Second, "text/html; charset=utf-8")
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
		regressInert(t, c.name, rec, regressCSP)
	}

	// the sandbox is for the two scriptable types only: an svg gets it, a
	// pdf or an image does not
	nodesProxyDo = func(*http.Request, time.Duration) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("bytes"))}, nil
	}
	for _, c := range []struct{ name, csp string }{{"x.svg", regressCSP}, {"x.pdf", ""}, {"x.png", ""}} {
		rec := httptest.NewRecorder()
		relayBytes(rec, node, req, time.Second, artifactMimeType(c.name))
		regressInert(t, c.name, rec, c.csp)
	}

	// the proxy's own refusals, before any relay, carry them too
	t.Setenv("HOME", t.TempDir())
	for _, c := range []struct{ name, csp string }{{"x.html", regressCSP}, {"x.pdf", ""}} {
		rec := httptest.NewRecorder()
		nodesArtifactHandler(rec, httptest.NewRequest(http.MethodGet, "/api/nodes/artifact?node=nope&slug=a&name="+c.name, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("unknown node: %d, want 404", rec.Code)
		}
		regressInert(t, "nodes/artifact unknown node "+c.name, rec, c.csp)
	}
}

// TestNodesRegressNodeArtifactRefusesLinksAndSandboxes drives a LIVE card's
// artifact read: a regular file is served, a symlink (to a file outside
// artifacts/, or as a directory segment) and a directory are 404, and every
// reply — the 404s included — carries nosniff and no-store, plus the sandbox
// CSP exactly when the requested name is html.
func TestNodesRegressNodeArtifactRefusesLinksAndSandboxes(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "")
	j := mustCreateStoreJob(t, st, store.Job{Slug: "linky", Repo: "kraken", State: "working"})

	root := jobs.CellArtifacts(j)
	if err := os.MkdirAll(filepath.Join(root, "real-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "key.md"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "00-overview.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "10-page.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "key.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	names := []string{"00-overview.md", "10-page.html", "link.md", "linkdir/key.md", "real-dir"}
	arts := make([]webArtifact, 0, len(names))
	for _, n := range names {
		arts = append(arts, webArtifact{Name: n, URL: artifactURL(j.Slug, "cell", n)})
	}
	saved := webBoardCache
	t.Cleanup(func() { webBoardCache = saved })
	webBoardCache = &boardCache{ttl: time.Hour, now: time.Now, build: func() webBoard {
		return webBoard{Jobs: []webJob{{Slug: j.Slug, Live: true, Visibility: jobs.VisibilityReadOnly, Artifacts: arts}}}
	}}

	get := func(name, csp string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		nodeArtifactHandler(rec, httptest.NewRequest(http.MethodGet, "/api/node/artifact?slug=linky&name="+name, nil))
		regressInert(t, name, rec, csp)
		return rec
	}
	if rec := get("00-overview.md", ""); rec.Code != http.StatusOK || rec.Body.String() != "# hi" {
		t.Fatalf("regular file = %d %q, want 200 # hi", rec.Code, rec.Body.String())
	}
	if rec := get("10-page.html", regressCSP); rec.Code != http.StatusOK || rec.Body.String() != "<p>hi</p>" {
		t.Fatalf("html file = %d %q, want 200 <p>hi</p>", rec.Code, rec.Body.String())
	}
	for _, n := range []string{"link.md", "linkdir/key.md", "real-dir", "unlisted.md"} {
		rec := get(n, "")
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("%s = %d %s, want 404 no such artifact", n, rec.Code, rec.Body.String())
		}
	}
	if rec := get("unlisted.html", regressCSP); rec.Code != http.StatusNotFound {
		t.Errorf("unlisted.html = %d, want 404", rec.Code)
	}
	// a refusal before the artifact lookup carries the headers too
	rec := httptest.NewRecorder()
	nodeArtifactHandler(rec, httptest.NewRequest(http.MethodGet, "/api/node/artifact?slug=nope&name=x.html", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown job = %d, want 404", rec.Code)
	}
	regressInert(t, "unknown job", rec, regressCSP)
}

// TestNodesRegressGateRefusesNonJSONPosts pins the gate's JSON rule for
// writes: an OWNER-tier text/plain POST to /api/kill (the CORS simple request
// a script in any page could fire at loopback) is refused 415 and the kill
// handler never runs; a JSON POST, a GET and the two multipart upload routes
// pass through.
func TestNodesRegressGateRefusesNonJSONPosts(t *testing.T) {
	orig := defaultGateDeps
	t.Cleanup(func() { defaultGateDeps = orig })
	defaultGateDeps = gateDeps{
		localOrigin:   func(string) bool { return true },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	}
	hits := map[string]int{}
	mux := http.NewServeMux()
	for _, p := range []string{"/api/kill", "/api/upload", "/api/background"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			hits[r.URL.Path]++
			webJSON(w, http.StatusOK, map[string]bool{"ok": true})
		})
	}
	h := Gate(mux)
	send := func(method, path, ct string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"slug":"x"}`))
		r.RemoteAddr = "127.0.0.1:4242"
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	for _, ct := range []string{"text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", ""} {
		if code := send(http.MethodPost, "/api/kill", ct); code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /api/kill as %q = %d, want 415", ct, code)
		}
	}
	if hits["/api/kill"] != 0 {
		t.Fatalf("kill handler ran %d times on non-JSON posts; want 0", hits["/api/kill"])
	}
	if code := send(http.MethodPost, "/api/kill", "application/json; charset=utf-8"); code != http.StatusOK || hits["/api/kill"] != 1 {
		t.Errorf("JSON POST /api/kill = %d, %d hits; want 200 and 1", code, hits["/api/kill"])
	}
	if code := send(http.MethodGet, "/api/kill", ""); code != http.StatusOK {
		t.Errorf("GET /api/kill = %d; the rule is for POSTs only", code)
	}
	for _, p := range []string{"/api/upload", "/api/background"} {
		if code := send(http.MethodPost, p, "multipart/form-data; boundary=x"); code != http.StatusOK || hits[p] != 1 {
			t.Errorf("multipart POST %s = %d, %d hits; want it to pass the gate", p, code, hits[p])
		}
	}
}

// TestNodesRegressNodeArtifactRefusesOverCap pins the owner-side size cap: a
// live-cell artifact one byte over nodeReplyMax is 413, never read whole,
// while one of exactly nodeReplyMax is served.
func TestNodesRegressNodeArtifactRefusesOverCap(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "")
	j := mustCreateStoreJob(t, st, store.Job{Slug: "biggy", Repo: "kraken", State: "working"})
	root := jobs.CellArtifacts(j)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int64{"big.bin": nodeReplyMax + 1, "edge.bin": nodeReplyMax} {
		f, err := os.Create(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	arts := []webArtifact{{Name: "big.bin"}, {Name: "edge.bin"}}
	saved := webBoardCache
	t.Cleanup(func() { webBoardCache = saved })
	webBoardCache = &boardCache{ttl: time.Hour, now: time.Now, build: func() webBoard {
		return webBoard{Jobs: []webJob{{Slug: j.Slug, Live: true, Visibility: jobs.VisibilityReadOnly, Artifacts: arts}}}
	}}
	rec := httptest.NewRecorder()
	nodeArtifactHandler(rec, httptest.NewRequest(http.MethodGet, "/api/node/artifact?slug=biggy&name=big.bin", nil))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "artifact over 16 MB") {
		t.Fatalf("over-cap artifact = %d %.80s; want 413 artifact over 16 MB", rec.Code, rec.Body.String())
	}
	regressInert(t, "over-cap", rec, "")
	rec = httptest.NewRecorder()
	nodeArtifactHandler(rec, httptest.NewRequest(http.MethodGet, "/api/node/artifact?slug=biggy&name=edge.bin", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != nodeReplyMax {
		t.Fatalf("at-cap artifact = %d, %d bytes; want 200 and %d", rec.Code, rec.Body.Len(), nodeReplyMax)
	}
}

// TestNodesRegressGateRefusesForeignOriginPosts pins the gate's Origin rule:
// a multipart POST to /api/upload — exempt from the JSON rule — from an
// opaque origin (Origin: null) or another site is refused 403 with nosniff
// and the handler never runs; same-origin and Origin-less posts pass, and a
// GET carrying a foreign Origin is not the rule's business.
func TestNodesRegressGateRefusesForeignOriginPosts(t *testing.T) {
	orig := defaultGateDeps
	t.Cleanup(func() { defaultGateDeps = orig })
	defaultGateDeps = gateDeps{
		localOrigin:   func(string) bool { return true },
		lookupToken:   func(string) (*store.PairDevice, bool) { return nil, false },
		tailnetDevice: func(string) (core.TailnetIdentity, bool) { return core.TailnetIdentity{}, false },
	}
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		hits++
		webJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	h := Gate(mux)
	send := func(method, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:7878/api/upload", strings.NewReader("--x--"))
		r.RemoteAddr = "127.0.0.1:4242"
		r.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for _, o := range []string{"null", "http://evil.example", "http://127.0.0.1:7777", "https://127.0.0.1:7878"} {
		rec := send(http.MethodPost, o)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-origin post refused") ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("Origin %q: %d %s nosniff %q; want 403 cross-origin post refused, nosniff", o, rec.Code,
				rec.Body.String(), rec.Header().Get("X-Content-Type-Options"))
		}
	}
	if hits != 0 {
		t.Fatalf("upload handler ran %d times on foreign-origin posts; want 0", hits)
	}
	if rec := send(http.MethodPost, "http://127.0.0.1:7878"); rec.Code != http.StatusOK {
		t.Errorf("same-origin post = %d, want 200", rec.Code)
	}
	if rec := send(http.MethodPost, ""); rec.Code != http.StatusOK {
		t.Errorf("Origin-less post = %d, want 200", rec.Code)
	}
	if rec := send(http.MethodGet, "null"); rec.Code != http.StatusOK {
		t.Errorf("GET with Origin null = %d, want 200 (the rule is for POSTs)", rec.Code)
	}
	if hits != 3 {
		t.Fatalf("handler hits = %d, want 3 (same-origin, Origin-less, GET)", hits)
	}

	// the gate's other refusals carry nosniff too
	jr := httptest.NewRequest(http.MethodPost, "/api/kill", strings.NewReader(`{}`))
	jr.RemoteAddr = "127.0.0.1:4242"
	jr.Header.Set("Content-Type", "text/plain")
	jrec := httptest.NewRecorder()
	h.ServeHTTP(jrec, jr)
	if jrec.Code != http.StatusUnsupportedMediaType || jrec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("text/plain POST: %d nosniff %q; want 415 nosniff", jrec.Code, jrec.Header().Get("X-Content-Type-Options"))
	}
	defaultGateDeps.localOrigin = func(string) bool { return false }
	r := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("anon caller: %d nosniff %q; want 401 nosniff", rec.Code, rec.Header().Get("X-Content-Type-Options"))
	}
}

// TestNodesRegressStrippedCardsDropsDuplicateArtifactNames: a sibling listing
// one name twice reaches the board once, with the first entry's proxy URL.
func TestNodesRegressStrippedCardsDropsDuplicateArtifactNames(t *testing.T) {
	cards, ok := strippedCards("n1", `[{"slug":"a","artifacts":[`+
		`{"name":"00-overview.md","url":"/api/artifact/a/cell/00-overview.md"},`+
		`{"name":"00-overview.md","url":"/evil"},`+
		`{"name":"10-arch.html","url":"/api/artifact/a/cell/10-arch.html"}]}]`)
	if !ok || len(cards) != 1 {
		t.Fatalf("strippedCards = %d cards, ok %v; want 1", len(cards), ok)
	}
	got := cards[0].Artifacts
	if len(got) != 2 || got[0].Name != "00-overview.md" || got[1].Name != "10-arch.html" {
		t.Fatalf("artifacts = %+v; want 00-overview.md then 10-arch.html, the repeat dropped", got)
	}
	if got[0].URL != remoteArtifactURL("n1", "a", "00-overview.md") {
		t.Fatalf("first artifact url = %q; want the proxy URL", got[0].URL)
	}
}
