package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLintBashCommandBlocks pins the four fatal shapes bash-lint exists
// for, each taken from a measured cell failure.
func TestLintBashCommandBlocks(t *testing.T) {
	cases := []struct {
		name, cmd, want string
	}{
		{"bare triple equals separator", `sed -n 1,5p f.go; echo ===; ls -la`, "equals-expanded"},
		{"long equals banner", `cat a.txt; echo ================; cat b.txt`, "equals-expanded"},
		{"equals-prefixed word", `grep -n foo f; echo ===SECTION===; grep -n bar f`, "equals-expanded"},
		{"timeout wrapper", `timeout 300 python -m pytest tests/`, "timeout(1)"},
		{"timeout mid-compound", `cd /tmp && timeout 600 ./run.sh 2>/dev/null | wc -l`, "timeout(1)"},
		{"unquoted include glob", `grep -rn --include=*.py "needle" .`, "no matches found"},
		{"unquoted exclude glob", `rg --exclude=*.min.js pattern src/`, "no matches found"},
		{"path assignment", `path=/tmp/scratch; cp a $path/`, "clobbers"},
		{"fpath assignment", `fpath=/tmp; echo hi`, "clobbers"},
		{"path loop variable", `for f in a b; do path=$f; kraken mine reduce $path; done`, "clobbers"},
		{"single-bracket double equals", `[ "$a" == "$b" ] && echo ok`, "equals-expanded"},
		{"code before a trailing comment still lints", `echo ===  # explain`, "equals-expanded"},
		{"hash inside a word is not a comment", `grep -rn --include=*.py a#b .`, "no matches found"},
		{"path assignment in while head", `while path=$(next); do echo done; done`, "clobbers"},
		{"status assignment", `out=$(python3 lint.py); status=$?; echo "$status"`, "read-only"},
		{"status assignment opening a command", `status=1; exit $status`, "read-only"},
		{"status loop variable", `for f in a b; do status=$f; echo $status; done`, "read-only"},
		{"bare git stash", `git stash`, "git stash"},
		{"git stash before redirect", `git stash 2>/dev/null; ./run.sh`, "git stash"},
		{"git stash in compound", `git stash list | head -2; cd svc && git -C .. stash 2>/dev/null; echo ok`, "git stash"},
		{"git stash pop", `git stash pop --index`, "git stash"},
		{"git stash push with message", `git stash push -m wip`, "git stash"},
		{"git stash with flag", `git stash -u`, "git stash"},
		{"git stash drop", `git stash drop stash@{0}`, "git stash"},
		{"git -C stash", `git -C /tmp/wt stash`, "git stash"},
		{"git stash in backticks", "x=`git stash` && echo done", "git stash"},
		{"path-qualified git stash", `/usr/bin/git stash pop`, "git stash"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := lintBashCommand(c.cmd)
			if msg == "" {
				t.Fatalf("want block for %q, got clean", c.cmd)
			}
			if !strings.Contains(msg, c.want) {
				t.Fatalf("block message %q does not mention %q", msg, c.want)
			}
		})
	}
}

