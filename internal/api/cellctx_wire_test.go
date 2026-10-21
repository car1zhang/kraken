package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// cellctx_wire_test.go — the `context` key on webJob: the route's new
// optional count, webContextFor's presence rule, the archived withholding,
// and the wire shape. Lead-authored (direct job, no blind authorship — see
// 90-build-report.md of job 630).

func wireCtxJob(t *testing.T, st *store.Store, slug, session string) *jobs.Job {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", Session: session})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return &jobs.Job{ID: id, Slug: slug, Session: session}
}

func TestCellContextRouteRecordsTotalInputTokens(t *testing.T) {
	st := useTestStore(t)
	j := wireCtxJob(t, st, "wire-ctx", "")
	body, _ := json.Marshal(map[string]any{
		"slug":                "wire-ctx",
		"session_id":          "sess-a",
		"used_percentage":     25,
		"context_window_size": 1_000_000,
		"total_input_tokens":  242_390,
	})
	rec := logicCellCtxPost(t, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s — want 200", rec.Code, rec.Body.String())
	}
	c, ok := jobs.ReadCellContext(st, j.ID, "sess-a")
	if !ok || c.Used != 242_390 {
		t.Fatalf("stored %+v ok=%v, want used 242390", c, ok)
	}
}

func TestCellContextRouteWithoutCountStoresZero(t *testing.T) {
	st := useTestStore(t)
	j := wireCtxJob(t, st, "wire-ctx-old", "")
	body := `{"slug":"wire-ctx-old","session_id":"sess-a","used_percentage":40,"context_window_size":1000000}`
	rec := logicCellCtxPost(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s — an older hook's body must still be accepted", rec.Code, rec.Body.String())
	}
	c, ok := jobs.ReadCellContext(st, j.ID, "sess-a")
	if !ok || c.Used != 0 || c.Pct != 40 {
		t.Fatalf("stored %+v ok=%v, want pct 40 with used 0", c, ok)
	}
}

func TestCellContextRouteRefusesNegativeCount(t *testing.T) {
	st := useTestStore(t)
	j := wireCtxJob(t, st, "wire-ctx-neg", "")
	body := `{"slug":"wire-ctx-neg","session_id":"sess-a","used_percentage":40,"context_window_size":1000000,"total_input_tokens":-5}`
	rec := logicCellCtxPost(t, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s — want 400", rec.Code, rec.Body.String())
	}
	if _, ok := jobs.ReadCellContext(st, j.ID, "sess-a"); ok {
		t.Fatal("a refused report was stored")
	}
}

func TestWebContextForPresenceRule(t *testing.T) {
	st := useTestStore(t)
	j := wireCtxJob(t, st, "wire-ctx-pres", "sess-z")
	if err := jobs.RecordCellContext(st, j.ID, "sess-z", 25, 1_000_000, 242_390); err != nil {
		t.Fatal(err)
	}
	if err := jobs.RecordCellContext(st, j.ID, "sess-mate", 90, 1_000_000, 900_000); err != nil {
		t.Fatal(err)
	}
	ctxIdx := jobs.CellContextIndex(st)
	hIdx := jobs.HandoffIndex(st)

	got := webContextFor(ctxIdx, hIdx, j)
	if got == nil {
		t.Fatal("webContextFor = nil for a stamped session with a row")
	}
	if got.Pct != 25 || got.Window != 1_000_000 || got.Used != 242_390 || got.At == 0 {
		t.Errorf("webContextFor = %+v, want the row's pct/window/used and a non-zero at", got)
	}
	if got.HandoffPct != jobs.HandoffThreshold() {
		t.Errorf("handoff_pct = %v, want the daemon's threshold %v", got.HandoffPct, jobs.HandoffThreshold())
	}
	if got.Handoff != nil {
		t.Errorf("handoff = %+v, want none when the pass has no state", got.Handoff)
	}
	// display state plays no part: the same job reads the same whatever its state
	for _, state := range []string{"working", "blocked", "done"} {
		jj := *j
		jj.State = state
		if webContextFor(ctxIdx, hIdx, &jj) == nil {
			t.Errorf("webContextFor = nil in state %s — presence must not flicker with state", state)
		}
	}
	// no stamped session, or a session with no row: no key
	if webContextFor(ctxIdx, hIdx, &jobs.Job{ID: j.ID}) != nil {
		t.Error("webContextFor served a fill for a job with no stamped session")
	}
	if webContextFor(ctxIdx, hIdx, &jobs.Job{ID: j.ID, Session: "sess-nobody"}) != nil {
		t.Error("webContextFor served a fill for a session with no row")
	}
}

