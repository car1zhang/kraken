// Everything about the graph view that is a pure function of its data.
//
// This module exists so GraphView.tsx's sigma effect holds nothing but wiring.
// Sigma renders to a real WebGL canvas, which jsdom does not have, so anything
// living inside that effect is untestable here — the filters, the colour rule,
// the sizes and the fallback layout are therefore all out here, and the effect
// only hands them to sigma.

import type { EdgeDisplayData, NodeDisplayData } from "sigma/types";

import type { GraphEdge, GraphNode } from "../../hooks/useGraph.ts";

// ------------------------------------------------------------------ colour

/**
 * Node colour by entity type, as style.css custom-property NAMES.
 *
 * Names rather than hexes because the board has a light theme: the same var
 * resolves to a different value under `:root[data-theme="light"]`, and sigma
 * needs a concrete colour string, so the resolution happens once per render
 * pass in readGraphPalette below. The five hues are the mockup's own
 * (40-ui-graph-view.html): accent for conventions, polling-cyan for
 * components, revealed-amber for failure modes, solved-green for decisions,
 * and the hairline grey for the structural file/job nodes that are context
 * rather than knowledge.
 */
export const TYPE_COLOR_VARS: Readonly<Record<string, string>> = {
  // All NINE seed types from the /graph skill's vocabulary — a seed type
  // falling through to the invented-type grey made three documented types
  // indistinguishable from unrecognised ones (review finding).
  convention: "--accent",
  component: "--polling",
  "failure-mode": "--revealed",
  decision: "--solved",
  person: "--counter",
  tool: "--skill-do",
  "external-system": "--skill-info",
  file: "--hairline-2",
  job: "--hairline-2",
};

/** Anything the extraction agent invented that the map above does not name. */
export const DEFAULT_TYPE_VAR = "--text-2";

/** Edges, labels and the selection ring. Edges take the graph's own token
 * rather than `--hairline-2`, whose evaluated mix is so dark it read as black
 * on the dark board — carl's report. The mockup's stroke is 22% accent; the
 * token is bumped to 32% (style.css says why beside its definition). */
export const EDGE_VAR = "--graph-edge";
export const SELECTED_VAR = "--skill-graph";
export const LABEL_VAR = "--text";

/**
 * This map is BOTH the list of vars readGraphPalette reads AND their
 * fallbacks — a var absent here is never read at all, which is how three
 * seed types (person/tool/external-system) once rendered a hardcoded grey
 * that ignored theme and accent alike (review finding): TYPE_COLOR_VARS
 * named their vars but this map did not carry them. Every var
 * TYPE_COLOR_VARS, DEFAULT_TYPE_VAR, or the three VAR constants above name
 * must have a row.
 *
 * Values are the HUD redesign's dark evaluations (the `:root` violet
 * palette), used when getComputedStyle has nothing to say — which is every
 * test (jsdom's getContext("2d") is null, so the resolver body never runs)
 * and any moment before the stylesheet has applied. Dark rather than light
 * because `:root` in style.css is the dark palette and every failure mode
 * there is documented to land on it. The mixes are computed by hand from
 * style.css's recipes; if the primitives move, these move with them.
 */
export const PALETTE_FALLBACK: Readonly<Record<string, string>> = {
  "--accent": "#a86aff",
  "--polling": "#00e5ff",
  "--revealed": "#ffb000",
  "--solved": "#7fd44a",
  "--counter": "#ff4fd8",
  "--skill-do": "#F5D34D",
  "--skill-info": "#5EA8FF",
  // color-mix(in srgb, var(--ac) 36%, var(--b1)) on the dark primitives
  "--hairline-2": "#452e67",
  // color-mix(in srgb, var(--ac) 28%, var(--g1)) on the dark primitives
  "--text-2": "#9785b6",
  "--text": "#f2edff",
  "--skill-graph": "#87DF49",
  // 32% violet over transparent — the dark evaluation of --graph-edge's mix,
  // in the rgba() form sigma parses.
  "--graph-edge": "rgba(168, 106, 255, 0.32)",
};

export type Palette = Readonly<Record<string, string>>;

