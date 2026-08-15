// The KRONOS view — the fifth hash route, and the board's window onto the one
// long-running claude that belongs to no job.
//
// Left: kronos' conversation, read from his tmux pane's transcript through the
// generic `/api/peers/read` and painted with the side-thread overlay's block
// idiom, plus a composer that types into the same pane through
// `/api/peers/send`. Right: liveness, the context meter fed by the statusline
// sentinel's last report, the two relay buttons, and the escalations he has not
// acked yet.
//
// Three rules shape this file:
//
//   - A composer that cannot send is never rendered. kronos off (no
//     ~/.kraken/kronos.env) and kronos not running are DIFFERENT facts with
//     different fixes, so each gets its own sentence where the composer would
//     be, rather than a disabled box that explains nothing.
//   - /COMPACT and /CLEAR confirm first and then relay the literal slash
//     command through the same send route as any other message. There is no
//     special endpoint: they are text kronos' own claude interprets, which is
//     also why /CLEAR's confirm says what is lost.
//   - A failed send says what the SERVER said. This view invents no error copy
//     — the note line under the composer carries the refusal verbatim, the same
//     promise ThreadOverlay makes about the status bar.

import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchKronos,
  fetchOrchInbox,
  fetchPaneRead,
  raisePane,
  sendToPane,
} from "../../api/client.ts";
import type {
  KronosStatus,
  OrchInbox,
  PeersRead,
} from "../../api/types.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { age, fmtTokens, logTime } from "../../lib/format.ts";
import {
  KRONOS_THRESHOLDS,
  kronosFillPct,
  kronosMeterState,
} from "../../lib/kronos.ts";
import { kronosConvoRender } from "./kronosConvo.ts";

/** The board tick. Everything on this view refreshes on it or slower. */
const KRONOS_POLL_MS = 3000;
/** Status and inbox move far more slowly than the conversation does. */
const KRONOS_STATUS_POLL_MS = 5000;
/** Non-tool blocks of the transcript tail; the server clamps this to 50. */
const KRONOS_READ_BLOCKS = 30;
/** How many escalations the rail lists — newest first. */
const KRONOS_ESC_SHOWN = 8;

export interface KronosViewProps {
  /**
   * Whether KRONOS is the visible view. It gates all three polls and drives
   * `hidden`, exactly as LogView/DreamView/GraphView do — the component stays
   * MOUNTED across view switches so a half-typed message survives a trip to
   * the board and back.
   */
  active: boolean;
}

