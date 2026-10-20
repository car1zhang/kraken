package store

import (
	"errors"
	"testing"
)

// fleet_edge_test.go — the EDGE dimension of 50-contract.md slice G2 for
// package store: the nodes registry's column-preservation rules on update and
// on a failed poll, ErrNotFound on every unknown id, empty-not-nil listings,
// ordering ties, and the pair_devices refusals. Written blind from the
// contract: internal/store/nodes.go, pair.go and schema.go were not read.
// Happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeFleet`.

func edgeFleetNode(id, label string, online bool, seen int64) *NodeRow {
	return &NodeRow{
		ID: id, Label: label, Operator: "Jess", DNSName: id + ".tail072ad8.ts.net",
		IP: "100.70.0.1", Port: 7777, Build: "abc1234", Contract: 7,
		LastSeen: seen, Online: online,
	}
}

func edgeFleetGet(t *testing.T, s *Store, id string) *NodeRow {
	t.Helper()
	n, err := s.GetNode(id)
	if err != nil {
		t.Fatalf("GetNode(%s): %v", id, err)
	}
	return n
}

// `UpsertNode`: "On insert `FirstSeen` is set from `r.FirstSeen` (or
// `LastSeen` when zero)".
func TestEdgeFleetUpsertInsertZeroFirstSeenTakesLastSeen(t *testing.T) {
	s := openTest(t)
	r := edgeFleetNode("n-zero", "studio", true, 1234)
	r.FirstSeen = 0
	if err := s.UpsertNode(r); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if got := edgeFleetGet(t, s, "n-zero").FirstSeen; got != 1234 {
		t.Fatalf("FirstSeen = %d, want 1234 (LastSeen)", got)
	}
}

// `UpsertNode`: "on update `first_seen` is NEVER changed and `jobs_json`,
// `last_ok`, `last_err` are NOT touched" — even when the update row carries
// values for all four.
func TestEdgeFleetUpsertUpdatePreservesPollColumns(t *testing.T) {
	s := openTest(t)
	r := edgeFleetNode("n-up", "studio", true, 100)
	r.FirstSeen = 50
	if err := s.UpsertNode(r); err != nil {
		t.Fatalf("UpsertNode insert: %v", err)
	}
	if err := s.SetNodePoll("n-up", true, "", `[{"slug":"a"}]`, 200); err != nil {
		t.Fatalf("SetNodePoll ok: %v", err)
	}
	if err := s.SetNodePoll("n-up", false, "connection refused", "", 300); err != nil {
		t.Fatalf("SetNodePoll fail: %v", err)
	}

	u := edgeFleetNode("n-up", "studio-renamed", true, 400)
	u.FirstSeen = 9999
	u.LastOK = 8888
	u.LastErr = "from the update row"
	u.JobsJSON = `[{"slug":"overwritten"}]`
	if err := s.UpsertNode(u); err != nil {
		t.Fatalf("UpsertNode update: %v", err)
	}
	got := edgeFleetGet(t, s, "n-up")
	if got.FirstSeen != 50 {
		t.Errorf("FirstSeen = %d, want 50 (never changed on update)", got.FirstSeen)
	}
	if got.JobsJSON != `[{"slug":"a"}]` {
		t.Errorf("JobsJSON = %q, want the polled value", got.JobsJSON)
	}
	if got.LastOK != 200 {
		t.Errorf("LastOK = %d, want 200", got.LastOK)
	}
	if got.LastErr != "connection refused" {
		t.Errorf("LastErr = %q, want the poll's error", got.LastErr)
	}
	if got.Label != "studio-renamed" || got.LastSeen != 400 {
		t.Errorf("identity columns not updated: label=%q last_seen=%d", got.Label, got.LastSeen)
	}
}

