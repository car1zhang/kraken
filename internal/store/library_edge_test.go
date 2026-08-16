package store

import (
	"bytes"
	"errors"
	"testing"
)

// library_edge_test.go — the EDGE dimension of 50-contract.md's Slice A store
// obligations (the `saved_artifacts` table and its four accessors): the
// refusals, the boundaries, and the survival promise. Authored blind from the
// frozen contract, with no sight of internal/store/saved.go or schema.go.
//
// The sibling blind author (athena-logic) owns the stated happy paths — a save
// round-trips, a list serves newest-first, the four accessors agree. What lives
// here is the other half: what the store must REFUSE (a delete or a get of a row
// that is not there), what must NOT change under a re-save (the row id), what
// must NOT collapse (two jobs holding the same artifact name), and the one
// promise no other table in this database makes — a saved row outliving a
// `DELETE FROM jobs` of the job it came from.
//
// Every package-level identifier is prefixed `edgeLib`: this package's test
// namespace is shared with the sibling blind file written in the same round, and
// a bare `edge`/`edgeGoal` prefix is already spoken for elsewhere in the tree.
//
// Isolation: openTest(t) (store_test.go) is CALLED, never redeclared — it opens
// a fresh DB under t.TempDir(). Nothing here reads HOME or ~/.kraken.

// edgeLibNow is a fixed unix-millis stamp for saves whose timestamp is not what
// the case is about — a constant rather than time.Now() so a failure message
// never depends on when the suite ran.
const edgeLibNow int64 = 1_770_000_000_000

// edgeLibJob seeds a job row and returns its immutable id. saved_artifacts
// carries no foreign key to jobs, but a save still names a real job in every
// case here except the ones deliberately about a dangling id.
func edgeLibJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug})
	if err != nil {
		t.Fatalf("CreateJob(%s): %v", slug, err)
	}
	return id
}

// edgeLibSave is SaveArtifact with the test's fatal-on-error boilerplate folded
// in, returning the row id the contract says is stable across re-saves.
func edgeLibSave(t *testing.T, s *Store, jobID int64, slug, name string, snapshot []byte, savedAt int64) int64 {
	t.Helper()
	id, err := s.SaveArtifact(jobID, slug, name, snapshot, savedAt)
	if err != nil {
		t.Fatalf("SaveArtifact(job %d, %q): %v", jobID, name, err)
	}
	if id <= 0 {
		t.Fatalf("SaveArtifact(job %d, %q) returned row id %d — a save must return the row's id", jobID, name, id)
	}
	return id
}

// edgeLibList is ListSavedArtifacts with its error checked, so every caller
// below reads as an assertion about ORDER or MEMBERSHIP rather than about
// plumbing.
func edgeLibList(t *testing.T, s *Store) []SavedArtifactMeta {
	t.Helper()
	got, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatalf("ListSavedArtifacts: %v", err)
	}
	return got
}

// edgeLibIDs projects a meta list onto its row ids — the only projection the
// ordering assertions need, and the one that makes a failure message readable.
func edgeLibIDs(metas []SavedArtifactMeta) []int64 {
	out := make([]int64, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.ID)
	}
	return out
}

// ---------------------------------------------------------------------------
// the empty table
// ---------------------------------------------------------------------------

// Obligation: "ListSavedArtifacts ... empty table → empty slice, nil error."
//
// This is the state every install is in until the first save, and therefore the
// first thing GET /api/library ever renders. An error here would make an empty
// library indistinguishable from a broken one; the route would have to answer
// 500 for the ordinary case.
func TestEdgeLibListOnEmptyTableIsEmptyWithNoError(t *testing.T) {
	s := openTest(t)
	got, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatalf("ListSavedArtifacts on an empty table = %v, want nil error", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListSavedArtifacts on an empty table = %+v, want an empty list", got)
	}
}

// TestEdgeLibListIsEmptyAgainAfterTheLastRowGoes is the same state reached the
// way production reaches it — carl removes the only saved item — rather than at
// a fresh install. A list path that only handled "never written" would be
// exercised on day one and never again.
func TestEdgeLibListIsEmptyAgainAfterTheLastRowGoes(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "the-only-one")
	id := edgeLibSave(t, s, jobID, "the-only-one", "00-overview.md", []byte("# hi"), edgeLibNow)
	if err := s.DeleteSavedArtifact(id); err != nil {
		t.Fatalf("DeleteSavedArtifact: %v", err)
	}
	got, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatalf("ListSavedArtifacts after the last delete = %v, want nil error", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListSavedArtifacts after the last delete = %+v, want an empty list", got)
	}
}

