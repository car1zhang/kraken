package jobs

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// bashlint.go — `kraken bash-lint`, wired as a Claude Code PreToolUse hook
// in each cell's worktree (cell.go's mergeHookSettings installs it beside
// the pr-hook PostToolUse entry). It screens every Bash tool call for a
// short list of shapes measured to fail SILENTLY — zsh-on-macOS quirks
// where the command aborts or lies while `is_error` stays false, plus one
// destructive git shape whose damage lands on OTHER agents — and blocks
// the call with the exact fix named, so the agent repairs it in one turn
// instead of shipping a conclusion built on output that never ran.
//
// The list is deliberately tiny and deterministic. Every entry is a shape
// that (a) recurred across many cells and nights, (b) was already named in
// skill prose that agents measurably keep not applying — or, for
// `$PIPESTATUS`, was never named in prose at all, since its failure prints
// nothing an agent could notice — and (c) has no legitimate use an agent
// would miss:
//
//   - a bare `=word` token (`echo ===`, `===SECTION===`): zsh
//     equals-expansion kills the whole compound after printing its first
//     half, leaving a ~20-char `== not found` tail on kilobytes of good
//     output that reads as success.
//   - `timeout <secs> …`: timeout(1) does not exist on macOS. Bare, it
//     exits 127; piped (`timeout … | wc -l`), the pipeline exits through
//     its last stage and returns a silent false zero.
//   - an unquoted `--include=*.py`-style glob: zsh NOMATCH aborts before
//     the command runs, and the abort reads as "no files match".
//   - assigning `path=`/`fpath=`/`status=`: zsh ties path/fpath to the
//     PATH/fpath arrays, so a scratch variable named `path` wipes command
//     lookup for the rest of the shell. `status=` rides the same arm for a
//     sibling reason:
//     `status` is a read-only special in zsh (an alias of `$?`), so the
//     natural idiom `out=$(cmd); status=$?` aborts the whole compound with
//     `read-only variable: status` before anything after it runs.
//   - expanding `$PIPESTATUS`: that is bash's name for the pipeline
//     exit-status array. zsh's is `$pipestatus`, lowercase and one-indexed,
//     and the bash spelling is simply unset there — so an exit-code probe
//     written with it prints an empty value, exits 0, and reads as a probe
//     that ran. It is the one entry that is an EXPANSION rather than a word,
//     which is why it is checked against its own view of the command: see
//     bashLintExpansionChecks.
//   - a mutating `git stash` (bare, or push/pop/apply/drop/…): the one
//     entry that is not a zsh quirk but a destructive shape — in a cell the
//     worktree is shared with live teammates, a stash sweeps THEIR
//     uncommitted work along with the caller's, and it is a twice-recorded
//     destructive incident with a "the ban is absolute" rule in pr/SKILL.md
//     (do/SKILL.md carries the shared-worktree half) that implementers
//     measurably keep reinventing around — the rules live in files only the
//     lead reads. Read-only `git stash list` and `git stash show` pass;
//     `create` is blocked for uniformity although it changes nothing in the
//     worktree or the ref namespace, and `--help` rides the flag arm — both
//     get a message naming a better route, acceptable for forms this
//     obscure.
//
// False-positive control is structural, not hopeful: every region of a
// command that is not shell-in-command-position is blanked before any WORD
// check runs (the expansion checks read a second view that keeps the spans
// the shell expands in — stated on `lintBashCommand` and
// `bashLintExpansionChecks`). Heredoc bodies are data (a Python payload
// carrying `k = None` or `=== banner ===` must never trip a hook — the
// monolith's gcp guardrail
// firing on heredoc payloads is the measured counterexample this design
// avoids); quoted spans go because the whole point of the equals rule is
// that `echo "==="` is the fix; `[[ … ]]` and `(( … ))` spans go because
// `==` is an ordinary operator in both; and comments are cut because a
// block naming a fix for text that never executes is a refusal nobody can
// satisfy. `lintBashCommand` states the set and the ORDER, which is
// load-bearing — read it there rather than restating it, since a second
// copy of that list is how this comment would go stale.
//
// Like pr-hook, a hook that fails the tool call by ACCIDENT is a bug: any
// malformed input exits 0 (allow). Only a confident lint hit blocks.

