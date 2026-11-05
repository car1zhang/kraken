package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// activity.go — what an agent is DOING, read out of its transcript for the
// board's agent map. Every tool call is classified into one of a fixed set of
// activities (ActivityIDs), each with a short label naming the tool or
// program, and every arriving message (a prompt, a teammate's message, a task
// notification, a kraken notice, another cell's message) is a `hear` event.
// The map animates a card's characters from these and the steer modal lists
// them, so the one property every label holds is that it never carries text
// from the call's arguments or from a message: a label names a program and at
// most its subcommand, a file by its base name, or the party a message came
// from. The same transcript records ScanTasks reads (tasks.go) are read here,
// with the same degradation: a missing file or a malformed line is nothing,
// never an error.

// ActivityIDs is the activity vocabulary, in a fixed order the web client
// mirrors (lib/agentmap/activity.ts). `think` is never a classification: it
// is what an agent with no call in flight is doing.
var ActivityIDs = []string{
	"think", "edit", "search", "read", "run", "home", "delegate", "talk", "review",
	"test", "web", "wait", "check", "ship", "data", "build", "other",
}

// bashStrength ranks the activities a Bash command line can resolve to:
// within one compound line the strongest simple command names the call, so
// `cd x && go test ./...` is a test and `git add a && git commit` a ship.
var bashStrength = func() map[string]int {
	order := []string{"ship", "test", "build", "check", "delegate", "talk", "data", "web", "run", "wait", "home", "review", "edit", "search", "read"}
	m := make(map[string]int, len(order))
	for i, a := range order {
		m[a] = i
	}
	return m
}()

