package core

import (
	"strings"
	"testing"
)

// zedargs_edge_test.go — the EDGE dimension of slice S's pure argv builder
// (artifacts/50-contract.md, the `exec` row). The contract states the suffix
// rule as two guarded clauses — `:line` "when line > 0" and `:line:col` "when
// both > 0" — so the interesting cases are the ones that fall OUTSIDE both
// guards and must degrade to a bare path rather than emitting `:0` or a
// column with nothing to hang it on.
//
// This file execs nothing and stats nothing: zedArgs is pure, and the CLI
// path it names is a string here, never a process.

const edgeZedCLI = "/Applications/Zed.app/Contents/MacOS/cli"

func edgeZedWant(target string) []string {
	return []string{edgeZedCLI, "-e", target}
}

func edgeZedCheck(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: zedArgs = %q, want %q", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: zedArgs = %q, want %q", what, got, want)
		}
	}
}

// TestEdgeZedArgsLineZeroIsABarePath: a file-level ref (the G7 grammar row,
// the most common code-map cell) carries no line at all. Appending ":0" would
// send Zed to a line that does not exist.
func TestEdgeZedArgsLineZeroIsABarePath(t *testing.T) {
	abs := "/Users/carl/Developer/kraken/web/serve.go"
	edgeZedCheck(t, "line 0", zedArgs(abs, 0, 0), edgeZedWant(abs))
}

// TestEdgeZedArgsColWithoutLineIsABarePath: a column is only meaningful
// against a line — the contract's suffix guard is "when BOTH > 0" — so a
// column arriving without one is dropped along with it, not promoted.
func TestEdgeZedArgsColWithoutLineIsABarePath(t *testing.T) {
	abs := "/Users/carl/Developer/kraken/web/serve.go"
	got := zedArgs(abs, 0, 25)
	edgeZedCheck(t, "col without line", got, edgeZedWant(abs))
	if strings.Contains(got[len(got)-1], "25") {
		t.Errorf("zedArgs = %q — a column with no line must not reach the target", got)
	}
}

// TestEdgeZedArgsNegativePositionsAreABarePath: neither guard admits a
// negative, and a negative can arrive — the endpoint refuses one, but zedArgs
// is a package-level pure function with its own callers.
func TestEdgeZedArgsNegativePositionsAreABarePath(t *testing.T) {
	abs := "/Users/carl/Developer/kraken/web/serve.go"
	edgeZedCheck(t, "line -1", zedArgs(abs, -1, 0), edgeZedWant(abs))
	edgeZedCheck(t, "line -1 col -1", zedArgs(abs, -1, -1), edgeZedWant(abs))
	edgeZedCheck(t, "line 37 col -1", zedArgs(abs, 37, -1), edgeZedWant(abs+":37"))
}

// TestEdgeZedArgsLineWithoutColStopsAtTheLine is the boundary between the two
// guarded clauses: line > 0 and col == 0 takes the FIRST clause only.
func TestEdgeZedArgsLineWithoutColStopsAtTheLine(t *testing.T) {
	abs := "/Users/carl/Developer/kraken/web/serve.go"
	edgeZedCheck(t, "line 37, col 0", zedArgs(abs, 37, 0), edgeZedWant(abs+":37"))
}

// TestEdgeZedArgsNeverRewritesTheAbsolutePath: the path is passed through
// verbatim, including a name with spaces or a colon in it — quoting is exec's
// problem, and a builder that "helpfully" escaped here would double-escape.
func TestEdgeZedArgsNeverRewritesTheAbsolutePath(t *testing.T) {
	for _, abs := range []string{
		"/Users/carl/Developer/my repo/web/serve.go",
		"/Users/carl/Developer/kraken/web/_ui/src/lib/markdown.ts",
	} {
		got := zedArgs(abs, 0, 0)
		edgeZedCheck(t, "verbatim "+abs, got, edgeZedWant(abs))
	}
}
