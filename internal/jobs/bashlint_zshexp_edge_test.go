package jobs

import (
	"strings"
	"testing"
)

// bashlint_zshexp_edge_test.go — the EDGE dimension of the zsh-only
// parameter-expansion shape bash-lint gains (contract-bashlint.md). Written
// blind from the frozen contract: bashlint.go's implementation was never
// read. Every fixture is invented — made-up variable names, paths and values.
//
// Blocks are asserted on the `zsh-only expansion` substring; allows on "".

const athenaEdgeZshWant = "zsh-only expansion"

// TestAthenaEdgeZshExpansionEveryModifierLetterBlocks walks the whole
// modifier alphabet the contract names, one bare `${name:<letter>}` each.
func TestAthenaEdgeZshExpansionEveryModifierLetterBlocks(t *testing.T) {
	for _, letter := range []string{"a", "A", "c", "e", "h", "l", "P", "q", "Q", "r", "s", "t", "u", "x"} {
		cmd := "echo ${zorp_file:" + letter + "}"
		t.Run(letter, func(t *testing.T) {
			msg := lintBashCommand(cmd)
			if !strings.Contains(msg, athenaEdgeZshWant) {
				t.Fatalf("modifier %q: want block for %q, got %q", letter, cmd, msg)
			}
		})
	}
}

