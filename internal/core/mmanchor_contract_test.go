package core

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// mmanchor_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice S1's `internal/core/anchor.go` rows:
// `Hunk`, `ParseHunks`, `MapRange`, `LocateText`, `StripWS` and
// `EndRowFromSelection`.
//
// Written from the frozen contract table alone. internal/core/anchor.go was
// never read by any route — no Read, no grep, no git show, no build output
// quoting it. Every assertion below transcribes a clause of a contract ROW,
// and every symbol it names is one the contract freezes.
//
// FILE NAME: the contract's natural name for this file is
// `anchor_contract_test.go`, but internal/core already carries a file of that
// name (the knowledge graph's name-anchor leg, `core.AnchorNodes`), so this one
// is `mmanchor_contract_test.go` to avoid clobbering it. Disclosed in the round
// report.
//
// Dimension: LOGIC — the stated invariants and the happy paths. Hostile inputs,
// malformed headers, degenerate ranges and overflow are athena-edge's ground and
// are deliberately not duplicated here. Where a clause is stated as an ABSOLUTE
// ("never uses NewStart-OldStart", "never below startRow") it is pinned here,
// because the happy path's meaning depends on it.
//
// Every package-level identifier declared here is prefixed `contractMMAnchor`:
// the edge author shares this package's test namespace, and `contractAnchor…`
// is already taken by the pre-existing name-anchor file.

// ---------------------------------------------------------------------------
// core.ParseHunks
// ---------------------------------------------------------------------------

// contractMMAnchorHunkCase is one `git diff --unified=0` fixture and the hunk
// list the contract says falls out of it.
type contractMMAnchorHunkCase struct {
	name string
	diff string
	want []Hunk
}