// bashLintChecks are the shapes bash-lint blocks, applied to the command
// text only after `lintBashCommand`'s strip pipeline has run over it.
var bashLintChecks = []struct {
	re  *regexp.Regexp
	msg string
}{
	{
		// A token starting with `=` plus at least one more character, in
		// command-argument position. A LONE `=` is fine (test's operator:
		// `[ a = b ]`); `=x` and `===` are not. Arithmetic `==` never
		// reaches this: `(( … ))`/`$(( … ))` spans are stripped first.
		regexp.MustCompile(`(?:^|[\s;|&(])=[^\s;|&)]+`),
		"a bare '=…' word (like `echo ===`) is zsh equals-expanded and kills the whole compound after its first half — quote it: echo \"===\"",
	},
	{
		regexp.MustCompile(`(?:^|[\s;|&(])timeout\s+\d`),
		"timeout(1) does not exist on macOS — run the long command with run_in_background (or a Monitor until-loop) instead of a wrapper; piped, the missing command becomes a silent false zero",
	},
	{
		regexp.MustCompile(`--(?:include|exclude)=[^\s'"]*[*?\[]`),
		"an unquoted --include=*/--exclude=* glob makes zsh abort with 'no matches found' before the command runs — quote the pattern: --include='*.py'",
	},
	{
		// COMMAND-WORD position only: `gh api -f path=x` and `curl -d
		// path=/tmp` are arguments, not assignments, and clobber nothing —
		// so the token must open the command (start, a separator, or a
		// keyword that precedes an assignment), never follow a bare space.
		regexp.MustCompile(`(?:^\s*|[;&|({]\s*|\n\s*|(?:^|[\s;|&({])(?:do|then|else|elif|while|until|export|local|readonly|typeset|declare)\s+)(?:f?path|status)=`),
		"assigning 'path'/'fpath' clobbers zsh's tied PATH/fpath arrays, and 'status' is read-only in zsh (it aliases $?) — use another variable name (rc=$?)",
	},
	{
		// Mutating `git stash` forms only, matched by what FOLLOWS `stash`:
		// end-of-command (bare `git stash`, incl. before a redirect or a
		// closing backtick), an explicit mutating subcommand, or a flag
		// (`-u`, `--include-untracked` — the flag arm also catches the
		// read-only `--help`, accepted per the header comment). `git stash
		// list`/`show` fall through all three arms and stay allowed — as are
		// read-only commands taking `stash` as a positional ref (`git log
		// stash`): a subcommand word before `stash` makes the flag group
		// unmatchable — and so does prose in quotes/heredocs/comments
		// (stripped before this runs). RE2 has no lookahead, so the allowed
		// words are excluded by never being matchable rather than by
		// negation. The leading group covers `$(…)` and backtick
		// substitution plus a path-qualified `/usr/bin/git`; global flags
		// between `git` and `stash` cover the measured `git -C .. stash`
		// shape. Documented fail-open residue, consistent with the hook's
		// guardrail-not-sandbox stance: the space-separated `--git-dir /x`
		// form, a `~`-relative path (`~/bin/git stash` — `~` is outside the
		// prefix class), a backslash-continuation splitting `stash` from its
		// subcommand, `git stash $ARGS`, and `eval`/`bash -c` wrappers
		// (quote-blanking is lint-wide, not stash-specific).
		regexp.MustCompile("(?:^|[\\s;|&(`])(?:[\\w./-]*/)?git(?:\\s+-[A-Za-z]\\s+\\S+|\\s+--?[A-Za-z][-A-Za-z0-9]*(?:=\\S+)?)*\\s+stash(?:\\s*(?:$|[;|&)\\n`])|\\s+\\d?[<>]|\\s+(?:push|pop|apply|drop|clear|branch|store|create|save)\\b|\\s+-)"),
		"a mutating `git stash` in a cell sweeps every agent's uncommitted work out of the shared worktree (a twice-recorded destructive incident) — verify against an isolated copy instead: `git show HEAD:<path>` into scratch, or a throwaway `git worktree add`; read-only `git stash list`/`show` are allowed, and if you were only PRINTING the words, quote them",
	},
}

