import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// mapdraw.edge.test.tsx: athena-edge's blind suite for drawing on the agent
// map (50-contract.md B3.1 to B3.23): presses that must create nothing
// (non-primary buttons, tiny gestures, zero-size shapes, pointercancel,
// Escape mid-press), the text editor's trims, cuts and refusals, the eraser's
// reach, overlap and queued-set cases, failed loads and saves, the unsaved
// record in localStorage, and the map with no tool armed. Gestures are mouse
// events (jsdom has no PointerEvent), dispatched on the surface so they reach
// both the surface and window; the map's page offset is (0, 0).

afterEach(async () => {
  // unmount first: the map's unmount flush (B3.21) must reach this test's own
  // fetch mock, and anything it writes back to the unsaved record (B3.22) must
  // be cleared below rather than carried into the next test's mount, so the
  // flush's answer is let settle before the record is cleared
  cleanup();
  vi.useRealTimers();
  await new Promise((r) => setTimeout(r, 50));
  vi.restoreAllMocks();
  localStorage.removeItem("kraken.mapView");
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapDrawUnsaved");
});

// ---- harness

type MapDrawEdgeView = { x: number; y: number; z: number };
type MapDrawEdgeDrawing = { kind: string; pts: number[]; size: number; text?: string };
interface MapDrawEdgePost {
  set: Record<string, MapDrawEdgeDrawing>;
  remove: string[];
}

const mapDrawEdgeActions = (): BoardActions => ({
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

function mapDrawEdgeModel(jobs: WebJob[]): BoardModel {
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

function MapDrawEdgeMap({ jobs, actions }: { jobs: WebJob[]; actions: BoardActions }) {
  const search = useJobSearch(mapDrawEdgeModel(jobs));
  return <MapView search={search} actions={actions} />;
}

const mapDrawEdgeJSON = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

interface MapDrawEdgeOpts {
  view?: MapDrawEdgeView;
  jobs?: WebJob[];
  positions?: Record<string, [number, number]>;
  load?: () => Promise<Response>;
  save?: (body: MapDrawEdgePost) => Response | Promise<Response>;
}

function mapDrawEdgeServer(o: MapDrawEdgeOpts) {
  const posts: MapDrawEdgePost[] = [];
  let gets = 0;
  const load = o.load ?? (async () => mapDrawEdgeJSON({ drawings: {} }));
  const save = o.save ?? (() => mapDrawEdgeJSON({ ok: true }));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.includes("/api/map/drawings")) {
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as MapDrawEdgePost;
        posts.push(body);
        return save(body);
      }
      gets++;
      return load();
    }
    if (url.includes("/api/map/positions")) {
      if (init?.method === "POST") return mapDrawEdgeJSON({ ok: true });
      return mapDrawEdgeJSON({ positions: o.positions ?? { a: [0, 0] } });
    }
    return new Response("{}", { status: 200 });
  });
  return { posts, gets: () => gets };
}

async function mapDrawEdgeMount(o: MapDrawEdgeOpts = {}) {
  localStorage.setItem("kraken.mapView", JSON.stringify(o.view ?? { x: 0, y: 0, z: 1 }));
  const srv = mapDrawEdgeServer(o);
  const actions = mapDrawEdgeActions();
  const jobs = o.jobs ?? [makeJob({ slug: "a", state: "draft" })];
  const view = render(<MapDrawEdgeMap jobs={jobs} actions={actions} />);
  await waitFor(() => expect(srv.gets()).toBeGreaterThan(0));
  await mapDrawEdgeSleep(30);
  return { ...view, ...srv, actions, jobs };
}

const mapDrawEdgeSleep = (ms: number) =>
  act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });

const mapDrawEdgeRoot = (c: HTMLElement) => c.querySelector("#agent-map") as HTMLElement;
const mapDrawEdgeTool = (c: HTMLElement, tool: string) =>
  c.querySelector(`#map-tools button.map-tool[data-tool="${tool}"]`) as HTMLElement;
const mapDrawEdgeSurface = (c: HTMLElement) => c.querySelector("#map-draw-surface") as HTMLElement | null;
const mapDrawEdgeEditor = (c: HTMLElement) => c.querySelector("textarea#map-text-input") as HTMLTextAreaElement | null;
const mapDrawEdgeDrawn = (c: HTMLElement) => [...c.querySelectorAll<SVGElement>("#map-drawings [data-draw-id]")];
const mapDrawEdgeIds = (c: HTMLElement) => mapDrawEdgeDrawn(c).map((el) => el.getAttribute("data-draw-id")!);
const mapDrawEdgeDrafts = (c: HTMLElement) => c.querySelectorAll('#map-drawings [data-draft="true"]');
const mapDrawEdgeNum = (el: Element, attr: string) => parseFloat(el.getAttribute(attr) ?? "NaN");
const mapDrawEdgeCard = (c: HTMLElement, key: string) => c.querySelector(`.map-card[data-key="${key}"]`) as HTMLElement;
const mapDrawEdgeWorld = (c: HTMLElement) => (c.querySelector(".map-world") as HTMLElement | null)?.style.transform;
const mapDrawEdgeSelected = (c: HTMLElement) =>
  [...c.querySelectorAll<HTMLElement>(".map-card.selected")].map((el) => el.dataset.key!).sort();

function mapDrawEdgeArm(c: HTMLElement, tool: string) {
  fireEvent.click(mapDrawEdgeTool(c, tool));
  expect(mapDrawEdgeRoot(c).getAttribute("data-tool"), `arming ${tool}`).toBe(tool);
}

/** A mouse press at `from`, a move through each of `moves`, a release at `to`. */
function mapDrawEdgeGesture(
  el: HTMLElement,
  from: [number, number],
  moves: Array<[number, number]>,
  to: [number, number],
  button = 0,
) {
  fireEvent.mouseDown(el, { clientX: from[0], clientY: from[1], button, buttons: button === 0 ? 1 : 1 << button });
  for (const [x, y] of moves) fireEvent.mouseMove(el, { clientX: x, clientY: y, buttons: 1 });
  fireEvent.mouseUp(el, { clientX: to[0], clientY: to[1], button });
}

/** A press-move-release from `from` to `to` on the surface. */
function mapDrawEdgeDraw(c: HTMLElement, from: [number, number], to: [number, number]) {
  mapDrawEdgeGesture(mapDrawEdgeSurface(c)!, from, [to], to);
}

