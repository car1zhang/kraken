package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintBashWritesBlocks pins the Bash-route write shapes bash-lint
// blocks when their target resolves inside the worktree root: redirects,
// `sed -i`, `perl -pi`, `tee`, through relative and absolute paths and
// through a `cd` into a subdirectory.
func TestLintBashWritesBlocks(t *testing.T) {
	root := "/w/cell/wt-repo"
	cases := []struct{ name, cmd, want string }{
		{"heredoc into a repo file", "cat > internal/x.go <<'EOF'\npackage x\nEOF", "> internal/x.go"},
		{"append to a repo file", `echo "- pin" >> tests/test_pins.py`, "> tests/test_pins.py"},
		{"absolute path under the root", `cat > /w/cell/wt-repo/README.md <<'EOF'` + "\nhi\nEOF", "> /w/cell/wt-repo/README.md"},
		{"stderr into the worktree", `go test ./... 2>err.log`, "> err.log"},
		{"redirect after a cd into a subdir", `cd internal && cat > x.go <<'EOF'` + "\nx\nEOF", "> x.go"},
		{"redirect after a cd back out and in", `cd /tmp; cd /w/cell/wt-repo/cmd && echo x > y.go`, "> y.go"},
		{"sed in-place quoted script", `sed -i '' 's/a/b/' cmd/x.go`, "sed -i cmd/x.go"},
		{"sed in-place with suffix and -e", `sed -i.bak -e 's/a/b/' x.go`, "sed -i x.go"},
		{"sed in-place unquoted script", `sed -i s/a/b/ x.go`, "sed -i x.go"},
		{"sed long in-place flag", `sed --in-place 's/a/b/' x.go`, "sed -i x.go"},
		{"perl in-place", `perl -pi -e 's/a/b/' lib/x.pm`, "perl -pi lib/x.pm"},
		{"perl in-place split flags", `perl -i -p -e 's/a/b/' x.txt`, "perl -pi x.txt"},
		{"tee into the worktree", `go test ./... | tee out.log`, "tee out.log"},
		{"tee append", `make 2>&1 | tee -a build.log`, "tee build.log"},
		{"tilde path under the root", `echo x > ~/cell/wt-repo/a.txt`, "> ~/cell/wt-repo/a.txt"},
		{"sed in-place with a trailing redirect", `sed -i '' 's/a/b/' cmd/x.go 2>/dev/null`, "sed -i cmd/x.go"},
		{"sed in-place unquoted script into the root", `sed -i s/foo/bar/g internal/x.go 2>/dev/null`, "sed -i internal/x.go"},
		{"sed long in-place with suffix", `sed --in-place=.bak s/a/b/ x.go`, "sed -i x.go"},
		{"tee then dev null", `go test ./... | tee out.log > /dev/null`, "tee out.log"},
		{"continued line", "sed -i '' 's/a/b/' \\\n  cmd/x.go", "sed -i cmd/x.go"},
		{"perl -I then -pi", `perl -Ilib -pi -e 's/a/b/' x.txt`, "perl -pi x.txt"},
		{"perl -pie cluster", `perl -pie 's/a/b/' x.txt`, "perl -pi x.txt"},
		{"pushd into a subdir", `pushd internal && echo x > a.go`, "> a.go"},
		{"builtin cd into a subdir", `builtin cd internal && echo x > a.go`, "> a.go"},
		{"brace group cd runs in the caller's shell", `cd /tmp/s; { cd /w/cell/wt-repo; }; echo x > n.md`, "> n.md"},
		{"sed behind then", `if true; then sed -i '' 's/a/b/' x.go; fi`, "sed -i x.go"},
		{"brace group redirect into the root", `{ echo a; echo b; } >> internal/x.go`, "> internal/x.go"},
		{"multi-line brace group redirect into the root", "{\n  echo a\n  echo b\n} > internal/x.go", "> internal/x.go"},
		{"fd dup before the operand", `sed -i 's/a/b/' 2>&1 x.go`, "sed -i x.go"},
		{"redirect to the root dir itself", `cat > /w/cell/wt-repo/notes <<'EOF'` + "\nx\nEOF", "> /w/cell/wt-repo/notes"},
	}
	t.Setenv("HOME", "/w")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := lintBashCommandAt(c.cmd, root, root)
			if msg == "" {
				t.Fatalf("want block for %q, got clean", c.cmd)
			}
			if !strings.Contains(msg, "`"+c.want+"`") {
				t.Fatalf("block message %q does not name %q", msg, c.want)
			}
			if !strings.Contains(msg, "Edit tool") {
				t.Fatalf("block message must name the Edit tool as the fix: %q", msg)
			}
		})
	}
}

