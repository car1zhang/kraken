package util

import (
	"fmt"
	"strings"
	"time"
)

// FormatTableLines aligns rows into columns (ANSI-aware) and returns one
// string per row, without trailing newlines.
func FormatTableLines(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if w := VisWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-VisWidth(c)+2))
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}

func PrintTable(rows [][]string) {
	for _, line := range FormatTableLines(rows) {
		fmt.Println(line)
	}
}

func Age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
