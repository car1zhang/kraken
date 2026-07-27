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
  /** Fetch now, out of band. Also what mutations call after a write. */
  refetch: () => Promise<void>;
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

  const run = useCallback(async () => {
    if (pauseWhenRef.current?.()) return;
    if (pauseWhenHidden && typeof document !== "undefined" && document.hidden) {
      return;
    }
    // One request at a time per hook. A slow tick must not stack up behind
    // itself and then deliver its results out of order.
    inFlightRef.current?.abort();
    const ac = new AbortController();
    inFlightRef.current = ac;
    try {
      const next = await fetcherRef.current(ac.signal);
      if (!aliveRef.current || ac.signal.aborted) return;
      setData(next);
      setError(undefined);
      setFailStreak(0);
    } catch (e) {
      // An abort is this hook superseding itself, not a failure — counting it
      // would poison failStreak every time a dependency changed.
      if (ac.signal.aborted) return;
      if (!aliveRef.current) return;
      setError(e instanceof Error ? e : new Error(String(e)));
      setFailStreak((n) => n + 1);
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
