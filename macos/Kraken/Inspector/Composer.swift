import AppKit
import SwiftUI
import UniformTypeIdentifiers

// Composer.swift — the steer/editor text box.
//
// This is the ONE AppKit-backed editor in the app, and 53-concessions A13 is
// where the choice is argued: the web's mirrored-div hack exists because a
// `<textarea>` cannot style substrings of its own value, and an `NSTextView`
// styles ranges directly. SwiftUI's attributed `TextEditor` would be the
// native answer and is macOS-26-only, so `NSViewRepresentable` it is — which
// also buys tinted selection and the composer's own find bar.
//
// ONE deliberate departure from the mirrored backdrop, and it is a
// SIMPLIFICATION rather than a loss: the whole class of bug that file carries
// (a trailing "\n" sentinel, gutter compensation, synchronous uncoalesced
// scroll sync, a `.cmd-hl-on` gate so a failed paint leaves readable text)
// exists only because two surfaces have to agree on every metric. There is one
// surface here.
//
// The two-layer neon glow is where the translation is inexact and it is worth
// stating: an `NSAttributedString` carries exactly ONE `.shadow` attribute per
// range, while `--text-glow-skill-*` is a comma pair (a 1px core under a 9px
// halo, both at full alpha). Stacking two would need a second mirrored text
// view drawn behind the first — precisely the mechanism this file exists to
// avoid — so the wide halo is applied as the shadow and the tight core is
// carried by the glyph's own saturated skill colour, which is what makes an
// inline `/do` read as neon rather than as coloured text. Recorded rather than
// hidden.

// MARK: - caret insertion

/// The pure half of `insertIntoTextarea` (useCmdHighlight.ts:168).
///
/// The GAP rule is the whole content: without `inlineGap`, spliced text gets
/// its own leading newline so it cannot glue onto existing prose — that is the
/// image-reference case, where `[image: …]` belongs on its own line. With it,
/// a single space closes the gap instead, which is the command-chip case.
enum ComposerInsert {
    struct Result: Equatable {
        let text: String
        /// Where the caret belongs afterwards, as a UTF-16 offset (what
        /// `NSTextView.selectedRange` speaks).
        let caret: Int
    }

    static func insert(
        into current: String,
        start: Int,
        end: Int,
        snippet: String,
        inlineGap: Bool
    ) -> Result {
        let ns = current as NSString
        let lo = max(0, min(start, ns.length))
        let hi = max(lo, min(end, ns.length))

        var gap = ""
        if lo > 0 {
            let prev = ns.substring(with: NSRange(location: lo - 1, length: 1))
            let prevChar = prev.first
            if inlineGap {
                if let prevChar, !prevChar.isWhitespace { gap = " " }
            } else if prev != "\n" {
                gap = "\n"
            }
        }
        let inserted = gap + snippet
        let out = ns.replacingCharacters(in: NSRange(location: lo, length: hi - lo), with: inserted)
        return Result(text: out, caret: lo + (inserted as NSString).length)
    }
}

/// The handle a parent view keeps so it can splice text in from OUTSIDE a
/// keystroke — the command chips, and the editor's image upload landing
/// `[image: <path>]` when it resolves.
///
/// It holds the live text view weakly rather than mirroring its state: the
/// caret is the thing being read, and a mirrored caret is a caret that can be
/// wrong.
@MainActor
final class ComposerHandle: ObservableObject {
    weak var textView: NSTextView?

    var isFocused: Bool {
        guard let tv = textView else { return false }
        return tv.window?.firstResponder === tv
    }

