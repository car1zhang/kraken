package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
// for its unscoped time-window sweep (the flat table version 16 retires;
// feedback.go's FeedbackSince). Version 10 adds the per-job token counters and their
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
// total on the last (see schemaV14). Version 15 adds the knowledge graph's
// seven tables (see schemaV15). Version 16 moves the watchlist INTO that
// graph: every observation becomes a live kg_edges fact carrying a thin
// kg_watch overlay, and the flat watchlist table is migrated forward and
// dropped (see schemaV16 and migrateV16Watchlist). Version 17 adds the Slack
// intake bot's two tables — slack_inbox (one row per seen message, carrying
// the whole seen → delivered → acked lifecycle) and slack_marks (the
// per-conversation `ts` watermarks that make polling incremental) — and
// touches nothing else (see schemaV17). Version 18 adds linear_issues, the
// Linear intake pass's one-row-per-issue dedupe and delivery-watermark table
// (see schemaV18 and store/linear.go). Version 19 adds kg_recall, the
// per-session record of which graph facts have already been injected into a
// conversation, which is what makes mid-conversation recall inject a given
// memory at most once per session (see schemaV19 and store/recall.go).
// Version 20 retires the queued status: jobs.ready is dropped, the prereqs
// table is dropped with the feature it served, and every board_order row for
// the vanished "queued" column is deleted (see schemaV20).
// Version 21 adds job_former_slugs, the record of every name a job has been
// renamed AWAY from, which is what keeps a renamed job's old CLI names,
// branches, cell dirs and transcript paths resolving (see schemaV21 and
// store/rename.go).
// Version 22 adds threads.model, the per-thread model carl picks at branch
// time, which is what lets one side thread ask a cheap question on Haiku while
// the next reads the whole diff on Opus (see schemaV22 and store/threads.go).
// Version 23 adds the goals table plus jobs.goal_id: carl's weekly and daily
// goals, and the one goal a job optionally serves (see schemaV23 and
// store/goals.go).
// Version 24 added orch_inbox, the since-retired orchestrator agent's inbox
// carrying slack_inbox's three-timestamp lifecycle (see schemaV24; v31 drops
// it again).
// Version 25 adds saved_artifacts, the artifact LIBRARY: one frozen snapshot
// per artifact carl saves, keyed (job_id, name) and outliving the job it came
// from (see schemaV25 and store/saved.go).
// Version 26 adds board_bg, the web board's one optional background image —
// a singleton blob row, replaced on upload and deleted on remove (see
// schemaV26 and store/background.go).
// Version 27 replaces jobs.goal_id with the job_goals join table: a job serves
// ANY number of goals rather than exactly one, the existing links are carried
// across in pure SQL, and the column is dropped (see schemaV27 and
// store/jobgoals.go).
// Version 28 adds the three account-binding columns — the pool account a
// cell's claudes bill, stamped on the job row at spawn so every later
// spawn in that cell (reactivation, the limit rescue's respawn, a side
// thread) can re-derive the same credential dir (see schemaV28).
// Version 29 is the mental model's cutover: it adds mm_items and
// mm_code_anchors (carl's own record of what he has decided and read), moves
// the dream loop's watch overlay back out of the graph into a standalone
// watch_items table, WIPES every kg_* table so the knowledge graph can be
// re-grown as a compilation of mm items alone, and rebuilds kg_episodes so its
// closed `kind` CHECK admits 'mental-model' (see schemaV29 and
// migrateV29MentalModel).
// Version 30 adds jobs.agent, the coding agent a cell's zeus runs (see
// schemaV30).
// Version 31 retires the orchestrator agent: it drops orch_inbox and deletes
// the five kv rows that agent wrote (see schemaV31).
// Version 32 drops every knowledge-graph table and the graph's kv rows
// (see schemaV32).
// Version 33 is the mental-model rework: mm_items gains the title/scope/
// repo/dir/quote/source_json/thread_session/revised_at columns and admits a
// seventh source, `capture`; threads gains role; mm_flags is new (see
// schemaV33).
// Version 34 is multiplayer kraken: the nodes registry, the paired-devices
// table, and jobs.visibility (see schemaV34).
// Version 35 adds job_stacks: a cell's manual-test URLs, one row per
// (job, url), registered by the cell through `kraken stack` and deleted with
// the cell (see schemaV35).
const schemaVersion = 35

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
	-- pins a job to "draft" in deriveState whatever its state says) had no
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
// EVERYTHING BELOW IS HISTORY AS OF v16: this whole table, both its indexes
// and the retired lookup they served are dropped by migrateV16Watchlist, which
// promotes each row to a kg_edges fact with a kg_watch overlay (schemaV16,
// watch.go). The paragraphs are kept because this step still RUNS — a v8
// database migrating forward creates this table before v16 empties and drops
// it — and because the reasoning is the argument watch.go's own matching rule
// inherited.
//
// watchlist deliberately had NO job_id, no FK, and no cascade — the one
// table here that wasn't scoped to a job. A watchlist row was an observation
// the nightly pass was TRACKING (a recurring correction, a wasted step, a
// prompting pattern), and its whole point was that it OUTLIVES the job that
// happened to surface it: the same observation recurs across many jobs, and
// a row must survive its originating job's teardown, archive, and even
// hard-delete. Attaching it to a job would have made it cascade away exactly
// when it had finally been seen often enough to matter. (The property
// survived both moves: kg_watch was global and job-free, and so is the
// watch_items table v29 moved it back out into.)
//
// The (state, last_seen) index served the only listing the table had: the
// v9-era ListWatchItems filtered on state and ordered by last_seen DESC, so
// both columns were covered in the order the query used them — the same
// argument watch_items_state_last_seen makes for today's read (schemaV29).
//
// watchlist_observation_key was an EXPRESSION index, not a plain column one,
// and it was deliberately written as the exact expression the v9-era
// FindWatchItem put in its WHERE clause. That coupling was the point — the
// normalisation rule is one expression, so the lookup and the index cannot
// drift into disagreeing about which rows are "the same observation"; a
// drifted pair still returns correct answers and merely stops using the
// index, which is why the retired watchlist tests asserted on EXPLAIN QUERY
// PLAN rather than on results.
//
// char(32,9,10,13) is the trim set — space, tab, newline, carriage return.
// Bare TRIM() strips SPACES ONLY, which was not enough here: the caller is an
// LLM and a trailing newline on a generated string is entirely ordinary, so
// the default would miss the repeat and insert a duplicate — the exact
// failure that lookup existed to prevent. char() and TRIM() are both
// deterministic, which is what let the expression be indexed at all. It is
// not UNIQUE: rows predating the lookup (or written by a caller that never
// looked first) may already collide, and a UNIQUE index would turn that into
// a failed INSERT on the nightly pass rather than the duplicate the lookup was
// there to prevent in the first place. Those collisions are real, and v16
// inherits them — migrateV16Watchlist's fold (v16Overlay) is what merges the
// pair this index deliberately allowed.
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

// schemaV15 adds the knowledge graph: seven brand-new tables, no hook (there
// is nothing anywhere to migrate forward — the graph starts empty and the
// nightly ingest job backfills it from transcripts that were never in this
// database).
//
// Shape notes, in the order they will be asked about:
//
//   - kg_episodes is the provenance root: one row per ingested unit, keyed by
//     a content hash so a re-scan of the same span is a no-op rather than a
//     duplicate. Every edge traces to exactly one episode; nothing else does.
//   - kg_edges carries the fact as a natural-language SENTENCE (`fact`) plus
//     the bi-temporal quartet: valid_from/valid_until are world time (when the
//     fact was/stopped being true; 0 = unknown/open), created_at/expired_at
//     are system time (when kraken learned/unlearned it). Contradiction sets
//     valid_until and expired_at on the old row and inserts the new one —
//     UPDATE of those two columns is the ONLY sanctioned mutation of a stored
//     edge; nothing is ever deleted (retention is Q10's "no pruning, measure").
//   - embedding BLOBs are little-endian float32, written by the daemon's
//     graphPass AFTER the row exists (async — an OpenAI outage degrades
//     retrieval to BM25-only, it never blocks a write). Zero-length means
//     "not embedded yet", which is also every row's state if no API key is
//     ever configured.
//   - kg_edges_fts is an external-content FTS5 table over kg_edges.fact with
//     the three standard sync triggers, so BM25 search cannot drift from the
//     base table. FTS5 exists only under the sqlite_fts5 build tag — the
//     Makefile carries it, and a binary built without it fails HERE, loudly,
//     at open ("no such module: fts5"), which is the web-assets stance: better
//     than silently serving a graph whose search returns nothing.
//   - kg_ingest_cursor is SumTokensDelta's watermark idea with a different
//     payload: one row per source (a transcript file path, or a named row
//     source like "feedback"), cursor = bytes for files / last-seen id for
//     row sources. Carried forward on errors by the caller, exactly as
//     job_token_files' comment prescribes.
//   - kg_layout caches the server-computed ForceAtlas2 positions per node,
//     stamped with the graph revision they were computed for (rev = max edge
//     id at compute time), so both clients draw the same picture and a
//     layout pass only reruns when the graph actually moved.
//   - NO foreign keys and NO cascades anywhere in kg_*, same reasoning as
//     job_tokens: the graph outlives the jobs/feedback/transcripts it was
//     extracted from — an archived or deleted source must not take facts
//     with it (the episode row IS the surviving provenance).
const schemaV15 = `
CREATE TABLE kg_episodes (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	kind       TEXT    NOT NULL CHECK(kind IN ('transcript','feedback','watchlist','artifact','job')),
	ref        TEXT    NOT NULL DEFAULT '',  -- source pointer: file path + byte span, or row id
	content    TEXT    NOT NULL DEFAULT '',  -- the reduced text handed to the extractor
	hash       TEXT    NOT NULL,             -- content hash; the dedupe key for re-scans
	ts         INTEGER NOT NULL DEFAULT 0,   -- unix millis: when the source material was written
	state      TEXT    NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','done')),
	created_at INTEGER NOT NULL DEFAULT 0    -- unix millis
);
CREATE UNIQUE INDEX kg_episodes_hash ON kg_episodes(hash);
CREATE INDEX kg_episodes_state ON kg_episodes(state, id);

CREATE TABLE kg_nodes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT    NOT NULL,            -- canonical name
	type        TEXT    NOT NULL DEFAULT '', -- seed vocabulary + open extension (Q8)
	description TEXT    NOT NULL DEFAULT '', -- short natural-language gloss of the concept
	embedding   BLOB    NOT NULL DEFAULT x'', -- little-endian float32; empty = not embedded yet
	created_at  INTEGER NOT NULL DEFAULT 0,  -- unix millis
	updated_at  INTEGER NOT NULL DEFAULT 0   -- unix millis
);
CREATE UNIQUE INDEX kg_nodes_name ON kg_nodes(LOWER(name));

CREATE TABLE kg_node_aliases (
	node_id INTEGER NOT NULL,
	alias   TEXT    NOT NULL,
	PRIMARY KEY (node_id, alias)
);
CREATE INDEX kg_node_aliases_alias ON kg_node_aliases(LOWER(alias));

CREATE TABLE kg_edges (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	src_id      INTEGER NOT NULL,
	dst_id      INTEGER NOT NULL,
	predicate   TEXT    NOT NULL DEFAULT '',
	fact        TEXT    NOT NULL,            -- the fact as one natural-language sentence
	episode_id  INTEGER NOT NULL DEFAULT 0,  -- provenance; 0 only for hand-planted rows
	strength    INTEGER NOT NULL DEFAULT 0,  -- extractor's 1-10 confidence
	occurrences INTEGER NOT NULL DEFAULT 1,  -- distinct episodes independently stating this
	valid_from  INTEGER NOT NULL DEFAULT 0,  -- unix millis world time; 0 = unknown
	valid_until INTEGER NOT NULL DEFAULT 0,  -- unix millis world time; 0 = still true
	embedding   BLOB    NOT NULL DEFAULT x'', -- little-endian float32; empty = not embedded yet
	created_at  INTEGER NOT NULL DEFAULT 0,  -- unix millis system time
	expired_at  INTEGER NOT NULL DEFAULT 0   -- unix millis system time; 0 = live
);
CREATE INDEX kg_edges_src ON kg_edges(src_id, expired_at);
CREATE INDEX kg_edges_dst ON kg_edges(dst_id, expired_at);
CREATE INDEX kg_edges_episode ON kg_edges(episode_id);

CREATE VIRTUAL TABLE kg_edges_fts USING fts5(fact, content='kg_edges', content_rowid='id');
CREATE TRIGGER kg_edges_ai AFTER INSERT ON kg_edges BEGIN
	INSERT INTO kg_edges_fts(rowid, fact) VALUES (new.id, new.fact);
END;
CREATE TRIGGER kg_edges_ad AFTER DELETE ON kg_edges BEGIN
	INSERT INTO kg_edges_fts(kg_edges_fts, rowid, fact) VALUES ('delete', old.id, old.fact);
END;
CREATE TRIGGER kg_edges_au AFTER UPDATE ON kg_edges BEGIN
	INSERT INTO kg_edges_fts(kg_edges_fts, rowid, fact) VALUES ('delete', old.id, old.fact);
	INSERT INTO kg_edges_fts(rowid, fact) VALUES (new.id, new.fact);
END;

CREATE TABLE kg_ingest_cursor (
	source     TEXT    PRIMARY KEY,          -- transcript file path, or a named row source
	cursor     INTEGER NOT NULL DEFAULT 0,   -- byte offset for files; last-seen row id otherwise
	updated_at INTEGER NOT NULL DEFAULT 0    -- unix millis
);

CREATE TABLE kg_layout (
	node_id    INTEGER PRIMARY KEY,
	x          REAL    NOT NULL DEFAULT 0,
	y          REAL    NOT NULL DEFAULT 0,
	rev        INTEGER NOT NULL DEFAULT 0    -- max kg_edges.id at compute time
);
`

