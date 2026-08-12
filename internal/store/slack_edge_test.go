package store

import (
	"strconv"
	"strings"
	"testing"
)

// slack_edge_test.go — the EDGE dimension of slice 2 of
// artifacts/50-contract.md: what the two new tables and their accessors
// REFUSE, what a refusal must leave behind (nothing), and the collateral
// damage a slack write must never do to a jobs row. Happy paths and the
// stated read invariants belong to the sibling blind file.
//
// Every package-level identifier declared here is prefixed `edge` — this
// package's test namespace is shared with the other blind author.

// --- helpers ---------------------------------------------------------------

// edgeMsg builds one inbox row with every NOT NULL column populated, so a
// case that varies one field is varying exactly that field.
func edgeMsg(channel, ts string) SlackInboxMsg {
	return SlackInboxMsg{
		ChannelID:   channel,
		ChannelName: "carl",
		TS:          ts,
		AuthorID:    "U0001",
		AuthorName:  "someone",
		Source:      "dm",
		Text:        "the csv export times out",
		SeenAt:      1769040000000,
	}
}

// edgeSeedInbox commits msgs through the frozen batch accessor and fails the
// test if the insert count is not what the caller expected — every later
// assertion depends on knowing exactly which rows exist.
func edgeSeedInbox(t *testing.T, s *Store, want int, msgs ...SlackInboxMsg) []SlackInboxMsg {
	t.Helper()
	n, err := s.AddSlackBatch(msgs, nil)
	if err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	if n != want {
		t.Fatalf("AddSlackBatch inserted %d, want %d", n, want)
	}
	rows, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	return rows
}

// edgeInboxIDs reads the current unacked rows' ids in order.
func edgeInboxIDs(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// edgeCounts is a one-line read of the three lifecycle counters.
func edgeCounts(t *testing.T, s *Store) (total, undelivered, unacked int) {
	t.Helper()
	total, undelivered, unacked, err := s.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts: %v", err)
	}
	return total, undelivered, unacked
}

// --- schema v17 ------------------------------------------------------------

// TestEdgeSlackSchemaIsV17 pins the version bump and its registration. A DDL
// string added without a migrations entry applies to a FRESH database and to
// no existing one — the failure mode is invisible on the machine that wrote it.
// A floor rather than an exact pin: this contract needs v17 REACHED and its
// step registered, not the ladder frozen — an exact == has now broken on
// every later migration it met (v17 over the watch contract's == 16, v18
// linear_issues over this test's own == 17).
func TestEdgeSlackSchemaIsV17(t *testing.T) {
	if schemaVersion < 17 {
		t.Fatalf("schemaVersion = %d, want >= 17", schemaVersion)
	}
	if _, ok := migrations[17]; !ok {
		t.Fatal("migrations[17] is not registered — a fresh db would have the tables and an upgraded one would not")
	}
	if strings.TrimSpace(schemaV17) == "" {
		t.Fatal("schemaV17 is empty")
	}
}

// TestEdgeSlackSchemaIsAppendOnly: v17 "adds slack_inbox and slack_marks,
// touches nothing else". A DROP or an ALTER of an existing table smuggled
// into this migration would take data with it, and the migration only ever
// runs once so there is no second chance to notice.
func TestEdgeSlackSchemaIsAppendOnly(t *testing.T) {
	lowered := strings.ToLower(schemaV17)
	for _, forbidden := range []string{"drop table", "drop column", "delete from", "alter table jobs", "drop index"} {
		if strings.Contains(lowered, forbidden) {
			t.Errorf("schemaV17 contains %q — v17 is append-only", forbidden)
		}
	}
	if !strings.Contains(lowered, "slack_inbox") || !strings.Contains(lowered, "slack_marks") {
		t.Errorf("schemaV17 does not create both new tables:\n%s", schemaV17)
	}
}

