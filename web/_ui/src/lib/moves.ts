// The board's drag transition table, ported from web/static/app.js:18 (the
// `MOVES` object) and app.js:1049-1072 (the drop handler's if/else ladder).
//
// Those were two separate things in the vanilla board — an adjacency map used
// only to paint `.droptarget` highlights, and a ladder of string comparisons
// that decided what a drop actually did. Keeping them apart is how a legal
// highlight and a no-op drop can drift: the adjacency said done→working was
// legal, and only the ladder knew it opened a modal rather than posting.
// Here there is ONE table. `MOVES` is derived from it, so a pair that lights
// up is by construction a pair that does something.
//
// This is the highest-consequence surface in the UI: every entry below starts
// a cell, stops an agent, or archives real work. A pair that is not in the
// table is a NO-OP — never a "close enough" fallback.

import {
  archiveJob,
  dequeueJob,
  doneJob,
  queueJob,
  reactivateJob,
  startJob,
  stopJob,
} from "../api/client.ts";
import type { Column } from "../hooks/useBoard.ts";

/**
 * The archive drawer is a drop target but not a board column: it has no
 * `COLUMNS` entry, nothing is ever dragged OUT of it (unarchive is a button),
 * and it is the only target whose id is not a job state. The vanilla board
 * gave the drawer `data-col="archived"` so the drag handlers could treat it
 * uniformly (app.js:883); this type is that same trick, typed.
 */
export const ARCHIVED = "archived" as const;
export type DropColumn = Column | typeof ARCHIVED;

/** The three steer-modal modes. Only two are reachable from a drag. */
export type SteerMode = "steer" | "unblock" | "reactivate";

/** A drop that writes job state through one endpoint. */
export interface PostMove {
  kind: "post";
  /**
   * Kept beside `run` deliberately: `run` is a closure and a test asserting
   * on it can only observe it by calling it (a real fetch). The endpoint
   * string is what the MOVES test pins, and it is the same string the
   * closure posts to.
   */
  endpoint: string;
  run: (slug: string) => Promise<unknown>;
}

/**
 * A drop that opens the steer modal instead of posting. There is NO network
 * write on this path — the drag only chooses the modal's mode, and the user's
 * text is what eventually moves the job (or does not).
 */
export interface SteerMove {
  kind: "steer";
  mode: SteerMode;
  /** reactivate refuses an empty message; unblock accepts one. */
  requireText: boolean;
}

/** A drop back into the column it came from: `POST /api/reorder`. */
export interface ReorderMove {
  kind: "reorder";
}

export type Move = PostMove | SteerMove | ReorderMove;

const post = (endpoint: string, run: (slug: string) => Promise<unknown>): PostMove => ({
  kind: "post",
  endpoint,
  run,
});

/**
 * The ten legal drops, from → to. Every pair the vanilla board handled, and
 * nothing else. Two of them (blocked→working, done→working) deliberately hold
 * a `steer` rather than a `post`: the board must not assert a state change it
 * has no message to justify.
 */
export const TRANSITIONS: Readonly<
  Record<Column, Readonly<Partial<Record<DropColumn, Move>>>>
> = {
  draft: {
    queued: post("/api/queue", (slug) => queueJob(slug)),
  },
  queued: {
    draft: post("/api/dequeue", (slug) => dequeueJob(slug)),
    working: post("/api/start", (slug) => startJob(slug)),
  },
  working: {
    blocked: post("/api/stop", (slug) => stopJob(slug)),
  },
  blocked: {
    done: post("/api/done", (slug) => doneJob(slug)),
    working: { kind: "steer", mode: "unblock", requireText: false },
  },
  done: {
    // text:"" is the wire's "reactivate without steering" — the endpoint takes
    // the same shape as the modal's send, with an empty message.
    blocked: post("/api/reactivate", (slug) => reactivateJob(slug, "")),
    working: { kind: "steer", mode: "reactivate", requireText: true },
    [ARCHIVED]: post("/api/archive", (slug) => archiveJob(slug)),
  },
};

/**
 * Cross-column adjacency, derived from TRANSITIONS — the set of drawers and
 * columns that light up as `.droptarget` while a card from `from` is in the
 * air. The source column is NOT in its own list, matching the vanilla board:
 * a same-column drop is a reorder and shows itself by the cards shifting, not
 * by a highlight.
 */
export const MOVES: Readonly<Record<Column, readonly DropColumn[]>> = Object.freeze(
  Object.fromEntries(
    (Object.keys(TRANSITIONS) as Column[]).map((from) => [
      from,
      Object.freeze(Object.keys(TRANSITIONS[from]) as DropColumn[]),
    ]),
  ) as Record<Column, readonly DropColumn[]>,
);

/**
 * What a drop from `from` onto `to` does. `null` means the pair is not in the
 * table and the drop is a no-op — the ONLY correct answer for an unlisted
 * pair. A drop back onto the source column is always a reorder.
 */
export function resolveMove(from: Column, to: DropColumn): Move | null {
  if (from === to) return { kind: "reorder" };
  const row = TRANSITIONS[from] as Record<string, Move> | undefined;
  if (row === undefined) return null;
  // hasOwnProperty.call, not `row[to]` — `to` comes from a droppable id and a
  // job state, i.e. from data. A column named `constructor` or `toString`
  // would otherwise resolve to an Object.prototype member and pass the
  // truthiness check, and this table's members POST. Same reason
  // threadBlockHTML uses it in the vanilla board (app.js:2529).
  if (!Object.prototype.hasOwnProperty.call(row, to)) return null;
  return row[to] ?? null;
}

/** True when a card from `from` may legally be dropped on `to`'s highlight. */
export function isLegalTarget(from: Column | null, to: DropColumn): boolean {
  if (from === null || from === to) return false;
  return resolveMove(from, to) !== null;
}
