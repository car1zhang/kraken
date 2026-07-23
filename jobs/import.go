package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"kraken/store"
	"kraken/util"
)

// import.go — the one-shot importer (spec: "Component: one-shot importer").
// ImportAll reads every current file store — active + archived job files,
// board order, uploads, pluto/cell artifacts, JSONL event logs, doc
// feedback — and upserts it into kraken.db. It is read-only with respect to
// every source: no file is ever written, renamed, or removed. Every write
// is a keyed upsert (or, for events/feedback, a dedup-by-content skip), so
// running ImportAll twice against the same sources leaves the database
// exactly as it was after the first run — the "idempotent" half of the
// spec's "idempotent (keyed upserts)" contract.
//
// This is also the last caller of ParseJob and the legacy file-tree shapes
// after cutover (spec's "Retirements" table): once kraken serve is the
// sole writer, this file and ParseJob are deleted together.
type ImportOptions struct {
	// DryRun computes every count without writing anything to st.
	DryRun bool

	// Force allows ImportAll to run even when st's jobs table already has
	// rows. Without it, ImportAll refuses immediately (before touching any
	// source file) rather than risk re-importing on top of a database
	// that's already in live use by kraken serve — a one-shot cutover tool
	// re-run by habit is a much likelier accident than a deliberate
	// re-import. DryRun bypasses this check unconditionally: it never
	// writes, so there's nothing to protect.
	Force bool

	// Reimport (E4) is the second, STRONGER override required once a prior
	// non-dry-run import has already completed — signaled by the
	// importedAtKVKey marker being set, independent of the jobs table's row
	// count. Force alone covers "this fresh DB already has stray rows";
	// it does NOT cover "a full import already happened here, and kraken
	// serve has likely accepted real writes on top of it since" — silently
	// re-sweeping the file-mode sources over that would stomp live serve
	// data with stale file snapshots. Reimport is the explicit
	// acknowledgment of that destructive semantics; DryRun bypasses this
	// check unconditionally, same as Force.
	Reimport bool

	// PlutoDir defaults to PlutoDir() (~/Documents/pluto, KRAKEN_PLUTO_DIR).
	PlutoDir string
	// ArchiveDir defaults to ArchiveDir() (PlutoDir()/archive).
	ArchiveDir string
	// UploadsDir defaults to the uploads root (~/.kraken/uploads,
	// KRAKEN_UPLOADS_DIR).
	UploadsDir string
	// LogsDir defaults to util.LogsDir() (~/.kraken/logs, KRAKEN_LOGS_DIR).
	LogsDir string
	// CellsDir defaults to ~/.kraken/cells — the root each job's live cell
	// (and its artifacts/ subtree) lives under.
	CellsDir string
	// OrderPath defaults to ~/.kraken/order.json.
	OrderPath string
}

// resolved fills every empty field with its real-location default. Called
// once at the top of ImportAll.
func (o ImportOptions) resolved() ImportOptions {
	if o.PlutoDir == "" {
		o.PlutoDir = PlutoDir()
	}
	if o.ArchiveDir == "" {
		o.ArchiveDir = ArchiveDir()
	}
	if o.UploadsDir == "" {
		o.UploadsDir = defaultUploadsRoot()
	}
	if o.LogsDir == "" {
		o.LogsDir = util.LogsDir()
	}
	if o.CellsDir == "" {
		o.CellsDir = defaultCellsRoot()
	}
	if o.OrderPath == "" {
		o.OrderPath = defaultOrderPath()
	}
	return o
}

// defaultUploadsRoot mirrors uploads.go's unexported uploadsRoot(): the
// importer can't call it directly (a custom root is a first-class
// ImportOptions field, not just an env override), so the same env-first
// logic is replicated here.
func defaultUploadsRoot() string {
	if d := os.Getenv("KRAKEN_UPLOADS_DIR"); d != "" {
		return d
	}
	return filepath.Join(util.Home(), ".kraken", "uploads")
}

// defaultCellsRoot mirrors cell.go's unexported cellsRoot(). No env
// override exists there today; ImportOptions.CellsDir is the only knob.
func defaultCellsRoot() string {
	return filepath.Join(util.Home(), ".kraken", "cells")
}

// defaultOrderPath mirrors order.go's unexported orderPath(). No env
// override exists there today; ImportOptions.OrderPath is the only knob.
func defaultOrderPath() string {
	return filepath.Join(util.Home(), ".kraken", "order.json")
}

// StoreReport is the per-store tally ImportAll returns for each of the
// seven sources it sweeps.
type StoreReport struct {
	Found    int      // rows/files/entries encountered
	Imported int      // rows that ended up represented in the store
	Skipped  []string // human-readable reasons, one per skipped/flagged item
}

func (r StoreReport) String() string {
	s := fmt.Sprintf("%d found, %d imported", r.Found, r.Imported)
	if n := len(r.Skipped); n > 0 {
		s += fmt.Sprintf(", %d skipped", n)
	}
	return s
}

