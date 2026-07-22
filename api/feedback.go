package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"kraken/jobs"
)

// feedbackReq is the artifact overlay's feedback-submit body: slug/home/name
// identify the artifact exactly as the overlay's own URL does
// (/api/artifact/<slug>/<home>/<name>), plus the plaintext note.
type feedbackReq struct {
	Slug     string `json:"slug"`
	Home     string `json:"home"`
	Name     string `json:"name"`
	Feedback string `json:"feedback"`
}

// feedbackHandler serves POST /api/feedback: resolves the job and artifact
// exactly like artifactHandler (web/serve.go) does, then hands off to
// jobs.SaveArtifactFeedback for the actual snapshot+note write.
func feedbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req feedbackReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if strings.TrimSpace(req.Feedback) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "feedback is empty"})
		return
	}

	// home decides the namespace the job resolves in — same split as
	// artifactHandler, so an archived "foo-2" and an active "foo-2" never collide.
	var j *jobs.Job
	if req.Home == "archive" {
		j = jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), req.Slug)
	} else {
		j = jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	}
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	// ArtifactPath itself os.Stats the resolved path and refuses a missing
	// file or a directory, so ok==true already means the file exists.
	p, ok := ArtifactPath(j, req.Home, req.Name)
	if !ok {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown artifact"})
		return
	}

	dir, err := jobs.SaveArtifactFeedback(req.Slug, req.Home, req.Name, p, req.Feedback)
	if err != nil {
		// empty feedback was rejected above, so what's left is server-side
		// I/O — 500, without leaking filesystem paths to the client
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "write failed"})
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "dir": dir})
}
