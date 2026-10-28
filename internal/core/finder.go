package core

import (
	"fmt"
	"os/exec"
	"strings"
)

// finder.go — the Finder half of the "open the thing you clicked" family
// beside zed.go: a live cell's artifact resolves to an absolute file, and
// this selects it in a Finder window.

// openCLI is macOS' own launcher, addressed absolutely for zedCLI's reason:
// `kraken serve` runs under launchd's PATH, and /usr/bin is the one place
// `open` always lives.
const openCLI = "/usr/bin/open"

// finderArgs builds the argv that reveals abs in Finder, split out pure so a
// test can pin the shape without execing. `-R` selects the file in its
// enclosing folder rather than opening it in its default app.
func finderArgs(abs string) []string {
	return []string{openCLI, "-R", abs}
}

// RevealInFinder selects abs in a Finder window (opening one on its folder
// when none shows it). `open` hands the request to Finder and exits, so no
// timeout is needed, exactly as OpenInZed.
func RevealInFinder(abs string) error {
	args := finderArgs(abs)
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}
