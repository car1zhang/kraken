package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// cursorpolicy_test.go — the Go twin of _ui/src/styles/cursors.test.ts,
// pinning the cursor family's art/role assignment: the REST cursor (body,
// `crosshair` fallback) is the full dot crosshair, and every ACT-ABLE
// surface (pointer/text/grab/grabbing fallbacks) wears the open reticle —
// pulled-back arms, a 3.5-radius ring. The reversal round swapped the
// roles, carl drove it and switched them back, so this assignment is a
// decision made twice — and a rule quietly flipping one role would read as
// "nothing changed" with nothing else failing.
//
// The twin exists for motionpolicy_test.go's exact reason: the vitest copy
// runs only under `npm test`, which neither `make build` nor
// `go test ./...` invokes — so this file is what puts the rule behind the
// command the repo actually runs. Package web because it reads a file under
// web/ and is already where the repo keeps its source-text pins on this
// stylesheet.

// cursorDeclRe matches every data-URI cursor declaration, capturing the SVG
// payload and the fallback keyword. It hard-codes the single quotes and the
// `12 12` hotspot the sheet uses everywhere; TestCursorPinSeesEveryDecl is
// what keeps a rule written differently from silently escaping it.
var cursorDeclRe = regexp.MustCompile(`cursor:\s*url\('([^']+)'\)\s*12 12,\s*([a-z-]+)`)

var cursorURLRe = regexp.MustCompile(`cursor:\s*url\(`)

// The two arts, by the substrings that can only be one of them: the ring's
// circle is stroked at r=3.5, the dot's filled at r=1.3.
const (
	ringArt = `r="3.5" fill="none"`
	dotArt  = `r="1.3" fill=`
)

func cursorDecls(t *testing.T) (css string, decls [][2]string) {
	t.Helper()
	data, err := os.ReadFile("_ui/src/styles/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	// Comments are stripped first, same as every pin on this sheet: its
	// comments quote selectors and CSS constantly.
	css = cssCommentRe.ReplaceAllString(string(data), "")
	for _, m := range cursorDeclRe.FindAllStringSubmatch(css, -1) {
		decls = append(decls, [2]string{m[1], m[2]})
	}
	return css, decls
}

func TestCursorPinSeesEveryDecl(t *testing.T) {
	css, decls := cursorDecls(t)
	if len(decls) == 0 {
		t.Fatal("no cursor declarations extracted — the matcher is broken")
	}
	// Tie the extraction to the sheet's own count, so a rule with different
	// quoting or hotspot fails here instead of dropping out of the pin.
	if want := len(cursorURLRe.FindAllString(css, -1)); len(decls) != want {
		t.Errorf("extracted %d cursor declarations but the sheet has %d cursor: url( rules — one no longer matches the extraction shape", len(decls), want)
	}
}

func TestRestCursorWearsTheDot(t *testing.T) {
	_, decls := cursorDecls(t)
	var rest [][2]string
	for _, d := range decls {
		if d[1] == "crosshair" {
			rest = append(rest, d)
		}
	}
	// The dark and light body rules, exactly.
	if len(rest) != 2 {
		t.Errorf("expected exactly 2 rest (crosshair-fallback) cursor rules, got %d", len(rest))
	}
	for _, d := range rest {
		if !strings.Contains(d[0], dotArt) {
			t.Errorf("rest cursor lost the dot art: %s", d[0])
		}
	}
}

func TestActableCursorsWearTheRing(t *testing.T) {
	_, decls := cursorDecls(t)
	kws := map[string]bool{}
	for _, d := range decls {
		kws[d[1]] = true
		if d[1] == "crosshair" {
			continue
		}
		if !strings.Contains(d[0], ringArt) {
			t.Errorf("act-able (%s) cursor lost the ring art: %s", d[1], d[0])
		}
	}
	// The fallback keywords are what keep the roles apart when a browser
	// refuses the data URI — all five must survive.
	for _, kw := range []string{"crosshair", "pointer", "text", "grab", "grabbing"} {
		if !kws[kw] {
			t.Errorf("missing fallback keyword: %s", kw)
		}
	}
}
