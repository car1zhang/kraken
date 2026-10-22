package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// threads.go — schema v7's threads table (schema.go): one row per side
// thread, a forked claude session branched off a job's zeus and living in a
// second tmux window of the same cell.
//
// A thread row is a POINTER, never a transcript. The conversation itself
// stays where claude already writes it (~/.claude/projects/<munged-cwd>/),
// located later by SessionID — so nothing here stores or mirrors message
// bytes, and there is deliberately no per-job cap on how many threads a job
// may accumulate. What the row does own is identity (TID/Title), provenance
// (ParentSession/ForkUUID/Seed — which session it forked from, at which
// message, and the text it was seeded with), the model it was branched on
// (Model, which every respawn re-reads so a revive comes back on the tier carl
// picked), and the live tmux coordinates (Pane/Window) that CloseThread clears
// and SetThreadPane records.
//
// Since schema v36 a row also has a Kind, which is how it RUNS and is
// independent of Role (what it is FOR). A "pane" row is the thread described
// above, in either role. A "headless" row (an ask thread, jobs/askthread.go)
// never has a pane: each question runs as one `claude -p` process, so the
// row instead carries who opened it (Origin*), whether a question is in
// flight (Answering, flipped by ClaimThreadAnswering/FinishThreadAnswering)
// and the last run's failure line (Note). A headless row is always Role
// "ask". The per-job cap on live headless rows lives in jobs
// (MaxAskThreads), not here.

// Thread is one side thread. Deliberately tag-free like every other type in
// this package: the store layer never carries JSON tags — the API layer owns
// its own DTOs and the wire shape that goes with them (internal/api).
type Thread struct {
	JobID         int64
	TID           string
	Title         string
	SessionID     string
	ParentSession string
	ForkUUID      string
	// TurnUUID is a mind thread's capture key: the uuid of zeus' last PROMPT
	// record at branch time, which holds still across every tool call of the
	// turn (core.LastTurnUUID). jobs.mindThreadFor matches on it; ForkUUID
	// stays the overlay's inherited-history split for every role. "" for an
	// ask thread and for a mind thread branched off a zeus with no prompt yet.
	TurnUUID string
	CWD      string
	Pane     string
	Window   string
	State    string
	Seed     string
	// Model is the model ALIAS carl picked for this thread at branch time —
	// one of haiku/sonnet/opus/fable — or "" for a thread branched before the
	// picker existed, or with no pick. The alias rather than the resolved id,
	// so a revive re-resolves through the one map that owns the mapping
	// (core.ThreadModels, which this package cannot import — see
	// validThreadModel below).
	Model string
	// Role is what the thread is FOR: "ask" (carl branched it to ask about the
	// work — every thread before schema v33) or "mind" (the job's mind thread,
	// which captures are routed to and which finalizes mental-model items).
	// An empty Role is stored as "ask", the column's DEFAULT spelled in Go so
	// AddThreadNext hands back the row as stored.
	Role      string
	CreatedAt int64 // unix millis
	ClosedAt  int64 // unix millis; 0 means still live
	// Kind is how the thread runs: "pane" (a claude in a tmux window) or
	// "headless" (an ask thread, one process per question). A write with ""
	// stores "pane". Orthogonal to Role, except that a headless row cannot be
	// a mind thread (ErrThreadKind).
	Kind string
	// OriginNode/OriginLabel/OriginLogin name who opened the thread: the
	// tailscale stable id and label of the node, and the login (or, for this
	// node's own threads, jobs.Operator()). All "" on a pane row.
	OriginNode  string
	OriginLabel string
	OriginLogin string
	// Answering is true while a question is in flight on a headless row.
	Answering bool
	// Note is the last ask run's failure line, "" after a clean answer.
	Note string
}

