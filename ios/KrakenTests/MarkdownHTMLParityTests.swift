import XCTest
@testable import Kraken

// This file makes TWO claims, and they are deliberately separate.
//
// 1. BODY PARITY (unchanged coverage, re-scoped). Byte-parity between the
//    current parser+renderer split (MarkdownParser.swift +
//    MarkdownHTML.swift) and the pre-refactor self-contained implementation
//    it replaced (see `git show HEAD~1:ios/Kraken/MarkdownHTML.swift`,
//    commit a5545b6's parent). That refactor was supposed to be a pure
//    restructuring — same markdown in, same HTML out — and the whole corpus
//    below, injection-safety cases included, is still compared BYTE for BYTE.
//
//    What changed is the SCOPE of the comparison: only the rendered markdown
//    body is compared now, not the whole document. The Liquid Glass redesign
//    made the document SHELL tint-derived (its CSS custom properties are
//    generated from the stored `ThemeTint` so an artifact page sits on the
//    same field as the app instead of reading as a near-black seam), which
//    deliberately and permanently obsoletes shell parity with a legacy copy
//    frozen at the cyber-dark palette. The parser/renderer parity the file
//    was written to protect is untouched by that and is what survives here.
//
//    The legacy implementation is embedded below verbatim, renamed with a
//    `legacy` prefix. Pure Foundation code, copied unchanged except for
//    names, so it keeps behaving exactly as it did in production before the
//    refactor regardless of any future edits to the current files.
//
//    NOTE ON ACCESS LEVEL: the current `markdownToHTML(_:)` is `private`,
//    i.e. file-private for a top-level declaration — @testable import does
//    not reach it. Parity therefore goes through the exported
//    `markdownHTML(_:)` wrapper and slices the body back out of the document
//    (`htmlBody`), which is exactly as strong as comparing the private
//    functions directly would have been.
//
// 2. SHELL OBLIGATIONS (new). The shell is no longer a constant, so it gets
//    assertions of its own instead of a frozen expected string: it carries
//    the STORED tint's hue, an unrecognized or absent stored tint degrades to
//    violet, inline code's `--revealed` stays the two literal KTheme.revealed
//    hexes (state hues are never tint-derived), and rendering is
//    deterministic for a fixed tint.
//
// Every test that touches the tint key SETS it explicitly and tearDown
// REMOVES it — no test may depend on, or leave behind, UserDefaults state.
final class MarkdownHTMLParityTests: XCTestCase {

    override func tearDown() {
        UserDefaults.standard.removeObject(forKey: KTheme.themeTintKey)
        super.tearDown()
    }

    // MARK: - helpers

    /// The rendered markdown body, sliced back out of a full document.
    ///
    /// Both shells (current and legacy) close `</head>` and open `<body>` on
    /// their own lines and end with `\n</body>\n</html>`, so these two anchors
    /// bracket the body exactly. They can't collide with rendered content: a
    /// literal `<body>` in the markdown source is entity-escaped to
    /// `&lt;body&gt;` before it ever reaches the document.
    private func htmlBody(_ document: String, file: StaticString = #filePath, line: UInt = #line) -> String {
        guard let open = document.range(of: "</head>\n<body>\n") else {
            XCTFail("no <body> opener in document", file: file, line: line)
            return ""
        }
        guard let close = document.range(of: "\n</body>\n</html>", options: .backwards) else {
            XCTFail("no </body> closer in document", file: file, line: line)
            return ""
        }
        guard open.upperBound <= close.lowerBound else {
            XCTFail("body markers out of order", file: file, line: line)
            return ""
        }
        return String(document[open.upperBound..<close.lowerBound])
    }

    /// Renders with a specific tint stored, so no test depends on whatever
    /// tint another test (or the app) happened to leave behind.
    private func rendered(_ markdown: String, tint: ThemeTint) -> String {
        UserDefaults.standard.set(tint.rawValue, forKey: KTheme.themeTintKey)
        return markdownHTML(markdown)
    }

    // MARK: - corpus