/** A pointer-family event carrying coordinates, built on MouseEvent (jsdom has no PointerEvent). */
function mapDrawEdgePointer(el: Element, type: string, x: number, y: number) {
  fireEvent(
    el,
    new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0, buttons: type === "pointerup" ? 0 : 1 }),
  );
}

function mapDrawEdgeShiftClick(c: HTMLElement, key: string) {
  const face = mapDrawEdgeCard(c, key).querySelector(".card") as HTMLElement;
  fireEvent.mouseDown(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
  fireEvent.mouseUp(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
  fireEvent.click(face, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
}

const mapDrawEdgeLine = (pts: number[], size = 2): MapDrawEdgeDrawing => ({ kind: "line", pts, size });

/** The value has at most one decimal place. */
const mapDrawEdgeOneDecimal = (v: number) => Math.abs(v * 10 - Math.round(v * 10)) < 1e-6;

function mapDrawEdgeTypeText(c: HTMLElement, value: string) {
  const ed = mapDrawEdgeEditor(c);
  expect(ed, "the text editor is open").not.toBeNull();
  fireEvent.change(ed!, { target: { value } });
  return ed!;
}

function mapDrawEdgeEnter(el: HTMLElement, shiftKey = false) {
  fireEvent.keyDown(el, { key: "Enter", code: "Enter", shiftKey });
}

// ---- toolbar (B3.1, B3.2, B3.3)

describe("B3.1 the toolbar with no card on the map", () => {
  it("renders all six tools in order, and the drawing layer, with no jobs", async () => {
    const { container } = await mapDrawEdgeMount({ jobs: [], positions: {} });
    const buttons = [...container.querySelectorAll<HTMLElement>(".map-corner #map-tools button.map-tool")];
    expect(buttons.map((b) => b.getAttribute("data-tool"))).toEqual(["pen", "line", "rect", "ellipse", "text", "erase"]);
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(["pen", "line", "rect", "oval", "text", "erase"]);
    expect(container.querySelector("#agent-map svg#map-drawings")).not.toBeNull();
  });
});

describe("B3.2 arming", () => {
  it("the armed tool is not remembered across a remount", async () => {
    const first = await mapDrawEdgeMount();
    mapDrawEdgeArm(first.container, "pen");
    first.unmount();
    const again = await mapDrawEdgeMount();
    expect(mapDrawEdgeRoot(again.container).hasAttribute("data-tool")).toBe(false);
    expect(mapDrawEdgeSurface(again.container)).toBeNull();
    for (const b of again.container.querySelectorAll("#map-tools button.map-tool")) {
      expect(b.getAttribute("aria-pressed")).toBe("false");
    }
  });

  it("clicking the armed tool again disarms it and removes the surface", async () => {
    const { container } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "rect");
    fireEvent.click(mapDrawEdgeTool(container, "rect"));
    expect(mapDrawEdgeRoot(container).hasAttribute("data-tool")).toBe(false);
    expect(mapDrawEdgeSurface(container)).toBeNull();
    expect(mapDrawEdgeTool(container, "rect").getAttribute("aria-pressed")).toBe("false");
  });

  it("clicking another tool switches straight to it, leaving exactly one pressed", async () => {
    const { container } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "pen");
    mapDrawEdgeArm(container, "erase");
    const pressed = [...container.querySelectorAll("#map-tools button.map-tool")].filter(
      (b) => b.getAttribute("aria-pressed") === "true",
    );
    expect(pressed.map((b) => b.getAttribute("data-tool"))).toEqual(["erase"]);
    expect(mapDrawEdgeSurface(container)).not.toBeNull();
  });
});

// ---- input (B3.4)

describe("B3.4 presses the surface ignores or contains", () => {
  for (const button of [1, 2]) {
    it(`a press with button ${button} creates nothing and shows no draft`, async () => {
      const { container, posts } = await mapDrawEdgeMount();
      mapDrawEdgeArm(container, "line");
      const s = mapDrawEdgeSurface(container)!;
      fireEvent.mouseDown(s, { clientX: 100, clientY: 100, button, buttons: 1 << button });
      fireEvent.mouseMove(s, { clientX: 300, clientY: 200, buttons: 1 << button });
      expect(mapDrawEdgeDrafts(container)).toHaveLength(0);
      fireEvent.mouseUp(s, { clientX: 300, clientY: 200, button });
      expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
      await mapDrawEdgeSleep(600);
      expect(posts).toHaveLength(0);
    });
  }

  it("a press and click over a card neither selects it, opens it, nor moves anything", async () => {
    const { container, actions } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px"));
    const world = mapDrawEdgeWorld(container);
    mapDrawEdgeArm(container, "rect");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 60, clientY: 60, button: 0, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 60, clientY: 60, button: 0 });
    fireEvent.click(s, { clientX: 60, clientY: 60, button: 0 });
    fireEvent.doubleClick(s, { clientX: 60, clientY: 60, button: 0 });
    expect(mapDrawEdgeSelected(container)).toEqual([]);
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
    expect(mapDrawEdgeWorld(container)).toBe(world);
    expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px");
    expect(mapDrawEdgeCard(container, "a").style.top).toBe("0px");
  });

  it("a plain press over the background keeps an existing selection", async () => {
    const { container } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px"));
    mapDrawEdgeShiftClick(container, "a");
    expect(mapDrawEdgeSelected(container)).toEqual(["a"]);
    mapDrawEdgeArm(container, "rect");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 1500, clientY: 1500, button: 0, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 1500, clientY: 1500, button: 0 });
    fireEvent.click(s, { clientX: 1500, clientY: 1500, button: 0 });
    expect(mapDrawEdgeSelected(container)).toEqual(["a"]);
  });

  it("a drag that starts over a card moves neither the card nor the view", async () => {
    const { container } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px"));
    const world = mapDrawEdgeWorld(container);
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [60, 60], [[200, 120], [300, 240]], [300, 240]);
    expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px");
    expect(mapDrawEdgeCard(container, "a").style.top).toBe("0px");
    expect(mapDrawEdgeWorld(container)).toBe(world);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1);
  });

  it("a pointer event with no coordinates does not stop mouse gestures from drawing", async () => {
    const { container } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent(s, new Event("pointermove", { bubbles: true }));
    mapDrawEdgeDraw(container, [100, 100], [200, 150]);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1);
  });

  it("a pointer gesture followed by the browser's compatibility mouse events draws once", async () => {
    const { container } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    const s = mapDrawEdgeSurface(container)!;
    mapDrawEdgePointer(s, "pointerdown", 100, 100);
    mapDrawEdgePointer(s, "pointermove", 200, 150);
    mapDrawEdgePointer(s, "pointerup", 200, 150);
    mapDrawEdgeGesture(s, [100, 100], [[200, 150]], [200, 150]);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1);
  });
});

