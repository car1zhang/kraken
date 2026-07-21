import XCTest
@testable import Kraken

// Exercises MarkdownParser.swift's block-level pass (`parseMarkdownBlocks`)
// and both inline passes (`inlineHTML`, `inlineAttributed`) directly.
//
// `markdownToHTML` (MarkdownHTML.swift) is file-private, not internal, so
// it isn't reachable here even under @testable import — the table
// padding/truncation and injection-safety cases below (which are properties
// of *rendering*, not parsing: `parseMarkdownBlocks` stores raw ragged rows
// verbatim, and never escapes anything) go through the exported
// `markdownHTML(_:)` wrapper instead. That wrapper's shell is fixed and
// deterministic, so it exercises the exact same code path.
final class MarkdownParserTests: XCTestCase {

    // MARK: - headings

    func testHeadingLevelsOneThroughFour() {
        let blocks = parseMarkdownBlocks("# h1\n## h2\n### h3\n#### h4")
        XCTAssertEqual(blocks, [
            .heading(level: 1, text: "h1"),
            .heading(level: 2, text: "h2"),
            .heading(level: 3, text: "h3"),
            .heading(level: 4, text: "h4"),
        ])
    }

    func testFiveHashesIsNotAHeading() {
        // The heading regex caps at {1,4} — a 5th hash falls through to a
        // plain paragraph, hashes and all.
        let blocks = parseMarkdownBlocks("##### not a heading")
        XCTAssertEqual(blocks, [.paragraph("##### not a heading")])
    }

    // MARK: - paragraphs

    func testEachNonBlankLineIsItsOwnParagraph() {
        // Line-oriented, single pass: consecutive prose lines are NOT
        // merged into one paragraph — each becomes its own block, exactly
        // like the original app.js port.
        let blocks = parseMarkdownBlocks("line one\nline two")
        XCTAssertEqual(blocks, [.paragraph("line one"), .paragraph("line two")])
    }

    // MARK: - lists

    func testUnorderedListGroupsConsecutiveItems() {
        let blocks = parseMarkdownBlocks("- a\n- b\n* c")
        XCTAssertEqual(blocks, [.list(ordered: false, items: ["a", "b", "c"])])
    }

    func testOrderedListGroupsConsecutiveItems() {
        let blocks = parseMarkdownBlocks("1. a\n2. b\n3) c")
        XCTAssertEqual(blocks, [.list(ordered: true, items: ["a", "b", "c"])])
    }

    func testListKindSwitchClosesPriorListAndOpensNew() {
        let blocks = parseMarkdownBlocks("- a\n- b\n1. c\n2. d")
        XCTAssertEqual(blocks, [
            .list(ordered: false, items: ["a", "b"]),
            .list(ordered: true, items: ["c", "d"]),
        ])
    }

    func testBlankLineClosesList() {
        let blocks = parseMarkdownBlocks("- a\n\n- b")
        XCTAssertEqual(blocks, [
            .list(ordered: false, items: ["a"]),
            .list(ordered: false, items: ["b"]),
        ])
    }

    func testListAtEndOfDocumentIsClosedImplicitly() {
        // No trailing blank line or blank-line close — `closeList()` at the
        // bottom of `parseMarkdownBlocks` must still flush it.
        let blocks = parseMarkdownBlocks("intro\n- item1\n- item2")
        XCTAssertEqual(blocks, [
            .paragraph("intro"),
            .list(ordered: false, items: ["item1", "item2"]),
        ])
    }

    // MARK: - fenced code

    func testFencedCodeBlockCapturesLinesBetweenFences() {
        let blocks = parseMarkdownBlocks("```\nline1\nline2\n```")
        XCTAssertEqual(blocks, [.codeFence("line1\nline2")])
    }

    func testUnterminatedFenceRunsToEnd() {
        // No closing "```" — the inner while loop runs off the end of the
        // document instead of swallowing the rest of the doc as one block
        // forever, or crashing.
        let blocks = parseMarkdownBlocks("```\nline1\nline2")
        XCTAssertEqual(blocks, [.codeFence("line1\nline2")])
    }

    func testEmptyFenceProducesEmptyCodeBlock() {
        let blocks = parseMarkdownBlocks("```\n```")
        XCTAssertEqual(blocks, [.codeFence("")])
    }

    // MARK: - blockquotes

