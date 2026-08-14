package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// goals_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's HTTP half: goalsHandler (GET + the three POST
// ops) and jobGoalHandler.
//
// Written from the contract text alone. internal/api/goals.go, register.go's
// new route lines and board.go's goals widening were never read; the harness
// (useTestStore, mustCreateStoreJob, mustJSON) is pre-existing package state
// at the base commit.
//
// Dimension: LOGIC — the stated wire shape and the happy paths. The mirror
// gate's exact 403 body, oversize/bad-JSON handling and the store's refusal
// messages surfacing as 400s are athena-edge's ground and are not probed here.
// The one 404 kept below is GET's unknown-slug arm, because the slug-scoped
// GET's happy path is meaningless without it.
//
// Every package-level identifier declared here is prefixed `contractGoals` —
// athena-edge shares this package's test namespace.

// contractGoalsGet drives GET /api/goals, optionally slug-scoped, and returns
// the recorder. Raw bytes rather than a typed response: the contract pins key
// PRESENCE and ABSENCE ("parent_id omitted when 0", "key absent otherwise"),
// which a struct with omitempty would silently paper over.
func contractGoalsGet(t *testing.T, slug string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/goals"
	if slug != "" {
		url += "?slug=" + slug
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	return rec
}

// contractGoalsGetOK drives GET and decodes the body into loose maps, failing
// on anything but 200.
func contractGoalsGetOK(t *testing.T, slug string) map[string]any {
	t.Helper()
	rec := contractGoalsGet(t, slug)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/goals: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET body %q: %v", rec.Body.String(), err)
	}
	return body
}

// contractGoalsPost drives POST /api/goals with a hand-built body — the wire
// shape is what is under test, so nothing here goes through the handler's own
// request struct.
func contractGoalsPost(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/goals", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	return rec
}

// contractGoalsPostOK drives POST and requires 200 with exactly {"ok":true}.
func contractGoalsPostOK(t *testing.T, body map[string]any) {
	t.Helper()
	rec := contractGoalsPost(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/goals %v: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var ok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode POST body %q: %v", rec.Body.String(), err)
	}
	if ok["ok"] != true {
		t.Fatalf("POST body = %s, want {\"ok\":true}", rec.Body.String())
	}
}

// contractGoalsJobGoal drives POST /api/job/goal.
func contractGoalsJobGoal(t *testing.T, slug string, goalID int64) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, map[string]any{"slug": slug, "goal_id": goalID})
	req := httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	jobGoalHandler(rec, req)
	return rec
}

// contractGoalsLane pulls one lane ("week"/"day") out of a decoded GET body,
// asserting the goals object and both lanes are present and are ARRAYS — the
// "never null" obligation, which is what the panel's `.map` depends on.
func contractGoalsLane(t *testing.T, body map[string]any, lane string) []any {
	t.Helper()
	raw, ok := body["goals"]
	if !ok {
		t.Fatalf("GET body has no \"goals\" key: %v", body)
	}
	goals, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("\"goals\" = %T, want an object with week/day", raw)
	}
	for _, want := range []string{"week", "day"} {
		v, ok := goals[want]
		if !ok {
			t.Fatalf("\"goals\" has no %q key: %v — both arrays are ALWAYS present", want, goals)
		}
		if v == nil {
			t.Fatalf("\"goals\".%s serialized as null; it must be [] when empty", want)
		}
		if _, ok := v.([]any); !ok {
			t.Fatalf("\"goals\".%s = %T, want an array", want, v)
		}
	}
	return goals[lane].([]any)
}

// contractGoalsCurrent is the pair GET serves for.
func contractGoalsCurrent(t *testing.T) (string, string) {
	t.Helper()
	return store.GoalPeriods(time.Now())
}

// contractGoalsItemByText finds one wire item in a lane by its text.
func contractGoalsItemByText(t *testing.T, lane []any, text string) map[string]any {
	t.Helper()
	for _, raw := range lane {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("lane item = %T, want an object", raw)
		}
		if item["text"] == text {
			return item
		}
	}
	t.Fatalf("no lane item with text %q; lane = %v", text, lane)
	return nil
}

// ---------------------------------------------------------------------------
// GET /api/goals — the shape
// ---------------------------------------------------------------------------

