package jobs

// Blind logic tests for slice B of the dream 2026-09-28 contract: the cell
// bash-lint blocks bash's `PIPESTATUS` array, which zsh expands to nothing.
// Authored from the contract text alone (artifacts/50-contract.md), before
// the implementation existed.

import (
	"strings"
	"testing"
)

const contractPipestatusPrefix = "kraken bash-lint: "

func contractPipestatusLines(msg string) []string {
	if msg == "" {
		return nil
	}
	return strings.Split(msg, "\n")
}

// TestContractPipestatusBlocksUnquotedExpansion pins B1.
func TestContractPipestatusBlocksUnquotedExpansion(t *testing.T) {
	for _, cmd := range []string{
		`go test ./... | tee /tmp/out.log; echo $PIPESTATUS`,
		`make build | tee /tmp/b.log; echo ${PIPESTATUS[0]}`,
		`make build | tee /tmp/b.log; rc=${PIPESTATUS[0]}; echo $rc`,
	} {
		msg := lintBashCommand(cmd)
		if msg == "" {
			t.Errorf("want block for %q, got clean", cmd)
			continue
		}
		if !strings.Contains(msg, "pipestatus") {
			t.Errorf("block for %q does not name pipestatus: %q", cmd, msg)
		}
	}
}

// TestContractPipestatusBlocksDoubleQuotedExpansion pins B2.
func TestContractPipestatusBlocksDoubleQuotedExpansion(t *testing.T) {
	for _, cmd := range []string{
		`make build | tee /tmp/b.log; echo "exit: ${PIPESTATUS[0]}"`,
		`go vet ./... | head -5; echo "status $PIPESTATUS"`,
	} {
		msg := lintBashCommand(cmd)
		if msg == "" {
			t.Errorf("want block for %q, got clean", cmd)
			continue
		}
		if !strings.Contains(msg, "pipestatus") {
			t.Errorf("block for %q does not name pipestatus: %q", cmd, msg)
		}
	}
}

// TestContractPipestatusAllowsNonExpansions pins B3 through B9: text that is
// not a live expansion of bash's array stays clean.
func TestContractPipestatusAllowsNonExpansions(t *testing.T) {
	cases := []struct{ obligation, name, cmd string }{
		{"B3", "single-quoted", `echo 'bash uses ${PIPESTATUS[0]} here'`},
		{"B3", "single-quoted dollar form", `echo 'see $PIPESTATUS'`},
		{"B4", "heredoc body", "cat > /tmp/note.md <<'EOF'\nbash reads ${PIPESTATUS[0]} and $PIPESTATUS\nEOF\necho done"},
		{"B5", "comment line", "# bash would use ${PIPESTATUS[0]}\nls -la"},
		{"B5", "trailing comment", `ls -la  # not $PIPESTATUS`},
		{"B6", "bare word", `grep -rn PIPESTATUS internal/`},
		{"B7", "escaped dollar unquoted", `echo \$PIPESTATUS`},
		{"B7", "escaped dollar in double quotes", `echo "\${PIPESTATUS[0]}"`},
		{"B8", "zsh dollar form", `make build | tee /tmp/b.log; echo $pipestatus`},
		{"B8", "zsh indexed form", `make build | tee /tmp/b.log; echo ${pipestatus[1]}`},
		{"B9", "longer name", `echo $PIPESTATUS_LOG`},
		{"B9", "longer name braced", `echo ${PIPESTATUS_LOG}`},
	}
	for _, c := range cases {
		t.Run(c.obligation+" "+c.name, func(t *testing.T) {
			if msg := lintBashCommand(c.cmd); msg != "" {
				t.Fatalf("want clean for %q, got block: %s", c.cmd, msg)
			}
		})
	}
}

// TestContractPipestatusMessageNamesTheZshArray pins B10.
func TestContractPipestatusMessageNamesTheZshArray(t *testing.T) {
	msg := lintBashCommand(`make build | tee /tmp/b.log; echo ${PIPESTATUS[0]}`)
	if !strings.Contains(msg, "pipestatus") {
		t.Fatalf("message does not contain lowercase pipestatus: %q", msg)
	}
	if !strings.Contains(msg, "one-indexed") {
		t.Fatalf("message does not say the zsh array is one-indexed: %q", msg)
	}
}

// TestContractPipestatusReportsTheShapeOnce pins B11.
func TestContractPipestatusReportsTheShapeOnce(t *testing.T) {
	cmd := `a | b; echo $PIPESTATUS ${PIPESTATUS[0]} "${PIPESTATUS[1]}"; x=${PIPESTATUS[0]}; echo $x`
	lines := contractPipestatusLines(lintBashCommand(cmd))
	if len(lines) != 1 {
		t.Fatalf("want exactly one message line, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "pipestatus") {
		t.Fatalf("the one line is not this shape's message: %q", lines[0])
	}
}

// TestContractPipestatusCombinesWithAnotherShape pins B12 and B13.
func TestContractPipestatusCombinesWithAnotherShape(t *testing.T) {
	cmd := `make build | tee /tmp/b.log; echo ===; echo ${PIPESTATUS[0]}`
	lines := contractPipestatusLines(lintBashCommand(cmd))
	if len(lines) != 2 {
		t.Fatalf("want two message lines, got %d: %q", len(lines), lines)
	}
	var sawPipe, sawEquals bool
	for _, l := range lines {
		if !strings.HasPrefix(l, contractPipestatusPrefix) {
			t.Errorf("line does not begin %q: %q", contractPipestatusPrefix, l)
		}
		if strings.Contains(l, "pipestatus") {
			sawPipe = true
		}
		if strings.Contains(l, "equals-expanded") {
			sawEquals = true
		}
	}
	if !sawPipe || !sawEquals {
		t.Fatalf("want one pipestatus line and one equals-expanded line, got %q", lines)
	}
}

// TestContractPipestatusMessageLinePrefix pins B13 on this shape alone.
func TestContractPipestatusMessageLinePrefix(t *testing.T) {
	lines := contractPipestatusLines(lintBashCommand(`a | b; echo $PIPESTATUS`))
	if len(lines) == 0 {
		t.Fatal("want a block, got clean")
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, contractPipestatusPrefix) {
			t.Errorf("line does not begin %q: %q", contractPipestatusPrefix, l)
		}
	}
}