    func testBlockquoteIsTrimmedPerLine() {
        // Each ">"-prefixed line becomes its own blockquote block (no
        // multi-line grouping like lists get), leading/trailing whitespace
        // trimmed off the quoted text.
        let blocks = parseMarkdownBlocks(">   line one  \n>line two")
        XCTAssertEqual(blocks, [.blockquote("line one"), .blockquote("line two")])
    }

    func testBlockquoteRunsInterleavedWithParagraphs() {
        let blocks = parseMarkdownBlocks(">q1\npara\n>q2")
        XCTAssertEqual(blocks, [
            .blockquote("q1"),
            .paragraph("para"),
            .blockquote("q2"),
        ])
    }

    // MARK: - GFM tables

    func testBasicTableParsing() {
        let src = "| H1 | H2 |\n| --- | --- |\n| a | b |\n| c | d |"
        let blocks = parseMarkdownBlocks(src)
        XCTAssertEqual(blocks, [
            .table(header: ["H1", "H2"], alignments: ["left", "left"], rows: [["a", "b"], ["c", "d"]]),
        ])
    }

    func testTableAlignmentRowVariants() {
        let cases: [(String, String)] = [
            ("---", "left"),
            (":--", "left"),
            ("--:", "right"),
            (":-:", "center"),
        ]
        for (sep, expected) in cases {
            let src = "| H |\n| \(sep) |\n| v |"
            let blocks = parseMarkdownBlocks(src)
            guard case .table(_, let aligns, _) = blocks.first else {
                XCTFail("expected a table for separator \(sep)")
                continue
            }
            XCTAssertEqual(aligns, [expected], "separator \(sep)")
        }
    }

    func testTableSeparatorWithoutOuterPipes() {
        let src = "H1 | H2\n--- | :-:\na | b"
        let blocks = parseMarkdownBlocks(src)
        XCTAssertEqual(blocks, [
            .table(header: ["H1", "H2"], alignments: ["left", "center"], rows: [["a", "b"]]),
        ])
    }

    func testEscapedPipeStaysLiteralInsideCell() {
        let src = "| a\\|b | c |\n| --- | --- |\n| x | y |"
        let blocks = parseMarkdownBlocks(src)
        XCTAssertEqual(blocks, [
            .table(header: ["a|b", "c"], alignments: ["left", "left"], rows: [["x", "y"]]),
        ])
    }

    func testLonePipeLineWithoutValidSeparatorFallsBackToParagraph() {
        // A stray "|" in prose must never be misparsed as a table just
        // because a pipe appears somewhere on the line.
        let blocks = parseMarkdownBlocks("just | a pipe\nnot a table")
        XCTAssertEqual(blocks, [.paragraph("just | a pipe"), .paragraph("not a table")])
    }

    func testTableAtEndOfDocumentIsClosedImplicitly() {
        let blocks = parseMarkdownBlocks("| H |\n| --- |\n| v |")
        XCTAssertEqual(blocks, [.table(header: ["H"], alignments: ["left"], rows: [["v"]])])
    }

    func testRaggedRowsAreStoredRawByTheParser() {
        // parseMarkdownBlocks itself never pads/truncates — that's a
        // rendering-time concern (MarkdownHTML.swift's renderTable). The
        // parser just keeps whatever cells were on the line.
        let src = "| A | B | C |\n| --- | --- | --- |\n| short |\n| 1 | 2 | 3 | 4 |"
        let blocks = parseMarkdownBlocks(src)
        XCTAssertEqual(blocks, [
            .table(header: ["A", "B", "C"], alignments: ["left", "left", "left"],
                   rows: [["short"], ["1", "2", "3", "4"]]),
        ])
    }

    func testShortRowIsPaddedWithEmptyCellsInHTML() {
        let src = "| A | B | C |\n| --- | --- | --- |\n| only-one |"
        let html = markdownHTML(src)
        // Header has 3 columns; the short body row must render exactly 3
        // <td> cells, the missing two empty, never an out-of-bounds read.
        let tdCount = html.components(separatedBy: "<td").count - 1
        XCTAssertEqual(tdCount, 3)
        XCTAssertTrue(html.contains("<td></td><td></td>"), "missing cells should render as empty <td>s")
    }

