import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fetchMapDrawings, saveMapDrawings } from "../../api/client.ts";
import {
  DRAW_UNSAVED_KEY,
  MAX_DRAWINGS,
  drawingBox,
  hitDrawing,
  newDrawingId,
  penPath,
  readUnsavedDrawOps,
  sanitizeDrawings,
  simplifyStroke,
  validDrawing,
  writeUnsavedDrawOps,
  type Drawing,
} from "./drawings.ts";

// mapdraw.edge.test.ts: athena-edge's blind suite for the drawing library and
// its two client calls (50-contract.md V, B1, B2): every boundary of V on the
// client side, what sanitizeDrawings drops, simplifyStroke's caps and
// degenerate inputs, hitDrawing at and past its reach and with corners in
// reverse order, and the unsaved-ops record under broken storage.

const EMPTY_OPS = { set: {}, remove: [] };

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.removeItem("kraken.mapView");
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapDrawUnsaved");
});

const line = (over: Partial<Drawing> & Record<string, unknown> = {}): Record<string, unknown> => ({
  kind: "line",
  pts: [0, 0, 10, 10],
  size: 2,
  ...over,
});

// ---- validDrawing

describe("validDrawing refuses what is not an object", () => {
  for (const v of [null, undefined, 5, "line", true, [], [0, 0, 10, 10]]) {
    it(`refuses ${JSON.stringify(v) ?? "undefined"}`, () => {
      expect(validDrawing(v)).toBe(false);
    });
  }
});

describe("validDrawing: kind", () => {
  for (const kind of ["", "Pen", "LINE", "circle", "erase", undefined, 3]) {
    it(`refuses kind ${JSON.stringify(kind) ?? "undefined"}`, () => {
      expect(validDrawing(line({ kind: kind as never }))).toBe(false);
    });
  }
  it("refuses a drawing with no kind key", () => {
    expect(validDrawing({ pts: [0, 0, 1, 1], size: 2 })).toBe(false);
  });
});

describe("validDrawing: pts length by kind", () => {
  const nums = (n: number) => Array.from({ length: n }, (_, i) => i);
  const cases: Array<[string, number, boolean]> = [
    ["pen", 2, false],
    ["pen", 4, true],
    ["pen", 5, false],
    ["pen", 1000, true],
    ["pen", 1002, false],
    ["line", 2, false],
    ["line", 4, true],
    ["line", 6, false],
    ["rect", 3, false],
    ["rect", 4, true],
    ["ellipse", 0, false],
    ["ellipse", 4, true],
    ["text", 0, false],
    ["text", 2, true],
    ["text", 4, false],
  ];
  for (const [kind, n, ok] of cases) {
    it(`${kind} with ${n} numbers is ${ok ? "valid" : "invalid"}`, () => {
      const d: Record<string, unknown> = { kind, pts: nums(n), size: 2 };
      if (kind === "text") d.text = "hi";
      expect(validDrawing(d)).toBe(ok);
    });
  }
  it("refuses pts that is not an array", () => {
    expect(validDrawing(line({ pts: "0,0,1,1" as never }))).toBe(false);
    expect(validDrawing(line({ pts: { 0: 0, 1: 0, 2: 1, 3: 1, length: 4 } as never }))).toBe(false);
    expect(validDrawing(line({ pts: undefined as never }))).toBe(false);
  });
});

describe("validDrawing: coordinates", () => {
  it("accepts ±10,000,000", () => {
    expect(validDrawing(line({ pts: [10_000_000, -10_000_000, -10_000_000, 10_000_000] }))).toBe(true);
  });
  for (const c of [10_000_000.5, -10_000_001, NaN, Infinity, -Infinity]) {
    it(`refuses coordinate ${c}`, () => {
      expect(validDrawing(line({ pts: [0, 0, 0, c] }))).toBe(false);
    });
  }
  for (const c of ["1", null, undefined, true]) {
    it(`refuses a non-number coordinate ${JSON.stringify(c) ?? "undefined"}`, () => {
      expect(validDrawing(line({ pts: [0, 0, 0, c as never] }))).toBe(false);
    });
  }
});