// schemaV16 moves the dream loop's watchlist INTO the knowledge graph. The
// DDL below is only half the migration — migrateV16Watchlist (the hook) is
// what carries the existing rows across, and schemaV16's own statements run
// FIRST so the hook has somewhere to write.
//
// EVERYTHING BELOW IS HISTORY AS OF v29, the same standing schemaV9's own
// doc comment carries: the kg_watch table this step creates is dropped by
// migrateV29MentalModel, which moves the record back out of the graph into
// the standalone watch_items (schemaV29, watch.go). The paragraphs are kept
// because this step still RUNS — a v15 database migrating forward creates
// kg_watch, fills it, and only then reaches v29 — and because the shape
// argument here is the one v29 answers rather than ignores.
//
// THE SHAPE CHANGE, stated once: an observation stops being a row in the flat
// `watchlist` table and becomes a live kg_edges FACT — a natural-language
// sentence between two entities — with kg_watch holding only what the graph
// deliberately has no verbs for. The overlay is keyed by edge_id, so there is
// exactly one watch record per watched fact and no way for the two to drift.
//
// Why the observation belongs in kg_edges rather than in a column here: the
// graph already indexes facts for FTS and vector retrieval and already injects
// them into a cell's graphContext. Leaving the observation in a flat column
// meant the single most useful sentence the dream loop produces — "this
// pattern recurs, here is the fix" — was the one thing a future job could not
// retrieve. Moving it buys that for free, and costs the flat table.
//
// What STAYS in the overlay is the workflow the graph has no opinion about:
// the dream-loop bookkeeping (class, state, sightings, first/last seen,
// evidence, suggested_fix, pr_url, updated_at) and carl's judgement trio
// (verdict, comment, commented_at), whose agent/carl ownership split survives
// the move unchanged — see watch.go's header.
//
// NO FOREIGN KEY on edge_id, same rule as every other kg_* table
// (schemaV15's last bullet): the graph carries no FKs and no cascades. An
// overlay whose edge is invalidated is not deleted, it simply stops being
// listed — every read in watch.go INNER JOINs `expired_at = 0`, which is what
// makes "retire the fact in the graph viewer" and "take it off the DREAM
// board" one action instead of two.
//
// The (state, last_seen) index serves the only listing this table has:
// ListWatchItems filters on state and orders by last_seen DESC, so both
// columns are covered in the order the query uses them. There is deliberately
// NO observation index here to replace v9's watchlist_observation_key —
// the observation now lives on kg_edges, and the lookups that used it
// (FindWatchItem, FindLiveEdgeByFact) match LOWER(kg_edges.fact) instead. That
// is a full scan of live edges today, which is the honest trade: the corpus is
// thousands of rows on one machine, and an expression index on
// kg_edges(LOWER(fact)) would be a change to the GRAPH's schema — made when
// the graph's own reads need it, not smuggled in by the watchlist.
//
// Appended rather than folded into schemaV9 (or v15) for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v15, long past
// the point where editing an earlier step would ever run again.
const schemaV16 = `
CREATE TABLE kg_watch (
	edge_id       INTEGER PRIMARY KEY,               -- the watched fact (kg_edges.id); no FK, per the kg_* no-FK rule
	class         TEXT    NOT NULL DEFAULT '',
	state         TEXT    NOT NULL DEFAULT 'watching',
	sightings     INTEGER NOT NULL DEFAULT 1,
	first_seen    INTEGER NOT NULL DEFAULT 0,        -- unix millis
	last_seen     INTEGER NOT NULL DEFAULT 0,        -- unix millis
	evidence      TEXT    NOT NULL DEFAULT '',
	suggested_fix TEXT    NOT NULL DEFAULT '',
	pr_url        TEXT    NOT NULL DEFAULT '',
	updated_at    INTEGER NOT NULL DEFAULT 0,        -- unix millis
	-- carl-owned trio: only SetWatchVerdict ever writes these, exactly as
	-- questions.answer/answered_at are kraken-owned (questions.go).
	verdict       TEXT    NOT NULL DEFAULT '',
	comment       TEXT    NOT NULL DEFAULT '',
	commented_at  TEXT    NOT NULL DEFAULT ''        -- RFC3339
);
CREATE INDEX kg_watch_state_last_seen ON kg_watch(state, last_seen);
`

// schemaV17 adds the Slack intake bot's two tables. Both are brand new and
// there is no hook: nothing anywhere holds Slack state today, so there is
// nothing to migrate forward — the same situation schemaV7/V15 were in.
// Appended rather than folded into an earlier schemaV<N> for the reason
// schemaV2's doc comment spells out: real kraken.db files already exist at
// v16.
//
// slack_inbox is one row per message the poller has SEEN, and the row carries
// the whole lifecycle in three timestamps rather than a state column:
// seen_at (slackPass wrote it), delivered_at (iris' pane was bumped about it),
// acked_at + disposition (iris said what it did with it). Three stamps rather
// than one enum because each transition has a different writer and each
// "not yet" is a real query the daemon runs every tick — `delivered_at = 0
// AND acked_at = 0` is the delivery pass' worklist, `acked_at = 0` is what the
// inbox route serves — and a stamp answers both "did it happen" and "when",
// which an enum alone cannot.
//
// UNIQUE(channel_id, ts) is the dedupe that makes the whole pipeline
// at-least-once safe rather than at-most-once. Slack's `oldest` parameter is
// EXCLUSIVE, and the mention search's `after:` is only day-grained, so the
// poller deliberately re-fetches overlapping spans and re-offers messages it
// has already stored; AddSlackBatch's INSERT OR IGNORE turns each of those
// into a skip. A (channel_id, ts) pair identifies a Slack message exactly —
// `ts` is unique per channel and is the message's identity in every Slack API
// — so the constraint is the message's true key, not a heuristic.
//
// `ts` and `thread_ts` are TEXT, never REAL, and that is the one shape
// decision here that is easy to get wrong later: a Slack timestamp is a
// "seconds.micros" STRING and round-tripping it through a float loses the
// digits that distinguish two messages in the same second. The whole feature
// keeps them as strings end to end (core.SlackTSLess compares them as integer
// pairs), and this column is where that promise would silently break.
//
// source is CHECKed rather than left free-form because it names which POLLING
// LOOP found the message ('dm' and 'mpim' from the history sweep, 'mention'
// from the search query), a closed set the poller owns entirely — the same
// treatment jobs.state and kg_episodes.kind get.
//
// NO FOREIGN KEY and no cascade anywhere, for the reason job_tokens and the
// kg_* tables state: an inbox row is global state belonging to no job. It
// records that a message arrived, which stays true whatever iris later did
// about it — including drafting a job that is subsequently archived or hard
// deleted. Q6 keeps these rows forever.
//
// NO SECONDARY INDEX, deliberately, and this is the paragraph to revisit if
// the reads ever get slow rather than a defect to fix now. Both listings scan
// the whole table (`acked_at = 0` and the delivered pair), and both run at most
// once per 15-second daemon tick. The row source is carl's own Slack traffic —
// DMs, group DMs and direct mentions, hundreds of rows a year even at the high
// end — so the table stays orders of magnitude below the point where a scan of
// it costs more than maintaining an index on every insert. If it ever does, the
// index to add is (acked_at, id), which covers both queries' filter and their
// shared ORDER BY id.
//
// slack_marks is the watermark table, keyed by a caller-composed string
// (`dm:<channel_id>`, `thread:<channel_id>:<thread_ts>`, `mentions`) rather
// than by a channel column, because the three polling loops watermark
// different things and only the first is per-channel. It is deliberately NOT
// folded into the existing kv table even though the shape matches: a mark is
// advanced in the SAME transaction that commits the rows it covers
// (AddSlackBatch), and kv's SetKV is a standalone statement with no way to
// join one. Marks and rows moving together is the invariant that makes a
// crashed pass re-fetch rather than skip.
const schemaV17 = `
CREATE TABLE slack_inbox (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	channel_id   TEXT NOT NULL,
	channel_name TEXT NOT NULL DEFAULT '',
	ts           TEXT NOT NULL,             -- Slack "seconds.micros"; a STRING, never a float
	thread_ts    TEXT NOT NULL DEFAULT '',  -- parent's ts when this is a thread reply
	author_id    TEXT NOT NULL DEFAULT '',
	author_name  TEXT NOT NULL DEFAULT '',
	source       TEXT NOT NULL CHECK(source IN ('dm','mpim','mention')),
	text         TEXT NOT NULL DEFAULT '',
	seen_at      INTEGER NOT NULL DEFAULT 0, -- unix millis: the poller stored it
	delivered_at INTEGER NOT NULL DEFAULT 0, -- unix millis: iris' pane was bumped; 0 = pending
	acked_at     INTEGER NOT NULL DEFAULT 0, -- unix millis: iris acted on it; 0 = unacked
	disposition  TEXT NOT NULL DEFAULT '',   -- iris' one-line account of what it did
	UNIQUE(channel_id, ts)
);

CREATE TABLE slack_marks (
	key TEXT PRIMARY KEY NOT NULL,          -- dm:<channel>, thread:<channel>:<thread_ts>, mentions
	ts  TEXT NOT NULL DEFAULT ''            -- newest ts already stored for that key
);
`

// schemaV18 adds linear_issues: one row per Linear issue the intake pass has
// ever processed. One brand-new table, no hook — there is nothing anywhere to
// migrate forward, and an ABSENT row correctly means "never seen", which is
// what every issue is on the day the feature is first configured.
//
// Purely additive: no existing table, column or row is touched, so a database
// that never gets a LINEAR_API_KEY carries one empty table and is otherwise
// byte-identical to a v17 one.
//
// Shape notes, in the order they will be asked about:
//
//   - issue_id is Linear's issue UUID and the PRIMARY KEY, deliberately not
//     the human identifier: ENG-142 changes when an issue moves team, and the
//     UUID is what makes "have I seen this before?" survive that.
//   - job_id defaults to 0, which is a real answer rather than a missing one —
//     "processed, no job created" — with skip_reason carrying why. There is no
//     sentinel for "not yet decided" because the row is only written once the
//     decision is made (store/linear.go's PutLinearIssue is insert-only).
//   - last_event_at is the delivery watermark, defaulting to 0 so a row
//     written for a skipped issue needs no value for it.
//   - identifier and created_at are the two columns with NO default: both are
//     known at insert time and a row missing either would be a bug the schema
//     can catch for free.
//   - NO foreign key and NO cascade on job_id, the job_tokens rule exactly:
//     the row must outlive the job, or archiving one would hand the issue back
//     to the next poll to create all over again.
//
// No index beyond the primary key. The two reads are a point lookup on
// issue_id (served by the PK) and a full listing; there is no third query to
// cover, and this table's row count is bounded by tickets carl is assigned.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v17, long past
// the point where editing an earlier step would ever run again.
const schemaV18 = `
CREATE TABLE linear_issues (
	issue_id      TEXT    PRIMARY KEY,          -- Linear's issue UUID
	identifier    TEXT    NOT NULL,             -- the human key, e.g. ENG-142
	job_id        INTEGER NOT NULL DEFAULT 0,   -- 0 = no job created; skip_reason says why
	skip_reason   TEXT    NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,             -- unix millis: when kraken processed it
	last_event_at INTEGER NOT NULL DEFAULT 0    -- unix millis: newest Linear event already delivered
);
`

