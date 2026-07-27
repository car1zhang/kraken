import { describe, expect, it } from "vitest";

import type { WebJob, WebPrereq } from "../api/types.ts";
import {
  CANVAS_PAD,
  GAP_X,
  GAP_Y,
  NODE_H,
  NODE_W,
  dagEdges,
  dagLayers,
  dagLayout,
} from "./dagLayers.ts";

function job(slug: string, prereqs: (string | WebPrereq)[] = []): WebJob {
  return {
    slug,
    repo: "kraken",
    state: "draft",
    live: false,
    mtime: 0,
    prereqs: prereqs.map((p) =>
      typeof p === "string" ? { job: p, gated: false, done: false } : p,
    ),
  };
}

/** `prereqs` is omitempty on the wire, so the field is genuinely absent. */
function bareJob(slug: string): WebJob {
  return { slug, repo: "kraken", state: "draft", live: false, mtime: 0 };
}

const depthsOf = (jobs: WebJob[]) => Object.fromEntries(dagLayers(jobs));

describe("dagLayers", () => {
  it("puts a job with no prereqs at depth 0", () => {
    expect(depthsOf([job("solo")])).toEqual({ solo: 0 });
  });

  it("handles an empty board without producing NaN geometry", () => {
    expect(depthsOf([])).toEqual({});
    const { pos, width, height } = dagLayout([]);
    expect(pos.size).toBe(0);
    // Math.max(0, ...[]) is Math.max(0) — the seed is what keeps this finite
    expect(width).toBe(CANVAS_PAD);
    expect(height).toBe(CANVAS_PAD);
  });

  it("treats an ABSENT prereqs field the same as an empty one", () => {
    // `json:"prereqs,omitempty"` — the server omits the key entirely
    expect(depthsOf([bareJob("a")])).toEqual({ a: 0 });
    expect(dagEdges([bareJob("a")], dagLayout([bareJob("a")]).pos)).toEqual([]);
  });

  it("is immune to a slug that names an Object.prototype member", () => {
    // Vanilla used object literals, so `depth["toString"]` was defined via the
    // prototype: the dep passed both the filter and the every, settled at
    // 1 + Math.max(<function>) = NaN, and left:NaNpx collapsed the whole
    // canvas. Set/Map have no such member.
    const jobs = [job("toString"), job("b", ["toString"]), job("c", ["constructor"])];
    const d = depthsOf(jobs);
    expect(d["toString"]).toBe(0);
    expect(d["b"]).toBe(1);
    // `constructor` is not on the board, so it is filtered, not resolved
    expect(d["c"]).toBe(0);
    expect(Number.isNaN(dagLayout(jobs).width)).toBe(false);
  });

  it("walks a linear chain one column per hop", () => {
    const d = depthsOf([job("a"), job("b", ["a"]), job("c", ["b"])]);
    expect(d).toEqual({ a: 0, b: 1, c: 2 });
  });

  it("settles a diamond at the DEEPEST prereq, not the first", () => {
    //   a → b ┐
    //     ↘ c ┴→ d      d must be 2, and stays 2 however the array is ordered
    const jobs = [job("d", ["b", "c"]), job("b", ["a"]), job("c", ["a"]), job("a")];
    expect(depthsOf(jobs)).toEqual({ a: 0, b: 1, c: 1, d: 2 });
  });

  it("ignores prereqs naming a job that is not on the board", () => {
    // an archived-away prereq must not make its dependent look unresolvable
    expect(depthsOf([job("b", ["gone"])])).toEqual({ b: 0 });
  });

  it("dumps cycle members into maxD+1 and terminates", () => {
    // a → b is the deepest real chain (maxD 1); x ⇄ y never settles
    const jobs = [job("a"), job("b", ["a"]), job("x", ["y"]), job("y", ["x"])];
    const d = depthsOf(jobs);
    expect(d).toEqual({ a: 0, b: 1, x: 2, y: 2 });
    // every job is placed — a cycle must never silently drop a node
    expect(Object.keys(d)).toHaveLength(4);
  });

  it("handles a self-cycle", () => {
    const d = depthsOf([job("a"), job("loop", ["loop"])]);
    expect(d).toEqual({ a: 0, loop: 1 });
  });

  it("terminates on a graph that is ENTIRELY a cycle", () => {
    // nothing ever settles, so maxD falls back to 0 and every member is 1
    expect(depthsOf([job("x", ["y"]), job("y", ["x"])])).toEqual({ x: 1, y: 1 });
  });
});

