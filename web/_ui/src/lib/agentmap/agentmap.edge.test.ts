import { describe, expect, it } from "vitest";

import type { AgentEvent, WebActivity, WebAgent, WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import {
  ACTIVITY_IDS,
  HOLD_MS,
  activityName,
  blockedUrgency,
  crewPose,
  freshEvents,
  leadPose,
  mapStatus,
  ribbon,
} from "./activity.ts";
import { MAP_GAP, overlaps, seedLayout, settle, stackOrder, type MapNode } from "./layout.ts";
import { miniFrame, miniToWorld, type MiniFrame } from "./minimap.ts";
import { COAT_HUE, modelCoat, roleHat } from "./avatar.ts";
import { marqueeKeys } from "./layout.ts";

// athena-edge's blind suite for the map's pure logic (50-contract.md W2, W3):
// missing activity, `open` events and the hold boundary in leadPose, the
// blocked-branch precedence in mapStatus, load edge values, equal `y` in
// stackOrder, a held node in settle, non-finite coordinates in seedLayout.

const T0 = 1_793_000_000_000;

function agentMapEdgeEv(over: Partial<AgentEvent> & { id: string }): AgentEvent {
  return { t: T0, act: "read", label: "Read x.go", ...over };
}

function agentMapEdgeActivity(events: AgentEvent[], over: Partial<WebActivity> = {}): WebActivity {
  return { act: "think", at: T0, events, ...over };
}

function agentMapEdgeWorking(over: Partial<WebJob> = {}): WebJob {
  return makeJob({ state: "working", live: true, ...over });
}

function agentMapEdgeAgent(over: Partial<WebAgent>): WebAgent {
  return { name: "heph-1", model: "opus", role: "hephaestus", status: "run", working: true, kind: "teammate", ...over } as WebAgent;
}

describe("constants", () => {
  it("ACTIVITY_IDS is 17 distinct ids and HOLD_MS is 6000", () => {
    expect(ACTIVITY_IDS).toHaveLength(17);
    expect(new Set(ACTIVITY_IDS).size).toBe(17);
    expect(ACTIVITY_IDS).not.toContain("hear");
    expect(HOLD_MS).toBe(6000);
  });
});

describe("mapStatus — precedence at the edges", () => {
  it("draft and done win over health and question", () => {
    const q = { tool_use_id: "t", questions: [] };
    expect(mapStatus(makeJob({ state: "draft", health: "usage limit", question: q } as Partial<WebJob>))).toBe("draft");
    expect(mapStatus(makeJob({ state: "done", health: "usage limit", zeus_status: "question" }))).toBe("done");
  });

  it("health alone puts a working job on the blocked branch", () => {
    expect(mapStatus(agentMapEdgeWorking({ health: "zeus pane gone" }))).toBe("blocked");
    expect(mapStatus(agentMapEdgeWorking({ live: false, health: "cell gone" }))).toBe("parked");
  });

  it("limit matches case-insensitively and beats a question", () => {
    expect(mapStatus(agentMapEdgeWorking({ health: "Usage LIMIT reached" }))).toBe("limit");
    expect(mapStatus(makeJob({ state: "blocked", live: false, health: "rate-limited", zeus_status: "question" }))).toBe("limit");
  });

  it("a question beats parked on the blocked branch", () => {
    expect(mapStatus(makeJob({ state: "blocked", live: false, zeus_status: "question" }))).toBe("question");
  });

  it("a working job with zeus_status question reads question", () => {
    expect(mapStatus(agentMapEdgeWorking({ zeus_status: "question" }))).toBe("question");
  });
});

describe("leadPose — activity edges", () => {
  it("think with no activity, an empty events list, or only hears", () => {
    expect(leadPose(agentMapEdgeWorking(), T0)).toBe("think");
    expect(leadPose(agentMapEdgeWorking({ activity: agentMapEdgeActivity([]) }), T0)).toBe("think");
    const hears = [
      agentMapEdgeEv({ id: "h1", act: "hear", label: "prompt from you", from: "you" }),
      agentMapEdgeEv({ id: "h2", act: "hear", label: "from heph-1", from: "teammate", who: "heph-1" }),
    ];
    expect(leadPose(agentMapEdgeWorking({ activity: agentMapEdgeActivity(hears) }), T0)).toBe("think");
  });

  it("a status other than working is the pose, whatever the activity", () => {
    const busy = agentMapEdgeActivity([agentMapEdgeEv({ id: "e", act: "edit", open: true })]);
    expect(leadPose(makeJob({ state: "blocked", live: true, activity: busy }), T0)).toBe("blocked");
    expect(leadPose(makeJob({ state: "blocked", live: false, activity: busy }), T0)).toBe("parked");
    expect(leadPose(makeJob({ state: "done", activity: busy }), T0)).toBe("done");
    expect(leadPose(makeJob({ state: "draft", activity: busy }), T0)).toBe("draft");
  });

  it("the newest non-hear event is the one read, even under a newer hear", () => {
    const evs = [
      agentMapEdgeEv({ id: "a", act: "edit", t: T0, dur_ms: 100 }),
      agentMapEdgeEv({ id: "b", act: "hear", t: T0 + 50, label: "prompt from you", from: "you" }),
    ];
    expect(leadPose(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) }), T0 + 200)).toBe("edit");
  });

  it("an open event holds its act long past the hold window", () => {
    const evs = [agentMapEdgeEv({ id: "o", act: "test", t: T0, open: true })];
    expect(leadPose(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) }), T0 + 10 * 60_000)).toBe("test");
  });

  it("other is think, even when open", () => {
    const evs = [agentMapEdgeEv({ id: "x", act: "other", t: T0, open: true })];
    expect(leadPose(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) }), T0)).toBe("think");
  });

  it("the hold boundary is inclusive: t + dur + HOLD_MS holds, one ms later is think", () => {
    const evs = [agentMapEdgeEv({ id: "d", act: "search", t: T0, dur_ms: 2500 })];
    const job = agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) });
    expect(leadPose(job, T0 + 2500 + HOLD_MS)).toBe("search");
    expect(leadPose(job, T0 + 2500 + HOLD_MS + 1)).toBe("think");
  });

  it("a missing dur_ms counts as 0", () => {
    const evs = [agentMapEdgeEv({ id: "d", act: "web", t: T0 })];
    const job = agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) });
    expect(leadPose(job, T0 + HOLD_MS)).toBe("web");
    expect(leadPose(job, T0 + HOLD_MS + 1)).toBe("think");
  });

  it("a clock behind the event still holds the act", () => {
    const evs = [agentMapEdgeEv({ id: "d", act: "run", t: T0, dur_ms: 10 })];
    expect(leadPose(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) }), T0 - 5000)).toBe("run");
  });
});

