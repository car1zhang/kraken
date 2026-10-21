package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// fleetparity_edge_test.go — the EDGE dimension of 50-contract.md's
// "Round 2 — remote parity", slice R2-G: every refusal and its exact body,
// the strip rules, header presence on 502s, the escaping of awkward names,
// and the boundaries of relayBytes. Written blind from the contract: nodes.go,
// board.go, gate.go, register.go, jobedit.go, transcript.go and threads.go
// were not read. Happy paths and 200 bodies are athena-logic's.
//
// Every package-level identifier is prefixed `edgeFP`. The round-1 helpers
// (`edgeFleetEnv`, `edgeFleetJob`, `edgeFleetReq`, `edgeFleetDo`,
// `edgeFleetErr`, `edgeFleetSeedRemote`, `edgeFleetJobRow`) are reused from
// fleet_edge_test.go.

// edgeFPGet is a GET from a tailnet sibling.
func edgeFPGet(target string) *http.Request {
	return edgeFleetReq(http.MethodGet, target, nil, "100.88.2.1:1")
}

// edgeFPNoSuchJob asserts the one `noSuchJob` reply and returns its body.
func edgeFPNoSuchJob(t *testing.T, label string, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s: status %d (%s), want 404", label, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	if got := edgeFleetErr(t, rec); got != "no such job" {
		t.Errorf("%s: error %q, want no such job", label, got)
	}
	return strings.TrimSpace(rec.Body.String())
}

// edgeFPSeedArtifact lands one artifact row for a non-live job through the
// store's own sync write path.
func edgeFPSeedArtifact(t *testing.T, st *store.Store, slug, name, body string) {
	t.Helper()
	row := edgeFleetJobRow(t, st, slug)
	if err := st.SyncArtifacts(row.ID, []store.ArtifactFile{{Path: name, Bytes: []byte(body), Mtime: 1}}, nil); err != nil {
		t.Fatalf("SyncArtifacts %s/%s: %v", slug, name, err)
	}
}

// edgeFPStubProxy swaps nodesProxyDo for the test and records the last
// outbound request and timeout.
type edgeFPProxyCall struct {
	req     *http.Request
	timeout time.Duration
	calls   int
}

func edgeFPStubProxy(t *testing.T, status int, ct, body string, err error) *edgeFPProxyCall {
	t.Helper()
	orig := nodesProxyDo
	t.Cleanup(func() { nodesProxyDo = orig })
	call := &edgeFPProxyCall{}
	nodesProxyDo = func(r *http.Request, d time.Duration) (*http.Response, error) {
		call.req, call.timeout = r, d
		call.calls++
		if err != nil {
			return nil, err
		}
		h := http.Header{}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return call
}

func edgeFPAssertSafeHeaders(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options %q, want nosniff", label, got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s: Cache-Control %q, want no-store", label, got)
	}
}

// Only ID and Label, the shape round 1's relay regression pin uses, so the
// `<label>` in every 502 is "studio".
var edgeFPNode = &store.NodeRow{ID: "nFP", Label: "studio"}

// ---------------------------------------------------------------------------
// nodeCard / remoteArtifactURL / strippedCards
// ---------------------------------------------------------------------------

// edgeFPFullCard is a card carrying every key round 2 names, plus the two it
// strips.
const edgeFPFullCard = `{
 "slug":"wide-job","repo":"kraken","state":"blocked","live":true,"mtime":5,
 "color":"blue","visibility":"readonly","detail":"zeus said","last_sent":"carl said",
 "artifacts":[{"name":"00-overview.md","url":"/api/artifact/wide-job/cell/00-overview.md"}],
 "feedback":[{"id":1,"artifact":"00-overview.md","note":"tighten","at":3}],
 "tokens":{"in":1,"out":2,"cache_write":3,"cache_read":4,"sessions":1,"updated_at":9},
 "threads":[{"tid":"t1","state":"live","created_at":1}],
 "doc_questions":[{"id":"Q1","text":"which?","importance":"P0"}],
 "prs":[{"url":"https://github.com/o/r/pull/1","state":"open"}],
 "load":{"unblock":2,"scope":6,"note":"n","at":7},
 "goal":{"id":1,"text":"ship","done":false},
 "goals":[{"id":1,"text":"ship","done":false}],
 "question":{"tool_use_id":"tu1","questions":[{"question":"go?","header":"h","multi_select":false}]},
 "roster":[{"name":"athena","model":"opus","role":"","status":"idle","working":false}],
 "account":{"slug":"pool-a","email":"a@b.c","at":1},
 "owner":{"node_id":"nX","label":"x","operator":"y","online":true}
}`

