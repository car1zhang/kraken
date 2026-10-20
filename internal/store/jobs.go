package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"kraken/internal/util"
)

// Job mirrors jobs.Job's field shapes so board-building code ports with
// minimal churn, but is a store-local type: store must not import jobs
// (that would invert util ← store ← core ← jobs ← api ← web into a cycle).
type Job struct {
	ID    int64
	Slug  string
	Repo  string
	Base  string
	Solo  bool
	Model string
	Color string
	// Agent is which coding agent a cell's zeus runs — "" (the claude
	// default) or "codex". Part of the job's authored contract like Model, so
	// it joins jobFieldColumns and a form/raw save may set it (unlike the
	// account_* columns, which are cell-runtime state).
	Agent string
	// Visibility is who on the tailnet may see or drive this job (schema
	// v34): "private", "readonly" or "public". The column defaults to
	// readonly, which is what every row that predates it reads. It is in
	// jobFieldColumns, exactly as color is: hand-authorable in the job text
	// and landed by the raw editor's parse.
	Visibility   string
	Body         string
	State        string
	Outcome      string
	Session      string
	Teardown     string
	Run          int64
	Rev          int64
	CreatedAt    int64 // unix millis
	UpdatedAt    int64 // unix millis
	ArchivedAt   int64 // 0 means not archived
	ArchivedName string
	// Err (L6) is ParseJob's parse-failure detail for the last saved text:
	// non-empty pins the job to "draft" in deriveState regardless of
	// state. Persisted by the editor's save paths (api/jobedit.go)
	// whenever the saved content fails to parse; cleared the moment a
	// later save parses clean.
	Err string
	// Load is zeus' triage estimate for this job (schema v13's four
	// load_* columns), written through SetJobLoad and never through
	// UpdateJobFields — see both methods' doc comments.
	Load JobLoad
	// AccountSlug/AccountEmail/AccountAt are this job's account binding
	// (schema v28's three account_* columns): the pool slot every claude in
	// its cell bills, the identity that slot held when the binding was made,
	// and the millis stamp of the bind.
	//
	// All three empty/zero is the UNBOUND job — no per-cell credential
	// override, so its claudes use the machine's main login. That is the
	// state of every job that predates the feature, and the state a job stays
	// in when the pool is dry or the rotation mode is pinned manual, so it is
	// a normal reading rather than a missing one.
	//
	// Written ONLY by SetJobAccount, never by UpdateJobFields (the columns are
	// deliberately absent from jobFieldColumns) — see SetJobAccount's doc
	// comment for why a form save must not be able to touch a binding.
	AccountSlug  string
	AccountEmail string
	AccountAt    int64 // unix millis; 0 means never bound
	// FormerSlugs is every name this job has been renamed away from (schema
	// v20's job_former_slugs), empty for the never-renamed job that every job
	// is until someone renames it. Order is unspecified: nothing ranks them,
	// and the resolvers that use them try all of them.
	//
	// It is filled by the row loads below rather than scanned, since it is a
	// second table — attachFormerSlugs (rename.go) is the one place that reads
	// it, so a load path that forgets it is a job whose old names silently
	// stop resolving rather than a compile error.
	FormerSlugs []string
	// GoalIDs is every goal this job serves (schema v27's job_goals rows), in
	// link position order, and empty for the job that serves none — the normal
	// state of every job, and what a job degrades to when the goals it pointed
	// at are deleted (schemaV27's ON DELETE CASCADE takes the rows with them).
	//
	// The head of the slice is the PRIMARY link: the wire's `goal` is `goals[0]`
	// durably (internal/api/goals.go), which is only a promise the store can
	// keep because job_goals.position orders the rows.
	//
	// Written ONLY by AddJobGoal/RemoveJobGoal/ClearJobGoals (jobgoals.go),
	// never by UpdateJobFields — which cannot reach it at all now, since the
	// link is a second TABLE rather than a column an allowlist has to exclude.
	// v23's goal_id needed that exclusion (a full form or raw save replaces
	// every field it knows about, so a client predating the picker would have
	// cleared the link); the join table gets the same guarantee by construction.
	//
	// It is filled by the row loads below rather than scanned, FormerSlugs'
	// situation exactly and with the same hazard: one attach helper
	// (attachJobGoals), N call sites, and a loader that forgets it is a job
	// whose chips silently vanish on one surface and not the others.
	GoalIDs []int64
}

