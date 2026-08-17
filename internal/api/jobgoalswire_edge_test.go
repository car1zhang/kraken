package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobgoalswire_edge_test.go — the EDGE dimension of 50-contract.md's Slice S1
// WIRE obligations for many-to-many goal links: what POST /api/job/goal
// REFUSES, what it accepts as a no-op, and the shapes a client must never
// receive from GET /api/goals?slug=.
//
// Written blind from the frozen contract. internal/api/{goals,board}.go were
// never read in their working-tree state; the harness (useTestStore,
// mustCreateStoreJob, mustJSON, decodeErr, postJSON, SetMirrorMode, Register)
// is pre-existing package state read at the base commit.
//
// The sibling blind author (athena-logic) owns the round trips and the served
// item shape — that `goals` is present exactly when links exist, in position
// order, elements complete. What lives here is the other half: the 400s that
// must NAME the valid ops, the 404/400 split between an unknown JOB and an
// unknown GOAL, the clear that ignores `op` entirely, the two no-ops, the
// mirror gate, and the ABSENT keys.
//
// Package-level identifiers are prefixed `edgeJGW` — this package's test
// namespace already carries an `edgeGoal*` family from the prior round and a
// second blind author is writing beside this file right now.

const edgeJGWNow int64 = 1_760_000_000_000

// edgeJGWJobGoal drives POST /api/job/goal with a hand-built body. A map, not
// the handler's own request struct: the wire shape is what the contract pins,
// and a test reusing the implementation's struct would agree with it by
// construction.
func edgeJGWJobGoal(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	return edgeJGWJobGoalRaw(t, mustJSON(t, body))
}

func edgeJGWJobGoalRaw(t *testing.T, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	jobGoalHandler(rec, req)
	return rec
}

// edgeJGWLink posts one op and fails the test on anything but 200 — the
// fixture-building form, for cases whose subject is a LATER request.
func edgeJGWLink(t *testing.T, slug string, goalID int64, op string) {
	t.Helper()
	body := map[string]any{"slug": slug, "goal_id": goalID}
	if op != "" {
		body["op"] = op
	}
	rec := edgeJGWJobGoal(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/goal %+v: status %d, want 200 (body %s)", body, rec.Code, rec.Body.String())
	}
}

// edgeJGWGet reads GET /api/goals?slug= and returns the decoded body as raw
// messages: the contract pins key PRESENCE and ABSENCE, which a typed struct
// with omitempty would silently smooth over.
func edgeJGWGet(t *testing.T, slug string) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/goals?slug="+slug, nil)
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/goals?slug=%s: status %d (body %s)", slug, rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return body
}

// edgeJGWLinkedIDs pulls the id list out of `job_goals`, failing when the key
// is absent. Its companion edgeJGWAssertUnlinked covers the other side.
func edgeJGWLinkedIDs(t *testing.T, slug string) []int64 {
	t.Helper()
	body := edgeJGWGet(t, slug)
	raw, ok := body["job_goals"]
	if !ok {
		t.Fatalf("GET ?slug=%s carries no job_goals key: %v", slug, body)
	}
	var items []struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode job_goals %s: %v", raw, err)
	}
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// edgeJGWAssertUnlinked pins the ONE spelling of "this job links nothing":
// both keys ABSENT. Not null, not `[]`, not a zero object — the client's whole
// test is key presence, and an empty array would read as "answered: none" to a
// reader that only asks whether the key is there.
func edgeJGWAssertUnlinked(t *testing.T, slug string) {
	t.Helper()
	body := edgeJGWGet(t, slug)
	for _, key := range []string{"job_goals", "job_goal"} {
		if raw, ok := body[key]; ok {
			t.Errorf("GET ?slug=%s carries %s = %s for a job with no links; the key must be ABSENT",
				slug, key, raw)
		}
	}
}

func edgeJGWSameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// edgeJGWSeed opens a store, creates one live job and two current-period
// goals, and returns them.
func edgeJGWSeed(t *testing.T, slug string) (*store.Store, *store.Goal, *store.Goal) {
	t.Helper()
	st := useTestStore(t)
	week, day := store.GoalPeriods(time.Now())
	a, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "ship the goal filter"}, edgeJGWNow)
	if err != nil {
		t.Fatalf("seed week goal: %v", err)
	}
	b, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: "land the migration"}, edgeJGWNow)
	if err != nil {
		t.Fatalf("seed day goal: %v", err)
	}
	mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	return st, a, b
}

// ---------------------------------------------------------------------------
// goal_id 0 — the clear that outranks `op`
// ---------------------------------------------------------------------------

// TestEdgeJGWZeroGoalIDClearsWhateverTheOpSays is the legacy shape held open
// on purpose: `goal_id: 0` has always meant "unlink", the picker's `—` row
// still posts it with no op, and a shipped client sending `op` alongside a 0
// must not be answered with a 400 or, worse, an ADD of goal zero. The op is
// read only when there is an id to act on.
func TestEdgeJGWZeroGoalIDClearsWhateverTheOpSays(t *testing.T) {
	_, a, b := edgeJGWSeed(t, "jgw-clear")

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"no op at all (the picker's — row)", map[string]any{"slug": "jgw-clear", "goal_id": 0}},
		{"op add", map[string]any{"slug": "jgw-clear", "goal_id": 0, "op": "add"}},
		{"op remove", map[string]any{"slug": "jgw-clear", "goal_id": 0, "op": "remove"}},
		{"an empty op", map[string]any{"slug": "jgw-clear", "goal_id": 0, "op": ""}},
		{"an op nobody recognises", map[string]any{"slug": "jgw-clear", "goal_id": 0, "op": "obliterate"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			edgeJGWLink(t, "jgw-clear", a.ID, "add")
			edgeJGWLink(t, "jgw-clear", b.ID, "add")
			if got := edgeJGWLinkedIDs(t, "jgw-clear"); len(got) != 2 {
				t.Fatalf("fixture: links = %v, want two before the clear", got)
			}

			rec := edgeJGWJobGoal(t, c.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST %+v: status %d, want 200 (body %s)", c.body, rec.Code, rec.Body.String())
			}
			edgeJGWAssertUnlinked(t, "jgw-clear")
		})
	}

	// And a clear against a job that already links nothing is the same
	// accepted no-op — a double-clicked `—` row is not a failure.
	rec := edgeJGWJobGoal(t, map[string]any{"slug": "jgw-clear", "goal_id": 0})
	if rec.Code != http.StatusOK {
		t.Errorf("a second clear: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// a positive goal_id with no usable op
// ---------------------------------------------------------------------------

// TestEdgeJGWPositiveGoalIDWithoutAUsableOpIs400NamingTheOps is the refusal
// that keeps a drifted client from being GUESSED at. With two verbs sharing
// one route, defaulting an absent op to either one is a write carl did not ask
// for: defaulting to add makes an unlink silently link, defaulting to remove
// makes a link silently unlink. The message has to name both valid ops,
// because the client that got here cannot see the contract.
func TestEdgeJGWPositiveGoalIDWithoutAUsableOpIs400NamingTheOps(t *testing.T) {
	_, a, _ := edgeJGWSeed(t, "jgw-badop")

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"no op key at all", map[string]any{"slug": "jgw-badop", "goal_id": a.ID}},
		{"an empty op", map[string]any{"slug": "jgw-badop", "goal_id": a.ID, "op": ""}},
		{"whitespace", map[string]any{"slug": "jgw-badop", "goal_id": a.ID, "op": " "}},
		{"the wrong case", map[string]any{"slug": "jgw-badop", "goal_id": a.ID, "op": "ADD"}},
		{"a near miss", map[string]any{"slug": "jgw-badop", "goal_id": a.ID, "op": "adds"}},
		{"a verb from another route", map[string]any{"slug": "jgw-badop", "goal_id": a.ID, "op": "update"}},
		{"the clear spelled as an op", map[string]any{"slug": "jgw-badop", "goal_id": a.ID, "op": "clear"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeJGWJobGoal(t, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST %+v: status %d, want 400 (body %s)", c.body, rec.Code, rec.Body.String())
			}
			msg := strings.ToLower(decodeErr(t, rec))
			for _, op := range []string{"add", "remove"} {
				if !strings.Contains(msg, op) {
					t.Errorf("400 body %q does not name the valid op %q", msg, op)
				}
			}
		})
	}

	// Not one of those refusals may have written a link on its way out.
	edgeJGWAssertUnlinked(t, "jgw-badop")
}

