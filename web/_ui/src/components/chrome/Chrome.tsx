import { useMemo, useRef } from "react";

import type { WebUsage } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { VIEWS, type View } from "../../hooks/useHashRoute.ts";
import { age, flagCount } from "../../lib/format.ts";
import { Banner, StaleBanner } from "./Banners.tsx";
import { AccentToggle, ScanToggle, SfxToggle } from "./HudControls.tsx";
import { ShipControl, useShipRun } from "./ShipControl.tsx";
import { ThemeToggle } from "./ThemeToggle.tsx";
import { UpdControlButton, useUpdateRun } from "./UpdateControl.tsx";
import { UsageStat } from "./UsageStat.tsx";

/**
 * How long a failed mutation keeps the clock corner. Long enough to read a
 * server refusal, short enough that the corner goes back to proving the board
 * is still ticking. It self-clears on the next render after the window, and the
 * board re-renders every 3s.
 */
const STATUS_ERROR_MS = 10_000;

export interface ChromeProps {
  board: BoardModel;
  view: View;
  onNavigate(v: View): void;
  /** Last /api/usage payload; undefined keeps the widget hidden. */
  usage: WebUsage | undefined;
  /** Opens the usage drilldown overlay (another slice). */
  onOpenUsage(): void;
  /** Toast host (another slice) — used only by update/ship refusals. */
  onToast(message: string): void;
  /**
   * Awaited before any self-update reload. The job editor autosaves on a 600ms
   * debounce and location.reload() drops that on the floor, so land it first.
   */
  onBeforeReload?(): Promise<void> | void;
}

/**
 * The header bar plus the three banner strips beneath it. Ports the chrome
 * half of index.html together with the status-bar writes in render()
 * (app.js:601-660) — `#build-stat` excepted, which comes off the update record
 * rather than the board one so it repaints on the 1s status poll too.
 *
 * Returns a fragment rather than a wrapper element: the banners are siblings
 * of `<header>` in the document, and wrapping them would change the layout.
 */
