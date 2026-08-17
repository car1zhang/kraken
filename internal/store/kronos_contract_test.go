package store

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
)

// kronos_contract_test.go — athena-logic's BLIND contract tests for the kronos
// build's Slice A (50-contract.md § "Slice A — store"): schema v24's new
// orch_inbox table, the five orch accessors over it, and ClearRecalled.
// (The imsg_sent table and its accessors were removed with the iMessage
// bridge — 50-contract.md amendments round 4 — and their tests with them.)
//
// Written from the contract text alone. internal/store/orch.go,
// internal/store/recall.go and internal/store/schema.go were never opened in
// any form — the shapes below come from the contract's own signature column,
// and the seeding of kg_recall goes through MarkRecalled, a PRE-EXISTING
// accessor already exercised by recall_contract_test.go.
//
// Dimension: LOGIC — the stated obligations, the happy paths, the invariants.
// Hostile inputs and boundary abuse are athena-edge's ground; where an
// obligation IS an input rule (AckOrchItems' all-or-nothing refusal, the empty
// kind) it is pinned here too, and the overlap is deliberate.
//
// Every package-level identifier is prefixed `contractKronos`: this package
// already carries bare `contract*` names (watch_contract_test.go) and
// `contractRecall*`/`contractSlack*` names, and a sibling author shares the
// test binary.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractKronosColumns reads a table's column names out of PRAGMA table_info,
// sorted, so a column-set assertion is one comparison. Going under the
// accessors is how "creates orch_inbox (…)" is observable at all: the frozen
// column list is a claim about the TABLE, and OrchItem's fields alone cannot
// tell a missing column from one the scan quietly defaults.
func contractKronosColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	sort.Strings(cols)
	return cols
}

// contractKronosAdd inserts one inbox row and fails the test if the insert
// itself did.
func contractKronosAdd(t *testing.T, s *Store, kind, slug, summary string, now int64) int64 {
	t.Helper()
	id, err := s.AddOrchItem(kind, slug, summary, now)
	if err != nil {
		t.Fatalf("AddOrchItem(%q, %q, …): %v", kind, slug, err)
	}
	if id == 0 {
		t.Fatalf("AddOrchItem(%q, %q, …) returned id 0 — the ack route addresses rows by id", kind, slug)
	}
	return id
}