// ---- Escape (B3.5, B3.10)

describe("B3.5 Escape with a tool armed", () => {
  it("disarms the tool and keeps the selection; a second Escape empties it", async () => {
    const { container } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px"));
    mapDrawEdgeShiftClick(container, "a");
    expect(mapDrawEdgeSelected(container)).toEqual(["a"]);
    mapDrawEdgeArm(container, "pen");
    fireEvent.keyDown(mapDrawEdgeRoot(container), { key: "Escape", code: "Escape" });
    expect(mapDrawEdgeRoot(container).hasAttribute("data-tool")).toBe(false);
    expect(mapDrawEdgeSurface(container)).toBeNull();
    expect(mapDrawEdgeSelected(container)).toEqual(["a"]);
    fireEvent.keyDown(mapDrawEdgeRoot(container), { key: "Escape", code: "Escape" });
    expect(mapDrawEdgeSelected(container)).toEqual([]);
  });
});

describe("B3.10 Escape while a press is held", () => {
  it("drops the draft, creates nothing at the release, and leaves the tool armed", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "rect");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 100, clientY: 100, button: 0, buttons: 1 });
    fireEvent.mouseMove(s, { clientX: 250, clientY: 220, buttons: 1 });
    expect(mapDrawEdgeDrafts(container)).toHaveLength(1);
    fireEvent.keyDown(mapDrawEdgeRoot(container), { key: "Escape", code: "Escape" });
    expect(mapDrawEdgeDrafts(container)).toHaveLength(0);
    expect(mapDrawEdgeRoot(container).getAttribute("data-tool")).toBe("rect");
    fireEvent.mouseMove(s, { clientX: 300, clientY: 260, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 300, clientY: 260, button: 0 });
    expect(mapDrawEdgeDrafts(container)).toHaveLength(0);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("the tool still draws on the next press", async () => {
    const { container } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "rect");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 100, clientY: 100, button: 0, buttons: 1 });
    fireEvent.mouseMove(s, { clientX: 250, clientY: 220, buttons: 1 });
    fireEvent.keyDown(mapDrawEdgeRoot(container), { key: "Escape", code: "Escape" });
    fireEvent.mouseUp(s, { clientX: 250, clientY: 220, button: 0 });
    mapDrawEdgeDraw(container, [400, 400], [500, 450]);
    expect(mapDrawEdgeDrawn(container).map((el) => el.getAttribute("data-kind"))).toEqual(["rect"]);
  });
});

// ---- the layer (B3.6)

describe("B3.6 the layer from a server answer", () => {
  it("orders by ascending id, and drops bad ids and bad drawings", async () => {
    const { container } = await mapDrawEdgeMount({
      load: async () =>
        mapDrawEdgeJSON({
          drawings: {
            b: { kind: "rect", pts: [0, 0, 10, 10], size: 2 },
            "9": mapDrawEdgeLine([0, 0, 5, 5]),
            a: { kind: "text", pts: [1, 1], size: 12, text: "hi" },
            "10": { kind: "ellipse", pts: [0, 0, 10, 20], size: 2 },
            "": mapDrawEdgeLine([0, 0, 5, 5]),
            ["x".repeat(65)]: mapDrawEdgeLine([0, 0, 5, 5]),
            bad: { kind: "star", pts: [0, 0, 1, 1], size: 2 },
            zero: mapDrawEdgeLine([0, 0, 1, 1], 0),
          },
        }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["10", "9", "a", "b"]));
    expect(mapDrawEdgeDrawn(container).map((el) => el.getAttribute("data-kind"))).toEqual([
      "ellipse",
      "line",
      "text",
      "rect",
    ]);
  });

  it("a rect and an ellipse stored with corners in reverse order", async () => {
    const { container } = await mapDrawEdgeMount({
      load: async () =>
        mapDrawEdgeJSON({
          drawings: {
            r: { kind: "rect", pts: [50, 40, 10, 20], size: 3 },
            e: { kind: "ellipse", pts: [50, 40, 10, 20], size: 3 },
          },
        }),
    });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(2));
    const r = container.querySelector('#map-drawings rect[data-draw-id="r"]')!;
    expect([mapDrawEdgeNum(r, "x"), mapDrawEdgeNum(r, "y"), mapDrawEdgeNum(r, "width"), mapDrawEdgeNum(r, "height")]).toEqual([
      10, 20, 40, 20,
    ]);
    const e = container.querySelector('#map-drawings ellipse[data-draw-id="e"]')!;
    expect([mapDrawEdgeNum(e, "cx"), mapDrawEdgeNum(e, "cy"), mapDrawEdgeNum(e, "rx"), mapDrawEdgeNum(e, "ry")]).toEqual([
      30, 30, 20, 10,
    ]);
  });

  it("a text with an empty middle line gets an empty tspan for it", async () => {
    const { container } = await mapDrawEdgeMount({
      load: async () => mapDrawEdgeJSON({ drawings: { t: { kind: "text", pts: [0, 0], size: 10, text: "a\n\nb" } } }),
    });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(1));
    const spans = [...container.querySelectorAll('#map-drawings text[data-draw-id="t"] tspan')];
    expect(spans.map((s) => s.textContent)).toEqual(["a", "", "b"]);
  });

  it("geometry does not change when the view pans or zooms", async () => {
    const { container } = await mapDrawEdgeMount({
      load: async () => mapDrawEdgeJSON({ drawings: { l: mapDrawEdgeLine([5, 6, 70, 80], 4) } }),
    });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(1));
    const el = () => container.querySelector('#map-drawings [data-draw-id="l"]')!;
    const geom = () => ["x1", "y1", "x2", "y2", "stroke-width"].map((a) => el().getAttribute(a));
    const before = geom();
    fireEvent.wheel(mapDrawEdgeRoot(container), { deltaX: 40, deltaY: 0 });
    fireEvent.wheel(mapDrawEdgeRoot(container), { deltaY: -120, ctrlKey: true, clientX: 200, clientY: 200 });
    expect(geom()).toEqual(before);
  });
});

