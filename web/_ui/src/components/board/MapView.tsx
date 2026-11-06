import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactElement,
  type ReactNode,
  type RefObject,
} from "react";

import {
  ApiError,
  fetchMapDrawings,
  fetchMapPositions,
  saveMapDrawings,
  saveMapPositions,
} from "../../api/client.ts";
import type { WebJob } from "../../api/types.ts";
import type { JobSearchModel } from "../../hooks/useJobSearch.ts";
import { readLS, writeLS } from "../../hooks/useLocalStorage.ts";
import { shouldStartDrag } from "../../hooks/useDragReorder.ts";
import { leadPose, mapStatus } from "../../lib/agentmap/activity.ts";
import { readPalette, type Palette } from "../../lib/agentmap/draw.ts";
import {
  DRAW_SAVE_TIMEOUT_MS,
  DRAW_STROKE_PX,
  DRAW_TEXT_PX,
  DRAW_TOOLS,
  ERASE_TOL_PX,
  MAX_DRAWINGS,
  MAX_PEN_POINTS,
  MAX_TEXT_RUNES,
  drawingBox,
  hitDrawing,
  newDrawingId,
  penPath,
  readUnsavedDrawOps,
  sanitizeDrawings,
  simplifyStroke,
  trimDrawText,
  validDrawing,
  writeUnsavedDrawOps,
  type DrawBox,
  type DrawKind,
  DRAW_COLORS,
  type DrawColor,
  type DrawOps,
  type DrawTool,
  type Drawing,
} from "../../lib/agentmap/drawings.ts";
import {
  freeSpot,
  marqueeKeys,
  overlaps,
  seedLayout,
  settle,
  stackOrder,
  type MapNode,
} from "../../lib/agentmap/layout.ts";
import { drawMinimap, miniFrame, miniToWorld, type MiniFrame } from "../../lib/agentmap/minimap.ts";
import {
  actorsShown,
  inkChosen,
  setActorsShown,
  setInkChosen,
  setToolsShown,
  toolsShown,
} from "../../lib/agentmap/prefs.ts";
import {
  drawCrossMsg,
  drawScene,
  drawSpawnEdge,
  drawThread,
  landCross,
  leadHead,
  newScene,
  sceneBusy,
  syncScene,
  type CardScene,
  type CrossMsg,
  type XY,
} from "../../lib/agentmap/scene.ts";
import { COLUMNS, type Column } from "../../lib/columns.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { BoardActions } from "./BoardView.tsx";
import { JobCard } from "./JobCard.tsx";

/** A map card's width; its height is whatever the JobCard inside it measures. */
const CARD_W = 300;
/** Height assumed for a card not yet measured (jsdom never measures one). */
const EST_H = 190;
/**
 * The characters' canvas reaches this far past the card: up for the lead's
 * head, a thought bubble and a message tag dropping in from above; sideways
 * for props held out past the card's ends.
 */
const PAD_TOP = 150;
const PAD_X = 64;
/**
 * How far the canvas reaches DOWN over the card's face, which the characters
 * now paint in front of: a seated character's dangling feet, never more. It
 * is the card's top padding at zoom 1 (style.css `.card { padding: 10px }`);
 * the map's text scale makes that padding 11.2 world pixels, so the 10 the
 * canvas reaches stays inside it and no character pixel can land on the
 * card's text.
 */
const PAD_BOTTOM = 10;
/**
 * Cap on every canvas' backing-store scale: device pixel ratio 2. A card's
 * canvas is never larger than its own strip at ratio 2, and the two
 * viewport-sized layers never larger than the viewport at ratio 2, however
 * deep the zoom or dense the screen.
 */
const MAX_BACKING = 2;
/** Settle steps one run may take before it hands the rest to a later frame. */
const SETTLE_STEPS = 400;
/** Positions the view changed are sent this long after the last change. */
const SAVE_DELAY_MS = 400;
/** A load that has not answered by now is placed provisionally (see below). */
const LOAD_TIMEOUT_MS = 4000;
/**
 * Per-card redraw intervals, by the lead's pose (`intervalFor`). A working
 * lead acting out a call draws at ~30 fps. A held pose (blocked, question,
 * asking, urgent, and a working lead in think or wait) moves gently enough for 12. A sleeper
 * (parked or frozen), an egg, a headstone or a spent lead reads the same at
 * four. A busy card (a bubble in
 * flight, crew walking, the pointer on it) draws at the fast rate whatever
 * its pose.
 */
const FAST_MS = 33;
const HELD_MS = 84;
const SLOW_MS = 250;
const HELD_POSES = new Set(["blocked", "question", "asking", "urgent", "think", "wait"]);
const SLOW_POSES = new Set(["parked", "frozen", "limit", "done", "draft"]);
/** Below this zoom the characters are a few pixels tall: every quiet card drops to the slow rate. */
const FAR_ZOOM = 0.45;
/** While an arrow is hot (its parent delegating) the under layer redraws this often for the dash run. */
const UNDER_HOT_MS = 100;
/** While a thread is fresh it fades, which needs only an occasional redraw. */
const UNDER_FRESH_MS = 500;
/** How often the arrows' hot state and the threads' freshness are re-derived from the clock. */
const UNDER_CHECK_MS = 500;
/**
 * A zoom is committed (canvases re-backed and, above 1, a CSS `zoom` that
 * relays out every card) this long after the last zoom event, HUD buttons
 * included. Long enough that a run of pinches or button presses pays for one
 * relayout once the hand has stopped, not one per pause: the relayout
 * measured about 300 ms on a 40-card board (2026-11-05), which between two
 * pinches reads as the map hanging.
 */
const ZOOM_COMMIT_MS = 900;
/**
 * Redraws of card canvases one frame may make for cards that are NOT in
 * motion (held and slow cadences). The slow cards all come due together
 * after anything that resets their clocks (a zoom commit, a theme change, the
 * first frame), and a frame that redrew all of them at once ran 47 to 98 ms
 * on a 40-card board (the lead's profile, 2026-11-05); capped, they are
 * spread over the next frames, most overdue first, and stay spread because
 * each keeps the time it was actually drawn. Cards in motion are never held
 * back, and neither is every waiting card: with six or more cards in motion
 * due, one waiting card (the most overdue) still draws, so none is starved.
 */
const CARD_BUDGET = 6;
/**
 * The arrow layer is drawn this far past every edge of the map, so a pan can
 * move the drawn layer instead of redrawing it until the pan has gone
 * further than this.
 */
const UNDER_MARGIN = 160;
/** A pan or zoom with no new event for this long has paused: the arrow layer is redrawn in place. */
const VIEW_PAUSE_MS = 140;
/** The overview's size, CSS pixels: the canvas' inline size and every overview frame read these. */
const MINI_W = 184;
const MINI_H = 120;
/** A thread is "fresh" (still fading) for this long after its last message, as drawThread fades it. */
const THREAD_FRESH_S = 40;
/** A moved card glides to its new place for this long (style.css `.map-card.glide`). */
const GLIDE_MS = 260;
const ZOOM_MIN = 0.25;
const ZOOM_MAX = 2.5;
/**
 * The per-browser pan and zoom. Positions are the server's, except one it has
 * not accepted yet, which waits under UNSAVED_KEY.
 */
const MAP_CAMERA_KEY = "kraken.mapView";

/** CSS `zoom` keeps cards as real, sharp DOM; a scale transform is the fallback. */
const ZOOM_OK = (() => {
  try {
    return typeof CSS !== "undefined" && typeof CSS.supports === "function" && CSS.supports("zoom", "2");
  } catch {
    return false;
  }
})();

/**
 * The map's gestures take pointer events, and mouse events only until the
 * first pointer event is seen. A browser fires pointerdown before the
 * compatibility mousedown, so there the mouse path is dead from the first
 * press on and no gesture runs twice. jsdom has no PointerEvent constructor,
 * so a pointer event there is a bare Event with no coordinates: it is ignored
 * (it could not place a card anyway) and does not switch the mouse path off.
 *
 * Whether a pointer event has been seen belongs to the MOUNTED map: each
 * MapView keeps its own flag in a ref and passes it here, so a fresh mount
 * handles its first press the same way whatever an earlier mount (or an
 * earlier test in the same file) was sent. Within one mount the card,
 * background, marquee and overview paths all read the same flag, so one
 * physical press still acts once.
 */
function gestureEventFor(pointerSeen: { current: boolean }, ev: { type: string; clientX?: number }): boolean {
  if (!ev.type.startsWith("mouse")) {
    // pointerup/cancel carry coordinates too in a browser; a coordinate-less
    // one ends nothing that a coordinate-less down could have started
    if (typeof ev.clientX !== "number" || !Number.isFinite(ev.clientX)) return false;
    pointerSeen.current = true;
    return true;
  }
  return !pointerSeen.current;
}

/**
 * Each canvas' 2D context, asked for once. A canvas with no 2D context (jsdom
 * has none, and logs a "not implemented" error on every ask) is remembered as
 * null, so the loop skips its drawing without asking again every frame.
 */
const contexts = new WeakMap<HTMLCanvasElement, CanvasRenderingContext2D | null>();
function ctxOf(cv: HTMLCanvasElement): CanvasRenderingContext2D | null {
  if (contexts.has(cv)) return contexts.get(cv) ?? null;
  let c: CanvasRenderingContext2D | null = null;
  try {
    c = cv.getContext("2d");
  } catch {
    c = null;
  }
  contexts.set(cv, c);
  return c;
}

/** Scratch head positions for the crossing bubbles, reused every frame. */
const HEAD_A: XY = [0, 0];
const HEAD_B: XY = [0, 0];

const raf: (cb: FrameRequestCallback) => number =
  typeof requestAnimationFrame === "function"
    ? (cb) => requestAnimationFrame(cb)
    : (cb) => window.setTimeout(() => cb(performance.now()), 33);
const caf: (id: number) => void =
  typeof cancelAnimationFrame === "function" ? (id) => cancelAnimationFrame(id) : (id) => window.clearTimeout(id);

interface ViewState {
  x: number;
  y: number;
  z: number;
}

/** One card moving with a group drag, and where it was at the press. */
interface GroupMember {
  key: string;
  ox: number;
  oy: number;
}

/** A press has turned into a drag once the pointer has gone this far (screen px, |dx| + |dy|). */
const DRAG_SLOP = 4;

type Drag =
  | {
      kind: "card";
      key: string;
      sx: number;
      sy: number;
      ox: number;
      oy: number;
      moved: boolean;
      /** the selection, when the pressed card is in it: every one of these moves with it */
      group: GroupMember[] | null;
    }
  /** `lx`/`ly`: the last pointer position a move reported (the release point's fallback, see onUp) */
  | { kind: "pan"; sx: number; sy: number; ox: number; oy: number; moved: boolean; lx: number; ly: number }
  /**
   * A press with Shift held. Dragged, it draws the marquee from the press
   * point (world units, `wx`/`wy`); released without a drag on a card
   * (`card`), it toggles that card in the selection. `left`/`top` is the
   * map's page offset, read from layout once at the press and kept for the
   * drag, so the box follows the cursor even when something above the map
   * changed height since the last window resize.
   */
  | {
      kind: "marquee";
      sx: number;
      sy: number;
      left: number;
      top: number;
      wx: number;
      wy: number;
      moved: boolean;
      card: string | null;
      lx: number;
      ly: number;
    }
  /** a press on the overview: its frame and page offset, held for the whole drag */
  | { kind: "mini"; left: number; top: number; f: MiniFrame }
  /**
   * A press on the drawing surface with a shape tool armed. `wx`/`wy` is the
   * rounded world point of the press, `cx`/`cy` the rounded world point of
   * the last move, `pts` (pen only) every rounded point so far and `path`
   * its path data, grown by one segment per new point rather than rebuilt,
   * `size` the stroke width and `color` the ink, both fixed at the press.
   * `dead`: an Escape dropped the draft, so the release creates nothing.
   */
  | {
      kind: "draw";
      tool: ShapeKind;
      sx: number;
      sy: number;
      left: number;
      top: number;
      wx: number;
      wy: number;
      cx: number;
      cy: number;
      pts: number[];
      path: string;
      size: number;
      color?: DrawColor;
      moved: boolean;
      lx: number;
      ly: number;
      dead: boolean;
    }
  /** a press on the drawing surface with the eraser armed: every move erases too */
  | { kind: "erase"; left: number; top: number };

/** The kinds drawn by a press, a drag and a release; text has its own editor. */
type ShapeKind = Exclude<DrawKind, "text">;

/**
 * The open text editor: `id` names this opening (a late blur from an editor
 * already replaced commits nothing), `wx`/`wy` the rounded world point of
 * the press, `size` the font size the committed drawing gets, world units.
 * Both are fixed at the press; applyView puts the editor on that point
 * through the camera, at `size` times the zoom.
 */
interface EditorAt {
  id: number;
  wx: number;
  wy: number;
  size: number;
  /** the ink chosen when the editor opened; null for the theme's foreground */
  color: DrawColor | null;
}

/** Each tool button's name: its accessible label, since the button shows an icon. */
const TOOL_LABEL: Record<DrawTool, string> = {
  pen: "pen",
  line: "line",
  rect: "rect",
  ellipse: "oval",
  text: "text",
  erase: "erase",
};
const TOOL_TITLE: Record<DrawTool, string> = {
  pen: "draw freehand",
  line: "draw a straight line",
  rect: "draw a rectangle",
  ellipse: "draw an oval",
  text: "write a label",
  erase: "erase drawings",
};
/**
 * Each tool's icon: one 16-unit glyph drawn in the button's text colour, so
 * the armed tool's accent fill and the hover colour reach it. The pen is a
 * stroke, the line a diagonal, the shapes themselves, text a T, and erase an
 * eraser block with its seam.
 */
const TOOL_ICON: Record<DrawTool, ReactElement> = {
  pen: <path d="M2 11c2-7 4-7 6-3s4 4 6-3" />,
  line: <path d="M3 13 13 3" />,
  rect: <rect x="2.5" y="3.5" width="11" height="9" />,
  ellipse: <ellipse cx="8" cy="8" rx="6" ry="4.5" />,
  text: <path d="M3 3.5h10M8 3.5v9.5M6 13h4" />,
  erase: <path d="M9.2 2.8l4 4-6.2 6.2H4.4L2.8 11.4zM6.4 5.6l4 4" />,
};
/** The panel toggle's icon: a pencil. */
const TOOLS_TOGGLE_ICON = <path d="M3 13l.8-3.2 7.4-7.4 2.4 2.4-7.4 7.4zM9.6 4l2.4 2.4" />;

/** One tool icon, sized by style.css (`.map-tool svg`). Hidden from assistive tech: the button carries the name. */
function ToolIcon({ children }: { children: ReactElement }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" focusable="false">
      {children}
    </svg>
  );
}
/**
 * Where a text line's baseline sits below the top of its line, in ems: the
 * half-leading of the editor's 1.25 line height plus the mono face's ascent,
 * so the committed text lands where the editor showed it.
 */
const TEXT_BASELINE_EM = 0.95;

/** To at most one decimal place. */
const round1 = (n: number) => Math.round(n * 10) / 10;
/** To at most two decimal places (a new drawing's size). */
const round2 = (n: number) => Math.round(n * 100) / 100;

const noOps = (): DrawOps => ({ set: {}, remove: [] });
const opsEmpty = (o: DrawOps) => o.remove.length === 0 && Object.keys(o.set).length === 0;
/** Queue a set: it replaces any queued op on the same id. */
function opSet(o: DrawOps, id: string, d: Drawing) {
  o.set[id] = d;
  if (o.remove.includes(id)) o.remove = o.remove.filter((r) => r !== id);
}
/** Queue a remove: any queued set of the id is dropped. */
function opRemove(o: DrawOps, id: string) {
  delete o.set[id];
  if (!o.remove.includes(id)) o.remove.push(id);
}
/** `a` then `b`, as one op set: a later op on an id wins. */
function mergeOps(a: DrawOps, b: DrawOps): DrawOps {
  const out: DrawOps = { set: { ...a.set }, remove: [...a.remove] };
  for (const id of b.remove) opRemove(out, id);
  for (const [id, d] of Object.entries(b.set)) opSet(out, id, d);
  return out;
}
/** The drawings `rec` holds once `ops` are applied: removes, then sets. */
function applyOps(ops: DrawOps, rec: Record<string, Drawing>): Record<string, Drawing> {
  const out = { ...rec };
  for (const id of ops.remove) delete out[id];
  for (const [id, d] of Object.entries(ops.set)) out[id] = d;
  return out;
}

