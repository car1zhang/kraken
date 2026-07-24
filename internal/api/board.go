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

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
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

// jobArtifacts collects a job's viewable artifacts. A live cell always
// serves straight from the cell dir (Q12), unchanged regardless of storage
// mode. Once the cell is gone: file mode keeps its original pluto-copy
// listing; store mode (L4+E6) UNIONS three sources — the (normally empty,
// post-teardown) cell-dir walk, the artifacts table (the block-event
// sync's mirror, which — unlike the pluto flat-copy preservation — keeps
// nested subdirs), and the pluto flat <slug>.<name> copies — deduped by
// name with precedence cell > store > pluto. The pluto source can't be
// dropped in store mode: an info job's root-level *.md docs are preserved
// straight from the worktree root by cell.go's preserveArtifacts, never
// through CellArtifacts()/artifacts/, so jobs/sync.go's syncCellArtifacts
// (which only ever mirrors the artifacts/ subdir) never captures them into
// the store's artifacts table at all — the pluto flat copy is the SOLE
// record of that deliverable, store mode or not. Dropping it (the
// pre-L4/E6 behavior: store mode returned cell ∪ store only) made an info
// job's doc vanish from the board entirely the moment its cell was torn
// down.
func jobArtifacts(j *jobs.Job, live bool) []webArtifact {
	seen := map[string]bool{}
	var out []webArtifact
	add := func(entries []webArtifact) {
		for _, e := range entries {
			if seen[e.Name] {
				continue // lower-precedence source already lost this name
			}
			seen[e.Name] = true
			out = append(out, e)
		}
	}

	root := jobs.CellArtifacts(j)
	var cellEntries []webArtifact
	for _, p := range jobs.ListArtifacts(j) {
		// name is the path relative to the artifacts dir, so nested files
		// read as "icon-variations/icon-v1.png" and clients can group by dir
		name := filepath.Base(p)
		if rel, err := filepath.Rel(root, p); err == nil {
			name = rel
		}
		cellEntries = append(cellEntries, webArtifact{Name: name, URL: "/api/artifact/" + j.Slug + "/cell/" + name})
	}
	add(cellEntries)

	if st := jobs.ActiveStore(); st != nil {
		// Q12's "a live cell always serves straight from the cell dir"
		// short-circuit is a store-mode-only rule: file mode's pre-migration
		// behavior always appended the pluto flat-copy listing regardless of
		// liveness, and that must stay exactly as it was — only
		// gate on live when a store is actually wired.
		if live {
			return out
		}
		add(storeArtifactList(j.Slug, j.ID, "pluto"))
		add(plutoFlatArtifacts(j))
		return out
	}
	add(plutoFlatArtifacts(j))
	return out
}

// plutoFlatArtifacts scans PlutoDir for j's <slug>.<name> flat copies —
// preserveArtifacts' legacy preservation format (cell.go). Pulled out of
// jobArtifacts so both the file-mode branch and the store-mode union
// (L4+E6) share one implementation.
func plutoFlatArtifacts(j *jobs.Job) []webArtifact {
	prefix := util.Slugify(j.Slug) + "."
	entries, err := os.ReadDir(jobs.PlutoDir())
	if err != nil {
		return nil
	}
	var out []webArtifact
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, jobs.JobSuffix) {
			continue
		}
		out = append(out, webArtifact{Name: strings.TrimPrefix(name, prefix), URL: "/api/artifact/" + j.Slug + "/pluto/" + strings.TrimPrefix(name, prefix)})
	}
	return out
}