describe("validDrawing: size", () => {
  it("accepts 1000 and a tiny positive size", () => {
    expect(validDrawing(line({ size: 1000 }))).toBe(true);
    expect(validDrawing(line({ size: 1e-9 }))).toBe(true);
  });
  for (const s of [0, -0, -1, 1000.0001, NaN, Infinity, "2", null, undefined]) {
    it(`refuses size ${JSON.stringify(s) ?? String(s)}`, () => {
      expect(validDrawing(line({ size: s as never }))).toBe(false);
    });
  }
});

describe("validDrawing: text", () => {
  const text = (t: unknown) => ({ kind: "text", pts: [0, 0], size: 18, text: t });
  it("refuses a text drawing with no text", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18 })).toBe(false);
  });
  for (const t of ["", " ", "  \n\t ", 5, null]) {
    it(`refuses text ${JSON.stringify(t)}`, () => {
      expect(validDrawing(text(t))).toBe(false);
    });
  }
  it("accepts 500 code points that are 1000 UTF-16 units", () => {
    expect(validDrawing(text("😀".repeat(500)))).toBe(true);
  });
  it("refuses 501 code points", () => {
    expect(validDrawing(text("😀".repeat(501)))).toBe(false);
    expect(validDrawing(text("a".repeat(501)))).toBe(false);
  });
  it("accepts text with surrounding whitespace and newlines", () => {
    expect(validDrawing(text("  a\n\nb  "))).toBe(true);
  });
  for (const kind of ["pen", "line", "rect", "ellipse"]) {
    it(`${kind}: accepts text "" and refuses a non-empty text`, () => {
      const pts = [0, 0, 1, 1];
      expect(validDrawing({ kind, pts, size: 2, text: "" })).toBe(true);
      expect(validDrawing({ kind, pts, size: 2, text: "x" })).toBe(false);
    });
  }
});

describe("validDrawing ignores keys beyond the four", () => {
  it("accepts a valid drawing carrying extra keys", () => {
    expect(validDrawing(line({ color: "red", id: "x" }))).toBe(true);
  });
});

// ---- sanitizeDrawings

describe("sanitizeDrawings on what is not a plain object", () => {
  for (const v of [null, undefined, "x", 5, [line()], []]) {
    it(`returns {} for ${JSON.stringify(v) ?? "undefined"}`, () => {
      expect(sanitizeDrawings(v)).toEqual({});
    });
  }
});

describe("sanitizeDrawings drops bad keys and bad values", () => {
  it("keeps a 64-character key and drops an empty or 65-character key", () => {
    const k64 = "a".repeat(64);
    const k65 = "b".repeat(65);
    const out = sanitizeDrawings({ [k64]: line(), [k65]: line(), "": line() });
    expect(Object.keys(out)).toEqual([k64]);
  });
  it("drops invalid values and keeps the rest", () => {
    const out = sanitizeDrawings({
      ok: line(),
      badkind: line({ kind: "star" as never }),
      zero: line({ size: 0 }),
      nul: null,
      str: "line",
      textline: line({ text: "x" }),
    });
    expect(Object.keys(out)).toEqual(["ok"]);
  });
});

describe("sanitizeDrawings strips and copies", () => {
  it("keeps only kind, pts, size, and text on a text drawing", () => {
    const out = sanitizeDrawings({
      t: { kind: "text", pts: [1, 2], size: 18, text: "hi", extra: 1 },
      // `color` became a known key with Amendment 3, so the extra key here is another
      l: { kind: "line", pts: [0, 0, 1, 1], size: 2, text: "", width: 4 },
    });
    expect(out.t).toEqual({ kind: "text", pts: [1, 2], size: 18, text: "hi" });
    expect(Object.keys(out.t!).sort()).toEqual(["kind", "pts", "size", "text"]);
    expect(Object.keys(out.l!).sort()).toEqual(["kind", "pts", "size"]);
  });
  it("copies pts: changing the input afterwards leaves the output alone", () => {
    const src = line();
    const out = sanitizeDrawings({ a: src });
    (src.pts as number[])[0] = 99;
    expect(out.a!.pts).toEqual([0, 0, 10, 10]);
    expect(out.a!.pts).not.toBe(src.pts);
  });
  it("returns a new record, never the input", () => {
    const src = { a: line() };
    expect(sanitizeDrawings(src)).not.toBe(src);
  });
});