// ---------------------------------------------------------------------------
// DeleteSavedArtifact: the refusal
// ---------------------------------------------------------------------------

// Obligation: "DeleteSavedArtifact ... store.ErrNotFound when no row was
// deleted; nil when one was."
//
// The sentinel is the whole point: POST /api/library/remove maps it to a 404,
// and a silent no-op would answer 200 for a row that is not there — telling a
// client the removal it asked for happened when nothing did. Ids 0 and -1 are
// included because they are what an absent or malformed JSON `id` decodes to,
// which is the way a bad id actually reaches this method.
func TestEdgeLibDeleteMissingIDIsErrNotFound(t *testing.T) {
	s := openTest(t)
	for _, id := range []int64{0, -1, 1, 987654, 9223372036854775807} {
		if err := s.DeleteSavedArtifact(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteSavedArtifact(%d) = %v, want ErrNotFound", id, err)
		}
	}
}

// TestEdgeLibDeleteTwiceIsErrNotFoundTheSecondTime pins that the sentinel is
// about the ROW rather than about the id's shape: the same id that succeeded a
// moment ago must refuse once its row is gone. A double-click on the board's
// remove button is exactly this sequence.
func TestEdgeLibDeleteTwiceIsErrNotFoundTheSecondTime(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "delete-twice")
	id := edgeLibSave(t, s, jobID, "delete-twice", "50-components.md", []byte("components"), edgeLibNow)

	if err := s.DeleteSavedArtifact(id); err != nil {
		t.Fatalf("first DeleteSavedArtifact(%d) = %v, want nil", id, err)
	}
	if err := s.DeleteSavedArtifact(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteSavedArtifact(%d) = %v, want ErrNotFound", id, err)
	}
}

// TestEdgeLibDeleteRemovesExactlyOneRow is the blast-radius half of the same
// method. A delete keyed on anything looser than the row id — the job, the name
// — would take siblings with it, and the caller would see one card vanish and
// several disappear.
func TestEdgeLibDeleteRemovesExactlyOneRow(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "many")
	keepA := edgeLibSave(t, s, jobID, "many", "00-overview.md", []byte("a"), edgeLibNow)
	drop := edgeLibSave(t, s, jobID, "many", "50-components.md", []byte("b"), edgeLibNow+1)
	keepB := edgeLibSave(t, s, jobID, "many", "90-build-report.md", []byte("c"), edgeLibNow+2)

	if err := s.DeleteSavedArtifact(drop); err != nil {
		t.Fatalf("DeleteSavedArtifact: %v", err)
	}
	got := edgeLibList(t, s)
	if len(got) != 2 {
		t.Fatalf("after deleting one of three rows the list holds %d: %+v", len(got), got)
	}
	for _, id := range []int64{keepA, keepB} {
		if _, err := s.GetSavedArtifact(id); err != nil {
			t.Errorf("GetSavedArtifact(%d) after deleting a SIBLING row = %v, want the row", id, err)
		}
	}
	if _, err := s.GetSavedArtifact(drop); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSavedArtifact(%d) on the deleted row = %v, want ErrNotFound", drop, err)
	}
}

// ---------------------------------------------------------------------------
// GetSavedArtifact: the refusal
// ---------------------------------------------------------------------------

// Obligation: "GetSavedArtifact ... store.ErrNotFound on miss."
//
// The bytes route is `GET /api/library/artifact/<id>/<name>` and its stated
// answer for an unknown id is 404, which the handler can only produce if the
// miss is distinguishable from a real store failure.
func TestEdgeLibGetMissingIDIsErrNotFound(t *testing.T) {
	s := openTest(t)
	for _, id := range []int64{0, -1, 1, 987654} {
		got, err := s.GetSavedArtifact(id)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSavedArtifact(%d) error = %v, want ErrNotFound", id, err)
		}
		if got != nil {
			t.Errorf("GetSavedArtifact(%d) returned a row alongside its error: %+v", id, got)
		}
	}
}

// ---------------------------------------------------------------------------
// SaveArtifact: the upsert
// ---------------------------------------------------------------------------

