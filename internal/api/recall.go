package api

import (
	"net/http"

	"kraken/internal/jobs"
)

// recall.go — POST /api/graph/recall: the server half of mid-conversation
// memory. A cell's UserPromptSubmit/PostToolUse hooks (`kraken
// recall-hook`) POST the
// hook payload here; the response is the block that hook emits as
// additionalContext.
//
// Its own file rather than an eighth handler in graph.go because it is the one
// graph route whose caller is neither a human nor the extraction agent: it is
// machinery running inside somebody else's conversation, and the failure
// posture that follows from that (never an error status, never anything but a
// string) belongs beside the reasoning for it rather than in the middle of the
// viewer's reads.
//
// Additive, like every other graph route — a NEW PATH, no existing shape
// touched — so `apiContract` stays 5 and no client that predates it decodes
// anything differently. Nothing in Models.swift consumes it; the only caller
// is a hook process on localhost.

// graphRecallHandler serves POST /api/graph/recall.
//
// A well-formed body ALWAYS gets a 200 carrying whatever jobs.Recall returned,
// and `""` is a completely ordinary answer: the caller is a hook, and "the
// graph has nothing to add" is the common case, not a fault. Returning an
// error status for it would put a red line in the user's hook output on almost
// every prompt.
//
// The 4xx cases are the ones where the REQUEST is wrong rather than the
// answer empty — a wrong method, a body that isn't the shape this route takes
// — which are bugs in the caller and worth being loud about.
func graphRecallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req jobs.RecallRequest
	if !decodeGraphBody(w, r, &req,
		"this route takes only session_id, event, prompt and transcript_path") {
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	// r.Context() rather than context.Background(): the hook client gives up
	// at 4s while the embedder Recall calls has a much longer budget of its
	// own, so a slow embed can outlive the request. Recall checks this
	// immediately before recording a delivery — a caller that is gone read
	// nothing, and marking it delivered would withhold those facts from the
	// session permanently.
	webJSON(w, http.StatusOK, map[string]string{"context": jobs.Recall(r.Context(), st, req)})
}
