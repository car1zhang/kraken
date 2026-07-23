package util

import (
	"fmt"
	"os"
	"strings"
)

func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		Die("home dir: %v", err)
	}
	return h
}

func Die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "kraken: "+format+"\n", a...)
	os.Exit(1)
}

func Slugify(s string) string {
	var out strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			out.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

// AtomicWrite writes content to path via temp file + rename in the same dir,
// so a reader never observes a half-written file.
func AtomicWrite(path, content string) error {
	tmp := path + ".kraken.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
