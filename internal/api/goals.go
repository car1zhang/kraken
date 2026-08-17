package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// goals.go — GET/POST /api/goals and POST /api/job/goal: carl's weekly and
// daily goals (store/goals.go) and the goals a job serves — any number of them
// since schema v27, held as job_goals rows (store/jobgoals.go) and served as an
// ordered array whose head is the retained single `goal` key.
//
// The GET is deliberately period-LESS: it always answers for the CURRENT
// local week and day (store.GoalPeriods), because every caller — the board's
// panel, the phone's drawer, `kraken goals get` inside a cell — is asking
// "what am I working toward right now", and a caller that had to compute
// Monday itself would be a second implementation of the one rule
// GoalPeriods exists to hold.
//
// The job link is its own route rather than a field of the job form for the
// reason /api/job/color and /api/job/load are (jobcolor.go, jobload.go): the
// form save is gated on editableJob — no lifecycle in flight — and labelling
// a job with the goals it serves is most useful precisely while its cell is
// LIVE, so routing it through the form would put the feature out of reach
// exactly where it matters. The full-replace blast radius argument applies
// identically, and it is why the route takes a per-goal `op` rather than a
// list of ids: a save (or a post) from a client that has not seen every link
// would clear the ones it does not know about.

// goalLogTextMax caps the goal text echoed into an event line for a REFUSED
// add — the one place a request body's text reaches the events table, since a
// successful add is logged by the id the store assigned it instead.
const goalLogTextMax = 60

// webGoal is one goals row on the wire. parent_id is the only omitempty
// field: 0 means "no parent" rather than "goal zero", and every other key is
// a real answer even at its zero value (a goal at position 0, an undone
// goal, a goal whose text is somehow empty) — absence of `done` would be
// indistinguishable from false to a client reading it unconditionally.
type webGoal struct {
	ID       int64  `json:"id"`
	Scope    string `json:"scope"`
	Period   string `json:"period"`
	Text     string `json:"text"`
	Done     bool   `json:"done"`
	Position int64  `json:"position"`
	ParentID int64  `json:"parent_id,omitempty"`
}

// webGoals is the two current-period lists, served both as /api/goals' whole
// payload and as /api/board's top-level "goals" key — one assembly function
// (currentGoals) so the two cannot drift, the same shape discipline
// webUpdate/webShipIOS follow.
//
// Both arrays are ALWAYS present and never null: a client renders "no goals
// this week" from an empty array, and a null would make every consumer write
// a second nil check for a state that is not different.
type webGoals struct {
	Week []webGoal `json:"week"`
	Day  []webGoal `json:"day"`
}

// webJobGoal is the goal a job links, as it rides a job card: id, text and
// done, and nothing else. Not webGoal — the card renders one chip, so period
// and position would be keys no client reads, and the id is here only so a
// click can address the goal it names.
type webJobGoal struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// newWebGoals returns the empty-but-present pair every assembly starts from,
// so "never null" is a property of the one constructor rather than a rule
// each call site has to remember.
func newWebGoals() webGoals {
	return webGoals{Week: []webGoal{}, Day: []webGoal{}}
}

func webGoalFrom(g store.Goal) webGoal {
	return webGoal{
		ID: g.ID, Scope: g.Scope, Period: g.Period, Text: g.Text,
		Done: g.Done, Position: g.Position, ParentID: g.ParentID,
	}
}

func webJobGoalFrom(g store.Goal) *webJobGoal {
	return &webJobGoal{ID: g.ID, Text: g.Text, Done: g.Done}
}

// currentGoals reads the current local week's and today's goals. Log-and-
// return-empty on a query failure rather than propagating the error, the
// same never-fail-the-whole-poll-for-one-field contract jobQuestions and
// jobTokensIndex follow: this feeds /api/board as well as /api/goals, and a
// goals query that failed must cost carl the panel, not the board.
func currentGoals(st *store.Store) webGoals {
	out := newWebGoals()
	if st == nil {
		return out
	}
	week, day := store.GoalPeriods(time.Now())
	for _, l := range []struct {
		scope  string
		period string
		into   *[]webGoal
	}{
		{"week", week, &out.Week},
		{"day", day, &out.Day},
	} {
		rows, err := st.ListGoals(l.scope, l.period)
		if err != nil {
			util.LogEvent("web", "error", "goals", fmt.Sprintf("list %s goals for %s: %v", l.scope, l.period, err))
			continue
		}
		for _, g := range rows {
			*l.into = append(*l.into, webGoalFrom(g))
		}
	}
	return out
}