// TestEdgeSlackTablesSurviveReopen: migrate() must be idempotent over the new
// version — a second Open of the same file re-runs nothing and the rows stay.
func TestEdgeSlackTablesSurviveReopen(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 1, edgeMsg("D1", "1769040000.000100"))
	if _, err := s.AddSlackBatch(nil, map[string]string{"dm:D1": "1769040000.000100"}); err != nil {
		t.Fatalf("AddSlackBatch marks: %v", err)
	}
	// Re-running the whole migration path against an already-current database
	// is what a daemon restart does; a v17 that is not idempotent fails here.
	if err := s.migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if total, _, _ := edgeCounts(t, s); total != 1 {
		t.Fatalf("total = %d after re-migrate, want 1", total)
	}
	marks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if marks["dm:D1"] != "1769040000.000100" {
		t.Fatalf("marks = %+v after re-migrate", marks)
	}
}

// --- AddSlackBatch refusals and skips -------------------------------------

// TestEdgeAddSlackBatchSkipsDuplicatesAcrossBatches is the at-least-once
// guarantee's other half: polling is exclusive-`oldest` but overlapping
// windows (and the mention search's 24h overlap) re-present the same message
// routinely, so a duplicate must be SKIPPED and counted as such — never an
// error that aborts the batch, and never a second row.
func TestEdgeAddSlackBatchSkipsDuplicatesAcrossBatches(t *testing.T) {
	s := openTest(t)
	batch := []SlackInboxMsg{
		edgeMsg("D1", "1769040000.000100"),
		edgeMsg("D1", "1769040000.000200"),
	}
	marks := map[string]string{"dm:D1": "1769040000.000200"}

	n, err := s.AddSlackBatch(batch, marks)
	if err != nil || n != 2 {
		t.Fatalf("first batch: n=%d err=%v, want 2/nil", n, err)
	}
	firstIDs := edgeInboxIDs(t, s)

	n, err = s.AddSlackBatch(batch, marks)
	if err != nil {
		t.Fatalf("replayed batch errored: %v", err)
	}
	if n != 0 {
		t.Fatalf("replayed batch inserted %d, want 0 — every row was a duplicate", n)
	}
	if got := edgeInboxIDs(t, s); len(got) != 2 || got[0] != firstIDs[0] || got[1] != firstIDs[1] {
		t.Fatalf("ids after replay = %v, want the original %v (no new rows, no renumbering)", got, firstIDs)
	}
	got, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if len(got) != 1 || got["dm:D1"] != "1769040000.000200" {
		t.Fatalf("marks after replay = %+v, want exactly the one final mark", got)
	}
}

// TestEdgeAddSlackBatchSkipsDuplicatesWithinOneBatch: the mention search and
// the history sweep can both surface the same message in ONE pass (a
// thread_broadcast is the documented case), so the duplicate arrives inside a
// single slice. INSERT OR IGNORE has to absorb it without failing the
// transaction that carries every other row.
func TestEdgeAddSlackBatchSkipsDuplicatesWithinOneBatch(t *testing.T) {
	s := openTest(t)
	dup := edgeMsg("D1", "1769040000.000100")
	other := dup
	other.Source = "mention" // same (channel_id, ts): still one row
	n, err := s.AddSlackBatch([]SlackInboxMsg{dup, other, edgeMsg("D1", "1769040000.000200")}, nil)
	if err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted %d, want 2 — the in-batch duplicate is skipped, not an error", n)
	}
	if total, _, _ := edgeCounts(t, s); total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
}

// TestEdgeAddSlackBatchKeepsSameTSInDifferentChannels: the uniqueness is on
// the PAIR. Slack ts values are per-workspace clock readings, so two DMs can
// legitimately carry the same one — a UNIQUE(ts) would silently drop the
// second message forever.
func TestEdgeAddSlackBatchKeepsSameTSInDifferentChannels(t *testing.T) {
	s := openTest(t)
	n, err := s.AddSlackBatch([]SlackInboxMsg{
		edgeMsg("D1", "1769040000.000100"),
		edgeMsg("D2", "1769040000.000100"),
	}, nil)
	if err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted %d, want 2 — uniqueness is (channel_id, ts), not ts", n)
	}
}

