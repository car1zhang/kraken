/**
 * kronosremoval.contract.test.tsx — athena-logic's BLIND logic suite for the
 * kronos removal, slice W of artifacts/50-contract.md. Written from the
 * contract text alone; no implementation file under src/ was read (the
 * render harness shapes were learned from the existing Chrome.test.tsx,
 * BoardView.test.tsx and useHashRoute.test.tsx). Dimension: LOGIC — four
 * views in order, `#/kronos` lands on the board, four tabs, no dock, deleted
 * modules gone, no kronos selector left.
 *
 * Every package-level identifier is prefixed `contract`; every test title
 * contains `KronosRemoval`.
 */
import { render as contractRender, renderHook as contractRenderHook } from "@testing-library/react";
import { existsSync as contractExists, readFileSync as contractRead } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "./api/types.ts";
import type { BoardModel } from "./hooks/useBoard.ts";
import { useHashRoute, VIEWS } from "./hooks/useHashRoute.ts";
import { useJobSearch } from "./hooks/useJobSearch.ts";
import { Chrome } from "./components/chrome/Chrome.tsx";
import { BoardView, type BoardActions } from "./components/board/BoardView.tsx";
import { bucketJobs } from "./lib/format.ts";
import { jobKey } from "./lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "./test/fixtures.ts";

const contractNow = 1_700_000_000_000;

function contractModel(jobs: WebJob[]): BoardModel {
  const board: WebBoard = makeBoard({
    daemon: makeDaemon({ pid: 42, last_tick: Math.floor(contractNow / 1000) - 3 }),
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

const contractActions = (): BoardActions => ({
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

function ContractBoardHarness({ board }: { board: BoardModel }) {
  const search = useJobSearch(board);
  return <BoardView board={board} actions={contractActions()} search={search} />;
}

beforeEach(() => {
  localStorage.clear();
  location.hash = "";
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("VIEWS (slice W)", () => {
  it("KronosRemoval: VIEWS is exactly board · memory · dream · log, in order", () => {
    expect([...VIEWS]).toEqual(["board", "memory", "dream", "log"]);
  });
});

describe("useHashRoute (slice W)", () => {
  it("KronosRemoval: #/kronos resolves to the board", () => {
    window.location.hash = "#/kronos";
    const { result } = contractRenderHook(() => useHashRoute());
    expect(result.current[0]).toBe("board");
  });

  it("KronosRemoval: #/memory, #/dream, #/log resolve to themselves", () => {
    for (const v of ["memory", "dream", "log"] as const) {
      window.location.hash = `#/${v}`;
      const { result, unmount } = contractRenderHook(() => useHashRoute());
      expect(result.current[0]).toBe(v);
      unmount();
    }
  });
});

describe("Chrome (slice W)", () => {
  it("KronosRemoval: renders exactly four .viewtab elements — BOARD, MEMORY, DREAM, LOG", () => {
    vi.spyOn(Date, "now").mockReturnValue(contractNow);
    const props = {
      board: contractModel([]),
      view: "board" as const,
      onNavigate: vi.fn(),
      usage: undefined,
      onOpenUsage: vi.fn(),
      onToast: vi.fn(),
    };
    const { container } = contractRender(<Chrome {...props} />);
    const tabs = [...container.querySelectorAll(".viewtab")];
    expect(tabs.map((t) => t.textContent)).toEqual(["BOARD", "MEMORY", "DREAM", "LOG"]);
  });
});

describe("BoardView (slice W)", () => {
  it("KronosRemoval: renders no #kronos-dock and no element whose class starts with kronos-dock", () => {
    const { container } = contractRender(
      <ContractBoardHarness board={contractModel([makeJob({ slug: "a", state: "draft", mtime: 0 })])} />,
    );
    expect(document.getElementById("kronos-dock")).toBeNull();
    const docked = [...container.querySelectorAll("*")].filter((el) =>
      [...el.classList].some((c) => c.startsWith("kronos-dock")),
    );
    expect(docked).toEqual([]);
  });
});

describe("deleted modules (slice W)", () => {
  // vitest runs with cwd = web/_ui (the existing style tests read
  // "src/styles/style.css" relative to it).
  it("KronosRemoval: the kronos view, dock and lib modules no longer exist", () => {
    for (const p of [
      "src/components/kronos",
      "src/components/board/KronosDock.tsx",
      "src/lib/kronos.ts",
      "src/lib/kronos.contract.test.ts",
      "src/lib/kronos.edge.test.ts",
    ]) {
      expect(contractExists(p), `${p} should be deleted`).toBe(false);
    }
  });

  it("KronosRemoval: the API client modules export no symbol named Kronos or Orch", async () => {
    // Runtime exports only; type-only exports are the build's (tsc) half.
    const mods: Record<string, Record<string, unknown>> = {
      "api/client.ts": await import("./api/client.ts"),
      "lib/client.ts": await import("./lib/client.ts"),
    };
    for (const [name, mod] of Object.entries(mods)) {
      const bad = Object.keys(mod).filter((k) => /kronos|orch/i.test(k));
      expect(bad, `${name} exports ${bad.join(", ")}`).toEqual([]);
    }
  });
});

describe("stylesheet (slice W)", () => {
  it("KronosRemoval: style.css has no selector containing kronos", () => {
    const css = contractRead("src/styles/style.css", "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    // Every rule prelude is the text before a `{` back to the previous `}`,
    // `{` or `;` — at-rule preludes included, which can only over-report.
    const preludes = [...css.matchAll(/([^{};]+)\{/g)].map((m) => m[1]!.trim());
    const bad = preludes.filter((s) => /kronos/i.test(s));
    expect(bad).toEqual([]);
  });
});
