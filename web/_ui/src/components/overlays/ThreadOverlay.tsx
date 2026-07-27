import { useCallback, useEffect, useRef, useState } from "react";
import { capturePane, fetchThreadRead, threadOp } from "../../api/client.ts";
import type { ThreadBlock, ThreadRead, WebJob } from "../../api/types.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { age } from "../../lib/format.ts";
import { esc, renderMD } from "../../lib/markdown.ts";
import { Overlay, useOpenTmux, type Mutate } from "./Overlay.tsx";

// A side thread: a fork of zeus' session running as a second claude in its own
// tmux window of the same cell — somewhere to ASK without disturbing zeus.
//
// ONE overlay, two modes (app.js:2461 openThread), split on `tid`:
//   tid == null   "new"    — branch: required name + optional seed question
//   tid != null   "thread" — read/steer/kill/revive one that exists
// They share a head, a body column and a close path, which is why they are one
// component rather than two.
//
// Every op goes through /api/threads, so a server refusal ("no live cell to
// branch", "3 live threads already", "t2 can't be revived: …") lands in the
// status bar IN THE SERVER'S OWN WORDS. This file never invents error copy for
// a failed op — it only points at the status bar.

const THREAD_BLOCK_KINDS: Record<
  string,
  { cls: string; icon: string; who: string }
> = {
  user: { cls: "user", icon: "❯", who: "carl" },
  text: { cls: "asst", icon: "↯", who: "" },
  thinking: { cls: "think", icon: "✳", who: "thinking" },
  tool: { cls: "tool", icon: "→", who: "" },
  teammate: { cls: "asst", icon: "✉", who: "teammate" },
};

/**
 * app.js:2526. Returns an HTML string because the block body is rendered
 * markdown and the conversation is one innerHTML region.
 *
 * `hasOwnProperty.call`, NOT a truthiness check on the lookup: a block kind of
 * "constructor" or "toString" would otherwise resolve to an Object.prototype
 * member and `spec.cls` would be garbage (or a throw).
 */
export function threadBlockHTML(
  b: ThreadBlock,
  inherited: boolean,
  tid: string,
): string {
  const known = Object.prototype.hasOwnProperty.call(
    THREAD_BLOCK_KINDS,
    b.kind,
  );
  const kind = known ? b.kind : "text";
  const spec = THREAD_BLOCK_KINDS[kind] as {
    cls: string;
    icon: string;
    who: string;
  };
  const cls = `blk ${spec.cls}${inherited ? " inherited" : ""}`;
  const text = b.text || "";
  if (kind === "tool") {
    // tool lines are already terse and mono — no speaker row, no markdown
    return `<div class="${cls}">${spec.icon} ${esc(text)}</div>`;
  }
  const from = typeof b.from === "string" ? b.from : "";
  const who = from || spec.who || (kind === "text" ? "thread " + tid : kind);
  // renderMD escapes before transforming, so agent-authored text is inert
  return `<div class="${cls}"><div class="who">${spec.icon} ${esc(who)}</div><div class="blk-body">${renderMD(text)}</div></div>`;
}

/**
 * app.js:2551 — one past the last inherited block. The server tags every block
 * with `inherited` and that is the only shape there is, so the split is 0 when
 * the thread has said everything itself and blocks.length when it has said
 * nothing yet.
 */
export function threadSplitIndex(blocks: ThreadBlock[]): number {
  let last = -1;
  blocks.forEach((b, i) => {
    if (b.inherited) last = i;
  });
  return last + 1;
}

export function threadConvoRender(data: ThreadRead, tid: string): string {
  const blocks = data.blocks || [];
  if (!blocks.length) return '<div class="thread-empty">no messages yet</div>';
  const split = threadSplitIndex(blocks);
  const forkLabel = data.fork_uuid ? " · " + data.fork_uuid : "";
  const out: string[] = [];
  blocks.forEach((b, i) => {
    if (i === split) {
      out.push(
        `<div class="thread-divider">⑂ branched here${esc(forkLabel)}</div>`,
      );
    }
    out.push(threadBlockHTML(b, i < split, tid));
  });
  // a thread whose every block is inherited (branched, nothing asked yet)
  // still gets its divider — at the very end, which is exactly where it is
  if (split >= blocks.length) {
    out.push(
      `<div class="thread-divider">⑂ branched here${esc(forkLabel)}</div>`,
    );
  }
  return out.join("");
}

/**
 * created_at/closed_at are not pinned to a unit by the wire contract and age()
 * takes seconds — normalize a millisecond stamp rather than rendering a
 * 56-year-old thread. Same defensive read as every other foreign field.
 */