describe("crewPose — edges", () => {
  it("a monitor waits even when not working and with no activity", () => {
    expect(crewPose(agentMapEdgeAgent({ kind: "monitor", working: false }), undefined, T0)).toBe("wait");
    expect(crewPose(agentMapEdgeAgent({ kind: "monitor", working: true }), undefined, T0)).toBe("wait");
  });

  it("an idle teammate is idle even with a crew entry", () => {
    const act = agentMapEdgeActivity([], { crew: [{ name: "heph-1", act: "edit", at: T0 }] });
    expect(crewPose(agentMapEdgeAgent({ working: false }), act, T0)).toBe("idle");
  });

  it("a working teammate with no activity or no entry under its name works", () => {
    expect(crewPose(agentMapEdgeAgent({}), undefined, T0)).toBe("work");
    const act = agentMapEdgeActivity([], { crew: [{ name: "heph-2", act: "edit", at: T0 }] });
    expect(crewPose(agentMapEdgeAgent({}), act, T0)).toBe("work");
    expect(crewPose(agentMapEdgeAgent({}), agentMapEdgeActivity([]), T0)).toBe("work");
  });

  it("a crew entry of other is think", () => {
    const act = agentMapEdgeActivity([], { crew: [{ name: "heph-1", act: "other", label: "Skill", at: T0 }] });
    expect(crewPose(agentMapEdgeAgent({}), act, T0)).toBe("think");
  });

  it("a subagent never takes a crew entry, even one under its own name", () => {
    const act = agentMapEdgeActivity([], { crew: [{ name: "Reading API.md", act: "read", at: T0 }] });
    expect(crewPose(agentMapEdgeAgent({ name: "Reading API.md", kind: "subagent", role: "Explore" }), act, T0)).toBe("work");
  });
});

describe("blockedUrgency — load edges", () => {
  it("0.4 with no load, a zero unblock, or a negative unblock", () => {
    expect(blockedUrgency(makeJob({ state: "blocked" }))).toBe(0.4);
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 0, scope: 0, note: "", at: 0 } }))).toBe(0.4);
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 0, scope: 9, note: "", at: 0 } }))).toBe(0.4);
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: -2, scope: 9, note: "", at: 0 } }))).toBe(0.4);
  });

  it("clamps to [0, 1]", () => {
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 1, scope: 10, note: "", at: 0 } }))).toBe(1);
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 1, scope: 3, note: "", at: 0 } }))).toBe(1);
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 5, scope: 0, note: "", at: 0 } }))).toBe(0);
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 2, scope: -4, note: "", at: 0 } }))).toBe(0);
  });

  it("is the ratio over 3 inside the range", () => {
    expect(blockedUrgency(makeJob({ state: "blocked", load: { unblock: 4, scope: 6, note: "", at: 0 } }))).toBeCloseTo(0.5, 10);
  });
});

describe("ribbon — edges", () => {
  const evs = Array.from({ length: 30 }, (_, i) => agentMapEdgeEv({ id: `e${i}`, t: T0 + i }));

  it("[] with no activity or no events", () => {
    expect(ribbon(agentMapEdgeWorking())).toEqual([]);
    expect(ribbon(agentMapEdgeWorking({ activity: agentMapEdgeActivity([]) }))).toEqual([]);
  });

  it("defaults to the newest 24, oldest first", () => {
    const got = ribbon(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs) }));
    expect(got).toHaveLength(24);
    expect(got.map((e) => e.id)).toEqual(evs.slice(6).map((e) => e.id));
  });

  it("a list shorter than 24 is returned whole by default", () => {
    const got = ribbon(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs.slice(0, 23)) }));
    expect(got.map((e) => e.id)).toEqual(evs.slice(0, 23).map((e) => e.id));
  });

  it("an n larger than the list returns the whole list", () => {
    const got = ribbon(agentMapEdgeWorking({ activity: agentMapEdgeActivity(evs.slice(0, 3)) }), 50);
    expect(got.map((e) => e.id)).toEqual(["e0", "e1", "e2"]);
  });

  it("keeps hears: the ribbon is every event, not only calls", () => {
    const mixed = [
      agentMapEdgeEv({ id: "c", act: "edit" }),
      agentMapEdgeEv({ id: "h", act: "hear", label: "prompt from you", from: "you" }),
    ];
    expect(ribbon(agentMapEdgeWorking({ activity: agentMapEdgeActivity(mixed) })).map((e) => e.id)).toEqual(["c", "h"]);
  });

  it("does not mutate the job's events", () => {
    const own = evs.slice(0, 12);
    const copy = [...own];
    ribbon(agentMapEdgeWorking({ activity: agentMapEdgeActivity(own) }), 3);
    expect(own).toEqual(copy);
  });
});

describe("freshEvents — edges", () => {
  const evs = [agentMapEdgeEv({ id: "a" }), agentMapEdgeEv({ id: "b" }), agentMapEdgeEv({ id: "c" })];

  it("an empty seen set returns every event in order", () => {
    expect(freshEvents(new Set(), evs).map((e) => e.id)).toEqual(["a", "b", "c"]);
  });

  it("all seen returns []", () => {
    expect(freshEvents(new Set(["a", "b", "c", "zzz"]), evs)).toEqual([]);
  });

  it("no events returns []", () => {
    expect(freshEvents(new Set(["a"]), [])).toEqual([]);
  });

  it("keeps the given order with gaps", () => {
    expect(freshEvents(new Set(["b"]), evs).map((e) => e.id)).toEqual(["a", "c"]);
  });
});

describe("activityName — edges", () => {
  it("hear and the unmapped ids are their own names", () => {
    expect(activityName("hear")).toBe("hear");
    for (const id of ACTIVITY_IDS) {
      if (["home", "web", "check", "data"].includes(id)) continue;
      expect(activityName(id)).toBe(id);
    }
  });
});

// --- layout -------------------------------------------------------------------

function agentMapEdgeNode(key: string, x: number, y: number, w = 200, h = 80): MapNode {
  return { key, x, y, w, h };
}

function agentMapEdgeAnyOverlap(nodes: MapNode[]): boolean {
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      if (overlaps(nodes[i]!, nodes[j]!)) return true;
    }
  }
  return false;
}

