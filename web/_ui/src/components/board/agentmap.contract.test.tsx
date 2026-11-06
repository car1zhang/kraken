import { act, fireEvent, render, waitFor } from "@testing-library/react";
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

// ---------------------------------------------------------------------------
// Round 8: W5 (zoom never moves a card) and W7 (#map-minimap)
// ---------------------------------------------------------------------------

const agentMapContractR8Jobs = () => [
  agentMapContractJob({ slug: "alpha", state: "working", live: true }),
  agentMapContractJob({ slug: "beta", state: "working", live: true }),
  agentMapContractJob({ slug: "gamma", state: "draft" }),
];

/** Renders the map with saved alpha/beta and a seeded gamma, then waits until the first placement has been saved and settled. */
async function agentMapContractR8Settled() {
  const stub = agentMapContractStubFetch({ alpha: [0, 3000], beta: [0, 0] });
  const utils = agentMapContractRenderMap(agentMapContractR8Jobs());
  await waitFor(() => expect(stub.posts.some((p) => "gamma" in p)).toBe(true));
  await new Promise((r) => setTimeout(r, 300));
  return { ...utils, ...stub };
}

/**
 * Every card's world position and stacking, by key: MapView writes a card's
 * position as the `.map-card`'s inline `left`/`top` (px, world units) and its
 * stackOrder as the inline z-index. W5/W7: a zoom or an overview press
 * changes none of them.
 */
interface AgentMapContractR8Pos {
  left: string;
  top: string;
  z: string;
}
const agentMapContractR8Z = (root: ParentNode): Record<string, AgentMapContractR8Pos> =>
  Object.fromEntries(
    agentMapContractCards(root).map((c): [string, AgentMapContractR8Pos] => [
      c.getAttribute("data-key") ?? "",
      { left: c.style.left, top: c.style.top, z: c.style.zIndex },
    ]),
  );

/** The snapshot is not vacuous: every card has a numeric px position. */
function agentMapContractR8ExpectPlaced(snap: ReturnType<typeof agentMapContractR8Z>) {
  expect(Object.keys(snap).sort()).toEqual(["alpha", "beta", "gamma"]);
  for (const p of Object.values(snap)) {
    expect(p.left).toMatch(/^-?[\d.]+(e[+-]?\d+)?px$/);
    expect(p.top).toMatch(/^-?[\d.]+(e[+-]?\d+)?px$/);
  }
}

/** The HUD zoom button whose text is exactly `text` (`+`, `−`, `fit`, as the contract names them). */
const agentMapContractR8Hud = (root: ParentNode, text: string) =>
  [...root.querySelectorAll<HTMLButtonElement>("#agent-map button")].find((b) => b.textContent?.trim() === text);

describe("MapView zoom never moves a card (W5)", () => {
  it("sends no save and moves no card when zoomed with the wheel and a pinch", async () => {
    const { container, posts } = await agentMapContractR8Settled();
    const before = posts.length;
    const z = agentMapContractR8Z(container);
    agentMapContractR8ExpectPlaced(z);
    const map = container.querySelector<HTMLElement>("#agent-map")!;
    for (const deltaY of [-400, -400, 900, 900, 900]) {
      fireEvent.wheel(map, { deltaY, clientX: 200, clientY: 200 });
      fireEvent.wheel(map, { deltaY, ctrlKey: true, clientX: 200, clientY: 200 });
    }
    await new Promise((r) => setTimeout(r, 300));
    expect(posts.length).toBe(before);
    expect(agentMapContractR8Z(container)).toEqual(z);
  });

  it("sends no save and moves no card when zoomed with the +, − and fit buttons", async () => {
    const { container, posts } = await agentMapContractR8Settled();
    const before = posts.length;
    const z = agentMapContractR8Z(container);
    agentMapContractR8ExpectPlaced(z);
    for (const text of ["+", "+", "−", "−", "−", "fit"]) {
      const btn = agentMapContractR8Hud(container, text);
      expect(btn, `HUD button ${text}`).toBeDefined();
      await userEvent.click(btn!);
    }
    await new Promise((r) => setTimeout(r, 300));
    expect(posts.length).toBe(before);
    expect(agentMapContractR8Z(container)).toEqual(z);
  });
});

