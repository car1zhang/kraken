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

// jobgoalswire_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's HTTP half after the many-to-many cutover:
// webJob.goals / webJob.goal, GET /api/goals?slug='s job_goals / job_goal, and
// POST /api/job/goal's three ops.
//
// Written from the contract text alone. The working-tree versions of
// internal/api/{goals,board}.go were never read; the harness (useTestStore,
// mustCreateStoreJob, mustJSON, the boardCache shape) is PRE-EXISTING package
// state at the base commit (HEAD = 66eb71a), as is the prior round's
// goals_contract_test.go, whose `contractGoals*` namespace this file
// deliberately does not touch.
//
// Dimension: LOGIC — the stated wire shape and the happy paths. The refusals
// (an absent or unknown `op`, an unknown slug's 404, the store's sentence
// surfacing as a 400, the mirror gate's exact 403 body, oversize bodies) are
// athena-edge's ground and are not probed here.
//
// Every package-level identifier declared here is prefixed `contractJGW`.

// contractJGWNow is the fixed write clock every seeded goal is stamped with.
const contractJGWNow = int64(1_700_000_000_000)

// contractJGWCurrent is the (week, day) pair the GET answers for.
func contractJGWCurrent(t *testing.T) (string, string) {
	t.Helper()
	return store.GoalPeriods(time.Now())
}

// contractJGWGoal seeds one CURRENT-week goal.
func contractJGWGoal(t *testing.T, st *store.Store, text string) *store.Goal {
	t.Helper()
	week, _ := contractJGWCurrent(t)
	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: text}, contractJGWNow)
	if err != nil {
		t.Fatalf("AddGoal(%q): %v", text, err)
	}
	return g
}

// contractJGWPost drives POST /api/job/goal with a HAND-BUILT body — the wire
// shape is what is under test, so nothing here goes through the handler's own
// request struct.
func contractJGWPost(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	jobGoalHandler(rec, req)
	return rec
}

// contractJGWPostOK drives the route and requires 200 with {"ok":true} — the
// answer this route has always given, retained because the contract's changes
// to it are all on the REQUEST side.
func contractJGWPostOK(t *testing.T, body map[string]any) {
	t.Helper()
	rec := contractJGWPost(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/goal %v: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var ok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode POST body %q: %v", rec.Body.String(), err)
	}
	if ok["ok"] != true {
		t.Fatalf("POST body = %s, want {\"ok\":true}", rec.Body.String())
	}
}

// contractJGWGet drives GET /api/goals?slug= and decodes into loose maps: the
// contract pins key PRESENCE and ABSENCE, which a struct with omitempty would
// silently paper over.
func contractJGWGet(t *testing.T, slug string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/goals?slug="+slug, nil)
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/goals?slug=%s: status %d, body %s", slug, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET body %q: %v", rec.Body.String(), err)
	}
	return body
}

// contractJGWObjects pulls a key that must be a JSON ARRAY of objects.
func contractJGWObjects(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key]
	if !ok {
		t.Fatalf("body has no %q key: %v", key, body)
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("%q = %T, want an array", key, raw)
	}
	out := make([]map[string]any, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%q[%d] = %T, want an object", key, i, item)
		}
		out = append(out, obj)
	}
	return out
}

// contractJGWIDs reads the `id` of each object in order.
func contractJGWIDs(t *testing.T, objs []map[string]any) []int64 {
	t.Helper()
	out := make([]int64, 0, len(objs))
	for i, o := range objs {
		n, ok := o["id"].(float64)
		if !ok {
			t.Fatalf("element %d has id = %#v, want a number", i, o["id"])
		}
		out = append(out, int64(n))
	}
	return out
}

func contractJGWSameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// contractJGWLink links through the STORE, for tests whose subject is the read
// side — the route has its own tests below.
func contractJGWLink(t *testing.T, st *store.Store, jobID, goalID int64) {
	t.Helper()
	if err := st.AddJobGoal(jobID, goalID); err != nil {
		t.Fatalf("AddJobGoal(job %d, goal %d): %v", jobID, goalID, err)
	}
}

// ---------------------------------------------------------------------------
// webJob.goals / webJob.goal — the card's wire shape
// ---------------------------------------------------------------------------