    private static let corpus: [String] = [
        // empty / degenerate
        "",
        "\n\n\n",
        "```",
        "```\n```",
        // headings
        "# h1\n## h2\n### h3\n#### h4\n##### not a heading",
        // paragraphs
        "hello world",
        "line one\nline two\nline three",
        // lists
        "- a\n- b\n- c",
        "1. a\n2. b\n3. c",
        "- a\n- b\n1. c\n2. d",
        "para\n- item1\n- item2",
        // fenced code
        "```\nfn main() {}\n```",
        "```\nno closing fence here\nsecond line",
        // blockquotes
        "> quote one\n> quote two",
        ">q1\npara\n>q2",
        // tables
        "| H1 | H2 | H3 |\n|:--|:-:|--:|\n| a | b | c |",
        "para\n\n| A | B |\n| --- | --- |\n| 1 | 2 |",
        "| a\\|b | c |\n| --- | --- |\n| x | y |",
        "| A | B | C |\n| --- | --- | --- |\n| 1 |\n| 2 | 3 | 4 | 5 |",
        "just | one pipe\nnot a table",
        // injection safety
        "<script>alert(1)</script>",
        "| <script>alert(1)</script> | b |\n| --- | --- |\n| 1 | 2 |",
        "```\n<script>alert(1)</script>\n```",
        // CRLF + multibyte
        "line one\r\nline two\r\n# heading\r\n",
        "héllo 🚀 world — \u{201c}quotes\u{201d} café",
        // mixed document: headings + lists + tables + fences interleaved
        """
        # Deploy notes

        Some *important* context before the list.

        - checked `k ls` output
        - confirmed the [PR](https://example.com/pr/1) is green
        1. reran tests
        2. merged

        | Step | Status |
        | --- | :-: |
        | build | ok |
        | test | ok |

        ```
        make install
        ```

        > carl: ship it
        """,
        // realistic long zeus-style transcript message, every construct at once
        """
        ## Summary

        Fixed the **memory** leak in `ArtifactView` by killing per-block WKWebViews.
        See [the commit](https://github.com/example/kraken/commit/a5545b6) for details.

        ### Changes

        - ported block parsing into `MarkdownParser.swift`
        - `MarkdownHTML.swift` now only renders blocks to HTML
        - added a *native* SwiftUI renderer for inline bodies

        1. run `go test ./...`
        2. run `xcodebuild test`
        3. open a PR

        | File | Delta |
        | --- | ---: |
        | MarkdownParser.swift | +200 |
        | MarkdownHTML.swift | -150 |

        ```swift
        func parseMarkdownBlocks(_ src: String) -> [MarkdownBlock] { ... }
        ```

        > Byte-parity with the pre-refactor implementation is a hard requirement.

        Final paragraph with a trailing thought and an unclosed **bold marker
        """,
    ]

    // MARK: - body parity with the pre-refactor renderer

    /// The whole corpus, byte for byte — headings, lists, fences, tables,
    /// ragged rows, CRLF, multibyte, and every injection-safety case.
    func testMarkdownBodyByteParityAcrossCorpus() {
        for (i, input) in Self.corpus.enumerated() {
            let actual = htmlBody(rendered(input, tint: .violet))
            let expected = htmlBody(legacyMarkdownHTML(input))
            XCTAssertEqual(actual, expected, "corpus[\(i)] mismatch for input:\n\(input)")
        }
    }

    /// The body is a pure function of the markdown: the tint changes the
    /// shell and nothing else. Rendering the corpus under a different tint
    /// must produce the identical body.
    func testMarkdownBodyIsIndependentOfTint() {
        for (i, input) in Self.corpus.enumerated() {
            let violet = htmlBody(rendered(input, tint: .violet))
            let amber = htmlBody(rendered(input, tint: .amber))
            XCTAssertEqual(violet, amber, "corpus[\(i)] body moved with the tint")
        }
    }

    func testMarkdownBodyParityOnMixedDocument() {
        let doc = """
        # Title

        Some **bold** and *italic* and `code` and a [link](https://example.com).

        - one
        - two

        | A | B |
        | --- | --- |
        | 1 | 2 |

        ```
        fenced
        ```

        > quoted
        """
        UserDefaults.standard.set(ThemeTint.violet.rawValue, forKey: KTheme.themeTintKey)
        XCTAssertEqual(htmlBody(markdownHTML(doc)), htmlBody(legacyMarkdownHTML(doc)))
    }

