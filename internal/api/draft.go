package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"kraken/internal/jobs"
)

// draftEntry is one job's stashed prompt-composer text — never persisted to
// disk, so a k web restart clears every draft (see ios/API.md).
type draftEntry struct {
	Text      string
	UpdatedAt int64 // unix millis
}

// draftStore is a mutex-guarded map keyed by JOB ID, the same idiom as
// boardCache (board.go): package-level singleton, injectable now for tests.
//
// This map is the real draft store — the `drafts` TABLE that used to sit
// beside it had zero non-test callers and zero rows, and was dropped in schema
// v11. So the collision the table's re-key would have fixed was always here
// instead: keyed by SLUG, an archived job and the active job that reused its
// name shared one draft, and whichever composer opened last read the other's
// half-written message. The id is unique for the life of the process, which is
// also the life of this map.
type draftStore struct {
	mu      sync.Mutex
	now     func() time.Time // injectable for tests
	entries map[int64]draftEntry
}

func (s *draftStore) get(jobID int64) draftEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[jobID]
}

// set stores text under jobID, stamping the current time — an empty text
// clears the entry outright rather than storing a blank one.
func (s *draftStore) set(jobID int64, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		delete(s.entries, jobID)
		return
	}
	s.entries[jobID] = draftEntry{Text: text, UpdatedAt: s.now().UnixMilli()}
}

func (s *draftStore) clear(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, jobID)
}

// webDraftStore is the /api/draft handlers' single instance.
var webDraftStore = &draftStore{now: time.Now, entries: map[int64]draftEntry{}}

// clearDraft drops a job's stashed draft, if any — called from the success
// path of every web action that moves a job past drafting it (steer,
// reactivate, done, archive, delete).
//
// Takes the job's id rather than its slug because that is now the map's key;
// a zero id is a no-op, matching the handlers' own rule below.
func clearDraft(jobID int64) {
	if jobID == 0 {
		return
	}
	webDraftStore.clear(jobID)
}

type webDraft struct {
	Slug      string `json:"slug"`
	Text      string `json:"text"`
	UpdatedAt int64  `json:"updated_at"`
}

// draftHandler serves the in-memory per-job prompt-draft store: GET reads,
// POST writes. Drafts are ephemeral (process memory only) — a k web restart
// or a job's disappearance clears them.
//
// The wire is unchanged and stays SLUG-addressed, like every other job-scoped
// route: the slug is what a human and an agent say, and requests only ever
// address ACTIVE jobs, whose slugs jobs_active_slug already makes unique.
// Both handlers resolve that slug to a job here and key the store by its id —
// the resolution boundary this whole change is built around.
func draftHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		draftGet(w, r)
	case http.MethodPost:
		draftPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// draftGet reads a job's draft. A slug with no matching ACTIVE job (deleted,
// archived, merged away) reads back empty — the id-keyed store needs no lazy
// eviction for that case any more, since a vanished job's id is never asked
// for again, and clearDraft on the action paths handles the rest.
func draftGet(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug required"})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil || j.ID == 0 {
		webJSON(w, http.StatusOK, webDraft{Slug: slug})
		return
	}
	e := webDraftStore.get(j.ID)
	webJSON(w, http.StatusOK, webDraft{Slug: slug, Text: e.Text, UpdatedAt: e.UpdatedAt})
}

// draftPost stores a job's draft.
//
// A slug that resolves to no persisted job stores NOTHING and still answers
// ok. That is deliberate rather than lazy: a draft is explicitly losable
// process memory (a restart clears every one of them, and ios/API.md says so),
// so there is no state for the client to reconcile, whereas erroring here would
// pop a failure toast on the composer of a card that is merely mid-refresh.
// The zero-id case is unreachable under `kraken serve`, which always has a
// store wired — only a Job built by ParseJob alone carries no id.
func draftPost(w http.ResponseWriter, r *http.Request) {
	var req webReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if req.Slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug required"})
		return
	}
	if j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug); j != nil && j.ID != 0 {
		webDraftStore.set(j.ID, req.Text)
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
