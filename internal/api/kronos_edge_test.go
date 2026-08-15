package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/store"
)

// kronos_edge_test.go — the EDGE dimension of Slice D of
// artifacts/50-contract.md (the kronos build): every stated refusal on the new
// routes, asserted as BOTH a status code and "nothing was written", plus the
// leak rule (bookkeeping columns withheld from the inbox wire) and the mirror
// gate. Happy paths and the stated wire invariants belong to the sibling blind
// file.
//
// POST /api/imsg/send and the status payload's `imsg_configured` key went with
// the iMessage bridge (carl's call at the manual gate), and their refusal and
// leak tests went with them.
//
// Written blind: no byte of internal/api/orch.go, internal/api/register.go or
// any new cmd/kraken/cmd_*.go was read while authoring this.
//
// Every route is driven through the real mux (api.Register), the same wiring
// web.Serve uses — the contract freezes ROUTES rather than handler symbols, so
// the mux is the only surface it actually names.
//
// Every package-level identifier declared here is prefixed `edgeKronos` — this
// package's test namespace is shared with the other blind author and with the
// pre-existing `edge*` files.

// --- harness ----------------------------------------------------------------

// edgeKronosAPI wires a temp store (useTestStore sandboxes HOME, so
// ~/.kraken/kronos.env is absent) and returns it beside a mux carrying every
// registered route.
func edgeKronosAPI(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	return st, mux
}

func edgeKronosPost(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return edgeKronosPostRaw(t, mux, path, string(mustJSON(t, body)))
}

