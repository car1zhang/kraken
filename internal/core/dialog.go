package core

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// dialog.go reads a modal option list off the bottom of a captured pane and
// turns a chosen option into the keys that select it.
//
// Claude Code draws its dialogs (theme picker, login method, folder trust) as
// a block of option lines near the bottom of the pane, one of them led by a
// cursor glyph: `❯` in Claude Code, `›` in codex, `>` in a terminal that
// renders neither. Options come in two shapes. Numbered ones read
// `❯ 1. Claude account with subscription` over `  2. Anthropic Console
// account`; un-numbered ones read `❯ ✔ Dark mode` over `    Light mode`, where
// `✔` marks the current setting. An un-numbered neighbour has nothing on it
// that says "option", so the parser recognises it by position instead: its
// text starts in the same column as the cursor line's text once the glyph,
// the mark and any number are stripped. Header prose, rule lines and the
// "Enter to confirm" footer all start further left, so they end the run.
//
// This generalises trustOptionLine/trustAcceptKeys in internal/jobs/cell.go,
// which only know the trust dialog's yes/no wording.

// DialogOption is one option line of a parsed dialog.
type DialogOption struct {
	// Text is the option with the cursor glyph, the ✔ mark and a leading
	// "N. " number stripped, trimmed.
	Text string
	// Line is the 0-based index of the line in the text handed to ParseDialog.
	Line int
	// Cursor is true on the one line led by the cursor glyph.
	Cursor bool
	// Number is the N of a numbered option, 0 when un-numbered.
	Number int
}

// Dialog is a parsed option list. Cursor indexes Options, -1 when no option
// carries the cursor; Header is the prose just above the options.
type Dialog struct {
	Header  string
	Options []DialogOption
	Cursor  int
}

const (
	// dialogWindow is how many lines from the bottom of the pane ParseDialog
	// reads, so scrollback above a dialog cannot contribute a second cursor.
	dialogWindow = 25
	// dialogHeaderLines is how far above the first option the header reaches.
	dialogHeaderLines = 8
	// dialogHeaderMinLetters drops splash art and rule lines from the header.
	dialogHeaderMinLetters = 3
)

// dialogNumbered matches a numbered option line as tmux captures it:
// "  2. Anthropic Console account".
var dialogNumbered = regexp.MustCompile(`^\s*\d+\.\s`)

// dialogLine is one line split into its parts. col is the rune column where
// the option text starts after the glyph, the mark and the number.
type dialogLine struct {
	cursor bool
	text   string
	number int
	col    int
}

// splitDialogLine strips a line's leading spaces, optional cursor glyph,
// optional ✔ mark and optional "N. " number, recording where the remaining
// text starts. cursor is set only when the first non-space rune is a glyph
// and, for `>`, something non-blank follows it, so an empty `>` input prompt
// never counts as a cursor.
func splitDialogLine(line string) dialogLine {
	r := []rune(line)
	i := 0
	skipSpace := func() {
		for i < len(r) && unicode.IsSpace(r[i]) {
			i++
		}
	}
	skipSpace()
	var out dialogLine
	if i < len(r) && (r[i] == '❯' || r[i] == '›' || r[i] == '>') {
		glyph := r[i]
		rest := strings.TrimSpace(string(r[i+1:]))
		if glyph != '>' || rest != "" {
			out.cursor = true
			i++
			skipSpace()
		}
	}
	if i < len(r) && r[i] == '✔' {
		i++
		skipSpace()
	}
	// "N. " — digits, a dot, then at least one space.
	j := i
	for j < len(r) && r[j] >= '0' && r[j] <= '9' {
		j++
	}
	if j > i && j+1 < len(r) && r[j] == '.' && unicode.IsSpace(r[j+1]) {
		if n, err := strconv.Atoi(string(r[i:j])); err == nil {
			out.number = n
			i = j + 1
			skipSpace()
		}
	}
	out.col = i
	out.text = strings.TrimSpace(string(r[i:]))
	return out
}

