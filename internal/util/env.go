package util

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvFilePath is kraken's own settings file, ~/.kraken/env: KEY=VALUE lines
// read by every kraken process at startup (ApplyEnvFile). It exists because
// a launchd job's environment is whatever its plist declares, so a knob set
// in a login shell never reaches `kraken serve`; one file both sides read
// does.
func EnvFilePath() string {
	return filepath.Join(Home(), ".kraken", "env")
}

// envFileAllowed reports whether key may be set from the env file: kraken's
// own knobs, the two account-pool roots, and PATH. Anything else is ignored,
// so the file can never smuggle in an arbitrary variable (a credential, a
// DYLD_* or a library path) that every kraken child would then inherit.
func envFileAllowed(key string) bool {
	if strings.HasPrefix(key, "KRAKEN_") {
		return true
	}
	switch key {
	case "CLAUDE_POOL_DIR", "CODEX_POOL_DIR", "PATH":
		return true
	}
	return false
}

// envFileMaxLine bounds one line of the env file. A line longer than this
// stops the read (logged with its line number) rather than being skipped.
const envFileMaxLine = 1 << 20

// ApplyEnvFile reads path as KEY=VALUE lines and sets each allowed key that
// the process environment does not already carry, returning the keys it set
// in file order.
//
// The file is parsed, never sourced. A leading UTF-8 BOM is ignored. Blank
// lines and lines whose first non-space byte is '#' are skipped, and so is a
// line with no '='. KEY is trimmed; VALUE is everything after the first '='
// trimmed of surrounding whitespace, with quotes kept as written (a value
// wrapped in matching quotes is still applied as-is, and one log line names
// the KEY, never the value). `$VAR` is not expanded: the one substitution is
// a leading `~` or `~/`, which becomes Home(). The first occurrence of a key
// in the file wins over later ones.
//
// A key already present in the environment wins, even when its value is
// empty. PATH is the one exception: empty elements are dropped from the
// file's value, which is then PREPENDED to the inherited PATH as
// `<file value>:<inherited>` (or set as given when no, or an empty, PATH is
// inherited) and listed in applied — unless the inherited PATH already
// starts with it, in which case nothing changes and PATH is not listed.
//
// A missing or unreadable file sets nothing and returns nil. A line too long
// to read stops the read at that line with one log line naming its number;
// the keys applied before it stay applied. A relative KRAKEN_REPOS_DIR is
// applied but logged, since the repos resolver ignores it.
func ApplyEnvFile(path string) (applied []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), envFileMaxLine)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, string(rune(0xFEFF)))
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || !envFileAllowed(key) || seen[key] {
			continue
		}
		seen[key] = true
		value = strings.TrimSpace(value)
		if value == "~" || strings.HasPrefix(value, "~/") {
			value = Home() + value[1:]
		}
		if key == "PATH" {
			// PATH alone is prepended rather than yielding: the serve plist
			// the Makefile writes declares a PATH, so a presence rule would
			// make the file's PATH dead on exactly the process it exists for.
			// An empty element would put the current directory on PATH.
			value = joinNonEmptyPath(value)
			if value == "" {
				continue
			}
			if inherited := os.Getenv("PATH"); inherited != "" {
				if inherited == value || strings.HasPrefix(inherited, value+":") {
					continue
				}
				value = value + ":" + inherited
			}
		} else if _, present := os.LookupEnv(key); present {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			continue
		}
		applied = append(applied, key)
		if isQuoteWrapped(value) {
			LogEvent("env", "warn", "env-file-quoted", fmt.Sprintf("%s: %s is wrapped in quotes; the quotes were kept as part of the value", path, key))
		}
		if key == "KRAKEN_REPOS_DIR" && value != "" && !filepath.IsAbs(value) {
			LogEvent("env", "warn", "env-file-relative", fmt.Sprintf("%s: %s is a relative path; kraken ignores it and uses the default repos dir", path, key))
		}
	}
	if err := sc.Err(); err != nil {
		LogEvent("env", "warn", "env-file-unreadable", fmt.Sprintf("%s: line %d could not be read (%v); stopped reading there", path, lineNo+1, err))
	}
	return applied
}

// joinNonEmptyPath drops empty elements from a colon-separated PATH value.
func joinNonEmptyPath(v string) string {
	var parts []string
	for _, p := range strings.Split(v, ":") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ":")
}

// isQuoteWrapped reports whether v starts and ends with the same quote
// character (' or ") and is at least two bytes long.
func isQuoteWrapped(v string) bool {
	if len(v) < 2 {
		return false
	}
	q := v[0]
	return (q == '"' || q == '\'') && v[len(v)-1] == q
}