describe("overlaps — the gap boundary", () => {
  const a = agentMapEdgeNode("a", 0, 0, 100, 50);

  it("sideways: clear past the full x gap, overlapping inside it", () => {
    expect(overlaps(a, agentMapEdgeNode("b", 100 + MAP_GAP.x + 1, 0, 100, 50))).toBe(false);
    expect(overlaps(a, agentMapEdgeNode("b", 100 + MAP_GAP.x - 1, 0, 100, 50))).toBe(true);
    expect(overlaps(agentMapEdgeNode("b", -(100 + MAP_GAP.x + 1), 0, 100, 50), a)).toBe(false);
  });

  it("vertically: clear past the full y gap, overlapping inside it", () => {
    expect(overlaps(a, agentMapEdgeNode("b", 0, 50 + MAP_GAP.y + 1, 100, 50))).toBe(false);
    expect(overlaps(a, agentMapEdgeNode("b", 0, 50 + MAP_GAP.y - 1, 100, 50))).toBe(true);
    expect(overlaps(agentMapEdgeNode("b", 0, -(50 + MAP_GAP.y - 1), 100, 50), a)).toBe(true);
  });

  it("near on one axis but clear on the other does not overlap", () => {
    expect(overlaps(a, agentMapEdgeNode("b", 100 + MAP_GAP.x - 1, 50 + MAP_GAP.y + 1, 100, 50))).toBe(false);
  });

  it("is symmetric", () => {
    const b = agentMapEdgeNode("b", 120, 40, 100, 50);
    expect(overlaps(a, b)).toBe(overlaps(b, a));
  });
});

describe("seedLayout — non-finite coordinates", () => {
  it("gives finite coordinates to NaN, ±Infinity and missing values", () => {
    const nodes = [
      agentMapEdgeNode("nan", NaN, NaN),
      agentMapEdgeNode("inf", Infinity, -Infinity),
      agentMapEdgeNode("half", 10, NaN),
      { key: "undef", w: 200, h: 80 } as unknown as MapNode,
    ];
    seedLayout(nodes, []);
    for (const n of nodes) {
      expect(Number.isFinite(n.x), `${n.key}.x`).toBe(true);
      expect(Number.isFinite(n.y), `${n.key}.y`).toBe(true);
    }
  });

  it("never moves a node that already had finite coordinates", () => {
    const placed = [agentMapEdgeNode("p1", -50.5, 1200), agentMapEdgeNode("p2", 0, 0)];
    const nodes = [...placed, agentMapEdgeNode("new", NaN, NaN)];
    seedLayout(nodes, [["p1", "new"]]);
    expect(nodes[0]).toMatchObject({ x: -50.5, y: 1200 });
    expect(nodes[1]).toMatchObject({ x: 0, y: 0 });
  });

  it("tolerates no nodes, and links naming keys that are not there", () => {
    expect(() => seedLayout([], [["a", "b"]])).not.toThrow();
    const nodes = [agentMapEdgeNode("a", NaN, NaN)];
    expect(() => seedLayout(nodes, [["a", "ghost"], ["ghost", "a"], ["a", "a"]])).not.toThrow();
    expect(Number.isFinite(nodes[0]!.x) && Number.isFinite(nodes[0]!.y)).toBe(true);
  });
});

describe("settle — edges", () => {
  it("false and no movement with nothing, one node, or no overlap", () => {
    expect(settle([])).toBe(false);
    const one = [agentMapEdgeNode("a", 5, 5)];
    expect(settle(one)).toBe(false);
    expect(one[0]).toMatchObject({ x: 5, y: 5 });
    const apart = [agentMapEdgeNode("a", 0, 0), agentMapEdgeNode("b", 1000, 0), agentMapEdgeNode("c", 0, 1000)];
    const before = apart.map((n) => [n.x, n.y]);
    expect(settle(apart, "a")).toBe(false);
    expect(apart.map((n) => [n.x, n.y])).toEqual(before);
  });

  it("an overlapping pair moves on the first step", () => {
    const nodes = [agentMapEdgeNode("a", 0, 0), agentMapEdgeNode("b", 10, 10)];
    expect(settle(nodes)).toBe(true);
  });

  it("the held node never moves, even when everything is stacked on it", () => {
    const nodes = Array.from({ length: 6 }, (_, i) => agentMapEdgeNode(`n${i}`, 300, 300));
    for (let i = 0; i < 400 && settle(nodes, "n2"); i++) {
      expect(nodes[2]).toMatchObject({ x: 300, y: 300 });
    }
    expect(nodes[2]).toMatchObject({ x: 300, y: 300 });
    expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
  });

  it("a held key that names no node is just no hold", () => {
    const nodes = [agentMapEdgeNode("a", 0, 0), agentMapEdgeNode("b", 0, 0)];
    for (let i = 0; i < 400 && settle(nodes, "ghost"); i++);
    expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
  });

  for (const held of [undefined, "n0", "n11"]) {
    it(`12 nodes on one exact point separate within 400 calls (held: ${held ?? "none"})`, () => {
      const nodes = Array.from({ length: 12 }, (_, i) => agentMapEdgeNode(`n${i}`, 0, 0, 220, 90));
      let calls = 0;
      while (agentMapEdgeAnyOverlap(nodes) && calls < 400) {
        settle(nodes, held);
        calls++;
      }
      expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
      for (const n of nodes) {
        expect(Number.isFinite(n.x) && Number.isFinite(n.y)).toBe(true);
      }
      if (held) expect(nodes.find((n) => n.key === held)).toMatchObject({ x: 0, y: 0 });
      expect(settle(nodes, held)).toBe(false);
    });
  }

  it("12 nodes of mixed sizes in a tight row separate within 400 calls", () => {
    const nodes = Array.from({ length: 12 }, (_, i) => agentMapEdgeNode(`m${i}`, i * 3, (i % 3) * 2, 120 + i * 20, 60 + (i % 4) * 30));
    let calls = 0;
    while (agentMapEdgeAnyOverlap(nodes) && calls < 400) {
      settle(nodes, "m5");
      calls++;
    }
    expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
  });
});

