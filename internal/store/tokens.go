package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// tokens.go — schema v10's job_tokens and job_token_files tables
// (schema.go): the per-job token spend, and the per-transcript-file byte
// watermarks that make computing it incremental.
//
// "tokens", never "usage". `usage` in this binary means ACCOUNT QUOTA
// (/api/usage, ~/.kraken/usage.json, core/usage.go) and the two must stay
// greppably distinct.
//
// Both tables are keyed on the job SLUG, with no job_id, no FK and no
// cascade — see schemaV10's doc comment for why (in short: the transcripts
// being measured live in a directory derived from the slug, and an archived
// or hard-deleted job's number must survive, since retrospective cost is the
// whole point).
//
// Ownership is simple and total: every column of both tables is written by
// the daemon's refresh pass and by nothing else. There is no carl-owned
// column here, so none of watchlist.go's careful preserve-the-other-side
// splitting applies.

// JobTokens is one job's summed token spend. Deliberately tag-free like
// every other type in this package — the API layer owns its own DTOs and the
// wire shape that goes with them (internal/api).
//
// The four counters are carried RAW, with no weighted total: they are billed
// at wildly different rates, and a stored "cost" number would bake a price
// ratio into the database and go stale the moment pricing moves.
type JobTokens struct {
	Slug       string
	In         int64 // input_tokens — uncached input
	Out        int64 // output_tokens — generated
	CacheWrite int64 // cache_creation_input_tokens
	CacheRead  int64 // cache_read_input_tokens
	// Sessions is the number of transcript FILES the counters were summed
	// from (root transcripts plus subagent sidecars). It is what lets a
	// reader tell "this job was cheap" from "this job has not been measured
	// yet", which a row of four zeros alone cannot say.
	Sessions  int64
	UpdatedAt int64 // unix millis — when a refresh last found new spend
}

// jobTokensColumns COALESCEs every scanned column defensively, same
// belt-and-suspenders reasoning as jobColumns/watchItemColumns: the schema
// already declares all of these NOT NULL with a same-typed default, but a
// NULL that sneaks in anyway (schema drift, a hand-edited row) must degrade
// to a zero field rather than error the whole query.
const jobTokensColumns = `job_slug, COALESCE(in_tokens,0), COALESCE(out_tokens,0),
	COALESCE(cache_write,0), COALESCE(cache_read,0), COALESCE(sessions,0),
	COALESCE(updated_at,0)`

