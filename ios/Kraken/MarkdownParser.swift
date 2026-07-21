import Foundation

// MARK: - shared markdown block model
//
// Pure, UI-free markdown parsing shared by the HTML renderer (MarkdownHTML.swift,
// still used for full-screen artifacts) and the native SwiftUI renderer
// (MarkdownText.swift, used for inline session/job bodies — one WKWebView per
// inline block was defeating WebKit's viewport tiling and getting the app
// jetsam-killed). Both renderers walk the same `[MarkdownBlock]` tree; each
// applies its own inline pass (`inlineHTML` or `inlineAttributed`) so the two
// output formats never have to agree on anything richer than plain strings.
//
// `parseMarkdownBlocks` ports the exact line-oriented single-pass semantics
// that used to live directly in MarkdownHTML.swift's `markdownToHTML` — see
// that file's history for the web/app.js origin. Block nodes keep their
// inline content as raw, unescaped markdown; escaping (HTML) or attribute
// styling (native) happens downstream, once, per renderer.

/// One block-level markdown element. Inline text (headings, paragraphs, list
/// items, blockquotes, table cells) is stored as raw markdown — neither
/// HTML-escaped nor otherwise processed — so each renderer can run its own
/// inline pass (`inlineHTML` or `inlineAttributed`) over it.
enum MarkdownBlock: Equatable {
    case heading(level: Int, text: String)
    case paragraph(String)
    case list(ordered: Bool, items: [String])
    case codeFence(String)
    case blockquote(String)
    case table(header: [String], alignments: [String], rows: [[String]])
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

// MARK: - GitHub-flavored pipe tables
//
// Not part of the web UI's renderMD() (web/app.js) — a genuine addition, not
// a port. Every cell's text is left raw here; both renderers escape/style it
// through their own inline pass, same as every other block kind.

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
/// general-purpose escape scanner, matching this parser's small-subset
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

/// Ports app.js's renderMD(): a small block-level pass over headings (#-####),
/// fenced code blocks, unordered/ordered lists, blockquotes, paragraphs, and
/// blank-line breaks. Line-oriented, single forward pass, exactly like the
/// original — this now produces `MarkdownBlock` nodes instead of HTML
/// strings directly, so both renderers share one parse. Tables are the one
/// addition beyond the port (see above).
func parseMarkdownBlocks(_ src: String) -> [MarkdownBlock] {
    var blocks: [MarkdownBlock] = []
    let lines = src.components(separatedBy: "\n")
    var i = 0
    var openListKind: String?
    var openListItems: [String] = []

    func closeList() {
        if let kind = openListKind {
            blocks.append(.list(ordered: kind == "ol", items: openListItems))
            openListKind = nil
            openListItems = []
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
            blocks.append(.codeFence(buf.joined(separator: "\n")))
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
            blocks.append(.table(header: headerCells, alignments: aligns, rows: bodyRows))
            continue
        }

        if let g = regexGroups(#"^(#{1,4})\s+(.*)$"#, in: line) {
            closeList()
            blocks.append(.heading(level: g[0].count, text: g[1]))
            i += 1
            continue
        }

        if let g = regexGroups(#"^\s*[-*]\s+(.*)$"#, in: line) {
            if openListKind != "ul" { closeList(); openListKind = "ul" }
            openListItems.append(g[0])
            i += 1
            continue
        }
        if let g = regexGroups(#"^\s*\d+[.)]\s+(.*)$"#, in: line) {
            if openListKind != "ol" { closeList(); openListKind = "ol" }
            openListItems.append(g[0])
            i += 1
            continue
        }

        if line.hasPrefix(">") {
            closeList()
            let text = String(line.dropFirst()).trimmingCharacters(in: .whitespaces)
            blocks.append(.blockquote(text))
            i += 1
            continue
        }

        if line.trimmingCharacters(in: .whitespaces).isEmpty {
            closeList()
            i += 1
            continue
        }

        closeList()
        blocks.append(.paragraph(line))
        i += 1
    }
    closeList()
    return blocks
}

// MARK: - inline pass: HTML

// Escaping happens FIRST inside inlineHTML, exactly like esc() does in
// app.js's inlineMD() — the inline-pattern regexes below only ever run over
// already-entity-escaped text. Matching that ordering precisely matters: do
// it the other way around (format, then escape) and `<`/`&` inside e.g. a
// backtick span would either double-escape or — worse — not escape at all,
// opening an HTML-injection gap the web UI doesn't have.

func escapeHTML(_ s: String) -> String {
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

/// Ports app.js's inlineMD(): backtick code, **bold**, *italic*, then
/// [text](https://url) links — in that exact order, over already-escaped text.
func inlineHTML(_ s: String) -> String {
    var out = escapeHTML(s)
    out = regexReplaceAll(#"`([^`]+)`"#, in: out, template: "<code>$1</code>")
    out = regexReplaceAll(#"\*\*([^*]+)\*\*"#, in: out, template: "<strong>$1</strong>")
    out = regexReplaceAll(#"\*([^*]+)\*"#, in: out, template: "<em>$1</em>")
    out = regexReplaceAll(#"\[([^\]]+)\]\((https?:[^)\s]+)\)"#, in: out,
                           template: #"<a href="$2" target="_blank" rel="noreferrer">$1</a>"#)
    return out
}

// MARK: - inline pass: AttributedString

/// A single inline styling applied to a run of `inlineAttributed`'s output.
private enum InlineStyle {
    case code
    case bold
    case italic
    case link(URL)
}

private struct InlineSegment {
    var text: String
    var style: InlineStyle?
}

/// Splits `text` on the first pass of `pattern`, styling each match with
/// `styleFor` (given the match's capture groups) and leaving everything else
/// as unstyled plain segments for the next pass to scan. A match whose
/// `styleFor` declines to style it (returns nil — used for the link pass
/// when the captured URL fails to parse) is emitted as plain text instead.
private func splitSegment(
    _ text: String,
    pattern: String,
    styleFor: (_ groups: [String]) -> (String, InlineStyle?)
) -> [InlineSegment] {
    guard let re = try? NSRegularExpression(pattern: pattern) else {
        return [InlineSegment(text: text, style: nil)]
    }
    let nsText = text as NSString
    let range = NSRange(location: 0, length: nsText.length)
    let matches = re.matches(in: text, range: range)
    guard !matches.isEmpty else { return [InlineSegment(text: text, style: nil)] }

    var segments: [InlineSegment] = []
    var lastEnd = 0
    for m in matches {
        if m.range.location > lastEnd {
            let plain = nsText.substring(with: NSRange(location: lastEnd, length: m.range.location - lastEnd))
            segments.append(InlineSegment(text: plain, style: nil))
        }
        var groups: [String] = []
        for g in 1..<m.numberOfRanges {
            let r = m.range(at: g)
            groups.append(r.location == NSNotFound ? "" : nsText.substring(with: r))
        }
        let (content, style) = styleFor(groups)
        segments.append(InlineSegment(text: content, style: style))
        lastEnd = m.range.location + m.range.length
    }
    if lastEnd < nsText.length {
        segments.append(InlineSegment(text: nsText.substring(from: lastEnd), style: nil))
    }
    return segments
}

/// Parses the same four inline constructs as `inlineHTML` — backtick code,
/// **bold**, *italic*, [text](https://url) links, in that exact precedence —
/// but over the raw (unescaped) markdown string, producing styled runs
/// instead of markup. Code spans are matched first and never rescanned by
/// the later passes, so markdown-looking characters inside a backtick span
/// stay literal; a segment styled by a later pass (bold/italic/link) is
/// likewise not rescanned by subsequent passes. This is a simpler, flatter
/// model than the HTML path's plain string rewrite — it doesn't attempt
/// CommonMark-complete nesting (e.g. bold-inside-italic), which the HTML
/// path doesn't reliably produce either.
func inlineAttributed(_ s: String) -> AttributedString {
    var segments = [InlineSegment(text: s, style: nil)]

    func applyPass(_ pattern: String, _ styleFor: @escaping ([String]) -> (String, InlineStyle?)) {
        segments = segments.flatMap { seg -> [InlineSegment] in
            guard seg.style == nil else { return [seg] }
            return splitSegment(seg.text, pattern: pattern, styleFor: styleFor)
        }
    }

    applyPass(#"`([^`]+)`"#) { g in (g[0], .code) }
    applyPass(#"\*\*([^*]+)\*\*"#) { g in (g[0], .bold) }
    applyPass(#"\*([^*]+)\*"#) { g in (g[0], .italic) }
    applyPass(#"\[([^\]]+)\]\((https?:[^)\s]+)\)"#) { g in
        guard let url = URL(string: g[1]) else { return (g[0], nil) }
        return (g[0], .link(url))
    }

    var result = AttributedString()
    for seg in segments {
        var run = AttributedString(seg.text)
        switch seg.style {
        case .code:
            run.inlinePresentationIntent = .code
        case .bold:
            run.inlinePresentationIntent = .stronglyEmphasized
        case .italic:
            run.inlinePresentationIntent = .emphasized
        case .link(let url):
            run.link = url
        case nil:
            break
        }
        result += run
    }
    return result
}
