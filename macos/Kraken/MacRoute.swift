import Foundation

// ---------------------------------------------------------------------------
// SEEDED BY S1 — DELETE THIS BLOCK WHEN THE S3 BOARD SOURCES LAND.
//
// `BoardColumn` is frozen under S3 (`macos/Kraken/Board/`) in
// artifacts/50-contract.md, but `MacRoute` (S1) references it, and the S1
// scaffold has to compile standalone this wave. It is declared here — inside a
// file S1 owns — rather than in a Board/ file S1 does not own, so removing it
// is a single contiguous deletion. If S3 also declares it the build fails with
// "invalid redeclaration of 'BoardColumn'": that is the signal to delete this
// block, not to rename anything.
// ---------------------------------------------------------------------------
enum BoardColumn: String, CaseIterable {
    case draft, queued, working, blocked, done, archived
}
// --------------------------- end of seeded block ---------------------------

/// A deep link the app can be opened with (`kraken://…`, registered via the
/// `CFBundleURLTypes` entry in `macos/project.yml`).
enum MacRoute: Equatable {
    case board(column: BoardColumn?)
    case job(slug: String)
    case graph
    case log
    case dream
    case settings

    /// Parses a `kraken://` URL into a route, or nil when nothing here handles
    /// it. The HOST is the discriminator — `kraken://board/blocked` has host
    /// `board` and path `/blocked`, which is why a column that does not name a
    /// `BoardColumn` degrades to `board(nil)` rather than failing the whole
    /// parse: the user asked for the board and named a filter this build does
    /// not have, and showing the board unfiltered is the honest answer.
    static func parse(_ url: URL) -> MacRoute? {
        guard let host = url.host()?.lowercased(), !host.isEmpty else { return nil }
        let parts = url.pathComponents.filter { $0 != "/" && !$0.isEmpty }

        switch host {
        case "board":
            guard let first = parts.first else { return .board(column: nil) }
            return .board(column: BoardColumn(rawValue: first.lowercased()))
        case "job":
            guard let slug = parts.first, !slug.isEmpty else { return nil }
            return .job(slug: slug.removingPercentEncoding ?? slug)
        case "graph": return .graph
        case "log": return .log
        case "dream": return .dream
        case "settings": return .settings
        default: return nil
        }
    }
}
