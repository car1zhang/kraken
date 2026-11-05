import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { HOLD_MS } from "../../lib/agentmap/activity.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import { ActivityStrip } from "./ActivityStrip.tsx";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// maprepair.test.tsx — regression pins for the agent map's review round one
// (amendment 6): the activity band's own clock, a drag made before the saved
// positions arrive, and a failed save kept queued. Named outside the blind
// suites' prefix on purpose.

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapView");
});

describe("ActivityStrip keeps its own clock", () => {
  it("turns to think once the hold runs out, with no new payload", () => {
    vi.useFakeTimers();
    const t0 = new Date("2026-11-05T12:00:00Z").getTime();
    vi.setSystemTime(t0);
    const job = makeJob({
      slug: "w",
      state: "working",
      live: true,
      activity: { act: "edit", at: t0, events: [{ id: "1", t: t0, act: "edit", label: "Edit a.go", dur_ms: 200 }] },
    });
    const { container } = render(<ActivityStrip job={job} />);
    expect(container.querySelector(".act-now")?.textContent).toContain("▸ edit");
    act(() => {
      vi.advanceTimersByTime(200 + HOLD_MS - 10);
    });
    expect(container.querySelector(".act-now")?.textContent).toContain("▸ edit");
    act(() => {
      vi.advanceTimersByTime(50);
    });
    expect(container.querySelector(".act-now")?.textContent).toContain("▸ think");
    // nothing is left scheduled once the pose can no longer change with time
    expect(vi.getTimerCount()).toBe(0);
  });

  it("with nowMs renders that instant and starts no timer", () => {
    vi.useFakeTimers();
    const t0 = new Date("2026-11-05T12:00:00Z").getTime();
    vi.setSystemTime(t0);
    const job = makeJob({
      slug: "w",
      state: "working",
      live: true,
      activity: { act: "edit", at: t0, events: [{ id: "1", t: t0, act: "edit", label: "x" }] },
    });
    const { container } = render(<ActivityStrip job={job} nowMs={t0 + HOLD_MS + 1} />);
    expect(container.querySelector(".act-now")?.textContent).toContain("▸ think");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("clears its timer on unmount", () => {
    vi.useFakeTimers();
    const t0 = Date.now();
    const job = makeJob({
      slug: "w",
      state: "working",
      live: true,
      activity: { act: "edit", at: t0, events: [{ id: "1", t: t0, act: "edit", label: "x" }] },
    });
    const { unmount } = render(<ActivityStrip job={job} />);
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});

// ---- MapView harness

const actions = (): BoardActions => ({
  onOpenSteer: vi.fn(),
  onOpenEditor: vi.fn(),
  onOpenArtifact: vi.fn(),
  onOpenThread: vi.fn(),
  onAnswerQuestion: vi.fn(),
  onRetry: vi.fn(),
  onOpenTmux: vi.fn(),
  onSaveFeedback: vi.fn(async () => null),
  onDeleteFeedback: vi.fn(),
  onUnarchive: vi.fn(),
  onDeleteForever: vi.fn(),
});

function model(jobs: WebJob[]): BoardModel {
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board: makeBoard({ daemon: makeDaemon({ pid: 1, last_tick: 1 }), jobs }),
    error: undefined,
    byKey: new Map(jobs.map((j) => [jobKey(j), j])),
    byColumn,
    archived,
    refetch: async () => {},
    setDragging: () => {},
    mutate: async () => null,
    lastError: null,
  };
}

function Harness({ board }: { board: BoardModel }) {
  const search = useJobSearch(board);
  return <MapView search={search} actions={actions()} />;
}

interface Call {
  method: string;
  body: unknown;
}

/** fetch routed by path: GET /api/map/positions answers `load()`, POST answers `save()`. */
function routeFetch(load: () => Promise<Response>, save: () => Response) {
  const posts: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.includes("/api/map/positions")) {
      if (init?.method === "POST") {
        posts.push({ method: "POST", body: JSON.parse(String(init.body)) });
        return save();
      }
      return load();
    }
    return new Response("{}", { status: 200 });
  });
  return posts;
}

