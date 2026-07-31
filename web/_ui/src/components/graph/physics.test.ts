// Tests for the physics layer, written from 57-physics-drag.md's stated
// obligations. Authorship note for the build report: this round was
// implemented solo (one coherent client-side slice), so these are the
// implementer's own tests rather than blind-authored ones.

import { describe, expect, it } from "vitest";

import {
  createPhysics,
  isDragMovement,
  physicsAllowed,
  readPhysicsPref,
  writePhysicsPref,
  DRAG_THRESHOLD_PX,
  PHYSICS_NODE_CAP,
  PHYSICS_PREF_KEY,
} from "./physics.ts";

function memStorage(): Storage {
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
    removeItem: (k) => void m.delete(k),
    clear: () => m.clear(),
    key: () => null,
    get length() {
      return m.size;
    },
  } as Storage;
}

describe("isDragMovement", () => {
  it("a press under the threshold is a click, at or past it a drag", () => {
    expect(isDragMovement(DRAG_THRESHOLD_PX - 1, 0)).toBe(false);
    expect(isDragMovement(DRAG_THRESHOLD_PX, 0)).toBe(true);
    // Diagonal distance counts, not per-axis: 2,2 is √8 ≈ 2.83 < 3.
    expect(isDragMovement(2, 2)).toBe(false);
    expect(isDragMovement(3, 3)).toBe(true);
  });
});

describe("physicsAllowed", () => {
  it("auto defers to the node cap", () => {
    expect(physicsAllowed(PHYSICS_NODE_CAP, "auto", false)).toBe(true);
    expect(physicsAllowed(PHYSICS_NODE_CAP + 1, "auto", false)).toBe(false);
  });
  it("an explicit pref wins in both directions, including over the cap", () => {
    expect(physicsAllowed(PHYSICS_NODE_CAP + 1, "on", false)).toBe(true);
    expect(physicsAllowed(1, "off", false)).toBe(false);
  });
  it("reduced motion turns AUTO off but never overrides an explicit on", () => {
    expect(physicsAllowed(1, "auto", true)).toBe(false);
    expect(physicsAllowed(1, "on", true)).toBe(true);
  });
});

describe("physics pref storage", () => {
  it("round-trips on/off and stores auto as absence", () => {
    const s = memStorage();
    writePhysicsPref(s, "on");
    expect(readPhysicsPref(s)).toBe("on");
    writePhysicsPref(s, "off");
    expect(readPhysicsPref(s)).toBe("off");
    writePhysicsPref(s, "auto");
    expect(s.getItem(PHYSICS_PREF_KEY)).toBeNull();
    expect(readPhysicsPref(s)).toBe("auto");
  });
  it("garbage in storage reads as auto, never a crash", () => {
    const s = memStorage();
    s.setItem(PHYSICS_PREF_KEY, "sideways");
    expect(readPhysicsPref(s)).toBe("auto");
  });
});

// A four-node line fixture: a—b—c, with d unlinked, all seeded on a grid.
function fixture() {
  return createPhysics({
    nodes: [
      { id: "a", x: 0, y: 0, size: 4 },
      { id: "b", x: 100, y: 0, size: 4 },
      { id: "c", x: 200, y: 0, size: 4 },
      { id: "d", x: 0, y: 300, size: 4 },
    ],
    links: [
      { source: "a", target: "b", occurrences: 1 },
      { source: "b", target: "c", occurrences: 1 },
    ],
  });
}

