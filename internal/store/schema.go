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
// FeedbackSince). Version 10 adds the per-job token counters and their
// per-file byte watermarks (tokens.go). Version 11 splits those counters into
// a zeus half and a subagent half, and WIPES both token tables so the daemon
// re-sums every job with the split in place (see schemaV11). Version 12
// finishes what version 6 started: a job's identity is jobs.id everywhere
// below the HTTP handler, so board_order and the two token tables move off the
// mutable slug onto the immutable id, events GAINS a job_id beside its
// job_slug fossil, and the never-used drafts table is dropped. Version 13
// adds the four job-load columns — zeus' unblock/scope estimate of what a
// blocked job costs carl and buys him (see schemaV13). Version 14 WIPES both
// token tables again — the group-collapse rule was taking output_tokens from
// the first record of a message where a subagent sidecar only reports the
// total on the last (see schemaV14).
const schemaVersion = 14

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

// schemaV10 adds the per-job TOKEN counters — the four numbers Claude Code
// stamps on every assistant record, summed across all of a job's own
// transcripts (core/tokens.go) — plus the per-file byte watermarks that make
// summing them incremental. Appended rather than folded into an earlier
// schemaV<N> for the reason schemaV2's doc comment spells out.
//
// "tokens", never "usage": `usage` in this binary already means ACCOUNT
// QUOTA end to end (/api/usage, ~/.kraken/usage.json, core/usage.go,
// core/usagepool.go), and colliding on it would make two unrelated numbers
// indistinguishable in every future grep.
//
// TWO TABLES rather than one, and the split is by READER. job_tokens is what
// the board reads — one row per job, ~74 of them, on a payload that rebuilds
// roughly every two seconds. job_token_files is the daemon's own bookkeeping
// and holds one row per TRANSCRIPT FILE, an order of magnitude more rows
// (a cell's project dir routinely holds 30+ once subagent sidecars are
// counted). Folding the watermarks into the same table the board sweeps
// would put that multiplier in front of the hottest read in the process for
// no benefit; the daemon is the only thing that ever needs a watermark.
//
// NEITHER table has a job_id, an FK, or a cascade, and that is deliberate in
// the way watchlist's absence of one is (schemaV9). The key is the job SLUG
// because the slug is what the measured thing is actually keyed on: a job's
// transcripts live in ~/.claude/projects/<munged ~/.kraken/cells/<slug>/wt-*>,
// a path derived from the slug alone. Two jobs that share a slug share that
// directory and their transcripts are physically inseparable, so a row here
// means "everything burned in this slug's cell dir" — which is also why an
// archived job's row must not cascade away: teardown deletes the cell dir
// (jobs/cell.go's teardownCell) but Claude Code keeps the transcripts
// indefinitely, and retrospective cost is the entire point of storing this.
//
// THE SLUG-KEYING ARGUMENT ABOVE IS NO LONGER TRUE, and schemaV12 is what
// changed it. A cell dir is now `<slugified slug>-<id>` (jobs/cell.go's
// cellName), so the measured directory is derived from the slug AND the id,
// and two jobs sharing a slug no longer share a directory — the premise the
// whole paragraph rests on. Keeping the tables slug-keyed after that would
// have preserved exactly the defect the id was added to remove: five archived
// jobs slugged `asdf` measured into one row that honestly belongs to none of
// them. So v12 re-keys both tables onto job_id, still with no FK and no
// cascade, for the SECOND half of the argument above, which is untouched: an
// archived or hard-deleted job's number must survive, because retrospective
// cost is the point.
//
// One consequence is permanent and is not a defect to be fixed later:
// transcripts written BEFORE the cutover are filed by Claude Code under the
// legacy bare-slug path, where same-slug jobs genuinely did share a directory.
// Their spend is inseparable in the source data, not merely in the schema.
// jobs/tokens.go's JobProjectDirs still sums that legacy base — dropping it
// would report zero for every job's entire pre-cutover history — so a job that
// once shared a slug carries some of its predecessor's old spend. Attributing
// it any other way would require information that was never recorded.
//
// updated_at is stamped only when a refresh actually FOUND something, so a
// settled job's row keeps the timestamp of its last real spend rather than
// creeping forward on every no-op pass.
const schemaV10 = `
CREATE TABLE job_tokens (
	job_slug    TEXT PRIMARY KEY NOT NULL,
	in_tokens   INTEGER NOT NULL DEFAULT 0, -- input_tokens
	out_tokens  INTEGER NOT NULL DEFAULT 0, -- output_tokens
	cache_write INTEGER NOT NULL DEFAULT 0, -- cache_creation_input_tokens
	cache_read  INTEGER NOT NULL DEFAULT 0, -- cache_read_input_tokens
	sessions    INTEGER NOT NULL DEFAULT 0, -- transcript FILES summed (root + sidecars)
	updated_at  INTEGER NOT NULL DEFAULT 0  -- unix millis
);

-- One row per transcript file: how many of its bytes have already been
-- summed into the job_tokens row above. Transcripts are append-only, so the
-- next pass reads only past this and a settled job costs one stat per file.
-- byte_offset rather than the obvious name because OFFSET is a SQLite
-- keyword and would need quoting at every use site.
CREATE TABLE job_token_files (
	job_slug    TEXT NOT NULL,
	path        TEXT NOT NULL, -- absolute transcript path
	byte_offset INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job_slug, path)
);
`