// schemaV19 adds kg_recall: one row per (session, fact) pair already injected
// into a live conversation by the mid-conversation recall hook. One brand-new
// table, no hook — an ABSENT row means "never delivered to this session", which
// is what every session is on the day the feature ships, so there is nothing
// anywhere to migrate forward (schemaV7/V15/V17/V18's situation exactly).
//
// Purely additive: no existing table, column or row is touched.
//
// WHY IT IS A TABLE AT ALL, since the alternative looks cheaper: the dedup has
// to survive `kraken serve` restarting mid-conversation. A process-local set
// would re-inject a session's whole recalled history on the first prompt after
// any restart — the one failure mode the feature's "at most once per session"
// rule exists to prevent, and the one an in-memory map cannot see coming.
//
// Shape notes, in the order they will be asked about:
//
//   - The PRIMARY KEY (session_id, edge_id) IS the dedup: MarkRecalled inserts
//     OR IGNORE against it, so re-marking a fact for a session is a skip rather
//     than a duplicate row or a moved `ts`. There is no separate id column
//     because nothing ever addresses a recall row individually.
//   - session_id LEADS the key deliberately. The only read is
//     RecalledEdgeIDs' `WHERE session_id = ?`, and a composite primary key can
//     only be seeked on a PREFIX of its columns — leading with edge_id would
//     make the one query this table has a full scan (schemaV9's feedback_ts
//     argument, applied before the fact rather than after).
//   - NO foreign key on edge_id and no cascade, the kg_* rule schemaV15's last
//     bullet states and the same stance job_tokens takes: an invalidated (or,
//     hypothetically, deleted) edge leaves its recall row standing harmlessly,
//     since the row's whole meaning is "this was already said to this session",
//     which stays true whatever later happens to the fact.
//   - ts carries NO DEFAULT, unlike most INTEGER millis columns here. Every
//     writer is MarkRecalled, which takes the stamp as a parameter and always
//     supplies it, so a default could only ever paper over a caller bug — and
//     this column is read by nothing today (it exists so a future pass can age
//     rows out), which is exactly when a silent 0 would go unnoticed.
//
// No secondary index. The table has one query, served by the primary key, and
// its row count is bounded by facts actually injected into conversations.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v18, long past
// the point where editing an earlier step would ever run again.
const schemaV19 = `
CREATE TABLE kg_recall (
	session_id TEXT    NOT NULL,             -- claude's own session uuid; the dedup scope
	edge_id    INTEGER NOT NULL,             -- the injected fact (kg_edges.id); no FK, per the kg_* no-FK rule
	ts         INTEGER NOT NULL,             -- unix millis: when it was injected
	PRIMARY KEY (session_id, edge_id)
);
`

// schemaV20 retires the queued status. Three drops, no hook — every one of
// them is expressible in SQL, and none of them needs a value carried forward:
//
//   - jobs.ready: the flag the board's draft→queued drag used to flip and the
//     daemon's queue pass used to read. Submit now starts the cell in the same
//     request, so there is no interval in which a job is "ready but not
//     started" for a column to hold. Dropped rather than left standing: a
//     column nothing reads is a column the next reader assumes still means
//     something. schemaV3's precedent — go-sqlite3 bundles a SQLite with
//     native ALTER TABLE ... DROP COLUMN, and ready carried no index and no
//     CHECK, so the drop is the whole step.
//   - the prereqs table: prereqs existed only to park a ready job until its
//     dependencies finished, and startable() — the one reader — is gone with
//     the queue pass (Q1). No active job carries a row, so nothing is lost;
//     the DROP is what stops the table reading as a live feature.
//   - board_order rows for the queued column: board_order has no CHECK on
//     "column" and nothing else ever prunes it, so a stale queued ordering
//     would sit in the table forever, invisible and unreachable. Rows for
//     every OTHER column are untouched.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v19.
const schemaV20 = `
ALTER TABLE jobs DROP COLUMN ready;
DROP TABLE prereqs;
DELETE FROM board_order WHERE "column" = 'queued';
`

// schemaV21 adds job_former_slugs: one row per name a job has been renamed
// AWAY from. One brand-new table, no hook — nothing anywhere records a rename
// today, so an EMPTY table is exactly right for every pre-existing job (none
// of them has ever been renamed), which is schemaV7/V15/V17/V18/V19's
// situation.
//
// Purely additive: no existing table, column or row is touched.
//
// WHY IT IS PERSISTED AT ALL, since a rename could just move everything: a
// job's runtime names are DERIVED from its slug — the cell dir, the tmux
// session, the branch family, the ~/.claude/projects transcript dirs Claude
// Code buckets by startup cwd — and most of those cannot be moved while the
// cell is live (renaming a worktree under a running agent breaks its pane;
// the transcript dirs are Claude Code's, not kraken's). So a rename moves
// almost nothing and this table is what lets every resolver keep PROBING the
// old names. Losing a row does not corrupt anything, it silently detaches a
// job from its own history — which is why the row is written in the same
// transaction as the slug change rather than afterwards.
//
// Shape notes, in the order they will be asked about:
//
//   - PRIMARY KEY (job_id, slug) IS the dedupe: renaming A→B→A→B must leave
//     ONE (id,"A") row rather than a second, so RenameJob upserts against it.
//     There is no id column because nothing addresses a former-slug row
//     individually.
//   - job_id LEADS the key, because the per-job read (every job row load
//     carries its former slugs) is the frequent one and a composite key can
//     only be seeked on a PREFIX of its columns — schemaV9's feedback_ts
//     argument, applied before the fact.
//   - job_former_slugs_slug is the index for the OTHER direction, the
//     resolution `GetJobByFormerSlug` does: given an old name, find the active
//     job that used to hold it. Without it that lookup is a full scan.
//   - NO foreign key and no cascade, the rule job_tokens and every kg_* table
//     state: a hard-deleted job's rows are harmless fossils, and a cascade
//     firing here buys nothing (the row's whole meaning is "this name once
//     belonged to that id", which stays true).
//   - renamed_at is unix millis and is READ, not decorative:
//     GetJobByFormerSlug orders by it so that when two active jobs somehow
//     share a former slug — reachable only for rows written before the
//     reservation in CreateJob/RenameJob existed — the most recent rename
//     wins rather than an arbitrary row.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v20, long past
// the point where editing an earlier step would ever run again.
const schemaV21 = `
CREATE TABLE job_former_slugs (
	job_id     INTEGER NOT NULL,
	slug       TEXT    NOT NULL,
	renamed_at INTEGER NOT NULL DEFAULT 0, -- unix millis: when the job left this name
	PRIMARY KEY (job_id, slug)
);
CREATE INDEX job_former_slugs_slug ON job_former_slugs(slug);
`

// schemaV22 adds threads.model: the model alias carl picked for THIS side
// thread at branch time — one of the four keys of core.ThreadModels — empty
// meaning he picked none, which is the state every thread branched before the
// picker existed is in. No hook: the empty-string DEFAULT is the whole
// backfill, schemaV8's situation exactly, and an empty value is not a
// placeholder here but the real answer (jobs.threadSpawnCommand falls back to
// the job's model: field and then core.DefaultModel for it, which is what
// those threads already ran on).
//
// The alias is stored rather than the resolved id, because the column has to
// survive a REVIVE: a revived thread re-renders its spawn command from this
// row, and storing the alias keeps that resolution in one place
// (core.ThreadModels) instead of freezing whatever id the map held on the day
// of the branch.
//
// No CHECK constraint, deliberately: the four aliases are a UI menu that will
// gain and lose entries as claude ships tiers, and a CHECK would make adding
// one a migration and an OLD row unreadable the day one is retired. The
// validation lives in Go (store.validateThreadModel), where the error can name
// the four names.
//
// Appended rather than folded into schemaV7 for the reason schemaV2's doc
// comment spells out: real kraken.db files already exist at v21.
const schemaV22 = `
ALTER TABLE threads ADD COLUMN model TEXT NOT NULL DEFAULT '';
`

// schemaV23 adds the goals table and jobs.goal_id: carl's weekly and daily
// goals (store/goals.go), and the one goal a job optionally serves. One
// brand-new table plus one nullable column, no hook — nothing anywhere
// records a goal today, so an EMPTY table and a NULL link are exactly right
// for every pre-existing row, which is schemaV7/V15/V17/V18/V19/V21's
// situation.
//
// Shape notes, in the order they will be asked about:
//
//   - scope carries a CHECK rather than living in Go alone, unlike
//     threads.model (schemaV22): week and day are the two halves of the
//     feature's whole data model — every reader groups by them, and the
//     panel, the injected brief block and the period validator all enumerate
//     exactly these two — so a third value is corruption rather than a menu
//     entry a later release might add. store.validateGoalScope still refuses
//     it first, so the CHECK is the backstop, never the error carl reads.
//   - period is the LOCAL calendar date '2006-01-02': the day itself for a
//     day goal, the week's MONDAY for a week goal (store.GoalPeriods is the
//     one place that derives both, and store.validateGoalPeriod is what keeps
//     a week row off a Tuesday). Stored as TEXT rather than millis because
//     every read is an equality match against a date string the caller
//     already has, and a millis column would make "which week is this" a
//     range query over a timezone.
//   - parent_id is the day→week nesting, self-referencing this same table,
//     with ON DELETE SET NULL so deleting a week goal DEGRADES its day
//     children to unassigned rather than deleting them — carl's call: a day
//     goal is work he still did, whatever happened to the week goal above it.
//     A week goal never carries one (store.AddGoal/UpdateGoal refuse it):
//     SQL cannot express "only when scope = 'day'" without a trigger, so that
//     half of the rule lives in Go where its refusal can say why.
//   - goals_scope_period indexes the ONE read every surface makes — list this
//     scope's goals for this period, in position order — so the trailing
//     position column is there to serve the ORDER BY, not just the WHERE.
//   - jobs.goal_id follows schemaV6's feedback.job_id precedent exactly: a
//     nullable REFERENCES ... ON DELETE SET NULL added by ALTER, which SQLite
//     allows precisely because its default is NULL. Deleting a goal therefore
//     unlinks every job pointing at it rather than orphaning a dangling id,
//     and NULL (Go 0) is the normal "this job serves no particular goal"
//     state that every job predating this feature is in. It is deliberately
//     NOT in jobFieldColumns (jobs.go) — a full form or raw save must be
//     unable to touch it — the same blast-radius rule the four load_* columns
//     follow, and by the same mechanism: absence from the allowlist. (Colour,
//     the other one-field route, is NOT this precedent — "color" is in
//     jobFieldColumns and is protected only by the form no longer sending it.)
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v22.
const schemaV23 = `
CREATE TABLE goals (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	scope      TEXT    NOT NULL CHECK(scope IN ('week','day')),
	period     TEXT    NOT NULL,             -- local calendar date '2006-01-02'; a week goal's is its Monday
	text       TEXT    NOT NULL,
	done       INTEGER NOT NULL DEFAULT 0,
	position   INTEGER NOT NULL DEFAULT 0,   -- order within (scope, period)
	parent_id  INTEGER NULL REFERENCES goals(id) ON DELETE SET NULL, -- day goal's week goal; NULL = unassigned
	created_at INTEGER NOT NULL DEFAULT 0,   -- unix millis
	updated_at INTEGER NOT NULL DEFAULT 0    -- unix millis
);
CREATE INDEX goals_scope_period ON goals(scope, period, position);
ALTER TABLE jobs ADD COLUMN goal_id INTEGER REFERENCES goals(id) ON DELETE SET NULL;
`

// schemaV24 added the orchestrator agent's one table, orch_inbox. That agent
// has since been retired and schemaV31 drops the table again; its statements
// stay exactly as they shipped (migrations are never edited, only appended —
// see the migrations comment), so a database older than v24 still walks
// forward through it and then loses the table at v31. Only comments — these,
// and the SQL comments inside the string — were reworded when the agent was
// removed.
//
// It was a brand-new table with no hook — an EMPTY inbox was precisely the
// state a database that predated the agent was in — and purely additive, so a
// Mac that never enabled the agent carried one empty table and was otherwise
// identical to a v23 one.
//
// orch_inbox was slack_inbox's shape with Slack's identity columns removed:
// a row was SEEN (the daemon enqueued it at a transition it was already
// notifying about), DELIVERED (the agent's pane was bumped with a count),
// then ACKED with a one-line disposition — the same three timestamps rather
// than a state column, for schemaV17's reason. kind carried no CHECK, since
// it named a daemon transition point whose set was expected to grow; slug was
// not a foreign key, for job_tokens' and slack_inbox's no-FK reason. (An
// imsg_sent sibling table — the removed iMessage bridge's echo guard — briefly
// shared this step and went with the bridge before the step ever shipped.)
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already existed at v23.
const schemaV24 = `
CREATE TABLE orch_inbox (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	kind         TEXT NOT NULL,              -- which daemon transition raised it; no CHECK, see above
	slug         TEXT NOT NULL DEFAULT '',   -- the job it is about; '' = belongs to no job
	summary      TEXT NOT NULL DEFAULT '',   -- one line, capped by the caller
	seen_at      INTEGER NOT NULL DEFAULT 0, -- unix millis: the daemon enqueued it
	delivered_at INTEGER NOT NULL DEFAULT 0, -- unix millis: the agent's pane was bumped; 0 = pending
	acked_at     INTEGER NOT NULL DEFAULT 0, -- unix millis: the agent acted on it; 0 = unacked
	disposition  TEXT NOT NULL DEFAULT ''    -- the agent's one-line account of what it did
);
`

