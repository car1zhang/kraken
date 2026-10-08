// The mental model's polls.
//
// `useMM` is App's ONE unfiltered list (the Model pane renders it), hoisted
// there for `useLibrary`'s reason; the pane runs a second, FILTERED instance of
// the same hook while its filter box holds text, because a `?q=` result is a
// different list rather than a view of the first. `useMMFlags` is the raised
// contradiction flags the pane lists above its rows.

import { fetchMM, fetchMMFlags } from "../api/client.ts";
import type { MMFlag, MMFlagsPage, MMItem, MMPage, MMQuery } from "../lib/mm.ts";
import { usePoll } from "./usePoll.ts";

/**
 * 30s, the library poll's interval.
 *
 * Almost nothing writes this table on a click in this tab any more: a row is
 * committed by a mind thread (`kraken mm commit`), and the daemon's verify
 * pass invalidates or flags. The interval is what brings either in.
 */
export const MM_POLL_MS = 30000;

export interface MMModel {
  items: MMItem[];
  /** Last tick's failure, if any. The pane shows it in place of its empty state. */
  error: Error | undefined;
  /** True once any response has landed — distinguishes "empty" from "loading". */
  loaded: boolean;
  /** Pull the list now. Every write awaits this so the pane moves at once. */
  refetch: () => Promise<boolean>;
}

/**
 * Poll the mental model while `active`, narrowed by `query` (`?q=&repo=&dir=
 * &scope=`; empty fields are no filter).
 *
 * Every absence is tolerated rather than thrown on — a route that 404s (an
 * older server), a `{}` body, or an `items` that arrived null all come out as
 * an empty list plus the error, never as a crashed view. The last good list
 * stays up under a failed tick rather than blanking.
 *
 * The query is read through the fetcher on each tick (usePoll holds the
 * fetcher in a ref), so a caller whose query changes should `refetch` to land
 * the new list without waiting out the interval.
 */
export const useMM = (active: boolean, query: MMQuery = {}): MMModel => {
  const { data, error, refetch } = usePoll<MMPage>(
    (signal) => fetchMM(signal, query),
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

export interface MMFlagsModel {
  flags: MMFlag[];
  error: Error | undefined;
  refetch: () => Promise<boolean>;
}

/**
 * Poll the RAISED contradiction flags while `active` — the ones zeus put in
 * front of carl. Pending flags are zeus' business and dismissed ones are
 * closed, so neither reaches the pane. Same tolerance rules as `useMM`.
 */
export const useMMFlags = (active: boolean): MMFlagsModel => {
  const { data, error, refetch } = usePoll<MMFlagsPage>(
    (signal) => fetchMMFlags("raised", signal),
    MM_POLL_MS,
    { enabled: active },
  );
  return {
    // A resolved flag moves to `state: "resolved"` server-side (contract
    // amendment 2), so `?state=raised` should serve open flags only. This
    // filter is a belt-and-braces guard: a flag carrying a `resolution` is
    // closed whatever its state says.
    flags: Array.isArray(data?.flags)
      ? data.flags.filter((f) => !f.resolution)
      : [],
    error,
    refetch,
  };
};