describe("settle with a held set — amendment 7", () => {
  it("an empty set holds nothing: a pile still comes apart", () => {
    const nodes = Array.from({ length: 6 }, (_, i) => agentMapEdgeNode(`e${i}`, 0, 0));
    for (let i = 0; i < 400 && settle(nodes, new Set<string>()); i++);
    expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
  });

  it("two held nodes overlapping each other stay put and do not count as movement", () => {
    const nodes = [agentMapEdgeNode("a", 0, 0), agentMapEdgeNode("b", 10, 10)];
    expect(settle(nodes, new Set(["a", "b"]))).toBe(false);
    expect(nodes[0]).toMatchObject({ x: 0, y: 0 });
    expect(nodes[1]).toMatchObject({ x: 10, y: 10 });
  });

  it("every held node stays put while the unheld ones make way", () => {
    const nodes = [
      agentMapEdgeNode("h1", 0, 0),
      agentMapEdgeNode("h2", 2000, 0),
      agentMapEdgeNode("free1", 5, 5),
      agentMapEdgeNode("free2", 1995, 5),
      agentMapEdgeNode("free3", 1000, 0),
    ];
    const held = new Set(["h1", "h2"]);
    expect(settle(nodes, held)).toBe(true);
    for (let i = 0; i < 400 && settle(nodes, held); i++);
    expect(nodes[0]).toMatchObject({ x: 0, y: 0 });
    expect(nodes[1]).toMatchObject({ x: 2000, y: 0 });
    for (const f of nodes.slice(2)) {
      expect(overlaps(f, nodes[0]!)).toBe(false);
      expect(overlaps(f, nodes[1]!)).toBe(false);
    }
  });

  it("a held pair overlapping each other leaves the rest free to settle", () => {
    const nodes = [agentMapEdgeNode("a", 0, 0), agentMapEdgeNode("b", 10, 10), agentMapEdgeNode("c", 20, 20)];
    const held = new Set(["a", "b"]);
    for (let i = 0; i < 400 && settle(nodes, held); i++);
    expect(nodes[0]).toMatchObject({ x: 0, y: 0 });
    expect(nodes[1]).toMatchObject({ x: 10, y: 10 });
    expect(overlaps(nodes[2]!, nodes[0]!)).toBe(false);
    expect(overlaps(nodes[2]!, nodes[1]!)).toBe(false);
    // only the held pair still overlaps, and that is not movement
    expect(settle(nodes, held)).toBe(false);
  });

  it("held keys naming no node are ignored", () => {
    const nodes = [agentMapEdgeNode("a", 0, 0), agentMapEdgeNode("b", 0, 0)];
    for (let i = 0; i < 400 && settle(nodes, new Set(["ghost", "phantom"])); i++);
    expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
  });
});

describe("stackOrder — edges", () => {
  it("an empty list is an empty map", () => {
    expect(stackOrder([]).size).toBe(0);
  });

  it("equal y gets distinct values ordered by key, whatever the input order", () => {
    const nodes = [agentMapEdgeNode("b", 0, 100), agentMapEdgeNode("c", 500, 100), agentMapEdgeNode("a", 900, 100)];
    const z = stackOrder(nodes);
    expect(z.get("a")!).toBeLessThan(z.get("b")!);
    expect(z.get("b")!).toBeLessThan(z.get("c")!);
  });

  it("every value is a positive integer, negative and fractional y included", () => {
    const nodes = [
      agentMapEdgeNode("neg", 0, -5000),
      agentMapEdgeNode("frac1", 0, 0.1),
      agentMapEdgeNode("frac2", 0, 0.2),
      agentMapEdgeNode("zero", 0, 0),
    ];
    const z = stackOrder(nodes);
    expect(z.size).toBe(4);
    for (const n of nodes) {
      const v = z.get(n.key)!;
      expect(Number.isInteger(v)).toBe(true);
      expect(v).toBeGreaterThan(0);
    }
    expect(z.get("neg")!).toBeLessThan(z.get("zero")!);
    expect(z.get("zero")!).toBeLessThan(z.get("frac1")!);
    expect(z.get("frac1")!).toBeLessThan(z.get("frac2")!);
  });

  it("does not reorder or move the nodes it reads", () => {
    const nodes = [agentMapEdgeNode("z", 1, 300), agentMapEdgeNode("a", 2, 100)];
    const copy = nodes.map((n) => ({ ...n }));
    stackOrder(nodes);
    expect(nodes).toEqual(copy);
  });
});

// --- Round 8: W7 minimap math --------------------------------------------------------

type AgentMapEdgeRect = { x: number; y: number; w: number; h: number };

const agentMapEdgeMini = (key: string, x: number, y: number, w: number, h: number): MapNode => ({ key, x, y, w, h });

const agentMapEdgeToMini = (x: number, y: number, f: MiniFrame): [number, number] => [(x - f.ox) * f.scale, (y - f.oy) * f.scale];

// The world box the contract says miniFrame fits: every node with finite
// coordinates, and the view rectangle.
function agentMapEdgeBox(nodes: readonly MapNode[], view: AgentMapEdgeRect): { x0: number; y0: number; x1: number; y1: number } {
  let x0 = view.x;
  let y0 = view.y;
  let x1 = view.x + view.w;
  let y1 = view.y + view.h;
  for (const n of nodes) {
    if (!Number.isFinite(n.x) || !Number.isFinite(n.y)) continue;
    x0 = Math.min(x0, n.x);
    y0 = Math.min(y0, n.y);
    x1 = Math.max(x1, n.x + n.w);
    y1 = Math.max(y1, n.y + n.h);
  }
  return { x0, y0, x1, y1 };
}

// Asserts the three stated properties: the box grown by pad fits in size, it
// is centred, and the scale is the largest (one axis touches the padded edge).
function agentMapEdgeExpectFit(
  f: MiniFrame,
  nodes: readonly MapNode[],
  view: AgentMapEdgeRect,
  size: { w: number; h: number },
  pad: number,
) {
  const b = agentMapEdgeBox(nodes, view);
  const [px0, py0] = agentMapEdgeToMini(b.x0, b.y0, f);
  const [px1, py1] = agentMapEdgeToMini(b.x1, b.y1, f);
  const eps = 1e-6;
  expect(px0).toBeGreaterThanOrEqual(pad - eps);
  expect(py0).toBeGreaterThanOrEqual(pad - eps);
  expect(px1).toBeLessThanOrEqual(size.w - pad + eps);
  expect(py1).toBeLessThanOrEqual(size.h - pad + eps);
  expect((px0 + px1) / 2).toBeCloseTo(size.w / 2, 6);
  expect((py0 + py1) / 2).toBeCloseTo(size.h / 2, 6);
  const fillsX = Math.abs(px1 - px0 - (size.w - 2 * pad)) < 1e-6;
  const fillsY = Math.abs(py1 - py0 - (size.h - 2 * pad)) < 1e-6;
  expect(fillsX || fillsY, "the scale is the largest that fits").toBe(true);
}

