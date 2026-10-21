package api

// fleetparity_contract_test.go — athena-logic's BLIND contract tests for the
// multiplayer-kraken job's Round 2 ("remote parity"), Slice R2-G. Authored
// from artifacts/50-contract.md alone; no implementation file was read. LOGIC
// dimension: the stated invariants and happy paths, one strict answer each.
// Refusals (405s, private/unknown 404s, empty names, 502s, the 16 MB cap,
// empty-name artifact drops) are athena-edge's.
//
// Every test is TestContractFleetParity…; every package-level identifier is
// prefixed contractFP. Harness helpers from fleet_contract_test.go
// (contractFleetEnv, contractFleetSeedJob, contractFleetMux, contractFleetDo,
// contractFleetJSON, contractFleetRemote, contractFleetRegisterRemote) and the
// package's other test files (assertShape, seedTranscript, postFeedback) are
// reused, not restated.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractFPSeed seeds a job with a visibility (vis "" leaves the default,
// which EffectiveVisibility reads as readonly) and returns it, with the board
// cache dropped so the node routes build from this test's rows.
func contractFPSeed(t *testing.T, slug, extra, vis string) *jobs.Job {
	t.Helper()
	contractFleetSeedJob(t, slug, extra, vis)
	webBoardCache.invalidate()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("seed %s vanished", slug)
	}
	return j
}

// contractFPArtifacts stores the job's artifact rows through the block-event
// sync's own write path (the same seam artifacts_storemode_test.go uses), so
// a not-live job serves them from ArtifactBytes(j, "pluto", name).
func contractFPArtifacts(t *testing.T, j *jobs.Job, files map[string]string) {
	t.Helper()
	var af []store.ArtifactFile
	for name, body := range files {
		af = append(af, store.ArtifactFile{Path: name, Bytes: []byte(body), Mtime: 1})
	}
	if err := jobs.ActiveStore().SyncArtifacts(j.ID, af, nil); err != nil {
		t.Fatal(err)
	}
	webBoardCache.invalidate()
}

// contractFPPopulated is a webJob carrying every read key round 2 names, an
// owner and an account (the contract_test.go populated fixture, extended).
func contractFPPopulated() webJob {
	return webJob{
		Slug: "tech-comms", Repo: "monolith", State: "blocked", Live: true,
		Color: "purple", Cell: "tech-comms-77", Visibility: "public",
		Artifacts: []webArtifact{
			{Name: "00-overview.md", URL: "/api/artifact/tech-comms/cell/00-overview.md"},
			{Name: "10-arch.html", URL: "/api/artifact/tech-comms/cell/10-arch.html"},
		},
		Detail: "zeus said something",
		Question: &webQuestion{
			ToolUseID: "tu_1",
			Questions: []webQuestionItem{{
				Question: "which approach?", Header: "approach",
				Options: []webQuestionOption{{Label: "A", Description: "do A"}},
			}},
		},
		Roster:       []webAgent{{Name: "hephaestus-x", Model: "opus", Role: "hephaestus", Status: "running", Working: true, Kind: "teammate"}},
		PRs:          []jobs.JobPR{{URL: "https://github.com/car1zhang/kraken/pull/7", State: "open"}},
		ModTime:      123,
		LastSent:     "pivot to plan B",
		DocQuestions: []webDocQuestion{{ID: "Q1", Text: "which approach?", Importance: "P1", Answer: "A"}},
		Feedback:     []webFeedback{{ID: 1, Artifact: "00-overview.md", Note: "tighten the intro", At: 1234567890}},
		Threads:      []webThread{{TID: "t1", Title: "why the retry loop", State: "live", Pane: "%42", CreatedAt: 1234567890}},
		Tokens:       &webTokens{In: 1, Out: 2, CacheWrite: 3, CacheRead: 4, Sessions: 5, UpdatedAt: 6, ZeusIn: 1, ZeusOut: 1},
		Load:         &webJobLoad{Unblock: 2, Scope: 8, Note: "one decision", At: 1769040000000},
		Goal:         &webJobGoal{ID: 3, Text: "ship goal tracking"},
		Account:      &webJobAccount{Slug: "dorisexplorer4", Email: "dorisexplorer4@gmail.com", At: 1769040000000},
		Owner:        &webJobOwner{NodeID: "n-b", Label: "studio-mini", Operator: "Jess", Online: true},
	}
}

// ---------------------------------------------------------------------------
// nodeCard / remoteArtifactURL / strippedCards / remoteCards
// ---------------------------------------------------------------------------