// TestContractJGWWebJobSerializesGoalsAndTheirPrimary pins the two keys'
// SPELLING and their omitempty, on a hand-built card rather than through
// buildWebBoard (which drags in tmux/process-table discovery and cannot be
// called from a test — update_test.go's and jobload_test.go's phrasing for the
// same constraint).
//
// Both halves matter. `goals` present with complete {id,text,done} elements in
// order is what a card renders its chip row from; and ABSENCE being the only
// no-goals spelling is what makes `job.goals ?? …` on three clients correct —
// a nil slice marshalled as `null` would put a second falsy shape on the wire
// for a state that is not different.
func TestContractJGWWebJobSerializesGoalsAndTheirPrimary(t *testing.T) {
	first := webJobGoal{ID: 7, Text: "ship goal boards", Done: false}
	second := webJobGoal{ID: 3, Text: "close the intake gap", Done: true}
	linked := webJob{Slug: "linked", Goals: []webJobGoal{first, second}, Goal: &first}

	raw, err := json.Marshal(linked)
	if err != nil {
		t.Fatalf("marshal a linked card: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	objs := contractJGWObjects(t, got, "goals")
	if ids := contractJGWIDs(t, objs); !contractJGWSameIDs(ids, []int64{7, 3}) {
		t.Fatalf("goals ids = %v, want [7 3] — link-position order, not sorted", ids)
	}
	if objs[0]["text"] != "ship goal boards" || objs[0]["done"] != false {
		t.Errorf("goals[0] = %v, want the whole {id,text,done}", objs[0])
	}
	if objs[1]["text"] != "close the intake gap" || objs[1]["done"] != true {
		t.Errorf("goals[1] = %v, want the whole {id,text,done}", objs[1])
	}
	primary, ok := got["goal"].(map[string]any)
	if !ok {
		t.Fatalf("goal = %#v, want the object goals[0] also is", got["goal"])
	}
	if primary["id"] != objs[0]["id"] || primary["text"] != objs[0]["text"] || primary["done"] != objs[0]["done"] {
		t.Errorf("goal = %v, want it EQUAL to goals[0] (%v) — the primary is the array's head", primary, objs[0])
	}

	// an unlinked card spells "no goals" by carrying neither key
	bare, err := json.Marshal(webJob{Slug: "bare"})
	if err != nil {
		t.Fatalf("marshal an unlinked card: %v", err)
	}
	var none map[string]any
	if err := json.Unmarshal(bare, &none); err != nil {
		t.Fatalf("decode %s: %v", bare, err)
	}
	if v, present := none["goals"]; present {
		t.Errorf("an unlinked card carries goals = %#v; absence is the only no-goals spelling", v)
	}
	if v, present := none["goal"]; present {
		t.Errorf("an unlinked card carries goal = %#v; absence is the only no-goals spelling", v)
	}
}

// TestContractJGWArchivedCardCarriesItsGoals is the ARCHIVED half of the same
// row, against the signature frozen at the join: the caller resolves the chips
// out of the board-wide maps and hands `archivedWebJob` the finished slice, so
// the function's whole obligation here is to carry that slice onto the card and
// to derive `Goal` from its HEAD.
//
// It is worth its own test because today's archived card omits `goal`
// SILENTLY, and the goal filter covers the archive drawer — an archived card
// with no chips is a card the filter matches and the eye cannot explain.
//
// Both arms are asserted on the same fixture so the test cannot pass merely by
// having nothing to serve: a real two-goal slice lands, and an EMPTY one leaves
// both keys off the wire entirely.
func TestContractJGWArchivedCardCarriesItsGoals(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	row := store.Job{Slug: "shelved", Repo: "kraken", State: "done"}
	if _, err := st.CreateJob(&row); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	sr, err := st.GetJobBySlug("shelved")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if _, err := st.ArchiveJob(sr.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	archived := jobs.LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("LoadArchivedJobs = %d jobs, want 1", len(archived))
	}
	a := archived[0]
	tokens := map[int64]store.JobTokens{a.ID: {JobID: a.ID, In: 1, Out: 2}}

	chips := []webJobGoal{
		{ID: 7, Text: "ship goal boards", Done: false},
		{ID: 3, Text: "close the intake gap", Done: true},
	}
	wj := archivedWebJob(a, st, tokens, chips)
	if wj.Tokens == nil {
		t.Fatal("the archived card dropped tokens — the fixture's own control just broke")
	}
	if len(wj.Goals) != 2 {
		t.Fatalf("archived card Goals = %+v, want the 2-element slice it was handed", wj.Goals)
	}
	if wj.Goals[0] != chips[0] || wj.Goals[1] != chips[1] {
		t.Errorf("archived card Goals = %+v, want %+v in the same order", wj.Goals, chips)
	}
	if wj.Goal == nil {
		t.Fatal("archived card carries goals but no primary; goal is goals[0]")
	}
	if *wj.Goal != chips[0] {
		t.Errorf("archived card Goal = %+v, want the slice's HEAD %+v", *wj.Goal, chips[0])
	}

	// and an unlinked archived job spells that with neither key on the wire
	empty := archivedWebJob(a, st, tokens, nil)
	if len(empty.Goals) != 0 {
		t.Errorf("an unlinked archived card carries Goals = %+v, want none", empty.Goals)
	}
	if empty.Goal != nil {
		t.Errorf("an unlinked archived card carries Goal = %+v, want nil", *empty.Goal)
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal the unlinked archived card: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if v, present := decoded["goals"]; present {
		t.Errorf("goals = %#v on an unlinked archived card; absence is the only no-goals spelling", v)
	}
	if v, present := decoded["goal"]; present {
		t.Errorf("goal = %#v on an unlinked archived card; absence is the only no-goals spelling", v)
	}
}

// ---------------------------------------------------------------------------
// GET /api/goals?slug= — job_goals
// ---------------------------------------------------------------------------

// TestContractJGWGoalsGetServesJobGoalsInPositionOrder is the picker's read.
// The linked set deliberately includes a DONE goal from a PAST period — the
// hardest legal case, and the whole reason the link is resolved by id rather
// than out of the two current-period lists: a picker that could not show an
// off-period link would render it as unlinked and unlink it on the next pick.
func TestContractJGWGoalsGetServesJobGoalsInPositionOrder(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()

	stale, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: "2026-01-05", Text: "an old, finished goal"}, contractJGWNow)
	if err != nil {
		t.Fatalf("seed stale goal: %v", err)
	}
	done := true
	if err := st.UpdateGoal(stale.ID, store.GoalUpdate{Done: &done}, contractJGWNow+1); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	current := contractJGWGoal(t, st, "this week's goal")

	j := mustCreateStoreJob(t, st, store.Job{Slug: "multi-linked", Repo: "kraken", State: "working"})
	contractJGWLink(t, st, j.ID, stale.ID)
	contractJGWLink(t, st, j.ID, current.ID)

	body := contractJGWGet(t, "multi-linked")
	objs := contractJGWObjects(t, body, "job_goals")
	if ids := contractJGWIDs(t, objs); !contractJGWSameIDs(ids, []int64{stale.ID, current.ID}) {
		t.Fatalf("job_goals ids = %v, want [%d %d] in link-position order", ids, stale.ID, current.ID)
	}
	if objs[0]["text"] != "an old, finished goal" || objs[0]["done"] != true {
		t.Errorf("job_goals[0] = %v, want the done past-period row served whole", objs[0])
	}
	if objs[1]["text"] != "this week's goal" || objs[1]["done"] != false {
		t.Errorf("job_goals[1] = %v, want the current goal", objs[1])
	}

	primary, ok := body["job_goal"].(map[string]any)
	if !ok {
		t.Fatalf("job_goal = %#v, want the object job_goals[0] also is", body["job_goal"])
	}
	if primary["id"] != objs[0]["id"] || primary["text"] != objs[0]["text"] || primary["done"] != objs[0]["done"] {
		t.Errorf("job_goal = %v, want it EQUAL to job_goals[0] (%v)", primary, objs[0])
	}

	// the two current-period lanes still ride alongside
	if _, ok := body["goals"]; !ok {
		t.Error("the slug-scoped GET dropped the goals lanes")
	}
}

// TestContractJGWGoalsGetOmitsBothKeysWhenUnlinked: ABSENT, not null and not an
// empty array — the client's test is key presence, and an empty array would
// claim "this job links a list of no goals".
func TestContractJGWGoalsGetOmitsBothKeysWhenUnlinked(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	contractJGWGoal(t, st, "a goal nobody linked")
	mustCreateStoreJob(t, st, store.Job{Slug: "no-links", Repo: "kraken", State: "working"})

	body := contractJGWGet(t, "no-links")
	if v, present := body["job_goals"]; present {
		t.Errorf("job_goals = %#v for an unlinked job; the key is absent", v)
	}
	if v, present := body["job_goal"]; present {
		t.Errorf("job_goal = %#v for an unlinked job; the key is absent", v)
	}
}

// ---------------------------------------------------------------------------
// POST /api/job/goal — the ops
// ---------------------------------------------------------------------------

// TestContractJGWJobGoalAddLinksOnePerPost is the picker's per-link write, and
// the property full-replace would lose: two surfaces racing can each land their
// own link without wiping the other's, so an `add` touches exactly the id it
// names and appends it after whatever was already there.
func TestContractJGWJobGoalAddLinksOnePerPost(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	first := contractJGWGoal(t, st, "first pick")
	second := contractJGWGoal(t, st, "second pick")
	j := mustCreateStoreJob(t, st, store.Job{Slug: "live-link", Repo: "kraken", State: "working"})

	contractJGWPostOK(t, map[string]any{"slug": "live-link", "goal_id": first.ID, "op": "add"})
	contractJGWPostOK(t, map[string]any{"slug": "live-link", "goal_id": second.ID, "op": "add"})

	row, err := st.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if !contractJGWSameIDs(row.GoalIDs, []int64{first.ID, second.ID}) {
		t.Fatalf("GoalIDs = %v, want [%d %d] in the order they were posted", row.GoalIDs, first.ID, second.ID)
	}
	// observable on the read route the picker polls
	ids := contractJGWIDs(t, contractJGWObjects(t, contractJGWGet(t, "live-link"), "job_goals"))
	if !contractJGWSameIDs(ids, []int64{first.ID, second.ID}) {
		t.Errorf("GET job_goals = %v, want [%d %d]", ids, first.ID, second.ID)
	}
}

// TestContractJGWJobGoalRemoveUnlinksExactlyOne is add's mirror: the id named
// goes, everything else stays, in the order it was already in.
func TestContractJGWJobGoalRemoveUnlinksExactlyOne(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	a := contractJGWGoal(t, st, "goal a")
	b := contractJGWGoal(t, st, "goal b")
	c := contractJGWGoal(t, st, "goal c")
	j := mustCreateStoreJob(t, st, store.Job{Slug: "unlink-one", Repo: "kraken", State: "working"})
	contractJGWLink(t, st, j.ID, a.ID)
	contractJGWLink(t, st, j.ID, b.ID)
	contractJGWLink(t, st, j.ID, c.ID)

	contractJGWPostOK(t, map[string]any{"slug": "unlink-one", "goal_id": b.ID, "op": "remove"})

	row, err := st.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if !contractJGWSameIDs(row.GoalIDs, []int64{a.ID, c.ID}) {
		t.Fatalf("GoalIDs = %v, want [%d %d]", row.GoalIDs, a.ID, c.ID)
	}
	body := contractJGWGet(t, "unlink-one")
	objs := contractJGWObjects(t, body, "job_goals")
	if ids := contractJGWIDs(t, objs); !contractJGWSameIDs(ids, []int64{a.ID, c.ID}) {
		t.Fatalf("GET job_goals = %v, want [%d %d]", ids, a.ID, c.ID)
	}
	primary, ok := body["job_goal"].(map[string]any)
	if !ok || primary["id"] != objs[0]["id"] {
		t.Errorf("job_goal = %#v after the remove, want the surviving head %v", body["job_goal"], objs[0])
	}
}

// TestContractJGWJobGoalZeroClearsEveryLinkWhateverOpSays is the LEGACY clear
// shape, kept because a shipped client (and the picker's `—` row) sends
// `goal_id: 0` with no op at all. The three variants below are the same
// request: id 0 is not an id, so there is nothing for an op to scope, and a
// handler that read `op` first would answer 400 to the one body every existing
// caller sends.
func TestContractJGWJobGoalZeroClearsEveryLinkWhateverOpSays(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	a := contractJGWGoal(t, st, "goal a")
	b := contractJGWGoal(t, st, "goal b")
	j := mustCreateStoreJob(t, st, store.Job{Slug: "clear-all", Repo: "kraken", State: "working"})

	for _, body := range []map[string]any{
		{"slug": "clear-all", "goal_id": 0},
		{"slug": "clear-all", "goal_id": 0, "op": "add"},
		{"slug": "clear-all", "goal_id": 0, "op": "remove"},
	} {
		contractJGWLink(t, st, j.ID, a.ID)
		contractJGWLink(t, st, j.ID, b.ID)
		if row, err := st.GetJobByID(j.ID); err != nil || len(row.GoalIDs) != 2 {
			t.Fatalf("fixture did not link two goals (%v, err %v)", row, err)
		}

		contractJGWPostOK(t, body)

		row, err := st.GetJobByID(j.ID)
		if err != nil {
			t.Fatalf("GetJobByID after %v: %v", body, err)
		}
		if len(row.GoalIDs) != 0 {
			t.Errorf("%v left GoalIDs = %v, want every link cleared", body, row.GoalIDs)
		}
		if v, present := contractJGWGet(t, "clear-all")["job_goals"]; present {
			t.Errorf("%v left job_goals = %#v on the wire, want the key absent", body, v)
		}
	}
}

// TestContractJGWJobGoalLeavesRevAndUpdatedAtAlone asserts the colour-route
// property through the ROUTE rather than the store method — the route is where
// a well-meaning "and stamp updated_at so the board refreshes" line gets added,
// and the three ops must each be exempt.
func TestContractJGWJobGoalLeavesRevAndUpdatedAtAlone(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	g := contractJGWGoal(t, st, "the linked goal")
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

	check := func(what string) {
		t.Helper()
		after, err := st.GetJobBySlug("rev-guard")
		if err != nil {
			t.Fatalf("GetJobBySlug after %s: %v", what, err)
		}
		if after.Rev != before.Rev {
			t.Errorf("%s moved rev %d → %d through the route", what, before.Rev, after.Rev)
		}
		if after.UpdatedAt != before.UpdatedAt {
			t.Errorf("%s moved updated_at %d → %d through the route", what, before.UpdatedAt, after.UpdatedAt)
		}
	}

	contractJGWPostOK(t, map[string]any{"slug": "rev-guard", "goal_id": g.ID, "op": "add"})
	check("add")
	contractJGWPostOK(t, map[string]any{"slug": "rev-guard", "goal_id": g.ID, "op": "remove"})
	check("remove")
	contractJGWPostOK(t, map[string]any{"slug": "rev-guard", "goal_id": 0})
	check("clear")
}

// TestContractJGWJobGoalInvalidatesTheBoardCache: the board is memoized for 2s
// and every client's post-action refresh is well inside that window, so a write
// that did not invalidate would leave the freshly-linked card showing no chip
// until the TTL happened to lapse — the class of bug that reads as "the click
// didn't work".
//
// The package's single cache instance is swapped for a counting one (the
// countingCache shape board_test.go already uses) and restored, so nothing
// leaks into another test in this binary.
func TestContractJGWJobGoalInvalidatesTheBoardCache(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	g := contractJGWGoal(t, st, "cache goal")
	mustCreateStoreJob(t, st, store.Job{Slug: "cache-job", Repo: "kraken", State: "working"})

	saved := webBoardCache
	t.Cleanup(func() { webBoardCache = saved })
	builds := 0
	clock := time.Now()
	webBoardCache = &boardCache{
		ttl:   time.Hour, // long enough that only an invalidate can force a rebuild
		now:   func() time.Time { return clock },
		build: func() webBoard { builds++; return webBoard{} },
	}

	for _, c := range []struct {
		what string
		body map[string]any
	}{
		{"add", map[string]any{"slug": "cache-job", "goal_id": g.ID, "op": "add"}},
		{"remove", map[string]any{"slug": "cache-job", "goal_id": g.ID, "op": "remove"}},
		{"clear", map[string]any{"slug": "cache-job", "goal_id": 0}},
	} {
		webBoardCache.get() // prime
		was := builds
		contractJGWPostOK(t, c.body)
		webBoardCache.get()
		if builds != was+1 {
			t.Errorf("a successful %s did not invalidate the board cache (builds %d → %d inside the ttl)",
				c.what, was, builds)
		}
	}
}

// ---------------------------------------------------------------------------
// apiContract
// ---------------------------------------------------------------------------

// TestContractJGWAPIContractStaysSix. Every key this change adds — `goals` on
// a job card, `job_goals` on the goals GET, `op` in the job-goal request — is
// ADDITIVE: nothing is retyped, renamed or removed, so a shipped iOS build
// that has never heard of any of them decodes the board exactly as it did
// before. A bump here would make every installed build refuse the payload for
// keys it does not read.
func TestContractJGWAPIContractStaysSix(t *testing.T) {
	if apiContract != 6 {
		t.Fatalf("apiContract = %d, want 6 — every key in this change is additive", apiContract)
	}
}
