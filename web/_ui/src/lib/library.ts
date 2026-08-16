// The saved-artifact library's two pure decisions: WHICH stored row an open
// artifact URL is, and WHICH rows a filter box's text keeps.
//
// A LEAF module — no React, no fetch, one type import. Everything here is
// exported because a pure function nobody can name is a pure function nobody
// can test, and because both consumers live in other slices of the tree: the
// artifact viewer's ☆/★ head button is `libraryMatch(items, url) !== null` —
// SERVER state, never a local latch, so a failed save simply leaves the button
// unsaved — and the memory view's library mode filters its list through
// `filterLibrary`.

import type { WebLibraryItem } from "../api/types.ts";

/**
 * A library URL: `/api/library/artifact/<id>/<name>`.
 *
 * The id is the whole lookup and the name segment is display/extension only
 * (the wire contract says so, and the server serves the snapshot by id) — which
 * is why the name is not captured here and why a bare `/api/library/artifact/7`
 * still matches row 7. The digits must be a WHOLE segment: `/artifact/7x/…`
 * names no row and must come out as null rather than as row 7.
 */
const LIBRARY_URL_RE = /^\/api\/library\/artifact\/(\d+)(?:\/|$)/;

/**
 * A live-artifact URL: `/api/artifact/<slug>/<home>/<name>`.
 *
 * The same shape ArtifactOverlay's `artifactParts` recovers, restated here
 * rather than imported: this module is a leaf and importing it back out of a
 * React component would drag the whole viewer (and its isoscene compiler) into
 * every consumer of these two functions. Only the FIRST TWO segments split off,
 * for artifactParts' own reason — a cell-home artifact name may itself contain
 * slashes, and `(.+)` has to keep them.
 *
 * `home` is captured but deliberately NOT matched on: a row records the slug
 * and the name it was frozen under, and the same artifact reached through the
 * archive namespace is the same artifact.
 */
const ARTIFACT_URL_RE = /^\/api\/artifact\/([^/]+)\/([^/]+)\/(.+)$/;

/**
 * The library row an open artifact URL refers to, or null.
 *
 * Two URL shapes answer, and nothing else does — a `null` here is what makes
 * the viewer's toggle read "unsaved", so a URL it cannot parse must never
 * accidentally match a row.
 */
export function libraryMatch(
  items: WebLibraryItem[],
  url: string,
): WebLibraryItem | null {
  const lib = LIBRARY_URL_RE.exec(url);
  if (lib) {
    const id = Number(lib[1]);
    return items.find((it) => it.id === id) ?? null;
  }
  const art = ARTIFACT_URL_RE.exec(url);
  if (!art) return null;
  const slug = art[1];
  const name = art[3];
  return items.find((it) => it.slug === slug && it.name === name) ?? null;
}

/**
 * The rows a filter box's text keeps, in the ORDER THEY ARRIVED.
 *
 * The server sorts (saved_at DESC, ties id DESC) and the client never
 * re-sorts — a list that reorders itself as you type is a list you cannot aim
 * at. An empty or whitespace-only query is not a filter at all and hands the
 * input straight back.
 */
export function filterLibrary(
  items: WebLibraryItem[],
  q: string,
): WebLibraryItem[] {
  const needle = q.trim().toLowerCase();
  if (!needle) return items;
  return items.filter(
    (it) =>
      it.name.toLowerCase().includes(needle) ||
      it.slug.toLowerCase().includes(needle),
  );
}
