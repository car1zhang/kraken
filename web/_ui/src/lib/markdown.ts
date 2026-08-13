// The board's markdown renderer — a port of web/static/app.js's
// esc()/inlineMD()/renderMD() (app.js:90, :1195, :1203), verbatim except for
// paragraph folding (see renderMD) and the two inline rules it forces (see
// inlineMD).
//
// app.js is no longer served: web/embed.go embeds `_ui/dist` and serve.go's
// only file server is `fs.Sub(webAssets, "_ui/dist")`. It survives on disk as
// the historical source and for the Go port guards, so the divergences below
// are NOT a parity gap to close — app.js is deliberately not updated, and
// "restoring parity" here would reintroduce the bug carl reported.
//
// Verbatim was a decision, not laziness (artifacts/53-port-checklist.md, Q7).
//
// ONE DELIBERATE DEPARTURE FROM VERBATIM, added later: the code-span restore
// step wraps a span whose text is a code reference as
// `<code class="code-ref" data-code="…">` so the board can open it in an editor
// (lib/codeRefs.ts). It is recorded here rather than left to be discovered
// because "verbatim" above is otherwise a promise, and this breaks it.
//
// It is safe on the same argument the rest of the file rests on, not on a new
// one. The rule runs on ALREADY-ESCAPED text, like every transform here. It
// emits no `href` and no scheme, so it cannot be a navigation of any kind — the
// value lands in a data-* attribute that only this app's own code reads, and
// the click it enables is a same-origin POST to a route that validates the path
// itself. And it only ever adds attributes to a `<code>` element this function
// was already emitting: no rejected span's output changes by a single byte, and
// no span that was not already a code span becomes one.
//
// Two reasons the rest is verbatim:
//
//  1. SAFETY BY CONSTRUCTION. Everything this renders is agent-authored — a
//     zeus detail line, a thread block, a spec, an artifact file — and it is
//     inert because escaping happens FIRST and every transform afterwards
//     operates on already-escaped text. None of the four commands, the pipe
//     table syntax, or the emphasis markers involve HTML-special characters,
//     so matching post-escape is safe. Swapping in a real markdown library
//     would replace that one-line argument with a dependency's threat model.
//
//  2. IT IS THE SCREENSHOT CONTRACT. style.css is byte-identical to the
//     vanilla board's, and its `.md` rules are written against exactly this
//     element set (`<div class="md-table">` wrapping the table, inline
//     `style="text-align:…"` per cell, `<pre><code>` for fences). A different
//     emitter is a different screenshot. The divergences below change how
//     source lines GROUP into elements and which emphasis pairings are
//     accepted, never WHICH elements are emitted, so every `.md` rule still
//     applies to exactly the same element set.
//
// The subset: fenced code, `#`–`####` headings, ul/ol, blockquote, GFM pipe
// tables with alignment, inline code/bold/em, and `https?:` links only.
// Everything else falls through to a `<p>`.

import { parseCodeRef } from "./codeRefs.ts";

/** HTML-escape. Ported from app.js:90. */
export const esc = (s: unknown): string =>
  String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      })[c] ?? c,
  );

/**
 * A stand-in for a lifted-out code span. NUL can never appear in the input:
 * these strings come from `.md` files and JSON wire fields, and even if one did
 * the failure would be a mis-restored code span, never an escape hole — the
 * substitution happens after esc() and puts back only text esc() already saw.
 */
const CODE_HOLE = String.fromCharCode(0);

/**
 * Inline transforms, applied to ALREADY-ESCAPED text (app.js:1195).
 *
 * The link rule only accepts an `https?:` target: job files and specs are
 * hand-editable and agent-written, and esc() stops an attribute breakout but
 * not a `javascript:` scheme.
 *
 * Two divergences from app.js, both forced by renderMD's paragraph fold — which
 * hands this function a whole paragraph where app.js handed it one line, so a
 * lone `*` now has the rest of the paragraph to find a partner in:
 *
 *  1. CODE SPANS ARE LIFTED OUT before the emphasis rules run, and put back
 *     after. app.js emitted `<code>` first and then let the emphasis regexes
 *     scan straight through it, so two `` `*Job` `` spans in one paragraph
 *     paired their asterisks into an `<em>` that opened inside one `<code>` and
 *     closed inside the other. Per line that needed both spans on one line;
 *     folded, one per line is enough, and `*Job`/`*.jsonl` in hard-wrapped
 *     prose is ordinary in this repo.
 *  2. THE EMPHASIS RULES REQUIRE NON-SPACE ON THE INNER EDGE (CommonMark's
 *     left/right-flanking rule). Without it `It scans *.ts files` + `and *.tsx
 *     files` folded into one paragraph renders `<em>.ts files under src and </em>`
 *     — the asterisks vanish and half the sentence goes italic.
 *
 * Both are strictly narrowing: they refuse pairings, never create one. `**two\n
 * words**` still bolds, because a soft break inside the span is untouched.
 *
 * The restore step is also where a code span that names a file gains its
 * `code-ref` class and `data-code` value — the header's one departure from
 * verbatim. Prose OUTSIDE backticks is never scanned for one: a bare
 * `web/serve.go:225` in a sentence stays plain text, because the span is the
 * author's own signal that the token is code and guessing without it would
 * paint clickable affordances across ordinary sentences.
 */
