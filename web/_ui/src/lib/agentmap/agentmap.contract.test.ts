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
import { MAP_GAP, freeSpot, marqueeKeys, overlaps, seedLayout, settle, stackOrder, type MapNode } from "./layout.ts";
import { COAT_HUE, modelCoat, roleHat } from "./avatar.ts";
import { miniFrame, miniToWorld, type MiniFrame } from "./minimap.ts";

// agentmap.contract.test.ts — athena-logic's blind tests for the map's pure
// logic (50-contract.md W2 and W3). Every assertion cites its contract row.

const T0 = 1_792_000_000_000;

function ev(over: Partial<AgentEvent> & { id: string }): AgentEvent {
  return { t: T0, act: "edit", label: "Edit x.go", ...over };
}

function withEvents(events: AgentEvent[], over: Partial<WebJob> = {}): WebJob {
  const activity: WebActivity = { act: "edit", at: T0, events };
  return makeJob({ state: "working", live: true, activity, ...over });
}

function agent(over: Partial<WebAgent> = {}): WebAgent {
  return { name: "heph-1", model: "opus", role: "hephaestus", status: "run", working: true, kind: "teammate", ...over } as WebAgent;
}

describe("ACTIVITY_IDS and HOLD_MS", () => {
  it("are the 17 ids of core.ActivityIDs in order, and 6000", () => {
    expect([...ACTIVITY_IDS]).toEqual([
      "think", "edit", "search", "read", "run", "home", "delegate", "talk", "review",
      "test", "web", "wait", "check", "ship", "data", "build", "other",
    ]);
    expect(HOLD_MS).toBe(6000);
  });
});

describe("mapStatus", () => {
  it("is draft and done for those states", () => {
    expect(mapStatus(makeJob({ state: "draft" }))).toBe("draft");
    expect(mapStatus(makeJob({ state: "done" }))).toBe("done");
  });

  it("reads a blocked job as limit, question, parked or blocked, in that order", () => {
    expect(mapStatus(makeJob({ state: "blocked", live: true, health: "usage LIMIT reached" }))).toBe("limit");
    expect(mapStatus(makeJob({ state: "blocked", live: true, zeus_status: "question" }))).toBe("question");
    expect(
      mapStatus(makeJob({ state: "blocked", live: true, question: { tool_use_id: "t", questions: [] } } as Partial<WebJob>)),
    ).toBe("question");
    expect(mapStatus(makeJob({ state: "blocked", live: false }))).toBe("parked");
    expect(mapStatus(makeJob({ state: "blocked", live: true }))).toBe("blocked");
    // limit outranks question
    expect(mapStatus(makeJob({ state: "blocked", live: true, health: "limit", zeus_status: "question" }))).toBe("limit");
  });

  it("reads a working job with health set as blocked", () => {
    expect(mapStatus(makeJob({ state: "working", live: true, health: "pane gone" }))).toBe("blocked");
    expect(mapStatus(makeJob({ state: "working", live: true, health: "session limit" }))).toBe("limit");
    expect(mapStatus(makeJob({ state: "working", live: false, health: "cell gone" }))).toBe("parked");
  });

  it("reads an ordinary working job as question or working", () => {
    expect(mapStatus(makeJob({ state: "working", live: true, zeus_status: "question" }))).toBe("question");
    expect(
      mapStatus(makeJob({ state: "working", live: true, question: { tool_use_id: "t", questions: [] } } as Partial<WebJob>)),
    ).toBe("question");
    expect(mapStatus(makeJob({ state: "working", live: true, zeus_status: "running" }))).toBe("working");
  });
});

