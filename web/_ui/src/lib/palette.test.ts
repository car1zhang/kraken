import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { JOB_COLORS, jobColorVar } from "./format.ts";

// palette.test.ts — the TypeScript end of a three-way agreement that nothing
// else can check.
//
// `web/jobpalette_test.go` already pins jobs.Palette (Go) against the two
// `--job-*` blocks in style.css and against ios/Kraken/Theme.swift, byte for
// byte. It cannot see TypeScript, so JOB_COLORS sits outside that guard — and
// the two ways it can drift are both silent:
//
//   a name here that style.css does not define → the swatch's
//   `--sw: var(--job-teal)` resolves to nothing and the chip paints blank;
//
//   a name style.css defines that is missing here → jobColorVar returns "" for
//   a colour the SERVER considers valid (jobs.ValidColor accepts it, the job
//   file stores it), so the card silently falls back to grey and the swatch
//   strip offers no way to pick it back.
//
// Vanilla carried the same exposure with one hand-written list; the port
// doubles the blast radius by having a TS list at all, which is exactly why it
// needs its own guard rather than an assumption.

/** The `--job-<name>` custom properties style.css actually defines. */
function cssJobNames(): string[] {
  // path relative to the vitest root (web/_ui), not import.meta.url: under
  // the jsdom environment that is not a file: URL and readFileSync rejects it
  const css = readFileSync("src/styles/style.css", "utf8");
  const names = new Set<string>();
  for (const m of css.matchAll(/--job-([a-z]+)\s*:/g)) {
    const name = m[1];
    if (name) names.add(name);
  }
  return [...names];
}

describe("JOB_COLORS vs style.css", () => {
  it("names exactly the colours the stylesheet defines", () => {
    // Sorted on both sides: order is style.css's business (dark block, then
    // light) and JOB_COLORS' order is /color's own, which the swatch strip
    // renders in. The SET is what has to agree.
    expect([...JOB_COLORS].sort()).toEqual(cssJobNames().sort());
  });

  it("resolves every one of them to a var the stylesheet defines", () => {
    const defined = new Set(cssJobNames());
    for (const c of JOB_COLORS) {
      const v = jobColorVar({ color: c });
      expect(v).toBe(`var(--job-${c})`);
      // the var this resolves to must be real, or the accent bar paints nothing
      expect(defined.has(c)).toBe(true);
    }
  });

  it("returns no custom property at all for an unset or unknown colour", () => {
    // NOT `var(--job-)` and not a literal: the card must omit the declaration
    // entirely so `var(--job-color, var(--text-2))` reaches its fallback.
    expect(jobColorVar({ color: undefined })).toBe("");
    expect(jobColorVar({})).toBe("");
    expect(jobColorVar(null)).toBe("");
    expect(jobColorVar({ color: "teal" })).toBe("");
  });

  it("has eight colours, matching jobs.Palette's length", () => {
    // A ninth added to the Go palette and style.css but not here would be
    // silently unpickable; this fails loudly instead.
    expect(JOB_COLORS).toHaveLength(8);
  });
});
