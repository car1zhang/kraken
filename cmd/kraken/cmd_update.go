package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_update.go — `kraken update`, the verb that owns the whole self-update
// sequence: preflight, fetch, fast-forward, build, install, restart. It is
// the ONE implementation of that sequence; the board's UPDATE button does
// nothing but spawn this binary with `update --detached` (internal/api/
// update.go), so a click and a terminal invocation are the same code path
// producing the same status record, the same event-log trail, and the same
// refusal sentences.
//
// Its relationship to the daemon is unusual and shapes everything below: the
// process this command restarts is `kraken serve` — the daemon that spawned
// it in the detached case. So the updater must be correct with no store, no
// serve, and no listener alive: every phase transition goes to
// ~/.kraken/update.json (a file, not a store row — see jobs.UpdateStatus) and
// to util.LogEvent, both of which work fine against a dead serve.
//
// parseUpdateArgs/runUpdate are split out of cmdUpdate so both are callable
// from tests without going through util.Die's os.Exit — the same shape
// cmd_import.go uses. Every subprocess this file runs goes through a
// package-level seam (bottom of the file) so the test suite never shells out.

type updateOpts struct {
	detached bool
	force    bool
	dryRun   bool
	test     bool
	help     bool
}

// parseUpdateArgs recognizes only the five documented flags; anything else is
// a usage error rather than a silently-ignored typo (a mistyped --dry-run
// that fell through to a real update would rebuild and restart the daemon).
func parseUpdateArgs(args []string) (updateOpts, error) {
	var o updateOpts
	for _, a := range args {
		switch a {
		case "--detached":
			o.detached = true
		case "--force":
			o.force = true
		case "--dry-run":
			o.dryRun = true
		case "--test":
			o.test = true
		case "--help", "-h":
			o.help = true
		default:
			return updateOpts{}, fmt.Errorf("unknown argument: %s", a)
		}
	}
	return o, nil
}

const updateUsageText = `usage: k update [--force] [--dry-run] [--test] [--detached]
  pull origin/main into the kraken checkout, rebuild, reinstall, restart the
  launchd job, then reinstall Kraken.app (an already-current runtime still
  catches the mac app up when a prior run missed it). Refuses on a dirty tree
  or a non-main branch; never stashes, never merges anything but a
  fast-forward.

  --force      full rebuild + reinstall + restart even when already up to date
  --dry-run    preflight + fetch and report only; changes nothing
  --test       run 'go test ./...' after the build, before installing
  --detached   spawned by the board's UPDATE button: inherit the run record
               the API already opened instead of starting a new one`

func cmdUpdate(args []string) {
	opts, err := parseUpdateArgs(args)
	if err != nil {
		util.Die("%v\n%s", err, updateUsageText)
	}
	if opts.help {
		fmt.Println(updateUsageText)
		return
	}
	if err := runUpdate(opts, os.Stdout); err != nil {
		// runUpdate has already recorded phase=failed where that was the
		// right thing to do; Die's job here is only the message + exit code.
		util.Die("%v", err)
	}
}