// Contract R2-G nodeCard: "Clears exactly Owner and Account. Every other key
// the board built — feedback, tokens, threads, doc_questions, artifacts, prs,
// load, goals/goal, question, roster, detail, last_sent, color, visibility … —
// is left as is. The artifact URLs it leaves are the OWNER's".
func TestContractFleetParityNodeCardClearsOnlyOwnerAndAccount(t *testing.T) {
	in := contractFPPopulated()
	want := contractFPPopulated()
	want.Owner = nil
	want.Account = nil
	got := nodeCard(in)
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		t.Fatalf("nodeCard =\n%s\nwant\n%s", gb, wb)
	}
	if got.Artifacts[0].URL != "/api/artifact/tech-comms/cell/00-overview.md" {
		t.Errorf("nodeCard rewrote an artifact URL: %q, want the owner's", got.Artifacts[0].URL)
	}
}

// Contract R2-G remoteArtifactURL: "Returns "/api/nodes/artifact?node=" +
// url.QueryEscape(nodeID) + "&slug=" + url.QueryEscape(slug) + "&name=" +
// url.QueryEscape(name)".
func TestContractFleetParityRemoteArtifactURL(t *testing.T) {
	cases := []struct{ node, slug, name, want string }{
		{"n-b", "tech-comms", "10-arch.html", "/api/nodes/artifact?node=n-b&slug=tech-comms&name=10-arch.html"},
		{"n-b", "tech-comms", "00-overview.md", "/api/nodes/artifact?node=n-b&slug=tech-comms&name=00-overview.md"},
		{"nea6/fba5", "a b", "nested/diagram one.svg", "/api/nodes/artifact?node=nea6%2Ffba5&slug=a+b&name=nested%2Fdiagram+one.svg"},
		{"n&x", "s=1", "q?.html", "/api/nodes/artifact?node=" + url.QueryEscape("n&x") + "&slug=" + url.QueryEscape("s=1") + "&name=" + url.QueryEscape("q?.html")},
	}
	for _, c := range cases {
		if got := remoteArtifactURL(c.node, c.slug, c.name); got != c.want {
			t.Errorf("remoteArtifactURL(%q,%q,%q) = %q, want %q", c.node, c.slug, c.name, got, c.want)
		}
	}
	// name is LAST, and .html survives QueryEscape: the SPA's html sniff
	// reads the URL's tail.
	if got := remoteArtifactURL("n", "s", "10-arch.html"); !strings.HasSuffix(got, ".html") {
		t.Errorf("remoteArtifactURL tail = %q, want it to end .html", got)
	}
}

// contractFPCardJSON is one stored sibling card: the owner's artifact URLs, an
// account binding, and every read key.
const contractFPCardJSON = `{"slug":"tech-comms","repo":"monolith","state":"blocked","live":true,"mtime":1,` +
	`"visibility":"readonly","detail":"zeus said","last_sent":"go","color":"cyan",` +
	`"artifacts":[{"name":"00-overview.md","url":"/api/artifact/tech-comms/cell/00-overview.md"},` +
	`{"name":"10-arch.html","url":"javascript:alert(1)"}],` +
	`"feedback":[{"id":1,"artifact":"00-overview.md","note":"tighten","at":5}],` +
	`"threads":[{"tid":"t1","title":"why","state":"live","role":"","created_at":5}],` +
	`"doc_questions":[{"id":"Q1","text":"which?","importance":"P1"}],` +
	`"tokens":{"in":1,"out":2,"cache_write":3,"cache_read":4,"sessions":1,"updated_at":9,"zeus_in":1,"zeus_out":1,"zeus_cache_write":1,"zeus_cache_read":1},` +
	`"prs":[{"url":"https://github.com/o/r/pull/1","state":"open"}],` +
	`"account":{"slug":"x","email":"x@y","at":1}}`

// contractFPWantStripped decodes the stored card the plain way and applies
// the contract's transform by hand: Owner and Account cleared, every
// artifact URL replaced by remoteArtifactURL(nodeID, slug, name).
func contractFPWantStripped(t *testing.T, nodeID, cardJSON string) webJob {
	t.Helper()
	var w webJob
	if err := json.Unmarshal([]byte(cardJSON), &w); err != nil {
		t.Fatal(err)
	}
	w.Owner, w.Account = nil, nil
	for i := range w.Artifacts {
		w.Artifacts[i].URL = remoteArtifactURL(nodeID, w.Slug, w.Artifacts[i].Name)
	}
	return w
}