    /// Splices at the caret and answers the new whole string. The caret is
    /// restored by the representable's next update — doing it here would fight
    /// the SwiftUI binding write that has not landed yet.
    ///
    /// `ownLineWhenUnfocused` treats an UNFOCUSED text view as stale cursor
    /// state and appends at the end instead: an upload can resolve long after
    /// the user has moved on, and splicing into a selection nobody is looking
    /// at drops text in a random place.
    func insert(
        _ snippet: String,
        into current: String,
        inlineGap: Bool = false,
        ownLineWhenUnfocused: Bool = false
    ) -> String {
        let useEnd = ownLineWhenUnfocused && !isFocused
        let length = (current as NSString).length
        let sel = textView?.selectedRange() ?? NSRange(location: length, length: 0)
        let start = useEnd ? length : sel.location
        let end = useEnd ? length : sel.location + sel.length
        let r = ComposerInsert.insert(
            into: current, start: start, end: end, snippet: snippet, inlineGap: inlineGap
        )
        pendingCaret = r.caret
        return r.text
    }

    /// Where the caret should land once the binding write lands. Consumed (and
    /// cleared) by the representable.
    var pendingCaret: Int?

    func focus() {
        guard let tv = textView else { return }
        tv.window?.makeFirstResponder(tv)
    }
}

// MARK: - the text view

/// An `NSTextView` that answers ⌘⏎ itself.
///
/// `performKeyEquivalent` rather than the delegate's `doCommandBy`: with
/// Command held, AppKit routes the press through the key-equivalent chain and
/// a plain text view never sees an `insertNewline:` to intercept.
final class HudComposerTextView: NSTextView {
    var onSubmit: (() -> Void)?

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if event.modifierFlags.contains(.command), event.keyCode == 36 {  // Return
            onSubmit?()
            return true
        }
        return super.performKeyEquivalent(with: event)
    }
}

/// The composer's editable surface: per-range skill colouring, neon glow,
/// theme-coloured caret and selection, and every one of AppKit's helpful text
/// substitutions turned OFF.
///
/// Those substitutions are not cosmetic here. This box types SHELL and
/// MARKDOWN into an agent's prompt: smart quotes turn `"foo"` into `“foo”`,
/// dash substitution turns `--flag` into `–flag`, and text replacement will
/// happily rewrite a word mid-command. Every one of them produces a prompt
/// that reads correctly and runs wrong.
struct ComposerTextView: NSViewRepresentable {
    @Binding var text: String
    let theme: HudTheme
    var isEditable: Bool = true
    var minHeight: CGFloat = 76
    var onSubmit: () -> Void = {}
    var handle: ComposerHandle?

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        guard let tv = scroll.documentView as? NSTextView else { return scroll }