// Obligation: "Upsert by (job_id, name); re-save with the same pair replaces
// snapshot/slug/saved_at and RETURNS THE SAME row id; returns the row id either
// way", plus the table's own `UNIQUE(job_id, name)`.
//
// The stable id is what makes the whole feature coherent: the served `url`
// embeds it, so a re-save that minted a fresh id would strand every url already
// on a phone screen and leave the old row listed beside the new one.
func TestEdgeLibResaveKeepsTheRowIDAndReplacesTheSnapshot(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "resave")

	first := edgeLibSave(t, s, jobID, "resave", "00-overview.md", []byte("the first draft"), edgeLibNow)
	second := edgeLibSave(t, s, jobID, "resave-renamed", "00-overview.md", []byte("the second draft"), edgeLibNow+5000)

	if second != first {
		t.Fatalf("re-save returned row id %d, want the original %d — the id is embedded in every served url", second, first)
	}
	if got := edgeLibList(t, s); len(got) != 1 {
		t.Fatalf("re-save left %d rows, want 1: %+v", len(got), got)
	}

	row, err := s.GetSavedArtifact(first)
	if err != nil {
		t.Fatalf("GetSavedArtifact after re-save: %v", err)
	}
	if !bytes.Equal(row.Snapshot, []byte("the second draft")) {
		t.Errorf("snapshot = %q, want the re-saved bytes %q", row.Snapshot, "the second draft")
	}
	if row.SavedAt != edgeLibNow+5000 {
		t.Errorf("saved_at = %d, want the re-save's stamp %d", row.SavedAt, edgeLibNow+5000)
	}
	if row.Slug != "resave-renamed" {
		t.Errorf("slug = %q, want the re-save's display slug %q", row.Slug, "resave-renamed")
	}
	if row.Name != "00-overview.md" {
		t.Errorf("name = %q, want %q — the name is the upsert key and never changes under it", row.Name, "00-overview.md")
	}
	if row.JobID != jobID {
		t.Errorf("job_id = %d, want %d", row.JobID, jobID)
	}
}

// TestEdgeLibResaveAcceptsAnOlderStamp is the boundary the word "replaces"
// draws. saved_at is the caller's number, not a monotonic clock, and the
// contract says a re-save REPLACES it — so an earlier stamp must land rather
// than be quietly kept at the larger value. A max() would make the list order
// disagree with what the row actually says.
func TestEdgeLibResaveAcceptsAnOlderStamp(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "older-stamp")

	id := edgeLibSave(t, s, jobID, "older-stamp", "10-architecture.html", []byte("<p>v1</p>"), edgeLibNow)
	edgeLibSave(t, s, jobID, "older-stamp", "10-architecture.html", []byte("<p>v2</p>"), edgeLibNow-90_000)

	row, err := s.GetSavedArtifact(id)
	if err != nil {
		t.Fatalf("GetSavedArtifact: %v", err)
	}
	if row.SavedAt != edgeLibNow-90_000 {
		t.Errorf("saved_at = %d, want the re-save's own (earlier) stamp %d — a re-save replaces it", row.SavedAt, edgeLibNow-90_000)
	}
	if !bytes.Equal(row.Snapshot, []byte("<p>v2</p>")) {
		t.Errorf("snapshot = %q, want the re-saved bytes", row.Snapshot)
	}
}

// TestEdgeLibUniquenessIsThePairNotTheName pins that `UNIQUE(job_id, name)` is a
// PAIR. Artifact names are a house convention — every job in the corpus has an
// `00-overview.md` — so a uniqueness rule on the name alone would let one job's
// save silently overwrite another's, which is data loss disguised as an upsert.
func TestEdgeLibUniquenessIsThePairNotTheName(t *testing.T) {
	s := openTest(t)
	jobA := edgeLibJob(t, s, "job-a")
	jobB := edgeLibJob(t, s, "job-b")

	idA := edgeLibSave(t, s, jobA, "job-a", "00-overview.md", []byte("a's overview"), edgeLibNow)
	idB := edgeLibSave(t, s, jobB, "job-b", "00-overview.md", []byte("b's overview"), edgeLibNow+1)

	if idA == idB {
		t.Fatalf("two jobs saving the same artifact NAME collapsed onto row id %d", idA)
	}
	if got := edgeLibList(t, s); len(got) != 2 {
		t.Fatalf("want 2 rows for the same name under two jobs, got %d: %+v", len(got), got)
	}
	rowA, err := s.GetSavedArtifact(idA)
	if err != nil {
		t.Fatalf("GetSavedArtifact(a): %v", err)
	}
	if !bytes.Equal(rowA.Snapshot, []byte("a's overview")) {
		t.Errorf("job A's snapshot = %q — job B's save overwrote it", rowA.Snapshot)
	}
}

