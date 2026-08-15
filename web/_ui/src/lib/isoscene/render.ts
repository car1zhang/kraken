// render.ts — the isoscene compiler's emitter: a ProjectedScene in, one
// inline `<svg>` string out. This file owns every attribute 50-contract.md's
// R1-R10 pin (the `data-entity`/`data-shape` envelope, `data-code`,
// `data-treat`, the dim `opacity`, the conduit groups and their markers);
// entities.ts owns what is drawn inside that envelope. Splitting it that way
// is deliberate: a contract attribute written in one place cannot drift
// per-silhouette, and there are twenty-nine silhouettes.
//
// The output is a STRING rather than a DOM tree because the compiled card is
// handed to the board as markup and because the blind suites read it as text.
// Two consequences are load-bearing. The root `<svg>` carries NO `xmlns`: that
// attribute's value is an `http://` URL and R9 forbids every URL in the
// output — inline SVG in an HTML document needs no namespace declaration
// anyway. And `<animateMotion>` carries its geometry in its own `path`
// attribute instead of an `<mpath href>`, so an emitted scene depends on no
// id resolution at all and two compiled cards can share one page.

import type {
  EntityNode,
  IsoPoint,
  ProjectedConduit,
  ProjectedScene,
  RenderOptions,
  Treatment,
} from "./types.ts";
import { isoProject } from "./project.ts";
import {
  C,
  TREAT,
  bodyHeight,
  entityBody,
  esc,
  greyMark,
  n,
  treatmentMark,
  type EntityCtx,
  type Proj,
} from "./entities.ts";

/** One arrow marker per paint, all of them defined whether or not the scene
 *  uses them: R10 asks that every `marker-end` resolve inside the output's own
 *  `<defs>`, and a defs block that does not depend on which conduits exist is
 *  one fewer thing to get wrong. A marker cannot inherit its parent's stroke,
 *  which is why the colour has to be baked per marker. */
const MARKER_PAINT: Array<[string, string]> = [
  ["iso-arrow", C.accentDim],
  ["iso-arrow-grey", TREAT.grey],
  ["iso-arrow-red", TREAT.red],
  ["iso-arrow-yellow", TREAT.yellow],
  ["iso-arrow-green", TREAT.green],
  ["iso-arrow-blue", TREAT.blue],
  ["iso-arrow-purple", TREAT.purple],
];

function markerId(t?: Treatment): string {
  return t ? `iso-arrow-${t}` : "iso-arrow";
}

function defs(): string {
  const markers = MARKER_PAINT.map(
    ([id, paint]) =>
      `<marker id="${id}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">` +
      `<path d="M0,0 L10,5 L0,10 z" fill="${paint}"/></marker>`,
  ).join("");
  return `<defs>${markers}</defs>`;
}

interface Placed {
  node: EntityNode;
  depth: number;
}

function collect(nodes: readonly EntityNode[], depth: number, out: Placed[]): void {
  for (const node of nodes) {
    out.push({ node, depth });
    collect(node.children, depth + 1, out);
  }
}

/** Marks (titles, notes, step and count badges) are the one thing the
 *  projector does not bound — P4 covers placements and conduit endpoints, and
 *  a card's title often sits at a corner that projects outside the box. Clamp
 *  rather than drop: an unreadable title is a bug, a missing one is a worse
 *  bug. */
function clampPt(p: IsoPoint, width: number, height: number, pad = 12): IsoPoint {
  return {
    sx: Math.min(Math.max(p.sx, pad), Math.max(pad, width - pad)),
    sy: Math.min(Math.max(p.sy, pad), Math.max(pad, height - pad)),
  };
}

const LABEL_FS = 11;

/** A label chip: a panel plate behind the text so a label stays readable over
 *  a lit entity or a tinted zone. Width is estimated from the glyph count
 *  because the emitter has no font metrics — the compiler never touches a DOM
 *  and `getBBox` is exactly the browser-only API the contract's environment
 *  section rules out. */
