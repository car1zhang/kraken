import { describe, expect, it } from "vitest";

import { simplifyStroke, validDrawing } from "./drawings.ts";

// Regression pins for defects found in review round 1 and settled by
// Amendment 2 of the map-drawings contract.

describe("drawrepair: whitespace is the server's set (AM10)", () => {
  it("a text of U+0085 alone is not a valid drawing", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18, text: "\u0085" })).toBe(false);
  });
  it("a text of U+FEFF and U+0085 around a space is not valid either", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18, text: "﻿ \u0085\n" })).toBe(false);
  });
  it("a text with one visible character among them stays valid", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18, text: "\u0085x\u0085" })).toBe(true);
  });
});

describe("drawrepair: thinning to fit keeps the stroke's shape (AM14)", () => {
  it("an evenly scribbled 8000-point stroke keeps more than half the cap", () => {
    // half-waves of 8 samples, amplitude 40: every feature the same size
    const pts: number[] = [];
    for (let i = 0; i < 8000; i++) {
      const phase = i % 16;
      const y = phase < 8 ? (phase / 8) * 40 : ((16 - phase) / 8) * 40;
      pts.push(i * 0.5, y);
    }
    const out = simplifyStroke(pts, 0.4, 500);
    expect(out.length / 2).toBeLessThanOrEqual(500);
    expect(out.length / 2).toBeGreaterThan(250);
    expect(out.slice(0, 2)).toEqual(pts.slice(0, 2));
    expect(out.slice(-2)).toEqual(pts.slice(-2));
  });
});
