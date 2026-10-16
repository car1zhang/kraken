package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"kraken/internal/api"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
	"kraken/web"
)

// defaultServePort is the port `kraken serve` binds by default — the same
// one the real, live deployment listens on. --mirror (local mirror-testing
// mode, see serveSetup/RunServeTicks' ServeOpts.Mirror) refuses to squat it:
// a mirror process is meant to run ALONGSIDE the live one, never in place
// of it.
const defaultServePort = "7777"

// k serve — the sqlite-migration merged process (spec: "Component: kraken
// serve"). Opens kraken.db, wires the jobs package onto it (jobs.UseStore),
// runs the scheduler tick loop as a background goroutine instead of the
// standalone `k daemon` process, runs the nightly backup rotation (Q15) as
// a second background goroutine, and serves the web UI + API in the
// foreground — the same web.Serve `k web` already uses, now running
// store-backed. Replaces `k daemon` + `k web` for real deployment; wiring
// launchd to run this instead of the two separate processes is
// make install's concern, out of this slice.
func cmdServe(args []string) {
	util.InitSelfCheck()
	port := defaultServePort
	tailnet := false
	explicitAddr := ""
	mirror := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--port":
			i++
			if i >= len(args) {
				util.Die("-p needs a port")
			}
			port = args[i]
		case "--tailnet":
			tailnet = true
		case "-addr":
			i++
			if i >= len(args) {
				util.Die("-addr needs a host:port")
			}
			explicitAddr = args[i]
		case "--mirror":
			mirror = true
		default:
			util.Die("unknown argument: %s (usage: k serve [-p port] [--tailnet] [-addr host:port] [--mirror])", args[i])
		}
	}

	// api.SetMirrorMode gates the board mutation endpoints that drive real
	// tmux/processes (kill, stop, steer, question, retry, start, queue,
	// done, reactivate — see api/actions.go) so they 403 instead of ever
	// touching the live deployment's real cells. Set unconditionally
	// (mirror is normally false) so a repeated in-process call — tests —
	// always resets it rather than inheriting a previous test's leftover.
	api.SetMirrorMode(mirror)

	// AlsoTailnet: a real serve binds localhost AND (whenever tailscale is
	// or comes up) the tailnet IP in one process — `make install` needs no
	// flags for the iOS app to reach the board. Mirror stays strictly
	// localhost: a parity sandbox has no business being tailnet-visible.
	opts := web.Options{Port: port, Tailnet: tailnet, ExplicitAddr: explicitAddr,
		DisableSignalHandler: true, AlsoTailnet: !mirror}
	ln, st, addr, err := serveSetup(opts, mirror)
	if err != nil {
		util.Die("%v", err)
	}
	// E7: record the address we actually bound (may differ from a naive
	// default guess — --tailnet, or "-p 0" letting the OS pick a port) so
	// every cell hook installed from here on routes its pr-hook POST at
	// the right place instead of assuming defaultServeAddr.
	jobs.SetHookServeAddr(addr)

	go jobs.RunServeTicks(jobs.ServeOpts{Mirror: mirror})
	if !mirror {
		// nightly backups back up the real kraken.db path this process has
		// wired (st) — under --mirror that's the sandbox DB the runbook
		// points KRAKEN_DB_PATH at, and mirror testing has no business
		// scheduling backups of a throwaway sandbox at all.
		go runNightlyBackups(st)
	}
	util.LogEvent("serve", "info", "serve-start", "kraken serve up, store "+store.DBPath())

	// shutdown: stop the tick loop and close the store before exiting. This
	// is the ONLY SIGTERM/SIGINT handler in this process — DisableSignalHandler
	// below stops web.Serve from ever registering its own, so there is no
	// race for which os.Exit(0) call wins.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigc
		util.LogEvent("serve", "info", "serve-stop", "kraken serve stopping")
		jobs.StopServeTicks()
		st.Close()
		os.Exit(0)
	}()

	// DisableSignalHandler: this process already registered its own (sole)
	// SIGTERM/SIGINT handler above (stop ticks → close store → exit) — web.
	// Serve must not also register its own simpler log-and-exit handler,
	// since two competing handlers really would race for which os.Exit(0)
	// wins.
	opts.Listener = ln
	if err := web.Serve(opts); err != nil {
		util.Die("%v", err)
	}
}

