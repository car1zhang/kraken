import { fireEvent, render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LogEntry, LogPage } from "../../api/types.ts";
import { LogView } from "./LogView.tsx";

vi.mock("../../api/client.ts", () => ({ fetchLog: vi.fn() }));
const { fetchLog } = await import("../../api/client.ts");
const mockFetchLog = vi.mocked(fetchLog);

const entry = (msg: string, level = "info"): LogEntry => ({
  ts: 1_700_000_000_000,
  level,
  src: "daemon",
  event: "tick",
  msg,
});

const page = (n: number, prefix: string, prev?: string): LogPage => ({
  file: "events",
  prev,
  entries: Array.from({ length: n }, (_, i) => entry(`${prefix}-${i}`)),
});

/**
 * jsdom has no layout, so every geometry property reads 0 and the scroll math
 * has nothing to act on. These stubs synthesize just enough of one: a row is
 * 20px tall, the viewport is 100px, and scrollTop is a real read/write cell.
 *
 * The CLAMP matters and is not decoration. A browser pins scrollTop into
 * [0, scrollHeight - clientHeight], and without it `preserve` and `bottom`
 * are algebraically the SAME number for a reader who is exactly at the
 * bottom — `prevTop + ΔH` = `(prevH - clientH) + ΔH` = `nextH - clientH` —
 * so an unclamped stub cannot tell the two branches apart at all. What
 * separates them is TAIL_FOLLOW_PX: a reader who is merely NEAR the bottom
 * gets snapped flush to it by tail-follow, and does not by preserve.
 */
const ROW_PX = 20;
const VIEWPORT_PX = 100;
function stubGeometry(el: HTMLElement) {
  const height = () => el.querySelectorAll(".log-row").length * ROW_PX;
  let top = 0;
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => top,
    set: (v: number) => {
      top = Math.max(0, Math.min(v, Math.max(0, height() - VIEWPORT_PX)));
    },
  });
  Object.defineProperty(el, "scrollHeight", { configurable: true, get: height });
  Object.defineProperty(el, "clientHeight", {
    configurable: true,
    get: () => VIEWPORT_PX,
  });
  return el;
}
/** Where a browser parks a reader who is scrolled all the way down. */
const bottomFor = (rows: number) => rows * ROW_PX - VIEWPORT_PX;

beforeEach(() => {
  mockFetchLog.mockReset();
});