/**
 * The geometry attributes of a pen, line, rect or ellipse element, world
 * units: the same for a saved drawing (rendered by React) and the draft
 * (written straight to its element as the pointer moves).
 */
function shapeGeom(kind: ShapeKind, p: readonly number[]): Record<string, string | number> {
  if (kind === "pen") return { d: penPath(p as number[]) };
  const [x1, y1, x2, y2] = p as [number, number, number, number];
  if (kind === "line") return { x1, y1, x2, y2 };
  if (kind === "rect") return { x: Math.min(x1, x2), y: Math.min(y1, y2), width: Math.abs(x2 - x1), height: Math.abs(y2 - y1) };
  return { cx: (x1 + x2) / 2, cy: (y1 + y2) / 2, rx: Math.abs(x2 - x1) / 2, ry: Math.abs(y2 - y1) / 2 };
}

/**
 * The zoom the card layer is LAID OUT at for a view zoom `z`. Below 1 the
 * layer stays laid out at 1 and is only scaled, so zooming out never lays a
 * card out again: no text re-wraps (a browser's minimum font size can force
 * that at a small CSS zoom) and no card's height changes. The operator saw
 * cards grow as the view zoomed out and push each other out of the
 * arrangement he had made. Above 1 the layer is laid out at the zoom, for
 * sharp text; what that does to a card's measured size is discounted in
 * `takeSize`.
 */
const layoutZoom = (z: number) => (ZOOM_OK ? Math.max(1, z) : 1);

/**
 * Where the saved positions stand. `waiting`: the GET has not answered, so no
 * card is placed — seeding now and saving the seeds would overwrite positions
 * the server is about to hand back. `provisional`: the GET is slow, so cards
 * are placed locally to show something, but nothing is SAVED until it answers.
 * `final`: the answer (or its failure) is in, and every change is saved.
 */
type LoadPhase = "waiting" | "provisional" | "final";

interface Entry {
  job: WebJob;
  col: Column;
  key: string;
  /**
   * The job's content as a string. The board parses a fresh payload every
   * poll, so object identity changes even when nothing did; comparing this is
   * what lets a poll re-render only the cards whose job actually changed.
   * JSON.stringify of one card's job is a few microseconds — far below the
   * cost of rendering one JobCard.
   */
  sig: string;
}

function readView(): ViewState | null {
  const raw = readLS(MAP_CAMERA_KEY);
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as Partial<ViewState>;
    if ([v.x, v.y, v.z].every((n) => typeof n === "number" && Number.isFinite(n))) {
      return { x: v.x!, y: v.y!, z: Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.z!)) };
    }
  } catch {
    // a malformed value is no saved view
  }
  return null;
}

/**
 * Positions the server has not accepted yet, kept in this browser so a
 * server that refuses saves (the read-only mirror answers every POST 403)
 * still shows every card where it was left after a reload. A key is written
 * when its position changes and removed when a save carrying that position
 * succeeds, so on a healthy server the record is empty almost always.
 */
const UNSAVED_KEY = "kraken.mapUnsaved";
/** Past this many keys the record is dropped (never the view): something wrote junk. */
const UNSAVED_MAX = 3000;
type PosRecord = Record<string, [number, number]>;

/**
 * The unsaved record, or `{}` when storage throws or holds anything
 * unparseable. A record past UNSAVED_MAX keys is junk: it is cleared from
 * storage as well as ignored.
 */
function readUnsaved(): PosRecord {
  const raw = readLS(UNSAVED_KEY);
  if (!raw) return {};
  try {
    const v: unknown = JSON.parse(raw);
    if (!v || typeof v !== "object" || Array.isArray(v)) return {};
    const out: PosRecord = {};
    let n = 0;
    for (const [k, xy] of Object.entries(v as Record<string, unknown>)) {
      if (!k || !Array.isArray(xy) || xy.length !== 2) continue;
      const [x, y] = xy as unknown[];
      if (typeof x !== "number" || typeof y !== "number" || !Number.isFinite(x) || !Number.isFinite(y)) continue;
      out[k] = [x, y];
      if (++n > UNSAVED_MAX) {
        writeLS(UNSAVED_KEY, null);
        return {};
      }
    }
    return out;
  } catch {
    return {};
  }
}

function writeUnsaved(rec: PosRecord) {
  const n = Object.keys(rec).length;
  // writeLS already swallows a storage that throws
  writeLS(UNSAVED_KEY, n === 0 || n > UNSAVED_MAX ? null : JSON.stringify(rec));
}

const finiteXY = (n: MapNode) => Number.isFinite(n.x) && Number.isFinite(n.y);
const num = (v: number | undefined) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
/** How often a card's characters redraw, given its lead's pose and whether anything on it is moving. */
function intervalFor(pose: string, busy: boolean, far: boolean): number {
  if (busy) return FAST_MS;
  if (SLOW_POSES.has(pose) || far) return SLOW_MS;
  return HELD_POSES.has(pose) ? HELD_MS : FAST_MS;
}

/** One message thread between two cards on the map, precomputed when the payload changes. */
interface TalkLink {
  a: string;
  b: string;
  n: number;
  lastMs: number;
}

/**
 * The key a spawn or talk peer's card has on this map. A peer link names a
 * slug in its OWN board's namespace, so a remote card's peers are that
 * owner's cards.
 */
function peerKeyFor(job: WebJob) {
  return (slug: string) => (job.owner ? `${job.owner.node_id}/${slug}` : slug);
}

/** A talk's `last_at` in millis, whichever unit the server stamped it in. */
function talkMillis(t: number): number {
  return t > 0 && t < 1e12 ? t * 1000 : t;
}

function sigOf(job: WebJob): string {
  try {
    return JSON.stringify(job);
  } catch {
    // a payload that cannot be stringified is never equal to anything
    return String(Math.random());
  }
}

export interface MapViewProps {
  search: JobSearchModel;
  actions: BoardActions;
  boardNodeID?: string;
  hidden?: boolean;
}

/**
 * The agent map: every card the board would show, laid out freely on a
 * pannable, zoomable plane, with each cell's lead and crew drawn standing on
 * its card and acting out what their transcripts say they are doing.
 *
 * Layers, back to front: the spawn arrows and talk threads on one canvas
 * beneath every card; the cards, each a real JobCard in a `.map-card` whose
 * inline z-index puts a card lower on the map in front of one higher up, and
 * each carrying its OWN characters' canvas, painted in front of that card's
 * face but inside its `.map-card`, so a card stacked in front covers both the
 * card behind and that card's characters; and a top canvas for the one thing
 * that has to cross cards, a message bubble flying between two cells.
 *
 * Positions are the server's (GET/POST /api/map/positions), so a card stays
 * where it was left in every browser. A card dragged before they arrive
 * keeps where it was dropped. A save that fails stays queued for the next
 * save; nothing retries on its own. Until the server accepts a position it is
 * also kept in this browser (`kraken.mapUnsaved`), which wins on the next
 * load, so a server that refuses saves still keeps the layout across a
 * reload. Cards never REST overlapping: `settle` runs to completion when a
 * drag ends (the dropped card held), when cards are first placed, and when a
 * showing card's size changes — never during a drag and never on an ordinary
 * frame — and a run that hits its step bound carries on over later frames.
 * Only cards on the map take part: a job filtered out reserves no space, and
 * one that comes back (or a new one) yields, moving to a free spot near
 * where it would have been while every card already showing stays put.
 *
 * Cards can be selected as a group. A press with Shift held that turns into
 * a drag (on the background or on a card) draws the marquee (`#map-marquee`)
 * instead of panning or moving anything, and its release selects exactly the
 * cards whose boxes it touches (`marqueeKeys`); a Shift click on a card
 * toggles that card. Dragging a selected card moves every selected card by
 * the same amount, and the drop holds them all (settle's set form) while the
 * other cards make way, in one save. A plain drag of an unselected card moves
 * it alone and empties the selection; an Escape nothing else handled (no
 * overlay closing, no text field), or a plain click on the background,
 * empties it too. The selection lives in this component only: it is never
 * saved, and a card that leaves the map leaves it (a group member that leaves
 * mid-drag goes back to where it stood at the press).
 *
 * Zooming never moves a card. Below a zoom of 1 the card layer stays laid
 * out at 1 and is only scaled (`layoutZoom`), so no card re-wraps or grows;
 * above 1 it is laid out at the zoom, and takeSize discounts the size change
 * that relayout reports, so physics knows every card by its size at zoom 1
 * and a zoom runs no settle and sends no save. A card's own text and chips
 * are laid out at style.css' `--map-text-scale` inside its 300px width.
 *
 * In the bottom-left corner, the zoom buttons sit above the overview
 * (`#map-minimap`): every card as a rectangle in its status colour and the
 * visible area as an outline, redrawn only when one of those changed. A
 * press or drag on it centres the view on the point under the pointer. The
 * `dinos` button beside the zoom buttons switches the characters off for
 * this browser (`MAP_ACTORS_KEY`, lib/agentmap/prefs.ts): no card then has a characters canvas and
 * the crossing-bubble layer is not rendered, so the frame loop draws
 * neither. A card's status still shows as its frame colour and as the square
 * beside its name (JobCard's `.map-status`). The `new job` button in the same
 * row opens the job editor in new-job mode, as the column board's "+ NEW
 * JOB" does; the draft it creates arrives on the map like any other card.
 *
 * Drawings (lib/agentmap/drawings.ts) are strokes, lines, rectangles, ovals
 * and text labels on the plane, one SVG element each in `svg#map-drawings`,
 * which paints above every card and below the corner and takes no pointer
 * events. Its one `<g>` carries the view as a transform that applyView
 * writes, so a drawing's geometry stays in world units and a pan or zoom
 * changes one attribute. The pencil button at the end of the zoom row opens
 * and closes a panel of icon buttons above it (`#map-tools`, open or closed
 * per browser under `MAP_TOOLS_KEY`, closed to begin with); the panel arms
 * one tool at a time, and closing it disarms the tool. While one is armed,
 * `#map-draw-surface` covers the
 * map above the cards and takes every press there, so a card cannot be
 * moved, opened or selected and the view does not pan (the wheel still
 * pans and zooms). A shape is a gesture like a card drag: its draft is
 * written straight to its element as the pointer moves, and the release adds
 * the drawing. The text tool opens a textarea at the press instead, which
 * applyView keeps on that world point through pans and zooms, and Enter, a
 * blur or the map's unmount commits it. Escape drops a draft in flight, else
 * disarms the tool, else empties the selection. Drawings save on their own
 * queue, independent of the positions': every change is an op (`set` or
 * `remove` by id), sent 400 ms after the last one with at most one POST in
 * flight, kept in this browser (`kraken.mapDrawUnsaved`) until a POST
 * carrying it succeeds, put back on a failure without a retry (a POST
 * unanswered after 20 s is aborted and is one), dropped on a refusal (400
 * or 413, which reloads the drawings), and sent once more on unmount. Neither waits for the other's load, and the drawings' load is
 * applied under every op made before it answered.
 *
 * The animation loop is built to be cheap on a board that is mostly asleep:
 * it reads no layout (sizes come from a ResizeObserver, the map's own size
 * from resize events, the palette from a theme change), allocates nothing in
 * its steady state, draws text from cached bitmaps, redraws each card's
 * characters at the rate its pose needs (30, 12 or 4 a second; see
 * `intervalFor`) with at most CARD_BUDGET redraws a frame for cards not in
 * motion, and redraws the arrow layer only when something it shows changed
 * or a pan or zoom has paused (while the view moves, the drawn layer is
 * moved with it).
 */
