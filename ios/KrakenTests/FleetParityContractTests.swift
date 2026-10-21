import XCTest
import Foundation
@testable import Kraken

// FleetParityContractTests — athena-logic's BLIND contract tests for the
// multiplayer-kraken job's Round 2 ("remote parity"), Slice R2-I. Written from
// artifacts/50-contract.md alone; nothing under ios/Kraken was opened by the
// author. LOGIC dimension: `FrontmatterText.body(of:)` and `NodeJobRaw`
// decoding only (the view rows are reviewed, not blind-tested). Odd inputs are
// athena-edge's.

final class FleetParityContractTests: XCTestCase {

    // MARK: - FrontmatterText.body(of:)

    /// Contract R2-I `FrontmatterText`: "The Swift twin of stripFrontmatter:
    /// same rule, same results ("---\nx: 1\n---\n\n# Spec" → "# Spec")".
    func testContractFleetParityBodyDropsFrontmatter() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n\n# Spec"), "# Spec")
        XCTAssertEqual(
            FrontmatterText.body(of: "---\njob: tech-comms\nrepo: kraken\n---\n\n# Spec\n\nbody\n"),
            "# Spec\n\nbody\n")
    }

    /// stripFrontmatter's rule: "returns everything after that closing line
    /// with one leading newline run trimmed".
    func testContractFleetParityBodyTrimsOnlyTheLeadingNewlineRun() {
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n# Spec"), "# Spec")
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\n\n\n# A\n\n\n# B"), "# A\n\n\n# B")
    }

    /// stripFrontmatter's rule: "starts with ---\n (or ---\r\n) and a later
    /// line is exactly --- (trailing \r allowed)".
    func testContractFleetParityBodyAcceptsCRLF() {
        XCTAssertEqual(FrontmatterText.body(of: "---\r\nx: 1\r\n---\r\n\r\n# Spec"), "# Spec")
        XCTAssertEqual(FrontmatterText.body(of: "---\nx: 1\n---\r\n# Spec"), "# Spec")
    }

    /// "text without a leading --- line unchanged; "" → """.
    func testContractFleetParityBodyLeavesPlainTextAlone() {
        XCTAssertEqual(FrontmatterText.body(of: "# Spec\n\nbody\n"), "# Spec\n\nbody\n")
        XCTAssertEqual(FrontmatterText.body(of: "intro\n---\nx: 1\n---\nrest"), "intro\n---\nx: 1\n---\nrest")
        XCTAssertEqual(FrontmatterText.body(of: ""), "")
    }

    // MARK: - NodeJobRaw

    /// Contract R2-I `APIClient.fetchNodeJobRaw`: "struct NodeJobRaw:
    /// Decodable { let content: String } … unknown keys ignored." The body is
    /// GET /api/job/raw's (slug, content, mtime, editable).
    func testContractFleetParityNodeJobRawDecodesContent() throws {
        let json = ##"{"slug":"tech-comms","content":"---\njob: tech-comms\n---\n\n# Spec","mtime":1769040000000,"editable":true}"##
        let raw = try JSONDecoder().decode(NodeJobRaw.self, from: Data(json.utf8))
        XCTAssertEqual(raw.content, "---\njob: tech-comms\n---\n\n# Spec")
        XCTAssertEqual(FrontmatterText.body(of: raw.content), "# Spec")
    }

    func testContractFleetParityNodeJobRawDecodesContentAlone() throws {
        let raw = try JSONDecoder().decode(NodeJobRaw.self, from: Data(##"{"content":"# Spec"}"##.utf8))
        XCTAssertEqual(raw.content, "# Spec")
    }
}
