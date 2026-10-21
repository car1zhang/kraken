package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// downgradeFreshDB rolls a freshly-opened Store (already migrated all the
// way to schemaVersion) back to look like a genuine on-disk database from
// an older user_version — not just "missing the one column this test cares
// about," but the FULL shape a real kraken.db at that version actually had,
// reversing every later migration's change in reverse order. This matters
// once schemaVersion grows past whatever a "vN-shaped" test originally
// targeted: migrate() always walks forward to the CURRENT schemaVersion, so
// a test simulating "a v2 database" must hand migrate() something that
// looks exactly like a real v2 database, or a later migration's SQL (e.g.
// v5's `ALTER TABLE jobs DROP COLUMN skip_doc`) fails against a shape that
// was never real — skip_doc, dropped only at v5, is still present at v1-v4,
// so rolling back to any of those must re-add it, not leave it gone.
// undoV29 reverses migration 29 on a store that has already migrated past it:
// the mental model's two tables and the standalone watch_items go, kg_watch
// comes back in its exact v16 shape (index included), and kg_episodes is
// rebuilt with the LEGACY five-kind CHECK. A real database at any version from
// 16 to 28 has that overlay table and that narrower constraint.
//
// It is a named helper rather than another arm inlined in downgradeFreshDB
// because v29 is the first migration whose reversal is more than a DROP — its
// hook DROPs kg_watch, so a replay finds the table missing unless something
// puts it back — and four sibling test files roll their own shape walks that
// each need exactly this step first.
//
// The rows v29 carried across are NOT carried back, the same ruling
// downgradeFreshDB's v16 and v11 arms state: this reproduces an older SHAPE,
// and a test that wants pre-migration rows seeds them afterwards. Neither is
// the graph v29 emptied refilled — there is nothing to refill it from.
func undoV29(t *testing.T, s *Store) {
	t.Helper()
	// v29's hook reads and rewrites the kg_* tables v32 dropped, so any walk
	// back past v29 first needs them present again.
	undoV32(t, s)
	for _, stmt := range []string{
		`DROP TABLE mm_code_anchors`,
		`DROP TABLE mm_items`,
		`DROP TABLE watch_items`,
		`CREATE TABLE kg_watch (
			edge_id       INTEGER PRIMARY KEY,
			class         TEXT    NOT NULL DEFAULT '',
			state         TEXT    NOT NULL DEFAULT 'watching',
			sightings     INTEGER NOT NULL DEFAULT 1,
			first_seen    INTEGER NOT NULL DEFAULT 0,
			last_seen     INTEGER NOT NULL DEFAULT 0,
			evidence      TEXT    NOT NULL DEFAULT '',
			suggested_fix TEXT    NOT NULL DEFAULT '',
			pr_url        TEXT    NOT NULL DEFAULT '',
			updated_at    INTEGER NOT NULL DEFAULT 0,
			verdict       TEXT    NOT NULL DEFAULT '',
			comment       TEXT    NOT NULL DEFAULT '',
			commented_at  TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX kg_watch_state_last_seen ON kg_watch(state, last_seen)`,
		`DROP TABLE kg_episodes`,
		`CREATE TABLE kg_episodes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT    NOT NULL CHECK(kind IN ('transcript','feedback','watchlist','artifact','job')),
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
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("undo v29 %q: %v", stmt, err)
		}
	}
}

// undoV32 reverses migration 32: it recreates every kg_* object v32 dropped,
// in the shape a real v31 database holds them — v15's six tables, FTS5 index
// and triggers, v19's kg_recall, and kg_episodes rebuilt with v29's widened
// kind CHECK. Shape only, no rows (v29 emptied the graph, so a real v29-v31
// database's kg tables are empty unless mental-model episodes were minted).
//
// It is a no-op when kg_edges already exists, so undoV29 can call it
// unconditionally and downgradeFreshDB can call it too without the two
// colliding on a bare CREATE.
func undoV32(t *testing.T, s *Store) {
	t.Helper()
	// Walking back past v32 means walking back past v33 first.
	undoV33(t, s)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'kg_edges'`).Scan(&n); err != nil {
		t.Fatalf("undo v32 probe: %v", err)
	}
	if n > 0 {
		return
	}
	for _, stmt := range []string{
		schemaV15,
		schemaV19,
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
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("undo v32 %q: %v", stmt, err)
		}
	}
}

// undoV33 reverses migration 33: mm_flags goes, threads.role and
// threads.turn_uuid are dropped, and
// mm_items is rebuilt in its exact v29 shape (the six-value source CHECK, none
// of the v33 columns), its rows carried back in their v29 columns. A no-op
// when mm_flags is already gone, so undoV32 can call it unconditionally.
func undoV33(t *testing.T, s *Store) {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'mm_flags'`).Scan(&n); err != nil {
		t.Fatalf("undo v33 probe: %v", err)
	}
	if n == 0 {
		return
	}
	for _, stmt := range []string{
		`DROP TABLE mm_flags`,
		`ALTER TABLE threads DROP COLUMN role`,
		`ALTER TABLE threads DROP COLUMN turn_uuid`,
		`CREATE TABLE mm_items_old (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			kind           TEXT    NOT NULL CHECK(kind IN ('decision','reading','note')),
			source         TEXT    NOT NULL CHECK(source IN ('question','artifact','artifact-element','code','agent-output','manual')),
			job_id         INTEGER NOT NULL DEFAULT 0,
			slug           TEXT    NOT NULL DEFAULT '',
			source_ref     TEXT    NOT NULL DEFAULT '',
			content        TEXT    NOT NULL,
			context        TEXT    NOT NULL DEFAULT '',
			content_sha    TEXT    NOT NULL DEFAULT '',
			snapshot       BLOB    NULL,
			snapshot_ext   TEXT    NOT NULL DEFAULT '',
			episode_id     INTEGER NOT NULL DEFAULT 0,
			created_at     INTEGER NOT NULL DEFAULT 0,
			invalidated_at INTEGER NULL,
			invalid_reason TEXT    NOT NULL DEFAULT '',
			invalid_detail TEXT    NOT NULL DEFAULT ''
		)`,
		`INSERT INTO mm_items_old SELECT id, kind, source, job_id, slug, source_ref, content, context,
			content_sha, snapshot, snapshot_ext, episode_id, created_at, invalidated_at,
			invalid_reason, invalid_detail FROM mm_items WHERE source <> 'capture'`,
		`DROP TABLE mm_items`,
		`ALTER TABLE mm_items_old RENAME TO mm_items`,
		`CREATE INDEX mm_items_ref ON mm_items(source, job_id, source_ref)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("undo v33 %q: %v", stmt, err)
		}
	}
}

