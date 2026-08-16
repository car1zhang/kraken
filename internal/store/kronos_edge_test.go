package store

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// kronos_edge_test.go — the EDGE dimension of Slice A of
// artifacts/50-contract.md (the kronos build): what the orch_inbox table and
// its accessors REFUSE, what a refusal must leave behind (nothing), which
// boundary a comparison actually falls on, and what a v24 migration must not
// disturb on a POPULATED v23 database. Happy paths and the stated read
// invariants belong to the sibling blind file.
//
// Written blind: no byte of internal/store/orch.go, internal/store/recall.go
// or internal/store/schema.go was read while authoring this.
//
// Every package-level identifier declared here is prefixed `edgeKronos` —
// this package's test namespace is shared with the other blind author AND
// with the pre-existing slack/goals/watch edge files, which already own the
// bare `edge*` names.

// --- helpers ---------------------------------------------------------------

// edgeKronosStore opens a throwaway store. It exists so every test in this
// file reads the same way as the slack edge file beside it.
func edgeKronosStore(t *testing.T) *Store {
	t.Helper()
	return openTest(t)
}

// edgeKronosSeed inserts n orch rows through the frozen accessor and returns
// their ids in insertion order. Each row carries a distinct summary so a
// later assertion can name which row moved.
func edgeKronosSeed(t *testing.T, s *Store, kind string, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, err := s.AddOrchItem(kind, "job-"+strconv.Itoa(i), "summary "+strconv.Itoa(i), int64(1_769_040_000_000+i))
		if err != nil {
			t.Fatalf("AddOrchItem #%d: %v", i, err)
		}
		if id == 0 {
			t.Fatalf("AddOrchItem #%d returned id 0 — the row's identity is what every later call names", i)
		}
		ids = append(ids, id)
	}
	return ids
}

