import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { DRAW_UNSAVED_KEY, readUnsavedDrawOps, type Drawing } from "../../lib/agentmap/drawings.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// mapdraw.contract.test.tsx — athena-logic's blind tests for the map's
// drawing layer (50-contract.md B3.1–B3.23): the toolbar, the layer's
// elements, drawing each kind, text, erase and saving. jsdom has no
// PointerEvent, so every gesture is a mouseDown on the element followed by
// mouseMove/mouseUp on window, and the map's page offset is (0, 0).

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

const mapDrawContractClear = () => {
  localStorage.removeItem("kraken.mapView");
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapDrawUnsaved");
};

beforeEach(() => {
  mapDrawContractClear();
});

afterEach(() => {
  vi.restoreAllMocks();
  mapDrawContractClear();
});

const mapDrawContractActions = (): BoardActions => ({
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

function mapDrawContractModel(jobs: WebJob[]): BoardModel {
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board: makeBoard({ daemon: makeDaemon({ pid: 1, last_tick: Math.floor(Date.now() / 1000) }), jobs }),
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

function MapDrawContractHarness({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <MapView search={search} actions={actions} />;
}

interface MapDrawContractOps {
  set: Record<string, Drawing>;
  remove: string[];
}

interface MapDrawContractNet {
  gets: number;
  posts: MapDrawContractOps[];
  failSaves: boolean;
  answer: (drawings: unknown) => void;
}

interface MapDrawContractOpts {
  view?: { x: number; y: number; z: number };
  drawings?: unknown;
  deferred?: boolean;
  loadFails?: boolean;
  jobs?: WebJob[];
  positions?: Record<string, [number, number]>;
}

const mapDrawContractJSON = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** Routes the map's calls by path: drawings and positions; anything else answers {}. */
function mapDrawContractNet(opts: MapDrawContractOpts): MapDrawContractNet {
  let resolveLoad!: (r: Response) => void;
  const pending = new Promise<Response>((r) => (resolveLoad = r));
  const net: MapDrawContractNet = {
    gets: 0,
    posts: [],
    failSaves: false,
    answer: (drawings) => resolveLoad(mapDrawContractJSON({ drawings })),
  };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://board.local");
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.pathname === "/api/map/drawings") {
      if (method === "POST") {
        net.posts.push(JSON.parse(String(init?.body)) as MapDrawContractOps);
        return net.failSaves ? mapDrawContractJSON({ error: "nope" }, 500) : mapDrawContractJSON({ ok: true });
      }
      net.gets++;
      if (opts.loadFails) return mapDrawContractJSON({ error: "down" }, 500);
      if (opts.deferred) return pending;
      return mapDrawContractJSON({ drawings: opts.drawings ?? {} });
    }
    if (url.pathname === "/api/map/positions") {
      if (method === "POST") return mapDrawContractJSON({ ok: true });
      return mapDrawContractJSON({ positions: opts.positions ?? {} });
    }
    return mapDrawContractJSON({});
  });
  return net;
}

const mapDrawContractSleep = (ms: number) =>
  act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });

/** Renders the map on a pinned camera and waits until the drawings load has been asked for. */
async function mapDrawContractOpen(opts: MapDrawContractOpts = {}) {
  localStorage.setItem("kraken.mapView", JSON.stringify(opts.view ?? { x: 0, y: 0, z: 1 }));
  const net = mapDrawContractNet(opts);
  const actions = mapDrawContractActions();
  const board = mapDrawContractModel(opts.jobs ?? []);
  const utils = render(<MapDrawContractHarness board={board} actions={actions} />);
  await waitFor(() => expect(net.gets).toBe(1));
  await mapDrawContractSleep(30);
  const remount = () => render(<MapDrawContractHarness board={board} actions={actions} />);
  return { ...utils, net, actions, remount };
}

const mapDrawContractRoot = (c: ParentNode) => c.querySelector<HTMLElement>("#agent-map")!;
const mapDrawContractSurface = (c: ParentNode) => c.querySelector<HTMLElement>("#map-draw-surface");
const mapDrawContractLayer = (c: ParentNode) => c.querySelector<SVGSVGElement>("svg#map-drawings");
const mapDrawContractDrawn = (c: ParentNode) => [...(mapDrawContractLayer(c)?.querySelectorAll<SVGElement>("[data-draw-id]") ?? [])];
const mapDrawContractIds = (c: ParentNode) => mapDrawContractDrawn(c).map((e) => e.getAttribute("data-draw-id"));
const mapDrawContractDrafts = (c: ParentNode) => [...(mapDrawContractLayer(c)?.querySelectorAll<SVGElement>('[data-draft="true"]') ?? [])];
const mapDrawContractTool = (c: ParentNode, t: string) =>
  c.querySelector<HTMLButtonElement>(`#map-tools button.map-tool[data-tool="${t}"]`)!;
const mapDrawContractArm = (c: ParentNode, t: string) => fireEvent.click(mapDrawContractTool(c, t));
const mapDrawContractPressed = (c: ParentNode) =>
  [...c.querySelectorAll<HTMLButtonElement>("#map-tools button.map-tool")]
    .filter((b) => b.getAttribute("aria-pressed") === "true")
    .map((b) => b.getAttribute("data-tool"));
