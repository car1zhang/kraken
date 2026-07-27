import { useCallback, useEffect, useRef } from "react";

/**
 * `debounce()` from app.js:1164, as a hook.
 *
 * The two extras are the whole reason the vanilla board wrote its own instead
 * of inlining a setTimeout, and both are load-bearing here too:
 *
 *   flush()  — fire the pending call NOW and return its result, so a caller
 *              can await it. Every modal-close path uses this: a draft or an
 *              autosave still sitting in the 600ms window has to land before
 *              the modal that owns it unmounts.
 *   cancel() — drop it unfired, for when the server has already applied what
 *              a late fire would resend. The steer send path calls it TWICE:
 *              once before posting (a debounce firing mid-send would land
 *              after the server's own draft clear and resurrect the sent
 *              message) and once after, because a keystroke during the
 *              in-flight send scheduled a fresh one the first cancel never saw.
 *
 * The callback is held in a ref, so a caller passing an inline arrow (all of
 * them do) does not reset the timer on every render.
 */
export interface Debounced<A extends unknown[]> {
  call: (...args: A) => void;
  flush: () => void;
  cancel: () => void;
}

export function useDebouncedCallback<A extends unknown[]>(
  fn: (...args: A) => void,
  ms: number,
): Debounced<A> {
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const timer = useRef<number | null>(null);
  const pending = useRef<A | null>(null);

  const cancel = useCallback(() => {
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = null;
    pending.current = null;
  }, []);

  const flush = useCallback(() => {
    if (timer.current === null) return;
    window.clearTimeout(timer.current);
    timer.current = null;
    const a = pending.current;
    pending.current = null;
    if (a) fnRef.current(...a);
  }, []);

  const call = useCallback(
    (...args: A) => {
      pending.current = args;
      if (timer.current !== null) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => {
        timer.current = null;
        const a = pending.current;
        pending.current = null;
        if (a) fnRef.current(...a);
      }, ms);
    },
    [ms],
  );

  // An unmount with a pending call is exactly the modal-close case, and the
  // vanilla board handled it by calling flushSteerDraft/flushEditorAutosave
  // from every single close path (backdrop, Escape, send, kill, archive, and
  // checkStaleModals' force-close). Flushing here covers all of them at once,
  // including the ones that are not the user's doing.
  const flushRef = useRef(flush);
  flushRef.current = flush;
  useEffect(() => () => flushRef.current(), []);

  return { call, flush, cancel };
}

export interface DraftSync {
  /** Land a pending draft immediately (close paths; also runs on unmount). */
  flush: () => void;
  /** Drop a pending draft unfired (the send path). */
  cancel: () => void;
  /**
   * Tell the hook a value came FROM the server, so it is not echoed back.
   * Called when the initial `GET /api/draft` lands in the composer.
   */
  markSynced: (v: string) => void;
}

/**
 * The steer composer's server-synced draft (app.js:1975).
 *
 * One shared draft per job regardless of mode, so steer / unblock / reactivate
 * all read and write the same text. Fire-and-forget: a failed POST is not
 * retried, because the next keystroke's debounce already is the retry.
 *
 * Watching `value` replaces the vanilla `input` listener. `markSynced` exists
 * because that swap introduces one thing the listener could not have: the
 * seeded draft arriving from the server is a value CHANGE, and echoing it
 * straight back would be a pointless write on every open.
 */
export function useDebouncedDraft(
  slug: string | null,
  value: string,
  save: (slug: string, text: string) => void,
  ms = 600,
): DraftSync {
  const { call, flush, cancel } = useDebouncedCallback(save, ms);
  const lastRef = useRef<string | null>(null);
  const slugRef = useRef<string | null>(null);

  // Both refs are read and written inside this effect, never during render: the
  // seed-vs-edit decision and the "composer switched jobs" reset are the same
  // decision, and splitting the second one into the render body made it a
  // conditional side effect for no benefit.
  useEffect(() => {
    if (!slug) return;
    if (slugRef.current !== slug) {
      // a new job's first reported value is its SEED, not an edit — never echo
      // it back, and never post job A's text against job B's slug
      slugRef.current = slug;
      lastRef.current = value;
      return;
    }
    if (lastRef.current === value) return;
    lastRef.current = value;
    call(slug, value);
  }, [slug, value, call]);

  const markSynced = useCallback((v: string) => {
    lastRef.current = v;
  }, []);

  return { flush, cancel, markSynced };
}
