package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// schemaVersion is the current embedded schema version, tracked in SQLite's
// own PRAGMA user_version so migrate() is a no-op on an already-current
// database (open/migrate idempotence). Bump this and append a new
// migration step when the schema changes; version 1 creates everything the
// spec's Data Model section calls for, version 2 adds the questions table,
// version 3 drops jobs.type (Q14 — the build/info job-level type concept is
// gone; the pipeline distinction now lives in /aim vs /info skill choice
// plus carl's /do steering, never a stored column), version 4 adds
// prs.conflict_state, version 5 drops jobs.skip_doc, extends feedback with
// session/addressed/addressed_at, and restructures the blob-canonical
// questions table into relational rows (parse-migrating the old blobs
// once). Version 6 (B2, a 54-feedback-contract follow-up) adds
// feedback.job_id: archive/unarchive must re-key a job's feedback rows by an
// immutable id, since the mutable job_slug string alone can't disambiguate
// which rows are "mine" once a freed slug is reused by another job (see
// store.ArchiveJob's doc comment) — job_slug itself is untouched as every
// read path's lookup key. Version 7 adds the threads table: side threads,
// forked claude sessions branched off a job's zeus and living in a second
// tmux window of the same cell (threads.go). Version 8 adds jobs.color.
// Version 9 adds the dream loop's watchlist table plus a feedback(ts) index
// for its unscoped time-window sweep (watchlist.go, feedback.go's
// FeedbackSince).
const schemaVersion = 9

