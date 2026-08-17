package web

import (
	"regexp"
	"strings"
	"testing"
)

// backgroundpolicy_test.go — pins the board-background rule's structural
// invariant: the whole image treatment in style.css is gated on [data-bg],
// so a board with no background set paints pixel-identically to a board
// that never had the feature (the job's stated requirement). The failure
// mode is silent — hoisting the background declarations out of the gate, or
// adding a second reader of --bg-image outside it, renders fine WITH a
// background and only breaks the no-background default, which nobody
// testing the feature looks at.
//
// _ui/src/styles/background.test.ts is this file's vitest twin, per
// motion.test.ts's stated split: `npm test` is not part of `make build` or
// `go test ./...`, so THIS copy is what puts the rule behind the commands
// the repo actually runs, and the vitest copy exists so a web-side edit
// fails in the web-side suite too, with the better error.

// cssRuleRe captures selector{body} pairs shallowly — the sheet's rule
// structure is flat (no nesting), which motionpolicy_test.go's scans lean
// on the same way.
var cssRuleRe = regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)

func TestBackgroundImageRulesStayGated(t *testing.T) {
	css := motionCSS(t) // comments stripped, same reason as the motion pins

	// The ungated base rule survives: html paints the plain ground. This is
	// the rule the [data-bg] block overrides, and deleting it would repaint
	// every board, background or not.
	if !regexp.MustCompile(`html\s*\{\s*background:\s*var\(--ground\);\s*\}`).MatchString(css) {
		t.Error("style.css no longer has the bare `html { background: var(--ground); }` rule — the plain board's one background paint")
	}

	readers := 0
	for _, m := range cssRuleRe.FindAllStringSubmatch(css, -1) {
		selector, body := m[1], m[2]
		if !strings.Contains(body, "--bg-image") {
			continue
		}
		readers++
		if !strings.Contains(selector, "[data-bg]") {
			t.Errorf("selector %q reads --bg-image outside the [data-bg] gate — the no-background default must stay pixel-identical", strings.TrimSpace(selector))
		}
	}
	if readers == 0 {
		t.Error("no rule reads --bg-image at all — the background image treatment is gone from style.css")
	}

	// The sheet may READ var(--bg-image) but never DECLARE it: the inline
	// stamp (web/_ui/src/lib/background.ts) is the property's one writer,
	// and a sheet-side declaration would silently race it.
	if regexp.MustCompile(`--bg-image\s*:`).MatchString(css) {
		t.Error("style.css declares --bg-image — the inline stamp in lib/background.ts is the one writer")
	}
}
