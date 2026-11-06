import { render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebAgent, WebThread } from "../../api/types.ts";
import { SteerCast, ThreadCast } from "../../components/overlays/SteerCast.tsx";
import { makeJob } from "../../test/fixtures.ts";
import { BRANCH_MAX, shownThreads, threadBranch, threadCoat } from "./avatar.ts";
import { drawKraken, fillPose, newPose, readPalette, type PoseOpts } from "./draw.ts";
import { MAP_ACTORS_KEY } from "./prefs.ts";
import { branchPose, drawScene, newScene, syncScene, threadActorName } from "./scene.ts";

// branchlook.test.tsx — a job's side threads drawn as characters (branches):
// which threads are drawn and how they are dressed, what the twig and the
// ghost draw, and the branch modal's strip.

const thread = (tid: string, over: Partial<WebThread> = {}): WebThread => ({
  tid,
  state: "live",
  created_at: 1000,
  ...over,
});
const agent = (name: string, over: Partial<WebAgent> = {}): WebAgent =>
  ({ name, role: "", working: true, kind: "teammate", ...over }) as WebAgent;

/** A 2D context that records each call's name and every globalAlpha it is given. */
function recorder() {
  const calls: string[] = [];
  const alphas: number[] = [];
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
      if (p === "globalAlpha") alphas.push(v as number);
      t[p] = v;
      return true;
    },
  }) as unknown as CanvasRenderingContext2D;
  return { c, calls, alphas, bad: () => bad, count: (name: string) => calls.filter((n) => n === name).length };
}

/** Sprite canvases get no context (jsdom has none); the drawing code skips a null one. */
const noContext = () => vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem(MAP_ACTORS_KEY);
});

describe("which look and coat a side thread takes", () => {
  it("is headless only for a headless thread", () => {
    expect(threadBranch({ kind: "headless" })).toBe("headless");
    expect(threadBranch({ kind: "pane" })).toBe("pane");
    expect(threadBranch({})).toBe("pane");
    expect(threadBranch({ kind: "something-new" })).toBe("pane");
  });

  it("wears its own model's coat, and Opus blue when none was picked", () => {
    expect(threadCoat({ model: "haiku" })).toBe("haiku");
    expect(threadCoat({ model: "sonnet" })).toBe("sonnet");
    expect(threadCoat({ model: "fable" })).toBe("fable");
    expect(threadCoat({ model: "opus" })).toBe("opus");
    expect(threadCoat({})).toBe("opus");
    expect(threadCoat({ model: "" })).toBe("opus");
  });
});

describe("shownThreads: the threads a card draws", () => {
  it("draws live threads only", () => {
    const got = shownThreads([thread("a"), thread("b", { state: "closed" }), thread("c", { state: "weird" })]);
    expect(got.map((t) => t.tid)).toEqual(["a"]);
  });

  it("puts a pane before a headless one, newest first within each", () => {
    const got = shownThreads([
      thread("h-old", { kind: "headless", created_at: 1 }),
      thread("p-old", { kind: "pane", created_at: 2 }),
      thread("h-new", { kind: "headless", created_at: 9 }),
      thread("p-new", { created_at: 8 }),
    ]);
    expect(got.map((t) => t.tid)).toEqual(["p-new", "p-old", "h-new", "h-old"]);
  });

  it("draws at most BRANCH_MAX, dropping the oldest headless first", () => {
    const many = [
      ...Array.from({ length: 6 }, (_, i) => thread("h" + i, { kind: "headless", created_at: i })),
      thread("p", { created_at: 0 }),
    ];
    const got = shownThreads(many);
    expect(got).toHaveLength(BRANCH_MAX);
    expect(got.map((t) => t.tid)).toEqual(["p", "h5", "h4", "h3"]);
  });

  it("answers an empty list for none, and skips a thread with no id", () => {
    expect(shownThreads(undefined)).toEqual([]);
    expect(shownThreads([])).toEqual([]);
    expect(shownThreads([thread("")])).toEqual([]);
  });

  it("does not reorder the list it was given", () => {
    const list = [thread("h", { kind: "headless" }), thread("p")];
    shownThreads(list);
    expect(list.map((t) => t.tid)).toEqual(["h", "p"]);
  });
});

