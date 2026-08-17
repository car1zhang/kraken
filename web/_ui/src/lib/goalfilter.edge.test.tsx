// goalfilter.edge.test.tsx — the EDGE dimension of 50-contract.md's Slice S3:
// the goal filter's boundaries inside useJobSearch (idle identity, the jobs
// that must NEVER match, the model the hook must not narrow) and
// GoalMultiPicker's refusal and collision paths (a refused post snapping the
// pending set back, two goals whose labels truncate to the same string, a
// write attempted with no slug).
//
// Written blind from the frozen contract. Nothing under web/_ui/src outside
// test files was read in its working-tree state; the harness idioms
// (makeJob/makeBoard, bucketJobs, jobKey, goalChipText, the getElementById
// button handle) are pre-existing state read at the base commit.
//
// The sibling blind author (athena-logic) owns the stated invariants: that a
// filter keeps exactly the jobs whose goals include the id, that the button
// label and the tick reflect the link set, that the menu lists the current
// periods. What lives here is the other half — the jobs that must not match,
// the identity semantics of the idle return, and what happens when a write is
// refused or a label collides.
//
// This file carries its OWN vi.mock of api/client.ts, per the contract's
// import-shape note; it spreads the real module so an unrelated helper the
// picker may reach for is never silently undefined.
import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../api/types.ts";
import { GoalMultiPicker } from "../components/overlays/GoalMultiPicker.tsx";
import type { BoardModel } from "../hooks/useBoard.ts";
import { useJobSearch } from "../hooks/useJobSearch.ts";
import { makeBoard, makeJob } from "../test/fixtures.ts";
import { bucketJobs } from "./format.ts";
import { goalChipText, goalJobCounts } from "./goals.ts";
import { jobKey } from "./jobKey.ts";

vi.mock("../api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return { ...actual, fetchGoals: vi.fn(), postJobGoal: vi.fn() };
});

import { fetchGoals, postJobGoal } from "../api/client.ts";

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

const SHIP = 11;
const LAND = 22;
const NEVER_LINKED = 33;

/** `goals` is Slice S3's own addition to WebJob, so fixtures are built through
 *  a loose overlay rather than a typed Partial — a test that could only be
 *  written once the type landed would be a test written from the code. */
const edgeMkJob = (over: Record<string, unknown>): WebJob =>
  makeJob(over as Partial<WebJob>);

const edgeGoalObj = (id: number, text: string, done = false) => ({ id, text, done });

const EDGE_JOBS: WebJob[] = [
  edgeMkJob({
    slug: "alpha-work",
    state: "working",
    live: true,
    goals: [edgeGoalObj(SHIP, "ship the filter"), edgeGoalObj(LAND, "land the migration")],
  }),
  edgeMkJob({
    slug: "bravo-block",
    state: "blocked",
    live: true,
    goals: [edgeGoalObj(LAND, "land the migration")],
  }),
  // no `goals` key at all — the shape every job on a pre-S1 payload has
  edgeMkJob({ slug: "charlie-draft", state: "draft" }),
  // present but EMPTY — the shape a client that always sends the key produces
  edgeMkJob({ slug: "delta-done", state: "done", goals: [] }),
  // only the retained SINGULAR key, no array: the payload a client renders
  // from when it is one version behind
  edgeMkJob({ slug: "echo-legacy", state: "working", goal: edgeGoalObj(SHIP, "ship the filter") }),
  edgeMkJob({
    slug: "arch-1",
    display_slug: "arch",
    state: "archived",
    archived: true,
    goals: [edgeGoalObj(SHIP, "ship the filter")],
  }),
];

// Named handles for the two fixtures the visibleKeys assertions address.
// Indexed access into an array is `T | undefined` under this repo's
// strictness, and a bare `EDGE_JOBS[0]` inside an expect() would not compile.
const EDGE_ALPHA = EDGE_JOBS[0]!;
const EDGE_ARCHIVED = EDGE_JOBS[5]!;