// storeArtifactList lists jobID's DB-mirrored artifacts (store/artifacts.go,
// populated by the block-event sync — jobs/sync.go) as webArtifacts under
// the given URL "home" segment. The URL scheme is unchanged from file mode
// on purpose (Q12 only moves where the BYTES are served from, not the wire
// shape): ArtifactBytes/artifactHandler resolve any non-"cell" home through
// the store first whenever one is wired, falling back to ArtifactPath's
// filesystem lookup otherwise. home is "pluto" for an active (done) job's
// artifacts, "archive" for an archived job's.
func storeArtifactList(slug string, jobID int64, home string) []webArtifact {
	st := jobs.ActiveStore()
	if st == nil || jobID == 0 {
		return nil
	}
	rows, err := st.ListArtifacts(jobID)
	if err != nil {
		return nil
	}
	out := make([]webArtifact, 0, len(rows))
	for _, a := range rows {
		out = append(out, webArtifact{Name: a.Path, URL: "/api/artifact/" + slug + "/" + home + "/" + a.Path})
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

// ArtifactBytes is ArtifactPath's store-backed twin (Q12): resolves
// job+name straight from the artifacts table instead of a filesystem home,
// for any non-"cell" home once a store is wired — a done/archived job's
// "pluto"/"archive" homes no longer need a file on disk to serve from. ok
// is false whenever no store is wired, j has no store row, or the path
// isn't found in the table — callers fall back to ArtifactPath, which is
// always safe (preserveArtifacts/movePrefixed still write the file copies
// too, per jobs/cell.go and jobs/archive.go's own store-mode branches).
//
// L11: home is now a required parameter, refused outright when "cell" —
// Q12's rule is that a live cell's artifacts are NEVER DB-backed, only
// ever served straight off disk. Without this, a caller that fell back to
// ArtifactBytes after an ArtifactPath("cell", ...) miss (feedback.go does
// exactly this: ArtifactPath first, ArtifactBytes as the fallback) could
// have the DB serve bytes for a same-named artifact that actually lives
// under a DIFFERENT home (e.g. a "pluto" flat copy sharing a filename with
// a would-be cell file) — silently mislabeling which home the returned
// bytes actually came from. Also refuses any name ending in jobs.JobSuffix
// — mirroring ArtifactPath's own "job files are never artifacts" refusal,
// which ArtifactBytes never had (a name reconstructing to a job file's own
// suffix must never be servable as an artifact, DB-backed or not).
func ArtifactBytes(j *jobs.Job, home, name string) (data []byte, mimeType string, ok bool) {
	if home == "cell" {
		return nil, "", false
	}
	st := jobs.ActiveStore()
	if st == nil || j.ID == 0 {
		return nil, "", false
	}
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return nil, "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return nil, "", false // no traversal, no empty/hidden segments — mirrors ArtifactPath
		}
	}
	if strings.HasSuffix(name, jobs.JobSuffix) {
		return nil, "", false
	}
	a, err := st.GetArtifact(j.ID, name)
	if err != nil {
		return nil, "", false
	}
	return a.Bytes, artifactMimeType(name), true
}

// artifactMimeType mirrors web/serve.go's artifactContentType (same
// extension → type mapping), duplicated here since api sits below web in
// the dependency chain and DB-served bytes never pass through
// http.ServeFile's own path-based content-type sniffing.
func artifactMimeType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "text/plain; charset=utf-8"
	}
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
	order := jobs.LoadBoardOrder()
	byState := map[string][]webJob{}
	for _, j := range js {
		var prereqs []webPrereq
		for _, p := range j.Prereqs {
			dep := bySlug[p.Job]
			prereqs = append(prereqs, webPrereq{Job: p.Job, Gated: p.Gated, Done: dep != nil && dep.State == "done"})
		}
		wj := webJob{
			Slug: j.Slug, Type: j.Type, Repo: j.Repo,
			State: jobs.DisplayState(j, bySlug), Err: j.Err, Outcome: j.Outcome,
			Prereqs: prereqs, Artifacts: jobArtifacts(j, live[jobs.CellSession(j)]),
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
		byState[wj.State] = append(byState[wj.State], wj)
	}
	// emit each display-state group in lifecycle order, within-group order
	// coming from the manual board reorder (jobs/order.go): members not yet
	// in the stored order land on top (slug-sorted, since js already is),
	// then stored members in their saved order — the flat array's
	// within-state order IS the wire contract, no new key needed.
	for _, state := range []string{"draft", "queued", "working", "blocked", "done"} {
		group := byState[state]
		// slug -> queue, not a single value: duplicate-slug job files (both
		// erred into draft) must each keep their own card, so consume one
		// entry per emitted slug — ApplyColumnOrder preserves multiplicity
		wjBySlug := make(map[string][]webJob, len(group))
		slugs := make([]string, len(group))
		for i, wj := range group {
			slugs[i] = wj.Slug
			wjBySlug[wj.Slug] = append(wjBySlug[wj.Slug], wj)
		}
		for _, slug := range jobs.ApplyColumnOrder(slugs, order[state]) {
			board.Jobs = append(board.Jobs, wjBySlug[slug][0])
			wjBySlug[slug] = wjBySlug[slug][1:]
		}
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
// wire: store mode lists from the DB (storeArtifactList), file mode from
// the archive home on disk — same split as jobArtifacts' non-live branch.
func archivedWebArtifacts(a *jobs.Job) []webArtifact {
	if jobs.ActiveStore() != nil {
		return storeArtifactList(a.ArchivedName, a.ID, "archive")
	}
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
