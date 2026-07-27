import { useCallback, useMemo, useRef, useState } from "react";

import { fetchBoard } from "../api/client.ts";
import type { WebBoard, WebJob } from "../api/types.ts";
import { bucketJobs } from "../lib/format.ts";
import { jobKey } from "../lib/jobKey.ts";
import { usePoll } from "./usePoll.ts";

// Re-exported from lib/columns.ts (a leaf module) rather than declared here:
// this file imports bucketJobs from lib/format.ts, which needs COLUMNS, and
// declaring it here made those two modules a cycle. Consumers still import
// COLUMNS/Column from this hook, so nothing downstream changed.
import type { Column } from "../lib/columns.ts";

export { COLUMNS } from "../lib/columns.ts";
export type { Column } from "../lib/columns.ts";

export const BOARD_POLL_MS = 3000;

export interface BoardModel {
  board: WebBoard | undefined;
  error: Error | undefined;
  /** Keyed by jobKey(), never bare slug — see lib/jobKey.ts. */
  byKey: Map<string, WebJob>;
  /** Active (non-archived) jobs bucketed by column, in server order. */
  byColumn: Record<Column, WebJob[]>;
  archived: WebJob[];
  refetch: () => Promise<void>;
  /** Suppresses polling for the duration of a drag. */
  setDragging: (dragging: boolean) => void;
  /**
   * Run a mutation, then refetch — on BOTH success and failure, matching the
   * vanilla post(). Returns the error rather than throwing so callers can put
   * its text in the tick-stat corner without a try/catch at every call site.
   */
  mutate: (fn: () => Promise<unknown>) => Promise<Error | null>;
  /** Last mutation failure, for the status line. Cleared by the next success. */
  lastError: string | null;
}

export function useBoard(): BoardModel {
  const draggingRef = useRef(false);
  const [lastError, setLastError] = useState<string | null>(null);

  // A ref rather than state: the poll reads it at tick time, and re-rendering
  // the whole board on dragstart is exactly what we are trying to avoid.
  const setDragging = useCallback((d: boolean) => {
    draggingRef.current = d;
  }, []);

  const { data, error, refetch } = usePoll<WebBoard>(
    (signal) => fetchBoard(signal),
    BOARD_POLL_MS,
    {
      // The board must not rebuild under the pointer mid-drag. The vanilla
      // board did this with `if (dragging) return` at the top of refresh();
      // dragend then forced a tick, which is what the drag hook does here.
      pauseWhen: () => draggingRef.current,
      pauseWhenHidden: true,
    },
  );

  const mutate = useCallback(
    async (fn: () => Promise<unknown>): Promise<Error | null> => {
      try {
        await fn();
        setLastError(null);
        await refetch();
        return null;
      } catch (e) {
        const err = e instanceof Error ? e : new Error(String(e));
        setLastError(err.message);
        // Refetch on failure too: the server is the truth, and a rejected
        // action usually means the board already moved on without us.
        await refetch();
        return err;
      }
    },
    [refetch],
  );

  const { byKey, byColumn, archived } = useMemo(() => {
    const key = new Map<string, WebJob>();
    for (const j of data?.jobs ?? []) key.set(jobKey(j), j);
    // ONE bucketing for the whole app — see bucketJobs' comment for the rule it
    // carries. This used to bucket on `j.state` alone, which was a real bug
    // rather than a simplification: the render layer relocates an unhealthy
    // cell into BLOCKED (app.js:614) while this map left it in WORKING, and
    // useDragReorder keys the drop table off THIS map. So a sick job rendered
    // in blocked reported `from: "working"`, and the drop table resolved the
    // wrong move — dragging it to DONE lit up nothing at all, while nudging it
    // a row down inside its own column resolved working→blocked and posted
    // /api/stop. A gesture that looked like a reorder interrupted the agent.
    //
    // Vanilla could not have this class of bug: cardHTML stamped
    // `data-col` from the RENDERED bucket (app.js:519) and dragstart read that
    // attribute back (app.js:964), so `from` was by construction the column
    // the user could see the card in. Keeping one function here restores that
    // property structurally instead of by agreement.
    const { byColumn: cols, archived: arch } = bucketJobs(data?.jobs ?? []);
    return { byKey: key, byColumn: cols, archived: arch };
  }, [data]);

  return {
    board: data,
    error,
    byKey,
    byColumn,
    archived,
    refetch,
    setDragging,
    mutate,
    lastError,
  };
}
