package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// watch.go — schema v29's watch_items table (schema.go): the dream loop's
// running record of observations the nightly pass is tracking.
//
// THE SHAPE CHANGE, stated once. v16 moved this record INTO the knowledge
// graph — the observation became a live kg_edges fact and this table shrank to
// a thin overlay keyed by edge id — so that the graph's own FTS and vector
// retrieval could reach the single most useful sentence the dream loop
// produces. v29 moves it back OUT, and the reason is the mental model rather
// than anything wrong with that argument: the graph is now a COMPILATION of
// what carl says he knows, and everything agent-observed has been swept out of
// it. A watch row riding an edge would be the one agent observation left
// inside carl's mental model, and every read of the graph — spawn injection,
// mid-conversation recall, the graph search verb — would surface it as
// something carl knows. The retrieval the overlay bought is the exact thing
// that had to stop. (v32 then removed the knowledge graph altogether; this
// table never depended on it after v29.)
//
// So the observation is a column again, and the table is standalone: no edge,
// no episode, no join. Two things survive the move deliberately.
//
// ROWS ARE EXPIRED, NEVER DELETED. v9's flat table deleted; the overlay era
// deleted the overlay and left the fact standing. Both lost the record. An
// `expired_at` stamp — the kg_edges spelling, kept because it was the right
// idea — means taking an observation off the board is not a claim it was never
// true, and a row carl retired last month is still readable by id.
//
// COLUMN OWNERSHIP, unchanged since v9 and still this file's reason for having
// narrow verbs instead of one blanket UPDATE:
//
//   - AGENT-owned: observation, src, dst, predicate, class, state, sightings,
//     first_seen, last_seen, evidence, suggested_fix, pr_url, updated_at —
//     written by AddWatchItem and UpdateWatchItem.
//   - CARL-owned: verdict, comment, commented_at — written ONLY by
//     SetWatchVerdict.
//
// Every agent-side write must leave the carl-owned trio exactly as it found
// it, and vice versa. That is the single most important behaviour in this
// file: the two sides write the same row at unpredictable times, the agent's
// write is a full-row restatement of what it currently believes, and a
// restatement that carried carl's columns would silently erase a comment he
// had just written — with the row still looking fine afterwards.

// WatchItem is one watched observation: the sentence, the endpoints it was
// stated between, the dream loop's bookkeeping, and carl's judgement.
//
// Deliberately tag-free like every other type in this package — the API layer
// owns its own DTO and the wire shape that goes with it (internal/api).
//
// ID is the identity end to end: it is what /api/watchlist serves as `id`,
// what the CLI passes back, and the table's primary key. Src/Dst/Predicate are
// plain columns now rather than joined labels — they carried across from the
// v16 overlay's endpoints, and nothing derives them any more.
type WatchItem struct {
	ID                             int64
	Observation                    string
	Src, Dst, Predicate            string
	Class                          string // feedback|correction|repetition|waste|prompting
	State                          string // watching|shipped|declined
	Sightings, FirstSeen, LastSeen int64  // unix millis for the two stamps
	Evidence                       string // JSON, local-only — never committed
	SuggestedFix, PRURL            string
	UpdatedAt                      int64 // unix millis: the AGENT's last write
	Verdict, Comment, CommentedAt  string
	CreatedAt, ExpiredAt           int64 // unix millis; ExpiredAt 0 = live
}

// oneLine collapses ALL whitespace runs — newlines included — to single spaces
// and trims the ends.
//
// Its one remaining caller is migrateV16Watchlist (schema.go), which folds the
// old v9 flat table onto graph edges with it and must keep matching what
// normalizeGraphAdd normalized the stored side with. It is deliberately NOT
// applied by the verbs below any more: v29's fold is BYTE-IDENTICAL on the
// observation, so there is exactly one string a caller can pass to hit an
// existing row, rather than a normalization rule a caller has to reproduce.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

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

