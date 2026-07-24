import SwiftUI
import UIKit

/// UITextView-backed drop-in replacement for SwiftUI's `TextEditor`, adding
/// inline per-skill coloring of `/info /aim /do /pr` command tokens as you
/// type — something plain `TextEditor` cannot do (no attributed-string
/// support), and the iOS 26 attributed-`TextEditor` API isn't available
/// since this app targets iOS 17. Drops into SteerView, ReactivateView, and
/// JobFormView's spec editor with the same `kField` chrome around it: this
/// view itself stays visually transparent (clear background, no internal
/// insets beyond UITextView's own defaults) so the raised background/border
/// that chrome supplies is unaffected.
///
/// Coloring is additive over a normal, always-visible UITextView: if
/// `commandTokenRanges` or attribute application ever misbehaves, the worst
/// case is plain uncolored text — never invisible text — per the spec's
/// failure-mode requirement.
///
/// Typing re-attributes IN PLACE on the existing `NSTextStorage`
/// (`applyHighlightingInPlace`) rather than reassigning `attributedText`:
/// the latter triggers a full relayout, resets `contentOffset` (the caret
/// scrolls out of view partway through a long spec), and clears the undo
/// stack — on every keystroke. `attributedText` is only reassigned for a
/// true external write (the binding changed out from under the view — a
/// chip append, a draft restore) via `applyExternalChange`, which restores
/// scroll position (clamped to the new contentSize) and clamps the
/// selection back into range (or, for a detected pure-append, moves the
/// caret to the end instead, so the user sees what just landed).
///
/// Invariant: the `text` binding always mirrors `uiView.text`, including
/// provisional composition text (a CJK candidate window, dictation, inline
/// predictive text) — `textViewDidChange` pushes it unconditionally, even
/// mid-composition. This is required so `updateUIView`'s `uiView.text !=
/// text` equality guard never fires while composing (an external re-render
/// — e.g. the steer sheet's ~4s board-poll refresh — landing mid-composition
/// must not send `applyExternalChange` in to reassign `attributedText` and
/// destroy the in-progress composition) and so reads of the binding for
/// Send/draft purposes are never stale. Only ATTRIBUTION is deferred until
/// composition ends: the mid-composition re-attribution call sites
/// (`textViewDidChange`, the trait-change handler) are gated on
/// `markedTextRange == nil`, since recoloring over marked text strips
/// UIKit's marked-text underline — `textViewDidEndEditing`'s call is
/// deliberately ungated because resign IS the commit point;
/// `updateUIView` additionally refuses to run at all while `uiView`
/// currently holds marked text, belt-and-braces against any external write
/// clobbering an active composition.
struct HighlightingTextEditor: UIViewRepresentable {
    @Binding var text: String
    /// Optional focus bridge: SwiftUI's `.focused($state)` never fires on a
    /// UIViewRepresentable, so call sites that need the `kField`
    /// accent-on-focus border pass this binding instead; the coordinator
    /// writes it from `textViewDidBeginEditing`/`textViewDidEndEditing`. Nil
    /// for call sites that don't need focus styling (JobFormView's spec
    /// editor).
    var isFocused: Binding<Bool>?

    func makeUIView(context: Context) -> UITextView {
        let textView = UITextView()
        textView.delegate = context.coordinator
        textView.backgroundColor = .clear
        textView.tintColor = KTheme.accentUI
        textView.isEditable = context.environment.isEnabled
        context.coordinator.applyFreshAttributedText(to: textView, fullText: text)
        context.coordinator.observeTraitChanges(on: textView)
        return textView
    }

