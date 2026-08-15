// kronos' conversation, rendered from `/api/peers/read`'s block list.
//
// The same shape and the same idiom as the side-thread overlay's convo
// (ThreadOverlay.threadBlockHTML): a speaker row plus a markdown body, with
// tool lines dimmed to one terse line and no speaker at all. It is a separate
// function rather than a shared one because the two differ in exactly the
// things a reader notices — the thread's blocks carry `inherited` and a
// branch divider, kronos' carry neither, and kronos' assistant voice is a
// NAMED one ("kronos") where a thread's is "thread t3".
//
// HTML strings rather than JSX for the reason the thread overlay states: the
// body is rendered markdown and the column is painted into one innerHTML
// region, which is what lets a 3s poll leave the reader's scroll position
// alone when nothing changed.

import type { ThreadBlock } from "../../api/types.ts";
import { esc, renderMD } from "../../lib/markdown.ts";

const KRONOS_BLOCK_KINDS: Record<
  string,
  { cls: string; icon: string; who: string }
> = {
  user: { cls: "user", icon: "❯", who: "carl" },
  text: { cls: "asst", icon: "↯", who: "kronos" },
  thinking: { cls: "think", icon: "✳", who: "thinking" },
  tool: { cls: "tool", icon: "→", who: "" },
  teammate: { cls: "asst", icon: "✉", who: "teammate" },
};

/**
 * One block.
 *
 * `hasOwnProperty.call`, not a truthiness check on the lookup: a block kind of
 * "constructor" or "toString" would otherwise resolve to an Object.prototype
 * member and the class name would be garbage (or a throw). An unknown kind
 * reads as assistant text, which is what the transcript reader emits for
 * anything it did not classify.
 */
export function kronosBlockHTML(b: ThreadBlock): string {
  const known = Object.prototype.hasOwnProperty.call(
    KRONOS_BLOCK_KINDS,
    b.kind,
  );
  const kind = known ? b.kind : "text";
  const spec = KRONOS_BLOCK_KINDS[kind] as {
    cls: string;
    icon: string;
    who: string;
  };
  const text = b.text || "";
  if (kind === "tool") {
    // a tool line is already terse and mono — no speaker row, no markdown
    return `<div class="blk ${spec.cls}">${spec.icon} ${esc(text)}</div>`;
  }
  const from = typeof b.from === "string" ? b.from : "";
  const who = from || spec.who || kind;
  // renderMD escapes before transforming, so agent-authored text is inert
  return `<div class="blk ${spec.cls}"><div class="who">${spec.icon} ${esc(who)}</div><div class="blk-body">${renderMD(text)}</div></div>`;
}

/** The whole tail, oldest first — the order the reader returns them in. */
export function kronosConvoRender(blocks: ThreadBlock[]): string {
  if (!blocks.length) {
    return '<div class="kronos-empty">nothing said yet</div>';
  }
  return blocks.map((b) => kronosBlockHTML(b)).join("");
}
