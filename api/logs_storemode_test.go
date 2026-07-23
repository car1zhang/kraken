package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/store"
)

// TestWebLogsStoreModePagination covers deliverable 7's logs endpoints in
// store mode: a virtual "events" file backed by store.ListEvents, with the
// scroll-back cursor baked into the opaque "prev"/"file" strings.
func TestWebLogsStoreModePagination(t *testing.T) {
	st := useTestStore(t)

	const total = eventsPageSize + 5
	var lastID int64
	for i := 0; i < total; i++ {
		id, err := st.AppendEvent(store.Event{Src: "test", Event: "e"})
		if err != nil {
			t.Fatal(err)
		}
		lastID = id
	}

	filesRec := httptest.NewRecorder()
	webLogFiles(filesRec, httptest.NewRequest(http.MethodGet, "/api/logs/files", nil))
	if filesRec.Code != http.StatusOK {
		t.Fatalf("webLogFiles: status %d, body %s", filesRec.Code, filesRec.Body.String())
	}
	var filesResp map[string]any
	if err := json.Unmarshal(filesRec.Body.Bytes(), &filesResp); err != nil {
		t.Fatal(err)
	}
	files, _ := filesResp["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("want exactly 1 virtual file, got %d: %+v", len(files), files)
	}

	// first page: no ?file= — newest eventsPageSize entries, "prev" set to a
	// scroll-back cursor since more history remains.
	rec := httptest.NewRecorder()
	webLogRead(rec, httptest.NewRequest(http.MethodGet, "/api/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("webLogRead: status %d, body %s", rec.Code, rec.Body.String())
	}
	var page1 struct {
		File    string           `json:"file"`
		Prev    string           `json:"prev"`
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if len(page1.Entries) != eventsPageSize {
		t.Fatalf("page1 entries = %d, want %d", len(page1.Entries), eventsPageSize)
	}
	if page1.Prev == "" {
		t.Fatal("want a non-empty prev cursor with more history remaining")
	}

	// second page via the cursor: the remaining 5 entries, no further prev.
	rec2 := httptest.NewRecorder()
	webLogRead(rec2, httptest.NewRequest(http.MethodGet, "/api/logs?file="+page1.Prev, nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("webLogRead page 2: status %d, body %s", rec2.Code, rec2.Body.String())
	}
	var page2 struct {
		File    string           `json:"file"`
		Prev    string           `json:"prev"`
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Entries) != total-eventsPageSize {
		t.Fatalf("page2 entries = %d, want %d", len(page2.Entries), total-eventsPageSize)
	}
	if page2.Prev != "" {
		t.Fatalf("page2 prev = %q, want empty (no more history)", page2.Prev)
	}
	if page2.File != page1.Prev {
		t.Fatalf("page2 file = %q, want it to echo the requested cursor %q", page2.File, page1.Prev)
	}
	if lastID == 0 {
		t.Fatal("no events were appended")
	}
}

// TestWebLogsStoreModeOldestFirst covers: within one page, entries
// must read oldest-first (append order), exactly like a file-mode log file —
// store.ListEvents itself returns newest-first for its own beforeID
// pagination, so webLogReadFromStore must reverse before handing entries
// back on the wire.
func TestWebLogsStoreModeOldestFirst(t *testing.T) {
	st := useTestStore(t)
	for i := 0; i < 3; i++ {
		if _, err := st.AppendEvent(store.Event{Src: "test", Event: fmt.Sprintf("e%d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	webLogRead(rec, httptest.NewRequest(http.MethodGet, "/api/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("webLogRead: status %d, body %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Entries []struct {
			Event string `json:"event"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(page.Entries))
	}
	if page.Entries[0].Event != "e0" || page.Entries[1].Event != "e1" || page.Entries[2].Event != "e2" {
		t.Fatalf("entries not oldest-first (append order): %+v", page.Entries)
	}
}

func TestWebLogReadStoreModeBadFileToken404s(t *testing.T) {
	useTestStore(t)
	rec := httptest.NewRecorder()
	webLogRead(rec, httptest.NewRequest(http.MethodGet, "/api/logs?file=../../etc/passwd", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestWebLogsStoreModeEmpty(t *testing.T) {
	useTestStore(t)
	rec := httptest.NewRecorder()
	webLogRead(rec, httptest.NewRequest(http.MethodGet, "/api/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	entries, _ := resp["entries"].([]any)
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %+v", entries)
	}
}