export function MapView({ search, actions, boardNodeID, hidden }: MapViewProps) {
  const entries = useMemo(() => {
    const out: Entry[] = [];
    for (const col of COLUMNS) {
      for (const job of search.byColumn[col]) {
        if (job.archived) continue;
        out.push({ job, col, key: jobKey(job), sig: sigOf(job) });
      }
    }
    return out;
  }, [search.byColumn]);

  // App rebuilds `actions` with every board payload (it closes over the
  // board), so its identity would defeat MapCard's memo on every poll. The
  // cards get a stable object that always calls the newest one.
  const actionsRef = useRef(actions);
  actionsRef.current = actions;
  const stableActions = useMemo<BoardActions>(
    () => ({
      onOpenSteer: (...a) => actionsRef.current.onOpenSteer(...a),
      onOpenEditor: (...a) => actionsRef.current.onOpenEditor(...a),
      onOpenArtifact: (...a) => actionsRef.current.onOpenArtifact(...a),
      onOpenThread: (...a) => actionsRef.current.onOpenThread(...a),
      onAnswerQuestion: (...a) => actionsRef.current.onAnswerQuestion(...a),
      onRetry: (...a) => actionsRef.current.onRetry(...a),
      onOpenTmux: (...a) => actionsRef.current.onOpenTmux(...a),
      onSaveFeedback: (...a) => actionsRef.current.onSaveFeedback(...a),
      onDeleteFeedback: (...a) => actionsRef.current.onDeleteFeedback(...a),
      onUnarchive: (...a) => actionsRef.current.onUnarchive(...a),
      onDeleteForever: (...a) => actionsRef.current.onDeleteForever(...a),
    }),
    [],
  );

  const rootRef = useRef<HTMLElement | null>(null);
  const worldRef = useRef<HTMLDivElement | null>(null);
  const zoomRef = useRef<HTMLDivElement | null>(null);
  const underRef = useRef<HTMLCanvasElement | null>(null);
  const overRef = useRef<HTMLCanvasElement | null>(null);

  const nodes = useRef(new Map<string, MapNode>()).current;
  const measured = useRef(new Set<string>()).current;
  const scenes = useRef(new Map<string, CardScene>()).current;
  const synced = useRef(new Map<string, string>()).current;
  const lastDraw = useRef(new Map<string, number>()).current;
  const cardEls = useRef(new Map<string, HTMLDivElement>()).current;
  const canvasEls = useRef(new Map<string, HTMLCanvasElement>()).current;
  const dirty = useRef(new Set<string>()).current;
  const entriesRef = useRef(entries);
  entriesRef.current = entries;

  const miniRef = useRef<HTMLCanvasElement | null>(null);

  const viewRef = useRef<ViewState>(readView() ?? { x: 40, y: 160, z: 1 });
  /**
   * The zoom the card layer is LAID OUT at (CSS zoom): `layoutZoom` of the
   * last committed zoom, so never below 1. The view zoom may differ from it
   * (always below 1, and mid-gesture above); the difference is a scale
   * transform.
   */
  const committedZ = useRef(layoutZoom(viewRef.current.z));
  /** The view zoom as of the last commit: what the card canvases are backed for. */
  const backZ = useRef(viewRef.current.z);
  /** When the view last changed (performance.now millis), for the arrow layer's pause. */
  const viewAt = useRef(-Infinity);
  /** The view and layout zoom applyView last saw, so it can tell a real change from a re-application. */
  const appliedView = useRef({ x: NaN, y: NaN, z: NaN, lz: NaN });
  /** The overview needs drawing again: a card's place, size or status, the cards, the view or the theme changed. */
  const miniDirty = useRef(true);
  /**
   * Per card: the layout zoom it was last measured at, and the height it
   * measured then, in the layer's own units. A measurement taken at a
   * different layout zoom from the last is the zoom's doing, not the
   * content's (see takeSize).
   */
  const measuredAtZ = useRef(new Map<string, number>()).current;
  const rawH = useRef(new Map<string, number>()).current;
  /** The map's own size and page offset, from the resize handler (never read per frame). */
  const sizeRef = useRef({ w: 0, h: 0, left: 0, top: 0 });
  const fitPending = useRef(readView() === null);
  const dragRef = useRef<Drag | null>(null);
  /** This mount's input state: whether it has seen a pointer event (see gestureEventFor). */
  const pointerSeen = useRef(false);
  /** Whether a gesture event should be handled at all, by this mount's rule. */
  const gestureEvent = useCallback((ev: { type: string; clientX?: number }) => gestureEventFor(pointerSeen, ev), []);
  const suppressClick = useRef(false);
  const phase = useRef<LoadPhase>("waiting");
  const crossRef = useRef<CrossMsg[]>([]);
  const saveTimer = useRef<number | null>(null);
  const zoomTimer = useRef<number | null>(null);
  const persistTimer = useRef<number | null>(null);
  const settleWanted = useRef(false);
  const underDirty = useRef(true);
  const roRef = useRef<ResizeObserver | null>(null);
  const aliveRef = useRef(true);
  const [, setVersion] = useState(0);
  const rerender = useCallback(() => setVersion((v) => v + 1), []);
  // Whether the characters are drawn. Off unmounts every card's characters
  // canvas and the crossing-bubble canvas, which is all the frame loop needs
  // to skip them.
  const [actorsOn, setActorsOn] = useState(actorsShown);
  const toggleActors = () => {
    const next = !actorsOn;
    setActorsShown(next);
    // the canvases that mount when the characters come back draw at once
    lastDraw.clear();
    setActorsOn(next);
  };
  /**
   * The selected cards' keys: component state only, never saved or sent, and
   * empty at mount. Held in a ref so the window's pointer handlers read the
   * current set; every change goes through setSelection, which re-renders so
   * each card's `selected` prop follows. Selecting moves nothing and saves
   * nothing.
   */
  const selRef = useRef<ReadonlySet<string>>(new Set());
  const setSelection = useCallback(
    (next: ReadonlySet<string>) => {
      const cur = selRef.current;
      if (cur.size === next.size && [...next].every((k) => cur.has(k))) return;
      selRef.current = next;
      rerender();
    },
    [rerender],
  );
  // ---- drawings (see the header). The shown set is replaced, never
  // mutated, on every change, so the memoized layer re-renders only then.
  /** The armed tool, or none; never saved. Mirrored in a ref for the window's handlers. */
  const [tool, setToolState] = useState<DrawTool | null>(null);
  const toolRef = useRef<DrawTool | null>(null);
  /**
   * Whether the tool panel is open: a per-browser preference (`MAP_TOOLS_KEY`,
   * lib/agentmap/prefs.ts), closed until the operator opens it. Only an open
   * panel renders the tool buttons, so closing it disarms the tool too.
   */
  const [toolsOpen, setToolsOpen] = useState(toolsShown);
  /** Ops restored from this browser at mount: shown at once, queued again, and applied over the load. */
  const restoredDraw = useRef<DrawOps | null>(null);
  if (restoredDraw.current === null) restoredDraw.current = readUnsavedDrawOps();
  const [drawings, setDrawings] = useState<Record<string, Drawing>>(() => applyOps(restoredDraw.current!, {}));
  const drawRef = useRef(drawings);
  /** Ops not yet sent. */
  const drawPending = useRef<DrawOps>(mergeOps(noOps(), restoredDraw.current));
  /** The ops the one POST in flight carries, or null when none is. */
  const drawInflight = useRef<DrawOps | null>(null);
  /** A save came due (or the map unmounted) while a POST was in flight: send when it answers. */
  const drawDue = useRef(false);
  /** Every op made since mount, until the load answers and is applied under them; then null. */
  const drawEarly = useRef<DrawOps | null>(mergeOps(noOps(), restoredDraw.current));
  /** Which load may still apply its answer: a refusal's reload supersedes the one before it. */
  const drawLoadSeq = useRef(0);
  const drawTimer = useRef<number | null>(null);
  const drawAlive = useRef(true);
  /** The draft shape while a draw press is held: the element mounts with it, moves write to it. */
  const [draft, setDraft] = useState<{ kind: ShapeKind; size: number; color?: DrawColor } | null>(null);
  /**
   * The ink new drawings take: a per-browser preference (`MAP_INK_KEY`), null
   * for the theme's foreground. Mirrored in a ref for the gesture handlers.
   */
  const [ink, setInkState] = useState<DrawColor | null>(inkChosen);
  const inkRef = useRef(ink);
  const chooseInk = (c: DrawColor | null) => {
    inkRef.current = c;
    setInkChosen(c);
    setInkState(c);
  };
  const draftEl = useRef<SVGElement | null>(null);
  const drawG = useRef<SVGGElement | null>(null);
  /** The drawing the eraser is over (class `erase-hit`), written straight to its element. */
  const eraseHit = useRef<string | null>(null);
  const [editor, setEditor] = useState<EditorAt | null>(null);
  const editorRef = useRef<EditorAt | null>(null);
  const editorInput = useRef<HTMLTextAreaElement | null>(null);
  /** The open editor's text as of its last change: what an unmount commits once the textarea is detached. */
  const editorText = useRef("");
  const editorSeq = useRef(0);
  /** commitOpenEditor, for the drawings effect's cleanup (declared before the function itself). */
  const commitOpenEditorRef = useRef<() => void>(() => {});

  /** The marquee: shown while a shift-drag is in flight; its rectangle is written straight to the element. */
  const [marqueeOn, setMarqueeOn] = useState(false);
  const marqueeRef = useRef<HTMLDivElement | null>(null);
  /** The marquee's rectangle in the map's own pixels (left, top, width, height). */
  const marqueeBox = useRef<[number, number, number, number]>([0, 0, 0, 0]);
  /** Cards the marquee would select if released now: they wear `marquee-hit` until it is. */
  const marqueeHits = useRef(new Set<string>()).current;
  /** The marquee element mounts at the rectangle the first move already computed. */
  const setMarqueeEl = useCallback((el: HTMLDivElement | null) => {
    marqueeRef.current = el;
    if (!el) return;
    const [l, t, w, h] = marqueeBox.current;
    el.style.left = `${l}px`;
    el.style.top = `${t}px`;
    el.style.width = `${w}px`;
    el.style.height = `${h}px`;
  }, []);

  // ---- nodes, kept in step with the cards on screen. Mutating refs during
  // render is idempotent here (a second StrictMode pass finds nothing to do).
  for (const { key } of entries) {
    if (!nodes.has(key)) nodes.set(key, { key, x: NaN, y: NaN, w: CARD_W, h: EST_H });
  }
  const shown = entries.map((e) => nodes.get(e.key)!);
  const spawnLinks = useMemo(() => {
    const out: Array<readonly [string, string]> = [];
    const seen = new Set<string>();
    const keys = new Set(entries.map((e) => e.key));
    const add = (a: string, b: string) => {
      if (a === b || !keys.has(a) || !keys.has(b) || seen.has(a + ">" + b)) return;
      seen.add(a + ">" + b);
      out.push([a, b] as const);
    };
    for (const { job, key } of entries) {
      const peer = peerKeyFor(job);
      for (const ch of job.spawned ?? []) add(key, peer(ch.slug));
      if (job.spawned_by) add(peer(job.spawned_by.slug), key);
    }
    return out;
  }, [entries]);
  const linksRef = useRef(spawnLinks);
  linksRef.current = spawnLinks;

  // What the frame needs from the payload, computed once per payload rather
  // than rebuilt by every frame: the entries by key, and one TalkLink per
  // pair of cards on the map that have messaged each other.
  const byKey = useMemo(() => new Map(entries.map((e) => [e.key, e])), [entries]);
  const talkLinks = useMemo(() => {
    const out: TalkLink[] = [];
    const seen = new Set<string>();
    for (const e of entries) {
      const peer = peerKeyFor(e.job);
      for (const t of e.job.talks ?? []) {
        const other = peer(t.slug);
        if (!byKey.has(other) || other === e.key) continue;
        const pair = e.key < other ? e.key + "|" + other : other + "|" + e.key;
        if (seen.has(pair)) continue;
        seen.add(pair);
        out.push({ a: e.key, b: other, n: t.sent + t.received, lastMs: talkMillis(t.last_at) });
      }
    }
    return out;
  }, [entries, byKey]);
  const byKeyRef = useRef(byKey);
  byKeyRef.current = byKey;
  // A card that left the map (filtered out, archived) leaves the selection,
  // so it is not selected again if it comes back. Idempotent during render.
  if ([...selRef.current].some((k) => !byKey.has(k))) {
    selRef.current = new Set([...selRef.current].filter((k) => byKey.has(k)));
  }
  const talkRef = useRef(talkLinks);
  talkRef.current = talkLinks;
  /**
   * Cards the operator dragged before the saved positions arrived. The late
   * load leaves these where they were dropped, and they stay queued to save.
   */
  const draggedEarly = useRef(new Set<string>()).current;
  /** A settle that hit its step bound with overlaps left: continue on a later frame, with this card held. */
  const settleMore = useRef<{ held: string | undefined } | null>(null);
  /** The unsaved record (see UNSAVED_KEY), read once at mount and kept in step with storage. */
  const unsaved = useRef<PosRecord | null>(null);
  if (unsaved.current === null) unsaved.current = readUnsaved();
  /**
   * The cards on the map as of the last commit, once cards have first been
   * placed (null until then), and the cards that came onto it since and have
   * not yet been given a free spot. An arriving card yields: it never moves
   * a card that was already showing.
   */
  const showing = useRef<Set<string> | null>(null);
  const arriving = useRef(new Set<string>()).current;
  const firstPlacement = showing.current === null;
  /**
   * Cards seeded on the middle of the view and not measured yet. A card is
   * seeded with an estimated height; when its real one arrives it is moved by
   * half the difference, so its middle stays where it was put.
   */
  const centredSeeds = useRef(new Set<string>()).current;

  // Seed every unplaced card once the saved positions are in (or the load is
  // slow enough to place provisionally). The seeds are saved by the effect
  // below, once saving is allowed.
  const seededNow: string[] = [];
  if (phase.current !== "waiting" && shown.some((n) => !finiteXY(n))) {
    const before = new Set(shown.filter(finiteXY).map((n) => n.key));
    // A card with no position that turns up after the first placement is a
    // job that was just added: it starts in the middle of what the operator
    // is looking at (the nearest free spot to it), not at the middle of the
    // whole layout, which may be off screen. The first placement, and a map
    // whose size is not known yet, keep the layout's own centre.
    let origin: readonly [number, number] | undefined;
    const size = sizeRef.current;
    const view = viewRef.current;
    if (!firstPlacement && size.w > 0 && size.h > 0 && view.z > 0) {
      origin = [(size.w / 2 - view.x) / view.z, (size.h / 2 - view.y) / view.z];
    }
    seedLayout(shown, spawnLinks, origin);
    for (const n of shown) {
      if (before.has(n.key)) continue;
      seededNow.push(n.key);
      // a card seeded beside its spawn partner was not put on the origin
      if (origin && !spawnLinks.some(([a, b]) => a === n.key || b === n.key)) centredSeeds.add(n.key);
    }
  }

  /**
   * Write the given cards' positions to the DOM, then every card's z-index
   * from stackOrder (only where it changed). Writes only: nothing here reads
   * layout.
   */
  const writePositions = useCallback(
    (keys: Iterable<string>, glide = false) => {
      for (const key of keys) {
        const n = nodes.get(key);
        const el = cardEls.get(key);
        if (!n || !el || !finiteXY(n)) continue;
        if (glide) {
          el.classList.add("glide");
          window.setTimeout(() => el.classList.remove("glide"), GLIDE_MS + 40);
        }
        el.style.left = `${n.x}px`;
        el.style.top = `${n.y}px`;
      }
      const placedNodes = entriesRef.current
        .map((e) => nodes.get(e.key))
        .filter((n): n is MapNode => !!n && finiteXY(n));
      const z = stackOrder(placedNodes);
      for (const n of placedNodes) {
        const el = cardEls.get(n.key);
        const zi = String(z.get(n.key) ?? 1);
        if (el && el.style.zIndex !== zi) el.style.zIndex = zi;
      }
      underDirty.current = true;
      miniDirty.current = true;
    },
    [nodes, cardEls],
  );

  /**
   * Queue cards' positions to save, and record them in this browser until a
   * save carrying them succeeds. Every position change the view makes goes
   * through here.
   */
  const markDirty = useCallback(
    (keys: Iterable<string>) => {
      const rec = unsaved.current!;
      let wrote = false;
      for (const key of keys) {
        dirty.add(key);
        const n = nodes.get(key);
        if (!n || !finiteXY(n)) continue;
        // before the saved positions are in, only a drag is the operator's:
        // a provisional seed or push must not outlive a reload and then beat
        // the server's position for that card
        if (phase.current !== "final" && !draggedEarly.has(key)) continue;
        rec[key] = [Math.round(n.x), Math.round(n.y)];
        wrote = true;
      }
      if (wrote) writeUnsaved(rec);
    },
    [dirty, nodes, draggedEarly],
  );

  /**
   * A save succeeded: drop each key it carried from the unsaved record —
   * unless the card has moved again since, in which case the record holds
   * the newer position and keeps it.
   */
  const forgetSaved = useCallback((changes: PosRecord) => {
    const rec = unsaved.current!;
    let wrote = false;
    for (const [key, xy] of Object.entries(changes)) {
      const r = rec[key];
      if (r && r[0] === xy[0] && r[1] === xy[1]) {
        delete rec[key];
        wrote = true;
      }
    }
    if (wrote) writeUnsaved(rec);
  }, []);

  /**
   * The unmount flush's send: one POST, not retried. A success clears the
   * keys from the unsaved record; a failure leaves them there, so the next
   * load in this browser still shows the cards where they were and queues
   * them again.
   */
  const sendFinal = useCallback(
    (changes: PosRecord) => {
      if (!Object.keys(changes).length) return;
      void saveMapPositions(changes).then(
        () => forgetSaved(changes),
        () => {},
      );
    },
    [forgetSaved],
  );

  const flush = useCallback(() => {
    saveTimer.current = null;
    if (phase.current !== "final" || !aliveRef.current) return;
    if (dragRef.current?.kind === "card") {
      // the drop schedules its own save
      return;
    }
    const changes: Record<string, [number, number]> = {};
    let n = 0;
    for (const key of dirty) {
      const node = nodes.get(key);
      if (!node || !finiteXY(node)) continue;
      node.x = Math.round(node.x);
      node.y = Math.round(node.y);
      changes[key] = [node.x, node.y];
      n++;
    }
    // what is on screen is what was saved, to the pixel
    writePositions(Object.keys(changes));
    dirty.clear();
    if (!n) return;
    // A failed save leaves the cards where they are on screen and puts their
    // keys back in the queue, so the next save (the next change, or the
    // unmount flush) carries them. Nothing retries on its own: a server that
    // refuses would otherwise be asked again forever.
    void saveMapPositions(changes).then(
      () => forgetSaved(changes),
      () => {
        // the view already left: its unmount flush has run, so this failure
        // goes out once more as that flush's own send would have
        if (!aliveRef.current) sendFinal(changes);
        else for (const key of Object.keys(changes)) dirty.add(key);
      },
    );
  }, [dirty, nodes, writePositions, forgetSaved, sendFinal]);

  const scheduleSave = useCallback(() => {
    if (saveTimer.current !== null) window.clearTimeout(saveTimer.current);
    saveTimer.current = window.setTimeout(flush, SAVE_DELAY_MS);
  }, [flush]);

  /**
   * Run settle until nothing overlaps (bounded), apply the result once, and
   * save what moved. `held` is the card just dropped: it stays where the
   * operator put it and the cards under it make way. A run that reaches its
   * step bound with overlaps left (a pile of cards saved on one point) asks
   * for another run on a later frame, so the pile still comes apart without
   * one frame paying for all of it.
   *
   * `held` may also be the set of cards a group drag just dropped: all of
   * them hold, so they keep the offsets they were dragged with (settle leaves
   * a pair of held cards alone) and only the unselected cards make way. An
   * unselected card can be caught between two held ones and pushed back and
   * forth, so a group run that reaches its step bound does not carry on over
   * later frames: each card still overlapping is moved to the nearest free
   * spot instead (freeSpot, the arriving card's way out), in this same run,
   * so the group's drop is one save.
   */
  const settleToRest = useCallback(
    (held?: string | ReadonlySet<string>) => {
      settleMore.current = null;
      const cur = entriesRef.current
        .map((e) => nodes.get(e.key))
        .filter((n): n is MapNode => !!n && finiteXY(n));
      if (cur.length < 2) return;
      const before = new Map(cur.map((n) => [n.key, [n.x, n.y] as const]));
      let still = true;
      for (let i = 0; i < SETTLE_STEPS && still; i++) still = settle(cur, held);
      if (still) {
        if (typeof held === "object") {
          for (const n of cur) {
            if (held.has(n.key)) continue;
            if (cur.some((o) => o !== n && overlaps(n, o))) freeSpot(n, cur);
          }
        } else settleMore.current = { held };
      }
      const moved: string[] = [];
      for (const n of cur) {
        const b = before.get(n.key)!;
        if (n.x !== b[0] || n.y !== b[1]) moved.push(n.key);
      }
      if (!moved.length) return;
      markDirty(moved);
      writePositions(moved, true);
      scheduleSave();
    },
    [nodes, markDirty, writePositions, scheduleSave],
  );

  /**
   * Give every arriving card a free spot without moving any card that was
   * already showing. The cards already showing are held (settle's set form),
   * so physics moves only the arrivals; one still wedged between held cards
   * when the step bound runs out is placed at the nearest free spot by
   * search instead. Whatever moved is saved.
   */
  const placeArrivals = useCallback(() => {
    const cur = entriesRef.current
      .map((e) => nodes.get(e.key))
      .filter((n): n is MapNode => !!n && finiteXY(n));
    const held = new Set<string>();
    for (const n of cur) if (!arriving.has(n.key)) held.add(n.key);
    const before = new Map<string, readonly [number, number]>();
    for (const n of cur) if (arriving.has(n.key)) before.set(n.key, [n.x, n.y]);
    arriving.clear();
    if (!before.size) return;
    let still = true;
    for (let i = 0; i < SETTLE_STEPS && still; i++) still = settle(cur, held);
    const moved: string[] = [];
    for (const n of cur) {
      const b = before.get(n.key);
      if (!b) continue;
      if (cur.some((o) => o !== n && overlaps(n, o))) freeSpot(n, cur);
      if (n.x !== b[0] || n.y !== b[1]) moved.push(n.key);
    }
    if (!moved.length) return;
    markDirty(moved);
    writePositions(moved, true);
    scheduleSave();
  }, [nodes, arriving, markDirty, writePositions, scheduleSave]);

  /** Whether every card on the map has a real (observed) size. */
  const allMeasured = () => entriesRef.current.every((e) => measured.has(e.key));
  /** Whether every arriving card has a real size yet. */
  const arrivalsMeasured = () => {
    for (const k of arriving) if (!measured.has(k)) return false;
    return true;
  };

  /**
   * Record a card's measured size; a change asks for a settle on the next
   * frame. Physics knows a card by its size at zoom 1, so a measurement the
   * ZOOM changed is never a size change:
   *  - at a view zoom of 1 or less the layer is laid out at 1 (layoutZoom),
   *    so zooming there measures nothing new at all;
   *  - above 1, the first measurement after the layout zoom changed (every
   *    card is re-observed on that commit, so each reports once) only
   *    re-baselines the card; a later change at the same layout zoom is the
   *    content's, and moves the card's zoom-1 height by the same amount;
   *  - back at layout zoom 1, that first measurement is adopted as the
   *    zoom-1 height, and one more than 2px off the stored height counts as
   *    the content change it is.
   * A browser that reports the box zoomed in `offset*` (its width reads as
   * the card's width times the zoom) is brought back to the layer's units
   * first.
   */
  const takeSize = useCallback(
    (key: string, el: HTMLElement) => {
      const n = nodes.get(key);
      if (!n) return;
      const lz = committedZ.current;
      let h = el.offsetHeight;
      let w = el.offsetWidth;
      if (!(h > 0)) return;
      if (lz !== 1 && w > 0 && Math.abs(w - CARD_W * lz) < 1) {
        h /= lz;
        w /= lz;
      }
      const first = !measured.has(key);
      const zoomOnly = !first && measuredAtZ.get(key) !== lz;
      measuredAtZ.set(key, lz);
      const before = rawH.get(key);
      rawH.set(key, h);
      let nh: number;
      let changed: boolean;
      if (zoomOnly) {
        // Above 1 a zoom-only report re-baselines and changes nothing. Back
        // at layout zoom 1 the measurement IS the zoom-1 height, so it is
        // adopted: whatever the deltas taken above 1 got wrong (a content
        // change in the same pass as a commit, a pixel of rounding each) is
        // corrected here. A difference past rounding is a content change
        // and is treated as one; a zoom alone leaves the height as it was,
        // so it still settles and saves nothing.
        if (lz !== 1) return;
        nh = h;
        changed = Math.abs(n.h - h) > 2;
        if (!changed) {
          n.h = h;
          return;
        }
      } else {
        nh = first || before === undefined ? h : n.h + (h - before);
        changed = first || Math.abs(n.h - nh) > 0.5 || (w > 0 && Math.abs(n.w - w) > 0.5);
      }
      measured.add(key);
      // a card seeded on the middle of the view keeps its middle there once
      // its real height replaces the estimate it was seeded with
      const recentre = first && centredSeeds.delete(key) && Number.isFinite(n.y);
      if (recentre) n.y = Math.round(n.y + (n.h - nh) / 2);
      n.h = nh;
      if (w > 0) n.w = w;
      if (recentre) {
        markDirty([key]);
        writePositions([key]);
        scheduleSave();
      }
      if (changed) {
        // an arriving card's size is part of its arrival (it yields when it
        // is placed); only a showing card's change moves others
        if (!arriving.has(key)) settleWanted.current = true;
        underDirty.current = true;
        miniDirty.current = true;
        lastDraw.delete(key);
      }
    },
    [nodes, measured, lastDraw, arriving, measuredAtZ, rawH, centredSeeds, markDirty, writePositions, scheduleSave],
  );

  // One ResizeObserver for every card. Its callback runs after layout, so the
  // offset reads inside it cost nothing extra; the animation frame itself
  // never reads a size.
  useEffect(() => {
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((list) => {
      for (const r of list) {
        const el = r.target as HTMLElement;
        const key = el.dataset.key;
        if (key) takeSize(key, el);
      }
    });
    roRef.current = ro;
    for (const el of cardEls.values()) ro.observe(el);
    return () => {
      ro.disconnect();
      roRef.current = null;
    };
  }, [cardEls, takeSize]);

  // Without a ResizeObserver: one measure after a render for cards not yet
  // measured (jsdom measures 0, which reads as "not measured").
  useLayoutEffect(() => {
    if (typeof ResizeObserver !== "undefined") return;
    for (const e of entries) {
      if (measured.has(e.key)) continue;
      const el = cardEls.get(e.key);
      if (el) takeSize(e.key, el);
    }
  });

  const observe = useCallback((el: HTMLDivElement | null, prev: HTMLDivElement | undefined) => {
    const ro = roRef.current;
    if (!ro) return;
    if (prev && prev !== el) ro.unobserve(prev);
    if (el) ro.observe(el);
  }, []);

  // Which cards came onto the map since the last commit. Runs before the
  // effects below and before the ResizeObserver reports the new cards, so an
  // arrival is known as one by the time its size arrives. A card that left
  // drops out of `showing`, so it reserves no space and never counts as
  // already showing when it comes back.
  useLayoutEffect(() => {
    if (phase.current === "waiting") return;
    const keys = new Set(entries.map((e) => e.key));
    const prev = showing.current;
    if (prev) for (const k of keys) if (!prev.has(k)) arriving.add(k);
    for (const k of arriving) if (!keys.has(k)) arriving.delete(k);
    for (const k of centredSeeds) if (!keys.has(k)) centredSeeds.delete(k);
    showing.current = keys;
  });

  useEffect(() => {
    if (!seededNow.length) return;
    markDirty(seededNow);
    // the first placement after mount: settle everything once the sizes are
    // real (the observer asks for it when they arrive). A card seeded later
    // (a job created) is an arrival and yields instead.
    if (firstPlacement) settleWanted.current = true;
    underDirty.current = true;
    miniDirty.current = true;
    scheduleSave();
  });

  // ---- scenes follow the payload: only a job whose content changed is
  // re-synced, and the arrow layer is redrawn when any did
  useEffect(() => {
    const now = performance.now() / 1000;
    const keysNow = new Set(entries.map((e) => e.key));
    let changed = false;
    for (const e of entries) {
      let s = scenes.get(e.key);
      if (!s) {
        s = newScene(e.key, now);
        scenes.set(e.key, s);
      }
      if (synced.get(e.key) === e.sig) continue;
      synced.set(e.key, e.sig);
      changed = true;
      lastDraw.delete(e.key);
      crossRef.current.push(
        ...syncScene(s, e.job, now, peerKeyFor(e.job), (k) => nodes.has(k) && keysNow.has(k)),
      );
    }
    if (changed) underDirty.current = true;
  }, [entries, scenes, synced, nodes, lastDraw]);

  // ---- the overview's inputs from the payload: each card's status, and a
  // signature of the cards and their statuses, so a poll that changed
  // neither leaves the overview alone
  const miniStatus = useMemo(() => new Map(entries.map((e) => [e.key, mapStatus(e.job) as string])), [entries]);
  const miniSig = useMemo(() => {
    let s = "";
    for (const [k, st] of miniStatus) s += k + "\u0000" + st + "\u0001";
    return s;
  }, [miniStatus]);
  const miniStatusRef = useRef(miniStatus);
  miniStatusRef.current = miniStatus;
  useEffect(() => {
    miniDirty.current = true;
  }, [miniSig]);

  // ---- the saved positions
  useEffect(() => {
    aliveRef.current = true;
    const ac = new AbortController();
    const timer = window.setTimeout(() => {
      if (phase.current !== "waiting") return;
      phase.current = "provisional";
      rerender();
    }, LOAD_TIMEOUT_MS);
    const settleLoad = (saved: Record<string, [number, number]>) => {
      window.clearTimeout(timer);
      if (!aliveRef.current || phase.current === "final") return;
      for (const [key, xy] of Object.entries(saved)) {
        if (!Array.isArray(xy) || !Number.isFinite(xy[0]) || !Number.isFinite(xy[1])) continue;
        // a card dragged while the load was slow keeps where it was dropped,
        // and stays queued: the drag is newer than anything the server holds
        if (draggedEarly.has(key)) continue;
        const node = nodes.get(key);
        if (node) {
          node.x = xy[0];
          node.y = xy[1];
        } else {
          nodes.set(key, { key, x: xy[0], y: xy[1], w: CARD_W, h: EST_H });
        }
        // a provisional seed for a card the server had a place for is not a
        // change to send back
        dirty.delete(key);
      }
      // then this browser's unsaved positions, which win over the server's
      // key by key (it never accepted them) and are queued again; a card
      // dragged during the slow load still wins over both
      const rec = unsaved.current!;
      for (const [key, xy] of Object.entries(rec)) {
        if (draggedEarly.has(key)) continue;
        const node = nodes.get(key);
        if (node) {
          node.x = xy[0];
          node.y = xy[1];
        } else {
          nodes.set(key, { key, x: xy[0], y: xy[1], w: CARD_W, h: EST_H });
        }
        dirty.add(key);
      }
      markDirty(draggedEarly);
      draggedEarly.clear();
      phase.current = "final";
      // everything still queued is now a position the server has not
      // accepted: record it, so a refusing server still keeps it
      markDirty([...dirty]);
      settleWanted.current = true;
      underDirty.current = true;
      miniDirty.current = true;
      rerender();
      scheduleSave();
    };
    fetchMapPositions(ac.signal).then(
      (p) => settleLoad(p && typeof p === "object" ? p : {}),
      () => settleLoad({}),
    );
    return () => {
      aliveRef.current = false;
      ac.abort();
      window.clearTimeout(timer);
      // Leaving the map sends whatever is queued rather than dropping it:
      // a pending debounced save, AND the keys a failed save put back, which
      // have no timer of their own (a failure schedules nothing, so there is
      // no retry loop). So the flush must not be gated on a pending timer: a
      // failed save's keys have none.
      if (saveTimer.current !== null) {
        window.clearTimeout(saveTimer.current);
        saveTimer.current = null;
      }
      if (phase.current === "final" && dirty.size) {
        const changes: Record<string, [number, number]> = {};
        for (const key of dirty) {
          const node = nodes.get(key);
          if (node && finiteXY(node)) changes[key] = [Math.round(node.x), Math.round(node.y)];
        }
        dirty.clear();
        sendFinal(changes);
      }
    };
  }, [nodes, dirty, draggedEarly, rerender, scheduleSave, markDirty, sendFinal]);

  // ---- the saved drawings: their own queue, independent of the positions'

  /** Every op not yet carried to success by a POST: the one in flight, then the pending ones. */
  const queuedDrawOps = useCallback(() => mergeOps(drawInflight.current ?? noOps(), drawPending.current), []);
  /** This browser's record is exactly the queued ops. */
  const persistDrawOps = useCallback(() => writeUnsavedDrawOps(queuedDrawOps()), [queuedDrawOps]);

  /**
   * Ask for the saved drawings again (after a refusal) and show the answer
   * with every queued op applied over it. It supersedes any load still
   * waiting, the mount's included, whose ops-since-mount record would
   * otherwise bring the refused ops back.
   */
  const reloadDrawings = useCallback(() => {
    const seq = ++drawLoadSeq.current;
    drawEarly.current = null;
    fetchMapDrawings().then(
      (d) => {
        if (!drawAlive.current || seq !== drawLoadSeq.current) return;
        drawRef.current = applyOps(queuedDrawOps(), sanitizeDrawings(d));
        setDrawings(drawRef.current);
      },
      () => {},
    );
  }, [queuedDrawOps]);

  /**
   * Send everything pending in one POST; at most one is in flight. A flush
   * while one is (the save timer, or the unmount) is remembered and runs
   * when it answers, carrying every op queued by then, so an op never
   * overtakes an older one on the same id. A failure (network, 5xx, 403)
   * puts the ops back under every op made since, so a later erase still
   * wins, and sends nothing by itself: the next change or the unmount
   * carries them. A refusal (400 or 413) can never pass: its ops are dropped
   * and the drawings reloaded, while ops queued after it save as usual. A
   * POST not answered within DRAW_SAVE_TIMEOUT_MS is aborted, which is a
   * failure like any other, so a hung request holds later saves for at most
   * that long. Its timer belongs to the POST, not the mount: an unmount
   * leaves it running, since the unmount's remaining ops go out only when
   * this POST answers.
   */
  const flushDraw = useCallback(() => {
    drawTimer.current = null;
    if (drawInflight.current) {
      drawDue.current = true;
      return;
    }
    const ops = drawPending.current;
    if (opsEmpty(ops)) return;
    drawPending.current = noOps();
    drawInflight.current = ops;
    const answered = () => {
      if (!drawDue.current) return;
      drawDue.current = false;
      flushDraw();
    };
    const ac = new AbortController();
    const giveUp = window.setTimeout(() => ac.abort(), DRAW_SAVE_TIMEOUT_MS);
    void saveMapDrawings(ops, ac.signal).then(
      () => {
        window.clearTimeout(giveUp);
        drawInflight.current = null;
        persistDrawOps();
        answered();
      },
      (err: unknown) => {
        window.clearTimeout(giveUp);
        drawInflight.current = null;
        const refused = err instanceof ApiError && (err.status === 400 || err.status === 413);
        if (!refused) drawPending.current = mergeOps(ops, drawPending.current);
        persistDrawOps();
        if (refused && drawAlive.current) reloadDrawings();
        answered();
      },
    );
  }, [persistDrawOps, reloadDrawings]);

  const scheduleDrawSave = useCallback(() => {
    if (drawTimer.current !== null) window.clearTimeout(drawTimer.current);
    drawTimer.current = window.setTimeout(() => flushDraw(), SAVE_DELAY_MS);
  }, [flushDraw]);

  /** Apply one op to what is shown and to every queue that tracks it, then save it later. */
  const changeDrawings = useCallback(
    (op: (o: DrawOps) => void, next: Record<string, Drawing>) => {
      drawRef.current = next;
      setDrawings(next);
      op(drawPending.current);
      if (drawEarly.current) op(drawEarly.current);
      persistDrawOps();
      scheduleDrawSave();
    },
    [persistDrawOps, scheduleDrawSave],
  );
  const addDrawing = useCallback(
    (d: Drawing) => {
      // a drawing the save route would refuse is never queued: it would hold
      // every later op back with it. That covers a full map too (the route
      // refuses a result past MAX_DRAWINGS); erasing still works there.
      if (!validDrawing(d) || Object.keys(drawRef.current).length >= MAX_DRAWINGS) return;
      const id = newDrawingId();
      changeDrawings((o) => opSet(o, id, d), { ...drawRef.current, [id]: d });
    },
    [changeDrawings],
  );
  const removeDrawing = useCallback(
    (id: string) => {
      if (eraseHit.current === id) eraseHit.current = null;
      const next = { ...drawRef.current };
      delete next[id];
      changeDrawings((o) => opRemove(o, id), next);
    },
    [changeDrawings],
  );

  useEffect(() => {
    drawAlive.current = true;
    let live = true;
    const seq = drawLoadSeq.current;
    const ac = new AbortController();
    const settleLoad = (saved: Record<string, Drawing> | null) => {
      // a refusal's reload, if one ran, has already applied everything
      if (!live || seq !== drawLoadSeq.current) return;
      const early = drawEarly.current;
      drawEarly.current = null;
      // a failed load shows none of the server's and blocks nothing
      if (saved === null) return;
      drawRef.current = applyOps(early ?? noOps(), saved);
      setDrawings(drawRef.current);
    };
    fetchMapDrawings(ac.signal).then(
      (d) => settleLoad(sanitizeDrawings(d)),
      () => settleLoad(null),
    );
    // ops restored from this browser are queued again
    if (!opsEmpty(drawPending.current)) scheduleDrawSave();
    return () => {
      live = false;
      // an open editor is committed first, so its text rides the unmount's save
      commitOpenEditorRef.current();
      drawAlive.current = false;
      ac.abort();
      if (drawTimer.current !== null) {
        window.clearTimeout(drawTimer.current);
        drawTimer.current = null;
      }
      // leaving sends whatever is queued, a failed POST's ops included (they
      // have no timer of their own), in one POST; with a POST in flight it
      // goes when that one answers. Nothing is sent after that.
      flushDraw();
    };
  }, [flushDraw, scheduleDrawSave]);

  // ---- pan and zoom
  /**
   * Put the view on the DOM. The card layer is laid out at `committedZ` (CSS
   * zoom); the difference between that and the view's zoom (below 1 always,
   * and while a zoom gesture runs ahead of a commit) is a scale transform,
   * which costs no layout. Writes only, and only what changed. The arrow
   * layer is not redrawn for a view change: the frame moves the drawn layer
   * and redraws it once the view pauses (see the frame's arrow block).
   */
  const applyView = useCallback(() => {
    const v = viewRef.current;
    // Only a view that actually changed marks the overview and stamps the
    // view as moving: this also runs after every render, and a render that
    // moved nothing must not redraw the overview or defer the arrow layer.
    const a = appliedView.current;
    if (a.x !== v.x || a.y !== v.y || a.z !== v.z || a.lz !== committedZ.current) {
      a.x = v.x;
      a.y = v.y;
      a.z = v.z;
      a.lz = committedZ.current;
      viewAt.current = performance.now();
      miniDirty.current = true;
    }
    const w = worldRef.current;
    if (w) {
      const t = `translate(${v.x}px, ${v.y}px)`;
      if (w.style.transform !== t) w.style.transform = t;
    }
    // the drawings follow the view as one transform on their group; their
    // own geometry stays in world units
    const g = drawG.current;
    if (g) {
      const t = `translate(${v.x} ${v.y}) scale(${v.z})`;
      if (g.getAttribute("transform") !== t) g.setAttribute("transform", t);
    }
    // an open text editor stays on the world point it was opened at, at the
    // size its text will be drawn
    const ed = editorRef.current;
    const ta = editorInput.current;
    if (ed && ta) {
      const s = ta.style;
      const l = `${ed.wx * v.z + v.x}px`;
      const tp = `${ed.wy * v.z + v.y}px`;
      const fs = `${ed.size * v.z}px`;
      if (s.left !== l) s.left = l;
      if (s.top !== tp) s.top = tp;
      if (s.fontSize !== fs) s.fontSize = fs;
    }
    const z = zoomRef.current;
    if (z) {
      if (ZOOM_OK) {
        const zs = z.style as CSSStyleDeclaration & { zoom: string };
        const cz = String(committedZ.current);
        if (zs.zoom !== cz) zs.zoom = cz;
        const k = v.z / committedZ.current;
        const t = Math.abs(k - 1) < 1e-6 ? "" : `scale(${k})`;
        if (z.style.transformOrigin !== "0px 0px") z.style.transformOrigin = "0 0";
        if (z.style.transform !== t) z.style.transform = t;
      } else {
        z.style.transformOrigin = "0 0";
        const t = `scale(${v.z})`;
        if (z.style.transform !== t) z.style.transform = t;
      }
    }
  }, []);
  const persistView = useCallback(() => {
    if (persistTimer.current !== null) window.clearTimeout(persistTimer.current);
    persistTimer.current = window.setTimeout(() => {
      persistTimer.current = null;
      const v = viewRef.current;
      writeLS(MAP_CAMERA_KEY, JSON.stringify({ x: Math.round(v.x), y: Math.round(v.y), z: +v.z.toFixed(3) }));
    }, 300);
  }, []);
  /**
   * Commit the current zoom: the card canvases are re-backed for it, and
   * above 1 the cards are laid out at it (one relayout, sharp text). At or
   * below 1 the layout zoom stays 1 and nothing is laid out again. A commit
   * that changes the layout zoom re-observes every card, so each reports its
   * size once at the new zoom and takeSize can tell that report from a
   * content change. Nothing here moves a card, settles or saves.
   */
  const commitZoom = useCallback(() => {
    if (zoomTimer.current !== null) window.clearTimeout(zoomTimer.current);
    zoomTimer.current = null;
    const z = viewRef.current.z;
    if (backZ.current === z) return;
    backZ.current = z;
    const lz = layoutZoom(z);
    const relayout = committedZ.current !== lz;
    committedZ.current = lz;
    applyView();
    if (relayout) {
      const ro = roRef.current;
      if (ro) {
        for (const el of cardEls.values()) {
          ro.unobserve(el);
          ro.observe(el);
        }
      }
    }
    // every card canvas is re-backed at the new scale on its next draw
    // (spread over frames by CARD_BUDGET)
    lastDraw.clear();
  }, [applyView, lastDraw, cardEls]);
  const zoomAt = useCallback(
    (f: number, px: number, py: number, commitNow = false) => {
      const v = viewRef.current;
      const z = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.z * f));
      const k = z / v.z;
      v.x = px - (px - v.x) * k;
      v.y = py - (py - v.y) * k;
      v.z = z;
      applyView();
      persistView();
      if (commitNow) commitZoom();
      else {
        if (zoomTimer.current !== null) window.clearTimeout(zoomTimer.current);
        zoomTimer.current = window.setTimeout(commitZoom, ZOOM_COMMIT_MS);
      }
    },
    [applyView, persistView, commitZoom],
  );
  const fit = useCallback(() => {
    const placed = entriesRef.current.map((e) => nodes.get(e.key)).filter((n): n is MapNode => !!n && finiteXY(n));
    if (!placed.length) return;
    let x0 = Infinity;
    let y0 = Infinity;
    let x1 = -Infinity;
    let y1 = -Infinity;
    for (const n of placed) {
      x0 = Math.min(x0, n.x - 40);
      y0 = Math.min(y0, n.y - PAD_TOP + 20);
      x1 = Math.max(x1, n.x + n.w + 40);
      y1 = Math.max(y1, n.y + n.h + 20);
    }
    const W = sizeRef.current.w || 1200;
    const H = sizeRef.current.h || 800;
    const z = Math.min(1.4, Math.max(0.3, Math.min(W / (x1 - x0), H / (y1 - y0))));
    viewRef.current = { z, x: (W - (x1 - x0) * z) / 2 - x0 * z, y: (H - (y1 - y0) * z) / 2 - y0 * z };
    applyView();
    persistView();
    commitZoom();
  }, [nodes, applyView, persistView, commitZoom]);

  /** The visible part of the plane, world units. */
  const viewRect = useCallback(() => {
    const v = viewRef.current;
    const s = sizeRef.current;
    return { x: -v.x / v.z, y: -v.y / v.z, w: s.w / v.z, h: s.h / v.z };
  }, []);
  /**
   * A page point (clientX, clientY) in world units, through the current view,
   * for a map whose top-left corner is at page point (left, top). Reads no
   * layout itself: the marquee passes the offset it read at its press.
   */
  const toWorld = useCallback((cx: number, cy: number, left: number, top: number) => {
    const v = viewRef.current;
    return { x: (cx - left - v.x) / v.z, y: (cy - top - v.y) / v.z };
  }, []);
  /**
   * The map's page offset, read from layout. A press reads it once (a press
   * is not an animation frame) and keeps it for its drag, so a banner or chip
   * row that appeared above the map since the last window resize does not put
   * the gesture off the cursor; where layout cannot be read the resize
   * handler's cached offset stands in.
   */
  const pageOffset = useCallback(() => {
    let left = sizeRef.current.left;
    let top = sizeRef.current.top;
    try {
      const r = rootRef.current?.getBoundingClientRect();
      if (r && Number.isFinite(r.left) && Number.isFinite(r.top)) {
        left = r.left;
        top = r.top;
      }
    } catch {
      // no layout to read: keep the cached offset
    }
    return { left, top };
  }, []);
  /**
   * Start a Shift press: the marquee drag, from page point (sx, sy), on a
   * card (`card`) or on the background (null), with the page offset read at
   * the press (pageOffset).
   */
  const startMarquee = useCallback(
    (sx: number, sy: number, card: string | null) => {
      const { left, top } = pageOffset();
      const w = toWorld(sx, sy, left, top);
      dragRef.current = { kind: "marquee", sx, sy, left, top, wx: w.x, wy: w.y, moved: false, card, lx: sx, ly: sy };
    },
    [toWorld, pageOffset],
  );

  // ---- drawing gestures

  /**
   * The ids in the layer's order and each drawing's box, for the eraser:
   * rebuilt only when the shown set is replaced, never per pointer move.
   */
  const hitIndex = useRef<{ rec: Record<string, Drawing> | null; ids: string[]; boxes: DrawBox[] }>({
    rec: null,
    ids: [],
    boxes: [],
  });
  /** The drawing an eraser at world point (x, y) would delete: the latest in the layer's order that it touches. */
  const drawingAt = useCallback((x: number, y: number): string | null => {
    const rec = drawRef.current;
    const ix = hitIndex.current;
    if (ix.rec !== rec) {
      ix.rec = rec;
      ix.ids = Object.keys(rec).sort();
      ix.boxes = ix.ids.map((id) => drawingBox(rec[id]!));
    }
    const tol = ERASE_TOL_PX / viewRef.current.z;
    for (let i = ix.ids.length - 1; i >= 0; i--) {
      const d = rec[ix.ids[i]!]!;
      // a point farther than the reach from the box cannot touch the drawing
      const r = tol + d.size / 2;
      const b = ix.boxes[i]!;
      if (x < b.x - r || x > b.x + b.w + r || y < b.y - r || y > b.y + b.h + r) continue;
      if (hitDrawing(d, x, y, tol)) return ix.ids[i]!;
    }
    return null;
  }, []);
  /** Mark one drawing (or none) as the one the eraser is over; class writes only. */
  const setEraseHit = useCallback((id: string | null) => {
    if (eraseHit.current === id) return;
    const g = drawG.current;
    const find = (want: string) => {
      if (!g) return null;
      for (const el of Array.from(g.children)) if (el.getAttribute("data-draw-id") === want) return el;
      return null;
    };
    if (eraseHit.current !== null) find(eraseHit.current)?.classList.remove("erase-hit");
    eraseHit.current = id;
    if (id !== null) find(id)?.classList.add("erase-hit");
  }, []);
  const eraseAt = useCallback(
    (x: number, y: number) => {
      const id = drawingAt(x, y);
      if (id !== null) removeDrawing(id);
    },
    [drawingAt, removeDrawing],
  );
  /** Write the draft's geometry from the press in flight; writes only. */
  const writeDraft = useCallback(() => {
    const d = dragRef.current;
    const el = draftEl.current;
    if (!el || d?.kind !== "draw") return;
    if (d.tool === "pen") {
      if (el.getAttribute("d") !== d.path) el.setAttribute("d", d.path);
      return;
    }
    const geom = shapeGeom(d.tool, [d.wx, d.wy, d.cx, d.cy]);
    for (const [k, val] of Object.entries(geom)) el.setAttribute(k, String(val));
  }, []);
  /** The draft element mounts at the geometry the press already set. */
  const setDraftEl = useCallback(
    (el: SVGElement | null) => {
      draftEl.current = el;
      if (el) writeDraft();
    },
    [writeDraft],
  );

  /**
   * Close the text editor `id` (a late blur from an editor already replaced
   * does nothing). `value` null closes it without a drawing; otherwise the
   * text trimmed of the whitespace the save route judges by (trimDrawText),
   * cut to MAX_TEXT_RUNES, becomes a text drawing at the point and size fixed
   * when it opened, unless nothing but whitespace is left (addDrawing's
   * validDrawing check refuses that).
   */
  const closeEditor = useCallback(
    (id: number, value: string | null) => {
      const ed = editorRef.current;
      if (!ed || ed.id !== id) return;
      editorRef.current = null;
      editorInput.current = null;
      setEditor(null);
      if (value === null) return;
      const text = [...trimDrawText(value)].slice(0, MAX_TEXT_RUNES).join("");
      addDrawing({ kind: "text", pts: [ed.wx, ed.wy], size: ed.size, text, ...(ed.color ? { color: ed.color } : {}) });
    },
    [addDrawing],
  );
  /** Commit the open editor, if any, with what it holds now (its last change once the textarea is detached). */
  const commitOpenEditor = useCallback(() => {
    const ed = editorRef.current;
    if (ed) closeEditor(ed.id, editorInput.current?.value ?? editorText.current);
  }, [closeEditor]);
  commitOpenEditorRef.current = commitOpenEditor;

  /**
   * Arm a tool, or none. An open editor is committed first; a press in
   * flight on the surface stops (a draft is dropped, an eraser stops
   * erasing), since the surface it started on goes away or changes tool.
   */
  const armTool = useCallback(
    (t: DrawTool | null) => {
      if (toolRef.current === t) return;
      commitOpenEditor();
      const d = dragRef.current;
      if (d?.kind === "erase") dragRef.current = null;
      else if (d?.kind === "draw" && !d.dead) {
        d.dead = true;
        setDraft(null);
      }
      setEraseHit(null);
      toolRef.current = t;
      setToolState(t);
    },
    [commitOpenEditor, setEraseHit],
  );
  /** Open or close the tool panel, remembering it for this browser; closing it disarms the tool. */
  const toggleTools = () => {
    const next = !toolsOpen;
    setToolsShown(next);
    if (!next) armTool(null);
    setToolsOpen(next);
  };

  /**
   * A press on the drawing surface. The compatibility mousedown's default is
   * always prevented, whether or not it is the event handled: the focus move
   * it would make would blur (and so commit) a text editor the press just
   * opened. Nothing under the surface ever sees the press.
   */
  const onSurfacePointerDown = (ev: ReactMouseEvent<HTMLDivElement>) => {
    if (ev.type === "mousedown" && (ev.button ?? 0) === 0) ev.preventDefault();
    ev.stopPropagation();
    if (!gestureEvent(ev)) return;
    if (ev.button !== undefined && ev.button !== 0) return;
    const t = toolRef.current;
    if (!t || dragRef.current) return;
    const { left, top } = pageOffset();
    const sx = num(ev.clientX);
    const sy = num(ev.clientY);
    const w = toWorld(sx, sy, left, top);
    const z = viewRef.current.z;
    if (t === "text") {
      commitOpenEditor();
      const ed: EditorAt = {
        id: ++editorSeq.current,
        wx: round1(w.x),
        wy: round1(w.y),
        size: round2(DRAW_TEXT_PX / z),
        color: inkRef.current,
      };
      editorRef.current = ed;
      editorText.current = "";
      setEditor(ed);
      return;
    }
    if (t === "erase") {
      setEraseHit(null);
      dragRef.current = { kind: "erase", left, top };
      eraseAt(w.x, w.y);
      return;
    }
    const wx = round1(w.x);
    const wy = round1(w.y);
    const size = round2(DRAW_STROKE_PX / z);
    dragRef.current = {
      kind: "draw",
      tool: t,
      sx,
      sy,
      left,
      top,
      wx,
      wy,
      cx: wx,
      cy: wy,
      pts: [wx, wy],
      path: penPath([wx, wy]),
      size,
      color: inkRef.current ?? undefined,
      moved: false,
      lx: sx,
      ly: sy,
      dead: false,
    };
    setDraft({ kind: t, size, color: inkRef.current ?? undefined });
  };
  /** With the eraser armed and no press held, mark the drawing a press here would delete. */
  const onSurfacePointerMove = (ev: ReactMouseEvent<HTMLDivElement>) => {
    if (!gestureEvent(ev)) return;
    if (toolRef.current !== "erase" || dragRef.current) return;
    const { left, top } = pageOffset();
    const w = toWorld(num(ev.clientX), num(ev.clientY), left, top);
    setEraseHit(drawingAt(w.x, w.y));
  };
  const onSurfaceLeave = () => setEraseHit(null);
  /** The overview's frame for the cards and view as they stand. */
  const currentMiniFrame = useCallback(() => {
    const placedNow = entriesRef.current.map((e) => nodes.get(e.key)).filter((n): n is MapNode => !!n && finiteXY(n));
    return miniFrame(placedNow, viewRect(), { w: MINI_W, h: MINI_H });
  }, [nodes, viewRect]);
  /** Move the view (never a card) so world point (wx, wy) is at the map's centre. */
  const centreOn = useCallback(
    (wx: number, wy: number) => {
      if (!Number.isFinite(wx) || !Number.isFinite(wy)) return;
      const v = viewRef.current;
      v.x = sizeRef.current.w / 2 - wx * v.z;
      v.y = sizeRef.current.h / 2 - wy * v.z;
      applyView();
      persistView();
    },
    [applyView, persistView],
  );
  /**
   * A press on the overview centres the view on the world point under it,
   * and a drag keeps doing so. The frame is the one at the press, held for
   * the drag: the overview's own frame follows the view, and re-reading it
   * mid-drag would move the point under the pointer as the view moved.
   */
  const onMiniPointerDown = (ev: ReactMouseEvent<HTMLCanvasElement>) => {
    if (!gestureEvent(ev)) return;
    if (ev.button !== undefined && ev.button !== 0) return;
    ev.stopPropagation();
    // a second finger never takes over a card drag (the drop must still be
    // written, settled and saved) or a marquee (its release still selects)
    const cur = dragRef.current?.kind;
    if (cur === "card" || cur === "marquee") return;
    let left = 0;
    let top = 0;
    try {
      const r = ev.currentTarget.getBoundingClientRect();
      left = r.left;
      top = r.top;
    } catch {
      // no layout to read: the overview's corner is the page origin
    }
    const d = { kind: "mini" as const, left, top, f: currentMiniFrame() };
    dragRef.current = d;
    const [wx, wy] = miniToWorld(num(ev.clientX) - left, num(ev.clientY) - top, d.f);
    centreOn(wx, wy);
  };

  useLayoutEffect(() => {
    applyView();
  });

  // the map fills the window below its own top edge; measured on mount, on a
  // window resize and when the map is shown again — never per frame
  useEffect(() => {
    const size = () => {
      const root = rootRef.current;
      if (!root || hidden) return;
      const r = root.getBoundingClientRect();
      const h = Math.max(320, window.innerHeight - Math.max(0, r.top));
      root.style.height = `${h}px`;
      sizeRef.current = { w: root.clientWidth, h, left: r.left, top: r.top };
      underDirty.current = true;
      miniDirty.current = true;
    };
    size();
    window.addEventListener("resize", size);
    return () => window.removeEventListener("resize", size);
  }, [hidden]);

  // the palette: read once, and again whenever the root's theme or accent
  // attributes change
  const paletteRef = useRef<Palette | null>(null);
  useEffect(() => {
    const read = () => {
      paletteRef.current = readPalette();
      underDirty.current = true;
      miniDirty.current = true;
      lastDraw.clear();
    };
    read();
    if (typeof MutationObserver === "undefined") return;
    const mo = new MutationObserver(read);
    mo.observe(document.documentElement, { attributes: true });
    return () => mo.disconnect();
  }, [lastDraw]);

  // wheel: pan, or zoom with ctrl/⌘ (and a trackpad pinch, which arrives as a
  // ctrl-wheel). A native listener, since React's is passive and could not
  // stop the page from scrolling.
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const onWheel = (ev: WheelEvent) => {
      ev.preventDefault();
      if (ev.ctrlKey || ev.metaKey) {
        // the root's page offset is cached by the resize handler, so a pinch
        // reads no layout per event
        const r = sizeRef.current;
        zoomAt(Math.exp(-ev.deltaY * 0.01), ev.clientX - r.left, ev.clientY - r.top);
      } else {
        viewRef.current.x -= ev.deltaX;
        viewRef.current.y -= ev.deltaY;
        applyView();
        persistView();
      }
    };
    root.addEventListener("wheel", onWheel, { passive: false });
    return () => root.removeEventListener("wheel", onWheel);
  }, [zoomAt, applyView, persistView]);

  // ---- pointer: drag a card (or the selected group), pan the background,
  // draw the marquee, or carry on a drawing tool's press (a shape's draft, or
  // the eraser)
  useEffect(() => {
    /** The cards on the map that have a place: what a marquee can select. */
    const placedNodes = () =>
      entriesRef.current.map((e) => nodes.get(e.key)).filter((n): n is MapNode => !!n && finiteXY(n));
    /**
     * Draw the marquee from the press point to the pointer, and mark the
     * cards it would select. The press point is kept in world units and put
     * through the current view, so the box stays on the same spot of the
     * plane if the view moves under it; the pointer is placed through the
     * page offset read at the press. A gesture: its allocations are its own,
     * and nothing here runs on an idle frame.
     */
    const updateMarquee = (d: Extract<Drag, { kind: "marquee" }>, cx: number, cy: number) => {
      const v = viewRef.current;
      const ax = d.wx * v.z + v.x;
      const ay = d.wy * v.z + v.y;
      const bx = cx - d.left;
      const by = cy - d.top;
      const box = marqueeBox.current;
      box[0] = Math.min(ax, bx);
      box[1] = Math.min(ay, by);
      box[2] = Math.abs(bx - ax);
      box[3] = Math.abs(by - ay);
      const el = marqueeRef.current;
      if (el) {
        el.style.left = `${box[0]}px`;
        el.style.top = `${box[1]}px`;
        el.style.width = `${box[2]}px`;
        el.style.height = `${box[3]}px`;
      }
      const hits = new Set(marqueeKeys(placedNodes(), { x: d.wx, y: d.wy }, toWorld(cx, cy, d.left, d.top)));
      for (const k of marqueeHits) {
        if (hits.has(k)) continue;
        marqueeHits.delete(k);
        cardEls.get(k)?.classList.remove("marquee-hit");
      }
      for (const k of hits) {
        if (marqueeHits.has(k)) continue;
        marqueeHits.add(k);
        cardEls.get(k)?.classList.add("marquee-hit");
      }
    };
    const clearMarqueeHits = () => {
      for (const k of marqueeHits) cardEls.get(k)?.classList.remove("marquee-hit");
      marqueeHits.clear();
    };
    const onMove = (ev: MouseEvent) => {
      const d = dragRef.current;
      if (!d) return;
      if (d.kind === "mini") {
        const [wx, wy] = miniToWorld(num(ev.clientX) - d.left, num(ev.clientY) - d.top, d.f);
        centreOn(wx, wy);
        return;
      }
      if (d.kind === "erase") {
        const w = toWorld(num(ev.clientX), num(ev.clientY), d.left, d.top);
        eraseAt(w.x, w.y);
        return;
      }
      if (d.kind === "draw") {
        if (d.dead) return;
        const cx = num(ev.clientX);
        const cy = num(ev.clientY);
        d.lx = cx;
        d.ly = cy;
        if (Math.abs(cx - d.sx) + Math.abs(cy - d.sy) > DRAG_SLOP) d.moved = true;
        const w = toWorld(cx, cy, d.left, d.top);
        d.cx = round1(w.x);
        d.cy = round1(w.y);
        const n = d.pts.length;
        if (d.tool === "pen" && (d.pts[n - 2] !== d.cx || d.pts[n - 1] !== d.cy)) {
          d.pts.push(d.cx, d.cy);
          // the same text penPath writes for this point
          d.path += ` L ${String(d.cx)} ${String(d.cy)}`;
        }
        writeDraft();
        return;
      }
      const dx = num(ev.clientX) - d.sx;
      const dy = num(ev.clientY) - d.sy;
      if (d.kind !== "card") {
        d.lx = num(ev.clientX);
        d.ly = num(ev.clientY);
      }
      if (d.kind === "pan") {
        if (Math.abs(dx) + Math.abs(dy) > DRAG_SLOP) d.moved = true;
        viewRef.current.x = d.ox + dx;
        viewRef.current.y = d.oy + dy;
        applyView();
        return;
      }
      if (d.kind === "marquee") {
        if (!d.moved && Math.abs(dx) + Math.abs(dy) > DRAG_SLOP) {
          d.moved = true;
          rootRef.current?.classList.add("marqueeing");
          setMarqueeOn(true);
        }
        if (d.moved) updateMarquee(d, num(ev.clientX), num(ev.clientY));
        return;
      }
      if (Math.abs(dx) + Math.abs(dy) > DRAG_SLOP && !d.moved) {
        d.moved = true;
        // a plain drag of a card outside the selection moves that card alone
        // and empties the selection
        if (!d.group) setSelection(new Set());
      }
      if (!d.moved) return;
      const z = viewRef.current.z;
      // the dragged card, or every card of the group by the same world
      // delta: nothing is pushed while they are in the air
      const moveOne = (key: string, ox: number, oy: number) => {
        const node = nodes.get(key);
        if (!node) return;
        node.x = ox + dx / z;
        node.y = oy + dy / z;
        const el = cardEls.get(key);
        if (el) {
          el.style.left = `${node.x}px`;
          el.style.top = `${node.y}px`;
        }
      };
      if (d.group) for (const m of d.group) moveOne(m.key, m.ox, m.oy);
      else moveOne(d.key, d.ox, d.oy);
      underDirty.current = true;
      miniDirty.current = true;
    };
    const onUp = (ev: Event) => {
      const d = dragRef.current;
      if (!d) return;
      dragRef.current = null;
      rootRef.current?.classList.remove("panning");
      if (d.kind === "mini") {
        persistView();
        return;
      }
      if (d.kind === "erase") return;
      // The release point: the release event's own position, or the last
      // position a move reported when the release carries none. A browser's
      // release always carries one; an event built without coordinates reads
      // as the page's corner (0, 0), which is taken as "none". A release far
      // from its press is a drag even if no move was reported.
      let cx = 0;
      let cy = 0;
      let far = false;
      if (d.kind !== "card") {
        const me = ev as MouseEvent;
        const ex = typeof me.clientX === "number" && Number.isFinite(me.clientX) ? me.clientX : 0;
        const ey = typeof me.clientY === "number" && Number.isFinite(me.clientY) ? me.clientY : 0;
        const given = ex !== 0 || ey !== 0;
        cx = given ? ex : d.lx;
        cy = given ? ey : d.ly;
        far = Math.abs(cx - d.sx) + Math.abs(cy - d.sy) > DRAG_SLOP;
      }
      if (d.kind === "draw") {
        // the draft goes in every case; a drawing is made only by a real
        // drag that no Escape or cancel ended
        setDraft(null);
        if (d.dead || ev.type === "pointercancel" || (!d.moved && !far)) return;
        const w = toWorld(cx, cy, d.left, d.top);
        const ex = round1(w.x);
        const ey = round1(w.y);
        let pts: number[];
        if (d.tool === "pen") {
          const raw = d.pts.slice();
          const n = raw.length;
          if (raw[n - 2] !== ex || raw[n - 1] !== ey) raw.push(ex, ey);
          pts = simplifyStroke(raw, 1 / viewRef.current.z, MAX_PEN_POINTS);
        } else {
          pts = [d.wx, d.wy, ex, ey];
          if ((d.tool === "rect" || d.tool === "ellipse") && (ex === d.wx || ey === d.wy)) return;
        }
        addDrawing({ kind: d.tool, pts, size: d.size, ...(d.color ? { color: d.color } : {}) });
        return;
      }
      if (d.kind === "pan") {
        persistView();
        // a plain press and release on the background empties the selection;
        // a background drag only pans
        if (!d.moved && !far && ev.type !== "pointercancel") setSelection(new Set());
        return;
      }
      if (d.kind === "marquee") {
        rootRef.current?.classList.remove("marqueeing");
        clearMarqueeHits();
        setMarqueeOn(false);
        // the click that ends a shift gesture on a card opens nothing
        if (d.card) suppressClick.current = true;
        if (ev.type === "pointercancel") return;
        if (d.moved || far) {
          setSelection(new Set(marqueeKeys(placedNodes(), { x: d.wx, y: d.wy }, toWorld(cx, cy, d.left, d.top))));
        } else if (d.card) {
          const next = new Set(selRef.current);
          if (next.has(d.card)) next.delete(d.card);
          else next.add(d.card);
          setSelection(next);
        }
        return;
      }
      rootRef.current?.classList.remove("dragging");
      if (d.moved) {
        // the click that ends this gesture is not a click on the card
        suppressClick.current = true;
        // the group's members still on the map, or the one dragged card. A
        // member that left the map mid-drag (filtered out, archived) goes back
        // to where it stood at the press: it is not saved, so the dragged spot
        // would otherwise be where it reappears in this mount while the server
        // and this browser's record still hold the old one
        let keys: string[] = [d.key];
        if (d.group) {
          keys = [];
          for (const m of d.group) {
            if (byKeyRef.current.has(m.key)) {
              keys.push(m.key);
              continue;
            }
            const n = nodes.get(m.key);
            if (n) {
              n.x = m.ox;
              n.y = m.oy;
            }
          }
        }
        if (phase.current !== "final") for (const k of keys) draggedEarly.add(k);
        markDirty(keys);
        writePositions(keys);
        // the drop: the cards it landed on make way, the dropped ones hold.
        // Every card that moved is queued here and goes out in the one
        // debounced save below.
        settleToRest(d.group ? new Set(keys) : d.key);
        scheduleSave();
        rerender();
      }
    };
    const move = (ev: MouseEvent) => {
      if (gestureEvent(ev)) onMove(ev);
    };
    const up = (ev: Event) => {
      if (gestureEvent(ev)) onUp(ev);
    };
    const types = [
      ["pointermove", move],
      ["mousemove", move],
      ["pointerup", up],
      ["pointercancel", up],
      ["mouseup", up],
    ] as const;
    for (const [t, fn] of types) window.addEventListener(t, fn as EventListener);
    return () => {
      for (const [t, fn] of types) window.removeEventListener(t, fn as EventListener);
    };
  }, [
    nodes,
    cardEls,
    markDirty,
    draggedEarly,
    applyView,
    persistView,
    scheduleSave,
    rerender,
    writePositions,
    settleToRest,
    centreOn,
    toWorld,
    setSelection,
    marqueeHits,
    gestureEvent,
    eraseAt,
    writeDraft,
    addDrawing,
  ]);

  const onCardPointerDown = useCallback(
    (key: string, ev: ReactMouseEvent<HTMLDivElement>) => {
      if (!gestureEvent(ev)) return;
      if (ev.button !== undefined && ev.button !== 0) return;
      // a second finger landing on a card never takes over a card drag or a
      // marquee in flight, as on the background and the overview: the drop
      // must still be written, settled and saved, and the marquee's release
      // still has to select and take its box down
      const cur = dragRef.current?.kind;
      if (cur === "card" || cur === "marquee") return;
      suppressClick.current = false;
      // a press on a control inside the card is that control's, never a drag
      if (!shouldStartDrag(ev.target)) return;
      const node = nodes.get(key);
      if (!node || !finiteXY(node)) return;
      ev.stopPropagation();
      const sx = num(ev.clientX);
      const sy = num(ev.clientY);
      if (ev.shiftKey) {
        // Shift: a drag draws the marquee, a click toggles this card; neither
        // moves a card or opens one
        startMarquee(sx, sy, key);
        return;
      }
      // a selected card carries the whole selection with it, each card from
      // where it stood at the press
      let group: GroupMember[] | null = null;
      if (selRef.current.has(key)) {
        group = [];
        for (const k of selRef.current) {
          const m = nodes.get(k);
          if (m && finiteXY(m) && byKeyRef.current.has(k)) group.push({ key: k, ox: m.x, oy: m.y });
        }
      }
      dragRef.current = { kind: "card", key, sx, sy, ox: node.x, oy: node.y, moved: false, group };
      rootRef.current?.classList.add("dragging");
    },
    [nodes, startMarquee, gestureEvent],
  );

  const onCardClickCapture = useCallback((ev: ReactMouseEvent) => {
    if (!suppressClick.current) return;
    suppressClick.current = false;
    ev.stopPropagation();
    ev.preventDefault();
  }, []);

  const onCardHover = useCallback(
    (key: string, on: boolean) => {
      const s = scenes.get(key);
      if (s) s.hover = on;
      lastDraw.delete(key);
    },
    [scenes, lastDraw],
  );

  const cardRefFor = useCallback(
    (key: string, el: HTMLDivElement | null) => {
      const prev = cardEls.get(key);
      if (el) cardEls.set(key, el);
      else cardEls.delete(key);
      observe(el, prev);
    },
    [cardEls, observe],
  );

  const onRootPointerDown = (ev: ReactMouseEvent<HTMLElement>) => {
    if (!gestureEvent(ev)) return;
    if (ev.button !== undefined && ev.button !== 0) return;
    const t = ev.target as Element | null;
    // the drawing surface stops its own presses; the text editor's are the editor's
    if (t && typeof t.closest === "function" && t.closest(".map-card, .map-corner, #map-draw-surface, #map-text-input")) return;
    // a second finger never takes over a card drag or a marquee (see onMiniPointerDown)
    const cur = dragRef.current?.kind;
    if (cur === "card" || cur === "marquee") return;
    const sx = num(ev.clientX);
    const sy = num(ev.clientY);
    if (ev.shiftKey) {
      // Shift on the background draws the marquee instead of panning
      startMarquee(sx, sy, null);
      return;
    }
    const v = viewRef.current;
    dragRef.current = { kind: "pan", sx, sy, ox: v.x, oy: v.y, moved: false, lx: sx, ly: sy };
    rootRef.current?.classList.add("panning");
  };

  // Escape, but only an Escape meant for the map. The listener is on the
  // window in the bubble phase, so it sees a key last:
  //  - an open overlay (steer, editor, thread, artifact, usage) closes through
  //    useEscapeStack's document listener, which calls preventDefault, so
  //    `defaultPrevented` says the key was spent;
  //  - the search box, a goal edit, a menu and a thread rename stop the key
  //    from propagating, so it never arrives here;
  //  - an Escape typed in any other text field (an input, a textarea, a
  //    contenteditable, the map's own text editor) is that field's, so it is
  //    ignored too.
  // An Escape meant for the map does one thing, the first that applies: it
  // drops a draft whose press is still held (the tool stays armed), else it
  // disarms the armed tool, else it empties the selection.
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== "Escape" || ev.defaultPrevented || hidden) return;
      const t = ev.target;
      if (t instanceof HTMLElement && (t.isContentEditable || t.closest("input, textarea, select"))) return;
      const d = dragRef.current;
      if (d?.kind === "draw" && !d.dead) {
        d.dead = true;
        setDraft(null);
        return;
      }
      if (toolRef.current) {
        armTool(null);
        return;
      }
      if (selRef.current.size) setSelection(new Set());
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [hidden, setSelection, armTool]);

  // ---- the animation loop. An ordinary frame reads no layout and runs no
  // physics; it redraws only what is due.
  useEffect(() => {
    let id = 0;
    let underAt = -Infinity;
    let checkAt = -Infinity;
    /** per spawn link: 1 while its parent's lead is delegating */
    let hot = new Uint8Array(0);
    let anyHot = false;
    let freshAny = false;
    let overLive = false;
    /** The view the arrow layer was last drawn at; `ok` false until it has been drawn. */
    const underView = { ok: false, x: 0, y: 0, z: 1 };
    /** Due cards not in motion, waiting on the frame's budget: list index, pose, millis since last drawn. Reused. */
    const pendIdx: number[] = [];
    const pendPose: string[] = [];
    const pendWait: number[] = [];

    const sizeCanvas = (cv: HTMLCanvasElement, cssW: number, cssH: number, scale: number) => {
      const w = Math.max(1, Math.round(cssW * scale));
      const h = Math.max(1, Math.round(cssH * scale));
      if (cv.width !== w || cv.height !== h) {
        cv.width = w;
        cv.height = h;
      }
    };

    /** Redraw one card's characters now. */
    const drawCard = (
      c: CanvasRenderingContext2D,
      cv: HTMLCanvasElement,
      n: MapNode,
      s: CardScene,
      pose: string,
      C: Palette,
      ms: number,
      now: number,
      nowMs: number,
      prev: number | undefined,
      bs: number,
    ) => {
      const dt = prev === undefined ? 0 : Math.min(0.25, (ms - prev) / 1000);
      lastDraw.set(s.key, ms);
      sizeCanvas(cv, n.w + PAD_X * 2, PAD_TOP + PAD_BOTTOM, bs);
      c.setTransform(1, 0, 0, 1, 0, 0);
      c.clearRect(0, 0, cv.width, cv.height);
      c.setTransform(bs, 0, 0, bs, PAD_X * bs, PAD_TOP * bs);
      s.w = n.w;
      drawScene(c, s, pose, C, now, nowMs, dt);
    };

    const frame = (ms: number) => {
      id = raf(frame);
      const root = rootRef.current;
      const C = paletteRef.current;
      if (!root || !C || hidden || (typeof document !== "undefined" && document.hidden)) return;
      const now = ms / 1000;
      const list = entriesRef.current;

      // the size-change / first-placement settle, once every size is real
      // (jsdom never measures one, which keeps saved positions exactly where
      // a test put them); and the rest of a settle that hit its step bound
      if (!dragRef.current && phase.current !== "waiting") {
        // arrivals first, once their real sizes are in (where sizes can be
        // observed at all): they yield to the cards already showing
        if (arriving.size && (typeof ResizeObserver === "undefined" || arrivalsMeasured())) placeArrivals();
        const more = settleMore.current;
        if (more) settleToRest(more.held);
        else if (settleWanted.current && allMeasured()) {
          settleWanted.current = false;
          settleToRest();
        }
      }
      if (fitPending.current && phase.current !== "waiting" && list.length) {
        fitPending.current = false;
        fit();
      }

      const v = viewRef.current;
      const W = sizeRef.current.w;
      const H = sizeRef.current.h;
      const dpr = Math.min(MAX_BACKING, window.devicePixelRatio || 1);
      const nowMs = Date.now();
      const links = linksRef.current;
      const talks = talkRef.current;
      const keyed = byKeyRef.current;

      // ---- beneath every card: arrows and threads, only when due. The hot
      // state is a per-link flag array, compared in place.
      if (ms - checkAt > UNDER_CHECK_MS) {
        checkAt = ms;
        if (hot.length !== links.length) {
          hot = new Uint8Array(links.length);
          underDirty.current = true;
        }
        anyHot = false;
        for (let i = 0; i < links.length; i++) {
          const pa = keyed.get(links[i]![0]);
          const h = pa && leadPose(pa.job, nowMs) === "delegate" ? 1 : 0;
          if (h !== hot[i]) {
            hot[i] = h;
            underDirty.current = true;
          }
          if (h) anyHot = true;
        }
        let fresh = false;
        for (let i = 0; i < talks.length; i++) {
          if ((nowMs - talks[i]!.lastMs) / 1000 < THREAD_FRESH_S) fresh = true;
        }
        if (fresh !== freshAny) underDirty.current = true;
        freshAny = fresh;
      }
      // The layer is drawn UNDER_MARGIN past every edge. While the view is
      // moving (a wheel pan, a pinch, a background drag) the drawn layer is
      // moved and scaled with a transform instead of redrawn — the hot and
      // fresh redraws wait too — and it is redrawn at the view once the view
      // has paused for VIEW_PAUSE_MS, or as soon as the moved layer no longer
      // covers the map (the pan went past the margin, or a zoom out shrank
      // it). A change to what it shows (underDirty) still redraws at once.
      const M = UNDER_MARGIN;
      const gesturing = ms - viewAt.current < VIEW_PAUSE_MS;
      const viewMoved = !underView.ok || underView.x !== v.x || underView.y !== v.y || underView.z !== v.z;
      let covered = false;
      let tx = 0;
      let ty = 0;
      let k = 1;
      if (viewMoved && underView.ok) {
        k = v.z / underView.z;
        tx = v.x + M - k * (M + underView.x);
        ty = v.y + M - k * (M + underView.y);
        covered = tx - M <= 0 && ty - M <= 0 && tx - M + k * (W + 2 * M) >= W && ty - M + k * (H + 2 * M) >= H;
      }
      const underDue =
        underDirty.current ||
        (viewMoved && (!gesturing || !covered)) ||
        (!gesturing && anyHot && ms - underAt > UNDER_HOT_MS) ||
        (!gesturing && freshAny && ms - underAt > UNDER_FRESH_MS);
      const under = underRef.current;
      const uc = under && underDue ? ctxOf(under) : null;
      if (under && !uc && viewMoved && covered) {
        const t = `translate(${tx}px, ${ty}px) scale(${k})`;
        if (under.style.transform !== t) under.style.transform = t;
      }
      if (under && uc) {
        underDirty.current = false;
        underAt = ms;
        underView.ok = true;
        underView.x = v.x;
        underView.y = v.y;
        underView.z = v.z;
        const cw = W + 2 * M;
        const ch = H + 2 * M;
        sizeCanvas(under, cw, ch, dpr);
        const us = under.style;
        if (us.width !== `${cw}px`) us.width = `${cw}px`;
        if (us.height !== `${ch}px`) us.height = `${ch}px`;
        if (us.left !== `${-M}px`) us.left = `${-M}px`;
        if (us.top !== `${-M}px`) us.top = `${-M}px`;
        if (us.transform !== "") us.transform = "";
        uc.setTransform(1, 0, 0, 1, 0, 0);
        uc.clearRect(0, 0, under.width, under.height);
        uc.setTransform(dpr * v.z, 0, 0, dpr * v.z, dpr * (v.x + M), dpr * (v.y + M));
        for (let i = 0; i < links.length; i++) {
          const l = links[i]!;
          const na = nodes.get(l[0]);
          const nb = nodes.get(l[1]);
          if (!na || !nb || !finiteXY(na) || !finiteXY(nb)) continue;
          drawSpawnEdge(uc, C, na, nb, hot[i] === 1, now);
        }
        for (let i = 0; i < talks.length; i++) {
          const t = talks[i]!;
          const na = nodes.get(t.a);
          const nb = nodes.get(t.b);
          if (!na || !nb || !finiteXY(na) || !finiteXY(nb)) continue;
          drawThread(uc, C, na, nb, t.n, Math.max(0, (nowMs - t.lastMs) / 1000));
        }
      } else if (underDue) {
        underDirty.current = false;
      }

      // ---- each card's own cast, at the rate its pose needs. A canvas is
      // sized when it is first drawn and when the committed zoom changes,
      // never shrunk for leaving the screen: re-allocating backing stores as
      // cards scroll in and out was a resize on every pan frame.
      // A card in motion (its interval is the fast one: a working lead in an
      // active pose, a bubble, a walk, a hover) draws as soon as it is due.
      // The rest that are due are held to what is left of CARD_BUDGET for
      // this frame (never less than one), most overdue first; the others
      // wait for a later frame.
      const bs = Math.min(MAX_BACKING, (window.devicePixelRatio || 1) * backZ.current);
      const far = v.z < FAR_ZOOM;
      const vx0 = -v.x / v.z;
      const vy0 = -v.y / v.z;
      const vx1 = (W - v.x) / v.z;
      const vy1 = (H - v.y) / v.z;
      let drawn = 0;
      let nPend = 0;
      for (let i = 0; i < list.length; i++) {
        const e = list[i]!;
        const cv = canvasEls.get(e.key);
        const n = nodes.get(e.key);
        const s = scenes.get(e.key);
        if (!cv || !n || !s || !finiteXY(n)) continue;
        const visible =
          W === 0 ||
          (n.x + n.w + PAD_X > vx0 && n.x - PAD_X < vx1 && n.y + PAD_BOTTOM > vy0 && n.y - PAD_TOP < vy1);
        if (!visible) continue;
        const pose = leadPose(e.job, nowMs);
        const prev = lastDraw.get(e.key);
        const iv = intervalFor(pose, sceneBusy(s, now), far);
        if (prev !== undefined && ms - prev < iv) continue;
        const c = ctxOf(cv);
        if (!c) continue;
        if (iv === FAST_MS) {
          drawCard(c, cv, n, s, pose, C, ms, now, nowMs, prev, bs);
          drawn++;
          continue;
        }
        pendIdx[nPend] = i;
        pendPose[nPend] = pose;
        pendWait[nPend] = prev === undefined ? Infinity : ms - prev;
        nPend++;
      }
      // never fewer than one: with six or more cards in motion due, the most
      // overdue waiting card still draws, so no card is starved of a redraw
      for (let left = Math.max(1, CARD_BUDGET - drawn); left > 0 && nPend > 0; left--) {
        let best = 0;
        for (let j = 1; j < nPend; j++) if (pendWait[j]! > pendWait[best]!) best = j;
        const e = list[pendIdx[best]!]!;
        const cv = canvasEls.get(e.key)!;
        const c = ctxOf(cv)!;
        drawCard(c, cv, nodes.get(e.key)!, scenes.get(e.key)!, pendPose[best]!, C, ms, now, nowMs, lastDraw.get(e.key), bs);
        nPend--;
        pendIdx[best] = pendIdx[nPend]!;
        pendPose[best] = pendPose[nPend]!;
        pendWait[best] = pendWait[nPend]!;
      }

      // ---- the overview, only when something it shows changed
      const mini = miniRef.current;
      if (miniDirty.current && mini) {
        miniDirty.current = false;
        const mc = ctxOf(mini);
        if (mc) {
          sizeCanvas(mini, MINI_W, MINI_H, dpr);
          mc.setTransform(dpr, 0, 0, dpr, 0, 0);
          // the same frame a press reads (currentMiniFrame), so a press lands
          // on the point drawn under it
          const placedNow = list.map((e) => nodes.get(e.key)).filter((n): n is MapNode => !!n && finiteXY(n));
          const statuses = miniStatusRef.current;
          drawMinimap(
            mc,
            C,
            placedNow,
            (key) => statuses.get(key) ?? "draft",
            viewRect(),
            currentMiniFrame(),
            { w: MINI_W, h: MINI_H },
          );
        }
      }

      // ---- on top: bubbles crossing between cells. Cleared only while one
      // is alive, and once after the last lands. Finished bubbles are dropped
      // by compacting the array in place.
      const over = overRef.current;
      const cross = crossRef.current;
      if (cross.length || overLive) {
        const oc = over ? ctxOf(over) : null;
        if (over && oc) {
          sizeCanvas(over, W, H, dpr);
          oc.setTransform(1, 0, 0, 1, 0, 0);
          oc.clearRect(0, 0, over.width, over.height);
          oc.setTransform(dpr * v.z, 0, 0, dpr * v.z, dpr * v.x, dpr * v.y);
          let keep = 0;
          for (let i = 0; i < cross.length; i++) {
            const m = cross[i]!;
            const sa = scenes.get(m.fromKey);
            const sb = scenes.get(m.toKey);
            const na = nodes.get(m.fromKey);
            const nb = nodes.get(m.toKey);
            if (!sa || !sb || !na || !nb || !finiteXY(na) || !finiteXY(nb)) continue;
            const alive = drawCrossMsg(oc, C, m, leadHead(sa, na, HEAD_A), leadHead(sb, nb, HEAD_B), now);
            if ((now - m.t0) / m.dur >= 1 && !m.landed) {
              m.landed = true;
              landCross(sb, now);
              lastDraw.delete(m.toKey);
            }
            if (alive) cross[keep++] = m;
          }
          cross.length = keep;
          overLive = keep > 0;
        } else {
          cross.length = 0;
          overLive = false;
        }
      }
    };
    id = raf(frame);
    return () => caf(id);
    // allMeasured is redefined per render but reads refs only, so the
    // closure captured here stays correct
  }, [hidden, nodes, scenes, canvasEls, lastDraw, settleToRest, placeArrivals, fit, viewRect, currentMiniFrame]);

  useEffect(
    () => () => {
      for (const t of [zoomTimer, persistTimer]) if (t.current !== null) window.clearTimeout(t.current);
    },
    [],
  );

  // ---- render
  const placed = phase.current !== "waiting";
  const order = stackOrder(shown.map((n) => (finiteXY(n) ? n : { ...n, y: 0 })));

  return (
    <main
      className="agent-map"
      id="agent-map"
      hidden={hidden}
      ref={rootRef}
      data-tool={tool ?? undefined}
      onPointerDown={onRootPointerDown}
      onMouseDown={onRootPointerDown}
      aria-label="agent map"
    >
      <canvas className="map-under" ref={underRef} aria-hidden="true" />
      <div className="map-world" ref={worldRef}>
        <div className="map-zoom" ref={zoomRef}>
          {entries.map(({ job, col, key, sig }) => {
            const n = nodes.get(key)!;
            const at = placed && finiteXY(n);
            return (
              <MapCard
                key={key}
                cardKey={key}
                job={job}
                sig={sig}
                col={col}
                actions={stableActions}
                boardNodeID={boardNodeID}
                x={at ? n.x : 0}
                y={at ? n.y : 0}
                z={order.get(key) ?? 1}
                placed={at}
                selected={selRef.current.has(key)}
                actors={actorsOn}
                cardRef={cardRefFor}
                canvasEls={canvasEls}
                onPointerDown={onCardPointerDown}
                onClickCapture={onCardClickCapture}
                onHover={onCardHover}
              />
            );
          })}
        </div>
      </div>
      <DrawLayer drawings={drawings} draft={draft} gRef={drawG} draftRef={setDraftEl} />
      {actorsOn && <canvas className="map-over" ref={overRef} aria-hidden="true" />}
      {/* the shift-drag marquee, only while one is in flight; MapView writes
          its rectangle straight to the element as the pointer moves */}
      {marqueeOn && <div id="map-marquee" ref={setMarqueeEl} aria-hidden="true" />}
      {/* while a tool is armed, every press on the map above the cards is
          the tool's */}
      {tool && (
        <div
          id="map-draw-surface"
          aria-hidden="true"
          onPointerDown={onSurfacePointerDown}
          onMouseDown={onSurfacePointerDown}
          onPointerMove={onSurfacePointerMove}
          onMouseMove={onSurfacePointerMove}
          onPointerLeave={onSurfaceLeave}
          onMouseLeave={onSurfaceLeave}
        />
      )}
      {editor && (
        <TextEditor key={editor.id} at={editor} inputRef={editorInput} textRef={editorText} onClose={closeEditor} />
      )}
      {/* the bottom-left corner: the drawing tools (while their panel is
          open), then the zoom buttons, above the overview, so the overview
          never covers them */}
      <div className="map-corner">
        {toolsOpen && (
          <div id="map-tools" className="map-tools" role="toolbar" aria-label="drawing tools">
            <div className="map-tool-row">
              {DRAW_TOOLS.map((t) => (
                <button
                  key={t}
                  type="button"
                  className="map-hud-btn map-tool"
                  data-tool={t}
                  aria-pressed={tool === t}
                  aria-label={TOOL_LABEL[t]}
                  title={TOOL_TITLE[t]}
                  onClick={() => armTool(toolRef.current === t ? null : t)}
                >
                  <ToolIcon>{TOOL_ICON[t]}</ToolIcon>
                </button>
              ))}
            </div>
            {/* the ink row: the theme's foreground first, then the eight
                job colours; the chosen one is what every new drawing takes */}
            <div className="map-inks" role="radiogroup" aria-label="drawing colour">
              {[null, ...DRAW_COLORS].map((c) => (
                <button
                  key={c ?? "ink"}
                  type="button"
                  className="map-ink"
                  role="radio"
                  data-color={c ?? undefined}
                  aria-checked={ink === c}
                  aria-label={c ?? "ink"}
                  title={c ? `draw in ${c}` : "draw in the theme's ink"}
                  onClick={() => chooseInk(c)}
                />
              ))}
            </div>
          </div>
        )}
        <div className="map-hud">
          {/* the column board's "+ NEW JOB", which the map has no draft
              column to carry: the same id, since only one of the two views
              is ever mounted, and the same editor in new-job mode */}
          <button
            type="button"
            className="map-hud-btn map-hud-new"
            id="new-job"
            title="draft a new job"
            onClick={() => actions.onOpenEditor(null)}
          >
            new job
          </button>
          <button
            type="button"
            className="map-hud-btn"
            title="zoom in"
            aria-label="zoom in"
            onClick={() => zoomAt(1.25, sizeRef.current.w / 2, sizeRef.current.h / 2)}
          >
            +
          </button>
          <button
            type="button"
            className="map-hud-btn"
            title="zoom out"
            aria-label="zoom out"
            onClick={() => zoomAt(0.8, sizeRef.current.w / 2, sizeRef.current.h / 2)}
          >
            −
          </button>
          <button type="button" className="map-hud-btn" title="fit every card in view" onClick={fit}>
            fit
          </button>
          <button
            type="button"
            className="map-hud-btn"
            aria-pressed={actorsOn}
            title={actorsOn ? "hide the characters" : "show the characters"}
            onClick={toggleActors}
          >
            dinos
          </button>
          {/* the drawing tools' panel switch: a pencil, filled while the
              panel above is open. aria-expanded rather than aria-pressed, so
              the struck-out look of a switched-off toggle does not apply */}
          <button
            type="button"
            className="map-hud-btn map-tools-toggle"
            id="map-tools-toggle"
            aria-expanded={toolsOpen}
            aria-controls="map-tools"
            aria-label="drawing tools"
            title={toolsOpen ? "hide the drawing tools" : "show the drawing tools"}
            onClick={toggleTools}
          >
            <ToolIcon>{TOOLS_TOGGLE_ICON}</ToolIcon>
          </button>
        </div>
        {entries.length > 0 && (
          <canvas
            id="map-minimap"
            ref={miniRef}
            role="img"
            aria-label="map overview"
            width={MINI_W}
            height={MINI_H}
            style={{ width: MINI_W, height: MINI_H }}
            onPointerDown={onMiniPointerDown}
            onMouseDown={onMiniPointerDown}
          />
        )}
      </div>
    </main>
  );
}