// JobLoad is one job's triage estimate: how much effort unblocking it costs
// carl (Unblock) against how much claude work that reply buys (Scope), both
// 1-10, plus a one-line rationale and the millis stamp of the write. Both
// zero is the "never estimated" state — a HALF estimate is meaningless
// (scope/unblock has no value when one side is zero) and is refused by the
// API layer before it can reach a row.
//
// A named struct rather than four loose parameters on SetJobLoad: the four
// only ever travel together, and a positional (int, int, string, int64) is
// exactly the shape a caller silently swaps two arguments in.
//
// The ratio the board renders is deliberately absent — scope/unblock is
// derived wherever it is shown, never stored, for the reason schemaV13's
// doc comment gives.
type JobLoad struct {
	Unblock int
	Scope   int
	Note    string
	At      int64 // unix millis; 0 means never estimated
}

// Archived reports whether the job is in the archive column.
func (j *Job) Archived() bool { return j.ArchivedAt != 0 }

// jobColumns COALESCEs every column a plain (non-Null*) Go field scans
// into: the schema declares these NOT NULL DEFAULT already, but a NULL
// that sneaks in anyway (schema drift, a hand-edited row) must degrade to
// a blank field rather than error the whole query — repo rule: "degrade to
// blank fields, never errors". archived_at/archived_name are genuinely
// nullable and scanned via sql.Null* below, so they're left bare.
var jobColumns = `id, slug, COALESCE(repo,''), COALESCE(base,''), solo,
	COALESCE(model,''), COALESCE(color,''), COALESCE(body,''), state, COALESCE(outcome,''),
	COALESCE(session,''), COALESCE(teardown,''), run, rev, COALESCE(created_at,0),
	COALESCE(updated_at,0), archived_at, archived_name, COALESCE(err,''),
	COALESCE(load_unblock,0), COALESCE(load_scope,0), COALESCE(load_note,''),
	COALESCE(load_at,0), COALESCE(account_slug,''), COALESCE(account_email,''),
	COALESCE(account_at,0), COALESCE(agent,''), COALESCE(visibility,'readonly')`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var archivedAt sql.NullInt64
	var archivedName sql.NullString
	err := row.Scan(
		&j.ID, &j.Slug, &j.Repo, &j.Base, &j.Solo, &j.Model, &j.Color,
		&j.Body, &j.State, &j.Outcome, &j.Session, &j.Teardown,
		&j.Run, &j.Rev, &j.CreatedAt, &j.UpdatedAt, &archivedAt, &archivedName, &j.Err,
		&j.Load.Unblock, &j.Load.Scope, &j.Load.Note, &j.Load.At,
		&j.AccountSlug, &j.AccountEmail, &j.AccountAt, &j.Agent, &j.Visibility,
	)
	if err != nil {
		return nil, err
	}
	j.ArchivedAt = archivedAt.Int64
	j.ArchivedName = archivedName.String
	return &j, nil
}