// TestEdgeLibNameKeepsItsSlashes covers the nested store spelling. The name is
// the store PATH ("nested/diagram.svg"), it is what the served url's last
// segment carries "with slashes intact", and it is the upsert key — so any
// normalization (a basename, a cleaned path, a stripped leading slash) would
// both mis-key the row and break the url the clients match on.
func TestEdgeLibNameKeepsItsSlashes(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "nested")

	const nested = "icons/icon-variations/v2.png"
	id := edgeLibSave(t, s, jobID, "nested", nested, []byte("\x89PNG"), edgeLibNow)

	row, err := s.GetSavedArtifact(id)
	if err != nil {
		t.Fatalf("GetSavedArtifact: %v", err)
	}
	if row.Name != nested {
		t.Errorf("name = %q, want the store spelling %q byte-exact", row.Name, nested)
	}

	// And the flat leaf of that same path is a DIFFERENT artifact, not the same
	// one reached by another spelling.
	flat := edgeLibSave(t, s, jobID, "nested", "v2.png", []byte("a different png"), edgeLibNow+1)
	if flat == id {
		t.Fatalf("%q and %q collapsed onto one row (%d) — the name is a path, not a basename", nested, "v2.png", id)
	}
	metas := edgeLibList(t, s)
	if len(metas) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(metas), metas)
	}
}

// ---------------------------------------------------------------------------
// ListSavedArtifacts: the ordering tie-break
// ---------------------------------------------------------------------------

// Obligation: "saved_at DESC, ties id DESC."
//
// The tie is not exotic: `saved_at` is unix MILLIS and a client can save two
// artifacts inside one millisecond, while a test seeds them with a literal
// stamp. Without the second key the order of a tie is whatever sqlite returns,
// which makes both clients' "newest first" list unstable between polls for rows
// they render side by side.
func TestEdgeLibListTieBreaksOnIDDescending(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "ties")

	// Three rows sharing one stamp — insertion order first, second, third.
	first := edgeLibSave(t, s, jobID, "ties", "a.md", []byte("a"), edgeLibNow)
	second := edgeLibSave(t, s, jobID, "ties", "b.md", []byte("b"), edgeLibNow)
	third := edgeLibSave(t, s, jobID, "ties", "c.md", []byte("c"), edgeLibNow)

	want := []int64{third, second, first}
	got := edgeLibIDs(edgeLibList(t, s))
	if len(got) != len(want) {
		t.Fatalf("list holds %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tie order = %v, want %v (equal saved_at breaks on id DESC — newest insert first)", got, want)
		}
	}
}

// TestEdgeLibListSortsBySavedAtBeforeID is the other half of the same rule: the
// stamp is the PRIMARY key of the order, so a row saved later but inserted
// earlier still sorts first. The fixture inverts the two keys deliberately —
// with an id-only sort it would come out exactly backwards.
func TestEdgeLibListSortsBySavedAtBeforeID(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "stamps")

	newest := edgeLibSave(t, s, jobID, "stamps", "a.md", []byte("a"), edgeLibNow+10_000)
	middle := edgeLibSave(t, s, jobID, "stamps", "b.md", []byte("b"), edgeLibNow+5_000)
	oldest := edgeLibSave(t, s, jobID, "stamps", "c.md", []byte("c"), edgeLibNow)

	want := []int64{newest, middle, oldest}
	got := edgeLibIDs(edgeLibList(t, s))
	if len(got) != 3 {
		t.Fatalf("list holds %d rows, want 3: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (saved_at DESC outranks the id tie-break)", got, want)
		}
	}
}