// runUpdate is cmdUpdate's exit-free core: it returns an error instead of
// calling util.Die, and writes every phase it passes through to
// ~/.kraken/update.json on the way.
//
// out receives the human progress lines. In --detached mode stdout is the
// update log the API pointed the child at (jobs.UpdateLogPath), so the same
// lines still get written — they just have no terminal to be interactive on.
func runUpdate(opts updateOpts, out io.Writer) error {
	st := jobs.UpdateStatus{StartedAt: time.Now().UnixMilli(), PID: os.Getpid()}
	// MacAppSHA survives from run to run — it is the record of the LAST
	// successful mac-app install, not of this run — so it is read before this
	// run's first write can clobber it. (The API's own claim-write preserves
	// it for the detached case; this covers the CLI-started run too.)
	prior := jobs.ReadUpdateStatus()
	st.MacAppSHA = prior.MacAppSHA
	if opts.detached {
		// The API wrote phase=preflight the instant the button was clicked —
		// that write is its mutex — and only then spawned us. Inherit its
		// StartedAt so ElapsedMS measures the run the user actually watched,
		// not the part of it that happened after fork+exec.
		if prior.StartedAt != 0 {
			st.StartedAt = prior.StartedAt
		}
	}

	// setPhase is the only writer of the run record. It no-ops under
	// --dry-run: a dry run reports what an update would do and must leave the
	// board's update state exactly as it found it — including a failed or
	// done record from a previous real run.
	setPhase := func(phase string) {
		st.Phase = phase
		if opts.dryRun {
			return
		}
		if err := updateWriteStatus(st); err != nil {
			fmt.Fprintf(out, "warning: could not write update status: %v\n", err)
		}
	}
	// fail records a failure that happened while we owned the run, then
	// returns the error verbatim — the same sentence lands in update.json,
	// the event log, and the process' stderr.
	fail := func(err error) error {
		st.Error = err.Error()
		setPhase(jobs.UpdateFailed)
		util.LogEvent("update", "error", "update-error", err.Error())
		return err
	}

	util.LogEvent("update", "info", "update-start", "kraken update starting")

	pf, err := updatePreflight()
	if err != nil {
		// Preflight runs BEFORE the lock, so at this point another updater
		// may legitimately own the run record. Only claim it when nothing is
		// in flight (or when we are the detached child, in which case the
		// non-terminal record on disk is the one the API opened for us) —
		// otherwise a `kraken update` typed on a non-main branch would stomp
		// a healthy concurrent build's phase with our own refusal.
		if opts.detached || jobs.UpdateTerminal(jobs.ReadUpdateStatus().Phase) {
			return fail(err)
		}
		util.LogEvent("update", "error", "update-error", err.Error())
		return err
	}

	// Stamped before the lock, not after it, so that a run which never gets
	// past contention still records the head it was standing on. It is the
	// one field of the record preflight already knows, and a failed record
	// with an empty from_sha says nothing about which build failed.
	st.FromSHA = pf.HeadSHA

	// The lock is held for the WHOLE run, not just the git steps: the thing
	// being serialized is `make install`'s mv onto ~/.local/bin/kraken and
	// the launchctl kickstart that follows it, so releasing it after the
	// merge would let a second updater start building against a tree the
	// first one is still installing from. flock, so a killed updater's lock
	// dies with it (jobs.AcquireUpdateLock).
	release, ok, err := updateAcquireLock()
	if err != nil {
		return fail(fmt.Errorf("acquire update lock: %w", err))
	}
	if !ok {
		// The holder's pid, when the lock file will give it up, is the entire
		// remedy for the case this message is most likely to appear in: a run
		// wedged past updateMaxRun (the codesign auth dialog nobody can see)
		// keeps its flock while the board, reading the status file, has
		// already downgraded it to failed and re-enabled the button. Every
		// click from then on lands here — so without the pid we would answer
		// a user who has just been told which process is stalled by
		// overwriting that record with a sentence naming nobody, and do it
		// again on every retry. No pid available is not worth mentioning:
		// the message simply loses its parenthetical.
		contention := errors.New("update already in flight")
		if pid := jobs.UpdateLockHolderPID(); pid > 0 {
			contention = fmt.Errorf("update already in flight (pid %d)", pid)
		}
		if opts.detached {
			// Detached, the record on disk is the one the API opened FOR US —
			// phase=preflight, pid 0 — and if we say nothing, nothing else
			// ever advances it: the board spins for staleUpdate's 60s grace
			// and then reports "updater never started", which is false and
			// discards the accurate reason. The holder can't be relied on to
			// write over it either — `k update --dry-run` takes this same
			// lock and deliberately writes no phases at all. Worst case a
			// real holder's next transition overwrites our note a moment
			// later, which is the correct outcome anyway.
			return fail(contention)
		}
		// Interactive, nothing is ours to record: the run record belongs to
		// whoever holds the lock, and stomping it would replace a healthy
		// build's phase with our refusal.
		return contention
	}
	defer release()

	setPhase(jobs.UpdateFetching)
	util.LogEvent("update", "info", "update-fetch", "fetching origin/main in "+pf.Repo)
	if _, err := updateGit(pf.Repo, "fetch", "origin", "main"); err != nil {
		// The one failure the updater rescues itself from: git authenticates
		// this fetch as whichever gh account is ACTIVE, and with the wrong one
		// GitHub answers "Repository not found" for a repo it can't see.
		// internal/jobs/ghauth.go owns that whole decision and the reasons it
		// is a narrow one.
		//
		// Never under --dry-run. That flag promises to change nothing, and the
		// machine's active gh account is the most global thing this command
		// can touch — every other consumer on the box reads it (other claude
		// sessions, the daemon's own `gh pr list`). A dry run that silently
		// re-signed the machine in would be the one mutation nobody could have
		// expected from it.
		if opts.dryRun {
			fmt.Fprintln(out, "dry run: fetch failed — a real update would try a gh account switch here; nothing was changed")
			return fail(err)
		}
		account, ghErr := updateGHRecover(pf.Repo, err.Error())
		if ghErr != nil {
			// Nothing was switched, so the fetch's own error is the true one —
			// ghErr only explains why the rescue did not apply, and reporting
			// it as the failure would replace the real cause with a footnote.
			util.LogEvent("update", "info", "update-gh-skip", "not switching gh accounts: "+ghErr.Error())
			return fail(err)
		}
		util.LogEvent("update", "info", "update-gh-switch", "fetch failed as the wrong gh account; switched to "+account+" and retrying")
		fmt.Fprintf(out, "fetch failed; switched gh to %s (origin's owner) and retrying\n", account)
		// Exactly one retry, and only after an actual switch: the credential
		// changed, so this is a genuinely different attempt rather than a
		// hopeful repeat of the same one.
		if _, retryErr := updateGit(pf.Repo, "fetch", "origin", "main"); retryErr != nil {
			// Named rather than shadowing err, so the account the retry ran as
			// is attached to the RETRY's failure and not to the first one.
			return fail(fmt.Errorf("%w (retried as gh account %s)", retryErr, account))
		}
	}

	behindOut, err := updateGit(pf.Repo, "rev-list", "--count", "HEAD..origin/main")
	if err != nil {
		return fail(err)
	}
	behind, convErr := strconv.Atoi(strings.TrimSpace(behindOut))
	if convErr != nil {
		return fail(fmt.Errorf("unexpected rev-list output %q", strings.TrimSpace(behindOut)))
	}
	st.Behind = behind
	// A missing remote sha is decoration, not a reason to abort a good
	// update — same treatment the daemon's availability check gives it.
	st.ToSHA, _ = updateGit(pf.Repo, "rev-parse", "--short", "origin/main")

	// Every build tool runs under this environment, never the inherited one:
	// the Makefile calls a BARE `go`, a BARE `npm` and a BARE `xcodegen`, and
	// the PATH this process inherits from launchd leads to none of them.
	// util.PrependToolDir carries the whole reasoning, including why widening
	// the plist's PATH is not enough; a cell spawn shares it for the identical
	// reason (internal/jobs/cell.go's cellSpawnEnv).
	//
	// Applied per tool rather than made variadic: ToolchainEnv is shared with
	// the cell spawner, and PrependToolDir already no-ops when a dir is empty
	// or already leads PATH — so tools sharing one homebrew prefix prepend it
	// once, not once each, and an unresolved xcodegen's empty path is inert
	// (the mac-app phase is what fails then, not the env plumbing).
	env := util.ToolchainEnv(os.Environ(), pf.GoPath)
	env = util.ToolchainEnv(env, pf.NpmPath)
	env = util.ToolchainEnv(env, pf.XcodegenPath)

	// installMacApp is the `mac app` phase: `make install-mac` — rebuild
	// Kraken.app, swap it into /Applications. It runs LAST wherever it runs —
	// after the restart on a full update, alone on a catch-up — because the
	// runtime is the critical path and must never be hostage to Xcode: by the
	// time this phase can fail, the runtime work of the run is already done,
	// which is what `prefix` states to the user. A success records the sha so
	// later runs know the app is current (see UpdateStatus.MacAppSHA); either
	// failure names the retry, which the MacAppSHA catch-up makes truthful —
	// a plain UPDATE click really does retry just this step.
	//
	// A machine with no Kraken.app installed never opted into the mac app,
	// so the phase is skipped rather than run: building one there would put
	// an app in /Applications nobody asked for, and failing on its missing
	// xcodegen would mark every update failed. The sha is still stamped so
	// the catch-up path below does not re-enter the phase on every click.
	// macSkipped records the skip for the run's closing sentence.
	macSkipped := false
	installMacApp := func(atSHA, prefix string) error {
		setPhase(jobs.UpdateMacApp)
		if !macAppInstalled(macAppPath) {
			util.LogEvent("update", "info", "update-mac-skipped", macAppPath+" is not installed — mac app build skipped")
			st.MacAppSHA = atSHA
			macSkipped = true
			return nil
		}
		if pf.XcodegenPath == "" {
			return fail(fmt.Errorf("%sthe mac app was NOT rebuilt: xcodegen not found (brew install xcodegen, then click UPDATE again — it retries just the app build)", prefix))
		}
		util.LogEvent("update", "info", "update-mac", "rebuilding Kraken.app into /Applications")
		if err := updateExec(pf.Repo, env, pf.MakePath, "install-mac"); err != nil {
			return fail(fmt.Errorf("%sthe mac app install failed: %w (fix the build, then click UPDATE again — it retries just the app build)", prefix, err))
		}
		st.MacAppSHA = atSHA
		return nil
	}

	if opts.dryRun {
		fmt.Fprintf(out, "repo:   %s (main @ %s)\n", pf.Repo, pf.HeadSHA)
		fmt.Fprintf(out, "remote: origin/main @ %s\n", st.ToSHA)
		fmt.Fprintf(out, "behind: %d commit(s)\n", behind)
		if behind == 0 {
			fmt.Fprintln(out, "dry run: already up to date — nothing would be built")
		} else {
			fmt.Fprintln(out, "dry run: would fast-forward, build, install, restart, and reinstall Kraken.app (nothing changed)")
		}
		// The dry run is the one place a missing xcodegen can surface before
		// a real run half-completes on it — and only matters when the app is
		// installed, since otherwise a real run skips the mac-app phase.
		if pf.XcodegenPath == "" && macAppInstalled(macAppPath) {
			fmt.Fprintln(out, "dry run: xcodegen not found — the mac-app step of a real run would fail (brew install xcodegen)")
		}
		return nil
	}

	if behind == 0 && !opts.force {
		// An up-to-date RUNTIME can still owe a mac-app build: the mac phase
		// is the only step after the merge, so its failure leaves HEAD at
		// origin/main and a plain retry lands here. MacAppSHA is what breaks
		// that trap — when the app was last installed at some other commit
		// (or never), fall through to the mac phase alone instead of
		// answering "up to date" about a binary while the app stays a build
		// behind with no board-side remedy.
		if st.MacAppSHA == pf.HeadSHA {
			setPhase(jobs.UpdateUpToDate)
			util.LogEvent("update", "info", "update-done", "already up to date at "+pf.HeadSHA)
			fmt.Fprintf(out, "already up to date (main @ %s) — nothing built, nothing restarted\n", pf.HeadSHA)
			return nil
		}
		fmt.Fprintf(out, "runtime already up to date (main @ %s); catching the mac app up\n", pf.HeadSHA)
		if err := installMacApp(pf.HeadSHA, "runtime already current; "); err != nil {
			return err
		}
		setPhase(jobs.UpdateDone)
		if macSkipped {
			util.LogEvent("update", "info", "update-done", "already up to date at "+pf.HeadSHA+"; mac app not installed, skipped")
			fmt.Fprintf(out, "already up to date (main @ %s); Kraken.app is not installed, so the mac app was skipped\n", pf.HeadSHA)
			return nil
		}
		util.LogEvent("update", "info", "update-done", "mac app caught up at "+pf.HeadSHA)
		fmt.Fprintf(out, "Kraken.app reinstalled at %s (runtime was already current)\n", pf.HeadSHA)
		return nil
	}

	// Only reachable with commits to take. Skipping the merge entirely when
	// behind == 0 matters for --force: a checkout that is merely AHEAD of
	// origin/main (unpushed local commits) has nothing to fast-forward and is
	// not diverged, so it must not be refused by the ancestor check below.
	if behind > 0 {
		// A fast-forward is only a fast-forward if HEAD is an ancestor of the
		// remote. `merge --ff-only` would refuse a diverged tree on its own,
		// but with a generic message; checking first lets the refusal say
		// what actually happened and what to do about it. Divergence needs a
		// human — an unattended updater has no business picking between
		// rebasing, merging, and discarding local commits.
		if _, err := updateGit(pf.Repo, "merge-base", "--is-ancestor", "HEAD", "origin/main"); err != nil {
			// Exit 1 is git's answer "no, not an ancestor". ANY other
			// non-zero — 128 for a corrupt object, an unreadable ref, a
			// missing origin/main — means git couldn't answer at all, and
			// reporting that as divergence would send someone off to
			// reconcile branches that are perfectly in line. Surface git's
			// own text instead.
			var ge *gitExitError
			if errors.As(err, &ge) && ge.code == 1 {
				return fail(errors.New("local main has diverged from origin/main — resolve by hand"))
			}
			return fail(fmt.Errorf("could not check for a fast-forward: %w", err))
		}
		setPhase(jobs.UpdateMerging)
		util.LogEvent("update", "info", "update-merge", fmt.Sprintf("fast-forwarding %d commit(s) to %s", behind, st.ToSHA))
		// --ff-only, never `git pull`: pull would happily write a merge
		// commit into main when the fast-forward doesn't apply, and an
		// updater running with nobody watching must refuse instead. There is
		// also no stash anywhere in this file — the dirty-tree check in
		// PreflightUpdate is a hard refusal precisely so this code never has
		// to decide what someone else's uncommitted work was for.
		if _, err := updateGit(pf.Repo, "merge", "--ff-only", "origin/main"); err != nil {
			return fail(err)
		}
	}

	// `make build` on its own first, even though `make install` depends on it:
	// it gives --test the exact slot Q3 asked for (after the build, before
	// anything is installed) and gives the UI's BUILDING… a phase that really
	// is the compile. The duplicate work is free — go's build cache makes
	// install's own build step a no-op a second later.
	setPhase(jobs.UpdateBuilding)
	util.LogEvent("update", "info", "update-build", "building in "+pf.Repo)
	if err := updateExec(pf.Repo, env, pf.MakePath, "build"); err != nil {
		return fail(fmt.Errorf("build failed: %w (nothing was installed; the running kraken is untouched)", err))
	}
	if opts.test {
		// After the build, before the install (Q3): a red suite must abort
		// while ~/.local/bin/kraken is still the last known-good binary. The
		// fts5 tag is not optional — without it every store-opening test dies
		// at migration (`no such module: fts5`, CLAUDE.md's Commands section),
		// so an untagged --test could never pass.
		fmt.Fprintln(out, "running go test -tags sqlite_fts5 ./…")
		if err := updateExec(pf.Repo, env, pf.GoPath, "test", "-tags", "sqlite_fts5", "./..."); err != nil {
			return fail(fmt.Errorf("tests failed: %w (nothing was installed; the running kraken is untouched)", err))
		}
	}

	// `make install` rather than an inlined build/sign/mv/kickstart: the
	// Makefile is the single source of truth for what installing kraken
	// means, so a board click and a hand-typed `make install` stay the same
	// operation forever. It is also the only place that knows the hazards —
	// signing before the mv, mv instead of cp so live processes keep their
	// inode, kickstart -k instead of unload+load. The cost is that the
	// installing and restarting phases both happen INSIDE this one call: we
	// mark installing before it and restarting after it returns, which is
	// exactly when the restart the UI cares about is in progress (kickstart
	// is the recipe's last step, so serve is down and rebinding at that
	// moment).
	setPhase(jobs.UpdateInstalling)
	if err := updateExec(pf.Repo, env, pf.MakePath, "install"); err != nil {
		return fail(fmt.Errorf("make install failed: %w", err))
	}

	// Restarted rides in the record from here on: the binary is replaced and
	// kickstarted, so a later failure (the mac phase) must still read as
	// "reload owed" to the SPA and "behind is stale" to the API.
	st.Restarted = true
	setPhase(jobs.UpdateRestarting)
	util.LogEvent("update", "info", "update-restart", "launchd job kickstarted onto the new binary")
	// Brief settle before announcing done: the board reloads itself the
	// moment it sees `done` (web/static is embedded in the binary, so the
	// open SPA belongs to the old build), and reloading into the gap between
	// kickstart killing serve and the new one binding just shows a
	// connection error.
	updateSleep(updateRestartSettle)

	// The Zed mental-model language server, swept once the runtime is
	// replaced. A running `kraken mm lsp` is Zed's child still holding the
	// OLD binary's inode — `make install` renames rather than rewrites
	// precisely so live processes survive — and Zed never restarts a server
	// that keeps running, so without this sweep every update leaves the
	// editor's read marks served by the previous build until carl remembers
	// `editor: restart language server`. Zed DOES demand-start a fresh
	// server from PATH the next time a buffer of a registered language
	// opens (measured 2026-08-21: a killed server stayed down while the
	// editor idled, and the next file open respawned it within a second),
	// so killing the stale one is the whole job. The extension's wasm needs
	// nothing here: it is a static shim whose body is "run `kraken mm lsp`",
	// rebuilt only when the crate itself changes — and only Zed can build
	// it (its extension builder does the WASM component encoding).
	sweepZedMMLSP(out)

	// The mac app, last (installMacApp's definition carries the why). The
	// running Kraken.app is deliberately not killed or relaunched — the
	// recipe renames the live bundle aside and swaps the new one in, never
	// rewriting it in place, so nothing is SIGKILLed; the SPA inside it
	// reloads itself when the record turns terminal-after-restart, and the
	// shell BINARY only matters on the rare shell-code change — relaunch
	// when one lands.
	// The recorded sha is HEAD re-read after the merge, not st.ToSHA:
	// origin/main's sha was read tolerantly (it may be ""), and a --force run
	// on a checkout AHEAD of origin/main builds from a HEAD that origin/main's
	// sha does not name. An empty re-read degrades to one spurious catch-up
	// on the next click, which self-heals.
	installedAt, _ := updateGit(pf.Repo, "rev-parse", "--short", "HEAD")
	if err := installMacApp(installedAt, fmt.Sprintf("runtime updated %s → %s and restarted, but ", pf.HeadSHA, st.ToSHA)); err != nil {
		return err
	}

	setPhase(jobs.UpdateDone)
	util.LogEvent("update", "info", "update-done", fmt.Sprintf("updated %s → %s", pf.HeadSHA, st.ToSHA))
	macNote := "Kraken.app reinstalled"
	if macSkipped {
		macNote = "Kraken.app not installed, mac app skipped"
	}
	fmt.Fprintf(out, "updated %s → %s (%d commit(s)); launchd job restarted on the new binary; %s\n", pf.HeadSHA, st.ToSHA, behind, macNote)
	return nil
}

