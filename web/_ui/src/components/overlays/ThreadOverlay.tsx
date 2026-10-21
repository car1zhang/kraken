import { useCallback, useEffect, useRef, useState } from "react";
import { fetchNodeThreadRead, fetchThreadRead, threadOp } from "../../api/client.ts";
import type { ThreadBlock, ThreadRead, WebJob } from "../../api/types.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useLocalStorage } from "../../hooks/useLocalStorage.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { ownerTurnLabel } from "../../lib/fleet.ts";
import { age } from "../../lib/format.ts";
import { esc, renderMD } from "../../lib/markdown.ts";
import {
  THREAD_MODELS,
  deriveThreadTitle,
  threadAwaitingReply,
} from "../../lib/threadquick.ts";
import { Overlay, useOpenTmux, type Mutate } from "./Overlay.tsx";

// A side thread: a fork of zeus' session running as a second claude in its own
// tmux window of the same cell — somewhere to ASK without disturbing zeus.
//
// ONE overlay, two modes (app.js:2461 openThread), split on `tid`:
//   tid == null   "new"    — branch: the question, then an optional name and
//                            the model to answer it on
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
  // the operator's turns: labelled with board.operator at render time (see
  // operatorLabel), so the table carries no name of its own
  user: { cls: "user", icon: "❯", who: "" },
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
/**
 * The label on the operator's own turns: the board's `operator` name, trimmed,
 * or "you" when the board carries none (a server predating the key) or a
 * blank one.
 */
export function operatorLabel(operator?: string): string {
  const name = typeof operator === "string" ? operator.trim() : "";
  return name || "you";
}

export function threadBlockHTML(
  b: ThreadBlock,
  inherited: boolean,
  tid: string,
  mind?: BlockMindAttrs,
  operator?: string,
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
  const attrs = mind && mind.stamp !== false ? blockMindAttrs(mind, b) : "";
  if (kind === "tool") {
    // tool lines are already terse and mono — no speaker row, no markdown
    return `<div class="${cls}"${attrs}>${spec.icon} ${esc(text)}</div>`;
  }
  const from = typeof b.from === "string" ? b.from : "";
  const who =
    from ||
    (kind === "user" ? operatorLabel(operator) : "") ||
    spec.who ||
    (kind === "text" ? (mind?.speaker ?? "thread " + tid) : kind);
  // renderMD escapes before transforming, so agent-authored text is inert
  return `<div class="${cls}"${attrs}><div class="who">${spec.icon} ${esc(who)}</div><div class="blk-body">${renderMD(text)}</div></div>`;
}

/**
 * What a rendered block stamps for the mind button (lib/mind.ts): each block
 * is a `transcript` source carrying the session it was read from and its own
 * record uuid (taken off the block), plus the thread's tid when it is one.
 */
export interface BlockMindAttrs {
  session?: string;
  tid?: string;
  /** The assistant speaker label, in place of "thread <tid>" (zeus' panel). */
  speaker?: string;
  /**
   * `false` keeps `speaker` and stamps NO `data-mind-*` attribute: another
   * node's transcript, which is never a mind source on this machine.
   */
  stamp?: false;
}

