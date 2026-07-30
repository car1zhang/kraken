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
  /** jobKey() → why that job survived. Empty while idle, since nothing was filtered. */
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
  /** A non-whitespace query is present. Everything cosmetic keys off this. */
  active: boolean;
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
 * this volatile can safely have.
 */
export function useJobSearch(board: BoardModel): JobSearchModel {
  const [query, setQuery] = useState("");
  const { byColumn, archived } = board;
  const terms = useMemo(() => parseTerms(query), [query]);

  return useMemo(() => {
    let total = archived.length;
    for (const col of COLUMNS) total += byColumn[col].length;

    // Idle returns the board's OWN arrays by identity rather than copies:
    // there is nothing to filter, and a fresh array every render would
    // re-register every column's sortable list on every keystroke elsewhere.
    if (terms.length === 0) {
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
      };
    }

    const reasonByKey = new Map<string, JobMatch["reason"]>();
    const visibleKeys = new Set<string>();
    const keep = (job: WebJob): boolean => {
      const m = matchJob(job, terms);
      if (!m) return false;
      // Keyed by jobKey(), never bare slug: an archived entry and a live job
      // that reused its freed slug are two rows here (see lib/jobKey.ts), and
      // a bare-slug key would hand one of them the other's match reason.
      const key = jobKey(job);
      reasonByKey.set(key, m.reason);
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
    };
  }, [byColumn, archived, terms, query]);
}
