package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// motionpolicy_test.go — the Go twin of _ui/src/styles/motion.test.ts,
// pinning the sheet's no-continuous-animation rule (the HUD effects
// section's PERFORMANCE RULE in style.css): an infinite animation that
// interpolates smoothly makes the compositor produce a frame at every vsync
// forever, which with an external monitor attached is system-wide
// WindowServer load — the display lag that retired the grid drift, the
// scan-bar sweep, and the wordmark glitch at the perf round. Stepped
// infinite animations invalidate a few times a second and are allowed;
// one-shot animations are allowed.
//
// The twin exists because the vitest copy runs only under `npm test`, which
// neither `make build` nor `go test ./...` invokes — so this file is what
// puts the rule behind the command the repo actually runs. Package web
// because it reads a file under web/ and is already where the repo keeps
// its source-text pins on this stylesheet (jobpalette_test.go reads the
// same path). Like everything in this package it needs `make web-assets`
// run once on a clean clone before it can compile — the embed constraint
// CLAUDE.md documents.

// cssStripComments removes /* … */ comments: this sheet's comments quote
// selectors and CSS constantly, and a pin that fails on prose would end up
// dictating how comments may be worded.
var cssCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

// cssAnimDeclRe matches every animation* declaration, property and value;
// the value runs to the next `;` or `}`, so a block-final declaration with
// no trailing semicolon is still caught.
var cssAnimDeclRe = regexp.MustCompile(`([a-z-]*animation[a-z-]*)\s*:\s*([^;}]+)`)

// splitAnimations splits a shorthand value on top-level commas —
// `steps(2, end)` stays whole.
func splitAnimations(value string) []string {
	var parts []string
	depth := 0
	var cur strings.Builder
	for _, ch := range value {
		switch {
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		}
		if ch == ',' && depth == 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(ch)
	}
	return append(parts, cur.String())
}

func motionCSS(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("_ui/src/styles/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	return cssCommentRe.ReplaceAllString(string(data), "")
}

var stepFnRe = regexp.MustCompile(`steps\(|step-start|step-end`)
var infiniteRe = regexp.MustCompile(`\binfinite\b`)
var cssRakeRe = regexp.MustCompile(`\.scan\s*\{[^}]*repeating-linear-gradient`)
var cssWillChangeRe = regexp.MustCompile(`will-change\s*:[^;}]+`)

func TestEveryInfiniteAnimationIsStepped(t *testing.T) {
	css := motionCSS(t)
	var infinite []string
	for _, m := range cssAnimDeclRe.FindAllStringSubmatch(css, -1) {
		prop, value := m[1], m[2]
		// A longhand iteration count would let an infinite animation hide
		// from the shorthand check below, so it is banned outright.
		if prop == "animation-iteration-count" {
			t.Errorf("animation-iteration-count longhand found (%q) — infinity must be visible in the animation shorthand", strings.TrimSpace(value))
		}
		if prop != "animation" {
			continue
		}
		for _, seg := range splitAnimations(value) {
			if infiniteRe.MatchString(seg) {
				infinite = append(infinite, strings.TrimSpace(seg))
			}
		}
	}
	// The sheet legitimately keeps stepped attention blinks (LED, clock
	// cursor, question chip, panel-title cursor, spinner) — an empty list
	// means this test is matching nothing and proving nothing.
	if len(infinite) == 0 {
		t.Fatal("no infinite animations found — the declaration matcher is broken")
	}
	for _, seg := range infinite {
		if !stepFnRe.MatchString(seg) {
			t.Errorf("smooth infinite animation %q — continuous animations must use a step timing function", seg)
		}
	}
}

func TestStaticScanlineRakeSurvives(t *testing.T) {
	css := motionCSS(t)
	// The rake is the one CRT layer deliberately kept at the perf round: a
	// static repeating gradient on .scan, with no animated ::after sweep.
	if !cssRakeRe.MatchString(css) {
		t.Error("the static scanline rake (.scan's repeating-linear-gradient) is gone — it was carl's explicit keep at the perf round")
	}
	if strings.Contains(css, ".scan::after") {
		t.Error(".scan::after is back — the sweeping scan bar was removed at the perf round for forcing a full-screen composite every vsync")
	}
}

func TestNoResidentWillChangeHints(t *testing.T) {
	css := motionCSS(t)
	// will-change holds a compositor layer resident; with nothing moving
	// continuously it is pure memory cost. The reduced-motion block's
	// `will-change: auto` reset is the one allowed mention.
	for _, m := range cssWillChangeRe.FindAllString(css, -1) {
		if !strings.Contains(m, "auto") {
			t.Errorf("resident will-change hint %q — nothing moves continuously, so a promoted layer is pure memory cost", strings.TrimSpace(m))
		}
	}
}