function agentMapEdgeExpectSaneScale(f: MiniFrame) {
  expect(Number.isFinite(f.scale), `scale ${f.scale} finite`).toBe(true);
  expect(f.scale).toBeGreaterThan(0);
}

describe("miniFrame — edges (W7)", () => {
  const size = { w: 200, h: 140 };

  it("with no nodes the box is the view rectangle alone", () => {
    const view = { x: -300, y: 50, w: 1200, h: 600 };
    const f = miniFrame([], view, size);
    agentMapEdgeExpectFit(f, [], view, size, 6);
  });

  it("the default pad is 6", () => {
    const nodes = [agentMapEdgeMini("a", 10, 20, 300, 160), agentMapEdgeMini("b", 2000, -400, 300, 160)];
    const view = { x: 0, y: 0, w: 800, h: 500 };
    expect(miniFrame(nodes, view, size)).toEqual(miniFrame(nodes, view, size, 6));
    agentMapEdgeExpectFit(miniFrame(nodes, view, size), nodes, view, size, 6);
  });

  it("pad 0 lets the box reach the minimap's edge", () => {
    const nodes = [agentMapEdgeMini("a", 0, 0, 100, 100)];
    const view = { x: 0, y: 0, w: 1000, h: 100 };
    const f = miniFrame(nodes, view, size, 0);
    agentMapEdgeExpectFit(f, nodes, view, size, 0);
    // a wide box binds on x: it spans the full width
    const [px0] = agentMapEdgeToMini(0, 0, f);
    const [px1] = agentMapEdgeToMini(1000, 0, f);
    expect(px0).toBeCloseTo(0, 6);
    expect(px1).toBeCloseTo(size.w, 6);
  });

  it("a tall box binds on y and is centred on x", () => {
    const nodes = [agentMapEdgeMini("tall", 0, -5000, 50, 10_000)];
    const view = { x: 0, y: 0, w: 50, h: 50 };
    const f = miniFrame(nodes, view, size, 6);
    agentMapEdgeExpectFit(f, nodes, view, size, 6);
    const [, py0] = agentMapEdgeToMini(0, -5000, f);
    const [, py1] = agentMapEdgeToMini(0, 5000, f);
    expect(py0).toBeCloseTo(6, 6);
    expect(py1).toBeCloseTo(size.h - 6, 6);
  });

  it("covers nodes wholly outside the view, negative coordinates included, by their full size", () => {
    const nodes = [agentMapEdgeMini("left", -9000, -7000, 300, 200), agentMapEdgeMini("right", 4000, 3000, 300, 200)];
    const view = { x: 0, y: 0, w: 800, h: 500 };
    const f = miniFrame(nodes, view, size);
    agentMapEdgeExpectFit(f, nodes, view, size, 6);
    // the far corner of "right" (x + w, y + h) is inside the padded minimap
    const [px, py] = agentMapEdgeToMini(4300, 3200, f);
    expect(px).toBeLessThanOrEqual(size.w - 6 + 1e-6);
    expect(py).toBeLessThanOrEqual(size.h - 6 + 1e-6);
  });

  it("a node with a non-finite coordinate is left out", () => {
    const good = [agentMapEdgeMini("a", 100, 100, 300, 160)];
    const view = { x: 0, y: 0, w: 800, h: 500 };
    const bad = [
      ...good,
      agentMapEdgeMini("nanx", Number.NaN, 0, 300, 160),
      agentMapEdgeMini("nany", 0, Number.NaN, 300, 160),
      agentMapEdgeMini("infx", Number.POSITIVE_INFINITY, 0, 300, 160),
      agentMapEdgeMini("ninfy", 0, Number.NEGATIVE_INFINITY, 300, 160),
    ];
    const withBad = miniFrame(bad, view, size);
    const without = miniFrame(good, view, size);
    expect(withBad.scale).toBeCloseTo(without.scale, 9);
    expect(withBad.ox).toBeCloseTo(without.ox, 6);
    expect(withBad.oy).toBeCloseTo(without.oy, 6);
  });

  it("only non-finite nodes reads as no nodes at all", () => {
    const view = { x: 10, y: 20, w: 400, h: 300 };
    const f = miniFrame([agentMapEdgeMini("n", Number.NaN, Number.NaN, 300, 160)], view, size);
    const g = miniFrame([], view, size);
    expect(f.scale).toBeCloseTo(g.scale, 9);
    expect(f.ox).toBeCloseTo(g.ox, 6);
    expect(f.oy).toBeCloseTo(g.oy, 6);
  });

  const degenerate: Array<[string, MapNode[], AgentMapEdgeRect, { w: number; h: number }, number | undefined]> = [
    ["a zero-size view and no nodes", [], { x: 0, y: 0, w: 0, h: 0 }, size, undefined],
    ["a zero-size view and one zero-size node on it", [agentMapEdgeMini("p", 0, 0, 0, 0)], { x: 0, y: 0, w: 0, h: 0 }, size, undefined],
    ["a zero-size minimap", [agentMapEdgeMini("a", 0, 0, 300, 160)], { x: 0, y: 0, w: 800, h: 500 }, { w: 0, h: 0 }, undefined],
    ["a minimap smaller than its padding", [agentMapEdgeMini("a", 0, 0, 300, 160)], { x: 0, y: 0, w: 800, h: 500 }, { w: 8, h: 8 }, 6],
    ["a negative minimap size", [], { x: 0, y: 0, w: 800, h: 500 }, { w: -50, h: -50 }, undefined],
    ["a NaN view", [], { x: Number.NaN, y: Number.NaN, w: Number.NaN, h: Number.NaN }, size, undefined],
    ["an infinite view", [], { x: 0, y: 0, w: Number.POSITIVE_INFINITY, h: Number.POSITIVE_INFINITY }, size, undefined],
    ["a NaN minimap size", [], { x: 0, y: 0, w: 800, h: 500 }, { w: Number.NaN, h: Number.NaN }, undefined],
    ["a NaN pad", [], { x: 0, y: 0, w: 800, h: 500 }, size, Number.NaN],
    ["a node with NaN width", [agentMapEdgeMini("a", 0, 0, Number.NaN, Number.NaN)], { x: 0, y: 0, w: 800, h: 500 }, size, undefined],
  ];
  for (const [name, nodes, view, sz, pad] of degenerate) {
    it(`scale stays finite and positive for ${name}`, () => {
      agentMapEdgeExpectSaneScale(pad === undefined ? miniFrame(nodes, view, sz) : miniFrame(nodes, view, sz, pad));
    });
  }

  it("does not move or reorder the nodes it reads", () => {
    const nodes = [agentMapEdgeMini("z", 5, 6, 7, 8), agentMapEdgeMini("a", Number.NaN, 1, 2, 3)];
    const copy = nodes.map((n) => ({ ...n }));
    miniFrame(nodes, { x: 0, y: 0, w: 100, h: 100 }, size);
    expect(nodes).toEqual(copy);
  });
});