// edgeKronosUnackedIDs reads the unacked worklist's ids in the order it
// returns them.
func edgeKronosUnackedIDs(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// edgeKronosUndeliveredIDs reads the delivery worklist's ids in order.
func edgeKronosUndeliveredIDs(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func edgeKronosSameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// --- schema v24 ------------------------------------------------------------

// TestEdgeKronosSchemaIsV24 pins the version bump and its registration. A DDL
// string added without a migrations entry applies to a FRESH database and to
// no existing one — the failure mode is invisible on the machine that wrote
// it, because that machine's db is the fresh one.
//
// A floor rather than an exact pin, following the slack file's own correction:
// this contract needs v24 REACHED and its step registered, not the ladder
// frozen at 24 forever.
func TestEdgeKronosSchemaIsV24(t *testing.T) {
	if schemaVersion < 24 {
		t.Fatalf("schemaVersion = %d, want >= 24", schemaVersion)
	}
	if _, ok := migrations[24]; !ok {
		t.Fatal("migrations[24] is not registered — a fresh db would have orch_inbox and an upgraded one would not")
	}
}

// TestEdgeKronosMigrationPreservesAPopulatedV23DB is the contract's
// "migrating an existing v23 db preserves every existing row of every
// existing table" clause, exercised against a db that actually HAS rows.
//
// The v23 shape is reconstructed by the contract's own description of v24
// rather than through downgradeFreshDB: v24 "creates orch_inbox", so dropping
// exactly that table and rewinding user_version is the
// exact inverse. Doing it this way also keeps this test independent of
// whether the implementer remembered to teach downgradeFreshDB a v24 arm —
// that omission is the sibling walk test's business, not this one's.
func TestEdgeKronosMigrationPreservesAPopulatedV23DB(t *testing.T) {
	s := edgeKronosStore(t)

	// Populate three unrelated tables that a real v23 database would carry.
	jobID, err := s.CreateJob(&Job{Slug: "pre-existing", Repo: "kraken", Body: "written before v24"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if err := s.SetKV("edge-kronos-canary", "survive-me"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	if _, err := s.AddSlackBatch([]SlackInboxMsg{{
		ChannelID: "D1", TS: "1769040000.000100", AuthorID: "U1",
		Source: "dm", Text: "an older message", SeenAt: 1_769_040_000_000,
	}}, map[string]string{"dm:D1": "1769040000.000100"}); err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}

	// Rewind to a genuine v23 shape: v24's table gone, user_version 23.
	for _, stmt := range []string{
		// v25 adds saved_artifacts (the artifact library) — a later
		// migration's table in this walk's terms; leftover, it fails that
		// step's bare CREATE. Newest first.
		`DROP TABLE IF EXISTS saved_artifacts`,
		`DROP TABLE IF EXISTS orch_inbox`,
		`PRAGMA user_version = 23`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("rewind to v23 (%q): %v", stmt, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate a populated v23 db to v24: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after migrating, want %d", version, schemaVersion)
	}

	// Every pre-existing row is still there, byte for byte where it matters.
	after, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID after migrate: %v — v24 took a job row with it", err)
	}
	if after.Slug != before.Slug || after.Body != before.Body || after.Repo != before.Repo {
		t.Fatalf("job row changed across the migration: %+v -> %+v", before, after)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("v24 bumped a job's rev/updated_at (%d/%d -> %d/%d) — a schema step must not touch either",
			before.Rev, before.UpdatedAt, after.Rev, after.UpdatedAt)
	}
	raw, err := s.GetKV("edge-kronos-canary")
	if err != nil || raw != "survive-me" {
		t.Fatalf("kv canary after migrate = %q/%v, want %q", raw, err, "survive-me")
	}
	msgs, err := s.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after migrate: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Text != "an older message" {
		t.Fatalf("slack rows after migrate = %+v, want the one pre-existing message", msgs)
	}
	marks, err := s.SlackMarks()
	if err != nil || marks["dm:D1"] != "1769040000.000100" {
		t.Fatalf("slack marks after migrate = %+v/%v", marks, err)
	}

	// And the new table is usable on the upgraded (not merely fresh) db.
	if _, err := s.AddOrchItem("job-blocked", "pre-existing", "still blocked", 1_769_040_001_000); err != nil {
		t.Fatalf("AddOrchItem on an UPGRADED db: %v — the migration created the table only for fresh installs", err)
	}
}

// TestEdgeKronosMigrationIsIdempotent: a daemon restart re-runs the whole
// migration path against an already-current database. A v24 step written with
// a bare CREATE TABLE would fail here the second time — and on a real machine
// that failure is at Open(), i.e. the daemon never starts.
func TestEdgeKronosMigrationIsIdempotent(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 2)

	if err := s.migrate(); err != nil {
		t.Fatalf("re-migrate an already-current db: %v", err)
	}

	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
		t.Fatalf("unacked ids after re-migrate = %v, want %v", got, ids)
	}
}

// --- AddOrchItem ------------------------------------------------------------

// TestEdgeAddOrchItemRefusesEmptyKind: "Empty kind is an error." Kind is what
// the delivery side and kronos' own triage switch on, so a kindless row is a
// row nothing knows how to describe — and it must not land.
func TestEdgeAddOrchItemRefusesEmptyKind(t *testing.T) {
	s := edgeKronosStore(t)
	id, err := s.AddOrchItem("", "some-job", "a summary", 1_769_040_000_000)
	if err == nil {
		t.Fatalf("AddOrchItem with an empty kind returned id %d, want an error", id)
	}
	if got := edgeKronosUnackedIDs(t, s); len(got) != 0 {
		t.Fatalf("a refused AddOrchItem left %d row(s) behind: %v", len(got), got)
	}
}

// TestEdgeAddOrchItemAllowsEmptySlug: "empty slug is allowed" — a row may
// describe something belonging to no job, and refusing it would make that
// whole row class unrepresentable. (The store validates no kind vocabulary,
// so the fixture kind here is arbitrary.)
func TestEdgeAddOrchItemAllowsEmptySlug(t *testing.T) {
	s := edgeKronosStore(t)
	id, err := s.AddOrchItem("note", "", "what is blocking the export job", 1_769_040_000_000)
	if err != nil {
		t.Fatalf("AddOrchItem with an empty slug: %v — a slugless row is legal", err)
	}
	rows, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("unacked = %+v, want the one slugless row %d", rows, id)
	}
	if rows[0].Slug != "" {
		t.Fatalf("slug read back as %q, want empty", rows[0].Slug)
	}
	if rows[0].Kind != "note" {
		t.Fatalf("kind read back as %q, want %q", rows[0].Kind, "note")
	}
	if rows[0].SeenAt != 1_769_040_000_000 {
		t.Fatalf("seen_at = %d, want the `now` the caller passed (1769040000000)", rows[0].SeenAt)
	}
	if rows[0].DeliveredAt != 0 || rows[0].AckedAt != 0 || rows[0].Disposition != "" {
		t.Fatalf("a brand-new row is already delivered/acked: %+v", rows[0])
	}
}

// TestEdgeAddOrchItemNeverDedupes: "Never dedupes — dedup is the caller's."
// The daemon's Notified-key pattern is what stops a persisting condition
// enqueuing every tick; a store-side dedupe would ALSO swallow two genuinely
// distinct events that happen to render the same summary (two jobs blocking
// on the same one-line reason), which is normal traffic.
func TestEdgeAddOrchItemNeverDedupes(t *testing.T) {
	s := edgeKronosStore(t)
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := s.AddOrchItem("note", "", "?", 1_769_040_000_000)
		if err != nil {
			t.Fatalf("AddOrchItem #%d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatalf("identical rows collapsed onto shared ids %v — dedup is the caller's", ids)
	}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
		t.Fatalf("unacked = %v, want all three rows %v", got, ids)
	}
}

// --- UndeliveredOrch / UnackedOrch boundaries ------------------------------

// TestEdgeUndeliveredOrchExcludesAckedRows pins the AND in
// `delivered_at = 0 AND acked_at = 0`. A row kronos acked without ever being
// bumped about it is the ordinary result of kronos reading its inbox itself;
// left on the delivery worklist it would be bumped about forever.
func TestEdgeUndeliveredOrchExcludesAckedRows(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 3)

	// row 0: acked, never delivered — the case the AND exists for.
	if err := s.AckOrchItems(ids[:1], "escalated", 1_769_040_001_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	// row 1: delivered, not acked.
	if err := s.MarkOrchDelivered(ids[1:2], 1_769_040_001_000); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}

	if got := edgeKronosUndeliveredIDs(t, s); !edgeKronosSameIDs(got, ids[2:]) {
		t.Fatalf("undelivered = %v, want only %v", got, ids[2:])
	}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids[1:]) {
		t.Fatalf("unacked = %v, want %v — delivered-but-unacked stays reachable", got, ids[1:])
	}
}