// schemaV11 splits the per-job token spend into the part the cell's LEAD
// agent burned and the part everything it delegated to did: four zeus columns
// shadowing the four totals schemaV10 created. Only the zeus half is stored —
// the subagent half is total-minus-zeus, derived by whoever renders it,
// because two stored halves can drift out of agreement with the total they
// are supposed to add up to and a derived one cannot.
//
// AND IT WIPES BOTH TOKEN TABLES, which is the load-bearing half of this
// migration rather than a convenience. The counters only ever accumulate
// FORWARD from the per-file byte watermarks in job_token_files: a pass adds
// what it finds past each offset and nothing ever re-reads what is behind one.
// So a row that already holds a job's entire history with the four new columns
// defaulted to zero would report that job as 100% subagent FOREVER — there is
// no pass anywhere that could backfill it, because the bytes those totals came
// from are all behind the watermarks. Emptying both tables makes the next
// daemon pass re-sum every job from byte zero with the classification in
// place, which is the only way the number is ever right.
//
// What that costs, said out loud rather than discovered later: a few minutes
// of paced background work (jobs with a live cell are re-summed every tick,
// everything else 8 idle jobs per 15-second tick with each job re-checked at
// most every 10 minutes — jobs/tokens.go; the 10 minutes is a per-JOB floor,
// not a batch rate, so a wipe drains in minutes rather than the hour and a
// half the other reading implies), and every job's
// `updated_at` is re-stamped as its re-scan lands, so the board's "measured Nh
// ago" line resets once for every job. Both tables are pure derived state, so
// the re-scan restores them in full for any job whose transcripts are still on
// disk — which is every job today, the oldest cell dir here being well inside
// Claude Code's own `cleanupPeriodDays` (30 by default, unset on this machine).
// That qualifier is the whole truth and it matters: ~/.claude/projects is not
// kraken's to keep, so a job whose transcripts have since been pruned would
// come back from a wipe at zero rather than at its old number. Anyone reading
// this as precedent for a future wipe should read that sentence, not the
// shorter one.
//
// A clean one-time cutover with no dual-mode guard, per this repo's standing
// ruling: one machine, one database, one move.
//
// Appended rather than folded into schemaV10 for the reason schemaV2's doc
// comment spells out — real kraken.db files already exist at v10.
const schemaV11 = `
ALTER TABLE job_tokens ADD COLUMN zeus_in          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE job_tokens ADD COLUMN zeus_out         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE job_tokens ADD COLUMN zeus_cache_write INTEGER NOT NULL DEFAULT 0;
ALTER TABLE job_tokens ADD COLUMN zeus_cache_read  INTEGER NOT NULL DEFAULT 0;

-- Not a cleanup: see the doc comment. Accumulate-forward counters cannot be
-- backfilled, so the pre-split rows have to go along with the watermarks that
-- would otherwise stop the re-sum from happening.
DELETE FROM job_tokens;
DELETE FROM job_token_files;
`

