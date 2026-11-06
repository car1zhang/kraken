import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fetchMapDrawings, saveMapDrawings } from "../../api/client.ts";
import {
  DRAW_STROKE_PX,
  DRAW_TEXT_PX,
  DRAW_TOOLS,
  DRAW_UNSAVED_KEY,
  ERASE_TOL_PX,
  MAX_DRAW_COORD,
  MAX_DRAW_SIZE,
  MAX_PEN_POINTS,
  MAX_TEXT_RUNES,
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

// mapdraw.contract.test.ts — athena-logic's blind tests for the drawings
// module and its two client calls (50-contract.md B1, B2): the stated
// invariants and the ordinary successful paths.

const mapDrawContractClear = () => {
  localStorage.removeItem("kraken.mapView");
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapDrawUnsaved");
};

beforeEach(() => {
  mapDrawContractClear();
  vi.unstubAllGlobals();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mapDrawContractClear();
});

const mapDrawContractPen: Drawing = { kind: "pen", pts: [0, 0, 10, 5, 20, 0], size: 3 };
const mapDrawContractLine: Drawing = { kind: "line", pts: [0, 0, 100, 0], size: 4 };
const mapDrawContractRect: Drawing = { kind: "rect", pts: [0, 0, 100, 50], size: 4 };
const mapDrawContractEllipse: Drawing = { kind: "ellipse", pts: [0, 0, 200, 100], size: 4 };
const mapDrawContractText: Drawing = { kind: "text", pts: [5, 5], size: 10, text: "ab\ncde" };

/** Distance from (px, py) to the segment (ax, ay)-(bx, by). */
function mapDrawContractSegDist(px: number, py: number, ax: number, ay: number, bx: number, by: number): number {
  const dx = bx - ax;
  const dy = by - ay;
  const len2 = dx * dx + dy * dy;
  const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / len2));
  return Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
}

function mapDrawContractPolyDist(px: number, py: number, pts: number[]): number {
  let best = Infinity;
  for (let i = 0; i + 3 < pts.length; i += 2) {
    best = Math.min(best, mapDrawContractSegDist(px, py, pts[i]!, pts[i + 1]!, pts[i + 2]!, pts[i + 3]!));
  }
  if (pts.length === 2) best = Math.hypot(px - pts[0]!, py - pts[1]!);
  return best;
}

// ---------------------------------------------------------------------------
// constants
// ---------------------------------------------------------------------------

describe("drawings constants", () => {
  it("lists the six tools in order", () => {
    expect([...DRAW_TOOLS]).toEqual(["pen", "line", "rect", "ellipse", "text", "erase"]);
  });

  it("carries the contract's values", () => {
    expect(DRAW_STROKE_PX).toBe(3);
    expect(DRAW_TEXT_PX).toBe(18);
    expect(ERASE_TOL_PX).toBe(6);
    expect(MAX_PEN_POINTS).toBe(500);
    expect(MAX_TEXT_RUNES).toBe(500);
    expect(MAX_DRAW_SIZE).toBe(1000);
    expect(MAX_DRAW_COORD).toBe(10_000_000);
    expect(DRAW_UNSAVED_KEY).toBe("kraken.mapDrawUnsaved");
  });
});

// ---------------------------------------------------------------------------
// validDrawing
// ---------------------------------------------------------------------------

