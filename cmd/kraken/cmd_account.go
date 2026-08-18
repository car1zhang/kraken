package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmdAccount is `k account`'s dispatcher: no args shows rotation mode + the
// usage/account table (status), "auto" releases the manual pin, anything
// else is treated as a ~/.claude-pool slot slug for a manual switch.
// "--help"/"-h"/"help" print usage and exit 0 rather than being treated as a
// (nonexistent) slot slug.
//
// What the two modes MEAN changed with per-cell binding: in auto, kraken
// picks a pool account for each cell it spawns and never moves the machine's
// main login on its own; manual pins the account carl chose and stops kraken
// taking any account decision at all, so every cell then runs on main. This
// command and POST /api/account/switch are the only things left anywhere
// that swap main.
func cmdAccount(args []string) {
	if len(args) == 0 {
		cmdAccountStatus()
		return
	}
	if len(args) != 1 {
		util.Die("usage: k account [auto|<slot-slug>]")
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Println("usage: k account [auto|<slot-slug>]")
		fmt.Println("  k account            show rotation mode + account table")
		fmt.Println("  k account auto       release the manual pin (kraken binds each new cell to a pool account; main is never moved)")
		fmt.Println("  k account <slug>     manually switch main to a ~/.claude-pool slot slug (pins manual)")
		return
	}
	// EqualFold, not ==: a slot slug can't collide with "auto" case-
	// insensitively anyway (pool slugs are lowercase-slugified emails), so
	// matching "AUTO"/"Auto" as the keyword rather than a literal slug typo
	// is the more forgiving read.
	if strings.EqualFold(args[0], "auto") {
		cmdAccountAuto()
		return
	}
	cmdAccountSwitch(args[0])
}

// cmdAccountStatus prints the rotation mode line followed by a table of
// every known Claude account (main + every pool slot), mirroring the data
// the per-cell binder itself decides against.
func cmdAccountStatus() {
	printRotationLine()
	snap, ok := jobs.ReadUsageSnapshot()
	if !ok {
		fmt.Println("no usage data yet (daemon hasn't run a usage pass) — try again shortly")
		return
	}
	printAccountTable(snap)
}

func printRotationLine() {
	st := jobs.ReadRotationState()
	if st.Mode != "manual" {
		fmt.Println("rotation: auto")
		return
	}
	by := st.SetBy
	if by == "" {
		by = "unknown"
	}
	// "just now" also covers SetAt == 0 (never stamped) and a SetAt in the
	// future (clock skew/rollback) — util.Age has no floor at zero, so a
	// negative duration would otherwise print as e.g. "-5s ago".
	ago := "just now"
	if st.SetAt > 0 {
		if d := time.Since(time.UnixMilli(st.SetAt)); d > 0 {
			ago = util.Age(d) + " ago"
		}
	}
	fmt.Printf("rotation: manual (set by %s %s)\n", by, ago)
}

func printAccountTable(snap core.UsageSnapshot) {
	color := util.UseColor()
	now := time.Now()
	// mainEmail feeds accountFlags' "mirrors main" flag below — the same
	// save-back-slot identity check core.ensureSaveBackSlot itself makes
	// when picking where to stash the outgoing main creds during a rotation.
	var mainEmail string
	for _, a := range snap.Accounts {
		if a.Slug == "main" {
			mainEmail = a.Email
			break
		}
	}
	rows := [][]string{{"SLUG", "EMAIL", "5H%", "7D%", "FABLE%", "FLAGS"}}
	for i, h := range rows[0] {
		rows[0][i] = util.Paint(color, util.Cyan, h)
	}
	for _, a := range snap.Accounts {
		slug := a.Slug
		if a.Current {
			slug = util.Paint(color, util.Green, slug+" (current)")
		}
		email := a.Email
		if email == "" {
			email = util.Paint(color, util.Dim, "-")
		}
		rows = append(rows, []string{
			slug,
			email,
			windowPct(color, a.Session),
			windowPct(color, a.Weekly),
			windowPct(color, fableWindow(a.Models)),
			accountFlags(color, a, now, mainEmail),
		})
	}
	for _, line := range util.FormatTableLines(rows) {
		fmt.Println(line)
	}
}

// windowPct renders a usage window's percent, "-" (dimmed) when unknown.
func windowPct(color bool, w *core.UsageWindow) string {
	if w == nil {
		return util.Paint(color, util.Dim, "-")
	}
	return fmt.Sprintf("%.0f%%", w.Percent)
}

// fableWindow finds the "Fable" weekly_scoped model window, case-insensitive
// — core.findModel does the same lookup for rotation decisions but is
// unexported, so this is its display-only counterpart.
func fableWindow(models []core.ModelWindow) *core.UsageWindow {
	for i := range models {
		if strings.EqualFold(models[i].Name, "Fable") {
			return &core.UsageWindow{Percent: models[i].Percent, ResetsAt: models[i].ResetsAt}
		}
	}
	return nil
}