describe("leadPose", () => {
  it("is the status itself for anything but working", () => {
    expect(leadPose(makeJob({ state: "draft" }), T0)).toBe("draft");
    expect(leadPose(makeJob({ state: "done" }), T0)).toBe("done");
    expect(leadPose(withEvents([ev({ id: "a", open: true })], { state: "blocked", live: true }), T0)).toBe("blocked");
    expect(leadPose(withEvents([ev({ id: "a", open: true })], { state: "blocked", live: false }), T0)).toBe("parked");
  });

  it("is think with no activity, or with only hears", () => {
    expect(leadPose(makeJob({ state: "working", live: true }), T0)).toBe("think");
    expect(leadPose(withEvents([ev({ id: "h", act: "hear", label: "prompt from you", from: "you" })]), T0)).toBe("think");
  });

  it("is think when the newest non-hear event is other", () => {
    const job = withEvents([ev({ id: "a", act: "edit", open: true, t: T0 - 10 }), ev({ id: "b", act: "other", open: true })]);
    expect(leadPose(job, T0)).toBe("think");
  });

  it("is the open event's act however long ago it started", () => {
    const job = withEvents([ev({ id: "a", act: "test", label: "go test", open: true, t: T0 - 10 * 60_000 })]);
    expect(leadPose(job, T0)).toBe("test");
  });

  it("skips a newer hear for the newest call", () => {
    const job = withEvents([
      ev({ id: "a", act: "search", label: "Grep", open: true, t: T0 - 100 }),
      ev({ id: "h", act: "hear", label: "from heph-1", from: "teammate", who: "heph-1", t: T0 }),
    ]);
    expect(leadPose(job, T0)).toBe("search");
  });

  it("holds a closed event's act until t + dur_ms + HOLD_MS, then think", () => {
    const e = ev({ id: "a", act: "build", label: "make build", t: T0, dur_ms: 2000 });
    const job = withEvents([e]);
    expect(leadPose(job, T0 + 1000)).toBe("build");
    expect(leadPose(job, T0 + 2000 + HOLD_MS)).toBe("build");
    expect(leadPose(job, T0 + 2000 + HOLD_MS + 1)).toBe("think");
  });

  it("treats a missing dur_ms as 0", () => {
    const job = withEvents([ev({ id: "a", act: "read", label: "Read a.md", t: T0 })]);
    expect(leadPose(job, T0 + HOLD_MS)).toBe("read");
    expect(leadPose(job, T0 + HOLD_MS + 1)).toBe("think");
  });
});

describe("crewPose", () => {
  const activity: WebActivity = {
    act: "edit",
    at: T0,
    events: [],
    crew: [
      { name: "heph-1", act: "edit", label: "Edit a.go", at: T0 },
      { name: "athena-logic", act: "other", label: "Skill", at: T0 },
      { name: "helper", act: "search", label: "Grep", at: T0 },
    ],
  };

  it("is wait for a monitor", () => {
    expect(crewPose(agent({ kind: "monitor", name: "watch", working: true }), activity, T0)).toBe("wait");
    expect(crewPose(agent({ kind: "monitor", name: "watch", working: false }), activity, T0)).toBe("wait");
  });

  it("is idle for a teammate that is not working", () => {
    expect(crewPose(agent({ working: false }), activity, T0)).toBe("idle");
  });

  it("is a working teammate's crew act, other mapped to think", () => {
    expect(crewPose(agent({ name: "heph-1" }), activity, T0)).toBe("edit");
    expect(crewPose(agent({ name: "athena-logic" }), activity, T0)).toBe("think");
  });

  it("is work for a working teammate with no entry, or a working subagent", () => {
    expect(crewPose(agent({ name: "heph-9" }), activity, T0)).toBe("work");
    expect(crewPose(agent({ name: "heph-1" }), undefined, T0)).toBe("work");
    expect(crewPose(agent({ name: "helper", kind: "subagent" }), activity, T0)).toBe("work");
  });
});

describe("blockedUrgency", () => {
  const load = (unblock: number, scope: number) => ({ unblock, scope, note: "", at: T0 });

  it("is (scope / unblock) / 3 clamped to 0..1", () => {
    expect(blockedUrgency(makeJob({ state: "blocked", load: load(2, 3) } as Partial<WebJob>))).toBeCloseTo(0.5);
    expect(blockedUrgency(makeJob({ state: "blocked", load: load(10, 3) } as Partial<WebJob>))).toBeCloseTo(0.1);
    expect(blockedUrgency(makeJob({ state: "blocked", load: load(1, 9) } as Partial<WebJob>))).toBe(1);
  });

  it("is 0.4 with no load or no positive unblock", () => {
    expect(blockedUrgency(makeJob({ state: "blocked" }))).toBe(0.4);
    expect(blockedUrgency(makeJob({ state: "blocked", load: load(0, 0) } as Partial<WebJob>))).toBe(0.4);
  });
});

