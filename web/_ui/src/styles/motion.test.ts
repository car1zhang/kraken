import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// motion.test.ts — pins the sheet's no-continuous-animation rule (the HUD
// effects section's PERFORMANCE RULE): an infinite animation that
// interpolates smoothly makes the compositor produce a frame at every vsync
// for the life of the page, which with an external monitor attached is
// system-wide WindowServer load — the display lag that retired the grid
// drift, the scan-bar sweep, and the wordmark glitch at the perf round.
// Discrete (stepped) infinite animations — the LED, the clock cursor, the
// question chip, the panel-title cursor, the update spinner — invalidate a
// few times a second and are fine; one-shot animations are fine.
//
// A source-text test on style.css itself, the same stance as
// lib/palette.test.ts here and web/jobpalette_test.go on the Go side: the
// invariant lives in a stylesheet no runtime assertion can see, so the test
// reads the sheet. web/motionpolicy_test.go is this file's Go twin — `npm
// test` is not part of `make build` or `go test ./...`, so the twin is what
// puts this rule behind the command the repo actually runs; this copy exists
// so a web-side edit fails in the web-side suite too, with the better error.

// path relative to the vitest root (web/_ui), not import.meta.url: under
// the jsdom environment that is not a file: URL and readFileSync rejects it
const raw = readFileSync("src/styles/style.css", "utf8");

// Comments are stripped before every assertion: this sheet's comments quote
// selectors and CSS constantly, and a pin that fails on prose would end up
// dictating how comments may be worded.
const css = raw.replace(/\/\*[\s\S]*?\*\//g, "");

/**
 * Every `animation*` declaration in the stripped sheet, as [property, value].
 * The value runs to the next `;` or `}`, so a block-final declaration with no
 * trailing semicolon is still caught.
 */
const decls = [...css.matchAll(/([a-z-]*animation[a-z-]*)\s*:\s*([^;}]+)/g)].map(
  (m) => [m[1] ?? "", (m[2] ?? "").trim()] as const,
);

/** Split a shorthand value on top-level commas — `steps(2, end)` stays whole. */
function splitAnimations(value: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let cur = "";
  for (const ch of value) {
    if (ch === "(") depth++;
    if (ch === ")") depth--;
    if (ch === "," && depth === 0) {
      parts.push(cur);
      cur = "";
    } else {
      cur += ch;
    }
  }
  parts.push(cur);
  return parts;
}

describe("motion policy", () => {
  it("no animation-iteration-count longhand — infinity must be visible in the shorthand", () => {
    // The stepped-infinite check below reads the shorthand; a longhand
    // iteration count would let an infinite animation hide from it.
    expect(decls.filter(([p]) => p === "animation-iteration-count")).toEqual([]);
  });

  it("every infinite animation is stepped, never smooth", () => {
    const infinite = decls
      .filter(([p]) => p === "animation")
      .flatMap(([, v]) => splitAnimations(v))
      .filter((seg) => /\binfinite\b/.test(seg));
    // The sheet legitimately keeps stepped attention blinks — so the list
    // must be non-empty, or this test is matching nothing and proving
    // nothing.
    expect(infinite.length).toBeGreaterThan(0);
    for (const seg of infinite) {
      expect(seg, `smooth infinite animation: ${seg}`).toMatch(
        /steps\(|step-start|step-end/,
      );
    }
  });

  it("the static scanline rake survives (carl's keep)", () => {
    // The rake is the one CRT layer deliberately kept at the perf round: a
    // static repeating gradient on .scan, with no animated ::after sweep.
    expect(css).toMatch(/\.scan\s*\{[^}]*repeating-linear-gradient/);
    expect(css).not.toMatch(/\.scan::after/);
  });

  it("no will-change hints outlive the movers they promoted", () => {
    // will-change holds a compositor layer resident; with nothing moving
    // continuously it is pure memory cost. The reduced-motion block's
    // `will-change: auto` reset is the one allowed mention.
    const mentions = css.match(/will-change\s*:[^;}]+/g) ?? [];
    expect(mentions.filter((m) => !m.includes("auto"))).toEqual([]);
  });
});
