import { useEffect, useMemo, useRef } from "react";

import type { WebJob, WebThread } from "../../api/types.ts";
import { leadPose } from "../../lib/agentmap/activity.ts";
import { threadBranch, threadCoat } from "../../lib/agentmap/avatar.ts";
import { hueOf, readPalette, type Palette } from "../../lib/agentmap/draw.ts";
import { actorsShown } from "../../lib/agentmap/prefs.ts";
import {
  branchPose,
  drawScene,
  isGirl,
  newScene,
  syncScene,
  threadActorName,
  type CardScene,
} from "../../lib/agentmap/scene.ts";
import { jobKey } from "../../lib/jobKey.ts";

/**
 * The characters in the modals, drawn by the agent map's own scene code on
 * one small canvas each:
 *
 * - `SteerCast`, at the top of the steer modal: the job's cast as the map
 *   stands it on the job's card (the lead in its model's coat, the crew in
 *   their role hats, the job's branches with their twigs, each acting out
 *   what it is doing, the bubbles between them).
 * - `ThreadCast`, at the top of the branch modal: that one branch alone, at
 *   the lead's size.
 *
 * The stage is a card's width whatever the modal's, so a cast stands as
 * close together as it does on the map, on a ledge where the map has the
 * card's top edge: the job's status colour in the steer modal, the branch's
 * state in the branch modal. The canvas has a fixed size (the stage plus
 * room each side for a prop held past its end), so nothing is measured, and
 * the strip clips it where the modal is narrower.
 *
 * Both follow the map's own switch: with the characters switched off there
 * (`dinos`), the modals show none either. The switch is read when a modal
 * opens. The loop runs only while the modal is open and the page visible,
 * and where a canvas has no 2D context the strip stays empty.
 */

/** The stage's width: a map card's. */
const STAGE_W = 300;
// Room around the stage: either side (a prop held past its end, a name tag),
// above (props, effects, a message dropping in) and below (dangling feet).
const PAD_X = 64;
const PAD_TOP = 96;
const PAD_BOTTOM = 14;
const CSS_W = STAGE_W + PAD_X * 2;
const CSS_H = PAD_TOP + PAD_BOTTOM;
/** At most this many backing pixels per CSS pixel. */
const MAX_BACKING = 2;
/** Redraw no more often than this: a 30-a-second cadence, the map's for a card in motion. */
const FRAME_MS = 33;

const raf: (cb: FrameRequestCallback) => number =
  typeof requestAnimationFrame === "function"
    ? (cb) => requestAnimationFrame(cb)
    : (cb) => window.setTimeout(() => cb(performance.now()), FRAME_MS);
const caf: (id: number) => void =
  typeof cancelAnimationFrame === "function" ? (id) => cancelAnimationFrame(id) : (id) => window.clearTimeout(id);

/** `syncScene`'s peer key: the modal shows one card, so a peer's slug serves as its key. */
const peerKey = (slug: string) => slug;
/** No other card is in the modal: a message from another cell drops in from a tag, as it does for a card off the map. */
const offMap = () => false;

/** What a strip draws each frame: its scene, the ledge's colour, and the lead's pose. */
interface CastSource {
  scene(): CardScene | null;
  ledge(C: Palette, s: CardScene): string;
  pose(nowMs: number): string;
}

/**
 * One strip: the canvas, its frame loop and the switch. `source` is read on
 * every frame through a ref, so a caller may hand a fresh object each render
 * without restarting the loop.
 */