// toolInput is the union of the input fields the classifier and TailEvents
// read. Unknown fields are ignored and a malformed input decodes to zero
// values, which every rule below treats as "absent".
type toolInput struct {
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	To          string `json:"to"`
	Recipient   string `json:"recipient"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TaskID      string `json:"task_id"`
	AgentName   string `json:"agent_name"`
}

func decodeToolInput(input json.RawMessage) toolInput {
	var in toolInput
	if len(bytes.TrimSpace(input)) > 0 {
		_ = json.Unmarshal(input, &in)
	}
	return in
}

// ClassifyToolCall names the activity of one tool call and a label for it.
// act is always an ActivityIDs member other than "think"; malformed or empty
// input classifies by the tool name alone.
func ClassifyToolCall(name string, input json.RawMessage) (act, label string) {
	in := decodeToolInput(input)
	switch name {
	case "Bash":
		return classifyBash(in.Command)
	case "Edit", "Write", "NotebookEdit":
		return "edit", withBase(name, in.FilePath)
	case "Read":
		return "read", withBase("Read", in.FilePath)
	case "Grep", "Glob", "ToolSearch":
		return "search", name
	case "Agent", "Task", "TaskStop", "ListAgents", "Workflow":
		return "delegate", name
	case "SendMessage":
		return "talk", "SendMessage"
	case "Monitor", "ScheduleWakeup", "ReadNotifications":
		return "wait", name
	case "WebFetch", "WebSearch":
		return "web", name
	}
	switch {
	case strings.Contains(name, "playwright"), strings.Contains(name, "claude-in-chrome"):
		return "web", "browser"
	case strings.Contains(name, "context7"):
		return "web", stripMCPPrefix(name)
	case strings.Contains(name, "supabase"):
		return "data", "supabase"
	}
	return "other", stripMCPPrefix(name)
}

// withBase is "<tool> <base name>", or the tool alone when there is no path.
func withBase(tool, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return tool
	}
	b := filepath.Base(p)
	if b == "." || b == "/" {
		return tool
	}
	return tool + " " + b
}

// stripMCPPrefix drops an `mcp__<server>__` prefix, leaving the tool's own
// name.
func stripMCPPrefix(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		if i := strings.Index(name[len("mcp__"):], "__"); i >= 0 {
			return name[len("mcp__")+i+2:]
		}
	}
	return name
}

// classifyBash resolves a command line to the strongest activity among its
// simple commands, the first of equal strength winning; a line none of whose
// commands match a rule is `other`/`shell`.
func classifyBash(cmd string) (string, string) {
	bestAct, bestLabel := "", ""
	for _, toks := range simpleCommands(cmd) {
		act, label, ok := classifySimple(toks)
		if !ok {
			continue
		}
		if bestAct == "" || bashStrength[act] < bashStrength[bestAct] {
			bestAct, bestLabel = act, label
		}
	}
	if bestAct == "" {
		return "other", "shell"
	}
	return bestAct, bestLabel
}

var (
	envAssignRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	labelWordRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)
	pythonVerRe  = regexp.MustCompile(`^python3?\.\d+$`)
	bashPrefixes = map[string]bool{"sudo": true, "time": true, "command": true, "exec": true, "nohup": true, "!": true}
)

// quotedMark leads every token any part of which was inside quotes (shell
// concatenation makes `x"secret"` one word). No rule matches a marked token
// (an equality, a prefix, labelWord), so a word that sits inside a quoted
// string is never read as a program, a subcommand, a flag or a label word —
// `echo "x; make tokens"` is one echo, not an echo and a make.
const quotedMark = "\x00"

// heredoc is a pending here-document: its delimiter, and whether the `<<-`
// form lets the closing line be tab-indented.
type heredoc struct {
	delim string
	dash  bool
}

// simpleCommands tokenizes a command line once with POSIX quoting — single
// quotes literal, ANSI-C `$'…'` honouring its backslash escapes, double
// quotes honouring a backslash before `"` `\` `$` or a backquote and holding
// any `$(…)` inside them as one opaque span (skipSubstitution: bash parses it
// as its own context, heredoc bodies included), a backslash outside quotes
// escaping the next character, an unquoted `#` at a word's start opening a
// comment — and splits it into simple commands on the UNQUOTED operators `;`
// `&&` `||` `|` and newlines, so a separator inside a quoted string never
// starts a command. An unclosed quote runs to the end of the command text,
// newlines and separators included, as one quoted token. An unquoted `<<WORD`
// or `<<-WORD` (never `<<<`, and never `<<` before a digit, which is an
// arithmetic shift) opens a here-document: at the next unquoted newline its body
// is skipped through the closing line (bash's exact match, after leading
// tabs for `<<-`), and a body that never closes is skipped to the end, so no
// payload line is ever read as a command. Leading assignments and prefix
// words are dropped, and empty commands with them.
func simpleCommands(cmd string) [][]string {
	var out [][]string
	var words []string
	var pending []heredoc
	var tok strings.Builder
	inTok, quoted := false, false
	flushTok := func() {
		if inTok {
			t := tok.String()
			if quoted {
				t = quotedMark + t
			}
			words = append(words, t)
		}
		tok.Reset()
		inTok, quoted = false, false
	}
	flushCmd := func() {
		flushTok()
		toks := words
		// an assignment whose value was quoted (`FOO="a b" cmd`) is still an
		// assignment; it is dropped, never read
		for len(toks) > 0 && (envAssignRe.MatchString(strings.TrimPrefix(toks[0], quotedMark)) || bashPrefixes[toks[0]]) {
			toks = toks[1:]
		}
		if len(toks) > 0 {
			out = append(out, toks)
		}
		words = nil
	}
	quote := func() { inTok, quoted = true, true }
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch c {
		case '\'':
			quote()
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				tok.WriteString(cmd[i+1:])
				i = len(cmd)
				break
			}
			tok.WriteString(cmd[i+1 : i+1+j])
			i += j + 1
		case '"':
			quote()
			i++
			for ; i < len(cmd) && cmd[i] != '"'; i++ {
				if cmd[i] == '\\' && i+1 < len(cmd) && strings.IndexByte("\"\\$`", cmd[i+1]) >= 0 {
					i++
					tok.WriteByte(cmd[i])
					continue
				}
				if cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '(' {
					// a command substitution is its own quoting context up
					// to its matching `)`: its quotes and heredoc bodies
					// never close this string
					end := skipSubstitution(cmd, i)
					tok.WriteString(cmd[i:min(end+1, len(cmd))])
					i = end
					continue
				}
				tok.WriteByte(cmd[i])
			}
		case '$':
			if i+1 < len(cmd) && cmd[i+1] == '\'' {
				// ANSI-C quoting: `\'` (and every other backslash pair) is
				// part of the string, so only an unescaped `'` closes it
				quote()
				i += 2
				for ; i < len(cmd) && cmd[i] != '\''; i++ {
					if cmd[i] == '\\' && i+1 < len(cmd) {
						i++
					}
					tok.WriteByte(cmd[i])
				}
				break
			}
			inTok = true
			tok.WriteByte(c)
		case '\\':
			if i+1 >= len(cmd) {
				continue
			}
			i++
			if cmd[i] == '\n' {
				continue // a line continuation joins the two lines
			}
			inTok = true
			tok.WriteByte(cmd[i])
		case '<':
			if i+2 < len(cmd) && cmd[i+1] == '<' && cmd[i+2] == '<' {
				// a here-string, not a heredoc: all three characters go
				// together, so the next `<` is never read as `<<`
				inTok = true
				tok.WriteString("<<<")
				i += 2
				break
			}
			if i+2 < len(cmd) && cmd[i+1] == '<' {
				if h, end, ok := readHeredocOp(cmd, i); ok {
					flushTok()
					pending = append(pending, h)
					i = end - 1
					break
				}
			}
			inTok = true
			tok.WriteByte(c)
		case ' ', '\t', '\r':
			flushTok()
		case '\n':
			flushCmd()
			if len(pending) > 0 {
				i = skipHeredocBodies(cmd, i+1, pending) - 1
				pending = nil
			}
		case ';':
			flushCmd()
		case '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				i++
			}
			flushCmd()
		case '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				i++
				flushCmd()
				break
			}
			inTok = true
			tok.WriteByte(c)
		case '#':
			if inTok {
				tok.WriteByte(c)
				break
			}
			for i+1 < len(cmd) && cmd[i+1] != '\n' {
				i++
			}
		default:
			inTok = true
			tok.WriteByte(c)
		}
	}
	flushCmd()
	return out
}

// readHeredocOp reads a here-document operator at cmd[i:] (`<<` already
// seen, not `<<<`): an optional `-`, blanks, then the delimiter word with
// any quotes and backslashes removed, as bash does. end is the index just
// past the word; ok is false when there is no word.
func readHeredocOp(cmd string, i int) (h heredoc, end int, ok bool) {
	j := i + 2
	if j < len(cmd) && cmd[j] == '-' {
		h.dash = true
		j++
	}
	for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	if j < len(cmd) && cmd[j] >= '0' && cmd[j] <= '9' {
		// `1<<3` is an arithmetic shift (`$((1<<3))`), not a heredoc
		return heredoc{}, 0, false
	}
	var w strings.Builder
	for j < len(cmd) {
		c := cmd[j]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || strings.IndexByte(";|&<>()", c) >= 0 {
			break
		}
		switch c {
		case '\'', '"':
			k := strings.IndexByte(cmd[j+1:], c)
			if k < 0 {
				w.WriteString(cmd[j+1:])
				j = len(cmd)
				continue
			}
			w.WriteString(cmd[j+1 : j+1+k])
			j += k + 2
			continue
		case '\\':
			if j+1 < len(cmd) {
				j++
				c = cmd[j]
			}
		}
		w.WriteByte(c)
		j++
	}
	if w.Len() == 0 {
		return heredoc{}, 0, false
	}
	h.delim = w.String()
	return h, j, true
}