describe("MapView minimap (W7)", () => {
  it("renders #map-minimap inside #agent-map as a canvas with role img and label map overview", async () => {
    const { container } = await agentMapContractR8Settled();
    const mini = container.querySelector("#agent-map #map-minimap");
    expect(mini).not.toBeNull();
    expect(mini!.tagName).toBe("CANVAS");
    expect(mini).toHaveAttribute("role", "img");
    expect(mini).toHaveAttribute("aria-label", "map overview");
  });

  it("renders it for a map that shows a single card", () => {
    agentMapContractStubFetch({});
    const { container } = agentMapContractRenderMap([agentMapContractJob({ slug: "solo", state: "draft" })]);
    expect(container.querySelector("#agent-map #map-minimap")).not.toBeNull();
  });

  it("keeps the zoom buttons alongside it", async () => {
    const { container } = await agentMapContractR8Settled();
    expect(container.querySelector("#map-minimap")).not.toBeNull();
    for (const text of ["+", "−", "fit"]) expect(agentMapContractR8Hud(container, text), `HUD button ${text}`).toBeDefined();
  });

  it("moves no card and sends no save when pressed or dragged", async () => {
    const { container, posts } = await agentMapContractR8Settled();
    const before = posts.length;
    const z = agentMapContractR8Z(container);
    agentMapContractR8ExpectPlaced(z);
    const mini = container.querySelector<HTMLElement>("#map-minimap")!;
    fireEvent.pointerDown(mini, { clientX: 10, clientY: 10, button: 0, pointerId: 1 });
    fireEvent.pointerMove(mini, { clientX: 40, clientY: 30, buttons: 1, pointerId: 1 });
    fireEvent.pointerUp(mini, { clientX: 40, clientY: 30, pointerId: 1 });
    fireEvent.mouseDown(mini, { clientX: 20, clientY: 15, button: 0 });
    fireEvent.mouseMove(mini, { clientX: 60, clientY: 50, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 70, clientY: 55, buttons: 1 });
    fireEvent.mouseUp(window, { clientX: 70, clientY: 55 });
    fireEvent.click(mini, { clientX: 70, clientY: 55 });
    await new Promise((r) => setTimeout(r, 300));
    expect(posts.length).toBe(before);
    expect(agentMapContractR8Z(container)).toEqual(z);
    // the view is not broken by it
    expect(agentMapContractCards(container).map((c) => c.getAttribute("data-key")).sort()).toEqual(["alpha", "beta", "gamma"]);
  });
});

// ---------------------------------------------------------------------------
// Round 9: W12 (closer cards) and W13 (group select, group move)
//
// A saved view of { x: 0, y: 0, z: 1 } makes a screen point a world point
// (jsdom measures the map at the origin), so a client coordinate here is the
// world coordinate a card's inline left/top is written in. A map card is 300
// world pixels wide; the cards are saved far enough apart vertically that
// their (estimated) heights never matter.
// ---------------------------------------------------------------------------

const agentMapContractR9Jobs = () => [
  agentMapContractJob({ slug: "a", state: "working", live: true }),
  agentMapContractJob({ slug: "b", state: "draft" }),
  agentMapContractJob({ slug: "c", state: "blocked", live: true }),
];

type AgentMapContractR9Saved = Record<string, [number, number]>;

// a: x 100..400, b: x 600..900, both at y 100; c far below.
const agentMapContractR9Spread = (): AgentMapContractR9Saved => ({ a: [100, 100], b: [600, 100], c: [100, 3000] });

