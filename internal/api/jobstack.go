package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobstack.go — GET/POST /api/job/stack: a cell's STACK LINKS, the URLs of
// whatever it spun up for manual testing (a parallel `kraken serve`, a vite
// dev server, an API on :8000), registered by the cell through
// `kraken stack add|rm|clear` and rendered by the board as linked chips in the
// card's PR band. The daemon's probe pass (internal/jobs/stackprobe.go) keeps
// each loopback link's `status` honest; this route never dials anything.
//
// Its own one-field route rather than a job-form field, for the reason
// /api/job/load is one (jobload.go): the form save is gated on editableJob
// and this list exists precisely for a job whose cell is LIVE. The body is
// PER-OP ({slug, op, url?, label?}) rather than a list, for the reason
// /api/job/goal's is (goals.go): a client sends what it wants CHANGED, never
// the set it believes is current, so a stale client cannot silently wipe a
// link it has not seen.

// webStackLink is one link on the wire: webJob's "stack" array element and
// GET /api/job/stack's items. No omitempty on any field — a client reads all
// four once the object is present; an empty label is a real answer (the chip
// then shows the port alone).
type webStackLink struct {
	URL    string `json:"url"`
	Label  string `json:"label"`
	Status string `json:"status"` // "up" | "down" | "unknown" (jobs.StackUp/Down/Unknown)
	At     int64  `json:"at"`     // unix millis, last registration time (a re-add refreshes it), stamped server-side
}

// webStackReq is POST /api/job/stack's body. Op is one of add | remove |
// clear; url carries the link for add and the url-or-label token for remove;
// label is add's optional short name. Decoded with DisallowUnknownFields so a
// misspelled or nested body is refused rather than read as some other op.
type webStackReq struct {
	Slug  string `json:"slug"`
	Op    string `json:"op"`
	URL   string `json:"url"`
	Label string `json:"label"`
}

// webStackLinksFrom shapes stored rows for the wire. nil (not an empty slice)
// for no rows, so webJob's omitempty drops the key: absence is the wire's
// only spelling of "no links".
func webStackLinksFrom(links []store.StackLink) []webStackLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]webStackLink, 0, len(links))
	for _, l := range links {
		out = append(out, webStackLink{URL: l.URL, Label: l.Label, Status: l.Status, At: l.At})
	}
	return out
}

// jobStackHandler serves GET (board/zeus read) and POST (the cell's write).
// POST is mirror-gated inline — the gate cannot go on the route since the GET
// must stay reachable — with the same plain-text body every gated route uses
// (mirror_test.go asserts the exact string across all of them).
func jobStackHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jobStackGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		jobStackPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// jobStackGet serves {"stack": [...]} — an EMPTY ARRAY, not null, for a job
// with no links: the caller asked about one job and "none" is a list of zero,
// which is also what `kraken stack get` prints straight through.
func jobStackGet(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	j, _, err := webJobFor(slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	links := webStackLinksFrom(jobs.ListStackLinks(j))
	if links == nil {
		links = []webStackLink{}
	}
	webJSON(w, http.StatusOK, map[string]any{"stack": links})
}

// jobStackPost validates the body and applies one op. Validation runs ahead
// of the job lookup, so a malformed request is answered the same way whether
// or not the slug exists.
func jobStackPost(w http.ResponseWriter, r *http.Request) {
	var req webStackReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		// encoding/json reports an unknown field only as prose (jobload.go
		// states the same trade-off); the offending key is not echoed.
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — this route takes only slug, op, url and label",
			})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	switch req.Op {
	case "add":
		if req.URL == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "add needs a url"})
			return
		}
		if _, err := jobs.ParseStackTarget(req.URL); err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad url: " + err.Error()})
			return
		}
	case "remove":
		if req.URL == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "remove needs a url or label in `url`"})
			return
		}
	case "clear":
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "op must be add, remove or clear"})
		return
	}

	j, _, err := webJobFor(req.Slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// A link is a fact about a CELL, so add is refused where there is none to
	// link to: a draft has never started and a done job's cell is torn down
	// (its rows were deleted with it). remove and clear are allowed in any
	// state — they only ever make the list smaller.
	if req.Op == "add" && (!j.Started() || j.State == "done") {
		webJSON(w, http.StatusConflict, map[string]string{
			"error": j.Slug + " has no running cell to link a stack to (state " + stateWord(j) + ")",
		})
		return
	}

	switch req.Op {
	case "add":
		// core.TruncateOneLine both one-lines and caps at RUNES, so a
		// multi-byte label cannot blow the cap when a byte count would not.
		label := core.TruncateOneLine(req.Label, jobs.StackLabelMax)
		if err := jobs.AddStackLink(j, req.URL, label); err != nil {
			status := http.StatusInternalServerError
			if strings.Contains(err.Error(), "the cap") {
				status = http.StatusBadRequest
			}
			webJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
	case "remove":
		if err := jobs.RemoveStackLink(j, req.URL); err != nil {
			// The not-found case gets its own sentence: the store's bare
			// "not found" would read, on the cell's stderr, as if the JOB
			// were missing, when it is the token that matched nothing.
			if errors.Is(err, store.ErrNotFound) {
				webJSON(w, http.StatusNotFound, map[string]string{
					"error": "no stack link on " + j.Slug + " has url or label " + strconv.Quote(req.URL) + " (`kraken stack get " + j.Slug + "` lists them)",
				})
				return
			}
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	case "clear":
		if err := jobs.ClearStackLinks(j); err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// stateWord names a job's state for a refusal: a never-started job has an
// empty State and would otherwise read as `state ` in the sentence.
func stateWord(j *jobs.Job) string {
	if j.State == "" {
		return "draft"
	}
	return j.State
}