    func testLongRowIsTruncatedAgainstHeaderCountInHTML() {
        let src = "| A | B |\n| --- | --- |\n| 1 | 2 | 3 | 4 |"
        let html = markdownHTML(src)
        let tdCount = html.components(separatedBy: "<td").count - 1
        XCTAssertEqual(tdCount, 2, "extra cells beyond the header's column count must be dropped")
        XCTAssertFalse(html.contains(">3<"))
        XCTAssertFalse(html.contains(">4<"))
    }

    // MARK: - injection safety
    //
    // Routed through markdownHTML(_:) since markdownToHTML itself is
    // file-private (see the type comment above) — same escape-then-format
    // code path either way.

    func testScriptTagInParagraphIsEscaped() {
        let html = markdownHTML("<script>alert(1)</script>")
        XCTAssertTrue(html.contains("&lt;script&gt;"))
        XCTAssertFalse(html.contains("<script"))
    }

    func testScriptTagInTableCellIsEscaped() {
        let src = "| <script>alert(1)</script> | b |\n| --- | --- |\n| 1 | 2 |"
        let html = markdownHTML(src)
        XCTAssertTrue(html.contains("&lt;script&gt;"))
        XCTAssertFalse(html.contains("<script"))
    }

    func testScriptTagInCodeFenceIsEscaped() {
        let src = "```\n<script>alert(1)</script>\n```"
        let html = markdownHTML(src)
        XCTAssertTrue(html.contains("&lt;script&gt;"))
        XCTAssertFalse(html.contains("<script"))
    }

    // MARK: - inlineAttributed

    private func styledRuns(_ s: String) -> [(text: String, style: String)] {
        let attributed = inlineAttributed(s)
        return attributed.runs.map { run in
            let text = String(attributed[run.range].characters)
            if run.inlinePresentationIntent == .code { return (text, "code") }
            if run.inlinePresentationIntent == .stronglyEmphasized { return (text, "bold") }
            if run.inlinePresentationIntent == .emphasized { return (text, "italic") }
            if let url = run.link { return (text, "link:\(url.absoluteString)") }
            return (text, "plain")
        }
    }

    func testInlineAttributedStylesEachConstruct() {
        let runs = styledRuns("plain `code` **bold** *italic* [text](https://example.com/a)")
        // Only the styled runs are looked up by text — several unstyled
        // separator runs (the spaces between constructs) share the same
        // text, so a text-keyed dictionary over *all* runs isn't safe.
        let styledByText = Dictionary(
            uniqueKeysWithValues: runs.filter { $0.style != "plain" }.map { ($0.text, $0.style) }
        )
        XCTAssertEqual(styledByText["code"], "code")
        XCTAssertEqual(styledByText["bold"], "bold")
        XCTAssertEqual(styledByText["italic"], "italic")
        XCTAssertEqual(styledByText["text"], "link:https://example.com/a")
        XCTAssertTrue(runs.contains { $0.style == "plain" && $0.text.contains("plain") })
    }

    func testMalformedURLDegradesToPlainText() {
        // "%zz" is invalid percent-encoding — URL(string:) returns nil for
        // it, so the link pass must decline styling and fall back to the
        // bare link text (not the raw "[text](url)" markdown).
        XCTAssertNil(URL(string: "https://%zz"), "test assumption: this must fail to parse as a URL")
        let runs = styledRuns("[bad](https://%zz)")
        XCTAssertEqual(runs.map(\.text).joined(), "bad")
        XCTAssertTrue(runs.allSatisfy { $0.style == "plain" })
    }

    func testMarkdownInsideBackticksStaysLiteral() {
        // Code spans are matched first and never rescanned by the later
        // bold/italic passes.
        let runs = styledRuns("`**bold** *italic*`")
        XCTAssertEqual(runs.count, 1)
        XCTAssertEqual(runs.first?.text, "**bold** *italic*")
        XCTAssertEqual(runs.first?.style, "code")
    }

    func testUnclosedBoldPassesThroughAsPlainText() {
        let runs = styledRuns("**bold text")
        XCTAssertEqual(runs.map(\.text).joined(), "**bold text")
        XCTAssertTrue(runs.allSatisfy { $0.style == "plain" })
    }

    func testInlineHTMLEscapesBeforeFormatting() {
        // esc() runs first in app.js's inlineMD(); a "<" inside a bold span
        // must come out entity-escaped, not raw.
        let html = inlineHTML("**<b>not real html</b>**")
        XCTAssertEqual(html, "<strong>&lt;b&gt;not real html&lt;/b&gt;</strong>")
    }
}