describe("miniToWorld — edges (W7)", () => {
  it("is exactly px / scale + ox, py / scale + oy, negative and fractional input included", () => {
    const f: MiniFrame = { scale: 0.025, ox: -1200, oy: 340 };
    for (const [px, py] of [[0, 0], [-10, -3], [17.5, 99.25], [1e6, -1e6]] as const) {
      const [x, y] = miniToWorld(px, py, f);
      expect(x).toBeCloseTo(px / f.scale + f.ox, 9);
      expect(y).toBeCloseTo(py / f.scale + f.oy, 9);
    }
  });

  it("round-trips the frame miniFrame chose", () => {
    const nodes = [agentMapEdgeMini("a", -400, 900, 300, 160), agentMapEdgeMini("b", 3000, -200, 300, 160)];
    const f = miniFrame(nodes, { x: 0, y: 0, w: 1400, h: 900 }, { w: 220, h: 150 });
    for (const [wx, wy] of [[-400, 900], [3300, -40], [0, 0], [123.456, -78.9]] as const) {
      const [px, py] = agentMapEdgeToMini(wx, wy, f);
      const [bx, by] = miniToWorld(px, py, f);
      expect(bx).toBeCloseTo(wx, 6);
      expect(by).toBeCloseTo(wy, 6);
    }
  });

  it("the minimap's top-left pixel is the frame's origin", () => {
    const f: MiniFrame = { scale: 3, ox: 7, oy: -9 };
    expect(miniToWorld(0, 0, f)).toEqual([7, -9]);
  });
});

// --- Round 8: W9 coat and hat ----------------------------------------------------

describe("modelCoat — edges (W9)", () => {
  const cases: Array<[string | undefined, string]> = [
    // exact ids kraken spawns
    ["claude-fable-5-2", "fable"],
    ["claude-opus-5", "opus"],
    ["claude-opus-5-5", "opus"],
    ["gpt-6-astra", "codex"],
    // case-insensitive
    ["CLAUDE-OPUS-5-5", "opus"],
    ["Sonnet", "sonnet"],
    ["HAIKU", "haiku"],
    ["GPT-5", "codex"],
    ["ASTRA", "codex"],
    ["Codex-Mini", "codex"],
    ["FaBlE", "fable"],
    // first match wins, in the stated order
    ["fable-opus", "fable"],
    ["opus-fable", "fable"],
    ["opus-sonnet", "opus"],
    ["sonnet-opus", "opus"],
    ["sonnet-haiku", "sonnet"],
    ["haiku-sonnet", "sonnet"],
    ["haiku-gpt", "haiku"],
    ["codex-haiku", "haiku"],
    // contains, not equals
    ["xopusx", "opus"],
    ["my-codex", "codex"],
    // everything else is the yellow default
    ["", "fable"],
    [undefined, "fable"],
    ["   ", "fable"],
    ["gemini-3", "fable"],
    ["gp", "fable"],
    ["opu", "fable"],
    ["haik", "fable"],
    ["cod", "fable"],
  ];
  for (const [model, coat] of cases) {
    it(`${JSON.stringify(model)} → ${coat}`, () => {
      expect(modelCoat(model)).toBe(coat);
    });
  }
});

describe("COAT_HUE — edges (W9)", () => {
  it("holds exactly the five coats with candidate set A's hues", () => {
    expect(Object.keys(COAT_HUE).sort()).toEqual(["codex", "fable", "haiku", "opus", "sonnet"]);
    expect({ ...COAT_HUE }).toEqual({
      fable: "#ffd21f",
      opus: "#6aaef5",
      sonnet: "#f1ead9",
      haiku: "#4fc7b9",
      codex: "#4b5263",
    });
  });

  it("every modelCoat answer has a hue", () => {
    for (const m of [undefined, "", "fable", "opus", "sonnet", "haiku", "gpt", "codex", "astra", "zzz"]) {
      expect(COAT_HUE[modelCoat(m)]).toMatch(/^#[0-9a-f]{6}$/);
    }
  });
});

describe("roleHat — edges (W9)", () => {
  const who = (over: Partial<WebAgent>): WebAgent => agentMapEdgeAgent(over);

  it("monitor and shell wear nothing, whatever their name", () => {
    expect(roleHat(who({ kind: "monitor", name: "hephaestus-go" }))).toBe("none");
    expect(roleHat(who({ kind: "shell", name: "athena-edge" }))).toBe("none");
    expect(roleHat(who({ kind: "monitor", name: "" }))).toBe("none");
  });

  it("a subagent wears a beanie, whatever its name", () => {
    expect(roleHat(who({ kind: "subagent", name: "hephaestus-go" }))).toBe("beanie");
    expect(roleHat(who({ kind: "subagent", name: "athena-logic" }))).toBe("beanie");
    expect(roleHat(who({ kind: "subagent", name: "hermes-d1" }))).toBe("beanie");
    expect(roleHat(who({ kind: "subagent", name: "" }))).toBe("beanie");
  });

  it("a teammate or a kindless agent is hatted by a case-insensitive name prefix", () => {
    for (const kind of ["teammate", undefined] as const) {
      expect(roleHat(who({ kind, name: "hephaestus" }))).toBe("hardhat");
      expect(roleHat(who({ kind, name: "Hephaestus-web" }))).toBe("hardhat");
      expect(roleHat(who({ kind, name: "ATHENA-EDGE" }))).toBe("helmet");
      expect(roleHat(who({ kind, name: "athena" }))).toBe("helmet");
      expect(roleHat(who({ kind, name: "HerMes-d7" }))).toBe("wings");
    }
  });

  it("a role name that is not at the start, or a near miss, is a cap", () => {
    for (const name of ["my-athena", "x-hephaestus", "the-hermes", "athen", "hephaestos", "herm", "zeus-agent-map", "", "researcher-1"]) {
      expect(roleHat(who({ kind: "teammate", name })), name).toBe("cap");
    }
  });

  it("the role field does not decide the hat, the name does", () => {
    expect(roleHat(who({ kind: "teammate", name: "researcher", role: "hephaestus" }))).toBe("cap");
    expect(roleHat(who({ kind: "teammate", name: "athena-logic", role: "researcher" }))).toBe("helmet");
  });

  it("an unrecognised kind falls to the name rule", () => {
    expect(roleHat(who({ kind: "something-new", name: "hermes-x" }))).toBe("wings");
    expect(roleHat(who({ kind: "something-new", name: "zed" }))).toBe("cap");
  });
});

// ---- Round 9 (50-contract.md W12, W13): the smaller gap at its boundary, a
// layout saved under the old gap, and marqueeKeys' corners, edges and refusals.

function agentMapEdgeR9Node(key: string, x: number, y: number, w = 300, h = 180): MapNode {
  return { key, x, y, w, h };
}

/** A grid of `cols` x `rows` cards spaced at exactly the OLD gap ({36, 112}). */
function agentMapEdgeR9OldGapGrid(cols: number, rows: number, w = 300, h = 180): MapNode[] {
  const out: MapNode[] = [];
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      out.push(agentMapEdgeR9Node(`g${r}-${c}`, c * (w + 36), r * (h + 112), w, h));
    }
  }
  return out;
}