// ---- newDrawingId

describe("newDrawingId", () => {
  it("never repeats within one millisecond, and every id has the form", () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_793_000_000_000);
    const ids = Array.from({ length: 500 }, () => newDrawingId());
    for (const id of ids) expect(id).toMatch(/^[a-z0-9]{1,64}$/);
    expect(new Set(ids).size).toBe(500);
  });
  it("one id minted a millisecond later is greater than many minted in the earlier millisecond", () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_793_000_000_000);
    const early = Array.from({ length: 200 }, () => newDrawingId());
    vi.setSystemTime(1_793_000_000_001);
    const late = newDrawingId();
    for (const id of early) expect(late > id, `${late} > ${id}`).toBe(true);
  });
});

// ---- simplifyStroke

describe("simplifyStroke on short lists", () => {
  for (const pts of [[], [3, 4], [0, 0, 5, 5]]) {
    it(`returns a copy of ${JSON.stringify(pts)}`, () => {
      const out = simplifyStroke(pts, 1);
      expect(out).toEqual(pts);
      expect(out).not.toBe(pts);
    });
  }
});

describe("simplifyStroke never changes its input", () => {
  it("leaves a long noisy input as it was", () => {
    const pts: number[] = [];
    for (let i = 0; i < 300; i++) pts.push(i, (i % 7) * 3);
    const before = [...pts];
    simplifyStroke(pts, 0.5, 20);
    expect(pts).toEqual(before);
  });
});

describe("simplifyStroke caps the point count", () => {
  const zigzag = (n: number) => {
    const pts: number[] = [];
    for (let i = 0; i < n; i++) pts.push(i, i % 2 === 0 ? 0 : 50);
    return pts;
  };
  it("defaults the cap to 500 points, keeping both ends exactly", () => {
    const pts = zigzag(2000);
    const out = simplifyStroke(pts, 0);
    expect(out.length % 2).toBe(0);
    expect(out.length / 2).toBeLessThanOrEqual(500);
    expect(out.slice(0, 2)).toEqual([0, 0]);
    expect(out.slice(-2)).toEqual(pts.slice(-2));
  });
  it("honours an explicit cap of 10", () => {
    const pts = zigzag(100);
    const out = simplifyStroke(pts, 0, 10);
    expect(out.length / 2).toBeLessThanOrEqual(10);
    expect(out.slice(0, 2)).toEqual(pts.slice(0, 2));
    expect(out.slice(-2)).toEqual(pts.slice(-2));
  });
  it("a cap of 2 leaves exactly the two ends", () => {
    const pts = zigzag(40);
    expect(simplifyStroke(pts, 0, 2)).toEqual([...pts.slice(0, 2), ...pts.slice(-2)]);
  });
});

describe("simplifyStroke on straight and degenerate lines", () => {
  it("collapses collinear points to the ends at a tiny tolerance", () => {
    expect(simplifyStroke([0, 0, 1, 1, 2.5, 2.5, 7, 7, 10, 10], 1e-9)).toEqual([0, 0, 10, 10]);
  });
  it("collapses a vertical run to its ends", () => {
    expect(simplifyStroke([3, -5, 3, 0, 3, 4, 3, 9], 0.001)).toEqual([3, -5, 3, 9]);
  });
  it("collapses repeats of one point to that point twice", () => {
    expect(simplifyStroke([5, 5, 5, 5, 5, 5, 5, 5], 0.5)).toEqual([5, 5, 5, 5]);
  });
  it("keeps a spike taller than the tolerance", () => {
    const out = simplifyStroke([0, 0, 5, 0, 10, 20, 15, 0, 20, 0], 1);
    const pairs: number[][] = [];
    for (let i = 0; i < out.length; i += 2) pairs.push([out[i]!, out[i + 1]!]);
    expect(pairs).toContainEqual([10, 20]);
  });
});

// ---- hitDrawing