// CreateJob inserts a new active job. Run defaults to 1 and Rev to 1 if the
// caller left them zero; CreatedAt/UpdatedAt default to now if zero. The
// job's assigned ID is returned; Slug must be free among active jobs
// (enforced by jobs_active_slug) or this returns the underlying UNIQUE
// constraint error.
//
// It must ALSO be free of every active job's FORMER slugs (schema v20), and
// that reservation is this method's, not the index's — SQLite cannot express
// "unique across two tables". Without it a new job could take a name a live
// job was renamed away from, and both would then resolve through the same old
// name: the renamed job's own alias fallback would start answering with the
// new job, which is the collision every id-scoped name in this codebase exists
// to prevent, reintroduced through the one door ids do not cover.
//
// The reservation LAPSES with its owner, exactly as the slug itself does: an
// ARCHIVED job's former slugs reserve nothing, since archiving frees the
// current slug too.
func (s *Store) CreateJob(j *Job) (int64, error) {
	if taken, err := formerSlugTakenBy(s.db, j.Slug, 0); err != nil {
		return 0, fmt.Errorf("create job %s: %w", j.Slug, err)
	} else if taken != 0 {
		return 0, fmt.Errorf("create job %s: %q is a former name of job %d: %w", j.Slug, j.Slug, taken, ErrSlugTaken)
	}
	now := time.Now().UnixMilli()
	if j.CreatedAt == 0 {
		j.CreatedAt = now
	}
	if j.UpdatedAt == 0 {
		j.UpdatedAt = now
	}
	if j.Run == 0 {
		j.Run = 1
	}
	if j.Rev == 0 {
		j.Rev = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO jobs (slug, repo, base, solo, model, color, agent,
			body, state, outcome, session, teardown, run, rev, created_at, updated_at, err,
			visibility)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), 'readonly'))`,
		j.Slug, j.Repo, j.Base, j.Solo, j.Model, j.Color, j.Agent,
		j.Body, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.Rev,
		j.CreatedAt, j.UpdatedAt, j.Err, j.Visibility,
	)
	if err != nil {
		return 0, fmt.Errorf("create job %s: %w", j.Slug, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create job %s: %w", j.Slug, err)
	}
	j.ID = id
	return id, nil
}

// GetJobBySlug looks up an active (non-archived) job by slug.
func (s *Store) GetJobBySlug(slug string) (*Job, error) {
	row := s.db.QueryRow(
		`SELECT `+jobColumns+` FROM jobs WHERE slug = ? AND archived_at IS NULL`,
		slug,
	)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job by slug %s: %w", slug, err)
	}
	former, err := formerSlugsFor(s.db, j.ID)
	if err != nil {
		return nil, err
	}
	j.FormerSlugs = former
	if err := attachJobGoals(s.db, []*Job{j}); err != nil {
		return nil, err
	}
	return j, nil
}

// GetJobByID looks up a job (active or archived) by id.
func (s *Store) GetJobByID(id int64) (*Job, error) {
	row := s.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job by id %d: %w", id, err)
	}
	former, err := formerSlugsFor(s.db, j.ID)
	if err != nil {
		return nil, err
	}
	j.FormerSlugs = former
	if err := attachJobGoals(s.db, []*Job{j}); err != nil {
		return nil, err
	}
	return j, nil
}

// ListJobs returns every active (non-archived) job, ordered by id (i.e.
// creation order).
func (s *Store) ListJobs() ([]*Job, error) {
	return s.queryJobs(`SELECT ` + jobColumns + ` FROM jobs WHERE archived_at IS NULL ORDER BY id`)
}

// ListArchivedJobs returns every archived job, ordered by archived_name —
// same ordering LoadArchivedJobs uses today.
func (s *Store) ListArchivedJobs() ([]*Job, error) {
	return s.queryJobs(`SELECT ` + jobColumns + ` FROM jobs WHERE archived_at IS NOT NULL ORDER BY archived_name`)
}

func (s *Store) queryJobs(query string, args ...any) ([]*Job, error) {
	return queryJobsTx(s.db, query, args...)
}

// queryJobsTx is queryJobs' body, written against execer so
// LoadJobsWithChildren (L12) can compose it inside a caller-owned read
// transaction.
func queryJobsTx(x execer, query string, args ...any) ([]*Job, error) {
	out, err := func() ([]*Job, error) {
		rows, err := x.Query(query, args...)
		if err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}
		// closed before the follow-up query below rather than deferred to the
		// end: x may be a *sql.Tx, which holds ONE connection, so issuing a
		// second query while these rows are still open is a self-deadlock
		// waiting to happen rather than a second round trip.
		defer rows.Close()
		var out []*Job
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return nil, fmt.Errorf("scan job: %w", err)
			}
			out = append(out, j)
		}
		return out, rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	// former slugs come from a second table, so they are attached here rather
	// than scanned — one query for the whole result set. Every multi-row load
	// in this package funnels through this function (ListJobs,
	// ListArchivedJobs, loadJobsWithChildrenTx), which is what keeps the board
	// from being the one board whose renamed jobs forget their old names.
	if err := attachFormerSlugs(x, out); err != nil {
		return nil, err
	}
	// goal links come from a second table too (schema v27's job_goals), and
	// they are attached here for exactly the reason the former slugs above are:
	// every multi-row load in this package funnels through this function, so
	// one call here is what keeps the board from being the one surface whose
	// cards forget which goals they serve.
	if err := attachJobGoals(x, out); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadJobsWithChildren returns every active job (ListJobs' own query) plus
// every one's PRs, ALL read inside one transaction (L12) — a single
// consistent snapshot, rather than the 1+N separate queries (ListJobs, then
// ListPRs per job) LoadJobs used to issue, where a write landing between any
// two of them could make the loaded board mix state from two different
// moments (a job's own columns read from before a concurrent save, its PRs
// from after, say). Read-only, so the transaction is always rolled back
// rather than committed — there's nothing to persist, and Rollback is
// cheaper than Commit for a query-only transaction.
func (s *Store) LoadJobsWithChildren() (rows []*Job, prs map[int64][]PR, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("load jobs with children: %w", err)
	}
	defer tx.Rollback()
	rows, prs, err = loadJobsWithChildrenTx(tx, `SELECT `+jobColumns+` FROM jobs WHERE archived_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	return rows, prs, nil
}