// ImportReport is ImportAll's return value: one StoreReport per source.
type ImportReport struct {
	Jobs       StoreReport
	Archived   StoreReport
	BoardOrder StoreReport
	Uploads    StoreReport
	Artifacts  StoreReport
	Events     StoreReport
	Feedback   StoreReport
}

func (r ImportReport) String() string {
	return fmt.Sprintf(
		"jobs: %s\narchived: %s\nboard order: %s\nuploads: %s\nartifacts: %s\nevents: %s\nfeedback: %s",
		r.Jobs, r.Archived, r.BoardOrder, r.Uploads, r.Artifacts, r.Events, r.Feedback,
	)
}

// importedAtKVKey is the kv key ImportAll stamps (unix millis) on a
// successfully completed non-dry run — a marker future runs can check
// before deciding whether Force is really intended.
const importedAtKVKey = "imported_at"

// importInProgressKVKey (L7) is set to the start time (unix millis) for the
// duration of every non-dry-run ImportAll call and cleared (set back to "")
// unconditionally on every exit path — success or error — via defer.
// GuardServeStart checks it so a `kraken serve` startup racing a live
// import refuses instead of double-writing the same kraken.db the importer
// hasn't finished sweeping into yet.
const importInProgressKVKey = "import_in_progress"

// ImportAll sweeps every source store under opts into st. It never modifies
// a source file (no os.WriteFile/os.Rename/os.Remove anywhere in this
// file), and every write to st is a keyed upsert, so re-running ImportAll
// against unchanged sources leaves st byte-for-byte where it was.
//
// Two things can make ImportAll return a non-nil error without it meaning
// "nothing happened": a non-empty jobs table without Force refuses before
// any source is even read (report comes back zero-valued), but a job file
// that fails to parse does NOT abort the sweep — every other source is
// still fully imported and reported, and the parse failures are surfaced
// only once the whole sweep (including dry runs) has completed.
func ImportAll(st *store.Store, opts ImportOptions) (ImportReport, error) {
	opts = opts.resolved()
	var report ImportReport

	if !opts.DryRun {
		importedAt, err := st.GetKV(importedAtKVKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return report, err
		}
		if importedAt != "" && !opts.Reimport {
			return report, fmt.Errorf(
				"this database was already imported (at unix-millis %s) — re-importing can silently overwrite live kraken serve data written since; pass Reimport to proceed anyway",
				importedAt,
			)
		}
		if !opts.Force {
			existingActive, err := st.ListJobs()
			if err != nil {
				return report, err
			}
			existingArchived, err := st.ListArchivedJobs()
			if err != nil {
				return report, err
			}
			if n := len(existingActive) + len(existingArchived); n > 0 {
				return report, fmt.Errorf(
					"import target already has %d job row(s) (%d active, %d archived); pass Force to re-import",
					n, len(existingActive), len(existingArchived),
				)
			}
		}

		// L7: mark the import in progress for the rest of this call, cleared
		// unconditionally on every exit path (success or error) via defer —
		// GuardServeStart refuses to start `kraken serve` while this is set,
		// closing the race where serve starts mid-sweep and both processes
		// write the same kraken.db concurrently.
		if err := st.SetKV(importInProgressKVKey, fmt.Sprintf("%d", time.Now().UnixMilli())); err != nil {
			return report, err
		}
		defer func() {
			_ = st.SetKV(importInProgressKVKey, "")
		}()
	}

	activeFiles, err := readJobFiles(opts.PlutoDir)
	if err != nil {
		return report, err
	}
	jobsRep, activeIDBySlug, activeIDBySlugified, activeParseErrs := importActiveJobs(st, activeFiles, opts.DryRun)
	report.Jobs = jobsRep

	archivedFiles, err := readArchivedJobFiles(opts.ArchiveDir)
	if err != nil {
		return report, err
	}
	archivedRep, archivedIDByName, archivedParseErrs := importArchivedJobs(st, archivedFiles, opts.DryRun)
	report.Archived = archivedRep

	boardRep, err := importBoardOrder(st, opts.OrderPath, opts.DryRun)
	if err != nil {
		return report, err
	}
	report.BoardOrder = boardRep

	uploadsRep, err := importUploads(st, opts.UploadsDir, activeIDBySlugified, opts.DryRun)
	if err != nil {
		return report, err
	}
	report.Uploads = uploadsRep

	artifactsRep, err := importArtifacts(st, opts, activeFiles, archivedFiles, activeIDBySlug, archivedIDByName, opts.DryRun)
	if err != nil {
		return report, err
	}
	report.Artifacts = artifactsRep

	eventsRep, err := importEventLogs(st, opts.LogsDir, opts.DryRun)
	if err != nil {
		return report, err
	}
	report.Events = eventsRep

	feedbackRep, err := importFeedback(st, filepath.Join(opts.PlutoDir, "doc-feedback"), opts.DryRun)
	if err != nil {
		return report, err
	}
	report.Feedback = feedbackRep

	if !opts.DryRun {
		if err := st.SetKV(importedAtKVKey, fmt.Sprintf("%d", time.Now().UnixMilli())); err != nil {
			return report, err
		}
	}

	var parseErrs []string
	parseErrs = append(parseErrs, activeParseErrs...)
	parseErrs = append(parseErrs, archivedParseErrs...)
	if len(parseErrs) > 0 {
		return report, fmt.Errorf("%d job file(s) failed to parse: %s", len(parseErrs), strings.Join(parseErrs, "; "))
	}

	return report, nil
}