describe("hitDrawing: pen", () => {
  // size 2, tol 1: r = 2
  const pen: Drawing = { kind: "pen", pts: [0, 0, 10, 0, 10, 10], size: 2 };
  it("hits at exactly r from a segment and misses just past it", () => {
    expect(hitDrawing(pen, 5, 2, 1)).toBe(true);
    expect(hitDrawing(pen, 5, 2.05, 1)).toBe(false);
  });
  it("reaches past an end by r and no farther", () => {
    expect(hitDrawing(pen, -2, 0, 1)).toBe(true);
    expect(hitDrawing(pen, -2.05, 0, 1)).toBe(false);
  });
  it("misses the inside of the corner farther than r from both segments", () => {
    expect(hitDrawing(pen, 5, 5, 1)).toBe(false);
  });
  it("a pen of one repeated point hits within r of that point", () => {
    const dot: Drawing = { kind: "pen", pts: [3, 3, 3, 3], size: 2 };
    expect(hitDrawing(dot, 3, 5, 1)).toBe(true);
    expect(hitDrawing(dot, 3, 5.05, 1)).toBe(false);
  });
});

describe("hitDrawing: line", () => {
  const a: Drawing = { kind: "line", pts: [0, 0, 20, 0], size: 4 };
  const b: Drawing = { kind: "line", pts: [20, 0, 0, 0], size: 4 };
  // size 4, tol 0: r = 2
  const probes: Array<[number, number, boolean]> = [
    [10, 2, true],
    [10, 2.05, false],
    [22, 0, true],
    [22.05, 0, false],
    [-1.5, 1.3, true],
    [21.5, -1.5, false],
  ];
  for (const [x, y, want] of probes) {
    it(`(${x}, ${y}) is ${want ? "a hit" : "a miss"} whichever end comes first`, () => {
      expect(hitDrawing(a, x, y, 0)).toBe(want);
      expect(hitDrawing(b, x, y, 0)).toBe(want);
    });
  }
  it("a zero-length line hits as a point", () => {
    const dot: Drawing = { kind: "line", pts: [7, 7, 7, 7], size: 4 };
    expect(hitDrawing(dot, 7, 9, 0)).toBe(true);
    expect(hitDrawing(dot, 7, 9.05, 0)).toBe(false);
  });
});

describe("hitDrawing: rect", () => {
  // size 2, tol 1: r = 2; the box spans (0,0)-(100,50)
  const corners: number[][] = [
    [0, 0, 100, 50],
    [100, 50, 0, 0],
    [100, 0, 0, 50],
    [0, 50, 100, 0],
  ];
  const probes: Array<[number, number, boolean, string]> = [
    [50, 0, true, "on the top edge"],
    [50, -2, true, "r outside the top edge"],
    [50, -2.05, false, "just past r outside the top edge"],
    [50, 2, true, "r inside the top edge"],
    [50, 25, false, "the middle, farther than r from every edge"],
    [3, 25, false, "inside, just past r from the left edge"],
    [1.5, 25, true, "inside, within r of the left edge"],
    [-1.4, -1.4, true, "diagonally off a corner within r"],
    [-1.5, -1.5, false, "diagonally off a corner past r"],
    [102, 25, true, "r outside the right edge"],
  ];
  for (const pts of corners) {
    for (const [x, y, want, what] of probes) {
      it(`corners ${JSON.stringify(pts)}: ${what}`, () => {
        expect(hitDrawing({ kind: "rect", pts, size: 2 }, x, y, 1)).toBe(want);
      });
    }
  }
  it("a zero-width rect hits along its one edge and misses beside it", () => {
    const flat: Drawing = { kind: "rect", pts: [10, 0, 10, 40], size: 2 };
    expect(hitDrawing(flat, 10, 20, 1)).toBe(true);
    expect(hitDrawing(flat, 12, 20, 1)).toBe(true);
    expect(hitDrawing(flat, 12.05, 20, 1)).toBe(false);
  });
});

