import { act, fireEvent, render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AgentEvent, WebBoard, WebJob } from "../../api/types.ts";
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
import { MapView } from "./MapView.tsx";

// athena-edge's blind suite for the map's components (50-contract.md W3 client
// half, W4): the toggle's absence, the view under broken or odd storage,
// MapView when the position load or save fails or a canvas has no 2D context,
// archived jobs, and ActivityStrip with missing or hear-only activity.

const T0 = 1_793_000_000_000;

const agentMapEdgeJob = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over });

const agentMapEdgeActions = (): BoardActions => ({
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

function agentMapEdgeModel(jobs: WebJob[]): BoardModel {
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

function AgentMapEdgeBoard({ jobs }: { jobs: WebJob[] }) {
  const board = agentMapEdgeModel(jobs);
  const search = useJobSearch(board);
  return <BoardView board={board} actions={agentMapEdgeActions()} search={search} />;
}

function AgentMapEdgeMap({ jobs }: { jobs: WebJob[] }) {
  const search = useJobSearch(agentMapEdgeModel(jobs));
  return <MapView search={search} actions={agentMapEdgeActions()} />;
}

type AgentMapEdgeRoute = (url: string, init?: RequestInit) => Promise<Response>;

function agentMapEdgeJSON(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function agentMapEdgeURL(input: RequestInfo | URL): string {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

let agentMapEdgeFetch: ReturnType<typeof vi.fn>;

function agentMapEdgeStubFetch(route: AgentMapEdgeRoute) {
  agentMapEdgeFetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => route(agentMapEdgeURL(input), init));
  vi.stubGlobal("fetch", agentMapEdgeFetch);
}

function agentMapEdgeCalls(path: string, method: string): number {
  return agentMapEdgeFetch.mock.calls.filter(([input, init]) => {
    const m = ((init as RequestInit | undefined)?.method ?? "GET").toUpperCase();
    return agentMapEdgeURL(input as RequestInfo).includes(path) && m === method;
  }).length;
}

const agentMapEdgeFlush = () => act(async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
});

beforeEach(() => {
  localStorage.clear();
  agentMapEdgeStubFetch(async (url) => {
    if (url.includes("/api/map/positions")) return agentMapEdgeJSON({ positions: {} });
    return agentMapEdgeJSON({});
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

// --- BoardBar -------------------------------------------------------------------

const agentMapEdgeNoJobs = Object.fromEntries(COLUMNS.map((c) => [c, []])) as Record<Column, []>;

function AgentMapEdgeBar({ view, onView }: { view?: "board" | "map"; onView?: (v: "board" | "map") => void }) {
  const [query, setQuery] = useState("");
  const terms = parseTerms(query);
  const search: JobSearchModel = {
    query,
    setQuery,
    terms,
    byColumn: agentMapEdgeNoJobs,
    archived: [],
    reasonByKey: new Map(),
    visibleKeys: new Set(),
    matched: 0,
    total: 0,
    active: terms.length > 0,
    goalFilter: null,
  };
  return <BoardBar search={search} view={view} onView={onView} />;
}

describe("BoardBar's view toggle — edges", () => {
  it("renders no #view-toggle without onView, even with a view", () => {
    const { container } = render(<AgentMapEdgeBar view="map" />);
    expect(container.querySelector("#view-toggle")).toBeNull();
    expect(container.querySelector("#view-board")).toBeNull();
    expect(container.querySelector("#view-map")).toBeNull();
  });

  it("clicking the already-pressed button still calls onView with its value", async () => {
    const onView = vi.fn();
    const { container } = render(<AgentMapEdgeBar view="map" onView={onView} />);
    expect(container.querySelector("#view-map")!.getAttribute("aria-pressed")).toBe("true");
    expect(container.querySelector("#view-board")!.getAttribute("aria-pressed")).toBe("false");
    await userEvent.click(container.querySelector("#view-map")!);
    expect(onView).toHaveBeenLastCalledWith("map");
    await userEvent.click(container.querySelector("#view-board")!);
    expect(onView).toHaveBeenLastCalledWith("board");
  });
});

// --- BoardView's persisted view ---------------------------------------------------

describe("BoardView's view under odd storage", () => {
  it("an unknown stored value falls back to the board", () => {
    localStorage.setItem("kraken.boardView", "nonsense");
    const { container } = render(<AgentMapEdgeBoard jobs={[agentMapEdgeJob({ slug: "a" })]} />);
    expect(container.querySelector("#board")).not.toBeNull();
    expect(container.querySelector("#agent-map")).toBeNull();
  });

  it("storage that throws on read still renders the default board", () => {
    // scoped to the view's own key: the board's other persisted flags are
    // not this contract's business
    const realGet = Storage.prototype.getItem;
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(function (this: Storage, key: string) {
      if (key === "kraken.boardView") throw new Error("storage disabled");
      return realGet.call(this, key);
    });
    const { container } = render(<AgentMapEdgeBoard jobs={[agentMapEdgeJob({ slug: "a" })]} />);
    expect(container.querySelector("#board")).not.toBeNull();
    expect(container.querySelector("#agent-map")).toBeNull();
  });

  it("storage that throws on write still switches the view", async () => {
    const realSet = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key: string, value: string) {
      if (key === "kraken.boardView") throw new Error("quota");
      return realSet.call(this, key, value);
    });
    const { container } = render(<AgentMapEdgeBoard jobs={[agentMapEdgeJob({ slug: "a" })]} />);
    await userEvent.click(container.querySelector("#view-map")!);
    expect(container.querySelector("#agent-map")).not.toBeNull();
    expect(container.querySelector("#board")).toBeNull();
  });

  it("the map view keeps archived jobs off the map", async () => {
    localStorage.setItem("kraken.boardView", "map");
    const live = agentMapEdgeJob({ slug: "live-one", state: "working", live: true });
    const old = agentMapEdgeJob({ slug: "old", state: "done", archived: true });
    const { container } = render(<AgentMapEdgeBoard jobs={[live, old]} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#board")).toBeNull();
    const keys = [...container.querySelectorAll(".map-card")].map((c) => c.getAttribute("data-key"));
    expect(keys).toEqual([jobKey(live)]);
  });
});

// --- MapView ---------------------------------------------------------------------

const agentMapEdgeThree = () => [
  agentMapEdgeJob({ slug: "d", state: "draft" }),
  agentMapEdgeJob({ slug: "w", state: "working", live: true }),
  agentMapEdgeJob({ slug: "b", state: "blocked", live: true }),
];

function agentMapEdgeCardKeys(container: HTMLElement): string[] {
  return [...container.querySelectorAll("#agent-map .map-card")].map((c) => c.getAttribute("data-key") ?? "").sort();
}

describe("MapView — failures never break the view", () => {
  it("renders every card when the position load rejects", async () => {
    agentMapEdgeStubFetch(async (url) => {
      if (url.includes("/api/map/positions")) throw new TypeError("network down");
      return agentMapEdgeJSON({});
    });
    const jobs = agentMapEdgeThree();
    const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#agent-map")).not.toBeNull();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
  });

  it("renders every card when the position load answers 500", async () => {
    agentMapEdgeStubFetch(async (url) => {
      if (url.includes("/api/map/positions")) return new Response("boom", { status: 500 });
      return agentMapEdgeJSON({});
    });
    const jobs = agentMapEdgeThree();
    const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
  });

  it("renders every card when the position load answers garbage", async () => {
    agentMapEdgeStubFetch(async (url) => {
      if (url.includes("/api/map/positions")) return new Response("not json", { status: 200 });
      return agentMapEdgeJSON({});
    });
    const jobs = agentMapEdgeThree();
    const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
  });

  it("a failing save neither throws nor drops a card", async () => {
    agentMapEdgeStubFetch(async (url, init) => {
      if (url.includes("/api/map/positions")) {
        if ((init?.method ?? "GET").toUpperCase() === "POST") throw new TypeError("network down");
        return agentMapEdgeJSON({ positions: {} });
      }
      return agentMapEdgeJSON({});
    });
    const jobs = agentMapEdgeThree();
    const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
    // cards with no saved position are seeded, which the view must save
    await waitFor(() => expect(agentMapEdgeCalls("/api/map/positions", "POST")).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
  });

  it("saved positions for keys not on the board are ignored", async () => {
    agentMapEdgeStubFetch(async (url) => {
      if (url.includes("/api/map/positions")) {
        return agentMapEdgeJSON({ positions: { "gone-job": [10, 10], "another-gone": [-5, 900] } });
      }
      return agentMapEdgeJSON({});
    });
    const jobs = agentMapEdgeThree();
    const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
  });

  it("renders without throwing where a canvas has no 2D context", async () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
    const jobs = agentMapEdgeThree();
    const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#agent-map")).not.toBeNull();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
    for (const card of container.querySelectorAll(".map-card")) {
      expect(card.querySelector("canvas.map-actors")).not.toBeNull();
    }
  });

  it("an empty board renders an empty map", async () => {
    const { container } = render(<AgentMapEdgeMap jobs={[]} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#agent-map")).not.toBeNull();
    expect(container.querySelectorAll(".map-card")).toHaveLength(0);
  });

  it("an archived job slipped into byColumn is still never drawn", async () => {
    const live = agentMapEdgeJob({ slug: "live-one", state: "done" });
    const old = agentMapEdgeJob({ slug: "old", state: "done", archived: true });
    const byColumn = Object.fromEntries(COLUMNS.map((c) => [c, c === "done" ? [live, old] : []])) as Record<Column, WebJob[]>;
    const search: JobSearchModel = {
      query: "",
      setQuery: () => {},
      terms: [],
      byColumn,
      archived: [old],
      reasonByKey: new Map(),
      visibleKeys: new Set([jobKey(live), jobKey(old)]),
      matched: 2,
      total: 2,
      active: false,
      goalFilter: null,
    };
    const { container } = render(<MapView search={search} actions={agentMapEdgeActions()} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeCardKeys(container)).toEqual([jobKey(live)]);
  });

  it("every card carries a positive integer z-index, distinct even at equal y", async () => {
    agentMapEdgeStubFetch(async (url) => {
      if (url.includes("/api/map/positions")) {
        const positions: Record<string, [number, number]> = {};
        agentMapEdgeThree().forEach((j, i) => {
          positions[jobKey(j)] = [i * 2000, 300];
        });
        return agentMapEdgeJSON({ positions });
      }
      return agentMapEdgeJSON({});
    });
    const { container } = render(<AgentMapEdgeMap jobs={agentMapEdgeThree()} />);
    await agentMapEdgeFlush();
    const zs = [...container.querySelectorAll<HTMLElement>(".map-card")].map((c) => Number(c.style.zIndex));
    expect(zs).toHaveLength(3);
    for (const z of zs) {
      expect(Number.isInteger(z) && z > 0).toBe(true);
    }
    expect(new Set(zs).size).toBe(3);
  });
});

// --- the strip lives inside the card (amended W4) -----------------------------------

const agentMapEdgeBusy = (): WebJob =>
  agentMapEdgeJob({
    slug: "busy",
    state: "working",
    live: true,
    activity: {
      act: "edit",
      label: "Edit board.go",
      at: T0,
      events: [
        { id: "e1", t: T0 - 1000, act: "read", label: "Read x.go", dur_ms: 10 },
        { id: "e2", t: T0, act: "edit", label: "Edit board.go", open: true },
      ],
    },
  });

describe("ActivityStrip placement — edges", () => {
  it("on the map, every strip part sits inside a .card, never beside it", async () => {
    const quiet = agentMapEdgeJob({ slug: "quiet", state: "draft" });
    const { container } = render(<AgentMapEdgeMap jobs={[agentMapEdgeBusy(), quiet]} />);
    await agentMapEdgeFlush();
    const parts = [...container.querySelectorAll(".act-now, .act-ribbon")];
    expect(parts.length).toBeGreaterThan(0);
    for (const p of parts) {
      expect(p.closest(".card")).not.toBeNull();
      expect(p.closest(".map-card")).not.toBeNull();
    }
    // the job with no activity carries no strip at all
    const quietCard = container.querySelector(`.map-card[data-key="${jobKey(quiet)}"]`)!;
    expect(quietCard.querySelector(".act-now, .act-ribbon")).toBeNull();
    // and the busy card's strip is inside that card's own .card
    const busyCard = container.querySelector(`.map-card[data-key="${jobKey(agentMapEdgeBusy())}"]`)!;
    expect(busyCard.querySelector(".card .act-now")).not.toBeNull();
    expect(busyCard.querySelector(".card .act-ribbon")).not.toBeNull();
  });

  it("the column board never renders a strip, even for a job with activity", () => {
    const { container } = render(<AgentMapEdgeBoard jobs={[agentMapEdgeBusy()]} />);
    expect(container.querySelector("#board")).not.toBeNull();
    expect(container.querySelector(".act-now, .act-ribbon")).toBeNull();
  });
});

// --- cost on an ordinary frame (amended W4) ----------------------------------------

describe("MapView — no layout read on an ordinary frame", () => {
  it("idle frames after mount read no layout", async () => {
    const jobs = [agentMapEdgeBusy(), agentMapEdgeJob({ slug: "b2", state: "blocked", live: true })];
    render(<AgentMapEdgeMap jobs={jobs} />);
    // let mount, the position load, placement and the first settle finish
    await agentMapEdgeFlush();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 200));
    });
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
    const offH = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get");
    const cliW = vi.spyOn(Element.prototype, "clientWidth", "get");
    const style = vi.spyOn(window, "getComputedStyle");
    // a run of ordinary animation frames, nothing changed
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400));
    });
    expect(rect).not.toHaveBeenCalled();
    expect(offH).not.toHaveBeenCalled();
    expect(cliW).not.toHaveBeenCalled();
    expect(style).not.toHaveBeenCalled();
  });
});