// ---- creating shapes (B3.8, B3.9)

async function mapDrawEdgeOnlyCreated(posts: MapDrawEdgePost[]): Promise<MapDrawEdgeDrawing> {
  await waitFor(() => expect(posts.length).toBeGreaterThan(0));
  const entries = Object.entries(posts[0]!.set);
  expect(entries, "one drawing created").toHaveLength(1);
  return entries[0]![1];
}

describe("B3.8 rounding and size away from zoom 1", () => {
  it("rounds a line's ends to one decimal at zoom 1.5", async () => {
    const { container, posts } = await mapDrawEdgeMount({ view: { x: 0, y: 0, z: 1.5 } });
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 151]);
    const d = await mapDrawEdgeOnlyCreated(posts);
    const exact = [100 / 1.5, 100 / 1.5, 200 / 1.5, 151 / 1.5];
    expect(d.pts).toHaveLength(4);
    d.pts.forEach((v, i) => {
      expect(mapDrawEdgeOneDecimal(v), `pts[${i}] = ${v} has at most one decimal`).toBe(true);
      expect(Math.abs(v - exact[i]!)).toBeLessThanOrEqual(0.05 + 1e-9);
    });
    expect(Math.abs(d.size - 3 / 1.5)).toBeLessThanOrEqual(0.01);
  });

  it("places a rect through a panned and zoomed view", async () => {
    const { container, posts } = await mapDrawEdgeMount({ view: { x: 40, y: -30, z: 2 } });
    mapDrawEdgeArm(container, "rect");
    mapDrawEdgeDraw(container, [140, 70], [240, 170]);
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d.pts).toEqual([50, 50, 100, 100]);
    expect(Math.abs(d.size - 1.5)).toBeLessThanOrEqual(0.01);
  });

  it("a long wiggly pen stroke is saved with at most 500 points, press first and last position last", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "pen");
    const moves: Array<[number, number]> = [];
    for (let i = 1; i <= 1200; i++) moves.push([10 + i, i % 2 === 0 ? 100 : 160]);
    const last = moves[moves.length - 1]!;
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [10, 130], moves, last);
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d.kind).toBe("pen");
    expect(d.pts.length % 2).toBe(0);
    expect(d.pts.length / 2).toBeGreaterThanOrEqual(2);
    expect(d.pts.length / 2).toBeLessThanOrEqual(500);
    expect(d.pts.slice(0, 2)).toEqual([10, 130]);
    expect(d.pts.slice(-2)).toEqual(last);
  });
});

describe("B3.9 gestures that create nothing", () => {
  async function expectNothing(c: HTMLElement, posts: MapDrawEdgePost[], tool: string) {
    expect(mapDrawEdgeDrafts(c)).toHaveLength(0);
    expect(mapDrawEdgeDrawn(c)).toHaveLength(0);
    expect(mapDrawEdgeRoot(c).getAttribute("data-tool")).toBe(tool);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  }

  for (const tool of ["pen", "line", "rect", "ellipse"]) {
    it(`${tool}: a release 3 px from the press with no move`, async () => {
      const { container, posts } = await mapDrawEdgeMount();
      mapDrawEdgeArm(container, tool);
      mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [100, 100], [], [102, 101]);
      await expectNothing(container, posts, tool);
    });
  }

  it("line: a release exactly 4 px (|dx| + |dy|) from the press", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [100, 100], [], [102, 102]);
    await expectNothing(container, posts, "line");
  });

  it("pen: moves that stay within 4 px", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "pen");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [100, 100], [[101, 101], [102, 100], [101, 102]], [102, 101]);
    await expectNothing(container, posts, "pen");
  });

  it("rect: zero width", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "rect");
    mapDrawEdgeDraw(container, [100, 100], [100, 180]);
    await expectNothing(container, posts, "rect");
  });

  it("ellipse: zero height", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "ellipse");
    mapDrawEdgeDraw(container, [100, 100], [180, 100]);
    await expectNothing(container, posts, "ellipse");
  });

  it("a gesture that ends in pointercancel", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    const s = mapDrawEdgeSurface(container)!;
    mapDrawEdgePointer(s, "pointerdown", 100, 100);
    mapDrawEdgePointer(s, "pointermove", 220, 180);
    mapDrawEdgePointer(s, "pointercancel", 220, 180);
    await expectNothing(container, posts, "line");
  });
});

describe("B3.9 gestures just past the threshold still create", () => {
  it("line: a release 5 px from the press", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [100, 100], [], [103, 102]);
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d).toMatchObject({ kind: "line", pts: [100, 100, 103, 102] });
  });

  it("line: a move went farther, then the release came back near the press", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [100, 100], [[130, 100]], [101, 100]);
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d).toMatchObject({ kind: "line", pts: [100, 100, 101, 100] });
  });
});

// ---- text (B3.11 to B3.14)

describe("B3.11 opening the editor", () => {
  it("opens one focused editor and creates nothing", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "text");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 100, clientY: 100, button: 0, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 100, clientY: 100, button: 0 });
    const ed = mapDrawEdgeEditor(container);
    expect(ed).not.toBeNull();
    expect(mapDrawEdgeRoot(container).contains(ed)).toBe(true);
    await waitFor(() => expect(document.activeElement).toBe(mapDrawEdgeEditor(container)));
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("a second press with the editor open leaves exactly one editor", async () => {
    const { container } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "text");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 100, clientY: 100, button: 0, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 100, clientY: 100, button: 0 });
    fireEvent.mouseDown(mapDrawEdgeSurface(container)!, { clientX: 300, clientY: 200, button: 0, buttons: 1 });
    fireEvent.mouseUp(mapDrawEdgeSurface(container)!, { clientX: 300, clientY: 200, button: 0 });
    expect(container.querySelectorAll("textarea#map-text-input")).toHaveLength(1);
  });
});

async function mapDrawEdgeOpenEditor(o: MapDrawEdgeOpts = {}, at: [number, number] = [100, 100]) {
  const m = await mapDrawEdgeMount(o);
  mapDrawEdgeArm(m.container, "text");
  const s = mapDrawEdgeSurface(m.container)!;
  fireEvent.mouseDown(s, { clientX: at[0], clientY: at[1], button: 0, buttons: 1 });
  fireEvent.mouseUp(s, { clientX: at[0], clientY: at[1], button: 0 });
  expect(mapDrawEdgeEditor(m.container)).not.toBeNull();
  return m;
}