describe("syncScene: branches join the cast", () => {
  const sync = (threads: WebThread[], roster: WebAgent[] = []) => {
    const s = newScene("j", 0);
    const job = makeJob({ slug: "j", state: "working", live: true, roster, threads });
    syncScene(s, job, 1, (x) => x, () => false);
    return { s, job };
  };
  const live = (s: ReturnType<typeof newScene>) => s.crew.filter((m) => m.leaving === null);

  it("adds one character per live thread, dressed by its kind and model", () => {
    const { s } = sync([
      thread("t1", { title: "why this way" }),
      thread("t2", { kind: "headless", model: "haiku", answering: true }),
      thread("t3", { state: "closed" }),
    ]);
    const pane = s.crew.find((m) => m.name === "branch:t1")!;
    const ghost = s.crew.find((m) => m.name === "branch:t2")!;
    expect(live(s)).toHaveLength(2);
    expect([pane.branch, pane.coat, pane.hat, pane.tag]).toEqual(["pane", "opus", "none", "⑂ why this way"]);
    expect([ghost.branch, ghost.coat, ghost.hat, ghost.tag]).toEqual(["headless", "haiku", "none", "⑂ t2"]);
    expect(s.threads.get("branch:t2")?.answering).toBe(true);
  });

  it("names a branch by its thread id, so a thread titled like a crew member is its own character", () => {
    const { s } = sync([thread("t1", { title: "athena-logic" })], [agent("athena-logic")]);
    expect(live(s).map((m) => m.name).sort()).toEqual(["athena-logic", "branch:t1"]);
    const crew = s.crew.find((m) => m.name === "athena-logic")!;
    expect([crew.branch, crew.hat]).toEqual(["none", "helmet"]);
    expect(threadActorName({ tid: "t1" })).toBe("branch:t1");
  });

  it("keeps a branch across syncs, and lets it leave when its thread closes", () => {
    const { s, job } = sync([thread("t1")], [agent("hephaestus-web")]);
    const again = () => syncScene(s, job, 2, (x) => x, () => false);
    again();
    again();
    expect(live(s).map((m) => m.name).sort()).toEqual(["branch:t1", "hephaestus-web"]);
    syncScene(s, { ...job, threads: [thread("t1", { state: "closed" })] }, 3, (x) => x, () => false);
    expect(live(s).map((m) => m.name)).toEqual(["hephaestus-web"]);
    expect(s.crew.find((m) => m.name === "branch:t1")?.leaving).toBe(3);
  });

  it("follows a thread whose kind or model changes", () => {
    const { s, job } = sync([thread("t1")]);
    syncScene(s, { ...job, threads: [thread("t1", { kind: "headless", model: "sonnet" })] }, 2, (x) => x, () => false);
    const m = s.crew.find((c) => c.name === "branch:t1")!;
    expect([m.branch, m.coat]).toEqual(["headless", "sonnet"]);
    expect(live(s)).toHaveLength(1);
  });

  it("never takes the crew's places: a full card keeps its crew", () => {
    const roster = Array.from({ length: 8 }, (_, i) => agent("relay-" + i));
    const { s } = sync([thread("t1"), thread("t2", { kind: "headless" })], roster);
    expect(live(s)).toHaveLength(8);
    expect(live(s).every((m) => m.branch === "none")).toBe(true);
  });

  it("leaves the lead and the crew unbranched", () => {
    const { s } = sync([thread("t1")], [agent("athena-edge")]);
    expect(s.lead.branch).toBe("none");
    expect(s.crew.find((m) => m.name === "athena-edge")?.branch).toBe("none");
  });
});

describe("branchPose", () => {
  it("thinks while a question is being answered and is idle otherwise", () => {
    expect(branchPose(thread("t", { answering: true }))).toBe("think");
    expect(branchPose(thread("t"))).toBe("idle");
    expect(branchPose(thread("t", { answering: false }))).toBe("idle");
    expect(branchPose(undefined)).toBe("idle");
  });
});

describe("drawKraken: what a branch draws", () => {
  beforeEach(() => {
    noContext();
  });
  const draw = (kind: string, branch: "none" | "pane" | "headless") => {
    const C = readPalette();
    const P = newPose();
    const o: PoseOpts = { seed: 0.4, moving: false, k: null, hue: "#888888", C };
    fillPose(P, kind, 1.3, o);
    const r = recorder();
    drawKraken(r.c, 50, 50, 1, 1, P, o, C.coats.opus, false, "none", false, branch);
    return r;
  };

  it("a branch with a pane is the plain character plus a twig", () => {
    const plain = draw("idle", "none");
    const pane = draw("idle", "pane");
    // the twig's two leaves, and its stem and two forks
    expect(pane.count("ellipse") - plain.count("ellipse")).toBe(2);
    expect(pane.count("quadraticCurveTo") - plain.count("quadraticCurveTo")).toBe(3);
    expect(pane.count("bezierCurveTo")).toBe(plain.count("bezierCurveTo"));
    expect(pane.bad()).toBe(0);
  });

  it("a headless branch has the twig, no feet, and a wisp", () => {
    const pane = draw("idle", "pane");
    const ghost = draw("idle", "headless");
    // two feet gone (an ellipse each), the wisp's two curves added
    expect(pane.count("ellipse") - ghost.count("ellipse")).toBe(2);
    expect(ghost.count("bezierCurveTo") - pane.count("bezierCurveTo")).toBe(2);
    expect(ghost.count("quadraticCurveTo")).toBe(pane.count("quadraticCurveTo"));
    expect(ghost.bad()).toBe(0);
  });

  it("keeps save and restore balanced and its numbers finite in every pose", () => {
    for (const branch of ["pane", "headless"] as const) {
      for (const kind of ["think", "idle", "read", "edit", "wait"]) {
        const r = draw(kind, branch);
        expect(r.count("save")).toBe(r.count("restore"));
        expect(r.bad()).toBe(0);
      }
    }
  });

  it("draws nothing extra on an unhatched egg", () => {
    expect(draw("draft", "headless").calls).toEqual(draw("draft", "none").calls);
  });
});