func edgeFPKeys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// Contract R2-G nodeCard: "Clears exactly `Owner` and `Account`. Every other
// key the board built — `feedback`, `tokens`, `threads`, `doc_questions`,
// `artifacts`, `prs`, `load`, `goals`/`goal`, `question`, `roster`, `detail`,
// `last_sent`, `color`, `visibility` … — is left as is. The artifact URLs it
// leaves are the OWNER's".
func TestEdgeFleetParityNodeCardClearsExactlyOwnerAndAccount(t *testing.T) {
	var wj webJob
	if err := json.Unmarshal([]byte(edgeFPFullCard), &wj); err != nil {
		t.Fatalf("decode full card: %v", err)
	}
	before := edgeFPKeys(t, wj)
	for _, k := range []string{"feedback", "tokens", "threads", "doc_questions", "artifacts", "prs", "load", "goals", "goal", "question", "roster", "detail", "last_sent", "color", "visibility", "account", "owner"} {
		if _, ok := before[k]; !ok {
			t.Fatalf("fixture: webJob does not round-trip key %q", k)
		}
	}
	after := edgeFPKeys(t, nodeCard(wj))
	for _, k := range []string{"owner", "account"} {
		if _, ok := after[k]; ok {
			t.Errorf("nodeCard left %q: %s", k, after[k])
		}
	}
	var lost []string
	for k, v := range before {
		if k == "owner" || k == "account" {
			continue
		}
		got, ok := after[k]
		if !ok {
			lost = append(lost, k)
			continue
		}
		if string(got) != string(v) {
			t.Errorf("nodeCard changed %q: %s → %s", k, v, got)
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Errorf("nodeCard cleared keys it must keep: %v", lost)
	}
	if len(after) != len(before)-2 {
		t.Errorf("nodeCard key count %d, want %d (before minus owner, account)", len(after), len(before)-2)
	}
}

// Contract R2-G remoteArtifactURL: "Returns `"/api/nodes/artifact?node=" +
// url.QueryEscape(nodeID) + "&slug=" + url.QueryEscape(slug) + "&name=" +
// url.QueryEscape(name)`. `name` is the LAST parameter" — with `/`, spaces,
// `#`, `&` and `=` in every part, and `.html` left intact at the tail.
func TestEdgeFleetParityRemoteArtifactURLEscapes(t *testing.T) {
	cases := []struct{ node, slug, name string }{
		{"nA", "plain", "00-overview.md"},
		{"n/B c", "sl ug#1", "nested/dir name/10 arch#v2.html"},
		{"n&x=1", "a&slug=evil", "x&name=y.md"},
		{"", "", ""},
	}
	for _, c := range cases {
		want := "/api/nodes/artifact?node=" + url.QueryEscape(c.node) + "&slug=" + url.QueryEscape(c.slug) + "&name=" + url.QueryEscape(c.name)
		got := remoteArtifactURL(c.node, c.slug, c.name)
		if got != want {
			t.Errorf("remoteArtifactURL(%q,%q,%q) = %q, want %q", c.node, c.slug, c.name, got, want)
		}
		if strings.Contains(got, "#") {
			t.Errorf("remoteArtifactURL(%q,%q,%q) = %q carries a raw #", c.node, c.slug, c.name, got)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Errorf("%q does not parse: %v", got, err)
			continue
		}
		q := u.Query()
		if q.Get("node") != c.node || q.Get("slug") != c.slug || q.Get("name") != c.name {
			t.Errorf("%q round-trips to node=%q slug=%q name=%q", got, q.Get("node"), q.Get("slug"), q.Get("name"))
		}
	}
	if got := remoteArtifactURL("n", "s", "10-arch.html"); !strings.HasSuffix(got, "&name=10-arch.html") {
		t.Errorf("html name not at the tail intact: %q", got)
	}
}

// Contract R2-G strippedCards: "each card passes `nodeCard`, then every
// artifact's `URL` is REPLACED by `remoteArtifactURL(nodeID, card.Slug,
// a.Name)`, and an artifact element whose `Name` is empty after
// `strings.TrimSpace` is dropped from the list … A stored card's own `url`
// values are never read." — and nodeCard's "never serves a sibling's URL
// onward".
func TestEdgeFleetParityStrippedCardsRewritesAndDrops(t *testing.T) {
	jobsJSON := `[{"slug":"rw job","repo":"kraken","state":"working","live":true,"mtime":1,"visibility":"readonly",
	  "account":{"slug":"pool-a","email":"a@b.c","at":1},
	  "owner":{"node_id":"smuggled","label":"s","operator":"o","online":true},
	  "artifacts":[
	    {"name":"","url":"http://evil.example/empty"},
	    {"name":"   ","url":"http://evil.example/blank"},
	    {"name":"a/b c#d.html","url":"http://evil.example/steal"},
	    {"name":"kept.md","url":"javascript:alert(1)"},
	    {"name":"no-url.md"}
	  ]}]`
	cards, _ := strippedCards("n/Ö 1", jobsJSON)
	if len(cards) != 1 {
		t.Fatalf("strippedCards = %d cards, want 1", len(cards))
	}
	c := cards[0]
	if c.Account != nil {
		t.Errorf("account survived the strip: %+v", c.Account)
	}
	if c.Owner != nil {
		t.Errorf("a stored owner survived the strip: %+v", c.Owner)
	}
	var names []string
	for _, a := range c.Artifacts {
		names = append(names, a.Name)
		if want := remoteArtifactURL("n/Ö 1", "rw job", a.Name); a.URL != want {
			t.Errorf("artifact %q url %q, want %q", a.Name, a.URL, want)
		}
	}
	if got := strings.Join(names, "|"); got != "a/b c#d.html|kept.md|no-url.md" {
		t.Errorf("artifact names %q, want the three non-blank names in wire order", got)
	}
	raw, _ := json.Marshal(cards)
	for _, bad := range []string{"evil.example", "javascript:", "smuggled"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("stripped cards still carry %q: %s", bad, raw)
		}
	}
}

