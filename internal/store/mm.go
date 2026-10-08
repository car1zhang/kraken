package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// mm.go — schema v29's mm_items and mm_code_anchors (schema.go): carl's own
// record of what he has decided and what he has actually read.
//
// The subsystem is called `mm` everywhere — table, file, route, verb —
// because "library" is already taken by saved_artifacts and the two are
// different things: a library entry is a copy carl kept, an mm item is a claim
// that carl KNOWS something. The item is the source of truth.
//
// episode_id is a vestigial column: nothing writes it any more (it used to
// name the graph episode an item minted), but it is still scanned and served
// because shipped iOS builds decode `episode_id` as a non-optional key.
//
// Three properties shape every verb below.
//
// SURVIVAL. Neither table carries a foreign key or a cascade onto jobs, the
// saved_artifacts posture (schemaV25) — sharpened here by the case that
// motivated the whole feature: a decision minted from an answered question
// COPIES the question's text, context, recommendation and answer, because
// `questions` is ON DELETE CASCADE and the row is gone the moment carl tidies
// the job up. An item that referenced its source would record only what carl
// still had the job for.
//
// ONE-WAY INVALIDATION. An item is live or it is dead, and nothing here moves
// it back. InvalidateMMItem stamps a row once and is a silent success on a
// row already stamped, deliberately preserving the FIRST reason: the verify
// pass re-checks a repo every ten minutes, so a second stamp would overwrite
// "the function you read was deleted" with whatever this pass happened to
// notice, and the frozen content plus the reason it died is the entire value
// of a dead item. Content reappearing later never resurrects one either —
// carl read a thing that is no longer there, and that stays true.
//
// A FAILED READ IS NOT A DEATH. mm_code_anchors keeps check_error and the
// invalidation columns strictly disjoint: an offline fetch, a missing
// checkout, a git timeout all land in check_error and leave the item live.
// Only the absence of the TEXT may say "changed" — see internal/core/anchor.go's
// header for why hunk overlap alone cannot.

// MMItem is one row of carl's mental model, WITHOUT its snapshot bytes.
//
// The bytes are elided from every list and get on purpose: only whole-artifact
// marks carry any, they are megabyte-scale, and the one caller that wants them
// is serving a single frozen file (GetMMSnapshot). This is the same reader
// split ListFeedbackMeta/GetFeedback and ListSavedArtifacts/GetSavedArtifact
// draw, for the same reason.
//
// InvalidatedAt is 0 for a LIVE item; the column itself is NULL, the
// archived_at precedent rather than a zero sentinel, and the two spellings
// meet in the scan.
//
// Tagless like every other type in this package — the wire shape is the API
// layer's own DTO (internal/api/mm.go), independently evolvable.
type MMItem struct {
	ID           int64
	Kind, Source string
	JobID        int64
	// Slug is the DISPLAY slug at capture time; identity is JobID, which may
	// itself be 0 for an item that belongs to no job (a note, a code reading).
	Slug, SourceRef, Content, Context string
	// SnapshotExt is the frozen artifact's extension, kept so the snapshot
	// route can answer with a MIME type; empty when there is no snapshot.
	SnapshotExt                         string
	EpisodeID, CreatedAt, InvalidatedAt int64
	InvalidReason, InvalidDetail        string
}

// MMCodeAnchor is the 1:1 companion of a source=code item: where the lines
// carl read live, in which commit's coordinates, and the exact text he read.
//
// Text is the load-bearing column. Storing it is what makes every later check
// self-contained — the verify pass never needs the original commit to still be
// reachable — and what preserves what carl knew after the anchor dies, which
// is the only thing a dead reading is still good for.
//
// Path moves as the file is renamed; OrigPath records where it started, set
// once on the first rename and never overwritten. CheckedAt/CheckedSHA/
// CheckError are verify bookkeeping and say nothing about liveness; InvalidSHA
// and InvalidPR record where it died and are written only alongside the item's
// own invalidation.
type MMCodeAnchor struct {
	ItemID               int64
	Repo, Path, OrigPath string
	StartLine, EndLine   int

	AnchorSHA, AnchorKind, Text, TextSHA, Lang string

	CheckedAt              int64
	CheckedSHA, CheckError string
	Ambiguous              bool

	InvalidSHA, InvalidPR string
}