// accountFlags renders the same disqualifying/advisory conditions
// core.SelectRotationTarget's eligibility check considers, so `k account`'s
// table explains at a glance why a slot would (or wouldn't) be picked as a
// rotation target. "disabled"/"disabled?" is derived straight from the
// slot's own marker file (not carried on AccountUsage) since a disabled slot
// otherwise reads identically to a bare "no creds" row. mainEmail (the
// current main row's own email, "" if unknown) flags a slot whose identity
// mirrors main's — core.ensureSaveBackSlot's own rotation target for the
// outgoing creds — since that row would otherwise look like an ordinary
// eligible pool slot despite core refusing to ever rotate onto it (see
// core.ErrSaveBackIsTarget).
func accountFlags(color bool, a core.AccountUsage, now time.Time, mainEmail string) string {
	var flags []string
	if a.Reauth {
		flags = append(flags, "reauth")
	}
	var disabledFlag string
	if a.Slug != "main" {
		disabledFlag = slotDisabledState(a.Slug)
	}
	switch {
	case disabledFlag != "":
		flags = append(flags, disabledFlag)
	case a.NoCreds:
		flags = append(flags, "no creds")
	}
	if a.CooldownUntil > now.UnixMilli() {
		flags = append(flags, "cooling")
	}
	if a.Stale {
		flags = append(flags, "stale")
	}
	if a.Slug != "main" && mainEmail != "" && a.Email != "" && strings.EqualFold(a.Email, mainEmail) {
		flags = append(flags, "mirrors main")
	}
	if len(flags) == 0 {
		return util.Paint(color, util.Dim, "-")
	}
	return util.Paint(color, util.Yellow, strings.Join(flags, ","))
}

// slotDisabledState reports a pool slot's .disabled marker: "disabled" on a
// confirmed marker, "" when confirmed absent, "disabled?" when the check
// itself failed (permission denied, unreadable pool dir, ...) — fail-closed
// like core's own .disabled checks (see SwapMainTo), rather than silently
// reading an ambiguous stat error as "no marker, therefore eligible".
func slotDisabledState(slug string) string {
	_, err := os.Stat(filepath.Join(core.PoolDir(), slug, ".disabled"))
	switch {
	case err == nil:
		return "disabled"
	case os.IsNotExist(err):
		return ""
	default:
		return "disabled?"
	}
}

// cmdAccountAuto releases the manual pin: no swap, and main stays exactly
// where this command leaves it. What it re-enables is per-cell BINDING —
// kraken picking a pool account for each new cell — not any movement of the
// machine's own login, and the printed line says so because it is the one
// sentence carl reads after running this.
func cmdAccountAuto() {
	if err := jobs.SetRotationMode("auto", "cli"); err != nil {
		util.Die("set rotation mode: %v", err)
	}
	util.LogEvent("cli", "info", "account", "k account auto")
	fmt.Println("rotation: auto — kraken binds each new cell to its own pool account; main is never moved automatically")
}

// cmdAccountSwitch is a manual main-login swap: it always executes
// regardless of usage thresholds, and pins rotation to manual on success.
// Ordering is load-bearing: the swap runs BEFORE the mode write, so a failed
// swap never touches the mode. The swap+pin pair runs under
// jobs.AcquireRotationLock so a manual switch can never race a concurrent
// web/CLI switch across processes — without it, the other one could commit a
// swap mid-manual-switch and this command would then stamp the (now stale)
// target slug's identity onto whatever slot that swap just landed main on.
// There is no daemon-side swapper left to race: per-cell binding retired the
// only one there was.
func cmdAccountSwitch(slug string) {
	release, ok, err := jobs.AcquireRotationLock()
	if err != nil {
		util.Die("acquire rotation lock: %v", err)
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "another rotation is in flight — try again")
		os.Exit(1)
	}
	// released guards against double-releasing the lock: the swap+pin below
	// is the actual critical section, and it's released explicitly right
	// after (see below) so the usage refresh — pure read-only network I/O —
	// never runs under it. The defer stays as a safety net for any path that
	// returns before that explicit release (there is none today, but this
	// keeps every future early-return path covered without re-deriving the
	// idempotence guard).
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			release()
		}
	}
	defer releaseOnce()

	from, to, err := core.SwapMainTo(slug)
	if err != nil {
		// Printed verbatim (no "kraken: " prefix, unlike util.Die) since
		// core.SwapMainTo's errors are already written for a human to read
		// as-is (they carry their own remediation, e.g. "run ccp login").
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := jobs.SetRotationMode("manual", "cli"); err != nil {
		// The swap already committed — main really did move to `to`. Don't
		// exit non-zero over a pin that failed to persist; just say so.
		fmt.Fprintf(os.Stderr, "warning: main switched to %s, but the manual pin did not persist: %v\n", to, err)
	}
	util.LogEvent("cli", "info", "account", fmt.Sprintf("k account %s: %s -> %s", slug, from, to))
	// Printed BEFORE the usage refresh below, not after: a Ctrl-C during that
	// refresh must not hide that the swap already committed.
	fmt.Printf("main: %s → %s (rotation pinned manual — re-enable with k account auto)\n", from, to)

	// Release the cross-process rotation lock now — the swap+pin critical
	// section is done. The usage refresh is pure read-only I/O; running it
	// under the lock would needlessly block the other manual switch surface
	// (POST /api/account/switch) for however long the refresh takes.
	releaseOnce()

	// Synchronous, not jobs.KickUsagePass(): that fires a background
	// goroutine, and this CLI process exits right after printing its result
	// — the goroutine is killed before it ever writes usage.json, so the
	// board never actually reflects the new main until the next daemon poll.
	fmt.Println("refreshing usage…")
	if err := jobs.UsagePass(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: usage refresh failed: %v\n", err)
	}
}
