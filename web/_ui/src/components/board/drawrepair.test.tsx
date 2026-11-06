import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { DRAW_UNSAVED_KEY, readUnsavedDrawOps, type DrawOps } from "../../lib/agentmap/drawings.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// Regression pins for the drawing layer's defects found in review round 1
// and settled by Amendment 2 of the map-drawings contract: the commit trim
// (AM10), one POST in flight (AM12), a refused POST (AM13), the editor
// following the view (AM15), and the editor at unmount and under an input
// method (AM16). jsdom has no PointerEvent, so a gesture is a mouseDown on
// the element, then mouseMove/mouseUp on window; the map's offset is (0, 0).

const drawRepairClear = () => {
  localStorage.removeItem("kraken.mapView");
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem(DRAW_UNSAVED_KEY);
  localStorage.removeItem("kraken.mapTools");
  localStorage.removeItem("kraken.mapInk");
};
beforeEach(drawRepairClear);
afterEach(() => {
  vi.restoreAllMocks();
  drawRepairClear();
});

const drawRepairActions = (): BoardActions => ({
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

function drawRepairModel(jobs: WebJob[]): BoardModel {
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

function DrawRepairHarness({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <MapView search={search} actions={actions} />;
}

const drawRepairJSON = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

interface DrawRepairNet {
  gets: number;
  posts: DrawOps[];
  /** the status the next POSTs answer with, when not held */
  status: number;
  /** while true, every POST waits until `release` answers it */
  hold: boolean;
  release: (status?: number) => void;
  /** what GET /api/map/drawings answers */
  drawings: unknown;
}

function drawRepairNet(): DrawRepairNet {
  const held: Array<(r: Response) => void> = [];
  const net: DrawRepairNet = {
    gets: 0,
    posts: [],
    status: 200,
    hold: false,
    drawings: {},
    release: (status = 200) => {
      const r = held.shift();
      if (r) r(status === 200 ? drawRepairJSON({ ok: true }) : drawRepairJSON({ error: "no" }, status));
    },
  };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://board.local");
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.pathname === "/api/map/drawings") {
      if (method === "POST") {
        net.posts.push(JSON.parse(String(init?.body)) as DrawOps);
        if (net.hold) {
          // a held POST still ends on an abort, as fetch does
          return new Promise<Response>((r, reject) => {
            held.push(r);
            init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
          });
        }
        return net.status === 200 ? drawRepairJSON({ ok: true }) : drawRepairJSON({ error: "no" }, net.status);
      }
      net.gets++;
      return drawRepairJSON({ drawings: net.drawings });
    }
    if (url.pathname === "/api/map/positions") {
      return method === "POST" ? drawRepairJSON({ ok: true }) : drawRepairJSON({ positions: {} });
    }
    return drawRepairJSON({});
  });
  return net;
}

const drawRepairSleep = (ms: number) =>
  act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });

async function drawRepairOpen() {
  localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
  // the tool panel is closed to begin with (AM18); these tests arm tools, so it starts open
  localStorage.setItem("kraken.mapTools", "open");
  const net = drawRepairNet();
  const utils = render(<DrawRepairHarness board={drawRepairModel([])} actions={drawRepairActions()} />);
  await waitFor(() => expect(net.gets).toBe(1));
  await drawRepairSleep(20);
  return { ...utils, net };
}

const drawRepairRoot = (c: ParentNode) => c.querySelector<HTMLElement>("#agent-map")!;
const drawRepairSurface = (c: ParentNode) => c.querySelector<HTMLElement>("#map-draw-surface")!;
const drawRepairArm = (c: ParentNode, t: string) =>
  fireEvent.click(c.querySelector<HTMLButtonElement>(`#map-tools button.map-tool[data-tool="${t}"]`)!);
const drawRepairEditor = (c: ParentNode) => c.querySelector<HTMLTextAreaElement>("textarea#map-text-input");
const drawRepairIds = (c: ParentNode) =>
  [...c.querySelectorAll<SVGElement>("svg#map-drawings [data-draw-id]")].map((e) => e.getAttribute("data-draw-id")!);

