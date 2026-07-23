package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/util"
)

func TestLogEventHandlerHappyPath(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())

	body := mustJSON(t, logEventReq{Src: "install", Event: "make-install", Msg: "installed to ~/.local/bin"})
	req := httptest.NewRequest(http.MethodPost, "/api/logevent", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	logEventHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp["ok"] {
		t.Fatalf("resp = %+v", resp)
	}

	files := util.ListLogFiles()
	if len(files) != 1 {
		t.Fatalf("want 1 log file written, got %d", len(files))
	}
	entries, err := util.ReadLogFile(files[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Src != "install" || entries[0].Event != "make-install" || entries[0].Level != "info" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestLogEventHandlerDefaultsLevelToInfo(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	body := mustJSON(t, logEventReq{Src: "cli", Event: "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/logevent", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	logEventHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	entries, err := util.ReadLogFile(util.ListLogFiles()[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Level != "info" {
		t.Fatalf("level = %q, want info", entries[0].Level)
	}
}

// TestLogEventHandlerLevelAllowlist covers: level must be one of
// info|error (defaulting to info when omitted) — anything else is a 400,
// never silently accepted/stored as an arbitrary string.
func TestLogEventHandlerLevelAllowlist(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	body := mustJSON(t, logEventReq{Src: "cli", Event: "x", Level: "error"})
	req := httptest.NewRequest(http.MethodPost, "/api/logevent", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	logEventHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("level=error: status %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	entries, err := util.ReadLogFile(util.ListLogFiles()[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Level != "error" {
		t.Fatalf("level = %q, want error", entries[0].Level)
	}

	body = mustJSON(t, logEventReq{Src: "cli", Event: "x", Level: "debug"})
	req = httptest.NewRequest(http.MethodPost, "/api/logevent", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	logEventHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("level=debug: status %d, want 400", rec.Code)
	}
}

func TestLogEventHandlerRefusals(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	cases := []struct {
		name       string
		method     string
		body       any
		wantStatus int
	}{
		{"GET refused", http.MethodGet, nil, http.StatusMethodNotAllowed},
		{"missing src", http.MethodPost, logEventReq{Event: "x"}, http.StatusBadRequest},
		{"missing event", http.MethodPost, logEventReq{Src: "x"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r *http.Request
			if c.body != nil {
				r = httptest.NewRequest(c.method, "/api/logevent", bytes.NewReader(mustJSON(t, c.body)))
			} else {
				r = httptest.NewRequest(c.method, "/api/logevent", nil)
			}
			rec := httptest.NewRecorder()
			logEventHandler(rec, r)
			if rec.Code != c.wantStatus {
				t.Errorf("status = %d, want %d, body %s", rec.Code, c.wantStatus, rec.Body.String())
			}
		})
	}
}
