// The mental model's client-side leaf: the wire types, the two pure decisions
// the capture surfaces make, and the one post-render DOM sweep.
//
// A LEAF module — no React, no fetch, no imports at all. Everything here is
// exported for lib/library.ts's reason: a pure function nobody can name is a
// pure function nobody can test, and each consumer lives in a different slice
// of the tree — the artifact viewer's ✓ read head button is
// `artifactReadMatch(items, url) !== null`, its selection affordance is
// `selectionText() !== ""`, and its body sweep is `decorateMMTables`.
//
// Every mark is SERVER state, exactly as ☆ save is: there is no local "I
// clicked it" latch anywhere, so a refused POST leaves the control reading
// unmarked and an invalidation made by the daemon's verify pass drops the
// button back to "not read" on the next poll without anything here noticing.

/**
 * A code reading's anchor — where in which repo the frozen text was read, and
 * what the last verify pass found there. Mirrors `webMMItem.anchor` (a
 * pointer, omitempty), so everything the server omits when empty is optional
 * here.
 */
export type MMAnchor = {
  repo: string;
  path: string;
  /** Set once, when a verify pass followed a rename. */
  orig_path?: string;
  start_line: number;
  end_line: number;
  anchor_sha?: string;
  /** origin-main | merge-base | head | dirty. */
  anchor_kind?: string;
  text_sha?: string;
  checked_at?: number;
  checked_sha?: string;
  /** A failed READ (fetch, resolve, git). Never an invalidation. */
  check_error?: string;
  /** The text was found in more than one place and the nearest one won. */
  ambiguous?: boolean;
  invalid_sha?: string;
  invalid_pr?: string;
};

/**
 * One thing carl knows, as `GET /api/mm` serves it.
 *
 * `invalidated_at` absent is the ONLY spelling of "live" (the server omits the
 * three invalid_* keys when the item is live), which is why every predicate
 * here tests the key's absence rather than a boolean the wire does not carry.
 *
 * The snapshot BYTES of a whole-artifact mark are deliberately not on this
 * type: they are served by `/api/mm/snapshot?id=N` so that a list of 400 items
 * is not also a list of 400 frozen documents.
 */
export type MMItem = {
  id: number;
  /** decision | reading | note. */
  kind: string;
  /** question | artifact | artifact-element | code | agent-output | manual. */
  source: string;
  slug: string;
  source_ref: string;
  content: string;
  context?: string;
  /** unix MILLIS. */
  created_at: number;
  episode_id?: number;
  /** unix MILLIS; ABSENT means live. */
  invalidated_at?: number;
  invalid_reason?: string;
  invalid_detail?: string;
  anchor?: MMAnchor;
};

/** GET /api/mm. Always an array, never null; newest first. */
export type MMPage = {
  items: MMItem[];
};

/** What every successful create answers with. */
export type MMCreated = {
  id: number;
  episode?: number;
  /** True when the identical live mark already existed — see the route family. */
  already?: boolean;
};

/**
 * A live-artifact URL: `/api/artifact/<slug>/<home>/<name>`.
 *
 * The same expression `libraryMatch` and `artifactParts` recover, restated
 * here rather than imported for lib/library.ts's exact reason: this module is
 * a leaf, and importing the overlay back out of it would drag the whole viewer
 * into every consumer. Only the FIRST TWO segments split off — a cell-home
 * artifact name may itself contain slashes, and `(.+)` has to keep them.
 *
 * `home` is captured but deliberately NOT matched on: an item records the slug
 * and the name it was frozen under, and the same artifact reached through the
 * archive namespace is the same artifact.
 */
const ARTIFACT_URL_RE = /^\/api\/artifact\/([^/]+)\/([^/]+)\/(.+)$/;

/** The server stores `source_ref` with its leading slash trimmed; so do we. */
const trimLead = (s: string) => s.replace(/^\/+/, "");

/**
 * The LIVE whole-artifact reading an open artifact URL is, or null.
 *
 * `libraryMatch`'s contract, with two differences that are both about what a
 * mark means. Only the `/api/artifact/…` shape answers — a library snapshot
 * URL carries no {slug, home, name} for `POST /api/mm/artifact` to address, so
 * it can neither be marked nor matched. And only LIVE items match: an
 * invalidated reading is history the memory pane keeps, not a claim that the
 * bytes on screen are ones carl has read — the button must drop back to
 * unmarked the moment the verify pass says the artifact moved.
 */
export const artifactReadMatch = (
  items: MMItem[],
  url: string,
): MMItem | null => {
  const m = ARTIFACT_URL_RE.exec(url);
  if (!m || !m[1] || !m[3]) return null;
  const slug = m[1];
  const name = trimLead(m[3]);
  return (
    items.find(
      (it) =>
        it.source === "artifact" &&
        !it.invalidated_at &&
        it.slug === slug &&
        trimLead(it.source_ref) === name,
    ) ?? null
  );
};