// LoadArchivedJobsWithChildren mirrors LoadJobsWithChildren for the
// archive: same single-transaction snapshot, ListArchivedJobs' own query
// and ordering.
func (s *Store) LoadArchivedJobsWithChildren() (rows []*Job, prs map[int64][]PR, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("load archived jobs with children: %w", err)
	}
	defer tx.Rollback()
	rows, prs, err = loadJobsWithChildrenTx(tx, `SELECT `+jobColumns+` FROM jobs WHERE archived_at IS NOT NULL ORDER BY archived_name`)
	if err != nil {
		return nil, nil, err
	}
	return rows, prs, nil
}

// loadJobsWithChildrenTx is the shared body of LoadJobsWithChildren/
// LoadArchivedJobsWithChildren: run query against tx, then read every
// resulting row's PRs, all within the same transaction.
func loadJobsWithChildrenTx(tx *sql.Tx, query string) ([]*Job, map[int64][]PR, error) {
	rows, err := queryJobsTx(tx, query)
	if err != nil {
		return nil, nil, err
	}
	prs := make(map[int64][]PR, len(rows))
	for _, j := range rows {
		r, err := listPRsTx(tx, j.ID)
		if err != nil {
			return nil, nil, err
		}
		prs[j.ID] = r
	}
	return rows, prs, nil
}

// jobFieldColumns is the set of columns UpdateFields is allowed to touch —
// an allowlist so a caller-supplied key can never smuggle arbitrary SQL
// into the generated statement.
var jobFieldColumns = map[string]bool{
	"slug": true, "repo": true, "base": true,
	"solo": true, "model": true, "color": true, "agent": true, "visibility": true, "body": true, "state": true,
	"outcome": true, "session": true, "teardown": true, "run": true, "err": true,
}

// UpdateJobFields applies a partial update to job id, gated on rev matching
// exactly — the same optimistic-concurrency check the old mtime comparison
// made. On success it bumps rev by 1 and stamps updated_at, and returns the
// new rev. Archived rows are immutable through this path (an archived job
// is only ever touched via UnarchiveJob first). Every key in fields must be
// in jobFieldColumns.
//
// On a zero-row update it disambiguates rather than always blaming rev:
// ErrNotFound if the row is gone (or archived), ErrStaleRev only if the row
// exists, is active, and simply has a different rev. A slug collision
// (only reachable via a "slug" field update) comes back as ErrSlugTaken
// instead of a raw driver error.
func (s *Store) UpdateJobFields(id, rev int64, fields map[string]any) (int64, error) {
	return updateJobFieldsTx(s.db, id, rev, fields)
}