/** The REAL bucketer, exactly as the existing search suites use it: a
 *  hand-bucketed fixture can only confirm its own copy of the rule. */
function edgeModel(jobs: WebJob[]): BoardModel {
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board: makeBoard({ jobs }),
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

const edgeSlugs = (jobs: WebJob[]) => jobs.map((j) => j.slug);

interface EdgeHookProps {
  board: BoardModel;
  goalFilter?: number | null;
}

function edgeRenderSearch(board: BoardModel, goalFilter?: number | null) {
  return renderHook((p: EdgeHookProps) => useJobSearch(p.board, p.goalFilter), {
    initialProps: { board, goalFilter } as EdgeHookProps,
  });
}

// ---------------------------------------------------------------------------
// useJobSearch: the idle return's IDENTITY
// ---------------------------------------------------------------------------

describe("useJobSearch — the goal filter's idle boundary", () => {
  it("returns the board's OWN arrays by identity when both the query and the filter are idle", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result, rerender } = edgeRenderSearch(m);

    // Identity, not deep equality: a fresh array every render re-registers
    // every column's sortable list on every keystroke elsewhere on the board,
    // which is the cost the existing idle path was written to avoid.
    expect(result.current.byColumn).toBe(m.byColumn);
    expect(result.current.archived).toBe(m.archived);
    expect(result.current.active).toBe(false);
    expect(result.current.matched).toBe(EDGE_JOBS.length);
    expect(result.current.total).toBe(EDGE_JOBS.length);
    expect(result.current.visibleKeys.size).toBe(0);
    expect(result.current.reasonByKey.size).toBe(0);

    // an EXPLICIT null is the same idle as an omitted argument — the two are
    // the same statement, and "no filter" must have one behaviour
    rerender({ board: m, goalFilter: null });
    expect(result.current.byColumn).toBe(m.byColumn);
    expect(result.current.archived).toBe(m.archived);
    expect(result.current.active).toBe(false);
  });

  it("echoes the filter it was given so consumers read one source", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result, rerender } = edgeRenderSearch(m, null);
    expect(result.current.goalFilter).toBe(null);
    rerender({ board: m, goalFilter: SHIP });
    expect(result.current.goalFilter).toBe(SHIP);
  });

  it("restores idle identity semantics once the filter is cleared again", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result, rerender } = edgeRenderSearch(m, SHIP);
    expect(result.current.active).toBe(true);
    expect(result.current.byColumn).not.toBe(m.byColumn);

    rerender({ board: m, goalFilter: null });
    expect(result.current.active).toBe(false);
    expect(result.current.byColumn).toBe(m.byColumn);
    expect(result.current.archived).toBe(m.archived);
    expect(result.current.matched).toBe(EDGE_JOBS.length);
    expect(result.current.visibleKeys.size).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// useJobSearch: the jobs that must never match
// ---------------------------------------------------------------------------

describe("useJobSearch — jobs a goal filter must never keep", () => {
  it("never matches a job with no goals key, an empty goals array, or only the legacy singular goal", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result } = edgeRenderSearch(m, SHIP);

    expect(result.current.active).toBe(true);
    // alpha-work carries SHIP in its array; echo-legacy carries it ONLY in the
    // singular key, and `goals` is the one place the filter reads.
    expect(edgeSlugs(result.current.byColumn.working)).toEqual(["alpha-work"]);
    expect(edgeSlugs(result.current.byColumn.draft)).toEqual([]);
    expect(edgeSlugs(result.current.byColumn.done)).toEqual([]);
    expect(edgeSlugs(result.current.byColumn.blocked)).toEqual([]);
    expect(edgeSlugs(result.current.archived)).toEqual(["arch-1"]);
    expect(result.current.matched).toBe(2);
    expect(result.current.total).toBe(EDGE_JOBS.length);
  });

  it("empties the board without touching total when the filter matches nothing", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result } = edgeRenderSearch(m, NEVER_LINKED);

    expect(result.current.active).toBe(true);
    expect(result.current.matched).toBe(0);
    expect(result.current.total).toBe(EDGE_JOBS.length);
    expect(result.current.visibleKeys.size).toBe(0);
    for (const col of Object.keys(result.current.byColumn) as (keyof typeof result.current.byColumn)[]) {
      expect(result.current.byColumn[col]).toEqual([]);
    }
    expect(result.current.archived).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// useJobSearch: the model the hook must not narrow
