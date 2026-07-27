package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// watchlist.go — schema v8's watchlist table (schema.go): the dream loop's
// running record of observations the nightly pass is tracking.
//
// The table is GLOBAL, not job-scoped — no job_id, no FK, no cascade (see
// schemaV8's doc comment). An observation outlives whichever job first
// surfaced it, which is the whole point: the same pattern recurs across many
// jobs, and BumpWatchSighting folds a repeat sighting into the EXISTING row
// rather than adding a second one, so a row's `sightings` count is the signal
// that separates a one-off from something worth fixing.
//
// Column ownership is split exactly the way questions.go splits its table,
// and for the same reason:
//
//   - AGENT-owned: observation, class, state, sightings, first_seen,
//     last_seen, evidence, suggested_fix, pr_url, updated_at — written by
//     AddWatchItem, UpdateWatchItem and BumpWatchSighting.
//   - CARL-owned: verdict, comment, commented_at — written ONLY by
//     SetWatchVerdict.
//
// Every agent-side write must therefore leave the carl-owned trio exactly as
// it found it, and vice versa. That invariant is this file's whole reason for
// having six narrow verbs instead of one blanket UPDATE.

// WatchItem is one row of the dream loop's watchlist: an observation the
// nightly pass is tracking but has not (or not yet) acted on. Deliberately
// tag-free like every other type in this package — the store layer never
// carries JSON tags, since the API layer owns its own DTOs and the wire shape
// that goes with them (internal/api).
type WatchItem struct {
	ID           int64
	Observation  string
	Class        string // feedback|correction|repetition|waste|prompting
	State        string // watching|shipped|declined
	Sightings    int64
	FirstSeen    int64  // unix millis
	LastSeen     int64  // unix millis
	Evidence     string // JSON, local-only — never committed
	SuggestedFix string // the agent's proposed correction, for carl to approve or push back on
	PRURL        string
	UpdatedAt    int64 // unix millis
	// Verdict/Comment/CommentedAt are CARL-OWNED: only SetWatchVerdict ever
	// writes them, exactly as questions.answer/answered_at are kraken-owned.
	Verdict     string // "" | fine | fix
	Comment     string
	CommentedAt string // RFC3339
}

// validWatchState / validWatchVerdict bound the two enums this package
// polices, same idiom (and the same API-surfaceable error shape) as
// questions.go's validImportance and threads.go's validThreadState.
//
// state is validated because it is the one column a QUERY branches on:
// ListWatchItems filters by it, so a typo'd state doesn't produce a visibly
// wrong row — it produces an INVISIBLE one, present in the table and absent
// from the only listing the board renders. That is precisely the failure mode
// this table cannot afford, since a watchlist nobody sees is a watchlist that
// silently stops working. verdict is validated for the cheaper reason that it
// is a two-value closed set carl's UI writes, and "" is a meaningful member of
// it (clear the judgement).
//
// class is deliberately NOT validated, and that is a decision rather than an
// oversight: nothing queries on it, it is descriptive metadata the sweep
// attaches for carl to read, and a new kind of observation is exactly the sort
// of thing the dream loop should be able to name without a schema change and a
// binary rebuild. The doc comment on WatchItem.Class lists today's classes as
// guidance, not as a constraint.
var validWatchState = map[string]bool{"watching": true, "shipped": true, "declined": true}

var validWatchVerdict = map[string]bool{"": true, "fine": true, "fix": true}

func validateWatchState(state string) error {
	if !validWatchState[state] {
		return fmt.Errorf("bad watch state %q: want watching, shipped, or declined", state)
	}
	return nil
}

func validateWatchVerdict(verdict string) error {
	if !validWatchVerdict[verdict] {
		return fmt.Errorf("bad watch verdict %q: want fine, fix, or empty to clear", verdict)
	}
	return nil
}

// watchItemColumns COALESCEs every scanned column defensively, same
// belt-and-suspenders reasoning as jobColumns/feedbackColumns/threadColumns:
// the schema already declares all of these NOT NULL with a same-typed
// default, but a NULL that sneaks in anyway (schema drift, a hand-edited row)
// must degrade to a blank field rather than error the whole query.
const watchItemColumns = `id, COALESCE(observation,''), COALESCE(class,''), COALESCE(state,''),
	COALESCE(sightings,0), COALESCE(first_seen,0), COALESCE(last_seen,0),
	COALESCE(evidence,''), COALESCE(suggested_fix,''), COALESCE(pr_url,''),
	COALESCE(updated_at,0), COALESCE(verdict,''), COALESCE(comment,''),
	COALESCE(commented_at,'')`