// schemaV25 adds saved_artifacts: the artifact LIBRARY, one FROZEN copy of an
// artifact's bytes per (job, name) that carl asked to keep. One brand-new
// table, no hook — nothing anywhere has ever saved an artifact, so an EMPTY
// table is exactly right for every pre-existing database, which is
// schemaV7/V15/V17/V18/V19/V21/V23/V24's situation.
//
// Shape notes, in the order they will be asked about:
//
//   - job_id carries NO foreign key and NO cascade, the job_tokens rule
//     (schemaV10's comment, second half): a saved artifact must survive the
//     job it came from — teardown, archiving, and the hard delete alike —
//     because the whole point of saving one is that it outlives the run. A
//     cascade would delete carl's library the moment he cleaned up a job.
//   - snapshot is the bytes as they stood AT SAVE TIME, never a pointer at
//     the live file: an artifact is rewritten in place by the agent that
//     owns it, and a library entry that silently followed those rewrites
//     would be a bookmark rather than a saved copy. This is the same freeze
//     feedback.snapshot performs, for the same reason.
//   - slug is the DISPLAY slug at save time — the string the client
//     addressed the job by — stored rather than joined because the row has
//     to render after the job is archived (which renames it) or deleted
//     (which leaves nothing to join against). It is display, never identity:
//     identity is job_id.
//   - UNIQUE(job_id, name) is what makes a re-save a REPLACE rather than a
//     second row. Saving an artifact twice is one library entry whose
//     snapshot and saved_at move forward, so the toggle in each viewer has
//     exactly two states to render.
//
// No index beyond that UNIQUE: the one read is "the whole library, newest
// first", a full scan of a table carl grows by hand a few rows at a time.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v24. (It was
// authored as v24 in a cell branch and renumbered here: the orchestrator's
// orch_inbox merged to main first and took that version.)
const schemaV25 = `
CREATE TABLE saved_artifacts (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id   INTEGER NOT NULL,             -- no FK, no cascade: the row outlives its job
	slug     TEXT    NOT NULL,             -- display slug at save time
	name     TEXT    NOT NULL,             -- artifact path within the job, slashes intact
	snapshot BLOB    NOT NULL,             -- the bytes frozen at save time
	saved_at INTEGER NOT NULL DEFAULT 0,   -- unix millis
	UNIQUE(job_id, name)
);
`

// schemaV26 adds board_bg: the web board's one OPTIONAL background image,
// carl-uploaded from the board's header. One brand-new table, no hook —
// nothing anywhere has ever set a background, so an EMPTY table is exactly
// right for every pre-existing database, which is schemaV25's situation
// (and V7/V15/V17/V18/V19/V21/V23/V24's before it).
//
// Shape notes:
//
//   - id is CHECK (id = 1): the board has exactly one background, so the
//     table is a singleton row — an upload REPLACES it (store/background.go
//     upserts id 1), a remove deletes it, and "no row" is the plain-board
//     default. A kv row was the other candidate and was rejected because kv
//     values are TEXT and this is a multi-megabyte binary blob.
//   - bytes is the image as uploaded, blob-canonical like uploads.bytes and
//     saved_artifacts.snapshot: the row IS the image, so it survives
//     restarts and rides the nightly VACUUM INTO backups with everything
//     else.
//   - updated_at (unix millis) doubles as the wire's cache-buster: the
//     board payload serves it so the SPA can re-fetch exactly when the
//     image actually changes. It is declared BEFORE bytes so the board
//     rebuild's stamp-only read (BoardBackgroundUpdatedAt) never walks the
//     blob's overflow pages to reach a trailing column.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v25.
const schemaV26 = `
CREATE TABLE board_bg (
	id         INTEGER PRIMARY KEY CHECK (id = 1),  -- singleton: the board has one background
	mime       TEXT    NOT NULL,
	updated_at INTEGER NOT NULL DEFAULT 0,          -- unix millis, strictly increasing (see store/background.go)
	bytes      BLOB    NOT NULL
);
`

// schemaV27 replaces jobs.goal_id with job_goals: a job serves ANY number of
// goals now, so the link stops being a column on the job row and becomes rows
// in a join table. No hook — the carry-across is one INSERT ... SELECT, which
// is exactly the shape SQL is for, unlike schemaV12's slug→id mapping.
//
// Shape notes, in the order they will be asked about:
//
//   - The composite PRIMARY KEY (job_id, goal_id) is what makes "link this
//     goal" idempotent in the SCHEMA rather than only in Go: one job cannot
//     hold the same goal twice, whatever a caller asks for, so
//     store.AddJobGoal's silent no-op on an existing link is a decision about
//     the ERROR MESSAGE rather than about the data.
//   - ON DELETE CASCADE on BOTH sides, which is the one place this table parts
//     company with the v23 column it replaces (that was ON DELETE SET NULL).
//     The two spell the same outcome for the same event: deleting a goal
//     un-links every job that served it, and the job reads cleanly with one
//     fewer goal. A NULLable column had to say "no goal" with a value; a row
//     says it by not existing, so the cascade is the direct spelling of it.
//     The job side matters for a reason the column never had: a hard-deleted
//     job's link rows would otherwise outlive it as garbage no reader can even
//     name (jobs.DeleteJob's other children — PRs, uploads, artifacts — cascade
//     for that same reason).
//   - position orders one job's goals, and it is the whole reason the wire can
//     promise that `goal` is `goals[0]` durably: without it the primary would
//     be whatever order SQLite returned that second, and a card's chip would
//     change on nothing. It defaults to 0 so the backfill below can leave it
//     out; store.AddJobGoal appends at MAX(position)+1 thereafter.
//   - job_goals_goal indexes the goal→jobs direction. The job→goals direction
//     is already served by the PRIMARY KEY's own index (job_id leads it), and
//     the reverse is what "which jobs serve this goal" asks — the board filter's
//     question, and the one a delete's cascade walks.
//   - The backfill runs BEFORE the DROP, necessarily: it reads the very column
//     the next statement removes. A NULL goal_id yields no row, which is the
//     join table's spelling of the same "serves no particular goal" the NULL
//     was, and position stays 0 for every carried link — one link cannot be out
//     of order with itself.
//   - The backfill's `AND goal_id IN (SELECT id FROM goals)` is not redundant
//     with v23's foreign key, and the asymmetry is the point: the OLD column was
//     declared REFERENCES but SQLite only enforces that when foreign_keys is on
//     for the connection that wrote the row, whereas the INSERT below is
//     enforced against the NEW table's FK inside the migration's own
//     transaction. So one dangling goal_id — a hand-edited row, a restored
//     backup, a write from a connection with the pragma off — would fail the
//     insert, fail the transaction, and make the database refuse to OPEN,
//     forever and on every run. Skipping it degrades one stale link (which
//     already pointed at nothing) instead. That trade is the whole rule: a
//     migration may lose a link it cannot honour; it may not cost carl the
//     board.
//
// Appended rather than folded into schemaV23 for the reason schemaV2's doc
// comment spells out: real kraken.db files already exist at v26, with real
// links in that column.
const schemaV27 = `
CREATE TABLE job_goals (
	job_id   INTEGER NOT NULL REFERENCES jobs(id)  ON DELETE CASCADE,
	goal_id  INTEGER NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
	position INTEGER NOT NULL DEFAULT 0,           -- order within one job's goals
	PRIMARY KEY (job_id, goal_id)
);
CREATE INDEX job_goals_goal ON job_goals(goal_id);
INSERT INTO job_goals (job_id, goal_id)
	SELECT id, goal_id FROM jobs
	WHERE goal_id IS NOT NULL AND goal_id IN (SELECT id FROM goals);
ALTER TABLE jobs DROP COLUMN goal_id;
`

// schemaV28 adds the three account-binding columns: which pool account this
// job's cell bills, and when it was bound. Three additive ALTERs with
// same-typed DEFAULTs, no hook — the defaults land every pre-existing job on
// the empty binding, which is exactly the state those jobs are in (their
// claudes ran on the machine's main login, which is what an unbound job still
// means).
//
// Shape notes, in the order they will be asked about:
//
//   - account_slug is the pool slot directory NAME under core.PoolDir(), and
//     it is what the spawn's CLAUDE_SECURESTORAGE_CONFIG_DIR is built from —
//     the slot DIR is never stored, since a stored absolute path would go
//     stale the moment $CLAUDE_POOL_DIR moved, and the slug is the only half
//     of it that is data rather than machine layout.
//   - account_email is the identity that slug held AT BIND TIME, stored beside
//     the slug rather than re-read from the pool on every render: the board's
//     chip and the limit rescue's own exclusion both need to say WHICH account
//     a cell is on, and a pool slot's identity can change under ccp (a re-login
//     into a different account) while the cell keeps billing the item it
//     started with. Empty means the binder had no identity for the slot, which
//     the clients render as the slug alone.
//   - account_at (unix millis) is the bind stamp, which is what makes a chip
//     able to read "bound 3h ago" and what makes a stale binding visible as
//     stale. Zero is the never-bound state, and the three columns move
//     together — SetJobAccount overwrites all three at once, since a rebind is
//     an overwrite and there is no separate clear verb.
//   - None of the three is in jobFieldColumns (jobs.go), the same
//     blast-radius rule the four load_* columns follow and by the same
//     mechanism: absence from the allowlist. A full form or raw save must be
//     unable to touch a binding, because the binding is a fact about a running
//     cell's credentials rather than part of the job's authored contract, and
//     a client predating the feature would otherwise clear it on every save.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v27.
const schemaV28 = `
ALTER TABLE jobs ADD COLUMN account_slug  TEXT    NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN account_email TEXT    NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN account_at    INTEGER NOT NULL DEFAULT 0; -- unix millis
`

// schemaV29 is the mental model's cutover. Its SQL half CREATES the three new
// tables and nothing else; everything destructive — the kg_watch carry-across,
// the graph wipe, and the kg_episodes CHECK rebuild — lives in
// migrateV29MentalModel, because migrate() runs the SQL FIRST and the hook has
// to read kg_watch ⋈ kg_edges before any wipe statement removes what it reads.
//
// THE SHAPE CHANGE, stated once: kraken's long-term memory stops being
// everything agents ever wrote and becomes exactly what carl has told it he
// knows. mm_items is that record; the knowledge graph downstream of it is
// emptied and re-grown as a COMPILATION of those items alone (the daemon's
// transcript/feedback/artifact/job-spec sweeps retire in the same change).
//
// Shape notes, in the order they will be asked about:
//
//   - mm_items carries NO foreign key and NO cascade onto jobs, the
//     saved_artifacts posture (schemaV25's first bullet) for a sharper reason:
//     a decision minted from a question is the ESCAPE HATCH from that
//     question's ON DELETE CASCADE. It copies the four values it needs and
//     references nothing, so hard-deleting the job leaves the item
//     byte-identical. An item that vanished with its job would record only
//     what carl still had the job for.
//   - invalidated_at is NULL when live, the archived_at precedent rather than
//     a zero sentinel, for exactly archived_at's reason: "never invalidated"
//     is a genuinely absent value, and NULL is the one spelling of it that no
//     arithmetic can mistake for a timestamp at the epoch. It parts company
//     with archived_at on the READ side, where mm.go COALESCEs it to 0 into a
//     plain int64 rather than scanning a sql.Null*: archived_at's absence is
//     asked about directly by half a dozen queries, while this column's is
//     asked about in SQL (`invalidated_at IS NULL`) and only RENDERED in Go,
//     where 0 and "absent" are the same answer to every caller.
//   - content_sha is the identity hash of what was FROZEN: sha256 of content
//     normally, and sha256 of the snapshot BYTES once SetMMSnapshot stores
//     some (an artifact item's content is only its name, so hashing that would
//     make every re-mark of a rewritten artifact look identical). One column
//     rather than two because exactly one of the two payloads exists per row,
//     and FindLiveMMItem asks one question of it.
//   - snapshot is nullable BLOB rather than NOT NULL with an empty-blob default: only whole-
//     artifact marks freeze bytes, and GetMMSnapshot has to tell "no snapshot"
//     apart from "a zero-byte artifact" to answer ErrNotFound honestly.
//   - mm_code_anchors is keyed by item_id as its PRIMARY KEY, which is what
//     makes the 1:1 with a source=code item structural rather than a
//     convention. No FK, same rule and same reason as the parent table.
//   - watch_items is the dream loop's overlay, moved back OUT of the graph
//     (Q9). v16 made an observation a live kg_edges fact so the graph's own
//     retrieval could reach it; the cutover empties the graph of everything
//     agent-observed, so a watch row riding an edge would be the one agent
//     observation left inside carl's mental model. It goes back to a standalone
//     table — and, unlike the v9 table it resembles, rows are EXPIRED rather
//     than deleted (expired_at, the kg_edges spelling), so an observation carl
//     took off the board is still readable by id afterwards.
//   - The carl-owned trio (verdict, comment, commented_at) survives the move
//     verbatim, ownership rule included: only SetWatchVerdict writes it, and
//     every agent-side write preserves it. See watch.go's header.
//
// Appended rather than folded into an earlier step for the reason schemaV2's
// doc comment spells out: real kraken.db files already exist at v28.
const schemaV29 = `
CREATE TABLE mm_items (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	kind           TEXT    NOT NULL CHECK(kind IN ('decision','reading','note')),
	source         TEXT    NOT NULL CHECK(source IN ('question','artifact','artifact-element','code','agent-output','manual')),
	job_id         INTEGER NOT NULL DEFAULT 0,   -- no FK, no cascade: the item outlives its job
	slug           TEXT    NOT NULL DEFAULT '',  -- display slug at capture time; identity is job_id
	source_ref     TEXT    NOT NULL DEFAULT '',  -- qid, artifact name, repo/path:range, content hash
	content        TEXT    NOT NULL,             -- the frozen thing carl knows
	context        TEXT    NOT NULL DEFAULT '',
	content_sha    TEXT    NOT NULL DEFAULT '',  -- hex sha256 of the frozen payload (see doc comment)
	snapshot       BLOB    NULL,                 -- frozen artifact bytes; NULL = this item has none
	snapshot_ext   TEXT    NOT NULL DEFAULT '',  -- the snapshot's extension, for its MIME on the way out
	episode_id     INTEGER NOT NULL DEFAULT 0,   -- the kg episode this item minted; 0 = not yet
	created_at     INTEGER NOT NULL DEFAULT 0,   -- unix millis
	invalidated_at INTEGER NULL,                 -- unix millis; NULL means live
	invalid_reason TEXT    NOT NULL DEFAULT '',
	invalid_detail TEXT    NOT NULL DEFAULT ''
);
-- The lookup FindLiveMMItem and SupersedeLiveMMItems both run, in the order
-- they use the columns. Listing is a full scan of a table carl grows by hand,
-- the saved_artifacts trade.
CREATE INDEX mm_items_ref ON mm_items(source, job_id, source_ref);

CREATE TABLE mm_code_anchors (
	item_id     INTEGER PRIMARY KEY,          -- 1:1 with a source=code mm_items row; no FK, no cascade
	repo        TEXT    NOT NULL DEFAULT '',  -- checkout dir basename under ~/Developer
	path        TEXT    NOT NULL DEFAULT '',  -- repo-relative, current coordinates
	orig_path   TEXT    NOT NULL DEFAULT '',  -- the path it was marked at, once a rename moved it
	start_line  INTEGER NOT NULL DEFAULT 0,   -- 1-based inclusive, in anchor_sha coordinates
	end_line    INTEGER NOT NULL DEFAULT 0,
	anchor_sha  TEXT    NOT NULL DEFAULT '',  -- '' if and only if anchor_kind is 'dirty'
	anchor_kind TEXT    NOT NULL DEFAULT 'dirty' CHECK(anchor_kind IN ('origin-main','merge-base','head','dirty')),
	text        TEXT    NOT NULL DEFAULT '',  -- the exact bytes read, \n-normalized
	text_sha    TEXT    NOT NULL DEFAULT '',  -- hex sha256 of text
	lang        TEXT    NOT NULL DEFAULT '',
	checked_at  INTEGER NOT NULL DEFAULT 0,   -- unix millis: last verify attempt, survived or not
	checked_sha TEXT    NOT NULL DEFAULT '',  -- the head the last SURVIVED verify re-anchored to
	check_error TEXT    NOT NULL DEFAULT '',  -- a failed READ; disjoint from invalidation
	ambiguous   INTEGER NOT NULL DEFAULT 0,   -- the text was found in more than one place
	invalid_sha TEXT    NOT NULL DEFAULT '',  -- head at the moment it died
	invalid_pr  TEXT    NOT NULL DEFAULT ''   -- best-effort PR url; '' when none was found
);
-- The verify pass walks live anchors repo by repo; this is that read's order.
CREATE INDEX mm_code_anchors_repo ON mm_code_anchors(repo, item_id);

CREATE TABLE watch_items (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	observation   TEXT    NOT NULL DEFAULT '',
	src           TEXT    NOT NULL DEFAULT '',  -- endpoint labels the v16 overlay joined from kg_nodes
	dst           TEXT    NOT NULL DEFAULT '',
	predicate     TEXT    NOT NULL DEFAULT '',
	class         TEXT    NOT NULL DEFAULT '',
	state         TEXT    NOT NULL DEFAULT 'watching',
	sightings     INTEGER NOT NULL DEFAULT 1,
	first_seen    INTEGER NOT NULL DEFAULT 0,   -- unix millis
	last_seen     INTEGER NOT NULL DEFAULT 0,   -- unix millis
	evidence      TEXT    NOT NULL DEFAULT '',  -- JSON, local-only — never committed
	suggested_fix TEXT    NOT NULL DEFAULT '',
	pr_url        TEXT    NOT NULL DEFAULT '',
	updated_at    INTEGER NOT NULL DEFAULT 0,   -- unix millis: the AGENT's last write
	-- carl-owned trio: only SetWatchVerdict ever writes these, exactly as
	-- questions.answer/answered_at are kraken-owned (questions.go).
	verdict       TEXT    NOT NULL DEFAULT '',
	comment       TEXT    NOT NULL DEFAULT '',
	commented_at  TEXT    NOT NULL DEFAULT '',  -- RFC3339
	created_at    INTEGER NOT NULL DEFAULT 0,   -- unix millis
	expired_at    INTEGER NOT NULL DEFAULT 0    -- unix millis; 0 = live. Rows are expired, never deleted
);
-- The one listing this table has: live rows, filtered by state, newest
-- sighting first — covered in the order the query uses the columns.
CREATE INDEX watch_items_state_last_seen ON watch_items(expired_at, state, last_seen);
-- AddWatchItem's fold probe: the live row carrying this exact observation.
CREATE INDEX watch_items_observation ON watch_items(observation);
`