/** Opens the map on a saved layout and waits until it has been placed and has gone quiet. */
async function agentMapContractR9Open(saved: AgentMapContractR9Saved = agentMapContractR9Spread(), jobs: WebJob[] = agentMapContractR9Jobs()) {
  localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
  const stub = agentMapContractStubFetch(saved);
  const actions = agentMapContractActions();
  const utils = render(<AgentMapContractMap board={agentMapContractModel(jobs)} actions={actions} />);
  const last = Object.keys(saved).at(-1)!;
  await waitFor(() => expect(agentMapContractR9Card(utils.container, last).style.top).toBe(`${saved[last]![1]}px`));
  await new Promise((r) => setTimeout(r, 300));
  const rerenderJobs = (next: WebJob[]) => utils.rerender(<AgentMapContractMap board={agentMapContractModel(next)} actions={actions} />);
  return { ...utils, ...stub, actions, rerenderJobs };
}

const agentMapContractR9Card = (root: ParentNode, key: string) => root.querySelector<HTMLElement>(`.map-card[data-key="${key}"]`)!;

/** The JobCard face inside a map card: where a press on "the card" lands. */
const agentMapContractR9Face = (root: ParentNode, key: string) => agentMapContractR9Card(root, key).querySelector<HTMLElement>(".card")!;

const agentMapContractR9Root = (root: ParentNode) => root.querySelector<HTMLElement>("#agent-map")!;

const agentMapContractR9Pos = (root: ParentNode, key: string) => {
  const el = agentMapContractR9Card(root, key);
  return [el.style.left, el.style.top];
};

const agentMapContractR9AllPos = (root: ParentNode) =>
  Object.fromEntries(agentMapContractCards(root).map((c) => [c.getAttribute("data-key") ?? "", [c.style.left, c.style.top]]));

/**
 * The selected keys, sorted. W13: a selected card carries the class
 * `selected` and `data-selected="true"`; an unselected card carries neither.
 */
function agentMapContractR9Selected(root: ParentNode): string[] {
  const out: string[] = [];
  for (const c of agentMapContractCards(root)) {
    const sel = c.classList.contains("selected");
    expect(c.getAttribute("data-selected"), `data-selected on ${c.getAttribute("data-key")}`).toBe(sel ? "true" : null);
    if (sel) out.push(c.getAttribute("data-key") ?? "");
  }
  return out.sort();
}

/** A press and release with no movement between (jsdom has no PointerEvent). */
function agentMapContractR9Click(el: HTMLElement, x: number, y: number, shiftKey: boolean) {
  fireEvent.mouseDown(el, { clientX: x, clientY: y, button: 0, buttons: 1, shiftKey });
  fireEvent.mouseUp(el, { clientX: x, clientY: y, button: 0, shiftKey });
  fireEvent.click(el, { clientX: x, clientY: y, button: 0, shiftKey });
}

/** A mouse drag from `from` to `to`, with `during` run while the button is still down. */
function agentMapContractR9Drag(
  el: HTMLElement,
  from: [number, number],
  to: [number, number],
  shiftKey: boolean,
  during?: () => void,
) {
  fireEvent.mouseDown(el, { clientX: from[0], clientY: from[1], button: 0, buttons: 1, shiftKey });
  fireEvent.mouseMove(window, { clientX: (from[0] + to[0]) / 2, clientY: (from[1] + to[1]) / 2, buttons: 1, shiftKey });
  fireEvent.mouseMove(window, { clientX: to[0], clientY: to[1], buttons: 1, shiftKey });
  during?.();
  fireEvent.mouseUp(window, { clientX: to[0], clientY: to[1], button: 0, shiftKey });
}

function agentMapContractR9ExpectNothingOpened(actions: BoardActions) {
  expect(actions.onOpenSteer).not.toHaveBeenCalled();
  expect(actions.onOpenEditor).not.toHaveBeenCalled();
}