// skipSubstitution returns the index of the `)` closing the command
// substitution whose `$(` is at cmd[i], or len(cmd) when it never closes
// (an arithmetic `$((…))` closes the same way, on its outer `)`). It
// scans the substitution the way bash parses it, as its own quoting context:
// nested parentheses, single quotes, double quotes (with their own nested
// substitutions), backquotes, backslash escapes, and heredocs, whose bodies
// are skipped through their closing line so a quote or `)` inside a body ends
// nothing. A heredoc operator with a digit after it is an arithmetic shift,
// and `<<<` is a here-string.
func skipSubstitution(cmd string, i int) int {
	depth := 0
	var pending []heredoc
	for j := i + 1; j < len(cmd); j++ {
		switch c := cmd[j]; c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		case '\\':
			j++
		case '\'', '`':
			k := strings.IndexByte(cmd[j+1:], c)
			if k < 0 {
				return len(cmd)
			}
			j += k + 1
		case '"':
			k := j + 1
			for ; k < len(cmd) && cmd[k] != '"'; k++ {
				switch {
				case cmd[k] == '\\':
					k++
				case cmd[k] == '$' && k+1 < len(cmd) && cmd[k+1] == '(':
					k = skipSubstitution(cmd, k)
				}
			}
			if k >= len(cmd) {
				return len(cmd)
			}
			j = k
		case '<':
			if j+2 < len(cmd) && cmd[j+1] == '<' && cmd[j+2] == '<' {
				j += 2
				continue
			}
			if j+2 < len(cmd) && cmd[j+1] == '<' {
				if h, end, ok := readHeredocOp(cmd, j); ok {
					pending = append(pending, h)
					j = end - 1
				}
			}
		case '\n':
			if len(pending) > 0 {
				j = skipHeredocBodies(cmd, j+1, pending) - 1
				pending = nil
			}
		}
	}
	return len(cmd)
}

// skipHeredocBodies skips each pending here-document's body in order,
// starting at cmd[pos:] (the line after the operator's), and returns the
// index just past the last closing line — or len(cmd) when a body never
// closes, since bash would run none of the lines after it.
func skipHeredocBodies(cmd string, pos int, pending []heredoc) int {
	for _, h := range pending {
		closed := false
		for pos < len(cmd) {
			line := cmd[pos:]
			next := len(cmd)
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
				next = pos + nl + 1
			}
			pos = next
			line = strings.TrimRight(line, "\r")
			if h.dash {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				closed = true
				break
			}
		}
		if !closed {
			return len(cmd)
		}
	}
	return pos
}

// nonFlags is every token after the program that does not start with `-`.
func nonFlags(toks []string) []string {
	var r []string
	for _, t := range toks[1:] {
		if !strings.HasPrefix(t, "-") {
			r = append(r, t)
		}
	}
	return r
}

// labelWord reports whether a token may appear in a label as a subcommand: a
// plain word, never a path, a number, a quoted message or a flag value.
func labelWord(s string) bool { return labelWordRe.MatchString(s) }

func hasAny(toks []string, words ...string) bool {
	for _, t := range toks {
		for _, w := range words {
			if t == w {
				return true
			}
		}
	}
	return false
}

// window is toks[from:to] clamped to the slice.
func window(toks []string, from, to int) []string {
	if from >= len(toks) {
		return nil
	}
	if to > len(toks) {
		to = len(toks)
	}
	return toks[from:to]
}

// gitSubcommand is git's subcommand, skipping global options and the values
// of the four that take one.
func gitSubcommand(toks []string) string {
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		switch t {
		case "-C", "-c", "--git-dir", "--work-tree":
			i++
			continue
		}
		if strings.HasPrefix(t, "-") {
			continue
		}
		return t
	}
	return ""
}

// ghGroups are gh's command groups that take a subcommand, so their label is
// two words deep (`gh pr create`); every other gh command is one (`gh api`).
var ghGroups = map[string]bool{
	"pr": true, "issue": true, "run": true, "repo": true, "auth": true, "release": true,
	"workflow": true, "gist": true, "label": true, "secret": true, "search": true,
	"cache": true, "codespace": true, "project": true, "ruleset": true, "variable": true,
	"extension": true, "org": true, "config": true, "ssh-key": true, "gpg-key": true,
}

// krakenGroups are kraken's verbs that take a subcommand (cmd/kraken/main.go's
// dispatch), labelled two words deep (`kraken mm get`).
var krakenGroups = map[string]bool{
	"questions": true, "goals": true, "feedback": true, "watchlist": true, "load": true,
	"stack": true, "artifacts": true, "mm": true, "slack": true, "board": true,
	"cell": true, "job": true, "nodes": true,
}

// groupLabel is "<prog> <w0>[ <w1>]": w1 only when w0 is a group, and each
// word only when it is a plain word.
func groupLabel(prog string, words []string, groups map[string]bool) string {
	if len(words) == 0 || !labelWord(words[0]) {
		return prog
	}
	l := prog + " " + words[0]
	if groups[words[0]] && len(words) > 1 && labelWord(words[1]) {
		l += " " + words[1]
	}
	return l
}

// ghWords is gh's leading non-flag words, skipping the value of -R/--repo.
func ghWords(toks []string) []string {
	var r []string
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if t == "-R" || t == "--repo" || t == "--hostname" {
			i++
			continue
		}
		if strings.HasPrefix(t, "-") {
			continue
		}
		r = append(r, t)
	}
	return r
}

