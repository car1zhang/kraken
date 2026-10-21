package store

// fleet_contract_test.go — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice G2 (store: schema v34, nodes, pair_devices,
// jobs.visibility). Authored from artifacts/50-contract.md alone; no
// implementation file was read. LOGIC dimension: happy paths and stated
// invariants. ErrNotFound arms, duplicate-hash refusal and other error paths
// are athena-edge's.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// --- schema v34 ----------------------------------------------------------

// Contract G2 `schemaVersion`: "const schemaVersion = 34 — v34 appends
// migrations[34] with no hook". 35 since job_stacks was appended after it
// (schemaV35, job 622).
func TestContractFleetSchemaV34(t *testing.T) {
	if schemaVersion != 35 {
		t.Errorf("schemaVersion = %d, want 35", schemaVersion)
	}
	step, ok := migrations[34]
	if !ok {
		t.Fatal("migrations[34] is not registered")
	}
	// AMENDED 15:12: migrations[34] carries migrateV34Visibility, which adds
	// the visibility column only when missing (idempotent).
	if step.hook == nil {
		t.Fatal("migrations[34] has no hook; the amended contract names migrateV34Visibility")
	}
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 35 {
		t.Errorf("fresh store user_version = %d, want 35", version)
	}
	// the hook is idempotent over a jobs table that already has the column
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := step.hook(tx); err != nil {
		tx.Rollback()
		t.Fatalf("re-running the v34 hook on a v34 store: %v", err)
	}
	if _, err := tx.Exec(step.sql); err != nil {
		tx.Rollback()
		t.Fatalf("re-running the v34 SQL on a v34 store: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nodes", "pair_devices"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing on a fresh store", table)
		}
	}
}