// threadIDRe / validThreadState bound the two fields this package validates
// on every write, so a caller passing garbage gets a descriptive error the
// API layer can turn straight into a 400 — same idiom, and the same layering
// reason, as questions.go's docQuestionIDRe/validImportance: store cannot
// import the packages above it (util ← store ← core ← jobs ← api ← web), so
// the shared shape lives here as store's own copy.
//
// The `[1-9][0-9]*` tail (rather than a bare `[0-9]+`) rules out "t0" and
// leading-zero forms like "t01": NextThreadID counts from t1, and allowing
// "t01" alongside "t1" would let two distinct tids denote the same number,
// which the numeric ordering and MAX(suffix) arithmetic below both assume
// can't happen.
var threadIDRe = regexp.MustCompile(`^t[1-9][0-9]*$`)

var validThreadState = map[string]bool{"live": true, "closed": true}

// validThreadModel is the set of model aliases a thread row may carry, store's
// own copy of core.ThreadModels' KEYS — the layering (util ← store ← core)
// forbids importing the map itself, the same reason threadIDRe and
// validThreadState are spelled here rather than shared with the layer that
// consumes them. Only the keys are duplicated, never the model ids: the alias
// is all this package stores, and resolving it to a `--model` value stays in
// core alone.
var validThreadModel = map[string]bool{"haiku": true, "sonnet": true, "opus": true, "fable": true}

// validThreadRole mirrors schemaV33's CHECK on threads.role, so a bad role is
// refused with a sentence naming the two values rather than a bare
// constraint-violation string.
var validThreadRole = map[string]bool{"ask": true, "mind": true}

// maxThreadSuffix bounds the numeric part of a tid. The regexp above bounds
// the SHAPE but not the MAGNITUDE, and an unbounded suffix is not a cosmetic
// problem: NextThreadID's `CAST(SUBSTR(tid,2) AS INTEGER)` CLAMPS an
// oversized literal to int64 max rather than erroring (SQLite does not
// overflow-check that cast), Go's max+1 then wraps NEGATIVE, and the
// resulting "t-9223372036854775808" fails validateThreadID forever — one
// storable row would permanently wedge a job out of ever creating another
// thread. Capping the stored value well below int64 max means no tid that
// passes validation here can make that arithmetic clamp or wrap, which is
// what NextThreadID's doc comment claims. A billion side threads on one job
// is not a number reality produces; the cap exists to keep the counter
// total, not to ration threads.
const maxThreadSuffix = 1_000_000_000

func validateThreadID(tid string) error {
	if !threadIDRe.MatchString(tid) {
		return fmt.Errorf("bad thread id %q: want t<digits>, counting from t1", tid)
	}
	// The regexp guarantees digits-only with no leading zero, so the only
	// way ParseInt fails here is overflow — which is exactly the case the
	// cap below exists for, and both answer with the same error.
	n, err := strconv.ParseInt(tid[1:], 10, 64)
	if err != nil || n > maxThreadSuffix {
		return fmt.Errorf("bad thread id %q: number must be at most %d", tid, maxThreadSuffix)
	}
	return nil
}

// validateThreadModel accepts the four aliases and "" — no pick at all, which
// is what every row branched before the picker carries and what a caller who
// left the choice to the job's model: field sends. The error names all four
// because it reaches the client verbatim (the API turns it into a 400), and a
// typo'd alias is the whole failure mode worth spelling out.
func validateThreadModel(model string) error {
	if model == "" || validThreadModel[model] {
		return nil
	}
	return fmt.Errorf("bad thread model %q: want haiku, sonnet, opus or fable", model)
}

func validateThreadState(state string) error {
	if !validThreadState[state] {
		return fmt.Errorf("bad thread state %q: want live or closed", state)
	}
	return nil
}