const mapDrawContractEditor = (c: ParentNode) => c.querySelector<HTMLTextAreaElement>("textarea#map-text-input");
const mapDrawContractEscape = (c: ParentNode) => fireEvent.keyDown(mapDrawContractRoot(c), { key: "Escape", code: "Escape" });

function mapDrawContractDown(el: Element, x: number, y: number, button = 0) {
  fireEvent.mouseDown(el, { clientX: x, clientY: y, button, buttons: button === 0 ? 1 : 2 });
}
function mapDrawContractMove(x: number, y: number) {
  fireEvent.mouseMove(window, { clientX: x, clientY: y, buttons: 1 });
}
function mapDrawContractUp(x: number, y: number, button = 0) {
  fireEvent.mouseUp(window, { clientX: x, clientY: y, button });
}

/** A press on the surface at the first point, a move through every later one, a release at the last. */
function mapDrawContractStroke(c: ParentNode, pts: [number, number][]) {
  const surface = mapDrawContractSurface(c)!;
  const [x0, y0] = pts[0]!;
  mapDrawContractDown(surface, x0, y0);
  for (const [x, y] of pts.slice(1)) mapDrawContractMove(x, y);
  const [xn, yn] = pts.at(-1)!;
  mapDrawContractUp(xn, yn);
}

/** The one drawing the first POST set, by id. */
async function mapDrawContractFirstSaved(net: MapDrawContractNet): Promise<[string, Drawing]> {
  await waitFor(() => expect(net.posts.length).toBe(1));
  const entries = Object.entries(net.posts[0]!.set);
  expect(entries).toHaveLength(1);
  return entries[0]!;
}

const mapDrawContractCard = (c: ParentNode, key: string) => c.querySelector<HTMLElement>(`.map-card[data-key="${key}"]`)!;
const mapDrawContractFace = (c: ParentNode, key: string) => mapDrawContractCard(c, key).querySelector<HTMLElement>(".card")!;
const mapDrawContractSelected = (c: ParentNode) =>
  [...c.querySelectorAll<HTMLElement>(".map-card")]
    .filter((e) => e.classList.contains("selected"))
    .map((e) => e.getAttribute("data-key"))
    .sort();
const mapDrawContractWorld = (c: ParentNode) => c.querySelector<HTMLElement>(".map-world")?.style.transform;

const mapDrawContractCardJobs = () => [makeJob({ slug: "a", state: "draft" }), makeJob({ slug: "b", state: "draft" })];

/** Opens the map with cards a and b placed, and waits for their placement. */
async function mapDrawContractOpenWithCards(opts: MapDrawContractOpts = {}) {
  const opened = await mapDrawContractOpen({ jobs: mapDrawContractCardJobs(), positions: { a: [100, 100], b: [600, 100] }, ...opts });
  await waitFor(() => expect(mapDrawContractCard(opened.container, "b").style.top).toBe("100px"));
  await mapDrawContractSleep(300);
  return opened;
}

const mapDrawContractLine = (x1: number, y1: number, x2: number, y2: number, size = 2): Drawing => ({
  kind: "line",
  pts: [x1, y1, x2, y2],
  size,
});

// ---------------------------------------------------------------------------
// Toolbar (B3.1–B3.5)
// ---------------------------------------------------------------------------

