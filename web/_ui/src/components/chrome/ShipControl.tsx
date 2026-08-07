import { useCallback, useEffect, useRef, useState } from "react";

import { fetchShipStatus, requestShipIOS as postShip } from "../../api/client.ts";
import type { WebShipIOS } from "../../api/types.ts";
import { useLocalStorage } from "../../hooks/useLocalStorage.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import type { BannerModel } from "./Banners.tsx";
import { UpdControlButton, type UpdButtonModel } from "./UpdateControl.tsx";

/**
 * The ⇧ button itself. A thin alias for the shared `.upd` painter: the two
 * controls are ONE widget with two records behind it, and giving ship its own
 * markup is exactly how they would drift apart.
 */
export function ShipControl(props: { id: string; model: UpdButtonModel; onClick(): void }) {
  return <UpdControlButton {...props} />;
}

// ---- the ⇧ SHIP iOS control (internal/api/shipios.go) ---------------------
//
// Structurally the update button's twin — same `.upd` classes, the same
// button/banner painters, the same "one server record plus the one refusal no
// record can carry" shape — with two deliberate differences that are the
// reason this is a separate state machine rather than a second caller of the
// update one:
//
//  1. NO restart-gap machinery of its OWN. A ship replaces nothing on this
//     Mac — serve, the daemon and the binary all stay up from preflight
//     through upload — so a failed status fetch here is a genuine error and is
//     reported as one, after a single retry to ride out a dropped request. The
//     one exception is not the ship's doing: a concurrent UPDATE ends in
//     `launchctl kickstart -k`, which takes serve down for a couple of
//     seconds, and the shipper is given its own session precisely so it
//     SURVIVES that. Painting "✕ ship failed" over the one outage the run is
//     engineered to live through would be exactly backwards, so the ceiling
//     stretches while an update is in flight.
//  2. NO reload when it finishes. Nothing this page is running was replaced.
//
// And the wording, which is the part worth getting right: `done` means the
// .ipa reached App Store Connect. TestFlight still has to process it, and
// someone still has to install it from the TestFlight app afterwards. So the
// control says UPLOADED, never SHIPPED.

/** jobs.ShipTerminal: no run is in flight, so the button is live again. */
const SHIP_TERMINAL = new Set(["idle", "done", "failed"]);
// Twice UPD_RECENT_MS: the update button's window is sized for a page that
// reloads itself the instant the run ends, whereas nothing here interrupts
// whoever is reading — and the thing they most need to read is the build
// number and the "not installed yet" note.
const SHIP_RECENT_MS = 120000;
const SHIP_POLL_MS = 1000;
// One dropped request is a hiccup; two in a row means the server really has
// stopped answering. Deliberately NOT the update button's 120s gap clock —
// there is no expected outage to ride out here.
const SHIP_FAIL_MAX = 2;
// …unless an update is in flight, whose `launchctl kickstart -k` is a real,
// expected, seconds-long outage that the ship is built to survive. 30 ticks is
// ~30s: ten times the couple of seconds a kickstart takes, and still short
// enough that a serve which never comes back is reported while anyone is still
// watching.
const SHIP_FAIL_MAX_UPDATING = 30;

export interface ShipRunOptions {
  /** board.ship_ios — absent on an older server, which reads as plain idle. */
  record: WebShipIOS | undefined;
  /** True while an update run is in flight — stretches the fail ceiling. */
  updating: boolean;
  onOpenLog(): void;
  onToast(message: string): void;
}

export interface ShipRun {
  button: UpdButtonModel;
  banner: BannerModel | null;
  onClick(): void;
  onBannerAction(act: string): void;
}

