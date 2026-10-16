package jobs

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"kraken/internal/store"
	"kraken/internal/util"
)

// mmcommit.go — the mental model's one write path for agents: `kraken mm
// commit` / `kraken mm revise`, run by a mind thread when carl says finalize.
//
// The gate is WHO, checked before WHAT: the caller's session must be a live
// thread row with role "mind". The mind thread is the only AGENT-side writer
// of the mental model (carl's own surfaces — mark/note/drop, the Zed task, the
// legacy one-shot routes — still write it). Its brief says so, and this is
// where that stops being a promise: zeus, a teammate or an ask thread
// presenting the same body is refused before its body is even looked at, so
// a refusal never leaks which
// field would have been wrong.

// ErrNotMindThread refuses a commit/revise whose session is not a live mind
// thread (→ 403); ErrCommitInvalid wraps every validation refusal (→ 400).
var (
	ErrNotMindThread = errors.New("not a mind thread")
	ErrCommitInvalid = errors.New("invalid mm item")
)

// ErrMMItemNotFound, ErrMMItemDead and ErrReviseLegacy are ReviseMMItem's
// item refusals (→ 404 / 409 / 409). Only a captured item is revisable: a
// legacy mark is superseded by a new commit, never rewritten in place.
var (
	ErrMMItemNotFound = errors.New("no such mm item")
	ErrMMItemDead     = errors.New("mm item is invalidated")
	ErrReviseLegacy   = errors.New("mm item is not revisable")
)

// The item caps: a title is one line, a body the 8000-rune decimal cap the
// note/detail marks already use.
const (
	mmTitleMaxRunes = 120
	mmBodyMaxRunes  = 8000
)

// MMCommit is one item as a mind thread finalizes it — `kraken mm commit`'s
// stdin and `kraken mm revise`'s.
type MMCommit struct {
	Title  string        `json:"title"`
	Scope  string        `json:"scope"`
	Repo   string        `json:"repo"`
	Dir    string        `json:"dir"`
	Kind   string        `json:"kind"`
	Body   string        `json:"body"`
	Quote  string        `json:"quote"`
	Source CaptureSource `json:"source"`
}

