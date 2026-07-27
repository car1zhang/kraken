package util

import (
	"os"
	"strings"
	"testing"
)

func TestPrependToolDir(t *testing.T) {
	cases := []struct {
		name, path, tool, want string
	}{
		{"prepends the resolved tool's dir", "/usr/bin:/bin", "/usr/local/go/bin/go", "/usr/local/go/bin:/usr/bin:/bin"},
		{"keeps the inherited entries", "/usr/bin:/bin", "/opt/homebrew/bin/xcodegen", "/opt/homebrew/bin:/usr/bin:/bin"},
		// An unresolved tool name has no directory to export; putting "." on
		// PATH would be both useless and a cwd-dependent hazard.
		{"bare tool name leaves PATH alone", "/usr/bin:/bin", "go", "/usr/bin:/bin"},
		{"empty tool leaves PATH alone", "/usr/bin:/bin", "", "/usr/bin:/bin"},
		{"root-level tool leaves PATH alone", "/usr/bin:/bin", "/go", "/usr/bin:/bin"},
		{"empty PATH becomes just the dir", "", "/usr/local/go/bin/go", "/usr/local/go/bin"},
		// Idempotent: a spawn per job would otherwise pile the same dir up.
		{"already leading is not repeated", "/usr/local/go/bin:/usr/bin", "/usr/local/go/bin/go", "/usr/local/go/bin:/usr/bin"},
		// Present but not first still gets promoted — being findable is not the
		// point, winning over a different go on an earlier entry is.
		{"present later is still promoted", "/usr/bin:/usr/local/go/bin", "/usr/local/go/bin/go", "/usr/local/go/bin:/usr/bin:/usr/local/go/bin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PrependToolDir(c.path, c.tool); got != c.want {
				t.Errorf("PrependToolDir(%q, %q) = %q, want %q", c.path, c.tool, got, c.want)
			}
		})
	}
}

func TestToolchainEnv(t *testing.T) {
	env := []string{"HOME=/Users/x", "PATH=/usr/bin:/bin", "LANG=en_US.UTF-8"}
	got := ToolchainEnv(env, "/opt/go/bin/go")

	var path string
	marker := false
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
		if kv == "LANG=en_US.UTF-8" {
			marker = true
		}
	}
	if path != "/opt/go/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q, want the go dir prepended to the inherited one", path)
	}
	if !marker {
		// HOME, LANG and the rest all have to survive: `make install` shells
		// out to git, security, codesign and launchctl.
		t.Error("ToolchainEnv dropped the rest of the environment")
	}
	if env[1] != "PATH=/usr/bin:/bin" {
		t.Errorf("ToolchainEnv wrote through the caller's slice: %q", env[1])
	}
}

func TestToolchainEnvEdges(t *testing.T) {
	// A bare (unresolved) tool must leave the environment alone rather than
	// prepend ".".
	env := []string{"PATH=/usr/bin"}
	if got := ToolchainEnv(env, "go"); len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Errorf("ToolchainEnv(_, \"go\") = %v, want the environment unchanged", got)
	}
	// No PATH at all: gain one, but only when there is a directory worth
	// naming.
	if got := ToolchainEnv([]string{"HOME=/x"}, "/opt/go/bin/go"); len(got) != 2 || got[1] != "PATH=/opt/go/bin" {
		t.Errorf("ToolchainEnv with no PATH = %v, want a PATH appended", got)
	}
	if got := ToolchainEnv([]string{"HOME=/x"}, "go"); len(got) != 1 {
		t.Errorf("ToolchainEnv with no PATH and no dir = %v, want no PATH invented", got)
	}
	// os.Environ() is the production input; it always carries a PATH, so this
	// is only here to catch a future refactor that returns a nil slice.
	if len(ToolchainEnv(os.Environ(), "/opt/go/bin/go")) == 0 {
		t.Error("ToolchainEnv(os.Environ(), ...) returned nothing")
	}
}