// serveSetup (E5) does everything that must succeed BEFORE kraken serve
// starts anything with a side effect other process/host state can observe —
// the scheduler tick goroutine (which writes daemon.json on its very first
// tick) and the nightly-backup goroutine. In order: resolve+validate the
// port (mirror's squat check, below), bind the listener (net.Listen, at the
// exact address web.Serve would otherwise bind lazily deep inside
// ListenAndServe), THEN open the store, THEN check the single-instance
// guard. An occupied port fails at the very first step, so none of the
// store/guard/goroutine machinery ever starts — the previous ordering
// called web.Serve (which binds internally) dead last, so a port conflict
// was only ever discovered after the tick loop, backup loop, and
// daemon.json write had already happened, leaving a half-up process that
// thought it owned the scheduler but never served a single request. Split
// out from cmdServe so a port conflict is testable without also driving the
// blocking HTTP serve loop.
//
// mirror is cmdServe's --mirror flag (jobs.ServeOpts.Mirror's own
// precondition): it requires the resolved address to bind a port different
// from defaultServePort — refusing before ANY of net.Listen/store.Open/the
// guard run, so a `--mirror` invocation that forgot -p/-addr never
// squats the real deployment's port even for an instant — and skips
// GuardServeStart entirely, since mirror is designed to run ALONGSIDE a
// live daemon/serve process (a separate sandbox DB, a separate port), never
// to contend with it for the single-instance slot.
//
// Also returns the listener's ACTUAL bound address (ln.Addr().String(),
// not the pre-bind ResolveAddr guess — "-p 0" lets the OS pick a port, so
// the two can differ) for cmdServe to hand to jobs.SetHookServeAddr (E7).
func serveSetup(opts web.Options, mirror bool) (ln net.Listener, st *store.Store, addr string, err error) {
	addr, err = web.ResolveAddr(opts)
	if err != nil {
		return nil, nil, "", err
	}
	if mirror {
		if _, resolvedPort, splitErr := net.SplitHostPort(addr); splitErr == nil && resolvedPort == defaultServePort {
			return nil, nil, "", fmt.Errorf(
				"--mirror requires -p/-addr on a port different from %s (the real kraken serve's default) — e.g. -p 7778",
				defaultServePort,
			)
		}
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, "", fmt.Errorf("listen %s: %w", addr, err)
	}
	addr = ln.Addr().String()
	st, err = store.Open(store.DBPath())
	if err != nil {
		ln.Close()
		return nil, nil, "", fmt.Errorf("open store: %w", err)
	}
	jobs.UseStore(st)

	if !mirror {
		// startup guard (findings 1+2): checked synchronously, BEFORE the tick
		// loop starts, so a standalone `kraken daemon` (or a second `kraken
		// serve`) already running makes this process exit non-zero right here
		// rather than come up part-way — RunServeTicks performs the same check
		// again internally as a backstop, but by then it's a goroutine and
		// can't undo the listener bind or daemon.json write. Skipped under
		// --mirror: see the doc comment above.
		if err := jobs.GuardServeStart(); err != nil {
			jobs.UseStore(nil)
			st.Close()
			ln.Close()
			return nil, nil, "", err
		}
	}
	return ln, st, addr, nil
}

// --- nightly backups (spec Q15) ---

const backupsKeep = 7

// backupsDir is where kraken serve's nightly backups land, overridable for
// tests.
func backupsDir() string {
	if d := os.Getenv("KRAKEN_BACKUPS_DIR"); d != "" {
		return d
	}
	return filepath.Join(util.Home(), ".kraken", "backups")
}

