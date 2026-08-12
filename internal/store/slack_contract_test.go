package store

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
)

// slack_contract_test.go — blind contract tests for slice 2 of the Slack
// intake bot (50-contract.md § "Slice 2 — internal/store"): schema v17's two
// tables and the six accessors over them.
//
// Dimension: stated invariants and happy paths. The refusal paths
// (AckSlackMessages on an unknown or already-acked id, an empty id list, a
// CHECK violation) belong to athena-edge.
//
// Every package-level identifier is `contract`-prefixed AND slack-scoped:
// watch_contract_test.go already owns the bare `contract*` names in this
// package.

// contractSlackMsg builds one inbox row with the fields a poller would have
// filled, leaving the three lifecycle stamps at their "just seen" values.
func contractSlackMsg(channel, ts, source, text string) SlackInboxMsg {
	return SlackInboxMsg{
		ChannelID:   channel,
		ChannelName: "",
		TS:          ts,
		ThreadTS:    "",
		AuthorID:    "U0BOB",
		AuthorName:  "bobby",
		Source:      source,
		Text:        text,
		SeenAt:      1755000000000,
	}
}

// contractSlackSeed commits msgs (and no marks) and fails the test unless
// every one of them was inserted.
func contractSlackSeed(t *testing.T, s *Store, msgs ...SlackInboxMsg) {
	t.Helper()
	n, err := s.AddSlackBatch(msgs, nil)
	if err != nil {
		t.Fatalf("AddSlackBatch seed: %v", err)
	}
	if n != len(msgs) {
		t.Fatalf("AddSlackBatch seed inserted %d rows, want %d", n, len(msgs))
	}
}