        // scrollableTextView() hands back a plain NSTextView; the ⌘⏎ subclass
        // has to replace it, which means rebuilding the scroll view's document
        // rather than mutating a class in place.
        let custom = HudComposerTextView(frame: tv.frame, textContainer: tv.textContainer)
        custom.autoresizingMask = tv.autoresizingMask
        custom.minSize = tv.minSize
        custom.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: .greatestFiniteMagnitude)
        custom.isVerticallyResizable = true
        custom.isHorizontallyResizable = false
        custom.textContainer?.widthTracksTextView = true
        scroll.documentView = custom

        custom.delegate = context.coordinator
        custom.onSubmit = onSubmit
        custom.drawsBackground = false
        custom.allowsUndo = true
        custom.usesFindBar = true
        custom.isIncrementalSearchingEnabled = true

        // every "helpful" rewrite, off — see the type comment
        custom.isAutomaticQuoteSubstitutionEnabled = false
        custom.isAutomaticDashSubstitutionEnabled = false
        custom.isAutomaticTextReplacementEnabled = false
        custom.isAutomaticSpellingCorrectionEnabled = false
        custom.isAutomaticDataDetectionEnabled = false
        custom.isAutomaticLinkDetectionEnabled = false
        custom.isContinuousSpellCheckingEnabled = false
        custom.isGrammarCheckingEnabled = false
        custom.smartInsertDeleteEnabled = false

        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.scrollerKnobStyle = theme.mode == .dark ? .light : .dark

        context.coordinator.textView = custom
        handle?.textView = custom
        custom.string = text
        context.coordinator.apply(theme: theme, to: custom)
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let tv = scroll.documentView as? HudComposerTextView else { return }
        context.coordinator.parent = self
        tv.onSubmit = onSubmit
        tv.isEditable = isEditable
        tv.isSelectable = true
        handle?.textView = tv

        if tv.string != text {
            // Never clobber a live IME composition: replacing the string mid
            // composition drops the candidate and the marked text with it.
            if !tv.hasMarkedText() {
                let caret = handle?.pendingCaret
                tv.string = text
                if let caret {
                    let len = (text as NSString).length
                    tv.setSelectedRange(NSRange(location: min(caret, len), length: 0))
                    handle?.pendingCaret = nil
                }
            }
        }
        context.coordinator.apply(theme: theme, to: tv)
        scroll.scrollerKnobStyle = theme.mode == .dark ? .light : .dark
    }

    final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: ComposerTextView
        weak var textView: NSTextView?

        init(_ parent: ComposerTextView) { self.parent = parent }

        func textDidChange(_ notification: Notification) {
            guard let tv = notification.object as? NSTextView else { return }
            // A marked (mid-IME) run is not committed text — writing it back
            // through the binding would re-enter updateNSView and reset the
            // composition on every keystroke.
            guard !tv.hasMarkedText() else { return }
            parent.text = tv.string
            apply(theme: parent.theme, to: tv)
        }

        /// Repaints the skill spans. Runs on every change and on every theme
        /// swap; cheap because it walks the string once and touches only the
        /// ranges `CmdHighlighter` found.
        func apply(theme: HudTheme, to tv: NSTextView) {
            guard let storage = tv.textStorage else { return }
            let text = tv.string
            let full = NSRange(location: 0, length: (text as NSString).length)

            tv.insertionPointColor = theme.ac.nsColor
            tv.selectedTextAttributes = [
                .backgroundColor: theme.ac.nsColor(0.25),
                .foregroundColor: theme.fg.nsColor,
            ]

            let base = NSFont(name: HudFont.resolvedMonoRegular ?? "", size: 11)
                ?? NSFont.monospacedSystemFont(ofSize: 11, weight: .regular)

            storage.beginEditing()
            storage.setAttributes(
                [.font: base, .foregroundColor: theme.body.nsColor], range: full
            )
            for hit in CmdHighlighter.ranges(in: text) {
                guard let ns = NSRange(hit.range, in: text) as NSRange?,
                      let colour = SkillPalette.color(hit.skill, theme.mode)
                else { continue }
                var attrs: [NSAttributedString.Key: Any] = [.foregroundColor: colour.nsColor]
                // the wide halo of the two-layer pair; the tight core is the
                // glyph's own colour — see the file header
                if let glow = theme.skillTextGlow(hit.skill).last {
                    let shadow = NSShadow()
                    shadow.shadowColor = NSColor(glow.color)
                    shadow.shadowBlurRadius = glow.radius
                    shadow.shadowOffset = .zero
                    attrs[.shadow] = shadow
                }
                storage.addAttributes(attrs, range: ns)
            }
            storage.endEditing()
        }
    }
}

// MARK: - the chip row

/// `/info /aim /do /pr` above the box (`CmdComposer.tsx`).
///
/// A chip inserts at the CARET with an inline-gap space and never takes focus
/// — the caret and the selection stay exactly where they were, which is what
/// makes a chip usable mid-sentence. `.active` is boundary-anchored presence,
/// the same rule the highlighter uses, so a chip lights up for the command it
/// would insert and not for `web/static/do`.
struct CommandChipRow: View {
    @Environment(\.hudTheme) private var theme
    @Binding var text: String
    let handle: ComposerHandle

    var body: some View {
        HStack(spacing: 6) {
            ForEach(CmdHighlighter.chips, id: \.self) { cmd in
                chip(cmd)
            }
            Spacer(minLength: 0)
        }
    }

