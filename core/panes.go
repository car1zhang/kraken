package core

import (
	"os/exec"
	"strings"
)

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