function CastStrip({
  id,
  label,
  open,
  source,
  onHover,
}: {
  id: string;
  label: string;
  open: boolean;
  source: CastSource;
  onHover?: (over: boolean) => void;
}) {
  // read when the strip mounts, and again if `open` ever changes under it.
  // Today the overlay unmounts its body on close, so each open is a fresh
  // mount and reads the switch afresh.
  const shown = useMemo(actorsShown, [open]);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const sourceRef = useRef(source);
  sourceRef.current = source;

  useEffect(() => {
    if (!shown || !open) return;
    const cv = canvasRef.current;
    if (!cv) return;
    let c: CanvasRenderingContext2D | null = null;
    try {
      c = cv.getContext("2d");
    } catch {
      c = null;
    }
    if (!c) return;
    const ctx = c;
    let C = readPalette();
    const mo = typeof MutationObserver === "undefined" ? null : new MutationObserver(() => (C = readPalette()));
    mo?.observe(document.documentElement, { attributes: true });
    let last: number | undefined;
    let id = 0;
    const frame = (ms: number) => {
      id = raf(frame);
      const src = sourceRef.current;
      const s = src.scene();
      if (!s || document.hidden) return;
      if (last !== undefined && ms - last < FRAME_MS) return;
      const dt = last === undefined ? 0 : Math.min(0.25, (ms - last) / 1000);
      last = ms;
      const bs = Math.min(MAX_BACKING, window.devicePixelRatio || 1);
      const w = Math.round(CSS_W * bs);
      const h = Math.round(CSS_H * bs);
      if (cv.width !== w || cv.height !== h) {
        cv.width = w;
        cv.height = h;
      }
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.clearRect(0, 0, w, h);
      ctx.setTransform(bs, 0, 0, bs, PAD_X * bs, PAD_TOP * bs);
      // the ledge the cast stands on, where the map has the card's top edge
      ctx.fillStyle = src.ledge(C, s);
      ctx.fillRect(0, 1, STAGE_W, 2);
      const nowMs = Date.now();
      drawScene(ctx, s, src.pose(nowMs), C, ms / 1000, nowMs, dt);
    };
    id = raf(frame);
    return () => {
      caf(id);
      mo?.disconnect();
    };
  }, [shown, open]);

  if (!shown) return null;
  return (
    <div
      className="steer-cast"
      id={id}
      style={{ height: CSS_H }}
      onPointerEnter={onHover ? () => onHover(true) : undefined}
      onPointerLeave={onHover ? () => onHover(false) : undefined}
    >
      <canvas ref={canvasRef} role="img" aria-label={label} style={{ width: CSS_W, height: CSS_H }} />
    </div>
  );
}

/** The job's whole cast, in the steer modal. Hovering the strip shows the crew's and branches' names. */
export function SteerCast({ job, open }: { job: WebJob; open: boolean }) {
  const sceneRef = useRef<CardScene | null>(null);
  const jobRef = useRef(job);
  jobRef.current = job;

  // the scene follows the job: a fresh one for another job, brought in line on every payload
  useEffect(() => {
    if (!open) return;
    const now = performance.now() / 1000;
    const key = jobKey(job);
    let s = sceneRef.current;
    if (!s || s.key !== key) {
      s = newScene(key, now);
      s.w = STAGE_W;
      sceneRef.current = s;
    }
    syncScene(s, job, now, peerKey, offMap);
  }, [job, open]);

  return (
    <CastStrip
      id="steer-cast"
      label={`${job.slug}: its characters`}
      open={open}
      source={{
        scene: () => sceneRef.current,
        ledge: (C, s) => hueOf(C, s.status),
        pose: (nowMs) => leadPose(jobRef.current, nowMs),
      }}
      onHover={(over) => {
        if (sceneRef.current) sceneRef.current.hover = over;
      }}
    />
  );
}

/**
 * One branch alone, in the branch modal: the side thread's own character at
 * the lead's size, in its model's coat with its twig (a ghost when it is
 * headless), on a ledge in its state's colour: the working colour while a
 * question is being answered, green while it is live, red once it is closed.
 * It thinks while answering and otherwise stands idle.
 */
export function ThreadCast({ thread, open }: { thread: WebThread; open: boolean }) {
  const sceneRef = useRef<CardScene | null>(null);
  const threadRef = useRef(thread);
  threadRef.current = thread;

  // one scene per thread, its lead dressed as the branch on every payload
  useEffect(() => {
    if (!open) return;
    const key = threadActorName(thread);
    let s = sceneRef.current;
    if (!s || s.key !== key) {
      s = newScene(key, performance.now() / 1000);
      s.w = STAGE_W;
      sceneRef.current = s;
    }
    s.lead.branch = threadBranch(thread);
    s.lead.coat = threadCoat(thread);
    s.lead.girl = isGirl(key);
  }, [thread, open]);

  return (
    <CastStrip
      id="thread-cast"
      label={`branch ${thread.title || thread.tid}: its character`}
      open={open}
      source={{
        scene: () => sceneRef.current,
        ledge: (C) => {
          const t = threadRef.current;
          return t.state !== "live" ? C.bad : t.answering ? C.ac : C.ok;
        },
        pose: () => branchPose(threadRef.current),
      }}
    />
  );
}
