package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// kronos_contract_test.go — athena-logic's BLIND contract tests for the kronos
// build's Slice D wire surface (50-contract.md § "Slice D — api + cli"): the
// routes.
//
// POST /api/imsg/send and the status payload's `imsg_configured` key were part
// of that surface and are gone with the iMessage bridge (carl's call at the
// manual gate), so the tests that covered them are gone too.
//
// Written from the contract text alone. internal/api/orch.go,
// internal/api/register.go and every new cmd/kraken/cmd_*.go were never
// opened.
//
// Dimension: LOGIC — the stated shapes, the happy paths, and the refusals the
// contract states as part of a route's own obligation. The full refusal
// matrix, DisallowUnknownFields on every body, and the mirror gate are
// athena-edge's ground; the overlap on the few refusals named here is
// deliberate.
//
// Everything goes through the real mux (api.Register), like mirror_test.go and
// slack_contract_test.go: the contract freezes ROUTES, not handler names, so a
// test calling a handler by name would pin something the contract never said.
//
// Every package-level identifier is prefixed `contractKronos`: this package
// already carries bare `contract*` names (watch) plus `contractSlack*`, and a
// sibling author shares the test binary.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

func contractKronosMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractKronosGet(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// contractKronosPostRaw sends a body verbatim, so a test can name a key the
// wire types do not have.
func contractKronosPostRaw(t *testing.T, mux http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractKronosPost(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return contractKronosPostRaw(t, mux, path, string(mustJSON(t, body)))
}

func contractKronosDecode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

// contractKronosEnable creates ~/.kraken/kronos.env under the sandboxed HOME
// useTestStore already installed: the whole feature's opt-in, empty file = on.
func contractKronosEnable(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(util.Home(), ".kraken"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.kraken: %v", err)
	}
	if err := os.WriteFile(filepath.Join(util.Home(), ".kraken", "kronos.env"), nil, 0o600); err != nil {
		t.Fatalf("write kronos.env: %v", err)
	}
}

func contractKronosSeedRow(t *testing.T, st *store.Store, kind, slug, summary string, now int64) int64 {
	t.Helper()
	id, err := st.AddOrchItem(kind, slug, summary, now)
	if err != nil {
		t.Fatalf("AddOrchItem(%q, %q): %v", kind, slug, err)
	}
	return id
}

func contractKronosKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// GET /api/orch/inbox
// ---------------------------------------------------------------------------

// TestContractKronosInboxEmptyIsAnEmptyList: kronos reads this JSON with a
// tool, so "nothing waiting" must decode as a list of length zero rather than
// null — a null is a shape change every consumer has to special-case, and the
// consumer here is an LLM reading raw stdout.
func TestContractKronosInboxEmptyIsAnEmptyList(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	rec := contractKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	contractKronosDecode(t, rec, &envelope)
	if envelope.Items == nil {
		t.Fatalf("items decoded as nil (body %s) — the empty inbox must serve []", rec.Body.String())
	}
	if len(envelope.Items) != 0 {
		t.Fatalf("items = %v, want empty", envelope.Items)
	}
}

// TestContractKronosInboxServesUnackedRowsInIDOrder pins the payload's five
// frozen keys and the two rules that make it usable: only rows still awaiting
// kronos' verdict, in arrival order — and the three lifecycle fields WITHHELD,
// iris' webSlackMsg rule. Kronos decides from what happened, not from kraken's
// bookkeeping about whether a bump landed.
func TestContractKronosInboxServesUnackedRowsInIDOrder(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	one := contractKronosSeedRow(t, st, "job-blocked", "csv-export", "csv-export is blocked", 1_755_000_000_001)
	two := contractKronosSeedRow(t, st, "note", "", "carl: what is blocking it?", 1_755_000_000_002)
	three := contractKronosSeedRow(t, st, "pr-merged", "csv-export", "PR #12 merged", 1_755_000_000_003)
	if err := st.AckOrchItems([]int64{three}, "noted", 1_755_000_000_009); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	mux := contractKronosMux(t)

	rec := contractKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	contractKronosDecode(t, rec, &envelope)
	if len(envelope.Items) != 2 {
		t.Fatalf("items = %v, want the two unacked rows", envelope.Items)
	}

	want := []string{"id", "kind", "seen_at", "slug", "summary"}
	if got := contractKronosKeys(envelope.Items[0]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("item keys = %v, want exactly %v — delivery and ack fields are withheld from the wire", got, want)
	}

	var first struct {
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		Slug    string `json:"slug"`
		Summary string `json:"summary"`
		SeenAt  int64  `json:"seen_at"`
	}
	if err := json.Unmarshal(mustJSON(t, envelope.Items[0]), &first); err != nil {
		t.Fatalf("decode item 0: %v", err)
	}
	if first.ID != one {
		t.Errorf("item 0 id = %d, want %d (oldest first)", first.ID, one)
	}
	if first.Kind != "job-blocked" || first.Slug != "csv-export" || first.Summary != "csv-export is blocked" {
		t.Errorf("item 0 = %+v, want the row's own fields", first)
	}
	if first.SeenAt != 1_755_000_000_001 {
		t.Errorf("item 0 seen_at = %d, want the stored stamp", first.SeenAt)
	}

	var second struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(mustJSON(t, envelope.Items[1]), &second); err != nil {
		t.Fatalf("decode item 1: %v", err)
	}
	if second.ID != two {
		t.Errorf("item 1 id = %d, want %d", second.ID, two)
	}
}

// TestContractKronosInboxKeepsADeliveredButUnackedRow: the ACK retires a row,
// never the delivery. A bump kronos never got to (its pane was busy, it was
// mid-compaction, the daemon restarted) must still be waiting on the next
// read.
func TestContractKronosInboxKeepsADeliveredButUnackedRow(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	id := contractKronosSeedRow(t, st, "cell-gone", "csv-export", "the cell is gone", 1_755_000_000_001)
	if err := st.MarkOrchDelivered([]int64{id}, 1_755_000_000_005); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	mux := contractKronosMux(t)

	var envelope struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/orch/inbox"), &envelope)
	if len(envelope.Items) != 1 || envelope.Items[0].ID != id {
		t.Fatalf("items = %+v, want the delivered-but-unacked row still listed", envelope.Items)
	}
}

// ---------------------------------------------------------------------------
// POST /api/orch/ack
// ---------------------------------------------------------------------------

// TestContractKronosAckSettlesTheBatch: one call retires several rows, and
// what kronos acked is gone from its next read.
func TestContractKronosAckSettlesTheBatch(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	one := contractKronosSeedRow(t, st, "job-blocked", "csv-export", "blocked", 1_755_000_000_001)
	two := contractKronosSeedRow(t, st, "note", "", "carl asked something", 1_755_000_000_002)
	three := contractKronosSeedRow(t, st, "pr-merged", "csv-export", "merged", 1_755_000_000_003)
	mux := contractKronosMux(t)

	rec := contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids":         []int64{one, two},
		"disposition": "escalated:asked carl",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var ok struct {
		OK bool `json:"ok"`
	}
	contractKronosDecode(t, rec, &ok)
	if !ok.OK {
		t.Fatalf("body = %s, want {\"ok\":true}", rec.Body.String())
	}

	var envelope struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/orch/inbox"), &envelope)
	if len(envelope.Items) != 1 || envelope.Items[0].ID != three {
		t.Fatalf("inbox after the ack = %+v, want only the unacked row", envelope.Items)
	}
}

// TestContractKronosAckRefusesEmptyInput: an ack with no ids, or with no
// disposition, is a caller mistake — and the disposition is the only durable
// record of why a row left the inbox, so accepting a blank one silently erases
// the decision.
func TestContractKronosAckRefusesEmptyInput(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	id := contractKronosSeedRow(t, st, "job-blocked", "csv-export", "blocked", 1_755_000_000_001)
	mux := contractKronosMux(t)

	for _, body := range []map[string]any{
		{"ids": []int64{}, "disposition": "escalated"},
		{"disposition": "escalated"},
		{"ids": []int64{id}, "disposition": ""},
		{"ids": []int64{id}},
	} {
		rec := contractKronosPost(t, mux, "/api/orch/ack", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %v = status %d, want 400", body, rec.Code)
		}
	}

	var envelope struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/orch/inbox"), &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("inbox = %+v, want the row untouched by every refusal", envelope.Items)
	}
}

// TestContractKronosAckIsAllOrNothingOnTheWire surfaces the store's strict
// verb as a 409 with NOTHING written. Under a lenient rule the mistake is
// invisible in both directions at once: the row kronos meant stays unacked and
// comes back forever, while the row it named is retired unread.
func TestContractKronosAckIsAllOrNothingOnTheWire(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	one := contractKronosSeedRow(t, st, "job-blocked", "csv-export", "blocked", 1_755_000_000_001)
	two := contractKronosSeedRow(t, st, "note", "", "carl asked something", 1_755_000_000_002)
	mux := contractKronosMux(t)

	rec := contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids":         []int64{one, 9_999_999},
		"disposition": "escalated",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("ack naming an unknown id = status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/orch/inbox"), &envelope)
	if len(envelope.Items) != 2 {
		t.Fatalf("inbox = %+v, want BOTH rows still unacked — a refused batch writes nothing", envelope.Items)
	}

	// the same refusal for a row that is already settled
	if rec := contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": []int64{one}, "disposition": "escalated",
	}); rec.Code != http.StatusOK {
		t.Fatalf("first ack = status %d, want 200", rec.Code)
	}
	rec = contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": []int64{one, two}, "disposition": "escalated again",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("ack naming an already-acked id = status %d, want 409", rec.Code)
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/orch/inbox"), &envelope)
	if len(envelope.Items) != 1 || envelope.Items[0].ID != two {
		t.Fatalf("inbox = %+v, want the still-unacked row untouched by the refused batch", envelope.Items)
	}
}