// makeTarget is make's first target, skipping flags (and the values of the
// ones that take a separate value) and VAR=value assignments.
func makeTarget(toks []string) string {
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		switch t {
		case "-C", "-f", "-o", "-I", "-W", "--directory", "--file":
			i++
			continue
		case "-j":
			if i+1 < len(toks) && toks[i+1] != "" && toks[i+1][0] >= '0' && toks[i+1][0] <= '9' {
				i++
			}
			continue
		}
		if strings.HasPrefix(t, "-") || strings.Contains(t, "=") || strings.HasPrefix(t, quotedMark) {
			continue
		}
		return t
	}
	return ""
}

var (
	checkProgs = map[string]bool{"ruff": true, "pyright": true, "mypy": true, "tsc": true, "eslint": true, "prettier": true, "gofmt": true, "black": true, "isort": true, "shellcheck": true, "swiftlint": true}
	buildProgs = map[string]bool{"xcodebuild": true, "xcodegen": true, "docker": true, "vite": true, "pip": true, "pip3": true, "cargo": true, "codesign": true, "brew": true}
	dataProgs  = map[string]bool{"sqlite3": true, "psql": true, "supabase": true, "bq": true}
	webProgs   = map[string]bool{"curl": true, "wget": true, "gcloud": true, "modal": true, "aws": true, "ngrok": true}
	runProgs   = map[string]bool{"node": true, "bash": true, "sh": true, "zsh": true, "osascript": true, "jq": true, "perl": true, "ruby": true, "swift": true, "tmux": true, "launchctl": true, "open": true}
	waitProgs  = map[string]bool{"until": true, "while": true, "sleep": true, "wait": true}
	searchProg = map[string]bool{"grep": true, "rg": true, "find": true, "fd": true, "ag": true, "which": true, "locate": true}
	readProgs  = map[string]bool{"cat": true, "head": true, "tail": true, "ls": true, "wc": true, "awk": true, "cut": true, "sort": true, "uniq": true, "tr": true, "diff": true, "cmp": true, "stat": true, "file": true, "du": true, "tree": true, "less": true, "date": true, "ps": true, "lsof": true, "strings": true, "xxd": true, "nl": true, "column": true, "basename": true, "dirname": true, "realpath": true, "env": true, "printenv": true, "shasum": true, "md5": true, "pbpaste": true}
	editProgs  = map[string]bool{"mkdir": true, "rm": true, "cp": true, "mv": true, "touch": true, "chmod": true, "ln": true, "tee": true, "rmdir": true, "kill": true, "pkill": true}
)

// classifySimple classifies one simple command; ok is false for glue (cd,
// echo, a shell keyword, an unknown program), which never names a call.
func classifySimple(toks []string) (act, label string, ok bool) {
	if len(toks) == 0 {
		return "", "", false
	}
	p := toks[0]
	if strings.HasPrefix(p, quotedMark) {
		return "", "", false // a quoted program is never read as one
	}
	if strings.Contains(p, "/") {
		p = filepath.Base(p)
	}
	switch {
	case (p == "poetry" || p == "uv") && len(toks) > 2 && toks[1] == "run":
		return classifySimple(toks[2:])
	case p == "npx" && len(toks) > 1:
		for i := 1; i < len(toks); i++ {
			if !strings.HasPrefix(toks[i], "-") {
				return classifySimple(toks[i:])
			}
		}
	case p == "python" || p == "python3" || pythonVerRe.MatchString(p):
		if len(toks) > 2 && toks[1] == "-m" {
			return classifySimple(toks[2:])
		}
		return "run", "python", true
	case p == "git":
		s := gitSubcommand(toks)
		label := "git"
		if labelWord(s) {
			label = "git " + s
		}
		switch s {
		case "commit", "push", "add", "merge", "tag", "rm", "mv":
			return "ship", label, true
		case "grep":
			return "search", label, true
		}
		return "review", label, true
	case p == "gh":
		w := ghWords(toks)
		s := strings.Join(window(w, 0, 2), " ")
		label := groupLabel("gh", w, ghGroups)
		switch {
		case strings.HasPrefix(s, "pr create"), strings.HasPrefix(s, "pr merge"), strings.HasPrefix(s, "pr edit"), strings.HasPrefix(s, "pr comment"):
			return "ship", label, true
		case strings.HasPrefix(s, "pr checks"), strings.HasPrefix(s, "run "), strings.HasPrefix(s, "pr view"), strings.HasPrefix(s, "pr list"):
			return "wait", label, true
		}
		return "web", label, true
	case p == "kraken" || p == "k":
		w := nonFlags(toks)
		if len(w) > 0 && w[0] == "s" {
			return "talk", "kraken s", true
		}
		label := groupLabel("kraken", w, krakenGroups)
		s := strings.Join(window(w, 0, 2), " ")
		if len(w) > 0 && (w[0] == "c" || w[0] == "n" || w[0] == "b") || strings.HasPrefix(s, "cell submit") || strings.HasPrefix(s, "job draft") {
			return "delegate", label, true
		}
		if len(w) > 0 && w[0] == "mine" {
			return "read", "kraken mine", true
		}
		return "home", label, true
	}
	args := window(toks, 1, 3)
	switch {
	case p == "pytest" || p == "vitest" || p == "jest":
		return "test", p, true
	case p == "go" && hasAny(args, "test"):
		return "test", "go test", true
	case p == "npm" && hasAny(args, "test"), p == "xcodebuild" && hasAny(toks, "test"), p == "cargo" && hasAny(window(toks, 1, 2), "test"):
		return "test", p, true
	case checkProgs[p]:
		return "check", p, true
	case p == "go" && hasAny(args, "vet"):
		return "check", "go vet", true
	case p == "make":
		t := makeTarget(toks)
		label := "make"
		if labelWord(t) {
			label = "make " + t
		}
		switch {
		case strings.Contains(t, "lint"), strings.Contains(t, "fmt"):
			return "check", label, true
		case strings.Contains(t, "test"):
			return "test", label, true
		}
		return "build", label, true
	case p == "go" && hasAny(args, "build", "install", "run"), p == "npm" && hasAny(args, "run", "ci", "install"), buildProgs[p]:
		label := p
		if len(toks) > 1 && labelWord(toks[1]) {
			label = p + " " + toks[1]
		}
		return "build", label, true
	case dataProgs[p]:
		return "data", p, true
	case webProgs[p]:
		return "web", p, true
	case runProgs[p] || strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".py") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "$"):
		if p == "jq" {
			return "read", "jq", true
		}
		if p == "perl" {
			for _, t := range window(toks, 1, 3) {
				if strings.HasPrefix(t, "-") && strings.Contains(t, "i") {
					return "edit", "perl -i", true
				}
			}
		}
		return "run", p, true
	case waitProgs[p]:
		return "wait", p, true
	case p == "sed":
		for _, t := range window(toks, 1, 3) {
			if strings.HasPrefix(t, "-i") {
				return "edit", "sed -i", true
			}
		}
		return "read", "sed -n", true
	case searchProg[p]:
		return "search", p, true
	case readProgs[p]:
		return "read", p, true
	case editProgs[p]:
		return "edit", p, true
	}
	return "", "", false
}

