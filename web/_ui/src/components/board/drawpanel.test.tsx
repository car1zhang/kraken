import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { DRAW_COLORS, DRAW_UNSAVED_KEY, type DrawOps } from "../../lib/agentmap/drawings.ts";
import { MAP_INK_KEY, MAP_TOOLS_KEY } from "../../lib/agentmap/prefs.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// The drawing tools' panel and ink row (contract Amendment 3): the panel is
// closed at first and opens from the pencil button in the zoom row, the
// buttons show icons with the tool's name as their label, closing the panel
// disarms the tool, and the ink chosen in the panel is stored on each new
// drawing. jsdom has no PointerEvent, so a gesture is a mouseDown on the
// element, then mouseMove/mouseUp on window; the map's offset is (0, 0).

const drawPanelClear = () => {
  for (const k of ["kraken.mapView", "kraken.mapUnsaved", DRAW_UNSAVED_KEY, MAP_TOOLS_KEY, MAP_INK_KEY]) {
    localStorage.removeItem(k);
  }
};
beforeEach(drawPanelClear);
afterEach(() => {
  vi.restoreAllMocks();
  drawPanelClear();
});

const drawPanelActions = (): BoardActions => ({
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

function drawPanelModel(jobs: WebJob[]): BoardModel {
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

function DrawPanelHarness({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <MapView search={search} actions={actions} />;
}

const drawPanelJSON = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** fetch by path: the drawings GET answers `drawings`, every POST is recorded and accepted. */
function drawPanelNet() {
  const net = { posts: [] as DrawOps[], gets: 0, drawings: {} as Record<string, unknown> };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    if (url.pathname === "/api/map/drawings") {
      if (method === "POST") {
        net.posts.push(JSON.parse(String(init?.body)) as DrawOps);
        return drawPanelJSON({ ok: true });
      }
      net.gets++;
      return drawPanelJSON({ drawings: net.drawings });
    }
    if (url.pathname === "/api/map/positions") {
      return method === "POST" ? drawPanelJSON({ ok: true }) : drawPanelJSON({ positions: {} });
    }
    return drawPanelJSON({});
  });
  return net;
}

const drawPanelSleep = (ms: number) =>
  act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });

async function drawPanelOpen() {
  localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
  const net = drawPanelNet();
  const utils = render(<DrawPanelHarness board={drawPanelModel([])} actions={drawPanelActions()} />);
  await waitFor(() => expect(net.gets).toBe(1));
  await drawPanelSleep(20);
  return { ...utils, net };
}

const drawPanelRoot = (c: ParentNode) => c.querySelector<HTMLElement>("#agent-map")!;
const drawPanelToggle = (c: ParentNode) => c.querySelector<HTMLButtonElement>("#map-tools-toggle")!;
const drawPanelTools = (c: ParentNode) => c.querySelector<HTMLElement>("#map-tools");
const drawPanelTool = (c: ParentNode, t: string) =>
  c.querySelector<HTMLButtonElement>(`#map-tools button.map-tool[data-tool="${t}"]`)!;
const drawPanelInks = (c: ParentNode) => [...c.querySelectorAll<HTMLButtonElement>("#map-tools .map-ink")];
const drawPanelInk = (c: ParentNode, color: string | null) =>
  drawPanelInks(c).find((b) => (b.dataset.color ?? null) === color)!;
const drawPanelDrawn = (c: ParentNode) => [...c.querySelectorAll<SVGElement>("svg#map-drawings [data-draw-id]")];

/** A line from (x0, y0) to (x1, y1), screen = world at the pinned camera. */
function drawPanelLine(c: ParentNode, x0: number, y0: number, x1: number, y1: number) {
  const surface = c.querySelector<HTMLElement>("#map-draw-surface")!;
  fireEvent.mouseDown(surface, { clientX: x0, clientY: y0, button: 0, buttons: 1 });
  fireEvent.mouseMove(window, { clientX: x1, clientY: y1, buttons: 1 });
  fireEvent.mouseUp(window, { clientX: x1, clientY: y1, button: 0 });
}

