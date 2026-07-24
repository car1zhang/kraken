package store

import "fmt"

// schemaVersion is the current embedded schema version, tracked in SQLite's
// own PRAGMA user_version so migrate() is a no-op on an already-current
// database (open/migrate idempotence). Bump this and append a new
// migration step when the schema changes; version 1 creates everything the
// spec's Data Model section calls for, version 2 adds the questions table,
// version 3 drops jobs.type (Q14 — the build/info job-level type concept is
// gone; the pipeline distinction now lives in /aim vs /info skill choice
// plus carl's /do steering, never a stored column), version 4 adds
// prs.conflict_state.
const schemaVersion = 4

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

// migrations maps a target user_version to the SQL that gets a freshly
// created (or older) database there. migrations[1] is the original
// release; migrations[2] adds the questions table; migrations[3] drops
// jobs.type; migrations[4] adds prs.conflict_state. Future schema changes
// append migrations[5], migrations[6], ... rather than editing an existing
// schemaV<N> string in place, so a database created under an older binary
// still migrates forward correctly.
var migrations = map[int]string{
	1: schemaV1,
	2: schemaV2,
	3: schemaV3,
	4: schemaV4,
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
		stmt, ok := migrations[v]
		if !ok {
			return fmt.Errorf("no migration registered for version %d", v)
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("apply migration %d: %w", v, err)
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