// jobGoalLinks reads the goal links for one board build — ONE query for the
// whole job_goals TABLE (store.ListJobGoalLinks), never a read per card, which
// is the same rule jobTokensIndex follows for the token rows and for the same
// reason: a board carrying ~74 jobs rebuilds roughly every two seconds.
//
// Whole-table rather than board-scoped, and the two coincide rather than
// agreeing by design: every link row belongs to a job, and the board renders
// every job there is — active cards and archived ones alike — so a WHERE over
// the board's ids would name every row the unfiltered read already returns,
// while costing a rebuilt, re-planned statement on the hottest read in the
// process. The map may therefore carry a job id no card looks up — the build
// loaded its jobs before this query ran, so a job linked in the gap is in here
// and not on that board — and that costs nothing: every consumer reads it BY
// job id, so a spare entry is never seen.
//
// A failure degrades to "no job links anything" rather than failing the poll,
// the never-fail-the-whole-poll-for-one-field contract jobQuestions and
// jobTokensIndex follow — the cost is every card's chips for one build, not the
// board.
func jobGoalLinks(st *store.Store) map[int64][]int64 {
	if st == nil {
		return nil
	}
	links, err := st.ListJobGoalLinks()
	if err != nil {
		util.LogEvent("web", "error", "goals", fmt.Sprintf("list job goal links: %v", err))
		return nil
	}
	return links
}

// linkedJobGoals resolves the chips for one board build: the goals the board's
// cards may need, keyed by id — the current period's, plus one by-id read per
// linked goal outside it.
//
// The GOALS table is the one this function refuses to read whole, and that is
// the distinction worth keeping straight: the link map above is read whole
// because it is small and every row of it is wanted, while goals only ever grows
// — carl writes new ones every week and deletes almost none — so reading it
// whole per build would cost more every month while the answer it feeds (one
// chip per link) stays the same handful of rows. What bounds the work here is
// the number of DISTINCT ids the links actually name.
//
// current is the pair the same build already assembled, and it is the SEED:
// most linked goals are in this week's or today's list, so those are already in
// hand and are not queried a second time. Only an id the current periods do not
// cover — a job still serving last week's goal, the case the whole any-period
// rule exists for — costs a GetGoal, and it costs exactly one for the whole
// board however many cards carry it.
//
// links is the link map (jobGoalLinks above), so the ids come from one query
// rather than one per job — and it covers the ARCHIVED cards too, which carry
// chips now, since it holds every link in the table rather than the active
// jobs' alone.
//
// A lookup that misses drops the chip rather than failing the poll, the same
// never-fail-the-whole-poll-for-one-field contract. ErrNotFound is not logged:
// schema v27's ON DELETE CASCADE takes the link rows with the goal, so it should
// be unreachable, and a dangling id is nothing an operator can act on. Any OTHER
// error is logged, because that one is real.
func linkedJobGoals(st *store.Store, current webGoals, links map[int64][]int64) map[int64]*webJobGoal {
	index := map[int64]*webJobGoal{}
	if st == nil {
		return index
	}
	for _, list := range [][]webGoal{current.Week, current.Day} {
		for _, g := range list {
			index[g.ID] = &webJobGoal{ID: g.ID, Text: g.Text, Done: g.Done}
		}
	}
	// tried, separate from index, so an id that resolves to nothing is queried
	// once for the whole board rather than once per job linking it.
	tried := map[int64]bool{}
	for jobID, ids := range links {
		for _, id := range ids {
			if id == 0 || index[id] != nil || tried[id] {
				continue
			}
			tried[id] = true
			g, err := st.GetGoal(id)
			if err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					util.LogEvent("web", "error", "goals", fmt.Sprintf("read goal %d for job %d: %v", id, jobID, err))
				}
				continue
			}
			index[g.ID] = webJobGoalFrom(*g)
		}
	}
	return index
}

