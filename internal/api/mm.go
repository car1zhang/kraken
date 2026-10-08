package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// mm.go — the /api/mm route family: the ONLY write path into carl's mental
// model (mm_items + mm_code_anchors, internal/store/mm.go). A mental-model
// item is one thing carl has read, decided, or noted, frozen at the moment he
// marked it — a question he answered, an artifact or an element of one he
// read, a range of code he read, a free note.
//
// The frozen part is the whole feature, and it is why every source carries
// its own anchor: an artifact mark keeps a SNAPSHOT of the bytes (served by
// GET /api/mm/snapshot, never by the list), an element mark keeps the
// element's own text, and a code mark keeps the blob's text plus the commit
// and line range it was read at. The verify pass (internal/jobs/mm.go) then
// says whether what carl read is still there; when it is not, the item is
// invalidated ONE-WAY and the frozen copy is what the board renders as "what
// you knew".
//
// Route posture, per slice: GET is ungated (a read is a read, and a mirror
// board must be able to show the model it cannot write), and every POST here
// is a POST-ONLY path, so each is wrapped with mirrorGated at REGISTRATION
// rather than gating inline — the legal form when there is no GET on the same
// path for a route wrapper to take down with it (register.go). The two GETs
// live on their own paths for exactly that reason.
//
// Decode discipline is jobload.go's, wholesale: a 1 MB MaxBytesReader (413,
// distinct from a malformed body's 400), DisallowUnknownFields with a refusal
// that NAMES the accepted field set and never echoes the offending key (it
// comes from the body, so quoting it would let a caller choose the size of the
// response), and an empty required field refused by name.
//
// The board's hot payload is deliberately untouched by all of this: no key on
// webJob, no webBoardCache.invalidate() on any write here. The mental model is
// its own view with its own poll, the library's posture (library.go) — putting
// it on the board's payload would make every card rebuild carry a list nothing
// on a card renders.

// The item kinds and sources, spelled out here as the names the routes write
// rather than left as literals at seven call sites. store.AddMMItem validates
// both sets independently — these constants are what keeps the HTTP layer
// writing the spellings that validation accepts.
const (
	mmKindDecision = "decision"
	mmKindReading  = "reading"
	mmKindNote     = "note"

	mmSourceQuestion        = "question"
	mmSourceArtifact        = "artifact"
	mmSourceArtifactElement = "artifact-element"
	mmSourceCode            = "code"
	mmSourceAgentOutput     = "agent-output"
	mmSourceManual          = "manual"
)

// The text caps, in RUNES rather than bytes: a cap counted in bytes would
// refuse a note two-thirds the documented length the moment it carried
// multi-byte characters, which is most prose carl would actually paste.
//
// An element is capped an order of magnitude higher than a note because it is
// a SELECTION out of a rendered artifact — a whole table or a long figure
// caption — while a note and a marked detail are things carl or an agent
// typed.
//
// The numbers are DECIMAL, not binary: `feedbackNoteMaxLen = 8000` and
// `docAnswerMaxLen = 4000` are the house precedent, and a cap documented as
// "8K runes" that actually accepts 8192 is a cap whose stated value and real
// value disagree — which is the one thing a boundary can be wrong about.
const (
	mmElementMaxRunes = 32000
	mmNoteMaxRunes    = 8000
	mmDetailMaxRunes  = 8000
)

// mmGitTimeout bounds every git call the /api/mm/code anchor ladder makes.
// The ladder runs INSIDE carl's request — the mark is not recorded until it
// resolves — so a wedged git (a stale index.lock, a filesystem that has gone
// away) has to fail the request rather than hold the connection open
// indefinitely. It is deliberately shorter than the verify pass' ~60s: that
// pass fetches from the network, this one only reads local objects.
const mmGitTimeout = 20 * time.Second

