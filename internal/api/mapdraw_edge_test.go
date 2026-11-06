package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// mapdraw_edge_test.go: athena-edge's blind suite for /api/map/drawings
// (50-contract.md A2): every 400, 403, 405 and 413 the route promises, that
// nothing is stored on any of them, the 2 MiB body bound at and just past
// its limit, never-null empty answers, and no text key on a non-text drawing.

const mapDrawEdgePath = "/api/map/drawings"

const mapDrawEdgeLineJSON = `{"kind":"line","pts":[0,0,10,10],"size":2}`

func mapDrawEdgeMux(t *testing.T) (*http.ServeMux, *store.Store) {
	t.Helper()
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	return mux, st
}

func mapDrawEdgeDo(t *testing.T, mux http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, mapDrawEdgePath, nil)
	} else {
		req = httptest.NewRequest(method, mapDrawEdgePath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// mapDrawEdgeSeed saves one drawing, "seed", through the jobs package.
func mapDrawEdgeSeed(t *testing.T) {
	t.Helper()
	if err := jobs.SetMapDrawings(map[string]jobs.MapDrawing{"seed": {Kind: "line", Pts: []float64{0, 0, 10, 10}, Size: 2}}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// mapDrawEdgeOnlySeed asserts the saved drawings are exactly the seed.
func mapDrawEdgeOnlySeed(t *testing.T, name string) {
	t.Helper()
	got := jobs.MapDrawings()
	if _, ok := got["seed"]; !ok || len(got) != 1 {
		t.Errorf("%s: saved drawings = %v, want only seed", name, got)
	}
}

// mapDrawEdgeErrorBody asserts a JSON body {"error": "<non-empty text>"}.
func mapDrawEdgeErrorBody(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Errorf("%s: body %q is not JSON: %v", name, rec.Body.String(), err)
		return
	}
	msg, ok := out["error"].(string)
	if !ok || msg == "" {
		t.Errorf("%s: body %q, want {\"error\": \"<text>\"}", name, rec.Body.String())
	}
}

// --- methods ------------------------------------------------------------------

func TestMapDrawEdgeOtherMethods405(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		if rec := mapDrawEdgeDo(t, mux, m, `{"remove":["seed"]}`); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d %s, want 405", m, rec.Code, rec.Body.String())
		}
	}
	mapDrawEdgeOnlySeed(t, "after other methods")
}

func TestMapDrawEdgeHead405(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	if rec := mapDrawEdgeDo(t, mux, http.MethodHead, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("HEAD: %d, want 405", rec.Code)
	}
}

// --- GET ------------------------------------------------------------------------

func mapDrawEdgeGetDrawings(t *testing.T, mux http.Handler) map[string]json.RawMessage {
	t.Helper()
	rec := mapDrawEdgeDo(t, mux, http.MethodGet, "")
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
	if strings.TrimSpace(string(raw)) == "null" {
		t.Fatalf("GET drawings is null, want an object")
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("GET drawings %s is not an object: %v", raw, err)
	}
	return out
}

func TestMapDrawEdgeGetEmptyIsObject(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	rec := mapDrawEdgeDo(t, mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("GET body %q: %v", rec.Body.String(), err)
	}
	if got := strings.TrimSpace(string(top["drawings"])); got != "{}" {
		t.Fatalf("drawings = %s, want {}", got)
	}
}

func TestMapDrawEdgeGetAfterLastRemoveIsObject(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	if rec := mapDrawEdgeDo(t, mux, http.MethodPost, `{"remove":["seed"]}`); rec.Code != http.StatusOK {
		t.Fatalf("remove seed: %d %s", rec.Code, rec.Body.String())
	}
	if got := mapDrawEdgeGetDrawings(t, mux); len(got) != 0 {
		t.Fatalf("drawings = %v, want {}", got)
	}
}

func TestMapDrawEdgeGetCorruptRowIsEmptyObject(t *testing.T) {
	mux, st := mapDrawEdgeMux(t)
	if err := st.SetKV("map_drawings", "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := mapDrawEdgeGetDrawings(t, mux); len(got) != 0 {
		t.Fatalf("drawings = %v, want {}", got)
	}
}

func TestMapDrawEdgeGetLeavesOutInvalidStoredEntry(t *testing.T) {
	mux, st := mapDrawEdgeMux(t)
	if err := st.SetKV("map_drawings", `{"good":`+mapDrawEdgeLineJSON+`,"bad":{"kind":"star","pts":[0,0,1,1],"size":2}}`); err != nil {
		t.Fatal(err)
	}
	got := mapDrawEdgeGetDrawings(t, mux)
	if _, ok := got["good"]; !ok || len(got) != 1 {
		t.Fatalf("drawings = %v, want only good", got)
	}
}

// A non-text drawing carries no text key, even one stored with "text":"".
func TestMapDrawEdgeGetNoTextKeyOnNonText(t *testing.T) {
	mux, st := mapDrawEdgeMux(t)
	if err := st.SetKV("map_drawings", `{"p":{"kind":"pen","pts":[0,0,1,1],"size":2,"text":""}}`); err != nil {
		t.Fatal(err)
	}
	got := mapDrawEdgeGetDrawings(t, mux)
	raw, ok := got["p"]
	if !ok {
		t.Fatalf("drawings = %v, want p", got)
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if _, has := d["text"]; has {
		t.Fatalf("pen drawing %s carries a text key", raw)
	}
}

func TestMapDrawEdgePostEmptyTextOnLineHasNoTextKey(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	body := `{"set":{"l":{"kind":"line","pts":[0,0,1,1],"size":2,"text":""}}}`
	if rec := mapDrawEdgeDo(t, mux, http.MethodPost, body); rec.Code != http.StatusOK {
		t.Fatalf("line with text \"\": %d %s, want 200", rec.Code, rec.Body.String())
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(mapDrawEdgeGetDrawings(t, mux)["l"], &d); err != nil {
		t.Fatal(err)
	}
	if _, has := d["text"]; has {
		t.Fatal("line drawing carries a text key")
	}
}

// --- POST success shapes ----------------------------------------------------------

func TestMapDrawEdgePostRemoveOnlyUnsavedOK(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	rec := mapDrawEdgeDo(t, mux, http.MethodPost, `{"remove":["ghost"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove of an unsaved id: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["ok"] != true || len(out) != 1 {
		t.Fatalf("body %q, want {\"ok\": true}", rec.Body.String())
	}
	mapDrawEdgeOnlySeed(t, "after no-op remove")
}

func TestMapDrawEdgePostSetOnlyOK(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	if rec := mapDrawEdgeDo(t, mux, http.MethodPost, `{"set":{"a":`+mapDrawEdgeLineJSON+`}}`); rec.Code != http.StatusOK {
		t.Fatalf("set only: %d %s, want 200", rec.Code, rec.Body.String())
	}
	if _, ok := jobs.MapDrawings()["a"]; !ok {
		t.Fatal("a not saved")
	}
}

// --- 400s -------------------------------------------------------------------------

func TestMapDrawEdgeBadBodies400(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	long := strings.Repeat("a", 65)
	many := make([]string, 1001)
	for i := range many {
		many[i] = fmt.Sprintf(`"g%04d"`, i)
	}
	for _, tc := range []struct{ name, body string }{
		{"empty body", ""},
		{"malformed", `{"set":`},
		{"array", `[]`},
		{"string", `"set"`},
		{"number", `5`},
		{"empty object", `{}`},
		{"both empty", `{"set":{},"remove":[]}`},
		{"set is an array", `{"set":[]}`},
		{"set is a string", `{"set":"x"}`},
		{"remove is a string", `{"remove":"seed"}`},
		{"remove holds a number", `{"remove":[1]}`},
		{"unknown top key", `{"remove":["seed"],"extra":1}`},
		{"unknown drawing key", `{"set":{"a":{"kind":"line","pts":[0,0,1,1],"size":2,"color":"red"}}}`},
		{"drawing is a string", `{"set":{"a":"line"}}`},
		{"pts as strings", `{"set":{"a":{"kind":"line","pts":["0","0","1","1"],"size":2}}}`},
		{"size as string", `{"set":{"a":{"kind":"line","pts":[0,0,1,1],"size":"2"}}}`},
		{"bad kind", `{"set":{"a":{"kind":"star","pts":[0,0,1,1],"size":2}}}`},
		{"zero size", `{"set":{"a":{"kind":"line","pts":[0,0,1,1],"size":0}}}`},
		{"size over 1000", `{"set":{"a":{"kind":"line","pts":[0,0,1,1],"size":1000.5}}}`},
		{"coordinate over cap", `{"set":{"a":{"kind":"line","pts":[0,0,1,10000001],"size":2}}}`},
		{"short pts", `{"set":{"a":{"kind":"rect","pts":[0,0,1],"size":2}}}`},
		{"blank text", `{"set":{"a":{"kind":"text","pts":[0,0],"size":18,"text":" \n "}}}`},
		{"text on a line", `{"set":{"a":{"kind":"line","pts":[0,0,1,1],"size":2,"text":"x"}}}`},
		{"empty id", `{"set":{"":` + mapDrawEdgeLineJSON + `}}`},
		{"65-byte id", `{"set":{"` + long + `":` + mapDrawEdgeLineJSON + `}}`},
		{"empty remove id", `{"remove":[""]}`},
		{"id in both", `{"set":{"seed":` + mapDrawEdgeLineJSON + `},"remove":["seed"]}`},
		{"1001 removes", `{"remove":[` + strings.Join(many, ",") + `]}`},
		{"good set beside a bad remove", `{"set":{"b":` + mapDrawEdgeLineJSON + `},"remove":["` + long + `"]}`},
	} {
		rec := mapDrawEdgeDo(t, mux, http.MethodPost, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.name, rec.Code, rec.Body.String())
			continue
		}
		mapDrawEdgeErrorBody(t, tc.name, rec)
	}
	mapDrawEdgeOnlySeed(t, "after every 400")
}

func TestMapDrawEdgeOverCap400(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	var b strings.Builder
	b.WriteString(`{"set":{`)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"n%04d":%s`, i, mapDrawEdgeLineJSON)
	}
	b.WriteString(`}}`)
	rec := mapDrawEdgeDo(t, mux, http.MethodPost, b.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("seed + 1000 new: %d %s, want 400", rec.Code, rec.Body.String())
	}
	mapDrawEdgeErrorBody(t, "over cap", rec)
	mapDrawEdgeOnlySeed(t, "after the over-cap 400")
}

// --- Amendment 1: AM2 -----------------------------------------------------------

func TestMapDrawEdgeAM2TrailingDataIs400(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	for _, tc := range []struct{ name, body string }{
		{"a trailing word", `{"remove":["a"]} x`},
		{"a second object removing seed", `{"remove":["ghost"]}{"remove":["seed"]}`},
		{"a trailing number", `{"remove":["ghost"]} 5`},
		{"a trailing brace", `{"set":{"n":` + mapDrawEdgeLineJSON + `}}}`},
	} {
		rec := mapDrawEdgeDo(t, mux, http.MethodPost, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.name, rec.Code, rec.Body.String())
			continue
		}
		mapDrawEdgeErrorBody(t, tc.name, rec)
	}
	mapDrawEdgeOnlySeed(t, "after trailing-data bodies")
}

// --- 413 --------------------------------------------------------------------------

// mapDrawEdgePadded is a valid body setting "big", padded with spaces inside
// the remove list to exactly n bytes.
func mapDrawEdgePadded(t *testing.T, n int) string {
	t.Helper()
	head := `{"set":{"big":` + mapDrawEdgeLineJSON + `},"remove":["ghost"`
	tail := `]}`
	pad := n - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("cannot pad to %d bytes", n)
	}
	body := head + strings.Repeat(" ", pad) + tail
	if len(body) != n {
		t.Fatalf("padded body is %d bytes, want %d", len(body), n)
	}
	return body
}

func TestMapDrawEdgeBodyAtTwoMiBAccepted(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	rec := mapDrawEdgeDo(t, mux, http.MethodPost, mapDrawEdgePadded(t, 2<<20))
	if rec.Code != http.StatusOK {
		t.Fatalf("2 MiB body: %d %.200s, want 200", rec.Code, rec.Body.String())
	}
	if _, ok := jobs.MapDrawings()["big"]; !ok {
		t.Fatal("big not saved")
	}
}

func TestMapDrawEdgeBodyOverTwoMiB413(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	rec := mapDrawEdgeDo(t, mux, http.MethodPost, mapDrawEdgePadded(t, 2<<20+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MiB + 1 body: %d %.200s, want 413", rec.Code, rec.Body.String())
	}
	mapDrawEdgeOnlySeed(t, "after the 413")
}

// --- mirror -------------------------------------------------------------------------

func TestMapDrawEdgeMirrorPost403StoresNothing(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	SetMirrorMode(true)
	for _, tc := range []struct{ name, body string }{
		{"valid set", `{"set":{"a":` + mapDrawEdgeLineJSON + `}}`},
		{"valid remove", `{"remove":["seed"]}`},
		{"malformed", `{"set":`},
	} {
		if rec := mapDrawEdgeDo(t, mux, http.MethodPost, tc.body); rec.Code != http.StatusForbidden {
			t.Errorf("mirror %s: %d %s, want 403", tc.name, rec.Code, rec.Body.String())
		}
	}
	SetMirrorMode(false)
	mapDrawEdgeOnlySeed(t, "after mirror POSTs")
}

func TestMapDrawEdgeMirrorGet200(t *testing.T) {
	mux, _ := mapDrawEdgeMux(t)
	mapDrawEdgeSeed(t)
	SetMirrorMode(true)
	got := mapDrawEdgeGetDrawings(t, mux)
	if _, ok := got["seed"]; !ok || len(got) != 1 {
		t.Fatalf("mirror GET drawings = %v, want only seed", got)
	}
}