// --- events out of a transcript ---

// AgentEvent is one thing an agent did or heard, as the agent map and the
// steer modal's events panel render it. Act is an ActivityIDs member, or
// "hear" for an arriving message, whose sender kind is From. Who names the
// other party (a recipient, a spawned teammate, a sending cell); it never
// carries message text.
type AgentEvent struct {
	ID    string `json:"id"`
	T     int64  `json:"t"` // unix milliseconds
	Act   string `json:"act"`
	Label string `json:"label"`
	DurMS int64  `json:"dur_ms,omitempty"`
	Open  bool   `json:"open,omitempty"`
	From  string `json:"from,omitempty"` // "teammate" | "task" | "you" | "kraken" | "cell", set only when Act is "hear"
	Who   string `json:"who,omitempty"`
}

// activityRecord is the slice of a transcript line TailEvents reads.
type activityRecord struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	UUID        string `json:"uuid"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// activityBlock is one content block of a record.
type activityBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
}

// TailEvents keeps one incremental parse per transcript path. A Claude
// transcript is append-only, so the state remembers how far it has parsed (a
// byte offset that only ever lands just past a newline, so a record caught
// mid-write is resumed rather than skipped) and the newest events with the
// calls among them still open; a later call parses only the bytes appended
// since. The first read of a path is one forward pass over the whole file,
// which is what makes the answer the one a fresh whole-file read gives. A file
// that shrank, was replaced (another inode), changed in place at the same
// size, or was rewritten in place to other content (the byte before the
// offset is no longer a newline, or the head's fingerprint differs) is parsed
// afresh. Each state keeps at most 2×keep events, and the map keeps at most
// eventsStatesMax paths, the least recently used dropped first.

const (
	// eventsKeep is how many newest events a state keeps by default:
	// above every caller's n (the board's 24, the crew's 16 and its 200 fallback, JobEvents' 200).
	eventsKeep = 256
	// eventsKeepMax bounds a state's keep when a caller asks for more; an n
	// above it is answered by a one-off parse that is not kept.
	eventsKeepMax = 4096
	// eventsStatesMax bounds how many paths hold a state.
	eventsStatesMax = 256
	// eventsReadBuf is the read buffer; a longer line is assembled from
	// several reads.
	eventsReadBuf = 1 << 20
	// eventsHeadPrint is how many of the file's first parsed bytes a state
	// keeps as its fingerprint.
	eventsHeadPrint = 256
)

// eventsState is one path's incremental parse.
type eventsState struct {
	mu      sync.Mutex
	info    os.FileInfo // the file as last read, for os.SameFile
	size    int64
	mtime   time.Time
	off     int64  // bytes parsed: always just past a newline, or 0
	head    []byte // the first min(eventsHeadPrint, off) parsed bytes
	keep    int
	events  []AgentEvent
	calls   map[string]int // tool_use id → its index in events
	trimmed bool           // events older than the kept ones were dropped
	used    time.Time
}

var (
	eventsStatesMu sync.Mutex
	eventsStates   = map[string]*eventsState{}
)

// TailEvents returns the newest n events of the Claude transcript at path,
// oldest first. n < 1 reads as 1; a path that does not exist is (nil, nil);
// a malformed line is skipped.
func TailEvents(path string, n int) ([]AgentEvent, error) {
	if n < 1 {
		n = 1
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			eventsStatesMu.Lock()
			delete(eventsStates, path)
			eventsStatesMu.Unlock()
			return nil, nil
		}
		return nil, err
	}
	if n > eventsKeepMax {
		st := newEventsState(n)
		if err := st.advance(path, info); err != nil {
			return nil, err
		}
		return st.newest(n), nil
	}
	st := eventsStateFor(path)
	st.mu.Lock()
	defer st.mu.Unlock()
	fresh := st.info == nil || !os.SameFile(st.info, info) || info.Size() < st.size ||
		(info.Size() == st.size && !info.ModTime().Equal(st.mtime)) || (n > st.keep && st.trimmed)
	if fresh {
		keep := eventsKeep
		if n > keep {
			keep = n
		}
		st.reset(keep)
	} else if n > st.keep {
		st.keep = n
	}
	st.used = time.Now()
	if fresh || info.Size() != st.size || !info.ModTime().Equal(st.mtime) {
		if err := st.advance(path, info); err != nil {
			// a failed read leaves nothing half-applied to trust next time
			st.reset(eventsKeep)
			return nil, err
		}
	}
	return st.newest(n), nil
}

func newEventsState(keep int) *eventsState {
	st := &eventsState{}
	st.reset(keep)
	return st
}

// reset empties the state (everything but its mutex) so the next advance
// parses the file from its first byte.
func (st *eventsState) reset(keep int) {
	st.info, st.size, st.mtime, st.off, st.head = nil, 0, time.Time{}, 0, nil
	st.keep, st.events, st.calls, st.trimmed = keep, nil, map[string]int{}, false
}

// eventsStateFor is path's state, created on first use; past eventsStatesMax
// the least recently used state is dropped.
func eventsStateFor(path string) *eventsState {
	eventsStatesMu.Lock()
	defer eventsStatesMu.Unlock()
	if st, ok := eventsStates[path]; ok {
		return st
	}
	if len(eventsStates) >= eventsStatesMax {
		var oldest string
		var at time.Time
		for p, s := range eventsStates {
			if s.mu.TryLock() {
				u := s.used
				s.mu.Unlock()
				if oldest == "" || u.Before(at) {
					oldest, at = p, u
				}
			}
		}
		delete(eventsStates, oldest)
	}
	st := newEventsState(eventsKeep)
	eventsStates[path] = st
	return st
}

// newest is a copy of the newest n kept events, nil when there are none.
func (st *eventsState) newest(n int) []AgentEvent {
	evs := st.events
	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}
	if len(evs) == 0 {
		return nil
	}
	out := make([]AgentEvent, len(evs))
	copy(out, evs)
	return out
}

// advance parses every complete line from st.off to the size info names and
// records the file's identity. A trailing line with no newline yet is left
// for the next call. Before resuming at st.off it checks the file still is
// the one parsed so far: the byte before the offset must be a newline and the
// head must match the fingerprint, or the state starts over from byte 0 (a
// same-inode rewrite to larger content passes every stat-based check).
func (st *eventsState) advance(path string, info os.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	size := info.Size()
	if st.off > 0 && !st.continues(f, size) {
		st.reset(st.keep)
	}
	defer st.fingerprint(f)
	if size > st.off {
		r := bufio.NewReaderSize(io.NewSectionReader(f, st.off, size-st.off), eventsReadBuf)
		var acc []byte
		for {
			chunk, err := r.ReadSlice('\n')
			if err == bufio.ErrBufferFull {
				acc = append(acc, chunk...)
				continue
			}
			if err != nil {
				if err == io.EOF {
					break // an unterminated tail: not a whole record yet
				}
				return err
			}
			line := chunk
			if len(acc) > 0 {
				acc = append(acc, chunk...)
				line = acc
			}
			st.off += int64(len(line))
			st.line(line)
			acc = acc[:0]
		}
	}
	st.info, st.size, st.mtime = info, size, info.ModTime()
	return nil
}

// continues reports whether f still holds what the state parsed: the byte
// before st.off is a newline and the head matches st.head.
func (st *eventsState) continues(f *os.File, size int64) bool {
	if st.off > size {
		return false
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], st.off-1); err != nil || b[0] != '\n' {
		return false
	}
	if len(st.head) == 0 {
		return true
	}
	got := make([]byte, len(st.head))
	if _, err := f.ReadAt(got, 0); err != nil {
		return false
	}
	return bytes.Equal(got, st.head)
}

// fingerprint records the first min(eventsHeadPrint, st.off) bytes, once
// they are all parsed; a full fingerprint is never re-read.
func (st *eventsState) fingerprint(f *os.File) {
	want := st.off
	if want > eventsHeadPrint {
		want = eventsHeadPrint
	}
	if int64(len(st.head)) >= want {
		return
	}
	head := make([]byte, want)
	if _, err := f.ReadAt(head, 0); err != nil {
		return
	}
	st.head = head
}

// The structural keys the line filter looks for. Inside a JSON string every
// quote is escaped, so an unescaped `"type":"tool_use"` can only be a key and
// its value, never text a record quotes. The filter assumes Claude Code's own
// encoding: compact JSON (no space after a colon) and a `role` on every
// assistant message — true of every one of the 138,480 assistant tool_use
// records and 155,289 user records in the 1,632 root transcripts on this
// machine when checked on 2026-11-05. A spaced or role-less record would be
// skipped.
var (
	toolUseKey    = []byte(`"type":"tool_use"`)
	assistantRole = []byte(`"role":"assistant"`)
	userType      = []byte(`"type":"user"`)
	toolResultKey = []byte(`"type":"tool_result"`)
	toolUseIDKey  = []byte(`"tool_use_id":"`)
	timestampKey  = []byte(`"timestamp":"`)
	sidechainKey  = []byte(`"isSidechain":true`)
)

