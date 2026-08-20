package core

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// anchor.go — the pure arithmetic behind the mental model's code anchors.
//
// A code anchor records a line range carl actually read, in the coordinates of
// the commit it was read at. Keeping it honest as origin/main moves is two
// questions, and this file answers the halves of both that need no git:
//
//  1. "Did anything touch those lines?" — jobs' verify pass runs
//     `git diff --unified=0 --ignore-all-space -M50% <anchor>..<head>` and
//     hands the output to ParseHunks + MapRange. A range no hunk intersects is
//     PROVABLY unchanged, and MapRange says where it moved to.
//  2. "If something did touch them, is the text still somewhere?" — LocateText
//     and StripWS are the exact and whitespace-normalized rungs of that
//     ladder, run against blobs the caller reads out of git.
//
// The division of labour is the whole reason this file exists in core rather
// than in jobs: everything here is a total function of strings and integers,
// so every git subtlety the pass has to survive (shifts, renames, block moves,
// whitespace-only reformatting) becomes a table row rather than a fixture
// repository. Nothing here shells out, reads a file, or touches the store.
//
// THE ONE INVARIANT THAT IS NOT OBVIOUS: hunk overlap NEVER decides that an
// anchor died. MapRange's `touched` is permission to run the text ladder, not
// a verdict — a pure block move produces hunks that can miss the moved range
// entirely in either direction, so "the diff intersects" and "the text is
// gone" are genuinely different questions. Only text absence may say
// "changed"; see 50-components.md's mmVerifyPass row.
//
// Whitespace policy is deliberately identical on both sides: stage (a) runs
// git with --ignore-all-space, and StripWS is stage (b)'s matching rung. If
// the two ever disagree, a whitespace-only reformat becomes non-deterministic
// — provably-unchanged one pass, invalidated the next.

// Hunk is one `@@ -OldStart,OldCount +NewStart,NewCount @@` header from a
// unified diff, already parsed. Counts are line counts, starts are 1-based;
// a pure insertion has OldCount 0 and a pure deletion has NewCount 0.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
}

// hunkHeaderRe matches a unified-diff hunk header's ranges and nothing else.
// The trailing `@@` is required so a line of CONTENT that happens to begin
// with "@@ -" — a diff of this very file, say — cannot be read as a header;
// the optional section heading git appends after it is ignored.
//
// Each count is optional because git omits `,1`: `@@ -4 +6 @@` is a one-line
// range on both sides, which is the most common header shape in
// `--unified=0` output and the one an anchored regexp is easiest to get
// wrong on.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseHunks reads every hunk header out of a unified diff, in file order.
//
// Everything that is not a header is ignored, which is what lets the caller
// hand it whole `git diff` output including the rename preamble (`diff --git`,
// `similarity index`, `rename from/to`) and the `---`/`+++` file lines. A diff
// with no headers at all — an empty diff, or a pure rename with no content
// change, which git reports as preamble ONLY — returns nil, and that is the
// answer MapRange needs: no hunks means no shift and nothing touched.
//
// An omitted count means 1, per the unified-diff format. Nothing else is
// defaulted: a header git did not write is not invented.
//
// The caller is expected to pass `--unified=0` output. This function does not
// enforce that — it cannot tell from the text — but with context lines the
// hunks are wider than the changes and MapRange's "touched" would fire on
// ranges nothing actually edited.
func ParseHunks(diff string) []Hunk {
	var out []Hunk
	for _, line := range strings.Split(diff, "\n") {
		m := hunkHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h := Hunk{
			OldStart: atoiOr(m[1], 0),
			OldCount: atoiOr(m[2], 1),
			NewStart: atoiOr(m[3], 0),
			NewCount: atoiOr(m[4], 1),
		}
		out = append(out, h)
	}
	return out
}