    func testPlainTextHTMLBodyParity() {
        let text = "raw <text> & \"quotes\" 'apostrophes' > done"
        UserDefaults.standard.set(ThemeTint.violet.rawValue, forKey: KTheme.themeTintKey)
        XCTAssertEqual(htmlBody(plainTextHTML(text)), htmlBody(legacyPlainTextHTML(text)))
    }

    // MARK: - shell obligations

    /// Every tint's shell carries that tint's own hue in its accent, in both
    /// the dark `:root` block and the light override — so a link in an
    /// artifact is the same colour as a link in the app.
    func testShellAccentCarriesStoredTintHue() {
        for tint in ThemeTint.allCases {
            let document = rendered("# hello", tint: tint)
            let needle = "--accent: hsl(\(Int(tint.hueDegrees)),"
            let occurrences = document.components(separatedBy: needle).count - 1
            XCTAssertEqual(
                occurrences, 2,
                "\(tint.rawValue): expected the accent at hue \(Int(tint.hueDegrees)) in both the dark and light blocks"
            )
        }
    }

    /// The LIGHT `--accent`'s lightness channel follows `ThemeTint.
    /// lightBrightness` (contract amendment 2026-08-07 r2/r3) — the hue test
    /// above cannot see this channel, so a shell pinned at one brightness for
    /// every tint would pass it while failing the amendment's contrast bar.
    /// Expected lightness replicates the shell's own HSV→HSL identity
    /// (L = V·(1−S/2)) at the light saturation 0.72 × saturationScale.
    func testShellLightAccentLightnessFollowsLightBrightness() {
        for tint in ThemeTint.allCases {
            let document = rendered("# hello", tint: tint)
            guard let lightBlock = document.components(separatedBy: "@media (prefers-color-scheme: light)").last,
                  let accentValue = lightBlock.components(separatedBy: "--accent: hsl(").dropFirst().first?
                      .components(separatedBy: ")").first,
                  let lightness = accentValue.components(separatedBy: ",").dropFirst(2).first?
                      .trimmingCharacters(in: CharacterSet(charactersIn: " %")),
                  let got = Double(lightness)
            else {
                XCTFail("\(tint.rawValue): could not extract the light --accent hsl() lightness")
                continue
            }
            let hsvSaturation = 0.72 * tint.saturationScale
            let want = tint.lightBrightness * (1 - hsvSaturation / 2) * 100
            XCTAssertEqual(got, want, accuracy: 1.0,
                           "\(tint.rawValue): light --accent lightness \(got)%, want \(want)%")
        }
    }

    /// A `:root` block exists at all, and it is the generated one (i.e. the
    /// old frozen cyber-dark hexes are gone from the tint-derived channels).
    func testShellEmitsGeneratedRootBlockAndDropsLegacyHexes() {
        let document = rendered("# hello", tint: .violet)
        XCTAssertTrue(document.contains(":root {"), "no :root block")
        XCTAssertTrue(document.contains("--ground: hsl("), "--ground is not tint-derived")
        XCTAssertTrue(document.contains("--text: hsl("), "--text is not tint-derived")
        for legacyHex in ["#0A0E14", "#9E7BFF", "#D7E2EC", "#6B3FE0", "#EEF2F7", "#1C2733"] {
            XCTAssertFalse(
                document.contains(legacyHex),
                "frozen cyber-dark hex \(legacyHex) survived into the tint-derived shell"
            )
        }
    }

    /// Different tints must actually produce different shells — otherwise
    /// every assertion above would pass on a shell that ignored the tint.
    func testDifferentTintsProduceDifferentShells() {
        let violet = rendered("# hello", tint: .violet)
        let green = rendered("# hello", tint: .green)
        XCTAssertNotEqual(violet, green)
        // ...while the graphite escape hatch differs from a chromatic tint by
        // saturation, not just hue.
        XCTAssertNotEqual(rendered("# hello", tint: .graphite), rendered("# hello", tint: .blue))
    }