// schemaV1 creates all nine tables plus kv. Exact DDL (types, indexes) was
// left to this package by the spec; shapes below mirror the spec's Data
// Model section field-for-field.
//
// Every column that a Go scan reads into a non-pointer field is declared
// NOT NULL with a same-typed DEFAULT (empty string for TEXT, 0 for
// INTEGER): the repo rule is "degrade to blank fields, never errors" for
// data owned by other programs, and a NULL landing in a plain string/int64
// Scan target would otherwise error the whole List. Only
// archived_at/archived_name stay genuinely nullable — NULL there is the
// normal, meaningful "not archived" state, scanned via sql.Null* on the Go
// side. Every INTEGER timestamp column is unix millis (matching the wire
// contract), noted per column; artifacts.mtime is caller-supplied millis,
// not stamped by this package.
const schemaV1 = `
CREATE TABLE jobs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	slug          TEXT NOT NULL,
	type          TEXT NOT NULL CHECK(type IN ('build','info')),
	repo          TEXT NOT NULL DEFAULT '',
	base          TEXT NOT NULL DEFAULT '',
	ready         INTEGER NOT NULL DEFAULT 0,
	solo          INTEGER NOT NULL DEFAULT 0,
	model         TEXT NOT NULL DEFAULT '',
	skip_doc      INTEGER NOT NULL DEFAULT 0,
	body          TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT '' CHECK(state IN ('','working','blocked','done')),
	outcome       TEXT NOT NULL DEFAULT '',
	session       TEXT NOT NULL DEFAULT '',
	teardown      TEXT NOT NULL DEFAULT '',
	run           INTEGER NOT NULL DEFAULT 1,
	rev           INTEGER NOT NULL DEFAULT 1,
	created_at    INTEGER NOT NULL DEFAULT 0, -- unix millis
	updated_at    INTEGER NOT NULL DEFAULT 0, -- unix millis
	archived_at   INTEGER NULL,               -- unix millis; NULL means not archived
	archived_name TEXT NULL,                  -- NULL means not archived
	-- L6: file mode's jobs.Job.Err (ParseJob's parse-failure detail, which
	-- pins a job to "draft" in deriveState regardless of ready/state) had no
	-- store-mode equivalent — a store-backed job with garbage raw content
	-- saved clean instead of pinning to draft like an unparseable file does.
	-- Added by editing schemaV1 in place rather than a new migrations[2]
	-- entry: this column shipped before any real kraken.db existed, so
	-- there was nothing to migrate forward at the time. Schema changes
	-- since then (e.g. the questions table) can't assume that anymore and
	-- append migrations[N] instead — see the schemaVersion doc comment.
	err           TEXT NOT NULL DEFAULT ''
);

-- Archive is a column, not a namespace: slug uniqueness is enforced only
-- among active (non-archived) rows, and archived_name uniqueness only
-- among archived rows — mirrors freeArchiveName's numbered de-dup.
CREATE UNIQUE INDEX jobs_active_slug ON jobs(slug) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX jobs_archived_name ON jobs(archived_name) WHERE archived_at IS NOT NULL;

CREATE TABLE prereqs (
	job_id      INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	prereq_slug TEXT NOT NULL,
	gated       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job_id, prereq_slug)
);

CREATE TABLE prs (
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	url    TEXT NOT NULL,
	state  TEXT NOT NULL CHECK(state IN ('open','merged','closed')),
	-- watchPRs watermarks (jobs/daemon.go): RFC3339 newest-seen comment
	-- time and the last CI SHA already bumped for. Bookkeeping, not a job
	-- mutation — SetPRWatermark deliberately does not bump jobs.rev.
	last_comment_at TEXT NOT NULL DEFAULT '',
	bumped_sha      TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (job_id, url)
);

CREATE TABLE board_order (
	"column"  TEXT NOT NULL,
	position  INTEGER NOT NULL,
	slug      TEXT NOT NULL,
	PRIMARY KEY ("column", position)
);

CREATE TABLE events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       INTEGER NOT NULL DEFAULT 0, -- unix millis
	src      TEXT NOT NULL DEFAULT '',
	level    TEXT NOT NULL DEFAULT '',
	event    TEXT NOT NULL DEFAULT '',
	msg      TEXT NOT NULL DEFAULT '',
	job_slug TEXT NOT NULL DEFAULT '' -- '' means not tied to a job (e.g. a daemon-start line)
);

CREATE TABLE uploads (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id     INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	mime       TEXT NOT NULL DEFAULT '',
	bytes      BLOB,
	created_at INTEGER NOT NULL DEFAULT 0, -- unix millis
	UNIQUE(job_id, name)
);

-- Blob-canonical, path relative to the job's artifacts dir, subdirs
-- preserved in path. SyncSet is the true-mirror sync: a row not present in
-- the given set is deleted.
CREATE TABLE artifacts (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	path   TEXT NOT NULL,
	bytes  BLOB,
	mtime  INTEGER NOT NULL DEFAULT 0, -- caller-supplied unix millis
	UNIQUE(job_id, path)
);

CREATE TABLE feedback (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	job_slug     TEXT NOT NULL DEFAULT '',
	artifact     TEXT NOT NULL DEFAULT '',
	snapshot     BLOB,
	snapshot_ext TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT '',
	ts           INTEGER NOT NULL DEFAULT 0 -- unix millis
);
CREATE INDEX feedback_job_slug_ts ON feedback(job_slug, ts);

CREATE TABLE drafts (
	job_slug   TEXT PRIMARY KEY NOT NULL,
	text       TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL DEFAULT 0 -- unix millis
);

CREATE TABLE kv (
	key   TEXT PRIMARY KEY NOT NULL,
	value TEXT NOT NULL DEFAULT ''
);
`

// schemaV2 adds the doc-questions table. It could not be folded into
// schemaV1 in place (the way the jobs.err column was): a real kraken.db
// already existed at user_version 1 without this table by the time the
// doc-questions feature landed, and migrate() only ever applies versions
// above the database's current one — an in-place edit to schemaV1 would
// never run against that database. This is the pattern every future schema
// change should follow: append migrations[N+1], never edit an already-
// released schemaV<N> string in place.
const schemaV2 = `
-- Doc-questions file, blob-canonical like artifacts: one row per job,
-- questions.yaml bytes, synced on the same triggers as the artifacts table
-- (jobs/sync.go).
CREATE TABLE questions (
	job_id INTEGER PRIMARY KEY NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	bytes  BLOB,
	mtime  INTEGER NOT NULL DEFAULT 0 -- caller-supplied unix millis
);
`

// schemaV3 drops jobs.type (Q14): go-sqlite3 bundles a modern SQLite with
// native ALTER TABLE ... DROP COLUMN support (no rebuild-the-table dance
// needed), and the column carried a CHECK constraint but no index, so
// dropping it is the entire migration. Like schemaV2, this could not be
// folded into schemaV1 in place — a real kraken.db already exists with the
// column by the time this shipped.
const schemaV3 = `
ALTER TABLE jobs DROP COLUMN type;
`

