package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clausecut_contract_test.go — the LOGIC pins for slice S1a of
// artifacts/50-contract.md: core.ClauseCut, the deterministic summary cutter
// that sits beside TruncateOneLine.
//
// Every case below is derived from the contract's six numbered obligations,
// and every expected string is HAND-COMPUTED from them rather than read back
// out of the implementation: the cut index, the trim, and the ellipsis are the
// whole specification, so a fixture whose answer was obtained by running the
// function would pin nothing at all.
//
// The arithmetic is stated once here so each fixture can be checked by eye.
// For a limit L over the one-lined rune slice r:
//
//	budget = L - 1   (one rune reserved for the "…")
//	floor  = L / 2   (integer division; governs CLAUSE cuts only)
//
// Every fixture in this file uses L = 40, so budget = 39 and floor = 20.
//
// Refusals, degenerate limits, and the all-punctuation fallback of obligation
// 5 belong to the sibling edge-case suite and are deliberately absent here.

// fxcCutLimit is the one limit every hand-computed fixture below is indexed
// against. Changing it invalidates every expected string in this file.
const fxcCutLimit = 40

// fxcOneLine reproduces the WHITESPACE half of obligation 1 (strings.Fields /
// Join semantics) for inputs that carry no control characters, which is what
// the ellipsis-iff-dropped property needs in order to know the post-sanitize
// length its prediction depends on.
func fxcOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestFxcClauseCutPassesUnderLimitTextThroughUnchanged pins obligation 2: "if
// the one-lined string is ≤ limit runes, return it unchanged — no ellipsis,
// byte-identical to the one-lined input."
//
// The at-exactly-limit row is the one that makes "≤" rather than "<" load
// bearing: a fact of exactly limit runes is complete, and ellipsizing it would
// drop a rune to announce a truncation that never happened.
func TestFxcClauseCutPassesUnderLimitTextThroughUnchanged(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"well under", "hello world", "hello world"},
		{"one under the limit", strings.Repeat("a", fxcCutLimit-1), strings.Repeat("a", fxcCutLimit-1)},
		{"exactly at the limit", strings.Repeat("a", fxcCutLimit), strings.Repeat("a", fxcCutLimit)},
		// Sanitizing happens BEFORE the length comparison, so a string whose
		// raw form is over the limit but whose one-lined form is under it is a
		// passthrough, not a cut.
		{"over raw, under once one-lined", "hello" + strings.Repeat(" ", 30) + "world", "hello world"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ClauseCut(c.in, fxcCutLimit)
			if got != c.want {
				t.Fatalf("ClauseCut(%q, %d) = %q, want %q", c.in, fxcCutLimit, got, c.want)
			}
			if strings.Contains(got, "…") {
				t.Errorf("nothing was dropped but the result carries an ellipsis: %q", got)
			}
		})
	}
}

// TestFxcClauseCutOneLinesBeforeMeasuring pins obligation 1: every rune < 0x20
// or == 0x7f becomes a space, whitespace runs collapse to single spaces, and
// an empty or all-whitespace input returns "".
//
// The block this cutter feeds is a bullet list read by eye, so a newline
// surviving inside one line breaks the format its reader is parsing.
func TestFxcClauseCutOneLinesBeforeMeasuring(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"newline", "a fact\nsplit over lines", "a fact split over lines"},
		{"whitespace run", "a  fact   with\t\tgaps", "a fact with gaps"},
		{"leading and trailing whitespace", "  \t a fact \n ", "a fact"},
		{"carriage return and vertical tab", "a\rfact\vhere", "a fact here"},
		// 0x01 is < 0x20 and 0x7f is the DEL the contract names explicitly;
		// both become spaces, which then collapse with their neighbours.
		{"control characters", "a\x01b\x7fc", "a b c"},
		{"control run collapses", "a\x01\x02\x03b", "a b"},
		{"empty", "", ""},
		{"all whitespace", "   \n\t  ", ""},
		{"all control", "\x01\x02\x7f", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ClauseCut(c.in, fxcCutLimit); got != c.want {
				t.Fatalf("ClauseCut(%q, %d) = %q, want %q", c.in, fxcCutLimit, got, c.want)
			}
		})
	}
}

