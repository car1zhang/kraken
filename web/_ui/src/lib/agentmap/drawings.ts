/**
 * Drawings on the agent map: freehand strokes, straight lines, rectangles,
 * ellipses and text labels on the map's plane, in world units, saved on the
 * server in one kv row (GET/POST /api/map/drawings). They have no effect on
 * cards, physics or jobs. Pure functions only; MapView draws them as SVG.
 *
 * A drawing is `{kind, pts, size, text?}`, addressed by a client-minted id.
 * `validDrawing` is the rule the save route also checks, so a drawing this
 * side keeps is one a save does not refuse for its shape.
 */
import { readLS, writeLS } from "../../hooks/useLocalStorage.ts";

export type DrawKind = "pen" | "line" | "rect" | "ellipse" | "text";
export type DrawTool = DrawKind | "erase";
export interface Drawing {
  kind: DrawKind;
  pts: number[];
  size: number;
  text?: string;
}
export interface DrawOps {
  set: Record<string, Drawing>;
  remove: string[];
}
export interface DrawBox {
  x: number;
  y: number;
  w: number;
  h: number;
}

export const DRAW_TOOLS: readonly DrawTool[] = ["pen", "line", "rect", "ellipse", "text", "erase"];
/** A new stroke's width, screen px (divided by the zoom it is drawn at). */
export const DRAW_STROKE_PX = 3;
/** A new text's font size, screen px. */
export const DRAW_TEXT_PX = 18;
/** How near the eraser must be, screen px. */
export const ERASE_TOL_PX = 6;
export const MAX_PEN_POINTS = 500;
export const MAX_TEXT_RUNES = 500;
export const MAX_DRAW_SIZE = 1000;
export const MAX_DRAW_COORD = 10_000_000;
/** The most drawings the map holds: the save route refuses a call whose result would hold more. */
export const MAX_DRAWINGS = 1000;
/** A drawings POST not answered by now is aborted and counts as a failed save (its ops stay queued). */
export const DRAW_SAVE_TIMEOUT_MS = 20_000;
/** Ops the server has not accepted yet, kept in this browser (see readUnsavedDrawOps). */
export const DRAW_UNSAVED_KEY = "kraken.mapDrawUnsaved";

const KINDS: ReadonlySet<string> = new Set(["pen", "line", "rect", "ellipse", "text"]);
/** The longest id, the server's 64 bytes; client-minted ids are ASCII. */
const MAX_ID = 64;

const runes = (s: string) => [...s].length;

/**
 * Whitespace, as both sides judge a text: every Unicode White_Space code
 * point plus U+FEFF. JS `\s` is that set less U+0085, which is added here.
 */
const SPACE = "\\s\\u0085";
const VISIBLE = new RegExp(`[^${SPACE}]`, "u");
const EDGE_SPACE = new RegExp(`^[${SPACE}]+|[${SPACE}]+$`, "gu");

/** `s` with leading and trailing whitespace (the set above) removed: the text tool's commit trim. */
export function trimDrawText(s: string): string {
  return s.replace(EDGE_SPACE, "");
}

