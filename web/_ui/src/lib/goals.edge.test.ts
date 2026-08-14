import { describe, expect, it } from "vitest";

import { type Goal, goalChipText, groupGoals } from "./goals.ts";

// goals.edge.test.ts — the EDGE dimension of 50-contract.md's Slice S3 pure
// functions: what groupGoals does with input that does not fit the happy shape,
// and where goalChipText's truncation actually falls. Written blind from the
// contract text, with no sight of web/_ui/src/lib/goals.ts.
//
// The sibling blind author owns the stated invariants (input order preserved,
// a day goal landing under its matching week goal). What lives here is the
// other half: the empty board, the dangling parent, and the two characters
// either side of the chip's limit.
//
// Every helper is prefixed `edgeGoal` — a second blind author is writing beside
// this file.

let edgeGoalNextID = 1;

function edgeGoalWeek(text: string, over: Partial<Goal> = {}): Goal {
  return {
    id: edgeGoalNextID++,
    scope: "week",
    period: "2026-08-10",
    text,
    done: false,
    position: 0,
    ...over,
  };
}

function edgeGoalDay(text: string, over: Partial<Goal> = {}): Goal {
  return {
    id: edgeGoalNextID++,
    scope: "day",
    period: "2026-08-13",
    text,
    done: false,
    position: 0,
    ...over,
  };
}

const edgeGoalTexts = (gs: Goal[]) => gs.map((g) => g.text);

// ---------------------------------------------------------------------------
// groupGoals: the empty board
// ---------------------------------------------------------------------------