interface MapCardProps {
  cardKey: string;
  job: WebJob;
  /** The job's content signature; see Entry.sig. */
  sig: string;
  col: Column;
  actions: BoardActions;
  boardNodeID?: string;
  x: number;
  y: number;
  z: number;
  placed: boolean;
  /** Whether the card is in the selection (class `selected`, `data-selected="true"`). */
  selected: boolean;
  /** Whether the card carries its characters' canvas. */
  actors: boolean;
  cardRef: (key: string, el: HTMLDivElement | null) => void;
  canvasEls: Map<string, HTMLCanvasElement>;
  onPointerDown: (key: string, ev: ReactMouseEvent<HTMLDivElement>) => void;
  onClickCapture: (ev: ReactMouseEvent) => void;
  onHover: (key: string, on: boolean) => void;
}

/**
 * Skip a card's re-render unless something it shows changed. The job is
 * compared by its content signature, never by identity (a poll hands every
 * card a fresh object); everything else is compared as given.
 */
function sameCard(a: MapCardProps, b: MapCardProps): boolean {
  return (
    a.sig === b.sig &&
    a.cardKey === b.cardKey &&
    a.col === b.col &&
    a.actions === b.actions &&
    a.boardNodeID === b.boardNodeID &&
    a.x === b.x &&
    a.y === b.y &&
    a.z === b.z &&
    a.placed === b.placed &&
    a.selected === b.selected &&
    a.actors === b.actors &&
    a.cardRef === b.cardRef &&
    a.canvasEls === b.canvasEls &&
    a.onPointerDown === b.onPointerDown &&
    a.onClickCapture === b.onClickCapture &&
    a.onHover === b.onHover
  );
}