// atoiOr parses s, returning def for an empty string (an omitted hunk count)
// and 0 for anything unparseable — which the regexp above has already made
// impossible, since every captured group is `\d+`.
func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// MapRange projects the 1-based inclusive line range [start, end] from the
// anchor commit's coordinates into head's, and reports whether any hunk
// intersects it.
//
// The two answers are independent and both are always valid. newStart/newEnd
// are start/end plus the shift accumulated from hunks lying STRICTLY EARLIER
// than the range — so a caller whose range was touched still gets a usable
// "where it would have been", which is what the text ladder uses to pick the
// nearest occurrence when the text turns up more than once.
//
// The shift of an earlier hunk is `NewCount - OldCount`, the number of lines
// it added or removed. It is NEVER `NewStart - OldStart`: that difference is
// the accumulated offset git happens to have reached at that point in the
// file, so summing it across hunks double-counts every earlier one.
//
// A PURE INSERTION (OldCount == 0) is the case worth spelling out, because
// its OldStart means something different from every other hunk's: git reports
// `-N,0` for text inserted AFTER old line N, so the insertion sits in the gap
// between old lines N and N+1 and touches neither.
//
//   - OldStart < start — the gap is above the range, which therefore slides
//     down by NewCount.
//   - OldStart >= end — the gap is at or below the range's last line. Text
//     appended directly after the anchor's final line does not change the
//     anchor, so this is a no-op rather than a touch. (`>=` rather than `>`
//     is the load-bearing half: the gap after line `end` is exactly the
//     boundary case, and reading it as a touch would invalidate every anchor
//     someone appended a line beneath.)
//   - otherwise — the gap is strictly inside, splitting the block carl read.
//     Touched.
//
// Any other hunk covers old lines [OldStart, OldStart+OldCount-1]. Entirely
// above the range shifts it; entirely below is ignored (hunks arrive in file
// order, so a later one cannot matter to an earlier range); anything else
// intersects, and is touched.
//
// Touched is a question, not a verdict — see this file's header.
func MapRange(hunks []Hunk, start, end int) (newStart, newEnd int, touched bool) {
	shift := 0
	for _, h := range hunks {
		if h.OldCount == 0 {
			switch {
			case h.OldStart < start:
				shift += h.NewCount
			case h.OldStart >= end:
				// Below the range's last line: no effect.
			default:
				touched = true
			}
			continue
		}
		last := h.OldStart + h.OldCount - 1
		switch {
		case last < start:
			shift += h.NewCount - h.OldCount
		case h.OldStart > end:
			// Entirely below the range: ignored.
		default:
			touched = true
		}
	}
	return start + shift, end + shift, touched
}

// LocateText returns the 1-based line number of the line containing the first
// byte of every occurrence of text inside blob, in file order.
//
// Both sides are `\r\n`-normalized first, which is what makes a mark taken
// from an editor buffer comparable with the bytes git hands back for the same
// file. An empty blob or an empty text yields nil: there is no such thing as
// locating nothing, and returning "line 1, matched everywhere" would let an
// empty anchor survive every verify forever.
//
// The match is a plain BYTE substring — it is not required to start at a line
// boundary, and a text that does start at one will only match where blob does
// too, since the leading newline is part of the needle. Occurrences may
// overlap, so the scan advances one byte past each hit rather than past the
// whole match; nothing in the anchor ladder depends on that, but "every
// occurrence" is the promise the name makes.
//
// More than one result is the AMBIGUOUS case the verify pass handles by
// picking the occurrence nearest the range MapRange projected — and never by
// invalidating, which is why this returns all of them rather than a best
// guess.
func LocateText(blob, text string) []int {
	blob = normalizeEOL(blob)
	text = normalizeEOL(text)
	if blob == "" || text == "" {
		return nil
	}
	var out []int
	line, scanned := 1, 0
	for i := 0; i < len(blob); {
		j := strings.Index(blob[i:], text)
		if j < 0 {
			break
		}
		at := i + j
		// Newlines are counted forward from the previous hit rather than from
		// the top of the file each time, so N occurrences cost one pass over
		// the blob rather than N.
		line += strings.Count(blob[scanned:at], "\n")
		scanned = at
		out = append(out, line)
		i = at + 1
	}
	return out
}