func edgeKronosPostRaw(t *testing.T, mux http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeKronosGet(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// edgeKronosSeedInbox enqueues n rows straight through the store, so a route
// test does not depend on the daemon-side enqueue helper.
func edgeKronosSeedInbox(t *testing.T, st *store.Store, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, err := st.AddOrchItem("job-blocked", "some-job", "blocked on carl", int64(1_769_040_000_000+i))
		if err != nil {
			t.Fatalf("AddOrchItem #%d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// edgeKronosUnackedCount reads how many rows are still open.
func edgeKronosUnackedCount(t *testing.T, st *store.Store) int {
	t.Helper()
	rows, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	return len(rows)
}

// edgeKronosBodyMap decodes a response body into a generic map so a test can
// assert on the PRESENCE of a key rather than on a typed zero value — the
// distinction /api/kronos' `context` pointer turns on.
func edgeKronosBodyMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not a JSON object (%d): %v\n%s", rec.Code, err, rec.Body.String())
	}
	return out
}

// --- GET /api/orch/inbox ----------------------------------------------------

// TestEdgeKronosInboxWithholdsBookkeepingColumns is iris' webSlackMsg rule
// applied here: delivered_at, acked_at and disposition are KRAKEN's record of
// what it did, not the escalation. Serving them would let kronos read back its
// own past dispositions as if they were memory — and its memory is the rows
// and the graph, not a field it can re-interpret.
func TestEdgeKronosInboxWithholdsBookkeepingColumns(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	ids := edgeKronosSeedInbox(t, st, 2)
	// One row DELIVERED but unacked: the state in which the withheld columns
	// actually carry non-zero values, so a leak would be visible.
	if err := st.MarkOrchDelivered(ids[:1], 1_769_040_001_000); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}

	rec := edgeKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/orch/inbox = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"delivered_at", "acked_at", "disposition"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the inbox wire carries %q:\n%s", forbidden, body)
		}
	}
	for _, want := range []string{`"id"`, `"kind"`, `"slug"`, `"summary"`, `"seen_at"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the inbox wire is missing %s:\n%s", want, body)
		}
	}
}

// TestEdgeKronosInboxServesOnlyUnackedRows: the ack is what retires a row, so
// an acked row reappearing would be re-handled — and a double-ack is then a
// 409 kronos cannot resolve, since the row it is looking at says otherwise.
func TestEdgeKronosInboxServesOnlyUnackedRows(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	ids := edgeKronosSeedInbox(t, st, 3)
	if err := st.AckOrchItems(ids[:1], "escalated", 1_769_040_001_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}

	var got struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	rec := edgeKronosGet(t, mux, "/api/orch/inbox")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode inbox: %v\n%s", err, rec.Body.String())
	}
	if len(got.Items) != 2 {
		t.Fatalf("inbox returned %d item(s), want the 2 still unacked", len(got.Items))
	}
	if got.Items[0].ID != ids[1] || got.Items[1].ID != ids[2] {
		t.Fatalf("inbox ids = %+v, want %v then %v (oldest first)", got.Items, ids[1], ids[2])
	}
}

// TestEdgeKronosInboxOnAnEmptyDB: kronos runs this on every bump and on every
// resume, including before anything has ever been enqueued. An empty inbox is
// a normal answer, not an error.
func TestEdgeKronosInboxOnAnEmptyDB(t *testing.T) {
	_, mux := edgeKronosAPI(t)
	rec := edgeKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/orch/inbox on a fresh db = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(got.Items) != 0 {
		t.Fatalf("items = %v, want none", got.Items)
	}
}

// --- POST /api/orch/ack -----------------------------------------------------

// TestEdgeKronosAckRefusalMatrix walks every refusal the contract states, and
// asserts on each row that NOTHING was acked. This route is driven unattended
// by an LLM, so a refusal that half-applied would surface days later as an
// escalation nobody ever saw.
func TestEdgeKronosAckRefusalMatrix(t *testing.T) {
	cases := []struct {
		name string
		// body is built per-case from the seeded ids
		body func(ids []int64) any
		want int
	}{
		{
			name: "empty ids",
			body: func([]int64) any { return map[string]any{"ids": []int64{}, "disposition": "escalated"} },
			want: http.StatusBadRequest,
		},
		{
			name: "null ids",
			body: func([]int64) any { return map[string]any{"ids": nil, "disposition": "escalated"} },
			want: http.StatusBadRequest,
		},
		{
			name: "ids key absent",
			body: func([]int64) any { return map[string]any{"disposition": "escalated"} },
			want: http.StatusBadRequest,
		},
		{
			name: "empty disposition",
			body: func(ids []int64) any { return map[string]any{"ids": ids[:1], "disposition": ""} },
			want: http.StatusBadRequest,
		},
		{
			name: "whitespace-only disposition",
			body: func(ids []int64) any { return map[string]any{"ids": ids[:1], "disposition": "   \n"} },
			want: http.StatusBadRequest,
		},
		{
			name: "disposition key absent",
			body: func(ids []int64) any { return map[string]any{"ids": ids[:1]} },
			want: http.StatusBadRequest,
		},
		{
			name: "unknown id alone",
			body: func(ids []int64) any {
				return map[string]any{"ids": []int64{ids[len(ids)-1] + 9999}, "disposition": "escalated"}
			},
			want: http.StatusConflict,
		},
		{
			name: "unknown id mixed with good ones",
			body: func(ids []int64) any {
				return map[string]any{"ids": []int64{ids[0], ids[len(ids)-1] + 9999}, "disposition": "escalated"}
			},
			want: http.StatusConflict,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeKronosAPI(t)
			ids := edgeKronosSeedInbox(t, st, 3)

			rec := edgeKronosPost(t, mux, "/api/orch/ack", c.body(ids))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if n := edgeKronosUnackedCount(t, st); n != 3 {
				t.Fatalf("unacked = %d, want 3 — a refused ack writes NOTHING", n)
			}
		})
	}
}

// TestEdgeKronosAckDoubleAckIs409: "any unknown or double-acked id → 409 and
// NOTHING written". A second ack means kronos lost track of what it had done,
// and silently re-stamping would overwrite the disposition recording what
// actually happened the first time.
func TestEdgeKronosAckDoubleAckIs409(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	ids := edgeKronosSeedInbox(t, st, 2)

	rec := edgeKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": ids[:1], "disposition": "escalated:flagged it to carl",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("first ack = %d: %s", rec.Code, rec.Body.String())
	}
	if n := edgeKronosUnackedCount(t, st); n != 1 {
		t.Fatalf("unacked after one ack = %d, want 1", n)
	}

	// The re-ack, alone and mixed with a still-open id: both must refuse, and
	// the mixed one must leave the still-open row open.
	for _, batch := range [][]int64{{ids[0]}, {ids[0], ids[1]}, {ids[1], ids[0]}} {
		rec := edgeKronosPost(t, mux, "/api/orch/ack", map[string]any{"ids": batch, "disposition": "ignored"})
		if rec.Code != http.StatusConflict {
			t.Fatalf("re-ack %v = %d, want 409: %s", batch, rec.Code, rec.Body.String())
		}
		if n := edgeKronosUnackedCount(t, st); n != 1 {
			t.Fatalf("re-ack %v left unacked = %d, want 1 — all-or-nothing", batch, n)
		}
	}
}

// --- POST /api/orch/context -------------------------------------------------

// TestEdgeKronosContextRefusalMatrix: "Blank session / out-of-range percentage
// → 400. Unknown fields → 400 (DisallowUnknownFields)." The strict decode is
// what turns a client sending `used_pct` into a visible refusal rather than a
// silent 0% report that fires every threshold at once.
func TestEdgeKronosContextRefusalMatrix(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"blank session", `{"session_id":"","used_percentage":42,"context_window_size":1000000}`},
		{"whitespace session", `{"session_id":"   ","used_percentage":42,"context_window_size":1000000}`},
		{"session key absent", `{"used_percentage":42,"context_window_size":1000000}`},
		{"percentage below zero", `{"session_id":"s","used_percentage":-1,"context_window_size":1000000}`},
		{"percentage over 100", `{"session_id":"s","used_percentage":100.5,"context_window_size":1000000}`},
		{"percentage wildly out of range", `{"session_id":"s","used_percentage":4000000,"context_window_size":1000000}`},
		{"unknown field", `{"session_id":"s","used_percentage":42,"context_window_size":1000000,"extra":1}`},
		{"misspelled percentage field", `{"session_id":"s","used_pct":42,"context_window_size":1000000}`},
		{"not an object", `[1,2,3]`},
		{"empty body", ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeKronosAPI(t)
			rec := edgeKronosPostRaw(t, mux, "/api/orch/context", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if raw, err := st.GetKV("kronos_ctx"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("a refused context POST wrote kronos_ctx = %q (%v)", raw, err)
			}
		})
	}
}

// TestEdgeKronosContextAcceptsTheClosedRange: 0 and 100 are both real
// readings — a fresh session and the instant before auto-compact takes over —
// so the range is closed and neither end may be refused.
func TestEdgeKronosContextAcceptsTheClosedRange(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	for _, pct := range []string{"0", "0.1", "50", "99.9", "100"} {
		rec := edgeKronosPostRaw(t, mux, "/api/orch/context",
			`{"session_id":"sess-1","used_percentage":`+pct+`,"context_window_size":1000000}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("used_percentage %s = %d, want 200: %s", pct, rec.Code, rec.Body.String())
		}
	}
	if _, err := st.GetKV("kronos_ctx"); err != nil {
		t.Fatalf("an accepted context POST did not persist kronos_ctx: %v", err)
	}
}