describe("B3.12 committing text", () => {
  it("trims, then cuts to 500 code points counted as code points", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    const ed = mapDrawEdgeTypeText(container, "  \n" + "😀".repeat(501) + " \t");
    mapDrawEdgeEnter(ed);
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d.kind).toBe("text");
    expect(d.text).toBe("😀".repeat(500));
  });

  it("keeps newlines inside the text and trims only its ends", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    mapDrawEdgeEnter(mapDrawEdgeTypeText(container, "  one\n\ntwo  \n"));
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d.text).toBe("one\n\ntwo");
  });

  it("Shift+Enter does not commit", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    const ed = mapDrawEdgeTypeText(container, "hello");
    mapDrawEdgeEnter(ed, true);
    expect(mapDrawEdgeEditor(container)).not.toBeNull();
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("blur commits, closes the editor, and leaves text armed", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    fireEvent.blur(mapDrawEdgeTypeText(container, "hi"));
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d).toMatchObject({ kind: "text", text: "hi", pts: [100, 100] });
    expect(mapDrawEdgeEditor(container)).toBeNull();
    expect(mapDrawEdgeRoot(container).getAttribute("data-tool")).toBe("text");
  });

  it("an Enter commit creates exactly one drawing", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    const ed = mapDrawEdgeTypeText(container, "once");
    mapDrawEdgeEnter(ed);
    if (ed.isConnected) fireEvent.blur(ed);
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await mapDrawEdgeSleep(600);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1);
    const sets = posts.flatMap((p) => Object.keys(p.set));
    expect(new Set(sets).size).toBe(1);
  });

  it("places the text at the rounded world point through a panned, zoomed view", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor({ view: { x: 10, y: 20, z: 2 } }, [110, 220]);
    mapDrawEdgeEnter(mapDrawEdgeTypeText(container, "z"));
    const d = await mapDrawEdgeOnlyCreated(posts);
    expect(d.pts).toEqual([50, 100]);
    expect(Math.abs(d.size - 9)).toBeLessThanOrEqual(0.01);
  });
});

describe("B3.13 text that creates nothing", () => {
  it("Enter on whitespace only", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    mapDrawEdgeEnter(mapDrawEdgeTypeText(container, "  \n\t "));
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("blur on an empty editor", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    fireEvent.blur(mapDrawEdgeEditor(container)!);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("Escape discards typed text, closes the editor, and keeps text armed", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    const ed = mapDrawEdgeTypeText(container, "discard me");
    fireEvent.keyDown(ed, { key: "Escape", code: "Escape" });
    expect(mapDrawEdgeEditor(container)).toBeNull();
    expect(mapDrawEdgeRoot(container).getAttribute("data-tool")).toBe("text");
    expect(mapDrawEdgeSurface(container)).not.toBeNull();
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });
});

describe("B3.14 a press with the editor open", () => {
  it("commits the open text, then opens a new editor that commits at the new point", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    mapDrawEdgeTypeText(container, "one");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 300, clientY: 200, button: 0, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 300, clientY: 200, button: 0 });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(1));
    expect(container.querySelectorAll("textarea#map-text-input")).toHaveLength(1);
    mapDrawEdgeEnter(mapDrawEdgeTypeText(container, "two"));
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(2));
    await waitFor(() => expect(posts.length).toBeGreaterThan(0));
    await mapDrawEdgeSleep(500);
    const all = Object.assign({}, ...posts.map((p) => p.set)) as Record<string, MapDrawEdgeDrawing>;
    const byText = Object.fromEntries(Object.values(all).map((d) => [d.text, d.pts]));
    expect(byText).toEqual({ one: [100, 100], two: [300, 200] });
  });

  it("an open editor holding only whitespace commits nothing, and the new editor opens", async () => {
    const { container, posts } = await mapDrawEdgeOpenEditor();
    mapDrawEdgeTypeText(container, "   ");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseDown(s, { clientX: 300, clientY: 200, button: 0, buttons: 1 });
    fireEvent.mouseUp(s, { clientX: 300, clientY: 200, button: 0 });
    expect(container.querySelectorAll("textarea#map-text-input")).toHaveLength(1);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });
});

// ---- erase (B3.15, B3.16)

const mapDrawEdgeTwin = {
  a1: mapDrawEdgeLine([0, 100, 400, 100]),
  b2: mapDrawEdgeLine([0, 100, 400, 100]),
};

function mapDrawEdgePress(c: HTMLElement, x: number, y: number) {
  const s = mapDrawEdgeSurface(c)!;
  fireEvent.mouseDown(s, { clientX: x, clientY: y, button: 0, buttons: 1 });
  fireEvent.mouseUp(s, { clientX: x, clientY: y, button: 0 });
}

describe("B3.15 erase", () => {
  it("a press that hits nothing changes nothing and saves nothing", async () => {
    const { container, posts } = await mapDrawEdgeMount({
      load: async () => mapDrawEdgeJSON({ drawings: { l: mapDrawEdgeLine([0, 100, 400, 100]) } }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["l"]));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 200, 300);
    expect(mapDrawEdgeIds(container)).toEqual(["l"]);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("of two drawings under the press, deletes only the latest in the layer's order", async () => {
    const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeTwin }) });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["a1", "b2"]));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 200, 100);
    expect(mapDrawEdgeIds(container)).toEqual(["a1"]);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({ set: {}, remove: ["b2"] });
  });

  it("a press inside a rectangle, far from its edges, misses it", async () => {
    const { container } = await mapDrawEdgeMount({
      load: async () => mapDrawEdgeJSON({ drawings: { r: { kind: "rect", pts: [400, 400, 0, 0], size: 2 } } }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["r"]));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 200, 200);
    expect(mapDrawEdgeIds(container)).toEqual(["r"]);
  });

  // zoom 2: tol = 6 / 2 = 3 world, reach = 3 + 2 / 2 = 4 world = 8 screen px
  it("at zoom 2, a press 3.5 world units off the line deletes it", async () => {
    const { container } = await mapDrawEdgeMount({
      view: { x: 0, y: 0, z: 2 },
      load: async () => mapDrawEdgeJSON({ drawings: { l: mapDrawEdgeLine([0, 50, 100, 50]) } }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["l"]));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 100, 107);
    expect(mapDrawEdgeIds(container)).toEqual([]);
  });

  it("at zoom 2, a press 5 world units off the line misses it", async () => {
    const { container } = await mapDrawEdgeMount({
      view: { x: 0, y: 0, z: 2 },
      load: async () => mapDrawEdgeJSON({ drawings: { l: mapDrawEdgeLine([0, 50, 100, 50]) } }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["l"]));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 100, 110);
    expect(mapDrawEdgeIds(container)).toEqual(["l"]);
  });

  it("a drag that starts on empty space deletes each drawing it passes over", async () => {
    const { container, posts } = await mapDrawEdgeMount({
      load: async () =>
        mapDrawEdgeJSON({
          drawings: { top: mapDrawEdgeLine([0, 50, 100, 50]), low: mapDrawEdgeLine([0, 150, 100, 150]), keep: mapDrawEdgeLine([600, 0, 700, 0]) },
        }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toHaveLength(3));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [50, 300], [[50, 150], [50, 50]], [50, 50]);
    expect(mapDrawEdgeIds(container)).toEqual(["keep"]);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.set).toEqual({});
    expect([...posts[0]!.remove].sort()).toEqual(["low", "top"]);
  });

  it("erasing a drawing whose save is still queued sends only its remove", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 300], [400, 300]);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1);
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 250, 300);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.set).toEqual({});
    expect(posts[0]!.remove).toHaveLength(1);
  });
});