// validMMKind / validMMSource / validMMAnchorKind bound the three closed sets
// this package polices, the idiom questions.go's validImportance and watch.go's
// validWatchState use.
//
// All three are validated for the same reason, and it is not tidiness: each is
// a set some QUERY or renderer branches on, so a typo does not produce a
// visibly wrong row — it produces an invisible one, stored and absent from
// every view that filters. The schema CHECKs say the same thing a second time;
// the Go check is what turns a constraint violation into a sentence naming the
// accepted set.
var (
	validMMKind       = map[string]bool{"decision": true, "reading": true, "note": true}
	validMMSource     = map[string]bool{"question": true, "artifact": true, "artifact-element": true, "code": true, "agent-output": true, "manual": true}
	validMMAnchorKind = map[string]bool{"origin-main": true, "merge-base": true, "head": true, "dirty": true}
)

// SHA256Hex is the identity hash this subsystem compares payloads with: hex
// sha256. The token watermarks are NOT a precedent for it — a
// job_token_files row is a byte offset, not a digest.
//
// Exported because FindLiveMMItem takes a hash rather than a payload — the
// caller holds the bytes (a snapshot) or the string (a content) and has to
// arrive at the same number this package stored — and a second implementation
// of "the hash" in the API layer is exactly how two halves of one comparison
// drift apart.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mmItemColumns is every read in this file. invalidated_at is COALESCEd to 0
// so a live row's NULL lands in a plain int64 — the one place the two
// spellings of "live" meet — and every other column is COALESCEd for
// jobColumns' belt-and-suspenders reason: the schema declares them NOT NULL
// with same-typed defaults, but a NULL that sneaks in anyway must degrade to a
// blank field rather than error the whole listing.
const mmItemColumns = `SELECT id, COALESCE(kind,''), COALESCE(source,''), COALESCE(job_id,0),
	COALESCE(slug,''), COALESCE(source_ref,''), COALESCE(content,''), COALESCE(context,''),
	COALESCE(snapshot_ext,''), COALESCE(episode_id,0), COALESCE(created_at,0),
	COALESCE(invalidated_at,0), COALESCE(invalid_reason,''), COALESCE(invalid_detail,'')
	FROM mm_items`

func scanMMItem(row interface{ Scan(...any) error }) (MMItem, error) {
	var it MMItem
	err := row.Scan(&it.ID, &it.Kind, &it.Source, &it.JobID, &it.Slug, &it.SourceRef,
		&it.Content, &it.Context, &it.SnapshotExt, &it.EpisodeID, &it.CreatedAt,
		&it.InvalidatedAt, &it.InvalidReason, &it.InvalidDetail)
	return it, err
}