// contractSlackIDs lists a row set's ids in the order returned, so an
// ordering assertion reads as one comparison.
func contractSlackIDs(msgs []SlackInboxMsg) []int64 {
	out := make([]int64, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

// contractSlackColumns reads a table's column names out of PRAGMA
// table_info, sorted.
func contractSlackColumns(t *testing.T, s *Store, table string) []string {
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
	sort.Strings(cols)
	return cols
}

// contractSlackHasUniqueIndex reports whether table carries a UNIQUE index
// over exactly the named columns, in order.
func contractSlackHasUniqueIndex(t *testing.T, s *Store, table string, want ...string) bool {
	t.Helper()
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA index_list(%s)`, table))
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	type idx struct {
		name   string
		unique bool
	}
	var idxs []idx
	for rows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		idxs = append(idxs, idx{name: name, unique: unique == 1})
	}
	rows.Close()

	for _, ix := range idxs {
		if !ix.unique {
			continue
		}
		irows, err := s.db.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, ix.name))
		if err != nil {
			t.Fatalf("index_info(%s): %v", ix.name, err)
		}
		var cols []string
		for irows.Next() {
			var seqno, cid int
			var name any
			if err := irows.Scan(&seqno, &cid, &name); err != nil {
				irows.Close()
				t.Fatalf("scan index_info(%s): %v", ix.name, err)
			}
			if s, ok := name.(string); ok {
				cols = append(cols, s)
			}
		}
		irows.Close()
		if len(cols) != len(want) {
			continue
		}
		match := true
		for i := range cols {
			if cols[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// --- schema ---

// TestContractSlackSchemaV17Lands: a fresh database carries both tables with
// exactly the frozen column sets and the UNIQUE(channel_id, ts) that makes
// the whole at-least-once pipeline idempotent.
func TestContractSlackSchemaV17Lands(t *testing.T) {
	s := openTest(t)

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version < 17 {
		t.Fatalf("user_version = %d, want at least 17 (the Slack tables' migration)", version)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want schemaVersion %d", version, schemaVersion)
	}

	wantInbox := []string{
		"acked_at", "author_id", "author_name", "channel_id", "channel_name",
		"delivered_at", "disposition", "id", "seen_at", "source", "text",
		"thread_ts", "ts",
	}
	if got := contractSlackColumns(t, s, "slack_inbox"); fmt.Sprint(got) != fmt.Sprint(wantInbox) {
		t.Errorf("slack_inbox columns = %v, want %v", got, wantInbox)
	}
	wantMarks := []string{"key", "ts"}
	if got := contractSlackColumns(t, s, "slack_marks"); fmt.Sprint(got) != fmt.Sprint(wantMarks) {
		t.Errorf("slack_marks columns = %v, want %v", got, wantMarks)
	}
	if !contractSlackHasUniqueIndex(t, s, "slack_inbox", "channel_id", "ts") {
		t.Error("slack_inbox has no UNIQUE(channel_id, ts) — a re-poll would duplicate every message")
	}
}

// TestContractSlackSourceAcceptsTheThreeKinds: the CHECK names exactly the
// three ways a message reaches the inbox, and all three must be insertable.
func TestContractSlackSourceAcceptsTheThreeKinds(t *testing.T) {
	s := openTest(t)
	contractSlackSeed(t, s,
		contractSlackMsg("D111", "1755000001.000100", "dm", "a dm"),
		contractSlackMsg("G222", "1755000002.000100", "mpim", "a group dm"),
		contractSlackMsg("C0ENG", "1755000003.000100", "mention", "<@U0CARL> hi"),
	)
	got, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want all three sources stored", len(got))
	}
	seen := map[string]bool{}
	for _, m := range got {
		seen[m.Source] = true
	}
	for _, want := range []string{"dm", "mpim", "mention"} {
		if !seen[want] {
			t.Errorf("source %q did not survive the round trip: %+v", want, got)
		}
	}
}

// TestContractSlackV17MigratesFromV16 covers the append-only claim on a
// POPULATED database: a real v16 kraken.db (the two tables absent, every
// earlier table full) migrates forward, gains both tables usable, and loses
// nothing — including the jobs row's rev and updated_at, which a migration
// touching the jobs table would move.
func TestContractSlackV17MigratesFromV16(t *testing.T) {
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
	if err := s.SetKV("slack_last_poll", "1755000000000"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	// Roll the shape back to a genuine v16: every table added at or after
	// v17 gone, the version stamp lowered. Done inline rather than through
	// downgradeFreshDB so this test measures the v16→current walk on its own
	// terms — which is also why the list must name LATER migrations' tables
	// too (linear_issues, v18): migrate() from 16 replays every step above
	// it, and a leftover table fails that step's bare CREATE.
	// v20 is the exception to "every table added at or after v17 gone": it
	// DROPS rather than adds, so replaying it needs its targets PRESENT —
	// jobs.ready and the prereqs table, both of which a genuine v16 database
	// had and this fresh one no longer does.
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS slack_inbox`,
		`DROP TABLE IF EXISTS slack_marks`,
		`DROP TABLE IF EXISTS linear_issues`,
		`DROP TABLE IF EXISTS kg_recall`,
		`ALTER TABLE jobs ADD COLUMN ready INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE prereqs (
			job_id      INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
			prereq_slug TEXT NOT NULL,
			gated       INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (job_id, prereq_slug)
		)`,
		`PRAGMA user_version = 16`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("downgrade to v16 (%q): %v", stmt, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v16: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version after migrate = %d, want %d", version, schemaVersion)
	}

	// both tables are usable, not merely present
	contractSlackSeed(t, s, contractSlackMsg("D111", "1755000001.000100", "dm", "post-migration"))
	if _, err := s.AddSlackBatch(nil, map[string]string{"dm:D111": "1755000001.000100"}); err != nil {
		t.Fatalf("AddSlackBatch marks after migration: %v", err)
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
	if v, err := s.GetKV("slack_last_poll"); err != nil || v != "1755000000000" {
		t.Errorf("kv row lost across the migration: %q, err %v", v, err)
	}

	// re-opening the same file re-runs migrate() and must be a no-op
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate (idempotence): %v", err)
	}
	rows, err := s.UnackedSlackInbox()
	if err != nil || len(rows) != 1 {
		t.Fatalf("after re-migrate: %d rows, err %v — the migration is not idempotent", len(rows), err)
	}
}

// --- AddSlackBatch ---

// TestContractAddSlackBatchCommitsRowsAndMarks: one call, one transaction,
// rows and their watermarks landing together — which is what makes "marks
// advance only with the rows they cover" enforceable at all.
func TestContractAddSlackBatchCommitsRowsAndMarks(t *testing.T) {
	s := openTest(t)

	msgs := []SlackInboxMsg{
		contractSlackMsg("D111", "1755000001.000100", "dm", "first"),
		contractSlackMsg("D111", "1755000002.000100", "dm", "second"),
		contractSlackMsg("C0ENG", "1755000003.000100", "mention", "<@U0CARL> third"),
	}
	marks := map[string]string{
		"dm:D111":  "1755000002.000100",
		"mentions": "1755000003.000100",
	}
	n, err := s.AddSlackBatch(msgs, marks)
	if err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	if n != 3 {
		t.Fatalf("AddSlackBatch = %d, want 3 inserted", n)
	}

	got, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	first := got[0]
	if first.ChannelID != "D111" || first.TS != "1755000001.000100" || first.Text != "first" {
		t.Errorf("row 0 = %+v", first)
	}
	if first.AuthorID != "U0BOB" || first.AuthorName != "bobby" || first.Source != "dm" {
		t.Errorf("row 0 author/source = %+v", first)
	}
	if first.SeenAt != 1755000000000 {
		t.Errorf("row 0 seen_at = %d, want the caller's stamp", first.SeenAt)
	}
	if first.DeliveredAt != 0 || first.AckedAt != 0 || first.Disposition != "" {
		t.Errorf("a freshly-committed row is already delivered/acked: %+v", first)
	}
	if first.ID == 0 {
		t.Error("row 0 has no id — the ack route addresses rows by id")
	}

	gotMarks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if len(gotMarks) != 2 || gotMarks["dm:D111"] != "1755000002.000100" || gotMarks["mentions"] != "1755000003.000100" {
		t.Fatalf("marks = %v, want both keys at their batch values", gotMarks)
	}
}

// TestContractAddSlackBatchIsIdempotent is the at-least-once guarantee's
// other half: a poll that re-sees messages it already committed (an
// exclusive `oldest` still returns the boundary message on some paths, and a
// crashed pass simply repeats) inserts nothing new, reports 0, and leaves
// exactly one row per (channel, ts) — while the marks re-land at the same
// values rather than erroring.
func TestContractAddSlackBatchIsIdempotent(t *testing.T) {
	s := openTest(t)

	msgs := []SlackInboxMsg{
		contractSlackMsg("D111", "1755000001.000100", "dm", "first"),
		contractSlackMsg("D111", "1755000002.000100", "dm", "second"),
	}
	marks := map[string]string{"dm:D111": "1755000002.000100"}

	n, err := s.AddSlackBatch(msgs, marks)
	if err != nil || n != 2 {
		t.Fatalf("first AddSlackBatch = %d, err %v", n, err)
	}
	firstPass, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}

	n, err = s.AddSlackBatch(msgs, marks)
	if err != nil {
		t.Fatalf("second AddSlackBatch: %v — a duplicate (channel_id, ts) is skipped, never an error", err)
	}
	if n != 0 {
		t.Fatalf("second AddSlackBatch = %d, want 0 newly inserted", n)
	}
	secondPass, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after re-run: %v", err)
	}
	if fmt.Sprint(contractSlackIDs(firstPass)) != fmt.Sprint(contractSlackIDs(secondPass)) {
		t.Fatalf("row ids changed across an identical re-run: %v -> %v",
			contractSlackIDs(firstPass), contractSlackIDs(secondPass))
	}
	if len(secondPass) != 2 {
		t.Fatalf("got %d rows after two identical batches, want 2", len(secondPass))
	}

	// a batch mixing one known and one new message inserts only the new one
	n, err = s.AddSlackBatch([]SlackInboxMsg{
		contractSlackMsg("D111", "1755000002.000100", "dm", "second"),
		contractSlackMsg("D111", "1755000003.000100", "dm", "third"),
	}, map[string]string{"dm:D111": "1755000003.000100"})
	if err != nil {
		t.Fatalf("overlapping AddSlackBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("overlapping AddSlackBatch = %d, want 1 (only the unseen message)", n)
	}
	gotMarks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if gotMarks["dm:D111"] != "1755000003.000100" {
		t.Fatalf("mark = %q, want it advanced to the batch's newest ts", gotMarks["dm:D111"])
	}
}

// TestContractAddSlackBatchSameTSDifferentChannels: the uniqueness is on the
// PAIR. Two conversations can carry the same ts, and collapsing them would
// silently drop one person's message.
func TestContractAddSlackBatchSameTSDifferentChannels(t *testing.T) {
	s := openTest(t)
	n, err := s.AddSlackBatch([]SlackInboxMsg{
		contractSlackMsg("D111", "1755000001.000100", "dm", "one"),
		contractSlackMsg("D222", "1755000001.000100", "dm", "two"),
	}, nil)
	if err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	if n != 2 {
		t.Fatalf("AddSlackBatch = %d, want 2 — the unique key is (channel_id, ts), not ts alone", n)
	}
}

// TestContractAddSlackBatchNeverTouchesJobs: the inbox is a side channel.
// Nothing about committing a message may bump a job's rev (which would stale
// an open editor save) or updated_at (which is what every card renders as
// its age).
func TestContractAddSlackBatchNeverTouchesJobs(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "untouched", Repo: "kraken", Body: "spec"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	contractSlackSeed(t, s, contractSlackMsg("D111", "1755000001.000100", "dm", "hi"))
	if _, err := s.AddSlackBatch(nil, map[string]string{"dm:D111": "1755000001.000100"}); err != nil {
		t.Fatalf("AddSlackBatch marks: %v", err)
	}
	rows, err := s.UnackedSlackInbox()
	if err != nil || len(rows) != 1 {
		t.Fatalf("UnackedSlackInbox = %d rows, err %v", len(rows), err)
	}
	if err := s.MarkSlackDelivered([]int64{rows[0].ID}, 1755000005000); err != nil {
		t.Fatalf("MarkSlackDelivered: %v", err)
	}
	if err := s.AckSlackMessages([]int64{rows[0].ID}, "ignored", 1755000006000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}

	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID after slack writes: %v", err)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("a slack write moved a job's rev/updated_at: %d/%d -> %d/%d",
			before.Rev, before.UpdatedAt, after.Rev, after.UpdatedAt)
	}
}

