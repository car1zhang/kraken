package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// watch.go — schema v16's kg_watch table (schema.go): the dream loop's
// running record of observations the nightly pass is tracking, now carried as
// a thin OVERLAY on a knowledge-graph edge rather than as a flat row of its
// own.
//
// What moved, and why it matters to every function below: the observation
// itself is no longer a column here. It is `kg_edges.fact` — a live graph
// fact between two entities — and kg_watch holds only what the graph has no
// verbs for: the dream-loop workflow (class, state, sightings, first/last
// seen, evidence, suggested_fix, pr_url) and carl's judgement trio. That
// single change is what makes a watch item searchable by FTS/vector retrieval
// and injectable into a future cell's graphContext with no extra plumbing:
// the fact IS the observation, and the graph already indexes facts.
//
// Two consequences the verbs here are shaped by:
//
//   - The overlay RIDES the edge. Every read joins live kg_edges
//     (expired_at = 0), so invalidating a watched fact removes it from the
//     DREAM view by construction — there is no second place to also remember
//     to delete from, and no way for the board to render a fact the graph has
//     unlearned.
//   - Edges stay insert-only. Nothing in this file UPDATEs kg_edges at all;
//     rewording an observation is remove-and-re-add, not a mutation (the
//     graph's whole bi-temporal story depends on a stored fact's sentence
//     never changing under it).
//
// Column ownership survives the move unchanged, and it is still this file's
// reason for having narrow verbs instead of one blanket UPDATE:
//
//   - AGENT-owned: class, state, sightings, first_seen, last_seen, evidence,
//     suggested_fix, pr_url, updated_at — written by WatchEdge,
//     UpdateWatchItem and BumpWatchSighting.
//   - CARL-owned: verdict, comment, commented_at — written ONLY by
//     SetWatchVerdict.
//
// Every agent-side write must leave the carl-owned trio exactly as it found
// it, and vice versa.

