import XCTest
@testable import Kraken

// FleetParityEdgeTests — the EDGE dimension of 50-contract.md's "Round 2 —
// remote parity", slice R2-I, for the two blind-tested symbols:
// FrontmatterText.body(of:) on empty input, a lone fence, CRLF (a single
// grapheme cluster in Swift, so a Character-wise "\n" split misses it), an
// unclosed fence and a fence that is not the first line; and NodeJobRaw
// decoding with extra and missing keys. Written blind from the contract, with
// no sight of ios/Kraken. Happy paths are FleetParityContractTests'.
final class FleetParityEdgeTests: XCTestCase {

    // `FrontmatterText`: "The Swift twin of `stripFrontmatter`: same rule,
    // same results … `""` → `""`."
    func testEmptyStringIsEmpty() {
        XCTAssertEqual(FrontmatterText.body(of: ""), "")
    }

    // `stripFrontmatter`: "When `text` starts with `---\n` (or `---\r\n`) and
    // a later line is exactly `---` … otherwise returns `text` unchanged."
    func testTextWithoutACompleteLeadingFenceIsUnchanged() {
        let unchanged = [
            "---",
            "---\n",
            "---\r\n",
            "---\nx: 1\n",
            "---\nx: 1\nbody text",
            "--- \nx: 1\n---\nbody",
            " ---\nx: 1\n---\nbody",
            "----\nx: 1\n---\nbody",
            "---\nx: 1\n--- \nbody",
            "---\nx: 1\n----\nbody",
            "---\nx: 1\n ---\nbody",
            "# Title\n---\nx: 1\n---\nbody",
            "\n---\nx: 1\n---\nbody",
            "plain spec\n",
        ]
        for text in unchanged {
            XCTAssertEqual(FrontmatterText.body(of: text), text, "input \(text.debugDescription)")
        }
    }

    // The contract's own vector.
    func testContractVector() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n\n# Spec"), "# Spec")
    }

    // "(or `---\r\n`) … (trailing `\r` allowed)" — a CRLF document, where
    // every "\r\n" is ONE Character in Swift.
    func testCRLFDocument() {
        XCTAssertEqual(FrontmatterText.body(of: "---\r\nx: 1\r\n---\r\n\r\n# Spec"), "# Spec")
        XCTAssertEqual(FrontmatterText.body(of: "---\r\nx: 1\r\n---\r\n# Spec"), "# Spec")
    }

    // "trailing `\r` allowed" on the closing line of an LF document.
    func testClosingLineWithTrailingCarriageReturn() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\r\nbody"), "body")
    }

    // "with one leading newline run trimmed" — the whole run, nothing past it.
    func testOneLeadingNewlineRunTrimmed() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n\n\n\n# Spec\n\n  indented\n"), "# Spec\n\n  indented\n")
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n  # Spec"), "  # Spec")
    }

    // The FIRST later `---` closes; a rule in the body stays.
    func testFirstClosingFenceWins() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\nbody\n---\nmore"), "body\n---\nmore")
    }

    func testEmptyFrontmatterBlock() {
        XCTAssertEqual(FrontmatterText.body(of: "---\n---\nbody"), "body")
    }

    func testClosingFenceAsLastLineLeavesNothing() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---"), "")
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n"), "")
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n\n\n"), "")
    }

    // MARK: - NodeJobRaw

    // `APIClient.fetchNodeJobRaw`: "`struct NodeJobRaw: Decodable { let
    // content: String }` … unknown keys ignored" — the full `/api/job/raw`
    // body (slug, content, mtime, editable) plus a future key.
    func testNodeJobRawIgnoresExtraKeys() throws {
        let json = #"{"slug":"remote-job","content":"---\nrepo: kraken\n---\n# x","mtime":1700000000000,"editable":false,"future":{"a":[1,2]}}"#
        let raw = try JSONDecoder().decode(NodeJobRaw.self, from: Data(json.utf8))
        XCTAssertEqual(raw.content, "---\nrepo: kraken\n---\n# x")
    }

    func testNodeJobRawEmptyContent() throws {
        let raw = try JSONDecoder().decode(NodeJobRaw.self, from: Data(#"{"content":""}"#.utf8))
        XCTAssertEqual(raw.content, "")
        XCTAssertEqual(FrontmatterText.body(of: raw.content), "")
    }

    // `content` is non-optional: a body without it, with it null, or with it
    // retyped does not decode.
    func testNodeJobRawMissingOrRetypedContentThrows() {
        for json in [#"{"slug":"remote-job","mtime":1}"#, #"{"content":null}"#, #"{"content":42}"#, #"[]"#, #"{"error":"no such job"}"#] {
            XCTAssertThrowsError(try JSONDecoder().decode(NodeJobRaw.self, from: Data(json.utf8)), "input \(json)")
        }
    }
}
