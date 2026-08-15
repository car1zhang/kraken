// entities.ts — the isoscene renderer's entity vocabulary: one designed
// isometric silhouette per SHAPE and PART class in the frozen grammar
// (50-contract.md). render.ts owns the `<g data-entity>` envelope and every
// attribute the contract pins (R2/R3/R5); everything in here is the body
// INSIDE that envelope, which the contract leaves to this slice under
// "renderer freedom". Nothing here is imported by a test — the file is
// internal by the contract's own ownership table.
//
// Three rules bind every emitter, and each is a contract line rather than a
// preference. Paint goes through CSS custom properties with a dark-HUD
// fallback (R9), so the board stylesheet can retheme a compiled scene without
// the compiler knowing which theme is live. SMIL is emitted only when the
// caller passes `motion: true` (R6), and always at its resting frame — an
// opacity animation's `values` list starts at `1` and every `begin` is `0s` or
// negative (R8) — so the reduced-motion output is the same picture, held
// still, never a picture missing its lit parts. And no emitter writes a URL or
// a `<script>`: R9 forbids both, which is also why the svg element render.ts
// emits carries no `xmlns` (that attribute's value is an `http://` URL).

import type { EntityNode, IsoPoint, Treatment } from "./types.ts";

/** Projects a card point (with an optional z lift) into scene screen space.
 *  render.ts builds this from the ProjectedScene's own rigid global offset, so
 *  an entity body never re-derives the projection for itself. */
export type Proj = (x: number, y: number, z?: number) => IsoPoint;

export interface EntityCtx {
  node: EntityNode;
  pt: Proj;
  /** Unique per node: the only safe prefix for any id this body mints. */
  uid: string;
  /** False under reduced motion — no SMIL element may be emitted at all. */
  motion: boolean;
}

// ---------------------------------------------------------------- palette --

/** Every paint value is a custom property with a dark-HUD fallback (R9). The
 *  fallbacks are the gallery mockup's values, so an unstyled host document
 *  still renders the intended scene rather than black-on-black. */
export const C = {
  top: "var(--iso-face-top, #2c3f68)",
  left: "var(--iso-face-left, #20304f)",
  right: "var(--iso-face-right, #16223b)",
  stroke: "var(--iso-stroke, #46619a)",
  floor: "var(--iso-floor, #121a2b)",
  floorLine: "var(--iso-floor-line, #1e2c49)",
  panel: "var(--iso-panel, #0f1522)",
  panelLine: "var(--iso-panel-line, #1c2740)",
  ink: "var(--iso-ink, #dce6f5)",
  muted: "var(--iso-muted, #8b9bb8)",
  faint: "var(--iso-faint, #5a6a88)",
  accent: "var(--iso-accent, #59d8ff)",
  accentDim: "var(--iso-accent-dim, #2c6f8a)",
  spark: "var(--iso-spark, #ffd166)",
  screen: "var(--iso-screen, #0b1220)",
  shadow: "var(--iso-shadow, #05070c)",
} as const;

/** The six semantic treatments. Same sixteen-hex discipline as the job palette
 *  elsewhere in kraken: the hex is a fallback, the property is the hook. */
export const TREAT: Record<Treatment, string> = {
  grey: "var(--iso-grey, #8a97ad)",
  red: "var(--iso-red, #ff5d6e)",
  yellow: "var(--iso-yellow, #ffc857)",
  green: "var(--iso-green, #4ade80)",
  blue: "var(--iso-blue, #5aa2ff)",
  purple: "var(--iso-purple, #c084fc)",
};

/** One glyph per treatment, carried above the entity so the six read apart
 *  when the viewer is colour-blind or the scene is printed grey. */
export const TREAT_GLYPH: Record<Treatment, string> = {
  grey: "∅",
  red: "✗",
  yellow: "⚠",
  green: "✓",
  blue: "+",
  purple: "★",
};

// ------------------------------------------------------------- primitives --

/** Two decimals is below one screen pixel at every viewBox this compiler
 *  emits, and keeps the markup a test can read. */
export function n(v: number): string {
  if (!Number.isFinite(v)) return "0";
  const r = Math.round(v * 100) / 100;
  return Object.is(r, -0) ? "0" : String(r);
}

/** Markup escape for emitted attribute values and text. Deliberately NOT
 *  lib/markdown.ts's esc: that one takes unknown and escapes for a context
 *  where `'` matters; this emitter writes only double-quoted attributes and
 *  element text, and the narrower signature keeps a non-string from sneaking
 *  into markup as "[object Object]". */
export function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** The top face's local space: local (0,0) is the footprint's card origin at
 *  height z, local +x runs along the card's x axis, local +y along its y. */
export function faceTop(pt: Proj, x: number, y: number, z: number): string {
  const p = pt(x, y, z);
  return `matrix(1 .5 -1 .5 ${n(p.sx)} ${n(p.sy)})`;
}

/** The x-extent side wall's local space, anchored at the top diamond's LEFT
 *  corner — one of the two NEAR edges, the ones facing the viewer. Local +y
 *  is screen-down, which is exactly a drop in z. */
export function faceLeft(pt: Proj, x: number, y: number, h: number, z: number): string {
  const p = pt(x, y + h, z);
  return `matrix(1 .5 0 1 ${n(p.sx)} ${n(p.sy)})`;
}

/** The y-extent side wall's local space, anchored at the top diamond's RIGHT
 *  corner — the other near edge. */
export function faceRight(pt: Proj, x: number, y: number, w: number, z: number): string {
  const p = pt(x + w, y, z);
  return `matrix(-1 .5 0 1 ${n(p.sx)} ${n(p.sy)})`;
}

export interface FacePaint {
  top: string;
  left: string;
  right: string;
  stroke: string;
  dash?: string;
}

/** A solid box standing on the floor: the top diamond plus the two walls that
 *  hang from its NEAR edges (the pair meeting at the corner closest to the
 *  viewer). Hanging them from the FAR edges instead produces a box that reads
 *  as inside-out — the gallery mockup shipped that way and was corrected, so
 *  the two matrices below are load-bearing, not decorative.
 *
 *  `zBase` lifts the whole solid off the floor for the entities that hover. */