// TestContractAddSlackBatchMarksOnly: advancing a watermark with no new rows
// is a legal batch (a sweep that found nothing still learned how far it
// read).
func TestContractAddSlackBatchMarksOnly(t *testing.T) {
	s := openTest(t)
	n, err := s.AddSlackBatch(nil, map[string]string{"thread:D111:1755000001.000100": "1755000009.000900"})
	if err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	if n != 0 {
		t.Fatalf("AddSlackBatch = %d, want 0 rows inserted", n)
	}
	marks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if marks["thread:D111:1755000001.000100"] != "1755000009.000900" {
		t.Fatalf("marks = %v, want the thread key at its new ts", marks)
	}
}

// TestContractSlackMarksEmptyMapWhenNone: no marks is an empty map, never a
// nil the caller has to guard before indexing.
func TestContractSlackMarksEmptyMapWhenNone(t *testing.T) {
	s := openTest(t)
	marks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if marks == nil {
		t.Fatal("SlackMarks returned nil — an empty map is the stated shape")
	}
	if len(marks) != 0 {
		t.Fatalf("SlackMarks = %v, want empty", marks)
	}
}

// --- lifecycle ---

// TestContractUnackedAndUndeliveredSetsAndOrder pins the two read queries
// the delivery pass and the inbox route depend on: both ordered by id
// ascending (arrival order, which is what makes iris read a conversation in
// sequence), with undelivered the strictly narrower set.
func TestContractUnackedAndUndeliveredSetsAndOrder(t *testing.T) {
	s := openTest(t)
	contractSlackSeed(t, s,
		contractSlackMsg("D111", "1755000001.000100", "dm", "one"),
		contractSlackMsg("D111", "1755000002.000100", "dm", "two"),
		contractSlackMsg("D111", "1755000003.000100", "dm", "three"),
		contractSlackMsg("D111", "1755000004.000100", "dm", "four"),
	)
	all, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d rows, want 4", len(all))
	}
	ids := contractSlackIDs(all)
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("UnackedSlackInbox ids = %v, want ascending", ids)
		}
	}

	// row 2 delivered but not acked: still unacked, no longer undelivered
	if err := s.MarkSlackDelivered([]int64{ids[1]}, 1755000005000); err != nil {
		t.Fatalf("MarkSlackDelivered: %v", err)
	}
	// row 3 acked: out of both sets
	if err := s.AckSlackMessages([]int64{ids[2]}, "ignored", 1755000006000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}

	unacked, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after stamps: %v", err)
	}
	if fmt.Sprint(contractSlackIDs(unacked)) != fmt.Sprint([]int64{ids[0], ids[1], ids[3]}) {
		t.Fatalf("unacked = %v, want the three rows with acked_at == 0 in id order", contractSlackIDs(unacked))
	}

	undelivered, err := s.UndeliveredSlackInbox()
	if err != nil {
		t.Fatalf("UndeliveredSlackInbox: %v", err)
	}
	if fmt.Sprint(contractSlackIDs(undelivered)) != fmt.Sprint([]int64{ids[0], ids[3]}) {
		t.Fatalf("undelivered = %v, want only the never-delivered, never-acked rows in id order — an acked row is settled even if it was never bumped",
			contractSlackIDs(undelivered))
	}
}