// TestFxcClauseCutTakesTheLastQualifyingClauseMarker pins the selection rule
// of obligation 4: "For each occurrence of a marker beginning at rune index i
// with floor ≤ i ≤ budget, the candidate keep is r[:i]. Take the occurrence
// with the LARGEST i" — and the marker itself is dropped, since the keep ends
// at i.
//
// The fixture carries three ", " markers, at rune indices 9, 20 and 31. With
// floor 20 and budget 39, the first is below the floor and the other two
// qualify; 31 is the larger, so the keep is r[:31] and the result is that plus
// the ellipsis — 32 runes.
func TestFxcClauseCutTakesTheLastQualifyingClauseMarker(t *testing.T) {
	// index: aaaa=0-3 ' '=4 bbbb=5-8 ','=9 ' '=10 cccc=11-14 ' '=15
	//        dddd=16-19 ','=20 ' '=21 eeee=22-25 ' '=26 ffff=27-30 ','=31 …
	const in = "aaaa bbbb, cccc dddd, eeee ffff, gggg hhhh iiii jjjj"
	const want = "aaaa bbbb, cccc dddd, eeee ffff…"

	got := ClauseCut(in, fxcCutLimit)
	if got != want {
		t.Fatalf("ClauseCut(%q, %d) = %q, want %q (last qualifying marker is the one at rune 31)", in, fxcCutLimit, got, want)
	}
	// The marker is DROPPED, not kept: the keep is r[:i], so the result must
	// not end with the comma that ended the clause.
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), ",") {
		t.Errorf("the clause marker survived the cut: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 32 {
		t.Errorf("result is %d runes, want 32", n)
	}
}

// TestFxcClauseCutCutsAtEachOfTheFourMarkers pins the marker SET of obligation
// 4: exactly ", ", " — ", "; " and ": ", with the em-dash marker carrying its
// surrounding spaces as part of the marker.
//
// Every row places its marker at rune index 24 — inside [floor 20, budget 39]
// — behind an identical 24-rune prefix, so all four must produce the same
// keep. The tail is 24 runes of marker-free text, which puts every input well
// over the limit and leaves a word-boundary cut available at a LARGER index
// than the clause cut: a clause marker in range wins anyway, and that
// precedence is what this case exists to pin.
func TestFxcClauseCutCutsAtEachOfTheFourMarkers(t *testing.T) {
	const prefix = "aaaa bbbb cccc dddd eeee" // 24 runes
	const tail = "ffff gggg hhhh iiii jjjj"   // 24 runes, no markers
	const want = "aaaa bbbb cccc dddd eeee…"

	for _, marker := range []string{", ", " — ", "; ", ": "} {
		t.Run("marker "+marker, func(t *testing.T) {
			in := prefix + marker + tail
			if got := ClauseCut(in, fxcCutLimit); got != want {
				t.Fatalf("ClauseCut(%q, %d) = %q, want %q", in, fxcCutLimit, got, want)
			}
		})
	}
}

// TestFxcClauseCutFallsBackToAWordBoundary pins the second bullet of
// obligation 4: with no qualifying clause marker, the keep is r[:i] for the
// largest i ≤ budget where r[i] is a space.
//
// The fixture's spaces sit at 4, 9, 14, 19, 24, 29, 34, 39, 44; the largest
// within budget 39 is 39 itself, so the keep is 39 runes and the result is
// exactly the limit — which also pins obligation 3's "result INCLUDING the …
// is ≤ limit runes" at its tightest point.
func TestFxcClauseCutFallsBackToAWordBoundary(t *testing.T) {
	const in = "aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj"
	const want = "aaaa bbbb cccc dddd eeee ffff gggg hhhh…"

	got := ClauseCut(in, fxcCutLimit)
	if got != want {
		t.Fatalf("ClauseCut(%q, %d) = %q, want %q", in, fxcCutLimit, got, want)
	}
	if n := utf8.RuneCountInString(got); n != fxcCutLimit {
		t.Errorf("result is %d runes, want exactly the limit %d", n, fxcCutLimit)
	}
	// "the result never ends mid-word" — the rune before the ellipsis is the
	// last rune of a whole token.
	if !strings.HasSuffix(got, "hhhh…") {
		t.Errorf("word cut landed mid-token: %q", got)
	}
}

