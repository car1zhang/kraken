// reader.ts — the isoscene compiler's front end: one diagram card's HTML in,
// one SceneGraph (or a refusal) out. Everything it knows comes from the card's
// own markup channels — class attribute, data-code, text content, <g> nesting,
// geometry attributes and the id↔mpath binding — which is why no card has to
// change to become compilable.
//
// Two rules shape the whole file. Geometry is read from ATTRIBUTES ONLY: the
// tests run in jsdom, which has no layout engine, so getBBox() and
// getComputedStyle() are not available and a reader that reached for them
// would pass in a browser and fail everywhere else. And the parse verdict is
// WHOLE-CARD: a single element outside the grammar makes the card
// not-parseable rather than producing a scene with that element quietly
// dropped, because a silently partial diagram is worse than a flat one.

import type {
  BadgeMark,
  Conduit,
  EntityNode,
  ReadResult,
  SceneGraph,
  StepMark,
  TextMark,
  Treatment,
} from "./types.ts";

/** Component silhouettes: a `<g>` carrying one of these becomes an entity. */
const SHAPE = new Set([
  "proc", "cli", "thread", "fn", "agent", "llm", "timer", "iface", "event",
  "ui", "store", "table", "file", "queue", "type", "pkg", "machine", "cloud",
  "node",
]);

/** Parts: also entity-bearing on a `<g>`, but several are routed elsewhere
 * (see STRUCTURE below, plus `step`/`badge` which become marks, not nodes). */
const PART = new Set([
  "actor", "decision", "pill", "cell", "lane", "group", "region", "step",
  "badge", "bar",
]);

/** Connectives, text roles and paint marks — never entities. */
const CONNECTIVE = new Set([
  "edge", "edgeLabel", "arrowhead", "flow-dot", "axis", "title", "note",
  "spark",
]);

const MODIFIER = new Set(["holds", "head", "dashed", "dim"]);

const TREATMENT = new Set<string>([
  "grey", "red", "yellow", "green", "blue", "purple",
]);

/** The three parts that are floor structures rather than components. */
const STRUCTURE = new Set(["group", "region", "lane"]);

/** Text roles, in the priority order a multi-role class list resolves by. */
const TEXT_KINDS: ReadonlyArray<TextMark["kind"]> = ["title", "note", "edgeLabel"];

/** Accounted by tag alone, whatever class they carry: the svg envelope, the
 * marker plumbing, the SMIL elements legacy cards animate with, and tspan. */
const STRUCTURAL_TAGS = new Set([
  "svg", "defs", "marker", "mpath", "animate", "animatemotion", "tspan",
]);

/** Tags whose attributes carry a readable extent. `line`/`polygon`/`polyline`
 * are here for tolerance — no shipped card measured this session uses one as a
 * silhouette's primary geometry, but a classless one inside a part is
 * accounted markup and a reader that ignored it would report a zero box. */
const GEOMETRY_TAGS = new Set([
  "rect", "circle", "ellipse", "path", "polygon", "polyline", "line",
]);

const NUM = String.raw`[-+]?(?:\d+\.\d*|\.\d+|\d+)(?:[eE][-+]?\d+)?`;

/** The only transform the grammar accepts. Deliberately strict about the
 * separator: the contract enumerates `translate(a)`, `translate(a,b)` and
 * `translate(a, b)`, so a space-separated pair is not one of them. */
const TRANSLATE_RE = new RegExp(
  `^\\s*translate\\(\\s*(${NUM})\\s*(?:,\\s*(${NUM})\\s*)?\\)\\s*$`,
);

const PATH_TOKEN_RE = new RegExp(`([MmLlHhVvCcSsQqTtAaZz])|(${NUM})`, "g");

/** Argument count per path command, keyed by the lowercase letter. */
const PATH_ARITY: Record<string, number> = {
  m: 2, l: 2, h: 1, v: 1, c: 6, s: 4, q: 4, t: 2, a: 7, z: 0,
};

interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}