describe("hitDrawing: ellipse", () => {
  // box (0,0)-(100,50): centre (50,25), radii 50 and 25; size 2, tol 1: r = 2
  const boxes = [
    [0, 0, 100, 50],
    [100, 50, 0, 0],
    [0, 50, 100, 0],
  ];
  const onOutline: Array<[number, number]> = [
    [100, 25],
    [0, 25],
    [50, 0],
    [50, 50],
    [50 + 50 * Math.cos(Math.PI / 4), 25 + 25 * Math.sin(Math.PI / 4)],
  ];
  for (const pts of boxes) {
    const e: Drawing = { kind: "ellipse", pts, size: 2 };
    it(`${JSON.stringify(pts)}: every outline point hits`, () => {
      for (const [x, y] of onOutline) expect(hitDrawing(e, x, y, 1), `(${x}, ${y})`).toBe(true);
    });
    it(`${JSON.stringify(pts)}: a point r/2 off the outline, outside or inside, hits`, () => {
      expect(hitDrawing(e, 101, 25, 1)).toBe(true);
      expect(hitDrawing(e, 99, 25, 1)).toBe(true);
      expect(hitDrawing(e, 50, -1, 1)).toBe(true);
      expect(hitDrawing(e, 50, 1, 1)).toBe(true);
    });
    it(`${JSON.stringify(pts)}: a point more than 2r off the outline misses`, () => {
      expect(hitDrawing(e, 105, 25, 1)).toBe(false);
      expect(hitDrawing(e, 90, 25, 1)).toBe(false);
      expect(hitDrawing(e, 50, -5, 1)).toBe(false);
      expect(hitDrawing(e, 50, 5, 1)).toBe(false);
    });
    it(`${JSON.stringify(pts)}: the centre misses`, () => {
      expect(hitDrawing(e, 50, 25, 1)).toBe(false);
    });
  }
  it("an ellipse with zero height hits as the segment between its extreme points", () => {
    const flat: Drawing = { kind: "ellipse", pts: [20, 10, 0, 10], size: 2 };
    expect(hitDrawing(flat, 10, 10, 1)).toBe(true);
    expect(hitDrawing(flat, 10, 11, 1)).toBe(true);
    expect(hitDrawing(flat, 10, 15, 1)).toBe(false);
    expect(hitDrawing(flat, 25, 10, 1)).toBe(false);
  });
  it("an ellipse with zero width hits as its vertical segment", () => {
    const flat: Drawing = { kind: "ellipse", pts: [5, 40, 5, 0], size: 2 };
    expect(hitDrawing(flat, 5, 20, 1)).toBe(true);
    expect(hitDrawing(flat, 6, 20, 1)).toBe(true);
    expect(hitDrawing(flat, 10, 20, 1)).toBe(false);
  });
  it("an ellipse of both radii zero hits at its point only", () => {
    const dot: Drawing = { kind: "ellipse", pts: [5, 5, 5, 5], size: 2 };
    expect(hitDrawing(dot, 5, 5, 1)).toBe(true);
    expect(hitDrawing(dot, 5, 10, 1)).toBe(false);
  });
});

describe("hitDrawing: text", () => {
  // "ab\ncde" at size 10: box (0,0) 18 wide, 25 tall; tol 2 grows it to (-2,-2)-(20,27)
  const t: Drawing = { kind: "text", pts: [0, 0], size: 10, text: "ab\ncde" };
  const probes: Array<[number, number, boolean]> = [
    [9, 12, true],
    [19.9, 0, true],
    [20.1, 0, false],
    [0, 26.9, true],
    [0, 27.1, false],
    [-1.9, -1.9, true],
    [-2.1, 0, false],
    [0, -2.1, false],
  ];
  for (const [x, y, want] of probes) {
    it(`(${x}, ${y}) is ${want ? "a hit" : "a miss"}`, () => {
      expect(hitDrawing(t, x, y, 2)).toBe(want);
    });
  }
  it("ignores the text's size for its reach beyond the box", () => {
    // the box is the whole reach: size/2 is not added for text
    expect(hitDrawing(t, 22.5, 10, 2)).toBe(false);
  });
});

// ---- drawingBox

