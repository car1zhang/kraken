import { act, render, renderHook, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "../api/types.ts";
import { ArchiveDrawer } from "../components/board/ArchiveDrawer.tsx";
import { BoardView, type BoardActions } from "../components/board/BoardView.tsx";
import { bucketJobs } from "../lib/format.ts";
import { addArchivedGoalCounts } from "../lib/goals.ts";
import { jobKey } from "../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../test/fixtures.ts";
import { useBoard, type BoardModel } from "./useBoard.ts";
import { useJobSearch } from "./useJobSearch.ts";

// archiveOnDemand.test.tsx — the archive off the board poll: the poll asks
// for the board without archived cards, and useBoard fetches them from
// /api/archived only once something needs them.

vi.mock("../api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../api/client.ts")>()),
  fetchBoard: vi.fn(),
  fetchArchived: vi.fn(),
}));

import { fetchArchived, fetchBoard } from "../api/client.ts";

const boardMock = vi.mocked(fetchBoard);
const archivedMock = vi.mocked(fetchArchived);

const live = makeJob({ slug: "live", state: "working", live: true });
const old1 = makeJob({ slug: "old", state: "archived", archived: true, display_slug: "old" });
const old2 = makeJob({ slug: "old-2", state: "archived", archived: true, display_slug: "old" });

const boardWith = (over: Partial<WebBoard> = {}): WebBoard =>
  makeBoard({ daemon: makeDaemon({ pid: 1, last_tick: 1 }), jobs: [live], archived_count: 2, ...over });

beforeEach(() => {
  boardMock.mockReset();
  archivedMock.mockReset();
  boardMock.mockResolvedValue(boardWith());
  archivedMock.mockResolvedValue({ jobs: [old1, old2] });
});

afterEach(() => {
  localStorage.removeItem("kraken-archive-open");
});

describe("useBoard: the archive is fetched on demand", () => {
  it("polls the board and does not fetch the archive until asked", async () => {
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.board).toBeDefined());
    expect(archivedMock).not.toHaveBeenCalled();
    expect(result.current.archived).toEqual([]);
    expect(result.current.archivedCount).toBe(2);
    expect(result.current.archivedLoaded).toBe(false);
    expect(result.current.byKey.has(jobKey(old1))).toBe(false);
  });

  it("fetches once asked, and the cards join the model", async () => {
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.board).toBeDefined());
    act(() => result.current.needArchived?.());
    await waitFor(() => expect(result.current.archived).toHaveLength(2));
    expect(archivedMock).toHaveBeenCalledTimes(1);
    expect(result.current.archivedLoaded).toBe(true);
    expect(result.current.byKey.get(jobKey(old2))).toBe(old2);
    // the active cards are still bucketed as before, and the raw payload is untouched
    expect(result.current.byColumn.working).toEqual([live]);
    expect(result.current.board?.jobs).toEqual([live]);
    // asking again fetches nothing new
    act(() => result.current.needArchived?.());
    act(() => result.current.needArchived?.());
    expect(archivedMock).toHaveBeenCalledTimes(1);
  });

  it("fetches again when the board's count moves", async () => {
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.board).toBeDefined());
    act(() => result.current.needArchived?.());
    await waitFor(() => expect(result.current.archived).toHaveLength(2));
    boardMock.mockResolvedValue(boardWith({ archived_count: 1 }));
    archivedMock.mockResolvedValue({ jobs: [old1] });
    await act(async () => {
      await result.current.refetch();
    });
    await waitFor(() => expect(result.current.archived).toEqual([old1]));
    expect(archivedMock).toHaveBeenCalledTimes(2);
    expect(result.current.archivedCount).toBe(1);
  });

  it("fetches again after a write, and not when the archive was never asked for", async () => {
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.board).toBeDefined());
    await act(async () => {
      await result.current.mutate(async () => {});
    });
    expect(archivedMock).not.toHaveBeenCalled();
    act(() => result.current.needArchived?.());
    await waitFor(() => expect(archivedMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.archivedLoaded).toBe(true));
    await act(async () => {
      await result.current.mutate(async () => {});
    });
    await waitFor(() => expect(archivedMock).toHaveBeenCalledTimes(2));
    // a refused write reads it again too
    await act(async () => {
      await result.current.mutate(async () => {
        throw new Error("refused");
      });
    });
    await waitFor(() => expect(archivedMock).toHaveBeenCalledTimes(3));
  });

  it("retries a failed fetch", async () => {
    archivedMock.mockRejectedValueOnce(new Error("down"));
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.board).toBeDefined());
    act(() => result.current.needArchived?.());
    await waitFor(() => expect(result.current.archived).toHaveLength(2), { timeout: 6000 });
    expect(archivedMock).toHaveBeenCalledTimes(2);
  }, 10000);

  it("reads the archive off the board from a server older than the count", async () => {
    boardMock.mockResolvedValue(makeBoard({ daemon: makeDaemon({ pid: 1, last_tick: 1 }), jobs: [live, old1] }));
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.archived).toEqual([old1]));
    act(() => result.current.needArchived?.());
    expect(archivedMock).not.toHaveBeenCalled();
    expect(result.current.archivedCount).toBe(1);
    expect(result.current.archivedLoaded).toBe(true);
  });

  it("never lists a card twice when an empty archive is asked for", async () => {
    boardMock.mockResolvedValue(boardWith({ archived_count: 0 }));
    archivedMock.mockResolvedValue({ jobs: [] });
    const { result } = renderHook(() => useBoard());
    await waitFor(() => expect(result.current.board).toBeDefined());
    act(() => result.current.needArchived?.());
    await waitFor(() => expect(result.current.archivedLoaded).toBe(true));
    expect(result.current.archived).toEqual([]);
    expect([...result.current.byKey.keys()]).toEqual([jobKey(live)]);
  });
});