describe("map drawing toolbar", () => {
  it("B3.1: .map-corner holds #map-tools with one button per tool, in order, with its label", async () => {
    const { container } = await mapDrawContractOpen();
    const tools = container.querySelector(".map-corner #map-tools");
    expect(tools).not.toBeNull();
    const buttons = [...tools!.querySelectorAll<HTMLButtonElement>("button.map-tool")];
    expect(buttons.map((b) => b.getAttribute("data-tool"))).toEqual(["pen", "line", "rect", "ellipse", "text", "erase"]);
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(["pen", "line", "rect", "oval", "text", "erase"]);
  });

  it("B3.1: renders with cards on the map too", async () => {
    const { container } = await mapDrawContractOpenWithCards();
    expect(container.querySelectorAll(".map-corner #map-tools button.map-tool")).toHaveLength(6);
  });

  it("B3.2: no tool is armed at mount", async () => {
    const { container } = await mapDrawContractOpen();
    const buttons = [...container.querySelectorAll<HTMLButtonElement>("#map-tools button.map-tool")];
    expect(buttons.map((b) => b.getAttribute("aria-pressed"))).toEqual(Array(6).fill("false"));
  });

  it("B3.2: a click arms, a second click disarms, another tool switches", async () => {
    const { container } = await mapDrawContractOpen();
    mapDrawContractArm(container, "rect");
    expect(mapDrawContractPressed(container)).toEqual(["rect"]);
    expect(mapDrawContractTool(container, "pen").getAttribute("aria-pressed")).toBe("false");
    mapDrawContractArm(container, "rect");
    expect(mapDrawContractPressed(container)).toEqual([]);
    expect(mapDrawContractTool(container, "rect").getAttribute("aria-pressed")).toBe("false");
    mapDrawContractArm(container, "pen");
    mapDrawContractArm(container, "erase");
    expect(mapDrawContractPressed(container)).toEqual(["erase"]);
  });

  it("B3.2: the armed tool is not saved across a remount", async () => {
    const { container, unmount, remount } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    expect(mapDrawContractPressed(container)).toEqual(["text"]);
    unmount();
    const again = remount();
    await mapDrawContractSleep(50);
    expect(mapDrawContractPressed(again.container)).toEqual([]);
    expect(mapDrawContractRoot(again.container).hasAttribute("data-tool")).toBe(false);
  });

  it("B3.3: #agent-map carries data-tool and holds #map-draw-surface only while a tool is armed", async () => {
    const { container } = await mapDrawContractOpen();
    const root = mapDrawContractRoot(container);
    expect(root.hasAttribute("data-tool")).toBe(false);
    expect(mapDrawContractSurface(container)).toBeNull();
    mapDrawContractArm(container, "ellipse");
    expect(root.getAttribute("data-tool")).toBe("ellipse");
    expect(root.querySelector("#map-draw-surface")).not.toBeNull();
    mapDrawContractArm(container, "erase");
    expect(root.getAttribute("data-tool")).toBe("erase");
    mapDrawContractArm(container, "erase");
    expect(root.hasAttribute("data-tool")).toBe(false);
    expect(mapDrawContractSurface(container)).toBeNull();
  });

  it("B3.4: a press with a non-primary button is ignored", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "rect");
    mapDrawContractDown(mapDrawContractSurface(container)!, 100, 100, 2);
    mapDrawContractMove(300, 300);
    expect(mapDrawContractDrafts(container)).toHaveLength(0);
    mapDrawContractUp(300, 300, 2);
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.4: a press on the surface over a card moves no card, pans nothing, selects and opens nothing", async () => {
    const { container, actions } = await mapDrawContractOpenWithCards();
    const world = mapDrawContractWorld(container);
    mapDrawContractArm(container, "rect");
    mapDrawContractStroke(container, [
      [150, 120],
      [220, 180],
      [300, 250],
    ]);
    const surface = mapDrawContractSurface(container)!;
    fireEvent.click(surface, { clientX: 300, clientY: 250, button: 0 });
    expect(mapDrawContractCard(container, "a").style.left).toBe("100px");
    expect(mapDrawContractCard(container, "a").style.top).toBe("100px");
    expect(mapDrawContractWorld(container)).toBe(world);
    expect(mapDrawContractSelected(container)).toEqual([]);
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("B3.4: a press on the surface leaves an existing selection as it was", async () => {
    const { container } = await mapDrawContractOpenWithCards();
    const face = mapDrawContractFace(container, "a");
    fireEvent.mouseDown(face, { clientX: 150, clientY: 120, button: 0, buttons: 1, shiftKey: true });
    fireEvent.mouseUp(face, { clientX: 150, clientY: 120, button: 0, shiftKey: true });
    fireEvent.click(face, { clientX: 150, clientY: 120, button: 0, shiftKey: true });
    expect(mapDrawContractSelected(container)).toEqual(["a"]);
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [2000, 1500],
      [2100, 1600],
    ]);
    expect(mapDrawContractSelected(container)).toEqual(["a"]);
  });

  it("B3.5: Escape with a tool armed disarms it and keeps the selection; the next Escape empties it", async () => {
    const { container } = await mapDrawContractOpenWithCards();
    const face = mapDrawContractFace(container, "a");
    fireEvent.mouseDown(face, { clientX: 150, clientY: 120, button: 0, buttons: 1, shiftKey: true });
    fireEvent.mouseUp(face, { clientX: 150, clientY: 120, button: 0, shiftKey: true });
    fireEvent.click(face, { clientX: 150, clientY: 120, button: 0, shiftKey: true });
    expect(mapDrawContractSelected(container)).toEqual(["a"]);
    mapDrawContractArm(container, "pen");
    mapDrawContractEscape(container);
    expect(mapDrawContractPressed(container)).toEqual([]);
    expect(mapDrawContractRoot(container).hasAttribute("data-tool")).toBe(false);
    expect(mapDrawContractSelected(container)).toEqual(["a"]);
    mapDrawContractEscape(container);
    expect(mapDrawContractSelected(container)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// The layer (B3.6)
// ---------------------------------------------------------------------------

const mapDrawContractEveryKind = (): Record<string, Drawing> => ({
  // deliberately not in id order
  e: { kind: "text", pts: [5, 6], size: 18, text: "one\ntwo" },
  b: mapDrawContractLine(1, 2, 3, 4, 2),
  d: { kind: "ellipse", pts: [0, 0, 200, 100], size: 4 },
  a: { kind: "pen", pts: [0, 0, 10, 5, 20, 0], size: 3 },
  c: { kind: "rect", pts: [50, 60, 10, 20], size: 1.5 },
});

const mapDrawContractNum = (el: Element, attr: string) => Number(el.getAttribute(attr));

describe("the drawings layer", () => {
  it("B3.6: #agent-map always holds svg#map-drawings", async () => {
    const { container } = await mapDrawContractOpen();
    expect(mapDrawContractRoot(container).querySelector("svg#map-drawings")).not.toBeNull();
    mapDrawContractArm(container, "pen");
    expect(mapDrawContractRoot(container).querySelector("svg#map-drawings")).not.toBeNull();
  });

  it("B3.6: one element per drawing, in ascending id order, with its kind", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const els = mapDrawContractDrawn(container);
    expect(els.map((e) => e.getAttribute("data-draw-id"))).toEqual(["a", "b", "c", "d", "e"]);
    expect(els.map((e) => e.getAttribute("data-kind"))).toEqual(["pen", "line", "rect", "ellipse", "text"]);
    expect(els.map((e) => e.localName)).toEqual(["path", "line", "rect", "ellipse", "text"]);
  });

  it("B3.6: pen is a path with d = penPath(pts)", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const pen = mapDrawContractLayer(container)!.querySelector('[data-draw-id="a"]')!;
    expect(pen.getAttribute("d")).toBe("M 0 0 L 10 5 L 20 0");
    expect(mapDrawContractNum(pen, "stroke-width")).toBe(3);
  });

  it("B3.6: line carries x1 y1 x2 y2 = pts", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const line = mapDrawContractLayer(container)!.querySelector('[data-draw-id="b"]')!;
    expect(["x1", "y1", "x2", "y2"].map((a) => mapDrawContractNum(line, a))).toEqual([1, 2, 3, 4]);
    expect(mapDrawContractNum(line, "stroke-width")).toBe(2);
  });

  it("B3.6: rect carries the smaller corner and the absolute spans", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const rect = mapDrawContractLayer(container)!.querySelector('[data-draw-id="c"]')!;
    expect(["x", "y", "width", "height"].map((a) => mapDrawContractNum(rect, a))).toEqual([10, 20, 40, 40]);
    expect(mapDrawContractNum(rect, "stroke-width")).toBe(1.5);
  });

  it("B3.6: ellipse carries the box centre and half the spans", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const el = mapDrawContractLayer(container)!.querySelector('[data-draw-id="d"]')!;
    expect(["cx", "cy", "rx", "ry"].map((a) => mapDrawContractNum(el, a))).toEqual([100, 50, 100, 50]);
    expect(mapDrawContractNum(el, "stroke-width")).toBe(4);
  });

  it("B3.6: text carries font-size and one tspan per line", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const el = mapDrawContractLayer(container)!.querySelector('[data-draw-id="e"]')!;
    expect(mapDrawContractNum(el, "font-size")).toBe(18);
    const spans = [...el.children].filter((n) => n.localName === "tspan");
    expect(spans.map((s) => s.textContent)).toEqual(["one", "two"]);
  });

  it("B3.6: geometry stays in world units when the view pans", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractEveryKind() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(5));
    const snap = () =>
      mapDrawContractDrawn(container).map((e) =>
        ["d", "x1", "y1", "x2", "y2", "x", "y", "width", "height", "cx", "cy", "rx", "ry", "stroke-width", "font-size"].map((a) =>
          e.getAttribute(a),
        ),
      );
    const before = snap();
    const worldBefore = mapDrawContractWorld(container);
    const root = mapDrawContractRoot(container);
    fireEvent.mouseDown(root, { clientX: 2000, clientY: 1500, button: 0, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 2050, clientY: 1550, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 2100, clientY: 1600, buttons: 1 });
    fireEvent.mouseUp(window, { clientX: 2100, clientY: 1600, button: 0 });
    expect(mapDrawContractWorld(container)).not.toBe(worldBefore);
    expect(snap()).toEqual(before);
  });
});