// Contract R2-G strippedCards: "`remoteCards` passes `n.ID`" — a card served
// through the board carries URLs naming the ROW's id, never a stored URL.
func TestEdgeFleetParityRemoteCardsRewriteWithRowID(t *testing.T) {
	rows := []*store.NodeRow{{ID: "n row#1", Label: "studio", Operator: "Jess", Online: true,
		JobsJSON: `[{"slug":"j1","repo":"kraken","state":"working","live":true,"mtime":1,"visibility":"public",
		  "artifacts":[{"name":"x.md","url":"/api/artifact/j1/cell/x.md"},{"name":"","url":"/api/artifact/j1/cell/"}]}]`}}
	cards := remoteCards(rows)
	if len(cards) != 1 {
		t.Fatalf("remoteCards = %d cards, want 1", len(cards))
	}
	if len(cards[0].Artifacts) != 1 {
		t.Fatalf("artifacts = %+v, want only x.md", cards[0].Artifacts)
	}
	if got, want := cards[0].Artifacts[0].URL, remoteArtifactURL("n row#1", "j1", "x.md"); got != want {
		t.Errorf("board artifact url %q, want %q (the owner's /api/artifact url is never served onward)", got, want)
	}
}

// ---------------------------------------------------------------------------
// visibleJob and the three node-tier reads
// ---------------------------------------------------------------------------

// Contract R2-G visibleJob: "`!jobs.FleetEnabled()`, empty slug, unknown
// slug, or `EffectiveVisibility() == jobs.VisibilityPrivate` → writes `404
// {"error":"no such job"}` (the one `noSuchJob` body, identical for all
// four) and returns nil. readonly and public return the job with nothing
// written."
func TestEdgeFleetParityVisibleJobRefusals(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	edgeFleetJob(t, st, "pub-job", "public")
	edgeFleetJob(t, st, "default-job", "")
	edgeFleetJob(t, st, "secret-job", "private")

	var bodies []string
	for _, slug := range []string{"", "never-existed", "secret-job"} {
		rec := httptest.NewRecorder()
		if j := visibleJob(rec, slug); j != nil {
			t.Errorf("visibleJob(%q) = %s, want nil", slug, j.Slug)
		}
		bodies = append(bodies, edgeFPNoSuchJob(t, "visibleJob("+slug+")", rec))
	}
	for _, slug := range []string{"ro-job", "pub-job", "default-job"} {
		rec := httptest.NewRecorder()
		j := visibleJob(rec, slug)
		if j == nil || j.Slug != slug {
			t.Errorf("visibleJob(%q) = %v, want the job", slug, j)
		}
		if rec.Body.Len() != 0 || rec.Code != http.StatusOK || len(rec.Header()) != 0 {
			t.Errorf("visibleJob(%q) wrote something: %d %v %q", slug, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	t.Setenv("KRAKEN_NODES", "off")
	rec := httptest.NewRecorder()
	if j := visibleJob(rec, "pub-job"); j != nil {
		t.Errorf("visibleJob with fleet off = %s, want nil", j.Slug)
	}
	bodies = append(bodies, edgeFPNoSuchJob(t, "visibleJob fleet off", rec))
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("noSuchJob bodies differ: %q", bodies)
			break
		}
	}
}

// Contract R2-G visibleJob: "Resolves `slug` in `jobs.LoadJobs()` (active
// jobs only)" — an archived public job is the same 404 on every new route.
func TestEdgeFleetParityArchivedJobIsNoSuchJob(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "old-job", "public")
	if _, err := st.ArchiveJob(edgeFleetJobRow(t, st, "old-job").ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	for name, h := range map[string]http.HandlerFunc{
		"/api/node/artifact?slug=old-job&name=x.md": nodeArtifactHandler,
		"/api/node/transcript?slug=old-job":         nodeTranscriptHandler,
		"/api/node/thread/read?slug=old-job&tid=t1": nodeThreadReadHandler,
		"/api/node/job/raw?slug=old-job":            nodeJobRawHandler,
		"/api/node/read?slug=old-job":               nodeReadHandler,
	} {
		edgeFPNoSuchJob(t, name, edgeFleetDo(h, edgeFPGet(name)))
	}
}

// Contract R2-G nodeArtifactHandler / nodeTranscriptHandler /
// nodeThreadReadHandler / nodeJobRawHandler / nodeReadHandler: each gated by
// `visibleJob` — fleet off, empty slug, unknown slug and private all answer
// the one identical `404 no such job`, before any route-specific refusal
// (a missing `name` or `tid` never surfaces for a hidden job).
func TestEdgeFleetParityNodeReadsHideTheSameFour(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "secret-job", "private")
	edgeFleetJob(t, st, "pub-job", "public")
	edgeFPSeedArtifact(t, st, "secret-job", "x.md", "secret bytes")
	routes := map[string]http.HandlerFunc{
		"/api/node/artifact":    nodeArtifactHandler,
		"/api/node/transcript":  nodeTranscriptHandler,
		"/api/node/thread/read": nodeThreadReadHandler,
		"/api/node/job/raw":     nodeJobRawHandler,
		"/api/node/read":        nodeReadHandler,
	}
	for path, h := range routes {
		var bodies []string
		for _, q := range []string{"", "?slug=", "?slug=never-existed", "?slug=secret-job&name=x.md&tid=t1"} {
			rec := edgeFleetDo(h, edgeFPGet(path+q))
			bodies = append(bodies, edgeFPNoSuchJob(t, path+q, rec))
			if strings.Contains(rec.Body.String(), "secret bytes") {
				t.Errorf("%s%s leaked a private artifact", path, q)
			}
		}
		t.Setenv("KRAKEN_NODES", "off")
		bodies = append(bodies, edgeFPNoSuchJob(t, path+" fleet off", edgeFleetDo(h, edgeFPGet(path+"?slug=pub-job&name=x.md&tid=t1"))))
		t.Setenv("KRAKEN_NODES", "")
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Errorf("%s: 404 bodies differ: %q", path, bodies)
				break
			}
		}
	}
}