interface Point {
  x: number;
  y: number;
}

// ---------------------------------------------------------------------------
// Small attribute helpers
// ---------------------------------------------------------------------------

function tagOf(el: Element): string {
  return el.localName.toLowerCase();
}

function tokensOf(el: Element): string[] {
  const raw = el.getAttribute("class");
  if (!raw) return [];
  return raw.split(/\s+/).filter((t) => t.length > 0);
}

function knownToken(token: string): boolean {
  return (
    SHAPE.has(token) ||
    PART.has(token) ||
    CONNECTIVE.has(token) ||
    MODIFIER.has(token) ||
    TREATMENT.has(token)
  );
}

/** parseFloat that never leaks NaN/Infinity into the scene: a projector fed
 * one non-finite coordinate produces a scene whose every bound is NaN. */
function num(el: Element, name: string, fallback = 0): number {
  const raw = el.getAttribute(name);
  if (raw === null) return fallback;
  const n = Number.parseFloat(raw);
  return Number.isFinite(n) ? n : fallback;
}

function finite(n: number): number {
  return Number.isFinite(n) ? n : 0;
}

/** The element's own translate offset, or null when it carries no transform.
 * A transform that is present but not a translate cannot reach here — the
 * validation pass refuses the whole card first. */
function translateOf(el: Element): Point | null {
  const raw = el.getAttribute("transform");
  if (raw === null) return null;
  const m = TRANSLATE_RE.exec(raw);
  if (!m) return null;
  const x = Number.parseFloat(m[1] ?? "0");
  const y = m[2] === undefined ? 0 : Number.parseFloat(m[2]);
  return { x: finite(x), y: finite(y) };
}

function treatmentOf(tokens: string[]): Treatment | undefined {
  for (const t of tokens) if (TREATMENT.has(t)) return t as Treatment;
  return undefined;
}

function countTreatments(tokens: string[]): number {
  let n = 0;
  for (const t of tokens) if (TREATMENT.has(t)) n++;
  return n;
}

function shapeOf(tokens: string[]): string | undefined {
  for (const t of tokens) if (SHAPE.has(t) || PART.has(t)) return t;
  return undefined;
}

function textKindOf(tokens: string[]): TextMark["kind"] | undefined {
  for (const kind of TEXT_KINDS) if (tokens.includes(kind)) return kind;
  return undefined;
}

// ---------------------------------------------------------------------------
// Path tracing
// ---------------------------------------------------------------------------

interface PathTrace {
  start: Point | null;
  end: Point | null;
  box: Box | null;
}

/** Walk a `d` attribute to absolute coordinates. Relative commands, `H`/`V`
 * and `Z` all resolve, because real cards route edges as `M … H … V …` and a
 * reader that only understood `M`/`L` would report the wrong terminal point
 * for most conduits on a shipped card. Curve control points count as
 * coordinate pairs for the extent; arc parameters (radii, flags) do not — only
 * the arc's endpoint is a point. */
