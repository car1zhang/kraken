package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// watchlist.go — the dream loop's watchlist over HTTP. A watch item is no
// longer a row in a flat table: it is a LIVE GRAPH EDGE (the observation IS
// the fact sentence) carrying a thin overlay row in `kg_watch`, keyed by edge
// id. store/watch.go is the whole surface, and the ids on this route are edge
// ids — which is why nothing here creates a row without also creating (or
// adopting) the fact it describes.
//
// What that buys, and the reason for the move: an observation the nightly
// pass is tracking becomes searchable by the same FTS/vector retrieval every
// other fact is, and gets injected into future cells' graphContext with zero
// extra plumbing. The overlay holds only what the graph deliberately has no
// verbs for — the dream-loop workflow (class, state, sightings, first/last
// seen, evidence, suggested_fix, pr_url) and carl's judgement trio.
//
// Two POST routes, not one, because the overlay has two owners and
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
// surfaced it, which is exactly why it belongs in the graph.

// watchClasses / watchStates are the value sets store/watch.go documents for
// the overlay's class and state columns. Validated here rather than left to
// SQL (the column is a plain TEXT with no CHECK), so a typo'd class from an
// LLM caller is a loud 400 instead of a row that silently never matches a
// filter.
var watchClasses = []string{"feedback", "correction", "repetition", "waste", "prompting"}

var watchStates = []string{"watching", "shipped", "declined"}

// watchVerdicts are the values carl's verdict may take. "" is the documented
// way to CLEAR a verdict (store.SetWatchVerdict writes all three columns
// unconditionally), so it is a legal value, not a missing one.
var watchVerdicts = []string{"", "fine", "fix"}

// watchDefaultPredicate is the predicate an add gets when it names none. The
// endpoints of a watch fact are real entities and the sentence carries the
// actual claim, so the predicate is the least informative part of the triple —
// requiring the caller to invent one would buy nothing.
const watchDefaultPredicate = "concerns"

// watchEpisodeRef is the ref every API-minted watch episode carries. The
// episode's dedupe hash covers its CONTENT as well as kind and ref (the
// addEpisode rule), so a shared ref does not collapse two different
// observations — it only records where they came from.
const watchEpisodeRef = "watch:api"

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// watchOneLine is the stored-text normalization the graph applies to every
// fact (normalizeGraphAdd's oneLine): internal whitespace, newlines included,
// collapsed to single spaces. A watch observation becomes a fact sentence, so
// it must arrive on that same shape — core.Triples emits one fact per line,
// and a fact carrying a newline breaks every line-oriented reader downstream.
//
// It is also the matching rule: store.FindWatchItem and
// store.FindLiveEdgeByFact both compare LOWER(oneLine(...)) against
// LOWER(fact), which is what makes re-stating an observation with different
// wrapping fold onto the existing edge instead of minting a near-duplicate.
func watchOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// webWatchItem is one watch item on the wire — the whole thing, both halves of
// the ownership split, since every reader (the board's DREAM view, the nightly
// pass deciding whether an observation is a repeat) needs to see the agent's
// observation and carl's verdict together. Its own DTO rather than
// store.WatchItem directly, for the same reason webJob never embeds jobs.Job.
//
// THE KEYS ARE FROZEN to what they were when this rode a flat table: `id` is
// now the EDGE id and `observation` is that edge's fact sentence, but no key
// was added, renamed or retyped, so the web DREAM view and the iOS app decode
// this unchanged and `apiContract` does not bump. src/dst/predicate deliberately
// stay OFF this wire — a key added to an existing shape is a contract bump
// (graph.go's header states the policy), and the graph routes already expose
// linkage for anything that wants it.
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
			ID: w.EdgeID, Observation: w.Observation, Class: w.Class, State: w.State,
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
// src/dst/predicate/strength joined the list when the add payload gained them:
// they describe the EDGE an observation is, which is as far from carl's
// verdict as a key on this surface gets.
var agentOwnedWatchKeys = []string{
	"observation", "class", "state", "sightings", "first_seen", "last_seen",
	"evidence", "suggested_fix", "pr_url", "updated_at",
	"src", "dst", "predicate", "strength",
}

