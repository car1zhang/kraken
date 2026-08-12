package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// viewer_jobs.go — the J-toggled job board inside k v: every job with state,
// repo, crew and live cell, plus daemon health in the header. Claudes
// that aren't part of a job stay in the sessions view, untouched.

// buildJobRows derives the selectable rows: the board, in the same lifecycle
// order the web board uses (sortJobs — live cells first, then actionable,
// then paperwork).
func buildJobRows(js []*jobs.Job) []*jobs.Job {
	board := append([]*jobs.Job(nil), js...)
	jobs.SortJobs(board)
	return board
}

// daemonHeader renders daemon health for the jobs view header line.
func daemonHeader(st jobs.DaemonStatus, ok bool) string {
	if !ok || !jobs.ProcessAlive(st.PID) {
		return util.Paint(true, util.Red, "daemon ○ not running") + util.Paint(true, util.Dim, " (make install loads it)")
	}
	age := time.Since(time.Unix(st.LastTick, 0))
	if age > 3*jobs.DaemonInterval {
		return util.Paint(true, util.Red, fmt.Sprintf("daemon ● stale (last tick %s ago)", age.Round(time.Second)))
	}
	s := util.Paint(true, util.Green, "daemon ●") + util.Paint(true, util.Dim, fmt.Sprintf(" tick %s ago", age.Round(time.Second)))
	if st.LastError != "" {
		s += util.Paint(true, util.Red, " !"+core.TruncateOneLine(st.LastError, 40))
	}
	return s
}

// crewRoleOrder lists the roles crewCell renders, in briefing order with
// "other" last, alongside the single-letter abbreviation shown per role.
var crewRoleOrder = []struct{ role, abbrev string }{
	{"hephaestus", "h"},
	{"athena", "a"},
	{"hermes", "m"},
	{"other", "o"},
}

// crewCell renders one job's live crew for the CREW column: a zeus marker
// (z● running, z⚑ question, z○ anything else, omitted entirely when
// ZeusStatus is "") followed by per-role working/total counts in
// crewRoleOrder, e.g. "z● h1/2 a0/1". Pure and color-toggleable so it's
// unit-testable without a viewerModel.
func crewCell(sum jobs.CrewSummary, color bool) string {
	var parts []string
	switch sum.ZeusStatus {
	case "running":
		parts = append(parts, util.Paint(color, util.Green, "z●"))
	case "polling":
		// mid poll-loop (pending ScheduleWakeup): live work, not idleness
		parts = append(parts, util.Paint(color, util.Cyan, "z↻"))
	case "question":
		parts = append(parts, util.Paint(color, util.Yellow, "z⚑"))
	case "":
		// no zeus yet — omit the marker entirely
	default:
		parts = append(parts, util.Paint(color, util.Dim, "z○"))
	}

	type counts struct{ working, total int }
	byRole := map[string]counts{}
	for _, a := range sum.Agents {
		c := byRole[a.Role]
		c.total++
		if a.Working {
			c.working++
		}
		byRole[a.Role] = c
	}
	for _, r := range crewRoleOrder {
		c, ok := byRole[r.role]
		if !ok {
			continue
		}
		s := fmt.Sprintf("%s%d/%d", r.abbrev, c.working, c.total)
		if c.working > 0 {
			parts = append(parts, util.Paint(color, util.Green, s))
		} else {
			parts = append(parts, util.Paint(color, util.Dim, s))
		}
	}

	if len(parts) == 0 {
		return util.Paint(color, util.Dim, "-")
	}
	return strings.Join(parts, " ")
}

// slugColor is the ANSI code the JOB cell paints a job's slug with: the
// job's own chosen colour when it has one, else the plain Cyan every job
// used before colours existed. Identity only — the STATE cell keeps
// jobs.JobStateColor, so state and identity stay separate channels and a
// coloured job's state chip still reads as its state.
func slugColor(j *jobs.Job) string {
	if c, ok := jobs.ColorByName(jobs.EffectiveColor(j)); ok {
		return util.Color256(c.Term256)
	}
	return util.Cyan
}