describe("drawingBox edges", () => {
  it("pen: the extent of points with negative coordinates", () => {
    expect(drawingBox({ kind: "pen", pts: [-5, 10, 3, -2, 0, 4], size: 9 })).toEqual({ x: -5, y: -2, w: 8, h: 12 });
  });
  it("pen of one repeated point: zero width and height", () => {
    expect(drawingBox({ kind: "pen", pts: [4, 4, 4, 4], size: 2 })).toEqual({ x: 4, y: 4, w: 0, h: 0 });
  });
  for (const kind of ["line", "rect", "ellipse"] as const) {
    it(`${kind}: corners in reverse order give the same box, never negative`, () => {
      expect(drawingBox({ kind, pts: [30, 40, 10, 5], size: 2 })).toEqual({ x: 10, y: 5, w: 20, h: 35 });
      expect(drawingBox({ kind, pts: [10, 40, 30, 5], size: 2 })).toEqual({ x: 10, y: 5, w: 20, h: 35 });
    });
  }
  it("rect of zero width", () => {
    expect(drawingBox({ kind: "rect", pts: [7, 0, 7, 9], size: 2 })).toEqual({ x: 7, y: 0, w: 0, h: 9 });
  });
  it("text counts code points, not UTF-16 units", () => {
    const b = drawingBox({ kind: "text", pts: [1, 2], size: 10, text: "😀😀" });
    expect(b.x).toBe(1);
    expect(b.y).toBe(2);
    expect(b.w).toBeCloseTo(2 * 10 * 0.6, 9);
    expect(b.h).toBeCloseTo(10 * 1.25, 9);
  });
  it("text with a trailing newline counts the empty last line", () => {
    const b = drawingBox({ kind: "text", pts: [0, 0], size: 8, text: "abc\n" });
    expect(b.w).toBeCloseTo(3 * 8 * 0.6, 9);
    expect(b.h).toBeCloseTo(2 * 8 * 1.25, 9);
  });
  it("text whose longest line is not the first", () => {
    const b = drawingBox({ kind: "text", pts: [0, 0], size: 10, text: "a\n\nabcd\nab" });
    expect(b.w).toBeCloseTo(4 * 10 * 0.6, 9);
    expect(b.h).toBeCloseTo(4 * 10 * 1.25, 9);
  });
});

// ---- penPath

describe("penPath edges", () => {
  it("one point is a bare move", () => {
    expect(penPath([3, 4])).toBe("M 3 4");
  });
  it("writes each number with String(n)", () => {
    expect(penPath([-1.5, 0.1, 2e-7, 3, 1e21, -0.25])).toBe(
      `M -1.5 0.1 L ${String(2e-7)} 3 L ${String(1e21)} -0.25`,
    );
  });
});

// ---- readUnsavedDrawOps / writeUnsavedDrawOps

describe("readUnsavedDrawOps on missing or broken records", () => {
  it("is empty with no key", () => {
    localStorage.removeItem(DRAW_UNSAVED_KEY);
    expect(readUnsavedDrawOps()).toEqual(EMPTY_OPS);
  });
  for (const raw of ["{not json", "null", "[]", "5", '"x"', '{"set":5,"remove":"x"}', '{"set":[],"remove":{}}']) {
    it(`is empty for ${raw}`, () => {
      localStorage.setItem(DRAW_UNSAVED_KEY, raw);
      expect(readUnsavedDrawOps()).toEqual(EMPTY_OPS);
    });
  }
  it("is empty when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(readUnsavedDrawOps()).toEqual(EMPTY_OPS);
  });
});

describe("readUnsavedDrawOps filters its entries", () => {
  it("drops invalid set entries and invalid remove ids", () => {
    localStorage.setItem(
      DRAW_UNSAVED_KEY,
      JSON.stringify({
        set: {
          ok: line(),
          bad: line({ size: 0 }),
          "": line(),
          ["k".repeat(65)]: line(),
        },
        remove: ["gone", "", "r".repeat(65), 5, null, "r".repeat(64)],
      }),
    );
    const ops = readUnsavedDrawOps();
    expect(Object.keys(ops.set)).toEqual(["ok"]);
    expect(ops.set.ok).toEqual({ kind: "line", pts: [0, 0, 10, 10], size: 2 });
    expect(ops.remove).toEqual(["gone", "r".repeat(64)]);
  });
});

