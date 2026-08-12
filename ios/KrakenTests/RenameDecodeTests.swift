import XCTest
@testable import Kraken

// Decodes POST /api/job/rename's wire shapes (see ios/API.md's
// "POST /api/job/rename" section and internal/api/jobrename.go): the answer
// with `skipped` and/or `notes` populated, and the ordinary answer with both
// keys ABSENT.
//
// The absent cases are the ones worth a test rather than a glance. `skipped`
// and `notes` are the only Optionals on RenameJobResponse and they are
// Optional precisely because the Go side omits each when empty, so every
// ordinary rename — which is nearly all of them — sends a body with two keys.
// A non-Optional `[String]` on either would throw `keyNotFound` on the common
// path, and the failure would surface as "rename failed" beside a rename that
// had in fact already committed: the worst available reading of a success.
//
// The two lists are decoded, and asserted, INDEPENDENTLY: they answer
// different questions (files still under the old name, versus post-commit
// steps that broke with their causes) and either can arrive without the other,
// so a test that only ever saw them together would not catch one being read
// off the wrong key.
//
// Also pinned: `slug` is decoded and is what the caller re-points to. The
// typed text is a request, never the result — JobFormView assigns
// `renamedSlug = res.slug` and never `renamePreview`.

final class RenameDecodeTests: XCTestCase {

    // The paths carry the OLD cell-name prefix on purpose: the server sends
    // the SOURCE — the copy that kept the old name — and never the
    // destination that blocked it, which is the file the notice's "move or
    // delete them by hand" must not be read as naming.
    func testDecodesResponseWithSkipped() throws {
        let json = """
        {
          "ok": true,
          "slug": "my-new-name",
          "skipped": [
            "/Users/carl/Documents/pluto/my-old-name-7.00-overview.md",
            "/Users/carl/Documents/pluto/my-old-name-7.50-contract.md"
          ]
        }
        """
        let r = try JSONDecoder().decode(RenameJobResponse.self, from: Data(json.utf8))

        XCTAssertTrue(r.ok)
        XCTAssertEqual(r.slug, "my-new-name")
        XCTAssertEqual(r.skipped?.count, 2)
        XCTAssertEqual(
            r.skipped?.first,
            "/Users/carl/Documents/pluto/my-old-name-7.00-overview.md"
        )
        // a conflict-only response carries no notes at all
        XCTAssertNil(r.notes)
    }

    // The other list, ALONE — which is a real shape: a tmux convergence step
    // or a zeus notice can fail on a rename where every preserved copy moved
    // cleanly. Each entry is a whole sentence carrying its own cause, not a
    // path, which is why the view renders it as prose in the failure colour
    // rather than as a monospace path list.
    func testDecodesResponseWithNotes() throws {
        let json = """
        {
          "ok": true,
          "slug": "my-new-name",
          "notes": [
            "preserved artifact not renamed: /Users/carl/Documents/pluto/my-old-name-7.90-build-report.md (move to /Users/carl/Documents/pluto/my-new-name-7.90-build-report.md failed: permission denied)",
            "tmux rename-session failed: no server running"
          ]
        }
        """
        let r = try JSONDecoder().decode(RenameJobResponse.self, from: Data(json.utf8))

        XCTAssertTrue(r.ok)
        XCTAssertEqual(r.slug, "my-new-name")
        XCTAssertNil(r.skipped)
        XCTAssertEqual(r.notes?.count, 2)
        XCTAssertEqual(r.notes?.last, "tmux rename-session failed: no server running")
    }

    // Both at once: independent keys, independently populated.
    func testDecodesResponseWithSkippedAndNotes() throws {
        let json = """
        {
          "ok": true, "slug": "n",
          "skipped": ["/p/old-1.00-overview.md"],
          "notes": ["zeus notice not delivered: cell pane is gone"]
        }
        """
        let r = try JSONDecoder().decode(RenameJobResponse.self, from: Data(json.utf8))
        XCTAssertEqual(r.skipped ?? [], ["/p/old-1.00-overview.md"])
        XCTAssertEqual(r.notes ?? [], ["zeus notice not delivered: cell pane is gone"])
    }

    // The ordinary answer: nothing was left behind and nothing broke, so
    // NEITHER key is sent. nil, not [] — and the view reads `?? []` for both,
    // so the two render identically, which is the point of allowing absence.
    func testDecodesResponseWithoutSkippedOrNotes() throws {
        let json = """
        {"ok": true, "slug": "my-new-name"}
        """
        let r = try JSONDecoder().decode(RenameJobResponse.self, from: Data(json.utf8))

        XCTAssertTrue(r.ok)
        XCTAssertEqual(r.slug, "my-new-name")
        XCTAssertNil(r.skipped)
        XCTAssertNil(r.notes)
        XCTAssertEqual(r.skipped ?? [], [])
        XCTAssertEqual(r.notes ?? [], [])
    }

    // Explicit empty arrays are not a shape the handler sends today (it omits
    // each key instead), but a client that treated [] differently from a
    // missing key would be one server-side `omitempty` removal away from
    // rendering an empty warning row and an empty failure row.
    func testDecodesEmptyArrays() throws {
        let json = """
        {"ok": true, "slug": "x", "skipped": [], "notes": []}
        """
        let r = try JSONDecoder().decode(RenameJobResponse.self, from: Data(json.utf8))
        XCTAssertEqual(r.skipped ?? [], [])
        XCTAssertEqual(r.notes ?? [], [])
    }

    // The request body's two keys, spelled as the handler's
    // DisallowUnknownFields decoder demands them: `slug` and `to`, and nothing
    // else. An extra or misspelled key is a 400 rather than a partial rename,
    // so the encoded shape is worth holding.
    func testEncodesRequestBody() throws {
        let data = try JSONEncoder().encode(RenameJobRequest(slug: "old", to: "My New Name!"))
        let obj = try XCTUnwrap(
            JSONSerialization.jsonObject(with: data) as? [String: Any]
        )

        XCTAssertEqual(obj.keys.sorted(), ["slug", "to"])
        XCTAssertEqual(obj["slug"] as? String, "old")
        // `to` is the RAW typed text, not a slug: the server slugifies it, and
        // sending the client's preview instead would make the port's known
        // divergences (see JobSlugTests) decide a stored name.
        XCTAssertEqual(obj["to"] as? String, "My New Name!")
    }
}
