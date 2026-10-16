package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// mind.go — the capture router: carl highlights text anywhere on the board,
// presses the mind button, and the passage lands in the job's MIND THREAD — a
// side thread with role "mind", forked from zeus, whose one job is turning the
// passage into a mental-model item with carl and committing it.
//
// The routing rule is "at this turn": every capture made while zeus' last
// message is unchanged reaches the SAME mind thread (it is still talking about
// the same moment of the job), and the first capture after zeus has said
// something new branches a fresh one, forked at that new message — so a mind
// thread's forked context is always the context the passage was read in. The
// thread row's fork_uuid, recorded at branch time as zeus' last message uuid,
// is what the comparison keys on.

// The capture refusals, each a distinct sentinel the API layer maps to a
// status: no live cell / no forkable zeus / a codex job → 409, a passage over
// captureMaxRunes → 400, a malformed source or empty text → 400.
var (
	ErrCaptureNoCell    = errors.New("no live cell with a forkable zeus to capture into")
	ErrCaptureTooLong   = errors.New("captured text is too long")
	ErrCaptureBadSource = errors.New("bad capture")
)

// captureMaxRunes caps one captured passage — the mm caps' decimal-runes
// convention (32000 for element and code marks).
const captureMaxRunes = 32000

// captureTitleRunes is how much of the passage names a newly branched mind
// thread in the thread list.
const captureTitleRunes = 60

// CaptureSource is where a captured passage came from — the pointer an item
// keeps (as mm_items.source_json) so `kraken mm source <id>` can drill back
// down to it. Kind decides which other fields matter: an artifact by job slug,
// home and name (plus the sha of the bytes carl was reading), a transcript
// record by session and uuid (tid when it was a thread's), a repo directory by
// repo and dir, another mm item by id, or manual for text with no pointer.
type CaptureSource struct {
	Kind    string `json:"kind"`
	Slug    string `json:"slug,omitempty"`
	Home    string `json:"home,omitempty"`
	Name    string `json:"name,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Session string `json:"session,omitempty"`
	UUID    string `json:"uuid,omitempty"`
	TID     string `json:"tid,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Dir     string `json:"dir,omitempty"`
	ItemID  int64  `json:"item_id,omitempty"`
}

// CaptureRequest is POST /api/mm/capture's body.
type CaptureRequest struct {
	Slug   string        `json:"slug"`
	Text   string        `json:"text"`
	Source CaptureSource `json:"source"`
}

// CaptureResult names the mind thread the capture reached, and whether this
// capture created it.
type CaptureResult struct {
	TID     string `json:"tid"`
	Created bool   `json:"created"`
}

// ValidCaptureSource refuses a kind outside the five, and a kind missing the
// one field that makes its pointer resolvable: an artifact's name, a
// transcript's session, a dir's repo.
func ValidCaptureSource(s CaptureSource) error {
	switch s.Kind {
	case "artifact":
		if strings.TrimSpace(s.Name) == "" {
			return errors.New("an artifact source needs a name")
		}
	case "transcript":
		if strings.TrimSpace(s.Session) == "" {
			return errors.New("a transcript source needs a session")
		}
	case "dir":
		if strings.TrimSpace(s.Repo) == "" {
			return errors.New("a dir source needs a repo")
		}
	case "item", "manual":
	default:
		return fmt.Errorf("bad source kind %q: want artifact, transcript, dir, item or manual", s.Kind)
	}
	return nil
}

// CaptureToMind routes one captured passage to the job's mind thread,
// branching one when no live mind thread was forked at zeus' current last
// message. Refusals come first and in a fixed order (see the sentinels);
// past them the capture either reaches the existing thread
// (Created:false) or a new one (Created:true).
//
// The cell check goes first, ahead of validating the passage, because it is
// the refusal carl can do something about (start the cell) and the one the
// board's button most often meets.
func CaptureToMind(j *Job, req CaptureRequest) (CaptureResult, error) {
	zeus, err := captureZeus(j)
	if err != nil {
		return CaptureResult{}, err
	}
	if utf8.RuneCountInString(req.Text) > captureMaxRunes {
		return CaptureResult{}, fmt.Errorf("%w: %d runes, the cap is %d", ErrCaptureTooLong, utf8.RuneCountInString(req.Text), captureMaxRunes)
	}
	if err := ValidCaptureSource(req.Source); err != nil {
		return CaptureResult{}, fmt.Errorf("%w: %v", ErrCaptureBadSource, err)
	}
	if strings.TrimSpace(req.Text) == "" {
		return CaptureResult{}, fmt.Errorf("%w: the captured text is empty", ErrCaptureBadSource)
	}

	// the turn key, the same value BranchThreadWithRole records as a mind
	// thread's TurnUUID
	zeusUUID := ""
	if path, perr := core.ReadTranscriptPath(zeus); perr == nil {
		zeusUUID = core.LastTurnUUID(path)
	}
	st, err := threadStore(j)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("%w: %v", ErrCaptureNoCell, err)
	}
	threads, err := st.ListThreads(j.ID)
	if err != nil {
		return CaptureResult{}, err
	}
	if t := mindThreadFor(threads, zeusUUID); t != nil {
		if err := SendToThread(j, t.TID, mindCaptureMessage(req)); err != nil {
			return CaptureResult{}, err
		}
		util.LogEvent("jobs", "info", "mm-capture", fmt.Sprintf("%s: capture sent to mind thread %s", j.Slug, t.TID))
		return CaptureResult{TID: t.TID, Created: false}, nil
	}
	t, err := BranchThreadWithRole(j, core.TruncateOneLine(req.Text, captureTitleRunes), mindSeed(req), "", "mind")
	if err != nil {
		return CaptureResult{}, err
	}
	util.LogEvent("jobs", "info", "mm-capture", fmt.Sprintf("%s: capture branched mind thread %s", j.Slug, t.TID))
	return CaptureResult{TID: t.TID, Created: true}, nil
}

