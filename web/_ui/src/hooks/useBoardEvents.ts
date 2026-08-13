import { useEffect, useRef } from "react";

import type { WebBoard } from "../api/types.ts";
import {
  coalesceEvents,
  consumeMuteFlag,
  diffBoard,
  EVENT_STAGGER_MS,
} from "../lib/boardEvents.ts";
import { playEventSfx } from "../lib/sfx.ts";

/**
 * The board's voice: every poll snapshot diffed against the one before it, the
 * result coalesced and played as a short burst of chip motifs.
 *
 * Three rules carry this hook, and each of them exists because the obvious
 * version is wrong:
 *
 *  1. **The first board is a BASELINE, never a performance.** `usePoll` starts
 *     at `data === undefined` and hands over a WHOLE board on its first
 *     success (its one `setData` call) — every job on it at once, none of it
 *     news to someone who just opened the tab. So the first snapshot is
 *     recorded and nothing more.
 *     `diffBoard` states the same rule from its own side (`prev === undefined`
 *     → no events); neither half leans on the other, because the two are
 *     answering different questions — "is there anything to compare" there,
 *     "is this a board carl has already seen" here.
 *
 *  2. **The prev ref ALWAYS advances, muted or not.** A mute means "carl did
 *     this and watched it happen", not "skip this snapshot": holding the old
 *     board back would leave the very changes the mutation caused sitting in
 *     the diff, and the next 3s tick would announce them anyway — a mute that
 *     delays by one poll instead of suppressing.
 *
 *  3. **Pending stagger timers die with the hook.** The burst is spread over
 *     `i × EVENT_STAGGER_MS`, and a sound landing after teardown is a board
 *     narrating a board that no longer exists.
 *
 * Snapshot IDENTITY is what drives all of it: `fetchBoard` decodes a fresh
 * object per tick (client.ts:81) and `usePoll` sets it as-is, so a new
 * reference is a new snapshot — and a re-render caused by anything else (an
 * overlay opening, a search keystroke) re-runs nothing here. The tick's VOLUME
 * is not this hook's problem either: `coalesceEvents` ranks and caps it, which
 * is why nothing below counts, rate-limits, or watermarks.
 */
export function useBoardEvents(board: WebBoard | undefined): void {
  const prevRef = useRef<WebBoard | undefined>(undefined);
  // Live stagger timers, held in a Set so a fired one can retire itself — the
  // alternative (an ever-growing array cleared only at unmount) accumulates two
  // dead ids every 3s for as long as the tab is open.
  const timersRef = useRef(new Set<number>());

  useEffect(() => {
    // No snapshot yet, or the poll handed back `undefined` again: there is
    // nothing to diff and nothing to record. Note this also covers the mount
    // pass under StrictMode (main.tsx) — `data` is undefined until the first
    // fetch resolves, which is well after the double-invoke, so the baseline
    // rule above is never spent on a replayed effect.
    if (!board) return;

    const prev = prevRef.current;
    prevRef.current = board; // rule 2 — before any early return below
    if (!prev) return; // rule 1

    const events = diffBoard(prev, board);
    // Consumed on EVERY non-baseline tick, including one that diffed to
    // nothing. The flag is one-shot and a mutation is free to change nothing
    // the differ can see (a rejected post, a write that moved only fields no
    // event type describes); leaving it set there would arm it against the
    // next unrelated poll and silence a change carl did not make.
    if (consumeMuteFlag()) return;

    // `.entries()` rather than an index loop only because
    // `noUncheckedIndexedAccess` would otherwise type every entry as possibly
    // undefined — the ordering is the coalescer's, and it is played as given.
    for (const [i, name] of coalesceEvents(events).entries()) {
      if (i === 0) {
        // Entry 0 plays INLINE, not through a 0ms timer: it belongs to the
        // render carl is looking at, and a task-queue hop would make the one
        // sound the tick is really about the one that arrives late.
        playEventSfx(name);
        continue;
      }
      const id = window.setTimeout(() => {
        timersRef.current.delete(id);
        playEventSfx(name);
      }, i * EVENT_STAGGER_MS);
      timersRef.current.add(id);
    }
  }, [board]);

  // Unmount only — deliberately its own effect rather than a cleanup on the one
  // above, whose cleanup would run on every new snapshot and cut the previous
  // tick's burst short. Overlapping bursts are not a real case anyway (the cap
  // spans at most one stagger step, against a 3s poll), so the last word on a
  // tick belongs to that tick.
  useEffect(() => {
    const timers = timersRef.current;
    return () => {
      for (const id of timers) window.clearTimeout(id);
      timers.clear();
    };
  }, []);
}
