package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// helpers.go — the two request helpers several handler files share (job
// drafts, Slack, the cell context report, recall).

// activeStore is the one place those handlers reach for the store, so "no
// store wired" is one sentence rather than one per handler. A nil store is a
// 500 with a reason, never a nil-pointer panic in a handler.
func activeStore(w http.ResponseWriter) (*store.Store, bool) {
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return nil, false
	}
	return st, true
}

// decodeStrictBody is the shared decode discipline for those handlers' POSTs,
// lifted from jobload.go: a 1 MB cap answered with 413 rather than read into
// memory, DisallowUnknownFields so a key matching NO field is a refusal
// instead of a request that quietly means something else, and the unknown-
// field case told apart from ordinary malformed JSON by the only means
// encoding/json offers — a substring of its prose. The offending key is
// never echoed: it comes from the body, so quoting it would let a caller
// choose the size of the response.
//
// `takes` is the tail of the unknown-field refusal, naming the route's actual
// field set so the caller is told what IS accepted rather than only that
// something wasn't.
func decodeStrictBody(w http.ResponseWriter, r *http.Request, into any, takes string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return false
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — " + takes,
			})
			return false
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return false
	}
	return true
}