// TestContractMarkSlackDeliveredStampsOnlyTheNamedRows: delivery is
// best-effort bookkeeping, so an unknown id rides along silently, while
// every row NOT named keeps delivered_at at 0 and stays in the retry set.
func TestContractMarkSlackDeliveredStampsOnlyTheNamedRows(t *testing.T) {
	s := openTest(t)
	contractSlackSeed(t, s,
		contractSlackMsg("D111", "1755000001.000100", "dm", "one"),
		contractSlackMsg("D111", "1755000002.000100", "dm", "two"),
	)
	rows, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	ids := contractSlackIDs(rows)

	const at = int64(1755000005000)
	if err := s.MarkSlackDelivered([]int64{ids[0], 9999}, at); err != nil {
		t.Fatalf("MarkSlackDelivered with an unknown id alongside a real one: %v — delivery bookkeeping is best-effort", err)
	}

	after, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after stamp: %v", err)
	}
	if after[0].DeliveredAt != at {
		t.Errorf("row 0 delivered_at = %d, want %d", after[0].DeliveredAt, at)
	}
	if after[1].DeliveredAt != 0 {
		t.Errorf("row 1 delivered_at = %d, want it untouched at 0", after[1].DeliveredAt)
	}
	if after[0].AckedAt != 0 || after[0].Disposition != "" {
		t.Errorf("delivery stamped the ack fields too: %+v", after[0])
	}
}

