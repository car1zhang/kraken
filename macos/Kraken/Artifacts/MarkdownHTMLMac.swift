import Foundation

// MarkdownHTMLMac.swift — the artifact viewer's markdown → HTML shell.
//
// A COPY of `ios/Kraken/MarkdownHTML.swift` with one thing changed: where the
// iOS file generates its CSS custom properties from the liquid-glass tint
// (`ThemeTint.resolve` / `KTheme.themeTintKey`, both in `ios/Kraken/Theme.swift`),
// this one takes a resolved `HudTheme` and emits the web board's own `.md`
// palette off it. That single dependency is why the file could not simply be
// REFERENCED into the mac target the way the other eight iOS sources are —
// `macos/project.yml` records the measured failure ("cannot find 'ThemeTint' in
// scope" at MarkdownHTML.swift:117) and points here.
//
// The block-level parse is unchanged and still shared: `parseMarkdownBlocks`,
// `inlineHTML` and `escapeHTML` all live in the REFERENCED
// `ios/Kraken/MarkdownParser.swift`, which is parity-pinned against
// `web/_ui/src/lib/markdown.ts`. Escaping happens FIRST inside `inlineHTML`,
// exactly as the web renderer's `esc()` does, so agent-authored artifact text
// is inert by construction.
//
// The stylesheet below is `web/_ui/src/styles/style.css:1883-1911` — the `.md`
// block, which is the artifact viewer's whole surface on the web board —
// transcribed rule for rule: 11px/1.7 mono body on `--body`, Chakra Petch
// headings with h1 in the accent, amber (`--warn`) inline code on a `--warn` 8%
// wash, `--divider`-ruled zebra tables with display-face headers. Plus
// `.panel-body.raw` (:1879) for the raw toggle.
//
// KNOWN FIDELITY CAVEAT, stated rather than hidden: the two bundled faces are
// registered into THIS process via `ATSApplicationFontsPath`, and WebKit renders
// in its own WebContent process, which does not inherit that registration. So
// `font-family: "Chakra Petch"` here resolves only if the face is installed
// system-wide; otherwise it steps down the same fallback chain style.css itself
// declares (`Avenir Next` / `SF Mono`, `Menlo`) — the identical degradation a
// browser with the webfont blocked would show. Native SwiftUI text elsewhere in
// the app is unaffected, since it resolves in-process through `Font.hudMono`.

// MARK: - blocks → HTML

/// Renders the parsed table. Ragged rows are padded with empty cells (fewer
/// than the header) or truncated (more) against the header's column count,
/// never propagating a raw index-out-of-bounds into a live layout. Wrapped in
/// the same `div.md-table` overflow container the web renderer emits, so a wide
/// table scrolls inside the panel instead of widening it.
private func renderTableMac(headerCells: [String], aligns: [String], bodyRows: [[String]]) -> String {
    let columnCount = headerCells.count
    func alignAttr(_ i: Int) -> String {
        let a = i < aligns.count ? aligns[i] : "left"
        return a == "left" ? "" : " style=\"text-align:\(a)\""
    }
    var html = "<div class=\"md-table\"><table><thead><tr>"
    for (i, cell) in headerCells.enumerated() {
        html += "<th\(alignAttr(i))>\(inlineHTML(cell))</th>"
    }
    html += "</tr></thead><tbody>"
    for row in bodyRows {
        html += "<tr>"
        for i in 0..<columnCount {
            let raw = i < row.count ? row[i] : ""
            html += "<td\(alignAttr(i))>\(inlineHTML(raw))</td>"
        }
        html += "</tr>"
    }
    html += "</tbody></table></div>"
    return html
}

/// `parseMarkdownBlocks`' output rendered to HTML. Top-level emitted strings
/// are joined with "\n"; a list's `<ul>`, each `<li>` and `</ul>` are separate
/// emitted strings, while a table renders as ONE string with no internal
/// newlines.
private func markdownToHTMLMac(_ src: String) -> String {
    var out: [String] = []
    for block in parseMarkdownBlocks(src) {
        switch block {
        case .heading(let level, let text):
            out.append("<h\(level)>\(inlineHTML(text))</h\(level)>")
        case .paragraph(let text):
            out.append("<p>\(inlineHTML(text))</p>")
        case .list(let ordered, let items):
            let tag = ordered ? "ol" : "ul"
            out.append("<\(tag)>")
            for item in items {
                out.append("<li>\(inlineHTML(item))</li>")
            }
            out.append("</\(tag)>")
        case .codeFence(let code):
            out.append("<pre><code>\(escapeHTML(code))</code></pre>")
        case .blockquote(let text):
            out.append("<blockquote>\(inlineHTML(text))</blockquote>")
        case .table(let header, let aligns, let rows):
            out.append(renderTableMac(headerCells: header, aligns: aligns, bodyRows: rows))
        }
    }
    return out.joined(separator: "\n")
}

// MARK: - the shell's palette, emitted from the resolved HUD theme

/// `rgba(r, g, b, a)` — how the two `…, transparent` mixes the `.md` block uses
/// (`--zebra`, and the panel ground's own wash) are spelled in CSS.
private func rgbaCSS(_ c: RGB, _ alpha: Double) -> String {
    String(format: "rgba(%d, %d, %d, %.3f)", c.r, c.g, c.b, alpha)
}