func scanWatchItem(row interface{ Scan(...any) error }) (*WatchItem, error) {
	var w WatchItem
	if err := row.Scan(&w.ID, &w.Observation, &w.Class, &w.State, &w.Sightings,
		&w.FirstSeen, &w.LastSeen, &w.Evidence, &w.SuggestedFix, &w.PRURL,
		&w.UpdatedAt, &w.Verdict, &w.Comment, &w.CommentedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

// AddWatchItem inserts one watchlist row and returns its assigned id,
// applying the defaults a caller normally leaves to this package: LastSeen/
// UpdatedAt stamped to now when zero (same convenience as AddFeedback's TS and
// AddThread's CreatedAt), Sightings defaulted to 1 (inserting a row IS the
// first sighting), and a blank State defaulted to "watching".
//
// FirstSeen defaults to LastSeen, NOT to now, and the difference is not
// cosmetic: a caller replaying history (an importer, a fixture, a sweep
// backfilling a night it missed) supplies LastSeen and leaves FirstSeen zero,
// and stamping "now" there would store a row first seen AFTER it was last
// seen. Every window/age calculation downstream reads that as a negative
// lifetime, and nothing errors. Defaulting to LastSeen makes the degenerate
// case "first and last sighting are the same moment", which is exactly what a
// one-sighting row means. Only when BOTH are zero does now apply, via
// LastSeen.
//
// Verdict/Comment/CommentedAt are deliberately NOT caller-settable here —
// they're carl's, written later only via SetWatchVerdict — so a fresh row
// always starts unjudged, exactly as AddFeedback starts a row unaddressed.
func (s *Store) AddWatchItem(w WatchItem) (int64, error) {
	now := time.Now().UnixMilli()
	if w.LastSeen == 0 {
		w.LastSeen = now
	}
	if w.FirstSeen == 0 {
		w.FirstSeen = w.LastSeen
	}
	if w.UpdatedAt == 0 {
		w.UpdatedAt = now
	}
	if w.Sightings == 0 {
		w.Sightings = 1
	}
	if w.State == "" {
		w.State = "watching"
	}
	if err := validateWatchState(w.State); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO watchlist (observation, class, state, sightings, first_seen, last_seen,
			evidence, suggested_fix, pr_url, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Observation, w.Class, w.State, w.Sightings, w.FirstSeen, w.LastSeen,
		w.Evidence, w.SuggestedFix, w.PRURL, w.UpdatedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("add watch item: %w", err)
	}
	return res.LastInsertId()
}

// GetWatchItem looks up one watchlist row by id, or ErrNotFound when no row
// matches — same pointer-returning, sql.ErrNoRows → ErrNotFound shape as
// every other single-row getter in this package.
func (s *Store) GetWatchItem(id int64) (*WatchItem, error) {
	row := s.db.QueryRow(`SELECT `+watchItemColumns+` FROM watchlist WHERE id = ?`, id)
	w, err := scanWatchItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get watch item %d: %w", id, err)
	}
	return w, nil
}

// FindWatchItem returns the row already tracking this observation, or
// ErrNotFound when none does. It is the lookup that makes BumpWatchSighting
// reachable: without it a caller has no way to discover that an observation
// is a REPEAT, so it inserts a near-duplicate row instead of folding, and
// `sightings` — the one signal separating a one-off from a recurring pattern,
// and so the whole basis for deciding what gets fixed versus merely watched —
// sits at 1 across N rows that are all the same thing. Nothing errors when
// that happens; the feature just quietly stops working. The intended call
// order is Find → (hit) BumpWatchSighting / (miss) AddWatchItem.
//
// MATCHING IS EXACT, after normalising both sides identically: surrounding
// whitespace stripped (space, tab, newline, carriage return — char(32,9,10,13),
// since bare TRIM() strips spaces only and a trailing newline on an
// LLM-generated string is entirely ordinary), then ASCII-case-folded.
// Case-folding is not cosmetic either — the caller is an LLM that will write
// "Hermes agents re-read the contract" one night and "hermes agents re-read
// the contract" the next, and those must land on one row.
//
// The rule lives ONLY in the SQL expression below — never half in Go and half
// in SQL — and schemaV8's watchlist_observation_key index is that same
// expression, which is what keeps this a seek. Drift between the two is
// silent (SQLite just scans and still answers correctly), so
// watchlist_test.go asserts on EXPLAIN QUERY PLAN rather than trusting the
// pair to stay in step.
//
// Two deliberate limits, both documented rather than papered over:
//
//   - SQLite's LOWER() folds ASCII only, so observations differing only in
//     the case of a non-ASCII letter will NOT match. Every class of
//     observation this table holds is English prose about code, so the
//     practical exposure is nil, and the alternative (a Go-side unicode fold)
//     costs the index and the single-expression property above.
//   - Internal whitespace is NOT collapsed and nothing fuzzier is attempted.
//     Deciding that two differently-worded observations are the same thing is
//     a judgment call, and it belongs to the agent doing the sweep — which can
//     read both — not to a SQL predicate that would silently fold genuinely
//     distinct observations together and lose one of them.
//
// State is deliberately NOT filtered: a `declined` or already-judged row
// matches like any other, because finding it is precisely how the caller
// learns this observation has been ruled on and must not be re-proposed.
// Hiding those rows would make a declined observation come back as a fresh
// one every single night.
//
// When several rows collide (duplicates that predate this lookup, or a caller
// that skipped it), the MOST RECENTLY SEEN wins — same last_seen DESC, id
// DESC ordering as ListWatchItems — so a bump lands on the live row rather
// than resurrecting the stalest copy.
func (s *Store) FindWatchItem(observation string) (*WatchItem, error) {
	row := s.db.QueryRow(
		`SELECT `+watchItemColumns+` FROM watchlist
			WHERE LOWER(TRIM(observation, char(32,9,10,13))) = LOWER(TRIM(?, char(32,9,10,13)))
			ORDER BY last_seen DESC, id DESC LIMIT 1`,
		observation,
	)
	w, err := scanWatchItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find watch item %q: %w", observation, err)
	}
	return w, nil
}