    /// `ThemeTint.resolve`'s contract, seen from this page: an unrecognized
    /// stored value and an absent key both render exactly as violet does,
    /// rather than producing a broken or empty palette.
    func testUnrecognizedOrAbsentStoredTintDegradesToViolet() {
        let violet = rendered("# hello", tint: .violet)

        UserDefaults.standard.set("chartreuse", forKey: KTheme.themeTintKey)
        XCTAssertEqual(markdownHTML("# hello"), violet, "unrecognized tint did not degrade to violet")

        UserDefaults.standard.set("", forKey: KTheme.themeTintKey)
        XCTAssertEqual(markdownHTML("# hello"), violet, "empty tint did not degrade to violet")

        UserDefaults.standard.removeObject(forKey: KTheme.themeTintKey)
        XCTAssertEqual(markdownHTML("# hello"), violet, "absent tint did not degrade to violet")
    }

    /// The stated exception: state hues are never tint-derived, so inline
    /// code keeps KTheme.revealed's two literal hexes under EVERY tint.
    func testInlineCodeRevealedStaysLiteralUnderEveryTint() {
        for tint in ThemeTint.allCases {
            let document = rendered("`code`", tint: tint)
            XCTAssertTrue(
                document.contains("--revealed: #F5B84D;"),
                "\(tint.rawValue): dark --revealed drifted off KTheme.revealed"
            )
            XCTAssertTrue(
                document.contains("--revealed: #8A5106;"),
                "\(tint.rawValue): light --revealed drifted off KTheme.revealed"
            )
        }
    }

    /// The shell reads UserDefaults at render time, so it must still be a
    /// pure function of (markdown, stored tint) — two consecutive calls with
    /// the key untouched are byte-identical.
    func testRenderingIsDeterministicForAFixedTint() {
        let doc = """
        # Title

        Some **bold** and `code`.

        | A | B |
        | --- | --- |
        | 1 | 2 |
        """
        UserDefaults.standard.set(ThemeTint.cyan.rawValue, forKey: KTheme.themeTintKey)
        XCTAssertEqual(markdownHTML(doc), markdownHTML(doc))
        XCTAssertEqual(plainTextHTML(doc), plainTextHTML(doc))
    }
}

// MARK: - legacy implementation (pre-refactor, verbatim modulo naming)
//
// Copied from `git show HEAD~1:ios/Kraken/MarkdownHTML.swift` — the version
// of MarkdownHTML.swift immediately before the parser/renderer split. Every
// symbol below is prefixed `legacy` and otherwise unchanged: same regexes,
// same control flow, same string templates.