// --- ActivityStrip ------------------------------------------------------------------

describe("ActivityStrip — edges", () => {
  it("renders nothing for a job with no activity", () => {
    const { container } = render(<ActivityStrip job={agentMapEdgeJob({ state: "working", live: true })} nowMs={T0} />);
    expect(container.innerHTML).toBe("");
  });

  it("hear-only activity reads think with a ribbon of hears", () => {
    const events: AgentEvent[] = [
      { id: "h1", t: T0 - 2000, act: "hear", label: "prompt from you", from: "you" },
      { id: "h2", t: T0 - 1000, act: "hear", label: "from heph-1", from: "teammate", who: "heph-1" },
    ];
    const job = agentMapEdgeJob({ state: "working", live: true, activity: { act: "think", at: T0 - 1000, events } });
    const { container } = render(<ActivityStrip job={job} nowMs={T0} />);
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ think");
    const marks = [...container.querySelectorAll(".act-ribbon i")];
    expect(marks).toHaveLength(2);
    expect(marks.map((m) => m.getAttribute("title"))).toEqual(["hear: prompt from you", "hear: from heph-1"]);
  });

  it("a blocked job's strip shows its status, and mapped names in titles", () => {
    const events: AgentEvent[] = [
      { id: "c1", t: T0 - 50_000, act: "check", label: "go vet", dur_ms: 900 },
      { id: "c2", t: T0 - 40_000, act: "home", label: "kraken mm get" },
    ];
    const job = agentMapEdgeJob({ state: "blocked", live: true, activity: { act: "home", label: "kraken mm get", at: T0 - 40_000, events } });
    const { container } = render(<ActivityStrip job={job} nowMs={T0} />);
    const now = container.querySelector(".act-now")!.textContent ?? "";
    expect(now).toContain("▸ blocked");
    expect(now).toContain("kraken mm get");
    const titles = [...container.querySelectorAll(".act-ribbon i")].map((m) => m.getAttribute("title"));
    expect(titles).toEqual(["lint: go vet", "report: kraken mm get"]);
  });

  it("an activity with no events renders think and an empty ribbon", () => {
    const job = agentMapEdgeJob({ state: "working", live: true, activity: { act: "think", at: T0, events: [] } });
    const { container } = render(<ActivityStrip job={job} nowMs={T0} />);
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ think");
    expect(container.querySelectorAll(".act-ribbon i")).toHaveLength(0);
  });
});

