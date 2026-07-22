package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/core"
	"kraken/jobs"
	"kraken/util"
)

// apiContract is the wire-shape version for /api/health and /api/board,
// mirrored by the iOS app's expected value (see ios/API.md). Bump ONLY on a
// breaking change (a JSON key retyped, removed, or renamed) — additive keys
// never bump it, since old clients already ignore fields they don't know.
// v2: the success criteria field was removed (webJob.success on /api/board,
// success on the job form GET/POST).
// v3: the auto-approve feature was deleted (auto_approve removed from the
// job form GET/POST shapes, auto-approve: frontmatter no longer written).
const apiContract = 3

type webArtifact struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type webPrereq struct {
	Job   string `json:"job"`
	Gated bool   `json:"gated"`
	Done  bool   `json:"done"`
}

// webQuestionOption/webQuestionItem/webQuestion mirror status.go's
// QuestionOption/QuestionItem/jobs.PendingQuestion for the wire: those types carry
// json tags matched to Claude's AskUserQuestion tool-call input shape
// (multiSelect etc, decoded straight from the transcript), which isn't the
// API's own naming convention — so the board gets its own DTO instead of
// reusing those tags directly.
type webQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type webQuestionItem struct {
	Question    string              `json:"question"`
	Header      string              `json:"header"`
	MultiSelect bool                `json:"multi_select"`
	Options     []webQuestionOption `json:"options"`
}

type webQuestion struct {
	ToolUseID string            `json:"tool_use_id"`
	Questions []webQuestionItem `json:"questions"`
}

