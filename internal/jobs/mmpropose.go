package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"kraken/internal/store"
	"kraken/internal/util"
)

// mmpropose.go — the mind pass' one write: `kraken mm propose`, run by the
// nightly mind job's zeus with the night's candidates in one batch.
//
// Three deltas from the mind thread's commit (mmcommit.go):
//
//   - The gate is the stamped LEAD session of the job mind_last_run names (by job_id),
//     not a mind thread — the pass has no thread. Checked before any item is
//     read, as the commit gate checks the thread before validating.
//   - Items land state "proposed", so ListMMItems(false) — the render pass, the
//     verify pass and `kraken mm get`'s live view — never sees them until the
//     operator confirms one on the Model pane.
//   - Suppression reads the dead rows: a quote whose only rows are invalidated
//     as removed or contradicted was already judged and thrown out, so it is
//     not proposed again. There is no suppression table.

// ErrNotMindLead refuses a propose whose session is not the mind job's
// stamped lead (→ 403).
var ErrNotMindLead = errors.New("not the mind job's lead session")

// mmProposeCap bounds the rows one propose call may add.
const mmProposeCap = 20

// mmProposeQuoteMaxRunes / mmProposeRungMaxRunes are the proposal's two extra
// caps (decimal runes).
const (
	mmProposeQuoteMaxRunes = 32000
	mmProposeRungMaxRunes  = 200
)

// mmProposeNotify is the notification seam.
var mmProposeNotify = notify

// MMProposal is one candidate item as the mind pass proposes it. Rung is the
// exclusion-ladder rung the candidate cleared, stored as the row's context.
type MMProposal struct {
	Title  string        `json:"title"`
	Scope  string        `json:"scope"`
	Repo   string        `json:"repo"`
	Dir    string        `json:"dir"`
	Kind   string        `json:"kind"`
	Body   string        `json:"body"`
	Quote  string        `json:"quote"`
	Rung   string        `json:"rung"`
	Source CaptureSource `json:"source"`
}