// runNightlyBackups runs one backupNow roughly every 24h, checked hourly
// (rather than requiring a specific wall-clock hour) so a long-running
// serve process backs up on a steady cadence regardless of when it
// started; a failed attempt is logged and retried on the next hourly
// check rather than waiting a full day. Never returns; intended as a
// goroutine alongside RunServeTicks.
func runNightlyBackups(st *store.Store) {
	// Seed `last` from the newest existing backup's mtime rather
	// than starting blank, so a serve restart shortly after a real backup
	// doesn't immediately churn out another one just because the in-memory
	// `last` forgot it happened.
	last := latestBackupTime(backupsDir())
	for {
		now := time.Now()
		if now.Sub(last) >= 24*time.Hour {
			if err := backupNow(st, backupsDir(), now); err != nil {
				fmt.Fprintf(os.Stderr, "kraken: nightly backup failed: %v\n", err)
				util.LogEvent("serve", "error", "backup-error", "nightly backup failed: "+err.Error())
			} else {
				last = now
				util.LogEvent("serve", "info", "backup", "nightly backup written to "+backupsDir())
			}
		}
		time.Sleep(time.Hour)
	}
}

// latestBackupTime returns the mtime of the newest kraken-*.db file in dir,
// or the zero time if none exist yet (fresh install, or dir missing) —
// seeds runNightlyBackups' `last` so a restart doesn't forget a backup
// already sitting on disk.
func latestBackupTime(dir string) time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "kraken-") || !strings.HasSuffix(e.Name(), ".db") {
			continue // ".db.tmp" partials fail the ".db" suffix check and are skipped here too
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

// backupNow writes one VACUUM INTO backup (via store.Backup) to dir,
// timestamped so backups sort chronologically by name, then rotates dir
// down to the newest backupsKeep. Written to a ".tmp" name first and
// renamed into place only once VACUUM INTO fully succeeds, so
// a backup killed mid-write (process killed, disk full) never leaves a
// truncated file wearing a real "kraken-*.db" name that rotateBackups or a
// restore would treat as good. A same-second collision on the final name
// (two backups landing within the same wall-clock second — an artifact of
// hourly-checked-but-24h-gated scheduling more than a real failure) is
// skipped and logged rather than surfaced as an error, since
// VACUUM INTO refuses to overwrite an existing destination.
func backupNow(st *store.Store, dir string, now time.Time) (err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	dest := filepath.Join(dir, "kraken-"+now.Format("20060102-150405")+".db")
	if _, err := os.Stat(dest); err == nil {
		util.LogEvent("serve", "info", "backup-skip", "backup skipped: "+dest+" already exists (same-second collision)")
		return nil
	}
	tmp := dest + ".tmp"
	_ = os.Remove(tmp) // best-effort: clear a stale .tmp left by an interrupted attempt at this exact timestamp
	// E9: any error from here on must not leak tmp on disk. Before this,
	// a backup killed mid-write (disk full, process killed between
	// st.Backup and os.Rename) left a partial .tmp sitting there until the
	// NEXT backupNow call's best-effort Remove above happened to clear
	// it — up to ~24h later on the nightly schedule. Named return + defer
	// so every error path below (not just one of them) is covered; `err`
	// here is the function's actual return value regardless of any
	// shadowed local `err` a `return err`/`return fmt.Errorf(...)`
	// statement below assigns through.
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if err := st.Backup(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename backup %s to %s: %w", tmp, dest, err)
	}
	return rotateBackups(dir, backupsKeep)
}

// rotateBackups keeps only the newest keep backup files in dir (by name,
// which sorts chronologically given backupNow's timestamp format),
// deleting the rest. A dir that doesn't exist yet is not an error — there
// is simply nothing to rotate. ".tmp" partials never match the
// ".db" suffix check below, so an in-progress backup is never counted
// toward — or rotated away as part of — the keep window.
func rotateBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "kraken-") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}
