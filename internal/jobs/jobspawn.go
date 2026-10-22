package jobs

import (
	"fmt"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// jobspawn.go — the jobs-level side of SPAWN LINKS: which job's cell filed
// which other job. A claude zeus runs with KRAKEN_JOB_SLUG in its environment
// (jobSlugEnvPrefix, cell.go), the `kraken job draft` and `kraken cell submit`
// verbs send it as the X-Kraken-From-Cell header (cmd/kraken/qfclient.go),
// and kraken serve stamps the row here when the header names another active
// job (internal/api/spawn.go). The board renders one chip per child on the
// parent and one parent chip on the child.
//
// Like stack links and the account binding, none of this goes through
// WriteJobFields: a spawn link is bookkeeping about who filed whom, and the
// rows live in their own table (store.job_spawns, schema v38) so neither
// job's `rev` or `updated_at` ever moves for it. Rows are retrospective — a
// link to a done or archived child is the useful kind — so nothing at
// teardown or archive deletes them; only a hard delete does (store.DeleteJob).

// SpawnVia values: which verb carried the stamp. The first writer wins, so a
// child the draft stamped keeps `draft` through its later submit.
const (
	SpawnViaDraft  = "draft"
	SpawnViaSubmit = "submit"
)

// LinkSpawn records parent as child's filer unless child already has one.
// Returns true when a row was written. A nil store refuses rather than
// degrading, as the stack writers do: there is no frontmatter spelling for a
// spawn link, so a silent no-op would be a link that never existed. Both ids
// must be real rows (the API resolves both first); a self-link is refused
// here too so no caller can record a job as its own parent.
func LinkSpawn(child, parent *Job, via string) (bool, error) {
	st := ActiveStore()
	if st == nil {
		return false, fmt.Errorf("link spawn %s <- %s: no store is wired", child.Slug, parent.Slug)
	}
	if child.ID == 0 || parent.ID == 0 {
		return false, fmt.Errorf("link spawn %s <- %s: a job with no id cannot be linked", child.Slug, parent.Slug)
	}
	if child.ID == parent.ID {
		return false, fmt.Errorf("link spawn %s: a job cannot spawn itself", child.Slug)
	}
	linked, err := st.SetJobSpawn(child.ID, parent.ID, time.Now().UnixMilli(), via)
	if err != nil {
		return false, fmt.Errorf("link spawn %s <- %s: %w", child.Slug, parent.Slug, err)
	}
	if linked {
		util.LogEvent("jobs", "info", "spawn-link", fmt.Sprintf("%s -> %s (%s)", parent.Slug, child.Slug, via))
	}
	return linked, nil
}

// ListSpawns is the read every renderer goes through: the whole table, in
// stamp order. No store means no links, never an error — the board calls
// this once per build.
func ListSpawns() []store.SpawnLink {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	rows, err := st.ListJobSpawns()
	if err != nil {
		util.LogEvent("jobs", "error", "spawn-list", err.Error())
		return nil
	}
	return rows
}
