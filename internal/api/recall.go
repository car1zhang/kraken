package api

import (
	"net/http"

	"kraken/internal/jobs"
)

// recall.go — POST /api/recall: the server half of mid-conversation delivery.
// A cell's UserPromptSubmit hook (`kraken recall-hook`) POSTs the hook payload
// here; the response is the block that hook emits as additionalContext —
// carl's goals and, for the job's lead session, his unread notes (jobs.Recall).
//
// The route's caller is machinery running inside somebody else's
// conversation, and the failure posture that follows from that (never an
// error status for an empty answer, never anything but a string) is stated on
// the handler below.
//
// Nothing in Models.swift consumes it; the only caller is `kraken recall-hook`
// on localhost.

// recallHandler serves POST /api/recall.
//
// A well-formed body ALWAYS gets a 200 carrying whatever jobs.Recall returned,
// and `""` is a completely ordinary answer: the caller is a hook, and "nothing
// new to say" is the common case, not a fault. Returning an error status for
// it would put a red line in the user's hook output on almost every prompt.
//
// The 4xx cases are the ones where the REQUEST is wrong rather than the
// answer empty — a wrong method, a body that isn't the shape this route takes
// — which are bugs in the caller and worth being loud about.
//
// Registered mirrorGated: a firing records what it delivered (the notes kv
// row), which is a write.
func recallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req jobs.RecallRequest
	if !decodeStrictBody(w, r, &req,
		"this route takes only session_id, event, prompt, transcript_path and slug") {
		return
	}
	st, ok := activeStore(w)
	if !ok {
		return
	}
	// r.Context() rather than context.Background(): the hook client gives up
	// at 4s, and Recall checks the context before recording a delivery — a
	// caller that is gone read nothing, and recording it as delivered would
	// suppress that block.
	webJSON(w, http.StatusOK, map[string]string{"context": jobs.Recall(r.Context(), st, req)})
}