// webJobGoalsFor turns one job's link ids into its chips, in link order,
// dropping any id the index could not resolve.
//
// It returns nil rather than an empty slice for a job with no links, since
// `goals` is omitempty and absence is the wire's only spelling of "serves no
// goal" — an empty ARRAY would be a second spelling of it.
func webJobGoalsFor(index map[int64]*webJobGoal, ids []int64) []webJobGoal {
	var out []webJobGoal
	for _, id := range ids {
		if g := index[id]; g != nil {
			out = append(out, *g)
		}
	}
	return out
}

// primaryJobGoal is the retained single `goal` key: the HEAD of the array,
// always, so the two can never disagree about which goal a card's one chip
// names. Nil when there are no goals at all, which is exactly when `goals`
// itself is absent — the two keys appear and vanish together.
//
// One function rather than a `goals[0]` at each call site: the promise is that
// `goal` IS `goals[0]`, and a promise about two keys has to be kept in the one
// place that builds both.
func primaryJobGoal(goals []webJobGoal) *webJobGoal {
	if len(goals) == 0 {
		return nil
	}
	head := goals[0]
	return &head
}

// goalWire is POST /api/goals' "add" payload. Period is optional — omitted
// means the CURRENT period for that scope, which is what every UI sends,
// since a panel showing this week's goals adds to this week. ParentID rides
// the add so the per-week-row "+ day goal…" affordance is one request rather
// than an add followed by an update.
type goalWire struct {
	Scope    string `json:"scope"`
	Period   string `json:"period,omitempty"`
	Text     string `json:"text"`
	ParentID int64  `json:"parent_id,omitempty"`
}

// goalsReq covers every POST /api/goals form. The update fields are POINTERS
// for store.GoalUpdate's reason: absent must stay different from explicitly
// zero, or every done-toggle would also blank the text and unnest the goal.
type goalsReq struct {
	Op   string    `json:"op"` // "add" | "update" | "remove"
	ID   int64     `json:"id,omitempty"`
	Goal *goalWire `json:"goal,omitempty"`
	// update-only fields, read only when Op is "update"
	Text     *string `json:"text,omitempty"`
	Done     *bool   `json:"done,omitempty"`
	ParentID *int64  `json:"parent_id,omitempty"`
}

// jobGoalReq is POST /api/job/goal's body: which job, which goal, and which
// way — `op` "add" or "remove" — with goal_id 0 the legacy CLEAR-ALL shape,
// whatever op says.
//
// A per-goal op rather than a full-replace list of ids, and that is the whole
// design decision on this route: a client sends what it wants CHANGED, never
// the set it believes is current, so a client that has not seen a link (an
// older build, a stale board, another window's write a second ago) cannot
// silently unlink it. It is the same stale-client wipe the one-field route
// exists to avoid, which a `goal_ids: [...]` body would reintroduce exactly.
//
// op is deliberately absent from the CLEAR: goal_id 0 was the pre-multi-goal
// wire's unlink, shipped iOS builds and any older board still send exactly
// that, and answering it with a 400 over a missing op would break them for
// nothing — 0 names no goal, so there is nothing for an op to disambiguate.
type jobGoalReq struct {
	Slug   string `json:"slug"`
	GoalID int64  `json:"goal_id"`
	Op     string `json:"op,omitempty"`
}

