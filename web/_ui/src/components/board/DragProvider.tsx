// DragProvider — the <DndContext> the whole board lives inside, plus the four
// hooks the board's own components consume to take part in a drag.
//
// The split is deliberate: useDragReorder owns every decision with a
// consequence (which pairs are legal, what each posts, the optimistic order
// and its revert), and this file owns only the plumbing that has to be React
// — the context, the sensors, the drag overlay, and the per-element props.
// BoardView / Column / JobCard never import @dnd-kit at all; they spread what
// these hooks return.
//
// Consumers, in the order a board renders them:
//
//   <DragProvider board={board} onOpenSteer={…} onToast={…}>   — once, around the board
//     useColumnJobs(column, jobs)   — the render order for a column's cards
//     useColumnDrop(column)         — a column section or the archive drawer
//     <SortableColumn column jobs>  — wraps that column's cards (renders no DOM)
//       useSortableCard(jobKey(j))  — one card
//
// `useColumnDrop(ARCHIVED)` is what makes the archive drawer a drop target for
// done→archived.

import { createContext, useContext, useMemo } from "react";
import type { ReactNode } from "react";
import { DndContext, DragOverlay, useDroppable } from "@dnd-kit/core";
import {
  SortableContext,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import type { WebJob } from "../../api/types.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { DropColumn, SteerMode } from "../../lib/moves.ts";
import type { Column } from "../../hooks/useBoard.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import {
  columnDropId,
  shouldStartDrag,
  useDragReorder,
} from "../../hooks/useDragReorder.ts";

interface DragContextValue {
  activeKey: string | null;
  orderJobs: (column: Column, jobs: WebJob[]) => WebJob[];
  isTarget: (to: DropColumn) => boolean;
}

/**
 * The no-provider default is identity, not a throw. A board rendered outside
 * a DragProvider — a unit test of a single Column, a future read-only view —
 * must still show its cards in server order rather than blow up.
 */
const IDENTITY: DragContextValue = {
  activeKey: null,
  orderJobs: (_column, jobs) => jobs,
  isTarget: () => false,
};

const DragCtx = createContext<DragContextValue>(IDENTITY);

export interface DragProviderProps {
  board: BoardModel;
  onOpenSteer: (
    slug: string,
    mode: SteerMode,
    opts: { requireText: boolean },
  ) => void;
  onToast?: (message: string) => void;
  /**
   * What follows the pointer. Defaults to a minimal ghost carrying the slug —
   * JobCard is another module's file, so the real card is passed IN rather
   * than imported here.
   */
  renderOverlay?: (job: WebJob | null, key: string) => ReactNode;
  /**
   * The jobKeys currently on screen while a search narrows the board;
   * undefined when nothing is filtered. Passed straight through to
   * useDragReorder, which reads it for ONE thing — where "the end of the
   * column" is for a drop on empty space. The board itself stays whole: this
   * provider is deliberately still given the UNFILTERED BoardModel.
   */
  visibleKeys?: ReadonlySet<string>;
  children: ReactNode;
}

export function DragProvider({
  board,
  onOpenSteer,
  onToast,
  renderOverlay,
  visibleKeys,
  children,
}: DragProviderProps) {
  const drag = useDragReorder({ board, onOpenSteer, onToast, visibleKeys });

  const value = useMemo<DragContextValue>(
    () => ({
      activeKey: drag.activeKey,
      orderJobs: drag.orderJobs,
      isTarget: drag.isTarget,
    }),
    [drag.activeKey, drag.orderJobs, drag.isTarget],
  );

  return (
    <DndContext
      sensors={drag.sensors}
      collisionDetection={drag.collisionDetection}
      onDragStart={drag.onDragStart}
      onDragOver={drag.onDragOver}
      onDragEnd={drag.onDragEnd}
      onDragCancel={drag.onDragCancel}
    >
      <DragCtx.Provider value={value}>{children}</DragCtx.Provider>
      {/* Rendered in a portal-like layer above the columns: a card dragged
          across a column boundary would otherwise be clipped by the source
          column's own overflow. */}
      <DragOverlay dropAnimation={null}>
        {drag.activeKey === null
          ? null
          : (renderOverlay?.(drag.activeJob, drag.activeKey) ?? (
              <div className="card dragging-overlay">
                <div className="card-top">
                  <span className="slug">{drag.activeJob?.slug ?? drag.activeKey}</span>
                </div>
              </div>
            ))}
      </DragOverlay>
    </DndContext>
  );
}

/** The board's drag state, for anything that needs it beyond the hooks below. */
export function useDragState(): DragContextValue {
  return useContext(DragCtx);
}