/**
 * One card on the map: the board's own JobCard (in map mode, so it is no
 * sortable, and carrying the activity band as its own last band) and, unless
 * the characters are switched off, its characters' canvas above it.
 * The status rides as `data-status`, which style.css turns into the card's
 * status colour.
 */
const MapCard = memo(function MapCard({
  cardKey,
  job,
  col,
  actions,
  boardNodeID,
  x,
  y,
  z,
  placed,
  selected,
  actors,
  cardRef,
  canvasEls,
  onPointerDown,
  onClickCapture,
  onHover,
}: MapCardProps) {
  const setCard = useCallback((el: HTMLDivElement | null) => cardRef(cardKey, el), [cardRef, cardKey]);
  const canvasRef = useCallback(
    (el: HTMLCanvasElement | null) => {
      if (el) canvasEls.set(cardKey, el);
      else canvasEls.delete(cardKey);
    },
    [canvasEls, cardKey],
  );
  const style: CSSProperties = {
    left: `${x}px`,
    top: `${y}px`,
    zIndex: z,
    ...(placed ? {} : { visibility: "hidden" }),
  };
  return (
    <div
      className={selected ? "map-card selected" : "map-card"}
      data-key={cardKey}
      data-status={mapStatus(job)}
      data-selected={selected ? "true" : undefined}
      style={style}
      ref={setCard}
      onPointerDown={(ev) => onPointerDown(cardKey, ev)}
      onMouseDown={(ev) => onPointerDown(cardKey, ev)}
      onClickCapture={onClickCapture}
      onPointerEnter={() => onHover(cardKey, true)}
      onPointerLeave={() => onHover(cardKey, false)}
    >
      {/* the activity band is inside the card (JobCard's map mode) */}
      <JobCard job={job} col={col} actions={actions} boardNodeID={boardNodeID} mapMode />
      {/* After the card, and lifted by style.css, so the characters paint IN
          FRONT of their own card's face; still inside this .map-card, so a
          card stacked in front covers both. It takes no pointer events. */}
      {actors && (
        <canvas
          className="map-actors"
          ref={canvasRef}
          aria-hidden="true"
          style={{ left: -PAD_X, top: -PAD_TOP, height: PAD_TOP + PAD_BOTTOM }}
        />
      )}
    </div>
  );
}, sameCard);

