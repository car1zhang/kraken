// The agent map's overview in its bottom-left corner: every card as a small
// rectangle in its status colour, and the part of the plane the map is
// showing as an outline. The geometry (`miniFrame`, `miniToWorld`) is pure,
// so a press on the overview can be turned back into a world point without a
// canvas; the drawing (`drawMinimap`) is the only part that needs one.

import { hueOf, type Palette } from "./draw.ts";
import type { MapNode } from "./layout.ts";

/** World to minimap pixels: `px = (x - ox) * scale`, `py = (y - oy) * scale`. */
export interface MiniFrame {
  scale: number;
  ox: number;
  oy: number;
}

const fin = (n: number) => typeof n === "number" && Number.isFinite(n);

/**
 * The frame that fits every card AND the visible world rectangle `view`
 * (top-left corner and size, world units) into a minimap of `size` pixels,
 * `pad` pixels in from every edge, at the largest scale that fits, centred.
 * A node with a non-finite coordinate is left out (a non-finite size counts
 * as zero), a non-finite view is left out, and with nothing left the box is
 * the origin. The scale is always finite and positive: a box with no extent
 * in one direction is fitted by the other, and one with none at all, or a
 * size with no room inside its padding, falls back to a scale of 1.
 */
export function miniFrame(
  nodes: readonly MapNode[],
  view: { x: number; y: number; w: number; h: number },
  size: { w: number; h: number },
  pad = 6,
): MiniFrame {
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  const take = (x: number, y: number, w: number, h: number) => {
    const ww = fin(w) && w > 0 ? w : 0;
    const hh = fin(h) && h > 0 ? h : 0;
    if (x < x0) x0 = x;
    if (y < y0) y0 = y;
    if (x + ww > x1) x1 = x + ww;
    if (y + hh > y1) y1 = y + hh;
  };
  if (fin(view.x) && fin(view.y)) take(view.x, view.y, view.w, view.h);
  for (const n of nodes) if (fin(n.x) && fin(n.y)) take(n.x, n.y, n.w, n.h);
  if (!(x1 >= x0) || !(y1 >= y0)) {
    x0 = y0 = x1 = y1 = 0;
  }
  const p = fin(pad) && pad > 0 ? pad : 0;
  const sw = fin(size.w) && size.w > 0 ? size.w : 0;
  const sh = fin(size.h) && size.h > 0 ? size.h : 0;
  const aw = sw - 2 * p;
  const ah = sh - 2 * p;
  const bw = x1 - x0;
  const bh = y1 - y0;
  const kx = bw > 0 && aw > 0 ? aw / bw : Infinity;
  const ky = bh > 0 && ah > 0 ? ah / bh : Infinity;
  let scale = Math.min(kx, ky);
  if (!(scale > 0) || !Number.isFinite(scale)) scale = 1;
  return {
    scale,
    ox: (x0 + x1) / 2 - sw / 2 / scale,
    oy: (y0 + y1) / 2 - sh / 2 / scale,
  };
}

/** The world point under minimap pixel (px, py): the inverse of the frame's mapping. */
export function miniToWorld(px: number, py: number, f: MiniFrame): [number, number] {
  return [px / f.scale + f.ox, py / f.scale + f.oy];
}

/**
 * Draw the overview: a backdrop, every placed card as a rectangle in its
 * status colour (draw.ts' `hueOf`, the palette's recipe for each status, so
 * the overview and the characters share one mapping; never smaller than a
 * pixel and a half, so a far-off card
 * still shows), and the visible area as an outline. `c`'s transform is the
 * caller's (the canvas' backing scale); everything here is in CSS pixels.
 */
export function drawMinimap(
  c: CanvasRenderingContext2D,
  C: Palette,
  nodes: readonly MapNode[],
  statusOf: (key: string) => string,
  view: { x: number; y: number; w: number; h: number },
  f: MiniFrame,
  size: { w: number; h: number },
): void {
  c.clearRect(0, 0, size.w, size.h);
  c.globalAlpha = 0.86;
  c.fillStyle = C.b0;
  c.fillRect(0, 0, size.w, size.h);
  c.globalAlpha = 1;
  for (const n of nodes) {
    if (!fin(n.x) || !fin(n.y)) continue;
    const w = Math.max(1.5, (fin(n.w) ? n.w : 0) * f.scale);
    const h = Math.max(1.5, (fin(n.h) ? n.h : 0) * f.scale);
    c.fillStyle = hueOf(C, statusOf(n.key));
    c.fillRect((n.x - f.ox) * f.scale, (n.y - f.oy) * f.scale, w, h);
  }
  if (fin(view.x) && fin(view.y) && fin(view.w) && fin(view.h)) {
    c.strokeStyle = C.fg;
    c.lineWidth = 1;
    c.strokeRect(
      Math.round((view.x - f.ox) * f.scale) + 0.5,
      Math.round((view.y - f.oy) * f.scale) + 0.5,
      Math.max(1, Math.round(view.w * f.scale) - 1),
      Math.max(1, Math.round(view.h * f.scale) - 1),
    );
  }
}