// schemaV12 moves a job's identity off the mutable SLUG and onto the
// immutable jobs.id for every table that still keyed on the string, drops the
// drafts table, and clears both token tables. Appended rather than folded into
// an earlier schemaV<N> for the reason schemaV2's doc comment spells out: real
// kraken.db files already exist at earlier versions — and in this case at
// exactly v11, since that is what ~/.kraken/kraken.db is already running.
//
// v11 vs v12, so the numbering reads honestly: v11 is the zeus/subagent TOKEN
// SPLIT and it is not this change's work at all; v12 is the id re-key. They
// landed on separate branches and both touch job_tokens, which is the one
// place they interact — see the CREATE below.
//
// WHY, in one sentence: a slug is unique only among ACTIVE jobs
// (jobs_active_slug), archiving deliberately FREES it, and every table below
// keyed on that freed string — so an archived job and the new job that reused
// its name were, to these tables, one job.
//
// board_order: slug -> job_id, no FK and no cascade. No FK specifically
// because a stale entry must not VANISH: SetBoardColumn's cross-column
// dedupe and ApplyColumnOrder both tolerate an id that no longer resolves
// (it is simply not a member and is pruned on the column's next save),
// whereas a cascade firing mid-drag would silently reshuffle the board under
// carl's cursor. The table is rebuilt rather than ALTERed because the column
// changes TYPE and the rows must be re-keyed by the hook; the old table is
// renamed out of the way here and dropped by the hook once it has been read,
// the same shape schemaV5 used for questions_blob.
//
// events: job_id is ADDED and job_slug is KEPT. This is the one table where
// the slug stays a first-class stored value, because an events row is a
// FOSSIL — it deliberately outlives its job's hard delete (see schemaV6's
// ON DELETE SET NULL reasoning, and DeleteJob's contract) — and once the job
// row is gone the id resolves to nothing while the slug still says which job
// the line was about. Nullable, no FK, no cascade, for exactly that reason.
//
// job_tokens / job_token_files: re-keyed to job_id, and EVERY EXISTING ROW IS
// DROPPED rather than mapped. That is Q4 ("we can just regenerate all token
// counters") and it is also the only honest option: on this machine five
// archived jobs share the slug `asdf`, so their single row measures all five
// together and there is no mapping — to any one of the five, or split among
// them — that is true. The daemon's existing throttled pass (jobs/tokens.go's
// tokensPass) re-sums from scratch afterwards, which is why the drop is
// affordable: re-summing inside this transaction would mean re-reading 20-60
// MB per cell dir across ~110 jobs synchronously while kraken serve starts.
// The board reads zero for a few minutes after a deliberate overnight
// migration, which is the accepted cost. DROP + CREATE rather than a rebuild
// with a data move, because deleting the rows IS the migration.
//
// THE RECREATED job_tokens MUST CARRY v11's FOUR zeus_* COLUMNS. This is the
// one place the two migrations interact, and getting it wrong is silent in the
// worst way: v11 ADDs those columns to the v10 table, v12 DROPs that table, so
// a CREATE that lists only schemaV10's columns would quietly take the zeus
// half of the token split back out — and the code that reads it lives on the
// other branch, so nothing here would fail to compile. The columns are carried
// with no backfill and no data move, which is exactly right: v11 wipes every
// row and v12 wipes them again, so there is nothing to carry, only a shape to
// preserve.
//
// drafts: DROPPED. It has zero non-test callers and zero rows on the live
// database — /api/draft is served entirely out of api/draft.go's in-memory
// draftStore, which is explicitly never persisted. Nothing reads this table,
// so nothing can miss it.
const schemaV12 = `
ALTER TABLE board_order RENAME TO board_order_slug;

CREATE TABLE board_order (
	"column"  TEXT NOT NULL,
	position  INTEGER NOT NULL,
	job_id    INTEGER NOT NULL,
	PRIMARY KEY ("column", position)
);

ALTER TABLE events ADD COLUMN job_id INTEGER;

DROP TABLE job_tokens;
DROP TABLE job_token_files;

CREATE TABLE job_tokens (
	job_id      INTEGER PRIMARY KEY NOT NULL,
	in_tokens   INTEGER NOT NULL DEFAULT 0, -- input_tokens
	out_tokens  INTEGER NOT NULL DEFAULT 0, -- output_tokens
	cache_write INTEGER NOT NULL DEFAULT 0, -- cache_creation_input_tokens
	cache_read  INTEGER NOT NULL DEFAULT 0, -- cache_read_input_tokens
	sessions    INTEGER NOT NULL DEFAULT 0, -- transcript FILES summed (root + sidecars)
	updated_at  INTEGER NOT NULL DEFAULT 0, -- unix millis
	-- v11's zeus half of the token split, carried across the DROP above. Not
	-- new here and not this migration's feature — see the doc comment: the
	-- four columns exist so the recreated table is v11's shape re-keyed, not
	-- schemaV10's shape resurrected. The subagent half stays derived
	-- (total minus zeus) and is deliberately not a column.
	zeus_in          INTEGER NOT NULL DEFAULT 0,
	zeus_out         INTEGER NOT NULL DEFAULT 0,
	zeus_cache_write INTEGER NOT NULL DEFAULT 0,
	zeus_cache_read  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE job_token_files (
	job_id      INTEGER NOT NULL,
	path        TEXT NOT NULL, -- absolute transcript path
	byte_offset INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job_id, path)
);

DROP TABLE drafts;
`

