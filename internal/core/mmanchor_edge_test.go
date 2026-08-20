package core

import (
	"reflect"
	"strings"
	"testing"
)

// mmanchor_edge_test.go — the EDGE dimension of 50-contract.md's slice S1 pure
// half: the five anchor-math functions in internal/core (ParseHunks, MapRange,
// LocateText, StripWS, EndRowFromSelection), probed at their boundaries, their
// degenerate inputs and the shapes that must NOT decide anything. Written blind
// from the contract text alone — internal/core/anchor.go was never read.
//
// The sibling blind author (athena-logic) owns the stated happy paths and the
// invariants. What lives here is the other half, and every case below is a
// boundary the verify pass would silently get wrong: a hunk that lands exactly
// ON the range's first or last line, a deletion whose NewStart-OldStart differs
// from its real delta, a selection that ends in a newline, a whitespace rune
// that is not a space.
//
// FILE NAME: the brief named internal/core/anchor_edge_test.go, but that path is
// ALREADY TAKEN in this repo by the graph anchor leg's own edge suite
// (core.AnchorNodes, TestEdgeAnchor…). Writing there would have deleted a live
// test file, so this file takes the mm-scoped name instead. Its package-level
// identifiers are all prefixed `edgeMM` — `edge` per the brief, `MM` because the
// existing anchor_edge_test.go already owns edgeAnchor…/edgeStore in this same
// package namespace.

// ---------------------------------------------------------------------------
// ParseHunks — what is NOT a hunk header
// ---------------------------------------------------------------------------

// edgeMMEmptyDiffs are the diff bodies that carry no header at all: the empty
// string (git printed nothing because nothing changed under the pathspec), a
// binary-file notice, and a rename-only diff — the exact shapes the pass meets
// on a quiet repo.
var edgeMMEmptyDiffs = []struct{ name, diff string }{
	{"empty output", ""},
	{"whitespace only", "\n\n"},
	{"binary notice", "Binary files a/x.png and b/x.png differ\n"},
	{"rename with no content change", strings.Join([]string{
		"diff --git a/a.go b/b.go",
		"similarity index 100%",
		"rename from a.go",
		"rename to b.go",
	}, "\n")},
}

// TestEdgeMMParseHunksNoHeadersIsNil pins the contract's literal word: "no
// headers → nil", not an empty slice. A non-nil empty slice reads the same to
// len(), which is exactly why only a reflect-level check catches a []Hunk{}
// return before it becomes the shape every caller quietly depends on.
func TestEdgeMMParseHunksNoHeadersIsNil(t *testing.T) {
	for _, c := range edgeMMEmptyDiffs {
		if got := ParseHunks(c.diff); got != nil {
			t.Errorf("%s: ParseHunks(%q) = %#v, want nil", c.name, c.diff, got)
		}
	}
}

// TestEdgeMMParseHunksIgnoresPreambleAndContent covers everything git puts in a
// `--unified=0` diff that is not a header: the rename preamble (which the
// contract names outright), the index/---/+++ lines, and — the case with real
// teeth — a CONTENT line whose own text begins with `@@`. With `--unified=0`
// there is no context, so every non-header line is a `+` or `-` line, and a
// parser matching `@@` anywhere in the line rather than at its start would
// invent a hunk out of a diff of this very test file.
func TestEdgeMMParseHunksIgnoresPreambleAndContent(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/old/path.go b/new/path.go",
		"similarity index 92%",
		"rename from old/path.go",
		"rename to new/path.go",
		"index 1234567..89abcde 100644",
		"--- a/old/path.go",
		"+++ b/new/path.go",
		"@@ -10,1 +10,1 @@",
		"-was",
		"+@@ -1,1 +1,1 @@",
		" @@ -2,2 +2,2 @@",
	}, "\n")
	want := []Hunk{{OldStart: 10, OldCount: 1, NewStart: 10, NewCount: 1}}
	if got := ParseHunks(diff); !reflect.DeepEqual(got, want) {
		t.Errorf("ParseHunks over a rename diff = %#v, want exactly %#v — only the real header line counts", got, want)
	}
}