func downgradeFreshDB(t *testing.T, s *Store, target int) {
	t.Helper()
	// Reverse v35: the stack-links table goes. Shape-only, newest first.
	if target < 35 {
		if _, err := s.db.Exec(`DROP TABLE job_stacks`); err != nil {
			t.Fatalf("downgrade to v%d (undo v35) drop job_stacks: %v", target, err)
		}
	}
	// Reverse v34: the fleet's two tables and jobs.visibility go.
	if target < 34 {
		for _, stmt := range []string{
			`DROP TABLE nodes`,
			`DROP TABLE pair_devices`,
			`ALTER TABLE jobs DROP COLUMN visibility`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v34) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v33: the mind-layer columns, threads.role and mm_flags go.
	if target < 33 {
		undoV33(t, s)
	}
	// Reverse v32: put the knowledge graph's tables back (empty). Reversed
	// after v33, since downgrades unwind newest-first.
	if target < 32 {
		undoV32(t, s)
	}
	// Reverse v31: put orch_inbox back in its exact v24 shape (schemaV24 is
	// that DDL verbatim). Shape-only, like every arm here: the five kv rows
	// v31 deletes are not recreated, since an older SHAPE is what this helper
	// reproduces and a kv row is data. The v24 arm below then drops the table
	// again for any target that predates it. Reversed after v32, since
	// downgrades unwind newest-first.
	if target < 31 {
		if _, err := s.db.Exec(schemaV24); err != nil {
			t.Fatalf("downgrade to v%d (undo v31) recreate orch_inbox: %v", target, err)
		}
	}
	// Reverse v30: drop the jobs.agent column (added at v30, absent through
	// v29). Shape-only, no data carried back — the codex/claude choice has no
	// place to live in a pre-v30 database. Reversed FIRST, since downgrades
	// unwind newest-first, mirroring the order migrate() applies them.
	if target < 30 {
		if _, err := s.db.Exec(`ALTER TABLE jobs DROP COLUMN agent`); err != nil {
			t.Fatalf("downgrade to v%d (undo v30) drop agent: %v", target, err)
		}
	}
	// Reverse v29: drop the mental model's two tables and the standalone
	// watch_items, put kg_watch back in its exact v16 shape (index included),
	// and rebuild kg_episodes with the LEGACY five-kind CHECK — a real
	// database at any version from 16 to 28 has that overlay table and that
	// narrower constraint, so a downgrade that only dropped the new tables
	// would hand migrate() a shape that was never real and v29's hook would
	// fail on the missing kg_watch.
	//
	// The rows v29 carried across are NOT carried back, the same ruling the
	// v16 and v11 arms below state: this helper reproduces an older SHAPE, and
	// a test that wants pre-migration rows seeds them after this call. Neither
	// is the graph v29 emptied refilled — there is nothing to refill it from.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 29 {
		undoV29(t, s)
	}
	// Reverse v28: drop the three account-binding columns (added at v28,
	// absent through v27). Shape-only, with no data to carry back: a binding
	// names a pool slot a LIVE cell is billing, so a database rolled back past
	// v28 has no place to record one and no reader that would look — the v26
	// arm's situation rather than v27's, which had to carry links.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 28 {
		for _, stmt := range []string{
			`ALTER TABLE jobs DROP COLUMN account_slug`,
			`ALTER TABLE jobs DROP COLUMN account_email`,
			`ALTER TABLE jobs DROP COLUMN account_at`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v28) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v27: put jobs.goal_id back, repopulate it from each job's FIRST
	// link, and drop job_goals. Unlike every other arm here this one carries
	// DATA back, and it has to: v27 is the only migration so far that moved
	// existing rows rather than adding an empty table, so a shape-only reversal
	// would hand migrate() a v26 database whose links had silently vanished —
	// and v27's own backfill would then re-run over an empty column, turning a
	// round trip through this helper into a wipe.
	//
	// LOWEST position wins, since that link is the one v27's wire calls the
	// PRIMARY (`goal` is `goals[0]`), and goal_id can only hold one. A job with
	// several links loses the rest, which is what a downgrade to a
	// one-goal-per-job shape means; goal_id itself carries the ON DELETE SET
	// NULL clause the pre-v27 column had, or v23's own reversal below would find
	// a column it does not recognise.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 27 {
		for _, stmt := range []string{
			`ALTER TABLE jobs ADD COLUMN goal_id INTEGER REFERENCES goals(id) ON DELETE SET NULL`,
			`UPDATE jobs SET goal_id = (
				SELECT jg.goal_id FROM job_goals jg
				WHERE jg.job_id = jobs.id
				ORDER BY jg.position, jg.goal_id
				LIMIT 1
			)`,
			`DROP TABLE job_goals`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v27) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v26: drop board_bg. Nothing to put back — v26 is a single
	// additive CREATE over a v25 shape that had no such table, so the drop is
	// the whole reversal (the v18 arm below owns the bare-CREATE-vs-IF-NOT-
	// EXISTS reasoning this arm exists to serve).
	//
	// Reversed after v27, for that block's reason: downgrades unwind
	// newest-first, mirroring the order migrate() applies them.
	if target < 26 {
		if _, err := s.db.Exec(`DROP TABLE board_bg`); err != nil {
			t.Fatalf("downgrade to v%d (undo v26): %v", target, err)
		}
	}
	// Reverse v25: drop saved_artifacts. Nothing to put back — v25 is a single
	// additive CREATE over a v24 shape that had no such table, so the drop is
	// the whole reversal (the v18 arm below owns the bare-CREATE-vs-IF-NOT-
	// EXISTS reasoning this arm exists to serve), and its UNIQUE index goes
	// with it, as SQLite drops a table's indexes along with the table.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 25 {
		if _, err := s.db.Exec(`DROP TABLE saved_artifacts`); err != nil {
			t.Fatalf("downgrade to v%d (undo v25): %v", target, err)
		}
	}
	// Reverse v24: drop the retired orchestrator's one table (recreated by the
	// v31 arm above). It is brand new at v24 (schemaV24
	// adds it and touches nothing else), so there is nothing to put back in
	// its place and no rows to carry — the v17 arm's situation exactly, and
	// the same ruling: this helper reproduces an older SHAPE.
	if target < 24 {
		for _, stmt := range []string{
			`DROP TABLE orch_inbox`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v24) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v23: drop jobs.goal_id and the goals table (both added at v23,
	// absent through v22). The column goes FIRST: it carries the REFERENCES
	// clause, so dropping the table out from under it would leave jobs holding
	// a foreign key onto nothing. goals_scope_period goes with its table —
	// SQLite drops a table's indexes along with it — and there are no rows to
	// carry back, since a pre-v23 database has never had a goal.
	//
	// Dropping a column named in a foreign key is exactly what the v6 arm
	// below already does to feedback.job_id, so the bundled SQLite allows it.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 23 {
		for _, stmt := range []string{
			`ALTER TABLE jobs DROP COLUMN goal_id`,
			`DROP TABLE goals`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v23) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v22: drop threads.model (added at v22, absent through v21). Pure
	// shape, nothing to carry back — the column's whole content is a choice
	// made at branch time, and a pre-v22 database has no thread that ever had
	// one. The jobs.color arm below is the precedent for the single-column
	// form.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 22 {
		if _, err := s.db.Exec(`ALTER TABLE threads DROP COLUMN model`); err != nil {
			t.Fatalf("downgrade to v%d (undo v22, drop threads.model): %v", target, err)
		}
	}
	// Reverse v21: drop job_former_slugs (its index goes with the table).
	// Nothing to put back — v21 is a single additive CREATE over a v20 shape
	// that had no such table, so the drop is the whole reversal (the v18 arm
	// below owns the bare-CREATE-vs-IF-NOT-EXISTS reasoning).
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 21 {
		if _, err := s.db.Exec(`DROP TABLE job_former_slugs`); err != nil {
			t.Fatalf("downgrade to v%d (undo v21): %v", target, err)
		}
	}
	// Reverse v20: put back jobs.ready and the prereqs table, both of which
	// v20 dropped and both of which every version through v19 really had.
	// board_order's queued rows are not restored: v20's DELETE removes rows
	// rather than a shape, and this helper reproduces SHAPES — a test that
	// wants queued rows in a pre-v20 database inserts them itself.
	if target < 20 {
		for _, stmt := range []string{
			`ALTER TABLE jobs ADD COLUMN ready INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE prereqs (
				job_id      INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				prereq_slug TEXT NOT NULL,
				gated       INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (job_id, prereq_slug)
			)`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v20) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v19: drop kg_recall. Nothing to put back — v19 is a single
	// additive CREATE over a v18 shape that had no such table, so the drop is
	// the whole reversal (the v18 arm below owns the bare-CREATE-vs-IF-NOT-
	// EXISTS reasoning this arm exists to serve).
	if target < 19 {
		if _, err := s.db.Exec(`DROP TABLE kg_recall`); err != nil {
			t.Fatalf("downgrade to v%d (undo v19): %v", target, err)
		}
	}
	// Reverse v18: drop linear_issues. Nothing to put back — v18 is a single
	// additive CREATE over a v17 shape that had no such table, so the drop is
	// the whole reversal.
	//
	// This block is why schemaV18's DDL is a bare CREATE rather than a
	// CREATE IF NOT EXISTS: replaying a migration over a database that already
	// has its table is a shape only this helper can produce, and the remedy
	// belongs here rather than in a production guard that would also swallow a
	// genuinely unexpected table at Open().
	//
	// Reversed after v19, for that block's reason: downgrades unwind
	// newest-first, mirroring the order migrate() applies them.
	if target < 18 {
		if _, err := s.db.Exec(`DROP TABLE linear_issues`); err != nil {
			t.Fatalf("downgrade to v%d (undo v18): %v", target, err)
		}
	}
	// Reverse v17: drop the Slack intake bot's two tables. Both are brand new
	// at v17 (schemaV17 adds them and touches nothing else), so there is
	// nothing to put back in their place and no rows to carry — the same
	// situation as the v15 arm below, and the same ruling: this helper
	// reproduces an older SHAPE.
	//
	// Reversed after v18, for that block's reason: downgrades unwind
	// newest-first, mirroring the order migrate() applies them.
	if target < 17 {
		for _, stmt := range []string{
			`DROP TABLE slack_inbox`,
			`DROP TABLE slack_marks`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v17) %q: %v", target, stmt, err)
			}
		}
	}

	// Reverse v16: drop kg_watch and put the flat watchlist table back, in the
	// exact v9 shape (both indexes included) — a real database at any version
	// from 9 to 15 HAS that table, so a downgrade that only dropped kg_watch
	// would hand migrate() a shape that was never real and v16's hook would
	// fail on the missing table.
	//
	// The rows v16 promoted into the graph are NOT carried back, the same
	// ruling the v11 comment below states for its row-deleting half: this
	// helper reproduces an older SHAPE, and a test that wants pre-migration
	// rows seeds them after this call.
	//
	// Reversed after v17, for that block's reason: downgrades unwind
	// newest-first, mirroring the order migrate() applies them.
	if target < 16 {
		for _, stmt := range []string{
			`DROP TABLE kg_watch`,
			`CREATE TABLE watchlist (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				observation   TEXT NOT NULL DEFAULT '',
				class         TEXT NOT NULL DEFAULT '',
				state         TEXT NOT NULL DEFAULT 'watching',
				sightings     INTEGER NOT NULL DEFAULT 1,
				first_seen    INTEGER NOT NULL DEFAULT 0,
				last_seen     INTEGER NOT NULL DEFAULT 0,
				evidence      TEXT NOT NULL DEFAULT '',
				suggested_fix TEXT NOT NULL DEFAULT '',
				pr_url        TEXT NOT NULL DEFAULT '',
				updated_at    INTEGER NOT NULL DEFAULT 0,
				verdict       TEXT NOT NULL DEFAULT '',
				comment       TEXT NOT NULL DEFAULT '',
				commented_at  TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX watchlist_state_last_seen ON watchlist(state, last_seen)`,
			`CREATE INDEX watchlist_observation_key ON watchlist(LOWER(TRIM(observation, char(32,9,10,13))))`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v16) %q: %v", target, stmt, err)
			}
		}
	}

	// Reverse v15: drop the knowledge graph's seven tables (all added at
	// v15, absent through v14). kg_edges' three FTS-sync triggers go with
	// their table — SQLite drops a table's triggers along with it — and
	// dropping the fts5 virtual table removes its shadow tables. v14 itself
	// needs no reversal: it changed no shape, only deleted rows, and this
	// helper's job is to reproduce an older SHAPE (the same ruling the v11
	// comment below states for its row-deleting half).
	if target < 15 {
		for _, stmt := range []string{
			`DROP TABLE kg_layout`,
			`DROP TABLE kg_ingest_cursor`,
			`DROP TABLE kg_edges_fts`,
			`DROP TABLE kg_edges`,
			`DROP TABLE kg_node_aliases`,
			`DROP TABLE kg_nodes`,
			`DROP TABLE kg_episodes`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v15) %q: %v", target, stmt, err)
			}
		}
	}

	// Reverse v13: drop the four job-load columns (added at v13, absent
	// through v12). Pure shape, no rows to carry back — the values only ever
	// come from a live write.
	if target < 13 {
		for _, stmt := range []string{
			`ALTER TABLE jobs DROP COLUMN load_unblock`,
			`ALTER TABLE jobs DROP COLUMN load_scope`,
			`ALTER TABLE jobs DROP COLUMN load_note`,
			`ALTER TABLE jobs DROP COLUMN load_at`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v13) %q: %v", target, stmt, err)
			}
		}
	}

	// Reverse v12: put board_order and the two token tables back on the slug,
	// drop events.job_id, and restore the drafts table. The token tables are
	// dropped and recreated rather than converted, because v12 deletes every
	// row regardless — there is nothing a downgrade could carry back.
	//
	// The recreated job_tokens carries the four zeus columns: downgrading from
	// v12 lands on v11, not v10, and v11 is where those columns live. Leaving
	// them out here would make a "downgrade to 11" produce a v10 shape and
	// quietly break the v11 step that runs next.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 12 {
		for _, stmt := range []string{
			`DROP TABLE board_order`,
			`CREATE TABLE board_order (
				"column"  TEXT NOT NULL,
				position  INTEGER NOT NULL,
				slug      TEXT NOT NULL,
				PRIMARY KEY ("column", position)
			)`,
			`ALTER TABLE events DROP COLUMN job_id`,
			`DROP TABLE job_tokens`,
			`DROP TABLE job_token_files`,
			`CREATE TABLE job_tokens (
				job_slug         TEXT PRIMARY KEY NOT NULL,
				in_tokens        INTEGER NOT NULL DEFAULT 0,
				out_tokens       INTEGER NOT NULL DEFAULT 0,
				cache_write      INTEGER NOT NULL DEFAULT 0,
				cache_read       INTEGER NOT NULL DEFAULT 0,
				sessions         INTEGER NOT NULL DEFAULT 0,
				updated_at       INTEGER NOT NULL DEFAULT 0,
				zeus_in          INTEGER NOT NULL DEFAULT 0,
				zeus_out         INTEGER NOT NULL DEFAULT 0,
				zeus_cache_write INTEGER NOT NULL DEFAULT 0,
				zeus_cache_read  INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE TABLE job_token_files (
				job_slug    TEXT NOT NULL,
				path        TEXT NOT NULL,
				byte_offset INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (job_slug, path)
			)`,
			`CREATE TABLE drafts (
				job_slug   TEXT PRIMARY KEY NOT NULL,
				text       TEXT NOT NULL DEFAULT '',
				updated_at INTEGER NOT NULL DEFAULT 0
			)`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d: %v", target, err)
			}
		}
	}

	// Reverse v11: drop job_tokens' four zeus columns (added at v11, absent
	// through v10). The v11 migration's OTHER half — emptying both token
	// tables — is deliberately not reversed: it destroys rows rather than
	// shape, and a downgrade helper's job is to reproduce an older SHAPE. A
	// test that wants pre-migration rows seeds them after this call.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 11 {
		for _, stmt := range []string{
			`ALTER TABLE job_tokens DROP COLUMN zeus_in`,
			`ALTER TABLE job_tokens DROP COLUMN zeus_out`,
			`ALTER TABLE job_tokens DROP COLUMN zeus_cache_write`,
			`ALTER TABLE job_tokens DROP COLUMN zeus_cache_read`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v11) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v10: drop the two token tables (added at v10, absent through
	// v9). Both are brand-new tables with no indexes of their own, so
	// dropping them is the whole reversal.
	if target < 10 {
		for _, stmt := range []string{
			`DROP TABLE job_token_files`,
			`DROP TABLE job_tokens`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v10) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v9: drop the watchlist table (added at v9, absent through v8)
	// and the feedback(ts) index it shipped alongside. The watchlist's own
	// indexes go with the table — SQLite drops a table's indexes along with
	// it — but feedback_ts sits on a table that SURVIVES, so it has to be
	// dropped by name or the v9 migration's CREATE INDEX would collide on the
	// way back up.
	//
	if target < 9 {
		for _, stmt := range []string{
			`DROP TABLE watchlist`,
			`DROP INDEX feedback_ts`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v9) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v8: drop jobs.color (added at v8, absent through v7).
	if target < 8 {
		if _, err := s.db.Exec(`ALTER TABLE jobs DROP COLUMN color`); err != nil {
			t.Fatalf("downgrade to v%d (undo v8, drop jobs.color): %v", target, err)
		}
	}
	// Reverse v7: drop the threads table entirely (added at v7, absent
	// through v6). Its unique index goes with it — SQLite drops a table's
	// indexes along with the table.
	if target < 7 {
		if _, err := s.db.Exec(`DROP TABLE threads`); err != nil {
			t.Fatalf("downgrade to v%d (undo v7, drop threads): %v", target, err)
		}
	}
	// Reverse v6: drop feedback.job_id (added at v6, absent through v5).
	if target < 6 {
		if _, err := s.db.Exec(`ALTER TABLE feedback DROP COLUMN job_id`); err != nil {
			t.Fatalf("downgrade to v%d (undo v6, drop feedback.job_id): %v", target, err)
		}
	}
	// Reverse v5: re-add jobs.skip_doc (dropped at v5, present through v4),
	// drop feedback's v5 columns (added at v5, absent through v4), and swap
	// the relational questions table back for schemaV2's blob shape (the
	// shape every version through v4 used).
	if target < 5 {
		for _, stmt := range []string{
			`ALTER TABLE jobs ADD COLUMN skip_doc INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE feedback DROP COLUMN session`,
			`ALTER TABLE feedback DROP COLUMN addressed`,
			`ALTER TABLE feedback DROP COLUMN addressed_at`,
			`DROP TABLE questions`,
			`CREATE TABLE questions (
				job_id INTEGER PRIMARY KEY NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				bytes  BLOB,
				mtime  INTEGER NOT NULL DEFAULT 0
			)`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v5) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v4: drop prs.conflict_state (added at v4, absent through v3).
	if target < 4 {
		if _, err := s.db.Exec(`ALTER TABLE prs DROP COLUMN conflict_state`); err != nil {
			t.Fatalf("downgrade to v%d (undo v4, drop prs.conflict_state): %v", target, err)
		}
	}
	// Reverse v3: re-add jobs.type (dropped at v3, present through v2).
	if target < 3 {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'build'`); err != nil {
			t.Fatalf("downgrade to v%d (undo v3, re-add type): %v", target, err)
		}
	}
	// Reverse v2: drop the questions table entirely — v1 never had one
	// (must run after the v5-undo block above, which (re)creates it).
	if target < 2 {
		if _, err := s.db.Exec(`DROP TABLE questions`); err != nil {
			t.Fatalf("downgrade to v%d (undo v2, drop questions): %v", target, err)
		}
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, target)); err != nil {
		t.Fatalf("reset user_version to %d: %v", target, err)
	}
}

// TestSchemaMigratesForwardFromV1 covers finding #1: a real
// kraken.db can exist at PRAGMA user_version = 1 (schemaV1's tables only,
// no questions table, jobs.type and jobs.skip_doc both still present) from
// before the doc-questions feature shipped. Reopening such a database must
// walk forward through every migration up to the current schemaVersion —
// creating the questions table (v2), dropping jobs.type (v3), adding
// prs.conflict_state (v4), and dropping jobs.skip_doc plus restructuring
// questions into relational rows (v5) —
// rather than silently leaving any of it undone (migrate() only applies
// versions above the database's CURRENT version, so an in-place edit to an
// already-released schemaV<N> string — the bug this test guards against —
// would never run against an existing v1 database).
func TestSchemaMigratesForwardFromV1(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the
	// real re-Open below, rather than only ever calling s.migrate() in
	// process.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	downgradeFreshDB(t, s, 1)

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v1: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// the (now relational) questions table must exist and be usable.
	id, err := s.CreateJob(&Job{Slug: "post-migrate"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "one", Importance: "P0"}); err != nil {
		t.Fatalf("UpsertQuestion after migration: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions after migration: %v", err)
	}
	if len(qs) != 1 || qs[0].Text != "one" {
		t.Fatalf("unexpected questions after migration: %+v", qs)
	}

	// Fidelity check (finding #7): the assertions above all ran an
	// in-process s.migrate() call directly — they don't prove a genuinely
	// fresh process opening a v1-shaped kraken.db off disk (the real-world
	// scenario) gets the same treatment through the actual Open() entry
	// point. Roll the same on-disk file back to v1-shaped again, close it,
	// and re-Open it for real.
	downgradeFreshDB(t, s, 1)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v1-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	id2, err := s2.CreateJob(&Job{Slug: "post-reopen"})
	if err != nil {
		t.Fatalf("CreateJob after re-Open: %v", err)
	}
	if err := s2.UpsertQuestion(id2, DocQuestion{QID: "Q1", Text: "two", Importance: "P1"}); err != nil {
		t.Fatalf("UpsertQuestion after re-Open: %v", err)
	}
	qs2, err := s2.ListQuestions(id2)
	if err != nil {
		t.Fatalf("ListQuestions after re-Open: %v", err)
	}
	if len(qs2) != 1 || qs2[0].Text != "two" {
		t.Fatalf("unexpected questions after re-Open migration: %+v", qs2)
	}
}

// TestSchemaMigratesForwardFromV2 covers Q14: a real kraken.db can exist at
// PRAGMA user_version = 2 (schemaV1+schemaV2's tables, including the jobs.type
// column with its CHECK(type IN ('build','info')) constraint) from before the
// job-level type concept was deleted. Reopening such a database must walk
// forward through every migration up to the current schemaVersion — dropping
// jobs.type (v3), adding prs.conflict_state (v4), then dropping
// jobs.skip_doc and restructuring questions (v5) —
// go-sqlite3 bundles a modern SQLite with native ALTER TABLE ... DROP
// COLUMN support (no rebuild-the-table dance needed), so each drop is a
// single statement.
func TestSchemaMigratesForwardFromV2(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring TestSchemaMigratesForwardFromV1.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	downgradeFreshDB(t, s, 2)
	if _, err := s.db.Exec(`INSERT INTO jobs (slug, type, repo) VALUES ('v2-shaped', 'info', 'kraken')`); err != nil {
		t.Fatalf("seed v2-shaped row: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v2: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// the type column must be gone.
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "type" {
			t.Fatal("jobs.type column survived migration")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}

	// the seeded row must survive intact, minus the dropped column.
	j, err := s.GetJobBySlug("v2-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Repo != "kraken" {
		t.Fatalf("row mangled by migration: %+v", j)
	}

	// Fidelity check: the assertions above all ran an in-process s.migrate()
	// call directly — roll the same on-disk file back to v2-shaped again,
	// close it, and re-Open it for real, mirroring
	// TestSchemaMigratesForwardFromV1's second-downgrade check.
	downgradeFreshDB(t, s, 2)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v2-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	if _, err := s2.GetJobBySlug("v2-shaped"); err != nil {
		t.Fatalf("row missing after re-Open: %v", err)
	}
}

// TestFreshOpenEndsAtCurrentSchemaVersion pins the fresh-database path
// (never seen an older version on disk) also lands at the current
// schemaVersion — distinct from TestOpenMigrateIdempotent, which
// round-trips an already-current database, and the "vN-shaped" tests above,
// which upgrade an older one.
func TestFreshOpenEndsAtCurrentSchemaVersion(t *testing.T) {
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("fresh open user_version = %d, want %d", version, schemaVersion)
	}
	// jobs.type/skip_doc must never exist on a freshly created database
	// either (migrations[1..6] all apply inside the same transaction).
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "type" || name == "skip_doc" {
			t.Fatalf("fresh database has a jobs.%s column", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
}

// TestOpenRefusesNewerSchema covers athena-edge-g's G-1 finding: migrate()'s
// loop only ever walks forward (version+1 up to schemaVersion), so a binary
// whose schemaVersion is older than the on-disk user_version silently
// no-ops and returns success — the db then fails deep inside the first
// query that touches a column/table only a later migration added or
// dropped, instead of erroring clearly at Open(). Simulate "an older
// binary opens a db written by a newer one" by bumping user_version above
// schemaVersion via a raw PRAGMA, closing, and re-Opening.
func TestOpenRefusesNewerSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	newerV := schemaVersion + 1
	if _, err := s.db.Exec(`PRAGMA user_version = ` + strconv.Itoa(newerV)); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Open a db from a newer schema version: want error, got nil")
	}
	wantV := strconv.Itoa(newerV)
	if !strings.Contains(err.Error(), "schema v"+wantV) || !strings.Contains(err.Error(), "upgrade the binary") {
		t.Fatalf("Open error = %q, want it to name v%s and say to upgrade the binary", err.Error(), wantV)
	}
}

// TestSchemaV4ToV5MigratesQuestionsAndFeedback covers the v5 migration's
// full contract (54-feedback-contract.md): jobs.skip_doc drops, feedback
// gains session/addressed/addressed_at defaulted empty on pre-existing
// rows, and questions.yaml blob rows become relational question rows —
// preserving file order as position — while a malformed blob is SKIPPED
// with a logged event rather than aborting the whole migration (carl's
// clean-cutover ruling: one-time move, best effort on bad legacy data).
func TestSchemaV4ToV5MigratesQuestionsAndFeedback(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the
	// real re-Open below, same pattern as the tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	goodID, err := s.CreateJob(&Job{Slug: "good-job"})
	if err != nil {
		t.Fatalf("CreateJob good: %v", err)
	}
	badID, err := s.CreateJob(&Job{Slug: "bad-job"})
	if err != nil {
		t.Fatalf("CreateJob bad: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobSlug: "good-job", Artifact: "explain.html", Note: "pre-existing", TS: 5}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	// Roll this fresh (already-migrated-to-v5) database back to a
	// v4-shaped one: re-add jobs.skip_doc, drop feedback's v5 columns, and
	// swap the relational questions table back out for schemaV2's blob
	// shape, faithfully reproducing what a real pre-v5 kraken.db looks
	// like on disk. downgradeFreshDB seeds neither jobs nor the blob rows
	// (goodID/badID/the feedback row above were created before the
	// downgrade, so they already exist and survive it) — the legacy
	// question blobs still need seeding by hand below.
	downgradeFreshDB(t, s, 4)

	goodBlob := []byte("questions:\n" +
		"  - id: Q1\n" +
		"    text: is this right?\n" +
		"    importance: P1\n" +
		"    answer: yes\n" +
		"    answered-at: 2026-01-01T00:00:00Z\n" +
		"  - id: Q2\n" +
		"    text: second one\n" +
		"    importance: P0\n")
	if _, err := s.db.Exec(`INSERT INTO questions (job_id, bytes, mtime) VALUES (?, ?, ?)`, goodID, goodBlob, 100); err != nil {
		t.Fatalf("seed good blob: %v", err)
	}
	// Missing "importance" — the original strict parser would reject this
	// item, so the migration must skip the whole blob rather than guess.
	badBlob := []byte("questions:\n  - id: Q1\n    text: broken\n")
	if _, err := s.db.Exec(`INSERT INTO questions (job_id, bytes, mtime) VALUES (?, ?, ?)`, badID, badBlob, 200); err != nil {
		t.Fatalf("seed bad blob: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v3: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// jobs.skip_doc must be gone.
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info(jobs): %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "skip_doc" {
			rows.Close()
			t.Fatal("jobs.skip_doc survived migration to v5")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(jobs) rows: %v", err)
	}
	rows.Close()

	// feedback's new columns exist and default empty on the pre-existing row.
	list, err := s.ListFeedback(goodID)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("feedback row lost across migration: %+v", list)
	}
	if list[0].Session != "" || list[0].Addressed != "" || list[0].AddressedAt != "" {
		t.Fatalf("pre-existing feedback row should default new columns empty: %+v", list[0])
	}
	if list[0].Note != "pre-existing" {
		t.Fatalf("feedback row mangled by migration: %+v", list[0])
	}

	// good-job's blob parsed into two rows, in file order, answer/answered_at
	// carried over from the blob.
	qs, err := s.ListQuestions(goodID)
	if err != nil {
		t.Fatalf("ListQuestions good: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("want 2 migrated questions, got %+v", qs)
	}
	if qs[0].QID != "Q1" || qs[0].Text != "is this right?" || qs[0].Importance != "P1" ||
		qs[0].Answer != "yes" || qs[0].AnsweredAt != "2026-01-01T00:00:00Z" || qs[0].Position != 0 {
		t.Fatalf("Q1 migrated wrong: %+v", qs[0])
	}
	if qs[1].QID != "Q2" || qs[1].Text != "second one" || qs[1].Importance != "P0" || qs[1].Position != 1 {
		t.Fatalf("Q2 migrated wrong: %+v", qs[1])
	}

	// bad-job's blob was unparseable and must be SKIPPED, not fatal to the
	// whole migration — no rows for it, and a logged event naming it.
	badQs, err := s.ListQuestions(badID)
	if err != nil {
		t.Fatalf("ListQuestions bad: %v", err)
	}
	if len(badQs) != 0 {
		t.Fatalf("malformed blob should not have produced any rows: %+v", badQs)
	}
	events, err := s.ListEvents(50, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Event == "questions-migration-skip" && e.JobSlug == "bad-job" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no logged event for the skipped malformed blob; events = %+v", events)
	}

	// questions_blob must be gone — its job is done.
	var blobTableExists int
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='questions_blob'`).Scan(&blobTableExists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("questions_blob table survived migration: err=%v", err)
	}

	// Fidelity check: the assertions above all ran an in-process
	// s.migrate() call directly — roll the same on-disk file back to
	// v4-shaped again, close it, and re-Open it for real, mirroring the
	// other migration tests' second-downgrade check.
	downgradeFreshDB(t, s, 4)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v4-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade recreated a blob-shaped, EMPTY questions table (no
	// re-seeded bytes for goodID this time) — re-Open's migration should
	// leave goodID with zero questions rather than erroring, which is
	// enough to prove the relational table survives another real Open()
	// round-trip.
	qs2, err := s2.ListQuestions(goodID)
	if err != nil {
		t.Fatalf("ListQuestions after re-Open: %v", err)
	}
	if len(qs2) != 0 {
		t.Fatalf("expected no questions after the 2nd (unseeded) downgrade+re-Open: %+v", qs2)
	}
}

// TestSchemaV5ToV6BackfillsFeedbackJobID covers the v6 migration (B2):
// existing feedback rows, seeded straight through the pre-v6 shape (no
// job_id column), must have job_id backfilled by matching job_slug against
// an active job's slug first, then an archived job's archived_name — and a
// row matching neither (an orphaned fossil, e.g. its job was later
// hard-deleted) must be left with job_id 0/NULL rather than failing the
// whole migration, same best-effort ruling as v5's malformed-blob handling.
func TestSchemaV5ToV6BackfillsFeedbackJobID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	activeID, err := s.CreateJob(&Job{Slug: "active-job"})
	if err != nil {
		t.Fatalf("CreateJob active: %v", err)
	}
	archivedID, err := s.CreateJob(&Job{Slug: "archived-job"})
	if err != nil {
		t.Fatalf("CreateJob to-archive: %v", err)
	}
	archivedName, err := s.ArchiveJob(archivedID)
	if err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	downgradeFreshDB(t, s, 5)

	// Seed three feedback rows straight through the raw v5 shape (no job_id
	// column yet): one matching an active job's slug, one matching an
	// archived job's archived_name, and one matching neither (an orphan —
	// e.g. its job was later hard-deleted, or the row simply predates
	// anything this migration can resolve).
	for _, r := range []struct{ slug, note string }{
		{"active-job", "for the active job"},
		{archivedName, "for the archived job"},
		{"ghost-job", "orphaned, no matching job"},
	} {
		if _, err := s.db.Exec(
			`INSERT INTO feedback (job_slug, artifact, note, ts) VALUES (?, ?, ?, ?)`,
			r.slug, "a.md", r.note, 100,
		); err != nil {
			t.Fatalf("seed feedback %q: %v", r.slug, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v5: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	rows, err := s.ListFeedback(0)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	byNote := map[string]Feedback{}
	for _, f := range rows {
		byNote[f.Note] = f
	}
	if got, ok := byNote["for the active job"]; !ok || got.JobID != activeID {
		t.Fatalf("active job feedback job_id = %+v, want JobID %d", got, activeID)
	}
	if got, ok := byNote["for the archived job"]; !ok || got.JobID != archivedID {
		t.Fatalf("archived job feedback job_id = %+v, want JobID %d", got, archivedID)
	}
	if got, ok := byNote["orphaned, no matching job"]; !ok || got.JobID != 0 {
		t.Fatalf("orphaned feedback job_id = %+v, want JobID 0 (unmatched, left NULL)", got)
	}
}

// TestSchemaV6ToV7AddsThreads covers the v7 migration: a real kraken.db can
// exist at PRAGMA user_version = 6 (no threads table at all) from before
// side threads shipped. Reopening such a database must create the table —
// usable immediately, unique index and FK cascade included — while leaving
// every pre-existing row untouched, since v7 is a pure add with no data to
// move forward (migrations[7] carries no hook).
func TestSchemaV6ToV7AddsThreads(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring the migration tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID, err := s.CreateJob(&Job{Slug: "v6-shaped", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobID: jobID, JobSlug: "v6-shaped", Note: "pre-existing", TS: 5}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	downgradeFreshDB(t, s, 6)

	// the table really is gone at the v6 shape — otherwise this test would
	// pass without the migration doing anything.
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='threads'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("threads table still present at the v6 shape: err=%v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v6: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// the new table exists and is usable end to end.
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Title: "post-migrate", Pane: "%1"}); err != nil {
		t.Fatalf("AddThread after migration: %v", err)
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads after migration: %v", err)
	}
	if len(list) != 1 || list[0].Title != "post-migrate" {
		t.Fatalf("unexpected threads after migration: %+v", list)
	}
	// ...including the unique index the migration's DDL carries.
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1"}); err == nil {
		t.Fatal("duplicate tid accepted after migration: threads_job_tid index missing")
	}

	// pre-existing rows survive untouched.
	j, err := s.GetJobBySlug("v6-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Repo != "kraken" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}
	fb, err := s.ListFeedback(jobID)
	if err != nil {
		t.Fatalf("ListFeedback after migration: %v", err)
	}
	if len(fb) != 1 || fb[0].Note != "pre-existing" {
		t.Fatalf("feedback row mangled by migration: %+v", fb)
	}

	// Fidelity check: the assertions above ran an in-process s.migrate()
	// call directly — roll the same on-disk file back to v6-shaped again,
	// close it, and re-Open it for real, mirroring the other migration
	// tests' second-downgrade check.
	downgradeFreshDB(t, s, 6)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v6-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade dropped the (seeded) threads table, so the re-created
	// one starts empty — enough to prove the table survives a real Open()
	// round-trip and numbering restarts cleanly on it.
	next, err := s2.NextThreadID(jobID)
	if err != nil {
		t.Fatalf("NextThreadID after re-Open: %v", err)
	}
	if next != "t1" {
		t.Fatalf("NextThreadID on the re-created table = %q, want t1", next)
	}
}

// TestSchemaV8ToV9AddsWatchlistAndFeedbackTSIndex covers the v9 migration: a
// real kraken.db can exist at PRAGMA user_version = 8 (no watchlist table, no
// feedback(ts) index) from before the dream loop shipped. Reopening such a
// database must create both, while leaving every pre-existing row untouched,
// since v9 is a pure add with no data to move forward (migrations[9] carries
// no hook).
//
// What it can assert about the watchlist has now changed twice, and the test
// is written for the chain rather than for v9 alone: migrate() always walks to
// the CURRENT schemaVersion, so a v8-shaped database now ends up past v29 —
// where the flat table v9 created was promoted into the graph as an overlay at
// v16 and then moved back out into the standalone watch_items at v29, with
// BOTH intermediate tables dropped behind it. The v9 half is therefore checked
// through its end state (watch_items present, `watchlist` and `kg_watch` both
// gone, and v9's other half — the feedback(ts) index — still there), which is
// what "reopening a v8 database works" actually means today.
func TestSchemaV8ToV9AddsWatchlistAndFeedbackTSIndex(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring the migration tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID, err := s.CreateJob(&Job{Slug: "v8-shaped", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobID: jobID, JobSlug: "v8-shaped", Note: "pre-existing", TS: 5}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	downgradeFreshDB(t, s, 8)

	// both really are gone at the v8 shape — otherwise this test would pass
	// without the migration doing anything.
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='watchlist'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("watchlist table still present at the v8 shape: err=%v", err)
	}
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='index' AND name='feedback_ts'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("feedback_ts index still present at the v8 shape: err=%v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v7: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// v9 created the flat table, v16 promoted it into the graph and v29 moved
	// it back out, so at head watch_items exists and NEITHER predecessor does
	// — the whole chain ran, not just its first link.
	if err := s.db.QueryRow(
		`SELECT 1 FROM sqlite_master WHERE type='table' AND name='watch_items'`,
	).Scan(&exists); err != nil {
		t.Fatalf("watch_items missing after migration: %v", err)
	}
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='watchlist'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("flat watchlist table survived v16: err=%v", err)
	}
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='kg_watch'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("kg_watch overlay table survived v29: err=%v", err)
	}
	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems after migration: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a v8-shaped db has nothing to promote, got %+v", list)
	}

	// ...and the index the sweep read depends on exists by name.
	if err := s.db.QueryRow(
		`SELECT 1 FROM sqlite_master WHERE type='index' AND name='feedback_ts'`,
	).Scan(&exists); err != nil {
		t.Fatalf("feedback_ts index missing after migration: %v", err)
	}

	// pre-existing rows survive untouched, and the new sweep read sees them.
	j, err := s.GetJobBySlug("v8-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Repo != "kraken" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}
	swept, err := s.FeedbackSince(0)
	if err != nil {
		t.Fatalf("FeedbackSince after migration: %v", err)
	}
	if len(swept) != 1 || swept[0].Note != "pre-existing" {
		t.Fatalf("feedback row mangled by migration: %+v", swept)
	}

	// Fidelity check: the assertions above ran an in-process s.migrate() call
	// directly — roll the same on-disk file back to v8-shaped again, close it,
	// and re-Open it for real, mirroring the other migration tests'
	// second-downgrade check.
	downgradeFreshDB(t, s, 8)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v8-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade rebuilt an empty flat table, so the standalone table
	// the whole chain lands on is empty too — enough to prove the v9 -> v16 ->
	// v29 path survives a real Open() round-trip rather than only an
	// in-process migrate().
	list2, err := s2.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems after re-Open: %v", err)
	}
	if len(list2) != 0 {
		t.Fatalf("expected an empty watchlist after the 2nd downgrade+re-Open: %+v", list2)
	}
}

// TestSchemaV10ToV11SplitsAndWipesTokens covers the v11 migration, whose two
// halves are checked separately because only one of them is the usual kind.
//
// The ordinary half: job_tokens gains four zeus columns, usable immediately.
//
// The half that is the whole point: BOTH token tables are EMPTIED. The
// counters accumulate forward from job_token_files' byte watermarks and
// nothing ever re-reads what is behind one, so a surviving pre-split row would
// hold a job's entire history with the four new columns at zero and report
// that job as 100% subagent forever, with no pass anywhere able to fix it.
// Wiping is what makes the daemon re-sum from byte zero with the split in
// place. A test that only asserted the columns exist would pass against
// exactly the bug this migration exists to avoid.
func TestSchemaV10ToV11SplitsAndWipesTokens(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring the migration tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID, err := s.CreateJob(&Job{Slug: "v10-shaped", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	downgradeFreshDB(t, s, 10)

	// The pre-split rows a real v10 database carries: an all-time total with
	// no split, plus the watermarks that would stop it ever being re-summed.
	// Seeded AFTER the downgrade and with raw SQL, because BumpJobTokens now
	// writes columns the v10 shape does not have.
	if _, err := s.db.Exec(
		`INSERT INTO job_tokens (job_slug, in_tokens, out_tokens, cache_write, cache_read, sessions, updated_at)
			VALUES ('v10-shaped', 111, 222, 333, 444, 9, 1700000000000)`); err != nil {
		t.Fatalf("seed a v10-shaped token row: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO job_token_files (job_slug, path, byte_offset) VALUES ('v10-shaped', '/p/a.jsonl', 4096)`); err != nil {
		t.Fatalf("seed a v10-shaped watermark: %v", err)
	}

	// the columns really are gone at the v10 shape — otherwise this test would
	// pass without the migration doing anything.
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM pragma_table_info('job_tokens') WHERE name = 'zeus_in'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("zeus_in still present at the v10 shape: err=%v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v10: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// BOTH tables are empty: the row AND the watermark that would have stopped
	// the re-sum.
	rows, err := s.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens after migration: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("job_tokens = %+v, want it EMPTIED — an un-backfillable row is worse than no row", rows)
	}
	offs, err := s.AllJobTokenOffsets()
	if err != nil {
		t.Fatalf("AllJobTokenOffsets after migration: %v", err)
	}
	if len(offs) != 0 {
		t.Fatalf("job_token_files = %v, want it EMPTIED — a surviving watermark makes the re-sum a no-op", offs)
	}

	// ...and the re-sum a daemon pass would then do lands with the split.
	if err := s.BumpJobTokens(7, JobTokens{
		In: 111, Out: 222, CacheWrite: 333, CacheRead: 444, Sessions: 9,
		ZeusIn: 40, ZeusOut: 80, ZeusCacheWrite: 100, ZeusCacheRead: 200,
	}); err != nil {
		t.Fatalf("BumpJobTokens after migration: %v", err)
	}
	got, err := s.GetJobTokens(7)
	if err != nil {
		t.Fatalf("GetJobTokens after migration: %v", err)
	}
	if got.In != 111 || got.ZeusIn != 40 || got.ZeusOut != 80 ||
		got.ZeusCacheWrite != 100 || got.ZeusCacheRead != 200 {
		t.Fatalf("row after the re-sum = %+v, want the totals back with the zeus half filled in", got)
	}

	// Rows OUTSIDE the two token tables are untouched: this migration wipes
	// derived state only.
	j, err := s.GetJobBySlug("v10-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.ID != jobID || j.Repo != "kraken" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}

	// Fidelity check: the assertions above ran an in-process s.migrate() call
	// directly — roll the same on-disk file back to v10-shaped again, close
	// it, and re-Open it for real, mirroring the other migration tests'
	// second-downgrade check.
	downgradeFreshDB(t, s, 10)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v10-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade left the (re-summed) row behind with its columns
	// dropped, and the real Open() wiped it again — which is the point: the
	// wipe is part of the migration, not of the test's setup.
	rows2, err := s2.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens after re-Open: %v", err)
	}
	if len(rows2) != 0 {
		t.Fatalf("job_tokens after re-Open = %+v, want the wipe to have run through Open() too", rows2)
	}
}

// schemaDump returns every table/index/trigger definition in the database,
// sorted by name — the whole shape, not just the one table a given test
// happens to care about. Comparing two dumps is what makes "migrated forward"
// and "created fresh" a checkable claim rather than an assumption.
//
// sqlite_autoindex_* entries are skipped: SQLite invents those for implicit
// PRIMARY KEY/UNIQUE constraints, gives them a NULL sql, and numbers them per
// table in creation order — so an ALTER-heavy migration path can legitimately
// end up with the same constraints under different autoindex numbers.
func schemaDump(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT type, name, COALESCE(sql,'') FROM sqlite_master
			WHERE name NOT LIKE 'sqlite_autoindex_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		fmt.Fprintf(&b, "%s %s\n%s\n---\n", typ, name, strings.Join(strings.Fields(ddl), " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master rows: %v", err)
	}
	return b.String()
}

// TestMigratedSchemaMatchesFreshSchema is the invariant every one of the
// per-version migration tests above assumes but none of them actually checks:
// a database that walked the whole migration chain must end up with the SAME
// shape as one created from scratch today. A migration that adds a column
// with a subtly different declaration (missing NOT NULL, a different DEFAULT)
// or forgets an index leaves a database that passes every functional test and
// then behaves differently under load or under a NULL — this compares the
// full sqlite_master dump so that drift is caught structurally instead.
func TestMigratedSchemaMatchesFreshSchema(t *testing.T) {
	fresh := openTest(t)
	want := schemaDump(t, fresh)

	// 15 is included because v16 is the first migration since v12 to change
	// shape by REMOVING something (the flat watchlist table and its two
	// indexes): a hook that created kg_watch but forgot the drop would leave a
	// migrated database carrying a table a fresh one has never had, and only a
	// full-dump comparison from v15 catches that.
	//
	// 28 is included for the same reason one version later: v29 drops kg_watch
	// AND rebuilds kg_episodes to widen its kind CHECK, and a rebuild is the
	// shape-drift case this test exists for — sqlite_master stores the CREATE
	// statement verbatim, so a rebuild that spelled a column differently, or
	// forgot one of the two indexes it has to recreate by hand, would leave a
	// database that passes every functional test and differs structurally.
	for _, from := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 28} {
		dbPath := filepath.Join(t.TempDir(), "kraken.db")
		s, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open (from v%d): %v", from, err)
		}
		downgradeFreshDB(t, s, from)
		if err := s.Close(); err != nil {
			t.Fatalf("Close before re-Open (from v%d): %v", from, err)
		}
		s2, err := Open(dbPath)
		if err != nil {
			t.Fatalf("re-Open a v%d-shaped db: %v", from, err)
		}
		got := schemaDump(t, s2)
		s2.Close()
		if got != want {
			t.Errorf("schema after migrating from v%d differs from a fresh database:\n--- migrated ---\n%s\n--- fresh ---\n%s", from, got, want)
		}
	}
}

// TestSchemaMigrateTwiceIsNoOp pins migrate()'s idempotence directly at the
// current schemaVersion: both v16's and v29's steps are pure CREATE (no IF NOT
// EXISTS) followed by a hook that DROPs a table (`watchlist`, then `kg_watch`),
// so a second application would fail loudly — on "table already exists", or on
// the dropped table — if the user_version guard ever stopped holding it back.
//
// The row it seeds is a watch item, because that is the record both of those
// hooks move: if a second pass ever ran, the survivor is the first thing it
// would trample.
func TestSchemaMigrateTwiceIsNoOp(t *testing.T) {
	s := openTest(t)
	id := seedWatchItem(t, s, "survivor")
	if err := s.migrate(); err != nil {
		t.Fatalf("re-running migrate() on an already-current db: %v", err)
	}
	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].Observation != "survivor" {
		t.Fatalf("a no-op migrate must not touch data: %+v", list)
	}
}

// seedWatchItem records one live observation and returns its row id — the
// minimum a watch item needs now that v29 has moved it back out of the graph
// into a standalone table of its own.
func seedWatchItem(t *testing.T, s *Store, observation string) int64 {
	t.Helper()
	id, folded, err := s.AddWatchItem(WatchItem{Observation: observation, Class: "waste"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if folded {
		t.Fatalf("AddWatchItem folded onto an existing row seeding %q", observation)
	}
	return id
}

// TestSchemaV5ToV6AmbiguousSlugLeavesJobIDNull is the sibling case
// TestSchemaV5ToV6BackfillsFeedbackJobID doesn't reach: a pre-v6 row whose
// job_slug matches an active job's slug AND an archived job's archived_name
// at the same time (gen1 archived under its bare slug, freeing it; gen2
// created live under the same name). Nothing in the pre-v6 data says which
// generation the row belongs to, so it must be left NULL. Resolving it to
// the active job instead is destructive, not merely wrong: archiving that
// job later rekeys by job_id and would drag every such row off the older
// generation's card.
func TestSchemaV5ToV6AmbiguousSlugLeavesJobIDNull(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	gen1, err := s.CreateJob(&Job{Slug: "shared"})
	if err != nil {
		t.Fatalf("CreateJob gen1: %v", err)
	}
	archivedName, err := s.ArchiveJob(gen1)
	if err != nil {
		t.Fatalf("ArchiveJob gen1: %v", err)
	}
	if archivedName != "shared" {
		t.Fatalf("archived name = %q, want the bare slug (fixture assumption)", archivedName)
	}
	gen2, err := s.CreateJob(&Job{Slug: "shared"})
	if err != nil {
		t.Fatalf("CreateJob gen2 (reusing the freed slug): %v", err)
	}

	downgradeFreshDB(t, s, 5)

	if _, err := s.db.Exec(
		`INSERT INTO feedback (job_slug, artifact, note, ts) VALUES (?, ?, ?, ?)`,
		"shared", "a.md", "ambiguous across two generations", 100,
	); err != nil {
		t.Fatalf("seed ambiguous feedback: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v5: %v", err)
	}

	rows, err := s.ListFeedback(0)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("feedback rows = %+v, want exactly the seeded one", rows)
	}
	if rows[0].JobID != 0 {
		t.Fatalf("ambiguous row job_id = %d, want 0/NULL (matched both job %d and job %d)",
			rows[0].JobID, gen2, gen1)
	}
	// and it must be invisible to BOTH generations' scoped reads, not
	// silently attached to one of them
	for _, id := range []int64{gen1, gen2} {
		scoped, err := s.ListFeedback(id)
		if err != nil {
			t.Fatalf("ListFeedback(%d): %v", id, err)
		}
		if len(scoped) != 0 {
			t.Fatalf("ambiguous row surfaced on job %d's scoped read: %+v", id, scoped)
		}
	}
}

// TestKeywordColumnsAreQuotedInDDL pins the house pattern F5 restored: a
// column whose name is a SQLite keyword is quoted in the DDL itself, not
// only at the Go use sites. Both cases in the schema today —
// board_order."column" (schemaV1) and threads."window" (schemaV7) — parse
// unquoted inside a CREATE TABLE, so the bare form looks fine right up until
// someone copies it into an expression, where it is a syntax error. Quoting
// it where the next schema author reads it is what stops that copy.
//
// The second half is the behavioral half: the quoted names must actually
// work in a real expression (WHERE / ORDER BY), which is where an unquoted
// declaration would have stopped being merely stylistic.
func TestKeywordColumnsAreQuotedInDDL(t *testing.T) {
	for _, c := range []struct{ ddl, col string }{
		{schemaV1, "column"},
		{schemaV7, "window"},
	} {
		quoted := `"` + c.col + `"`
		if !strings.Contains(c.ddl, quoted) {
			t.Fatalf("DDL declares %s unquoted; SQLite keyword columns must be written %s", c.col, quoted)
		}
	}

	s := openTest(t)
	jobID := seedThreadJob(t, s, "keyword-cols")
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Pane: "%1", Window: "@4"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	var got string
	if err := s.db.QueryRow(
		`SELECT "window" FROM threads WHERE job_id = ? AND "window" != '' ORDER BY "window"`, jobID,
	).Scan(&got); err != nil {
		t.Fatalf(`querying threads."window" in an expression: %v`, err)
	}
	if got != "@4" {
		t.Fatalf(`threads."window" = %q, want @4`, got)
	}
}

// TestSchemaV7ToV8AddsColor covers the v8 migration against a database that
// really is at v7: the column must appear with every EXISTING row left on
// the empty string — "no colour", which is exactly the behaviour those jobs had before the
// column existed. A migration that defaulted to some palette entry instead
// would silently recolour (and, via the daemon's reconcile, retype) every
// job on the board the first time the new binary ran.
func TestSchemaV7ToV8AddsColor(t *testing.T) {
	s := openTest(t)
	downgradeFreshDB(t, s, 7)

	if _, err := s.db.Exec(
		`INSERT INTO jobs (slug, state, rev, run) VALUES ('legacy', 'working', 1, 1)`,
	); err != nil {
		t.Fatalf("seed v7 row: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v7: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after migrate, want %d", version, schemaVersion)
	}

	var color string
	if err := s.db.QueryRow(`SELECT color FROM jobs WHERE slug = 'legacy'`).Scan(&color); err != nil {
		t.Fatalf("read migrated color: %v", err)
	}
	if color != "" {
		t.Fatalf("pre-existing row got color %q, want '' (no colour)", color)
	}

	// and the column is NOT NULL DEFAULT '', so an insert that never
	// mentions it still scans into a plain string field
	got, err := s.GetJobBySlug("legacy")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if got.Color != "" {
		t.Fatalf("scanned color = %q, want empty", got.Color)
	}
}

// --- v10 -> v11: the job's identity becomes its id ---

// seedV10 opens a store, creates the given jobs, then walks the database back
// to the v10 shape so a test can insert rows the way the pre-cutover binary
// wrote them. Returns the store and the ids, in the order the slugs were
// given.
//
// The jobs are created BEFORE the downgrade because CreateJob/ArchiveJob are
// v11-shaped accessors; the jobs table itself is untouched by v11, so the rows
// they write are exactly what a v10 database would have held.
func seedV10(t *testing.T, slugs ...string) (*Store, []int64) {
	t.Helper()
	s := openTest(t)
	ids := make([]int64, 0, len(slugs))
	for _, slug := range slugs {
		id, err := s.CreateJob(&Job{Slug: slug})
		if err != nil {
			t.Fatalf("CreateJob %q: %v", slug, err)
		}
		ids = append(ids, id)
	}
	downgradeFreshDB(t, s, 10)
	return s, ids
}

// TestMigrateV11RekeysBoardOrderAndEvents walks all four branches of
// resolveV11Slug's precedence in one fixture: an ACTIVE job wins outright, a
// single ARCHIVED job matches on its archived_name, the NEWEST archived job
// breaks a slug tie, and an unmappable slug drops its board_order row (with an
// event logged) while its events rows SURVIVE with a NULL job_id.
func TestMigrateV11RekeysBoardOrderAndEvents(t *testing.T) {
	s := openTest(t)

	// active: holds the slug "live" right now
	activeID, err := s.CreateJob(&Job{Slug: "live"})
	if err != nil {
		t.Fatalf("CreateJob active: %v", err)
	}
	// archived under its own name: "renamed" archives to "renamed"
	namedID, err := s.CreateJob(&Job{Slug: "renamed"})
	if err != nil {
		t.Fatalf("CreateJob named: %v", err)
	}
	archivedName, err := s.ArchiveJob(namedID)
	if err != nil {
		t.Fatalf("ArchiveJob named: %v", err)
	}
	// Three archived generations of the slug "twice", then the first one
	// hard-deleted. ArchiveJob's numbered de-dup means only the FIRST took the
	// bare archived_name "twice", so deleting it leaves no archived_name match
	// at all — which is what forces the third rule (newest archived job with
	// that SLUG) to be the one that fires, rather than the archived_name rule
	// above it silently answering first.
	var twice []int64
	for i := 0; i < 3; i++ {
		id, err := s.CreateJob(&Job{Slug: "twice"})
		if err != nil {
			t.Fatalf("CreateJob twice#%d: %v", i, err)
		}
		if _, err := s.ArchiveJob(id); err != nil {
			t.Fatalf("ArchiveJob twice#%d: %v", i, err)
		}
		twice = append(twice, id)
	}
	if err := s.DeleteJob(twice[0]); err != nil {
		t.Fatalf("DeleteJob twice#0 (frees the bare archived_name): %v", err)
	}
	oldID, newID := twice[1], twice[2]

	downgradeFreshDB(t, s, 10)

	for i, slug := range []string{"live", archivedName, "twice", "vanished"} {
		if _, err := s.db.Exec(
			`INSERT INTO board_order ("column", position, slug) VALUES (?, ?, ?)`,
			"draft", i, slug,
		); err != nil {
			t.Fatalf("seed board_order %q: %v", slug, err)
		}
	}
	for _, slug := range []string{"live", "vanished"} {
		if _, err := s.db.Exec(
			`INSERT INTO events (ts, src, level, event, msg, job_slug) VALUES (?, ?, ?, ?, ?, ?)`,
			100, "daemon", "info", "seed", "hello "+slug, slug,
		); err != nil {
			t.Fatalf("seed events %q: %v", slug, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v10: %v", err)
	}

	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	want := []int64{activeID, namedID, newID}
	if len(order["draft"]) != len(want) {
		t.Fatalf("draft = %v, want %v (the unmappable slug dropped)", order["draft"], want)
	}
	for i, id := range want {
		if order["draft"][i] != id {
			t.Fatalf("draft[%d] = %d, want %d (order preserved, positions repacked)", i, order["draft"][i], id)
		}
	}
	if order["draft"][2] == oldID {
		t.Fatal("the OLDER archived generation won the slug tie; newest must win")
	}

	// the dropped board_order row left a note behind, in the same transaction
	events, err := s.ListEvents(500, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var logged bool
	byMsg := map[string]Event{}
	for _, e := range events {
		byMsg[e.Msg] = e
		if e.Event == "board-order-migration-drop" && e.JobSlug == "vanished" {
			logged = true
		}
	}
	if !logged {
		t.Fatal("an unmappable board_order row must be dropped WITH an events row saying so")
	}

	// events: backfilled where mappable, kept-with-NULL where not
	if got := byMsg["hello live"]; got.JobID != activeID {
		t.Fatalf("events job_id for the live job = %d, want %d", got.JobID, activeID)
	}
	got, ok := byMsg["hello vanished"]
	if !ok {
		t.Fatal("an events row whose slug maps to nothing must SURVIVE — it is a fossil by design")
	}
	if got.JobID != 0 {
		t.Fatalf("unmappable events row job_id = %d, want 0/NULL", got.JobID)
	}
	if got.JobSlug != "vanished" {
		t.Fatalf("job_slug = %q, want it kept as the display fossil", got.JobSlug)
	}
}

// TestMigrateV11ClearsTokenTables pins Q4: the counters are REGENERATED by the
// daemon, never migrated. There is no honest mapping — on the live database
// five archived jobs share the slug `asdf`, so their single row measures all
// five together — and re-summing ~110 cell dirs inside the migration
// transaction is exactly the synchronous work the incremental watermark design
// exists to avoid.
func TestMigrateV11ClearsTokenTables(t *testing.T) {
	s, ids := seedV10(t, "measured")

	if _, err := s.db.Exec(
		`INSERT INTO job_tokens (job_slug, in_tokens, out_tokens, sessions, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"measured", 1, 2, 3, 4,
	); err != nil {
		t.Fatalf("seed job_tokens: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO job_token_files (job_slug, path, byte_offset) VALUES (?, ?, ?)`,
		"measured", "/p/a.jsonl", 99,
	); err != nil {
		t.Fatalf("seed job_token_files: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v10: %v", err)
	}

	rows, err := s.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("job_tokens = %+v, want empty — nothing is mapped forward", rows)
	}
	all, err := s.AllJobTokenOffsets()
	if err != nil {
		t.Fatalf("AllJobTokenOffsets: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("job_token_files = %v, want empty", all)
	}
	// and specifically NOT re-keyed onto the job that happened to hold the slug
	if _, err := s.GetJobTokens(ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJobTokens(%d) = %v, want ErrNotFound — the row was cleared, not moved", ids[0], err)
	}
}

// TestMigrateV11DropsDraftsTable: the table had zero non-test callers and zero
// rows (the real draft store is api/draft.go's in-memory map), so it goes.
func TestMigrateV11DropsDraftsTable(t *testing.T) {
	s, _ := seedV10(t, "drafted")

	// it really is there at the v10 shape, or this test would pass vacuously
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='drafts'`).Scan(&exists); err != nil {
		t.Fatalf("drafts table missing at the v10 shape: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v10: %v", err)
	}

	err := s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='drafts'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("drafts table still present after v11: err=%v", err)
	}
	// board_order_slug is the migration's own scaffolding and must be gone too
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='board_order_slug'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("board_order_slug scaffolding table survived the migration: err=%v", err)
	}
}

// TestMigrateV11AgainstRealDuplicateShapes runs the migration against the
// duplicate-slug shapes actually present on carl's machine — 5x `asdf`, 2x
// `helper-catalog`, 2x `sadfsa` — because those are the rows that make the
// mapping ambiguous at all, and a migration that only ever saw unique slugs
// would prove nothing about them.
func TestMigrateV11AgainstRealDuplicateShapes(t *testing.T) {
	s := openTest(t)

	// five generations of `asdf`, all archived — the worst case
	var asdf []int64
	for i := 0; i < 5; i++ {
		id, err := s.CreateJob(&Job{Slug: "asdf"})
		if err != nil {
			t.Fatalf("CreateJob asdf#%d: %v", i, err)
		}
		if _, err := s.ArchiveJob(id); err != nil {
			t.Fatalf("ArchiveJob asdf#%d: %v", i, err)
		}
		asdf = append(asdf, id)
	}
	// helper-catalog: one archived, one ACTIVE — the shape that broke a start
	hcOld, err := s.CreateJob(&Job{Slug: "helper-catalog"})
	if err != nil {
		t.Fatalf("CreateJob helper-catalog#1: %v", err)
	}
	if _, err := s.ArchiveJob(hcOld); err != nil {
		t.Fatalf("ArchiveJob helper-catalog#1: %v", err)
	}
	hcLive, err := s.CreateJob(&Job{Slug: "helper-catalog"})
	if err != nil {
		t.Fatalf("CreateJob helper-catalog#2: %v", err)
	}
	// sadfsa: two archived
	saOld, err := s.CreateJob(&Job{Slug: "sadfsa"})
	if err != nil {
		t.Fatalf("CreateJob sadfsa#1: %v", err)
	}
	if _, err := s.ArchiveJob(saOld); err != nil {
		t.Fatalf("ArchiveJob sadfsa#1: %v", err)
	}
	saNew, err := s.CreateJob(&Job{Slug: "sadfsa"})
	if err != nil {
		t.Fatalf("CreateJob sadfsa#2: %v", err)
	}
	if _, err := s.ArchiveJob(saNew); err != nil {
		t.Fatalf("ArchiveJob sadfsa#2: %v", err)
	}

	downgradeFreshDB(t, s, 10)

	// The live table carries a stale `helper-catalog` row in a board column
	// belonging to no ACTIVE job at the time it was written — reproduced here.
	// The column is `draft` rather than the `queued` the real rows sat in:
	// migrate() walks all the way to v20 now, and v20 DELETEs every queued
	// row, so seeding this fixture there would leave nothing to assert on.
	// The rekey under test does not read the column name.
	for i, slug := range []string{"asdf", "helper-catalog", "sadfsa"} {
		if _, err := s.db.Exec(
			`INSERT INTO board_order ("column", position, slug) VALUES (?, ?, ?)`,
			"draft", i, slug,
		); err != nil {
			t.Fatalf("seed board_order %q: %v", slug, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate against the real duplicate shapes: %v", err)
	}

	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(order["draft"]) != 3 {
		t.Fatalf("draft = %v, want all three mappable rows kept", order["draft"])
	}
	// asdf: no active job, no archived_name match ("asdf" archives to "asdf"
	// for the FIRST one only), so the rule that fires depends on which — but
	// whichever it is, it must be ONE of the five and it must be deterministic.
	if order["draft"][0] != asdf[0] {
		t.Errorf("asdf resolved to %d, want %d — the single archived_name match", order["draft"][0], asdf[0])
	}
	// helper-catalog: the ACTIVE job wins over the archived one, always
	if order["draft"][1] != hcLive {
		t.Errorf("helper-catalog resolved to %d, want the ACTIVE job %d", order["draft"][1], hcLive)
	}
	// sadfsa: no active job; the first archived took the bare name
	if order["draft"][2] != saOld {
		t.Errorf("sadfsa resolved to %d, want %d", order["draft"][2], saOld)
	}
}

// --- v19 -> v20: the queued status is retired ---

// TestSchemaV19ToV20RetiresQueued covers the whole of migrations[20] on a
// database shaped like a real v19 one: jobs.ready and the prereqs table both
// go, every board_order row for the vanished queued column goes with them,
// and — the part a "drop three things" migration gets wrong silently — the
// rows it must NOT touch survive intact.
//
// The surviving-rows half is the reason this asserts on two columns rather
// than one. `DELETE FROM board_order WHERE "column" = 'queued'` and
// `DELETE FROM board_order` differ by a clause, both leave the queued column
// empty, and only a fixture holding a second column can tell them apart.
func TestSchemaV19ToV20RetiresQueued(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "legacy-ready", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	other, err := s.CreateJob(&Job{Slug: "legacy-other", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob other: %v", err)
	}
	downgradeFreshDB(t, s, 19)

	// a v19-shaped database: a ready job, a prereq row, and board_order rows
	// in both the doomed column and a surviving one.
	if _, err := s.db.Exec(`UPDATE jobs SET ready = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("seed jobs.ready: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO prereqs (job_id, prereq_slug, gated) VALUES (?, ?, 1)`, id, "legacy-other",
	); err != nil {
		t.Fatalf("seed prereqs: %v", err)
	}
	// board_order keys on jobs.id from v12 onward, which a v19 fixture is
	// well past — so these rows carry ids, not slugs.
	for _, row := range []struct {
		column string
		jobID  int64
	}{
		{"queued", id},
		{"draft", other},
	} {
		if _, err := s.db.Exec(
			`INSERT INTO board_order ("column", position, job_id) VALUES (?, 0, ?)`,
			row.column, row.jobID,
		); err != nil {
			t.Fatalf("seed board_order %s/%d: %v", row.column, row.jobID, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v19: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after migrate, want %d", version, schemaVersion)
	}

	// jobs.ready is gone as a COLUMN — a select naming it must fail, which is
	// a stronger claim than "it reads as 0".
	if err := s.db.QueryRow(`SELECT ready FROM jobs WHERE id = ?`, id).Scan(new(int)); err == nil {
		t.Fatal("jobs.ready still selectable after v20")
	}
	// the prereqs table is gone
	if _, err := s.db.Exec(`SELECT 1 FROM prereqs`); err == nil {
		t.Fatal("prereqs table still present after v20")
	}
	// the job rows themselves survived the column drop
	got, err := s.GetJobBySlug("legacy-ready")
	if err != nil {
		t.Fatalf("GetJobBySlug after v20: %v", err)
	}
	if got.ID != id || got.Repo != "kraken" {
		t.Fatalf("job row mangled by v20: %+v", got)
	}

	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(order["queued"]) != 0 {
		t.Fatalf("queued board_order rows survived v20: %v", order["queued"])
	}
	if len(order["draft"]) != 1 || order["draft"][0] != other {
		t.Fatalf("draft board_order = %v, want [%d] — v20 must delete ONLY the queued rows", order["draft"], other)
	}
}
