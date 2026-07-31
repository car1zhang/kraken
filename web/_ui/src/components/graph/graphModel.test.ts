import { describe, expect, it } from "vitest";

import type { GraphEdge, GraphNode } from "../../hooks/useGraph.ts";
import {
  DEFAULT_FILTERS,
  edgeVisible,
  hiddenNodeIds,
  invalidatedAt,
  isInvalidated,
  makeEdgeReducer,
  makeNodeReducer,
  nodeColor,
  nodeKey,
  nodeSize,
  nodeTypes,
  PALETTE_FALLBACK,
  readGraphPalette,
  shortDate,
  withFallbackPositions,
  type EdgeAttrs,
  type GraphFilters,
  type NodeAttrs,
} from "./graphModel.ts";

/**
 * WIRE FIXTURES — every field of `/api/graph`'s node and edge objects, at the
 * types ios/API.md gives them (ids are NUMBERS, both validity columns are
 * unix millis and always present). A renamed or retyped key stops this file
 * compiling, which is the same mechanism DreamView.test.tsx documents for its
 * own two literals.
 */
const node = (over: Partial<GraphNode> = {}): GraphNode => ({
  id: 1,
  name: "artifact prose",
  type: "convention",
  description: "how markdown artifacts are formatted",
  x: 3,
  y: 4,
  ...over,
});

const edge = (over: Partial<GraphEdge> = {}): GraphEdge => ({
  id: 1,
  src: 1,
  dst: 2,
  predicate: "prefers",
  fact: "carl prefers markdown artifacts unwrapped",
  strength: 9,
  occurrences: 2,
  valid_until: 0,
  expired_at: 0,
  episode: 4412,
  ...over,
});

const filters = (over: Partial<GraphFilters> = {}): GraphFilters => ({
  ...DEFAULT_FILTERS,
  ...over,
});

/** graphology attributes, as GraphView builds them. */
const attrs = (over: Partial<NodeAttrs> = {}): NodeAttrs => ({
  x: 1,
  y: 2,
  size: 6,
  label: "artifact prose",
  kind: "convention",
  degree: 4,
  ...over,
});

const eattrs = (over: Partial<EdgeAttrs> = {}): EdgeAttrs => ({
  size: 1,
  occurrences: 2,
  invalidated: false,
  ...over,
});

describe("invalidation", () => {
  // 0 is a real recorded answer on both columns, never an absent key.
  it("reads either time axis, and 0 on both means current", () => {
    expect(isInvalidated(edge())).toBe(false);
    expect(isInvalidated(edge({ expired_at: 1769040000000 }))).toBe(true);
    // World time PAST: ended, so invalidated.
    expect(isInvalidated(edge({ valid_until: 1769040000000 }), 1790000000000)).toBe(true);
    // World time FUTURE: "true until then" is currently true — must render
    // as current, exactly matching the Go injection filter.
    expect(isInvalidated(edge({ valid_until: 1990000000000 }), 1790000000000)).toBe(false);
    expect(invalidatedAt(edge())).toBe(0);
    expect(invalidatedAt(edge({ valid_until: 1769040000000 }))).toBe(
      1769040000000,
    );
    // system time wins when both are set: it is when the row actually closed
    expect(
      invalidatedAt(edge({ valid_until: 1, expired_at: 1769040000000 })),
    ).toBe(1769040000000);
  });

  it("renders an instant as a local calendar day, and 0 as nothing", () => {
    expect(shortDate(0)).toBe("");
    expect(shortDate(Number.NaN)).toBe("");
    const d = new Date(2026, 6, 28, 12, 0, 0);
    expect(shortDate(d.getTime())).toBe("2026-07-28");
  });
});

