package jobs

import (
	"strings"
	"testing"
)

// Edge tests for slice B of the dream-2026-09-28 contract: the cell's bash
// lint blocks bash's exit-status array `$PIPESTATUS`, which expands to
// nothing under zsh. Authored blind from artifacts/50-contract.md; each case
// names the obligation it pins.

// edgePipestatusLines returns the lines of this shape's message: those that
// mention pipestatus in any case. Other shapes' messages never do.
func edgePipestatusLines(msg string) []string {
	var out []string
	for _, l := range strings.Split(msg, "\n") {
		if strings.Contains(strings.ToLower(l), "pipestatus") {
			out = append(out, l)
		}
	}
	return out
}

// TestEdgePipestatusBlocksEveryUnquotedSpelling pins B1 (and B2 for the
// double-quoted rows) over the positions an expansion can hide in: at the
// end of a command, glued to other text, inside $( ), backticks, [[ ]],
// $(( )), a parameter-expansion default, after a closed heredoc, and after a
// `#` that sits inside a word and so opens no comment.
func TestEdgePipestatusBlocksEveryUnquotedSpelling(t *testing.T) {
	for _, c := range []struct{ name, cmd string }{
		{"bare at end", `false | true; echo $PIPESTATUS`},
		{"braced no index", `false | true; echo ${PIPESTATUS}`},
		{"braced index zero", `false | true; echo ${PIPESTATUS[0]}`},
		{"braced all elements", `false | true; echo ${PIPESTATUS[@]}`},
		{"braced default operator", `false | true; echo ${PIPESTATUS:-none}`},
		{"unbraced followed by bracket", `false | true; echo $PIPESTATUS[1]`},
		{"terminated by semicolon, no space", `a | b; echo $PIPESTATUS;ls`},
		{"terminated by dash", `a | b; echo $PIPESTATUS-rc`},
		{"terminated by dot", `a | b; echo $PIPESTATUS.`},
		{"glued after text", `a | b; echo rc=$PIPESTATUS`},
		{"inside command substitution", `a | b; rc=$(echo ${PIPESTATUS[0]})`},
		{"inside backticks", "a | b; rc=`echo $PIPESTATUS`"},
		{"inside double brackets", `a | b; [[ ${PIPESTATUS[0]} -eq 0 ]] && echo ok`},
		{"inside single brackets", `a | b; [ ${PIPESTATUS[0]} -eq 0 ] && echo ok`},
		{"inside dollar arithmetic", `a | b; echo $(( ${PIPESTATUS[0]} + 1 ))`},
		{"shell after a closed quoted heredoc", "cat > /tmp/x <<'EOF'\nbody\nEOF\na | b; echo ${PIPESTATUS[0]}"},
		{"hash inside a word opens no comment", `a | b; echo a#$PIPESTATUS`},
		{"escaped backslash leaves the dollar live", `a | b; echo \\$PIPESTATUS`},
		{"second line of a multi-line command", "go test ./... | tee /tmp/out\necho ${PIPESTATUS[0]}"},
		{"quoted part then unquoted part", `echo '$PIPESTATUS'; echo $PIPESTATUS`},
		// B2
		{"double quoted", `a | b; echo "rc=${PIPESTATUS[0]}"`},
		{"double quoted bare", `a | b; echo "$PIPESTATUS"`},
		{"apostrophe inside double quotes is literal", `a | b; echo "it's ${PIPESTATUS[0]}"`},
		{"escaped quote earlier in the double-quoted span", `a | b; echo "say \"hi\" ${PIPESTATUS[1]}"`},
		{"double quotes inside command substitution", `rc="$(a | b; echo "${PIPESTATUS[0]}")"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := lintBashCommand(c.cmd)
			if len(edgePipestatusLines(msg)) == 0 {
				t.Fatalf("want the pipestatus block for %q, got %q", c.cmd, msg)
			}
		})
	}
}

// TestEdgePipestatusAllowsNonExpansions pins B3–B9: text that names the
// array but does not expand bash's spelling of it.
func TestEdgePipestatusAllowsNonExpansions(t *testing.T) {
	for _, c := range []struct{ name, cmd string }{
		// B3
		{"single quoted", `echo '$PIPESTATUS'`},
		{"single quoted braced", `echo '${PIPESTATUS[0]}'`},
		{"awk program in single quotes", `awk '{print $PIPESTATUS}' f`},
		{"double quotes inside single quotes", `echo 'say "${PIPESTATUS[0]}"'`},
		// B4
		{"quoted heredoc body", "cat > /tmp/x.sh <<'EOF'\nfalse | true\necho ${PIPESTATUS[0]}\nEOF"},
		{"unquoted heredoc body", "cat > /tmp/x.sh <<EOF\necho $PIPESTATUS\nEOF"},
		{"tab-stripped heredoc body", "cat <<-EOF\n\techo ${PIPESTATUS[@]}\n\tEOF\necho after"},
		{"python heredoc", "python3 - <<'PY'\nprint('${PIPESTATUS[0]}')\nPY"},
		// B5
		{"trailing comment", `a | b  # ${PIPESTATUS[0]} would be wrong here`},
		{"comment line", "# bash would use $PIPESTATUS\na | b; echo ${pipestatus[1]}"},
		// B6
		{"bare word", `grep -rn PIPESTATUS .`},
		{"bare word with index", `echo PIPESTATUS[0]`},
		// B7
		{"escaped dollar", `echo \$PIPESTATUS`},
		{"escaped dollar braced", `echo \${PIPESTATUS[0]}`},
		{"escaped dollar in double quotes", `echo "\$PIPESTATUS"`},
		// B8
		{"zsh lowercase bare", `a | b; echo $pipestatus`},
		{"zsh lowercase indexed", `a | b; echo ${pipestatus[1]}`},
		{"zsh lowercase all", `a | b; echo "${pipestatus[@]}"`},
		// B9
		{"longer name with underscore", `echo $PIPESTATUS_LOG`},
		{"longer name braced", `echo ${PIPESTATUS_LOG}`},
		{"longer name with digit", `echo $PIPESTATUS2`},
		{"longer name with letter", `echo $PIPESTATUSX`},
		{"name that ends with the letters", `echo $MYPIPESTATUS`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if msg := lintBashCommand(c.cmd); msg != "" {
				t.Fatalf("want clean for %q, got %q", c.cmd, msg)
			}
		})
	}
}