// TestEdgeUnackedOrchKeepsDeliveredRows is the contract's stated REASON for
// UnackedOrch's looser filter, asserted as behaviour: "a row must never be
// unreachable because its bump send failed". Stamping delivery must not
// retire anything.
func TestEdgeUnackedOrchKeepsDeliveredRows(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "cell-gone", 2)
	if err := s.MarkOrchDelivered(ids, 1_769_040_001_000); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
		t.Fatalf("unacked after delivery = %v, want both rows %v", got, ids)
	}
	if got := edgeKronosUndeliveredIDs(t, s); len(got) != 0 {
		t.Fatalf("undelivered after delivery = %v, want none", got)
	}
}

// TestEdgeOrchReadsOnAnEmptyTable: a fresh install runs both worklists on
// every tick, before anything has ever been enqueued. Neither may error, and
// neither may report a phantom row.
func TestEdgeOrchReadsOnAnEmptyTable(t *testing.T) {
	s := edgeKronosStore(t)
	if got := edgeKronosUnackedIDs(t, s); len(got) != 0 {
		t.Fatalf("UnackedOrch on a fresh db = %v, want none", got)
	}
	if got := edgeKronosUndeliveredIDs(t, s); len(got) != 0 {
		t.Fatalf("UndeliveredOrch on a fresh db = %v, want none", got)
	}
}

