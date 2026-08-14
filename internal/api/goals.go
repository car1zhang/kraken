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
// daily goals (store/goals.go) and the one goal a job optionally serves.
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
// a job with the goal it serves is most useful precisely while its cell is
// LIVE, so routing it through the form would put the feature out of reach
// exactly where it matters. The full-replace blast radius argument applies
// identically: a form save from a client predating the field would clear it.

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

// linkedJobGoals resolves the chips for one board build: the goals the board's
// cards may need, keyed by id — the current period's, plus one by-id read per
// linked goal outside it.
//
// It is scoped to the demand rather than to the table on purpose. A board
// rebuilds roughly every two seconds for as long as kraken serve is up, and
// the goals table only ever grows — carl writes new goals every week and
// deletes almost none — so reading it whole per build would cost more every
// month while the answer it feeds (one chip per labelled card) stays the same
// handful of rows. What bounds the work here instead is the number of DISTINCT
// linked ids on the board, which is at most the number of jobs and in practice
// a few.
//
// current is the pair the same build already assembled, and it is the SEED:
// most labelled jobs point at a goal that is in this week's or today's list,
// so those are already in hand and are not queried a second time. Only an id
// the current periods do not cover — a job still serving last week's goal, the
// case the whole any-period rule exists for — costs a GetGoal.
//
// A lookup that misses drops the chip rather than failing the poll, the same
// never-fail-the-whole-poll-for-one-field contract jobQuestions and
// jobTokensIndex follow. ErrNotFound is not logged: schema v23's ON DELETE SET
// NULL means it should be unreachable, and a job holding a dangling id is
// nothing an operator can act on. Any OTHER error is logged, because that one
// is real.
func linkedJobGoals(st *store.Store, current webGoals, js []*jobs.Job) map[int64]*webJobGoal {
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
	// once for the whole board rather than once per job pointing at it.
	tried := map[int64]bool{}
	for _, j := range js {
		if j.GoalID == 0 || index[j.GoalID] != nil || tried[j.GoalID] {
			continue
		}
		tried[j.GoalID] = true
		g, err := st.GetGoal(j.GoalID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				util.LogEvent("web", "error", "goals", fmt.Sprintf("read goal %d for job %s: %v", j.GoalID, j.Slug, err))
			}
			continue
		}
		index[g.ID] = webJobGoalFrom(*g)
	}
	return index
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

// jobGoalReq is POST /api/job/goal's body: which job, which goal, 0 to
// unlink. Two fields and no more — this is the colour route's shape, and for
// the same reason.
type jobGoalReq struct {
	Slug   string `json:"slug"`
	GoalID int64  `json:"goal_id"`
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
// periods, plus "job_goal" when ?slug= names a job that links one.
//
// job_goal is OMITTED rather than null for an unlinked job, unlike
// /api/job/load's explicit null: this payload's subject is the goals, and the
// slug is a rider on it, so the key's presence is exactly the question the
// caller asked ("does this job serve a goal") with no third state to express.
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
		// One by-id read — GetGoal is frozen for exactly this: the linked goal
		// is deliberately NOT restricted to the current periods or to undone
		// goals (the link is carl's statement about what this job is FOR, and
		// it must keep reading after the week rolls over or the goal is ticked
		// off), so it cannot be answered from the two lists above.
		if j.GoalID != 0 {
			g, err := st.GetGoal(j.GoalID)
			switch {
			case errors.Is(err, store.ErrNotFound):
				// The link points at a row that is gone. Unreachable behind
				// schema v23's ON DELETE SET NULL, and the honest answer either
				// way is the same as an unlinked job: no key.
			case err != nil:
				// NOT degraded to "no linked goal": the caller named one job and
				// asked a yes/no question about it, so answering "no" from a
				// failed read would be a wrong answer rather than a missing one.
				util.LogEvent("web", "error", "goals", fmt.Sprintf("read goal %d for job %s: %v", j.GoalID, j.Slug, err))
				webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			default:
				resp["job_goal"] = webJobGoalFrom(*g)
			}
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

// jobGoalHandler sets (or clears) the one goal a job serves. POST-only, so
// unlike goalsHandler there is no GET to keep reachable — the link rides back
// on the next /api/board poll, already the board's single source of truth for
// job state, exactly as /api/job/color's colour does. The mirror gate is
// still inline rather than a mirrorGated wrapper at registration, so a caller
// reaching the handler directly still hits it.
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
	if err := st.SetJobGoal(j.ID, req.GoalID); err != nil {
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
	util.LogEvent("web", "info", "job-goal", fmt.Sprintf("job-goal %s -> %d", req.Slug, req.GoalID))
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