// TestEdgePipestatusRepeatsYieldOneMessage pins B11: however many times, and
// in however many spellings, the expansion appears, the shape reports once.
func TestEdgePipestatusRepeatsYieldOneMessage(t *testing.T) {
	cmd := "a | b; echo ${PIPESTATUS[0]} ${PIPESTATUS[1]} \"$PIPESTATUS\"\nc | d; echo $PIPESTATUS"
	msg := lintBashCommand(cmd)
	if got := edgePipestatusLines(msg); len(got) != 1 {
		t.Fatalf("B11: want exactly one pipestatus line, got %d in %q", len(got), msg)
	}
	if n := len(strings.Split(msg, "\n")); n != 1 {
		t.Fatalf("B11: a command tripping only this shape must yield one line, got %d: %q", n, msg)
	}
}

// TestEdgePipestatusCombinesWithOtherShapes pins B12/B13: with one, then two,
// other existing shapes tripped alongside, every shape reports on its own
// line, the line count equals the shape count, and every line carries the
// `kraken bash-lint: ` prefix.
func TestEdgePipestatusCombinesWithOtherShapes(t *testing.T) {
	for _, c := range []struct {
		name   string
		cmd    string
		others []string // substrings the existing shapes' messages carry (bashlint_test.go)
	}{
		{"with timeout", `timeout 30 go test ./... | tee /tmp/o; echo ${PIPESTATUS[0]}`, []string{"timeout(1)"}},
		{"with git stash", `git stash; a | b; echo $PIPESTATUS`, []string{"git stash"}},
		{"with equals banner and path", `echo ===; path=/tmp; a | b; echo "${PIPESTATUS[0]}"`, []string{"equals-expanded", "clobbers"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := lintBashCommand(c.cmd)
			lines := strings.Split(msg, "\n")
			if len(lines) != len(c.others)+1 {
				t.Fatalf("B12: want %d lines, got %d: %q", len(c.others)+1, len(lines), msg)
			}
			if got := edgePipestatusLines(msg); len(got) != 1 {
				t.Fatalf("B12: want one pipestatus line, got %d: %q", len(got), msg)
			}
			for _, o := range c.others {
				found := 0
				for _, l := range lines {
					if strings.Contains(l, o) {
						found++
					}
				}
				if found != 1 {
					t.Fatalf("B12: want the %q shape on exactly one line, found %d: %q", o, found, msg)
				}
			}
			for _, l := range lines {
				if !strings.HasPrefix(l, "kraken bash-lint: ") {
					t.Fatalf("B13: line %q lacks the `kraken bash-lint: ` prefix", l)
				}
			}
		})
	}
}

// TestEdgePipestatusSoleMessageShape pins B10/B13 on the shape's own line
// when nothing else trips: prefixed, names the lowercase zsh array, and says
// it is one-indexed; with no trailing blank line.
func TestEdgePipestatusSoleMessageShape(t *testing.T) {
	msg := lintBashCommand(`a | b; echo ${PIPESTATUS[0]}`)
	if msg == "" || strings.HasSuffix(msg, "\n") {
		t.Fatalf("want one un-terminated message line, got %q", msg)
	}
	if !strings.HasPrefix(msg, "kraken bash-lint: ") {
		t.Fatalf("B13: %q lacks the prefix", msg)
	}
	if !strings.Contains(msg, "pipestatus") {
		t.Fatalf("B10: %q does not name the lowercase zsh array", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "one-indexed") {
		t.Fatalf("B10: %q does not say the zsh array is one-indexed", msg)
	}
}
