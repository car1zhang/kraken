import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebArtifact, WebDocQuestion } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { heldPapers, leadPose, leadPoseExpiry, mapStatus, openAsks, openP0s } from "./activity.ts";
import {
  BLOCKED_LIGHT_HUE,
  POSE_NAMES,
  drawKraken,
  fillPose,
  hueOf,
  newPose,
  readPalette,
  type PoseOpts,
} from "./draw.ts";
import { newScene, syncScene } from "./scene.ts";

// askingmap.test.ts — how the agent map shows a blocked job with questions
// and one with artifacts the operator has not read: an open P1 question row
// gives the lead the `asking` pose and an open P0 the `urgent` one (the
// blocked wave turned up one and two steps, both arms waving under a `?` or
// `?!` flag, in red where a plain block is yellow), and new artifacts put a
// stack of paper in its far hand on every stopped reading.

const T0 = 1_800_000_000_000;

// pose text (the flags' ? and ?!, the sleeper's z) comes from sprite canvases;
// jsdom has no 2D context, and the drawing code skips a null one
beforeEach(() => {
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
});
afterEach(() => {
  vi.restoreAllMocks();
});

const q = (id: string, importance: string, answer?: string): WebDocQuestion => ({
  id,
  text: "q " + id,
  importance,
  ...(answer === undefined ? {} : { answer }),
});
const art = (name: string, isNew?: boolean): WebArtifact => ({
  name,
  url: "/api/artifact/demo/cell/" + name,
  ...(isNew ? { new: true } : {}),
});

describe("openAsks: the question rows that count", () => {
  it("counts unanswered P0 and P1 rows and nothing else", () => {
    const job = makeJob({
      doc_questions: [q("Q1", "P0"), q("Q2", "P1"), q("Q3", "P2"), q("Q4", "P0", "yes"), q("Q5", "P1", "")],
    });
    // Q5's empty answer is no answer (the chip's own `!q.answer` rule)
    expect(openAsks(job)).toBe(3);
    expect(openP0s(job)).toBe(1);
    expect(openAsks(makeJob({ doc_questions: [q("Q1", "P2")] }))).toBe(0);
    expect(openAsks(makeJob({ doc_questions: [] }))).toBe(0);
    expect(openAsks(makeJob({}))).toBe(0);
  });
});

describe("a blocked job with open questions on the map", () => {
  const rows = [q("Q1", "P1")];

  it("reads asking for an open P1 and urgent for an open P0, live or gone", () => {
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: rows }))).toBe("asking");
    expect(mapStatus(makeJob({ state: "blocked", live: false, doc_questions: rows }))).toBe("asking");
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: [q("Q1", "P0")] }))).toBe("urgent");
    expect(mapStatus(makeJob({ state: "blocked", live: false, doc_questions: [q("Q1", "P0")] }))).toBe("urgent");
    // a P0 beside a P1: the P0 decides
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: [q("Q1", "P1"), q("Q2", "P0")] }))).toBe(
      "urgent",
    );
    // an answered P0 beside an open P1 is a P1 card
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: [q("Q1", "P0", "yes"), q("Q2", "P1")] }))).toBe(
      "asking",
    );
    // an unhealthy working cell is a blocked reading too
    expect(mapStatus(makeJob({ state: "working", live: true, health: "stalled", doc_questions: rows }))).toBe("asking");
  });

  it("stays blocked when every open row is a P2 or every row is answered", () => {
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: [q("Q1", "P2")] }))).toBe("blocked");
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: [q("Q1", "P0", "do it")] }))).toBe(
      "blocked",
    );
    expect(mapStatus(makeJob({ state: "blocked", live: false, doc_questions: [q("Q1", "P0", "do it")] }))).toBe(
      "parked",
    );
  });

  it("yields to the readings ranked above it and outranks parked", () => {
    const base = { state: "blocked", live: true, doc_questions: rows } as const;
    expect(mapStatus(makeJob({ ...base, frozen_at: 1 }))).toBe("frozen");
    expect(mapStatus(makeJob({ ...base, health: "usage limit" }))).toBe("limit");
    expect(mapStatus(makeJob({ ...base, zeus_status: "question" }))).toBe("question");
    expect(mapStatus(makeJob({ ...base, question: { tool_use_id: "t" } }))).toBe("question");
    expect(mapStatus(makeJob({ ...base, live: false }))).toBe("asking");
  });

  it("is a blocked reading only: a working, done or draft job keeps its own", () => {
    expect(mapStatus(makeJob({ state: "working", live: true, doc_questions: rows }))).toBe("working");
    expect(mapStatus(makeJob({ state: "done", doc_questions: rows }))).toBe("done");
    expect(mapStatus(makeJob({ state: "draft", doc_questions: rows }))).toBe("draft");
  });

  it("holds the asking pose whatever the transcript last did, with no expiry", () => {
    const job = makeJob({
      state: "blocked",
      live: true,
      doc_questions: rows,
      activity: { act: "edit", at: T0, events: [{ id: "e", act: "edit", label: "Edit", t: T0 - 100, open: true }] },
    });
    expect(leadPose(job, T0)).toBe("asking");
    expect(leadPoseExpiry(job, T0)).toBeNull();
  });
});

