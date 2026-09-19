package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mm_edge_test.go — the EDGE dimension of 50-contract.md's slice S1 store half:
// what `mm_items`, `mm_code_anchors` and the REWORKED `watch_items` REFUSE, what
// they leave alone, and the two one-way doors (invalidate, expire) that must
// never swing back. Written blind from the contract text alone — neither
// internal/store/mm.go, schema.go nor watch.go was read.
//
// The sibling blind author (athena-logic) owns the round trips and the stated
// invariants. What lives here is the other half: every validation arm, every
// ErrNotFound, the already-invalidated/already-expired successes, and the
// migration rows that must NOT be carried across.
//
// Every package-level identifier is prefixed `edgeMM`. This package's test
// namespace is shared with the sibling blind author AND with the existing
// watch_edge_test.go/anchor_edge_test.go, which already own the shorter `edge…`
// names (edgeStore, edgeOneLine, edgeLiveEdge, edgeWatchRowCount).

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// edgeMMOpen opens a throwaway store with HOME isolated. HOME is sandboxed even
// though the store is addressed by an explicit path: an accidental fallthrough
// to a home-relative default must land in a temp dir rather than in the real
// ~/.kraken/kraken.db, and the isolation is the TEST's obligation rather than an
// assumption about the machine running it.
func edgeMMOpen(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const edgeMMNow int64 = 1_770_000_000_000

// edgeMMValid is the smallest item AddMMItem must accept — every refusal case
// below is this value with exactly ONE field broken, so a test that fails is
// naming the field it broke and nothing else.
func edgeMMValid() MMItem {
	return MMItem{
		Kind:      "note",
		Source:    "manual",
		Content:   "the thing carl knows",
		CreatedAt: edgeMMNow,
	}
}

func edgeMMAdd(t *testing.T, s *Store, it MMItem) int64 {
	t.Helper()
	id, err := s.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	if id <= 0 {
		t.Fatalf("AddMMItem returned id %d, want a real row id", id)
	}
	return id
}

func edgeMMGet(t *testing.T, s *Store, id int64) MMItem {
	t.Helper()
	it, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

// edgeMMValidAnchor is the smallest anchor AddMMCodeAnchor must accept, for the
// same one-broken-field discipline as edgeMMValid.
func edgeMMValidAnchor(itemID int64) MMCodeAnchor {
	return MMCodeAnchor{
		ItemID:     itemID,
		Repo:       "kraken",
		Path:       "internal/core/anchor.go",
		StartLine:  10,
		EndLine:    12,
		AnchorSHA:  "0f1e2d3c4b5a69788796a5b4c3d2e1f009182736",
		AnchorKind: "origin-main",
		Text:       "if err != nil {\n\treturn err\n}",
		TextSHA:    "9a8b7c6d5e4f30211203f4e5d6c7b8a900112233445566778899aabbccddeeff",
		Lang:       "go",
	}
}

func edgeMMAnchor(t *testing.T, s *Store, a MMCodeAnchor) {
	t.Helper()
	if err := s.AddMMCodeAnchor(a); err != nil {
		t.Fatalf("AddMMCodeAnchor(%+v): %v", a, err)
	}
}

func edgeMMReadAnchor(t *testing.T, s *Store, itemID int64) MMCodeAnchor {
	t.Helper()
	a, ok, err := s.GetMMCodeAnchor(itemID)
	if err != nil {
		t.Fatalf("GetMMCodeAnchor(%d): %v", itemID, err)
	}
	if !ok {
		t.Fatalf("GetMMCodeAnchor(%d): no anchor", itemID)
	}
	return a
}

// ---------------------------------------------------------------------------
// AddMMItem — the closed sets
// ---------------------------------------------------------------------------

// TestEdgeMMAddMMItemRefusesKindOutsideTheClosedSet walks the kinds that are not
// one of the three. The board filters and the episode render both switch on this
// column, so a fourth value would render as neither a decision nor a reading nor
// a note — an item carl marked that shows up nowhere.
func TestEdgeMMAddMMItemRefusesKindOutsideTheClosedSet(t *testing.T) {
	s := edgeMMOpen(t)
	for _, kind := range []string{"", "Decision", "DECISION", "fact", "note ", " note", "readings", "mental-model"} {
		it := edgeMMValid()
		it.Kind = kind
		if _, err := s.AddMMItem(it); err == nil {
			t.Errorf("AddMMItem with kind %q succeeded, want a refusal", kind)
		}
	}
	edgeMMRequireEmpty(t, s)
}

// TestEdgeMMAddMMItemRefusesSourceOutsideTheClosedSet is the same shape for the
// six sources. `artifact-selection` is in the list deliberately: the components
// doc spells the element source that way and the frozen contract spells it
// `artifact-element`, so the near-miss is the value a reader of the wrong
// document would send, and accepting it would give the verify pass a source it
// has no arm for — an item that can never be invalidated.
func TestEdgeMMAddMMItemRefusesSourceOutsideTheClosedSet(t *testing.T) {
	s := edgeMMOpen(t)
	for _, source := range []string{"", "artifact-selection", "selection", "Question", "CODE", "code ", "agent_output", "zeus"} {
		it := edgeMMValid()
		it.Source = source
		if _, err := s.AddMMItem(it); err == nil {
			t.Errorf("AddMMItem with source %q succeeded, want a refusal", source)
		}
	}
	edgeMMRequireEmpty(t, s)
}

// TestEdgeMMAddMMItemRefusesEmptyContent covers the empty payload. Content IS
// the item — it is what the episode renders, what the artifact-element verify
// substring-searches for, and what the board shows for an invalidated row — so
// an empty one is a row that can never say anything and can never be verified.
func TestEdgeMMAddMMItemRefusesEmptyContent(t *testing.T) {
	s := edgeMMOpen(t)
	it := edgeMMValid()
	it.Content = ""
	if _, err := s.AddMMItem(it); err == nil {
		t.Error("AddMMItem with empty Content succeeded, want a refusal")
	}
	edgeMMRequireEmpty(t, s)
}

// TestEdgeMMAddMMItemRefusesNonPositiveCreatedAt covers both halves of
// `CreatedAt > 0`: the zero a caller that forgot to stamp sends, and a negative
// one. created_at is the list's primary sort key and the episode's ts, so a zero
// would file every unstamped item at the bottom of the board forever.
func TestEdgeMMAddMMItemRefusesNonPositiveCreatedAt(t *testing.T) {
	s := edgeMMOpen(t)
	for _, at := range []int64{0, -1, -edgeMMNow} {
		it := edgeMMValid()
		it.CreatedAt = at
		if _, err := s.AddMMItem(it); err == nil {
			t.Errorf("AddMMItem with CreatedAt %d succeeded, want a refusal", at)
		}
	}
	edgeMMRequireEmpty(t, s)
}

// edgeMMRequireEmpty is the "a refusal writes nothing" half every validation
// case above shares: validate-first means a refused item leaves ZERO rows, which
// is also what makes MintMMEpisode's own validate-first rule reachable.
func edgeMMRequireEmpty(t *testing.T, s *Store) {
	t.Helper()
	items, err := s.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("a refused AddMMItem left %d rows behind: %+v", len(items), items)
	}
}

// ---------------------------------------------------------------------------
// lookups that miss
// ---------------------------------------------------------------------------

// TestEdgeMMGetMMItemUnknownIsErrNotFound covers the id that names no row,
// including the two ids a caller produces by accident: 0 (an unset variable) and
// a negative one. errors.Is rather than != nil, because the API layer maps this
// error and only this error to a 404.
func TestEdgeMMGetMMItemUnknownIsErrNotFound(t *testing.T) {
	s := edgeMMOpen(t)
	live := edgeMMAdd(t, s, edgeMMValid())
	for _, id := range []int64{0, -1, live + 1, 99999} {
		if _, err := s.GetMMItem(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetMMItem(%d) err = %v, want ErrNotFound", id, err)
		}
	}
}

// TestEdgeMMGetMMSnapshotMissingIsErrNotFound covers BOTH arms the contract
// names with one error: an id that is no row at all, and a real item that simply
// has no snapshot — every note, decision and code reading, which is most of the
// table. The route serves this as a 404, so the two must be indistinguishable to
// the caller.
func TestEdgeMMGetMMSnapshotMissingIsErrNotFound(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	for _, c := range []struct {
		name string
		id   int64
	}{
		{"unknown id", id + 100},
		{"zero id", 0},
		{"a real item with no snapshot", id},
	} {
		if _, _, err := s.GetMMSnapshot(c.id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: GetMMSnapshot(%d) err = %v, want ErrNotFound", c.name, c.id, err)
		}
	}
}

// TestEdgeMMGetMMCodeAnchorMissingIsNotAnError is the one lookup in the family
// that does NOT report ErrNotFound: the contract spells its miss as
// (zero, false, nil), because most items have no anchor and the pass asks this
// question about every one of them.
func TestEdgeMMGetMMCodeAnchorMissingIsNotAnError(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	for _, probe := range []int64{id, id + 50, 0} {
		a, ok, err := s.GetMMCodeAnchor(probe)
		if err != nil {
			t.Errorf("GetMMCodeAnchor(%d) err = %v, want nil", probe, err)
		}
		if ok {
			t.Errorf("GetMMCodeAnchor(%d) ok = true, want false", probe)
		}
		if a != (MMCodeAnchor{}) {
			t.Errorf("GetMMCodeAnchor(%d) returned %+v, want the zero value", probe, a)
		}
	}
}

// ---------------------------------------------------------------------------
// invalidation is one-way
// ---------------------------------------------------------------------------

// TestEdgeMMInvalidateMMItemUnknownIsErrNotFound is the contract's first arm.
func TestEdgeMMInvalidateMMItemUnknownIsErrNotFound(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	for _, probe := range []int64{0, -3, id + 7} {
		if err := s.InvalidateMMItem(probe, edgeMMNow, "removed", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("InvalidateMMItem(%d) err = %v, want ErrNotFound", probe, err)
		}
	}
}

// TestEdgeMMInvalidateMMItemIsOneWay is the door that must not swing back. The
// second call SUCCEEDS — the pass re-reaches a dead item and must not error —
// while leaving the FIRST reason, detail and timestamp in place: those three are
// the whole forensic record of what killed the item, and a later, vaguer cause
// overwriting a specific one loses it permanently. Both a later and an EARLIER
// timestamp are tried, since a re-verify can carry either.
func TestEdgeMMInvalidateMMItemIsOneWay(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())

	if err := s.InvalidateMMItem(id, edgeMMNow, "content-changed", "sha abc123"); err != nil {
		t.Fatalf("first InvalidateMMItem: %v", err)
	}
	first := edgeMMGet(t, s, id)
	if first.InvalidatedAt != edgeMMNow || first.InvalidReason != "content-changed" || first.InvalidDetail != "sha abc123" {
		t.Fatalf("after the first invalidate: at=%d reason=%q detail=%q", first.InvalidatedAt, first.InvalidReason, first.InvalidDetail)
	}

	for _, c := range []struct {
		at     int64
		reason string
		detail string
	}{
		{edgeMMNow + 60_000, "removed", "carl"},
		{edgeMMNow - 60_000, "artifact-deleted", ""},
	} {
		if err := s.InvalidateMMItem(id, c.at, c.reason, c.detail); err != nil {
			t.Fatalf("re-invalidate with %q: %v, want success", c.reason, err)
		}
		got := edgeMMGet(t, s, id)
		if got.InvalidatedAt != first.InvalidatedAt || got.InvalidReason != first.InvalidReason || got.InvalidDetail != first.InvalidDetail {
			t.Fatalf("re-invalidate with %q overwrote the record: at=%d reason=%q detail=%q, want at=%d reason=%q detail=%q",
				c.reason, got.InvalidatedAt, got.InvalidReason, got.InvalidDetail,
				first.InvalidatedAt, first.InvalidReason, first.InvalidDetail)
		}
	}
}