/// The `:root` custom properties for a resolved theme.
///
/// Every value is taken from the SAME `HudTheme` the SwiftUI panel around this
/// web view is drawn from, so the artifact page is the same surface as the
/// board rather than a foreign rectangle pasted onto it — and switching accent
/// or mode repaints both together, because the shell is rebuilt from the new
/// theme and reloaded.
///
/// The three `color-mix` values style.css writes INLINE in the `.md` block
/// (code background, `pre` background, `th` background) are computed here with
/// `HudTheme.mix` rather than emitted as CSS `color-mix()`: one arithmetic
/// implementation for the whole app is the point of the theme engine, and a
/// second one living in a stylesheet string is exactly how the two drift.
private func mdCSSVariables(_ t: HudTheme) -> String {
    """
      :root {
        color-scheme: \(t.mode == .dark ? "dark" : "light");
        --ac: \(t.ac.hex);
        --ac2: \(t.ac2.hex);
        --fg: \(t.fg.hex);
        --body: \(t.body.hex);
        --muted: \(t.muted.hex);
        --divider: \(t.divider.hex);
        --border-strong: \(t.borderStrong.hex);
        --panel-bg: \(HudTheme.mix(t.ac, 5, over: t.b0).hex);
        --code-bg: \(HudTheme.mix(t.warn, 8, over: t.b1).hex);
        --code-fg: \(t.warn.hex);
        --pre-bg: \(HudTheme.mix(t.ac, 2, over: t.b0).hex);
        --th-bg: \(HudTheme.mix(t.ac, 10, over: t.b1).hex);
        --zebra: \(rgbaCSS(t.ac, 0.04));
        --display: "Chakra Petch", "Avenir Next", sans-serif;
        --mono: "IBM Plex Mono", "SF Mono", "Menlo", monospace;
      }
    """
}

/// The document shell. `body` is already-rendered HTML.
///
/// The page paints `--panel-bg` (the `.panel` recipe, style.css:1664) rather
/// than being transparent: a WKWebView with no background of its own shows the
/// window's ground through it, which on this design is a different colour from
/// the panel it sits inside.
private func documentShellMac(body: String, theme: HudTheme) -> String {
    """
    <!doctype html>
    <html>
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
    \(mdCSSVariables(theme))
      * { box-sizing: border-box; -webkit-text-size-adjust: 100%; }
      html, body { background: var(--panel-bg); }
      /* .panel-body's own padding (style.css:1878) — the panel around this web
         view draws no inset of its own, so the document carries it. */
      body {
        margin: 0; padding: 12px 14px;
        min-height: 100%;
        font: 11px/1.7 var(--mono);
        color: var(--body);
      }
      /* ---- style.css :1884-1911, the .md block ---- */
      h1, h2, h3, h4 { margin: 1.1em 0 0.4em; font-family: var(--display); }
      h1 { font: 700 15px var(--display); letter-spacing: 0.08em; color: var(--ac); margin: 0 0 10px; }
      h2 {
        font: 600 12px var(--display); letter-spacing: 0.1em; color: var(--fg);
        border-bottom: 1px solid var(--divider); padding-bottom: 4px; margin: 16px 0 8px;
      }
      h3 { font: 600 11px var(--display); letter-spacing: 0.1em; color: var(--fg); }
      h4 { font: 600 11px var(--display); letter-spacing: 0.1em; color: var(--fg); }
      p, ul, ol { margin: 0.55em 0; }
      ul, ol { padding-left: 20px; }
      li { margin: 0.15em 0; }
      code { font: 10px var(--mono); background: var(--code-bg); color: var(--code-fg); padding: 1px 4px; }
      pre {
        background: var(--pre-bg); border: 1px solid var(--divider);
        padding: 10px 12px; overflow-x: auto; margin: 0.7em 0;
      }
      pre code { background: none; padding: 0; color: var(--body); }
      a { color: var(--ac); }
      a:hover { color: var(--ac2); }
      strong { color: var(--fg); font-weight: 600; }
      blockquote {
        border-left: 2px solid var(--border-strong); padding-left: 10px;
        color: var(--muted); margin: 0.55em 0;
      }
      .md-table { overflow-x: auto; margin: 0.7em 0; }
      table { border-collapse: collapse; width: 100%; font-size: 10px; }
      th, td { border: 1px solid var(--divider); padding: 4px 9px; }
      th {
        background: var(--th-bg); color: var(--fg);
        font: 600 10px var(--display); letter-spacing: 0.08em; text-align: left;
      }
      td { color: var(--body); }
      tbody tr:nth-child(even) td, tr:nth-child(even) td { background: var(--zebra); }
      /* .panel-body.raw (style.css:1879) — the raw toggle's verbatim body. */
      pre.raw {
        background: none; border: none; padding: 0; margin: 0;
        white-space: pre-wrap; word-break: break-word;
        font: 10px/1.7 var(--mono); color: var(--body);
      }
    </style>
    </head>
    <body>
    \(body)
    </body>
    </html>
    """
}

/// Full-page HTML for a markdown source, dressed in the HUD theme — what the
/// artifact viewer hands its web view for every `.text` artifact.
func markdownHTMLMac(_ md: String, theme: HudTheme) -> String {
    documentShellMac(body: markdownToHTMLMac(md), theme: theme)
}

/// The same shell around VERBATIM text — the `raw` toggle. Escaped, never
/// parsed: `raw` means "show me the bytes", so running the markdown transform
/// over them would be answering a different question.
func plainTextHTMLMac(_ text: String, theme: HudTheme) -> String {
    documentShellMac(body: "<pre class=\"raw\">\(escapeHTML(text))</pre>", theme: theme)
}