/** A non-empty string of at most 64 characters (code points). */
function validId(k: unknown): k is string {
  return typeof k === "string" && k.length > 0 && runes(k) <= MAX_ID;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** True exactly when `v` is an object satisfying the shared validity rule; other keys are ignored. */
export function validDrawing(v: unknown): v is Drawing {
  if (!isPlainObject(v)) return false;
  const { kind, pts, size, text } = v;
  if (typeof kind !== "string" || !KINDS.has(kind)) return false;
  if (!Array.isArray(pts)) return false;
  const n = pts.length;
  if (kind === "pen") {
    if (n < 4 || n > MAX_PEN_POINTS * 2 || n % 2 !== 0) return false;
  } else if (kind === "text") {
    if (n !== 2) return false;
  } else if (n !== 4) return false;
  for (const p of pts) {
    if (typeof p !== "number" || !Number.isFinite(p) || Math.abs(p) > MAX_DRAW_COORD) return false;
  }
  if (typeof size !== "number" || !Number.isFinite(size) || size <= 0 || size > MAX_DRAW_SIZE) return false;
  if (kind === "text") {
    if (typeof text !== "string" || !VISIBLE.test(text) || runes(text) > MAX_TEXT_RUNES) return false;
  } else if (text !== undefined && text !== "") return false;
  return true;
}

/** A drawing holding only its own keys, `pts` copied. Assumes `d` passed validDrawing. */
function cleanDrawing(d: Drawing): Drawing {
  const out: Drawing = { kind: d.kind, pts: d.pts.slice(), size: d.size };
  if (d.kind === "text") out.text = d.text;
  return out;
}

/** The entries of `v` with a valid id and a valid drawing, each holding only its own keys. `{}` for a non-object. */
export function sanitizeDrawings(v: unknown): Record<string, Drawing> {
  const out: Record<string, Drawing> = {};
  if (!isPlainObject(v)) return out;
  for (const [k, d] of Object.entries(v)) {
    if (validId(k) && validDrawing(d)) out[k] = cleanDrawing(d);
  }
  return out;
}

let idSeq = 0;
/**
 * A fresh id: the millisecond clock in fixed-width base 36 (so a later
 * millisecond's id sorts after an earlier one's, and the layer's id order is
 * the order drawings were made), then a per-page counter (no two calls in one
 * page repeat) and a random tail (two browsers drawing in the same
 * millisecond do not collide).
 */
export function newDrawingId(): string {
  const t = Math.max(0, Math.floor(Date.now())).toString(36).padStart(10, "0");
  const seq = (idSeq++).toString(36).padStart(4, "0");
  let tail = "";
  for (let i = 0; i < 6; i++) tail += Math.floor(Math.random() * 36).toString(36);
  return t + seq + tail;
}

/** Distance from (px, py) to the segment (ax, ay)-(bx, by). */
function segDist(px: number, py: number, ax: number, ay: number, bx: number, by: number): number {
  const dx = bx - ax;
  const dy = by - ay;
  const len2 = dx * dx + dy * dy;
  let t = len2 > 0 ? ((px - ax) * dx + (py - ay) * dy) / len2 : 0;
  t = Math.max(0, Math.min(1, t));
  return Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
}

/**
 * Douglas-Peucker over point indices [0, n): which points to keep so every
 * dropped point lies within `tol` of the segment that replaced it (and so of
 * the returned polyline). Iterative, so a long stroke cannot overflow the stack.
 */
function douglasPeucker(pts: number[], n: number, tol: number): boolean[] {
  const keep = new Array<boolean>(n).fill(false);
  keep[0] = true;
  keep[n - 1] = true;
  const stack: number[] = [0, n - 1];
  while (stack.length) {
    const b = stack.pop()!;
    const a = stack.pop()!;
    let worst = -1;
    let worstD = 0;
    for (let i = a + 1; i < b; i++) {
      const d = segDist(pts[2 * i]!, pts[2 * i + 1]!, pts[2 * a]!, pts[2 * a + 1]!, pts[2 * b]!, pts[2 * b + 1]!);
      if (d > worstD) {
        worstD = d;
        worst = i;
      }
    }
    // a point off by floating-point noise alone is on the line
    if (worst >= 0 && worstD > tol && worstD > 1e-9) {
      keep[worst] = true;
      stack.push(a, worst, worst, b);
    }
  }
  return keep;
}

function collect(pts: number[], keep: boolean[]): number[] {
  const out: number[] = [];
  for (let i = 0; i < keep.length; i++) if (keep[i]) out.push(pts[2 * i]!, pts[2 * i + 1]!);
  return out;
}

/**
 * Thin a flat point list to the points that matter: every dropped point lies
 * within `tolerance` of the returned polyline, the first and last points are
 * kept exactly, and the result holds at most `maxPoints` points. When that
 * tolerance still leaves too many, the kept points are thinned to exactly
 * `maxPoints` by `thinTo`, so a long stroke keeps its shape at the cap
 * instead of collapsing. Never changes its input.
 */
export function simplifyStroke(pts: number[], tolerance: number, maxPoints: number = MAX_PEN_POINTS): number[] {
  const n = Math.floor(pts.length / 2);
  if (n <= 2) return pts.slice();
  const cap = Math.max(2, Math.floor(maxPoints));
  const tol = Number.isFinite(tolerance) && tolerance > 0 ? tolerance : 0;
  const kept = collect(pts, douglasPeucker(pts, n, tol));
  return kept.length / 2 <= cap ? kept : thinTo(kept, cap);
}

/**
 * Drop points from a flat list, least important first, until `cap` are
 * left (the first and last are never dropped). A point's importance is its
 * distance from the segment joining its two neighbours, re-measured as its
 * neighbours go, so the shape's corners outlast its near-straight runs. A
 * binary heap with stale entries skipped keeps it O(n log n).
 */
function thinTo(pts: number[], cap: number): number[] {
  const n = pts.length / 2;
  const prev = new Int32Array(n);
  const next = new Int32Array(n);
  const gone = new Uint8Array(n);
  const stamp = new Int32Array(n);
  for (let i = 0; i < n; i++) {
    prev[i] = i - 1;
    next[i] = i + 1;
  }
  const weight = (i: number) => {
    const a = prev[i]!;
    const b = next[i]!;
    return segDist(pts[2 * i]!, pts[2 * i + 1]!, pts[2 * a]!, pts[2 * a + 1]!, pts[2 * b]!, pts[2 * b + 1]!);
  };
  // heap entries: [weight, index, stamp]
  const heap: Array<[number, number, number]> = [];
  const less = (x: number, y: number) => heap[x]![0] < heap[y]![0];
  const push = (e: [number, number, number]) => {
    heap.push(e);
    let i = heap.length - 1;
    while (i > 0) {
      const p = (i - 1) >> 1;
      if (!less(i, p)) break;
      [heap[i], heap[p]] = [heap[p]!, heap[i]!];
      i = p;
    }
  };
  const pop = () => {
    const top = heap[0]!;
    const last = heap.pop()!;
    if (heap.length) {
      heap[0] = last;
      let i = 0;
      for (;;) {
        const l = 2 * i + 1;
        const r = l + 1;
        let m = i;
        if (l < heap.length && less(l, m)) m = l;
        if (r < heap.length && less(r, m)) m = r;
        if (m === i) break;
        [heap[i], heap[m]] = [heap[m]!, heap[i]!];
        i = m;
      }
    }
    return top;
  };
  for (let i = 1; i < n - 1; i++) push([weight(i), i, 0]);
  let left = n;
  while (left > cap && heap.length) {
    const [, i, s] = pop();
    if (gone[i] || s !== stamp[i]) continue;
    gone[i] = 1;
    left--;
    const a = prev[i]!;
    const b = next[i]!;
    next[a] = b;
    prev[b] = a;
    for (const j of [a, b]) {
      if (j <= 0 || j >= n - 1) continue;
      stamp[j]!++;
      push([weight(j), j, stamp[j]!]);
    }
  }
  const out: number[] = [];
  for (let i = 0; i < n; i++) if (!gone[i]) out.push(pts[2 * i]!, pts[2 * i + 1]!);
  return out;
}

/** Distance from (px, py) to a polyline given as a flat point list. */
function polylineDist(pts: number[], px: number, py: number): number {
  const n = Math.floor(pts.length / 2);
  if (n === 0) return Infinity;
  if (n === 1) return Math.hypot(px - pts[0]!, py - pts[1]!);
  let best = Infinity;
  for (let i = 0; i + 1 < n; i++) {
    best = Math.min(best, segDist(px, py, pts[2 * i]!, pts[2 * i + 1]!, pts[2 * i + 2]!, pts[2 * i + 3]!));
  }
  return best;
}

/**
 * Distance from (px, py), relative to an ellipse's centre, to its outline,
 * radii a, b > 0. The iteration walks a point on the first quadrant's arc
 * towards the nearest one; eight steps stay well inside hitDrawing's bounds
 * (checked against a densely sampled outline for radius ratios up to 1000).
 */
function ellipseDist(px: number, py: number, a: number, b: number): number {
  const x = Math.abs(px);
  const y = Math.abs(py);
  let tx = Math.SQRT1_2;
  let ty = Math.SQRT1_2;
  for (let i = 0; i < 8; i++) {
    const ex = ((a * a - b * b) * tx * tx * tx) / a;
    const ey = ((b * b - a * a) * ty * ty * ty) / b;
    const rx = a * tx - ex;
    const ry = b * ty - ey;
    const qx = x - ex;
    const qy = y - ey;
    const r = Math.hypot(rx, ry);
    const q = Math.hypot(qx, qy);
    if (q === 0) break;
    tx = Math.min(1, Math.max(0, ((qx * r) / q + ex) / a));
    ty = Math.min(1, Math.max(0, ((qy * r) / q + ey) / b));
    const t = Math.hypot(tx, ty);
    if (t === 0) break;
    tx /= t;
    ty /= t;
  }
  return Math.hypot(a * tx - x, b * ty - y);
}

/** Whether world point (x, y) touches the drawing, with reach `tol + size / 2` (outlines only for shapes). */
export function hitDrawing(d: Drawing, x: number, y: number, tol: number): boolean {
  const p = d.pts;
  if (d.kind === "text") {
    const b = drawingBox(d);
    return x >= b.x - tol && x <= b.x + b.w + tol && y >= b.y - tol && y <= b.y + b.h + tol;
  }
  const r = tol + d.size / 2;
  if (d.kind === "pen") return polylineDist(p, x, y) <= r;
  const [x1, y1, x2, y2] = p as [number, number, number, number];
  if (d.kind === "line") return segDist(x, y, x1, y1, x2, y2) <= r;
  const l = Math.min(x1, x2);
  const rr = Math.max(x1, x2);
  const t = Math.min(y1, y2);
  const bo = Math.max(y1, y2);
  if (d.kind === "rect") {
    const dist = Math.min(
      segDist(x, y, l, t, rr, t),
      segDist(x, y, rr, t, rr, bo),
      segDist(x, y, rr, bo, l, bo),
      segDist(x, y, l, bo, l, t),
    );
    return dist <= r;
  }
  // ellipse
  const cx = (l + rr) / 2;
  const cy = (t + bo) / 2;
  const a = (rr - l) / 2;
  const b = (bo - t) / 2;
  if (a === 0 || b === 0) return segDist(x, y, l, t, rr, bo) <= r;
  return ellipseDist(x - cx, y - cy, a, b) <= r;
}

/** The drawing's bounding box, stroke width not included. */
export function drawingBox(d: Drawing): DrawBox {
  const p = d.pts;
  if (d.kind === "text") {
    const lines = (d.text ?? "").split("\n");
    let longest = 0;
    for (const l of lines) longest = Math.max(longest, runes(l));
    return { x: p[0]!, y: p[1]!, w: longest * d.size * 0.6, h: lines.length * d.size * 1.25 };
  }
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (let i = 0; i + 1 < p.length; i += 2) {
    x0 = Math.min(x0, p[i]!);
    x1 = Math.max(x1, p[i]!);
    y0 = Math.min(y0, p[i + 1]!);
    y1 = Math.max(y1, p[i + 1]!);
  }
  if (!Number.isFinite(x0)) return { x: 0, y: 0, w: 0, h: 0 };
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

/** SVG path data for a flat point list: `M x0 y0 L x1 y1 …`. */
export function penPath(pts: number[]): string {
  let s = "";
  for (let i = 0; i + 1 < pts.length; i += 2) {
    s += (i === 0 ? "M " : " L ") + String(pts[i]) + " " + String(pts[i + 1]);
  }
  return s;
}

/** The ops this browser queued that the server has not accepted, or none. */
export function readUnsavedDrawOps(): DrawOps {
  const none: DrawOps = { set: {}, remove: [] };
  const raw = readLS(DRAW_UNSAVED_KEY);
  if (!raw) return none;
  try {
    const v: unknown = JSON.parse(raw);
    if (!isPlainObject(v)) return none;
    const { set, remove } = v;
    if (set !== undefined && !isPlainObject(set)) return none;
    if (remove !== undefined && !Array.isArray(remove)) return none;
    return {
      set: sanitizeDrawings(set ?? {}),
      remove: ((remove as unknown[] | undefined) ?? []).filter(validId),
    };
  } catch {
    return none;
  }
}

/** Keep `ops` under DRAW_UNSAVED_KEY, or remove the key when they are empty. Never throws. */
export function writeUnsavedDrawOps(ops: DrawOps): void {
  try {
    const empty = Object.keys(ops.set).length === 0 && ops.remove.length === 0;
    writeLS(DRAW_UNSAVED_KEY, empty ? null : JSON.stringify({ set: ops.set, remove: ops.remove }));
  } catch {
    // an op set that cannot be stringified is not worth breaking the map over
  }
}