// ---------------------------------------------------------------------------
// unknown addressees: the 404/400 split
// ---------------------------------------------------------------------------

// TestEdgeJGWUnknownSlugIs404ForEveryShape: an unknown JOB is 404 on every
// form of the request, the clear included — "unlink a job that does not exist"
// is still a caller addressing nothing.
func TestEdgeJGWUnknownSlugIs404ForEveryShape(t *testing.T) {
	_, a, _ := edgeJGWSeed(t, "jgw-404-control")

	for _, body := range []map[string]any{
		{"slug": "no-such-job", "goal_id": a.ID, "op": "add"},
		{"slug": "no-such-job", "goal_id": a.ID, "op": "remove"},
		{"slug": "no-such-job", "goal_id": 0},
		{"slug": "", "goal_id": a.ID, "op": "add"},
		{"goal_id": a.ID, "op": "add"}, // no slug key at all
	} {
		rec := edgeJGWJobGoal(t, body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %+v: status %d, want 404 (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeJGWUnknownGoalIs400CarryingTheStoreSentence keeps the two addressees
// deliberately apart: an unknown SLUG is 404 (the job does not exist) while an
// unknown GOAL is 400 (the job exists; the request is bad). Collapsing them
// would make an editor picker holding a just-deleted goal indistinguishable
// from a dead job — and the id has to survive into the body, since which id was
// wrong is the only actionable fact in the answer.
func TestEdgeJGWUnknownGoalIs400CarryingTheStoreSentence(t *testing.T) {
	st, a, _ := edgeJGWSeed(t, "jgw-badgoal")
	edgeJGWLink(t, "jgw-badgoal", a.ID, "add")

	for _, goalID := range []int64{987654, a.ID + 9999} {
		rec := edgeJGWJobGoal(t, map[string]any{"slug": "jgw-badgoal", "goal_id": goalID, "op": "add"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("goal_id %d: status %d, want 400 (body %s)", goalID, rec.Code, rec.Body.String())
		}
		msg := decodeErr(t, rec)
		if strings.TrimSpace(msg) == "" {
			t.Errorf("goal_id %d: 400 body carries no message", goalID)
		}
		if !strings.Contains(msg, strconv.FormatInt(goalID, 10)) {
			t.Errorf("goal_id %d: 400 body %q does not name the goal id", goalID, msg)
		}
	}

	// A goal that existed and was deleted between the picker's render and the
	// pick is the way this actually happens in production — same 400.
	doomed, err := st.AddGoal(store.GoalAdd{
		Scope: "day", Period: edgeJGWDayPeriod(t), Text: "about to be deleted"}, edgeJGWNow)
	if err != nil {
		t.Fatalf("seed doomed goal: %v", err)
	}
	if err := st.DeleteGoal(doomed.ID); err != nil {
		t.Fatalf("DeleteGoal: %v", err)
	}
	rec := edgeJGWJobGoal(t, map[string]any{"slug": "jgw-badgoal", "goal_id": doomed.ID, "op": "add"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a just-deleted goal: status %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	// and none of it disturbed the link the job really had
	if got := edgeJGWLinkedIDs(t, "jgw-badgoal"); !edgeJGWSameIDs(got, []int64{a.ID}) {
		t.Errorf("links = %v after the refusals, want just %v", got, []int64{a.ID})
	}
}

// edgeJGWDayPeriod is today's local period key — a one-liner rather than
// another resolution of the pair every fixture above already builds.
func edgeJGWDayPeriod(t *testing.T) string {
	t.Helper()
	_, day := store.GoalPeriods(time.Now())
	return day
}

// ---------------------------------------------------------------------------
// the two no-ops, through the route
// ---------------------------------------------------------------------------

// TestEdgeJGWAddOfAnExistingLinkIs200AndPreservesOrder is the idempotence the
// per-link ops exist to buy, asserted where two surfaces actually race: the
// editor form and the steer head can both hold a stale tick, and a re-add that
// answered 400 would paint an error over a state that is exactly what the
// caller wanted — while a re-add that MOVED the link would silently reorder the
// card's chips.
func TestEdgeJGWAddOfAnExistingLinkIs200AndPreservesOrder(t *testing.T) {
	_, a, b := edgeJGWSeed(t, "jgw-readd")
	edgeJGWLink(t, "jgw-readd", a.ID, "add")
	edgeJGWLink(t, "jgw-readd", b.ID, "add")
	want := []int64{a.ID, b.ID}

	for i := 0; i < 3; i++ {
		edgeJGWLink(t, "jgw-readd", a.ID, "add")
		edgeJGWLink(t, "jgw-readd", b.ID, "add")
		if got := edgeJGWLinkedIDs(t, "jgw-readd"); !edgeJGWSameIDs(got, want) {
			t.Fatalf("round %d: links = %v, want %v — a re-add is a position-preserving no-op", i, got, want)
		}
	}
}

// TestEdgeJGWRemoveOfAnAbsentLinkIs200AndChangesNothing: same stance for the
// other direction, plus the shape that reaches it most often — a second click
// on a row the first click already unticked.
func TestEdgeJGWRemoveOfAnAbsentLinkIs200AndChangesNothing(t *testing.T) {
	_, a, b := edgeJGWSeed(t, "jgw-remove-absent")
	edgeJGWLink(t, "jgw-remove-absent", a.ID, "add")

	// b was never linked
	edgeJGWLink(t, "jgw-remove-absent", b.ID, "remove")
	if got := edgeJGWLinkedIDs(t, "jgw-remove-absent"); !edgeJGWSameIDs(got, []int64{a.ID}) {
		t.Fatalf("links = %v after removing a link that never existed, want %v", got, []int64{a.ID})
	}
	// and a link removed twice
	edgeJGWLink(t, "jgw-remove-absent", a.ID, "remove")
	edgeJGWLink(t, "jgw-remove-absent", a.ID, "remove")
	edgeJGWAssertUnlinked(t, "jgw-remove-absent")
}

// TestEdgeJGWRemoveAffectsExactlyOneLink is the per-link op's whole point
// against a full replace: unlinking one goal leaves the others where they are,
// so two surfaces each landing their own change cannot wipe the other's.
func TestEdgeJGWRemoveAffectsExactlyOneLink(t *testing.T) {
	st, a, b := edgeJGWSeed(t, "jgw-remove-one")
	week, _ := store.GoalPeriods(time.Now())
	c, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "a third goal"}, edgeJGWNow)
	if err != nil {
		t.Fatalf("seed third goal: %v", err)
	}
	for _, g := range []*store.Goal{a, b, c} {
		edgeJGWLink(t, "jgw-remove-one", g.ID, "add")
	}

	edgeJGWLink(t, "jgw-remove-one", b.ID, "remove")
	if got := edgeJGWLinkedIDs(t, "jgw-remove-one"); !edgeJGWSameIDs(got, []int64{a.ID, c.ID}) {
		t.Errorf("links = %v after removing the middle one, want %v", got, []int64{a.ID, c.ID})
	}
}

// ---------------------------------------------------------------------------
// the retained singular key
// ---------------------------------------------------------------------------

// TestEdgeJGWJobGoalIsAlwaysTheHeadOfJobGoals pins the primary against DRIFT
// rather than against a single happy value: it is asserted after an add, after
// a second add, and after the HEAD itself is removed — the one transition
// where a cached primary and the array's first element part company, and the
// one every pre-multi client (iOS, `kraken goals get`) still reads.
func TestEdgeJGWJobGoalIsAlwaysTheHeadOfJobGoals(t *testing.T) {
	_, a, b := edgeJGWSeed(t, "jgw-head")

	assertHead := func(what string) {
		t.Helper()
		body := edgeJGWGet(t, "jgw-head")
		arr, hasArr := body["job_goals"]
		single, hasSingle := body["job_goal"]
		if hasArr != hasSingle {
			t.Fatalf("%s: job_goals present %v but job_goal present %v — they appear and vanish together",
				what, hasArr, hasSingle)
		}
		if !hasArr {
			return
		}
		var items []json.RawMessage
		if err := json.Unmarshal(arr, &items); err != nil {
			t.Fatalf("%s: decode job_goals %s: %v", what, arr, err)
		}
		if len(items) == 0 {
			t.Fatalf("%s: job_goals is present but empty (%s) — absence is the only no-links spelling", what, arr)
		}
		var head, primary map[string]any
		if err := json.Unmarshal(items[0], &head); err != nil {
			t.Fatalf("%s: decode head: %v", what, err)
		}
		if err := json.Unmarshal(single, &primary); err != nil {
			t.Fatalf("%s: decode job_goal %s: %v", what, single, err)
		}
		if !reflect.DeepEqual(head, primary) {
			t.Errorf("%s: job_goal = %v but job_goals[0] = %v — the primary IS the array's head", what, primary, head)
		}
	}

	edgeJGWLink(t, "jgw-head", a.ID, "add")
	assertHead("after the first link")
	edgeJGWLink(t, "jgw-head", b.ID, "add")
	assertHead("after a second link")

	edgeJGWLink(t, "jgw-head", a.ID, "remove")
	assertHead("after removing the head")
	if got := edgeJGWLinkedIDs(t, "jgw-head"); !edgeJGWSameIDs(got, []int64{b.ID}) {
		t.Fatalf("links = %v after removing the head, want %v", got, []int64{b.ID})
	}

	edgeJGWLink(t, "jgw-head", b.ID, "remove")
	assertHead("after removing the last link")
	edgeJGWAssertUnlinked(t, "jgw-head")
}

// TestEdgeJGWUnlinkedJobOmitsBothKeys is the state every fresh job is in and
// therefore the one a client meets first.
func TestEdgeJGWUnlinkedJobOmitsBothKeys(t *testing.T) {
	edgeJGWSeed(t, "jgw-unlinked")
	edgeJGWAssertUnlinked(t, "jgw-unlinked")
}

// ---------------------------------------------------------------------------
// bodies, methods, and the mirror gate
// ---------------------------------------------------------------------------

func TestEdgeJGWBadJSONIs400(t *testing.T) {
	edgeJGWSeed(t, "jgw-badjson")
	for _, raw := range []string{
		``,
		`{`,
		`{"slug":`,
		`not json at all`,
		`[{"slug":"jgw-badjson","goal_id":1,"op":"add"}]`,
		`"add"`,
	} {
		rec := edgeJGWJobGoalRaw(t, []byte(raw))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST body %q: status %d, want 400 (body %s)", raw, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeJGWNonPostIsMethodNotAllowed: the link rides back on the next
// /api/board poll, so this route has no GET to keep reachable — and answering
// one would be a second, un-cached read of state the board already carries.
func TestEdgeJGWNonPostIsMethodNotAllowed(t *testing.T) {
	edgeJGWSeed(t, "jgw-method")
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/job/goal", nil)
		rec := httptest.NewRecorder()
		jobGoalHandler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/job/goal: status %d, want 405 (body %s)", method, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeJGWMirrorModeGatesEveryOpButNotTheRead: a mirror board is read-only,
// full stop — and the gate has to run BEFORE the body decode, or a mirror could
// learn which slugs and goal ids exist by malforming requests at it. The GET
// stays open through the same mux in the same mode, because taking it down
// would leave the mirror unable to render the panel at all.
func TestEdgeJGWMirrorModeGatesEveryOpButNotTheRead(t *testing.T) {
	_, a, _ := edgeJGWSeed(t, "jgw-mirror")
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	for _, body := range []any{
		map[string]any{"slug": "jgw-mirror", "goal_id": a.ID, "op": "add"},
		map[string]any{"slug": "jgw-mirror", "goal_id": a.ID, "op": "remove"},
		map[string]any{"slug": "jgw-mirror", "goal_id": 0},
	} {
		rec := postJSON(t, mux, "/api/job/goal", mustJSON(t, body))
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %+v: status %d, want 403 (body %s)", body, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Errorf("POST %+v: body = %q, want exactly %q", body, got, "disabled in mirror mode")
		}
	}
	// the gate runs before the decode
	rec := postJSON(t, mux, "/api/job/goal", []byte(`{not json`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST with a malformed body: status %d, want 403", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
		t.Errorf("malformed body: %q, want exactly %q", got, "disabled in mirror mode")
	}

	// the read stays reachable, in the same mode, through the same mux
	req := httptest.NewRequest(http.MethodGet, "/api/goals?slug=jgw-mirror", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, req)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/goals under mirror mode: status %d, want 200 (body %s)",
			getRec.Code, getRec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// the watermarks the route must not move
// ---------------------------------------------------------------------------

// TestEdgeJGWNoOpMovesRevOrUpdatedAt is the store-level guarantee asserted
// through the ROUTE, which is where a well-meaning "and stamp updated_at so the
// board refreshes" line actually gets added. Every op is exercised, the two
// no-ops included: a no-op that stamped would reset a card's rendered AGE for a
// write that changed nothing at all.
func TestEdgeJGWNoOpMovesRevOrUpdatedAt(t *testing.T) {
	st := useTestStore(t)
	week, _ := store.GoalPeriods(time.Now())
	a, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "alpha"}, edgeJGWNow)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	b, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "bravo"}, edgeJGWNow)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	// rev/updated_at deliberately in the past, so a rewrite to "now" is loud
	row := store.Job{Slug: "jgw-watermarks", Repo: "kraken", State: "working", Rev: 7,
		CreatedAt: 1_600_000_000_000, UpdatedAt: 1_600_000_000_000}
	if _, err := st.CreateJob(&row); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := st.GetJobBySlug("jgw-watermarks")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}

	for _, step := range []struct {
		name   string
		goalID int64
		op     string
	}{
		{"add", a.ID, "add"},
		{"add a second", b.ID, "add"},
		{"add an existing link", a.ID, "add"},
		{"remove", a.ID, "remove"},
		{"remove an absent link", a.ID, "remove"},
		{"clear", 0, ""},
		{"clear at zero links", 0, ""},
	} {
		edgeJGWLink(t, "jgw-watermarks", step.goalID, step.op)
		after, err := st.GetJobBySlug("jgw-watermarks")
		if err != nil {
			t.Fatalf("%s: GetJobBySlug: %v", step.name, err)
		}
		if after.Rev != before.Rev {
			t.Errorf("%s moved rev %d → %d through the route", step.name, before.Rev, after.Rev)
		}
		if after.UpdatedAt != before.UpdatedAt {
			t.Errorf("%s moved updated_at %d → %d through the route", step.name, before.UpdatedAt, after.UpdatedAt)
		}
	}
}

// ---------------------------------------------------------------------------
// the card's own keys
// ---------------------------------------------------------------------------

// TestEdgeJGWArchivedCardWithNoChipsCarriesNeitherKey is the archived card's
// EMPTY boundary, reachable now that `archivedWebJob`'s signature is frozen.
// The caller resolves the chips out of the board-wide maps, so "this archived
// job links nothing" and "this archived job's goals could not be resolved"
// both arrive here as an empty slice — and both must serve NO key, since the
// archive drawer's cards are exactly where a `"goals":[]` would teach a client
// a third state that means nothing.
//
// The one-element case is included as this test's own boundary control:
// `Goal` must appear the instant `Goals` does, and be its head.
func TestEdgeJGWArchivedCardWithNoChipsCarriesNeitherKey(t *testing.T) {
	st := useTestStore(t)
	row := store.Job{Slug: "jgw-archived", Repo: "kraken", State: "done"}
	id, err := st.CreateJob(&row)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	archived := jobs.LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("LoadArchivedJobs = %d jobs, want 1", len(archived))
	}
	a := archived[0]
	tokens := map[int64]store.JobTokens{a.ID: {JobID: a.ID, In: 1, Out: 2}}

	for _, c := range []struct {
		name  string
		goals []webJobGoal
	}{
		{"a nil slice", nil},
		{"an empty slice — what a caller that always builds one produces", []webJobGoal{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wj := archivedWebJob(a, st, tokens, c.goals)
			if len(wj.Goals) != 0 {
				t.Errorf("archived card carries goals = %+v, want none", wj.Goals)
			}
			if wj.Goal != nil {
				t.Errorf("archived card carries goal = %+v, want no key at all", wj.Goal)
			}
			raw, err := json.Marshal(wj)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			for _, key := range []string{"goals", "goal"} {
				if v, ok := keys[key]; ok {
					t.Errorf("archived card serialized %s = %s; absence is the only no-goals spelling", key, v)
				}
			}
			if wj.Tokens == nil {
				t.Error("archived card dropped tokens — the control for this test just broke")
			}
		})
	}

	t.Run("one chip is the boundary: both keys appear together", func(t *testing.T) {
		head := webJobGoal{ID: 41, Text: "ship the filter", Done: false}
		wj := archivedWebJob(a, st, tokens, []webJobGoal{head})
		if len(wj.Goals) != 1 || wj.Goals[0] != head {
			t.Fatalf("archived card goals = %+v, want the passed slice verbatim", wj.Goals)
		}
		if wj.Goal == nil {
			t.Fatal("archived card carries goals but no goal — the primary IS the array's head")
		}
		if *wj.Goal != head {
			t.Errorf("archived card goal = %+v, want the head %+v", *wj.Goal, head)
		}
	})
}

// TestEdgeJGWWebJobGoalKeysAreOmitemptyPointersOrSlices pins the two card keys
// by their TAGS, which is the only reachable assertion for them: buildWebBoard
// drags in tmux/process-table discovery and cannot be called from a test (the
// same reason jobload_test.go, update_test.go and shipios_test.go all build
// their board keys without it). The tag is where "absence is the only
// no-goals spelling" is actually implemented — drop the omitempty and every
// unlabelled card starts serving `"goals":null`, which is a THIRD state two
// clients would then have to learn.
func TestEdgeJGWWebJobGoalKeysAreOmitemptyPointersOrSlices(t *testing.T) {
	typ := reflect.TypeOf(webJob{})
	for _, c := range []struct {
		field string
		tag   string
		kind  reflect.Kind
	}{
		{"Goals", "goals,omitempty", reflect.Slice},
		{"Goal", "goal,omitempty", reflect.Ptr},
	} {
		f, ok := typ.FieldByName(c.field)
		if !ok {
			t.Errorf("webJob has no %s field", c.field)
			continue
		}
		if got := f.Tag.Get("json"); got != c.tag {
			t.Errorf("webJob.%s json tag = %q, want %q", c.field, got, c.tag)
		}
		if f.Type.Kind() != c.kind {
			t.Errorf("webJob.%s is a %s, want a %s — the empty value must marshal to no key at all",
				c.field, f.Type.Kind(), c.kind)
		}
	}
}
