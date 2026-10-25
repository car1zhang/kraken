package store

import (
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

// mindruns.go — schema v39's mind_runs and mind_run_blobs (schema.go): the
// mind run queue, each run's record, and the payloads it read and answered.
//
// One row is both the job ticket and the history. A run is enqueued `queued`,
// claimed `running` by the one worker in kraken serve, and finished `done`
// (counts, report, usage) or `failed` (a note). The blobs live in their own
// table, keyed by run id, so listing runs never reads a bundle.

// MindRun is one mind_runs row. Trigger is done | daily | replay; State is
// queued | running | done | failed. JobID 0 is the daily run, which belongs to
// no job. SourceRun is the run a replay re-judged (0 otherwise). Every
// timestamp is unix millis, 0 meaning "not yet".
type MindRun struct {
	ID, JobID                                                                  int64
	Slug, Trigger                                                              string
	Since, Until                                                               int64
	State                                                                      string
	CreatedAt, StartedAt, FinishedAt                                           int64
	Note, PromptSHA, Model                                                     string
	InTokens, OutTokens, BundleBytes                                           int64
	Added, Superseded, Retired, Flagged, Excluded, Folded, Suppressed, Refused int
	Report                                                                     string
	SourceRun                                                                  int64
}

// MindRunCounts tallies a finished run's per-op outcomes.
type MindRunCounts struct {
	Added, Superseded, Retired, Flagged, Excluded, Folded, Suppressed, Refused int
}

// MindRunFinish is everything FinishMindRun stamps on a running row.
type MindRunFinish struct {
	At                               int64
	Counts                           MindRunCounts
	Report, PromptSHA, Model         string
	InTokens, OutTokens, BundleBytes int64
}

// MindRunBlobs is one mind_run_blobs row. BundleGz is nil once pruned
// (PrunedAt > 0); Manifest and Answer are never pruned.
type MindRunBlobs struct {
	RunID    int64
	BundleGz []byte
	Manifest string
	Answer   string
	PrunedAt int64
}

// validMindTrigger mirrors mind_runs' trigger CHECK, so a bad value is refused
// with a sentence naming the set (mm.go's idiom).
var validMindTrigger = map[string]bool{"done": true, "daily": true, "replay": true}

// mindRunNoteMaxRunes caps a failure note: one line, long enough for the
// first 500 bytes of an unparseable answer plus the sentence around it.
const mindRunNoteMaxRunes = 2000

const mindRunColumns = `SELECT id, job_id, slug, trigger_kind, since_ms, until_ms, state,
	created_at, started_at, finished_at, note, prompt_sha, model, in_tokens, out_tokens,
	bundle_bytes, added, superseded, retired, flagged, excluded, folded, suppressed, refused,
	report, source_run
	FROM mind_runs`

func scanMindRun(row interface{ Scan(...any) error }) (MindRun, error) {
	var r MindRun
	err := row.Scan(&r.ID, &r.JobID, &r.Slug, &r.Trigger, &r.Since, &r.Until, &r.State,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.Note, &r.PromptSHA, &r.Model,
		&r.InTokens, &r.OutTokens, &r.BundleBytes, &r.Added, &r.Superseded, &r.Retired,
		&r.Flagged, &r.Excluded, &r.Folded, &r.Suppressed, &r.Refused, &r.Report, &r.SourceRun)
	return r, err
}

// mindRunNote one-lines a note and caps it at mindRunNoteMaxRunes.
func mindRunNote(s string) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) > mindRunNoteMaxRunes {
		s = string([]rune(s)[:mindRunNoteMaxRunes])
	}
	return s
}

// EnqueueMindRun inserts r as a queued run and returns its id. The caller
// stamps CreatedAt (the AddMMItem convention). An unknown trigger and an
// empty or inverted window are refused before the INSERT.
func (s *Store) EnqueueMindRun(r MindRun) (int64, error) {
	if !validMindTrigger[r.Trigger] {
		return 0, fmt.Errorf("bad mind run trigger %q: want done, daily or replay", r.Trigger)
	}
	if r.Until <= r.Since {
		return 0, fmt.Errorf("mind run window is empty: until %d <= since %d", r.Until, r.Since)
	}
	res, err := s.db.Exec(
		`INSERT INTO mind_runs (job_id, slug, trigger_kind, since_ms, until_ms, state, created_at, note, source_run)
			VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?)`,
		r.JobID, r.Slug, r.Trigger, r.Since, r.Until, r.CreatedAt, mindRunNote(r.Note), r.SourceRun,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue mind run (%s, job %d): %w", r.Trigger, r.JobID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("enqueue mind run (%s, job %d): %w", r.Trigger, r.JobID, err)
	}
	return id, nil
}

