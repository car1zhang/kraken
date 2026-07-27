// predictReorderedJobs — a client-side simulation of the server's
// within-column reorder, ported from web/static/app.js:1102.
//
// It exists because a same-column drag is the board's ONE optimistic path.
// The card is already where the user dropped it; refetching after the POST
// would repaint the same content and flash. So instead the client computes
// what the next `/api/board` payload is expected to look like, and holds its
// local order until a payload that agrees (or disagrees loudly) arrives.
//
// The simulation itself is jobs/order.go's `ApplyColumnOrder` in miniature:
// `buildWebBoard` groups each display state contiguously and reorders only
// WITHIN it, so the predicted array is the same job objects at the same
// positions, except the members of `column` now follow `slugs`' order.
//
// That contiguity claim holds ONLY for a column whose members all carry the
// matching `state`. Once bucketJobs relocates a health-flagged job (a
// `working` cell rendered in BLOCKED), a column's members are no longer
// contiguous in the flat payload, and the array built below interleaves them
// into scattered positions — it is then not a faithful simulation of any
// payload the server would emit. This is inert today because the ONLY
// consumer, commitReorder, uses the return value as a null/non-null validity
// gate and discards the array itself. Do not start rendering from it without
// fixing this first; the `memberSet` walk below is correct for non-contiguous
// members, but the positions the result lands in are not.
//
// Returns null when `slugs` does not line up with what the client already
// knows — a job appeared, vanished, or moved column between the last poll and
// the drop. The caller must then fall back to a plain refetch rather than
// guess: a wrong prediction is worse than a repaint.

import type { WebJob } from "../api/types.ts";
import type { Column } from "./columns.ts";
import { bucketJobs } from "./format.ts";

// Membership comes from bucketJobs, never from a local `j.state === column`
// test. Those are NOT the same question: bucketJobs relocates an unhealthy
// cell into BLOCKED regardless of its file state (app.js:614), so a job with
// `health` set and `state:"working"` is a member of `blocked`. This file used
// to re-derive membership from `j.state`, and the moment useBoard adopted
// bucketJobs the two disagreed — `members !== reordered.length` in any column
// holding a relocated card, so EVERY prediction there was rejected and the
// optimistic path silently degraded to a flashing refetch. One function,
// asked once, is what stops that from drifting a third time.

/** The slugs of `column`'s active members, in server order. */
export function columnSlugs(jobs: readonly WebJob[], column: Column): string[] {
  return bucketJobs(jobs).byColumn[column].map((j) => j.slug);
}

export function predictReorderedJobs(
  jobs: readonly WebJob[],
  column: Column,
  slugs: readonly string[],
): WebJob[] | null {
  const members = bucketJobs(jobs).byColumn[column];

  // Keyed over the COLUMN's members rather than every active job, which
  // settles two things at once. A slug from another column can no longer be
  // spliced in here (it simply is not found, and the length check below
  // rejects the drop) — and archived entries are already out, which matters
  // because an archived job rides the same array under its numbered archive
  // name: archiving `foo` and creating a new active `foo` puts two entries
  // with slug "foo" in `jobs`, archived appended LAST, so a map over the flat
  // array hands the archived one a last-writer-wins victory and renders an
  // unsteerable card in the live job's slot. `web/jobkey_test.go` pinned this
  // on the vanilla side; predictReorder.test.ts pins it here.
  const bySlug = new Map<string, WebJob>();
  for (const j of members) bySlug.set(j.slug, j);

  const reordered: WebJob[] = [];
  for (const s of slugs) {
    const j = bySlug.get(s);
    if (j) reordered.push(j);
  }
  if (reordered.length !== slugs.length) return null;

  // The vanilla version relied on the column's member count matching
  // `slugs.length` and would have spliced `undefined` into the array if it did
  // not. Checking it is the same "don't guess" rule one step earlier, and it
  // is the difference between a stale prediction and a hole in the board.
  if (members.length !== reordered.length) return null;

  // Identity, not a re-test of the bucketing rule: `memberSet` holds the very
  // objects bucketJobs put in this column.
  const memberSet = new Set<WebJob>(members);
  let i = 0;
  const out: WebJob[] = [];
  for (const j of jobs) {
    if (memberSet.has(j)) {
      // Guarded above: `members.length === reordered.length`, so this never
      // runs off the end.
      out.push(reordered[i++] as WebJob);
    } else {
      out.push(j);
    }
  }
  return out;
}