// --- shared job-file reading ---

// parsedJobFile is one *.job.md file read straight off disk (os.ReadDir +
// ParseJob — never LoadJobs/LoadArchivedJobs, which may be mid-refactor
// onto the store in a concurrently edited file; this stays independent of
// that).
type parsedJobFile struct {
	Name    string
	Job     *Job
	ModTime time.Time
}

// entryModTimeMillis reads e's on-disk mtime, falling back to time.Now()
// when Info() fails (e.g. a TOCTOU race against a concurrent delete)
// instead of leaving a zero value: a zero int64/time.Time would stamp a
// fake epoch/year-1 timestamp on the row rather than degrading sensibly.
func entryModTimeMillis(e fs.DirEntry) int64 {
	if info, err := e.Info(); err == nil {
		return info.ModTime().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// readJobFiles lists dir's *.job.md files and parses each one. A missing
// dir degrades to an empty list (same contract as LoadJobs), never an
// error; an individual unreadable file becomes a parsedJobFile whose Job
// carries a synthetic Err, so it's still counted and reported rather than
// silently vanishing.
func readJobFiles(dir string) ([]parsedJobFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []parsedJobFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), JobSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			out = append(out, parsedJobFile{Name: e.Name(), Job: &Job{Err: fmt.Sprintf("read error: %v", err)}})
			continue
		}
		var mt time.Time
		if info, err := e.Info(); err == nil {
			mt = info.ModTime()
		} else {
			mt = time.Now() // guard against a zero Time producing a year-1 millis stamp
		}
		out = append(out, parsedJobFile{Name: e.Name(), Job: ParseJob(e.Name(), data), ModTime: mt})
	}
	return out, nil
}

// readArchivedJobFiles is readJobFiles plus ArchivedName stamping — the
// numbered file base that's an archived job's real identity (two archived
// jobs can share a Slug, never a file), mirroring LoadArchivedJobs.
func readArchivedJobFiles(dir string) ([]parsedJobFile, error) {
	files, err := readJobFiles(dir)
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].Job.ArchivedName = strings.TrimSuffix(files[i].Name, JobSuffix)
	}
	return files, nil
}

// runValue normalizes a parsed job's Run field the same way store.CreateJob
// defaults it: unset (0) means run 1.
func runValue(run int) int64 {
	if run <= 0 {
		return 1
	}
	return int64(run)
}

// --- active jobs ---

// importActiveJobs upserts every successfully parsed file in files as an
// active job row (keyed on slug). It returns the report; two lookup maps
// for later phases — id by raw slug (uploads dir names are slugified, so
// this alone isn't enough) and id by util.Slugify(slug) (matches the
// on-disk uploads/cell dir naming) — populated for every job that actually
// won its slug (dry-run ids are sentinel -1, present for membership checks,
// never used to write); and the list of files whose ParseJob Err was
// non-empty, for ImportAll to fold into its aggregate error.
//
// Two distinct problems get caught and reported here, neither silently:
// a duplicate raw slug (two files whose frontmatter `job:` — or filename
// fallback — resolves to the same string) keeps its first file and skips
// the rest, since re-querying the store would otherwise just look like an
// ordinary update; and two DIFFERENT slugs that util.Slugify identically
// (e.g. differing only in case/punctuation) both import as distinct job
// rows, but only the first claims the shared uploads/cell-artifacts
// directory name — the second is flagged so that ambiguity is visible
// instead of one job's uploads quietly vanishing into the other's.
func importActiveJobs(st *store.Store, files []parsedJobFile, dryRun bool) (StoreReport, map[string]int64, map[string]int64, []string) {
	rep := StoreReport{}
	bySlug := map[string]int64{}
	bySlugified := map[string]int64{}
	seenSlug := map[string]bool{}
	slugBySlugified := map[string]string{}
	var parseErrs []string

	for _, pf := range files {
		rep.Found++
		if pf.Job.Err != "" {
			msg := fmt.Sprintf("%s: %s", pf.Name, pf.Job.Err)
			rep.Skipped = append(rep.Skipped, msg)
			parseErrs = append(parseErrs, msg)
			continue
		}
		if seenSlug[pf.Job.Slug] {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: duplicate slug %q (an earlier file already imported it)", pf.Name, pf.Job.Slug))
			continue
		}
		seenSlug[pf.Job.Slug] = true

		id := int64(-1)
		if !dryRun {
			var err error
			id, err = upsertActiveJobRow(st, pf.Job, pf.ModTime)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: %v", pf.Name, err))
				continue
			}
		}
		bySlug[pf.Job.Slug] = id

		slugified := util.Slugify(pf.Job.Slug)
		if other, ok := slugBySlugified[slugified]; ok && other != pf.Job.Slug {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf(
				"%s: slug %q slugifies to %q, same as already-imported slug %q — uploads/cell artifacts may be misattributed between them",
				pf.Name, pf.Job.Slug, slugified, other,
			))
		} else {
			slugBySlugified[slugified] = pf.Job.Slug
			bySlugified[slugified] = id
		}
		rep.Imported++
	}
	return rep, bySlug, bySlugified, parseErrs
}