function tracePath(d: string): PathTrace {
  const items: Array<string | number> = [];
  for (const m of d.matchAll(PATH_TOKEN_RE)) {
    if (m[1] !== undefined) items.push(m[1]);
    else items.push(Number.parseFloat(m[2] ?? "0"));
  }

  let cx = 0;
  let cy = 0;
  let subX = 0;
  let subY = 0;
  let seen = false;
  let start: Point | null = null;
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;

  const mark = (x: number, y: number): void => {
    if (!Number.isFinite(x) || !Number.isFinite(y)) return;
    seen = true;
    if (x < minX) minX = x;
    if (y < minY) minY = y;
    if (x > maxX) maxX = x;
    if (y > maxY) maxY = y;
  };

  let cmd = "";
  let i = 0;
  while (i < items.length) {
    const head = items[i];
    if (typeof head === "string") {
      cmd = head;
      i++;
    } else if (cmd === "") {
      // Numbers before any command letter: malformed, and nothing sane to do
      // with them.
      i++;
      continue;
    }

    const lower = cmd.toLowerCase();
    const rel = cmd === lower && lower !== "z";
    const arity = PATH_ARITY[lower];
    if (arity === undefined) {
      cmd = "";
      continue;
    }

    if (arity === 0) {
      cx = subX;
      cy = subY;
      mark(cx, cy);
      cmd = "";
      continue;
    }

    let consumed = false;
    while (i + arity <= items.length) {
      let allNumbers = true;
      for (let k = 0; k < arity; k++) {
        if (typeof items[i + k] !== "number") {
          allNumbers = false;
          break;
        }
      }
      if (!allNumbers) break;
      const a = items.slice(i, i + arity) as number[];
      i += arity;
      consumed = true;

      const ax = (k: number): number => (rel ? cx : 0) + (a[k] ?? 0);
      const ay = (k: number): number => (rel ? cy : 0) + (a[k] ?? 0);

      switch (lower) {
        case "m": {
          const x = ax(0);
          const y = ay(1);
          cx = x;
          cy = y;
          subX = x;
          subY = y;
          if (start === null) start = { x, y };
          mark(x, y);
          break;
        }
        case "l": {
          const x = ax(0);
          const y = ay(1);
          cx = x;
          cy = y;
          mark(x, y);
          break;
        }
        case "h": {
          cx = ax(0);
          mark(cx, cy);
          break;
        }
        case "v": {
          cy = ay(0);
          mark(cx, cy);
          break;
        }
        case "c": {
          mark(ax(0), ay(1));
          mark(ax(2), ay(3));
          const x = ax(4);
          const y = ay(5);
          cx = x;
          cy = y;
          mark(x, y);
          break;
        }
        case "s":
        case "q": {
          mark(ax(0), ay(1));
          const x = ax(2);
          const y = ay(3);
          cx = x;
          cy = y;
          mark(x, y);
          break;
        }
        case "t": {
          const x = ax(0);
          const y = ay(1);
          cx = x;
          cy = y;
          mark(x, y);
          break;
        }
        case "a": {
          const x = ax(5);
          const y = ay(6);
          cx = x;
          cy = y;
          mark(x, y);
          break;
        }
      }

      // A repeated `M`/`m` argument pair is a lineto, per the SVG path
      // grammar — hand the rest of the run back to the outer loop as `L`/`l`.
      if (lower === "m") {
        cmd = rel ? "l" : "L";
        break;
      }
    }
    if (!consumed) cmd = "";
  }

  return {
    start,
    end: start === null && !seen ? null : { x: cx, y: cy },
    box: seen
      ? { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
      : null,
  };
}

function pointsExtent(raw: string | null): Box | null {
  if (!raw) return null;
  const nums = raw
    .split(/[\s,]+/)
    .filter((s) => s.length > 0)
    .map((s) => Number.parseFloat(s))
    .filter((n) => Number.isFinite(n));
  if (nums.length < 2) return null;
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (let i = 0; i + 1 < nums.length; i += 2) {
    const x = nums[i] ?? 0;
    const y = nums[i + 1] ?? 0;
    if (x < minX) minX = x;
    if (y < minY) minY = y;
    if (x > maxX) maxX = x;
    if (y > maxY) maxY = y;
  }
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY };
}

/** The extent this one element's attributes state. A rect reports itself
 * exactly (the primary rect IS the box a card author drew); circle and ellipse
 * report their bounding box; a path reports the extent of its traced absolute
 * coordinate pairs. */
