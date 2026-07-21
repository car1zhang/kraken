import Foundation

// MARK: - markdown → HTML, ported from web/app.js's esc()/inlineMD()/renderMD()
//
// The ArtifactView-only path (.md artifacts, full-screen) — inline body text
// (e.g. JobDetailView's DetailCard) renders natively via MarkdownText.swift
// instead; one WKWebView per inline block was defeating WebKit's viewport
// tiling and getting the app jetsam-killed.
//
// Block-level parsing lives in MarkdownParser.swift (`parseMarkdownBlocks`,
// shared with the native SwiftUI renderer in MarkdownText.swift); this file
// only renders those blocks to HTML via `inlineHTML`/`escapeHTML` (also in
// MarkdownParser.swift). Escaping happens FIRST inside inlineHTML, exactly
// like esc() does in app.js's inlineMD() — the inline-pattern regexes only
// ever run over already-entity-escaped text. Matching that ordering
// precisely matters: do it the other way around (format, then escape) and
// `<`/`&` inside e.g. a backtick span would either double-escape or —
// worse — not escape at all, opening an HTML-injection gap the web UI
// doesn't have.

/// Renders the parsed table. Ragged rows are padded with empty cells (fewer
/// than the header) or truncated (more) against the header's column count,
/// never propagating a raw index-out-of-bounds into a live layout. Wrapped
/// in an overflow-x:auto div so a wide table scrolls instead of squeezing
/// the phone layout.
private func renderTable(headerCells: [String], aligns: [String], bodyRows: [[String]]) -> String {
    let columnCount = headerCells.count
    func alignAttr(_ i: Int) -> String {
        let a = i < aligns.count ? aligns[i] : "left"
        return a == "left" ? "" : " style=\"text-align:\(a)\""
    }
    var html = "<div style=\"overflow-x:auto\"><table><thead><tr>"
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

/// Renders `parseMarkdownBlocks`' output (MarkdownParser.swift) to HTML.
/// Byte-for-byte equivalent to the old inline block-level pass this replaced:
/// top-level emitted strings are joined with "\n", but a list's `<ul>`,
/// each `<li>`, and `</ul>` are separate emitted strings (so they end up
/// newline-separated), while a table renders as ONE string with no internal
/// newlines — match `renderTable`'s single-string return, don't split it.
private func markdownToHTML(_ src: String) -> String {
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
            out.append(renderTable(headerCells: header, aligns: aligns, bodyRows: rows))
        }
    }
    return out.joined(separator: "\n")
}

/// Common HTML shell matching web/style.css's .md rules (accent h1,
/// hairline-underlined h2, raised-bg inline code, bordered pre blocks,
/// accent links) plus a viewport meta tag and a touch-friendly base font
/// size. Always fills its container — full-screen ArtifactView is the only
/// caller now, so there's no transparent/inline variant to switch between.
private func documentShell(body: String) -> String {
    return """
    <!doctype html>
    <html>
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
      :root {
        color-scheme: light dark;
        --ground: #0A0E14;
        --raised: #151C28;
        --hairline: #1E2836;
        --hairline2: #2A3A50;
        --text: #D7E2EC;
        --text2: #6B7A8C;
        --accent: #9E7BFF;
        --revealed: #F5B84D;
        --zebra: rgba(255, 255, 255, 0.02);
      }
      @media (prefers-color-scheme: light) {
        :root {
          --ground: #EEF2F7;
          --raised: #E3E9F1;
          --hairline: #D8E0EA;
          --hairline2: #B9C6D6;
          --text: #1C2733;
          --text2: #5D6C7E;
          --accent: #6B3FE0;
          --revealed: #8A5106;
          --zebra: rgba(0, 0, 0, 0.025);
        }
      }
      * { box-sizing: border-box; -webkit-text-size-adjust: 100%; }
      html, body { background: var(--ground); }
      body {
        margin: 0; padding: 16px;
        color: var(--text);
        font: 15px/1.55 -apple-system, "Avenir Next", "Helvetica Neue", sans-serif;
      }
      h1, h2, h3, h4 { margin: 1.1em 0 0.4em; font-weight: 600; }
      h1:first-child, h2:first-child, h3:first-child, h4:first-child, p:first-child { margin-top: 0; }
      h1 { font-size: 20px; color: var(--accent); }
      h2 { font-size: 17px; border-bottom: 1px solid var(--hairline); padding-bottom: 3px; }
      h3 { font-size: 16px; }
      h4 { font-size: 15px; }
      p, ul, ol { margin: 0.55em 0; }
      ul, ol { padding-left: 20px; }
      li { margin: 0.15em 0; }
      code { font: 13px "SF Mono", Menlo, monospace; background: var(--raised); color: var(--revealed); padding: 1px 4px; }
      pre {
        background: var(--ground); border: 1px solid var(--hairline); padding: 10px 12px;
        overflow-x: auto; margin: 0.7em 0; white-space: pre-wrap; word-break: break-word;
      }
      pre code { background: none; padding: 0; color: var(--text); }
      a { color: var(--accent); }
      strong { color: var(--text); font-weight: 600; }
      blockquote { border-left: 2px solid var(--hairline2); padding-left: 10px; color: var(--text2); margin: 0.55em 0; }
      table { border-collapse: collapse; width: 100%; margin: 0.7em 0; font-size: 14px; }
      th, td { border: 1px solid var(--hairline); padding: 6px 10px; }
      th { background: var(--raised); color: var(--text); font-weight: 600; text-align: left; }
      td { color: var(--text); }
      tr:nth-child(even) td { background: var(--zebra); }
    </style>
    </head>
    <body>
    \(body)
    </body>
    </html>
    """
}

/// Full-page dark-shell HTML for a markdown source — used by ArtifactView
/// for .md artifacts (and anywhere else a markdown document should fill the
/// screen the same way it does in the browser UI).
func markdownHTML(_ md: String) -> String {
    documentShell(body: markdownToHTML(md))
}

/// Preformatted (non-markdown) text in the same dark shell — .log/.txt
/// artifacts.
func plainTextHTML(_ text: String) -> String {
    documentShell(body: "<pre>\(escapeHTML(text))</pre>")
}