const card = (c: HTMLElement, key: string) => c.querySelector(`.map-card[data-key="${key}"]`) as HTMLElement;

/** A mouse drag of a card by (dx, dy) screen pixels (jsdom has no PointerEvent). */
function drag(el: HTMLElement, dx: number, dy: number) {
  fireEvent.mouseDown(el, { clientX: 100, clientY: 100, button: 0 });
  fireEvent.mouseMove(window, { clientX: 100 + dx, clientY: 100 + dy });
  fireEvent.mouseUp(window, { clientX: 100 + dx, clientY: 100 + dy });
}

const jobs = () => [makeJob({ slug: "a", state: "draft" }), makeJob({ slug: "b", state: "draft" })];

describe("MapView keeps the operator's positions", () => {
  it(
    "keeps and saves a drag made before the saved positions arrive",
    async () => {
      // a saved view pins zoom to 1, so a screen delta is a world delta
      localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
      let answer!: (r: Response) => void;
      const pending = new Promise<Response>((r) => (answer = r));
      const posts = routeFetch(() => pending, () => new Response('{"ok":true}', { status: 200 }));
      const { container } = render(<Harness board={model(jobs())} />);
      // the load is slow: the cards are placed provisionally after its timeout
      await waitFor(() => expect(card(container, "a").style.visibility).not.toBe("hidden"), { timeout: 6000 });
      const a = card(container, "a");
      const ax = parseFloat(a.style.left);
      const ay = parseFloat(a.style.top);
      drag(a, 60, 30);
      expect(parseFloat(a.style.left)).toBeCloseTo(ax + 60);
      expect(posts).toHaveLength(0);
      // the load arrives late, naming a place for both cards
      await act(async () => {
        answer(new Response(JSON.stringify({ positions: { a: [999, 999], b: [5, 7] } }), { status: 200 }));
        await pending;
      });
      await waitFor(() => expect(card(container, "b").style.left).toBe("5px"));
      expect(parseFloat(card(container, "a").style.left)).toBeCloseTo(ax + 60, 0);
      expect(parseFloat(card(container, "a").style.top)).toBeCloseTo(ay + 30, 0);
      await waitFor(() => expect(posts.length).toBeGreaterThan(0));
      const saved = posts.map((p) => (p.body as { positions: Record<string, [number, number]> }).positions);
      const aSaved = saved.find((p) => p.a)?.a;
      expect(aSaved).toEqual([Math.round(ax + 60), Math.round(ay + 30)]);
      expect(saved.some((p) => p.b)).toBe(false);
    },
    15000,
  );

  it("keeps a failed save queued and sends it with the next save", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    let fail = true;
    const posts = routeFetch(
      async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [0, 600] } }), { status: 200 }),
      () => (fail ? new Response('{"error":"nope"}', { status: 500 }) : new Response('{"ok":true}', { status: 200 })),
    );
    const { container } = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(container, "a").style.left).toBe("0px"));
    drag(card(container, "a"), 40, 0);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect((posts[0]!.body as { positions: Record<string, unknown> }).positions.a).toEqual([40, 0]);
    // no retry of its own: nothing more goes out until the next change
    await new Promise((r) => setTimeout(r, 900));
    expect(posts).toHaveLength(1);
    fail = false;
    drag(card(container, "b"), 0, 20);
    await waitFor(() => expect(posts).toHaveLength(2));
    expect((posts[1]!.body as { positions: Record<string, unknown> }).positions).toEqual({
      a: [40, 0],
      b: [0, 620],
    });
  });
});

