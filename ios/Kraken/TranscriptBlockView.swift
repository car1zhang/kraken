import SwiftUI

/// One parsed transcript block, rendered identically everywhere a transcript
/// is shown: a peer's session (SessionDetailView) and a side thread
/// (ThreadDetailView) read the same `/api/*/read` block shape, so they render
/// it through one view rather than two copies that drift apart — same reason
/// PRChip exists as a shared struct instead of a per-view helper.
///
/// Pure function of `block`: no view state, no polling, no environment. A
/// caller that wants the block treated specially (the thread view dims
/// inherited pre-fork history) applies its own modifiers on the outside.
struct TranscriptBlockView: View {
    let block: ReadBlock

    var body: some View {
        switch block.kind {
        case "user":
            // A "❯ user" marker keeps the message visually distinct (like
            // `k r`'s header), but the body renders as markdown too — users
            // paste markdown (headings, tables, code) and it should render,
            // not show raw ## / | syntax.
            VStack(alignment: .leading, spacing: 4) {
                Text("❯ user")
                    .font(.data(10))
                    .foregroundStyle(KTheme.accent)
                MarkdownText(markdown: block.text)
            }
        case "thinking":
            Text(block.text)
                .font(.voice(13))
                .italic()
                .foregroundStyle(KTheme.text2)
        case "tool":
            Text("→ " + block.text)
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
        case "teammate":
            if let from = block.from, !from.isEmpty {
                // Short one-liners (a status ping, a quick ack) read fine as a
                // single compact row, like the tool case above. Anything
                // longer/multi-line gets its own "⇄ <from>" header (echoing
                // the "❯ user" header style, but in the accent color so it
                // doesn't get mistaken for carl's own messages) followed by
                // full markdown rendering, same as assistant prose below.
                if block.text.contains("\n") || block.text.count > 100 {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("⇄ " + from)
                            .font(.data(10))
                            .foregroundStyle(KTheme.accent)
                        MarkdownText(markdown: block.text)
                    }
                } else {
                    Text("⇄ \(from) — \(block.text)")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2)
                }
            } else {
                // No `from` — fall back to the same plain rendering as an
                // unrecognized/default block below.
                MarkdownText(markdown: block.text)
            }
        default:
            // Assistant prose — the only block kind rendered as markdown, so
            // **bold**/lists/`code`/links/headings show up properly instead
            // of raw markdown syntax.
            MarkdownText(markdown: block.text)
        }
    }
}