// TestContractGoalsGetServesEmptyArraysNeverNull is the shape a FRESH install
// serves, and the one a Go handler gets wrong for free: a nil []Goal marshals
// to `null`, and `null.map` is the crash that takes the whole board down on
// the day carl has typed no goals yet.
func TestContractGoalsGetServesEmptyArraysNeverNull(t *testing.T) {
	useTestStore(t)

	rec := contractGoalsGet(t, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	// the literal bytes, not just the decoded shape: `null` decodes to a nil
	// interface that a lenient reader could still call "absent".
	if raw := rec.Body.Bytes(); !bytes.Contains(raw, []byte(`"week":[]`)) ||
		!bytes.Contains(raw, []byte(`"day":[]`)) {
		t.Errorf("GET body = %s, want both lanes serialized as []", raw)
	}
	body := contractGoalsGetOK(t, "")
	if got := contractGoalsLane(t, body, "week"); len(got) != 0 {
		t.Errorf("week lane = %v, want empty", got)
	}
	if got := contractGoalsLane(t, body, "day"); len(got) != 0 {
		t.Errorf("day lane = %v, want empty", got)
	}
	if _, ok := body["job_goal"]; ok {
		t.Error("job_goal is present on an unscoped GET; it belongs only to ?slug=")
	}
}

// TestContractGoalsGetServesTheCurrentPeriods pins WHICH goals GET answers
// with: the CURRENT local week and day per store.GoalPeriods, not everything
// in the table. A last-week goal leaking into the panel is a goal carl already
// closed the book on.
func TestContractGoalsGetServesTheCurrentPeriods(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, day := contractGoalsCurrent(t)

	if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "this week"}, 1); err != nil {
		t.Fatalf("seed week goal: %v", err)
	}
	if _, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: "today"}, 1); err != nil {
		t.Fatalf("seed day goal: %v", err)
	}
	if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: "2026-01-05", Text: "an old week"}, 1); err != nil {
		t.Fatalf("seed stale week goal: %v", err)
	}
	if _, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: "2026-01-07", Text: "an old day"}, 1); err != nil {
		t.Fatalf("seed stale day goal: %v", err)
	}

	body := contractGoalsGetOK(t, "")
	weekLane := contractGoalsLane(t, body, "week")
	dayLane := contractGoalsLane(t, body, "day")
	if len(weekLane) != 1 {
		t.Fatalf("week lane = %v, want exactly this week's one goal", weekLane)
	}
	if len(dayLane) != 1 {
		t.Fatalf("day lane = %v, want exactly today's one goal", dayLane)
	}
	contractGoalsItemByText(t, weekLane, "this week")
	contractGoalsItemByText(t, dayLane, "today")
}

// TestContractGoalsGetItemCarriesTheWireFields pins the wire item field by
// field. These six keys are what the web panel, the editor picker and the iOS
// drawer all decode; a rename here is a silent blank in three clients.
func TestContractGoalsGetItemCarriesTheWireFields(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, _ := contractGoalsCurrent(t)

	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "wire shape"}, 1)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	done := true
	if err := st.UpdateGoal(g.ID, store.GoalUpdate{Done: &done}, 2); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	item := contractGoalsItemByText(t, contractGoalsLane(t, contractGoalsGetOK(t, ""), "week"), "wire shape")
	if got, ok := item["id"].(float64); !ok || int64(got) != g.ID {
		t.Errorf("id = %v, want %d", item["id"], g.ID)
	}
	if item["scope"] != "week" {
		t.Errorf("scope = %v, want \"week\"", item["scope"])
	}
	if item["period"] != week {
		t.Errorf("period = %v, want %q", item["period"], week)
	}
	if item["done"] != true {
		t.Errorf("done = %v, want true — done goals stay visible for the period", item["done"])
	}
	if _, ok := item["position"]; !ok {
		t.Error("item has no \"position\" key")
	}
}

// TestContractGoalsGetOmitsParentIDWhenZeroAndCarriesItOtherwise: the TS type
// spells parent_id OPTIONAL, and groupGoals reads a missing key as "no
// parent". A zero serialized on the wire would be a parent_id matching no week
// goal — which lands in unassigned anyway, so the bug is invisible until
// something starts treating 0 as an id.
func TestContractGoalsGetOmitsParentIDWhenZeroAndCarriesItOtherwise(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, day := contractGoalsCurrent(t)

	parent, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "the weekly"}, 1)
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: "nested", ParentID: parent.ID}, 1); err != nil {
		t.Fatalf("seed nested: %v", err)
	}
	if _, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: "loose"}, 1); err != nil {
		t.Fatalf("seed loose: %v", err)
	}

	body := contractGoalsGetOK(t, "")
	dayLane := contractGoalsLane(t, body, "day")

	nested := contractGoalsItemByText(t, dayLane, "nested")
	raw, ok := nested["parent_id"]
	if !ok {
		t.Fatal("a nested day goal has no parent_id key")
	}
	if got, ok := raw.(float64); !ok || int64(got) != parent.ID {
		t.Errorf("parent_id = %v, want %d", raw, parent.ID)
	}

	loose := contractGoalsItemByText(t, dayLane, "loose")
	if _, ok := loose["parent_id"]; ok {
		t.Errorf("an unparented day goal carries parent_id = %v; the key is omitted when 0", loose["parent_id"])
	}
	weekItem := contractGoalsItemByText(t, contractGoalsLane(t, body, "week"), "the weekly")
	if _, ok := weekItem["parent_id"]; ok {
		t.Errorf("a week goal carries parent_id = %v; week goals never have one", weekItem["parent_id"])
	}
}

