package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// watchlist.go — the dream loop's watchlist over HTTP. A watch item is a row
// in its OWN table (`watch_items`, internal/store/watch.go): the observation
// is the row's own sentence, and the row carries the dream-loop workflow
// (class, state, sightings, first/last seen, evidence, suggested_fix, pr_url)
// beside carl's judgement trio. The ids on this route are watch-row ids.
//
// It used to be a graph EDGE with a thin `kg_watch` overlay, and Q9 moved it
// back out. The reason is what the mental model did to the graph: after the
// cutover the graph is compiled from carl's own marks rather than from
// anything an agent observed, so a nightly pass' observation — which is
// exactly an agent's observation — no longer belongs in it. A watch item is
// dream-loop workflow state, and workflow state with its own lifecycle is a
// table, not a fact.
//
// What that cost, stated plainly because it was the point of the edge era:
// an observation stopped being retrievable through the graph's search and
// stopped riding into a cell's spawn brief (and v31 then removed the graph
// altogether). Nothing replaces that, and nothing is meant to — the watchlist is read by the nightly pass and by carl on the
// DREAM view, both of which address it directly.
//
// Two POST routes, not one, because the row has two owners and
// store/watch.go enforces the split at the SQL level (UpdateWatchItem never
// writes verdict/comment/commented_at; SetWatchVerdict never writes anything
// else):
//
//   - POST /api/watchlist       — CARL's write: verdict + comment, nothing
//     else. This is the board's DREAM view judging a row.
//   - POST /api/watchlist/item  — the AGENT's write: add/update/remove of the
//     observation itself, reached only through the `kraken watchlist` cell
//     verb.
//
// Collapsing them into one endpoint with a fat request body would put the
// store's carefully separated column ownership back into a single struct and
// leave "which caller may set which key" as a convention rather than a shape.
// Keeping them apart means carl's surface literally cannot express an
// observation edit, and the agent's literally cannot express a verdict — each
// wrong-side key is rejected with a 400 that names the other route, so a
// confused agent is told rather than silently having its write dropped.
//
// Unlike questions and feedback, watch items are GLOBAL — no job_id, no slug
// on any of these requests. An observation outlives whichever job first
// surfaced it.

// WatchClasses / watchStates are the value sets store/watch.go documents for
// the class and state columns. Validated here rather than left to SQL (the
// column is a plain TEXT with no CHECK), so a typo'd class from an LLM caller
// is a loud 400 instead of a row that silently never matches a filter.
var WatchClasses = []string{"feedback", "correction", "repetition", "waste", "prompting"}

var watchStates = []string{"watching", "shipped", "declined"}

// watchVerdicts are the values carl's verdict may take. "" is the documented
// way to CLEAR a verdict (store.SetWatchVerdict writes all three columns
// unconditionally), so it is a legal value, not a missing one.
var watchVerdicts = []string{"", "fine", "fix"}