// schemaV30 adds the job's coding-agent column: which agent a cell's zeus
// runs, "" (the claude default) or "codex". It is part of the job's authored
// contract like model:/color:, so unlike the v28 account columns it DOES join
// jobFieldColumns (a form/raw save may set it). Appended rather than folded
// in, same reason as v28: real kraken.db files already exist at v29.
const schemaV30 = `
ALTER TABLE jobs ADD COLUMN agent TEXT NOT NULL DEFAULT '';
`

// schemaV31 retires the orchestrator agent. Two halves, both plain SQL:
//
//   - orch_inbox (schemaV24) is DROPPED, rows and all — nothing reads it any
//     more, and an inbox whose only consumer is gone has no forward home.
//     IF EXISTS because the step only drops: it must not care what shape the
//     table has, or whether a hand-repaired database still carries it.
//   - the five kv rows the agent wrote — its spawn record, its context
//     report and warning stamp, its limit-hit record and its Fable-fallback
//     record — are DELETED by exact key, so the store holds nothing the
//     binary can no longer read. Every other kv row, including iris'
//     limit_hit:iris, is untouched.
//
// Appended rather than folded in, same reason as v28: real kraken.db files
// already exist at v30.
const schemaV31 = `
DROP TABLE IF EXISTS orch_inbox;
-- the retired orchestrator (kronos) agent's rows
DELETE FROM kv WHERE key IN (
	'kronos_agent',
	'kronos_ctx',
	'kronos_ctx_warned',
	'limit_hit:kronos',
	'fable_fallback:kronos'
);
`

// schemaV32 removes the knowledge graph: every kg_* table the earlier steps
// created (v15's six plus the FTS5 index and its three sync triggers, v19's
// kg_recall), and the two kv families only the graph wrote — the job-spec
// vector cache (`kg.jobvec.<id>` / `kg.jobvechash.<id>`) and the nightly
// ingest watermark (`graph_ingest_last`). kg_watch is not named: v29 already
// dropped it after carrying its live rows into watch_items.
//
// Every statement is IF EXISTS so the step is safe on any database that
// reaches it, whatever subset of these objects it holds. Triggers go first and
// the FTS table before kg_edges, so nothing is left referencing a dropped
// table mid-step. Dropping the FTS5 virtual table still needs the fts5 module
// compiled in, which is one reason the sqlite_fts5 build tag stays (v15's DDL,
// replayed by every fresh database, is the other). The historical steps that
// created these objects are left exactly as they were.
//
// mm_items.episode_id is deliberately kept: nothing writes it any more, but
// shipped iOS builds decode the `episode_id` key /api/mm serves from it.
const schemaV32 = `
DROP TRIGGER IF EXISTS kg_edges_ai;
DROP TRIGGER IF EXISTS kg_edges_ad;
DROP TRIGGER IF EXISTS kg_edges_au;
DROP TABLE IF EXISTS kg_edges_fts;
DROP TABLE IF EXISTS kg_edges;
DROP TABLE IF EXISTS kg_node_aliases;
DROP TABLE IF EXISTS kg_nodes;
DROP TABLE IF EXISTS kg_episodes;
DROP TABLE IF EXISTS kg_ingest_cursor;
DROP TABLE IF EXISTS kg_layout;
DROP TABLE IF EXISTS kg_recall;
DELETE FROM kv WHERE key LIKE 'kg.jobvec%' OR key = 'graph_ingest_last';
`

// schemaV33 is the mental-model rework's schema half. Three changes: mm_flags
// is this string (IF NOT EXISTS), while the mm_items rebuild
// (schemaV33MMItems) and threads.role (schemaV33ThreadsRole) are run by the
// hook, migrateV33MindLayer, each behind a column probe that keeps the step
// replayable — see the hook for why.
//
//   - mm_items is REBUILT, because its v29 CHECK on `source` must admit a
//     seventh value (`capture`, an item finalized from text carl highlighted
//     and routed to the job's mind thread) and SQLite cannot alter a CHECK in
//     place. The rebuild is the documented twelve-step shape cut to what this
//     table needs: create mm_items_new with every v29 column declared exactly
//     as before plus the eight new ones, copy every row across by naming each
//     old column (so every existing value — the NULL that spells "live", the
//     snapshot BLOB — lands unchanged, and the new columns take their
//     DEFAULTs), drop, rename, recreate the one index. Nothing references
//     mm_items — no FK, no trigger, no view — so the drop strands nothing, and
//     mm_code_anchors (keyed on item_id, ids preserved) is untouched.
//   - the AUTOINCREMENT high-water mark is carried across before the drop.
//     Copying rows only seeds mm_items_new's sqlite_sequence entry with the
//     largest SURVIVING id, so without the carry an id freed by a deleted top
//     row could be handed out a second time — and an mm id is a name agents
//     and carl cite (`kraken mm show <id>`) long after the row was written.
//     The rename moves the sqlite_sequence entry along with the table.
//   - threads gains role (ask = carl's question thread, every row before this
//     step; mind = the job's mind thread) and turn_uuid (a mind thread's
//     capture key, see schemaV33ThreadsTurn), and mm_flags is new: one row per
//     contradiction an agent noticed between two items, or between an item and
//     what it was looking at, carried through pending → raised|dismissed and,
//     once raised, resolved drop|keep by carl (state `resolved`). No FK and
//     no cascade on any of it, the mm durability posture (mm.go's header): a
//     flag outlives its job.
//
// Appended rather than folded in, same reason as v28: real kraken.db files
// already exist at v32.
const schemaV33 = `
CREATE TABLE IF NOT EXISTS mm_flags (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	item_id     INTEGER NOT NULL DEFAULT 0,   -- the item flagged; no FK, no cascade
	against_id  INTEGER NOT NULL DEFAULT 0,   -- the item it contradicts; 0 = the transcript, not an item
	job_id      INTEGER NOT NULL DEFAULT 0,
	slug        TEXT    NOT NULL DEFAULT '',
	session     TEXT    NOT NULL DEFAULT '',  -- the claude session that noticed it
	note        TEXT    NOT NULL,
	state       TEXT    NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','raised','dismissed','resolved')),
	raised_by   TEXT    NOT NULL DEFAULT '',
	raise_note  TEXT    NOT NULL DEFAULT '',
	raised_at   INTEGER NOT NULL DEFAULT 0,   -- unix millis of the raise OR the dismissal
	resolved_at INTEGER NOT NULL DEFAULT 0,   -- unix millis; 0 = carl has not ruled
	resolution  TEXT    NOT NULL DEFAULT '' CHECK(resolution IN ('','drop','keep')),
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS mm_flags_state ON mm_flags(state);
`

// schemaV33MMItems is v33's mm_items rebuild, run by migrateV33MindLayer only
// when mm_items does not already carry the v33 columns (see schemaV33).
const schemaV33MMItems = `
CREATE TABLE mm_items_new (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	kind           TEXT    NOT NULL CHECK(kind IN ('decision','reading','note')),
	source         TEXT    NOT NULL CHECK(source IN ('question','artifact','artifact-element','code','agent-output','manual','capture')),
	job_id         INTEGER NOT NULL DEFAULT 0,   -- no FK, no cascade: the item outlives its job
	slug           TEXT    NOT NULL DEFAULT '',  -- display slug at capture time; identity is job_id
	source_ref     TEXT    NOT NULL DEFAULT '',  -- qid, artifact name, repo/path:range, content hash
	content        TEXT    NOT NULL,             -- the item's body: the frozen thing carl knows
	context        TEXT    NOT NULL DEFAULT '',
	content_sha    TEXT    NOT NULL DEFAULT '',  -- hex sha256 of the frozen payload (see schemaV29)
	snapshot       BLOB    NULL,                 -- frozen artifact bytes; NULL = this item has none
	snapshot_ext   TEXT    NOT NULL DEFAULT '',  -- the snapshot's extension, for its MIME on the way out
	episode_id     INTEGER NOT NULL DEFAULT 0,   -- vestigial: written by nothing, still served
	created_at     INTEGER NOT NULL DEFAULT 0,   -- unix millis
	invalidated_at INTEGER NULL,                 -- unix millis; NULL means live
	invalid_reason TEXT    NOT NULL DEFAULT '',
	invalid_detail TEXT    NOT NULL DEFAULT '',
	title          TEXT    NOT NULL DEFAULT '',  -- the one line the rules files render
	scope          TEXT    NOT NULL DEFAULT 'repo' CHECK(scope IN ('universal','repo')),
	repo           TEXT    NOT NULL DEFAULT '',  -- checkout basename a repo-scoped item belongs to
	dir            TEXT    NOT NULL DEFAULT '',  -- repo-relative directory; '' = the whole repo
	quote          TEXT    NOT NULL DEFAULT '',  -- the verbatim text carl highlighted
	source_json    TEXT    NOT NULL DEFAULT '',  -- where the capture came from, as JSON
	thread_session TEXT    NOT NULL DEFAULT '',  -- the mind thread session that committed it
	revised_at     INTEGER NOT NULL DEFAULT 0    -- unix millis of the last revise; 0 = never
);
INSERT INTO mm_items_new (id, kind, source, job_id, slug, source_ref, content, context,
	content_sha, snapshot, snapshot_ext, episode_id, created_at, invalidated_at,
	invalid_reason, invalid_detail)
	SELECT id, kind, source, job_id, slug, source_ref, content, context,
	content_sha, snapshot, snapshot_ext, episode_id, created_at, invalidated_at,
	invalid_reason, invalid_detail
	FROM mm_items;
INSERT INTO sqlite_sequence (name, seq)
	SELECT 'mm_items_new', seq FROM sqlite_sequence
	WHERE name = 'mm_items'
	AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'mm_items_new');
UPDATE sqlite_sequence
	SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'mm_items')
	WHERE name = 'mm_items_new'
	AND seq < COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'mm_items'), 0);
DROP TABLE mm_items;
ALTER TABLE mm_items_new RENAME TO mm_items;
CREATE INDEX mm_items_ref ON mm_items(source, job_id, source_ref);
`