// ClaimNextMindRun moves the oldest queued run (created_at, then id) to
// running with StartedAt = at and returns it; nil, nil when nothing is
// queued. The move is a compare-and-set on state, retried when another caller
// claimed the same candidate first, so two claimers never get one row.
func (s *Store) ClaimNextMindRun(at int64) (*MindRun, error) {
	for {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM mind_runs WHERE state = 'queued' ORDER BY created_at, id LIMIT 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("claim mind run: %w", err)
		}
		res, err := s.db.Exec(`UPDATE mind_runs SET state = 'running', started_at = ? WHERE id = ? AND state = 'queued'`, at, id)
		if err != nil {
			return nil, fmt.Errorf("claim mind run %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("claim mind run %d: %w", id, err)
		}
		if n == 0 {
			continue // lost the race for this row; look again
		}
		r, err := s.GetMindRun(id)
		if err != nil {
			return nil, err
		}
		return &r, nil
	}
}

// mindRunState reads one row's state, ErrNotFound when the id names nothing.
func (s *Store) mindRunState(id int64) (string, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM mind_runs WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("mind run %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("mind run %d: %w", id, err)
	}
	return state, nil
}

// FinishMindRun closes a running run as done with its counts, usage and
// report. Only a running row may finish: anything else is refused with its
// state named, so a run requeued by a restart cannot also be finished by the
// goroutine that lost it.
func (s *Store) FinishMindRun(id int64, f MindRunFinish) error {
	c := f.Counts
	res, err := s.db.Exec(
		`UPDATE mind_runs SET state = 'done', finished_at = ?, added = ?, superseded = ?, retired = ?,
			flagged = ?, excluded = ?, folded = ?, suppressed = ?, refused = ?, report = ?,
			prompt_sha = ?, model = ?, in_tokens = ?, out_tokens = ?, bundle_bytes = ?
			WHERE id = ? AND state = 'running'`,
		f.At, c.Added, c.Superseded, c.Retired, c.Flagged, c.Excluded, c.Folded, c.Suppressed, c.Refused,
		f.Report, f.PromptSHA, f.Model, f.InTokens, f.OutTokens, f.BundleBytes, id,
	)
	if err != nil {
		return fmt.Errorf("finish mind run %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish mind run %d: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	state, err := s.mindRunState(id)
	if err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	return fmt.Errorf("finish mind run %d: it is %s, not running", id, state)
}

// FailMindRun closes a queued or running run as failed with a one-line note.
// A run already done or failed is refused with its state named.
func (s *Store) FailMindRun(id, at int64, note string) error {
	res, err := s.db.Exec(
		`UPDATE mind_runs SET state = 'failed', finished_at = ?, note = ? WHERE id = ? AND state IN ('queued','running')`,
		at, mindRunNote(note), id,
	)
	if err != nil {
		return fmt.Errorf("fail mind run %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("fail mind run %d: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	state, err := s.mindRunState(id)
	if err != nil {
		return fmt.Errorf("fail: %w", err)
	}
	return fmt.Errorf("fail mind run %d: it is already %s", id, state)
}

// RequeueRunningMindRuns is the serve-restart repair: a run found running
// when the process starts has no worker any more. Each running row is put
// back in the queue (StartedAt 0, note appended) at most ONCE — a row whose
// note already carries this note was requeued by an earlier restart and is
// failed instead, so a run that kills its serve cannot loop forever. A
// `replay` row is failed rather than requeued too: its bundle is a recorded
// one, which a requeued run would rebuild from today's store. Returns how
// many rows were requeued (failed ones are not counted).
func (s *Store) RequeueRunningMindRuns(note string) (int, error) {
	note = mindRunNote(note)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("requeue running mind runs: %w", err)
	}
	defer tx.Rollback()
	failNote := func(why string) string { return mindRunNote(note + ": " + why) }
	if _, err := tx.Exec(
		`UPDATE mind_runs SET state = 'failed', finished_at = CAST(strftime('%s','now') AS INTEGER) * 1000,
			note = CASE WHEN note = '' THEN ? ELSE note || '; ' || ? END
			WHERE state = 'running' AND trigger_kind = 'replay'`,
		failNote("a replay is never requeued"), failNote("a replay is never requeued"),
	); err != nil {
		return 0, fmt.Errorf("requeue running mind runs: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE mind_runs SET state = 'failed', finished_at = CAST(strftime('%s','now') AS INTEGER) * 1000,
			note = note || '; ' || ?
			WHERE state = 'running' AND instr(note, ?) > 0`,
		failNote("already requeued once"), note,
	); err != nil {
		return 0, fmt.Errorf("requeue running mind runs: %w", err)
	}
	res, err := tx.Exec(
		`UPDATE mind_runs SET state = 'queued', started_at = 0,
			note = CASE WHEN note = '' THEN ? ELSE note || '; ' || ? END
			WHERE state = 'running'`,
		note, note,
	)
	if err != nil {
		return 0, fmt.Errorf("requeue running mind runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("requeue running mind runs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("requeue running mind runs: %w", err)
	}
	return int(n), nil
}

// ListRunningMindRuns returns every running row, oldest started first — the
// stale-row sweep's input, read without the list's 50-row window.
func (s *Store) ListRunningMindRuns() ([]MindRun, error) {
	rows, err := s.db.Query(mindRunColumns + ` WHERE state = 'running' ORDER BY started_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list running mind runs: %w", err)
	}
	defer rows.Close()
	out := []MindRun{}
	for rows.Next() {
		r, err := scanMindRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list running mind runs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRunningMindRun inserts r directly as running with StartedAt = at —
// a replay with --write, which runs its row itself and must never sit in the
// queue where the worker could claim it and rebuild its bundle. The same
// refusals as EnqueueMindRun.
func (s *Store) InsertRunningMindRun(r MindRun, at int64) (int64, error) {
	if !validMindTrigger[r.Trigger] {
		return 0, fmt.Errorf("bad mind run trigger %q: want done, daily or replay", r.Trigger)
	}
	if r.Until <= r.Since {
		return 0, fmt.Errorf("mind run window is empty: until %d <= since %d", r.Until, r.Since)
	}
	res, err := s.db.Exec(
		`INSERT INTO mind_runs (job_id, slug, trigger_kind, since_ms, until_ms, state, created_at, started_at, note, source_run)
			VALUES (?, ?, ?, ?, ?, 'running', ?, ?, ?, ?)`,
		r.JobID, r.Slug, r.Trigger, r.Since, r.Until, r.CreatedAt, at, mindRunNote(r.Note), r.SourceRun,
	)
	if err != nil {
		return 0, fmt.Errorf("insert running mind run (%s, job %d): %w", r.Trigger, r.JobID, err)
	}
	return res.LastInsertId()
}

// ListMindRuns returns runs newest first (created_at, then id). jobID 0 lists
// every run; n <= 0 means 50. Blobs are never read.
func (s *Store) ListMindRuns(jobID int64, n int) ([]MindRun, error) {
	if n <= 0 {
		n = 50
	}
	query := mindRunColumns
	var args []any
	if jobID != 0 {
		query += ` WHERE job_id = ?`
		args = append(args, jobID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, n)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list mind runs: %w", err)
	}
	defer rows.Close()
	out := []MindRun{}
	for rows.Next() {
		r, err := scanMindRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list mind runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mind runs: %w", err)
	}
	return out, nil
}

// GetMindRun reads one run, ErrNotFound on an unknown id.
func (s *Store) GetMindRun(id int64) (MindRun, error) {
	r, err := scanMindRun(s.db.QueryRow(mindRunColumns+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MindRun{}, fmt.Errorf("get mind run %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return MindRun{}, fmt.Errorf("get mind run %d: %w", id, err)
	}
	return r, nil
}

// LastMindRunUntil is the floor the next run of this job and trigger starts
// from: the greatest until among its queued, running and done rows, 0 when
// there are none. A failed run does not count — its window was never read,
// so the next run must read it again.
func (s *Store) LastMindRunUntil(jobID int64, trigger string) (int64, error) {
	var until int64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(until_ms),0) FROM mind_runs
			WHERE job_id = ? AND trigger_kind = ? AND state IN ('queued','running','done')`,
		jobID, trigger,
	).Scan(&until)
	if err != nil {
		return 0, fmt.Errorf("last mind run until (job %d, %s): %w", jobID, trigger, err)
	}
	return until, nil
}

// SetMindRunBlobs writes a run's gzipped bundle and its manifest, replacing
// any earlier ones and clearing pruned_at; an answer already stored is kept.
// ErrNotFound when no mind_runs row carries runID.
func (s *Store) SetMindRunBlobs(runID int64, bundleGz []byte, manifest string) error {
	if _, err := s.mindRunState(runID); err != nil {
		return fmt.Errorf("set mind run blobs: %w", err)
	}
	if bundleGz == nil {
		bundleGz = []byte{} // NULL is reserved for "pruned"
	}
	if _, err := s.db.Exec(
		`INSERT INTO mind_run_blobs (run_id, bundle_gz, manifest, pruned_at) VALUES (?, ?, ?, 0)
			ON CONFLICT(run_id) DO UPDATE SET bundle_gz = excluded.bundle_gz,
			manifest = excluded.manifest, pruned_at = 0`,
		runID, bundleGz, manifest,
	); err != nil {
		return fmt.Errorf("set mind run blobs %d: %w", runID, err)
	}
	return nil
}

// SetMindRunAnswer writes the raw answer onto the run's blob row.
// ErrNotFound when SetMindRunBlobs never ran for it.
func (s *Store) SetMindRunAnswer(runID int64, answer string) error {
	res, err := s.db.Exec(`UPDATE mind_run_blobs SET answer = ? WHERE run_id = ?`, answer, runID)
	if err != nil {
		return fmt.Errorf("set mind run answer %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set mind run answer %d: %w", runID, err)
	}
	if n == 0 {
		return fmt.Errorf("set mind run answer %d: %w", runID, ErrNotFound)
	}
	return nil
}

// GetMindRunBlobs reads a run's blob row, ErrNotFound when there is none. A
// pruned row comes back with BundleGz nil and PrunedAt set.
func (s *Store) GetMindRunBlobs(runID int64) (MindRunBlobs, error) {
	b := MindRunBlobs{RunID: runID}
	var bundle []byte
	err := s.db.QueryRow(
		`SELECT bundle_gz, COALESCE(manifest,''), COALESCE(answer,''), COALESCE(pruned_at,0)
			FROM mind_run_blobs WHERE run_id = ?`, runID,
	).Scan(&bundle, &b.Manifest, &b.Answer, &b.PrunedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MindRunBlobs{}, fmt.Errorf("get mind run blobs %d: %w", runID, ErrNotFound)
	}
	if err != nil {
		return MindRunBlobs{}, fmt.Errorf("get mind run blobs %d: %w", runID, err)
	}
	if b.PrunedAt > 0 {
		bundle = nil
	} else if bundle == nil {
		bundle = []byte{}
	}
	b.BundleGz = bundle
	return b, nil
}

// HasMindRunBundle reports whether a run still holds an unpruned bundle — the
// runs list's has_bundle, answered without reading the bytes.
func (s *Store) HasMindRunBundle(runID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mind_run_blobs WHERE run_id = ? AND pruned_at = 0 AND bundle_gz IS NOT NULL`, runID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has mind run bundle %d: %w", runID, err)
	}
	return n > 0, nil
}

// MindRunManifests returns the manifest text of every run with this trigger
// and state that has a blob row, oldest first — the daily run's "sessions a
// job run already swept", read without touching any bundle bytes.
func (s *Store) MindRunManifests(trigger, state string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT COALESCE(b.manifest,'') FROM mind_run_blobs b JOIN mind_runs r ON r.id = b.run_id
			WHERE r.trigger_kind = ? AND r.state = ? ORDER BY r.created_at, r.id`,
		trigger, state,
	)
	if err != nil {
		return nil, fmt.Errorf("list mind run manifests: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("list mind run manifests: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mind run manifests: %w", err)
	}
	return out, nil
}

// PruneMindRunBundles drops the bundle bytes of every run created before
// `before` that still holds them, stamping pruned_at = at. Manifests, answers
// and the run rows are kept forever; only the large bytes expire.
func (s *Store) PruneMindRunBundles(before, at int64) (int, error) {
	res, err := s.db.Exec(
		`UPDATE mind_run_blobs SET bundle_gz = NULL, pruned_at = ?
			WHERE pruned_at = 0 AND run_id IN (SELECT id FROM mind_runs WHERE created_at < ?)`,
		at, before,
	)
	if err != nil {
		return 0, fmt.Errorf("prune mind run bundles: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune mind run bundles: %w", err)
	}
	return int(n), nil
}
