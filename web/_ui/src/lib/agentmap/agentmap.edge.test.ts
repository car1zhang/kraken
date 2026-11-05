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