// webQuestionFrom converts status.go's jobs.PendingQuestion into the board's DTO.
func webQuestionFrom(pq *jobs.PendingQuestion) *webQuestion {
	if pq == nil {
		return nil
	}
	wq := &webQuestion{ToolUseID: pq.ToolUseID}
	for _, q := range pq.Questions {
		item := webQuestionItem{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
		for _, o := range q.Options {
			item.Options = append(item.Options, webQuestionOption{Label: o.Label, Description: o.Description})
		}
		wq.Questions = append(wq.Questions, item)
	}
	return wq
}

// webAgent is one roster entry: a live subagent in the cell's tmux session.
type webAgent struct {
	Name  string `json:"name"`
	Model string `json:"model"`
	// Role/Status/Working mirror jobs.CrewAgent — always present (no
	// omitempty), since false/"" are meaningful roster states, not absence.
	Role    string `json:"role"`
	Status  string `json:"status"`
	Working bool   `json:"working"`
}

// webWakeup mirrors core.Wakeup for the wire, minus DelaySeconds — the
// client derives its countdown from FiresAt against wall-clock time, it
// doesn't need the original delay.
type webWakeup struct {
	Reason  string `json:"reason"`
	FiresAt int64  `json:"fires_at"`
}

// webWakeupFrom converts a pending core.Wakeup into the board's DTO.
func webWakeupFrom(w *core.Wakeup) *webWakeup {
	if w == nil {
		return nil
	}
	return &webWakeup{Reason: w.Reason, FiresAt: w.FiresAt}
}

type webJob struct {
	Slug      string        `json:"slug"`
	Type      string        `json:"type"`
	Repo      string        `json:"repo"`
	State     string        `json:"state"`
	Err       string        `json:"err,omitempty"`
	Outcome   string        `json:"outcome,omitempty"`
	Cell      string        `json:"cell,omitempty"`
	Live      bool          `json:"live"`
	Prereqs   []webPrereq   `json:"prereqs,omitempty"`
	Artifacts []webArtifact `json:"artifacts,omitempty"`
	// Detail is the cell's last words: for a live started cell, the full
	// text of the last assistant text block of zeus' transcript, capped at
	// 4000 chars.
	Detail string `json:"detail,omitempty"`
	// LastSent is the last real user message from zeus' own transcript,
	// regardless of send channel (board steer, k s, direct tmux typing),
	// one-lined and capped at 4000 chars. Omitted when no transcript is
	// resolvable or it has no user turn yet; question-answer digit
	// keypresses never appear here since they're tool_results, not user text.
	LastSent string `json:"last_sent,omitempty"`
	// Question mirrors an unanswered AskUserQuestion call in zeus'
	// transcript; the board renders it as header + option buttons.
	Question *webQuestion `json:"question,omitempty"`
	// ZeusStatus is zeus' own refined status (running/polling/waiting/
	// question), distinct from the cell's aggregate state; omitted when zeus
	// isn't resolvable (fresh fork, no session file yet).
	ZeusStatus string `json:"zeus_status,omitempty"`
	// Wakeup mirrors the zeus peer's own pending ScheduleWakeup call and is
	// present only while the cell is polling (mid /loop or /poll-pr-style
	// tick). The client renders a countdown chip from it; the cell still
	// counts as "working".
	Wakeup *webWakeup `json:"wakeup,omitempty"`
	// Roster lists the cell's live subagents (nested claude peers).
	Roster []webAgent `json:"roster,omitempty"`
	// Health is a computed condition on the live cell ("" = healthy): API
	// errors or dead agent processes. Never written to the job file — the
	// board shows it in BLOCKED with a retry affordance.
	Health  string `json:"health,omitempty"`
	ModTime int64  `json:"mtime"`
	// Archived jobs ride the same list with State "archived". Their Slug is
	// the numbered archive file base (unique even when an active job reuses
	// the freed slug — it's what unarchive/delete target); DisplaySlug
	// carries the original slug for rendering.
	Archived    bool   `json:"archived,omitempty"`
	DisplaySlug string `json:"display_slug,omitempty"`
	// CellGone marks a started, non-done job whose tmux session vanished
	// (killed by hand, tmux restart, a teardown that died mid-flight): the
	// worktree — uncommitted work included — is still on disk, and reactivate
	// is the recovery that preserves it. Additive key; clients without it
	// just miss the chip.
	CellGone bool `json:"cell_gone,omitempty"`
	// Reactivatable marks a job whose zeus session id is recorded and whose
	// cell is resumable — done jobs, plus gone-cell started jobs — so the UIs
	// only offer the reactivate action where it can actually work.
	Reactivatable bool `json:"reactivatable,omitempty"`
	// PRs mirrors the job file's kraken-owned prs: list — every PR the
	// cell ever shipped, rendered by the clients as linked state chips.
	PRs []jobs.JobPR `json:"prs,omitempty"`
}

type webBoard struct {
	Daemon    jobs.DaemonStatus `json:"daemon"`
	DaemonOK  bool              `json:"daemon_ok"`
	CellCap   int               `json:"cell_cap"`
	LiveCells int               `json:"live_cells"`
	Jobs      []webJob          `json:"jobs"`
	Contract  int               `json:"contract"`
	// Stale is this k web process' own self-check (see selfcheck.go) — the
	// daemon's equivalent rides Daemon.Stale.
	Stale bool `json:"stale"`
}

// jobArtifacts collects a job's viewable artifacts from both homes: the live
// cell's artifacts dir and the preserved <slug>.<name> copies in pluto.
func jobArtifacts(j *jobs.Job) []webArtifact {
	var out []webArtifact
	root := jobs.CellArtifacts(j)
	for _, p := range jobs.ListArtifacts(j) {
		// name is the path relative to the artifacts dir, so nested files
		// read as "icon-variations/icon-v1.png" and clients can group by dir
		name := filepath.Base(p)
		if rel, err := filepath.Rel(root, p); err == nil {
			name = rel
		}
		out = append(out, webArtifact{Name: name, URL: "/api/artifact/" + j.Slug + "/cell/" + name})
	}
	prefix := util.Slugify(j.Slug) + "."
	entries, err := os.ReadDir(jobs.PlutoDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, jobs.JobSuffix) {
			continue
		}
		out = append(out, webArtifact{Name: strings.TrimPrefix(name, prefix), URL: "/api/artifact/" + j.Slug + "/pluto/" + strings.TrimPrefix(name, prefix)})
	}
	return out
}

// ArtifactPath resolves an artifact request to a real file, refusing anything
// that escapes the two artifact homes (the URL is attacker-adjacent input:
// any local process can hit the port). A cell name may be a subpath
// (icon-variations/icon-v1.png) so folders are navigable, but every segment is
// validated against traversal and hidden files.
func ArtifactPath(j *jobs.Job, home, name string) (string, bool) {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false // no traversal, no empty/hidden segments
		}
	}
	var p, root string
	switch home {
	case "cell":
		root = jobs.CellArtifacts(j)
		p = filepath.Join(root, name)
	case "pluto":
		// pluto copies are flat (<slug>.<name>), never nested
		if strings.Contains(name, "/") {
			return "", false
		}
		root = jobs.PlutoDir()
		p = filepath.Join(root, util.Slugify(j.Slug)+"."+name)
	case "archive":
		// archived copies are flat under the numbered archive prefix
		if j.ArchivedName == "" || strings.Contains(name, "/") {
			return "", false
		}
		root = jobs.ArchiveDir()
		p = filepath.Join(root, j.ArchivedName+"."+name)
	default:
		return "", false
	}
	// the flat homes join <prefix>.<name>, so a name like "job.md" would
	// reassemble the job FILE's own path — job files are never artifacts
	if strings.HasSuffix(p, jobs.JobSuffix) {
		return "", false
	}
	// belt-and-suspenders: the cleaned path must still sit under its home
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return "", false
	}
	return p, true
}

