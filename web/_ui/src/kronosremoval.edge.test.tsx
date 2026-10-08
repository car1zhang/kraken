import { readFileSync } from "node:fs";

import { render, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "./api/types.ts";
import type { BoardModel } from "./hooks/useBoard.ts";
import { useHashRoute, VIEWS } from "./hooks/useHashRoute.ts";
import { useJobSearch } from "./hooks/useJobSearch.ts";
import { bucketJobs } from "./lib/format.ts";
import { jobKey } from "./lib/jobKey.ts";
import { BoardView, type BoardActions } from "./components/board/BoardView.tsx";
import { makeBoard, makeDaemon, makeJob } from "./test/fixtures.ts";

// kronosremoval.edge.test.tsx — the EDGE dimension of slice W of
// artifacts/50-contract.md (kronos removal): every stale way a browser can
// still ask for the kronos view (URL hash in several spellings, a legacy
// localStorage value) lands on the board rather than on a view no <main>
// matches; VIEWS has exactly four entries; the board renders no dock in any
// job state; the stylesheet carries no kronos selector. Written blind.

const LEGACY_KEY = "kraken-view";

beforeEach(() => {
  localStorage.clear();
  location.hash = "";
});

describe("KronosRemoval VIEWS boundary", () => {
  it("KronosRemoval: VIEWS has exactly four entries and none is kronos", () => {
    expect(VIEWS.length).toBe(4);
    expect([...VIEWS]).toEqual(["board", "memory", "dream", "log"]);
    expect(VIEWS as readonly string[]).not.toContain("kronos");
    expect(new Set(VIEWS).size).toBe(4);
  });
});

describe("KronosRemoval stale routes", () => {
  for (const hash of ["#/kronos", "#/KRONOS", "#/Kronos", "#/kronos/", "#/kronos?x=1", "#kronos"]) {
    it(`KronosRemoval: hash ${hash} resolves to board`, () => {
      location.hash = hash;
      const { result } = renderHook(() => useHashRoute());
      expect(result.current[0]).toBe("board");
    });
  }

  it("KronosRemoval: a stale localStorage view 'kronos' resolves to board", () => {
    localStorage.setItem(LEGACY_KEY, "kronos");
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("board");
  });

  it("KronosRemoval: a JSON-quoted stale localStorage view resolves to board", () => {
    localStorage.setItem(LEGACY_KEY, JSON.stringify("kronos"));
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("board");
  });

  it("KronosRemoval: the surviving views still resolve to themselves", () => {
    for (const v of ["memory", "dream", "log"] as const) {
      location.hash = `#/${v}`;
      const { result, unmount } = renderHook(() => useHashRoute());
      expect(result.current[0]).toBe(v);
      unmount();
    }
  });
});

// --- BoardView render harness (idiom from BoardView.test.tsx) --------------

const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over });

const stubActions = (): BoardActions =>
  ({
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
  }) as unknown as BoardActions;

function model(jobs: WebJob[]): BoardModel {
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

function BoardHarness({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <BoardView board={board} actions={actions} search={search} />;
}

function expectNoDock(container: HTMLElement) {
  expect(container.querySelector("#kronos-dock")).toBeNull();
  expect(document.getElementById("kronos-dock")).toBeNull();
  for (const el of container.querySelectorAll("*")) {
    for (const cls of el.classList) {
      expect(cls.startsWith("kronos-dock"), `element carries class ${cls}`).toBe(false);
    }
  }
}

describe("KronosRemoval BoardView has no dock", () => {
  it("KronosRemoval: an empty board renders no kronos dock", () => {
    const { container } = render(<BoardHarness board={model([])} actions={stubActions()} />);
    expectNoDock(container);
  });

  it("KronosRemoval: a board with every state renders no kronos dock", () => {
    const { container } = render(
      <BoardHarness
        board={model([
          job({ slug: "d", state: "draft" }),
          job({ slug: "w", state: "working", live: true }),
          job({ slug: "b", state: "blocked", live: true }),
          job({ slug: "x", state: "done" }),
        ])}
        actions={stubActions()}
      />,
    );
    expectNoDock(container);
  });

  it("KronosRemoval: the board's markup names kronos nowhere", () => {
    const { container } = render(
      <BoardHarness board={model([job({ slug: "b", state: "blocked", live: true })])} actions={stubActions()} />,
    );
    expect(container.innerHTML.toLowerCase()).not.toContain("kronos");
  });
});

describe("KronosRemoval stylesheet", () => {
  it("KronosRemoval: style.css contains no kronos selector (any case)", () => {
    // path relative to the vitest root (web/_ui), as the styles tests do.
    const css = readFileSync("src/styles/style.css", "utf8");
    expect(css.toLowerCase()).not.toContain("kronos");
  });
});
