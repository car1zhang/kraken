import { useCallback, useEffect, useRef, useState } from "react";

import { requestUpdate as postUpdate, fetchUpdateStatus } from "../../api/client.ts";
import type { WebUpdate } from "../../api/types.ts";
import { useLocalStorage } from "../../hooks/useLocalStorage.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { age } from "../../lib/format.ts";
import type { BannerModel } from "./Banners.tsx";

// ---- the ⟳ UPDATE control (internal/api/update.go) ------------------------
//
// The button is a view over one server record — board.update, and GET
// /api/update/status, which are the same webUpdate shape — plus two facts only
// the browser knows: whether THIS page watched a run start (so only a page
// that did reloads itself when it ends) and the last refusal, which no server
// record carries because a refused update never happened.
//
// While a run is live the button polls /api/update/status every second rather
// than riding the 3s board poll, and it polls with failures SWALLOWED. That is
// the whole trick: an update's last act is restarting the server that answers
// these requests, so for a couple of seconds every fetch fails. That gap is
// the normal middle of a SUCCESSFUL update — the shared mutation path, which
// reports any failure, would call it an error and be wrong every single run.

/** jobs.UpdateTerminal: no run is in progress, so the button is live again. */
export const UPD_TERMINAL = new Set(["idle", "done", "failed", "uptodate"]);
// How long a finished run keeps the button. ~/.kraken/update.json holds its
// last record forever, so without a window every page load for the rest of
// time would reopen a green ✓ UPDATED — or a pink failure — from days ago.
const UPD_RECENT_MS = 60000;
// Past this, a restart that hasn't come back has stopped being a restart.
const UPD_GAP_MAX_MS = 120000;
const UPD_POLL_MS = 1000;
const UPD_RELOAD_MS = 1500;

/** One painted `.upd` control: the same six fields paintUpdControl took. */
export interface UpdButtonModel {
  /** "" | "busy" | "gap" | "ok" | "bad" | "avail". */
  cls: string;
  /** "gly" for a glyph, "dot" for the availability pip. */
  glyphCls: string;
  glyphChar: string;
  label: string;
  disabled: boolean;
  title: string;
}

/**
 * One `.upd` button, shared by the update and ship controls — they are
 * deliberately one widget with two records behind it: same classes, same
 * glyph/label grammar, only the state machine that decides what to paint
 * differs.
 *
 * The vanilla painter wrote this node-by-node, and only where something had
 * actually changed, because the in-flight indicator (now `.upd .spinner`, in
 * vanilla the glyph itself) spins on a 1.1s keyframe while its caller
 * repaints on BOTH the 1s status poll and the 3s board poll: reassigning
 * innerHTML replaced the element even for identical markup, which restarted
 * the animation from 0° about once a second, and the spinner never once
 * completed a turn during a build. React's reconciler keeps the same DOM
 * nodes and only touches changed attributes, which is exactly what that
 * hand-rolled differ was for — so there is no differ here, and adding one
 * back would be re-solving a solved problem.
 */
export function UpdControlButton({
  id,
  model,
  onClick,
}: {
  id: string;
  model: UpdButtonModel;
  onClick(): void;
}) {
  return (
    <button
      className={"upd" + (model.cls ? " " + model.cls : "")}
      id={id}
      disabled={model.disabled}
      title={model.title}
      onClick={onClick}
    >
      <span className={model.glyphCls}>{model.glyphChar}</span>
      {model.label}
    </button>
  );
}

export interface UpdateRunOptions {
  /** board.update — absent on an older server, which reads as plain idle. */
  record: WebUpdate | undefined;
  /** Navigate to the LOG view (a fresh failure's button is the door to it). */
  onOpenLog(): void;
  onToast(message: string): void;
  /**
   * Flushed before any reload. The editor autosave is 600ms of debounce away
   * from the server at all times and location.reload() drops it on the floor.
   */
  onBeforeReload?(): Promise<void> | void;
}

export interface UpdateRun {
  button: UpdButtonModel;
  banner: BannerModel | null;
  onClick(): void;
  onBannerAction(act: string): void;
  /** The header's `@sha` chip. "" when unstamped (a bare `go build`). */
  buildChip: string;
  /** True while the server is expected to be down: "daemon — restarting". */
  restarting: boolean;
  /** True while any update run is in flight — the ship poll's fail ceiling. */
  updating: boolean;
}