const agentMapContractR9Storage = () => {
  const out: Record<string, string | null> = {};
  for (let i = 0; i < localStorage.length; i++) {
    const k = localStorage.key(i)!;
    out[k] = localStorage.getItem(k);
  }
  return out;
};

describe("MapView: a layout saved under the old gap is at rest (W12)", () => {
  it("moves no card and sends no save for cards kept just clear of the old gap", async () => {
    // 300 wide + 37: clear of the old 36 sideways gap
    const saved: AgentMapContractR9Saved = { a: [0, 0], b: [337, 0], c: [0, 3000] };
    const { container, posts } = await agentMapContractR9Open(saved);
    expect(agentMapContractR9AllPos(container)).toEqual({
      a: ["0px", "0px"],
      b: ["337px", "0px"],
      c: ["0px", "3000px"],
    });
    expect(posts).toHaveLength(0);
  });
});

describe("MapView selection (W13)", () => {
  it("is empty at mount", async () => {
    const { container } = await agentMapContractR9Open();
    expect(agentMapContractR9Selected(container)).toEqual([]);
  });

  it("toggles a card on shift-click, opens nothing, moves nothing and saves nothing", async () => {
    const { container, posts, actions, fetchMock } = await agentMapContractR9Open();
    const where = agentMapContractR9AllPos(container);
    const stored = agentMapContractR9Storage();
    const postsBefore = posts.length;
    const callsBefore = fetchMock.mock.calls.length;

    agentMapContractR9Click(agentMapContractR9Face(container, "a"), 150, 120, true);
    expect(agentMapContractR9Selected(container)).toEqual(["a"]);
    agentMapContractR9Click(agentMapContractR9Face(container, "b"), 650, 120, true);
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);
    agentMapContractR9Click(agentMapContractR9Face(container, "a"), 150, 120, true);
    expect(agentMapContractR9Selected(container)).toEqual(["b"]);

    await new Promise((r) => setTimeout(r, 300));
    agentMapContractR9ExpectNothingOpened(actions);
    expect(agentMapContractR9AllPos(container)).toEqual(where);
    expect(posts).toHaveLength(postsBefore);
    // never saved, never sent
    const postsOfAnyKind = fetchMock.mock.calls
      .slice(callsBefore)
      .filter(([, init]) => ((init as RequestInit | undefined)?.method ?? "GET").toUpperCase() !== "GET");
    expect(postsOfAnyKind).toHaveLength(0);
    expect(agentMapContractR9Storage()).toEqual(stored);
  });

  it("drops a card that leaves the map and keeps the others", async () => {
    const { container, rerenderJobs } = await agentMapContractR9Open();
    agentMapContractR9Click(agentMapContractR9Face(container, "a"), 150, 120, true);
    agentMapContractR9Click(agentMapContractR9Face(container, "b"), 650, 120, true);
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);

    const [a, b, c] = agentMapContractR9Jobs();
    rerenderJobs([b!, c!]);
    await waitFor(() => expect(container.querySelector('.map-card[data-key="a"]')).toBeNull());
    expect(agentMapContractR9Selected(container)).toEqual(["b"]);

    // back on the map, it is not selected
    rerenderJobs([a!, b!, c!]);
    await waitFor(() => expect(container.querySelector('.map-card[data-key="a"]')).not.toBeNull());
    expect(agentMapContractR9Selected(container)).toEqual(["b"]);
  });
});