// TestContractAckSlackMessagesStampsBothFields is the disposition record
// iris writes for every row it handles: one call may settle a whole batch,
// and it must touch nothing else.
func TestContractAckSlackMessagesStampsBothFields(t *testing.T) {
	s := openTest(t)
	contractSlackSeed(t, s,
		contractSlackMsg("D111", "1755000001.000100", "dm", "one"),
		contractSlackMsg("D111", "1755000002.000100", "dm", "two"),
		contractSlackMsg("D111", "1755000003.000100", "dm", "three"),
	)
	rows, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	ids := contractSlackIDs(rows)

	const at = int64(1755000009000)
	if err := s.AckSlackMessages([]int64{ids[0], ids[1]}, "drafted:csv-export-timeout", at); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}

	remaining, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after ack: %v", err)
	}
	if fmt.Sprint(contractSlackIDs(remaining)) != fmt.Sprint([]int64{ids[2]}) {
		t.Fatalf("unacked after ack = %v, want only the un-acked row", contractSlackIDs(remaining))
	}
	if remaining[0].AckedAt != 0 || remaining[0].Disposition != "" {
		t.Errorf("the un-acked row was touched: %+v", remaining[0])
	}

	total, undelivered, unacked, err := s.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts: %v", err)
	}
	if total != 3 || unacked != 1 {
		t.Fatalf("counts = total %d / unacked %d, want 3 / 1", total, unacked)
	}
	if undelivered != 1 {
		t.Fatalf("undelivered = %d, want 1 — an acked row is no longer pending delivery", undelivered)
	}

	// the acked rows really carry both fields (read back through the counts'
	// sibling query rather than trusting the write)
	var gotAt int64
	var gotDisp string
	if err := s.db.QueryRow(`SELECT acked_at, disposition FROM slack_inbox WHERE id = ?`, ids[0]).Scan(&gotAt, &gotDisp); err != nil {
		t.Fatalf("read back acked row: %v", err)
	}
	if gotAt != at || gotDisp != "drafted:csv-export-timeout" {
		t.Fatalf("acked row = acked_at %d / disposition %q, want %d / %q", gotAt, gotDisp, at, "drafted:csv-export-timeout")
	}
}

