package api

import (
	"encoding/json"
	"net/http"

	"kraken/util"
)

// logevent.go — POST /api/logevent: the localhost mutation endpoint
// cmd_jobs.go's `k logevent` (make install's log line, and any other
// non-Go writer) POSTs to instead of appending the JSONL file directly. The
// actual append still goes through util.LogEvent — its eventSink hook
// (wired by jobs.UseStore) is what makes this land in the events table when
// kraken serve is running, additive to the JSONL file write as always.

type logEventReq struct {
	Src   string `json:"src"`
	Level string `json:"level"`
	Event string `json:"event"`
	Msg   string `json:"msg"`
}

func logEventHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req logEventReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if req.Src == "" || req.Event == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "src and event are required"})
		return
	}
	level := req.Level
	switch level {
	case "":
		level = "info"
	case "info", "error":
		// allowed as submitted
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "level must be info or error"})
		return
	}
	util.LogEvent(req.Src, level, req.Event, req.Msg)
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
