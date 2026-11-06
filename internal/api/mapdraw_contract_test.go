package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// mapdraw_contract_test.go — athena-logic's blind tests for the drawings route
// (50-contract.md A2): the GET's answer shape, the POST's success path, one
// representative 400 and the mirror gate. Requests go through the real mux
// (Register) from a loopback address, the owner tier.

const mapDrawContractLocal = "127.0.0.1:54321"

func mapDrawContractMux(t *testing.T) *http.ServeMux {
	t.Helper()
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func mapDrawContractDo(t *testing.T, mux http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/map/drawings", nil)
	} else {
		req = httptest.NewRequest(method, "/api/map/drawings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = mapDrawContractLocal
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// mapDrawContractGetDrawings GETs the route and returns each drawing's raw
// object, keyed by id.
func mapDrawContractGetDrawings(t *testing.T, mux http.Handler) (string, map[string]map[string]json.RawMessage) {
	t.Helper()
	rec := mapDrawContractDo(t, mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("GET body %q: %v", rec.Body.String(), err)
	}
	raw, ok := top["drawings"]
	if !ok {
		t.Fatalf("GET body %q has no drawings key", rec.Body.String())
	}
	var out map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("drawings %s: %v", raw, err)
	}
	return strings.TrimSpace(string(raw)), out
}

func TestMapDrawContractGetEmptyIsObject(t *testing.T) {
	mux := mapDrawContractMux(t)
	raw, _ := mapDrawContractGetDrawings(t, mux)
	if raw != "{}" {
		t.Fatalf("drawings = %s, want {}", raw)
	}
}

// A2.1: the GET serves what is saved, and a non-text drawing has no text key.
func TestMapDrawContractGetServesSaved(t *testing.T) {
	mux := mapDrawContractMux(t)
	if err := jobs.SetMapDrawings(map[string]jobs.MapDrawing{
		"l1": {Kind: "line", Pts: []float64{1, 2, 3, 4}, Size: 2},
		"t1": {Kind: "text", Pts: []float64{5, 6}, Size: 18, Text: "hi\nthere"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, got := mapDrawContractGetDrawings(t, mux)
	if len(got) != 2 {
		t.Fatalf("drawings = %v, want 2", got)
	}
	l1 := got["l1"]
	if string(l1["kind"]) != `"line"` || string(l1["pts"]) != `[1,2,3,4]` || string(l1["size"]) != `2` {
		t.Errorf("l1 = %v", l1)
	}
	if _, has := l1["text"]; has {
		t.Errorf("line l1 carries a text key: %v", l1)
	}
	t1 := got["t1"]
	if string(t1["kind"]) != `"text"` || string(t1["pts"]) != `[5,6]` || string(t1["size"]) != `18` || string(t1["text"]) != `"hi\nthere"` {
		t.Errorf("t1 = %v", t1)
	}
}

// A2.2: a POST with set and remove answers {"ok": true} and is applied.
func TestMapDrawContractPostSetAndRemove(t *testing.T) {
	mux := mapDrawContractMux(t)
	if err := jobs.SetMapDrawings(map[string]jobs.MapDrawing{
		"old":  {Kind: "rect", Pts: []float64{0, 0, 10, 10}, Size: 1},
		"keep": {Kind: "ellipse", Pts: []float64{0, 0, 20, 10}, Size: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	rec := mapDrawContractDo(t, mux, http.MethodPost,
		`{"set":{"new":{"kind":"pen","pts":[0,0,5,5,10,0],"size":3}},"remove":["old"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var ok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("POST body %q: %v", rec.Body.String(), err)
	}
	if !reflect.DeepEqual(ok, map[string]any{"ok": true}) {
		t.Errorf("POST body = %v, want {ok: true}", ok)
	}
	want := map[string]jobs.MapDrawing{
		"keep": {Kind: "ellipse", Pts: []float64{0, 0, 20, 10}, Size: 1},
		"new":  {Kind: "pen", Pts: []float64{0, 0, 5, 5, 10, 0}, Size: 3},
	}
	if got := jobs.MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

// A2.2: either key is optional.
func TestMapDrawContractPostSetOnly(t *testing.T) {
	mux := mapDrawContractMux(t)
	rec := mapDrawContractDo(t, mux, http.MethodPost, `{"set":{"t":{"kind":"text","pts":[1,2],"size":18,"text":"note"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s, want 200", rec.Code, rec.Body.String())
	}
	want := map[string]jobs.MapDrawing{"t": {Kind: "text", Pts: []float64{1, 2}, Size: 18, Text: "note"}}
	if got := jobs.MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

func TestMapDrawContractPostRemoveOnly(t *testing.T) {
	mux := mapDrawContractMux(t)
	if err := jobs.SetMapDrawings(map[string]jobs.MapDrawing{
		"a": {Kind: "line", Pts: []float64{0, 0, 1, 1}, Size: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	rec := mapDrawContractDo(t, mux, http.MethodPost, `{"remove":["a","never-saved"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := jobs.MapDrawings(); len(got) != 0 {
		t.Fatalf("MapDrawings() = %#v, want empty", got)
	}
}

// A2.1 + A2.2: what a POST saves, the GET serves back.
func TestMapDrawContractPostThenGet(t *testing.T) {
	mux := mapDrawContractMux(t)
	rec := mapDrawContractDo(t, mux, http.MethodPost,
		`{"set":{"r":{"kind":"rect","pts":[50,60,10,20],"size":1.5}},"remove":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}
	_, got := mapDrawContractGetDrawings(t, mux)
	r := got["r"]
	if len(got) != 1 || string(r["kind"]) != `"rect"` || string(r["pts"]) != `[50,60,10,20]` || string(r["size"]) != `1.5` {
		t.Fatalf("drawings = %v", got)
	}
	if _, has := r["text"]; has {
		t.Errorf("rect carries a text key: %v", r)
	}
}

// A2.3, one representative case: a refusal by SetMapDrawings is a 400 with
// {"error": text}, and nothing is stored.
func TestMapDrawContractPostRefusalIs400(t *testing.T) {
	mux := mapDrawContractMux(t)
	rec := mapDrawContractDo(t, mux, http.MethodPost, `{"set":{"bad":{"kind":"star","pts":[0,0,1,1],"size":1}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST: %d %s, want 400", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("400 body %q is not JSON: %v", rec.Body.String(), err)
	}
	var msg string
	if err := json.Unmarshal(body["error"], &msg); err != nil || msg == "" {
		t.Errorf("400 body %q: error = %s, want a non-empty string", rec.Body.String(), body["error"])
	}
	if got := jobs.MapDrawings(); len(got) != 0 {
		t.Fatalf("MapDrawings() = %#v after a 400, want empty", got)
	}
}

// A2.5: under mirror mode the POST is 403 and stores nothing; the GET is 200.
func TestMapDrawContractMirrorGate(t *testing.T) {
	mux := mapDrawContractMux(t)
	if err := jobs.SetMapDrawings(map[string]jobs.MapDrawing{
		"a": {Kind: "line", Pts: []float64{0, 0, 1, 1}, Size: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	SetMirrorMode(true)
	rec := mapDrawContractDo(t, mux, http.MethodPost, `{"set":{"b":{"kind":"line","pts":[0,0,2,2],"size":1}},"remove":["a"]}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("mirror POST: %d %s, want 403", rec.Code, rec.Body.String())
	}
	_, got := mapDrawContractGetDrawings(t, mux)
	if len(got) != 1 || got["a"] == nil {
		t.Fatalf("mirror GET drawings = %v, want only a", got)
	}
}

// A2.6: the route is documented in ios/API.md, and CLAUDE.md names the route
// and the kv row.
func TestMapDrawContractDocumented(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	api := read(filepath.Join("ios", "API.md"))
	if !strings.Contains(api, "/api/map/drawings") {
		t.Error("ios/API.md does not name /api/map/drawings")
	}
	if !strings.Contains(api, "/api/map/positions") {
		t.Error("ios/API.md does not name /api/map/positions")
	}
	claude := read("CLAUDE.md")
	if !strings.Contains(claude, "/api/map/drawings") {
		t.Error("CLAUDE.md does not name /api/map/drawings")
	}
	if !strings.Contains(claude, "map_drawings") {
		t.Error("CLAUDE.md does not name the map_drawings kv row")
	}
}
