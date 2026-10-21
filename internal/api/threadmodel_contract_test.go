package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// threadmodel_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's HTTP half: threadsReq.Model's branch-only
// validation, and webThread.Model's presence on the board wire.
//
// Written from the contract text alone. internal/api/threads.go and
// internal/api/board.go were read ONLY at HEAD, for the harness this file
// reuses — useTestStore/mustCreateStoreJob (storemode_test.go,
// jobedit_storemode_test.go), postThreads/decodeErr (threads_test.go,
// feedback_test.go) — and for webThread's existing field set. Nothing from
// the working tree was observed.
//
// Dimension: LOGIC — the stated obligations and their happy paths.
//
// Every package-level identifier declared here is prefixed `contract` — a
// sibling author shares this package's namespace.

// contractBadModelErr is the refusal body the contract states VERBATIM. It
// is a user-facing string on a picker's own error path, so it is pinned as
// bytes rather than by keyword: "bad model" alone would not tell carl which
// four words the server will take.
const contractBadModelErr = "bad model — want haiku, sonnet, opus or fable"

// contractThreadModelAliases is the accept set, spelled out from the
// contract rather than read off core.ThreadModels — deriving the expectation
// from the implementation would make the assertions vacuous.
var contractThreadModelAliases = []string{"haiku", "sonnet", "opus", "fable"}

// ---------------------------------------------------------------------------
// POST /api/threads — branch-op model validation
// ---------------------------------------------------------------------------

