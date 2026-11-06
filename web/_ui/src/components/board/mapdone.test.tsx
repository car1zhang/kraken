import { fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// mapdone.test.tsx — the map's `done` button: three levels (show, fade,
// hide) for done jobs, fade to begin with, remembered per browser. A hidden
// done job is off the map the way a filtered-out one is, and comes back as
// an arriving card.

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem("kraken.mapDone");
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapView");
});

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

type Positions = Record<string, [number, number]>;

/** The map's requests: `saved` answers the positions load, every save is accepted and recorded. */
function routeFetch(saved: Positions) {
  const posts: Positions[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    if (String(input).includes("/api/map/positions")) {
      if (init?.method === "POST") {
        posts.push((JSON.parse(String(init.body)) as { positions: Positions }).positions);
        return new Response('{"ok":true}', { status: 200 });
      }
      return new Response(JSON.stringify({ positions: saved }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
  return posts;
}

/** Two cards that are not done and one that is, far enough apart that nothing overlaps. */
const apart: Positions = { a: [0, 0], b: [3000, 0], d: [0, 600] };
const threeJobs = () => [
  makeJob({ slug: "a", state: "working", live: true }),
  makeJob({ slug: "b", state: "draft" }),
  makeJob({ slug: "d", state: "done" }),
];

const root = (c: HTMLElement) => c.querySelector("#agent-map") as HTMLElement;
const btn = (c: HTMLElement) => c.querySelector("#agent-map #map-done") as HTMLButtonElement;
const card = (c: HTMLElement, key: string) => c.querySelector(`.map-card[data-key="${key}"]`) as HTMLElement | null;
const keys = (c: HTMLElement) => [...c.querySelectorAll<HTMLElement>(".map-card")].map((el) => el.dataset.key).sort();
const spots = (c: HTMLElement) =>
  [...c.querySelectorAll<HTMLElement>(".map-card")]
    .map((el) => `${el.dataset.key}:${el.style.left},${el.style.top}`)
    .sort()
    .join("|");

/** Mount the three cards and wait until the saved positions are on them. */
async function mount(saved: Positions = apart, jobs: WebJob[] = threeJobs()) {
  const posts = routeFetch(saved);
  const out = render(<Harness board={model(jobs)} />);
  await waitFor(() => expect(card(out.container, "a")!.style.left).toBe(`${saved.a![0]}px`));
  return { ...out, posts };
}

/** Saves are debounced: wait past the delay so a save that was coming has been sent. */
const quiet = () => new Promise((r) => setTimeout(r, 900));

describe("the done button's levels", () => {
  it("starts at fade: every card is on the map and the root says so", async () => {
    const { container } = await mount();
    expect(keys(container)).toEqual(["a", "b", "d"]);
    expect(root(container).getAttribute("data-done")).toBe("fade");
    expect(btn(container).textContent?.trim()).toBe("done");
    expect(btn(container).getAttribute("aria-pressed")).toBe("mixed");
    expect(btn(container).getAttribute("aria-label")).toBe("done jobs: faded");
    expect(card(container, "d")!.getAttribute("data-status")).toBe("done");
    expect(localStorage.getItem("kraken.mapDone")).toBeNull();
  });

  it("steps fade, hide, show and back to fade, recording each level", async () => {
    const { container } = await mount();
    fireEvent.click(btn(container));
    expect(root(container).getAttribute("data-done")).toBe("hide");
    expect(btn(container).getAttribute("aria-pressed")).toBe("false");
    expect(btn(container).getAttribute("aria-label")).toBe("done jobs: hidden");
    expect(localStorage.getItem("kraken.mapDone")).toBe("hide");

    fireEvent.click(btn(container));
    expect(root(container).getAttribute("data-done")).toBe("show");
    expect(btn(container).getAttribute("aria-pressed")).toBe("true");
    expect(btn(container).getAttribute("aria-label")).toBe("done jobs: shown");
    expect(localStorage.getItem("kraken.mapDone")).toBe("show");

    fireEvent.click(btn(container));
    expect(root(container).getAttribute("data-done")).toBe("fade");
    expect(btn(container).getAttribute("aria-pressed")).toBe("mixed");
    expect(localStorage.getItem("kraken.mapDone")).toBeNull();
  });

  it("is remembered: a fresh mount starts at the stored level", async () => {
    localStorage.setItem("kraken.mapDone", "hide");
    const hidden = await mount();
    expect(root(hidden.container).getAttribute("data-done")).toBe("hide");
    expect(keys(hidden.container)).toEqual(["a", "b"]);
    hidden.unmount();
    vi.restoreAllMocks();

    localStorage.setItem("kraken.mapDone", "show");
    const shown = await mount();
    expect(root(shown.container).getAttribute("data-done")).toBe("show");
    expect(keys(shown.container)).toEqual(["a", "b", "d"]);
  });

  it("reads anything else in storage as fade", async () => {
    localStorage.setItem("kraken.mapDone", "off");
    const { container } = await mount();
    expect(root(container).getAttribute("data-done")).toBe("fade");
    expect(keys(container)).toEqual(["a", "b", "d"]);
  });
});

describe("hide takes done jobs off the map", () => {
  it("removes the done card and its characters, and only those", async () => {
    const { container } = await mount();
    expect(container.querySelectorAll(".map-card .map-actors").length).toBe(3);
    fireEvent.click(btn(container));
    expect(keys(container)).toEqual(["a", "b"]);
    expect(card(container, "d")).toBeNull();
    expect(container.querySelectorAll(".map-actors").length).toBe(2);
    expect(container.querySelector("#map-minimap")).not.toBeNull();
  });

  it("moves no card that stays and sends no position save", async () => {
    const { container, posts } = await mount();
    await quiet();
    const before = posts.length;
    const at = spots(container);
    fireEvent.click(btn(container));
    expect(spots(container)).toBe(at.replace("|d:0px,600px", ""));
    await quiet();
    expect(posts.length).toBe(before);
  });

  it("drops a job from the map when it becomes done while hidden", async () => {
    localStorage.setItem("kraken.mapDone", "hide");
    const { container, rerender } = await mount();
    expect(keys(container)).toEqual(["a", "b"]);
    rerender(
      <Harness
        board={model([
          makeJob({ slug: "a", state: "done" }),
          makeJob({ slug: "b", state: "draft" }),
          makeJob({ slug: "d", state: "done" }),
        ])}
      />,
    );
    await waitFor(() => expect(keys(container)).toEqual(["b"]));
  });

  it("leaves no overview on a map whose every card is hidden", async () => {
    localStorage.setItem("kraken.mapDone", "hide");
    routeFetch({});
    const { container } = render(<Harness board={model([makeJob({ slug: "d", state: "done" })])} />);
    expect(container.querySelectorAll(".map-card").length).toBe(0);
    expect(container.querySelector("#map-minimap")).toBeNull();
    // the button is still there to bring them back
    expect(btn(container)).not.toBeNull();
  });

  it("drops a hidden card from the selection, and it comes back unselected", async () => {
    const { container } = await mount();
    const face = card(container, "d")!.querySelector(".card") as HTMLElement;
    fireEvent.mouseDown(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
    fireEvent.mouseUp(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
    fireEvent.click(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
    expect(card(container, "d")!.classList.contains("selected")).toBe(true);
    fireEvent.click(btn(container)); // hide
    fireEvent.click(btn(container)); // show
    expect(card(container, "d")).not.toBeNull();
    expect(card(container, "d")!.classList.contains("selected")).toBe(false);
  });
});

describe("the arrow layer follows the done level", () => {
  it("redraws when done cards leave the map and when they come back, and not in between", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    // a 2D context for the arrow layer only; every other canvas has none, as in jsdom
    let clears = 0;
    const fake = new Proxy({} as Record<string | symbol, unknown>, {
      get: (t, p) => (p in t ? t[p] : p === "clearRect" ? () => void clears++ : () => {}),
      set: (t, p, v) => {
        t[p] = v;
        return true;
      },
    });
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(function (this: HTMLCanvasElement) {
      return (this.classList.contains("map-under") ? fake : null) as never;
    });
    // a and d have a thread between them, long since gone quiet, so nothing
    // but a change to what the layer shows asks for a redraw
    const jobs = [
      makeJob({
        slug: "a",
        state: "working",
        live: true,
        talks: [{ slug: "d", state: "done", sent: 1, received: 1, last_at: 1 }],
      }),
      makeJob({ slug: "b", state: "draft" }),
      makeJob({ slug: "d", state: "done" }),
    ];
    const { container } = await mount(apart, jobs);
    await waitFor(() => expect(clears).toBeGreaterThan(0));
    // past the layer's own half-second check, so what the load asked for has landed
    const idle = () => new Promise((r) => setTimeout(r, 700));
    await idle();
    const settled = clears;
    await idle();
    expect(clears, "the layer is quiet while nothing changes").toBe(settled);

    fireEvent.click(btn(container)); // fade -> hide: d and its thread leave
    await waitFor(() => expect(clears).toBeGreaterThan(settled));
    await idle();
    const hidden = clears;
    await idle();
    expect(clears, "and quiet again once hidden").toBe(hidden);

    fireEvent.click(btn(container)); // hide -> show: d and its thread come back
    await waitFor(() => expect(clears).toBeGreaterThan(hidden));
    await idle();
    const shown = clears;

    fireEvent.click(btn(container)); // show -> fade: the layer shows the same thing
    await idle();
    expect(clears, "a step that takes no card off the map leaves the layer alone").toBe(shown);
  }, 15000);
});

describe("a done job coming back onto the map", () => {
  it("returns to its saved spot when that spot is free", async () => {
    localStorage.setItem("kraken.mapDone", "hide");
    const { container, posts } = await mount();
    await quiet();
    const before = posts.length;
    fireEvent.click(btn(container)); // hide -> show
    await waitFor(() => expect(card(container, "d")).not.toBeNull());
    expect(card(container, "d")!.style.left).toBe("0px");
    expect(card(container, "d")!.style.top).toBe("600px");
    await quiet();
    // nothing moved, so nothing is saved
    expect(posts.length).toBe(before);
    expect(spots(container)).toBe("a:0px,0px|b:3000px,0px|d:0px,600px");
  });

  it("yields when a showing card stands on its saved spot: it moves, the showing card does not", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    localStorage.setItem("kraken.mapDone", "hide");
    // d's saved spot overlaps a's: while d was hidden, a was put there
    const { container, posts } = await mount({ a: [0, 0], b: [3000, 0], d: [10, 10] });
    fireEvent.click(btn(container)); // hide -> show
    await waitFor(() => expect(card(container, "d")).not.toBeNull());
    await waitFor(() =>
      expect([card(container, "d")!.style.left, card(container, "d")!.style.top]).not.toEqual(["10px", "10px"]),
    );
    expect(card(container, "a")!.style.left).toBe("0px");
    expect(card(container, "a")!.style.top).toBe("0px");
    expect(card(container, "b")!.style.left).toBe("3000px");
    await waitFor(() => expect(posts.some((p) => p.d !== undefined)).toBe(true));
    await quiet();
    for (const p of posts) {
      expect(p.a, "a was showing and is not moved").toBeUndefined();
      expect(p.b, "b was showing and is not moved").toBeUndefined();
    }
  });

  it("while hidden, takes no part in physics: a card dropped on its spot stays there", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    localStorage.setItem("kraken.mapDone", "hide");
    const { container, posts } = await mount();
    // drag b onto d's saved spot (0, 600): d is hidden, so nothing pushes b off it
    const face = card(container, "b")!.querySelector(".card") as HTMLElement;
    fireEvent.mouseDown(face, { clientX: 100, clientY: 100, button: 0 });
    fireEvent.mouseMove(window, { clientX: 100 - 1500, clientY: 100 + 300 });
    fireEvent.mouseMove(window, { clientX: 100 - 3000, clientY: 100 + 600 });
    fireEvent.mouseUp(window, { clientX: 100 - 3000, clientY: 100 + 600 });
    await waitFor(() => expect(posts.some((p) => p.b !== undefined)).toBe(true));
    await quiet();
    expect(card(container, "b")!.style.left).toBe("0px");
    expect(card(container, "b")!.style.top).toBe("600px");
    for (const p of posts) expect(p.d, "d is off the map and is not moved").toBeUndefined();
  });
});