// --- POST /api/graph/recall-rearm -------------------------------------------

// TestEdgeKronosRearmRefusals: "Session mismatch / no kronos → 403; blank →
// 400." The status split is the point — a blank id is a malformed request, a
// mismatched one is an authorization failure — and BOTH must delete nothing.
// This route deletes rows; a permissive version wipes every cell's recall
// marks on the machine.
func TestEdgeKronosRearmRefusals(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"blank session", `{"session_id":""}`, http.StatusBadRequest},
		{"whitespace session", `{"session_id":"   "}`, http.StatusBadRequest},
		{"session key absent", `{}`, http.StatusBadRequest},
		{"no kronos recorded", `{"session_id":"a-cell-session"}`, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeKronosAPI(t)
			if err := st.MarkRecalled("a-cell-session", []int64{1, 2, 3}, 1_769_040_000_000); err != nil {
				t.Fatalf("MarkRecalled: %v", err)
			}

			rec := edgeKronosPostRaw(t, mux, "/api/graph/recall-rearm", c.body)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			ids, err := st.RecalledEdgeIDs("a-cell-session")
			if err != nil {
				t.Fatalf("RecalledEdgeIDs: %v", err)
			}
			if len(ids) != 3 {
				t.Fatalf("a refused re-arm deleted marks: %d of 3 left", len(ids))
			}
		})
	}
}