// --- amendment 6 ----------------------------------------------------------------

describe("ActivityStrip's clock — amendment 6", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("with no nowMs, .act-now turns to think by itself once the hold runs out", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const now = Date.now();
    // a closed call 1 s ago that took 0.5 s: held until now + 5.5 s
    const events: AgentEvent[] = [{ id: "c1", t: now - 1000, act: "edit", label: "Edit board.go", dur_ms: 500 }];
    const job = agentMapEdgeJob({ state: "working", live: true, activity: { act: "edit", label: "Edit board.go", at: now - 1000, events } });
    const { container } = render(<ActivityStrip job={job} />);
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ edit");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ edit");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ think");
  });

  it("an open call never times out on its own clock", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const now = Date.now();
    const events: AgentEvent[] = [{ id: "o1", t: now, act: "test", label: "go test", open: true }];
    const job = agentMapEdgeJob({ state: "working", live: true, activity: { act: "test", label: "go test", at: now, events } });
    const { container } = render(<ActivityStrip job={job} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * 60_000);
    });
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ test");
  });

  it("with nowMs given, it renders for exactly that instant, however long passes", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const events: AgentEvent[] = [{ id: "c1", t: T0, act: "search", label: "Grep", dur_ms: 100 }];
    const job = agentMapEdgeJob({ state: "working", live: true, activity: { act: "search", label: "Grep", at: T0, events } });
    const { container } = render(<ActivityStrip job={job} nowMs={T0 + 1000} />);
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ search");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(container.querySelector(".act-now")!.textContent).toContain("▸ search");
  });
});

