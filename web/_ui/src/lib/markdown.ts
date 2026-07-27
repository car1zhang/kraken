// The board's markdown renderer — a VERBATIM port of web/static/app.js's
// esc()/inlineMD()/renderMD() (app.js:90, :1195, :1203).
//
// Verbatim is a decision, not laziness (artifacts/53-port-checklist.md, Q7).
// Two reasons:
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
//     emitter is a different screenshot.
//
// The subset: fenced code, `#`–`####` headings, ul/ol, blockquote, GFM pipe
// tables with alignment, inline code/bold/em, and `https?:` links only.
// Everything else falls through to a `<p>`.

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
 * Inline transforms, applied to ALREADY-ESCAPED text (app.js:1195).
 *
 * The link rule only accepts an `https?:` target: job files and specs are
 * hand-editable and agent-written, and esc() stops an attribute breakout but
 * not a `javascript:` scheme.
 */
export function inlineMD(s: string): string {
  return esc(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/\*([^*]+)\*/g, "<em>$1</em>")
    .replace(
      /\[([^\]]+)\]\((https?:[^)\s]+)\)/g,
      '<a href="$2" target="_blank" rel="noreferrer">$1</a>',
    );
}

/**
 * Block-level render (app.js:1203). Returns an HTML string, rendered by the
 * callers through `dangerouslySetInnerHTML` — see the header comment for why
 * that is the faithful port rather than a smell.
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
    if (
      line.includes("|") &&
      next !== undefined &&
      /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/.test(next)
    ) {
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
    out.push(`<p>${inlineMD(line)}</p>`);
    i++;
  }
  closeList();
  return out.join("\n");
}