describe("B3.16 the eraser's hover mark", () => {
  const hit = (c: HTMLElement) => [...c.querySelectorAll("#map-drawings .erase-hit")].map((el) => el.getAttribute("data-draw-id"));

  it("marks only the latest of two overlapping drawings", async () => {
    const { container } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeTwin }) });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["a1", "b2"]));
    mapDrawEdgeArm(container, "erase");
    fireEvent.mouseMove(mapDrawEdgeSurface(container)!, { clientX: 200, clientY: 100, buttons: 0 });
    expect(hit(container)).toEqual(["b2"]);
  });

  it("moves from one drawing to another, and clears over empty space", async () => {
    const { container } = await mapDrawEdgeMount({
      load: async () =>
        mapDrawEdgeJSON({ drawings: { top: mapDrawEdgeLine([0, 50, 100, 50]), low: mapDrawEdgeLine([0, 150, 100, 150]) } }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toHaveLength(2));
    mapDrawEdgeArm(container, "erase");
    const s = mapDrawEdgeSurface(container)!;
    fireEvent.mouseMove(s, { clientX: 50, clientY: 50, buttons: 0 });
    expect(hit(container)).toEqual(["top"]);
    fireEvent.mouseMove(s, { clientX: 50, clientY: 150, buttons: 0 });
    expect(hit(container)).toEqual(["low"]);
    fireEvent.mouseMove(s, { clientX: 50, clientY: 400, buttons: 0 });
    expect(hit(container)).toEqual([]);
  });

  it("hovering deletes nothing", async () => {
    const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeTwin }) });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["a1", "b2"]));
    mapDrawEdgeArm(container, "erase");
    fireEvent.mouseMove(mapDrawEdgeSurface(container)!, { clientX: 200, clientY: 100, buttons: 0 });
    await mapDrawEdgeSleep(600);
    expect(mapDrawEdgeIds(container)).toEqual(["a1", "b2"]);
    expect(posts).toHaveLength(0);
  });
});

// ---- loading and saving (B3.17 to B3.22)

describe("B3.17 a failed or odd load", () => {
  for (const [what, load] of [
    ["a 500", async () => mapDrawEdgeJSON({ error: "down" }, 500)],
    ["a network error", async () => Promise.reject(new TypeError("network"))],
  ] as Array<[string, () => Promise<Response>]>) {
    it(`${what}: shows no drawings, and drawing and saving still work`, async () => {
      const { container, posts } = await mapDrawEdgeMount({ load });
      expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
      expect(container.querySelector("#agent-map svg#map-drawings")).not.toBeNull();
      mapDrawEdgeArm(container, "line");
      mapDrawEdgeDraw(container, [100, 100], [200, 200]);
      expect(mapDrawEdgeDrawn(container)).toHaveLength(1);
      await waitFor(() => expect(posts).toHaveLength(1));
    });
  }

  for (const [what, body] of [
    ["drawings as an array", { drawings: [mapDrawEdgeLine([0, 0, 1, 1])] }],
    ["drawings as a string", { drawings: "x" }],
    ["only invalid drawings", { drawings: { s: { kind: "star", pts: [0, 0, 1, 1], size: 2 } } }],
  ] as Array<[string, unknown]>) {
    it(`${what}: shows none`, async () => {
      const { container } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON(body) });
      await mapDrawEdgeSleep(100);
      expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    });
  }

  it("loads once, however often the board re-renders", async () => {
    const m = await mapDrawEdgeMount();
    m.rerender(<MapDrawEdgeMap jobs={[...m.jobs]} actions={m.actions} />);
    m.rerender(<MapDrawEdgeMap jobs={[...m.jobs, makeJob({ slug: "b", state: "draft" })]} actions={m.actions} />);
    await mapDrawEdgeSleep(200);
    expect(m.gets()).toBe(1);
  });
});

describe("B3.18 what goes out, and when", () => {
  it("gestures that change nothing never send an empty save", async () => {
    const { container, posts } = await mapDrawEdgeMount({
      load: async () => mapDrawEdgeJSON({ drawings: { l: mapDrawEdgeLine([0, 100, 400, 100]) } }),
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["l"]));
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeGesture(mapDrawEdgeSurface(container)!, [500, 500], [], [501, 501]);
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 300, 400);
    mapDrawEdgeArm(container, "text");
    mapDrawEdgePress(container, 600, 600);
    fireEvent.keyDown(mapDrawEdgeEditor(container)!, { key: "Escape", code: "Escape" });
    await mapDrawEdgeSleep(800);
    expect(posts).toHaveLength(0);
  });

  it("waits 400 ms after the last change, then sends both changes in one save", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    await mapDrawEdgeSleep(250);
    expect(posts).toHaveLength(0);
    mapDrawEdgeDraw(container, [100, 300], [200, 300]);
    await mapDrawEdgeSleep(250);
    expect(posts).toHaveLength(0);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(Object.keys(posts[0]!.set)).toHaveLength(2);
    expect(posts[0]!.remove).toEqual([]);
  });

  it("saving does not wait for a load that never answers", async () => {
    const { container, posts } = await mapDrawEdgeMount({ load: () => new Promise<Response>(() => {}) });
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    await waitFor(() => expect(posts).toHaveLength(1));
  });
});