// TestLintBashWritesAllows pins the fail-open fences: scratch targets,
// /dev/null and fd duplication, quoted or variable targets, unknown cwd
// after `cd -`/`cd $X`, non-writing sed/perl, operators that are not
// redirects, writes named only in data, and a disabled check (no root).
func TestLintBashWritesAllows(t *testing.T) {
	root := "/w/cell/wt-repo"
	t.Setenv("HOME", "/w")
	cases := []struct{ name, cmd string }{
		{"heredoc into scratch", "cat > /tmp/dr/notes.md <<'EOF'\nfindings\nEOF"},
		{"append to scratch", `echo line >> /private/tmp/x/notes.md`},
		{"cd to scratch then relative", `cd /tmp/scratch && cat > notes.md <<'EOF'` + "\nx\nEOF"},
		{"cd to scratch then deeper", `cd /tmp; cd sub; echo x > a.txt`},
		{"subshell cd to scratch", `(cd /tmp && echo x > a.txt)`},
		{"dev null", `go vet ./... > /dev/null 2>&1`},
		{"fd duplication", `echo warn >&2; ls 2>&1 | head`},
		{"quoted target stands down", `cat > "$out" <<'EOF'` + "\nx\nEOF"},
		{"variable target stands down", `go test ./... > $LOG`},
		{"glob target stands down", `cat a > out-*.txt`},
		{"cd dash makes cwd unknown", `cd - && echo x > a.go`},
		{"cd var makes cwd unknown", `cd $DIR && echo x > a.go`},
		{"cd tilde user unknown", `cd ~someone && echo x > a.go`},
		{"sed without -i", `sed -n '1,5p' x.go`},
		{"sed -i on scratch", `sed -i '' 's/a/b/' /tmp/x/notes.md`},
		{"sed -n -e no in-place", `sed -n -e 's/a/b/p' x.go`},
		{"perl without -i", `perl -ne 'print if /x/' x.go`},
		{"perl -pi on scratch", `perl -pi -e 's/a/b/' /tmp/x.txt`},
		{"tee to scratch", `go test ./... | tee /tmp/out.log`},
		{"tee to dev tty", `make | tee /dev/tty`},
		{"process substitution target", `diff <(ls a) >(cat)`},
		{"double-bracket comparison", `[[ $a > $b ]] && echo gt`},
		{"arithmetic comparison", `(( a > b )) && echo gt`},
		{"quoted redirect text", `echo "run: cat > x.go later"`},
		{"heredoc body naming a write", "cat > /tmp/r.md <<'EOF'\nthe agent ran sed -i '' 's/a/b/' cmd/x.go\nand cat > internal/x.go\nEOF"},
		{"comment naming a write", "# cat > x.go was the mistake\nls"},
		{"git show into scratch", `git show HEAD:internal/x.go > /tmp/x.go`},
		{"outside the root absolute", `echo x > /w/cell/artifacts/00-report.md`},
		{"sibling dir sharing a prefix", `echo x > /w/cell/wt-repo-two/a.txt`},
		{"herestring", `grep x <<< "$y"`},
		{"ordinary build", `go build -o /tmp/kraken-test ./cmd/kraken`},
		{"sed unquoted script on scratch", `sed -i '' s/a/b/ /tmp/x.md`},
		{"sed -e unquoted script on scratch", `sed -i -e s/a/b/ /tmp/x.md`},
		{"sed script file is read, not written", `sed -i -f fix.sed /tmp/notes.md`},
		{"tee to scratch with stderr dropped", `go test ./... | tee /tmp/out.log 2>/dev/null`},
		{"tee to scratch then dev null", `tee /tmp/a > /dev/null`},
		{"tee to scratch with fd dup", `echo x | tee -a /tmp/a 1>&2`},
		{"tee with process substitution", `echo x | tee >(cat) /tmp/a`},
		{"perl module flag is not in-place", `perl -Mstrict -ne 'print' x.go`},
		{"perl module with argument", `perl -MList::Util=sum -lane 'print sum @F' data.txt`},
		{"perl include flag is not in-place", `perl -Ilib -ne 'print' x.go`},
		{"perl flag after the operand is argv", `perl -ne 'print' tools/find.pl -fix`},
		{"sed -e cluster is not in-place", `sed -es/i/j/ x.go`},
		{"continued line to scratch", "sed -i '' 's/a/b/' \\\n  /tmp/x.md"},
		{"pushd to scratch", `pushd /tmp && echo x > a.txt && popd`},
		{"popd makes cwd unknown", `pushd /tmp; popd; echo x > a.go`},
		{"builtin cd to scratch", `builtin cd /tmp && echo x > a.txt`},
		{"subshell cd into the root does not leak", `cd /tmp/s && (cd /w/cell/wt-repo && git status) && echo x > notes.md`},
		{"command substitution cd into the root does not leak", `cd /tmp/s && d=$(cd /w/cell/wt-repo && pwd) && echo x > notes.md`},
		{"later cd inside a subshell does not leak", `cd /tmp/s && (git fetch; cd /w/cell/wt-repo) && echo x > notes.md`},
		{"later cd inside a substitution does not leak", `cd /tmp/s && d=$(true; cd /w/cell/wt-repo; pwd) && echo x > notes.md`},
		{"cd inside backticks does not leak", "cd /tmp/s && d=`cd /w/cell/wt-repo && pwd` && echo x > notes.md"},
		{"cd behind if", `if cd /tmp/s; then echo x > notes.md; fi`},
		{"cd behind do", `for f in a; do cd /tmp/s; echo x > notes.md; done`},
		{"cd behind a brace", `{ cd /tmp && cat > notes.md; }`},
		{"cd behind bang", `! cd /tmp/s || echo x > notes.md`},
		{"cd behind time", `time cd /tmp/s; echo x > notes.md`},
		{"later cd inside a subshell to scratch", `cd /tmp/s && (cd /tmp/t; cd /w/cell/wt-repo) && echo x > notes.md`},
		{"cd inside a function definition does not run", `cd /tmp/s; f() { cd /w/cell/wt-repo; }; echo x > n.md`},
		{"cd inside a function-keyword definition does not run", "cd /tmp/s; function f {\n  cd /w/cell/wt-repo\n}\necho x > n.md"},
		{"later cd inside a one-line function-keyword body", `cd /tmp/s; function f { true; cd /w/cell/wt-repo; }; echo x > n.md`},
		{"brace group redirect to scratch", `{ echo a; echo b; } > /tmp/out.log`},
		{"underscore-delimited sed script on scratch", `sed -i s_a_b_ /tmp/x`},
		{"quoted cd argument stands down", `cd "$WT" && echo x > a.go`},
		{"escaped greater-than in test", `[ a \> b ] && echo gt`},
		{"unquoted arrow in a format string", `git log --format=%h->%s`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := lintBashCommandAt(c.cmd, root, root); msg != "" {
				t.Fatalf("want clean for %q, got block: %s", c.cmd, msg)
			}
		})
	}
	// No root (not inside a git checkout) disables the check entirely.
	if msg := lintBashCommandAt(`cat > internal/x.go <<'EOF'`+"\nx\nEOF", "/w/cell/wt-repo", ""); msg != "" {
		t.Fatalf("no root must disable the write check, got: %s", msg)
	}
	if msg := lintBashCommand(`cat > internal/x.go <<'EOF'` + "\nx\nEOF"); msg != "" {
		t.Fatalf("the no-location form must not run the write check, got: %s", msg)
	}
}

