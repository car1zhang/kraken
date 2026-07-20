package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// viewer_jobs.go — the J-toggled job board inside k v: every job with state,
// type, repo, prereqs and live cell, plus daemon health in the header.
// Claudes that aren't part of a job stay in the sessions view, untouched.

// buildJobRows derives the selectable rows: the board, in the same lifecycle
// order k j uses (sortJobs — live cells first, then actionable, then
// paperwork).
func buildJobRows(jobs []*Job) []*Job {
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}
	board := append([]*Job(nil), jobs...)
	sortJobs(board, bySlug)
	return board
}

// daemonHeader renders daemon health for the jobs view header line.
func daemonHeader(st daemonStatus, ok bool) string {
	if !ok || !processAlive(st.PID) {
		return paint(true, red, "daemon ○ not running") + paint(true, dim, " (make install loads it)")
	}
	age := time.Since(time.Unix(st.LastTick, 0))
	if age > 3*daemonInterval {
		return paint(true, red, fmt.Sprintf("daemon ● stale (last tick %s ago)", age.Round(time.Second)))
	}
	s := paint(true, green, "daemon ●") + paint(true, dim, fmt.Sprintf(" tick %s ago", age.Round(time.Second)))
	if st.LastError != "" {
		s += paint(true, red, " !"+truncateOneLine(st.LastError, 40))
	}
	return s
}