    // Only re-attributes wholesale on an external (non-typing) write — a
    // chip append, a draft restore, etc. — since typing already
    // re-attributes itself in place in Coordinator.textViewDidChange.
    // Comparing strings first is what keeps this from looping: without it,
    // the attributedText assignment inside applyExternalChange would itself
    // look like a text change and re-trigger updateUIView indefinitely. The
    // string comparison alone is why this never fires mid-composition either
    // (the binding mirrors marked text too — see the struct doc comment) —
    // the markedTextRange guard below is belt-and-braces on top of that.
    func updateUIView(_ uiView: UITextView, context: Context) {
        uiView.isEditable = context.environment.isEnabled
        // Belt-and-braces: never write over an active composition. The
        // equality guard below should already prevent this (the binding
        // mirrors marked text too, per textViewDidChange), but a composition
        // driven by something other than this view's own typing (e.g. a
        // programmatic marked-text session) must never be clobbered either.
        guard uiView.markedTextRange == nil else { return }
        guard uiView.text != text else { return }
        context.coordinator.applyExternalChange(to: uiView, newText: text)
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(text: $text, isFocused: isFocused)
    }

    final class Coordinator: NSObject, UITextViewDelegate {
        private let text: Binding<String>
        private let isFocused: Binding<Bool>?

        init(text: Binding<String>, isFocused: Binding<Bool>? = nil) {
            self.text = text
            self.isFocused = isFocused
        }

        func textViewDidBeginEditing(_ textView: UITextView) {
            isFocused?.wrappedValue = true
        }

        func textViewDidEndEditing(_ textView: UITextView) {
            isFocused?.wrappedValue = false
            // Covers resign-mid-composition: UIKit commits any marked text
            // on resign (markedTextRange becomes nil) but does not guarantee
            // a textViewDidChange call for that commit. Without this, the
            // binding could stay stale on the pre-composition text and the
            // just-committed characters would go unattributed.
            text.wrappedValue = textView.text
            applyHighlightingInPlace(to: textView)
        }

        func textViewDidChange(_ textView: UITextView) {
            // The binding always mirrors uiView.text, including provisional
            // composition text — pushed unconditionally, even mid-composition
            // (a CJK candidate window, dictation, inline predictive text).
            // This is required so updateUIView's `uiView.text != text`
            // equality guard never fires while composing (which would send
            // applyExternalChange in to reassign attributedText and destroy
            // the in-progress composition) and so Send/draft reads of the
            // binding are never stale mid-composition. Only ATTRIBUTION is
            // deferred until composition ends: re-attributing over marked
            // text would strip UIKit's marked-text underline, so
            // applyHighlightingInPlace is gated on markedTextRange == nil and
            // simply resumes the instant UIKit fires this delegate again
            // with markedTextRange nil.
            text.wrappedValue = textView.text
            guard textView.markedTextRange == nil else { return }
            applyHighlightingInPlace(to: textView)
        }

        /// The base (uncolored) attributes every character starts from:
        /// font + text color. Recomputed fresh each call (cheap — just two
        /// dictionary values) rather than cached, since KTheme's colors are
        /// dynamic UIColors that must be read freshly after a trait flip.
        private var baseAttributes: [NSAttributedString.Key: Any] {
            [.font: UIFont.voice(13), .foregroundColor: KTheme.textUI]
        }

        /// Mutates the existing `NSTextStorage` in place: base attributes
        /// over the full range, then a foreground color overlaid on each
        /// matched command token. Never touches the string itself or
        /// `attributedText`/`selectedRange`, so the caret, scroll position,
        /// and undo stack are all left exactly where they were — this is
        /// what fixes the per-keystroke scroll-jump/undo-loss the naive
        /// "rebuild and reassign attributedText" approach caused, while
        /// still recoloring every keystroke.
        func applyHighlightingInPlace(to textView: UITextView) {
            let fullText = textView.text ?? ""
            let nsLength = (fullText as NSString).length
            let storage = textView.textStorage
            let base = baseAttributes
            storage.beginEditing()
            storage.setAttributes(base, range: NSRange(location: 0, length: nsLength))
            for token in HighlightingTextEditor.commandTokenRanges(in: fullText) {
                guard let color = HighlightingTextEditor.color(forCommand: token.command) else { continue }
                // A stale/out-of-bounds range must degrade to "no color",
                // never crash or corrupt the rest of the text.
                guard token.range.location >= 0, token.range.location + token.range.length <= nsLength else { continue }
                storage.addAttribute(.foregroundColor, value: color, range: token.range)
            }
            storage.endEditing()
            // Reset so a character typed right after a colored token isn't
            // itself colored.
            textView.typingAttributes = base
        }