describe("groupGoals: empty inputs", () => {
  // The state a fresh install is in, and therefore the first render every user
  // ever sees. Empty ARRAYS, not undefined: the panel maps over all three of
  // these, and a null would take the whole board view down rather than draw an
  // empty panel.
  it("returns empty groups and zeroed counts for two empty arrays", () => {
    const g = groupGoals([], []);
    expect(g.week).toEqual([]);
    expect(g.unassigned).toEqual([]);
    expect(g.counts).toEqual({
      weekDone: 0,
      weekTotal: 0,
      dayDone: 0,
      dayTotal: 0,
    });
  });

  it("week goals with no day goals produce empty days arrays, never undefined", () => {
    const w1 = edgeGoalWeek("ship goal tracking");
    const w2 = edgeGoalWeek("clear the PR queue");
    const g = groupGoals([w1, w2], []);
    expect(g.week.map((x) => x.goal.text)).toEqual([
      "ship goal tracking",
      "clear the PR queue",
    ]);
    expect(g.week.map((x) => x.days)).toEqual([[], []]);
    expect(g.unassigned).toEqual([]);
    expect(g.counts).toEqual({
      weekDone: 0,
      weekTotal: 2,
      dayDone: 0,
      dayTotal: 0,
    });
  });

  it("day goals with no week goals all land in unassigned", () => {
    const d1 = edgeGoalDay("review the diff");
    const d2 = edgeGoalDay("answer carl");
    const g = groupGoals([], [d1, d2]);
    expect(g.week).toEqual([]);
    expect(edgeGoalTexts(g.unassigned)).toEqual(["review the diff", "answer carl"]);
    expect(g.counts.dayTotal).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// groupGoals: the dangling parent
// ---------------------------------------------------------------------------

describe("groupGoals: a parent_id that matches nothing", () => {
  // The realistic producer: yesterday's day goal points at a week goal from a
  // week that is no longer being listed, or carl deletes a week goal in one tab
  // while another tab still holds the old payload. A day goal that vanished
  // from every group would be work silently dropped off the board — so it falls
  // back to unassigned, exactly as one with no parent at all does.
  it("puts a day goal whose parent_id matches no week goal into unassigned", () => {
    const w = edgeGoalWeek("the week's theme");
    const orphan = edgeGoalDay("orphaned child", { parent_id: 987654 });
    const g = groupGoals([w], [orphan]);
    expect(g.week).toHaveLength(1);
    expect(g.week[0]!.days).toEqual([]);
    expect(edgeGoalTexts(g.unassigned)).toEqual(["orphaned child"]);
  });

  it("treats a parent_id of 0 as no parent", () => {
    // 0 is the store's "no parent" and the key the wire omits; a client that
    // sends it explicitly must not be searching for a week goal with id 0.
    const w = edgeGoalWeek("the week's theme");
    const zero = edgeGoalDay("no parent, spelled out", { parent_id: 0 });
    const g = groupGoals([w], [zero]);
    expect(g.week[0]!.days).toEqual([]);
    expect(edgeGoalTexts(g.unassigned)).toEqual(["no parent, spelled out"]);
  });

  it("keeps input order inside unassigned when parented and orphaned goals interleave", () => {
    const w = edgeGoalWeek("the week's theme");
    const a = edgeGoalDay("a", { parent_id: 987654 });
    const b = edgeGoalDay("b", { parent_id: w.id });
    const c = edgeGoalDay("c");
    const d = edgeGoalDay("d", { parent_id: 987655 });
    const g = groupGoals([w], [a, b, c, d]);
    expect(edgeGoalTexts(g.week[0]!.days)).toEqual(["b"]);
    expect(edgeGoalTexts(g.unassigned)).toEqual(["a", "c", "d"]);
  });
});

// ---------------------------------------------------------------------------
// groupGoals: counts with mixed done
// ---------------------------------------------------------------------------

describe("groupGoals: counts", () => {
  // The counts are what the collapsed pill shows, so they are the only thing a
  // collapsed panel says at all. The rule under test is that DAY counts span
  // both lanes — nested and unassigned alike — because "today 1/3" that only
  // counted nested goals would read as progress carl has not made.
  it("counts done/total per scope across nested and unassigned day goals", () => {
    const w1 = edgeGoalWeek("done week goal", { done: true });
    const w2 = edgeGoalWeek("open week goal");
    const w3 = edgeGoalWeek("another open week goal");
    const nestedDone = edgeGoalDay("nested and done", { parent_id: w1.id, done: true });
    const nestedOpen = edgeGoalDay("nested and open", { parent_id: w2.id });
    const looseDone = edgeGoalDay("loose and done", { done: true });
    const looseOpen = edgeGoalDay("loose and open", { parent_id: 987654 });

    const g = groupGoals(
      [w1, w2, w3],
      [nestedDone, nestedOpen, looseDone, looseOpen],
    );

    expect(g.counts).toEqual({
      weekDone: 1,
      weekTotal: 3,
      dayDone: 2,
      dayTotal: 4,
    });
    expect(edgeGoalTexts(g.week[0]!.days)).toEqual(["nested and done"]);
    expect(edgeGoalTexts(g.week[1]!.days)).toEqual(["nested and open"]);
    expect(g.week[2]!.days).toEqual([]);
    expect(edgeGoalTexts(g.unassigned)).toEqual(["loose and done", "loose and open"]);
  });

  it("counts every goal done as done, and none as none", () => {
    const allDone = groupGoals(
      [edgeGoalWeek("w", { done: true })],
      [edgeGoalDay("d", { done: true })],
    );
    expect(allDone.counts).toEqual({
      weekDone: 1,
      weekTotal: 1,
      dayDone: 1,
      dayTotal: 1,
    });
    const noneDone = groupGoals([edgeGoalWeek("w")], [edgeGoalDay("d")]);
    expect(noneDone.counts).toEqual({
      weekDone: 0,
      weekTotal: 1,
      dayDone: 0,
      dayTotal: 1,
    });
  });
});

// ---------------------------------------------------------------------------
// goalChipText: the two characters either side of the limit
// ---------------------------------------------------------------------------

describe("goalChipText", () => {
  const MAX = 24;

  it("defaults max to 24", () => {
    const exactly = "a".repeat(MAX);
    expect(goalChipText(exactly)).toBe(exactly);
    expect(goalChipText("a".repeat(MAX + 1))).toBe("a".repeat(MAX) + "…");
  });

  it("returns text unchanged at exactly max", () => {
    // The boundary a `>` / `>=` slip moves by one: at exactly the limit the
    // text FITS, and adding an ellipsis here would truncate a chip that had
    // room for every character it was hiding.
    for (const max of [1, 5, MAX, 40]) {
      const text = "x".repeat(max);
      expect(goalChipText(text, max)).toBe(text);
    }
    expect(goalChipText("", MAX)).toBe("");
    expect(goalChipText("short", MAX)).toBe("short");
  });

  it("truncates at max + 1 and never returns more than max + 1 characters", () => {
    for (const max of [1, 5, MAX, 40]) {
      const text = "y".repeat(max + 1);
      const out = goalChipText(text, max);
      expect(out).toBe("y".repeat(max) + "…");
      expect(out.endsWith("…")).toBe(true);
      expect(out.length).toBeLessThanOrEqual(max + 1);
    }
  });

  it("never exceeds max + 1 however long the input is", () => {
    const huge = "z".repeat(5000);
    expect(goalChipText(huge, MAX).length).toBeLessThanOrEqual(MAX + 1);
    expect(goalChipText(huge).length).toBeLessThanOrEqual(MAX + 1);
    expect(goalChipText("word ".repeat(400)).length).toBeLessThanOrEqual(MAX + 1);
  });

  it("drops a trailing space before the ellipsis", () => {
    // "the first max characters (no trailing-space) plus …": a cut that lands
    // just after a word boundary would otherwise render as "word …", which
    // reads as a deliberate gap rather than as elision.
    const text = "a".repeat(MAX - 1) + " tail";
    expect(text.slice(0, MAX)).toBe("a".repeat(MAX - 1) + " ");
    const out = goalChipText(text, MAX);
    expect(out).toBe("a".repeat(MAX - 1) + "…");
    expect(out.length).toBeLessThanOrEqual(MAX + 1);
  });
});