// TestEdgeAddSlackBatchRefusesAnUnknownSource pins the CHECK constraint. The
// three values are what the delivery and drafting sides switch on, so a
// fourth would flow all the way to iris' prompt as an unhandled case — and
// the refusal must roll the WHOLE batch back, since a half-committed batch
// beside an advanced mark is a message lost forever.
func TestEdgeAddSlackBatchRefusesAnUnknownSource(t *testing.T) {
	for _, bad := range []string{"email", "channel", "DM", "", "dm;", "im"} {
		t.Run(strconv.Quote(bad), func(t *testing.T) {
			s := openTest(t)
			good := edgeMsg("D1", "1769040000.000100")
			offender := edgeMsg("D1", "1769040000.000200")
			offender.Source = bad

			_, err := s.AddSlackBatch([]SlackInboxMsg{good, offender}, map[string]string{"dm:D1": "1769040000.000200"})
			if err == nil {
				t.Fatalf("source %q was accepted", bad)
			}
			if total, _, _ := edgeCounts(t, s); total != 0 {
				t.Fatalf("a refused batch committed %d row(s) — it must be one transaction", total)
			}
			marks, merr := s.SlackMarks()
			if merr != nil {
				t.Fatalf("SlackMarks: %v", merr)
			}
			if len(marks) != 0 {
				t.Fatalf("a refused batch advanced marks: %+v — a mark past an uncommitted row loses it forever", marks)
			}
		})
	}
	// The three legal values are accepted, so the test above is measuring the
	// constraint rather than a broken insert.
	for _, ok := range []string{"dm", "mpim", "mention"} {
		s := openTest(t)
		m := edgeMsg("D1", "1769040000.000100")
		m.Source = ok
		if _, err := s.AddSlackBatch([]SlackInboxMsg{m}, nil); err != nil {
			t.Fatalf("source %q refused: %v", ok, err)
		}
	}
}

// TestEdgeAddSlackBatchEmptyInputs: a pass that found nothing still calls the
// batch (the marks may have moved, or not), so neither an empty slice nor a
// nil map may be an error.
func TestEdgeAddSlackBatchEmptyInputs(t *testing.T) {
	s := openTest(t)
	for _, c := range []struct {
		name  string
		msgs  []SlackInboxMsg
		marks map[string]string
	}{
		{"both nil", nil, nil},
		{"empty slice, nil map", []SlackInboxMsg{}, nil},
		{"nil slice, empty map", nil, map[string]string{}},
	} {
		n, err := s.AddSlackBatch(c.msgs, c.marks)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n != 0 {
			t.Fatalf("%s: inserted %d, want 0", c.name, n)
		}
	}
	if total, _, _ := edgeCounts(t, s); total != 0 {
		t.Fatalf("total = %d after empty batches, want 0", total)
	}
}

// TestEdgeSlackMarksReplaceRatherThanAccumulate: `key` is the PRIMARY KEY, so
// re-writing a conversation's watermark every pass must upsert. A second row
// per key would make SlackMarks' map non-deterministic and could hand the
// poller an OLD watermark, re-emitting everything since.
func TestEdgeSlackMarksReplaceRatherThanAccumulate(t *testing.T) {
	s := openTest(t)
	for _, ts := range []string{"1769040000.000100", "1769040000.000200", "1769040000.000300"} {
		if _, err := s.AddSlackBatch(nil, map[string]string{"dm:D1": ts, "mentions": ts}); err != nil {
			t.Fatalf("AddSlackBatch marks %s: %v", ts, err)
		}
	}
	marks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks: %v", err)
	}
	if len(marks) != 2 {
		t.Fatalf("marks = %+v, want exactly 2 keys", marks)
	}
	if marks["dm:D1"] != "1769040000.000300" || marks["mentions"] != "1769040000.000300" {
		t.Fatalf("marks = %+v, want the last write to win", marks)
	}
}