// schemaV33ThreadsRole is v33's threads.role, run by migrateV33MindLayer only
// when the column is not already there (see schemaV33).
const schemaV33ThreadsRole = `
ALTER TABLE threads ADD COLUMN role TEXT NOT NULL DEFAULT 'ask' CHECK(role IN ('ask','mind'));
`

// schemaV33ThreadsTurn is v33's threads.turn_uuid, its own probe-guarded
// step beside role: the uuid of zeus' last PROMPT record when a mind thread
// was branched — the key CaptureToMind matches a capture to the turn's mind
// thread on. It is deliberately NOT fork_uuid, which stays zeus' last record
// of any kind for every role: fork_uuid is where the thread overlay splits
// inherited history from the thread's own turns, and keying captures on it
// would either split a working turn's forty tool calls into forty threads or
// (anchored at the prompt instead) render all of zeus' turn as the thread's.
const schemaV33ThreadsTurn = `
ALTER TABLE threads ADD COLUMN turn_uuid TEXT NOT NULL DEFAULT '';
`

// migrateV33MindLayer is migrations[33]'s hook: it runs the mm_items rebuild
// and the threads.role ALTER, each only when the table does not already carry
// its v33 column.
//
// The probe is what makes the step safe to REPLAY, which the earlier steps
// get for free from IF EXISTS / IF NOT EXISTS and these two cannot: SQLite has
// no ADD COLUMN IF NOT EXISTS, and a rebuild run over a table that already
// has the v33 columns would copy only the v29 ones across and silently zero
// every title, scope and quote. A database whose user_version was set back
// without its shape being reversed — which is how several migration fixtures
// in this package's tests build an "older" database — then replays this step
// as a no-op instead of failing the Open.
func migrateV33MindLayer(tx *sql.Tx) error {
	for _, step := range []struct{ table, column, ddl string }{
		{"mm_items", "title", schemaV33MMItems},
		{"threads", "role", schemaV33ThreadsRole},
		{"threads", "turn_uuid", schemaV33ThreadsTurn},
	} {
		var n int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, step.table, step.column,
		).Scan(&n); err != nil {
			return fmt.Errorf("probe %s.%s: %w", step.table, step.column, err)
		}
		if n > 0 {
			continue
		}
		if _, err := tx.Exec(step.ddl); err != nil {
			return fmt.Errorf("add %s.%s: %w", step.table, step.column, err)
		}
	}
	return nil
}

// schemaV35 is the stack-links table (jobstack.go): one row per (job, url)
// a cell registered as a manual-test URL through `kraken stack add`. label is
// the cell's short name for it; status is the daemon probe's last verdict
// (`unknown` until a loopback dial has run, `up`/`down` after); at is the
// registration time and probed_at the last dial, both unix millis.
//
// No FK and no cascade, the house style of prs and job_tokens — but unlike
// those two the rows are NOT retrospective: a stack dies with its tmux
// session, so teardownCell deletes them (DeleteStacksForJob) and a
// reactivated job starts with none. The composite primary key is what makes
// re-registering a url an UPSERT of its label rather than a second chip.
const schemaV35 = `
CREATE TABLE IF NOT EXISTS job_stacks (
	job_id    INTEGER NOT NULL,
	url       TEXT    NOT NULL,
	label     TEXT    NOT NULL DEFAULT '',
	status    TEXT    NOT NULL DEFAULT 'unknown',
	at        INTEGER NOT NULL DEFAULT 0,
	probed_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job_id, url)
);
`

// schemaV34 is multiplayer kraken's storage, three independent pieces:
//
//   - nodes: one row per sibling `kraken serve` this node has ever seen on
//     the tailnet, keyed on the device's tailscale stable id. The daemon's
//     nodes pass is its writer; rows are never deleted by it, a device that
//     went away reads online = 0. jobs_json is the last polled card list,
//     stored verbatim so the board serves it without a network call.
//   - pair_devices: one row per paired phone or browser. token_hash is the
//     SHA-256 of the bearer token, never the token; UNIQUE so a collision is
//     an insert error rather than two rows one token could match.
//   - jobs.visibility: private | readonly | public, defaulting to readonly so
//     every pre-existing job reads exactly what it would have been had the
//     field existed, and nothing becomes drivable from another machine by
//     upgrading.
//
// No FK anywhere: neither table refers to a job, and a node row outlives
// anything the job tables do.
const schemaV34 = `
CREATE TABLE IF NOT EXISTS nodes (
	id         TEXT    PRIMARY KEY,
	label      TEXT    NOT NULL DEFAULT '',
	operator   TEXT    NOT NULL DEFAULT '',
	dns_name   TEXT    NOT NULL DEFAULT '',
	ip         TEXT    NOT NULL DEFAULT '',
	port       INTEGER NOT NULL DEFAULT 0,
	build      TEXT    NOT NULL DEFAULT '',
	contract   INTEGER NOT NULL DEFAULT 0,
	first_seen INTEGER NOT NULL DEFAULT 0,
	last_seen  INTEGER NOT NULL DEFAULT 0,
	last_ok    INTEGER NOT NULL DEFAULT 0,
	last_err   TEXT    NOT NULL DEFAULT '',
	online     INTEGER NOT NULL DEFAULT 0,
	jobs_json  TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS pair_devices (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	label        TEXT    NOT NULL DEFAULT '',
	tailnet_name TEXT    NOT NULL DEFAULT '',
	token_hash   TEXT    NOT NULL UNIQUE,
	created_at   INTEGER NOT NULL DEFAULT 0,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	last_ip      TEXT    NOT NULL DEFAULT ''
);
`

// schemaV34Visibility is v34's one ALTER, run by migrateV34Visibility only
// when jobs lacks the column. ADD COLUMN has no IF NOT EXISTS form, and
// several migration tests age a FRESH database by resetting user_version
// alone (no downgradeFreshDB), so v34 re-runs over a jobs table that already
// carries the column — v33's ALTERs are guarded for the same reason.
const schemaV34Visibility = `ALTER TABLE jobs ADD COLUMN visibility TEXT NOT NULL DEFAULT 'readonly';`

