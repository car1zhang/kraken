package jobs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kraken/internal/util"
)

// bashlint_write.go — the one bash-lint check that is about WHERE a command
// writes rather than how zsh parses it: a Bash-route write into the cell's
// worktree. The /do skill bans editing a repo file through Bash (`cat > f`,
// `sed -i`, `perl -pi`, `tee`) because the route fails silently — a
// `0 matches` from a rewrite script, or a partial append, prints a green
// tail while the file is not what the agent believes — and because the Edit
// tool's read-first guard is the only mechanical check an edit gets. The
// ban sat in prose only, read by whoever's brief carried it; this check
// makes it the same kind of guardrail the zsh shapes have. It is about hand
// edits: generated output (`go run ./gen > x_gen.go`) goes through scratch
// and a `cp`, which the check does not police.
//
// It is PATH-AWARE, which is what keeps it inside the hook's charter of
// shapes with no legitimate use an agent would miss: every one of these
// commands is fine against a scratch file, so the check blocks only when
// the target RESOLVES inside the worktree root the hook was invoked under.
// Relative targets resolve against the payload's cwd, tracked textually
// through every `cd`/`pushd` in the compound (the measured `cd /tmp && cat >
// notes` shape must pass). Wherever the location cannot be known statically
// the check STANDS DOWN rather than guessing, so its errors are misses, not
// refusals: a quoted target is blanked by the strip pipeline and so unseen,
// a `$VAR`/glob target, a `cd -`, `cd $X`, `popd`, a bare `cd` (whose
// argument may have been a blanked quote), and a `cd` anywhere inside a
// subshell, `$( … )` or backtick group all make the location unknown, and a
// cwd with no `.git` above it disables the check entirely. A heredoc body
// is still data: `python3 - <<'PY'` rewriting a file through `write_text` is
// out of reach here, as it must be (the hook never lints a payload), and
// remains the one route the skills police by prose alone.

// writeShape is one Bash-route write idiom and how its target tokens are
// found inside a simple command's token list.
type writeShape struct {
	name   string
	files  func(tokens []string) []string // target paths, already filtered
	marker string                         // the fix-naming label in the message
}

// bashWriteShapes are the command-word shapes (redirects are handled
// separately, since they attach to any command). The token lists they see
// have quoted spans blanked, so a quoted sed script or perl -e program is
// already gone; what remains are flags and file operands.
var bashWriteShapes = []writeShape{
	{name: "sed", marker: "sed -i", files: func(t []string) []string {
		if !hasFlagLetter(t[1:], 'i', "ef") && !hasFlag(t[1:], "--in-place") {
			return nil
		}
		return operands(t[1:], map[string]bool{"-f": true, "--file": true}, isSedScript)
	}},
	{name: "perl", marker: "perl -pi", files: func(t []string) []string {
		if !hasFlagLetter(t[1:], 'i', "IMmxdDCeEV") {
			return nil
		}
		return operands(t[1:], nil, func(string) bool { return false })
	}},
	{name: "tee", marker: "tee", files: func(t []string) []string {
		return operands(t[1:], nil, func(string) bool { return false })
	}},
}

// redirectRE finds an output redirection and its unquoted target in a
// stripped simple command: `> f`, `>> f`, `2> f`, and `&> f` once
// lintBashWrites has normalised its `&>` to `>`. Targets
// beginning with `&` (`>&2`, `2>&1`) or `(` (process substitution) are
// excluded by the class; a quoted target has been blanked to spaces and so
// leaves nothing for the group to match; a `>` preceded by `\` (`[ a \> b ]`)
// or `-` (an unquoted `->` in a format string) is not a redirect. Two
// documented misses, both fail-open: the clobber form `>| f` (cmdSeparatorRE
// splits on its `|` first) and bash's `>&f` both-fds form (fdDupRE blanks
// the `>&`, leaving f an ordinary token).
var redirectRE = regexp.MustCompile(`(?:^|[^<>\\-])\d?>{1,2}[ \t]*([^\s;|&()<>]+)`)

// leadingWords are the tokens a simple command may open with that are not
// its command word: shell keywords and wrappers ahead of the real command
// word (`env` runs it as a child process, the rest in the caller's shell;
// tracking an `env cd` is harmless).
var leadingWords = map[string]bool{"env": true, "builtin": true, "command": true, "{": true, "!": true, "if": true, "then": true, "elif": true, "else": true, "do": true, "while": true, "until": true, "time": true}

// fdDupRE matches a file-descriptor duplication or close, blanked before
// the separator split (see lintBashWrites). It also swallows the csh/zsh
// `>& file` spelling of `&> file`, a documented miss (fail-open).
var fdDupRE = regexp.MustCompile(`\d*[<>]&[\d-]*`)

// cmdSeparatorRE splits a stripped command into simple commands. `(` and
// `)` count as separators so `$( … )` and subshell bodies are scanned as
// commands of their own; backticks likewise. Backslash-newline continuations
// are joined before the split (lintBashWrites), so a continued command stays
// one span and its `\` is not read as an operand.
var cmdSeparatorRE = regexp.MustCompile("[;|&()`\n]+")

