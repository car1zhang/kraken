// The mental model's polls.
//
// `useMM` is App's ONE unfiltered list (the Model pane renders it), hoisted
// there for `useLibrary`'s reason; the pane runs a second, FILTERED instance of
// the same hook while its filter box holds text, because a `?q=` result is a
// different list rather than a view of the first. `useMMFlags` is the raised
// contradiction flags the pane lists above its rows; `useMMRuns` and
// `useMMMarks` are the mind runs and the pending read marks it lists too.

import { fetchMM, fetchMMFlags, fetchMMMarks, fetchMMRuns } from "../api/client.ts";
import type { WebMindRun, WebMMMark } from "../api/types.ts";
import type { MMFlag, MMFlagsPage, MMItem, MMPage, MMQuery } from "../lib/mm.ts";
import { usePoll } from "./usePoll.ts";

/**
 * 30s, the library poll's interval.
 *
 * Almost nothing writes this table on a click in this tab any more: a row is
 * written by a mind run inside kraken serve, and the daemon's verify pass
 * invalidates or flags. The interval is what brings either in.
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

export interface MMRunsModel {
  runs: WebMindRun[];
  /** True until the first response (or failure) lands. */
  loading: boolean;
  error: Error | undefined;
  refetch: () => Promise<boolean>;
}

/**
 * Poll the mind runs while `active` — every job's, or one job's when `slug`
 * is given. The list never carries a blob; a run's report, answer or bundle
 * is fetched on demand (`fetchMMRun`). Same tolerance rules as `useMM`.
 */
export const useMMRuns = (active: boolean, slug?: string): MMRunsModel => {
  const { data, error, refetch } = usePoll<{ runs: WebMindRun[] }>(
    (signal) => fetchMMRuns(slug, undefined, signal),
    MM_POLL_MS,
    { enabled: active },
  );
  return {
    runs: Array.isArray(data?.runs) ? data.runs : [],
    loading: data === undefined && error === undefined,
    error,
    refetch,
  };
};

export interface MMMarksModel {
  marks: WebMMMark[];
  /** True until the first response (or failure) lands. */
  loading: boolean;
  error: Error | undefined;
  refetch: () => Promise<boolean>;
}

/**
 * Poll the PENDING read marks of every job while `active` — what the next
 * mind runs will read. A consumed mark belongs to the run that read it and
 * is not listed. Same tolerance rules as `useMM`.
 */
export const useMMMarks = (active: boolean): MMMarksModel => {
  const { data, error, refetch } = usePoll<{ marks: WebMMMark[] }>(
    (signal) => fetchMMMarks(undefined, true, signal),
    MM_POLL_MS,
    { enabled: active },
  );
  return {
    // belt and braces: a mark a run has consumed is not pending, whatever
    // the server's filter did
    marks: Array.isArray(data?.marks) ? data.marks.filter((m) => !m.consumed_run) : [],
    loading: data === undefined && error === undefined,
    error,
    refetch,
  };
};