// migrateV34Visibility is migrations[34]'s hook: add jobs.visibility unless
// it is already there.
func migrateV34Visibility(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name = 'visibility'`).Scan(&n); err != nil {
		return fmt.Errorf("probe jobs.visibility: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := tx.Exec(schemaV34Visibility); err != nil {
		return fmt.Errorf("add jobs.visibility: %w", err)
	}
	return nil
}

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
// migrations[15] adds the knowledge graph's seven tables (no hook — brand-new
// tables, nothing to migrate forward; see schemaV15);
// migrations[16] adds kg_watch and moves every watchlist row into the graph as
// a live fact + overlay, then DROPS the flat table (migrateV16Watchlist below
// does the promotion, which SQL alone cannot express — it has to find-or-create
// an episode by content hash and resolve endpoint nodes case-insensitively);
// migrations[17] adds the Slack intake bot's slack_inbox and slack_marks
// tables (no hook — two brand-new tables with no prior home for their data,
// and an empty inbox is exactly the state a database that predates the
// feature is in; see schemaV17);
// migrations[18] adds linear_issues (no hook — a brand-new table has nothing to
// migrate forward, and an absent row correctly means "never seen"; see
// schemaV18);
// migrations[19] adds kg_recall (no hook — a brand-new table has nothing to
// migrate forward, and an absent row correctly means "never delivered to this
// session"; see schemaV19);
// migrations[20] retires the queued status — drops jobs.ready, drops the
// prereqs table, and deletes every board_order row for the queued column (no
// hook — all three are plain SQL, and none of the dropped state has a forward
// home; see schemaV20);
// migrations[21] adds job_former_slugs (no hook — a brand-new table has
// nothing to migrate forward, and an EMPTY table correctly means "no job has
// ever been renamed", which is true of every job that predates the feature;
// see schemaV21);
// migrations[22] adds threads.model (no hook — the column's empty-string
// DEFAULT is the whole backfill, and empty is the right answer for every
// thread branched before carl could pick a model: those threads ran on the
// job's model: field or core.DefaultModel, which is exactly what an empty
// value still resolves to; see schemaV22);
// migrations[23] adds the goals table and jobs.goal_id (no hook — a brand-new
// table has nothing to migrate forward, and a NULL goal_id correctly means
// "this job serves no particular goal", which is true of every job that
// predates the feature; see schemaV23);
// migrations[24] added the since-retired orchestrator's orch_inbox (no hook —
// a brand-new table with no prior home for its data; see schemaV24, and
// migrations[31], which drops it);
// migrations[25] adds saved_artifacts (no hook — a brand-new table has nothing
// to migrate forward, and an EMPTY library correctly means "carl has never
// saved an artifact", which is true of every database that predates the
// feature; see schemaV25).
// migrations[26] adds board_bg (no hook — a brand-new singleton table, and an
// empty one correctly means "no background set", which is true of every
// database that predates the feature; see schemaV26).
// migrations[27] creates job_goals, carries every RESOLVABLE jobs.goal_id link
// into it and drops the column (no hook — the carry-across is one INSERT ...
// SELECT over two tables in this same database, which is precisely what SQL
// expresses; a NULL link yields no row, which is the join table's own spelling
// of "serves no particular goal", and a link naming a goal that no longer exists
// is skipped rather than allowed to fail the whole open; see schemaV27).
// migrations[28] adds the three account-binding columns (no hook — the
// DEFAULTs land every pre-existing job on the empty binding, which is exactly
// the state those jobs are in: they ran on the machine's main login, which is
// what an unbound job still means; see schemaV28).
// migrations[29] creates mm_items, mm_code_anchors and watch_items in SQL,
// then runs migrateV29MentalModel: it carries every LIVE kg_watch overlay
// across into watch_items (reading kg_watch ⋈ kg_edges BEFORE anything is
// wiped, which is why the destructive half is a hook rather than more SQL),
// drops kg_watch, empties every kg_* table, and rebuilds kg_episodes with
// 'mental-model' added to its closed kind CHECK (see schemaV29).
// migrations[30] adds jobs.agent (no hook — the empty-string DEFAULT is the
// claude agent every pre-existing job ran; see schemaV30).
// migrations[31] retires the orchestrator agent: drops orch_inbox and deletes
// the five kv rows it wrote (no hook — both halves are plain SQL; see
// schemaV31).
// migrations[32] drops every kg_* table and the graph's kv rows (no hook —
// plain DROP/DELETE statements, all IF EXISTS; see schemaV32).
// migrations[33] rebuilds mm_items with the rework's columns and the
// `capture` source, adds threads.role, and creates mm_flags; the hook,
// migrateV33MindLayer, runs the rebuild and the ALTER only when each table
// lacks its v33 column, since neither is expressible as IF NOT EXISTS (see
// schemaV33).
// migrations[34] creates nodes and pair_devices (CREATE TABLE IF NOT EXISTS)
// and adds jobs.visibility through its hook, migrateV34Visibility, which runs
// the ALTER only when the column is missing (see schemaV34).
// migrations[35] creates job_stacks (CREATE TABLE IF NOT EXISTS, no hook;
// see schemaV35).
// Future schema changes append
// migrations[36], migrations[37], ... rather than editing an existing
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
	15: {sql: schemaV15},
	16: {sql: schemaV16, hook: migrateV16Watchlist},
	17: {sql: schemaV17},
	18: {sql: schemaV18},
	19: {sql: schemaV19},
	20: {sql: schemaV20},
	21: {sql: schemaV21},
	22: {sql: schemaV22},
	23: {sql: schemaV23},
	24: {sql: schemaV24},
	25: {sql: schemaV25},
	26: {sql: schemaV26},
	27: {sql: schemaV27},
	28: {sql: schemaV28},
	29: {sql: schemaV29, hook: migrateV29MentalModel},
	30: {sql: schemaV30},
	31: {sql: schemaV31},
	32: {sql: schemaV32},
	33: {sql: schemaV33, hook: migrateV33MindLayer},
	34: {sql: schemaV34, hook: migrateV34Visibility},
	35: {sql: schemaV35},
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

// --- v15 -> v16: the watchlist becomes graph facts ---

// v16WatchRow is one flat `watchlist` row, read whole before any writing
// starts. Deliberately its own type rather than a reuse of WatchItem: that
// type no longer HAS an Observation column or a plain id, and migration code
// is frozen at what the v16 cutover needed — the same ruling legacyQuestion's
// comment states, so a future WatchItem field change can never silently alter
// how a v15 database migrates.
type v16WatchRow struct {
	id           int64
	observation  string
	class        string
	state        string
	sightings    int64
	firstSeen    int64
	lastSeen     int64
	evidence     string
	suggestedFix string
	prURL        string
	updatedAt    int64
	verdict      string
	comment      string
	commentedAt  string
}

// The endpoints a promoted observation hangs off when its sentence isn't
// already a fact in the graph. "dream pass observed <the observation>" is the
// literal truth about a watchlist row: the nightly pass is what saw it, and
// kraken is what it is about. Both nodes are resolved case-insensitively
// against existing names AND aliases before being created, so a graph that
// already knows either one gains no duplicate — that matters most for
// `kraken`, which any real database already has and which must never become
// two nodes.
const (
	v16SrcNode     = "dream pass"
	v16SrcNodeDesc = "The nightly corroboration pass that reads the previous day's feedback and transcripts."
	v16DstNode     = "kraken"
	v16DstNodeDesc = "The kraken orchestrator this graph describes."
	v16NodeType    = "component"
	v16Predicate   = "observed"
	// A promoted observation is a fact carl's dream loop already vetted, so it
	// lands mid-scale rather than at either extreme: 5 of 10 says "recorded and
	// believed", without claiming the corroboration an extractor's high score
	// means.
	v16EdgeStrength = 5
)

// migrateV16Watchlist is migrations[16]'s Go hook: schemaV16's SQL has already
// created kg_watch by the time this runs. What is left is the promotion of
// every existing watchlist row into a graph fact plus an overlay, which SQL
// alone cannot express — it has to hash episode content, resolve endpoint
// nodes through two case-insensitive lookups, and fold rows that collide onto
// one edge.
//
// The order per row is: normalize → episode → edge → overlay. Each step is
// find-or-create rather than blind-insert, because the graph may ALREADY
// contain the fact this row is about (an extraction agent read the same
// observation out of a transcript months ago), and minting a second edge
// saying the same sentence is exactly the near-duplicate the graph's revision
// pass then has to clean up by hand.
//
// STRICT, unlike migrateV5Questions' log-and-continue: any SQL error fails the
// whole migration. That difference is not inconsistency — v5 was PARSING
// hand-written YAML, where one malformed blob is a data defect that must not
// block the other blobs. Nothing here parses anything, so a failure is a
// broken database rather than a broken row, and the only per-row skip is S2.2's
// empty observation (below), which is a row with no content to promote.
//
// ALL states migrate, including shipped and declined. Those are suppression
// signals: the dream loop reads them to learn an observation has been ruled on
// and must not be re-proposed, so dropping them would make every declined
// observation come back as fresh the first night after the migration.
func migrateV16Watchlist(tx *sql.Tx) error {
	rows, err := v16WatchlistRows(tx)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	// One resolve per endpoint across the whole migration: the two fallback
	// nodes are the same for every row, and re-resolving them per row would be
	// two queries times the table's length for an answer that cannot change.
	nodes := map[string]int64{}
	for _, r := range rows {
		// The stored observation is normalized the way normalizeGraphAdd
		// normalizes a fact on its way in (watch.go's oneLine), because that is
		// what makes the match below meaningful: the edges this compares
		// against were all stored through that same rule. A row that is empty
		// once collapsed carries no observation at all — there is no sentence
		// to make a fact out of — so it is skipped rather than promoted into a
		// blank fact nothing can ever match or read.
		obs := oneLine(r.observation)
		if obs == "" {
			continue
		}
		episodeID, err := v16Episode(tx, r, now)
		if err != nil {
			return err
		}
		edgeID, err := v16Edge(tx, r, obs, episodeID, now, nodes)
		if err != nil {
			return err
		}
		if err := v16Overlay(tx, edgeID, r); err != nil {
			return err
		}
	}

	// Every watchlist-kind episode is marked done, not just the ones this hook
	// created. A PENDING watchlist episode is one the v15 sweep enqueued and
	// the extraction agent never drained; the row it describes has just been
	// promoted to a first-class fact by this very migration, so extracting it
	// later would only mint a near-duplicate edge of something the graph now
	// states directly.
	if _, err := tx.Exec(`UPDATE kg_episodes SET state = 'done' WHERE kind = 'watchlist'`); err != nil {
		return fmt.Errorf("mark watchlist episodes done: %w", err)
	}
	// The flat table goes, per this repo's clean-cutover ruling: one machine,
	// one database, one move — no dual-mode read path, no dormant copy to drift
	// out of sync with the overlay. Its indexes go with it.
	if _, err := tx.Exec(`DROP TABLE watchlist`); err != nil {
		return fmt.Errorf("drop watchlist: %w", err)
	}
	// ...and so does the sweep watermark that pointed at it (internal/jobs'
	// graphSrcWatchlist). Leaving the cursor row behind would strand a
	// watermark for a source no pass reads any more, which is the kind of thing
	// that reads as live configuration years later.
	if _, err := tx.Exec(`DELETE FROM kg_ingest_cursor WHERE source = 'rows:watchlist'`); err != nil {
		return fmt.Errorf("drop watchlist ingest cursor: %w", err)
	}
	return nil
}

// v16WatchlistRows reads the whole flat table, ordered by id, split out only so
// the rows cursor is closed before this hook's writes run against the same
// transaction — the same reason v12DistinctEventSlugs exists.
//
// Ordered by id, and the order is load-bearing rather than tidy: when two rows
// fold onto one edge (S2.5), the fold's tiebreak on equal last_seen is "the
// row processed first wins", so a deterministic input order is what makes the
// migration reproducible.
func v16WatchlistRows(tx *sql.Tx) ([]v16WatchRow, error) {
	rows, err := tx.Query(`SELECT id, COALESCE(observation,''), COALESCE(class,''), COALESCE(state,''),
		COALESCE(sightings,0), COALESCE(first_seen,0), COALESCE(last_seen,0),
		COALESCE(evidence,''), COALESCE(suggested_fix,''), COALESCE(pr_url,''),
		COALESCE(updated_at,0), COALESCE(verdict,''), COALESCE(comment,''),
		COALESCE(commented_at,'') FROM watchlist ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read watchlist: %w", err)
	}
	defer rows.Close()
	var out []v16WatchRow
	for rows.Next() {
		var r v16WatchRow
		if err := rows.Scan(&r.id, &r.observation, &r.class, &r.state, &r.sightings,
			&r.firstSeen, &r.lastSeen, &r.evidence, &r.suggestedFix, &r.prURL,
			&r.updatedAt, &r.verdict, &r.comment, &r.commentedAt); err != nil {
			return nil, fmt.Errorf("scan watchlist row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read watchlist rows: %w", err)
	}
	return out, nil
}

// v16Episode finds-or-creates the provenance episode for one migrated row,
// rendering its content in EXACTLY the format the retiring sweepWatchlist used
// (internal/jobs/graph.go) and hashing it with addEpisode's rule
// (sha256 over kind, ref and content, NUL-separated).
//
// Reproducing both verbatim is the entire point: a v15 database whose sweep
// already ingested this row holds an episode with precisely that hash, so
// find-or-create REUSES it instead of storing the same text twice under two
// ids. Change either the template or the hash rule and the migration silently
// doubles every already-swept row's provenance.
//
// The row's raw observation goes into the content (the sweep's own behaviour),
// while the FACT gets the normalized form — the two differ only for an
// observation carrying odd whitespace, and the content is a provenance record
// of what the table actually held.
//
// The episode is created `done`: it describes a row this migration has already
// turned into a fact, so handing it to the extraction agent later would ask it
// to re-derive an edge that exists.
func v16Episode(tx *sql.Tx, r v16WatchRow, now int64) (int64, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "watchlist observation (%s, %s, seen %dx): %s", r.class, r.state, r.sightings, r.observation)
	if r.suggestedFix != "" {
		b.WriteString("\nsuggested fix: " + r.suggestedFix)
	}
	if r.verdict != "" {
		b.WriteString("\ncarl's verdict: " + r.verdict)
		if r.comment != "" {
			b.WriteString(" — " + r.comment)
		}
	}
	content := b.String()
	ref := fmt.Sprintf("watchlist:%d", r.id)
	sum := sha256.Sum256([]byte("watchlist" + "\x00" + ref + "\x00" + content))
	hash := hex.EncodeToString(sum[:])

	var id int64
	err := tx.QueryRow(`SELECT id FROM kg_episodes WHERE hash = ?`, hash).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("find watchlist episode for row %d: %w", r.id, err)
	}
	res, err := tx.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('watchlist', ?, ?, ?, ?, 'done', ?)`, ref, content, hash, r.lastSeen, now)
	if err != nil {
		return 0, fmt.Errorf("insert watchlist episode for row %d: %w", r.id, err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert watchlist episode for row %d: %w", r.id, err)
	}
	return id, nil
}

// v16Edge returns the edge this observation becomes: the live edge whose fact
// already says it, or a freshly inserted fallback edge.
//
// MATCH (lowest live edge id whose LOWER(fact) equals the normalized
// observation): that edge IS the watch edge, and nothing about it is rewritten
// — its endpoints, predicate and strength are whatever the graph decided, which
// is better information than this migration has. The ONE exception is a
// provenance backfill: an edge with episode_id = 0 was hand-planted with no
// source, and this row is a real source for it, so the episode id lands there.
// That is a sanctioned one-time mutation of an otherwise insert-only table,
// called out here because W5 (edges are insert-only outside
// valid_until/expired_at) is a standing invariant and this is its only
// exception.
//
// Lowest id wins among several matches for the same reason FindLiveEdgeByFact
// picks it: the oldest edge is the one the rest of the graph already points at.
//
// NO MATCH: a fallback edge is inserted mechanically.
//
// occurrences is 1, NOT the row's sightings, and the distinction is the whole
// reason the two counters exist separately (S3.10). occurrences counts
// DISTINCT EPISODES independently stating the fact — the graph's corroboration
// measure — and a flat watchlist row is exactly ONE episode however many times
// the dream loop sighted the pattern. The recurrence count is not lost by
// this: it lands verbatim in kg_watch.sightings, where the DREAM view reads it.
//
// Seeding occurrences from sightings instead would hand every migrated fact an
// unearned rerank advantage, and the size of it is measurable rather than
// theoretical: core's fuseScore (graphsearch.go) multiplies an edge's fused
// rank by 1 + occBoostScale*log1p(occurrences), and on this machine's database
// at v15 the highest occurrences on any live edge is 2 while watchlist
// sightings reach 19. Every promoted row would have outranked the graph's
// genuinely best-corroborated facts on a number that measures something else
// entirely. This also matches the runtime add path, which floors occurrences
// at 1 (InsertEdge).
//
// valid_from is the row's first_seen (when the fact started being true in the
// world), while created_at is now (when kraken learned it) — the bi-temporal
// split schemaV15 describes. FTS stays in sync automatically through kg_edges'
// insert trigger.
func v16Edge(tx *sql.Tx, r v16WatchRow, obs string, episodeID, now int64, nodes map[string]int64) (int64, error) {
	var edgeID, edgeEpisode int64
	err := tx.QueryRow(`SELECT id, episode_id FROM kg_edges
		WHERE expired_at = 0 AND LOWER(fact) = LOWER(?) ORDER BY id LIMIT 1`, obs).Scan(&edgeID, &edgeEpisode)
	if err == nil {
		if edgeEpisode == 0 {
			if _, err := tx.Exec(`UPDATE kg_edges SET episode_id = ? WHERE id = ?`, episodeID, edgeID); err != nil {
				return 0, fmt.Errorf("backfill episode on edge %d: %w", edgeID, err)
			}
		}
		return edgeID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("find edge for watchlist row %d: %w", r.id, err)
	}

	srcID, err := v16Node(tx, nodes, v16SrcNode, v16SrcNodeDesc, now)
	if err != nil {
		return 0, err
	}
	dstID, err := v16Node(tx, nodes, v16DstNode, v16DstNodeDesc, now)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO kg_edges
		(src_id, dst_id, predicate, fact, episode_id, strength, occurrences, valid_from, valid_until, embedding, created_at, expired_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, 0, x'', ?, 0)`,
		srcID, dstID, v16Predicate, obs, episodeID, v16EdgeStrength, r.firstSeen, now)
	if err != nil {
		return 0, fmt.Errorf("insert edge for watchlist row %d: %w", r.id, err)
	}
	edgeID, err = res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert edge for watchlist row %d: %w", r.id, err)
	}
	return edgeID, nil
}

