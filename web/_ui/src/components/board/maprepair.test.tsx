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

// ---- round 8 review pins

describe("MapView: a second press never takes over a card drag", () => {
  for (const where of ["the overview", "the map background"] as const) {
    it(`a press on ${where} mid-drag leaves the drop written and saved`, async () => {
      localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
      const posts = routeFetch(
        async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [0, 600] } }), { status: 200 }),
        () => new Response('{"ok":true}', { status: 200 }),
      );
      const { container } = render(<Harness board={model(jobs())} />);
      await waitFor(() => expect(card(container, "a").style.left).toBe("0px"));
      const root = container.querySelector("#agent-map") as HTMLElement;
      const second = where === "the overview" ? (container.querySelector("#map-minimap") as HTMLElement) : root;
      const worldBefore = (container.querySelector(".map-world") as HTMLElement).style.transform;
      fireEvent.mouseDown(card(container, "a"), { clientX: 100, clientY: 100, button: 0 });
      fireEvent.mouseMove(window, { clientX: 140, clientY: 100 });
      fireEvent.mouseDown(second, { clientX: 20, clientY: 20, button: 0 });
      fireEvent.mouseMove(window, { clientX: 150, clientY: 100 });
      fireEvent.mouseUp(window, { clientX: 150, clientY: 100 });
      // the view did not move, the card went where it was dropped, and the drop was saved
      expect((container.querySelector(".map-world") as HTMLElement).style.transform).toBe(worldBefore);
      expect(card(container, "a").style.left).toBe("50px");
      await waitFor(() =>
        expect(posts.some((p) => (p.body as { positions: Record<string, unknown> }).positions.a !== undefined)).toBe(
          true,
        ),
      );
      const saved = posts.map((p) => (p.body as { positions: Record<string, [number, number]> }).positions);
      expect(saved.find((p) => p.a)?.a).toEqual([50, 0]);
    });
  }
});

describe("MapView: the overview redraws only on its triggers", () => {
  it("a render that changed no card, status or view does not redraw it", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    routeFetch(
      async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [0, 600] } }), { status: 200 }),
      () => new Response('{"ok":true}', { status: 200 }),
    );
    // a 2D context for the overview only; every other canvas has none, as in jsdom
    let clears = 0;
    const fake = new Proxy({} as Record<string | symbol, unknown>, {
      get: (t, p) => (p in t ? t[p] : p === "clearRect" ? () => void clears++ : () => {}),
      set: (t, p, v) => {
        t[p] = v;
        return true;
      },
    });
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(function (this: HTMLCanvasElement) {
      return (this.id === "map-minimap" ? fake : null) as never;
    });
    const { container, rerender } = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(container, "a").style.left).toBe("0px"));
    await waitFor(() => expect(clears).toBeGreaterThan(0));
    // let any redraw the load asked for land
    await new Promise((r) => setTimeout(r, 150));
    const settled = clears;
    // the same jobs again, as a fresh payload: a render, and nothing it shows changed
    rerender(<Harness board={model(jobs())} />);
    rerender(<Harness board={model(jobs())} />);
    await new Promise((r) => setTimeout(r, 150));
    expect(clears).toBe(settled);
    // a view change does redraw it
    fireEvent.wheel(container.querySelector("#agent-map") as HTMLElement, { deltaX: 30, deltaY: 0 });
    await waitFor(() => expect(clears).toBeGreaterThan(settled));
  });
});

/** A pointer event carrying coordinates, as a browser sends one (jsdom builds none with them). */
function pointerEvent(type: string, x: number, y: number): Event {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.assign(ev, { clientX: x, clientY: y, button: 0 });
  return ev;
}

describe("MapView: input state belongs to the mounted map (amendment 10)", () => {
  it("a fresh mount handles a mouse press after an earlier mount saw pointer events", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    routeFetch(
      async () => new Response(JSON.stringify({ positions: { a: [0, 0], b: [1000, 0] } }), { status: 200 }),
      () => new Response('{"ok":true}', { status: 200 }),
    );
    const first = render(<Harness board={model(jobs())} />);
    await waitFor(() => expect(card(first.container, "a").style.left).toBe("0px"));
    // the first mount is sent a whole pointer drag: it acts on it...
    act(() => {
      card(first.container, "a").dispatchEvent(pointerEvent("pointerdown", 100, 100));
      window.dispatchEvent(pointerEvent("pointermove", 130, 100));
      window.dispatchEvent(pointerEvent("pointerup", 130, 100));
    });
    expect(card(first.container, "a").style.left).toBe("30px");
    // ...and from then on ignores mouse events, as a browser's compatibility
    // twins of a pointer press must be
    drag(card(first.container, "a"), 60, 0);
    expect(card(first.container, "a").style.left).toBe("30px");
    first.unmount();

    const second = render(<Harness board={model(jobs())} />);
    // placed (the first mount's drag may come back from this browser's
    // unsaved record, so the start is read rather than assumed)
    await waitFor(() => expect(card(second.container, "a").style.visibility).not.toBe("hidden"));
    const x0 = parseFloat(card(second.container, "a").style.left);
    // a mouse drag on the fresh mount moves the card
    drag(card(second.container, "a"), 60, 0);
    expect(parseFloat(card(second.container, "a").style.left)).toBe(x0 + 60);
  });
});