// Contract R2-G nodeArtifactHandler: "Non-GET → `405 {"error":"GET
// only"}`" — checked first, so even an unknown slug is a 405. The transcript
// and thread-read rows say "GET only (405)"; the same body is the literal
// reading (listed as a silence).
func TestEdgeFleetParityNodeReadsGETOnly(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "pub-job", "public")
	routes := map[string]http.HandlerFunc{
		"/api/node/artifact":    nodeArtifactHandler,
		"/api/node/transcript":  nodeTranscriptHandler,
		"/api/node/thread/read": nodeThreadReadHandler,
	}
	for path, h := range routes {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			for _, slug := range []string{"pub-job", "never-existed"} {
				rec := edgeFleetDo(h, edgeFleetReq(m, path+"?slug="+slug+"&name=x.md&tid=t1", nil, "100.88.2.2:1"))
				if rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s %s: status %d, want 405", m, path, slug, rec.Code)
					continue
				}
				if got := edgeFleetErr(t, rec); got != "GET only" {
					t.Errorf("%s %s %s: error %q, want GET only", m, path, slug, got)
				}
			}
		}
	}
}

// Contract R2-G nodeArtifactHandler: "`strings.TrimSpace(name) == ""` → `404
// {"error":"no such artifact"}` … `name` must EXACTLY equal one entry's
// `Name`, else `404 {"error":"no such artifact"}`."
func TestEdgeFleetParityNodeArtifactNameRefusals(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	edgeFPSeedArtifact(t, st, "ro-job", "kept.md", "row bytes")
	for _, name := range []string{
		"", "   ", "%09", // empty, blank, a lone tab
		"Kept.md", " kept.md", "kept.md ", "/kept.md", "./kept.md",
		"kept", "kept.md/", "../kept.md", "missing.md", "kept.md%00",
	} {
		rec := edgeFleetDo(nodeArtifactHandler, edgeFPGet("/api/node/artifact?slug=ro-job&name="+strings.ReplaceAll(name, " ", "%20")))
		if rec.Code != http.StatusNotFound {
			t.Errorf("name %q: status %d (%q), want 404", name, rec.Code, rec.Body.String())
			continue
		}
		if got := edgeFleetErr(t, rec); got != "no such artifact" {
			t.Errorf("name %q: error %q, want no such artifact", name, got)
		}
		if strings.Contains(rec.Body.String(), "row bytes") {
			t.Errorf("name %q leaked the artifact bytes", name)
		}
	}
	// the name parameter missing entirely is the empty name
	rec := edgeFleetDo(nodeArtifactHandler, edgeFPGet("/api/node/artifact?slug=ro-job"))
	if rec.Code != http.StatusNotFound || edgeFleetErr(t, rec) != "no such artifact" {
		t.Errorf("no name param: %d %q, want 404 no such artifact", rec.Code, rec.Body.String())
	}
}

// Contract R2-G nodeArtifactHandler: names are matched against THIS job's
// own list — another job's artifact, even with the same visibility, is
// `404 no such artifact`, and a private job's artifact is `no such job`.
func TestEdgeFleetParityNodeArtifactIsJobScoped(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-a", "readonly")
	edgeFleetJob(t, st, "ro-b", "readonly")
	edgeFPSeedArtifact(t, st, "ro-b", "only-b.md", "b bytes")
	rec := edgeFleetDo(nodeArtifactHandler, edgeFPGet("/api/node/artifact?slug=ro-a&name=only-b.md"))
	if rec.Code != http.StatusNotFound || edgeFleetErr(t, rec) != "no such artifact" {
		t.Errorf("cross-job name: %d %q, want 404 no such artifact", rec.Code, rec.Body.String())
	}
}

