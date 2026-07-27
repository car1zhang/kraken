// Reverse-infinite-scroll paging for the unified event log, ported from
// refreshLog() (web/static/app.js:3885) and loadOlderLog() (:3915).
//
// The pagination unit is a log FILE, not an offset: /api/logs hands back one
// page plus a `prev` cursor naming the page before it, and the client walks
// that cursor backwards. `chain` holds file names oldest→newest and `data`
// maps a name to its entries + its own prev, so the newest page can be
// re-fetched in place on every tick while older pages stay frozen.
//
// In store mode "file" is a virtual token ("events", then "events:<id>"),
// which is what makes the change detection below subtle — see logPageChanged.

import type { RefObject } from "react";
import { useCallback, useLayoutEffect, useRef, useState } from "react";

import { fetchLog } from "../api/client.ts";
import type { LogEntry, LogPage } from "../api/types.ts";
import { usePoll } from "./usePoll.ts";

/** The log rides the same 3s cadence the board poll used (app.js:3942). */
export const LOG_POLL_MS = 3000;

/** Within this many px of the bottom counts as "watching the tail". */
export const TAIL_FOLLOW_PX = 40;

/** Scrolling above this many px from the top pulls the previous page. */
export const OLDER_TRIGGER_PX = 40;

export interface LogFileData {
  entries: LogEntry[];
  /** The page before this one; absent at the start of history. */
  prev?: string;
}

interface LogState {
  /** File names, oldest → newest. */
  chain: string[];
  data: Record<string, LogFileData>;
}

type PendingScroll =
  | { mode: "bottom" }
  | { mode: "preserve"; top: number; height: number };

export interface ScrollMetrics {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
}

/**
 * Tail-follow test. Only a reader already looking at the newest lines gets
 * yanked to the bottom by an incoming entry.
 */
export function isAtBottom(el: ScrollMetrics): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < TAIL_FOLLOW_PX;
}

/**
 * The scroll offset that keeps the reader looking at the SAME line after
 * content is prepended above it: everything they were looking at moved down
 * by exactly the height the new content added (app.js:3879).
 */
export function preservedScrollTop(
  prevTop: number,
  prevHeight: number,
  nextHeight: number,
): number {
  return prevTop + (nextHeight - prevHeight);
}

/**
 * Did the newest page actually change? Count ALONE is not enough, and that is
 * a fixed bug rather than belt-and-braces.
 *
 * Store mode's virtual "events" file name never rotates (unlike a file-mode
 * log file, which gets a fresh name — and therefore an immediate "unseen
 * file" hit — on every rotation). Once the store holds more than one page of
 * events, `entries.length` pins at the page size FOREVER while the window
 * keeps sliding: oldest entries drop off the top as newest ones enter the
 * tail. A length-only comparison reads that as "unchanged" and the view
 * freezes. Comparing the newest entry too catches the slide (app.js:3902).
 */
export function logPageChanged(
  prev: LogEntry[] | undefined,
  next: LogEntry[],
): boolean {
  if (!prev) return true;
  if (prev.length !== next.length) return true;
  if (next.length === 0) return false;
  return (
    JSON.stringify(prev[prev.length - 1]) !== JSON.stringify(next[next.length - 1])
  );
}

export interface LogPagingModel {
  /** File names oldest → newest; render in this order. */
  chain: string[];
  data: Record<string, LogFileData>;
  /** The head of the chain still has a prev, so more history exists. */
  hasMore: boolean;
  /** Attach to the scrolling container — the hook writes its scrollTop. */
  scrollRef: RefObject<HTMLDivElement | null>;
  /** Attach as onScroll; pulls the older page near the top. */
  handleScroll: () => void;
  refresh: () => Promise<void>;
  loadOlder: () => Promise<void>;
}

/**
 * @param active whether the log view is showing. Polling runs only while it
 *   is — in the vanilla board the log refresh was a line inside the board
 *   tick guarded by `view === "log"`.
 */
export function useLogPaging(active: boolean): LogPagingModel {
  const [state, setState] = useState<LogState>({ chain: [], data: {} });

  // stateRef is authoritative, not a mirror of the last render: refresh and
  // loadOlder both read-modify-write asynchronously, and a second call that
  // resumed before React committed would otherwise clobber the first.
  const stateRef = useRef<LogState>(state);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const loadingRef = useRef(false);
  const pendingRef = useRef<PendingScroll | null>(null);

  const commit = useCallback((next: LogState) => {
    stateRef.current = next;
    setState(next);
  }, []);

  const refresh = useCallback(
    async (signal?: AbortSignal) => {
      let page: LogPage | undefined;
      try {
        page = await fetchLog(undefined, signal);
      } catch {
        return; // the status line already reports an unreachable server
      }
      if (!page?.file) return;
      const cur = stateRef.current;
      const firstLoad = cur.chain.length === 0;
      const el = scrollRef.current;
      // Read the metrics BEFORE the state change: after it, scrollHeight is
      // the post-render height and the comparison is meaningless.
      const bottom = el ? isAtBottom(el) : true;
      const prevTop = el?.scrollTop ?? 0;
      const prevHeight = el?.scrollHeight ?? 0;
      const entries = page.entries ?? [];
      const changed = logPageChanged(cur.data[page.file]?.entries, entries);
      if (changed || firstLoad) {
        pendingRef.current =
          firstLoad || bottom
            ? { mode: "bottom" }
            : { mode: "preserve", top: prevTop, height: prevHeight };
      }
      commit({
        chain: cur.chain.includes(page.file) ? cur.chain : [...cur.chain, page.file],
        data: { ...cur.data, [page.file]: { entries, prev: page.prev } },
      });
    },
    [commit],
  );

  const loadOlder = useCallback(async () => {
    const cur = stateRef.current;
    const first = cur.chain[0];
    const prev = first ? cur.data[first]?.prev : undefined;
    // Already loaded, already loading, or at the start of history.
    if (!prev || loadingRef.current || cur.data[prev]) return;
    loadingRef.current = true;
    try {
      const page = await fetchLog(prev);
      if (page?.file) {
        const el = scrollRef.current;
        // Never tail-follow on a prepend: the reader is at the top by
        // definition, and that is the position being preserved.
        pendingRef.current = {
          mode: "preserve",
          top: el?.scrollTop ?? 0,
          height: el?.scrollHeight ?? 0,
        };
        const now = stateRef.current;
        commit({
          chain: [page.file, ...now.chain],
          data: {
            ...now.data,
            [page.file]: { entries: page.entries ?? [], prev: page.prev },
          },
        });
      }
    } catch {
      // same as refresh: an unreachable server is already reported
    }
    loadingRef.current = false;
  }, [commit]);

  // Layout, not effect: the scroll correction has to land in the same frame
  // the new rows do, or the reader sees the view jump and snap back.
  useLayoutEffect(() => {
    const p = pendingRef.current;
    if (!p) return;
    pendingRef.current = null;
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop =
      p.mode === "bottom"
        ? el.scrollHeight
        : preservedScrollTop(p.top, p.height, el.scrollHeight);
  }, [state]);

  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (el && el.scrollTop < OLDER_TRIGGER_PX) void loadOlder();
  }, [loadOlder]);

  usePoll<void>((signal) => refresh(signal), LOG_POLL_MS, { enabled: active });

  const head = state.chain[0];
  return {
    chain: state.chain,
    data: state.data,
    hasMore: Boolean(head && state.data[head]?.prev),
    scrollRef,
    handleScroll,
    refresh,
    loadOlder,
  };
}