// updateJobFieldsTx is UpdateJobFields' actual body, written against the
// execer interface (store.go) so SaveJobEdit (L10) can run it inside a
// caller-owned transaction alongside the PR sync instead of its own implicit
// one — same SQL, same error contract, either way.
func updateJobFieldsTx(x execer, id, rev int64, fields map[string]any) (int64, error) {
	if len(fields) == 0 {
		return 0, fmt.Errorf("update job %d: no fields given", id)
	}
	// Deterministic column order keeps the generated SQL (and therefore
	// test expectations / debugging) stable across runs.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if !jobFieldColumns[k] {
			return 0, fmt.Errorf("update job %d: field %q is not updatable", id, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	setClause := ""
	args := make([]any, 0, len(keys)+3)
	for _, k := range keys {
		setClause += k + " = ?, "
		args = append(args, fields[k])
	}
	now := time.Now().UnixMilli()
	setClause += "rev = rev + 1, updated_at = ?"
	args = append(args, now, id, rev)

	res, err := x.Exec(
		`UPDATE jobs SET `+setClause+` WHERE id = ? AND rev = ? AND archived_at IS NULL`,
		args...,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, fmt.Errorf("update job %d: %w", id, ErrSlugTaken)
		}
		return 0, fmt.Errorf("update job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("update job %d: %w", id, err)
	}
	if n == 0 {
		var archivedAt sql.NullInt64
		lookupErr := x.QueryRow(`SELECT archived_at FROM jobs WHERE id = ?`, id).Scan(&archivedAt)
		switch {
		case errors.Is(lookupErr, sql.ErrNoRows):
			return 0, fmt.Errorf("update job %d: %w", id, ErrNotFound)
		case lookupErr != nil:
			return 0, fmt.Errorf("update job %d: %w", id, lookupErr)
		case archivedAt.Valid:
			return 0, fmt.Errorf("update job %d: archived job is immutable: %w", id, ErrNotFound)
		default:
			return 0, fmt.Errorf("update job %d at rev %d: %w", id, rev, ErrStaleRev)
		}
	}
	return rev + 1, nil
}

// SetJobLoad writes job jobID's triage estimate (schema v13's four load_*
// columns) — a direct UPDATE, deliberately OUTSIDE UpdateJobFields and
// therefore outside its rev gate: this call does NOT bump `rev` and does NOT
// stamp `updated_at`, exactly as SetPRWatermark does not, and for the same
// class of reason.
//
// Not bumping updated_at, first: zeus rewrites this estimate at the end of
// every turn, and updated_at is what the board renders as each card's AGE.
// Stamping it here would reset every blocked card's age on every zeus turn,
// so the one column carl uses to see which job has been sitting longest would
// track how recently an agent typed rather than how long the job has waited.
//
// Not bumping rev, second: rev is the editor's optimistic lock
// (UpdateJobFields), so bumping it would 409 an open job-form save over a
// number that form neither shows nor sends. The estimate is not part of the
// job's authored contract — it is bookkeeping about the job, the same
// category as a PR watermark.
//
// A zero-row update is an ERROR here rather than SetPRWatermark's no-op. Those
// are different situations: a watermark's URL can legitimately vanish between
// its caller's load and the write, whereas a job id that matches no row means
// the caller was addressing something that does not exist — and this write's
// caller is a CLI verb whose whole output is a confirmation line, so a silent
// no-op would print success for a write that never happened.
func (s *Store) SetJobLoad(jobID int64, l JobLoad) error {
	res, err := s.db.Exec(
		`UPDATE jobs SET load_unblock = ?, load_scope = ?, load_note = ?, load_at = ? WHERE id = ?`,
		l.Unblock, l.Scope, l.Note, l.At, jobID,
	)
	if err != nil {
		return fmt.Errorf("set job load for job %d: %w", jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set job load for job %d: %w", jobID, err)
	}
	if n == 0 {
		return fmt.Errorf("set job load for job %d: %w", jobID, ErrNotFound)
	}
	return nil
}

// SetJobAccount writes job id's account binding (schema v28's three account_*
// columns): the pool slot its cell bills, that slot's identity at bind time,
// and the millis stamp. All three are overwritten together — a REBIND is an
// overwrite, and clearing a binding is the same call with the zero values, so
// there is no separate clear verb to keep in step with this one.
//
// A direct UPDATE, deliberately OUTSIDE UpdateJobFields and therefore outside
// its rev gate: this call bumps NEITHER `rev` NOR `updated_at`, exactly as
// SetJobLoad and SetPRWatermark do not, and for the same class of reason.
//
// Not bumping updated_at, first: updated_at is what the board renders as each
// card's AGE, and a binding is written at every spawn and again at every
// limit-rescue rebind — stamping it here would reset a long-running job's age
// on an event that says nothing about how long the job has waited.
//
// Not bumping rev, second: rev is the job editor's optimistic lock
// (UpdateJobFields), so bumping it would 409 an open form save over a field
// that form neither shows nor sends. The binding is bookkeeping about the
// job's runtime, not part of its authored contract — which is also why the
// three columns are absent from jobFieldColumns: a full form or raw save from
// a client predating the feature would otherwise clear a LIVE cell's binding,
// leaving the row claiming main while its claudes still bill the slot.
//
// A zero-row update is an ERROR rather than a silent no-op, SetJobLoad's rule
// and reason: a job id matching no row means the caller was addressing
// something that does not exist, and the binder's next move is to stamp the
// same values onto its in-memory job and spawn against them, so a swallowed
// miss would spawn a cell whose binding no row records.
func (s *Store) SetJobAccount(id int64, slug, email string, at int64) error {
	res, err := s.db.Exec(
		`UPDATE jobs SET account_slug = ?, account_email = ?, account_at = ? WHERE id = ?`,
		slug, email, at, id,
	)
	if err != nil {
		return fmt.Errorf("set job account for job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set job account for job %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set job account for job %d: %w", id, ErrNotFound)
	}
	return nil
}

// CreateArchivedJob upserts one archived-job row keyed on ArchivedName — the
// numbered file base that's the only stable identity among archived jobs
// (unlike Slug, which can collide across two archived runs of the same job
// name). This is the importer's write path for archive-dir files, which are
// read straight off disk and never went through the active table first, so
// ArchiveJob (which only moves an already-active row) doesn't fit: an INSERT
// stamping archived_at/archived_name from row one avoids the transient
// jobs_active_slug collision an insert-as-active-then-archive two-step would
// risk when an active job already holds the same slug.
//
// If a row with this ArchivedName already exists, its fields are updated in
// place and its id returned; otherwise a fresh archived row is inserted with
// rev 1. An update never bumps rev or touches created_at — archived rows
// have no optimistic-concurrency contract (nothing else writes them
// concurrently), and leaving rev/created_at alone is what lets a re-import
// of an unchanged archive file leave the row exactly as it was.
func (s *Store) CreateArchivedJob(j *Job) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM jobs WHERE archived_name = ? AND archived_at IS NOT NULL`,
		j.ArchivedName,
	).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		archivedAt := j.ArchivedAt
		if archivedAt == 0 {
			archivedAt = time.Now().UnixMilli()
		}
		created := j.CreatedAt
		if created == 0 {
			created = archivedAt
		}
		rev := j.Rev
		if rev == 0 {
			rev = 1
		}
		run := j.Run
		if run == 0 {
			run = 1
		}
		res, err := s.db.Exec(
			`INSERT INTO jobs (slug, repo, base, solo, model, color, agent,
				body, state, outcome, session, teardown, run, rev, created_at, updated_at,
				archived_at, archived_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.Slug, j.Repo, j.Base, j.Solo, j.Model, j.Color, j.Agent,
			j.Body, j.State, j.Outcome, j.Session, j.Teardown, run,
			rev, created, archivedAt, archivedAt, j.ArchivedName,
		)
		if err != nil {
			return 0, fmt.Errorf("create archived job %s: %w", j.ArchivedName, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("create archived job %s: %w", j.ArchivedName, err)
		}
		return id, nil
	case err != nil:
		return 0, fmt.Errorf("create archived job %s: %w", j.ArchivedName, err)
	default:
		archivedAt := j.ArchivedAt
		if archivedAt == 0 {
			archivedAt = time.Now().UnixMilli()
		}
		run := j.Run
		if run == 0 {
			run = 1
		}
		if _, err := s.db.Exec(
			`UPDATE jobs SET slug = ?, repo = ?, base = ?, solo = ?,
				model = ?, color = ?, agent = ?, body = ?, state = ?, outcome = ?, session = ?,
				teardown = ?, run = ?, archived_at = ?, updated_at = ? WHERE id = ?`,
			j.Slug, j.Repo, j.Base, j.Solo, j.Model, j.Color, j.Agent,
			j.Body, j.State, j.Outcome, j.Session, j.Teardown, run,
			archivedAt, archivedAt, id,
		); err != nil {
			return 0, fmt.Errorf("create archived job %s: %w", j.ArchivedName, err)
		}
		return id, nil
	}
}

// isUniqueConstraintErr reports whether err is a SQLite UNIQUE constraint
// violation. UpdateJobFields only ever writes to jobFieldColumns, and the
// only unique index touching those columns is jobs_active_slug, so any
// unique-constraint error from that statement is necessarily a slug
// collision.
func isUniqueConstraintErr(err error) bool {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrConstraint && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique
	}
	return false
}