function extentOf(el: Element): Box | null {
  switch (tagOf(el)) {
    case "rect":
      return {
        x: num(el, "x"),
        y: num(el, "y"),
        width: num(el, "width"),
        height: num(el, "height"),
      };
    case "circle": {
      const r = num(el, "r");
      return { x: num(el, "cx") - r, y: num(el, "cy") - r, width: 2 * r, height: 2 * r };
    }
    case "ellipse": {
      const rx = num(el, "rx");
      const ry = num(el, "ry");
      return {
        x: num(el, "cx") - rx,
        y: num(el, "cy") - ry,
        width: 2 * rx,
        height: 2 * ry,
      };
    }
    case "line": {
      const x1 = num(el, "x1");
      const y1 = num(el, "y1");
      const x2 = num(el, "x2");
      const y2 = num(el, "y2");
      return {
        x: Math.min(x1, x2),
        y: Math.min(y1, y2),
        width: Math.abs(x2 - x1),
        height: Math.abs(y2 - y1),
      };
    }
    case "polygon":
    case "polyline":
      return pointsExtent(el.getAttribute("points"));
    case "path":
      return tracePath(el.getAttribute("d") ?? "").box;
    default:
      return null;
  }
}

// ---------------------------------------------------------------------------
// The part's OWN content (label text, primary geometry)
// ---------------------------------------------------------------------------

/** Text lines of one `<text>`: its tspans when it has any, else its own text,
 * each trimmed. Whitespace-only lines are dropped — the parser hands back a
 * text node for every newline between tspans, and those are not label lines. */
function textLines(el: Element): string[] {
  const tspans = Array.from(el.children).filter((c) => tagOf(c) === "tspan");
  const source = tspans.length > 0 ? tspans : [el];
  const lines: string[] = [];
  for (const s of source) {
    const line = (s.textContent ?? "").trim();
    if (line.length > 0) lines.push(line);
  }
  return lines;
}

/** Label lines belonging to this part alone. Descent stops at any classed
 * descendant: a nested `<g class="store">`'s text is the store's label, and
 * hoisting it into the parent would put a child's name on the parent's face. */
function ownLabelLines(el: Element): string[] {
  const lines: string[] = [];
  const walk = (node: Element): void => {
    for (const child of Array.from(node.children)) {
      if (tokensOf(child).length > 0) continue;
      if (tagOf(child) === "text") lines.push(...textLines(child));
      else walk(child);
    }
  };
  walk(el);
  return lines;
}

/** The part's primary geometry: the first geometry element in document order
 * that belongs to the part itself, translate-resolved to absolute card
 * coordinates. Classed descendants are skipped for the same reason labels
 * skip them, and because a `spark` or an `arrowhead` is paint, not silhouette. */