// AddMMItem stores one item and returns its id.
//
// VALIDATE FIRST, and the reason is atomicity rather than tidiness: the caller
// may write more rows for this item immediately after this returns (a frozen
// snapshot, a code item's anchor), and no transaction spans those writes. A
// refusal here must
// therefore leave zero rows anywhere, which it does by refusing before the
// INSERT rather than by cleaning up after one.
//
// content_sha is stamped from Content. For an item that will carry frozen
// bytes it is re-stamped by SetMMSnapshot from those bytes instead — an
// artifact item's content is only its name, so the name's hash could never
// tell one revision of the artifact from another. One column, one rule: it
// hashes whatever this row FROZE.
//
// CreatedAt is required rather than defaulted to now, the AddGoal/SaveArtifact
// convention: the API layer stamps one time per request, and a test can write
// a row at a chosen instant without moving the machine's clock.
//
// A non-zero InvalidatedAt is honoured rather than ignored, so a caller
// replaying history can store a row that is already dead; the ordinary path
// leaves it zero and the column lands NULL, which is what "live" is spelled as.
func (s *Store) AddMMItem(it MMItem) (int64, error) {
	if !validMMKind[it.Kind] {
		return 0, fmt.Errorf("bad mm kind %q: want decision, reading, or note", it.Kind)
	}
	if !validMMSource[it.Source] {
		return 0, fmt.Errorf("bad mm source %q: want question, artifact, artifact-element, code, agent-output, or manual", it.Source)
	}
	if strings.TrimSpace(it.Content) == "" {
		return 0, errors.New("mm item content is empty")
	}
	if it.CreatedAt <= 0 {
		return 0, fmt.Errorf("mm item created_at must be positive, got %d", it.CreatedAt)
	}
	var invalidatedAt any
	if it.InvalidatedAt > 0 {
		invalidatedAt = it.InvalidatedAt
	}
	res, err := s.db.Exec(
		`INSERT INTO mm_items (kind, source, job_id, slug, source_ref, content, context,
			content_sha, snapshot_ext, episode_id, created_at, invalidated_at,
			invalid_reason, invalid_detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.Kind, it.Source, it.JobID, it.Slug, it.SourceRef, it.Content, it.Context,
		SHA256Hex([]byte(it.Content)), it.SnapshotExt, it.EpisodeID, it.CreatedAt,
		invalidatedAt, it.InvalidReason, it.InvalidDetail,
	)
	if err != nil {
		return 0, fmt.Errorf("add mm item (%s/%s): %w", it.Kind, it.Source, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("add mm item (%s/%s): %w", it.Kind, it.Source, err)
	}
	return id, nil
}

// SetMMSnapshot freezes an artifact's bytes onto an existing item, and
// re-stamps content_sha from those bytes.
//
// The re-stamp is what makes FindLiveMMItem's quad answer the right question
// for a whole-artifact mark: two marks of the same artifact name differ only
// in their BYTES, so hashing the name would report every rewrite as an
// identical re-mark and never supersede anything.
//
// The bytes are a COPY, never a pointer at the live file — feedback.snapshot's
// freeze, for its reason: the agent that owns an artifact rewrites it in
// place, and a mark that silently followed those rewrites would be a bookmark
// rather than a record of what carl read.
//
// ErrNotFound when the id names nothing.
func (s *Store) SetMMSnapshot(id int64, bytes []byte, ext string) error {
	res, err := s.db.Exec(
		`UPDATE mm_items SET snapshot = ?, snapshot_ext = ?, content_sha = ? WHERE id = ?`,
		bytes, ext, SHA256Hex(bytes), id,
	)
	if err != nil {
		return fmt.Errorf("set mm snapshot %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set mm snapshot %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set mm snapshot %d: %w", id, ErrNotFound)
	}
	return nil
}

// GetMMSnapshot reads one item's frozen bytes and their extension.
//
// ErrNotFound covers BOTH an unknown id and an item that never froze any, and
// collapsing them is deliberate: the caller is serving a file, and "no such
// item" and "that item is not a file" are the same 404 to whoever asked.
//
// A zero-length snapshot is distinguishable from an absent one — the column is
// nullable precisely so that a genuinely empty artifact can still be served —
// and only the NULL is a miss.
func (s *Store) GetMMSnapshot(id int64) ([]byte, string, error) {
	var snap []byte
	var ext string
	err := s.db.QueryRow(`SELECT snapshot, COALESCE(snapshot_ext,'') FROM mm_items WHERE id = ?`, id).
		Scan(&snap, &ext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("get mm snapshot %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, "", fmt.Errorf("get mm snapshot %d: %w", id, err)
	}
	if snap == nil {
		return nil, "", fmt.Errorf("get mm snapshot %d: %w", id, ErrNotFound)
	}
	return snap, ext, nil
}

// ListMMItems returns items newest first — created_at DESC with id DESC as the
// tiebreak, so two items marked in the same millisecond still come back in a
// stable order rather than in whatever order SQLite chose today.
//
// includeInvalidated false returns LIVE items only, which is the memory pane's
// default view; true returns everything, which is what makes a dead item's
// frozen content readable long after its source moved on.
//
// An empty result is not an error: it is the state every database is in before
// carl marks his first thing.
func (s *Store) ListMMItems(includeInvalidated bool) ([]MMItem, error) {
	query := mmItemColumns
	if !includeInvalidated {
		query += ` WHERE invalidated_at IS NULL`
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list mm items: %w", err)
	}
	defer rows.Close()
	out := []MMItem{}
	for rows.Next() {
		it, err := scanMMItem(rows)
		if err != nil {
			return nil, fmt.Errorf("list mm items: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mm items: %w", err)
	}
	return out, nil
}

// GetMMItem reads one item by id, live or not — ErrNotFound when the id names
// nothing. Invalidated items are readable on purpose: the pane expands a dead
// row to show what carl knew.
func (s *Store) GetMMItem(id int64) (MMItem, error) {
	it, err := scanMMItem(s.db.QueryRow(mmItemColumns+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MMItem{}, fmt.Errorf("get mm item %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return MMItem{}, fmt.Errorf("get mm item %d: %w", id, err)
	}
	return it, nil
}

// FindLiveMMItem answers "has carl already marked exactly this?", matching all
// four of source, job, source_ref and the frozen payload's hash.
//
// It is what makes every capture surface idempotent: clicking ✓ read twice, or
// re-running the Zed task on the same lines, returns the item that already
// exists instead of minting a near-duplicate the memory pane would render
// twice.
//
// All four have to match, and the fourth is what makes the other three safe: a
// whole-artifact mark whose bytes have moved on is NOT this item, it is a
// successor, and the caller supersedes rather than dedupes. contentSHA is
// SHA256Hex of the item's Content, or of the SNAPSHOT bytes for a
// source=artifact item — the same rule content_sha itself follows.
//
// (0, false, nil) on no match: not finding one is the ordinary path here
// rather than a failure, so the caller's next step is a create, exactly as
// ResolveNode returns nil to mean "create rather than merge".
//
// Newest first among several matches, for the same reason AddWatchItem's fold
// probe orders that way: the freshest row is the live one a caller should be
// folding onto, and the stalest is the one a supersede has simply not reached
// yet.
func (s *Store) FindLiveMMItem(source string, jobID int64, sourceRef, contentSHA string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM mm_items
			WHERE invalidated_at IS NULL AND source = ? AND job_id = ? AND source_ref = ? AND content_sha = ?
			ORDER BY created_at DESC, id DESC LIMIT 1`,
		source, jobID, sourceRef, contentSHA,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find live mm item (%s %d %q): %w", source, jobID, sourceRef, err)
	}
	return id, true, nil
}

