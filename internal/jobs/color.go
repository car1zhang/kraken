package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"kraken/internal/core"
	"kraken/internal/util"
)

// JobColor is one entry of the fixed palette below: a colour NAME Claude
// Code's /color accepts, plus the three renderings the surfaces need — a
// dark-theme hex (web/iOS dark), a light-theme hex (web/iOS light), and an
// xterm-256 slot (the TUI and tmux, neither of which speaks hex).
type JobColor struct {
	Name     string
	Hex      string
	HexLight string
	Term256  int
}

// Palette is the eight colours Claude Code's /color accepts, in /color's own
// order. It is fixed, not extensible: kraken types "/color <Name>" into
// zeus' pane, so a name that isn't on this list is a name the claude will
// reject. ("default" is /color's clear verb rather than a colour, so it is
// deliberately absent — an unset job colour is the empty string, and
// syncCellColor translates that to "default" only at the moment of sending.)
//
// The Term256 slots are the nearest neighbours of Hex in the xterm 216-colour
// cube + grey ramp (TestPaletteTerm256MatchesItsHex recomputes them, so a
// hand-typo here fails the build rather than shipping a subtly-off pane
// label). The same sixteen hexes are spelled out in web/static/style.css's
// two --job-* blocks and in ios/Kraken/Theme.swift; TestJobPaletteMatchesCSS
// and TestJobPaletteMatchesSwift (package web) are what keep all three in
// step. HexLight is the
// darkened variant that clears 4.5:1 WCAG contrast against both the light
// board background (#E3E9F1) and plain white.
var Palette = []JobColor{
	{Name: "red", Hex: "#DC2626", HexLight: "#CA2323", Term256: 160},
	{Name: "blue", Hex: "#6A9BCC", HexLight: "#496B8D", Term256: 68},
	{Name: "green", Hex: "#16A34A", HexLight: "#107937", Term256: 35},
	{Name: "yellow", Hex: "#CA8A04", HexLight: "#8B5F03", Term256: 172},
	{Name: "purple", Hex: "#827DBD", HexLight: "#676395", Term256: 103},
	{Name: "orange", Hex: "#D97757", HexLight: "#9C563F", Term256: 173},
	{Name: "pink", Hex: "#C46686", HexLight: "#9B516A", Term256: 168},
	{Name: "cyan", Hex: "#0891B2", HexLight: "#06718B", Term256: 31},
}

// ColorByName looks a palette entry up by its /color name. The bool is
// false for "" and for anything not on the list.
func ColorByName(name string) (JobColor, bool) {
	for _, c := range Palette {
		if c.Name == name {
			return c, true
		}
	}
	return JobColor{}, false
}

// ValidColor reports whether name is one of the eight. Empty is NOT valid —
// it is the absence of a colour, which every caller handles separately.
func ValidColor(name string) bool {
	_, ok := ColorByName(name)
	return ok
}

// EffectiveColor is the colour a job should render as. Empty means no
// colour at all — never a default to fall back on, so every surface must
// treat "" as "render exactly what you rendered before colours existed".
func EffectiveColor(j *Job) string {
	if j == nil {
		return ""
	}
	return j.Color
}

// colorWatermarkPath is where kraken records the colour it last typed into a
// cell's zeus. It lives in the cell dir beside zeus-brief.md, which is what
// gives it the right lifetime almost for free: teardown deletes the cell
// dir, so a job restarted into a fresh cell re-delivers its colour without
// StartCell needing to know this file exists. "Almost" because the record
// has a second, in-memory half (colorFallback) that no os.RemoveAll can
// reach — teardownCell pairs this deletion with forgetColorDelivery.
//
// existingCellDir, so a renamed cell keeps ONE watermark: written under
// cellDir it would land in a directory teardown never removes (teardown
// removes the dir the cell actually lives in), and the record would outlive
// the cell it describes — exactly the lifetime this file is placed here to
// get right.
func colorWatermarkPath(j *Job) string {
	return filepath.Join(existingCellDir(j), "color.applied")
}