// Review round of round 9: a second press during a marquee, an Escape meant
// for something else, and a group member that leaves the map mid-drag.
describe("MapView: selection review pins", () => {
  const placedAt = { a: [0, 0], b: [1000, 0] };
  const mount = async (list: WebJob[] = jobs()) => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    const posts = routeFetch(
      async () => new Response(JSON.stringify({ positions: placedAt }), { status: 200 }),
      () => new Response('{"ok":true}', { status: 200 }),
    );
    const r = render(<Harness board={model(list)} />);
    await waitFor(() => expect(card(r.container, "b").style.left).toBe("1000px"));
    return { ...r, posts };
  };
  const shiftClick = (el: HTMLElement) => {
    fireEvent.mouseDown(el, { clientX: 50, clientY: 50, button: 0, shiftKey: true });
    fireEvent.mouseUp(window, { clientX: 50, clientY: 50 });
  };
  const selected = (c: HTMLElement) =>
    [...c.querySelectorAll<HTMLElement>(".map-card[data-selected='true']")].map((e) => e.dataset.key).sort();

  it("a press on a card never takes over a marquee in flight", async () => {
    const { container } = await mount();
    const root = container.querySelector("#agent-map") as HTMLElement;
    fireEvent.mouseDown(root, { clientX: 500, clientY: 500, button: 0, shiftKey: true });
    fireEvent.mouseMove(window, { clientX: 200, clientY: 200 });
    expect(container.querySelector("#map-marquee")).not.toBeNull();
    // a second press lands on card b mid-marquee
    fireEvent.mouseDown(card(container, "b"), { clientX: 1100, clientY: 50, button: 0 });
    fireEvent.mouseMove(window, { clientX: -10, clientY: -10 });
    fireEvent.mouseUp(window, { clientX: -10, clientY: -10 });
    // the marquee still ended: box gone, its cards selected, b unmoved
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(selected(container)).toEqual(["a"]);
    expect(card(container, "b").style.left).toBe("1000px");
  });

  it("Escape spent elsewhere, or typed in a field, keeps the selection; a free Escape empties it", async () => {
    const { container } = await mount();
    shiftClick(card(container, "a"));
    expect(selected(container)).toEqual(["a"]);
    // an overlay's escape layer already handled it
    act(() => {
      const ev = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
      ev.preventDefault();
      window.dispatchEvent(ev);
    });
    expect(selected(container)).toEqual(["a"]);
    // typed in a text field
    const input = document.createElement("input");
    document.body.appendChild(input);
    fireEvent.keyDown(input, { key: "Escape" });
    input.remove();
    expect(selected(container)).toEqual(["a"]);
    // nothing else wanted it
    fireEvent.keyDown(window, { key: "Escape" });
    expect(selected(container)).toEqual([]);
  });

  it("a group member that leaves the map mid-drag goes back to where it stood", async () => {
    const { container, rerender, posts } = await mount();
    shiftClick(card(container, "a"));
    shiftClick(card(container, "b"));
    expect(selected(container)).toEqual(["a", "b"]);
    const all = jobs();
    fireEvent.mouseDown(card(container, "a"), { clientX: 100, clientY: 100, button: 0 });
    fireEvent.mouseMove(window, { clientX: 100, clientY: 400 });
    // b leaves the map (filtered out) while the group is in the air
    rerender(<Harness board={model(all.filter((j) => j.slug === "a"))} />);
    fireEvent.mouseUp(window, { clientX: 100, clientY: 400 });
    expect(card(container, "a").style.top).toBe("300px");
    // b comes back where it was, and nothing saved it anywhere else
    rerender(<Harness board={model(all)} />);
    expect(card(container, "b").style.left).toBe("1000px");
    expect(card(container, "b").style.top).toBe("0px");
    await new Promise((r) => setTimeout(r, 600));
    const saved = posts.map((p) => (p.body as { positions: Record<string, [number, number]> }).positions);
    expect(saved.some((p) => p.b)).toBe(false);
  });
});
