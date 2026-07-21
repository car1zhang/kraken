package util

import (
	"os"
	"regexp"
)

const (
	Reset   = "\x1b[0m"
	Dim     = "\x1b[2m"
	Cyan    = "\x1b[36m"
	Green   = "\x1b[32m"
	Yellow  = "\x1b[33m"
	Magenta = "\x1b[35m"
	Red     = "\x1b[31m"
	Gray    = "\x1b[90m"
)

func UseColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func Paint(on bool, code, s string) string {
	if !on {
		return s
	}
	return code + s + Reset
}

func StatusColor(status string) string {
	switch status {
	case "running":
		return Green
	case "question", "shell":
		return Yellow
	case "waiting":
		return Red
	default:
		return Gray
	}
}

var AnsiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// VisWidth is a cell's on-screen width: ANSI codes are invisible to the
// terminal but not to a byte-counting aligner like tabwriter.
func VisWidth(s string) int {
	return len([]rune(AnsiRe.ReplaceAllString(s, "")))
}
