package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"kraken/internal/jobs"
)

// mappos.go — GET/POST /api/map/positions: where the operator left each card
// on the agent map (jobs/mappos.go, the `map_positions` kv row). One path for
// both, so the POST gates itself through mirrorGated inside the handler and
// the GET stays reachable under --mirror (jobstack.go's arrangement).

// mapPositionsReq is the POST body: the positions to change, keyed by the
// board's own card keys. Each value is decoded as a list so a pair of the
// wrong length is refused rather than padded or cut.
type mapPositionsReq struct {
	Positions map[string][]float64 `json:"positions"`
}

func mapPositionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		webJSON(w, http.StatusOK, map[string]any{"positions": jobs.MapPositions()})
	case http.MethodPost:
		mirrorGated(mapPositionsPost)(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// mapPositionsPost merges the body's positions into the saved ones.
func mapPositionsPost(w http.ResponseWriter, r *http.Request) {
	var req mapPositionsReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized field in request body — this route takes only positions"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	changes := make(map[string][2]float64, len(req.Positions))
	for k, p := range req.Positions {
		if len(p) != 2 {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "a position is [x, y]"})
			return
		}
		changes[k] = [2]float64{p[0], p[1]}
	}
	if err := jobs.SetMapPositions(changes); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