// TestEdgeOrchWorklistsAreOldestFirstAcrossAGap: "oldest first (by id)" has to
// survive rows being retired out of the middle. Insertion order is the order
// kronos should read a batch in, and id is insertion order by construction —
// a seen_at sort would be a wall clock NTP can step backwards.
func TestEdgeOrchWorklistsAreOldestFirstAcrossAGap(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "pr-merged", 4)
	if err := s.AckOrchItems([]int64{ids[1]}, "noted", 1_769_040_001_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	want := []int64{ids[0], ids[2], ids[3]}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, want) {
		t.Fatalf("unacked = %v, want %v in ascending id order", got, want)
	}
	if got := edgeKronosUndeliveredIDs(t, s); !edgeKronosSameIDs(got, want) {
		t.Fatalf("undelivered = %v, want %v in ascending id order", got, want)
	}
}

// --- MarkOrchDelivered ------------------------------------------------------

// TestEdgeMarkOrchDeliveredRefusesUnknownID: "Unknown id is an error." This is
// the deliberate opposite of the slack table's stance, and the contract picked
// it, so it is pinned here.
//
// Only the ERROR is asserted, not a rollback: the contract says nothing about
// whether the known ids in a mixed batch are stamped, and inventing an answer
// either way would be this file writing the contract.
func TestEdgeMarkOrchDeliveredRefusesUnknownID(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 2)
	unknown := ids[len(ids)-1] + 4242

	for _, batch := range [][]int64{{unknown}, {ids[0], unknown}, {unknown, ids[0]}} {
		err := s.MarkOrchDelivered(batch, 1_769_040_001_000)
		if err == nil {
			t.Fatalf("MarkOrchDelivered(%v) = nil, want an error naming the unknown id %d", batch, unknown)
		}
		if !strings.Contains(err.Error(), strconv.FormatInt(unknown, 10)) {
			t.Errorf("MarkOrchDelivered(%v) err = %v, want it to name the offending id %d", batch, err, unknown)
		}
	}
}

// TestEdgeMarkOrchDeliveredAcceptsAlreadyDelivered: "already-delivered is NOT
// an error (re-bump after a failed stamp is legal)". The delivery pass is
// send-then-watermark, so a crash between the two leaves rows that get bumped
// and stamped twice — and the second stamp must not fail the whole batch and
// strand its neighbours undelivered forever.
func TestEdgeMarkOrchDeliveredAcceptsAlreadyDelivered(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "cell-unhealthy", 2)

	if err := s.MarkOrchDelivered(ids, 1_769_040_001_000); err != nil {
		t.Fatalf("first MarkOrchDelivered: %v", err)
	}
	if err := s.MarkOrchDelivered(ids, 1_769_040_002_000); err != nil {
		t.Fatalf("re-delivering an already-delivered batch = %v, want nil", err)
	}
	if got := edgeKronosUndeliveredIDs(t, s); len(got) != 0 {
		t.Fatalf("undelivered = %v after two stamps, want none", got)
	}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
		t.Fatalf("unacked = %v, want %v — delivery never retires a row", got, ids)
	}
}

// TestEdgeMarkOrchDeliveredEmptyIDsStampsNothing: the contract does not say
// whether an empty list is an error here (unlike the ack, where it is spelled
// out), so this asserts only the half that cannot be read two ways — no row is
// stamped either way.
func TestEdgeMarkOrchDeliveredEmptyIDsStampsNothing(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "start-failed", 1)
	for _, batch := range [][]int64{nil, {}} {
		_ = s.MarkOrchDelivered(batch, 1_769_040_001_000)
		if got := edgeKronosUndeliveredIDs(t, s); !edgeKronosSameIDs(got, ids) {
			t.Fatalf("ids %v: undelivered = %v, want %v — an empty list stamps nothing", batch, got, ids)
		}
	}
}