// TestEdgeSlackMarksEmptyMapWhenNone: the "empty map when none" clause. A nil
// map is fine to READ from in Go, so this is really about the error: a fresh
// install must not surface "no rows" as a failure the poller aborts on.
func TestEdgeSlackMarksEmptyMapWhenNone(t *testing.T) {
	s := openTest(t)
	marks, err := s.SlackMarks()
	if err != nil {
		t.Fatalf("SlackMarks on an empty table: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("marks = %+v, want empty", marks)
	}
	if marks["dm:nothing"] != "" {
		t.Fatalf("absent key read back as %q", marks["dm:nothing"])
	}
}

// --- AckSlackMessages: the all-or-nothing refusals ------------------------

// TestEdgeAckSlackMessagesRefusesUnknownID is the contract's strictest store
// rule: iris acks a BATCH it just read, so an id that does not exist means
// iris is working from a stale or invented list — and applying the rest of
// the batch anyway would mark real messages handled on the strength of a
// request that was already wrong.
func TestEdgeAckSlackMessagesRefusesUnknownID(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 2, edgeMsg("D1", "1769040000.000100"), edgeMsg("D1", "1769040000.000200"))
	ids := edgeInboxIDs(t, s)
	unknown := ids[len(ids)-1] + 4242

	for _, batch := range [][]int64{
		{unknown},
		{ids[0], unknown},
		{unknown, ids[0]},
		{ids[0], ids[1], unknown},
	} {
		err := s.AckSlackMessages(batch, "drafted:csv-export-timeout", 1769040001000)
		if err == nil {
			t.Fatalf("batch %v was accepted with an unknown id in it", batch)
		}
		if !strings.Contains(err.Error(), strconv.FormatInt(unknown, 10)) {
			t.Errorf("batch %v: err = %v, want it to name the offending id %d", batch, err, unknown)
		}
		if _, _, unacked := edgeCounts(t, s); unacked != 2 {
			t.Fatalf("batch %v: unacked = %d, want 2 — a refused ack applies NOTHING", batch, unacked)
		}
	}
}

// TestEdgeAckSlackMessagesRefusesAnAlreadyAckedID: a re-ack means iris lost
// track of what it had done, and silently re-stamping would overwrite the
// disposition that recorded what actually happened the first time (the row is
// the only durable record — iris' context is not).
func TestEdgeAckSlackMessagesRefusesAnAlreadyAckedID(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 2, edgeMsg("D1", "1769040000.000100"), edgeMsg("D1", "1769040000.000200"))
	ids := edgeInboxIDs(t, s)

	if err := s.AckSlackMessages(ids[:1], "drafted:csv-export-timeout", 1769040001000); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if _, _, unacked := edgeCounts(t, s); unacked != 1 {
		t.Fatalf("unacked = %d after one ack, want 1", unacked)
	}

	for _, batch := range [][]int64{{ids[0]}, {ids[0], ids[1]}, {ids[1], ids[0]}} {
		err := s.AckSlackMessages(batch, "ignored", 1769040002000)
		if err == nil {
			t.Fatalf("batch %v re-acked an already-acked row", batch)
		}
		if !strings.Contains(err.Error(), strconv.FormatInt(ids[0], 10)) {
			t.Errorf("batch %v: err = %v, want it to name id %d", batch, err, ids[0])
		}
		if _, _, unacked := edgeCounts(t, s); unacked != 1 {
			t.Fatalf("batch %v: unacked = %d, want 1 — the still-open row must not be acked by a refused call",
				batch, unacked)
		}
	}
}

// TestEdgeAckSlackMessagesRefusesEmptyIDs: "empty ids is an error". A silent
// no-op 200 would let iris believe it had closed a batch it never named.
func TestEdgeAckSlackMessagesRefusesEmptyIDs(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 1, edgeMsg("D1", "1769040000.000100"))

	for _, ids := range [][]int64{nil, {}} {
		if err := s.AckSlackMessages(ids, "drafted:x", 1769040001000); err == nil {
			t.Fatalf("ids %v was accepted", ids)
		}
		if _, _, unacked := edgeCounts(t, s); unacked != 1 {
			t.Fatalf("ids %v: unacked = %d, want 1", ids, unacked)
		}
	}
}