// threadColumns COALESCEs every plain-scanned column defensively, same
// belt-and-suspenders reasoning as jobColumns/feedbackColumns: the schema
// already declares all of these NOT NULL with a same-typed default, but a
// NULL that sneaks in anyway (schema drift, a hand-edited row) must degrade
// to a blank field rather than error the whole query. "window" is quoted at
// every use site in this file for the reason schemaV7's DDL spells out
// (schema.go): WINDOW is a SQLite keyword.
const threadColumns = `job_id, COALESCE(tid,''), COALESCE(title,''), COALESCE(session_id,''),
	COALESCE(parent_session,''), COALESCE(fork_uuid,''), COALESCE(cwd,''),
	COALESCE(pane,''), COALESCE("window",''), COALESCE(state,''), COALESCE(seed,''),
	COALESCE(model,''), COALESCE(role,'ask'), COALESCE(turn_uuid,''), COALESCE(created_at,0), COALESCE(closed_at,0),
	COALESCE(kind,'pane'), COALESCE(origin_node,''), COALESCE(origin_label,''),
	COALESCE(origin_login,''), COALESCE(answering,0), COALESCE(note,'')`

func scanThread(row interface{ Scan(...any) error }) (*Thread, error) {
	var t Thread
	var answering int64
	if err := row.Scan(&t.JobID, &t.TID, &t.Title, &t.SessionID, &t.ParentSession,
		&t.ForkUUID, &t.CWD, &t.Pane, &t.Window, &t.State, &t.Seed, &t.Model,
		&t.Role, &t.TurnUUID, &t.CreatedAt, &t.ClosedAt, &t.Kind, &t.OriginNode,
		&t.OriginLabel, &t.OriginLogin, &answering, &t.Note); err != nil {
		return nil, err
	}
	t.Answering = answering != 0
	return &t, nil
}