export function inlineMD(s: string): string {
  const code: string[] = [];
  const held = esc(s).replace(
    /`([^`]+)`/g,
    (_m, c: string) => CODE_HOLE + (code.push(c) - 1) + CODE_HOLE,
  );
  return held
    .replace(/\*\*(?=\S)([^*]+?)(?<=\S)\*\*/g, "<strong>$1</strong>")
    .replace(/\*(?=\S)([^*]+?)(?<=\S)\*/g, "<em>$1</em>")
    .replace(
      /\[([^\]]+)\]\((https?:[^)\s]+)\)/g,
      '<a href="$2" target="_blank" rel="noreferrer">$1</a>',
    )
    .replace(
      new RegExp(CODE_HOLE + "(\\d+)" + CODE_HOLE, "g"),
      (_m, i: string) => {
        const held = code[Number(i)] ?? "";
        // The ONE non-verbatim rule (see the header). parseCodeRef is handed
        // the ESCAPED span text and returns a verbatim substring of it, so the
        // value is already attribute-safe — esc() has already turned any `"`
        // into `&quot;` — and neither of the two things it removes (a
        // surrounding trim, one cut at a `:` before whitespace) can land inside
        // an escape entity, since no entity contains whitespace or a `:`.
        const ref = parseCodeRef(held);
        return ref === null
          ? `<code>${held}</code>`
          : `<code class="code-ref" data-code="${ref}">${held}</code>`;
      },
    );
}

/**
 * A GFM pipe table's separator row (`| --- | :--: | ---: |`). Hoisted because
 * two places have to agree on it: the branch that STARTS a table, and the
 * paragraph fold that has to stop before one.
 */
const TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

/**
 * Does this line open a block that is not a paragraph? `next` is the line after
 * it, needed only for the table case — a table is a line containing `|`
 * followed by a separator row, so the lookahead is what tells a table header
 * from prose with a pipe in it.
 *
 * This exists so the paragraph fold below stops at exactly the lines the main
 * loop would have claimed, rather than swallowing the heading after a
 * paragraph.
 *
 * Five of the six predicates are duplicated from the main loop's own branches
 * (the table one is shared as TABLE_SEP because it is the only non-obvious
 * one). Change a branch, change its twin here: a mismatch makes the fold
 * swallow a block instead of stopping at it, and there is no test at the seam
 * that would fail — the two are only ever compared by reading them.
 */
function startsBlock(line: string, next: string | undefined): boolean {
  return (
    line.startsWith("```") ||
    /^#{1,4}\s+/.test(line) ||
    /^\s*[-*]\s+/.test(line) ||
    /^\s*\d+[.)]\s+/.test(line) ||
    line.startsWith(">") ||
    line.trim() === "" ||
    (line.includes("|") && next !== undefined && TABLE_SEP.test(next))
  );
}

/**
 * Block-level render (app.js:1203). Returns an HTML string, rendered by the
 * callers through `dangerouslySetInnerHTML` — see the header comment for why
 * that is the faithful port rather than a smell.
 *
 * ONE deliberate divergence from app.js: consecutive prose lines fold into a
 * single `<p>`, so a paragraph break needs a BLANK line. app.js emitted one
 * `<p>` per source line, which meant a hard-wrapped paragraph — how most agents
 * write markdown, and how every editor soft-wraps it back — rendered as a stack
 * of one-line paragraphs with a margin between each. The rule is CommonMark's:
 * a single newline is a soft break (whitespace), a blank line ends the block.
 *
 * The lines are joined RAW and passed to inlineMD once, rather than rendered
 * per line and concatenated, so emphasis and code spans survive a soft break
 * (`**two\nwords**` is bold). That is also what forces inlineMD's two narrowing
 * rules — read them there before widening anything here.
 *
 * None of the four containers this output lands in sets `white-space` — `.md`
 * (ArtifactOverlay, DreamView), `.steer-detail` and `.steer-spec-body`
 * (SteerOverlay), `#thread-convo .blk-body` (ThreadOverlay) — so the joining
 * newline collapses the way HTML already collapses whitespace. Setting
 * `white-space: pre-wrap` on any of them would silently restore
 * one-line-per-source-line rendering; `.detail` on the card does exactly that
 * to the same zeus text in its clamped form, so the trap is one copy away.
 */
