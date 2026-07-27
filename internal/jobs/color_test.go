package jobs

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// --- the palette itself ---

func TestPaletteIsTheEightColorNames(t *testing.T) {
	// exactly /color's eight, in /color's own order. Anything kraken types
	// that isn't on this list is a name the claude will reject, so this is a
	// contract with a shipped binary, not a style choice.
	want := []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"}
	if len(Palette) != len(want) {
		t.Fatalf("palette has %d entries, want %d", len(Palette), len(want))
	}
	for i, name := range want {
		if Palette[i].Name != name {
			t.Errorf("palette[%d] = %q, want %q", i, Palette[i].Name, name)
		}
	}
	// "default" is /color's clear verb, not a colour — it must never be an
	// entry, or a job could be authored with color: default and kraken would
	// treat a clear as a set.
	if ValidColor("default") {
		t.Error(`"default" must not be a palette entry — it is /color's clear verb`)
	}
}

// xterm256Nearest recomputes a hex's nearest xterm-256 slot from scratch —
// the 216-colour cube (16-231) plus the 24-step grey ramp (232-255), by
// Euclidean RGB distance, first index wins a tie. Recomputing rather than
// trusting the table is the point: a hand-copied literal that's off by one
// row of the cube looks completely plausible in a diff and shows up only as
// a subtly-wrong tmux window label.
func xterm256Nearest(hex string) int {
	r, g, b := parseHexTest(hex)
	best, bestDist := 0, math.MaxFloat64
	try := func(idx, cr, cg, cb int) {
		d := float64((r-cr)*(r-cr) + (g-cg)*(g-cg) + (b-cb)*(b-cb))
		if d < bestDist {
			best, bestDist = idx, d
		}
	}
	levels := []int{0, 95, 135, 175, 215, 255}
	for ri, rv := range levels {
		for gi, gv := range levels {
			for bi, bv := range levels {
				try(16+36*ri+6*gi+bi, rv, gv, bv)
			}
		}
	}
	for i := 0; i < 24; i++ {
		v := 8 + 10*i
		try(232+i, v, v, v)
	}
	return best
}

func parseHexTest(hex string) (int, int, int) {
	n, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		panic(fmt.Sprintf("bad hex %q: %v", hex, err))
	}
	return int(n>>16) & 0xff, int(n>>8) & 0xff, int(n) & 0xff
}

func TestPaletteTerm256MatchesItsHex(t *testing.T) {
	for _, c := range Palette {
		if got := xterm256Nearest(c.Hex); got != c.Term256 {
			t.Errorf("%s: Term256 = %d, but %s's nearest xterm slot is %d", c.Name, c.Term256, c.Hex, got)
		}
	}
}