// line applies one transcript line. Only a line that can carry an event is
// decoded: one holding an assistant message with a tool_use block, or a user
// record with no tool_result (whose top-level type the decode confirms). A
// user record carrying tool_results and nothing assistant-shaped is read
// without a decode — its tool_use_id keys and timestamp are found by byte
// search, and only ids of kept open calls matter — so a multi-megabyte tool
// output costs one scan, never a decode. Every other line (api blobs,
// attachments, settings records, text-only assistant turns) is skipped on
// the byte test alone.
func (st *eventsState) line(line []byte) {
	asst := bytes.Contains(line, toolUseKey) && bytes.Contains(line, assistantRole)
	user := bytes.Contains(line, userType)
	switch {
	case !asst && !user:
		return
	case !asst && bytes.Contains(line, toolResultKey):
		st.results(line)
		return
	}
	var rec activityRecord
	if json.Unmarshal(line, &rec) != nil || rec.IsSidechain {
		return
	}
	ts := parseRecordTime(rec.Timestamp)
	switch rec.Type {
	case "assistant":
		var blocks []activityBlock
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || b.ID == "" {
				continue
			}
			if _, seen := st.calls[b.ID]; seen {
				continue
			}
			st.calls[b.ID] = len(st.events)
			st.events = append(st.events, callEvent(b, ts))
		}
	case "user":
		text, results := userTextAndResults(rec.Message.Content)
		if len(results) > 0 {
			for _, id := range results {
				st.close(id, ts)
			}
			return
		}
		if rec.IsMeta {
			return
		}
		ev, ok := hearEvent(text)
		if !ok {
			return
		}
		ev.ID, ev.T = rec.UUID, ts
		st.events = append(st.events, ev)
	default:
		return
	}
	st.trim()
}

