import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// goalfilter.contract.test.tsx — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S3: useJobSearch's goal-filter composition,
// postJobGoal's request bodies, and GoalMultiPicker's stated behaviors.
//
// Written from the contract text alone. Nothing under web/_ui/src outside test
// files was read in its WORKING-TREE form; the context used is PRE-EXISTING
// state at the base commit (HEAD = 66eb71a) — useJobSearch.test.tsx's `model()`
// harness, test/fixtures.ts, GoalPicker.test.tsx's mock shape, and
// FieldSelect.tsx, which the contract states is UNTOUCHED and of which
// GoalMultiPicker is declared "a multi-select sibling".
//
// Dimension: LOGIC — the stated filter semantics, the stated request bodies,
// and the picker's happy paths. Hostile inputs, fetch failures and the
// silent-degrade arms are athena-edge's.
//
// Every module-scope identifier declared here is prefixed `contract`.
//
// THE MOCK. `../api/client.ts` is mocked with importOriginal plus two
// overrides rather than an exhaustive literal, for two reasons: the REAL
// module is what postJobGoal's own body assertions need (reached through
// vi.importActual), and a literal factory would break the moment
// GoalMultiPicker imports one more helper from it — a failure that says
// nothing about the contract.
vi.mock("../api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/client.ts")>();
  return {
    ...actual,
    fetchGoals: vi.fn(() =>
      Promise.resolve({
        goals: {
          week: [
            { id: 1, scope: "week", period: "2026-08-10", text: "ship v3", done: false, position: 0 },
          ],
          day: [
            { id: 2, scope: "day", period: "2026-08-17", text: "fix intake", done: false, position: 0 },
          ],
        },
      }),
    ),
    postJobGoal: vi.fn(() => Promise.resolve({ ok: true })),
  };
});

import { fetchGoals, postJobGoal } from "../api/client.ts";
import type { WebJob, WebJobGoal } from "../api/types.ts";
import { GoalMultiPicker } from "../components/overlays/GoalMultiPicker.tsx";
import type { Mutate } from "../components/overlays/Overlay.tsx";
import type { BoardModel } from "../hooks/useBoard.ts";
import { useJobSearch } from "../hooks/useJobSearch.ts";
import { makeBoard, makeJob } from "../test/fixtures.ts";
import { bucketJobs } from "./format.ts";
import { goalJobCounts } from "./goals.ts";
import { jobKey } from "./jobKey.ts";

/** Narrow handles onto the two mocked exports, so a test can re-program one
 * without dragging vitest's generic Mock type through this file. */
const contractFetchGoalsMock = fetchGoals as unknown as {
  mockResolvedValueOnce: (v: unknown) => void;
};
const contractPostJobGoalMock = postJobGoal as unknown as {
  mockRejectedValueOnce: (v: unknown) => void;
};