// Contract R2-G nodeArtifactHandler: "A 200 carries `Content-Type:
// artifactMimeType(name)`, `X-Content-Type-Options: nosniff`,
// `Cache-Control: no-store` and the bytes verbatim" — for a name with a
// space, a nested dir and a `#`, which the query must carry escaped, and
// for a readonly job with no visibility line (the default).
func TestEdgeFleetParityNodeArtifactAwkwardName(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "default-job", "")
	name := "nested/dir name/10 arch#v2.html"
	body := "<script>alert(1)</script>\x00\xff"
	edgeFPSeedArtifact(t, st, "default-job", name, body)
	q := url.Values{"slug": {"default-job"}, "name": {name}}
	rec := edgeFleetDo(nodeArtifactHandler, edgeFPGet("/api/node/artifact?"+q.Encode()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%q), want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("bytes %q, want verbatim %q", got, body)
	}
	if got, want := rec.Header().Get("Content-Type"), artifactMimeType(name); got != want {
		t.Errorf("Content-Type %q, want artifactMimeType(name) %q", got, want)
	}
	edgeFPAssertSafeHeaders(t, "artifact 200", rec)
}

// Contract R2-G nodeArtifactHandler: "Routed `TierNode`, not mirror-gated"
// (and the two other node reads likewise) — a mirror serve still answers a
// readonly job's artifact through the real mux.
func TestEdgeFleetParityNodeReadsNotMirrorGated(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	edgeFPSeedArtifact(t, st, "ro-job", "kept.md", "row bytes")
	SetMirrorMode(true)
	rec := edgeFleetServe(edgeFleetMux(t), edgeFPGet("/api/node/artifact?slug=ro-job&name=kept.md"))
	if rec.Code != http.StatusOK || rec.Body.String() != "row bytes" {
		t.Errorf("mirror: /api/node/artifact = %d %q, want 200 row bytes", rec.Code, rec.Body.String())
	}
	rec = edgeFleetServe(edgeFleetMux(t), edgeFPGet("/api/node/thread/read?slug=ro-job"))
	if rec.Code != http.StatusBadRequest || edgeFleetErr(t, rec) != "missing tid" {
		t.Errorf("mirror: /api/node/thread/read without tid = %d %q, want the local 400 missing tid", rec.Code, rec.Body.String())
	}
}