// Contract R2-G strippedCards: "each card passes nodeCard, then every
// artifact's URL is REPLACED by remoteArtifactURL(nodeID, card.Slug, a.Name)
// … A stored card's own url values are never read."
func TestContractFleetParityStrippedCardsRewritesArtifactURLs(t *testing.T) {
	cards, ok := strippedCards("n-b", "["+contractFPCardJSON+"]")
	if !ok {
		t.Fatal("strippedCards ok = false on a well-formed list")
	}
	if len(cards) != 1 {
		t.Fatalf("strippedCards = %d cards, want 1", len(cards))
	}
	want := contractFPWantStripped(t, "n-b", contractFPCardJSON)
	if !reflect.DeepEqual(cards[0], want) {
		gb, _ := json.Marshal(cards[0])
		wb, _ := json.Marshal(want)
		t.Fatalf("strippedCards card =\n%s\nwant\n%s", gb, wb)
	}
	urls := []string{cards[0].Artifacts[0].URL, cards[0].Artifacts[1].URL}
	wantURLs := []string{
		"/api/nodes/artifact?node=n-b&slug=tech-comms&name=00-overview.md",
		"/api/nodes/artifact?node=n-b&slug=tech-comms&name=10-arch.html",
	}
	if !reflect.DeepEqual(urls, wantURLs) {
		t.Errorf("artifact urls = %v, want %v", urls, wantURLs)
	}
	if cards[0].Account != nil {
		t.Errorf("account survived the strip: %+v", cards[0].Account)
	}
	if len(cards[0].Feedback) != 1 || len(cards[0].Threads) != 1 || len(cards[0].DocQuestions) != 1 || cards[0].Tokens == nil || len(cards[0].PRs) != 1 {
		t.Errorf("a read key was stripped: feedback %d threads %d doc_questions %d tokens %v prs %d",
			len(cards[0].Feedback), len(cards[0].Threads), len(cards[0].DocQuestions), cards[0].Tokens, len(cards[0].PRs))
	}
}

// Contract R2-G strippedCards: "remoteCards passes n.ID" — the board's remote
// cards carry the rewritten URLs and the owner stamped from the row.
func TestContractFleetParityRemoteCardsCarryProxyURLs(t *testing.T) {
	rows := []*store.NodeRow{{ID: "n-b", Label: "studio-mini", Operator: "Jess", Online: true,
		JobsJSON: "[" + contractFPCardJSON + "]"}}
	cards := remoteCards(rows)
	if len(cards) != 1 {
		t.Fatalf("remoteCards = %d cards, want 1", len(cards))
	}
	want := contractFPWantStripped(t, "n-b", contractFPCardJSON)
	want.Owner = &webJobOwner{NodeID: "n-b", Label: "studio-mini", Operator: "Jess", Online: true}
	if !reflect.DeepEqual(cards[0], want) {
		gb, _ := json.Marshal(cards[0])
		wb, _ := json.Marshal(want)
		t.Fatalf("remoteCards card =\n%s\nwant\n%s", gb, wb)
	}
}

