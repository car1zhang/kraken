import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type MouseEvent as ReactMouseEvent,
} from "react";

import { fetchMapPositions, saveMapPositions } from "../../api/client.ts";
import type { WebJob } from "../../api/types.ts";
import type { JobSearchModel } from "../../hooks/useJobSearch.ts";
import { readLS, writeLS } from "../../hooks/useLocalStorage.ts";
import { shouldStartDrag } from "../../hooks/useDragReorder.ts";
import { leadPose, mapStatus } from "../../lib/agentmap/activity.ts";
import { readPalette, type Palette } from "../../lib/agentmap/draw.ts";
import { freeSpot, overlaps, seedLayout, settle, stackOrder, type MapNode } from "../../lib/agentmap/layout.ts";
import { drawMinimap, miniFrame, miniToWorld, type MiniFrame } from "../../lib/agentmap/minimap.ts";
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
 * and a working lead in think or wait) moves gently enough for 12. A sleeper,
 * an egg or a spent lead reads the same at four. A busy card (a bubble in
 * flight, crew walking, the pointer on it) draws at the fast rate whatever
 * its pose.
 */
const FAST_MS = 33;
const HELD_MS = 84;
const SLOW_MS = 250;
const HELD_POSES = new Set(["blocked", "question", "think", "wait"]);
const SLOW_POSES = new Set(["parked", "limit", "done", "draft"]);
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
 */