export function prism(
  pt: Proj,
  x: number,
  y: number,
  w: number,
  h: number,
  hz: number,
  paint: FacePaint,
  zBase = 0,
): string {
  const zTop = zBase + hz;
  const dash = paint.dash ? ` stroke-dasharray="${paint.dash}"` : "";
  const edge = ` stroke="${paint.stroke}" stroke-width="1.1"${dash}`;
  return (
    `<rect width="${n(w)}" height="${n(hz)}" transform="${faceLeft(pt, x, y, h, zTop)}" fill="${paint.left}"${edge}/>` +
    `<rect width="${n(h)}" height="${n(hz)}" transform="${faceRight(pt, x, y, w, zTop)}" fill="${paint.right}"${edge}/>` +
    `<rect width="${n(w)}" height="${n(h)}" transform="${faceTop(pt, x, y, zTop)}" fill="${paint.top}"${edge}/>`
  );
}

/** The footprint as a flat diamond at height z — floors, plates and zones. */
export function diamond(
  pt: Proj,
  x: number,
  y: number,
  w: number,
  h: number,
  z: number,
  attrs: string,
): string {
  const a = pt(x, y, z);
  const b = pt(x + w, y, z);
  const c = pt(x + w, y + h, z);
  const d = pt(x, y + h, z);
  return (
    `<polygon points="${n(a.sx)},${n(a.sy)} ${n(b.sx)},${n(b.sy)} ` +
    `${n(c.sx)},${n(c.sy)} ${n(d.sx)},${n(d.sy)}" ${attrs}/>`
  );
}

// ------------------------------------------------------------------- SMIL --

/** R8: the resting frame is the fully-visible one, so every opacity values
 *  list opens at 1 and closes back on it. Freezing the scene therefore loses
 *  the motion and nothing else. */
export function blink(dur: string, begin: string): string {
  return `<animate attributeName="opacity" values="1;0.14;1" dur="${dur}" begin="${begin}" repeatCount="indefinite"/>`;
}

export function breathe(dur: string, begin: string): string {
  return `<animate attributeName="opacity" values="1;0.38;1" dur="${dur}" begin="${begin}" repeatCount="indefinite"/>`;
}

/** A hover: animateMotion over a there-and-back path rather than an
 *  animateTransform, so the only SMIL element kinds this compiler emits are
 *  `<animate>` and `<animateMotion>` — the exact two R8 legislates. */
export function bob(lift: number, dur: string, begin: string): string {
  return `<animateMotion path="M 0,0 L 0,${n(-lift)} L 0,0" dur="${dur}" begin="${begin}" repeatCount="indefinite"/>`;
}

/** A status light: a soft halo plus a core, so glow survives without a
 *  `filter` (whose drop-shadow colour could not go through a theme hook). */
export function lamp(
  cx: number,
  cy: number,
  r: number,
  colour: string,
  motion: boolean,
  dur = "2.1s",
  begin = "0s",
): string {
  return (
    `<g>${motion ? blink(dur, begin) : ""}` +
    `<circle cx="${n(cx)}" cy="${n(cy)}" r="${n(r * 2.7)}" fill="${colour}" opacity="0.16"/>` +
    `<circle cx="${n(cx)}" cy="${n(cy)}" r="${n(r)}" fill="${colour}"/></g>`
  );
}

// `C` is `as const`, so every default below is annotated `string` on purpose:
// without it the literal type of the default narrows the parameter and no
// other palette entry can be passed.
function etch(x1: number, y1: number, x2: number, y2: number, colour: string = C.faint): string {
  return `<line x1="${n(x1)}" y1="${n(y1)}" x2="${n(x2)}" y2="${n(y2)}" stroke="${colour}" stroke-width="1"/>`;
}

function box(
  x: number,
  y: number,
  w: number,
  h: number,
  fill: string,
  stroke: string = C.stroke,
  extra: string = "",
): string {
  return `<rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" fill="${fill}" stroke="${stroke}" stroke-width="1"${extra}/>`;
}

/** A card-space circle projects to an axis-aligned screen ellipse: substitute
 *  (cx+r·cosθ, cy+r·sinθ) into the projection and the offsets collapse to
 *  r√2·cos(θ+45°) across and (r√2/2)·sin(θ+45°) down. Verified algebraically
 *  this session against P1's formula; it is why silos and shadows need no
 *  rotation. */
function ellipseAt(p: IsoPoint, r: number, attrs: string): string {
  return `<ellipse cx="${n(p.sx)}" cy="${n(p.sy)}" rx="${n(r * 1.4142)}" ry="${n(r * 0.7071)}" ${attrs}/>`;
}

function shadow(pt: Proj, x: number, y: number, w: number, h: number): string {
  const c = pt(x + w / 2, y + h / 2, 0);
  return ellipseAt(c, Math.min(w, h) * 0.34, `fill="${C.shadow}" opacity="0.4"`);
}

// ----------------------------------------------------------- height policy --

/** Per-kind standing height in CARD units, so an entity's bulk stays in
 *  proportion to the footprint the card author drew. */
const KIND_HEIGHT: Record<string, number> = {
  proc: 54, cli: 30, thread: 4, fn: 24, agent: 28, llm: 6, timer: 44,
  iface: 38, event: 6, ui: 40, store: 44, table: 18, file: 6, queue: 14,
  type: 38, pkg: 42, machine: 18, cloud: 6, node: 28,
  actor: 6, decision: 34, pill: 5, cell: 10, step: 6, badge: 6, bar: 46,
};

/** group/region/lane are floor structures — they have no standing height at
 *  all, and clamping them up to a minimum would lift a district off its own
 *  floor. */
const FLAT = new Set(["group", "region", "lane"]);

export function bodyHeight(node: EntityNode): number {
  if (FLAT.has(node.shape)) return 0;
  const base = KIND_HEIGHT[node.shape] ?? 28;
  // A box the card nested other boxes inside renders as a low platform: at
  // full height its solid would swallow exactly the children it holds.
  const wanted = node.holds || node.children.length > 0 ? Math.min(base, 8) : base;
  const room = Math.max(4, Math.min(node.width, node.height) * 0.85);
  return Math.max(3, Math.min(wanted, room));
}

/** The faces an entity paints with. `grey` is the one treatment that changes
 *  the MATERIAL rather than adding a light: missing/removed reads as a dotted
 *  wireframe with no fill, which is also where R5's stroke-dasharray comes
 *  from. */
export function facePaint(node: EntityNode): FacePaint {
  if (node.treatment === "grey") {
    return { top: "none", left: "none", right: "none", stroke: TREAT.grey, dash: "3 3" };
  }
  return {
    top: C.top,
    left: C.left,
    right: C.right,
    stroke: node.treatment ? TREAT[node.treatment] : C.stroke,
  };
}

/** True when the entity may carry lit detail. A ghost frame carries none —
 *  a lamp on a box that is not there reads as a box that is. */
