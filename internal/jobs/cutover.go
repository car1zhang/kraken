package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kraken/internal/util"
)

// cutover.go — the one-time move onto id-scoped naming.
//
// Everything a job's runtime owns used to be derived from its slug alone, and
// archiving FREES a slug (store.ArchiveJob stamps archived_at/archived_name
// and leaves jobs.slug untouched), so two generations of one name resolved to
// one set of paths. cellName now appends the job's immutable id. New cells
// and new preserved artifacts are written under the new naming from the
// moment that shipped; this pass moves what already exists.
//
// Its scope is deliberately narrow — it renames the flat artifact copies in
// ~/Documents/pluto and its archive/ subdir, and nothing else:
//
//   - CELL DIRECTORIES are left exactly where they are, as orphans. Renaming
//     a worktree out from under a running agent breaks its pane and needs a
//     `git worktree repair`; the ruling was to run this with no active jobs
//     instead, which makes the whole adoption problem disappear. Nothing
//     reads the old dirs afterwards, and deleting them is a separate,
//     deliberate call.
//   - TOKEN COUNTERS are cleared by schema v11, not here, and re-summed by
//     the daemon's own throttled pass. JobProjectDirs keeps a legacy
//     slug-only base so the pre-cutover transcripts are still found.
//
// The pluto copies MUST move, though, and that is not optional: their reader
// (api/board.go's plutoFlatArtifacts and ArtifactPath) now matches on
// PlutoArtifactPrefix, so a file still carrying the bare-slug prefix simply
// stops appearing on its job's card. The alternative — teaching the reader
// both spellings — is a compatibility branch that would have to live forever
// on a single-machine tool.

// CutoverRename is one intended file move.
type CutoverRename struct {
	Dir  string // the directory both names sit in
	From string // current filename
	To   string // intended filename
	Job  string // the job it was attributed to, for the dry-run listing
}

// CutoverPlan is everything the pass intends to do, plus what it could not
// attribute. Building the plan touches nothing.
type CutoverPlan struct {
	Renames []CutoverRename
	// Unmatched lists files whose prefix resolved to no job, as
	// "<dir>/<file>". They are LEFT ALONE rather than guessed at: a flat
	// pluto file whose owner cannot be identified is somebody's artifact, and
	// the cost of leaving it is that it stays where it is.
	Unmatched []string
}

// PlanCutover works out every rename without performing any of them.
//
// Two mappings, not one, because the two directories are prefixed
// differently: a live pluto copy carries the job's slugified SLUG, while a
// copy that has been through ArchiveJob carries its numbered ARCHIVED NAME
// (`foo`, `foo-2`, `foo-3`). Both land on the same target spelling,
// PlutoArtifactPrefix — slug plus id.
//
// Ambiguity is resolved by preferring the LONGEST matching prefix. Prefixes
// are not self-delimiting: with jobs `foo` and `foo-2` both present, the file
// `foo-2.plan.md` matches both `foo.` (as "foo" + name "2.plan.md") and
// `foo-2.`, and only the longer one is right. Getting this backwards would
// silently file one job's artifact under another job's id — the exact class
// of bug this whole change exists to end.
func PlanCutover() (*CutoverPlan, error) {
	// Without a store every job carries ID == 0: the id is minted by the
	// store and never appears in a .job.md file, and LoadJobs falls back to
	// LoadJobsFromFiles when nothing is wired. The pass would then attribute
	// nothing and report every file as unmatched — a confident, wrong
	// "0 renames, 269 unattributed" that reads like there is nothing to do.
	if ActiveStore() == nil {
		return nil, fmt.Errorf("no store open — the cutover needs each job's id, which only kraken.db has")
	}
	active := LoadJobs()
	archived := LoadArchivedJobs()

	// prefix → job, for each directory's own naming rule.
	live := map[string]*Job{}
	for _, j := range active {
		if s := util.Slugify(j.Slug); s != "" && j.ID > 0 {
			live[s+"."] = j
		}
	}
	// Archived jobs' bare-slug prefixes belong in the PlutoDir map too. A job
	// archived before the id landed had nothing to move (ArchiveJob matched
	// the id prefix, which no old file carried), so its copies are still
	// sitting in PlutoDir under the bare slug. Without this they resolve to
	// no job and stay "unattributed" forever. Added AFTER the active map and
	// only where the key is free, so a live job always wins its own slug.
	for _, a := range archived {
		if s := util.Slugify(a.Slug); s != "" && a.ID > 0 {
			if _, taken := live[s+"."]; !taken {
				live[s+"."] = a
			}
		}
	}
	arch := map[string]*Job{}
	for _, a := range archived {
		if a.ArchivedName != "" && a.ID > 0 {
			arch[a.ArchivedName+"."] = a
		}
	}

	plan := &CutoverPlan{}
	if err := planDir(plan, PlutoDir(), live); err != nil {
		return nil, err
	}
	if err := planDir(plan, ArchiveDir(), arch); err != nil {
		return nil, err
	}
	sort.Slice(plan.Renames, func(a, b int) bool {
		if plan.Renames[a].Dir != plan.Renames[b].Dir {
			return plan.Renames[a].Dir < plan.Renames[b].Dir
		}
		return plan.Renames[a].From < plan.Renames[b].From
	})
	sort.Strings(plan.Unmatched)
	return plan, nil
}