let pointerSeen = false;
/** Whether a gesture event should be handled at all. */
function gestureEvent(ev: { type: string; clientX?: number }): boolean {
  if (!ev.type.startsWith("mouse")) {
    // pointerup/cancel carry coordinates too in a browser; a coordinate-less
    // one ends nothing that a coordinate-less down could have started
    if (typeof ev.clientX !== "number" || !Number.isFinite(ev.clientX)) return false;
    pointerSeen = true;
    return true;
  }
  return !pointerSeen;
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

type Drag =
  | { kind: "card"; key: string; sx: number; sy: number; ox: number; oy: number; moved: boolean }
  | { kind: "pan"; sx: number; sy: number; ox: number; oy: number }
  /** a press on the overview: its frame and page offset, held for the whole drag */
  | { kind: "mini"; left: number; top: number; f: MiniFrame };

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
 * press or drag on it centres the view on the point under the pointer.
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

  // Seed every unplaced card once the saved positions are in (or the load is
  // slow enough to place provisionally). The seeds are saved by the effect
  // below, once saving is allowed.
  const seededNow: string[] = [];
  if (phase.current !== "waiting" && shown.some((n) => !finiteXY(n))) {
    const before = new Set(shown.filter(finiteXY).map((n) => n.key));
    seedLayout(shown, spawnLinks);
    for (const n of shown) if (!before.has(n.key)) seededNow.push(n.key);
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
   */
  const settleToRest = useCallback(
    (held?: string) => {
      settleMore.current = null;
      const cur = entriesRef.current
        .map((e) => nodes.get(e.key))
        .filter((n): n is MapNode => !!n && finiteXY(n));
      if (cur.length < 2) return;
      const before = new Map(cur.map((n) => [n.key, [n.x, n.y] as const]));
      let still = true;
      for (let i = 0; i < SETTLE_STEPS && still; i++) still = settle(cur, held);
      if (still) settleMore.current = { held };
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
      n.h = nh;
      if (w > 0) n.w = w;
      if (changed) {
        // an arriving card's size is part of its arrival (it yields when it
        // is placed); only a showing card's change moves others
        if (!arriving.has(key)) settleWanted.current = true;
        underDirty.current = true;
        miniDirty.current = true;
        lastDraw.delete(key);
      }
    },
    [nodes, measured, lastDraw, arriving, measuredAtZ, rawH],
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
    // a second finger never takes over a card drag: the drop must still be
    // written, settled and saved
    if (dragRef.current?.kind === "card") return;
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

  // ---- pointer: drag a card, or pan the background
  useEffect(() => {
    const onMove = (ev: MouseEvent) => {
      const d = dragRef.current;
      if (!d) return;
      if (d.kind === "mini") {
        const [wx, wy] = miniToWorld(num(ev.clientX) - d.left, num(ev.clientY) - d.top, d.f);
        centreOn(wx, wy);
        return;
      }
      const dx = num(ev.clientX) - d.sx;
      const dy = num(ev.clientY) - d.sy;
      if (d.kind === "pan") {
        viewRef.current.x = d.ox + dx;
        viewRef.current.y = d.oy + dy;
        applyView();
        return;
      }
      if (Math.abs(dx) + Math.abs(dy) > 4) d.moved = true;
      if (!d.moved) return;
      const node = nodes.get(d.key);
      if (!node) return;
      const z = viewRef.current.z;
      node.x = d.ox + dx / z;
      node.y = d.oy + dy / z;
      // the dragged card only: nothing is pushed while it is in the air
      const el = cardEls.get(d.key);
      if (el) {
        el.style.left = `${node.x}px`;
        el.style.top = `${node.y}px`;
      }
      underDirty.current = true;
      miniDirty.current = true;
    };
    const onUp = () => {
      const d = dragRef.current;
      if (!d) return;
      dragRef.current = null;
      rootRef.current?.classList.remove("panning");
      if (d.kind === "pan" || d.kind === "mini") {
        persistView();
        return;
      }
      rootRef.current?.classList.remove("dragging");
      if (d.moved) {
        // the click that ends this gesture is not a click on the card
        suppressClick.current = true;
        if (phase.current !== "final") draggedEarly.add(d.key);
        markDirty([d.key]);
        writePositions([d.key]);
        // the drop: the cards it landed on make way, the dropped one holds
        settleToRest(d.key);
        scheduleSave();
        rerender();
      }
    };
    const move = (ev: MouseEvent) => {
      if (gestureEvent(ev)) onMove(ev);
    };
    const up = (ev: Event) => {
      if (gestureEvent(ev)) onUp();
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
  ]);

  const onCardPointerDown = useCallback(
    (key: string, ev: ReactMouseEvent<HTMLDivElement>) => {
      if (!gestureEvent(ev)) return;
      if (ev.button !== undefined && ev.button !== 0) return;
      suppressClick.current = false;
      // a press on a control inside the card is that control's, never a drag
      if (!shouldStartDrag(ev.target)) return;
      const node = nodes.get(key);
      if (!node || !finiteXY(node)) return;
      ev.stopPropagation();
      dragRef.current = {
        kind: "card",
        key,
        sx: num(ev.clientX),
        sy: num(ev.clientY),
        ox: node.x,
        oy: node.y,
        moved: false,
      };
      rootRef.current?.classList.add("dragging");
    },
    [nodes],
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
    if (t && typeof t.closest === "function" && t.closest(".map-card, .map-corner")) return;
    // a second finger never takes over a card drag (see onMiniPointerDown)
    if (dragRef.current?.kind === "card") return;
    const v = viewRef.current;
    dragRef.current = { kind: "pan", sx: num(ev.clientX), sy: num(ev.clientY), ox: v.x, oy: v.y };
    rootRef.current?.classList.add("panning");
  };

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
      <canvas className="map-over" ref={overRef} aria-hidden="true" />
      {/* the bottom-left corner: the zoom buttons above the overview, so
          the overview never covers them */}
      <div className="map-corner">
        <div className="map-hud">
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
    a.cardRef === b.cardRef &&
    a.canvasEls === b.canvasEls &&
    a.onPointerDown === b.onPointerDown &&
    a.onClickCapture === b.onClickCapture &&
    a.onHover === b.onHover
  );
}

/**
 * One card on the map: the board's own JobCard (in map mode, so it is no
 * sortable, and carrying the activity band as its own last band) and its
 * characters' canvas above it.
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
      className="map-card"
      data-key={cardKey}
      data-status={mapStatus(job)}
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
      <canvas
        className="map-actors"
        ref={canvasRef}
        aria-hidden="true"
        style={{ left: -PAD_X, top: -PAD_TOP, height: PAD_TOP + PAD_BOTTOM }}
      />
    </div>
  );
}, sameCard);
