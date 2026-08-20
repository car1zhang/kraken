package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// mm_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice S1's store rows: the `mm_items` verbs, the
// `mm_code_anchors` verbs, the REWORKED standalone-table watch verbs, and the
// v29 migration's stated obligations.
//
// Written from the frozen contract alone. internal/store/mm.go,
// internal/store/watch.go and internal/store/schema.go were never read by any
// route — no Read, no grep, no git show, no build output quoting them. Every
// assertion transcribes a clause of a contract ROW.
//
// Dimension: LOGIC — the stated invariants and the happy paths. Refusals
// (`AddMMItem`'s four validations rejecting garbage, `AddMMCodeAnchor`'s
// AnchorKind/line refusals, `ErrNotFound` arms) are athena-edge's ground and are
// deliberately not duplicated here; the one-way clauses ARE here, because
// "invalidation is one-way and terminal" is a stated invariant rather than an
// error path.
//
// HOME is isolated in every test that opens a Store (contractMMStore) — an
// obligation of the test, never an assumption about the machine.
//
// Every package-level identifier declared here is prefixed `contractMM`.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractMMStore opens a throwaway store with HOME isolated.
func contractMMStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// contractMMSHA is the contract's contentSHA: hex sha256 of the text.
func contractMMSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// contractMMNote is the minimal valid item: the four fields AddMMItem
// validates, and nothing else.
func contractMMNote(content string, createdAt int64) MMItem {
	return MMItem{Kind: "note", Source: "manual", Content: content, CreatedAt: createdAt}
}

func contractMMAdd(t *testing.T, s *Store, it MMItem) int64 {
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

func contractMMGet(t *testing.T, s *Store, id int64) MMItem {
	t.Helper()
	it, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

func contractMMJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	j := Job{Slug: slug, Repo: "kraken"}
	id, err := s.CreateJob(&j)
	if err != nil {
		t.Fatalf("CreateJob(%s): %v", slug, err)
	}
	return id
}

// contractMMAnchorRow is a fully-populated anchor, so a round trip can measure
// every column rather than the handful a minimal fixture would reach.
func contractMMAnchorRow(itemID int64) MMCodeAnchor {
	return MMCodeAnchor{
		ItemID:     itemID,
		Repo:       "kraken",
		Path:       "internal/jobs/mm.go",
		StartLine:  10,
		EndLine:    12,
		AnchorSHA:  "1111111111111111111111111111111111111111",
		AnchorKind: "origin-main",
		Text:       "func f() {\n\treturn\n}",
		TextSHA:    contractMMSHA("func f() {\n\treturn\n}"),
		Lang:       "go",
		CheckedAt:  1_700_000_000_000,
		CheckedSHA: "1111111111111111111111111111111111111111",
	}
}

// ---------------------------------------------------------------------------
// mm_items: AddMMItem / GetMMItem / ListMMItems
// ---------------------------------------------------------------------------

// TestContractMMAddGetRoundTripsEveryStoredField pins the MMItem row: every
// field the struct carries survives the write, the id comes back stamped, and a
// live item reads `InvalidatedAt == 0` ("0 = live — the column is NULL when
// live", the archived_at precedent).
func TestContractMMAddGetRoundTripsEveryStoredField(t *testing.T) {
	s := contractMMStore(t)
	job := contractMMJob(t, s, "mental-model")

	want := MMItem{
		Kind:      "reading",
		Source:    "code",
		JobID:     job,
		Slug:      "mental-model",
		SourceRef: "kraken/internal/jobs/mm.go:10-12",
		Content:   "func f() {\n\treturn\n}",
		Context:   "the mint's validate-first order",
		CreatedAt: 1_700_000_000_000,
	}
	id := contractMMAdd(t, s, want)

	got := contractMMGet(t, s, id)
	if got.ID != id {
		t.Errorf("ID = %d, want %d", got.ID, id)
	}
	if got.Kind != want.Kind || got.Source != want.Source {
		t.Errorf("kind/source = %q/%q, want %q/%q", got.Kind, got.Source, want.Kind, want.Source)
	}
	if got.JobID != want.JobID || got.Slug != want.Slug {
		t.Errorf("job/slug = %d/%q, want %d/%q", got.JobID, got.Slug, want.JobID, want.Slug)
	}
	if got.SourceRef != want.SourceRef {
		t.Errorf("SourceRef = %q, want %q", got.SourceRef, want.SourceRef)
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
	if got.Context != want.Context {
		t.Errorf("Context = %q, want %q", got.Context, want.Context)
	}
	if got.CreatedAt != want.CreatedAt {
		t.Errorf("CreatedAt = %d, want %d — the caller's stamp, not the store's clock", got.CreatedAt, want.CreatedAt)
	}
	if got.InvalidatedAt != 0 || got.InvalidReason != "" || got.InvalidDetail != "" {
		t.Errorf("a fresh item reads invalidated (%d, %q, %q) — 0/empty is LIVE",
			got.InvalidatedAt, got.InvalidReason, got.InvalidDetail)
	}
	if got.EpisodeID != 0 {
		t.Errorf("EpisodeID = %d on a just-created item, want 0 until MintMMEpisode stamps it", got.EpisodeID)
	}
}

// TestContractMMAcceptsEveryFrozenKindAndSource pins the two closed sets the
// row names, from the accepting side: three kinds x six sources, all valid.
// (The refusing side is athena-edge's.)
func TestContractMMAcceptsEveryFrozenKindAndSource(t *testing.T) {
	s := contractMMStore(t)
	kinds := []string{"decision", "reading", "note"}
	sources := []string{"question", "artifact", "artifact-element", "code", "agent-output", "manual"}
	at := int64(1_700_000_000_000)
	for _, kind := range kinds {
		for _, source := range sources {
			at++
			it := MMItem{Kind: kind, Source: source, Content: "c", CreatedAt: at}
			id, err := s.AddMMItem(it)
			if err != nil {
				t.Fatalf("AddMMItem kind=%q source=%q: %v — both are in the frozen set", kind, source, err)
			}
			got := contractMMGet(t, s, id)
			if got.Kind != kind || got.Source != source {
				t.Fatalf("round trip of kind=%q source=%q came back %q/%q", kind, source, got.Kind, got.Source)
			}
		}
	}
}

// TestContractMMListOrdersNewestFirstAndFiltersInvalidated pins ListMMItems:
// `created_at DESC, id DESC`, and `false` meaning live only.
func TestContractMMListOrdersNewestFirstAndFiltersInvalidated(t *testing.T) {
	s := contractMMStore(t)
	old := contractMMAdd(t, s, contractMMNote("oldest", 100))
	tieLow := contractMMAdd(t, s, contractMMNote("tie, lower id", 500))
	tieHigh := contractMMAdd(t, s, contractMMNote("tie, higher id", 500))
	newest := contractMMAdd(t, s, contractMMNote("newest", 900))

	all, err := s.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems(true): %v", err)
	}
	wantOrder := []int64{newest, tieHigh, tieLow, old}
	if len(all) != len(wantOrder) {
		t.Fatalf("ListMMItems(true) returned %d rows, want %d", len(all), len(wantOrder))
	}
	for i, want := range wantOrder {
		if all[i].ID != want {
			t.Fatalf("ListMMItems(true) order = %v, want %v (created_at DESC, ties id DESC)",
				contractMMIDs(all), wantOrder)
		}
	}

	if err := s.InvalidateMMItem(tieHigh, 1_000, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	live, err := s.ListMMItems(false)
	if err != nil {
		t.Fatalf("ListMMItems(false): %v", err)
	}
	wantLive := []int64{newest, tieLow, old}
	if got := contractMMIDs(live); len(got) != len(wantLive) {
		t.Fatalf("ListMMItems(false) = %v, want %v — false is live only", got, wantLive)
	}
	for i, want := range wantLive {
		if live[i].ID != want {
			t.Fatalf("ListMMItems(false) = %v, want %v", contractMMIDs(live), wantLive)
		}
	}
	// ... and `true` still carries the invalidated row, with its stamps.
	all, err = s.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems(true) after invalidation: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListMMItems(true) = %v, want all four rows", contractMMIDs(all))
	}
	for _, it := range all {
		if it.ID == tieHigh && it.InvalidatedAt != 1_000 {
			t.Errorf("the invalidated row lists with InvalidatedAt %d, want 1000", it.InvalidatedAt)
		}
	}
}

func contractMMIDs(items []MMItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// TestContractMMListItemsNeverCarriesSnapshotBytes pins the row's stated
// elision: "Snapshot BYTES are not on this struct — elided from every list/get,
// served by GetMMSnapshot". The only observable form of that promise at this
// layer is that the ext IS carried while the bytes have their own accessor.
func TestContractMMListItemsNeverCarriesSnapshotBytes(t *testing.T) {
	s := contractMMStore(t)
	id := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "artifact", Slug: "j", SourceRef: "00-overview.md",
		Content: "00-overview.md", CreatedAt: 10,
	})
	if err := s.SetMMSnapshot(id, []byte("# frozen bytes\n"), ".md"); err != nil {
		t.Fatalf("SetMMSnapshot: %v", err)
	}
	got := contractMMGet(t, s, id)
	if got.SnapshotExt != ".md" {
		t.Errorf("SnapshotExt = %q, want %q — the ext rides the row, the bytes do not", got.SnapshotExt, ".md")
	}
	if got.Content != "00-overview.md" {
		t.Errorf("Content = %q — the snapshot must not have overwritten it", got.Content)
	}
	data, ext, err := s.GetMMSnapshot(id)
	if err != nil {
		t.Fatalf("GetMMSnapshot: %v", err)
	}
	if string(data) != "# frozen bytes\n" || ext != ".md" {
		t.Fatalf("GetMMSnapshot = (%q, %q), want (%q, %q)", data, ext, "# frozen bytes\n", ".md")
	}
}