// TestEdgeMarkOrchDeliveredStampsExactlyTheGivenRows: "on exactly the given
// rows". A neighbour stamped by accident is a bump kronos never got.
func TestEdgeMarkOrchDeliveredStampsExactlyTheGivenRows(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 3)
	if err := s.MarkOrchDelivered([]int64{ids[1]}, 1_769_040_001_000); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	want := []int64{ids[0], ids[2]}
	if got := edgeKronosUndeliveredIDs(t, s); !edgeKronosSameIDs(got, want) {
		t.Fatalf("undelivered = %v, want %v", got, want)
	}
	rows, err := s.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	for _, r := range rows {
		wantStamped := r.ID == ids[1]
		if (r.DeliveredAt != 0) != wantStamped {
			t.Fatalf("row %d delivered_at = %d, wantStamped=%v", r.ID, r.DeliveredAt, wantStamped)
		}
		if wantStamped && r.DeliveredAt != 1_769_040_001_000 {
			t.Fatalf("row %d delivered_at = %d, want the `now` passed in", r.ID, r.DeliveredAt)
		}
	}
}

// --- AckOrchItems: the all-or-nothing refusals -----------------------------

// TestEdgeAckOrchItemsRefusesUnknownID is the strictest rule in the slice:
// "if ANY id is unknown or already acked, the call errors and NO row changes".
// Kronos acks a batch it just read, so an id naming no row means it is working
// from a stale list — and applying the rest would retire real escalations on
// the strength of a request already known to be wrong.
func TestEdgeAckOrchItemsRefusesUnknownID(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 3)
	unknown := ids[len(ids)-1] + 4242

	for _, batch := range [][]int64{
		{unknown},
		{ids[0], unknown},
		{unknown, ids[0]},
		{ids[0], ids[1], unknown},
	} {
		err := s.AckOrchItems(batch, "escalated to carl", 1_769_040_001_000)
		if err == nil {
			t.Fatalf("AckOrchItems(%v) = nil, want a refusal — id %d does not exist", batch, unknown)
		}
		if !strings.Contains(err.Error(), strconv.FormatInt(unknown, 10)) {
			t.Errorf("AckOrchItems(%v) err = %v, want it to name the offending id %d", batch, err, unknown)
		}
		if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
			t.Fatalf("AckOrchItems(%v): unacked = %v, want all %v — a refused ack applies NOTHING", batch, got, ids)
		}
	}
}

// TestEdgeAckOrchItemsRefusesAnAlreadyAckedID: a re-ack means kronos lost
// track of what it had done, and silently re-stamping would overwrite the
// disposition recording what actually happened the first time — the row is the
// only durable record, kronos' context is not.
func TestEdgeAckOrchItemsRefusesAnAlreadyAckedID(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 2)

	if err := s.AckOrchItems(ids[:1], "escalated:told carl", 1_769_040_001_000); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids[1:]) {
		t.Fatalf("unacked after one ack = %v, want %v", got, ids[1:])
	}

	for _, batch := range [][]int64{{ids[0]}, {ids[0], ids[1]}, {ids[1], ids[0]}} {
		err := s.AckOrchItems(batch, "ignored", 1_769_040_002_000)
		if err == nil {
			t.Fatalf("AckOrchItems(%v) re-acked an already-acked row", batch)
		}
		if !strings.Contains(err.Error(), strconv.FormatInt(ids[0], 10)) {
			t.Errorf("AckOrchItems(%v) err = %v, want it to name id %d", batch, err, ids[0])
		}
		if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids[1:]) {
			t.Fatalf("AckOrchItems(%v): unacked = %v, want %v — the still-open row must survive a refused call",
				batch, got, ids[1:])
		}
	}

	// And the first ack's disposition is intact: the refused calls carried a
	// different one, and a partial apply would have overwritten it.
	rows, err := s.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	for _, r := range rows {
		if r.Disposition == "ignored" {
			t.Fatalf("row %d carries the REFUSED call's disposition %q", r.ID, r.Disposition)
		}
	}
}

// TestEdgeAckOrchItemsRefusesEmptyDisposition: "Empty disposition is an
// error." The disposition is the only record of WHY a row left the inbox, so
// "acked, reason unknown" is indistinguishable from a bug that acks
// everything.
func TestEdgeAckOrchItemsRefusesEmptyDisposition(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 1)
	if err := s.AckOrchItems(ids, "", 1_769_040_001_000); err == nil {
		t.Fatal("AckOrchItems with an empty disposition = nil, want an error")
	}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
		t.Fatalf("unacked = %v, want %v — a refused ack applies NOTHING", got, ids)
	}
}

