import AppKit
import SwiftUI

// Panels.swift — the drilldown surfaces both inspectors mount, plus the two
// pieces of chrome they share (the button vocabulary and the toast).
//
// Every panel here is MODAL-ONLY by design, and that is the same split the web
// board draws: a card carries a COUNT and the drilldown carries the thing
// itself. A card that inlined the questions panel grew taller than everything
// around it and buried the one thing the board exists to show.

// MARK: - buttons

/// The board's button vocabulary (style.css :1712-1760). The base `.btn` is an
/// OUTLINE control; `.primary` is the one that takes the accent fill and wears
/// the trailing shear. Sizes are pinned rather than intrinsic because a modal
/// head is ONE row at ONE height — a strip of assorted button sizes reads as
/// assorted buttons rather than as an instrument strip.
enum HudButtonStyle {
    case standard
    case ghost
    case danger
    case ok
    case warn
    /// The composer's send: accent fill, trailing shear.
    case big
}

struct HudButton: View {
    @Environment(\.hudTheme) private var theme
    @Environment(\.isEnabled) private var isEnabled
    @State private var hovering = false

    let title: String
    var style: HudButtonStyle = .standard
    let action: () -> Void

    init(_ title: String, style: HudButtonStyle = .standard, action: @escaping () -> Void) {
        self.title = title
        self.style = style
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            Text(title)
                .hudDisplay(size: 9, tracking: 0.10)
                .foregroundStyle(foreground)
                .padding(.horizontal, style == .big ? 14 : 9)
                .padding(.vertical, style == .big ? 4 : 3)
                .background(background)
                .modifier(BorderShape(style: style, colour: border))
                .hudGlow(hovering && isEnabled ? glow : nil)
        }
        .buttonStyle(.plain)
        .opacity(isEnabled ? 1 : 0.45)
        .onHover { hovering = $0 }
    }

    private struct BorderShape: ViewModifier {
        let style: HudButtonStyle
        let colour: Color
        func body(content: Content) -> some View {
            if style == .big {
                content.hudShear(8, border: colour)
            } else {
                content.overlay { Rectangle().strokeBorder(colour, lineWidth: 1) }
            }
        }
    }

    private var foreground: Color {
        switch style {
        case .standard, .ghost: return hovering ? theme.textColor : theme.mutedColor
        case .danger: return theme.counterColor
        case .ok: return theme.okColor
        case .warn: return theme.warnColor
        case .big: return theme.accentInk
        }
    }

    private var background: Color {
        switch style {
        case .big: return theme.accentColor
        case .ghost: return hovering ? theme.ac.color(0.08) : .clear
        case .danger: return hovering ? theme.ac2.color(0.14) : .clear
        case .ok: return hovering ? theme.ok.color(0.14) : .clear
        case .warn: return hovering ? theme.warn.color(0.16) : theme.warn.color(0.08)
        case .standard: return .clear
        }
    }

    private var border: Color {
        switch style {
        case .big: return theme.accentColor
        case .ghost: return hovering ? theme.borderStrongColor : theme.borderColor
        case .danger: return hovering ? theme.counterColor : theme.ac2.color(0.45)
        case .ok: return hovering ? theme.okColor : theme.ok.color(0.55)
        case .warn: return hovering ? theme.warnColor : theme.warn.color(0.50)
        case .standard: return hovering ? theme.accentColor : theme.borderStrongColor
        }
    }

    private var glow: HudGlow? {
        switch style {
        case .big: return theme.primaryHoverGlow
        case .danger: return theme.glowHotStyle
        case .warn: return theme.glowWarnStyle
        case .ghost, .ok: return nil
        case .standard: return theme.glowStyle
        }
    }
}

// MARK: - toast

