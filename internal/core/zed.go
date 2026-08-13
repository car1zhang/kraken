package core

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// zed.go — the editor half of the "open the thing you clicked" family that
// OpenPeer (actions.go) already covers for tmux panes: a code ref in an
// artifact resolves to an absolute file, and this is what puts Zed on it.
// Same shape as the Ghostty raise next door — a pure argv builder a test can
// pin, plus one exec that captures both streams into its error.

// zedCLI is Zed's own command-line launcher, inside the app bundle. It is
// addressed absolutely on purpose: the `zed` shim users install lands in
// ~/.local/bin, which is NOT on the PATH `kraken serve` inherits from
// launchd, so a PATH lookup here would fail on exactly the deployment that
// matters. The stat guard below turns "the bundle isn't there" into a
// message naming this path instead of an exec error naming nothing.
const zedCLI = "/Applications/Zed.app/Contents/MacOS/cli"

// zedArgs builds the argv that opens abs in Zed INSIDE a workspace rooted at
// root — split out pure so a test can pin the shape without execing. Zed takes
// the position as a suffix on the path itself (path:line:col), so a zero line
// means the bare path and a col without a line is dropped: there is no way to
// spell "column 12 of no particular line", and inventing one would send Zed a
// path it cannot open.
//
// The root rides as its own leading path argument, and there is deliberately
// no -e/-n flag: passed a directory plus a file within it, Zed anchors the
// file to the workspace whose root is that directory — reusing the window
// that already has it open, else creating a new one rooted there (carl's
// window-per-working-dir rule). The bare `-e <file>` form this replaces
// dropped the file into WHICHEVER window was frontmost, which put a
// worktree-A file in a worktree-B project. Verified against Zed 1.15 on this
// machine: two invocations against one root left one workspace row whose path
// set is the root alone (`workspaces` is uniquely indexed on the path set, so
// this pins the anchoring, not the window count) — and window reuse is the
// flagless default, which is what `-n, --new  Create a new workspace` exists
// to override.
func zedArgs(root, abs string, line, col int) []string {
	target := abs
	if line > 0 {
		target += ":" + strconv.Itoa(line)
		if col > 0 {
			target += ":" + strconv.Itoa(col)
		}
	}
	return []string{zedCLI, root, target}
}

// OpenInZed opens abs in Zed at the given position, in a window rooted at
// root (see zedArgs). The CLI hands the paths to the running app (or launches
// it) and exits, which is why there is no context timeout here, unlike the
// tmux calls in actions.go that can block on a wedged server.
//
// The os.Stat guard is the lookToolchain convention (jobs/update.go): a
// missing CLI is the one failure a user can actually fix, and the error has
// to name the path that was checked for that to be true.
func OpenInZed(root, abs string, line, col int) error {
	args := zedArgs(root, abs, line, col)
	if fi, err := os.Stat(args[0]); err != nil || fi.IsDir() {
		return fmt.Errorf("zed cli not found — checked %s", args[0])
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}
