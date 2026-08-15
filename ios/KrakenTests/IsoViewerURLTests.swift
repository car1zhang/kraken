import XCTest
@testable import Kraken

// Tests for isoViewerURL — the one decision the app makes about the 3D
// diagram view: which artifact loads route through /iso.html. Everything
// beyond the rewrite (compile, bespoke fallback, theming) is the viewer
// page's job and is tested in web/_ui.
final class IsoViewerURLTests: XCTestCase {
    func testHTMLCardRoutesThroughViewer() {
        let url = isoViewerURL(
            baseURL: "http://127.0.0.1:7777",
            path: "/api/artifact/kraken-3d-diagram-compiler/cell/11-compile-flow.html",
            name: "11-compile-flow.html"
        )
        XCTAssertEqual(
            url?.absoluteString,
            "http://127.0.0.1:7777/iso.html?src=/api/artifact/kraken-3d-diagram-compiler/cell/11-compile-flow.html"
        )
    }

    func testHTMExtensionRoutesToo() {
        let url = isoViewerURL(
            baseURL: "http://127.0.0.1:7777",
            path: "/api/artifact/demo/cell/x.htm",
            name: "x.htm"
        )
        XCTAssertNotNil(url)
        XCTAssertEqual(url?.path, "/iso.html")
    }

    func testNonHTMLStaysDirect() {
        for name in ["diagram.svg", "report.pdf", "shot.png", "notes.md"] {
            XCTAssertNil(
                isoViewerURL(baseURL: "http://127.0.0.1:7777", path: "/api/artifact/demo/cell/\(name)", name: name),
                "\(name) must load directly, never through the viewer"
            )
        }
    }

    func testNonArtifactPathRefused() {
        // The viewer page validates ?src= itself too, but the app must not
        // construct a viewer URL for a path outside the artifact tree in the
        // first place.
        XCTAssertNil(isoViewerURL(baseURL: "http://127.0.0.1:7777", path: "/anything/else.html", name: "else.html"))
    }

    func testCaseInsensitiveExtension() {
        XCTAssertNotNil(
            isoViewerURL(baseURL: "http://127.0.0.1:7777", path: "/api/artifact/demo/cell/CARD.HTML", name: "CARD.HTML")
        )
    }

    func testPlusAndAmpersandSurvivePercentEncoded() {
        // URLSearchParams parses the query with form rules: a literal `+`
        // reads as a space and a literal `&` truncates the value, so both
        // leave here percent-encoded. That keeps `+` working end to end; a
        // decoded `&` additionally trips the viewer page's own allowlist and
        // lands on its escaped open-it-flat link rather than rendering — the
        // encoding fixes the truncation, not that refusal.
        let plus = isoViewerURL(
            baseURL: "http://127.0.0.1:7777",
            path: "/api/artifact/demo/cell/a+b.html",
            name: "a+b.html"
        )
        XCTAssertEqual(plus?.query(percentEncoded: true), "src=/api/artifact/demo/cell/a%2Bb.html")
        let amp = isoViewerURL(
            baseURL: "http://127.0.0.1:7777",
            path: "/api/artifact/demo/cell/a&b.html",
            name: "a&b.html"
        )
        XCTAssertEqual(amp?.query(percentEncoded: true), "src=/api/artifact/demo/cell/a%26b.html")
    }
}