export function renderMD(src: string): string {
  const out: string[] = [];
  const lines = src.split("\n");
  let i = 0;
  let list: "ul" | "ol" | null = null;
  const closeList = () => {
    if (list) {
      out.push(`</${list}>`);
      list = null;
    }
  };
  while (i < lines.length) {
    const line = lines[i] ?? "";
    if (line.startsWith("```")) {
      closeList();
      const buf: string[] = [];
      i++;
      while (i < lines.length && !(lines[i] ?? "").startsWith("```")) {
        buf.push(lines[i] ?? "");
        i++;
      }
      i++;
      out.push(`<pre><code>${esc(buf.join("\n"))}</code></pre>`);
      continue;
    }
    const h = line.match(/^(#{1,4})\s+(.*)/);
    if (h) {
      closeList();
      const depth = (h[1] ?? "").length;
      out.push(`<h${depth}>${inlineMD(h[2] ?? "")}</h${depth}>`);
      i++;
      continue;
    }
    const ul = line.match(/^\s*[-*]\s+(.*)/);
    const ol = line.match(/^\s*\d+[.)]\s+(.*)/);
    if (ul || ol) {
      const want = ul ? "ul" : "ol";
      if (list !== want) {
        closeList();
        out.push(`<${want}>`);
        list = want;
      }
      out.push(`<li>${inlineMD((ul || ol)?.[1] ?? "")}</li>`);
      i++;
      continue;
    }
    if (line.startsWith(">")) {
      closeList();
      out.push(`<blockquote>${inlineMD(line.slice(1).trim())}</blockquote>`);
      i++;
      continue;
    }
    // GitHub-flavored pipe table: a line with | immediately followed by a
    // separator row (| --- | :--: | ---: |). A stray | in prose is left alone
    // because it requires that separator line to trigger.
    const next = lines[i + 1];
    if (line.includes("|") && next !== undefined && TABLE_SEP.test(next)) {
      closeList();
      const splitRow = (r: string) =>
        r
          .trim()
          .replace(/^\|/, "")
          .replace(/\|$/, "")
          .split("|")
          .map((c) => c.trim());
      const headers = splitRow(line);
      const aligns = splitRow(next).map((c) => {
        const l = c.startsWith(":");
        const r = c.endsWith(":");
        return l && r ? "center" : r ? "right" : "left";
      });
      i += 2;
      const rows: string[][] = [];
      while (
        i < lines.length &&
        (lines[i] ?? "").trim() &&
        (lines[i] ?? "").includes("|")
      ) {
        rows.push(splitRow(lines[i] ?? ""));
        i++;
      }
      const cell = (c: string | undefined, tag: "th" | "td", k: number) =>
        `<${tag} style="text-align:${aligns[k] || "left"}">${inlineMD(c || "")}</${tag}>`;
      let t = '<div class="md-table"><table><thead><tr>';
      headers.forEach((hh, k) => {
        t += cell(hh, "th", k);
      });
      t += "</tr></thead><tbody>";
      rows.forEach((row) => {
        t += "<tr>";
        for (let k = 0; k < headers.length; k++) t += cell(row[k], "td", k);
        t += "</tr>";
      });
      out.push(t + "</tbody></table></div>");
      continue;
    }
    if (line.trim() === "") {
      closeList();
      i++;
      continue;
    }
    closeList();
    // Fold every following prose line into this paragraph. The first line is
    // known not to start a block (every branch above already declined it), so
    // the buffer is never empty and `i` always advances.
    const para: string[] = [];
    while (i < lines.length && !startsBlock(lines[i] ?? "", lines[i + 1])) {
      para.push(lines[i] ?? "");
      i++;
    }
    out.push(`<p>${inlineMD(para.join("\n"))}</p>`);
  }
  closeList();
  return out.join("\n");
}