    private func isActive(_ cmd: String) -> Bool {
        let skill = String(cmd.dropFirst())
        return CmdHighlighter.ranges(in: text).contains { $0.skill == skill }
    }

    @ViewBuilder
    private func chip(_ cmd: String) -> some View {
        let skill = String(cmd.dropFirst())
        let active = isActive(cmd)
        let colour = SkillPalette.color(skill, theme.mode)?.color ?? theme.mutedColor
        let fill = SkillPalette.soft(skill, theme.mode)?.color ?? theme.chipBgColor
        Button {
            text = handle.insert(cmd + " ", into: text, inlineGap: true)
            handle.focus()
        } label: {
            Text(cmd)
                .hudMono(size: 9, weight: .medium, tracking: HudTracking.stat)
                .foregroundStyle(active ? colour : theme.mutedColor)
                .padding(.horizontal, 7)
                .padding(.vertical, 3)
                .background(active ? fill : Color.clear)
                .overlay {
                    Rectangle()
                        .strokeBorder(active ? colour.opacity(0.55) : theme.chipBorder, lineWidth: 1)
                }
        }
        .buttonStyle(.plain)
        .help(cmd)
    }
}

// MARK: - attachments

/// One uploaded image, as `POST /api/upload` returns it.
struct AttachmentChip: Identifiable, Equatable {
    let name: String
    /// The absolute server-side path — the value that goes into the message as
    /// `[image: <path>]`.
    let path: String
    /// Server-relative url, for the thumbnail.
    let url: String

    var id: String { path }
}

/// The upload flow shared by both composers, and the ONE thing they mean
/// differently by it.
///
/// In the STEER composer the chips ARE the message: at send time they compose
/// to `[image: <path>]` lines appended after the typed text, so removing a
/// chip removes it from what gets sent. In the EDITOR the reference is spliced
/// into the spec text IMMEDIATELY and the chip is only visual confirmation —
/// removing it does NOT remove the text, because that text is now
/// user-editable prose in the spec.
///
/// This object owns only the half both share: the pending counter (which
/// blocks send), the chip list, and the note a failure writes.
@MainActor
final class AttachmentUploader: ObservableObject {
    @Published private(set) var chips: [AttachmentChip] = []
    /// Uploads in flight. Non-zero blocks a send — the message would go
    /// without its images otherwise.
    @Published private(set) var pending = 0
    @Published var note = ""

    private let slug: String
    /// Called with each successful upload, for the editor's immediate splice.
    private let onUploaded: ((AttachmentChip) -> Void)?

    init(slug: String, onUploaded: ((AttachmentChip) -> Void)? = nil) {
        self.slug = slug
        self.onUploaded = onUploaded
    }

    func remove(_ chip: AttachmentChip) {
        chips.removeAll { $0.id == chip.id }
    }

    /// `[image: <path>]` for every chip, one per line — steer's send-time
    /// composition.
    var imageLines: String {
        chips.map { "[image: \($0.path)]" }.joined(separator: "\n")
    }

    /// Accepts a batch from any of the three sources (the 📎 picker, ⌘V, a
    /// panel drop). A batch that carried files but no IMAGE earns a note
    /// rather than silence — that distinction is why `HudPastedImage` filters
    /// on the way in and the caller reports the count it started with.
    func add(_ images: [HudPastedImage], hadFiles: Bool = true) {
        if images.isEmpty {
            if hadFiles { note = "images only (png/jpeg/gif/webp)" }
            return
        }
        for image in images {
            pending += 1
            Task { [weak self] in
                guard let self else { return }
                defer { self.pending -= 1 }
                do {
                    let resp = try await APIClient.shared.uploadImage(
                        baseURL: InspectorAPI.baseURL,
                        slug: self.slug,
                        data: image.data,
                        filename: image.filename,
                        mimeType: image.mimeType
                    )
                    let chip = AttachmentChip(name: resp.name, path: resp.path, url: resp.url)
                    self.chips.append(chip)
                    self.note = ""
                    self.onUploaded?(chip)
                } catch {
                    self.note = "upload failed: " + error.localizedDescription
                }
            }
        }
    }