// SupersedeLiveMMItems invalidates every live item for one (source, job,
// source_ref) with reason `superseded`, and returns how many it closed.
//
// This is the "carl read it again, and it says something different now" path:
// FindLiveMMItem missed on the hash, so what is stored is a record of an
// EARLIER revision. It is invalidated rather than updated because the old row
// is still true — carl really did read that version — and updating it would
// silently rewrite the record of what he read.
//
// Zero matches is a success returning 0, not ErrNotFound: the first mark of an
// artifact supersedes nothing, and that is the common case rather than an
// error.
//
// excludeID SPARES one row, and it exists to fix an ordering hazard rather
// than as a convenience. Superseding first and inserting second means a create
// that fails afterwards has already invalidated carl's previous mark and left
// no replacement — one-way and terminal, so the mark is simply gone while the
// response talks about something else. With an exclusion the caller can insert
// the replacement FIRST and supersede after, naming the new row, which makes
// that window unreachable: either both rows exist or neither does. 0 excludes
// nothing, since AUTOINCREMENT ids start at 1 and no row can carry it.
func (s *Store) SupersedeLiveMMItems(source string, jobID int64, sourceRef string, at, excludeID int64) (int, error) {
	res, err := s.db.Exec(
		`UPDATE mm_items SET invalidated_at = ?, invalid_reason = 'superseded'
			WHERE invalidated_at IS NULL AND source = ? AND job_id = ? AND source_ref = ? AND id <> ?`,
		at, source, jobID, sourceRef, excludeID,
	)
	if err != nil {
		return 0, fmt.Errorf("supersede mm items (%s %d %q): %w", source, jobID, sourceRef, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("supersede mm items (%s %d %q): %w", source, jobID, sourceRef, err)
	}
	return int(n), nil
}

// InvalidateMMItem closes an item: one-way, terminal, and idempotent.
//
// Three outcomes, and the middle one is the whole design: an unknown id is
// ErrNotFound; an ALREADY-invalidated item is a silent success that overwrites
// NOTHING; a live item gets all three columns stamped.
//
// Preserving the first reason matters because the verify pass revisits every
// repo on a ten-minute floor. A second stamp would replace "the function you
// read was deleted, at abc123" with whatever this pass happened to notice
// about a file that no longer contains it — and the frozen content plus the
// reason it died is the entire remaining value of a dead item.
//
// Nothing resurrects a row. The same text reappearing at head later does not
// clear invalidated_at, because the claim being recorded is that carl read
// something that has since changed underneath him, and that stays true.
func (s *Store) InvalidateMMItem(id, at int64, reason, detail string) error {
	res, err := s.db.Exec(
		`UPDATE mm_items SET invalidated_at = ?, invalid_reason = ?, invalid_detail = ?
			WHERE id = ? AND invalidated_at IS NULL`,
		at, reason, detail, id,
	)
	if err != nil {
		return fmt.Errorf("invalidate mm item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("invalidate mm item %d: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	// Zero rows means either "no such item" or "already invalidated", and the
	// two answer differently — so the existence probe runs only on that miss,
	// never on the common path.
	var exists int64
	err = s.db.QueryRow(`SELECT id FROM mm_items WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("invalidate mm item %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("invalidate mm item %d: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// mm_code_anchors
// ---------------------------------------------------------------------------

// mmAnchorColumns is every anchor read in this file, COALESCEd for
// mmItemColumns' reason.
const mmAnchorColumns = `SELECT a.item_id, COALESCE(a.repo,''), COALESCE(a.path,''), COALESCE(a.orig_path,''),
	COALESCE(a.start_line,0), COALESCE(a.end_line,0), COALESCE(a.anchor_sha,''),
	COALESCE(a.anchor_kind,''), COALESCE(a.text,''), COALESCE(a.text_sha,''), COALESCE(a.lang,''),
	COALESCE(a.checked_at,0), COALESCE(a.checked_sha,''), COALESCE(a.check_error,''),
	COALESCE(a.ambiguous,0), COALESCE(a.invalid_sha,''), COALESCE(a.invalid_pr,'')
	FROM mm_code_anchors a`

func scanMMCodeAnchor(row interface{ Scan(...any) error }) (MMCodeAnchor, error) {
	var a MMCodeAnchor
	err := row.Scan(&a.ItemID, &a.Repo, &a.Path, &a.OrigPath, &a.StartLine, &a.EndLine,
		&a.AnchorSHA, &a.AnchorKind, &a.Text, &a.TextSHA, &a.Lang,
		&a.CheckedAt, &a.CheckedSHA, &a.CheckError, &a.Ambiguous, &a.InvalidSHA, &a.InvalidPR)
	return a, err
}

// AddMMCodeAnchor stores the anchor for a source=code item.
//
// Six refusals in four groups, all validate-first for AddMMItem's reason (no
// transaction spans the item and the anchor). Three groups
// are ordinary field checks:
//
//   - AnchorKind must name one of the four rungs of the resolution ladder.
//   - StartLine ≥ 1 and EndLine ≥ StartLine, because the range is 1-based and
//     INCLUSIVE. A zero start would be a range no editor can produce, and an
//     inverted range makes MapRange's above/below classification meaningless
//     rather than merely wrong.
//   - Text and TextSHA must both be non-empty. An anchor with no text is one
//     the verify pass can never check — LocateText refuses an empty needle
//     precisely so an empty anchor cannot match everywhere and survive forever
//     — so it would be a permanently unverifiable claim.
//
// And the fourth group is one BICONDITIONAL rather than two independent
// checks: AnchorKind == "dirty" if and only if AnchorSHA == "". A dirty anchor is one
// taken from an unsaved buffer that matched no commit, so there is no SHA to
// record and a fabricated one would send the verify pass diffing against a
// commit that never contained this text; conversely a committed anchor with no
// SHA has no coordinates for stage (a) to work in, and would silently fall
// through to the text ladder on every single pass.
func (s *Store) AddMMCodeAnchor(a MMCodeAnchor) error {
	if !validMMAnchorKind[a.AnchorKind] {
		return fmt.Errorf("bad mm anchor kind %q: want origin-main, merge-base, head, or dirty", a.AnchorKind)
	}
	if a.StartLine < 1 {
		return fmt.Errorf("mm anchor start line must be >= 1, got %d", a.StartLine)
	}
	if a.EndLine < a.StartLine {
		return fmt.Errorf("mm anchor end line %d is before start line %d", a.EndLine, a.StartLine)
	}
	if a.Text == "" {
		return errors.New("mm anchor text is empty")
	}
	if a.TextSHA == "" {
		return errors.New("mm anchor text sha is empty")
	}
	if (a.AnchorKind == "dirty") != (a.AnchorSHA == "") {
		return fmt.Errorf("mm anchor kind %q and sha %q disagree: dirty means no sha, and a sha means a commit rung",
			a.AnchorKind, a.AnchorSHA)
	}
	if _, err := s.db.Exec(
		`INSERT INTO mm_code_anchors (item_id, repo, path, orig_path, start_line, end_line,
			anchor_sha, anchor_kind, text, text_sha, lang, checked_at, checked_sha,
			check_error, ambiguous, invalid_sha, invalid_pr)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ItemID, a.Repo, a.Path, a.OrigPath, a.StartLine, a.EndLine,
		a.AnchorSHA, a.AnchorKind, a.Text, a.TextSHA, a.Lang, a.CheckedAt, a.CheckedSHA,
		a.CheckError, a.Ambiguous, a.InvalidSHA, a.InvalidPR,
	); err != nil {
		return fmt.Errorf("add mm code anchor for item %d: %w", a.ItemID, err)
	}
	return nil
}

// GetMMCodeAnchor reads one item's anchor. (zero, false, nil) when the item has
// none — every item except a code reading is in exactly that state, so absence
// is the ordinary answer rather than ErrNotFound.
func (s *Store) GetMMCodeAnchor(itemID int64) (MMCodeAnchor, bool, error) {
	a, err := scanMMCodeAnchor(s.db.QueryRow(mmAnchorColumns+` WHERE a.item_id = ?`, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return MMCodeAnchor{}, false, nil
	}
	if err != nil {
		return MMCodeAnchor{}, false, fmt.Errorf("get mm code anchor %d: %w", itemID, err)
	}
	return a, true, nil
}

// ListLiveMMCodeAnchors returns every anchor whose ITEM is still live — the
// verify pass' whole worklist, in the order the pass walks it: repo first (one
// fetch and one head resolve serve every anchor in a repo), then item id.
//
// Liveness is a property of the item rather than of the anchor, and the join
// is what makes that structural: an invalidated item's anchor row survives
// untouched so the pane can render what carl knew, but it is never checked
// again — invalidation is terminal, and a pass that re-checked dead anchors
// would spend its budget on rows no answer can change.
func (s *Store) ListLiveMMCodeAnchors() ([]MMCodeAnchor, error) {
	rows, err := s.db.Query(mmAnchorColumns + `
		JOIN mm_items i ON i.id = a.item_id AND i.invalidated_at IS NULL
		ORDER BY a.repo, a.item_id`)
	if err != nil {
		return nil, fmt.Errorf("list live mm code anchors: %w", err)
	}
	defer rows.Close()
	out := []MMCodeAnchor{}
	for rows.Next() {
		a, err := scanMMCodeAnchor(rows)
		if err != nil {
			return nil, fmt.Errorf("list live mm code anchors: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list live mm code anchors: %w", err)
	}
	return out, nil
}

// ListMMCodeAnchors returns EVERY anchor row — dead items' as well as live
// ones — ordered by item id.
//
// It is the RENDER-side sibling of ListLiveMMCodeAnchors, and the split is
// about which question is being asked. The verify pass wants a worklist, so it
// takes the live join; a listing wants to draw each code mark's coordinates
// beside its frozen text, and an invalidated mark is exactly the row whose
// anchor a reader most wants to see ("here is where this used to be, and here
// is the sha it died at"). Filtering those out would leave the memory pane
// rendering a dead code reading with no location at all.
//
// It exists as a bulk read rather than as a loop over GetMMCodeAnchor because
// the caller is a listing served on a 30s poll from every open surface, and
// this package runs on ONE connection: one query per code item is a round trip
// per item on a shared connection, which is the read that grows fastest as
// carl marks code. Two queries — the items, then this — is the whole cost
// regardless of how many there are.
//
// Ordered by item_id rather than by repo, deliberately the opposite of the
// live listing's order: the caller joins this against a list already ordered
// by the ITEM, so repo grouping would buy nothing and item order makes the
// join a single pass.
func (s *Store) ListMMCodeAnchors() ([]MMCodeAnchor, error) {
	rows, err := s.db.Query(mmAnchorColumns + ` ORDER BY a.item_id`)
	if err != nil {
		return nil, fmt.Errorf("list mm code anchors: %w", err)
	}
	defer rows.Close()
	out := []MMCodeAnchor{}
	for rows.Next() {
		a, err := scanMMCodeAnchor(rows)
		if err != nil {
			return nil, fmt.Errorf("list mm code anchors: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mm code anchors: %w", err)
	}
	return out, nil
}

// ReanchorMMCodeAnchor records a SURVIVED verify: the text is still there, at
// these coordinates, in this head.
//
// Re-anchoring rather than merely stamping a check time is what keeps the next
// pass cheap and correct — the diff it runs is `<anchor_sha>..<head>`, so an
// anchor left at its original SHA would re-derive the same accumulated shift
// from a growing diff every single pass, and would eventually be diffing
// against a commit git has garbage-collected.
//
// The kind becomes `origin-main` whatever it was, deliberately: a survived
// verify resolves the anchor against origin/main's head, so a reading that
// started life on a dirty buffer or a local HEAD is now pinned to a commit
// everyone shares. That is the promotion the ladder exists to reach.
//
// check_error is CLEARED, because this read succeeded — a stale error beside a
// fresh success would read as a repo still broken.
//
// orig_path is set ONCE, on the first pass that moves the path, and never
// overwritten: it answers "where was this when carl read it", which a second
// rename does not change. The CASE evaluates against the pre-update row, so
// `path` on its right-hand side is still the OLD path.
//
// ErrNotFound when the item has no anchor.
func (s *Store) ReanchorMMCodeAnchor(itemID int64, path string, start, end int, sha string, checkedAt int64, ambiguous bool) error {
	res, err := s.db.Exec(
		`UPDATE mm_code_anchors SET
			orig_path   = CASE WHEN orig_path = '' AND path <> ? THEN path ELSE orig_path END,
			path        = ?,
			start_line  = ?,
			end_line    = ?,
			anchor_sha  = ?,
			anchor_kind = 'origin-main',
			checked_at  = ?,
			checked_sha = ?,
			ambiguous   = ?,
			check_error = ''
			WHERE item_id = ?`,
		path, path, start, end, sha, checkedAt, sha, ambiguous, itemID,
	)
	if err != nil {
		return fmt.Errorf("reanchor mm code anchor %d: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reanchor mm code anchor %d: %w", itemID, err)
	}
	if n == 0 {
		return fmt.Errorf("reanchor mm code anchor %d: %w", itemID, ErrNotFound)
	}
	return nil
}

// SetMMCodeAnchorError records that the READ failed — an unreachable network,
// a checkout that is no longer there, a git call that timed out.
//
// It touches checked_at and check_error and NOTHING else, which is the
// invariant rather than an optimisation: a failed read is never an
// invalidation, so this verb must be structurally unable to move the range,
// the SHA or the item's liveness. That is why it exists at all instead of the
// pass reusing Reanchor with empty coordinates.
//
// ErrNotFound when the item has no anchor.
func (s *Store) SetMMCodeAnchorError(itemID, at int64, msg string) error {
	res, err := s.db.Exec(
		`UPDATE mm_code_anchors SET checked_at = ?, check_error = ? WHERE item_id = ?`,
		at, msg, itemID,
	)
	if err != nil {
		return fmt.Errorf("set mm code anchor %d error: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set mm code anchor %d error: %w", itemID, err)
	}
	if n == 0 {
		return fmt.Errorf("set mm code anchor %d error: %w", itemID, ErrNotFound)
	}
	return nil
}

// SetMMCodeAnchorInvalidMeta records WHERE an anchor died: the head SHA at the
// moment the text could not be found, and a best-effort PR url when the pass
// managed to attribute it to one.
//
// It is deliberately separate from the death itself, which goes through
// InvalidateMMItem on the ITEM. The item row is the source of truth for "carl
// knows this"; these two columns are forensics hung off the anchor, and
// writing them says nothing about liveness on its own. An empty pr is the
// normal case rather than a failure.
//
// ErrNotFound when the item has no anchor.
func (s *Store) SetMMCodeAnchorInvalidMeta(itemID int64, sha, pr string) error {
	res, err := s.db.Exec(
		`UPDATE mm_code_anchors SET invalid_sha = ?, invalid_pr = ? WHERE item_id = ?`,
		sha, pr, itemID,
	)
	if err != nil {
		return fmt.Errorf("set mm code anchor %d invalid meta: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set mm code anchor %d invalid meta: %w", itemID, err)
	}
	if n == 0 {
		return fmt.Errorf("set mm code anchor %d invalid meta: %w", itemID, ErrNotFound)
	}
	return nil
}
