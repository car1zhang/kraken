import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchKronos,
  fetchPaneRead,
  raisePane,
  sendToPane,
} from "../../api/client.ts";
import type { KronosStatus, PeersRead } from "../../api/types.ts";
import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { navigateTo } from "../../hooks/useHashRoute.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { kronosMeterState } from "../../lib/kronos.ts";
import { kronosConvoRender } from "../kronos/kronosConvo.ts";

/** Blocks shown in the dock's compact tail — enough to read the last exchange. */
const DOCK_READ_BLOCKS = 8;
const DOCK_STATUS_POLL_MS = 5000;
const DOCK_READ_POLL_MS = 4000;

/**
 * The kronos dock: the orchestrator's conversation as a collapsible fixed
 * panel BESIDE the goals dock, carl's ask verbatim — the board is where he
 * lives, and a glance at kronos should not cost a tab switch.
 *
 * It is GoalsPanel's shape on purpose (fixed, out of the columns' flow,
 * collapsible to a one-line pill, board-only — BoardView drops it while the
 * board is hidden, since a fixed element ignores the hidden <main>), anchored
 * at the board's LEFT corner mirroring the goals box on the right (carl's
 * call 2026-08-15), so the two docks bracket the board and neither covers
 * the other whatever state each is in. Shape, not classes: the dock's head
 * and pill are kd- rules on the same --ac/--tint palette the goals panel
 * uses, so it reads as board chrome — but a goals restyle still cannot
 * silently restyle this panel too.
 *
 * DEFAULT COLLAPSED, the opposite of the goals default, deliberately: goals
 * are a glance, kronos is a conversation — expanded-by-default it would cover
 * a card column on every fresh board load for everyone who has not opted in.
 * The pill still carries the live dot and the context percentage, so the
 * collapsed state answers "is he up and how full" for free.
 *
 * READ-MOSTLY. The composer types into kronos' pane through the same
 * peers/send route the full view uses. Against the full view this trades
 * depth for cost: the same status poll, a SHALLOWER transcript read (8 blocks
 * vs 30) at a slower tick (4s vs 3s), and no inbox poll at all — the
 * escalation list is the full view's job, one ⤢ away. Polls are gated on the
 * panel being OPEN — a collapsed dock costs one status poll and zero reads.
 */