// CommitMMItem creates one item from a mind thread's commit and returns its
// id. The gate, then validation (see mmValidateCommit), then the row: source
// "capture", owned by the thread's job, body as content, the source pointer as
// JSON and the committing session recorded. The quote is stored verbatim for
// every source kind; the source pointer rides beside it as source_json so
// `kraken mm source` can re-read the original.
func CommitMMItem(st *store.Store, c MMCommit, threadSession string) (int64, error) {
	if st == nil {
		return 0, errors.New("no store")
	}
	t, err := mmMindThread(st, threadSession)
	if err != nil {
		return 0, err
	}
	if err := mmValidateCommit(&c); err != nil {
		return 0, err
	}
	slug := ""
	if sr, err := st.GetJobByID(t.JobID); err == nil && sr != nil {
		slug = sr.Slug
	}
	id, err := st.AddMMItem(store.MMItem{
		Kind:          c.Kind,
		Source:        "capture",
		JobID:         t.JobID,
		Slug:          slug,
		SourceRef:     mmCaptureSourceRef(c.Source),
		Content:       c.Body,
		Title:         c.Title,
		Scope:         c.Scope,
		Repo:          c.Repo,
		Dir:           c.Dir,
		Quote:         c.Quote,
		SourceJSON:    captureSourceJSON(c.Source),
		ThreadSession: threadSession,
		CreatedAt:     time.Now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	util.LogEvent("jobs", "info", "mm-commit", fmt.Sprintf("mm item %d committed by mind thread %s of %s: %s", id, t.TID, slug, c.Title))
	return id, nil
}

// ReviseMMItem rewrites an existing live item from a mind thread's revise:
// the same gate and validation as a commit, then store.ReviseMMItem. The
// source pointer and the committing session are the item's history and are
// not rewritten.
func ReviseMMItem(st *store.Store, id int64, c MMCommit, threadSession string) error {
	if st == nil {
		return errors.New("no store")
	}
	t, err := mmMindThread(st, threadSession)
	if err != nil {
		return err
	}
	cur, err := st.GetMMItem(id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %d", ErrMMItemNotFound, id)
	}
	if err != nil {
		return err
	}
	if cur.Source != "capture" {
		return fmt.Errorf("%w: %d is a legacy mark, not a captured item — commit a new item with `kraken mm commit` instead", ErrReviseLegacy, id)
	}
	if cur.InvalidatedAt != 0 {
		return fmt.Errorf("%w: %d (%s)", ErrMMItemDead, id, cur.InvalidReason)
	}
	if err := mmValidateCommit(&c); err != nil {
		return err
	}
	if err := st.ReviseMMItem(id, store.MMRevision{
		Title: c.Title, Content: c.Body, Quote: c.Quote,
		Scope: c.Scope, Repo: c.Repo, Dir: c.Dir,
		At: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	util.LogEvent("jobs", "info", "mm-revise", fmt.Sprintf("mm item %d revised by mind thread %s: %s", id, t.TID, c.Title))
	return nil
}

// mmMindThread is the commit gate: threadSession must name a LIVE thread row
// with role "mind". A store failure is returned as itself (it says nothing
// about the caller); every other miss is ErrNotMindThread.
func mmMindThread(st *store.Store, threadSession string) (*store.Thread, error) {
	threadSession = strings.TrimSpace(threadSession)
	if threadSession == "" {
		return nil, ErrNotMindThread
	}
	t, err := st.GetThreadBySession(threadSession)
	if err != nil {
		return nil, err
	}
	if t == nil || t.Role != "mind" || t.State != "live" {
		return nil, ErrNotMindThread
	}
	return t, nil
}

// mmValidateCommit checks and normalizes c in place: title and body trimmed
// and within their caps, kind and scope in their sets, a universal item
// carrying no placement, a repo item naming a repo ResolveRepo finds (stored
// as that checkout's name) and a repo-relative dir with no `..`, and a valid
// source. Every refusal wraps ErrCommitInvalid with its reason.
func mmValidateCommit(c *MMCommit) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrCommitInvalid, fmt.Sprintf(format, a...))
	}
	c.Title = strings.TrimSpace(c.Title)
	if n := utf8.RuneCountInString(c.Title); n < 1 || n > mmTitleMaxRunes {
		return bad("title must be 1-%d runes, got %d", mmTitleMaxRunes, n)
	}
	c.Body = strings.TrimSpace(c.Body)
	if n := utf8.RuneCountInString(c.Body); n < 1 || n > mmBodyMaxRunes {
		return bad("body must be 1-%d runes, got %d", mmBodyMaxRunes, n)
	}
	switch c.Kind {
	case "decision", "reading", "note":
	default:
		return bad("kind %q: want decision, reading or note", c.Kind)
	}
	switch c.Scope {
	case "universal":
		c.Repo, c.Dir = "", ""
	case "repo":
		name := strings.TrimSpace(c.Repo)
		if name == "" {
			return bad("a repo-scoped item needs a repo")
		}
		resolved, err := ResolveRepo(name)
		if err != nil {
			return bad("repo %q: %v", name, err)
		}
		// ResolveRepo is a case-insensitive SUBSTRING match with an alphabetic
		// tiebreak — fine for finding the checkout, wrong for accepting it: a
		// commit naming "k" would file under whichever repo sorts first. The
		// resolve stays (it finds the path); acceptance demands the exact name.
		if !strings.EqualFold(filepath.Base(resolved), name) {
			return bad("repo %q: must be the checkout's exact directory name under %s (closest match: %s)", name, repoRoot(), filepath.Base(resolved))
		}
		c.Repo = filepath.Base(resolved)
		dir := strings.TrimSpace(c.Dir)
		if strings.HasPrefix(dir, "/") {
			return bad("dir %q must be repo-relative", dir)
		}
		// the dir is rendered inside a quoted YAML glob (mmFrontmatter), so a
		// quote or a line break would break the rules file's frontmatter, and a
		// backslash starts an escape that is invalid there
		if strings.ContainsAny(dir, "\"\r\n") {
			return bad("dir %q must not contain quotes or line breaks", dir)
		}
		if strings.Contains(dir, `\`) {
			return bad("dir %q must not contain backslashes", dir)
		}
		for _, seg := range strings.Split(dir, "/") {
			if seg == ".." {
				return bad("dir %q must not contain ..", dir)
			}
		}
		dir = path.Clean("/" + dir)[1:] // "" for the repo root, no trailing slash
		c.Dir = dir
	default:
		return bad("scope %q: want universal or repo", c.Scope)
	}
	if err := ValidCaptureSource(c.Source); err != nil {
		return bad("source: %v", err)
	}
	return nil
}

// mmCaptureSourceRef is the item's source_ref: the one-string handle the
// older surfaces (mm pane, verify pass) key on — the artifact name for an
// artifact capture, empty otherwise. The full pointer is source_json.
func mmCaptureSourceRef(s CaptureSource) string {
	if s.Kind == "artifact" {
		return s.Name
	}
	return ""
}