describe("B3.19 changes made before the load answers", () => {
  it("a drawing made before the answer is still shown after it", async () => {
    let answer!: (r: Response) => void;
    const pending = new Promise<Response>((r) => (answer = r));
    const { container } = await mapDrawEdgeMount({ load: () => pending });
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 300], [400, 300]);
    const [mine] = mapDrawEdgeIds(container);
    expect(mine).toBeDefined();
    await act(async () => {
      answer(mapDrawEdgeJSON({ drawings: { other: { kind: "rect", pts: [0, 0, 10, 10], size: 2 } } }));
      await pending;
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toContain("other"));
    expect(mapDrawEdgeIds(container)).toContain(mine);
  });

  it("a drawing erased before the answer stays erased when the answer names it", async () => {
    let answer!: (r: Response) => void;
    const pending = new Promise<Response>((r) => (answer = r));
    const { container, posts } = await mapDrawEdgeMount({ load: () => pending });
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 300], [400, 300]);
    await waitFor(() => expect(posts).toHaveLength(1));
    const [id, drawing] = Object.entries(posts[0]!.set)[0]!;
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 250, 300);
    expect(mapDrawEdgeIds(container)).toEqual([]);
    await act(async () => {
      answer(mapDrawEdgeJSON({ drawings: { [id]: drawing, other: { kind: "rect", pts: [0, 0, 10, 10], size: 2 } } }));
      await pending;
    });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toContain("other"));
    expect(mapDrawEdgeIds(container)).not.toContain(id);
  });
});

describe("B3.20 a failed save", () => {
  for (const [what, fail] of [
    ["a 500", () => mapDrawEdgeJSON({ error: "no" }, 500)],
    ["a network error", () => Promise.reject(new TypeError("network"))],
  ] as Array<[string, () => Response | Promise<Response>]>) {
    it(`${what}: is not retried on its own, and goes out with the next change`, async () => {
      let failing = true;
      const { container, posts } = await mapDrawEdgeMount({
        save: () => (failing ? fail() : mapDrawEdgeJSON({ ok: true })),
      });
      mapDrawEdgeArm(container, "line");
      mapDrawEdgeDraw(container, [100, 100], [200, 100]);
      await waitFor(() => expect(posts).toHaveLength(1));
      const firstIds = Object.keys(posts[0]!.set);
      expect(firstIds).toHaveLength(1);
      await mapDrawEdgeSleep(900);
      expect(posts).toHaveLength(1);
      failing = false;
      mapDrawEdgeDraw(container, [100, 300], [200, 300]);
      await waitFor(() => expect(posts).toHaveLength(2));
      const secondIds = Object.keys(posts[1]!.set);
      expect(secondIds).toHaveLength(2);
      expect(secondIds).toContain(firstIds[0]);
    });
  }

  it("goes out again on unmount with no further change", async () => {
    let failing = true;
    const { container, posts, unmount } = await mapDrawEdgeMount({
      save: () => (failing ? mapDrawEdgeJSON({ error: "no" }, 500) : mapDrawEdgeJSON({ ok: true })),
    });
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    await waitFor(() => expect(posts).toHaveLength(1));
    await mapDrawEdgeSleep(300);
    failing = false;
    unmount();
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1]).toEqual(posts[0]);
  });
});

describe("B3.21 unmounting", () => {
  it("sends queued ops at once, before the 400 ms timer", async () => {
    const { container, posts, unmount } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    expect(posts).toHaveLength(0);
    unmount();
    await waitFor(() => expect(posts).toHaveLength(1), { timeout: 300 });
    expect(Object.keys(posts[0]!.set)).toHaveLength(1);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(1);
  });

  it("sends nothing with nothing queued", async () => {
    const { unmount, posts } = await mapDrawEdgeMount();
    unmount();
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });
});

describe("B3.22 the unsaved record", () => {
  const record = () => JSON.parse(localStorage.getItem("kraken.mapDrawUnsaved") ?? "null") as MapDrawEdgePost | null;

  it("holds a new drawing before its save goes out, and drops it once the save succeeds", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    await waitFor(() => expect(Object.keys(record()?.set ?? {})).toHaveLength(1), { timeout: 300 });
    expect(posts).toHaveLength(0);
    await waitFor(() => expect(posts).toHaveLength(1));
    await waitFor(() => expect(localStorage.getItem("kraken.mapDrawUnsaved")).toBeNull());
  });

  it("keeps the ops after a failed save", async () => {
    const { container, posts } = await mapDrawEdgeMount({ save: () => mapDrawEdgeJSON({ error: "read-only" }, 403) });
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    await waitFor(() => expect(posts).toHaveLength(1));
    await mapDrawEdgeSleep(100);
    expect(Object.keys(record()?.set ?? {})).toEqual(Object.keys(posts[0]!.set));
  });

  it("on mount, applies the record over the server's answer and queues it again", async () => {
    localStorage.setItem(
      "kraken.mapDrawUnsaved",
      JSON.stringify({ set: { zz: { kind: "rect", pts: [0, 0, 30, 30], size: 2 } }, remove: ["a1"] }),
    );
    const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeTwin }) });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["b2", "zz"]));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(Object.keys(posts[0]!.set)).toEqual(["zz"]);
    expect(posts[0]!.remove).toEqual(["a1"]);
  });

  it("filters the record's invalid entries before applying it", async () => {
    localStorage.setItem(
      "kraken.mapDrawUnsaved",
      JSON.stringify({
        set: { ok: mapDrawEdgeLine([0, 0, 50, 50]), bad: { kind: "star", pts: [0, 0, 1, 1], size: 2 } },
        remove: [""],
      }),
    );
    const { container, posts } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["ok"]));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({ set: { ok: mapDrawEdgeLine([0, 0, 50, 50]) }, remove: [] });
  });

  it("treats a junk record as empty: the server's answer shows and nothing is sent", async () => {
    localStorage.setItem("kraken.mapDrawUnsaved", "{not json");
    const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeTwin }) });
    await waitFor(() => expect(mapDrawEdgeIds(container)).toEqual(["a1", "b2"]));
    await mapDrawEdgeSleep(700);
    expect(posts).toHaveLength(0);
  });
});

// ---- Amendment 1: AM7 (B3.9a), the cap and invalid new drawings