/**
 * A column's cards in RENDER order — server order, or the optimistic order a
 * just-dropped reorder is holding. Call this before rendering the cards and
 * before anything that reads their order.
 */
export function useColumnJobs(column: Column, jobs: WebJob[]): WebJob[] {
  return useContext(DragCtx).orderJobs(column, jobs);
}

export interface ColumnDrop {
  /** Ref for the column `<section>` / the archive drawer `<div>`. */
  setNodeRef: (element: HTMLElement | null) => void;
  /** The pointer is inside this drop zone right now. */
  isOver: boolean;
  /** A card is in the air and this zone is a legal destination for it. */
  isTarget: boolean;
  /**
   * `droptarget` / `drophover` — the two vanilla classes, on the same
   * elements and with the same meaning (style.css's `.col.droptarget` /
   * `.col.drophover` rules). Append it to the element's own className.
   */
  className: string;
}

/**
 * Makes a column section — or the archive drawer, via `useColumnDrop(ARCHIVED)`
 * — a drop zone.
 *
 * Note for the drawer: the vanilla board needed
 * `.archive-head:disabled { pointer-events: none }` because a disabled button
 * swallows drag events and the header IS the empty drawer's whole drop area.
 * dnd-kit does not hit-test through DOM events during a drag — it measures
 * registered droppable rects and resolves collisions geometrically — so that
 * rule is no longer load-bearing here.
 */
export function useColumnDrop(column: DropColumn): ColumnDrop {
  const { isTarget } = useContext(DragCtx);
  const { setNodeRef, isOver } = useDroppable({ id: columnDropId(column) });
  const target = isTarget(column);
  const className = [target ? "droptarget" : "", target && isOver ? "drophover" : ""]
    .filter(Boolean)
    .join(" ");
  return { setNodeRef, isOver, isTarget: target, className };
}

export interface SortableColumnProps {
  column: Column;
  /** The column's jobs; ordering is applied here, so server order is fine. */
  jobs: WebJob[];
  children: ReactNode;
}

/**
 * Registers a column's cards as one sortable list. Renders no DOM of its own —
 * wrap the `.col-cards` children with it and the markup is unchanged.
 */
export function SortableColumn({ column, jobs, children }: SortableColumnProps) {
  const ordered = useColumnJobs(column, jobs);
  const items = useMemo(() => ordered.map((j) => jobKey(j)), [ordered]);
  return (
    <SortableContext items={items} strategy={verticalListSortingStrategy}>
      {children}
    </SortableContext>
  );
}

export interface SortableCard {
  setNodeRef: (element: HTMLElement | null) => void;
  /** Spread on the card element: role/tabIndex/aria for the KeyboardSensor. */
  attributes: Record<string, unknown>;
  /** Spread on the card element: the sensor activators, form-control guarded. */
  listeners: Record<string, (event: never) => void>;
  style: { transform?: string; transition?: string };
  isDragging: boolean;
  /** `"dragging"` while in the air — style.css's `.card.dragging` rule dims it to 0.35. */
  className: string;
}

/**
 * One draggable card. `key` is jobKey(job), never the bare slug — an archived
 * entry and an active job that reused its freed slug would otherwise share a
 * drag id (see lib/jobKey.ts).
 */
export function useSortableCard(key: string): SortableCard {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: key });

  // The vanilla dragstart guard (app.js:957), reimplemented one layer up.
  //
  // dnd-kit's own way to do this is to subclass each sensor and override its
  // static `activators`; wrapping the listeners instead is one implementation
  // covering ALL of them — mouse, touch, and keyboard — and it is the layer
  // where the DOM target actually is. Without it, a press-drag that starts on
  // an input inside a card drags the CARD (browsers resolve the gesture to
  // the draggable ancestor), so selecting a word of typed feedback posts a
  // real column move.
  const guarded = useMemo(() => {
    const out: Record<string, (event: never) => void> = {};
    for (const [name, fn] of Object.entries(listeners ?? {})) {
      out[name] = (event: never) => {
        const e = event as unknown as { target?: EventTarget | null };
        if (!shouldStartDrag(e?.target ?? null)) return;
        (fn as (ev: unknown) => void)(event);
      };
    }
    return out;
  }, [listeners]);

  return {
    setNodeRef,
    attributes: attributes as unknown as Record<string, unknown>,
    listeners: guarded,
    style: {
      transform: CSS.Transform.toString(transform) ?? undefined,
      transition: transition ?? undefined,
    },
    isDragging,
    className: isDragging ? "dragging" : "",
  };
}
