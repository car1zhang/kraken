package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// viewer_jobs.go — the J-toggled job board inside k v: every job with state,
// repo, prereqs and live cell, an inbox of open escalations, and daemon
// health in the header. Claudes that aren't part of a job stay in the
// sessions view, untouched.

// A jobsRow is one selectable line in the jobs view: a job (esc nil) or an
// inbox entry (esc set; job set when the slug resolves).
type jobsRow struct {
	job *Job
	esc *Escalation
}

// buildJobRows derives the selectable rows: board first (lifecycle order),
// then the inbox (open first). Unparseable jobs and unhealthy cells double as
// inbox entries so neither is missable — errors open the file, health rows
// retry the cell.
func buildJobRows(jobs []*Job, escs []*Escalation, health map[string]string) []jobsRow {
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}
	board := append([]*Job(nil), jobs...)
	sortJobs(board, bySlug)
	var rows []jobsRow
	for _, j := range board {
		rows = append(rows, jobsRow{job: j})
	}
	for _, e := range escs {
		if e.Status != "open" {
			continue
		}
		rows = append(rows, jobsRow{job: bySlug[e.JobSlug], esc: e})
	}
	for _, j := range board {
		if j.Err != "" {
			rows = append(rows, jobsRow{job: j, esc: &Escalation{
				JobSlug: j.Slug, Status: "open", Category: "error",
				Title: "unparseable: " + j.Err, File: j.File,
			}})
		}
		if reason := health[j.Slug]; reason != "" {
			rows = append(rows, jobsRow{job: j, esc: &Escalation{
				JobSlug: j.Slug, Status: "open", Category: "health",
				Title: reason,
			}})
		}
	}
	return rows
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
	open := 0
	for _, r := range m.jrows {
		if r.esc != nil {
			open++
		}
	}
	head := daemonHeader(m.daemonSt, m.daemonOK)
	head += paint(true, dim, fmt.Sprintf(" · cells %d/%d", m.liveCells(), cellCap))
	if open > 0 {
		head += paint(true, red, fmt.Sprintf(" · ⚑ %d", open))
	}
	lines = append(lines, viewLine{text: head, row: -1}, viewLine{row: -1})
	if len(m.jrows) == 0 {
		lines = append(lines, viewLine{text: "no jobs in " + plutoDir() + " ({{name}} in a pluto note creates one)", row: -1})
		return lines
	}

	bySlug := map[string]*Job{}
	for _, r := range m.jrows {
		if r.esc == nil {
			bySlug[r.job.Slug] = r.job
		}
	}
	now := time.Now()
	lines = append(lines, viewLine{text: paint(true, cyan, "JOBS"), row: -1})
	header := []string{"  #", "STATE", "JOB", "TYPE", "REPO", "PREREQS", "CELL", "ART", "AGE"}
	for i, h := range header {
		header[i] = paint(true, cyan, h)
	}
	table := [][]string{header}
	repos := repoCandidates()
	live := m.liveSessionSet
	idx := 0
	var boardIdx []int
	for _, r := range m.jrows {
		if r.esc != nil {
			idx++
			continue
		}
		j := r.job
		color := idx != m.cursor
		marker := " "
		if idx == m.cursor {
			marker = "▸"
		}
		state := displayState(j, bySlug)
		stateMark := ""
		if m.healthBySlug[j.Slug] != "" {
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
			marker + paint(color, dim, fmt.Sprintf("%2d", len(boardIdx)+1)),
			paint(color, jobStateColor(state), "● "+state) + stateMark,
			paint(color, cyan, j.Slug),
			j.Type,
			repo,
			prereqCell(j, bySlug, color),
			cell,
			art,
			paint(color, dim, ageStr),
		})
		boardIdx = append(boardIdx, idx)
		idx++
	}
	aligned := formatTableLines(table)
	lines = append(lines, viewLine{text: aligned[0], row: -1})
	for i, l := range aligned[1:] {
		lines = append(lines, viewLine{text: l, row: boardIdx[i]})
	}

	// inbox
	var inbox []viewLine
	idx = 0
	for _, r := range m.jrows {
		if r.esc == nil {
			idx++
			continue
		}
		e := r.esc
		color := idx != m.cursor
		marker := " "
		if idx == m.cursor {
			marker = "▸"
		}
		ageStr := ""
		if !e.ModTime.IsZero() {
			ageStr = age(now.Sub(e.ModTime))
		}
		text := fmt.Sprintf("%s%s %s %s  %s",
			marker,
			paint(color, red, "⚑"),
			paint(color, cyan, e.JobSlug),
			paint(color, dim, e.Category+" "+ageStr),
			truncateOneLine(e.Title, 90))
		inbox = append(inbox, viewLine{text: text, row: idx})
		idx++
	}
	if len(inbox) > 0 {
		lines = append(lines, viewLine{row: -1}, viewLine{text: paint(true, cyan, "INBOX"), row: -1})
		lines = append(lines, inbox...)
	}
	return lines
}