func planDir(plan *CutoverPlan, dir string, byPrefix map[string]*Job) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A directory that was never created holds no artifacts to move.
			// Anything else is a directory we cannot SEE, which is not the
			// same as an empty one — report it rather than silently planning
			// zero renames for it.
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Job files are the jobs themselves, never artifacts, and pluto's
		// own daily notes (<dayID>.md) belong to the user's notes convention.
		if strings.HasSuffix(name, JobSuffix) || isPlutoDayNote(name) {
			continue
		}
		j, prefix := longestPrefixMatch(name, byPrefix)
		if j == nil {
			plan.Unmatched = append(plan.Unmatched, filepath.Join(dir, name))
			continue
		}
		want := PlutoArtifactPrefix(j) + strings.TrimPrefix(name, prefix)
		if want == name {
			continue // already migrated — this pass is re-runnable
		}
		plan.Renames = append(plan.Renames, CutoverRename{
			Dir: dir, From: name, To: want, Job: j.Slug,
		})
	}
	return nil
}

// longestPrefixMatch picks the most specific prefix a filename starts with.
// See PlanCutover for why longest rather than first.
func longestPrefixMatch(name string, byPrefix map[string]*Job) (*Job, string) {
	var best *Job
	var bestPrefix string
	for p, j := range byPrefix {
		if !strings.HasPrefix(name, p) {
			continue
		}
		if len(p) > len(bestPrefix) {
			best, bestPrefix = j, p
		}
	}
	return best, bestPrefix
}

// isPlutoDayNote reports whether name is one of pluto's own daily notes
// (`<dayID>.md`, days since 2006-03-12). They share the directory with flat
// artifact copies and must never be touched.
func isPlutoDayNote(name string) bool {
	base, ok := strings.CutSuffix(name, ".md")
	if !ok || base == "" {
		return false
	}
	for _, r := range base {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RunCutover performs a plan's renames. It refuses to overwrite: a target
// that already exists means two files claim one name, which needs a human,
// not a winner picked by directory order.
//
// Renames are independent, so one failure does not abort the rest — the pass
// collects every problem and reports them together, because being told about
// the first of nine broken moves and nothing else is how a half-migrated
// directory happens.
func RunCutover(plan *CutoverPlan) (done int, problems []string) {
	for _, r := range plan.Renames {
		from := filepath.Join(r.Dir, r.From)
		to := filepath.Join(r.Dir, r.To)
		if _, err := os.Stat(to); err == nil {
			problems = append(problems, fmt.Sprintf("%s: target %s already exists — left alone", from, r.To))
			continue
		}
		if err := os.Rename(from, to); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", from, err))
			continue
		}
		done++
	}
	return done, problems
}