// ---------------------------------------------------------------------------
// GET /api/goals?slug= — job_goal
// ---------------------------------------------------------------------------

// TestContractGoalsGetSlugAddsJobGoal pins the slug-scoped extra, INCLUDING
// the "any period, done or not" clause: the editor picker has to be able to
// show the currently-linked goal even when it is not one of the current-period
// rows, or picking it once and reopening the form shows an empty select.
func TestContractGoalsGetSlugAddsJobGoal(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()

	// deliberately a DONE goal from a PAST period — the hardest legal case
	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: "2026-01-05", Text: "an old, finished goal"}, 1)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	done := true
	if err := st.UpdateGoal(g.ID, store.GoalUpdate{Done: &done}, 2); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	j := mustCreateStoreJob(t, st, store.Job{Slug: "goal-linked", Repo: "kraken", State: "working"})
	if err := st.SetJobGoal(j.ID, g.ID); err != nil {
		t.Fatalf("SetJobGoal: %v", err)
	}

	body := contractGoalsGetOK(t, "goal-linked")
	raw, ok := body["job_goal"]
	if !ok {
		t.Fatalf("GET ?slug= has no job_goal key: %v", body)
	}
	jg, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("job_goal = %T, want an object", raw)
	}
	if got, ok := jg["id"].(float64); !ok || int64(got) != g.ID {
		t.Errorf("job_goal.id = %v, want %d", jg["id"], g.ID)
	}
	if jg["text"] != "an old, finished goal" {
		t.Errorf("job_goal.text = %v, want the goal's text", jg["text"])
	}
	if jg["done"] != true {
		t.Errorf("job_goal.done = %v, want true", jg["done"])
	}
	// the lanes are still served alongside it
	contractGoalsLane(t, body, "week")
	contractGoalsLane(t, body, "day")
}

// TestContractGoalsGetSlugOmitsJobGoalWhenUnlinked: ABSENT, not null and not
// a zero-valued object — the client's test is key presence.
func TestContractGoalsGetSlugOmitsJobGoalWhenUnlinked(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "goal-unlinked", Repo: "kraken", State: "working"})

	body := contractGoalsGetOK(t, "goal-unlinked")
	if v, ok := body["job_goal"]; ok {
		t.Errorf("job_goal = %v for a job with no goal; the key is absent", v)
	}
	contractGoalsLane(t, body, "week")
	contractGoalsLane(t, body, "day")
}