describe("ribbon", () => {
  const events = Array.from({ length: 30 }, (_, i) => ev({ id: `e${i}`, t: T0 + i }));

  it("is the newest 24 by default (amended from 10), oldest first", () => {
    expect(ribbon(withEvents(events)).map((e) => e.id)).toEqual(events.slice(6).map((e) => e.id));
  });

  it("takes n", () => {
    expect(ribbon(withEvents(events), 3).map((e) => e.id)).toEqual(["e27", "e28", "e29"]);
  });

  it("is [] with no activity", () => {
    expect(ribbon(makeJob({ state: "working", live: true }))).toEqual([]);
  });
});

describe("freshEvents", () => {
  it("keeps the unseen events in their given order", () => {
    const events = [ev({ id: "c" }), ev({ id: "a" }), ev({ id: "b" }), ev({ id: "d" })];
    expect(freshEvents(new Set(["a", "d"]), events).map((e) => e.id)).toEqual(["c", "b"]);
    expect(freshEvents(new Set(), events).map((e) => e.id)).toEqual(["c", "a", "b", "d"]);
    expect(freshEvents(new Set(["a", "b", "c", "d"]), events)).toEqual([]);
  });
});

describe("activityName", () => {
  it("renames four ids and leaves every other one, hear included, alone", () => {
    expect(activityName("home")).toBe("report");
    expect(activityName("web")).toBe("fetch");
    expect(activityName("check")).toBe("lint");
    expect(activityName("data")).toBe("query");
    for (const id of ACTIVITY_IDS) {
      if (["home", "web", "check", "data"].includes(id)) continue;
      expect(activityName(id)).toBe(id);
    }
    expect(activityName("hear")).toBe("hear");
  });
});

// ---------------------------------------------------------------------------
// layout
// ---------------------------------------------------------------------------

const node = (key: string, x: number, y: number, w = 240, h = 140): MapNode => ({ key, x, y, w, h });

function anyOverlap(nodes: MapNode[]): boolean {
  for (let i = 0; i < nodes.length; i++)
    for (let j = i + 1; j < nodes.length; j++) if (overlaps(nodes[i]!, nodes[j]!)) return true;
  return false;
}

describe("MAP_GAP and overlaps", () => {
  // Round 9, W12 replaces the earlier pin of { x: 36, y: 112 }.
  it("MAP_GAP is { x: 16, y: 64 } (W12)", () => {
    expect(MAP_GAP).toEqual({ x: 16, y: 64 });
  });

  it("is true when the boxes grown by half the gap intersect, false when clear of it", () => {
    const a = node("a", 0, 0, 100, 50);
    // sideways: the boxes themselves are apart, but closer than MAP_GAP.x
    expect(overlaps(a, node("b", 100 + MAP_GAP.x - 4, 0, 100, 50))).toBe(true);
    expect(overlaps(a, node("b", 100 + MAP_GAP.x + 4, 0, 100, 50))).toBe(false);
    // vertically: closer than MAP_GAP.y, then clear of it
    expect(overlaps(a, node("b", 0, 50 + MAP_GAP.y - 8, 100, 50))).toBe(true);
    expect(overlaps(a, node("b", 0, 50 + MAP_GAP.y + 8, 100, 50))).toBe(false);
    // on top of each other, and far apart
    expect(overlaps(a, node("b", 10, 10, 100, 50))).toBe(true);
    expect(overlaps(a, node("b", 5000, 5000, 100, 50))).toBe(false);
  });

  it("is symmetric", () => {
    const a = node("a", 0, 0);
    const b = node("b", 250, 30);
    expect(overlaps(a, b)).toBe(overlaps(b, a));
  });
});

describe("seedLayout", () => {
  it("gives finite coordinates to unplaced nodes and leaves placed ones alone", () => {
    const nodes = [
      node("placed", 120, -40),
      node("nan", Number.NaN, Number.NaN),
      node("half", 50, Number.NaN),
      node("inf", Number.POSITIVE_INFINITY, 10),
    ];
    seedLayout(nodes, [["placed", "nan"]]);
    for (const n of nodes) {
      expect(Number.isFinite(n.x)).toBe(true);
      expect(Number.isFinite(n.y)).toBe(true);
    }
    expect(nodes[0]).toMatchObject({ x: 120, y: -40 });
  });
});

