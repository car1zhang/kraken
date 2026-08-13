import { useCallback, useEffect, useRef, useState } from "react";

/**
 * The board's one refresh primitive. It replaces six ad-hoc setIntervals in
 * the vanilla app (board 3s, usage 60s, thread 3s, pane mirror 3s, update 1s,
 * ship 1s) and, more importantly, three hand-rolled "generation counter"
 * variables — steerGen/editorGen/threadGen — whose entire job was to let an
 * async continuation notice it had been superseded before painting. An
 * AbortController plus an effect cleanup is that, natively.
 *
 * Deliberately no exponential backoff, matching the vanilla board: this talks
 * to localhost (or one tailnet hop) and a fixed interval is the honest model.
 * What the caller gets instead is `failStreak`, because the update and ship
 * controls need to distinguish "one blip" from "serve is genuinely gone" —
 * see their gap clocks.
 */
export interface PollOptions {
  /** false stops the loop entirely (e.g. an overlay that is closed). */
  enabled?: boolean;
  /**
   * Skip ticks while the tab is hidden, and force one immediately on
   * becoming visible. On by default because Chrome freezes hidden tabs: a
   * wall-clock gap measured across a freeze is not a real outage, which is
   * exactly the false "serve did not come back" the vanilla board had to
   * defend against by resetting its gap clock on visibilitychange.
   */
  pauseWhenHidden?: boolean;
  /** Extra suppression, re-read at each tick. Used for `dragging`. */
  pauseWhen?: () => boolean;
}

export interface PollResult<T> {
  data: T | undefined;
  error: Error | undefined;
  /** Consecutive failures; reset to 0 by any success. */
  failStreak: number;
  /**
   * Fetch now, out of band. Also what mutations call after a write.
   *
   * The question it answers is **"will a payload land because of what I just
   * did"** — NOT "did my own fetch finish". The distinction is the whole
   * point, and getting it wrong was a real defect:
   *
   *   - Delivered (`setData` called with a fresh payload) → `true`.
   *   - **Superseded — this run was aborted by a later one → also `true`.**
   *     An abort here means a newer run is already in flight, and ITS payload
   *     is the one the caller is waiting on (unless that run in turn fails —
   *     a residual the mute accepts): this hook aborts the previous
   *     request at the top of every run, so the 3s interval routinely
   *     supersedes an out-of-band refetch. Answering `false` there says "no
   *     board is coming" while a board is, in fact, coming. That lie let a
   *     mutate's superseded refetch disarm a mute the interval's own board
   *     was about to need, and carl's own write got announced back at the
   *     board. The unmount teardown aborts in flight too and so shares this
   *     branch; mutate's continuation does read the answer there and leaves
   *     its arm standing, which nothing in practice ever meets — the board's
   *     tree is the root (main.tsx renders App into #root and never unmounts
   *     it), and the suites reset the flag per test.
   *   - Declined — paused (`pauseWhen`), skipped for a hidden tab, or failed
   *     outright → `false`. Nothing was fetched, or the fetch is over and
   *     lost; either way no payload is on its way.
   *
   * The caller that reads this is useBoard.mutate, which arms a one-shot mute
   * before the write and must take it back down if no board is coming —
   * otherwise the mute outlives its tick and silences an arbitrarily later,
   * unrelated poll.
   *
   * Every other CALL site ignores the value, so widening in place cost them
   * nothing. Two declared TYPES did pin `Promise<void>` and are re-narrowed by
   * a one-line adapter at each — `BoardModel.refetch` (useBoard.ts) and the
   * usage panel's prop (App.tsx's `refetchUsage`) — deliberately, so the
   * board's mute mechanism stops at the two places that implement it.
   */
  refetch: () => Promise<boolean>;
}

export function usePoll<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  intervalMs: number,
  opts: PollOptions = {},
): PollResult<T> {
  const { enabled = true, pauseWhenHidden = true, pauseWhen } = opts;

  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<Error | undefined>(undefined);
  const [failStreak, setFailStreak] = useState(0);

  // Held in refs so changing them never restarts the interval — a caller that
  // passes an inline arrow (all of them do) would otherwise re-subscribe on
  // every render and effectively poll on every paint.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const pauseWhenRef = useRef(pauseWhen);
  pauseWhenRef.current = pauseWhen;

  const aliveRef = useRef(true);
  const inFlightRef = useRef<AbortController | null>(null);

  // Returns "will a payload land because of this call" — see PollResult.refetch
  // for the three answers and why an ABORT is one of the true ones. The three
  // `return false`s below are the ways this DECLINES: paused, hidden, failed.
  // Nothing here is an error path — a suppressed tick is the design.
  const run = useCallback(async (): Promise<boolean> => {
    if (pauseWhenRef.current?.()) return false;
    if (pauseWhenHidden && typeof document !== "undefined" && document.hidden) {
      return false;
    }
    // One request at a time per hook. A slow tick must not stack up behind
    // itself and then deliver its results out of order. This abort is also
    // what makes the superseded case common enough to matter: a refetch and
    // the 3s interval land on top of each other routinely.
    inFlightRef.current?.abort();
    const ac = new AbortController();
    inFlightRef.current = ac;
    try {
      const next = await fetcherRef.current(ac.signal);
      // Two cases share this branch, and neither may paint. Superseded: a
      // newer run is carrying the payload, so the honest answer is true — the
      // question is about the payload, not about which run fetched it.
      // Unmounted: the cleanup effect aborts in flight, so it lands here too,
      // and the answer is unobservable because no consumer is left to read it.
      if (!aliveRef.current || ac.signal.aborted) return true;
      setData(next);
      setError(undefined);
      setFailStreak(0);
      return true;
    } catch (e) {
      // An abort is this hook superseding itself, not a failure — counting it
      // would poison failStreak every time a dependency changed. Same verdict
      // as the try side above, for the same reason.
      if (ac.signal.aborted) return true;
      if (!aliveRef.current) return true;
      setError(e instanceof Error ? e : new Error(String(e)));
      setFailStreak((n) => n + 1);
      return false;
    }
  }, [pauseWhenHidden]);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      inFlightRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (!enabled) return;
    void run();
    const id = window.setInterval(() => void run(), intervalMs);
    return () => window.clearInterval(id);
  }, [enabled, intervalMs, run]);

  useEffect(() => {
    if (!enabled || !pauseWhenHidden) return;
    const onVis = () => {
      if (!document.hidden) void run();
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [enabled, pauseWhenHidden, run]);

  return { data, error, failStreak, refetch: run };
}