// schemaV4 adds watchPRs' third watermark: the PR's last-known merge-
// conflict state (empty never fetched / 'clean' / 'conflicting'), bumped-for
// on the clean→conflicting transition. Sibling of last_comment_at/
// bumped_sha — bookkeeping, not a job mutation, so SetPRWatermark still
// doesn't bump jobs.rev. Like schemaV2/V3, appended rather than folded into
// schemaV1: real kraken.db files already exist without the column.
const schemaV4 = `
ALTER TABLE prs ADD COLUMN conflict_state TEXT NOT NULL DEFAULT '';
`

// schemaV5 (54-feedback-contract.md, Q11/Q14 steering): drops jobs.skip_doc
// (the doc phase is zeus' own call from the spec text, never a stored
// flag); extends feedback with the columns the new addressed/session
// contract needs; and restructures questions from a blob-canonical mirror
// of questions.yaml into real relational rows. The restructure can't be
// pure SQL — the old blob bytes need the hand-rolled questions.yaml parser
// (in Go) to become rows — so this is the first migration that pairs SQL
// with a Go hook (see migrationStep, migrateV5Questions below): the SQL
// below only gets the shape ready (rename the blob table out of the way,
// create the new relational one), and migrateV5Questions does the actual
// parse-and-insert plus the final DROP of the renamed blob table, all
// inside the same migrate() transaction.
const schemaV5 = `
ALTER TABLE jobs DROP COLUMN skip_doc;

ALTER TABLE feedback ADD COLUMN session TEXT NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN addressed TEXT NOT NULL DEFAULT '';
ALTER TABLE feedback ADD COLUMN addressed_at TEXT NOT NULL DEFAULT '';

ALTER TABLE questions RENAME TO questions_blob;

-- Relational doc-questions, one row per question (replaces the blob mirror
-- of questions.yaml): position preserves the author's file/list order;
-- answer/answered_at are the kraken-owned columns (written only when carl
-- answers from the board — zeus-owned writes must preserve them).
CREATE TABLE questions (
	job_id         INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	qid            TEXT NOT NULL,
	text           TEXT NOT NULL DEFAULT '',
	context        TEXT NOT NULL DEFAULT '',
	importance     TEXT NOT NULL DEFAULT '',
	recommendation TEXT NOT NULL DEFAULT '',
	answer         TEXT NOT NULL DEFAULT '',
	answered_at    TEXT NOT NULL DEFAULT '',
	position       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job_id, qid)
);
`

// schemaV6 (B2): adds feedback.job_id, an immutable FK to jobs.id used only
// to make ArchiveJob/UnarchiveJob's re-key of feedback.job_slug unambiguous
// (see ArchiveJob's doc comment) — every read path keeps using job_slug
// exactly as before, this column is write-side plumbing only. ON DELETE SET
// NULL rather than CASCADE: DeleteJob's contract is that a feedback row
// survives its job's hard-delete as a fossil (same as an events row does),
// so the column nulls out instead of taking the row with it. go-sqlite3's
// bundled SQLite supports ALTER TABLE ... ADD COLUMN with a REFERENCES
// clause directly, same as schemaV3/schemaV4/schemaV5's in-place ALTERs — no
// rebuild-the-table dance needed.
const schemaV6 = `
ALTER TABLE feedback ADD COLUMN job_id INTEGER REFERENCES jobs(id) ON DELETE SET NULL;
`