/** A line from (x0, y0) to (x1, y1), screen = world at the pinned camera. */
function drawRepairLine(c: ParentNode, x0: number, y0: number, x1: number, y1: number) {
  fireEvent.mouseDown(drawRepairSurface(c), { clientX: x0, clientY: y0, button: 0, buttons: 1 });
  fireEvent.mouseMove(window, { clientX: x1, clientY: y1, buttons: 1 });
  fireEvent.mouseUp(window, { clientX: x1, clientY: y1, button: 0 });
}

function drawRepairOpenEditor(c: ParentNode, x: number, y: number) {
  fireEvent.mouseDown(drawRepairSurface(c), { clientX: x, clientY: y, button: 0, buttons: 1 });
  fireEvent.mouseUp(window, { clientX: x, clientY: y, button: 0 });
  return drawRepairEditor(c)!;
}

describe("drawrepair: the commit trim strips the server's whitespace (AM10)", () => {
  it("a label of U+0085 alone makes nothing and sends nothing", async () => {
    const { container, net } = await drawRepairOpen();
    drawRepairArm(container, "text");
    const ed = drawRepairOpenEditor(container, 100, 100);
    fireEvent.change(ed, { target: { value: "\u0085" } });
    fireEvent.keyDown(ed, { key: "Enter" });
    expect(drawRepairEditor(container)).toBeNull();
    expect(drawRepairIds(container)).toHaveLength(0);
    await drawRepairSleep(450);
    expect(net.posts).toHaveLength(0);
  });

  it("U+0085 around a label is trimmed off", async () => {
    const { container, net } = await drawRepairOpen();
    drawRepairArm(container, "text");
    const ed = drawRepairOpenEditor(container, 100, 100);
    fireEvent.change(ed, { target: { value: "\u0085hi\u0085" } });
    fireEvent.keyDown(ed, { key: "Enter" });
    await waitFor(() => expect(net.posts).toHaveLength(1));
    expect(Object.values(net.posts[0]!.set).map((d) => d.text)).toEqual(["hi"]);
  });
});

describe("drawrepair: one drawings POST in flight (AM12)", () => {
  it("a save due while a POST is in flight waits for its answer, then carries every op", async () => {
    const { container, net } = await drawRepairOpen();
    net.hold = true;
    drawRepairArm(container, "line");
    drawRepairLine(container, 100, 100, 200, 100);
    await drawRepairSleep(450);
    expect(net.posts).toHaveLength(1);
    drawRepairLine(container, 100, 200, 200, 200);
    await drawRepairSleep(450);
    expect(net.posts).toHaveLength(1);
    net.hold = false;
    await act(async () => net.release());
    await waitFor(() => expect(net.posts).toHaveLength(2));
    const first = Object.keys(net.posts[0]!.set);
    const second = Object.keys(net.posts[1]!.set);
    expect(first).toHaveLength(1);
    expect(second).toHaveLength(1);
    expect(second[0]).not.toBe(first[0]);
  });

  it("an erase made while the drawing's own POST is in flight never lets it come back", async () => {
    const { container, net } = await drawRepairOpen();
    net.hold = true;
    drawRepairArm(container, "line");
    drawRepairLine(container, 100, 100, 200, 100);
    await drawRepairSleep(450);
    expect(net.posts).toHaveLength(1);
    const [id] = Object.keys(net.posts[0]!.set);
    drawRepairArm(container, "erase");
    fireEvent.mouseDown(drawRepairSurface(container), { clientX: 150, clientY: 100, button: 0, buttons: 1 });
    fireEvent.mouseUp(window, { clientX: 150, clientY: 100, button: 0 });
    expect(drawRepairIds(container)).toHaveLength(0);
    await drawRepairSleep(450);
    // the set's POST now fails; whatever goes out after it must not set the erased drawing again
    await act(async () => {
      while (net.posts.length) {
        const before = net.posts.length;
        net.release(500);
        await new Promise((r) => setTimeout(r, 20));
        if (net.posts.length === before) break;
      }
    });
    net.hold = false;
    await act(async () => {
      for (let i = 0; i < 4; i++) {
        net.release(200);
        await new Promise((r) => setTimeout(r, 20));
      }
    });
    expect(readUnsavedDrawOps().set[id!]).toBeUndefined();
    const last = net.posts.at(-1)!;
    expect(last.set[id!]).toBeUndefined();
  });
});