// contractKronosIDs renders a row set as its ids in the order returned.
func contractKronosIDs(items []OrchItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// contractKronosRawOrch reads one row's three lifecycle fields straight out of
// SQL. The listings filter on exactly these columns, so reading a settled row
// back through them is impossible — an acked row is absent from both.
func contractKronosRawOrch(t *testing.T, s *Store, id int64) (delivered, acked int64, disposition string) {
	t.Helper()
	err := s.db.QueryRow(
		`SELECT COALESCE(delivered_at,0), COALESCE(acked_at,0), COALESCE(disposition,'') FROM orch_inbox WHERE id = ?`, id,
	).Scan(&delivered, &acked, &disposition)
	if err != nil {
		t.Fatalf("read back orch row %d: %v", id, err)
	}
	return delivered, acked, disposition
}

// contractKronosOrchTotal is the whole-table count, for the "nothing was
// written" halves: a refusal that inserted a row somewhere is still a write.
func contractKronosOrchTotal(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM orch_inbox`).Scan(&n); err != nil {
		t.Fatalf("count orch_inbox: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// schema v24
// ---------------------------------------------------------------------------

// TestContractKronosSchemaV24Lands pins the version FLOOR and both tables'
// frozen column sets. A floor rather than the equality the contract wrote:
// v25 (saved_artifacts) landed in a concurrent branch days after this file
// was authored, and an exact pin fails on every later migration while
// asserting nothing kronos needs — the v23 walk below already fails loudly
// when a new migration's table is missing from its list, which is the real
// extension signal (it just fired for v25).
func TestContractKronosSchemaV24Lands(t *testing.T) {
	if schemaVersion < 24 {
		t.Fatalf("schemaVersion = %d, want >= 24 (kronos' orch_inbox lands at 24)", schemaVersion)
	}
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("a freshly opened store is at user_version %d, want %d", version, schemaVersion)
	}

	wantInbox := []string{"acked_at", "delivered_at", "disposition", "id", "kind", "seen_at", "slug", "summary"}
	if got := contractKronosColumns(t, s, "orch_inbox"); fmt.Sprint(got) != fmt.Sprint(wantInbox) {
		t.Errorf("orch_inbox columns = %v, want %v", got, wantInbox)
	}
	// No state column, deliberately: the three timestamps ARE the lifecycle
	// (slack_inbox's shape), and a state column beside them is a second source
	// of truth that can disagree with them.
	for _, banned := range []string{"state", "status"} {
		for _, got := range contractKronosColumns(t, s, "orch_inbox") {
			if got == banned {
				t.Errorf("orch_inbox carries a %q column — the contract's lifecycle is the three timestamps alone", banned)
			}
		}
	}

}

// TestContractKronosV24MigratesFromV23 covers the append-only claim on a
// POPULATED database: a real v23 kraken.db (neither new table present, every
// earlier table full) migrates forward, gains both tables USABLE, and loses
// nothing — including the jobs row's rev and updated_at, which a migration
// touching the jobs table would move, and a kv row, which is where every
// watermark in the binary lives.
//
// Done inline rather than through downgradeFreshDB so this measures the
// v23→current walk on its own terms; downgradeFreshDB's own forward walk is
// covered by the tests that already call it, which the contract requires to
// pass over the new step unmodified.
func TestContractKronosV24MigratesFromV23(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	id, err := s.CreateJob(&Job{Slug: "pre-existing", Repo: "kraken", Body: "spec", State: "working"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SetKV("fixture_mark", "4242"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	if err := s.MarkRecalled("sess-pre", []int64{11, 12}, 1_700_000_000_000); err != nil {
		t.Fatalf("MarkRecalled seed: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	// Roll the shape back to a genuine v23: exactly the table v24 adds, gone,
	// and the version stamp lowered.
	for _, stmt := range []string{
		// v27 replaced jobs.goal_id with the job_goals join table, so
		// reversing it takes BOTH directions: the table goes, and the column
		// comes back — a genuine v23 database has it, and step 27 replays by
		// dropping it.
		`DROP TABLE IF EXISTS job_goals`,
		`ALTER TABLE jobs ADD COLUMN goal_id INTEGER REFERENCES goals(id) ON DELETE SET NULL`,
		// v26 adds board_bg (the board background) and v25 saved_artifacts
		// (the artifact library) — later migrations' tables in this walk's
		// terms; leftover, each fails its step's bare CREATE. Newest first.
		`DROP TABLE IF EXISTS board_bg`,
		`DROP TABLE IF EXISTS saved_artifacts`,
		`DROP TABLE IF EXISTS orch_inbox`,
		`PRAGMA user_version = 23`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("downgrade to v23 (%q): %v", stmt, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v23: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version after migrate = %d, want %d", version, schemaVersion)
	}

	// the table is USABLE, not merely present
	rowID := contractKronosAdd(t, s, "job-blocked", "post-migration", "a job went blocked", 1_755_000_000_000)
	if items, err := s.UndeliveredOrch(); err != nil || len(items) != 1 || items[0].ID != rowID {
		t.Fatalf("UndeliveredOrch after migration = %+v, err %v — want the one row just added", items, err)
	}

	// nothing else moved
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID after migrate: %v", err)
	}
	if after.Slug != before.Slug || after.Body != before.Body || after.State != before.State {
		t.Errorf("job row changed across the migration: %+v -> %+v", before, after)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Errorf("job rev/updated_at moved across the migration: %d/%d -> %d/%d",
			before.Rev, before.UpdatedAt, after.Rev, after.UpdatedAt)
	}
	if v, err := s.GetKV("fixture_mark"); err != nil || v != "4242" {
		t.Errorf("kv row lost across the migration: %q, err %v", v, err)
	}
	ids, err := s.RecalledEdgeIDs("sess-pre")
	if err != nil || !ids[11] || !ids[12] {
		t.Errorf("kg_recall rows lost across the migration: %v, err %v", ids, err)
	}

	// re-running migrate() on the same file must be a no-op
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate (idempotence): %v", err)
	}
	if items, err := s.UnackedOrch(); err != nil || len(items) != 1 {
		t.Fatalf("after re-migrate: %d rows, err %v — the migration is not idempotent", len(items), err)
	}
}

// ---------------------------------------------------------------------------
// AddOrchItem
// ---------------------------------------------------------------------------

// TestContractKronosAddOrchItemStampsSeenAtAndReturnsID is the insert's whole
// stated shape: the caller's `now` lands in seen_at (never a clock read of the
// store's own — the daemon stamps one time per tick and every row of that tick
// must agree), the three lifecycle fields start empty, and the returned id is
// what the ack route addresses.
func TestContractKronosAddOrchItemStampsSeenAtAndReturnsID(t *testing.T) {
	s := openTest(t)
	const now = int64(1_755_000_000_000)

	id := contractKronosAdd(t, s, "job-blocked", "csv-export", "csv-export is blocked on a spec question", now)

	items, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d rows, want 1", len(items))
	}
	got := items[0]
	if got.ID != id {
		t.Errorf("row id = %d, want the id AddOrchItem returned (%d)", got.ID, id)
	}
	if got.Kind != "job-blocked" || got.Slug != "csv-export" || got.Summary != "csv-export is blocked on a spec question" {
		t.Errorf("row = %+v, want the three fields round-tripped verbatim", got)
	}
	if got.SeenAt != now {
		t.Errorf("row seen_at = %d, want the caller's %d", got.SeenAt, now)
	}
	if got.DeliveredAt != 0 || got.AckedAt != 0 || got.Disposition != "" {
		t.Errorf("a freshly-inserted row is already delivered/acked: %+v", got)
	}
}

// TestContractKronosAddOrchItemAllowsAnEmptySlug: the store does not require
// a slug — a row may describe an event that belongs to no single job, and
// refusing a slugless row here would make any such event unrepresentable.
func TestContractKronosAddOrchItemAllowsAnEmptySlug(t *testing.T) {
	s := openTest(t)
	id := contractKronosAdd(t, s, "note", "", "carl: what's blocking the csv job?", 1_755_000_000_000)

	items, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("rows = %+v, want the one slugless row", items)
	}
	if items[0].Slug != "" {
		t.Errorf("slug = %q, want it stored empty rather than defaulted", items[0].Slug)
	}
	if items[0].Kind != "note" {
		t.Errorf("kind = %q, want note", items[0].Kind)
	}
}

// TestContractKronosAddOrchItemNeverDedupes: "dedup is the caller's". The
// daemon's in-memory Notified-key pattern is what makes a persisting condition
// enqueue once per streak; a store-side dedupe on (kind, slug) would ALSO
// collapse two genuinely separate streaks of the same condition, and nothing
// downstream could tell that had happened.
func TestContractKronosAddOrchItemNeverDedupes(t *testing.T) {
	s := openTest(t)
	first := contractKronosAdd(t, s, "job-blocked", "csv-export", "same summary", 1_755_000_000_000)
	second := contractKronosAdd(t, s, "job-blocked", "csv-export", "same summary", 1_755_000_000_000)

	if first == second {
		t.Fatalf("two identical AddOrchItem calls returned one id (%d) — the store deduped", first)
	}
	items, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d rows after two identical inserts, want 2: %+v", len(items), items)
	}
}

// TestContractKronosAddOrchItemRefusesAnEmptyKind pins the refusal and its
// "no write" half: kind is what tells kronos what it is looking at, and a
// kindless row is an escalation with no category anywhere.
func TestContractKronosAddOrchItemRefusesAnEmptyKind(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddOrchItem("", "csv-export", "a summary", 1_755_000_000_000); err == nil {
		t.Error("AddOrchItem with an empty kind = nil error, want a refusal")
	}
	if n := contractKronosOrchTotal(t, s); n != 0 {
		t.Errorf("orch_inbox holds %d rows after a refused insert, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// the two listings
// ---------------------------------------------------------------------------

// TestContractKronosListingSetsAndOrder pins both reads at once, because their
// difference is the point. UnackedOrch is kronos' worklist and includes rows
// whose BUMP failed — a row must never be unreachable because a tmux send
// didn't land. UndeliveredOrch is the delivery pass' worklist and is the
// strictly narrower set. Both are oldest-first by id, which is insertion order
// by construction and the order kronos should read a batch in.
func TestContractKronosListingSetsAndOrder(t *testing.T) {
	s := openTest(t)
	one := contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)
	two := contractKronosAdd(t, s, "cell-gone", "b", "two", 1_755_000_000_002)
	three := contractKronosAdd(t, s, "pr-merged", "c", "three", 1_755_000_000_003)
	four := contractKronosAdd(t, s, "imsg", "", "four", 1_755_000_000_004)

	all, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if want := fmt.Sprint([]int64{one, two, three, four}); fmt.Sprint(contractKronosIDs(all)) != want {
		t.Fatalf("UnackedOrch ids = %v, want %s (oldest first, by id)", contractKronosIDs(all), want)
	}

	// row two delivered but not acked: still unacked, no longer undelivered
	if err := s.MarkOrchDelivered([]int64{two}, 1_755_000_005_000); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	// row three acked: out of both sets
	if err := s.AckOrchItems([]int64{three}, "escalated", 1_755_000_006_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}

	unacked, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch after stamps: %v", err)
	}
	if want := fmt.Sprint([]int64{one, two, four}); fmt.Sprint(contractKronosIDs(unacked)) != want {
		t.Fatalf("UnackedOrch = %v, want %s — a delivered-but-unacked row is still kronos' work",
			contractKronosIDs(unacked), want)
	}

	undelivered, err := s.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if want := fmt.Sprint([]int64{one, four}); fmt.Sprint(contractKronosIDs(undelivered)) != want {
		t.Fatalf("UndeliveredOrch = %v, want %s — delivered_at = 0 AND acked_at = 0",
			contractKronosIDs(undelivered), want)
	}
}

// TestContractKronosEmptyListingsOnAFreshStore: "nothing waiting" is a normal
// state for both reads and must never be an error — the delivery pass runs on
// every tick and an error here would be a log line per tick forever.
func TestContractKronosEmptyListingsOnAFreshStore(t *testing.T) {
	s := openTest(t)
	undelivered, err := s.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch on a fresh store: %v", err)
	}
	if len(undelivered) != 0 {
		t.Errorf("UndeliveredOrch = %+v, want empty", undelivered)
	}
	unacked, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch on a fresh store: %v", err)
	}
	if len(unacked) != 0 {
		t.Errorf("UnackedOrch = %+v, want empty", unacked)
	}
}

// ---------------------------------------------------------------------------
// MarkOrchDelivered
// ---------------------------------------------------------------------------

// TestContractKronosMarkOrchDeliveredStampsExactlyTheNamedRows: delivery is
// send-then-watermark bookkeeping about a bump that has ALREADY happened, so
// it stamps the named rows and touches nothing else — in particular not the
// ack fields, which belong to kronos.
func TestContractKronosMarkOrchDeliveredStampsExactlyTheNamedRows(t *testing.T) {
	s := openTest(t)
	one := contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)
	two := contractKronosAdd(t, s, "cell-gone", "b", "two", 1_755_000_000_002)

	const at = int64(1_755_000_005_000)
	if err := s.MarkOrchDelivered([]int64{one}, at); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}

	gotDelivered, gotAcked, gotDisp := contractKronosRawOrch(t, s, one)
	if gotDelivered != at {
		t.Errorf("row one delivered_at = %d, want %d", gotDelivered, at)
	}
	if gotAcked != 0 || gotDisp != "" {
		t.Errorf("delivery stamped the ack fields too: acked_at %d, disposition %q", gotAcked, gotDisp)
	}
	if d, _, _ := contractKronosRawOrch(t, s, two); d != 0 {
		t.Errorf("row two delivered_at = %d, want it untouched at 0 — it stays on the retry set", d)
	}
}

// TestContractKronosMarkOrchDeliveredIsRepeatable pins the contract's stated
// asymmetry with the ack: re-stamping an already-delivered row is NOT an error.
// A bump that landed but whose stamp failed is re-bumped on the next tick, and
// refusing the second stamp would leave that row bumping forever.
func TestContractKronosMarkOrchDeliveredIsRepeatable(t *testing.T) {
	s := openTest(t)
	id := contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)

	if err := s.MarkOrchDelivered([]int64{id}, 1_755_000_005_000); err != nil {
		t.Fatalf("first MarkOrchDelivered: %v", err)
	}
	if err := s.MarkOrchDelivered([]int64{id}, 1_755_000_009_000); err != nil {
		t.Fatalf("re-delivering an already-delivered row = %v, want nil (a re-bump after a failed stamp is legal)", err)
	}
	undelivered, err := s.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(undelivered) != 0 {
		t.Errorf("UndeliveredOrch = %+v, want empty after the row was stamped", undelivered)
	}
}

// TestContractKronosMarkOrchDeliveredRefusesAnUnknownID: the daemon names rows
// it just read out of UndeliveredOrch, so an id with no row behind it means the
// pass is stamping something other than what it bumped.
func TestContractKronosMarkOrchDeliveredRefusesAnUnknownID(t *testing.T) {
	s := openTest(t)
	contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)
	if err := s.MarkOrchDelivered([]int64{9_999_999}, 1_755_000_005_000); err == nil {
		t.Error("MarkOrchDelivered with an unknown id = nil error, want a refusal")
	}
}

// ---------------------------------------------------------------------------
// AckOrchItems
// ---------------------------------------------------------------------------

// TestContractKronosAckOrchItemsStampsBothFields: one call settles a whole
// batch, writing acked_at AND the disposition on every named row and touching
// nothing else.
func TestContractKronosAckOrchItemsStampsBothFields(t *testing.T) {
	s := openTest(t)
	one := contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)
	two := contractKronosAdd(t, s, "cell-gone", "b", "two", 1_755_000_000_002)
	three := contractKronosAdd(t, s, "imsg", "", "three", 1_755_000_000_003)

	const at = int64(1_755_000_009_000)
	if err := s.AckOrchItems([]int64{one, two}, "escalated:csv-export blocked", at); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}

	remaining, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch after ack: %v", err)
	}
	if want := fmt.Sprint([]int64{three}); fmt.Sprint(contractKronosIDs(remaining)) != want {
		t.Fatalf("UnackedOrch after ack = %v, want %s", contractKronosIDs(remaining), want)
	}
	if remaining[0].AckedAt != 0 || remaining[0].Disposition != "" {
		t.Errorf("the un-acked row was touched: %+v", remaining[0])
	}
	for _, id := range []int64{one, two} {
		_, gotAcked, gotDisp := contractKronosRawOrch(t, s, id)
		if gotAcked != at {
			t.Errorf("row %d acked_at = %d, want %d", id, gotAcked, at)
		}
		if gotDisp != "escalated:csv-export blocked" {
			t.Errorf("row %d disposition = %q, want the caller's", id, gotDisp)
		}
	}
}

// TestContractKronosAckOrchItemsIsAllOrNothing is the strict verb's whole
// point, in both of its stated forms. Kronos is ASSERTING it acted on specific
// rows; a wrong id means it acted on something else, and under a lenient rule
// the mistake is invisible in both directions at once — the row it meant stays
// unacked forever while the row it named is retired unread.
func TestContractKronosAckOrchItemsIsAllOrNothing(t *testing.T) {
	s := openTest(t)
	one := contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)
	two := contractKronosAdd(t, s, "cell-gone", "b", "two", 1_755_000_000_002)

	// an unknown id in the batch
	if err := s.AckOrchItems([]int64{one, 9_999_999}, "escalated", 1_755_000_009_000); err == nil {
		t.Error("AckOrchItems with an unknown id = nil error, want a refusal")
	}
	for _, id := range []int64{one, two} {
		if _, acked, disp := contractKronosRawOrch(t, s, id); acked != 0 || disp != "" {
			t.Errorf("row %d was acked (%d/%q) by a REFUSED batch — nothing may be written", id, acked, disp)
		}
	}

	// an already-acked id in the batch
	if err := s.AckOrchItems([]int64{one}, "escalated", 1_755_000_009_000); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if err := s.AckOrchItems([]int64{one, two}, "ignored", 1_755_000_010_000); err == nil {
		t.Error("AckOrchItems naming an already-acked row = nil error, want a refusal")
	}
	if _, acked, disp := contractKronosRawOrch(t, s, one); acked != 1_755_000_009_000 || disp != "escalated" {
		t.Errorf("the already-acked row was rewritten by the refused batch: %d/%q", acked, disp)
	}
	if _, acked, disp := contractKronosRawOrch(t, s, two); acked != 0 || disp != "" {
		t.Errorf("row two was acked (%d/%q) by a refused batch", acked, disp)
	}
}

// TestContractKronosAckOrchItemsRefusesEmptyInput: an ack with no ids, or with
// no disposition, is a caller bug. Answering either with success would tell
// kronos its batch was recorded when nothing was — and the disposition is the
// ONLY durable record anywhere of why a row left the inbox.
func TestContractKronosAckOrchItemsRefusesEmptyInput(t *testing.T) {
	s := openTest(t)
	id := contractKronosAdd(t, s, "job-blocked", "a", "one", 1_755_000_000_001)

	if err := s.AckOrchItems(nil, "escalated", 1_755_000_009_000); err == nil {
		t.Error("AckOrchItems with nil ids = nil error, want a refusal")
	}
	if err := s.AckOrchItems([]int64{}, "escalated", 1_755_000_009_000); err == nil {
		t.Error("AckOrchItems with an empty id slice = nil error, want a refusal")
	}
	if err := s.AckOrchItems([]int64{id}, "", 1_755_000_009_000); err == nil {
		t.Error("AckOrchItems with an empty disposition = nil error, want a refusal")
	}
	if _, acked, disp := contractKronosRawOrch(t, s, id); acked != 0 || disp != "" {
		t.Errorf("a refused ack wrote to the row anyway: %d/%q", acked, disp)
	}
}

// ---------------------------------------------------------------------------
// ClearRecalled
// ---------------------------------------------------------------------------
// (Two imsg_sent echo-guard tests sat here and were deliberately obsoleted
// with the iMessage bridge's removal at the manual gate.)

// TestContractKronosClearRecalledDeletesOnlyThatSession is the re-arm's whole
// mechanism: compaction dropped the delivered facts out of kronos' context, so
// re-delivery is correct — but ONLY for that session. Every cell on the machine
// shares kg_recall, and clearing another session's marks would re-inject facts
// into conversations that still hold them.
func TestContractKronosClearRecalledDeletesOnlyThatSession(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("kronos-session", []int64{1, 2, 3}, 1_000); err != nil {
		t.Fatalf("MarkRecalled kronos: %v", err)
	}
	if err := s.MarkRecalled("a-cell-session", []int64{2, 4}, 1_000); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}

	n, err := s.ClearRecalled("kronos-session")
	if err != nil {
		t.Fatalf("ClearRecalled: %v", err)
	}
	if n != 3 {
		t.Errorf("ClearRecalled = %d, want 3 (the count deleted)", n)
	}

	cleared, err := s.RecalledEdgeIDs("kronos-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs after clear: %v", err)
	}
	if len(cleared) != 0 {
		t.Errorf("kronos' recalled set = %v, want empty after the re-arm", cleared)
	}
	other, err := s.RecalledEdgeIDs("a-cell-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs other: %v", err)
	}
	if !other[2] || !other[4] || len(other) != 2 {
		t.Errorf("another session's recalled set = %v, want {2,4} untouched", other)
	}

	// the same facts really can be delivered again
	if err := s.MarkRecalled("kronos-session", []int64{1}, 2_000); err != nil {
		t.Fatalf("re-mark after clear: %v", err)
	}
	again, err := s.RecalledEdgeIDs("kronos-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs after re-mark: %v", err)
	}
	if !again[1] {
		t.Errorf("recalled set after re-mark = %v, want edge 1 back", again)
	}
}

// TestContractKronosClearRecalledUnknownSessionIsZero: a session with no marks
// is not an error — the PostCompact hook fires on every compaction, including
// one where recall had never delivered anything.
func TestContractKronosClearRecalledUnknownSessionIsZero(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("someone-else", []int64{7}, 1_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	n, err := s.ClearRecalled("never-seen-session")
	if err != nil {
		t.Fatalf("ClearRecalled on a session with no rows: %v", err)
	}
	if n != 0 {
		t.Errorf("ClearRecalled = %d, want 0", n)
	}
	if ids, _ := s.RecalledEdgeIDs("someone-else"); !ids[7] {
		t.Errorf("another session's row was deleted by a no-match clear: %v", ids)
	}
}

// TestContractKronosClearRecalledRefusesABlankSession pins the refusal AND its
// "deletes nothing" half. A blank session id matches no row under the stated
// filter, but the danger is an implementation that treats blank as "no WHERE
// clause" — which would wipe every cell's recall marks on the machine from one
// malformed hook payload.
func TestContractKronosClearRecalledRefusesABlankSession(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("kronos-session", []int64{1, 2}, 1_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	if err := s.MarkRecalled("a-cell-session", []int64{3}, 1_000); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}

	n, err := s.ClearRecalled("")
	if err == nil {
		t.Error("ClearRecalled(\"\") = nil error, want a refusal")
	}
	if n != 0 {
		t.Errorf("ClearRecalled(\"\") = %d, want 0 deleted", n)
	}
	if ids, _ := s.RecalledEdgeIDs("kronos-session"); len(ids) != 2 {
		t.Errorf("kronos' rows = %v, want both still present after a refusal", ids)
	}
	if ids, _ := s.RecalledEdgeIDs("a-cell-session"); len(ids) != 1 {
		t.Errorf("the cell's rows = %v, want it untouched after a refusal", ids)
	}
}
