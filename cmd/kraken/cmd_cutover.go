package main

import (
	"fmt"
	"os"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// cmd_cutover.go — `kraken cutover`, the one-time move of existing flat
// artifact copies onto id-scoped names (jobs/cutover.go explains the scope).
//
// It is a VERB rather than a `kraken serve` startup pass on purpose. This
// renames a few hundred real files in the user's notes directory, once, and
// the thing that makes that safe is a human reading the list first — which a
// startup pass has nowhere to show. Dry-run is therefore the default, and
// the real run requires --commit.
func cmdCutover(args []string) {
	commit := false
	for _, a := range args {
		switch a {
		case "--commit":
			commit = true
		case "-h", "--help":
			fmt.Println(`kraken cutover - move flat artifact copies onto <slug>-<id> names

usage:
  kraken cutover            plan only: print every intended rename, change nothing
  kraken cutover --commit   perform the renames

Run with no active jobs. Cell directories are deliberately left alone.`)
			return
		default:
			util.Die("unknown flag: %s (try 'kraken cutover --help')", a)
		}
	}

	// The plan needs every job's id, which only kraken.db has — a file-backed
	// job carries ID == 0. Opened read-write because --commit renames real
	// files off the back of it; sqlite's busy timeout handles a concurrently
	// running kraken serve, though the intended way to run this is with serve
	// stopped and no active jobs.
	st, err := store.Open(store.DBPath())
	if err != nil {
		util.Die("open %s: %v", store.DBPath(), err)
	}
	defer st.Close()
	jobs.UseStore(st)
	defer jobs.UseStore(nil)

	plan, err := jobs.PlanCutover()
	if err != nil {
		util.Die("%v", err)
	}

	for _, r := range plan.Renames {
		fmt.Printf("  %s\n    %s\n    -> %s\n", r.Job, r.From, r.To)
	}
	for _, u := range plan.Unmatched {
		fmt.Fprintf(os.Stderr, "  unattributed (left alone): %s\n", u)
	}
	fmt.Printf("\n%d rename(s), %d unattributed\n", len(plan.Renames), len(plan.Unmatched))

	if !commit {
		fmt.Println("\ndry run — nothing changed. Re-run with --commit to perform these renames.")
		return
	}
	if len(plan.Renames) == 0 {
		return
	}
	done, problems := jobs.RunCutover(plan)
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "  %s\n", p)
	}
	fmt.Printf("renamed %d of %d\n", done, len(plan.Renames))
	if len(problems) > 0 {
		// A partial migration is exactly the state that must not be mistaken
		// for a finished one: the pass is re-runnable, so say so and exit
		// non-zero rather than printing a count that reads like success.
		util.Die("%d rename(s) could not be performed — resolve them and re-run", len(problems))
	}
}