// cellZeusTranscript resolves a live job's zeus peer and (best-effort) its
// transcript path in one shot — the shared lookup behind the board's detail
// and question fields, and the question-answer action. An unresolvable
// transcript (fresh fork, no session file yet) reads as "" rather than an
// error — the board field is just omitted.
func cellZeusTranscript(j *jobs.Job, all []*core.Peer) (zeus *core.Peer, transcript string) {
	zeus = jobs.CellZeus(j, all)
	if zeus == nil {
		return nil, ""
	}
	path, err := core.ReadTranscriptPath(zeus)
	if err != nil {
		return zeus, ""
	}
	return zeus, path
}

// lastAssistantText scans a transcript for the last assistant text block
// (parseBlocks' "text" kind — thinking and tool_use blocks don't count),
// tail-reading via lastBlocks the same way k r does so huge transcripts stay
// fast. "" when the file is unreadable or has no assistant text within the
// lookback window.
func lastAssistantText(path string) string {
	blocks, _, err := core.LastBlocks(path, 20)
	if err != nil {
		return ""
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Kind == "text" {
			return truncateText(blocks[i].Text, 4000)
		}
	}
	return ""
}

// truncateText caps s at limit runes, preserving newlines and internal
// whitespace (unlike truncateOneLine, which flattens to a single line) —
// the board wants the cell's actual formatting, just bounded so a runaway
// transcript block doesn't blow up the wire payload. Other control chars
// (esp. \x1b) are mapped to space, same as truncateOneLine, since this
// feeds iOS notification bodies too.
func truncateText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// lastUserSent resolves a transcript path to the board's last_sent field:
// the last real user message, one-lined and capped at 4000 chars. ""
// when the path is unresolved or the transcript has no user turn yet.
func lastUserSent(transcript string) string {
	if transcript == "" {
		return ""
	}
	return core.TruncateOneLine(core.LastUserText(transcript), 4000)
}

// boardCache memoizes buildWebBoard for a short TTL so a burst of board
// polls (several web tabs + the iOS app) costs one discovery, not N. The
// mutex doubles as single-flight: concurrent misses serialize and late
// arrivals get the fresh entry. Mutations invalidate so a post-action
// refresh never sees pre-action state.
type boardCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time // injectable for tests
	build func() webBoard  // injectable for tests
	at    time.Time        // zero = invalid
	board webBoard
}

// get returns the cached board if it's still within ttl, otherwise rebuilds
// under the lock — the lock held across build is what makes concurrent
// misses single-flight instead of a thundering herd of discoveries.
func (c *boardCache) get() webBoard {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && c.now().Sub(c.at) < c.ttl {
		return c.board
	}
	c.board = c.build()
	c.at = c.now()
	return c.board
}

// invalidate forces the next get() to rebuild — called after any write so a
// client's post-action refresh never reads a pre-action snapshot.
func (c *boardCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
}

// webBoardCache is the /api/board handler's single instance.
var webBoardCache = &boardCache{ttl: 2 * time.Second, now: time.Now, build: buildWebBoard}