// ArchiveJob moves job id into the archive column: stamps archived_at and
// picks a free archived_name (the job's own slug, or slug-2, slug-3, ...
// when taken — same numbering as jobs.freeArchiveName), which frees the
// slug for reuse by a new active job. Returns the assigned archived_name.
//
// E13: numbers off util.Slugify(slug), not the raw slug column, so the
// archived name is the same normalized form every other derived name (cell
// dir, branch, session) is built from. A raw slug and its slugified form
// only diverge for characters slugify strips/normalizes (spaces, caps,
// punctuation) that job slugs in practice never have.
func (s *Store) ArchiveJob(id int64) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("archive job %d: %w", id, err)
	}
	defer tx.Rollback()

	var rawSlug string
	if err := tx.QueryRow(`SELECT slug FROM jobs WHERE id = ? AND archived_at IS NULL`, id).Scan(&rawSlug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("archive job %d: %w", id, ErrNotFound)
		}
		return "", fmt.Errorf("archive job %d: %w", id, err)
	}
	slug := util.Slugify(rawSlug)

	name := slug
	for n := 2; ; n++ {
		var exists int
		err := tx.QueryRow(`SELECT 1 FROM jobs WHERE archived_name = ? AND archived_at IS NOT NULL`, name).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("archive job %d: %w", id, err)
		}
		name = fmt.Sprintf("%s-%d", slug, n)
	}

	now := time.Now().UnixMilli()
	if _, err := tx.Exec(
		`UPDATE jobs SET archived_at = ?, archived_name = ?, rev = rev + 1, updated_at = ? WHERE id = ?`,
		now, name, now, id,
	); err != nil {
		return "", fmt.Errorf("archive job %d: %w", id, err)
	}
	// B2: feedback rows display-key on job_slug, so archiving must re-key
	// every row from the job's active slug to the numbered archive name in
	// the SAME transaction as the move, or a card's feedback either vanishes
	// (a new job later reuses the freed slug) or cross-wires onto that new
	// job. The WHERE clause matches on job_id (schema v5), not job_slug: a
	// job_slug string match would move BOTH generations' rows the moment a
	// freed slug is reused, since the FIRST archival of any slug keeps the
	// bare slug as its archived_name (no numbering needed yet) — so a
	// same-named active job's live rows and this job's already-archived
	// rows share an identical job_slug string until job_id disambiguates
	// them. id is jobs' own primary key, immune to that collision.
	if _, err := tx.Exec(`UPDATE feedback SET job_slug = ? WHERE job_id = ?`, name, id); err != nil {
		return "", fmt.Errorf("archive job %d: rekey feedback: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("archive job %d: %w", id, err)
	}
	return name, nil
}

