// The mind button's pure half: which SOURCE a highlighted passage came from,
// read off the DOM ancestry the board's reading surfaces stamp.
//
// A LEAF module — no React, no fetch. Every surface carl reads prose on marks
// its own root with `data-mind-*` attributes (the artifact viewer's
// `#panel-body`, each block of the steer modal's transcript panel and of the
// thread overlay, each row of the Model pane), and this walks up from the
// selection to the innermost one. The surfaces own the attributes; this file
// owns their meaning, so a new surface joins by stamping the same names.
//
// The source travels to `POST /api/mm/capture` as `jobs.CaptureSource`
// (internal/jobs), which is why the key spellings here are its JSON tags.

/** `jobs.CaptureSource` on the wire. Empty fields are omitted, never sent as "". */
export type CaptureSource = {
  kind: "artifact" | "transcript" | "dir" | "item" | "manual";
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

/**
 * The element `node` sits in: itself when it is one, else its parent element.
 * A text node (where a selection's anchor almost always lands) has no
 * attributes of its own, so the walk starts one level up.
 */
const elementOf = (node: Node | null): Element | null => {
  if (!node) return null;
  if (node.nodeType === 1) return node as Element;
  return node.parentElement;
};

/**
 * One step up the ancestry, crossing out of a same-origin iframe.
 *
 * An html/svg artifact card renders in an iframe, so a selection inside it is
 * anchored in the FRAME's document, whose root has no parent element. The
 * frame element itself lives inside the viewer's `#panel-body`, which is what
 * carries the artifact's attributes, so the walk continues from it. A
 * cross-origin frame throws on `frameElement`; that is the end of the walk.
 */
const parentOf = (el: Element): Element | null => {
  if (el.parentElement) return el.parentElement;
  try {
    const frame = el.ownerDocument?.defaultView?.frameElement ?? null;
    return frame;
  } catch {
    return null;
  }
};

/** The innermost element at or above `anchor` carrying attribute `attr`. */
const closestWith = (anchor: Node | null, attr: string): Element | null => {
  for (let el = elementOf(anchor); el; el = parentOf(el)) {
    if (el.hasAttribute(attr)) return el;
  }
  return null;
};

/** An attribute's value, or undefined when absent OR empty — never "". */
const attr = (el: Element, name: string): string | undefined => {
  const v = el.getAttribute(name);
  return v ? v : undefined;
};

/** Drop undefined keys so the posted JSON carries only what was stamped. */
const compact = (s: CaptureSource): CaptureSource => {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(s)) {
    if (v !== undefined) out[k] = v;
  }
  return out as CaptureSource;
};

/**
 * The source of the INNERMOST ancestor of `anchor` carrying `data-mind-kind`,
 * or null when there is none.
 *
 *   artifact   → {kind, slug, home, name, sha}   from data-mind-slug/home/name/sha
 *   transcript → {kind, session, uuid, tid?}     from data-mind-session/uuid/tid
 *   item       → {kind, item_id}                 from data-mind-item-id
 *   dir        → {kind, repo, dir}               from data-mind-repo/dir
 *   manual     → {kind}
 *
 * An unrecognised kind value answers null rather than a guess: the server
 * refuses any kind outside the five, and a guess would turn a typo in a
 * surface's markup into a capture filed under the wrong source.
 */
export function resolveSelectionSource(anchor: Node | null): CaptureSource | null {
  const el = closestWith(anchor, "data-mind-kind");
  if (!el) return null;
  const kind = el.getAttribute("data-mind-kind");
  switch (kind) {
    case "artifact":
      return compact({
        kind: "artifact",
        slug: attr(el, "data-mind-slug"),
        home: attr(el, "data-mind-home"),
        name: attr(el, "data-mind-name"),
        sha: attr(el, "data-mind-sha"),
      });
    case "transcript":
      return compact({
        kind: "transcript",
        session: attr(el, "data-mind-session"),
        uuid: attr(el, "data-mind-uuid"),
        tid: attr(el, "data-mind-tid"),
      });
    case "item": {
      const raw = attr(el, "data-mind-item-id");
      const id = raw === undefined ? NaN : Number(raw);
      return compact({
        kind: "item",
        item_id: Number.isFinite(id) ? id : undefined,
      });
    }
    case "dir":
      return compact({
        kind: "dir",
        repo: attr(el, "data-mind-repo"),
        dir: attr(el, "data-mind-dir"),
      });
    case "manual":
      return { kind: "manual" };
    default:
      return null;
  }
}

/**
 * The job a selection belongs to: the nearest `data-mind-slug` at or above
 * `anchor`, or "" when nothing up the ancestry names one. Independent of
 * `data-mind-kind` — a surface may name its job without naming a source (the
 * steer modal's root does), and the capture still needs to know whose zeus to
 * fork.
 */
export function selectionSlug(anchor: Node | null): string {
  const el = closestWith(anchor, "data-mind-slug");
  return el?.getAttribute("data-mind-slug") ?? "";
}
