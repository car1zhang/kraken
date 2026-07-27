import XCTest
@testable import Kraken

// The pure seam under ArtifactPagerView: the display order a swipe walks, the
// index/neighbour lookup the chevrons clamp on, and the singleton fallback a
// selection the sequence doesn't hold degrades to.
//
// ArtifactPagerView itself isn't exercised here — a SwiftUI View's @State
// can't be driven from XCTest (the same reason BoardDragStateTests stops at
// BoardDragState), so the swipe, the bottom bar and the three-live-WKWebView
// ceiling live in the manual matrix in the artifact set's 52-testing-plan.md.
final class ArtifactOrderTests: XCTestCase {

    private func artifact(_ name: String) -> WebArtifact {
        WebArtifact(name: name, url: "/api/artifact/demo/cell/\(name)")
    }

    /// Deliberately interleaved: two folders' children arrive BETWEEN the
    /// top-level files, so a flatten that just echoed the wire order would
    /// pass none of the assertions below.
    private var mixed: [WebArtifact] {
        [
            artifact("00-overview.md"),
            artifact("icon-variations/v1.png"),
            artifact("10-arch.html"),
            artifact("shots/deep/x.svg"),
            artifact("icon-variations/v2.png"),
            artifact("50-components.md"),
        ]
    }

    // MARK: - flattenArtifactOrder

    // Top-level files first in wire order, then each folder in FIRST-SEEN
    // order with its own files after it — what CollapsedArtifactList renders,
    // which is the list the tap came from.
    func testDisplayOrderPutsTopLevelFilesBeforeFolderChildren() {
        XCTAssertEqual(
            flattenArtifactOrder(mixed).map(\.name),
            ["00-overview.md", "10-arch.html", "50-components.md", "v1.png", "v2.png", "x.svg"]
        )
    }

    // "icon-variations" is seen before "shots" even though shots' only file
    // appears earlier in the wire array than icon-variations' second one.
    func testFoldersKeepFirstSeenOrder() {
        let names = flattenArtifactOrder(mixed).map(\.name)
        XCTAssertLessThan(
            names.firstIndex(of: "v1.png") ?? .max,
            names.firstIndex(of: "x.svg") ?? .max
        )
    }

    // The name is rewritten to the leaf at every nesting level; the url never
    // is. That divergence is the reason everything downstream keys on url.
    func testNestedFileKeepsItsFullURLWhileTheNameIsRewritten() {
        let deep = flattenArtifactOrder(mixed).first { $0.url.hasSuffix("x.svg") }
        XCTAssertEqual(deep?.name, "x.svg")
        XCTAssertEqual(deep?.url, "/api/artifact/demo/cell/shots/deep/x.svg")
    }

    func testEmptyListFlattensToEmpty() {
        XCTAssertTrue(flattenArtifactOrder([]).isEmpty)
    }

    // MARK: - artifactIndex / artifactNeighbours

    func testNeighboursInTheMiddle() {
        let pages = flattenArtifactOrder(mixed)
        let n = artifactNeighbours(of: "/api/artifact/demo/cell/10-arch.html", in: pages)
        XCTAssertEqual(n.prev?.name, "00-overview.md")
        XCTAssertEqual(n.next?.name, "50-components.md")
    }

    func testNeighboursClampAtBothEnds() {
        let pages = flattenArtifactOrder(mixed)
        let first = artifactNeighbours(of: pages[0].url, in: pages)
        XCTAssertNil(first.prev)
        XCTAssertEqual(first.next?.name, "10-arch.html")

        let last = artifactNeighbours(of: pages[pages.count - 1].url, in: pages)
        XCTAssertEqual(last.prev?.name, "v2.png")
        XCTAssertNil(last.next)
    }

    // Paging out of a folder is just the next entry — no special case, which
    // is exactly what the flatten buys.
    func testPagingLeavesAFolderIntoTheNextFolder() {
        let pages = flattenArtifactOrder(mixed)
        let n = artifactNeighbours(of: "/api/artifact/demo/cell/icon-variations/v2.png", in: pages)
        XCTAssertEqual(n.next?.url, "/api/artifact/demo/cell/shots/deep/x.svg")
    }

    func testNeighboursOfAnAbsentURLAreBothNil() {
        let n = artifactNeighbours(of: "/api/artifact/demo/cell/ghost.md", in: flattenArtifactOrder(mixed))
        XCTAssertNil(n.prev)
        XCTAssertNil(n.next)
    }

    func testIndexIsNilForAnAbsentURL() {
        XCTAssertNil(artifactIndex(of: "/api/artifact/demo/cell/ghost.md", in: flattenArtifactOrder(mixed)))
    }

    // MARK: - artifactPages

    // The case the whole url-not-name rule exists for: a folder row hands the
    // sheet a WebArtifact whose name has been rewritten to "v2.png", while the
    // job's wire list still calls it "icon-variations/v2.png". Matching on
    // name would miss and collapse the sheet to one page; matching on url
    // finds it at its real position.
    func testNestedSelectionIsFoundByURLDespiteTheRewrittenName() {
        let selected = WebArtifact(name: "v2.png", url: "/api/artifact/demo/cell/icon-variations/v2.png")
        let pages = artifactPages(in: mixed, selected: selected)
        XCTAssertEqual(pages.count, 6)
        XCTAssertEqual(artifactIndex(of: selected.url, in: pages), 4)
    }

    func testTopLevelSelectionKeepsTheWholeSequence() {
        let selected = artifact("00-overview.md")
        let pages = artifactPages(in: mixed, selected: selected)
        XCTAssertEqual(pages.count, 6)
        XCTAssertEqual(artifactIndex(of: selected.url, in: pages), 0)
    }

    // A deep link resolving a name the flattened order doesn't hold, or an
    // artifact a live cell removed between tap and present: one page, and an
    // index that is in range rather than a crash.
    func testUnknownSelectionDegradesToASingleton() {
        let ghost = artifact("ghost.md")
        let pages = artifactPages(in: mixed, selected: ghost)
        XCTAssertEqual(pages.count, 1)
        XCTAssertEqual(pages[0].url, ghost.url)
        XCTAssertEqual(artifactIndex(of: ghost.url, in: pages), 0)
    }

    func testSelectionAgainstAnEmptyListIsASingleton() {
        let only = artifact("00-overview.md")
        let pages = artifactPages(in: [], selected: only)
        XCTAssertEqual(pages.map(\.url), [only.url])
    }
}