// UnarchiveJob moves an archived job back into the active set under its
// original slug, refusing when that slug is already taken by another
// active job — same contract as jobs.UnarchiveJob.
func (s *Store) UnarchiveJob(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("unarchive job %d: %w", id, err)
	}
	defer tx.Rollback()

	var slug string
	if err := tx.QueryRow(`SELECT slug FROM jobs WHERE id = ? AND archived_at IS NOT NULL`, id).Scan(&slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("unarchive job %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("unarchive job %d: %w", id, err)
	}

	var taken int
	err = tx.QueryRow(`SELECT 1 FROM jobs WHERE slug = ? AND archived_at IS NULL`, slug).Scan(&taken)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("unarchive job %d: %w", id, err)
	}
	if err == nil {
		return fmt.Errorf("unarchive job %d (slug %s): %w", id, slug, ErrSlugTaken)
	}
	// The same refusal against an active job's FORMER slugs, and it closes the
	// one remaining door into the two-active-jobs-answer-one-name state that
	// CreateJob's and RenameJob's reservations otherwise seal. Reachable
	// entirely through supported board actions: archive B holding "foo",
	// create A as "foo" (legal — jobs_active_slug is partial over active
	// rows), rename A to "bar" so "foo" becomes A's former slug, unarchive B.
	// Nothing above notices, because no ACTIVE job holds "foo" — and from then
	// on A's live cell's baked hook commands (kraken pr-hook foo, kraken
	// limit-hook foo, k load set foo) resolve to B, cross-attributing A's PR,
	// limit-hit and load records to a stranger, silently.
	formerOwner, err := formerSlugTakenBy(tx, slug, id)
	if err != nil {
		return fmt.Errorf("unarchive job %d: %w", id, err)
	}
	if formerOwner != 0 {
		return fmt.Errorf("unarchive job %d: slug %s is taken by an active job (%d) as a former name: %w", id, slug, formerOwner, ErrSlugTaken)
	}

	now := time.Now().UnixMilli()
	if _, err := tx.Exec(
		`UPDATE jobs SET archived_at = NULL, archived_name = NULL, rev = rev + 1, updated_at = ? WHERE id = ?`,
		now, id,
	); err != nil {
		return fmt.Errorf("unarchive job %d: %w", id, err)
	}
	// B2's reverse direction: re-key feedback rows from the archive name
	// back to the restored active slug, matched by job_id for the same
	// collision-immunity reason as ArchiveJob's own rekey above.
	if _, err := tx.Exec(`UPDATE feedback SET job_slug = ? WHERE job_id = ?`, slug, id); err != nil {
		return fmt.Errorf("unarchive job %d: rekey feedback: %w", id, err)
	}
	return tx.Commit()
}

// DeleteJob permanently removes a job row. PRs, uploads, and
// artifacts cascade via their FK ON DELETE CASCADE; events/drafts key on
// job_slug only and are wholly untouched, same as the file store today (an
// archived job's fossil events aren't deleted with it). feedback carries
// job_id too (schema v5, ON DELETE SET NULL — used only to make archive/
// unarchive's re-key unambiguous, see ArchiveJob/UnarchiveJob), so a
// feedback row survives its job's hard-delete exactly like an event does;
// only its now-meaningless job_id column nulls out, job_slug/note untouched.
func (s *Store) DeleteJob(id int64) error {
	res, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete job %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("delete job %d: %w", id, ErrNotFound)
	}
	return nil
}