// `SetNodePoll`: "`!ok`: `last_err = errText`, `last_seen = at`; `jobs_json`
// and `last_ok` unchanged; `online` unchanged." — checked from an offline row
// (a failure must not set it online the way a success does) and from an
// online one (a single failure must not set it offline).
func TestEdgeFleetSetNodePollFailurePreserves(t *testing.T) {
	for _, online := range []bool{false, true} {
		s := openTest(t)
		if err := s.UpsertNode(edgeFleetNode("n-f", "studio", true, 10)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodePoll("n-f", true, "", `[{"slug":"kept"}]`, 100); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodeOnline("n-f", online); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodePoll("n-f", false, "timeout", `[{"slug":"ignored"}]`, 200); err != nil {
			t.Fatalf("SetNodePoll(!ok): %v", err)
		}
		got := edgeFleetGet(t, s, "n-f")
		if got.JobsJSON != `[{"slug":"kept"}]` {
			t.Errorf("online=%v: JobsJSON = %q, want unchanged", online, got.JobsJSON)
		}
		if got.LastOK != 100 {
			t.Errorf("online=%v: LastOK = %d, want 100", online, got.LastOK)
		}
		if got.Online != online {
			t.Errorf("online=%v: Online = %v, want unchanged", online, got.Online)
		}
		if got.LastErr != "timeout" || got.LastSeen != 200 {
			t.Errorf("online=%v: LastErr=%q LastSeen=%d, want timeout/200", online, got.LastErr, got.LastSeen)
		}
	}
}

// `SetNodePoll` ok: "`last_err` set to the empty string" — a success after a failure clears it.
func TestEdgeFleetSetNodePollSuccessClearsError(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertNode(edgeFleetNode("n-c", "studio", false, 10)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodePoll("n-c", false, "refused", "", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodePoll("n-c", true, "", `[]`, 30); err != nil {
		t.Fatal(err)
	}
	got := edgeFleetGet(t, s, "n-c")
	if got.LastErr != "" || !got.Online || got.LastOK != 30 {
		t.Fatalf("after success: LastErr=%q Online=%v LastOK=%d", got.LastErr, got.Online, got.LastOK)
	}
}

// "Unknown id → `ErrNotFound`" for SetNodePoll (both arms) and SetNodeOnline,
// and `GetNode`: "`ErrNotFound` when absent."
func TestEdgeFleetNodeUnknownIDIsErrNotFound(t *testing.T) {
	s := openTest(t)
	if err := s.SetNodePoll("ghost", true, "", "[]", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetNodePoll(ok, unknown) = %v, want ErrNotFound", err)
	}
	if err := s.SetNodePoll("ghost", false, "x", "", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetNodePoll(!ok, unknown) = %v, want ErrNotFound", err)
	}
	if err := s.SetNodeOnline("ghost", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetNodeOnline(unknown) = %v, want ErrNotFound", err)
	}
	if n, err := s.GetNode("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetNode(unknown) = (%+v, %v), want ErrNotFound", n, err)
	}
	// none of those calls may have created a row
	if rows, _ := s.ListNodes(); len(rows) != 0 {
		t.Errorf("unknown-id calls created %d rows", len(rows))
	}
}

// `ListNodes`: "Empty slice (not nil) when none."
func TestEdgeFleetListNodesEmptyIsNotNil(t *testing.T) {
	s := openTest(t)
	rows, err := s.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if rows == nil {
		t.Fatalf("ListNodes on an empty registry returned nil, want an empty slice")
	}
	if len(rows) != 0 {
		t.Fatalf("ListNodes returned %d rows", len(rows))
	}
}

// `ListNodes`: "Online rows first, then by `Label` ascending
// (case-insensitive), then `ID`." — a label tie differing only in case breaks
// on ID, an uppercase label does not sort before a lowercase one, and an
// offline row whose label sorts first still comes last.
func TestEdgeFleetListNodesOrderingTies(t *testing.T) {
	s := openTest(t)
	for _, r := range []*NodeRow{
		edgeFleetNode("d", "aardvark", false, 1),
		edgeFleetNode("c", "Beta", true, 1),
		edgeFleetNode("b", "alpha", true, 1),
		edgeFleetNode("a", "Alpha", true, 1),
	} {
		if err := s.UpsertNode(r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.ID)
	}
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("ListNodes ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListNodes ids = %v, want %v", got, want)
		}
	}
}