// TestContractMMSnapshotOverwriteFreezesTheNewBytes: SetMMSnapshot is the
// freeze, and re-freezing an item (the supersede-then-insert path never does,
// but the verb is not one-way) replaces both halves together.
func TestContractMMSnapshotOverwriteFreezesTheNewBytes(t *testing.T) {
	s := contractMMStore(t)
	id := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "artifact", SourceRef: "d.svg", Content: "d.svg", CreatedAt: 10,
	})
	if err := s.SetMMSnapshot(id, []byte("<svg/>"), ".svg"); err != nil {
		t.Fatalf("SetMMSnapshot: %v", err)
	}
	if err := s.SetMMSnapshot(id, []byte("<svg id=2/>"), ".svg"); err != nil {
		t.Fatalf("SetMMSnapshot again: %v", err)
	}
	data, ext, err := s.GetMMSnapshot(id)
	if err != nil {
		t.Fatalf("GetMMSnapshot: %v", err)
	}
	if string(data) != "<svg id=2/>" || ext != ".svg" {
		t.Fatalf("GetMMSnapshot = (%q, %q), want the second freeze", data, ext)
	}
}

// ---------------------------------------------------------------------------
// mm_items: FindLiveMMItem / SupersedeLiveMMItems / InvalidateMMItem
// ---------------------------------------------------------------------------

// TestContractMMFindLiveMatchesAllFourAndOnlyLiveRows pins the idempotence
// primitive every `already` path on the route family is built out of: the match
// is on all FOUR of (source, jobID, sourceRef, contentSHA), and an invalidated
// row does not answer.
func TestContractMMFindLiveMatchesAllFourAndOnlyLiveRows(t *testing.T) {
	s := contractMMStore(t)
	job := contractMMJob(t, s, "j")
	other := contractMMJob(t, s, "other")
	const content = "the exact text carl read"
	sha := contractMMSHA(content)

	id := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "artifact-element", JobID: job, Slug: "j",
		SourceRef: "50-components.md", Content: content, CreatedAt: 10,
	})

	got, ok, err := s.FindLiveMMItem("artifact-element", job, "50-components.md", sha)
	if err != nil {
		t.Fatalf("FindLiveMMItem: %v", err)
	}
	if !ok || got != id {
		t.Fatalf("FindLiveMMItem = (%d, %t), want (%d, true)", got, ok, id)
	}

	// Each of the four, varied one at a time, must MISS: three zeroes rather
	// than a stray hit is what keeps a second mark from folding onto an
	// unrelated row.
	for _, miss := range []struct {
		name      string
		source    string
		job       int64
		sourceRef string
		sha       string
	}{
		{"another source", "artifact", job, "50-components.md", sha},
		{"another job", "artifact-element", other, "50-components.md", sha},
		{"another source_ref", "artifact-element", job, "51-code-map.md", sha},
		{"another content", "artifact-element", job, "50-components.md", contractMMSHA(content + "!")},
	} {
		gotID, gotOK, err := s.FindLiveMMItem(miss.source, miss.job, miss.sourceRef, miss.sha)
		if err != nil {
			t.Fatalf("FindLiveMMItem (%s): %v", miss.name, err)
		}
		if gotOK || gotID != 0 {
			t.Errorf("FindLiveMMItem (%s) = (%d, %t), want (0, false)", miss.name, gotID, gotOK)
		}
	}

	// "returns the LIVE item": once invalidated, the same quad answers (0,false)
	// — which is what makes a re-mark after an invalidation a fresh row rather
	// than a resurrection.
	if err := s.InvalidateMMItem(id, 20, "content-changed", "bytes differ"); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	gotID, gotOK, err := s.FindLiveMMItem("artifact-element", job, "50-components.md", sha)
	if err != nil {
		t.Fatalf("FindLiveMMItem after invalidation: %v", err)
	}
	if gotOK || gotID != 0 {
		t.Fatalf("FindLiveMMItem still answers %d/%t after the row was invalidated — content reappearing "+
			"must never resurrect an item", gotID, gotOK)
	}
}

// TestContractMMSupersedeInvalidatesEveryLiveMatch pins the whole row:
// (source, jobID, sourceRef) — deliberately WITHOUT the content sha, since the
// point is "the bytes changed" — reason `superseded`, the count returned, and
// zero matches being success rather than an error.
func TestContractMMSupersedeInvalidatesEveryLiveMatch(t *testing.T) {
	s := contractMMStore(t)
	job := contractMMJob(t, s, "j")
	other := contractMMJob(t, s, "other")

	a := contractMMAdd(t, s, MMItem{Kind: "reading", Source: "artifact", JobID: job, SourceRef: "00-overview.md", Content: "v1", CreatedAt: 10})
	b := contractMMAdd(t, s, MMItem{Kind: "reading", Source: "artifact", JobID: job, SourceRef: "00-overview.md", Content: "v2", CreatedAt: 20})
	// Three rows that must NOT be touched: another job, another ref, another
	// source.
	keepJob := contractMMAdd(t, s, MMItem{Kind: "reading", Source: "artifact", JobID: other, SourceRef: "00-overview.md", Content: "v1", CreatedAt: 30})
	keepRef := contractMMAdd(t, s, MMItem{Kind: "reading", Source: "artifact", JobID: job, SourceRef: "51-code-map.md", Content: "v1", CreatedAt: 40})
	keepSrc := contractMMAdd(t, s, MMItem{Kind: "reading", Source: "artifact-element", JobID: job, SourceRef: "00-overview.md", Content: "v1", CreatedAt: 50})

	n, err := s.SupersedeLiveMMItems("artifact", job, "00-overview.md", 999, 0)
	if err != nil {
		t.Fatalf("SupersedeLiveMMItems: %v", err)
	}
	if n != 2 {
		t.Fatalf("SupersedeLiveMMItems returned %d, want 2 (both live rows for that triple)", n)
	}
	for _, id := range []int64{a, b} {
		got := contractMMGet(t, s, id)
		if got.InvalidatedAt != 999 {
			t.Errorf("item %d InvalidatedAt = %d, want the passed 999", id, got.InvalidatedAt)
		}
		if got.InvalidReason != "superseded" {
			t.Errorf("item %d reason = %q, want %q", id, got.InvalidReason, "superseded")
		}
	}
	for _, id := range []int64{keepJob, keepRef, keepSrc} {
		if got := contractMMGet(t, s, id); got.InvalidatedAt != 0 {
			t.Errorf("item %d was superseded too — the match is (source, job, source_ref) and all three must bind", id)
		}
	}

	// "zero matches is success"
	n, err = s.SupersedeLiveMMItems("artifact", job, "nothing-like-this.md", 1000, 0)
	if err != nil {
		t.Fatalf("SupersedeLiveMMItems on no matches: %v — zero matches is success", err)
	}
	if n != 0 {
		t.Fatalf("SupersedeLiveMMItems on no matches returned %d, want 0", n)
	}
	// A second supersede of the same triple finds nothing live left.
	n, err = s.SupersedeLiveMMItems("artifact", job, "00-overview.md", 1001, 0)
	if err != nil {
		t.Fatalf("second SupersedeLiveMMItems: %v", err)
	}
	if n != 0 {
		t.Fatalf("second SupersedeLiveMMItems returned %d, want 0 — the rows are no longer live", n)
	}
}

