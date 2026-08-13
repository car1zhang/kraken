// Clickable code refs — the ONE place that decides whether a scrap of text is
// a reference to a file in this repo, and the only place that turns one into a
// {path, line, col} the editor endpoint can act on.
//
// Two producers feed it and they must agree, which is why the rule lives here
// rather than in either of them:
//
//   1. a backticked span in agent-written markdown (`internal/api/board.go:37`)
//      — lib/markdown.ts wraps the ones this module accepts;
//   2. a `data-code` attribute a hermes diagram put on a component `<g>` or an
//      edge `<path>` (.claude/skills/aim/assets/hermes-contract.md sanctions
//      exactly that one attribute).
//
// The markdown producer is why parseCodeRef is DELIBERATELY conservative. It is
// handed every code span on every doc the board renders, and a false positive
// there is not a cosmetic slip: it paints a clickable affordance on a word that
// is not a file, and the click then travels to a POST. So the accept list is
// shapes that can only be a path: a `/` and a real extension, optionally plus
// a `:line[-end|:col]` — or, with no `/`, a real extension AND a `:line`,
// because one bare component with no position is how prose NAMES a file rather
// than points into one. Everything that merely LOOKS colon-separated (host and
// port, a clock time, `key:value`, a CSS selector) is refused by name.
//
// Nothing here rewrites the token. `parseCodeRef` returns a VERBATIM substring
// of what it was given — a surrounding trim, then one cut at the diagnostic
// boundary — which is what lets markdown.ts put the value straight into an
// attribute: the span text it passes in has already been through esc(), so a
// verbatim substring of it is already escaped, and neither the trim nor that
// cut can land inside an escape entity, since no entity contains whitespace or
// a `:`. Resolving the path against a checkout is the server's job
// (internal/api's editor-open handler), not this module's — it never touches
// the filesystem and never decides whether the file exists.

/** A ref, split into the parts the editor endpoint takes. */
export interface CodeRefTarget {
  path: string;
  line?: number;
  col?: number;
}

/**
 * `path[:line[-end|:col]]`, anchored. The first group is lazy so the engine
 * finds the EARLIEST split that leaves a legal suffix — without that,
 * `BoardView.swift:794:25` would keep `:794` inside the path and read 25 as the
 * line.
 */
const POS = /^(.*?)(?::(\d+)(?:-(\d+)|:(\d+))?)?$/;

/**
 * A real extension: a dot, then a LETTER. The leading-letter requirement is the
 * whole host:port defence — `127.0.0.1` ends in `.1`, which is not an
 * extension, so the left side of `127.0.0.1:7777` is extensionless and the ref
 * is refused.
 */
const EXT = /\.[A-Za-z][A-Za-z0-9_+-]*$/;

/**
 * A set artifact (`51-code-map.md`). These are the docs the board is DISPLAYING
 * — they are named in their own prose constantly, they live in the cell's
 * artifacts directory rather than in the checkout, and opening one in an editor
 * is never what the reader meant by clicking it. What the reader DID mean is
 * resolved by `lib/artifactRefs.ts`: the overlays' click handlers check
 * membership in the job's artifact list before they reach codeRefTarget. At
 * render time this rule alone keeps the span plain — nothing consults the
 * artifact list there — and the iframe's `data-code` path has no artifact-ref
 * stage at all.
 */
const SET_ARTIFACT = /^\d\d-[^/]*\.md$/;

/**
 * The normalized `data-code` value for a backticked span's text, or null.
 *
 * The return is the leading path token VERBATIM — no rewriting, no
 * canonicalization, no trailing-slash trimming. The input is trimmed of
 * surrounding whitespace, and then ONE cut is made, at the first `:` followed
 * by whitespace: that is where a compiler or linter line stops being a location
 * and starts being a message (`ios/K/B.swift:794:25: error: …` →
 * `ios/K/B.swift:794:25`). What survives that cut is returned as-is or refused
 * whole; nothing else is removed from it.
 */
