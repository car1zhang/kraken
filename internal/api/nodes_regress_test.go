package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// Regression pins for defects fixed in review rounds 1-2 (not spec tests):
// an announce bound to the body rather than the caller, a relay that echoed a
// sibling's Content-Type, and a board (or /api/nodes) that rendered a stale
// registry with the fleet switched off or repeated fields a sibling should not
// have served.

// regressRequest is a request carrying a gate classification, as Gate would
// attach it.
func regressRequest(method, path, body string, tier Tier, id core.TailnetIdentity) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "100.64.1.2:4242"
	ctx := context.WithValue(r.Context(), gateTierKey, tier)
	ctx = context.WithValue(ctx, gateIdentityKey, id)
	return r.WithContext(ctx)
}

func TestNodesRegressAnnounceRefusesAnIDTheCallerDoesNotHold(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "")
	orig := nodeSelf
	nodeSelf = func() jobs.NodeEnvelope { return jobs.NodeEnvelope{ID: "self-id", IPs: []string{}} }
	t.Cleanup(func() { nodeSelf = orig })

	body := `{"node_id":"victim-id","label":"x","port":7777}`
	rec := httptest.NewRecorder()
	nodeAnnounceHandler(rec, regressRequest(http.MethodPost, "/api/node/announce", body, TierNode, core.TailnetIdentity{ID: "caller-id"}))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "node id does not match caller") {
		t.Fatalf("announce of another node's id = %d %s, want 403 node id does not match caller", rec.Code, rec.Body.String())
	}
	if _, err := st.GetNode("victim-id"); err == nil {
		t.Fatal("a refused announce still recorded a row")
	}

	// the caller's own id is recorded, under the caller's address
	rec = httptest.NewRecorder()
	nodeAnnounceHandler(rec, regressRequest(http.MethodPost, "/api/node/announce",
		`{"node_id":"caller-id","label":"x","port":7777,"ips":["100.99.99.99"]}`, TierNode, core.TailnetIdentity{ID: "caller-id"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("own-id announce = %d %s, want 200", rec.Code, rec.Body.String())
	}
	n, err := st.GetNode("caller-id")
	if err != nil || n.IP != "100.64.1.2" {
		t.Fatalf("recorded row = %+v, %v; want ip 100.64.1.2 (the caller's, not the body's)", n, err)
	}
}

func TestNodesRegressRelayForcesJSON(t *testing.T) {
	orig := nodesProxyDo
	t.Cleanup(func() { nodesProxyDo = orig })
	node := &store.NodeRow{ID: "n1", Label: "studio"}
	req := httptest.NewRequest(http.MethodGet, "http://100.64.1.3:7777/api/node/read", nil)

	reply := func(status int, ct, body string) {
		nodesProxyDo = func(*http.Request, time.Duration) (*http.Response, error) {
			h := http.Header{}
			h.Set("Content-Type", ct)
			return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	}

	reply(http.StatusOK, "text/html", `{"ok":true}`)
	rec := httptest.NewRecorder()
	relay(rec, node, req, time.Second)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("relayed JSON = %d, CT %q, nosniff %q; want 200 application/json nosniff",
			rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("X-Content-Type-Options"))
	}

	reply(http.StatusOK, "text/html", `<script>alert(1)</script>`)
	rec = httptest.NewRecorder()
	relay(rec, node, req, time.Second)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "studio answered non-JSON") {
		t.Fatalf("non-JSON body = %d %s, want 502 naming the sibling", rec.Code, rec.Body.String())
	}

	reply(http.StatusUnauthorized, "application/json", `{"error":"unauthorized"}`)
	rec = httptest.NewRecorder()
	relay(rec, node, req, time.Second)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "did not recognise this node") {
		t.Fatalf("sibling 401 = %d %s, want 502", rec.Code, rec.Body.String())
	}
}

func TestNodesRegressFleetOffShowsNoRemoteCards(t *testing.T) {
	st := useTestStore(t)
	if err := st.UpsertNode(&store.NodeRow{ID: "n1", Label: "studio", Online: true, LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodePoll("n1", true, "", `[{"slug":"a","visibility":"public"}]`, 2); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KRAKEN_NODES", "")
	if cards, nodes := fleetBoard(st); len(cards) != 1 || len(nodes) != 1 || cards[0].Owner == nil {
		t.Fatalf("fleet on: %d cards, %d nodes; want 1 owned card and 1 node", len(cards), len(nodes))
	}

	// /api/nodes with the fleet on re-encodes the stored list through the
	// strip: a smuggled threads field never reaches the client
	if err := st.SetNodePoll("n1", true, "", `[{"slug":"a","visibility":"public","threads":[{"tid":"x"}]},"bad"]`, 3); err != nil {
		t.Fatal(err)
	}
	onRec := httptest.NewRecorder()
	nodesHandler(onRec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	if body := onRec.Body.String(); strings.Contains(body, "threads") || !strings.Contains(body, `"slug":"a"`) {
		t.Fatalf("GET /api/nodes fleet on = %s, want card a with no threads key", body)
	}

	t.Setenv("KRAKEN_NODES", "off")
	if cards, nodes := fleetBoard(st); len(cards) != 0 || len(nodes) != 0 {
		t.Fatalf("fleet off: %d cards, %d nodes; want none", len(cards), len(nodes))
	}
	rec := httptest.NewRecorder()
	nodesHandler(rec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	var resp struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Nodes == nil || len(resp.Nodes) != 0 {
		t.Fatalf("GET /api/nodes with fleet off = %s, want {\"nodes\":[]}", rec.Body.String())
	}
}
