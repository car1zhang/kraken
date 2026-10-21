import {
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { esc } from "../lib/markdown.ts";

// The mirrored-backdrop composer: /direct /info /aim /do /handoff rendered in
// their own skill colors behind a text-transparent <textarea>. Ported from
// app.js: 1741-1913, and the ONE mechanism this port does not get to simplify — a
// <textarea> cannot style substrings of its own value, so the real glyphs live
// in a sibling div and the two surfaces have to agree on every metric.
//
// What React DOES remove is the bug class: the vanilla code needed a manual
// refreshCmdHighlight() call after every programmatic `.value =` write (draft
// restore, form load, chip insert, image insert — four call sites), because a
// programmatic write fires no "input" event. Here the backdrop is derived from
// the same state the textarea's `value` is, so there is no path that changes
// one without the other.
//
// What React does NOT remove, and this file therefore keeps:
//
//  1. the unconditional trailing "\n" sentinel — a <textarea> reserves a blank
//     row below its last line that a `pre-wrap` div will not, so without it
//     scrollTop sync runs the mirror out of vertical room before the textarea;
//  2. the gutter compensation — `offsetWidth - clientWidth - 2` is whatever
//     width a classic (space-taking) scrollbar is actually eating; the
//     backdrop has `overflow:hidden` and reserves nothing, so without this the
//     two surfaces wrap at different columns and whole lines ghost;
//  3. scroll sync stays SYNCHRONOUS and uncoalesced — coalescing it visibly
//     lags the mirror behind a scroll-wheel fling, and a drag-resize changes
//     the width split without firing an input event, so the gutter is
//     recomputed here too;
//  4. `.cmd-hl-on` gating — style.css puts `color: transparent` behind that
//     class so a failure leaves a READABLE composer rather than an invisible
//     one. The class is applied only after a backdrop paint has actually
//     succeeded (see `active` below), which is the same promise the vanilla
//     `wrap.classList.add` made by sitting after the risky work.

// Two chips are NOT phases (dream has a hue in style.css but no web chip).
// /direct sits FIRST because it is a modifier typed ahead of the phase it
// modifies (`/direct do`, or `/direct /do` when both come from chips): it
// runs that phase skill minus its subagents and review rounds, so it has a
// chip and a hue but never a stage badge (jobs.pipelineStageSet keeps it
// out on purpose). /handoff sits LAST for the mirror reason: it writes
// 80-handoff.md and compacts (or spawns continuation teammates) and returns
// the cell to the phase it was in, and it is the one carl reaches for on a
// cell already mid-phase. There is no /pr chip: the pr skill folded into do
// (job 624) — the PR is do's own last step, opened before the manual-test
// gate — so kraken ships no pr skill and no phase asks for /pr; a typed /pr
// would reach only a personal skill of that name, which is not the
// pipeline's.
export const CMD_CHIPS = ["/direct", "/info", "/aim", "/do", "/handoff"] as const;
export type CmdChip = (typeof CMD_CHIPS)[number];

/**
 * Boundary-anchored match for any of the five commands.
 *
 * The leading `(^|\s)` group matters as much as the trailing `\b`: `\b` only
 * guards the END of the match, so without the anchor a mid-word slash run like
 * `web/static/do` or `[image: /Users/x/do-notes.png]` lights up. Accepted gap,
 * carried over verbatim: the anchor guards the start of a run of
 * non-whitespace, not "is actually a command", so `[image: /do/x.png]` still
 * highlights. Harmless — kraken's own upload paths are `~/.kraken/uploads/…`.
 */
export const CMD_HL_RE = new RegExp(
  "(^|\\s)(/(?:" + CMD_CHIPS.map((c) => c.slice(1)).join("|") + "))\\b",
  "g",
);

/** Per-command presence test, same anchoring, for the chip row's `active`. */
export const CMD_ACTIVE_RE = Object.fromEntries(
  CMD_CHIPS.map((cmd) => [cmd, new RegExp("(^|\\s)" + cmd + "\\b")]),
) as Record<CmdChip, RegExp>;

/**
 * app.js:1771 — escape first, then wrap. None of the five commands contain an
 * HTML-special character, so matching against already-escaped text is safe.
 * The trailing newline is the sentinel described in (1) above.
 */
export function cmdHighlightHTML(text: string): string {
  const highlighted = esc(text).replace(
    CMD_HL_RE,
    (_m, lead: string, cmd: string) =>
      `${lead}<span class="cmd-hl cmd-hl-${cmd.slice(1)}">${cmd}</span>`,
  );
  return highlighted + "\n";
}

export interface CmdHighlight {
  /** `cmd-hl-wrap`, plus `cmd-hl-on` once a paint has succeeded. */
  wrapClassName: string;
  backdropRef: RefObject<HTMLDivElement | null>;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  /** Feed to the backdrop's dangerouslySetInnerHTML. */
  backdropHTML: string;
  /** Wire to the textarea's onScroll. */
  onScroll: () => void;
}

export function useCmdHighlight(value: string): CmdHighlight {
  const backdropRef = useRef<HTMLDivElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const [active, setActive] = useState(false);

  // useMemo replaces the vanilla rAF coalescing: the escape+regex pass is O(n)
  // in the composer's length, and this way it re-runs once per value change
  // instead of once per render (React already coalesces renders itself).
  const backdropHTML = useMemo(() => cmdHighlightHTML(value), [value]);

  const sync = useCallback(() => {
    const ta = textareaRef.current;
    const bd = backdropRef.current;
    if (!ta || !bd) return false;
    // the -2 accounts for the two 1px borders shared by style.css's unioned
    // metric rule for #steer-text/#steer-text-backdrop
    bd.style.right = ta.offsetWidth - ta.clientWidth - 2 + "px";
    bd.scrollTop = ta.scrollTop;
    return true;
  }, []);

  // Layout effect, not an effect: the gutter must be right in the same frame
  // the new backdrop text paints, or a wrap-point change flashes.
  useLayoutEffect(() => {
    let ok = false;
    try {
      ok = sync();
    } catch {
      ok = false;
    }
    // gate color:transparent on a paint having actually landed — see (4)
    setActive(ok);
  }, [backdropHTML, sync]);

  const onScroll = useCallback(() => {
    // deliberately not coalesced — see (3)
    sync();
  }, [sync]);

  return {
    wrapClassName: active ? "cmd-hl-wrap cmd-hl-on" : "cmd-hl-wrap",
    backdropRef,
    textareaRef,
    backdropHTML,
    onScroll,
  };
}

// ------------------------------------------------------------- insertAtCursor

export interface InsertOptions {
  /**
   * Treat a textarea that has LOST focus as stale cursor state and append at
   * the end instead. The image-attachment call site passes this: an upload can
   * resolve after the user has moved on, and splicing into a selection nobody
   * is looking at drops text in a random place.
   */
  ownLineWhenUnfocused?: boolean;
  /**
   * Close a gap against the preceding non-whitespace character with a single
   * space instead of a newline — the command-chip call site, where a chip lands
   * inline with a trailing space rather than on its own line.
   */
  inlineGap?: boolean;
}

export interface Inserted {
  value: string;
  /** Where the caret belongs afterwards. */
  pos: number;
}

/**
 * The pure half of app.js:2894. Splices `text` in at [start,end), applying the
 * gap rule: without `inlineGap`, mid-line text gets its own leading "\n" so it
 * cannot glue onto existing prose (the image-reference case).
 */
export function insertAt(
  current: string,
  start: number,
  end: number,
  text: string,
  opts: InsertOptions = {},
): Inserted {
  const prevChar = start > 0 ? current[start - 1] : "";
  let gap = "";
  if (prevChar) {
    if (opts.inlineGap) {
      if (!/\s/.test(prevChar)) gap = " ";
    } else if (prevChar !== "\n") {
      gap = "\n";
    }
  }
  const inserted = gap + text;
  return {
    value: current.slice(0, start) + inserted + current.slice(end),
    pos: start + inserted.length,
  };
}

/**
 * The DOM half. Reads the live selection off `el`, computes the new value, and
 * hands it to `setValue` — then restores the caret on the next frame, since in
 * React the textarea's value only changes when the re-render lands.
 *
 * A textarea's selectionStart/End are never null (0 if never focused), so no
 * fallback is needed there.
 */
export function insertIntoTextarea(
  el: HTMLTextAreaElement | null,
  text: string,
  setValue: (v: string) => void,
  opts: InsertOptions = {},
): void {
  if (!el) return;
  const useEnd = !!opts.ownLineWhenUnfocused && document.activeElement !== el;
  const start = useEnd ? el.value.length : el.selectionStart;
  const end = useEnd ? el.value.length : el.selectionEnd;
  const { value, pos } = insertAt(el.value, start, end, text, opts);
  setValue(value);
  requestAnimationFrame(() => {
    if (!el.isConnected) return;
    try {
      el.selectionStart = el.selectionEnd = pos;
    } catch {
      // a textarea swapped out from under us mid-frame — the value landed,
      // which is the part that matters
    }
  });
}