// upsertActiveJobRow inserts j as a new active job, or — if a job with that
// slug already exists — updates its fields in place, so a re-import is a
// no-op rather than a duplicate or a stale-rev error.
func upsertActiveJobRow(st *store.Store, j *Job, modTime time.Time) (int64, error) {
	existing, err := st.GetJobBySlug(j.Slug)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if existing == nil {
		millis := modTime.UnixMilli()
		row := &store.Job{
			Slug: j.Slug, Type: j.Type, Repo: j.Repo, Base: j.Base,
			Ready: j.Ready, Solo: j.Solo, Model: j.Model, SkipDoc: j.SkipDoc,
			Body: j.Body, State: j.State, Outcome: j.Outcome,
			Session: j.Session, Teardown: j.Teardown, Run: runValue(j.Run),
			CreatedAt: millis, UpdatedAt: millis,
		}
		id, err := st.CreateJob(row)
		if err != nil {
			return 0, err
		}
		if err := syncJobChildren(st, id, j); err != nil {
			return 0, err
		}
		return id, nil
	}

	fields := jobFieldsMap(j)
	if jobFieldsChanged(existing, fields) {
		if _, err := st.UpdateJobFields(existing.ID, existing.Rev, fields); err != nil {
			return 0, err
		}
	}
	if err := syncJobChildren(st, existing.ID, j); err != nil {
		return 0, err
	}
	return existing.ID, nil
}

// syncJobChildren replaces id's prereqs (delete+reinsert, so it's exactly
// what the file says) and upserts every PR the file records.
func syncJobChildren(st *store.Store, id int64, j *Job) error {
	prereqs := make([]store.Prereq, 0, len(j.Prereqs))
	for _, p := range j.Prereqs {
		prereqs = append(prereqs, store.Prereq{Slug: p.Job, Gated: p.Gated})
	}
	if err := st.ReplacePrereqs(id, prereqs); err != nil {
		return err
	}
	for _, p := range j.PRs {
		if err := st.UpsertPR(id, p.URL, p.State); err != nil {
			return err
		}
	}
	return nil
}

// jobFieldsMap builds the UpdateJobFields payload from a parsed job — every
// column jobFieldColumns allows, so re-importing a changed file converges
// the row rather than only ever adding. Body is stored verbatim, including
// any "## Mid-run log" section: losslessness wins here (every runtime
// reader that wants the human-authored spec alone already strips that
// section itself — jobPrompt in cell.go, splitBodyLog in api/jobedit.go —
// so the importer doing it too would just be a second, divergent place to
// keep that logic in sync, at the cost of the archival copy losing bytes
// the original file had).
func jobFieldsMap(j *Job) map[string]any {
	return map[string]any{
		"slug": j.Slug, "type": j.Type, "repo": j.Repo, "base": j.Base,
		"ready": j.Ready, "solo": j.Solo, "model": j.Model, "skip_doc": j.SkipDoc,
		"body": j.Body, "state": j.State, "outcome": j.Outcome,
		"session": j.Session, "teardown": j.Teardown, "run": runValue(j.Run),
	}
}

// jobFieldsChanged reports whether any value in fields (as built by
// jobFieldsMap) differs from existing's current column value. Re-importing
// an unchanged file must leave the row untouched — not just
// unchanged-in-content but byte-for-byte, including rev and updated_at — so
// upsertActiveJobRow calls this to skip UpdateJobFields entirely rather
// than issue a no-op write that would still bump rev and restamp
// updated_at.
func jobFieldsChanged(existing *store.Job, fields map[string]any) bool {
	for k, v := range fields {
		switch k {
		case "slug":
			if existing.Slug != v.(string) {
				return true
			}
		case "type":
			if existing.Type != v.(string) {
				return true
			}
		case "repo":
			if existing.Repo != v.(string) {
				return true
			}
		case "base":
			if existing.Base != v.(string) {
				return true
			}
		case "ready":
			if existing.Ready != v.(bool) {
				return true
			}
		case "solo":
			if existing.Solo != v.(bool) {
				return true
			}
		case "model":
			if existing.Model != v.(string) {
				return true
			}
		case "skip_doc":
			if existing.SkipDoc != v.(bool) {
				return true
			}
		case "body":
			if existing.Body != v.(string) {
				return true
			}
		case "state":
			if existing.State != v.(string) {
				return true
			}
		case "outcome":
			if existing.Outcome != v.(string) {
				return true
			}
		case "session":
			if existing.Session != v.(string) {
				return true
			}
		case "teardown":
			if existing.Teardown != v.(string) {
				return true
			}
		case "run":
			if existing.Run != v.(int64) {
				return true
			}
		}
	}
	return false
}

