package jobs

import (
	"encoding/json"
	"testing"
)

// cellctx_wire_test.go — the board-facing readers of the cell context rows:
// the widened record (Used), the one-query indexes, and the two presence
// rules (CellContextFor, HandoffFor). Lead-authored (direct job, no blind
// authorship — see 90-build-report.md of job 630).

func TestCellContextRoundTripCarriesUsed(t *testing.T) {
	st := useTestStore(t)
	if err := RecordCellContext(st, 630, "sess-z", 25, 1_000_000, 242_390); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}
	c, ok := ReadCellContext(st, 630, "sess-z")
	if !ok || c.Used != 242_390 || c.Pct != 25 || c.Window != 1_000_000 {
		t.Fatalf("ReadCellContext = %+v, ok=%v; want used 242390, pct 25, window 1000000", c, ok)
	}
	raw, err := st.GetKV("cell_ctx:630:sess-z")
	if err != nil {
		t.Fatal(err)
	}
	var tagged struct {
		Used int64 `json:"used"`
	}
	if err := json.Unmarshal([]byte(raw), &tagged); err != nil || tagged.Used != 242_390 {
		t.Errorf("stored %q, want the count under json tag `used`", raw)
	}
}

func TestCellContextRefusesANegativeCount(t *testing.T) {
	st := useTestStore(t)
	if err := RecordCellContext(st, 1, "s", 10, 1_000_000, -1); err == nil {
		t.Fatal("RecordCellContext accepted a negative token count")
	}
	if _, ok := ReadCellContext(st, 1, "s"); ok {
		t.Fatal("a refused report was stored")
	}
}

// A row written before the field existed carries no `used` key: it reads back
// as a real report with Used 0, never as absent.
func TestCellContextLegacyRowReadsAsUsedZero(t *testing.T) {
	st := useTestStore(t)
	legacy := `{"session_id":"sess-old","pct":57,"window":1000000,"at":1791492403788}`
	if err := st.SetKV("cell_ctx:551:sess-old", legacy); err != nil {
		t.Fatal(err)
	}
	c, ok := ReadCellContext(st, 551, "sess-old")
	if !ok || c.Used != 0 || c.Pct != 57 {
		t.Fatalf("legacy row read as %+v ok=%v, want present with used 0 and pct 57", c, ok)
	}
	idx := CellContextIndex(st)
	if got, ok := idx["cell_ctx:551:sess-old"]; !ok || got.Used != 0 || got.Pct != 57 {
		t.Fatalf("index read the legacy row as %+v ok=%v", got, ok)
	}
}

func TestCellContextIndexAndPresenceRule(t *testing.T) {
	st := useTestStore(t)
	if err := RecordCellContext(st, 630, "sess-z", 25, 1_000_000, 242_390); err != nil {
		t.Fatal(err)
	}
	if err := RecordCellContext(st, 630, "sess-teammate", 90, 1_000_000, 900_000); err != nil {
		t.Fatal(err)
	}
	if err := RecordCellContext(st, 631, "sess-other-job", 50, 200_000, 100_000); err != nil {
		t.Fatal(err)
	}
	// a row that does not parse, and one naming no session, are left out
	if err := st.SetKV("cell_ctx:9:bad", "{not json"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("cell_ctx:9:nosess", `{"pct":10}`); err != nil {
		t.Fatal(err)
	}

	idx := CellContextIndex(st)
	if len(idx) != 3 {
		t.Fatalf("CellContextIndex has %d rows %v, want the 3 well-formed ones", len(idx), idx)
	}

	// stamped session, matching row
	j := &Job{ID: 630, Slug: "ctx", Session: "sess-z"}
	c, ok := CellContextFor(idx, j)
	if !ok || c.Used != 242_390 || c.Pct != 25 {
		t.Fatalf("CellContextFor(stamped) = %+v ok=%v, want zeus' row", c, ok)
	}
	// a row for another session of the same job is not this job's fill
	if _, ok := CellContextFor(idx, &Job{ID: 630, Session: "sess-nobody"}); ok {
		t.Fatal("CellContextFor served a row under a session other than the stamped one")
	}
	// no stamped session: no key to look under
	if _, ok := CellContextFor(idx, &Job{ID: 630}); ok {
		t.Fatal("CellContextFor served a fill for a job with no stamped session")
	}
	// another job's row is not this job's
	if _, ok := CellContextFor(idx, &Job{ID: 632, Session: "sess-other-job"}); ok {
		t.Fatal("CellContextFor served job 631's row to job 632")
	}
	if _, ok := CellContextFor(nil, j); ok {
		t.Fatal("CellContextFor served a fill out of a nil index")
	}
	if _, ok := CellContextFor(idx, nil); ok {
		t.Fatal("CellContextFor served a fill for a nil job")
	}
}

func TestCellContextIndexNilStore(t *testing.T) {
	if CellContextIndex(nil) != nil {
		t.Fatal("CellContextIndex(nil) != nil")
	}
	if HandoffIndex(nil) != nil {
		t.Fatal("HandoffIndex(nil) != nil")
	}
}

func TestHandoffIndexAndPresenceRule(t *testing.T) {
	st := useTestStore(t)
	if err := writeAutoHandoffState(st, 630, autoHandoffState{SessionID: "sess-z", Fires: 2, LastFireAt: 1_792_590_000_000, Armed: false, Exhausted: false}); err != nil {
		t.Fatal(err)
	}
	if err := writeAutoHandoffState(st, 553, autoHandoffState{SessionID: "sess-old", Fires: 0, LastFireAt: 1_791_574_839_068, Armed: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("auto_handoff:notanid", `{"session_id":"x"}`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("auto_handoff:77", "{broken"); err != nil {
		t.Fatal(err)
	}

	idx := HandoffIndex(st)
	if len(idx) != 2 {
		t.Fatalf("HandoffIndex has %d rows %v, want the 2 well-formed ones keyed by job id", len(idx), idx)
	}
	h, ok := HandoffFor(idx, &Job{ID: 630, Session: "sess-z"})
	if !ok || h.Fires != 2 || h.LastFireAt != 1_792_590_000_000 || h.Exhausted {
		t.Fatalf("HandoffFor(630, sess-z) = %+v ok=%v, want fires 2, the stored last fire, not exhausted", h, ok)
	}
	// state left by a previous run's session is not this session's streak
	if _, ok := HandoffFor(idx, &Job{ID: 553, Session: "sess-new"}); ok {
		t.Fatal("HandoffFor served a previous session's state")
	}
	if _, ok := HandoffFor(idx, &Job{ID: 630}); ok {
		t.Fatal("HandoffFor served state for a job with no stamped session")
	}
	if _, ok := HandoffFor(idx, &Job{ID: 999, Session: "s"}); ok {
		t.Fatal("HandoffFor served state for a job with none")
	}
}

func TestHandoffThresholdIsTheDaemonConstant(t *testing.T) {
	if HandoffThreshold() != autoHandoffThreshold {
		t.Fatalf("HandoffThreshold() = %v, want autoHandoffThreshold %v", HandoffThreshold(), autoHandoffThreshold)
	}
}