// ListThreads returns jobID's threads in tid order — t1, t2, ... t9, t10 —
// oldest first.
//
// The ORDER BY is `id`, NOT `tid`: tid is TEXT, so a lexicographic sort puts
// "t10" between "t1" and "t2", which is exactly the wrong answer once a job
// passes nine threads. `id` is the AUTOINCREMENT rowid, so ordering by it is
// insertion order BY CONSTRUCTION, and insertion order is numeric tid order
// because a tid is only ever allocated as MAX(suffix)+1 — a later row always
// carries a higher number. It also costs no CAST/SUBSTR arithmetic.
//
// It is deliberately not `created_at, id`: CreatedAt is a caller-settable
// field (an importer replaying history, a test fixture) and even the stamped
// path only reads a clock NTP can step backwards, so a created_at-first sort
// is equivalent to tid order only while that column happens to be monotonic.
// `id` needs no such assumption.
func (s *Store) ListThreads(jobID int64) ([]Thread, error) {
	rows, err := s.db.Query(
		`SELECT `+threadColumns+` FROM threads WHERE job_id = ? ORDER BY id`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list threads for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, fmt.Errorf("list threads for job %d: %w", jobID, err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// GetThread looks up one thread by its (job_id, tid) key, returning nil and
// ErrNotFound when no row matches — so the API layer can surface an
// unknown-thread error rather than serving a zero value. Pointer-returning
// like every other single-row getter in this package (GetFeedback,
// GetJobBySlug, GetArtifact, GetDraft, GetUpload): same shape, same
// sql.ErrNoRows → ErrNotFound translation, so callers can use one idiom.
func (s *Store) GetThread(jobID int64, tid string) (*Thread, error) {
	row := s.db.QueryRow(`SELECT `+threadColumns+` FROM threads WHERE job_id = ? AND tid = ?`, jobID, tid)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get thread %s for job %d: %w", tid, jobID, err)
	}
	return t, nil
}

// GetThreadBySession finds the thread row whose claude session is sessionID,
// in ANY state — a closed mind thread still owns the items it committed, so
// the caller decides what a closed row means. (nil, nil) when no row matches,
// not ErrNotFound: the ordinary caller is asking "is this session a thread at
// all?", and for zeus or a teammate the answer is simply no.
//
// An empty sessionID matches nothing rather than every row whose session is
// not yet recorded. Should several rows ever share one session, the newest
// wins — the FindLiveMMItem tiebreak.
func (s *Store) GetThreadBySession(sessionID string) (*Thread, error) {
	if sessionID == "" {
		return nil, nil
	}
	row := s.db.QueryRow(`SELECT `+threadColumns+` FROM threads WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get thread by session %s: %w", sessionID, err)
	}
	return t, nil
}

// normalizeThread applies the two convenience stamps and the two defaults
// every insert path shares, then validates. Returns the row as it will
// actually be stored, which is what AddThreadNext hands back to its caller.
//
// The ClosedAt stamp mirrors CreatedAt's (the AddFeedback precedent), and it
// is a correctness fix rather than a convenience: a row inserted as
// State:"closed" with ClosedAt left 0 stores state='closed', closed_at=0,
// which directly contradicts the column's documented meaning ("0 means still
// live") — anything deriving liveness from ClosedAt == 0 reads that thread
// as live, and a UI renders it as the epoch. Pane/Window are cleared on the
// same path for exactly CloseThread's reason: tmux recycles pane ids, so a
// closed row holding coordinates would point the roll call (and any send or
// kill derived from it) at a stranger's pane.
func normalizeThread(t Thread) (Thread, error) {
	if err := validateThreadID(t.TID); err != nil {
		return t, err
	}
	if t.State == "" {
		t.State = "live"
	}
	if err := validateThreadState(t.State); err != nil {
		return t, err
	}
	// the model is validated on the way IN and never again: a row that is
	// already stored keeps whatever alias it was branched with even if the
	// menu later loses that entry, because the alternative — refusing to READ
	// the row — would make a retired tier hide a live thread from the board.
	if err := validateThreadModel(t.Model); err != nil {
		return t, err
	}
	if t.Role == "" {
		t.Role = "ask"
	}
	if !validThreadRole[t.Role] {
		return t, fmt.Errorf("bad thread role %q: want ask or mind", t.Role)
	}
	if t.Kind == "" {
		t.Kind = "pane"
	}
	if t.Kind != "pane" && t.Kind != "headless" {
		return t, fmt.Errorf("bad thread kind %q: %w", t.Kind, ErrThreadKind)
	}
	if t.Kind == "headless" && t.Role == "mind" {
		return t, fmt.Errorf("thread %s for job %d: a mind thread cannot be headless: %w", t.TID, t.JobID, ErrThreadKind)
	}
	if t.Kind == "headless" && (t.Pane != "" || t.Window != "") {
		return t, fmt.Errorf("thread %s for job %d: %w", t.TID, t.JobID, ErrAskThreadPane)
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	if t.State == "closed" {
		if t.ClosedAt == 0 {
			t.ClosedAt = time.Now().UnixMilli()
		}
		t.Pane, t.Window = "", ""
	}
	return t, nil
}

// insertThreadTx is the shared INSERT behind AddThread and AddThreadNext. t
// must already have been through normalizeThread. It takes the package's
// execer (store.go) rather than a concrete handle so one body serves both the
// unwrapped verb (its own implicit transaction, via s.db) and AddThreadNext's
// caller-owned transaction — the same reason every other *Tx helper here
// takes it.
//
// A duplicate (job_id, tid) is refused with ErrThreadIDTaken rather than a
// bare UNIQUE-constraint string: the INSERT ... SELECT ... WHERE NOT EXISTS
// makes the check and the insert one atomic statement, so the answer can't
// go stale between them the way a separate SELECT-then-INSERT could, and a
// zero RowsAffected means exactly one thing — the tid is already taken.
func insertThreadTx(x execer, t Thread) error {
	res, err := x.Exec(
		`INSERT INTO threads (job_id, tid, title, session_id, parent_session, fork_uuid,
			cwd, pane, "window", state, seed, model, role, turn_uuid, created_at, closed_at,
			kind, origin_node, origin_label, origin_login, answering, note)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM threads WHERE job_id = ? AND tid = ?)`,
		t.JobID, t.TID, t.Title, t.SessionID, t.ParentSession, t.ForkUUID,
		t.CWD, t.Pane, t.Window, t.State, t.Seed, t.Model, t.Role, t.TurnUUID, t.CreatedAt, t.ClosedAt,
		t.Kind, t.OriginNode, t.OriginLabel, t.OriginLogin, t.Answering, t.Note,
		t.JobID, t.TID,
	)
	if err != nil {
		return fmt.Errorf("add thread %s for job %d: %w", t.TID, t.JobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("add thread %s for job %d: %w", t.TID, t.JobID, err)
	}
	if n == 0 {
		return fmt.Errorf("add thread %s for job %d: %w", t.TID, t.JobID, ErrThreadIDTaken)
	}
	return nil
}

// AddThread inserts one thread row at a tid the CALLER picked, stamping
// CreatedAt to now when it was left zero, defaulting an empty State to
// "live", and — for a row inserted already closed — stamping ClosedAt and
// clearing pane/window (see normalizeThread). A tid already used by this job
// is refused with ErrThreadIDTaken.
//
// NOT the creation path: pairing this with NextThreadID is RACY, because the
// read and the write are two statements against a database that kraken serve
// hits from concurrent HTTP handlers — two branches of one job can both read
// MAX(suffix)=0, both be told "t1", and one insert then loses. Use
// AddThreadNext, which allocates and inserts in a single transaction. This
// verb survives for the callers that legitimately supply their own tid: an
// importer replaying history, and tests pinning a specific numbering.
func (s *Store) AddThread(t Thread) error {
	t, err := normalizeThread(t)
	if err != nil {
		return err
	}
	return insertThreadTx(s.db, t)
}

// AddThreadNext allocates the job's next tid and inserts the row in ONE
// transaction, so two concurrent branches can never receive the same tid.
// Any TID the caller set is ignored — the allocation is the point. Returns
// the row as stored, TID populated.
//
// This is the creation path, and the transaction is not decoration. kraken
// serve answers HTTP concurrently, so the NextThreadID-then-AddThread pair
// interleaves: both requests read MAX(suffix)=0, both get "t1", and the
// loser's insert fails — but its caller (jobs.BranchThread) has by then
// already staged the spawn files under that tid and opened a tmux window, so
// the loser's writes stomp the winner's and one thread comes up briefed with
// the other's title and seed. Allocating inside the transaction removes the
// window entirely: _txlock=immediate (see the DSN in store.go) makes Begin
// take the write lock up front, so the second transaction waits for the
// first to commit and then reads a MAX that already includes it.
//
// Allocation uses the same MAX(suffix)+1 rule as NextThreadID, so a tid is
// still never reused — the reasoning is in that function's comment.
func (s *Store) AddThreadNext(t Thread) (Thread, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Thread{}, fmt.Errorf("add thread for job %d: %w", t.JobID, err)
	}
	defer tx.Rollback()

	tid, err := nextThreadIDTx(tx, t.JobID)
	if err != nil {
		return Thread{}, err
	}
	t.TID = tid
	if t, err = normalizeThread(t); err != nil {
		return Thread{}, err
	}
	if err := insertThreadTx(tx, t); err != nil {
		return Thread{}, err
	}
	if err := tx.Commit(); err != nil {
		return Thread{}, fmt.Errorf("add thread %s for job %d: %w", t.TID, t.JobID, err)
	}
	return t, nil
}

// SetThreadTitle renames one thread, leaving every other column alone — a
// thread's title is the only field carl edits by hand after the fork.
// ErrNotFound when the tid doesn't exist for jobID.
func (s *Store) SetThreadTitle(jobID int64, tid, title string) error {
	res, err := s.db.Exec(`UPDATE threads SET title = ? WHERE job_id = ? AND tid = ?`, title, jobID, tid)
	if err != nil {
		return fmt.Errorf("set title of thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set title of thread %s for job %d: %w", tid, jobID, err)
	}
	if n == 0 {
		return fmt.Errorf("set title of thread %s for job %d: %w", tid, jobID, ErrNotFound)
	}
	return nil
}

// ClaimClosedThread flips a CLOSED row to live — clearing the stale tmux
// coordinates it was closed with and zeroing closed_at — and is the gate a
// revive must pass BEFORE it spawns anything.
//
// It exists because the obvious shape of revive is a TOCTOU: scan discovery
// for a live pane, decide the thread is gone, open a tmux window, record
// where it landed. kraken serve is net/http, which runs handlers
// CONCURRENTLY and holds no lock across that path, so two revives of one
// thread — a double-tapped board button, the board and the phone at once —
// both pass the scan, both spawn, and the last writer simply overwrites
// pane/window. The rows look fine afterwards; what is actually running is two
// `claude -r <same session id>` processes, two writers on one transcript,
// which this feature's design calls the unrecoverable outcome.
//
// So the DATABASE decides the winner, not the scan. The state='closed'
// predicate makes this one atomic compare-and-set: of N concurrent claims
// exactly one updates a row, and the losers get ErrThreadNotClosed and must
// spawn NOTHING. RowsAffected()==0 covers two different situations, which the
// follow-up read separates — a missing tid is ErrNotFound (a caller bug, a
// 404), an already-live row is ErrThreadNotClosed (the race working as
// designed) — because a caller that can't tell them apart can't tell "unknown
// thread" from "already open" in the UI.
//
// Pane/window are cleared rather than set here: the claim happens before the
// tmux window exists, so the coordinates aren't known yet. The winner calls
// SetThreadPane once it has them, and in the gap the row is live-but-paneless
// — the same state a freshly branched row sits in, and one LiveThreadPanes
// already skips. Identity and provenance (session_id, parent_session,
// fork_uuid, seed) are untouched throughout: reviving resumes the same claude
// session, it does not fork a new one.
func (s *Store) ClaimClosedThread(jobID int64, tid string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET state = 'live', pane = '', "window" = '', closed_at = 0
			WHERE job_id = ? AND tid = ? AND state = 'closed'`,
		jobID, tid,
	)
	if err != nil {
		return fmt.Errorf("claim thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim thread %s for job %d: %w", tid, jobID, err)
	}
	if n == 0 {
		// Zero rows means the row was missing OR already live. Read it back
		// to say which. This read is deliberately NOT part of the decision —
		// the UPDATE already settled who won — so it being a separate
		// statement introduces no race: whatever it finds, this caller has
		// lost either way, and the only question left is which error to
		// report.
		if _, gerr := s.GetThread(jobID, tid); gerr != nil {
			return fmt.Errorf("claim thread %s for job %d: %w", tid, jobID, gerr)
		}
		return fmt.Errorf("claim thread %s for job %d: %w", tid, jobID, ErrThreadNotClosed)
	}
	return nil
}

// SetThreadPane records where a live thread's tmux window actually landed,
// leaving every other column alone. Both creation paths end here: branch
// (after AddThreadNext has reserved the row) and revive (after
// ClaimClosedThread has won the race), because in both the row is written
// before the window exists and the coordinates only become knowable after the
// spawn.
//
// It deliberately does NOT touch state: the row is already live by the time
// anyone has coordinates to record, and a verb that could flip state here
// would be exactly the unconditional open that let two revives both "succeed".
// ErrNotFound when the tid doesn't exist for jobID; ErrAskThreadPane when the
// row is headless and a non-empty pane or window was passed (the kind test is
// in the UPDATE's own WHERE, so it cannot go stale between a read and the
// write).
func (s *Store) SetThreadPane(jobID int64, tid, pane, window string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET pane = ?, "window" = ? WHERE job_id = ? AND tid = ?
			AND (kind != 'headless' OR (? = '' AND ? = ''))`,
		pane, window, jobID, tid, pane, window,
	)
	if err != nil {
		return fmt.Errorf("set pane of thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set pane of thread %s for job %d: %w", tid, jobID, err)
	}
	if n == 0 {
		// missing row or a headless row; the read only picks which error to name
		if _, gerr := s.GetThread(jobID, tid); gerr != nil {
			return fmt.Errorf("set pane of thread %s for job %d: %w", tid, jobID, gerr)
		}
		return fmt.Errorf("set pane of thread %s for job %d: %w", tid, jobID, ErrAskThreadPane)
	}
	return nil
}

// ClaimThreadAnswering marks a live thread as answering, clearing its note —
// the gate every ask run passes BEFORE its process starts. One
// compare-and-set, for ClaimClosedThread's reason: two sends racing on one
// thread must not both start a `claude -r` on its session id, so the
// answering = 0 predicate decides the winner and the loser gets
// ErrThreadAnswering having changed nothing. A closed row is ErrThreadClosed
// and an unknown tid ErrNotFound. The sentinels are returned bare, not
// wrapped, so a caller may hand them on as they are.
func (s *Store) ClaimThreadAnswering(jobID int64, tid string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET answering = 1, note = ''
			WHERE job_id = ? AND tid = ? AND state = 'live' AND answering = 0`,
		jobID, tid,
	)
	if err != nil {
		return fmt.Errorf("claim answering on thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim answering on thread %s for job %d: %w", tid, jobID, err)
	}
	if n == 1 {
		return nil
	}
	// lost or refused; the read names which, it decides nothing
	t, gerr := s.GetThread(jobID, tid)
	switch {
	case errors.Is(gerr, ErrNotFound):
		return ErrNotFound
	case gerr != nil:
		return gerr
	case t.State != "live":
		return ErrThreadClosed
	default:
		return ErrThreadAnswering
	}
}

