package jobs

import (
	"strings"
	"testing"
)

// bashlint_zshexp_logic_test.go — the LOGIC dimension of the bash-lint
// zsh-only expansion contract (contract-bashlint.md). Written blind from the
// frozen contract: the new shape's implementation was never read. Every
// fixture command is invented (made-up variable names, paths and values).

const athenaLogicZshExpWant = "zsh-only expansion"

// TestAthenaLogicZshExpansionBlocks pins the four blocked families: a zsh
// modifier after the colon, a parenthesised flag group, the =/~/^ forms and
// a nested command substitution as the parameter — in every shell position,
// double quotes included.
func TestAthenaLogicZshExpansionBlocks(t *testing.T) {
	cases := []struct{ name, cmd string }{
		// Family 1: modifiers.
		{"tail modifier", `echo ${zorbfile:t}`},
		{"tail then offset", `echo ${zorbfile:t:0:8}`},
		{"head modifier", `cd ${quillpath:h}`},
		{"root modifier", `mv a.txt ${quillname:r}.bak`},
		{"absolute modifier", `ls ${relwidget:a}`},
		{"substitute modifier", `echo ${greeting:s/hi/yo}`},
		{"lowercase modifier", `echo ${shouty:l}`},
		{"uppercase modifier", `echo ${quiet:u}`},
		{"chained tail and root", `echo ${archive_tgz:t:r}`},
		{"identifier with underscore and digits", `echo ${out_dir2:h}`},
		{"leading underscore identifier", `echo ${_private:t}`},
		// Family 2: flag groups.
		{"lowercase flag", `echo ${(L)titlecase}`},
		{"split lines flag", `for ln in ${(f)manylines}; do echo $ln; done`},
		{"join flag", `echo ${(j:,:)parts}`},
		{"split on space flag", `echo ${(s: :)wordlist}`},
		// Family 3: =, ~, ^ forms.
		{"equals split", `mytool ${=extraopts}`},
		{"tilde glob", `ls ${~globpat}`},
		{"caret rc-expand", `echo pre${^suffixes}`},
		// Family 4: nested command substitution.
		{"nested command substitution with default", `echo ${$(basename "$blobfile"):-fallback}`},
		// Positions: double quotes do not stop expansion.
		{"double-quoted tail", `echo "${zorbfile:t}"`},
		{"double-quoted head in assignment", `parent="${quillpath:h}"; echo "$parent"`},
		{"double-quoted flag group", `echo "${(L)titlecase}"`},
		{"double-quoted equals split", `echo "${=extraopts}"`},
		{"double-quoted nested substitution", `echo "${$(date +%s):-0}"`},
		{"inside command substitution", `stem=$(echo ${zorbfile:r}); echo "$stem"`},
		{"after && in a compound", `cd /tmp/athena-fake && cp a.txt ${dest:h}/`},
		{"second half of a pipeline", `ls /tmp/athena-fake | xargs -n1 echo ${pfx:u}`},
		{"in a for loop body", `for zz in a b; do echo ${zz:t}; done`},
		{"inside a double-quoted string with text around it", `echo "file is ${zorbfile:t} ok"`},
		{"after a heredoc body", "cat > /tmp/athena-fake.txt <<'EOF'\nplain payload\nEOF\necho ${zorbfile:t}"},
		{"before a trailing comment", `echo ${zorbfile:t}  # strip the dir`},
		{"on a later line", "ls /tmp/athena-fake\necho ${zorbfile:h}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := lintBashCommand(c.cmd)
			if msg == "" {
				t.Fatalf("want block for %q, got clean", c.cmd)
			}
			if !strings.Contains(msg, athenaLogicZshExpWant) {
				t.Fatalf("block message for %q does not mention %q: %q", c.cmd, athenaLogicZshExpWant, msg)
			}
		})
	}
}

// TestAthenaLogicZshExpansionEveryModifierLetter pins the contract's
// modifier set letter by letter: each of a A c e h l P q Q r s t u x after
// `${name:` blocks.
func TestAthenaLogicZshExpansionEveryModifierLetter(t *testing.T) {
	for _, letter := range []string{"a", "A", "c", "e", "h", "l", "P", "q", "Q", "r", "s", "t", "u", "x"} {
		cmd := "echo ${frobvar:" + letter + "}"
		t.Run(letter, func(t *testing.T) {
			msg := lintBashCommand(cmd)
			if !strings.Contains(msg, athenaLogicZshExpWant) {
				t.Fatalf("modifier %q in %q: want %q, got %q", letter, cmd, athenaLogicZshExpWant, msg)
			}
		})
	}
}