function lit(node: EntityNode): boolean {
  return node.treatment !== "grey";
}

// ------------------------------------------------------------- emitters ----

interface Geo {
  x: number;
  y: number;
  w: number;
  h: number;
  cx: number;
  cy: number;
  hz: number;
}

function geo(ctx: EntityCtx): Geo {
  const { x, y, width: w, height: h } = ctx.node;
  return { x, y, w, h, cx: x + w / 2, cy: y + h / 2, hz: bodyHeight(ctx.node) };
}

/** proc — server tower: a tall rack whose slots and status LEDs sit on the
 *  wall facing the viewer. */
function eProc(ctx: EntityCtx): string {
  const g = geo(ctx);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, facePaint(ctx.node));
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceLeft(ctx.pt, g.x, g.y, g.h, g.hz)}">`;
  for (let i = 1; i <= 3; i++) out += etch(g.w * 0.12, (g.hz * i) / 5, g.w * 0.88, (g.hz * i) / 5);
  out += lamp(g.w * 0.16, g.hz * 0.85, 2.2, C.accent, ctx.motion);
  out += lamp(g.w * 0.28, g.hz * 0.85, 2.2, TREAT.green, ctx.motion, "2.9s", "-0.7s");
  return out + `</g>`;
}

/** cli — terminal kiosk: a plinth carrying a screen that shows a prompt. */
function eCli(ctx: EntityCtx): string {
  const g = geo(ctx);
  const deskH = Math.max(4, g.hz * 0.3);
  const sx = g.x + g.w * 0.14;
  const sy = g.y + g.h * 0.2;
  const sw = g.w * 0.6;
  const sh = g.h * 0.2;
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, deskH, facePaint(ctx.node));
  out += prism(ctx.pt, sx, sy, sw, sh, g.hz, facePaint(ctx.node), deskH);
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceLeft(ctx.pt, sx, sy, sh, deskH + g.hz)}">`;
  out += box(sw * 0.08, g.hz * 0.12, sw * 0.84, g.hz * 0.7, C.screen, C.panelLine);
  out += `<text x="${n(sw * 0.16)}" y="${n(g.hz * 0.55)}" fill="${C.accent}" font-size="${n(g.hz * 0.3)}" font-family="ui-monospace, monospace">&gt;_</text>`;
  out += `</g>`;
  return out;
}

/** thread — loop track: a closed circuit on the floor with a runner on it. */
function eThread(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, 0);
  const r = Math.min(g.w, g.h) * 0.42;
  const rx = r * 1.4142;
  const ry = r * 0.7071;
  const loop = `M ${n(c.sx - rx)},${n(c.sy)} a ${n(rx)},${n(ry)} 0 1,0 ${n(rx * 2)},0 a ${n(rx)},${n(ry)} 0 1,0 ${n(-rx * 2)},0`;
  const col = ctx.node.treatment ? TREAT[ctx.node.treatment] : C.accentDim;
  let out = `<path d="${loop}" fill="none" stroke="${col}" stroke-width="2" stroke-dasharray="7 5"/>`;
  out += ellipseAt(c, r * 0.55, `fill="none" stroke="${C.floorLine}" stroke-width="1"`);
  if (lit(ctx.node) && ctx.motion) {
    out += `<circle r="3.6" fill="${C.accent}"><animateMotion path="${loop}" dur="4s" begin="0s" repeatCount="indefinite"/></circle>`;
    out += `<circle r="2.6" fill="${C.accent}" opacity="0.4"><animateMotion path="${loop}" dur="4s" begin="-1s" repeatCount="indefinite"/></circle>`;
  } else if (lit(ctx.node)) {
    out += `<circle cx="${n(c.sx - rx)}" cy="${n(c.sy)}" r="3.6" fill="${C.accent}"/>`;
  }
  return out;
}

/** fn — gated mill: a bed between two rails, work passing through. */
function eFn(ctx: EntityCtx): string {
  const g = geo(ctx);
  const paint = facePaint(ctx.node);
  const railW = Math.max(3, g.w * 0.06);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, paint);
  out += prism(ctx.pt, g.x + g.w * 0.16, g.y, railW, g.h, g.hz * 1.5, paint, g.hz);
  out += prism(ctx.pt, g.x + g.w * 0.74, g.y, railW, g.h, g.hz * 1.5, paint, g.hz);
  if (!lit(ctx.node)) return out;
  const c = ctx.pt(g.cx, g.cy, g.hz);
  out += lamp(c.sx, c.sy, 2.4, C.accent, ctx.motion, "1.8s", "0s");
  return out;
}

/** agent — hover droid: a lifted chassis with eye lamps and the gold sparkle
 *  that marks anything model-powered. */
function eAgent(ctx: EntityCtx): string {
  const g = geo(ctx);
  const lift = Math.max(8, g.hz * 0.5);
  let out = shadow(ctx.pt, g.x, g.y, g.w, g.h);
  out += `<g>${ctx.motion ? bob(4, "2.6s", "0s") : ""}`;
  out += prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, facePaint(ctx.node), lift);
  if (lit(ctx.node)) {
    out += `<g transform="${faceLeft(ctx.pt, g.x, g.y, g.h, lift + g.hz)}">`;
    out += `<rect x="${n(g.w * 0.22)}" y="${n(g.hz * 0.3)}" width="${n(g.w * 0.16)}" height="${n(g.hz * 0.16)}" fill="${C.accent}"/>`;
    out += `<rect x="${n(g.w * 0.58)}" y="${n(g.hz * 0.3)}" width="${n(g.w * 0.16)}" height="${n(g.hz * 0.16)}" fill="${C.accent}"/>`;
    out += `</g>`;
    const top = ctx.pt(g.cx, g.cy, lift + g.hz);
    out += `<line x1="${n(top.sx)}" y1="${n(top.sy)}" x2="${n(top.sx)}" y2="${n(top.sy - 12)}" stroke="${C.accentDim}" stroke-width="1.2"/>`;
    out += lamp(top.sx, top.sy - 14, 2.4, C.accent, ctx.motion, "1.6s", "0s");
    out += sparkle(top.sx - Math.min(30, g.w * 0.5), top.sy - 6, 9, ctx.motion);
  }
  return out + `</g>`;
}