// TestContractMMInvalidateIsOneWayAndTerminal is the cross-slice invariant at
// its own layer: the FIRST invalidation stamps all three, a SECOND one succeeds
// without overwriting the earlier reason or timestamp, and nothing anywhere
// clears the stamp.
func TestContractMMInvalidateIsOneWayAndTerminal(t *testing.T) {
	s := contractMMStore(t)
	id := contractMMAdd(t, s, contractMMNote("a thing carl knows", 10))

	if err := s.InvalidateMMItem(id, 500, "content-changed", "abc123 -> def456"); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	got := contractMMGet(t, s, id)
	if got.InvalidatedAt != 500 || got.InvalidReason != "content-changed" || got.InvalidDetail != "abc123 -> def456" {
		t.Fatalf("first invalidation stamped (%d, %q, %q), want (500, content-changed, abc123 -> def456)",
			got.InvalidatedAt, got.InvalidReason, got.InvalidDetail)
	}

	// "already-invalidated -> success WITHOUT overwriting the earlier
	// reason/at". The second caller is a later verify pass finding the same
	// item dead for a shallower reason; the FIRST verdict is the one that
	// explains the death.
	if err := s.InvalidateMMItem(id, 9_999, "removed", "carl clicked remove"); err != nil {
		t.Fatalf("second InvalidateMMItem returned an error: %v — an already-invalidated row is SUCCESS", err)
	}
	got = contractMMGet(t, s, id)
	if got.InvalidatedAt != 500 || got.InvalidReason != "content-changed" || got.InvalidDetail != "abc123 -> def456" {
		t.Fatalf("the second invalidation overwrote the first: (%d, %q, %q)",
			got.InvalidatedAt, got.InvalidReason, got.InvalidDetail)
	}
	// ... and a supersede pass over the same row is equally powerless.
	if _, err := s.SupersedeLiveMMItems(got.Source, got.JobID, got.SourceRef, 12_345, 0); err != nil {
		t.Fatalf("SupersedeLiveMMItems over an invalidated row: %v", err)
	}
	if again := contractMMGet(t, s, id); again.InvalidReason != "content-changed" || again.InvalidatedAt != 500 {
		t.Fatalf("a supersede pass rewrote the dead row's verdict: (%d, %q)", again.InvalidatedAt, again.InvalidReason)
	}
}

// TestContractMMSetItemEpisodeStampsTheLink: the mint's back-stamp, and the
// only writer of MMItem.EpisodeID.
func TestContractMMSetItemEpisodeStampsTheLink(t *testing.T) {
	s := contractMMStore(t)
	id := contractMMAdd(t, s, contractMMNote("linked", 10))
	if err := s.SetMMItemEpisode(id, 4242); err != nil {
		t.Fatalf("SetMMItemEpisode: %v", err)
	}
	if got := contractMMGet(t, s, id); got.EpisodeID != 4242 {
		t.Fatalf("EpisodeID = %d, want 4242", got.EpisodeID)
	}
	// The mint is idempotent (AddEpisode's hash dedupe returns the existing id
	// and the stamp is written again), so re-stamping the same value must be a
	// plain success rather than a conflict.
	if err := s.SetMMItemEpisode(id, 4242); err != nil {
		t.Fatalf("re-stamping the same episode id: %v", err)
	}
	if got := contractMMGet(t, s, id); got.EpisodeID != 4242 {
		t.Fatalf("EpisodeID = %d after a re-stamp, want 4242", got.EpisodeID)
	}
}

// ---------------------------------------------------------------------------
// mm_code_anchors
// ---------------------------------------------------------------------------

// TestContractMMCodeAnchorRoundTripsEveryColumn pins the 1:1 companion row.
func TestContractMMCodeAnchorRoundTripsEveryColumn(t *testing.T) {
	s := contractMMStore(t)
	item := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", SourceRef: "kraken/internal/jobs/mm.go:10-12",
		Content: "func f() {\n\treturn\n}", CreatedAt: 10,
	})
	want := contractMMAnchorRow(item)
	want.OrigPath = "internal/jobs/anchor.go"
	want.Ambiguous = true
	if err := s.AddMMCodeAnchor(want); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	got, ok, err := s.GetMMCodeAnchor(item)
	if err != nil {
		t.Fatalf("GetMMCodeAnchor: %v", err)
	}
	if !ok {
		t.Fatal("GetMMCodeAnchor reported no anchor for an item that has one")
	}
	if got != want {
		t.Fatalf("GetMMCodeAnchor =\n  %+v\nwant\n  %+v", got, want)
	}
}

// TestContractMMCodeAnchorAcceptsEveryFrozenAnchorKind pins the closed set from
// the accepting side, including the stated biconditional: `dirty` is the one
// kind that carries an EMPTY AnchorSHA, and the three commit kinds carry one.
func TestContractMMCodeAnchorAcceptsEveryFrozenAnchorKind(t *testing.T) {
	s := contractMMStore(t)
	for _, kind := range []string{"origin-main", "merge-base", "head", "dirty"} {
		item := contractMMAdd(t, s, MMItem{
			Kind: "reading", Source: "code", SourceRef: "kraken/a.go:1-1", Content: "x", CreatedAt: 10,
		})
		a := contractMMAnchorRow(item)
		a.AnchorKind = kind
		if kind == "dirty" {
			a.AnchorSHA = ""
			a.CheckedSHA = ""
		}
		if err := s.AddMMCodeAnchor(a); err != nil {
			t.Fatalf("AddMMCodeAnchor kind=%q: %v — it is in the frozen set", kind, err)
		}
		got, ok, err := s.GetMMCodeAnchor(item)
		if err != nil || !ok {
			t.Fatalf("GetMMCodeAnchor kind=%q: %+v, %t, %v", kind, got, ok, err)
		}
		if got.AnchorKind != kind {
			t.Fatalf("AnchorKind = %q, want %q", got.AnchorKind, kind)
		}
		if kind == "dirty" && got.AnchorSHA != "" {
			t.Fatalf("a dirty anchor stored AnchorSHA %q — never a fabricated SHA", got.AnchorSHA)
		}
		if kind != "dirty" && got.AnchorSHA == "" {
			t.Fatalf("a %q anchor stored an empty AnchorSHA", kind)
		}
	}
}

// TestContractMMGetCodeAnchorAbsentIsZeroFalseNil: "(zero, false, nil) when
// none" — a NOTE or an artifact reading has no anchor, which is the normal
// reading rather than a missing one, so it must not be an error.
func TestContractMMGetCodeAnchorAbsentIsZeroFalseNil(t *testing.T) {
	s := contractMMStore(t)
	item := contractMMAdd(t, s, contractMMNote("no anchor here", 10))
	got, ok, err := s.GetMMCodeAnchor(item)
	if err != nil {
		t.Fatalf("GetMMCodeAnchor on an anchorless item returned an error: %v", err)
	}
	if ok {
		t.Fatal("GetMMCodeAnchor reported an anchor for an item that has none")
	}
	if got != (MMCodeAnchor{}) {
		t.Fatalf("GetMMCodeAnchor = %+v, want the zero value", got)
	}
}

