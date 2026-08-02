package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// tokens.go — the job_tokens and job_token_files tables (schema.go): the
// per-job token spend, and the per-transcript-file byte watermarks that make
// computing it incremental.
//
// "tokens", never "usage". `usage` in this binary means ACCOUNT QUOTA
// (/api/usage, ~/.kraken/usage.json, core/usage.go) and the two must stay
// greppably distinct.
//
// Both tables are keyed on the job ID since schema v11, with no FK and no
// cascade. The two halves of that are separate decisions and only one of them
// changed:
//
//   - ID rather than SLUG (new in v11): what is measured is the transcripts
//     under a job's cell dir, and a cell dir is `<slugified slug>-<id>`
//     (jobs/cell.go's cellName). Keyed on the slug alone, the five archived
//     jobs slugged `asdf` on this machine shared ONE row that honestly measured
//     all five together. See schemaV10's doc comment, which still carries the
//     old argument and the note on why it stopped being true.
//   - No FK, no cascade (unchanged from v10): an archived or hard-deleted job's
//     number must survive, because retrospective cost is the entire point of
//     storing it. teardownCell removes the cell dir; Claude Code keeps the
//     transcripts indefinitely.
//
// Ownership is simple and total: every column of both tables is written by
// the daemon's refresh pass and by nothing else. There is no carl-owned
// column here, so none of watch.go's careful preserve-the-other-side
// splitting applies.

// JobTokens is one job's summed token spend. Deliberately tag-free like
// every other type in this package — the API layer owns its own DTOs and the
// wire shape that goes with them (internal/api).
//
// The four counters are carried RAW, with no weighted total: they are billed
// at wildly different rates, and a stored "cost" number would bake a price
// ratio into the database and go stale the moment pricing moves.
type JobTokens struct {
	JobID      int64
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

	// The ZEUS half of the four counters above: what the cell's LEAD agent
	// spent, as opposed to everything it delegated to (teammates and
	// in-process subagents alike). Each is a SUBSET of the counter it shadows,
	// so the subagent half is In-ZeusIn and so on.
	//
	// Only this half is stored. The other one is derived, deliberately: two
	// stored halves can drift out of agreement with the total they are
	// supposed to add up to, and a derived one cannot.
	//
	// These accumulate exactly like the four totals do — see BumpJobTokens.
	ZeusIn         int64
	ZeusOut        int64
	ZeusCacheWrite int64
	ZeusCacheRead  int64
}

// jobTokensColumns COALESCEs every scanned column defensively, same
// belt-and-suspenders reasoning as jobColumns/feedbackColumns: the schema
// already declares all of these NOT NULL with a same-typed default, but a
// NULL that sneaks in anyway (schema drift, a hand-edited row) must degrade
// to a zero field rather than error the whole query.
const jobTokensColumns = `job_id, COALESCE(in_tokens,0), COALESCE(out_tokens,0),
	COALESCE(cache_write,0), COALESCE(cache_read,0), COALESCE(sessions,0),
	COALESCE(updated_at,0), COALESCE(zeus_in,0), COALESCE(zeus_out,0),
	COALESCE(zeus_cache_write,0), COALESCE(zeus_cache_read,0)`