// close marks a kept open call answered at ts.
func (st *eventsState) close(id string, ts int64) {
	k, ok := st.calls[id]
	if !ok || !st.events[k].Open {
		return
	}
	ev := &st.events[k]
	ev.Open = false
	if d := ts - ev.T; d > 0 && ts > 0 && ev.T > 0 {
		ev.DurMS = d
	}
}

// results closes every kept open call a tool_result line answers, by byte
// search; the line's timestamp is read only once an id matches.
func (st *eventsState) results(line []byte) {
	if bytes.Contains(line, sidechainKey) {
		return
	}
	ts := int64(-1)
	for rest := line; ; {
		i := bytes.Index(rest, toolUseIDKey)
		if i < 0 {
			return
		}
		rest = rest[i+len(toolUseIDKey):]
		j := bytes.IndexByte(rest, '"')
		if j < 0 {
			return
		}
		id := string(rest[:j])
		rest = rest[j:]
		if k, ok := st.calls[id]; !ok || !st.events[k].Open {
			continue
		}
		if ts < 0 {
			ts = byteTimestamp(line)
		}
		st.close(id, ts)
	}
}

// byteTimestamp is the record's timestamp found by byte search: the first
// structural "timestamp" key, which in a user record is the top-level one (a
// tool_result block carries none). 0 when absent or unparsable.
func byteTimestamp(line []byte) int64 {
	i := bytes.Index(line, timestampKey)
	if i < 0 {
		return 0
	}
	rest := line[i+len(timestampKey):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return 0
	}
	return parseRecordTime(string(rest[:j]))
}

// trim drops the oldest events once more than 2×keep are held, keeping keep.
func (st *eventsState) trim() {
	if len(st.events) <= 2*st.keep {
		return
	}
	drop := len(st.events) - st.keep
	st.events = append([]AgentEvent(nil), st.events[drop:]...)
	st.calls = make(map[string]int, len(st.events))
	for i, ev := range st.events {
		if ev.Act != "hear" {
			st.calls[ev.ID] = i
		}
	}
	st.trimmed = true
}

// callEvent is one tool call's event, Open until its result is seen.
func callEvent(b activityBlock, ts int64) AgentEvent {
	act, label := ClassifyToolCall(b.Name, b.Input)
	ev := AgentEvent{ID: b.ID, T: ts, Act: act, Label: label, Open: true}
	in := decodeToolInput(b.Input)
	switch b.Name {
	case "SendMessage":
		who := strings.TrimSpace(in.To)
		if who == "" {
			who = strings.TrimSpace(in.Recipient)
		}
		ev.Label, ev.Who = "to "+who, who
	case "Agent":
		if name := strings.TrimSpace(in.Name); name != "" {
			ev.Label, ev.Who = "spawn "+name, name
		} else {
			d := strings.TrimSpace(in.Description)
			ev.Label, ev.Who = "subagent: "+d, d
		}
	case "TaskStop":
		target := strings.TrimSpace(in.TaskID)
		if target == "" {
			target = strings.TrimSpace(in.AgentName)
		}
		ev.Label, ev.Who = "stop "+target, target
	}
	return ev
}

// userTextAndResults reads a user record's content: its text (a string, or
// its text blocks joined) and the tool_use ids of any tool_result blocks.
func userTextAndResults(raw json.RawMessage) (string, []string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []activityBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}
	var texts, results []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			results = append(results, b.ToolUseID)
		case "text":
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n"), results
}

// hearSkipPrefixes are user texts that are harness plumbing rather than a
// message anyone sent: slash-command echoes, compaction summaries, caveats,
// reminders, shell echoes, image placeholders and interrupt markers.
var hearSkipPrefixes = []string{
	"<command-", "<local-command", "This session is being continued", "Caveat:",
	"<system-reminder", "<bash-", "[Image:", "[Request interrupted",
}

// hearEvent classifies an arriving message by its sender. Only the sender's
// name is ever kept; the message text never leaves this function.
func hearEvent(text string) (AgentEvent, bool) {
	t := strings.TrimLeft(text, " \t\r\n")
	if t == "" {
		return AgentEvent{}, false
	}
	for _, p := range hearSkipPrefixes {
		if strings.HasPrefix(t, p) {
			return AgentEvent{}, false
		}
	}
	ev := AgentEvent{Act: "hear"}
	if who, ok := teammateSender(t); ok {
		ev.From, ev.Who, ev.Label = "teammate", who, "from "+who
		return ev, true
	}
	if strings.HasPrefix(t, "<task-notification") {
		ev.From, ev.Label = "task", "task finished"
		return ev, true
	}
	if strings.Contains(firstRunes(t, 90), "[kraken ") {
		ev.From, ev.Label = "kraken", "notice from kraken"
		return ev, true
	}
	if slug, ok := fromCellSlug(t); ok {
		ev.From, ev.Who, ev.Label = "cell", slug, "from cell "+slug
		return ev, true
	}
	ev.From, ev.Label = "you", "prompt from you"
	return ev, true
}