/** The gold sparkle — one model call, wherever it appears. */
function sparkle(cx: number, cy: number, r: number, motion: boolean, dur = "2.4s"): string {
  const q = r * 0.24;
  const d =
    `M ${n(cx)},${n(cy - r)} Q ${n(cx + q)},${n(cy - q)} ${n(cx + r)},${n(cy)} ` +
    `Q ${n(cx + q)},${n(cy + q)} ${n(cx)},${n(cy + r)} ` +
    `Q ${n(cx - q)},${n(cy + q)} ${n(cx - r)},${n(cy)} ` +
    `Q ${n(cx - q)},${n(cy - q)} ${n(cx)},${n(cy - r)} Z`;
  return (
    `<g>${motion ? breathe(dur, "0s") : ""}` +
    `<circle cx="${n(cx)}" cy="${n(cy)}" r="${n(r * 1.9)}" fill="${C.spark}" opacity="0.08"/>` +
    `<path d="${d}" fill="${C.spark}"/></g>`
  );
}

/** llm — the sparkle itself, at entity scale. */
function eLlm(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, Math.max(14, g.hz * 3));
  const r = Math.max(10, Math.min(g.w, g.h) * 0.45);
  let out = shadow(ctx.pt, g.x, g.y, g.w, g.h);
  out += `<g>${ctx.motion ? bob(5, "3.4s", "-0.5s") : ""}`;
  out += sparkle(c.sx, c.sy, r, ctx.motion, "2.8s");
  out += sparkle(c.sx + r * 0.95, c.sy + r * 0.6, r * 0.3, ctx.motion, "2.1s");
  return out + `</g>`;
}

/** timer — clock pylon: a post carrying a dial. Deliberately no rotating
 *  hand: animateTransform is the one SMIL kind this compiler never emits, so
 *  R8's two rules cover the whole surface. */
function eTimer(ctx: EntityCtx): string {
  const g = geo(ctx);
  const postW = Math.max(3, g.w * 0.1);
  let out = prism(ctx.pt, g.cx - postW / 2, g.cy - postW / 2, postW, postW, g.hz, facePaint(ctx.node));
  const c = ctx.pt(g.cx, g.cy, g.hz + 14);
  const r = Math.max(9, Math.min(g.w, g.h) * 0.3);
  out += `<circle cx="${n(c.sx)}" cy="${n(c.sy)}" r="${n(r)}" fill="${C.panel}" stroke="${facePaint(ctx.node).stroke}" stroke-width="1.5"/>`;
  for (const [dx, dy] of [[0, -1], [0, 1], [-1, 0], [1, 0]] as const) {
    out += etch(c.sx + dx * r * 0.78, c.sy + dy * r * 0.78, c.sx + dx * r * 0.95, c.sy + dy * r * 0.95);
  }
  if (!lit(ctx.node)) return out;
  out += `<line x1="${n(c.sx)}" y1="${n(c.sy)}" x2="${n(c.sx)}" y2="${n(c.sy - r * 0.65)}" stroke="${C.muted}" stroke-width="2" stroke-linecap="round"/>`;
  out += `<g>${ctx.motion ? blink("1.4s", "0s") : ""}<line x1="${n(c.sx)}" y1="${n(c.sy)}" x2="${n(c.sx + r * 0.55)}" y2="${n(c.sy)}" stroke="${C.accent}" stroke-width="2" stroke-linecap="round"/></g>`;
  return out;
}

/** iface — port gate: a wall with a lit porthole, the only way through. */
function eIface(ctx: EntityCtx): string {
  const g = geo(ctx);
  const gateW = Math.max(4, g.w * 0.16);
  let out = prism(ctx.pt, g.cx - gateW / 2, g.y, gateW, g.h, g.hz, facePaint(ctx.node));
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceRight(ctx.pt, g.cx - gateW / 2, g.y, gateW, g.hz)}">`;
  out += etch(g.h * 0.12, g.hz * 0.26, g.h * 0.88, g.hz * 0.26, C.accentDim);
  out += etch(g.h * 0.12, g.hz * 0.74, g.h * 0.88, g.hz * 0.74, C.accentDim);
  out += `</g>`;
  const port = ctx.pt(g.cx, g.cy, g.hz * 0.55);
  out += `<circle cx="${n(port.sx)}" cy="${n(port.sy)}" r="7" fill="${C.panel}" stroke="${C.accentDim}" stroke-width="1.5"/>`;
  out += lamp(port.sx, port.sy, 2.6, C.accent, ctx.motion, "2.4s", "0s");
  return out;
}

/** event — signal dart: a payload in flight, with speed lines behind it. */
function eEvent(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, Math.max(12, g.hz * 2.5));
  const r = Math.max(9, Math.min(g.w, g.h) * 0.4);
  const col = ctx.node.treatment ? TREAT[ctx.node.treatment] : C.stroke;
  let out = shadow(ctx.pt, g.x, g.y, g.w, g.h);
  out += `<g>${ctx.motion ? bob(4, "2.4s", "0s") : ""}`;
  out += `<path d="M ${n(c.sx - r * 1.5)},${n(c.sy - r * 0.6)} h ${n(r * 1.9)} l ${n(r * 0.9)},${n(r * 0.6)} l ${n(-r * 0.9)},${n(r * 0.6)} h ${n(-r * 1.9)} z" fill="${C.left}" stroke="${col}" stroke-width="1.3"/>`;
  if (lit(ctx.node)) {
    out += `<path d="M ${n(c.sx + r * 0.5)},${n(c.sy - r * 0.3)} l ${n(r * 0.55)},${n(r * 0.3)} l ${n(-r * 0.55)},${n(r * 0.3)} z" fill="${C.accent}"/>`;
    out += `<g>${ctx.motion ? breathe("1.8s", "0s") : ""}` +
      etch(c.sx - r * 2.5, c.sy - r * 0.5, c.sx - r * 1.7, c.sy - r * 0.5, C.accentDim) +
      etch(c.sx - r * 2.9, c.sy + r * 0.1, c.sx - r * 1.9, c.sy + r * 0.1, C.accentDim) +
      `</g>`;
  }
  return out + `</g>`;
}