describe("drawScene: a headless branch is a ghost", () => {
  beforeEach(() => {
    noContext();
  });
  const alphasFor = (threads: WebThread[]) => {
    const s = newScene("j", 0);
    syncScene(s, makeJob({ slug: "j", state: "working", live: true, threads }), 0, (x) => x, () => false);
    const r = recorder();
    // well past the 0.3 s fade-in a new character has
    drawScene(r.c, s, "think", readPalette(), 5, Date.now(), 0.016);
    return r;
  };

  it("is drawn see-through, where a branch with a pane is solid", () => {
    const ghost = alphasFor([thread("t1", { kind: "headless" })]);
    expect(ghost.alphas.some((a) => Math.abs(a - 0.62) < 1e-9)).toBe(true);
    const pane = alphasFor([thread("t1")]);
    expect(pane.alphas.some((a) => Math.abs(a - 0.62) < 1e-9)).toBe(false);
    expect(ghost.bad()).toBe(0);
    expect(pane.bad()).toBe(0);
  });

  it("leaves the alpha at 1 for whatever is drawn after the crew", () => {
    const r = alphasFor([thread("t1", { kind: "headless" }), thread("t2")]);
    expect((r.c as unknown as { globalAlpha: number }).globalAlpha).toBe(1);
  });
});

describe("the strips in the modals", () => {
  function fakeContext() {
    const seen = { clears: 0, fills: 0, fillStyles: [] as string[] };
    const store: Record<string | symbol, unknown> = { globalAlpha: 1 };
    const ctx = new Proxy(store, {
      get: (t, p) => {
        if (p in t) return t[p];
        if (p === "clearRect") return () => void seen.clears++;
        if (p === "fillRect") return () => void (seen.fills++, seen.fillStyles.push(String(t.fillStyle)));
        return () => {};
      },
      set: (t, p, v) => {
        t[p] = v;
        return true;
      },
    });
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => ctx as never);
    return seen;
  }

  it("ThreadCast renders one labelled canvas for the branch and draws it", async () => {
    const seen = fakeContext();
    const { container } = render(<ThreadCast thread={thread("t1", { title: "why", kind: "headless" })} open />);
    const cv = container.querySelector("#thread-cast canvas") as HTMLCanvasElement;
    expect(cv.getAttribute("role")).toBe("img");
    expect(cv.getAttribute("aria-label")).toContain("why");
    await waitFor(() => expect(seen.clears).toBeGreaterThan(1));
    expect(seen.fills).toBeGreaterThan(0);
  });

  it("ThreadCast's ledge is the working colour while answering, green while live, red once closed", async () => {
    const C = readPalette();
    for (const [over, want] of [
      [{ answering: true }, C.ac],
      [{}, C.ok],
      [{ state: "closed" }, C.bad],
    ] as const) {
      const seen = fakeContext();
      const { unmount } = render(<ThreadCast thread={thread("t1", over)} open />);
      await waitFor(() => expect(seen.fills).toBeGreaterThan(0));
      expect(seen.fillStyles[0]).toBe(want);
      unmount();
      vi.restoreAllMocks();
    }
  });

  it("ThreadCast is not rendered when the characters are switched off", () => {
    localStorage.setItem(MAP_ACTORS_KEY, "off");
    const getContext = noContext();
    const { container } = render(<ThreadCast thread={thread("t1")} open />);
    expect(container.querySelector("#thread-cast")).toBeNull();
    expect(getContext).not.toHaveBeenCalled();
  });

  it("ThreadCast stops drawing once it is unmounted", async () => {
    const seen = fakeContext();
    const { unmount } = render(<ThreadCast thread={thread("t1")} open />);
    await waitFor(() => expect(seen.clears).toBeGreaterThan(0));
    unmount();
    const at = seen.clears;
    await new Promise((r) => setTimeout(r, 150));
    expect(seen.clears).toBe(at);
  });

  it("SteerCast still draws a job that has branches", async () => {
    const seen = fakeContext();
    const job = makeJob({ slug: "j", state: "working", live: true, threads: [thread("t1"), thread("t2", { kind: "headless" })] });
    const { container } = render(<SteerCast job={job} open />);
    expect(container.querySelector("#steer-cast canvas")).not.toBeNull();
    await waitFor(() => expect(seen.clears).toBeGreaterThan(1));
  });
});