export function Chrome({
  board,
  view,
  onNavigate,
  usage,
  onOpenUsage,
  onToast,
  onBeforeReload,
}: ChromeProps) {
  const b = board.board;

  const update = useUpdateRun({
    record: b?.update,
    onOpenLog: () => onNavigate("log"),
    onToast,
    ...(onBeforeReload ? { onBeforeReload } : {}),
  });
  const ship = useShipRun({
    record: b?.ship_ios,
    updating: update.updating,
    onOpenLog: () => onNavigate("log"),
    onToast,
  });

  const flags = useMemo(() => flagCount(b?.jobs ?? []), [b]);

  // The clock corner is written on every refresh, and doubles as the failure
  // line for the shared mutation path — a rejected action's text belongs here
  // rather than in a modal. Read in the render body, not through a useMemo:
  // "sample the wall clock whenever `b` changes" is not a memoizable pure
  // computation, and React reserves the right to discard a memo and recompute
  // it. This component already re-renders on every board tick, which is exactly
  // when vanilla rewrote #tick-stat.
  //
  // The failure text is shown for a BOUNDED window. useBoard clears lastError
  // only on the next SUCCESSFUL mutation, so one refused drag would otherwise
  // pin "✕ already started" over the corner indefinitely — and the corner is
  // the board's only "am I still ticking" signal. Vanilla's was the opposite
  // extreme: post() wrote the error and its own refresh() overwrote it with the
  // clock milliseconds later, too fast to read.
  //
  // This corner is now where a refused SUBMIT lands — the 409 from a
  // draft→working drop, whose body IS the reason. The window is measured from
  // when the text is SET, i.e. from the answer rather than from the drag, so
  // the seconds the request spends in flight do not eat into it.
  const clock = new Date().toLocaleTimeString();
  const errSince = useRef(0);
  const lastErrText = useRef<string | null>(null);
  if (board.lastError !== lastErrText.current) {
    lastErrText.current = board.lastError;
    errSince.current = board.lastError ? Date.now() : 0;
  }
  const showErr =
    !!board.lastError && Date.now() - errSince.current < STATUS_ERROR_MS;
  const tick = showErr ? "✕ " + board.lastError : clock;

  // A board fetch that failed says so; mid-update the server is gone ON
  // PURPOSE, and "unreachable" would read as a crash.
  const daemonText = board.error
    ? update.restarting
      ? "daemon — restarting"
      : "kraken web server unreachable"
    : !b
      ? "connecting…"
      : b.daemon_ok
        ? `daemon pid ${b.daemon.pid} · tick ${age(b.daemon.last_tick)} ago`
        : "daemon not running";
  const ledOK = !board.error && !!b?.daemon_ok;

  // Both stale fields are absent on an older server, which reads as "not
  // stale" — never a hidden error.
  const staleMessages: string[] = [];
  if (b?.stale) {
    staleMessages.push("kraken was reinstalled since this server started — restart k web");
  }
  if (b?.daemon_ok && b.daemon?.stale) {
    staleMessages.push("kraken was reinstalled since the daemon started — restart the daemon");
  }

  return (
    <>
      <header className="chrome">
        {/* The wordmark's trailing glyph is a `<b>`, not the `<i>` the clock
            corner uses, and the distinction is SEMANTIC: this header's one
            `<i>▊</i>` is the BLINKING cursor block, and it is in #tick-stat,
            where a blink means "still ticking". (It is not the only ▊ the app
            can put on screen — style.css blinks the same glyph off
            `.panel-title::after` in an open overlay — so the reading is local to
            this bar rather than global.) The wordmark's `▞` is an accent mark
            that means nothing of the sort, so it gets a different element —
            style.css styles the two tags identically and says so. */}
        <span className="mark">
          KRAKEN<b>▞</b>
        </span>
        <span className={"led" + (ledOK ? " ok" : "")} id="daemon-led" />
        <span className="stat" id="daemon-stat">
          {daemonText}
        </span>
        <span className="stat" id="cells-stat">
          {b ? `cells ${b.live_cells}/${b.cell_cap}` : null}
        </span>
        <span className="stat hot" id="flag-stat">
          {flags ? `⚑ ${flags}` : null}
        </span>
        {/* The commit the running binary was built from. Visible so a
            self-update proves itself: the sha changes the moment the new
            binary is actually serving. Empty on an unstamped `go build`,
            which `.stat.build:empty` hides rather than showing "dev". */}
        <span className="stat build" id="build-stat" title="commit this kraken was built from">
          {update.buildChip || null}
        </span>
        <UsageStat usage={usage} onOpen={onOpenUsage} />
        <span className="viewtabs">
          {VIEWS.map((v) => (
            <button
              key={v}
              className={"viewtab" + (v === view ? " active" : "")}
              data-view={v}
              onClick={() => onNavigate(v)}
            >
              {v.toUpperCase()}
            </button>
          ))}
        </span>
        <span className="spacer" />
        <UpdControlButton id="update-btn" model={update.button} onClick={update.onClick} />
        <ShipControl id="ship-btn" model={ship.button} onClick={ship.onClick} />
        <AccentToggle />
        <ScanToggle />
        <SfxToggle />
        <ThemeToggle />
        {/* The blinking block rides the CLOCK only. On the failure text it
            would read as the corner still ticking at the exact moment it has
            stopped saying so — and the refusal is a sentence to read, not a
            terminal prompt. */}
        <span className="stat dim" id="tick-stat">
          {tick}
          {showErr ? null : <i>▊</i>}
        </span>
      </header>
      <StaleBanner id="stale-banner" messages={staleMessages} />
      <Banner id="update-banner" model={update.banner} onAction={update.onBannerAction} />
      <Banner id="ship-banner" model={ship.banner} onAction={ship.onBannerAction} />
    </>
  );
}