// bashLintExpansionChecks are the shapes that are parameter EXPANSIONS, and
// they are matched against a different view of the command than
// bashLintChecks: the shell expands `$name` inside double quotes and inside
// `[[ … ]]` and inside `(( … ))`, so blanking those spans — right for a check
// about WORDS, where quoting is the fix — would hide the most common form of
// the shape (`echo "rc=${PIPESTATUS[0]}"`). Single-quoted spans, heredoc
// bodies and comments are still blanked: nothing expands in any of them.
//
// A check here is a function rather than a pattern because whether a `$` is
// live turns on the PARITY of the backslashes in front of it, which RE2,
// having no lookbehind, cannot count.
var bashLintExpansionChecks = []struct {
	hit func(text string) bool
	msg string
}{
	{
		expandsBashPipestatus,
		"$PIPESTATUS is bash's array and is unset in zsh, so the probe prints an empty value and still exits 0 — zsh spells it $pipestatus, lowercase and one-indexed: ${pipestatus[1]} is the first stage's exit status",
	},
}

// expandsBashPipestatus reports whether text expands `$PIPESTATUS` or
// `${PIPESTATUS…`. Three things make an occurrence NOT an expansion of that
// name: a longer name that merely starts with it (`$PIPESTATUS_LOG`), a
// different case (zsh's own `$pipestatus`), and a `$` that is escaped — an
// ODD run of backslashes in front of it. An even run escapes itself and
// leaves the `$` live.
func expandsBashPipestatus(text string) bool {
	const name = "PIPESTATUS"
	for i := 0; i < len(text); i++ {
		if text[i] != '$' {
			continue
		}
		j := i + 1
		if j < len(text) && text[j] == '{' {
			j++
		}
		if !strings.HasPrefix(text[j:], name) {
			continue
		}
		if end := j + len(name); end < len(text) && isShellNameByte(text[end]) {
			continue
		}
		slashes := 0
		for k := i - 1; k >= 0 && text[k] == '\\'; k-- {
			slashes++
		}
		if slashes%2 == 0 {
			return true
		}
	}
	return false
}

// isShellNameByte reports whether c can continue a shell variable name.
func isShellNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// heredocIntroRE matches a heredoc redirection and captures the `-`
// variant marker and the delimiter: <<EOF, << 'EOF', <<-"PY", etc. The
// caller screens out `<<<` (herestring) at the MATCH position — the regex
// itself would happily match the tail of one, and a herestring's payload
// sits on the same line inside quotes, which the quote strip handles.
var heredocIntroRE = regexp.MustCompile(`<<(-?)\s*(["']?)([A-Za-z_][A-Za-z0-9_]*)(["']?)`)

// stripHeredocBodies removes the BODY lines of every heredoc in cmd,
// keeping the shell lines (including the intro line itself). Body text is
// data — a Python script fed via `python3 - <<'PY'` is exactly where `=`
// signs and the word `timeout` legitimately live.
func stripHeredocBodies(cmd string) string {
	return dropHeredocBodies(cmd, cmd)
}