// TestLintBashCommandAllows pins the false-positive fences: quoted
// separators, heredoc payloads (data, not shell), [[ ]] operators, test's
// lone `=`, and ordinary commands. The heredoc cases mirror the measured
// guardrail misfires (a Python `k = None` payload blocked by a text-match
// hook) this design must not reproduce.
func TestLintBashCommandAllows(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"quoted separator", `echo "==="; ls`},
		{"single-quoted separator", `echo '====SECTION===='; ls`},
		{"lone equals in test", `[ "$a" = "$b" ] && echo same`},
		{"double-equals in cond", `[[ $x == foo* ]] && echo hit`},
		{"quoted include glob", `grep -rn --include='*.py' needle .`},
		{"double-quoted include glob", `grep -rn --include="*.go" needle .`},
		{"python heredoc with equals and timeout", "python3 - <<'PY'\nk = None\nt = \"===\"\ntimeout = 300\nfor k in range(2):\n    pass\nPY"},
		{"cat heredoc with banner", "cat > /tmp/x.md <<'EOF'\n=== Findings ===\ntimeout 300 python ran here\nEOF\necho done"},
		{"tabbed heredoc delimiter", "cat <<-EOF\n\t=== body ===\n\tEOF\necho after"},
		{"var assignment not path", `mypath=/tmp; echo $mypath`},
		{"uppercase PATH assignment", `PATH=/usr/bin:$PATH go build ./...`},
		{"status as an argument, not an assignment", `gh api -f status=open repos/x/y/issues`},
		{"var assignment not status", `mystatus=$?; echo $mystatus`},
		{"rc assignment is the recommended form", `out=$(cmd); rc=$?; echo $rc`},
		{"plain command", `go test -tags sqlite_fts5 ./...`},
		{"timeout as word in quoted string", `echo "the timeout 30 case is handled"`},
		{"herestring", `grep foo <<< "=== data ==="`},
		{"arithmetic equality", `(( count == 0 )) && echo done`},
		{"dollar arithmetic", `echo $(( n == 1 ))`},
		{"arith left shift then shell", "a=$((1 << n))\necho after"},
		{"path as gh api field", `gh api repos/o/r/contents -f path=README.md`},
		{"path as curl data", `curl -d path=/tmp -X POST http://x`},
		{"comment line with banner", "# banner === here\nls -la"},
		{"comment line with timeout", "# timeout 30 was tried\nls"},
		{"herestring plus heredoc payload", "grep x <<< \"$y\" && python3 - <<'PY'\ntimeout = 300\nk = None\nPY"},
		{"trailing comment with timeout", `ls  # timeout 5 was tried`},
		{"trailing comment with banner", `ls -la  # === section marker ===`},
		{"quoted paren never opens an arith span", "grep -c '((' f\ncat <<'EOF'\n=== payload ===\nEOF"},
		{"quoted parens same line, heredoc intact", "grep -c '((' f ; cat <<'EOF' ; echo '))'\n=== payload ===\nEOF"},
		{"arithmetic spanning lines is legal", "if ((\n  count == 0\n)); then echo done; fi"},
		{"flag ending in a keyword is not an assignment", `cmd --until path=x`},
		{"git config --local path field", `git config --local path=x`},
		{"fragment hash is not a comment", `curl https://host/x#frag`},
		{"git stash list", `git stash list`},
		{"git stash list piped", `git stash list | head -2`},
		{"git stash show", `git stash show -p stash@{0}`},
		{"git stash in quotes", `echo "never run git stash here"`},
		{"git stash in heredoc payload", "cat > /tmp/report.md <<'EOF'\nthe agent ran git stash pop --index to recover\nEOF"},
		{"git stash in comment", "# git stash was the mistake\ngit status"},
		{"stash as a positional ref", `git log --oneline stash`},
		{"git diff against stash ref", `git diff --stat stash`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := lintBashCommand(c.cmd); msg != "" {
				t.Fatalf("want clean for %q, got block: %s", c.cmd, msg)
			}
		})
	}
}

// TestLintBashCommandShellAfterHeredoc holds that shell text FOLLOWING a
// heredoc body is still linted — the strip removes only the body.
func TestLintBashCommandShellAfterHeredoc(t *testing.T) {
	cmd := "cat > /tmp/x <<'EOF'\nsafe payload\nEOF\necho ===; ls"
	if msg := lintBashCommand(cmd); !strings.Contains(msg, "equals-expanded") {
		t.Fatalf("shell after heredoc not linted: %q", msg)
	}
}

// TestBashLintStdin pins the hook protocol: only a well-formed Bash
// payload with a fatal shape blocks; everything else allows silently.
func TestBashLintStdin(t *testing.T) {
	mk := func(tool, cmd string) string {
		b, _ := json.Marshal(map[string]any{
			"tool_name":  tool,
			"tool_input": map[string]any{"command": cmd},
		})
		return string(b)
	}
	if msg, block := BashLintStdin(strings.NewReader(mk("Bash", "echo ===; ls"))); !block || msg == "" {
		t.Fatalf("fatal Bash payload must block, got block=%v msg=%q", block, msg)
	}
	if _, block := BashLintStdin(strings.NewReader(mk("Bash", "ls -la"))); block {
		t.Fatal("clean Bash payload must allow")
	}
	if _, block := BashLintStdin(strings.NewReader(mk("Read", "echo ==="))); block {
		t.Fatal("non-Bash tool must allow")
	}
	if _, block := BashLintStdin(strings.NewReader("not json at all")); block {
		t.Fatal("malformed payload must allow")
	}
	if _, block := BashLintStdin(strings.NewReader(`{"tool_name":"Bash","tool_input":{}}`)); block {
		t.Fatal("empty command must allow")
	}
}

