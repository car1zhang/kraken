package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// cmd_import.go — `kraken import`, the sqlite migration's temporary one-shot
// cutover verb (spec: "Component: one-shot importer"). It sweeps every
// current file store into kraken.db via jobs.ImportAll and exits. This is a
// migration tool, not a day-to-day CLI verb: kept out of usage() the same
// way pr-hook/logevent are, and deleted along with the legacy parser and
// jobs.ImportAll itself once cutover is verified (spec's Retirements table,
// final phase).
//
// parseImportArgs/runImport are split out from cmdImport so both can be
// exercised by tests without going through util.Die's os.Exit — cmdImport
// itself stays a thin, untested wrapper (same shape as every other cmd_*.go
// entry point in this codebase).
func cmdImport(args []string) {
	dryRun, force, reimport, liveSources, err := parseImportArgs(args)
	if err != nil {
		util.Die("%v (usage: kraken import [--dry-run] [--force] [--reimport] [--live-sources])", err)
	}

	report, err := runImport(dryRun, force, reimport, liveSources)
	// E12: only print the report once it means something — a guard refusal
	// (daemon/serve alive, or a prior import already completed without
	// --reimport) never touches a source or the store, so the report coming
	// back is the zero value; printing "0 found, 0 imported" for every
	// source alongside the refusal reads as a second, contradictory status
	// line instead of the one that actually explains what happened.
	if !errors.Is(err, errGuardRefused) {
		fmt.Println(report.String())
	}
	if err != nil {
		util.Die("%v", err)
	}
}

// parseImportArgs recognizes only --dry-run, --force, --reimport, and
// --live-sources; anything else is a usage error.
func parseImportArgs(args []string) (dryRun, force, reimport, liveSources bool, err error) {
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--force":
			force = true
		case "--reimport":
			reimport = true
		case "--live-sources":
			liveSources = true
		default:
			return false, false, false, false, fmt.Errorf("unknown argument: %s", a)
		}
	}
	return dryRun, force, reimport, liveSources, nil
}

// runImport is cmdImport's testable core: refuse while a daemon/serve is
// alive (concurrent writes against the same file stores this importer is
// reading — the exact mess the sqlite migration exists to kill), then open
// the store (creating ~/.kraken if needed) and hand off to jobs.ImportAll.
// The guard runs before the store is even opened, so a refusal never
// touches kraken.db at all.
//
// E3: a dry run opens a throwaway temp-dir database instead of
// store.DBPath(). store.Open alone — independent of whether ImportAll ever
// writes a row — creates ~/.kraken, touches kraken.db, and applies the full
// schema migration (os.MkdirAll + sql.Open + the migrate pass), so opening
// the REAL path for a "just show me the counts" dry run left kraken.db
// sitting on disk afterward even though DryRun never inserts anything into
// it. Routing dry-run opens at a temp path instead means
// store.DBPath() is guaranteed to not exist after `kraken import
// --dry-run` on a fresh machine.
//
// errGuardRefused marks a runImport failure that happened before any
// source or the store was ever touched (GuardServeStart, or
// jobs.ImportAll's own pre-flight refusals) — cmdImport uses it (E12) to
// decide whether printing the (zero-value, in that case) report would add
// anything.
var errGuardRefused = errors.New("import guard refused")

// defaultDBPath is store.DBPath()'s own unconditional default
// (~/.kraken/kraken.db), recomputed independent of whatever KRAKEN_DB_PATH
// currently resolves to — isSandboxDBPath compares the two to tell "a real
// override is set" apart from "no override, or one that happens to name
// the same real path".
func defaultDBPath() string {
	return filepath.Join(util.Home(), ".kraken", "kraken.db")
}

// isSandboxDBPath reports whether store.DBPath() (KRAKEN_DB_PATH-aware)
// currently resolves somewhere other than the real deployment's default —
// --live-sources' precondition for skipping the daemon-alive guard.
func isSandboxDBPath() bool {
	return store.DBPath() != defaultDBPath()
}

// runImport is cmdImport's testable core. liveSources is --live-sources
// (spec: "local mirror testing"): it lets a sandboxed import read straight
// from the REAL, currently-live pluto/uploads/logs/cells sources — the
// daemon-alive guard below exists precisely because a concurrent
// daemon/serve mutating those same sources while this importer reads them
// is a real race, so --live-sources may ONLY skip it when the destination
// database is unambiguously a sandbox (KRAKEN_DB_PATH resolves somewhere
// other than the real ~/.kraken/kraken.db) — never against the real DB
// path, where skipping the guard would be the exact double-write hazard the
// guard exists to prevent. Even in a legitimate sandbox, the imported
// snapshot can be seconds-stale (the live daemon may be mid-write to a
// source file the instant this reads it) — fine for parity/UI testing,
// never as a substitute for the real one-shot cutover import.
func runImport(dryRun, force, reimport, liveSources bool) (jobs.ImportReport, error) {
	if liveSources {
		if !isSandboxDBPath() {
			return jobs.ImportReport{}, fmt.Errorf("%w: --live-sources requires a sandbox KRAKEN_DB_PATH (resolved db path %s matches the real deployment's default — set KRAKEN_DB_PATH to a throwaway path first)", errGuardRefused, store.DBPath())
		}
	} else if err := jobs.GuardServeStart(); err != nil {
		return jobs.ImportReport{}, fmt.Errorf("refusing to import while a daemon/serve is running: %w: %v", errGuardRefused, err)
	}

	dbPath := store.DBPath()
	if dryRun {
		tmpDir, err := os.MkdirTemp("", "kraken-import-dry-run-*")
		if err != nil {
			return jobs.ImportReport{}, fmt.Errorf("dry-run temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)
		dbPath = filepath.Join(tmpDir, "kraken.db")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return jobs.ImportReport{}, fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	report, err := jobs.ImportAll(st, jobs.ImportOptions{DryRun: dryRun, Force: force, Reimport: reimport})
	if err != nil && reflect.DeepEqual(report, jobs.ImportReport{}) {
		// ImportAll's own pre-flight refusals (non-empty jobs table without
		// Force, or a prior import without Reimport) return a zero report
		// the exact same way GuardServeStart's refusal above does — before
		// touching any source — so they get the same errGuardRefused
		// treatment rather than printing a meaningless all-zero report.
		err = fmt.Errorf("%w: %v", errGuardRefused, err)
	}
	return report, err
}