// watchDefaultPredicate is the predicate an add gets when it names none. The
// endpoints of a watch observation are entity names and the sentence carries
// the actual claim, so the predicate is the least informative part of the
// triple — requiring the caller to invent one would buy nothing. It is
// defaulted HERE rather than in the store because it is a policy about what an
// agent may leave out, not a property of the column.
const watchDefaultPredicate = "concerns"

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// watchOneLine collapses internal whitespace, newlines included, to single
// spaces. It is what makes re-stating an observation with different wrapping
// FOLD onto the existing row instead of adding a near-duplicate: the store's
// fold compares the stored `Observation` byte for byte, so the normalization
// has to happen before the value is ever stored, on every write, or the same
// sentence typed twice would be two rows.
//
// It also keeps every observation on ONE line, which every line-oriented
// reader downstream (the report's tables, `kraken watchlist get`'s JSON read
// by an agent) depends on.
func watchOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// webWatchItem is one watch item on the wire — the whole thing, both halves of
// the ownership split, since every reader (the board's DREAM view, the nightly
// pass deciding whether an observation is a repeat) needs to see the agent's
// observation and carl's verdict together. Its own DTO rather than
// store.WatchItem directly, for the same reason webJob never embeds jobs.Job.
//
// THE KEYS ARE FROZEN, across both moves this row has now made: `id` is the
// watch_items row id again, but no key was added, renamed or retyped in either
// direction, so the web DREAM view and the iOS app decode this unchanged and
// `apiContract` does not bump. src/dst/predicate deliberately stay OFF this
// wire, and nothing on either client renders them.
type webWatchItem struct {
	ID          int64  `json:"id"`
	Observation string `json:"observation"`
	Class       string `json:"class"`
	State       string `json:"state"`
	Sightings   int64  `json:"sightings"`
	FirstSeen   int64  `json:"first_seen"` // unix millis
	LastSeen    int64  `json:"last_seen"`  // unix millis
	// Evidence is a local-only JSON sample of the most recent occurrence — it
	// never enters git (the dream loop's load-bearing destination rule), but it
	// does ride this route, which is localhost/tailnet only, same as every
	// other read here.
	Evidence     string `json:"evidence,omitempty"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	UpdatedAt    int64  `json:"updated_at"`
	// Verdict/Comment/CommentedAt are carl-owned — omitempty, since an
	// unjudged row genuinely has none of the three.
	Verdict     string `json:"verdict,omitempty"`
	Comment     string `json:"comment,omitempty"`
	CommentedAt string `json:"commented_at,omitempty"` // RFC3339
}

// webWatchItemsFrom converts store rows into the wire DTO. nil in, nil out —
// the handler turns that into an empty array so the wire never carries null.
func webWatchItemsFrom(ws []store.WatchItem) []webWatchItem {
	if len(ws) == 0 {
		return nil
	}
	out := make([]webWatchItem, len(ws))
	for i, w := range ws {
		out[i] = webWatchItem{
			ID: w.ID, Observation: w.Observation, Class: w.Class, State: w.State,
			Sightings: w.Sightings, FirstSeen: w.FirstSeen, LastSeen: w.LastSeen,
			Evidence: w.Evidence, SuggestedFix: w.SuggestedFix, PRURL: w.PRURL,
			UpdatedAt: w.UpdatedAt, Verdict: w.Verdict, Comment: w.Comment,
			CommentedAt: w.CommentedAt,
		}
	}
	return out
}

// watchVerdictReq is POST /api/watchlist's whole request body: carl's
// judgement of one item, and nothing else. Every agent-owned column is absent
// BY CONSTRUCTION — there is no field to set — and watchlistPost additionally
// refuses a request that mentions one by name (see agentOwnedWatchKeys).
type watchVerdictReq struct {
	ID      int64  `json:"id"`
	Verdict string `json:"verdict"`
	Comment string `json:"comment,omitempty"`
}

// agentOwnedWatchKeys are the request keys POST /api/watchlist refuses
// outright. Silently ignoring them (encoding/json's default for unknown
// fields) would let a caller believe it had edited an observation when it had
// only stamped a verdict — the exact failure mode the two-route split exists
// to prevent, so it is named rather than dropped.
//
// src/dst/predicate are on the list because they describe the observation's
// endpoints, which is as far from carl's verdict as a key on this surface
// gets. `strength` is NOT: it was the edge's confidence and the standalone
// table has no such column, so refusing it would be refusing a key that no
// longer means anything anywhere.
var agentOwnedWatchKeys = []string{
	"observation", "class", "state", "sightings", "first_seen", "last_seen",
	"evidence", "suggested_fix", "pr_url", "updated_at",
	"src", "dst", "predicate",
}

// carlOwnedWatchKeys are the mirror image: the keys POST /api/watchlist/item
// refuses. store.UpdateWatchItem already PRESERVES these three columns
// whatever it is handed, so honoring them is impossible — which is precisely
// why they must be refused loudly instead of dropped on the floor.
var carlOwnedWatchKeys = []string{"verdict", "comment", "commented_at"}

// immutableWatchKeys are the keys op `update` refuses inside `item`. They are
// not carl's — they are the observation's IDENTITY. The store's fold matches a
// re-add byte for byte on `observation`, so a reworded sentence is a different
// item by construction, and `UpdateWatchItem` writes the workflow columns
// only. Accepting one of these keys just to ignore it would tell a caller its
// edit landed; the way to reword is to remove and re-add.
var immutableWatchKeys = []string{"observation", "src", "dst", "predicate"}

// rejectKeys reports the first key of set present in raw, or "" if none is.
// Operates on the raw JSON object rather than the decoded struct because the
// whole point is to catch keys the struct deliberately does not model.
//
// The comparison FOLDS CASE, and that is load-bearing rather than tidy:
// encoding/json matches struct fields case-insensitively, so `{"Observation":
// "..."}` populates exactly the field `{"observation": ...}` would. An exact
// lookup here would wave that spelling past the guard and then silently
// discard it downstream — the accepted-then-dropped failure all three of these
// key sets exist to prevent. Every legitimate key on this surface is
// lowercase, so folding refuses nothing a caller may legally send.
func rejectKeys(raw map[string]json.RawMessage, set []string) string {
	folded := make(map[string]struct{}, len(raw))
	for k := range raw {
		folded[strings.ToLower(k)] = struct{}{}
	}
	for _, k := range set {
		if _, ok := folded[k]; ok {
			return k
		}
	}
	return ""
}

// decodeWatchBody reads the request body once into both a raw key map (for
// the wrong-side-key checks) and, via a second pass, the caller's typed
// struct. An oversize body is 413, an unparseable one 400 — same shapes
// feedbackPost/questionsPost use.
func decodeWatchBody(w http.ResponseWriter, r *http.Request, into any) (map[string]json.RawMessage, bool) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return nil, false
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return nil, false
	}
	// re-marshal the already-validated object rather than reading the body
	// twice (it is consumed) — raw is a faithful copy of what arrived.
	buf, err := json.Marshal(raw)
	if err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return nil, false
	}
	if err := json.Unmarshal(buf, into); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return nil, false
	}
	return raw, true
}