describe("createPhysics", () => {
  it("seeds from the given positions, not a random scatter", () => {
    const h = fixture();
    const byID = new Map(h.nodes().map((n) => [n.id, n]));
    expect(byID.get("a")?.x).toBe(0);
    expect(byID.get("b")?.x).toBe(100);
    expect(byID.get("d")?.y).toBe(300);
  });

  it("drag pins fx/fy for exactly the drag's duration; release clears them", () => {
    const h = fixture();
    h.drag("b", 150, 50);
    const b = h.nodes().find((n) => n.id === "b")!;
    expect(b.fx).toBe(150);
    expect(b.fy).toBe(50);
    // Pinned through ticks: the node IS at the pointer, never lagging it.
    h.tick();
    h.tick();
    expect(b.x).toBe(150);
    expect(b.y).toBe(50);
    h.release("b");
    expect(b.fx).toBeNull();
    expect(b.fy).toBeNull();
  });

  it("linked neighbours follow a drag; unlinked nodes move less", () => {
    const h = fixture();
    // Drag b far downward and let the simulation chase for a while.
    h.drag("b", 100, 400);
    for (let i = 0; i < 60; i++) h.tick();
    const byID = new Map(h.nodes().map((n) => [n.id, n]));
    const aMoved = Math.abs(byID.get("a")!.y! - 0);
    const dMoved = Math.abs(byID.get("d")!.y! - 300);
    // a is linked to b and must be pulled substantially toward it; d has no
    // link and only feels charge, so it moves far less. The 3x margin keeps
    // the assertion about the MECHANISM (link force vs none), not the tuning.
    expect(aMoved).toBeGreaterThan(30);
    expect(aMoved).toBeGreaterThan(dMoved * 3);
  });

  it("wakes on drag and settles back to sleep after release", () => {
    const h = fixture();
    // A warm-started simulation begins slightly awake (settling the seed
    // delta); run it down first so the wake below is unambiguous.
    for (let i = 0; i < 300 && h.awake(); i++) h.tick();
    expect(h.awake()).toBe(false);
    h.drag("b", 120, 30);
    expect(h.awake()).toBe(true);
    h.release("b");
    // alphaTarget dropped to 0 on release, so alpha decays below the floor
    // within a bounded number of ticks — the ≤ ~1 s resettle obligation
    // (300 ticks is 5 s of frames, far past the spec's budget; the loop
    // exits as soon as it sleeps, and the assertion is that it DOES).
    for (let i = 0; i < 300 && h.awake(); i++) h.tick();
    expect(h.awake()).toBe(false);
  });

  it("only the nodes it was given exist in the simulation — membership is the caller's filter", () => {
    // The spec's filtered-out-nodes-leave-the-simulation obligation, at this
    // module's boundary: construct with a subset and nothing else appears.
    const h = createPhysics({
      nodes: [{ id: "a", x: 1, y: 2, size: 4 }],
      links: [],
    });
    expect(h.nodes().map((n) => n.id)).toEqual(["a"]);
  });

  it("a drag on an unknown id is a no-op, never a crash", () => {
    const h = fixture();
    h.drag("nope", 1, 2);
    h.release("nope");
    h.tick();
  });

  it("stop() kills the handle: tick, drag and awake are all no-ops after", () => {
    const h = fixture();
    h.stop();
    const before = h.nodes().map((n) => ({ x: n.x, y: n.y }));
    h.drag("b", 999, 999);
    h.tick();
    const after = h.nodes();
    expect(h.awake()).toBe(false);
    for (let i = 0; i < before.length; i++) {
      expect(after[i]!.x).toBe(before[i]!.x);
      expect(after[i]!.y).toBe(before[i]!.y);
    }
    expect(after.find((n) => n.id === "b")?.fx ?? null).toBeNull();
  });

  it("release of an unknown id does not end a live drag's wake", () => {
    const h = fixture();
    h.drag("b", 150, 50);
    expect(h.awake()).toBe(true);
    h.release("nope");
    // b's pin and the raised alphaTarget both survive — only releasing the
    // pinned node itself may end the wake.
    expect(h.awake()).toBe(true);
    expect(h.nodes().find((n) => n.id === "b")?.fx).toBe(150);
  });

  it("release settles to sleep inside the spec's ~1 s budget", () => {
    const h = fixture();
    h.drag("b", 100, 400);
    for (let i = 0; i < 30; i++) h.tick();
    h.release("b");
    // 90 ticks = 1.5 s of frames; the tuned alphaDecay makes it in ~60.
    let ticks = 0;
    while (h.awake() && ticks < 90) {
      h.tick();
      ticks++;
    }
    expect(h.awake()).toBe(false);
  });

  it("a throwing storage reads as auto and a denied write does not throw", () => {
    const throwing = {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
      removeItem: () => {
        throw new Error("denied");
      },
    };
    expect(readPhysicsPref(throwing as unknown as Storage)).toBe("auto");
    expect(() => writePhysicsPref(throwing as unknown as Storage, "on")).not.toThrow();
  });
});