// carlOwnedWatchKeys are the mirror image: the keys POST /api/watchlist/item
// refuses. store.UpdateWatchItem already PRESERVES these three columns
// whatever it is handed, so honoring them is impossible — which is precisely
// why they must be refused loudly instead of dropped on the floor.
var carlOwnedWatchKeys = []string{"verdict", "comment", "commented_at"}

// immutableWatchKeys are the keys op `update` refuses inside `item`. They are
// not carl's — they are the EDGE's. An edge is insert-only outside the two
// sanctioned window mutations, so there is no store verb that could honor a
// reworded observation or a moved endpoint, and accepting the key only to
// ignore it would tell a caller its edit landed.
var immutableWatchKeys = []string{"observation", "src", "dst", "predicate", "strength"}

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

// watchlistGet lists items, newest-observation-first (store.ListWatchItems'
// last_seen DESC, edge_id DESC), optionally filtered to one state. An absent
// or empty `state` means EVERY item — the filter is a narrowing, not a
// required argument. An unrecognized state is a 400 rather than an empty list:
// a typo'd filter and a genuinely empty watchlist are different answers, and
// the board would render both as "nothing to review".
//
// An item whose edge has been INVALIDATED is absent by construction
// (ListWatchItems joins live edges only). That is the intended coupling: the
// graph's revision pass striking a fact through is also the DREAM view
// dropping it, with no second bookkeeping step to forget.
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
//
// `id` is an EDGE id, and a 404 covers both ways it can fail to name a watch
// item: no overlay row, or an overlay whose edge has been invalidated.
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
	// The liveness half of the 404 is checked here rather than left to
	// SetWatchVerdict: that verb reports ErrNotFound only for a MISSING
	// overlay, and an overlay riding an invalidated edge is a row the board
	// can no longer see — judging it would write into a fact kraken has
	// unlearned.
	if _, err := st.GetWatchItem(req.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

// watchItemWire is the agent's view of a watch item: the agent-owned fields
// only. There is deliberately no verdict/comment/commented_at field — see
// carlOwnedWatchKeys.
//
// Src/Dst/Predicate/Strength describe the EDGE an `add` mints when the
// observation is genuinely new. They are meaningless on the other two ops and
// refused there: a fold already has endpoints, and an update cannot move them
// (see immutableWatchKeys).
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
	Strength     int64  `json:"strength,omitempty"`
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
		if err := st.UnwatchEdge(req.ID); err != nil {
			util.LogEvent("web", "error", "watch-remove", "watch-remove: "+err.Error())
			if errors.Is(err, store.ErrNotFound) {
				webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
				return
			}
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		util.LogEvent("web", "info", "watch-remove", "watch-remove "+strconv.FormatInt(req.ID, 10))
		// `unwatched` rather than a bare ok because "remove" now means
		// something narrower than it used to: the OVERLAY is dropped, the fact
		// and its episode survive in the graph. A caller that meant to unlearn
		// the fact itself wants /api/graph/add's invalidate list.
		webJSON(w, http.StatusOK, map[string]bool{"ok": true, "unwatched": true})
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized watchlist request — need op add|update|remove"})
	}
}

// watchEpisodeContent renders a watch observation as episode text, in the
// exact format the retiring `sweepWatchlist` used — kept byte-identical
// because schema v16's migration wrote every historical row in it, and two
// spellings of the same provenance unit would read as two different sources
// to anything sweeping kg_episodes.
//
// Carl's verdict is absent by construction here: an episode is minted at ADD
// time, when the item is definitionally unjudged.
func watchEpisodeContent(class, state string, sightings int64, observation, suggestedFix string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "watchlist observation (%s, %s, seen %dx): %s", class, state, sightings, observation)
	if suggestedFix != "" {
		b.WriteString("\nsuggested fix: " + suggestedFix)
	}
	return b.String()
}