// TestEdgeMMParseHunksOmittedCountsAreOne is the contract's "an omitted count
// means 1", asserted on all three shapes git actually emits: both sides
// omitted, and each side omitted alone. Reading an omitted count as 0 would turn
// a one-line edit into a pure insertion, and pure insertions are the one hunk
// class MapRange treats as never touching a range they sit above.
func TestEdgeMMParseHunksOmittedCountsAreOne(t *testing.T) {
	for _, c := range []struct {
		diff string
		want Hunk
	}{
		{"@@ -4 +6 @@", Hunk{OldStart: 4, OldCount: 1, NewStart: 6, NewCount: 1}},
		{"@@ -4 +6,3 @@", Hunk{OldStart: 4, OldCount: 1, NewStart: 6, NewCount: 3}},
		{"@@ -4,3 +6 @@", Hunk{OldStart: 4, OldCount: 3, NewStart: 6, NewCount: 1}},
		{"@@ -0,0 +1 @@", Hunk{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 1}},
	} {
		got := ParseHunks(c.diff)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("ParseHunks(%q) = %#v, want [%#v]", c.diff, got, c.want)
		}
	}
}

// TestEdgeMMParseHunksZeroCountsSurviveAsZero covers the two degenerate counts
// that carry the most meaning downstream: a pure insertion's `-a,0` and a pure
// deletion's `+c,0`. MapRange keys its whole insertion branch on OldCount==0,
// and the delta it applies for a deletion is NewCount-OldCount — so a parser
// that "helpfully" floored either count at 1 would both mis-classify the hunk
// and mis-shift every anchor below it.
func TestEdgeMMParseHunksZeroCountsSurviveAsZero(t *testing.T) {
	for _, c := range []struct {
		diff string
		want Hunk
	}{
		{"@@ -0,0 +1,3 @@", Hunk{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 3}},
		{"@@ -12,0 +13,2 @@", Hunk{OldStart: 12, OldCount: 0, NewStart: 13, NewCount: 2}},
		{"@@ -5,2 +5,0 @@", Hunk{OldStart: 5, OldCount: 2, NewStart: 5, NewCount: 0}},
	} {
		got := ParseHunks(c.diff)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("ParseHunks(%q) = %#v, want [%#v]", c.diff, got, c.want)
		}
	}
}

// TestEdgeMMParseHunksKeepsHeadersInOrder pins the ordering the whole of
// MapRange's "accumulated shift from strictly-earlier hunks" rests on: git emits
// hunks in ascending old-line order, and a parser that sorted, reversed or
// deduped them would make every shift below the second hunk wrong without
// changing any single hunk's contents.
func TestEdgeMMParseHunksKeepsHeadersInOrder(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -3,1 +3,2 @@ func a() {",
		"-x",
		"+x",
		"+y",
		"@@ -20,0 +22,1 @@ func b() {",
		"+z",
		"@@ -40,2 +43,0 @@",
		"-p",
		"-q",
	}, "\n")
	want := []Hunk{
		{OldStart: 3, OldCount: 1, NewStart: 3, NewCount: 2},
		{OldStart: 20, OldCount: 0, NewStart: 22, NewCount: 1},
		{OldStart: 40, OldCount: 2, NewStart: 43, NewCount: 0},
	}
	if got := ParseHunks(diff); !reflect.DeepEqual(got, want) {
		t.Errorf("ParseHunks = %#v, want %#v (in emitted order)", got, want)
	}
}

// TestEdgeMMParseHunksMalformedHeadersAreNotHunks covers the line that opens
// with `@@` but is not a header git ever wrote. The parser's job is to read
// git's output, and a malformed line is evidence something upstream is wrong —
// inventing a Hunk{0,0,0,0} out of it would feed MapRange an insertion at line
// 0, which shifts nothing and touches nothing, i.e. it would be silently
// mistaken for "this file is unchanged".
func TestEdgeMMParseHunksMalformedHeadersAreNotHunks(t *testing.T) {
	for _, diff := range []string{
		"@@ -a,b +c,d @@",
		"@@ -1,1 +1,1",
		"@@@ -1,1 +1,1 @@@",
		"@@ +1,1 -1,1 @@",
		"@@",
		"@@ -1,1 @@",
	} {
		if got := ParseHunks(diff); got != nil {
			t.Errorf("ParseHunks(%q) = %#v, want nil — that is not a header git emits", diff, got)
		}
	}
}