export function parseCodeRef(spanText: string): string | null {
  const s = spanText.trim();
  if (!s) return null;

  // `file:line:col: message` — keep the location, drop the diagnostic. Done
  // BEFORE the whitespace check below, which is what makes a diagnostic line
  // acceptable and `2026-07-30 19:47` (a space, but no `: `) not.
  const cut = s.search(/:\s/);
  const head = cut === -1 ? s : s.slice(0, cut);
  if (!head) return null;

  // Internal whitespace before the position suffix: prose in backticks, not a
  // ref. Prose is the overwhelmingly common case in these docs.
  if (/\s/.test(head)) return null;
  // Glob and brace metacharacters: `~/.kraken/logs/*.jsonl` names a SET of
  // files, and the endpoint opens exactly one.
  if (/[{}*?]/.test(head)) return null;
  // A URL (`https://…`, `//host/share`). The server would refuse it too, but a
  // link-shaped token must not even wear the affordance.
  if (head.includes("//")) return null;
  // Absolute paths are the server's refusal as well — everything it opens is
  // relative to a resolved checkout root.
  if (head.startsWith("/")) return null;
  // CSS-ish: `.btn:active`, `:hover`. The digit exemption keeps a decimal-ish
  // leading token from being refused by this rule specifically rather than by
  // the extension rule that actually applies to it.
  if (/^[.:](?![0-9])/.test(head)) return null;

  const m = POS.exec(head);
  if (!m) return null;
  const p = m[1] ?? "";
  if (!p) return null;
  // `..` traversal, in any position. The server refuses it too; refusing here
  // means it never becomes a click.
  if (p.split("/").some((seg) => seg === "..")) return null;

  // The extension gate, applied to the BASENAME so that a dotted directory
  // cannot lend its dot to an extensionless file. It is what refuses
  // `127.0.0.1:7777` (dotted-numeric) and `org_id:123` (extensionless).
  const base = p.slice(p.lastIndexOf("/") + 1);
  if (!EXT.test(base)) return null;
  if (SET_ARTIFACT.test(p)) return null;

  // A BASENAME-ONLY ref requires a `:line`. `cell.go:553` is a ref; a bare
  // `cell.go` is not, and neither is `Makefile` — one path component with no
  // position is how prose NAMES a file ("see cell.go", "the Makefile"), not how
  // it points into one, and the endpoint would have to guess which of the N
  // tracked files with that basename was meant. A path carrying a `/` is
  // exempt: `web/_ui/src/App.tsx` already says which file it is, so it opens
  // file-level with no line.
  if (!p.includes("/") && !m[2]) return null;

  return head;
}

/**
 * The ref an element sits inside, parsed — or null if it sits inside none.
 *
 * `closest` rather than a direct attribute read because the clickable thing is
 * rarely the thing carrying the ref: a diagram's `data-code` lives on the
 * component `<g>` while the click lands on the `<rect>` or the `<text>` inside
 * it. The attribute value goes back through parseCodeRef, so an attribute a
 * hand edit made malformed is inert rather than half-honoured.
 *
 * A `start-end` range yields `line` = start: the endpoint puts a cursor
 * somewhere, and the top of the range is where reading begins.
 */
export function codeRefTarget(el: Element): CodeRefTarget | null {
  const host = el.closest("[data-code]");
  if (!host) return null;
  const raw = host.getAttribute("data-code");
  if (raw === null) return null;
  const value = parseCodeRef(raw);
  if (value === null) return null;
  const m = POS.exec(value);
  if (!m) return null;
  const target: CodeRefTarget = { path: m[1] ?? "" };
  if (m[2]) target.line = Number(m[2]);
  if (m[4]) target.col = Number(m[4]);
  return target;
}

/** The id of the one style element wireCodeRefs injects. */
const STYLE_ID = "code-ref-style";

/**
 * The act-able cursor, verbatim from style.css's pointer list — the ring
 * reticle with a `pointer` fallback (web/cursorpolicy_test.go and
 * styles/cursors.test.ts both pin that assignment for the board's own sheet).
 *
 * It has to be repeated as a string here rather than added to that list because
 * this style lands in ANOTHER document: the artifact iframe's, which loads the
 * artifact's own stylesheet and never style.css. The plain `cursor: pointer`
 * ahead of it is the ordinary CSS two-declaration fallback for a renderer that
 * refuses the data URI, and it is also what keeps this rule readable as "a
 * pointer cursor" at a glance.
 *
 * Two colours because the host document's theme is unknowable from here: the
 * artifact chooses its own, so the art follows `prefers-color-scheme` instead.
 */
const RING_DARK =
  `url('data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><path d="M12 3 V6 M12 18 V21 M3 12 H6 M18 12 H21" stroke="%23f2edff" stroke-width="2" stroke-linecap="round"/><circle cx="12" cy="12" r="3.5" fill="none" stroke="%23f2edff" stroke-width="1.8"/></svg>') 12 12, pointer`;
