package core

import (
	"os/exec"
	"regexp"
	"strings"
)

// ApiErrorRe matches the error banners Claude Code paints into its pane when
// the API is unhappy (overload, rate limit, connectivity). Scoped to obvious
// phrasings — a false positive only surfaces a retry button, never acts.
var ApiErrorRe = regexp.MustCompile(`(?i)API Error|overloaded|rate.?limit(ed)?|connection (error|refused|reset)|network error`)

// PaneTail returns the last visible lines of a pane ("" on any failure).
func PaneTail(pane string, n int) string {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// chromeLineRe matches claude's UI furniture: task lists, agent roster,
// recaps, spinners, separators, the input box, status bars, update nags.
// Everything it matches is presentation, not transcript content.
var chromeLineRe = regexp.MustCompile(`^\s*(✔|◼|◯|⧉|※|✻|✘|⏵⏵|📂|❯|↓|─|⎿|⏺ main$|\d+ tasks? \(|Jump to bottom)`)

// TranscriptTail returns the last n content lines of a pane's transcript:
// the visible text minus chrome. Used to decide whether an error banner is
// the LAST thing the agent said (a dead turn) or old history that later
// output has superseded (an agent that recovered and finished).
func TranscriptTail(pane string, n int) []string {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return nil
	}
	var content []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" || chromeLineRe.MatchString(line) {
			continue
		}
		content = append(content, line)
	}
	if len(content) > n {
		content = content[len(content)-n:]
	}
	return content
}