// TestContractMMListLiveCodeAnchorsFiltersByItemAndOrders pins the verify
// pass' worklist: anchors whose ITEM is live, ordered by Repo then ItemID.
func TestContractMMListLiveCodeAnchorsFiltersByItemAndOrders(t *testing.T) {
	s := contractMMStore(t)
	mk := func(repo string, createdAt int64) int64 {
		item := contractMMAdd(t, s, MMItem{
			Kind: "reading", Source: "code", SourceRef: repo + "/a.go:1-1", Content: "x", CreatedAt: createdAt,
		})
		a := contractMMAnchorRow(item)
		a.Repo = repo
		if err := s.AddMMCodeAnchor(a); err != nil {
			t.Fatalf("AddMMCodeAnchor: %v", err)
		}
		return item
	}
	monolithFirst := mk("monolith", 10)
	krakenFirst := mk("kraken", 20)
	krakenSecond := mk("kraken", 30)
	dead := mk("kraken", 40)

	if err := s.InvalidateMMItem(dead, 500, "content-changed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}

	got, err := s.ListLiveMMCodeAnchors()
	if err != nil {
		t.Fatalf("ListLiveMMCodeAnchors: %v", err)
	}
	want := []int64{krakenFirst, krakenSecond, monolithFirst}
	if len(got) != len(want) {
		t.Fatalf("ListLiveMMCodeAnchors returned %d anchors, want %d (the dead item's is excluded)", len(got), len(want))
	}
	for i := range want {
		if got[i].ItemID != want[i] {
			t.Fatalf("order = %+v, want item ids %v (Repo then ItemID)", got, want)
		}
	}
	for _, a := range got {
		if a.ItemID == dead {
			t.Fatal("an anchor whose item is invalidated is still on the verify worklist")
		}
	}
}

// TestContractMMReanchorRewritesToHeadCoordinates pins the SURVIVED-verify row
// column by column, including the two it CLEARS: check_error goes (the read
// worked this time) and anchor_kind is forced to origin-main (a survived verify
// re-anchors against origin/main, whatever the mark's original rung was).
func TestContractMMReanchorRewritesToHeadCoordinates(t *testing.T) {
	s := contractMMStore(t)
	item := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", SourceRef: "kraken/a.go:10-12", Content: "x", CreatedAt: 10,
	})
	a := contractMMAnchorRow(item)
	a.AnchorKind = "merge-base"
	if err := s.AddMMCodeAnchor(a); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	// A previous pass failed to read the repo; the stamp must not survive a
	// pass that succeeded.
	if err := s.SetMMCodeAnchorError(item, 1_100, "git fetch: dial tcp: no route to host"); err != nil {
		t.Fatalf("SetMMCodeAnchorError: %v", err)
	}

	const head = "2222222222222222222222222222222222222222"
	if err := s.ReanchorMMCodeAnchor(item, "internal/jobs/mm.go", 15, 17, head, 2_000, false); err != nil {
		t.Fatalf("ReanchorMMCodeAnchor: %v", err)
	}
	got, ok, err := s.GetMMCodeAnchor(item)
	if err != nil || !ok {
		t.Fatalf("GetMMCodeAnchor: %+v, %t, %v", got, ok, err)
	}
	if got.Path != "internal/jobs/mm.go" || got.StartLine != 15 || got.EndLine != 17 {
		t.Errorf("path/range = %q %d-%d, want internal/jobs/mm.go 15-17", got.Path, got.StartLine, got.EndLine)
	}
	if got.AnchorSHA != head || got.CheckedSHA != head {
		t.Errorf("anchor_sha/checked_sha = %q/%q, want both %q", got.AnchorSHA, got.CheckedSHA, head)
	}
	if got.AnchorKind != "origin-main" {
		t.Errorf("anchor_kind = %q, want origin-main — a survived verify re-anchors against origin/main", got.AnchorKind)
	}
	if got.CheckedAt != 2_000 {
		t.Errorf("checked_at = %d, want 2000", got.CheckedAt)
	}
	if got.Ambiguous {
		t.Error("ambiguous stayed set after a re-anchor that passed false")
	}
	if got.CheckError != "" {
		t.Errorf("check_error = %q, want cleared — this pass READ the repo successfully", got.CheckError)
	}
	// The text and its hash are the anchor's identity and are NOT part of a
	// re-anchor's argument list, so they must survive untouched.
	if got.Text != a.Text || got.TextSHA != a.TextSHA {
		t.Errorf("a re-anchor rewrote the stored text/hash: %q/%q", got.Text, got.TextSHA)
	}

	// ambiguous=true is carried through on a relocation with several hits.
	if err := s.ReanchorMMCodeAnchor(item, "internal/jobs/mm.go", 40, 42, head, 3_000, true); err != nil {
		t.Fatalf("ReanchorMMCodeAnchor (ambiguous): %v", err)
	}
	if got, _, _ := s.GetMMCodeAnchor(item); !got.Ambiguous {
		t.Error("ambiguous=true was not recorded — a relocation with several hits survives WITH the flag")
	}
}

// TestContractMMReanchorSetsOrigPathOnceOnly pins the stated once-and-never-
// again clause: the FIRST rename records where the mark was originally taken,
// and a later rename must not overwrite it (the diff's `-M50%` rename detection
// needs both the original and the current path, and losing the original loses
// the mark's provenance).
func TestContractMMReanchorSetsOrigPathOnceOnly(t *testing.T) {
	s := contractMMStore(t)
	item := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", SourceRef: "kraken/old.go:1-2", Content: "x", CreatedAt: 10,
	})
	a := contractMMAnchorRow(item)
	a.Path = "internal/jobs/old.go"
	a.OrigPath = ""
	if err := s.AddMMCodeAnchor(a); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}

	// A re-anchor to the SAME path leaves orig_path empty: nothing was renamed.
	if err := s.ReanchorMMCodeAnchor(item, "internal/jobs/old.go", 1, 2, "aaa", 100, false); err != nil {
		t.Fatalf("ReanchorMMCodeAnchor (same path): %v", err)
	}
	if got, _, _ := s.GetMMCodeAnchor(item); got.OrigPath != "" {
		t.Fatalf("orig_path = %q after a re-anchor that did not move the file, want empty", got.OrigPath)
	}

	// The first rename records the OLD path.
	if err := s.ReanchorMMCodeAnchor(item, "internal/jobs/mid.go", 1, 2, "bbb", 200, false); err != nil {
		t.Fatalf("ReanchorMMCodeAnchor (first rename): %v", err)
	}
	got, _, _ := s.GetMMCodeAnchor(item)
	if got.OrigPath != "internal/jobs/old.go" {
		t.Fatalf("orig_path = %q, want the pre-rename path internal/jobs/old.go", got.OrigPath)
	}
	if got.Path != "internal/jobs/mid.go" {
		t.Fatalf("path = %q, want the new path", got.Path)
	}

	// A SECOND rename moves path again and leaves orig_path exactly where it
	// was — "set once, never overwritten".
	if err := s.ReanchorMMCodeAnchor(item, "internal/jobs/new.go", 1, 2, "ccc", 300, false); err != nil {
		t.Fatalf("ReanchorMMCodeAnchor (second rename): %v", err)
	}
	got, _, _ = s.GetMMCodeAnchor(item)
	if got.OrigPath != "internal/jobs/old.go" {
		t.Fatalf("orig_path = %q after a second rename, want the ORIGINAL internal/jobs/old.go", got.OrigPath)
	}
	if got.Path != "internal/jobs/new.go" {
		t.Fatalf("path = %q, want internal/jobs/new.go", got.Path)
	}
}

// TestContractMMSetCodeAnchorErrorTouchesNothingElse is the cross-slice
// invariant at this layer: "a failed read is never an invalidation". The verb
// stamps two columns and every other one — including the item's live-ness —
// comes out byte-identical.
func TestContractMMSetCodeAnchorErrorTouchesNothingElse(t *testing.T) {
	s := contractMMStore(t)
	item := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", SourceRef: "kraken/a.go:10-12", Content: "x", CreatedAt: 10,
	})
	before := contractMMAnchorRow(item)
	if err := s.AddMMCodeAnchor(before); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}

	if err := s.SetMMCodeAnchorError(item, 5_000, "git fetch origin main: context deadline exceeded"); err != nil {
		t.Fatalf("SetMMCodeAnchorError: %v", err)
	}
	got, ok, err := s.GetMMCodeAnchor(item)
	if err != nil || !ok {
		t.Fatalf("GetMMCodeAnchor: %+v, %t, %v", got, ok, err)
	}
	if got.CheckedAt != 5_000 {
		t.Errorf("checked_at = %d, want 5000", got.CheckedAt)
	}
	if got.CheckError != "git fetch origin main: context deadline exceeded" {
		t.Errorf("check_error = %q", got.CheckError)
	}
	// Everything else, restated as the row it started as with only the two
	// stamped columns updated.
	want := before
	want.CheckedAt = 5_000
	want.CheckError = "git fetch origin main: context deadline exceeded"
	if got != want {
		t.Fatalf("SetMMCodeAnchorError touched more than checked_at/check_error:\n got %+v\nwant %+v", got, want)
	}
	if it := contractMMGet(t, s, item); it.InvalidatedAt != 0 {
		t.Fatal("a failed read invalidated the item — check_error and invalidation are DISJOINT outcomes")
	}
}