describe("MapView — kraken.mapUnsaved (amendment 7)", () => {
  const UNSAVED = "kraken.mapUnsaved";

  function agentMapEdgePositionsServer(opts: { saved?: Record<string, [number, number]>; postStatus?: number }) {
    const posts: Record<string, [number, number]>[] = [];
    agentMapEdgeStubFetch(async (url, init) => {
      if (url.includes("/api/map/positions")) {
        if ((init?.method ?? "GET").toUpperCase() === "POST") {
          const body = JSON.parse(String(init?.body ?? "{}")) as { positions?: Record<string, [number, number]> };
          posts.push(body.positions ?? {});
          const status = opts.postStatus ?? 200;
          return status === 200 ? agentMapEdgeJSON({ ok: true }) : new Response("refused", { status });
        }
        return agentMapEdgeJSON({ positions: opts.saved ?? {} });
      }
      return agentMapEdgeJSON({});
    });
    return posts;
  }

  function agentMapEdgeUnsaved(): Record<string, unknown> {
    const raw = localStorage.getItem(UNSAVED);
    if (!raw) return {};
    return JSON.parse(raw) as Record<string, unknown>;
  }

  it("a refused save keeps the positions in this browser", async () => {
    const posts = agentMapEdgePositionsServer({ postStatus: 403 });
    const jobs = agentMapEdgeThree();
    render(<AgentMapEdgeMap jobs={jobs} />);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    const kept = agentMapEdgeUnsaved();
    for (const k of Object.keys(posts[0]!)) {
      const v = kept[k];
      expect(Array.isArray(v), `unsaved[${k}]`).toBe(true);
      expect(v).toEqual(posts[0]![k]);
    }
  });

  it("an accepted save leaves none of its keys behind", async () => {
    const posts = agentMapEdgePositionsServer({});
    render(<AgentMapEdgeMap jobs={agentMapEdgeThree()} />);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    const kept = agentMapEdgeUnsaved();
    for (const k of Object.keys(posts[0]!)) expect(kept).not.toHaveProperty(k);
  });

  it("a recorded position wins over the server's and is queued again", async () => {
    const jobs = agentMapEdgeThree();
    const k = jobKey(jobs[1]!);
    localStorage.setItem(UNSAVED, JSON.stringify({ [k]: [5, 7] }));
    const saved: Record<string, [number, number]> = {};
    jobs.forEach((j, i) => {
      saved[jobKey(j)] = [i * 2000, 100];
    });
    const posts = agentMapEdgePositionsServer({ saved });
    render(<AgentMapEdgeMap jobs={jobs} />);
    await waitFor(() => expect(posts.some((p) => k in p)).toBe(true));
    const carrying = posts.find((p) => k in p)!;
    expect(carrying[k]).toEqual([5, 7]);
  });

  for (const [name, raw] of [
    ["not JSON", "{not json"],
    ["a JSON array", "[1,2,3]"],
    ["a JSON string", '"hello"'],
    ["null", "null"],
  ] as const) {
    it(`unparseable storage (${name}) is treated as empty`, async () => {
      localStorage.setItem(UNSAVED, raw);
      agentMapEdgePositionsServer({});
      const jobs = agentMapEdgeThree();
      const { container } = render(<AgentMapEdgeMap jobs={jobs} />);
      await agentMapEdgeFlush();
      expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
    });
  }

  it("storage that throws on read or write never breaks the view, and saves still go out", async () => {
    const realGet = Storage.prototype.getItem;
    const realSet = Storage.prototype.setItem;
    const realRemove = Storage.prototype.removeItem;
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(function (this: Storage, key: string) {
      if (key === UNSAVED) throw new Error("storage disabled");
      return realGet.call(this, key);
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key: string, value: string) {
      if (key === UNSAVED) throw new Error("quota");
      return realSet.call(this, key, value);
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(function (this: Storage, key: string) {
      if (key === UNSAVED) throw new Error("storage disabled");
      return realRemove.call(this, key);
    });
    const posts = agentMapEdgePositionsServer({ postStatus: 403 });
    const jobs = agentMapEdgeThree();
    const { container, unmount } = render(<AgentMapEdgeMap jobs={jobs} />);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
    expect(() => unmount()).not.toThrow();
  });
});

describe("MapView — a failed save stays queued (amendment 6)", () => {
  it("the unmount flush carries what a failed save could not send", async () => {
    const posts: Record<string, [number, number]>[] = [];
    agentMapEdgeStubFetch(async (url, init) => {
      if (url.includes("/api/map/positions")) {
        if ((init?.method ?? "GET").toUpperCase() === "POST") {
          const body = JSON.parse(String(init?.body ?? "{}")) as { positions?: Record<string, [number, number]> };
          posts.push(body.positions ?? {});
          if (posts.length === 1) throw new TypeError("network down");
          return agentMapEdgeJSON({ ok: true });
        }
        return agentMapEdgeJSON({ positions: {} });
      }
      return agentMapEdgeJSON({});
    });
    const jobs = agentMapEdgeThree();
    const { unmount } = render(<AgentMapEdgeMap jobs={jobs} />);
    // the seeded cards are saved, and that first save fails
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    const failed = Object.keys(posts[0]!);
    expect(failed.length).toBeGreaterThan(0);
    unmount();
    await agentMapEdgeFlush();
    expect(posts.length).toBeGreaterThan(1);
    const later = posts.slice(1).flatMap((p) => Object.keys(p));
    for (const k of failed) expect(later).toContain(k);
  });
});

// --- Round 8: W5 zoom never moves a card, W7 the minimap -----------------------------

// Every card saved far apart, so placement needs no settle and the view has
// nothing of its own to save once it has mounted.
function agentMapEdgeR8Server(jobs: WebJob[], postStatus = 200) {
  const saved: Record<string, [number, number]> = {};
  jobs.forEach((j, i) => {
    saved[jobKey(j)] = [i * 3000, 400];
  });
  const posts: Record<string, [number, number]>[] = [];
  agentMapEdgeStubFetch(async (url, init) => {
    if (url.includes("/api/map/positions")) {
      if ((init?.method ?? "GET").toUpperCase() === "POST") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { positions?: Record<string, [number, number]> };
        posts.push(body.positions ?? {});
        return postStatus === 200 ? agentMapEdgeJSON({ ok: true }) : new Response("refused", { status: postStatus });
      }
      return agentMapEdgeJSON({ positions: saved });
    }
    return agentMapEdgeJSON({});
  });
  return posts;
}

function AgentMapEdgeR8Map({ jobs, actions }: { jobs: WebJob[]; actions: BoardActions }) {
  const search = useJobSearch(agentMapEdgeModel(jobs));
  return <MapView search={search} actions={actions} />;
}

const agentMapEdgeR8Settle = () =>
  act(async () => {
    for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 250));
  });

// The HUD's zoom buttons, found by the glyphs the contract names them by.
function agentMapEdgeR8Hud(container: HTMLElement, glyph: "+" | "−" | "fit"): HTMLButtonElement {
  const map = container.querySelector("#agent-map")!;
  const hits = [...map.querySelectorAll<HTMLButtonElement>("button")].filter((b) => (b.textContent ?? "").trim() === glyph);
  expect(hits.length, `zoom button ${glyph}`).toBeGreaterThan(0);
  return hits[0]!;
}

const agentMapEdgeR8Jobs = () => [
  agentMapEdgeJob({ slug: "r8-draft", state: "draft" }),
  agentMapEdgeJob({ slug: "r8-work", state: "working", live: true }),
  agentMapEdgeJob({ slug: "r8-block", state: "blocked", live: true }),
  agentMapEdgeJob({ slug: "r8-limit", state: "blocked", live: true, health: "usage limit" }),
];

const agentMapEdgeR8Unsaved = () => localStorage.getItem("kraken.mapUnsaved");