// --- archived jobs ---

// importArchivedJobs upserts every successfully parsed archive-dir file as
// an archived job row, keyed on ArchivedName (the numbered file base — the
// only identity that's actually unique among archived jobs, since Slug can
// collide: unlike active jobs, no separate duplicate-detection pass is
// needed here, since ArchivedName is exactly the on-disk filename base and
// os.ReadDir can never hand back two entries with the same name). Returns
// the report, an id-by-ArchivedName map for the artifacts phase, and the
// list of files whose ParseJob Err was non-empty.
func importArchivedJobs(st *store.Store, files []parsedJobFile, dryRun bool) (StoreReport, map[string]int64, []string) {
	rep := StoreReport{}
	byName := map[string]int64{}
	var parseErrs []string
	for _, pf := range files {
		rep.Found++
		if pf.Job.Err != "" {
			msg := fmt.Sprintf("%s: %s", pf.Name, pf.Job.Err)
			rep.Skipped = append(rep.Skipped, msg)
			parseErrs = append(parseErrs, msg)
			continue
		}
		id := int64(-1)
		if !dryRun {
			var err error
			id, err = upsertArchivedJobRow(st, pf.Job, pf.ModTime.UnixMilli())
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: %v", pf.Name, err))
				continue
			}
			if err := syncJobChildren(st, id, pf.Job); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: %v", pf.Name, err))
				continue
			}
		}
		byName[pf.Job.ArchivedName] = id
		rep.Imported++
	}
	return rep, byName, parseErrs
}

// upsertArchivedJobRow builds the store.Job row for one archived file and
// hands it to store.CreateArchivedJob, which does the actual
// insert-or-update keyed on ArchivedName.
func upsertArchivedJobRow(st *store.Store, j *Job, archivedAtMillis int64) (int64, error) {
	row := &store.Job{
		Slug: j.Slug, Type: j.Type, Repo: j.Repo, Base: j.Base,
		Ready: j.Ready, Solo: j.Solo, Model: j.Model, SkipDoc: j.SkipDoc,
		Body: j.Body, State: j.State, Outcome: j.Outcome,
		Session: j.Session, Teardown: j.Teardown, Run: runValue(j.Run),
		ArchivedAt: archivedAtMillis, ArchivedName: j.ArchivedName,
	}
	return st.CreateArchivedJob(row)
}

// --- board order ---

