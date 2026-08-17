// Goals — the wire item, the grouping the panel renders, the one-line chip
// truncation the cards and the steer head share, and the per-goal job count
// the panel's badges and the board's filter agree on.
//
// A LEAF module: no React, no fetch, and its ONE import is type-only
// (`WebJob`, for the count below), so nothing is pulled into the bundle and
// the api/types.ts ↔ here pair is not a runtime cycle — that file imports
// `Goal` back from here the same way. Everything is exported because it is
// what the panel, the card chip, the board view, the picker and the steer
// head all read — and because a pure function nobody can name is a pure
// function nobody can test.
//
// `Goal` mirrors the wire item served by GET /api/goals and by the board
// payload's `goals` key, field for field (internal/api/goals.go). It lives
// HERE rather than in api/types.ts even though it is a wire shape, because
// `groupGoals` is its only real consumer and one definition beats two that can
// drift; api/types.ts imports it back for the board payload.

import type { WebJob } from "../api/types.ts";

/**
 * One `goals` row as it arrives on the wire.
 *
 * `parent_id` is omitted (never 0) when the goal has no parent — the server
 * drops the key rather than sending a zero, so absence IS the no-parent state
 * and `parent_id === 0` never arrives. Nothing here relies on that: a 0 that
 * somehow did arrive matches no week goal's id and lands in `unassigned`,
 * which is the same answer.
 *
 * Only `day` goals ever carry one (the store refuses a parent on a week goal),
 * so the nesting is exactly one level deep and always week→day.
 */
export interface Goal {
  id: number;
  scope: "week" | "day";
  /** the period key: a calendar date `2006-01-02`, a Monday for `week` */
  period: string;
  text: string;
  done: boolean;
  position: number;
  parent_id?: number;
}

/** One week goal and the day goals nested under it, in input order. */
export interface WeekGroup {
  goal: Goal;
  days: Goal[];
}

/**
 * What the panel renders: the week groups, the day goals that belong to no
 * week goal, and the two done/total pairs the collapsed pill shows.
 *
 * The counts are over the INPUTS, not over what got grouped — `dayDone` /
 * `dayTotal` count nested and unassigned day goals alike — so the pill's
 * numbers cannot disagree with the rows below it.
 */
export interface GoalGroups {
  week: WeekGroup[];
  unassigned: Goal[];
  counts: {
    weekDone: number;
    weekTotal: number;
    dayDone: number;
    dayTotal: number;
  };
}

/**
 * Group the two flat wire arrays into the panel's shape.
 *
 * Order is INPUT order throughout — the server lists by position then id, and
 * that ordering is the one carl arranged, so nothing here sorts. A day goal
 * lands under the week goal whose id its `parent_id` matches; a day goal with
 * no `parent_id`, or with one matching no week goal IN THIS INPUT, lands in
 * `unassigned`.
 *
 * That second case is not defensive padding. The panel is fed the CURRENT
 * week and day, so a day goal parented to LAST week's goal arrives with a
 * parent that is not on screen — dropping it would lose a goal silently, and
 * unassigned is where it can still be read and ticked off.
 */
export function groupGoals(week: Goal[], day: Goal[]): GoalGroups {
  const groups: WeekGroup[] = week.map((goal) => ({ goal, days: [] }));
  const byId = new Map<number, WeekGroup>();
  for (const g of groups) byId.set(g.goal.id, g);

  const unassigned: Goal[] = [];
  for (const d of day) {
    const parent = d.parent_id ? byId.get(d.parent_id) : undefined;
    if (parent) parent.days.push(d);
    else unassigned.push(d);
  }

  return {
    week: groups,
    unassigned,
    counts: {
      weekDone: week.filter((g) => g.done).length,
      weekTotal: week.length,
      dayDone: day.filter((g) => g.done).length,
      dayTotal: day.length,
    },
  };
}

/**
 * How many of these jobs link each goal — goal id → count, ids with no jobs
 * simply ABSENT rather than present at zero, which is how a panel row knows to
 * draw no badge at all.
 *
 * Reads `goals` and ONLY `goals`, never the legacy single `goal` that rides
 * beside it as the array's head. That is the whole point of the function and
 * not a shortcut: the board's goal filter matches on `goals` alone, so a count
 * that folded in a `goal`-only job would promise more cards than clicking the
 * badge delivers — a number that disagrees with the board it sits beside.
 * A job with no `goals` therefore contributes nothing.
 *
 * The caller decides what "these jobs" means; BoardView passes the payload's
 * whole flat array, active and archived alike, because an archived job links a
 * goal like any other and the archive drawer is on that same board.
 */
export function goalJobCounts(jobs: WebJob[]): Map<number, number> {
  const counts = new Map<number, number>();
  for (const job of jobs) {
    for (const g of job.goals ?? []) {
      counts.set(g.id, (counts.get(g.id) ?? 0) + 1);
    }
  }
  return counts;
}

/** The chip's character budget. One number, read by every chip renderer. */
const GOAL_CHIP_MAX = 24;

/**
 * A goal's text cut to chip width.
 *
 * Short text is returned UNCHANGED — no ellipsis on something that already
 * fits, and no copy is made. Longer text is the first `max` characters with
 * any trailing whitespace shaved off (a cut landing in a space would render
 * as a floating "…") plus a single ellipsis character, so the result is never
 * longer than `max + 1`.
 *
 * CHARACTERS, not UTF-16 code units: both the length test and the cut run over
 * `Array.from(text)`, which iterates by code point. A bare `.length` /
 * `.slice()` pair counts an emoji as two and can cut BETWEEN its surrogate
 * halves, putting a lone `�` on the card — and a goal is free text carl
 * types, so an emoji in one is a matter of time. For ASCII (which every
 * measurement in this app's own strings is) the two spellings agree exactly.
 *
 * It stops at code points and does NOT walk grapheme clusters: a ZWJ emoji
 * family or a combining accent can still be split into pieces that render
 * separately. That is a deliberate floor — `Intl.Segmenter` is the only honest
 * fix and it is a lot of machinery for a 24-character chip — and it can never
 * produce the lone-surrogate `�` this cut exists to avoid.
 *
 * The full text is never dropped on the floor: every call site puts it in the
 * element's `title`.
 */
export function goalChipText(text: string, max: number = GOAL_CHIP_MAX): string {
  const chars = Array.from(text);
  if (chars.length <= max) return text;
  return chars.slice(0, max).join("").replace(/\s+$/, "") + "…";
}