const RING_LIGHT =
  `url('data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><path d="M12 3 V6 M12 18 V21 M3 12 H6 M18 12 H21" stroke="%231a1724" stroke-width="2" stroke-linecap="round"/><circle cx="12" cy="12" r="3.5" fill="none" stroke="%231a1724" stroke-width="1.8"/></svg>') 12 12, pointer`;

/**
 * `[data-code][title]`, not a bare `[data-code]`: `decorate` sets the title on
 * exactly the values parseCodeRef accepts, so the attribute the reader can
 * already see is what gates the cursor — a hand-edited or malformed
 * `data-code` gets no title, no act-able cursor, and a click that does nothing,
 * with no second piece of state to keep in step. `code.code-ref` needs no such
 * guard: markdown.ts only ever puts that class on a span it has already
 * accepted.
 */
const STYLE_TEXT =
  `[data-code][title], code.code-ref { cursor: pointer; cursor: ${RING_DARK}; }\n` +
  `@media (prefers-color-scheme: light) { [data-code][title], code.code-ref { cursor: ${RING_LIGHT}; } }\n`;

interface Wiring {
  handler: (ev: Event) => void;
  onOpen: (t: CodeRefTarget) => void;
}

/**
 * One wiring per document, so a second wireCodeRefs call on the same document
 * adopts the existing listener instead of adding a second one that would fire
 * the same click twice. A WeakMap rather than a flag on the document: an iframe
 * document dies with its frame, and nothing here should be what keeps it alive.
 */
const wired = new WeakMap<Document, Wiring>();

/** The Element a click landed on, or the element holding the text node it hit. */
function eventElement(ev: Event): Element | null {
  const t = ev.target as Node | null;
  if (!t) return null;
  if (t.nodeType === 1) return t as Element;
  return (t as ChildNode).parentElement;
}

function decorate(doc: Document): void {
  doc.querySelectorAll("[data-code]").forEach((el) => {
    const raw = el.getAttribute("data-code");
    const value = raw === null ? null : parseCodeRef(raw);
    // A malformed attribute gets NO title, deliberately: the title is the
    // element's only promise that a click will do something, and this one's
    // will not.
    if (value !== null) el.setAttribute("title", value);
  });
}

function ensureStyle(doc: Document): void {
  if (doc.getElementById(STYLE_ID)) return;
  const parent = doc.head ?? doc.documentElement ?? doc.body;
  if (!parent) return;
  // createElementNS against the host document's own namespace: an artifact
  // iframe may hold an SVG document, where `createElement` would mint a
  // null-namespace element the renderer ignores.
  const ns = doc.documentElement?.namespaceURI ?? null;
  const style = ns
    ? doc.createElementNS(ns, "style")
    : doc.createElement("style");
  style.setAttribute("id", STYLE_ID);
  style.textContent = STYLE_TEXT;
  parent.appendChild(style);
}

/**
 * Make every code ref in `doc` clickable. Returns the undo.
 *
 * ONE delegated listener on the document rather than a listener per ref: the
 * artifact documents this runs against are re-rendered wholesale (a diagram
 * card rebuilds its own SVG, a markdown body is replaced by innerHTML), and
 * per-element listeners would have to be re-hung on every one of those. A
 * delegated click also picks up refs that arrive AFTER this call, which is what
 * a live cell writing into an artifact does.
 *
 * Idempotent: calling it again on the same document re-decorates (new refs get
 * their titles) and re-points `onOpen`, but does not add a second listener or a
 * second style element. The re-point is latest-wins on purpose — the caller
 * re-wires precisely because its old callback closed over a stale slug.
 */
export function wireCodeRefs(
  doc: Document,
  onOpen: (t: CodeRefTarget) => void,
): () => void {
  decorate(doc);
  ensureStyle(doc);

  let wiring = wired.get(doc);
  if (wiring) {
    wiring.onOpen = onOpen;
  } else {
    const w: Wiring = {
      onOpen,
      handler: (ev: Event) => {
        const el = eventElement(ev);
        if (!el) return;
        const target = codeRefTarget(el);
        if (!target) return;
        w.onOpen(target);
      },
    };
    doc.addEventListener("click", w.handler);
    wired.set(doc, w);
    wiring = w;
  }

  const mine = wiring;
  return () => {
    // Guarded so a stale undo (the caller re-wired and is now tearing down the
    // OLD handle) cannot unhook the live wiring underneath the new one.
    if (wired.get(doc) === mine) {
      doc.removeEventListener("click", mine.handler);
      wired.delete(doc);
    }
    doc.getElementById(STYLE_ID)?.remove();
  };
}
