import { fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { JobCard } from "./JobCard.tsx";
import { MapView } from "./MapView.tsx";

// mapoptions.test.tsx — the map's status square beside a card's name, and
// the switch that turns the characters off.

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem("kraken.mapActors");
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

/** Every map request answered: saved positions for the two cards, saves accepted. */
function stubFetch() {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    if (String(input).includes("/api/map/positions") && init?.method !== "POST") {
      return new Response(JSON.stringify({ positions: { a: [0, 0], b: [3000, 0] } }), { status: 200 });
    }
    return new Response('{"ok":true}', { status: 200 });
  });
}

const twoJobs = () => [
  makeJob({ slug: "a", state: "working", live: true }),
  makeJob({ slug: "b", state: "draft" }),
];

const dinos = (c: HTMLElement) =>
  [...c.querySelectorAll("#agent-map button")].find((b) => b.textContent?.trim() === "dinos") as HTMLButtonElement;

describe("the status square on a map card", () => {
  it("sits before the slug in map mode and names the status", () => {
    const { container } = render(
      <JobCard job={makeJob({ slug: "w", state: "working", live: true })} col="working" actions={actions()} mapMode />,
    );
    const sq = container.querySelector(".card-top .map-status") as HTMLElement;
    expect(sq).not.toBeNull();
    expect(sq.getAttribute("data-status")).toBe("working");
    expect(sq.getAttribute("aria-label")).toBe("status: working");
    expect(sq.getAttribute("title")).toBe("working");
    // it leads the name
    expect(sq.nextElementSibling?.classList.contains("slug")).toBe(true);
  });

  it("follows the same status the map frames the card with", () => {
    const cases: [WebJob, string][] = [
      [makeJob({ slug: "d", state: "draft" }), "draft"],
      [makeJob({ slug: "x", state: "done" }), "done"],
      [makeJob({ slug: "b", state: "blocked", live: true }), "blocked"],
      [makeJob({ slug: "p", state: "blocked", live: false }), "parked"],
    ];
    for (const [job, want] of cases) {
      const { container, unmount } = render(<JobCard job={job} col="blocked" actions={actions()} mapMode />);
      expect(container.querySelector(".map-status")?.getAttribute("data-status")).toBe(want);
      unmount();
    }
  });

  it("is not rendered on the column board", () => {
    const { container } = render(
      <JobCard job={makeJob({ slug: "w", state: "working", live: true })} col="working" actions={actions()} />,
    );
    expect(container.querySelector(".map-status")).toBeNull();
  });

  it("matches each map card's own data-status", async () => {
    stubFetch();
    const { container } = render(<Harness board={model(twoJobs())} />);
    await waitFor(() => expect(container.querySelectorAll(".map-card").length).toBe(2));
    for (const card of container.querySelectorAll(".map-card")) {
      expect(card.querySelector(".map-status")?.getAttribute("data-status")).toBe(card.getAttribute("data-status"));
    }
  });
});

describe("the characters switch", () => {
  it("is on by default: every card has its characters canvas", async () => {
    stubFetch();
    const { container } = render(<Harness board={model(twoJobs())} />);
    await waitFor(() => expect(container.querySelectorAll(".map-card").length).toBe(2));
    expect(dinos(container).getAttribute("aria-pressed")).toBe("true");
    expect(container.querySelectorAll(".map-card .map-actors").length).toBe(2);
    expect(container.querySelector(".map-over")).not.toBeNull();
  });

  it("off removes every characters canvas, keeps the cards and their status, and is remembered", async () => {
    stubFetch();
    const { container, unmount } = render(<Harness board={model(twoJobs())} />);
    await waitFor(() => expect(container.querySelectorAll(".map-card").length).toBe(2));
    fireEvent.click(dinos(container));
    expect(dinos(container).getAttribute("aria-pressed")).toBe("false");
    expect(container.querySelectorAll(".map-actors").length).toBe(0);
    expect(container.querySelector(".map-over")).toBeNull();
    expect(container.querySelectorAll(".map-card").length).toBe(2);
    expect(container.querySelectorAll(".map-card .map-status").length).toBe(2);
    expect(container.querySelector("#map-minimap")).not.toBeNull();
    expect(localStorage.getItem("kraken.mapActors")).toBe("off");
    unmount();

    // a fresh mount starts off
    const again = render(<Harness board={model(twoJobs())} />);
    await waitFor(() => expect(again.container.querySelectorAll(".map-card").length).toBe(2));
    expect(dinos(again.container).getAttribute("aria-pressed")).toBe("false");
    expect(again.container.querySelectorAll(".map-actors").length).toBe(0);
  });

  it("back on restores the canvases and forgets the setting", async () => {
    localStorage.setItem("kraken.mapActors", "off");
    stubFetch();
    const { container } = render(<Harness board={model(twoJobs())} />);
    await waitFor(() => expect(container.querySelectorAll(".map-card").length).toBe(2));
    expect(container.querySelectorAll(".map-actors").length).toBe(0);
    fireEvent.click(dinos(container));
    expect(dinos(container).getAttribute("aria-pressed")).toBe("true");
    expect(container.querySelectorAll(".map-card .map-actors").length).toBe(2);
    expect(container.querySelector(".map-over")).not.toBeNull();
    expect(localStorage.getItem("kraken.mapActors")).toBeNull();
  });

  it("sends no position save and moves no card", async () => {
    stubFetch();
    const { container } = render(<Harness board={model(twoJobs())} />);
    await waitFor(() =>
      expect((container.querySelector('.map-card[data-key="b"]') as HTMLElement).style.left).toBe("3000px"),
    );
    const spy = vi.mocked(globalThis.fetch);
    const posts = () => spy.mock.calls.filter(([u, init]) => String(u).includes("/api/map/positions") && init?.method === "POST").length;
    const before = posts();
    const where = () =>
      [...container.querySelectorAll<HTMLElement>(".map-card")].map((c) => `${c.dataset.key}:${c.style.left},${c.style.top}`).join("|");
    const at = where();
    fireEvent.click(dinos(container));
    fireEvent.click(dinos(container));
    expect(where()).toBe(at);
    // saves are debounced: wait past the delay before counting
    await new Promise((r) => setTimeout(r, 900));
    expect(posts()).toBe(before);
  });
});