// ParseDialog finds the option list at the bottom of a captured pane. It reads
// only the last 25 lines, and needs exactly one of them to be led by a cursor
// glyph. The run is the cursor line plus the contiguous lines above and below
// it (no blank line in between) that are numbered or whose text starts in the
// cursor line's text column; the first line that is neither ends the run in
// that direction. When the cursor line is numbered, an un-numbered line in the
// run is folded into the option above it, and the parse needs at least two
// options after that fold. Any failure returns a zero Dialog and false.
func ParseDialog(paneText string) (Dialog, bool) {
	if paneText == "" {
		return Dialog{}, false
	}
	lines := strings.Split(paneText, "\n")
	start := 0
	if len(lines) > dialogWindow {
		start = len(lines) - dialogWindow
	}
	parsed := make([]dialogLine, len(lines))
	cursor := -1
	for i := start; i < len(lines); i++ {
		lines[i] = strings.TrimRight(lines[i], "\r")
		parsed[i] = splitDialogLine(lines[i])
		if parsed[i].cursor {
			if cursor >= 0 {
				return Dialog{}, false
			}
			cursor = i
		}
	}
	if cursor < 0 || parsed[cursor].text == "" {
		return Dialog{}, false
	}
	col := parsed[cursor].col
	member := func(i int) bool {
		if strings.TrimSpace(lines[i]) == "" || parsed[i].text == "" {
			return false
		}
		return dialogNumbered.MatchString(lines[i]) || parsed[i].col == col
	}
	first, last := cursor, cursor
	for first-1 >= start && member(first-1) {
		first--
	}
	for last+1 < len(lines) && member(last+1) {
		last++
	}
	if first == last {
		return Dialog{}, false
	}

	// When the cursor line is numbered, only numbered lines are options. A
	// same-column line without a number belongs to the option above it (a
	// narrow pane's wrap, or a description line), so it is folded into that
	// option's Text; counting it as an option would shift every arrow count
	// below it. A same-column line above the first numbered option has
	// nothing to fold into and is dropped.
	numbered := parsed[cursor].number > 0
	d := Dialog{Cursor: -1}
	for i := first; i <= last; i++ {
		p := parsed[i]
		if numbered && p.number == 0 {
			if n := len(d.Options); n > 0 {
				d.Options[n-1].Text += " " + p.text
			}
			continue
		}
		if p.cursor {
			d.Cursor = len(d.Options)
		}
		d.Options = append(d.Options, DialogOption{Text: p.text, Line: i, Cursor: p.cursor, Number: p.number})
	}
	if len(d.Options) < 2 {
		return Dialog{}, false
	}

	var header []string
	for i := max(start, first-dialogHeaderLines); i < first; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || letterCount(t) < dialogHeaderMinLetters {
			continue
		}
		header = append(header, t)
	}
	d.Header = strings.Join(header, "\n")
	return d, true
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// DialogKeys returns the `tmux send-keys` key names that select
// d.Options[pick]. With a known cursor it moves there with Down or Up and
// presses Enter. Without one it types a numbered option's digit and nothing
// else: Claude Code's numbered selects pick an option on its digit alone
// (job 666's contract; not re-measured here). An un-numbered option without a
// cursor, an out-of-range pick, or a cursor index past the end of Options
// gives nil.
func DialogKeys(d Dialog, pick int) []string {
	if pick < 0 || pick >= len(d.Options) || d.Cursor >= len(d.Options) {
		return nil
	}
	if d.Cursor < 0 {
		if n := d.Options[pick].Number; n > 0 {
			return []string{strconv.Itoa(n)}
		}
		return nil
	}
	step, n := "Down", pick-d.Cursor
	if n < 0 {
		step, n = "Up", -n
	}
	keys := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		keys = append(keys, step)
	}
	return append(keys, "Enter")
}
