import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebAgent } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { drawKraken, fillPose, newPose, readPalette, type DinoColors, type PoseOpts } from "./draw.ts";
import { isGirl, newScene, syncScene } from "./scene.ts";

// girllook.test.ts — about one character in three is drawn as a girl, with
// round glasses and a ponytail: who is picked, and what the look draws.

// a pose's text (the blocked flag, the sleeper's z) is drawn from sprite
// canvases; jsdom has no 2D context, and the drawing code skips a null one
beforeEach(() => {
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
});
afterEach(() => {
  vi.restoreAllMocks();
});

const agent = (name: string, over: Partial<WebAgent> = {}): WebAgent =>
  ({ name, role: "", working: true, kind: "teammate", ...over }) as WebAgent;

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
  return { c, calls, bad: () => bad, count: (name: string) => calls.filter((n) => n === name).length };
}

function draw(kind: string, girl: boolean, col: DinoColors, face?: number) {
  const C = readPalette();
  const P = newPose();
  const o: PoseOpts = { seed: 0.4, moving: false, k: null, hue: "#888888", C };
  fillPose(P, kind, 1.3, o);
  if (face !== undefined) P.face = face;
  const r = recorder();
  drawKraken(r.c, 50, 50, 1, 1, P, o, col, false, "none", girl);
  return r;
}

describe("isGirl: who is drawn as a girl", () => {
  it("picks about one name in three", () => {
    let leads = 0;
    let crew = 0;
    const roles = ["hephaestus", "athena", "hermes", "relay"];
    for (let i = 0; i < 3000; i++) {
      if (isGirl(`job-${i}-${(i * 7919) % 1000}`)) leads++;
      if (isGirl(`${roles[i % 4]}-slice${i}`)) crew++;
    }
    expect(leads / 3000).toBeGreaterThan(0.28);
    expect(leads / 3000).toBeLessThan(0.39);
    expect(crew / 3000).toBeGreaterThan(0.28);
    expect(crew / 3000).toBeLessThan(0.39);
  });

  it("holds the pick still for known names", () => {
    // a change to the hash would switch every character's look at once
    expect(["agent-map", "jev-on-kraken", "athena-style", "hermes-10-flow"].map(isGirl)).toEqual([true, true, true, true]);
    expect(["athena-logic", "athena-edge", "hephaestus-web", "linear-intake"].map(isGirl)).toEqual([
      false,
      false,
      false,
      false,
    ]);
  });
});

describe("syncScene: the pick reaches the cast", () => {
  const sync = (slug: string, roster: WebAgent[]) => {
    const s = newScene(slug, 0);
    syncScene(s, makeJob({ slug, state: "working", live: true, roster }), 1, (x) => x, () => false);
    return s;
  };

  it("asks the lead by its job's slug", () => {
    expect(sync("agent-map", []).lead.girl).toBe(true);
    expect(sync("linear-intake", []).lead.girl).toBe(false);
  });

  it("asks a crew member by its own name, whatever job it is on", () => {
    for (const slug of ["agent-map", "linear-intake"]) {
      const s = sync(slug, [agent("athena-style"), agent("athena-logic")]);
      expect(s.crew.find((m) => m.name === "athena-style")?.girl).toBe(true);
      expect(s.crew.find((m) => m.name === "athena-logic")?.girl).toBe(false);
    }
  });

  it("keeps the look under any coat and any hat", () => {
    const s = sync("linear-intake", [
      agent("athena-style", { model: "claude-haiku-5-5" }),
      agent("hermes-10-flow", { kind: "subagent", model: "gpt-6-astra" }),
    ]);
    const a = s.crew.find((m) => m.name === "athena-style")!;
    const h = s.crew.find((m) => m.name === "hermes-10-flow")!;
    expect([a.girl, a.coat, a.hat]).toEqual([true, "haiku", "helmet"]);
    expect([h.girl, h.coat, h.hat]).toEqual([true, "codex", "beanie"]);
  });
});

describe("the look's colours", () => {
  it("every coat and the spent coat carry a rim and a hair colour, in both themes", () => {
    try {
      for (const theme of ["dark", "light"]) {
        document.documentElement.dataset.theme = theme;
        const C = readPalette();
        for (const col of [...Object.values(C.coats), C.dino.spent]) {
          for (const k of ["rim", "hair", "hairEdge"] as const) expect(col[k]).toMatch(/^(#|rgb)/);
        }
      }
    } finally {
      delete document.documentElement.dataset.theme;
    }
  });

  it("the rim is light on the dark slate coat and dark on every other", () => {
    const C = readPalette();
    expect(C.coats.codex.rim).toBe("#e8ecf2");
    for (const coat of ["fable", "opus", "sonnet", "haiku"] as const) expect(C.coats[coat].rim).toBe("#2b2f38");
    expect(C.dino.spent.rim).toBe("#2b2f38");
  });
});

describe("drawKraken: what the look draws", () => {
  const col = () => readPalette().coats.opus;

  it("adds lenses, a ponytail and a hair tie to the plain character", () => {
    const plain = draw("think", false, col());
    const girl = draw("think", true, col());
    // two lens rings and two glints, and the hair tie's ellipse
    expect(girl.count("arc") - plain.count("arc")).toBe(4);
    expect(girl.count("ellipse") - plain.count("ellipse")).toBe(1);
    // the ponytail's outline is the look's only extra bezier
    expect(girl.count("bezierCurveTo") - plain.count("bezierCurveTo")).toBe(1);
    expect(girl.bad()).toBe(0);
  });

  it("draws both temple arms facing out and only the near one side-on", () => {
    const lines = (face: number) => draw("think", true, col(), face).count("lineTo") - draw("think", false, col(), face).count("lineTo");
    expect(lines(0) - lines(1)).toBe(1);
  });

  it("keeps save and restore balanced, as the plain character does", () => {
    const r = draw("edit", true, col());
    expect(r.count("save")).toBe(r.count("restore"));
  });

  it("draws nothing extra on an unhatched egg", () => {
    expect(draw("draft", true, col()).calls).toEqual(draw("draft", false, col()).calls);
  });

  it("draws with finite numbers in every pose, on every coat", () => {
    const C = readPalette();
    for (const c of [...Object.values(C.coats), C.dino.spent]) {
      for (const kind of ["think", "edit", "read", "sleep", "limit", "blocked", "done"]) {
        expect(draw(kind, true, c).bad()).toBe(0);
      }
    }
  });
});
