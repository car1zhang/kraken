package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// threadmodel_edge_test.go — the EDGE dimension of the per-thread model rows
// of artifacts/50-contract.md (slice S1), for internal/api: the 400 and its
// EXACT body, the refusal ORDER, the ops that must ignore the field, and the
// wire's silence for a legacy row.
//
// Written BLIND, against the frozen contract text alone — no implementation
// was read. The obligations exercised here, verbatim:
//
//   - branch op validation: "Checked AFTER the existing title check (missing
//     title still wins). `model` empty → allowed (legacy behavior). `model`
//     non-empty and not a `core.ThreadModels` key → status 400, body exactly
//     `{"error":"bad model — want haiku, sonnet, opus or fable"}`, and
//     `jobs.BranchThread` is not called."
//   - `api.threadsReq.Model`: "Read only by `op:\"branch\"`; other ops ignore it."
//   - `api.webThread.Model`: "`webThreadsFrom` copies the store row's `Model`;
//     absent from the JSON for `\"\"`."
//   - the contract's note: "Model validation in the API happens after slug
//     resolution, so an unknown-slug request is 404 regardless of model."
//
// Wiring is this package's existing harness — useTestStore (storemode_test.go),
// mustCreateStoreJob (jobedit_storemode_test.go), decodeErr (feedback_test.go)
// — read at HEAD.
//
// Package-level identifiers are `edge`-prefixed by this package's own
// convention (submit_edge_test.go et al), and because a sibling blind author
// writes `contract`-prefixed declarations into the same package.

// edgeBadModelBody is the refusal the contract pins CHARACTER FOR CHARACTER,
// em dash and all. It is spelled out here rather than derived from any
// constant in the implementation: this string is the whole API contract for an
// invalid model, and a test that read it back from the code under test would
// accept whatever that code happened to say.
const edgeBadModelBody = `{"error":"bad model — want haiku, sonnet, opus or fable"}`

// edgeBadModels is the set that must be refused. It deliberately includes the
// four EXACT model ids the aliases resolve to: those are real, correct-looking
// ids, and they are not `core.ThreadModels` KEYS, so the API must refuse them
// exactly like garbage. Case variants are here for the same reason — the map
// is not case-folded, so "Haiku" is not a key.
var edgeBadModels = []string{
	"gpt-5",
	"claude-opus-5",
	"claude-haiku-4-5-20251001",
	"claude-sonnet-5",
	"claude-fable-5-1",
	"Haiku",
	"OPUS",
	"SoNnEt",
	"haiku ",
	" fable",
	"haiku,sonnet",
	"opusx",
	"hai",
	" ",
}

// edgePostThreads posts a threadsReq at the handler, exactly as
// threads_test.go's postThreads does at HEAD. Redeclared under an `edge` name
// so this file does not depend on a helper another author may adapt.
func edgePostThreads(t *testing.T, req threadsReq) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/threads", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	threadsHandler(rec, httpReq)
	return rec
}

func edgeGetThreads(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodGet, "/api/threads?"+query, nil)
	rec := httptest.NewRecorder()
	threadsHandler(rec, httpReq)
	return rec
}