// buildJobLines renders the jobs view; viewLine.row indexes m.jrows.
func (m *viewerModel) buildJobLines() []viewLine {
	if !m.loaded {
		return []viewLine{{text: util.Paint(true, util.Dim, "discovering…"), row: -1}}
	}
	var lines []viewLine
	head := daemonHeader(m.daemonSt, m.daemonOK)
	// live count only: there is no cap any more (the queue pass that enforced
	// one is gone), so a "N/M" here would name a limit nothing applies.
	head += util.Paint(true, util.Dim, fmt.Sprintf(" · cells %d", m.liveCells()))
	lines = append(lines, viewLine{text: head, row: -1}, viewLine{row: -1})
	if len(m.jrows) == 0 {
		lines = append(lines, viewLine{text: "no jobs in " + jobs.PlutoDir() + " ({{name}} in a pluto note creates one)", row: -1})
		return lines
	}

	now := time.Now()
	lines = append(lines, viewLine{text: util.Paint(true, util.Cyan, "JOBS"), row: -1})
	header := []string{"  #", "STATE", "JOB", "REPO", "CREW", "CELL", "ART", "AGE"}
	for i, h := range header {
		header[i] = util.Paint(true, util.Cyan, h)
	}
	table := [][]string{header}
	repos := jobs.RepoCandidates()
	live := m.liveSessionSet
	for idx, j := range m.jrows {
		color := idx != m.cursor
		marker := " "
		if idx == m.cursor {
			marker = "▸"
		}
		state := jobs.DisplayState(j)
		// muted detail suffix: an unhealthy live cell or an unparseable job
		// gets its reason surfaced right on the state cell — cheap, since
		// both are already computed for the board (healthBySlug, j.Err) and
		// there's no separate inbox to carry the detail anymore.
		stateMark := ""
		if reason := m.healthBySlug[j.Slug]; reason != "" {
			stateMark = util.Paint(color, util.Red, " ⚠")
		} else if j.Err != "" {
			stateMark = util.Paint(color, util.Red, " ⚠")
		}
		repo := util.Paint(color, util.Dim, "-")
		if j.Repo != "" {
			if hit := jobs.PickRepo(j.Repo, repos); hit != "" {
				repo = hit
			} else {
				repo = util.Paint(color, util.Red, j.Repo+"?")
			}
		}
		cell := util.Paint(color, util.Dim, "-")
		crew := util.Paint(color, util.Dim, "-")
		if live[jobs.CellSession(j)] {
			cell = jobs.CellSession(j)
			if sum, ok := m.crewBySlug[j.Slug]; ok {
				crew = crewCell(sum, color)
			}
		}
		ageStr := ""
		if !j.ModTime.IsZero() {
			ageStr = util.Age(now.Sub(j.ModTime))
		}
		art := util.Paint(color, util.Dim, "-")
		if n := len(jobs.AllArtifactPaths(j)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		table = append(table, []string{
			marker + util.Paint(color, util.Dim, fmt.Sprintf("%2d", idx+1)),
			util.Paint(color, jobs.JobStateColor(state), "● "+state) + stateMark,
			util.Paint(color, slugColor(j), j.Slug),
			repo,
			crew,
			cell,
			art,
			util.Paint(color, util.Dim, ageStr),
		})
	}
	aligned := util.FormatTableLines(table)
	lines = append(lines, viewLine{text: aligned[0], row: -1})
	for i, l := range aligned[1:] {
		lines = append(lines, viewLine{text: l, row: i})
	}
	return lines
}

// liveCells counts started jobs whose cell session is up.
func (m *viewerModel) liveCells() int {
	n := 0
	for _, j := range m.jrows {
		if j.Started() && j.State != "done" && m.liveSessionSet[jobs.CellSession(j)] {
			n++
		}
	}
	return n
}

// handleJobsKey handles keys the jobs view owns; shared keys (motion, quit,
// refresh, J) are handled by the caller.
func (m *viewerModel) handleJobsKey(key string) (tea.Model, tea.Cmd) {
	j := m.jselected()
	switch key {
	case "enter":
		if j == nil {
			return m, nil
		}
		// drill into the cell: zeus pane. Every name the job has held —
		// a renamed cell's zeus still carries its spawn-time one.
		if p := m.peerNamedAny(jobs.ZeusNameCandidates(j)); p != nil {
			return m, m.jobsActionCmd("opening "+j.Slug, func() (string, error) { return openSelectedPeer(p) })
		}
		m.note, m.noteIsErr = "no live cell for "+j.Slug, true
	case "a":
		if j == nil {
			return m, nil
		}
		state := jobs.DisplayState(j)
		if state != "blocked" {
			m.note, m.noteIsErr = "a steers a blocked job — select one", true
			return m, nil
		}
		// candidate-aware for the reason the send path below is: refusing
		// here on a renamed cell means carl cannot answer the very block he
		// is looking at, and the composer never opens to tell him why.
		if m.peerNamedAny(jobs.ZeusNameCandidates(j)) == nil {
			m.note, m.noteIsErr = "no live cell for "+j.Slug, true
			return m, nil
		}
		m.startAnswer(j)
	case "s":
		if j == nil {
			return m, nil
		}
		// One pre-check, and it is an ACCIDENT GUARD rather than validation:
		// jobs.SubmitJob owns every real refusal (unparseable, done, already
		// started, empty spec, unresolvable repo) and its message is the one
		// the board shows too, so duplicating any of them here is how the two
		// paths drifted apart before — the viewer used to gate on "queued" and
		// call StartCell directly, which is exactly how it came to skip the
		// empty-spec check the board's queue action made.
		//
		// What this guard is for is the keystroke itself. `s` used to move a
		// job between two columns; it now cuts a git worktree and spawns a
		// cell, on whatever row the cursor happens to be sitting on, with no
		// confirmation step. Refusing it anywhere but the draft column costs
		// nothing and turns a mistyped key on a live job into a status line.
		if state := jobs.DisplayState(j); state != "draft" {
			m.note, m.noteIsErr = j.Slug+" is "+state+" — s starts a draft", true
			return m, nil
		}
		return m, m.jobsActionCmd("submitting "+j.Slug, func() (string, error) {
			if err := jobs.SubmitJob(j); err != nil {
				return "", err
			}
			return "started " + j.Slug + " (" + jobs.CellSession(j) + ")", nil
		})
	case "A":
		if j == nil {
			return m, nil
		}
		arts := jobs.AllArtifactPaths(j)
		if len(arts) == 0 {
			m.note, m.noteIsErr = "no artifacts for "+j.Slug, true
			return m, nil
		}
		return m, m.jobsActionCmd("opening artifacts", func() (string, error) {
			// one open call, all files: each lands in its default app
			if err := exec.Command("open", arts...).Run(); err != nil {
				return "", fmt.Errorf("open artifacts: %v", err)
			}
			if len(arts) == 1 {
				return "opened " + arts[0], nil
			}
			return fmt.Sprintf("opened %d artifacts for %s", len(arts), j.Slug), nil
		})
	case "x":
		if j == nil {
			m.pendingKill = ""
			return m, nil
		}
		if !j.Started() || j.State == "done" {
			m.note, m.noteIsErr = j.Slug+" has no cell to abandon", true
			return m, nil
		}
		if m.pendingKill == "job:"+j.Slug {
			m.pendingKill = ""
			return m, m.jobsActionCmd("abandoning "+j.Slug, func() (string, error) {
				return abandonJob(j)
			})
		}
		m.pendingKill = "job:" + j.Slug
		m.note, m.noteIsErr = "x again to abandon "+j.Slug+" (kills the cell, keeps the worktree)", false
	}
	return m, nil
}

// abandonJob kills a job's cell session (verified) but keeps the worktree for
// autopsy, stamping state abandoned.
//
// The session is resolved and verified against every name the job has held —
// jobs.ProbeCellSession and jobs.CellMarkerSlugs — because a renamed cell
// whose tmux convergence failed is still running under an older session name,
// and a current-name-only read answers wrongly twice over: it misses the
// session, and it verifies against a marker the pane does not carry.
//
// The three outcomes ProbeCellSession distinguishes are exactly the three this
// verb needs, and collapsing any two of them breaks it in one direction or the
// other:
//
//   - A live session is verified, killed, and the job stamped.
//   - A CONFIRMED-dead cell is stamped with no kill. That is the verb's whole
//     purpose rather than an edge case: abandon means keep the worktree for
//     autopsy, and the alternatives (Kill, Done) both run teardownCell, which
//     `git worktree remove`s and deletes the cell dir. Refusing here would
//     leave a job whose session died with no path anywhere that clears it and
//     preserves the tree — the daemon's cell-gone handling DETECTS the state
//     and notifies; recovery stays carl's explicit reactivate/done/kill.
//   - An unanswered tmux is refused. "abandoned" is not in jobs.Started(), so
//     once stamped the daemon's cell loop never looks at the job again; doing
//     that on the strength of a failed tmux call leaves a live zeus nothing
//     will ever collect. Absence of evidence is the one case that must not act.
func abandonJob(j *jobs.Job) (string, error) {
	sess, known := jobs.ProbeCellSession(j)
	if !known {
		return "", fmt.Errorf("cannot reach tmux to check %s's cell — not stamping it abandoned while a live session may still be out there; state left at %s", j.Slug, j.State)
	}
	if sess == "" {
		if err := jobs.WriteJobState(j, "abandoned"); err != nil {
			return "", err
		}
		return "abandoned " + j.Slug + " — cell session already gone, worktree kept for autopsy", nil
	}
	if err := jobs.VerifyCellSession(sess, jobs.CellMarkerSlugs(j)...); err != nil {
		return "", err
	}
	if err := exec.Command("tmux", "kill-session", "-t", sess).Run(); err != nil {
		return "", fmt.Errorf("tmux kill-session: %v", err)
	}
	if err := jobs.WriteJobState(j, "abandoned"); err != nil {
		return "", err
	}
	return "abandoned " + j.Slug + " — worktree kept for autopsy", nil
}

func (m *viewerModel) jobsBySlug() map[string]*jobs.Job {
	bySlug := map[string]*jobs.Job{}
	for _, j := range m.jrows {
		bySlug[j.Slug] = j
	}
	return bySlug
}

// peerNamedAny finds the messageable depth-0 peer answering to any of names,
// trying them in order. It replaced a single-name peerNamed because all three
// of that function's callers resolve a job's ZEUS, and zeus keeps the --name
// it was spawned with: after a rename the live agent answers to an older name
// forever, so one name was never enough to find it.
//
// Order matters and is the caller's: jobs.ZeusNameCandidates puts the current
// name first, so a cell that has converged is matched on its first try and the
// former names are only ever reached by a cell that has not.
func (m *viewerModel) peerNamedAny(names []string) *core.Peer {
	for _, name := range names {
		for _, p := range m.rawPeers {
			if p.Name == name && p.Messageable && p.Depth == 0 {
				return p
			}
		}
	}
	return nil
}

func (m *viewerModel) jselected() *jobs.Job {
	if m.view != "jobs" || m.cursor < 0 || m.cursor >= len(m.jrows) {
		return nil
	}
	return m.jrows[m.cursor]
}

// jobsActionCmd mirrors actOnSelected for jobs-view actions: run off the UI
// goroutine, report through actionMsg.
func (m *viewerModel) jobsActionCmd(verb string, f func() (string, error)) tea.Cmd {
	m.note, m.noteIsErr = verb+"…", false
	return func() tea.Msg {
		note, err := f()
		return actionMsg{note, err}
	}
}

// --- answer input ---

// startAnswer enters inline steering-message mode for a blocked job; keys
// are captured into m.input until enter (send) or esc (cancel).
func (m *viewerModel) startAnswer(j *jobs.Job) {
	m.answering = j
	m.input = ""
	m.note, m.noteIsErr = "", false
}

func (m *viewerModel) handleAnswerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.answering = nil
		m.input = ""
		m.note, m.noteIsErr = "answer cancelled", false
	case tea.KeyEnter:
		j := m.answering
		text := strings.TrimSpace(m.input)
		m.answering = nil
		m.input = ""
		if text == "" {
			m.note, m.noteIsErr = "empty answer — cancelled", true
			return m, nil
		}
		// the costliest of the three to get wrong: carl has already typed the
		// answer, and a nil zeus here discards it with a message that reads
		// like a stale snapshot. On a renamed cell the single-name lookup
		// missed EVERY time, so the text was never recoverable by refreshing.
		zeus := m.peerNamedAny(jobs.ZeusNameCandidates(j))
		if zeus == nil {
			m.note, m.noteIsErr = "no live cell for "+j.Slug+" — rerun refresh", true
			return m, nil
		}
		return m, m.jobsActionCmd("answering "+j.Slug, func() (string, error) {
			if err := core.SendMessage(zeus, text); err != nil {
				return "", err
			}
			if err := jobs.WriteJobState(j, "working"); err != nil {
				return "", err
			}
			return "sent → zeus", nil
		})
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			rs := []rune(m.input)
			m.input = string(rs[:len(rs)-1])
		}
	case tea.KeySpace:
		m.input += " "
	case tea.KeyRunes:
		m.input += string(msg.Runes)
	}
	return m, nil
}

// answerPromptLine renders the inline input while steering a blocked job.
func (m *viewerModel) answerPromptLine() string {
	return util.Paint(true, util.Yellow, "steer "+m.answering.Slug+" → zeus ") + m.input + util.Paint(true, util.Dim, "▏(enter send · esc cancel)")
}