// ---------------------------------------------------------------------------
// Pen, line, rect, ellipse (B3.7–B3.10)
// ---------------------------------------------------------------------------

describe("drawing a shape", () => {
  for (const [kind, tag] of [
    ["pen", "path"],
    ["line", "line"],
    ["rect", "rect"],
    ["ellipse", "ellipse"],
  ] as const) {
    it(`B3.7: ${kind} shows one ${tag} draft while held, then the drawing on release`, async () => {
      const { container } = await mapDrawContractOpen();
      mapDrawContractArm(container, kind);
      mapDrawContractDown(mapDrawContractSurface(container)!, 100, 100);
      mapDrawContractMove(160, 140);
      mapDrawContractMove(220, 190);
      const drafts = mapDrawContractDrafts(container);
      expect(drafts).toHaveLength(1);
      expect(drafts[0]!.localName).toBe(tag);
      expect(drafts[0]!.hasAttribute("data-draw-id")).toBe(false);
      expect(mapDrawContractDrawn(container)).toHaveLength(0);
      mapDrawContractUp(220, 190);
      expect(mapDrawContractDrafts(container)).toHaveLength(0);
      const drawn = mapDrawContractDrawn(container);
      expect(drawn).toHaveLength(1);
      expect(drawn[0]!.getAttribute("data-kind")).toBe(kind);
      expect(drawn[0]!.localName).toBe(tag);
    });
  }

  it("B3.8: a line's pts are the rounded press then release world points, size DRAW_STROKE_PX / z", async () => {
    // world = ((clientX - 30) / 0.75, (clientY - 20) / 0.75); the camera's zoom is kept within 0.25..2.5
    const { container, net } = await mapDrawContractOpen({ view: { x: 30, y: 20, z: 0.75 } });
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 80],
      [180, 140],
      [250, 191],
    ]);
    const [id, d] = await mapDrawContractFirstSaved(net);
    expect(d.kind).toBe("line");
    expect(d.pts).toEqual([93.3, 80, 293.3, 228]);
    expect(d.size).toBeCloseTo(4, 2);
    expect(mapDrawContractIds(container)).toEqual([id]);
  });

  it("B3.8: a rect keeps press-then-release order in pts", async () => {
    const { container, net } = await mapDrawContractOpen({ view: { x: 0, y: 0, z: 2 } });
    mapDrawContractArm(container, "rect");
    mapDrawContractStroke(container, [
      [301, 401],
      [200, 300],
      [101, 51],
    ]);
    const [, d] = await mapDrawContractFirstSaved(net);
    expect(d.kind).toBe("rect");
    expect(d.pts).toEqual([150.5, 200.5, 50.5, 25.5]);
    expect(d.size).toBeCloseTo(1.5, 2);
  });

  it("B3.8: an ellipse's pts are the rounded press then release points", async () => {
    const { container, net } = await mapDrawContractOpen({ view: { x: 0, y: 0, z: 1 } });
    mapDrawContractArm(container, "ellipse");
    mapDrawContractStroke(container, [
      [100, 100],
      [200, 160],
      [300, 220],
    ]);
    const [, d] = await mapDrawContractFirstSaved(net);
    expect(d.kind).toBe("ellipse");
    expect(d.pts).toEqual([100, 100, 300, 220]);
    expect(d.size).toBeCloseTo(3, 2);
  });

  it("B3.8: a pen starts at the rounded press point and ends at the rounded last position", async () => {
    // z = 2: world = client / 2
    const { container, net } = await mapDrawContractOpen({ view: { x: 0, y: 0, z: 2 } });
    mapDrawContractArm(container, "pen");
    mapDrawContractStroke(container, [
      [100, 100],
      [150, 200],
      [201, 101],
    ]);
    const [, d] = await mapDrawContractFirstSaved(net);
    expect(d.kind).toBe("pen");
    expect(d.size).toBeCloseTo(1.5, 2);
    expect(d.pts.length % 2).toBe(0);
    expect(d.pts.length).toBeGreaterThanOrEqual(4);
    expect(d.pts.slice(0, 2)).toEqual([50, 50]);
    expect(d.pts.slice(-2)).toEqual([100.5, 50.5]);
    // the corner at (75, 100) is far off the chord, so simplification keeps it
    const pairs: string[] = [];
    for (let i = 0; i < d.pts.length; i += 2) pairs.push(`${d.pts[i]},${d.pts[i + 1]}`);
    expect(pairs).toContain("75,100");
  });

  it("B3.8: every pen coordinate is rounded to at most one decimal place", async () => {
    // z 0.75, inside the camera's 0.25..2.5 range: world = client / 0.75
    const { container, net } = await mapDrawContractOpen({ view: { x: 0, y: 0, z: 0.75 } });
    mapDrawContractArm(container, "pen");
    const path: [number, number][] = [];
    for (let i = 0; i <= 30; i++) path.push([100 + i * 7, 100 + Math.round(Math.sin(i / 3) * 80)]);
    mapDrawContractStroke(container, path);
    const [, d] = await mapDrawContractFirstSaved(net);
    for (const n of d.pts) expect(Math.abs(n * 10 - Math.round(n * 10))).toBeLessThan(1e-6);
    const round1 = (v: number) => Math.round(v * 10) / 10;
    const [lx, ly] = path.at(-1)!;
    expect(d.pts.slice(0, 2)).toEqual([133.3, 133.3]);
    expect(d.pts.slice(-2)).toEqual([round1(lx / 0.75), round1(ly / 0.75)]);
  });

  it("B3.9: a release within 4px of the press, with no farther move, creates nothing", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [101, 101],
      [102, 101],
    ]);
    expect(mapDrawContractDrafts(container)).toHaveLength(0);
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    expect(mapDrawContractPressed(container)).toEqual(["line"]);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.9: a rect or ellipse of zero width or zero height creates nothing", async () => {
    const { container, net } = await mapDrawContractOpen();
    for (const kind of ["rect", "ellipse"] as const) {
      mapDrawContractArm(container, kind);
      mapDrawContractStroke(container, [
        [100, 100],
        [100, 160],
        [100, 220],
      ]);
      mapDrawContractStroke(container, [
        [100, 100],
        [160, 100],
        [220, 100],
      ]);
      expect(mapDrawContractDrafts(container)).toHaveLength(0);
      expect(mapDrawContractDrawn(container)).toHaveLength(0);
      expect(mapDrawContractPressed(container)).toEqual([kind]);
    }
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.10: Escape while pressed drops the draft, creates nothing, and keeps the tool", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "rect");
    mapDrawContractDown(mapDrawContractSurface(container)!, 100, 100);
    mapDrawContractMove(200, 200);
    expect(mapDrawContractDrafts(container)).toHaveLength(1);
    mapDrawContractEscape(container);
    expect(mapDrawContractDrafts(container)).toHaveLength(0);
    mapDrawContractUp(250, 250);
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    expect(mapDrawContractPressed(container)).toEqual(["rect"]);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Text (B3.11–B3.14)
// ---------------------------------------------------------------------------