// TestContractMMAnchorParseHunksReadsUnifiedZeroHeaders walks the whole stated
// obligation of ParseHunks in one table: `@@ -a[,b] +c[,d] @@` headers in ORDER,
// an omitted count meaning 1, non-header lines and the rename preamble ignored,
// and no headers at all coming back nil.
func TestContractMMAnchorParseHunksReadsUnifiedZeroHeaders(t *testing.T) {
	cases := []contractMMAnchorHunkCase{
		{
			name: "a full four-number header",
			diff: "@@ -10,3 +10,4 @@ func foo() {\n-old\n+new\n+extra\n",
			want: []Hunk{{OldStart: 10, OldCount: 3, NewStart: 10, NewCount: 4}},
		},
		{
			// "an omitted count means 1" — on BOTH sides at once.
			name: "both counts omitted",
			diff: "@@ -4 +6 @@\n-a\n+b\n",
			want: []Hunk{{OldStart: 4, OldCount: 1, NewStart: 6, NewCount: 1}},
		},
		{
			// ... and one side at a time, which is the shape git actually
			// emits for a single-line-to-many change.
			name: "old count omitted only",
			diff: "@@ -4 +6,3 @@\n",
			want: []Hunk{{OldStart: 4, OldCount: 1, NewStart: 6, NewCount: 3}},
		},
		{
			name: "new count omitted only",
			diff: "@@ -4,3 +6 @@\n",
			want: []Hunk{{OldStart: 4, OldCount: 3, NewStart: 6, NewCount: 1}},
		},
		{
			// A PURE INSERTION: --unified=0 spells it `-N,0`, and the zero is
			// what MapRange's insertion branch keys on, so it must survive the
			// parse as a real 0 rather than being defaulted to 1.
			name: "pure insertion keeps OldCount 0",
			diff: "@@ -4,0 +5,3 @@\n+one\n+two\n+three\n",
			want: []Hunk{{OldStart: 4, OldCount: 0, NewStart: 5, NewCount: 3}},
		},
		{
			// The mirror case: a pure DELETION spells `+M,0`.
			name: "pure deletion keeps NewCount 0",
			diff: "@@ -5,2 +5,0 @@\n-gone\n-also gone\n",
			want: []Hunk{{OldStart: 5, OldCount: 2, NewStart: 5, NewCount: 0}},
		},
		{
			// "in order" — the list is the file's order, not sorted, not
			// deduped, not reversed.
			name: "several headers, in order",
			diff: "@@ -2,1 +2,1 @@\n-a\n+b\n@@ -20,0 +21,2 @@\n+c\n+d\n@@ -40,5 +43,1 @@\n",
			want: []Hunk{
				{OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 1},
				{OldStart: 20, OldCount: 0, NewStart: 21, NewCount: 2},
				{OldStart: 40, OldCount: 5, NewStart: 43, NewCount: 1},
			},
		},
		{
			// "rename preamble ignored" — -M50% puts six lines of prose ahead
			// of the first header, and two of them start with `---`/`+++`,
			// which a naive line scanner mistakes for content.
			name: "rename preamble ignored",
			diff: strings.Join([]string{
				"diff --git a/old/path.go b/new/path.go",
				"similarity index 92%",
				"rename from old/path.go",
				"rename to new/path.go",
				"index 1234567..89abcde 100644",
				"--- a/old/path.go",
				"+++ b/new/path.go",
				"@@ -7,2 +7,3 @@",
				"-x",
				"+y",
				"+z",
				"",
			}, "\n"),
			want: []Hunk{{OldStart: 7, OldCount: 2, NewStart: 7, NewCount: 3}},
		},
		{
			// "non-header lines ... ignored": a diff body line may itself
			// begin with `@@` once it is prefixed by `+`/`-`/space.
			name: "body lines that look like headers are not headers",
			diff: "@@ -1,1 +1,1 @@\n-@@ -99,9 +99,9 @@\n+@@ -98,8 +98,8 @@\n",
			want: []Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}},
		},
		{
			name: "no headers is nil",
			diff: "diff --git a/x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n",
			want: nil,
		},
		{
			name: "empty input is nil",
			diff: "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseHunks(tc.diff)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("ParseHunks(%q) = %+v, want no hunks (nil)", tc.diff, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseHunks(%q) =\n  %+v\nwant\n  %+v", tc.diff, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// core.MapRange
// ---------------------------------------------------------------------------

// contractMMAnchorMapCase is one MapRange fixture: a hunk list, the anchor's
// 1-based inclusive range, and the frozen (newStart, newEnd, touched) triple.
type contractMMAnchorMapCase struct {
	name               string
	hunks              []Hunk
	start, end         int
	wantStart, wantEnd int
	wantTouched        bool
}

// contractMMAnchorIns builds a PURE INSERTION hunk: OldCount 0, inserting
// `count` lines AFTER old line `after`. NewStart is deliberately set to a value
// that is NOT `after+1` in several cases below — the contract forbids MapRange
// from deriving its shift from NewStart-OldStart, so the fixtures must be able
// to tell the two apart.
func contractMMAnchorIns(after, newStart, count int) Hunk {
	return Hunk{OldStart: after, OldCount: 0, NewStart: newStart, NewCount: count}
}

// TestContractMMAnchorMapRangeShiftsAndTouches is the whole MapRange row,
// clause by clause. Every case is named after the clause it transcribes.
func TestContractMMAnchorMapRangeShiftsAndTouches(t *testing.T) {
	cases := []contractMMAnchorMapCase{
		{
			// The degenerate identity: nothing changed, so the range is
			// itself and untouched. This is what a settled anchor sees on
			// every pass after the one that re-anchored it.
			name:  "no hunks leaves the range alone",
			hunks: nil, start: 10, end: 20, wantStart: 10, wantEnd: 20,
		},
		{
			// "a pure insertion inserts AFTER old line OldStart: strictly
			// above the range (OldStart < start) shifts by NewCount"
			name:  "insertion strictly above shifts by NewCount",
			hunks: []Hunk{contractMMAnchorIns(5, 6, 3)},
			start: 10, end: 20, wantStart: 13, wantEnd: 23,
		},
		{
			// The tightest above-case there is: an insertion after line
			// start-1 lands immediately before the block and still only
			// shifts it. The testing plan names this one explicitly
			// ("insertion directly above the range (shifts, not touched)").
			name:  "insertion directly above the first line shifts, never touches",
			hunks: []Hunk{contractMMAnchorIns(9, 10, 4)},
			start: 10, end: 20, wantStart: 14, wantEnd: 24,
		},
		{
			// "at or below the last line (OldStart >= end) has no effect"
			name:  "insertion at the range's last line has no effect",
			hunks: []Hunk{contractMMAnchorIns(20, 21, 6)},
			start: 10, end: 20, wantStart: 10, wantEnd: 20,
		},
		{
			name:  "insertion below the range has no effect",
			hunks: []Hunk{contractMMAnchorIns(40, 41, 6)},
			start: 10, end: 20, wantStart: 10, wantEnd: 20,
		},
		{
			// "strictly inside splits the block -> touched". OldStart == start
			// is INSIDE: the insertion lands after the block's first line.
			name:  "insertion after the range's first line splits it",
			hunks: []Hunk{contractMMAnchorIns(10, 11, 1)},
			start: 10, end: 20, wantStart: 10, wantEnd: 20, wantTouched: true,
		},
		{
			name:  "insertion in the middle splits it",
			hunks: []Hunk{contractMMAnchorIns(15, 16, 2)},
			start: 10, end: 20, wantStart: 10, wantEnd: 20, wantTouched: true,
		},
		{
			// "A non-insertion hunk entirely above (OldStart+OldCount-1 <
			// start) shifts by NewCount-OldCount". A pure deletion of 3 lines
			// at old 2..4 pulls the block up by 3.
			name:  "pure deletion above shifts by NewCount-OldCount",
			hunks: []Hunk{{OldStart: 2, OldCount: 3, NewStart: 2, NewCount: 0}},
			start: 10, end: 20, wantStart: 7, wantEnd: 17,
		},
		{
			// The same clause at its boundary: the hunk's LAST old line is
			// start-1, which is still "entirely above".
			name:  "a replacement ending exactly one line above shifts",
			hunks: []Hunk{{OldStart: 8, OldCount: 2, NewStart: 8, NewCount: 5}},
			start: 10, end: 20, wantStart: 13, wantEnd: 23,
		},
		{
			// "Never uses NewStart-OldStart of an intersecting hunk as the
			// shift." Stated for the intersecting case, but the fixture that
			// can actually SEE the difference is an above-hunk whose NewStart
			// has been dragged far away by earlier edits to other files in the
			// same diff: NewStart-OldStart is +48 here, NewCount-OldCount is
			// -2, and only the second is right.
			name:  "the shift is NewCount-OldCount, never NewStart-OldStart",
			hunks: []Hunk{{OldStart: 2, OldCount: 3, NewStart: 50, NewCount: 1}},
			start: 10, end: 20, wantStart: 8, wantEnd: 18,
		},
		{
			// "entirely below (OldStart > end) is ignored (hunks are ordered —
			// later ones cannot matter)"
			name:  "a non-insertion hunk entirely below is ignored",
			hunks: []Hunk{{OldStart: 25, OldCount: 2, NewStart: 25, NewCount: 9}},
			start: 10, end: 20, wantStart: 10, wantEnd: 20,
		},
		{
			// "otherwise touched" — a replacement that overlaps the block.
			name:  "a replacement inside the range touches it",
			hunks: []Hunk{{OldStart: 12, OldCount: 2, NewStart: 12, NewCount: 5}},
			start: 10, end: 20, wantStart: 10, wantEnd: 20, wantTouched: true,
		},
		{
			name:  "a replacement straddling the first line touches it",
			hunks: []Hunk{{OldStart: 9, OldCount: 3, NewStart: 9, NewCount: 1}},
			start: 10, end: 20, wantStart: 10, wantEnd: 20, wantTouched: true,
		},
		{
			name:  "a replacement straddling the last line touches it",
			hunks: []Hunk{{OldStart: 20, OldCount: 4, NewStart: 20, NewCount: 4}},
			start: 10, end: 20, wantStart: 10, wantEnd: 20, wantTouched: true,
		},
		{
			// "newStart/newEnd = accumulated shift from strictly-earlier hunks
			// applied to start/end" — two above-hunks compose, one negative and
			// one positive.
			name: "shifts from several earlier hunks accumulate",
			hunks: []Hunk{
				{OldStart: 2, OldCount: 3, NewStart: 2, NewCount: 0}, // -3
				contractMMAnchorIns(5, 3, 2),                         // +2
			},
			start: 10, end: 20, wantStart: 9, wantEnd: 19,
		},
		{
			// "... valid even when touched": the coordinates a touched anchor
			// comes back with are still the accumulated earlier shift, because
			// the text ladder searches AROUND them for the nearest occurrence.
			name: "a touched range still reports the earlier hunks' shift",
			hunks: []Hunk{
				{OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 3},   // +2, above
				{OldStart: 12, OldCount: 2, NewStart: 14, NewCount: 2}, // touches
			},
			start: 10, end: 20, wantStart: 12, wantEnd: 22, wantTouched: true,
		},
		{
			// A touched anchor stays touched even though the hunks after it are
			// all ignorable: `touched` is a latch, not the last hunk's verdict.
			name: "touched survives later below-range hunks",
			hunks: []Hunk{
				{OldStart: 15, OldCount: 1, NewStart: 15, NewCount: 1}, // touches
				{OldStart: 90, OldCount: 4, NewStart: 90, NewCount: 0}, // below
			},
			start: 10, end: 20, wantStart: 10, wantEnd: 20, wantTouched: true,
		},
		{
			// A single-line anchor is the common case for `k mm mark` with no
			// selection, and it is where the two insertion boundaries collapse
			// onto one line: OldStart == start == end is ">= end", so it has NO
			// effect rather than touching.
			name:  "single-line range: insertion at that line has no effect",
			hunks: []Hunk{contractMMAnchorIns(10, 11, 3)},
			start: 10, end: 10, wantStart: 10, wantEnd: 10,
		},
		{
			name:  "single-line range: insertion above it shifts",
			hunks: []Hunk{contractMMAnchorIns(9, 10, 3)},
			start: 10, end: 10, wantStart: 13, wantEnd: 13,
		},
		{
			name:  "single-line range: a replacement on that line touches",
			hunks: []Hunk{{OldStart: 10, OldCount: 1, NewStart: 10, NewCount: 1}},
			start: 10, end: 10, wantStart: 10, wantEnd: 10, wantTouched: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd, gotTouched := MapRange(tc.hunks, tc.start, tc.end)
			if gotStart != tc.wantStart || gotEnd != tc.wantEnd || gotTouched != tc.wantTouched {
				t.Fatalf("MapRange(%+v, %d, %d) = (%d, %d, %t), want (%d, %d, %t)",
					tc.hunks, tc.start, tc.end,
					gotStart, gotEnd, gotTouched,
					tc.wantStart, tc.wantEnd, tc.wantTouched)
			}
		})
	}
}