        /// First paint: nothing to preserve yet, so build and assign a fresh
        /// attributed string directly.
        func applyFreshAttributedText(to textView: UITextView, fullText: String) {
            let base = baseAttributes
            textView.attributedText = HighlightingTextEditor.attributedString(for: fullText, baseAttributes: base)
            textView.typingAttributes = base
        }

        /// A true external write: the bound `text` changed out from under
        /// the view (chip append, draft restore, external undo). Assigning
        /// `attributedText` is unavoidable here since the string itself
        /// changed, so this restores what that assignment would otherwise
        /// clobber: scroll position and the selection, clamped back into
        /// range. Scroll position is clamped against the *new* contentSize
        /// (after forcing layout), not just re-applied verbatim: an external
        /// write to a much shorter string would otherwise leave the view
        /// scrolled past its own content. The one exception is a detected
        /// pure-append (the new text is the old text plus a suffix — a
        /// command chip tap or attachment insert): there the caret jumps to
        /// the end instead, so the user sees what just landed rather than
        /// staying put scrolled out of view.
        func applyExternalChange(to textView: UITextView, newText: String) {
            let oldText = textView.text ?? ""
            let priorSelection = textView.selectedRange
            let priorOffset = textView.contentOffset
            let isAppend = newText.utf16.count > oldText.utf16.count && newText.hasPrefix(oldText)

            let base = baseAttributes
            textView.attributedText = HighlightingTextEditor.attributedString(for: newText, baseAttributes: base)
            textView.typingAttributes = base

            let nsLength = (newText as NSString).length
            if isAppend {
                textView.selectedRange = NSRange(location: nsLength, length: 0)
                // Setting selectedRange alone doesn't scroll, and the
                // attributedText assignment above reset contentOffset —
                // bring the caret (and the appended text) into view.
                textView.scrollRangeToVisible(textView.selectedRange)
            } else {
                let location = min(priorSelection.location, nsLength)
                textView.selectedRange = NSRange(location: location, length: min(priorSelection.length, nsLength - location))
                // An external write to a much shorter string can leave the
                // prior scroll offset past the new (smaller) contentSize —
                // force layout so contentSize reflects newText, then clamp
                // both axes into range before restoring, rather than
                // scrolling the view past its own content.
                textView.layoutIfNeeded()
                let maxX = max(0, textView.contentSize.width - textView.bounds.width)
                let maxY = max(0, textView.contentSize.height - textView.bounds.height)
                let clampedOffset = CGPoint(
                    x: min(max(priorOffset.x, 0), maxX),
                    y: min(max(priorOffset.y, 0), maxY)
                )
                textView.setContentOffset(clampedOffset, animated: false)
            }
        }

        /// KTheme's colors are dynamic UIColors, which UIKit generally
        /// re-resolves on redraw automatically when a view's trait
        /// collection changes. Belt-and-suspenders here since TextKit-drawn
        /// text has historically been less consistent about this than
        /// UILabel: on any `UITraitUserInterfaceStyle` change, explicitly
        /// re-run the in-place attribution (which re-reads KTheme's dynamic
        /// colors fresh) and force a redraw, so a light/dark flip can never
        /// leave stale-colored text on screen.
        func observeTraitChanges(on textView: UITextView) {
            _ = textView.registerForTraitChanges([UITraitUserInterfaceStyle.self]) { [weak self] (view: UITextView, _: UITraitCollection) in
                // Gated on markedTextRange == nil like every other
                // re-attribution site: re-running applyHighlightingInPlace
                // mid-composition would strip UIKit's marked-text underline.
                // A trait flip mid-composition just leaves the old colors
                // until composition ends and textViewDidChange re-attributes.
                if view.markedTextRange == nil {
                    self?.applyHighlightingInPlace(to: view)
                }
                view.setNeedsDisplay()
            }
        }
    }