// Contract R2-G strippedCards: "webNodeFrom(n, true) passes n.ID"; ios/API.md
// row: "/api/nodes jobs carry the rewritten URLs".
func TestContractFleetParityNodesRouteCarriesProxyURLs(t *testing.T) {
	st := contractFleetEnv(t)
	contractFleetRegisterRemote(t, st, "n-b", "100.70.1.2", 7777)
	if err := st.SetNodePoll("n-b", true, "", "["+contractFPCardJSON+"]", 5); err != nil {
		t.Fatal(err)
	}
	rec := contractFleetDo(contractFleetMux(t), http.MethodGet, "/api/nodes", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/nodes: %d %s", rec.Code, rec.Body)
	}
	var page struct {
		Nodes []struct {
			NodeID string   `json:"node_id"`
			Jobs   []webJob `json:"jobs"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 1 || len(page.Nodes[0].Jobs) != 1 {
		t.Fatalf("GET /api/nodes = %s, want one node with one job", rec.Body)
	}
	got := page.Nodes[0].Jobs[0]
	want := contractFPWantStripped(t, "n-b", contractFPCardJSON)
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		t.Errorf("/api/nodes job =\n%s\nwant\n%s", gb, wb)
	}
}

// ---------------------------------------------------------------------------
// /api/node/jobs serves the read keys (nodeCard on the outbound side)
// ---------------------------------------------------------------------------

// Contract R2-G nodeCard (superseding round 1's nodeJobsHandler strip) +
// ios/API.md row: "/api/node/jobs cards are the board's objects minus owner
// and account (artifact urls are the owner's …)".
func TestContractFleetParityNodeJobsServesReadKeys(t *testing.T) {
	contractFleetEnv(t)
	j := contractFPSeed(t, "ro-parity", "state: done\n", "readonly")
	contractFPArtifacts(t, j, map[string]string{"00-overview.md": "# Overview\n"})
	if rec := postFeedback(t, feedbackReq{Slug: "ro-parity", Home: "pluto", Name: "00-overview.md", Feedback: "tighten"}); rec.Code != http.StatusOK {
		t.Fatalf("seed feedback: %d %s", rec.Code, rec.Body)
	}
	if err := jobs.ActiveStore().SetJobAccount(j.ID, "slot-a", "one@example.com", 1769040000000); err != nil {
		t.Fatal(err)
	}
	webBoardCache.invalidate()

	rec := contractFleetDo(contractFleetMux(t), http.MethodGet, "/api/node/jobs", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/node/jobs: %d %s", rec.Code, rec.Body)
	}
	var reply struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	for _, c := range reply.Jobs {
		if c["slug"] == "ro-parity" {
			card = c
		}
	}
	if card == nil {
		t.Fatalf("ro-parity missing from /api/node/jobs: %s", rec.Body)
	}
	if _, ok := card["account"]; ok {
		t.Errorf("node card carries account: %v", card["account"])
	}
	if _, ok := card["owner"]; ok {
		t.Errorf("node card carries owner: %v", card["owner"])
	}
	arts, _ := card["artifacts"].([]any)
	if len(arts) != 1 {
		t.Fatalf("node card artifacts = %v, want the one stored artifact", card["artifacts"])
	}
	a := arts[0].(map[string]any)
	if a["name"] != "00-overview.md" || a["url"] != "/api/artifact/ro-parity/pluto/00-overview.md" {
		t.Errorf("node card artifact = %v, want name 00-overview.md and the OWNER's url /api/artifact/ro-parity/pluto/00-overview.md", a)
	}
	fb, _ := card["feedback"].([]any)
	if len(fb) != 1 || fb[0].(map[string]any)["note"] != "tighten" {
		t.Errorf("node card feedback = %v, want the one row", card["feedback"])
	}
}

// ---------------------------------------------------------------------------
// visibleJob and the loosened read routes
// ---------------------------------------------------------------------------

// Contract R2-G visibleJob: "readonly and public return the job with nothing
// written."
func TestContractFleetParityVisibleJobAdmitsReadonlyAndPublic(t *testing.T) {
	contractFleetEnv(t)
	contractFPSeed(t, "vis-ro", "", "readonly")
	contractFPSeed(t, "vis-default", "", "")
	contractFPSeed(t, "vis-pub", "state: working\n", "public")
	for _, slug := range []string{"vis-ro", "vis-default", "vis-pub"} {
		rec := httptest.NewRecorder()
		j := visibleJob(rec, slug)
		if j == nil {
			t.Errorf("visibleJob(%s) = nil, want the job", slug)
			continue
		}
		if j.Slug != slug {
			t.Errorf("visibleJob(%s) returned %s", slug, j.Slug)
		}
		if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
			t.Errorf("visibleJob(%s) wrote %q / headers %v, want nothing", slug, rec.Body, rec.Header())
		}
	}
}

// Contract R2-G nodeJobRawHandler: "a readonly job answers 200 with exactly
// GET /api/job/raw's body (slug, content, mtime, editable …)".
func TestContractFleetParityNodeJobRawAnswersReadonly(t *testing.T) {
	contractFleetEnv(t)
	contractFPSeed(t, "ro-raw", "state: done\ncolor: cyan\n", "readonly")
	mux := contractFleetMux(t)
	local := contractFleetDo(mux, http.MethodGet, "/api/job/raw?slug=ro-raw", "", "", nil)
	remote := contractFleetDo(mux, http.MethodGet, "/api/node/job/raw?slug=ro-raw", "", "", nil)
	if remote.Code != http.StatusOK {
		t.Fatalf("GET /api/node/job/raw readonly: %d %s, want 200", remote.Code, remote.Body)
	}
	if local.Code != http.StatusOK || local.Body.String() != remote.Body.String() {
		t.Errorf("node raw body differs from local raw:\nlocal %d %s\nnode  %d %s", local.Code, local.Body, remote.Code, remote.Body)
	}
	m := contractFleetJSON(t, "node raw", remote.Body.Bytes())
	for _, k := range []string{"slug", "content", "mtime", "editable"} {
		if _, ok := m[k]; !ok {
			t.Errorf("node raw lacks %q: %v", k, m)
		}
	}
}

// Contract R2-G nodeReadHandler: "gated by visibleJob; the 409
// {"error":"<slug> has no live zeus"} rule … unchanged" — a readonly job
// passes the gate and reaches the liveness rule (no tmux cell in a test).
func TestContractFleetParityNodeReadAdmitsReadonly(t *testing.T) {
	contractFleetEnv(t)
	contractFPSeed(t, "ro-read", "state: done\n", "readonly")
	rec := contractFleetDo(contractFleetMux(t), http.MethodGet, "/api/node/read?slug=ro-read&n=3", "", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("GET /api/node/read readonly, no zeus: %d %s, want 409", rec.Code, rec.Body)
	}
	if m := contractFleetJSON(t, "node read", rec.Body.Bytes()); m["error"] != "ro-read has no live zeus" {
		t.Errorf("node read error = %v, want \"ro-read has no live zeus\"", m["error"])
	}
}

// ---------------------------------------------------------------------------
// serveLocalGet
// ---------------------------------------------------------------------------

type contractFPCtxKey struct{}

// Contract R2-G serveLocalGet: "Runs h on a synthesized GET path?q.Encode()
// carrying r.Context() and r.RemoteAddr … The reply is whatever h writes,
// byte for byte."
func TestContractFleetParityServeLocalGet(t *testing.T) {
	var seenMethod, seenPath, seenRawQuery, seenRemote string
	var seenCtx any
	h := func(w http.ResponseWriter, r *http.Request) {
		seenMethod, seenPath, seenRawQuery, seenRemote = r.Method, r.URL.Path, r.URL.RawQuery, r.RemoteAddr
		seenCtx = r.Context().Value(contractFPCtxKey{})
		w.Header().Set("X-Contract-FP", "1")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "{\"verbatim\": true}\n  ")
	}
	in := httptest.NewRequest(http.MethodPost, "/api/node/transcript?slug=ignored&extra=1", strings.NewReader("body"))
	in.RemoteAddr = "100.70.1.2:40000"
	in = in.WithContext(context.WithValue(in.Context(), contractFPCtxKey{}, "carried"))
	q := url.Values{"slug": {"tech-comms"}, "turns": {"4"}}

	rec := httptest.NewRecorder()
	serveLocalGet(rec, in, "/api/job/transcript", h, q)

	if seenMethod != http.MethodGet {
		t.Errorf("h saw method %s, want GET", seenMethod)
	}
	if seenPath != "/api/job/transcript" {
		t.Errorf("h saw path %q, want /api/job/transcript", seenPath)
	}
	if seenRawQuery != q.Encode() {
		t.Errorf("h saw query %q, want %q", seenRawQuery, q.Encode())
	}
	if seenRemote != "100.70.1.2:40000" {
		t.Errorf("h saw RemoteAddr %q, want the caller's", seenRemote)
	}
	if seenCtx != "carried" {
		t.Errorf("h's context value = %v, want the caller's context", seenCtx)
	}
	if rec.Code != http.StatusTeapot || rec.Body.String() != "{\"verbatim\": true}\n  " || rec.Header().Get("X-Contract-FP") != "1" {
		t.Errorf("reply = %d %q (hdr %q), want h's 418 and bytes verbatim", rec.Code, rec.Body, rec.Header().Get("X-Contract-FP"))
	}
}

// ---------------------------------------------------------------------------
// node-tier read routes (the owner's side)
// ---------------------------------------------------------------------------

// Contract R2-G nodeArtifactHandler: "not live → ArtifactBytes(j, "pluto",
// name) … A 200 carries Content-Type: artifactMimeType(name),
// X-Content-Type-Options: nosniff, Cache-Control: no-store and the bytes
// verbatim". Both readonly and public pass.
func TestContractFleetParityNodeArtifactServesStoredBytes(t *testing.T) {
	contractFleetEnv(t)
	files := map[string]string{
		"00-overview.md": "# Overview\n\nbody  \n",
		"10-arch.html":   "<!doctype html><p>arch</p>",
	}
	ro := contractFPSeed(t, "ro-art", "state: done\n", "readonly")
	contractFPArtifacts(t, ro, files)
	pub := contractFPSeed(t, "pub-art", "state: done\n", "public")
	contractFPArtifacts(t, pub, files)
	mux := contractFleetMux(t)

	for _, slug := range []string{"ro-art", "pub-art"} {
		for name, body := range files {
			target := "/api/node/artifact?" + url.Values{"slug": {slug}, "name": {name}}.Encode()
			rec := contractFleetDo(mux, http.MethodGet, target, "", "", nil)
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s: %d %s, want 200", target, rec.Code, rec.Body)
				continue
			}
			if rec.Body.String() != body {
				t.Errorf("GET %s body = %q, want %q verbatim", target, rec.Body, body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != artifactMimeType(name) {
				t.Errorf("GET %s Content-Type = %q, want artifactMimeType = %q", target, ct, artifactMimeType(name))
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("GET %s headers nosniff %q, cache %q; want nosniff, no-store", target,
					rec.Header().Get("X-Content-Type-Options"), rec.Header().Get("Cache-Control"))
			}
		}
	}
}

// Contract R2-G nodeTranscriptHandler: "serveLocalGet(w, r,
// "/api/job/transcript", jobTranscriptHandler, q) with q = slug: j.Slug plus
// n and turns copied … The body is exactly GET /api/job/transcript's".
func TestContractFleetParityNodeTranscriptIsLocalTranscript(t *testing.T) {
	contractFleetEnv(t)
	contractFPSeed(t, "ro-tr", "state: done\n", "readonly")
	contractFPSeed(t, "pub-tr", "state: done\n", "public")
	mux := contractFleetMux(t)
	for _, slug := range []string{"ro-tr", "pub-tr"} {
		for _, extra := range []string{"", "&turns=5", "&n=7"} {
			local := contractFleetDo(mux, http.MethodGet, "/api/job/transcript?slug="+slug+extra, "", "", nil)
			remote := contractFleetDo(mux, http.MethodGet, "/api/node/transcript?slug="+slug+extra, "", "", nil)
			if remote.Code != local.Code || remote.Body.String() != local.Body.String() {
				t.Errorf("%s%s: node %d %s, local %d %s — must be identical", slug, extra, remote.Code, remote.Body, local.Code, local.Body)
			}
		}
	}
	rec := contractFleetDo(mux, http.MethodGet, "/api/node/transcript?slug=ro-tr", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node transcript readonly = %d %s, want 200", rec.Code, rec.Body)
	}
	assertShape(t, "/api/node/transcript", rec.Body.Bytes(), jobTranscriptSpec)
}

// Contract R2-G nodeThreadReadHandler: "serveLocalGet(w, r,
// "/api/thread/read", threadReadHandler, q) with slug: j.Slug plus tid, n
// copied".
func TestContractFleetParityNodeThreadReadIsLocalThreadRead(t *testing.T) {
	contractFleetEnv(t)
	j := contractFPSeed(t, "ro-thread", "state: done\n", "readonly")
	sess := "fpfpfpfp-1111-2222-3333-444444444444"
	if err := jobs.ActiveStore().AddThread(store.Thread{
		JobID: j.ID, TID: "t1", Title: "why the retry", SessionID: sess, ForkUUID: "a1", State: "closed", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	seedTranscript(t, sess,
		`{"type":"user","uuid":"u1","message":{"content":"zeus, do the thing"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"doing it"}]}}`,
		`{"type":"user","uuid":"u2","message":{"content":"why the retry?"}}`,
	)
	mux := contractFleetMux(t)
	for _, extra := range []string{"&tid=t1", "&tid=t1&n=2"} {
		local := contractFleetDo(mux, http.MethodGet, "/api/thread/read?slug=ro-thread"+extra, "", "", nil)
		remote := contractFleetDo(mux, http.MethodGet, "/api/node/thread/read?slug=ro-thread"+extra, "", "", nil)
		if remote.Code != http.StatusOK {
			t.Errorf("node thread read %s = %d %s, want 200", extra, remote.Code, remote.Body)
		}
		if remote.Code != local.Code || remote.Body.String() != local.Body.String() {
			t.Errorf("%s: node %d %s, local %d %s — must be identical", extra, remote.Code, remote.Body, local.Code, local.Body)
		}
	}
}

// ---------------------------------------------------------------------------
// relayBytes and the paired proxies (the viewer's side)
// ---------------------------------------------------------------------------

// Contract R2-G relayBytes: "Sets X-Content-Type-Options: nosniff and
// Cache-Control: no-store on EVERY reply … Sibling 200 → status 200,
// Content-Type: mimeType (the CALLER's value …; the sibling's Content-Type
// header is never read) and the body verbatim. Any other sibling status → …
// the sibling's status and body as application/json."
func TestContractFleetParityRelayBytes(t *testing.T) {
	sib := &contractFleetRemote{status: http.StatusOK, reply: "<svg>not json</svg>"}
	srv, _, _ := sib.start(t)
	node := &store.NodeRow{ID: "n-b", Label: "studio-mini"}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/node/artifact?slug=s&name=a.svg", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	relayBytes(rec, node, req, 5*time.Second, "image/svg+xml")
	if rec.Code != http.StatusOK || rec.Body.String() != "<svg>not json</svg>" {
		t.Errorf("relayBytes 200 = %d %q, want 200 and the bytes verbatim", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("relayBytes Content-Type = %q, want the caller's image/svg+xml (the sibling sent application/json)", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("relayBytes 200 headers: nosniff %q cache %q", rec.Header().Get("X-Content-Type-Options"), rec.Header().Get("Cache-Control"))
	}

	sib.mu.Lock()
	sib.status, sib.reply = http.StatusNotFound, `{"error":"no such artifact"}`
	sib.mu.Unlock()
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/node/artifact?slug=s&name=a.svg", nil)
	rec = httptest.NewRecorder()
	relayBytes(rec, node, req, 5*time.Second, "image/svg+xml")
	if rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"no such artifact"}` {
		t.Errorf("relayBytes 404 = %d %q, want the sibling's 404 and body", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("relayBytes non-200 Content-Type = %q, want application/json", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("relayBytes 404 headers: nosniff %q cache %q", rec.Header().Get("X-Content-Type-Options"), rec.Header().Get("Cache-Control"))
	}
}

// Contract R2-G nodesArtifactHandler: "forwards GET <nodeBase(n)>/api/node/
// artifact?slug=&name= … through relayBytes(w, n, req,
// nodesProxyArtifactTimeout, artifactMimeType(name)) with const
// nodesProxyArtifactTimeout = 15 * time.Second."
func TestContractFleetParityNodesArtifactProxies(t *testing.T) {
	if nodesProxyArtifactTimeout != 15*time.Second {
		t.Errorf("nodesProxyArtifactTimeout = %v, want 15s", nodesProxyArtifactTimeout)
	}
	st := contractFleetEnv(t)
	sib := &contractFleetRemote{status: http.StatusOK, reply: "<!doctype html><p>arch</p>"}
	_, host, port := sib.start(t)
	contractFleetRegisterRemote(t, st, "n-b", host, port)

	rec := contractFleetDo(contractFleetMux(t), http.MethodGet,
		"/api/nodes/artifact?node=n-b&slug=tech-comms&name=10-arch.html", "", "", nil)
	if sib.method != http.MethodGet || sib.path != "/api/node/artifact" {
		t.Fatalf("sibling saw %s %s, want GET /api/node/artifact", sib.method, sib.path)
	}
	if !reflect.DeepEqual(sib.query, map[string]string{"slug": "tech-comms", "name": "10-arch.html"}) {
		t.Errorf("forwarded query = %v, want exactly slug=tech-comms name=10-arch.html", sib.query)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "<!doctype html><p>arch</p>" {
		t.Errorf("proxied artifact = %d %q, want 200 and the bytes verbatim", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != artifactMimeType("10-arch.html") {
		t.Errorf("proxied Content-Type = %q, want artifactMimeType(10-arch.html) = %q", ct, artifactMimeType("10-arch.html"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("proxied headers: nosniff %q cache %q", rec.Header().Get("X-Content-Type-Options"), rec.Header().Get("Cache-Control"))
	}

	// an escaped name is forwarded decoded-equal
	rec = contractFleetDo(contractFleetMux(t), http.MethodGet,
		"/api/nodes/artifact?"+url.Values{"node": {"n-b"}, "slug": {"tech-comms"}, "name": {"nested/diagram one.svg"}}.Encode(), "", "", nil)
	if sib.query["name"] != "nested/diagram one.svg" || sib.query["slug"] != "tech-comms" {
		t.Errorf("forwarded escaped query = %v", sib.query)
	}
	if ct := rec.Header().Get("Content-Type"); ct != artifactMimeType("nested/diagram one.svg") {
		t.Errorf("proxied svg Content-Type = %q, want %q", ct, artifactMimeType("nested/diagram one.svg"))
	}
}

// Contract R2-G nodesTranscriptHandler / nodesThreadReadHandler:
// "nodesGetProxy(w, r, "/api/node/transcript", "slug", "n", "turns")" and
// "nodesGetProxy(w, r, "/api/node/thread/read", "slug", "tid", "n")".
func TestContractFleetParityNodesTranscriptAndThreadProxy(t *testing.T) {
	st := contractFleetEnv(t)
	sib := &contractFleetRemote{status: http.StatusOK}
	_, host, port := sib.start(t)
	contractFleetRegisterRemote(t, st, "n-b", host, port)
	mux := contractFleetMux(t)

	sib.reply = `{"session":"s1","blocks":[{"kind":"asst","text":"working"}]}`
	rec := contractFleetDo(mux, http.MethodGet, "/api/nodes/transcript?node=n-b&slug=tech-comms&n=3&turns=4", "", "", nil)
	if sib.method != http.MethodGet || sib.path != "/api/node/transcript" {
		t.Fatalf("sibling saw %s %s, want GET /api/node/transcript", sib.method, sib.path)
	}
	if !reflect.DeepEqual(sib.query, map[string]string{"slug": "tech-comms", "n": "3", "turns": "4"}) {
		t.Errorf("forwarded transcript query = %v, want slug, n, turns", sib.query)
	}
	if rec.Code != http.StatusOK || !reflect.DeepEqual(contractFleetJSON(t, "tr", rec.Body.Bytes()), contractFleetJSON(t, "sib", []byte(sib.reply))) {
		t.Errorf("relayed transcript = %d %s, want the sibling's body", rec.Code, rec.Body)
	}

	sib.reply = `{"blocks":[],"counted":0,"live":false,"pane":"","fork_uuid":"a1","source":"","session":"s2"}`
	rec = contractFleetDo(mux, http.MethodGet, "/api/nodes/thread/read?node=n-b&slug=tech-comms&tid=t1&n=20", "", "", nil)
	if sib.method != http.MethodGet || sib.path != "/api/node/thread/read" {
		t.Fatalf("sibling saw %s %s, want GET /api/node/thread/read", sib.method, sib.path)
	}
	if !reflect.DeepEqual(sib.query, map[string]string{"slug": "tech-comms", "tid": "t1", "n": "20"}) {
		t.Errorf("forwarded thread query = %v, want slug, tid, n", sib.query)
	}
	if rec.Code != http.StatusOK || !reflect.DeepEqual(contractFleetJSON(t, "th", rec.Body.Bytes()), contractFleetJSON(t, "sib", []byte(sib.reply))) {
		t.Errorf("relayed thread read = %d %s, want the sibling's body", rec.Code, rec.Body)
	}
}

// ---------------------------------------------------------------------------
// routing
// ---------------------------------------------------------------------------

// Contract R2-G routePolicy/policyFor/Register: "policyFor answers TierNode
// for /api/node/artifact, /api/node/transcript, /api/node/thread/read … and
// TierPaired for /api/nodes/artifact, /api/nodes/transcript,
// /api/nodes/thread/read … All six registered in register.go".
func TestContractFleetParityRoutePolicy(t *testing.T) {
	want := map[string]Tier{
		"/api/node/artifact":     TierNode,
		"/api/node/transcript":   TierNode,
		"/api/node/thread/read":  TierNode,
		"/api/nodes/artifact":    TierPaired,
		"/api/nodes/transcript":  TierPaired,
		"/api/nodes/thread/read": TierPaired,
	}
	registered := map[string]bool{}
	for _, p := range RoutePatterns() {
		registered[p] = true
	}
	for path, tier := range want {
		if got := policyFor(path); got != tier {
			t.Errorf("policyFor(%s) = %v, want %v", path, got, tier)
		}
		if !registered[path] {
			t.Errorf("%s is not a registered pattern", path)
		}
	}
	for _, path := range []string{"/api/node/artifact", "/api/node/transcript", "/api/node/thread/read"} {
		if tier, ok := routePolicy[path]; !ok || tier != TierNode {
			t.Errorf("routePolicy[%s] = %v (present %v), want an explicit TierNode entry", path, tier, ok)
		}
	}
}

// Contract R2-G: "Routed TierNode, not mirror-gated" / "Paired, not
// mirror-gated" / "none wrapped in mirrorGated" — under --mirror the reads
// still answer.
func TestContractFleetParityReadRoutesNotMirrorGated(t *testing.T) {
	st := contractFleetEnv(t)
	j := contractFPSeed(t, "ro-mirror", "state: done\n", "readonly")
	contractFPArtifacts(t, j, map[string]string{"00-overview.md": "# hi\n"})
	sib := &contractFleetRemote{status: http.StatusOK, reply: `{"session":"","blocks":[]}`}
	_, host, port := sib.start(t)
	contractFleetRegisterRemote(t, st, "n-b", host, port)
	mux := contractFleetMux(t)

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	local := []string{
		"/api/node/artifact?slug=ro-mirror&name=00-overview.md",
		"/api/node/transcript?slug=ro-mirror",
	}
	for _, target := range local {
		if rec := contractFleetDo(mux, http.MethodGet, target, "", "", nil); rec.Code != http.StatusOK {
			t.Errorf("mirror: GET %s = %d %s, want 200", target, rec.Code, rec.Body)
		}
	}
	proxied := []string{
		"/api/nodes/artifact?node=n-b&slug=tech-comms&name=00-overview.md",
		"/api/nodes/transcript?node=n-b&slug=tech-comms",
		"/api/nodes/thread/read?node=n-b&slug=tech-comms&tid=t1",
	}
	for _, target := range proxied {
		if rec := contractFleetDo(mux, http.MethodGet, target, "", "", nil); rec.Code != http.StatusOK {
			t.Errorf("mirror: GET %s = %d %s, want the sibling's 200", target, rec.Code, rec.Body)
		}
	}
}

// Contract R2-G apiContract: "Unchanged" (7).
func TestContractFleetParityContractStaysSeven(t *testing.T) {
	if apiContract != 7 {
		t.Errorf("apiContract = %d, want 7", apiContract)
	}
}