// schemaV7 adds the threads table: one row per side thread — a forked
// claude session branched off a job's zeus, living in a second tmux window
// of the same cell. The row is a POINTER, not a transcript: session_id
// locates the real conversation under ~/.claude/projects later, so there is
// deliberately no snapshot blob here (and no cap on threads per job).
// pane/window are the live tmux coordinates and are cleared on close, so a
// 'closed' row keeps only its identity and provenance. Appended rather than
// folded into schemaV1 for the reason schemaV2's doc comment spells out:
// real kraken.db files already exist at earlier versions.
//
// The unique index on (job_id, tid) is what makes NextThreadID's
// MAX(suffix)+1 safe to insert against — a duplicate tid is refused by the
// database, not just by the Go-side check (threads.go's AddThread).
const schemaV7 = `
CREATE TABLE threads (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id         INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	tid            TEXT NOT NULL DEFAULT '',
	title          TEXT NOT NULL DEFAULT '',
	session_id     TEXT NOT NULL DEFAULT '',
	parent_session TEXT NOT NULL DEFAULT '',
	fork_uuid      TEXT NOT NULL DEFAULT '',
	cwd            TEXT NOT NULL DEFAULT '',
	pane           TEXT NOT NULL DEFAULT '',
	-- "window" is quoted because WINDOW is a SQLite keyword (window
	-- functions); an unquoted column of that name parses in a CREATE TABLE
	-- but breaks in expressions. Same treatment as board_order."column" in
	-- schemaV1 — quote it in the DDL so every use site copies the quoting.
	"window"       TEXT NOT NULL DEFAULT '',
	state          TEXT NOT NULL DEFAULT 'live' CHECK(state IN ('live','closed')),
	seed           TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL DEFAULT 0, -- unix millis
	closed_at      INTEGER NOT NULL DEFAULT 0  -- unix millis; 0 means still live
);
CREATE UNIQUE INDEX threads_job_tid ON threads(job_id, tid);
`

// schemaV8 adds jobs.color: the job's chosen Claude Code prompt-bar colour,
// one of /color's eight names, empty meaning no colour at all (NOT a
// default to auto-assign). Appended rather than folded into schemaV1 for
// the reason schemaV2's doc comment spells out: real kraken.db files
// already exist at earlier versions. The empty-string DEFAULT is what makes
// the migration total — every pre-existing row lands on "no colour", which is
// exactly the behaviour those jobs had before the column existed.
const schemaV8 = `
ALTER TABLE jobs ADD COLUMN color TEXT NOT NULL DEFAULT '';
`

// schemaV9 adds the dream loop's two pieces, both pure additions with
// nothing to move forward (no Go hook). Appended rather than folded into an
// earlier schemaV<N> for the reason schemaV2's doc comment spells out: real
// kraken.db files already exist at earlier versions, and migrate() only ever
// applies versions ABOVE the database's current one.
//
// feedback_ts exists because schemaV1's only feedback index is
// (job_slug, ts) with job_slug LEADING, and a composite index can only be
// seeked on a prefix of its columns — so the nightly sweep's unscoped
// `WHERE ts >= ?` across ALL jobs (FeedbackSince/CountFeedbackSince,
// feedback.go) can't use it at all and degrades to a full table scan. A
// second index with ts leading is what makes that read a seek.
//
// watchlist deliberately has NO job_id, no FK, and no cascade — the one
// table here that isn't scoped to a job. A watchlist row is an observation
// the nightly pass is TRACKING (a recurring correction, a wasted step, a
// prompting pattern), and its whole point is that it OUTLIVES the job that
// happened to surface it: the same observation recurs across many jobs, and
// a row must survive its originating job's teardown, archive, and even
// hard-delete. Attaching it to a job would make it cascade away exactly when
// it has finally been seen often enough to matter.
//
// The (state, last_seen) index serves the only listing this table has:
// ListWatchItems filters on state and orders by last_seen DESC, so both
// columns are covered in the order the query uses them.
//
// watchlist_observation_key is an EXPRESSION index, not a plain column one,
// and it is deliberately written as the exact expression FindWatchItem
// (watchlist.go) puts in its WHERE clause. That coupling is the point — the
// normalisation rule is one expression, so the lookup and the index cannot
// drift into disagreeing about which rows are "the same observation"; the
// EXPLAIN QUERY PLAN assertion in watchlist_test.go fails loudly if they do,
// since a drifted pair still returns correct answers and merely stops using
// the index.
//
// char(32,9,10,13) is the trim set — space, tab, newline, carriage return.
// Bare TRIM() strips SPACES ONLY, which is not enough here: the caller is an
// LLM and a trailing newline on a generated string is entirely ordinary, so
// the default would miss the repeat and insert a duplicate — the exact
// failure FindWatchItem exists to prevent. char() and TRIM() are both
// deterministic, which is what lets the expression be indexed at all. It is
// not UNIQUE: rows
// predating the lookup (or written by a caller that never looked first) may
// already collide, and a UNIQUE index would turn that into a failed INSERT
// on the nightly pass rather than the duplicate FindWatchItem is there to
// prevent in the first place.
const schemaV9 = `
CREATE INDEX feedback_ts ON feedback(ts);

CREATE TABLE watchlist (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	observation   TEXT NOT NULL DEFAULT '',
	class         TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT 'watching',
	sightings     INTEGER NOT NULL DEFAULT 1,
	first_seen    INTEGER NOT NULL DEFAULT 0, -- unix millis
	last_seen     INTEGER NOT NULL DEFAULT 0, -- unix millis
	evidence      TEXT NOT NULL DEFAULT '',
	suggested_fix TEXT NOT NULL DEFAULT '',
	pr_url        TEXT NOT NULL DEFAULT '',
	updated_at    INTEGER NOT NULL DEFAULT 0, -- unix millis
	-- carl-owned trio: only SetWatchVerdict ever writes these, exactly as
	-- questions.answer/answered_at are kraken-owned (questions.go).
	verdict       TEXT NOT NULL DEFAULT '',
	comment       TEXT NOT NULL DEFAULT '',
	commented_at  TEXT NOT NULL DEFAULT '' -- RFC3339
);
CREATE INDEX watchlist_state_last_seen ON watchlist(state, last_seen);
CREATE INDEX watchlist_observation_key ON watchlist(LOWER(TRIM(observation, char(32,9,10,13))));
`