// TestEdgeAckSlackMessagesLeavesOtherRowsAlone: the successful case's
// boundary — stamping the named rows must not touch a neighbour, and must not
// touch a row in another channel.
func TestEdgeAckSlackMessagesLeavesOtherRowsAlone(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 3,
		edgeMsg("D1", "1769040000.000100"),
		edgeMsg("D1", "1769040000.000200"),
		edgeMsg("D2", "1769040000.000300"),
	)
	ids := edgeInboxIDs(t, s)
	if err := s.AckSlackMessages(ids[1:2], "ignored", 1769040001000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}
	left := edgeInboxIDs(t, s)
	if len(left) != 2 || left[0] != ids[0] || left[1] != ids[2] {
		t.Fatalf("remaining unacked = %v, want %v", left, []int64{ids[0], ids[2]})
	}
	total, _, unacked := edgeCounts(t, s)
	if total != 3 || unacked != 2 {
		t.Fatalf("total/unacked = %d/%d, want 3/2 — an ack removes nothing", total, unacked)
	}
}

// --- MarkSlackDelivered: best-effort, by contract -------------------------

// TestEdgeMarkSlackDeliveredIgnoresUnknownIDs is the deliberate ASYMMETRY
// with ack: delivery is bookkeeping about a pane bump, so an id that has
// since been acked (or never existed) must not fail the call and strand the
// rows that DID get bumped at delivered_at 0, re-bumping iris every tick.
func TestEdgeMarkSlackDeliveredIgnoresUnknownIDs(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 2, edgeMsg("D1", "1769040000.000100"), edgeMsg("D1", "1769040000.000200"))
	ids := edgeInboxIDs(t, s)

	if err := s.MarkSlackDelivered([]int64{ids[0], ids[1] + 9999}, 1769040001000); err != nil {
		t.Fatalf("MarkSlackDelivered with an unknown id: %v — unknown ids are ignored", err)
	}
	total, undelivered, unacked := edgeCounts(t, s)
	if total != 2 || undelivered != 1 || unacked != 2 {
		t.Fatalf("total/undelivered/unacked = %d/%d/%d, want 2/1/2", total, undelivered, unacked)
	}
}

// TestEdgeMarkSlackDeliveredEmptyIDsChangesNothing: the contract does not say
// whether an empty list is an error here (unlike ack, where it is spelled
// out), so this asserts only the part that cannot be read two ways — no row
// is stamped either way.
func TestEdgeMarkSlackDeliveredEmptyIDsChangesNothing(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 1, edgeMsg("D1", "1769040000.000100"))
	for _, ids := range [][]int64{nil, {}} {
		_ = s.MarkSlackDelivered(ids, 1769040001000)
		if _, undelivered, _ := edgeCounts(t, s); undelivered != 1 {
			t.Fatalf("ids %v: undelivered = %d, want 1 — an empty list stamps nothing", ids, undelivered)
		}
	}
}

// --- the two read sets' boundaries ----------------------------------------