describe("settle", () => {
  it("returns false and moves nothing when no pair overlaps", () => {
    const nodes = [node("a", 0, 0), node("b", 1000, 0), node("c", 0, 1000)];
    const before = nodes.map((n) => [n.x, n.y]);
    expect(settle(nodes)).toBe(false);
    expect(nodes.map((n) => [n.x, n.y])).toEqual(before);
  });

  it("moves overlapping nodes apart and reports it", () => {
    const nodes = [node("a", 0, 0), node("b", 20, 10)];
    expect(settle(nodes)).toBe(true);
    for (let i = 0; i < 400 && anyOverlap(nodes); i++) settle(nodes);
    expect(anyOverlap(nodes)).toBe(false);
  });

  it("never moves the held node", () => {
    const nodes = [node("held", 100, 100), node("b", 110, 120), node("c", 90, 80)];
    for (let i = 0; i < 400; i++) {
      settle(nodes, "held");
      expect(nodes[0]).toMatchObject({ x: 100, y: 100 });
    }
    expect(anyOverlap(nodes)).toBe(false);
  });

  it("clears 12 crowded nodes within 400 calls, with and without one held", () => {
    for (const held of [undefined, "n5"]) {
      const nodes = Array.from({ length: 12 }, (_, i) => node(`n${i}`, i * 7, i * 5));
      let calls = 0;
      while (anyOverlap(nodes) && calls < 400) {
        settle(nodes, held);
        calls++;
      }
      expect(anyOverlap(nodes)).toBe(false);
      for (const n of nodes) {
        expect(Number.isFinite(n.x)).toBe(true);
        expect(Number.isFinite(n.y)).toBe(true);
      }
      if (held) expect(nodes[5]).toMatchObject({ x: 35, y: 25 });
      // once settled, a further step moves nothing
      expect(settle(nodes, held)).toBe(false);
    }
  });
});

describe("settle with a held set (amendment 7)", () => {
  it("never moves any key in the set, and moves the others clear", () => {
    const nodes = [node("h1", 0, 0), node("h2", 600, 0), node("free", 300, 40)];
    const held: ReadonlySet<string> = new Set(["h1", "h2"]);
    for (let i = 0; i < 400; i++) {
      settle(nodes, held);
      expect(nodes[0]).toMatchObject({ x: 0, y: 0 });
      expect(nodes[1]).toMatchObject({ x: 600, y: 0 });
    }
    expect(anyOverlap(nodes)).toBe(false);
  });

  it("leaves two held nodes that overlap each other alone, and that is not movement", () => {
    const nodes = [node("h1", 0, 0), node("h2", 30, 20), node("far", 5000, 5000)];
    const held: ReadonlySet<string> = new Set(["h1", "h2"]);
    expect(settle(nodes, held)).toBe(false);
    expect(nodes[0]).toMatchObject({ x: 0, y: 0 });
    expect(nodes[1]).toMatchObject({ x: 30, y: 20 });
    expect(nodes[2]).toMatchObject({ x: 5000, y: 5000 });
  });
});

describe("stackOrder", () => {
  it("gives a positive integer per key, greater for greater y, distinct and key-ordered on equal y", () => {
    const nodes = [node("low", 0, 900), node("high", 0, -50), node("mid-b", 300, 400), node("mid-a", 0, 400)];
    const z = stackOrder(nodes);
    expect(z.size).toBe(4);
    for (const n of nodes) {
      const v = z.get(n.key)!;
      expect(Number.isInteger(v)).toBe(true);
      expect(v).toBeGreaterThan(0);
    }
    expect(z.get("high")!).toBeLessThan(z.get("mid-a")!);
    expect(z.get("mid-a")!).toBeLessThan(z.get("mid-b")!);
    expect(z.get("mid-b")!).toBeLessThan(z.get("low")!);
  });
});

// ---------------------------------------------------------------------------
// Round 8, W7: minimap frame (lib/agentmap/minimap.ts)
// ---------------------------------------------------------------------------

/** Where a world point lands on the minimap, per MiniFrame's stated mapping. */
const agentMapContractToMini = (x: number, y: number, f: MiniFrame): [number, number] => [(x - f.ox) * f.scale, (y - f.oy) * f.scale];