// ---------------------------------------------------------------------------
// MapRange — the four boundaries a hunk can land on
// ---------------------------------------------------------------------------

// TestEdgeMMMapRangeNoHunksIsIdentity is the state every unchanged file is in:
// git printed no hunks, so the anchor survives at exactly its old coordinates.
// Both the nil and the empty slice are covered because ParseHunks' own contract
// makes nil the normal spelling and a future caller could pass either.
func TestEdgeMMMapRangeNoHunksIsIdentity(t *testing.T) {
	for _, hunks := range [][]Hunk{nil, {}} {
		s, e, touched := MapRange(hunks, 10, 12)
		if s != 10 || e != 12 || touched {
			t.Errorf("MapRange(%#v, 10, 12) = (%d, %d, %v), want (10, 12, false)", hunks, s, e, touched)
		}
	}
}

// TestEdgeMMMapRangeInsertionDirectlyAboveShifts is the anchorStart-1 case the
// testing plan names first. An insertion AFTER old line start-1 lands wholly
// above the range: it shifts by NewCount and must NOT touch. Getting this wrong
// is the common invalidation — every mark below a newly added import block would
// go to the text ladder on every pass.
func TestEdgeMMMapRangeInsertionDirectlyAboveShifts(t *testing.T) {
	// three lines inserted after old line 9; the anchor is 10-12
	h := []Hunk{{OldStart: 9, OldCount: 0, NewStart: 10, NewCount: 3}}
	s, e, touched := MapRange(h, 10, 12)
	if touched {
		t.Error("an insertion strictly above the range reported touched")
	}
	if s != 13 || e != 15 {
		t.Errorf("MapRange = (%d, %d), want (13, 15) — shifted by NewCount", s, e)
	}
}

// TestEdgeMMMapRangeInsertionAtTheLastLineIsANoOp is the other named boundary:
// an insertion AT the range's last line inserts after it, i.e. below the block,
// and must neither shift nor touch. `OldStart >= end` is the contract's own
// words, so end itself is on the no-effect side of the boundary — the single
// most likely off-by-one in the whole function.
func TestEdgeMMMapRangeInsertionAtTheLastLineIsANoOp(t *testing.T) {
	h := []Hunk{{OldStart: 12, OldCount: 0, NewStart: 13, NewCount: 5}}
	s, e, touched := MapRange(h, 10, 12)
	if touched {
		t.Error("an insertion at the range's LAST line reported touched — it inserts after it")
	}
	if s != 10 || e != 12 {
		t.Errorf("MapRange = (%d, %d), want (10, 12) — an insertion below the block shifts nothing", s, e)
	}
}

// TestEdgeMMMapRangeInsertionStrictlyInsideTouches walks the inside of the range
// one line at a time. `start <= OldStart < end` is the touching band, so the
// FIRST line of the range is inside (an insertion after it splits the block)
// while the last is not — the two ends of the band, asserted together so a
// shifted band cannot pass by getting one of them right.
func TestEdgeMMMapRangeInsertionStrictlyInsideTouches(t *testing.T) {
	for _, at := range []int{10, 11} {
		h := []Hunk{{OldStart: at, OldCount: 0, NewStart: at + 1, NewCount: 2}}
		s, e, touched := MapRange(h, 10, 12)
		if !touched {
			t.Errorf("insertion after old line %d inside 10-12 reported touched=false", at)
		}
		// the shift comes from strictly-earlier hunks only, and there are none
		if s != 10 || e != 12 {
			t.Errorf("insertion after old line %d: MapRange = (%d, %d), want (10, 12) — an intersecting hunk contributes no shift", at, s, e)
		}
	}
}