function ownGeometry(el: Element, dx: number, dy: number): Box | null {
  for (const child of Array.from(el.children)) {
    if (tokensOf(child).length > 0) continue;
    const t = translateOf(child);
    const cdx = dx + (t?.x ?? 0);
    const cdy = dy + (t?.y ?? 0);
    if (GEOMETRY_TAGS.has(tagOf(child))) {
      const box = extentOf(child);
      if (box) {
        return {
          x: finite(box.x + cdx),
          y: finite(box.y + cdy),
          width: finite(box.width),
          height: finite(box.height),
        };
      }
    }
    const inner = ownGeometry(child, cdx, cdy);
    if (inner) return inner;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Pass 1 — the whole-card grammar verdict
// ---------------------------------------------------------------------------

function describe(el: Element, offendingToken?: string): string {
  const token = offendingToken ?? tokensOf(el)[0];
  return token === undefined ? `<${tagOf(el)}>` : `<${tagOf(el)} class="${token}">`;
}

/** Document-order scan for the FIRST element that steps outside the grammar.
 * Returns the refusal reason, or undefined when the whole subtree is
 * accounted. `underClassed` tracks rule (c): a classless element is accounted
 * only as the internals of an accounted CLASSED ancestor, which is what keeps
 * a stray top-level `<path>` (the bespoke case) from reading as a silhouette. */
function findOffence(el: Element, underClassed: boolean): string | undefined {
  const tokens = tokensOf(el);
  const classAccounted = tokens.length > 0 && tokens.every(knownToken);
  const accounted =
    STRUCTURAL_TAGS.has(tagOf(el)) ||
    classAccounted ||
    (tokens.length === 0 && underClassed);

  if (!accounted) {
    const unknown = tokens.find((t) => !knownToken(t));
    return `outside the parts-kit grammar: ${describe(el, unknown)}`;
  }

  const transform = el.getAttribute("transform");
  if (transform !== null && !TRANSLATE_RE.test(transform)) {
    return `only translate() transforms are supported, found "${transform}" on ${describe(el)}`;
  }

  if (countTreatments(tokens) > 1) {
    const treats = tokens.filter((t) => TREATMENT.has(t)).join(", ");
    return `more than one treatment (${treats}) on ${describe(el)}`;
  }

  const nextUnderClassed = underClassed || classAccounted;
  for (const child of Array.from(el.children)) {
    const offence = findOffence(child, nextUnderClassed);
    if (offence !== undefined) return offence;
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Pass 2 — the scene graph
// ---------------------------------------------------------------------------

interface FlowBinding {
  edgeId: string;
  dur: string;
  begin: string;
}

interface BuildState {
  entities: EntityNode[];
  structures: EntityNode[];
  conduits: Conduit[];
  texts: TextMark[];
  steps: StepMark[];
  badges: BadgeMark[];
  flows: FlowBinding[];
  next: number;
}

function makeConduit(el: Element, tokens: string[], dx: number, dy: number): Conduit {
  const d = el.getAttribute("d") ?? "";
  const trace = tracePath(d);
  const id = el.getAttribute("id");
  const codeRef = el.getAttribute("data-code");
  return {
    ...(id !== null && id.length > 0 ? { id } : {}),
    d,
    x1: finite((trace.start?.x ?? 0) + dx),
    y1: finite((trace.start?.y ?? 0) + dy),
    x2: finite((trace.end?.x ?? 0) + dx),
    y2: finite((trace.end?.y ?? 0) + dy),
    dashed: tokens.includes("dashed"),
    treatment: treatmentOf(tokens),
    dim: tokens.includes("dim"),
    ...(codeRef !== null ? { codeRef } : {}),
  };
}

/** The legacy flow-dot binding: the dot's own animateMotion carries the timing
 * and its mpath names the edge. New cards ship no SMIL at all, so this is
 * emphasis input the renderer may use and never a precondition for a scene. */
function readFlow(el: Element): FlowBinding | null {
  const motion = Array.from(el.getElementsByTagName("*")).find(
    (c) => tagOf(c) === "animatemotion",
  );
  if (!motion) return null;
  const mpath = Array.from(motion.getElementsByTagName("*")).find(
    (c) => tagOf(c) === "mpath",
  );
  if (!mpath) return null;
  const href =
    mpath.getAttribute("href") ??
    mpath.getAttribute("xlink:href") ??
    mpath.getAttributeNS("http://www.w3.org/1999/xlink", "href");
  if (!href || !href.startsWith("#")) return null;
  return {
    edgeId: href.slice(1),
    dur: motion.getAttribute("dur") ?? "",
    begin: motion.getAttribute("begin") ?? "",
  };
}

function build(el: Element, dx: number, dy: number, parent: EntityNode | null, st: BuildState): void {
  const tokens = tokensOf(el);
  const t = translateOf(el);
  const ox = dx + (t?.x ?? 0);
  const oy = dy + (t?.y ?? 0);
  const tag = tagOf(el);

  let nextParent = parent;
  const shape = tag === "g" ? shapeOf(tokens) : undefined;

  if (shape !== undefined && shape === "step") {
    const box = ownGeometry(el, ox, oy);
    st.steps.push({
      label: ownLabelLines(el).join(" "),
      x: box?.x ?? ox,
      y: box?.y ?? oy,
    });
  } else if (shape !== undefined && shape === "badge") {
    const box = ownGeometry(el, ox, oy);
    st.badges.push({
      label: ownLabelLines(el).join(" "),
      x: box?.x ?? ox,
      y: box?.y ?? oy,
    });
  } else if (shape !== undefined) {
    const box = ownGeometry(el, ox, oy);
    const codeRef = el.getAttribute("data-code");
    const node: EntityNode = {
      id: `n${st.next++}`,
      shape,
      labelLines: ownLabelLines(el),
      treatment: treatmentOf(tokens),
      dim: tokens.includes("dim"),
      holds: tokens.includes("holds"),
      head: tokens.includes("head"),
      ...(codeRef !== null ? { codeRef } : {}),
      x: box?.x ?? ox,
      y: box?.y ?? oy,
      width: box?.width ?? 0,
      height: box?.height ?? 0,
      children: [],
    };
    if (parent) parent.children.push(node);
    else if (STRUCTURE.has(shape)) st.structures.push(node);
    else st.entities.push(node);
    nextParent = node;
  } else if (tag === "path" && tokens.includes("edge")) {
    st.conduits.push(makeConduit(el, tokens, ox, oy));
  } else if (tokens.includes("flow-dot")) {
    const flow = readFlow(el);
    if (flow) st.flows.push(flow);
  } else {
    const kind = textKindOf(tokens);
    if (kind !== undefined) {
      st.texts.push({
        kind,
        lines: textLines(el),
        x: finite(num(el, "x") + ox),
        y: finite(num(el, "y") + oy),
        dim: tokens.includes("dim"),
      });
    }
  }

  for (const child of Array.from(el.children)) build(child, ox, oy, nextParent, st);
}

function readViewBox(svg: Element): { width: number; height: number } {
  const raw = svg.getAttribute("viewBox") ?? svg.getAttribute("viewbox") ?? "";
  const nums = raw
    .split(/[\s,]+/)
    .filter((s) => s.length > 0)
    .map((s) => Number.parseFloat(s));
  const w = nums[2];
  const h = nums[3];
  return {
    width: w !== undefined && Number.isFinite(w) ? w : 0,
    height: h !== undefined && Number.isFinite(h) ? h : 0,
  };
}

/**
 * Read one diagram card's HTML into a SceneGraph.
 *
 * The three outcomes are the card's own doing, not a quality judgement:
 * `not-diagram` means there was no `figure.diagram` svg to read at all,
 * `not-parseable` means the card is a bespoke drawing that steps outside the
 * parts kit (it displays as-is), and `scene` means every element was
 * accounted for.
 */
export function readCard(html: string): ReadResult {
  let svg: Element | null = null;
  try {
    const doc = new DOMParser().parseFromString(html, "text/html");
    svg = doc.querySelector("figure.diagram svg");
  } catch {
    return { kind: "not-diagram" };
  }
  if (!svg) return { kind: "not-diagram" };

  try {
    const offence = findOffence(svg, false);
    if (offence !== undefined) return { kind: "not-parseable", reason: offence };

    const st: BuildState = {
      entities: [],
      structures: [],
      conduits: [],
      texts: [],
      steps: [],
      badges: [],
      flows: [],
      next: 0,
    };
    for (const child of Array.from(svg.children)) build(child, 0, 0, null, st);

    // The flow bindings are applied after the walk because a flow-dot may sit
    // anywhere in the svg relative to the edge it names — in every measured
    // card the dots are last, but the grammar does not require it.
    for (const flow of st.flows) {
      const target = st.conduits.find((c) => c.id === flow.edgeId);
      if (target) target.flow = { dur: flow.dur, begin: flow.begin };
    }

    const title = st.texts.find((t) => t.kind === "title");
    const scene: SceneGraph = {
      title: title ? title.lines.join(" ") : "",
      viewBox: readViewBox(svg),
      entities: st.entities,
      structures: st.structures,
      conduits: st.conduits,
      texts: st.texts,
      steps: st.steps,
      badges: st.badges,
    };
    return { kind: "scene", scene };
  } catch (err) {
    // A throw here is a reader defect, not a card defect — but a half-built
    // scene is the one outcome the whole-card rule exists to prevent, so the
    // card reads as bespoke and displays flat rather than blank.
    return {
      kind: "not-parseable",
      reason: `reader failed: ${err instanceof Error ? err.message : String(err)}`,
    };
  }
}
