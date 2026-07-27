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

// watchlist.go — the dream loop's watchlist over HTTP. Two POST routes, not
// one, because the `watchlist` table has two owners and store/watchlist.go
// enforces the split at the SQL level (UpdateWatchItem never writes
// verdict/comment/commented_at; SetWatchVerdict never writes anything else):
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
// Unlike questions and feedback, watchlist rows are GLOBAL — no job_id, no
// slug on any of these requests (see store/watchlist.go's header).

// watchClasses / watchStates are the value sets store/watchlist.go documents
// for the class and state columns. Validated here rather than left to SQL
// (the column is a plain TEXT with no CHECK), so a typo'd class from an LLM
// caller is a loud 400 instead of a row that silently never matches a filter.
var watchClasses = []string{"feedback", "correction", "repetition", "waste", "prompting"}

var watchStates = []string{"watching", "shipped", "declined"}

// watchVerdicts are the values carl's verdict may take. "" is the documented
// way to CLEAR a verdict (store.SetWatchVerdict writes all three columns
// unconditionally), so it is a legal value, not a missing one.
var watchVerdicts = []string{"", "fine", "fix"}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// webWatchItem is one `watchlist` row on the wire — the whole row, both
// halves of the ownership split, since every reader (the board's DREAM view,
// the nightly pass deciding whether an observation is a repeat) needs to see
// the agent's observation and carl's verdict together. Its own DTO rather
// than store.WatchItem directly, for the same reason webJob never embeds
// jobs.Job. Additive endpoint — no apiContract bump.
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
// judgement of one row, and nothing else. Every agent-owned column is absent
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
var agentOwnedWatchKeys = []string{
	"observation", "class", "state", "sightings", "first_seen", "last_seen",
	"evidence", "suggested_fix", "pr_url", "updated_at",
}

// carlOwnedWatchKeys are the mirror image: the keys POST /api/watchlist/item
// refuses. store.UpdateWatchItem already PRESERVES these three columns
// whatever it is handed, so honoring them is impossible — which is precisely
// why they must be refused loudly instead of dropped on the floor.
var carlOwnedWatchKeys = []string{"verdict", "comment", "commented_at"}

// rejectKeys reports the first key of set present in raw, or "" if none is.
// Operates on the raw JSON object rather than the decoded struct because the
// whole point is to catch keys the struct deliberately does not model.
func rejectKeys(raw map[string]json.RawMessage, set []string) string {
	for _, k := range set {
		if _, ok := raw[k]; ok {
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

// watchlistGet lists rows, newest-observation-first (store.ListWatchItems'
// last_seen DESC, id DESC), optionally filtered to one state. An absent or
// empty `state` means EVERY row — the filter is a narrowing, not a required
// argument. An unrecognized state is a 400 rather than an empty list: a
// typo'd filter and a genuinely empty watchlist are different answers, and
// the board would render both as "nothing to review".
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

// watchlistPost is carl's verdict write: SetWatchVerdict, with commented_at
// stamped SERVER-side in RFC3339 (never accepted from the client — a
// timestamp a caller can choose is not a record of when anything happened).
// An empty verdict AND an empty comment clears the judgement outright,
// commented_at included, which is store.SetWatchVerdict's documented undo.
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
	commentedAt := ""
	if verdict != "" || comment != "" {
		commentedAt = time.Now().UTC().Format(time.RFC3339)
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	if err := st.SetWatchVerdict(req.ID, verdict, comment, commentedAt); err != nil {
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

// watchItemWire is the agent's view of a row: the agent-owned columns only.
// There is deliberately no verdict/comment/commented_at field — see
// carlOwnedWatchKeys.
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
// row (both ownership halves) and is what both the board and the agent read.
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
		webJSON(w, http.StatusBadRequest, map[string]string{"error": bad + " is carl's, not yours — the store preserves it on every agent write; judge a row from the board instead"})
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
				webJSON(w, http.StatusBadRequest, map[string]string{"error": "item." + bad + " is carl's, not yours — drop it; the store preserves it across your writes"})
				return
			}
		}
		watchItemUpsert(w, st, req)
	case "remove":
		if req.ID <= 0 {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
			return
		}
		if err := st.DeleteWatchItem(req.ID); err != nil {
			util.LogEvent("web", "error", "watch-remove", "watch-remove: "+err.Error())
			if errors.Is(err, store.ErrNotFound) {
				webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
				return
			}
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		util.LogEvent("web", "info", "watch-remove", "watch-remove "+strconv.FormatInt(req.ID, 10))
		webJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized watchlist request — need op add|update|remove"})
	}
}

// watchItemUpsert runs the add and update halves of the agent's write.
//
// The update half re-reads the existing row first and back-fills every
// agent-owned field the request left at its zero value from it. That is not
// convenience: store.UpdateWatchItem is a full-row restatement of the
// agent-owned columns, so an update that only meant to change
// suggested_fix would otherwise zero first_seen, last_seen and sightings —
// silently destroying the two things the whole table exists to record (when
// a pattern was first seen, and how often). A row's history is only ever
// added to by an agent write, never reset by one.
func watchItemUpsert(w http.ResponseWriter, st *store.Store, req watchItemReq) {
	it := *req.Item
	it.Observation = strings.TrimSpace(it.Observation)
	it.Class = strings.TrimSpace(it.Class)
	it.State = strings.TrimSpace(it.State)
	if it.Observation == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "observation is empty"})
		return
	}
	if !oneOf(it.Class, watchClasses) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad class " + strconv.Quote(it.Class) + " — want one of " + strings.Join(watchClasses, "|")})
		return
	}
	if it.State != "" && !oneOf(it.State, watchStates) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad state " + strconv.Quote(it.State) + " — want one of " + strings.Join(watchStates, "|")})
		return
	}
	row := store.WatchItem{
		Observation: it.Observation, Class: it.Class, State: it.State,
		Sightings: it.Sightings, FirstSeen: it.FirstSeen, LastSeen: it.LastSeen,
		Evidence: it.Evidence, SuggestedFix: it.SuggestedFix, PRURL: it.PRURL,
	}
	if req.Op == "add" {
		// AddWatchItem fills the rest: sightings 1, first/last seen and
		// updated_at now, state "watching" — and never touches carl's trio.
		id, err := st.AddWatchItem(row)
		if err != nil {
			util.LogEvent("web", "error", "watch-add", "watch-add: "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		util.LogEvent("web", "info", "watch-add", "watch-add "+strconv.FormatInt(id, 10)+" "+it.Class)
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
		return
	}
	if it.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing item id"})
		return
	}
	existing, err := st.GetWatchItem(it.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if row.State == "" {
		row.State = existing.State
	}
	if row.Sightings == 0 {
		row.Sightings = existing.Sightings
	}
	if row.FirstSeen == 0 {
		row.FirstSeen = existing.FirstSeen
	}
	if row.LastSeen == 0 {
		row.LastSeen = existing.LastSeen
	}
	if err := st.UpdateWatchItem(it.ID, row); err != nil {
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