describe("AM5 readUnsavedDrawOps with a key missing or mistyped", () => {
  it("a record with no remove reads remove as empty", () => {
    localStorage.setItem(DRAW_UNSAVED_KEY, JSON.stringify({ set: { ok: line() } }));
    expect(readUnsavedDrawOps()).toEqual({ set: { ok: { kind: "line", pts: [0, 0, 10, 10], size: 2 } }, remove: [] });
  });
  it("a record with no set reads set as empty", () => {
    localStorage.setItem(DRAW_UNSAVED_KEY, JSON.stringify({ remove: ["a"] }));
    expect(readUnsavedDrawOps()).toEqual({ set: {}, remove: ["a"] });
  });
  it("a record with neither key is no ops", () => {
    localStorage.setItem(DRAW_UNSAVED_KEY, "{}");
    expect(readUnsavedDrawOps()).toEqual(EMPTY_OPS);
  });
  it("a remove that is not an array drops a good set too", () => {
    localStorage.setItem(DRAW_UNSAVED_KEY, JSON.stringify({ set: { ok: line() }, remove: "a" }));
    expect(readUnsavedDrawOps()).toEqual(EMPTY_OPS);
  });
  it("a set that is not an object drops a good remove too", () => {
    for (const set of [[line()], "x", 5]) {
      localStorage.setItem(DRAW_UNSAVED_KEY, JSON.stringify({ set, remove: ["a"] }));
      expect(readUnsavedDrawOps(), JSON.stringify(set)).toEqual(EMPTY_OPS);
    }
  });
});

describe("AM6 MAX_DRAWINGS", () => {
  it("is 1000", () => {
    expect(MAX_DRAWINGS).toBe(1000);
  });
});

describe("writeUnsavedDrawOps", () => {
  it("removes the key when there is nothing to keep", () => {
    localStorage.setItem(DRAW_UNSAVED_KEY, JSON.stringify({ set: { a: line() }, remove: [] }));
    writeUnsavedDrawOps({ set: {}, remove: [] });
    expect(localStorage.getItem(DRAW_UNSAVED_KEY)).toBeNull();
  });
  it("keeps a record that holds only removes", () => {
    writeUnsavedDrawOps({ set: {}, remove: ["a"] });
    expect(localStorage.getItem(DRAW_UNSAVED_KEY)).not.toBeNull();
    expect(readUnsavedDrawOps()).toEqual({ set: {}, remove: ["a"] });
  });
  it("never throws when setItem throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    expect(() => writeUnsavedDrawOps({ set: { a: line() as unknown as Drawing }, remove: [] })).not.toThrow();
  });
  it("never throws when removeItem throws", () => {
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(() => writeUnsavedDrawOps({ set: {}, remove: [] })).not.toThrow();
  });
});

// ---- client

describe("fetchMapDrawings edges", () => {
  beforeEach(() => vi.unstubAllGlobals());

  const answer = (body: string, status = 200) =>
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(body, { status, headers: { "Content-Type": "application/json" } })),
    );

  it("resolves to {} when the answer has no drawings key", async () => {
    answer("{}");
    await expect(fetchMapDrawings()).resolves.toEqual({});
  });
  it("resolves to {} when drawings is null", async () => {
    answer('{"drawings":null}');
    await expect(fetchMapDrawings()).resolves.toEqual({});
  });
  for (const status of [400, 404, 500, 503]) {
    it(`rejects on ${status}`, async () => {
      answer('{"error":"no"}', status);
      await expect(fetchMapDrawings()).rejects.toBeDefined();
    });
  }
});

describe("saveMapDrawings edges", () => {
  beforeEach(() => vi.unstubAllGlobals());

  it("sends both keys even when both are empty", async () => {
    const f = vi.fn(async (_u: string, _i?: RequestInit) => new Response('{"ok":true}', { status: 200 }));
    vi.stubGlobal("fetch", f);
    await saveMapDrawings({ set: {}, remove: [] });
    expect(f).toHaveBeenCalledTimes(1);
    const [url, init] = f.mock.calls[0]!;
    expect(new URL(String(url), "http://board.local").pathname).toBe("/api/map/drawings");
    expect((init?.method ?? "GET").toUpperCase()).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ set: {}, remove: [] });
  });
  for (const status of [400, 403, 413, 500]) {
    it(`rejects on ${status}`, async () => {
      vi.stubGlobal("fetch", vi.fn(async () => new Response('{"error":"no"}', { status })));
      await expect(saveMapDrawings({ set: {}, remove: ["a"] })).rejects.toBeDefined();
    });
  }
});