// schemaV13 adds the job LOAD estimate: the two 1-10 numbers zeus writes for
// a blocked job — load_unblock (how much effort carl must spend to unblock
// it) and load_scope (how big the claude work that reply unblocks is likely
// to be) — plus the one-line rationale and the millis stamp of when they were
// written. Four columns on `jobs` rather than a table of their own: this is
// state ABOUT the job with exactly the job's lifetime, one row per job at
// most, and the board already selects every job row on every build — a
// separate table would add a join (or a second sweep) to the hottest read in
// the process for no gain.
//
// Appended rather than folded into an earlier schemaV<N> for the reason
// schemaV2's doc comment spells out: real kraken.db files already exist at
// earlier versions, and migrate() only ever applies versions ABOVE the
// database's current one.
//
// NO GO HOOK, and the DEFAULTS are why. Every pre-existing job lands on
// unblock 0 / scope 0 / an empty note / at 0, which is not a placeholder standing
// in for a real value — it is the feature's own "never estimated" state,
// spelled exactly as a clear writes it. Zero for BOTH numbers is the clear
// (a half estimate is refused at the API layer, since scope/unblock has no
// meaning when one side is zero), and load_at 0 is what tells a reader the
// pair was never written rather than written as zero. So the migration is
// total on arrival with nothing left to backfill: there is no earlier place
// an estimate could have been recorded, and nothing anywhere could reconstruct
// one after the fact.
//
// The ratio the board renders (scope / unblock) is deliberately NOT a column:
// two stored halves and a stored quotient can drift out of agreement and a
// derived one cannot — the same reasoning job_tokens' zeus/subagent split
// follows in schemaV11.
const schemaV13 = `
ALTER TABLE jobs ADD COLUMN load_unblock INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN load_scope   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN load_note    TEXT    NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN load_at      INTEGER NOT NULL DEFAULT 0; -- unix millis; 0 means never estimated
`