describe("miniFrame (W7)", () => {
  it("fits the union of the nodes and the view, padded by 6 by default, and centres it", () => {
    // union box: x 0..200, y 0..100; size 212x112 with pad 6 on each side leaves exactly 200x100
    const f = miniFrame([node("a", 0, 0, 100, 50)], { x: 0, y: 0, w: 200, h: 100 }, { w: 212, h: 112 });
    expect(f.scale).toBeCloseTo(1);
    expect(f.ox).toBeCloseTo(-6);
    expect(f.oy).toBeCloseTo(-6);
    expect(agentMapContractToMini(0, 0, f)[0]).toBeCloseTo(6);
    expect(agentMapContractToMini(200, 100, f)[1]).toBeCloseTo(106);
  });

  it("takes the largest scale the tighter axis allows and centres the box on the other", () => {
    // union 400x100 into 212x112, pad 6: width allows 200/400 = 0.5, height 100/100 = 1
    const f = miniFrame([node("a", 0, 0, 400, 100)], { x: 0, y: 0, w: 100, h: 50 }, { w: 212, h: 112 });
    expect(f.scale).toBeCloseTo(0.5);
    const [left, top] = agentMapContractToMini(0, 0, f);
    const [right, bottom] = agentMapContractToMini(400, 100, f);
    expect(left).toBeCloseTo(6);
    expect(right).toBeCloseTo(206);
    // 50 minimap pixels of box in 112: 31 above and 31 below
    expect(top).toBeCloseTo(31);
    expect(bottom).toBeCloseTo(81);
  });

  it("includes nodes outside the view and the view outside the nodes", () => {
    // node at -100,-50 (100x50) and view 0,0 200x100 -> union -100..200, -50..100 (300x150)
    const f = miniFrame([node("a", -100, -50, 100, 50)], { x: 0, y: 0, w: 200, h: 100 }, { w: 312, h: 162 });
    expect(f.scale).toBeCloseTo(1);
    expect(f.ox).toBeCloseTo(-106);
    expect(f.oy).toBeCloseTo(-56);
    // the view far from a node: the box spans both
    const g = miniFrame([node("a", 0, 0, 100, 100)], { x: 900, y: 0, w: 100, h: 100 }, { w: 1000, h: 1000 }, 0);
    expect(g.scale).toBeCloseTo(1);
    expect(agentMapContractToMini(0, 0, g)[0]).toBeCloseTo(0);
    expect(agentMapContractToMini(1000, 0, g)[0]).toBeCloseTo(1000);
  });

  it("honours an explicit pad", () => {
    const f = miniFrame([], { x: 0, y: 0, w: 200, h: 100 }, { w: 200, h: 100 }, 0);
    expect(f.scale).toBeCloseTo(1);
    expect(f.ox).toBeCloseTo(0);
    expect(f.oy).toBeCloseTo(0);
    const g = miniFrame([], { x: 0, y: 0, w: 200, h: 100 }, { w: 220, h: 120 }, 10);
    expect(g.scale).toBeCloseTo(1);
    expect(g.ox).toBeCloseTo(-10);
    expect(g.oy).toBeCloseTo(-10);
  });

  it("uses the view rectangle alone with no nodes", () => {
    const f = miniFrame([], { x: 50, y: 20, w: 400, h: 200 }, { w: 212, h: 112 });
    expect(f.scale).toBeCloseTo(0.5);
    expect(agentMapContractToMini(50, 20, f)[0]).toBeCloseTo(6);
    expect(agentMapContractToMini(450, 220, f)[0]).toBeCloseTo(206);
    expect(agentMapContractToMini(50, 20, f)[1]).toBeCloseTo(6);
    expect(agentMapContractToMini(450, 220, f)[1]).toBeCloseTo(106);
  });

  it("leaves out a node with a non-finite coordinate", () => {
    const view = { x: 0, y: 0, w: 200, h: 100 };
    const size = { w: 212, h: 112 };
    const clean = miniFrame([node("a", 0, 0, 100, 50)], view, size);
    const withBad = miniFrame([node("a", 0, 0, 100, 50), node("bad", Number.NaN, 5000), node("inf", 0, Number.POSITIVE_INFINITY)], view, size);
    expect(withBad.scale).toBeCloseTo(clean.scale);
    expect(withBad.ox).toBeCloseTo(clean.ox);
    expect(withBad.oy).toBeCloseTo(clean.oy);
  });

  it("always answers a finite positive scale, also for a zero-size view and no nodes", () => {
    const f = miniFrame([], { x: 0, y: 0, w: 0, h: 0 }, { w: 200, h: 120 });
    expect(Number.isFinite(f.scale)).toBe(true);
    expect(f.scale).toBeGreaterThan(0);
  });
});