// importBoardOrder reads order.json directly (not jobs.LoadBoardOrder,
// which hardcodes the real path with no override) and replays each column
// through SetBoardColumn, which is already an idempotent replace.
//
// SetBoardColumn enforces "a slug lives in exactly one column" by deleting
// it from every OTHER column as a side effect of placing it into the one
// being written — so a slug listed in two columns of order.json silently
// keeps only its alphabetically-last column once every column has been
// replayed (columns are processed in sorted order, mirrored below). Found
// counts slugs, not columns (a column is just a grouping, not a unit of
// work), and every such drop gets a Skipped note instead of vanishing.
func importBoardOrder(st *store.Store, path string, dryRun bool) (StoreReport, error) {
	rep := StoreReport{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read %s: %w", path, err)
	}
	var parsed struct {
		Columns map[string][]string `json:"columns"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || parsed.Columns == nil {
		// same degrade-to-empty contract as LoadBoardOrder: a corrupt file
		// is not a fatal import error
		return rep, nil
	}
	cols := make([]string, 0, len(parsed.Columns))
	for c := range parsed.Columns {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	// finalCol tracks, as columns are processed in order, which column each
	// slug currently resolves to in the store — used purely to detect and
	// report the cross-column drops SetBoardColumn performs internally.
	finalCol := map[string]string{}
	for _, col := range cols {
		slugs := parsed.Columns[col]
		rep.Found += len(slugs)

		if !dryRun {
			if err := st.SetBoardColumn(col, slugs); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("column %s: %v", col, err))
				continue
			}
		}

		seen := map[string]bool{}
		for _, slug := range slugs {
			if seen[slug] {
				continue
			}
			seen[slug] = true
			if prevCol, ok := finalCol[slug]; ok && prevCol != col {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf(
					"slug %s: dropped from column %s (also listed in column %s, which wins)", slug, prevCol, col,
				))
			}
			finalCol[slug] = col
		}
	}
	rep.Imported = len(finalCol)
	return rep, nil
}

// --- uploads ---

// importUploadNameRe matches the img-NNN.<ext> shape uploads.go writes;
// anything else found in a job's uploads dir is reported, not imported.
var importUploadNameRe = regexp.MustCompile(`^img-\d+\.[a-z0-9]+$`)

var uploadMimeByExt = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg",
	"gif": "image/gif", "webp": "image/webp", "heic": "image/heic",
}

func mimeForExt(ext string) string {
	return uploadMimeByExt[strings.ToLower(ext)]
}

// importUploads sweeps <uploadsDir>/<slugified-slug>/img-NNN.<ext> for every
// active job the jobs phase produced. A dir whose name doesn't match any
// imported job's slugified slug is an orphan — counted (its files) and
// skipped (one note per dir) rather than silently dropped or guessed at. A
// per-job dir that exists but can't be read is reported too: unreadable is
// not the same thing as empty, and treating it as empty would silently
// drop whatever uploads it actually holds.
func importUploads(st *store.Store, uploadsDir string, activeIDBySlugified map[string]int64, dryRun bool) (StoreReport, error) {
	rep := StoreReport{}
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read %s: %w", uploadsDir, err)
	}
	for _, dirEnt := range entries {
		if !dirEnt.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(uploadsDir, dirEnt.Name()))
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", dirEnt.Name(), err))
			continue
		}
		jobID, ok := activeIDBySlugified[dirEnt.Name()]
		if !ok {
			rep.Found += len(files)
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("orphan upload dir %s (no matching job, %d file(s))", dirEnt.Name(), len(files)))
			continue
		}
		for _, fe := range files {
			if fe.IsDir() {
				continue
			}
			rep.Found++
			name := fe.Name()
			if !importUploadNameRe.MatchString(strings.ToLower(name)) {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s/%s: unrecognized upload name", dirEnt.Name(), name))
				continue
			}
			path := filepath.Join(uploadsDir, dirEnt.Name(), name)
			data, err := os.ReadFile(path)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", path, err))
				continue
			}
			if dryRun {
				rep.Imported++
				continue
			}
			ext := strings.TrimPrefix(filepath.Ext(name), ".")
			if err := st.UpsertUploadNamed(jobID, name, mimeForExt(ext), data, entryModTimeMillis(fe)); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: store error: %v", path, err))
				continue
			}
			rep.Imported++
		}
	}
	return rep, nil
}

// --- artifacts ---

// jobArtifactSource names one job's two possible artifact homes: the flat
// <prefix>.<name> copies in FlatDir (pluto for active jobs, the archive dir
// for archived ones), and the live/former cell's artifacts/ tree, which
// wins on any path collision (spec: "cell wins on collision"). CellDir
// applies to archived jobs too — a teardown doesn't always clean up the
// artifacts subtree, so an archived job's cell dir can still hold real
// deliverables (empty string only for a job whose CellDir genuinely can't
// be derived).
type jobArtifactSource struct {
	JobID      int64
	FlatDir    string
	FlatPrefix string
	CellDir    string
}

// importArtifacts walks both artifact homes for every successfully imported
// active and archived job, merging them (cell wins) and syncing the result
// into st via store.SyncArtifacts — an upsert-then-delete-stale mirror of
// exactly what's on disk right now, which is idempotent by construction:
// re-running against unchanged sources produces the same file set and thus
// the same rows. It also sweeps opts.CellsDir for any <slug>/artifacts
// directory no imported job claimed, reporting each as a Skipped note: a
// cell whose job file vanished, failed to parse, or changed slug must never
// silently drop real deliverables.
func importArtifacts(st *store.Store, opts ImportOptions, activeFiles, archivedFiles []parsedJobFile, activeIDBySlug, archivedIDByName map[string]int64, dryRun bool) (StoreReport, error) {
	var total StoreReport
	claimedCellDirs := map[string]bool{}

	for _, pf := range activeFiles {
		if pf.Job.Err != "" {
			continue
		}
		id, ok := activeIDBySlug[pf.Job.Slug]
		if !ok {
			continue
		}
		slugified := util.Slugify(pf.Job.Slug)
		src := jobArtifactSource{
			JobID:      id,
			FlatDir:    opts.PlutoDir,
			FlatPrefix: slugified,
			CellDir:    filepath.Join(opts.CellsDir, slugified, "artifacts"),
		}
		claimedCellDirs[src.CellDir] = true
		rep, err := importArtifactsForJob(st, src, dryRun)
		if err != nil {
			return total, err
		}
		total.Found += rep.Found
		total.Imported += rep.Imported
		total.Skipped = append(total.Skipped, rep.Skipped...)
	}
	for _, pf := range archivedFiles {
		if pf.Job.Err != "" {
			continue
		}
		id, ok := archivedIDByName[pf.Job.ArchivedName]
		if !ok {
			continue
		}
		slugified := util.Slugify(pf.Job.Slug)
		src := jobArtifactSource{
			JobID:      id,
			FlatDir:    opts.ArchiveDir,
			FlatPrefix: pf.Job.ArchivedName,
			CellDir:    filepath.Join(opts.CellsDir, slugified, "artifacts"),
		}
		claimedCellDirs[src.CellDir] = true
		rep, err := importArtifactsForJob(st, src, dryRun)
		if err != nil {
			return total, err
		}
		total.Found += rep.Found
		total.Imported += rep.Imported
		total.Skipped = append(total.Skipped, rep.Skipped...)
	}

	total.Skipped = append(total.Skipped, orphanCellArtifactDirs(opts.CellsDir, claimedCellDirs)...)
	return total, nil
}

// orphanCellArtifactDirs reports every <cellsDir>/*/artifacts directory not
// claimed by any job this run imported. A missing CellsDir degrades to no
// orphans (nothing to sweep), same as everywhere else here.
func orphanCellArtifactDirs(cellsDir string, claimed map[string]bool) []string {
	entries, err := os.ReadDir(cellsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var notes []string
	for _, name := range names {
		artDir := filepath.Join(cellsDir, name, "artifacts")
		if claimed[artDir] {
			continue
		}
		if info, err := os.Stat(artDir); err == nil && info.IsDir() {
			notes = append(notes, fmt.Sprintf("orphan cell artifacts dir %s (no matching job claims it)", artDir))
		}
	}
	return notes
}

// importArtifactsForJob collects src's flat-copy and (if any) cell-tree
// files into one path-keyed set — cell entries overwrite flat entries on a
// path collision, since the cell copy is the live/authoritative one — then
// syncs the set into st in a single call.
func importArtifactsForJob(st *store.Store, src jobArtifactSource, dryRun bool) (StoreReport, error) {
	rep := StoreReport{}
	files := map[string]store.ArtifactFile{}

	entries, err := os.ReadDir(src.FlatDir)
	switch {
	case err == nil:
		prefix := src.FlatPrefix + "."
		for _, e := range entries {
			name := e.Name()
			// Flat-dir entries are already gated by the "<prefix>." naming
			// convention, so — unlike the cell-tree walk below, which has
			// to actively skip dotfiles/dot-dirs to avoid sweeping in git
			// internals etc. — a leading dot elsewhere in the name isn't a
			// hidden-file signal worth special-casing here. What DOES need
			// excluding is util.AtomicWrite's own "<path>.kraken.tmp"
			// rename-staging residue, which matches the prefix just as
			// well as a real artifact and would otherwise get swept in as
			// one if a write raced this scan.
			if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, JobSuffix) || strings.HasSuffix(name, ".kraken.tmp") {
				continue
			}
			rep.Found++
			path := filepath.Join(src.FlatDir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", path, err))
				continue
			}
			key := strings.TrimPrefix(name, prefix)
			files[key] = store.ArtifactFile{Path: key, Bytes: data, Mtime: entryModTimeMillis(e)}
		}
	case os.IsNotExist(err):
		// no flat dir for this job: nothing to report, same degrade-to-empty
		// contract as everywhere else here
	default:
		// unreadable is not the same thing as empty — report it rather than
		// silently importing zero flat artifacts
		rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", src.FlatDir, err))
	}

	if src.CellDir != "" {
		_ = filepath.WalkDir(src.CellDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // unreadable entry: skip it, keep walking the rest
			}
			if d.IsDir() {
				if p != src.CellDir && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, err := filepath.Rel(src.CellDir, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			rep.Found++
			data, err := os.ReadFile(p)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", p, err))
				return nil
			}
			files[rel] = store.ArtifactFile{Path: rel, Bytes: data, Mtime: entryModTimeMillis(d)} // cell wins
			return nil
		})
	}

	if dryRun {
		rep.Imported = len(files)
		return rep, nil
	}
	if len(files) == 0 {
		return rep, nil
	}
	list := make([]store.ArtifactFile, 0, len(files))
	for _, f := range files {
		list = append(list, f)
	}
	// The importer has no prior-sync state to diff against — it always
	// upserts every file it just read off disk, so there's nothing to pass
	// as the unchanged set.
	if err := st.SyncArtifacts(src.JobID, list, nil); err != nil {
		return rep, fmt.Errorf("sync artifacts for job %d: %w", src.JobID, err)
	}
	rep.Imported = len(list)
	return rep, nil
}

// --- event logs ---

// importLogFileRe pins the same timestamp-derived shape util.LogEvent
// writes (util/eventlog.go's unexported logFileRe, replicated since util
// sits below this package and can't be reached for the constant). Anything
// else in the logs dir — notably daemon.log — never matches and is silently
// left alone: Q16 decided JSONL logs only, prose not parsed.
var importLogFileRe = regexp.MustCompile(`^\d{8}-\d{6}\.jsonl$`)

// importEventLogs replays every *.jsonl file, oldest name first (names are
// timestamp-derived, so lexicographic order is chronological order), into
// the events table. Since events have no natural unique key, idempotency is
// achieved by pre-loading every event already in st and matching source
// lines against that multiset by content: a line whose (ts, src, level,
// event, msg) already has an unconsumed match is treated as already
// imported (counted, not re-inserted) rather than duplicated.
func importEventLogs(st *store.Store, logsDir string, dryRun bool) (StoreReport, error) {
	rep := StoreReport{}
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read %s: %w", logsDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !importLogFileRe.MatchString(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	existing, err := allEvents(st)
	if err != nil {
		return rep, err
	}
	consumed := make(map[string]int, len(existing))
	for _, e := range existing {
		consumed[eventKeyFromStore(e)]++
	}

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(logsDir, name))
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", name, err))
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			rep.Found++
			var le util.LogEntry
			if err := json.Unmarshal([]byte(line), &le); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: malformed line", name))
				continue
			}
			key := eventKeyFromEntry(le)
			if consumed[key] > 0 {
				consumed[key]--
				rep.Imported++
				continue
			}
			if dryRun {
				rep.Imported++
				continue
			}
			if _, err := st.AppendEvent(store.Event{TS: le.TS, Src: le.Src, Level: le.Level, Event: le.Event, Msg: le.Msg}); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: store error: %v", name, err))
				continue
			}
			rep.Imported++
		}
	}
	return rep, nil
}

// allEvents pages through every event in st via ListEvents' beforeID
// cursor — the importer's only caller that needs the entire table rather
// than one page.
func allEvents(st *store.Store) ([]store.Event, error) {
	const pageSize = 1000
	var out []store.Event
	var beforeID int64
	for {
		page, err := st.ListEvents(pageSize, beforeID)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		out = append(out, page...)
		beforeID = page[len(page)-1].ID
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

func eventKeyFromEntry(e util.LogEntry) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", e.TS, e.Src, e.Level, e.Event, e.Msg)
}

func eventKeyFromStore(e store.Event) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", e.TS, e.Src, e.Level, e.Event, e.Msg)
}

// --- feedback ---

// importFeedback walks PlutoDir()/doc-feedback/<day>/<entry>/ the same way
// SaveArtifactFeedback (feedback.go) lays it out: a snapshot.<ext> file and
// a feedback.txt whose header carries provenance and whose body (after the
// first blank line) is the verbatim note. Like events, feedback rows have
// no natural unique key, so idempotency uses the same pre-load-and-consume
// dedup by (job_slug, artifact, note, ts).
func importFeedback(st *store.Store, root string, dryRun bool) (StoreReport, error) {
	rep := StoreReport{}
	dayEntries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read %s: %w", root, err)
	}

	existing, err := st.ListFeedback("")
	if err != nil {
		return rep, err
	}
	consumed := make(map[string]int, len(existing))
	for _, f := range existing {
		consumed[feedbackKey(f.JobSlug, f.Artifact, f.Note, f.TS)]++
	}

	var days []string
	for _, d := range dayEntries {
		if d.IsDir() {
			days = append(days, d.Name())
		}
	}
	sort.Strings(days)
	for _, day := range days {
		dayPath := filepath.Join(root, day)
		entryEntries, err := os.ReadDir(dayPath)
		if err != nil {
			// unreadable is not the same thing as empty — report it rather
			// than silently importing zero entries for the day
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: read error: %v", day, err))
			continue
		}
		var entryNames []string
		for _, e := range entryEntries {
			if e.IsDir() {
				entryNames = append(entryNames, e.Name())
			}
		}
		sort.Strings(entryNames)
		for _, name := range entryNames {
			entryDir := filepath.Join(dayPath, name)
			rep.Found++
			f, err := parseFeedbackEntry(entryDir)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s/%s: %v", day, name, err))
				continue
			}
			key := feedbackKey(f.JobSlug, f.Artifact, f.Note, f.TS)
			if consumed[key] > 0 {
				consumed[key]--
				rep.Imported++
				continue
			}
			if dryRun {
				rep.Imported++
				continue
			}
			if _, err := st.AddFeedback(f); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s/%s: store error: %v", day, name, err))
				continue
			}
			rep.Imported++
		}
	}
	return rep, nil
}

// parseFeedbackEntry reads one claimed feedback dir into a store.Feedback:
// the header (time/job/artifact) parsed key-by-key so unknown/legacy
// header lines degrade rather than error, the note verbatim after the
// first blank line, and the snapshot file's bytes + extension. ts falls
// back to the dir's mtime when the header's time: is missing or unparsable
// — same degrade-never-error contract as everything else here.
func parseFeedbackEntry(entryDir string) (store.Feedback, error) {
	var f store.Feedback
	header, err := os.ReadFile(filepath.Join(entryDir, "feedback.txt"))
	if err != nil {
		return f, fmt.Errorf("read feedback.txt: %w", err)
	}
	head, note, _ := strings.Cut(string(header), "\n\n")
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "job":
			f.JobSlug = strings.TrimSpace(v)
		case "artifact":
			f.Artifact = strings.TrimSpace(v)
		case "time":
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
				f.TS = t.UnixMilli()
			}
		}
	}
	f.Note = note
	if f.TS == 0 {
		if info, err := os.Stat(entryDir); err == nil {
			f.TS = info.ModTime().UnixMilli()
		}
	}

	entries, err := os.ReadDir(entryDir)
	if err != nil {
		return f, fmt.Errorf("read entry dir: %w", err)
	}
	var snapPath string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "snapshot") {
			snapPath = filepath.Join(entryDir, e.Name())
			f.SnapshotExt = strings.TrimPrefix(strings.ToLower(filepath.Ext(e.Name())), ".")
			break
		}
	}
	if snapPath == "" {
		return f, fmt.Errorf("no snapshot file")
	}
	data, err := os.ReadFile(snapPath)
	if err != nil {
		return f, fmt.Errorf("read snapshot: %w", err)
	}
	f.Snapshot = data
	return f, nil
}

func feedbackKey(jobSlug, artifact, note string, ts int64) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", jobSlug, artifact, note, ts)
}