// ListWatchItems returns watchlist rows in last_seen DESC order (most
// recently observed first — the order the board reads them in), filtered to
// one state; state == "" returns EVERY row regardless of state. The
// (state, last_seen) index added by schemaV8 covers both halves of that query
// in the order it uses them.
func (s *Store) ListWatchItems(state string) ([]WatchItem, error) {
	query := `SELECT ` + watchItemColumns + ` FROM watchlist`
	args := []any{}
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	// id DESC breaks last_seen ties deterministically (newest row first),
	// so two rows stamped in the same millisecond don't reorder between
	// otherwise identical reads.
	query += ` ORDER BY last_seen DESC, id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list watch items (state %q): %w", state, err)
	}
	defer rows.Close()
	var out []WatchItem
	for rows.Next() {
		w, err := scanWatchItem(rows)
		if err != nil {
			return nil, fmt.Errorf("list watch items (state %q): %w", state, err)
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// UpdateWatchItem is the AGENT's write: it updates the agent-owned columns of
// an existing row and PRESERVES the carl-owned trio (verdict, comment,
// commented_at) completely, whatever the passed WatchItem happens to carry in
// those three fields.
//
// This is the single most important behaviour in this file, and it is the
// same rule questions.go's upsertQuestionZeusColumns enforces for
// answer/answered_at: the two sides write the same row at unpredictable
// times, and the agent's write is a full-row restatement of what it currently
// believes. If that restatement carried the carl-owned columns too, then any
// nightly pass that re-stated an observation carl had just commented on would
// silently erase his comment — the row would still look fine, so nothing
// would ever surface the loss. Naming the agent-owned columns explicitly in
// the SET clause makes that impossible by construction rather than by the
// caller remembering to re-read first.
//
// THE UPDATE IS PARTIAL: a zero-valued field means "leave this column alone",
// never "write a zero here". Only the fields the caller actually populated are
// overwritten.
//
// That is not a convenience, it is the difference between working and silently
// broken. The caller is an LLM assembling a JSON object, and it will restate
// only the fields it means to change — which under verbatim-write semantics
// stores state=” and sightings=0. Neither errors, neither removes the row
// from the table, and both remove it from ListWatchItems("watching"), the one
// query the board renders: the observation stops existing as far as the
// feature is concerned while every test and every read of the raw table still
// looks fine. Sightings would additionally reset to 0, destroying the recurrence
// count that is the whole basis for deciding what gets fixed.
//
// The "leave alone" test is done in SQL (COALESCE(NULLIF(?, <zero>), col)) so
// this stays ONE statement: a read-modify-write in Go would reintroduce the
// lost-update race that BumpWatchSighting's SQL-side increment exists to
// avoid.
//
// The cost, stated plainly: clearing a text column back to empty is not
// expressible through this verb. Evidence has its own path
// (BumpWatchSighting replaces it outright), and blanking suggested_fix/pr_url
// is not something the sweep does — whereas accidentally blanking them is
// something it would do constantly. If a clearing verb is ever genuinely
// needed, it should be its own narrow method, not a loosening of this one.
//
// UpdatedAt is the deliberate exception, always written (stamped to now when
// zero): it records that the agent touched the row at all, which is true even
// on an update that changed one field.
//
// ErrNotFound when id doesn't exist.
func (s *Store) UpdateWatchItem(id int64, w WatchItem) error {
	if w.UpdatedAt == 0 {
		w.UpdatedAt = time.Now().UnixMilli()
	}
	// An empty State means "leave alone" (above), so only a populated one is
	// checked — otherwise every partial update would be rejected.
	if w.State != "" {
		if err := validateWatchState(w.State); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(
		`UPDATE watchlist SET
			observation   = COALESCE(NULLIF(?, ''), observation),
			class         = COALESCE(NULLIF(?, ''), class),
			state         = COALESCE(NULLIF(?, ''), state),
			sightings     = COALESCE(NULLIF(?, 0), sightings),
			first_seen    = COALESCE(NULLIF(?, 0), first_seen),
			last_seen     = COALESCE(NULLIF(?, 0), last_seen),
			evidence      = COALESCE(NULLIF(?, ''), evidence),
			suggested_fix = COALESCE(NULLIF(?, ''), suggested_fix),
			pr_url        = COALESCE(NULLIF(?, ''), pr_url),
			updated_at    = ?
			WHERE id = ?`,
		w.Observation, w.Class, w.State, w.Sightings, w.FirstSeen, w.LastSeen,
		w.Evidence, w.SuggestedFix, w.PRURL, w.UpdatedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update watch item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update watch item %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("update watch item %d: %w", id, ErrNotFound)
	}
	return nil
}

// BumpWatchSighting folds a REPEAT observation into the row that already
// tracks it: sightings += 1, last_seen and updated_at set to lastSeen, and
// evidence replaced wholesale with the fresh sample.
//
// The increment is done in SQL (`sightings = sightings + 1`) rather than
// read-modify-write in Go so the count can't be lost to a concurrent write —
// kraken serve answers HTTP concurrently, and a read-then-write pair around
// this column would drop a sighting whenever two passes overlapped.
//
// Evidence is REPLACED, not appended: it is a local-only JSON sample of the
// most recent occurrence (never committed), and the value carried by the row
// is "here is what this looks like", not an ever-growing log. first_seen is
// untouched — that is the whole point of a first-seen column — and so is the
// carl-owned trio. lastSeen == 0 stamps now, matching this file's other
// timestamp conveniences. ErrNotFound when id doesn't exist.
func (s *Store) BumpWatchSighting(id int64, lastSeen int64, evidence string) error {
	if lastSeen == 0 {
		lastSeen = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`UPDATE watchlist SET sightings = sightings + 1, last_seen = ?, updated_at = ?,
			evidence = ? WHERE id = ?`,
		lastSeen, lastSeen, evidence, id,
	)
	if err != nil {
		return fmt.Errorf("bump watch item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("bump watch item %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("bump watch item %d: %w", id, ErrNotFound)
	}
	return nil
}

// SetWatchVerdict is CARL's write, and the exact mirror of UpdateWatchItem: it
// touches ONLY verdict/comment/commented_at and leaves every agent-owned
// column alone, so judging a row can never roll back an observation the
// nightly pass has since refined. Same split, same reasoning, as
// questions.go's AnswerQuestion against upsertQuestionZeusColumns.
//
// An empty verdict CLEARS the judgement (reopening the row) rather than being
// rejected — the three columns are written unconditionally, so passing
// ("", "", "") is the documented way to undo a verdict. updated_at is
// deliberately NOT bumped here: it tracks the agent's own last write, and a
// verdict is not one. ErrNotFound when id doesn't exist.
func (s *Store) SetWatchVerdict(id int64, verdict, comment, commentedAt string) error {
	if err := validateWatchVerdict(verdict); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE watchlist SET verdict = ?, comment = ?, commented_at = ? WHERE id = ?`,
		verdict, comment, commentedAt, id,
	)
	if err != nil {
		return fmt.Errorf("set verdict on watch item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set verdict on watch item %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set verdict on watch item %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteWatchItem permanently removes a watchlist row. ErrNotFound when id
// doesn't exist — same contract as DeleteFeedback, including on a repeat
// delete of an already-deleted id.
func (s *Store) DeleteWatchItem(id int64) error {
	res, err := s.db.Exec(`DELETE FROM watchlist WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete watch item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete watch item %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("delete watch item %d: %w", id, ErrNotFound)
	}
	return nil
}