/**
 * The update state machine. Port of renderUpdate/updIngest/updPollTick
 * (app.js:4006-4420).
 */
export function useUpdateRun({
  record,
  onOpenLog,
  onToast,
  onBeforeReload,
}: UpdateRunOptions): UpdateRun {
  const [state, setState] = useState<WebUpdate | null>(null);
  const [polling, setPolling] = useState(false);
  const [refusal, setRefusal] = useState("");
  // When it was set. A refusal has no server record to age out with, and left
  // unbounded it is the FIRST paint branch and therefore outranks every other
  // one forever — masking a `behind` count, and any done/failed record the CLI
  // or another tab produces in the meantime. The ship control already bounds
  // its identical construct; this is the same bound.
  const [refusalAt, setRefusalAt] = useState(0);
  const [starting, setStarting] = useState(false);
  const [reloading, setReloading] = useState(false);
  // The restart gap: ms of this gap's first failed fetch, 0 while the server is
  // answering. STATE, not a ref, because the paint reads it — a ref read during
  // render is not reactive, so a render triggered by anything other than a poll
  // tick could paint a stale gap. It changes at most once per gap, so the extra
  // render costs nothing. The ref beside it is the event-time mirror.
  const [gapSince, setGapSince] = useState(0);
  const [dismissed, setDismissed] = useLocalStorage("kraken-dismissed-upd", "");

  // Refs rather than state: every one of these is read at event time, and none
  // of them should itself cause a paint. A poll tick always re-renders (new
  // data object, or a bumped failStreak), so the render below never reads a
  // stale one.
  const watchingRef = useRef(false); // this page saw a run start, so it owes itself a reload
  const reloadTimerRef = useRef<number | null>(null);
  const gapSinceRef = useRef(0); // event-time mirror of gapSince
  const failBaseRef = useRef(0); // failures to ignore — see the visibilitychange reset
  const stateRef = useRef<WebUpdate | null>(null);
  stateRef.current = state;
  const cbRef = useRef({ onOpenLog, onToast, onBeforeReload });
  cbRef.current = { onOpenLog, onToast, onBeforeReload };

  const setGap = useCallback((v: number) => {
    gapSinceRef.current = v;
    setGapSince(v);
  }, []);

  const { data: polled, failStreak } = usePoll(fetchUpdateStatus, UPD_POLL_MS, {
    enabled: polling,
  });
  const failStreakRef = useRef(0);
  failStreakRef.current = failStreak;

  /**
   * The post-update reload is MANDATORY, not cosmetic: the SPA is embedded in
   * the binary, so the moment the new `kraken serve` is up this page is
   * running the OLD build's bundle against the NEW server — its markup, its
   * CSS and its idea of every API shape are all one version behind. Only a
   * page that WATCHED the run start reloads: a page opened afterwards reads
   * the same `done` record, and reloading on it would loop forever.
   */
  const scheduleReload = useCallback(() => {
    if (reloadTimerRef.current || !watchingRef.current) return;
    watchingRef.current = false;
    setReloading(true);
    reloadTimerRef.current = window.setTimeout(() => {
      void (async () => {
        await cbRef.current.onBeforeReload?.();
        location.reload();
      })();
    }, UPD_RELOAD_MS);
  }, []);

  useEffect(
    () => () => {
      if (reloadTimerRef.current) window.clearTimeout(reloadTimerRef.current);
    },
    [],
  );

  // updIngest: a fresh record from either poll. Reload scheduling lives here
  // rather than in the render so that repainting the button is never itself an
  // event.
  const ingest = useCallback(
    (u: WebUpdate | null | undefined) => {
      if (!u) return;
      setState(u);
      setGap(0);
      if (u.phase === "done") scheduleReload();
    },
    [scheduleReload, setGap],
  );

  // The 1s poll's record.
  useEffect(() => {
    if (!polled) return;
    ingest(polled);
    if (UPD_TERMINAL.has(polled.phase ?? "idle")) setPolling(false);
  }, [polled, ingest]);

  // The restart gap, and the point past which it stops being one.
  useEffect(() => {
    if (failStreak === 0) {
      failBaseRef.current = 0;
      setGap(0);
      return;
    }
    if (failStreak <= failBaseRef.current) return; // inside an ignored prefix
    if (!gapSinceRef.current) setGap(Date.now());
    // Two resets (poll start, tab becoming visible) aren't enough on their own,
    // because a freeze can land between them and the next tick. The ceiling
    // additionally needs two failures BACK TO BACK: at 1s a real two-minute
    // outage racks up a hundred of them, while a single blip on unfreeze is
    // followed 1s later by a success that clears the gap.
    if (
      failStreak - failBaseRef.current >= 2 &&
      Date.now() - gapSinceRef.current > UPD_GAP_MAX_MS
    ) {
      setState((prev) => ({
        ...(prev ?? {}),
        phase: "failed",
        error:
          "kraken serve did not come back after the restart — see ~/.kraken/update.log",
        updated_at: Date.now(),
      }));
      setPolling(false);
    }
  }, [failStreak, setGap]);

  // Chrome freezes hidden tabs, and the gap clock is wall-clock: hiding the
  // board through a restart would let the first tick back measure the whole
  // hidden stretch and synthesize "kraken serve did not come back" for a run
  // that finished while nobody was looking. Time spent hidden doesn't count.
  useEffect(() => {
    const onVis = () => {
      if (document.hidden) return;
      setGap(0);
      failBaseRef.current = failStreakRef.current;
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [setGap]);

  // The 3s board poll's copy of the record. The 1s status poll wins outright
  // while it is running: the two race, and a board response can easily be
  // older than a status response that landed just before it — applying it
  // would rewind the phase on screen.
  useEffect(() => {
    if (polling || !record) return;
    if (!UPD_TERMINAL.has(record.phase ?? "idle")) {
      // A run someone else started — the CLI, another tab. This page holds the
      // old build's bundle just the same, so it wants the reload just as much
      // as the tab that clicked.
      watchingRef.current = true;
      setRefusal(""); // whatever we were refused, something is running now
      setRefusalAt(0);
      setPolling(true);
    }
    ingest(record);
  }, [record, polling, ingest]);

  // requestUpdate fires the run. No confirmation step: the click IS the
  // decision. Deliberately not the shared mutation path — a refusal here is
  // one specific sentence about the machine's state ("… has uncommitted
  // changes", "… is on carl/foo, not main"), and the shared path's ✕ in the
  // clock corner truncates exactly the part worth reading.
  const start = useCallback(async () => {
    setRefusal("");
    setRefusalAt(0);
    setStarting(true); // no double-fire while the POST is in flight
    try {
      const data = await postUpdate();
      watchingRef.current = true;
      // The handler writes its claim (phase preflight) before spawning, so
      // paint it now rather than waiting out the first poll tick.
      ingest({
        ...(stateRef.current ?? {}),
        phase: data.phase || "preflight",
        from_sha: data.from_sha || "",
        to_sha: "",
        error: "",
        started_at: Date.now(),
        updated_at: Date.now(),
      });
      setPolling(true);
    } catch (e) {
      // String(e) is load-bearing: a catch binding is `unknown`, and a thrown
      // non-Error (a string, a rejected fetch's DOMException) has to become one.
      const msg = (e instanceof Error ? e.message : String(e)) || "update refused";
      setRefusal(msg);
      setRefusalAt(Date.now());
      cbRef.current.onToast("✕ " + msg);
    }
    setStarting(false);
  }, [ingest]);

  // ---- paint ------------------------------------------------------------
  const u = state ?? {};
  const phase = u.phase ?? "idle";
  const running = !UPD_TERMINAL.has(phase);
  // A terminal record only speaks for the button while it's fresh — bounded on
  // BOTH sides. An `updated_at` in the future (clock skew, a corrupt
  // update.json) is otherwise fresh forever, and a `failed` record that is
  // fresh forever pins the button at UPDATE FAILED, whose click opens the log
  // instead of starting a run — so the button would never update again.
  const updatedAt = u.updated_at ?? 0;
  const startedAt = u.started_at ?? 0;
  const fresh = !!updatedAt && Math.abs(Date.now() - updatedAt) < UPD_RECENT_MS;
  const buildSha = u.build_sha ?? "";
  const build = buildSha ? " · running " + buildSha : "";
  const behind = u.behind ?? 0;
  const checkedAt = u.checked_at ?? 0;
  const errText = u.error ?? "";

  let cls = "";
  let glyphCls = "gly";
  let glyphChar = "⟳";
  let label = "UPDATE";
  let disabled = false;
  let title =
    "update kraken — fast-forward main, rebuild, reinstall, restart the daemon" + build;
  let banner: BannerModel | null = null;
  let action: "update" | "log" = "update";

  if (refusal && Date.now() - refusalAt < UPD_RECENT_MS) {
    // Refused before anything was spawned (409 from the POST, already
    // toasted): nothing ran, so the record behind it is untouched and the
    // button stays clickable for the retry once the reason is fixed.
    cls = "bad";
    glyphChar = "✕";
    label = "UPDATE BLOCKED";
    title = refusal;
  } else if (running && (gapSince || phase === "restarting")) {
    // Either the updater says it's restarting, or the endpoint stopped
    // answering mid-run — which IS the restart, seen from this side.
    cls = "gap";
    glyphCls = "spinner";
    glyphChar = "";
    label = "RESTARTING…";
    disabled = true;
    banner = {
      cls: "cyan",
      text: "⟳ kraken serve is restarting on the new binary — board data is stale for a few seconds. Cells are unaffected.",
      stamp: updatedAt,
    };
  } else if (running) {
    const secs = startedAt ? Math.max(0, Math.round((Date.now() - startedAt) / 1000)) : 0;
    cls = "busy";
    glyphCls = "spinner";
    glyphChar = "";
    label = `${phase.toUpperCase()}… ${secs}s`;
    disabled = true;
  } else if (phase === "failed" && fresh) {
    // A failure after the updater detached can't come back as an HTTP status —
    // the request returned long ago — so it arrives here, in the record.
    cls = "bad";
    glyphChar = "✕";
    label = "UPDATE FAILED";
    title = errText || "update failed";
    action = "log";
    banner = {
      cls: "bad",
      text: `✕ update failed — ${errText || "no error recorded"}`,
      action: { act: "log", label: "OPEN LOG" },
      stamp: updatedAt,
    };
  } else if (phase === "done" && fresh) {
    const secs =
      startedAt && updatedAt ? Math.max(0, Math.round((updatedAt - startedAt) / 1000)) : 0;
    cls = "ok";
    glyphChar = "✓";
    label = ("UPDATED " + (u.to_sha || "")).trim();
    // Disabled only while THIS page is on its way out — a page that merely
    // reads a recent `done` (the one that just reloaded, say) stays usable.
    disabled = reloading;
    banner = {
      cls: "",
      text:
        `✓ updated ${u.from_sha || "?"} → ${u.to_sha || "?"}` +
        (secs ? ` in ${secs}s` : "") +
        (reloading ? " · reloading…" : ""),
      action: { act: "reload", label: "RELOAD NOW" },
      stamp: updatedAt,
    };
  } else if (phase === "uptodate" && fresh) {
    // A run that found nothing to do built and restarted nothing. Without this
    // the click would flash busy and land back on a bare UPDATE, which reads
    // as "did that even do anything".
    cls = "ok";
    glyphChar = "✓";
    label = "UP TO DATE";
  } else if (behind > 0) {
    // The daemon's background check (jobs.refreshUpdateAvail, every 10min).
    cls = "avail";
    glyphCls = "dot";
    glyphChar = "";
    label = `UPDATE · ${behind} BEHIND`;
    const checked = checkedAt ? ", checked " + age(Math.floor(checkedAt / 1000)) + " ago" : "";
    title = `origin/main is ${behind} commit(s) ahead${checked}${build}`;
  }

  // String(stamp) here and below is a real number→string conversion, not a
  // narrowing: the dismissal is stored under the run's `updated_at` and
  // localStorage only holds strings.
  if (banner && banner.stamp && dismissed === String(banner.stamp)) banner = null;

  const onBannerAction = useCallback(
    (act: string) => {
      if (act === "reload") {
        void (async () => {
          await cbRef.current.onBeforeReload?.();
          location.reload();
        })();
        return;
      }
      if (act === "log") cbRef.current.onOpenLog();
      if (act === "dismiss") {
        const stamp = stateRef.current?.updated_at;
        if (stamp) setDismissed(String(stamp));
      }
    },
    [setDismissed],
  );

  return {
    button: { cls, glyphCls, glyphChar, label, disabled: disabled || starting, title },
    banner,
    // A fresh failure's button is the way into the log that explains it; the
    // failure ages out after UPD_RECENT_MS and it is a plain UPDATE again.
    onClick: () => (action === "log" ? cbRef.current.onOpenLog() : void start()),
    onBannerAction,
    buildChip: buildSha && buildSha !== "dev" ? "@" + buildSha : "",
    restarting: polling,
    updating: polling || running,
  };
}