describe("miniToWorld (W7)", () => {
  it("is [px / scale + ox, py / scale + oy]", () => {
    const f: MiniFrame = { scale: 0.25, ox: -100, oy: 40 };
    expect(miniToWorld(10, 20, f)).toEqual([10 / 0.25 - 100, 20 / 0.25 + 40]);
    expect(miniToWorld(0, 0, f)).toEqual([-100, 40]);
  });

  it("inverts the frame miniFrame chose", () => {
    const f = miniFrame([node("a", -300, 200, 300, 140), node("b", 800, -90)], { x: 0, y: 0, w: 1200, h: 700 }, { w: 180, h: 120 });
    for (const [x, y] of [
      [0, 0],
      [-300, 200],
      [1040, 50],
    ] as const) {
      const [px, py] = agentMapContractToMini(x, y, f);
      const [wx, wy] = miniToWorld(px, py, f);
      expect(wx).toBeCloseTo(x);
      expect(wy).toBeCloseTo(y);
    }
  });
});

// ---------------------------------------------------------------------------
// Round 8, W9: coat by model, hat by role (lib/agentmap/avatar.ts)
// ---------------------------------------------------------------------------

describe("modelCoat (W9)", () => {
  it("matches fable, opus, sonnet, haiku, then gpt/codex/astra, case-insensitively", () => {
    expect(modelCoat("claude-fable-5-1")).toBe("fable");
    expect(modelCoat("claude-opus-5")).toBe("opus");
    expect(modelCoat("claude-opus-5-5")).toBe("opus");
    expect(modelCoat("opus")).toBe("opus");
    expect(modelCoat("claude-sonnet-4-5")).toBe("sonnet");
    expect(modelCoat("claude-haiku-4")).toBe("haiku");
    expect(modelCoat("gpt-6-astra")).toBe("codex");
    expect(modelCoat("gpt-5")).toBe("codex");
    expect(modelCoat("codex-mini")).toBe("codex");
    expect(modelCoat("astra")).toBe("codex");
    expect(modelCoat("Claude-OPUS-5")).toBe("opus");
    expect(modelCoat("HAIKU")).toBe("haiku");
    expect(modelCoat("GPT-6")).toBe("codex");
    expect(modelCoat("FABLE")).toBe("fable");
  });

  it("takes the first match in the stated order", () => {
    expect(modelCoat("fable-opus")).toBe("fable");
    expect(modelCoat("opus-sonnet")).toBe("opus");
    expect(modelCoat("haiku-sonnet")).toBe("sonnet");
    expect(modelCoat("gpt-haiku")).toBe("haiku");
  });

  it("is fable for anything else, an empty string and undefined", () => {
    expect(modelCoat(undefined)).toBe("fable");
    expect(modelCoat("")).toBe("fable");
    expect(modelCoat("llama-4")).toBe("fable");
  });
});

describe("COAT_HUE (W9)", () => {
  it("is exactly candidate set A's coats", () => {
    expect({ ...COAT_HUE }).toEqual({
      fable: "#ffd21f",
      opus: "#6aaef5",
      sonnet: "#f1ead9",
      haiku: "#4fc7b9",
      codex: "#4b5263",
    });
  });
});

