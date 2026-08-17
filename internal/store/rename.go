package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// rename.go — the slug half of a job's identity, and the record of every name
// it has held before.
//
// jobs.id is the immutable identity everything below the HTTP handler keys on
// (schema v12), so a rename is deliberately a small change: one column on one
// row, plus the one place that still stores the SLUG as a reference
// (feedback's display key). What makes it more than an UPDATE is
// the FORMER-SLUG row, written in the same transaction: a job's runtime names
// are derived from its slug and most of them cannot be moved (see schemaV20),
// so every resolver keeps probing the old ones and that probing needs a record.

// formerSlugsFor reads one job's former slugs. Order is unspecified — no
// reader ranks them, and the two that iterate (the cell-dir and branch-family
// probes) try all of them.
func formerSlugsFor(x execer, id int64) ([]string, error) {
	rows, err := x.Query(`SELECT slug FROM job_former_slugs WHERE job_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("former slugs for job %d: %w", id, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("former slugs for job %d: %w", id, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// attachFormerSlugs fills FormerSlugs on every row in jobs using ONE query
// rather than one per job. It reads the WHOLE table and filters in Go on
// purpose, and the reason is the table's SHAPE rather than a measurement:
// job_former_slugs gains a row only when a job is renamed, so its row count is
// bounded by the number of renames ever performed rather than by anything that
// grows per tick — and the alternatives both scale with the CALL instead. An
// N-query loop issues one round trip per job on a pool capped at a single
// connection; an IN-list has to be rebuilt and re-planned for every board
// load, which is the hottest read in the process. One unparameterised
// statement is the only shape that is constant in the number of jobs.
func attachFormerSlugs(x execer, jobs []*Job) error {
	if len(jobs) == 0 {
		return nil
	}
	byID := make(map[int64]*Job, len(jobs))
	for _, j := range jobs {
		byID[j.ID] = j
	}
	rows, err := x.Query(`SELECT job_id, slug FROM job_former_slugs`)
	if err != nil {
		return fmt.Errorf("attach former slugs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return fmt.Errorf("attach former slugs: %w", err)
		}
		if j := byID[id]; j != nil {
			j.FormerSlugs = append(j.FormerSlugs, slug)
		}
	}
	return rows.Err()
}

// formerSlugTakenBy reports the id of the ACTIVE job that already holds slug
// as a former slug, or 0. exceptID is skipped: a job's OWN former slugs are
// not a reservation against itself, since renaming back to an earlier name is
// legal and is the whole reason the rows are kept.
//
// ARCHIVED owners never match, deliberately: archiving frees the slug itself
// (jobs_active_slug is a partial index over active rows only), so a
// reservation that outlived the job would reserve more than the name it is
// protecting.
func formerSlugTakenBy(x execer, slug string, exceptID int64) (int64, error) {
	var id int64
	err := x.QueryRow(
		`SELECT f.job_id FROM job_former_slugs f
			JOIN jobs j ON j.id = f.job_id
			WHERE f.slug = ? AND f.job_id != ? AND j.archived_at IS NULL
			LIMIT 1`,
		slug, exceptID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetJobByFormerSlug resolves a name a job has been renamed away from to the
// ACTIVE job that owns it. Archived owners never match, for the reason
// formerSlugTakenBy states. A miss is ErrNotFound — the same shape
// GetJobBySlug returns — so a caller that tries the current-slug lookup first
// and falls back here handles one error kind, not two.
//
// The ORDER BY only decides a case the reservation in CreateJob/RenameJob now
// prevents: two active jobs sharing one former slug, reachable for rows
// written before that reservation existed. The most recent rename wins, since
// the older claim is the one more likely to have been superseded by hand.
func (s *Store) GetJobByFormerSlug(slug string) (*Job, error) {
	row := s.db.QueryRow(
		`SELECT `+jobColumns+` FROM jobs WHERE archived_at IS NULL AND id = (
			SELECT f.job_id FROM job_former_slugs f
				JOIN jobs j2 ON j2.id = f.job_id
				WHERE f.slug = ? AND j2.archived_at IS NULL
				ORDER BY f.renamed_at DESC, f.job_id DESC
				LIMIT 1)`,
		slug,
	)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job by former slug %s: %w", slug, err)
	}
	former, err := formerSlugsFor(s.db, j.ID)
	if err != nil {
		return nil, err
	}
	j.FormerSlugs = former
	// Goal links come from a second table too (schema v27's job_goals) and are
	// attached through the SAME helper the other three scanJob callers use. This
	// loader is the one that gets forgotten — it lives in rename.go rather than
	// jobs.go — and forgetting it is silent: a renamed job resolved by its old
	// name would serve no chips at all while the same job resolved by its
	// current one served them, which is exactly the split FormerSlugs itself
	// exists to prevent.
	if err := attachJobGoals(s.db, []*Job{j}); err != nil {
		return nil, err
	}
	return j, nil
}

// RenameJob gives job id a new slug, gated on rev exactly as UpdateJobFields
// is, and records the old one as a former slug. `to` arrives ALREADY
// SLUGIFIED: the jobs layer owns that normalisation (it is what every derived
// runtime name is built from), and a store that slugified again would be a
// second opinion on the one string both layers must agree about.
//
// ONE transaction, all-or-nothing, because the four statements below — the
// slug UPDATE, the feedback re-key, the former-slug upsert and the
// former-slug DELETE — are one fact. A committed slug change with an
// uncommitted former-slug row is the worst outcome available here: the job
// answers to its new name, nothing answers to its old one, and its live cell
// dir, tmux session and branch — all still named for the old slug — are
// orphaned with no record that they belong to it.
//
// The feedback re-key follows ArchiveJob's own rule, which is about WHAT is
// matched rather than about SQL: feedback is matched on job_id, never on the
// slug string, because a freed-and-reused slug makes two generations' rows
// indistinguishable (see ArchiveJob's comment). (A fifth statement re-keyed
// peer jobs' prereq_slug references until schema v20 dropped the prereqs
// table with the queued status — the rebase that joined the two features
// removed it here.)
//
// A no-op (to == the current slug) returns the CURRENT rev without bumping it,
// writing a former row, or checking anything else. That is what makes the
// jobs layer's no-op gate honest: a rename to the name a job already has must
// not 409 an open editor over a write that changed nothing.
func (s *Store) RenameJob(id, rev int64, to string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("rename job %d: %w", id, err)
	}
	defer tx.Rollback()

	var oldSlug string
	var curRev int64
	if err := tx.QueryRow(`SELECT slug, rev FROM jobs WHERE id = ?`, id).Scan(&oldSlug, &curRev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("rename job %d: %w", id, ErrNotFound)
		}
		return 0, fmt.Errorf("rename job %d: %w", id, err)
	}
	if oldSlug == to {
		return curRev, nil
	}

	// the reservation, read INSIDE the transaction so it cannot be raced by a
	// concurrent rename onto the same name
	taken, err := formerSlugTakenBy(tx, to, id)
	if err != nil {
		return 0, fmt.Errorf("rename job %d: %w", id, err)
	}
	if taken != 0 {
		return 0, fmt.Errorf("rename job %d: %q is a former name of job %d: %w", id, to, taken, ErrSlugTaken)
	}

	now := time.Now().UnixMilli()
	res, err := tx.Exec(
		`UPDATE jobs SET slug = ?, rev = rev + 1, updated_at = ? WHERE id = ? AND rev = ? AND archived_at IS NULL`,
		to, now, id, rev,
	)
	if err != nil {
		// jobs_active_slug is the only unique index over jobs.slug, so a
		// unique-constraint failure from this statement can only be a
		// collision with another ACTIVE job's slug. It is the race backstop
		// behind the jobs layer's own pre-check, not the primary refusal.
		if isUniqueConstraintErr(err) {
			return 0, fmt.Errorf("rename job %d: %w", id, ErrSlugTaken)
		}
		return 0, fmt.Errorf("rename job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rename job %d: %w", id, err)
	}
	if n == 0 {
		// same disambiguation UpdateJobFields makes, and for the same reason:
		// blaming rev for a row that is gone or archived sends the caller
		// looking for a concurrent writer that does not exist.
		var archivedAt sql.NullInt64
		lookupErr := tx.QueryRow(`SELECT archived_at FROM jobs WHERE id = ?`, id).Scan(&archivedAt)
		switch {
		case errors.Is(lookupErr, sql.ErrNoRows):
			return 0, fmt.Errorf("rename job %d: %w", id, ErrNotFound)
		case lookupErr != nil:
			return 0, fmt.Errorf("rename job %d: %w", id, lookupErr)
		case archivedAt.Valid:
			return 0, fmt.Errorf("rename job %d: archived job is immutable: %w", id, ErrNotFound)
		default:
			return 0, fmt.Errorf("rename job %d at rev %d: %w", id, rev, ErrStaleRev)
		}
	}

	if _, err := tx.Exec(`UPDATE feedback SET job_slug = ? WHERE job_id = ?`, to, id); err != nil {
		return 0, fmt.Errorf("rename job %d: rekey feedback: %w", id, err)
	}
	// upsert rather than insert: renaming A→B→A→B must leave ONE (id,"A") row.
	// The stamp moves to the latest departure, which is what
	// GetJobByFormerSlug's ordering reads.
	if _, err := tx.Exec(
		`INSERT INTO job_former_slugs (job_id, slug, renamed_at) VALUES (?, ?, ?)
			ON CONFLICT(job_id, slug) DO UPDATE SET renamed_at = excluded.renamed_at`,
		id, oldSlug, now,
	); err != nil {
		return 0, fmt.Errorf("rename job %d: record former slug: %w", id, err)
	}
	// a job's former-slug set never contains its CURRENT slug. Renaming back
	// to an earlier name would otherwise leave the name listed in both places,
	// and every probe that iterates the former slugs (cell dirs, branch
	// family, project dirs) would derive the same path twice.
	if _, err := tx.Exec(`DELETE FROM job_former_slugs WHERE job_id = ? AND slug = ?`, id, to); err != nil {
		return 0, fmt.Errorf("rename job %d: clear former slug: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("rename job %d: %w", id, err)
	}
	return rev + 1, nil
}
