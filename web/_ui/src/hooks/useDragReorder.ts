// useDragReorder — the board's drag & drop wiring, on dnd-kit.
//
// This replaces ~200 lines of vanilla app.js: `flipMove` (a hand-rolled FLIP
// animation), `insertionIndex` + the `lastSlotAnchor` dragover throttle,
// `dragOriginParent`/`dragOriginAnchor` snap-back bookkeeping, and the
// `sameColumnDropHandled` flag that told dragend whether postReorder had
// already settled the DOM. dnd-kit owns all of that. What CANNOT be delegated
// — and is therefore all this file does — is the part with consequences:
//
//   1. which (from, to) pairs are legal and what each one posts (lib/moves.ts)
//   2. the optimistic same-column reorder and its revert (lib/predictReorder.ts)
//   3. suppressing the 3s poll for the duration of the drag
//
// Why dnd-kit at all (open question Q2): native HTML5 drag does not fire on
// touch AT ALL, so the vanilla board is effectively read-only on a phone —
// and the board is reachable over tailscale today. Everything below that
// mentions TouchSensor is that gap being closed.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  KeyboardSensor,
  MouseSensor,
  TouchSensor,
  pointerWithin,
  rectIntersection,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type {
  CollisionDetection,
  DragEndEvent,
  DragOverEvent,
  DragStartEvent,
  UniqueIdentifier,
} from "@dnd-kit/core";
import { arrayMove, sortableKeyboardCoordinates } from "@dnd-kit/sortable";

import { reorderColumn } from "../api/client.ts";
import type { WebBoard, WebJob } from "../api/types.ts";
import { jobKey } from "../lib/jobKey.ts";
import { columnSlugs, predictReorderedJobs } from "../lib/predictReorder.ts";
import { ARCHIVED, SUBMIT_ENDPOINT, resolveMove } from "../lib/moves.ts";
import type { DropColumn, SteerMode } from "../lib/moves.ts";
import { COLUMNS } from "./useBoard.ts";
import type { BoardModel, Column } from "./useBoard.ts";

// ------------------------------------------------------------ droppable ids
//
// Two kinds of droppable share one id space: every column (and the archive
// drawer) is a droppable so a card can be dropped on empty space, and every
// card is a sortable so a drop between cards knows its slot. Column ids are
// prefixed because a job slug is arbitrary user text and `draft` is a
// perfectly legal slug.

export const COLUMN_DROP_PREFIX = "col:";

export const columnDropId = (column: DropColumn): string =>
  COLUMN_DROP_PREFIX + column;

export function parseColumnDropId(id: UniqueIdentifier | undefined): DropColumn | null {
  if (id === undefined) return null;
  const s = String(id);
  if (!s.startsWith(COLUMN_DROP_PREFIX)) return null;
  return s.slice(COLUMN_DROP_PREFIX.length) as DropColumn;
}

// ------------------------------------------------------------ sensors

/**
 * Mouse drags start after 4px of travel. Without a distance constraint every
 * click on a card would be a zero-length drag, and the board's cards are
 * click targets first (steer / editor / chips).
 */
export const MOUSE_ACTIVATION = { distance: 4 } as const;

/**
 * Touch drags start after a 250ms hold that stays within 8px. This is the
 * whole point of moving to dnd-kit: without the DELAY a finger drag on the
 * board would be a card drag and the columns could never be scrolled; without
 * the TOLERANCE a hold that wobbles by a pixel — every real thumb — would
 * cancel. 250ms is the conventional long-press threshold and is short enough
 * that a deliberate drag does not feel stuck.
 */
export const TOUCH_ACTIVATION = { delay: 250, tolerance: 8 } as const;

/**
 * Where a drag must NOT start, ported from the vanilla dragstart guard
 * (app.js:957). Browsers treat a press-and-drag that begins on a form control
 * inside a draggable ancestor as dragging the ANCESTOR rather than selecting
 * text — which turned "select my typed note to fix a word" into a real column
 * move that posted. The two named panels are the vanilla list verbatim; the
 * generic control selectors are the same rule stated as its reason, so a new
 * in-card input inherits the protection instead of re-earning the bug.
 * `[data-no-drag]` is the explicit opt-out for anything else.
 */