/// The transient bottom-centre notice. A module-level singleton for the same
/// reason `showToast` is a bare function on the web: its callers sit in three
/// different layers (a reverted reorder, a refused rotation, a tmux focus
/// failure), and threading a setter through all of them would be strictly more
/// plumbing for the same behaviour. There is only ever ONE on screen — a
/// second message replaces the first and RESTARTS its 4s timer.
@MainActor
final class HudToast: ObservableObject {
    static let shared = HudToast()

    @Published private(set) var message: String = ""
    @Published private(set) var visible = false

    /// Bumped per show so two identical messages in a row restart the timer
    /// rather than let the first one's expiry close the second.
    private var sequence = 0

    private init() {}

    func show(_ message: String) {
        self.message = message
        visible = true
        sequence += 1
        let mine = sequence
        DispatchQueue.main.asyncAfter(deadline: .now() + 4) { [weak self] in
            guard let self, self.sequence == mine else { return }
            self.visible = false
        }
    }
}

struct HudToastView: View {
    @Environment(\.hudTheme) private var theme
    @ObservedObject private var toast = HudToast.shared

    var body: some View {
        VStack {
            Spacer()
            if toast.visible {
                Text(toast.message)
                    .hudMono(size: 10, tracking: HudTracking.stat)
                    .foregroundStyle(theme.counterColor)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 8)
                    .frame(maxWidth: 720, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
                    .background(HudTheme.mix(theme.ac2, 12, over: theme.b0).color)
                    .hudCut(CornerCuts.toast, border: theme.counterColor)
                    .hudGlow(theme.glowHotStyle)
                    .padding(.bottom, 24)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .allowsHitTesting(false)
    }
}

// MARK: - formatting

/// The two formatters both inspectors share. Ported from `lib/format.ts` and
/// deliberately NOT re-derived: the web board and the phone both print these
/// strings, and a third spelling is a third thing to drift.
enum HudFormat {
    /// `fmtTokens` (format.ts:129) — 1.23M / 45.6K / 900, promoting rather
    /// than printing "1000K", and trimming zeros only INSIDE a decimal part
    /// (a blanket trim turns "100" into "1").
    static func tokens(_ n: Int64) -> String {
        let v = Double(max(0, n))
        let units: [(Double, String)] = [(1e9, "B"), (1e6, "M"), (1e3, "K")]
        for (i, unit) in units.enumerated() {
            let (div, suffix) = unit
            guard v >= div else { continue }
            let scaled = v / div
            let places = scaled < 10 ? 2 : scaled < 100 ? 1 : 0
            var out = String(format: "%.\(places)f", scaled)
            if (Double(out) ?? 0) >= 1000, i > 0 { return "1" + units[i - 1].1 }
            if out.contains(".") {
                while out.hasSuffix("0") { out.removeLast() }
                if out.hasSuffix(".") { out.removeLast() }
            }
            return out + suffix
        }
        return String(Int(v.rounded()))
    }

    /// `age` (format.ts:72) — one unit, no decimals, floored at 0. Takes unix
    /// MILLIS because every stamp on this wire is millis and the divide is
    /// exactly where the two boards have gone wrong before.
    static func age(millis: Int64, now: Date = Date()) -> String {
        guard millis > 0 else { return "" }
        let seconds = Int(max(0, Int64(now.timeIntervalSince1970 * 1000) - millis) / 1000)
        return compactAgeLabel(secondsElapsed: seconds)
    }
}

// MARK: - markdown

/// In-app markdown as one selectable container.
///
/// One `Text` per block rather than per line, and `.textSelection(.enabled)`,
/// because 53-concessions B names cross-view selection as a true concession:
/// SwiftUI has no selection that spans views, so long prose surfaces have to
/// be as FEW views as the content allows or a reader cannot sweep a sentence
/// that wraps.
struct HudMarkdown: View {
    @Environment(\.hudTheme) private var theme
    let source: String
    var size: CGFloat = 11

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(parseMarkdownBlocks(source).enumerated()), id: \.offset) { _, block in
                blockView(block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
    }

    @ViewBuilder
    private func blockView(_ block: MarkdownBlock) -> some View {
        switch block {
        case .heading(let level, let text):
            Text(inlineAttributed(text))
                .hudDisplay(size: max(size, 15 - CGFloat(level)), tracking: 0.06)
                .foregroundStyle(theme.textColor)
        case .paragraph(let text):
            Text(inlineAttributed(text))
                .hudMono(size: size)
                .foregroundStyle(theme.bodyColor)
        case .list(let ordered, let items):
            VStack(alignment: .leading, spacing: 2) {
                ForEach(Array(items.enumerated()), id: \.offset) { i, item in
                    HStack(alignment: .top, spacing: 6) {
                        Text(ordered ? "\(i + 1)." : "•")
                            .hudMono(size: size)
                            .foregroundStyle(theme.mutedColor)
                        Text(inlineAttributed(item))
                            .hudMono(size: size)
                            .foregroundStyle(theme.bodyColor)
                    }
                }
            }
        case .codeFence(let code):
            Text(code)
                .hudMono(size: size)
                .foregroundStyle(theme.warnColor)
                .padding(6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(theme.raisedColor)
        case .blockquote(let text):
            HStack(alignment: .top, spacing: 6) {
                Rectangle().fill(theme.accentEdge).frame(width: 2)
                Text(inlineAttributed(text))
                    .hudMono(size: size)
                    .foregroundStyle(theme.mutedColor)
            }
        case .table(let header, _, let rows):
            VStack(alignment: .leading, spacing: 2) {
                Text(header.joined(separator: " · "))
                    .hudMono(size: size, weight: .semibold)
                    .foregroundStyle(theme.textColor)
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    Text(row.joined(separator: " · "))
                        .hudMono(size: size)
                        .foregroundStyle(theme.bodyColor)
                }
            }
        }
    }
}

// MARK: - the colour dropdown

/// A job's colour: a button showing the current dot and name, opening a
/// listbox of "no colour" first, then `/color`'s own eight in `/color`'s own
/// order.
///
/// Three things carried over verbatim from `SwatchStrip.tsx`:
///
///   - "no colour" is a REAL row with value `""` rather than an absence, so an
///     unrecognised colour off the wire falls back to it instead of leaving
///     the button blank with no way to click back to neutral;
///   - re-picking the value already in force STILL posts — the endpoint is
///     idempotent, and a re-pick after a failed POST has to be a retry rather
///     than a silent no-op;
///   - Escape closes the MENU and nothing else. On the web that needs a
///     capture-phase listener to beat the Escape stack; here it is a LOCAL
///     ESCAPE CLAIM on the router, which is the same statement made as data
///     (see OverlayRouter's claim stack).
struct SwatchStrip: View {
    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var router: OverlayRouter

    /// The job's current colour; anything not in `JobPalette.names` reads as
    /// "no colour".
    let value: String?
    let onPick: (String) -> Void

    @State private var open = false
    @State private var claim: UUID?

    private var want: String {
        guard let value, JobPalette.names.contains(value) else { return "" }
        return value
    }

    var body: some View {
        Button {
            open.toggle()
        } label: {
            HStack(spacing: 5) {
                dot(want)
                Text(want.isEmpty ? "NO COLOR" : want.uppercased())
                    .hudDisplay(size: 9, tracking: HudTracking.stat)
                    .foregroundStyle(theme.mutedColor)
                Text("▾").hudMono(size: 8).foregroundStyle(theme.mutedColor)
            }
            .padding(.horizontal, 9)
            .padding(.vertical, 3)
            .hudCut(CornerCuts.colorButton, border: theme.borderColor)
        }
        .buttonStyle(.plain)
        .help("job color")
        .popover(isPresented: $open, arrowEdge: .bottom) { menu }
        .onChange(of: open) { _, isOpen in
            if isOpen {
                // The claim, not a boolean: it is released by id, so a menu
                // torn down while another control also holds a claim cannot
                // strand the one above it.
                claim = router.claimEscape { open = false }
            } else if let id = claim {
                router.releaseEscape(id)
                claim = nil
            }
        }
        .onDisappear {
            if let id = claim { router.releaseEscape(id) }
            claim = nil
        }
    }

    private var menu: some View {
        VStack(alignment: .leading, spacing: 0) {
            row("")
            ForEach(JobPalette.names, id: \.self) { row($0) }
        }
        .padding(4)
        .background(theme.raisedColor)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("job color")
    }

    private func row(_ val: String) -> some View {
        let selected = want == val
        return Button {
            open = false
            onPick(val)
        } label: {
            HStack(spacing: 6) {
                dot(val)
                Text(val.isEmpty ? "NO COLOR" : val.uppercased())
                    .hudDisplay(size: 9, tracking: HudTracking.stat)
                    .foregroundStyle(selected ? theme.textColor : theme.mutedColor)
                Spacer(minLength: 12)
                if selected { Text("✓").hudMono(size: 9).foregroundStyle(theme.accentColor) }
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(val.isEmpty ? "no color" : val)
    }

    @ViewBuilder
    private func dot(_ val: String) -> some View {
        let colour = JobPalette.swatch(val, theme.mode)
        Rectangle()
            .fill(colour ?? Color.clear)
            .overlay { if colour == nil { Rectangle().strokeBorder(theme.chipBorder, lineWidth: 1) } }
            .frame(width: 8, height: 8)
    }
}

// MARK: - doc questions

/// Unsent answers, surviving a close/reopen. Module-level for exactly the
/// reason the web's `dqDrafts` is: the panel is modal-only, so a closing modal
/// has nowhere to hand its draft to — and the closes that matter most are not
/// the user's. Auto-close fires the instant the job changes state, which is
/// precisely when someone is mid-answer.
///
/// An entry is dropped when its answer posts, or when the panel goes away with
/// that input EMPTY: a deliberately cleared answer must not come back on the
/// next open from a stale stash.
@MainActor
enum DocQuestionDrafts {
    private static var store: [String: String] = [:]

    static func key(slug: String, id: String) -> String { slug + "\u{0}" + id }

    static func get(slug: String, id: String) -> String? { store[key(slug: slug, id: id)] }

    static func set(slug: String, id: String, text: String) {
        let k = key(slug: slug, id: id)
        if text.isEmpty { store.removeValue(forKey: k) } else { store[k] = text }
    }

    static func drop(slug: String, id: String) { store.removeValue(forKey: key(slug: slug, id: id)) }
}

/// zeus' open doc questions, answerable in place.
///
/// Sort: open first by importance P0 < P1 < P2 (ties keep file order — the
/// sort is stable), then answered last in file order. An ANSWERED row is a
/// click target that reopens it as editable, because answers stay editable
/// forever, live or done.
struct DocQuestionsPanel: View {
    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var board: BoardStore

    let slug: String
    let questions: [WebDocQuestion]

    @State private var values: [String: String] = [:]
    @State private var reanswering: Set<String> = []
    @State private var busy: Set<String> = []

    private static let rank: [String: Int] = ["P0": 0, "P1": 1, "P2": 2]

    static func sorted(_ list: [WebDocQuestion]) -> [WebDocQuestion] {
        let indexed = Array(list.enumerated())
        let open = indexed.filter { !$0.element.isAnswered }
        let answered = indexed.filter { $0.element.isAnswered }
        let openSorted = open.sorted { a, b in
            let ra = rank[a.element.importance] ?? 3
            let rb = rank[b.element.importance] ?? 3
            return ra == rb ? a.offset < b.offset : ra < rb
        }
        return openSorted.map(\.element) + answered.map(\.element)
    }

    var body: some View {
        let rows = Self.sorted(questions)
        VStack(alignment: .leading, spacing: 8) {
            ForEach(rows) { q in
                if q.isAnswered && !reanswering.contains(q.id) {
                    answeredRow(q)
                } else {
                    editableRow(q)
                }
            }
            if rows.contains(where: { !$0.isAnswered }) {
                Text("answers are read by the agent next time it works")
                    .hudMono(size: 9)
                    .foregroundStyle(theme.mutedColor)
            }
        }
        .onAppear {
            for q in questions {
                if let d = DocQuestionDrafts.get(slug: slug, id: q.id) { values[q.id] = d }
            }
        }
        .onDisappear {
            for (id, text) in values { DocQuestionDrafts.set(slug: slug, id: id, text: text) }
        }
    }

    @ViewBuilder
    private func editableRow(_ q: WebDocQuestion) -> some View {
        let disabled = busy.contains(q.id)
        VStack(alignment: .leading, spacing: 4) {
            head(q)
            if let ctx = q.context, !ctx.isEmpty {
                Text(ctx).hudMono(size: 9).foregroundStyle(theme.mutedColor)
            }
            if let rec = q.recommendation, !rec.isEmpty {
                Text("rec: " + rec).hudMono(size: 9).foregroundStyle(theme.tintColor)
            }
            HStack(spacing: 6) {
                if let rec = q.recommendation, !rec.isEmpty {
                    HudButton("accept rec", style: .ghost) { send(q.id, rec) }
                        .disabled(disabled)
                }
                TextField(
                    "answer…",
                    text: Binding(
                        get: { values[q.id] ?? (reanswering.contains(q.id) ? (q.answer ?? "") : "") },
                        set: { values[q.id] = $0 }
                    )
                )
                .hudField()
                .disabled(disabled)
                // Enter submits. SwiftUI's onSubmit never fires mid-IME
                // composition, which is the guard the web has to write by hand.
                .onSubmit { submit(q) }
                HudButton("send", style: .ghost) { submit(q) }
                    .disabled(disabled)
                if reanswering.contains(q.id) {
                    HudButton("cancel", style: .ghost) {
                        reanswering.remove(q.id)
                        values[q.id] = nil
                        DocQuestionDrafts.drop(slug: slug, id: q.id)
                    }
                    .disabled(disabled)
                }
            }
        }
        .padding(6)
        .background(theme.raisedColor)
    }

    @ViewBuilder
    private func answeredRow(_ q: WebDocQuestion) -> some View {
        Button {
            reanswering.insert(q.id)
            values[q.id] = q.answer ?? ""
        } label: {
            VStack(alignment: .leading, spacing: 4) {
                head(q)
                Text("✓ " + (q.answer ?? ""))
                    .hudMono(size: 10)
                    .foregroundStyle(theme.okColor)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(6)
            .background(theme.raisedColor)
            .opacity(0.72)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(q.answeredAt.map { "answered \($0) — click to re-answer" } ?? "click to re-answer")
    }

    @ViewBuilder
    private func head(_ q: WebDocQuestion) -> some View {
        HStack(spacing: 6) {
            Text(q.importance.isEmpty ? "P2" : q.importance)
                .hudDisplay(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(q.importance == "P0" ? theme.counterColor : theme.warnColor)
            Text("\(q.id) — \(q.text)")
                .hudMono(size: 10)
                .foregroundStyle(theme.textColor)
                .lineLimit(3)
        }
    }

    private func submit(_ q: WebDocQuestion) {
        let text = (values[q.id] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        send(q.id, text)
    }

    private func send(_ id: String, _ text: String) {
        guard !busy.contains(id) else { return }
        // synchronous, so a double-click or a double-Enter cannot double-post
        busy.insert(id)
        Task {
            let err = await board.mutate(
                path: "/api/doc-question", body: ["slug": slug, "id": id, "text": text]
            )
            busy.remove(id)
            guard err == nil else { return }  // the row stays open, its text intact
            reanswering.remove(id)
            values[id] = nil
            DocQuestionDrafts.drop(slug: slug, id: id)
        }
    }
}

// MARK: - feedback

/// carl's per-artifact notes, editable and deletable.
///
/// An ADDRESSED entry (the nightly dream pass recorded a durable fix — no
/// other agent writes that column) dims but stays fully editable: a note
/// staying accurate is more useful than freezing it the moment zeus reacts.
struct FeedbackPanel: View {
    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var board: BoardStore

    let slug: String
    let entries: [WebFeedback]

    @State private var editing: Set<Int> = []
    @State private var drafts: [Int: String] = [:]
    @State private var notes: [Int: String] = [:]
    @State private var saving: Set<Int> = []

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(entries) { fb in
                if editing.contains(fb.id) { editRow(fb) } else { readRow(fb) }
            }
        }
    }

    @ViewBuilder
    private func readRow(_ fb: WebFeedback) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Text(fb.artifact)
                    .hudMono(size: 9, weight: .medium)
                    .foregroundStyle(theme.tintColor)
                Spacer(minLength: 8)
                HudButton("edit", style: .ghost) {
                    drafts[fb.id] = fb.note
                    editing.insert(fb.id)
                }
                HudButton("delete", style: .ghost) { confirmDelete(fb) }
            }
            Text(fb.note)
                .hudMono(size: 10)
                .foregroundStyle(theme.bodyColor)
                .textSelection(.enabled)
            if let addressed = fb.addressed, !addressed.isEmpty {
                Text("✓ " + addressed)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.okColor)
                    .help(fb.addressedAt.map { "addressed " + $0 } ?? "")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(6)
        .background(theme.raisedColor)
        .opacity(fb.isAddressed ? 0.6 : 1)
    }

    @ViewBuilder
    private func editRow(_ fb: WebFeedback) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(fb.artifact).hudMono(size: 9, weight: .medium).foregroundStyle(theme.tintColor)
            TextEditor(text: Binding(get: { drafts[fb.id] ?? fb.note }, set: { drafts[fb.id] = $0 }))
                .hudEditor(minHeight: 54)
            HStack(spacing: 8) {
                Text(notes[fb.id] ?? "").hudMono(size: 9).foregroundStyle(theme.counterColor)
                Spacer(minLength: 8)
                HudButton("cancel", style: .ghost) {
                    editing.remove(fb.id)
                    drafts[fb.id] = nil
                    notes[fb.id] = nil
                }
                HudButton("save", style: .ghost) { save(fb) }
                    .disabled(saving.contains(fb.id))
            }
        }
        .padding(6)
        .background(theme.raisedColor)
    }

    private func save(_ fb: WebFeedback) {
        let text = (drafts[fb.id] ?? fb.note).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else {
            notes[fb.id] = "feedback is empty"
            return
        }
        saving.insert(fb.id)
        Task {
            let err = await board.mutate(
                path: "/api/feedback", body: ["slug": slug, "id": fb.id, "feedback": text]
            )
            saving.remove(fb.id)
            if err != nil {
                // the server's own sentence is already in the status bar; the
                // row only has to stay open
                notes[fb.id] = "save failed — see status bar"
            } else {
                editing.remove(fb.id)
                notes[fb.id] = nil
            }
        }
    }

    private func confirmDelete(_ fb: WebFeedback) {
        let alert = NSAlert()
        alert.messageText = "delete this feedback note?"
        alert.informativeText = "this can't be undone."
        alert.addButton(withTitle: "delete")
        alert.addButton(withTitle: "cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        Task {
            _ = await board.mutate(
                path: "/api/feedback", body: ["slug": slug, "id": fb.id, "delete": true]
            )
        }
    }
}

// MARK: - tokens

/// What the job actually cost.
///
/// ABSENT is not zero, and that is the whole of this panel's logic: no
/// `tokens` key means never measured (render nothing), while a measured zero
/// arrives as an object of zeros and is a real answer. The ORIGIN split is
/// checked on ONE key rather than on a sum — the four ride together or not at
/// all, and a job whose lead genuinely burned nothing is a real state a
/// sum-based test would hide.
struct TokensPanel: View {
    @Environment(\.hudTheme) private var theme
    let tokens: WebTokens?

    /// The same gate `ChipRules.tokensVisible` applies, restated here so the
    /// panel can be mounted unconditionally.
    static func visible(_ t: WebTokens?) -> Bool {
        guard let t else { return false }
        let total = t.input + t.output + t.cacheWrite + t.cacheRead
        return !(total == 0 && t.sessions == 0)
    }

    var body: some View {
        if let t = tokens, Self.visible(t) {
            let total = t.input + t.output + t.cacheWrite + t.cacheRead
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline, spacing: 5) {
                    Text(HudFormat.tokens(total))
                        .hudDisplay(size: 16)
                        .foregroundStyle(theme.textColor)
                    Text("tokens").hudMono(size: 9).foregroundStyle(theme.mutedColor)
                }
                cells([
                    ("in", t.input), ("out", t.output),
                    ("cache w", t.cacheWrite), ("cache r", t.cacheRead),
                ])
                // subagents is the REMAINDER, never a second stored half: two
                // halves on the wire could arrive disagreeing with the total,
                // a subtracted one cannot.
                if let zeus = t.zeusTotal {
                    cells([("zeus", min(zeus, total)), ("subagents", max(0, total - zeus))])
                }
                Text(note(t)).hudMono(size: 9).foregroundStyle(theme.mutedColor)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .help(
                "token spend summed from this job's own Claude Code transcripts, subagent sidecars included. "
                    + "zeus is the cell's lead agent and its side threads; subagents is every teammate and in-process subagent it delegated to. "
                    + "Counters are raw and unweighted; a transcript still being written is resumed next pass, so treat these as a floor."
            )
        }
    }

    private func cells(_ list: [(String, Int64)]) -> some View {
        HStack(spacing: 12) {
            ForEach(list, id: \.0) { pair in
                HStack(spacing: 4) {
                    Text(pair.0).hudMono(size: 9).foregroundStyle(theme.mutedColor)
                    Text(HudFormat.tokens(pair.1)).hudMono(size: 9).foregroundStyle(theme.bodyColor)
                }
            }
            Spacer(minLength: 0)
        }
    }

    private func note(_ t: WebTokens) -> String {
        var parts: [String] = []
        parts.append(
            t.sessions > 0
                ? "measured across \(t.sessions) transcript\(t.sessions == 1 ? "" : "s")"
                : "measured"
        )
        let when = HudFormat.age(millis: t.updatedAt)
        if !when.isEmpty { parts.append(when + " ago") }
        return parts.joined(separator: " · ")
    }
}

// MARK: - the load drilldown

/// zeus' triage estimate with its rungs NAMED.
///
/// No column gate, unlike the card's badge: `ChipRules.loadChipVisible` demands
/// the card read as blocked because a stale estimate sitting in the blocked
/// column reads as a current fact about a job that has since started running.
/// That argument is about a card being SCANNED, not about a drilldown being
/// opened deliberately — so this renders on any live job that has one, with
/// the AGE beside it, which is what makes a stale answer read as stale.
///
/// The anchor labels come from `LoadAnchors` in the referenced `Models.swift`
/// — the same twenty strings a Go test pins byte-identical against
/// `.claude/skills/load/SKILL.md` and the web board. Rendering the label rather
/// than restating the ladder is what keeps that pin meaningful.
struct LoadDrill: View {
    @Environment(\.hudTheme) private var theme
    let load: WebJobLoad

