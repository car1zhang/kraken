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

// zedArgs builds the argv that opens abs in Zed, at line:col when they are
// known — split out pure so a test can pin the shape without execing. Zed
// takes the position as a suffix on the path itself (path:line:col), so a
// zero line means the bare path and a col without a line is dropped: there is
// no way to spell "column 12 of no particular line", and inventing one would
// send Zed a path it cannot open.
func zedArgs(abs string, line, col int) []string {
	target := abs
	if line > 0 {
		target += ":" + strconv.Itoa(line)
		if col > 0 {
			target += ":" + strconv.Itoa(col)
		}
	}
	return []string{zedCLI, "-e", target}
}

// OpenInZed opens abs in Zed at the given position, creating no window of its
// own — the CLI hands the path to the running app (or launches it) and exits,
// which is why there is no context timeout here, unlike the tmux calls in
// actions.go that can block on a wedged server.
//
// The os.Stat guard is the lookToolchain convention (jobs/update.go): a
// missing CLI is the one failure a user can actually fix, and the error has
// to name the path that was checked for that to be true.
func OpenInZed(abs string, line, col int) error {
	args := zedArgs(abs, line, col)
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