function labelChip(
  at: IsoPoint,
  lines: string[],
  colour: string,
  anchor: "middle" | "start",
  bounds: IsoPoint,
): string {
  const shown = lines.filter((l) => l.length > 0).slice(0, 2);
  if (shown.length === 0) return "";
  const widest = shown.reduce((m, l) => Math.max(m, l.length), 0);
  const w = Math.max(26, widest * LABEL_FS * 0.56 + 12);
  const h = 6 + shown.length * (LABEL_FS + 2);
  // The CHIP is what has to fit, not its anchor: clamping the anchor alone
  // still lets a wide label run off the right edge, which is how a card's note
  // loses half its words. Only an overflowing chip moves.
  const left = Math.min(
    Math.max(anchor === "middle" ? at.sx - w / 2 : at.sx - 6, 4),
    Math.max(4, bounds.sx - w - 4),
  );
  const top = Math.min(Math.max(at.sy - h / 2, 4), Math.max(4, bounds.sy - h - 4));
  const tx = anchor === "middle" ? left + w / 2 : left + 6;
  const first = shown[0] ?? "";
  const rest = shown
    .slice(1)
    .map((l) => `<tspan x="${n(tx)}" dy="${LABEL_FS + 2}">${esc(l)}</tspan>`)
    .join("");
  return (
    `<rect x="${n(left)}" y="${n(top)}" width="${n(w)}" height="${n(h)}" rx="4" fill="${C.panel}" stroke="${C.panelLine}" stroke-width="1" opacity="0.92"/>` +
    `<text x="${n(tx)}" y="${n(top + LABEL_FS + 1)}" fill="${colour}" font-size="${LABEL_FS}" text-anchor="${anchor}" font-family="inherit">${esc(first)}${rest}</text>`
  );
}

const FLAT_SHAPES = new Set(["group", "region", "lane"]);

/** One entity's whole group. Every contract-visible attribute is written
 *  here and nowhere else. */
function entityGroup(node: EntityNode, pt: Proj, motion: boolean, bounds: IsoPoint): string {
  const ctx: EntityCtx = { node, pt, uid: `iso-${node.id}`, motion };
  const attrs: string[] = [`data-entity="${esc(node.id)}"`, `data-shape="${esc(node.shape)}"`];
  // R3 wants the codeRef VERBATIM as read back by the consumer. esc() is the
  // one encoding whose output round-trips through the HTML parser to the
  // identical string — quote-only escaping left an entity-shaped run (&amp;)
  // in the value decoding differently on getAttribute than the card wrote it.
  if (node.codeRef !== undefined) attrs.push(`data-code="${esc(node.codeRef)}"`);
  if (node.treatment) attrs.push(`data-treat="${node.treatment}"`);
  if (node.head) attrs.push(`data-head="1"`);
  // R5: dim is opacity and nothing else — the entity keeps its silhouette and
  // its colour, exactly as `dim` behaves on the flat card.
  if (node.dim) attrs.push(`opacity="0.38"`);

  const hz = bodyHeight(node);
  const flat = FLAT_SHAPES.has(node.shape);
  const labelAt = flat
    ? pt(node.x + node.width * 0.14, node.y + node.height * 0.14, 6)
    : pt(node.x + node.width / 2, node.y + node.height / 2, hz + 16);
  const label = labelChip(labelAt, node.labelLines, C.ink, flat ? "start" : "middle", bounds);

  return (
    `<g ${attrs.join(" ")}>` +
    entityBody(ctx) +
    greyMark(ctx) +
    treatmentMark(ctx) +
    label +
    `</g>`
  );
}

/** Legacy cards carry their own flow-dot timing; new cards carry none. Either
 *  way the pulse is GENERATED from geometry (R7), so a card's `dur` is
 *  emphasis input only — and only when it is a duration this emitter can
 *  vouch for, since it lands unescaped in the markup. */