// TestEdgeMMMapRangeSingleLineRangeIsAllBoundary is the degenerate range every
// `k mm mark` with an empty selection produces (start == end, one line). Both
// insertion branches collapse onto it: OldStart == start is now also
// OldStart >= end, so the no-effect arm wins and a one-line mark can never be
// touched by an insertion at its own line. The multi-line case above returns
// touched for the same OldStart, which is exactly why this needs its own case.
func TestEdgeMMMapRangeSingleLineRangeIsAllBoundary(t *testing.T) {
	for _, c := range []struct {
		name    string
		hunk    Hunk
		wantS   int
		wantE   int
		touched bool
	}{
		{"insertion after the line itself", Hunk{OldStart: 7, OldCount: 0, NewStart: 8, NewCount: 4}, 7, 7, false},
		{"insertion after the line above", Hunk{OldStart: 6, OldCount: 0, NewStart: 7, NewCount: 4}, 11, 11, false},
		{"a replacement OF the line", Hunk{OldStart: 7, OldCount: 1, NewStart: 7, NewCount: 1}, 7, 7, true},
	} {
		s, e, touched := MapRange([]Hunk{c.hunk}, 7, 7)
		if s != c.wantS || e != c.wantE || touched != c.touched {
			t.Errorf("%s: MapRange = (%d, %d, %v), want (%d, %d, %v)", c.name, s, e, touched, c.wantS, c.wantE, c.touched)
		}
	}
}

// TestEdgeMMMapRangePureDeletionAboveUsesTheCountDelta is the contract's
// loudest "never": the shift is NewCount-OldCount, never NewStart-OldStart. This
// fixture is built so the two DISAGREE — three lines deleted at line 1 leaves
// NewStart == OldStart == 1 (delta 0) while the real delta is -3 — so an
// implementation reading the start columns passes every other case in this file
// and fails only here, which is the whole point of the case.
func TestEdgeMMMapRangePureDeletionAboveUsesTheCountDelta(t *testing.T) {
	h := []Hunk{{OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 0}}
	s, e, touched := MapRange(h, 10, 12)
	if touched {
		t.Error("a deletion entirely above the range reported touched")
	}
	if s != 7 || e != 9 {
		t.Errorf("MapRange = (%d, %d), want (7, 9) — shift is NewCount-OldCount (-3), not NewStart-OldStart (0)", s, e)
	}
}

// TestEdgeMMMapRangeNonInsertionEndingJustAboveShifts pins the "entirely above"
// arithmetic at its exact edge: a hunk whose LAST old line is start-1. One
// higher and it intersects; this is the last position from which a change is
// provably not about the anchored text.
func TestEdgeMMMapRangeNonInsertionEndingJustAboveShifts(t *testing.T) {
	// old lines 7,8,9 replaced by 5 lines: last old line 9 == start-1
	h := []Hunk{{OldStart: 7, OldCount: 3, NewStart: 7, NewCount: 5}}
	s, e, touched := MapRange(h, 10, 12)
	if touched {
		t.Error("a hunk ending exactly at start-1 reported touched")
	}
	if s != 12 || e != 14 {
		t.Errorf("MapRange = (%d, %d), want (12, 14) — shifted by +2", s, e)
	}
}

// TestEdgeMMMapRangeNonInsertionOverlappingEitherEndTouches walks the two
// positions immediately inside the boundary the previous two cases sit outside:
// a hunk whose last line is the range's first, and one whose first line is the
// range's last. `OldStart > end` is the ignore test, so a hunk starting AT end
// is an overlap — the boundary is closed on the range's side at both ends.
func TestEdgeMMMapRangeNonInsertionOverlappingEitherEndTouches(t *testing.T) {
	for _, c := range []struct {
		name string
		hunk Hunk
	}{
		{"last old line == range start", Hunk{OldStart: 8, OldCount: 3, NewStart: 8, NewCount: 1}},
		{"first old line == range end", Hunk{OldStart: 12, OldCount: 4, NewStart: 12, NewCount: 1}},
		{"the hunk covers the whole range", Hunk{OldStart: 1, OldCount: 40, NewStart: 1, NewCount: 2}},
	} {
		_, _, touched := MapRange([]Hunk{c.hunk}, 10, 12)
		if !touched {
			t.Errorf("%s: reported touched=false", c.name)
		}
	}
}