/**
 * Resolve every var this module can ask for against a live element, falling
 * back per-var. Called once per sigma settings pass rather than per node: it
 * is a forced style recalc, and there are thousands of nodes.
 *
 * Resolution goes through a PROBE ELEMENT's `color`, never through
 * `getPropertyValue(--x)` directly: since the HUD token redesign most of
 * these vars hold `color-mix(...)` expressions, and an unregistered custom
 * property's value is handed back as that raw expression string — which
 * sigma's color parser cannot read, so every edge and half the node types
 * rendered BLACK (carl's report, dark theme). Assigning `var(--x)` to a real
 * `color` property makes the browser evaluate the mix at computed-value
 * time — but the SERIALIZATION it hands back is `color(srgb …)` in Chromium,
 * which sigma cannot parse either, and a canvas `fillStyle` round-trip does
 * NOT normalize it (fillStyle re-serializes modern color syntax in the same
 * form; measured, and it is how the first cut of this fix still drew black).
 * So the normalizer PAINTS: fill one pixel, read the bytes back with
 * getImageData, and compose the `rgba()` string ourselves — pixels have no
 * serialization to disagree about. The read is `var(--x, #010203)` with the
 * SENTINEL as the var's own fallback: a DELETED token substitutes the
 * sentinel and keeps its pinned hex. (A token that exists but holds garbage
 * is invalid-at-computed-value-time and inherits the container's color —
 * that case is not caught, and `color` being inherited is why the bare
 * `var(--x)` form silently painted the container's text color for a missing
 * token too, which two comments then mispromised as "falls back".) Remaining
 * failure paths — no document, no canvas 2d context (jsdom, which is also
 * why NO test reaches this body), an empty computed value — fall back
 * per-var. One accuracy bound worth knowing: getImageData un-premultiplies,
 * so a low-alpha token loses precision — worst-case per-channel drift is
 * ±127/α₈ (α₈ being the 8-bit alpha, round(255·α)), i.e. ±5 at α = 0.1, ±13 at α ≈ 0.04, and unbounded as α → 0. At
 * --graph-edge's 0.32 all three channels round-trip exactly (168 → 54 → 168,
 * 106 → 34 → 106, 255 → 82 → 255); the alpha recomposes as 0.322 rather
 * than 0.320.
 */