// captureZeus is CaptureToMind's cell guard: BranchThread's own refusals (a
// codex job, no store row, no live cell session, no worktree, no forkable
// zeus), every one folded into ErrCaptureNoCell with BranchThread's reason
// kept as the message. It returns the zeus to fork from.
func captureZeus(j *Job) (*core.Peer, error) {
	if j == nil {
		return nil, fmt.Errorf("%w: no job", ErrCaptureNoCell)
	}
	if err := refuseCodexThread(j, "capture into"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCaptureNoCell, err)
	}
	if _, err := threadStore(j); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCaptureNoCell, err)
	}
	if _, live := FindCellSession(j, LiveSessions()); !live {
		return nil, fmt.Errorf("%w: %s has no live cell — start or reactivate it first", ErrCaptureNoCell, j.Slug)
	}
	if threadWorktree(j) == "" {
		return nil, fmt.Errorf("%w: %s has no worktree — reactivate the job first", ErrCaptureNoCell, j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus, err := zeusForkTarget(j, all)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCaptureNoCell, err)
	}
	return zeus, nil
}

// mindThreadFor picks the mind thread a capture made at zeusUUID belongs to:
// the live "mind" thread whose TurnUUID is exactly that prompt record, else
// nil. PURE.
//
// An empty zeusUUID never matches — a zeus with no readable transcript has no
// turn to be "at", and matching an empty turn key would funnel every such
// capture into whichever thread happened to record none.
func mindThreadFor(threads []store.Thread, zeusUUID string) *store.Thread {
	if zeusUUID == "" {
		return nil
	}
	for i := range threads {
		t := &threads[i]
		if t.Role == "mind" && t.State == "live" && t.TurnUUID == zeusUUID {
			return t
		}
	}
	return nil
}

// captureSourceJSON is the source as the seed and the message carry it, and
// as the thread hands it back on commit: plain encoding/json.
func captureSourceJSON(s CaptureSource) string {
	b, err := json.Marshal(s)
	if err != nil {
		return "{}" // unreachable for a struct of strings and an int
	}
	return string(b)
}

// mindSeed is a new mind thread's initial prompt: the passage verbatim, its
// source pointer as JSON, and the item contract the thread commits against —
// staged at the recency point so the thread never has to re-derive it. It
// carries the pointer, never the artifact body: the thread is forked from
// zeus and reads the source itself if it needs more. Pure apart from naming
// the operator (Operator(), read from the environment).
func mindSeed(req CaptureRequest) string {
	op := Operator()
	var b strings.Builder
	b.WriteString(op + " highlighted this passage and sent it to you to become an item in their mental model:\n\n")
	b.WriteString(captureQuote(req.Text))
	b.WriteString("\nsource: ")
	b.WriteString(captureSourceJSON(req.Source))
	b.WriteString("\n\nThe item you build with them:\n")
	b.WriteString("- title: ≤ 120 runes, stated as a CLAIM (what is true), never a topic\n")
	b.WriteString("- scope: `universal` (true everywhere " + op + " works), or `repo` with `repo` (the checkout name) and `dir` (repo-relative, \"\" for the whole repo) — where the knowledge applies\n")
	b.WriteString("- kind: `decision`, `reading` or `note`\n")
	b.WriteString("- body: ≤ 8,000 runes — what " + op + " now knows, in their terms\n")
	b.WriteString("- quote: the passage above, verbatim; source: the source JSON above, verbatim\n")
	b.WriteString("Fill those fields in SILENTLY. What " + op + " reads is your paraphrase of what they now understand — at most four sentences, in their terms, no field list, no mechanism explanation, no quote-back — and at most one question; they confirm, deny, or dig deeper, one point per turn. When they say finalize, run `kraken mm commit` with the item as JSON on stdin — {\"title\",\"scope\",\"repo\",\"dir\",\"kind\",\"body\",\"quote\",\"source\"} — and tell them the id in one line.\n")
	return b.String()
}

// mindCaptureMessage is what a further capture types into an existing mind
// thread: the passage verbatim and its source JSON, with a one-line pointer
// back to the contract the seed already stated. Pure apart from Operator().
func mindCaptureMessage(req CaptureRequest) string {
	op := Operator()
	var b strings.Builder
	b.WriteString(op + " highlighted another passage for their mental model:\n\n")
	b.WriteString(captureQuote(req.Text))
	b.WriteString("\nsource: ")
	b.WriteString(captureSourceJSON(req.Source))
	b.WriteString("\n\nSame item contract as before: propose a title, scope, kind and body for it (or fold it into the item you are building, if it belongs there), and commit with `kraken mm commit` only when " + op + " says finalize.\n")
	return b.String()
}

// captureQuote fences the passage between two marker lines rather than
// prefixing each line, so the text inside stays byte-for-byte what carl
// highlighted.
func captureQuote(text string) string {
	return "<<<passage\n" + text + "\npassage>>>\n"
}
