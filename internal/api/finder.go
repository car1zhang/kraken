package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// finder.go — POST /api/finder/reveal, the server half of the artifact
// viewer's "Finder" button: the browser hands over {slug, name} for a LIVE
// cell's artifact and this selects that file in a Finder window on this Mac.
//
// It is editor.go's sibling and takes its two gates for the same reasons:
// the name comes from the client, so it is resolved only through
// ArtifactPath (board.go), whose traversal and hidden-file guard is the one
// every cell artifact read already passes; and a Finder window only ever
// opens on the machine serving the request, so a caller from anywhere else
// is refused before any resolution work. Only the "cell" home is
// revealable — a done or archived job's artifacts live in kraken.db rows,
// not in a file Finder could show.

// finderRevealReq is the request body: the job's slug (active namespace
// only, since only an active job has a live cell) and the artifact's name
// relative to the cell's artifacts dir, a subpath allowed.
type finderRevealReq struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// revealInFinderFunc performs the actual reveal. Swapped in tests — nothing
// in the suite ever execs.
var revealInFinderFunc = core.RevealInFinder

// finderRevealHandler resolves a live cell artifact to its file and reveals
// it. The refusal codes follow editorOpenHandler's: 405/415 from
// requireJSONPost, 403 from the origin gate, 400 for a body that isn't JSON,
// and 409 with a sentence for the toast for everything after that.
func finderRevealHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	if !isLocalOrigin(r.RemoteAddr) {
		msg := fmt.Sprintf("Finder reveals are restricted to this Mac — %s is not a local address", r.RemoteAddr)
		util.LogEvent("web", "error", "finder", "reveal: "+msg)
		webJSON(w, http.StatusForbidden, map[string]string{"error": msg})
		return
	}
	var req finderRevealReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		finderFail(w, fmt.Sprintf("no job %q", req.Slug))
		return
	}
	abs, ok := ArtifactPath(j, "cell", req.Name)
	if !ok {
		finderFail(w, fmt.Sprintf("%s is not a live artifact of %s", req.Name, req.Slug))
		return
	}
	if err := revealInFinderFunc(abs); err != nil {
		finderFail(w, err.Error())
		return
	}
	util.LogEvent("web", "info", "finder", fmt.Sprintf("reveal %s: %s", req.Slug, abs))
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// finderFail is editorFail's twin: a 409 carrying a sentence for the toast,
// logged, because a click that silently did nothing must not be possible.
func finderFail(w http.ResponseWriter, msg string) {
	util.LogEvent("web", "error", "finder", "reveal: "+msg)
	webJSON(w, http.StatusConflict, map[string]string{"error": msg})
}