// --- GET /api/kronos --------------------------------------------------------

// TestEdgeKronosStatusOmitsContextWhenNeverReported: "`context` is a pointer,
// omitted when never reported (absent key = no report — never a zeroed
// object)". A zeroed object claims the context is at 0% with a window of 0,
// which the meter would render as a healthy reading — the exact false-negative
// the null-guard exists for.
func TestEdgeKronosStatusOmitsContextWhenNeverReported(t *testing.T) {
	_, mux := edgeKronosAPI(t)
	rec := edgeKronosGet(t, mux, "/api/kronos")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/kronos = %d: %s", rec.Code, rec.Body.String())
	}
	got := edgeKronosBodyMap(t, rec)
	if v, present := got["context"]; present && v != nil {
		t.Fatalf("context = %#v with nothing ever reported — want the key absent, and NEVER a zeroed object", v)
	}
	for _, key := range []string{"enabled", "live"} {
		if _, present := got[key]; !present {
			t.Errorf("/api/kronos is missing the %q key: %s", key, rec.Body.String())
		}
	}
}

// TestEdgeKronosStatusReportsDisabled: with no ~/.kraken/kronos.env the whole
// feature is off, and the board's view has to say so — an `enabled:true` on an
// opted-out machine would render a composer that can never reach anything.
func TestEdgeKronosStatusReportsDisabled(t *testing.T) {
	_, mux := edgeKronosAPI(t)
	got := edgeKronosBodyMap(t, edgeKronosGet(t, mux, "/api/kronos"))
	if enabled, _ := got["enabled"].(bool); enabled {
		t.Fatalf("enabled = true with no kronos.env: %#v", got)
	}
}

// TestEdgeKronosStatusEnabledFollowsTheFile: the switch is a file under HOME,
// and useTestStore sandboxes HOME — so creating it must flip the answer. If it
// does not, the route is reading a hardcoded path and every test above is
// measuring the developer's real machine.
func TestEdgeKronosStatusEnabledFollowsTheFile(t *testing.T) {
	_, mux := edgeKronosAPI(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.kraken: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kraken", "kronos.env"), nil, 0o600); err != nil {
		t.Fatalf("write kronos.env: %v", err)
	}

	got := edgeKronosBodyMap(t, edgeKronosGet(t, mux, "/api/kronos"))
	if enabled, _ := got["enabled"].(bool); !enabled {
		t.Fatalf("enabled = false after creating ~/.kraken/kronos.env: %#v", got)
	}
}

// --- the mirror gate --------------------------------------------------------