// TestLintBashWritesOneMessagePerHit holds that an unquoted sed script is
// never named as a file beside the real target: exactly one message.
func TestLintBashWritesOneMessagePerHit(t *testing.T) {
	for _, cmd := range []string{`sed -i s/a/b/ x.go`, `sed -i -e s/a/b/ x.go`, `sed --in-place=.bak s/a/b/ x.go`} {
		msg := lintBashCommandAt(cmd, "/w/cell/wt-repo", "/w/cell/wt-repo")
		if n := strings.Count(msg, "kraken bash-lint:"); n != 1 || !strings.Contains(msg, "`sed -i x.go`") {
			t.Fatalf("%q: want exactly one message naming x.go, got %d: %q", cmd, n, msg)
		}
	}
}

// TestLintBashWritesCwdBelowRoot holds that a teammate started in a
// subpackage still resolves relative targets inside the worktree, and that
// the shape checks still run beside the write check (one message each).
func TestLintBashWritesCwdBelowRoot(t *testing.T) {
	root := "/w/cell/wt-repo"
	msg := lintBashCommandAt(`echo === ; cat > x.go <<'EOF'`+"\nx\nEOF", root+"/internal/jobs", root)
	if !strings.Contains(msg, "equals-expanded") || !strings.Contains(msg, "`> x.go`") {
		t.Fatalf("want both the equals block and the write block, got: %q", msg)
	}
	if n := strings.Count(msg, "kraken bash-lint:"); n != 2 {
		t.Fatalf("want exactly two messages, got %d: %q", n, msg)
	}
}

// TestBashLintStdinWriteCheck pins the hook protocol for the write check:
// the payload's cwd locates the worktree (a `.git` FILE, as in every cell
// checkout), a write inside it blocks, the same write under scratch allows,
// and a cwd with no `.git` above it allows.
func TestBashLintStdinWriteCheck(t *testing.T) {
	top := t.TempDir()
	wt := filepath.Join(top, "wt-repo")
	if err := os.MkdirAll(filepath.Join(wt, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(cwd, cmd string) string {
		b, _ := json.Marshal(map[string]any{
			"tool_name":  "Bash",
			"cwd":        cwd,
			"tool_input": map[string]any{"command": cmd},
		})
		return string(b)
	}
	if msg, block := BashLintStdin(strings.NewReader(mk(filepath.Join(wt, "internal"), "cat > x.go <<'EOF'\nx\nEOF"))); !block || !strings.Contains(msg, "`> x.go`") {
		t.Fatalf("write inside the worktree must block, got block=%v msg=%q", block, msg)
	}
	if _, block := BashLintStdin(strings.NewReader(mk(wt, "cat > "+filepath.Join(top, "notes.md")+" <<'EOF'\nx\nEOF"))); block {
		t.Fatal("write beside the worktree must allow")
	}
	if _, block := BashLintStdin(strings.NewReader(mk(top, "cat > x.go <<'EOF'\nx\nEOF"))); block {
		t.Fatal("a cwd with no .git above it must allow")
	}
}