func buildWebBoard() webBoard {
	js := jobs.LoadJobs()
	bySlug := map[string]*jobs.Job{}
	for _, j := range js {
		bySlug[j.Slug] = j
	}
	live := jobs.LiveSessions()
	st, ok := jobs.ReadDaemonStatus()
	all, _ := core.OrderAll(core.DiscoverAll())
	board := webBoard{Daemon: st, DaemonOK: ok && jobs.ProcessAlive(st.PID), CellCap: jobs.CellCap, Jobs: []webJob{}, Contract: apiContract, Stale: util.IsSelfStale()}
	for _, j := range js {
		var prereqs []webPrereq
		for _, p := range j.Prereqs {
			dep := bySlug[p.Job]
			prereqs = append(prereqs, webPrereq{Job: p.Job, Gated: p.Gated, Done: dep != nil && dep.State == "done"})
		}
		wj := webJob{
			Slug: j.Slug, Type: j.Type, Repo: j.Repo,
			State: jobs.DisplayState(j, bySlug), Err: j.Err, Outcome: j.Outcome,
			Prereqs: prereqs, Artifacts: jobArtifacts(j),
			ModTime: j.ModTime.Unix(),
			PRs:     j.PRs,
		}
		if live[jobs.CellSession(j)] {
			wj.Cell, wj.Live = jobs.CellSession(j), true
			if j.Started() && j.State != "done" {
				board.LiveCells++
				wj.Health = jobs.CellHealth(j, all)
				zeus, transcript := cellZeusTranscript(j, all)
				if transcript != "" {
					wj.Detail = lastAssistantText(transcript)
					wj.LastSent = lastUserSent(transcript)
					wj.Question = webQuestionFrom(jobs.FindPendingQuestion(transcript))
					if zeus != nil && zeus.Status == "polling" && zeus.Wakeup != nil {
						wj.Wakeup = webWakeupFrom(zeus.Wakeup)
					}
				}
				var members []core.TeamMember
				if zeus != nil && !zeus.SessionIDGuessed {
					members = core.TeamMembersForLead(zeus.SessionID)
				}
				crew := jobs.CellCrew(jobs.CellSession(j), zeus, all, members)
				wj.ZeusStatus = crew.ZeusStatus
				for _, a := range crew.Agents {
					wj.Roster = append(wj.Roster, webAgent{Name: a.Name, Model: a.Model, Role: a.Role, Status: a.Status, Working: a.Working})
				}
			}
		} else if j.Started() && j.State != "done" {
			// dead cell: the session is gone but the worktree (and any
			// uncommitted WIP) is still on disk. Health routes the card into
			// BLOCKED immediately — a tick ahead of the daemon persisting the
			// blocked state — and the transcript's last words still surface,
			// located by the stamped session id since no live peer exists.
			wj.CellGone = true
			wj.Health = "cell session is gone — reactivate to resume; done/kill discard the worktree"
			if transcript := core.TranscriptPathForSession(j.Session); transcript != "" {
				wj.Detail = lastAssistantText(transcript)
				wj.LastSent = lastUserSent(transcript)
			}
		}
		wj.Reactivatable = (j.State == "done" || wj.CellGone) && j.Session != ""
		board.Jobs = append(board.Jobs, wj)
	}
	// archived jobs ride the same list under state "archived": no cell, no
	// prereqs, no derivation — just the file, its artifacts, and the two
	// actions (unarchive, delete) keyed on the archive file base
	for _, a := range jobs.LoadArchivedJobs() {
		board.Jobs = append(board.Jobs, webJob{
			Slug: a.ArchivedName, Type: a.Type, Repo: a.Repo,
			State: "archived", Archived: true, DisplaySlug: a.Slug,
			Err: a.Err, Outcome: a.Outcome,
			Artifacts: archivedWebArtifacts(a), ModTime: a.ModTime.Unix(),
		})
	}
	return board
}

// archivedWebArtifacts lists an archived job's artifact copies for the
// wire, served from the archive home.
func archivedWebArtifacts(a *jobs.Job) []webArtifact {
	var out []webArtifact
	for _, p := range jobs.ArchivedArtifactPaths(a) {
		name := strings.TrimPrefix(filepath.Base(p), a.ArchivedName+".")
		out = append(out, webArtifact{Name: name, URL: "/api/artifact/" + a.ArchivedName + "/archive/" + name})
	}
	return out
}

// boardHandler serves the memoized board snapshot.
func boardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(webBoardCache.get())
}

func webJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jobMTime is a job file's mtime as unix milliseconds — the wire format for
// every editor endpoint. UnixMilli (~1.75e12) fits safely in both a Swift
// Int64 and a JS number, unlike UnixNano, which overflows JS's 2^53
// safe-integer range and forced the old string encoding. fi is nil when the
// preceding os.Stat failed; callers get 0 rather than a panic.
func jobMTime(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.ModTime().UnixMilli()
}

// mtimeToken decodes an mtime from the wire as either a JSON number (current
// format) or a legacy JSON string (older clients that still send the
// pre-UnixMilli encoding), normalizing to unix-millis. An empty string or
// JSON null decodes to 0, which callers treat as "skip the optimistic-lock
// check" — the same meaning "0" already carried under the string encoding.
type mtimeToken int64

func (t *mtimeToken) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*t = mtimeToken(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("bad mtime")
	}
	if s == "" {
		*t = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("bad mtime")
	}
	*t = mtimeToken(n)
	return nil
}
