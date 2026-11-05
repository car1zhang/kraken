package store

import (
	"testing"
)

// agentmap_contract_test.go — athena-logic's blind tests for the cell-to-cell
// message record (50-contract.md G4): schema v40's job_messages table,
// BumpJobMessage, ListJobMessages, and DeleteJob's cascade.

func agentMapContractSeed(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	s := openTest(t)
	var ids [3]int64
	for i, slug := range []string{"msg-a", "msg-b", "msg-c"} {
		id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken"})
		if err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
		ids[i] = id
	}
	return s, ids[0], ids[1], ids[2]
}

func agentMapContractRows(t *testing.T, s *Store) []JobMessage {
	t.Helper()
	rows, err := s.ListJobMessages()
	if err != nil {
		t.Fatalf("ListJobMessages: %v", err)
	}
	return rows
}

// G4: "Schema v40 adds job_messages".
func TestAgentMapContractSchemaV40(t *testing.T) {
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 40 {
		t.Fatalf("user_version = %d, want at least 40", version)
	}
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(job_messages)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, c := range []string{"from_id", "to_id", "n", "first_at", "last_at"} {
		if !cols[c] {
			t.Errorf("job_messages lacks column %q (has %v)", c, cols)
		}
	}
	if got := agentMapContractRows(t, s); len(got) != 0 {
		t.Fatalf("fresh store rows = %+v, want none", got)
	}
}

// G4: the first call stores N 1 with both stamps; each later call adds 1 and
// moves LastAt only.
func TestAgentMapContractBumpJobMessage(t *testing.T) {
	s, a, b, _ := agentMapContractSeed(t)
	if err := s.BumpJobMessage(a, b, 100); err != nil {
		t.Fatalf("bump 1: %v", err)
	}
	rows := agentMapContractRows(t, s)
	if len(rows) != 1 || rows[0] != (JobMessage{FromID: a, ToID: b, N: 1, FirstAt: 100, LastAt: 100}) {
		t.Fatalf("after one bump rows = %+v", rows)
	}
	if err := s.BumpJobMessage(a, b, 250); err != nil {
		t.Fatalf("bump 2: %v", err)
	}
	if err := s.BumpJobMessage(a, b, 400); err != nil {
		t.Fatalf("bump 3: %v", err)
	}
	rows = agentMapContractRows(t, s)
	if len(rows) != 1 || rows[0] != (JobMessage{FromID: a, ToID: b, N: 3, FirstAt: 100, LastAt: 400}) {
		t.Fatalf("after three bumps rows = %+v, want N 3 first 100 last 400", rows)
	}
}

// G4: "(a, b) and (b, a) are separate rows."
func TestAgentMapContractBumpJobMessageDirections(t *testing.T) {
	s, a, b, _ := agentMapContractSeed(t)
	if err := s.BumpJobMessage(a, b, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpJobMessage(b, a, 20); err != nil {
		t.Fatal(err)
	}
	rows := agentMapContractRows(t, s)
	want := []JobMessage{
		{FromID: a, ToID: b, N: 1, FirstAt: 10, LastAt: 10},
		{FromID: b, ToID: a, N: 1, FirstAt: 20, LastAt: 20},
	}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
}

// G4: "Every row, ordered by LastAt then FromID."
func TestAgentMapContractListJobMessagesOrder(t *testing.T) {
	s, a, b, c := agentMapContractSeed(t)
	// c->a last at 300, b->c last at 100, a->c and b->a tie at 200.
	for _, bump := range []struct{ from, to, at int64 }{
		{c, a, 50}, {c, a, 300},
		{b, c, 100},
		{b, a, 200},
		{a, c, 200},
	} {
		if err := s.BumpJobMessage(bump.from, bump.to, bump.at); err != nil {
			t.Fatal(err)
		}
	}
	rows := agentMapContractRows(t, s)
	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want 4", rows)
	}
	type pair struct{ from, to int64 }
	got := make([]pair, len(rows))
	for i, r := range rows {
		got[i] = pair{r.FromID, r.ToID}
	}
	// a < b < c by creation order, so the 200 tie lists a->c before b->a
	want := []pair{{b, c}, {a, c}, {b, a}, {c, a}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %+v, want %+v", got, want)
		}
	}
	if rows[3].FirstAt != 50 || rows[3].LastAt != 300 || rows[3].N != 2 {
		t.Fatalf("c->a row = %+v, want N 2 first 50 last 300", rows[3])
	}
}

// G4: "Changes neither jobs.rev nor jobs.updated_at of either job."
func TestAgentMapContractBumpJobMessageLeavesRevAlone(t *testing.T) {
	s, a, b, _ := agentMapContractSeed(t)
	snap := func(id int64) (int64, int64) {
		j, err := s.GetJobByID(id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		return j.Rev, j.UpdatedAt
	}
	aRev, aUpd := snap(a)
	bRev, bUpd := snap(b)
	if err := s.BumpJobMessage(a, b, 1_800_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpJobMessage(a, b, 1_800_000_000_500); err != nil {
		t.Fatal(err)
	}
	if r, u := snap(a); r != aRev || u != aUpd {
		t.Fatalf("sender rev/updated_at moved: %d/%d -> %d/%d", aRev, aUpd, r, u)
	}
	if r, u := snap(b); r != bRev || u != bUpd {
		t.Fatalf("receiver rev/updated_at moved: %d/%d -> %d/%d", bRev, bUpd, r, u)
	}
}

// G4: "Deleting a job forever removes every job_messages row naming it on
// either side. Archiving and unarchiving keep the rows."
func TestAgentMapContractDeleteJobRemovesMessages(t *testing.T) {
	s, a, b, c := agentMapContractSeed(t)
	for _, bump := range []struct{ from, to, at int64 }{{a, b, 1}, {b, a, 2}, {c, b, 3}, {a, c, 4}} {
		if err := s.BumpJobMessage(bump.from, bump.to, bump.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ArchiveJob(b); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if rows := agentMapContractRows(t, s); len(rows) != 4 {
		t.Fatalf("rows after archive = %+v, want all 4 kept", rows)
	}
	if err := s.UnarchiveJob(b); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if rows := agentMapContractRows(t, s); len(rows) != 4 {
		t.Fatalf("rows after unarchive = %+v, want all 4 kept", rows)
	}
	if _, err := s.ArchiveJob(b); err != nil {
		t.Fatalf("archive again: %v", err)
	}
	if err := s.DeleteJob(b); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows := agentMapContractRows(t, s)
	if len(rows) != 1 || rows[0].FromID != a || rows[0].ToID != c {
		t.Fatalf("rows after deleting b = %+v, want only a->c", rows)
	}
}