// WatchItem is one watched fact: the edge's sentence plus the overlay the
// dream loop keeps about it. Deliberately tag-free like every other type in
// this package — the store layer never carries JSON tags, since the API layer
// owns its own DTOs and the wire shape that goes with them (internal/api).
//
// EdgeID is the identity end to end: it is what /api/watchlist serves as
// `id`, what the CLI passes back, and the kg_watch primary key. Src/Dst/
// Predicate are joined context (the endpoints the fact connects), not
// overlay columns — no verb here writes them.
type WatchItem struct {
	EdgeID      int64
	Observation string // = kg_edges.fact
	Src, Dst    string // endpoint canonical names (joined from kg_nodes)
	Predicate   string

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

// oneLine collapses ALL whitespace runs — newlines included — to single
// spaces and trims the ends. It is the exact normalization
// internal/api/graph.go's normalizeGraphAdd applies to a fact before storing
// it, which is the only reason matching a caller's observation against
// LOWER(kg_edges.fact) works at all: the stored side was normalized on the
// way in, so normalizing the query side the SAME way is what makes the two
// comparable.
//
// Do not change this rule casually. migrateV16Watchlist (schema.go) folds the
// old flat watchlist onto edges with it, so a divergence would mean the
// migration grouped rows the live verbs would then fail to find.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// validWatchState / validWatchVerdict bound the two enums this package
// polices, same idiom (and the same API-surfaceable error shape) as
// questions.go's validImportance and threads.go's validThreadState.
//
// state is validated because it is the one column a QUERY branches on:
// ListWatchItems filters by it, so a typo'd state doesn't produce a visibly
// wrong row — it produces an INVISIBLE one, present in the table and absent
// from the only listing the board renders. That is precisely the failure mode
// this overlay cannot afford, since a watchlist nobody sees is a watchlist
// that silently stops working. verdict is validated for the cheaper reason
// that it is a two-value closed set carl's UI writes, and "" is a meaningful
// member of it (clear the judgement).
//
// class is deliberately NOT validated, and that is a decision rather than an
// oversight: nothing queries on it, it is descriptive metadata the sweep
// attaches for carl to read, and a new kind of observation is exactly the
// sort of thing the dream loop should be able to name without a schema change
// and a binary rebuild. The doc comment on WatchItem.Class lists today's
// classes as guidance, not as a constraint.
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

// watchItemSelect is every read in this file: the overlay INNER JOINed to its
// LIVE edge, with both endpoint names LEFT JOINed on.
//
// The two join kinds are different on purpose. The edge join is INNER and
// carries `expired_at = 0`, so an overlay whose fact has been invalidated (or
// whose edge is somehow gone) drops out of every read — the "the overlay
// rides the edge" rule in this file's header, expressed once here rather than
// re-stated per query. The NODE joins are LEFT because kg_* carries no
// foreign keys by design (schemaV15): a dangling src_id must cost the item
// its endpoint LABEL, never its whole row, since the fact sentence is the
// payload and it is right there in the same row.
//
// Every scanned column is COALESCEd defensively, same belt-and-suspenders
// reasoning as jobColumns/feedbackColumns/threadColumns: the schema declares
// all of these NOT NULL with a same-typed default, but a NULL that sneaks in
// anyway (schema drift, a hand-edited row) must degrade to a blank field
// rather than error the whole query.
const watchItemSelect = `SELECT w.edge_id, COALESCE(e.fact,''), COALESCE(sn.name,''), COALESCE(dn.name,''),
	COALESCE(e.predicate,''), COALESCE(w.class,''), COALESCE(w.state,''), COALESCE(w.sightings,0),
	COALESCE(w.first_seen,0), COALESCE(w.last_seen,0), COALESCE(w.evidence,''),
	COALESCE(w.suggested_fix,''), COALESCE(w.pr_url,''), COALESCE(w.updated_at,0),
	COALESCE(w.verdict,''), COALESCE(w.comment,''), COALESCE(w.commented_at,'')
	FROM kg_watch w
	JOIN kg_edges e ON e.id = w.edge_id AND e.expired_at = 0
	LEFT JOIN kg_nodes sn ON sn.id = e.src_id
	LEFT JOIN kg_nodes dn ON dn.id = e.dst_id`

func scanWatchItem(row interface{ Scan(...any) error }) (*WatchItem, error) {
	var w WatchItem
	if err := row.Scan(&w.EdgeID, &w.Observation, &w.Src, &w.Dst, &w.Predicate,
		&w.Class, &w.State, &w.Sightings, &w.FirstSeen, &w.LastSeen, &w.Evidence,
		&w.SuggestedFix, &w.PRURL, &w.UpdatedAt, &w.Verdict, &w.Comment, &w.CommentedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWatchItems returns watched facts in last_seen DESC order (most recently
// observed first — the order the board reads them in), filtered to one state;
// state == "" returns EVERY overlay row whose edge is live, regardless of
// state. The kg_watch(state, last_seen) index added by schemaV16 covers both
// halves of the filtered query in the order it uses them.
//
// A non-empty state is VALIDATED rather than passed through: an unknown state
// matches nothing, so the difference between "typo" and "genuinely empty
// board" would otherwise be invisible at the only listing the DREAM view
// renders.
//
// Items whose edge has been invalidated are absent, not merely re-ordered —
// see watchItemSelect. That is the deliberate coupling: retiring a watched
// fact in the graph viewer retires it from the dream loop too.
func (s *Store) ListWatchItems(state string) ([]WatchItem, error) {
	query := watchItemSelect
	args := []any{}
	if state != "" {
		if err := validateWatchState(state); err != nil {
			return nil, err
		}
		query += ` WHERE w.state = ?`
		args = append(args, state)
	}
	// edge_id DESC breaks last_seen ties deterministically (newest overlay
	// first), so two rows stamped in the same millisecond don't reorder
	// between otherwise identical reads.
	query += ` ORDER BY w.last_seen DESC, w.edge_id DESC`
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

// GetWatchItem looks up one watched fact by EDGE id, or ErrNotFound when there
// is no overlay row or its edge is not live — same pointer-returning,
// sql.ErrNoRows → ErrNotFound shape as every other single-row getter in this
// package. The two absences collapse into one answer on purpose: from every
// caller's point of view ("is this still on the watchlist?") an invalidated
// fact and a never-watched one are the same no.
func (s *Store) GetWatchItem(edgeID int64) (*WatchItem, error) {
	row := s.db.QueryRow(watchItemSelect+` WHERE w.edge_id = ?`, edgeID)
	w, err := scanWatchItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get watch item %d: %w", edgeID, err)
	}
	return w, nil
}

// FindWatchItem returns the WATCHED fact already tracking this observation, or
// ErrNotFound when none does. It is the lookup that makes BumpWatchSighting
// reachable: without it a caller has no way to discover that an observation is
// a REPEAT, so it mints a near-duplicate fact instead of folding, and
// `sightings` — the one signal separating a one-off from a recurring pattern,
// and so the whole basis for deciding what gets fixed versus merely watched —
// sits at 1 across N edges that are all the same thing. Nothing errors when
// that happens; the feature just quietly stops working.
//
// MATCHING IS EXACT, after normalising both sides identically: oneLine on the
// Go side (whitespace runs collapsed, ends trimmed — the same rule
// normalizeGraphAdd already applied to the stored fact), then ASCII-case-
// folded in SQL. Case-folding is not cosmetic — the caller is an LLM that will
// write "Hermes agents re-read the contract" one night and "hermes agents
// re-read the contract" the next, and those must land on one fact.
//
// SQLite's LOWER() folds ASCII only, so observations differing solely in the
// case of a non-ASCII letter will NOT match. Every class of observation this
// table holds is English prose about code, so the practical exposure is nil,
// and a Go-side unicode fold would cost the single-expression property that
// keeps this comparable to what the migration folded on.
//
// State is deliberately NOT filtered: a `declined` or already-judged item
// matches like any other, because finding it is precisely how the caller
// learns this observation has been ruled on and must not be re-proposed.
// Hiding those would make a declined observation come back as a fresh one
// every single night.
//
// When several watched facts collide, the MOST RECENTLY SEEN wins — same
// last_seen DESC, edge_id DESC ordering as ListWatchItems — so a bump lands on
// the live one rather than resurrecting the stalest copy.
func (s *Store) FindWatchItem(observation string) (*WatchItem, error) {
	row := s.db.QueryRow(
		watchItemSelect+` WHERE LOWER(e.fact) = LOWER(?)
			ORDER BY w.last_seen DESC, w.edge_id DESC LIMIT 1`,
		oneLine(observation),
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

// FindLiveEdgeByFact is FindWatchItem's wider sibling: the same normalization
// over ALL live edges, watched or not. It is the "create vs promote" probe the
// add path runs after FindWatchItem misses — an observation the dream loop has
// never watched may still already BE a fact in the graph (extracted from a
// transcript, or written by hand through `k graph add`), and in that case the
// right move is to hang an overlay on the existing fact rather than mint a
// second edge saying the same sentence.
//
// (nil, nil) — not ErrNotFound — when nothing matches, because "no such fact
// yet" is the ordinary path here rather than a failure: the caller's next step
// is to create one, exactly as ResolveNode returns nil to mean "create rather
// than merge".
//
// LOWEST edge id wins, deliberately the opposite tiebreak from FindWatchItem's
// newest-first. There the question is "which watched item should absorb this
// sighting", and the freshest one is the live one; here it is "which existing
// fact is THE fact", and the oldest is the one every other reference in the
// graph already points at.
func (s *Store) FindLiveEdgeByFact(fact string) (*GraphEdge, error) {
	var e GraphEdge
	err := s.db.QueryRow(`SELECT id, src_id, dst_id, predicate, fact, episode_id, strength, occurrences,
		valid_from, valid_until, embedding, created_at, expired_at
		FROM kg_edges WHERE expired_at = 0 AND LOWER(fact) = LOWER(?) ORDER BY id LIMIT 1`,
		oneLine(fact)).
		Scan(&e.ID, &e.SrcID, &e.DstID, &e.Predicate, &e.Fact, &e.EpisodeID, &e.Strength,
			&e.Occurrences, &e.ValidFrom, &e.ValidUntil, &e.Embedding, &e.CreatedAt, &e.ExpiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find live edge by fact %q: %w", fact, err)
	}
	return &e, nil
}

// WatchEdge starts watching an EXISTING live edge: it inserts the overlay and
// nothing else, applying the defaults a caller normally leaves to this package
// — LastSeen/UpdatedAt stamped to now when zero (same convenience as
// AddFeedback's TS and AddThread's CreatedAt), Sightings defaulted to 1
// (starting to watch IS the first sighting), and a blank State defaulted to
// "watching".
//
// FirstSeen defaults to LastSeen, NOT to now, and the difference is not
// cosmetic: a caller replaying history (an importer, a fixture, a sweep
// backfilling a night it missed) supplies LastSeen and leaves FirstSeen zero,
// and stamping "now" there would store an item first seen AFTER it was last
// seen. Every window/age calculation downstream reads that as a negative
// lifetime, and nothing errors. Defaulting to LastSeen makes the degenerate
// case "first and last sighting are the same moment", which is exactly what a
// one-sighting item means. Only when BOTH are zero does now apply, via
// LastSeen.
//
// Verdict/Comment/CommentedAt are deliberately NOT caller-settable here —
// they're carl's, written later only via SetWatchVerdict — so a fresh overlay
// always starts unjudged, exactly as AddFeedback starts a row unaddressed.
//
// Two refusals rather than two silent successes:
//
//   - ErrNotFound when no LIVE edge carries that id. Watching a fact the graph
//     has unlearned (or never had) would insert an overlay no read path can
//     ever surface, since every read INNER JOINs the live edge — a write that
//     "succeeds" into invisibility is the failure mode this package keeps
//     paying for.
//   - An error, not an overwrite, when the edge is ALREADY watched. The
//     overlay carries sightings, first_seen and carl's verdict; a blind
//     re-insert would reset all of it, which is precisely what the add path's
//     Find→fold flow exists to avoid. The caller that meant to fold has
//     BumpWatchSighting/UpdateWatchItem.
func (s *Store) WatchEdge(edgeID int64, w WatchItem) error {
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
	} else if err := validateWatchState(w.State); err != nil {
		return err
	}

	var live int64
	err := s.db.QueryRow(`SELECT id FROM kg_edges WHERE id = ? AND expired_at = 0`, edgeID).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("watch edge %d: %w", edgeID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("watch edge %d: %w", edgeID, err)
	}

	// ON CONFLICT DO NOTHING rather than a SELECT-then-INSERT pair: kraken
	// serve answers HTTP concurrently, and the primary key is the only thing
	// that can decide the race without a transaction.
	res, err := s.db.Exec(
		`INSERT INTO kg_watch (edge_id, class, state, sightings, first_seen, last_seen,
			evidence, suggested_fix, pr_url, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(edge_id) DO NOTHING`,
		edgeID, w.Class, w.State, w.Sightings, w.FirstSeen, w.LastSeen,
		w.Evidence, w.SuggestedFix, w.PRURL, w.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("watch edge %d: %w", edgeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("watch edge %d: %w", edgeID, err)
	}
	if n == 0 {
		return fmt.Errorf("watch edge %d: already watched", edgeID)
	}
	return nil
}

// UpdateWatchItem is the AGENT's write: it updates the agent-owned overlay
// columns and PRESERVES the carl-owned trio (verdict, comment, commented_at)
// completely, whatever the passed WatchItem happens to carry in those three
// fields.
//
// This is the single most important behaviour in this file, and it is the same
// rule questions.go's upsertQuestionZeusColumns enforces for
// answer/answered_at: the two sides write the same row at unpredictable times,
// and the agent's write is a full-row restatement of what it currently
// believes. If that restatement carried the carl-owned columns too, then any
// nightly pass that re-stated an observation carl had just commented on would
// silently erase his comment — the row would still look fine, so nothing would
// ever surface the loss. Naming the agent-owned columns explicitly in the SET
// clause makes that impossible by construction rather than by the caller
// remembering to re-read first.
//
// THE UPDATE IS PARTIAL: a zero-valued field means "leave this column alone",
// never "write a zero here". Only the fields the caller actually populated are
// overwritten.
//
// That is not a convenience, it is the difference between working and silently
// broken. The caller is an LLM assembling a JSON object, and it will restate
// only the fields it means to change — which under verbatim-write semantics
// stores state=” and sightings=0. Neither errors, neither removes the
// overlay, and both remove it from ListWatchItems("watching"), the one query
// the board renders: the observation stops existing as far as the feature is
// concerned while every read of the raw table still looks fine. Sightings
// would additionally reset to 0, destroying the recurrence count that is the
// whole basis for deciding what gets fixed.
//
// The "leave alone" test is done in SQL (COALESCE(NULLIF(?, <zero>), col)) so
// this stays ONE statement: a read-modify-write in Go would reintroduce the
// lost-update race that BumpWatchSighting's SQL-side increment exists to
// avoid.
//
// The cost, stated plainly: clearing a text column back to empty is not
// expressible through this verb. Evidence has its own path (BumpWatchSighting
// replaces it outright), and blanking suggested_fix/pr_url is not something
// the sweep does — whereas accidentally blanking them is something it would do
// constantly. If a clearing verb is ever genuinely needed, it should be its own
// narrow method, not a loosening of this one.
//
// THE FACT ITSELF IS NOT REACHABLE FROM HERE, and that is structural rather
// than an omission: the observation, its endpoints and its predicate live on
// kg_edges, which is insert-only outside invalidation. Rewording is
// remove-and-re-add, so this verb touches overlay columns only and cannot move
// an overlay to a different edge.
//
// UpdatedAt is the deliberate exception to "zero means leave alone", always
// written (stamped to now when zero): it records that the agent touched the
// row at all, which is true even on an update that changed one field.
//
// ErrNotFound when no overlay row carries that edge id.
func (s *Store) UpdateWatchItem(edgeID int64, w WatchItem) error {
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
		`UPDATE kg_watch SET
			class         = COALESCE(NULLIF(?, ''), class),
			state         = COALESCE(NULLIF(?, ''), state),
			sightings     = COALESCE(NULLIF(?, 0), sightings),
			first_seen    = COALESCE(NULLIF(?, 0), first_seen),
			last_seen     = COALESCE(NULLIF(?, 0), last_seen),
			evidence      = COALESCE(NULLIF(?, ''), evidence),
			suggested_fix = COALESCE(NULLIF(?, ''), suggested_fix),
			pr_url        = COALESCE(NULLIF(?, ''), pr_url),
			updated_at    = ?
			WHERE edge_id = ?`,
		w.Class, w.State, w.Sightings, w.FirstSeen, w.LastSeen,
		w.Evidence, w.SuggestedFix, w.PRURL, w.UpdatedAt, edgeID,
	)
	if err != nil {
		return fmt.Errorf("update watch item %d: %w", edgeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update watch item %d: %w", edgeID, err)
	}
	if n == 0 {
		return fmt.Errorf("update watch item %d: %w", edgeID, ErrNotFound)
	}
	return nil
}

// BumpWatchSighting folds a REPEAT observation into the overlay that already
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
// timestamp conveniences. ErrNotFound when no overlay row carries that edge id.
//
// The overlay's `sightings` is NOT the edge's `occurrences`, and nothing here
// keeps them in step: occurrences counts distinct EPISODES independently
// stating the fact (the graph's corroboration measure, bumped by InsertEdge),
// while sightings counts the dream loop's own re-observations. They answer
// different questions and would be wrong averaged together.
func (s *Store) BumpWatchSighting(edgeID int64, lastSeen int64, evidence string) error {
	if lastSeen == 0 {
		lastSeen = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`UPDATE kg_watch SET sightings = sightings + 1, last_seen = ?, updated_at = ?,
			evidence = ? WHERE edge_id = ?`,
		lastSeen, lastSeen, evidence, edgeID,
	)
	if err != nil {
		return fmt.Errorf("bump watch item %d: %w", edgeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("bump watch item %d: %w", edgeID, err)
	}
	if n == 0 {
		return fmt.Errorf("bump watch item %d: %w", edgeID, ErrNotFound)
	}
	return nil
}

// SetWatchVerdict is CARL's write, and the exact mirror of UpdateWatchItem: it
// touches ONLY verdict/comment/commented_at and leaves every agent-owned
// column alone, so judging an item can never roll back an observation the
// nightly pass has since refined. Same split, same reasoning, as questions.go's
// AnswerQuestion against upsertQuestionZeusColumns.
//
// An empty verdict CLEARS the judgement (reopening the item) rather than being
// rejected — the three columns are written unconditionally, so passing
// ("", "", "") is the documented way to undo a verdict. updated_at is
// deliberately NOT bumped here: it tracks the agent's own last write, and a
// verdict is not one. ErrNotFound when no overlay row carries that edge id.
func (s *Store) SetWatchVerdict(edgeID int64, verdict, comment, commentedAt string) error {
	if err := validateWatchVerdict(verdict); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE kg_watch SET verdict = ?, comment = ?, commented_at = ? WHERE edge_id = ?`,
		verdict, comment, commentedAt, edgeID,
	)
	if err != nil {
		return fmt.Errorf("set verdict on watch item %d: %w", edgeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set verdict on watch item %d: %w", edgeID, err)
	}
	if n == 0 {
		return fmt.Errorf("set verdict on watch item %d: %w", edgeID, ErrNotFound)
	}
	return nil
}

// UnwatchEdge stops watching a fact: it deletes the OVERLAY row and nothing
// else. The edge and its episode survive untouched, which is the whole
// difference between this and the DeleteWatchItem it replaces — taking an
// observation off the dream loop's board is not a claim that the observation
// was never true, and the graph is the place true-then, false-now is expressed
// (InvalidateEdge), not a DELETE.
//
// ErrNotFound when no overlay row carries that edge id — including on a repeat
// unwatch of an already-unwatched edge, same contract as DeleteFeedback.
func (s *Store) UnwatchEdge(edgeID int64) error {
	res, err := s.db.Exec(`DELETE FROM kg_watch WHERE edge_id = ?`, edgeID)
	if err != nil {
		return fmt.Errorf("unwatch edge %d: %w", edgeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("unwatch edge %d: %w", edgeID, err)
	}
	if n == 0 {
		return fmt.Errorf("unwatch edge %d: %w", edgeID, ErrNotFound)
	}
	return nil
}
