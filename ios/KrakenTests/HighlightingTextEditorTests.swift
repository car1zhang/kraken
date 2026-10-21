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

    // MARK: - all six commands

    func testAllSixCommandsMatch() {
        let text = "/direct /info /aim /do /dream /handoff"
        XCTAssertEqual(matches(text), [
            token(0, 7, "/direct"),
            token(8, 5, "/info"),
            token(14, 4, "/aim"),
            token(19, 3, "/do"),
            token(23, 6, "/dream"),
            token(30, 8, "/handoff"),
        ])
    }

    // /pr is not a command any more: the pr skill folded into do (job 624),
    // so the token finder — built from CmdChipsRow.commands — must not match
    // it, and a typed /pr stays plain text.
    func testRetiredPrIsNotAToken() {
        XCTAssertEqual(matches("/do then /pr it"), [
            token(0, 3, "/do"),
        ])
    }

    func testDirectModifierAndItsPhaseAreTwoTokens() {
        // `/direct do` is the skill's spelling; `/direct /do` is what two
        // chip taps produce. The modifier is a token either way, the phase
        // only when it keeps its slash — and "/directory" is not "/direct".
        XCTAssertEqual(matches("/direct do"), [token(0, 7, "/direct")])
        XCTAssertEqual(matches("/direct /do"), [token(0, 7, "/direct"), token(8, 3, "/do")])
        XCTAssertEqual(matches("/directory listing"), [])
    }

    func testEveryChipCommandIsRecognized() {
        // The chip row and the highlighter share CmdChipsRow.commands, so a
        // chip that the finder does not recognize would insert text it then
        // fails to colour — the drift this pins against.
        for cmd in CmdChipsRow.commands {
            XCTAssertEqual(matches(cmd), [token(0, cmd.utf16.count, cmd)], cmd)
        }
    }

    func testHandoffResumeIsStillTheHandoffToken() {
        // `/handoff resume` is the form the skill types into its own pane;
        // only the command token is coloured, the argument stays plain.
        XCTAssertEqual(matches("/handoff resume"), [token(0, 8, "/handoff")])
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
        XCTAssertEqual(matches("next job: /do"), [
            token(10, 3, "/do"),
        ])
    }

    func testEmptyStringHasNoMatches() {
        XCTAssertEqual(matches(""), [])
    }

    // MARK: - multiline / repeated

    func testMultilineTextMatchesAcrossLines() {
        let text = "steer notes:\n/do the doc\nthen /aim it"
        XCTAssertEqual(matches(text), [
            token(13, 3, "/do"),
            token(30, 4, "/aim"),
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
