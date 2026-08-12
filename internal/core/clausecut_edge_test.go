package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clausecut_edge_test.go — core.ClauseCut's boundaries, degenerate inputs and
// refusals, authored blind against 50-contract.md's "S1a — summary cutter"
// section. Every assertion below quotes the obligation it is derived from;
// where the contract is silent the test says so in a comment and asserts ONE
// answer, because a tolerant probe here would turn a contract gap into a
// green build.
//
// The happy paths (a clause marker in range wins, a plain sentence under the
// limit passes through) belong to the sibling contract suite; what lives here
// is the arithmetic at the edges: exactly-limit, one-over, the floor's two
// sides, the single sanctioned mid-word cut, and the all-punctuation input
// that empties its own keep.

// fxeOneLine reproduces obligation 1's sanitize step ("every rune < 0x20 or
// == 0x7f becomes a space, then whitespace runs collapse to single spaces")
// so the tests can measure the string ClauseCut measures rather than the raw
// input. It is a test-side restatement on purpose: comparing ClauseCut
// against itself would prove nothing.
func fxeOneLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// fxeSafeClauseCut calls ClauseCut behind a recover so a panic on a hostile
// limit fails ONE test instead of aborting the whole package's test binary
// and hiding every other result. A recovered panic is still a failure — the
// callers below assert panicked == nil.
func fxeSafeClauseCut(s string, limit int) (out string, panicked any) {
	defer func() { panicked = recover() }()
	out = ClauseCut(s, limit)
	return out, nil
}

// Obligation 1: "Empty input (or all-whitespace) returns ""."
// Control runes are covered here too, since they become spaces first and so
// reduce to the all-whitespace case.
func TestFxeClauseCutEmptyAndAllWhitespaceReturnEmpty(t *testing.T) {
	for _, in := range []string{
		"",
		" ",
		"      ",
		"\n\t\r\n ",
		"\x00\x01\x1f",   // control runes → spaces → nothing
		"\x7f",           // DEL, the other sanitized rune
		" \x00 \t \x7f ", // a mix of both
	} {
		if got := ClauseCut(in, 180); got != "" {
			t.Errorf("ClauseCut(%q, 180) = %q, want \"\" — obligation 1: empty or all-whitespace input returns the empty string", in, got)
		}
		// The same input must not acquire an ellipsis at a tiny limit either:
		// nothing was dropped, because there was nothing there.
		if got := ClauseCut(in, 4); got != "" {
			t.Errorf("ClauseCut(%q, 4) = %q, want \"\"", in, got)
		}
	}
}

