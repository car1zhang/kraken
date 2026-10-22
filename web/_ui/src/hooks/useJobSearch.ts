import { useMemo, useState } from "react";

import type { WebJob } from "../api/types.ts";
import { COLUMNS, type Column } from "../lib/columns.ts";
import { jobKey } from "../lib/jobKey.ts";
import { matchJob, parseTerms, type JobMatch } from "../lib/jobSearch.ts";
import type { BoardModel } from "./useBoard.ts";

export interface JobSearchModel {
  /** The raw query, exactly as typed — the input is controlled by it. */
  query: string;
  setQuery(query: string): void;
  /** parseTerms(query): trimmed, split, lowercased. Empty while idle. */
  terms: readonly string[];
  /** The FILTERED render lists. Never board.byColumn — see the docblock. */
  byColumn: Record<Column, WebJob[]>;
  archived: WebJob[];
  /**
   * jobKey() → why that job survived a TEXT search. Empty while idle, since
   * nothing was filtered — and empty under a goal filter with no query, since
   * a card kept by its goal has no matched words to explain or to mark.
   */
  reasonByKey: ReadonlyMap<string, JobMatch["reason"]>;
  /**
   * The jobKeys currently on screen. Empty while idle — ask `active` first,
   * since "nothing is rendered" and "everything is rendered" share that value.
   *
   * The drag layer reads this, and ONLY this: it is a set of keys, not a set
   * of jobs and not a narrowed model, so nothing that consumes it can end up
   * posting a short column (see the docblock).
   */
  visibleKeys: ReadonlySet<string>;
  /** Jobs kept, and jobs on the board — the `N of M jobs` readout. */
  matched: number;
  total: number;
  /**
   * Something is narrowing the board: a non-whitespace query, a goal filter,
   * or both. Everything cosmetic keys off this.
   */
  active: boolean;
  /**
   * The goal id these lists were filtered by, echoed straight from the
   * argument (with `undefined` normalised to `null`) so that a consumer
   * holding only this model can tell WHY the lists are short.
   *
   * Nothing renders it today: App owns the filter state and hands it to the
   * bar and the panel directly, so both of those read the prop rather than
   * this field. It is here because a model that reports `active: true` while
   * carrying no sign of the goal half would make "narrowed by a query" and
   * "narrowed by a goal" indistinguishable to anyone downstream — and because
   * the contract freezes it as the model's one statement of the filter.
   */
  goalFilter: number | null;
}

/** Shared, never written: the idle return needs stable empty collections. */
const NO_REASONS: ReadonlyMap<string, JobMatch["reason"]> = new Map();
const NO_KEYS: ReadonlySet<string> = new Set();

/**
 * The board's search state, and the SECOND view it derives from the board.
 *
 * The load-bearing word is "second". This hook must never mutate, wrap,
 * replace or narrow the BoardModel it is given, because two things downstream
 * read that model directly and both are wrong if it has been filtered:
 * useDragReorder builds its drop table from `byColumn` (jobKey → the column a
 * card is in) and commitReorder builds the `POST /api/reorder` body from
 * `byColumn[column]` — the WHOLE column. `store.SetBoardColumn` REPLACES the
 * stored order with exactly the ids posted and `jobs.ApplyColumnOrder` floats
 * every member absent from that list to the TOP, so a reorder inside a
 * filtered column that posted only the visible cards would send the hidden
 * ones to the top of the column permanently, with nothing on screen saying so.
 *
 * Search is a render concern; the model stays whole. App.tsx keeps handing
 * DragProvider the unfiltered board, and the one thing the drag layer takes
 * from here is `visibleKeys` — a set of ids, which cannot be mistaken for a
 * column and so cannot be posted as one.
 *
 * Nothing is persisted — no localStorage key, no hash parameter. A reload
 * lands on an unfiltered board, which is the only resting state a card set
 * this volatile can safely have. The goal filter obeys the same rule and for
 * the same reason: it is App state, and a reload lands unfiltered.
 *
 * The GOAL FILTER composes with the query rather than replacing it: a job is
 * kept when it links the filtered goal AND matches every typed term, which is
 * the reading that makes the `N of M` readout a count of what is on screen.
 * It changes nothing above — the filtered lists are still the second view, and
 * `board.byColumn` is still handed back untouched.
 */
export function useJobSearch(
  board: BoardModel,
  goalFilter: number | null = null,
): JobSearchModel {
  const [query, setQuery] = useState("");
  const { byColumn, archived } = board;
  const terms = useMemo(() => parseTerms(query), [query]);
  // One spelling of "no filter" from here down: the prop may arrive as
  // undefined (a host that never passes one) or null (App's cleared state),
  // and two absent values would be two branches in every test below.
  const goal = goalFilter ?? null;

  return useMemo(() => {
    let total = archived.length;
    for (const col of COLUMNS) total += byColumn[col].length;

    // Idle returns the board's OWN arrays by identity rather than copies:
    // there is nothing to filter, and a fresh array every render would
    // re-register every column's sortable list on every keystroke elsewhere.
    // FULLY idle: a goal filter with no query is not idle, so it takes the
    // filtering path below like any other narrowing.
    if (terms.length === 0 && goal === null) {
      return {
        query,
        setQuery,
        terms,
        byColumn,
        archived,
        reasonByKey: NO_REASONS,
        visibleKeys: NO_KEYS,
        matched: total,
        total,
        active: false,
        goalFilter: null,
      };
    }

    const reasonByKey = new Map<string, JobMatch["reason"]>();
    const visibleKeys = new Set<string>();
    const keep = (job: WebJob): boolean => {
      // The goal predicate reads `goals` and ONLY `goals` — the array is the
      // job's whole link set, and a job carrying no array links nothing, so
      // there is no fallback to the legacy `goal` key to write here: it is
      // that array's head whenever it exists at all.
      if (goal !== null && !(job.goals ?? []).some((g) => g.id === goal)) return false;
      // A remote card's goal ids are its OWNER's goals: an id that happens to
      // equal one of this board's must not file it under this board's goal.
      if (goal !== null && job.owner) return false;
      // Keyed by jobKey(), never bare slug: an archived entry and a live job
      // that reused its freed slug are two rows here (see lib/jobKey.ts), and
      // a bare-slug key would hand one of them the other's match reason.
      const key = jobKey(job);
      if (terms.length > 0) {
        const m = matchJob(job, terms);
        if (!m) return false;
        // A reason explains a TEXT match, so a card kept by the goal filter
        // alone gets none — there are no matched words on it to mark.
        reasonByKey.set(key, m.reason);
      }
      visibleKeys.add(key);
      return true;
    };

    const cols = {} as Record<Column, WebJob[]>;
    let matched = 0;
    for (const col of COLUMNS) {
      cols[col] = byColumn[col].filter(keep);
      matched += cols[col].length;
    }
    const arch = archived.filter(keep);
    matched += arch.length;

    return {
      query,
      setQuery,
      terms,
      byColumn: cols,
      archived: arch,
      reasonByKey,
      visibleKeys,
      matched,
      total,
      active: true,
      goalFilter: goal,
    };
  }, [byColumn, archived, terms, query, goal]);
}