describe("drawpanel: the tool panel", () => {
  it("is closed at first: the pencil button is there, the tools are not", async () => {
    const { container } = await drawPanelOpen();
    const toggle = drawPanelToggle(container);
    expect(toggle).not.toBeNull();
    expect(toggle.closest(".map-corner .map-hud")).not.toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.querySelector("svg")).not.toBeNull();
    expect(drawPanelTools(container)).toBeNull();
    expect(drawPanelRoot(container).hasAttribute("data-tool")).toBe(false);
  });

  it("opens on the pencil, with six icon buttons named by tool and the ink row, and remembers it", async () => {
    const { container } = await drawPanelOpen();
    fireEvent.click(drawPanelToggle(container));
    expect(drawPanelToggle(container).getAttribute("aria-expanded")).toBe("true");
    const tools = drawPanelTools(container)!;
    expect(tools).not.toBeNull();
    const buttons = [...tools.querySelectorAll<HTMLButtonElement>("button.map-tool")];
    expect(buttons.map((b) => b.getAttribute("aria-label"))).toEqual(["pen", "line", "rect", "oval", "text", "erase"]);
    for (const b of buttons) {
      expect(b.querySelector("svg")).not.toBeNull();
      expect(b.textContent?.trim()).toBe("");
    }
    expect(drawPanelInks(container).map((b) => b.dataset.color ?? null)).toEqual([null, ...DRAW_COLORS]);
    expect(localStorage.getItem(MAP_TOOLS_KEY)).toBe("open");
  });

  it("comes up open in a browser that left it open", async () => {
    localStorage.setItem(MAP_TOOLS_KEY, "open");
    const { container } = await drawPanelOpen();
    expect(drawPanelTools(container)).not.toBeNull();
    expect(drawPanelToggle(container).getAttribute("aria-expanded")).toBe("true");
  });

  it("closing it disarms the tool and takes the surface down", async () => {
    localStorage.setItem(MAP_TOOLS_KEY, "open");
    const { container } = await drawPanelOpen();
    fireEvent.click(drawPanelTool(container, "rect"));
    expect(drawPanelRoot(container).getAttribute("data-tool")).toBe("rect");
    fireEvent.click(drawPanelToggle(container));
    expect(drawPanelTools(container)).toBeNull();
    expect(drawPanelRoot(container).hasAttribute("data-tool")).toBe(false);
    expect(container.querySelector("#map-draw-surface")).toBeNull();
    expect(localStorage.getItem(MAP_TOOLS_KEY)).toBeNull();
  });

  it("Escape with a tool armed disarms the tool and leaves the panel open", async () => {
    localStorage.setItem(MAP_TOOLS_KEY, "open");
    const { container } = await drawPanelOpen();
    fireEvent.click(drawPanelTool(container, "pen"));
    fireEvent.keyDown(drawPanelRoot(container), { key: "Escape", code: "Escape" });
    expect(drawPanelRoot(container).hasAttribute("data-tool")).toBe(false);
    expect(drawPanelTools(container)).not.toBeNull();
  });
});

describe("drawpanel: the ink", () => {
  it("is the theme's ink at first: a new drawing carries no color", async () => {
    localStorage.setItem(MAP_TOOLS_KEY, "open");
    const { container, net } = await drawPanelOpen();
    expect(drawPanelInk(container, null).getAttribute("aria-checked")).toBe("true");
    fireEvent.click(drawPanelTool(container, "line"));
    drawPanelLine(container, 10, 10, 50, 40);
    const [el] = drawPanelDrawn(container);
    expect(el!.hasAttribute("data-color")).toBe(false);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    const d = Object.values(net.posts[0]!.set)[0]!;
    expect("color" in d).toBe(false);
  });

  it("a chosen ink is stored on every new drawing, drawn in it, and remembered", async () => {
    localStorage.setItem(MAP_TOOLS_KEY, "open");
    const { container, net } = await drawPanelOpen();
    fireEvent.click(drawPanelInk(container, "red"));
    expect(drawPanelInk(container, "red").getAttribute("aria-checked")).toBe("true");
    expect(drawPanelInk(container, null).getAttribute("aria-checked")).toBe("false");
    expect(localStorage.getItem(MAP_INK_KEY)).toBe("red");
    fireEvent.click(drawPanelTool(container, "rect"));
    drawPanelLine(container, 10, 10, 60, 50);
    fireEvent.click(drawPanelTool(container, "text"));
    fireEvent.mouseDown(container.querySelector("#map-draw-surface")!, { clientX: 100, clientY: 100, button: 0, buttons: 1 });
    const editor = container.querySelector<HTMLTextAreaElement>("textarea#map-text-input")!;
    expect(editor.dataset.color).toBe("red");
    fireEvent.change(editor, { target: { value: "hi" } });
    fireEvent.keyDown(editor, { key: "Enter", code: "Enter" });
    const drawn = drawPanelDrawn(container);
    expect(drawn.map((e) => [e.dataset.kind, e.dataset.color])).toEqual([
      ["rect", "red"],
      ["text", "red"],
    ]);
    await waitFor(() => expect(net.posts.length).toBeGreaterThan(0));
    const set = Object.assign({}, ...net.posts.map((p) => p.set)) as Record<string, { color?: string }>;
    expect(Object.values(set).map((d) => d.color)).toEqual(["red", "red"]);
  });

  it("comes up with the remembered ink, and switching back to the theme's ink forgets it", async () => {
    localStorage.setItem(MAP_TOOLS_KEY, "open");
    localStorage.setItem(MAP_INK_KEY, "cyan");
    const { container } = await drawPanelOpen();
    expect(drawPanelInk(container, "cyan").getAttribute("aria-checked")).toBe("true");
    fireEvent.click(drawPanelInk(container, null));
    expect(localStorage.getItem(MAP_INK_KEY)).toBeNull();
    expect(drawPanelInk(container, null).getAttribute("aria-checked")).toBe("true");
  });

  it("a saved drawing's color is shown, and a bad one is dropped with the drawing", async () => {
    localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
    const net = drawPanelNet();
    net.drawings = {
      a: { kind: "line", pts: [0, 0, 10, 10], size: 3, color: "green" },
      b: { kind: "line", pts: [0, 0, 10, 10], size: 3, color: "#00ff00" },
      c: { kind: "line", pts: [0, 0, 10, 10], size: 3 },
    };
    const { container } = render(<DrawPanelHarness board={drawPanelModel([])} actions={drawPanelActions()} />);
    await waitFor(() => expect(drawPanelDrawn(container)).toHaveLength(2));
    expect(drawPanelDrawn(container).map((e) => [e.dataset.drawId, e.dataset.color ?? null])).toEqual([
      ["a", "green"],
      ["c", null],
    ]);
  });
});
