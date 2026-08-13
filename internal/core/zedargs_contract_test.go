package core

// zedargs_contract_test.go — blind CONTRACT test for the Zed launcher's argv
// builder (50-contract.md, Slice S, C8).
//
// zedArgs lives in internal/core, so it cannot be pinned from the api package's
// test file the way the endpoint's seams are — hence this second file. It is
// the whole test: zedArgs is pure, and everything around it (the os.Stat guard,
// the exec) is behind the `var` seam the api tests swap. Nothing here execs and
// nothing here touches /Applications.
//
// Every package-level identifier is prefixed `contractZed` so the edge author
// sharing this package cannot collide with it.

import (
	"strings"
	"testing"
)

// contractZedCLI is the absolute path the contract pins. Zed's CLI is NOT on
// PATH and never will be for a launchd LaunchAgent, so this is a literal rather
// than a lookup — pinning it here is what stops a well-meaning refactor to
// exec.LookPath("zed") from shipping a launcher that only works from a
// terminal.
const contractZedCLI = "/Applications/Zed.app/Contents/MacOS/cli"

func TestContractZedArgsShapes(t *testing.T) {
	const abs = "/Users/carl/.kraken/cells/demo-2/wt-kraken/internal/api/board.go"

	cases := []struct {
		name      string
		line, col int
		want      []string
	}{
		{
			// file-level (G7): no position suffix at all — a bare ":0" would
			// send Zed to a line that does not exist.
			name: "no line, no col",
			want: []string{contractZedCLI, "-e", abs},
		},
		{
			name: "line only",
			line: 37,
			want: []string{contractZedCLI, "-e", abs + ":37"},
		},
		{
			name: "line and col",
			line: 794, col: 25,
			want: []string{contractZedCLI, "-e", abs + ":794:25"},
		},
		{
			// col without a line has no meaning to append to: the contract
			// appends ":line" only when line > 0, so this degrades to the
			// file-level form rather than emitting a bare ":25".
			name: "col without a line",
			col:  25,
			want: []string{contractZedCLI, "-e", abs},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := zedArgs(abs, c.line, c.col)
			if len(got) != len(c.want) {
				t.Fatalf("zedArgs(%q, %d, %d) = %v, want %v", abs, c.line, c.col, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("zedArgs(%q, %d, %d) = %v, want %v", abs, c.line, c.col, got, c.want)
				}
			}
		})
	}
}

// TestContractZedArgsNeverInventsAFlag guards the shape rather than the string:
// the argv is exactly three elements, the CLI path is absolute, and `-e` sits
// between it and the target. A fourth element (a --wait, a --new) would change
// what a click does without any test above noticing.
func TestContractZedArgsNeverInventsAFlag(t *testing.T) {
	got := zedArgs("/tmp/x/y.go", 12, 3)
	if len(got) != 3 {
		t.Fatalf("zedArgs returned %d args (%v), want exactly 3", len(got), got)
	}
	if !strings.HasPrefix(got[0], "/") {
		t.Errorf("argv[0] = %q, want an absolute path (Zed's CLI is not on PATH)", got[0])
	}
	if got[1] != "-e" {
		t.Errorf("argv[1] = %q, want %q", got[1], "-e")
	}
}