function threadSeconds(ts: number | undefined): number {
  const n = Number(ts) || 0;
  return n > 1e12 ? Math.floor(n / 1000) : n;
}

export interface ThreadOverlayProps {
  open: boolean;
  /** The job the thread hangs off. */
  slug: string | null;
  /** null opens the branch dialog; a tid opens that thread. */
  tid: string | null;
  /**
   * The job's freshest board entry. Its `threads` array is where the head
   * reads title/state/pane between reads — undefined means the job has left
   * the board, the ONE condition that force-closes this overlay.
   */
  job: WebJob | undefined;
  onClose: () => void;
  /**
   * A branch succeeded: flip the open overlay onto the thread it created,
   * WITHOUT closing and reopening. The title is passed because the board has
   * not caught the new row yet.
   */
  onBranched: (tid: string, title: string) => void;
  mutate: Mutate;
}

export function ThreadOverlay(props: ThreadOverlayProps) {
  return (
    <Overlay
      id="thread-overlay"
      open={props.open}
      priority={ESC_PRIORITY.thread}
      onClose={props.onClose}
    >
      {props.slug ? <ThreadBody {...props} /> : null}
    </Overlay>
  );
}

function ThreadBody({
  slug,
  tid,
  job,
  onClose,
  onBranched,
  mutate,
}: ThreadOverlayProps) {
  const jobSlug = slug as string;
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState("");
  const [newName, setNewName] = useState("");
  const [seed, setSeed] = useState("");
  const [renaming, setRenaming] = useState(false);
  const [renameValue, setRenameValue] = useState("");
  // a just-branched thread's title, until the board catches its row
  const [titleOverride, setTitleOverride] = useState<string | null>(null);
  const openTmuxTarget = useOpenTmux(mutate);
  const nameInput = useRef<HTMLInputElement | null>(null);
  const convoRef = useRef<HTMLDivElement | null>(null);
  const lastHTML = useRef<string>("");

  // The live-pane mirror is opt-in and STICKY, the same gating iOS does with
  // its Terminal tab: a capture nobody opened is a second tmux round-trip
  // every 3s for a pane nobody is looking at. One click, remembered from then
  // on.
  const [mirror, setMirror] = useLocalStorageFlag("kraken-thread-mirror", false);

  const rec = (job?.threads || []).find((t) => t.tid === tid) || null;

  // ---- the 3s read, only while the overlay is open on a thread ------------
  // No `n`: the server's own default is the right size. LastBlocks counts
  // NON-tool blocks, so a big number means re-reading and re-parsing
  // multi-megabyte transcript tails every tick to redraw a column nobody
  // scrolled.
  const read = usePoll<ThreadRead>(
    (signal) => fetchThreadRead(jobSlug, tid as string, undefined, signal),
    3000,
    { enabled: !!tid },
  );
  const refetchRead = read.refetch;

  // Two sources of truth, merged the way the vanilla board merged them: the
  // board payload (every 3s, knows the title) and the read endpoint's own
  // live/pane (fresher — it just looked at tmux). Whichever landed last wins,
  // which is safe only because head AND body are painted from this one
  // derivation: the two out-of-phase timers can disagree about the AGE of the
  // truth, never about what is on screen.
  const live = read.data ? !!read.data.live : rec?.state === "live";
  const pane = read.data?.pane || rec?.pane || "";
  const kept =
    typeof read.data?.counted === "number"
      ? read.data.counted
      : (read.data?.blocks || []).length;

  // a read failure gets its own note and clears its OWN note on recovery, so
  // it cannot eat a "send failed"/"message is empty" the user just triggered
  const readFailed = !!read.error && read.failStreak > 0;
  const shownNote = readFailed
    ? "couldn't read this thread: " + (read.error?.message ?? "")
    : note;

  // ---- checkStaleModals, the ASYMMETRIC half (app.js:591) -----------------
  // The thread overlay is scoped to a job but NOT to its state: a thread
  // outlives working→blocked→done, and a closed one outlives the cell. So
  // unlike steer and editor it force-closes ONLY when the job itself leaves
  // the board entirely — archived, deleted, renamed out from under it.
  const sawJob = useRef(false);
  useEffect(() => {
    if (job) {
      sawJob.current = true;
      return;
    }
    if (sawJob.current) onClose();
  }, [job, onClose]);

  // ---- the conversation ---------------------------------------------------
  // Painted imperatively for one reason: re-rendering identical markup every
  // 3s would reset the scroll position mid-read. The HTML is compared to the
  // last painted string and the scroll position is preserved across a real
  // change — pinned to the bottom if that is where the reader already was
  // (the live-tail case).
  useEffect(() => {
    const el = convoRef.current;
    if (!el || !tid) return;
    if (!read.data) {
      if (lastHTML.current === "") {
        el.innerHTML = '<div class="thread-empty">loading…</div>';
      }
      return;
    }
    const html = threadConvoRender(read.data, tid);
    if (html === lastHTML.current) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const prevTop = el.scrollTop;
    lastHTML.current = html;
    el.innerHTML = html;
    el.scrollTop = atBottom ? el.scrollHeight : prevTop;
  }, [read.data, tid]);

  // ---- the pane mirror ----------------------------------------------------
  // A second tmux round-trip on every tick, so it runs ONLY while the mirror
  // is actually open. A pane that just died is the common case here and the
  // next read re-derives live/pane, so a capture failure stays quiet.
  const capture = usePoll(
    (signal) => capturePane(pane, signal),
    3000,
    { enabled: !!tid && live && !!pane && mirror },
  );
  // A capture that fails is almost always a pane that just died, and the next
  // read re-derives live/pane — so it stays QUIET and the mirror goes away
  // rather than freezing the last frame or explaining itself in the note.
  const captureOK = !capture.error;

  const op = useCallback(
    async (
      o: "kill" | "revive" | "rename" | "send",
      extra: { title?: string; text?: string } = {},
    ) => {
      if (!tid) return null;
      setBusy(true);
      const res = await mutate(() => threadOp(jobSlug, o, { tid, ...extra }));
      setBusy(false);
      if (!res) setNote(o + " failed — see status bar");
      else {
        setNote("");
        void refetchRead();
      }
      return res;
    },
    [jobSlug, mutate, refetchRead, tid],
  );

  const branch = useCallback(async () => {
    const t = newName.trim();
    if (!t) {
      setNote("name this thread — one line on what it's for");
      nameInput.current?.focus();
      return;
    }
    setBusy(true);
    const res = await mutate(() =>
      threadOp(jobSlug, "branch", { title: t, text: seed.trim() }),
    );
    setBusy(false);
    if (!res) {
      setNote("branch failed — see status bar");
      return;
    }
    // branching spawns a claude in a new tmux window — seconds, during which
    // Escape/backdrop/a stale-close can all dismiss the overlay. A cancelled
    // branch stays cancelled: the thread exists either way and its chip is on
    // the card, so there is nothing to reopen an overlay for. React gives us
    // that for free — this component is unmounted and the callback is gone.
    if (res.tid) {
      setTitleOverride(t);
      onBranched(res.tid, t);
    } else {
      onClose(); // branched, but no id came back — the card carries it
    }
  }, [jobSlug, mutate, newName, onBranched, onClose, seed]);

  const send = useCallback(async () => {
    const body = text.trim();
    if (!body) {
      setNote("message is empty");
      return;
    }
    const res = await op("send", { text: body });
    if (res) setText("");
  }, [op, text]);

  const commitRename = useCallback(async () => {
    const t = renameValue.trim();
    if (!t || t === (rec?.title ?? "")) {
      setRenaming(false);
      return;
    }
    const res = await op("rename", { title: t });
    if (res) setTitleOverride(t);
    setRenaming(false);
  }, [op, rec?.title, renameValue]);

  // ---- head ---------------------------------------------------------------
  const closedAt = rec?.closed_at ? threadSeconds(rec.closed_at) : 0;
  const closedNote =
    "tmux window closed" +
    // 0 means "no read has landed yet", not "nothing was kept" — say nothing
    // rather than undercount the conversation sitting right above
    (kept ? ` · ${kept} blocks kept on the job` : "") +
    (closedAt ? ` · killed ${age(closedAt)} ago` : "") +
    " · revive to continue";

  const mirrorable = live && !!pane && captureOK;
  const paneLabel =
    (mirror ? "▾ live pane · " : "▸ live pane · ") +
    (job?.cell ? job.cell + " · " : "") +
    pane;

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="thread-tid">
          {tid ? "⑂ " + tid : "⑂ branch — " + jobSlug}
        </span>
        <span
          className="thread-name-label"
          id="thread-name-label"
          hidden={!tid || renaming}
          role="button"
          tabIndex={0}
          title="rename this thread"
          onClick={() => {
            if (!tid) return;
            setRenameValue(titleOverride ?? rec?.title ?? "");
            setRenaming(true);
          }}
          onKeyDown={(e) => {
            // role="button" promises keyboard operability, and renaming is the
            // ONLY thing this label does — without it the rename is mouse-only
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              if (!tid) return;
              setRenameValue(titleOverride ?? rec?.title ?? "");
              setRenaming(true);
            }
          }}
        >
          {titleOverride ?? rec?.title ?? ""}
        </span>
        <input
          className="thread-name-input"
          id="thread-name-input"
          hidden={!renaming}
          spellCheck={false}
          maxLength={80}
          aria-label="thread name"
          autoFocus={renaming}
          disabled={busy}
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          onBlur={() => setRenaming(false)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              // Escape here means "stop renaming", not "close the overlay" —
              // the document-level Escape chain must NOT also fire
              e.stopPropagation();
              setRenaming(false);
              return;
            }
            if (
              e.key !== "Enter" ||
              e.nativeEvent.isComposing ||
              e.nativeEvent.keyCode === 229
            ) {
              return;
            }
            e.preventDefault();
            void commitRename();
          }}
        />
        <span
          className={"thread-pill " + (live ? "live" : "closed")}
          id="thread-pill"
          hidden={!tid}
        >
          {live ? "live" : "closed"}
        </span>
        <span className="spacer"></span>
        {/* raises THIS THREAD's window, by pane — pointless once the window is
            gone, and hidden without a pane too: a slug-only post would raise
            zeus' window instead, the one window a thread exists not to
            disturb. */}
        <button
          className="btn tmux-open open-tmux"
          id="thread-tmux"
          hidden={!tid || !live || !pane}
          data-pane={pane}
          title="raise this thread's window in Ghostty"
          onClick={() => void openTmuxTarget({ pane })}
        >
          ⧉ tmux
        </button>
        <button
          className="btn ghost"
          id="thread-revive"
          hidden={!tid || live}
          disabled={busy}
          title="resume this thread's session in a fresh window of the cell"
          onClick={() => void op("revive")}
        >
          ⟲ revive
        </button>
        <button
          className="btn ghost danger"
          id="thread-kill"
          hidden={!tid || !live}
          disabled={busy}
          title="close this thread's tmux window — the conversation stays and can be revived"
          // no confirm: killing a thread only closes its tmux window; the row
          // and the conversation stay on the job and ⟲ revive brings it back
          onClick={() => void op("kill")}
        >
          ✕ kill thread
        </button>
      </div>
      <div className="panel-body answer-body">
        <div className="thread-new" id="thread-new" hidden={!!tid}>
          <div className="thread-banner">
            <b>⑂ branch a side thread</b>
            <br />
            Forks zeus' session as it stands right now into a new window of the
            same cell, in the same worktree. Zeus is not interrupted and this
            conversation is unaffected. Threads are for asking, not for doing.
          </div>
          <label className="thread-field">
            what is this thread for?
            <input
              id="thread-new-name"
              ref={nameInput}
              placeholder="short name — e.g. short-clip WER"
              spellCheck={false}
              maxLength={80}
              autoFocus={!tid}
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void branch();
                }
              }}
            />
          </label>
          <label className="thread-field">
            seed question <span className="dim">— optional</span>
            <textarea
              id="thread-seed"
              rows={4}
              placeholder="optional: the first thing to ask — leave empty to open the thread at its prompt with zeus' context loaded"
              value={seed}
              onChange={(e) => setSeed(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void branch();
                }
              }}
            />
          </label>
          <div className="thread-new-actions">
            <button
              className="btn"
              id="thread-branch"
              disabled={busy}
              onClick={() => void branch()}
            >
              ⑂ branch thread ⌘⏎
            </button>
            <button className="btn ghost" id="thread-cancel" onClick={onClose}>
              cancel
            </button>
          </div>
        </div>

        <div className="thread-view" id="thread-view" hidden={!tid}>
          <div className="convo" id="thread-convo" ref={convoRef} />
          {/* the panehead IS the mirror's toggle: capture-pane runs on every
              3s tick while it is open, so it is opt-in and sticky */}
          <div
            className="panehead"
            id="thread-panehead"
            hidden={!mirrorable}
            role="button"
            tabIndex={0}
            title="mirror this thread's tmux pane — polls while open"
            onClick={() => setMirror(!mirror)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                setMirror(!mirror);
              }
            }}
          >
            <span className="live-dot"></span>
            <span id="thread-panelabel">{paneLabel}</span>
          </div>
          <pre
            className="thread-pane"
            id="thread-pane"
            hidden={!mirrorable || !mirror}
          >
            {capture.data?.content ?? ""}
          </pre>
          <div className="thread-empty" id="thread-closed-note" hidden={live}>
            {closedNote}
          </div>
          <div className="thread-composer" id="thread-composer" hidden={!live}>
            <textarea
              id="thread-text"
              rows={3}
              placeholder="ask this thread — routed straight to its pane"
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
              id="thread-send"
              disabled={busy}
              onClick={() => void send()}
            >
              send ⌘⏎
            </button>
          </div>
        </div>
        <div className="answer-note" id="thread-note">
          {shownNote}
        </div>
      </div>
    </>
  );
}