describe("edgeVisible", () => {
  it("drops invalidated facts unless they are asked for", () => {
    const dead = edge({ expired_at: 1769040000000 });
    expect(edgeVisible(dead, filters())).toBe(false);
    expect(edgeVisible(dead, filters({ includeInvalidated: true }))).toBe(true);
  });

  it("applies the occurrence floor", () => {
    expect(edgeVisible(edge({ occurrences: 1 }), filters({ minOccurrences: 2 }))).toBe(
      false,
    );
    expect(edgeVisible(edge({ occurrences: 2 }), filters({ minOccurrences: 2 }))).toBe(
      true,
    );
    // the floor and the validity filter are an AND, not an OR
    expect(
      edgeVisible(
        edge({ occurrences: 9, expired_at: 1769040000000 }),
        filters({ minOccurrences: 2 }),
      ),
    ).toBe(false);
  });
});

describe("hiddenNodeIds", () => {
  it("hides exactly the switched-off types, by sigma key", () => {
    const nodes = [
      node({ id: 1, type: "convention" }),
      node({ id: 2, type: "file" }),
      node({ id: 3, type: "file" }),
    ];
    const hidden = hiddenNodeIds(nodes, filters({ hiddenTypes: new Set(["file"]) }));
    expect([...hidden].sort()).toEqual(["2", "3"]);
  });

  // The rail is a HIDDEN-set, so a type the extraction agent invents tomorrow
  // renders by default instead of silently never appearing.
  it("shows a type nobody has an opinion about", () => {
    expect(hiddenNodeIds([node({ type: "brand-new-kind" })], filters()).size).toBe(0);
  });
});

describe("nodeTypes", () => {
  it("is the distinct types in first-seen order", () => {
    expect(
      nodeTypes([
        node({ id: 1, type: "decision" }),
        node({ id: 2, type: "file" }),
        node({ id: 3, type: "decision" }),
      ]),
    ).toEqual(["decision", "file"]);
  });
});

describe("withFallbackPositions", () => {
  it("leaves server coordinates alone", () => {
    expect(withFallbackPositions([node({ id: 7, x: 12, y: -4 })])).toEqual([
      { id: 7, x: 12, y: -4 },
    ]);
  });

  // A graph the layout pass has not reached serves 0,0 for every node
  // (ios/API.md says so explicitly), and sigma stacks them all on one pixel.
  // The ring is what makes that read as "no layout yet" rather than as "the
  // view is broken".
  it("rings a graph with no layout at all, at distinct points", () => {
    const nodes = [1, 2, 3, 4].map((id) => node({ id, x: 0, y: 0 }));
    const out = withFallbackPositions(nodes);
    const seen = new Set(out.map((p) => `${p.x.toFixed(6)},${p.y.toFixed(6)}`));
    expect(seen.size).toBe(4);
    for (const p of out) expect(Math.hypot(p.x, p.y)).toBeCloseTo(1, 6);
  });

  // Two consecutive polls of an unchanged graph must draw the same picture —
  // a random or time-seeded scatter would make the canvas jump every 30s.
  it("is deterministic in the order the server sent", () => {
    const nodes = [1, 2, 3].map((id) => node({ id, x: 0, y: 0 }));
    expect(withFallbackPositions(nodes)).toEqual(withFallbackPositions(nodes));
  });

  it("parks unplaced nodes outside the part that IS placed", () => {
    const out = withFallbackPositions([
      node({ id: 1, x: 30, y: 40 }), // radius 50
      node({ id: 2, x: 0, y: 0 }),
    ]);
    expect(out[0]).toEqual({ id: 1, x: 30, y: 40 });
    expect(Math.hypot(out[1]!.x, out[1]!.y)).toBeCloseTo(57.5, 6);
  });

  it("treats a non-finite coordinate as unplaced rather than drawing NaN", () => {
    const out = withFallbackPositions([node({ id: 1, x: Number.NaN, y: 0 })]);
    expect(Number.isFinite(out[0]!.x)).toBe(true);
    expect(Number.isFinite(out[0]!.y)).toBe(true);
  });
});