// TestContractBranchRefusesAnUnknownModel pins the refusal exactly: status
// 400 and the stated body, byte for byte.
//
// The status matters as much as the text. threadErrStatus maps a
// live-state refusal onto 409 and a store failure onto 500; a bad model is
// neither — it is the client's own request being wrong, which is the one
// thing a 400 says and the only code that tells the board to re-ask rather
// than retry.
func TestContractBranchRefusesAnUnknownModel(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-badmodel", Repo: "monolith"})

	for _, bad := range []string{"gpt-5", "claude-opus-5", "Haiku", "opus-5", " haiku"} {
		rec := postThreads(t, threadsReq{Slug: "th-badmodel", Op: "branch", Title: "why blocked", Text: "explain", Model: bad})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("model %q: status %d, want 400, body %s", bad, rec.Code, rec.Body.String())
		}
		if got := decodeErr(t, rec); got != contractBadModelErr {
			t.Errorf("model %q: error = %q, want %q", bad, got, contractBadModelErr)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"`+contractBadModelErr+`"}` {
			t.Errorf("model %q: body = %s, want exactly {\"error\":%q}", bad, got, contractBadModelErr)
		}
	}
}

// TestContractBranchAcceptsEveryAliasAndReachesBranchThread is the other
// side of the same gate: each of the four aliases must get PAST validation
// and into jobs.BranchThread.
//
// There is no live cell under `go test`, so BranchThread's own first refusal
// ("no live cell to branch") is what comes back — and that is exactly the
// evidence wanted here. It can only be produced by code inside BranchThread,
// so seeing it proves the request was handed on rather than rejected at the
// door, which no assertion on the status code alone could show.
func TestContractBranchAcceptsEveryAliasAndReachesBranchThread(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-goodmodel", Repo: "monolith"})

	// "" rides along: the contract keeps the legacy no-model branch working.
	// headless:false since the ask-thread job: a branch without it opens an
	// ask thread (jobs.AskThread), and BranchThread is now the opt-in pane path.
	paneThread := false
	for _, model := range append([]string{""}, contractThreadModelAliases...) {
		rec := postThreads(t, threadsReq{Slug: "th-goodmodel", Op: "branch", Title: "why blocked", Text: "explain", Model: model, Headless: &paneThread})
		if rec.Code == http.StatusBadRequest {
			t.Errorf("model %q: status 400 (%s) — a valid model must pass validation", model, rec.Body.String())
			continue
		}
		got := decodeErr(t, rec)
		if got == contractBadModelErr {
			t.Errorf("model %q: refused as a bad model", model)
			continue
		}
		if !strings.Contains(got, "no live cell to branch") {
			t.Errorf("model %q: error = %q, want jobs.BranchThread's own dead-cell refusal — the request never reached it", model, got)
		}
	}
}

// TestContractMissingTitleStillWins pins the ORDER: the model check lands
// after the existing title check. A request with neither must hear about the
// title, because that is the field the client can actually fix — reporting a
// bad model to a form whose title box is empty sends carl to the wrong
// control.
func TestContractMissingTitleStillWins(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-order", Repo: "monolith"})

	for _, title := range []string{"", "   "} {
		rec := postThreads(t, threadsReq{Slug: "th-order", Op: "branch", Title: title, Text: "explain", Model: "gpt-5"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("title %q: status %d, want 400, body %s", title, rec.Code, rec.Body.String())
		}
		if got := decodeErr(t, rec); got != "missing title" {
			t.Errorf("title %q with a bad model: error = %q, want %q", title, got, "missing title")
		}
	}
}

// TestContractOtherOpsIgnoreTheModel: `model` is read ONLY by the branch op.
// A rename carrying garbage in that field must be answered on its own terms
// — here an unknown tid, a 404 — rather than 400'd on a field the op does
// not have. The clients send one request shape for every op, so a leftover
// model from a previous branch riding along on a rename must be inert.
func TestContractOtherOpsIgnoreTheModel(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-otherops", Repo: "monolith"})

	ops := []threadsReq{
		{Op: "rename", TID: "t1", Title: "x", Model: "gpt-5"},
		{Op: "kill", TID: "t1", Model: "gpt-5"},
		{Op: "revive", TID: "t1", Model: "gpt-5"},
		{Op: "send", TID: "t1", Text: "hi", Model: "gpt-5"},
	}
	for _, req := range ops {
		req.Slug = "th-otherops"
		rec := postThreads(t, req)
		if got := decodeErr(t, rec); got == contractBadModelErr {
			t.Errorf("op %q was refused for its model — only branch reads that field", req.Op)
			continue
		}
		// every request above carries its op's own required fields, so a 400
		// of any kind here could only be the model check firing outside the
		// branch case
		if rec.Code == http.StatusBadRequest {
			t.Errorf("op %q: status 400 (%s) — nothing about these requests is malformed", req.Op, rec.Body.String())
		}
	}

	// rename is the op whose unknown-tid mapping this package already pins,
	// so it can carry the positive half: the request was dispatched normally
	// and answered on its own terms.
	rec := postThreads(t, threadsReq{Slug: "th-otherops", Op: "rename", TID: "t1", Title: "x", Model: "gpt-5"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("rename with a garbage model: status %d, want 404 (unknown tid), body %s", rec.Code, rec.Body.String())
	}
}

// TestContractUnknownSlugIs404RegardlessOfModel pins the contract's own
// note: model validation happens after slug resolution, so a request for a
// job that does not exist is a 404 even when its model is garbage. A 400
// here would tell a client its model was the problem when the job it named
// is the problem.
func TestContractUnknownSlugIs404RegardlessOfModel(t *testing.T) {
	useTestStore(t)

	rec := postThreads(t, threadsReq{Slug: "nope", Op: "branch", Title: "why", Text: "explain", Model: "gpt-5"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// the board wire — webThread.Model
// ---------------------------------------------------------------------------

// TestContractWebThreadsFromCopiesTheModel: the board poll is where the pill
// comes from, and webThreadsFrom is the one place a store row becomes a
// board row. A dropped field here is invisible in Go (the DTO still
// compiles, the JSON still validates) and shows up only as a pill that never
// renders.
func TestContractWebThreadsFromCopiesTheModel(t *testing.T) {
	rows := []store.Thread{
		{TID: "t1", Title: "haiku thread", State: "live", Model: "haiku"},
		{TID: "t2", Title: "fable thread", State: "closed", Model: "fable"},
		{TID: "t3", Title: "legacy thread", State: "live"},
	}
	got := webThreadsFrom(rows)
	if len(got) != len(rows) {
		t.Fatalf("webThreadsFrom returned %d rows, want %d", len(got), len(rows))
	}
	for i, want := range rows {
		if got[i].Model != want.Model {
			t.Errorf("row %d (%s): Model = %q, want %q", i, want.TID, got[i].Model, want.Model)
		}
	}
	// the rest of the mapping is untouched — a new field must not shuffle the
	// existing ones
	if got[0].TID != "t1" || got[0].Title != "haiku thread" || got[0].State != "live" {
		t.Errorf("existing fields disturbed: %+v", got[0])
	}
}

// TestContractWebThreadModelIsOmittedWhenEmpty pins the wire shape both
// clients read: a legacy thread emits NO model key at all, so `rec.model` is
// undefined rather than "" — the difference between "this thread has no
// model" and "this thread's model is the empty string", which is what the
// pill's render condition turns on.
func TestContractWebThreadModelIsOmittedWhenEmpty(t *testing.T) {
	withModel, err := json.Marshal(webThread{TID: "t1", State: "live", Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(withModel), `"model":"opus"`) {
		t.Errorf("webThread JSON = %s, want a model key", withModel)
	}

	legacy, err := json.Marshal(webThread{TID: "t2", State: "live"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "model") {
		t.Errorf("webThread with no model = %s, want the key absent entirely", legacy)
	}
	// and the key spelling is what the clients (and ios/Kraken/Models.swift)
	// decode
	var decoded map[string]any
	if err := json.Unmarshal(withModel, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["model"] != "opus" {
		t.Errorf("decoded[%q] = %#v, want %q", "model", decoded["model"], "opus")
	}
}

// TestContractApiContractDoesNotBump: every part of this change is additive
// — a new optional key on webThread, a new optional field on the POST body —
// so the wire-shape version stays where it is. Bumping it would tell every
// client its shape is stale and force an update none of them needs.
func TestContractApiContractDoesNotBump(t *testing.T) {
	if apiContract != 7 {
		t.Errorf("apiContract = %d, want 7 (bumped by the fleet gate) — a per-thread model is additive on both shapes", apiContract)
	}
}