// contractModel builds a BoardModel with the REAL bucketer, exactly as
// useJobSearch.test.tsx and BoardView.test.tsx do: a hand-bucketed fixture can
// only confirm its own copy of the rule.
function contractModel(jobs: WebJob[]): BoardModel {
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

const contractShip: WebJobGoal = { id: 1, text: "ship goal boards", done: false };
const contractIntake: WebJobGoal = { id: 2, text: "close the intake gap", done: true };

// Five jobs across four columns plus the archive. `delta-four` carries the
// legacy PRIMARY key alone and no `goals` array — the contract's "a job with
// no `goals` never matches" is aimed exactly at it.
const contractJobs: WebJob[] = [
  makeJob({
    slug: "alpha-one",
    state: "working",
    live: true,
    goals: [contractShip, contractIntake],
    goal: contractShip,
  }),
  makeJob({ slug: "bravo-two", state: "blocked", live: true, goals: [contractIntake], goal: contractIntake }),
  makeJob({ slug: "charlie-three", state: "working", live: true }),
  makeJob({ slug: "delta-four", state: "draft", goal: contractShip }),
  makeJob({
    slug: "echo-five-2",
    display_slug: "echo-five",
    state: "archived",
    archived: true,
    goals: [contractShip],
    goal: contractShip,
  }),
];

const contractSlugs = (jobs: WebJob[]) => jobs.map((j) => j.slug);

describe("useJobSearch — the goal filter", () => {
  // The resting state, and the one the existing eight tests already pin: with
  // no filter and no query the hook must be what it was, down to handing back
  // the board's OWN arrays by identity. A fresh array here re-registers every
  // column's sortable list on every unrelated keystroke.
  it("is idle, with the board's own arrays, when no goal is selected", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, null));
    expect(result.current.active).toBe(false);
    expect(result.current.byColumn).toBe(board.byColumn);
    expect(result.current.archived).toBe(board.archived);
    expect(result.current.matched).toBe(5);
    expect(result.current.total).toBe(5);
    expect(result.current.visibleKeys.size).toBe(0);
    expect(result.current.goalFilter).toBe(null);
  });

  // The second spelling of "no filter": the argument omitted entirely, which
  // is what every call site that predates the feature looks like.
  it("is idle when the goal filter argument is omitted", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board));
    expect(result.current.active).toBe(false);
    expect(result.current.byColumn).toBe(board.byColumn);
    expect(result.current.archived).toBe(board.archived);
    expect(result.current.matched).toBe(5);
  });

  // THE filter semantics. `active` is true with no query at all — everything
  // cosmetic keys off it, so a filter that left it false would narrow the board
  // while the bar still read as resting.
  it("keeps exactly the jobs whose goals include the selected id", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, 1));
    expect(result.current.active).toBe(true);
    expect(contractSlugs(result.current.byColumn.working)).toEqual(["alpha-one"]);
    expect(result.current.byColumn.blocked).toEqual([]);
    expect(result.current.byColumn.draft).toEqual([]);
    expect(result.current.byColumn.done).toEqual([]);
    expect(contractSlugs(result.current.archived)).toEqual(["echo-five-2"]);
    // `matched` counts the combined predicate while `total` stays the board's
    // WHOLE count (AMENDED at join): the readout is `N of M jobs`, and M is
    // what makes N legible — an M that narrowed with the filter would always
    // read `N of N`.
    expect(result.current.matched).toBe(2);
    expect(result.current.total).toBe(5);
  });

  // A job's SECOND goal is as real as its first — the whole point of the join
  // table — so filtering on it must find the multi-linked card too.
  it("matches on any member of a job's goals, not just its primary", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, 2));
    expect(contractSlugs(result.current.byColumn.working)).toEqual(["alpha-one"]);
    expect(contractSlugs(result.current.byColumn.blocked)).toEqual(["bravo-two"]);
    expect(result.current.matched).toBe(2);
  });

  // "a job with no `goals` never matches". `delta-four` carries the legacy
  // `goal` primary pointing at the SAME id and is deliberately excluded: the
  // wire serves `goals` whenever a job links anything, so a fallback onto
  // `goal` would only ever fire for a payload that cannot exist — and would
  // quietly make the counts and the render lists disagree if it did.
  it("never matches a job that carries no goals array", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, 1));
    expect(contractSlugs(result.current.byColumn.draft)).toEqual([]);
    expect(result.current.visibleKeys.has("delta-four")).toBe(false);
  });

  // A goal nothing is filed under narrows the board to nothing rather than to
  // everything — the fail-open reading is how a filter silently stops filtering.
  it("keeps nothing for a goal no job links", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, 99));
    expect(result.current.active).toBe(true);
    expect(result.current.matched).toBe(0);
    expect(result.current.byColumn.working).toEqual([]);
    expect(result.current.archived).toEqual([]);
  });

  // AND, not OR: the two predicates compose inside the ONE hook, which is why
  // there is no second filtering hook downstream.
  it("ANDs the query terms with the goal predicate", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, 1));

    // a query naming a job that DOES carry the goal
    act(() => result.current.setQuery("alpha"));
    expect(contractSlugs(result.current.byColumn.working)).toEqual(["alpha-one"]);
    expect(result.current.matched).toBe(1);
    expect(result.current.total).toBe(5);

    // a query naming a job that does NOT carry it — an OR would keep it
    act(() => result.current.setQuery("bravo"));
    expect(result.current.matched).toBe(0);
    expect(result.current.byColumn.blocked).toEqual([]);

    // and the goal alone still narrows once the query clears
    act(() => result.current.setQuery(""));
    expect(result.current.matched).toBe(2);
    expect(result.current.active).toBe(true);
  });

  // visibleKeys is the ONE thing the drag layer reads. Keys, never slugs: the
  // archived entry and a live job that reused its freed slug are two rows.
  it("reflects the combined predicate in visibleKeys", () => {
    const board = contractModel(contractJobs);
    const { result } = renderHook(() => useJobSearch(board, 1));
    expect(result.current.visibleKeys.has("alpha-one")).toBe(true);
    expect(result.current.visibleKeys.has("archive:echo-five-2")).toBe(true);
    expect(result.current.visibleKeys.has("bravo-two")).toBe(false);
    expect(result.current.visibleKeys.has("charlie-three")).toBe(false);
    expect(result.current.visibleKeys.size).toBe(2);
  });

  // THE invariant this hook exists to preserve, restated for the new input.
  // useDragReorder builds the /api/reorder body from board.byColumn[column] —
  // the WHOLE column — and ApplyColumnOrder floats every id missing from that
  // post to the TOP, permanently. A filter pushed into the model rather than
  // returned beside it makes every drag under an active filter reorder the
  // board behind carl's back.
  it("never mutates or narrows the board's own columns", () => {
    const board = contractModel(contractJobs);
    const beforeWorking = board.byColumn.working;
    const beforeArchived = board.archived;
    const { result } = renderHook(() => useJobSearch(board, 1));

    expect(result.current.byColumn.working).not.toBe(board.byColumn.working);
    expect(board.byColumn.working).toBe(beforeWorking);
    expect(board.archived).toBe(beforeArchived);
    expect(contractSlugs(board.byColumn.working)).toEqual(["alpha-one", "charlie-three"]);
    expect(contractSlugs(board.byColumn.blocked)).toEqual(["bravo-two"]);
    expect(contractSlugs(board.archived)).toEqual(["echo-five-2"]);
  });

  // One source: consumers (the bar chip, the panel's active row) read the
  // selection off the model rather than keeping a second copy that can drift.
  it("echoes the selected goal back on the model", () => {
    const board = contractModel(contractJobs);
    const { result, rerender } = renderHook(
      ({ goal }: { goal: number | null }) => useJobSearch(board, goal),
      { initialProps: { goal: 2 as number | null } },
    );
    expect(result.current.goalFilter).toBe(2);
    rerender({ goal: null });
    expect(result.current.goalFilter).toBe(null);
  });

  // Clearing is a return to the resting state, identity included — not merely
  // "everything matches again".
  it("restores idle identity semantics when the filter is cleared", () => {
    const board = contractModel(contractJobs);
    const { result, rerender } = renderHook(
      ({ goal }: { goal: number | null }) => useJobSearch(board, goal),
      { initialProps: { goal: 1 as number | null } },
    );
    expect(result.current.active).toBe(true);
    rerender({ goal: null });
    expect(result.current.active).toBe(false);
    expect(result.current.byColumn).toBe(board.byColumn);
    expect(result.current.archived).toBe(board.archived);
    expect(result.current.matched).toBe(5);
    expect(result.current.visibleKeys.size).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// goalJobCounts (frozen at the join)
// ---------------------------------------------------------------------------

describe("goalJobCounts", () => {
  // The panel's per-row badge. It is derived from the SAME payload the filter
  // reads, and the two must agree exactly: a badge that promises more cards
  // than clicking it delivers is worse than no badge, because the person
  // clicking has no way to tell which number lied.
  it("counts every job whose goals include the id, active and archived alike", () => {
    const counts = goalJobCounts(contractJobs);
    // goal 1: alpha-one (active) + echo-five-2 (archived)
    expect(counts.get(1)).toBe(2);
    // goal 2: alpha-one + bravo-two
    expect(counts.get(2)).toBe(2);
    // no third key: the map is built from what the jobs actually link
    expect(counts.size).toBe(2);
  });

  // `goals` ONLY. `delta-four` carries the legacy single `goal` pointing at
  // goal 1 and contributes nothing — counting it would make the badge read 3
  // where the filter delivers 2, which is precisely the disagreement the
  // contract's "never the legacy single `goal`" clause forbids.
  it("never counts the legacy single goal", () => {
    const legacyOnly = [makeJob({ slug: "legacy", state: "draft", goal: contractShip })];
    expect(goalJobCounts(legacyOnly).size).toBe(0);
    // and the same job inside the full set does not inflate goal 1
    expect(goalJobCounts(contractJobs).get(1)).toBe(2);
  });

  // A job with no goals at all contributes nothing, and an empty board is an
  // empty map — the shape the panel mounts with on a fresh install.
  it("returns an empty map for jobs that link nothing", () => {
    expect(goalJobCounts([]).size).toBe(0);
    expect(goalJobCounts([makeJob({ slug: "bare", state: "working" })]).size).toBe(0);
  });

  // One job linking N goals contributes ONE to each of them — not N to the
  // first, and not one to the set as a whole.
  it("counts a multi-linked job once against each of its goals", () => {
    const counts = goalJobCounts([
      makeJob({ slug: "both", state: "working", goals: [contractShip, contractIntake] }),
    ]);
    expect(counts.get(1)).toBe(1);
    expect(counts.get(2)).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// postJobGoal — the request bodies
// ---------------------------------------------------------------------------

describe("postJobGoal", () => {
  // The REAL function, reached past this file's own mock: what is under test is
  // the body it puts on the wire, which a vi.fn() cannot answer for.
  async function contractRealPost() {
    const mod = await vi.importActual<typeof import("../api/client.ts")>("../api/client.ts");
    return mod.postJobGoal;
  }

  function contractStubFetch() {
    const fetchMock = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        statusText: "OK",
        json: () => Promise.resolve({ ok: true }),
      } as unknown as Response),
    );
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function contractSentBody(fetchMock: ReturnType<typeof contractStubFetch>) {
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/job/goal");
    expect(init.method).toBe("POST");
    return JSON.parse(String(init.body)) as Record<string, unknown>;
  }

  beforeEach(() => vi.unstubAllGlobals());
  // restored, so the stubbed fetch cannot leak into the picker suite below
  afterEach(() => vi.unstubAllGlobals());

  it("carries op exactly when one is given", async () => {
    const real = await contractRealPost();

    const addFetch = contractStubFetch();
    await real("job-a", 5, "add");
    expect(contractSentBody(addFetch)).toEqual({ slug: "job-a", goal_id: 5, op: "add" });

    vi.unstubAllGlobals();
    const removeFetch = contractStubFetch();
    await real("job-a", 5, "remove");
    expect(contractSentBody(removeFetch)).toEqual({ slug: "job-a", goal_id: 5, op: "remove" });
  });

  // The clear-all, and the LEGACY shape a shipped client still sends: goal id 0
  // with no op at all. An `op` defaulted in here would put a key on the wire
  // that the route then has to decide to ignore.
  it("sends the bare clear-all shape for goal id 0 with no op", async () => {
    const real = await contractRealPost();
    const clearFetch = contractStubFetch();
    await real("job-a", 0);
    const body = contractSentBody(clearFetch);
    expect(body).toEqual({ slug: "job-a", goal_id: 0 });
    expect("op" in body).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// GoalMultiPicker
// ---------------------------------------------------------------------------
//
// Pinned against the component itself by bare-mounting it, rather than through
// either host: the control takes its whole world through props, so a host would
// add wiring without adding coverage. What a host DOES add — that the editor
// form and the steer head each mount the picker where they say they do — is
// asserted in EditorOverlay.test.tsx and SteerOverlay.test.tsx, not here.
//
// The queries below use role="option", aria-selected and
// document.getElementById(id) because the contract FREEZES all three (AMENDED
// at join): menu rows carry role="option", a ticked row aria-selected="true"
// and an unticked one "false", and the `id` prop lands on the picker's button.
// Row LABEL text is still deliberately matched by a regex on the goal's own
// text — the contract states no scope prefix either way, so pinning one would
// invent an obligation.

// `Mutate` is the type the contract freezes for this prop (overlays/Overlay.tsx),
// and it is what both hosts actually pass. The catch is what lets the refusal
// case below run at all: a rejected post resolves to null here, exactly as the
// real board's mutate funnels a failure into the clock corner rather than
// throwing at the call site.
const contractMutate: Mutate = async (op) => {
  try {
    return await op();
  } catch {
    return null;
  }
};

const contractRow = (name: RegExp) => screen.getByRole("option", { name });

describe("GoalMultiPicker", () => {
  beforeEach(() => vi.clearAllMocks());

  async function contractMount(props: {
    id?: string;
    goals?: WebJobGoal[];
    cut?: number;
    active?: boolean;
  }) {
    const id = props.id ?? "p";
    const user = userEvent.setup();
    render(
      <GoalMultiPicker
        id={id}
        slug="j"
        goals={props.goals}
        mutate={contractMutate}
        active={props.active}
        cut={props.cut}
      />,
    );
    if (props.active !== false) await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    const btn = document.getElementById(id) as HTMLButtonElement;
    return { user, btn };
  }

  it("lists the current periods and posts add for an unticked row", async () => {
    const { user, btn } = await contractMount({ goals: undefined });
    await user.click(btn);
    expect(contractRow(/ship v3/)).toBeInTheDocument();
    expect(contractRow(/fix intake/)).toBeInTheDocument();

    await user.click(contractRow(/ship v3/));
    expect(postJobGoal).toHaveBeenCalledWith("j", 1, "add");
  });

  it("posts remove for a row that is already linked", async () => {
    const { user, btn } = await contractMount({ goals: [{ id: 1, text: "ship v3", done: false }] });
    await user.click(btn);
    await user.click(contractRow(/ship v3/));
    expect(postJobGoal).toHaveBeenCalledWith("j", 1, "remove");
  });

  it("ticks exactly the rows in the link set", async () => {
    const { user, btn } = await contractMount({ goals: [{ id: 1, text: "ship v3", done: false }] });
    await user.click(btn);
    expect(contractRow(/ship v3/)).toHaveAttribute("aria-selected", "true");
    expect(contractRow(/fix intake/)).toHaveAttribute("aria-selected", "false");
  });

  it("posts the clear-all on the — row", async () => {
    const { user, btn } = await contractMount({
      goals: [
        { id: 1, text: "ship v3", done: false },
        { id: 2, text: "fix intake", done: false },
      ],
    });
    await user.click(btn);
    await user.click(contractRow(/—/));
    expect(postJobGoal).toHaveBeenCalledWith("j", 0);
  });

  // The reason this control exists rather than a second FieldSelect: filing a
  // job under three goals must be three clicks, not three open-pick-reopen
  // cycles.
  it("keeps the menu open across picks", async () => {
    const { user, btn } = await contractMount({ goals: undefined });
    await user.click(btn);
    await user.click(contractRow(/ship v3/));
    expect(contractRow(/fix intake/)).toBeInTheDocument();
    await user.click(contractRow(/fix intake/));
    expect(postJobGoal).toHaveBeenCalledWith("j", 1, "add");
    expect(postJobGoal).toHaveBeenCalledWith("j", 2, "add");
    expect(postJobGoal).toHaveBeenCalledTimes(2);
  });

  it("labels the button — at zero links", async () => {
    const { btn } = await contractMount({ goals: undefined });
    expect(btn).toHaveTextContent("—");
  });

  it("labels the button with the goal's own text at one link", async () => {
    const { btn } = await contractMount({ goals: [{ id: 1, text: "ship v3", done: false }] });
    expect(btn).toHaveTextContent("ship v3");
  });

  it("labels the button N goals above one link", async () => {
    const { btn } = await contractMount({
      goals: [
        { id: 1, text: "ship v3", done: false },
        { id: 2, text: "fix intake", done: false },
        { id: 3, text: "land the PR", done: false },
      ],
    });
    expect(btn).toHaveTextContent("3 goals");
  });

  // An off-period link is the case the by-id read on the server exists for: it
  // is in neither current-period list, so without the append it would be
  // unticked and un-removable — the picker would silently offer to ADD a goal
  // the job already has.
  it("appends every off-period linked goal after the current ones", async () => {
    const { user, btn } = await contractMount({
      goals: [{ id: 9, text: "last week's push", done: false }],
    });
    await user.click(btn);
    const names = screen.getAllByRole("option").map((el) => el.textContent ?? "");
    const iCurrent = names.findIndex((n) => n.includes("fix intake"));
    const iOff = names.findIndex((n) => n.includes("last week's push"));
    expect(iCurrent).toBeGreaterThan(-1);
    expect(iOff).toBeGreaterThan(iCurrent);
    expect(contractRow(/last week's push/)).toHaveAttribute("aria-selected", "true");
  });

  // active={false} is the archived host: the option list is an offer the
  // server would refuse, but the WRITE path stays live (the colour strip's
  // stance), so the clear row still posts.
  it("skips the fetch when inactive and still writes", async () => {
    const { user, btn } = await contractMount({ goals: undefined, active: false });
    expect(fetchGoals).not.toHaveBeenCalled();
    await user.click(btn);
    await user.click(contractRow(/—/));
    expect(postJobGoal).toHaveBeenCalledWith("j", 0);
  });

  // Two goals whose text truncates to the same label would be a picker that
  // cannot say which one it picked.
  it("disambiguates truncation collisions with the goal id", async () => {
    const shared = "a".repeat(28);
    contractFetchGoalsMock.mockResolvedValueOnce({
      goals: {
        week: [
          { id: 11, scope: "week", period: "2026-08-10", text: shared + "XX", done: false, position: 0 },
          { id: 12, scope: "week", period: "2026-08-10", text: shared + "YY", done: false, position: 1 },
        ],
        day: [],
      },
    });
    const { user, btn } = await contractMount({ goals: undefined });
    await user.click(btn);
    const names = screen.getAllByRole("option").map((el) => el.textContent ?? "");
    expect(names.some((n) => n.includes("#12"))).toBe(true);
  });

  // Optimism with a snap-back: the pending set is the click's instant feedback,
  // and the payload is the truth the moment the write settles. A refused post
  // that left the pending value in place would show a link the server refused.
  it("snaps the pending set back when a post is refused", async () => {
    contractPostJobGoalMock.mockRejectedValueOnce(new Error("nope"));
    const { user, btn } = await contractMount({ goals: undefined });
    await user.click(btn);
    await user.click(contractRow(/ship v3/));
    await waitFor(() => expect(btn).toHaveTextContent("—"));
  });
});