// FinishThreadAnswering clears the answering flag and records note ("" clears
// it) whatever state the row is in: a run that ends after its thread was
// closed still has to drop the flag. ErrNotFound for an unknown tid.
func (s *Store) FinishThreadAnswering(jobID int64, tid, note string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET answering = 0, note = ? WHERE job_id = ? AND tid = ?`,
		note, jobID, tid,
	)
	if err != nil {
		return fmt.Errorf("finish answering on thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish answering on thread %s for job %d: %w", tid, jobID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearStaleThreadAnswering is FinishThreadAnswering for a row whose run is
// believed lost: it writes only where answering = 1 still holds at write
// time, so it never overwrites the outcome a run recorded after the caller
// read the row. Reports whether it wrote; an unknown tid, or a row no longer
// answering, writes nothing and is not an error.
func (s *Store) ClearStaleThreadAnswering(jobID int64, tid, note string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE threads SET answering = 0, note = ? WHERE job_id = ? AND tid = ? AND answering = 1`,
		note, jobID, tid,
	)
	if err != nil {
		return false, fmt.Errorf("clear stale answering on thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("clear stale answering on thread %s for job %d: %w", tid, jobID, err)
	}
	return n == 1, nil
}

// CountLiveThreads is how many of jobID's rows are live and of kind kind
// ("pane" or "headless").
func (s *Store) CountLiveThreads(jobID int64, kind string) (int, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM threads WHERE job_id = ? AND state = 'live' AND kind = ?`,
		jobID, kind,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count live %s threads for job %d: %w", kind, jobID, err)
	}
	return n, nil
}

// CloseThread marks a thread closed and CLEARS its pane/window, stamping
// closedAt (now, when the caller passes 0 — same convenience as AddThread's
// CreatedAt). Clearing the coordinates is the point, not incidental: a
// closed thread's old pane id gets recycled by tmux for something else
// entirely, so leaving it behind would make LiveThreadPanes' roll call (and
// any send/kill target derived from it) point at a stranger's pane. The row
// itself survives — session_id still locates the transcript, so a closed
// thread stays readable and can be revived later (ClaimClosedThread, then
// SetThreadPane once the new window exists). It also drops a headless row's
// answering flag, since a closed thread has no run to wait on; its note is
// kept.
// ErrNotFound when the tid doesn't exist for jobID.
func (s *Store) CloseThread(jobID int64, tid string, closedAt int64) error {
	if closedAt == 0 {
		closedAt = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`UPDATE threads SET state = 'closed', pane = '', "window" = '', closed_at = ?, answering = 0
			WHERE job_id = ? AND tid = ?`,
		closedAt, jobID, tid,
	)
	if err != nil {
		return fmt.Errorf("close thread %s for job %d: %w", tid, jobID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("close thread %s for job %d: %w", tid, jobID, err)
	}
	if n == 0 {
		return fmt.Errorf("close thread %s for job %d: %w", tid, jobID, ErrNotFound)
	}
	return nil
}

// NextThreadID returns the tid to give jobID's next thread: t1, t2, t3, ...
//
// RACY as a creation path when paired with AddThread: the read and the
// insert are two statements, so two concurrent callers can be handed the
// same tid (see AddThreadNext, which fuses them into one transaction and is
// what branch creation must use). Kept for read-only callers — showing what
// the next thread would be called, and tests.
//
// Derived from MAX(numeric suffix) + 1 over the job's EXISTING rows, never
// from a count: a count reuses a tid the moment any row is deleted — with
// {t1, t2} and t1 gone, counting hands out "t2", the name a live thread
// already answers to. Taking the max instead means a gap left by a deleted
// row stays a gap, so no tid ever names two different forked sessions.
//
// The one number this does free is the highest one, if that exact row is
// hard-deleted: nothing in this package deletes a thread row (close is a
// state change, and the only cascade is the job's own hard-delete, which
// takes every tid with it and correctly restarts at t1), so within kraken's
// own verbs a tid is never recycled at all.
//
// The suffix arithmetic is done in SQL (SUBSTR past the leading 't', CAST to
// INTEGER) so this stays one query no matter how many threads a job has.
// Every tid in the table passed validateThreadID on the way in, so the
// substring is always digits with no leading zero AND at most
// maxThreadSuffix — that magnitude bound is what keeps the CAST from
// clamping at int64 max and max+1 from wrapping negative into a tid that can
// never validate again. The explicit check below is the belt to that
// suspenders: a row that arrived by some other route (schema drift, a
// hand-edited database) gets a real error instead of silently wedging the
// job's counter. An empty table COALESCEs to 0 and yields t1.
func (s *Store) NextThreadID(jobID int64) (string, error) {
	return nextThreadIDTx(s.db, jobID)
}

func nextThreadIDTx(x execer, jobID int64) (string, error) {
	var max int64
	if err := x.QueryRow(
		`SELECT COALESCE(MAX(CAST(SUBSTR(tid, 2) AS INTEGER)), 0) FROM threads WHERE job_id = ?`,
		jobID,
	).Scan(&max); err != nil {
		return "", fmt.Errorf("next thread id for job %d: %w", jobID, err)
	}
	if max < 0 || max >= maxThreadSuffix {
		return "", fmt.Errorf("next thread id for job %d: thread counter out of range (max suffix %d)", jobID, max)
	}
	return fmt.Sprintf("t%d", max+1), nil
}

// LiveThreadPanes returns the set of tmux pane ids held by live threads
// across ALL jobs, empty panes excluded. This feeds the daemon's per-tick
// roll call — a hot path that only ever asks "is this pane one of ours?" —
// so it is deliberately one unscoped query returning a set, rather than a
// per-job ListThreads loop the caller would have to flatten itself.
func (s *Store) LiveThreadPanes() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT pane FROM threads WHERE state = 'live' AND pane != ''`)
	if err != nil {
		return nil, fmt.Errorf("list live thread panes: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var pane string
		if err := rows.Scan(&pane); err != nil {
			return nil, fmt.Errorf("list live thread panes: %w", err)
		}
		out[pane] = true
	}
	return out, rows.Err()
}
