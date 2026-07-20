import SwiftUI
import WebKit
import UIKit

// MARK: - markdown → HTML, ported from web/app.js's esc()/inlineMD()/renderMD()
//
// Shared by ArtifactView (.md artifacts, full-screen) and any inline body
// text (JobDetailView's DetailCard) — one renderer, one dark shell, so a
// markdown body reads the same wherever it's opened.
//
// Escaping happens FIRST inside inlineMarkdown, exactly like esc() does in
// app.js's inlineMD() — the inline-pattern regexes below only ever run over
// already-entity-escaped text. Matching that ordering precisely matters: do
// it the other way around (format, then escape) and `<`/`&` inside e.g. a
// backtick span would either double-escape or — worse — not escape at all,
// opening an HTML-injection gap the web UI doesn't have.

private func escapeHTML(_ s: String) -> String {
    var out = ""
    out.reserveCapacity(s.count)
    for c in s {
        switch c {
        case "&": out += "&amp;"
        case "<": out += "&lt;"
        case ">": out += "&gt;"
        case "\"": out += "&quot;"
        case "'": out += "&#39;"
        default: out.append(c)
        }
    }
    return out
}

private func regexReplaceAll(_ pattern: String, in text: String, template: String) -> String {
    guard let re = try? NSRegularExpression(pattern: pattern) else { return text }
    let range = NSRange(text.startIndex..., in: text)
    return re.stringByReplacingMatches(in: text, range: range, withTemplate: template)
}

/// Matches `pattern` against a single line and returns its capture groups
/// (empty string for any group that didn't participate), or nil if no match.
private func regexGroups(_ pattern: String, in line: String) -> [String]? {
    guard let re = try? NSRegularExpression(pattern: pattern) else { return nil }
    let range = NSRange(line.startIndex..., in: line)
    guard let m = re.firstMatch(in: line, range: range) else { return nil }
    var groups: [String] = []
    for i in 1..<m.numberOfRanges {
        if let r = Range(m.range(at: i), in: line) {
            groups.append(String(line[r]))
        } else {
            groups.append("")
        }
    }
    return groups
}