/** n valid lines far off to the right of where the tests draw, ids d0000… */
function mapDrawEdgeMany(n: number): Record<string, MapDrawEdgeDrawing> {
  const out: Record<string, MapDrawEdgeDrawing> = {};
  for (let i = 0; i < n; i++) out[`d${String(i).padStart(4, "0")}`] = mapDrawEdgeLine([5000, 5000 + i * 3, 5010, 5000 + i * 3], 1);
  return out;
}

/** Arms `tool` and makes one drawing of it in the middle of the screen. */
function mapDrawEdgeMakeOne(c: HTMLElement, tool: string) {
  mapDrawEdgeArm(c, tool);
  if (tool === "text") {
    mapDrawEdgePress(c, 200, 200);
    const ed = mapDrawEdgeEditor(c);
    if (ed) {
      fireEvent.change(ed, { target: { value: "label" } });
      mapDrawEdgeEnter(ed);
    }
    return;
  }
  mapDrawEdgeGesture(mapDrawEdgeSurface(c)!, [150, 150], [[200, 180], [260, 230]], [260, 230]);
}

describe("AM7 at MAX_DRAWINGS", () => {
  for (const tool of ["pen", "line", "rect", "ellipse", "text"]) {
    it(`${tool}: creates nothing, queues nothing, and the tool stays armed`, async () => {
      const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeMany(1000) }) });
      await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(1000));
      mapDrawEdgeMakeOne(container, tool);
      expect(mapDrawEdgeDrawn(container)).toHaveLength(1000);
      expect(mapDrawEdgeDrafts(container)).toHaveLength(0);
      expect(mapDrawEdgeRoot(container).getAttribute("data-tool")).toBe(tool);
      expect(localStorage.getItem("kraken.mapDrawUnsaved")).toBeNull();
      await mapDrawEdgeSleep(600);
      expect(posts).toHaveLength(0);
    });
  }

  it("text: the editor is closed after the refused commit", async () => {
    const { container } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeMany(1000) }) });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(1000));
    mapDrawEdgeMakeOne(container, "text");
    expect(mapDrawEdgeEditor(container)).toBeNull();
  });

  it("at 999, one drawing is created and the next is refused", async () => {
    const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings: mapDrawEdgeMany(999) }) });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(999));
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1000);
    mapDrawEdgeDraw(container, [100, 300], [200, 300]);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1000);
    await waitFor(() => expect(posts).toHaveLength(1));
    await mapDrawEdgeSleep(500);
    expect(posts).toHaveLength(1);
    expect(Object.values(posts[0]!.set)).toEqual([{ kind: "line", pts: [100, 100, 200, 100], size: 3 }]);
  });

  it("erasing still works at the cap, and then a new drawing is created", async () => {
    const drawings = { ...mapDrawEdgeMany(999), target: mapDrawEdgeLine([0, 300, 400, 300]) };
    const { container, posts } = await mapDrawEdgeMount({ load: async () => mapDrawEdgeJSON({ drawings }) });
    await waitFor(() => expect(mapDrawEdgeDrawn(container)).toHaveLength(1000));
    mapDrawEdgeArm(container, "erase");
    mapDrawEdgePress(container, 200, 300);
    expect(mapDrawEdgeIds(container)).not.toContain("target");
    expect(mapDrawEdgeDrawn(container)).toHaveLength(999);
    mapDrawEdgeArm(container, "line");
    mapDrawEdgeDraw(container, [100, 100], [200, 100]);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(1000);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.remove).toEqual(["target"]);
    expect(Object.values(posts[0]!.set)).toEqual([{ kind: "line", pts: [100, 100, 200, 100], size: 3 }]);
  });
});

describe("AM7 a new drawing that would fail validDrawing", () => {
  // zoom 0.25 (the clamp's floor) with the camera panned so that screen x 100
  // is world (100 + 2,500,000) / 0.25 = 10,000,400: past V's 10,000,000
  const far = { x: -2_500_000, y: 0, z: 0.25 };

  for (const tool of ["pen", "line", "rect", "ellipse", "text"]) {
    it(`${tool}: drawn beyond the coordinate bound, creates nothing and stays armed`, async () => {
      const { container, posts } = await mapDrawEdgeMount({ view: far });
      mapDrawEdgeMakeOne(container, tool);
      expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
      expect(mapDrawEdgeDrafts(container)).toHaveLength(0);
      expect(mapDrawEdgeRoot(container).getAttribute("data-tool")).toBe(tool);
      if (tool === "text") expect(mapDrawEdgeEditor(container)).toBeNull();
      expect(localStorage.getItem("kraken.mapDrawUnsaved")).toBeNull();
      await mapDrawEdgeSleep(600);
      expect(posts).toHaveLength(0);
    });
  }
});

// ---- no tool armed (B3.23)

describe("B3.23 after a tool is armed and disarmed", () => {
  it("a background drag pans the view again and creates nothing", async () => {
    const { container, posts } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px"));
    mapDrawEdgeArm(container, "line");
    fireEvent.click(mapDrawEdgeTool(container, "line"));
    const before = mapDrawEdgeWorld(container);
    const root = mapDrawEdgeRoot(container);
    fireEvent.mouseDown(root, { clientX: 1500, clientY: 1500, button: 0, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 1550, clientY: 1520, buttons: 1 });
    fireEvent.mouseMove(window, { clientX: 1600, clientY: 1540, buttons: 1 });
    fireEvent.mouseUp(window, { clientX: 1600, clientY: 1540, button: 0 });
    expect(mapDrawEdgeWorld(container)).not.toBe(before);
    expect(mapDrawEdgeDrawn(container)).toHaveLength(0);
    await mapDrawEdgeSleep(600);
    expect(posts).toHaveLength(0);
  });

  it("Escape with no tool armed empties the selection as before", async () => {
    const { container } = await mapDrawEdgeMount();
    await waitFor(() => expect(mapDrawEdgeCard(container, "a").style.left).toBe("0px"));
    mapDrawEdgeArm(container, "pen");
    fireEvent.click(mapDrawEdgeTool(container, "pen"));
    mapDrawEdgeShiftClick(container, "a");
    expect(mapDrawEdgeSelected(container)).toEqual(["a"]);
    fireEvent.keyDown(mapDrawEdgeRoot(container), { key: "Escape", code: "Escape" });
    expect(mapDrawEdgeSelected(container)).toEqual([]);
  });
});