/** ui — billboard screen: a stand carrying a lit panel with window chrome. */
function eUi(ctx: EntityCtx): string {
  const g = geo(ctx);
  const baseH = Math.max(3, g.hz * 0.14);
  const px = g.x + g.w * 0.08;
  const py = g.y + g.h * 0.34;
  const pw = g.w * 0.84;
  const ph = Math.max(2, g.h * 0.14);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, baseH, facePaint(ctx.node));
  out += prism(ctx.pt, px, py, pw, ph, g.hz, facePaint(ctx.node), baseH);
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceLeft(ctx.pt, px, py, ph, baseH + g.hz)}">`;
  out += box(pw * 0.04, g.hz * 0.08, pw * 0.92, g.hz * 0.82, C.screen, C.panelLine);
  out += box(pw * 0.04, g.hz * 0.08, pw * 0.92, g.hz * 0.16, C.left, C.panelLine);
  out += `<rect x="${n(pw * 0.09)}" y="${n(g.hz * 0.12)}" width="${n(pw * 0.04)}" height="${n(g.hz * 0.08)}" fill="${TREAT.red}" opacity="0.8"/>`;
  out += `<rect x="${n(pw * 0.16)}" y="${n(g.hz * 0.12)}" width="${n(pw * 0.04)}" height="${n(g.hz * 0.08)}" fill="${TREAT.yellow}" opacity="0.8"/>`;
  out += `<rect x="${n(pw * 0.23)}" y="${n(g.hz * 0.12)}" width="${n(pw * 0.04)}" height="${n(g.hz * 0.08)}" fill="${TREAT.green}" opacity="0.8"/>`;
  out += `<rect x="${n(pw * 0.09)}" y="${n(g.hz * 0.35)}" width="${n(pw * 0.38)}" height="${n(g.hz * 0.14)}" fill="${C.top}"/>`;
  out += `<rect x="${n(pw * 0.09)}" y="${n(g.hz * 0.58)}" width="${n(pw * 0.6)}" height="${n(g.hz * 0.08)}" fill="${C.top}"/>`;
  out += `<g>${ctx.motion ? breathe("3s", "0s") : ""}<rect x="${n(pw * 0.04)}" y="${n(g.hz * 0.08)}" width="${n(pw * 0.92)}" height="${n(g.hz * 0.82)}" fill="${C.accent}" opacity="0.14"/></g>`;
  return out + `</g>`;
}

/** store — data silo: the cylinder, with a lit fill band. */
function eStore(ctx: EntityCtx): string {
  const g = geo(ctx);
  const r = Math.min(g.w, g.h) * 0.46;
  const rx = r * 1.4142;
  const ry = r * 0.7071;
  const top = ctx.pt(g.cx, g.cy, g.hz);
  const paint = facePaint(ctx.node);
  const wall = paint.dash ? ` stroke-dasharray="${paint.dash}"` : "";
  const body =
    `M ${n(top.sx - rx)},${n(top.sy)} V ${n(top.sy + g.hz)} ` +
    `A ${n(rx)} ${n(ry)} 0 0 0 ${n(top.sx + rx)},${n(top.sy + g.hz)} V ${n(top.sy)}`;
  let out = `<path d="${body}" fill="${paint.left}" stroke="${paint.stroke}" stroke-width="1.3"${wall}/>`;
  out += ellipseAt(top, r, `fill="${paint.top}" stroke="${paint.stroke}" stroke-width="1.3"${wall}`);
  if (!lit(ctx.node)) return out;
  const band = top.sy + g.hz * 0.55;
  out += `<g>${ctx.motion ? breathe("2.2s", "0s") : ""}<path d="M ${n(top.sx - rx)},${n(band)} A ${n(rx)} ${n(ry)} 0 0 0 ${n(top.sx + rx)},${n(band)}" fill="none" stroke="${C.accent}" stroke-width="1.4"/></g>`;
  out += lamp(top.sx, top.sy, 2.6, C.accent, ctx.motion, "2.6s", "0s");
  return out;
}

/** table — card rack: a low slab with a header band and a column rule. */
function eTable(ctx: EntityCtx): string {
  const g = geo(ctx);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, facePaint(ctx.node));
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceTop(ctx.pt, g.x, g.y, g.hz)}">`;
  out += box(0, 0, g.w, g.h * 0.24, C.top);
  out += etch(0, g.h * 0.62, g.w, g.h * 0.62);
  out += etch(g.w * 0.5, g.h * 0.24, g.w * 0.5, g.h);
  out += `<rect x="${n(g.w * 0.04)}" y="${n(g.h * 0.32)}" width="${n(g.w * 0.4)}" height="${n(g.h * 0.24)}" fill="${C.accent}" opacity="0.14"/>`;
  return out + `</g>`;
}

/** file — standing document: a sheet with a folded corner and rule lines. */
function eFile(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, 0);
  const w = Math.max(20, Math.min(g.w, g.h) * 0.8);
  const hh = w * 1.35;
  const l = c.sx - w / 2;
  const t = c.sy - hh;
  const fold = w * 0.32;
  const paint = facePaint(ctx.node);
  let out = shadow(ctx.pt, g.x, g.y, g.w, g.h);
  out += `<path d="M ${n(l)},${n(t)} h ${n(w - fold)} l ${n(fold)},${n(fold)} v ${n(hh - fold)} h ${n(-w)} z" fill="${paint.left === "none" ? "none" : C.panel}" stroke="${paint.stroke}" stroke-width="1.4"${paint.dash ? ` stroke-dasharray="${paint.dash}"` : ""}/>`;
  out += `<path d="M ${n(l + w - fold)},${n(t)} v ${n(fold)} h ${n(fold)}" fill="none" stroke="${paint.stroke}" stroke-width="1.4"/>`;
  if (!lit(ctx.node)) return out;
  for (let i = 1; i <= 3; i++) out += etch(l + w * 0.14, t + fold + i * (hh * 0.14), l + w * 0.86, t + fold + i * (hh * 0.14));
  return out;
}

/** queue — conveyor: a belt with crates on it and a runner travelling the
 *  belt's own length, so the direction of the backlog is visible. */
function eQueue(ctx: EntityCtx): string {
  const g = geo(ctx);
  const paint = facePaint(ctx.node);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, paint);
  const cs = Math.min(g.w * 0.16, g.h * 0.5);
  for (let i = 0; i < 3; i++) {
    const bx = g.x + g.w * (0.14 + i * 0.28);
    out += prism(ctx.pt, bx, g.cy - cs / 2, cs, cs, cs, paint, g.hz);
  }
  if (!lit(ctx.node)) return out;
  const a = ctx.pt(g.x + g.w * 0.06, g.cy, g.hz);
  const b = ctx.pt(g.x + g.w * 0.94, g.cy, g.hz);
  const run = `M ${n(a.sx)},${n(a.sy)} L ${n(b.sx)},${n(b.sy)}`;
  out += ctx.motion
    ? `<circle r="3" fill="${C.accent}"><animateMotion path="${run}" dur="2.6s" begin="0s" repeatCount="indefinite"/></circle>`
    : `<circle cx="${n(a.sx)}" cy="${n(a.sy)}" r="3" fill="${C.accent}"/>`;
  return out;
}

