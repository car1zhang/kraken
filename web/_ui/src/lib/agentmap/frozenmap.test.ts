import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { makeJob } from "../../test/fixtures.ts";
import { leadPose, leadPoseExpiry, mapStatus } from "./activity.ts";
import { POSE_NAMES, drawKraken, fillPose, hueOf, newPose, readPalette, type PoseOpts } from "./draw.ts";

// frozenmap.test.ts — how the agent map shows a job on hold and a finished
// one: a frozen job's lead sleeps, a done job's lead is a headstone, and a
// frozen card is not dimmed on the map.

const FROZEN_AT = 1_700_000_000_000;
const T0 = 1_800_000_000_000;

const C = readPalette();
const opts = (hue = ""): PoseOpts => ({ seed: 0.3, moving: false, k: null, hue, C });

/**
 * A canvas context that records the calls a drawer makes. jsdom has no 2D
 * context, and what these tests ask is which parts of a figure were drawn,
 * which the call list answers.
 */
function recorder() {
  const calls: string[] = [];
  const target: Record<string, unknown> = { globalAlpha: 1 };
  const ctx = new Proxy(target, {
    get(t, prop: string) {
      if (prop in t) return t[prop];
      return (...args: unknown[]) => {
        calls.push(prop + "(" + args.map((a) => (typeof a === "number" ? a.toFixed(1) : String(a))).join(",") + ")");
      };
    },
    set(t, prop: string, value) {
      t[prop] = value;
      return true;
    },
  }) as unknown as CanvasRenderingContext2D;
  return { ctx, calls };
}

function drawn(kind: string): string[] {
  const P = newPose();
  const o = opts(hueOf(C, kind));
  fillPose(P, kind, 12.3, o);
  const { ctx, calls } = recorder();
  drawKraken(ctx, 0, 0, 1, 1, P, o, C.coats.fable, false);
  return calls;
}