// schemaV14 changes NO shape at all: it clears both token tables so the
// daemon re-sums every job from byte zero. Every stored counter was measured
// under a rule that is now known to be wrong for one of the four.
//
// The rule was: one API message is written as one record per content block,
// all four counters stamped identically on each, so keep the FIRST record of a
// group and skip the rest. Measured over every kraken cell transcript on this
// machine as of 2026-07-30 (the counts below are that snapshot's and drift as
// live cells append; the zeros and ratios do not), that is exactly true for
// `input_tokens`,
// `cache_creation_input_tokens` and `cache_read_input_tokens` — 0 of 39,818
// multi-record groups vary in any of them — and it is true for
// `output_tokens` in ROOT transcripts only. In a SUBAGENT SIDECAR the leading
// records of a group carry a streaming PARTIAL and the last carries the
// message's total, so first-wins kept the partial: 4,022 of 4,444 sidecar
// groups affected, 31,832 output tokens recorded where 4,621,522 were spent.
// core.addTokenLine now takes a group's output from its last record; this
// migration is what lets that reach the numbers already stored.
//
// WHY A WIPE RATHER THAN A CORRECTION, which is the same argument schemaV11
// makes and is repeated rather than cross-referenced because it is the whole
// justification: the counters only ever accumulate FORWARD from the per-file
// byte watermarks in job_token_files. A pass adds what it finds past each
// offset and nothing ever re-reads what is behind one, so a wrong figure
// already behind a watermark can never be revisited — there is no pass
// anywhere that could correct it, and the error is not a scale factor that
// could be divided out either (it lands entirely on delegated work, so it
// ranges from nothing to several-fold PER JOB). Emptying both tables makes the
// next daemon pass re-sum every job from byte zero under the fixed rule, which
// is the only way the number is ever right.
//
// Costs, said out loud rather than discovered later, exactly as schemaV11's
// wipe stated them: a few minutes of paced background work (jobs with a live
// cell re-summed every tick, everything else 8 idle jobs per 15-second tick
// with each job re-checked at most every 10 minutes — jobs/tokens.go), and one
// reset of every job's `updated_at` as its re-scan lands, so each card's
// "measured Nh ago" line restarts once.
//
// "A few minutes" is the arithmetic rather than a hedge, and it is worth
// spelling out because this is the sentence someone reads to decide whether a
// slow-looking recovery is a bug. tokensIdleBatch (8) is a per-TICK cap and
// DaemonInterval is 15s, so idle jobs drain at 8 per 15 seconds;
// tokensIdleInterval (10 minutes) is a per-JOB re-check floor, NOT a batch
// rate. A wipe makes every job due at once, so ~74 jobs clear in ~10 ticks —
// about 2.5 minutes. Reading the 10 minutes as the batch rate instead gives
// ~92 minutes, 37x too slow, and would make a healthy recovery look wedged.
//
// Both tables are pure derived state and the re-scan restores them in full for
// any job whose transcripts are still on disk. That qualifier is the whole truth:
// ~/.claude/projects is not kraken's to keep, so a job whose transcripts have
// since been pruned comes back from a wipe at zero rather than at its old
// number. Anyone citing this as precedent for a future wipe should read that
// sentence, not the shorter one.
//
// A clean one-time cutover with no dual-mode guard, per this repo's standing
// ruling: one machine, one database, one move.
//
// Appended rather than folded into schemaV11's identical DELETEs for the
// reason schemaV2's doc comment spells out — real kraken.db files already
// exist at v13, long past the point where editing v11 would run again.
const schemaV14 = `
-- Not a cleanup: see the doc comment. Accumulate-forward counters cannot be
-- corrected in place, so the mismeasured rows have to go along with the
-- watermarks that would otherwise stop the re-sum from happening.
DELETE FROM job_tokens;
DELETE FROM job_token_files;
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
// the watchlist table and the feedback(ts) index (no hook, same reason);
// migrations[10] adds the job_tokens/job_token_files tables (no hook — two
// brand-new tables, and an absent row correctly means "not measured yet",
// which is exactly what a job that predates the feature is);
// migrations[11] adds the four zeus columns and WIPES both token tables (no
// hook — the wipe IS the migration's data half, and it is expressible in SQL:
// accumulate-forward counters cannot be backfilled, so re-summing from byte
// zero is the only way the split is ever right);
// migrations[12] re-keys board_order and the two token tables onto jobs.id,
// adds events.job_id, and drops drafts (migrateV12JobIDs below does the
// slug -> id mapping the SQL cannot express);
// migrations[13] adds the four job-load columns (no hook — the DEFAULTs land
// every pre-existing job on "never estimated", which is exactly the state
// those jobs are in; see schemaV13);
// migrations[14] clears both token tables a second time (no hook — the wipe IS
// the whole migration, and it is expressible in SQL: the group-collapse rule
// took output_tokens from the first record of a message where a sidecar only
// reports the total on the last, and accumulate-forward counters cannot be
// corrected behind their watermarks).
// Future schema changes append
// migrations[15], migrations[16], ... rather than editing an existing
// schemaV<N> string (or hook) in place, so a database created under an older
// binary still migrates forward correctly.
var migrations = map[int]migrationStep{
	1:  {sql: schemaV1},
	2:  {sql: schemaV2},
	3:  {sql: schemaV3},
	4:  {sql: schemaV4},
	5:  {sql: schemaV5, hook: migrateV5Questions},
	6:  {sql: schemaV6, hook: migrateV6FeedbackJobID},
	7:  {sql: schemaV7},
	8:  {sql: schemaV8},
	9:  {sql: schemaV9},
	10: {sql: schemaV10},
	11: {sql: schemaV11},
	12: {sql: schemaV12, hook: migrateV12JobIDs},
	13: {sql: schemaV13},
	14: {sql: schemaV14},
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

// resolveV12Slug maps one legacy slug string to the job id it most likely
// meant, or 0 when nothing plausible matches. It is the whole of v12's
// judgment, and the precedence is ordered most-certain first:
//
//  1. The ACTIVE job with that slug. jobs_active_slug makes this unique, and
//     an active job is what every slug-addressed write in the running system
//     already resolves to — so this is not a guess, it is the same answer the
//     handler would give today.
//  2. Else the archived job whose ARCHIVED_NAME is that string.
//     jobs_archived_name makes this unique too. It catches the rows written
//     while the job was still active and then re-keyed by ArchiveJob, plus
//     everything the archive namespace addresses by name.
//  3. Else the NEWEST archived job that still carries that slug. This is the
//     only genuine guess in the list, and it is where the duplicate-slug shapes
//     on the live database land: five archived jobs slugged `asdf`, two
//     `helper-catalog`, two `sadfsa`. Newest-wins because a board_order row or
//     an events line naming a slug was, overwhelmingly, written by whichever
//     generation held it most recently.
//  4. Else nothing (0). The job was hard-deleted, or the string never named a
//     job at all.
//
// Deliberately DIFFERENT from migrateV6FeedbackJobID's rule, which refuses to
// choose when a slug matches both an active and an archived job and leaves the
// row NULL. That refusal was right there because a feedback row carries carl's
// own words and mis-filing it moves content between cards. Nothing here has
// that weight: a board_order row is a scroll position and an events row is a
// log line, so a best-guess attachment costs at worst a card that sorts oddly
// once, while refusing to choose costs the position outright.
func resolveV12Slug(tx *sql.Tx, slug string) (int64, error) {
	if slug == "" {
		return 0, nil
	}
	for _, q := range []string{
		`SELECT id FROM jobs WHERE slug = ? AND archived_at IS NULL`,
		`SELECT id FROM jobs WHERE archived_name = ? AND archived_at IS NOT NULL`,
		`SELECT id FROM jobs WHERE slug = ? AND archived_at IS NOT NULL ORDER BY archived_at DESC, id DESC LIMIT 1`,
	} {
		var id int64
		err := tx.QueryRow(q, slug).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("resolve slug %q: %w", slug, err)
		}
	}
	return 0, nil
}

// migrateV12JobIDs is migrations[12]'s Go hook: schemaV12's SQL has already
// renamed board_order out of the way as board_order_slug, created the new
// job_id-keyed board_order, added the nullable events.job_id, recreated the
// two token tables empty, and dropped drafts. What is left is the slug -> id
// mapping, which SQL alone cannot express because the precedence in
// resolveV12Slug is three ordered lookups rather than one join.
//
// Two tables, and they are treated DIFFERENTLY on the unmappable case:
//
//   - board_order: the row is DROPPED. Its whole content is (column, position,
//     identity); with no identity there is nothing left to carry, and a row
//     that names no job would just sit in the table forever, since a column is
//     only ever pruned by being re-saved. Each drop gets an events row, the
//     same log-and-continue ruling migrateV5Questions follows — carl's ordering
//     is his own work, and losing a piece of it silently is not acceptable even
//     though losing it is.
//   - events: the row STAYS, with job_id left NULL. An events row is a fossil
//     that deliberately outlives its job's hard delete, so "no job matches this
//     slug any more" is its NORMAL end state rather than a defect — dropping
//     those rows would delete precisely the history the fossil exists to keep,
//     and would grow with every job carl ever deletes. job_slug still says what
//     the line was about.
//
// The events backfill runs FIRST and per DISTINCT slug rather than per row:
// the table is capped at maxEvents (50,000) rows over a few dozen slugs, so
// one UPDATE per slug is two orders of magnitude fewer statements than one per
// row. Running it before the board_order pass also keeps the drop-log rows
// this hook itself writes out of its own input.
func migrateV12JobIDs(tx *sql.Tx) error {
	// One resolve per distinct slug across both passes: resolveV12Slug costs
	// up to three queries and the same slug recurs constantly (an events table
	// holds thousands of rows over a handful of jobs).
	resolved := map[string]int64{}
	resolve := func(slug string) (int64, error) {
		if id, ok := resolved[slug]; ok {
			return id, nil
		}
		id, err := resolveV12Slug(tx, slug)
		if err != nil {
			return 0, err
		}
		resolved[slug] = id
		return id, nil
	}

	// --- events: backfill job_id, keep every row ---
	eventSlugs, err := v12DistinctEventSlugs(tx)
	if err != nil {
		return err
	}
	for _, slug := range eventSlugs {
		id, err := resolve(slug)
		if err != nil {
			return err
		}
		if id == 0 {
			continue // fossil of a job that no longer exists — job_slug still tells the story
		}
		if _, err := tx.Exec(`UPDATE events SET job_id = ? WHERE job_slug = ?`, id, slug); err != nil {
			return fmt.Errorf("backfill events job_id for slug %q: %w", slug, err)
		}
	}

	// --- board_order: re-key, dropping (and logging) what cannot be mapped ---
	rows, err := tx.Query(`SELECT "column", slug FROM board_order_slug ORDER BY "column", position`)
	if err != nil {
		return fmt.Errorf("read board_order_slug: %w", err)
	}
	type orderRow struct{ column, slug string }
	var legacy []orderRow
	for rows.Next() {
		var r orderRow
		if err := rows.Scan(&r.column, &r.slug); err != nil {
			rows.Close()
			return fmt.Errorf("scan board_order_slug row: %w", err)
		}
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read board_order_slug rows: %w", err)
	}
	rows.Close()

	// Positions are REPACKED per column rather than carried across, because a
	// dropped row would otherwise leave a hole and — more importantly — two
	// different legacy slugs can resolve to the SAME job (a job's slug and its
	// archived_name are both matched, and they differ), which would collide on
	// the (column, position) primary key only by luck. Dedupe by id, first
	// occurrence wins, exactly as SetBoardColumn does.
	next := map[string]int{}
	placed := map[int64]bool{}
	for _, r := range legacy {
		id, err := resolve(r.slug)
		if err != nil {
			return err
		}
		if id == 0 {
			if _, err := tx.Exec(
				`INSERT INTO events (ts, src, level, event, msg, job_slug) VALUES (?, ?, ?, ?, ?, ?)`,
				time.Now().UnixMilli(), "migrate", "warn", "board-order-migration-drop",
				fmt.Sprintf("board order entry for %q in column %q dropped: no active or archived job holds that slug", r.slug, r.column),
				r.slug,
			); err != nil {
				return fmt.Errorf("log dropped board order entry %q: %w", r.slug, err)
			}
			continue
		}
		if placed[id] {
			continue
		}
		placed[id] = true
		if _, err := tx.Exec(
			`INSERT INTO board_order ("column", position, job_id) VALUES (?, ?, ?)`,
			r.column, next[r.column], id,
		); err != nil {
			return fmt.Errorf("insert migrated board order entry %q: %w", r.slug, err)
		}
		next[r.column]++
	}

	if _, err := tx.Exec(`DROP TABLE board_order_slug`); err != nil {
		return fmt.Errorf("drop board_order_slug: %w", err)
	}
	return nil
}

// v12DistinctEventSlugs is migrateV12JobIDs' events input, split out only so
// the rows cursor is closed before the UPDATE statements run against the same
// transaction.
func v12DistinctEventSlugs(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query(`SELECT DISTINCT job_slug FROM events WHERE job_slug != ''`)
	if err != nil {
		return nil, fmt.Errorf("read event slugs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("scan event slug: %w", err)
		}
		out = append(out, slug)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read event slugs: %w", err)
	}
	return out, nil
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