// mmAnchorGit is the swappable seam every git call on this surface goes
// through, so no api test shells out — the same discipline jobs' mmRunGit
// applies to the verify pass. It is a SECOND seam rather than a reuse of that
// one because jobs' is unexported and this package sits above it: two
// packages, two vars, one behavior frozen by the contract.
var mmAnchorGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// mmGit runs one git call under mmGitTimeout and returns its trimmed stdout.
// Trimming here rather than at each call site is what lets the callers treat
// the result as a value (a sha, a url) instead of remembering which git verbs
// end their output with a newline — and every caller on this surface wants
// the value, never the framing.
func mmGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mmGitTimeout)
	defer cancel()
	out, err := mmAnchorGit(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// --- the wire ---

// webMMAnchor is a code mark's anchor on the wire. The item's `content` IS
// the anchored text, so there is no `text` key here — carrying it twice would
// let a client render two spellings of one thing, and the contract's own
// column list names none.
//
// omitempty falls on exactly the fields that are legitimately absent: an
// `orig_path` only exists once a verify has followed a rename, `anchor_sha` is
// empty by DEFINITION on a dirty anchor (store.AddMMCodeAnchor refuses the two
// disagreeing), the `checked_*` trio is empty until the first verify pass has
// looked at it, and the `invalid_*` pair only after it has died. The rest are
// present on every anchor the store will accept, so they are unconditional.
type webMMAnchor struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	OrigPath  string `json:"orig_path,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	// AnchorSHA is empty exactly when AnchorKind is "dirty" — the commit the
	// text was located in, or nothing when it was located in no commit at all.
	AnchorSHA  string `json:"anchor_sha,omitempty"`
	AnchorKind string `json:"anchor_kind"`
	TextSHA    string `json:"text_sha"`
	CheckedAt  int64  `json:"checked_at,omitempty"` // unix millis
	CheckedSHA string `json:"checked_sha,omitempty"`
	// CheckError and an invalidation are DISJOINT outcomes: a failed read
	// (offline, repo gone, timeout) records this and never invalidates.
	CheckError string `json:"check_error,omitempty"`
	Ambiguous  bool   `json:"ambiguous,omitempty"`
	InvalidSHA string `json:"invalid_sha,omitempty"`
	InvalidPR  string `json:"invalid_pr,omitempty"`
}

// webMMItem is one mental-model item on the wire — the elements of GET
// /api/mm's "items" array. Additive: a NEW path with a new shape, no key
// added to, retyped in, or removed from anything a shipped client decodes, so
// apiContract stays 6.
//
// `invalidated_at` and the two `invalid_*` strings are omitempty together, and
// their ABSENCE is the wire's only spelling of "live" — the same rule
// webJob.load follows for "no estimate": one absence, no second boolean that
// could disagree with it. `anchor` is a pointer for the same reason a zeroed
// one would claim an anchor at line 0 of no file.
//
// The snapshot BYTES are deliberately not here: they are what
// GET /api/mm/snapshot is for, and a list that carried every frozen artifact
// would be megabytes on a poll.
type webMMItem struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Slug      string `json:"slug"`
	SourceRef string `json:"source_ref"`
	// Content is served WHOLE — never cut. It is what carl read, and the
	// invalidated view exists precisely to show it back to him in full.
	Content   string `json:"content"`
	Context   string `json:"context"`
	CreatedAt int64  `json:"created_at"` // unix millis
	// EpisodeID is vestigial — nothing writes it since the knowledge graph
	// was removed, so it is 0 on every new row — but it stays on the wire
	// because shipped iOS builds decode `episode_id` as a non-optional key.
	EpisodeID     int64        `json:"episode_id"`
	InvalidatedAt int64        `json:"invalidated_at,omitempty"` // unix millis
	InvalidReason string       `json:"invalid_reason,omitempty"`
	InvalidDetail string       `json:"invalid_detail,omitempty"`
	Anchor        *webMMAnchor `json:"anchor,omitempty"`
}

func webMMAnchorFrom(a store.MMCodeAnchor) *webMMAnchor {
	return &webMMAnchor{
		Repo: a.Repo, Path: a.Path, OrigPath: a.OrigPath,
		StartLine: a.StartLine, EndLine: a.EndLine,
		AnchorSHA: a.AnchorSHA, AnchorKind: a.AnchorKind, TextSHA: a.TextSHA,
		CheckedAt: a.CheckedAt, CheckedSHA: a.CheckedSHA, CheckError: a.CheckError,
		Ambiguous: a.Ambiguous, InvalidSHA: a.InvalidSHA, InvalidPR: a.InvalidPR,
	}
}

func webMMItemFrom(it store.MMItem, anchor *store.MMCodeAnchor) webMMItem {
	out := webMMItem{
		ID: it.ID, Kind: it.Kind, Source: it.Source, Slug: it.Slug,
		SourceRef: it.SourceRef, Content: it.Content, Context: it.Context,
		CreatedAt: it.CreatedAt, EpisodeID: it.EpisodeID,
		InvalidatedAt: it.InvalidatedAt, InvalidReason: it.InvalidReason,
		InvalidDetail: it.InvalidDetail,
	}
	if anchor != nil {
		out.Anchor = webMMAnchorFrom(*anchor)
	}
	return out
}

// --- request bodies ---
//
// One struct per route rather than one fat body with an op field: each of
// these is reached by its own path, and a shared struct would make every
// route's DisallowUnknownFields accept the other six routes' keys — which is
// exactly the "accepted then silently ignored" outcome the strict decode
// exists to prevent.

type mmDecisionReq struct {
	Slug string `json:"slug"`
	QID  string `json:"qid"`
}

type mmArtifactReq struct {
	Slug string `json:"slug"`
	Home string `json:"home"`
	Name string `json:"name"`
}

type mmElementReq struct {
	Slug string `json:"slug"`
	Home string `json:"home"`
	Name string `json:"name"`
	Text string `json:"text"`
}

type mmCodeReq struct {
	Worktree string `json:"worktree"`
	File     string `json:"file"`
	StartRow int    `json:"start_row"`
	Text     string `json:"text"`
	Note     string `json:"note"`
}

type mmNoteReq struct {
	Text    string `json:"text"`
	Context string `json:"context"`
}

type mmDetailReq struct {
	Slug string `json:"slug"`
	Text string `json:"text"`
}

type mmRemoveReq struct {
	ID int64 `json:"id"`
}

// --- shared plumbing ---

// mmDecode is the one decode on this surface: 1 MB cap, DisallowUnknownFields,
// and a refusal that names `fields` — the accepted set for THIS route, written
// out at the call site so the sentence a caller reads matches the struct it
// actually failed against.
//
// The unknown-field case is detected by substring, which is the only handle
// encoding/json offers: it reports the condition as prose with no typed error
// to match on. A reworded stdlib message degrades this to the generic 400
// below — a worse sentence, never a different outcome (jobload.go measured the
// same thing). Note it does NOT catch a case variant: encoding/json matches
// fields case-insensitively and this option fires only when nothing matches at
// all, so `{"Slug":"x"}` decodes cleanly. That is benign here — every route on
// this surface writes a NEW row rather than clearing an existing one, so a
// spelling that lands on the right field with the right meaning is a request
// that meant what it said.
func mmDecode(w http.ResponseWriter, r *http.Request, into any, fields string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return false
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — this route takes only " + fields,
			})
			return false
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return false
	}
	return true
}

// mmRequire refuses the first named field that is blank after trimming,
// naming it. One at a time rather than "slug, home and name are required":
// every one of these comes from a client assembling a request it already has
// the parts of, so the one it dropped is the whole content of the refusal.
func mmRequire(w http.ResponseWriter, pairs ...string) bool {
	for i := 0; i+1 < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing " + pairs[i]})
			return false
		}
	}
	return true
}

// mmStore is the store handle every handler here starts from, with the same
// 500 the rest of the package answers when nothing is wired.
func mmStore(w http.ResponseWriter) *store.Store {
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return nil
	}
	return st
}

// mmTextSHA is the content hash the idempotence probe keys on, for the four
// probed routes whose payload is TEXT rather than bytes (note has no probe —
// two identical notes are two things carl thought; its own site says so).
//
// It is a one-line adapter over store.SHA256Hex rather than its own sha, and
// that is the whole point: `FindLiveMMItem` takes a hash instead of a payload,
// so the value written into a row and the value looked up before writing one
// are computed by the same function or they are two functions that agree only
// until one of them is edited. The store exports SHA256Hex for exactly this
// caller.
func mmTextSHA(s string) string { return store.SHA256Hex([]byte(s)) }

// mmNormalize is the line-ending normalization every stored text goes through.
// CRLF reaches this surface from two real places — a Zed selection on a file
// with Windows endings, and a browser textarea — and a model whose stored text
// differs from the file's only in invisible bytes would invalidate itself on
// the first verify pass.
func mmNormalize(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// mmTooLong reports a rune-count overflow with the cap in the message, so a
// caller that has to split something knows what it is splitting to.
func mmTooLong(w http.ResponseWriter, field, s string, max int) bool {
	if n := len([]rune(s)); n > max {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("%s is %d runes, over the %d-rune limit", field, n, max),
		})
		return true
	}
	return false
}

// mmAlready answers the idempotent re-mark: the SAME thing marked twice is one
// item, and saying so is what lets every client fire the mark button without
// first checking whether it is already on. `already` is the key that separates
// it from a fresh create — a caller told only an id could not tell the two
// apart, and the two mean different things to a UI that has just animated a
// state change.
func mmAlready(w http.ResponseWriter, id int64) {
	webJSON(w, http.StatusOK, map[string]any{"id": id, "already": true})
}

// mmFindLive is the four-part idempotence probe, wrapped so a store failure is
// a 500 at one place rather than at six. It returns handled=true when it has
// already written a response.
func mmFindLive(w http.ResponseWriter, st *store.Store, source string, jobID int64, ref, sha string) (handled bool) {
	id, ok, err := st.FindLiveMMItem(source, jobID, ref, sha)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return true
	}
	if ok {
		mmAlready(w, id)
		return true
	}
	return false
}

// mmWriteMu serializes every find-live→(supersede)→create critical section on
// this surface, `jobs.SubmitJob`'s per-id-mutex precedent applied to the same
// shape of bug.
//
// The `already` contract is check-then-act: `FindLiveMMItem` and `AddMMItem`
// are two statements with no transaction between them, so two concurrent
// identical marks both miss the probe and both insert — two live items for one
// artifact, and a memory pane showing carl the same reading twice. The
// producers are real rather than theoretical: a Zed keybinding that
// double-fires, the CLI racing a board click, a second tab. The board's own
// `markingRead`/`markingSel` flags guard exactly one of those three.
//
// ONE global mutex rather than one per (source, job, ref), because the
// contended thing is a human pressing a button twice: the critical sections
// are single-digit milliseconds of sqlite on a store that has one connection
// and therefore already serializes them, and a keyed map of mutexes would add
// a lifetime problem to buy nothing measurable.
//
// It is deliberately NOT held across the code route's git ladder — see
// mmCodeHandler. Locking there would put a human keystroke behind up to six
// 20-second git calls belonging to someone else's keystroke.
var mmWriteMu sync.Mutex

// mmWrite is one create's whole payload. A struct rather than five positional
// parameters because four of the five are absent on most routes, and a
// positional (item, snapshot, hasSnapshot, anchor, afterInsert) is exactly the
// shape a caller silently passes in the wrong order.
type mmWrite struct {
	item   store.MMItem
	anchor *store.MMCodeAnchor
	// snapshot rides with hasSnapshot rather than alone, because a
	// ZERO-BYTE snapshot and NO snapshot are different facts and nil-vs-empty
	// is not a distinction to hang them on. An artifact that is genuinely
	// empty must still get a stored snapshot: without one the verify pass
	// skips the item forever (it reads the snapshot to compare against) and
	// GET /api/mm/snapshot 404s it. store.GetMMSnapshot's column is nullable
	// precisely so the two can be told apart, and a `len(snapshot) > 0` guard
	// here is what used to stop that being true.
	snapshot    []byte
	hasSnapshot bool
	// afterInsert runs once the item, its snapshot and its anchor are all
	// persisted. It exists for the artifact
	// route's supersede, which must not run until the replacement it is
	// replacing things FOR actually exists — see mmArtifactHandler. A failure
	// here is a 500 with the item already saved, which the `already` probe
	// makes safe to retry.
	afterInsert func(id int64) error
}

// mmCreate is the one write path: persist the item, attach its snapshot and
// anchor if it has them, run afterInsert, answer {"id"}.
//
// The item is persisted FIRST, so a later failure (snapshot, anchor,
// afterInsert) is a 500 with the item already saved, which is safe precisely
// because every route's `already` probe makes the retry idempotent.
//
// Callers hold mmWriteMu across their probe and this call.
func mmCreate(w http.ResponseWriter, st *store.Store, wr mmWrite) {
	it := wr.item
	id, err := st.AddMMItem(it)
	if err != nil {
		util.LogEvent("web", "error", "mm-create", "mm-create "+it.Source+": "+err.Error())
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	it.ID = id
	if wr.hasSnapshot {
		if err := st.SetMMSnapshot(id, wr.snapshot, it.SnapshotExt); err != nil {
			util.LogEvent("web", "error", "mm-create", "mm-snapshot: "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	if wr.anchor != nil {
		wr.anchor.ItemID = id
		if err := st.AddMMCodeAnchor(*wr.anchor); err != nil {
			util.LogEvent("web", "error", "mm-create", "mm-anchor: "+err.Error())
			webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if wr.afterInsert != nil {
		if err := wr.afterInsert(id); err != nil {
			util.LogEvent("web", "error", "mm-create", "mm-after: "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	util.LogEvent("web", "info", "mm-create", "mm-create "+it.Source+" "+strconv.FormatInt(id, 10))
	webJSON(w, http.StatusOK, map[string]any{"id": id})
}

// --- GET /api/mm ---

// mmListHandler serves {"items": [...]} newest first (created_at DESC, id
// DESC). `?live=1` narrows to items that have not been invalidated; anything
// else means EVERYTHING, invalidated included, because the invalidated ones
// are half the point — "what you knew" against what is there now.
//
// The array is always present and never null, B3's convention on every list
// route here: a client renders "nothing marked yet" from an empty array, and a
// null would make every consumer write a second nil check for a state that is
// not different.
//
// A GET is a read, so it stays reachable under --mirror.
func mmListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	live := r.URL.Query().Get("live") == "1"
	// ?file= narrows to CODE marks whose anchor names that path — the read an
	// agent makes before explaining a file, citing it, or briefing a diagram
	// over it ("which exact lines has carl read here"). Notes and artifact
	// marks have no path, so under the filter they are simply not the
	// question being asked. ?worktree= (an absolute local path) resolves the
	// repo half through mmRepoName — the SAME identity rule the mark route
	// applies to its own worktree — so a filtered read from a cell worktree
	// matches marks made in the primary checkout, exactly as the marks
	// themselves do. Without it, the path matches alone, with `repo/path`
	// accepted for hand-typed disambiguation.
	fileFilter := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(r.URL.Query().Get("file"))), "./")
	repoFilter := ""
	if fileFilter != "" {
		if wt := strings.TrimSpace(r.URL.Query().Get("worktree")); wt != "" {
			repoFilter = mmRepoName(wt)
		}
	}
	rows, err := st.ListMMItems(!live)
	if err != nil {
		util.LogEvent("web", "error", "mm-list", "mm-list: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// TWO queries regardless of how many code marks there are, never one per
	// item: this is the read every open surface makes on a 30s poll, and it is
	// the one that grows fastest as carl marks code.
	//
	// ListMMCodeAnchors rather than ListLiveMMCodeAnchors — the latter joins on
	// the item being live, and an INVALIDATED code mark is exactly the row
	// whose anchor a reader most wants (which commit killed it, which PR). Both
	// queries run to whole slices before the loop starts, which is what keeps
	// the store's single connection out of a nested-query deadlock (a second
	// query issued while the first's rows are still open would wait on the
	// one connection forever).
	//
	// An anchor read that fails is not fatal to the list: the items are the
	// payload and an anchor is decoration, so a broken anchor table costs the
	// `anchor` objects rather than the whole memory pane.
	byItem := map[int64]store.MMCodeAnchor{}
	if anchors, err := st.ListMMCodeAnchors(); err == nil {
		for _, a := range anchors {
			byItem[a.ItemID] = a
		}
	} else {
		util.LogEvent("web", "error", "mm-list", "mm-list anchors: "+err.Error())
		// Decoration on the plain list, but the SELECTOR under ?file= — an
		// unreadable anchor table there would return an empty list that reads
		// as "carl has read nothing here", which is the one wrong answer.
		if fileFilter != "" {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "code anchors unreadable: " + err.Error()})
			return
		}
	}
	items := make([]webMMItem, 0, len(rows))
	for _, it := range rows {
		var anchor *store.MMCodeAnchor
		if a, ok := byItem[it.ID]; ok {
			anchor = &a
		}
		if fileFilter != "" && (anchor == nil || !mmAnchorMatchesFile(*anchor, repoFilter, fileFilter)) {
			continue
		}
		items = append(items, webMMItemFrom(it, anchor))
	}
	webJSON(w, http.StatusOK, map[string]any{"items": items})
}

// mmAnchorMatchesFile is `?file=` deciding whether one code anchor names the
// asked-about file. With a repo (derived from ?worktree=) both halves of the
// identity must match; without one the path alone decides, with the combined
// `repo/path` spelling accepted so a hand query can disambiguate a name like
// `README.md` that exists in every repo.
func mmAnchorMatchesFile(a store.MMCodeAnchor, repo, file string) bool {
	if repo != "" {
		return a.Repo == repo && a.Path == file
	}
	return a.Path == file || a.Repo+"/"+a.Path == file
}

// --- GET /api/mm/snapshot?id=N ---

// mmSnapshotHandler serves one artifact mark's FROZEN bytes — never the live
// artifact, which is the whole reason the snapshot exists: the agent that owns
// an artifact rewrites it in place and the cell dir goes at teardown, so an
// item that followed the live file would be a bookmark whose target keeps
// changing (library.go's argument, applied to a mark instead of a save).
//
// The content type comes from the STORED extension, through the same
// extension → type mapping the live artifact route uses, so a marked artifact
// renders the way carl was looking at it. An item with no snapshot and an
// unknown id are both 404: neither names frozen bytes.
func mmSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing or non-numeric id"})
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	data, ext, err := st.GetMMSnapshot(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", artifactMimeType("snapshot."+ext))
	// Harmless rather than required — these bytes are frozen and can never
	// change under the id — but it costs nothing and keeps every artifact-ish
	// response on this surface answering the same way.
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(data); err != nil {
		util.LogEvent("web", "error", "mm-snapshot", "mm-snapshot write: "+err.Error())
	}
}

// --- POST /api/mm/decision ---

// mmDecisionHandler marks an ANSWERED question as a decision carl has taken.
// The server copies the row's own four fields rather than taking text from the
// client: the point of the mark is that the decision is a durable record of
// what was actually asked and answered, and a client-supplied restatement
// would be a record of what a client believed at render time.
//
// An UNANSWERED question is refused: there is no decision yet, and marking one
// would freeze a question as though it had been settled.
func mmDecisionHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmDecisionReq
	if !mmDecode(w, r, &req, "slug and qid") {
		return
	}
	if !mmRequire(w, "slug", req.Slug, "qid", req.QID) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	j := resolveJobBySlug(strings.TrimSpace(req.Slug))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	qid := strings.TrimSpace(req.QID)
	var q *store.DocQuestion
	for i := range qs {
		if qs[i].QID == qid {
			q = &qs[i]
			break
		}
	}
	if q == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown question " + strconv.Quote(qid)})
		return
	}
	if strings.TrimSpace(q.Answer) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": qid + " has no answer yet — there is no decision to mark until carl has answered it",
		})
		return
	}
	// The recommendation rides along only when there IS one, and after the
	// answer rather than before it: what carl decided is the record, and the
	// recommendation is context for why that answer reads the way it does.
	content := q.Text + "\n→ " + q.Answer
	if strings.TrimSpace(q.Recommendation) != "" {
		content += "\nrecommendation: " + q.Recommendation
	}
	mmWriteMu.Lock()
	defer mmWriteMu.Unlock()
	if mmFindLive(w, st, mmSourceQuestion, j.ID, qid, mmTextSHA(content)) {
		return
	}
	mmCreate(w, st, mmWrite{item: store.MMItem{
		Kind: mmKindDecision, Source: mmSourceQuestion,
		JobID: j.ID, Slug: feedbackJobKey(j), SourceRef: qid,
		Content: content, Context: q.Context,
		CreatedAt: time.Now().UnixMilli(),
	}})
}

// --- POST /api/mm/artifact ---

// mmArtifactHandler marks a WHOLE artifact read, freezing its current bytes.
//
// Three outcomes, and the middle one is the reason this route is not just an
// insert: the same artifact marked again with the SAME bytes is the same fact
// (`already`), while the same artifact marked again with DIFFERENT bytes is a
// new reading of a document that has moved on — so the earlier item is
// superseded rather than left beside it, and the list never shows carl two
// live "I read this" rows for one file that can only be in one state.
//
// Q3's rule is what makes that comparison exact: ANY byte change invalidates,
// so the probe is a sha of the snapshot bytes rather than of a normalized
// rendering of them.
func mmArtifactHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmArtifactReq
	if !mmDecode(w, r, &req, "slug, home and name") {
		return
	}
	if !mmRequire(w, "slug", req.Slug, "home", req.Home, "name", req.Name) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	j := resolveJobForHome(req.Slug, req.Home)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	data, ext, ok := resolveArtifact(j, req.Home, req.Name)
	if !ok {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown artifact"})
		return
	}
	// The same trim every reader of an artifact name on this surface applies
	// (library.go's save, both artifact resolvers): "/00-overview.md" and
	// "00-overview.md" are one file, so without it they would be two source
	// refs the supersede could never match against each other.
	ref := strings.TrimPrefix(req.Name, "/")
	mmWriteMu.Lock()
	defer mmWriteMu.Unlock()
	if mmFindLive(w, st, mmSourceArtifact, j.ID, ref, store.SHA256Hex(data)) {
		return
	}
	now := time.Now().UnixMilli()
	mmCreate(w, st, mmWrite{
		item: store.MMItem{
			Kind: mmKindReading, Source: mmSourceArtifact,
			JobID: j.ID, Slug: feedbackJobKey(j), SourceRef: ref,
			// Content is the REF rather than the bytes: an artifact's content is
			// its snapshot, which is served by its own route and deliberately
			// never rides the list.
			Content: ref, SnapshotExt: ext,
			CreatedAt: now,
		},
		// Always stored, even at zero bytes: an artifact that is genuinely
		// empty is still a thing carl read, and an item with no snapshot is one
		// the verify pass skips forever and the snapshot route 404s.
		snapshot: data, hasSnapshot: true,
		// The supersede runs AFTER the replacement exists, excluding it. Doing
		// it first — the order the contract originally named — means a create
		// that fails at any of its three writes leaves carl's earlier reading
		// already invalidated with reason `superseded` and no replacement to
		// show for it: the mark is gone and the response is a 500 about
		// something else. Insert-then-supersede makes that unreachable, and
		// excludeID is what stops the new row superseding itself.
		afterInsert: func(id int64) error {
			_, err := st.SupersedeLiveMMItems(mmSourceArtifact, j.ID, ref, now, id)
			return err
		},
	})
}

// --- POST /api/mm/element ---

// mmElementHandler marks one ELEMENT of an artifact read — a prose selection,
// a table, a figure caption. The element's own text is the anchor: the verify
// pass asks whether that text is still PRESENT in the artifact — comparing
// alphanumeric projections of both sides, contiguously and then as a
// subsequence (`mmElementPresent`, internal/jobs/mm.go) — so an artifact
// rewritten around the element leaves the mark alone while a rewrite OF the
// element invalidates it. That is the whole difference from the whole-artifact
// mark above, where any byte anywhere counts.
//
// WHAT ARRIVES IS THE RENDERED CAPTURE, NOT THE SOURCE. The board hands over
// what the browser shows: a table selection is tab-separated cell text with no
// pipes, a list is item text with the markers the renderer generated. Stored
// as-is, that loses the structure carl marked — his gate-round complaint in
// one sentence — so the capture is used as a POINTER instead:
// core.LocateProjectedSpan finds where it came from in the artifact's markdown
// source and hands back whole source lines, which is what gets stored. Pipes,
// list markers and fences come back with them.
//
// A LOCATE THAT FAILS IS NOT AN ERROR. Nothing about this route requires the
// artifact to be resolvable or the capture to be findable — a mark on text
// that no longer matches the file, or on an artifact this process cannot read,
// still records something carl read. So every failure degrades to storing the
// rendered text exactly as it did before locating existed, and the route's
// refusals are unchanged: the only 4xx here are the ones that were always
// here. Everything downstream — the idempotence probe, the mint — keys on
// whatever content won, so it never learns which one that was.
//
// Verification is unchanged and gets STRICTER for free on a located mark:
// source lines are the artifact's own bytes, so they clear the verify pass'
// contiguity rung directly instead of falling through to the subsequence
// fallback that a rendered capture always needed — and a real rewrite of the
// passage kills the mark honestly rather than surviving on scattered runes.
func mmElementHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmElementReq
	if !mmDecode(w, r, &req, "slug, home, name and text") {
		return
	}
	if !mmRequire(w, "slug", req.Slug, "home", req.Home, "name", req.Name, "text", req.Text) {
		return
	}
	text := mmNormalize(req.Text)
	if mmTooLong(w, "text", text, mmElementMaxRunes) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	j := resolveJobForHome(req.Slug, req.Home)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	ref := strings.TrimPrefix(req.Name, "/")
	// The locate runs BEFORE the idempotence probe, because the probe keys on
	// a sha of the content and the located source lines are the content. Doing
	// it the other way round would probe with the rendered text, miss the row
	// the last identical mark stored as source lines, and insert a second live
	// item for the same reading.
	if data, _, ok := resolveArtifact(j, req.Home, req.Name); ok {
		src := mmNormalize(string(data))
		if start, end, located := core.LocateProjectedSpan(src, text); located {
			text = src[start:end]
		}
	}
	mmWriteMu.Lock()
	defer mmWriteMu.Unlock()
	if mmFindLive(w, st, mmSourceArtifactElement, j.ID, ref, mmTextSHA(text)) {
		return
	}
	// No supersede here, unlike the whole-artifact mark: two different
	// elements of one artifact are two different readings, and they are told
	// apart by their CONTENT rather than by their source ref, which they
	// share.
	mmCreate(w, st, mmWrite{item: store.MMItem{
		Kind: mmKindReading, Source: mmSourceArtifactElement,
		JobID: j.ID, Slug: feedbackJobKey(j), SourceRef: ref,
		Content: text, CreatedAt: time.Now().UnixMilli(),
	}})
}

// --- POST /api/mm/code ---

// mmCodeAnchor is one rung of the anchor ladder's result: where the text was
// found, and under what identity.
type mmCodeAnchor struct {
	kind       string
	sha        string
	start, end int
	text       string
	ambiguous  bool
}

// mmCodeHandler marks a range of CODE read. It is the route the Zed task
// drives, so its inputs are what an editor can cheaply hand over — the
// worktree, the file relative to it, the 1-based row the selection starts on,
// and the selection itself.
//
// The end row is DERIVED from the selection rather than sent: Zed closed
// ZED_END_ROW as not-planned, so the newline count is the only thing a task
// can supply, and deriving it in one place (core.EndRowFromSelection, shared
// with the CLI) is what keeps the editor's idea of the range and the server's
// identical.
//
// An empty selection means "the single line at start_row", read from disk
// here rather than defaulted to an empty mark: a keystroke with the cursor
// parked on a line is the commonest way this route is reached.
func mmCodeHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmCodeReq
	if !mmDecode(w, r, &req, "worktree, file, start_row, text and note") {
		return
	}
	if !mmRequire(w, "worktree", req.Worktree, "file", req.File) {
		return
	}
	worktree := strings.TrimSpace(req.Worktree)
	file := strings.TrimSpace(req.File)
	if !filepath.IsAbs(worktree) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "worktree must be an absolute path"})
		return
	}
	// A file path that is absolute or climbs out of the worktree is refused
	// rather than cleaned: the source ref is `<repo>/<file>`, so a path that
	// does not sit inside the repo names a line in a file this repo does not
	// have, and silently rewriting it would produce a mark nothing can verify.
	if filepath.IsAbs(file) || strings.HasPrefix(filepath.Clean(file), "..") {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "file must be a path relative to the worktree"})
		return
	}
	if req.StartRow < 1 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "start_row must be 1 or greater"})
		return
	}
	text := mmNormalize(req.Text)
	fromDisk := text == ""
	if fromDisk {
		line, ok := mmLineAt(filepath.Join(worktree, file), req.StartRow)
		if !ok {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("empty selection and no line %d in %s", req.StartRow, file),
			})
			return
		}
		text = line
	}
	// A blank line is refused HERE, naming the situation, rather than three
	// layers down where store.AddMMItem refuses it as "mm item content is
	// empty". This is reachable by the commonest keystroke there is — the Zed
	// task fired with the cursor parked on a blank line — and by the phantom
	// last line of any file ending in "\n", which splits to a trailing "". The
	// Zed task runs `hide: on_success`, so this message is the ONLY thing carl
	// sees, and a sentence naming a store column tells him nothing about what
	// he just pressed.
	if strings.TrimSpace(text) == "" {
		msg := "the selection is blank — nothing to mark"
		if fromDisk {
			msg = fmt.Sprintf("line %d of %s is empty — nothing to mark", req.StartRow, file)
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if mmTooLong(w, "text", text, mmElementMaxRunes) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}

	repo := mmRepoName(worktree)
	// The ladder runs in the PRIMARY checkout when there is one, not in the
	// worktree carl happens to be editing: a cell worktree's `origin/main` is
	// the same ref but its objects and its HEAD are the cell's, and an anchor
	// recorded against a cell branch would be unverifiable the moment the cell
	// is torn down. An unresolvable repo falls back to the worktree, which is
	// strictly better than refusing the mark.
	dir := worktree
	if p, err := jobs.ResolveRepo(repo); err == nil {
		dir = p
	}
	endRow := core.EndRowFromSelection(req.StartRow, text)
	// The ladder runs BEFORE the lock, and that placement is the whole reason
	// this route can be serialized at all: resolving an anchor is up to six git
	// calls at 20s each, and holding mmWriteMu across them would put one
	// human's keystroke behind another human's worst-case minute of git. The
	// probe cannot run any earlier either — the source ref carries the LOCATED
	// coordinates, so there is nothing to probe until the ladder has answered.
	// So: resolve unlocked, then lock, then probe and insert.
	anchor := mmResolveCodeAnchor(dir, file, text, req.StartRow, endRow)

	ref := fmt.Sprintf("%s/%s:%d-%d", repo, file, anchor.start, anchor.end)
	textSHA := mmTextSHA(anchor.text)
	mmWriteMu.Lock()
	defer mmWriteMu.Unlock()
	if mmFindLive(w, st, mmSourceCode, 0, ref, textSHA) {
		return
	}
	mmCreate(w, st, mmWrite{
		item: store.MMItem{
			Kind: mmKindReading, Source: mmSourceCode,
			SourceRef: ref, Content: anchor.text, Context: strings.TrimSpace(req.Note),
			CreatedAt: time.Now().UnixMilli(),
		},
		anchor: &store.MMCodeAnchor{
			Repo: repo, Path: file,
			StartLine: anchor.start, EndLine: anchor.end,
			AnchorSHA: anchor.sha, AnchorKind: anchor.kind,
			Text: anchor.text, TextSHA: textSHA,
			Lang:      strings.TrimPrefix(filepath.Ext(file), "."),
			Ambiguous: anchor.ambiguous,
		},
	})
}

// mmLineAt reads one 1-based line out of a file, without its terminator. Used
// only for the empty-selection case; a file that cannot be read reads as "no
// such line", since both mean the same thing to the caller.
func mmLineAt(path string, row int) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.Split(mmNormalize(string(data)), "\n")
	if row < 1 || row > len(lines) {
		return "", false
	}
	return lines[row-1], true
}

// mmRepoName is the repo half of a code mark's identity: origin's basename
// with any `.git` suffix stripped, falling back to the worktree's own
// basename. Origin first rather than the directory name because the SAME repo
// is checked out under many names on this machine — `~/Developer/kraken` and a
// dozen `~/.kraken/cells/<slug>-<id>/wt-kraken` worktrees — and a mark made
// from a cell must be the same identity as one made from the primary checkout,
// which is exactly what makes carl's model survive the cell that produced it.
func mmRepoName(worktree string) string {
	if url, err := mmGit(worktree, "remote", "get-url", "origin"); err == nil && url != "" {
		return strings.TrimSuffix(filepath.Base(strings.TrimSuffix(url, "/")), ".git")
	}
	return filepath.Base(worktree)
}

// mmResolveCodeAnchor is the anchor ladder: find the selection in the most
// DURABLE commit that has it, in descending order of durability —
// origin/main, then the merge base of HEAD and origin/main, then HEAD, then
// nothing at all.
//
// The order is the point. An anchor on origin/main survives every rebase of
// every branch; one on HEAD survives only until the branch moves; a `dirty`
// anchor (text that exists in no commit — carl marking something he has just
// typed) records exactly that, with an empty sha, and is honest about being
// unverifiable rather than pointing at a commit that never held the text.
//
// On a commit rung the STORED text is the BLOB's bytes at the located lines,
// never the editor buffer's, and the coordinates are the located ones. That is
// what makes a later verify comparable: the buffer may hold uncommitted edits,
// and an anchor whose text was never in the commit it names would fail its
// first verify against a file nobody had touched.
func mmResolveCodeAnchor(dir, file, text string, startRow, endRow int) mmCodeAnchor {
	span := core.EndRowFromSelection(1, text) // how many lines the selection covers
	for _, rung := range []struct {
		kind string
		args []string
	}{
		{"origin-main", []string{"rev-parse", "origin/main"}},
		{"merge-base", []string{"merge-base", "HEAD", "origin/main"}},
		{"head", []string{"rev-parse", "HEAD"}},
	} {
		sha, err := mmGit(dir, rung.args...)
		if err != nil || sha == "" {
			continue
		}
		// `git show <sha>:<file>` rather than `<ref>:<file>`: the sha is what
		// gets recorded, so reading the blob through the same resolved object
		// makes the recorded commit and the text provably the same one, even
		// if the ref moves between the two calls.
		blob, err := mmGit(dir, "show", sha+":"+file)
		if err != nil {
			continue
		}
		hits := core.LocateText(blob, text)
		if len(hits) == 0 {
			continue
		}
		// core.NearestLine, not a local copy: the jobs-side relocator picks the
		// same way, and the anchor arithmetic lives in core precisely so both
		// consumers share ONE implementation rather than two that agree until
		// one is edited. Multiple hits are never an error and never invalidate
		// anything — a helper called three times in one file is a normal thing
		// to mark — so the tie is broken by position, the only signal the
		// caller actually gave.
		start := core.NearestLine(hits, startRow)
		end := start + span - 1
		return mmCodeAnchor{
			kind: rung.kind, sha: sha, start: start, end: end,
			text: mmBlobLines(blob, start, end), ambiguous: len(hits) > 1,
		}
	}
	return mmCodeAnchor{kind: "dirty", start: startRow, end: endRow, text: text}
}

// mmBlobLines returns lines start..end of blob (1-based, inclusive) joined
// with "\n" and no trailing terminator — the canonical spelling of a line
// range, and still a byte substring of the blob, which is what every later
// verify searches for.
func mmBlobLines(blob string, start, end int) string {
	lines := strings.Split(mmNormalize(blob), "\n")
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// --- POST /api/mm/note ---

// mmNoteHandler adds a free note — the one item with no source outside carl's
// own head, which is why it belongs to no job and anchors to nothing. It has
// no `already` probe either: two identical notes written at different moments
// are two things carl thought, not one thing marked twice.
func mmNoteHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmNoteReq
	if !mmDecode(w, r, &req, "text and context") {
		return
	}
	if !mmRequire(w, "text", req.Text) {
		return
	}
	text := mmNormalize(strings.TrimSpace(req.Text))
	if mmTooLong(w, "text", text, mmNoteMaxRunes) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	// No mmWriteMu here, and that is the one deliberate exception: the mutex
	// guards find-live→create critical sections, and this route has no
	// find-live to guard. Two identical notes are two things carl thought.
	mmCreate(w, st, mmWrite{item: store.MMItem{
		Kind: mmKindNote, Source: mmSourceManual,
		Content: text, Context: strings.TrimSpace(req.Context),
		CreatedAt: time.Now().UnixMilli(),
	}})
}

// --- POST /api/mm/detail ---

// mmDetailHandler marks an agent's rendered DETAIL read — the line a cell
// reports into the steer modal. Q1: carl asked for it to be markable, and it
// is the one source whose text exists nowhere else on disk, which is why the
// item's content IS the record rather than a pointer to one.
//
// Its source ref is a short hash of that text rather than a position, because
// there is no stable address for it: the detail line is replaced on every zeus
// turn, so "the detail of job X" names a different sentence every few minutes
// while the sentence itself is exactly what was marked.
func mmDetailHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmDetailReq
	if !mmDecode(w, r, &req, "slug and text") {
		return
	}
	if !mmRequire(w, "slug", req.Slug, "text", req.Text) {
		return
	}
	text := mmNormalize(strings.TrimSpace(req.Text))
	if mmTooLong(w, "text", text, mmDetailMaxRunes) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	j := resolveJobBySlug(strings.TrimSpace(req.Slug))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	sha := mmTextSHA(text)
	ref := sha[:16]
	mmWriteMu.Lock()
	defer mmWriteMu.Unlock()
	if mmFindLive(w, st, mmSourceAgentOutput, j.ID, ref, sha) {
		return
	}
	mmCreate(w, st, mmWrite{item: store.MMItem{
		Kind: mmKindReading, Source: mmSourceAgentOutput,
		JobID: j.ID, Slug: feedbackJobKey(j), SourceRef: ref,
		Content: text, CreatedAt: time.Now().UnixMilli(),
	}})
}

// --- POST /api/mm/remove ---

// mmRemoveHandler is carl unmarking something: an invalidation with reason
// `removed`, never a delete. The model is append-only for the same reason the
// verify pass never resurrects an item — what carl knew at a moment is a fact
// about that moment, and a row deleted on a mis-click would take its frozen
// snapshot with it.
//
// An already-invalidated item answers 200 and re-runs NOTHING. The read is
// what makes that checkable: store.InvalidateMMItem returns a silent nil for a
// dead row (deliberately — it must not overwrite the first reason), so without
// looking first this handler could not tell a real transition from a repeat.
// Re-removing an item the verify pass already killed therefore neither
// rewrites the cause of death nor logs a second removal.
func mmRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmRemoveReq
	if !mmDecode(w, r, &req, "id") {
		return
	}
	// id 0 is what an absent key decodes to and no row ever has, so the two
	// share one refusal; a negative id is the same mistake wearing a sign.
	if req.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	it, err := st.GetMMItem(req.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown mental-model item"})
			return
		}
		util.LogEvent("web", "error", "mm-remove", "mm-remove read: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if it.InvalidatedAt != 0 {
		webJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	now := time.Now().UnixMilli()
	if err := st.InvalidateMMItem(req.ID, now, "removed", ""); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown mental-model item"})
			return
		}
		util.LogEvent("web", "error", "mm-remove", "mm-remove: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "mm-remove", "mm-remove "+strconv.FormatInt(req.ID, 10))
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// mmPost is the POST-only guard every write here opens with. The mirror gate
// is NOT here — it wraps these handlers at registration (register.go), which
// is available precisely because each is a single-method path.
func mmPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return false
	}
	return true
}