describe("roleHat (W9)", () => {
  it("is none for a monitor or a shell whatever its name", () => {
    expect(roleHat(agent({ kind: "monitor", name: "hephaestus-go" }))).toBe("none");
    expect(roleHat(agent({ kind: "shell", name: "athena-logic" }))).toBe("none");
  });

  it("is beanie for a subagent whatever its name", () => {
    expect(roleHat(agent({ kind: "subagent", name: "hermes-fig" }))).toBe("beanie");
    expect(roleHat(agent({ kind: "subagent", name: "explore the repo" }))).toBe("beanie");
  });

  it("goes by name for a teammate or no kind, case-insensitively", () => {
    expect(roleHat(agent({ kind: "teammate", name: "hephaestus-web" }))).toBe("hardhat");
    expect(roleHat(agent({ kind: "teammate", name: "Athena-logic" }))).toBe("helmet");
    expect(roleHat(agent({ kind: "teammate", name: "HERMES-10-flow" }))).toBe("wings");
    expect(roleHat(agent({ kind: "teammate", name: "researcher" }))).toBe("cap");
    expect(roleHat(agent({ kind: undefined, name: "hephaestus" }))).toBe("hardhat");
    expect(roleHat(agent({ kind: undefined, name: "athena" }))).toBe("helmet");
    expect(roleHat(agent({ kind: undefined, name: "scribe" }))).toBe("cap");
  });

  it("matches a role only at the start of the name", () => {
    expect(roleHat(agent({ kind: "teammate", name: "my-hephaestus" }))).toBe("cap");
    expect(roleHat(agent({ kind: "teammate", name: "x-athena" }))).toBe("cap");
  });
});

// ---------------------------------------------------------------------------
// Round 9, W12: cards keep less distance (lib/agentmap/layout.ts)
// ---------------------------------------------------------------------------

const agentMapContractR9Node = (key: string, x: number, y: number, w = 100, h = 50): MapNode => ({ key, x, y, w, h });

function agentMapContractR9AnyOverlap(nodes: MapNode[]): boolean {
  for (let i = 0; i < nodes.length; i++)
    for (let j = i + 1; j < nodes.length; j++) if (overlaps(nodes[i]!, nodes[j]!)) return true;
  return false;
}

describe("overlaps in terms of the new MAP_GAP (W12)", () => {
  it("keeps 16 sideways and 64 above and below, and no more", () => {
    const a = agentMapContractR9Node("a", 0, 0);
    // sideways: closer than 16, then clear of it
    expect(overlaps(a, agentMapContractR9Node("b", 100 + 15, 0))).toBe(true);
    expect(overlaps(a, agentMapContractR9Node("b", 100 + 17, 0))).toBe(false);
    // vertically: closer than 64, then clear of it
    expect(overlaps(a, agentMapContractR9Node("b", 0, 50 + 63))).toBe(true);
    expect(overlaps(a, agentMapContractR9Node("b", 0, 50 + 65))).toBe(false);
    // the old gap's distances are now clear
    expect(overlaps(a, agentMapContractR9Node("b", 100 + 30, 0))).toBe(false);
    expect(overlaps(a, agentMapContractR9Node("b", 0, 50 + 100))).toBe(false);
  });
});

describe("a layout at rest under the old gap is at rest under the new one (W12)", () => {
  // A 3x3 grid kept just clear of the OLD gap ({ x: 36, y: 112 }).
  const grid = () => {
    const nodes: MapNode[] = [];
    for (let r = 0; r < 3; r++)
      for (let c = 0; c < 3; c++) nodes.push(agentMapContractR9Node(`g${r}${c}`, c * (100 + 37), r * (50 + 113)));
    return nodes;
  };

  it("has no overlapping pair, so settle moves nothing and reports no movement", () => {
    const nodes = grid();
    expect(agentMapContractR9AnyOverlap(nodes)).toBe(false);
    const before = nodes.map((n) => [n.x, n.y]);
    expect(settle(nodes)).toBe(false);
    expect(settle(nodes, "g11")).toBe(false);
    expect(settle(nodes, new Set(["g00", "g22"]))).toBe(false);
    expect(nodes.map((n) => [n.x, n.y])).toEqual(before);
  });

  it("is left alone by seedLayout, every node being placed", () => {
    const nodes = grid();
    const before = nodes.map((n) => [n.x, n.y]);
    seedLayout(nodes, [["g00", "g11"]]);
    expect(nodes.map((n) => [n.x, n.y])).toEqual(before);
  });

  // freeSpot's signature is the one the lead's own maprepair.test.ts uses:
  // freeSpot(card, allNodes) → whether it moved the card.
  it("does not move a card freeSpot finds clear under the new gap, though the old gap would not", () => {
    const other = agentMapContractR9Node("a", 0, 0);
    const n = agentMapContractR9Node("x", 100 + 20, 0);
    expect(freeSpot(n, [other, n])).toBe(false);
    expect([n.x, n.y]).toEqual([120, 0]);
    expect([other.x, other.y]).toEqual([0, 0]);
  });
});