// TestEdgeMMInvalidateMMItemStampsAllThreeOnALiveRow is the positive half of the
// same door, kept here because the one-way test above is only meaningful if the
// first call actually writes all three columns — an implementation that stamped
// invalidated_at alone would pass "does not overwrite" trivially.
func TestEdgeMMInvalidateMMItemStampsAllThreeOnALiveRow(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	if got := edgeMMGet(t, s, id); got.InvalidatedAt != 0 {
		t.Fatalf("a fresh item has InvalidatedAt = %d, want 0 (live)", got.InvalidatedAt)
	}
	if err := s.InvalidateMMItem(id, edgeMMNow+5, "file-deleted", "internal/gone.go"); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	got := edgeMMGet(t, s, id)
	if got.InvalidatedAt != edgeMMNow+5 {
		t.Errorf("InvalidatedAt = %d, want %d", got.InvalidatedAt, edgeMMNow+5)
	}
	if got.InvalidReason != "file-deleted" || got.InvalidDetail != "internal/gone.go" {
		t.Errorf("reason/detail = %q/%q, want file-deleted/internal/gone.go", got.InvalidReason, got.InvalidDetail)
	}
}

// ---------------------------------------------------------------------------
// ListMMItems / FindLiveMMItem / SupersedeLiveMMItems
// ---------------------------------------------------------------------------

