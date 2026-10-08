// The mental model's client-side leaf: the wire types and two small pure
// helpers.
//
// A LEAF module — no React, no fetch, no imports at all. The board's one-shot
// marks (the artifact viewer's ✓ read / mark selection, the per-table ✓ sweep
// `decorateMMTables`, the steer detail's ○ mark known, the questions panel's
// ◆ decision) are RETIRED (Q10): capture now goes through the mind button
// (lib/mind.ts, components/chrome/MindButton.tsx) into a mind thread that
// commits the item. `artifactReadMatch` and `selectionText` stay exported
// because they are still pure and still pinned by their own tests; no board
// surface calls them any more.

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
  // ---- schema v33, the mind layer. Optional on this side because a legacy
  // server omits them and a test fixture need not spell them; the v33 server
  // sends every key on every row ("" / 0 / null for a legacy row).
  /** A one-line claim; "" on a legacy row, which then headlines its content. */
  title?: string;
  /** universal | repo. */
  scope?: string;
  repo?: string;
  /** Repo-relative; "" is the repo root. */
  dir?: string;
  /** The highlighted text the item was captured from, verbatim. */
  quote?: string;
  /** The mind thread's session that committed it. */
  thread_session?: string;
  /** unix MILLIS; 0 = never revised. */
  revised_at?: number;
  /**
   * Where the captured text came from (`jobs.CaptureSource`), or null on a
   * legacy row. A key of its own because `source` above is already the item's
   * source KIND string.
   */
  capture_source?: MMCaptureSource | null;
};

/** `jobs.CaptureSource` as `/api/mm` serves it — lib/mind.ts's CaptureSource. */
export type MMCaptureSource = {
  kind: string;
  slug?: string;
  home?: string;
  name?: string;
  sha?: string;
  session?: string;
  uuid?: string;
  tid?: string;
  repo?: string;
  dir?: string;
  item_id?: number;
};

/** One contradiction flag, `GET /api/mm/flags`. Every `store.MMFlag` field. */
export type MMFlag = {
  id: number;
  item_id: number;
  against_id: number;
  job_id: number;
  slug: string;
  session: string;
  note: string;
  /** pending | raised | dismissed | resolved (amendment 2). */
  state: string;
  raised_by: string;
  raise_note: string;
  raised_at: number;
  resolved_at: number;
  /** "" | drop | keep. */
  resolution: string;
  created_at: number;
};

/** GET /api/mm/flags. */
export type MMFlagsPage = {
  flags: MMFlag[];
};

/** GET /api/mm/source?id=. Never a 5xx for a missing source. */
export type MMSourceView = {
  kind: string;
  available: boolean;
  changed: boolean;
  text: string;
  ext: string;
};

/** The query `GET /api/mm` takes beyond the bare list. Empty = no filter. */
export type MMQuery = {
  q?: string;
  repo?: string;
  dir?: string;
  scope?: string;
  live?: boolean;
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