/**
 * The current window selection as text, trimmed; `""` when there is none.
 *
 * A collapsed selection stringifies to `""` on its own, so the explicit check
 * is only for the environments where `getSelection` is absent entirely.
 */
export const selectionText = (): string => {
  if (typeof window === "undefined" || !window.getSelection) return "";
  return (window.getSelection()?.toString() ?? "").trim();
};

/**
 * The text one element mark freezes.
 *
 * `innerText` is what the contract names — it is the RENDERED text, so a table
 * comes out row-per-line with cells tab-separated rather than as the run of
 * markup whitespace `textContent` yields. jsdom implements no layout and so
 * has no `innerText` at all, which is why the fallback exists: the two agree
 * on which table is which, and the sweep's mark/compare pair uses one
 * derivation, so a run under either engine is self-consistent.
 */
const elementText = (el: HTMLElement): string => {
  // typed through a widening cast, not `el.innerText`: the DOM lib declares it
  // non-optional, and it is genuinely absent under jsdom.
  const rendered = (el as { innerText?: string }).innerText;
  return rendered ?? el.textContent ?? "";
};

/**
 * Keep only unicode letters and digits, in order, case preserved.
 *
 * The exact mirror of `core.ProjectAlnum` (internal/core/anchor.go), down to
 * the two categories: `\p{L}` is Go's `unicode.IsLetter` and `\p{Nd}` is its
 * `unicode.IsDigit` — `\p{N}` would additionally admit Nl/No and put the two
 * projections out of step on exactly the characters nobody would think to
 * test.
 *
 * It exists here because the SERVER now stores something different from what
 * the browser handed it. `POST /api/mm/element` locates the rendered capture in
 * the artifact's markdown SOURCE and stores those source lines, so a marked
 * table comes back as `| tier | cells |\n|---|---|\n…` while the rendered
 * `<table>` in front of the reader still says `tier\tcells\n…`. The two can
 * never be equal as strings — the projection is the only comparison they can
 * both pass, which is why the server's own verify pass compares through it too
 * (`jobs.mmElementPresent`).
 */
export const projectAlnum = (s: string): string =>
  s.replace(/[^\p{L}\p{Nd}]/gu, "");

/** The one line-ending policy the strict fallback shares with `jobs.mmNormalize`. */
const normalizeEOL = (s: string) => s.replace(/\r\n/g, "\n");

/**
 * Do `want`'s code points appear in `have`, in order, not necessarily adjacent?
 *
 * `jobs.mmSubsequence` in TypeScript, iterated by CODE POINT (`for…of`) rather
 * than by UTF-16 unit, so a surrogate pair is one character on this side too.
 */
const subsequence = (have: string, want: string): boolean => {
  const w = Array.from(want);
  if (w.length === 0) return true;
  let i = 0;
  for (const r of have) {
    if (r !== w[i]) continue;
    if (++i === w.length) return true;
  }
  return false;
};

/**
 * Is `text` — one element's RENDERED text — the passage that `content` froze?
 *
 * `jobs.mmElementPresent` with its two arguments swapped, and the swap is the
 * whole idea. The verify pass asks "is the marked content still somewhere in
 * the current artifact?"; this asks "is what I am looking at inside what was
 * marked?". Both are the same containment through the same projection, run in
 * opposite directions, and one predicate covers both shapes a stored content
 * can have:
 *
 *   LOCATED (today's writes) — content is the markdown SOURCE. The renderer
 *   only ever DELETES alphanumerics on the way to the screen (a fence's
 *   language tag, an ordered-list marker, a link's url), so the rendered
 *   projection is a SUBSEQUENCE of the source's, which is the second rung.
 *   LEGACY (marks made before the locate shipped) — content IS the rendered
 *   text, so the two projections are equal and the first rung answers.
 *
 * The subsequence rung is deliberately permissive in one direction: a small
 * table whose projection happens to thread through a much larger marked block
 * reads as marked. That is the trade the server already makes on the same
 * comparison, and it errs toward "you have read this", which is recoverable by
 * looking at the memory pane — where the opposite error, the one this replaces,
 * was a ✓ that could never light for any table at all.
 */
const markedBy = (content: string, text: string): boolean => {
  const want = projectAlnum(text);
  if (want === "") {
    // No letters and no digits to compare — fall back to the strict form, and
    // never let an element with NO text at all match: nothing could have
    // frozen it, and `"".includes` would light every empty table in the
    // document the moment any mark existed.
    return text.trim() !== "" && normalizeEOL(content).includes(normalizeEOL(text));
  }
  const have = projectAlnum(content);
  return have.includes(want) || subsequence(have, want);
};