const agentMapEdgeR9Pos = (nodes: readonly MapNode[]) => nodes.map((n) => [n.key, n.x, n.y, n.w, n.h]);

describe("W12 — overlaps at the new gap's boundary", () => {
  const a = agentMapEdgeR9Node("a", 0, 0, 100, 50);

  it("sideways: one past 16 is clear, one inside it overlaps", () => {
    expect(overlaps(a, agentMapEdgeR9Node("b", 100 + 17, 0, 100, 50))).toBe(false);
    expect(overlaps(a, agentMapEdgeR9Node("b", 100 + 15, 0, 100, 50))).toBe(true);
    expect(overlaps(agentMapEdgeR9Node("b", -(100 + 17), 0, 100, 50), a)).toBe(false);
    expect(overlaps(agentMapEdgeR9Node("b", -(100 + 15), 0, 100, 50), a)).toBe(true);
  });

  it("vertically: one past 64 is clear, one inside it overlaps", () => {
    expect(overlaps(a, agentMapEdgeR9Node("b", 0, 50 + 65, 100, 50))).toBe(false);
    expect(overlaps(a, agentMapEdgeR9Node("b", 0, 50 + 63, 100, 50))).toBe(true);
    expect(overlaps(agentMapEdgeR9Node("b", 0, -(50 + 65), 100, 50), a)).toBe(false);
    expect(overlaps(agentMapEdgeR9Node("b", 0, -(50 + 63), 100, 50), a)).toBe(true);
  });

  it("distances between the new gap and the old one no longer overlap", () => {
    for (const dx of [17, 20, 30, 35, 36, 37]) {
      expect(overlaps(a, agentMapEdgeR9Node("b", 100 + dx, 0, 100, 50)), `dx ${dx}`).toBe(false);
    }
    for (const dy of [65, 80, 100, 111, 112, 113]) {
      expect(overlaps(a, agentMapEdgeR9Node("b", 0, 50 + dy, 100, 50)), `dy ${dy}`).toBe(false);
    }
  });
});

describe("W12 — a layout saved under the old gap is at rest", () => {
  it("settle moves nothing and reports no movement, with no hold", () => {
    const nodes = agentMapEdgeR9OldGapGrid(4, 3);
    const before = agentMapEdgeR9Pos(nodes);
    expect(agentMapEdgeAnyOverlap(nodes)).toBe(false);
    expect(settle(nodes)).toBe(false);
    expect(agentMapEdgeR9Pos(nodes)).toEqual(before);
  });

  it("settle moves nothing with a held key or a held set either", () => {
    const nodes = agentMapEdgeR9OldGapGrid(4, 3);
    const before = agentMapEdgeR9Pos(nodes);
    expect(settle(nodes, "g1-1")).toBe(false);
    expect(settle(nodes, new Set(["g0-0", "g2-3"]))).toBe(false);
    expect(settle(nodes, new Set<string>())).toBe(false);
    expect(agentMapEdgeR9Pos(nodes)).toEqual(before);
  });

  it("cards of mixed sizes spaced at the old gap stay at rest", () => {
    const nodes: MapNode[] = [];
    let y = -400;
    for (let i = 0; i < 6; i++) {
      const h = 90 + i * 37;
      nodes.push(agentMapEdgeR9Node(`m${i}`, (i % 2) * (300 + 36), y, 300, h));
      if (i % 2 === 1) y += Math.max(h, 90 + (i - 1) * 37) + 112;
    }
    const before = agentMapEdgeR9Pos(nodes);
    expect(settle(nodes)).toBe(false);
    expect(agentMapEdgeR9Pos(nodes)).toEqual(before);
  });

  it("seedLayout leaves an old-gap layout exactly where it was", () => {
    const nodes = agentMapEdgeR9OldGapGrid(3, 3);
    const before = agentMapEdgeR9Pos(nodes);
    seedLayout(nodes, [["g0-0", "g1-1"], ["g1-1", "g2-2"]]);
    expect(agentMapEdgeR9Pos(nodes)).toEqual(before);
  });
});

describe("W13 — marqueeKeys: corners", () => {
  const nodes = [
    agentMapEdgeR9Node("a", 0, 0, 100, 50),
    agentMapEdgeR9Node("b", 500, 0, 100, 50),
    agentMapEdgeR9Node("c", 0, 500, 100, 50),
  ];

  it("the four orders of the same two corners give the same answer", () => {
    const want = ["a", "b"];
    expect(marqueeKeys(nodes, { x: -10, y: -10 }, { x: 550, y: 20 })).toEqual(want);
    expect(marqueeKeys(nodes, { x: 550, y: 20 }, { x: -10, y: -10 })).toEqual(want);
    expect(marqueeKeys(nodes, { x: -10, y: 20 }, { x: 550, y: -10 })).toEqual(want);
    expect(marqueeKeys(nodes, { x: 550, y: -10 }, { x: -10, y: 20 })).toEqual(want);
  });

  it("a zero-size rectangle (a click point) inside a box selects that box", () => {
    expect(marqueeKeys(nodes, { x: 50, y: 25 }, { x: 50, y: 25 })).toEqual(["a"]);
  });

  it("a rectangle entirely inside one box selects it", () => {
    expect(marqueeKeys(nodes, { x: 510, y: 10 }, { x: 520, y: 20 })).toEqual(["b"]);
  });

  it("a rectangle over empty world selects nothing", () => {
    expect(marqueeKeys(nodes, { x: 200, y: 200 }, { x: 300, y: 300 })).toEqual([]);
  });

  it("negative world coordinates work like any other", () => {
    const neg = [agentMapEdgeR9Node("n", -900, -700, 100, 50), agentMapEdgeR9Node("p", 900, 700, 100, 50)];
    expect(marqueeKeys(neg, { x: -850, y: -680 }, { x: -1000, y: -1000 })).toEqual(["n"]);
  });
});