describe("validDrawing", () => {
  it("accepts one drawing of every kind", () => {
    for (const d of [mapDrawContractPen, mapDrawContractLine, mapDrawContractRect, mapDrawContractEllipse, mapDrawContractText]) {
      expect(validDrawing(d), d.kind).toBe(true);
    }
  });

  it("accepts a non-text kind with text absent or empty", () => {
    expect(validDrawing({ kind: "line", pts: [0, 0, 1, 1], size: 1, text: "" })).toBe(true);
  });

  it("ignores keys other than the four", () => {
    expect(validDrawing({ kind: "rect", pts: [0, 0, 1, 1], size: 1, colour: "red", z: 3 })).toBe(true);
  });

  it("accepts a two-point pen and a 500-point pen", () => {
    expect(validDrawing({ kind: "pen", pts: [0, 0, 1, 1], size: 1 })).toBe(true);
    expect(validDrawing({ kind: "pen", pts: Array.from({ length: 1000 }, (_, i) => i), size: 1 })).toBe(true);
  });

  it("accepts text of 500 code points even when it is longer in UTF-16", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18, text: "😀".repeat(500) })).toBe(true);
  });

  it("accepts size 1000 and coordinates at ±10,000,000", () => {
    expect(validDrawing({ kind: "line", pts: [-10_000_000, 10_000_000, 0, 0], size: 1000 })).toBe(true);
  });

  it("rejects an unknown kind", () => {
    expect(validDrawing({ kind: "star", pts: [0, 0, 1, 1], size: 1 })).toBe(false);
  });

  it("rejects pts of the wrong length for the kind", () => {
    expect(validDrawing({ kind: "line", pts: [0, 0, 1], size: 1 })).toBe(false);
    expect(validDrawing({ kind: "rect", pts: [0, 0, 1, 1, 2, 2], size: 1 })).toBe(false);
    expect(validDrawing({ kind: "text", pts: [0, 0, 1, 1], size: 1, text: "x" })).toBe(false);
    expect(validDrawing({ kind: "pen", pts: [0, 0], size: 1 })).toBe(false);
    expect(validDrawing({ kind: "pen", pts: [0, 0, 1], size: 1 })).toBe(false);
  });

  it("rejects a size of 0 or over 1000", () => {
    expect(validDrawing({ kind: "line", pts: [0, 0, 1, 1], size: 0 })).toBe(false);
    expect(validDrawing({ kind: "line", pts: [0, 0, 1, 1], size: 1001 })).toBe(false);
  });

  it("rejects a non-finite or out-of-range coordinate", () => {
    expect(validDrawing({ kind: "line", pts: [0, NaN, 1, 1], size: 1 })).toBe(false);
    expect(validDrawing({ kind: "line", pts: [0, 0, Infinity, 1], size: 1 })).toBe(false);
    expect(validDrawing({ kind: "line", pts: [0, 0, 10_000_001, 1], size: 1 })).toBe(false);
  });

  it("rejects text that is missing, only whitespace, or a non-text kind carrying text", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18 })).toBe(false);
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18, text: " \n\t" })).toBe(false);
    expect(validDrawing({ kind: "line", pts: [0, 0, 1, 1], size: 1, text: "x" })).toBe(false);
  });

  it("rejects text over 500 code points", () => {
    expect(validDrawing({ kind: "text", pts: [0, 0], size: 18, text: "a".repeat(501) })).toBe(false);
  });

  it("rejects things that are not objects", () => {
    expect(validDrawing(null)).toBe(false);
    expect(validDrawing("pen")).toBe(false);
    expect(validDrawing([0, 0])).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// sanitizeDrawings
// ---------------------------------------------------------------------------

describe("sanitizeDrawings", () => {
  it("keeps valid entries and drops invalid values and invalid ids", () => {
    const got = sanitizeDrawings({
      a: mapDrawContractLine,
      b: { kind: "star", pts: [0, 0, 1, 1], size: 1 },
      "": mapDrawContractLine,
      ["x".repeat(65)]: mapDrawContractLine,
      ["y".repeat(64)]: mapDrawContractRect,
      t: mapDrawContractText,
    });
    expect(got).toEqual({ a: mapDrawContractLine, ["y".repeat(64)]: mapDrawContractRect, t: mapDrawContractText });
  });

  it("keeps only kind, pts, size and (for text) text", () => {
    const got = sanitizeDrawings({
      l: { kind: "line", pts: [0, 0, 1, 1], size: 2, text: "", extra: 1 },
      t: { kind: "text", pts: [1, 2], size: 18, text: "hi", extra: true },
    });
    expect(Object.keys(got.l!).sort()).toEqual(["kind", "pts", "size"]);
    expect(Object.keys(got.t!).sort()).toEqual(["kind", "pts", "size", "text"]);
    expect(got.t).toEqual({ kind: "text", pts: [1, 2], size: 18, text: "hi" });
  });

  it("copies pts", () => {
    const pts = [0, 0, 1, 1];
    const got = sanitizeDrawings({ l: { kind: "line", pts, size: 2 } });
    expect(got.l!.pts).not.toBe(pts);
    pts[0] = 99;
    expect(got.l!.pts).toEqual([0, 0, 1, 1]);
  });

  it("returns a new record", () => {
    const input = { a: mapDrawContractLine };
    expect(sanitizeDrawings(input)).not.toBe(input);
  });

  it("returns {} for null, an array and a string", () => {
    expect(sanitizeDrawings(null)).toEqual({});
    expect(sanitizeDrawings([mapDrawContractLine])).toEqual({});
    expect(sanitizeDrawings("drawings")).toEqual({});
  });
});

// ---------------------------------------------------------------------------
// newDrawingId
// ---------------------------------------------------------------------------

describe("newDrawingId", () => {
  it("matches /^[a-z0-9]{1,64}$/", () => {
    for (let i = 0; i < 50; i++) expect(newDrawingId()).toMatch(/^[a-z0-9]{1,64}$/);
  });

  it("never repeats, even within one millisecond", () => {
    vi.spyOn(Date, "now").mockReturnValue(1_792_000_000_000);
    const ids = new Set<string>();
    for (let i = 0; i < 1000; i++) ids.add(newDrawingId());
    expect(ids.size).toBe(1000);
  });

  it("compares greater when minted at a later millisecond", () => {
    const now = vi.spyOn(Date, "now");
    for (const [early, late] of [
      [1_792_000_000_000, 1_792_000_000_001],
      [1_792_000_000_999, 1_792_000_001_000],
      [1_792_000_000_000, 1_892_000_000_000],
    ] as const) {
      now.mockReturnValue(early);
      const a = newDrawingId();
      now.mockReturnValue(late);
      const b = newDrawingId();
      expect(b > a, `${b} > ${a}`).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// simplifyStroke
// ---------------------------------------------------------------------------

const mapDrawContractWave = (n: number) => {
  const out: number[] = [];
  for (let i = 0; i < n; i++) out.push(i * 2, Math.sin(i / 7) * 40);
  return out;
};

describe("simplifyStroke", () => {
  it("never changes its input and returns a new list", () => {
    const pts = mapDrawContractWave(100);
    const before = [...pts];
    const got = simplifyStroke(pts, 1);
    expect(pts).toEqual(before);
    expect(got).not.toBe(pts);
  });

  it("keeps the first and last points exactly", () => {
    const pts = mapDrawContractWave(120);
    const got = simplifyStroke(pts, 2);
    expect(got.slice(0, 2)).toEqual(pts.slice(0, 2));
    expect(got.slice(-2)).toEqual(pts.slice(-2));
  });

  it("returns a flat list of whole points", () => {
    const got = simplifyStroke(mapDrawContractWave(120), 2);
    expect(got.length % 2).toBe(0);
  });

  it("collapses collinear points to the two ends for any tolerance > 0", () => {
    const pts = [0, 0, 1, 1, 2, 2, 5, 5, 9, 9, 10, 10];
    for (const tol of [0.001, 1, 100]) expect(simplifyStroke(pts, tol)).toEqual([0, 0, 10, 10]);
    expect(simplifyStroke([0, 3, 4, 3, 8, 3, 12, 3], 0.5)).toEqual([0, 3, 12, 3]);
  });

  it("keeps every dropped point within tolerance of the result", () => {
    const pts = mapDrawContractWave(200);
    for (const tol of [0.5, 2, 5]) {
      const got = simplifyStroke(pts, tol);
      expect(got.length / 2).toBeLessThanOrEqual(MAX_PEN_POINTS);
      for (let i = 0; i < pts.length; i += 2) {
        expect(mapDrawContractPolyDist(pts[i]!, pts[i + 1]!, got)).toBeLessThanOrEqual(tol + 1e-9);
      }
    }
  });

  it("thins to at most MAX_PEN_POINTS by default", () => {
    const pts = mapDrawContractWave(1500);
    const got = simplifyStroke(pts, 0.0001);
    expect(got.length / 2).toBeLessThanOrEqual(MAX_PEN_POINTS);
    expect(got.slice(0, 2)).toEqual(pts.slice(0, 2));
    expect(got.slice(-2)).toEqual(pts.slice(-2));
  });

  it("thins to at most maxPoints when given", () => {
    const pts = mapDrawContractWave(300);
    const got = simplifyStroke(pts, 0.0001, 10);
    expect(got.length / 2).toBeLessThanOrEqual(10);
    expect(got.slice(0, 2)).toEqual(pts.slice(0, 2));
    expect(got.slice(-2)).toEqual(pts.slice(-2));
  });

  it("returns a copy of a list of two points or fewer", () => {
    for (const pts of [[], [3, 4], [3, 4, 5, 6]]) {
      const got = simplifyStroke(pts, 1);
      expect(got).toEqual(pts);
      expect(got).not.toBe(pts);
    }
  });
});

// ---------------------------------------------------------------------------
// hitDrawing — every case below uses tol 2 and size 4, so r = 4
// ---------------------------------------------------------------------------

describe("hitDrawing", () => {
  const tol = 2;

  it("pen: hits within r of the polyline and misses beyond it", () => {
    const pen: Drawing = { kind: "pen", pts: [0, 0, 100, 0, 100, 100], size: 4 };
    expect(hitDrawing(pen, 50, 3.9, tol)).toBe(true);
    expect(hitDrawing(pen, 103.9, 50, tol)).toBe(true);
    expect(hitDrawing(pen, 50, 4.1, tol)).toBe(false);
    expect(hitDrawing(pen, 50, 50, tol)).toBe(false);
  });

  it("line: measures to the segment, not the infinite line", () => {
    expect(hitDrawing(mapDrawContractLine, 50, -3.9, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractLine, 103.9, 0, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractLine, 104.1, 0, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractLine, 50, 4.1, tol)).toBe(false);
  });

  it("the reach grows with size", () => {
    const thick: Drawing = { kind: "line", pts: [0, 0, 100, 0], size: 20 };
    expect(hitDrawing(thick, 50, 11.9, tol)).toBe(true);
    expect(hitDrawing(thick, 50, 12.1, tol)).toBe(false);
  });

  it("rect: hits near any edge, misses deep inside and far outside", () => {
    expect(hitDrawing(mapDrawContractRect, 50, 0, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractRect, 50, 53.9, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractRect, -3.9, 25, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractRect, 96.1, 25, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractRect, 50, 25, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractRect, 50, 4.1, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractRect, 50, -4.1, tol)).toBe(false);
  });

  it("ellipse: a point on the outline hits", () => {
    // box 0..200 x 0..100: centre (100, 50), radii 100 and 50
    for (const [x, y] of [
      [200, 50],
      [0, 50],
      [100, 0],
      [100, 100],
      [100 + 100 * Math.cos(Math.PI / 4), 50 + 50 * Math.sin(Math.PI / 4)],
    ] as const) {
      expect(hitDrawing(mapDrawContractEllipse, x, y, tol), `${x},${y}`).toBe(true);
    }
  });

  it("ellipse: a point within r/2 of the outline hits", () => {
    expect(hitDrawing(mapDrawContractEllipse, 202, 50, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractEllipse, 198, 50, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractEllipse, 100, 102, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractEllipse, 100, 2, tol)).toBe(true);
  });

  it("ellipse: a point more than 2r from the outline misses, the centre included", () => {
    expect(hitDrawing(mapDrawContractEllipse, 209, 50, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractEllipse, 100, 109, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractEllipse, 100, 50, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractEllipse, 150, 50, tol)).toBe(false);
  });

  it("ellipse: a zero radius hits as the segment between its extreme points", () => {
    const flat: Drawing = { kind: "ellipse", pts: [0, 10, 100, 10], size: 4 };
    expect(hitDrawing(flat, 50, 13, tol)).toBe(true);
    expect(hitDrawing(flat, 0, 10, tol)).toBe(true);
    expect(hitDrawing(flat, 50, 20, tol)).toBe(false);
    expect(hitDrawing(flat, 150, 10, tol)).toBe(false);
  });

  it("text: hits inside the box grown by tol", () => {
    // box: x 5, y 5, w 3 * 10 * 0.6 = 18, h 2 * 10 * 1.25 = 25
    expect(hitDrawing(mapDrawContractText, 10, 10, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractText, 24.9, 5, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractText, 3.1, 31.9, tol)).toBe(true);
    expect(hitDrawing(mapDrawContractText, 25.1, 10, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractText, 10, 32.1, tol)).toBe(false);
    expect(hitDrawing(mapDrawContractText, 2.9, 10, tol)).toBe(false);
  });

  it("corner order never changes the answer", () => {
    const probes: [number, number][] = [
      [50, 0],
      [50, 25],
      [100, 52],
      [200, 50],
      [100, 50],
      [-3, 25],
      [150, 120],
    ];
    for (const kind of ["line", "rect", "ellipse"] as const) {
      const a: Drawing = { kind, pts: [0, 0, 200, 100], size: 4 };
      const b: Drawing = { kind, pts: [200, 100, 0, 0], size: 4 };
      const c: Drawing = { kind, pts: [0, 100, 200, 0], size: 4 };
      const d: Drawing = { kind, pts: [200, 0, 0, 100], size: 4 };
      for (const [x, y] of probes) {
        expect(hitDrawing(b, x, y, tol), `${kind} ${x},${y}`).toBe(hitDrawing(a, x, y, tol));
        if (kind !== "line") {
          expect(hitDrawing(c, x, y, tol), `${kind} ${x},${y}`).toBe(hitDrawing(a, x, y, tol));
          expect(hitDrawing(d, x, y, tol), `${kind} ${x},${y}`).toBe(hitDrawing(a, x, y, tol));
        }
      }
    }
  });
});

// ---------------------------------------------------------------------------
// drawingBox
// ---------------------------------------------------------------------------

describe("drawingBox", () => {
  it("pen: the extent of its points", () => {
    expect(drawingBox({ kind: "pen", pts: [10, 20, -5, 40, 30, 0], size: 50 })).toEqual({ x: -5, y: 0, w: 35, h: 40 });
  });

  it("line, rect, ellipse: the box spanned by the two points, in any order", () => {
    for (const kind of ["line", "rect", "ellipse"] as const) {
      expect(drawingBox({ kind, pts: [50, 60, 10, 20], size: 9 }), kind).toEqual({ x: 10, y: 20, w: 40, h: 40 });
      expect(drawingBox({ kind, pts: [10, 60, 50, 20], size: 9 }), kind).toEqual({ x: 10, y: 20, w: 40, h: 40 });
    }
  });

  it("never reports a negative width or height", () => {
    const box = drawingBox({ kind: "line", pts: [5, 5, 5, 5], size: 1 });
    expect(box).toEqual({ x: 5, y: 5, w: 0, h: 0 });
  });

  it("text: longest line's code points × size × 0.6 by lines × size × 1.25", () => {
    expect(drawingBox(mapDrawContractText)).toEqual({ x: 5, y: 5, w: 18, h: 25 });
    const box = drawingBox({ kind: "text", pts: [-3, 7], size: 20, text: "😀😀\nx\n" });
    expect(box.x).toBe(-3);
    expect(box.y).toBe(7);
    expect(box.w).toBeCloseTo(2 * 20 * 0.6, 9);
    expect(box.h).toBeCloseTo(3 * 20 * 1.25, 9);
  });
});

// ---------------------------------------------------------------------------
// penPath
// ---------------------------------------------------------------------------

describe("penPath", () => {
  it("writes the contract's example", () => {
    expect(penPath([0, 0, 10, 5, 20, 0])).toBe("M 0 0 L 10 5 L 20 0");
  });

  it("writes each number with String(n)", () => {
    expect(penPath([-1.5, 2.25, 0.1, -0])).toBe(`M -1.5 2.25 L 0.1 ${String(-0)}`);
    expect(penPath([3, 4])).toBe("M 3 4");
  });
});

// ---------------------------------------------------------------------------
// readUnsavedDrawOps / writeUnsavedDrawOps
// ---------------------------------------------------------------------------

describe("unsaved draw ops in localStorage", () => {
  it("reads {set: {}, remove: []} when the key is absent", () => {
    expect(readUnsavedDrawOps()).toEqual({ set: {}, remove: [] });
  });

  it("round-trips what it writes, as JSON {set, remove} under DRAW_UNSAVED_KEY", () => {
    const ops = { set: { a: mapDrawContractLine, t: mapDrawContractText }, remove: ["gone"] };
    writeUnsavedDrawOps(ops);
    expect(JSON.parse(localStorage.getItem("kraken.mapDrawUnsaved")!)).toEqual(ops);
    expect(readUnsavedDrawOps()).toEqual(ops);
  });

  it("removes the key when there is nothing to keep", () => {
    writeUnsavedDrawOps({ set: { a: mapDrawContractLine }, remove: [] });
    expect(localStorage.getItem("kraken.mapDrawUnsaved")).not.toBeNull();
    writeUnsavedDrawOps({ set: {}, remove: [] });
    expect(localStorage.getItem("kraken.mapDrawUnsaved")).toBeNull();
  });

  it("keeps a write that only removes", () => {
    writeUnsavedDrawOps({ set: {}, remove: ["x"] });
    expect(readUnsavedDrawOps()).toEqual({ set: {}, remove: ["x"] });
  });

  it("reads empty ops from a value that does not parse to the shape", () => {
    for (const v of ["{not json", "[]", "null", '"text"', '{"set": [], "remove": 4}']) {
      localStorage.setItem("kraken.mapDrawUnsaved", v);
      expect(readUnsavedDrawOps(), v).toEqual({ set: {}, remove: [] });
    }
  });

  it("sanitizes set and drops invalid ids from remove", () => {
    localStorage.setItem(
      "kraken.mapDrawUnsaved",
      JSON.stringify({
        set: { ok: mapDrawContractLine, bad: { kind: "star", pts: [], size: 1 } },
        remove: ["keep", "", "z".repeat(65), 7],
      }),
    );
    expect(readUnsavedDrawOps()).toEqual({ set: { ok: mapDrawContractLine }, remove: ["keep"] });
  });

  it("reads empty ops when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(readUnsavedDrawOps()).toEqual({ set: {}, remove: [] });
  });

  it("never throws on write when storage throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(() => writeUnsavedDrawOps({ set: { a: mapDrawContractLine }, remove: [] })).not.toThrow();
    expect(() => writeUnsavedDrawOps({ set: {}, remove: [] })).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// client: fetchMapDrawings / saveMapDrawings (B2)
// ---------------------------------------------------------------------------

function mapDrawContractStubFetch(body: unknown, status = 200) {
  const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve(
      new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function mapDrawContractOnlyCall(fetchMock: ReturnType<typeof mapDrawContractStubFetch>) {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit | undefined];
  return { url: new URL(String(url), "http://board.local"), method: (init?.method ?? "GET").toUpperCase(), init };
}

describe("fetchMapDrawings", () => {
  it("GETs /api/map/drawings and resolves to its drawings object", async () => {
    const drawings = { a: mapDrawContractLine, t: mapDrawContractText, junk: { kind: "star" } };
    const f = mapDrawContractStubFetch({ drawings });
    await expect(fetchMapDrawings()).resolves.toEqual(drawings);
    const c = mapDrawContractOnlyCall(f);
    expect(c.method).toBe("GET");
    expect(c.url.pathname).toBe("/api/map/drawings");
  });

  it("resolves to {} when drawings is absent or null", async () => {
    mapDrawContractStubFetch({});
    await expect(fetchMapDrawings()).resolves.toEqual({});
    mapDrawContractStubFetch({ drawings: null });
    await expect(fetchMapDrawings()).resolves.toEqual({});
  });

  it("rejects on a non-2xx answer", async () => {
    mapDrawContractStubFetch({ error: "boom" }, 500);
    await expect(fetchMapDrawings()).rejects.toBeDefined();
  });
});

describe("saveMapDrawings", () => {
  it("POSTs {set, remove} to /api/map/drawings", async () => {
    const f = mapDrawContractStubFetch({ ok: true });
    const ops = { set: { a: mapDrawContractLine }, remove: ["gone"] };
    await saveMapDrawings(ops);
    const c = mapDrawContractOnlyCall(f);
    expect(c.method).toBe("POST");
    expect(c.url.pathname).toBe("/api/map/drawings");
    expect(JSON.parse(String(c.init?.body))).toEqual({ set: { a: mapDrawContractLine }, remove: ["gone"] });
  });

  it("sends both keys even when one side is empty", async () => {
    const f = mapDrawContractStubFetch({ ok: true });
    await saveMapDrawings({ set: {}, remove: ["x"] });
    const body = JSON.parse(String(mapDrawContractOnlyCall(f).init?.body)) as Record<string, unknown>;
    expect(body).toEqual({ set: {}, remove: ["x"] });
  });

  it("rejects on a non-2xx answer", async () => {
    mapDrawContractStubFetch({ error: "bad" }, 400);
    await expect(saveMapDrawings({ set: { a: mapDrawContractLine }, remove: [] })).rejects.toBeDefined();
  });
});