/** Port of renderShipIOS/shipIngest/shipPollTick (app.js:4422-4731). */
export function useShipRun({ record, updating, onOpenLog, onToast }: ShipRunOptions): ShipRun {
  const [state, setState] = useState<WebShipIOS | null>(null);
  const [polling, setPolling] = useState(false);
  const [refusal, setRefusal] = useState("");
  const [refusalAt, setRefusalAt] = useState(0);
  // The POST is in flight but hasn't answered. A FLAG, not `btn.disabled`:
  // the vanilla painter wrote btn.disabled on every repaint, so the one
  // assignment the click made was undone 3s later at the latest by a board
  // poll and a second click earned a guaranteed 409. React re-renders on the
  // same schedule and would do exactly the same to a DOM-level flag, so the
  // fix ports over unchanged — the flag is what the paint reads.
  const [starting, setStarting] = useState(false);
  const [dismissed, setDismissed] = useLocalStorage("kraken-dismissed-ship", "");

  const stateRef = useRef<WebShipIOS | null>(null);
  stateRef.current = state;
  const cbRef = useRef({ onOpenLog, onToast });
  cbRef.current = { onOpenLog, onToast };
  // Read at tick time by the ceiling, which must see the update's CURRENT
  // state rather than whatever it was when the poll subscribed.
  const updatingRef = useRef(updating);
  updatingRef.current = updating;
  const refusalAtRef = useRef(0);
  refusalAtRef.current = refusalAt;

  const { data: polled, failStreak } = usePoll(fetchShipStatus, SHIP_POLL_MS, {
    enabled: polling,
  });
  // usePoll's failStreak is only ever zeroed by a SUCCESS, and it survives an
  // enabled false→true cycle — which this control does routinely: tripping the
  // ceiling stops the fast poll, and the 3s board poll restarts it the moment
  // it sees a non-terminal record. Without a baseline the stale streak rides
  // through and the FIRST failed tick after restarting re-synthesizes the
  // failure, where vanilla's startShipPoll() reset the counter and demanded two
  // consecutive failures from a clean start (app.js:4536).
  const failStreakRef = useRef(0);
  failStreakRef.current = failStreak;
  const failBaseRef = useRef(0);
  useEffect(() => {
    if (polling) failBaseRef.current = failStreakRef.current;
  }, [polling]);

  /**
   * shipIngest is also where a stale refusal dies. Two ways it can outlive its
   * cause, both of which used to leave SHIP BLOCKED painted over the truth:
   *
   *   - a 409 refused this click because a run started elsewhere was already
   *     in flight. That run IS the answer to "what is happening"; the refusal
   *     was only ever a toast.
   *   - that same run then finishes, and its "uploaded build N" banner is the
   *     one thing worth reading. A refusal older than the record it complained
   *     about has nothing left to say.
   */
  const ingest = useCallback((s: WebShipIOS | null | undefined) => {
    if (!s) return;
    const running = !SHIP_TERMINAL.has(s.phase ?? "idle");
    const stamp = s.updated_at ?? 0;
    setRefusal((prev) => {
      if (!prev) return prev;
      return running || stamp > refusalAtRef.current ? "" : prev;
    });
    setState(s);
  }, []);

  useEffect(() => {
    if (!polled) return;
    ingest(polled);
    if (SHIP_TERMINAL.has(polled.phase ?? "idle")) setPolling(false);
  }, [polled, ingest]);

  useEffect(() => {
    const streak = failStreak - failBaseRef.current;
    if (streak <= 0) return;
    const ceiling = updatingRef.current ? SHIP_FAIL_MAX_UPDATING : SHIP_FAIL_MAX;
    if (streak < ceiling) return;
    // Past the ceiling — generous while an update is restarting serve, tight
    // otherwise, because nothing else on this Mac takes the server down
    // mid-ship. Say what is and isn't known: the shipper is a detached child
    // of a process that may well have died under it, so the upload itself
    // could still be running — the log is the only witness.
    setState((prev) => ({
      ...(prev ?? {}),
      phase: "failed",
      error:
        "kraken serve stopped answering mid-ship — the run itself may still be going; see ~/.kraken/ship-ios.log",
      updated_at: Date.now(),
    }));
    setPolling(false);
  }, [failStreak]);

  // The 3s board poll's copy. As with update, the 1s status poll wins outright
  // while it is running, or a board response older than a status response
  // would rewind the phase on screen. This is also how the poll recovers on
  // its own after a lost-contact failure: the board poll is the first thing to
  // come back, and it restarts the fast one.
  useEffect(() => {
    if (polling || !record) return;
    if (!SHIP_TERMINAL.has(record.phase ?? "idle")) setPolling(true);
    ingest(record); // clears a refusal this record has outlived
  }, [record, polling, ingest]);

  const start = useCallback(async () => {
    setRefusal("");
    setRefusalAt(0);
    setStarting(true);
    try {
      const data = await postShip();
      ingest({
        ...(stateRef.current ?? {}),
        phase: data.phase || "preflight",
        build: 0,
        error: "",
        started_at: Date.now(),
        updated_at: Date.now(),
      });
      setPolling(true);
    } catch (e) {
      // String(e): a catch binding is `unknown` — see UpdateControl's twin.
      const msg = (e instanceof Error ? e.message : String(e)) || "ship refused";
      setRefusal(msg);
      setRefusalAt(Date.now());
      cbRef.current.onToast("✕ " + msg);
    }
    setStarting(false);
  }, [ingest]);

  // ---- paint ------------------------------------------------------------
  const s = state ?? {};
  const phase = s.phase ?? "idle";
  const running = !SHIP_TERMINAL.has(phase);
  const updatedAt = s.updated_at ?? 0;
  const startedAt = s.started_at ?? 0;
  const fresh = !!updatedAt && Math.abs(Date.now() - updatedAt) < SHIP_RECENT_MS;
  const build = s.build ? " b" + s.build : "";
  const errText = s.error ?? "";

  let cls = "";
  let glyphCls = "gly";
  let glyphChar = "⇧";
  let label = "SHIP iOS";
  let disabled = false;
  // The default title carries the honest version of what the button does, so
  // the distinction is on screen before anyone clicks rather than only after.
  let title =
    "build the iOS app and upload it to App Store Connect — TestFlight then " +
    "takes a few minutes to process it, and the new build still has to be " +
    "installed from the TestFlight app on the phone";
  let banner: BannerModel | null = null;
  let action: "ship" | "log" = "ship";

  if (starting && !running) {
    // The click landed, the POST hasn't answered, and the record still reads
    // terminal — without this the next repaint hands the button back and a
    // second click earns a 409.
    cls = "busy";
    glyphCls = "spinner";
    glyphChar = "";
    label = "STARTING…";
    disabled = true;
  } else if (refusal && Date.now() - refusalAt < SHIP_RECENT_MS) {
    // Refused before anything was spawned. Bounded by the same window a
    // finished run gets: a refusal has no record to age out with, and left
    // unbounded it outranks every branch below forever.
    cls = "bad";
    glyphChar = "✕";
    label = "SHIP BLOCKED";
    title = refusal;
  } else if (running) {
    const secs = startedAt ? Math.max(0, Math.round((Date.now() - startedAt) / 1000)) : 0;
    cls = "busy";
    glyphCls = "spinner";
    glyphChar = "";
    label = `${phase.toUpperCase()}… ${secs}s`;
    disabled = true;
    title = "ship in flight — archiving and uploading takes several minutes" + build;
  } else if (phase === "failed" && fresh) {
    cls = "bad";
    glyphChar = "✕";
    label = "SHIP FAILED";
    title = errText || "ship failed";
    action = "log";
    banner = {
      cls: "bad",
      text: `✕ ship${build ? " of build " + s.build : ""} failed — ${errText || "no error recorded"}`,
      action: { act: "ship-log", label: "OPEN LOG" },
      stamp: updatedAt,
    };
  } else if (phase === "done" && fresh) {
    const secs =
      startedAt && updatedAt ? Math.max(0, Math.round((updatedAt - startedAt) / 1000)) : 0;
    cls = "ok";
    glyphChar = "✓";
    // UPLOADED, not SHIPPED: the .ipa is at Apple, the phone is unchanged.
    label = "UPLOADED" + build;
    title = "uploaded to App Store Connect — not yet installed anywhere";
    banner = {
      cls: "",
      text:
        `✓ build ${s.build || "?"} uploaded to App Store Connect` +
        (secs ? ` in ${secs}s` : "") +
        " · TestFlight still has to process it (usually a few minutes), then install " +
        "the new build from the TestFlight app on the phone — nothing on this Mac changed",
      stamp: updatedAt,
    };
  }

  // number→string for the localStorage key, not a narrowing — see update's twin.
  if (banner && banner.stamp && dismissed === String(banner.stamp)) banner = null;

  const onBannerAction = useCallback(
    (act: string) => {
      // No RELOAD NOW twin here — a ship replaces nothing this page is running.
      if (act === "ship-log") cbRef.current.onOpenLog();
      if (act === "dismiss") {
        const stamp = stateRef.current?.updated_at;
        if (stamp) setDismissed(String(stamp));
      }
    },
    [setDismissed],
  );

  return {
    button: { cls, glyphCls, glyphChar, label, disabled, title },
    banner,
    onClick: () => (action === "log" ? cbRef.current.onOpenLog() : void start()),
    onBannerAction,
  };
}