describe("drawrepair: a POST that never answers is given up on (AM17)", () => {
  it("is aborted after the limit, its ops stay queued, and the save that came due goes out", async () => {
    const { container, net } = await drawRepairOpen();
    vi.useFakeTimers();
    try {
      net.hold = true;
      drawRepairArm(container, "line");
      drawRepairLine(container, 100, 100, 200, 100);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(450);
      });
      expect(net.posts).toHaveLength(1);
      drawRepairLine(container, 100, 200, 200, 200);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(450);
      });
      expect(net.posts).toHaveLength(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(net.posts).toHaveLength(2);
      expect(Object.keys(net.posts[1]!.set)).toHaveLength(2);
      expect(Object.keys(readUnsavedDrawOps().set)).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("drawrepair: a refused POST is dropped and the map reloads (AM13)", () => {
  it("a 400 drops its ops from the record and shows the server's answer again", async () => {
    const { container, net } = await drawRepairOpen();
    net.status = 400;
    drawRepairArm(container, "line");
    drawRepairLine(container, 100, 100, 200, 100);
    net.drawings = { srv1: { kind: "line", pts: [0, 0, 50, 50], size: 2 } };
    await waitFor(() => expect(net.posts).toHaveLength(1));
    await waitFor(() => expect(net.gets).toBe(2));
    await waitFor(() => expect(drawRepairIds(container)).toEqual(["srv1"]));
    expect(localStorage.getItem(DRAW_UNSAVED_KEY)).toBeNull();
    // a refused batch is not re-sent with the next change
    net.status = 200;
    drawRepairLine(container, 100, 200, 200, 200);
    await waitFor(() => expect(net.posts).toHaveLength(2));
    expect(Object.keys(net.posts[1]!.set)).toHaveLength(1);
  });

  it("a 403 is a failure, not a refusal: its ops stay queued", async () => {
    const { container, net } = await drawRepairOpen();
    net.status = 403;
    drawRepairArm(container, "line");
    drawRepairLine(container, 100, 100, 200, 100);
    await waitFor(() => expect(net.posts).toHaveLength(1));
    await drawRepairSleep(30);
    expect(net.gets).toBe(1);
    expect(Object.keys(readUnsavedDrawOps().set)).toHaveLength(1);
  });
});

describe("drawrepair: the open editor stays on its world point (AM15)", () => {
  it("follows a wheel pan and a zoom, and the drawing keeps the open-time point and size", async () => {
    const { container, net } = await drawRepairOpen();
    drawRepairArm(container, "text");
    const ed = drawRepairOpenEditor(container, 100, 120);
    expect(ed.style.left).toBe("100px");
    expect(ed.style.top).toBe("120px");
    expect(ed.style.fontSize).toBe("18px");
    fireEvent.wheel(drawRepairRoot(container), { deltaX: 30, deltaY: 10 });
    expect(ed.style.left).toBe("70px");
    expect(ed.style.top).toBe("110px");
    fireEvent.click(container.querySelector<HTMLButtonElement>('button[aria-label="zoom in"]')!);
    expect(ed.style.fontSize).toBe("22.5px");
    fireEvent.change(ed, { target: { value: "x" } });
    fireEvent.keyDown(ed, { key: "Enter" });
    await waitFor(() => expect(net.posts).toHaveLength(1));
    const [d] = Object.values(net.posts[0]!.set);
    expect(d!.pts).toEqual([100, 120]);
    expect(d!.size).toBe(18);
  });
});

describe("drawrepair: the editor at unmount and under an input method (AM16)", () => {
  it("unmounting with the editor open commits it in the unmount's save", async () => {
    const { container, net, unmount } = await drawRepairOpen();
    drawRepairArm(container, "text");
    const ed = drawRepairOpenEditor(container, 100, 100);
    fireEvent.change(ed, { target: { value: "  hello  " } });
    unmount();
    await waitFor(() => expect(net.posts).toHaveLength(1));
    expect(Object.values(net.posts[0]!.set).map((d) => d.text)).toEqual(["hello"]);
  });

  it("Enter while composing does not commit", async () => {
    const { container } = await drawRepairOpen();
    drawRepairArm(container, "text");
    const ed = drawRepairOpenEditor(container, 100, 100);
    fireEvent.change(ed, { target: { value: "にほ" } });
    fireEvent.keyDown(ed, { key: "Enter", isComposing: true });
    expect(drawRepairEditor(container)).not.toBeNull();
    expect(drawRepairIds(container)).toHaveLength(0);
  });
});