/** ` data-mind-…="…"` for one block; values escaped, empty ones omitted. */
function blockMindAttrs(m: BlockMindAttrs, b: ThreadBlock): string {
  const uuid = typeof b.uuid === "string" ? b.uuid : "";
  const pairs: [string, string | undefined][] = [
    ["data-mind-kind", "transcript"],
    ["data-mind-session", m.session],
    ["data-mind-uuid", uuid],
    ["data-mind-tid", m.tid],
  ];
  return pairs
    .filter(([, v]) => !!v)
    .map(([k, v]) => ` ${k}="${esc(v as string)}"`)
    .join("");
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

export function threadConvoRender(
  data: ThreadRead,
  tid: string,
  mind?: BlockMindAttrs,
  operator?: string,
): string {
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
    out.push(threadBlockHTML(b, i < split, tid, mind, operator));
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
  /** `board.operator` — labels the operator's turns ("you" when absent). */
  operator?: string;
  /**
   * The owner node of a REMOTE job's thread: the conversation is read through
   * `/api/nodes/thread/read` and nothing here can act on it.
   */
  node?: string;
}

export function ThreadOverlay(props: ThreadOverlayProps) {
  return (
    <Overlay
      id="thread-overlay"
      open={props.open}
      priority={ESC_PRIORITY.thread}
      onClose={props.onClose}
    >
      {props.slug && props.node ? (
        // the branch dialog is the owner's alone, so a remote open with no
        // tid renders an empty panel inside the overlay shell
        props.tid ? (
          <RemoteThreadBody
            key={props.node + "/" + props.slug + "/" + props.tid}
            node={props.node}
            slug={props.slug}
            tid={props.tid}
            job={props.job}
            onClose={props.onClose}
            // the thread's prompts are the OWNER's — see ownerTurnLabel
            operator={ownerTurnLabel(props.job?.owner, props.operator)}
          />
        ) : null
      ) : props.slug ? (
        <ThreadBody {...props} />
      ) : null}
    </Overlay>
  );
}

/**
 * A remote job's side thread, read-only: the head's title and live/closed
 * pill, then the conversation — no composer, no rename, no kill/revive, no
 * tmux and no mind source. Read on the local body's 3s cadence (its 1s
 * awaiting-a-reply tick exists for a reply THIS reader asked for, which a
 * read-only view never does).
 */
function RemoteThreadBody({
  node,
  slug,
  tid,
  job,
  onClose,
  operator,
}: {
  node: string;
  slug: string;
  tid: string;
  job: WebJob | undefined;
  onClose: () => void;
  operator?: string;
}) {
  const read = usePoll<ThreadRead>(
    (signal) => fetchNodeThreadRead(node, slug, tid, undefined, signal),
    3000,
  );
  const convoRef = useRef<HTMLDivElement | null>(null);
  const lastHTML = useRef("");
  const rec = (job?.threads || []).find((t) => t.tid === tid) || null;
  const live = read.data ? !!read.data.live : rec ? rec.state === "live" : true;

  // ThreadBody's force-close rule: only the job leaving the board closes this
  const sawJob = useRef(false);
  useEffect(() => {
    if (job) {
      sawJob.current = true;
      return;
    }
    if (sawJob.current) onClose();
  }, [job, onClose]);

  // painted imperatively for ThreadBody's scroll-preserving reason; no mind
  // attrs are passed, so no block carries a `data-mind-*` source
  useEffect(() => {
    const el = convoRef.current;
    if (!el) return;
    if (!read.data) {
      if (lastHTML.current === "") {
        el.innerHTML = '<div class="thread-empty">loading…</div>';
      }
      return;
    }
    const html = threadConvoRender(read.data, tid, undefined, operator);
    if (html === lastHTML.current) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const prevTop = el.scrollTop;
    lastHTML.current = html;
    el.innerHTML = html;
    el.scrollTop = atBottom ? el.scrollHeight : prevTop;
  }, [read.data, tid, operator]);

  const readFailed = !!read.error && read.failStreak > 0;
  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="thread-tid">
          {"⑂ " + tid}
        </span>
        {rec?.title ? (
          <span className="thread-name-label" id="thread-name-label">
            {rec.title}
          </span>
        ) : null}
        <span className={"thread-pill " + (live ? "live" : "closed")} id="thread-pill">
          {live ? "live" : "closed"}
        </span>
        {rec?.model ? (
          <span className="thread-pill model" id="thread-model-pill">
            {rec.model}
          </span>
        ) : null}
        <span className="spacer"></span>
      </div>
      <div className="panel-body answer-body">
        <div className="thread-view" id="thread-view">
          <div className="convo" id="thread-convo" ref={convoRef} />
        </div>
        <div className="answer-note" id="thread-note">
          {readFailed ? "couldn't read this thread: " + (read.error?.message ?? "") : ""}
        </div>
      </div>
    </>
  );
}