describe("MapView shift-drag draws a marquee (W13)", () => {
  it("from the background: #map-marquee only while dragging, then the selection is what it caught", async () => {
    const { container, posts, actions } = await agentMapContractR9Open();
    const where = agentMapContractR9AllPos(container);
    const postsBefore = posts.length;
    const root = agentMapContractR9Root(container);
    expect(container.querySelector("#map-marquee")).toBeNull();

    // world rectangle (50, 50)-(700, 200) catches a (100..400) and b (600..900), not c (y 3000)
    let during: Element | null = null;
    agentMapContractR9Drag(root, [50, 50], [700, 200], true, () => {
      during = root.querySelector("#map-marquee");
    });
    expect(during).not.toBeNull();
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);

    await new Promise((r) => setTimeout(r, 300));
    expect(agentMapContractR9AllPos(container)).toEqual(where);
    expect(posts).toHaveLength(postsBefore);
    agentMapContractR9ExpectNothingOpened(actions);
  });

  it("from a card: it neither moves nor opens that card", async () => {
    const { container, posts, actions } = await agentMapContractR9Open();
    const where = agentMapContractR9AllPos(container);
    const postsBefore = posts.length;
    const root = agentMapContractR9Root(container);

    let during: Element | null = null;
    // (150, 120) is on a; the rectangle reaches into b
    agentMapContractR9Drag(agentMapContractR9Face(container, "a"), [150, 120], [650, 130], true, () => {
      during = root.querySelector("#map-marquee");
    });
    expect(during).not.toBeNull();
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);

    await new Promise((r) => setTimeout(r, 300));
    expect(agentMapContractR9AllPos(container)).toEqual(where);
    expect(posts).toHaveLength(postsBefore);
    agentMapContractR9ExpectNothingOpened(actions);
  });

  it("replaces the selection with exactly what the rectangle caught", async () => {
    const { container } = await agentMapContractR9Open();
    agentMapContractR9Click(agentMapContractR9Face(container, "c"), 150, 3020, true);
    expect(agentMapContractR9Selected(container)).toEqual(["c"]);
    agentMapContractR9Drag(agentMapContractR9Root(container), [50, 50], [450, 200], true);
    expect(agentMapContractR9Selected(container)).toEqual(["a"]);
  });
});