/// Ports app.js's inlineMD(): backtick code, **bold**, *italic*, then
/// [text](https://url) links — in that exact order, over already-escaped text.
private func inlineMarkdown(_ s: String) -> String {
    var out = escapeHTML(s)
    out = regexReplaceAll(#"`([^`]+)`"#, in: out, template: "<code>$1</code>")
    out = regexReplaceAll(#"\*\*([^*]+)\*\*"#, in: out, template: "<strong>$1</strong>")
    out = regexReplaceAll(#"\*([^*]+)\*"#, in: out, template: "<em>$1</em>")
    out = regexReplaceAll(#"\[([^\]]+)\]\((https?:[^)\s]+)\)"#, in: out,
                           template: #"<a href="$2" target="_blank" rel="noreferrer">$1</a>"#)
    return out
}

// MARK: - GitHub-flavored pipe tables
//
// The web UI's renderMD() (web/app.js) doesn't support tables at all — this
// is a genuine addition, not a port. Kept in the same escape-then-format
// discipline as the rest of this file: every cell goes through
// inlineMarkdown (which escapes first internally), so **bold**/`code`/links
// work inside cells and raw HTML in a cell can't leak through.

/// A GFM separator line ("| --- | :--: | ---: |", pipes optional at the
/// ends): every cell is only dashes/colons/whitespace, at least one dash.
private func isTableSeparatorLine(_ line: String) -> Bool {
    let trimmed = line.trimmingCharacters(in: .whitespaces)
    guard !trimmed.isEmpty, trimmed.contains("-") else { return false }
    let pattern = #"^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$"#
    guard let re = try? NSRegularExpression(pattern: pattern) else { return false }
    let range = NSRange(trimmed.startIndex..., in: trimmed)
    return re.firstMatch(in: trimmed, range: range) != nil
}

/// Splits a row on unescaped `|` (a `\|` is a literal pipe inside a cell,
/// per GFM), trims the outer pipes first. Only a backslash immediately
/// before a pipe is treated as an escape (a lone `\` elsewhere is kept
/// as-is) — covers the common "literal pipe in a cell" case without a full
/// general-purpose escape scanner, matching this renderer's small-subset
/// scope everywhere else.
private func splitTableRow(_ line: String) -> [String] {
    var trimmed = line.trimmingCharacters(in: .whitespaces)
    if trimmed.hasPrefix("|") { trimmed.removeFirst() }
    if trimmed.hasSuffix("|") { trimmed.removeLast() }

    let chars = Array(trimmed)
    var cells: [String] = []
    var current = ""
    var idx = 0
    while idx < chars.count {
        let c = chars[idx]
        if c == "\\", idx + 1 < chars.count, chars[idx + 1] == "|" {
            current.append("|")
            idx += 2
            continue
        }
        if c == "|" {
            cells.append(current)
            current = ""
            idx += 1
            continue
        }
        current.append(c)
        idx += 1
    }
    cells.append(current)
    return cells.map { $0.trimmingCharacters(in: .whitespaces) }
}

/// "---"/":---"/":--:"/"---:" → left/left/center/right, per column.
private func parseTableAlignments(_ separatorLine: String) -> [String] {
    splitTableRow(separatorLine).map { raw in
        let c = raw.trimmingCharacters(in: .whitespaces)
        switch (c.hasPrefix(":"), c.hasSuffix(":")) {
        case (true, true): return "center"
        case (false, true): return "right"
        default: return "left"
        }
    }
}

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
        html += "<th\(alignAttr(i))>\(inlineMarkdown(cell))</th>"
    }
    html += "</tr></thead><tbody>"
    for row in bodyRows {
        html += "<tr>"
        for i in 0..<columnCount {
            let raw = i < row.count ? row[i] : ""
            html += "<td\(alignAttr(i))>\(inlineMarkdown(raw))</td>"
        }
        html += "</tr>"
    }
    html += "</tbody></table></div>"
    return html
}

/// Ports app.js's renderMD(): a small block-level pass over headings (#-####),
/// fenced code blocks, unordered/ordered lists, blockquotes, paragraphs, and
/// blank-line breaks. Line-oriented, single forward pass, exactly like the
/// original. Tables are the one addition beyond the port (see above).
private func markdownToHTML(_ src: String) -> String {
    var out: [String] = []
    let lines = src.components(separatedBy: "\n")
    var i = 0
    var openList: String?

    func closeList() {
        if let l = openList {
            out.append("</\(l)>")
            openList = nil
        }
    }

    while i < lines.count {
        let line = lines[i]

        if line.hasPrefix("```") {
            closeList()
            var buf: [String] = []
            i += 1
            while i < lines.count && !lines[i].hasPrefix("```") {
                buf.append(lines[i])
                i += 1
            }
            i += 1 // skip the closing fence (or run off the end if unterminated)
            out.append("<pre><code>\(escapeHTML(buf.joined(separator: "\n")))</code></pre>")
            continue
        }

        // A pipe line immediately followed by a valid separator line is a
        // table; anything else (including a lone pipe line with no valid
        // separator after it) falls through to the normal paragraph case
        // below, so a stray "|" in prose is never misparsed.
        if line.contains("|"), i + 1 < lines.count, isTableSeparatorLine(lines[i + 1]) {
            closeList()
            let headerCells = splitTableRow(line)
            let aligns = parseTableAlignments(lines[i + 1])
            i += 2
            var bodyRows: [[String]] = []
            while i < lines.count {
                let rowLine = lines[i]
                if rowLine.trimmingCharacters(in: .whitespaces).isEmpty { break }
                if !rowLine.contains("|") { break }
                bodyRows.append(splitTableRow(rowLine))
                i += 1
            }
            out.append(renderTable(headerCells: headerCells, aligns: aligns, bodyRows: bodyRows))
            continue
        }

        if let g = regexGroups(#"^(#{1,4})\s+(.*)$"#, in: line) {
            closeList()
            let level = g[0].count
            out.append("<h\(level)>\(inlineMarkdown(g[1]))</h\(level)>")
            i += 1
            continue
        }

        if let g = regexGroups(#"^\s*[-*]\s+(.*)$"#, in: line) {
            if openList != "ul" { closeList(); out.append("<ul>"); openList = "ul" }
            out.append("<li>\(inlineMarkdown(g[0]))</li>")
            i += 1
            continue
        }
        if let g = regexGroups(#"^\s*\d+[.)]\s+(.*)$"#, in: line) {
            if openList != "ol" { closeList(); out.append("<ol>"); openList = "ol" }
            out.append("<li>\(inlineMarkdown(g[0]))</li>")
            i += 1
            continue
        }

        if line.hasPrefix(">") {
            closeList()
            let text = String(line.dropFirst()).trimmingCharacters(in: .whitespaces)
            out.append("<blockquote>\(inlineMarkdown(text))</blockquote>")
            i += 1
            continue
        }

        if line.trimmingCharacters(in: .whitespaces).isEmpty {
            closeList()
            i += 1
            continue
        }

        closeList()
        out.append("<p>\(inlineMarkdown(line))</p>")
        i += 1
    }
    closeList()
    return out.joined(separator: "\n")
}

/// Common HTML shell matching web/style.css's .md rules (accent h1,
/// hairline-underlined h2, raised-bg inline code, bordered pre blocks,
/// accent links) plus a viewport meta tag and a touch-friendly base font
/// size. `transparent` drops the ground background/padding for content
/// meant to sit inline inside a SwiftUI card instead of filling a screen.
private func documentShell(body: String, transparent: Bool) -> String {
    let background = transparent ? "transparent" : "#0A0E14"
    let padding = transparent ? "0" : "16px"
    return """
    <!doctype html>
    <html>
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
      :root { color-scheme: dark; }
      * { box-sizing: border-box; -webkit-text-size-adjust: 100%; }
      html, body { background: \(background); }
      body {
        margin: 0; padding: \(padding);
        color: #D7E2EC;
        font: 15px/1.55 -apple-system, "Avenir Next", "Helvetica Neue", sans-serif;
      }
      h1, h2, h3, h4 { margin: 1.1em 0 0.4em; font-weight: 600; }
      h1:first-child, h2:first-child, h3:first-child, h4:first-child, p:first-child { margin-top: 0; }
      h1 { font-size: 20px; color: #9E7BFF; }
      h2 { font-size: 17px; border-bottom: 1px solid #1E2836; padding-bottom: 3px; }
      h3 { font-size: 16px; }
      h4 { font-size: 15px; }
      p, ul, ol { margin: 0.55em 0; }
      ul, ol { padding-left: 20px; }
      li { margin: 0.15em 0; }
      code { font: 13px "SF Mono", Menlo, monospace; background: #151C28; color: #F5B84D; padding: 1px 4px; }
      pre {
        background: #0A0E14; border: 1px solid #1E2836; padding: 10px 12px;
        overflow-x: auto; margin: 0.7em 0; white-space: pre-wrap; word-break: break-word;
      }
      pre code { background: none; padding: 0; color: #D7E2EC; }
      a { color: #9E7BFF; }
      strong { color: #D7E2EC; font-weight: 600; }
      blockquote { border-left: 2px solid #2A3A50; padding-left: 10px; color: #6B7A8C; margin: 0.55em 0; }
      table { border-collapse: collapse; width: 100%; margin: 0.7em 0; font-size: 14px; }
      th, td { border: 1px solid #1E2836; padding: 6px 10px; }
      th { background: #151C28; color: #D7E2EC; font-weight: 600; text-align: left; }
      td { color: #D7E2EC; }
      tr:nth-child(even) td { background: rgba(255, 255, 255, 0.02); }
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
    documentShell(body: markdownToHTML(md), transparent: false)
}

/// Same markdown pipeline, transparent/tightly-padded shell meant to sit
/// inline inside a SwiftUI card (escalation bodies) rather than fill a screen.
func markdownFragmentHTML(_ md: String) -> String {
    documentShell(body: markdownToHTML(md), transparent: true)
}

/// Preformatted (non-markdown) text in the same dark shell — .log/.txt
/// artifacts.
func plainTextHTML(_ text: String) -> String {
    documentShell(body: "<pre>\(escapeHTML(text))</pre>", transparent: false)
}

// MARK: - self-sizing WKWebView for markdown fragments

/// Renders a markdown fragment and reports its rendered height back via
/// `height`, so a SwiftUI parent can size a non-scrolling WKWebView to fit
/// its content instead of guessing a fixed height.
struct MarkdownWebView: UIViewRepresentable {
    let markdown: String
    @Binding var height: CGFloat
    var scrollEnabled: Bool = false

    func makeCoordinator() -> Coordinator { Coordinator(height: $height) }

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView()
        webView.isOpaque = false
        webView.backgroundColor = .clear
        webView.scrollView.backgroundColor = .clear
        webView.scrollView.isScrollEnabled = scrollEnabled
        webView.navigationDelegate = context.coordinator
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        webView.scrollView.isScrollEnabled = scrollEnabled
        guard context.coordinator.lastMarkdown != markdown else { return }
        context.coordinator.lastMarkdown = markdown
        webView.loadHTMLString(markdownFragmentHTML(markdown), baseURL: nil)
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var lastMarkdown: String?
        @Binding var height: CGFloat

        init(height: Binding<CGFloat>) {
            _height = height
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            webView.evaluateJavaScript("document.body.scrollHeight") { [weak self] result, _ in
                guard let self, let n = result as? NSNumber else { return }
                let measured = CGFloat(n.doubleValue)
                DispatchQueue.main.async {
                    self.height = max(measured, 20)
                }
            }
        }
    }
}

/// Convenience wrapper owning its own height state: drop `MarkdownBody(markdown:)`
/// into any layout and it sizes itself to its content. `maxHeight`, when set,
/// clamps the frame and enables internal scrolling — used for compact
/// previews as opposed to a fully expanded card (JobDetailView's DetailCard).
struct MarkdownBody: View {
    let markdown: String
    var maxHeight: CGFloat?

    @State private var measuredHeight: CGFloat = 24

    var body: some View {
        MarkdownWebView(markdown: markdown, height: $measuredHeight, scrollEnabled: maxHeight != nil)
            .frame(height: maxHeight.map { min(measuredHeight, $0) } ?? measuredHeight)
    }
}