describe("MapView — zoom sends no save and moves no card (W5)", () => {
  it("hammering the zoom buttons to both extremes sends no position save", async () => {
    const jobs = agentMapEdgeR8Jobs();
    const posts = agentMapEdgeR8Server(jobs);
    const actions = agentMapEdgeActions();
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={actions} />);
    await agentMapEdgeR8Settle();
    const before = posts.length;
    const unsavedBefore = agentMapEdgeR8Unsaved();
    const zoomIn = agentMapEdgeR8Hud(container, "+");
    const zoomOut = agentMapEdgeR8Hud(container, "−");
    const fit = agentMapEdgeR8Hud(container, "fit");
    for (let i = 0; i < 40; i++) fireEvent.click(zoomOut);
    await agentMapEdgeR8Settle();
    for (let i = 0; i < 80; i++) fireEvent.click(zoomIn);
    await agentMapEdgeR8Settle();
    fireEvent.click(fit);
    fireEvent.click(fit);
    for (let i = 0; i < 40; i++) fireEvent.click(zoomOut);
    fireEvent.click(fit);
    await agentMapEdgeR8Settle();
    expect(posts.length).toBe(before);
    expect(agentMapEdgeR8Unsaved()).toBe(unsavedBefore);
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
    // zooming opens nothing
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("wheel and pinch-style wheel gestures, on the map and over a card, send no position save", async () => {
    const jobs = agentMapEdgeR8Jobs();
    const posts = agentMapEdgeR8Server(jobs);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    const before = posts.length;
    const unsavedBefore = agentMapEdgeR8Unsaved();
    const map = container.querySelector<HTMLElement>("#agent-map")!;
    const card = container.querySelector<HTMLElement>(".map-card .card") ?? container.querySelector<HTMLElement>(".map-card")!;
    for (const target of [map, card]) {
      for (const deltaY of [-5000, -120, -1, 1, 120, 5000]) {
        fireEvent.wheel(target, { deltaY, clientX: 50, clientY: 50 });
        fireEvent.wheel(target, { deltaY, ctrlKey: true, clientX: 50, clientY: 50 });
        fireEvent.wheel(target, { deltaY, metaKey: true, clientX: 50, clientY: 50 });
      }
    }
    await agentMapEdgeR8Settle();
    expect(posts.length).toBe(before);
    expect(agentMapEdgeR8Unsaved()).toBe(unsavedBefore);
  });

  it("zoom on a server that refuses saves records nothing unsaved in this browser", async () => {
    const jobs = agentMapEdgeR8Jobs();
    agentMapEdgeR8Server(jobs, 403);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    const unsavedBefore = agentMapEdgeR8Unsaved();
    for (let i = 0; i < 20; i++) fireEvent.click(agentMapEdgeR8Hud(container, "−"));
    fireEvent.click(agentMapEdgeR8Hud(container, "fit"));
    for (let i = 0; i < 20; i++) fireEvent.click(agentMapEdgeR8Hud(container, "+"));
    await agentMapEdgeR8Settle();
    expect(agentMapEdgeR8Unsaved()).toBe(unsavedBefore);
  });

  it("fit with a single card sends no save and does not throw", async () => {
    const jobs = [agentMapEdgeJob({ slug: "solo", state: "draft" })];
    const posts = agentMapEdgeR8Server(jobs);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    const before = posts.length;
    expect(() => {
      fireEvent.click(agentMapEdgeR8Hud(container, "fit"));
      fireEvent.click(agentMapEdgeR8Hud(container, "fit"));
    }).not.toThrow();
    await agentMapEdgeR8Settle();
    expect(posts.length).toBe(before);
  });
});

describe("MapView's #map-minimap — edges (W7)", () => {
  function agentMapEdgeR8Minimap(container: HTMLElement): HTMLCanvasElement {
    const mini = container.querySelector<HTMLCanvasElement>("#agent-map #map-minimap");
    expect(mini, "#map-minimap inside #agent-map").not.toBeNull();
    return mini!;
  }

  it("is present with a single card, as a canvas with role img and its label", async () => {
    const jobs = [agentMapEdgeJob({ slug: "only", state: "draft" })];
    agentMapEdgeR8Server(jobs);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    const mini = agentMapEdgeR8Minimap(container);
    expect(mini.tagName).toBe("CANVAS");
    expect(mini.getAttribute("role")).toBe("img");
    expect(mini.getAttribute("aria-label")).toBe("map overview");
    // it is the map's overview, not part of any card
    expect(mini.closest(".map-card")).toBeNull();
  });

  it("is present when every card had to be seeded (no saved positions)", async () => {
    // the default beforeEach server answers no saved positions
    const { container } = render(<AgentMapEdgeR8Map jobs={agentMapEdgeR8Jobs()} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    agentMapEdgeR8Minimap(container);
  });

  it("is present when the position load fails", async () => {
    agentMapEdgeStubFetch(async (url) => {
      if (url.includes("/api/map/positions")) throw new TypeError("network down");
      return agentMapEdgeJSON({});
    });
    const { container } = render(<AgentMapEdgeR8Map jobs={agentMapEdgeR8Jobs()} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    agentMapEdgeR8Minimap(container);
  });

  it("the zoom buttons stay in the map beside it", async () => {
    const jobs = agentMapEdgeR8Jobs();
    agentMapEdgeR8Server(jobs);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    agentMapEdgeR8Minimap(container);
    for (const g of ["+", "−", "fit"] as const) {
      const b = agentMapEdgeR8Hud(container, g);
      expect(b.closest("#map-minimap")).toBeNull();
    }
  });

  it("pressing and dragging on it sends no position save, opens nothing and keeps every card", async () => {
    const jobs = agentMapEdgeR8Jobs();
    const posts = agentMapEdgeR8Server(jobs);
    const actions = agentMapEdgeActions();
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={actions} />);
    await agentMapEdgeR8Settle();
    const before = posts.length;
    const unsavedBefore = agentMapEdgeR8Unsaved();
    const statusBefore = [...container.querySelectorAll(".map-card")].map((c) => `${c.getAttribute("data-key")}=${c.getAttribute("data-status")}`).sort();
    const mini = agentMapEdgeR8Minimap(container);
    // corners, the middle, and points well outside the canvas
    for (const [x, y] of [[0, 0], [5, 5], [60, 40], [-500, -500], [10_000, 10_000]] as const) {
      fireEvent.pointerDown(mini, { clientX: x, clientY: y, button: 0, buttons: 1, pointerId: 7 });
      fireEvent.pointerMove(mini, { clientX: x + 30, clientY: y + 20, buttons: 1, pointerId: 7 });
      fireEvent.pointerMove(mini, { clientX: x - 80, clientY: y + 90, buttons: 1, pointerId: 7 });
      fireEvent.pointerUp(mini, { clientX: x - 80, clientY: y + 90, button: 0, pointerId: 7 });
      fireEvent.mouseDown(mini, { clientX: x, clientY: y, button: 0, buttons: 1 });
      fireEvent.mouseMove(mini, { clientX: x + 30, clientY: y + 20, buttons: 1 });
      fireEvent.mouseUp(mini, { clientX: x + 30, clientY: y + 20, button: 0 });
      fireEvent.click(mini, { clientX: x + 30, clientY: y + 20 });
    }
    await agentMapEdgeR8Settle();
    expect(posts.length).toBe(before);
    expect(agentMapEdgeR8Unsaved()).toBe(unsavedBefore);
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
    const statusAfter = [...container.querySelectorAll(".map-card")].map((c) => `${c.getAttribute("data-key")}=${c.getAttribute("data-status")}`).sort();
    expect(statusAfter).toEqual(statusBefore);
  });

  it("renders, and takes a press, without throwing where a canvas has no 2D context", async () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
    const jobs = agentMapEdgeR8Jobs();
    const posts = agentMapEdgeR8Server(jobs);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    const mini = agentMapEdgeR8Minimap(container);
    expect(mini.getAttribute("role")).toBe("img");
    expect(mini.getAttribute("aria-label")).toBe("map overview");
    const before = posts.length;
    expect(() => {
      fireEvent.pointerDown(mini, { clientX: 20, clientY: 20, button: 0, buttons: 1, pointerId: 1 });
      fireEvent.pointerMove(mini, { clientX: 40, clientY: 30, buttons: 1, pointerId: 1 });
      fireEvent.pointerUp(mini, { clientX: 40, clientY: 30, button: 0, pointerId: 1 });
      fireEvent.mouseDown(mini, { clientX: 20, clientY: 20, button: 0, buttons: 1 });
      fireEvent.mouseUp(mini, { clientX: 20, clientY: 20, button: 0 });
    }).not.toThrow();
    for (let i = 0; i < 5; i++) fireEvent.click(agentMapEdgeR8Hud(container, "−"));
    await agentMapEdgeR8Settle();
    expect(posts.length).toBe(before);
    expect(agentMapEdgeCardKeys(container)).toEqual(jobs.map(jobKey).sort());
  });

  it("stays after zooming far out and back", async () => {
    const jobs = agentMapEdgeR8Jobs();
    agentMapEdgeR8Server(jobs);
    const { container } = render(<AgentMapEdgeR8Map jobs={jobs} actions={agentMapEdgeActions()} />);
    await agentMapEdgeR8Settle();
    for (let i = 0; i < 40; i++) fireEvent.click(agentMapEdgeR8Hud(container, "−"));
    await agentMapEdgeR8Settle();
    agentMapEdgeR8Minimap(container);
    for (let i = 0; i < 80; i++) fireEvent.click(agentMapEdgeR8Hud(container, "+"));
    await agentMapEdgeR8Settle();
    agentMapEdgeR8Minimap(container);
  });
});

// --- Round 9 (50-contract.md W12, W13) ------------------------------------------
// The selection's refusals and boundaries: what never moves, opens or saves; a
// marquee that replaces rather than adds; a card that leaves the map; and an old
// gap layout that stays at rest. A saved view of {x:0,y:0,z:1} makes a client
// point a world point here (jsdom measures every element at 0,0).

type AgentMapEdgeR9Pos = Record<string, [number, number]>;

function agentMapEdgeR9Server(saved: AgentMapEdgeR9Pos): AgentMapEdgeR9Pos[] {
  const posts: AgentMapEdgeR9Pos[] = [];
  agentMapEdgeStubFetch(async (url, init) => {
    if (url.includes("/api/map/positions")) {
      if ((init?.method ?? "GET").toUpperCase() === "POST") {
        posts.push((JSON.parse(String(init!.body)) as { positions: AgentMapEdgeR9Pos }).positions);
        return agentMapEdgeJSON({ ok: true });
      }
      return agentMapEdgeJSON({ positions: saved });
    }
    return agentMapEdgeJSON({});
  });
  return posts;
}

function AgentMapEdgeR9Map({ jobs, actions }: { jobs: WebJob[]; actions: BoardActions }) {
  const search = useJobSearch(agentMapEdgeModel(jobs));
  return <MapView search={search} actions={actions} />;
}

const agentMapEdgeR9Drafts = (...slugs: string[]) => slugs.map((slug) => agentMapEdgeJob({ slug, state: "draft" }));

/** a and b in one column, c far to the right: no pair is near another. */
const agentMapEdgeR9Saved: AgentMapEdgeR9Pos = { a: [0, 0], b: [0, 600], c: [3000, 0] };

const agentMapEdgeR9Card = (c: HTMLElement, key: string) =>
  c.querySelector(`.map-card[data-key="${key}"]`) as HTMLElement;
const agentMapEdgeR9Face = (c: HTMLElement, key: string) =>
  agentMapEdgeR9Card(c, key).querySelector(".card") as HTMLElement;
const agentMapEdgeR9Root = (c: HTMLElement) => c.querySelector("#agent-map") as HTMLElement;
const agentMapEdgeR9World = (c: HTMLElement) => (c.querySelector(".map-world") as HTMLElement | null)?.style.transform;

function agentMapEdgeR9Spots(c: HTMLElement): Record<string, string> {
  const out: Record<string, string> = {};
  for (const el of c.querySelectorAll<HTMLElement>(".map-card")) out[el.dataset.key!] = `${el.style.left},${el.style.top}`;
  return out;
}

function agentMapEdgeR9IsSelected(c: HTMLElement, key: string): boolean {
  const el = agentMapEdgeR9Card(c, key);
  const cls = el.classList.contains("selected");
  const attr = el.getAttribute("data-selected");
  // a selected card carries both marks, an unselected one carries neither
  if (cls) expect(attr, `${key} has the class but not data-selected="true"`).toBe("true");
  else expect(attr, `${key} is unselected but carries data-selected`).toBeNull();
  return cls;
}

function agentMapEdgeR9Selected(c: HTMLElement): string[] {
  return [...c.querySelectorAll<HTMLElement>(".map-card")]
    .map((el) => el.dataset.key!)
    .filter((k) => agentMapEdgeR9IsSelected(c, k))
    .sort();
}

function agentMapEdgeR9ShiftClick(c: HTMLElement, key: string) {
  const face = agentMapEdgeR9Face(c, key);
  fireEvent.mouseDown(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
  fireEvent.mouseUp(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
  fireEvent.click(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
}

/** A press at `from`, a move through the midpoint to `to`, a release at `to`; Shift held throughout or not at all. */
function agentMapEdgeR9Drag(
  el: HTMLElement,
  from: [number, number],
  to: [number, number],
  shiftKey: boolean,
  mid?: () => void,
) {
  fireEvent.mouseDown(el, { clientX: from[0], clientY: from[1], button: 0, shiftKey });
  fireEvent.mouseMove(window, { clientX: (from[0] + to[0]) / 2, clientY: (from[1] + to[1]) / 2, buttons: 1, shiftKey });
  fireEvent.mouseMove(window, { clientX: to[0], clientY: to[1], buttons: 1, shiftKey });
  mid?.();
  fireEvent.mouseUp(window, { clientX: to[0], clientY: to[1], button: 0, shiftKey });
}

async function agentMapEdgeR9Mount(saved: AgentMapEdgeR9Pos = agentMapEdgeR9Saved, jobs = agentMapEdgeR9Drafts("a", "b", "c")) {
  localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
  const posts = agentMapEdgeR9Server(saved);
  const actions = agentMapEdgeActions();
  const view = render(<AgentMapEdgeR9Map jobs={jobs} actions={actions} />);
  const first = Object.keys(saved)[0]!;
  await waitFor(() =>
    expect(agentMapEdgeR9Card(view.container, first).style.left).toBe(`${saved[first]![0]}px`),
  );
  await agentMapEdgeFlush();
  return { ...view, posts, actions, jobs };
}

const agentMapEdgeR9Quiet = () => act(async () => {
  await new Promise((r) => setTimeout(r, 250));
});

describe("W12 — a layout saved under the old gap", () => {
  it("opening the map moves no card and sends no save", async () => {
    // a row spaced 300 + 36 apart and two rows 600 apart: at rest under the old gap
    const saved: AgentMapEdgeR9Pos = { a: [0, 0], b: [336, 0], c: [672, 0], d: [0, 600], e: [336, 600] };
    const { container, posts } = await agentMapEdgeR9Mount(saved, agentMapEdgeR9Drafts("a", "b", "c", "d", "e"));
    await agentMapEdgeR9Quiet();
    const want = Object.fromEntries(Object.entries(saved).map(([k, [x, y]]) => [k, `${x}px,${y}px`]));
    expect(agentMapEdgeR9Spots(container)).toEqual(want);
    expect(posts).toEqual([]);
  });
});

describe("W13 — the selection at mount", () => {
  it("is empty: no card carries the class or the attribute, and no marquee shows", async () => {
    const { container } = await agentMapEdgeR9Mount();
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
    expect(container.querySelector("#map-marquee")).toBeNull();
  });

  it("is not restored from anything saved: a remount starts empty again", async () => {
    const first = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(first.container, "a");
    expect(agentMapEdgeR9Selected(first.container)).toEqual(["a"]);
    first.unmount();
    const again = await agentMapEdgeR9Mount();
    expect(agentMapEdgeR9Selected(again.container)).toEqual([]);
  });
});

describe("W13 — shift-click", () => {
  it("toggles a card in and out, opens nothing, moves nothing and saves nothing", async () => {
    const { container, posts, actions } = await agentMapEdgeR9Mount();
    const spots = agentMapEdgeR9Spots(container);
    agentMapEdgeR9ShiftClick(container, "a");
    expect(agentMapEdgeR9Selected(container)).toEqual(["a"]);
    agentMapEdgeR9ShiftClick(container, "c");
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "c"]);
    agentMapEdgeR9ShiftClick(container, "a");
    expect(agentMapEdgeR9Selected(container)).toEqual(["c"]);
    agentMapEdgeR9ShiftClick(container, "c");
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
    await agentMapEdgeR9Quiet();
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
    expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    expect(posts).toEqual([]);
  });

  it("a press with Shift but no drag shows no marquee", async () => {
    const { container } = await agentMapEdgeR9Mount();
    fireEvent.mouseDown(agentMapEdgeR9Face(container, "a"), { clientX: 100, clientY: 100, button: 0, shiftKey: true });
    expect(container.querySelector("#map-marquee")).toBeNull();
    fireEvent.mouseUp(agentMapEdgeR9Face(container, "a"), { clientX: 100, clientY: 100, button: 0, shiftKey: true });
    expect(container.querySelector("#map-marquee")).toBeNull();
  });
});

describe("W13 — shift-drag", () => {
  it("on the background: a marquee only while in flight, no pan, no move, no save", async () => {
    const { container, posts, actions } = await agentMapEdgeR9Mount();
    const spots = agentMapEdgeR9Spots(container);
    const world = agentMapEdgeR9World(container);
    let during: Element | null = null;
    agentMapEdgeR9Drag(agentMapEdgeR9Root(container), [-20, -20], [350, 700], true, () => {
      // inside #agent-map itself, as the contract places it
      during = agentMapEdgeR9Root(container).querySelector("#map-marquee");
      expect(agentMapEdgeR9World(container)).toBe(world);
      expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    });
    expect(during).not.toBeNull();
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "b"]);
    await agentMapEdgeR9Quiet();
    expect(agentMapEdgeR9World(container)).toBe(world);
    expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    expect(posts).toEqual([]);
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("starting on a card: the card does not move or open, and a marquee is drawn", async () => {
    const { container, posts, actions } = await agentMapEdgeR9Mount();
    const spots = agentMapEdgeR9Spots(container);
    const world = agentMapEdgeR9World(container);
    let during: Element | null = null;
    // from inside a's box down to b's top edge region; c is far to the right
    agentMapEdgeR9Drag(agentMapEdgeR9Face(container, "a"), [150, 10], [160, 650], true, () => {
      during = agentMapEdgeR9Root(container).querySelector("#map-marquee");
      expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    });
    expect(during).not.toBeNull();
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "b"]);
    await agentMapEdgeR9Quiet();
    expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    expect(agentMapEdgeR9World(container)).toBe(world);
    expect(posts).toEqual([]);
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("replaces the selection rather than adding to it", async () => {
    const { container } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "c");
    expect(agentMapEdgeR9Selected(container)).toEqual(["c"]);
    // a rectangle over a alone, well clear of b (y 600) and c (x 3000)
    agentMapEdgeR9Drag(agentMapEdgeR9Root(container), [-40, -40], [50, 20], true);
    expect(agentMapEdgeR9Selected(container)).toEqual(["a"]);
  });

  it("over empty world empties the selection", async () => {
    const { container } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    // between the column (x 0..300) and c (x 3000), above nothing
    agentMapEdgeR9Drag(agentMapEdgeR9Root(container), [1000, -500], [2000, -300], true);
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
  });

  it("released anywhere, the drag ends: the next plain move changes nothing", async () => {
    const { container } = await agentMapEdgeR9Mount();
    const world = agentMapEdgeR9World(container);
    agentMapEdgeR9Drag(agentMapEdgeR9Root(container), [-20, -20], [350, 700], true);
    const spots = agentMapEdgeR9Spots(container);
    fireEvent.mouseMove(window, { clientX: 900, clientY: 900 });
    expect(container.querySelector("#map-marquee")).toBeNull();
    expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    expect(agentMapEdgeR9World(container)).toBe(world);
  });
});

describe("W13 — group move", () => {
  it("moves every selected card by the drag's delta in flight, keeps the offset and the selection on drop, and saves once", async () => {
    const { container, posts, actions } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    await agentMapEdgeR9Quiet();
    expect(posts).toEqual([]);
    agentMapEdgeR9Drag(agentMapEdgeR9Face(container, "a"), [100, 100], [140, 130], false, () => {
      expect(agentMapEdgeR9Card(container, "a").style.left).toBe("40px");
      expect(agentMapEdgeR9Card(container, "a").style.top).toBe("30px");
      expect(agentMapEdgeR9Card(container, "b").style.left).toBe("40px");
      expect(agentMapEdgeR9Card(container, "b").style.top).toBe("630px");
      expect(agentMapEdgeR9Card(container, "c").style.left).toBe("3000px");
    });
    expect(agentMapEdgeR9Spots(container)).toEqual({ a: "40px,30px", b: "40px,630px", c: "3000px,0px" });
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "b"]);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeR9Quiet();
    expect(posts).toHaveLength(1);
    expect(posts[0]).toEqual({ a: [40, 30], b: [40, 630] });
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("dragging the group onto an unselected card: the group keeps its offset, the other card yields, one save carries all three", async () => {
    // c sits just right of the column; moving the column 100 right lands a on it
    const saved: AgentMapEdgeR9Pos = { a: [0, 0], b: [0, 600], c: [400, 0] };
    const { container, posts } = await agentMapEdgeR9Mount(saved);
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    agentMapEdgeR9Drag(agentMapEdgeR9Face(container, "b"), [100, 100], [200, 100], false);
    expect(agentMapEdgeR9Card(container, "a").style.left).toBe("100px");
    expect(agentMapEdgeR9Card(container, "a").style.top).toBe("0px");
    expect(agentMapEdgeR9Card(container, "b").style.left).toBe("100px");
    expect(agentMapEdgeR9Card(container, "b").style.top).toBe("600px");
    expect(agentMapEdgeR9Spots(container).c).not.toBe("400px,0px");
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "b"]);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeR9Quiet();
    expect(posts).toHaveLength(1);
    expect(Object.keys(posts[0]!).sort()).toEqual(["a", "b", "c"]);
    expect(posts[0]!.a).toEqual([100, 0]);
    expect(posts[0]!.b).toEqual([100, 600]);
    const c = agentMapEdgeR9Card(container, "c");
    expect(posts[0]!.c).toEqual([parseFloat(c.style.left), parseFloat(c.style.top)]);
  });

  it("a one-card selection moves like a group of one and stays selected", async () => {
    const { container, posts } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "c");
    agentMapEdgeR9Drag(agentMapEdgeR9Face(container, "c"), [100, 100], [60, 160], false);
    expect(agentMapEdgeR9Spots(container)).toEqual({ a: "0px,0px", b: "0px,600px", c: "2960px,60px" });
    expect(agentMapEdgeR9Selected(container)).toEqual(["c"]);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeR9Quiet();
    expect(posts).toEqual([{ c: [2960, 60] }]);
  });
});