/** A press and release on the surface at one point. */
function mapDrawContractTap(c: ParentNode, x: number, y: number) {
  mapDrawContractDown(mapDrawContractSurface(c)!, x, y);
  mapDrawContractUp(x, y);
}

async function mapDrawContractOpenEditor(c: ParentNode, x: number, y: number) {
  mapDrawContractTap(c, x, y);
  await waitFor(() => expect(mapDrawContractEditor(c)).not.toBeNull());
  return mapDrawContractEditor(c)!;
}

const mapDrawContractType = (ta: HTMLTextAreaElement, value: string) => fireEvent.change(ta, { target: { value } });

describe("text", () => {
  it("B3.11: a press opens one focused textarea inside #agent-map and creates nothing", async () => {
    const { container } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    expect(mapDrawContractRoot(container).contains(ta)).toBe(true);
    await waitFor(() => expect(document.activeElement).toBe(ta));
    expect(container.querySelectorAll("textarea#map-text-input")).toHaveLength(1);
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
  });

  it("B3.12: Enter commits a text drawing at the press point with size DRAW_TEXT_PX / z", async () => {
    // world = ((clientX - 10) / 2, clientY / 2)
    const { container, net } = await mapDrawContractOpen({ view: { x: 10, y: 0, z: 2 } });
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 111, 41);
    mapDrawContractType(ta, "  hello\nworld  ");
    fireEvent.keyDown(ta, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(mapDrawContractEditor(container)).toBeNull());
    expect(mapDrawContractPressed(container)).toEqual(["text"]);
    const [id, d] = await mapDrawContractFirstSaved(net);
    expect(d.kind).toBe("text");
    expect(d.pts).toEqual([50.5, 20.5]);
    expect(d.size).toBeCloseTo(9, 2);
    expect(d.text).toBe("hello\nworld");
    const el = mapDrawContractLayer(container)!.querySelector(`[data-draw-id="${id}"]`)!;
    expect([...el.children].filter((n) => n.localName === "tspan").map((s) => s.textContent)).toEqual(["hello", "world"]);
  });

  it("B3.12: blur commits the same way", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    mapDrawContractType(ta, "note");
    fireEvent.blur(ta);
    await waitFor(() => expect(mapDrawContractEditor(container)).toBeNull());
    const [, d] = await mapDrawContractFirstSaved(net);
    expect(d).toEqual({ kind: "text", pts: [200, 150], size: 18, text: "note" });
    expect(mapDrawContractDrawn(container)).toHaveLength(1);
    expect(mapDrawContractPressed(container)).toEqual(["text"]);
  });

  it("B3.12: Shift+Enter does not commit", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    mapDrawContractType(ta, "line one");
    fireEvent.keyDown(ta, { key: "Enter", code: "Enter", shiftKey: true });
    await mapDrawContractSleep(50);
    expect(mapDrawContractEditor(container)).not.toBeNull();
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    await mapDrawContractSleep(550);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.12: the committed text is trimmed and cut to MAX_TEXT_RUNES code points", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    mapDrawContractType(ta, `  ${"😀".repeat(600)}  `);
    fireEvent.keyDown(ta, { key: "Enter", code: "Enter" });
    const [, d] = await mapDrawContractFirstSaved(net);
    expect(d.text).toBe("😀".repeat(500));
  });

  it("B3.13: a value of only whitespace creates nothing", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    mapDrawContractType(ta, "  \n\t ");
    fireEvent.keyDown(ta, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(mapDrawContractEditor(container)).toBeNull());
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.13: Escape in the editor closes it, creates nothing, and keeps the tool armed", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    mapDrawContractType(ta, "discard me");
    fireEvent.keyDown(ta, { key: "Escape", code: "Escape" });
    await waitFor(() => expect(mapDrawContractEditor(container)).toBeNull());
    expect(mapDrawContractPressed(container)).toEqual(["text"]);
    expect(mapDrawContractRoot(container).getAttribute("data-tool")).toBe("text");
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.14: a press while the editor is open commits it and opens a new one at the new point", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "text");
    const ta = await mapDrawContractOpenEditor(container, 200, 150);
    mapDrawContractType(ta, "first");
    mapDrawContractTap(container, 400, 300);
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(1));
    expect(container.querySelectorAll("textarea#map-text-input")).toHaveLength(1);
    const second = mapDrawContractEditor(container)!;
    expect(second.value).toBe("");
    mapDrawContractType(second, "second");
    fireEvent.keyDown(second, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(2));
    await waitFor(() => expect(net.posts.length).toBeGreaterThan(0));
    await mapDrawContractSleep(500);
    const saved = Object.assign({}, ...net.posts.map((p) => p.set)) as Record<string, Drawing>;
    const byText = Object.fromEntries(Object.values(saved).map((d) => [d.text ?? "", d.pts]));
    expect(byText).toEqual({ first: [200, 150], second: [400, 300] });
  });
});