// liveCells counts started jobs whose cell session is up.
func (m *viewerModel) liveCells() int {
	n := 0
	for _, r := range m.jrows {
		if r.esc == nil && r.job.started() && r.job.State != "done" && m.liveSessionSet[cellSession(r.job)] {
			n++
		}
	}
	return n
}

// handleJobsKey handles keys the jobs view owns; shared keys (motion, quit,
// refresh, J) are handled by the caller.
func (m *viewerModel) handleJobsKey(key string) (tea.Model, tea.Cmd) {
	r := m.jselected()
	switch key {
	case "enter":
		if r == nil {
			return m, nil
		}
		if r.esc != nil {
			if r.esc.Category == "error" {
				return m, m.jobsActionCmd("opening job file", func() (string, error) {
					return "opened " + r.esc.File, exec.Command("open", r.esc.File).Run()
				})
			}
			if r.esc.Category == "health" {
				j := r.job
				return m, m.jobsActionCmd("retrying "+j.Slug, func() (string, error) {
					if err := retryCell(j); err != nil {
						return "", err
					}
					return "poked " + j.Slug + " — it should resume", nil
				})
			}
			m.startAnswer(r.esc)
			return m, nil
		}
		// drill into the cell: zeus pane
		if p := m.peerNamed(zeusName(r.job)); p != nil {
			return m, m.jobsActionCmd("opening "+r.job.Slug, func() (string, error) { return openPeer(p) })
		}
		m.note, m.noteIsErr = "no live cell for "+r.job.Slug, true
	case "a":
		if r == nil || r.esc == nil || r.esc.Category == "error" {
			m.note, m.noteIsErr = "a answers an inbox escalation — select one", true
			return m, nil
		}
		if r.esc.Category == "health" {
			return m.handleJobsKey("enter") // health rows retry, not answer
		}
		m.startAnswer(r.esc)
	case "s":
		if r == nil || r.esc != nil {
			return m, nil
		}
		j := r.job
		state := displayState(j, m.jobsBySlug())
		if state != "ready" && state != "gate-review" {
			m.note, m.noteIsErr = j.Slug+" is "+state+" — only ready/gate-review jobs start", true
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
		if r == nil || r.job == nil {
			return m, nil // an orphaned inbox row has no cell to list from
		}
		arts := allArtifactPaths(r.job)
		if len(arts) == 0 {
			m.note, m.noteIsErr = "no artifacts for "+r.job.Slug, true
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
			return fmt.Sprintf("opened %d artifacts for %s", len(arts), r.job.Slug), nil
		})
	case "x":
		if r == nil || r.esc != nil {
			m.pendingKill = ""
			return m, nil
		}
		j := r.job
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
	for _, r := range m.jrows {
		if r.esc == nil {
			bySlug[r.job.Slug] = r.job
		}
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

func (m *viewerModel) jselected() *jobsRow {
	if m.view != "jobs" || m.cursor < 0 || m.cursor >= len(m.jrows) {
		return nil
	}
	return &m.jrows[m.cursor]
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

// startAnswer enters inline answer mode for an escalation; keys are captured
// into m.input until enter (send) or esc (cancel).
func (m *viewerModel) startAnswer(e *Escalation) {
	m.answering = e
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
		e := m.answering
		text := strings.TrimSpace(m.input)
		m.answering = nil
		m.input = ""
		if text == "" {
			m.note, m.noteIsErr = "empty answer — cancelled", true
			return m, nil
		}
		j := m.jobsBySlug()[e.JobSlug]
		if j == nil {
			m.note, m.noteIsErr = "job "+e.JobSlug+" not found — rerun refresh", true
			return m, nil
		}
		return m, m.jobsActionCmd("answering "+e.JobSlug, func() (string, error) {
			if err := answerEscalation(j, e, text); err != nil {
				return "", err
			}
			return "answered " + e.JobSlug + " → hermes", nil
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

// answerPromptLine renders the inline input while answering.
func (m *viewerModel) answerPromptLine() string {
	return paint(true, yellow, "answer "+m.answering.JobSlug+" ⚑ ") + m.input + paint(true, dim, "▏(enter send · esc cancel)")
}
