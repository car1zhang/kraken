import SwiftUI

// MARK: - native SwiftUI markdown renderer
//
// Renders `parseMarkdownBlocks` (MarkdownParser.swift) directly as SwiftUI
// views instead of an HTML string in a WKWebView. Each inline body used to
// get its own WKWebView inflated to full content height (the deleted
// MarkdownBody/MarkdownWebView) — fine for one internally-scrolling artifact
// per screen, but a transcript with dozens of inline bodies defeated
// WebKit's viewport tiling and got the app jetsam-killed. This view renders
// every inline markdown body now; the only WKWebView left is ArtifactView's.
//
// Styling mirrors MarkdownHTML.swift's `documentShell` CSS hierarchy (h1
// accent, h2 hairline-underlined, monospace code with a raised/ground
// background) using KTheme (Theme.swift) instead of CSS custom properties.

struct MarkdownText: View {
    let markdown: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(parseMarkdownBlocks(markdown).enumerated()), id: \.offset) { _, block in
                blockView(block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func blockView(_ block: MarkdownBlock) -> some View {
        switch block {
        case .heading(let level, let text):
            headingView(level: level, text: text)
        case .paragraph(let text):
            Text(styledInline(text))
                .font(.voice(13))
                .foregroundStyle(KTheme.text)
                .textSelection(.enabled)
        case .list(let ordered, let items):
            listView(ordered: ordered, items: items)
        case .codeFence(let code):
            codeView(code)
        case .blockquote(let text):
            blockquoteView(text)
        case .table(let header, let alignments, let rows):
            tableView(header: header, alignments: alignments, rows: rows)
        }
    }

    /// h1 → accent/semibold (matches documentShell's `h1 { color: var(--accent) }`);
    /// h2 → hairline underline via a bottom overlay (CSS `border-bottom`);
    /// h3/h4 step down in size only, same as the CSS rules.
    @ViewBuilder
    private func headingView(level: Int, text: String) -> some View {
        switch level {
        case 1:
            Text(styledInline(text))
                .font(.voice(17, weight: .semibold))
                .foregroundStyle(KTheme.accent)
                .textSelection(.enabled)
        case 2:
            Text(styledInline(text))
                .font(.voice(15, weight: .semibold))
                .foregroundStyle(KTheme.text)
                .padding(.bottom, 3)
                .overlay(alignment: .bottom) {
                    Rectangle().fill(KTheme.hairline).frame(height: 1)
                }
                .textSelection(.enabled)
        case 3:
            Text(styledInline(text))
                .font(.voice(14, weight: .semibold))
                .foregroundStyle(KTheme.text)
                .textSelection(.enabled)
        default:
            Text(styledInline(text))
                .font(.voice(13, weight: .semibold))
                .foregroundStyle(KTheme.text)
                .textSelection(.enabled)
        }
    }

    /// Leading "•"/"N." marker column, matching the CSS list's padding-left
    /// indent without relying on SwiftUI's system List chrome.
    private func listView(ordered: Bool, items: [String]) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(Array(items.enumerated()), id: \.offset) { idx, item in
                HStack(alignment: .top, spacing: 6) {
                    Text(ordered ? "\(idx + 1)." : "•")
                        .font(.voice(13))
                        .foregroundStyle(KTheme.text2)
                        .frame(minWidth: 18, alignment: .trailing)
                    Text(styledInline(item))
                        .font(.voice(13))
                        .foregroundStyle(KTheme.text)
                        .textSelection(.enabled)
                }
            }
        }
    }

    /// Raw text, not inline-styled — a code fence's contents are literal,
    /// same as `<pre><code>` on the HTML side. Horizontal scroll instead of
    /// wrapping, matching `pre { overflow-x: auto }`.
    private func codeView(_ code: String) -> some View {
        let shape = RoundedRectangle(cornerRadius: 12, style: .continuous)
        return ScrollView(.horizontal, showsIndicators: false) {
            Text(code)
                .font(.data(12))
                .foregroundStyle(KTheme.text)
                .textSelection(.enabled)
                .padding(10)
        }
        .background(KTheme.ground, in: shape)
        .overlay(shape.stroke(KTheme.hairline, lineWidth: 1))
        .clipShape(shape)
    }

    /// 3pt rounded leading bar in hairline2, dimmed text2 body — matches
    /// `blockquote { border-left; color: var(--text2) }`.
    private func blockquoteView(_ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Capsule()
                .fill(KTheme.hairline2)
                .frame(width: 3)
            Text(styledInline(text))
                .font(.voice(13))
                .foregroundStyle(KTheme.text2)
                .textSelection(.enabled)
        }
    }

    /// Bordered-cell GFM table via Grid, header row on a raised background —
    /// matches the HTML shell's `th { background: var(--raised) }`. Wrapped
    /// in a horizontal ScrollView so a wide table scrolls instead of
    /// squeezing the phone layout, same as the HTML path's overflow-x div.
    private func tableView(header: [String], alignments: [String], rows: [[String]]) -> some View {
        // The cell hairlines are the table's grid and stay square; the TABLE
        // is what gets the rounded corner, clipped as one block so it sits in
        // the glass idiom without redrawing every cell.
        let shape = RoundedRectangle(cornerRadius: 12, style: .continuous)
        return ScrollView(.horizontal, showsIndicators: false) {
            Grid(horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow {
                    ForEach(Array(header.enumerated()), id: \.offset) { i, cell in
                        Text(styledInline(cell))
                            .font(.voice(12, weight: .semibold))
                            .foregroundStyle(KTheme.text)
                            .textSelection(.enabled)
                            .padding(6)
                            .frame(maxWidth: .infinity, alignment: columnAlignment(i, in: alignments))
                            .background(KTheme.raised)
                            .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 0.5))
                    }
                }
                // Ragged rows pad with an empty cell (fewer than the header)
                // against the header's column count — same as renderTable's
                // `i < row.count ? row[i] : ""` in the HTML path.
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(0..<header.count, id: \.self) { i in
                            Text(styledInline(i < row.count ? row[i] : ""))
                                .font(.voice(12))
                                .foregroundStyle(KTheme.text)
                                .textSelection(.enabled)
                                .padding(6)
                                .frame(maxWidth: .infinity, alignment: columnAlignment(i, in: alignments))
                                .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 0.5))
                        }
                    }
                }
            }
            .clipShape(shape)
            .overlay(shape.stroke(KTheme.hairline, lineWidth: 1))
        }
    }

    private func columnAlignment(_ i: Int, in alignments: [String]) -> Alignment {
        switch i < alignments.count ? alignments[i] : "left" {
        case "center": return .center
        case "right": return .trailing
        default: return .leading
        }
    }

    /// `inlineAttributed` plus one bit of styling it can't know about itself
    /// (font/color live in SwiftUI, not Foundation): code-intent runs get the
    /// mono data font and the "revealed" tint, matching `code { font: ...
    /// "SF Mono" ...; color: var(--revealed) }` in the HTML shell.
    private func styledInline(_ s: String) -> AttributedString {
        var attr = inlineAttributed(s)
        for run in attr.runs where run.inlinePresentationIntent == .code {
            attr[run.range].font = .data(12)
            attr[run.range].foregroundColor = KTheme.revealed
        }
        return attr
    }
}