// TestEdgeMMMapRangeHunksBelowAreIgnored is the ordering promise stated as a
// negative: a hunk starting at end+1 changes nothing about the anchor, and no
// later hunk can either. Asserted with a hunk carrying a huge delta, so an
// implementation that accumulated it would be off by a thousand rather than by
// one.
func TestEdgeMMMapRangeHunksBelowAreIgnored(t *testing.T) {
	h := []Hunk{
		{OldStart: 13, OldCount: 1, NewStart: 13, NewCount: 1000},
		{OldStart: 900, OldCount: 500, NewStart: 1900, NewCount: 0},
	}
	s, e, touched := MapRange(h, 10, 12)
	if touched {
		t.Error("hunks entirely below the range reported touched")
	}
	if s != 10 || e != 12 {
		t.Errorf("MapRange = (%d, %d), want (10, 12) — nothing below the range may shift it", s, e)
	}
}

// TestEdgeMMMapRangeAccumulatesOnlyStrictlyEarlierHunks is the composite case:
// two hunks above (their deltas add), one intersecting (contributes NO shift
// even though its own delta is enormous), one below (ignored). The returned
// coordinates must be the accumulated shift of the first two alone — the
// contract's "valid even when touched" clause, which the verify pass relies on
// to hand the text ladder an expected POSITION to pick the nearest hit from.
func TestEdgeMMMapRangeAccumulatesOnlyStrictlyEarlierHunks(t *testing.T) {
	h := []Hunk{
		{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 0},    // -2, entirely above
		{OldStart: 5, OldCount: 0, NewStart: 4, NewCount: 4},    // +4 insertion above
		{OldStart: 11, OldCount: 1, NewStart: 14, NewCount: 60}, // intersects: no shift
		{OldStart: 30, OldCount: 1, NewStart: 90, NewCount: 9},  // below: ignored
	}
	s, e, touched := MapRange(h, 10, 12)
	if !touched {
		t.Fatal("an intersecting hunk reported touched=false")
	}
	if s != 12 || e != 14 {
		t.Errorf("MapRange = (%d, %d), want (12, 14) — +2 accumulated from the two earlier hunks only", s, e)
	}
}

// TestEdgeMMMapRangePureBlockMoveIsTouchedNotChanged is the verified pure-move
// fixture the components doc calls out. Git renders a moved block as a deletion
// where it was plus an insertion where it went, so stage (a) reports the anchor
// as TOUCHED even though every byte of the text still exists — which is why
// `touched` may never be an invalidation on its own and the text ladder has the
// last word. The assertion here is deliberately about the mapped coordinates as
// well: the intersecting deletion contributes no shift, so the range comes back
// unmoved and the ladder gets its original position as the expected one.
func TestEdgeMMMapRangePureBlockMoveIsTouchedNotChanged(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -10,3 +9,0 @@ func before() {",
		"-\tif err != nil {",
		"-\t\treturn err",
		"-\t}",
		"@@ -39,0 +37,3 @@ func after() {",
		"+\tif err != nil {",
		"+\t\treturn err",
		"+\t}",
	}, "\n")
	hunks := ParseHunks(diff)
	if len(hunks) != 2 {
		t.Fatalf("ParseHunks over the move diff = %#v, want 2 hunks", hunks)
	}
	s, e, touched := MapRange(hunks, 10, 12)
	if !touched {
		t.Error("the moved block's own deletion hunk did not report touched — stage (a) must hand this to the text ladder")
	}
	if s != 10 || e != 12 {
		t.Errorf("MapRange = (%d, %d), want (10, 12) — the intersecting hunk contributes no shift", s, e)
	}
}

