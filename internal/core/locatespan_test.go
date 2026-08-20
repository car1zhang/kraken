package core

import (
	"strings"
	"testing"
)

// locatespan_test.go — pins for the GATE-R1 amendment (job 349): element marks
// preserve source structure by locating the rendered capture's span in the
// markdown source. LEAD-AUTHORED and sighted, recorded as such in that job's
// build report: the blind suites were authored before the amendment existed,
// so none of their assertions distinguish rendered-content from
// located-content — this file is the amendment's only pin.

const locateFixture = `Some prose before the table that mentions cells.

| tier | cells | share |
|---|---|---|
| zero | 8,430 | 94.4% |
| one | 12 | 5.6% |

And after.

1. first item with a [link](https://example.com/deep/path) inside
2. second item
3. third item
`

func locateSlice(t *testing.T, source, rendered string) (string, bool) {
	t.Helper()
	start, end, ok := LocateProjectedSpan(source, rendered)
	if !ok {
		return "", false
	}
	if start < 0 || end > len(source) || start >= end {
		t.Fatalf("LocateProjectedSpan returned span [%d,%d) outside source len %d", start, end, len(source))
	}
	return source[start:end], true
}

// A rendered table capture (tab-separated cells, separator row gone — what
// innerText yields) locates the pipe table and comes back with the pipes and
// the separator row intact. This is the carl complaint verbatim.
func TestLocateProjectedSpanRenderedTableRecoversPipes(t *testing.T) {
	got, ok := locateSlice(t, locateFixture, "tier\tcells\tshare\nzero\t8,430\t94.4%\none\t12\t5.6%")
	if !ok {
		t.Fatal("rendered table capture did not locate")
	}
	want := "| tier | cells | share |\n|---|---|---|\n| zero | 8,430 | 94.4% |\n| one | 12 | 5.6% |"
	if got != want {
		t.Fatalf("located content = %q, want the pipe table %q", got, want)
	}
}

// A rendered ordered-list capture (markers gone — ::marker never reaches
// innerText) locates the list and comes back with the markers and the link
// url the renderer deleted.
func TestLocateProjectedSpanRenderedListRecoversMarkersAndURL(t *testing.T) {
	got, ok := locateSlice(t, locateFixture, "first item with a link inside\nsecond item\nthird item")
	if !ok {
		t.Fatal("rendered list capture did not locate")
	}
	want := "1. first item with a [link](https://example.com/deep/path) inside\n2. second item\n3. third item"
	if got != want {
		t.Fatalf("located content = %q, want the marked list %q", got, want)
	}
}

// The subsequence rung is MINIMAL-WINDOW: a single list item whose url the
// renderer stripped must come back as that item's own line, never the
// document preamble a forward-only greedy would anchor at (the measured
// failure the contract row records).
func TestLocateProjectedSpanSubsequenceIsMinimalWindow(t *testing.T) {
	got, ok := locateSlice(t, locateFixture, "first item with a link inside")
	if !ok {
		t.Fatal("url-stripped item did not locate")
	}
	want := "1. first item with a [link](https://example.com/deep/path) inside"
	if got != want {
		t.Fatalf("located content = %q, want the single item line %q — a prefix-anchored span means the forward-only greedy came back", got, want)
	}
}

// The three not-ok paths the contract row names, plus the span bound.
func TestLocateProjectedSpanNotLocatable(t *testing.T) {
	for _, c := range []struct {
		name, source, rendered string
	}{
		{"empty rendered projection", locateFixture, "—…!!"},
		{"empty source", "", "tier cells"},
		{"text absent", locateFixture, "entirely different words nowhere present zebra quixotic"},
	} {
		if _, _, ok := LocateProjectedSpan(c.source, c.rendered); ok {
			t.Errorf("%s: located, want not ok", c.name)
		}
	}
	// Span bound: the capture's runes exist in order but only scattered so far
	// apart that the tight window blows the 3*len+200 budget — a "match" like
	// that is a failed locate, not a located mark.
	scattered := "q" + strings.Repeat("filler words here ", 40) + "w" + strings.Repeat("more padding text ", 40) + "z"
	if _, _, ok := LocateProjectedSpan(scattered, "qwz"); ok {
		t.Error("scattered subsequence located, want the span bound to refuse it")
	}
}

// Whole-line expansion: a capture matching mid-line comes back as the full
// source line, and a contiguous hit spanning lines lands on line boundaries.
func TestLocateProjectedSpanExpandsToWholeLines(t *testing.T) {
	got, ok := locateSlice(t, locateFixture, "prose before the table")
	if !ok {
		t.Fatal("mid-line capture did not locate")
	}
	if got != "Some prose before the table that mentions cells." {
		t.Fatalf("located content = %q, want the whole source line", got)
	}
}
