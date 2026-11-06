import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { fetchArchived, fetchBoard } from "../api/client.ts";
import type { WebBoard, WebJob } from "../api/types.ts";
import { disarmMute, muteNextDiff } from "../lib/boardEvents.ts";
import { filterFleet } from "../lib/fleet.ts";
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
  /**
   * The archived cards IN HAND. The board poll carries none (`?archived=0`):
   * they are fetched from `/api/archived` once something asks for them through
   * `needArchived`, so until then this is empty however many the archive
   * holds. `archivedCount` is the archive's size either way.
   */
  archived: WebJob[];
  /**
   * How many jobs the archive holds, from the board payload: the number to
   * label the archive with before its cards are loaded. Optional so a model
   * built by hand (tests) need not carry it; absent reads as `archived.length`.
   */
  archivedCount?: number;
  /**
   * Whether `archived` is the whole archive. False until the first fetch
   * lands. Absent (a hand-built model) reads as true: such a model's
   * `archived` is all there is.
   */
  archivedLoaded?: boolean;
  /**
   * Ask for the archived cards: the first call starts the fetch, and they are
   * kept current from then on (re-fetched when the board's `archived_count`
   * moves and after every `mutate`). Called by whatever is about to show or
   * search the archive. Safe to call on every render of such a view.
   */
  needArchived?: () => void;
  refetch: () => Promise<void>;
  /** Suppresses polling for the duration of a drag. */
  setDragging: (dragging: boolean) => void;
  /**
   * Run a mutation, then refetch — on BOTH success and failure, matching the
   * vanilla post(). Returns the error rather than throwing so callers can put
   * its text in the tick-stat corner without a try/catch at every call site.
   *
   * That refetch is also SILENT: a self-inflicted board change is not news, so
   * the event sounds skip it (the muteNextDiff/disarmMute pair below). This is
   * the ONLY mute site, which makes it an invariant a future writer has to
   * keep rather than a fact about today's tree:
   *
   *   **every POST that can move a PER-JOB field lib/boardEvents.ts diffs goes
   *   through `mutate`** — state, question, health, prs, threads, artifacts,
   *   color.
   *
   * Per-job is the scope on purpose: the differ also reads one BOARD-level
   * axis, `update.phase`, and that one is exempt — last bullet below.
   *
   * The board does post outside this function, and each of those is silent for
   * a reason of its OWN, none of which generalises to a new call site:
   *
   *   - `renameJob` (RenameControl.tsx:259) and `createJob`
   *     (EditorOverlay.tsx:531) are silent by the jobKey ONE-SIDE rule — the
   *     differ matches by key and skips any job present on only one side
   *     (`diffBoard`'s `was === undefined` continue), so a job arriving under
   *     a new name, or arriving at all, has nothing to be compared against.
   *   - `saveJobForm` (EditorOverlay.tsx:326) posts repo/base/solo/model/spec
   *     (JobForm, api/types.ts:455) — not one diffed field among them.
   *   - `reorderColumn`'s optimistic path (useDragReorder.ts:507) moves only
   *     job ORDER, which no member of `EventType` describes.
   *   - The bare `refetch()` calls (useDragReorder's correcting ticks, App's
   *     onRenamed) post nothing at all; they are ordinary polls and are
   *     SUPPOSED to speak.
   *   - `requestUpdate` (UpdateControl.tsx:291, and see its own comment on why
   *     it skips this path) is the one write that DOES move a diffed field —
   *     `update.phase`, which the differ turns into update-done/update-failed
   *     — and it is exempt anyway. A one-shot mute reaches exactly ONE tick,
   *     and the click's own tick is nowhere near the firing one: the handler
   *     writes `preflight` before spawning (UpdateControl.tsx:293) and the
   *     run walks fetching → merging → building → installing → restarting
   *     (UpdatePhase, api/types.ts:313) before it can reach done or failed,
   *     minutes later, on an ordinary poll. The chime is also the point: an
   *     update finishing is news even though carl started it, which is the
   *     opposite of the argument the rest of this docblock makes.
   *
   * Add a POST anywhere and the question is not "is it like one of those" but
   * "does the differ read what it writes, and can this action's own tick be
   * the one that fires" — if both, it belongs in `mutate`.
   */
  mutate: (fn: () => Promise<unknown>) => Promise<Error | null>;
  /** Last mutation failure, for the status line. Cleared by the next success. */
  lastError: string | null;
}

/**
 * The fleet filter's one per-browser preference: the machine keys to show
 * (lib/fleet.ts filterFleet; empty is no filter). Applied to the COLUMNS and
 * the drawer only: `board` stays the raw payload (the event differ and the
 * chrome's nodes stat read every card), and `byKey` keeps every card so an
 * overlay already open on a card that just got hidden still resolves it.
 */
export interface FleetFilter {
  nodes: readonly string[];
}

const NO_FLEET_FILTER: FleetFilter = { nodes: [] };