// TestEdgeMMMapRangeIsPure re-runs one fixture and requires an identical answer
// with an untouched input slice. The pass parses one diff per file and reuses
// those hunks across every anchor in it, so a function that sorted or rewrote
// its input in place would corrupt every anchor after the first — a defect that
// only ever shows on the SECOND anchor.
func TestEdgeMMMapRangeIsPure(t *testing.T) {
	h := []Hunk{
		{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 0},
		{OldStart: 5, OldCount: 0, NewStart: 4, NewCount: 4},
	}
	before := append([]Hunk(nil), h...)
	s1, e1, t1 := MapRange(h, 10, 12)
	s2, e2, t2 := MapRange(h, 10, 12)
	if s1 != s2 || e1 != e2 || t1 != t2 {
		t.Errorf("MapRange is not repeatable: (%d,%d,%v) then (%d,%d,%v)", s1, e1, t1, s2, e2, t2)
	}
	if !reflect.DeepEqual(h, before) {
		t.Errorf("MapRange mutated its input: %#v, was %#v", h, before)
	}
}

// ---------------------------------------------------------------------------
// LocateText — the ladder's eyes
// ---------------------------------------------------------------------------

// TestEdgeMMLocateTextEmptyInputsAreNil covers both empty arms the contract
// names. An empty text would otherwise match at every byte offset in the file
// and hand the relocation logic every line at once — the anchor would survive
// forever, anywhere, which is worse than dying.
func TestEdgeMMLocateTextEmptyInputsAreNil(t *testing.T) {
	for _, c := range []struct{ blob, text string }{
		{"", ""},
		{"", "foo"},
		{"foo\nbar\n", ""},
	} {
		if got := LocateText(c.blob, c.text); got != nil {
			t.Errorf("LocateText(%q, %q) = %#v, want nil", c.blob, c.text, got)
		}
	}
}

// TestEdgeMMLocateTextNoHitIsNil is the ladder's death condition, and the ONLY
// signal in the whole pass that may invalidate an item — so it must be nil
// rather than a zero-valued line number, which would read as "found at line 0".
func TestEdgeMMLocateTextNoHitIsNil(t *testing.T) {
	if got := LocateText("alpha\nbeta\ngamma\n", "delta"); got != nil {
		t.Errorf("LocateText = %#v, want nil for text that is not in the blob", got)
	}
}

// TestEdgeMMLocateTextIsOneBasedAndCountsFromTheFirstByte pins the coordinate
// system against the two shapes that break naive line-splitting: a match that
// starts MID-line, and a match that SPANS lines. Both must report the line
// holding the match's first byte, which is what the contract says and what makes
// the number comparable with an anchor's stored start line.
func TestEdgeMMLocateTextIsOneBasedAndCountsFromTheFirstByte(t *testing.T) {
	blob := "package core\n\nfunc alpha() {\n\treturn\n}\n"
	for _, c := range []struct {
		text string
		want []int
	}{
		{"package core", []int{1}},
		{"func alpha() {", []int{3}},
		{"alpha", []int{3}},                      // starts mid-line
		{"func alpha() {\n\treturn\n", []int{3}}, // spans lines: the FIRST byte's line
		{"}\n", []int{5}},
	} {
		got := LocateText(blob, c.text)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("LocateText(blob, %q) = %#v, want %#v", c.text, got, c.want)
		}
	}
}

// TestEdgeMMLocateTextReportsEveryOccurrenceInOrder is the multi-hit case the
// relocation step calls ambiguous. Order matters as much as membership: the
// caller picks the hit NEAREST the expected position, and a set returned in
// arbitrary order makes "nearest" a coin flip between equally distant hits.
func TestEdgeMMLocateTextReportsEveryOccurrenceInOrder(t *testing.T) {
	blob := "one\nreturn err\ntwo\nthree\nreturn err\nfour\nreturn err\n"
	want := []int{2, 5, 7}
	if got := LocateText(blob, "return err"); !reflect.DeepEqual(got, want) {
		t.Errorf("LocateText = %#v, want %#v (ascending)", got, want)
	}
}

// TestEdgeMMLocateTextNormalizesCRLFOnBothSides is the contract's "both sides
// compared after \r\n→\n" and it has to hold in BOTH directions: a CRLF file on
// disk against LF anchor text, and CRLF anchor text (a Windows-clipboard
// selection) against an LF blob. Without it a line-ending change alone would
// kill every anchor in the file at once — which the circuit breaker would then
// mask as a repo-level problem.
func TestEdgeMMLocateTextNormalizesCRLFOnBothSides(t *testing.T) {
	for _, c := range []struct {
		name string
		blob string
		text string
		want []int
	}{
		{"CRLF blob, LF text", "alpha\r\nbeta\r\ngamma\r\n", "beta\ngamma", []int{2}},
		{"LF blob, CRLF text", "alpha\nbeta\ngamma\n", "beta\r\ngamma", []int{2}},
		{"CRLF both", "alpha\r\nbeta\r\n", "beta\r\n", []int{2}},
	} {
		if got := LocateText(c.blob, c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: LocateText = %#v, want %#v", c.name, got, c.want)
		}
	}
}

