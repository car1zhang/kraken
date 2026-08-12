package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// stoppedpalette_contract_test.go — the "stopped" status red is spelled out in
// TWO places: `--stopped` in web/_ui/src/styles/style.css (the closed
// side-thread chips and the thread overlay's closed pill) and
// `KTheme.stoppedUI` in ios/Kraken/Theme.swift (the same chips and pill on the
// phone). Both declarations claim, in their own comments, to mirror the other
// exactly; nothing but this test keeps that claim true, and the drift is
// silent. The mould is graphpalette_contract_test.go's, deliberately — one
// token, two sources, byte agreement.

// stoppedCSSVarRe matches a `--stopped: #HEX` DECLARATION. Prose mentions of
// the token in comments never carry a `: #hex` tail, and `var(--stopped)`
// reads are followed by `)`, so neither can match.
var stoppedCSSVarRe = regexp.MustCompile(`--stopped\s*:\s*(#[0-9A-Fa-f]{6})`)

// stoppedSwiftRe matches Theme.swift's one frozen line:
//
//	static let stoppedUI = dyn(light: 0xC22A2A, dark: 0xFF5C5C)
var stoppedSwiftRe = regexp.MustCompile(`stoppedUI\s*=\s*dyn\(light:\s*0x([0-9A-Fa-f]{6}),\s*dark:\s*0x([0-9A-Fa-f]{6})\)`)

// stoppedCSSHexes returns the dark and light `--stopped` hexes, split at the
// light-theme block the same way graphContractCSSHexes splits, with the same
// exactly-one-per-block requirement and for the same reason: a duplicate
// declaration means a later rule silently wins and the test pins the loser.
func stoppedCSSHexes(t *testing.T) graphContractPair {
	t.Helper()
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
	for _, m := range stoppedCSSVarRe.FindAllStringSubmatchIndex(src, -1) {
		hex := strings.TrimPrefix(strings.ToUpper(src[m[2]:m[3]]), "#")
		if m[0] < lightAt {
			darks = append(darks, hex)
		} else {
			lights = append(lights, hex)
		}
	}
	if len(darks) != 1 {
		t.Fatalf("style.css declares --stopped %d times before %s, want exactly 1 (got %v)",
			len(darks), graphContractLightBlock, darks)
	}
	if len(lights) != 1 {
		t.Fatalf("style.css declares --stopped %d times inside %s, want exactly 1 (got %v)",
			len(lights), graphContractLightBlock, lights)
	}
	return graphContractPair{light: lights[0], dark: darks[0]}
}

// TestStoppedPaletteMatchesSwift reads Theme.swift off disk relative to this
// package, exactly as TestGraphPaletteMatchesSwift does: a checkout without
// ios/ skips, a present-but-disagreeing file fails.
func TestStoppedPaletteMatchesSwift(t *testing.T) {
	css := stoppedCSSHexes(t)

	data, err := os.ReadFile("../ios/Kraken/Theme.swift")
	if os.IsNotExist(err) {
		t.Skip("no ios/Kraken/Theme.swift in this checkout")
	}
	if err != nil {
		t.Fatalf("read Theme.swift: %v", err)
	}
	m := stoppedSwiftRe.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("Theme.swift declares no `stoppedUI = dyn(light: 0x…, dark: 0x…)`; "+
			"the contract freezes that line verbatim (style.css has light %s / dark %s)",
			css.light, css.dark)
	}
	swift := graphContractPair{light: strings.ToUpper(m[1]), dark: strings.ToUpper(m[2])}

	if swift.dark != css.dark {
		t.Errorf("stoppedUI dark = 0x%s, but --stopped in :root is #%s", swift.dark, css.dark)
	}
	if swift.light != css.light {
		t.Errorf("stoppedUI light = 0x%s, but --stopped in %s is #%s",
			swift.light, graphContractLightBlock, css.light)
	}
}