describe("W13 — a plain drag of an unselected card", () => {
  it("moves that card alone and empties the selection", async () => {
    const { container, posts } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    agentMapEdgeR9Drag(agentMapEdgeR9Face(container, "c"), [100, 100], [150, 100], false);
    expect(agentMapEdgeR9Spots(container)).toEqual({ a: "0px,0px", b: "0px,600px", c: "3050px,0px" });
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeR9Quiet();
    for (const p of posts) {
      expect(p.a, "a was not moved and is not saved").toBeUndefined();
      expect(p.b, "b was not moved and is not saved").toBeUndefined();
    }
    expect(posts.find((p) => p.c)?.c).toEqual([3050, 0]);
  });
});

describe("W13 — clearing", () => {
  it("Escape empties the selection, and is harmless with nothing selected", async () => {
    const { container, posts } = await agentMapEdgeR9Mount();
    expect(() => fireEvent.keyDown(agentMapEdgeR9Root(container), { key: "Escape" })).not.toThrow();
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "c");
    fireEvent.keyDown(agentMapEdgeR9Root(container), { key: "Escape" });
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
    await agentMapEdgeR9Quiet();
    expect(posts).toEqual([]);
  });

  it("a plain press and release on the background empties it", async () => {
    const { container, posts } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    const root = agentMapEdgeR9Root(container);
    fireEvent.mouseDown(root, { clientX: 1500, clientY: 1500, button: 0 });
    fireEvent.mouseUp(root, { clientX: 1500, clientY: 1500, button: 0 });
    fireEvent.click(root, { clientX: 1500, clientY: 1500, button: 0 });
    expect(agentMapEdgeR9Selected(container)).toEqual([]);
    await agentMapEdgeR9Quiet();
    expect(posts).toEqual([]);
  });

  it("a background drag without Shift pans and keeps the selection", async () => {
    const { container, posts } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    const spots = agentMapEdgeR9Spots(container);
    const world = agentMapEdgeR9World(container);
    agentMapEdgeR9Drag(agentMapEdgeR9Root(container), [1500, 1500], [1700, 1600], false);
    expect(agentMapEdgeR9World(container)).not.toBe(world);
    expect(agentMapEdgeR9Spots(container)).toEqual(spots);
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "b"]);
    expect(container.querySelector("#map-marquee")).toBeNull();
    await agentMapEdgeR9Quiet();
    expect(posts).toEqual([]);
  });
});

