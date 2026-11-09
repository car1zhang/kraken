package api

import (
	"net/http"

	"kraken/internal/store"
)

// jobload.go — GET /api/job/load: a blocked job's triage estimate as the
// daemon's load judge wrote it (internal/jobs/loadjudge.go). Two numbers,
// both 1-10 — `unblock` (how much effort the operator must spend to unblock
// it: reading, deciding, explaining) and `scope` (how big the claude work
// that reply unblocks is likely to be) — plus a one-line note naming the two
// rungs the judgment landed on.
//
// The board renders scope/unblock as a ratio: a cheap unblock buying a long
// claude session ranks first. That ratio is DERIVED wherever it is shown and
// is deliberately neither stored nor on the wire — two stored halves and a
// stored quotient can drift out of agreement, a derived one cannot (the same
// reasoning webTokens' zeus/subagent split follows).
//
// The route is read-only. The estimate used to be zeus' own write, posted
// here from the cell's `kraken load set` verb; it is now a Jev judgment the
// daemon makes about zeus' final message after every turn, written straight
// to the store, so there is one writer and it is not an HTTP client. The GET
// stays for `kraken load get` and for anyone reading one job's estimate
// without the whole board.

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
	At      int64  `json:"at"` // unix millis, stamped by the judge
}

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

// jobLoadHandler serves GET and refuses every other method: the estimate has
// no HTTP writer any more.
func jobLoadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only — the load estimate is written by the daemon's judge, not by clients"})
		return
	}
	jobLoadGet(w, r)
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