// TestContractMMSetCodeAnchorInvalidMetaRecordsWhereItDied: the death record is
// the anchor's, while the death itself is the ITEM's (InvalidateMMItem) — two
// verbs because the two are separately recoverable.
func TestContractMMSetCodeAnchorInvalidMetaRecordsWhereItDied(t *testing.T) {
	s := contractMMStore(t)
	item := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", SourceRef: "kraken/a.go:10-12", Content: "x", CreatedAt: 10,
	})
	before := contractMMAnchorRow(item)
	if err := s.AddMMCodeAnchor(before); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	const head = "3333333333333333333333333333333333333333"
	const pr = "https://github.com/car1zhang/kraken/pull/231"
	if err := s.SetMMCodeAnchorInvalidMeta(item, head, pr); err != nil {
		t.Fatalf("SetMMCodeAnchorInvalidMeta: %v", err)
	}
	got, _, _ := s.GetMMCodeAnchor(item)
	if got.InvalidSHA != head || got.InvalidPR != pr {
		t.Fatalf("invalid_sha/invalid_pr = %q/%q, want %q/%q", got.InvalidSHA, got.InvalidPR, head, pr)
	}
	want := before
	want.InvalidSHA = head
	want.InvalidPR = pr
	if got != want {
		t.Fatalf("SetMMCodeAnchorInvalidMeta touched more than the two death columns:\n got %+v\nwant %+v", got, want)
	}
	// The PR url is best-effort: a death with no PR to name records the SHA
	// alone rather than refusing.
	if err := s.SetMMCodeAnchorInvalidMeta(item, head, ""); err != nil {
		t.Fatalf("SetMMCodeAnchorInvalidMeta with no PR url: %v — the url is best-effort", err)
	}
}

// ---------------------------------------------------------------------------
// cross-slice invariants
// ---------------------------------------------------------------------------

// TestContractMMWritesNeverBumpJobRevOrUpdatedAt is the first cross-slice
// invariant. `rev` is the job editor's optimistic lock and `updated_at` is what
// each board card renders as its AGE, so a mark taken while carl has the editor
// open must neither 409 his save nor reset the card's age — the SetJobLoad /
// SetPRWatermark treatment.
func TestContractMMWritesNeverBumpJobRevOrUpdatedAt(t *testing.T) {
	s := contractMMStore(t)
	job := contractMMJob(t, s, "steady")
	before, err := s.GetJobByID(job)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	id := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "artifact", JobID: job, Slug: "steady",
		SourceRef: "00-overview.md", Content: "00-overview.md", CreatedAt: 10,
	})
	if err := s.SetMMSnapshot(id, []byte("frozen"), ".md"); err != nil {
		t.Fatalf("SetMMSnapshot: %v", err)
	}
	if err := s.SetMMItemEpisode(id, 7); err != nil {
		t.Fatalf("SetMMItemEpisode: %v", err)
	}
	anchorItem := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", JobID: job, SourceRef: "kraken/a.go:1-1", Content: "x", CreatedAt: 20,
	})
	a := contractMMAnchorRow(anchorItem)
	if err := s.AddMMCodeAnchor(a); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	if err := s.ReanchorMMCodeAnchor(anchorItem, a.Path, 2, 4, "bbb", 30, false); err != nil {
		t.Fatalf("ReanchorMMCodeAnchor: %v", err)
	}
	if err := s.SetMMCodeAnchorError(anchorItem, 40, "boom"); err != nil {
		t.Fatalf("SetMMCodeAnchorError: %v", err)
	}
	if err := s.SetMMCodeAnchorInvalidMeta(anchorItem, "ccc", ""); err != nil {
		t.Fatalf("SetMMCodeAnchorInvalidMeta: %v", err)
	}
	if _, err := s.SupersedeLiveMMItems("artifact", job, "00-overview.md", 50, 0); err != nil {
		t.Fatalf("SupersedeLiveMMItems: %v", err)
	}
	if err := s.InvalidateMMItem(anchorItem, 60, "content-changed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}

	after, err := s.GetJobByID(job)
	if err != nil {
		t.Fatalf("GetJobByID after the mm writes: %v", err)
	}
	if after.Rev != before.Rev {
		t.Errorf("job rev moved %d -> %d across mm writes — an open editor save would 409 over a mark",
			before.Rev, after.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("job updated_at moved %d -> %d across mm writes — every card's AGE would reset on a mark",
			before.UpdatedAt, after.UpdatedAt)
	}
}

// TestContractMMRowsSurviveJobHardDelete is the second: no FK, no cascade. The
// item is what carl knows, and it outlives the job that occasioned it — while
// the job's OWN dependent rows (questions) go, which is what makes this a
// deliberate absence of a cascade rather than a database with none.
func TestContractMMRowsSurviveJobHardDelete(t *testing.T) {
	s := contractMMStore(t)
	job := contractMMJob(t, s, "doomed")
	if err := s.UpsertQuestion(job, DocQuestion{QID: "Q1", Text: "one", Importance: "P0"}); err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}

	item := contractMMAdd(t, s, MMItem{
		Kind: "decision", Source: "question", JobID: job, Slug: "doomed", SourceRef: "Q1",
		Content: "one\n→ do it", Context: "because", CreatedAt: 10,
	})
	if err := s.SetMMSnapshot(item, []byte("frozen"), ".md"); err != nil {
		t.Fatalf("SetMMSnapshot: %v", err)
	}
	anchorItem := contractMMAdd(t, s, MMItem{
		Kind: "reading", Source: "code", JobID: job, SourceRef: "kraken/a.go:1-1", Content: "x", CreatedAt: 20,
	})
	if err := s.AddMMCodeAnchor(contractMMAnchorRow(anchorItem)); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}

	if err := s.DeleteJob(job); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	// The job's own dependents cascaded away...
	qs, err := s.ListQuestions(job)
	if err != nil {
		t.Fatalf("ListQuestions after delete: %v", err)
	}
	if len(qs) != 0 {
		t.Fatalf("questions survived the hard delete: %+v — they cascade, mm rows do not", qs)
	}

	// ... and the mm rows did not.
	got := contractMMGet(t, s, item)
	if got.Content != "one\n→ do it" || got.Context != "because" || got.Slug != "doomed" || got.SourceRef != "Q1" {
		t.Fatalf("the decision item changed across the job's hard delete: %+v", got)
	}
	if got.JobID != job {
		t.Errorf("JobID = %d, want the (now dangling) %d — provenance is recorded, not enforced", got.JobID, job)
	}
	data, ext, err := s.GetMMSnapshot(item)
	if err != nil || string(data) != "frozen" || ext != ".md" {
		t.Fatalf("GetMMSnapshot after the job's delete = (%q, %q, %v), want the frozen bytes", data, ext, err)
	}
	anchor, ok, err := s.GetMMCodeAnchor(anchorItem)
	if err != nil || !ok {
		t.Fatalf("the code anchor did not survive the job's hard delete: %+v, %t, %v", anchor, ok, err)
	}
	live, err := s.ListMMItems(false)
	if err != nil {
		t.Fatalf("ListMMItems after delete: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("ListMMItems(false) = %v after the job's delete, want both rows", contractMMIDs(live))
	}
}

// ---------------------------------------------------------------------------
// watch_items (the Q9 rework: a standalone table, out of the graph)
// ---------------------------------------------------------------------------

func contractMMWatch(observation string) WatchItem {
	return WatchItem{Observation: observation}
}