// ---------------------------------------------------------------------------
// Erase (B3.15–B3.16)
// ---------------------------------------------------------------------------

// a1 runs across at y 50, b1 runs down at x 150; they cross at (150, 50)
const mapDrawContractCross = (): Record<string, Drawing> => ({
  a1: mapDrawContractLine(0, 50, 300, 50),
  b1: mapDrawContractLine(150, 0, 150, 100),
  c1: mapDrawContractLine(0, 400, 300, 400),
});

describe("erase", () => {
  it("B3.15: a press on a drawing deletes it and queues its remove", async () => {
    const { container, net } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(3));
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 50, 400);
    expect(mapDrawContractIds(container)).toEqual(["a1", "b1"]);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    expect(net.posts[0]).toEqual({ set: {}, remove: ["c1"] });
  });

  it("B3.15: when several are hit, the one latest in the layer's order goes", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(3));
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 150, 50);
    expect(mapDrawContractIds(container)).toEqual(["a1", "c1"]);
  });

  it("B3.15: a press that hits nothing changes nothing", async () => {
    const { container, net } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(3));
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 600, 600);
    expect(mapDrawContractIds(container)).toEqual(["a1", "b1", "c1"]);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.15: each move while held deletes what it hits", async () => {
    const { container, net } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(3));
    mapDrawContractArm(container, "erase");
    mapDrawContractDown(mapDrawContractSurface(container)!, 600, 600);
    mapDrawContractMove(250, 400);
    expect(mapDrawContractIds(container)).toEqual(["a1", "b1"]);
    mapDrawContractMove(250, 50);
    expect(mapDrawContractIds(container)).toEqual(["b1"]);
    mapDrawContractUp(250, 50);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    expect(net.posts[0]!.set).toEqual({});
    expect([...net.posts[0]!.remove].sort()).toEqual(["a1", "c1"]);
  });

  it("B3.15: the reach is ERASE_TOL_PX / z plus half the stroke, in world units", async () => {
    // z 2: tol = 3 world units; size 2 adds 1, so r = 4. world = client / 2.
    const { container } = await mapDrawContractOpen({ view: { x: 0, y: 0, z: 2 }, drawings: { l: mapDrawContractLine(0, 100, 300, 100, 2) } });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(1));
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 200, 210); // world (100, 105): 5 away
    expect(mapDrawContractIds(container)).toEqual(["l"]);
    mapDrawContractTap(container, 200, 207); // world (100, 103.5): 3.5 away
    expect(mapDrawContractIds(container)).toEqual([]);
  });

  it("B3.16: hovering marks the drawing a press would delete with erase-hit, and only that one", async () => {
    const { container } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(3));
    mapDrawContractArm(container, "erase");
    const surface = mapDrawContractSurface(container)!;
    const hit = () => mapDrawContractDrawn(container).filter((e) => e.classList.contains("erase-hit")).map((e) => e.getAttribute("data-draw-id"));
    fireEvent.mouseMove(surface, { clientX: 150, clientY: 50, buttons: 0 });
    expect(hit()).toEqual(["b1"]);
    fireEvent.mouseMove(surface, { clientX: 50, clientY: 400, buttons: 0 });
    expect(hit()).toEqual(["c1"]);
    fireEvent.mouseMove(surface, { clientX: 600, clientY: 600, buttons: 0 });
    expect(hit()).toEqual([]);
    expect(mapDrawContractIds(container)).toEqual(["a1", "b1", "c1"]);
  });
});

