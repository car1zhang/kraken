import XCTest
@testable import Kraken

// Exercises HighlightingTextEditor.commandTokenRanges directly — the pure
// token finder behind the iOS inline command highlighter. Mirrors the same
// anchored-boundary cases CmdChipsRow.isPresent's doc comment calls out
// (mid-word prefix traps like "/doc", path fragments like "web/static/do")
// plus the UTF-16-range correctness the live UITextView attribution depends
// on, since NSRange (unlike Swift's String indices) counts UTF-16 code
// units, not Characters.
final class HighlightingTextEditorTests: XCTestCase {

    private typealias Token = HighlightingTextEditor.CommandToken

    private func matches(_ text: String) -> [Token] {
        HighlightingTextEditor.commandTokenRanges(in: text)
    }

    private func token(_ location: Int, _ length: Int, _ command: String) -> Token {
        Token(range: NSRange(location: location, length: length), command: command)
    }

    // MARK: - all four commands

    func testAllFourCommandsMatch() {
        let text = "/info /aim /do /pr"
        XCTAssertEqual(matches(text), [
            token(0, 5, "/info"),
            token(6, 4, "/aim"),
            token(11, 3, "/do"),
            token(15, 3, "/pr"),
        ])
    }

    // MARK: - boundary traps

    func testDocDoesNotMatchDo() {
        // Trailing \b fails between the shared "o" and "c" — both are word
        // characters, so there's no boundary there for "/do" to stop at.
        XCTAssertEqual(matches("please run /doc now"), [])
    }

    func testPathFragmentDoesNotMatch() {
        // Nothing precedes this "/do" with whitespace (or start-of-string) —
        // the char right before it is another "/", not a boundary.
        XCTAssertEqual(matches("see web/static/do for the source"), [])
    }

    func testTokenFollowedByPunctuationStillMatches() {
        // "." is a non-word character, so \b is satisfied right after "do".
        XCTAssertEqual(matches("looks good, /do."), [
            token(12, 3, "/do"),
        ])
    }

    // MARK: - position edge cases

    func testTokenAtStartOfText() {
        XCTAssertEqual(matches("/info the daemon"), [
            token(0, 5, "/info"),
        ])
    }

    func testTokenAtEndOfText() {
        XCTAssertEqual(matches("next job: /pr"), [
            token(10, 3, "/pr"),
        ])
    }

    func testEmptyStringHasNoMatches() {
        XCTAssertEqual(matches(""), [])
    }

    // MARK: - multiline / repeated

    func testMultilineTextMatchesAcrossLines() {
        let text = "steer notes:\n/do the doc\nthen /pr it"
        XCTAssertEqual(matches(text), [
            token(13, 3, "/do"),
            token(30, 3, "/pr"),
        ])
    }

    func testRepeatedTokenMatchesEachOccurrence() {
        XCTAssertEqual(matches("/aim then /aim again"), [
            token(0, 4, "/aim"),
            token(10, 4, "/aim"),
        ])
    }

    // MARK: - UTF-16 correctness

    func testUTF16RangeAfterMultibyteCharacter() {
        // "é" (U+00E9, precomposed) is one UTF-16 code unit but two UTF-8
        // bytes — a regression here would mean the finder was computing
        // byte offsets instead of the UTF-16 offsets NSRange/UITextView
        // actually require. Expected layout: "é"(0) " "(1) "/"(2) "d"(3)
        // "o"(4) — command range is (location: 2, length: 3).
        XCTAssertEqual(matches("é /do"), [
            token(2, 3, "/do"),
        ])
    }
}