// TestEdgeKronosWritesAreMirrorGated: "every write mirror-gated". A mirror
// board is read-only, full stop — and these writes are worse than most: the ack
// retires the live deployment's escalations, and the re-arm deletes recall
// rows.
func TestEdgeKronosWritesAreMirrorGated(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range []string{
		"/api/orch/ack",
		"/api/orch/context",
		"/api/graph/recall-rearm",
	} {
		t.Run(path, func(t *testing.T) {
			rec := edgeKronosPostRaw(t, mux, path, `{}`)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s under --mirror = %d, want 403: %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestEdgeKronosReadsStayOpenUnderMirror: a read is a read. The mirror board
// exists to LOOK at the live deployment, so refusing its inbox and status
// reads would be a new policy rather than the one every other GET follows.
func TestEdgeKronosReadsStayOpenUnderMirror(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	_, mux := edgeKronosAPI(t)

	for _, path := range []string{"/api/orch/inbox", "/api/kronos"} {
		rec := edgeKronosGet(t, mux, path)
		if rec.Code == http.StatusForbidden {
			t.Errorf("GET %s = 403 under --mirror — reads stay open", path)
		}
	}
}

// --- the contract number ----------------------------------------------------

// TestEdgeKronosAdditiveRoutesDoNotBumpTheContract: "apiContract stays 6 —
// additive only". Every route in this slice is a NEW PATH rather than a key
// retyped, renamed or removed from an existing shape, and a bump would make
// every shipped iOS build refuse to talk to this daemon over a feature it does
// not have.
func TestEdgeKronosAdditiveRoutesDoNotBumpTheContract(t *testing.T) {
	if apiContract != 6 {
		t.Fatalf("apiContract = %d, want 6 — the kronos routes are additive", apiContract)
	}
}

// --- POST /api/graph/recall-rearm, with a kronos recorded -------------------
//
// Round-1 amendment: `kronos_agent` holds JSON
// `{"session_id": "<uuid>", "spawned_at": <unix millis>}` — iris' shape
// exactly. That unlocks both halves this route could not reach blind: the 200
// with its `{"cleared":N}` body, and the 403 for a mismatch measured against a
// RECORDED kronos rather than against an empty kv.

// Both ids carry ALPHABETIC hex digits deliberately: the mismatch matrix below
// includes a case-folded variant, and an all-numeric uuid would make
// strings.ToUpper the identity function — the subtest would then post the
// recorded id and demand a refusal of a genuine match, which is the opposite
// of what the contract obliges.
const (
	edgeKronosAPISessionID = "a1b2c3d4-e5f6-4a7b-8c9d-e0f1a2b3c4d5"
	edgeKronosAPIOtherUUID = "f9e8d7c6-b5a4-4938-8271-6f5e4d3c2b1a"
)

// edgeKronosRecordAgent writes the frozen kv shape verbatim — built as a JSON
// literal rather than through a struct, so the fixture is pinned to the shape
// the contract names rather than to whatever type the implementer declared.
func edgeKronosRecordAgent(t *testing.T, st *store.Store, sessionID string) {
	t.Helper()
	value := `{"session_id":"` + sessionID + `","spawned_at":1769040000000}`
	if err := st.SetKV("kronos_agent", value); err != nil {
		t.Fatalf("SetKV(kronos_agent): %v", err)
	}
}

// edgeKronosMarkCount reads how many recall marks a session still holds.
func edgeKronosMarkCount(t *testing.T, st *store.Store, sessionID string) int {
	t.Helper()
	ids, err := st.RecalledEdgeIDs(sessionID)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(%q): %v", sessionID, err)
	}
	return len(ids)
}

// TestEdgeKronosRearmClearedCountOnAMatch: the 200 body is `{"cleared":N}`, and
// N is the number actually deleted rather than the number asked for. The
// PostCompact hook prints nothing and exits 0 whatever happens, so this body is
// the ONLY place the re-arm is observable at all — a hardcoded 0, or a count of
// the request rather than of the delete, would make a silently broken re-arm
// look identical to a working one.
func TestEdgeKronosRearmClearedCountOnAMatch(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	edgeKronosRecordAgent(t, st, edgeKronosAPISessionID)

	if err := st.MarkRecalled(edgeKronosAPISessionID, []int64{1, 2, 3, 4}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled kronos: %v", err)
	}
	if err := st.MarkRecalled("a-cell-session", []int64{1, 2}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}

	rec := edgeKronosPost(t, mux, "/api/graph/recall-rearm",
		map[string]string{"session_id": edgeKronosAPISessionID})
	if rec.Code != http.StatusOK {
		t.Fatalf("matching re-arm = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Cleared int64 `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if got.Cleared != 4 {
		t.Fatalf("cleared = %d, want 4: %s", got.Cleared, rec.Body.String())
	}
	if n := edgeKronosMarkCount(t, st, edgeKronosAPISessionID); n != 0 {
		t.Fatalf("kronos still holds %d mark(s) after a 200", n)
	}
	if n := edgeKronosMarkCount(t, st, "a-cell-session"); n != 2 {
		t.Fatalf("the cell session holds %d of 2 marks — the re-arm is session-scoped", n)
	}
}

// TestEdgeKronosRearmClearedZeroIsA200: kronos can compact before recall has
// ever injected anything, and the hook fires regardless. Nothing to clear is a
// successful re-arm, not a 4xx — and answering with an error here would leave
// the very first post-compaction turn with no facts AND no signal.
func TestEdgeKronosRearmClearedZeroIsA200(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	edgeKronosRecordAgent(t, st, edgeKronosAPISessionID)

	rec := edgeKronosPost(t, mux, "/api/graph/recall-rearm",
		map[string]string{"session_id": edgeKronosAPISessionID})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-arm with nothing to clear = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Cleared int64 `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if got.Cleared != 0 {
		t.Fatalf("cleared = %d, want 0: %s", got.Cleared, rec.Body.String())
	}
}

// TestEdgeKronosRearmIsRepeatable: a compaction can fire the hook twice, and
// the daemon-side retry is a plain repeat. The second call must be a 200 with
// cleared 0 rather than a conflict — the row set is already in the state the
// caller wanted.
func TestEdgeKronosRearmIsRepeatable(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	edgeKronosRecordAgent(t, st, edgeKronosAPISessionID)
	if err := st.MarkRecalled(edgeKronosAPISessionID, []int64{7, 8}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	first := edgeKronosPost(t, mux, "/api/graph/recall-rearm",
		map[string]string{"session_id": edgeKronosAPISessionID})
	if first.Code != http.StatusOK {
		t.Fatalf("first re-arm = %d: %s", first.Code, first.Body.String())
	}
	second := edgeKronosPost(t, mux, "/api/graph/recall-rearm",
		map[string]string{"session_id": edgeKronosAPISessionID})
	if second.Code != http.StatusOK {
		t.Fatalf("second re-arm = %d, want 200: %s", second.Code, second.Body.String())
	}
	var got struct {
		Cleared int64 `json:"cleared"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, second.Body.String())
	}
	if got.Cleared != 0 {
		t.Fatalf("second re-arm cleared = %d, want 0", got.Cleared)
	}
}

// TestEdgeKronosRearmMismatchWithARecordedKronosIs403 is the authorization
// check asserted against the state it actually guards. `kraken rearm-hook`
// posts whatever session id its payload carries, and that hook could be
// installed — or invoked — anywhere; a route that honoured a non-kronos id
// would delete that session's recall marks on the strength of a request nobody
// authorized. The prefix variants are here because a `HasPrefix`-style
// comparison passes a plain equality test and fails these.
func TestEdgeKronosRearmMismatchWithARecordedKronosIs403(t *testing.T) {
	for _, sid := range []string{
		edgeKronosAPIOtherUUID,
		edgeKronosAPISessionID + "-suffix",
		edgeKronosAPISessionID[:8],
		strings.ToUpper(edgeKronosAPISessionID),
	} {
		t.Run(sid, func(t *testing.T) {
			st, mux := edgeKronosAPI(t)
			edgeKronosRecordAgent(t, st, edgeKronosAPISessionID)
			if err := st.MarkRecalled(edgeKronosAPISessionID, []int64{1, 2, 3}, 1_769_040_000_000); err != nil {
				t.Fatalf("MarkRecalled kronos: %v", err)
			}
			if err := st.MarkRecalled(sid, []int64{4, 5}, 1_769_040_000_000); err != nil {
				t.Fatalf("MarkRecalled caller: %v", err)
			}

			rec := edgeKronosPost(t, mux, "/api/graph/recall-rearm", map[string]string{"session_id": sid})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("mismatch %q = %d, want 403: %s", sid, rec.Code, rec.Body.String())
			}
			if n := edgeKronosMarkCount(t, st, edgeKronosAPISessionID); n != 3 {
				t.Fatalf("kronos' marks = %d of 3 after a refused re-arm", n)
			}
			if n := edgeKronosMarkCount(t, st, sid); n != 2 {
				t.Fatalf("the caller's own marks = %d of 2 after a refused re-arm", n)
			}
		})
	}
}

// TestEdgeKronosRearmRefusalCarriesNoSessionID: the refusal is served to
// whatever posted it, which by construction is NOT kronos. Echoing the
// recorded session id back would hand an unauthorized caller exactly the value
// it needs to make the next request succeed — the one piece of state this
// route's whole authorization rests on.
func TestEdgeKronosRearmRefusalCarriesNoSessionID(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	edgeKronosRecordAgent(t, st, edgeKronosAPISessionID)

	rec := edgeKronosPost(t, mux, "/api/graph/recall-rearm",
		map[string]string{"session_id": edgeKronosAPIOtherUUID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mismatch = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), edgeKronosAPISessionID) {
		t.Fatalf("the refusal echoes kronos' recorded session id:\n%s", rec.Body.String())
	}
}

// TestEdgeKronosRearmStillRefusesBlankWithAKronosRecorded: the blank check is
// ahead of the match, not a consequence of it. With a kronos recorded, a blank
// id must still be the 400 — never "blank does not equal kronos, therefore
// 403", and above all never a delete with no session predicate.
func TestEdgeKronosRearmStillRefusesBlankWithAKronosRecorded(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	edgeKronosRecordAgent(t, st, edgeKronosAPISessionID)
	if err := st.MarkRecalled(edgeKronosAPISessionID, []int64{1, 2}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	for _, body := range []string{`{"session_id":""}`, `{"session_id":"   "}`, `{}`} {
		rec := edgeKronosPostRaw(t, mux, "/api/graph/recall-rearm", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
		if n := edgeKronosMarkCount(t, st, edgeKronosAPISessionID); n != 2 {
			t.Fatalf("body %s deleted marks: %d of 2 left", body, n)
		}
	}
}

// --- round 2 --------------------------------------------------------------
//
// Everything below this line was authored SIGHTED, against the round-2
// amendment "GET /api/orch/inbox caps at the oldest 100 and gains an additive
// `total` key (the full unacked count) so a backlog is visible without
// shipping it whole." The blindness above is unaffected: the cap did not exist
// when the file was written, and the assertions derive from the amendment text.

// edgeKronosInboxPage decodes the inbox's page and its total.
func edgeKronosInboxPage(t *testing.T, mux http.Handler) ([]int64, int) {
	t.Helper()
	rec := edgeKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/orch/inbox = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode inbox: %v\n%s", err, rec.Body.String())
	}
	ids := make([]int64, 0, len(got.Items))
	for _, it := range got.Items {
		ids = append(ids, it.ID)
	}
	// `total` is additive, so its ABSENCE decodes as zero — and zero is also a
	// legal value. The presence check is made on the raw body so a route that
	// simply never learned the key cannot pass by looking like an empty inbox.
	if !strings.Contains(rec.Body.String(), `"total"`) {
		t.Fatalf("the inbox wire carries no `total` key — a truncated page would be silent:\n%s", rec.Body.String())
	}
	return ids, got.Total
}

// TestEdgeKronosInboxCapsAtOneHundredOldestFirst — authored SIGHTED. The cap
// and the count are one mechanism: kronos reads its whole inbox in one verb
// and acks what it read, so a page that silently dropped the tail would leave
// those rows unacked, undelivered-about (the bump already fired) and invisible
// — the inbox would look drained while it was not. `total` is what makes the
// truncation legible from inside the page, and OLDEST-first is what makes the
// page the right hundred: the oldest rows are the ones that have waited.
func TestEdgeKronosInboxCapsAtOneHundredOldestFirst(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	ids := edgeKronosSeedInbox(t, st, 101)

	page, total := edgeKronosInboxPage(t, mux)
	if len(page) != 100 {
		t.Fatalf("inbox returned %d item(s) for 101 unacked rows, want the oldest 100", len(page))
	}
	if total != 101 {
		t.Fatalf("total = %d, want 101 — the FULL unacked count, not the page's length", total)
	}
	for i, id := range page {
		if id != ids[i] {
			t.Fatalf("page[%d] = %d, want %d — the page is the OLDEST hundred, in id order", i, id, ids[i])
		}
	}
	if page[len(page)-1] == ids[len(ids)-1] {
		t.Fatalf("the page ends at the NEWEST row %d — a DESC page would starve the oldest rows forever", page[len(page)-1])
	}
}

// TestEdgeKronosInboxAtExactlyTheCap — authored SIGHTED. The off-by-one: a
// hundred rows is one full page and NOT a truncation, so `total` equals the
// page length and nothing is withheld. A cap applied as `> 100` on the wrong
// side would either drop the hundredth row or report a backlog that is not
// there — and kronos, told 101 of 100, would keep asking for a page that never
// changes.
func TestEdgeKronosInboxAtExactlyTheCap(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	ids := edgeKronosSeedInbox(t, st, 100)

	page, total := edgeKronosInboxPage(t, mux)
	if len(page) != 100 {
		t.Fatalf("inbox returned %d item(s) for exactly 100 unacked rows, want all 100", len(page))
	}
	if total != 100 {
		t.Fatalf("total = %d, want 100 — a full page is not a truncated one", total)
	}
	for i, id := range page {
		if id != ids[i] {
			t.Fatalf("page[%d] = %d, want %d", i, id, ids[i])
		}
	}
}

// TestEdgeKronosInboxTotalCountsOnlyUnackedRows — authored SIGHTED. `total` is
// "the full unacked count", so acking rows has to move it: a total that
// counted the whole table would report a backlog that no amount of work could
// ever clear, and kronos' one signal for "am I behind" would be a constant.
func TestEdgeKronosInboxTotalCountsOnlyUnackedRows(t *testing.T) {
	st, mux := edgeKronosAPI(t)
	ids := edgeKronosSeedInbox(t, st, 105)

	if _, total := edgeKronosInboxPage(t, mux); total != 105 {
		t.Fatalf("total = %d, want 105", total)
	}
	if err := st.AckOrchItems(ids[:10], "escalated", 1_769_040_001_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	page, total := edgeKronosInboxPage(t, mux)
	if total != 95 {
		t.Fatalf("total = %d after acking 10 of 105, want 95", total)
	}
	if len(page) != 95 {
		t.Fatalf("page = %d item(s), want all 95 — the remainder is under the cap", len(page))
	}
	if page[0] != ids[10] {
		t.Fatalf("page[0] = %d, want %d — the acked rows are gone from the head", page[0], ids[10])
	}
}

// TestEdgeKronosInboxTotalOnAnEmptyDB — authored SIGHTED. The everyday call.
// Zero is a real answer and must not be an error or an absent key: kronos runs
// this on every bump and every resume, and an absent `total` on the quiet path
// is exactly the shape a client learns to stop looking for.
func TestEdgeKronosInboxTotalOnAnEmptyDB(t *testing.T) {
	_, mux := edgeKronosAPI(t)
	page, total := edgeKronosInboxPage(t, mux)
	if len(page) != 0 || total != 0 {
		t.Fatalf("inbox on a fresh db = %d item(s)/total %d, want 0/0", len(page), total)
	}
}