// Obligation 2: "if the one-lined string is <= limit runes, return it
// unchanged — no ellipsis, byte-identical to the one-lined input."
// The boundary is the interesting half: EXACTLY limit runes is "at or under".
func TestFxeClauseCutAtTheLimitIsUnchangedAndUnellipsized(t *testing.T) {
	const s = "alpha beta gamma delta" // 22 runes
	if n := utf8.RuneCountInString(s); n != 22 {
		t.Fatalf("fixture is %d runes, not 22 — the arithmetic below assumes 22", n)
	}
	got := ClauseCut(s, 22)
	if got != s {
		t.Errorf("ClauseCut(%q, 22) = %q, want it returned byte-identical at exactly the limit", s, got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("ClauseCut(%q, 22) = %q — obligation 3: `…` appears iff text was dropped, and nothing was dropped", s, got)
	}
	// A limit far above the length is the same case.
	if got := ClauseCut(s, 180); got != s {
		t.Errorf("ClauseCut(%q, 180) = %q, want %q", s, got, s)
	}
}

// Obligation 2 again, but measured on the ONE-LINED string rather than the
// raw one: a 40-rune input whose collapsed form is 22 runes is under a
// 22-rune limit, and must come back one-lined with no ellipsis.
func TestFxeClauseCutMeasuresTheOneLinedLength(t *testing.T) {
	raw := "  alpha   beta\tgamma\ndelta  "
	want := "alpha beta gamma delta"
	if fxeOneLine(raw) != want {
		t.Fatalf("fixture one-lines to %q, not %q", fxeOneLine(raw), want)
	}
	if utf8.RuneCountInString(raw) <= 22 {
		t.Fatalf("fixture is not longer than the limit in RAW form, so it proves nothing")
	}
	if got := ClauseCut(raw, 22); got != want {
		t.Errorf("ClauseCut(%q, 22) = %q, want %q — the limit governs the ONE-LINED string, and nothing was dropped", raw, got, want)
	}
}

// Obligation 3: "Over the limit: text is dropped and the result ends with `…`
// (U+2026, one rune). The result INCLUDING the `…` is <= limit runes."
// One rune over is the boundary that separates obligation 2 from 3.
func TestFxeClauseCutOneRuneOverTheLimitIsCut(t *testing.T) {
	const s = "alpha beta gamma delta" // 22 runes; spaces at 5, 10, 16
	// limit 21 → budget 20, floor 10. No clause marker anywhere, so the word
	// cut takes the largest space index <= 20, which is 16.
	const want = "alpha beta gamma…"
	got := ClauseCut(s, 21)
	if got != want {
		t.Errorf("ClauseCut(%q, 21) = %q, want %q", s, got, want)
	}
	if n := utf8.RuneCountInString(got); n > 21 {
		t.Errorf("result is %d runes, over the 21-rune limit", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("result %q does not end with the ellipsis, though text was dropped", got)
	}
	if strings.Count(got, "…") != 1 {
		t.Errorf("result %q carries %d ellipses, want exactly one", got, strings.Count(got, "…"))
	}
}

// Obligation 3 + 4: the limit counts RUNES, not bytes. A 50-rune CJK string
// is 150 bytes; at limit 50 it passes through whole, and at 49 it is cut to
// 49 runes (145 bytes) — a byte-counting implementation would mangle both.
func TestFxeClauseCutCountsRunesNotBytes(t *testing.T) {
	s := strings.Repeat("知", 50)
	if got := ClauseCut(s, 50); got != s {
		t.Errorf("ClauseCut(50 CJK runes, 50) = %q, want it unchanged — 150 bytes is irrelevant, the limit is in runes", got)
	}
	// One over: no space anywhere, so obligation 4's hard cut applies —
	// budget = 48.
	want := strings.Repeat("知", 48) + "…"
	if got := ClauseCut(s, 49); got != want {
		t.Errorf("ClauseCut(50 CJK runes, 49) = %q, want %q", got, want)
	}

	// With spaces, so the word cut runs: 12 groups of 5 CJK runes = 71 runes,
	// spaces at 5, 11, 17, 23, 29, 35, 41... The largest <= budget 39 is 35.
	spaced := strings.TrimSpace(strings.Repeat("知知知知知 ", 12))
	if n := utf8.RuneCountInString(spaced); n != 71 {
		t.Fatalf("fixture is %d runes, not 71", n)
	}
	wantSpaced := strings.TrimSpace(strings.Repeat("知知知知知 ", 6)) + "…"
	got := ClauseCut(spaced, 40)
	if got != wantSpaced {
		t.Errorf("ClauseCut(spaced CJK, 40) = %q, want %q", got, wantSpaced)
	}
	if n := utf8.RuneCountInString(got); n > 40 {
		t.Errorf("result is %d runes, over the 40-rune limit", n)
	}
	if len(got) <= 40 {
		t.Errorf("result is %d bytes — the fixture was chosen so a correct rune-counted cut EXCEEDS the limit in bytes; %d bytes means bytes are being counted", len(got), len(got))
	}
}

// Obligation 4, hard cut: "no space anywhere in r[:budget+1] — a single
// unbroken token longer than the budget — keep r[:budget]. This is the ONE
// sanctioned mid-word cut."
func TestFxeClauseCutHardCutsAnUnbrokenToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{
			// Nothing but the token.
			"token only",
			strings.Repeat("a", 100), 20,
			strings.Repeat("a", 19) + "…",
		},
		{
			// A space exists, but only PAST budget+1 — still a hard cut, since
			// the rule reads r[:budget+1] and not the whole string.
			"space past the budget",
			strings.Repeat("a", 50) + " tail", 20,
			strings.Repeat("a", 19) + "…",
		},
		{
			// A clause marker exists too, and also past budget+1: it cannot
			// rescue the cut either.
			"marker past the budget",
			strings.Repeat("a", 50) + ", tail", 20,
			strings.Repeat("a", 19) + "…",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClauseCut(tc.in, tc.limit)
			if got != tc.want {
				t.Errorf("ClauseCut(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
			if n := utf8.RuneCountInString(got); n != tc.limit {
				t.Errorf("hard cut produced %d runes, want exactly the %d-rune limit (budget + the ellipsis)", n, tc.limit)
			}
		})
	}
}

// Obligation 4, clause cut: "For each occurrence of a marker beginning at rune
// index i with floor <= i <= budget". A marker BELOW the floor does not
// qualify, so the word cut runs instead — the case that stops a 180-rune fact
// from being cut down to its first two words because it opened with a comma.
func TestFxeClauseCutClauseMarkerBelowTheFloorDoesNotQualify(t *testing.T) {
	// limit 40 → budget 39, floor 20. The only marker is ", " at index 2.
	// Word cut takes the largest space index <= 39, which is 39.
	const s = "ab, cdefg hijkl mnopq rstuv wxyza bcdef ghijk lmnop"
	if s[2] != ',' || s[3] != ' ' || s[39] != ' ' {
		t.Fatalf("fixture indices moved: %q", s)
	}
	want := s[:39] + "…"
	got := ClauseCut(s, 40)
	if got != want {
		t.Errorf("ClauseCut(%q, 40) = %q, want %q — a marker at index 2 is below the floor (20) and must not be taken", s, got, want)
	}
	if got == s[:2]+"…" {
		t.Errorf("the sub-floor clause marker was taken, cutting the sentence to %q", got)
	}
}

// Obligation 4, clause cut: the window "floor <= i <= budget" is INCLUSIVE at
// both ends, and one past the top end is out. Each case is built so the
// clause answer and the word answer differ, which is the only way an
// off-by-one shows up.
func TestFxeClauseCutClauseMarkerWindowIsInclusive(t *testing.T) {
	t.Run("marker exactly at the floor qualifies", func(t *testing.T) {
		// limit 40 → floor 20. ", " begins at index 20; the word cut would
		// have taken index 39 instead.
		const s = "aaaaabbbbbcccccddddd, eeeee fffff ggggg hhhhh"
		if s[20] != ',' || s[21] != ' ' {
			t.Fatalf("fixture indices moved: %q", s)
		}
		want := s[:20] + "…"
		if got := ClauseCut(s, 40); got != want {
			t.Errorf("ClauseCut(%q, 40) = %q, want %q — i == floor is inside the window", s, got, want)
		}
	})

	t.Run("marker exactly at the budget qualifies", func(t *testing.T) {
		// limit 40 → budget 39. ", " begins at index 39; the word cut would
		// have taken index 29.
		const s = "aaaaaaaaa bbbbbbbbb ccccccccc ddddddddd, eeeee fff"
		if s[39] != ',' || s[40] != ' ' {
			t.Fatalf("fixture indices moved: %q", s)
		}
		want := s[:39] + "…"
		got := ClauseCut(s, 40)
		if got != want {
			t.Errorf("ClauseCut(%q, 40) = %q, want %q — i == budget is inside the window", s, got, want)
		}
		if n := utf8.RuneCountInString(got); n != 40 {
			t.Errorf("result is %d runes, want exactly 40 (the widest legal keep plus the ellipsis)", n)
		}
	})

	t.Run("marker one past the budget does not qualify", func(t *testing.T) {
		// limit 40 → budget 39. ", " begins at index 40, one too far; the word
		// cut takes index 30.
		const s = "aaaaaaaaaa bbbbbbbbb ccccccccc ddddddddd, eeeee"
		if s[40] != ',' || s[30] != ' ' {
			t.Fatalf("fixture indices moved: %q", s)
		}
		want := s[:30] + "…"
		got := ClauseCut(s, 40)
		if got != want {
			t.Errorf("ClauseCut(%q, 40) = %q, want %q — i == budget+1 is outside the window", s, got, want)
		}
		if n := utf8.RuneCountInString(got); n > 40 {
			t.Errorf("taking the out-of-window marker produced %d runes, over the limit", n)
		}
	})
}

// Obligation 5: "trim trailing runes in the set ' ', ',', ';', ':', '.', '—'
// from the keep, then append `…`". A cut that lands just after a period must
// not read as "sentence end, then more" — `.…` is the shape this forbids.
func TestFxeClauseCutTrimsTrailingPunctuationBeforeTheEllipsis(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		keep int // rune index the trimmed keep ends at
	}{
		// limit 40 → budget 39, floor 20. Word cut at the space (index 39);
		// the keep ends with the punctuation at index 38, which is trimmed.
		{"trailing period", "aaaaaaaaa bbbbbbbbb ccccccccc dddddddd. eeeee ffff", 38},
		{"trailing em dash", "aaaaaaaaa bbbbbbbbb ccccccccc dddddddd— eeeee ffff", 38},
		{"two trailing periods", "aaaaaaaaa bbbbbbbbb ccccccccc ddddddd.. eeeee ffff", 37},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := []rune(tc.in)
			if r[39] != ' ' {
				t.Fatalf("fixture index 39 is %q, not a space: %q", r[39], tc.in)
			}
			want := string(r[:tc.keep]) + "…"
			got := ClauseCut(tc.in, 40)
			if got != want {
				t.Errorf("ClauseCut(%q, 40) = %q, want %q", tc.in, got, want)
			}
			for _, bad := range []string{" …", ",…", ";…", ":…", ".…", "—…"} {
				if strings.HasSuffix(got, bad) {
					t.Errorf("result %q ends with %q — obligation 5 trims that rune before the ellipsis", got, bad)
				}
			}
		})
	}
}