// dropHeredocBodies removes from text the lines that guide identifies as
// heredoc bodies. The two are byte-aligned views of one command — guide is
// the view in which heredoc intros can be trusted (arithmetic blanked, so a
// `<< n` shift is not read as one), text is the view whose remaining lines
// the caller wants — and stripHeredocBodies is the case where they are one
// text.
func dropHeredocBodies(text, guide string) string {
	lines := strings.Split(text, "\n")
	guides := strings.Split(guide, "\n")
	var out []string
	for i := 0; i < len(guides) && i < len(lines); i++ {
		out = append(out, lines[i])
		tabbed, delim, ok := heredocMatch(guides[i])
		if !ok {
			continue
		}
		// Skip body lines until the delimiter line. Only the <<- form
		// strips leading tabs (POSIX); trimming them for plain << would end
		// the strip early and lint heredoc data as shell. An unterminated
		// heredoc swallows the rest — body text must never be linted, and
		// the command is broken anyway.
		for i++; i < len(guides) && i < len(lines); i++ {
			body := guides[i]
			if tabbed {
				body = strings.TrimLeft(body, "\t")
			}
			if body == delim {
				out = append(out, lines[i])
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// heredocMatch finds the first REAL heredoc intro on line: tabbed reports
// the `<<-` form (the only one whose terminator may be tab-indented), delim
// is the delimiter word, ok is false when the line opens no heredoc. A
// `<<<` herestring is skipped at the MATCH position rather than
// disqualifying the whole line — `grep x <<< "$y" && python3 - <<'PY'`
// carries both, and a line-wide exclusion would leave PY's body linted as
// shell.
func heredocMatch(line string) (tabbed bool, delim string, ok bool) {
	for start := 0; start < len(line); {
		loc := heredocIntroRE.FindStringSubmatchIndex(line[start:])
		if loc == nil {
			return false, "", false
		}
		at := start + loc[0]
		herestring := (at > 0 && line[at-1] == '<') ||
			(at+2 < len(line) && line[at+2] == '<')
		if !herestring {
			// group 1 is the `-` marker, group 3 the delimiter word; read
			// both out of the offsets this match already produced rather
			// than re-running the pattern
			dash := line[start+loc[2] : start+loc[3]]
			word := line[start+loc[6] : start+loc[7]]
			return dash == "-", word, true
		}
		start = at + 2
	}
	return false, "", false
}

// stripArith blanks `(( … ))` arithmetic spans (the `$(( … ))` form
// included — its inner text is the same `(( … ))`). Two reasons, both
// measured: `==` is an ordinary operator there, so an unstripped span makes
// the equals check hard-refuse valid zsh (`(( count == 0 ))`); and `<< n`
// is a left-shift there, which the heredoc scanner would otherwise read as
// an intro and swallow the rest of the command as body — which is why this
// runs BEFORE stripHeredocBodies, and so ahead of the quote pass rather
// than inside it.
//
// Running first means this scan cannot lean on the quote pass, so it does
// its own: quotedMask marks every byte inside a quoted span, and only an
// UNQUOTED `((`…`))` pair is blanked. That is not tidiness. A quote-blind
// scan pairs a `((` inside a string with a `))` anywhere after it, and one
// such pair blanked a heredoc INTRO and exposed its Python payload to the
// shell checks — the single failure direction this whole design exists to
// prevent. Bounding the closer to the opener's own line was tried first and
// is strictly worse: it left that same-line case untouched while newly
// refusing legal arithmetic that spans lines. An unclosed `((`, and one
// whose closer is quoted, are both left alone.
func stripArith(cmd string) string {
	b := []byte(cmd)
	quoted := quotedMask(b)
	for i := 0; i+1 < len(b); i++ {
		if quoted[i] || b[i] != '(' || b[i+1] != '(' {
			continue
		}
		found := false
		for j := i + 2; j+1 < len(b); j++ {
			if quoted[j] || b[j] != ')' || b[j+1] != ')' {
				continue
			}
			blank(b, i, j+1)
			i = j + 1
			found = true
			break
		}
		// No unquoted closer after this opener means none after any LATER
		// opener either — their search ranges are subsets of this one — so
		// stopping here is verdict-neutral and turns a quadratic walk over
		// unmatched openers into a linear one.
		if !found {
			break
		}
	}
	return string(b)
}

// quotedMask reports, per byte, whether it lies inside a single- or
// double-quoted span (delimiters included). It is the quote knowledge
// stripQuotedAndCond applies inline, lifted out for stripArith, which runs
// before it and would otherwise be quote-blind. An unterminated quote runs
// to end of input — the same fail-toward-more-blanking choice
// stripQuotedAndCond makes, and for the same reason: over-blanking costs a
// missed block, under-blanking costs a false refusal.
func quotedMask(b []byte) []bool {
	mask := make([]bool, len(b))
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '\\':
			i++ // an escaped byte is neither a delimiter nor inside one
		case b[i] == '\'' || b[i] == '"':
			q := b[i]
			j := i + 1
			for ; j < len(b); j++ {
				if q == '"' && b[j] == '\\' {
					j++
					continue
				}
				if j < len(b) && b[j] == q { // bound re-stated, not required: the
					break // backslash branch's j++ is re-tested by the loop
				}
			}
			for k := i; k <= j && k < len(b); k++ {
				mask[k] = true
			}
			i = j
		}
	}
	return mask
}

// stripQuotedAndCond blanks single-quoted spans, double-quoted spans, and
// [[ … ]] conditional spans, replacing their contents with spaces so
// token positions elsewhere survive. Backslash escapes a following quote
// outside single quotes.
func stripQuotedAndCond(s string) string {
	b := []byte(s)
	n := len(b)
	for i := 0; i < n; i++ {
		switch {
		case b[i] == '\\' && i+1 < n:
			i++ // escaped char: leave both, skip the next
		case b[i] == '\'' || b[i] == '"':
			// The end of the span, or the end of input when it is never
			// closed — the run-to-end case is deliberate, and it is stated
			// once here rather than as a `j == n-1` arm inside the loop:
			// written that way, a `"` span whose backslash skip stepped
			// straight past the last byte missed the arm and left its text
			// unblanked, which is a false REFUSAL on the one input where
			// this function and quotedMask disagreed.
			// TestQuotedMaskMirrorsStripQuotedAndCond pins that they cannot
			// diverge again.
			q := b[i]
			j := i + 1
			for ; j < n; j++ {
				if q == '"' && b[j] == '\\' {
					j++
					continue
				}
				if j < n && b[j] == q {
					break
				}
			}
			if j >= n {
				j = n - 1
			}
			blank(b, i, j)
			i = j
		case b[i] == '[' && i+1 < n && b[i+1] == '[':
			if end := bytes.Index(b[i:], []byte("]]")); end >= 0 {
				blank(b, i, i+end+1)
				i += end + 1
			}
		}
	}
	return string(b)
}

// blank overwrites b[from..to] inclusive with spaces, preserving newlines —
// not for `^` anchors (no check compiles with (?m)) but because a newline
// feeds the separator class `[\s;|&(]`, so a token opening a later line is
// still seen in command-word position.
func blank(b []byte, from, to int) {
	for k := from; k <= to && k < len(b); k++ {
		if b[k] != '\n' {
			b[k] = ' '
		}
	}
}

// lintBashCommand returns one message line per fatal-shape hit in cmd,
// joined, or "" when the command is clean. The preprocessing order is
// load-bearing: arithmetic spans first (so `<< n` shifts never read as
// heredocs), then heredoc bodies, then quotes/conditionals — see each
// stripper's comment. Comments are dropped LAST, which is what makes
// stripComments' `#` test exact: by then every `#` still standing is
// outside a quoted span and outside a heredoc body by construction.
//
// The expansion checks run over a second view of the same lines:
// single-quoted spans blanked, and double-quoted, `[[ … ]]` and `(( … ))`
// spans all left standing, since the shell expands inside each of them. Its
// heredoc bodies are dropped where the arithmetic-blanked view says they
// are, and its comments are cut where the FIRST view says they start,
// because that is the view in which a `#` is known to be outside every
// quoted span — a `#` inside a double-quoted string is text, and cutting
// there would hide an expansion that follows it.
func lintBashCommand(cmd string) string {
	arith := stripArith(cmd)
	words := stripQuotedAndCond(stripHeredocBodies(arith))
	text := stripComments(words)
	var msgs []string
	for _, c := range bashLintChecks {
		if c.re.MatchString(text) {
			msgs = append(msgs, "kraken bash-lint: "+c.msg)
		}
	}
	expanded := cutComments(stripSingleQuoted(dropHeredocBodies(cmd, arith)), words)
	for _, c := range bashLintExpansionChecks {
		if c.hit(expanded) {
			msgs = append(msgs, "kraken bash-lint: "+c.msg)
		}
	}
	return strings.Join(msgs, "\n")
}

// stripSingleQuoted blanks single-quoted spans only, leaving double-quoted
// spans standing — the view bashLintExpansionChecks needs. A double-quoted
// span is still WALKED, so an apostrophe inside one (`"it's"`) does not open
// a single-quoted span. An unterminated single quote runs to end of input,
// the same fail-toward-more-blanking choice stripQuotedAndCond makes.
func stripSingleQuoted(s string) string {
	b := []byte(s)
	n := len(b)
	for i := 0; i < n; i++ {
		switch {
		case b[i] == '\\' && i+1 < n:
			i++ // escaped char: leave both, skip the next
		case b[i] == '"':
			j := i + 1
			for ; j < n; j++ {
				if b[j] == '\\' {
					j++
					continue
				}
				if b[j] == '"' {
					break
				}
			}
			i = j
		case b[i] == '\'':
			j := i + 1
			for ; j < n && b[j] != '\''; j++ {
			}
			if j >= n {
				j = n - 1
			}
			blank(b, i, j)
			i = j
		}
	}
	return string(b)
}

// stripComments truncates each line at its comment, whole-line and TRAILING
// alike (`ls  # timeout 5 was tried`): comment text is never executed, so a
// hit there is a pure false positive, and the block message would name a
// fix — quote it — that cannot apply to prose. A `#` counts only at line
// start or after whitespace, which is the shell's own rule and leaves
// `echo a#b` and `curl host/x#frag` alone.
//
// It is the one stripper that REMOVES rather than blanks, so it is the one
// that must run last: every other pass preserves byte offsets (see blank)
// because a later pass depends on them, and nothing runs after this one.
func stripComments(text string) string {
	return cutComments(text, text)
}

// cutComments truncates each line of text at the offset where the SAME line
// of mask starts its comment. The two are byte-aligned views of one command
// — every stripper between them blanks in place — so an offset found in one
// names the same byte in the other. stripComments is the case where the two
// are one text.
func cutComments(text, mask string) string {
	lines := strings.Split(text, "\n")
	masks := strings.Split(mask, "\n")
	for i := range lines {
		if i >= len(masks) {
			break
		}
		m := masks[i]
		for j := 0; j < len(m); j++ {
			if m[j] != '#' {
				continue
			}
			if j == 0 || m[j-1] == ' ' || m[j-1] == '\t' {
				if j <= len(lines[i]) {
					lines[i] = lines[i][:j]
				}
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// bashLintPayload is the subset of Claude Code's PreToolUse payload
// bash-lint reads — parsed defensively for the same reason hookPayload is:
// the schema is Claude Code's, not kraken's.
type bashLintPayload struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// BashLintStdin reads one PreToolUse payload from r and returns the block
// message and whether to block. Anything that is not a well-formed Bash
// payload allows: the hook must never fail a tool call by accident.
func BashLintStdin(r io.Reader) (string, bool) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", false
	}
	var p bashLintPayload
	if json.Unmarshal(raw, &p) != nil {
		return "", false
	}
	if p.ToolName != "Bash" || strings.TrimSpace(p.ToolInput.Command) == "" {
		return "", false
	}
	if msg := lintBashCommand(p.ToolInput.Command); msg != "" {
		return msg, true
	}
	return "", false
}

// bashLintCommand is the exact hook command string cell.go installs and
// hasHookCommand matches for idempotency — the same single-definition rule
// prHookCommand follows, for the same drift reason.
func bashLintCommand(krakenBin string) string {
	return krakenBin + " bash-lint"
}

// bashLintHookEntry is the PreToolUse matcher group kraken installs into a
// cell's worktree settings.
func bashLintHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": bashLintCommand(krakenBin),
			},
		},
	}
}