/**
 * The click handler's current callback, per affordance button.
 *
 * A WeakMap rather than a listener re-bound on each sweep: the sweep is
 * idempotent by contract ("running twice adds nothing"), and a second
 * `addEventListener` would fire one click twice. The listener is hung once,
 * when the button is created, and reads whatever this map holds — so a later
 * sweep can hand it a fresh closure without touching the DOM.
 */
const tableMarkers = new WeakMap<HTMLElement, (text: string) => void>();

/**
 * Dress every `<table>` under `bodyEl` with a corner ✓ that marks exactly that
 * table's text internalized.
 *
 * The `decorateArtifactRefs` shape: a post-render sweep, because the markdown
 * renderer is pure and cannot know which of its tables carl has already read.
 * IDEMPOTENT in both directions — a second run over the same body adds no
 * second wrapper, no second button and no second listener, and a table that
 * has LEFT `markedTexts` (its mark was removed, or the verify pass invalidated
 * it) is undressed back to the unmarked state in the same pass, so the
 * affordance never outlives the fact it claims.
 *
 * `markedTexts` carries what the SERVER stored, which since the locate
 * amendment is markdown source rather than the rendered text this sweep hands
 * over — so membership is `markedBy`'s projected containment, never string
 * equality. Read that function before touching the comparison.
 *
 * The wrapper exists because a `<button>` is not valid content inside a
 * `<table>` — the parser hoists one out — so the affordance is positioned
 * against a `<div>` the sweep inserts around the table on first contact.
 */
export const decorateMMTables = (
  bodyEl: HTMLElement,
  markedTexts: string[],
  onMark: (text: string) => void,
): void => {
  // A Set of raw strings used to live here, on the premise that what came back
  // in `markedTexts` was the text this sweep had handed over. The server no
  // longer keeps that promise: it locates the capture in the artifact's
  // markdown SOURCE and stores the source lines, so a raw comparison against
  // the rendered table is one the two sides can never pass. `markedBy` is the
  // comparison that survives it, and it is a scan rather than a lookup because
  // containment has no hash — with a handful of tables and a handful of marks
  // per artifact, on a sweep that is already fingerprinted against re-running.
  for (const table of Array.from(bodyEl.querySelectorAll("table"))) {
    // A table INSIDE another table gets no affordance. Markdown cannot nest
    // tables, but raw HTML in a `.md` artifact can, and the wrapper plus its
    // button would land in the outer table's own `<td>`: document order makes
    // the first sweep clean, and every sweep after it reads the outer table's
    // text with a stray ✓ in it — so a marked outer table silently stops
    // matching `markedTexts`, and a click on it freezes text carrying a glyph
    // that is in no document.
    if (table.parentElement?.closest("table")) continue;
    const host = table.parentElement;
    let wrap: HTMLElement;
    let btn: HTMLButtonElement;
    if (host && host.classList.contains("mm-tblwrap")) {
      wrap = host;
      const found = wrap.querySelector<HTMLButtonElement>(".mm-tblmark");
      if (!found) continue; // hand-mangled DOM; leave it alone
      btn = found;
    } else {
      wrap = bodyEl.ownerDocument.createElement("div");
      wrap.className = "mm-tblwrap";
      table.parentNode?.insertBefore(wrap, table);
      wrap.appendChild(table);
      btn = bodyEl.ownerDocument.createElement("button");
      btn.type = "button";
      btn.className = "mm-tblmark";
      btn.textContent = "✓";
      btn.addEventListener("click", (ev) => {
        // The body carries ONE delegated click handler for code and artifact
        // refs (ArtifactOverlay's onBodyClick) — this press is neither.
        ev.preventDefault();
        ev.stopPropagation();
        tableMarkers.get(btn)?.(elementText(table));
      });
      wrap.insertBefore(btn, table);
    }
    tableMarkers.set(btn, onMark);
    const text = elementText(table);
    const on = markedTexts.some((content) => markedBy(content, text));
    wrap.classList.toggle("mm-marked", on);
    btn.classList.toggle("on", on);
    btn.setAttribute("aria-pressed", on ? "true" : "false");
    // The marked state is a STATEMENT, not a toggle: this affordance's only
    // verb is `onMark`, which re-posts and gets `already` back, and dropping a
    // mark needs an item id this sweep is never handed. Unmarking lives in the
    // memory view's pane, and the strings say so rather than promising a click
    // that changes nothing.
    btn.title = on
      ? "you have read this table — already marked; drop it from the memory pane"
      : "mark this table internalized";
    btn.setAttribute(
      "aria-label",
      on ? "this table is already marked read" : "mark this table read",
    );
  }
};
