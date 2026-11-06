import { describe, expect, it } from "vitest";

import { overlaps, seedLayout, type MapNode } from "./layout.ts";

// seedorigin.test.ts — seedLayout's `origin`: where a card with no position
// starts when the map names a point for it (the middle of the visible area,
// for a job added after the map was first laid out).

const node = (key: string, x: number, y: number, w = 300, h = 120): MapNode => ({ key, x, y, w, h });
const unplaced = (key: string, w = 300, h = 120): MapNode => ({ key, x: NaN, y: NaN, w, h });
const centre = (n: MapNode): [number, number] => [n.x + n.w / 2, n.y + n.h / 2];

describe("seedLayout with an origin", () => {
  it("centres an unplaced card on the origin when that spot is free", () => {
    // everything placed is far away, so the layout's own centre is nowhere near
    const nodes = [node("a", 0, 0), node("b", 400, 0), unplaced("new")];
    seedLayout(nodes, [], [5000, 3000]);
    expect(centre(nodes[2]!)).toEqual([5000, 3000]);
    // the placed cards did not move
    expect([nodes[0]!.x, nodes[0]!.y, nodes[1]!.x, nodes[1]!.y]).toEqual([0, 0, 400, 0]);
  });

  it("takes the nearest free spot when the origin is under a placed card", () => {
    const placed = node("a", 4850, 2940);
    const nodes = [placed, unplaced("new")];
    seedLayout(nodes, [], [5000, 3000]);
    const n = nodes[1]!;
    expect(overlaps(n, placed)).toBe(false);
    const [cx, cy] = centre(n);
    expect(Math.hypot(cx - 5000, cy - 3000)).toBeLessThan(700);
    expect([placed.x, placed.y]).toEqual([4850, 2940]);
  });

  it("still puts a linked card under its partner, whatever the origin", () => {
    const parent = node("parent", 0, 0);
    const nodes = [parent, unplaced("child")];
    seedLayout(nodes, [["parent", "child"]], [5000, 3000]);
    const child = nodes[1]!;
    expect(child.x + child.w / 2).toBe(150);
    expect(child.y).toBeGreaterThan(parent.y + parent.h);
    expect(child.y).toBeLessThan(1000);
  });

  it("without an origin starts at the centre of everything placed, as before", () => {
    const a = [node("a", 0, 0), node("b", 2000, 0), unplaced("new")];
    const b = [node("a", 0, 0), node("b", 2000, 0), unplaced("new")];
    seedLayout(a, []);
    seedLayout(b, [], undefined);
    expect([a[2]!.x, a[2]!.y]).toEqual([b[2]!.x, b[2]!.y]);
    // between the two placed cards, not out at some other point
    expect(a[2]!.x).toBeGreaterThan(300);
    expect(a[2]!.x).toBeLessThan(1700);
  });

  it("ignores an origin that is not finite", () => {
    for (const bad of [[NaN, 0], [0, Infinity], [-Infinity, NaN]] as const) {
      const want = [node("a", 0, 0), node("b", 2000, 0), unplaced("new")];
      const got = [node("a", 0, 0), node("b", 2000, 0), unplaced("new")];
      seedLayout(want, []);
      seedLayout(got, [], bad);
      expect([got[2]!.x, got[2]!.y]).toEqual([want[2]!.x, want[2]!.y]);
    }
  });

  it("leaves placed cards alone and seeds several new ones clear of each other", () => {
    const nodes = [node("a", 0, 0), unplaced("n1"), unplaced("n2"), unplaced("n3")];
    seedLayout(nodes, [], [3000, 3000]);
    expect([nodes[0]!.x, nodes[0]!.y]).toEqual([0, 0]);
    for (let i = 1; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) expect(overlaps(nodes[i]!, nodes[j]!)).toBe(false);
      const [cx, cy] = centre(nodes[i]!);
      expect(Math.hypot(cx - 3000, cy - 3000)).toBeLessThan(900);
    }
  });

  it("places a card that has no size yet at the origin", () => {
    const nodes = [node("a", 0, 0), { key: "new", x: NaN, y: NaN, w: 300, h: 0 }];
    seedLayout(nodes, [], [5000, 3000]);
    expect([nodes[1]!.x, nodes[1]!.y]).toEqual([4850, 3000]);
  });
});