/** type — blueprint plate: an upright schema sheet with a header band. */
function eTypePlate(ctx: EntityCtx): string {
  const g = geo(ctx);
  const depth = Math.max(3, g.h * 0.16);
  const py = g.cy - depth / 2;
  let out = prism(ctx.pt, g.x, py, g.w, depth, g.hz, facePaint(ctx.node));
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceLeft(ctx.pt, g.x, py, depth, g.hz)}">`;
  out += box(g.w * 0.04, g.hz * 0.06, g.w * 0.92, g.hz * 0.2, C.top);
  for (let i = 1; i <= 3; i++) out += etch(g.w * 0.1, g.hz * (0.34 + i * 0.16), g.w * (i === 2 ? 0.72 : 0.86), g.hz * (0.34 + i * 0.16));
  out += `<g>${ctx.motion ? breathe("3s", "0s") : ""}<rect x="${n(g.w * 0.04)}" y="${n(g.hz * 0.28)}" width="${n(g.w * 0.92)}" height="${n(g.hz * 0.66)}" fill="${C.accent}" opacity="0.1"/></g>`;
  return out + `</g>`;
}

/** pkg — crate: a cube with strapping on all three faces. */
function ePkgCrate(ctx: EntityCtx): string {
  const g = geo(ctx);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, facePaint(ctx.node));
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceLeft(ctx.pt, g.x, g.y, g.h, g.hz)}">${etch(g.w / 2, 0, g.w / 2, g.hz)}</g>`;
  out += `<g transform="${faceRight(ctx.pt, g.x, g.y, g.w, g.hz)}">` +
    box(g.h * 0.24, g.hz * 0.2, g.h * 0.28, g.hz * 0.2, C.top) +
    box(g.h * 0.24, g.hz * 0.58, g.h * 0.28, g.hz * 0.2, C.top) +
    `</g>`;
  out += `<g transform="${faceTop(ctx.pt, g.x, g.y, g.hz)}">${etch(0, g.h / 2, g.w, g.h / 2)}</g>`;
  return out;
}

/** machine — platform: a broad deck with a small unit on it and corner
 *  lights, the host everything else stands on. */
function eMachine(ctx: EntityCtx): string {
  const g = geo(ctx);
  const paint = facePaint(ctx.node);
  const uw = g.w * 0.22;
  const uh = g.h * 0.28;
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, paint);
  out += prism(ctx.pt, g.x + g.w * 0.6, g.y + g.h * 0.36, uw, uh, g.hz * 1.1, paint, g.hz);
  if (!lit(ctx.node)) return out;
  const a = ctx.pt(g.x + g.w, g.y, g.hz);
  const b = ctx.pt(g.x, g.y + g.h, g.hz);
  out += lamp(a.sx, a.sy, 2.2, C.accent, ctx.motion, "2.3s", "0s");
  out += lamp(b.sx, b.sy, 2.2, C.accent, ctx.motion, "3.1s", "-0.9s");
  return out;
}

/** cloud — sky service: someone else's machine, tethered not grounded. */
function eCloud(ctx: EntityCtx): string {
  const g = geo(ctx);
  const ground = ctx.pt(g.cx, g.cy, 0);
  const c = ctx.pt(g.cx, g.cy, Math.max(20, Math.min(g.w, g.h) * 0.7));
  const r = Math.max(14, Math.min(g.w, g.h) * 0.42);
  const paint = facePaint(ctx.node);
  let out = shadow(ctx.pt, g.x, g.y, g.w, g.h);
  out += `<line x1="${n(c.sx)}" y1="${n(c.sy + r * 0.4)}" x2="${n(ground.sx)}" y2="${n(ground.sy)}" stroke="${C.faint}" stroke-width="1" stroke-dasharray="2 4"/>`;
  out += `<g>${ctx.motion ? bob(5, "3.4s", "-0.5s") : ""}`;
  out += `<path d="M ${n(c.sx - r)},${n(c.sy + r * 0.28)} a ${n(r * 0.36)},${n(r * 0.3)} 0 0 1 ${n(r * 0.64)},${n(-r * 0.23)} ` +
    `a ${n(r * 0.49)},${n(r * 0.41)} 0 0 1 ${n(r * 0.9)},${n(-r * 0.08)} ` +
    `a ${n(r * 0.33)},${n(r * 0.28)} 0 0 1 ${n(r * 0.56)},${n(r * 0.31)} ` +
    `q 0,${n(r * 0.36)} ${n(-r * 0.36)},${n(r * 0.36)} h ${n(-r * 1.38)} q ${n(-r * 0.36)},0 ${n(-r * 0.36)},${n(-r * 0.36)} z" ` +
    `fill="${paint.left}" stroke="${paint.stroke}" stroke-width="1.4"${paint.dash ? ` stroke-dasharray="${paint.dash}"` : ""}/>`;
  if (lit(ctx.node)) {
    out += lamp(c.sx - r * 0.38, c.sy + r * 0.15, 2, C.accent, ctx.motion, "2.2s", "0s");
    out += lamp(c.sx + r * 0.38, c.sy + r * 0.15, 2, C.accent, ctx.motion, "2.9s", "-0.7s");
  }
  return out + `</g>`;
}

/** node — the honest fallback: a plain plinth, for anything the kind table
 *  does not name. */
function ePlinth(ctx: EntityCtx): string {
  const g = geo(ctx);
  return prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, facePaint(ctx.node));
}

/** actor — a person, drawn in screen space: a human is the one entity that
 *  should not read as machinery. */
function eActor(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, 0);
  const s = Math.max(18, Math.min(g.w, g.h) * 0.55);
  const col = ctx.node.treatment ? TREAT[ctx.node.treatment] : C.stroke;
  const headY = c.sy - s * 1.5;
  let out = shadow(ctx.pt, g.x, g.y, g.w, g.h);
  out += `<circle cx="${n(c.sx)}" cy="${n(headY)}" r="${n(s * 0.28)}" fill="${C.top}" stroke="${col}" stroke-width="1.5"/>`;
  out += `<path d="M ${n(c.sx)},${n(headY + s * 0.28)} v ${n(s * 0.8)} M ${n(c.sx - s * 0.42)},${n(headY + s * 0.62)} h ${n(s * 0.84)} ` +
    `M ${n(c.sx)},${n(headY + s * 1.08)} l ${n(-s * 0.32)},${n(s * 0.56)} M ${n(c.sx)},${n(headY + s * 1.08)} l ${n(s * 0.32)},${n(s * 0.56)}" ` +
    `fill="none" stroke="${col}" stroke-width="3" stroke-linecap="round"/>`;
  return out;
}