// migrationStep is one migration: SQL applied first, then an optional Go
// hook run afterward inside the SAME transaction. Every migration before
// v5 was a pure SQL string — DDL can't parse questions.yaml's hand-rolled
// format, so v5's blob-to-rows restructure needed an escape hatch into Go.
// Wrapping v1-v4 as migrationStep{sql: schemaVN} (hook left nil) changes
// nothing about how they run: migrate() below still just execs the string
// and moves on when hook is nil, byte-identical to the old map[int]string
// behavior.
type migrationStep struct {
	sql  string
	hook func(tx *sql.Tx) error
}

// migrations maps a target user_version to the step that gets a freshly
// created (or older) database there. migrations[1] is the original
// release; migrations[2] adds the questions table; migrations[3] drops
// jobs.type; migrations[4] adds prs.conflict_state; migrations[5] drops
// jobs.skip_doc, extends feedback, and restructures questions into
// relational rows (migrateV5Questions below); migrations[6] adds
// feedback.job_id and backfills it for existing rows (migrateV6FeedbackJobID
// below); migrations[7] adds the threads table (no hook — a brand-new table
// has nothing to migrate forward); migrations[8] adds jobs.color (no hook —
// the column's empty-string DEFAULT is the whole backfill); migrations[9] adds
// the watchlist table and the feedback(ts) index (no hook, same reason).
// Future schema changes append
// migrations[10], migrations[11], ... rather than editing an existing
// schemaV<N> string (or hook) in place, so a database created under an older
// binary still migrates forward correctly.
var migrations = map[int]migrationStep{
	1: {sql: schemaV1},
	2: {sql: schemaV2},
	3: {sql: schemaV3},
	4: {sql: schemaV4},
	5: {sql: schemaV5, hook: migrateV5Questions},
	6: {sql: schemaV6, hook: migrateV6FeedbackJobID},
	7: {sql: schemaV7},
	8: {sql: schemaV8},
	9: {sql: schemaV9},
}