// watchlistHandler serves GET (the board's and the nightly pass' read) and
// POST (carl's verdict) /api/watchlist. POST is mirror-gated inline — the
// same shape feedback's and questions' handlers use, since GET must stay
// reachable — with the exact plain-text body mirror_test.go asserts across
// every gated route.
func watchlistHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		watchlistGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		watchlistPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// watchlistGet lists LIVE items, newest-observation-first, optionally filtered
// to one state. An absent or empty `state` means EVERY item — the filter is a
// narrowing, not a required argument. An unrecognized state is a 400 rather
// than an empty list: a typo'd filter and a genuinely empty watchlist are
// different answers, and the board would render both as "nothing to review".
//
// An EXPIRED item is absent by construction (store.ListWatchItems returns live
// rows only). Expiry is what `remove` does now, and it is a close rather than
// a delete: the row stays readable by id forever, so nothing carl judged is
// ever actually gone — it just stops asking for attention.
func watchlistGet(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state != "" && !oneOf(state, watchStates) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad state " + strconv.Quote(state) + " — want one of " + strings.Join(watchStates, "|") + " (or omit for all)"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	ws, err := st.ListWatchItems(state)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := webWatchItemsFrom(ws)
	if out == nil {
		out = []webWatchItem{}
	}
	webJSON(w, http.StatusOK, map[string]any{"watchlist": out})
}

// watchLookup resolves an id to a real watch item, answering the 404 itself.
// requireLive additionally refuses an EXPIRED row: judging or editing one
// would write into something no reader can see (ListWatchItems is live-only),
// which is the worst shape a 200 can have. `remove` passes false, because
// re-closing a closed row is exactly the idempotent success
// store.ExpireWatchItem documents.
func watchLookup(w http.ResponseWriter, st *store.Store, id int64, requireLive bool) bool {
	item, err := st.GetWatchItem(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
			return false
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return false
	}
	if requireLive && item.ExpiredAt != 0 {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
		return false
	}
	return true
}

