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
)

// jobload.go — GET/POST /api/job/load: zeus' triage estimate for a blocked
// job. Two numbers, both 1-10 — `unblock` (how much effort carl must spend to
// unblock it: reading, deciding, explaining) and `scope` (how big the claude
// work that reply unblocks is likely to be) — plus a one-line rationale.
//
// The board renders scope/unblock as a ratio: a cheap unblock buying a long
// claude session ranks first. That ratio is DERIVED wherever it is shown and
// is deliberately neither stored nor on the wire — two stored halves and a
// stored quotient can drift out of agreement, a derived one cannot (the same
// reasoning webTokens' zeus/subagent split follows).
//
// Its own one-field route rather than a field of the job form, for the reason
// /api/job/color is one (jobcolor.go): the form save is gated on editableJob
// — no lifecycle in flight — and this estimate exists precisely for a job
// whose cell is LIVE and blocked, so routing it through the form would make
// the feature unreachable exactly where it matters. The full-replace blast
// radius argument applies identically: a form save from a client predating
// the field would clear it.

// webJobLoad is one job's estimate on the wire: webJob's "load" object and
// the whole payload of GET /api/job/load. No omitempty on any field — a
// client reads all four unconditionally once the object is present, and a
// zero note is a real answer. Absence is expressed by the object itself
// being gone (webJob.Load is a pointer, GET serves null), never by a missing
// key.
type webJobLoad struct {
	Unblock int    `json:"unblock"`
	Scope   int    `json:"scope"`
	Note    string `json:"note"`
	At      int64  `json:"at"` // unix millis, stamped server-side
}

// webJobLoadReq is POST /api/job/load's body. At is deliberately absent: the
// write time is stamped here, never accepted from a client, so an estimate
// can never claim to be fresher (or older) than the write that made it.
//
// Unblock and Scope are POINTERS so that "absent" and "explicitly 0" stay
// different requests. With plain ints they are the same value, and since 0/0
// is the CLEAR, every body that simply failed to carry the two numbers —
// `{"slug":"x"}`, `{"slug":"x","note":"fixing the wording"}`, a misspelled
// key, a nested `{"estimate":{...}}` — would have decoded to a clear and
// silently wiped the row it was trying to describe. That is the same coercion
// this route refuses one level up: a body carrying NEITHER number is strictly
// less informative than a half estimate, which is a 400.
type webJobLoadReq struct {
	Slug    string `json:"slug"`
	Unblock *int   `json:"unblock"`
	Scope   *int   `json:"scope"`
	Note    string `json:"note"`
}

// jobLoadNoteMax caps the rationale. It is a one-liner shown under a badge,
// so an over-long one is TRUNCATED rather than refused: the numbers are the
// payload and losing the whole write over a verbose sentence would be a
// worse answer than a clipped sentence.
const jobLoadNoteMax = 200

// webJobLoadFrom shapes a stored row for the wire, or nil when the row holds
// no estimate — which is one state in the store, not two: a clear writes the
// same four zeroes a never-written row already has.
//
// At is the presence test because it is the one field stamped on EVERY write.
// Testing the numbers instead would give the same answer for every row any
// writer in this binary can currently produce — a clear zeroes all four
// columns at once, so 0/0 and At 0 always arrive together — and that
// agreement is a property of today's writer, not of the data. At stays
// meaningful if a later writer ever clears the numbers without clearing the
// stamp, or stamps without setting numbers; the pair does not.
func webJobLoadFrom(l store.JobLoad) *webJobLoad {
	if l.At == 0 {
		return nil
	}
	return &webJobLoad{Unblock: l.Unblock, Scope: l.Scope, Note: l.Note, At: l.At}
}

// jobLoadHandler serves GET (board/zeus read) and POST (zeus' write). POST is
// mirror-gated inline — the gate cannot go on the route, since the GET must
// stay reachable — with the same plain-text body every gated route uses
// (mirror_test.go asserts the exact string across all of them). Same policy
// as questions/feedback/watchlist: a mirror board is read-only, full stop.
func jobLoadHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jobLoadGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		jobLoadPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// jobLoadGet serves {"load": {...}} — or {"load": null} for a job with no
// estimate. Null rather than an omitted key or a zeroed object: the caller
// asked about one specific job, so "this job has no estimate" is the answer
// itself and deserves to be stated.
func jobLoadGet(w http.ResponseWriter, r *http.Request) {
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
	webJSON(w, http.StatusOK, map[string]any{"load": webJobLoadFrom(j.Load)})
}