function ThreadBody({
  slug,
  tid,
  job,
  onClose,
  onBranched,
  mutate,
  operator,
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
  // the question is the field a failed branch sends carl back to, so it is the
  // one holding a ref — the name is optional and cannot be the thing missing
  const seedInput = useRef<HTMLTextAreaElement | null>(null);
  const convoRef = useRef<HTMLDivElement | null>(null);
  const lastHTML = useRef<string>("");

  // No live-pane mirror any more (carl, 2026-10-20: "in branched we shouldn't
  // display the pane"). The overlay is the CONVERSATION — the read endpoint's
  // blocks — and the pane itself is one ⧉ tmux click away in Ghostty. What
  // went with it: the sticky `kraken-thread-mirror` flag, the 3s
  // capture-pane poll it gated, the ▸/▾ panehead toggle and the <pre> under
  // it. `pane` below is still read, for the tmux button's target.

  // The model picker's choice is STICKY: it is a standing preference, not a
  // per-branch decision, and re-picking it on every branch would be the
  // typing this dialog exists to remove.
  const [storedModel, setStoredModel] = useLocalStorage(
    "kraken-thread-model",
    "opus",
  );
  // A key left by a hand edit (or a future board with a fifth model) must not
  // ride out as a request the server refuses by name — an unrecognised value
  // reads as the default, and the picker lights that default, so what is shown
  // as picked is what a branch would actually send.
  const model = (THREAD_MODELS as readonly string[]).includes(storedModel)
    ? storedModel
    : "opus";

  const rec = (job?.threads || []).find((t) => t.tid === tid) || null;

  // ---- the read, only while the overlay is open on a thread ---------------
  // No `n`: the server's own default is the right size. LastBlocks counts
  // NON-tool blocks, so a big number means re-reading and re-parsing
  // multi-megabyte transcript tails every tick to redraw a column nobody
  // scrolled.
  //
  // 1s while an answer is in flight, 3s the rest of the time. The interval is
  // STATE rather than an inline derivation because the payload that decides it
  // is this poll's own — `read.data` does not exist yet on the line below. The
  // effect further down closes that loop one render later, which is the render
  // the deciding block landed in anyway.
  const [awaiting, setAwaiting] = useState(false);
  const read = usePoll<ThreadRead>(
    (signal) => fetchThreadRead(jobSlug, tid as string, undefined, signal),
    awaiting ? 1000 : 3000,
    { enabled: !!tid },
  );
  const refetchRead = read.refetch;

  // Two sources of truth, merged the way the vanilla board merged them: the
  // board payload (every 3s, knows the title) and the read endpoint's own
  // live/pane (fresher — it just looked at tmux). Whichever landed last wins,
  // which is safe only because head AND body are painted from this one
  // derivation: the two out-of-phase timers can disagree about the AGE of the
  // truth, never about what is on screen.
  // The no-data-at-all fallback is live, not closed: a just-branched thread
  // has no board row yet (titleOverride above exists for the same window) and
  // rendering the pill red-"closed" for it asserts a state nobody measured —
  // grey was an acceptable lie, --stopped red is not. A row that EXISTS and
  // says closed still wins until the read lands.
  const live = read.data ? !!read.data.live : rec ? rec.state === "live" : true;
  const pane = read.data?.pane || rec?.pane || "";
  const kept =
    typeof read.data?.counted === "number"
      ? read.data.counted
      : (read.data?.blocks || []).length;

  // The gap between asking and the first block landing is the ONE stretch a
  // reader WATCHES this column rather than reads it, so that is where the
  // faster tick goes — and it goes away again the moment the answer is whole.
  // A closed thread is never awaiting: nothing is running to answer. `seed` is
  // what makes a just-branched thread count — it carries only inherited blocks
  // until its first turn, so the question carl typed is the only evidence that
  // anything is coming at all.
  const nextAwaiting =
    live && threadAwaitingReply(read.data?.blocks || [], seed);
  useEffect(() => {
    setAwaiting(nextAwaiting);
  }, [nextAwaiting]);

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
    // Every block is a mind-capture source: the session it was read from (the
    // THREAD's own transcript, which carries the inherited records too — the
    // read's top-level `session`, contract amendment 3) and its record uuid,
    // plus this thread's tid.
    const html = threadConvoRender(
      read.data,
      tid,
      { session: read.data.session, tid },
      operator,
    );
    if (html === lastHTML.current) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const prevTop = el.scrollTop;
    lastHTML.current = html;
    el.innerHTML = html;
    el.scrollTop = atBottom ? el.scrollHeight : prevTop;
  }, [read.data, tid, operator]);

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
    // A typed name always wins; otherwise the question names the thread. The
    // server still REQUIRES a title, so the derivation is not a nicety — it is
    // what lets the name field be optional at all.
    const t = newName.trim() || deriveThreadTitle(seed);
    if (!t) {
      // the only way to reach here is an empty question AND an empty name,
      // so the ask is for either one — not for the name in particular
      setNote("ask something — or name the thread and open it at its prompt");
      seedInput.current?.focus();
      return;
    }
    setBusy(true);
    const res = await mutate(() =>
      threadOp(jobSlug, "branch", { title: t, text: seed.trim(), model }),
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
  }, [jobSlug, model, mutate, newName, onBranched, onClose, seed]);

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

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="thread-tid">
          {tid ? "⑂ " + tid : "⑂ branch — " + jobSlug}
        </span>
        {/* A mind thread (forked by the ◆ mind button) is the one writer of
            the mental model; the chip says which kind of thread this is. */}
        {tid && rec?.role === "mind" ? (
          <span
            className="thread-role mind"
            title="a mind thread — drafts and commits mental-model items"
          >
            mind
          </span>
        ) : null}
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
        {/* Rendered only when the row carries a model, rather than hidden with
            a fallback: a thread branched before per-thread models existed has
            no model of its own, and printing the job's would claim a choice
            nobody made. Absence is the honest paint for it. */}
        {rec?.model ? (
          <span
            className="thread-pill model"
            id="thread-model-pill"
            title="the model this thread was branched on — kept across kill/revive"
          >
            {rec.model}
          </span>
        ) : null}
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
          {/* The question comes FIRST and takes the focus: asking is the whole
              errand, and making a name the price of admission put a naming
              decision in front of every question. */}
          <label className="thread-field">
            what do you want to ask?
            <textarea
              id="thread-seed"
              ref={seedInput}
              rows={4}
              placeholder="the thread opens on this, with zeus' context already loaded — leave it empty to open at a bare prompt instead"
              autoFocus={!tid}
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
          <label className="thread-field">
            name <span className="dim">— optional, taken from the question</span>
            {/* the placeholder IS the preview: it shows the title this branch
                would actually be filed under, so the derivation is something
                carl reads before it happens rather than after */}
            <input
              id="thread-new-name"
              placeholder={
                deriveThreadTitle(seed) || "short name — e.g. short-clip WER"
              }
              spellCheck={false}
              maxLength={80}
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
          {/* a div, not a <label>: a label wrapping buttons hands its click to
              the first one, which would make "model" itself a pick of haiku.
              role=group + aria-label is what names it for AT instead — the
              same move, and the same reason, as the load drill's group. */}
          <div className="thread-field">
            model
            <div
              className="thread-models"
              id="thread-models"
              role="group"
              aria-label="model for this thread"
            >
              {THREAD_MODELS.map((m) => (
                <button
                  key={m}
                  type="button"
                  className="btn"
                  aria-pressed={model === m}
                  disabled={busy}
                  onClick={() => setStoredModel(m)}
                >
                  {m}
                </button>
              ))}
            </div>
          </div>
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
          <div
            className="convo"
            id="thread-convo"
            ref={convoRef}
            // the job a selection in this conversation belongs to (lib/mind.ts
            // selectionSlug); each block below carries its own source
            data-mind-slug={jobSlug}
          />
          {/* no pane mirror between the conversation and the composer — see
              the note at the top of ThreadBody */}
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
