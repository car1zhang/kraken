package store

import (
	"fmt"
	"regexp"
)

// questions.go — schema v4's relational doc-questions table (schema.go):
// real rows (job_id, qid, text, context, importance, recommendation,
// answer, answered_at, position), PK (job_id, qid), replacing the old
// blob-canonical mirror of questions.yaml (schemaV2's `questions` table,
// migrated to rows by migrateV4Questions). Column ownership mirrors the old
// file's contract: zeus owns text/context/importance/recommendation/
// position (its bulk-set/per-question writes); kraken owns answer/
// answered_at (only ever written by AnswerQuestion, carl's web-board
// action). Every write here that touches zeus-owned columns must therefore
// leave answer/answered_at exactly as they were on any surviving row — see
// SetQuestions/UpsertQuestion's "preserve" doc comments.

// DocQuestion is one open (or answered) doc-question row. Its JSON shape
// becomes wire-contract-pinned the moment the API layer serves it
// (internal/api) — treat field renames/removals the same as any other
// apiContract-covered type.
type DocQuestion struct {
	JobID          int64
	QID            string
	Text           string
	Context        string
	Importance     string
	Recommendation string
	Answer         string
	AnsweredAt     string
	Position       int64
}

// docQuestionIDRe / validImportance bound the two fields this package
// validates on every zeus-owned write, so a caller passing garbage gets a
// descriptive error the API layer can turn into a 400 — mirrors jobs.
// docQuestionIDRe/validImportance (internal/jobs/questions.go), kept as
// store's own copy since store cannot import jobs (util ← store ← core ←
// jobs ← api ← web: importing upward would both invert the layering and
// create an import cycle).
var docQuestionIDRe = regexp.MustCompile(`^Q[0-9]+$`)

var validImportance = map[string]bool{"P0": true, "P1": true, "P2": true}

// validateQID/validateImportance return a descriptive, API-surfaceable
// error rather than a bare bool, so every write path below can just
// `return err` straight into a 400.
func validateQID(qid string) error {
	if !docQuestionIDRe.MatchString(qid) {
		return fmt.Errorf("bad question id %q: want Q<digits>", qid)
	}
	return nil
}

func validateImportance(importance string) error {
	if !validImportance[importance] {
		return fmt.Errorf("bad importance %q: want P0, P1, or P2", importance)
	}
	return nil
}

// ListQuestions returns jobID's questions ordered by position — the same
// top-to-bottom order questions.yaml was always read in.
func (s *Store) ListQuestions(jobID int64) ([]DocQuestion, error) {
	out, err := s.scanQuestions(questionColumns+` WHERE job_id = ? ORDER BY position, qid`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list questions for job %d: %w", jobID, err)
	}
	return out, nil
}

// ListQuestionsAll returns every question of every job, ordered by job then
// position — the mind pass' cross-job sweep of carl's answers, which are
// decisions he already made in words and so the strongest evidence it gets.
// Same scan as ListQuestions, so the two can never read a row differently.
func (s *Store) ListQuestionsAll() ([]DocQuestion, error) {
	out, err := s.scanQuestions(questionColumns + ` ORDER BY job_id, position, qid`)
	if err != nil {
		return nil, fmt.Errorf("list all questions: %w", err)
	}
	return out, nil
}

// questionColumns is the one SELECT both question listings read through.
const questionColumns = `SELECT job_id, qid, COALESCE(text,''), COALESCE(context,''), COALESCE(importance,''),
	COALESCE(recommendation,''), COALESCE(answer,''), COALESCE(answered_at,''), COALESCE(position,0)
	FROM questions`