describe("the question poses and the colour ladder", () => {
  const C = readPalette();
  const opts = (hue = "", papers = 0): PoseOpts => ({ seed: 0.3, moving: false, k: null, hue, C, papers });

  it("are poses of their own: blocked yellow, every question reading red", () => {
    expect(POSE_NAMES).toContain("asking");
    expect(POSE_NAMES).toContain("urgent");
    expect(C.light).toBe(false);
    expect(hueOf(C, "blocked")).toBe(C.warn);
    expect(hueOf(C, "parked")).toBe(C.parked);
    for (const id of ["question", "asking", "urgent"]) expect(hueOf(C, id), id).toBe(C.bad);
    expect(C.bad).not.toBe(C.warn);
  });

  it("gives blocked a real yellow on the light theme, the same literal in style.css", () => {
    const root = document.createElement("div");
    root.dataset.theme = "light";
    const L = readPalette(root as HTMLElement);
    expect(L.light).toBe(true);
    expect(hueOf(L, "blocked")).toBe(BLOCKED_LIGHT_HUE);
    expect(hueOf(L, "blocked")).not.toBe(L.warn);
    const css = readFileSync("src/styles/style.css", "utf8");
    expect(css).toContain(`:root[data-theme="light"] .map-card[data-status="blocked"] { --map-sc: ${BLOCKED_LIGHT_HUE}; }`);
    expect(css).toContain(`:root[data-theme="light"] [data-act="blocked"] { --act: ${BLOCKED_LIGHT_HUE}; }`);
    expect(css).toContain(
      `:root[data-theme="light"] .map-card[data-status="parked"] { --map-sc: color-mix(in srgb, ${BLOCKED_LIGHT_HUE} 55%, var(--g1)); }`,
    );
    expect(css).toContain(
      `:root[data-theme="light"] [data-act="parked"] { --act: color-mix(in srgb, ${BLOCKED_LIGHT_HUE} 55%, var(--g1)); }`,
    );
  });

  it("wave both arms facing the viewer, where the plain wave lifts one", () => {
    const B = newPose();
    fillPose(B, "blocked", 3.1, opts());
    // the default urgency (k null → 0.4) waves the near arm and hangs the far one
    expect(B.hands[1]).toBeLessThan(-25);
    expect(B.hands[3]).toBeGreaterThan(-15);
    for (const kind of ["asking", "urgent"]) {
      const P = newPose();
      fillPose(P, kind, 3.1, opts());
      expect(P.kind, kind).toBe(kind);
      expect(P.face, kind).toBe(0);
      expect(P.grave, kind).toBe(0);
      expect(P.egg, kind).toBe(0);
      expect(P.eyes.wide, kind).toBe(true);
      // both hands up by the head (y = -31 ± 2, the wave's top form)
      expect(P.hands[1], kind).toBeLessThan(-25);
      expect(P.hands[3], kind).toBeLessThan(-25);
    }
  });

  it("hop higher and more often from asking to urgent", () => {
    // the hop's height rides in P.a; sample a cycle of each and take the peak and the count
    const peak = (kind: string, per: number) => {
      let top = 0;
      let hops = 0;
      let up = false;
      for (let t = 0; t < 10; t += 0.02) {
        const P = newPose();
        fillPose(P, kind, t, opts());
        top = Math.max(top, P.a);
        if (P.a > 0.5 && !up) hops++;
        up = P.a > 0.5;
      }
      return { top, hops, per };
    };
    const a = peak("asking", 2.2);
    const u = peak("urgent", 1);
    expect(a.top).toBeGreaterThan(5);
    expect(u.top).toBeGreaterThan(a.top);
    expect(u.hops).toBeGreaterThan(a.hops * 1.5);
  });
});

/** A 2D context that records every call's name and flags a non-finite argument. */
function recorder() {
  const calls: string[] = [];
  let bad = 0;
  const store: Record<string | symbol, unknown> = { globalAlpha: 1 };
  const c = new Proxy(store, {
    get: (t, p) =>
      p in t
        ? t[p]
        : (...args: unknown[]) => {
            calls.push(String(p));
            for (const a of args) if (typeof a === "number" && !Number.isFinite(a)) bad++;
          },
    set: (t, p, v) => {
      if (typeof v === "number" && !Number.isFinite(v)) bad++;
      t[p] = v;
      return true;
    },
  }) as unknown as CanvasRenderingContext2D;
  return { calls, bad: () => bad, c, count: (name: string) => calls.filter((n) => n === name).length };
}