    private static let summary =
        "zeus' triage estimate for this block. unblock is what answering costs CARL — reading, context, decisions. "
        + "next step is how big the claude work that answer buys is likely to be. roi is the second over the first, "
        + "so a cheap answer buying a long session ranks above an expensive one buying a message."

    var body: some View {
        if let roi = load.roi {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text("roi " + String(format: "%.1f", roi) + "×")
                        .hudDisplay(size: 11, tracking: HudTracking.stat)
                        .foregroundStyle(bandColour)
                    if let age = load.age(nowMillis: Int64(Date().timeIntervalSince1970 * 1000)) {
                        Text("estimated \(age) ago")
                            .hudMono(size: 9)
                            .foregroundStyle(theme.mutedColor)
                    }
                    Spacer(minLength: 0)
                }
                rung("unblock \(load.unblock)/10", LoadAnchors.label(unblock: load.unblock))
                rung("next step \(load.scope)/10", LoadAnchors.label(scope: load.scope))
                if !load.note.isEmpty {
                    Text(load.note).hudMono(size: 9).foregroundStyle(theme.mutedColor)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(6)
            .background(theme.raisedColor)
            .overlay(alignment: .leading) { Rectangle().fill(bandColour).frame(width: 2) }
            .accessibilityElement(children: .contain)
            .accessibilityLabel(Self.summary)
            .help(Self.summary)
        }
    }