// jobLoadPost validates the pair and writes it. Validation is the whole gate
// for this feature and it lives here, ahead of the job lookup, so a malformed
// request is answered the same way whether or not the slug happens to exist.
func jobLoadPost(w http.ResponseWriter, r *http.Request) {
	// Body cap like every other POST in this package (peers.go, feedback.go,
	// questions.go, jobcolor.go): the whole legitimate request is two small
	// numbers and one short line, so an oversize one is refused with 413
	// rather than read into memory, and that stays distinct from an ordinary
	// decode error's 400.
	var req webJobLoadReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	// DisallowUnknownFields turns a key that matches NO field into a refusal
	// instead of a request that decodes to "clear everything" — a misspelling
	// (`unblok`, `un_block`), a nested `{"estimate":{...}}`, or a client-sent
	// `at`, which is refused rather than silently ignored, the honest answer
	// for a field the server stamps.
	//
	// What it does NOT catch is a CASE variant: encoding/json matches struct
	// fields case-insensitively and this option only fires when nothing
	// matches at all, so `{"Unblock":2,"Scope":8}` decodes cleanly and writes
	// (measured, not assumed). That is benign — the values land under the
	// right fields with the right meaning — but it is why this is a guard
	// against a body that means something ELSE, not a spelling checker.
	//
	// It is safe to be this strict here because this route has exactly one
	// writer — cmd/kraken/cmd_load.go's runLoadSet/runLoadClear, which post
	// the four documented keys and nothing else — and neither client posts to
	// it at all.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		// encoding/json reports an unknown field only as prose, with no typed
		// error to match on, so this substring test is the only way to tell
		// that case from ordinary malformed JSON. If the stdlib ever reworded
		// it the request still fails with the same 400, just under the
		// siblings' generic "bad json" — a degraded message, never a changed
		// outcome. The offending key is NOT echoed: it comes from the body,
		// so quoting it would let a client choose the size of the response.
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — this route takes only slug, unblock, scope and note (at is stamped server-side)",
			})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}

	// Three legal shapes and nothing else: both numbers present and in 1..10
	// (an estimate), both present and zero (the clear), and nothing else at
	// all. A HALF estimate is refused with the reason rather than silently
	// coerced, because there is no honest coercion — the whole value of the
	// pair is the ratio scope/unblock, which a zero on either side leaves
	// undefined (unblock 0) or meaningless (scope 0).
	//
	// An ABSENT number is refused by the same sentence, and it is the same
	// rule read one level up: a body that names neither number says even less
	// than one that names one, so accepting it as a clear would destroy an
	// estimate on the strength of a request that never mentioned it. `null`
	// as the whole body lands here too — it decodes into the zero struct
	// without error, leaving both pointers nil.
	if req.Unblock == nil || req.Scope == nil {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing " + missingLoadFields(req) + ": roi is scope/unblock, so a partial request says nothing — send both in 1..10, or both 0 to clear",
		})
		return
	}
	unblock, scope := *req.Unblock, *req.Scope
	clear := unblock == 0 && scope == 0
	inRange := func(n int) bool { return n >= 1 && n <= 10 }
	if !clear && !(inRange(unblock) && inRange(scope)) {
		var msg string
		if (unblock == 0) != (scope == 0) {
			msg = fmt.Sprintf("half estimate (unblock %d, scope %d): roi is scope/unblock, so one number alone says nothing — send both in 1..10, or both 0 to clear", unblock, scope)
		} else {
			msg = fmt.Sprintf("unblock and scope must each be 1..10 (or both 0 to clear); got unblock %d, scope %d", unblock, scope)
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	j, _, err := webJobFor(req.Slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	// A clear writes the zero row whole, note included: a rationale with no
	// numbers to explain is not a smaller estimate, it is a leftover.
	load := store.JobLoad{}
	if !clear {
		load = store.JobLoad{
			Unblock: unblock,
			Scope:   scope,
			// core.TruncateOneLine both one-lines (control characters, incl.
			// newlines, become spaces; runs of whitespace collapse) and caps
			// at 200 RUNES, which is what keeps a multi-byte note from
			// blowing the cap when a byte count would not.
			Note: core.TruncateOneLine(req.Note, jobLoadNoteMax),
			At:   time.Now().UnixMilli(),
		}
	}
	if err := jobs.SetJobLoad(j, load); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// missingLoadFields names which of the two numbers the request left out, so
// the refusal points at the actual omission rather than at both. A body with
// neither is the common case (an unshaped or nested body), and saying so is
// what tells the caller the request was not merely mistyped.
func missingLoadFields(req webJobLoadReq) string {
	switch {
	case req.Unblock == nil && req.Scope == nil:
		return "both unblock and scope"
	case req.Unblock == nil:
		return "unblock"
	default:
		return "scope"
	}
}
