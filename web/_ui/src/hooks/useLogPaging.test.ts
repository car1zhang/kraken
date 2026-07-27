import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LogEntry, LogPage } from "../api/types.ts";
import {
  isAtBottom,
  logPageChanged,
  preservedScrollTop,
  useLogPaging,
} from "./useLogPaging.ts";

vi.mock("../api/client.ts", () => ({ fetchLog: vi.fn() }));
const { fetchLog } = await import("../api/client.ts");
const mockFetchLog = vi.mocked(fetchLog);

const entry = (msg: string, ts = 1): LogEntry => ({
  ts,
  level: "info",
  src: "daemon",
  event: "tick",
  msg,
});

beforeEach(() => {
  mockFetchLog.mockReset();
});

describe("preservedScrollTop", () => {
  it("shifts the viewport down by exactly the height the prepend added", () => {
    // reader was 120px down a 1000px column; an older page added 400px above
    expect(preservedScrollTop(120, 1000, 1400)).toBe(520);
  });

  it("is a no-op when nothing was added", () => {
    expect(preservedScrollTop(120, 1000, 1000)).toBe(120);
  });

  it("does not clamp — the caller assigns to scrollTop, which clamps itself", () => {
    expect(preservedScrollTop(0, 1400, 1000)).toBe(-400);
  });
});

describe("isAtBottom", () => {
  it("counts within 40px of the bottom as watching the tail", () => {
    expect(isAtBottom({ scrollHeight: 1000, scrollTop: 461, clientHeight: 500 })).toBe(
      true,
    );
    expect(isAtBottom({ scrollHeight: 1000, scrollTop: 460, clientHeight: 500 })).toBe(
      false,
    );
  });
});

describe("logPageChanged", () => {
  it("is true the first time a file is seen", () => {
    expect(logPageChanged(undefined, [])).toBe(true);
  });

  it("catches a count change", () => {
    expect(logPageChanged([entry("a")], [entry("a"), entry("b")])).toBe(true);
  });

  it("catches a SLID WINDOW that a count-only compare would miss", () => {
    // store mode pins the page size: the count is identical on both sides and
    // only the newest entry gives the slide away. This is the bug fix.
    const before = [entry("1"), entry("2"), entry("3")];
    const after = [entry("2"), entry("3"), entry("4")];
    expect(before.length).toBe(after.length); // a count compare says "unchanged"
    expect(logPageChanged(before, after)).toBe(true);
  });

  it("is false when the page is genuinely identical", () => {
    expect(logPageChanged([entry("a"), entry("b")], [entry("a"), entry("b")])).toBe(
      false,
    );
  });

  it("is false for two empty pages", () => {
    expect(logPageChanged([], [])).toBe(false);
  });
});

describe("useLogPaging", () => {
  it("loads the newest page on refresh", async () => {
    mockFetchLog.mockResolvedValue({
      file: "events",
      prev: "events:100",
      entries: [entry("hello")],
    } satisfies LogPage);

    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    expect(mockFetchLog).toHaveBeenCalledWith(undefined, undefined);
    expect(result.current.chain).toEqual(["events"]);
    expect(result.current.data["events"]?.entries).toHaveLength(1);
    expect(result.current.hasMore).toBe(true);
  });

  it("walks the prev cursor and PREPENDS the older page", async () => {
    mockFetchLog.mockResolvedValueOnce({
      file: "events",
      prev: "events:100",
      entries: [entry("newest")],
    });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    mockFetchLog.mockResolvedValueOnce({
      file: "events:100",
      entries: [entry("older")],
    });
    await act(() => result.current.loadOlder());

    // oldest → newest, and the OLD page went on the front
    expect(mockFetchLog).toHaveBeenLastCalledWith("events:100");
    expect(result.current.chain).toEqual(["events:100", "events"]);
    // the head has no prev of its own, so history is exhausted
    expect(result.current.hasMore).toBe(false);
  });

  it("refuses to re-fetch a page already in the chain", async () => {
    mockFetchLog.mockResolvedValueOnce({
      file: "events",
      prev: "events:100",
      entries: [entry("newest")],
    });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    mockFetchLog.mockResolvedValueOnce({ file: "events:100", entries: [entry("old")] });
    await act(() => result.current.loadOlder());
    const calls = mockFetchLog.mock.calls.length;

    await act(() => result.current.loadOlder());
    expect(mockFetchLog.mock.calls.length).toBe(calls);
  });

  it("stops at the start of history", async () => {
    mockFetchLog.mockResolvedValue({ file: "events", entries: [entry("only")] });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());
    expect(result.current.hasMore).toBe(false);

    const calls = mockFetchLog.mock.calls.length;
    await act(() => result.current.loadOlder());
    expect(mockFetchLog.mock.calls.length).toBe(calls);
  });

  it('treats the server\'s empty-string prev as the start of history', async () => {
    // internal/api/logs.go writes `prev` with no omitempty, so the real wire
    // value at the start of history is "" rather than an absent key.
    mockFetchLog.mockResolvedValue({ file: "events", prev: "", entries: [entry("a")] });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    expect(result.current.hasMore).toBe(false);
    const calls = mockFetchLog.mock.calls.length;
    await act(() => result.current.loadOlder());
    expect(mockFetchLog.mock.calls.length).toBe(calls);
  });

  it("releases the loading latch when loadOlder rejects, so a retry works", async () => {
    mockFetchLog.mockResolvedValueOnce({
      file: "events",
      prev: "events:100",
      entries: [entry("newest")],
    });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    mockFetchLog.mockRejectedValueOnce(new Error("network"));
    await act(() => result.current.loadOlder());
    expect(result.current.chain).toEqual(["events"]);

    // a wedged latch would make the page unreachable for the rest of the session
    mockFetchLog.mockResolvedValueOnce({ file: "events:100", entries: [entry("old")] });
    await act(() => result.current.loadOlder());
    expect(result.current.chain).toEqual(["events:100", "events"]);
  });

  it("adopts a rotation without dropping the previous file's entries", async () => {
    mockFetchLog.mockResolvedValueOnce({ file: "events", entries: [entry("a")] });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    mockFetchLog.mockResolvedValueOnce({ file: "events-2", entries: [entry("b")] });
    await act(() => result.current.refresh());

    expect(result.current.chain).toEqual(["events", "events-2"]);
    expect(result.current.data["events"]?.entries).toHaveLength(1);
  });

  it("survives an unreachable server without losing what it has", async () => {
    mockFetchLog.mockResolvedValueOnce({ file: "events", entries: [entry("a")] });
    const { result } = renderHook(() => useLogPaging(false));
    await act(() => result.current.refresh());

    mockFetchLog.mockRejectedValueOnce(new Error("network"));
    await act(() => result.current.refresh());

    expect(result.current.chain).toEqual(["events"]);
  });

  it("does not poll while the log view is not active", async () => {
    mockFetchLog.mockResolvedValue({ file: "events", entries: [] });
    renderHook(() => useLogPaging(false));
    await new Promise((r) => setTimeout(r, 20));
    expect(mockFetchLog).not.toHaveBeenCalled();
  });

  it("polls immediately once the log view becomes active", async () => {
    mockFetchLog.mockResolvedValue({ file: "events", entries: [] });
    renderHook(() => useLogPaging(true));
    await waitFor(() => expect(mockFetchLog).toHaveBeenCalled());
  });
});