describe("W13 — marqueeKeys: edges count, MAP_GAP does not", () => {
  const box = agentMapEdgeR9Node("a", 0, 0, 100, 50);

  it("touching any one of the four edges counts as intersecting", () => {
    expect(marqueeKeys([box], { x: 100, y: 10 }, { x: 200, y: 20 })).toEqual(["a"]); // right edge
    expect(marqueeKeys([box], { x: -100, y: 10 }, { x: 0, y: 20 })).toEqual(["a"]); // left edge
    expect(marqueeKeys([box], { x: 10, y: 50 }, { x: 20, y: 90 })).toEqual(["a"]); // bottom edge
    expect(marqueeKeys([box], { x: 10, y: -90 }, { x: 20, y: 0 })).toEqual(["a"]); // top edge
  });

  it("touching only a corner counts", () => {
    expect(marqueeKeys([box], { x: 100, y: 50 }, { x: 150, y: 90 })).toEqual(["a"]);
    expect(marqueeKeys([box], { x: -50, y: -50 }, { x: 0, y: 0 })).toEqual(["a"]);
  });

  it("a zero-size rectangle on an edge counts", () => {
    expect(marqueeKeys([box], { x: 100, y: 25 }, { x: 100, y: 25 })).toEqual(["a"]);
  });

  it("just past an edge does not intersect", () => {
    expect(marqueeKeys([box], { x: 100.5, y: 10 }, { x: 200, y: 20 })).toEqual([]);
    expect(marqueeKeys([box], { x: -100, y: 10 }, { x: -0.5, y: 20 })).toEqual([]);
    expect(marqueeKeys([box], { x: 10, y: 50.5 }, { x: 20, y: 90 })).toEqual([]);
    expect(marqueeKeys([box], { x: 10, y: -90 }, { x: 20, y: -0.5 })).toEqual([]);
  });

  it("inside the gap the map keeps between cards, but outside the box, is not selected", () => {
    expect(marqueeKeys([box], { x: 100 + MAP_GAP.x / 4, y: 10 }, { x: 300, y: 20 })).toEqual([]);
    expect(marqueeKeys([box], { x: 10, y: 50 + MAP_GAP.y / 4 }, { x: 20, y: 300 })).toEqual([]);
  });
});

describe("W13 — marqueeKeys: order, refusals and purity", () => {
  it("returns keys in the order of nodes, not sorted", () => {
    const nodes = [
      agentMapEdgeR9Node("zeta", 0, 0, 10, 10),
      agentMapEdgeR9Node("alpha", 20, 0, 10, 10),
      agentMapEdgeR9Node("mid", 40, 0, 10, 10),
    ];
    expect(marqueeKeys(nodes, { x: -1, y: -1 }, { x: 100, y: 100 })).toEqual(["zeta", "alpha", "mid"]);
    expect(marqueeKeys([...nodes].reverse(), { x: -1, y: -1 }, { x: 100, y: 100 })).toEqual(["mid", "alpha", "zeta"]);
  });

  it("no nodes is no keys", () => {
    expect(marqueeKeys([], { x: -1e6, y: -1e6 }, { x: 1e6, y: 1e6 })).toEqual([]);
  });

  it("leaves out a node with any non-finite coordinate or size", () => {
    const good = agentMapEdgeR9Node("good", 0, 0, 100, 50);
    const bad = [
      agentMapEdgeR9Node("nan-x", NaN, 0, 100, 50),
      agentMapEdgeR9Node("nan-y", 0, NaN, 100, 50),
      agentMapEdgeR9Node("inf-x", Infinity, 0, 100, 50),
      agentMapEdgeR9Node("ninf-y", 0, -Infinity, 100, 50),
      agentMapEdgeR9Node("inf-w", 0, 0, Infinity, 50),
      agentMapEdgeR9Node("nan-h", 0, 0, 100, NaN),
      { key: "missing", w: 100, h: 50 } as unknown as MapNode,
      { key: "no-size", x: 0, y: 0 } as unknown as MapNode,
    ];
    expect(marqueeKeys([...bad, good], { x: -1e9, y: -1e9 }, { x: 1e9, y: 1e9 })).toEqual(["good"]);
  });

  it("a non-finite corner returns no keys", () => {
    const nodes = [agentMapEdgeR9Node("a", 0, 0, 100, 50)];
    const corners: Array<[{ x: number; y: number }, { x: number; y: number }]> = [
      [{ x: NaN, y: 0 }, { x: 200, y: 200 }],
      [{ x: 0, y: NaN }, { x: 200, y: 200 }],
      [{ x: -10, y: -10 }, { x: Infinity, y: 200 }],
      [{ x: -10, y: -10 }, { x: 200, y: -Infinity }],
      [{ x: -Infinity, y: -Infinity }, { x: Infinity, y: Infinity }],
    ];
    for (const [a, b] of corners) {
      expect(marqueeKeys(nodes, a, b), JSON.stringify([a, b])).toEqual([]);
    }
  });

  it("mutates neither the nodes nor the corners", () => {
    const nodes = [
      agentMapEdgeR9Node("a", 0, 0, 100, 50),
      agentMapEdgeR9Node("b", NaN, 0, 100, 50),
      agentMapEdgeR9Node("c", 400, 400, 100, 50),
    ];
    const a = { x: 300, y: 300 };
    const b = { x: -5, y: -5 };
    const before = JSON.stringify(nodes);
    const order = nodes.map((n) => n.key);
    marqueeKeys(nodes, a, b);
    expect(JSON.stringify(nodes)).toBe(before);
    expect(nodes.map((n) => n.key)).toEqual(order);
    expect(a).toEqual({ x: 300, y: 300 });
    expect(b).toEqual({ x: -5, y: -5 });
  });

  it("works on frozen input", () => {
    const nodes = Object.freeze([
      Object.freeze(agentMapEdgeR9Node("a", 0, 0, 100, 50)),
      Object.freeze(agentMapEdgeR9Node("b", 200, 0, 100, 50)),
    ]);
    const a = Object.freeze({ x: -1, y: -1 });
    const b = Object.freeze({ x: 150, y: 60 });
    expect(() => marqueeKeys(nodes, a, b)).not.toThrow();
    expect(marqueeKeys(nodes, a, b)).toEqual(["a"]);
  });
});
