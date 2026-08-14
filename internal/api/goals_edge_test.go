package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// goals_edge_test.go — the EDGE dimension of 50-contract.md's Slice S1 API
// obligations: what /api/goals and /api/job/goal REFUSE, and the two shapes a
// client must never receive (a null array, a 403 with the wrong body).
// Written blind from the contract text, with no sight of internal/api/goals.go.
//
// The sibling blind author owns the round-trips and the wire-item shape. What
// lives here is the other half — the 400s, the 404s, the mirror gate, and the
// empty state, which is the state every fresh database is in and therefore the
// one a client meets first.
//
// Package-level identifiers are prefixed `edgeGoal`: this package's test
// namespace is shared with a second blind author writing in the same round.

const edgeGoalNow int64 = 1_760_000_000_000

// edgeGoalPost drives POST /api/goals with a JSON-encodable body. The body is
// built from a map rather than the handler's own request struct on purpose:
// the wire shape is what the contract pins, and a test that reused the
// implementation's struct would agree with it by construction.
func edgeGoalPost(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	return edgeGoalPostRaw(t, mustJSON(t, body))
}

func edgeGoalPostRaw(t *testing.T, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/goals", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	return rec
}

func edgeGoalGet(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/goals"+query, nil)
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	return rec
}

func edgeGoalJobPost(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	jobGoalHandler(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// bad bodies
// ---------------------------------------------------------------------------

// TestEdgeGoalsBadJSONIs400 pins the decode failure at the API's own boundary:
// a body that is not JSON never reaches the store, and the answer is a 400
// rather than a 500 (which would read as "kraken is broken" for a client bug).
func TestEdgeGoalsBadJSONIs400(t *testing.T) {
	useTestStore(t)
	for _, raw := range []string{
		``,
		`{`,
		`{"op":`,
		`not json at all`,
		`[{"op":"add"}]`, // an array where the wire shape is one object
		`"add"`,
	} {
		rec := edgeGoalPostRaw(t, []byte(raw))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST body %q: status %d, want 400 (body %s)", raw, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeGoalsUnknownOpIs400 covers the op the dispatch does not know. The
// three ops are a closed set; a fourth is a client that has drifted, and
// answering 200 to it would report success for a write that never happened.
func TestEdgeGoalsUnknownOpIs400(t *testing.T) {
	useTestStore(t)
	for _, op := range []string{"", "delete", "ADD", "upsert"} {
		rec := edgeGoalPost(t, map[string]any{"op": op, "id": 1})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("op %q: status %d, want 400 (body %s)", op, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeGoalsStoreValidationErrorsCarryTheStoreMessage is the contract's
// "400 with the store's message" clause, asserted as equality against the
// message the store method itself produces for the same input — the only
// reading of "the store's message" that a rewording of the store's errors
// cannot silently drift away from.
func TestEdgeGoalsStoreValidationErrorsCarryTheStoreMessage(t *testing.T) {
	st := useTestStore(t)
	_, curDay := store.GoalPeriods(time.Now())

	for _, c := range []struct {
		name string
		add  store.GoalAdd
		wire map[string]any
	}{
		{
			name: "malformed period",
			add:  store.GoalAdd{Scope: "day", Period: "13-08-2026", Text: "ship it"},
			wire: map[string]any{"scope": "day", "period": "13-08-2026", "text": "ship it"},
		},
		{
			name: "unknown scope",
			add:  store.GoalAdd{Scope: "month", Period: curDay, Text: "ship it"},
			wire: map[string]any{"scope": "month", "period": curDay, "text": "ship it"},
		},
		{
			name: "whitespace-only text",
			add:  store.GoalAdd{Scope: "day", Period: curDay, Text: "   "},
			wire: map[string]any{"scope": "day", "period": curDay, "text": "   "},
		},
		{
			name: "week period that is not a monday",
			add:  store.GoalAdd{Scope: "week", Period: "2026-08-12", Text: "ship it"},
			wire: map[string]any{"scope": "week", "period": "2026-08-12", "text": "ship it"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, storeErr := st.AddGoal(c.add, edgeGoalNow)
			if storeErr == nil {
				t.Fatalf("fixture is wrong: the store accepted %+v", c.add)
			}
			rec := edgeGoalPost(t, map[string]any{"op": "add", "goal": c.wire})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := decodeErr(t, rec); got != storeErr.Error() {
				t.Errorf("400 body error = %q, want the store's own message %q", got, storeErr.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// unknown addressees
// ---------------------------------------------------------------------------

func TestEdgeGoalsUnknownSlugIs404(t *testing.T) {
	useTestStore(t)
	rec := edgeGoalGet(t, "?slug=no-such-job")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET ?slug=no-such-job: status %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestEdgeGoalsNoSlugIsNot404 is the other half of the same rule: the slug is
// OPTIONAL, so its absence is the ordinary board read, not a missing job.
func TestEdgeGoalsNoSlugIsNot404(t *testing.T) {
	useTestStore(t)
	if rec := edgeGoalGet(t, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET with no slug: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// An explicitly EMPTY slug is the same request a client that always sends
	// the parameter produces; it addresses no job, so it must not 404 either.
	if rec := edgeGoalGet(t, "?slug="); rec.Code != http.StatusOK {
		t.Fatalf("GET ?slug=: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestEdgeGoalJobUnknownSlugIs404(t *testing.T) {
	st := useTestStore(t)
	_, curDay := store.GoalPeriods(time.Now())
	g, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: curDay, Text: "a goal"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	for _, body := range []map[string]any{
		{"slug": "no-such-job", "goal_id": g.ID},
		{"slug": "no-such-job", "goal_id": 0}, // the unlink form addresses the same missing job
		{"slug": "", "goal_id": g.ID},
	} {
		rec := edgeGoalJobPost(t, body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /api/job/goal %+v: status %d, want 404 (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeGoalJobUnknownGoalIs400 separates the two addressees deliberately: an
// unknown SLUG is 404 (the job does not exist) while an unknown GOAL is 400
// (the job exists; the request is bad). Collapsing them would make an editor
// picker holding a just-deleted goal indistinguishable from a dead job.
func TestEdgeGoalJobUnknownGoalIs400(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "goal-link-400", Repo: "monolith"})

	for _, goalID := range []any{987654, -1} {
		rec := edgeGoalJobPost(t, map[string]any{"slug": "goal-link-400", "goal_id": goalID})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("goal_id %v: status %d, want 400 (body %s)", goalID, rec.Code, rec.Body.String())
		}
	}
	// And 0 — the unlink — is not "an unknown goal": it is the documented clear
	// and must succeed against a job with no link at all.
	rec := edgeGoalJobPost(t, map[string]any{"slug": "goal-link-400", "goal_id": 0})
	if rec.Code != http.StatusOK {
		t.Errorf("goal_id 0 (unlink): status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// mirror mode
// ---------------------------------------------------------------------------

// TestEdgeGoalsMirrorModeGatesWritesButNotReads is the whole reason the gate is
// INLINE rather than a route wrapper: a mirror board is read-only, and taking
// GET /api/goals down with the POST would leave that board unable to render
// the panel at all.
//
// Driven through the real mux (api.Register), the same wiring web.Serve uses,
// so a gate applied at registration and one applied inside the handler are both
// covered — and a route wrapper that swallowed the GET fails here.
func TestEdgeGoalsMirrorModeGatesWritesButNotReads(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	_, curDay := store.GoalPeriods(time.Now())
	for _, c := range []struct {
		path string
		body any
	}{
		{"/api/goals", map[string]any{"op": "add", "goal": map[string]any{"scope": "day", "period": curDay, "text": "no"}}},
		{"/api/goals", map[string]any{"op": "update", "id": 1, "done": true}},
		{"/api/goals", map[string]any{"op": "remove", "id": 1}},
		{"/api/job/goal", map[string]any{"slug": "anything", "goal_id": 0}},
	} {
		rec := postJSON(t, mux, c.path, mustJSON(t, c.body))
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s %+v: status %d, want 403 (body %s)", c.path, c.body, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Errorf("POST %s: body = %q, want exactly %q", c.path, got, "disabled in mirror mode")
		}
	}

	// The read stays open, through the same mux, in the same mirror mode.
	req := httptest.NewRequest(http.MethodGet, "/api/goals", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/goals under mirror mode: status %d, want 200 — the gate must be inline, not a route wrapper (body %s)",
			rec.Code, rec.Body.String())
	}
	edgeGoalAssertArraysPresent(t, rec)
}

// TestEdgeGoalsMirrorGateRunsBeforeTheBodyDecode matches the package norm
// (mirror_test.go): a refused route answers the same 403 whatever the body is,
// so a mirror board cannot learn anything about the store by malforming a
// request.
func TestEdgeGoalsMirrorGateRunsBeforeTheBodyDecode(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range []string{"/api/goals", "/api/job/goal"} {
		rec := postJSON(t, mux, path, []byte(`{not json`))
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s with a malformed body: status %d, want 403", path, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Errorf("POST %s: body = %q, want exactly %q", path, got, "disabled in mirror mode")
		}
	}
}

// ---------------------------------------------------------------------------
// the tolerant write and the empty state
// ---------------------------------------------------------------------------

// TestEdgeGoalsRemoveAbsentIDIsStill200 mirrors DeleteGoal's no-op stance at
// the wire: the caller wanted the goal gone, and it is gone. A second delete
// from a double-click, or one from a panel whose payload predates someone
// else's delete, must not surface an error.
func TestEdgeGoalsRemoveAbsentIDIsStill200(t *testing.T) {
	useTestStore(t)
	for _, id := range []any{987654, 0} {
		rec := edgeGoalPost(t, map[string]any{"op": "remove", "id": id})
		if rec.Code != http.StatusOK {
			t.Errorf("remove id %v: status %d, want 200 (body %s)", id, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeGoalsEmptyStateArraysAreNeverNull is the shape every fresh database
// serves and therefore the first thing a client ever decodes. `null` and `[]`
// are different values to a strict decoder (Swift's especially) and to any
// client that calls .map on the result — the contract says both arrays are
// always present, and this is that clause asserted against the raw bytes
// rather than against a Go decode that would smooth them into the same nil
// slice.
func TestEdgeGoalsEmptyStateArraysAreNeverNull(t *testing.T) {
	useTestStore(t)
	rec := edgeGoalGet(t, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/goals: status %d (body %s)", rec.Code, rec.Body.String())
	}
	edgeGoalAssertArraysPresent(t, rec)

	// The same guarantee with a slug in play: an unlinked job adds no key and
	// must not turn either array into null on the way.
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "goal-empty-state", Repo: "monolith"})
	rec = edgeGoalGet(t, "?slug=goal-empty-state")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?slug=goal-empty-state: status %d (body %s)", rec.Code, rec.Body.String())
	}
	edgeGoalAssertArraysPresent(t, rec)
	// job_goal is ABSENT (not null) for a job that links nothing.
	var withSlug map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &withSlug); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if _, ok := withSlug["job_goal"]; ok {
		t.Errorf("job_goal is present for a job with no linked goal: %s", rec.Body.String())
	}
}

// edgeGoalAssertArraysPresent checks the raw JSON: both keys exist and both
// are `[]`, never `null` and never missing.
func edgeGoalAssertArraysPresent(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Goals map[string]json.RawMessage `json:"goals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if body.Goals == nil {
		t.Fatalf(`"goals" key missing or null: %s`, rec.Body.String())
	}
	for _, key := range []string{"week", "day"} {
		raw, ok := body.Goals[key]
		if !ok {
			t.Errorf(`goals.%s missing — both arrays are always present: %s`, key, rec.Body.String())
			continue
		}
		if got := strings.TrimSpace(string(raw)); got != "[]" {
			t.Errorf("goals.%s = %s, want [] on an empty period (never null)", key, got)
		}
	}
}