/** decision — signal gantry: a lit diamond on posts, the branch point. */
function eDecision(ctx: EntityCtx): string {
  const g = geo(ctx);
  const postW = Math.max(3, g.w * 0.07);
  const paint = facePaint(ctx.node);
  let out = prism(ctx.pt, g.cx - postW / 2, g.cy - postW / 2, postW, postW, g.hz, paint);
  const c = ctx.pt(g.cx, g.cy, g.hz + 16);
  const r = Math.max(12, Math.min(g.w, g.h) * 0.32);
  const pts = `${n(c.sx)},${n(c.sy - r)} ${n(c.sx + r)},${n(c.sy)} ${n(c.sx)},${n(c.sy + r)} ${n(c.sx - r)},${n(c.sy)}`;
  out += `<polygon points="${pts}" fill="${paint.left}" stroke="${paint.stroke}" stroke-width="1.4"${paint.dash ? ` stroke-dasharray="${paint.dash}"` : ""}/>`;
  if (!lit(ctx.node)) return out;
  out += `<g>${ctx.motion ? breathe("2.2s", "0s") : ""}<polygon points="${pts}" fill="none" stroke="${C.accent}" stroke-width="1.2"/></g>`;
  out += `<text x="${n(c.sx)}" y="${n(c.sy + r * 0.3)}" fill="${C.accent}" font-size="${n(r * 0.8)}" text-anchor="middle">?</text>`;
  return out;
}

/** pill — floor beacon: a lit lozenge lying flat on the floor plane. */
function ePill(ctx: EntityCtx): string {
  const g = geo(ctx);
  const paint = facePaint(ctx.node);
  const t = faceTop(ctx.pt, g.x, g.y, 1);
  const r = Math.min(g.w, g.h) / 2;
  let out = `<rect width="${n(g.w)}" height="${n(g.h)}" rx="${n(r)}" transform="${t}" fill="${paint.left === "none" ? "none" : C.left}" stroke="${paint.stroke}" stroke-width="1.4"${paint.dash ? ` stroke-dasharray="${paint.dash}"` : ""}/>`;
  if (!lit(ctx.node)) return out;
  out += `<g>${ctx.motion ? breathe("2.6s", "0s") : ""}<rect width="${n(g.w)}" height="${n(g.h)}" rx="${n(r)}" transform="${t}" fill="none" stroke="${C.accent}" stroke-width="1.2"/></g>`;
  return out;
}

/** cell — tile grid: the matrix as raised floor tiles; a `head` cell lights
 *  its first row, which is the header modifier's whole job. */
function eCell(ctx: EntityCtx): string {
  const g = geo(ctx);
  const paint = facePaint(ctx.node);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, paint);
  if (!lit(ctx.node)) return out;
  const cols = 3;
  const rows = 3;
  const gap = 0.08;
  out += `<g transform="${faceTop(ctx.pt, g.x, g.y, g.hz)}">`;
  for (let r = 0; r < rows; r++) {
    for (let cI = 0; cI < cols; cI++) {
      const head = ctx.node.head && r === 0;
      out += box(
        (g.w / cols) * (cI + gap),
        (g.h / rows) * (r + gap),
        (g.w / cols) * (1 - gap * 2),
        (g.h / rows) * (1 - gap * 2),
        head ? C.accentDim : C.floor,
        head ? C.accent : C.floorLine,
      );
    }
  }
  out += `<g>${ctx.motion ? breathe("2.8s", "0s") : ""}` +
    box(g.w / cols + (g.w / cols) * gap, g.h / rows + (g.h / rows) * gap, (g.w / cols) * (1 - gap * 2), (g.h / rows) * (1 - gap * 2), C.accent, "none", ` opacity="0.22"`) +
    `</g>`;
  return out + `</g>`;
}

/** lane — a lifeline as a pulse rail down the participant's own column. */
function eLane(ctx: EntityCtx): string {
  const g = geo(ctx);
  const vertical = g.h >= g.w;
  const a = ctx.pt(vertical ? g.cx : g.x, vertical ? g.y : g.cy, 0);
  const b = ctx.pt(vertical ? g.cx : g.x + g.w, vertical ? g.y + g.h : g.cy, 0);
  const col = ctx.node.treatment ? TREAT[ctx.node.treatment] : C.stroke;
  let out = diamond(ctx.pt, g.x, g.y, g.w, g.h, 0, `fill="${C.floor}" stroke="${C.floorLine}" stroke-width="1" opacity="0.6"`);
  out += `<line x1="${n(a.sx)}" y1="${n(a.sy)}" x2="${n(b.sx)}" y2="${n(b.sy)}" stroke="${col}" stroke-width="1.3" stroke-dasharray="5 5"/>`;
  if (!lit(ctx.node)) return out;
  const rail = `M ${n(a.sx)},${n(a.sy)} L ${n(b.sx)},${n(b.sy)}`;
  out += ctx.motion
    ? `<circle r="3" fill="${C.accent}"><animateMotion path="${rail}" dur="3.2s" begin="0s" repeatCount="indefinite"/></circle>`
    : `<circle cx="${n(a.sx)}" cy="${n(a.sy)}" r="3" fill="${C.accent}"/>`;
  return out;
}

/** group — district: a dashed floor plate with a lit post at each corner. */
function eGroupPlate(ctx: EntityCtx): string {
  const g = geo(ctx);
  const col = ctx.node.treatment ? TREAT[ctx.node.treatment] : C.stroke;
  let out = diamond(ctx.pt, g.x, g.y, g.w, g.h, 0, `fill="${C.floor}" stroke="${col}" stroke-width="1.4" stroke-dasharray="7 5"`);
  if (!lit(ctx.node)) return out;
  const corners: Array<[number, number]> = [
    [g.x, g.y],
    [g.x + g.w, g.y],
    [g.x + g.w, g.y + g.h],
    [g.x, g.y + g.h],
  ];
  corners.forEach(([px, py], i) => {
    const p = ctx.pt(px, py, 0);
    out += `<line x1="${n(p.sx)}" y1="${n(p.sy)}" x2="${n(p.sx)}" y2="${n(p.sy - 10)}" stroke="${C.accentDim}" stroke-width="1.2"/>`;
    out += lamp(p.sx, p.sy - 12, 2, C.accent, ctx.motion, i % 2 ? "2.9s" : "2.1s", i % 2 ? "-0.7s" : "0s");
  });
  return out;
}

