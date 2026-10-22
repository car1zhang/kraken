package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// protocolParityFiles is the protocol-bearing surface CLAUDE.md § Protocol
// parity names: the multiplayer protocol (discovery, the envelope, the node
// routes and proxies, the gate tiers, pairing) and the update protocol. Every
// file here opens with the "Protocol parity:" header comment, so an agent
// editing one reads the rule before the code.
var protocolParityFiles = []string{
	"internal/api/nodes.go",
	"internal/api/pair.go",
	"internal/api/gate.go",
	"internal/api/update.go",
	"internal/jobs/node.go",
	"internal/jobs/nodes.go",
	"internal/jobs/update.go",
	"internal/jobs/ghauth.go",
	"internal/core/tailscale.go",
	"internal/store/nodes.go",
	"internal/store/pair.go",
	"cmd/kraken/cmd_nodes.go",
	"cmd/kraken/cmd_update.go",
	"ios/Kraken/Pairing.swift",
}

const (
	protocolParityHeading = "## Protocol parity"
	// the two sentences the rule turns on, pinned verbatim: compatibility is
	// all-ways, and a change is flagged to whoever makes it
	protocolParityCompat = "they are identical in every fork, byte for byte at the wire, and any build of kraken-core interoperates with any fork in every direction"
	protocolParityFlag   = "A change to either is a protocol change and is flagged to the person making it"
)

// protocolParitySection returns the body of CLAUDE.md § Protocol parity, up to
// the next `## ` heading.
func protocolParitySection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	doc := string(b)
	i := strings.Index(doc, protocolParityHeading)
	if i < 0 {
		t.Fatalf("CLAUDE.md has no %q section", protocolParityHeading)
	}
	rest := doc[i+len(protocolParityHeading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// isProtocolRoute says whether a registered pattern belongs to the
// multiplayer or update protocol: the node routes a sibling calls, the
// `/api/nodes/*` proxies a board calls, pairing, update, and the health probe
// every tier may reach.
func isProtocolRoute(p string) bool {
	for _, pre := range []string{"/api/health", "/api/node", "/api/nodes", "/api/pair", "/api/update"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// TestProtocolParityDocCoversTheSurface pins CLAUDE.md § Protocol parity
// against the code it governs: the section exists and states the two
// sentences the rule turns on, every protocol route the api package registers
// is listed in it (so a new route cannot land undocumented), every
// protocol-bearing file opens with the header comment and is named in the
// section, and the README points at the section.
func TestProtocolParityDocCoversTheSurface(t *testing.T) {
	section := protocolParitySection(t)
	for _, want := range []string{protocolParityCompat, protocolParityFlag} {
		if !strings.Contains(section, want) {
			t.Errorf("CLAUDE.md § Protocol parity lacks the sentence %q", want)
		}
	}

	rec := &patternRecorder{}
	registerRoutes(rec)
	n := 0
	for _, p := range rec.patterns {
		if !isProtocolRoute(p) {
			continue
		}
		n++
		if !strings.Contains(section, "`"+p+"`") {
			t.Errorf("route %s is registered but CLAUDE.md § Protocol parity does not list it", p)
		}
	}
	if n < 20 {
		t.Errorf("only %d protocol routes recorded; the recorder or the route prefixes moved", n)
	}

	for _, f := range protocolParityFiles {
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Errorf("protocol file %s: %v", f, err)
			continue
		}
		head := string(b)
		if len(head) > 2048 {
			head = head[:2048]
		}
		if !strings.Contains(head, "Protocol parity:") {
			t.Errorf("%s has no \"Protocol parity:\" header comment", f)
		}
		// named either by its full path or, after a sibling in the same
		// directory, by its base name
		if !strings.Contains(section, "`"+f+"`") && !strings.Contains(section, "`"+filepath.Base(f)+"`") {
			t.Errorf("CLAUDE.md § Protocol parity does not name %s", f)
		}
	}

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if !strings.Contains(string(readme), "Protocol parity") {
		t.Error("README.md does not point at CLAUDE.md § Protocol parity")
	}
}