// MMProposeResult is one proposal's outcome: added | folded | suppressed |
// refused. ID is the new row for added, the existing row for folded.
type MMProposeResult struct {
	Index   int    `json:"index"`
	ID      int64  `json:"id,omitempty"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// MMProposeSummary is the call's answer; the counts tally Results.
type MMProposeSummary struct {
	Results    []MMProposeResult `json:"results"`
	Added      int               `json:"added"`
	Folded     int               `json:"folded"`
	Suppressed int               `json:"suppressed"`
	Refused    int               `json:"refused"`
}

// mindLeadSession resolves the propose gate: the job mind_last_run names —
// by its immutable JobID, falling back to the Job slug only for a watermark
// written before JobID existed — only while that row is ACTIVE and has a
// stamped session.
func mindLeadSession(st *store.Store) (jobID int64, slug, session string, ok bool) {
	if st == nil {
		return 0, "", "", false
	}
	run, rok := readMindRunFrom(st)
	if !rok {
		return 0, "", "", false
	}
	var j *store.Job
	var err error
	switch {
	case run.JobID != 0:
		j, err = st.GetJobByID(run.JobID)
	case run.Job != "":
		j, err = st.GetJobBySlug(run.Job)
	default:
		return 0, "", "", false
	}
	if err != nil || j == nil || j.ArchivedAt != 0 || j.Session == "" {
		return 0, "", "", false
	}
	return j.ID, j.Slug, j.Session, true
}

// ProposeMMItems stores the night's proposals. Gate first; then every item is
// validated before any is stored (one bad item stores nothing); then each item
// in order folds onto a non-invalidated row with the same quote, is suppressed
// by a row invalidated as removed/contradicted, is refused past the cap, or is
// added as a proposed nightly row.
func ProposeMMItems(st *store.Store, session string, items []MMProposal) (MMProposeSummary, error) {
	if st == nil {
		return MMProposeSummary{}, errors.New("no store")
	}
	session = strings.TrimSpace(session)
	jobID, slug, lead, ok := mindLeadSession(st)
	if session == "" || !ok || session != lead {
		return MMProposeSummary{}, ErrNotMindLead
	}

	for i := range items {
		if err := mmValidateProposal(&items[i]); err != nil {
			return MMProposeSummary{}, fmt.Errorf("%w (item %d)", err, i)
		}
	}

	sum := MMProposeSummary{Results: make([]MMProposeResult, 0, len(items))}
	for i, p := range items {
		res, err := mmProposeOne(st, p, jobID, slug, sum.Added)
		if err != nil {
			return sum, fmt.Errorf("item %d: %w", i, err)
		}
		res.Index = i
		switch res.Outcome {
		case "added":
			sum.Added++
		case "folded":
			sum.Folded++
		case "suppressed":
			sum.Suppressed++
		case "refused":
			sum.Refused++
		}
		sum.Results = append(sum.Results, res)
	}
	util.LogEvent("jobs", "info", "mm-propose", fmt.Sprintf("mind pass %s proposed %d (%d folded, %d suppressed, %d refused)",
		slug, sum.Added, sum.Folded, sum.Suppressed, sum.Refused))
	if sum.Added > 0 {
		mmProposeNotify("kraken ◆ mental model", fmt.Sprintf("%d proposed from last night (%d folded, %d suppressed) — confirm on the board",
			sum.Added, sum.Folded, sum.Suppressed))
	}
	return sum, nil
}

// mmProposeOne decides and (for an add) writes one validated proposal.
func mmProposeOne(st *store.Store, p MMProposal, jobID int64, slug string, added int) (MMProposeResult, error) {
	rows, err := st.FindMMItemsByQuote(p.Quote)
	if err != nil {
		return MMProposeResult{}, err
	}
	// rows are newest first, so the first non-invalidated row is the newest.
	for _, r := range rows {
		if r.InvalidatedAt == 0 {
			return MMProposeResult{ID: r.ID, Outcome: "folded"}, nil
		}
	}
	for _, r := range rows {
		if r.InvalidReason == "removed" || r.InvalidReason == "contradicted" {
			return MMProposeResult{Outcome: "suppressed"}, nil
		}
	}
	if added >= mmProposeCap {
		return MMProposeResult{Outcome: "refused", Reason: "cap"}, nil
	}
	id, err := st.AddMMItem(store.MMItem{
		Kind:          p.Kind,
		Source:        "nightly",
		State:         "proposed",
		JobID:         jobID,
		Slug:          slug,
		Content:       p.Body,
		Context:       p.Rung,
		Title:         p.Title,
		Scope:         p.Scope,
		Repo:          p.Repo,
		Dir:           p.Dir,
		Quote:         p.Quote,
		SourceJSON:    captureSourceJSON(p.Source),
		ThreadSession: "",
		CreatedAt:     time.Now().UnixMilli(),
	})
	if err != nil {
		return MMProposeResult{}, err
	}
	return MMProposeResult{ID: id, Outcome: "added"}, nil
}

// mmValidateProposal is mmValidateCommit plus the proposal's own rules: no
// `reading` kind (the operator reads; the pass does not), a pointer kind the
// pass can resolve, a non-empty quote within its cap, and a short rung. The
// quote is TrimSpaced here, in place, so the fold/suppress lookup and the
// stored row both see the trimmed value — a trailing-newline or CRLF variant
// of a dropped quote is still suppressed (the lookup itself is byte-exact).
func mmValidateProposal(p *MMProposal) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrCommitInvalid, fmt.Sprintf(format, a...))
	}
	p.Quote = strings.TrimSpace(p.Quote)
	switch p.Kind {
	case "decision", "note":
	default:
		return bad("kind %q: a proposal wants decision or note", p.Kind)
	}
	switch p.Source.Kind {
	case "transcript", "job", "feedback", "question":
	default:
		return bad("source kind %q: a proposal wants transcript, job, feedback or question", p.Source.Kind)
	}
	if n := utf8.RuneCountInString(p.Quote); n < 1 || n > mmProposeQuoteMaxRunes {
		return bad("quote must be 1-%d runes, got %d", mmProposeQuoteMaxRunes, n)
	}
	if n := utf8.RuneCountInString(p.Rung); n > mmProposeRungMaxRunes {
		return bad("rung must be at most %d runes, got %d", mmProposeRungMaxRunes, n)
	}
	c := MMCommit{Title: p.Title, Scope: p.Scope, Repo: p.Repo, Dir: p.Dir, Kind: p.Kind, Body: p.Body, Quote: p.Quote, Source: p.Source}
	if err := mmValidateCommit(&c); err != nil {
		return err
	}
	p.Title, p.Scope, p.Repo, p.Dir, p.Body = c.Title, c.Scope, c.Repo, c.Dir, c.Body
	return nil
}