// TestEdgeAckOrchItemsRefusesEmptyIDs: "Empty ids is an error." A silent
// success would tell kronos its batch was recorded when nothing was.
func TestEdgeAckOrchItemsRefusesEmptyIDs(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 1)
	for _, batch := range [][]int64{nil, {}} {
		if err := s.AckOrchItems(batch, "escalated", 1_769_040_001_000); err == nil {
			t.Fatalf("AckOrchItems(%v) = nil, want an error", batch)
		}
		if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, ids) {
			t.Fatalf("AckOrchItems(%v): unacked = %v, want %v", batch, got, ids)
		}
	}
}

// TestEdgeAckOrchItemsLeavesOtherRowsAlone: the successful case's boundary —
// stamping the named rows must not retire a neighbour.
func TestEdgeAckOrchItemsLeavesOtherRowsAlone(t *testing.T) {
	s := edgeKronosStore(t)
	ids := edgeKronosSeed(t, s, "job-blocked", 3)
	if err := s.AckOrchItems(ids[1:2], "ignored:duplicate of the row above", 1_769_040_001_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	want := []int64{ids[0], ids[2]}
	if got := edgeKronosUnackedIDs(t, s); !edgeKronosSameIDs(got, want) {
		t.Fatalf("unacked = %v, want %v", got, want)
	}
	if got := edgeKronosUndeliveredIDs(t, s); !edgeKronosSameIDs(got, want) {
		t.Fatalf("undelivered = %v, want %v — an ack removes nothing from the table", got, want)
	}
}

// TestEdgeOrchWritesNeverTouchJobRows: no column in orch_inbox belongs to a
// job, so nothing in the slice may bump a job's rev or updated_at — the same
// rule SetJobLoad and SetPRWatermark follow, and for the same reason
// (`updated_at` is a card's rendered AGE, `rev` is the editor's optimistic
// lock). An escalation about a job is not an edit of it.
func TestEdgeOrchWritesNeverTouchJobRows(t *testing.T) {
	s := edgeKronosStore(t)
	jobID, err := s.CreateJob(&Job{Slug: "escalated-about", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	assertUntouched := func(what string) {
		t.Helper()
		after, err := s.GetJobByID(jobID)
		if err != nil {
			t.Fatalf("%s: GetJobByID: %v", what, err)
		}
		if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
			t.Fatalf("%s moved the job's rev/updated_at: %d/%d -> %d/%d",
				what, before.Rev, before.UpdatedAt, after.Rev, after.UpdatedAt)
		}
	}

	id, err := s.AddOrchItem("job-blocked", "escalated-about", "waiting on carl", 1_769_040_000_000)
	if err != nil {
		t.Fatalf("AddOrchItem: %v", err)
	}
	assertUntouched("AddOrchItem")

	if err := s.MarkOrchDelivered([]int64{id}, 1_769_040_001_000); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	assertUntouched("MarkOrchDelivered")

	if err := s.AckOrchItems([]int64{id}, "escalated", 1_769_040_002_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}
	assertUntouched("AckOrchItems")

	_ = s.AckOrchItems([]int64{id + 9999}, "escalated", 1_769_040_003_000)
	assertUntouched("a refused AckOrchItems")
}

// --- ClearRecalled ---------------------------------------------------------
// (Five imsg_sent echo-guard boundary tests sat here and were deliberately
// obsoleted with the iMessage bridge's removal at the manual gate.)

// TestEdgeClearRecalledRefusesABlankSession: "Blank sessionID is an error and
// deletes nothing." This is the whole safety of the PostCompact re-arm — a
// blank id that fell through to a DELETE with no WHERE would wipe every
// cell's recall marks on the machine, and every one of those facts would then
// be re-injected into every session that had already seen it.
func TestEdgeClearRecalledRefusesABlankSession(t *testing.T) {
	s := edgeKronosStore(t)
	if err := s.MarkRecalled("cell-session", []int64{11, 22, 33}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	for _, blank := range []string{"", "   ", "\t\n"} {
		n, err := s.ClearRecalled(blank)
		if err == nil {
			t.Errorf("ClearRecalled(%q) = %d, nil — want an error", blank, n)
		}
		if n != 0 {
			t.Errorf("ClearRecalled(%q) reported %d deleted despite refusing", blank, n)
		}
		ids, rerr := s.RecalledEdgeIDs("cell-session")
		if rerr != nil {
			t.Fatalf("RecalledEdgeIDs: %v", rerr)
		}
		if len(ids) != 3 {
			t.Fatalf("ClearRecalled(%q) left %d of 3 marks — a blank session must delete NOTHING", blank, len(ids))
		}
	}
}

// TestEdgeClearRecalledTouchesOnlyTheNamedSession: "Rows of other sessions are
// untouched." Kronos' re-arm runs on one eternal session; taking a cell's
// marks with it would re-deliver facts into cells that already have them.
func TestEdgeClearRecalledTouchesOnlyTheNamedSession(t *testing.T) {
	s := edgeKronosStore(t)
	if err := s.MarkRecalled("kronos-session", []int64{1, 2, 3, 4}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled kronos: %v", err)
	}
	if err := s.MarkRecalled("a-cell-session", []int64{1, 2}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}

	n, err := s.ClearRecalled("kronos-session")
	if err != nil {
		t.Fatalf("ClearRecalled: %v", err)
	}
	if n != 4 {
		t.Fatalf("ClearRecalled deleted %d, want 4 — the count is what the route reports back", n)
	}
	gone, err := s.RecalledEdgeIDs("kronos-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(kronos): %v", err)
	}
	if len(gone) != 0 {
		t.Fatalf("kronos' marks after the clear = %+v, want none", gone)
	}
	kept, err := s.RecalledEdgeIDs("a-cell-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(cell): %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("the other session's marks = %+v, want its 2 — a clear is session-scoped", kept)
	}
}

// TestEdgeClearRecalledOnASessionWithNoRows: kronos compacts before it has
// ever been injected with anything, and the PostCompact hook fires anyway.
// Zero rows is a real answer, not a failure — the hook exits 0 on any failure,
// so an error here would be invisible AND would leave the marks in place.
func TestEdgeClearRecalledOnASessionWithNoRows(t *testing.T) {
	s := edgeKronosStore(t)
	if err := s.MarkRecalled("someone-else", []int64{7}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	n, err := s.ClearRecalled("never-injected")
	if err != nil {
		t.Fatalf("ClearRecalled on a session with no rows = %v, want nil", err)
	}
	if n != 0 {
		t.Fatalf("ClearRecalled reported %d deleted for a session with no rows", n)
	}
	kept, err := s.RecalledEdgeIDs("someone-else")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("the unrelated session lost marks: %+v", kept)
	}
}

// TestEdgeClearRecalledIsRepeatable: the hook can fire twice for one
// compaction (or the daemon can retry), and the second call must be a plain
// zero rather than an error — nothing is left to delete, which is success.
func TestEdgeClearRecalledIsRepeatable(t *testing.T) {
	s := edgeKronosStore(t)
	if err := s.MarkRecalled("kronos-session", []int64{1, 2}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	if n, err := s.ClearRecalled("kronos-session"); err != nil || n != 2 {
		t.Fatalf("first ClearRecalled = %d/%v, want 2/nil", n, err)
	}
	n, err := s.ClearRecalled("kronos-session")
	if err != nil {
		t.Fatalf("second ClearRecalled = %v, want nil", err)
	}
	if n != 0 {
		t.Fatalf("second ClearRecalled deleted %d, want 0", n)
	}
}

// TestEdgeClearRecalledErrorIsNotErrNotFound guards one specific
// misclassification: the blank-session refusal must be a real error the route
// can turn into a 400, not the store's "nothing here" sentinel, which every
// caller in this codebase treats as a benign absence.
func TestEdgeClearRecalledErrorIsNotErrNotFound(t *testing.T) {
	s := edgeKronosStore(t)
	_, err := s.ClearRecalled("")
	if err == nil {
		t.Fatal("ClearRecalled(\"\") = nil, want an error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("ClearRecalled(\"\") returned ErrNotFound (%v) — a blank id is a caller mistake, not an absence", err)
	}
}
