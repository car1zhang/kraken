import { describe, expect, it } from "vitest";

import { type Goal, goalChipText, groupGoals } from "./goals.ts";

// goals.contract.test.ts — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S3's pure half: groupGoals and goalChipText.
//
// Written from the contract text alone; goals.ts, GoalsPanel.tsx, JobCard.tsx,
// types.ts and client.ts were never read in any form. A blind TypeScript suite
// can only reach exports, so the panel's DOM (collapse pill, localStorage,
// inline CRUD, the card chip's markup) is out of reach here and is disclosed
// in the report rather than probed loosely through a render.
//
// Dimension: LOGIC — the stated grouping/ordering/counting rules and the chip
// truncation's happy paths. Boundary and hostile inputs are athena-edge's.
//
// Every module-scope identifier declared here is prefixed `contract`.

/** contractGoal builds one wire item; parent_id is omitted unless given, which
 * is exactly how the Go handler serializes it. */
function contractGoal(
  id: number,
  scope: "week" | "day",
  text: string,
  extra: { done?: boolean; position?: number; parent_id?: number } = {},
): Goal {
  const g: Goal = {
    id,
    scope,
    period: scope === "week" ? "2026-08-10" : "2026-08-13",
    text,
    done: extra.done ?? false,
    position: extra.position ?? id,
  };
  if (extra.parent_id !== undefined) g.parent_id = extra.parent_id;
  return g;
}

const contractTexts = (gs: Goal[]) => gs.map((g) => g.text);

describe("groupGoals", () => {
  // The empty board is the shape the panel mounts with on a fresh install, and
  // the one a `.map` over an undefined key crashes on.
  it("returns empty groups and zero counts for empty inputs", () => {
    const got = groupGoals([], []);
    expect(got.week).toEqual([]);
    expect(got.unassigned).toEqual([]);
    expect(got.counts).toEqual({
      weekDone: 0,
      weekTotal: 0,
      dayDone: 0,
      dayTotal: 0,
    });
  });

  // Order is the contract's word: the panel renders these top to bottom and the
  // server already sorted them by position. Re-sorting here (by id, by text, by
  // done) would make the rows shuffle relative to every other client.
  it("preserves the input order of the week lane", () => {
    const week = [
      contractGoal(30, "week", "third by position", { position: 3 }),
      contractGoal(10, "week", "first by position", { position: 1 }),
      contractGoal(20, "week", "second by position", { position: 2 }),
    ];
    const got = groupGoals(week, []);
    expect(got.week.map((w) => w.goal.text)).toEqual([
      "third by position",
      "first by position",
      "second by position",
    ]);
  });

  // A day goal lands under the week goal its parent_id names — this is the
  // whole point of the column, and what the `↳` rows render from.
  it("nests a day goal under the week goal its parent_id matches", () => {
    const parent = contractGoal(1, "week", "the weekly");
    const other = contractGoal(2, "week", "another weekly");
    const child = contractGoal(11, "day", "today's step", { parent_id: 1 });

    const got = groupGoals([parent, other], [child]);
    expect(got.week).toHaveLength(2);
    expect(got.week[0]!.goal.id).toBe(1);
    expect(contractTexts(got.week[0]!.days)).toEqual(["today's step"]);
    expect(got.week[1]!.goal.id).toBe(2);
    expect(got.week[1]!.days).toEqual([]);
    expect(got.unassigned).toEqual([]);
  });

  // Within one group, order is again the input's — the day lane arrives already
  // positioned, and a group is a filter over it, not a re-sort.
  it("preserves input order within a group's days", () => {
    const parent = contractGoal(1, "week", "the weekly");
    const day = [
      contractGoal(13, "day", "c", { parent_id: 1, position: 3 }),
      contractGoal(11, "day", "a", { parent_id: 1, position: 1 }),
      contractGoal(12, "day", "b", { parent_id: 1, position: 2 }),
    ];
    const got = groupGoals([parent], day);
    expect(contractTexts(got.week[0]!.days)).toEqual(["c", "a", "b"]);
  });

  // Two ways to be unassigned, and the contract names both: no parent_id at
  // all, and a parent_id naming no week goal IN THE INPUT — which is the live
  // case where a day goal's parent belongs to a period the panel is not
  // showing. Both land in the same group rather than vanishing.
  it("puts unparented and dangling-parent day goals in unassigned, in input order", () => {
    const parent = contractGoal(1, "week", "the weekly");
    const day = [
      contractGoal(11, "day", "no parent at all"),
      contractGoal(12, "day", "nested", { parent_id: 1 }),
      contractGoal(13, "day", "parent not in this week's list", { parent_id: 999 }),
    ];
    const got = groupGoals([parent], day);
    expect(contractTexts(got.week[0]!.days)).toEqual(["nested"]);
    expect(contractTexts(got.unassigned)).toEqual([
      "no parent at all",
      "parent not in this week's list",
    ]);
  });

  // Every day goal is somewhere: nested or unassigned, never dropped.
  it("accounts for every day goal exactly once", () => {
    const week = [contractGoal(1, "week", "w1"), contractGoal(2, "week", "w2")];
    const day = [
      contractGoal(11, "day", "d1", { parent_id: 1 }),
      contractGoal(12, "day", "d2", { parent_id: 2 }),
      contractGoal(13, "day", "d3"),
      contractGoal(14, "day", "d4", { parent_id: 77 }),
    ];
    const got = groupGoals(week, day);
    const placed = [
      ...got.week.flatMap((w) => w.days.map((d) => d.id)),
      ...got.unassigned.map((d) => d.id),
    ].sort((a, b) => a - b);
    expect(placed).toEqual([11, 12, 13, 14]);
  });

  // Counts are the pill's whole content when the panel is collapsed. They are
  // per SCOPE over the INPUTS — so the day counts span nested and unassigned
  // alike, which a naive "count what I placed in groups" implementation gets
  // wrong the moment a day goal is unassigned.
  it("counts done/total per scope, with day counts spanning nested and unassigned", () => {
    const week = [
      contractGoal(1, "week", "w done", { done: true }),
      contractGoal(2, "week", "w open"),
      contractGoal(3, "week", "w open too"),
    ];
    const day = [
      contractGoal(11, "day", "d nested done", { parent_id: 1, done: true }),
      contractGoal(12, "day", "d nested open", { parent_id: 2 }),
      contractGoal(13, "day", "d loose done", { done: true }),
      contractGoal(14, "day", "d loose open"),
    ];
    expect(groupGoals(week, day).counts).toEqual({
      weekDone: 1,
      weekTotal: 3,
      dayDone: 2,
      dayTotal: 4,
    });
  });

  // A done goal still renders (struck through) for the rest of its period, so
  // it must still be grouped — counting is not filtering.
  it("keeps done goals in their groups", () => {
    const week = [contractGoal(1, "week", "done weekly", { done: true })];
    const day = [contractGoal(11, "day", "done daily", { parent_id: 1, done: true })];
    const got = groupGoals(week, day);
    expect(got.week).toHaveLength(1);
    expect(contractTexts(got.week[0]!.days)).toEqual(["done daily"]);
  });
});

