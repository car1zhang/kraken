import { useCallback, useEffect, useRef } from "react";

// The artifact viewer's next/prev pager: the ‹ n / N › group in .panel-head
// (index.html:123) and the ArrowLeft/ArrowRight half that drives the same one
// navigation command.

/**
 * `isTextEntry` (app.js:1832) — "is this keystroke someone typing?".
 *
 * The pager's arrow keys must never steal a caret move inside the feedback
 * composer, which sits pixels below the artifact you are reading about.
 *
 * Duck-typed rather than `instanceof HTMLElement`, and that is not laziness:
 * this same handler is attached to an artifact IFRAME's own document, whose
 * elements are instances of the FRAME's HTMLElement, not the parent window's.
 * `instanceof` is false across realms, so every input inside a rendered
 * artifact would read as "not a text entry".
 */
export function isTextEntry(el: unknown): boolean {
  const e = el as {
    tagName?: unknown;
    isContentEditable?: unknown;
  } | null;
  if (!e || typeof e.tagName !== "string") return false;
  if (e.isContentEditable === true) return true;
  const tag = e.tagName.toUpperCase();
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

/**
 * `artifactKeydown` (app.js:1844), as a hook.
 *
 * Registers the handler on the board document for as long as the caller is
 * mounted — the viewer's children only mount while it is open, so mount
 * lifetime replaces the vanilla `if (overlay.hidden) return`.
 *
 * It RETURNS the same handler because the listener gets attached twice: once
 * here, and again to each artifact iframe's own document (see ArtifactFrame in
 * ArtifactOverlay.tsx). A modified arrow is left alone — those are the
 * browser's (word-jump, history) and none of them mean "next artifact".
 */
export function useArtifactKeys(
  step: (delta: -1 | 1) => void,
): (ev: KeyboardEvent) => void {
  // the handler identity has to be STABLE (an iframe listener is attached on
  // load and removed on unmount, and a changing function would leak one per
  // render), so the current step is reached through a ref rather than closed
  // over
  // render-phase ref write, deliberately: a latest-value mirror, read only
  // from the keydown handler (never during the render that writes it), so a
  // StrictMode double render or a discarded pass writes the same value twice
  const latest = useRef(step);
  latest.current = step;
  const onKey = useCallback((ev: KeyboardEvent) => {
    if (ev.key !== "ArrowLeft" && ev.key !== "ArrowRight") return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
    if (isTextEntry(ev.target)) return;
    ev.preventDefault();
    latest.current(ev.key === "ArrowRight" ? 1 : -1);
  }, []);
  useEffect(() => {
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onKey]);
  return onKey;
}

export interface ArtifactPagerProps {
  /** 0-based position of the open artifact within `count`. */
  idx: number;
  count: number;
  onStep: (delta: -1 | 1) => void;
}

/**
 * `renderPager` (app.js:1733) — hidden outright for a sequence of one (an
 * unresolvable chip, or a job with a single artifact: behaviour is then exactly
 * what it was before the pager existed), and each end button disabled at its
 * end, because the step CLAMPS rather than wrapping.
 *
 * `#panel-pos` is an aria-live region and the vanilla renderer had to compare
 * before writing — it runs on every 3s board poll, and assigning textContent
 * replaces the text node, which is an announceable mutation even when the
 * string is identical (web/artifactpager_test.go pins this). Here that falls
 * out of reconciliation: React leaves a text node alone when its value has not
 * changed, so a poll that moves nothing announces nothing.
 */
export function ArtifactPager({ idx, count, onStep }: ArtifactPagerProps) {
  return (
    <span className="panel-pager" id="panel-pager" hidden={count < 2}>
      <button
        className="btn ghost pg"
        id="panel-prev"
        aria-label="previous artifact"
        title="previous artifact (←)"
        disabled={idx <= 0}
        onClick={() => onStep(-1)}
      >
        ‹
      </button>
      <span className="pg-pos" id="panel-pos" aria-live="polite">
        {count ? `${idx + 1} / ${count}` : ""}
      </span>
      <button
        className="btn ghost pg"
        id="panel-next"
        aria-label="next artifact"
        title="next artifact (→)"
        disabled={idx >= count - 1}
        onClick={() => onStep(1)}
      >
        ›
      </button>
    </span>
  );
}