// ---------------------------------------------------------------------------

describe("useJobSearch — the drag-reorder invariant", () => {
  it("never mutates or narrows board.byColumn, whatever the filter keeps", () => {
    const m = edgeModel(EDGE_JOBS);
    const workingBefore = m.byColumn.working;
    const workingSlugsBefore = edgeSlugs(workingBefore);
    const archivedBefore = m.archived;
    const archivedSlugsBefore = edgeSlugs(archivedBefore);

    const { result, rerender } = edgeRenderSearch(m, SHIP);
    act(() => result.current.setQuery("alpha"));
    rerender({ board: m, goalFilter: SHIP });

    // The hook's own lists narrowed…
    expect(edgeSlugs(result.current.byColumn.working)).toEqual(["alpha-work"]);
    // …and the MODEL is untouched, by identity AND by content. commitReorder
    // posts board.byColumn[column] whole; a narrowed one would send every
    // hidden card to the top of that column permanently.
    expect(m.byColumn.working).toBe(workingBefore);
    expect(edgeSlugs(m.byColumn.working)).toEqual(workingSlugsBefore);
    expect(m.archived).toBe(archivedBefore);
    expect(edgeSlugs(m.archived)).toEqual(archivedSlugsBefore);
    expect(result.current.byColumn.working).not.toBe(m.byColumn.working);
  });
});

// ---------------------------------------------------------------------------
// useJobSearch: composition with the query
// ---------------------------------------------------------------------------

