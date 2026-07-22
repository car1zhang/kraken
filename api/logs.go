package api

import (
	"net/http"

	"kraken/util"
)

// --- unified event log (viewer endpoints) ---
//
// The log's pagination unit is the file (util/eventlog.go rotates by size):
// clients fetch the newest file first, then walk backwards via the `prev`
// pointer as the user scrolls into history. Both endpoints are read-only
// GETs over ~/.kraken/logs.

// webLogFiles lists every log file oldest→newest — the viewer's pagination
// index.
func webLogFiles(w http.ResponseWriter, r *http.Request) {
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