// Obligation 5's fallback: "If trimming empties the keep entirely (degenerate
// all-punctuation input), fall back to r[:budget] trimmed of trailing spaces
// only, then `…`." Three shapes, one per cut path that can reach it.
func TestFxeClauseCutDegenerateAllPunctuationFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{
			// Hard-cut path: no space anywhere, so the keep is r[:9], which
			// trims away to nothing and comes back whole.
			"unbroken commas",
			strings.Repeat(",", 15), 10,
			strings.Repeat(",", 9) + "…",
		},
		{
			// Word-cut path: keep = r[:9] = ". . . . .", which trims to "".
			"alternating periods",
			strings.TrimSpace(strings.Repeat(". ", 12)), 10,
			". . . . .…",
		},
		{
			// Clause-cut path: markers at 0,2,4,6,8; floor 5 and budget 9 admit
			// 6 and 8, largest wins (8), keep = ", , , ," which trims to "".
			"alternating commas",
			strings.TrimSpace(strings.Repeat(", ", 12)), 10,
			", , , , ,…",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClauseCut(tc.in, tc.limit)
			if got != tc.want {
				t.Errorf("ClauseCut(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
			if got == "…" {
				t.Errorf("the fallback collapsed to a bare ellipsis — obligation 5 keeps r[:budget]")
			}
			if n := utf8.RuneCountInString(got); n > tc.limit {
				t.Errorf("result is %d runes, over the %d-rune limit", n, tc.limit)
			}
		})
	}
}