    // MARK: - attribution helpers

    /// Builds a fresh attributed string over the full text: base attributes
    /// everywhere, then a foreground color overlaid per matched command
    /// token. Shared by the two call sites that must assign `attributedText`
    /// wholesale (first paint, external writes) — everyday typing instead
    /// goes through Coordinator.applyHighlightingInPlace, which never
    /// rebuilds the string.
    private static func attributedString(for fullText: String, baseAttributes: [NSAttributedString.Key: Any]) -> NSAttributedString {
        let attributed = NSMutableAttributedString(string: fullText, attributes: baseAttributes)
        let nsLength = (fullText as NSString).length
        for token in commandTokenRanges(in: fullText) {
            guard let color = color(forCommand: token.command) else { continue }
            guard token.range.location >= 0, token.range.location + token.range.length <= nsLength else { continue }
            attributed.addAttribute(.foregroundColor, value: color, range: token.range)
        }
        return attributed
    }

    // MARK: - token model (unit-tested in KrakenTests)

    /// One matched command token: its UTF-16 range in the source text (as
    /// returned by `commandTokenRanges`) and which command matched. A named
    /// struct rather than a `(range:, command:)` tuple, per repo convention
    /// against tuples for structured data.
    struct CommandToken: Equatable {
        let range: NSRange
        let command: String
    }

    /// Finds every `/info /aim /do /pr` command token in `text`, anchored
    /// the same way as `CmdChipsRow.isPresent` / web's `CMD_HL_RE`: a
    /// leading `(^|\s)` boundary (consumed by the match but NOT part of the
    /// returned range) and a trailing `\b` (so "/doc" doesn't match "/do",
    /// and "web/static/do" doesn't match either, since nothing there is
    /// whitespace-preceded). Ranges are UTF-16 (`NSRange`), matching
    /// `UITextView`/`NSAttributedString`'s native indexing — this is safe
    /// over multibyte characters preceding a token because both the regex
    /// engine and the returned ranges operate on the same `NSString`/UTF-16
    /// view of the text throughout, never on Swift's grapheme-cluster or
    /// UTF-8 offsets.
    static func commandTokenRanges(in text: String) -> [CommandToken] {
        guard !text.isEmpty, let regex = tokenRegex else { return [] }
        let nsText = text as NSString
        let fullRange = NSRange(location: 0, length: nsText.length)
        var results: [CommandToken] = []
        regex.enumerateMatches(in: text, range: fullRange) { match, _, _ in
            guard let match, match.numberOfRanges > 1 else { return }
            let commandRange = match.range(at: 1)
            guard commandRange.location != NSNotFound else { return }
            results.append(CommandToken(range: commandRange, command: nsText.substring(with: commandRange)))
        }
        return results
    }

    /// Single source for skill → color: delegates to CmdChipsRow.styleUI,
    /// the one switch statement both the chip row and this highlighter draw
    /// from (see that function's doc comment).
    private static func color(forCommand command: String) -> UIColor? {
        CmdChipsRow.styleUI(for: command)?.color
    }

    // Built once from CmdChipsRow.commands — the single source of which
    // commands are recognized — rather than a second hand-duplicated list.
    private static let tokenRegex: NSRegularExpression? = {
        let names = CmdChipsRow.commands.map { NSRegularExpression.escapedPattern(for: String($0.dropFirst())) }
        let pattern = "(?:^|\\s)(/(?:\(names.joined(separator: "|"))))\\b"
        return try? NSRegularExpression(pattern: pattern)
    }()
}