// relLuminance / contrastRatio are WCAG 2.x's own formulas, recomputed here
// so the light-theme hexes are ASSERTED accessible rather than asserted-by-
// comment. Both backgrounds matter: the light board's own panel colour and
// plain white (an artifact page, a print, a screenshot on white).
func relLuminance(hex string) float64 {
	r, g, b := parseHexTest(hex)
	lin := func(v int) float64 {
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrastRatio(a, b string) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestPaletteLightHexesClearWCAGAA(t *testing.T) {
	for _, bg := range []string{"#E3E9F1", "#FFFFFF"} {
		for _, c := range Palette {
			if ratio := contrastRatio(c.HexLight, bg); ratio < 4.5 {
				t.Errorf("%s: HexLight %s on %s is %.2f:1, want >= 4.5:1", c.Name, c.HexLight, bg, ratio)
			}
		}
	}
}

func TestColorByNameAndValidColor(t *testing.T) {
	c, ok := ColorByName("purple")
	if !ok || c.Term256 != 103 {
		t.Fatalf("ColorByName(purple) = %+v, %v", c, ok)
	}
	// empty is the absence of a colour, not a colour — both lookups must
	// refuse it, because every surface handles "" on its own path.
	for _, bad := range []string{"", "purpel", "PURPLE", "default", "#827DBD"} {
		if _, ok := ColorByName(bad); ok {
			t.Errorf("ColorByName(%q) unexpectedly matched", bad)
		}
		if ValidColor(bad) {
			t.Errorf("ValidColor(%q) = true", bad)
		}
	}
}

func TestEffectiveColor(t *testing.T) {
	if got := EffectiveColor(&Job{Slug: "x", Color: "cyan"}); got != "cyan" {
		t.Errorf("set color: got %q, want cyan", got)
	}
	// unset means NO colour — never an auto-assignment, so every surface
	// keeps rendering exactly what it rendered before colours existed.
	if got := EffectiveColor(&Job{Slug: "x"}); got != "" {
		t.Errorf("unset color: got %q, want empty", got)
	}
	if got := EffectiveColor(nil); got != "" {
		t.Errorf("nil job: got %q, want empty", got)
	}
}

// --- the tmux window label ---

func TestCellWindowColor(t *testing.T) {
	// a chosen colour wins
	if got := cellWindowColor(&Job{Slug: "yelp-dedupe", Color: "orange"}); got != 173 {
		t.Errorf("orange job: got colour %d, want 173", got)
	}
	// unset falls back to the OLD slug hash, byte-identical to before —
	// jobColor's determinism is pinned separately (TestJobColorStable), and
	// this is what keeps an un-coloured job's window looking unchanged.
	j := &Job{Slug: "yelp-dedupe"}
	if got, want := cellWindowColor(j), jobColor("yelp-dedupe"); got != want {
		t.Errorf("unset job: got %d, want jobColor's %d", got, want)
	}
	// a garbage hand-typed colour also falls back rather than erroring: the
	// job file's own parse already flagged it, and the window still needs
	// SOME colour.
	if got, want := cellWindowColor(&Job{Slug: "yelp-dedupe", Color: "purpel"}), jobColor("yelp-dedupe"); got != want {
		t.Errorf("invalid colour: got %d, want jobColor's %d", got, want)
	}
}

// --- the watermark ---

func TestColorWatermarkRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "wm-job"}
	if got := readColorWatermark(j); got != "" {
		t.Fatalf("absent watermark reads %q, want empty", got)
	}
	if err := writeColorWatermark(j, "green"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readColorWatermark(j); got != "green" {
		t.Fatalf("after write: got %q, want green", got)
	}
	// clearing is a real recorded value, not a delete
	if err := writeColorWatermark(j, ""); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if got := readColorWatermark(j); got != "" {
		t.Fatalf("after clear: got %q, want empty", got)
	}
	// it lives in the cell dir, which is what makes teardown free it and a
	// fresh cell re-deliver without StartCell knowing this file exists
	if want := cellDir(j); !strings.HasPrefix(colorWatermarkPath(j), want) {
		t.Errorf("watermark at %q, want it under the cell dir %q", colorWatermarkPath(j), want)
	}
}

// --- the reconcile decision ---

func TestColorSyncDecision(t *testing.T) {
	cases := []struct {
		name               string
		state, want, have  string
		zeusLive, cellLive bool
		wantSend           bool
		wantArg            string
	}{
		{"working, first delivery", "working", "purple", "", true, true, true, "purple"},
		{"blocked cells deliver too", "blocked", "cyan", "", true, true, true, "cyan"},
		{"changed colour", "working", "red", "blue", true, true, true, "red"},
		// never a BARE /color: Claude Code picks a colour AT RANDOM when the
		// argument is missing, so clearing sends the explicit "default"
		{"cleared colour", "working", "", "red", true, true, true, "default"},
		// never reassert: a /color typed by hand in the pane survives, because
		// kraken only ever acts on want != have
		{"already delivered", "working", "purple", "purple", true, true, false, ""},
		{"never had one", "working", "", "", true, true, false, ""},
		{"draft", "", "purple", "", true, true, false, ""},
		{"queued", "queued", "purple", "", true, true, false, ""},
		{"done", "done", "purple", "", true, true, false, ""},
		// dead cell / still-starting zeus are both silent no-ops that retry
		// on the next tick, never errors
		{"dead cell", "working", "purple", "", true, false, false, ""},
		{"no zeus peer yet", "working", "purple", "", false, true, false, ""},
	}
	for _, tc := range cases {
		send, arg := colorSyncDecision(tc.state, tc.want, tc.have, tc.zeusLive, tc.cellLive)
		if send != tc.wantSend {
			t.Errorf("%s: send = %v, want %v", tc.name, send, tc.wantSend)
			continue
		}
		if send && arg != tc.wantArg {
			t.Errorf("%s: arg = %q, want %q", tc.name, arg, tc.wantArg)
		}
	}
}

// TestColorSyncSendsAnArgument pins the one thing a bare "/color" would
// break twice over: Claude Code would pick a random colour, and
// core.guardBareSlashCommand would treat the message as an unfinished
// command.
func TestColorSyncSendsAnArgument(t *testing.T) {
	for _, want := range []string{"", "red", "cyan"} {
		send, arg := colorSyncDecision("working", want, "sentinel", true, true)
		if !send {
			t.Fatalf("want %q: expected a send", want)
		}
		if arg == "" || strings.ContainsAny(arg, " \t") {
			t.Errorf("want %q: arg %q must be a single non-empty token", want, arg)
		}
	}
}

// TestColorSyncRefusesOffPaletteWant is the belt to the endpoint's
// allowlist. Both cases below reached the old code as clean parses:
// whitespace is not "" so it skipped the clear arm and would have sent
// `/color ` with an EMPTY argument (Claude Code then picks AT RANDOM — the
// one outcome the design forbids), and a comment-bearing value survives
// ParseJob's stripComment and would have been typed verbatim.
func TestColorSyncRefusesOffPaletteWant(t *testing.T) {
	for _, want := range []string{" ", "\t", "red # note", "purpel", "default", "red blue", "red; rm -rf /"} {
		if send, arg := colorSyncDecision("working", want, "", true, true); send {
			t.Errorf("want %q: would have sent %q, expected a refusal", want, "/color "+arg)
		}
	}
}

// --- syncCellColor's own gates and record-keeping ---

// TestSyncCellColorGatesBeforeTmux drives the real entry point: every arm
// exercised here returns before CellZeus/SendToPeer, so it needs no tmux.
func TestSyncCellColorGatesBeforeTmux(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "gated", State: "working", Color: "red"}
	// dead cell: silent no-op, retried next tick, nothing recorded
	sent, arg, err := syncCellColor(j, nil, map[string]bool{})
	if sent || arg != "" || err != nil {
		t.Fatalf("dead cell: sent=%v arg=%q err=%v", sent, arg, err)
	}
	// wrong state, live cell
	j.State = "queued"
	if sent, _, err = syncCellColor(j, nil, map[string]bool{CellSession(j): true}); sent || err != nil {
		t.Fatalf("queued: sent=%v err=%v", sent, err)
	}
	// already delivered: the watermark alone stops it, no peer walk needed
	j.State = "working"
	colorSyncMu.Lock()
	recordColorDelivery(j, "red")
	colorSyncMu.Unlock()
	if sent, _, err = syncCellColor(j, nil, map[string]bool{CellSession(j): true}); sent || err != nil {
		t.Fatalf("already delivered: sent=%v err=%v", sent, err)
	}
	if got := readColorWatermark(j); got != "red" {
		t.Fatalf("watermark = %q, want red", got)
	}
}

