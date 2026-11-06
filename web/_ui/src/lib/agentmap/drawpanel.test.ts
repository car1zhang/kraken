import { describe, expect, it } from "vitest";

import { DRAW_COLORS, sanitizeDrawings, validDrawColor, validDrawing } from "./drawings.ts";

// A drawing's ink (contract Amendment 3): `color` is optional and, when
// present, one of the eight job colour names; anything else fails the
// drawing, and sanitizeDrawings keeps the name on the drawings it keeps.

const line = (extra: Record<string, unknown> = {}) => ({ kind: "line", pts: [0, 0, 1, 1], size: 2, ...extra });

describe("drawpanel: a drawing's color", () => {
  it("names the eight job colours", () => {
    expect(DRAW_COLORS).toEqual(["red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"]);
    for (const c of DRAW_COLORS) expect(validDrawColor(c)).toBe(true);
    for (const v of ["", "ink", "#ff0000", "RED", null, 3]) expect(validDrawColor(v)).toBe(false);
  });

  it("is optional, and refused when it is not a palette name", () => {
    expect(validDrawing(line())).toBe(true);
    for (const c of DRAW_COLORS) expect(validDrawing(line({ color: c }))).toBe(true);
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 12, text: "x", color: "pink" })).toBe(true);
    for (const c of ["", "#ff0000", "fg", "Red", 0, null]) expect(validDrawing(line({ color: c }))).toBe(false);
  });

  it("survives sanitizeDrawings, and only when present", () => {
    const out = sanitizeDrawings({ a: line({ color: "blue" }), b: line(), c: line({ color: "bad" }) });
    expect(Object.keys(out)).toEqual(["a", "b"]);
    expect(out.a).toEqual({ kind: "line", pts: [0, 0, 1, 1], size: 2, color: "blue" });
    expect("color" in out.b!).toBe(false);
  });
});