// readColorWatermark returns the colour last delivered to this cell's zeus.
// A missing or unreadable file reads as "" — which is also a legitimate
// recorded value (the cleared state), and the conflation is harmless: both
// mean "the next non-empty want is worth sending".
func readColorWatermark(j *Job) string {
	data, err := os.ReadFile(colorWatermarkPath(j))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeColorWatermark records what was just delivered. Only ever called
// after a successful send, so a failed send leaves the old value in place
// and the next tick retries — the same "state only advances on a confirmed
// write" discipline watchPRs' watermarks follow.
func writeColorWatermark(j *Job, name string) error {
	path := colorWatermarkPath(j)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return util.AtomicWrite(path, name)
}

// colorSyncDecision is syncCellColor's gate chain as a pure function over
// the five facts it consults, so every arm is table-testable without tmux,
// a cell dir, or a live claude. sendArg is meaningful only when send is
// true; it is "default" for the cleared case, since a BARE /color makes
// Claude Code pick a colour AT RANDOM.
func colorSyncDecision(state, want, have string, zeusLive, cellLive bool) (send bool, sendArg string) {
	if state != "working" && state != "blocked" {
		return false, ""
	}
	if !cellLive {
		return false, ""
	}
	if want == have {
		return false, ""
	}
	// Belt to the endpoint's allowlist: whatever route a value took to get
	// here, it is about to be TYPED INTO A LIVE CLAUDE, so the last thing
	// that touches it re-checks it. Two values reach this point looking
	// harmless and are not: a whitespace-only colour is not "" (so the clear
	// arm below is skipped) and would send `/color ` with an EMPTY argument,
	// which is exactly the make-Claude-pick-at-random case the design
	// forbids; and a comment-bearing value like "red # note" survives
	// ParseJob's stripComment and would be typed verbatim.
	if want != "" && !ValidColor(want) {
		return false, ""
	}
	if !zeusLive {
		return false, ""
	}
	if want == "" {
		return true, "default"
	}
	return true, want
}

// colorSyncMu serialises the whole read-watermark → send → write-watermark
// sequence. Two writers really do exist in one process: the daemon's
// per-tick pass and PushJobColor's goroutine off the recolour endpoint.
// Interleaved without this, the pane can end up on one colour while the
// watermark records the other, after which every later tick reads
// want == have and sends nothing — the pane stays wrong FOREVER, with no
// path back short of a hand-typed /color. One package-level mutex rather
// than a per-slug map because colour sends are rare (a human clicking a
// swatch) and the critical section is one tmux send.
var colorSyncMu sync.Mutex

// colorFallback stands in for a watermark file that could not be WRITTEN,
// keyed by slug, guarded by colorSyncMu. Without it a disk that refuses the
// write turns a single delivery into an unbounded retype: the send keeps
// succeeding, the record keeps failing, and zeus' pane collects a /color
// every tick forever.
//
// An entry is NOT self-clearing, and it is worth being precise about why:
// the only thing that drops one is a successful recordColorDelivery, which
// only runs after a send, which only happens on want != have — and
// deliveredColor returns this very entry AS have. So once written for an
// unchanged colour the loop is closed against itself and no retry is ever
// attempted, however quickly the disk recovers. That is fine for the pane
// (it already has the right colour) but it means the entry's lifetime must
// be bounded from OUTSIDE, by the same event that bounds the watermark
// file's: forgetColorDelivery, called from teardownCell beside the
// os.RemoveAll of the cell dir. Without that the entry outlives the cell
// (it is package memory, not a file) and the job's NEXT cell — same slug,
// same colour, brand-new zeus — reads want == have and never gets its
// /color at all, while the board cheerfully shows the job coloured.
var colorFallback = map[string]string{}

// forgetColorDelivery drops a job's in-memory delivery record. It exists
// only so teardownCell can end the fallback's life at exactly the moment it
// deletes the cell dir the watermark file lives in — the two halves of one
// record must expire together, or a fresh cell inherits half of it. Takes
// colorSyncMu itself, so callers must NOT hold it.
func forgetColorDelivery(j *Job) {
	colorSyncMu.Lock()
	defer colorSyncMu.Unlock()
	delete(colorFallback, j.Slug)
	delete(colorFallbackLogged, j.Slug)
}

// colorFallbackLogged remembers which slugs have already had their
// watermark-write failure logged, so the retry-forever case that populates
// colorFallback doesn't also write one error line PER TICK — to a log that
// lives on the very disk that just refused a write. Same shape as the
// daemon's colorfail:/bumpfail: dedup keys, kept here because this failure
// never reaches the daemon as an error. Cleared on the next successful
// write, so a recovered disk logs again if it fails again.
var colorFallbackLogged = map[string]bool{}

// deliveredColor is the read half of the watermark. A colorFallback entry
// wins outright over the file, comma-ok rather than empty-check: its very
// presence means the file is STALE, and stale-but-non-empty is the NORMAL
// shape of a failed write — util.AtomicWrite renames a temp file into
// place, so a refused write leaves the previous contents intact. Reading
// the file first would therefore see the old colour, decide want != have,
// and retype the same /color every tick forever — the exact runaway the
// fallback exists to stop, re-entered through a different door.
// Caller must hold colorSyncMu.
func deliveredColor(j *Job) string {
	if have, ok := colorFallback[j.Slug]; ok {
		return have
	}
	return readColorWatermark(j)
}

// recordColorDelivery is the write half, run only after a /color has
// actually reached the pane. It cannot fail meaningfully: the keystroke is
// already gone, so a refused write is logged and mirrored into
// colorFallback rather than reported, because reporting it would both
// suppress the caller's "delivered" event for a delivery that DID happen
// and — via the untouched watermark — retype the same /color every tick
// forever. Caller must hold colorSyncMu.
func recordColorDelivery(j *Job, want string) {
	if err := writeColorWatermark(j, want); err != nil {
		colorFallback[j.Slug] = want
		if !colorFallbackLogged[j.Slug] {
			dlogErr("%s: color watermark write failed (delivery stands, not retried): %v", j.Slug, err)
			colorFallbackLogged[j.Slug] = true
		}
		return
	}
	delete(colorFallback, j.Slug)
	delete(colorFallbackLogged, j.Slug)
}

// syncCellColor delivers the job's colour into its live zeus pane when it
// differs from what was last delivered. Returns (sent, arg, err): sent is
// true only when a /color actually went out and arg is the exact token that
// followed it, so the caller logs the keystroke that happened rather than
// re-deriving it (and drifting from it).
//
// Modelled beat for beat on watchPRs (daemon.go): the same working/blocked +
// live-session gate, the same "no zeus peer yet → silently retry next tick"
// (a still-starting claude has no name), the same send-then-watermark order.
// Like watchPRs it does NOT check zeus' status — there is no idle gate, a
// busy pane gets typed into, exactly as a PR bump does. And it never
// reasserts: kraken acts only on a want ≠ have difference, so a /color typed
// by hand in the pane survives untouched.
//
// Delivery targets zeus ONLY. Side threads and teammate panes are never
// touched — Claude Code refuses /color inside a teammate session outright,
// so fanning out would be a no-op with a confusing pane message.
func syncCellColor(j *Job, all []*core.Peer, live map[string]bool) (bool, string, error) {
	colorSyncMu.Lock()
	defer colorSyncMu.Unlock()

	want := EffectiveColor(j)
	have := deliveredColor(j)
	cellLive := live[CellSession(j)]
	// Probed with zeusLive=true first so the cheap gates (state, liveness,
	// want == have) reject the overwhelmingly common no-op tick before
	// CellZeus walks every peer — the same reason watchPRs defers its own
	// zeus lookup until it knows it has something to say.
	if send, _ := colorSyncDecision(j.State, want, have, true, cellLive); !send {
		return false, "", nil
	}
	zeus := CellZeus(j, all)
	send, arg := colorSyncDecision(j.State, want, have, zeus != nil, cellLive)
	if !send {
		return false, "", nil
	}
	// "/color <arg>" carries an argument, so core.guardBareSlashCommand
	// leaves it alone. The queued bool is discarded exactly as watchPRs
	// discards it — a queued message still arrives.
	if _, err := core.SendToPeer(zeus, "/color "+arg); err != nil {
		return false, "", err // watermark untouched: next tick retries
	}
	// The keystroke is gone; everything below records it and can only fail
	// non-fatally.
	recordColorDelivery(j, want)
	// The tmux window label is set from cellWindowColor at spawn only, so
	// without this a recoloured live cell would show the OLD label beside
	// the new prompt bar for the rest of the run.
	setCellWindowColor(j)
	return true, arg, nil
}

// PushJobColor runs one job's colour reconcile now instead of waiting for
// the next daemon tick — the recolour endpoint's kick, so a swatch click
// lands in the pane immediately. Fire-and-forget: errors go to the log,
// never to the caller, and it runs its own discovery in a goroutine so an
// HTTP handler never blocks on the `ps`/`tmux` sweep DiscoverAll costs.
func PushJobColor(slug string) {
	go func() {
		j := LoadJobBySlug(slug)
		if j == nil {
			return
		}
		all, _ := core.OrderAll(core.DiscoverAll())
		if _, _, err := syncCellColor(j, all, LiveSessions()); err != nil {
			dlogErr("%s: color push failed: %v", slug, err)
		}
	}()
}