// v16Node resolves an endpoint by canonical name then by alias, both
// case-insensitively, and creates the node only when neither matches — the
// same precedence ResolveNode/UpsertNode use, re-implemented against this
// hook's *sql.Tx because those methods query s.db, OUTSIDE the migration's
// transaction, and so cannot see the rows this hook has already written. A
// call through them would miss the `dream pass` node created for the first
// promoted row and insert a second one on the next.
//
// An EXISTING node's description is left exactly as it is. This migration
// knows less about `kraken` than whatever wrote the node did, and overwriting
// a richer gloss with a one-liner is the failure UpsertNode's
// "an extractor that saw less detail must not blank a richer gloss" rule
// exists to prevent. The descriptions above are therefore create-time seeds,
// not assertions.
//
// The alias arm is ordered by node id for the reason ResolveNode states:
// kg_node_aliases' primary key is (node_id, alias), so two nodes CAN carry the
// same alias, and without an order the winner would be whichever row SQLite
// handed back first.
func v16Node(tx *sql.Tx, nodes map[string]int64, name, desc string, now int64) (int64, error) {
	if id, ok := nodes[name]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM kg_nodes WHERE LOWER(name) = LOWER(?)`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`SELECT n.id FROM kg_node_aliases a JOIN kg_nodes n ON n.id = a.node_id
			WHERE LOWER(a.alias) = LOWER(?) ORDER BY n.id LIMIT 1`, name).Scan(&id)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("resolve node %q: %w", name, err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		res, insErr := tx.Exec(`INSERT INTO kg_nodes (name, type, description, embedding, created_at, updated_at)
			VALUES (?, ?, ?, x'', ?, ?)`, name, v16NodeType, desc, now, now)
		if insErr != nil {
			return 0, fmt.Errorf("insert node %q: %w", name, insErr)
		}
		id, insErr = res.LastInsertId()
		if insErr != nil {
			return 0, fmt.Errorf("insert node %q: %w", name, insErr)
		}
	}
	nodes[name] = id
	return id, nil
}

// v16Overlay writes one row's twelve overlay columns onto its edge, FOLDING
// when a previous row already claimed the same edge.
//
// The fold is not a hypothetical. The flat table's lookup normalized only
// surrounding whitespace and case, while an edge fact is matched on the fully
// collapsed form, so two rows differing in INTERNAL whitespace — which the old
// FindWatchItem treated as distinct observations and happily stored twice —
// land on one edge here. Something has to decide what the merged overlay says,
// and "last writer wins outright" would silently drop the older row's
// recurrence count, which is the one number the whole feature turns on.
//
// So: the row with the greater last_seen wins every field (it is the more
// recent statement of class/state/evidence/fix/pr and of carl's judgement),
// EXCEPT the two where both rows genuinely carry information — sightings sums
// (the pattern really was seen that many times in total) and first_seen takes
// the min (the earliest either row saw it is when it was first seen). Ties on
// last_seen keep the earlier-processed row's fields, which by
// v16WatchlistRows' id ordering means the lower row id.
//
// first_seen takes a literal min, including of a zero: a 0 first_seen is a row
// the flat table never stamped, and inventing a later timestamp for it would
// claim knowledge this migration does not have.
func v16Overlay(tx *sql.Tx, edgeID int64, r v16WatchRow) error {
	var exLast, exSightings, exFirst int64
	err := tx.QueryRow(`SELECT last_seen, sightings, first_seen FROM kg_watch WHERE edge_id = ?`, edgeID).
		Scan(&exLast, &exSightings, &exFirst)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(`INSERT INTO kg_watch (edge_id, class, state, sightings, first_seen, last_seen,
			evidence, suggested_fix, pr_url, updated_at, verdict, comment, commented_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			edgeID, r.class, r.state, r.sightings, r.firstSeen, r.lastSeen, r.evidence,
			r.suggestedFix, r.prURL, r.updatedAt, r.verdict, r.comment, r.commentedAt); err != nil {
			return fmt.Errorf("insert watch overlay for row %d: %w", r.id, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read watch overlay for edge %d: %w", edgeID, err)
	}

	sightings := exSightings + r.sightings
	firstSeen := exFirst
	if r.firstSeen < firstSeen {
		firstSeen = r.firstSeen
	}
	if r.lastSeen <= exLast {
		// The stored row is the more recent one: only the two accumulating
		// columns move.
		if _, err := tx.Exec(`UPDATE kg_watch SET sightings = ?, first_seen = ? WHERE edge_id = ?`,
			sightings, firstSeen, edgeID); err != nil {
			return fmt.Errorf("fold watch overlay for row %d: %w", r.id, err)
		}
		return nil
	}
	if _, err := tx.Exec(`UPDATE kg_watch SET class = ?, state = ?, sightings = ?, first_seen = ?,
		last_seen = ?, evidence = ?, suggested_fix = ?, pr_url = ?, updated_at = ?,
		verdict = ?, comment = ?, commented_at = ? WHERE edge_id = ?`,
		r.class, r.state, sightings, firstSeen, r.lastSeen, r.evidence, r.suggestedFix,
		r.prURL, r.updatedAt, r.verdict, r.comment, r.commentedAt, edgeID); err != nil {
		return fmt.Errorf("fold watch overlay for row %d: %w", r.id, err)
	}
	return nil
}

// v29WatchRow is one live kg_watch overlay joined to the edge that carried its
// observation — everything migrateV29MentalModel has to carry into watch_items,
// read out in one pass before any wipe statement runs.
type v29WatchRow struct {
	observation, src, dst, predicate string
	class, state                     string
	sightings, firstSeen, lastSeen   int64
	evidence, suggestedFix, prURL    string
	updatedAt                        int64
	verdict, comment, commentedAt    string
	createdAt                        int64
}

// migrateV29MentalModel is migrations[29]'s Go hook: schemaV29's SQL has
// already created mm_items, mm_code_anchors and watch_items by the time this
// runs. What is left is everything DESTRUCTIVE, and it is a hook rather than
// more SQL for one reason of sequencing — the carry-across reads kg_watch and
// kg_edges, and the wipe empties both, so the two halves have to be ordered
// against each other by something that can hold the rows in memory in between.
//
// The order is: read the live overlays → insert them into watch_items → drop
// kg_watch → empty every kg_* table → rebuild kg_episodes with the widened
// CHECK. Nothing after the first step can change what the first step saw.
//
// STRICT, like migrateV16Watchlist and unlike migrateV5Questions: nothing here
// parses anything, so a failure is a broken database rather than a broken row.
//
// WHY THE GRAPH IS WIPED rather than filtered or carried: the graph's whole
// corpus up to this point was swept out of transcripts, feedback, artifacts and
// job specs, and the sibling memory-usefulness cell measured what that corpus
// was worth — of 2,556 hand-classified fact deliveries across 15 days, 4 were
// ever used. The cutover re-points the graph at carl's own mental model, and
// there is no honest partial migration from one corpus to the other: an
// episode's kind says where it came from, and every kind that exists today is
// one the sweeps produced. This is the v11/v14 argument in a different domain —
// derived state with no correct in-place correction is re-derived, not patched.
// The cost is stated where carl reads it (the board renders a near-empty graph
// until he marks things) rather than hidden, and Q10 puts a manual backup in
// front of the upgrade.
//
// ALL states carry across, shipped and declined included, for
// migrateV16Watchlist's reason: those are suppression signals, and dropping
// them would make every declined observation come back as fresh the first
// night after the migration.
//
// An overlay whose edge is NOT live is deliberately left behind. Every read in
// the v16 design INNER JOINed the live edge, so such a row was already invisible
// to the board and to the dream loop — carrying it into watch_items would
// RESURRECT observations kraken had unlearned, which is the one thing this
// migration must not do while it is otherwise moving rows sideways.
func migrateV29MentalModel(tx *sql.Tx) error {
	rows, err := v29LiveWatchRows(tx)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, r := range rows {
		// created_at is when this watch BEGAN, and the row it comes from has
		// no such column — first_seen is that moment by definition, the
		// edge's own created_at is the next best witness, and now is the
		// floor so a carried row never reads as created at the epoch.
		created := r.firstSeen
		if created == 0 {
			created = r.createdAt
		}
		if created == 0 {
			created = now
		}
		if _, err := tx.Exec(`INSERT INTO watch_items (observation, src, dst, predicate, class, state,
			sightings, first_seen, last_seen, evidence, suggested_fix, pr_url, updated_at,
			verdict, comment, commented_at, created_at, expired_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			r.observation, r.src, r.dst, r.predicate, r.class, r.state,
			r.sightings, r.firstSeen, r.lastSeen, r.evidence, r.suggestedFix, r.prURL,
			r.updatedAt, r.verdict, r.comment, r.commentedAt, created); err != nil {
			return fmt.Errorf("carry watch overlay into watch_items: %w", err)
		}
	}

	// The overlay table goes, per this repo's clean-cutover ruling: one
	// machine, one database, one move — no dual-mode read path and no dormant
	// copy to drift out of sync with the standalone table.
	if _, err := tx.Exec(`DROP TABLE kg_watch`); err != nil {
		return fmt.Errorf("drop kg_watch: %w", err)
	}

	// The wipe. kg_edges goes first so its AFTER DELETE triggers can post one
	// 'delete' command per row into the external-content FTS index; the
	// 'rebuild' afterwards makes emptiness unconditional rather than
	// contingent on that index having been in sync beforehand.
	for _, stmt := range []string{
		`DELETE FROM kg_edges`,
		`INSERT INTO kg_edges_fts(kg_edges_fts) VALUES('rebuild')`,
		`DELETE FROM kg_nodes`,
		`DELETE FROM kg_node_aliases`,
		`DELETE FROM kg_recall`,
		`DELETE FROM kg_layout`,
		`DELETE FROM kg_ingest_cursor`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("wipe graph (%s): %w", stmt, err)
		}
	}

	// kg_episodes is REBUILT rather than emptied, because its `kind` CHECK is
	// a closed set written into the table definition and SQLite has no ALTER
	// for one. The rebuild is a drop-and-create rather than the usual
	// create-copy-drop-rename dance precisely because the wipe means there is
	// nothing to copy — but the new definition still has to carry both indexes
	// forward by hand, since dropping a table drops its indexes with it.
	//
	// 'mental-model' is ADDED to the legacy five rather than replacing them.
	// Widening is the safe direction: a CHECK that no longer admits a kind
	// some straggler code path still writes would fail an INSERT at runtime,
	// where a kind nothing writes any more is inert. The absence of writers for
	// the retired kinds is pinned by a test, not by this constraint.
	for _, stmt := range []string{
		`DROP TABLE kg_episodes`,
		`CREATE TABLE kg_episodes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT    NOT NULL CHECK(kind IN ('transcript','feedback','watchlist','artifact','job','mental-model')),
			ref        TEXT    NOT NULL DEFAULT '',
			content    TEXT    NOT NULL DEFAULT '',
			hash       TEXT    NOT NULL,
			ts         INTEGER NOT NULL DEFAULT 0,
			state      TEXT    NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','done')),
			created_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX kg_episodes_hash ON kg_episodes(hash)`,
		`CREATE INDEX kg_episodes_state ON kg_episodes(state, id)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("rebuild kg_episodes: %w", err)
		}
	}
	return nil
}

// v29LiveWatchRows reads every kg_watch overlay whose edge is still LIVE,
// joined to that edge's sentence and endpoint names — the same shape
// watchItemSelect served the v16 verbs, so a row comes across carrying exactly
// what the board was rendering for it.
//
// Split out so the rows cursor is closed before this hook's writes run against
// the same transaction, the reason v12DistinctEventSlugs and v16WatchlistRows
// exist: this package runs on ONE connection.
//
// The node joins are LEFT and every column is COALESCEd for watchItemSelect's
// reasons — kg_* carries no foreign keys, so a dangling src_id must cost the
// row its endpoint LABEL rather than its whole observation.
//
// Ordered by edge id, which is what makes the migration reproducible: watch_items
// ids are assigned in this order.
func v29LiveWatchRows(tx *sql.Tx) ([]v29WatchRow, error) {
	rows, err := tx.Query(`SELECT COALESCE(e.fact,''), COALESCE(sn.name,''), COALESCE(dn.name,''),
		COALESCE(e.predicate,''), COALESCE(w.class,''), COALESCE(w.state,''),
		COALESCE(w.sightings,0), COALESCE(w.first_seen,0), COALESCE(w.last_seen,0),
		COALESCE(w.evidence,''), COALESCE(w.suggested_fix,''), COALESCE(w.pr_url,''),
		COALESCE(w.updated_at,0), COALESCE(w.verdict,''), COALESCE(w.comment,''),
		COALESCE(w.commented_at,''), COALESCE(e.created_at,0)
		FROM kg_watch w
		JOIN kg_edges e ON e.id = w.edge_id AND e.expired_at = 0
		LEFT JOIN kg_nodes sn ON sn.id = e.src_id
		LEFT JOIN kg_nodes dn ON dn.id = e.dst_id
		ORDER BY w.edge_id`)
	if err != nil {
		return nil, fmt.Errorf("read kg_watch overlays: %w", err)
	}
	defer rows.Close()
	var out []v29WatchRow
	for rows.Next() {
		var r v29WatchRow
		if err := rows.Scan(&r.observation, &r.src, &r.dst, &r.predicate, &r.class, &r.state,
			&r.sightings, &r.firstSeen, &r.lastSeen, &r.evidence, &r.suggestedFix, &r.prURL,
			&r.updatedAt, &r.verdict, &r.comment, &r.commentedAt, &r.createdAt); err != nil {
			return nil, fmt.Errorf("scan kg_watch overlay: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read kg_watch overlay rows: %w", err)
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