// ---------------------------------------------------------------------------
// POST /api/orch/context
// ---------------------------------------------------------------------------

// TestContractKronosContextRouteAcceptsAReport: the statusline posts this
// every few seconds, and it is the sentinel's only input.
func TestContractKronosContextRouteAcceptsAReport(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	rec := contractKronosPost(t, mux, "/api/orch/context", map[string]any{
		"session_id":          "sess-a",
		"used_percentage":     42.5,
		"context_window_size": 1_000_000,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var ok struct {
		OK bool `json:"ok"`
	}
	contractKronosDecode(t, rec, &ok)
	if !ok.OK {
		t.Fatalf("body = %s, want {\"ok\":true}", rec.Body.String())
	}
	if _, err := st.GetKV("kronos_ctx"); err != nil {
		t.Fatalf("kronos_ctx kv after an accepted report: %v — the report must be persisted", err)
	}
}

// TestContractKronosContextRouteRefusals: a blank session cannot be keyed on,
// a percentage outside 0-100 is a malformed payload, and an unknown field is
// the refusal itself — a client sending `pct` instead of `used_percentage`
// would otherwise get a 200 while reporting nothing.
func TestContractKronosContextRouteRefusals(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	for _, body := range []map[string]any{
		{"session_id": "", "used_percentage": 50, "context_window_size": 1_000_000},
		{"session_id": "sess-a", "used_percentage": -1, "context_window_size": 1_000_000},
		{"session_id": "sess-a", "used_percentage": 101, "context_window_size": 1_000_000},
	} {
		if rec := contractKronosPost(t, mux, "/api/orch/context", body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %v = status %d, want 400", body, rec.Code)
		}
	}

	rec := contractKronosPostRaw(t, mux, "/api/orch/context",
		`{"session_id":"sess-a","used_percentage":50,"context_window_size":1000000,"pct":50}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a body carrying an unknown field = status %d, want 400 (DisallowUnknownFields)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/graph/recall-rearm
// ---------------------------------------------------------------------------

// contractKronosRecordAgent writes the kv singleton the spawn pass maintains.
// The VALUE shape is not spelled out by the contract; it is derived from the
// contract's own "iris' exact lifecycle, re-implemented for kronos", which
// makes iris' {session_id, spawned_at} JSON the one derivable answer.
// Disclosed as a silence in the report.
func contractKronosRecordAgent(t *testing.T, st *store.Store, sessionID string) {
	t.Helper()
	raw, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		SpawnedAt int64  `json:"spawned_at"`
	}{sessionID, time.Now().UnixMilli()})
	if err != nil {
		t.Fatalf("marshal kronos_agent: %v", err)
	}
	if err := st.SetKV("kronos_agent", string(raw)); err != nil {
		t.Fatalf("SetKV kronos_agent: %v", err)
	}
}

// TestContractKronosRearmRouteClearsAndReportsTheCount: the PostCompact hook
// calls this, and the count is the only signal anywhere that the re-arm did
// something.
func TestContractKronosRearmRouteClearsAndReportsTheCount(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	contractKronosRecordAgent(t, st, sid)
	if err := st.MarkRecalled(sid, []int64{1, 2, 3}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	if err := st.MarkRecalled("a-cell-session", []int64{4}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}
	mux := contractKronosMux(t)

	rec := contractKronosPost(t, mux, "/api/graph/recall-rearm", map[string]any{"session_id": sid})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Cleared int64 `json:"cleared"`
	}
	contractKronosDecode(t, rec, &got)
	if got.Cleared != 3 {
		t.Errorf("cleared = %d, want 3", got.Cleared)
	}
	if ids, _ := st.RecalledEdgeIDs(sid); len(ids) != 0 {
		t.Errorf("kronos' recall marks = %v, want cleared", ids)
	}
	if ids, _ := st.RecalledEdgeIDs("a-cell-session"); !ids[4] {
		t.Errorf("a cell's recall marks were cleared too: %v", ids)
	}
}

// TestContractKronosRearmRouteRefusals: a blank session is a malformed payload
// (400), while a well-formed session that is not kronos' is an authorization
// failure (403) — the two are different answers because the second one is a
// hook on some OTHER agent's compaction trying to wipe kronos' marks.
func TestContractKronosRearmRouteRefusals(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	mux := contractKronosMux(t)

	// no kronos recorded at all
	if rec := contractKronosPost(t, mux, "/api/graph/recall-rearm", map[string]any{"session_id": sid}); rec.Code != http.StatusForbidden {
		t.Errorf("re-arm with no kronos recorded = status %d, want 403", rec.Code)
	}

	contractKronosRecordAgent(t, st, sid)
	if err := st.MarkRecalled(sid, []int64{1, 2}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	if rec := contractKronosPost(t, mux, "/api/graph/recall-rearm", map[string]any{"session_id": ""}); rec.Code != http.StatusBadRequest {
		t.Errorf("re-arm with a blank session = status %d, want 400", rec.Code)
	}
	if rec := contractKronosPost(t, mux, "/api/graph/recall-rearm",
		map[string]any{"session_id": "99999999-2222-4333-8444-555555555555"}); rec.Code != http.StatusForbidden {
		t.Errorf("re-arm for another session = status %d, want 403", rec.Code)
	}
	if ids, _ := st.RecalledEdgeIDs(sid); len(ids) != 2 {
		t.Errorf("kronos' marks = %v, want both intact after the refusals", ids)
	}
}

// ---------------------------------------------------------------------------
// GET /api/kronos
// ---------------------------------------------------------------------------

// TestContractKronosStatusOmitsContextUntilItIsReported is the pointer rule
// stated for this key: absent means no report. A zeroed object would claim
// kronos' context is at 0%, which is the one reading that looks like good news
// and is never true.
func TestContractKronosStatusOmitsContextUntilItIsReported(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	rec := contractKronosGet(t, mux, "/api/kronos")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var top map[string]json.RawMessage
	contractKronosDecode(t, rec, &top)
	if raw, ok := top["context"]; ok && strings.TrimSpace(string(raw)) != "null" {
		t.Fatalf("context = %s with nothing ever reported, want the key absent (or null) — never a zeroed object", raw)
	}
}

// TestContractKronosStatusReportsTheContextOnceItArrives: the meter reads
// this, and the AGE is what lets a stale reading read as stale rather than as
// current.
func TestContractKronosStatusReportsTheContextOnceItArrives(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	if rec := contractKronosPost(t, mux, "/api/orch/context", map[string]any{
		"session_id":          "sess-a",
		"used_percentage":     42.5,
		"context_window_size": 1_000_000,
	}); rec.Code != http.StatusOK {
		t.Fatalf("POST context = status %d, body %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Context *struct {
			Pct    float64 `json:"pct"`
			Window int64   `json:"window"`
			At     int64   `json:"at"`
		} `json:"context"`
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/kronos"), &got)
	if got.Context == nil {
		t.Fatal("context is absent after a report was accepted")
	}
	if got.Context.Pct != 42.5 {
		t.Errorf("context.pct = %v, want 42.5", got.Context.Pct)
	}
	if got.Context.Window != 1_000_000 {
		t.Errorf("context.window = %d, want the reported window (never a hardcoded one)", got.Context.Window)
	}
	if got.Context.At == 0 {
		t.Error("context.at = 0 — the meter needs the report's age")
	}
}

// TestContractKronosStatusCarriesTheFrozenKeys: the board's whole kronos view
// is built from this one payload, so every key it renders has to be present
// whether or not kronos is up — an absent key and a false one read the same in
// JavaScript, and the board would show a live kronos as disabled.
func TestContractKronosStatusCarriesTheFrozenKeys(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	rec := contractKronosGet(t, mux, "/api/kronos")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var top map[string]json.RawMessage
	contractKronosDecode(t, rec, &top)
	for _, key := range []string{"enabled", "live", "session_id", "pane", "spawned_at"} {
		if _, ok := top[key]; !ok {
			t.Errorf("payload is missing the %q key: %s", key, rec.Body.String())
		}
	}

	var got struct {
		Enabled bool `json:"enabled"`
		Live    bool `json:"live"`
	}
	contractKronosDecode(t, rec, &got)
	if !got.Enabled {
		t.Error("enabled = false with ~/.kraken/kronos.env present")
	}
}

// TestContractKronosStatusReportsDisabled: with the opt-in file absent the
// board must be able to say so, rather than rendering an empty conversation
// that looks like a dead agent.
func TestContractKronosStatusReportsDisabled(t *testing.T) {
	useTestStore(t)
	mux := contractKronosMux(t)

	var got struct {
		Enabled bool `json:"enabled"`
	}
	rec := contractKronosGet(t, mux, "/api/kronos")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	contractKronosDecode(t, rec, &got)
	if got.Enabled {
		t.Error("enabled = true with no ~/.kraken/kronos.env")
	}
}

// ---------------------------------------------------------------------------
// ROUND 2 — the inbox's `total`
//
// Authored SIGHTED, after the review round closed blindness. Derived from
// 50-contract.md § "Amendments (round 2)": "`GET /api/orch/inbox` caps at the
// oldest 100 and gains an additive `total` key (the full unacked count) so a
// backlog is visible without shipping it whole." The cap's BOUNDARY is
// athena-edge's ground; this pins the key's meaning on an ordinary inbox.
// ---------------------------------------------------------------------------

// TestContractKronosInboxCarriesTheFullUnackedTotal: `total` is what keeps the
// truncation from being silent. Kronos reads this JSON with a tool and has no
// second way to count the queue, so an inbox served without it reads as the
// whole inbox whether it is or not — and a kronos that believes it has seen
// everything acks what it read and moves on.
//
// Below the cap the two numbers agree, which is the statement worth pinning
// here: `total` counts UNACKED rows, not delivered ones and not everything ever
// enqueued.
//
// Authored SIGHTED post-review.
func TestContractKronosInboxCarriesTheFullUnackedTotal(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	contractKronosSeedRow(t, st, "job-blocked", "csv-export", "csv-export is blocked", 1_755_000_000_001)
	contractKronosSeedRow(t, st, "note", "", "carl: what is blocking it?", 1_755_000_000_002)
	delivered := contractKronosSeedRow(t, st, "cell-gone", "csv-export", "the cell is gone", 1_755_000_000_003)
	acked := contractKronosSeedRow(t, st, "pr-merged", "csv-export", "PR #12 merged", 1_755_000_000_004)
	if err := st.MarkOrchDelivered([]int64{delivered}, 1_755_000_000_005); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	if err := st.AckOrchItems([]int64{acked}, "noted", 1_755_000_000_009); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	mux := contractKronosMux(t)

	var envelope struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	rec := contractKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	contractKronosDecode(t, rec, &envelope)
	if len(envelope.Items) != 3 {
		t.Fatalf("items = %+v, want the three unacked rows (the delivered one included)", envelope.Items)
	}
	if envelope.Total != 3 {
		t.Fatalf("total = %d, want 3 — the FULL unacked count, matching the items when nothing was truncated", envelope.Total)
	}

	// The ack is what moves both numbers: a settled row leaves the list and the
	// count together, so they can never disagree about what is outstanding.
	if rec := contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": []int64{envelope.Items[0].ID}, "disposition": "escalated",
	}); rec.Code != http.StatusOK {
		t.Fatalf("ack = status %d, body %s", rec.Code, rec.Body.String())
	}
	contractKronosDecode(t, contractKronosGet(t, mux, "/api/orch/inbox"), &envelope)
	if len(envelope.Items) != 2 || envelope.Total != 2 {
		t.Fatalf("after one ack: items = %d, total = %d, want 2 and 2", len(envelope.Items), envelope.Total)
	}
}

// TestContractKronosInboxEmptyTotalIsZero: the empty inbox's own shape. A
// missing key decodes to the same zero, so this is deliberately paired with the
// populated case above — together they say the key is present and means the
// count, rather than that a zero happened to appear.
//
// Authored SIGHTED post-review.
func TestContractKronosInboxEmptyTotalIsZero(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	var top map[string]json.RawMessage
	rec := contractKronosGet(t, mux, "/api/orch/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	contractKronosDecode(t, rec, &top)
	raw, ok := top["total"]
	if !ok {
		t.Fatalf("the empty inbox carries no `total` key: %s", rec.Body.String())
	}
	if strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("total = %s, want 0", raw)
	}
}