const actions = (): BoardActions => ({
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

describe("ArchiveDrawer: before its cards are loaded", () => {
  it("shows the archive's size and can be opened, which asks for the cards", async () => {
    const onNeed = vi.fn();
    const { container } = render(<ArchiveDrawer archived={[]} actions={actions()} count={7} onNeed={onNeed} />);
    const head = container.querySelector("#archive-toggle") as HTMLButtonElement;
    expect(head.querySelector(".n")).toHaveTextContent("7");
    expect(head.disabled).toBe(false);
    expect(onNeed).not.toHaveBeenCalled();
    expect(container.querySelector(".archive-loading")).toBeNull();
    await userEvent.click(head);
    expect(onNeed).toHaveBeenCalled();
    expect(container.querySelector(".archive-loading")).not.toBeNull();
  });

  it("asks at once when it starts open, and drops the loading line once cards arrive", () => {
    localStorage.setItem("kraken-archive-open", "1");
    const onNeed = vi.fn();
    const { container, rerender } = render(
      <ArchiveDrawer archived={[]} actions={actions()} count={2} onNeed={onNeed} />,
    );
    expect(onNeed).toHaveBeenCalled();
    expect(container.querySelector(".archive-loading")).not.toBeNull();
    rerender(<ArchiveDrawer archived={[old1, old2]} actions={actions()} count={2} onNeed={onNeed} />);
    expect(container.querySelector(".archive-loading")).toBeNull();
    expect(container.querySelectorAll(".archive-cards > *")).toHaveLength(2);
  });

  it("stays disabled over an empty archive", () => {
    const { container } = render(<ArchiveDrawer archived={[]} actions={actions()} count={0} onNeed={vi.fn()} />);
    const head = container.querySelector("#archive-toggle") as HTMLButtonElement;
    expect(head.disabled).toBe(true);
    expect(head.querySelector(".n")).toHaveTextContent("0");
  });

  it("under a query still counts matches against the archive's size", () => {
    const { container } = render(
      <ArchiveDrawer archived={[]} actions={actions()} count={7} total={7} onNeed={vi.fn()} />,
    );
    const head = container.querySelector("#archive-toggle") as HTMLButtonElement;
    expect(head.querySelector(".n")).toHaveTextContent("0/7");
    expect(head.disabled).toBe(true);
    expect(container.querySelector(".archive-loading")).toBeNull();
  });
});

describe("addArchivedGoalCounts", () => {
  it("adds the archived tally to the counts of the cards in hand", () => {
    const counts = new Map([
      [5, 1],
      [9, 4],
    ]);
    expect([...addArchivedGoalCounts(counts, { "5": 2, "7": 3 })]).toEqual([
      [5, 3],
      [9, 4],
      [7, 3],
    ]);
  });

  it("leaves the counts alone for an absent tally and skips junk", () => {
    const counts = new Map([[5, 1]]);
    expect(addArchivedGoalCounts(counts, undefined)).toBe(counts);
    addArchivedGoalCounts(counts, { x: 2, "0": 2, "-3": 1, "6": 0, "8": Number.NaN, "5.5": 1 });
    expect([...counts]).toEqual([[5, 1]]);
  });
});

describe("BoardView: the archive not loaded yet", () => {
  const serving = makeJob({ slug: "serving", state: "working", live: true });

  function unloaded(need = vi.fn()): BoardModel {
    const jobs: WebJob[] = [serving];
    const { byColumn, archived } = bucketJobs(jobs);
    return {
      board: makeBoard({
        daemon: makeDaemon({ pid: 1, last_tick: Math.floor(Date.now() / 1000) }),
        jobs,
        archived_count: 3,
        archived_goal_counts: { "5": 2 },
      }),
      error: undefined,
      byKey: new Map(jobs.map((j) => [jobKey(j), j])),
      byColumn,
      archived,
      archivedCount: 3,
      archivedLoaded: false,
      needArchived: need,
      refetch: async () => {},
      setDragging: () => {},
      mutate: async () => null,
      lastError: null,
    };
  }

  function Harness({ board }: { board: BoardModel }) {
    const search = useJobSearch(board);
    return <BoardView board={board} actions={actions()} search={search} />;
  }

  it("labels the drawer with the board's count and asks for nothing while idle and shut", () => {
    const need = vi.fn();
    const { container } = render(<Harness board={unloaded(need)} />);
    expect(container.querySelector(".archive-head .n")).toHaveTextContent("3");
    expect(need).not.toHaveBeenCalled();
  });

  it("asks for the archive as soon as a search is typed", async () => {
    const need = vi.fn();
    const { container } = render(<Harness board={unloaded(need)} />);
    const input = container.querySelector("#job-search") as HTMLInputElement;
    expect(input).not.toBeNull();
    await userEvent.type(input, "x");
    expect(need).toHaveBeenCalled();
  });
});