describe("MapView's unmount flush carries a failed save", () => {
  it("sends a failed save's keys again on unmount, with no further change", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    // nothing saved yet: both cards are seeded on mount and saved
    const posts: Array<Record<string, unknown>> = [];
    let failNext = true;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes("/api/map/positions") && init?.method === "POST") {
        posts.push((JSON.parse(String(init.body)) as { positions: Record<string, unknown> }).positions);
        if (failNext) {
          failNext = false;
          throw new TypeError("network error");
        }
        return new Response('{"ok":true}', { status: 200 });
      }
      if (url.includes("/api/map/positions")) return new Response('{"positions":{}}', { status: 200 });
      return new Response("{}", { status: 200 });
    });
    const { unmount } = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(posts).toHaveLength(1));
    const first = posts[0]!;
    expect(Object.keys(first).sort()).toEqual(["a", "b"]);
    // let the failure settle; nothing else changes, so nothing else is sent
    await new Promise((r) => setTimeout(r, 600));
    expect(posts).toHaveLength(1);
    // the record holds what the server has not accepted
    expect(Object.keys(unsaved()).sort()).toEqual(["a", "b"]);
    unmount();
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1]).toEqual(first);
    // and that save succeeding clears the record
    await waitFor(() => expect(localStorage.getItem("kraken.mapUnsaved")).toBeNull());
  });
});

// ---- amendment 7

const unsaved = () => JSON.parse(localStorage.getItem("kraken.mapUnsaved") ?? "{}") as Record<string, unknown>;

describe("MapView keeps unsaved positions in this browser", () => {
  it("records a position the server refused, and lets it win on the next load", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    // the read-only mirror: every POST refused
    const load = async () =>
      new Response(JSON.stringify({ positions: { a: [0, 0], b: [0, 600] } }), { status: 200 });
    const posts = routeFetch(load, () => new Response('{"error":"read-only"}', { status: 403 }));
    const first = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(first.container, "a").style.left).toBe("0px"));
    drag(card(first.container, "a"), 40, 10);
    // written at once, before any save could have answered
    expect(unsaved().a).toEqual([40, 10]);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 50));
    expect(unsaved().a).toEqual([40, 10]);
    first.unmount();

    // a reload in the same browser: the server still says (0, 0)
    const again = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(again.container, "a").style.left).toBe("40px"));
    expect(card(again.container, "a").style.top).toBe("10px");
    expect(card(again.container, "b").style.top).toBe("600px");
    // and it is queued to save again
    await waitFor(() =>
      expect(posts.some((p) => (p.body as { positions: Record<string, unknown> }).positions.a !== undefined && posts.indexOf(p) > 0)).toBe(true),
    );
  });

  it("drops a key once a save carrying it succeeds", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    routeFetch(
      async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [0, 600] } }), { status: 200 }),
      () => new Response('{"ok":true}', { status: 200 }),
    );
    const { container } = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(container, "a").style.left).toBe("0px"));
    drag(card(container, "a"), 40, 0);
    expect(unsaved().a).toEqual([40, 0]);
    await waitFor(() => expect(localStorage.getItem("kraken.mapUnsaved")).toBeNull());
  });

  it("treats junk in the record as empty", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    localStorage.setItem("kraken.mapUnsaved", "{not json");
    routeFetch(
      async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [0, 600] } }), { status: 200 }),
      () => new Response('{"ok":true}', { status: 200 }),
    );
    const { container } = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(container, "a").style.left).toBe("0px"));
    expect(card(container, "b").style.top).toBe("600px");
  });
});

describe("MapView: a card coming back onto the map yields", () => {
  it("moves the arriving card, never the one already showing, and saves where it went", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    // b's saved spot is a's spot: while b was filtered out, a was put there
    const posts = routeFetch(
      async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [10, 10] } }), { status: 200 }),
      () => new Response('{"ok":true}', { status: 200 }),
    );
    const [a, b] = jobs();
    const { container, rerender } = render(<Harness board={model([a!])} />);
    await waitFor(() => expect(card(container, "a").style.left).toBe("0px"));
    // the filter clears: b comes back
    rerender(<Harness board={model([a!, b!])} />);
    await waitFor(() =>
      expect([card(container, "b").style.left, card(container, "b").style.top]).not.toEqual(["10px", "10px"]),
    );
    expect(card(container, "a").style.left).toBe("0px");
    expect(card(container, "a").style.top).toBe("0px");
    await waitFor(() =>
      expect(posts.some((p) => (p.body as { positions: Record<string, unknown> }).positions.b !== undefined)).toBe(true),
    );
    const saved = posts.map((p) => (p.body as { positions: Record<string, [number, number]> }).positions);
    expect(saved.some((p) => p.a !== undefined)).toBe(false);
  });
});