// `AddPairDevice`: "a duplicate `TokenHash` is an error" — and the first row
// survives it alone.
func TestEdgeFleetAddPairDeviceDuplicateHash(t *testing.T) {
	s := openTest(t)
	d := &PairDevice{Label: "iPhone", TailnetName: "iphone173", TokenHash: "aa11", CreatedAt: 1}
	if _, err := s.AddPairDevice(d); err != nil {
		t.Fatalf("first AddPairDevice: %v", err)
	}
	dup := &PairDevice{Label: "iPad", TailnetName: "ipad", TokenHash: "aa11", CreatedAt: 2}
	if _, err := s.AddPairDevice(dup); err == nil {
		t.Fatalf("duplicate TokenHash accepted")
	}
	rows, err := s.ListPairDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Label != "iPhone" {
		t.Fatalf("after duplicate: rows = %+v, want only the first", rows)
	}
}

// `GetPairDeviceByHash`: "`ErrNotFound` when absent."; `DeletePairDevice`:
// "`ErrNotFound` when no row had that id."
func TestEdgeFleetPairDeviceUnknownIsErrNotFound(t *testing.T) {
	s := openTest(t)
	if d, err := s.GetPairDeviceByHash("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPairDeviceByHash(absent) = (%+v, %v), want ErrNotFound", d, err)
	}
	if err := s.DeletePairDevice(424242); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeletePairDevice(absent) = %v, want ErrNotFound", err)
	}
	id, err := s.AddPairDevice(&PairDevice{Label: "x", TokenHash: "bb22", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePairDevice(id); err != nil {
		t.Fatalf("DeletePairDevice(id): %v", err)
	}
	if err := s.DeletePairDevice(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeletePairDevice(id) = %v, want ErrNotFound", err)
	}
	if _, err := s.GetPairDeviceByHash("bb22"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted device still found by hash: %v", err)
	}
}

// `ListPairDevices`: "empty slice when none"; `DeleteAllPairDevices`:
// "Returns the count removed." — 0 on an empty table, then n.
func TestEdgeFleetPairDevicesEmptyAndDeleteAllCount(t *testing.T) {
	s := openTest(t)
	rows, err := s.ListPairDevices()
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("ListPairDevices on empty = %#v, want empty non-nil", rows)
	}
	if n, err := s.DeleteAllPairDevices(); err != nil || n != 0 {
		t.Fatalf("DeleteAllPairDevices on empty = (%d, %v), want (0, nil)", n, err)
	}
	for i, h := range []string{"c1", "c2", "c3"} {
		if _, err := s.AddPairDevice(&PairDevice{Label: h, TokenHash: h, CreatedAt: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.DeleteAllPairDevices(); err != nil || n != 3 {
		t.Fatalf("DeleteAllPairDevices = (%d, %v), want (3, nil)", n, err)
	}
	if rows, _ := s.ListPairDevices(); len(rows) != 0 {
		t.Fatalf("after DeleteAll: %d rows remain", len(rows))
	}
}

// `ListPairDevices`: "`CreatedAt` ascending" — independent of insertion order.
func TestEdgeFleetListPairDevicesOrderIgnoresInsertion(t *testing.T) {
	s := openTest(t)
	for _, d := range []*PairDevice{
		{Label: "late", TokenHash: "h3", CreatedAt: 300},
		{Label: "early", TokenHash: "h1", CreatedAt: 100},
		{Label: "mid", TokenHash: "h2", CreatedAt: 200},
	} {
		if _, err := s.AddPairDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListPairDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Label != "early" || rows[1].Label != "mid" || rows[2].Label != "late" {
		var got []string
		for _, r := range rows {
			got = append(got, r.Label)
		}
		t.Fatalf("ListPairDevices order = %v, want [early mid late]", got)
	}
}