// Obligation 4: "in every other case the result never ends mid-word." Stated
// as a property rather than a fixture, because the failure it guards against
// is a half-word rendered as a complete claim in an injected brief.
//
// The check: strip the ellipsis, and what remains must be a prefix of the
// one-lined input whose NEXT rune is a separator — a space (word cut, or the
// em-dash marker's leading space), or one of the runes obligation 5 trims.
func TestFxeClauseCutNeverEndsMidWordWhenASpaceIsInBudget(t *testing.T) {
	inputs := []string{
		"the quick brown fox jumps over the lazy dog and keeps running along the fence",
		"kraken serve, the single writer, owns the scheduler tick and the web API forever",
		"zeus plans and briefs — hephaestus implements each slice; athena reviews them all",
		"one: two three four five six seven eight nine ten eleven twelve thirteen fourteen",
		"aaa bbb, ccc; ddd: eee — fff ggg hhh iii jjj kkk lll mmm nnn ooo ppp qqq rrr sss",
	}
	separators := map[rune]bool{' ': true, ',': true, ';': true, ':': true, '.': true, '—': true}

	for _, in := range inputs {
		one := fxeOneLine(in)
		if one != in {
			t.Fatalf("fixture is not already one-lined: %q", in)
		}
		r := []rune(one)
		for _, limit := range []int{25, 30, 40} {
			if len(r) <= limit {
				continue
			}
			got := ClauseCut(in, limit)
			if n := utf8.RuneCountInString(got); n > limit {
				t.Errorf("ClauseCut(%q, %d) = %q, %d runes — over the limit", in, limit, got, n)
				continue
			}
			if !strings.HasSuffix(got, "…") {
				t.Errorf("ClauseCut(%q, %d) = %q — text was dropped, so it must end with the ellipsis", in, limit, got)
				continue
			}
			keep := []rune(strings.TrimSuffix(got, "…"))
			if len(keep) == 0 {
				t.Errorf("ClauseCut(%q, %d) kept nothing though a space sits inside the budget", in, limit)
				continue
			}
			if string(r[:len(keep)]) != string(keep) {
				t.Errorf("ClauseCut(%q, %d) = %q — the keep is not a prefix of the one-lined input", in, limit, got)
				continue
			}
			next := r[len(keep)]
			if !separators[next] {
				t.Errorf("ClauseCut(%q, %d) = %q ends mid-word: the next input rune is %q, not a separator", in, limit, got, next)
			}
		}
	}
}