// migrateV5Questions is migrations[5]'s Go hook: schemaV5's SQL has already
// renamed the old blob table to questions_blob and created the new
// relational questions table by the time this runs. It reads every
// questions_blob row, parses its bytes with parseLegacyQuestions (a copy of
// jobs.ParseDocQuestions' logic — store cannot import jobs, see that
// function's doc comment), and inserts one questions row per parsed
// question, position stamped from the parsed item's order in the file (top
// to bottom, matching how questions.yaml was always read).
//
// carl's clean-cutover ruling (54-feedback-contract.md): migrations are
// one-time best-effort moves of existing data, never required to succeed
// perfectly. A blob that fails to parse is SKIPPED, not fatal — logged into
// the events table (in this same transaction, so the note only survives if
// the migration itself commits) and the migration continues with the next
// blob. Not routed through util.LogEvent: that forwards through eventSink,
// which jobs.UseStore wires to a *different*, already-open Store's
// AppendEvent — calling it here, mid-migration on a Store that isn't even
// finished opening yet, would be either a no-op (nothing wired this early)
// or circular, neither of which is what "log-and-continue" means here.
// Once every blob is handled, questions_blob is dropped — its job is done.
func migrateV5Questions(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT job_id, bytes FROM questions_blob`)
	if err != nil {
		return fmt.Errorf("read questions_blob: %w", err)
	}
	type blobRow struct {
		jobID int64
		bytes []byte
	}
	var blobs []blobRow
	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.jobID, &b.bytes); err != nil {
			rows.Close()
			return fmt.Errorf("scan questions_blob row: %w", err)
		}
		blobs = append(blobs, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read questions_blob rows: %w", err)
	}
	rows.Close()

	for _, b := range blobs {
		qs, perr := parseLegacyQuestions(b.bytes)
		if perr != nil {
			var slug string
			// Best-effort only: an orphaned blob row (no matching job)
			// shouldn't exist (questions_blob's job_id carried an
			// ON DELETE CASCADE FK), but if it somehow does, slug just
			// stays empty rather than failing the whole migration over a
			// cosmetic lookup.
			_ = tx.QueryRow(`SELECT slug FROM jobs WHERE id = ?`, b.jobID).Scan(&slug)
			if _, err := tx.Exec(
				`INSERT INTO events (ts, src, level, event, msg, job_slug) VALUES (?, ?, ?, ?, ?, ?)`,
				time.Now().UnixMilli(), "migrate", "error", "questions-migration-skip",
				fmt.Sprintf("job %d: legacy questions.yaml blob dropped, unparseable: %v", b.jobID, perr),
				slug,
			); err != nil {
				return fmt.Errorf("log skipped blob for job %d: %w", b.jobID, err)
			}
			continue
		}
		for i, q := range qs {
			if _, err := tx.Exec(
				`INSERT INTO questions (job_id, qid, text, context, importance, recommendation, answer, answered_at, position)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				b.jobID, q.ID, q.Text, q.Context, q.Importance, q.Recommendation, q.Answer, q.AnsweredAt, i,
			); err != nil {
				return fmt.Errorf("insert migrated question %s for job %d: %w", q.ID, b.jobID, err)
			}
		}
	}

	if _, err := tx.Exec(`DROP TABLE questions_blob`); err != nil {
		return fmt.Errorf("drop questions_blob: %w", err)
	}
	return nil
}