// buildJobLines renders the jobs view; viewLine.row indexes m.jrows.
func (m *viewerModel) buildJobLines() []viewLine {
	if !m.loaded {
		return []viewLine{{text: paint(true, dim, "discovering…"), row: -1}}
	}
	var lines []viewLine
	head := daemonHeader(m.daemonSt, m.daemonOK)
	head += paint(true, dim, fmt.Sprintf(" · cells %d/%d", m.liveCells(), cellCap))
	lines = append(lines, viewLine{text: head, row: -1}, viewLine{row: -1})
	if len(m.jrows) == 0 {
		lines = append(lines, viewLine{text: "no jobs in " + plutoDir() + " ({{name}} in a pluto note creates one)", row: -1})
		return lines
	}

	bySlug := m.jobsBySlug()
	now := time.Now()
	lines = append(lines, viewLine{text: paint(true, cyan, "JOBS"), row: -1})
	header := []string{"  #", "STATE", "JOB", "TYPE", "REPO", "PREREQS", "CELL", "ART", "AGE"}
	for i, h := range header {
		header[i] = paint(true, cyan, h)
	}
	table := [][]string{header}
	repos := repoCandidates()
	live := m.liveSessionSet
	for idx, j := range m.jrows {
		color := idx != m.cursor
		marker := " "
		if idx == m.cursor {
			marker = "▸"
		}
		state := displayState(j, bySlug)
		// muted detail suffix: an unhealthy live cell or an unparseable job
		// gets its reason surfaced right on the state cell — cheap, since
		// both are already computed for the board (healthBySlug, j.Err) and
		// there's no separate inbox to carry the detail anymore.
		stateMark := ""
		if reason := m.healthBySlug[j.Slug]; reason != "" {
			stateMark = paint(color, red, " ⚠")
		} else if j.Err != "" {
			stateMark = paint(color, red, " ⚠")
		}
		repo := paint(color, dim, "-")
		if j.Repo != "" {
			if hit := pickRepo(j.Repo, repos); hit != "" {
				repo = hit
			} else {
				repo = paint(color, red, j.Repo+"?")
			}
		}
		cell := paint(color, dim, "-")
		if live[cellSession(j)] {
			cell = cellSession(j)
		}
		ageStr := ""
		if !j.ModTime.IsZero() {
			ageStr = age(now.Sub(j.ModTime))
		}
		art := paint(color, dim, "-")
		if n := len(allArtifactPaths(j)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		table = append(table, []string{
			marker + paint(color, dim, fmt.Sprintf("%2d", idx+1)),
			paint(color, jobStateColor(state), "● "+state) + stateMark,
			paint(color, cyan, j.Slug),
			j.Type,
			repo,
			prereqCell(j, bySlug, color),
			cell,
			art,
			paint(color, dim, ageStr),
		})
	}
	aligned := formatTableLines(table)
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
		if j.started() && j.State != "done" && m.liveSessionSet[cellSession(j)] {
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
		// drill into the cell: zeus pane
		if p := m.peerNamed(zeusName(j)); p != nil {
			return m, m.jobsActionCmd("opening "+j.Slug, func() (string, error) { return openPeer(p) })
		}
		m.note, m.noteIsErr = "no live cell for "+j.Slug, true
	case "a":
		if j == nil {
			return m, nil
		}
		state := displayState(j, m.jobsBySlug())
		if state != "blocked" {
			m.note, m.noteIsErr = "a steers a blocked job — select one", true
			return m, nil
		}
		if m.peerNamed(zeusName(j)) == nil {
			m.note, m.noteIsErr = "no live cell for "+j.Slug, true
			return m, nil
		}
		m.startAnswer(j)
	case "s":
		if j == nil {
			return m, nil
		}
		bySlug := m.jobsBySlug()
		state := displayState(j, bySlug)
		if state != "queued" {
			m.note, m.noteIsErr = j.Slug+" is "+state+" — only queued jobs start", true
			return m, nil
		}
		if unmet := unmetPrereqs(j, bySlug); len(unmet) > 0 {
			var names []string
			for _, p := range unmet {
				names = append(names, p.Job)
			}
			m.note, m.noteIsErr = j.Slug+" has unmet prereqs: "+strings.Join(names, ", "), true
			return m, nil
		}
		return m, m.jobsActionCmd("starting "+j.Slug, func() (string, error) {
			repo, err := resolveRepo(j.Repo)
			if err != nil {
				return "", err
			}
			if err := startCell(j, repo); err != nil {
				return "", err
			}
			return "started " + j.Slug + " (" + cellSession(j) + ")", nil
		})
	case "A":
		if j == nil {
			return m, nil
		}
		arts := allArtifactPaths(j)
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
		if !j.started() || j.State == "done" {
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
func abandonJob(j *Job) (string, error) {
	sess := cellSession(j)
	if liveSessions()[sess] {
		if err := verifyCellSession(sess, slugify(j.Slug)); err != nil {
			return "", err
		}
		if err := exec.Command("tmux", "kill-session", "-t", sess).Run(); err != nil {
			return "", fmt.Errorf("tmux kill-session: %v", err)
		}
	}
	if err := writeJobState(j, "abandoned"); err != nil {
		return "", err
	}
	return "abandoned " + j.Slug + " — worktree kept for autopsy", nil
}

func (m *viewerModel) jobsBySlug() map[string]*Job {
	bySlug := map[string]*Job{}
	for _, j := range m.jrows {
		bySlug[j.Slug] = j
	}
	return bySlug
}

func (m *viewerModel) peerNamed(name string) *Peer {
	for _, p := range m.rawPeers {
		if p.Name == name && p.Messageable && p.Depth == 0 {
			return p
		}
	}
	return nil
}

func (m *viewerModel) jselected() *Job {
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
func (m *viewerModel) startAnswer(j *Job) {
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
		zeus := m.peerNamed(zeusName(j))
		if zeus == nil {
			m.note, m.noteIsErr = "no live cell for "+j.Slug+" — rerun refresh", true
			return m, nil
		}
		return m, m.jobsActionCmd("answering "+j.Slug, func() (string, error) {
			if err := sendMessage(zeus, text); err != nil {
				return "", err
			}
			if err := appendMidRunLog(j, "me→zeus", "steering: "+oneLine(text)); err != nil {
				return "", err
			}
			if err := writeJobState(j, "working"); err != nil {
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
	return paint(true, yellow, "steer "+m.answering.Slug+" → zeus ") + m.input + paint(true, dim, "▏(enter send · esc cancel)")
}
