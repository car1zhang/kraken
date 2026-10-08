package api

import (
	"net/http"
	"strconv"
	"strings"

	"kraken/internal/jobs"
)

// transcript.go — GET /api/job/transcript: the tail of zeus' own
// conversation, for the steer modal's read-only transcript panel (Q5). Each
// block carries its record uuid, which is what the mind button captures as a
// transcript source's anchor.

// jobTranscriptDefaultN is the steer modal's panel size.
const jobTranscriptDefaultN = 50

// jobTranscriptHandler serves the last n blocks of zeus' transcript. The
// clamp to 1..200 is jobs.ZeusTranscriptBlocks'; a job with no session or no
// transcript yet is an empty list, never an error. A read, so ungated.
func jobTranscriptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	n := jobTranscriptDefaultN
	if v := strings.TrimSpace(r.URL.Query().Get("n")); v != "" {
		if iv, err := strconv.Atoi(v); err == nil {
			n = iv
		}
	}
	j, _, err := webJobFor(slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	blocks, session, err := jobs.ZeusTranscriptBlocks(j, n)
	if err != nil {
		mmErr(w, "job-transcript", http.StatusInternalServerError, err.Error())
		return
	}
	if blocks == nil {
		blocks = []jobs.TranscriptBlock{}
	}
	webJSON(w, http.StatusOK, map[string]any{"session": session, "blocks": blocks})
}