export function KronosDock() {
  const [open, setOpen] = useLocalStorageFlag("kraken-kronos-dock-open", false);

  const status = usePoll<KronosStatus>(fetchKronos, DOCK_STATUS_POLL_MS);
  const st = status.data;
  const enabled = !!st?.enabled;
  const live = !!st?.live;
  const pane = st?.pane || "";
  const meter = kronosMeterState(st?.context?.pct);

  const read = usePoll<PeersRead>(
    useCallback(
      (signal: AbortSignal) => fetchPaneRead(pane, DOCK_READ_BLOCKS, signal),
      [pane],
    ),
    DOCK_READ_POLL_MS,
    { enabled: open && enabled && live && pane !== "" },
  );
  const refetchRead = read.refetch;
  // A dead read must not impersonate a quiet kronos: the tail freezes while
  // the dot stays green, so without its own note a stale conversation reads
  // as a current one — the exact staleness this board argues against for the
  // load estimate. Same derivation the full view uses.
  const readFailed = !!read.error && read.failStreak > 0;

  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");

  // The full view's imperative-innerHTML idiom, for the full view's reason:
  // the tail is server-rendered blocks, and React re-rendering markdown HTML
  // on every poll would lose the scroll position mid-read.
  //
  // The idiom is only safe while the painted NODE outlives the lastHTML
  // watermark that claims to describe it — the first shipped version
  // unmounted the div on collapse and on a liveness flip while the ref kept
  // the old string, so a re-expanded dock refused to repaint an identical
  // tail and sat permanently blank (the logic and edge reviewers' sole
  // blocking finding).
  // Two rules close it: the div stays MOUNTED (hidden, never removed) for as
  // long as the panel exists, and expanding the panel clears the watermark so
  // the remount that collapse/expand DOES cause always repaints.
  const convoRef = useRef<HTMLDivElement | null>(null);
  const lastHTML = useRef<string>("");
  useEffect(() => {
    if (open) lastHTML.current = "";
  }, [open]);
  useEffect(() => {
    const el = convoRef.current;
    if (!el) return;
    // pre-first-read is "loading", not "nothing said yet" — an empty tail is
    // a claim about durable state and must wait for a payload to back it
    const html = read.data
      ? kronosConvoRender(read.data.blocks ?? [])
      : '<div class="kronos-empty">loading…</div>';
    if (html === lastHTML.current) return;
    const pinned =
      el.scrollHeight - el.scrollTop - el.clientHeight < 40 || !lastHTML.current;
    el.innerHTML = html;
    lastHTML.current = html;
    if (pinned) el.scrollTop = el.scrollHeight;
  }, [read.data, open]);

  // Synchronous in-flight guard, same ref reasoning as `raising` below: the
  // input is deliberately NOT disabled during a send (disabling blurs it on
  // every message), so bare Enter — a double-tap, key-repeat — can re-enter
  // send() for the whole round trip, and `busy` commits asynchronously so a
  // state check would still race one frame. peers/send is non-idempotent:
  // a doubled send is the same prompt typed into kronos' pane twice.
  const sending = useRef(false);
  const send = useCallback(async () => {
    const body = text.trim();
    if (!body || sending.current) return;
    if (!pane) {
      // live with no pane is a respawn caught mid-tick — say so rather than
      // swallowing the Enter
      setNote("kronos has no pane to send to");
      return;
    }
    sending.current = true;
    setBusy(true);
    try {
      await sendToPane(pane, body);
      setText("");
      setNote("");
      void refetchRead();
    } catch (e) {
      // the server's own sentence, never invented copy
      setNote(e instanceof Error ? e.message : String(e));
    } finally {
      sending.current = false;
      setBusy(false);
    }
  }, [text, pane, refetchRead]);

  // In-flight guard is a ref, GoalsPanel's double-click reasoning: it must be
  // read and written synchronously inside one click handler.
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

  const dot = !enabled ? "off" : live ? "live" : "down";
  const pillMeta = `${dot}${meter.pct === null ? "" : " · " + meter.label}`;
  const shownNote = readFailed
    ? "couldn't read kronos' pane: " + (read.error?.message || "read failed")
    : note;

  return (
    <div className="kronos-dock" id="kronos-dock">
      {open ? (
        <div className="kronos-dock-panel">
          <div className="kd-head-row">
            <button
              className="kd-head"
              id="kronos-dock-toggle"
              title="collapse the kronos panel"
              onClick={() => setOpen(false)}
            >
              <span className="tri">▾</span> KRONOS{" "}
              <span className={"kd-dot " + dot} /> <span className="n">{pillMeta}</span>
            </button>
            <button
              className="kd-act"
              title="raise kronos' tmux pane in the terminal"
              aria-label="raise kronos' tmux pane"
              disabled={!live || !pane}
              onClick={() => void raise()}
            >
              ⧉
            </button>
            <button
              className="kd-act"
              title="open the full kronos view"
              aria-label="open the full kronos view"
              onClick={() => navigateTo("kronos")}
            >
              ⤢
            </button>
          </div>
          <div
            className="kronos-convo kd-convo"
            ref={convoRef}
            hidden={!enabled || !live}
          />
          {enabled && live ? (
            <div className="kd-composer">
              <input
                value={text}
                placeholder="ask kronos…"
                aria-label="ask kronos"
                onChange={(e) => setText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.nativeEvent.isComposing)
                    void send();
                }}
              />
              <button disabled={busy || !text.trim()} onClick={() => void send()}>
                send
              </button>
            </div>
          ) : (
            <div className="kd-empty">
              {enabled
                ? "kronos is not running — the daemon respawns him on a tick."
                : "kronos is off — create ~/.kraken/kronos.env to turn him on."}
            </div>
          )}
          {shownNote ? <div className="kd-note">{shownNote}</div> : null}
        </div>
      ) : (
        <button
          className="kd-pill"
          id="kronos-dock-toggle"
          title="expand the kronos panel"
          onClick={() => setOpen(true)}
        >
          <span className="tri">▸</span> KRONOS <span className={"kd-dot " + dot} />{" "}
          <span className="n">{pillMeta}</span>
        </button>
      )}
    </div>
  );
}