describe("MapView group move (W13)", () => {
  it("moves every selected card by the same delta, keeps it on the drop, and saves them in one save", async () => {
    const { container, posts, actions } = await agentMapContractR9Open();
    agentMapContractR9Click(agentMapContractR9Face(container, "a"), 150, 120, true);
    agentMapContractR9Click(agentMapContractR9Face(container, "b"), 650, 120, true);
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);
    const postsBefore = posts.length;

    let midA: string[] = [];
    let midB: string[] = [];
    agentMapContractR9Drag(agentMapContractR9Face(container, "a"), [150, 120], [190, 140], false, () => {
      midA = agentMapContractR9Pos(container, "a");
      midB = agentMapContractR9Pos(container, "b");
    });
    // while in flight, both had moved by (40, 20)
    expect(midA).toEqual(["140px", "120px"]);
    expect(midB).toEqual(["640px", "120px"]);
    // the drop keeps that offset; c did not move
    expect(agentMapContractR9AllPos(container)).toEqual({
      a: ["140px", "120px"],
      b: ["640px", "120px"],
      c: ["100px", "3000px"],
    });
    // the selection is unchanged and nothing opened
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);
    agentMapContractR9ExpectNothingOpened(actions);

    // ONE save carries both
    await waitFor(() => expect(posts.slice(postsBefore).some((p) => "a" in p)).toBe(true));
    await new Promise((r) => setTimeout(r, 300));
    const after = posts.slice(postsBefore).filter((p) => "a" in p || "b" in p);
    expect(after).toHaveLength(1);
    expect(after[0]!.a).toEqual([140, 120]);
    expect(after[0]!.b).toEqual([640, 120]);
    expect("c" in after[0]!).toBe(false);
  });

  it("holds the whole selection on the drop: an unselected card in the way yields, and the one save carries it too", async () => {
    // c sits exactly where b will land
    const saved: AgentMapContractR9Saved = { a: [100, 100], b: [600, 100], c: [1200, 100] };
    const { container, posts } = await agentMapContractR9Open(saved);
    agentMapContractR9Click(agentMapContractR9Face(container, "a"), 150, 120, true);
    agentMapContractR9Click(agentMapContractR9Face(container, "b"), 650, 120, true);
    const postsBefore = posts.length;

    agentMapContractR9Drag(agentMapContractR9Face(container, "a"), [150, 120], [750, 120], false);
    await waitFor(() => expect(agentMapContractR9Pos(container, "c")).not.toEqual(["1200px", "100px"]));
    await new Promise((r) => setTimeout(r, 300));
    // the selected cards never moved off their drop
    expect(agentMapContractR9Pos(container, "a")).toEqual(["700px", "100px"]);
    expect(agentMapContractR9Pos(container, "b")).toEqual(["1200px", "100px"]);
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);

    await waitFor(() => expect(posts.slice(postsBefore).some((p) => "c" in p)).toBe(true));
    await new Promise((r) => setTimeout(r, 300));
    const after = posts.slice(postsBefore).filter((p) => "a" in p || "b" in p || "c" in p);
    expect(after).toHaveLength(1);
    expect(after[0]!.a).toEqual([700, 100]);
    expect(after[0]!.b).toEqual([1200, 100]);
    expect(after[0]!.c).toBeDefined();
    expect(after[0]!.c).not.toEqual([1200, 100]);
  });

  it("a plain drag of an unselected card moves that card alone and empties the selection", async () => {
    const { container, posts, actions } = await agentMapContractR9Open();
    agentMapContractR9Click(agentMapContractR9Face(container, "a"), 150, 120, true);
    agentMapContractR9Click(agentMapContractR9Face(container, "b"), 650, 120, true);
    const postsBefore = posts.length;

    agentMapContractR9Drag(agentMapContractR9Face(container, "c"), [150, 3020], [200, 3020], false);
    expect(agentMapContractR9AllPos(container)).toEqual({
      a: ["100px", "100px"],
      b: ["600px", "100px"],
      c: ["150px", "3000px"],
    });
    expect(agentMapContractR9Selected(container)).toEqual([]);
    agentMapContractR9ExpectNothingOpened(actions);

    await waitFor(() => expect(posts.slice(postsBefore).some((p) => "c" in p)).toBe(true));
    await new Promise((r) => setTimeout(r, 300));
    const after = posts.slice(postsBefore);
    expect(after.find((p) => "c" in p)!.c).toEqual([150, 3000]);
    expect(after.some((p) => "a" in p || "b" in p)).toBe(false);
  });
});

describe("MapView clearing the selection (W13)", () => {
  async function selectedTwo() {
    const opened = await agentMapContractR9Open();
    agentMapContractR9Click(agentMapContractR9Face(opened.container, "a"), 150, 120, true);
    agentMapContractR9Click(agentMapContractR9Face(opened.container, "b"), 650, 120, true);
    expect(agentMapContractR9Selected(opened.container)).toEqual(["a", "b"]);
    return opened;
  }

  it("Escape empties it", async () => {
    const { container } = await selectedTwo();
    fireEvent.keyDown(agentMapContractR9Root(container), { key: "Escape", code: "Escape" });
    expect(agentMapContractR9Selected(container)).toEqual([]);
  });

  it("a press and release on the background with no Shift and no drag empties it", async () => {
    const { container, actions } = await selectedTwo();
    agentMapContractR9Click(agentMapContractR9Root(container), 2000, 1500, false);
    expect(agentMapContractR9Selected(container)).toEqual([]);
    agentMapContractR9ExpectNothingOpened(actions);
  });

  it("a background drag without Shift keeps it and moves no card", async () => {
    const { container, posts } = await selectedTwo();
    const where = agentMapContractR9AllPos(container);
    const postsBefore = posts.length;
    agentMapContractR9Drag(agentMapContractR9Root(container), [2000, 1500], [2100, 1600], false);
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(agentMapContractR9Selected(container)).toEqual(["a", "b"]);
    await new Promise((r) => setTimeout(r, 300));
    expect(agentMapContractR9AllPos(container)).toEqual(where);
    expect(posts).toHaveLength(postsBefore);
  });
});