func scanJobTokens(row interface{ Scan(...any) error }) (*JobTokens, error) {
	var t JobTokens
	if err := row.Scan(&t.Slug, &t.In, &t.Out, &t.CacheWrite, &t.CacheRead,
		&t.Sessions, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetJobTokens returns one job's row, or ErrNotFound when the job has never
// been measured — same pointer-returning, sql.ErrNoRows → ErrNotFound shape
// as every other single-row getter in this package.
//
// ErrNotFound is a MEANINGFUL answer here rather than merely an absence: it
// is what distinguishes "no transcripts yet" from a genuine zero, and it is
// what lets the wire shape omit the key entirely for such a job.
func (s *Store) GetJobTokens(slug string) (*JobTokens, error) {
	row := s.db.QueryRow(`SELECT `+jobTokensColumns+` FROM job_tokens WHERE job_slug = ?`, slug)
	t, err := scanJobTokens(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job tokens %q: %w", slug, err)
	}
	return t, nil
}

// ListJobTokens returns every measured job's row, slug-ordered.
//
// It exists so a caller assembling many jobs at once — the board payload
// carries ~74 of them and rebuilds roughly every two seconds — makes ONE
// query rather than one per job. Ordering is by slug purely so the result is
// deterministic; nothing renders in this order.
func (s *Store) ListJobTokens() ([]JobTokens, error) {
	rows, err := s.db.Query(`SELECT ` + jobTokensColumns + ` FROM job_tokens ORDER BY job_slug`)
	if err != nil {
		return nil, fmt.Errorf("list job tokens: %w", err)
	}
	defer rows.Close()
	var out []JobTokens
	for rows.Next() {
		t, err := scanJobTokens(rows)
		if err != nil {
			return nil, fmt.Errorf("list job tokens: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// BumpJobTokens folds one incremental pass's result into a job's row,
// inserting it when the job has never been measured.
//
// The two halves of the fold are DIFFERENT and the difference is the whole
// contract of this verb:
//
//   - The four counters ACCUMULATE (+=). The caller passes a DELTA — the
//     tokens found past the byte offsets recorded by the previous pass — so
//     adding is the only correct operation. The addition is done in SQL
//     rather than read-modify-write in Go for BumpWatchSighting's reason:
//     kraken serve answers HTTP concurrently, and a read-then-write pair
//     around these columns would lose a delta whenever two passes overlapped.
//   - Sessions is SET (=). It is a FILE COUNT, not a delta, and a caller that
//     accumulated it would report a job with 12 transcripts as having several
//     thousand. core.SumTokensDelta returns it as a total for exactly this
//     reason and says so.
//
// UpdatedAt is stamped to now when zero, and — on an UPDATE — is written
// ONLY when the delta actually carries spend. A pass can legitimately have
// something to write with all four counters at zero: a new transcript file
// appearing bumps `sessions` before anything has been generated into it, and
// on a live cell that happens constantly, since a fresh session's first
// record is a user turn. Moving the timestamp then would contradict what it
// means — both this comment and the drilldowns render it as "measured Ns
// ago", which on an idle job reads as spend just now. The CASE keeps that
// honest in one statement rather than making callers pre-read the row.
// The INSERT still stamps unconditionally: a row's first appearance IS news.
func (s *Store) BumpJobTokens(slug string, delta JobTokens) error {
	if slug == "" {
		return fmt.Errorf("bump job tokens: empty slug")
	}
	if delta.UpdatedAt == 0 {
		delta.UpdatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO job_tokens (job_slug, in_tokens, out_tokens, cache_write, cache_read, sessions, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(job_slug) DO UPDATE SET
				in_tokens   = in_tokens + excluded.in_tokens,
				out_tokens  = out_tokens + excluded.out_tokens,
				cache_write = cache_write + excluded.cache_write,
				cache_read  = cache_read + excluded.cache_read,
				sessions    = excluded.sessions,
				updated_at  = CASE WHEN excluded.in_tokens + excluded.out_tokens
					+ excluded.cache_write + excluded.cache_read > 0
					THEN excluded.updated_at ELSE updated_at END`,
		slug, delta.In, delta.Out, delta.CacheWrite, delta.CacheRead,
		delta.Sessions, delta.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("bump job tokens %q: %w", slug, err)
	}
	return nil
}

// JobTokenOffsets returns one job's per-file byte watermarks as an absolute
// transcript path -> bytes-already-summed map, which is exactly the shape
// core.SumTokensDelta takes. A job with no watermarks yet yields an empty
// (non-nil) map, never an error: "never measured" is a normal state and the
// empty map makes the next pass read every file whole, which is correct.
func (s *Store) JobTokenOffsets(slug string) (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT path, COALESCE(byte_offset,0) FROM job_token_files WHERE job_slug = ?`, slug)
	if err != nil {
		return nil, fmt.Errorf("job token offsets %q: %w", slug, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var path string
		var off int64
		if err := rows.Scan(&path, &off); err != nil {
			return nil, fmt.Errorf("job token offsets %q: %w", slug, err)
		}
		out[path] = off
	}
	return out, rows.Err()
}

// AllJobTokenOffsets returns every job's watermarks in one query, keyed
// slug -> path -> bytes.
//
// This is the form the daemon's refresh pass uses, and the reason it exists
// is the same as ListJobTokens': the pass considers every job on every tick,
// and one query beats ~74. A slug absent from the outer map has no
// watermarks, which the pass treats identically to an empty inner map.
func (s *Store) AllJobTokenOffsets() (map[string]map[string]int64, error) {
	rows, err := s.db.Query(`SELECT job_slug, path, COALESCE(byte_offset,0) FROM job_token_files`)
	if err != nil {
		return nil, fmt.Errorf("all job token offsets: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var slug, path string
		var off int64
		if err := rows.Scan(&slug, &path, &off); err != nil {
			return nil, fmt.Errorf("all job token offsets: %w", err)
		}
		if out[slug] == nil {
			out[slug] = map[string]int64{}
		}
		out[slug][path] = off
	}
	return out, rows.Err()
}

// SetJobTokenOffsets REPLACES a job's whole watermark set, in one
// transaction.
//
// Replace rather than upsert-each, because the set shrinking is meaningful:
// core.SumTokensDelta returns one entry per transcript file that exists RIGHT
// NOW, so a path missing from offsets is a file that is gone, and leaving its
// stale row behind would grow this table without bound as sessions come and
// go. One transaction so a crash mid-write can never leave a job with half
// the previous pass's watermarks and half this one's — which would re-sum
// the files whose watermarks were lost and double-count them into the
// accumulating counters.
//
// An empty map is legal and clears the job's watermarks.
func (s *Store) SetJobTokenOffsets(slug string, offsets map[string]int64) error {
	if slug == "" {
		return fmt.Errorf("set job token offsets: empty slug")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set job token offsets %q: %w", slug, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM job_token_files WHERE job_slug = ?`, slug); err != nil {
		return fmt.Errorf("set job token offsets %q: %w", slug, err)
	}
	for path, off := range offsets {
		if _, err := tx.Exec(
			`INSERT INTO job_token_files (job_slug, path, byte_offset) VALUES (?, ?, ?)`,
			slug, path, off,
		); err != nil {
			return fmt.Errorf("set job token offsets %q: %w", slug, err)
		}
	}
	return tx.Commit()
}
