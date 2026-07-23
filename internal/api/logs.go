package api

import (
	"net/http"
	"regexp"
	"strconv"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// --- unified event log (viewer endpoints) ---
//
// File mode's pagination unit is the file (util/eventlog.go rotates by
// size): clients fetch the newest file first, then walk backwards via the
// `prev` pointer as the user scrolls into history. Store mode has no files
// at all — util.LogEvent's eventSink (wired by jobs.UseStore) lands every
// call straight into the events table — so it presents a single virtual
// file named "events" and bakes the DB scroll-back cursor into the same
// opaque "file"/"prev" string fields the file-mode viewer already treats as
// black boxes (e.g. "events:1042"); the wire SHAPE (files:[{name,size,
// mtime}], {file,prev,entries:[...]}) is unchanged either way. Both
// endpoints are read-only GETs.

// eventsFileToken matches the store-mode virtual log "file" name: a bare
// "events" (the newest page) or "events:<beforeID>" (a scroll-back cursor).
var eventsFileToken = regexp.MustCompile(`^events(?::([1-9][0-9]*))?$`)

// eventsPageSize is store mode's page size for webLogRead — comparable to a
// file-mode log file's few-thousand-entry granularity.
const eventsPageSize = 200

// webLogFiles lists every log file oldest→newest — the viewer's pagination
// index.
func webLogFiles(w http.ResponseWriter, r *http.Request) {
	if st := jobs.ActiveStore(); st != nil {
		newest, err := st.ListEvents(1, 0)
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Size/MTime move as events accrue (the newest event's own id/ts)
		// rather than staying pinned at 0 — a constant-valued virtual file
		// gives any size/mtime-based change detection nothing to key off.
		var size, mtime int64
		if len(newest) > 0 {
			size, mtime = newest[0].ID, newest[0].TS
		}
		webJSON(w, http.StatusOK, map[string]any{
			"files": []util.LogFileInfo{{Name: "events", Size: size, MTime: mtime}},
		})
		return
	}
	files := util.ListLogFiles()
	if files == nil {
		files = []util.LogFileInfo{}
	}
	webJSON(w, http.StatusOK, map[string]any{"files": files})
}

// webLogRead returns one log file's parsed entries. No ?file= means the
// newest file (the viewer's initial load). prev names the next-older file,
// "" at the start of history — the client's scroll-back cursor.
func webLogRead(w http.ResponseWriter, r *http.Request) {
	if jobs.ActiveStore() != nil {
		webLogReadFromStore(w, r)
		return
	}
	files := util.ListLogFiles()
	name := r.URL.Query().Get("file")
	idx := len(files) - 1 // default: newest
	if name != "" {
		idx = -1
		for i, f := range files {
			if f.Name == name {
				idx = i
				break
			}
		}
		// only names from the listing resolve — anything else (traversal
		// attempts, deleted files) is a plain 404
		if idx < 0 {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "no such log file"})
			return
		}
	}
	if idx < 0 { // no log files at all: an empty, well-formed page
		webJSON(w, http.StatusOK, map[string]any{"file": "", "prev": "", "entries": []util.LogEntry{}})
		return
	}
	entries, err := util.ReadLogFile(files[idx].Name)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []util.LogEntry{}
	}
	prev := ""
	if idx > 0 {
		prev = files[idx-1].Name
	}
	webJSON(w, http.StatusOK, map[string]any{"file": files[idx].Name, "prev": prev, "entries": entries})
}

// webLogReadFromStore is webLogRead's store-mode body: paginate
// store.ListEvents by id instead of walking JSONL files. name must be ""
// (first page) or match eventsFileToken exactly — anything else 404s, the
// same "only names from the listing resolve" contract the file-mode branch
// enforces for its own filenames.
func webLogReadFromStore(w http.ResponseWriter, r *http.Request) {
	st := jobs.ActiveStore()
	name := r.URL.Query().Get("file")
	reqFile := "events"
	var beforeID int64
	if name != "" {
		m := eventsFileToken.FindStringSubmatch(name)
		if m == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "no such log file"})
			return
		}
		reqFile = name
		if m[1] != "" {
			beforeID, _ = strconv.ParseInt(m[1], 10, 64)
		}
	}
	rows, err := st.ListEvents(eventsPageSize, beforeID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// rows comes back newest-first (store.ListEvents' own contract, so its
	// beforeID pagination can walk backwards id-by-id); the wire contract for
	// entries within a page is OLDEST-first — same append order file mode's
	// JSONL entries already read in — so the viewer renders top-to-bottom the
	// same way regardless of storage mode. prev still keys off the lowest id
	// in the page (rows[len(rows)-1] in the store's own newest-first order),
	// unaffected by how entries itself gets built below.
	entries := make([]util.LogEntry, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		e := rows[i]
		entries = append(entries, util.LogEntry{TS: e.TS, Src: e.Src, Level: e.Level, Event: e.Event, Msg: e.Msg})
	}
	prev := ""
	if len(rows) == eventsPageSize {
		prev = "events:" + strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	webJSON(w, http.StatusOK, map[string]any{"file": reqFile, "prev": prev, "entries": entries})
}
