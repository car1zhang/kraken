// types.ts — the isoscene compiler's shared types, frozen by the set's
// 50-contract.md. The reader (reader.ts) produces a SceneGraph from a diagram
// card's HTML, the projector (project.ts) maps it onto the isometric floor,
// and the renderer (render.ts) emits the compiled inline SVG. Everything here
// is exported because TypeScript tests can only reach exported names — the
// export is part of the frozen contract, not a style choice.

/** The six semantic colour treatments a card element can carry. */
export type Treatment = "grey" | "red" | "yellow" | "green" | "blue" | "purple";

/** One component box or part read from the card. `shape` is the shape class
 * exactly as written (`proc`, `ui`, `node`, `pill`, `decision`, `cell`, …);
 * the reader never maps an unknown KIND to anything — kind text is content,
 * not a class, and an unlisted kind arrives here as whatever shape class the
 * card gave it (the `node` fallback is the card author's move, not ours). */
export interface EntityNode {
  /** Synthetic id, stable within one read: "n0", "n1", … in document order. */
  id: string;
  shape: string;
  /** Text lines of the part's own label(s), tspan-split, document order. */
  labelLines: string[];
  treatment?: Treatment;
  dim: boolean;
  holds: boolean;
  /** `cell head` header-cell modifier. */
  head: boolean;
  /** data-code attribute value, verbatim, when present. */
  codeRef?: string;
  /** Absolute card coordinates (all translate() offsets resolved). For
   * rect-backed shapes these are the primary rect's exact values; for
   * path-only silhouettes an extent derived from the path's absolute
   * coordinate pairs. */
  x: number;
  y: number;
  width: number;
  height: number;
  /** Component gs nested inside this one (shape-inside-shape containment). */
  children: EntityNode[];
}

/** One edge path. */
export interface Conduit {
  /** The path's own id attribute, when it has one. */
  id?: string;
  /** The d attribute, verbatim. */
  d: string;
  /** Absolute start and end points of the path (translate-resolved). */
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  dashed: boolean;
  treatment?: Treatment;
  dim: boolean;
  codeRef?: string;
  /** Present iff a legacy flow-dot's mpath names this edge's id: the dot's
   * animateMotion dur and begin, verbatim. New cards carry no SMIL, so this
   * is emphasis input only — never required for the conduit to animate. */
  flow?: { dur: string; begin: string };
}

/** title / note / edgeLabel text, with resolved anchor position. */
export interface TextMark {
  kind: "title" | "note" | "edgeLabel";
  lines: string[];
  x: number;
  y: number;
  dim: boolean;
}

/** A step badge; label text is opaque (real cards contain non-numeric steps
 * like "3b"). */
export interface StepMark {
  label: string;
  x: number;
  y: number;
}

/** A count badge (×800, 5 min, p99). */
export interface BadgeMark {
  label: string;
  x: number;
  y: number;
}

/** The card, read semantically. `structures` holds the floor-structure parts
 * (`group`, `region`, `lane`); a group's markup-nested children live in its
 * own `children`. Everything else classed lands in `entities` (components and
 * freestanding parts alike, document order). */
export interface SceneGraph {
  /** The svg's required `.title` text (takeaway line). */
  title: string;
  viewBox: { width: number; height: number };
  entities: EntityNode[];
  structures: EntityNode[];
  conduits: Conduit[];
  texts: TextMark[];
  steps: StepMark[];
  badges: BadgeMark[];
}

export type ReadResult =
  | { kind: "scene"; scene: SceneGraph }
  /** The card holds a figure.diagram svg but its markup steps outside the
   * grammar — a bespoke concept diagram. It displays as-is; `reason` names
   * the first offending element for the dev console. */
  | { kind: "not-parseable"; reason: string }
  /** No figure.diagram svg envelope at all (not a diagram card). */
  | { kind: "not-diagram" };

/** A projected screen point. */
export interface IsoPoint {
  sx: number;
  sy: number;
}

/** One entity's projected placement: the iso projection of its card (x, y)
 * origin, with the scene's global margin offset already applied. */
export interface Placement {
  nodeId: string;
  base: IsoPoint;
}

export interface ProjectedConduit {
  conduit: Conduit;
  start: IsoPoint;
  end: IsoPoint;
}

export interface ProjectedScene {
  scene: SceneGraph;
  /** Emitted viewBox dimensions. */
  width: number;
  height: number;
  /** The one global offset every projected point carries (P3's rigidity).
   * Exposed so the renderer can project arbitrary card points — a box's top
   * face, a mark's anchor — into the same frame without re-deriving the
   * offset from a placement that a marks-only scene does not have (additive;
   * nothing in the blind suites reads it). */
  offset: IsoPoint;
  /** Keyed by EntityNode.id; includes nested children's ids. */
  placements: Map<string, Placement>;
  conduits: ProjectedConduit[];
}

export interface RenderOptions {
  /** True renders the resting frame: the emitted markup contains no SMIL
   * element at all (no <animate>, no <animateMotion>). */
  reducedMotion: boolean;
}

export type CompileResult =
  | { kind: "scene"; svg: string }
  | { kind: "not-parseable"; reason: string }
  | { kind: "not-diagram" };