describe("nodeSize", () => {
  it("grows with degree, clamped at both ends", () => {
    expect(nodeSize(0)).toBe(4);
    expect(nodeSize(9)).toBe(10);
    expect(nodeSize(10_000)).toBe(16);
    // a degree that never arrived is a leaf, not a NaN radius
    expect(nodeSize(Number.NaN)).toBe(4);
  });
});

describe("the palette", () => {
  // jsdom resolves no custom properties, so this is also the path every test
  // in this file takes — and the path a page takes before its stylesheet has
  // applied. It must never produce `undefined` as a colour.
  it("falls back per-var when nothing resolves", () => {
    expect(readGraphPalette(null)).toEqual(PALETTE_FALLBACK);
    expect(nodeColor("convention", PALETTE_FALLBACK)).toBe("#9E7BFF");
    expect(nodeColor("file", PALETTE_FALLBACK)).toBe("#2A3A50");
  });

  it("gives an unrecognized type the fallback hue rather than nothing", () => {
    expect(nodeColor("something-new", PALETTE_FALLBACK)).toBe("#6B7A8C");
  });
});

describe("makeNodeReducer", () => {
  // Sigma REPLACES display data with the reducer's return value, so a reducer
  // that forgets to spread `data` strips x/y and sigma throws.
  it("always carries the position through", () => {
    const r = makeNodeReducer(new Set(), null, PALETTE_FALLBACK);
    expect(r("1", attrs())).toMatchObject({ x: 1, y: 2 });
    const hiddenR = makeNodeReducer(new Set(["1"]), null, PALETTE_FALLBACK);
    expect(hiddenR("1", attrs())).toMatchObject({ x: 1, y: 2, hidden: true });
  });

  it("colours by type and only hides what the rail switched off", () => {
    const r = makeNodeReducer(new Set([nodeKey(2)]), null, PALETTE_FALLBACK);
    expect(r("1", attrs())).toMatchObject({ color: "#9E7BFF" });
    expect(r("1", attrs()).hidden).toBeUndefined();
    expect(r("2", attrs())).toMatchObject({ hidden: true });
  });

  it("marks the selected node and forces its label", () => {
    const r = makeNodeReducer(new Set(), "1", PALETTE_FALLBACK);
    const out = r("1", attrs({ size: 5 }));
    expect(out).toMatchObject({ highlighted: true, forceLabel: true, color: "#87DF49" });
    expect(out.size).toBe(7);
  });
});

describe("makeEdgeReducer", () => {
  const build = (e: GraphEdge, f: GraphFilters, hidden: string[] = []) =>
    makeEdgeReducer(f, new Set(hidden), new Map([[nodeKey(e.id), e]]), PALETTE_FALLBACK);

  it("hides an edge whose endpoint the type rail switched off", () => {
    const e = edge();
    expect(build(e, filters(), ["2"])("1", eattrs())).toMatchObject({ hidden: true });
  });

  it("hides an edge under the occurrence floor", () => {
    const e = edge({ occurrences: 1 });
    expect(
      build(e, filters({ minOccurrences: 3 }))("1", eattrs({ occurrences: 1 })),
    ).toMatchObject({ hidden: true });
  });

  it("draws an invalidated edge faint once it is asked for", () => {
    const e = edge({ expired_at: 1769040000000 });
    const out = build(e, filters({ includeInvalidated: true }))(
      "1",
      eattrs({ invalidated: true }),
    );
    expect(out.hidden).toBeUndefined();
    expect(out.size).toBe(0.4);
  });

  // An edge sigma still holds but the payload no longer describes cannot be
  // decided about — hiding it is the only answer that cannot draw a lie.
  it("hides an edge it has no record of", () => {
    const r = makeEdgeReducer(filters(), new Set(), new Map(), PALETTE_FALLBACK);
    expect(r("ghost", eattrs())).toMatchObject({ hidden: true });
  });
});
