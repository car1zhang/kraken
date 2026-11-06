package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"kraken/internal/jobs"
)

// mapdraw.go: GET/POST /api/map/drawings, the drawings on the agent map's
// plane (jobs/mapdraw.go, the `map_drawings` kv row). One path for both, so
// the POST gates itself through mirrorGated inside the handler and the GET
// stays reachable under --mirror (mappos.go's arrangement).

// mapDrawingsReq is the POST body: drawings to save by id, and ids to delete.
// Either may be absent; jobs.SetMapDrawings refuses a body with neither.
type mapDrawingsReq struct {
	Set    map[string]jobs.MapDrawing `json:"set"`
	Remove []string                   `json:"remove"`
}

func mapDrawingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		webJSON(w, http.StatusOK, map[string]any{"drawings": jobs.MapDrawings()})
	case http.MethodPost:
		mirrorGated(mapDrawingsPost)(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// mapDrawingsPost applies the body's removes and sets to the saved drawings.
// The decoder refuses unknown keys at every level, inside a drawing as well
// as at the top, and anything after the one JSON value.
func mapDrawingsPost(w http.ResponseWriter, r *http.Request) {
	var req mapDrawingsReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	if err == nil {
		if _, tail := dec.Token(); tail != io.EOF {
			err = tail
			if err == nil {
				err = errors.New("trailing data after the request body")
			}
		}
	}
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized field in request body: this route takes set and remove, and a drawing takes kind, pts, size and text"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// A store failure is the server's fault, and the board drops a batch
	// answered 400 as refused, so it must not look like one.
	if err := jobs.SetMapDrawings(req.Set, req.Remove); errors.Is(err, jobs.ErrMapDrawingsStore) {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