// TestRecordColorDeliverySurvivesAnUnwritableCellDir covers the failure
// path that used to retype /color into the pane every 15s forever: the
// watermark file can't be written, so the delivery is remembered in memory
// instead and the next reconcile still reads want == have.
func TestRecordColorDeliverySurvivesAnUnwritableCellDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "nodisk", State: "working", Color: "blue"}

	// a regular FILE where the cell dir belongs: MkdirAll refuses it, which
	// is the cheapest honest stand-in for a disk that won't take the write
	if err := os.MkdirAll(cellsRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellDir(j), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	colorSyncMu.Lock()
	defer colorSyncMu.Unlock()
	// package-level state: never leak between tests
	defer func() {
		delete(colorFallback, j.Slug)
		delete(colorFallbackLogged, j.Slug)
	}()

	recordColorDelivery(j, "blue")
	if got := readColorWatermark(j); got != "" {
		t.Fatalf("watermark file unexpectedly readable: %q", got)
	}
	// the delivery is still remembered, so the gate sees want == have and
	// the pane is not retyped
	if got := deliveredColor(j); got != "blue" {
		t.Fatalf("deliveredColor = %q, want blue from the fallback", got)
	}
	if send, _ := colorSyncDecision(j.State, EffectiveColor(j), deliveredColor(j), true, true); send {
		t.Error("a failed watermark write must not drive a re-send")
	}
}