// TestContractSlackInboxCountsThreeStages: the numbers `kraken slack status`
// renders. undelivered is the pending-delivery set (never delivered AND
// never acked), unacked is everything still awaiting iris' verdict.
func TestContractSlackInboxCountsThreeStages(t *testing.T) {
	s := openTest(t)

	total, undelivered, unacked, err := s.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts on an empty inbox: %v", err)
	}
	if total != 0 || undelivered != 0 || unacked != 0 {
		t.Fatalf("empty inbox counts = %d/%d/%d, want zeros", total, undelivered, unacked)
	}

	contractSlackSeed(t, s,
		contractSlackMsg("D111", "1755000001.000100", "dm", "one"),
		contractSlackMsg("D111", "1755000002.000100", "dm", "two"),
		contractSlackMsg("D111", "1755000003.000100", "dm", "three"),
	)
	rows, _ := s.UnackedSlackInbox()
	ids := contractSlackIDs(rows)

	total, undelivered, unacked, err = s.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts: %v", err)
	}
	if total != 3 || undelivered != 3 || unacked != 3 {
		t.Fatalf("counts after seed = %d/%d/%d, want 3/3/3", total, undelivered, unacked)
	}

	if err := s.MarkSlackDelivered([]int64{ids[0]}, 1755000005000); err != nil {
		t.Fatalf("MarkSlackDelivered: %v", err)
	}
	if err := s.AckSlackMessages([]int64{ids[1]}, "ignored", 1755000006000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}
	total, undelivered, unacked, err = s.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts after stamps: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 — rows are kept forever, never deleted", total)
	}
	if undelivered != 1 {
		t.Errorf("undelivered = %d, want 1 (one delivered, one acked, one still pending)", undelivered)
	}
	if unacked != 2 {
		t.Errorf("unacked = %d, want 2", unacked)
	}
}