// TestEdgeMMLocateTextWholeBlobIsLineOne is the largest anchor there is: the
// mark covers the entire file. Line 1 rather than 0, and one hit rather than
// none.
func TestEdgeMMLocateTextWholeBlobIsLineOne(t *testing.T) {
	blob := "only\nthing\n"
	if got := LocateText(blob, blob); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("LocateText(blob, blob) = %#v, want [1]", got)
	}
}

// TestEdgeMMLocateTextIsCaseAndWhitespaceSensitive states the two things the
// exact rung is NOT. The whitespace-normalized comparison is a SEPARATE rung
// built on StripWS and it runs after this one; folding either in here would
// collapse the ladder's stages and make the pass' verdict depend on which rung
// happened to answer first.
func TestEdgeMMLocateTextIsCaseAndWhitespaceSensitive(t *testing.T) {
	blob := "\tif err != nil {\n"
	for _, text := range []string{
		"if err != NIL {",
		"if err  != nil {",
		"iferr != nil {",
	} {
		if got := LocateText(blob, text); got != nil {
			t.Errorf("LocateText(%q, %q) = %#v, want nil — the exact rung is byte-exact", blob, text, got)
		}
	}
}

// ---------------------------------------------------------------------------
// StripWS — the whitespace policy shared with --ignore-all-space
// ---------------------------------------------------------------------------