export function KronosView({ active }: KronosViewProps) {
  const status = usePoll<KronosStatus>(
    (signal) => fetchKronos(signal),
    KRONOS_STATUS_POLL_MS,
    { enabled: active },
  );
  const st = status.data;
  const enabled = !!st?.enabled;
  const live = !!st?.live;
  const pane = st?.pane || "";

  // The transcript read runs only while there is something to read: a pane,
  // and a claude in it. A dead kronos' pane id is stale by definition, and
  // asking for its transcript every 3s would be a tmux round-trip for a 404.
  const read = usePoll<PeersRead>(
    (signal) => fetchPaneRead(pane, KRONOS_READ_BLOCKS, signal),
    KRONOS_POLL_MS,
    { enabled: active && enabled && live && !!pane },
  );
  const refetchRead = read.refetch;

  // The escalation rail is the one panel that stays useful with kronos down:
  // an unacked row is exactly what nobody is reading while he is not running.
  const inbox = usePoll<OrchInbox>(
    (signal) => fetchOrchInbox(signal),
    KRONOS_STATUS_POLL_MS,
    { enabled: active && enabled },
  );

  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");

  const convoRef = useRef<HTMLDivElement | null>(null);
  const lastHTML = useRef<string>("");

  // Painted imperatively, ThreadOverlay's reason exactly: re-rendering
  // identical markup every 3s would reset the scroll position mid-read. The
  // column is pinned to the bottom only if that is where the reader already
  // was — the live-tail case — and otherwise holds its place.
  useEffect(() => {
    const el = convoRef.current;
    if (!el) return;
    if (!read.data) {
      if (lastHTML.current === "") {
        el.innerHTML = '<div class="kronos-empty">loading…</div>';
      }
      return;
    }
    const html = kronosConvoRender(read.data.blocks || []);
    if (html === lastHTML.current) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const prevTop = el.scrollTop;
    lastHTML.current = html;
    el.innerHTML = html;
    el.scrollTop = atBottom ? el.scrollHeight : prevTop;
  }, [read.data]);

  /**
   * Type one message into kronos' pane.
   *
   * Deliberately not routed through the board's shared mutation path: nothing
   * here moves the board payload, so there is nothing to invalidate, and a
   * refusal ("no session in pane %7") belongs beside the composer that caused
   * it rather than in the clock corner.
   */
  const relay = useCallback(
    async (message: string): Promise<boolean> => {
      if (!pane) {
        setNote("kronos has no pane to send to");
        return false;
      }
      setBusy(true);
      try {
        await sendToPane(pane, message);
        setNote("");
        void refetchRead();
        return true;
      } catch (e) {
        setNote(e instanceof Error ? e.message : String(e));
        return false;
      } finally {
        setBusy(false);
      }
    },
    [pane, refetchRead],
  );

  const send = useCallback(async () => {
    const body = text.trim();
    if (!body) {
      setNote("message is empty");
      return;
    }
    if (await relay(body)) setText("");
  }, [relay, text]);

  const compact = useCallback(() => {
    if (
      !window.confirm(
        "send /compact to kronos? the conversation is summarised in place — his open turn is interrupted.",
      )
    ) {
      return;
    }
    void relay("/compact");
  }, [relay]);

  const clear = useCallback(() => {
    if (
      !window.confirm(
        "send /clear to kronos? this drops the whole conversation. His durable memory (the inbox rows and the knowledge graph) survives — anything said only in this pane does not.",
      )
    ) {
      return;
    }
    void relay("/clear");
  }, [relay]);

  // raisePane reduces the response to the note line's one string (a raise
  // that worked but could not focus the terminal still surfaces its note);
  // the ref is a double-click guard, synchronous where state would lag.
  const raising = useRef(false);
  const raise = useCallback(async () => {
    if (raising.current || !pane) return;
    raising.current = true;
    try {
      setNote(await raisePane(pane));
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    } finally {
      raising.current = false;
    }
  }, [pane]);

  const meter = kronosMeterState(st?.context?.pct);
  const fill = kronosFillPct(st?.context?.pct);
  const windowSize = st?.context?.window ?? 0;
  const reportedAt = st?.context?.at ?? 0;

  // A read failure gets its own note and clears it on recovery, so it cannot
  // eat a "message is empty" the reader just triggered.
  const readFailed = !!read.error && read.failStreak > 0;
  const shownNote = readFailed
    ? "couldn't read kronos' pane: " + (read.error?.message ?? "")
    : note;

  const items = [...(inbox.data?.items || [])].reverse();

  return (
    <main className="kronosview" id="kronos" hidden={!active}>
      <section className="kronos-chat">
        <div className="kronos-panehdr">
          <span className={"led" + (live ? " ok" : "")} />
          <span className="kronos-title">KRONOS</span>
          <span className="kronos-sub">
            {status.error
              ? "kraken web server unreachable"
              : !st
                ? "connecting…"
                : !enabled
                  ? "off"
                  : live
                    ? pane
                    : "not running"}
          </span>
        </div>
        <div className="kronos-convo" ref={convoRef} hidden={!enabled || !live} />
        {/* The two OFF states, each with its own fix. Neither renders a
            composer: a box that cannot deliver is worse than an explanation. */}
        {st && !enabled ? (
          <div className="kronos-off">
            <b>kronos is off.</b>
            <br />
            The whole feature is opt-in on one file: create{" "}
            <code>~/.kraken/kronos.env</code> (an empty file is enough) and the
            daemon spawns him on its next tick. Until then nothing escalates
            and nothing is delivered.
          </div>
        ) : null}
        {st && enabled && !live ? (
          <div className="kronos-off">
            <b>kronos is not running.</b>
            <br />
            The daemon respawns him on a tick — first spawn mints his session,
            every later one resumes it, so the conversation comes back with him.
            A spawn attempted in the last few minutes is not retried
            immediately.
          </div>
        ) : null}
        {enabled && live ? (
          <div className="kronos-composer">
            <textarea
              id="kronos-text"
              rows={3}
              placeholder="ask kronos — typed straight into his pane"
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void send();
                }
              }}
            />
            <button
              className="btn btn-big"
              id="kronos-send"
              disabled={busy}
              onClick={() => void send()}
            >
              send ⌘⏎
            </button>
          </div>
        ) : null}
        <div className="kronos-note" id="kronos-note">
          {shownNote}
        </div>
      </section>

      <aside className="kronos-side">
        <div className="kronos-block">
          <h3>Status</h3>
          <div className="kronos-stat">
            <span>session</span>
            <b className={live ? "live" : "down"}>
              {!st
                ? "—"
                : !enabled
                  ? "OFF"
                  : live
                    ? "LIVE" +
                      (st.spawned_at
                        ? " · " + age(Math.floor(st.spawned_at / 1000))
                        : "")
                    : "DOWN"}
            </b>
          </div>
          <div className="kronos-stat">
            <span>session id</span>
            {/* the first segment only: enough to match against the pane's own
                banner, short enough not to wrap the rail */}
            <b>{st?.session_id ? st.session_id.slice(0, 8) : "—"}</b>
          </div>
          <div className="kronos-stat">
            <span>pane</span>
            <b>{pane || "—"}</b>
          </div>
        </div>

        <div className="kronos-block">
          <h3>
            Context · <span className={"kronos-pct " + meter.band}>{meter.label}</span>
            {windowSize ? <span className="dim"> of {fmtTokens(windowSize)}</span> : null}
          </h3>
          <div className="kronos-meter">
            <div
              className={"kronos-fill " + meter.band}
              style={{ width: fill + "%" }}
            />
            {KRONOS_THRESHOLDS.map((t) => (
              <div
                key={t}
                className={"kronos-tick" + (t >= 90 ? " hot" : "")}
                style={{ left: t + "%" }}
              />
            ))}
          </div>
          <div className="kronos-mlab">
            <span>0</span>
            {KRONOS_THRESHOLDS.map((t) => (
              <span key={t} className={t >= 90 ? "hot" : ""}>
                {t}
              </span>
            ))}
            <span>100%</span>
          </div>
          {/* The age is what makes a stale report read as stale rather than as
              current — the same reason the load drilldown prints one. */}
          <div className="kronos-mnote">
            {meter.pct === null
              ? "no report yet — the statusline sentinel files one as kronos works"
              : "reported " + (age(Math.floor(reportedAt / 1000)) || "just now") + " ago"}
          </div>
        </div>

        <div className="kronos-block">
          <h3>Controls</h3>
          <div className="kronos-btns">
            <button
              className="btn warn"
              id="kronos-compact"
              disabled={!enabled || !live || busy}
              title="summarise kronos' conversation in place"
              onClick={compact}
            >
              /COMPACT…
            </button>
            <button
              className="btn hot"
              id="kronos-clear"
              disabled={!enabled || !live || busy}
              title="drop kronos' conversation — his rows and the graph survive"
              onClick={clear}
            >
              /CLEAR…
            </button>
            <button
              className="btn"
              id="kronos-open-tmux"
              disabled={!live || !pane}
              title="raise kronos' tmux pane in the terminal — the same jump every cell card offers"
              onClick={() => void raise()}
            >
              OPEN TMUX
            </button>
          </div>
        </div>

        <div className="kronos-block">
          <h3>Escalations · unacked</h3>
          {items.length ? (
            <div className="kronos-esc">
              {items.slice(0, KRONOS_ESC_SHOWN).map((it) => (
                <div className="kronos-escrow" key={it.id}>
                  <span className="t">{logTime(it.seen_at)}</span>{" "}
                  <span className="k">{it.kind}</span>{" "}
                  {it.slug ? <span className="s">{it.slug}</span> : null}
                  <div className="sum">{it.summary}</div>
                </div>
              ))}
            </div>
          ) : (
            <div className="kronos-mnote">
              {inbox.error ? "inbox unavailable" : "nothing waiting"}
            </div>
          )}
        </div>
      </aside>
    </main>
  );
}
