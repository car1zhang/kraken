// DAG layout math, ported verbatim from web/static/app.js:3457-3484 (the
// geometry constants and dagLayers) plus the position pass at the head of
// renderDag() :3485.
//
// Kept out of the component on purpose: this is the whole of what the DAG
// view can get wrong, and it is pure, so it is the part worth unit testing.

import type { WebJob } from "../api/types.ts";

export const NODE_W = 190;
export const NODE_H = 52;
export const GAP_X = 90;
export const GAP_Y = 22;

/** The margin renderDag() adds past the last node, on both axes. */
export const CANVAS_PAD = 20;

/**
 * Assigns each job a column: roots at 0, dependents one past their deepest
 * prereq.
 *
 * The assignment is an iterative FIXED POINT rather than a recursive walk, and
 * that is what makes it total on a cyclic prereq graph: each pass only settles
 * jobs whose every in-graph prereq is already settled, so a cycle simply never
 * settles and the loop stops making progress instead of recursing forever.
 * Those unsettled jobs — the cycle members, already flagged as an error by the
 * job parser — are then DUMPED INTO `maxD + 1` rather than dropped, because a
 * silently missing node is exactly the state a broken prereq graph must not
 * render as.
 *
 * Prereqs naming a job that is not on the board (archived away, deleted) are
 * filtered out before the check — otherwise every dependent of a vanished job
 * would look like a cycle member.
 */
export function dagLayers(jobs: WebJob[]): Map<string, number> {
  const present = new Set(jobs.map((j) => j.slug));
  const depth = new Map<string, number>();
  let progress = true;
  while (progress) {
    progress = false;
    for (const j of jobs) {
      if (depth.has(j.slug)) continue;
      const deps = (j.prereqs ?? []).filter((p) => present.has(p.job));
      if (deps.every((p) => depth.has(p.job))) {
        depth.set(
          j.slug,
          // The `?? 0` is the noUncheckedIndexedAccess tax: the `every` on the
          // line above has just proven every one of these is present.
          deps.length ? 1 + Math.max(...deps.map((p) => depth.get(p.job) ?? 0)) : 0,
        );
        progress = true;
      }
    }
  }
  const maxD = Math.max(0, ...depth.values());
  for (const j of jobs) if (!depth.has(j.slug)) depth.set(j.slug, maxD + 1);
  return depth;
}

export interface DagPos {
  x: number;
  y: number;
}

export interface DagLayout {
  depth: Map<string, number>;
  pos: Map<string, DagPos>;
  /** maxX + CANVAS_PAD — the canvas/svg width. */
  width: number;
  /** maxY + CANVAS_PAD — the canvas/svg/#dag-nodes height. */
  height: number;
}

/**
 * Places every job: x from its depth, y from its index within its layer, with
 * each layer sorted by slug so the picture is stable across ticks (the wire
 * order is board order, which moves).
 */
export function dagLayout(jobs: WebJob[]): DagLayout {
  const depth = dagLayers(jobs);
  const layers = new Map<number, WebJob[]>();
  for (const j of jobs) {
    const d = depth.get(j.slug) ?? 0;
    const bucket = layers.get(d);
    if (bucket) bucket.push(j);
    else layers.set(d, [j]);
  }
  const pos = new Map<string, DagPos>();
  let maxX = 0;
  let maxY = 0;
  for (const d of [...layers.keys()].sort((a, b) => a - b)) {
    const js = layers.get(d) ?? [];
    js.sort((a, b) => a.slug.localeCompare(b.slug));
    js.forEach((j, i) => {
      const x = d * (NODE_W + GAP_X);
      const y = i * (NODE_H + GAP_Y);
      pos.set(j.slug, { x, y });
      maxX = Math.max(maxX, x + NODE_W);
      maxY = Math.max(maxY, y + NODE_H);
    });
  }
  return { depth, pos, width: maxX + CANVAS_PAD, height: maxY + CANVAS_PAD };
}

export interface DagEdge {
  /**
   * React key, injective in (prereq index, prereq slug, dependent slug) BY
   * CONSTRUCTION — see edgeKey. Two things force that:
   *
   *   1. parsePrereqs (internal/jobs/job.go) appends every `- job:` item
   *      unconditionally — a repeat is not even an error — so a hand-edited
   *      job file naming one prereq twice reaches here as two edges. React
   *      keeps ONE fiber per key, so which twin survived (and therefore
   *      whether the ⚑ gate marker rendered at all) would come down to
   *      reconciliation order. Hence the index.
   *   2. `a` + `b->c` and `a->b` + `c` both flatten to `a->b->c`, and both
   *      are at prereq index 0, so the index alone does not save it. Hence
   *      the length prefix.
   *
   * util.Slugify would rule (2) out for anything created through the board,
   * but a job file dropped into ~/Documents/pluto by hand never passes
   * through it and neither does a hand-typed `job:` value.
   */
  key: string;
  /** The <path> class, straight off app.js:3546. Client-computed, so it is a
   * closed set — nothing on the wire can add a value here. */
  cls: "gated" | "done" | "";
  d: string;
  /** Present only on a gated edge: the ⚑ marker's position. */
  flag?: { x: number; y: number };
}

/**
 * The prereq slug is LENGTH-PREFIXED, which is what makes the result injective:
 * the count says exactly where `from` ends, so no pair of slugs can flatten
 * into another pair's key however many separators they contain.
 */
function edgeKey(from: string, to: string, i: number): string {
  return `${i}|${from.length}|${from}->${to}`;
}

/**
 * Builds one cubic bezier per prereq edge. The control points share the
 * midpoint x of the two endpoints (`M x1,y1 C mx,y1 mx,y2 x2,y2`), which is
 * what gives the flat-then-curve-then-flat shape at every fan-out.
 */
export function dagEdges(jobs: WebJob[], pos: Map<string, DagPos>): DagEdge[] {
  const edges: DagEdge[] = [];
  for (const j of jobs) {
    (j.prereqs ?? []).forEach((p, i) => {
      const from = pos.get(p.job);
      const to = pos.get(j.slug);
      // A prereq naming a job that is not on the board draws nothing.
      if (!from || !to) return;
      const x1 = from.x + NODE_W;
      const y1 = from.y + NODE_H / 2;
      const x2 = to.x;
      const y2 = to.y + NODE_H / 2;
      const mx = (x1 + x2) / 2;
      const edge: DagEdge = {
        key: edgeKey(p.job, j.slug, i),
        cls: p.gated ? "gated" : p.done ? "done" : "",
        d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
      };
      if (p.gated) edge.flag = { x: mx - 5, y: (y1 + y2) / 2 - 4 };
      edges.push(edge);
    });
  }
  return edges;
}