// TestEdgeBranchBadModelIsA400WithTheExactBody is the headline refusal. The
// body is asserted BYTE FOR BYTE rather than by substring: the four aliases
// are the only thing that tells a client (or carl reading a toast) what to
// send instead, and a paraphrase — "invalid model", "bad model" alone, a
// listing that drops fable — passes every reasonable Contains check while
// leaving the caller no way to recover.
//
// That the request never reaches jobs.BranchThread is observable from the
// status alone: BranchThread demands a live tmux cell session, and these
// fixtures have none, so ANY response other than this 400 means the model
// check did not stop the request.
func TestEdgeBranchBadModelIsA400WithTheExactBody(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-badmodel", Repo: "monolith"})

	for _, model := range edgeBadModels {
		t.Run(model, func(t *testing.T) {
			rec := edgePostThreads(t, threadsReq{
				Slug: "edge-badmodel", Op: "branch", Title: "why is it blocked",
				Text: "explain the block", Model: model,
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("model %q: status %d, want 400, body %s", model, rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != edgeBadModelBody {
				t.Fatalf("model %q: body = %s, want exactly %s", model, got, edgeBadModelBody)
			}
		})
	}
}

// TestEdgeBranchMissingTitleBeatsBadModel pins the refusal ORDER the contract
// states outright ("Checked AFTER the existing title check (missing title
// still wins)"). Order is not cosmetic here: the client sends the model from a
// sticky localStorage picker and the title from a field carl may have left
// blank, so a request with both problems is overwhelmingly a title problem —
// answering it with "bad model" points carl at the control that is fine.
//
// "missing title" is the existing wording pinned by threads_test.go at HEAD;
// a blank-but-present title takes the same path.
func TestEdgeBranchMissingTitleBeatsBadModel(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-order", Repo: "monolith"})

	cases := []struct {
		name  string
		title string
	}{
		{"absent title", ""},
		{"blank title", "   "},
		{"newline-only title", "\n\t "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := edgePostThreads(t, threadsReq{
				Slug: "edge-order", Op: "branch", Title: c.title, Text: "why?", Model: "gpt-5",
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
			if got := decodeErr(t, rec); got != "missing title" {
				t.Fatalf("error = %q, want %q — the model check ran before the title check", got, "missing title")
			}
		})
	}
}

// TestEdgeBranchUnknownSlugBeatsBadModel pins the other ordering the contract
// states: validation happens after slug resolution, so a request naming a job
// that does not exist is a 404 no matter how bad its model is. A 400 here
// would tell a client the model was the problem when the job was.
func TestEdgeBranchUnknownSlugBeatsBadModel(t *testing.T) {
	useTestStore(t)

	rec := edgePostThreads(t, threadsReq{
		Slug: "edge-nope", Op: "branch", Title: "a title", Text: "why?", Model: "gpt-5",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got == edgeBadModelBody {
		t.Fatal("unknown slug answered with the bad-model refusal")
	}
}

// TestEdgeBranchAllowsEmptyAndValidModels is the negative space around the
// refusal: `model` empty is legacy behaviour and explicitly allowed, and each
// of the four aliases is valid. None of these can be driven to SUCCESS in a
// unit test — the success path opens a real tmux window — so what is asserted
// is precisely what is knowable: the request got PAST the model gate. An
// over-strict validator (rejecting "", rejecting an alias it spelled wrong in
// its own map) would surface here as the exact 400 body and nothing else.
func TestEdgeBranchAllowsEmptyAndValidModels(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-goodmodel", Repo: "monolith"})

	for _, model := range []string{"", "haiku", "sonnet", "opus", "fable"} {
		name := model
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			rec := edgePostThreads(t, threadsReq{
				Slug: "edge-goodmodel", Op: "branch", Title: "why is it blocked",
				Text: "explain the block", Model: model,
			})
			// the raw body, not decodeErr: a branch that somehow SUCCEEDED
			// answers with a mixed-type object decodeErr cannot unmarshal,
			// and a success is a pass here, not a decode failure.
			body := strings.TrimSpace(rec.Body.String())
			if body == edgeBadModelBody {
				t.Fatalf("model %q was refused as invalid: %s", model, body)
			}
			if strings.Contains(body, "bad model") {
				t.Fatalf("model %q was refused as invalid: %s", model, body)
			}
		})
	}
}

// TestEdgeNonBranchOpsIgnoreTheModelField pins "Read only by op:\"branch\";
// other ops ignore it". The board's client sends one request shape from one
// place, so a sticky picker value rides along on every op — and a kill or a
// send that started 400ing on a model it has no use for would break verbs that
// have nothing to do with this change.
//
// Each case asserts the response the op gives WITHOUT a model (the wordings
// are pinned by threads_test.go at HEAD), so "ignored" means literally
// unchanged rather than merely "not a model error".
func TestEdgeNonBranchOpsIgnoreTheModelField(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-otherops", Repo: "monolith"})

	cases := []struct {
		name     string
		req      threadsReq
		wantCode int
		wantErr  string
	}{
		{"rename an unknown tid", threadsReq{Op: "rename", TID: "t7", Title: "x"}, http.StatusNotFound, "unknown thread"},
		{"kill without tid", threadsReq{Op: "kill"}, http.StatusBadRequest, "missing tid"},
		{"revive without tid", threadsReq{Op: "revive"}, http.StatusBadRequest, "missing tid"},
		{"rename without title", threadsReq{Op: "rename", TID: "t1"}, http.StatusBadRequest, "missing title"},
		{"send without text", threadsReq{Op: "send", TID: "t1"}, http.StatusBadRequest, "missing text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, model := range []string{"gpt-5", "Haiku", "claude-opus-5", "haiku"} {
				req := c.req
				req.Slug = "edge-otherops"
				req.Model = model
				rec := edgePostThreads(t, req)
				if rec.Code != c.wantCode {
					t.Fatalf("model %q: status %d, want %d, body %s", model, rec.Code, c.wantCode, rec.Body.String())
				}
				if got := decodeErr(t, rec); got != c.wantErr {
					t.Fatalf("model %q: error = %q, want %q", model, got, c.wantErr)
				}
			}
		})
	}
}

// TestEdgeWebThreadsFromOmitsAnEmptyModel is the wire's silence for a legacy
// row, asserted on the converter directly (it is the seam the contract names)
// AND through its JSON encoding. `omitempty` is the whole obligation: an
// always-present `"model":""` would make every client that renders a pill on
// truthiness draw an empty chip on every pre-v22 thread, and the iOS mirror
// declares the field OPTIONAL precisely so the key can be absent.
func TestEdgeWebThreadsFromOmitsAnEmptyModel(t *testing.T) {
	got := webThreadsFrom([]store.Thread{
		{TID: "t1", Title: "legacy", State: "live", CreatedAt: 1},
		{TID: "t2", Title: "modelled", State: "live", CreatedAt: 2, Model: "haiku"},
	})
	if len(got) != 2 {
		t.Fatalf("webThreadsFrom = %+v, want 2 rows", got)
	}
	if got[0].Model != "" {
		t.Fatalf("legacy row Model = %q, want \"\"", got[0].Model)
	}
	if got[1].Model != "haiku" {
		t.Fatalf("modelled row Model = %q, want haiku", got[1].Model)
	}

	legacy, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "model") {
		t.Fatalf("legacy webThread JSON = %s, want the model key ABSENT", legacy)
	}
	modelled, err := json.Marshal(got[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(modelled), `"model":"haiku"`) {
		t.Fatalf("modelled webThread JSON = %s, want \"model\":\"haiku\"", modelled)
	}
}

// TestEdgeThreadsGetOmitsAnEmptyModel is the same silence end to end, over
// the real GET the board polls: a store row with no model must reach the wire
// with no `model` key at all. The raw body is inspected rather than a decoded
// struct, because decoding into webThread cannot tell an absent key from a
// present empty one — which is exactly the distinction under test.
func TestEdgeThreadsGetOmitsAnEmptyModel(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-wire", Repo: "monolith"})
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", Title: "legacy", CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}

	rec := edgeGetThreads(t, "slug=edge-wire")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "model") {
		t.Fatalf("GET /api/threads body = %s, want no model key for a legacy row", body)
	}

	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t2", Title: "modelled", CreatedAt: 200, Model: "fable"}); err != nil {
		t.Fatal(err)
	}
	rec = edgeGetThreads(t, "slug=edge-wire")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"model":"fable"`) {
		t.Fatalf("GET /api/threads body = %s, want \"model\":\"fable\" on the second row", body)
	}
}