// TestContractMMWatchAddInsertsWithTheStatedDefaults pins the INSERT arm: state
// defaults to `watching`, sightings is 1, and first_seen == last_seen == now.
func TestContractMMWatchAddInsertsWithTheStatedDefaults(t *testing.T) {
	s := contractMMStore(t)
	before := time.Now().UnixMilli()
	id, folded, err := s.AddWatchItem(contractMMWatch("hermes agents re-read the contract each round"))
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if folded {
		t.Fatal("the first add of an observation reported folded=true")
	}
	if id <= 0 {
		t.Fatalf("AddWatchItem returned id %d", id)
	}
	after := time.Now().UnixMilli()

	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Observation != "hermes agents re-read the contract each round" {
		t.Errorf("Observation = %q", got.Observation)
	}
	if got.State != "watching" {
		t.Errorf("State = %q, want the default %q", got.State, "watching")
	}
	if got.Sightings != 1 {
		t.Errorf("Sightings = %d, want 1", got.Sightings)
	}
	if got.FirstSeen != got.LastSeen {
		t.Errorf("first_seen (%d) != last_seen (%d) on a fresh row", got.FirstSeen, got.LastSeen)
	}
	if got.FirstSeen < before || got.FirstSeen > after {
		t.Errorf("first_seen = %d, want a stamp inside [%d, %d] (now)", got.FirstSeen, before, after)
	}
	if got.ExpiredAt != 0 {
		t.Errorf("ExpiredAt = %d on a fresh row, want 0 (live)", got.ExpiredAt)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Errorf("the carl-owned trio is pre-filled on a fresh row: %q/%q/%q", got.Verdict, got.Comment, got.CommentedAt)
	}
	// An explicit state on the way in is honoured — the default only fills a
	// blank.
	other, _, err := s.AddWatchItem(WatchItem{Observation: "another", State: "shipped"})
	if err != nil {
		t.Fatalf("AddWatchItem with an explicit state: %v", err)
	}
	if got, err := s.GetWatchItem(other); err != nil || got.State != "shipped" {
		t.Fatalf("explicit state came back %+v, %v; want state=shipped", got, err)
	}
}

// TestContractMMWatchAddFoldsOnAByteIdenticalObservation pins the FOLD arm in
// full: sightings+1, last_seen advanced, the non-empty agent fields updated, the
// carl trio untouched, the same id returned with folded=true — and first_seen
// preserved, since it is when the recurrence STARTED.
func TestContractMMWatchAddFoldsOnAByteIdenticalObservation(t *testing.T) {
	s := contractMMStore(t)
	const obs = "zeus re-reads the contract before every fix brief"
	id, _, err := s.AddWatchItem(WatchItem{
		Observation: obs, Class: "waste", State: "watching",
		Evidence: "first sighting", SuggestedFix: "cite the obligation",
	})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if err := s.SetWatchVerdict(id, "fix", "yes, do this"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	first, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}

	again, folded, err := s.AddWatchItem(WatchItem{
		Observation: obs, Class: "correction", State: "shipped",
		Evidence: "second sighting", SuggestedFix: "cite the obligation, harder",
		PRURL: "https://example.invalid/pr/1",
	})
	if err != nil {
		t.Fatalf("AddWatchItem (fold): %v", err)
	}
	if !folded {
		t.Fatal("a byte-identical observation created a second row instead of folding")
	}
	if again != id {
		t.Fatalf("the fold returned id %d, want the existing %d", again, id)
	}

	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after the fold: %v", err)
	}
	if got.Sightings != 2 {
		t.Errorf("Sightings = %d after one fold, want 2", got.Sightings)
	}
	if got.FirstSeen != first.FirstSeen {
		t.Errorf("first_seen moved %d -> %d on a fold — it is when the recurrence STARTED", first.FirstSeen, got.FirstSeen)
	}
	if got.LastSeen < first.LastSeen {
		t.Errorf("last_seen went backwards on a fold: %d -> %d", first.LastSeen, got.LastSeen)
	}
	// "non-empty agent fields of w ... updated"
	if got.Class != "correction" || got.State != "shipped" {
		t.Errorf("class/state = %q/%q, want correction/shipped", got.Class, got.State)
	}
	if got.Evidence != "second sighting" || got.SuggestedFix != "cite the obligation, harder" {
		t.Errorf("evidence/suggested_fix = %q/%q", got.Evidence, got.SuggestedFix)
	}
	if got.PRURL != "https://example.invalid/pr/1" {
		t.Errorf("pr_url = %q", got.PRURL)
	}
	// "carl trio untouched" — the whole point of the fold rather than an insert.
	if got.Verdict != "fix" || got.Comment != "yes, do this" || got.CommentedAt != first.CommentedAt {
		t.Fatalf("the fold rewrote the carl trio: %q/%q/%q, want fix/yes, do this/%q",
			got.Verdict, got.Comment, got.CommentedAt, first.CommentedAt)
	}

	// A fold that carries EMPTY agent fields leaves the stored ones alone —
	// "non-empty ... updated" is the whole clause.
	if _, _, err := s.AddWatchItem(WatchItem{Observation: obs}); err != nil {
		t.Fatalf("AddWatchItem (empty fold): %v", err)
	}
	got, err = s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after the empty fold: %v", err)
	}
	if got.Class != "correction" || got.Evidence != "second sighting" {
		t.Errorf("an empty fold blanked the agent fields: class=%q evidence=%q", got.Class, got.Evidence)
	}
	if got.Sightings != 3 {
		t.Errorf("Sightings = %d after two folds, want 3", got.Sightings)
	}

	// A DIFFERENT observation is a different row, not a fold.
	fresh, folded, err := s.AddWatchItem(contractMMWatch(obs + " "))
	if err != nil {
		t.Fatalf("AddWatchItem (near-identical): %v", err)
	}
	if folded || fresh == id {
		t.Fatal("a one-byte-different observation folded — the fold key is BYTE-identical text")
	}
}

