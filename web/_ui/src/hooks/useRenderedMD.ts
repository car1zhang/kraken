import { useMemo } from "react";
import { renderMD } from "../lib/markdown.ts";

// A STABLE `dangerouslySetInnerHTML` payload.
//
// React does not diff that prop by its HTML string — `updateProperties`
// compares the prop VALUE by identity (`nextProp === lastProp`) and hands
// anything else to `setProp`, which calls `setInnerHTML` unconditionally. A
// `{ __html: … }` object literal written inline in JSX is a fresh object on
// every render, so it never equals the last one, so the element's entire
// subtree is torn down and re-parsed on EVERY render even when the HTML has
// not changed by a byte.
//
// The board re-renders on every 3s poll, which made that "every render" into
// "every tick": an open artifact was measured rebuilding 109 nodes every
// ~3s, taking the reader's text selection with it and putting the scroll
// offset at the mercy of whatever the browser re-derives for a subtree that
// no longer exists. ThreadOverlay already hand-rolls the same guard
// imperatively ("re-rendering identical markup every 3s would reset the
// scroll position mid-read") — this is that guard, stated once, for the
// declarative sites.
//
// Memoising the OBJECT is the whole fix: an unchanged string yields the same
// object, `nextProp === lastProp` holds, and React skips the node entirely.

/**
 * A `dangerouslySetInnerHTML` value whose identity changes only when `html`
 * does. Use for HTML assembled from more than one source; prefer
 * {@link useRenderedMD} when the source is plain markdown.
 *
 * Keyed on the STRING rather than on whatever produced it, deliberately: the
 * board hands every overlay a freshly parsed job object on each poll, so a
 * memo keyed on `job`/`form` would bust on every tick and rebuild the DOM
 * exactly as the inline literal did.
 */
export function useHTML(html: string): { __html: string } {
  return useMemo(() => ({ __html: html }), [html]);
}

/**
 * {@link useHTML} over `renderMD`, memoised on the SOURCE so a large artifact
 * is also parsed once rather than on every render.
 *
 * `null`/`undefined` renders as empty — `renderMD("")` is `""`, so a caller
 * that used to write `x ? renderMD(x) : ""` can pass `x` straight through.
 */
export function useRenderedMD(src: string | null | undefined): {
  __html: string;
} {
  return useMemo(() => ({ __html: renderMD(src ?? "") }), [src]);
}