// TestAthenaLogicZshExpansionAllows pins the bash-compatible forms and the
// non-shell spans the contract keeps open: offsets, defaults, trims,
// substitutions, case conversions, and every blocked form when it sits in a
// single-quoted span, a heredoc body, a comment or a [[ … ]] span.
func TestAthenaLogicZshExpansionAllows(t *testing.T) {
	cases := []struct{ name, cmd string }{
		// Offsets and lengths.
		{"offset and length", `echo "${zorbfile:0:8}"`},
		{"negative offset with space", `echo "${zorbfile: -45}"`},
		{"offset only", `echo "${zorbfile:1}"`},
		{"length", `echo "${#zorbfile}"`},
		{"unquoted offset and length", `echo ${zorbfile:2:3}`},
		// Defaults and tests.
		{"default", `echo "${zorbfile:-none}"`},
		{"alternate", `echo "${zorbfile:+set}"`},
		{"assign default", `echo "${zorbfile:=fallback.txt}"`},
		{"error if unset", `echo "${zorbfile:?zorbfile is required}"`},
		{"default without colon", `echo "${zorbfile-none}"`},
		{"unquoted default", `echo ${zorbfile:-none}`},
		// Pattern trims and substitution.
		{"longest suffix trim", `echo "${sentence%% *}"`},
		{"longest prefix trim", `echo "${quillpath##*/}"`},
		{"single substitution", `echo "${greeting/hi/yo}"`},
		{"global substitution", `echo "${greeting//hi/yo}"`},
		{"shortest prefix trim", `echo "${tagname#v}"`},
		{"shortest suffix trim", `echo "${archive%.gz}"`},
		// Plain forms.
		{"plain dollar", `echo $zorbfile`},
		{"braced", `echo "${zorbfile}"`},
		{"array all", `echo "${widgets[@]}"`},
		{"indirection", `echo "${!refname}"`},
		{"bash uppercase", `echo "${shouty^^}"`},
		{"bash lowercase", `echo "${quiet,,}"`},
		// Blocked forms where they are data, not expansions.
		{"single-quoted tail", `echo '${zorbfile:t}'`},
		{"single-quoted flag group", `printf '%s\n' '${(L)titlecase}'`},
		{"single-quoted equals split", `echo '${=extraopts}'`},
		{"single-quoted nested substitution", `echo '${$(basename "$x"):-y}'`},
		{"heredoc body with modifiers", "cat > /tmp/athena-fake.md <<'EOF'\nuse ${zorbfile:t} and ${(L)titlecase} and ${=extraopts}\nEOF\necho done"},
		{"heredoc body with nested substitution", "python3 - <<'PY'\nprint('${$(basename x):-y}')\nPY"},
		{"comment line", "# zsh would write ${zorbfile:t} here\nls /tmp/athena-fake"},
		{"trailing comment", `ls /tmp/athena-fake  # not ${quillpath:h}`},
		{"double-bracket span", `[[ ${zorbfile:t} == fake.txt ]] && echo hit`},
		{"double-bracket span with flag group", `[[ -n ${(L)titlecase} ]] && echo set`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := lintBashCommand(c.cmd); msg != "" {
				t.Fatalf("want clean for %q, got block: %s", c.cmd, msg)
			}
		})
	}
}

// TestAthenaLogicZshExpansionMessage pins the fixed parts of the hit's
// message: the `kraken bash-lint: ` line prefix, the `zsh-only expansion`
// name, the `basename` remedy, and that it explains the prompt nobody in a
// cell answers.
func TestAthenaLogicZshExpansionMessage(t *testing.T) {
	msg := lintBashCommand(`echo "${zorbfile:t}"`)
	if msg == "" {
		t.Fatal("want a block, got clean")
	}
	if !strings.HasPrefix(msg, "kraken bash-lint: ") {
		t.Fatalf("message does not open with the bash-lint prefix: %q", msg)
	}
	for _, want := range []string{athenaLogicZshExpWant, "basename"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %q", want, msg)
		}
	}
	if !strings.Contains(strings.ToLower(msg), "prompt") {
		t.Fatalf("message does not say the shape opens a prompt: %q", msg)
	}
	if strings.Count(msg, "\n") != 0 {
		t.Fatalf("a single hit must be one line, got %q", msg)
	}
}

// TestAthenaLogicZshExpansionOneLinePerShape pins "one line per shape": two
// zsh-only expansions in one command are one shape (one line), and a command
// that also carries an existing fatal shape gets one line for each, every
// line prefixed.
func TestAthenaLogicZshExpansionOneLinePerShape(t *testing.T) {
	msg := lintBashCommand(`echo ${alpha:t} "${(L)beta}" ${=gamma}`)
	if got := strings.Count(msg, athenaLogicZshExpWant); got != 1 {
		t.Fatalf("three zsh-only expansions of one shape: want the shape named once, got %d in %q", got, msg)
	}
	if strings.Count(msg, "\n") != 0 {
		t.Fatalf("one shape must be one line, got %q", msg)
	}

	both := lintBashCommand(`echo ===; echo ${alpha:h}`)
	lines := strings.Split(both, "\n")
	if len(lines) != 2 {
		t.Fatalf("an equals word plus a zsh-only expansion: want 2 lines, got %d in %q", len(lines), both)
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "kraken bash-lint: ") {
			t.Fatalf("line %q lacks the bash-lint prefix", ln)
		}
	}
	if !strings.Contains(both, "equals-expanded") || !strings.Contains(both, athenaLogicZshExpWant) {
		t.Fatalf("want both shapes named, got %q", both)
	}
}