    /// The file picker. `allowsMultipleSelection`, and the four types only —
    /// the same set the endpoint accepts, refused in the panel rather than by
    /// the server.
    func pickFiles() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = true
        panel.canChooseDirectories = false
        panel.allowedContentTypes = [.png, .jpeg, .gif, .webP]
        guard panel.runModal() == .OK else { return }
        add(panel.urls.compactMap(HudPastedImage.load(url:)), hadFiles: !panel.urls.isEmpty)
    }
}

/// The attachment strip: thumbnails with a ✕ each, the 📎 button, and the
/// hint. The DROP target is the whole panel rather than this zone (the web
/// binds dragover/drop to `.panel`, head included, so a drop lands wherever
/// you aim it) — see `hudPanelFileDrop`.
struct AttachmentBar: View {
    @Environment(\.hudTheme) private var theme
    @ObservedObject var uploader: AttachmentUploader

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !uploader.chips.isEmpty {
                HStack(spacing: 6) {
                    ForEach(uploader.chips) { chip in
                        HStack(spacing: 4) {
                            Text("▤").hudMono(size: 9)
                            Text(chip.name).hudMono(size: 9).lineLimit(1)
                            Button {
                                uploader.remove(chip)
                            } label: {
                                Text("✕").hudMono(size: 9)
                            }
                            .buttonStyle(.plain)
                        }
                        .foregroundStyle(theme.bodyColor)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 3)
                        .background(theme.chipBgColor)
                        .overlay { Rectangle().strokeBorder(theme.chipBorder, lineWidth: 1) }
                    }
                    Spacer(minLength: 0)
                }
            }
            HStack(spacing: 8) {
                HudButton("📎 attach", style: .ghost) { uploader.pickFiles() }
                Text("paste (⌘V) or drop images")
                    .hudMono(size: 9)
                    .foregroundStyle(theme.mutedColor)
                Spacer(minLength: 0)
            }
        }
    }
}

extension View {
    /// A Files drop anywhere on the panel. `sink` nil refuses the drop
    /// outright, which is how the editor stays inert while read-only.
    func hudPanelFileDrop(_ sink: (([HudPastedImage]) -> Void)?) -> some View {
        onDrop(of: [.fileURL], isTargeted: nil) { providers in
            guard let sink else { return false }
            Task { @MainActor in
                var out: [HudPastedImage] = []
                for provider in providers {
                    guard let item = try? await provider.loadItem(
                        forTypeIdentifier: UTType.fileURL.identifier
                    ) else { continue }
                    let url: URL?
                    if let data = item as? Data {
                        url = URL(dataRepresentation: data, relativeTo: nil)
                    } else {
                        url = item as? URL
                    }
                    if let url, let img = HudPastedImage.load(url: url) { out.append(img) }
                }
                sink(out)
            }
            return true
        }
    }
}

// MARK: - colour bridge

extension RGB {
    /// The AppKit colour, in sRGB explicitly for the reason `RGB.color` is:
    /// every mix in this app is sRGB arithmetic, and resolving through another
    /// working space would undo the parity the theme engine exists to hold.
    var nsColor: NSColor {
        NSColor(srgbRed: CGFloat(r) / 255, green: CGFloat(g) / 255, blue: CGFloat(b) / 255, alpha: 1)
    }

    func nsColor(_ alpha: Double) -> NSColor {
        NSColor(
            srgbRed: CGFloat(r) / 255, green: CGFloat(g) / 255, blue: CGFloat(b) / 255,
            alpha: CGFloat(alpha)
        )
    }
}