// ---------------------------------------------------------------------------
// Saving (B3.17–B3.22)
// ---------------------------------------------------------------------------

describe("saving drawings", () => {
  it("B3.17: mount loads the drawings once and shows them after sanitizeDrawings", async () => {
    const { container, net } = await mapDrawContractOpen({
      drawings: { ok: mapDrawContractLine(0, 0, 10, 10), bad: { kind: "star", pts: [0, 0, 1, 1], size: 1 }, "": mapDrawContractLine(0, 0, 5, 5) },
    });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(1));
    expect(mapDrawContractIds(container)).toEqual(["ok"]);
    await mapDrawContractSleep(300);
    expect(net.gets).toBe(1);
  });

  it("B3.17: a failed load shows none and blocks nothing", async () => {
    const { container, net } = await mapDrawContractOpen({ loadFails: true });
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [200, 200],
    ]);
    expect(mapDrawContractDrawn(container)).toHaveLength(1);
    await waitFor(() => expect(net.posts).toHaveLength(1));
  });

  it("B3.18: one POST, 400 ms after the last change, carries every queued op", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [200, 200],
    ]);
    await mapDrawContractSleep(250);
    mapDrawContractStroke(container, [
      [300, 100],
      [400, 200],
    ]);
    await mapDrawContractSleep(250);
    expect(net.posts).toHaveLength(0);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    await mapDrawContractSleep(500);
    expect(net.posts).toHaveLength(1);
    const ids = mapDrawContractIds(container);
    expect(Object.keys(net.posts[0]!.set).sort()).toEqual([...ids].sort());
    expect(ids).toHaveLength(2);
    expect(net.posts[0]!.remove).toEqual([]);
  });

  it("B3.18: erasing a drawing still queued drops its set and queues its remove", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [id] = mapDrawContractIds(container);
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 200, 100);
    expect(mapDrawContractIds(container)).toEqual([]);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    expect(net.posts[0]).toEqual({ set: {}, remove: [id] });
  });

  it("B3.18: nothing is sent when nothing is queued", async () => {
    const { net } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await mapDrawContractSleep(700);
    expect(net.posts).toHaveLength(0);
  });

  it("B3.18: saving does not wait for the load", async () => {
    const { container, net } = await mapDrawContractOpen({ deferred: true });
    mapDrawContractArm(container, "rect");
    mapDrawContractStroke(container, [
      [100, 100],
      [200, 200],
    ]);
    const [id] = await mapDrawContractFirstSaved(net);
    expect(mapDrawContractIds(container)).toEqual([id]);
  });

  it("B3.19: a drawing made before the load answers survives the answer", async () => {
    const { container, net } = await mapDrawContractOpen({ deferred: true });
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [mine] = mapDrawContractIds(container);
    await act(async () => {
      net.answer({ "0server": mapDrawContractLine(0, 500, 100, 500) });
    });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(2));
    expect([...mapDrawContractIds(container)].sort()).toEqual(["0server", mine].sort());
  });

  it("B3.19: a drawing erased before the load answers stays erased", async () => {
    const { container, net } = await mapDrawContractOpen({ deferred: true });
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [x] = await mapDrawContractFirstSaved(net);
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 200, 100);
    await waitFor(() => expect(net.posts).toHaveLength(2));
    expect(net.posts[1]).toEqual({ set: {}, remove: [x] });
    // the answer still names x, as a load issued before the save would
    await act(async () => {
      net.answer({ [x]: mapDrawContractLine(100, 100, 300, 100, 3), other: mapDrawContractLine(0, 500, 100, 500) });
    });
    await waitFor(() => expect(mapDrawContractIds(container)).toEqual(["other"]));
  });

  it("B3.20: a failed POST keeps its ops queued, does not retry, and sends them with the next change", async () => {
    const { container, net } = await mapDrawContractOpen();
    net.failSaves = true;
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [first] = await mapDrawContractFirstSaved(net);
    await mapDrawContractSleep(900);
    expect(net.posts).toHaveLength(1);
    net.failSaves = false;
    mapDrawContractStroke(container, [
      [100, 300],
      [300, 300],
    ]);
    await waitFor(() => expect(net.posts).toHaveLength(2));
    const ids = mapDrawContractIds(container);
    expect(ids).toContain(first);
    expect(Object.keys(net.posts[1]!.set).sort()).toEqual([...ids].sort());
  });

  it("B3.20: a failed POST's ops go out with the unmount", async () => {
    const { container, net, unmount } = await mapDrawContractOpen();
    net.failSaves = true;
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [first] = await mapDrawContractFirstSaved(net);
    await mapDrawContractSleep(100);
    net.failSaves = false;
    unmount();
    await waitFor(() => expect(net.posts).toHaveLength(2));
    expect(Object.keys(net.posts[1]!.set)).toEqual([first]);
  });

  it("B3.21: unmounting with ops queued sends them in one POST", async () => {
    const { container, net, unmount } = await mapDrawContractOpen({ drawings: mapDrawContractCross() });
    await waitFor(() => expect(mapDrawContractDrawn(container)).toHaveLength(3));
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 600],
      [300, 600],
    ]);
    const [made] = mapDrawContractIds(container).filter((i) => !["a1", "b1", "c1"].includes(i ?? ""));
    mapDrawContractArm(container, "erase");
    mapDrawContractTap(container, 50, 400);
    unmount();
    await waitFor(() => expect(net.posts).toHaveLength(1));
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(1);
    expect(Object.keys(net.posts[0]!.set)).toEqual([made]);
    expect(net.posts[0]!.remove).toEqual(["c1"]);
  });

  it("B3.22: queued ops sit in localStorage until a POST carrying them succeeds", async () => {
    const { container, net } = await mapDrawContractOpen();
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [id] = mapDrawContractIds(container);
    await waitFor(() => expect(Object.keys(readUnsavedDrawOps().set)).toEqual([id]));
    expect(net.posts).toHaveLength(0);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    await waitFor(() => expect(localStorage.getItem(DRAW_UNSAVED_KEY)).toBeNull());
  });

  it("B3.22: a failed POST leaves its ops in localStorage", async () => {
    const { container, net } = await mapDrawContractOpen();
    net.failSaves = true;
    mapDrawContractArm(container, "line");
    mapDrawContractStroke(container, [
      [100, 100],
      [300, 100],
    ]);
    const [id] = await mapDrawContractFirstSaved(net);
    await mapDrawContractSleep(100);
    expect(Object.keys(readUnsavedDrawOps().set)).toEqual([id]);
  });

  it("B3.22: on mount the stored ops are applied over the server's answer and queued again", async () => {
    localStorage.setItem(
      DRAW_UNSAVED_KEY,
      JSON.stringify({ set: { u1: mapDrawContractLine(0, 0, 50, 50) }, remove: ["s1"] }),
    );
    const { container, net } = await mapDrawContractOpen({
      drawings: { s1: mapDrawContractLine(0, 100, 50, 100), s2: mapDrawContractLine(0, 200, 50, 200) },
    });
    await waitFor(() => expect(mapDrawContractIds(container)).toEqual(["s2", "u1"]));
    await waitFor(() => expect(net.posts).toHaveLength(1));
    expect(net.posts[0]).toEqual({ set: { u1: mapDrawContractLine(0, 0, 50, 50) }, remove: ["s1"] });
  });
});

// ---------------------------------------------------------------------------
// Unchanged (B3.23)
// ---------------------------------------------------------------------------

describe("the map with no tool armed", () => {
  it("B3.23: a card drag still moves the card and draws nothing", async () => {
    const { container, net } = await mapDrawContractOpenWithCards();
    const face = mapDrawContractFace(container, "a");
    fireEvent.mouseDown(face, { clientX: 150, clientY: 120, button: 0, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 180, clientY: 135, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 210, clientY: 150, buttons: 1 });
    fireEvent.mouseUp(window, { clientX: 210, clientY: 150, button: 0 });
    expect(mapDrawContractCard(container, "a").style.left).toBe("160px");
    expect(mapDrawContractCard(container, "a").style.top).toBe("130px");
    expect(mapDrawContractDrawn(container)).toHaveLength(0);
    await mapDrawContractSleep(600);
    expect(net.posts).toHaveLength(0);
  });
});