function safeDur(raw: string | undefined, fallback: string): string {
  // Normalised to SECONDS on the way out: the trailing pulse below halves the
  // value with parseFloat, and parseFloat("500ms") is 500 — an ms duration
  // left as-is would compute a -250s begin against a 0.5s cycle, landing the
  // trailing pulse exactly on top of the leading one.
  const m = raw !== undefined ? /^(\d+(?:\.\d+)?)(ms|s)$/.exec(raw) : null;
  if (!m || m[1] === undefined) return fallback;
  const v = m[2] === "ms" ? parseFloat(m[1]) / 1000 : parseFloat(m[1]);
  if (!Number.isFinite(v)) return fallback;
  // Guard the ROUNDED string, not the raw value: n() rounds to two decimals,
  // so a sub-5ms duration would otherwise pass `v > 0` and still emit
  // `dur="0s"`, which SMIL treats as no duration at all.
  const s = n(v);
  return parseFloat(s) > 0 ? `${s}s` : fallback;
}

function conduitGroup(pc: ProjectedConduit, index: number, motion: boolean): string {
  const c = pc.conduit;
  const s = pc.start;
  const e = pc.end;
  const dx = e.sx - s.sx;
  const dy = e.sy - s.sy;
  const len = Math.hypot(dx, dy) || 1;
  // A conduit hangs: the control point sags below the straight run, which is
  // what separates two conduits that share endpoints on the flat card and
  // makes direction readable at a glance.
  const sag = Math.min(26, len * 0.1);
  const d =
    `M ${n(s.sx)},${n(s.sy)} Q ${n((s.sx + e.sx) / 2)},${n((s.sy + e.sy) / 2 + sag)} ${n(e.sx)},${n(e.sy)}`;
  const colour = c.treatment ? TREAT[c.treatment] : C.accentDim;
  const glow = c.treatment ? TREAT[c.treatment] : C.accent;

  const attrs: string[] = [`data-conduit="${esc(c.id ?? `c${index}`)}"`];
  if (c.codeRef !== undefined) attrs.push(`data-code="${esc(c.codeRef)}"`);
  if (c.treatment) attrs.push(`data-treat="${c.treatment}"`);
  if (c.dashed) attrs.push(`data-dashed="1"`);
  if (c.dim) attrs.push(`opacity="0.34"`);

  let out = `<g ${attrs.join(" ")}>`;
  out += `<path d="${d}" fill="none" stroke="${glow}" stroke-width="9" opacity="0.1"/>`;
  out +=
    `<path d="${d}" fill="none" stroke="${colour}" stroke-width="${c.dashed ? "1.8" : "2"}"` +
    (c.dashed ? ` stroke-dasharray="7 6"` : "") +
    ` marker-end="url(#${markerId(c.treatment)})"/>`;

  // R7: every non-dim conduit animates, whether or not the card ever carried a
  // flow-dot. A dim conduit is out of this figure's story, so it stays still.
  if (motion && !c.dim) {
    const dur = safeDur(c.flow?.dur, c.dashed ? "5.5s" : "3s");
    out += `<circle r="${c.dashed ? "3.4" : "4"}" fill="${glow}"${c.dashed ? ` opacity="0.7"` : ""}>` +
      `<animateMotion path="${d}" dur="${dur}" begin="0s" repeatCount="indefinite"/></circle>`;
    if (!c.dashed) {
      // R8: a trailing pulse offsets with a NEGATIVE begin, never a positive
      // one — the scene's first frame has to be its resting frame.
      out += `<circle r="3" fill="${glow}" opacity="0.45">` +
        `<animateMotion path="${d}" dur="${dur}" begin="-${(parseFloat(dur) / 2 || 1.5).toFixed(2)}s" repeatCount="indefinite"/></circle>`;
    }
  }
  return out + `</g>`;
}