// lintBashWrites returns one message per Bash-route write whose target
// resolves inside root, scanning text (already through the strip pipeline)
// left to right with cwd tracked through each `cd`. An empty root or cwd
// disables the check.
func lintBashWrites(text, cwd, root string) []string {
	if root == "" || cwd == "" {
		return nil
	}
	root = filepath.Clean(root)
	cur := filepath.Clean(cwd)
	home := util.Home()
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\\\r\n", " "), "\\\n", " ")
	// A fd duplication (`2>&1`, `>&2`, `<&0`, `>&-`) names no file and would
	// otherwise be cut at its `&` by the separator split, stranding the
	// operand after it in a span of its own; `&> f` is a redirect to f.
	text = fdDupRE.ReplaceAllStringFunc(text, func(m string) string { return strings.Repeat(" ", len(m)) })
	text = strings.ReplaceAll(text, "&>", " >")
	var msgs []string
	seen := map[string]bool{}
	// Walk spans with the nesting DEPTH of `(`/`$(` groups and backticks
	// tracked across the separators, because a `cd` anywhere inside such a
	// group changes the subshell's directory and not the caller's — it
	// leaks textually either way, and when the group steps INTO the
	// worktree the leak would be a false refusal, so a cd in a group makes
	// the location unknown instead. A `{ …; }` brace group runs in the
	// caller's shell, so its `cd` is tracked like any other (the `{` is
	// stripped as a leading word below). Two textual residues, both in the
	// fail-open direction or contrived: a `)` closing a `case` pattern
	// inside a group lowers the depth early, a `cd` behind `if`/`do` is
	// taken as executed, an empty `$()` or `arr=()` reads as a definition,
	// a brace group nested inside a body closes it early, and a spaced
	// `f ( )` definition is not recognised as one.
	seps := cmdSeparatorRE.FindAllStringIndex(text, -1)
	start, depth, inBacktick, inFunc := 0, 0, false, false
	for i := 0; i <= len(seps); i++ {
		end := len(text)
		if i < len(seps) {
			end = seps[i][0]
		}
		span := text[start:end]
		if i < len(seps) {
			start = seps[i][1]
		}
		tokens := strings.Fields(span)
		// Leading `VAR=value` words, `env`, `builtin`, `command` and the
		// compound-command keywords (`if cd …; then`, `do cd …`, `{ cd …; }`,
		// `! cd`, `time cd`) are not the command.
		for len(tokens) > 0 && !strings.HasPrefix(tokens[0], "-") && (strings.Contains(tokens[0], "=") || leadingWords[tokens[0]]) {
			tokens = tokens[1:]
		}
		if len(tokens) > 0 {
			cmd := filepath.Base(tokens[0])
			// Function-body bookkeeping happens BEFORE the switch so the span
			// still reaches the redirect scan: a brace group's closing span is
			// `} > f`, and a `function f { … } > /abs/path` is a redirect too
			// (a relative target stands down, since the definition left the
			// location unknown).
			switch cmd {
			case "function":
				cur, inFunc = "", true // the keyword spelling of a definition
			case "}":
				inFunc = false // a body closed; what follows runs now
			}
			switch {
			case cmd == "popd":
				cur = ""
			case cmd == "cd" || cmd == "pushd":
				if depth > 0 || inBacktick || inFunc {
					cur = ""
				} else {
					cur = trackCD(tokens, cur, home)
				}
			default:
				var hits []string
				for _, m := range redirectRE.FindAllStringSubmatch(span, -1) {
					if p, ok := resolveWriteTarget(m[1], cur, home); ok && insideRoot(p, root) {
						hits = append(hits, "> "+m[1])
					}
				}
				for _, s := range bashWriteShapes {
					if cmd != s.name {
						continue
					}
					for _, f := range s.files(tokens) {
						if p, ok := resolveWriteTarget(f, cur, home); ok && insideRoot(p, root) {
							hits = append(hits, s.marker+" "+f)
						}
					}
				}
				for _, h := range hits {
					if seen[h] {
						continue
					}
					seen[h] = true
					msgs = append(msgs, "kraken bash-lint: `"+h+"` writes into the worktree through Bash, the edit route the /do skill bans because it fails silently (a 0-match rewrite or a partial append prints a green tail) — a repo file takes the Edit tool (Write for a new file) even where a harness reminder says to prefer sed or heredocs, and any other output goes under your scratch dir, never the worktree")
				}
			}
		}
		if i < len(seps) {
			sep := text[seps[i][0]:seps[i][1]]
			if strings.Contains(sep, "()") {
				// `f() { cd …; }` is a function DEFINITION (the `function f`
				// keyword spelling is caught at the command word): its body
				// runs later, if at all, so a cd in it makes the location
				// unknown rather than moving it, until the body's `}` closes
				cur, inFunc = "", true
			}
			for _, c := range sep {
				switch c {
				case '(':
					depth++
				case ')':
					if depth > 0 {
						depth--
					}
				case '`':
					inBacktick = !inBacktick
				}
			}
		}
	}
	return msgs
}