// watchlistPost is carl's verdict write: SetWatchVerdict, with commented_at
// stamped SERVER-side in RFC3339 (never accepted from the client — a
// timestamp a caller can choose is not a record of when anything happened,
// which is why the store stamps it rather than taking it as a parameter).
// An empty verdict AND an empty comment clears the judgement outright, which
// is store.SetWatchVerdict's documented undo.
func watchlistPost(w http.ResponseWriter, r *http.Request) {
	var req watchVerdictReq
	raw, ok := decodeWatchBody(w, r, &req)
	if !ok {
		return
	}
	if bad := rejectKeys(raw, agentOwnedWatchKeys); bad != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": bad + " is agent-owned — POST /api/watchlist takes only {id, verdict, comment}; use `kraken watchlist update` for the observation itself"})
		return
	}
	if req.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	verdict := strings.TrimSpace(req.Verdict)
	if !oneOf(verdict, watchVerdicts) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad verdict " + strconv.Quote(verdict) + " — want fine|fix (or empty to clear)"})
		return
	}
	comment := strings.TrimSpace(req.Comment)
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	if !watchLookup(w, st, req.ID, true) {
		return
	}
	if err := st.SetWatchVerdict(req.ID, verdict, comment); err != nil {
		util.LogEvent("web", "error", "watch-verdict", "watch-verdict: "+err.Error())
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "watch-verdict", "watch-verdict "+strconv.FormatInt(req.ID, 10)+" "+verdict)
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// watchItemWire is the agent's view of a watch item: the agent-owned fields
// only. There is deliberately no verdict/comment/commented_at field — see
// carlOwnedWatchKeys.
//
// Src/Dst/Predicate name the entities the observation runs between. They are
// meaningless on the other two ops and refused there: a fold already has
// endpoints, and an update cannot move them (see immutableWatchKeys).
//
// There is no `strength` any more — it was the EDGE's confidence, and the
// standalone table has no such column. A caller that still sends one is not
// refused for it, since decodeWatchBody ignores unmodelled keys; it is simply
// a word this surface no longer has a meaning for.
type watchItemWire struct {
	ID           int64  `json:"id,omitempty"`
	Observation  string `json:"observation,omitempty"`
	Class        string `json:"class,omitempty"`
	State        string `json:"state,omitempty"`
	Sightings    int64  `json:"sightings,omitempty"`
	FirstSeen    int64  `json:"first_seen,omitempty"`
	LastSeen     int64  `json:"last_seen,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	Src          string `json:"src,omitempty"`
	Dst          string `json:"dst,omitempty"`
	Predicate    string `json:"predicate,omitempty"`
}

// watchItemReq is POST /api/watchlist/item's body: an op plus either an item
// (add/update) or a bare id (remove) — the same {op, <payload>} dispatch
// shape questionsReq uses, for the same reason: one route, one cell verb, one
// place the ops are documented.
type watchItemReq struct {
	Op   string         `json:"op"` // "add" | "update" | "remove"
	Item *watchItemWire `json:"item,omitempty"`
	ID   int64          `json:"id,omitempty"`
}

// watchItemHandler serves POST /api/watchlist/item — the agent's write.
// Mirror-gated like every other write on this surface. GET is deliberately
// not served here: reads go through /api/watchlist, which serves the whole
// item (both ownership halves) and is what both the board and the agent read.
func watchItemHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if mirrorModeOn() {
		http.Error(w, "disabled in mirror mode", http.StatusForbidden)
		return
	}
	var req watchItemReq
	raw, ok := decodeWatchBody(w, r, &req)
	if !ok {
		return
	}
	if bad := rejectKeys(raw, carlOwnedWatchKeys); bad != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": bad + " is the operator's, not yours — the store preserves it on every agent write; judge a row from the board instead"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	switch req.Op {
	case "add", "update":
		if req.Item == nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing item"})
			return
		}
		// the carl-owned keys are also refused one level down, inside `item`:
		// the top-level check above can't see them there, and an LLM caller
		// that has just read a whole row back from GET /api/watchlist is
		// exactly the caller most likely to echo the whole thing straight back.
		var itemKeys map[string]json.RawMessage
		if err := json.Unmarshal(raw["item"], &itemKeys); err == nil {
			if bad := rejectKeys(itemKeys, carlOwnedWatchKeys); bad != "" {
				webJSON(w, http.StatusBadRequest, map[string]string{"error": "item." + bad + " is the operator's, not yours — drop it; the store preserves it across your writes"})
				return
			}
		}
		if req.Op == "add" {
			watchItemAdd(w, st, *req.Item)
			return
		}
		watchItemUpdate(w, st, *req.Item, itemKeys)
	case "remove":
		if req.ID <= 0 {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
			return
		}
		if !watchLookup(w, st, req.ID, false) {
			return
		}
		if err := st.ExpireWatchItem(req.ID, time.Now().UnixMilli()); err != nil {
			util.LogEvent("web", "error", "watch-remove", "watch-remove: "+err.Error())
			if errors.Is(err, store.ErrNotFound) {
				webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
				return
			}
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		util.LogEvent("web", "info", "watch-remove", "watch-remove "+strconv.FormatInt(req.ID, 10))
		// `unwatched` rather than a bare ok because "remove" means something
		// narrower than it reads: the row is CLOSED, not deleted — it leaves
		// every list and stays readable by id forever, carl's verdict on it
		// included. No watch row is ever destroyed.
		webJSON(w, http.StatusOK, map[string]bool{"ok": true, "unwatched": true})
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized watchlist request — need op add|update|remove"})
	}
}

// watchOverlayFrom projects the agent's wire item onto the store's row type.
func watchOverlayFrom(it watchItemWire) store.WatchItem {
	return store.WatchItem{
		Class: it.Class, State: it.State, Sightings: it.Sightings,
		FirstSeen: it.FirstSeen, LastSeen: it.LastSeen, Evidence: it.Evidence,
		SuggestedFix: it.SuggestedFix, PRURL: it.PRURL,
	}
}

// watchItemAdd is op `add`: the agent claiming it has observed something.
//
// Two outcomes, and the store decides which: a live row already stating this
// same observation FOLDS (sightings+1, last_seen bumped, the workflow fields
// the caller restated applied, carl's trio untouched), and anything else is a
// new row. The fold is the signal separating a one-off from a recurring
// pattern, which is the whole reason the nightly pass has a watchlist at all.
//
// The PROMOTE branch is gone with the edge model: there is no longer a
// separate population of facts an observation could be adopted from, so
// "already stated somewhere else" is not a state that exists.
//
// src/dst are OPTIONAL now, and that is a real change rather than an omission.
// They were required because a new fact could not be inserted without two
// endpoints to hang it between; a `watch_items` row has no such requirement,
// and refusing an add for want of them would be enforcing a constraint that no
// longer exists anywhere below this line.
func watchItemAdd(w http.ResponseWriter, st *store.Store, it watchItemWire) {
	it.Observation = watchOneLine(it.Observation)
	it.Class = strings.TrimSpace(it.Class)
	it.State = strings.TrimSpace(it.State)
	it.Src = watchOneLine(it.Src)
	it.Dst = watchOneLine(it.Dst)
	it.Predicate = watchOneLine(it.Predicate)
	if it.Observation == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "observation is empty"})
		return
	}
	if !oneOf(it.Class, WatchClasses) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad class " + strconv.Quote(it.Class) + " — want one of " + strings.Join(WatchClasses, "|")})
		return
	}
	if it.State != "" && !oneOf(it.State, watchStates) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad state " + strconv.Quote(it.State) + " — want one of " + strings.Join(watchStates, "|")})
		return
	}
	predicate := it.Predicate
	if predicate == "" {
		predicate = watchDefaultPredicate
	}
	row := watchOverlayFrom(it)
	row.Observation = it.Observation
	row.Src, row.Dst, row.Predicate = it.Src, it.Dst, predicate
	id, folded, err := st.AddWatchItem(row)
	if err != nil {
		util.LogEvent("web", "error", "watch-add", "watch-add: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	verb := "watch-add "
	if folded {
		verb = "watch-fold "
	}
	util.LogEvent("web", "info", "watch-add", verb+strconv.FormatInt(id, 10)+" "+it.Class)
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "folded": folded})
}

// watchItemUpdate is op `update`: a partial write of the workflow columns, and
// nothing but those.
//
// store.UpdateWatchItem is already partial (COALESCE(NULLIF(...)) per column,
// one statement), so a request that restates only suggested_fix leaves
// sightings, first_seen and last_seen — the history the whole feature exists
// to record — untouched without the handler re-reading anything.
//
// The observation and its endpoints are refused rather than ignored: they are
// the row's identity (the fold matches on the sentence), so an accepted reword
// would silently split one tracked pattern into two.
func watchItemUpdate(w http.ResponseWriter, st *store.Store, it watchItemWire, itemKeys map[string]json.RawMessage) {
	if bad := rejectKeys(itemKeys, immutableWatchKeys); bad != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "item." + bad + " is the observation's identity, not its workflow — the fold matches on the sentence, so a reword is a different item; remove and re-add to reword"})
		return
	}
	it.Class = strings.TrimSpace(it.Class)
	it.State = strings.TrimSpace(it.State)
	if it.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing item id"})
		return
	}
	if it.Class != "" && !oneOf(it.Class, WatchClasses) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad class " + strconv.Quote(it.Class) + " — want one of " + strings.Join(WatchClasses, "|")})
		return
	}
	if it.State != "" && !oneOf(it.State, watchStates) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad state " + strconv.Quote(it.State) + " — want one of " + strings.Join(watchStates, "|")})
		return
	}
	if !watchLookup(w, st, it.ID, true) {
		return
	}
	if err := st.UpdateWatchItem(it.ID, watchOverlayFrom(it)); err != nil {
		util.LogEvent("web", "error", "watch-update", "watch-update: "+err.Error())
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "watch-update", "watch-update "+strconv.FormatInt(it.ID, 10))
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": it.ID})
}