// TestMergeHookSettingsInstallsBashLint holds that a cell's settings get
// the PreToolUse bash-lint entry beside the PostToolUse pr-hook entry,
// idempotently, and that a repo's own PreToolUse groups survive the merge.
func TestMergeHookSettingsInstallsBashLint(t *testing.T) {
	out, err := mergeHookSettings(nil, "demo", "/usr/local/bin/kraken")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	hooks := parsed["hooks"].(map[string]any)
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok || !hasHookCommand(pre, "/usr/local/bin/kraken bash-lint") {
		t.Fatalf("PreToolUse bash-lint entry missing: %s", out)
	}
	// Idempotent: merging the rendered output again must not duplicate.
	again, err := mergeHookSettings(out, "demo", "/usr/local/bin/kraken")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(again), "bash-lint") != strings.Count(string(out), "bash-lint") {
		t.Fatalf("second merge duplicated the bash-lint entry:\n%s", again)
	}
	// A non-ARRAY hooks[kind] is left exactly as the repo wrote it, never
	// replaced — ensureHookGroup's early return. Untested, this branch reads
	// as dead weight in a dedup refactor and gets "simplified" back into the
	// clobber it replaced.
	odd := []byte(`{"hooks":{"PreToolUse":"nonsense"}}`)
	kept, err := mergeHookSettings(odd, "demo", "/usr/local/bin/kraken")
	if err != nil {
		t.Fatal(err)
	}
	var oddParsed map[string]any
	if err := json.Unmarshal(kept, &oddParsed); err != nil {
		t.Fatal(err)
	}
	if got := oddParsed["hooks"].(map[string]any)["PreToolUse"]; got != "nonsense" {
		t.Fatalf("non-array PreToolUse must survive untouched, got %#v", got)
	}

	// A repo's own PreToolUse group survives.
	existing := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./guard.sh"}]}]}}`)
	merged, err := mergeHookSettings(existing, "demo", "/usr/local/bin/kraken")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "./guard.sh") || !strings.Contains(string(merged), "bash-lint") {
		t.Fatalf("merge dropped an entry:\n%s", merged)
	}
}

// TestQuotedMaskMirrorsStripQuotedAndCond pins the invariant quotedMask's
// own comment asserts: it answers the same question stripQuotedAndCond
// answers inline, and the two must not drift. Nothing else enforces that —
// quotedMask is reached only through lintBashCommand, where a divergence
// surfaces as a mysteriously missed block rather than as a failure naming
// the cause. CLAUDE.md records the expensive version of this exact shape:
// the per-job token rule was written out twice, once per reader, "so the two
// agreed with each other and both disagreed with the transcripts". A mirror
// test is also what makes collapsing the two into one implementation a safe
// refactor later rather than a risky one.
func TestQuotedMaskMirrorsStripQuotedAndCond(t *testing.T) {
	for _, cmd := range []string{
		`echo "a'b"`,
		`echo 'say "hi"'`,
		`echo 'unterminated`,
		`echo "esc \" inside" tail`,
		`echo a\'b`,
		`echo ''`,
		`echo ""`,
		`printf '%s' x`,
		`grep -rn "needle" . && echo '===' ; ls`,
		`echo trailing backslash \`,
		`echo "abc\`,
		`plain command with no quotes at all`,
	} {
		t.Run(cmd, func(t *testing.T) {
			b := []byte(cmd)
			mask := quotedMask(b)
			// stripQuotedAndCond blanks exactly the spans it considers
			// quoted; on input carrying no [[ ]] span, a byte differs from
			// the original iff it was inside one.
			stripped := stripQuotedAndCond(cmd)
			if len(stripped) != len(cmd) {
				t.Fatalf("strip changed length: %d != %d", len(stripped), len(cmd))
			}
			for i := range b {
				blanked := stripped[i] != cmd[i]
				// a space or newline inside a span is blanked to itself, so
				// only disagreement in the other direction is a real drift
				if mask[i] && !blanked && cmd[i] != ' ' && cmd[i] != '\n' {
					t.Errorf("byte %d (%q): quotedMask says quoted, stripQuotedAndCond kept it\n  %s\n  %s", i, cmd[i], cmd, stripped)
				}
				if blanked && !mask[i] {
					t.Errorf("byte %d (%q): stripQuotedAndCond blanked it, quotedMask says unquoted\n  %s\n  %s", i, cmd[i], cmd, stripped)
				}
			}
		})
	}
}