// TestFallbackWinsOverAStaleWatermarkFile is the shape the first fallback
// fix missed. util.AtomicWrite renames a temp file into place, so a REFUSED
// write leaves the previous watermark intact and non-empty — that, not an
// absent file, is the normal look of a failed write. A reader that
// consulted the file first would see the OLD colour, decide want != have,
// and retype the same /color every tick forever.
func TestFallbackWinsOverAStaleWatermarkFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "stale-wm", State: "working", Color: "purple"}

	colorSyncMu.Lock()
	defer colorSyncMu.Unlock()
	defer func() {
		delete(colorFallback, j.Slug)
		delete(colorFallbackLogged, j.Slug)
	}()

	// an earlier delivery that DID record cleanly
	recordColorDelivery(j, "red")
	if got := readColorWatermark(j); got != "red" {
		t.Fatalf("setup: watermark = %q, want red", got)
	}

	// now make writes fail while leaving that file exactly where it is —
	// the cell dir goes read-only, so AtomicWrite's temp file can't be
	// created and "red" survives on disk
	if err := os.Chmod(cellDir(j), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cellDir(j), 0o755) })

	recordColorDelivery(j, "purple")
	if got := readColorWatermark(j); got != "red" {
		t.Fatalf("precondition: watermark should still read the stale red, got %q", got)
	}
	if got, ok := colorFallback[j.Slug]; !ok || got != "purple" {
		t.Fatalf("fallback = %q (present=%v), want purple", got, ok)
	}

	// the whole point: the delivery is not repeated
	if got := deliveredColor(j); got != "purple" {
		t.Fatalf("deliveredColor = %q, want purple — the fallback must beat the stale file", got)
	}
	if send, arg := colorSyncDecision(j.State, EffectiveColor(j), deliveredColor(j), true, true); send {
		t.Errorf("would retype %q against a stale watermark", "/color "+arg)
	}
}

// TestWatermarkFailureLogsOnce pins the dedup: the retry-forever state
// writes to a log on the very disk that just refused a write, so it must
// not add a line per tick.
func TestWatermarkFailureLogsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "noisy"}

	colorSyncMu.Lock()
	defer colorSyncMu.Unlock()
	defer func() {
		delete(colorFallback, j.Slug)
		delete(colorFallbackLogged, j.Slug)
	}()

	if err := os.MkdirAll(cellsRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellDir(j), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		recordColorDelivery(j, "green")
	}
	if !colorFallbackLogged[j.Slug] {
		t.Fatal("first failure should have logged")
	}
	// a recovered disk re-arms the log, so a LATER failure is still visible
	if err := os.Remove(cellDir(j)); err != nil {
		t.Fatal(err)
	}
	recordColorDelivery(j, "green")
	if colorFallbackLogged[j.Slug] {
		t.Error("a successful write must clear the logged flag")
	}
	if _, ok := colorFallback[j.Slug]; ok {
		t.Error("a successful write must clear the fallback")
	}
}

// TestTeardownForgetsTheColorFallback is the teardown half of the
// watermark's lifetime. The FILE half expires for free (teardownCell
// os.RemoveAll's the cell dir it lives in), which is what makes a fresh cell
// re-deliver. The in-memory half is package state that no RemoveAll can
// reach, and it is NOT self-clearing — nothing retries a failed write once
// want == have — so without forgetColorDelivery the job's next cell reads
// its colour as already delivered and its brand-new zeus never gets a
// /color at all, while the board shows the job coloured.
func TestTeardownForgetsTheColorFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "torn-down", State: "working", Color: "purple"}
	t.Cleanup(func() { forgetColorDelivery(j) })

	// a delivery whose watermark write was refused (cell dir is a file)
	if err := os.MkdirAll(cellsRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellDir(j), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	colorSyncMu.Lock()
	recordColorDelivery(j, "purple")
	got := deliveredColor(j)
	colorSyncMu.Unlock()
	if got != "purple" {
		t.Fatalf("setup: deliveredColor = %q, want purple", got)
	}

	// teardown's own two steps, in its order
	os.RemoveAll(cellDir(j))
	forgetColorDelivery(j)

	colorSyncMu.Lock()
	got = deliveredColor(j)
	colorSyncMu.Unlock()
	if got != "" {
		t.Fatalf("after teardown deliveredColor = %q, want empty — the record must not outlive the cell", got)
	}
	// so the job's next cell actually gets its /color
	if send, arg := colorSyncDecision(j.State, EffectiveColor(j), got, true, true); !send || arg != "purple" {
		t.Errorf("a fresh cell must re-deliver: send=%v arg=%q", send, arg)
	}
}

// TestRecordColorDeliveryClearsFallbackOnSuccess keeps the fallback from
// outliving the failure it stands in for — otherwise a job that once failed
// to record would consult stale memory forever.
func TestRecordColorDeliveryClearsFallbackOnSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "recovered"}

	colorSyncMu.Lock()
	defer colorSyncMu.Unlock()
	defer func() {
		delete(colorFallback, j.Slug)
		delete(colorFallbackLogged, j.Slug)
	}()

	colorFallback[j.Slug] = "stale"
	recordColorDelivery(j, "yellow")
	if _, ok := colorFallback[j.Slug]; ok {
		t.Error("fallback survived a successful watermark write")
	}
	if got := deliveredColor(j); got != "yellow" {
		t.Fatalf("deliveredColor = %q, want yellow", got)
	}
}