// contractFleetOpenV33 replays migrations[1..33] exactly as migrate() does
// (the harness convention library_contract_test.go established), seeds one
// job row at v33, closes, and returns the path — so the v34 assertions run
// against a database that REACHED v34 by migration.
func contractFleetOpenV33(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kraken.db")
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open v33 db: %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= 33; v++ {
		step, ok := migrations[v]
		if !ok {
			tx.Rollback()
			t.Fatalf("no migration registered for version %d", v)
		}
		if _, err := tx.Exec(step.sql); err != nil {
			tx.Rollback()
			t.Fatalf("apply migration %d: %v", v, err)
		}
		if step.hook != nil {
			if err := step.hook(tx); err != nil {
				tx.Rollback()
				t.Fatalf("apply migration %d hook: %v", v, err)
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 33`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs (slug, repo) VALUES ('legacy-v33', 'kraken')`); err != nil {
		t.Fatalf("seed v33 job: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Contract G2: "A v33 store migrates in place; every pre-existing job row
// reads readonly." + `Job.Visibility`: "Scanned from the visibility column;
// "readonly" on a migrated row."
func TestContractFleetV33MigratesInPlaceToReadonly(t *testing.T) {
	path := contractFleetOpenV33(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v33 store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 35 {
		t.Fatalf("migrated user_version = %d, want 35", version)
	}
	j, err := s.GetJobBySlug("legacy-v33")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Visibility != "readonly" {
		t.Errorf("migrated job Visibility = %q, want readonly", j.Visibility)
	}
	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes on migrated store: %v", err)
	}
	if nodes == nil || len(nodes) != 0 {
		t.Errorf("ListNodes on migrated store = %#v, want empty non-nil", nodes)
	}
	devs, err := s.ListPairDevices()
	if err != nil {
		t.Fatalf("ListPairDevices on migrated store: %v", err)
	}
	if devs == nil || len(devs) != 0 {
		t.Errorf("ListPairDevices on migrated store = %#v, want empty non-nil", devs)
	}
}

// Contract G2 `Job.Visibility`: "visibility IS in jobFieldColumns (exactly as
// color is), so UpdateFields may set it". The existing field writer is
// UpdateJobFields (the contract's "UpdateFields"); it round-trips through
// GetJobBySlug and GetJobByID.
func TestContractFleetVisibilityWritableThroughFieldUpdate(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "vis"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateJobFields(id, j.Rev, map[string]any{"visibility": "public"}); err != nil {
		t.Fatalf("UpdateJobFields(visibility): %v", err)
	}
	got, err := s.GetJobBySlug("vis")
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != "public" {
		t.Errorf("Visibility after field update = %q, want public", got.Visibility)
	}
	byID, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if byID.Visibility != "public" {
		t.Errorf("GetJobByID Visibility = %q, want public", byID.Visibility)
	}
}

// --- nodes ---------------------------------------------------------------

func contractFleetNode(id, label string, online bool) *NodeRow {
	return &NodeRow{
		ID: id, Label: label, Operator: "Jess", DNSName: label + ".tail072ad8.ts.net",
		IP: "100.70.1.2", Port: 7777, Build: "abc1234", Contract: 7,
		FirstSeen: 1_000, LastSeen: 1_000, Online: online,
	}
}

// Contract G2 `UpsertNode`: "Inserts or updates by ID. On insert FirstSeen is
// set from r.FirstSeen" + `GetNode`.
func TestContractFleetUpsertNodeInsertRoundTrip(t *testing.T) {
	s := openTest(t)
	in := &NodeRow{
		ID: "nSTUDIOmini77CNTRL", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IP: "100.70.1.2", Port: 7777,
		Build: "abc1234", Contract: 7, FirstSeen: 1_700_000_000_000, LastSeen: 1_700_000_000_500,
		Online: true,
	}
	if err := s.UpsertNode(in); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	got, err := s.GetNode("nSTUDIOmini77CNTRL")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	want := *in
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetNode = %+v\nwant     %+v", *got, want)
	}
}

// "On insert FirstSeen is set from r.FirstSeen (or LastSeen when zero)".
func TestContractFleetUpsertNodeInsertFirstSeenFallsBackToLastSeen(t *testing.T) {
	s := openTest(t)
	r := contractFleetNode("n-a", "alpha", true)
	r.FirstSeen = 0
	r.LastSeen = 42_000
	if err := s.UpsertNode(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode("n-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstSeen != 42_000 {
		t.Errorf("FirstSeen = %d, want LastSeen 42000 when inserted with zero", got.FirstSeen)
	}
}

// "on update first_seen is NEVER changed and jobs_json, last_ok, last_err are
// NOT touched (only identity columns: label, operator, dns_name, ip, port,
// build, contract, last_seen, online)."
func TestContractFleetUpsertNodeUpdateTouchesOnlyIdentity(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertNode(contractFleetNode("n-a", "alpha", true)); err != nil {
		t.Fatal(err)
	}
	// poll state the update must leave alone
	if err := s.SetNodePoll("n-a", true, "", `[{"slug":"x"}]`, 2_000); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodePoll("n-a", false, "dial tcp: timeout", "", 3_000); err != nil {
		t.Fatal(err)
	}

	upd := &NodeRow{
		ID: "n-a", Label: "alpha-renamed", Operator: "Jessica", DNSName: "alpha2.tail072ad8.ts.net",
		IP: "100.70.9.9", Port: 7878, Build: "def5678", Contract: 8,
		FirstSeen: 9_999, LastSeen: 4_000, LastOK: 77, LastErr: "should not land",
		Online: false, JobsJSON: `[{"slug":"should-not-land"}]`,
	}
	if err := s.UpsertNode(upd); err != nil {
		t.Fatalf("UpsertNode update: %v", err)
	}
	got, err := s.GetNode("n-a")
	if err != nil {
		t.Fatal(err)
	}
	want := NodeRow{
		ID: "n-a", Label: "alpha-renamed", Operator: "Jessica", DNSName: "alpha2.tail072ad8.ts.net",
		IP: "100.70.9.9", Port: 7878, Build: "def5678", Contract: 8,
		FirstSeen: 1_000,               // never changed
		LastSeen:  4_000,               // identity column
		LastOK:    2_000,               // untouched by upsert
		LastErr:   "dial tcp: timeout", // untouched by upsert
		Online:    false,               // identity column
		JobsJSON:  `[{"slug":"x"}]`,    // untouched by upsert
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("after update GetNode = %+v\nwant                  %+v", *got, want)
	}
}

// Contract G2 `SetNodePoll` ok arm: "last_ok = at, last_err = empty, jobs_json =
// jobsJSON, online = 1, last_seen = at."
func TestContractFleetSetNodePollOK(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertNode(contractFleetNode("n-a", "alpha", false)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodePoll("n-a", false, "boom", "", 1_500); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodePoll("n-a", true, "ignored", `[{"slug":"a"},{"slug":"b"}]`, 2_500); err != nil {
		t.Fatalf("SetNodePoll ok: %v", err)
	}
	got, err := s.GetNode("n-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastOK != 2_500 || got.LastErr != "" || got.JobsJSON != `[{"slug":"a"},{"slug":"b"}]` || !got.Online || got.LastSeen != 2_500 {
		t.Errorf("after ok poll: LastOK=%d LastErr=%q JobsJSON=%q Online=%v LastSeen=%d; want 2500 \"\" [a,b] true 2500",
			got.LastOK, got.LastErr, got.JobsJSON, got.Online, got.LastSeen)
	}
	if got.FirstSeen != 1_000 || got.Label != "alpha" {
		t.Errorf("ok poll moved an unrelated column: %+v", got)
	}
}

// Contract G2 `SetNodePoll` !ok arm: "last_err = errText, last_seen = at;
// jobs_json and last_ok unchanged; online unchanged."
func TestContractFleetSetNodePollFailureKeepsCardsAndOnline(t *testing.T) {
	for _, online := range []bool{true, false} {
		t.Run(fmt.Sprintf("online=%v", online), func(t *testing.T) {
			s := openTest(t)
			if err := s.UpsertNode(contractFleetNode("n-a", "alpha", true)); err != nil {
				t.Fatal(err)
			}
			if err := s.SetNodePoll("n-a", true, "", `[{"slug":"kept"}]`, 2_000); err != nil {
				t.Fatal(err)
			}
			if !online {
				if err := s.SetNodeOnline("n-a", false); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SetNodePoll("n-a", false, "connection refused", `[{"slug":"ignored"}]`, 3_000); err != nil {
				t.Fatalf("SetNodePoll fail: %v", err)
			}
			got, err := s.GetNode("n-a")
			if err != nil {
				t.Fatal(err)
			}
			if got.LastErr != "connection refused" || got.LastSeen != 3_000 {
				t.Errorf("LastErr=%q LastSeen=%d, want connection refused / 3000", got.LastErr, got.LastSeen)
			}
			if got.JobsJSON != `[{"slug":"kept"}]` || got.LastOK != 2_000 {
				t.Errorf("failed poll moved cards: JobsJSON=%q LastOK=%d", got.JobsJSON, got.LastOK)
			}
			if got.Online != online {
				t.Errorf("failed poll changed Online to %v, want unchanged %v", got.Online, online)
			}
		})
	}
}

// Contract G2 `SetNodeOnline`: "Flips online only."
func TestContractFleetSetNodeOnlineFlipsOnlyOnline(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertNode(contractFleetNode("n-a", "alpha", true)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodePoll("n-a", true, "", `[{"slug":"x"}]`, 2_000); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetNode("n-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeOnline("n-a", false); err != nil {
		t.Fatalf("SetNodeOnline(false): %v", err)
	}
	after, err := s.GetNode("n-a")
	if err != nil {
		t.Fatal(err)
	}
	want := *before
	want.Online = false
	if !reflect.DeepEqual(*after, want) {
		t.Errorf("after SetNodeOnline(false) = %+v\nwant                     %+v", *after, want)
	}
	if err := s.SetNodeOnline("n-a", true); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetNode("n-a")
	if !again.Online {
		t.Error("SetNodeOnline(true) did not flip back")
	}
}

// Contract G2 `ListNodes`: "Online rows first, then by Label ascending
// (case-insensitive), then ID. Empty slice (not nil) when none."
func TestContractFleetListNodesOrdering(t *testing.T) {
	s := openTest(t)
	empty, err := s.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ListNodes on empty registry = %#v, want empty non-nil slice", empty)
	}
	for _, r := range []*NodeRow{
		contractFleetNode("id-5", "zulu", false),
		contractFleetNode("id-4", "Bravo", true),
		contractFleetNode("id-3", "alpha", false),
		contractFleetNode("id-2", "charlie", true),
		contractFleetNode("id-1", "bravo", true), // same label, case-insensitively, as id-4
	} {
		if err := s.UpsertNode(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range got {
		ids = append(ids, n.ID)
	}
	want := []string{"id-1", "id-4", "id-2", "id-3", "id-5"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ListNodes order = %v, want %v (online first, label ci, then id)", ids, want)
	}
}

// --- pair_devices --------------------------------------------------------

// Contract G2 `AddPairDevice` ("Returns the new row id") +
// `GetPairDeviceByHash`.
func TestContractFleetPairDeviceAddAndGetByHash(t *testing.T) {
	s := openTest(t)
	in := &PairDevice{
		Label: "Carl's iPhone", TailnetName: "iphone173",
		TokenHash:  "0b9f31c5403adf5a5676eafcf2ed391dec857be4b8d54e9ae0923a3d10f86e47",
		CreatedAt:  1_700_000_000_000,
		LastSeenAt: 0, LastIP: "",
	}
	id, err := s.AddPairDevice(in)
	if err != nil {
		t.Fatalf("AddPairDevice: %v", err)
	}
	if id <= 0 {
		t.Fatalf("AddPairDevice returned id %d, want a positive row id", id)
	}
	got, err := s.GetPairDeviceByHash(in.TokenHash)
	if err != nil {
		t.Fatalf("GetPairDeviceByHash: %v", err)
	}
	want := *in
	want.ID = id
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetPairDeviceByHash = %+v\nwant                %+v", *got, want)
	}
}

// Contract G2 `ListPairDevices`: "CreatedAt ascending; empty slice when none."
func TestContractFleetListPairDevicesOrder(t *testing.T) {
	s := openTest(t)
	empty, err := s.ListPairDevices()
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ListPairDevices empty = %#v, want empty non-nil", empty)
	}
	for _, d := range []*PairDevice{
		{Label: "c", TokenHash: "hash-c", CreatedAt: 300},
		{Label: "a", TokenHash: "hash-a", CreatedAt: 100},
		{Label: "b", TokenHash: "hash-b", CreatedAt: 200},
	} {
		if _, err := s.AddPairDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListPairDevices()
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, d := range got {
		labels = append(labels, d.Label)
	}
	if !reflect.DeepEqual(labels, []string{"a", "b", "c"}) {
		t.Errorf("ListPairDevices order = %v, want [a b c] by CreatedAt", labels)
	}
}

// Contract G2 `DeletePairDevice` + `DeleteAllPairDevices` ("Returns the count
// removed").
func TestContractFleetDeletePairDevices(t *testing.T) {
	s := openTest(t)
	var ids []int64
	for i, h := range []string{"h1", "h2", "h3"} {
		id, err := s.AddPairDevice(&PairDevice{Label: h, TokenHash: h, CreatedAt: int64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := s.DeletePairDevice(ids[1]); err != nil {
		t.Fatalf("DeletePairDevice: %v", err)
	}
	left, _ := s.ListPairDevices()
	if len(left) != 2 || left[0].ID != ids[0] || left[1].ID != ids[2] {
		t.Fatalf("after deleting the middle device: %+v", left)
	}
	n, err := s.DeleteAllPairDevices()
	if err != nil {
		t.Fatalf("DeleteAllPairDevices: %v", err)
	}
	if n != 2 {
		t.Errorf("DeleteAllPairDevices removed %d, want 2", n)
	}
	left, _ = s.ListPairDevices()
	if len(left) != 0 {
		t.Errorf("devices remain after DeleteAll: %+v", left)
	}
}

// Contract G2 `TouchPairDevice`: "Sets last_seen_at, last_ip."
func TestContractFleetTouchPairDevice(t *testing.T) {
	s := openTest(t)
	id, err := s.AddPairDevice(&PairDevice{Label: "phone", TailnetName: "iphone173", TokenHash: "hh", CreatedAt: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TouchPairDevice(id, 5_000, "100.101.102.103"); err != nil {
		t.Fatalf("TouchPairDevice: %v", err)
	}
	got, err := s.GetPairDeviceByHash("hh")
	if err != nil {
		t.Fatal(err)
	}
	want := PairDevice{ID: id, Label: "phone", TailnetName: "iphone173", TokenHash: "hh", CreatedAt: 10, LastSeenAt: 5_000, LastIP: "100.101.102.103"}
	if *got != want {
		t.Errorf("after touch = %+v, want %+v", *got, want)
	}
}
