import SwiftUI

// ArchiveDrawer.swift — the collapsible ARCHIVED drawer, inside the DONE
// column. Port of `web/_ui/src/components/board/ArchiveDrawer.tsx`.
//
// Always rendered, even with zero archived jobs, because it doubles as the
// done→archived drop target — the one target that is not a board column.
//
// Open-ness is the stored flag OR `forceOpen` (a search matched something
// archived), and only the FLAG is ever written: the override opens the drawer
// without overwriting the preference, so clearing the query returns it to
// however carl had it set.
//
// The header is DISABLED whenever a press could not show anything, which is
// one rule covering three states: nothing archived at all, a query holding the
// drawer open, and a query that matched nothing archived. All three would
// otherwise write the preference from a press whose only visible effect is a
// triangle flipping. The head still says what is THERE — `0/1` distinguishes
// "no matches" from "nothing archived", which is the part disabling would
// otherwise cost, on the one surface where the drawer is a search's only route
// into the archive.

struct ArchiveDrawerView: View {
    /// The archived jobs to RENDER — already narrowed by the search.
    let archived: [WebJob]
    /// How many the archive HOLDS before the search narrowed it; nil when no
    /// query is running.
    let total: Int?
    let forceOpen: Bool
    let isHighlighted: Bool
    let isHovered: Bool

    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var drag: BoardDragModel
    @AppStorage("kraken-archive-open") private var stored = false

    private var open: Bool { stored || forceOpen }
    private var shown: Int { archived.count }
    /// Does the ARCHIVE hold anything, as opposed to the query having matched
    /// something in it — the triangle's question, and the one place the two
    /// numbers differ.
    private var hasArchived: Bool { (total ?? shown) > 0 }
    private var headDisabled: Bool { shown == 0 || forceOpen }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            head
            // The CARDS follow the query, not the archive: a drawer held open
            // over zero matches renders its head and nothing else, which is
            // what the `0/1` count is there to explain.
            if open && shown > 0 {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(archived) { job in
                        ArchivedCardView(job: job)
                    }
                }
                .padding(.top, 10)
            }
        }
        .padding(.top, 18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(isHovered ? theme.dropTintHover : (isHighlighted ? theme.dropTint : .clear))
        .overlay {
            if isHighlighted {
                Rectangle().strokeBorder(
                    theme.accentColor,
                    style: StrokeStyle(lineWidth: 1, dash: [3, 3])
                )
            }
        }
        .contentShape(Rectangle())
        // The drawer registers itself as the ARCHIVED column's drop region —
        // the done→archived drag's whole target. Archived cards never drag
        // OUT (they carry no drag gesture), so registration is one-way.
        .onGeometryChange(for: CGRect.self) {
            $0.frame(in: .named(BoardDragModel.spaceName))
        } action: { frame in
            drag.registerColumn(.archived, frame: frame)
        }
    }

    private var head: some View {
        Button {
            // Reachable only when `forceOpen` is off, so `open` and `stored`
            // are the same value here and this is a plain flip of the flag.
            stored.toggle()
        } label: {
            HStack(spacing: 7) {
                Text(hasArchived ? (open ? "▾" : "▸") : "·").hudMono(size: 9)
                Text("ARCHIVED").hudDisplay(size: 10, tracking: 0.2)
                Spacer(minLength: 0)
                Text(total == nil ? "\(shown)" : "\(shown)/\(total ?? 0)").hudMono(size: 10)
            }
            .foregroundStyle(theme.mutedColor)
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(theme.ac.color(0.05))
            .overlay(alignment: .leading) { Rectangle().fill(theme.mutedColor).frame(width: 3) }
        }
        .buttonStyle(.plain)
        .disabled(headDisabled)
        // There is deliberately no tooltip explaining the disabled state: a
        // dead head next to an honest count beats a live one that edits a
        // preference invisibly.
        .hudCursor(headDisabled ? .rest : .actable)
    }
}