// NearestLine picks the candidate line closest to want — the tie-break the
// anchor ladder applies when text turns up in more than one place.
//
// Multiple hits are never an error and never invalidate anything: a helper
// called three times in one file is an ordinary thing to mark, so the answer
// is the occurrence nearest where the caller said it was, which is the only
// signal distinguishing them. Ambiguity is flagged on the row, not punished.
//
// TIES GO TO THE EARLIER candidate (the comparison is strict `<`), which is
// what makes the choice stable across passes: an anchor equidistant between
// two copies must not oscillate between them every time the pass runs, since
// each swing rewrites the stored range.
//
// Empty cands is 0 rather than a panic. There is no such thing as the nearest
// of nothing, and both callers reach this only after checking — but a total
// function is what lets the CAPTURE side (api) and the VERIFY side (jobs)
// share one implementation rather than each guarding its own copy, which is
// the whole reason this lives in core beside the rest of the anchor
// arithmetic.
func NearestLine(cands []int, want int) int {
	if len(cands) == 0 {
		return 0
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if absInt(c-want) < absInt(best-want) {
			best = c
		}
	}
	return best
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// StripWS removes every Unicode whitespace rune from s.
//
// It is the whitespace-normalized rung of the text ladder, and it must agree
// with what `git diff --ignore-all-space` calls equal in stage (a): a change
// that only moves whitespace around compares equal here too. Removing rather
// than collapsing runs is the strict reading of "ignore all space" — a run
// collapsed to one space would still differ from a run deleted entirely, and
// reformatters do both.
//
// unicode.IsSpace rather than an ASCII set: source files carry non-breaking
// spaces and ideographic spaces, and a rung that missed them would report a
// reformat as a death.
func StripWS(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// EndRowFromSelection derives the 1-based inclusive END row of a selection
// that starts at startRow, from the selected text alone.
//
// The editor side of the mental model is a Zed task, and Zed exposes the
// selection's start row but not its end (the upstream request for ZED_END_ROW
// was closed as not-planned), so the end has to be counted out of the text.
//
// end = startRow + newlines(selection), minus one when the selection ends in
// a newline. That last subtraction is the whole-line case: selecting a line
// with the cursor wrapping onto the next one yields "text\n", one newline but
// one line — without it every whole-line mark would claim one line more than
// carl selected, and the anchor's text would not match the range beside it.
//
// An empty selection is startRow: the caller's documented meaning for it is
// "the single line the cursor is on". The result never falls below startRow.
func EndRowFromSelection(startRow int, selection string) int {
	if selection == "" {
		return startRow
	}
	s := normalizeEOL(selection)
	end := startRow + strings.Count(s, "\n")
	if strings.HasSuffix(s, "\n") {
		end--
	}
	if end < startRow {
		return startRow
	}
	return end
}

// normalizeEOL folds CRLF to LF and nothing else. Lone CRs are left alone
// deliberately: they are not a line ending on any platform this runs against,
// and rewriting them would change bytes the anchor is supposed to preserve
// exactly.
func normalizeEOL(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// ProjectAlnum keeps only the unicode letters and digits of s, in order, with
// case preserved.
//
// It is the one projection the mental model compares text through, and it has
// exactly one job: strip away everything the markdown RENDERER either adds or
// removes, so that a capture taken from the rendered board and the source
// lines it came from can be compared at all. Pipes, list markers, fence
// backticks, link syntax, every kind of whitespace — all of it is punctuation
// or space, none of it survives here, so `| zero | 8,430 |` and
// `zero 8430` project to the same runes.
//
// Both readers of the projection share this function rather than each keeping
// a copy: the verify pass (internal/jobs' element-presence test) and the
// capture-side locate below are two halves of ONE comparison — the pass asks
// whether what the locate stored is still there — and two copies of a
// projection are two things that can drift into disagreeing about what a
// mark's identity is.
//
// The assumptions this projection makes about unicode normal form, and why
// short projections are effectively unfalsifiable through it, are recorded
// where the verify pass uses it; nothing here re-states them.
func ProjectAlnum(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LocateProjectedSpan finds where a RENDERED capture came from in the markdown
// SOURCE it was rendered out of, and returns that span as whole source lines.
//
// The problem it solves is that the board hands back what the browser shows.
// Selecting a table yields tab-separated cell text with no pipes; selecting an
// ordered list yields the item text with the markers the renderer generated
// rather than the `1.` in the file. Storing that as the mark loses the
// structure carl marked — the thing he complained about at the gate — so the
// capture is instead treated as a POINTER into the source, and the source
// lines are what gets stored. Everything downstream (the idempotence probe,
// supersede, the episode mint) keys on the content it is handed, so it does
// not care which form won.
//
// Both sides are compared through ProjectAlnum, because that is the only
// comparison a rendered capture and its source can pass at all. Two rungs:
//
//  1. CONTIGUOUS. The rendered projection appears as a substring of the source
//     projection. First occurrence wins — a document holding the passage twice
//     is a document where either copy is an honest answer, and picking the
//     first is at least stable across calls.
//  2. MINIMAL-WINDOW SUBSEQUENCE. The renderer only ever DELETES alphanumerics
//     from the source (a link's url, an ordered-list marker's digits, a
//     fence's language tag), so a rendered projection is a subsequence of its
//     source's even when it is not a substring. The window is found in two
//     passes — forward to the earliest rune at which the capture can be
//     COMPLETED, then backward from that same rune to the LATEST rune at which
//     the match can start — and the span runs between them. Forward alone
//     would anchor at the document's first matching rune and store the
//     preamble; the body says why at the loop.
//
// THE BOUND ON RUNG 2 IS WHAT MAKES IT A LOCATE RATHER THAN A CURIOSITY. A
// subsequence needs only the wanted runes somewhere in order, and an ordinary
// artifact carries orders of magnitude more alphanumerics than any one mark,
// so an unrelated passage's runes can nearly always be spelled out across the
// whole document. That match is not a location — it is the absence of one. So
// a matched span wider than `3*len(rendered projection) + 200` projected runes
// is REFUSED: three times allows for everything a renderer plausibly drops
// inside a real passage (urls are the fat case), and the additive 200 keeps
// short marks, where the multiple alone is far too tight, from being refused
// for the fixed cost of a table's own markup.
//
// A refusal is not an error and the caller is expected to have an answer for
// it: not-ok means "store what you were given", which is exactly the behaviour
// that existed before this function did.
//
// The returned span is BYTE offsets into source, expanded outward to whole
// lines (from just after the preceding newline to just before the following
// one, so source[start:end] carries no trailing newline). Whole lines are the
// point: half a table row is not a table row, and the structure only survives
// if the markup around the match comes with it.
//
// source is expected to be `\r\n`-normalized ALREADY — the offsets are into
// the string as passed, so normalizing in here would return offsets into a
// string the caller does not hold. rendered is projected, so its line endings
// cannot matter.
func LocateProjectedSpan(source, rendered string) (start, end int, ok bool) {
	want := ProjectAlnum(rendered)
	if want == "" {
		// Nothing alphanumeric to match on: every test would succeed against
		// every document, which is the one answer that must never be reported
		// as a location.
		return 0, 0, false
	}
	proj, offs := projectWithOffsets(source)
	if proj == "" {
		return 0, 0, false
	}
	wantRunes := utf8.RuneCountInString(want)

	if i := strings.Index(proj, want); i >= 0 {
		s, e := expandToLines(source, offs[i].start, offs[i+len(want)-1].end)
		return s, e, true
	}

	// Greedy subsequence, in TWO passes, and the second one is not an
	// optimization — it is what makes the span mean anything.
	//
	// Forward alone matches each of want's runes at the earliest place it can,
	// so the span starts at the first occurrence anywhere in the document of
	// want's FIRST rune. For a passage halfway down a real artifact that is
	// rune ~0, and the "matched span" is then the whole document prefix: the
	// bound below rejects the genuine locates it exists to protect, and the
	// ones it lets through store the preamble as the mark. Measured on a
	// four-paragraph fixture before this pass was added — a list item located
	// as itself PLUS every line above it.
	//
	// So: forward to find the earliest rune at which want can be COMPLETED,
	// then backward from exactly that rune to find the latest rune at which
	// the same match can START. The result is the tightest window ending
	// there, which is the span the bound is a statement about.
	w := []rune(want)

	// Forward: the earliest byte at which want can be completed.
	next, endByte := 0, -1
	for i, r := range proj {
		if r == w[next] {
			endByte = i
			if next++; next == len(w) {
				break
			}
		}
	}
	if next < len(w) || endByte < 0 {
		return 0, 0, false
	}

	// Backward from that same rune: the latest byte at which the match can
	// start, and the window's width in projected runes counted as it goes.
	head := proj[:endByte+utf8.RuneLen(w[len(w)-1])]
	back, startByte, span := len(w)-1, -1, 0
	for len(head) > 0 {
		r, size := utf8.DecodeLastRuneInString(head)
		head = head[:len(head)-size]
		span++
		if r == w[back] {
			startByte = len(head)
			if back--; back < 0 {
				break
			}
		}
	}
	if back >= 0 || startByte < 0 {
		return 0, 0, false
	}
	if span > 3*wantRunes+200 {
		return 0, 0, false
	}
	s, e := expandToLines(source, offs[startByte].start, offs[endByte].end)
	return s, e, true
}

// projSpan is one projected rune's footprint in the source: the byte offsets
// its original rune occupied. Kept per BYTE of the projection rather than per
// rune so a strings.Index result — a byte index — indexes it directly.
type projSpan struct{ start, end int }

// projectWithOffsets is ProjectAlnum plus the map back. offs is parallel to
// the returned projection's BYTES: every byte of a projected rune carries that
// rune's own [start,end) in source, so a match at projection bytes [i,j)
// spans source [offs[i].start, offs[j-1].end).
func projectWithOffsets(s string) (string, []projSpan) {
	var b strings.Builder
	b.Grow(len(s))
	offs := make([]projSpan, 0, len(s))
	for i, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		n, _ := b.WriteRune(r)
		span := projSpan{start: i, end: i + utf8.RuneLen(r)}
		for k := 0; k < n; k++ {
			offs = append(offs, span)
		}
	}
	return b.String(), offs
}

// expandToLines widens the byte span [start,end) to the whole source lines it
// touches: back to just after the previous newline, forward to just before the
// next one. The trailing newline is left OUT so the slice reads as content
// rather than as a block with a dangling blank.
func expandToLines(source string, start, end int) (int, int) {
	if i := strings.LastIndexByte(source[:start], '\n'); i >= 0 {
		start = i + 1
	} else {
		start = 0
	}
	if j := strings.IndexByte(source[end:], '\n'); j >= 0 {
		end += j
	} else {
		end = len(source)
	}
	return start, end
}
