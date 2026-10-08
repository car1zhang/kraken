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
// silent. One token, two sources, byte agreement.

// paletteLightBlock is where style.css's light overrides start. Every
// declaration before it is the dark (`:root`) value, every one after it the
// light one — the same source-order split jobpalette_test.go makes, stated by
// anchor rather than by count because there is only one variable to count.
const paletteLightBlock = `:root[data-theme="light"]`

// palettePair is one colour's two halves, uppercased and `#`-stripped, so both
// sides of the comparison carry their theme with them rather than relying on
// argument order.
type palettePair struct{ light, dark string }

// stoppedCSSVarRe matches a `--stopped: #HEX` DECLARATION. Prose mentions of
// the token in comments never carry a `: #hex` tail, and `var(--stopped)`
// reads are followed by `)`, so neither can match.
var stoppedCSSVarRe = regexp.MustCompile(`--stopped\s*:\s*(#[0-9A-Fa-f]{6})`)

// stoppedSwiftRe matches Theme.swift's one frozen line:
//
//	static let stoppedUI = dyn(light: 0xC22A2A, dark: 0xFF5C5C)
var stoppedSwiftRe = regexp.MustCompile(`stoppedUI\s*=\s*dyn\(light:\s*0x([0-9A-Fa-f]{6}),\s*dark:\s*0x([0-9A-Fa-f]{6})\)`)

// stoppedCSSHexes returns the dark and light `--stopped` hexes, split at the
// light-theme block, with exactly one declaration required per block: a
// duplicate declaration means a later rule silently wins and the test pins the
// loser.
func stoppedCSSHexes(t *testing.T) palettePair {
	t.Helper()
	data, err := os.ReadFile("_ui/src/styles/style.css")
	if err != nil {
		t.Fatalf("read _ui/src/styles/style.css: %v", err)
	}
	src := string(data)
	lightAt := strings.Index(src, paletteLightBlock)
	if lightAt < 0 {
		t.Fatalf("style.css has no %s block to read the light theme from", paletteLightBlock)
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
			len(darks), paletteLightBlock, darks)
	}
	if len(lights) != 1 {
		t.Fatalf("style.css declares --stopped %d times inside %s, want exactly 1 (got %v)",
			len(lights), paletteLightBlock, lights)
	}
	return palettePair{light: lights[0], dark: darks[0]}
}

// TestStoppedPaletteMatchesSwift reads Theme.swift off disk relative to this
// package: a checkout without ios/ skips, a present-but-disagreeing file fails.
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
	swift := palettePair{light: strings.ToUpper(m[1]), dark: strings.ToUpper(m[2])}

	if swift.dark != css.dark {
		t.Errorf("stoppedUI dark = 0x%s, but --stopped in :root is #%s", swift.dark, css.dark)
	}
	if swift.light != css.light {
		t.Errorf("stoppedUI light = 0x%s, but --stopped in %s is #%s",
			swift.light, paletteLightBlock, css.light)
	}
}
