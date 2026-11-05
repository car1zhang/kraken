import { act, render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AgentEvent, WebActivity, WebBoard, WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch, type JobSearchModel } from "../../hooks/useJobSearch.ts";
import { COLUMNS, type Column } from "../../lib/columns.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { parseTerms } from "../../lib/jobSearch.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import { ActivityStrip } from "./ActivityStrip.tsx";
import { BoardBar } from "./BoardBar.tsx";
import { BoardView, type BoardActions } from "./BoardView.tsx";
import { JobCard } from "./JobCard.tsx";
import { MapView } from "./MapView.tsx";

// agentmap.contract.test.tsx — athena-logic's blind tests for the toggle, the
// view switch, MapView and ActivityStrip (50-contract.md W4). Canvas drawing is
// out of scope (checked by eye); jsdom has no 2D context, which MapView must
// survive.

const T0 = 1_792_000_000_000;

// mtime is seconds here, as BoardView.test.tsx does.
const agentMapContractJob = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over });

const agentMapContractActions = (): BoardActions => ({
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

function agentMapContractModel(jobs: WebJob[]): BoardModel {
  const board: WebBoard = makeBoard({
    daemon: makeDaemon({ pid: 42, last_tick: Math.floor(Date.now() / 1000) }),
    cell_cap: 3,
    live_cells: 1,
    jobs,
  });
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board,
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

/** Routes the map's two position calls; everything else answers `{}`. */
function agentMapContractStubFetch(saved: Record<string, [number, number]>) {
  const posts: Record<string, [number, number]>[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://board.local");
    const method = (init?.method ?? "GET").toUpperCase();
    let body: unknown = {};
    if (url.pathname === "/api/map/positions" && method === "GET") body = { positions: saved };
    if (url.pathname === "/api/map/positions" && method === "POST") {
      posts.push((JSON.parse(String(init?.body)) as { positions: Record<string, [number, number]> }).positions);
      body = { ok: true };
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, posts };
}

function AgentMapContractMap({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <MapView search={search} actions={actions} />;
}

function AgentMapContractBoard({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <BoardView board={board} actions={actions} search={search} />;
}

const agentMapContractActivity = (events: AgentEvent[]): WebActivity => ({
  act: events.at(-1)?.act ?? "think",
  label: events.at(-1)?.label,
  at: events.at(-1)?.t ?? T0,
  events,
});

const agentMapContractCards = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>(".map-card")];

beforeEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// ---------------------------------------------------------------------------
// BoardBar
// ---------------------------------------------------------------------------

const agentMapContractNoJobs = Object.fromEntries(COLUMNS.map((c) => [c, []])) as Record<Column, []>;

function AgentMapContractBarHost({ onView, initial }: { onView?: (v: "board" | "map") => void; initial?: "board" | "map" }) {
  const [view, setView] = useState<"board" | "map">(initial ?? "board");
  const [query, setQuery] = useState("");
  const terms = parseTerms(query);
  const search: JobSearchModel = {
    query,
    setQuery,
    terms,
    byColumn: agentMapContractNoJobs,
    archived: [],
    reasonByKey: new Map(),
    visibleKeys: new Set(),
    matched: 0,
    total: 0,
    active: terms.length > 0,
    goalFilter: null,
  };
  const handler = onView
    ? (v: "board" | "map") => {
        onView(v);
        setView(v);
      }
    : undefined;
  return <BoardBar search={search} view={view} onView={handler} />;
}

describe("BoardBar's view toggle", () => {
  it("renders #view-toggle with two buttons, the current one pressed, and reports clicks", async () => {
    const onView = vi.fn();
    render(<AgentMapContractBarHost onView={onView} />);
    const toggle = document.querySelector("#view-toggle")!;
    expect(toggle).not.toBeNull();
    const boardBtn = toggle.querySelector("#view-board")!;
    const mapBtn = toggle.querySelector("#view-map")!;
    expect(boardBtn).toHaveAttribute("aria-pressed", "true");
    expect(mapBtn).toHaveAttribute("aria-pressed", "false");

    await userEvent.click(mapBtn);
    expect(onView).toHaveBeenLastCalledWith("map");
    expect(document.querySelector("#view-map")).toHaveAttribute("aria-pressed", "true");
    expect(document.querySelector("#view-board")).toHaveAttribute("aria-pressed", "false");

    await userEvent.click(document.querySelector("#view-board")!);
    expect(onView).toHaveBeenLastCalledWith("board");
  });

  it("presses #view-map when view is map", () => {
    render(<AgentMapContractBarHost onView={vi.fn()} initial="map" />);
    expect(document.querySelector("#view-map")).toHaveAttribute("aria-pressed", "true");
    expect(document.querySelector("#view-board")).toHaveAttribute("aria-pressed", "false");
  });

  it("renders no #view-toggle without onView", () => {
    render(<AgentMapContractBarHost />);
    expect(document.querySelector(".boardbar")).not.toBeNull();
    expect(document.querySelector("#view-toggle")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// BoardView: switching views
// ---------------------------------------------------------------------------

function agentMapContractRenderBoard(jobs: WebJob[]) {
  const actions = agentMapContractActions();
  const board = agentMapContractModel(jobs);
  const utils = render(<AgentMapContractBoard board={board} actions={actions} />);
  return { ...utils, actions, board };
}

const agentMapContractTwoJobs = () => [
  agentMapContractJob({ slug: "alpha-cell", state: "working", live: true }),
  agentMapContractJob({ slug: "beta-cell", state: "blocked", live: true }),
];

describe("BoardView switches between the board and the map", () => {
  it("defaults to the board: #board, no #agent-map, and the toggle on board", () => {
    agentMapContractStubFetch({});
    const { container } = agentMapContractRenderBoard(agentMapContractTwoJobs());
    expect(container.querySelector("#board")).not.toBeNull();
    expect(container.querySelector("#agent-map")).toBeNull();
    expect(container.querySelector("#view-board")).toHaveAttribute("aria-pressed", "true");
  });

  it("shows the map on #view-map, drops #board, and persists the choice", async () => {
    agentMapContractStubFetch({});
    const { container, unmount } = agentMapContractRenderBoard(agentMapContractTwoJobs());
    await userEvent.click(container.querySelector("#view-map")!);
    expect(container.querySelector("#agent-map")).not.toBeNull();
    expect(container.querySelector("#board")).toBeNull();
    expect(localStorage.getItem("kraken.boardView")).toBe("map");
    unmount();

    // a fresh mount reads the stored view
    const again = agentMapContractRenderBoard(agentMapContractTwoJobs());
    expect(again.container.querySelector("#agent-map")).not.toBeNull();
    expect(again.container.querySelector("#board")).toBeNull();

    await userEvent.click(again.container.querySelector("#view-board")!);
    expect(again.container.querySelector("#board")).not.toBeNull();
    expect(again.container.querySelector("#agent-map")).toBeNull();
    expect(localStorage.getItem("kraken.boardView")).toBe("board");
  });

  it("starts on the map when localStorage says map", () => {
    agentMapContractStubFetch({});
    localStorage.setItem("kraken.boardView", "map");
    const { container } = agentMapContractRenderBoard(agentMapContractTwoJobs());
    expect(container.querySelector("#agent-map")).not.toBeNull();
    expect(container.querySelector("#board")).toBeNull();
  });

  it("keeps the search working on the map", async () => {
    agentMapContractStubFetch({});
    localStorage.setItem("kraken.boardView", "map");
    const { container } = agentMapContractRenderBoard(agentMapContractTwoJobs());
    expect(agentMapContractCards(container).map((c) => c.getAttribute("data-key")).sort()).toEqual(["alpha-cell", "beta-cell"]);
    await userEvent.type(container.querySelector<HTMLInputElement>("#job-search")!, "alpha");
    expect(agentMapContractCards(container).map((c) => c.getAttribute("data-key"))).toEqual(["alpha-cell"]);
  });

  it("never renders the activity strip on the column board", () => {
    agentMapContractStubFetch({});
    const job = agentMapContractJob({
      slug: "busy",
      state: "working",
      live: true,
      activity: agentMapContractActivity([{ id: "e1", t: T0, act: "edit", label: "Edit a.go", open: true }]),
    });
    const { container } = agentMapContractRenderBoard([job]);
    expect(container.querySelector("#board")).not.toBeNull();
    expect(container.querySelector(".act-now")).toBeNull();
    expect(container.querySelector(".act-ribbon")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// MapView
// ---------------------------------------------------------------------------

function agentMapContractRenderMap(jobs: WebJob[]) {
  const actions = agentMapContractActions();
  const board = agentMapContractModel(jobs);
  const utils = render(<AgentMapContractMap board={board} actions={actions} />);
  return { ...utils, actions };
}

describe("MapView", () => {
  it("renders one .map-card per non-archived job with its key, status and JobCard", () => {
    agentMapContractStubFetch({});
    const jobs = [
      agentMapContractJob({ slug: "d-job", state: "draft" }),
      agentMapContractJob({ slug: "w-job", state: "working", live: true }),
      agentMapContractJob({ slug: "q-job", state: "working", live: true, zeus_status: "question" }),
      agentMapContractJob({ slug: "b-job", state: "blocked", live: true }),
      agentMapContractJob({ slug: "p-job", state: "blocked", live: false }),
      agentMapContractJob({ slug: "l-job", state: "blocked", live: true, health: "usage limit hit" }),
      agentMapContractJob({ slug: "x-job", state: "done" }),
      agentMapContractJob({ slug: "old-job", state: "done", archived: true }),
    ];
    const { container } = agentMapContractRenderMap(jobs);
    expect(container.querySelector("#agent-map")).not.toBeNull();
    const cards = agentMapContractCards(container);
    const byKey = new Map(cards.map((c) => [c.getAttribute("data-key"), c]));
    expect([...byKey.keys()].sort()).toEqual(["b-job", "d-job", "l-job", "p-job", "q-job", "w-job", "x-job"]);
    expect(byKey.has("archive:old-job")).toBe(false);
    const status: Record<string, string> = {
      "d-job": "draft",
      "w-job": "working",
      "q-job": "question",
      "b-job": "blocked",
      "p-job": "parked",
      "l-job": "limit",
      "x-job": "done",
    };
    for (const [key, want] of Object.entries(status)) {
      const card = byKey.get(key)!;
      expect(card.getAttribute("data-status")).toBe(want);
      const inner = card.querySelector(".card");
      expect(inner).not.toBeNull();
      expect(inner!.getAttribute("data-slug")).toBe(key);
    }
  });

  it("draws each card's characters on a canvas.map-actors inside that card", () => {
    agentMapContractStubFetch({});
    const { container } = agentMapContractRenderMap(agentMapContractTwoJobs());
    for (const card of agentMapContractCards(container)) {
      expect(card.querySelector("canvas.map-actors")).not.toBeNull();
    }
  });

  it("opens what the same card opens on the board when clicked", async () => {
    agentMapContractStubFetch({});
    const { container, actions } = agentMapContractRenderMap([
      agentMapContractJob({ slug: "stuck", state: "blocked", live: true }),
      agentMapContractJob({ slug: "busy", state: "working", live: true }),
      agentMapContractJob({ slug: "idea", state: "draft" }),
    ]);
    const card = (key: string) => container.querySelector(`.map-card[data-key="${key}"] .card`)!;
    await userEvent.click(card("stuck"));
    expect(actions.onOpenSteer).toHaveBeenLastCalledWith("stuck", "unblock");
    await userEvent.click(card("busy"));
    expect(actions.onOpenSteer).toHaveBeenLastCalledWith("busy", "steer");
  });

  it("reads saved positions on mount and saves the position it seeds for an unplaced card", async () => {
    const { fetchMock, posts } = agentMapContractStubFetch({ alpha: [0, 3000], beta: [0, 0] });
    agentMapContractRenderMap([
      agentMapContractJob({ slug: "alpha", state: "working", live: true }),
      agentMapContractJob({ slug: "beta", state: "working", live: true }),
      agentMapContractJob({ slug: "gamma", state: "draft" }),
    ]);
    await waitFor(() => {
      const gets = fetchMock.mock.calls.filter(([u, init]) => {
        const url = new URL(String(u), "http://board.local");
        return url.pathname === "/api/map/positions" && ((init as RequestInit | undefined)?.method ?? "GET").toUpperCase() === "GET";
      });
      expect(gets.length).toBeGreaterThan(0);
    });
    await waitFor(() => {
      const seeded = posts.find((p) => "gamma" in p);
      expect(seeded).toBeDefined();
      const [x, y] = seeded!.gamma!;
      expect(Number.isFinite(x)).toBe(true);
      expect(Number.isFinite(y)).toBe(true);
    });
  });

  it("stacks a card lower on the map above one higher up, with an inline z-index", async () => {
    agentMapContractStubFetch({ alpha: [0, 3000], beta: [0, 0] });
    const { container } = agentMapContractRenderMap([
      agentMapContractJob({ slug: "alpha", state: "working", live: true }),
      agentMapContractJob({ slug: "beta", state: "working", live: true }),
    ]);
    const z = (key: string) => {
      const el = container.querySelector<HTMLElement>(`.map-card[data-key="${key}"]`)!;
      return Number(el.style.zIndex);
    };
    await waitFor(() => {
      expect(Number.isInteger(z("alpha")) && z("alpha") > 0).toBe(true);
      expect(Number.isInteger(z("beta")) && z("beta") > 0).toBe(true);
      // alpha is saved far below beta, so it draws on top
      expect(z("alpha")).toBeGreaterThan(z("beta"));
    });
  });

  it("carries the activity strip as a band inside the JobCard's own .card on the map", () => {
    agentMapContractStubFetch({});
    const { container } = agentMapContractRenderMap([
      agentMapContractJob({
        slug: "busy",
        state: "working",
        live: true,
        activity: agentMapContractActivity([{ id: "e1", t: T0, act: "test", label: "go test", open: true }]),
      }),
      agentMapContractJob({ slug: "quiet", state: "working", live: true }),
    ]);
    const busy = container.querySelector('.map-card[data-key="busy"]')!;
    expect(busy.querySelector(".card .act-now")).not.toBeNull();
    expect(busy.querySelector(".card .act-ribbon")).not.toBeNull();
    // nothing renders it outside a .card
    for (const el of container.querySelectorAll(".act-now, .act-ribbon")) {
      expect(el.closest(".card")).not.toBeNull();
    }
    const quiet = container.querySelector('.map-card[data-key="quiet"]')!;
    expect(quiet.querySelector(".act-now")).toBeNull();
  });

  // Cost: "the view does no layout read (offsetHeight, clientWidth,
  // getBoundingClientRect, getComputedStyle) on an ordinary animation frame".
  // Once the cards are placed and nothing changes, the frames that follow
  // are ordinary ones.
  it("does no layout read on ordinary frames once the map is settled", async () => {
    const { posts } = agentMapContractStubFetch({ alpha: [0, 3000], beta: [0, 0] });
    agentMapContractRenderMap([
      agentMapContractJob({
        slug: "alpha",
        state: "working",
        live: true,
        activity: agentMapContractActivity([{ id: "e1", t: Date.now(), act: "test", label: "go test", open: true }]),
      }),
      agentMapContractJob({ slug: "beta", state: "working", live: true }),
      agentMapContractJob({ slug: "gamma", state: "draft" }),
    ]);
    await waitFor(() => expect(posts.some((p) => "gamma" in p)).toBe(true));
    await new Promise((r) => setTimeout(r, 100));

    let reads = 0;
    const count = () => {
      reads++;
    };
    const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function () {
      count();
      return { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: () => ({}) } as DOMRect;
    });
    const style = vi.spyOn(window, "getComputedStyle");
    const props = ["offsetHeight", "offsetWidth", "clientWidth", "clientHeight"] as const;
    // each property's OWN descriptor on HTMLElement.prototype, undefined when it is inherited
    const own = props.map((p) => [p, Object.getOwnPropertyDescriptor(HTMLElement.prototype, p)] as const);
    for (const p of props) {
      Object.defineProperty(HTMLElement.prototype, p, {
        configurable: true,
        get() {
          count();
          return 0;
        },
      });
    }
    try {
      await new Promise((r) => setTimeout(r, 300));
      expect(reads + style.mock.calls.length).toBe(0);
    } finally {
      rect.mockRestore();
      style.mockRestore();
      for (const [p, d] of own) {
        if (d) Object.defineProperty(HTMLElement.prototype, p, d);
        else delete (HTMLElement.prototype as unknown as Record<string, unknown>)[p];
      }
    }
  });
});

// ---------------------------------------------------------------------------
// JobCard mapMode
// ---------------------------------------------------------------------------

describe("JobCard mapMode", () => {
  const busy = () =>
    agentMapContractJob({
      slug: "busy",
      state: "working",
      live: true,
      activity: agentMapContractActivity([{ id: "e1", t: T0, act: "web", label: "WebFetch", open: true }]),
    });

  it("carries the ActivityStrip band inside its .card with mapMode", () => {
    const { container } = render(<JobCard job={busy()} col="working" actions={agentMapContractActions()} mapMode />);
    const card = container.querySelector(".card")!;
    expect(card.querySelector(".act-now")).not.toBeNull();
    expect(card.querySelector(".act-now")!.textContent).toContain("WebFetch");
    expect(card.querySelectorAll(".act-ribbon i")).toHaveLength(1);
  });

  it("renders no strip without mapMode", () => {
    const { container } = render(<JobCard job={busy()} col="working" actions={agentMapContractActions()} />);
    expect(container.querySelector(".act-now")).toBeNull();
    expect(container.querySelector(".act-ribbon")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// ActivityStrip
// ---------------------------------------------------------------------------

describe("ActivityStrip", () => {
  it("renders nothing for a job with no activity", () => {
    const { container } = render(<ActivityStrip job={agentMapContractJob({ state: "working", live: true })} nowMs={T0} />);
    expect(container.innerHTML).toBe("");
  });

  it("shows the lead's pose and the newest call's label, and one titled <i> per ribbon event", () => {
    const events: AgentEvent[] = [
      { id: "e1", t: T0 - 5000, act: "data", label: "sqlite3", dur_ms: 100 },
      { id: "e2", t: T0 - 3000, act: "web", label: "WebFetch", open: true },
      { id: "e3", t: T0 - 1000, act: "hear", label: "from heph-1", from: "teammate", who: "heph-1" },
    ];
    const job = agentMapContractJob({ state: "working", live: true, activity: agentMapContractActivity(events) });
    const { container } = render(<ActivityStrip job={job} nowMs={T0} />);
    const now = container.querySelector(".act-now")!;
    expect(now).not.toBeNull();
    expect(now.textContent).toContain("▸ fetch");
    expect(now.textContent).toContain("WebFetch");
    const marks = [...container.querySelectorAll(".act-ribbon i")];
    expect(marks.map((i) => i.getAttribute("title"))).toEqual(["query: sqlite3", "fetch: WebFetch", "hear: from heph-1"]);
  });

  it("shows think once a closed call's hold has passed", () => {
    const events: AgentEvent[] = [{ id: "e1", t: T0, act: "check", label: "go vet", dur_ms: 1000 }];
    const job = agentMapContractJob({ state: "working", live: true, activity: agentMapContractActivity(events) });
    const { container } = render(<ActivityStrip job={job} nowMs={T0 + 1000 + 6000 + 1} />);
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ think");
    expect(container.querySelector(".act-now")!.textContent).toContain("go vet");
    expect(container.querySelector(".act-ribbon i")!.getAttribute("title")).toBe("lint: go vet");
  });

  // Amendment 6: "with no nowMs prop the strip keeps its own clock, so
  // .act-now changes to ▸ think by itself once leadPose's hold has run out,
  // with no new board payload".
  it("keeps its own clock without nowMs, turning to think when the hold runs out", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(T0 + 1000);
      const events: AgentEvent[] = [{ id: "e1", t: T0, act: "check", label: "go vet", dur_ms: 1000 }];
      const job = agentMapContractJob({ state: "working", live: true, activity: agentMapContractActivity(events) });
      const { container } = render(<ActivityStrip job={job} />);
      expect(container.querySelector(".act-now")!.textContent).toContain("▸ lint");
      // the hold ends at T0 + 1000 + 6000; nothing is re-rendered from outside
      act(() => {
        vi.advanceTimersByTime(7000);
      });
      expect(container.querySelector(".act-now")!.textContent).toContain("▸ think");
      expect(container.querySelector(".act-now")!.textContent).toContain("go vet");
    } finally {
      vi.useRealTimers();
    }
  });

  // Amendment 6: "with nowMs given it renders for exactly that instant".
  it("renders for exactly the given instant with nowMs, however long it stays mounted", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(T0 + 1000);
      const events: AgentEvent[] = [{ id: "e1", t: T0, act: "check", label: "go vet", dur_ms: 1000 }];
      const job = agentMapContractJob({ state: "working", live: true, activity: agentMapContractActivity(events) });
      const { container } = render(<ActivityStrip job={job} nowMs={T0 + 1000} />);
      expect(container.querySelector(".act-now")!.textContent).toContain("▸ lint");
      act(() => {
        vi.advanceTimersByTime(60_000);
      });
      expect(container.querySelector(".act-now")!.textContent).toContain("▸ lint");
    } finally {
      vi.useRealTimers();
    }
  });
});

// ---------------------------------------------------------------------------
// MapView amendment 6: a failed save stays queued
// ---------------------------------------------------------------------------

describe("MapView saves after a failure", () => {
  // "a save that fails keeps its changes queued, and they go out again with
  // the next save (no retry loop of its own: the next change, or the unmount
  // flush, carries them)".
  it("sends a failed save's changes again with the unmount flush", async () => {
    const posts: Record<string, [number, number]>[] = [];
    let failNext = true;
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://board.local");
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.pathname === "/api/map/positions" && method === "POST") {
        const sent = (JSON.parse(String(init?.body)) as { positions: Record<string, [number, number]> }).positions;
        posts.push(sent);
        // the first save that carries gamma fails
        if (failNext && "gamma" in sent) {
          failNext = false;
          return Promise.reject(new TypeError("network down"));
        }
      }
      const body = url.pathname === "/api/map/positions" && method === "GET" ? { positions: { alpha: [0, 3000], beta: [0, 0] } } : { ok: true };
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "OK",
        headers: new Headers({ "Content-Type": "application/json" }),
        json: () => Promise.resolve(body),
        text: () => Promise.resolve(JSON.stringify(body)),
      } as unknown as Response);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { unmount, container } = agentMapContractRenderMap([
      agentMapContractJob({ slug: "alpha", state: "working", live: true }),
      agentMapContractJob({ slug: "beta", state: "working", live: true }),
      agentMapContractJob({ slug: "gamma", state: "draft" }),
    ]);
    // the seed of gamma goes out, and that first save fails
    await waitFor(() => expect(posts.some((p) => "gamma" in p)).toBe(true));
    const failedAt = posts.findIndex((p) => "gamma" in p);
    await new Promise((r) => setTimeout(r, 50));
    // the view is not broken by it
    expect(container.querySelector('.map-card[data-key="gamma"]')).not.toBeNull();

    unmount();
    await waitFor(() => expect(posts.slice(failedAt + 1).some((p) => "gamma" in p)).toBe(true));
    const resent = posts.slice(failedAt + 1).find((p) => "gamma" in p)!;
    expect(Number.isFinite(resent.gamma![0]) && Number.isFinite(resent.gamma![1])).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// MapView amendment 7: unsaved positions kept in this browser
// ---------------------------------------------------------------------------

function agentMapContractRouteFetch(saved: Record<string, [number, number]>, failPosts: boolean) {
  const posts: Record<string, [number, number]>[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://board.local");
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.pathname === "/api/map/positions" && method === "POST") {
      posts.push((JSON.parse(String(init?.body)) as { positions: Record<string, [number, number]> }).positions);
      if (failPosts) return Promise.reject(new TypeError("read-only mirror"));
    }
    const body = url.pathname === "/api/map/positions" && method === "GET" ? { positions: saved } : { ok: true };
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return posts;
}

const agentMapContractUnsaved = (): Record<string, [number, number]> => {
  const raw = localStorage.getItem("kraken.mapUnsaved");
  return raw ? (JSON.parse(raw) as Record<string, [number, number]>) : {};
};

describe("MapView keeps unsaved positions in localStorage", () => {
  const threeJobs = () => [
    agentMapContractJob({ slug: "alpha", state: "working", live: true }),
    agentMapContractJob({ slug: "beta", state: "working", live: true }),
    agentMapContractJob({ slug: "gamma", state: "draft" }),
  ];

  it("records a seeded position the server refused under kraken.mapUnsaved", async () => {
    const posts = agentMapContractRouteFetch({ alpha: [0, 3000], beta: [0, 0] }, true);
    agentMapContractRenderMap(threeJobs());
    await waitFor(() => expect(posts.some((p) => "gamma" in p)).toBe(true));
    await waitFor(() => {
      const xy = agentMapContractUnsaved().gamma;
      expect(xy).toBeDefined();
      expect(Number.isFinite(xy![0]) && Number.isFinite(xy![1])).toBe(true);
    });
  });

  it("removes a key once a save carrying that position succeeds", async () => {
    const posts = agentMapContractRouteFetch({ alpha: [0, 3000], beta: [0, 0] }, false);
    agentMapContractRenderMap(threeJobs());
    await waitFor(() => expect(posts.some((p) => "gamma" in p)).toBe(true));
    await waitFor(() => expect("gamma" in agentMapContractUnsaved()).toBe(false));
  });

  it("prefers a browser-kept position over the server's on load, and queues it for saving", async () => {
    localStorage.setItem("kraken.mapUnsaved", JSON.stringify({ alpha: [0, -4000] }));
    const posts = agentMapContractRouteFetch({ alpha: [0, 3000], beta: [0, 0] }, false);
    const { container } = agentMapContractRenderMap(threeJobs());
    await waitFor(() => {
      const sent = posts.find((p) => "alpha" in p);
      expect(sent).toBeDefined();
      expect(sent!.alpha).toEqual([0, -4000]);
    });
    // alpha is now ABOVE beta (y -4000 < 0), so beta draws in front
    const z = (key: string) => Number(container.querySelector<HTMLElement>(`.map-card[data-key="${key}"]`)!.style.zIndex);
    await waitFor(() => expect(z("beta")).toBeGreaterThan(z("alpha")));
  });
});