var (
	teammateIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)
	cellSlugRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// relayedPrefix is the one line allowed in front of the teammate tag: the
// harness line a delivery relayed through the cross-session channel carries
// first (2,780 of 6,048 real deliveries measured on 2026-11-05 open with
// it). It is fixed harness text and holds nothing the operator wrote.
const relayedPrefix = "Another Claude session sent a message:"

// teammateSender is the sending teammate of a text that IS a teammate
// delivery: one that starts (t is already left-trimmed) with the tag, or
// with relayedPrefix, whitespace and then the tag, and whose id is closed by
// `"` and name-shaped. A prompt that merely quotes or discusses the tag is
// not one, so none of its prose can become a sender.
func teammateSender(t string) (string, bool) {
	const tm = `<teammate-message teammate_id="`
	if rest, ok := strings.CutPrefix(t, relayedPrefix); ok {
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if len(trimmed) == len(rest) {
			return "", false // the harness line is always followed by whitespace
		}
		t = trimmed
	}
	if !strings.HasPrefix(t, tm) {
		return "", false
	}
	rest := t[len(tm):]
	j := strings.IndexByte(rest, '"')
	if j < 0 || !teammateIDRe.MatchString(rest[:j]) {
		return "", false
	}
	return rest[:j], true
}

// fromCellSlug reads the sending cell out of a relayed message's
// `[from <label> · cell <slug>]` stamp (jobs/node.go's format). The slug must
// be one word of slug characters, so a bracket of prose is never read as one.
func fromCellSlug(t string) (string, bool) {
	if !strings.HasPrefix(t, "[from ") {
		return "", false
	}
	end := strings.IndexByte(t, ']')
	if end < 0 {
		return "", false
	}
	seg := t[:end]
	i := strings.LastIndex(seg, "cell ")
	if i < 0 || (i > 0 && seg[i-1] != ' ') {
		return "", false
	}
	slug := strings.TrimSpace(seg[i+len("cell "):])
	if !cellSlugRe.MatchString(slug) {
		return "", false
	}
	return slug, true
}

func firstRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// parseRecordTime is a record's RFC 3339 timestamp in unix millis, 0 when it
// does not parse.
func parseRecordTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// --- a teammate's own transcript ---

// agentHeadBytes is how much of a transcript's head NewestAgentTranscript
// reads for its agentName: the record carrying it sits near the head but can
// follow a long brief (isAgentTranscript's probe, tokens.go, reads the same
// span for the same marker).
const agentHeadBytes = 256 << 10

type agentNameCacheEntry struct {
	info  os.FileInfo
	size  int64
	mtime time.Time
	names map[string]bool
}

var (
	agentNameCacheMu sync.Mutex
	agentNameCache   = map[string]agentNameCacheEntry{}
)

const agentNameCacheMax = 4096

// NewestAgentTranscript returns the most recently modified `*.jsonl` directly
// inside one of dirs whose head holds a record with top-level agentName equal
// to agentName, or "" when none does. A dir under `subagents/` is never read.
func NewestAgentTranscript(dirs []string, agentName string) string {
	if agentName == "" {
		return ""
	}
	type cand struct {
		path  string
		info  os.FileInfo
		mtime time.Time
	}
	var cands []cand
	for _, d := range dirs {
		if d == "" || underSubagents(d) {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			cands = append(cands, cand{filepath.Join(d, e.Name()), info, info.ModTime()})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })
	for _, c := range cands {
		if headAgentNames(c.path, c.info)[agentName] {
			return c.path
		}
	}
	return ""
}

func underSubagents(dir string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(dir)), "/") {
		if part == "subagents" {
			return true
		}
	}
	return false
}

// headAgentNames is every top-level agentName in the file's first
// agentHeadBytes. Cached on (size, mtime), and past that for good once the
// file was already at least agentHeadBytes long when it was read: a
// transcript is append-only, so the same file (os.SameFile) grown further
// has the very head that was read, and a live teammate's growing transcript
// is not re-read on every board build.
func headAgentNames(path string, info os.FileInfo) map[string]bool {
	size, mtime := info.Size(), info.ModTime()
	agentNameCacheMu.Lock()
	e, ok := agentNameCache[path]
	agentNameCacheMu.Unlock()
	if ok && e.size == size && e.mtime.Equal(mtime) {
		return e.names
	}
	if ok && e.size >= agentHeadBytes && size >= e.size && e.info != nil && os.SameFile(e.info, info) {
		return e.names
	}
	names := map[string]bool{}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(io.NewSectionReader(f, 0, agentHeadBytes))
		sc.Buffer(make([]byte, 0, 4096), agentHeadBytes+1)
		for sc.Scan() {
			line := sc.Bytes()
			if !bytes.Contains(line, []byte(`"agentName"`)) {
				continue
			}
			var rec struct {
				AgentName string `json:"agentName"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.AgentName != "" {
				names[rec.AgentName] = true
			}
		}
		f.Close()
	}
	agentNameCacheMu.Lock()
	if len(agentNameCache) >= agentNameCacheMax {
		agentNameCache = map[string]agentNameCacheEntry{}
	}
	agentNameCache[path] = agentNameCacheEntry{info: info, size: size, mtime: mtime, names: names}
	agentNameCacheMu.Unlock()
	return names
}