// TestAthenaEdgeZshExpansionBlocks pins every blocked family in every
// position the contract calls shell-in-command-position: command start, after
// ; | && ||, inside $( ), backticks, a later line, double quotes (which do not
// stop expansion), and unusual spacing.
func TestAthenaEdgeZshExpansionBlocks(t *testing.T) {
	cases := []struct{ name, cmd string }{
		// family 1: modifiers, with chained forms and identifier shapes.
		{"tail modifier then offset", `echo ${qux_log:t:0:8}`},
		{"substitute modifier", `echo ${wombat:s/alpha/beta}`},
		{"identifier with digits and underscores", `echo ${_snark_42:h}`},
		{"uppercase identifier", `echo ${TARGET_DIR:a}`},
		{"modifier as the command word", `${tool_bin:h}/bin/frobnicate --dry`},
		{"after semicolon", `ls /tmp/zz; echo ${blip:r}`},
		{"after pipe", `printf 'x' | cat ${blip:a}`},
		{"after and-and", `cd /tmp/quell && echo ${blip:u}`},
		{"after or-or", `test -f /tmp/nope || echo ${blip:l}`},
		{"inside command substitution", `n=$(echo ${blip:t}); echo "$n"`},
		{"inside nested command substitution", `x=$(cat $(echo ${blip:h})/conf)`},
		{"inside backticks", "x=`echo ${blip:t}`; echo $x"},
		{"on a later line", "ls -la /tmp/fizz\necho ${blip:e}"},
		{"tab spacing", "echo\t\t${blip:t}\t"},
		{"no space before semicolon", `echo ${blip:t};ls`},
		{"glued to surrounding text", `cp /tmp/a.txt /tmp/out-${blip:r}.bak`},
		// family 1 inside double quotes: double quotes do not stop expansion.
		{"double-quoted modifier", `echo "${blip:t}"`},
		{"double-quoted in the middle of text", `echo "file is ${blip:t} now"`},
		{"apostrophe inside double quotes is literal", `echo "it's ${blip:t}"`},
		{"single quotes inside double quotes are literal", `echo "'${blip:h}'"`},
		{"double-quoted inside command substitution", `echo "$(echo "${blip:t}")"`},
		{"double-quoted after a single-quoted span", `echo 'plain' "${blip:r}"`},
		// family 2: parenthesised flag groups.
		{"lowercase flag", `echo ${(L)greeting}`},
		{"split-lines flag", `for w in ${(f)rows}; do echo $w; done`},
		{"join flag with delimiter", `echo ${(j:,:)parts}`},
		{"split flag with space delimiter", `echo ${(s: :)words}`},
		{"flag group double-quoted", `echo "${(U)greeting}"`},
		{"flag group after pipe", `ls | head -1 && echo ${(L)greeting}`},
		// family 3: =, ~ and ^ forms.
		{"equals split", `for a in ${=argline}; do echo $a; done`},
		{"glob subst", `ls ${~pattern_var}`},
		{"rc-expand", `echo ${^prefixes}`},
		{"equals split double-quoted", `echo "${=argline}"`},
		{"glob subst inside command substitution", `n=$(ls ${~pattern_var} | wc -l)`},
		// family 4: nested command substitution as the parameter.
		{"nested command substitution with default", `echo ${$(basename "$blip"):-none}`},
		{"nested command substitution double-quoted", `echo "${$(pwd):t}"`},
		{"nested command substitution at command start", `${$(which frob):-/bin/true} --help`},
		// a heredoc ends; the shell after it is still linted.
		{"shell after quoted heredoc", "cat > /tmp/zz.txt <<'EOF'\nharmless body\nEOF\necho ${blip:h}"},
		// a comment on one line does not hide the next line.
		{"shell after comment line", "# plain note\necho ${blip:t}"},
		// a closed [[ ]] span does not hide what follows it.
		{"shell after a cond span", `[[ -n $blip ]] && echo ${blip:t}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := lintBashCommand(c.cmd)
			if !strings.Contains(msg, athenaEdgeZshWant) {
				t.Fatalf("want %q block for %q, got %q", athenaEdgeZshWant, c.cmd, msg)
			}
		})
	}
}

// TestAthenaEdgeZshExpansionAllows pins every bash-compatible form the
// contract keeps, the near-misses next to each blocked family, and the four
// non-shell regions (single quotes, heredoc bodies, comments, [[ ]] spans).
func TestAthenaEdgeZshExpansionAllows(t *testing.T) {
	cases := []struct{ name, cmd string }{
		// offsets and lengths.
		{"offset and length", `echo ${blip:0:8}`},
		{"negative offset with space", `echo ${blip: -45}`},
		{"offset only", `echo ${blip:1}`},
		{"length", `echo ${#blip}`},
		{"offset whose length is a nested length", `echo ${blip:0:${#other}}`},
		{"array slice", `echo ${list_arr[@]:1:2}`},
		// defaults and tests, including letters after the operator.
		{"default", `echo ${blip:-fallback}`},
		{"default starting with a modifier letter", `echo ${blip:-tmp}`},
		{"alternate starting with a modifier letter", `echo ${blip:+us}`},
		{"assign default starting with a modifier letter", `echo ${blip:=alpha}`},
		{"error message", `echo ${blip:?missing blip}`},
		{"non-colon default", `echo ${blip-xyz}`},
		{"default holding a command substitution", `echo ${blip:-$(basename "$other")}`},
		{"double-quoted default", `echo "${blip:-none}"`},
		// pattern trims and substitution.
		{"longest suffix trim", `echo "${blip%% *}"`},
		{"longest prefix trim", `echo "${blip##*/}"`},
		{"first substitution", `echo ${blip/a/b}`},
		{"global substitution", `echo ${blip//a/b}`},
		{"shortest prefix trim", `echo ${blip#x}`},
		{"shortest suffix trim", `echo ${blip%x}`},
		// plain forms, indirection, case changes.
		{"plain dollar", `echo $blip`},
		{"braced", `echo ${blip}`},
		{"array all", `echo ${list_arr[@]}`},
		{"indirection", `echo ${!blip}`},
		{"uppercase", `echo ${blip^^}`},
		{"lowercase", `echo ${blip,,}`},
		// near-misses: the colon or letter sits outside the braces.
		{"closed brace then colon text", `echo ${HOME}:t`},
		{"double-quoted closed brace then colon", `echo "${outdir}:h"`},
		{"plain command substitution", `echo $(basename "$blip")`},
		{"arithmetic with a colon-free brace", `echo $(( ${#blip} + 1 ))`},
		// single-quoted spans are data.
		{"single-quoted modifier", `echo '${blip:t}'`},
		{"single-quoted flag group", `sed 's/${(L)greeting}/x/' /tmp/in.txt`},
		{"single-quoted equals split", `printf '%s\n' '${=argline}'`},
		{"single-quoted nested substitution", `echo '${$(pwd):t}'`},
		{"single-quoted after a clean command", `ls /tmp; grep -c '${blip:h}' /tmp/log.txt`},
		// heredoc bodies are data.
		{"quoted heredoc body", "cat > /tmp/notes.md <<'EOF'\n${blip:t} and ${(L)greeting}\n${=argline} ${~pat} ${^pre}\n${$(pwd):h}\nEOF"},
		{"unquoted heredoc body", "cat > /tmp/notes.md <<EOF\nthe zsh form ${blip:t} is documented here\nEOF\necho done"},
		{"tab-stripped heredoc body", "cat <<-EOF\n\t${(f)rows}\n\tEOF\nls"},
		// comments are data.
		{"trailing comment", `ls -la  # zsh would write ${blip:t}`},
		{"comment line", "# ${(L)greeting} and ${=argline}\nls"},
		// [[ ]] spans are data for this lint.
		{"cond span modifier", `[[ -n ${blip:t} ]] && echo hit`},
		{"cond span equals split", `[[ $a == ${=argline} ]] && echo same`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := lintBashCommand(c.cmd); msg != "" {
				t.Fatalf("want clean for %q, got block: %s", c.cmd, msg)
			}
		})
	}
}

// TestAthenaEdgeZshExpansionMessage pins the fixed parts of the message: the
// shape name, the basename remedy, the permission-prompt reason, and the
// `kraken bash-lint: ` prefix on its line.
func TestAthenaEdgeZshExpansionMessage(t *testing.T) {
	msg := lintBashCommand(`echo ${blip:t}`)
	if !strings.Contains(msg, athenaEdgeZshWant) {
		t.Fatalf("message %q lacks %q", msg, athenaEdgeZshWant)
	}
	if !strings.Contains(msg, "basename") {
		t.Fatalf("message %q does not name basename as a remedy", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "permission") {
		t.Fatalf("message %q does not say the shape opens a permission prompt", msg)
	}
	for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
		if strings.Contains(line, athenaEdgeZshWant) && !strings.HasPrefix(line, "kraken bash-lint: ") {
			t.Fatalf("shape line %q lacks the %q prefix", line, "kraken bash-lint: ")
		}
	}
}

// TestAthenaEdgeZshExpansionOneLinePerShape pins "one line per shape": many
// zsh hits in one command report the shape once, and a command carrying both
// this shape and an existing one reports both, each on its own line.
func TestAthenaEdgeZshExpansionOneLinePerShape(t *testing.T) {
	many := lintBashCommand(`echo ${blip:t} ${(L)greeting} ${=argline}; ls ${blip:h}`)
	if n := strings.Count(many, athenaEdgeZshWant); n != 1 {
		t.Fatalf("four zsh hits reported the shape %d times, want 1: %q", n, many)
	}
	both := lintBashCommand(`echo ===; echo ${blip:t}`)
	if !strings.Contains(both, athenaEdgeZshWant) || !strings.Contains(both, "equals-expanded") {
		t.Fatalf("want both the zsh shape and the equals shape, got %q", both)
	}
	var zshLine, eqLine int = -1, -1
	for i, line := range strings.Split(strings.TrimRight(both, "\n"), "\n") {
		if strings.Contains(line, athenaEdgeZshWant) {
			zshLine = i
		}
		if strings.Contains(line, "equals-expanded") {
			eqLine = i
		}
	}
	if zshLine < 0 || eqLine < 0 || zshLine == eqLine {
		t.Fatalf("the two shapes must sit on separate lines (zsh %d, equals %d): %q", zshLine, eqLine, both)
	}
}