describe("a frozen job on the map", () => {
  it("reads frozen ahead of every blocked reading", () => {
    const hold = { state: "blocked", frozen_at: FROZEN_AT } as const;
    expect(mapStatus(makeJob({ ...hold, live: true }))).toBe("frozen");
    // each of these would otherwise be question, limit and parked
    expect(mapStatus(makeJob({ ...hold, live: true, zeus_status: "question" }))).toBe("frozen");
    expect(mapStatus(makeJob({ ...hold, live: true, question: { tool_use_id: "t" } }))).toBe("frozen");
    expect(mapStatus(makeJob({ ...hold, live: true, health: "usage limit" }))).toBe("frozen");
    expect(mapStatus(makeJob({ ...hold, live: false }))).toBe("frozen");
  });

  it("ignores a stamp on a job that is not blocked", () => {
    expect(mapStatus(makeJob({ state: "working", live: true, frozen_at: FROZEN_AT }))).toBe("working");
    expect(mapStatus(makeJob({ state: "done", frozen_at: FROZEN_AT }))).toBe("done");
    expect(mapStatus(makeJob({ state: "draft", frozen_at: FROZEN_AT }))).toBe("draft");
    // an unhealthy working cell is drawn as blocked, never as frozen
    expect(mapStatus(makeJob({ state: "working", live: true, health: "stalled", frozen_at: FROZEN_AT }))).toBe(
      "blocked",
    );
  });

  it("leaves an ordinary blocked job's readings as they were", () => {
    expect(mapStatus(makeJob({ state: "blocked", live: true }))).toBe("blocked");
    expect(mapStatus(makeJob({ state: "blocked", live: false }))).toBe("parked");
    expect(mapStatus(makeJob({ state: "blocked", live: true, zeus_status: "question" }))).toBe("question");
  });

  it("holds the frozen pose whatever its transcript last did, with no expiry", () => {
    const job = makeJob({
      state: "blocked",
      live: true,
      frozen_at: FROZEN_AT,
      activity: { act: "edit", at: T0, events: [{ id: "a", t: T0, act: "edit", label: "x", open: true }] },
    });
    expect(leadPose(job, T0)).toBe("frozen");
    expect(leadPoseExpiry(job, T0)).toBeNull();
  });

  it("sleeps: seated, facing out, eyes shut, exactly as a parked lead does", () => {
    expect(POSE_NAMES).toContain("frozen");
    const frozen = newPose();
    const parked = newPose();
    fillPose(frozen, "frozen", 4.2, opts());
    fillPose(parked, "parked", 4.2, opts());
    expect(frozen.kind).toBe("frozen");
    expect(frozen.sit).toBe(1);
    expect(frozen.face).toBe(0);
    expect(frozen.eyes.lid).toBe(1);
    expect(frozen.grave).toBe(0);
    expect(frozen.egg).toBe(0);
    expect({ ...frozen, kind: "" }).toEqual({ ...parked, kind: "" });
  });

  it("draws the whole character and the z's a parked one gets", () => {
    const frozen = drawn("frozen");
    const parked = drawn("parked");
    expect(frozen).toEqual(parked);
    // a body was drawn, not a prop standing in for one
    expect(frozen.filter((c) => c.startsWith("bezierCurveTo")).length).toBeGreaterThan(4);
  });

  it("is muted on the canvas, the frame and the activity band alike", () => {
    expect(hueOf(C, "frozen")).toBe(C.muted);
    const css = readFileSync("src/styles/style.css", "utf8");
    expect(css).toContain('.map-card[data-status="frozen"] { --map-sc: var(--muted); }');
    expect(css).toContain('[data-act="frozen"] { --act: var(--muted); }');
  });

  it("is dimmed in the board's drawer only, never on the map", () => {
    const css = readFileSync("src/styles/style.css", "utf8");
    expect(css).toContain(".frozen-cards > .card.frozen { opacity: 0.7; }");
    // no rule dims `.card.frozen` wherever it is drawn, which is what made
    // the map's card translucent
    expect(css).not.toMatch(/^\.card\.frozen\s*\{[^}]*opacity/m);
    expect(css).not.toMatch(/\.map-card[^{]*\.frozen[^{]*\{[^}]*opacity/);
  });
});

describe("a done job on the map", () => {
  it("is a headstone: the pose sets grave and nothing of the sleeper", () => {
    const P = newPose();
    fillPose(P, "done", 4.2, opts());
    expect(P.kind).toBe("done");
    expect(P.grave).toBe(1);
    expect(P.egg).toBe(0);
    expect(P.sit).toBe(0);
    expect(P.eyes.lid).toBe(0);
  });

  it("clears the headstone when the actor takes another pose", () => {
    // a reactivated job's lead is the same actor, refilled
    const P = newPose();
    fillPose(P, "done", 1, opts());
    fillPose(P, "think", 2, opts());
    expect(P.grave).toBe(0);
  });

  it("draws the stone with its inscription and no character", () => {
    const done = drawn("done");
    const body = drawn("parked");
    // the inscription goes through the text sprite path, which jsdom cannot
    // rasterise, so what is checked is the shape: one arched outline, far
    // fewer path calls than a body, and no eyes (ellipse) or effects
    expect(done.filter((c) => c.startsWith("bezierCurveTo"))).toHaveLength(1);
    expect(done.some((c) => c.startsWith("ellipse"))).toBe(false);
    expect(done.length).toBeLessThan(body.length / 2);
    // balanced: the early return still restores the context it saved
    expect(done.filter((c) => c.startsWith("save")).length).toBe(done.filter((c) => c.startsWith("restore")).length);
  });

  it("keeps the done green for its grass, and the draft egg is untouched", () => {
    expect(hueOf(C, "done")).toBe(C.ok);
    const egg = newPose();
    fillPose(egg, "draft", 1, opts());
    expect(egg.egg).toBe(1);
    expect(egg.grave).toBe(0);
  });
});