private func legacyEscapeHTML(_ s: String) -> String {
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

private func legacyRegexReplaceAll(_ pattern: String, in text: String, template: String) -> String {
    guard let re = try? NSRegularExpression(pattern: pattern) else { return text }
    let range = NSRange(text.startIndex..., in: text)
    return re.stringByReplacingMatches(in: text, range: range, withTemplate: template)
}

private func legacyRegexGroups(_ pattern: String, in line: String) -> [String]? {
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

private func legacyInlineMarkdown(_ s: String) -> String {
    var out = legacyEscapeHTML(s)
    out = legacyRegexReplaceAll(#"`([^`]+)`"#, in: out, template: "<code>$1</code>")
    out = legacyRegexReplaceAll(#"\*\*([^*]+)\*\*"#, in: out, template: "<strong>$1</strong>")
    out = legacyRegexReplaceAll(#"\*([^*]+)\*"#, in: out, template: "<em>$1</em>")
    out = legacyRegexReplaceAll(#"\[([^\]]+)\]\((https?:[^)\s]+)\)"#, in: out,
                                 template: #"<a href="$2" target="_blank" rel="noreferrer">$1</a>"#)
    return out
}

private func legacyIsTableSeparatorLine(_ line: String) -> Bool {
    let trimmed = line.trimmingCharacters(in: .whitespaces)
    guard !trimmed.isEmpty, trimmed.contains("-") else { return false }
    let pattern = #"^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$"#
    guard let re = try? NSRegularExpression(pattern: pattern) else { return false }
    let range = NSRange(trimmed.startIndex..., in: trimmed)
    return re.firstMatch(in: trimmed, range: range) != nil
}

private func legacySplitTableRow(_ line: String) -> [String] {
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

private func legacyParseTableAlignments(_ separatorLine: String) -> [String] {
    legacySplitTableRow(separatorLine).map { raw in
        let c = raw.trimmingCharacters(in: .whitespaces)
        switch (c.hasPrefix(":"), c.hasSuffix(":")) {
        case (true, true): return "center"
        case (false, true): return "right"
        default: return "left"
        }
    }
}

private func legacyRenderTable(headerCells: [String], aligns: [String], bodyRows: [[String]]) -> String {
    let columnCount = headerCells.count
    func alignAttr(_ i: Int) -> String {
        let a = i < aligns.count ? aligns[i] : "left"
        return a == "left" ? "" : " style=\"text-align:\(a)\""
    }
    var html = "<div style=\"overflow-x:auto\"><table><thead><tr>"
    for (i, cell) in headerCells.enumerated() {
        html += "<th\(alignAttr(i))>\(legacyInlineMarkdown(cell))</th>"
    }
    html += "</tr></thead><tbody>"
    for row in bodyRows {
        html += "<tr>"
        for i in 0..<columnCount {
            let raw = i < row.count ? row[i] : ""
            html += "<td\(alignAttr(i))>\(legacyInlineMarkdown(raw))</td>"
        }
        html += "</tr>"
    }
    html += "</tbody></table></div>"
    return html
}

private func legacyMarkdownToHTML(_ src: String) -> String {
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
            out.append("<pre><code>\(legacyEscapeHTML(buf.joined(separator: "\n")))</code></pre>")
            continue
        }

        if line.contains("|"), i + 1 < lines.count, legacyIsTableSeparatorLine(lines[i + 1]) {
            closeList()
            let headerCells = legacySplitTableRow(line)
            let aligns = legacyParseTableAlignments(lines[i + 1])
            i += 2
            var bodyRows: [[String]] = []
            while i < lines.count {
                let rowLine = lines[i]
                if rowLine.trimmingCharacters(in: .whitespaces).isEmpty { break }
                if !rowLine.contains("|") { break }
                bodyRows.append(legacySplitTableRow(rowLine))
                i += 1
            }
            out.append(legacyRenderTable(headerCells: headerCells, aligns: aligns, bodyRows: bodyRows))
            continue
        }

        if let g = legacyRegexGroups(#"^(#{1,4})\s+(.*)$"#, in: line) {
            closeList()
            let level = g[0].count
            out.append("<h\(level)>\(legacyInlineMarkdown(g[1]))</h\(level)>")
            i += 1
            continue
        }

        if let g = legacyRegexGroups(#"^\s*[-*]\s+(.*)$"#, in: line) {
            if openList != "ul" { closeList(); out.append("<ul>"); openList = "ul" }
            out.append("<li>\(legacyInlineMarkdown(g[0]))</li>")
            i += 1
            continue
        }
        if let g = legacyRegexGroups(#"^\s*\d+[.)]\s+(.*)$"#, in: line) {
            if openList != "ol" { closeList(); out.append("<ol>"); openList = "ol" }
            out.append("<li>\(legacyInlineMarkdown(g[0]))</li>")
            i += 1
            continue
        }

        if line.hasPrefix(">") {
            closeList()
            let text = String(line.dropFirst()).trimmingCharacters(in: .whitespaces)
            out.append("<blockquote>\(legacyInlineMarkdown(text))</blockquote>")
            i += 1
            continue
        }

        if line.trimmingCharacters(in: .whitespaces).isEmpty {
            closeList()
            i += 1
            continue
        }

        closeList()
        out.append("<p>\(legacyInlineMarkdown(line))</p>")
        i += 1
    }
    closeList()
    return out.joined(separator: "\n")
}

private func legacyDocumentShell(body: String, transparent: Bool) -> String {
    let background = transparent ? "transparent" : "var(--ground)"
    let padding = transparent ? "0" : "16px"
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
      html, body { background: \(background); }
      body {
        margin: 0; padding: \(padding);
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

// The legacy exported entry points passed `transparent: false` for the
// full-screen shell — that's the only variant the current, post-refactor
// `documentShell` still has (it dropped the `transparent:` param entirely),
// so this is the correct comparison point.
private func legacyMarkdownHTML(_ md: String) -> String {
    legacyDocumentShell(body: legacyMarkdownToHTML(md), transparent: false)
}

private func legacyPlainTextHTML(_ text: String) -> String {
    legacyDocumentShell(body: "<pre>\(legacyEscapeHTML(text))</pre>", transparent: false)
}