describe("W13 — a card that leaves the map leaves the selection", () => {
  it("an archived card is dropped from the selection and comes back unselected", async () => {
    const { container, rerender, actions } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    expect(agentMapEdgeR9Selected(container)).toEqual(["a", "b"]);
    const gone = [
      agentMapEdgeJob({ slug: "a", state: "draft" }),
      agentMapEdgeJob({ slug: "b", state: "done", archived: true }),
      agentMapEdgeJob({ slug: "c", state: "draft" }),
    ];
    rerender(<AgentMapEdgeR9Map jobs={gone} actions={actions} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeR9Card(container, "b")).toBeNull();
    expect(agentMapEdgeR9Selected(container)).toEqual(["a"]);
    rerender(<AgentMapEdgeR9Map jobs={agentMapEdgeR9Drafts("a", "b", "c")} actions={actions} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeR9Card(container, "b")).not.toBeNull();
    expect(agentMapEdgeR9Selected(container)).toEqual(["a"]);
  });

  it("a group move after a selected card left moves only the cards still showing", async () => {
    const { container, rerender, actions, posts } = await agentMapEdgeR9Mount();
    agentMapEdgeR9ShiftClick(container, "a");
    agentMapEdgeR9ShiftClick(container, "b");
    const gone = [
      agentMapEdgeJob({ slug: "a", state: "draft" }),
      agentMapEdgeJob({ slug: "b", state: "done", archived: true }),
      agentMapEdgeJob({ slug: "c", state: "draft" }),
    ];
    rerender(<AgentMapEdgeR9Map jobs={gone} actions={actions} />);
    await agentMapEdgeFlush();
    agentMapEdgeR9Drag(agentMapEdgeR9Face(container, "a"), [100, 100], [120, 100], false);
    expect(agentMapEdgeR9Spots(container)).toEqual({ a: "20px,0px", c: "3000px,0px" });
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await agentMapEdgeR9Quiet();
    for (const p of posts) expect(p.b, "b is off the map and is not moved").toBeUndefined();
    // b comes back exactly where it was saved
    rerender(<AgentMapEdgeR9Map jobs={agentMapEdgeR9Drafts("a", "b", "c")} actions={actions} />);
    await agentMapEdgeFlush();
    expect(agentMapEdgeR9Card(container, "b").style.left).toBe("0px");
    expect(agentMapEdgeR9Card(container, "b").style.top).toBe("600px");
  });
});