describe("useJobSearch — goal AND query", () => {
  it("ANDs the two predicates rather than ORing them, and counts the combination", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result, rerender } = edgeRenderSearch(m, LAND);

    // LAND alone keeps two jobs
    expect(result.current.matched).toBe(2);

    // …and a query that only one of them satisfies keeps exactly that one
    act(() => result.current.setQuery("alpha"));
    rerender({ board: m, goalFilter: LAND });
    expect(edgeSlugs(result.current.byColumn.working)).toEqual(["alpha-work"]);
    expect(edgeSlugs(result.current.byColumn.blocked)).toEqual([]);
    expect(result.current.matched).toBe(1);
    expect(result.current.total).toBe(EDGE_JOBS.length);
    expect(result.current.active).toBe(true);
    expect(result.current.visibleKeys.has(jobKey(EDGE_ALPHA))).toBe(true);
    expect(result.current.visibleKeys.size).toBe(1);

    // a query the goal's jobs do not satisfy is an EMPTY intersection, never
    // the union of the two
    act(() => result.current.setQuery("charlie"));
    rerender({ board: m, goalFilter: LAND });
    expect(result.current.matched).toBe(0);
    expect(result.current.visibleKeys.size).toBe(0);
  });

  it("puts the archive drawer's matches in visibleKeys under its archive: key", () => {
    const m = edgeModel(EDGE_JOBS);
    const { result } = edgeRenderSearch(m, SHIP);
    const keys = [...result.current.visibleKeys].sort();
    expect(keys).toEqual([jobKey(EDGE_ALPHA), jobKey(EDGE_ARCHIVED)].sort());
    // the archived job's key is NOT its bare slug — an archived entry and a
    // live job that reused the freed slug are two rows
    expect(result.current.visibleKeys.has("arch-1")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// GoalMultiPicker
// ---------------------------------------------------------------------------

const PERIOD_GOALS = {
  goals: {
    week: [
      { id: 1, scope: "week", period: "2026-08-10", text: "ship v3", done: false, position: 0 },
    ],
    day: [
      { id: 2, scope: "day", period: "2026-08-17", text: "fix intake", done: false, position: 0 },
    ],
  },
};

/** `Mutate` as overlays/Overlay.tsx exports it — declared locally rather than
 *  imported so this file depends on the frozen SHAPE and not on a module the
 *  change does not touch. A refused op is a `null` return, never a throw: the
 *  picker awaits it and must reach its pending-clear either way. */
type EdgeMutate = <T,>(op: () => Promise<T>) => Promise<T | null>;

const edgeMutate: EdgeMutate = async <T,>(op: () => Promise<T>): Promise<T | null> => {
  try {
    return await op();
  } catch {
    return null;
  }
};

const edgeBtn = (id = "p") => document.getElementById(id) as HTMLButtonElement;
const edgeRows = () => screen.queryAllByRole("option");
const edgeRowWith = (text: string) =>
  edgeRows().find((r) => (r.textContent ?? "").includes(text));

async function edgeOpen(user: ReturnType<typeof userEvent.setup>, id = "p") {
  await user.click(edgeBtn(id));
}

describe("GoalMultiPicker — refusals, collisions, and the labels at the edges", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fetchGoals).mockResolvedValue(PERIOD_GOALS as never);
    vi.mocked(postJobGoal).mockResolvedValue({ ok: true } as never);
  });

  it("labels the button — at zero links, the goal's own text at one, and `N goals` beyond", async () => {
    const { rerender } = render(
      <GoalMultiPicker id="p" slug="j" goals={undefined} mutate={edgeMutate} />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    expect(edgeBtn()).toHaveTextContent("—");

    rerender(
      <GoalMultiPicker id="p" slug="j" goals={[edgeGoalObj(1, "ship v3")]} mutate={edgeMutate} />,
    );
    await waitFor(() => expect(edgeBtn()).toHaveTextContent("ship v3"));
    expect(edgeBtn().textContent ?? "").not.toContain("1 goal");

    rerender(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(1, "ship v3"), edgeGoalObj(2, "fix intake")]}
        mutate={edgeMutate}
      />,
    );
    await waitFor(() => expect(edgeBtn()).toHaveTextContent("2 goals"));
    // the plural label REPLACES the texts rather than listing them — that is
    // the whole point of collapsing at N>1
    expect(edgeBtn().textContent ?? "").not.toContain("fix intake");

    rerender(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(1, "a"), edgeGoalObj(2, "b"), edgeGoalObj(3, "c")]}
        mutate={edgeMutate}
      />,
    );
    await waitFor(() => expect(edgeBtn()).toHaveTextContent("3 goals"));
  });

  it("truncates the one-link label at `cut`, exactly as goalChipText does", async () => {
    const long = "a goal whose text is far too long for one chip in the steer head";
    render(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(7, long)]}
        mutate={edgeMutate}
        cut={20}
      />,
    );
    await waitFor(() => expect(edgeBtn()).toHaveTextContent(goalChipText(long, 20)));
    expect(edgeBtn().textContent ?? "").not.toContain(long);
  });

  it("posts add for an unticked row and remove for a linked one, without closing the menu", async () => {
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(1, "ship v3")]}
        mutate={edgeMutate}
      />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    await edgeOpen(user);

    // goal 2 is in the period list and NOT linked → add
    await user.click(edgeRowWith("fix intake") as HTMLElement);
    expect(postJobGoal).toHaveBeenLastCalledWith("j", 2, "add");

    // the menu is still open, which is the whole reason this control is not a
    // FieldSelect: linking a job to three goals must be three clicks, not
    // three round trips through the button.
    expect(edgeRows().length).toBeGreaterThan(0);

    // goal 1 IS linked → the same gesture unlinks it
    await user.click(edgeRowWith("ship v3") as HTMLElement);
    expect(postJobGoal).toHaveBeenLastCalledWith("j", 1, "remove");
    expect(edgeRows().length).toBeGreaterThan(0);
    expect(vi.mocked(postJobGoal).mock.calls.length).toBe(2);
  });

  it("posts the clear-all from the leading — row, with no op at all", async () => {
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(1, "ship v3"), edgeGoalObj(2, "fix intake")]}
        mutate={edgeMutate}
      />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    await edgeOpen(user);

    const rows = edgeRows();
    expect(rows.length).toBeGreaterThan(0);
    // the LEADING row, held as its own binding so the `— is first` claim is
    // what is asserted rather than an incidental index
    const leading = rows[0]!;
    expect(leading.textContent ?? "").toContain("—");

    await user.click(leading);
    const call = vi.mocked(postJobGoal).mock.calls.at(-1);
    expect(call?.[0]).toBe("j");
    expect(call?.[1]).toBe(0);
    // goalId 0 with NO op is the clear-all; an op alongside it would be a
    // request the route reads and then ignores, which is a client saying
    // something it does not mean.
    expect(call?.[2]).toBeUndefined();
  });

  it("snaps the pending set back to the payload's truth when a post is refused", async () => {
    const user = userEvent.setup();
    let refuse!: (e: unknown) => void;
    vi.mocked(postJobGoal).mockImplementationOnce(
      () => new Promise((_resolve, reject) => { refuse = reject; }),
    );

    // the payload's truth is ZERO links throughout: nothing here ever refetches
    render(<GoalMultiPicker id="p" slug="j" goals={[]} mutate={edgeMutate} />);
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    expect(edgeBtn()).toHaveTextContent("—");

    await edgeOpen(user);
    await user.click(edgeRowWith("ship v3") as HTMLElement);

    // optimistic: the pick shows before the server has answered anything, on
    // the button AND on the row's own tick state
    await waitFor(() => expect(edgeBtn()).toHaveTextContent("ship v3"));
    expect(edgeRowWith("ship v3")).toHaveAttribute("aria-selected", "true");

    await act(async () => {
      refuse(new Error("refused"));
      await Promise.resolve();
    });

    // and both snap back, because the payload still says zero links
    await waitFor(() => expect(edgeBtn()).toHaveTextContent("—"));
    expect(edgeBtn().textContent ?? "").not.toContain("ship v3");
    // "false", not absent: an unticked row states its state rather than
    // leaving a reader to infer it from a missing attribute
    expect(edgeRowWith("ship v3")).toHaveAttribute("aria-selected", "false");
  });

  it("disambiguates two rows whose labels truncate identically with ` #<id>`", async () => {
    const user = userEvent.setup();
    // Both texts cut to the same 28 characters, so without the id the menu
    // would show two identical rows and could not say which one was picked.
    const first = "finish the goal filter work today";
    const second = "finish the goal filter work tomorrow";
    expect(goalChipText(first, 28)).toBe(goalChipText(second, 28));
    vi.mocked(fetchGoals).mockResolvedValue({
      goals: {
        week: [
          { id: 101, scope: "week", period: "2026-08-10", text: first, done: false, position: 0 },
          { id: 102, scope: "week", period: "2026-08-10", text: second, done: false, position: 1 },
        ],
        day: [],
      },
    } as never);

    render(<GoalMultiPicker id="p" slug="j" goals={undefined} mutate={edgeMutate} />);
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    await edgeOpen(user);

    const disambiguated = edgeRowWith("#102");
    expect(disambiguated).toBeTruthy();
    // exactly ONE of the pair carries the suffix — the first keeps the base
    const withHash = edgeRows().filter((r) => (r.textContent ?? "").includes("#"));
    expect(withHash.length).toBe(1);

    await user.click(disambiguated as HTMLElement);
    expect(postJobGoal).toHaveBeenLastCalledWith("j", 102, "add");
  });

  it("appends an off-period linked goal and unlinks it on pick", async () => {
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(900, "last week's push")]}
        mutate={edgeMutate}
      />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    await edgeOpen(user);

    const row = edgeRowWith("last week's push");
    expect(row).toBeTruthy();
    await user.click(row as HTMLElement);
    // it is LINKED, so the gesture is a remove — a link from a rolled-over
    // period must be reachable to unlink, or it can never be undone
    expect(postJobGoal).toHaveBeenLastCalledWith("j", 900, "remove");
  });

  it("skips the option fetch when inactive while leaving the write live", async () => {
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(900, "last week's push")]}
        mutate={edgeMutate}
        active={false}
      />,
    );
    expect(fetchGoals).not.toHaveBeenCalled();
    await edgeOpen(user);
    const row = edgeRowWith("last week's push");
    expect(row).toBeTruthy();
    await user.click(row as HTMLElement);
    expect(postJobGoal).toHaveBeenLastCalledWith("j", 900, "remove");
  });

  it("degrades silently when the option fetch fails", async () => {
    vi.mocked(fetchGoals).mockRejectedValue(new Error("network"));
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id="p"
        slug="j"
        goals={[edgeGoalObj(900, "last week's push")]}
        mutate={edgeMutate}
      />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    // the control still renders its own link, and the button still labels it —
    // the goal link is not what carl opened the overlay for
    await waitFor(() => expect(edgeBtn()).toHaveTextContent("last week's push"));
    await edgeOpen(user);
    expect(edgeRowWith("last week's push")).toBeTruthy();
  });

  it("posts nothing when there is no slug to address", async () => {
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id="p"
        slug={null}
        goals={[edgeGoalObj(900, "last week's push")]}
        mutate={edgeMutate}
      />,
    );
    await edgeOpen(user);
    const row = edgeRowWith("last week's push");
    if (row) await user.click(row);
    expect(postJobGoal).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// goalJobCounts — the panel badge's arithmetic
// ---------------------------------------------------------------------------

describe("goalJobCounts — what must NOT be counted", () => {
  it("counts nothing for a job with no goals key, an empty array, or only the legacy singular goal", () => {
    const counts = goalJobCounts(EDGE_JOBS);
    // alpha-work + arch-1 carry SHIP in their arrays; echo-legacy carries it
    // ONLY in `goal`, and a badge must never promise more cards than the
    // filter delivers.
    expect(counts.get(SHIP)).toBe(2);
    expect(counts.get(LAND)).toBe(2);

    // a goal nobody links is ABSENT from the map, not present as 0 — the
    // panel renders a count only when >0, and a stored 0 is a second way to
    // say the same thing that two readers will eventually disagree about
    expect(counts.has(NEVER_LINKED)).toBe(false);

    // the three non-contributing shapes, in isolation, produce an EMPTY map
    const barren = goalJobCounts([
      edgeMkJob({ slug: "no-key", state: "draft" }),
      edgeMkJob({ slug: "empty-array", state: "draft", goals: [] }),
      edgeMkJob({ slug: "legacy-only", state: "draft", goal: edgeGoalObj(SHIP, "ship the filter") }),
    ]);
    expect(barren.size).toBe(0);
  });

  it("returns an empty map for an empty board", () => {
    expect(goalJobCounts([]).size).toBe(0);
  });

  it("agrees exactly with what the filter delivers, for every id", () => {
    const m = edgeModel(EDGE_JOBS);
    const counts = goalJobCounts(EDGE_JOBS);
    for (const id of [SHIP, LAND, NEVER_LINKED]) {
      const { result } = edgeRenderSearch(m, id);
      // The badge and the filter are two readings of one predicate. A count
      // that overshoots is a row carl clicks expecting N cards and gets fewer,
      // with nothing on screen saying where they went.
      expect(counts.get(id) ?? 0).toBe(result.current.matched);
    }
  });
});