// migrateV6FeedbackJobID is migrations[6]'s Go hook: schemaV6's SQL has
// already added the (NULL-default) feedback.job_id column by the time this
// runs. Every existing row's job_slug is matched against both an active
// job's slug and an archived job's archived_name (each uniquely indexed —
// jobs_active_slug, jobs_archived_name — so at most one row can match on
// each side), and job_id is backfilled only when EXACTLY ONE side matches.
// A row whose slug matches NEITHER (its job was hard-deleted, or the row is
// otherwise an orphan) is left with job_id NULL, same "best-effort, log and
// continue" ruling as migrateV5Questions — it was already unreachable by id
// before this column existed, so nothing regresses.
//
// A row whose slug matches BOTH an active job and an archived job is
// genuinely ambiguous — the pre-v6 data can't tell which generation of a
// reused slug the row actually belongs to — and is ALSO left NULL rather
// than resolved to either side: silently attaching it to the active job
// would move it out from under the archived job's card the moment
// migrateV6FeedbackJobID's write-side sibling (ArchiveJob/UnarchiveJob's
// job_id rekey) runs again, which is strictly worse than the row simply not
// appearing on any card until a human sorts out the collision by hand.
func migrateV6FeedbackJobID(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, job_slug FROM feedback WHERE job_slug != ''`)
	if err != nil {
		return fmt.Errorf("read feedback rows for job_id backfill: %w", err)
	}
	type feedbackRow struct {
		id   int64
		slug string
	}
	var toBackfill []feedbackRow
	for rows.Next() {
		var r feedbackRow
		if err := rows.Scan(&r.id, &r.slug); err != nil {
			rows.Close()
			return fmt.Errorf("scan feedback row for job_id backfill: %w", err)
		}
		toBackfill = append(toBackfill, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read feedback rows for job_id backfill: %w", err)
	}
	rows.Close()

	for _, r := range toBackfill {
		var activeID int64
		err := tx.QueryRow(`SELECT id FROM jobs WHERE slug = ? AND archived_at IS NULL`, r.slug).Scan(&activeID)
		activeMatch := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("resolve active job for feedback %d (slug %q): %w", r.id, r.slug, err)
		}

		var archivedID int64
		err = tx.QueryRow(`SELECT id FROM jobs WHERE archived_name = ? AND archived_at IS NOT NULL`, r.slug).Scan(&archivedID)
		archivedMatch := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("resolve archived job for feedback %d (slug %q): %w", r.id, r.slug, err)
		}

		var jobID int64
		switch {
		case activeMatch && archivedMatch:
			continue // ambiguous: matches both a live job and an archived one — leave job_id NULL
		case activeMatch:
			jobID = activeID
		case archivedMatch:
			jobID = archivedID
		default:
			continue // orphan: no active or archived job currently matches — leave job_id NULL
		}
		if _, err := tx.Exec(`UPDATE feedback SET job_id = ? WHERE id = ?`, jobID, r.id); err != nil {
			return fmt.Errorf("backfill job_id for feedback %d: %w", r.id, err)
		}
	}
	return nil
}

// legacyQuestion is parseLegacyQuestions' output shape — field-for-field the
// same as jobs.DocQuestion, but store cannot import jobs (util ← store ←
// core ← jobs ← api ← web: importing upward would both invert the layering
// and create an import cycle), so this is its own copy, used only here.
// It is also deliberately decoupled from this package's own DocQuestion:
// migration code is frozen at what the v5 cutover needed, so a future
// DocQuestion field change can never silently alter how legacy blobs parse.
type legacyQuestion struct {
	ID             string
	Text           string
	Context        string
	Importance     string
	Recommendation string
	Answer         string
	AnsweredAt     string
}

var (
	legacyQuestionIDRe  = regexp.MustCompile(`^Q[0-9]+$`)
	legacyImportanceSet = map[string]bool{"P0": true, "P1": true, "P2": true}
)

// parseLegacyQuestions exists ONLY for the v5 migration (migrateV5Questions
// above): a trimmed copy of jobs.ParseDocQuestions' hand-rolled
// questions.yaml subset —
//
//	questions:
//	  - id: Q1
//	    text: ...
//	    context: ...
//	    importance: P0
//	    recommendation: ...
//	    answer: ...
//	    answered-at: ...
//
// — ported here because store cannot import jobs (see legacyQuestion's doc
// comment) to reuse the original parser directly. Deliberately less strict
// than the original: this runs once, against blob bytes that already
// passed the original strict parser when zeus/carl wrote them, so its job
// is "recover what's recoverable, in file order," not "re-validate from
// scratch." An unknown continuation key is ignored rather than rejected; a
// value is trimmed of surrounding whitespace but not otherwise sanitized.
// What it still refuses — a missing/malformed header, a bad or duplicate
// id, a value line outside any item, a stray/misindented line, a missing
// text, or a bad importance — mirrors the shapes the original parser could
// never have written in the first place, so hitting one of those means the
// blob is genuinely corrupt, not just reformatted; the caller
// (migrateV5Questions) skips that blob entirely rather than guessing at a
// partial recovery. An empty file, or one that's only blank lines/
// full-line comments, is a valid "no questions" state: nil, nil.
func parseLegacyQuestions(data []byte) ([]legacyQuestion, error) {
	s := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")

	headerLine := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		headerLine = i
		break
	}
	if headerLine < 0 {
		return nil, nil
	}
	if strings.TrimRight(lines[headerLine], " \t") != "questions:" {
		return nil, fmt.Errorf("line %d: expected \"questions:\" header, got %q", headerLine+1, lines[headerLine])
	}

	var out []legacyQuestion
	seen := map[string]bool{}
	var cur *legacyQuestion

	finish := func() error {
		if cur == nil {
			return nil
		}
		if cur.Text == "" {
			return fmt.Errorf("item %s missing text", cur.ID)
		}
		if !legacyImportanceSet[cur.Importance] {
			return fmt.Errorf("item %s bad importance %q", cur.ID, cur.Importance)
		}
		out = append(out, *cur)
		cur = nil
		return nil
	}

	isItemStart := func(line string) bool {
		return len(line) > 2 && line[0] == ' ' && line[1] == ' ' && line[2] != ' ' && strings.HasPrefix(line[2:], "- id:")
	}
	isContinuation := func(line string) bool {
		return len(line) > 4 && line[:4] == "    " && line[4] != ' ' && line[4] != '\t'
	}

	for i := headerLine + 1; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}

		switch {
		case isItemStart(line):
			if err := finish(); err != nil {
				return nil, err
			}
			rest := strings.TrimPrefix(line[2:], "- ")
			key, val, ok := strings.Cut(rest, ":")
			if !ok || strings.TrimSpace(key) != "id" {
				return nil, fmt.Errorf("line %d: item must start with \"- id: Qn\", got %q", i+1, t)
			}
			id := strings.TrimSpace(val)
			if !legacyQuestionIDRe.MatchString(id) {
				return nil, fmt.Errorf("line %d: bad id %q, want Q<digits>", i+1, id)
			}
			if seen[id] {
				return nil, fmt.Errorf("line %d: duplicate id %q", i+1, id)
			}
			seen[id] = true
			cur = &legacyQuestion{ID: id}

		case isContinuation(line):
			if cur == nil {
				return nil, fmt.Errorf("line %d: value line outside any item %q", i+1, t)
			}
			key, val, ok := strings.Cut(line[4:], ":")
			if !ok {
				return nil, fmt.Errorf("line %d: bad line %q", i+1, t)
			}
			v := strings.TrimSpace(val)
			switch strings.TrimSpace(key) {
			case "text":
				cur.Text = v
			case "context":
				cur.Context = v
			case "importance":
				cur.Importance = v
			case "recommendation":
				cur.Recommendation = v
			case "answer":
				cur.Answer = v
			case "answered-at":
				cur.AnsweredAt = v
			}

		default:
			return nil, fmt.Errorf("line %d: stray/misindented line %q", i+1, t)
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return out, nil
}

// migrate reads the database's current PRAGMA user_version and applies
// every migration above it in order, all inside one BEGIN IMMEDIATE
// transaction: the immediate lock is acquired up front (via the DSN's
// _txlock=immediate, see Open) so two processes/connections racing to
// migrate the same fresh file serialize on that lock — subject to
// _busy_timeout — rather than colliding mid-upgrade. user_version is
// re-read inside the transaction so a migration that lost the race to
// acquire the lock sees the winner's already-applied version and no-ops,
// instead of re-applying (or erroring on) DDL that already ran.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migrate: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	// A db written by a newer binary (user_version > schemaVersion) must
	// never be opened: migrate()'s loop below is a strict no-op in that
	// case (version+1 > schemaVersion), so without this guard an old binary
	// would silently proceed to query columns/tables a later migration
	// added or removed — surfacing as confusing deep-query failures (e.g.
	// "no such column") instead of a clear upgrade message at Open().
	if version > schemaVersion {
		return fmt.Errorf("kraken.db is schema v%d but this binary only knows v%d — upgrade the binary", version, schemaVersion)
	}
	for v := version + 1; v <= schemaVersion; v++ {
		step, ok := migrations[v]
		if !ok {
			return fmt.Errorf("no migration registered for version %d", v)
		}
		if _, err := tx.Exec(step.sql); err != nil {
			return fmt.Errorf("apply migration %d: %w", v, err)
		}
		if step.hook != nil {
			if err := step.hook(tx); err != nil {
				return fmt.Errorf("apply migration %d hook: %w", v, err)
			}
		}
		// PRAGMA user_version doesn't accept a bound parameter; v is our
		// own int constant, never user input.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
			return fmt.Errorf("set user_version %d: %w", v, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrate: %w", err)
	}
	return nil
}