export function renderSceneMarkup(p: ProjectedScene, opts: RenderOptions = { reducedMotion: false }): string {
  const motion = opts.reducedMotion !== true;
  const scene = p.scene;

  const structures: Placed[] = [];
  collect(scene.structures, 0, structures);
  const entities: Placed[] = [];
  collect(scene.entities, 0, entities);

  const all = [...structures, ...entities];
  // The projector's own offset (P3's one rigid vector), taken straight from
  // the ProjectedScene rather than recovered from a placement — a marks-only
  // scene has no placement to recover it from.
  const off = p.offset;
  const pt: Proj = (x, y, z = 0) => {
    const q = isoProject(x, y, z);
    return { sx: q.sx + off.sx, sy: q.sy + off.sy };
  };

  // Floors first, then everything that stands on one. A structure's own
  // markup-nested children are components, not floors, so they move into the
  // standing layer — which is also what keeps them above the conduits.
  const floorLayer = structures.filter((pl) => pl.depth === 0 && FLAT_SHAPES.has(pl.node.shape));
  const floorIds = new Set(floorLayer.map((pl) => pl.node.id));
  const standing = all.filter((pl) => !floorIds.has(pl.node.id));

  // Painter's order: back of the floor first. A card point's depth from the
  // viewer is x+y under this projection, and depth breaks ties so a child
  // sharing its parent's origin still lands on top of it.
  standing.sort((a, b) => a.node.x + a.node.y - (b.node.x + b.node.y) || a.depth - b.depth);

  const bounds: IsoPoint = { sx: p.width, sy: p.height };
  let body = defs();
  body += `<g class="iso-floors">${floorLayer.map((pl) => entityGroup(pl.node, pt, motion, bounds)).join("")}</g>`;
  body += `<g class="iso-conduits">${p.conduits.map((pc, i) => conduitGroup(pc, i, motion)).join("")}</g>`;
  body += `<g class="iso-entities">${standing.map((pl) => entityGroup(pl.node, pt, motion, bounds)).join("")}</g>`;

  let marks = "";
  for (const t of scene.texts) {
    const at = clampPt(pt(t.x, t.y, 0), p.width, p.height);
    const colour = t.kind === "title" ? C.ink : C.muted;
    marks +=
      `<g class="iso-text" data-text="${t.kind}"${t.dim ? ` opacity="0.38"` : ""}>` +
      labelChip(at, t.lines, colour, t.kind === "edgeLabel" ? "middle" : "start", bounds) +
      `</g>`;
  }
  for (const s of scene.steps) {
    const at = clampPt(pt(s.x, s.y, 8), p.width, p.height);
    marks +=
      `<g class="iso-step">` +
      `<circle cx="${n(at.sx)}" cy="${n(at.sy)}" r="11" fill="${C.panel}" stroke="${C.accent}" stroke-width="1.4"/>` +
      `<text x="${n(at.sx)}" y="${n(at.sy + 4)}" fill="${C.accent}" font-size="11" text-anchor="middle" font-family="inherit">${esc(s.label)}</text>` +
      `</g>`;
  }
  for (const b of scene.badges) {
    const at = clampPt(pt(b.x, b.y, 8), p.width, p.height);
    const w = Math.max(32, b.label.length * 6.6 + 14);
    marks +=
      `<g class="iso-badge">` +
      `<rect x="${n(at.sx - w / 2)}" y="${n(at.sy - 9)}" width="${n(w)}" height="18" rx="9" fill="${C.panel}" stroke="${C.panelLine}" stroke-width="1"/>` +
      `<text x="${n(at.sx)}" y="${n(at.sy + 4)}" fill="${C.muted}" font-size="11" text-anchor="middle" font-family="inherit">${esc(b.label)}</text>` +
      `</g>`;
  }
  body += `<g class="iso-marks">${marks}</g>`;

  // R1: the viewBox is the ProjectedScene's own box, verbatim — no rounding,
  // so a caller comparing against `p.width`/`p.height` sees its own numbers.
  return (
    `<svg viewBox="0 0 ${p.width} ${p.height}" class="iso-scene" role="img" ` +
    `preserveAspectRatio="xMidYMid meet" aria-label="${esc(scene.title || "compiled diagram")}">` +
    body +
    `</svg>`
  );
}