// TestFxcClauseCutFloorGovernsOnlyClauseCuts pins the sentence obligation 4
// spends its last line on: "The floor does NOT govern word cuts — only clause
// cuts."
//
// Two halves, one property. A marker BELOW the floor does not qualify, so the
// cut falls through to the word rule even though a comma was available; and a
// word boundary below the floor is taken regardless, even when it leaves a
// two-rune keep, because a short honest summary beats a mid-word one.
func TestFxcClauseCutFloorGovernsOnlyClauseCuts(t *testing.T) {
	t.Run("marker below the floor does not qualify", func(t *testing.T) {
		// The only ", " begins at rune 4, under floor 20. Spaces sit at 5, 10,
		// 15, 20, 25, 30, 35, 40, 45; the largest within budget 39 is 35.
		const in = "aaaa, bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj"
		const want = "aaaa, bbbb cccc dddd eeee ffff gggg…"
		if got := ClauseCut(in, fxcCutLimit); got != want {
			t.Fatalf("ClauseCut(%q, %d) = %q, want %q", in, fxcCutLimit, got, want)
		}
	})

	t.Run("word boundary below the floor is still taken", func(t *testing.T) {
		// One space, at rune 2, far below floor 20 — and a space DOES exist
		// inside r[:budget+1], so this is a word cut rather than the hard cut.
		in := "ab " + strings.Repeat("z", 60)
		const want = "ab…"
		if got := ClauseCut(in, fxcCutLimit); got != want {
			t.Fatalf("ClauseCut(%q, %d) = %q, want %q — the floor must not push a word cut into the middle of a token", in, fxcCutLimit, got, want)
		}
	})
}

// TestFxcClauseCutTrimsTrailingPunctuationBeforeTheEllipsis pins obligation 5:
// trailing runes in the set ' ' ',' ';' ':' '.' '—' come off the keep before
// the "…" is appended.
//
// The fixture's chosen word boundary is the space at rune 39, whose keep
// r[:39] ends with the '.' at rune 38. Untrimmed the line would read
// "…gggggggg.…", which is two terminators for one sentence.
func TestFxcClauseCutTrimsTrailingPunctuationBeforeTheEllipsis(t *testing.T) {
	// index: … ffff=25-28 ' '=29 gggggggg=30-37 '.'=38 ' '=39 iiii=40-43 …
	const in = "aaaa bbbb cccc dddd eeee ffff gggggggg. iiii jjjj"
	const want = "aaaa bbbb cccc dddd eeee ffff gggggggg…"

	got := ClauseCut(in, fxcCutLimit)
	if got != want {
		t.Fatalf("ClauseCut(%q, %d) = %q, want %q", in, fxcCutLimit, got, want)
	}
	for _, bad := range []string{".…", ",…", ";…", ":…", " …", "—…"} {
		if strings.HasSuffix(got, bad) {
			t.Errorf("result ends %q — obligation 5 trims that rune before appending the ellipsis: %q", bad, got)
		}
	}
}

// TestFxcClauseCutCountsRunesNotBytes pins the unit the limit is measured in,
// through the hard-cut branch of obligation 4 — "no space anywhere in
// r[:budget+1] … keep r[:budget]", the one sanctioned mid-word cut.
//
// A single unbroken CJK token is the case where a byte-measured limit and a
// rune-measured one differ by 3x, so it is the fixture that can tell them
// apart: 39 kept runes plus the ellipsis is 40 runes and 120 bytes.
func TestFxcClauseCutCountsRunesNotBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"ascii token", strings.Repeat("z", 60), strings.Repeat("z", fxcCutLimit-1) + "…"},
		{"cjk token", strings.Repeat("知", 50), strings.Repeat("知", fxcCutLimit-1) + "…"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ClauseCut(c.in, fxcCutLimit)
			if got != c.want {
				t.Fatalf("ClauseCut(%d-rune unbroken token, %d) = %q, want %q",
					utf8.RuneCountInString(c.in), fxcCutLimit, got, c.want)
			}
			if n := utf8.RuneCountInString(got); n != fxcCutLimit {
				t.Errorf("result is %d runes, want exactly %d", n, fxcCutLimit)
			}
		})
	}
}