// trackCD applies one `cd`/`pushd` command to the tracked directory.
// Unknown destinations (`cd -`, `cd $X`, `cd ~user`, and a bare `cd`, whose
// argument may have been a blanked quoted string) return "" so every later
// relative target stands down — the fail-open direction.
func trackCD(tokens []string, cur, home string) string {
	args := tokens[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		args = args[1:] // `cd -P dir`
	}
	if len(args) == 0 {
		return ""
	}
	p, ok := resolveWriteTarget(args[0], cur, home)
	if !ok {
		return ""
	}
	return p
}

// resolveWriteTarget turns one unquoted path token into an absolute path,
// or reports that it cannot be known statically.
func resolveWriteTarget(tok, cur, home string) (string, bool) {
	if tok == "" || tok == "-" || strings.ContainsAny(tok, "$*?{}`") {
		return "", false
	}
	switch {
	case tok == "~":
		return home, true
	case strings.HasPrefix(tok, "~/"):
		return filepath.Clean(filepath.Join(home, tok[2:])), true
	case strings.HasPrefix(tok, "~"):
		return "", false // ~user: unknown
	case filepath.IsAbs(tok):
		return filepath.Clean(tok), true
	}
	if cur == "" {
		return "", false
	}
	return filepath.Clean(filepath.Join(cur, tok)), true
}

// insideRoot reports whether p is root or lies beneath it.
func insideRoot(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// hasFlagLetter reports whether a single-dash flag cluster BEFORE the first
// operand carries letter — `-i`, `-pi`, `-ni`, `-i.bak` all do; a
// double-dash flag and a non-flag operand do not. Scanning stops at `--`
// and at the first operand (flags after it belong to the program's ARGV),
// and inside a cluster at the first letter in argFlags, since what follows
// such a letter is that flag's ARGUMENT, not more flags: the `i` in
// `-Mstrict` or `-Ilib` is a module name, and reading it as in-place would
// refuse a read-only perl.
func hasFlagLetter(tokens []string, letter byte, argFlags string) bool {
	for _, t := range tokens {
		if t == "--" || !strings.HasPrefix(t, "-") || t == "-" {
			return false
		}
		if strings.HasPrefix(t, "--") {
			continue
		}
		cluster := t[1:]
		if dot := strings.IndexByte(cluster, '.'); dot >= 0 {
			cluster = cluster[:dot] // -i.bak: the suffix is not flags
		}
		for i := 0; i < len(cluster); i++ {
			if cluster[i] == letter {
				return true
			}
			if strings.IndexByte(argFlags, cluster[i]) >= 0 {
				break // the rest of the cluster is this flag's argument
			}
		}
	}
	return false
}

// hasFlag reports a long flag, bare or `=value`-suffixed, anywhere in tokens.
func hasFlag(tokens []string, flag string) bool {
	for _, t := range tokens {
		if t == flag || strings.HasPrefix(t, flag+"=") {
			return true
		}
	}
	return false
}

// operands returns the non-flag tokens that are file operands: minus a
// redirect token and, for a bare one (`>`, `2>`), the target after it; minus
// the token after a flag in argFlags (`-f script.sed` reads the script);
// minus any token skip() recognises as not-a-file. A QUOTED script (`-e
// 's/a/b/'`) has already been blanked by the strip pipeline, which is why
// the token after `-e` is not consumed: in the stripped text that token is
// the file. An unquoted sed script is caught by isSedScript; an unquoted
// perl `-e print` program would be read as a file, a residue accepted
// because no agent writes a perl program without quotes.
func operands(tokens []string, argFlags map[string]bool, skip func(string) bool) []string {
	var out []string
	skipNext := false
	for _, t := range tokens {
		if skipNext {
			skipNext = false
			continue
		}
		if t == "--" || t == "\\" {
			continue
		}
		if strings.ContainsAny(t, "<>") {
			// a bare redirect (`>`, `2>`) owns the next token (fd dups were
			// blanked before the split, so none reaches here)
			skipNext = strings.TrimLeft(t, "0123456789<>") == ""
			continue
		}
		if argFlags[t] {
			skipNext = true
			continue
		}
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			continue
		}
		if skip(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// isSedScript recognises an UNQUOTED sed script operand so it is not read as
// a file: an `s`/`y` command is its delimiter appearing three times
// (`s/a/b/`, `y|x|y|` — a file named `s.go` or `s/x` has fewer), and an
// address-plus-command form (`5d`, `1,5p`, `$d`) matches sedAddrRE. Quoted
// scripts never reach here. A path that happens to match stands down, the
// fail-open direction.
func isSedScript(tok string) bool {
	if len(tok) >= 2 && (tok[0] == 's' || tok[0] == 'y') && !isWordByte(tok[1]) {
		return strings.Count(tok[1:], tok[1:2]) >= 3
	}
	return sedAddrRE.MatchString(tok)
}

var sedAddrRE = regexp.MustCompile(`^[0-9,$]*[dpqnNgGhHxlP=]?$`)

func isWordByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// gitRootAbove walks up from dir to the nearest directory holding a `.git`
// entry — existence, never directory-ness, since a worktree's `.git` is a
// file — and returns "" when there is none (then the write check is off).
func gitRootAbove(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