// watchItemColumns is every read in this file. Every column is COALESCEd
// defensively, same belt-and-suspenders reasoning as jobColumns/feedbackColumns/
// threadColumns: the schema declares all of these NOT NULL with a same-typed
// default, but a NULL that sneaks in anyway (schema drift, a hand-edited row)
// must degrade to a blank field rather than error the whole query.
const watchItemColumns = `SELECT id, COALESCE(observation,''), COALESCE(src,''), COALESCE(dst,''),
	COALESCE(predicate,''), COALESCE(class,''), COALESCE(state,''), COALESCE(sightings,0),
	COALESCE(first_seen,0), COALESCE(last_seen,0), COALESCE(evidence,''),
	COALESCE(suggested_fix,''), COALESCE(pr_url,''), COALESCE(updated_at,0),
	COALESCE(verdict,''), COALESCE(comment,''), COALESCE(commented_at,''),
	COALESCE(created_at,0), COALESCE(expired_at,0)
	FROM watch_items`

func scanWatchItem(row interface{ Scan(...any) error }) (WatchItem, error) {
	var w WatchItem
	err := row.Scan(&w.ID, &w.Observation, &w.Src, &w.Dst, &w.Predicate,
		&w.Class, &w.State, &w.Sightings, &w.FirstSeen, &w.LastSeen, &w.Evidence,
		&w.SuggestedFix, &w.PRURL, &w.UpdatedAt, &w.Verdict, &w.Comment, &w.CommentedAt,
		&w.CreatedAt, &w.ExpiredAt)
	return w, err
}