    private func rung(_ key: String, _ anchor: String?) -> some View {
        HStack(spacing: 8) {
            Text(key).hudMono(size: 9, weight: .medium).foregroundStyle(theme.bodyColor)
            // dropped rather than printed as "unknown": a rung nobody defined
            // is a number the server should never have served, and a bare
            // number says that more honestly than a placeholder beside it
            Text(anchor ?? "").hudMono(size: 9).foregroundStyle(theme.mutedColor)
            Spacer(minLength: 0)
        }
    }

    private var bandColour: Color {
        switch load.band {
        case .hot: return theme.counterColor
        case .warm: return theme.warnColor
        case .even: return theme.accentColor
        case .cold, .none: return theme.mutedColor
        }
    }
}

// MARK: - the pending AskUserQuestion

/// Option buttons for a SINGLE non-multi-select question, and read-only
/// headers for anything richer.
///
/// That split is not caution, it is the endpoint's contract: `POST
/// /api/question` carries one 1-based digit, which cannot express two
/// questions or a multi-select — so a richer shape renders as headers plus a
/// pointer at the pane rather than as buttons that would silently mis-answer.
///
/// ONE click disables EVERY option (the answer is in flight and a second digit
/// would be a second answer), and only a returned FAILURE re-arms them. A
/// failed answer never reaches the pane, so without the re-arm the question is
/// unanswerable from the board short of a relaunch.
struct QuestionBlock: View {
    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var board: BoardStore

