// The mental model's ONE poll, hoisted to App for the reason `useLibrary` is:
// four surfaces read the same list and must never disagree about it. The
// artifact viewer's ✓ read button IS `artifactReadMatch(items, url) !== null`,
// its table ✓s are a membership test over the same items, the steer modal's
// → decision and detail marks are two more, and the memory view's MENTAL MODEL
// pane renders the whole list beside them. Two polls would be two answers to
// "have I read this", and a button would go on claiming a mark the pane it
// sits beside had just dropped.

import { fetchMM } from "../api/client.ts";
import type { MMItem, MMPage } from "../lib/mm.ts";
import { usePoll } from "./usePoll.ts";

/**
 * 30s, the library poll's interval and its argument verbatim.
 *
 * Almost nothing writes this table on its own: a row appears when carl marks
 * something and every one of those goes through `refetch` below, which lands
 * the change at once. The interval is the backstop for the two writers that
 * are NOT a click in this tab — another tab (or `k mm mark`, fired from the
 * editor by a keystroke), and the daemon's verify pass, which invalidates a
 * mark when the thing it was made against moves. The second one is why the
 * interval matters more here than it does for the library: a ✓ that has
 * silently gone stale is a claim, not just an absence.
 */
export const MM_POLL_MS = 30000;

export interface MMModel {
  items: MMItem[];
  /** Last tick's failure, if any. The pane shows it in place of its empty state. */
  error: Error | undefined;
  /** True once any response has landed — distinguishes "empty" from "loading". */
  loaded: boolean;
  /** Pull the list now. Every mark/remove awaits this so the control flips at once. */
  refetch: () => Promise<boolean>;
}

/**
 * Poll the mental model while `active`.
 *
 * Every absence is tolerated rather than thrown on — a route that 404s (an
 * older server), a `{}` body, or an `items` that arrived null all come out as
 * an empty list plus the error, never as a crashed view. An empty list is
 * also the honest degradation for the capture surfaces: nothing reads as
 * marked, and pressing a control posts, which is the same behaviour as a
 * genuinely empty mental model.
 *
 * The last good list stays up under a failed tick rather than blanking, which
 * is `useLibrary`'s trade for `useLibrary`'s reason: blanking swaps a list
 * that is at worst one tick stale for no list at all, over a read this client
 * can neither confirm nor deny.
 */
export const useMM = (active: boolean): MMModel => {
  const { data, error, refetch } = usePoll<MMPage>(
    (signal) => fetchMM(signal),
    MM_POLL_MS,
    { enabled: active },
  );
  return {
    items: Array.isArray(data?.items) ? data.items : [],
    error,
    loaded: data !== undefined,
    refetch,
  };
};
