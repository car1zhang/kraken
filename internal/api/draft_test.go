package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kraken/internal/store"
)

// draft_test.go — /api/draft and the in-memory store behind it. Every test
// here is store-backed rather than file-backed, and that is the point: the
// draft map keys on jobs.id now, and a file-backed *Job carries no id at all
// (ParseJob never sets one).

// resetDraftStore swaps in a fresh, empty draft store pinned to now — tests
// share the package-level webDraftStore singleton, so each test needs its
// own isolated instance rather than leaking entries/timestamps between runs.
func resetDraftStore(t *testing.T, now time.Time) {
	t.Helper()
	orig := webDraftStore
	webDraftStore = &draftStore{now: func() time.Time { return now }, entries: map[int64]draftEntry{}}
	t.Cleanup(func() { webDraftStore = orig })
}

// draftJob creates one active job and returns its id — the key every
// assertion below reads the store under.
func draftJob(t *testing.T, st *store.Store, slug string) int64 {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug})
	if err != nil {
		t.Fatalf("CreateJob %q: %v", slug, err)
	}
	return id
}

func draftGetReq(slug string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/draft?slug="+slug, nil)
}

func draftPostReq(t *testing.T, body any) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/api/draft", bytes.NewReader(mustJSON(t, body)))
}

// decodeDraft runs one request through the handler and decodes the reply,
// failing on any non-200.
func decodeDraft(t *testing.T, req *http.Request) webDraft {
	t.Helper()
	rec := httptest.NewRecorder()
	draftHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp webDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	return resp
}

func TestDraftGetUnknownSlugEmpty(t *testing.T) {
	st := useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))
	draftJob(t, st, "draft-unknown")

	resp := decodeDraft(t, draftGetReq("draft-unknown"))
	if resp.Slug != "draft-unknown" || resp.Text != "" || resp.UpdatedAt != 0 {
		t.Errorf("resp = %+v, want empty draft", resp)
	}
}

func TestDraftRoundTrip(t *testing.T) {
	st := useTestStore(t)
	resetDraftStore(t, time.UnixMilli(5000))
	draftJob(t, st, "draft-roundtrip")

	decodeDraft(t, draftPostReq(t, webReq{Slug: "draft-roundtrip", Text: "hello world"}))

	resp := decodeDraft(t, draftGetReq("draft-roundtrip"))
	if resp.Text != "hello world" || resp.UpdatedAt != 5000 {
		t.Errorf("resp = %+v, want text %q at 5000", resp, "hello world")
	}
	// the wire still speaks slugs — the id is an internal key, never a field
	if resp.Slug != "draft-roundtrip" {
		t.Errorf("resp.Slug = %q, want the slug echoed back unchanged", resp.Slug)
	}
}

// TestDraftSecondPostOverwrites pins last-write-wins: a later POST replaces
// both the text and the updated_at stamp, no merge or history kept.
func TestDraftSecondPostOverwrites(t *testing.T) {
	st := useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))
	draftJob(t, st, "draft-overwrite")

	decodeDraft(t, draftPostReq(t, webReq{Slug: "draft-overwrite", Text: "first"}))
	webDraftStore.now = func() time.Time { return time.UnixMilli(2000) }
	decodeDraft(t, draftPostReq(t, webReq{Slug: "draft-overwrite", Text: "second"}))

	resp := decodeDraft(t, draftGetReq("draft-overwrite"))
	if resp.Text != "second" || resp.UpdatedAt != 2000 {
		t.Errorf("resp = %+v, want \"second\" at 2000 (last write wins)", resp)
	}
}

func TestDraftPostEmptyTextClears(t *testing.T) {
	st := useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))
	draftJob(t, st, "draft-clear")

	decodeDraft(t, draftPostReq(t, webReq{Slug: "draft-clear", Text: "something"}))
	decodeDraft(t, draftPostReq(t, webReq{Slug: "draft-clear", Text: ""}))

	resp := decodeDraft(t, draftGetReq("draft-clear"))
	if resp.Text != "" || resp.UpdatedAt != 0 {
		t.Errorf("resp = %+v, want cleared", resp)
	}
}

func TestDraftGetMissingSlug(t *testing.T) {
	useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))

	rec := httptest.NewRecorder()
	draftHandler(rec, httptest.NewRequest(http.MethodGet, "/api/draft", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDraftPostBadJSON(t *testing.T) {
	useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))

	rec := httptest.NewRecorder()
	draftHandler(rec, httptest.NewRequest(http.MethodPost, "/api/draft", bytes.NewReader([]byte("not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestDraftGetEmptyWhenJobGone: a slug with a stored draft whose job is gone
// (deleted, archived, merged away) reads back empty. Under the id-keyed store
// the stale entry is also unreachable by construction — no later job can be
// handed that id — which is what retires the old lazy-eviction path.
func TestDraftGetEmptyWhenJobGone(t *testing.T) {
	st := useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))
	id := draftJob(t, st, "draft-vanish")

	decodeDraft(t, draftPostReq(t, webReq{Slug: "draft-vanish", Text: "stale"}))
	if e := webDraftStore.get(id); e.Text != "stale" {
		t.Fatalf("draft not stored before job removal: %+v", e)
	}

	if err := st.DeleteJob(id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	resp := decodeDraft(t, draftGetReq("draft-vanish"))
	if resp.Text != "" {
		t.Errorf("resp = %+v, want empty text after job removal", resp)
	}
}

// TestDraftStoreKeyedByJobID is the defect this re-key exists for, and it is
// the ONE place the collision was ever real: the `drafts` TABLE had zero rows
// and zero non-test callers, so the slug-keyed map below was the whole feature.
// Archiving FREES a slug, so an archived job and the active job that reused its
// name shared one entry — carl would open the new job's composer and find the
// old job's half-written message in it, or overwrite one with the other.
//
// Both generations are addressed by the SAME slug on the wire (that is the
// point — the wire never changed), and the handler's resolution to the ACTIVE
// job is what keeps them apart.
func TestDraftStoreKeyedByJobID(t *testing.T) {
	st := useTestStore(t)
	resetDraftStore(t, time.UnixMilli(1000))

	oldID := draftJob(t, st, "helper-catalog")
	if _, err := st.ArchiveJob(oldID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	newID := draftJob(t, st, "helper-catalog") // reuses the freed slug
	if oldID == newID {
		t.Fatal("fixture is wrong: the two generations must be distinct rows")
	}

	// the archived generation still holds a draft from when it was live
	webDraftStore.set(oldID, "the archived job's half-written message")

	// carl types into the NEW job's composer, addressed by the same slug
	decodeDraft(t, draftPostReq(t, webReq{Slug: "helper-catalog", Text: "the new job's message"}))

	if got := webDraftStore.get(oldID).Text; got != "the archived job's half-written message" {
		t.Errorf("the archived job's draft = %q, want it untouched", got)
	}
	if got := webDraftStore.get(newID).Text; got != "the new job's message" {
		t.Errorf("the active job's draft = %q, want its own text", got)
	}

	// and the read comes back with the ACTIVE job's draft, not the archived one
	resp := decodeDraft(t, draftGetReq("helper-catalog"))
	if resp.Text != "the new job's message" {
		t.Errorf("GET = %q, want the ACTIVE job's draft", resp.Text)
	}

	// clearDraft is likewise scoped: clearing one must not touch the other
	clearDraft(newID)
	if got := webDraftStore.get(oldID).Text; got == "" {
		t.Error("clearing the active job's draft also wiped the archived job's")
	}
}
