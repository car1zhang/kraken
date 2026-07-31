package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// graphpalette_contract_test.go — the graph screen's selection accent is
// spelled out in TWO places: `--skill-graph` in web/_ui/src/styles/style.css
// (the board's sigma view) and `KTheme.skillGraphUI` in ios/Kraken/Theme.swift
// (the native iOS graph screen). They must agree to the byte, for the same
// reason the eight job colours next door do: a selected node is supposed to be
// the same pixel colour on the phone as it is on the board.
//
// Nothing but this test keeps them agreeing, and the drift is silent — exactly
// how --skill-info/--skill-do already drifted in this repo once. Package web
// is the home because it is the only package that ships the stylesheet; the
// mould is jobpalette_test.go's, deliberately.

// graphContractCSSVarRe matches a `--skill-graph: #HEX` DECLARATION. It cannot
// match `--skill-graph-soft:` (a `-` follows `graph`, not `:`), `var(--skill-graph)`
// (a `)` follows), or `--glow-skill-graph:`/`--text-glow-skill-graph:` (only one
// `-` precedes `skill`, not two).
var graphContractCSSVarRe = regexp.MustCompile(`--skill-graph\s*:\s*(#[0-9A-Fa-f]{6})`)

// graphContractSwiftRe matches Theme.swift's one frozen line:
//
//	static let skillGraphUI = dyn(light: 0x456F0B, dark: 0x87DF49)
var graphContractSwiftRe = regexp.MustCompile(`skillGraphUI\s*=\s*dyn\(light:\s*0x([0-9A-Fa-f]{6}),\s*dark:\s*0x([0-9A-Fa-f]{6})\)`)

// graphContractLightBlock is where style.css's light overrides start. Every
// `--skill-graph` declaration before it is the dark (`:root`) value, every one
// after it the light one — the same source-order split jobpalette_test.go
// makes, stated by anchor rather than by count because there is only one
// variable here to count.
const graphContractLightBlock = `:root[data-theme="light"]`

// graphContractPair is one colour's two halves, uppercased and `#`-stripped —
// jobpalette_test.go's `swiftPair` in miniature, so both sides of the
// comparison carry their theme with them rather than relying on argument order.
type graphContractPair struct{ light, dark string }

// graphContractCSSHexes returns the dark and light `--skill-graph` hexes.
// Exactly one declaration must live in each block: a second one in either would
// mean a later rule silently wins and this test would be pinning the loser.
func graphContractCSSHexes(t *testing.T) graphContractPair {
	t.Helper()
	// Read from source on disk, not from the embed: the embed holds
	// _ui/dist — vite's hashed, minified bundle — so locating the CSS in it
	// would mean globbing a content-hashed filename and parsing minified
	// output. This is the copy a human edits and the copy that ships.
	data, err := os.ReadFile("_ui/src/styles/style.css")
	if err != nil {
		t.Fatalf("read _ui/src/styles/style.css: %v", err)
	}
	src := string(data)
	lightAt := strings.Index(src, graphContractLightBlock)
	if lightAt < 0 {
		t.Fatalf("style.css has no %s block to read the light theme from", graphContractLightBlock)
	}
	var darks, lights []string
	for _, m := range graphContractCSSVarRe.FindAllStringSubmatchIndex(src, -1) {
		hex := strings.TrimPrefix(strings.ToUpper(src[m[2]:m[3]]), "#")
		if m[0] < lightAt {
			darks = append(darks, hex)
		} else {
			lights = append(lights, hex)
		}
	}
	if len(darks) != 1 {
		t.Fatalf("style.css declares --skill-graph %d times before %s, want exactly 1 (got %v)",
			len(darks), graphContractLightBlock, darks)
	}
	if len(lights) != 1 {
		t.Fatalf("style.css declares --skill-graph %d times inside %s, want exactly 1 (got %v)",
			len(lights), graphContractLightBlock, lights)
	}
	return graphContractPair{light: lights[0], dark: darks[0]}
}

// TestGraphPaletteMatchesSwift reads Theme.swift off disk (it is not embedded
// — the iOS app is built from the repo, not from the binary), relative to this
// package. A repo checkout without ios/ skips rather than fails; a
// present-but-disagreeing file is a hard failure, which is the point.
func TestGraphPaletteMatchesSwift(t *testing.T) {
	css := graphContractCSSHexes(t)

	data, err := os.ReadFile("../ios/Kraken/Theme.swift")
	if os.IsNotExist(err) {
		t.Skip("no ios/Kraken/Theme.swift in this checkout")
	}
	if err != nil {
		t.Fatalf("read Theme.swift: %v", err)
	}
	m := graphContractSwiftRe.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("Theme.swift declares no `skillGraphUI = dyn(light: 0x…, dark: 0x…)`; "+
			"the contract freezes that line verbatim (style.css has light %s / dark %s)",
			css.light, css.dark)
	}
	swift := graphContractPair{light: strings.ToUpper(m[1]), dark: strings.ToUpper(m[2])}

	if swift.dark != css.dark {
		t.Errorf("skillGraphUI dark = 0x%s, but --skill-graph in :root is #%s", swift.dark, css.dark)
	}
	if swift.light != css.light {
		t.Errorf("skillGraphUI light = 0x%s, but --skill-graph in %s is #%s",
			swift.light, graphContractLightBlock, css.light)
	}
}
