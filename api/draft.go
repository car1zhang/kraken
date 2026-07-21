package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"kraken/jobs"
)

// draftEntry is one job's stashed prompt-composer text — never persisted to
// disk, so a k web restart clears every draft (see ios/API.md).
type draftEntry struct {
	Text      string
	UpdatedAt int64 // unix millis
}

// draftStore is a mutex-guarded map keyed by job slug, the same idiom as
// boardCache (board.go): package-level singleton, injectable now for tests.
type draftStore struct {
	mu      sync.Mutex
	now     func() time.Time // injectable for tests
	entries map[string]draftEntry
}

func (s *draftStore) get(slug string) draftEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[slug]
}

// set stores text under slug, stamping the current time — an empty text
// clears the entry outright rather than storing a blank one.
func (s *draftStore) set(slug, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		delete(s.entries, slug)
		return
	}
	s.entries[slug] = draftEntry{Text: text, UpdatedAt: s.now().UnixMilli()}
}

func (s *draftStore) clear(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, slug)
}

// webDraftStore is the /api/draft handlers' single instance.
var webDraftStore = &draftStore{now: time.Now, entries: map[string]draftEntry{}}

// clearDraft drops slug's stashed draft, if any — called from the success
// path of every web action that moves a job past drafting it (steer,
// reactivate, done, archive, delete).
func clearDraft(slug string) {
	webDraftStore.clear(slug)
}

type webDraft struct {
	Slug      string `json:"slug"`
	Text      string `json:"text"`
	UpdatedAt int64  `json:"updated_at"`
}

// draftHandler serves the in-memory per-job prompt-draft store: GET reads,
// POST writes. Drafts are ephemeral (process memory only) — a k web restart
// or a job's disappearance clears them.
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

// draftGet lazily clears a stored draft the moment its job is no longer
// active (deleted, archived, merged away) — nothing else evicts a stale
// entry, since the store has no TTL of its own.
func draftGet(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug required"})
		return
	}
	if jobs.JobBySlug(jobs.LoadJobs(), slug) == nil {
		webDraftStore.clear(slug)
		webJSON(w, http.StatusOK, webDraft{Slug: slug})
		return
	}
	e := webDraftStore.get(slug)
	webJSON(w, http.StatusOK, webDraft{Slug: slug, Text: e.Text, UpdatedAt: e.UpdatedAt})
}

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
	webDraftStore.set(req.Slug, req.Text)
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