// TestContractGoalsGetUnknownSlugIs404 — the slug-scoped read's one refusal,
// kept here because the scoped happy path above is only meaningful if an
// unknown slug is distinguishable from an unlinked job.
func TestContractGoalsGetUnknownSlugIs404(t *testing.T) {
	useTestStore(t)
	if rec := contractGoalsGet(t, "no-such-job"); rec.Code != http.StatusNotFound {
		t.Errorf("GET ?slug=no-such-job: status %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/goals — the three ops
// ---------------------------------------------------------------------------

// TestContractGoalsPostAddDefaultsThePeriod is the add op's convenience half:
// the panel's add inputs never send a period, so an absent one means "the
// current period FOR THAT SCOPE" — the week's Monday for a week goal, today's
// date for a day goal. Getting this wrong by defaulting both to `day` puts
// weekly goals in a period that is never listed again after midnight.
func TestContractGoalsPostAddDefaultsThePeriod(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, day := contractGoalsCurrent(t)

	contractGoalsPostOK(t, map[string]any{"op": "add", "goal": map[string]any{"scope": "week", "text": "weekly, no period"}})
	contractGoalsPostOK(t, map[string]any{"op": "add", "goal": map[string]any{"scope": "day", "text": "daily, no period"}})

	weekRows, err := st.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals week: %v", err)
	}
	if len(weekRows) != 1 || weekRows[0].Text != "weekly, no period" {
		t.Fatalf("week %s rows = %+v, want the added weekly goal", week, weekRows)
	}
	dayRows, err := st.ListGoals("day", day)
	if err != nil {
		t.Fatalf("ListGoals day: %v", err)
	}
	if len(dayRows) != 1 || dayRows[0].Text != "daily, no period" {
		t.Fatalf("day %s rows = %+v, want the added day goal", day, dayRows)
	}

	// and the write is immediately observable on the read route
	body := contractGoalsGetOK(t, "")
	contractGoalsItemByText(t, contractGoalsLane(t, body, "week"), "weekly, no period")
	contractGoalsItemByText(t, contractGoalsLane(t, body, "day"), "daily, no period")
}

// TestContractGoalsPostAddHonoursAnExplicitPeriodAndParent covers the other
// half of the add op's wire shape: an explicit period is obeyed, and a
// parent_id nests a day goal under a week goal (the "+ day goal…" affordance
// on a week row).
func TestContractGoalsPostAddHonoursAnExplicitPeriodAndParent(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, day := contractGoalsCurrent(t)

	contractGoalsPostOK(t, map[string]any{"op": "add", "goal": map[string]any{
		"scope": "week", "period": week, "text": "explicit weekly"}})
	weekRows, err := st.ListGoals("week", week)
	if err != nil || len(weekRows) != 1 {
		t.Fatalf("ListGoals week = %+v, err %v", weekRows, err)
	}
	parent := weekRows[0]

	contractGoalsPostOK(t, map[string]any{"op": "add", "goal": map[string]any{
		"scope": "day", "period": day, "text": "nested via the API", "parent_id": parent.ID}})
	dayRows, err := st.ListGoals("day", day)
	if err != nil || len(dayRows) != 1 {
		t.Fatalf("ListGoals day = %+v, err %v", dayRows, err)
	}
	if dayRows[0].ParentID != parent.ID {
		t.Errorf("ParentID = %d, want the posted %d", dayRows[0].ParentID, parent.ID)
	}
	item := contractGoalsItemByText(t, contractGoalsLane(t, contractGoalsGetOK(t, ""), "day"), "nested via the API")
	if got, ok := item["parent_id"].(float64); !ok || int64(got) != parent.ID {
		t.Errorf("GET parent_id = %v, want %d", item["parent_id"], parent.ID)
	}
}

// TestContractGoalsPostUpdateChangesOnlyWhatIsSent is the update op's whole
// contract: an absent field is unchanged. The done checkbox posts done alone;
// click-to-edit posts text alone. A handler that decoded into plain (non-
// pointer) fields would blank the goal's text on every checkbox click.
func TestContractGoalsPostUpdateChangesOnlyWhatIsSent(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, day := contractGoalsCurrent(t)

	parentRow, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "the weekly"}, 1)
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	g, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: "original", ParentID: parentRow.ID}, 1)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}

	contractGoalsPostOK(t, map[string]any{"op": "update", "id": g.ID, "done": true})
	rows, err := st.ListGoals("day", day)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", rows, err)
	}
	if !rows[0].Done {
		t.Error("done was not applied")
	}
	if rows[0].Text != "original" {
		t.Errorf("text = %q after a done-only update, want it unchanged", rows[0].Text)
	}
	if rows[0].ParentID != parentRow.ID {
		t.Errorf("parent_id = %d after a done-only update, want it unchanged", rows[0].ParentID)
	}

	contractGoalsPostOK(t, map[string]any{"op": "update", "id": g.ID, "text": "renamed"})
	rows, err = st.ListGoals("day", day)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", rows, err)
	}
	if rows[0].Text != "renamed" {
		t.Errorf("text = %q, want %q", rows[0].Text, "renamed")
	}
	if !rows[0].Done {
		t.Error("done was cleared by a text-only update")
	}
	item := contractGoalsItemByText(t, contractGoalsLane(t, contractGoalsGetOK(t, ""), "day"), "renamed")
	if item["done"] != true {
		t.Errorf("GET done = %v after the text update, want true", item["done"])
	}
}

// TestContractGoalsPostUpdateExplicitParentIDZeroClears is the one value that
// cannot be expressed as "absent": posting parent_id 0 is the unassign verb,
// so 0 must reach the store as a CLEAR rather than being read as "field not
// sent".
func TestContractGoalsPostUpdateExplicitParentIDZeroClears(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, day := contractGoalsCurrent(t)

	parent, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "the weekly"}, 1)
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	g, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: "nested", ParentID: parent.ID}, 1)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}

	contractGoalsPostOK(t, map[string]any{"op": "update", "id": g.ID, "parent_id": 0})
	rows, err := st.ListGoals("day", day)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", rows, err)
	}
	if rows[0].ParentID != 0 {
		t.Errorf("ParentID = %d after an explicit parent_id:0, want 0 (cleared)", rows[0].ParentID)
	}
	item := contractGoalsItemByText(t, contractGoalsLane(t, contractGoalsGetOK(t, ""), "day"), "nested")
	if _, ok := item["parent_id"]; ok {
		t.Errorf("GET still carries parent_id = %v after the clear", item["parent_id"])
	}
}

