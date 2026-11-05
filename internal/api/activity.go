package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// activity.go — the agent map's reads. GET /api/job/events is the steer
// modal's events panel: a longer tail of the lead's events than the 24 a
// board card carries (webJob.activity, filled by buildWebBoard). talksFor
// shapes the cell-to-cell message rows (jobmsg.go) into a card's threads.

// jobEventsDefaultN is GET /api/job/events' n when the query names none.
const jobEventsDefaultN = 60

// jobEventsHandler serves {"events": [...]}, the lead's newest n events
// oldest first (jobs.JobEvents clamps n to 1..200). An `n` that is not an
// integer reads as the default, transcript.go's rule. A job with no session
// or no transcript yet is an empty list, never an error. A read, so ungated.
func jobEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	n := jobEventsDefaultN
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
	events, err := jobs.JobEvents(j, n)
	if err != nil {
		mmErr(w, "job-events", http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []core.AgentEvent{}
	}
	webJSON(w, http.StatusOK, map[string]any{"events": events})
}

// webTalk is one message thread on a card: the other job, how many messages
// this job's cell sent it and received from it, and the newer of the two
// last stamps (unix millis). No omitempty: a zero count is a real answer.
type webTalk struct {
	Slug     string `json:"slug"`
	State    string `json:"state"`
	Sent     int64  `json:"sent"`
	Received int64  `json:"received"`
	LastAt   int64  `json:"last_at"`
}

// talksFor is webJob.Talks for job id: one entry per other job opposite it
// in msgs that peers can name (an active job), newest thread first, then by
// slug. nil when there is none, so omitempty drops the key.
func talksFor(id int64, msgs []store.JobMessage, peers map[int64]webSpawnLink) []webTalk {
	byPeer := map[int64]*webTalk{}
	var order []int64
	for _, m := range msgs {
		var peer int64
		switch {
		case m.FromID == id && m.ToID != id:
			peer = m.ToID
		case m.ToID == id && m.FromID != id:
			peer = m.FromID
		default:
			continue
		}
		link, ok := peers[peer]
		if !ok {
			continue
		}
		t := byPeer[peer]
		if t == nil {
			t = &webTalk{Slug: link.Slug, State: link.State}
			byPeer[peer] = t
			order = append(order, peer)
		}
		if m.FromID == id {
			t.Sent += m.N
		} else {
			t.Received += m.N
		}
		if m.LastAt > t.LastAt {
			t.LastAt = m.LastAt
		}
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]webTalk, 0, len(order))
	for _, p := range order {
		out = append(out, *byPeer[p])
	}
	sort.SliceStable(out, func(i, k int) bool {
		if out[i].LastAt != out[k].LastAt {
			return out[i].LastAt > out[k].LastAt
		}
		return out[i].Slug < out[k].Slug
	})
	return out
}

// talkPeers names every active job for talksFor: its slug and display state.
// Archived jobs are left out, so a thread to one renders as nothing.
func talkPeers(active []*jobs.Job) map[int64]webSpawnLink {
	m := make(map[int64]webSpawnLink, len(active))
	for _, j := range active {
		m[j.ID] = webSpawnLink{Slug: j.Slug, State: jobs.DisplayState(j)}
	}
	return m
}