// TestContractMMWatchListIsLiveOnlyAndStateFiltered pins ListWatchItems: live
// rows only, and `state == ""` meaning all live states rather than "rows with a
// blank state".
func TestContractMMWatchListIsLiveOnlyAndStateFiltered(t *testing.T) {
	s := contractMMStore(t)
	watching, _, err := s.AddWatchItem(WatchItem{Observation: "a", State: "watching"})
	if err != nil {
		t.Fatal(err)
	}
	shipped, _, err := s.AddWatchItem(WatchItem{Observation: "b", State: "shipped"})
	if err != nil {
		t.Fatal(err)
	}
	dead, _, err := s.AddWatchItem(WatchItem{Observation: "c", State: "watching"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireWatchItem(dead, 5_000); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems(\"\"): %v", err)
	}
	if got := contractMMWatchIDs(all); len(got) != 2 {
		t.Fatalf("ListWatchItems(\"\") = %v, want the two LIVE rows %v", got, []int64{watching, shipped})
	}
	for _, w := range all {
		if w.ID == dead {
			t.Fatal("an expired row is still listed — ListWatchItems is live rows only")
		}
	}

	only, err := s.ListWatchItems("shipped")
	if err != nil {
		t.Fatalf("ListWatchItems(\"shipped\"): %v", err)
	}
	if len(only) != 1 || only[0].ID != shipped {
		t.Fatalf("ListWatchItems(\"shipped\") = %v, want [%d]", contractMMWatchIDs(only), shipped)
	}
}

func contractMMWatchIDs(items []WatchItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, w := range items {
		out = append(out, w.ID)
	}
	return out
}

// TestContractMMWatchUpdateWritesAgentFieldsAndPreservesTheCarlTrio pins the
// ownership split that survives the rework verbatim: UpdateWatchItem is the
// agent's half and must never touch verdict/comment/commented_at.
func TestContractMMWatchUpdateWritesAgentFieldsAndPreservesTheCarlTrio(t *testing.T) {
	s := contractMMStore(t)
	id, _, err := s.AddWatchItem(WatchItem{Observation: "obs", Class: "waste", State: "watching"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWatchVerdict(id, "fine", "leave it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	before, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateWatchItem(id, WatchItem{
		Class: "correction", State: "shipped", Evidence: "measured twice",
		SuggestedFix: "pin it", PRURL: "https://example.invalid/pr/9", UpdatedAt: 4242,
	}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Class != "correction" || got.State != "shipped" || got.Evidence != "measured twice" ||
		got.SuggestedFix != "pin it" || got.PRURL != "https://example.invalid/pr/9" {
		t.Fatalf("the agent fields did not land: %+v", got)
	}
	if got.Verdict != before.Verdict || got.Comment != before.Comment || got.CommentedAt != before.CommentedAt {
		t.Fatalf("UpdateWatchItem rewrote the carl trio: %q/%q/%q, want %q/%q/%q",
			got.Verdict, got.Comment, got.CommentedAt, before.Verdict, before.Comment, before.CommentedAt)
	}
	if got.Observation != "obs" {
		t.Errorf("Observation = %q — the fold key is not an agent field", got.Observation)
	}
}

// TestContractMMWatchSetVerdictWritesTheCarlTrioAndStampsRFC3339 pins the other
// half: three columns, a server-stamped commented_at in RFC3339 UTC (the verb
// no longer takes one), and the agent fields untouched.
func TestContractMMWatchSetVerdictWritesTheCarlTrioAndStampsRFC3339(t *testing.T) {
	s := contractMMStore(t)
	id, _, err := s.AddWatchItem(WatchItem{
		Observation: "obs", Class: "waste", State: "watching", Evidence: "e", SuggestedFix: "f",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetWatchVerdict(id, "fix", "agreed, ship it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fix" || got.Comment != "agreed, ship it" {
		t.Fatalf("verdict/comment = %q/%q", got.Verdict, got.Comment)
	}
	stamped, err := time.Parse(time.RFC3339, got.CommentedAt)
	if err != nil {
		t.Fatalf("commented_at = %q, which does not parse as RFC3339: %v", got.CommentedAt, err)
	}
	if _, offset := stamped.Zone(); offset != 0 {
		t.Errorf("commented_at = %q, want UTC (zero offset)", got.CommentedAt)
	}
	if got.Class != before.Class || got.State != before.State ||
		got.Evidence != before.Evidence || got.SuggestedFix != before.SuggestedFix {
		t.Fatalf("SetWatchVerdict rewrote the agent fields: %+v", got)
	}
	if got.Sightings != before.Sightings || got.FirstSeen != before.FirstSeen {
		t.Errorf("SetWatchVerdict moved the sighting bookkeeping: %d/%d", got.Sightings, got.FirstSeen)
	}
}

// TestContractMMWatchExpireClosesTheRowAndKeepsItReadable pins the replacement
// for delete: "no watch row is ever deleted", already-expired is success, and
// an expired row is STILL readable by id (the new clause — a dream report
// citing a closed observation must still resolve it).
func TestContractMMWatchExpireClosesTheRowAndKeepsItReadable(t *testing.T) {
	s := contractMMStore(t)
	id, _, err := s.AddWatchItem(WatchItem{Observation: "obs", Class: "waste", State: "watching"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWatchVerdict(id, "fix", "do it"); err != nil {
		t.Fatal(err)
	}

	if err := s.ExpireWatchItem(id, 7_777); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}
	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem on an expired row: %v — expired rows stay readable by id", err)
	}
	if got.ExpiredAt != 7_777 {
		t.Errorf("ExpiredAt = %d, want the passed 7777", got.ExpiredAt)
	}
	if got.Observation != "obs" || got.Verdict != "fix" || got.Comment != "do it" {
		t.Errorf("the closed row lost content: %+v", got)
	}

	// "already-expired -> success", and the first close's stamp stands.
	if err := s.ExpireWatchItem(id, 9_999); err != nil {
		t.Fatalf("second ExpireWatchItem: %v — already-expired is success", err)
	}
	if again, err := s.GetWatchItem(id); err != nil || again.ExpiredAt == 0 {
		t.Fatalf("the row came back live after a second expire: %+v, %v", again, err)
	}

	// A closed observation no longer folds: the next sighting starts a fresh
	// row, since the fold arm is defined over LIVE rows.
	fresh, folded, err := s.AddWatchItem(contractMMWatch("obs"))
	if err != nil {
		t.Fatalf("AddWatchItem after expiry: %v", err)
	}
	if folded || fresh == id {
		t.Fatal("an EXPIRED row absorbed a new sighting — the fold arm is over live rows only")
	}
}

// ---------------------------------------------------------------------------
// the v29 migration
// ---------------------------------------------------------------------------

func contractMMTableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&n)
	if err != nil {
		t.Fatalf("look up table %s: %v", name, err)
	}
	return n > 0
}

func contractMMCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestContractMMV29ShapeOnAFreshDatabase pins the migration's structural half
// against a database opened from nothing: the three new tables exist, kg_watch
// is gone, and the episode-kind CHECK admits `mental-model` alongside the
// legacy five.
func TestContractMMV29ShapeOnAFreshDatabase(t *testing.T) {
	s := contractMMStore(t)

	for _, table := range []string{"mm_items", "mm_code_anchors", "watch_items"} {
		if !contractMMTableExists(t, s, table) {
			t.Errorf("table %s does not exist after v29", table)
		}
	}
	if contractMMTableExists(t, s, "kg_watch") {
		t.Error("kg_watch still exists after v29 — Q9 moves the watchlist out of the graph entirely")
	}

	// "kg_episodes.kind admits `mental-model` AND the legacy five" — measured
	// through AddEpisode, since the CHECK is what would refuse the insert.
	for i, kind := range []string{
		"mental-model", "transcript", "feedback", "watchlist", "artifact", "job",
	} {
		_, added, err := s.AddEpisode(Episode{
			Kind: kind, Ref: fmt.Sprintf("ref-%d", i), Content: "c",
			Hash: fmt.Sprintf("hash-%d", i), TS: 1,
		})
		if err != nil {
			t.Errorf("AddEpisode(kind=%q): %v — the rebuilt CHECK must admit it", kind, err)
			continue
		}
		if !added {
			t.Errorf("AddEpisode(kind=%q) reported the row as a duplicate on a fresh db", kind)
		}
	}
}

// contractMMOpenV28 builds a genuine v28-shaped database by applying
// migrations[1..28] the way migrate() does (SQL then hook, user_version stamped
// last) and hands back the still-open handle plus its path. Seed the kg_*
// fixtures through the handle, then close it and re-Open the path: THAT runs
// migrations[29] and its hook for real.
//
// Built this way rather than through schema_test.go's downgradeFreshDB so the
// fixture measures the v28→v29 walk on its own terms, and so a forgotten v29
// downgrade arm shows up as its own failure (below) rather than as a confusing
// one here.
func contractMMOpenV28(t *testing.T) (*sql.DB, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")
	dsn := fmt.Sprintf(
		"file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open v28 db: %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin v28 build: %v", err)
	}
	for v := 1; v <= 28; v++ {
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
	if _, err := tx.Exec(`PRAGMA user_version = 28`); err != nil {
		tx.Rollback()
		t.Fatalf("stamp user_version 28: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v28 build: %v", err)
	}
	return db, path
}

func contractMMMigrateV28(t *testing.T, db *sql.DB, path string) *Store {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close the v28 handle: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate a v28 database forward: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func contractMMSeedNode(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO kg_nodes (name, type, description, embedding, created_at, updated_at)
			VALUES (?, 'concept', '', x'', 1, 1)`, name)
	if err != nil {
		t.Fatalf("seed node %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func contractMMSeedEpisode(t *testing.T, db *sql.DB, ref string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
			VALUES ('watchlist', ?, 'c', ?, 1, 'done', 1)`, ref, "hash-"+ref)
	if err != nil {
		t.Fatalf("seed episode %q: %v", ref, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func contractMMSeedEdge(t *testing.T, db *sql.DB, src, dst, episode int64, predicate, fact string, expiredAt int64) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO kg_edges
			(src_id, dst_id, predicate, fact, episode_id, strength, occurrences,
			 valid_from, valid_until, embedding, created_at, expired_at)
			VALUES (?, ?, ?, ?, ?, 5, 1, 0, 0, x'', 1, ?)`,
		src, dst, predicate, fact, episode, expiredAt)
	if err != nil {
		t.Fatalf("seed edge %q: %v", fact, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func contractMMSeedOverlay(t *testing.T, db *sql.DB, edgeID int64, w WatchItem) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO kg_watch
		(edge_id, class, state, sightings, first_seen, last_seen, evidence,
		 suggested_fix, pr_url, updated_at, verdict, comment, commented_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		edgeID, w.Class, w.State, w.Sightings, w.FirstSeen, w.LastSeen, w.Evidence,
		w.SuggestedFix, w.PRURL, w.UpdatedAt, w.Verdict, w.Comment, w.CommentedAt)
	if err != nil {
		t.Fatalf("seed kg_watch overlay for edge %d: %v", edgeID, err)
	}
}

// TestContractMMV29CopiesEveryLiveWatchedEdgeIntoWatchItems is the migration's
// data half: a real v28 fixture carrying watched edges walks forward, and every
// formerly-LIVE one comes out as one standalone watch_items row with its
// observation, its endpoints' NAMES, its predicate, every overlay column and
// the carl trio — while the graph itself is emptied.
func TestContractMMV29CopiesEveryLiveWatchedEdgeIntoWatchItems(t *testing.T) {
	db, path := contractMMOpenV28(t)

	agents := contractMMSeedNode(t, db, "hermes agents")
	contracts := contractMMSeedNode(t, db, "the frozen contract")
	ep := contractMMSeedEpisode(t, db, "watch:1")

	const liveFact = "hermes agents re-read the contract on every lint round"
	liveEdge := contractMMSeedEdge(t, db, agents, contracts, ep, "re-read", liveFact, 0)
	contractMMSeedOverlay(t, db, liveEdge, WatchItem{
		Class: "waste", State: "shipped", Sightings: 6,
		FirstSeen: 1_700_000_000_000, LastSeen: 1_700_500_000_000,
		Evidence: "measured over four cells", SuggestedFix: "cache the read",
		PRURL: "https://example.invalid/pr/12", UpdatedAt: 1_700_600_000_000,
		Verdict: "fix", Comment: "agreed", CommentedAt: "2026-08-01T12:00:00Z",
	})

	// A second live one, so "every" is measured rather than "the one".
	const secondFact = "zeus cites the failing assertion instead of the obligation"
	secondEdge := contractMMSeedEdge(t, db, contracts, agents, ep, "cites", secondFact, 0)
	contractMMSeedOverlay(t, db, secondEdge, WatchItem{
		Class: "correction", State: "watching", Sightings: 1,
		FirstSeen: 1_700_100_000_000, LastSeen: 1_700_100_000_000,
	})

	// A watched edge whose EDGE was already world-closed: the migration copies
	// the formerly-LIVE set, so this one does not come across.
	const deadFact = "an observation whose edge was already invalidated"
	deadEdge := contractMMSeedEdge(t, db, agents, contracts, ep, "was", deadFact, 1_699_000_000_000)
	contractMMSeedOverlay(t, db, deadEdge, WatchItem{Class: "waste", State: "declined", Sightings: 2})

	s := contractMMMigrateV28(t, db, path)

	// version advanced and the shape landed
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after the v28 walk, want %d", version, schemaVersion)
	}
	if contractMMTableExists(t, s, "kg_watch") {
		t.Error("kg_watch survived v29")
	}
	for _, table := range []string{"mm_items", "mm_code_anchors", "watch_items"} {
		if !contractMMTableExists(t, s, table) {
			t.Errorf("table %s does not exist after migrating a v28 fixture", table)
		}
	}

	// "every formerly-live watched edge is one watch_items row"
	rows, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems after migration: %v", err)
	}
	byObs := map[string]WatchItem{}
	for _, w := range rows {
		byObs[w.Observation] = w
	}
	if len(rows) != 2 {
		t.Fatalf("ListWatchItems returned %d rows, want 2 (the two LIVE watched edges); got %+v", len(rows), byObs)
	}
	if _, ok := byObs[deadFact]; ok {
		t.Error("the already-invalidated edge's overlay was migrated — the copy is the formerly-LIVE set")
	}

	got, ok := byObs[liveFact]
	if !ok {
		t.Fatalf("the live watched edge did not come across; rows = %+v", byObs)
	}
	if got.Src != "hermes agents" || got.Dst != "the frozen contract" {
		t.Errorf("src/dst = %q/%q, want the NODE NAMES hermes agents / the frozen contract", got.Src, got.Dst)
	}
	if got.Predicate != "re-read" {
		t.Errorf("predicate = %q, want re-read", got.Predicate)
	}
	if got.Class != "waste" || got.State != "shipped" || got.Sightings != 6 {
		t.Errorf("class/state/sightings = %q/%q/%d, want waste/shipped/6", got.Class, got.State, got.Sightings)
	}
	if got.FirstSeen != 1_700_000_000_000 || got.LastSeen != 1_700_500_000_000 {
		t.Errorf("first/last seen = %d/%d, want them PRESERVED (1700000000000/1700500000000)", got.FirstSeen, got.LastSeen)
	}
	if got.Evidence != "measured over four cells" || got.SuggestedFix != "cache the read" {
		t.Errorf("evidence/suggested_fix = %q/%q", got.Evidence, got.SuggestedFix)
	}
	if got.PRURL != "https://example.invalid/pr/12" || got.UpdatedAt != 1_700_600_000_000 {
		t.Errorf("pr_url/updated_at = %q/%d", got.PRURL, got.UpdatedAt)
	}
	if got.Verdict != "fix" || got.Comment != "agreed" || got.CommentedAt != "2026-08-01T12:00:00Z" {
		t.Errorf("the carl trio did not survive the copy: %q/%q/%q", got.Verdict, got.Comment, got.CommentedAt)
	}
	if got.ExpiredAt != 0 {
		t.Errorf("the copied row reads expired (%d) — it was a LIVE observation", got.ExpiredAt)
	}
	if got.ID <= 0 {
		t.Errorf("the copied row has id %d — watch ids are now watch-row ids", got.ID)
	}

	second, ok := byObs[secondFact]
	if !ok {
		t.Fatalf("the second live watched edge did not come across; rows = %+v", byObs)
	}
	if second.Class != "correction" || second.State != "watching" || second.Sightings != 1 {
		t.Errorf("the second row = %+v", second)
	}

	// "kg_edges, kg_episodes, kg_nodes, kg_node_aliases, kg_recall, kg_layout,
	// kg_ingest_cursor are EMPTY; kg_edges_fts is empty"
	for _, table := range []string{
		"kg_edges", "kg_episodes", "kg_nodes", "kg_node_aliases",
		"kg_recall", "kg_layout", "kg_ingest_cursor", "kg_edges_fts",
	} {
		if n := contractMMCount(t, s, table); n != 0 {
			t.Errorf("%s has %d rows after v29, want 0 — the cutover WIPES the graph", table, n)
		}
	}

	// The migrated rows are ordinary watch_items rows: a fold on one of them
	// works, and it does not resurrect an edge.
	again, folded, err := s.AddWatchItem(WatchItem{Observation: liveFact, Evidence: "seen again"})
	if err != nil {
		t.Fatalf("AddWatchItem onto a migrated row: %v", err)
	}
	if !folded || again != got.ID {
		t.Fatalf("a re-sighting of a migrated observation = (%d, %t), want (%d, true)", again, folded, got.ID)
	}
	if n := contractMMCount(t, s, "kg_edges"); n != 0 {
		t.Errorf("a watch add created %d graph edges — watch adds no longer touch the graph at all", n)
	}
	if n := contractMMCount(t, s, "kg_episodes"); n != 0 {
		t.Errorf("a watch add created %d episodes — watch adds no longer mint one", n)
	}
}

// TestContractMMV29HasItsDowngradeCompanion pins the "known-forgotten set" the
// contract names explicitly: schema_test.go's downgradeFreshDB must learn a v29
// arm, or every existing vN-shaped test in this package silently measures a
// database shape that was never real. Reached through the helper itself, since
// that is the thing that has to change.
func TestContractMMV29HasItsDowngradeCompanion(t *testing.T) {
	s := contractMMStore(t)

	downgradeFreshDB(t, s, 28)
	if contractMMTableExists(t, s, "mm_items") {
		t.Fatal("downgradeFreshDB(28) left mm_items in place — a v28 database never had one, " +
			"and v29's own CREATE will fail against it")
	}
	if !contractMMTableExists(t, s, "kg_watch") {
		t.Fatal("downgradeFreshDB(28) did not put kg_watch back — a v28 database HAS one, " +
			"and v29's copy hook reads it")
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate a v28-shaped database forward: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after the round trip, want %d", version, schemaVersion)
	}
	if contractMMTableExists(t, s, "kg_watch") {
		t.Error("kg_watch is back after re-migrating")
	}
	// ... and the tables are usable again, rather than merely present.
	id, err := s.AddMMItem(contractMMNote("post-round-trip", 10))
	if err != nil {
		t.Fatalf("AddMMItem after the downgrade round trip: %v", err)
	}
	if _, err := s.GetMMItem(id); err != nil {
		t.Fatalf("GetMMItem after the downgrade round trip: %v", err)
	}
	if _, _, err := s.AddWatchItem(contractMMWatch("post-round-trip")); err != nil {
		t.Fatalf("AddWatchItem after the downgrade round trip: %v", err)
	}
}
