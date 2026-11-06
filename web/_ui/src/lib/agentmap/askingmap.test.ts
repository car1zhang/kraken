import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebArtifact, WebDocQuestion } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { heldPapers, leadPose, leadPoseExpiry, mapStatus, openAsks } from "./activity.ts";
import { POSE_NAMES, drawKraken, fillPose, hueOf, newPose, readPalette, type PoseOpts } from "./draw.ts";
import { newScene, syncScene } from "./scene.ts";

// askingmap.test.ts — how the agent map shows a blocked job with questions
// and one with artifacts the operator has not read: open P0/P1 question rows
// give the lead the `asking` pose (a raised hand, question marks drifting
// up), and new artifacts put a stack of paper in its far hand on every
// stopped reading.

const T0 = 1_800_000_000_000;

// pose text (the drifting ?s, the sleeper's z) comes from sprite canvases;
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
    expect(openAsks(makeJob({ doc_questions: [q("Q1", "P2")] }))).toBe(0);
    expect(openAsks(makeJob({ doc_questions: [] }))).toBe(0);
    expect(openAsks(makeJob({}))).toBe(0);
  });
});

describe("a blocked job with open questions on the map", () => {
  const rows = [q("Q1", "P1")];

  it("reads asking on a blocked cell, live or gone", () => {
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: rows }))).toBe("asking");
    expect(mapStatus(makeJob({ state: "blocked", live: false, doc_questions: rows }))).toBe("asking");
    expect(mapStatus(makeJob({ state: "blocked", live: true, doc_questions: [q("Q1", "P0")] }))).toBe("asking");
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

describe("the asking pose", () => {
  const C = readPalette();
  const opts = (hue = "", papers = 0): PoseOpts => ({ seed: 0.3, moving: false, k: null, hue, C, papers });

  it("is a pose of its own, in the question colour", () => {
    expect(POSE_NAMES).toContain("asking");
    expect(hueOf(C, "asking")).toBe(C.warn);
    expect(hueOf(C, "asking")).toBe(hueOf(C, "question"));
  });

  it("faces the viewer with the near hand raised above the head and the far hand at its side", () => {
    const P = newPose();
    fillPose(P, "asking", 3.1, opts());
    expect(P.kind).toBe("asking");
    expect(P.face).toBe(0);
    expect(P.grave).toBe(0);
    expect(P.egg).toBe(0);
    // the head's crown is at y = -34; the raised hand is above it
    expect(P.hands[1]).toBeLessThan(-34);
    // the far hand hangs by the body, where the papers go
    expect(P.hands[3]).toBeGreaterThan(-15);
    // and it is not the blocked wave: that one's near hand swings at -31 ± 2
    const B = newPose();
    fillPose(B, "blocked", 3.1, opts());
    expect(P.hands[1]).toBeLessThan(B.hands[1] - 5);
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
    for (const kind of ["blocked", "asking", "question", "parked", "limit"]) {
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
