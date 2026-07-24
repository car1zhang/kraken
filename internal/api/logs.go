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
// util.LogEvent's eventSink (wired by jobs.UseStore, always true inside
// kraken serve) lands every call straight into the events table on top of
// its existing JSONL file write (util/eventlog.go — kept for other tooling
// that reads the raw files directly), but the web viewer reads only the
// events table: it presents a single virtual file named "events" and bakes
// the DB scroll-back cursor into the "file"/"prev" string fields the
// viewer already treats as opaque (e.g. "events:1042"). The wire SHAPE
// (files:[{name,size,mtime}], {file,prev,entries:[...]}) matches what a
// file-per-page log would produce. Both endpoints are read-only GETs.

// eventsFileToken matches the store-mode virtual log "file" name: a bare
// "events" (the newest page) or "events:<beforeID>" (a scroll-back cursor).
var eventsFileToken = regexp.MustCompile(`^events(?::([1-9][0-9]*))?$`)

// eventsPageSize is store mode's page size for webLogRead — comparable to a
// file-mode log file's few-thousand-entry granularity.
const eventsPageSize = 200

// webLogFiles lists the (single, virtual) log file — the viewer's
// pagination index.
func webLogFiles(w http.ResponseWriter, r *http.Request) {
	st := jobs.ActiveStore()
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
}

// webLogRead returns one page of event log entries: paginate
// store.ListEvents by id. No ?file= means the newest page (the viewer's
// initial load). name must be "" or match eventsFileToken exactly —
// anything else 404s. prev names the next-older page, "" at the start of
// history — the client's scroll-back cursor.
func webLogRead(w http.ResponseWriter, r *http.Request) {
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