// fxcCutProbes is the corpus the two whole-function properties below run over:
// a spread of shapes across both sides of the limit, none of which contains an
// ellipsis of its own.
var fxcCutProbes = []string{
	"",
	"   ",
	"short",
	strings.Repeat("a", fxcCutLimit),
	"aaaa bbbb, cccc dddd, eeee ffff, gggg hhhh iiii jjjj",
	"aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
	"aaaa bbbb cccc dddd eeee — ffff gggg hhhh iiii jjjj",
	"aaaa: bbbb cccc dddd eeee; ffff gggg hhhh iiii jjjj",
	"ab " + strings.Repeat("z", 60),
	strings.Repeat("z", 60),
	strings.Repeat("知", 50),
	"a fact\nsplit over\tlines with plenty of words to spare beyond the limit",
}

// TestFxcClauseCutEllipsisAppearsIffTextWasDropped pins obligation 3 as a
// property over the whole corpus: the "…" is present exactly when the one-lined
// input exceeded the limit, it is a SINGLE rune at the very end, and the whole
// result — ellipsis included — fits within the limit.
//
// Restricted to inputs that carry no "…" of their own, since a passthrough of
// text that already ended in one is a shape the contract does not resolve (see
// the report's silences).
func TestFxcClauseCutEllipsisAppearsIffTextWasDropped(t *testing.T) {
	for _, in := range fxcCutProbes {
		oneLined := fxcOneLine(in)
		dropped := utf8.RuneCountInString(oneLined) > fxcCutLimit

		got := ClauseCut(in, fxcCutLimit)

		if has := strings.Contains(got, "…"); has != dropped {
			t.Errorf("ClauseCut(%q, %d) = %q: ellipsis present = %v, want %v (one-lined input is %d runes)",
				in, fxcCutLimit, got, has, dropped, utf8.RuneCountInString(oneLined))
			continue
		}
		if n := utf8.RuneCountInString(got); n > fxcCutLimit {
			t.Errorf("ClauseCut(%q, %d) = %q is %d runes, over the limit", in, fxcCutLimit, got, n)
		}
		if !dropped {
			continue
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("ClauseCut(%q, %d) = %q dropped text but does not END with the ellipsis", in, fxcCutLimit, got)
		}
		if n := strings.Count(got, "…"); n != 1 {
			t.Errorf("ClauseCut(%q, %d) = %q carries %d ellipses, want exactly 1", in, fxcCutLimit, got, n)
		}
	}
}

// TestFxcClauseCutIsDeterministic pins obligation 6. The block this feeds is a
// system prompt: a summary that varied between two spawns of the same job
// would make the brief unreproducible by construction, and the contract's word
// for the function is "pure".
func TestFxcClauseCutIsDeterministic(t *testing.T) {
	for _, in := range fxcCutProbes {
		first := ClauseCut(in, fxcCutLimit)
		for i := 0; i < 5; i++ {
			if got := ClauseCut(in, fxcCutLimit); got != first {
				t.Fatalf("ClauseCut(%q, %d) run %d = %q, first run = %q", in, fxcCutLimit, i, got, first)
			}
		}
	}
}

// TestFxcClauseCutLeavesTruncateOneLineAlone is the promise the contract
// attaches to the placement rather than to the new symbol: TruncateOneLine
// "stays byte-identical for its other callers".
//
// It is pinned here because the two functions share obligation 1 and the
// cheapest way to implement the new one is to reach into the old one — which
// is fine, and stops being fine the moment the shared helper starts
// ellipsizing. TruncateOneLine's own contract has no clause cut and no floor.
func TestFxcClauseCutLeavesTruncateOneLineAlone(t *testing.T) {
	const in = "aaaa bbbb, cccc dddd, eeee ffff, gggg hhhh iiii jjjj"

	got := TruncateOneLine(in, fxcCutLimit)
	if strings.Contains(got, "aaaa bbbb, cccc dddd, eeee ffff…") {
		t.Errorf("TruncateOneLine has started clause-cutting: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > fxcCutLimit {
		t.Errorf("TruncateOneLine(%q, %d) = %q is %d runes, over its own limit", in, fxcCutLimit, got, n)
	}
	// And its one-lining is unchanged for a short input.
	if got := TruncateOneLine("a fact\nsplit over lines", 100); got != "a fact split over lines" {
		t.Errorf("TruncateOneLine one-lining changed: got %q", got)
	}
}