// TestEdgeMMStripWSRemovesEveryWhitespaceRune walks the whitespace runes a real
// source file carries beyond the plain space: tab, newline, carriage return,
// vertical tab, form feed, NBSP, NEL and the ideographic space. The contract
// says "every Unicode whitespace rune", so an implementation matching only ' ',
// '\t' and '\n' would leave an NBSP behind and report two whitespace-only
// variants of one line as different — an invalidation of a line nobody edited.
func TestEdgeMMStripWSRemovesEveryWhitespaceRune(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a b", "ab"},
		{"a\tb", "ab"},
		{"a\nb", "ab"},
		{"a\r\nb", "ab"},
		{"a\vb", "ab"},
		{"a\fb", "ab"},
		{"a\u00a0b", "ab"}, // NO-BREAK SPACE (U+00A0)
		{"a\u3000b", "ab"}, // IDEOGRAPHIC SPACE (U+3000)
		{"a\u0085b", "ab"}, // NEXT LINE (U+0085)
		{"  \t\n  ", ""},   // whitespace only
		{"", ""},
	} {
		if got := StripWS(c.in); got != c.want {
			t.Errorf("StripWS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEdgeMMStripWSKeepsEverythingElse is the other half of the same rule, and
// the reason it is a separate case: a "strip whitespace" helper that reached for
// a Unicode category one step wider would eat zero-width and combining
// characters too. U+200B ZERO WIDTH SPACE is NOT a whitespace rune (Go's
// unicode.IsSpace says so), so it stays — the contract's own definition, applied
// literally.
func TestEdgeMMStripWSKeepsEverythingElse(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"abc", "abc"},
		{"a\u200bb", "a\u200bb"}, // ZERO WIDTH SPACE (U+200B) is not whitespace
		{"café", "café"},
		{"→", "→"},
		{"a_b-c", "a_b-c"},
	} {
		if got := StripWS(c.in); got != c.want {
			t.Errorf("StripWS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEdgeMMStripWSMakesWhitespaceOnlyChangesEqual is the obligation stated as
// the pass uses it: the rung exists to agree with `--ignore-all-space`, so a
// reindent, a tabs-to-spaces conversion and a line-ending change must all
// compare EQUAL after stripping. If they ever disagree, one stage of the verify
// says "unchanged" and the other says "gone", and which one wins depends on
// whether git happened to emit a hunk.
func TestEdgeMMStripWSMakesWhitespaceOnlyChangesEqual(t *testing.T) {
	base := "if err != nil {\n\treturn err\n}"
	for _, variant := range []string{
		"if err != nil {\n    return err\n}",
		"if err!=nil{\nreturn err\n}",
		"if err != nil {\r\n\treturn err\r\n}",
		"  if   err  !=  nil  {  return   err  }  ",
	} {
		if StripWS(base) != StripWS(variant) {
			t.Errorf("StripWS(%q) != StripWS(%q) — a whitespace-only change must compare equal", base, variant)
		}
	}
	// and the converse: a real edit is still different
	if StripWS(base) == StripWS("if err == nil {\n\treturn err\n}") {
		t.Error("StripWS collapsed a real edit (!= became ==) into equality")
	}
}

// ---------------------------------------------------------------------------
// EndRowFromSelection — the Zed task's whole arithmetic
// ---------------------------------------------------------------------------

// TestEdgeMMEndRowFromSelectionTable is the end-row boundary table. The
// trailing-newline arm is the one the whole rule exists for: Zed's whole-line
// selection includes the final newline, so counting newlines alone would report
// an end row one BELOW the last selected line and anchor a line carl never
// looked at. The floor arm is what keeps a lone "\n" from mapping above the
// start.
func TestEdgeMMEndRowFromSelectionTable(t *testing.T) {
	const start = 42
	for _, c := range []struct {
		name string
		sel  string
		want int
	}{
		{"empty selection is the single line at start", "", start},
		{"single line, no newline", "\tif err != nil {", start},
		{"two lines, no trailing newline", "a\nb", start + 1},
		{"three lines, no trailing newline", "a\nb\nc", start + 2},
		{"one whole line (trailing newline)", "a\n", start},
		{"two whole lines (trailing newline)", "a\nb\n", start + 1},
		{"a lone newline is still one line", "\n", start},
		{"two blank whole lines", "\n\n", start + 1},
		{"CRLF two whole lines", "a\r\nb\r\n", start + 1},
		{"CRLF two lines, no trailing newline", "a\r\nb", start + 1},
		{"a lone CR is not a line break", "a\rb", start},
		{"a trailing CR without LF does not end a line", "a\nb\r", start + 1},
	} {
		if got := EndRowFromSelection(start, c.sel); got != c.want {
			t.Errorf("%s: EndRowFromSelection(%d, %q) = %d, want %d", c.name, start, c.sel, got, c.want)
		}
	}
}

// TestEdgeMMEndRowFromSelectionNeverGoesBelowStart states the floor as its own
// assertion over the shapes that would drive the subtraction negative. An end
// row below the start is not a range at all, and store.AddMMCodeAnchor refuses
// it — so a slip here surfaces as a failed mark rather than a wrong one, but it
// is still the CLI silently losing carl's keystroke.
func TestEdgeMMEndRowFromSelectionNeverGoesBelowStart(t *testing.T) {
	for _, sel := range []string{"", "\n", "\r\n", "x", "x\n", "\n\n\n", strings.Repeat("\n", 20)} {
		for _, start := range []int{1, 2, 1000} {
			if got := EndRowFromSelection(start, sel); got < start {
				t.Errorf("EndRowFromSelection(%d, %q) = %d, below the start row", start, sel, got)
			}
		}
	}
}

// TestEdgeMMEndRowFromSelectionCountsFromTheGivenStart pins that the function is
// relative, not absolute: the same selection at two different start rows must
// produce the same SPAN. A helper that anchored at 1 would work in every test
// whose fixture starts at 1 and be wrong for every real mark.
func TestEdgeMMEndRowFromSelectionCountsFromTheGivenStart(t *testing.T) {
	const sel = "a\nb\nc"
	for _, start := range []int{1, 7, 500} {
		if got, want := EndRowFromSelection(start, sel), start+2; got != want {
			t.Errorf("EndRowFromSelection(%d, %q) = %d, want %d", start, sel, got, want)
		}
	}
}