// TestContractGoalsPostRemoveDeletesAndIsIdempotent: remove deletes, and an
// id that is already gone STILL answers 200 — the delete is a no-op
// underneath, so a double-click or a retry from a stale panel is not an error
// the user has to see.
func TestContractGoalsPostRemoveDeletesAndIsIdempotent(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, _ := contractGoalsCurrent(t)

	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "doomed"}, 1)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	contractGoalsPostOK(t, map[string]any{"op": "remove", "id": g.ID})
	rows, err := st.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListGoals = %+v after remove, want empty", rows)
	}
	if got := contractGoalsLane(t, contractGoalsGetOK(t, ""), "week"); len(got) != 0 {
		t.Errorf("GET week lane = %v after remove, want empty", got)
	}

	// the same id again, and an id that never existed
	contractGoalsPostOK(t, map[string]any{"op": "remove", "id": g.ID})
	contractGoalsPostOK(t, map[string]any{"op": "remove", "id": g.ID + 9999})
}

// ---------------------------------------------------------------------------
// POST /api/job/goal
// ---------------------------------------------------------------------------

// TestContractJobGoalLinksALiveJobAndClears is the route's reason for
// existing: it is settable on a LIVE job (the editor form is read-only there,
// which is exactly when carl decides what a running job is for), and goal_id 0
// is the unlink.
func TestContractJobGoalLinksALiveJobAndClears(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, _ := contractGoalsCurrent(t)

	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "the linked goal"}, 1)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	j := mustCreateStoreJob(t, st, store.Job{Slug: "live-link", Repo: "kraken", State: "working"})

	rec := contractGoalsJobGoal(t, "live-link", g.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("link: status %d, body %s", rec.Code, rec.Body.String())
	}
	var ok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if ok["ok"] != true {
		t.Fatalf("body = %s, want {\"ok\":true}", rec.Body.String())
	}

	row, err := st.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if row.GoalID != g.ID {
		t.Fatalf("GoalID = %d, want %d", row.GoalID, g.ID)
	}
	// observable on the read route the editor polls
	body := contractGoalsGetOK(t, "live-link")
	jg, okKey := body["job_goal"].(map[string]any)
	if !okKey {
		t.Fatalf("job_goal missing after the link: %v", body)
	}
	if got, isNum := jg["id"].(float64); !isNum || int64(got) != g.ID {
		t.Errorf("job_goal.id = %v, want %d", jg["id"], g.ID)
	}

	if rec := contractGoalsJobGoal(t, "live-link", 0); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d, body %s", rec.Code, rec.Body.String())
	}
	row, err = st.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID after clear: %v", err)
	}
	if row.GoalID != 0 {
		t.Errorf("GoalID = %d after goal_id 0, want 0", row.GoalID)
	}
	if _, present := contractGoalsGetOK(t, "live-link")["job_goal"]; present {
		t.Error("job_goal is still present after the unlink")
	}
}

// TestContractJobGoalLeavesRevAndUpdatedAtAlone is the colour-route property
// asserted through the ROUTE rather than the store method — the route is where
// a well-meaning "and stamp updated_at so the board refreshes" line gets added.
func TestContractJobGoalLeavesRevAndUpdatedAtAlone(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	week, _ := contractGoalsCurrent(t)

	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "goal"}, 1)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	// rev/updated_at deliberately in the past, so a rewrite to "now" is visible
	row := store.Job{Slug: "rev-guard", Repo: "kraken", State: "working", Rev: 5,
		CreatedAt: 1_600_000_000_000, UpdatedAt: 1_600_000_000_000}
	if _, err := st.CreateJob(&row); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := st.GetJobBySlug("rev-guard")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}

	if rec := contractGoalsJobGoal(t, "rev-guard", g.ID); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	after, err := st.GetJobBySlug("rev-guard")
	if err != nil {
		t.Fatalf("GetJobBySlug after: %v", err)
	}
	if after.GoalID != g.ID {
		t.Fatalf("GoalID = %d, want %d", after.GoalID, g.ID)
	}
	if after.Rev != before.Rev {
		t.Errorf("rev moved %d → %d through the route", before.Rev, after.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updated_at moved %d → %d through the route", before.UpdatedAt, after.UpdatedAt)
	}
}