// watchOverlayFrom projects the agent's wire item onto the store's overlay
// type. Only the overlay columns ride: the fact sentence and its endpoints
// belong to the edge, not to this struct.
func watchOverlayFrom(it watchItemWire) store.WatchItem {
	return store.WatchItem{
		Class: it.Class, State: it.State, Sightings: it.Sightings,
		FirstSeen: it.FirstSeen, LastSeen: it.LastSeen, Evidence: it.Evidence,
		SuggestedFix: it.SuggestedFix, PRURL: it.PRURL,
	}
}

// watchItemAdd is op `add`: the agent claiming it has observed something.
//
// Three outcomes, probed in this order, and the order is the whole design:
//
//  1. FOLD — an already-watched live edge states this same observation. The
//     sighting is bumped into it, which is the signal separating a one-off
//     from a recurring pattern. src/dst/predicate/strength are IGNORED here:
//     the observation already has endpoints, and letting a re-add move them
//     would rewrite history on the strength of one night's phrasing.
//  2. PROMOTE — a live edge states it but nothing is watching it. That fact
//     was extracted from a transcript or a feedback note; the dream loop
//     noticing it is a reason to WATCH it, never a reason to mint a second
//     copy of the same sentence.
//  3. CREATE — nothing states it yet. Endpoints are upserted by name (the
//     graphAddHandler idiom: a name that resolves merges, a new one creates a
//     bare node), an episode is minted and closed immediately, and the edge is
//     inserted and then watched.
//
// The episode is marked done at creation on purpose: extracting it later would
// only mint a near-duplicate of the edge this handler just wrote from the same
// sentence.
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
	if !oneOf(it.Class, watchClasses) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad class " + strconv.Quote(it.Class) + " — want one of " + strings.Join(watchClasses, "|")})
		return
	}
	if it.State != "" && !oneOf(it.State, watchStates) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad state " + strconv.Quote(it.State) + " — want one of " + strings.Join(watchStates, "|")})
		return
	}

	// --- fold: this observation is already watched ---
	hit, err := st.FindWatchItem(it.Observation)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err == nil && hit != nil {
		if err := st.BumpWatchSighting(hit.EdgeID, time.Now().UnixMilli(), it.Evidence); err != nil {
			util.LogEvent("web", "error", "watch-add", "watch-fold: "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// The four workflow fields a re-add can legitimately restate ride
		// through UpdateWatchItem's partial semantics; everything else about
		// the item is either the edge's (immutable) or the bump's.
		if it.SuggestedFix != "" || it.PRURL != "" || it.Class != "" || it.State != "" {
			if err := st.UpdateWatchItem(hit.EdgeID, store.WatchItem{
				Class: it.Class, State: it.State,
				SuggestedFix: it.SuggestedFix, PRURL: it.PRURL,
			}); err != nil {
				util.LogEvent("web", "error", "watch-add", "watch-fold-update: "+err.Error())
				webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		util.LogEvent("web", "info", "watch-add", "watch-fold "+strconv.FormatInt(hit.EdgeID, 10)+" "+it.Class)
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": hit.EdgeID, "folded": true})
		return
	}

	// --- promote: the fact exists, nothing watches it yet ---
	edge, err := st.FindLiveEdgeByFact(it.Observation)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if edge != nil {
		if err := st.WatchEdge(edge.ID, watchOverlayFrom(it)); err != nil {
			util.LogEvent("web", "error", "watch-add", "watch-promote: "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		util.LogEvent("web", "info", "watch-add", "watch-promote "+strconv.FormatInt(edge.ID, 10)+" "+it.Class)
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": edge.ID, "folded": false})
		return
	}

	// --- create: a genuinely new fact ---
	//
	// Endpoints and strength are only required on THIS path, but they are
	// validated up front all the same: discovering halfway through that the
	// caller named no src would leave a minted episode with no edge attached
	// to it, and there is no transaction spanning these writes.
	if it.Src == "" || it.Dst == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "a new observation needs src and dst — a watch item is a fact between two entities; resolve real ones with `k graph search` rather than inventing names"})
		return
	}
	if it.Strength < graphStrengthMin || it.Strength > graphStrengthMax {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("strength %d out of range — want %d..%d, and send it explicitly rather than omitting it (an absent strength is not a default, it is an unstated confidence)", it.Strength, graphStrengthMin, graphStrengthMax)})
		return
	}
	predicate := it.Predicate
	if predicate == "" {
		predicate = watchDefaultPredicate
	}
	state := it.State
	if state == "" {
		state = "watching"
	}
	srcID, err := st.UpsertNode(store.GraphNode{Name: it.Src})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	dstID, err := st.UpsertNode(store.GraphNode{Name: it.Dst})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now().UnixMilli()
	content := watchEpisodeContent(it.Class, state, 1, it.Observation, it.SuggestedFix)
	sum := sha256.Sum256([]byte("watchlist\x00" + watchEpisodeRef + "\x00" + content))
	epID, _, err := st.AddEpisode(store.Episode{
		Kind:    "watchlist",
		Ref:     watchEpisodeRef,
		Content: content,
		Hash:    hex.EncodeToString(sum[:]),
		TS:      now,
	})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := st.MarkEpisodeDone(epID); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	edgeID, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: srcID, DstID: dstID, Predicate: predicate, Fact: it.Observation,
		EpisodeID: epID, Strength: it.Strength, ValidFrom: now,
	})
	if err != nil {
		util.LogEvent("web", "error", "watch-add", "watch-add edge: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := st.WatchEdge(edgeID, watchOverlayFrom(it)); err != nil {
		util.LogEvent("web", "error", "watch-add", "watch-add overlay: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "watch-add", "watch-add "+strconv.FormatInt(edgeID, 10)+" "+it.Class)
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": edgeID, "folded": false})
}

// watchItemUpdate is op `update`: a partial write of the OVERLAY, and nothing
// but the overlay.
//
// The old GET-then-backfill dance is gone. store.UpdateWatchItem is already
// partial (COALESCE(NULLIF(...)) per column, one statement), so a request that
// restates only suggested_fix leaves sightings, first_seen and last_seen —
// the history the whole feature exists to record — untouched without the
// handler re-reading anything.
//
// The fact sentence and its endpoints are refused rather than ignored:
// kg_edges is insert-only, so no verb here could honor a reword, and an
// accepted-then-dropped key is exactly the silent no-op the two-route split
// was built to avoid.
func watchItemUpdate(w http.ResponseWriter, st *store.Store, it watchItemWire, itemKeys map[string]json.RawMessage) {
	if bad := rejectKeys(itemKeys, immutableWatchKeys); bad != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "item." + bad + " is the fact itself, not the overlay — a watch item IS a graph edge and edges are insert-only; remove and re-add to reword"})
		return
	}
	it.Class = strings.TrimSpace(it.Class)
	it.State = strings.TrimSpace(it.State)
	if it.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing item id"})
		return
	}
	if it.Class != "" && !oneOf(it.Class, watchClasses) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad class " + strconv.Quote(it.Class) + " — want one of " + strings.Join(watchClasses, "|")})
		return
	}
	if it.State != "" && !oneOf(it.State, watchStates) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad state " + strconv.Quote(it.State) + " — want one of " + strings.Join(watchStates, "|")})
		return
	}
	// "Unknown watch item" means the same thing on both writes: no overlay row,
	// OR an overlay whose edge kraken has since unlearned. UpdateWatchItem
	// reports ErrNotFound only for the first, so without this probe an update
	// against an invalidated edge would answer 200 into a row no reader can
	// see — the same reason watchlistPost carries one.
	if _, err := st.GetWatchItem(it.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown watch item"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