// macAppPath is where `make install-mac` installs the board app (the
// Makefile's install-mac recipe swaps the bundle into exactly this path).
const macAppPath = "/Applications/Kraken.app"

// macAppInstalled reports whether path stats as a directory — an app bundle
// is one. The update's mac-app phase runs only when it does.
func macAppInstalled(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// updateRestartSettle is how long the updater waits after `make install`
// returns before declaring done. Long enough for launchd to have the new
// `kraken serve` listening, short enough that nobody watching the button
// notices it.
const updateRestartSettle = 2 * time.Second

// sweepZedMMLSP kills any running `kraken mm lsp` process so that Zed's next
// buffer open demand-starts a fresh server on the just-installed binary (the
// call site in runUpdate carries the full why). Best-effort in every
// direction — no pgrep, no match, a pid gone by the time it is signalled —
// because a missed sweep costs one manual `editor: restart language server`
// and a failed sweep must never fail an update whose runtime work is done.
//
// pgrep -f matches the phrase anywhere in any argv — a `grep kraken mm lsp`
// in flight matches too, and pids recycle — so each candidate is re-read
// from ps and killed only when its argv is exactly the server's: a first
// token whose basename is `kraken`, followed by exactly `mm lsp`.
func sweepZedMMLSP(out io.Writer) {
	pids, err := exec.Command("pgrep", "-f", "kraken mm lsp").Output()
	if err != nil {
		return // exit 1 is "no match"; anything else is equally not worth an update's attention
	}
	for _, pid := range strings.Fields(string(pids)) {
		argv, err := exec.Command("ps", "-o", "command=", "-p", pid).Output()
		if err != nil {
			continue
		}
		f := strings.Fields(strings.TrimSpace(string(argv)))
		if len(f) != 3 || f[0][strings.LastIndex(f[0], "/")+1:] != "kraken" || f[1] != "mm" || f[2] != "lsp" {
			continue
		}
		if exec.Command("kill", pid).Run() == nil {
			fmt.Fprintf(out, "swept stale zed mm language server (pid %s); zed starts a fresh one on the next file open\n", pid)
		}
	}
}

// gitExitError carries git's exit STATUS alongside the message, because one
// caller has to tell git's answers apart from git's failures: `merge-base
// --is-ancestor` says "no" with exit 1 and "I couldn't tell you" with 128,
// and both are just a non-nil error otherwise. Nothing else inspects code —
// it stays -1 for a git that never ran at all (binary missing, dir gone),
// which reads as "not a plain answer" everywhere it matters.
type gitExitError struct {
	code int
	msg  string
}

func (e *gitExitError) Error() string { return e.msg }

// --- swappable seams: every subprocess, the lock, and the clock, so the
// tests below drive the whole sequence without shelling out, touching a real
// repo, or restarting anything. Same package-var pattern jobs/update.go and
// api/account.go use. ---
var (
	// updateGit runs one git command in dir and returns its trimmed output.
	// CombinedOutput, and the output folded into the error, because git's
	// most useful failure text (merge refusals, missing remotes) is on
	// stderr and would otherwise be lost inside a bare "exit status 1".
	updateGit = func(dir string, args ...string) (string, error) {
		full := append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			ge := &gitExitError{code: -1, msg: fmt.Sprintf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))}
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				ge.code = ee.ExitCode()
			}
			return "", ge
		}
		return strings.TrimSpace(string(out)), nil
	}

	// updateExec runs a build tool (make, go) in dir under env. name is always
	// an absolute path resolved by jobs.PreflightUpdate — never a bare command
	// name — because this process may have been spawned by launchd, whose
	// PATH is not a login shell's; env (util.ToolchainEnv) closes the same gap for
	// the bare `go` the Makefile itself invokes.
	updateExec = func(dir string, env []string, name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		// Streamed, not captured: `make install` takes tens of seconds and a
		// human watching it deserves to see it move. Detached, these pipes
		// are the update log the API opened, which is the only surviving
		// record of what the build did.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
		}
		return nil
	}

	updatePreflight   = jobs.PreflightUpdate
	updateAcquireLock = jobs.AcquireUpdateLock
	updateWriteStatus = jobs.WriteUpdateStatus
	updateGHRecover   = jobs.RecoverGHAccount
	updateSleep       = time.Sleep
)