// TestEdgeLibResaveMovesARowToTheFrontOfTheList is the ordering rule meeting the
// upsert rule. A re-save keeps the row id — which under an id-ordered list would
// leave a just-saved artifact wherever it was first inserted — so the stamp is
// what has to move it. This is the case carl sees: re-save an artifact and it
// must appear at the top.
func TestEdgeLibResaveMovesARowToTheFrontOfTheList(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "refresh")

	stale := edgeLibSave(t, s, jobID, "refresh", "a.md", []byte("a"), edgeLibNow)
	fresh := edgeLibSave(t, s, jobID, "refresh", "b.md", []byte("b"), edgeLibNow+1_000)
	if got := edgeLibIDs(edgeLibList(t, s)); got[0] != fresh {
		t.Fatalf("pre-condition: order = %v, want %d first", got, fresh)
	}

	edgeLibSave(t, s, jobID, "refresh", "a.md", []byte("a, rewritten"), edgeLibNow+2_000)
	got := edgeLibIDs(edgeLibList(t, s))
	if len(got) != 2 || got[0] != stale {
		t.Fatalf("after re-saving the older row, order = %v, want the re-saved row (%d) first", got, stale)
	}
}

// ---------------------------------------------------------------------------
// SavedArtifactMeta.Size
// ---------------------------------------------------------------------------

// Obligation: "SavedArtifactMeta ... Size is LENGTH(snapshot)", and
// "ListSavedArtifacts ... never reads the snapshot column (LENGTH only)".
//
// The edge is the unit. `snapshot` is a BLOB, so LENGTH() counts BYTES — and a
// meta list computed in Go from a decoded string, or from a TEXT-typed column,
// would count characters instead and under-report every artifact carrying a
// non-ASCII byte, which for this corpus is most of them (em dashes, arrows,
// box-drawing). The 0-byte row is included because it is the one length that a
// "did we read anything" check and a real length disagree about.
func TestEdgeLibMetaSizeCountsBytesNotRunes(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "sizes")

	// Three 3-byte characters among ASCII, so the byte count and the rune
	// count differ by six — the want below is computed from len() rather than
	// written out, so the case states the RULE and not an arithmetic claim.
	multibyte := []byte("kraken — zeus → athena ✓ ok")
	wide := edgeLibSave(t, s, jobID, "sizes", "wide.md", multibyte, edgeLibNow)
	ascii := edgeLibSave(t, s, jobID, "sizes", "plain.md", []byte("plain ascii"), edgeLibNow+1)

	want := map[int64]int64{
		wide:  int64(len(multibyte)),
		ascii: int64(len("plain ascii")),
	}
	for _, m := range edgeLibList(t, s) {
		w, ok := want[m.ID]
		if !ok {
			t.Errorf("unexpected row %d in the list", m.ID)
			continue
		}
		if m.Size != w {
			t.Errorf("row %d Size = %d, want %d bytes", m.ID, m.Size, w)
		}
	}
}

// TestEdgeLibMetaCarriesTheRowsIdentity pins the four identity fields the meta
// form must carry alongside Size: the served list is built entirely from this
// struct, so a field left zero here is a key that arrives empty on the wire.
func TestEdgeLibMetaCarriesTheRowsIdentity(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "identity")
	id := edgeLibSave(t, s, jobID, "identity", "nested/diagram.svg", []byte("<svg/>"), edgeLibNow)

	metas := edgeLibList(t, s)
	if len(metas) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(metas), metas)
	}
	m := metas[0]
	if m.ID != id {
		t.Errorf("meta ID = %d, want %d", m.ID, id)
	}
	if m.JobID != jobID {
		t.Errorf("meta JobID = %d, want %d", m.JobID, jobID)
	}
	if m.Slug != "identity" {
		t.Errorf("meta Slug = %q, want %q", m.Slug, "identity")
	}
	if m.Name != "nested/diagram.svg" {
		t.Errorf("meta Name = %q, want the store spelling with its slash", m.Name)
	}
	if m.SavedAt != edgeLibNow {
		t.Errorf("meta SavedAt = %d, want %d", m.SavedAt, edgeLibNow)
	}
}

// ---------------------------------------------------------------------------
// survival: no foreign key, no cascade
// ---------------------------------------------------------------------------