/** region — lit zone: a tinted floor wash marking a phase or a window. */
function eRegion(ctx: EntityCtx): string {
  const g = geo(ctx);
  const col = ctx.node.treatment ? TREAT[ctx.node.treatment] : C.accent;
  let out = diamond(ctx.pt, g.x, g.y, g.w, g.h, 0, `fill="${col}" opacity="0.09"`);
  if (lit(ctx.node)) {
    out += `<g>${ctx.motion ? breathe("3s", "0s") : ""}${diamond(ctx.pt, g.x, g.y, g.w, g.h, 0, `fill="${col}" opacity="0.08"`)}</g>`;
  }
  out += diamond(ctx.pt, g.x, g.y, g.w, g.h, 0, `fill="none" stroke="${lit(ctx.node) ? C.accentDim : col}" stroke-width="1.2"${ctx.node.treatment === "grey" ? ` stroke-dasharray="3 3"` : ""}`);
  return out;
}

/** step — a floor marker. Steps normally arrive as StepMarks and render in
 *  the marks layer; this covers a `step`-classed <g> read as an EntityNode. */
function eStepDisc(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, 1);
  const r = Math.max(9, Math.min(g.w, g.h) * 0.42);
  return (
    `<ellipse cx="${n(c.sx)}" cy="${n(c.sy)}" rx="${n(r * 1.2)}" ry="${n(r * 0.6)}" fill="${C.panel}" stroke="${C.accent}" stroke-width="1.4"/>` +
    (lit(ctx.node) ? lamp(c.sx, c.sy, 2, C.accent, ctx.motion, "2.4s", "0s") : "")
  );
}

/** badge — a count chip, standing where the card put it. */
function eBadgeChip(ctx: EntityCtx): string {
  const g = geo(ctx);
  const c = ctx.pt(g.cx, g.cy, 6);
  const w = Math.max(30, g.w);
  const h = Math.max(16, g.h);
  return `<rect x="${n(c.sx - w / 2)}" y="${n(c.sy - h / 2)}" width="${n(w)}" height="${n(h)}" rx="${n(h / 2)}" fill="${C.panel}" stroke="${ctx.node.treatment ? TREAT[ctx.node.treatment] : C.panelLine}" stroke-width="1.2"/>`;
}

/** bar — a measured column: the one entity whose height is the point. */
function eBar(ctx: EntityCtx): string {
  const g = geo(ctx);
  const paint = facePaint(ctx.node);
  let out = prism(ctx.pt, g.x, g.y, g.w, g.h, g.hz, paint);
  if (!lit(ctx.node)) return out;
  out += `<g transform="${faceLeft(ctx.pt, g.x, g.y, g.h, g.hz)}">` +
    etch(0, g.hz * 0.34, g.w, g.hz * 0.34, C.accentDim) +
    etch(0, g.hz * 0.67, g.w, g.hz * 0.67, C.accentDim) +
    `</g>`;
  return out;
}

/** The vocabulary. A shape outside it degrades to the plinth — the reader
 *  hands over whatever class the card wrote, and an unlisted kind is the card
 *  author's move, not an error here. */
const EMITTERS: Record<string, (ctx: EntityCtx) => string> = {
  proc: eProc,
  cli: eCli,
  thread: eThread,
  fn: eFn,
  agent: eAgent,
  llm: eLlm,
  timer: eTimer,
  iface: eIface,
  event: eEvent,
  ui: eUi,
  store: eStore,
  table: eTable,
  file: eFile,
  queue: eQueue,
  type: eTypePlate,
  pkg: ePkgCrate,
  machine: eMachine,
  cloud: eCloud,
  node: ePlinth,
  actor: eActor,
  decision: eDecision,
  pill: ePill,
  cell: eCell,
  lane: eLane,
  group: eGroupPlate,
  region: eRegion,
  step: eStepDisc,
  badge: eBadgeChip,
  bar: eBar,
};

/** The body of one entity's group. Never emits `data-entity`, `data-shape`,
 *  `data-code`, `data-treat` or the dim `opacity` — those are render.ts's,
 *  because the contract pins them and one owner is what keeps them pinned. */
export function entityBody(ctx: EntityCtx): string {
  const emit = EMITTERS[ctx.node.shape] ?? ePlinth;
  return emit(ctx);
}

/** grey is the one treatment R5 reads out of the group's BODY rather than off
 *  an attribute — it wants a `stroke-dasharray` inside the group. Most
 *  silhouettes carry it already, because facePaint dashes their faces; a few
 *  (the person, the sparkle, the dart, the floor marks) are drawn from paths
 *  with no faces at all. This ghost footprint guarantees it for every shape
 *  from one place, which is the same reason the envelope attributes live in
 *  render.ts rather than in twenty-nine emitters. */
export function greyMark(ctx: EntityCtx): string {
  if (ctx.node.treatment !== "grey") return "";
  const g = geo(ctx);
  return diamond(
    ctx.pt,
    g.x,
    g.y,
    g.w,
    g.h,
    0,
    `fill="none" stroke="${TREAT.grey}" stroke-width="1.2" stroke-dasharray="3 3"`,
  );
}

/** The treatment overlay: a rim on the footprint plus the glyph, added by
 *  render.ts on top of any body so all six treatments read the same way
 *  whatever silhouette they land on. `grey` is excluded — its material IS the
 *  treatment, and a rim light on a ghost frame would contradict it. */
export function treatmentMark(ctx: EntityCtx): string {
  const t = ctx.node.treatment;
  if (!t || t === "grey") return "";
  const g = geo(ctx);
  const col = TREAT[t];
  const rim = diamond(ctx.pt, g.x, g.y, g.w, g.h, g.hz, `fill="none" stroke="${col}" stroke-width="1.5"`);
  const c = ctx.pt(g.cx, g.cy, g.hz);
  const strobe = t === "red" ? "1.1s" : t === "yellow" ? "2.6s" : "";
  return (
    `<g>${ctx.motion ? breathe(t === "red" ? "1.4s" : "2.6s", "0s") : ""}${rim}</g>` +
    lamp(c.sx + 10, c.sy - 12, 3, col, ctx.motion && strobe !== "", strobe || "2.4s", "0s") +
    `<text x="${n(c.sx + 24)}" y="${n(c.sy - 10)}" fill="${col}" font-size="12" text-anchor="middle">${esc(TREAT_GLYPH[t])}</text>`
  );
}