func (s *Store) scanQuestions(query string, args ...any) ([]DocQuestion, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocQuestion
	for rows.Next() {
		var q DocQuestion
		if err := rows.Scan(&q.JobID, &q.QID, &q.Text, &q.Context, &q.Importance,
			&q.Recommendation, &q.Answer, &q.AnsweredAt, &q.Position); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// upsertQuestionZeusColumns is SetQuestions/UpsertQuestion's shared write:
// insert, or on a (job_id, qid) conflict update ONLY the zeus-owned
// columns — text/context/importance/recommendation/position — so
// answer/answered_at on a surviving row is left completely untouched. One
// implementation, so the "preserve kraken-owned columns" invariant only
// has to be gotten right in one place.
func upsertQuestionZeusColumns(x execer, jobID int64, q DocQuestion) error {
	_, err := x.Exec(
		`INSERT INTO questions (job_id, qid, text, context, importance, recommendation, position)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(job_id, qid) DO UPDATE SET
				text = excluded.text, context = excluded.context,
				importance = excluded.importance, recommendation = excluded.recommendation,
				position = excluded.position`,
		jobID, q.QID, q.Text, q.Context, q.Importance, q.Recommendation, q.Position,
	)
	return err
}

// SetQuestions bulk-sets jobID's questions to exactly qs: upserts each by
// qid (zeus-owned columns only, see upsertQuestionZeusColumns), deletes any
// existing row whose qid is not present in qs, and PRESERVES answer/
// answered_at on every surviving row. This is zeus' full bulk-set verb
// (`kraken questions set`) — one call replaces the entire question set the
// same way a fresh questions.yaml write used to, except answers now survive
// across it instead of requiring a separate re-patch step.
func (s *Store) SetQuestions(jobID int64, qs []DocQuestion) error {
	seen := make(map[string]bool, len(qs))
	for _, q := range qs {
		if err := validateQID(q.QID); err != nil {
			return err
		}
		if err := validateImportance(q.Importance); err != nil {
			return err
		}
		// A duplicate qid within one bulk set would silently last-win via the
		// upsert's conflict-update — reject it up front instead, same loud
		// contract as a bad qid (the old file parser errored on dup ids too).
		if seen[q.QID] {
			return fmt.Errorf("duplicate question id %q in set", q.QID)
		}
		seen[q.QID] = true
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set questions for job %d: %w", jobID, err)
	}
	defer tx.Rollback()

	keep := make(map[string]bool, len(qs))
	for _, q := range qs {
		keep[q.QID] = true
		if err := upsertQuestionZeusColumns(tx, jobID, q); err != nil {
			return fmt.Errorf("set questions for job %d: upsert %s: %w", jobID, q.QID, err)
		}
	}

	rows, err := tx.Query(`SELECT qid FROM questions WHERE job_id = ?`, jobID)
	if err != nil {
		return fmt.Errorf("set questions for job %d: %w", jobID, err)
	}
	var stale []string
	for rows.Next() {
		var qid string
		if err := rows.Scan(&qid); err != nil {
			rows.Close()
			return fmt.Errorf("set questions for job %d: %w", jobID, err)
		}
		if !keep[qid] {
			stale = append(stale, qid)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("set questions for job %d: %w", jobID, err)
	}
	rows.Close()
	for _, qid := range stale {
		if _, err := tx.Exec(`DELETE FROM questions WHERE job_id = ? AND qid = ?`, jobID, qid); err != nil {
			return fmt.Errorf("set questions for job %d: delete %s: %w", jobID, qid, err)
		}
	}

	return tx.Commit()
}

// UpsertQuestion adds or updates exactly one question (zeus' per-question
// add/update verbs, Q12's "both styles" alongside SetQuestions), writing
// only the zeus-owned columns and preserving answer/answered_at on an
// existing row — same rule as SetQuestions, scoped to one qid.
func (s *Store) UpsertQuestion(jobID int64, q DocQuestion) error {
	if err := validateQID(q.QID); err != nil {
		return err
	}
	if err := validateImportance(q.Importance); err != nil {
		return err
	}
	if err := upsertQuestionZeusColumns(s.db, jobID, q); err != nil {
		return fmt.Errorf("upsert question %s for job %d: %w", q.QID, jobID, err)
	}
	return nil
}

// DeleteQuestion removes one question row (zeus' per-question remove
// verb). A no-op, not an error, when the qid doesn't exist — mirrors the
// old file-delete semantics (removing an already-absent question was never
// an error either).
func (s *Store) DeleteQuestion(jobID int64, qid string) error {
	if _, err := s.db.Exec(`DELETE FROM questions WHERE job_id = ? AND qid = ?`, jobID, qid); err != nil {
		return fmt.Errorf("delete question %s for job %d: %w", qid, jobID, err)
	}
	return nil
}

// AnswerQuestion overwrites qid's answer/answered_at unconditionally — a
// re-answer is the same UPDATE as a first answer (Q13: rows are editable
// forever, live or done). Errors when qid doesn't exist for jobID, so the
// API layer can surface an unknown-question error rather than silently
// no-op-ing; unlike the retired file-mode AnswerDocQuestion, an
// already-answered question is NOT refused — that's the entire point of
// "re-answer allowed."
func (s *Store) AnswerQuestion(jobID int64, qid, answer, answeredAt string) error {
	res, err := s.db.Exec(
		`UPDATE questions SET answer = ?, answered_at = ? WHERE job_id = ? AND qid = ?`,
		answer, answeredAt, jobID, qid,
	)
	if err != nil {
		return fmt.Errorf("answer question %s for job %d: %w", qid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("answer question %s for job %d: %w", qid, jobID, err)
	}
	if n == 0 {
		return fmt.Errorf("answer question %s for job %d: %w", qid, jobID, ErrNotFound)
	}
	return nil
}