// TestEdgeMMListMMItemsLiveFilterAndTieBreak covers the filter's two states and
// the ORDER at its hardest point: three items sharing one created_at, where the
// only remaining key is `id DESC`. Marks arrive in bursts (a board session, a
// Zed keystroke run), so same-millisecond ties are the normal case rather than a
// contrived one, and an unstable order there makes the pane reshuffle on every
// 30s poll.
func TestEdgeMMListMMItemsLiveFilterAndTieBreak(t *testing.T) {
	s := edgeMMOpen(t)
	var ids []int64
	for i := 0; i < 3; i++ {
		it := edgeMMValid()
		it.Content = strings.Repeat("x", i+1)
		ids = append(ids, edgeMMAdd(t, s, it))
	}
	older := edgeMMValid()
	older.CreatedAt = edgeMMNow - 1000
	older.Content = "older"
	olderID := edgeMMAdd(t, s, older)

	all, err := s.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems(true): %v", err)
	}
	want := []int64{ids[2], ids[1], ids[0], olderID}
	if len(all) != len(want) {
		t.Fatalf("ListMMItems(true) returned %d items, want %d", len(all), len(want))
	}
	for i := range want {
		if all[i].ID != want[i] {
			t.Fatalf("ListMMItems(true) order = %v, want %v (created_at DESC, id DESC)", edgeMMIDs(all), want)
		}
	}

	if err := s.InvalidateMMItem(ids[1], edgeMMNow, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	live, err := s.ListMMItems(false)
	if err != nil {
		t.Fatalf("ListMMItems(false): %v", err)
	}
	wantLive := []int64{ids[2], ids[0], olderID}
	if got := edgeMMIDs(live); !edgeMMSameIDs(got, wantLive) {
		t.Errorf("ListMMItems(false) = %v, want %v — an invalidated item is not live", got, wantLive)
	}
	if all2, err := s.ListMMItems(true); err != nil || len(all2) != 4 {
		t.Errorf("ListMMItems(true) after an invalidate = %d items (err %v), want 4 — invalidation hides, never deletes", len(all2), err)
	}
}

