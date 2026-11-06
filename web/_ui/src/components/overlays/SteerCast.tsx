import { useEffect, useMemo, useRef } from "react";

import type { WebJob } from "../../api/types.ts";
import { leadPose } from "../../lib/agentmap/activity.ts";
import { hueOf, readPalette } from "../../lib/agentmap/draw.ts";
import { actorsShown } from "../../lib/agentmap/prefs.ts";
import { drawScene, newScene, syncScene, type CardScene } from "../../lib/agentmap/scene.ts";
import { jobKey } from "../../lib/jobKey.ts";

/**
 * SteerCast — the job's characters at the top of the steer modal: the same
 * cast the agent map stands on the job's card (the lead in its model's coat,
 * the crew in their role hats, each acting out what it is doing, the bubbles
 * between them), drawn by the same scene code on one small canvas.
 *
 * The stage is a card's width whatever the modal's, so the cast stands as
 * close together as it does on the map, on a ledge in the job's status
 * colour where the map has the card's top edge. The canvas has a fixed size
 * (the stage plus room each side for a prop held past its end), so nothing
 * is measured, and the strip clips it where the modal is narrower.
 *
 * It follows the map's own switch: with the characters switched off there
 * (`dinos`), the modal shows none either. The switch is read when the modal
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

export function SteerCast({ job, open }: { job: WebJob; open: boolean }) {
  // read when the strip mounts, and again if `open` ever changes under it.
  // Today the overlay unmounts its body on close, so each open is a fresh
  // mount and reads the switch afresh.
  const shown = useMemo(actorsShown, [open]);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const sceneRef = useRef<CardScene | null>(null);
  const jobRef = useRef(job);
  jobRef.current = job;

  // the scene follows the job: a fresh one for another job, brought in line on every payload
  useEffect(() => {
    if (!shown || !open) return;
    const now = performance.now() / 1000;
    const key = jobKey(job);
    let s = sceneRef.current;
    if (!s || s.key !== key) {
      s = newScene(key, now);
      s.w = STAGE_W;
      sceneRef.current = s;
    }
    syncScene(s, job, now, peerKey, offMap);
  }, [job, shown, open]);

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
      const s = sceneRef.current;
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
      ctx.fillStyle = hueOf(C, s.status);
      ctx.fillRect(0, 1, STAGE_W, 2);
      const nowMs = Date.now();
      drawScene(ctx, s, leadPose(jobRef.current, nowMs), C, ms / 1000, nowMs, dt);
    };
    id = raf(frame);
    return () => {
      caf(id);
      mo?.disconnect();
    };
  }, [shown, open]);

  if (!shown) return null;
  const hover = (on: boolean) => {
    if (sceneRef.current) sceneRef.current.hover = on;
  };
  return (
    <div
      className="steer-cast"
      id="steer-cast"
      style={{ height: CSS_H }}
      onPointerEnter={() => hover(true)}
      onPointerLeave={() => hover(false)}
    >
      <canvas
        ref={canvasRef}
        role="img"
        aria-label={`${job.slug}: its characters`}
        style={{ width: CSS_W, height: CSS_H }}
      />
    </div>
  );
}
