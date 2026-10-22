package main

// fleet_contract_test.go — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice G5 (CLI: `kraken nodes`, node-qualified
// selectors for `k s` / `k r`). Authored from artifacts/50-contract.md alone;
// no implementation file was read. LOGIC dimension: happy paths. The
// serve-down hint, non-2xx exits and unknown-node selectors are athena-edge's.
//
// Fixtures are DECODED from wire JSON into nodesResponse, so the test never
// depends on nodeRowWire's Go field names — only on "nodeRowWire mirrors
// webNode" (its json tags).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const contractFleetNodesBody = `{"nodes":[
  {"node_id":"nOFFattic999CNTRL","label":"attic-mac","operator":"Sam","dns_name":"attic.tail072ad8.ts.net","ip":"100.70.1.9","port":7777,"build":"a","contract":7,"online":false,"last_ok":0},
  {"node_id":"nSTUDIOmini77CNTRL","label":"Studio-Mini","operator":"Jess","dns_name":"jess-studio.tail072ad8.ts.net","ip":"100.70.1.2","port":7777,"build":"b","contract":7,"online":true,"last_ok":1760000000000,
   "jobs":[{"slug":"a","state":"working"},{"slug":"b","state":"working"},{"slug":"c","state":"blocked"},{"slug":"d","state":"draft"},{"slug":"e","state":"done"},{"slug":"f","state":"done"},{"slug":"g","state":"done"}]}
]}`

func contractFleetNodes(t *testing.T) nodesResponse {
	t.Helper()
	var resp nodesResponse
	if err := json.Unmarshal([]byte(contractFleetNodesBody), &resp); err != nil {
		t.Fatalf("fixture does not decode into nodesResponse: %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("decoded %d nodes, want 2", len(resp.Nodes))
	}
	return resp
}

// contractFleetNodeID reads node_id back off a *nodeRowWire via its json tags.
func contractFleetNodeID(t *testing.T, n *nodeRowWire) string {
	t.Helper()
	if n == nil {
		return ""
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	id, _ := m["node_id"].(string)
	return id
}

var contractFleetCols = regexp.MustCompile(`\s{2,}`)

// Contract G5 `renderNodesTable`: "Header NODE  OPERATOR  DNS  ONLINE  WORKING
// BLOCKED  DRAFT  DONE  LAST OK; one row per node, online first". Columns are
// read as runs separated by two or more spaces (the header's own separator).
func TestContractFleetRenderNodesTable(t *testing.T) {
	out := renderNodesTable(contractFleetNodes(t))
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("table has %d non-empty lines, want header + 2 rows:\n%s", len(lines), out)
	}
	header := contractFleetCols.Split(strings.TrimSpace(lines[0]), -1)
	wantHeader := []string{"NODE", "OPERATOR", "DNS", "ONLINE", "WORKING", "BLOCKED", "DRAFT", "DONE", "LAST OK"}
	if !reflect.DeepEqual(header, wantHeader) {
		t.Errorf("header columns = %q, want %q", header, wantHeader)
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[1]), "Studio-Mini") {
		t.Errorf("first row = %q, want the ONLINE node Studio-Mini first", lines[1])
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[2]), "attic-mac") {
		t.Errorf("second row = %q, want the offline attic-mac", lines[2])
	}
	row := contractFleetCols.Split(strings.TrimSpace(lines[1]), -1)
	if len(row) < 8 {
		t.Fatalf("online row has %d columns: %q", len(row), row)
	}
	if row[1] != "Jess" || row[2] != "jess-studio.tail072ad8.ts.net" {
		t.Errorf("online row operator/dns = %q/%q", row[1], row[2])
	}
	if got := row[4:8]; !reflect.DeepEqual(got, []string{"2", "1", "1", "3"}) {
		t.Errorf("WORKING/BLOCKED/DRAFT/DONE = %q, want [2 1 1 3]", got)
	}
}

// "`no nodes on the tailnet` when empty".
func TestContractFleetRenderNodesTableEmpty(t *testing.T) {
	out := renderNodesTable(nodesResponse{})
	if !strings.Contains(out, "no nodes on the tailnet") {
		t.Errorf("empty registry rendered %q, want it to say no nodes on the tailnet", out)
	}
}

// Contract G5 `splitNodeSelector`: "ok only when sel contains / and the text
// before the FIRST / matches a node's Label (case-insensitive), the first label
// of its DNSName, or a prefix (≥4 chars) of its NodeID; slug is everything
// after that /."
func TestContractFleetSplitNodeSelectorMatches(t *testing.T) {
	resp := contractFleetNodes(t)
	for _, c := range []struct {
		sel, wantID, wantSlug string
	}{
		{"studio-mini/tech-comms", "nSTUDIOmini77CNTRL", "tech-comms"}, // label, case-insensitive
		{"Studio-Mini/tech-comms", "nSTUDIOmini77CNTRL", "tech-comms"}, // label, exact
		{"jess-studio/tech-comms", "nSTUDIOmini77CNTRL", "tech-comms"}, // first DNS label
		{"nSTU/tech-comms", "nSTUDIOmini77CNTRL", "tech-comms"},        // 4-char id prefix
		{"attic-mac/a/b", "nOFFattic999CNTRL", "a/b"},                  // split on the FIRST /
	} {
		node, slug, ok := splitNodeSelector(c.sel, resp.Nodes)
		if !ok {
			t.Errorf("%q: ok = false, want a match", c.sel)
			continue
		}
		if id := contractFleetNodeID(t, node); id != c.wantID {
			t.Errorf("%q: node = %q, want %q", c.sel, id, c.wantID)
		}
		if slug != c.wantSlug {
			t.Errorf("%q: slug = %q, want %q", c.sel, slug, c.wantSlug)
		}
	}
}

// "A selector without /, or whose left half matches no node, returns ok ==
// false and leaves local resolution untouched."
func TestContractFleetSplitNodeSelectorLocal(t *testing.T) {
	resp := contractFleetNodes(t)
	for _, sel := range []string{"tech-comms", "3", "feature/branchy"} {
		if _, _, ok := splitNodeSelector(sel, resp.Nodes); ok {
			t.Errorf("%q: ok = true, want the local meaning", sel)
		}
	}
}

// contractFleetServe is a fake loopback `kraken serve`: it answers
// /api/nodes with the fixture and records every other request.
type contractFleetServe struct {
	mu     sync.Mutex
	method string
	path   string
	query  map[string]string
	body   []byte
	header http.Header
	reply  string
	nodesN int
}

func (f *contractFleetServe) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/nodes" {
			f.mu.Lock()
			f.nodesN++
			f.mu.Unlock()
			io.WriteString(w, contractFleetNodesBody)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.method, f.path, f.body, f.header = r.Method, r.URL.Path, b, r.Header.Clone()
		f.query = map[string]string{}
		for k := range r.URL.Query() {
			f.query[k] = r.URL.Query().Get(k)
		}
		reply := f.reply
		f.mu.Unlock()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
}

// Contract G5 `cmdSend`: "When splitNodeSelector says ok …: POST
// /api/nodes/action {node, slug, action:"steer", payload:{text}} with header
// X-Kraken-From-Cell: <slug> … the CLI reads KRAKEN_JOB_SLUG". `node` is the
// registry node's id (the proxy's lookup key, G4 `nodesActionHandler`).
func TestContractFleetSendToRemoteNode(t *testing.T) {
	f := &contractFleetServe{reply: `{"ok":true}`}
	f.start(t)
	t.Setenv("KRAKEN_JOB_SLUG", "agent-cell")

	cmdSend([]string{"studio-mini/tech-comms", "hello from a"})

	if f.method != http.MethodPost || f.path != "/api/nodes/action" {
		t.Fatalf("serve saw %s %s, want POST /api/nodes/action", f.method, f.path)
	}
	var got map[string]any
	if err := json.Unmarshal(f.body, &got); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, f.body)
	}
	want := map[string]any{
		"node":    "nSTUDIOmini77CNTRL",
		"slug":    "tech-comms",
		"action":  "steer",
		"payload": map[string]any{"text": "hello from a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("posted %v, want %v", got, want)
	}
	if h := f.header.Get("X-Kraken-From-Cell"); h != "agent-cell" {
		t.Errorf("X-Kraken-From-Cell = %q, want agent-cell", h)
	}
}

// "empty outside a cell": with no KRAKEN_JOB_SLUG the header carries nothing.
func TestContractFleetSendOutsideCellHasNoFromCell(t *testing.T) {
	f := &contractFleetServe{reply: `{"ok":true}`}
	f.start(t)
	t.Setenv("KRAKEN_JOB_SLUG", "")
	os.Unsetenv("KRAKEN_JOB_SLUG")

	cmdSend([]string{"studio-mini/tech-comms", "hi"})

	if f.path != "/api/nodes/action" {
		t.Fatalf("serve saw %s, want /api/nodes/action", f.path)
	}
	if h := f.header.Get("X-Kraken-From-Cell"); h != "" {
		t.Errorf("outside a cell X-Kraken-From-Cell = %q, want empty", h)
	}
}

// Contract G5 `cmdRead`: "Same split; GET /api/nodes/read?node=&slug=&n=".
func TestContractFleetReadFromRemoteNode(t *testing.T) {
	f := &contractFleetServe{reply: `{"blocks":[{"kind":"asst","text":"working on it"}],"counted":1}`}
	f.start(t)

	cmdRead([]string{"studio-mini/tech-comms"})

	if f.method != http.MethodGet || f.path != "/api/nodes/read" {
		t.Fatalf("serve saw %s %s, want GET /api/nodes/read", f.method, f.path)
	}
	if f.query["node"] != "nSTUDIOmini77CNTRL" || f.query["slug"] != "tech-comms" {
		t.Errorf("query = %v, want node=nSTUDIOmini77CNTRL slug=tech-comms", f.query)
	}
	if _, ok := f.query["n"]; !ok {
		t.Errorf("query = %v, want an n parameter", f.query)
	}
}
