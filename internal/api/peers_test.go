package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kraken/internal/core"
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
	h := peersHandler(func() []*core.Peer { return nil })
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
	h := peersHandler(func() []*core.Peer { return []*core.Peer{{Pane: "%1", Name: "lead", Status: "running"}} })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers", nil))
	var resp struct {
		Peers []core.Peer `json:"peers"`
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
	peers := []*core.Peer{{Pane: "", AgentName: "in-process-mate", Messageable: false}}
	if p := findPeerByPane(peers, ""); p != nil {
		t.Errorf("empty pane matched %+v, want nil", p)
	}
}

func TestPeersReadHandlerUnknownPane(t *testing.T) {
	source := func() []*core.Peer { return []*core.Peer{{Pane: "%1", Name: "lead"}} }
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
	source := func() []*core.Peer { return []*core.Peer{{Pane: "%3", Role: "shell", Name: "sh"}} }
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
	source := func() []*core.Peer { return []*core.Peer{{Pane: "%5", Name: "noid"}} }
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
	h := peersSendHandler(func() []*core.Peer { return nil })
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

// A shell target is now routed to the shell send path rather than refused for
// being a shell (round 4: the app can drive shells). An empty command is the
// deterministic guard we can assert without a live tmux pane — and crucially
// the error must NOT be the old "shell pane" refusal.
func TestPeersSendHandlerShellRoutedNotRefused(t *testing.T) {
	source := func() []*core.Peer { return []*core.Peer{{Pane: "%4", Role: "shell", Name: "sh"}} }
	h := peersSendHandler(source)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`{"pane":"%4","message":"  "}`))
	h(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "would run the message as a shell command") {
		t.Errorf("shell target should be routed to shell send, not refused: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "empty command") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSendHandlerEmptyMessageRefused(t *testing.T) {
	source := func() []*core.Peer { return []*core.Peer{{Pane: "%6", Name: "lead", Messageable: true}} }
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
	h := peersSendHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers/send", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPeersSendHandlerBadJSON(t *testing.T) {
	h := peersSendHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`not json`))
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPeersCaptureUnknownPane404(t *testing.T) {
	h := peersCaptureHandler(func() []*core.Peer { return []*core.Peer{{Pane: "%1", Role: "shell"}} })
	w := httptest.NewRecorder()
	h(w, getJSON("/api/peers/capture", url.Values{"pane": {"%9"}}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no session in pane %9") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersCaptureMissingPane404(t *testing.T) {
	// an omitted pane param must miss, never resolve a Pane-"" teammate
	h := peersCaptureHandler(func() []*core.Peer { return []*core.Peer{{Pane: "", Role: "teammate"}} })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers/capture", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPeersSendUnknownPane404(t *testing.T) {
	h := peersSendHandler(func() []*core.Peer { return []*core.Peer{{Pane: "%1", Role: "shell"}} })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/api/peers/send", strings.NewReader(`{"pane":"%9","message":"ls"}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSendToShellRejectsAgent(t *testing.T) {
	// sendToShell must refuse a non-shell peer before any tmux exec
	if err := core.SendToShell(&core.Peer{Pane: "%1", Role: "main", Agent: "claude"}, "ls"); err == nil {
		t.Error("want error for non-shell target")
	}
}

func TestSendToShellRejectsEmpty(t *testing.T) {
	if err := core.SendToShell(&core.Peer{Pane: "%1", Role: "shell"}, "  "); err == nil {
		t.Error("want error for empty command")
	}
}

func TestBuildSpawnArgv(t *testing.T) {
	cases := []struct {
		name             string
		kind, argName    string
		prompt, cmd      string
		wantErr          bool
		wantErrSubstring string
		check            func(t *testing.T, argv []string)
	}{
		{
			name: "shell no cmd", kind: "shell",
			check: func(t *testing.T, argv []string) {
				if argv != nil {
					t.Errorf("want nil argv, got %v", argv)
				}
			},
		},
		{
			name: "shell with cmd", kind: "shell", cmd: "echo hi",
			check: func(t *testing.T, argv []string) {
				if len(argv) != 3 || argv[1] != "-c" || !strings.Contains(argv[2], "echo hi") || !strings.Contains(argv[2], "exec ") {
					t.Errorf("got %v", argv)
				}
			},
		},
		{
			name: "claude ok", kind: "claude", argName: "foo",
			check: func(t *testing.T, argv []string) {
				if len(argv) < 3 {
					t.Fatalf("got %v", argv)
				}
				foundSkip, foundName := false, false
				for i, a := range argv {
					if a == "--dangerously-skip-permissions" {
						foundSkip = true
					}
					if a == "--name" && i+1 < len(argv) && argv[i+1] == "foo" {
						foundName = true
					}
				}
				if !foundSkip || !foundName {
					t.Errorf("got %v", argv)
				}
			},
		},
		{
			name: "claude with dash prompt", kind: "claude", argName: "foo", prompt: "-rf everything",
			check: func(t *testing.T, argv []string) {
				foundGuard := false
				for i, a := range argv {
					if a == "--" && i+1 < len(argv) && argv[i+1] == "-rf everything" {
						foundGuard = true
					}
				}
				if !foundGuard {
					t.Errorf("want \"--\" guard before prompt, got %v", argv)
				}
			},
		},
		{
			name: "claude no name", kind: "claude",
			wantErr: true, wantErrSubstring: "claude spawn needs a name",
		},
		{
			name: "bad kind", kind: "bogus",
			wantErr: true, wantErrSubstring: "kind must be",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, err := core.BuildSpawnArgv(c.kind, c.argName, c.prompt, c.cmd)
			if c.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !strings.Contains(err.Error(), c.wantErrSubstring) {
					t.Errorf("error = %q, want substring %q", err.Error(), c.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			c.check(t, argv)
		})
	}
}

func TestPeersKillHandlerMissingPane(t *testing.T) {
	h := peersKillHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/kill", strings.NewReader(`{"pane":"%9"}`))
	req.Header.Set("Content-Type", "application/json")
	h(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPeersKillHandlerCellSessionRefused(t *testing.T) {
	// TmuxSession on the stale snapshot is deliberately NOT "cell-foo" here —
	// the handler must refuse based on the freshly re-read session, not the
	// snapshot, so paneSessionFunc is what decides this test's outcome
	source := func() []*core.Peer {
		return []*core.Peer{{Pane: "%2", Name: "zeus", TmuxSession: "mobile"}}
	}
	old := paneSessionFunc
	paneSessionFunc = func(pane string) (string, error) { return "cell-foo", nil }
	t.Cleanup(func() { paneSessionFunc = old })
	h := peersKillHandler(source)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/kill", strings.NewReader(`{"pane":"%2"}`))
	req.Header.Set("Content-Type", "application/json")
	h(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "use the job's Kill action") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersKillHandlerPaneGoneOnFreshCheck(t *testing.T) {
	source := func() []*core.Peer {
		return []*core.Peer{{Pane: "%2", Name: "zeus", TmuxSession: "mobile"}}
	}
	old := paneSessionFunc
	paneSessionFunc = func(pane string) (string, error) { return "", fmt.Errorf("pane %s is gone", pane) }
	t.Cleanup(func() { paneSessionFunc = old })
	h := peersKillHandler(source)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/kill", strings.NewReader(`{"pane":"%2"}`))
	req.Header.Set("Content-Type", "application/json")
	h(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPeersKillHandlerMethodNotAllowed(t *testing.T) {
	h := peersKillHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/peers/kill", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPeersOpenHandlerNeitherPaneNorSlug(t *testing.T) {
	h := peersOpenHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/open", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPeersOpenHandlerBothPaneAndSlug(t *testing.T) {
	h := peersOpenHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/open", strings.NewReader(`{"pane":"%1","slug":"foo"}`))
	req.Header.Set("Content-Type", "application/json")
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPeersOpenHandlerUnknownPane(t *testing.T) {
	h := peersOpenHandler(func() []*core.Peer { return nil })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/open", strings.NewReader(`{"pane":"%9"}`))
	req.Header.Set("Content-Type", "application/json")
	h(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPeersSpawnHandlerBadKind(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/spawn", strings.NewReader(`{"kind":"bogus"}`))
	req.Header.Set("Content-Type", "application/json")
	peersSpawnHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPeersSpawnHandlerClaudeWithoutName(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/spawn", strings.NewReader(`{"kind":"claude"}`))
	req.Header.Set("Content-Type", "application/json")
	peersSpawnHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "claude spawn needs a name") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSpawnHandlerMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	h := httptest.NewRequest(http.MethodGet, "/api/peers/spawn", nil)
	peersSpawnHandler(w, h)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPeersSpawnHandlerBadDirIsBadRequest(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/spawn", strings.NewReader(`{"kind":"claude","name":"foo","dir":"/no/such/kraken-test-dir"}`))
	req.Header.Set("Content-Type", "application/json")
	peersSpawnHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not a directory") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestPeersSpawnHandlerRelativeDirIsBadRequest(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/spawn", strings.NewReader(`{"kind":"claude","name":"foo","dir":"relative/path"}`))
	req.Header.Set("Content-Type", "application/json")
	peersSpawnHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dir must be absolute or ~-relative") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestRequireJSONPost(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		ct         string
		wantStatus int
	}{
		{"missing content-type", http.MethodPost, "", http.StatusUnsupportedMediaType},
		{"text/plain rejected", http.MethodPost, "text/plain", http.StatusUnsupportedMediaType},
		{"wrong method", http.MethodGet, "application/json", http.StatusMethodNotAllowed},
		{"json accepted", http.MethodPost, "application/json", http.StatusOK},
		{"json with charset accepted", http.MethodPost, "application/json; charset=utf-8", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/api/peers/spawn", nil)
			if c.ct != "" {
				req.Header.Set("Content-Type", c.ct)
			}
			w := httptest.NewRecorder()
			ok := requireJSONPost(w, req)
			wantOK := c.wantStatus == http.StatusOK
			if ok != wantOK {
				t.Fatalf("ok = %v, want %v", ok, wantOK)
			}
			if !ok && w.Code != c.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, c.wantStatus)
			}
		})
	}
}

func TestIsCellPane(t *testing.T) {
	cases := []struct {
		session string
		want    bool
	}{
		{"cell-foo", true},
		{"cell-", true},
		{"mobile", false},
		{"", false},
		{"my-cell-thing", false},
	}
	for _, c := range cases {
		if got := isCellPane(c.session); got != c.want {
			t.Errorf("isCellPane(%q) = %v, want %v", c.session, got, c.want)
		}
	}
}