export function readGraphPalette(el: Element | null | undefined): Palette {
  if (!el || typeof getComputedStyle !== "function" || typeof document === "undefined") {
    return PALETTE_FALLBACK;
  }
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  const ctx = canvas.getContext?.("2d", { willReadFrequently: true });
  if (!ctx || typeof ctx.getImageData !== "function") return PALETTE_FALLBACK;
  const probe = document.createElement("span");
  probe.style.display = "none";
  el.appendChild(probe);
  const out: Record<string, string> = { ...PALETTE_FALLBACK };
  try {
    for (const name of Object.keys(PALETTE_FALLBACK)) {
      // the sentinel doubles as the var() fallback — see the docstring
      probe.style.color = `var(${name}, #010203)`;
      const evaluated = getComputedStyle(probe).color.trim();
      if (!evaluated) continue;
      // an invalid assignment leaves fillStyle unchanged, so seed the same
      // sentinel and treat a sentinel pixel as the parse failing; the alpha
      // check keeps a real (impossible today) rgba(1,2,3,x) token from
      // colliding, since the sentinel always paints opaque
      ctx.fillStyle = "#010203";
      ctx.fillStyle = evaluated;
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillRect(0, 0, 1, 1);
      const d = ctx.getImageData(0, 0, 1, 1).data;
      const [r, g, b, a] = [d[0]!, d[1]!, d[2]!, d[3]!];
      if (r === 1 && g === 2 && b === 3 && a === 255) continue;
      // alpha is painted onto a cleared (transparent) pixel, so the bytes are
      // the color itself, not a blend; 255 → opaque shorthand
      out[name] =
        a === 255
          ? `rgb(${r}, ${g}, ${b})`
          : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`;
    }
  } finally {
    probe.remove();
  }
  return out;
}

export function colorOf(varName: string, palette: Palette): string {
  // the last resort reads the default-type row itself rather than repeating
  // its hex by hand (a hand copy is the drift the review round just caught in
  // this map's own values); the coverage test pins the row present, and it
  // fires only for a var name in neither map
  return palette[varName] ?? PALETTE_FALLBACK[varName] ?? PALETTE_FALLBACK[DEFAULT_TYPE_VAR]!;
}

export function nodeColor(type: string, palette: Palette): string {
  return colorOf(TYPE_COLOR_VARS[type] ?? DEFAULT_TYPE_VAR, palette);
}

// ------------------------------------------------------------------- size

/**
 * Radius from degree. sqrt so a hub with 200 edges is bigger than one with 9
 * without being 20x the area; clamped at both ends so an isolated node is
 * still clickable and a hub cannot swallow its neighbourhood. The 4-16 range
 * is the mockup's own (r=4 for a leaf, r=12 for the selected hub).
 */
export function nodeSize(degree: number): number {
  const d = Number.isFinite(degree) && degree > 0 ? degree : 0;
  return Math.min(16, 4 + Math.sqrt(d) * 2);
}

// --------------------------------------------------------------- position

/**
 * The graph's ids are integers on the wire, and sigma/graphology key every
 * element by STRING. One conversion function rather than a String() at each
 * call site, so a node key and the edge endpoint that must match it cannot be
 * spelled differently.
 */
export function nodeKey(id: number): string {
  return String(id);
}

export interface Positioned {
  id: number;
  x: number;
  y: number;
}

/** A node the server has not laid out yet — or one it could not. */
function unpositioned(n: GraphNode): boolean {
  return (
    !Number.isFinite(n.x) || !Number.isFinite(n.y) || (n.x === 0 && n.y === 0)
  );
}

/**
 * Positions for every node, server coordinates preserved and the rest ringed.
 *
 * Layout is server-side, so a graph the layout pass has not reached yet
 * arrives with every node at the origin — and sigma draws all of them on top
 * of each other at one point, which reads as "the view is broken" rather than
 * as "no layout yet". Everything at (0,0) is therefore spread around a ring
 * instead. The placement is deterministic in the node ORDER the server sent,
 * so two consecutive polls of an unchanged graph produce an unchanged picture
 * — a random or time-seeded scatter would make the canvas jump every 30s.
 *
 * The ring sits outside whatever IS positioned (a partially laid-out graph
 * keeps its real structure in the middle and parks the new arrivals around
 * it), and falls back to a unit ring when nothing is positioned at all —
 * sigma normalizes coordinates to the viewport, so the absolute scale of a
 * graph that is nothing but the ring does not matter.
 */
export function withFallbackPositions(nodes: readonly GraphNode[]): Positioned[] {
  let extent = 0;
  let missing = 0;
  for (const n of nodes) {
    if (unpositioned(n)) missing++;
    else extent = Math.max(extent, Math.hypot(n.x, n.y));
  }
  const radius = extent > 0 ? extent * 1.15 : 1;
  const out: Positioned[] = [];
  let k = 0;
  for (const n of nodes) {
    if (!unpositioned(n)) {
      out.push({ id: n.id, x: n.x, y: n.y });
      continue;
    }
    const angle = missing > 0 ? (2 * Math.PI * k) / missing : 0;
    k++;
    out.push({
      id: n.id,
      x: radius * Math.cos(angle),
      y: radius * Math.sin(angle),
    });
  }
  return out;
}

// -------------------------------------------------------------- filtering

export interface GraphFilters {
  /**
   * Types NOT to draw. A hidden-set rather than a visible-set so a type the
   * extraction agent invents tomorrow shows up by default instead of silently
   * never rendering.
   */
  hiddenTypes: ReadonlySet<string>;
  /** false = current facts only; true = superseded ones too, dimmed. */
  includeInvalidated: boolean;
  /** Edges asserted by fewer than this many episodes are dropped. */
  minOccurrences: number;
}

export const DEFAULT_FILTERS: GraphFilters = {
  hiddenTypes: new Set<string>(),
  includeInvalidated: false,
  minOccurrences: 1,
};

/**
 * Has something superseded this fact?
 *
 * Two independent axes, both unix millis and both meaning "0 = not set":
 * `expired_at` is SYSTEM time (the row was retired) and `valid_until` is
 * WORLD time (the fact stopped holding). Either one is enough — a fact whose
 * world-validity has ended is superseded whether or not the row was closed.
 * `> 0` rather than truthiness, because 0 here is a real recorded answer and
 * not an absent key.
 */
export function isInvalidated(e: GraphEdge, now: number = Date.now()): boolean {
  // Two axes, one predicate — aligned with the Go side's graphContext filter:
  // system-time retired (expired_at stamped) OR world-time ENDED
  // (valid_until in the past). A FUTURE valid_until is a fact that is
  // currently true and known to end — "true until March" hasn't ended yet —
  // so it renders as current; the old `valid_until > 0` check wrongly
  // retired those, and hid from the canvas exactly the edges the spawn-path
  // injection was serving (review finding).
  return e.expired_at > 0 || (e.valid_until > 0 && e.valid_until <= now);
}

/** When it was superseded, unix millis, or 0. */
export function invalidatedAt(e: GraphEdge): number {
  return e.expired_at > 0 ? e.expired_at : e.valid_until > 0 ? e.valid_until : 0;
}

/**
 * A unix-millis instant as a local calendar day, matching how the mockup
 * writes an invalidation ("invalidated 2026-07-28") and how the DREAM view
 * names a night. Local rather than UTC for the same reason the dream loop's
 * day boundary is local: the reader is in one timezone, always this one.
 */
export function shortDate(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function edgeVisible(e: GraphEdge, f: GraphFilters): boolean {
  if (!f.includeInvalidated && isInvalidated(e)) return false;
  return (e.occurrences ?? 0) >= f.minOccurrences;
}

/** The node KEYS the type checkboxes have switched off. */
export function hiddenNodeIds(
  nodes: readonly GraphNode[],
  f: GraphFilters,
): Set<string> {
  const out = new Set<string>();
  for (const n of nodes) if (f.hiddenTypes.has(n.type)) out.add(nodeKey(n.id));
  return out;
}

/** Every type present in the payload, in first-seen order. */
export function nodeTypes(nodes: readonly GraphNode[]): string[] {
  const seen: string[] = [];
  const have = new Set<string>();
  for (const n of nodes) {
    if (have.has(n.type)) continue;
    have.add(n.type);
    seen.push(n.type);
  }
  return seen;
}

// ---------------------------------------------------------------- reducers

/**
 * What GraphView stores on each graphology node.
 *
 * `kind` rather than `type` is load-bearing and not a naming preference:
 * sigma reserves `type` on both nodes and edges for its RENDERER program name
 * ("circle", "line"), so storing an entity type there swaps the program out
 * for one that does not exist and the element stops drawing.
 */
export interface NodeAttrs {
  x: number;
  y: number;
  size: number;
  label: string;
  kind: string;
  degree: number;
}

export interface EdgeAttrs {
  size: number;
  occurrences: number;
  invalidated: boolean;
}

/**
 * Sigma REPLACES an element's display data with whatever the reducer returns
 * (sigma.esm.js: `attr = this.settings.nodeReducer(key, attr)`), so every
 * reducer below spreads `data` first — dropping it would strip x/y and sigma
 * throws "could not find a valid position".
 */
export function makeNodeReducer(
  hidden: ReadonlySet<string>,
  selected: string | null,
  palette: Palette,
): (key: string, data: NodeAttrs) => Partial<NodeDisplayData> {
  const ring = colorOf(SELECTED_VAR, palette);
  return (key, data) => {
    if (hidden.has(key)) return { ...data, hidden: true };
    if (key === selected) {
      return {
        ...data,
        color: ring,
        size: data.size * 1.4,
        highlighted: true,
        forceLabel: true,
        zIndex: 1,
      };
    }
    return { ...data, color: nodeColor(data.kind, palette) };
  };
}

/**
 * Edge visibility is the AND of its own filters and its endpoints': an edge
 * whose source the type rail switched off must go with it, or the canvas
 * fills with lines to nothing. Endpoint state arrives as the same `hidden`
 * set the node reducer uses rather than being re-derived per edge.
 */
export function makeEdgeReducer(
  f: GraphFilters,
  hidden: ReadonlySet<string>,
  byId: ReadonlyMap<string, GraphEdge>,
  palette: Palette,
): (key: string, data: EdgeAttrs) => Partial<EdgeDisplayData> {
  const base = colorOf(EDGE_VAR, palette);
  return (key, data) => {
    const e = byId.get(key);
    if (!e) return { ...data, hidden: true };
    if (hidden.has(nodeKey(e.src)) || hidden.has(nodeKey(e.dst))) {
      return { ...data, hidden: true };
    }
    if (!edgeVisible(e, f)) return { ...data, hidden: true };
    // An invalidated fact shown under "include invalidated" is drawn thin and
    // faint rather than in a colour of its own: it is the same relationship,
    // superseded, and the inspector's strike-through is where that is said in
    // words. Sigma's canvas edge program has no dash, so weight is the dial.
    if (data.invalidated) return { ...data, color: base, size: 0.4 };
    return { ...data, color: base };
  };
}