// Contract R2-G nodeThreadReadHandler: "The local handler's `400
// {"error":"missing tid"}` and `404 {"error":"unknown thread"}` pass through
// unchanged (a tid of another job's thread is `unknown thread`, since
// `GetThread(j.ID, tid)` is keyed on the job)."
func TestEdgeFleetParityNodeThreadReadPassThrough(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-a", "readonly")
	edgeFleetJob(t, st, "ro-b", "readonly")
	b := edgeFleetJobRow(t, st, "ro-b")
	if err := st.AddThread(store.Thread{JobID: b.ID, TID: "t7", SessionID: "sb1", State: "live", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	cases := []struct {
		q, want string
		code    int
	}{
		{"?slug=ro-a", "missing tid", http.StatusBadRequest},
		{"?slug=ro-a&tid=", "missing tid", http.StatusBadRequest},
		{"?slug=ro-a&tid=nope", "unknown thread", http.StatusNotFound},
		{"?slug=ro-a&tid=t7", "unknown thread", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := edgeFleetDo(nodeThreadReadHandler, edgeFPGet("/api/node/thread/read"+c.q))
		if rec.Code != c.code {
			t.Errorf("%s: status %d (%q), want %d", c.q, rec.Code, rec.Body.String(), c.code)
			continue
		}
		if got := edgeFleetErr(t, rec); got != c.want {
			t.Errorf("%s: error %q, want %q", c.q, got, c.want)
		}
		local := httptest.NewRecorder()
		threadReadHandler(local, httptest.NewRequest(http.MethodGet, "/api/thread/read"+c.q, nil))
		if local.Code != rec.Code || local.Body.String() != rec.Body.String() {
			t.Errorf("%s: node reply %d %q differs from local %d %q", c.q, rec.Code, rec.Body.String(), local.Code, local.Body.String())
		}
	}
}

// Contract R2-G nodeTranscriptHandler: "The body is exactly `GET
// /api/job/transcript`'s …, the local handler's own 500 included" — whatever
// the local handler answers for a readonly job with no cell, the node route
// answers byte for byte.
func TestEdgeFleetParityNodeTranscriptMirrorsLocal(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	for _, q := range []string{"?slug=ro-job", "?slug=ro-job&n=3", "?slug=ro-job&turns=2"} {
		rec := edgeFleetDo(nodeTranscriptHandler, edgeFPGet("/api/node/transcript"+q))
		local := httptest.NewRecorder()
		jobTranscriptHandler(local, httptest.NewRequest(http.MethodGet, "/api/job/transcript"+q, nil))
		if rec.Code != local.Code || rec.Body.String() != local.Body.String() {
			t.Errorf("%s: node %d %q, local %d %q", q, rec.Code, rec.Body.String(), local.Code, local.Body.String())
		}
	}
}

// Contract R2-G publicJob: "keeps round 1's behaviour (readonly → `403
// {"error":"not public"}`) and is called by `nodeActionHandler` ONLY" — the
// operator's steer singled out stop and colour; neither drives a readonly
// job.
func TestEdgeFleetParityNodeActionReadonlyStill403(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetJob(t, st, "ro-job", "readonly")
	edgeFleetJob(t, st, "default-job", "")
	for _, slug := range []string{"ro-job", "default-job"} {
		for _, a := range []struct {
			action  string
			payload map[string]any
		}{
			{"stop", nil},
			{"color", map[string]any{"color": "blue"}},
			{"save", map[string]any{"content": "---\nrepo: kraken\n---\n# x\n", "mtime": 1}},
		} {
			rec := edgeFleetDo(nodeActionHandler, edgeFleetAction(slug, a.action, a.payload))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d (%q), want 403", slug, a.action, rec.Code, rec.Body.String())
				continue
			}
			if got := edgeFleetErr(t, rec); got != "not public" {
				t.Errorf("%s %s: error %q, want not public", slug, a.action, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// relayBytes
// ---------------------------------------------------------------------------

func edgeFPRelay(t *testing.T, mime string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://100.64.9.9:7777/api/node/artifact?slug=s&name=x.md", nil)
	relayBytes(rec, edgeFPNode, req, time.Second, mime)
	return rec
}

// Contract R2-G relayBytes: "Transport error → `502 {"error": <err>}`" with
// nosniff and no-store "on EVERY reply, 502s included".
func TestEdgeFleetParityRelayBytesTransportError(t *testing.T) {
	boom := errors.New("dial tcp 100.64.9.9:7777: connect: connection refused")
	edgeFPStubProxy(t, 0, "", "", boom)
	rec := edgeFPRelay(t, "text/markdown; charset=utf-8")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != boom.Error() {
		t.Errorf("error %q, want %q", got, boom.Error())
	}
	edgeFPAssertSafeHeaders(t, "transport 502", rec)
	if got := rec.Header().Get("Content-Type"); got == "text/markdown; charset=utf-8" {
		t.Errorf("a 502 carried the artifact mime type %q", got)
	}
}

// Contract R2-G relayBytes: "Reads at most `nodeReplyMax` (16 MB) + 1 bytes;
// a longer body → `502 {"error":"<label> answered more than 16 MB"}`" — the
// boundary: exactly nodeReplyMax is served, one byte more is refused.
func TestEdgeFleetParityRelayBytesSizeBoundary(t *testing.T) {
	max := int(nodeReplyMax)
	exact := strings.Repeat("a", max)
	edgeFPStubProxy(t, http.StatusOK, "text/plain", exact, nil)
	rec := edgeFPRelay(t, "text/plain; charset=utf-8")
	if rec.Code != http.StatusOK || rec.Body.Len() != max {
		t.Errorf("exactly nodeReplyMax: status %d, %d bytes; want 200 with %d", rec.Code, rec.Body.Len(), max)
	}

	edgeFPStubProxy(t, http.StatusOK, "text/plain", exact+"b", nil)
	rec = edgeFPRelay(t, "text/plain; charset=utf-8")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("nodeReplyMax+1: status %d, want 502", rec.Code)
	}
	if got := edgeFleetErr(t, rec); got != "studio answered more than 16 MB" {
		t.Errorf("error %q, want studio answered more than 16 MB", got)
	}
	if rec.Body.Len() > 1024 {
		t.Errorf("the 502 carried %d bytes of the oversized body", rec.Body.Len())
	}
	edgeFPAssertSafeHeaders(t, "oversize 502", rec)
}

// The size refusal also holds for an oversized ERROR body: the cap is read
// before any status rule.
func TestEdgeFleetParityRelayBytesOversizedErrorBody(t *testing.T) {
	edgeFPStubProxy(t, http.StatusNotFound, "application/json", `{"error":"`+strings.Repeat("x", int(nodeReplyMax))+`"}`, nil)
	rec := edgeFPRelay(t, "text/plain")
	if rec.Code != http.StatusBadGateway || edgeFleetErr(t, rec) != "studio answered more than 16 MB" {
		t.Errorf("oversized 404: %d (%d bytes), want 502 answered more than 16 MB", rec.Code, rec.Body.Len())
	}
}

// Contract R2-G relayBytes: "Sibling 401 → `502 {"error":"<label> did not
// recognise this node"}`" — whatever the 401's body, JSON or not.
func TestEdgeFleetParityRelayBytesSibling401(t *testing.T) {
	for _, body := range []string{`{"error":"unauthorized"}`, `<html>login</html>`, ``} {
		edgeFPStubProxy(t, http.StatusUnauthorized, "text/html", body, nil)
		rec := edgeFPRelay(t, "text/html; charset=utf-8")
		if rec.Code != http.StatusBadGateway {
			t.Errorf("401 body %q: status %d, want 502", body, rec.Code)
			continue
		}
		if got := edgeFleetErr(t, rec); got != "studio did not recognise this node" {
			t.Errorf("401 body %q: error %q", body, got)
		}
		if got := rec.Header().Get("Content-Type"); got == "text/html; charset=utf-8" || got == "text/html" {
			t.Errorf("401 relay carried an html Content-Type %q", got)
		}
		edgeFPAssertSafeHeaders(t, "401 502", rec)
	}
}

// Contract R2-G relayBytes: "Sibling 200 → status 200, `Content-Type:
// mimeType` (the CALLER's value …; the sibling's `Content-Type` header is
// never read) and the body verbatim" — a 200 body is never JSON-checked,
// and an absent sibling Content-Type changes nothing.
func TestEdgeFleetParityRelayBytes200UsesCallersMime(t *testing.T) {
	for _, siblingCT := range []string{"text/html", "application/json", ""} {
		body := "# not json <b>\x00"
		edgeFPStubProxy(t, http.StatusOK, siblingCT, body, nil)
		rec := edgeFPRelay(t, "image/svg+xml")
		if rec.Code != http.StatusOK || rec.Body.String() != body {
			t.Errorf("sibling CT %q: %d %q, want 200 verbatim", siblingCT, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
			t.Errorf("sibling CT %q: Content-Type %q, want the caller's image/svg+xml", siblingCT, got)
		}
		edgeFPAssertSafeHeaders(t, "200", rec)
	}
}

// Contract R2-G relayBytes: "Any other sibling status → the same rules
// `relay` applies: a non-JSON body → `502 {"error":"<label> answered
// non-JSON"}`, else the sibling's status and body as `application/json`."
func TestEdgeFleetParityRelayBytesOtherStatuses(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		wantCode int
		wantErr  string
	}{
		{http.StatusNotFound, `{"error":"no such artifact"}`, http.StatusNotFound, "no such artifact"},
		{http.StatusNotFound, `{"error":"no such job"}`, http.StatusNotFound, "no such job"},
		{http.StatusMethodNotAllowed, `{"error":"GET only"}`, http.StatusMethodNotAllowed, "GET only"},
		{http.StatusInternalServerError, `{"error":"x"}`, http.StatusInternalServerError, "x"},
		{http.StatusNotFound, `404 page not found`, http.StatusBadGateway, "studio answered non-JSON"},
		{http.StatusInternalServerError, ``, http.StatusBadGateway, "studio answered non-JSON"},
		{http.StatusForbidden, `<html>no</html>`, http.StatusBadGateway, "studio answered non-JSON"},
	}
	for _, c := range cases {
		edgeFPStubProxy(t, c.status, "text/html", c.body, nil)
		rec := edgeFPRelay(t, "text/markdown; charset=utf-8")
		if rec.Code != c.wantCode {
			t.Errorf("sibling %d %q: status %d, want %d", c.status, c.body, rec.Code, c.wantCode)
			continue
		}
		if got := edgeFleetErr(t, rec); got != c.wantErr {
			t.Errorf("sibling %d %q: error %q, want %q", c.status, c.body, got, c.wantErr)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("sibling %d %q: Content-Type %q, want application/json", c.status, c.body, got)
		}
		if c.wantCode == c.status && rec.Body.String() != c.body {
			t.Errorf("sibling %d: body %q, want verbatim %q", c.status, rec.Body.String(), c.body)
		}
		edgeFPAssertSafeHeaders(t, "non-200", rec)
	}
}

// ---------------------------------------------------------------------------
// the three paired proxies
// ---------------------------------------------------------------------------

// Contract R2-G nodesArtifactHandler: "Non-GET → 405; unknown `node` → `404
// {"error":"no such node"}`" — neither reaches the network.
func TestEdgeFleetParityNodesArtifactRefusals(t *testing.T) {
	edgeFleetEnv(t)
	call := edgeFPStubProxy(t, http.StatusOK, "", "should not be read", nil)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := edgeFleetDo(nodesArtifactHandler, edgeFleetReq(m, "/api/nodes/artifact?node=ghost&slug=s&name=x.md", nil, "127.0.0.1:1"))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, rec.Code)
		}
	}
	for _, node := range []string{"ghost", ""} {
		rec := edgeFleetDo(nodesArtifactHandler, edgeFleetReq(http.MethodGet, "/api/nodes/artifact?node="+node+"&slug=s&name=x.md", nil, "127.0.0.1:1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("node %q: status %d, want 404", node, rec.Code)
			continue
		}
		if got := edgeFleetErr(t, rec); got != "no such node" {
			t.Errorf("node %q: error %q, want no such node", node, got)
		}
	}
	if call.calls != 0 {
		t.Errorf("refused requests reached the sibling %d times", call.calls)
	}
}

// Contract R2-G nodesArtifactHandler: "forwards `GET
// <nodeBase(n)>/api/node/artifact?slug=&name=` (both
// `url.Values`-encoded) through `relayBytes(w, n, req,
// nodesProxyArtifactTimeout, artifactMimeType(name))` with `const
// nodesProxyArtifactTimeout = 15 * time.Second`" — a name with `/`, a space,
// `#` and `&` arrives intact and unsplit, and nothing but slug and name is
// forwarded.
func TestEdgeFleetParityNodesArtifactForwardsEscaped(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSeedRemote(t, st, "nFWD", "http://100.64.9.10:7777")
	call := edgeFPStubProxy(t, http.StatusOK, "text/html", "bytes", nil)
	slug, name := "s x&y", "dir/sub dir/10 arch#v2&x=1.html"
	q := url.Values{"node": {"nFWD"}, "slug": {slug}, "name": {name}, "evil": {"1"}}
	rec := edgeFleetDo(nodesArtifactHandler, edgeFleetReq(http.MethodGet, "/api/nodes/artifact?"+q.Encode(), nil, "127.0.0.1:1"))
	if rec.Code != http.StatusOK || rec.Body.String() != "bytes" {
		t.Fatalf("status %d %q, want 200 bytes", rec.Code, rec.Body.String())
	}
	if call.calls != 1 || call.req == nil {
		t.Fatalf("sibling called %d times", call.calls)
	}
	if call.req.Method != http.MethodGet {
		t.Errorf("forwarded method %s, want GET", call.req.Method)
	}
	if call.req.URL.Path != "/api/node/artifact" {
		t.Errorf("forwarded path %q, want /api/node/artifact", call.req.URL.Path)
	}
	fq := call.req.URL.Query()
	if fq.Get("slug") != slug || fq.Get("name") != name {
		t.Errorf("forwarded slug=%q name=%q, want %q %q", fq.Get("slug"), fq.Get("name"), slug, name)
	}
	if len(fq) != 2 {
		t.Errorf("forwarded query %v, want exactly slug and name", fq)
	}
	if call.req.URL.Fragment != "" {
		t.Errorf("the # in the name became a fragment: %q", call.req.URL.Fragment)
	}
	if call.timeout != 15*time.Second || nodesProxyArtifactTimeout != 15*time.Second {
		t.Errorf("timeout %v (const %v), want 15s", call.timeout, nodesProxyArtifactTimeout)
	}
	if got, want := rec.Header().Get("Content-Type"), artifactMimeType(name); got != want {
		t.Errorf("Content-Type %q, want artifactMimeType(name) %q", got, want)
	}
	edgeFPAssertSafeHeaders(t, "nodes artifact 200", rec)
}

// Contract R2-G nodesTranscriptHandler / nodesThreadReadHandler:
// `nodesGetProxy(w, r, "/api/node/transcript", "slug", "n", "turns")` and
// `nodesGetProxy(w, r, "/api/node/thread/read", "slug", "tid", "n")` — an
// unknown node is 404 without a network call; a sibling's `400 missing tid`
// arrives verbatim; only the named keys are forwarded (the literal reading
// of the key list — listed as a silence).
func TestEdgeFleetParityNodesGetProxies(t *testing.T) {
	st := edgeFleetEnv(t)
	edgeFleetSeedRemote(t, st, "nGP", "http://100.64.9.11:7777")
	call := edgeFPStubProxy(t, http.StatusBadRequest, "text/plain", `{"error":"missing tid"}`, nil)
	for path, h := range map[string]http.HandlerFunc{"/api/nodes/transcript": nodesTranscriptHandler, "/api/nodes/thread/read": nodesThreadReadHandler} {
		if rec := edgeFleetDo(h, edgeFleetReq(http.MethodGet, path+"?node=ghost&slug=s", nil, "127.0.0.1:1")); rec.Code != http.StatusNotFound {
			t.Errorf("%s unknown node: status %d, want 404", path, rec.Code)
		}
	}
	if call.calls != 0 {
		t.Fatalf("unknown node reached the network %d times", call.calls)
	}

	rec := edgeFleetDo(nodesThreadReadHandler, edgeFleetReq(http.MethodGet, "/api/nodes/thread/read?node=nGP&slug=s&n=4&turns=9&name=z", nil, "127.0.0.1:1"))
	if rec.Code != http.StatusBadRequest || edgeFleetErr(t, rec) != "missing tid" {
		t.Errorf("relayed missing tid: %d %q, want 400 missing tid", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("relayed Content-Type %q, want application/json", got)
	}
	if call.req.URL.Path != "/api/node/thread/read" {
		t.Errorf("forwarded path %q", call.req.URL.Path)
	}
	fq := call.req.URL.Query()
	if fq.Get("slug") != "s" || fq.Get("n") != "4" || fq.Has("turns") || fq.Has("name") || fq.Has("node") {
		t.Errorf("thread/read forwarded %v, want slug and n only (no tid given)", fq)
	}

	edgeFleetDo(nodesTranscriptHandler, edgeFleetReq(http.MethodGet, "/api/nodes/transcript?node=nGP&slug=s+x&turns=3&tid=t9", nil, "127.0.0.1:1"))
	if call.req.URL.Path != "/api/node/transcript" {
		t.Errorf("forwarded path %q", call.req.URL.Path)
	}
	fq = call.req.URL.Query()
	if fq.Get("slug") != "s x" || fq.Get("turns") != "3" || fq.Has("tid") || fq.Has("node") {
		t.Errorf("transcript forwarded %v, want slug and turns only", fq)
	}
}

// Contract R2-G routePolicy/policyFor: "`policyFor` answers `TierNode` for
// `/api/node/artifact`, `/api/node/transcript`, `/api/node/thread/read`
// (explicit entries) and `TierPaired` for `/api/nodes/artifact`,
// `/api/nodes/transcript`, `/api/nodes/thread/read`."
func TestEdgeFleetParityPolicyTiers(t *testing.T) {
	for path, want := range map[string]Tier{
		"/api/node/artifact":     TierNode,
		"/api/node/transcript":   TierNode,
		"/api/node/thread/read":  TierNode,
		"/api/nodes/artifact":    TierPaired,
		"/api/nodes/transcript":  TierPaired,
		"/api/nodes/thread/read": TierPaired,
	} {
		if got := policyFor(path); got != want {
			t.Errorf("policyFor(%q) = %v, want %v", path, got, want)
		}
	}
}