// TestContractMMAnchorMapRangeOverlapAloneNeverDecidesChanged is the
// cross-slice invariant restated at the only level that can hold it: MapRange's
// third return is `touched`, a request for the text ladder — never a verdict.
// The pure-block-move fixture the testing plan names produces hunks that may or
// may not intersect the moved range depending on how git chose to describe the
// move; the contract's promise is that BOTH readings are survivable, because
// the shifted coordinates come back either way and only text absence may say
// "changed".
func TestContractMMAnchorMapRangeOverlapAloneNeverDecidesChanged(t *testing.T) {
	// A block move: lines 30-34 deleted from their old home, re-inserted after
	// line 8. git --unified=0 describes it as two hunks.
	moved := []Hunk{
		contractMMAnchorIns(8, 9, 5),                           // the arrival
		{OldStart: 30, OldCount: 5, NewStart: 35, NewCount: 0}, // the departure
	}
	// An anchor on an untouched block BETWEEN the two: the arrival is above it
	// (+5), the departure is below it (ignored). Nothing touched, and the
	// coordinates move by exactly the arrival's line count.
	start, end, touched := MapRange(moved, 12, 14)
	if touched {
		t.Fatalf("MapRange(block move, 12, 14) reported touched — neither hunk intersects [12,14]")
	}
	if start != 17 || end != 19 {
		t.Fatalf("MapRange(block move, 12, 14) = (%d, %d), want (17, 19) — the arrival's +5 alone", start, end)
	}
	// And the moved block itself: the departure hunk covers it, so it comes
	// back touched with the arrival's shift already applied. `touched` here is
	// the hand-off to the text ladder, which is what finds the block at its new
	// home — MapRange itself must not have declared anything about the content.
	start, end, touched = MapRange(moved, 30, 34)
	if !touched {
		t.Fatal("MapRange(block move, 30, 34) must report touched — the departure hunk covers it")
	}
	if start != 35 || end != 39 {
		t.Fatalf("MapRange(block move, 30, 34) = (%d, %d), want (35, 39) — the strictly-earlier arrival's +5", start, end)
	}
}

