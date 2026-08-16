// The saved-artifact library's ONE poll, hoisted to App because two surfaces
// read the same list and must never disagree about it: the memory view's
// library mode renders it, and the artifact viewer's ☆/★ head button IS
// `libraryMatch(items, url) !== null` over it. Two polls would be two answers
// to "is this saved", and the button would lie for up to a full interval after
// a removal made from the list beside it.

import { fetchLibrary } from "../api/client.ts";
import type { LibraryPage, WebLibraryItem } from "../api/types.ts";
import { usePoll } from "./usePoll.ts";

/**
 * 30s, the graph poll's interval and its argument.
 *
 * Nothing in kraken writes this table on its own: a row appears only when carl
 * presses ☆ save and disappears only when he removes one, and BOTH of those go
 * through `refetch` below, which lands the change immediately. The interval is
 * therefore not how the list stays current — it is only the backstop for
 * another tab (or the phone) having saved something, which is a "you will not
 * have to reload" promise rather than a timeliness one.
 */
export const LIBRARY_POLL_MS = 30000;

export interface LibraryModel {
  items: WebLibraryItem[];
  /** Last tick's failure, if any. The list shows it in place of its empty state. */
  error: Error | undefined;
  /** True once any response has landed — distinguishes "empty" from "loading". */
  loaded: boolean;
  /** Pull the list now. Every save/remove awaits this so the row moves at once. */
  refetch: () => Promise<boolean>;
}

/**
 * Poll the library while `enabled`.
 *
 * Every absence is tolerated rather than thrown on — a route that 404s (an
 * older server), a `{}` body, or an `items` that arrived null all come out as
 * an empty list plus the error, never as a crashed view.
 *
 * The last good list stays up under a failed tick rather than blanking the pane
 * (DreamView's opposite rule). NOT because the data cannot have moved — the
 * interval's own note above says another tab or the phone can move it — but
 * because of what the two failures cost: blanking trades a list that is at
 * worst one 30s tick stale for no list at all, over a read this client can
 * neither confirm nor deny. `error` is served alongside so the pane can say so
 * where it has room to — it stands in for the empty state when there is nothing
 * to show.
 */
export function useLibrary(enabled: boolean): LibraryModel {
  const { data, error, refetch } = usePoll<LibraryPage>(
    (signal) => fetchLibrary(signal),
    LIBRARY_POLL_MS,
    { enabled },
  );
  return {
    items: Array.isArray(data?.items) ? data.items : [],
    error,
    loaded: data !== undefined,
    refetch,
  };
}