function drawn(kind: string, papers: number) {
  const C = readPalette();
  const P = newPose();
  const o: PoseOpts = { seed: 0.4, moving: false, k: null, hue: hueOf(C, kind), C, papers };
  fillPose(P, kind, 1.3, o);
  const r = recorder();
  drawKraken(r.c, 50, 50, 1, 1, P, o, C.coats.fable, false);
  return r;
}

describe("the papers a lead holds", () => {
  it("adds one sheet per new artifact, up to three, on any pose that has a body", () => {
    for (const kind of ["blocked", "asking", "urgent", "question", "parked", "limit"]) {
      const none = drawn(kind, 0).count("rect");
      expect(drawn(kind, 1).count("rect"), kind).toBe(none + 1);
      expect(drawn(kind, 2).count("rect"), kind).toBe(none + 2);
      expect(drawn(kind, 3).count("rect"), kind).toBe(none + 3);
      expect(drawn(kind, 9).count("rect"), kind).toBe(none + 3);
    }
  });

  it("draws every sheet with finite coordinates and leaves the context where it was", () => {
    const r = drawn("blocked", 3);
    expect(r.bad()).toBe(0);
    expect(r.count("save")).toBe(r.count("restore"));
    // each sheet's translate and rotate is undone in place, so the frame's
    // own rotate (the lean, undone the same way) leaves an even count
    expect(r.count("rotate") % 2).toBe(0);
    expect(r.count("rotate")).toBe(drawn("blocked", 0).count("rotate") + 6);
  });

  it("draws none for an egg or a headstone, which have no hands", () => {
    expect(drawn("draft", 2).count("rect")).toBe(drawn("draft", 0).count("rect"));
    expect(drawn("done", 2).count("rect")).toBe(drawn("done", 0).count("rect"));
  });

  it("is omitted when papers is absent, as every pose option literal before it", () => {
    const C = readPalette();
    const P = newPose();
    const o: PoseOpts = { seed: 0.4, moving: false, k: null, hue: hueOf(C, "blocked"), C };
    fillPose(P, "blocked", 1.3, o);
    const r = recorder();
    drawKraken(r.c, 50, 50, 1, 1, P, o, C.coats.fable, false);
    expect(r.count("rect")).toBe(drawn("blocked", 0).count("rect"));
  });
});

describe("heldPapers: which cards hold their new artifacts", () => {
  const arts = [art("00-overview.md", true), art("50-contract.md"), art("90-build-report.md", true)];

  it("counts the new artifacts on every stopped reading", () => {
    expect(heldPapers(makeJob({ state: "blocked", live: true, artifacts: arts }))).toBe(2);
    expect(heldPapers(makeJob({ state: "blocked", live: false, artifacts: arts }))).toBe(2);
    expect(heldPapers(makeJob({ state: "blocked", live: true, artifacts: arts, doc_questions: [q("Q1", "P0")] }))).toBe(
      2,
    );
    expect(heldPapers(makeJob({ state: "blocked", live: true, artifacts: arts, zeus_status: "question" }))).toBe(2);
    expect(heldPapers(makeJob({ state: "blocked", live: true, artifacts: arts, health: "usage limit" }))).toBe(2);
  });

  it("holds none on a working, frozen, done or draft card, or with nothing new", () => {
    expect(heldPapers(makeJob({ state: "working", live: true, artifacts: arts }))).toBe(0);
    expect(heldPapers(makeJob({ state: "blocked", live: true, artifacts: arts, frozen_at: 1 }))).toBe(0);
    expect(heldPapers(makeJob({ state: "done", artifacts: arts }))).toBe(0);
    expect(heldPapers(makeJob({ state: "draft", artifacts: arts }))).toBe(0);
    expect(heldPapers(makeJob({ state: "blocked", live: true, artifacts: [art("a.md"), art("b.md")] }))).toBe(0);
    expect(heldPapers(makeJob({ state: "blocked", live: true }))).toBe(0);
  });

  it("reaches the lead's pose options through the scene, and only the lead's", () => {
    const s = newScene("k", 10);
    const job = makeJob({
      state: "blocked",
      live: true,
      artifacts: arts,
      roster: [{ name: "hephaestus", model: "opus", role: "", status: "running", working: true, kind: "teammate" }],
    });
    syncScene(s, job, 10, (slug) => slug, () => false);
    expect(s.lead.papers).toBe(2);
    expect(s.crew.map((m) => m.papers)).toEqual([0]);
    // the artifact read: the next payload carries no new flag, the hand empties
    syncScene(s, makeJob({ ...job, artifacts: arts.map((a) => art(a.name)) }), 11, (slug) => slug, () => false);
    expect(s.lead.papers).toBe(0);
  });
});