// Obligation: "A saved row and its snapshot remain listable and readable
// BYTE-IDENTICAL after `DELETE FROM jobs WHERE id = <job_id>` on the same store
// (the hard-delete case)", from the table's "NO foreign key and NO cascade".
//
// This is the feature's whole reason to exist, and the edge framing is what a
// cascade would actually do: the row does not merely survive with a nulled
// pointer, it survives WHOLE — the dangling job_id included, since that id is
// the only provenance the row has left once the job is gone. `foreign_keys` is
// ON in this database (store_test.go's TestPragmasApplied), so a declared FK
// here would delete these rows rather than error, silently.
func TestEdgeLibRowSurvivesItsJobsHardDelete(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "doomed")

	snapshot := []byte("# the frozen bytes\n\nwith a — dash and a trailing newline\n")
	id := edgeLibSave(t, s, jobID, "doomed", "00-overview.md", snapshot, edgeLibNow)

	if err := s.DeleteJob(jobID); err != nil {
		t.Fatalf("DeleteJob: %v — the hard delete must not be blocked by the saved row", err)
	}
	if _, err := s.GetJobByID(jobID); err == nil {
		t.Fatalf("pre-condition: job %d still readable after DeleteJob", jobID)
	}

	metas := edgeLibList(t, s)
	if len(metas) != 1 || metas[0].ID != id {
		t.Fatalf("saved row not listable after its job's hard delete: %+v", metas)
	}
	if metas[0].JobID != jobID {
		t.Errorf("meta JobID = %d after the hard delete, want the dangling %d — no cascade, and no nulling either", metas[0].JobID, jobID)
	}
	if metas[0].Slug != "doomed" {
		t.Errorf("meta Slug = %q, want %q — the display slug is frozen at save time and outlives the job row", metas[0].Slug, "doomed")
	}

	row, err := s.GetSavedArtifact(id)
	if err != nil {
		t.Fatalf("GetSavedArtifact after the source job's hard delete: %v", err)
	}
	if !bytes.Equal(row.Snapshot, snapshot) {
		t.Errorf("snapshot = %q, want the frozen bytes byte-identical", row.Snapshot)
	}
	if row.SavedAt != edgeLibNow {
		t.Errorf("saved_at = %d, want %d", row.SavedAt, edgeLibNow)
	}
}

// TestEdgeLibSaveAcceptsAJobIDWithNoRow is the same "no foreign key" property
// approached from the write side. It is reachable in production, not
// hypothetical: nothing serializes a save against a concurrent hard delete, and
// a save that errored here would surface as a 500 on a button press. It is also
// the exact write a declared FK would refuse.
func TestEdgeLibSaveAcceptsAJobIDWithNoRow(t *testing.T) {
	s := openTest(t)
	const orphan int64 = 424242

	id := edgeLibSave(t, s, orphan, "gone", "00-overview.md", []byte("orphaned bytes"), edgeLibNow)
	row, err := s.GetSavedArtifact(id)
	if err != nil {
		t.Fatalf("GetSavedArtifact: %v", err)
	}
	if row.JobID != orphan {
		t.Errorf("job_id = %d, want the dangling %d as given", row.JobID, orphan)
	}
	if !bytes.Equal(row.Snapshot, []byte("orphaned bytes")) {
		t.Errorf("snapshot = %q", row.Snapshot)
	}
}

// TestEdgeLibSurvivesTheWholeDeathMarch walks the four destructive steps in the
// order a real job takes them — save, archive, unarchive is skipped, hard delete
// — asserting the row is untouched by each. Each step alone is covered above;
// the sequence is here because the failure it guards against is cumulative (an
// archive that re-keys the row, then a delete that finds nothing to preserve).
func TestEdgeLibSurvivesTheWholeDeathMarch(t *testing.T) {
	s := openTest(t)
	jobID := edgeLibJob(t, s, "march")
	snapshot := []byte("frozen at save time, and never again")
	id := edgeLibSave(t, s, jobID, "march", "90-build-report.md", snapshot, edgeLibNow)

	if _, err := s.ArchiveJob(jobID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if row, err := s.GetSavedArtifact(id); err != nil {
		t.Fatalf("GetSavedArtifact after archive: %v", err)
	} else if !bytes.Equal(row.Snapshot, snapshot) {
		t.Errorf("snapshot changed under archive: %q", row.Snapshot)
	}

	if err := s.DeleteJob(jobID); err != nil {
		t.Fatalf("DeleteJob after archive: %v", err)
	}
	row, err := s.GetSavedArtifact(id)
	if err != nil {
		t.Fatalf("GetSavedArtifact after the hard delete: %v", err)
	}
	if !bytes.Equal(row.Snapshot, snapshot) {
		t.Errorf("snapshot = %q, want the frozen bytes byte-identical after archive + hard delete", row.Snapshot)
	}
	if got := edgeLibList(t, s); len(got) != 1 || got[0].ID != id {
		t.Errorf("list after the death march = %+v, want the one saved row", got)
	}
}