func TestWebContextForAttachesHandoffForTheSameSessionOnly(t *testing.T) {
	st := useTestStore(t)
	j := wireCtxJob(t, st, "wire-ctx-h", "sess-z")
	if err := jobs.RecordCellContext(st, j.ID, "sess-z", 81, 1_000_000, 812_000); err != nil {
		t.Fatal(err)
	}
	// state for another session: not attached
	if err := st.SetKV(fmt.Sprintf("auto_handoff:%d", j.ID), `{"session_id":"sess-prev","fires":3,"last_fire_at":5,"armed":false,"exhausted":true}`); err != nil {
		t.Fatal(err)
	}
	got := webContextFor(jobs.CellContextIndex(st), jobs.HandoffIndex(st), j)
	if got == nil || got.Handoff != nil {
		t.Fatalf("webContextFor = %+v, want the fill with NO handoff for a previous session's state", got)
	}
	// state for this session: attached verbatim
	if err := st.SetKV(fmt.Sprintf("auto_handoff:%d", j.ID), `{"session_id":"sess-z","fires":1,"last_fire_at":1792590000000,"armed":false,"exhausted":false}`); err != nil {
		t.Fatal(err)
	}
	got = webContextFor(jobs.CellContextIndex(st), jobs.HandoffIndex(st), j)
	if got == nil || got.Handoff == nil {
		t.Fatalf("webContextFor = %+v, want a handoff sub-object for this session's state", got)
	}
	if got.Handoff.Fires != 1 || got.Handoff.LastAt != 1_792_590_000_000 || got.Handoff.Exhausted {
		t.Errorf("handoff = %+v, want fires 1, last_at 1792590000000, not exhausted", got.Handoff)
	}
}

// TestArchivedWebJobWithholdsContext pins the archived-card decision for
// context the way TestArchivedWebJobWithholdsLoad pins it for load: the row
// is real (the active rule would serve it) and the archived card carries no
// key.
func TestArchivedWebJobWithholdsContext(t *testing.T) {
	st := useTestStore(t)
	j := wireCtxJob(t, st, "wire-ctx-arch", "sess-z")
	if err := jobs.RecordCellContext(st, j.ID, "sess-z", 60, 1_000_000, 600_000); err != nil {
		t.Fatal(err)
	}
	if webContextFor(jobs.CellContextIndex(st), jobs.HandoffIndex(st), j) == nil {
		t.Fatal("the fixture's row would not be served even on the active loop — proves nothing")
	}
	if _, err := st.ArchiveJob(j.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	archived := jobs.LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("LoadArchivedJobs = %d, want 1", len(archived))
	}
	wj := archivedWebJob(archived[0], st, nil, nil)
	if wj.Context != nil {
		t.Fatalf("archived card carries context = %+v, want no key at all", wj.Context)
	}
	// the row itself survives: unarchiving brings the key back
	if _, ok := jobs.ReadCellContext(st, j.ID, "sess-z"); !ok {
		t.Fatal("archiving deleted the context row — it must survive so the key returns on unarchive")
	}
}

// webJobContextSpec covers board.go's webJobContext, webJob's "context"
// object. Every key but `handoff` is required: no omitempty inside the type,
// so a zero number still rides and absence is said by the "context" key
// itself being gone (webJobSpec marks it optional). `handoff` is the one
// omitempty sub-object: absent when the daemon's pass holds no state for the
// reporting session.
var webJobContextSpec = map[string]fieldSpec{
	"pct":         {kind: wireNumber, required: true},
	"window":      {kind: wireNumber, required: true},
	"used":        {kind: wireNumber, required: true},
	"at":          {kind: wireNumber, required: true},
	"handoff_pct": {kind: wireNumber, required: true},
	"handoff":     {kind: wireObject, required: false},
}

var webJobHandoffSpec = map[string]fieldSpec{
	"fires":     {kind: wireNumber, required: true},
	"last_at":   {kind: wireNumber, required: true},
	"exhausted": {kind: wireBool, required: true},
}

func TestWebJobContextWireShape(t *testing.T) {
	data, err := json.Marshal(webJobContext{})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobContext (zero)", data, webJobContextSpec)
	var zero map[string]json.RawMessage
	if err := json.Unmarshal(data, &zero); err != nil {
		t.Fatal(err)
	}
	if _, ok := zero["handoff"]; ok {
		t.Error("a zero webJobContext serialised a handoff key — it must be omitted when nil")
	}

	// this cell's real report (job 630, 2026-10-21), with a fired handoff
	populated := webJobContext{
		Pct: 25, Window: 1_000_000, Used: 242_390, At: 1_792_599_370_254, HandoffPct: 75,
		Handoff: &webJobHandoff{Fires: 1, LastAt: 1_792_590_000_000, Exhausted: false},
	}
	data, err = json.Marshal(populated)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobContext (populated)", data, webJobContextSpec)
	var full map[string]json.RawMessage
	if err := json.Unmarshal(data, &full); err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobHandoff", full["handoff"], webJobHandoffSpec)
}
