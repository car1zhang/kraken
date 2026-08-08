import Foundation

// Symbols the REFERENCED `ios/Kraken/*.swift` sources call but that live in
// iOS view files this target does not reference (51-code-map.md lists exactly
// nine referenced files; `JobDetailView.swift` is not one of them, and cannot
// become one — it is a full SwiftUI iOS view).
//
// ios/ is read-only to this job, so these are PORTED here rather than moved
// there. Each is a copy of the iOS body verbatim, and must stay one: the doc
// comments on the referenced `Models.swift` assert this behaviour (e.g.
// `WebLoad.age(nowMillis:)` promises a future stamp floors at "0s"), so a
// divergent version here would make a file this target compiles lie about
// itself. Keep the two in step, or copy the caller in instead.

/// Formats an elapsed span as the app's compact age — "45s", "3m", "3h", "2d",
/// one unit and no decimals. Floored at 0: a stamp from the future reads as
/// "0s", never as a negative.
///
/// Verbatim from `ios/Kraken/JobDetailView.swift`, called by
/// `WebLoad.age(nowMillis:)` in the referenced `Models.swift`.
func compactAgeLabel(secondsElapsed: Int) -> String {
    let s = max(0, secondsElapsed)
    if s < 60 { return "\(s)s" }
    if s < 3600 { return "\(s / 60)m" }
    if s < 86400 { return "\(s / 3600)h" }
    return "\(s / 86400)d"
}
