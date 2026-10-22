package api

import (
	"strings"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// spawn.go — the SPAWN-LINK stamp: when a cell's own `kraken job draft` or
// `kraken cell submit` reaches kraken serve, it carries the sending cell's slug
// in X-Kraken-From-Cell (the header the cross-node steer already uses, read
// from KRAKEN_JOB_SLUG by cmd/kraken/qfclient.go), and the route that created
// or started the job calls stampSpawn to record that cell as the job's parent
// (jobs.LinkSpawn → store.job_spawns, schema v38). The board then renders the
// link as chips (webSpawnLink, board.go).
//
// This is the PR hook's outcome without the PR hook's machinery: pr-hook has
// to parse `gh pr create`'s stdout because gh is a foreign binary, while these
// two verbs are kraken's own and already know their cell. One header on two
// requests that already happen is the whole "hook".
//
// The stamp never changes a route's answer. A header naming no active job, the
// job itself, or a child that already has a parent records nothing, and a
// store failure is logged, not returned: a draft or a submit refused over
// bookkeeping would be worse than a missing chip.

// fromCellHeader is the header the cell verbs carry; one spelling here and in
// cmd/kraken/qfclient.go (cmd_nodes.go's cross-node steer uses the same).
const fromCellHeader = "X-Kraken-From-Cell"

// stampSpawn records fromCell's job as child's parent, via naming the verb
// (jobs.SpawnViaDraft or jobs.SpawnViaSubmit). webJobFor resolves the header
// through former slugs too, so a parent renamed mid-run still stamps — its
// cell learned its slug at spawn and keeps sending the old one.
func stampSpawn(child *jobs.Job, fromCell, via string) {
	fromCell = strings.TrimSpace(fromCell)
	if child == nil || fromCell == "" {
		return
	}
	parent, _, err := webJobFor(fromCell)
	if err != nil {
		// A header that resolves to nothing is diagnosable from the log alone
		// (a cell whose KRAKEN_JOB_SLUG export is broken); the common
		// no-header case above logs nothing, so board and iOS submits stay quiet.
		util.LogEvent("web", "info", "spawn-link-unresolved", child.Slug+": "+fromCellHeader+" "+fromCell+" names no active job")
		return
	}
	if parent.ID == child.ID {
		return
	}
	if _, err := jobs.LinkSpawn(child, parent, via); err != nil {
		util.LogEvent("web", "error", "spawn-link", child.Slug+": "+err.Error())
	}
}