// ---------------------------------------------------------------------------
// core.LocateText
// ---------------------------------------------------------------------------

// TestContractMMAnchorLocateTextFindsEveryOccurrence walks the row: a 1-based
// line number per occurrence, `\r\n` normalized on BOTH sides, the line number
// being the one containing the match's FIRST byte, and the two empty inputs
// coming back nil.
func TestContractMMAnchorLocateTextFindsEveryOccurrence(t *testing.T) {
	cases := []struct {
		name string
		blob string
		text string
		want []int
	}{
		{
			name: "a single line, at the top of the file",
			blob: "package main\n\nfunc main() {}\n",
			text: "package main",
			want: []int{1},
		},
		{
			name: "a single line further down",
			blob: "a\nb\nc\n",
			text: "b",
			want: []int{2},
		},
		{
			// The anchor's own shape: a contiguous multi-line block. The
			// returned number is the block's FIRST line.
			name: "a contiguous multi-line block",
			blob: "a\nfunc x() {\n\treturn 1\n}\nz\n",
			text: "func x() {\n\treturn 1\n}",
			want: []int{2},
		},
		{
			// "each occurrence" — the ambiguity the verify pass resolves by
			// nearest-to-expected-position, which it can only do if LocateText
			// hands it all of them.
			name: "every occurrence, in file order",
			blob: "x\nfoo\ny\nfoo\nz\nfoo\n",
			text: "foo",
			want: []int{2, 4, 6},
		},
		{
			// "both sides compared after \r\n -> \n": a CRLF BLOB against an LF
			// needle.
			name: "a CRLF blob is normalized before the search",
			blob: "a\r\nfoo\r\nb\r\n",
			text: "foo",
			want: []int{2},
		},
		{
			// ... and the mirror: a CRLF NEEDLE against an LF blob. A stored
			// anchor text captured from a Windows-line-ended buffer must still
			// find its block.
			name: "a CRLF needle is normalized before the search",
			blob: "a\nfoo\nbar\nb\n",
			text: "foo\r\nbar",
			want: []int{2},
		},
		{
			// "a returned line number is the line containing the match's first
			// byte" — a mid-line match belongs to its own line, not the next.
			name: "a mid-line match reports the line it starts on",
			blob: "aaa\nbbXbb\nccc\n",
			text: "X",
			want: []int{2},
		},
		{
			// The same rule where it bites: a needle that STARTS mid-line and
			// runs into the next one is reported on the line it starts on.
			name: "a match spanning a newline reports its starting line",
			blob: "aaa\nbb tail\nhead cc\n",
			text: "tail\nhead",
			want: []int{2},
		},
		{name: "no occurrence is nil", blob: "a\nb\nc\n", text: "zzz", want: nil},
		{name: "empty text is nil", blob: "a\nb\n", text: "", want: nil},
		{name: "empty blob is nil", blob: "", text: "a", want: nil},
		{name: "both empty is nil", blob: "", text: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LocateText(tc.blob, tc.text)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("LocateText(%q, %q) = %v, want nil", tc.blob, tc.text, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("LocateText(%q, %q) = %v, want %v", tc.blob, tc.text, got, tc.want)
			}
		})
	}
}

