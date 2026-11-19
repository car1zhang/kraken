package util

import (
	"path/filepath"
	"strings"
)

// PrependToolDir returns path — a PATH value — with the directory holding
// toolPath at its front. It is the one implementation of kraken's answer to a
// PATH that does not lead to the toolchain, and every caller that has that
// problem has it for the same reason.
//
// `kraken serve` is a launchd agent, and launchd hands its jobs their own PATH
// rather than a login shell's. On this machine the only `go` lives in
// /usr/local/go/bin, which that PATH need not contain, so anything invoking a
// BARE `go` — `make build` during a board-click update, or an agent typing
// `go test` inside a cell — dies with `go: command not found` while the
// identical command typed in a terminal works, which makes the bug look like
// it isn't real.
//
// Widening the plist's PATH is NOT sufficient on its own: `launchctl kickstart
// -k` — how every install restarts the daemon — relaunches from launchd's
// cached in-memory job definition and never re-reads the plist, so a PATH edit
// there only lands after a bootout/bootstrap or a logout. Resolving the tool to
// an absolute path and exporting its directory is what actually works, no
// matter what launchd is holding.
//
// Prepended, never replaced: everything else the caller shells out to (make,
// git, security, codesign, launchctl, gh, claude) still has to be findable, and
// an operator-set PATH is a preference, not an obstacle. A toolPath with no
// meaningful directory ("go", "", "/") leaves path alone rather than putting
// "." on it, and a dir that already leads path is not repeated.
func PrependToolDir(path, toolPath string) string {
	dir := filepath.Dir(toolPath)
	if toolPath == "" || dir == "" || dir == "." || dir == string(filepath.Separator) {
		return path
	}
	if path == "" {
		return dir
	}
	if first, _, _ := strings.Cut(path, ":"); first == dir {
		return path
	}
	return dir + ":" + path
}

// AppendToolDir returns path with the directory holding toolPath at its END,
// for a tool whose directory is a general bin dir. Prepending such a directory
// would let anything else installed in it shadow a name the caller already
// resolved; appended, it supplies only names nothing earlier on path has. A
// directory already anywhere on path is not repeated, and a toolPath with no
// meaningful directory leaves path alone, as in PrependToolDir.
func AppendToolDir(path, toolPath string) string {
	dir := filepath.Dir(toolPath)
	if toolPath == "" || dir == "" || dir == "." || dir == string(filepath.Separator) {
		return path
	}
	if path == "" {
		return dir
	}
	for _, d := range strings.Split(path, ":") {
		if d == dir {
			return path
		}
	}
	return path + ":" + dir
}

// ToolchainEnvAppend is ToolchainEnv with AppendToolDir in place of
// PrependToolDir: env copied, its PATH entry extended at the end.
func ToolchainEnvAppend(env []string, toolPath string) []string {
	out := append([]string(nil), env...)
	for i, kv := range out {
		if strings.HasPrefix(kv, "PATH=") {
			out[i] = "PATH=" + AppendToolDir(strings.TrimPrefix(kv, "PATH="), toolPath)
			return out
		}
	}
	if p := AppendToolDir("", toolPath); p != "" {
		out = append(out, "PATH="+p)
	}
	return out
}

// ToolchainEnv is env (normally os.Environ()) with PrependToolDir applied to
// its PATH entry, for handing to a subprocess. env is copied, so the caller's
// slice is never written through; an env with no PATH at all gains one.
func ToolchainEnv(env []string, toolPath string) []string {
	out := append([]string(nil), env...)
	for i, kv := range out {
		if strings.HasPrefix(kv, "PATH=") {
			out[i] = "PATH=" + PrependToolDir(strings.TrimPrefix(kv, "PATH="), toolPath)
			return out
		}
	}
	if p := PrependToolDir("", toolPath); p != "" {
		out = append(out, "PATH="+p)
	}
	return out
}
