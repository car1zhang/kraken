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
	ID           int64
	Slug         string
	Repo         string
	Base         string
	Ready        bool
	Solo         bool
	Model        string
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
	// Err (L6) mirrors file mode's ParseJob-derived parse-failure detail:
	// non-empty pins the job to "draft" in deriveState regardless of
	// ready/state, same as an unparseable job file does. Persisted by the
	// editor's store-mode save paths (api/jobedit.go) whenever the saved
	// content fails to parse; cleared the moment a later save parses clean.
	Err string
}

// Archived reports whether the job is in the archive column.
func (j *Job) Archived() bool { return j.ArchivedAt != 0 }

// jobColumns COALESCEs every column a plain (non-Null*) Go field scans
// into: the schema declares these NOT NULL DEFAULT already, but a NULL
// that sneaks in anyway (schema drift, a hand-edited row) must degrade to
// a blank field rather than error the whole query — repo rule: "degrade to
// blank fields, never errors". archived_at/archived_name are genuinely
// nullable and scanned via sql.Null* below, so they're left bare.
var jobColumns = `id, slug, COALESCE(repo,''), COALESCE(base,''), ready, solo,
	COALESCE(model,''), COALESCE(body,''), state, COALESCE(outcome,''),
	COALESCE(session,''), COALESCE(teardown,''), run, rev, COALESCE(created_at,0),
	COALESCE(updated_at,0), archived_at, archived_name, COALESCE(err,'')`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var archivedAt sql.NullInt64
	var archivedName sql.NullString
	err := row.Scan(
		&j.ID, &j.Slug, &j.Repo, &j.Base, &j.Ready, &j.Solo, &j.Model,
		&j.Body, &j.State, &j.Outcome, &j.Session, &j.Teardown,
		&j.Run, &j.Rev, &j.CreatedAt, &j.UpdatedAt, &archivedAt, &archivedName, &j.Err,
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
func (s *Store) CreateJob(j *Job) (int64, error) {
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
		`INSERT INTO jobs (slug, repo, base, ready, solo, model,
			body, state, outcome, session, teardown, run, rev, created_at, updated_at, err)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Slug, j.Repo, j.Base, j.Ready, j.Solo, j.Model,
		j.Body, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.Rev,
		j.CreatedAt, j.UpdatedAt, j.Err,
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
	rows, err := x.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
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
}

// LoadJobsWithChildren returns every active job (ListJobs' own query) plus
// every one's prereqs and PRs, ALL read inside one transaction (L12) — a
// single consistent snapshot, rather than the 1+2N separate queries
// (ListJobs, then ListPrereqs+ListPRs per job) LoadJobs used to issue,
// where a write landing between any two of them could make the loaded
// board mix state from two different moments (a job's own columns read
// from before a concurrent save, its prereqs from after, say). Read-only,
// so the transaction is always rolled back rather than committed — there's
// nothing to persist, and Rollback is cheaper than Commit for a query-only
// transaction.
func (s *Store) LoadJobsWithChildren() (rows []*Job, prereqs map[int64][]Prereq, prs map[int64][]PR, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load jobs with children: %w", err)
	}
	defer tx.Rollback()
	rows, prereqs, prs, err = loadJobsWithChildrenTx(tx, `SELECT `+jobColumns+` FROM jobs WHERE archived_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, nil, nil, err
	}
	return rows, prereqs, prs, nil
}

// LoadArchivedJobsWithChildren mirrors LoadJobsWithChildren for the
// archive: same single-transaction snapshot, ListArchivedJobs' own query
// and ordering.
func (s *Store) LoadArchivedJobsWithChildren() (rows []*Job, prereqs map[int64][]Prereq, prs map[int64][]PR, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load archived jobs with children: %w", err)
	}
	defer tx.Rollback()
	rows, prereqs, prs, err = loadJobsWithChildrenTx(tx, `SELECT `+jobColumns+` FROM jobs WHERE archived_at IS NOT NULL ORDER BY archived_name`)
	if err != nil {
		return nil, nil, nil, err
	}
	return rows, prereqs, prs, nil
}

// loadJobsWithChildrenTx is the shared body of LoadJobsWithChildren/
// LoadArchivedJobsWithChildren: run query against tx, then read every
// resulting row's prereqs and PRs, all within the same transaction.
func loadJobsWithChildrenTx(tx *sql.Tx, query string) ([]*Job, map[int64][]Prereq, map[int64][]PR, error) {
	rows, err := queryJobsTx(tx, query)
	if err != nil {
		return nil, nil, nil, err
	}
	prereqs := make(map[int64][]Prereq, len(rows))
	prs := make(map[int64][]PR, len(rows))
	for _, j := range rows {
		p, err := listPrereqsTx(tx, j.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		prereqs[j.ID] = p
		r, err := listPRsTx(tx, j.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		prs[j.ID] = r
	}
	return rows, prereqs, prs, nil
}

// jobFieldColumns is the set of columns UpdateFields is allowed to touch —
// an allowlist so a caller-supplied key can never smuggle arbitrary SQL
// into the generated statement.
var jobFieldColumns = map[string]bool{
	"slug": true, "repo": true, "base": true, "ready": true,
	"solo": true, "model": true, "body": true, "state": true,
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
// caller-owned transaction alongside replacePrereqsTx/PR-sync instead of
// its own implicit one — same SQL, same error contract, either way.
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
			`INSERT INTO jobs (slug, repo, base, ready, solo, model,
				body, state, outcome, session, teardown, run, rev, created_at, updated_at,
				archived_at, archived_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.Slug, j.Repo, j.Base, j.Ready, j.Solo, j.Model,
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
			`UPDATE jobs SET slug = ?, repo = ?, base = ?, ready = ?, solo = ?,
				model = ?, body = ?, state = ?, outcome = ?, session = ?,
				teardown = ?, run = ?, archived_at = ?, updated_at = ? WHERE id = ?`,
			j.Slug, j.Repo, j.Base, j.Ready, j.Solo, j.Model,
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
// E13: numbers off util.Slugify(slug), not the raw slug column — unifying
// with jobs.ArchiveJob's file-mode path (jobs/archive.go), which has always
// called freeArchiveName(util.Slugify(j.Slug)) for the same reason
// movePrefixed's artifact-move target uses util.Slugify(j.Slug) too: the
// archive name doubles as a filesystem-safe artifact-file prefix
// (<archived_name>.<name>), so it must be slugified exactly like the pluto
// flat-copy prefix already is. A raw slug and its slugified form only
// diverge for characters slugify strips/normalizes (spaces, caps,
// punctuation) that job slugs in practice never have — but there's no
// reason to leave the two paths disagreeing once unifying costs nothing.
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

// DeleteJob permanently removes a job row. Prereqs, PRs, uploads, and
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