interface DrawLayerProps {
  drawings: Record<string, Drawing>;
  /** The shape being drawn, while its press is held: its element mounts here and MapView writes its geometry. */
  draft: { kind: ShapeKind; size: number; color?: DrawColor } | null;
  /** The group MapView's applyView puts the view on, as a transform. */
  gRef: RefObject<SVGGElement | null>;
  draftRef: (el: SVGElement | null) => void;
}

/**
 * The drawings layer: one element per drawing in ascending id order, then
 * the draft. Memoized, so a board poll (which re-renders MapView) leaves it
 * alone; it re-renders when a drawing is added or erased, or a draft starts
 * or ends, never per pointer move.
 */
const DrawLayer = memo(function DrawLayer({ drawings, draft, gRef, draftRef }: DrawLayerProps) {
  const ids = useMemo(() => Object.keys(drawings).sort(), [drawings]);
  let draftEl: ReactNode = null;
  if (draft) {
    const p = { "data-draft": "true", "data-color": draft.color, ref: draftRef, strokeWidth: draft.size };
    if (draft.kind === "pen") draftEl = <path {...p} />;
    else if (draft.kind === "line") draftEl = <line {...p} />;
    else if (draft.kind === "rect") draftEl = <rect {...p} />;
    else draftEl = <ellipse {...p} />;
  }
  return (
    <svg id="map-drawings" className="map-drawings" aria-hidden="true">
      <g ref={gRef}>
        {ids.map((id) => (
          <DrawingEl key={id} id={id} d={drawings[id]!} />
        ))}
        {draftEl}
      </g>
    </svg>
  );
});