    let slug: String
    let cell: String?
    let question: WebQuestion

    @State private var answered = false

    var body: some View {
        let items = question.questions
        let single = items.count == 1 && !(items.first?.multiSelect ?? true)
        VStack(alignment: .leading, spacing: 6) {
            if single, let first = items.first {
                Text("⚑ " + (first.header.isEmpty ? first.question : first.header))
                    .hudDisplay(size: 10, tracking: HudTracking.stat)
                    .foregroundStyle(theme.warnColor)
                if !first.header.isEmpty && !first.question.isEmpty {
                    Text(first.question).hudMono(size: 10).foregroundStyle(theme.bodyColor)
                }
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(first.options.enumerated()), id: \.offset) { i, option in
                        HudButton("\(i + 1). \(option.label)", style: .warn) { answer(i) }
                            .disabled(answered)
                            .help(option.description)
                    }
                }
            } else {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    Text("⚑ " + (item.header.isEmpty ? item.question : item.header))
                        .hudDisplay(size: 10, tracking: HudTracking.stat)
                        .foregroundStyle(theme.warnColor)
                }
                Text("answer in the pane (k o \(cell ?? ""))")
                    .hudMono(size: 10)
                    .foregroundStyle(theme.mutedColor)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(6)
        .background(theme.warnSoft)
    }

    private func answer(_ optionIndex: Int) {
        answered = true
        Task {
            let err = await board.mutate(
                path: "/api/question",
                body: ["slug": slug, "text": QuestionAnswer.payload(optionIndex: optionIndex)]
            )
            if err != nil { answered = false }
        }
    }
}