// AddWatchItem records an observation, FOLDING it onto the live row that
// already carries the same one rather than storing it twice.
//
// The fold is the whole reason this is one verb instead of a find and an
// insert. `sightings` is the one signal separating a one-off from a recurring
// pattern, and so the entire basis for deciding what gets fixed versus merely
// watched — and the caller is an LLM that re-states the same observation night
// after night. Without the fold nothing errors; the count just sits at 1
// across N rows that are all the same thing, and the feature quietly stops
// working.
//
// MATCHING IS BYTE-IDENTICAL on Observation, against LIVE rows only. That is a
// deliberate narrowing of the v16 era's normalize-then-case-fold rule: the
// matching side is now a plain column this package owns end to end, so there
// is exactly one string that hits an existing row and no normalization for a
// caller to reproduce or for a migration to disagree with. An expired row is
// never folded onto — retiring an observation and then seeing it again is a
// NEW observation, and burying it inside a row carl has closed would hide it.
//
// State is deliberately NOT part of the match: a `declined` or already-judged
// row folds like any other, because finding it is precisely how the caller
// learns this observation has been ruled on and must not be re-proposed.
// Hiding those would make a declined observation come back as fresh every
// single night.
//
// ON A FOLD the row's sightings go up by one, last_seen and updated_at move to
// now, and each of the agent's descriptive fields is overwritten only when the
// caller supplied a non-empty one — a partial restatement leaves the rest
// alone, UpdateWatchItem's rule and for its reason. first_seen is untouched,
// which is the whole point of a first-seen column, and so is the carl-owned
// trio. Newest live row wins when several match, so a fold lands on the live
// one rather than the stalest copy.
//
// ON A CREATE the counters are this package's, not the caller's: sightings
// starts at 1, first_seen and last_seen are both now (a first sighting is by
// definition its own last), expired_at is 0, and a blank state starts the row
// at `watching` — the one place that default applies. A caller replaying history
// sets those afterwards through UpdateWatchItem, which takes them as ordinary
// agent fields. Verdict/Comment/CommentedAt are not settable here at all —
// they are carl's, written later only via SetWatchVerdict — so a fresh row
// always starts unjudged, exactly as AddFeedback starts a row unaddressed.
func (s *Store) AddWatchItem(w WatchItem) (int64, bool, error) {
	// An empty state skips validation and means "leave it alone" on a fold;
	// it is defaulted only on the CREATE path below. Defaulting it here made
	// every fold that sent no state overwrite a shipped or declined row to
	// watching — the one descriptive field the "non-empty overwrites" rule
	// did not actually govern, and a demotion nothing surfaced as one.
	if w.State != "" {
		if err := validateWatchState(w.State); err != nil {
			return 0, false, err
		}
	}
	now := time.Now().UnixMilli()

	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM watch_items WHERE expired_at = 0 AND observation = ?
			ORDER BY last_seen DESC, id DESC LIMIT 1`, w.Observation).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// fall through to the insert
	case err != nil:
		return 0, false, fmt.Errorf("add watch item: %w", err)
	default:
		// The increment is done in SQL rather than read-modify-write in Go so
		// the count can't be lost to a concurrent write — kraken serve answers
		// HTTP concurrently, and a read-then-write pair around this column
		// would drop a sighting whenever two passes overlapped.
		//
		// COALESCE(NULLIF(?, ''), col) is the same "zero means leave alone"
		// test UpdateWatchItem uses, in SQL for the same reason: it keeps this
		// one statement.
		if _, uerr := s.db.Exec(
			`UPDATE watch_items SET
				sightings     = sightings + 1,
				last_seen     = ?,
				updated_at    = ?,
				class         = COALESCE(NULLIF(?, ''), class),
				state         = COALESCE(NULLIF(?, ''), state),
				evidence      = COALESCE(NULLIF(?, ''), evidence),
				suggested_fix = COALESCE(NULLIF(?, ''), suggested_fix),
				pr_url        = COALESCE(NULLIF(?, ''), pr_url)
				WHERE id = ?`,
			now, now, w.Class, w.State, w.Evidence, w.SuggestedFix, w.PRURL, id,
		); uerr != nil {
			return 0, false, fmt.Errorf("fold watch item %d: %w", id, uerr)
		}
		return id, true, nil
	}

	if w.State == "" {
		w.State = "watching" // a fresh row starts watched; only a create defaults it
	}
	res, err := s.db.Exec(
		`INSERT INTO watch_items (observation, src, dst, predicate, class, state,
			sightings, first_seen, last_seen, evidence, suggested_fix, pr_url,
			updated_at, created_at, expired_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, 0)`,
		w.Observation, w.Src, w.Dst, w.Predicate, w.Class, w.State,
		now, now, w.Evidence, w.SuggestedFix, w.PRURL, now, now,
	)
	if err != nil {
		return 0, false, fmt.Errorf("add watch item: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("add watch item: %w", err)
	}
	return id, false, nil
}

// ListWatchItems returns LIVE observations in last_seen DESC order (most
// recently observed first — the order the board reads them in), filtered to one
// state; state == "" returns every live row regardless of state. The
// watch_items(expired_at, state, last_seen) index added by schemaV29 covers the
// query in the order it uses the columns.
//
// A non-empty state is VALIDATED rather than passed through: an unknown state
// matches nothing, so the difference between "typo" and "genuinely empty board"
// would otherwise be invisible at the only listing the DREAM view renders.
//
// Expired rows are absent, not merely re-ordered. They remain readable by id
// (GetWatchItem) — closing an observation retires it from the board without
// erasing it.
func (s *Store) ListWatchItems(state string) ([]WatchItem, error) {
	query := watchItemColumns + ` WHERE expired_at = 0`
	args := []any{}
	if state != "" {
		if err := validateWatchState(state); err != nil {
			return nil, err
		}
		query += ` AND state = ?`
		args = append(args, state)
	}
	// id DESC breaks last_seen ties deterministically (newest row first), so
	// two rows stamped in the same millisecond don't reorder between otherwise
	// identical reads.
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
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWatchItem looks up one observation by id, or ErrNotFound when no row
// carries it.
//
// An EXPIRED row still reads, unlike in every listing: the id is a durable
// handle carl (or a notification, or a CLI invocation) may still be holding
// after the row came off the board, and answering "no such thing" for a row
// that is right there would make retirement look like deletion.
func (s *Store) GetWatchItem(id int64) (WatchItem, error) {
	w, err := scanWatchItem(s.db.QueryRow(watchItemColumns+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WatchItem{}, fmt.Errorf("get watch item %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return WatchItem{}, fmt.Errorf("get watch item %d: %w", id, err)
	}
	return w, nil
}

// UpdateWatchItem is the AGENT's write: it updates the agent-owned columns and
// PRESERVES the carl-owned trio (verdict, comment, commented_at) completely,
// whatever the passed WatchItem happens to carry in those three fields.
//
// This is the rule this file's header calls its most important behaviour, and
// it is the same one questions.go's upsertQuestionZeusColumns enforces for
// answer/answered_at. Naming the agent-owned columns explicitly in the SET
// clause makes the erasure impossible by construction rather than by the
// caller remembering to re-read first.
//
// THE UPDATE IS PARTIAL: a zero-valued field means "leave this column alone",
// never "write a zero here". Only the fields the caller actually populated are
// overwritten.
//
// That is not a convenience, it is the difference between working and silently
// broken. The caller is an LLM assembling a JSON object, and it will restate
// only the fields it means to change — which under verbatim-write semantics
// stores state=” and sightings=0. Neither errors, neither removes the row,
// and both remove it from ListWatchItems("watching"), the one query the board
// renders: the observation stops existing as far as the feature is concerned
// while every read of the raw table still looks fine. Sightings would
// additionally reset to 0, destroying the recurrence count that is the whole
// basis for deciding what gets fixed.
//
// The "leave alone" test is done in SQL (COALESCE(NULLIF(?, <zero>), col)) so
// this stays ONE statement: a read-modify-write in Go would reintroduce the
// lost-update race AddWatchItem's SQL-side increment exists to avoid.
//
// The cost, stated plainly: clearing a text column back to empty is not
// expressible through this verb. Blanking suggested_fix/pr_url is not
// something the sweep does — whereas accidentally blanking them is something
// it would do constantly. If a clearing verb is ever genuinely needed, it
// should be its own narrow method, not a loosening of this one.
//
// THE OBSERVATION ITSELF IS NOT REACHABLE FROM HERE, and that is structural
// rather than an omission: it is the fold key, so rewording it in place would
// silently re-partition which future sightings land where. Rewording is expire
// and re-add.
//
// UpdatedAt is the deliberate exception to "zero means leave alone", always
// written (stamped to now when zero): it records that the agent touched the
// row at all, which is true even on an update that changed one field.
//
// ErrNotFound when no row carries that id. An expired row updates like any
// other — the pass may still be recording what it learned about a retired
// observation.
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
		`UPDATE watch_items SET
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
		w.Class, w.State, w.Sightings, w.FirstSeen, w.LastSeen,
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

// SetWatchVerdict is CARL's write, and the exact mirror of UpdateWatchItem: it
// touches ONLY verdict/comment/commented_at and leaves every agent-owned
// column alone, so judging an observation can never roll back what the nightly
// pass has since refined. Same split, same reasoning, as questions.go's
// AnswerQuestion against upsertQuestionZeusColumns.
//
// commented_at is stamped HERE — RFC3339 in UTC — rather than passed in,
// because it records when carl judged it and the server is the only party that
// knows. Passing an empty verdict AND an empty comment CLEARS the judgement
// (reopening the row), and clears the stamp with them: a timestamp left
// standing beside no verdict would read as a judgement whose text had gone
// missing.
//
// updated_at is deliberately NOT bumped: it tracks the AGENT's own last write,
// and a verdict is not one. ErrNotFound when no row carries that id.
func (s *Store) SetWatchVerdict(id int64, verdict, comment string) error {
	if err := validateWatchVerdict(verdict); err != nil {
		return err
	}
	commentedAt := time.Now().UTC().Format(time.RFC3339)
	if verdict == "" && comment == "" {
		commentedAt = ""
	}
	res, err := s.db.Exec(
		`UPDATE watch_items SET verdict = ?, comment = ?, commented_at = ? WHERE id = ?`,
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

// ExpireWatchItem takes an observation off the board by CLOSING its row.
//
// It replaces the delete both earlier designs performed, and the difference is
// the point: retiring an observation is not a claim that the observation was
// never true, so the row survives, keeps its id, and stays readable by
// GetWatchItem — the same true-then, closed-now shape kg_edges' expired_at
// expresses, kept when the table left the graph.
//
// Already-expired is a SUCCESS that overwrites nothing, InvalidateMMItem's
// contract and for its reason: the first close is when it actually happened,
// and a second call would move that moment to whenever a client double-clicked.
// An unknown id is ErrNotFound — the caller is looking at a row, so "that row
// was never here" is news (its view is stale) rather than the same outcome by
// another route.
//
// at == 0 stamps now, this package's timestamp convention; a zero left in the
// column would leave the row reading as live and make the close a silent no-op.
func (s *Store) ExpireWatchItem(id, at int64) error {
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(`UPDATE watch_items SET expired_at = ? WHERE id = ? AND expired_at = 0`, at, id)
	if err != nil {
		return fmt.Errorf("expire watch item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("expire watch item %d: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	// Zero rows means either "no such row" or "already expired", and the two
	// answer differently — so the existence probe runs only on that miss.
	var exists int64
	err = s.db.QueryRow(`SELECT id FROM watch_items WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("expire watch item %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("expire watch item %d: %w", id, err)
	}
	return nil
}