/**
 * One saved drawing. Re-rendered only when its drawing object changes (they
 * are replaced, never mutated); its class is MapView's to write (the
 * eraser's `erase-hit`), so it sets none.
 */
const DrawingEl = memo(function DrawingEl({ id, d }: { id: string; d: Drawing }) {
  if (d.kind === "text") {
    const [x, y] = d.pts as [number, number];
    const lines = (d.text ?? "").split("\n");
    return (
      <text data-draw-id={id} data-kind="text" data-color={d.color} x={x} y={y} fontSize={d.size}>
        {lines.map((line, i) => (
          // each line's baseline sits where the editor's line box put it
          <tspan key={i} x={x} y={y + d.size * (1.25 * i + TEXT_BASELINE_EM)}>
            {line}
          </tspan>
        ))}
      </text>
    );
  }
  const p = {
    "data-draw-id": id,
    "data-kind": d.kind,
    "data-color": d.color,
    strokeWidth: d.size,
    ...shapeGeom(d.kind, d.pts),
  };
  if (d.kind === "pen") return <path {...p} />;
  if (d.kind === "line") return <line {...p} />;
  if (d.kind === "rect") return <rect {...p} />;
  return <ellipse {...p} />;
});

interface TextEditorProps {
  at: EditorAt;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  /** Kept at the text as of the last change, for a commit after the textarea is gone. */
  textRef: RefObject<string>;
  onClose: (id: number, value: string | null) => void;
}

