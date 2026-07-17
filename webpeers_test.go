package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// getJSON builds a GET request whose query values are properly percent-
// encoded (pane ids like "%3" must round-trip through url.Values, not be
// pasted raw into the target — a literal "%3" in a URL is an incomplete
// percent-escape and net/url silently drops it).
func getJSON(path string, vals url.Values) *http.Request {
	return httptest.NewRequest(http.MethodGet, path+"?"+vals.Encode(), nil)
}

func TestHealthHandler(t *testing.T) {
	w := httptest.NewRecorder()
	healthHandler(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
	if resp["host"] == "" || resp["host"] == nil {
		t.Errorf("host missing: %v", resp["host"])
	}
}

func TestPeersHandlerEmptyIsArrayNotNull(t *testing.T) {
	h := peersHandler(func() []*Peer { return nil })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"peers":[]`) {
		t.Errorf("want empty array, got %s", w.Body.String())
	}
}

func TestPeersHandlerListsPeers(t *testing.T) {
	h := peersHandler(func() []*Peer { return []*Peer{{Pane: "%1", Name: "lead", Status: "running"}} })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers", nil))
	var resp struct {
		Peers []Peer `json:"peers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Peers) != 1 || resp.Peers[0].Pane != "%1" {
		t.Errorf("got %+v", resp.Peers)
	}
}

func TestFindPeerByPaneEmptyNeverMatches(t *testing.T) {
	// in-process teammates also carry Pane "" — an omitted query param must
	// not accidentally resolve to one of them
	peers := []*Peer{{Pane: "", AgentName: "in-process-mate", Messageable: false}}
	if p := findPeerByPane(peers, ""); p != nil {
		t.Errorf("empty pane matched %+v, want nil", p)
	}
}

func TestPeersReadHandlerUnknownPane(t *testing.T) {
	source := func() []*Peer { return []*Peer{{Pane: "%1", Name: "lead"}} }
	h := peersReadHandler(source)
	w := httptest.NewRecorder()
	h(w, getJSON("/api/peers/read", url.Values{"pane": {"%2"}, "n": {"3"}}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no session in pane %2") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersReadHandlerShellPaneRefused(t *testing.T) {
	source := func() []*Peer { return []*Peer{{Pane: "%3", Role: "shell", Name: "sh"}} }
	h := peersReadHandler(source)
	w := httptest.NewRecorder()
	h(w, getJSON("/api/peers/read", url.Values{"pane": {"%3"}}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no transcript to read") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersReadHandlerNDefaultAndClamp(t *testing.T) {
	// no session id → 409 before n ever reaches lastBlocks, but the handler
	// must still parse/clamp n without panicking on garbage input
	source := func() []*Peer { return []*Peer{{Pane: "%5", Name: "noid"}} }
	h := peersReadHandler(source)
	w := httptest.NewRecorder()
	h(w, getJSON("/api/peers/read", url.Values{"pane": {"%5"}, "n": {"not-a-number"}}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no session id resolved") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSendHandlerUnknownPane(t *testing.T) {
	h := peersSendHandler(func() []*Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`{"pane":"%9","message":"hi"}`))
	h(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no session in pane %9") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSendHandlerShellPaneRefused(t *testing.T) {
	source := func() []*Peer { return []*Peer{{Pane: "%4", Role: "shell", Name: "sh"}} }
	h := peersSendHandler(source)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`{"pane":"%4","message":"hi"}`))
	h(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "shell pane") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSendHandlerEmptyMessageRefused(t *testing.T) {
	source := func() []*Peer { return []*Peer{{Pane: "%6", Name: "lead", Messageable: true}} }
	h := peersSendHandler(source)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`{"pane":"%6","message":"   "}`))
	h(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "empty message") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSendHandlerMethodNotAllowed(t *testing.T) {
	h := peersSendHandler(func() []*Peer { return nil })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers/send", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPeersSendHandlerBadJSON(t *testing.T) {
	h := peersSendHandler(func() []*Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`not json`))
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}