// goalsHandler serves GET (every client's read) and POST (carl's writes).
// POST is mirror-gated INLINE — the gate cannot go on the route, since the
// GET must stay reachable — with the same plain-text body every gated route
// uses (mirror_test.go asserts the exact string across all of them). Same
// policy as questions/feedback/load: a mirror board is read-only, full stop.
func goalsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		goalsGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		goalsPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// goalsGet serves {"goals":{"week":[…],"day":[…]}} for the current local
// periods, plus "job_goals" (and its head, "job_goal") when ?slug= names a job
// that links at least one.
//
// Both are OMITTED rather than null for an unlinked job, unlike
// /api/job/load's explicit null: this payload's subject is the goals, and the
// slug is a rider on it, so the keys' presence is exactly the question the
// caller asked ("does this job serve any goal") with no third state to express.
//
// job_goals is the array and job_goal is its FIRST element, always — the same
// pairing webJob.Goals/webJob.Goal carries on the board payload, kept here by
// the same one function (primaryJobGoal) so the two spellings cannot drift.
// job_goal is retained rather than dropped because shipped iOS builds read it
// and the array is additive.
func goalsGet(w http.ResponseWriter, r *http.Request) {
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	resp := map[string]any{"goals": currentGoals(st)}
	if slug := r.URL.Query().Get("slug"); slug != "" {
		j, _, err := webJobFor(slug)
		if err != nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		// One by-id read per link — GetGoal is frozen for exactly this: a linked
		// goal is deliberately NOT restricted to the current periods or to
		// undone goals (the link is carl's statement about what this job is FOR,
		// and it must keep reading after the week rolls over or the goal is
		// ticked off), so it cannot be answered from the two lists above. The
		// count is the job's own link count — a handful — not the board's, since
		// this route answers about one job.
		var linked []webJobGoal
		for _, id := range j.GoalIDs {
			g, err := st.GetGoal(id)
			switch {
			case errors.Is(err, store.ErrNotFound):
				// The link points at a row that is gone. Unreachable behind
				// schema v27's ON DELETE CASCADE, which takes the link row with
				// the goal, and the honest answer either way is the same as an
				// unlinked job: one fewer entry, and no key at all if it was the
				// only one.
			case err != nil:
				// NOT degraded to "no linked goal": the caller named one job and
				// asked a yes/no question about it, so answering "no" from a
				// failed read would be a wrong answer rather than a missing one.
				util.LogEvent("web", "error", "goals", fmt.Sprintf("read goal %d for job %s: %v", id, j.Slug, err))
				webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			default:
				linked = append(linked, *webJobGoalFrom(*g))
			}
		}
		if len(linked) > 0 {
			resp["job_goals"] = linked
			resp["job_goal"] = primaryJobGoal(linked)
		}
	}
	webJSON(w, http.StatusOK, resp)
}