// TestContractMMAnchorLocateTextIsWhatMakesRelocationPossible states the
// consumer's reading of the row in one place: the ladder's "exact text found in
// `git show head:<path>`" rung is LocateText over the head blob, and the number
// it returns is what `ReanchorMMCodeAnchor` stores as the new StartLine. A blob
// whose only change is ABOVE the anchor therefore reports the anchor's new
// first line directly.
func TestContractMMAnchorLocateTextIsWhatMakesRelocationPossible(t *testing.T) {
	const anchored = "\tif err != nil {\n\t\treturn err\n\t}"
	head := "package p\n\n// two\n// new\n// comment\n// lines\nfunc f() error {\n" + anchored + "\n\treturn nil\n}\n"
	got := LocateText(head, anchored)
	if len(got) != 1 {
		t.Fatalf("LocateText found %v, want exactly one occurrence", got)
	}
	if got[0] != 8 {
		t.Fatalf("LocateText reported line %d, want 8 (the block's first line, 1-based)", got[0])
	}
}

// ---------------------------------------------------------------------------
// core.StripWS
// ---------------------------------------------------------------------------

// TestContractMMAnchorStripWSRemovesEveryWhitespaceRune pins the row: EVERY
// Unicode whitespace rune goes, not just ASCII space and tab.
func TestContractMMAnchorStripWSRemovesEveryWhitespaceRune(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"spaces and tabs", "a b\tc", "abc"},
		{"newlines", "a\nb\r\nc", "abc"},
		{"leading and trailing", "   padded   ", "padded"},
		{"whitespace only", " \t\n\r ", ""},
		{"empty", "", ""},
		{"nothing to strip", "abc", "abc"},
		{"a form feed and a vertical tab", "a\fb\vc", "abc"},
		// Not ASCII: NBSP, an en quad, and an ideographic space are all
		// unicode.IsSpace, and all three turn up in prose pasted into a
		// source comment.
		{"non-ASCII whitespace", "a b c　d", "abcd"},
		// Non-whitespace runes survive untouched, including multi-byte ones —
		// the rung compares CONTENT, and mangling it would make two different
		// blocks compare equal.
		{"multi-byte content survives", " é → ü ", "é→ü"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripWS(tc.in); got != tc.want {
				t.Fatalf("StripWS(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// The absolute, stated as a property rather than as a table row: no rune of
	// the result may be whitespace, for any input.
	for _, in := range []string{"a b\tc\n", "   x  ", "no-ws"} {
		for _, r := range StripWS(in) {
			if unicode.IsSpace(r) {
				t.Fatalf("StripWS(%q) left the whitespace rune %q in its result", in, r)
			}
		}
	}
}

// TestContractMMAnchorStripWSAgreesWithIgnoreAllSpace is the invariant the row
// exists for: the whitespace-normalized rung must agree with stage (a)'s
// `--ignore-all-space`, or one stage says "unchanged" while the other says
// "gone" and invalidation goes non-deterministic. Concretely: two blocks that
// differ ONLY in whitespace must be StripWS-equal.
func TestContractMMAnchorStripWSAgreesWithIgnoreAllSpace(t *testing.T) {
	pairs := [][2]string{
		{"if (x) {", "if(x){"},
		{"\tfoo(a, b)", "        foo( a,b )"},
		{"a\nb\nc", "a b c"},
		{"return\tnil", "return nil"},
		{"func f() {\n\treturn 1\n}", "func f(){return 1}"},
	}
	for _, p := range pairs {
		if StripWS(p[0]) != StripWS(p[1]) {
			t.Errorf("StripWS(%q) = %q and StripWS(%q) = %q — a whitespace-only difference must compare EQUAL, "+
				"or the ladder disagrees with --ignore-all-space", p[0], StripWS(p[0]), p[1], StripWS(p[1]))
		}
	}
	// The other direction, so the rung is not simply "everything is equal": a
	// difference in NON-whitespace content must still compare unequal.
	if StripWS("if (x) {") == StripWS("if (y) {") {
		t.Error("StripWS collapsed a real content difference — the rung would survive any edit at all")
	}
}

// ---------------------------------------------------------------------------
// core.EndRowFromSelection
// ---------------------------------------------------------------------------

// TestContractMMAnchorEndRowFromSelection walks the row's arithmetic:
// end = startRow + newlines(selection after \r\n -> \n), minus 1 when the
// selection ends in \n, floored at startRow, and an empty selection meaning the
// single line at startRow.
func TestContractMMAnchorEndRowFromSelection(t *testing.T) {
	cases := []struct {
		name      string
		startRow  int
		selection string
		want      int
	}{
		// "empty selection -> startRow": the keystroke with no selection means
		// "the single line the cursor is on".
		{"empty selection is one line", 10, "", 10},
		// One line with no trailing newline: zero newlines, no decrement.
		{"a single line without a newline", 10, "one line", 10},
		// Two lines, no trailing newline: one newline.
		{"two lines without a trailing newline", 10, "a\nb", 11},
		{"three lines without a trailing newline", 5, "a\nb\nc", 7},
		// "minus 1 when the selection ends in \n": Zed's whole-line selection
		// carries the trailing newline, and counting it would overshoot by one
		// line every single time.
		{"a whole-line selection does not overshoot", 10, "a\nb\n", 11},
		{"three whole lines", 5, "a\nb\nc\n", 7},
		{"one whole line", 10, "a\n", 10},
		// CRLF is normalized first, so a \r\n pair counts as ONE newline and
		// still triggers the trailing-newline decrement.
		{"CRLF two whole lines", 10, "a\r\nb\r\n", 11},
		{"CRLF without a trailing newline", 10, "a\r\nb", 11},
		{"CRLF three whole lines", 1, "x\r\ny\r\nz\r\n", 3},
		// "never below startRow": a selection that is nothing but a newline is
		// still one line.
		{"a bare newline is one line", 10, "\n", 10},
		{"a bare newline at row 1", 1, "\n", 1},
		{"whitespace-only selection", 7, "   ", 7},
		// A blank line in the middle counts like any other.
		{"a blank line inside the block counts", 20, "a\n\nb\n", 22},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EndRowFromSelection(tc.startRow, tc.selection); got != tc.want {
				t.Fatalf("EndRowFromSelection(%d, %q) = %d, want %d", tc.startRow, tc.selection, got, tc.want)
			}
		})
	}
	// The absolute, as a property: the end row is never below the start row,
	// whatever the selection.
	for _, sel := range []string{"", "\n", "\r\n", "   ", "a", "a\n", "a\nb\n"} {
		if got := EndRowFromSelection(42, sel); got < 42 {
			t.Errorf("EndRowFromSelection(42, %q) = %d — never below startRow", sel, got)
		}
	}
}
