import { act, render, waitFor } from "@testing-library/react";
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