// goalsPost dispatches add/update/remove. Store validation errors (a bad
// scope, a week period that is not a Monday, an empty text, a parent that is
// not a week goal) surface as 400 carrying the store's own sentence — the
// panel shows it inline and `kraken goals` would print it to stderr, so it is
// written to be read by whoever typed the goal.
func goalsPost(w http.ResponseWriter, r *http.Request) {
	// Body cap like every other POST in this package (peers.go, feedback.go,
	// questions.go, jobcolor.go): the whole legitimate request is one short
	// line of text, so an oversize one is refused with 413 rather than read
	// into memory, and that stays distinct from a decode error's 400.
	var req goalsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	action := "goals-" + req.Op
	// subject names WHICH goal this write touched, so the events table reads
	// "goals-update 41" rather than "goals-update" repeated — the `<event>
	// <subject>` shape every sibling logs (questions.go logs the slug,
	// jobGoalHandler below logs slug -> id). Set per op before the write, and
	// upgraded to the assigned id afterwards on an add, which has no id to
	// name until the row exists.
	subject := ""
	now := time.Now().UnixMilli()
	switch req.Op {
	case "add":
		if req.Goal == nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing goal"})
			return
		}
		g := store.GoalAdd{
			Scope:    req.Goal.Scope,
			Period:   strings.TrimSpace(req.Goal.Period),
			Text:     req.Goal.Text,
			ParentID: req.Goal.ParentID,
		}
		if g.Period == "" {
			// The default every UI relies on: a panel showing this week adds to
			// this week. Resolved from the SAME GoalPeriods call the GET uses,
			// so an add issued from a board that has been open since Sunday
			// lands in the period the panel is actually showing.
			week, day := store.GoalPeriods(time.Now())
			switch g.Scope {
			case "week":
				g.Period = week
			case "day":
				g.Period = day
			}
			// A scope that is neither falls through with an empty period and is
			// refused by AddGoal's own scope check, naming the scope rather
			// than the period it never had.
		}
		// Before the write there is no id to name, so a refused add is logged
		// by what was asked for. The text is echoed through core.TruncateOneLine
		// — one bounded, one-lined echo, jobcolor.go's rule — since it comes
		// from a request body and an unbounded copy would let a caller choose
		// the size of a line in the events table.
		subject = fmt.Sprintf("%s %q", g.Scope, core.TruncateOneLine(g.Text, goalLogTextMax))
		created, err := st.AddGoal(g, now)
		if err != nil {
			util.LogEvent("web", "error", action, action+" "+subject+": "+err.Error())
			webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		subject = fmt.Sprintf("%d (%s %s)", created.ID, created.Scope, created.Period)
	case "update":
		if req.ID == 0 {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
			return
		}
		subject = fmt.Sprintf("%d", req.ID)
		upd := store.GoalUpdate{Text: req.Text, Done: req.Done, ParentID: req.ParentID}
		// Every store refusal on this route is a 400 carrying the store's own
		// sentence — an unknown id included, which reads as "goal 41 not
		// found" rather than as an empty 404 body. One status for the whole
		// write surface: the caller is a panel that renders the message
		// inline, and a 404 would send it down a not-a-route path for a
		// request that reached exactly the right route.
		if err := st.UpdateGoal(req.ID, upd, now); err != nil {
			util.LogEvent("web", "error", action, action+" "+subject+": "+err.Error())
			webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	case "remove":
		// A missing id is not refused here: DeleteGoal is a no-op on an absent
		// row by contract, and id 0 is simply an absent row. A double-clicked
		// delete affordance is not a failure.
		subject = fmt.Sprintf("%d", req.ID)
		if err := st.DeleteGoal(req.ID); err != nil {
			util.LogEvent("web", "error", action, action+" "+subject+": "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized goals request — need op add, update or remove"})
		return
	}
	util.LogEvent("web", "info", action, action+" "+subject)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// jobGoalHandler links, unlinks, or clears the goals a job serves. POST-only,
// so unlike goalsHandler there is no GET to keep reachable — the links ride
// back on the next /api/board poll, already the board's single source of truth
// for job state, exactly as /api/job/color's colour does. The mirror gate is
// still inline rather than a mirrorGated wrapper at registration, so a caller
// reaching the handler directly still hits it.
//
// Three shapes, and the order they are tested in is the contract: goal_id 0 is
// the CLEAR whatever `op` says (the pre-multi-goal wire, still spoken by
// shipped clients); otherwise `op` decides, and an absent or unrecognised one
// is a 400 naming the two that work rather than a guess — defaulting to "add"
// would make a typo'd remove silently link the goal it meant to drop.
func jobGoalHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if mirrorModeOn() {
		http.Error(w, "disabled in mirror mode", http.StatusForbidden)
		return
	}
	var req jobGoalReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	op := req.Op
	var write func() error
	switch {
	case req.GoalID == 0:
		op = "clear"
		write = func() error { return st.ClearJobGoals(j.ID) }
	case req.Op == "add":
		write = func() error { return st.AddJobGoal(j.ID, req.GoalID) }
	case req.Op == "remove":
		write = func() error { return st.RemoveJobGoal(j.ID, req.GoalID) }
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unrecognized job-goal op %q — need add or remove (or goal_id 0 to clear)", req.Op),
		})
		return
	}
	if err := write(); err != nil {
		util.LogEvent("web", "error", "job-goal", "job-goal "+req.Slug+": "+err.Error())
		// An unknown goal is the caller's mistake (a stale picker, a typo'd
		// id) and comes back as a 400 with the store's own sentence naming
		// the id. ErrNotFound here means the JOB vanished between webJobFor
		// and the write — a race, not a bad request — so it keeps the 404
		// the lookup itself would have given.
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		webJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "job-goal", fmt.Sprintf("job-goal %s %s %d", req.Slug, op, req.GoalID))
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