describe("goalChipText", () => {
  // The chip's job is to fit on a card band; a text that already fits is passed
  // through untouched, with no ellipsis bolted on.
  it("returns text unchanged when it is not longer than max", () => {
    expect(goalChipText("short goal")).toBe("short goal");
    const exactly24 = "123456789012345678901234";
    expect(exactly24).toHaveLength(24);
    expect(goalChipText(exactly24)).toBe(exactly24);
    expect(goalChipText("")).toBe("");
  });

  // The default is 24 and it is the one every card uses, so it is pinned as a
  // value rather than inferred from whatever the call site happens to pass.
  it("defaults max to 24", () => {
    const long = "abcdefghijklmnopqrstuvwxyz0123456789";
    expect(goalChipText(long)).toBe(long.slice(0, 24) + "…");
    expect(goalChipText(long)).toBe(goalChipText(long, 24));
  });

  // "the first max characters (no trailing-space) plus …" — the no-trailing-
  // space clause is the whole reason this is a function rather than a CSS
  // ellipsis: "deploy the new  " + "…" reads as a typo, "deploy the new…" reads
  // as a truncation.
  it("drops a trailing space before the ellipsis", () => {
    // built so the naive first-24 slice ENDS on a space: 23 letters, then the
    // space at index 23, then more text past the cut.
    const head = "a".repeat(23);
    const text = `${head} tail beyond the cut`;
    expect(text.slice(0, 24)).toBe(`${head} `);
    expect(goalChipText(text)).toBe(`${head}…`);
    expect(goalChipText(text).endsWith(" …")).toBe(false);
  });

  // The chip is laid out against a fixed budget, so the ceiling is arithmetic,
  // not a suggestion: max characters plus the one-character ellipsis.
  it("never returns more than max + 1 characters", () => {
    const long = "x".repeat(200);
    for (const max of [1, 5, 24, 60]) {
      expect(goalChipText(long, max).length).toBeLessThanOrEqual(max + 1);
    }
    expect(goalChipText(long, 8)).toBe("xxxxxxxx…");
  });

  // An explicit max is obeyed — the steer modal and the card do not have to
  // share one width.
  it("honours an explicit max", () => {
    const text = "rewrite the tmux pane discovery loop";
    expect(goalChipText(text, 10)).toBe(text.slice(0, 10).trimEnd() + "…");
    expect(goalChipText(text, text.length)).toBe(text);
  });
});