export function useBoard(fleet: FleetFilter = NO_FLEET_FILTER): BoardModel {
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

  // ---- the archive, off the poll. The board says how many jobs it holds;
  // their cards come from /api/archived, and only once something needs them
  // (the drawer opened, a search or goal filter running). From then on they
  // are kept current by re-fetching when the board's count moves and after
  // each of this tab's own writes, which is everything that can change one
  // short of a note written on an archived artifact from another device.
  const [archiveWanted, setArchiveWanted] = useState(false);
  /** Bumped to force a re-fetch: after a mutate, and to retry a failed fetch. */
  const [archiveStamp, setArchiveStamp] = useState(0);
  const [archive, setArchive] = useState<{ jobs: WebJob[]; count: number; stamp: number } | null>(null);
  const needArchived = useCallback(() => setArchiveWanted(true), []);
  const archivedCount = data?.archived_count;
  useEffect(() => {
    // a server older than the key still carries the archived cards on the board
    if (!archiveWanted || archivedCount === undefined) return;
    if (archive && archive.count === archivedCount && archive.stamp === archiveStamp) return;
    const ac = new AbortController();
    let retry: number | undefined;
    fetchArchived(ac.signal)
      .then((r) => {
        if (!ac.signal.aborted) setArchive({ jobs: r.jobs ?? [], count: archivedCount, stamp: archiveStamp });
      })
      .catch(() => {
        // a failed read is retried on the board's own cadence, not dropped
        if (!ac.signal.aborted) retry = window.setTimeout(() => setArchiveStamp((s) => s + 1), BOARD_POLL_MS);
      });
    return () => {
      ac.abort();
      if (retry !== undefined) window.clearTimeout(retry);
    };
  }, [archiveWanted, archivedCount, archiveStamp, archive]);

  const mutate = useCallback(
    async (fn: () => Promise<unknown>): Promise<Error | null> => {
      // ARMED BEFORE THE WRITE, not before the refetch. This is a change carl
      // is making and watching, so the snapshot that reports it back must not
      // be announced — and the snapshot that reports it back is not
      // necessarily OUR refetch. The 3s poll can tick while `fn()` is in
      // flight and straddle the write, landing the change first;
      // arming after the await hands that tick the announcement. Arming here
      // mutes whichever board gets there first.
      const gen = muteNextDiff();
      try {
        await fn();
        setLastError(null);
        // the write may have changed an archived card (an unarchive, a
        // delete, a note): if the archive is loaded, read it again
        setArchiveStamp((s) => s + 1);
        const delivered = await refetch();
        // …and TAKEN BACK DOWN if no board is coming. `refetch` declines in
        // three ways — a hidden tab, a drag holding polling paused, or a
        // fetch that failed outright — and an arm left standing after one of
        // those is not a mute of this action at all: it is a mute of some
        // arbitrarily later, unrelated tick, worst case eating the burst carl
        // comes back to the tab for.
        //
        // Note what is NOT in that list. A refetch SUPERSEDED by the 3s
        // interval answers true, because the interval's own request — issued
        // after this write — is the board this mute is for; disarming there
        // would hand carl's own change to the very tick about to report it.
        // (An unmounted hook answers true as well — the arm it leaves standing
        // is never met: App is the root and never unmounts, and tests reset.)
        // `gen` is what keeps this honest across overlapping mutations: it
        // disarms OUR arm and never a newer one.
        if (!delivered) disarmMute(gen);
        return null;
      } catch (e) {
        const err = e instanceof Error ? e : new Error(String(e));
        setLastError(err.message);
        setArchiveStamp((s) => s + 1);
        // Refetch on failure too: the server is the truth, and a rejected
        // action usually means the board already moved on without us. Same
        // arm/disarm rule — a refusal still refetches, and that board is still
        // this action's answer.
        const delivered = await refetch();
        if (!delivered) disarmMute(gen);
        return err;
      }
    },
    [refetch],
  );

  // BoardModel keeps the `Promise<void>` shape deliberately. usePoll's refetch
  // now reports whether it delivered, but that boolean is a PRIVATE handshake
  // between the poll and `mutate` above — the arm/disarm of the one-shot mute
  // — and no consumer of this model has a use for it: every one of them awaits
  // refetch purely to sequence a repaint. Narrowing it back here also keeps a
  // poll-internal detail from leaking into five suites that build a
  // BoardModel by hand with `refetch: async () => {}`. useCallback, not an
  // inline arrow: useDragReorder carries this in its callback deps.
  const refetchBoard = useCallback(async () => {
    await refetch();
  }, [refetch]);

  // Every card in hand: the board's own, then the archived cards once they
  // are loaded — the order the board itself appends them in. A server older
  // than `archived_count` still carries them in `jobs`, and then nothing is
  // added. Everything below reads this one list, so an archived card is
  // looked up, bucketed and searched exactly as when it rode the poll.
  const archivedJobs = archivedCount === undefined ? undefined : archive?.jobs;
  const allJobs = useMemo(() => {
    const own = data?.jobs ?? [];
    return archivedJobs && archivedJobs.length ? [...own, ...archivedJobs] : own;
  }, [data, archivedJobs]);

  const { byKey, byColumn, archived } = useMemo(() => {
    const key = new Map<string, WebJob>();
    for (const j of allJobs) key.set(jobKey(j), j);
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
    const shown = filterFleet(allJobs, fleet.nodes);
    const { byColumn: cols, archived: arch } = bucketJobs(shown);
    return { byKey: key, byColumn: cols, archived: arch };
  }, [allJobs, fleet.nodes]);

  return {
    board: data,
    error,
    byKey,
    byColumn,
    archived,
    // an older server's board carries the archive itself: its size is the list's
    archivedCount: archivedCount ?? archived.length,
    archivedLoaded: archivedCount === undefined || archive !== null,
    needArchived,
    refetch: refetchBoard,
    setDragging,
    mutate,
    lastError,
  };
}