// TestEdgeUndeliveredExcludesAckedRows pins the AND in
// `delivered_at == 0 AND acked_at == 0`. A row acked without ever having been
// delivered is the ordinary result of iris reading the inbox itself rather
// than waiting for a bump — and if it stayed in the undelivered set the
// delivery pass would bump iris about it forever.
func TestEdgeUndeliveredExcludesAckedRows(t *testing.T) {
	s := openTest(t)
	edgeSeedInbox(t, s, 3,
		edgeMsg("D1", "1769040000.000100"),
		edgeMsg("D1", "1769040000.000200"),
		edgeMsg("D1", "1769040000.000300"),
	)
	ids := edgeInboxIDs(t, s)

	// row 0: acked, never delivered — the case the AND exists for.
	if err := s.AckSlackMessages(ids[:1], "ignored", 1769040001000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}
	// row 1: delivered, not acked — still unacked, no longer undelivered.
	if err := s.MarkSlackDelivered(ids[1:2], 1769040001000); err != nil {
		t.Fatalf("MarkSlackDelivered: %v", err)
	}

	undel, err := s.UndeliveredSlackInbox()
	if err != nil {
		t.Fatalf("UndeliveredSlackInbox: %v", err)
	}
	if len(undel) != 1 || undel[0].ID != ids[2] {
		t.Fatalf("undelivered = %+v, want only row %d", undel, ids[2])
	}
	unacked, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if len(unacked) != 2 || unacked[0].ID != ids[1] || unacked[1].ID != ids[2] {
		t.Fatalf("unacked = %+v, want rows %d and %d in id order", unacked, ids[1], ids[2])
	}
	total, undeliveredN, unackedN := edgeCounts(t, s)
	if total != 3 || undeliveredN != 1 || unackedN != 2 {
		t.Fatalf("counts = %d/%d/%d, want 3/1/2 (and they must agree with the two lists)",
			total, undeliveredN, unackedN)
	}
}

// TestEdgeSlackReadsOnAnEmptyTable: a fresh install must answer three empty
// results and three zero counts without an error — the poller runs before
// anything has ever been seen, every time the daemon starts.
func TestEdgeSlackReadsOnAnEmptyTable(t *testing.T) {
	s := openTest(t)
	unacked, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if len(unacked) != 0 {
		t.Fatalf("unacked = %+v, want none", unacked)
	}
	undel, err := s.UndeliveredSlackInbox()
	if err != nil {
		t.Fatalf("UndeliveredSlackInbox: %v", err)
	}
	if len(undel) != 0 {
		t.Fatalf("undelivered = %+v, want none", undel)
	}
	total, undeliveredN, unackedN := edgeCounts(t, s)
	if total != 0 || undeliveredN != 0 || unackedN != 0 {
		t.Fatalf("counts = %d/%d/%d, want all zero", total, undeliveredN, unackedN)
	}
}

// --- no collateral writes -------------------------------------------------

// TestEdgeSlackWritesNeverTouchJobRows is the "never touches any jobs row"
// clause, swept across every writer in the slice. It matters for the same
// reason SetJobLoad bypasses UpdateJobFields: `updated_at` is what a card
// renders as its AGE and `rev` is the job editor's optimistic lock, so a
// slack write that bumped either would reorder the whole board on every poll
// and 409 an open editor over a message nobody's job had anything to do with.
func TestEdgeSlackWritesNeverTouchJobRows(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "unrelated", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	assertUntouched := func(t *testing.T, what string) {
		t.Helper()
		after, err := s.GetJobByID(id)
		if err != nil {
			t.Fatalf("%s: GetJobByID: %v", what, err)
		}
		if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
			t.Fatalf("%s moved the job's rev/updated_at: %d/%d -> %d/%d",
				what, before.Rev, before.UpdatedAt, after.Rev, after.UpdatedAt)
		}
	}

	if _, err := s.AddSlackBatch([]SlackInboxMsg{edgeMsg("D1", "1769040000.000100")},
		map[string]string{"dm:D1": "1769040000.000100"}); err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	assertUntouched(t, "AddSlackBatch")

	ids := edgeInboxIDs(t, s)
	if err := s.MarkSlackDelivered(ids, 1769040001000); err != nil {
		t.Fatalf("MarkSlackDelivered: %v", err)
	}
	assertUntouched(t, "MarkSlackDelivered")

	if err := s.AckSlackMessages(ids, "drafted:unrelated", 1769040002000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}
	assertUntouched(t, "AckSlackMessages")

	// A REFUSED ack takes the same path far enough to open a transaction, so
	// it gets the same assertion.
	_ = s.AckSlackMessages([]int64{ids[0] + 9999}, "drafted:unrelated", 1769040003000)
	assertUntouched(t, "a refused AckSlackMessages")
}