// Obligation 3: "The result INCLUDING the `…` is <= limit runes." Swept over
// hostile inputs and small limits, where the ellipsis reservation and the
// floor arithmetic collide. Obligation 2 rides along: an input already inside
// the limit must come back one-lined and untouched.
func TestFxeClauseCutNeverExceedsTheLimitOnAdversarialInput(t *testing.T) {
	inputs := []string{
		"",
		"   ",
		"a",
		"ab",
		"ab, cd",
		", leading marker then a long tail that runs well past any small budget",
		"trailing marker at the very end, ",
		strings.Repeat("x", 500),
		strings.Repeat(",", 40),
		strings.Repeat(". ", 40),
		"  ,  ;  :  ",
		"a — b — c — d — e — f — g — h",
		"word, another; third: fourth — fifth, sixth; seventh: eighth",
		strings.Repeat("知", 200),
		strings.Repeat("\U0001D356", 60), // 4-byte runes, no spaces anywhere
		"知 知知 知知知 知知知知 知知知知知 知知知知知知",
		"one\ttwo\nthree\r\nfour\x00five",
	}
	for _, limit := range []int{1, 2, 3, 5, 8, 13, 40, 180} {
		for _, in := range inputs {
			got, panicked := fxeSafeClauseCut(in, limit)
			if panicked != nil {
				t.Errorf("ClauseCut(%q, %d) panicked: %v — a pure cutter on the spawn path must never panic", in, limit, panicked)
				continue
			}
			if n := utf8.RuneCountInString(got); n > limit {
				t.Errorf("ClauseCut(%q, %d) = %q is %d runes, over the limit", in, limit, got, n)
			}
			one := fxeOneLine(in)
			if utf8.RuneCountInString(one) <= limit {
				if got != one {
					t.Errorf("ClauseCut(%q, %d) = %q, want the one-lined input %q unchanged — nothing needed dropping", in, limit, got, one)
				}
				if strings.Contains(got, "…") {
					t.Errorf("ClauseCut(%q, %d) = %q carries an ellipsis though nothing was dropped", in, limit, got)
				}
			} else if !strings.HasSuffix(got, "…") {
				t.Errorf("ClauseCut(%q, %d) = %q — text was dropped, so the result must end with `…`", in, limit, got)
			}
		}
	}
}

// CONTRACT SILENCE: obligation 4 computes `budget := limit - 1` and
// `floor := limit / 2` with no stated guard, so a limit of 0 or less has no
// specified answer (and r[:budget] with a negative budget would panic).
// Nothing in the binary calls ClauseCut with a non-positive limit today —
// factLine passes graphFactRunes (180) — but a helper exported beside
// TruncateOneLine will acquire callers.
//
// Asserted here, strictly: a non-positive limit has room for nothing, so the
// answer is "" — never a panic, and never a lone "…" (which would claim text
// was dropped into a budget that could not hold the ellipsis either).
func TestFxeClauseCutNonPositiveLimitYieldsEmpty(t *testing.T) {
	for _, limit := range []int{0, -1, -180} {
		for _, in := range []string{"", "short", strings.Repeat("a", 500), "a, b, c"} {
			got, panicked := fxeSafeClauseCut(in, limit)
			if panicked != nil {
				t.Errorf("ClauseCut(%q, %d) panicked: %v — a pure cutter must refuse, not crash the caller", in, limit, panicked)
				continue
			}
			if got != "" {
				t.Errorf("ClauseCut(%q, %d) = %q, want \"\" — a non-positive limit has room for nothing, not even the ellipsis", in, limit, got)
			}
		}
	}
}

// Obligation 6: "Pure function, no side effects, deterministic." The same
// input must produce the same bytes however many times it is asked, and one
// call must not disturb the next — the property the injected block's
// reproducibility rests on.
func TestFxeClauseCutIsDeterministicAcrossRepeatedCalls(t *testing.T) {
	inputs := []string{
		"kraken serve, the single writer, owns the scheduler tick and the web API forever",
		strings.Repeat("知", 200),
		strings.TrimSpace(strings.Repeat(", ", 12)),
	}
	for _, in := range inputs {
		first := ClauseCut(in, 40)
		for i := 0; i < 5; i++ {
			// Interleave a different input, so a cutter holding state between
			// calls is caught rather than agreeing with itself.
			ClauseCut("something else entirely, with its own clause markers", 17)
			if got := ClauseCut(in, 40); got != first {
				t.Fatalf("call %d for %q = %q, want %q — ClauseCut must be pure", i, in, got, first)
			}
		}
	}
}