/**
 * The text tool's editor, focused on mount. MapView's applyView places it
 * (left, top, font size) on its world point through the camera, so this
 * sets no position. Its text sits at the textarea's top-left corner with no
 * padding or border, in the face and line height the committed drawing is
 * drawn with, and it grows with what is typed. Enter (without Shift, and not
 * while an input method is composing) or a blur commits; Escape closes it
 * with nothing made, and goes no further, so the tool stays armed.
 */
function TextEditor({ at, inputRef, textRef, onClose }: TextEditorProps) {
  const [value, setValue] = useState("");
  const local = useRef<HTMLTextAreaElement | null>(null);
  useLayoutEffect(() => {
    local.current?.focus();
  }, []);
  const setEl = useCallback(
    (el: HTMLTextAreaElement | null) => {
      // the next editor's mount may already have taken the shared ref
      if (el) inputRef.current = el;
      else if (inputRef.current === local.current) inputRef.current = null;
      local.current = el;
    },
    [inputRef],
  );
  const onKeyDown = (ev: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // an Enter that confirms an input method's candidate is the method's
    const composing = ev.nativeEvent.isComposing || ev.keyCode === 229;
    if (ev.key === "Enter" && !ev.shiftKey && !composing) {
      ev.preventDefault();
      onClose(at.id, ev.currentTarget.value);
    } else if (ev.key === "Escape") {
      ev.preventDefault();
      ev.stopPropagation();
      onClose(at.id, null);
    }
  };
  const lines = value.split("\n");
  let cols = 0;
  for (const l of lines) cols = Math.max(cols, [...l].length);
  const style: CSSProperties = {
    width: `${cols + 2}ch`,
    height: `${lines.length * 1.25}em`,
  };
  return (
    <textarea
      id="map-text-input"
      ref={setEl}
      style={style}
      data-color={at.color ?? undefined}
      value={value}
      rows={1}
      spellCheck={false}
      aria-label="map text"
      onChange={(ev) => {
        textRef.current = ev.currentTarget.value;
        setValue(ev.currentTarget.value);
      }}
      onKeyDown={onKeyDown}
      onBlur={(ev) => onClose(at.id, ev.currentTarget.value)}
    />
  );
}