describe("dagLayout", () => {
  it("spaces columns by depth and rows by within-layer index", () => {
    const { pos, width, height } = dagLayout([job("a"), job("b", ["a"])]);
    expect(pos.get("a")).toEqual({ x: 0, y: 0 });
    expect(pos.get("b")).toEqual({ x: NODE_W + GAP_X, y: 0 });
    expect(width).toBe(NODE_W + GAP_X + NODE_W + CANVAS_PAD);
    expect(height).toBe(NODE_H + CANVAS_PAD);
  });

  it("sorts each layer by slug so the picture is stable across ticks", () => {
    const { pos } = dagLayout([job("zeta"), job("alpha")]);
    expect(pos.get("alpha")).toEqual({ x: 0, y: 0 });
    expect(pos.get("zeta")).toEqual({ x: 0, y: NODE_H + GAP_Y });
  });
});

describe("dagEdges", () => {
  it("draws a cubic bezier whose control points share the midpoint x", () => {
    const jobs = [job("a"), job("b", ["a"])];
    const { pos } = dagLayout(jobs);
    const edges = dagEdges(jobs, pos);
    expect(edges).toHaveLength(1);
    const x1 = NODE_W;
    const y1 = NODE_H / 2;
    const x2 = NODE_W + GAP_X;
    const mx = (x1 + x2) / 2;
    expect(edges[0]?.d).toBe(`M${x1},${y1} C${mx},${y1} ${mx},${y1} ${x2},${y1}`);
    expect(edges[0]?.cls).toBe("");
    expect(edges[0]?.flag).toBeUndefined();
  });

  it("marks a gated edge and gives it a ⚑ position", () => {
    const jobs = [job("a"), job("b", [{ job: "a", gated: true, done: false }])];
    const edges = dagEdges(jobs, dagLayout(jobs).pos);
    expect(edges[0]?.cls).toBe("gated");
    expect(edges[0]?.flag).toBeDefined();
  });

  it("classes a satisfied ungated edge as done", () => {
    const jobs = [job("a"), job("b", [{ job: "a", gated: false, done: true }])];
    expect(dagEdges(jobs, dagLayout(jobs).pos)[0]?.cls).toBe("done");
  });

  it("skips an edge whose endpoint is not placed", () => {
    const jobs = [job("b", ["gone"])];
    expect(dagEdges(jobs, dagLayout(jobs).pos)).toHaveLength(0);
  });

  it("gives DUPLICATE prereqs distinct keys", () => {
    // parsePrereqs (internal/jobs/job.go) appends every `- job:` item and does
    // not even flag a repeat, so a hand-edited job file reaches here as two
    // identical edges. One shared key means React keeps one fiber and the ⚑
    // on the gated twin may never render.
    const jobs = [
      job("a"),
      job("b", [
        { job: "a", gated: false, done: false },
        { job: "a", gated: true, done: false },
      ]),
    ];
    const edges = dagEdges(jobs, dagLayout(jobs).pos);
    expect(edges).toHaveLength(2);
    expect(new Set(edges.map((e) => e.key)).size).toBe(2);
    // the gated twin survives with its marker intact
    expect(edges.filter((e) => e.cls === "gated")).toHaveLength(1);
  });

  it("keys are unique even when a slug itself contains the -> separator", () => {
    // `a` + `b->c` and `a->b` + `c` both flatten to "a->b->c" without the index
    const jobs = [job("a"), job("b->c", ["a"]), job("a->b"), job("c", ["a->b"])];
    const edges = dagEdges(jobs, dagLayout(jobs).pos);
    expect(new Set(edges.map((e) => e.key)).size).toBe(edges.length);
  });
});