func edgeMMIDs(items []MMItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func edgeMMSameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEdgeMMFindLiveMMItemMissesOnEveryFieldAndOnDeath is the dedupe key's
// negative space. All four parts must match, so each is broken in turn; and an
// INVALIDATED row must not match at all, or a re-mark of something carl removed
// would answer `already` and silently refuse to bring it back.
func TestEdgeMMFindLiveMMItemMissesOnEveryFieldAndOnDeath(t *testing.T) {
	s := edgeMMOpen(t)
	const (
		source  = "artifact-element"
		job     = int64(7)
		ref     = "50-contract.md"
		content = "the frozen element text"
	)
	// the contract's own definition of the fourth key, computed here rather
	// than copied from anywhere: hex sha256 of Content.
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	it := edgeMMValid()
	it.Kind = "reading"
	it.Source = source
	it.JobID = job
	it.SourceRef = ref
	it.Content = content
	id := edgeMMAdd(t, s, it)

	if got, ok, err := s.FindLiveMMItem(source, job, ref, sha); err != nil || !ok || got != id {
		t.Fatalf("FindLiveMMItem on the exact quad = (%d, %v, %v), want (%d, true, nil) — contentSHA is hex sha256 of Content", got, ok, err, id)
	}

	for _, c := range []struct {
		name   string
		source string
		job    int64
		ref    string
		sha    string
	}{
		{"different source", "artifact", job, ref, sha},
		{"different job", source, job + 1, ref, sha},
		{"different ref", source, job, "51-code-map.md", sha},
		{"different content sha", source, job, ref, strings.Repeat("0", 64)},
		{"empty everything", "", 0, "", ""},
	} {
		got, ok, err := s.FindLiveMMItem(c.source, c.job, c.ref, c.sha)
		if err != nil {
			t.Errorf("%s: FindLiveMMItem err = %v, want nil (a miss is not an error)", c.name, err)
		}
		if ok || got != 0 {
			t.Errorf("%s: FindLiveMMItem = (%d, %v), want (0, false)", c.name, got, ok)
		}
	}

	if err := s.InvalidateMMItem(id, edgeMMNow, "content-changed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	got, ok, err := s.FindLiveMMItem(source, job, ref, sha)
	if err != nil || ok || got != 0 {
		t.Errorf("FindLiveMMItem after invalidation = (%d, %v, %v), want (0, false, nil) — it is not LIVE any more", got, ok, err)
	}
}

// TestEdgeMMSupersedeZeroMatchesIsSuccess is the contract's stated arm: the
// first mark of an artifact supersedes nothing, and answering that with an error
// would make every first mark a 500.
func TestEdgeMMSupersedeZeroMatchesIsSuccess(t *testing.T) {
	s := edgeMMOpen(t)
	n, err := s.SupersedeLiveMMItems("artifact", 42, "00-overview.md", edgeMMNow, 0)
	if err != nil {
		t.Fatalf("SupersedeLiveMMItems on an empty table: %v", err)
	}
	if n != 0 {
		t.Errorf("SupersedeLiveMMItems returned %d, want 0", n)
	}
}

// TestEdgeMMSupersedeTouchesOnlyItsOwnLiveTriple is the blast-radius case: the
// same artifact name under a different job, the same job under a different name,
// and an already-invalidated row of the very same triple must all survive
// untouched — the last one keeping its ORIGINAL reason, since superseding a row
// that already died of a content change would rewrite the history of why.
func TestEdgeMMSupersedeTouchesOnlyItsOwnLiveTriple(t *testing.T) {
	s := edgeMMOpen(t)
	mk := func(job int64, ref, content string) int64 {
		it := edgeMMValid()
		it.Kind = "reading"
		it.Source = "artifact"
		it.JobID = job
		it.SourceRef = ref
		it.Content = content
		return edgeMMAdd(t, s, it)
	}
	target1 := mk(1, "00-overview.md", "v1")
	target2 := mk(1, "00-overview.md", "v2")
	otherJob := mk(2, "00-overview.md", "other job")
	otherName := mk(1, "51-code-map.md", "other name")
	dead := mk(1, "00-overview.md", "already dead")
	if err := s.InvalidateMMItem(dead, edgeMMNow-500, "removed", "by hand"); err != nil {
		t.Fatalf("seed an already-invalidated row: %v", err)
	}

	n, err := s.SupersedeLiveMMItems("artifact", 1, "00-overview.md", edgeMMNow, 0)
	if err != nil {
		t.Fatalf("SupersedeLiveMMItems: %v", err)
	}
	if n != 2 {
		t.Errorf("SupersedeLiveMMItems returned %d, want 2 (the two LIVE rows of that triple)", n)
	}
	for _, id := range []int64{target1, target2} {
		got := edgeMMGet(t, s, id)
		if got.InvalidatedAt != edgeMMNow || got.InvalidReason != "superseded" {
			t.Errorf("item %d: at=%d reason=%q, want %d/superseded", id, got.InvalidatedAt, got.InvalidReason, edgeMMNow)
		}
	}
	for _, id := range []int64{otherJob, otherName} {
		if got := edgeMMGet(t, s, id); got.InvalidatedAt != 0 {
			t.Errorf("item %d was superseded but belongs to another (job, name)", id)
		}
	}
	if got := edgeMMGet(t, s, dead); got.InvalidReason != "removed" || got.InvalidatedAt != edgeMMNow-500 {
		t.Errorf("the already-invalidated row was rewritten: at=%d reason=%q, want %d/removed", got.InvalidatedAt, got.InvalidReason, edgeMMNow-500)
	}
}

// ---------------------------------------------------------------------------
// AddMMCodeAnchor — the ladder's own closed set, and the dirty biconditional
// ---------------------------------------------------------------------------

// TestEdgeMMAddMMCodeAnchorRefusesKindOutsideTheClosedSet covers the four-value
// set plus the near-misses a caller writing the ladder by hand produces
// (`origin/main` with a slash, a bare `HEAD`).
func TestEdgeMMAddMMCodeAnchorRefusesKindOutsideTheClosedSet(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	for _, kind := range []string{"", "origin/main", "HEAD", "Head", "mergebase", "merge base", "clean"} {
		a := edgeMMValidAnchor(id)
		a.AnchorKind = kind
		if err := s.AddMMCodeAnchor(a); err == nil {
			t.Errorf("AddMMCodeAnchor with kind %q succeeded, want a refusal", kind)
		}
	}
	if _, ok, _ := s.GetMMCodeAnchor(id); ok {
		t.Error("a refused AddMMCodeAnchor left a row behind")
	}
}

// TestEdgeMMAddMMCodeAnchorRefusesDegenerateRanges walks the line-number
// boundary from both sides. StartLine ≥ 1 is 1-BASED, so 0 is the off-by-one a
// zero-based editor produces; EndLine ≥ StartLine makes the single-line range
// (the empty-selection mark) legal and an inverted one illegal.
func TestEdgeMMAddMMCodeAnchorRefusesDegenerateRanges(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	for _, c := range []struct {
		name        string
		start, end  int
		wantRefusal bool
	}{
		{"start 0", 0, 5, true},
		{"start negative", -1, 5, true},
		{"end below start", 10, 9, true},
		{"end 0 with start 1", 1, 0, true},
		{"single-line range", 7, 7, false},
		{"start 1", 1, 1, false},
	} {
		a := edgeMMValidAnchor(id)
		a.StartLine, a.EndLine = c.start, c.end
		err := s.AddMMCodeAnchor(a)
		if c.wantRefusal && err == nil {
			t.Errorf("%s: AddMMCodeAnchor(%d-%d) succeeded, want a refusal", c.name, c.start, c.end)
		}
		if !c.wantRefusal && err != nil {
			t.Errorf("%s: AddMMCodeAnchor(%d-%d) = %v, want success", c.name, c.start, c.end, err)
		}
		// an accepted anchor is 1:1 with its item, so the next case needs a
		// fresh item rather than a second anchor on this one
		if !c.wantRefusal {
			id = edgeMMAdd(t, s, edgeMMValid())
		}
	}
}

// TestEdgeMMAddMMCodeAnchorRefusesEmptyText covers Text and TextSHA. The text IS
// the anchor — the whole ladder is a search for it — so an empty one would
// "find" itself in every file and survive every verify forever; an empty hash
// makes the identity check that dedupes a re-mark match everything.
func TestEdgeMMAddMMCodeAnchorRefusesEmptyText(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	for _, c := range []struct {
		name   string
		mutate func(*MMCodeAnchor)
	}{
		{"empty Text", func(a *MMCodeAnchor) { a.Text = "" }},
		{"empty TextSHA", func(a *MMCodeAnchor) { a.TextSHA = "" }},
		{"both empty", func(a *MMCodeAnchor) { a.Text, a.TextSHA = "", "" }},
	} {
		a := edgeMMValidAnchor(id)
		c.mutate(&a)
		if err := s.AddMMCodeAnchor(a); err == nil {
			t.Errorf("%s: AddMMCodeAnchor succeeded, want a refusal", c.name)
		}
	}
}

// TestEdgeMMAddMMCodeAnchorDirtyBiconditional is the contract's `⇔`, asserted in
// BOTH directions because each failure is silent in its own way. A `dirty`
// anchor carrying a SHA claims the text was found at a commit it never was, and
// the next verify would diff against it and invalidate; a commit-kind anchor
// with an EMPTY sha sends the pass straight down the text ladder on every pass,
// so a mark that could have been proven unchanged is re-searched forever and
// dies the first time the text moves.
func TestEdgeMMAddMMCodeAnchorDirtyBiconditional(t *testing.T) {
	s := edgeMMOpen(t)
	const sha = "0f1e2d3c4b5a69788796a5b4c3d2e1f009182736"
	for _, c := range []struct {
		name      string
		kind      string
		sha       string
		wantError bool
	}{
		{"dirty with a sha", "dirty", sha, true},
		{"dirty with a whitespace sha", "dirty", " ", true},
		{"origin-main with no sha", "origin-main", "", true},
		{"merge-base with no sha", "merge-base", "", true},
		{"head with no sha", "head", "", true},
		{"dirty with no sha", "dirty", "", false},
		{"origin-main with a sha", "origin-main", sha, false},
		{"merge-base with a sha", "merge-base", sha, false},
		{"head with a sha", "head", sha, false},
	} {
		id := edgeMMAdd(t, s, edgeMMValid())
		a := edgeMMValidAnchor(id)
		a.AnchorKind, a.AnchorSHA = c.kind, c.sha
		err := s.AddMMCodeAnchor(a)
		if c.wantError && err == nil {
			t.Errorf("%s: AddMMCodeAnchor succeeded, want a refusal", c.name)
		}
		if !c.wantError && err != nil {
			t.Errorf("%s: AddMMCodeAnchor = %v, want success", c.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// verify bookkeeping: check_error and invalidation are disjoint
// ---------------------------------------------------------------------------

// TestEdgeMMSetMMCodeAnchorErrorTouchesNothingElse is the cross-slice invariant
// at its narrowest: a failed read stamps checked_at and check_error and NOTHING
// else. The anchor's coordinates are what an eventual successful verify diffs
// from, so an error path that also moved them would quietly re-anchor a mark
// against a repo it could not even read.
func TestEdgeMMSetMMCodeAnchorErrorTouchesNothingElse(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	before := edgeMMValidAnchor(id)
	edgeMMAnchor(t, s, before)

	if err := s.SetMMCodeAnchorError(id, edgeMMNow, "fetch origin main: exit 128"); err != nil {
		t.Fatalf("SetMMCodeAnchorError: %v", err)
	}
	got := edgeMMReadAnchor(t, s, id)
	if got.CheckError == "" {
		t.Error("check_error is empty after SetMMCodeAnchorError")
	}
	if got.CheckedAt != edgeMMNow {
		t.Errorf("CheckedAt = %d, want %d", got.CheckedAt, edgeMMNow)
	}
	if got.Path != before.Path || got.StartLine != before.StartLine || got.EndLine != before.EndLine {
		t.Errorf("the range moved on a failed read: %s:%d-%d, want %s:%d-%d", got.Path, got.StartLine, got.EndLine, before.Path, before.StartLine, before.EndLine)
	}
	if got.AnchorSHA != before.AnchorSHA || got.AnchorKind != before.AnchorKind {
		t.Errorf("the anchor commit moved on a failed read: %s/%s, want %s/%s", got.AnchorKind, got.AnchorSHA, before.AnchorKind, before.AnchorSHA)
	}
	if got.CheckedSHA != "" {
		t.Errorf("CheckedSHA = %q after a FAILED read, want it left alone — a read that failed verified nothing", got.CheckedSHA)
	}
	if got.Ambiguous {
		t.Error("Ambiguous was set by a failed read")
	}
	if item := edgeMMGet(t, s, id); item.InvalidatedAt != 0 {
		t.Error("SetMMCodeAnchorError invalidated the item — a failed read is never an invalidation")
	}
}

// TestEdgeMMReanchorSetsOrigPathOnceAndClearsTheError covers the "set once,
// never overwritten" clause across TWO renames. orig_path is how a mark made
// before a file moved is still findable by the name carl knew it by, so the
// FIRST path is the one worth keeping; the second rename overwriting it would
// leave a chain with its head cut off. The unchanged-path arm is the boundary on
// the other side: a survived verify that did not move the file must not fill
// orig_path in with a copy of path.
func TestEdgeMMReanchorSetsOrigPathOnceAndClearsTheError(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAdd(t, s, edgeMMValid())
	orig := edgeMMValidAnchor(id)
	edgeMMAnchor(t, s, orig)
	if err := s.SetMMCodeAnchorError(id, edgeMMNow, "boom"); err != nil {
		t.Fatalf("SetMMCodeAnchorError: %v", err)
	}

	const head1 = "1111111111111111111111111111111111111111"
	if err := s.ReanchorMMCodeAnchor(id, orig.Path, 20, 22, head1, edgeMMNow+1, false); err != nil {
		t.Fatalf("Reanchor (same path): %v", err)
	}
	got := edgeMMReadAnchor(t, s, id)
	if got.OrigPath != "" {
		t.Errorf("OrigPath = %q after a re-anchor that did not move the file, want empty", got.OrigPath)
	}
	if got.CheckError != "" {
		t.Errorf("CheckError = %q after a SURVIVED verify, want cleared", got.CheckError)
	}
	if got.AnchorKind != "origin-main" || got.AnchorSHA != head1 || got.CheckedSHA != head1 {
		t.Errorf("after a survived verify: kind=%q sha=%q checked=%q, want origin-main/%s/%s", got.AnchorKind, got.AnchorSHA, got.CheckedSHA, head1, head1)
	}
	if got.StartLine != 20 || got.EndLine != 22 {
		t.Errorf("range = %d-%d, want 20-22 (the shifted coordinates)", got.StartLine, got.EndLine)
	}
	if got.CheckedAt != edgeMMNow+1 {
		t.Errorf("CheckedAt = %d, want %d", got.CheckedAt, edgeMMNow+1)
	}

	const head2 = "2222222222222222222222222222222222222222"
	if err := s.ReanchorMMCodeAnchor(id, "internal/core/mmanchor.go", 30, 32, head2, edgeMMNow+2, true); err != nil {
		t.Fatalf("Reanchor (renamed): %v", err)
	}
	got = edgeMMReadAnchor(t, s, id)
	if got.OrigPath != orig.Path {
		t.Errorf("OrigPath = %q after the first rename, want %q", got.OrigPath, orig.Path)
	}
	if !got.Ambiguous {
		t.Error("Ambiguous was not carried through the re-anchor")
	}

	const head3 = "3333333333333333333333333333333333333333"
	if err := s.ReanchorMMCodeAnchor(id, "internal/anchor/anchor.go", 40, 42, head3, edgeMMNow+3, false); err != nil {
		t.Fatalf("Reanchor (renamed again): %v", err)
	}
	got = edgeMMReadAnchor(t, s, id)
	if got.OrigPath != orig.Path {
		t.Errorf("OrigPath = %q after a SECOND rename, want the first path %q — set once, never overwritten", got.OrigPath, orig.Path)
	}
	if got.Path != "internal/anchor/anchor.go" {
		t.Errorf("Path = %q, want the newest one", got.Path)
	}
	if got.Ambiguous {
		t.Error("Ambiguous stayed true after an unambiguous re-anchor — the flag describes the LAST verify")
	}
}

// TestEdgeMMAnchorWritersRefuseAnUnknownItem covers the three anchor writers
// against an item id that names no anchor row. ErrNotFound is the house answer
// for every other unknown-id verb in this contract (GetMMItem, GetMMSnapshot,
// InvalidateMMItem, GetWatchItem, UpdateWatchItem, SetWatchVerdict); the mm
// anchor writers are the one family the contract leaves unstated, and a silent
// success there is a verify pass reporting that it re-anchored a row that does
// not exist.
func TestEdgeMMAnchorWritersRefuseAnUnknownItem(t *testing.T) {
	s := edgeMMOpen(t)
	const ghost = int64(4242)
	if err := s.ReanchorMMCodeAnchor(ghost, "x.go", 1, 2, "abc", edgeMMNow, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReanchorMMCodeAnchor(unknown) err = %v, want ErrNotFound", err)
	}
	if err := s.SetMMCodeAnchorError(ghost, edgeMMNow, "boom"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMMCodeAnchorError(unknown) err = %v, want ErrNotFound", err)
	}
	if err := s.SetMMCodeAnchorInvalidMeta(ghost, "abc", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMMCodeAnchorInvalidMeta(unknown) err = %v, want ErrNotFound", err)
	}
}

// TestEdgeMMListLiveMMCodeAnchorsFollowsTheITEMSLiveness is the join the pass
// depends on: an anchor row is never deleted, so the only thing keeping a dead
// mark out of the next pass is its ITEM's invalidated_at. A list that filtered
// on the anchor alone would re-verify — and re-notify about — items carl already
// saw die.
func TestEdgeMMListLiveMMCodeAnchorsFollowsTheITEMSLiveness(t *testing.T) {
	s := edgeMMOpen(t)
	alive := edgeMMAdd(t, s, edgeMMValid())
	doomed := edgeMMAdd(t, s, edgeMMValid())
	a1 := edgeMMValidAnchor(alive)
	a1.Repo = "zzz-last"
	edgeMMAnchor(t, s, a1)
	a2 := edgeMMValidAnchor(doomed)
	a2.Repo = "aaa-first"
	edgeMMAnchor(t, s, a2)

	got, err := s.ListLiveMMCodeAnchors()
	if err != nil {
		t.Fatalf("ListLiveMMCodeAnchors: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListLiveMMCodeAnchors = %d anchors, want 2", len(got))
	}
	if got[0].Repo != "aaa-first" || got[1].Repo != "zzz-last" {
		t.Errorf("order = %q, %q, want aaa-first then zzz-last (by repo)", got[0].Repo, got[1].Repo)
	}

	if err := s.InvalidateMMItem(doomed, edgeMMNow, "content-changed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	got, err = s.ListLiveMMCodeAnchors()
	if err != nil {
		t.Fatalf("ListLiveMMCodeAnchors after invalidation: %v", err)
	}
	if len(got) != 1 || got[0].ItemID != alive {
		t.Errorf("ListLiveMMCodeAnchors = %+v, want only the live item's anchor (%d)", got, alive)
	}
	if _, ok, err := s.GetMMCodeAnchor(doomed); err != nil || !ok {
		t.Error("the invalidated item's anchor row was deleted — an invalidation hides, it never deletes")
	}
}

// ---------------------------------------------------------------------------
// watch_items — the reworked standalone table
// ---------------------------------------------------------------------------

func edgeMMWatch(observation string) WatchItem {
	return WatchItem{
		Observation: observation,
		Src:         "hephaestus",
		Dst:         "the frozen contract",
		Predicate:   "ignores",
		Class:       "process",
		Evidence:    "session abc",
	}
}

func edgeMMAddWatch(t *testing.T, s *Store, w WatchItem) int64 {
	t.Helper()
	id, folded, err := s.AddWatchItem(w)
	if err != nil {
		t.Fatalf("AddWatchItem(%q): %v", w.Observation, err)
	}
	if folded {
		t.Fatalf("AddWatchItem(%q) folded onto an existing row — the fixture expected an insert", w.Observation)
	}
	return id
}

func edgeMMGetWatch(t *testing.T, s *Store, id int64) WatchItem {
	t.Helper()
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem(%d): %v", id, err)
	}
	return w
}

// TestEdgeMMAddWatchItemInsertDefaults pins the two values an insert fills in
// for a caller that supplied neither: state `watching` (the dream view filters
// on it, and an empty state matches no filter) and sightings 1 (a fold is +1, so
// a 0 here would make the second sighting read as the first).
func TestEdgeMMAddWatchItemInsertDefaults(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAddWatch(t, s, edgeMMWatch("teammates skip the contract"))
	got := edgeMMGetWatch(t, s, id)
	if got.State != "watching" {
		t.Errorf("State = %q, want watching", got.State)
	}
	if got.Sightings != 1 {
		t.Errorf("Sightings = %d, want 1", got.Sightings)
	}
	if got.FirstSeen == 0 || got.LastSeen == 0 || got.FirstSeen != got.LastSeen {
		t.Errorf("FirstSeen/LastSeen = %d/%d, want one stamped value in both", got.FirstSeen, got.LastSeen)
	}
	if got.ExpiredAt != 0 {
		t.Errorf("ExpiredAt = %d on a fresh row, want 0 (live)", got.ExpiredAt)
	}
}

// TestEdgeMMAddWatchItemFoldPreservesTheCarlTrio is the rework's load-bearing
// carry-over. A repeat sighting updates the AGENT's fields and the counters, and
// touches none of verdict/comment/commented_at — carl's answer to an observation
// survives every later sighting of it, or the loop would silently re-ask a
// question he has already ruled on.
func TestEdgeMMAddWatchItemFoldPreservesTheCarlTrio(t *testing.T) {
	s := edgeMMOpen(t)
	const obs = "hephaestus edits files outside its slice"
	id := edgeMMAddWatch(t, s, edgeMMWatch(obs))
	if err := s.SetWatchVerdict(id, "fix", "yes, tighten the brief"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	sealed := edgeMMGetWatch(t, s, id)
	if sealed.Verdict != "fix" || sealed.Comment != "yes, tighten the brief" || sealed.CommentedAt == "" {
		t.Fatalf("seed verdict did not land: %+v", sealed)
	}

	again := edgeMMWatch(obs)
	again.Class = "correctness"
	again.State = "shipped"
	again.Evidence = "session def"
	again.SuggestedFix = "add the check"
	again.PRURL = "https://example.invalid/pr/1"
	gotID, folded, err := s.AddWatchItem(again)
	if err != nil {
		t.Fatalf("AddWatchItem (repeat): %v", err)
	}
	if !folded || gotID != id {
		t.Fatalf("AddWatchItem (repeat) = (%d, %v), want (%d, true) — a byte-identical observation folds", gotID, folded, id)
	}
	got := edgeMMGetWatch(t, s, id)
	if got.Sightings != 2 {
		t.Errorf("Sightings = %d, want 2", got.Sightings)
	}
	if got.FirstSeen != sealed.FirstSeen {
		t.Errorf("FirstSeen moved on a fold: %d, was %d", got.FirstSeen, sealed.FirstSeen)
	}
	if got.LastSeen < sealed.LastSeen {
		t.Errorf("LastSeen went backwards on a fold: %d, was %d", got.LastSeen, sealed.LastSeen)
	}
	for _, c := range []struct{ name, got, want string }{
		{"Class", got.Class, "correctness"},
		{"State", got.State, "shipped"},
		{"Evidence", got.Evidence, "session def"},
		{"SuggestedFix", got.SuggestedFix, "add the check"},
		{"PRURL", got.PRURL, "https://example.invalid/pr/1"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q after the fold, want %q — non-empty agent fields update", c.name, c.got, c.want)
		}
	}
	for _, c := range []struct{ name, got, want string }{
		{"Verdict", got.Verdict, sealed.Verdict},
		{"Comment", got.Comment, sealed.Comment},
		{"CommentedAt", got.CommentedAt, sealed.CommentedAt},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q after the fold, want %q — the carl trio is untouched by every agent-side write", c.name, c.got, c.want)
		}
	}

	// and an EMPTY agent field on a later fold does not blank what is there
	blank := WatchItem{Observation: obs}
	if _, folded, err := s.AddWatchItem(blank); err != nil || !folded {
		t.Fatalf("AddWatchItem (blank repeat) = (%v, %v), want a fold", folded, err)
	}
	after := edgeMMGetWatch(t, s, id)
	if after.Class != "correctness" || after.SuggestedFix != "add the check" || after.PRURL != "https://example.invalid/pr/1" {
		t.Errorf("an empty field on a fold blanked a stored one: %+v", after)
	}
	if after.Sightings != 3 {
		t.Errorf("Sightings = %d after the third sighting, want 3", after.Sightings)
	}
}

// TestEdgeMMAddWatchItemDoesNotFoldOntoAnExpiredRow is the fold's boundary: the
// contract's fold arm reads "a LIVE row (expired_at=0)". An observation carl
// closed and that then recurs is NEWS — folding it back onto the closed row
// would resurrect a row that was deliberately retired and re-attach his old
// verdict to a fresh sighting.
func TestEdgeMMAddWatchItemDoesNotFoldOntoAnExpiredRow(t *testing.T) {
	s := edgeMMOpen(t)
	const obs = "the same observation, twice in history"
	first := edgeMMAddWatch(t, s, edgeMMWatch(obs))
	if err := s.ExpireWatchItem(first, edgeMMNow); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}
	second, folded, err := s.AddWatchItem(edgeMMWatch(obs))
	if err != nil {
		t.Fatalf("AddWatchItem after expiry: %v", err)
	}
	if folded {
		t.Fatal("AddWatchItem folded onto an EXPIRED row")
	}
	if second == first {
		t.Fatalf("AddWatchItem returned the expired row's id %d", first)
	}
	if got := edgeMMGetWatch(t, s, second); got.Sightings != 1 {
		t.Errorf("the new row's Sightings = %d, want 1 — it is not a continuation", got.Sightings)
	}
	if got := edgeMMGetWatch(t, s, first); got.ExpiredAt != edgeMMNow {
		t.Errorf("the expired row was reopened: ExpiredAt = %d, want %d", got.ExpiredAt, edgeMMNow)
	}
}

// TestEdgeMMAddWatchItemFoldsOnlyOnByteIdenticalObservations pins "byte-
// identical" literally: a case change, a trailing space or a collapsed double
// space is a DIFFERENT observation. Folding on a normalized comparison would
// merge two findings under one verdict; the contract chose the strict reading,
// and the strict reading is the one worth pinning because it is the one a
// helpful `strings.TrimSpace` silently loosens.
func TestEdgeMMAddWatchItemFoldsOnlyOnByteIdenticalObservations(t *testing.T) {
	s := edgeMMOpen(t)
	const base = "zeus dispatches before freezing the contract"
	id := edgeMMAddWatch(t, s, edgeMMWatch(base))
	for _, variant := range []string{
		base + " ",
		" " + base,
		strings.ToUpper(base),
		strings.Replace(base, " ", "  ", 1),
		base + ".",
	} {
		gotID, folded, err := s.AddWatchItem(edgeMMWatch(variant))
		if err != nil {
			t.Fatalf("AddWatchItem(%q): %v", variant, err)
		}
		if folded || gotID == id {
			t.Errorf("AddWatchItem(%q) folded onto %d — the observations are not byte-identical", variant, id)
		}
	}
}

// TestEdgeMMExpireWatchItemIsOneWayAndIdempotent is the watch table's version of
// the invalidation door, including the arm that makes the dream loop's
// re-close safe: expiring an already-expired row succeeds, and the ORIGINAL
// timestamp stands — the same reason InvalidateMMItem does not overwrite, since
// the stamp is the record of when the observation was actually closed.
func TestEdgeMMExpireWatchItemIsOneWayAndIdempotent(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAddWatch(t, s, edgeMMWatch("something to close"))
	if err := s.ExpireWatchItem(id, edgeMMNow); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}
	if err := s.ExpireWatchItem(id, edgeMMNow+90_000); err != nil {
		t.Fatalf("second ExpireWatchItem: %v, want success", err)
	}
	if got := edgeMMGetWatch(t, s, id); got.ExpiredAt != edgeMMNow {
		t.Errorf("ExpiredAt = %d after a re-expire, want the original %d", got.ExpiredAt, edgeMMNow)
	}
}

// TestEdgeMMWatchWritersRefuseAnUnknownID covers the three verbs the contract
// gives an explicit ErrNotFound, plus ExpireWatchItem — whose unknown-id arm the
// contract leaves unstated and which takes the family's house answer here, since
// "remove" is a board action on a row the caller is looking at and a silent 200
// would report a close that never happened.
func TestEdgeMMWatchWritersRefuseAnUnknownID(t *testing.T) {
	s := edgeMMOpen(t)
	for _, id := range []int64{0, -1, 12345} {
		if _, err := s.GetWatchItem(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetWatchItem(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.UpdateWatchItem(id, WatchItem{SuggestedFix: "x"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateWatchItem(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.SetWatchVerdict(id, "fine", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetWatchVerdict(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.ExpireWatchItem(id, edgeMMNow); !errors.Is(err, ErrNotFound) {
			t.Errorf("ExpireWatchItem(%d) err = %v, want ErrNotFound", id, err)
		}
	}
}

// TestEdgeMMUpdateWatchItemPreservesTheCarlTrio is the other agent-side write.
// The rework kept the verb name and moved its first parameter to the watch-row
// id, and the ONE rule that had to survive that move verbatim is this one.
func TestEdgeMMUpdateWatchItemPreservesTheCarlTrio(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAddWatch(t, s, edgeMMWatch("an observation with a verdict"))
	if err := s.SetWatchVerdict(id, "fine", "leave it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	sealed := edgeMMGetWatch(t, s, id)

	if err := s.UpdateWatchItem(id, WatchItem{
		Class:        "process",
		State:        "shipped",
		Evidence:     "new evidence",
		SuggestedFix: "new fix",
		PRURL:        "https://example.invalid/pr/2",
		Verdict:      "fix",
		Comment:      "an agent trying to answer for carl",
		CommentedAt:  "2000-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	got := edgeMMGetWatch(t, s, id)
	if got.Verdict != sealed.Verdict || got.Comment != sealed.Comment || got.CommentedAt != sealed.CommentedAt {
		t.Errorf("UpdateWatchItem wrote the carl trio: %q/%q/%q, want %q/%q/%q",
			got.Verdict, got.Comment, got.CommentedAt, sealed.Verdict, sealed.Comment, sealed.CommentedAt)
	}
	if got.State != "shipped" || got.Evidence != "new evidence" {
		t.Errorf("UpdateWatchItem did not write the agent fields: %+v", got)
	}
}

// TestEdgeMMSetWatchVerdictStampsRFC3339UTC pins the format the contract names,
// parsed rather than pattern-matched, and required to be UTC. Carl's verdicts
// are read back by a nightly pass that compares them against episode timestamps,
// and a local-offset stamp compares wrong by hours exactly once a run.
func TestEdgeMMSetWatchVerdictStampsRFC3339UTC(t *testing.T) {
	s := edgeMMOpen(t)
	id := edgeMMAddWatch(t, s, edgeMMWatch("verdict format"))
	if err := s.SetWatchVerdict(id, "fix", "do it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	got := edgeMMGetWatch(t, s, id)
	ts, err := time.Parse(time.RFC3339, got.CommentedAt)
	if err != nil {
		t.Fatalf("CommentedAt = %q, which is not RFC3339: %v", got.CommentedAt, err)
	}
	if !strings.HasSuffix(got.CommentedAt, "Z") {
		t.Errorf("CommentedAt = %q, want a UTC stamp ending in Z", got.CommentedAt)
	}
	if ts.IsZero() {
		t.Errorf("CommentedAt = %q parsed to the zero time", got.CommentedAt)
	}
}

// TestEdgeMMListWatchItemsIsLiveOnly covers the list's stated filter and the
// state argument's empty case. An expired row still READS by id — the dream
// report links to closed observations — but it is out of every list, which is
// what makes expiry the replacement for a delete.
func TestEdgeMMListWatchItemsIsLiveOnly(t *testing.T) {
	s := edgeMMOpen(t)
	watching := edgeMMAddWatch(t, s, edgeMMWatch("still watching"))
	shipped := edgeMMAddWatch(t, s, edgeMMWatch("shipped one"))
	if err := s.UpdateWatchItem(shipped, WatchItem{State: "shipped"}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	gone := edgeMMAddWatch(t, s, edgeMMWatch("closed one"))
	if err := s.ExpireWatchItem(gone, edgeMMNow); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems(\"\"): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListWatchItems(\"\") = %d rows, want 2 (every LIVE state)", len(all))
	}
	for _, w := range all {
		if w.ID == gone {
			t.Error("an expired row is in ListWatchItems")
		}
	}
	only, err := s.ListWatchItems("watching")
	if err != nil {
		t.Fatalf("ListWatchItems(watching): %v", err)
	}
	if len(only) != 1 || only[0].ID != watching {
		t.Errorf("ListWatchItems(watching) = %+v, want just %d", only, watching)
	}
	// The vocabulary is validated everywhere it appears, reads included: a state
	// outside {watching, shipped, declined} cannot name any row, so a caller
	// asking for one has a bug and is told rather than handed an empty pane that
	// looks like "nothing is being watched".
	if none, err := s.ListWatchItems("no-such-state"); err == nil || len(none) != 0 {
		t.Errorf("ListWatchItems(no-such-state) = %+v (err %v), want no rows and a refusal", none, err)
	}
	// still readable by id
	if got, err := s.GetWatchItem(gone); err != nil || got.ExpiredAt == 0 {
		t.Errorf("GetWatchItem(expired) = (%+v, %v), want the row with its ExpiredAt set", got, err)
	}
}

// ---------------------------------------------------------------------------
// v29 migration: what must NOT be carried across
// ---------------------------------------------------------------------------

// TestEdgeMMMigrationV29CarriesOnlyLiveWatchedEdges is the migration's negative
// space. The contract's source set is "kg_watch ⋈ kg_edges where expired_at=0",
// so an overlay whose EDGE was invalidated is a closed observation and must not
// come back as a live watch_items row — the whole point of the join. The three
// other assertions are the ones a hook that ran AFTER the wipe would fail: the
// observation text comes off the edge's fact and the node names off kg_nodes,
// both of which are empty by the time the migration finishes.
func TestEdgeMMMigrationV29CarriesOnlyLiveWatchedEdges(t *testing.T) {
	// EXACT rather than a floor: this fixture seeds a v28 shape and lets
	// migrate() walk forward, so whoever appends v32 has to come here and
	// confirm downgradeFreshDB reverses their step before this passes again.
	// (v30 added jobs.agent; downgradeFreshDB's v30 arm drops it. v31 only
	// deleted token rows, so there is no shape for the helper to reverse.)
	if schemaVersion != 31 {
		t.Fatalf("schemaVersion = %d, want 31 — this fixture seeds a v28 shape and walks it forward", schemaVersion)
	}
	s := edgeMMOpen(t)

	src, err := s.UpsertNode(GraphNode{Name: "hephaestus"})
	if err != nil {
		t.Fatalf("upsert src node: %v", err)
	}
	dst, err := s.UpsertNode(GraphNode{Name: "the frozen contract"})
	if err != nil {
		t.Fatalf("upsert dst node: %v", err)
	}
	liveEdge, added, err := s.InsertEdge(GraphEdge{SrcID: src, DstID: dst, Predicate: "ignores", Fact: "hephaestus ignores the frozen contract", Strength: 5})
	if err != nil || !added {
		t.Fatalf("insert live edge = (%d, %v, %v)", liveEdge, added, err)
	}
	deadEdge, added, err := s.InsertEdge(GraphEdge{SrcID: src, DstID: dst, Predicate: "ignored", Fact: "an observation carl already closed", Strength: 5})
	if err != nil || !added {
		t.Fatalf("insert dead edge = (%d, %v, %v)", deadEdge, added, err)
	}

	downgradeFreshDB(t, s, 28)

	for _, c := range []int64{liveEdge, deadEdge} {
		if _, err := s.db.Exec(
			`INSERT INTO kg_watch (edge_id, class, state, sightings, first_seen, last_seen,
				evidence, suggested_fix, pr_url, updated_at, verdict, comment, commented_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c, "process", "shipped", 4, edgeMMNow-9000, edgeMMNow-100,
			"three sessions", "tighten the brief", "https://example.invalid/pr/9",
			edgeMMNow-50, "fix", "agreed, do it", "2026-08-01T00:00:00Z",
		); err != nil {
			t.Fatalf("seed kg_watch for edge %d: %v", c, err)
		}
	}
	// close the second edge: its overlay must not survive the migration
	if _, err := s.db.Exec(`UPDATE kg_edges SET expired_at = ? WHERE id = ?`, edgeMMNow-10, deadEdge); err != nil {
		t.Fatalf("expire the dead edge: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v28: %v", err)
	}

	var tables int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='kg_watch'`).Scan(&tables); err != nil {
		t.Fatalf("probe kg_watch: %v", err)
	}
	if tables != 0 {
		t.Error("kg_watch still exists after v29 — the overlay became its own table")
	}

	rows, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems after the migration: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("watch_items holds %d rows after the migration, want exactly 1 (only the LIVE watched edge): %+v", len(rows), rows)
	}
	got := rows[0]
	if got.Observation != "hephaestus ignores the frozen contract" {
		t.Errorf("Observation = %q, want the live edge's fact", got.Observation)
	}
	if got.Src != "hephaestus" || got.Dst != "the frozen contract" || got.Predicate != "ignores" {
		t.Errorf("triple = %q/%q/%q, want hephaestus/ignores/the frozen contract", got.Src, got.Predicate, got.Dst)
	}
	if got.Sightings != 4 || got.FirstSeen != edgeMMNow-9000 || got.LastSeen != edgeMMNow-100 {
		t.Errorf("counters = %d sightings, %d..%d, want 4, %d..%d", got.Sightings, got.FirstSeen, got.LastSeen, edgeMMNow-9000, edgeMMNow-100)
	}
	if got.Verdict != "fix" || got.Comment != "agreed, do it" || got.CommentedAt != "2026-08-01T00:00:00Z" {
		t.Errorf("the carl trio did not survive: %q/%q/%q", got.Verdict, got.Comment, got.CommentedAt)
	}
	if got.State != "shipped" || got.Class != "process" || got.Evidence != "three sessions" || got.SuggestedFix != "tighten the brief" || got.PRURL != "https://example.invalid/pr/9" {
		t.Errorf("an overlay column was dropped: %+v", got)
	}
	if got.ExpiredAt != 0 {
		t.Errorf("the carried row is expired (%d), want live", got.ExpiredAt)
	}
}