describe("settle clears crowded nodes under the new gap (W12)", () => {
  it("still clears 12 crowded nodes within 400 calls, and then reports no movement", () => {
    const nodes = Array.from({ length: 12 }, (_, i) => agentMapContractR9Node(`n${i}`, i * 3, i * 2, 300, 160));
    let calls = 0;
    while (agentMapContractR9AnyOverlap(nodes) && calls < 400) {
      settle(nodes);
      calls++;
    }
    expect(agentMapContractR9AnyOverlap(nodes)).toBe(false);
    expect(settle(nodes)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Round 9, W13: marqueeKeys (lib/agentmap/layout.ts)
// ---------------------------------------------------------------------------

describe("marqueeKeys (W13)", () => {
  const four = () => [
    agentMapContractR9Node("n1", 0, 0), // 0..100 x 0..50
    agentMapContractR9Node("n2", 200, 0), // 200..300 x 0..50
    agentMapContractR9Node("n3", 0, 200), // 0..100 x 200..250
    agentMapContractR9Node("n4", 500, 500, 10, 10),
  ];

  it("returns the keys of every node the rectangle intersects, in the order of nodes", () => {
    expect(marqueeKeys(four(), { x: 50, y: 25 }, { x: 250, y: 30 })).toEqual(["n1", "n2"]);
    expect(marqueeKeys([...four()].reverse(), { x: 50, y: 25 }, { x: 250, y: 30 })).toEqual(["n2", "n1"]);
    expect(marqueeKeys(four(), { x: -1000, y: -1000 }, { x: 1000, y: 1000 })).toEqual(["n1", "n2", "n3", "n4"]);
  });

  it("takes the two corners in either order", () => {
    const want = ["n1", "n2"];
    expect(marqueeKeys(four(), { x: 250, y: 30 }, { x: 50, y: 25 })).toEqual(want);
    expect(marqueeKeys(four(), { x: 50, y: 30 }, { x: 250, y: 25 })).toEqual(want);
    expect(marqueeKeys(four(), { x: 250, y: 25 }, { x: 50, y: 30 })).toEqual(want);
  });

  it("includes a node the rectangle lies wholly inside", () => {
    expect(marqueeKeys(four(), { x: 10, y: 10 }, { x: 20, y: 20 })).toEqual(["n1"]);
  });

  it("counts touching edges and corners as intersecting", () => {
    // touches n2's right edge, x = 300
    expect(marqueeKeys(four(), { x: 300, y: 10 }, { x: 400, y: 20 })).toEqual(["n2"]);
    // touches n1's bottom-right corner, (100, 50)
    expect(marqueeKeys(four(), { x: 100, y: 50 }, { x: 150, y: 60 })).toEqual(["n1"]);
    // touches n3's top edge, y = 200, from above
    expect(marqueeKeys(four(), { x: 10, y: 150 }, { x: 20, y: 200 })).toEqual(["n3"]);
  });

  it("adds no MAP_GAP: a rectangle in the gap between boxes catches nothing", () => {
    // between n1 and n2 sideways
    expect(marqueeKeys(four(), { x: 101, y: 0 }, { x: 199, y: 50 })).toEqual([]);
    // between n1 and n3 vertically
    expect(marqueeKeys(four(), { x: 0, y: 51 }, { x: 100, y: 199 })).toEqual([]);
    // one unit off n4
    expect(marqueeKeys(four(), { x: 511, y: 511 }, { x: 600, y: 600 })).toEqual([]);
  });

  it("returns [] when the rectangle meets no node", () => {
    expect(marqueeKeys(four(), { x: 2000, y: 2000 }, { x: 3000, y: 3000 })).toEqual([]);
    expect(marqueeKeys([], { x: 0, y: 0 }, { x: 100, y: 100 })).toEqual([]);
  });

  it("mutates nothing", () => {
    const nodes = four().map((n) => Object.freeze(n));
    Object.freeze(nodes);
    const a = Object.freeze({ x: 50, y: 25 });
    const b = Object.freeze({ x: 250, y: 30 });
    const before = JSON.stringify(nodes);
    expect(marqueeKeys(nodes, a, b)).toEqual(["n1", "n2"]);
    expect(JSON.stringify(nodes)).toBe(before);
    expect(a).toEqual({ x: 50, y: 25 });
    expect(b).toEqual({ x: 250, y: 30 });
  });
});