describe("<LogView>", () => {
  it("shows the empty state before anything has loaded", () => {
    mockFetchLog.mockResolvedValue({ file: "events", entries: [] });
    const { container } = render(<LogView active={false} />);
    expect(container.querySelector(".log-empty")?.textContent).toBe(
      "no log entries yet",
    );
  });

  it("still shows the empty state once a page LOADS with zero entries", async () => {
    // A fresh kraken.db: the fetch succeeds, so `chain` is non-empty, but
    // there is nothing to read. Vanilla drew a bare `events` divider here.
    mockFetchLog.mockResolvedValue({ file: "events", entries: [] });
    const { container } = render(<LogView active />);

    await waitFor(() => expect(mockFetchLog).toHaveBeenCalled());
    await waitFor(() =>
      expect(container.querySelector(".log-empty")).not.toBeNull(),
    );
    expect(container.querySelector(".log-file-divider")).toBeNull();
  });

  it("renders a row whose level, msg and ts are all absent", async () => {
    // every one of those is optional in api/types.ts
    mockFetchLog.mockResolvedValue({ file: "events", entries: [{}] });
    const { container } = render(<LogView active />);

    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(1),
    );
    const row = container.querySelector(".log-row")!;
    expect(row.className).toBe("log-row lvl-");
    expect(row.querySelector(".log-ts")?.textContent).toBe("");
    expect(row.querySelector(".log-msg")?.textContent).toBe("");
  });

  it("renders one .lvl-<level> row per entry, plus a file divider", async () => {
    mockFetchLog.mockResolvedValue({
      file: "events",
      entries: [entry("all good"), entry("it broke", "error")],
    });
    const { container } = render(<LogView active />);

    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(2),
    );
    expect(container.querySelectorAll(".log-row.lvl-info")).toHaveLength(1);
    const err = container.querySelector(".log-row.lvl-error");
    expect(err).not.toBeNull();
    expect(err?.querySelector(".log-msg")?.textContent).toBe("it broke");
    expect(err?.querySelector(".log-src")?.textContent).toBe("daemon");
    expect(container.querySelector(".log-file-divider")?.textContent).toBe("events");
  });

  it("does not poll while it is not the active view", async () => {
    mockFetchLog.mockResolvedValue({ file: "events", entries: [] });
    const { container } = render(<LogView active={false} />);
    await new Promise((r) => setTimeout(r, 20));
    expect(mockFetchLog).not.toHaveBeenCalled();
    expect(container.querySelector("main.logview")?.hasAttribute("hidden")).toBe(true);
  });

  it("offers the older-page affordance only while a prev cursor exists", async () => {
    mockFetchLog.mockResolvedValue({
      file: "events",
      prev: "events:100",
      entries: [entry("a")],
    });
    const { container } = render(<LogView active />);
    await waitFor(() =>
      expect(container.querySelector(".log-more")?.hasAttribute("hidden")).toBe(false),
    );
  });

  it("pins to the bottom on first load (tail-follow)", async () => {
    mockFetchLog.mockResolvedValue(page(50, "new"));
    const { container } = render(<LogView active />);
    const scroll = stubGeometry(container.querySelector<HTMLElement>(".log-scroll")!);

    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(50),
    );
    expect(scroll.scrollTop).toBe(bottomFor(50));
  });

  // usePoll forces an out-of-band tick on visibilitychange (document.hidden is
  // false in jsdom), which is how these two drive a SECOND refresh without
  // waiting out the 3s interval.
  const forceRefresh = () => fireEvent(document, new Event("visibilitychange"));

  it("tail-follows a refresh for a reader who is NEAR the bottom", async () => {
    mockFetchLog.mockResolvedValue(page(50, "a"));
    const { container } = render(<LogView active />);
    const scroll = stubGeometry(container.querySelector<HTMLElement>(".log-scroll")!);
    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(50),
    );

    // 20px shy of the bottom — inside TAIL_FOLLOW_PX, so still "watching the
    // tail". This is the ONLY configuration that separates the two branches:
    // exactly at the bottom they compute the same number.
    scroll.scrollTop = bottomFor(50) - 20;
    mockFetchLog.mockResolvedValue(page(60, "b"));
    forceRefresh();

    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(60),
    );
    // snapped flush to the new bottom, not left 20px above it
    expect(scroll.scrollTop).toBe(bottomFor(60));
  });

  it("does NOT yank a reader who has scrolled up to the bottom", async () => {
    mockFetchLog.mockResolvedValue(page(50, "a"));
    const { container } = render(<LogView active />);
    const scroll = stubGeometry(container.querySelector<HTMLElement>(".log-scroll")!);
    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(50),
    );

    // parked mid-history: 1000 - 400 - 100 = 500, far outside TAIL_FOLLOW_PX
    scroll.scrollTop = 400;
    mockFetchLog.mockResolvedValue(page(60, "b"));
    forceRefresh();

    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(60),
    );
    // NOT 1200 — the reader stays put rather than being dragged to the tail.
    // The delta correction itself is vanilla parity (app.js:3877-3879); see
    // athena-edge N2 on whether an append deserves a correction at all.
    expect(scroll.scrollTop).toBe(400 + (60 - 50) * ROW_PX);
  });

  it("keeps the reader on the same line across a PREPENDED older page", async () => {
    mockFetchLog.mockImplementation(async (file?: string) =>
      file === "events:100"
        ? { file: "events:100", entries: page(10, "old").entries }
        : { file: "events", prev: "events:100", entries: page(50, "new").entries },
    );

    const { container } = render(<LogView active />);
    const scroll = stubGeometry(container.querySelector<HTMLElement>(".log-scroll")!);
    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(50),
    );

    // the reader scrolls to the very top, which is what pulls the older page
    const prevHeight = 50 * ROW_PX;
    scroll.scrollTop = 0;
    fireEvent.scroll(scroll);

    await waitFor(() =>
      expect(container.querySelectorAll(".log-row")).toHaveLength(60),
    );
    const nextHeight = 60 * ROW_PX;
    // prevTop + (scrollHeight - prevHeight) — the whole point: the line they
    // were reading moved down by exactly the height the prepend added, so
    // they must move down with it rather than be left staring at new content
    expect(scroll.scrollTop).toBe(0 + (nextHeight - prevHeight));
    expect(container.querySelectorAll(".log-file-divider")).toHaveLength(2);
    // oldest first
    expect(container.querySelectorAll(".log-file-divider")[0]?.textContent).toBe(
      "events:100",
    );
  });
});