func scanJobTokens(row interface{ Scan(...any) error }) (*JobTokens, error) {
	var t JobTokens
	if err := row.Scan(&t.JobID, &t.In, &t.Out, &t.CacheWrite, &t.CacheRead,
		&t.Sessions, &t.UpdatedAt, &t.ZeusIn, &t.ZeusOut,
		&t.ZeusCacheWrite, &t.ZeusCacheRead); err != nil {
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
func (s *Store) GetJobTokens(jobID int64) (*JobTokens, error) {
	row := s.db.QueryRow(`SELECT `+jobTokensColumns+` FROM job_tokens WHERE job_id = ?`, jobID)
	t, err := scanJobTokens(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job tokens %d: %w", jobID, err)
	}
	return t, nil
}

// ListJobTokens returns every measured job's row, id-ordered.
//
// It exists so a caller assembling many jobs at once — the board payload
// carries ~74 of them and rebuilds roughly every two seconds — makes ONE
// query rather than one per job. Ordering is by id purely so the result is
// deterministic; nothing renders in this order.
func (s *Store) ListJobTokens() ([]JobTokens, error) {
	rows, err := s.db.Query(`SELECT ` + jobTokensColumns + ` FROM job_tokens ORDER BY job_id`)
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
//   - The four ZEUS counters ACCUMULATE (+=) with the totals and for the
//     identical reason: they are the same delta, partitioned. Their addition
//     is in SQL too, so a concurrent pair of passes cannot lose one half of a
//     row while keeping the other — which would be worse than losing both,
//     since it would put the split permanently out of agreement with the total
//     it is a subset of.
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
//
// That CASE keys off the four TOTAL counters only, and the zeus ones are
// deliberately left out of it: they are a SUBSET of those four, so they can
// never make the sum non-zero on their own, and adding them would only make
// the condition longer to read for no change in behaviour.
func (s *Store) BumpJobTokens(jobID int64, delta JobTokens) error {
	if jobID <= 0 {
		return fmt.Errorf("bump job tokens: job id %d is not a persisted job", jobID)
	}
	if delta.UpdatedAt == 0 {
		delta.UpdatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO job_tokens (job_id, in_tokens, out_tokens, cache_write, cache_read, sessions, updated_at,
				zeus_in, zeus_out, zeus_cache_write, zeus_cache_read)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(job_id) DO UPDATE SET
				in_tokens        = in_tokens + excluded.in_tokens,
				out_tokens       = out_tokens + excluded.out_tokens,
				cache_write      = cache_write + excluded.cache_write,
				cache_read       = cache_read + excluded.cache_read,
				zeus_in          = zeus_in + excluded.zeus_in,
				zeus_out         = zeus_out + excluded.zeus_out,
				zeus_cache_write = zeus_cache_write + excluded.zeus_cache_write,
				zeus_cache_read  = zeus_cache_read + excluded.zeus_cache_read,
				sessions         = excluded.sessions,
				updated_at       = CASE WHEN excluded.in_tokens + excluded.out_tokens
					+ excluded.cache_write + excluded.cache_read > 0
					THEN excluded.updated_at ELSE updated_at END`,
		jobID, delta.In, delta.Out, delta.CacheWrite, delta.CacheRead,
		delta.Sessions, delta.UpdatedAt,
		delta.ZeusIn, delta.ZeusOut, delta.ZeusCacheWrite, delta.ZeusCacheRead,
	)
	if err != nil {
		return fmt.Errorf("bump job tokens %d: %w", jobID, err)
	}
	return nil
}

// JobTokenOffsets returns one job's per-file byte watermarks as an absolute
// transcript path -> bytes-already-summed map, which is exactly the shape
// core.SumTokensDelta takes. A job with no watermarks yet yields an empty
// (non-nil) map, never an error: "never measured" is a normal state and the
// empty map makes the next pass read every file whole, which is correct.
func (s *Store) JobTokenOffsets(jobID int64) (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT path, COALESCE(byte_offset,0) FROM job_token_files WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, fmt.Errorf("job token offsets %d: %w", jobID, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var path string
		var off int64
		if err := rows.Scan(&path, &off); err != nil {
			return nil, fmt.Errorf("job token offsets %d: %w", jobID, err)
		}
		out[path] = off
	}
	return out, rows.Err()
}

// AllJobTokenOffsets returns every job's watermarks in one query, keyed
// job id -> path -> bytes.
//
// This is the form the daemon's refresh pass uses, and the reason it exists
// is the same as ListJobTokens': the pass considers every job on every tick,
// and one query beats ~74. A job absent from the outer map has no watermarks,
// which the pass treats identically to an empty inner map.
func (s *Store) AllJobTokenOffsets() (map[int64]map[string]int64, error) {
	rows, err := s.db.Query(`SELECT job_id, path, COALESCE(byte_offset,0) FROM job_token_files`)
	if err != nil {
		return nil, fmt.Errorf("all job token offsets: %w", err)
	}
	defer rows.Close()
	out := map[int64]map[string]int64{}
	for rows.Next() {
		var jobID, off int64
		var path string
		if err := rows.Scan(&jobID, &path, &off); err != nil {
			return nil, fmt.Errorf("all job token offsets: %w", err)
		}
		if out[jobID] == nil {
			out[jobID] = map[string]int64{}
		}
		out[jobID][path] = off
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
// An empty map is legal and clears the job's watermarks. A zero job id is
// refused, for BumpJobTokens' reason.
func (s *Store) SetJobTokenOffsets(jobID int64, offsets map[string]int64) error {
	if jobID <= 0 {
		return fmt.Errorf("set job token offsets: job id %d is not a persisted job", jobID)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set job token offsets %d: %w", jobID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM job_token_files WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("set job token offsets %d: %w", jobID, err)
	}
	for path, off := range offsets {
		if _, err := tx.Exec(
			`INSERT INTO job_token_files (job_id, path, byte_offset) VALUES (?, ?, ?)`,
			jobID, path, off,
		); err != nil {
			return fmt.Errorf("set job token offsets %d: %w", jobID, err)
		}
	}
	return tx.Commit()
}