export const NO_DRAG_SELECTOR = [
  ".doc-questions",
  ".job-feedback",
  "[data-no-drag]",
  "input",
  "textarea",
  "select",
  "[contenteditable]:not([contenteditable='false'])",
].join(",");

export function shouldStartDrag(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return true;
  return target.closest(NO_DRAG_SELECTOR) === null;
}

/**
 * MouseSensor + TouchSensor rather than the single PointerSensor dnd-kit
 * defaults to: PointerSensor handles touch through pointer events, and a
 * pointer-based drag cannot be given a touch-only activation delay without
 * also delaying the mouse. Separate sensors are how the two gestures get the
 * two different activation rules above.
 *
 * KeyboardSensor is a straight win the native implementation never had — the
 * vanilla board had NO keyboard path to reorder or move a card at all. It
 * shares the same activator listeners as the pointer sensors, so it inherits
 * the form-control guard without a second implementation.
 */
export function useBoardSensors() {
  return useSensors(
    useSensor(MouseSensor, { activationConstraint: MOUSE_ACTIVATION }),
    useSensor(TouchSensor, { activationConstraint: TOUCH_ACTIVATION }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
}

/**
 * Pointer-first collision detection. `pointerWithin` answers "what is the
 * cursor actually inside", which for nested droppables (a card inside a
 * column) ranks the card first — exactly what a slot-precise reorder needs.
 * It returns nothing once the pointer leaves every droppable (dragging past
 * the last column, or off the top of the board), and `rectIntersection` is
 * the fallback that still finds the column the CARD overlaps. Touch drags in
 * particular spend a lot of time with the finger outside the rect it is
 * dragging.
 */
export const boardCollisionDetection: CollisionDetection = (args) => {
  const within = pointerWithin(args);
  return within.length > 0 ? within : rectIntersection(args);
};

// ------------------------------------------------------------ optimistic order

/**
 * There is exactly ONE override slot, not one per column — a deliberate
 * simplification with a known, self-healing cost. Drop in DRAFT and then drop
 * in BLOCKED before the first POST lands (two drags inside one localhost
 * round-trip), and the second override replaces the first: DRAFT's settle
 * callback then finds a different column and no-ops, so DRAFT snaps back to
 * the pre-drag server order until the next 3s poll corrects it. Cosmetic, and
 * a map keyed by column would have to answer what "settled" means across
 * columns. Stated here so the assumption is a choice rather than an oversight.
 */
interface Override {
  column: Column;
  /** The order posted to /api/reorder, and what the board renders until cleared. */
  slugs: string[];
  /** False while the POST is in flight — the override is held unconditionally. */
  settled: boolean;
  /** The board payload current when the POST settled; a newer one is a verdict. */
  at: WebBoard | undefined;
  /** How many post-settle payloads have disagreed with the prediction. */
  disagreements: number;
}

// ------------------------------------------------------------ in-flight submit

/**
 * One card the board is showing somewhere the server has not put it yet.
 *
 * Submit used to be a parking write that answered instantly and left the job
 * in QUEUED for a daemon tick to pick up; the column was the progress
 * indicator. It now cuts a worktree and spawns a tmux session before it
 * answers, so the request holds for seconds — and with no queued column the
 * card would otherwise sit motionless in DRAFT for all of them, indicating
 * nothing, with a second drag able to fire a second submit at the same job.
 *
 * So the card is RELOCATED into WORKING for the duration and marked in flight.
 * The job object is carried here rather than looked up by key, because for
 * those same seconds the job exists in no column the board would find it in:
 * the draft list still holds it (the server has not moved it) but the working
 * list does not.
 */
interface Submitting {
  /** jobKey of the card, never a bare slug. */
  key: string;
  /** The card to draw in WORKING while the POST is in flight. */
  job: WebJob;
}

// ------------------------------------------------------------ the hook

export interface DragReorderOptions {
  board: BoardModel;
  /**
   * blocked→working and done→working open the steer modal instead of posting.
   * The drag chooses the MODE; the user's text is what actually moves the job.
   * This is a callback rather than a fetch on purpose — a drag must never
   * assert a state change it has no message to justify.
   */
  onOpenSteer: (
    slug: string,
    mode: SteerMode,
    opts: { requireText: boolean },
  ) => void;
  /**
   * Transient bottom-center notice. Two callers: a reverted reorder, and a
   * submit refused locally because that same card's POST is still running.
   * Both are cases where the gesture did something other than what it looked
   * like, and neither has a server answer to park in the status corner.
   */
  onToast?: (message: string) => void;
  /**
   * The jobKeys the board is currently RENDERING, when a search is narrowing
   * it; undefined means every card is on screen. It is a set of ids and never
   * a column: the reorder body is still built from the whole `board.byColumn`,
   * because posting the visible subset would move every hidden card to the top
   * of its column (`store.SetBoardColumn` stores exactly what it is sent and
   * `jobs.ApplyColumnOrder` puts absent members first).
   *
   * The one thing it changes is where "the end" is. A drop on the column BODY
   * lands the card in the last slot, and under a filter the last slot of the
   * whole column is not the place the pointer is — it is below cards that are
   * not on screen. Without this, dropping into the space under the last
   * visible card moves it past hidden cards, and the rendered order is
   * identical before and after: a write with nothing to show for it.
   */
  visibleKeys?: ReadonlySet<string>;
}

export interface DragReorderModel {
  /** jobKey of the card in the air, or null. */
  activeKey: string | null;
  activeJob: WebJob | null;
  /** The column the active card started in. */
  from: Column | null;
  /** The column/drawer the pointer is currently over. */
  over: DropColumn | null;
  sensors: ReturnType<typeof useBoardSensors>;
  collisionDetection: CollisionDetection;
  onDragStart: (e: DragStartEvent) => void;
  onDragOver: (e: DragOverEvent) => void;
  onDragEnd: (e: DragEndEvent) => void;
  onDragCancel: () => void;
  /**
   * A column's cards in RENDER order: the optimistic reorder if this column
   * has one, and any in-flight submits relocated out of DRAFT into WORKING.
   * Identity when neither applies.
   */
  orderJobs: (column: Column, jobs: WebJob[]) => WebJob[];
  /** True while a card is in the air and `to` is a legal destination for it. */
  isTarget: (to: DropColumn) => boolean;
  /**
   * jobKeys of every card whose submit POST is still running. A SET rather
   * than one key: submits of different jobs run concurrently, and each clears
   * on its own answer.
   */
  submittingKeys: ReadonlySet<string>;
}

export function useDragReorder({
  board,
  onOpenSteer,
  onToast,
  visibleKeys,
}: DragReorderOptions): DragReorderModel {
  const [activeKey, setActiveKey] = useState<string | null>(null);
  const [from, setFrom] = useState<Column | null>(null);
  const [over, setOver] = useState<DropColumn | null>(null);
  const [override, setOverride] = useState<Override | null>(null);
  /**
   * A LIST, not a single slot — one entry per card whose submit POST is still
   * running, and the whole point is that they are independent.
   *
   * A single slot made the in-flight state board-global: the guard below read
   * "is anything submitting" rather than "is THIS card submitting", so a
   * second, different draft dropped during the seconds the first one takes was
   * dropped with no POST and nothing on screen — an ordinary two-drag sequence
   * silently losing one of them. Worse, nothing here aborts: a POST that never
   * resolves never clears the slot, so one hung request disabled the board's
   * only start affordance permanently rather than just its own card's.
   *
   * Concurrent submits of DIFFERENT jobs are safe to let through — the server
   * serialises them, and StartCell's already-live and worktree guards turn a
   * genuine duplicate into a clean 409 rather than a second cell.
   */
  const [submitting, setSubmitting] = useState<readonly Submitting[]>([]);
  const submittingKeys = useMemo(
    () => new Set(submitting.map((s) => s.key)),
    [submitting],
  );

  // Read inside async continuations that must see the CURRENT value rather
  // than the one closed over at drag start.
  const boardRef = useRef<WebBoard | undefined>(board.board);
  boardRef.current = board.board;
  const activeRef = useRef<{ key: string; from: Column } | null>(null);

  const { byKey, byColumn, archived, setDragging, refetch, mutate } = board;

  /** jobKey → the column (or the drawer) it currently lives in. */
  const columnOf = useMemo(() => {
    const m = new Map<string, DropColumn>();
    for (const c of COLUMNS) for (const j of byColumn[c]) m.set(jobKey(j), c);
    for (const j of archived) m.set(jobKey(j), ARCHIVED);
    return m;
  }, [byColumn, archived]);

  const resolveOver = useCallback(
    (id: UniqueIdentifier | undefined): DropColumn | null => {
      const asColumn = parseColumnDropId(id);
      if (asColumn !== null) return asColumn;
      if (id === undefined) return null;
      return columnOf.get(String(id)) ?? null;
    },
    [columnOf],
  );

  const orderJobs = useCallback(
    (column: Column, jobs: WebJob[]): WebJob[] => {
      let out = jobs;
      if (override && override.column === column) {
        const bySlug = new Map(jobs.map((j) => [j.slug, j] as const));
        const ordered: WebJob[] = [];
        for (const s of override.slugs) {
          const j = bySlug.get(s);
          if (j) {
            ordered.push(j);
            bySlug.delete(s);
          }
        }
        // A job the override has never heard of (created between the drop and
        // now) goes last rather than being dropped. It lives there for at most
        // one poll — the override is cleared by the very next payload.
        for (const j of jobs) if (bySlug.has(j.slug)) ordered.push(j);
        out = ordered;
      }
      // The in-flight submits, applied AFTER the reorder: the reorder is about
      // where a card sits inside the order the server holds, and this is about
      // which column shows it at all, which is the later question. Both can be
      // live at once — a reorder in DRAFT settles on its own clock.
      if (submitting.length > 0) {
        if (column === "draft") {
          out = out.filter((j) => !submittingKeys.has(jobKey(j)));
        } else if (column === "working") {
          // The dedupe is load-bearing, not defensive. mutate() refetches
          // BEFORE the await returns, so there is at least one render in which
          // the payload already carries the job as working and its entry is
          // still here — without this the card would appear twice, and React
          // would key-collide on jobKey while it did.
          const add = submitting.filter(
            (s) => !out.some((j) => jobKey(j) === s.key),
          );
          // Newest first, in front of the column: several can be in flight at
          // once, and the order they were dropped in is the only order that
          // means anything while none of them has a server position yet.
          if (add.length > 0) out = [...add.map((s) => s.job).reverse(), ...out];
        }
      }
      return out;
    },
    [override, submitting, submittingKeys],
  );

  // The override's exit. The POST has landed; the question is whether the
  // server's next word agrees with what the board is already showing.
  useEffect(() => {
    if (!override || !override.settled) return;
    const incoming = board.board;
    if (incoming === override.at) return; // no payload since the POST settled
    const order = columnSlugs(incoming?.jobs ?? [], override.column);
    const agrees =
      order.length === override.slugs.length &&
      order.every((s, i) => s === override.slugs[i]);
    if (agrees) {
      // The prediction was right: dropping the override changes nothing that
      // renders. This is the no-flash guarantee, and it is the entire reason
      // predictReorderedJobs exists.
      setOverride(null);
      return;
    }
    // A disagreeing payload is either STALE — /api/board memoizes for 2s, so
    // a tick that started before the POST can land after it — or a real
    // change from another client. Those are indistinguishable from here, so
    // tolerate exactly one (≈one 3s tick) before handing the column back.
    // Clearing on the first would flash the pre-drag order; never clearing
    // would mask a real change forever.
    setOverride((o) =>
      o === null
        ? null
        : o.disagreements >= 1
          ? null
          : { ...o, disagreements: o.disagreements + 1, at: incoming },
    );
  }, [board.board, override]);

  const finishDrag = useCallback(() => {
    activeRef.current = null;
    setActiveKey(null);
    setFrom(null);
    setOver(null);
    // The board may rebuild again — the drag is over, the pointer is gone.
    setDragging(false);
  }, [setDragging]);

  const onDragStart = useCallback(
    (e: DragStartEvent) => {
      const key = String(e.active.id);
      const col = columnOf.get(key);
      // Archived cards are not draggable (nothing leaves the drawer by drag),
      // and an unknown key means the board moved under us.
      if (col === undefined || col === ARCHIVED) return;
      activeRef.current = { key, from: col };
      setActiveKey(key);
      setFrom(col);
      setOver(col);
      // Poll suppression: the board must not rebuild under the pointer. The
      // vanilla equivalent was `if (dragging) return` at the top of refresh().
      setDragging(true);
    },
    [columnOf, setDragging],
  );

  const onDragOver = useCallback(
    (e: DragOverEvent) => {
      setOver(resolveOver(e.over?.id));
    },
    [resolveOver],
  );

  /**
   * The index, in the WHOLE column, of the last card the board is rendering.
   * `ordered.length - 1` when nothing is filtered — and also when a filter is
   * running but somehow hides every card in this column, which cannot happen
   * from a real drag: the card in the air was visible enough to grab.
   */
  const lastVisibleIndex = useCallback(
    (ordered: WebJob[]): number => {
      if (!visibleKeys) return ordered.length - 1;
      for (let i = ordered.length - 1; i >= 0; i--) {
        if (visibleKeys.has(jobKey(ordered[i]!))) return i;
      }
      return ordered.length - 1;
    },
    [visibleKeys],
  );

  const commitReorder = useCallback(
    async (column: Column, key: string, overId: UniqueIdentifier | undefined) => {
      const ordered = orderJobs(column, byColumn[column]);
      const oldIdx = ordered.findIndex((j) => jobKey(j) === key);
      // Dropped on the column's own body rather than on a card: the slot is
      // the end of the list, which is where the pointer necessarily is — and
      // under a filter "the end" is the last card ON SCREEN, not the last card
      // in the column. With every card visible the two are the same index and
      // this is the path it has always been.
      //
      // Clamped at oldIdx so a drop at the end can only ever move a card DOWN.
      // `visibleKeys` is a snapshot of the CURRENT render while the drag began
      // against an earlier one — nothing in the search bar's cmd+K handler
      // asks whether a card is in the air — and a set that no longer holds the
      // dragged card would otherwise report a last-visible index ABOVE it,
      // sending a "put this at the end" gesture to the TOP of the column. The
      // clamp makes that structural rather than an argument about which render
      // produced the set; equal indices then take the no-op skip below.
      const newIdx =
        parseColumnDropId(overId) !== null
          ? Math.max(oldIdx, lastVisibleIndex(ordered))
          : ordered.findIndex((j) => jobKey(j) === String(overId));
      if (oldIdx < 0 || newIdx < 0) {
        await refetch();
        return;
      }
      // The vanilla board posted every same-column drop, including the ones
      // that changed nothing (a 4px twitch, a drop back in the same slot).
      // Skipping those costs nothing — the rendered order is already the
      // server's — and saves a write on the board's most-clicked surface.
      if (oldIdx === newIdx) return;

      const slugs = arrayMove(ordered, oldIdx, newIdx).map((j) => j.slug);
      const predicted = predictReorderedJobs(
        boardRef.current?.jobs ?? [],
        column,
        slugs,
      );
      if (predicted === null) {
        // The client's picture of this column no longer lines up with the
        // order it just posted — a job appeared, vanished, or changed column
        // mid-drag. Predicting from a stale picture is worse than a repaint,
        // so this one drop goes down the pessimistic path.
        await mutate(() => reorderColumn(column, slugs));
        return;
      }

      setOverride({
        column,
        slugs,
        settled: false,
        at: undefined,
        disagreements: 0,
      });
      try {
        await reorderColumn(column, slugs);
        // Deliberately NOT a refetch: the board already shows the final
        // order, and refetching would repaint identical content — the flash
        // this whole path exists to avoid.
        setOverride((o) =>
          o !== null && o.column === column && !o.settled
            ? { ...o, settled: true, at: boardRef.current }
            : o,
        );
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        setOverride(null);
        onToast?.(`reorder failed: ${msg} — reverted`);
        await refetch();
      }
    },
    [byColumn, lastVisibleIndex, mutate, onToast, orderJobs, refetch],
  );

  const onDragEnd = useCallback(
    (e: DragEndEvent) => {
      const active = activeRef.current;
      finishDrag();
      if (active === null) return;
      const to = resolveOver(e.over?.id);
      const job = byKey.get(active.key);
      // Dropped outside every droppable, or onto a card the board no longer
      // knows: nothing to do but correct whatever staleness the suppressed
      // poll accumulated.
      if (to === null || job === undefined) {
        void refetch();
        return;
      }
      const move = resolveMove(active.from, to);
      if (move === null) {
        // A pair that is not in the table. No-op — never a nearest-legal
        // fallback: every entry in that table starts a cell or stops an agent.
        void refetch();
        return;
      }
      if (move.kind === "reorder") {
        void commitReorder(active.from, active.key, e.over?.id);
        return;
      }
      if (move.kind === "steer") {
        onOpenSteer(job.slug, move.mode, { requireText: move.requireText });
        void refetch();
        return;
      }
      // Cross-column transitions are PESSIMISTIC: await, then refetch on both
      // outcomes. mutate() does that and parks the failure text in the
      // tick-stat corner, exactly as the vanilla post() did.
      if (move.endpoint === SUBMIT_ENDPOINT) {
        // The one slow post. Hold the card in WORKING, marked in flight, for
        // as long as the request takes; clear on BOTH outcomes and let the
        // board's own payload be the answer — success has already moved the
        // job there, and a refusal leaves it in DRAFT with mutate()'s error
        // text in the status corner. Nothing here writes a state of its own,
        // so a 409 needs no bounce-back logic: dropping the relocation IS the
        // bounce.
        //
        // A second submit of THIS card is refused locally rather than sent —
        // the card is drawn in WORKING and marked non-draggable while its POST
        // runs, so a duplicate can only come from a stale render, and this POST
        // spawns a tmux session, which is the one thing on the board worth
        // being sure happens once.
        //
        // Per CARD, never board-wide: a different draft dropped meanwhile is a
        // separate start carl asked for and it goes through. And the refusal
        // is AUDIBLE, because the alternative is the failure this whole
        // in-flight state exists to prevent — a start-shaped gesture that
        // leaves no trace of having been declined.
        if (submittingKeys.has(active.key)) {
          onToast?.(`${job.slug} is already starting`);
          void refetch();
          return;
        }
        setSubmitting((prev) => [...prev, { key: active.key, job }]);
        void mutate(() => move.run(job.slug)).finally(() =>
          setSubmitting((prev) => prev.filter((s) => s.key !== active.key)),
        );
        return;
      }
      void mutate(() => move.run(job.slug));
    },
    [
      byKey,
      commitReorder,
      finishDrag,
      mutate,
      onOpenSteer,
      onToast,
      refetch,
      resolveOver,
      submittingKeys,
    ],
  );

  const onDragCancel = useCallback(() => {
    finishDrag();
    void refetch();
  }, [finishDrag, refetch]);

  const isTarget = useCallback(
    (to: DropColumn): boolean => {
      if (from === null || from === to) return false;
      return resolveMove(from, to) !== null;
    },
    [from],
  );

  const sensors = useBoardSensors();

  return {
    activeKey,
    activeJob: activeKey === null ? null : (byKey.get(activeKey) ?? null),
    from,
    over,
    sensors,
    collisionDetection: boardCollisionDetection,
    onDragStart,
    onDragOver,
    onDragEnd,
    onDragCancel,
    orderJobs,
    isTarget,
    submittingKeys,
  };
}
