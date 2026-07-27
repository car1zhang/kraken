package web

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// jobpalette_test.go — the eight job colours are spelled out in THREE
// places: jobs.Palette (Go, the only one that reaches tmux and the TUI),
// web/static/style.css's two --job-* blocks, and ios/Kraken/Theme.swift's
// jobXxxUI pairs. They must agree to the byte, because the whole promise of
// the feature is that a board swatch is the same pixel colour as the prompt
// bar of the pane that job opens.
//
// Nothing but this test keeps them agreeing, and the failure is silent —
// exactly how --skill-info/--skill-do already drifted in this repo. Package
// web is the home because it is the only package that both reads the
// embedded static assets and imports jobs.

// cssJobVars extracts every `--job-<name>: #HEX` declaration in src, in
// source order, as (name, uppercase hex) pairs. Two blocks exist (dark in
// :root, light in the light-scheme override), so order matters: the first
// eight are dark, the next eight light.
var cssJobVarRe = regexp.MustCompile(`--job-([a-z]+)\s*:\s*(#[0-9A-Fa-f]{6})`)

type cssVar struct{ name, hex string }

func cssJobVars(t *testing.T) []cssVar {
	t.Helper()
	data, err := fs.ReadFile(webAssets, "static/style.css")
	if err != nil {
		t.Fatalf("read embedded static/style.css: %v", err)
	}
	var out []cssVar
	for _, m := range cssJobVarRe.FindAllStringSubmatch(string(data), -1) {
		out = append(out, cssVar{name: m[1], hex: strings.ToUpper(m[2])})
	}
	return out
}

func TestJobPaletteMatchesCSS(t *testing.T) {
	vars := cssJobVars(t)
	if want := 2 * len(jobs.Palette); len(vars) != want {
		t.Fatalf("style.css declares %d --job-* vars, want %d (a dark block and a light block of %d)",
			len(vars), want, len(jobs.Palette))
	}
	dark, light := vars[:len(jobs.Palette)], vars[len(jobs.Palette):]
	for i, c := range jobs.Palette {
		// order is /color's own and must be the same in both blocks and in Go
		if dark[i].name != c.Name {
			t.Errorf("dark block position %d is --job-%s, want --job-%s", i, dark[i].name, c.Name)
			continue
		}
		if light[i].name != c.Name {
			t.Errorf("light block position %d is --job-%s, want --job-%s", i, light[i].name, c.Name)
			continue
		}
		if got, want := dark[i].hex, strings.ToUpper(c.Hex); got != want {
			t.Errorf("--job-%s (dark) = %s, but jobs.Palette says %s", c.Name, got, want)
		}
		if got, want := light[i].hex, strings.ToUpper(c.HexLight); got != want {
			t.Errorf("--job-%s (light) = %s, but jobs.Palette says %s", c.Name, got, want)
		}
	}
}

// swiftJobColorRe matches Theme.swift's one-line-per-colour form, e.g.
//
//	static let jobRedUI = dyn(light: 0xCA2323, dark: 0xDC2626)
var swiftJobColorRe = regexp.MustCompile(`job([A-Za-z]+)UI\s*=\s*dyn\(light:\s*0x([0-9A-Fa-f]{6}),\s*dark:\s*0x([0-9A-Fa-f]{6})\)`)

// TestJobPaletteMatchesSwift reads Theme.swift off disk (it is not embedded
// — the iOS app is built from the repo, not from the binary), relative to
// this package. A repo checkout without ios/ skips rather than fails; a
// present-but-disagreeing file is a hard failure, which is the point.
func TestJobPaletteMatchesSwift(t *testing.T) {
	data, err := os.ReadFile("../ios/Kraken/Theme.swift")
	if os.IsNotExist(err) {
		t.Skip("no ios/Kraken/Theme.swift in this checkout")
	}
	if err != nil {
		t.Fatalf("read Theme.swift: %v", err)
	}
	type swiftPair struct{ light, dark string }
	found := map[string]swiftPair{}
	for _, m := range swiftJobColorRe.FindAllStringSubmatch(string(data), -1) {
		found[strings.ToLower(m[1])] = swiftPair{light: strings.ToUpper(m[2]), dark: strings.ToUpper(m[3])}
	}
	if len(found) != len(jobs.Palette) {
		t.Fatalf("Theme.swift declares %d job colours, want %d", len(found), len(jobs.Palette))
	}
	for _, c := range jobs.Palette {
		got, ok := found[c.Name]
		if !ok {
			t.Errorf("Theme.swift has no job colour named %q", c.Name)
			continue
		}
		if want := strings.TrimPrefix(strings.ToUpper(c.Hex), "#"); got.dark != want {
			t.Errorf("job %s dark = 0x%s, but jobs.Palette says %s", c.Name, got.dark, want)
		}
		if want := strings.TrimPrefix(strings.ToUpper(c.HexLight), "#"); got.light != want {
			t.Errorf("job %s light = 0x%s, but jobs.Palette says %s", c.Name, got.light, want)
		}
	}
}
