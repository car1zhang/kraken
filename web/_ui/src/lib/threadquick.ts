// The branch dialog's two derivations, the model list it renders, and the two
// ask-thread rules (the origin badge, whether a composer is offered).
//
// They sit in lib/ rather than in ThreadOverlay.tsx because all are pure
// functions with no React in them, read by code that runs on every poll tick —
// the title preview under the question box, the poll-speed decision, the
// chips and the overlay head. ThreadOverlay keeps the derivations that need
// its markup (threadBlockHTML, threadConvoRender).

import type { ThreadBlock, ThreadRead, WebThread } from "../api/types.ts";

/**
 * The models a side thread may be branched on, in the order the picker renders
 * them — the four tiers as carl listed them, which is an order the contract
 * FREEZES rather than a cost or capability ranking anything here derives.
 *
 * These are ALIASES, not model ids: the server maps each to an exact id
 * (`core.ThreadModels`) precisely so this list never carries one. A tier alias
 * on the wire would silently follow whatever claude ships next, which is the
 * repo's stated model policy and the reason the mapping is server-side.
 */
export const THREAD_MODELS = ["haiku", "sonnet", "opus", "fable"] as const;

/**
 * The name to give a thread carl did not name — the first line of the question
 * he asked, one-lined and cut to a readable length.
 *
 * The server still REQUIRES a title (nothing about that changed), so this is
 * what makes the name field optional rather than the field going away: the UI
 * always sends a title, it just stops making him type one.
 *
 * FIRST non-blank line rather than the whole seed: a seed question is often a
 * paragraph, and its opening line is the part that names it. Whitespace runs
 * collapse so a wrapped or indented paste does not become a title full of gaps.
 *
 * 48 is this function's OWN title budget and nothing else's — it is near the
 * head label's 42ch (`.thread-name-label`, style.css) but not derived from it,
 * so a title in the 43-48 range can out-run that label. That is fine and
 * deliberate: the label ellipsises its own overhang in CSS already (max-width
 * + overflow:hidden + text-overflow:ellipsis), so the worst case there is one
 * ellipsis on top of another — while cutting at 42 here would shorten the
 * title everywhere else, the name the server files included.
 *
 * The 48-char cut backs up to the last space so the title ends on a word — and
 * falls back to a hard cut when the first 48 characters contain no space at all
 * (a URL, a stack frame), because a word-boundary rule with no word boundary
 * would otherwise return nothing. The ellipsis is appended ONLY on a cut, so a
 * 48-char title is not decorated into a lie about being truncated.
 */
export function deriveThreadTitle(seed: string): string {
  const line = seed.split("\n").find((l) => l.trim() !== "") ?? "";
  const t = line.replace(/\s+/g, " ").trim();
  if (t.length <= 48) return t;
  const head = t.slice(0, 48);
  const i = head.lastIndexOf(" ");
  const base = i > 0 ? head.slice(0, i) : head;
  return base.trimEnd() + "…";
}

/**
 * Is this thread mid-answer — i.e. is a payload worth polling for on its way?
 *
 * Two states answer yes, and they are the two the reader is actually watching:
 *
 *   - the thread has said nothing of its own yet but was branched WITH a
 *     question (`seed`). Its first answer is being generated in a window that
 *     opened seconds ago, and until a block lands there is nothing on the wire
 *     to infer that from — the seed carl typed is the only evidence, which is
 *     why it is a parameter rather than something read off `blocks`.
 *   - the thread's last block is not assistant text: a user turn, a tool call
 *     or a thinking block all mean the turn is still in flight.
 *
 * A thread branched IDLE (no seed, nothing asked) is not awaiting anything, and
 * neither is one whose last block is the assistant's finished answer. Those poll
 * at the ordinary rate.
 *
 * The split is threadSplitIndex's rule (ThreadOverlay.tsx) — one past the last
 * inherited block — re-derived here rather than imported: lib/ must not import a
 * component, and pulling ThreadOverlay into this module's graph would drag React
 * and api/client.ts along with it for four lines of arithmetic.
 */
export function threadAwaitingReply(
  blocks: ThreadBlock[],
  seed: string,
): boolean {
  let last = -1;
  blocks.forEach((b, i) => {
    if (b.inherited) last = i;
  });
  const own = blocks.slice(last + 1);
  if (!own.length) return seed.trim() !== "";
  // A non-empty `own` slice means `blocks` is non-empty and its last element IS
  // own's last, so the `?.` is for noUncheckedIndexedAccess and not a state
  // that can be reached.
  return blocks[blocks.length - 1]?.kind !== "text";
}

/**
 * The "from <node>" badge an ask thread wears, or null for none.
 *
 * An ask thread is a row whose `kind` is "headless". `kind` is not `role`:
 * `role` ("ask" | "mind") says what the thread is FOR and rides every thread
 * object already, while `kind` says how it RUNS (a pane, or headless `claude
 * -p` runs). That is why the kind value is "headless" rather than "ask" — a
 * pane thread with `role: "ask"` is the ordinary side thread, and must stay
 * unbadged.
 *
 * null for a pane thread (a row without `kind` is one), for a row that records
 * no origin, and for an ask thread opened from THIS node — the board's own
 * `node_id`. An absent `boardNodeID` compares as "": a board with no tailscale
 * or with `KRAKEN_NODES=off` states no node id, and SelfNodeID stamps "" on the
 * rows such a node opens, so they are its own.
 *
 * Otherwise the origin's device label, falling back to its login, then to its
 * node id; an origin with all three blank is null rather than an empty badge.
 */
export function threadOriginBadge(
  t: WebThread,
  boardNodeID: string | undefined,
): string | null {
  if (t.kind !== "headless" || !t.origin) return null;
  const o = t.origin;
  if ((o.node_id ?? "") === (boardNodeID ?? "")) return null;
  return o.label || o.login || o.node_id || null;
}

/**
 * Whether the thread view offers a composer.
 *
 * `live` is the read's own `live` once a read has landed, else
 * `t.state === "live"`; with neither (a just-branched thread the board has not
 * caught yet) it is true, the overlay head's own fallback.
 *
 *   - remote (a card another node owns): only a live headless (ask) thread.
 *     Any node that can see the card may send into it; a pane thread —
 *     including a row with no `kind`, whichever its `role` — is the owner's
 *     alone.
 *   - local: any live thread, pane or headless.
 */
export function threadComposerAllowed(
  t: Pick<WebThread, "kind" | "state"> | undefined,
  read: ThreadRead | null,
  remote: boolean,
): boolean {
  const live = read ? !!read.live : t ? t.state === "live" : true;
  if (remote) return t?.kind === "headless" && live;
  return live;
}
