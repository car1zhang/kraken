import SwiftUI

// Controls.swift — the HUD's form-control vocabulary: text fields, multiline
// editors, checkboxes, dropdowns. One definition each, used everywhere a
// system control would otherwise show its Aqua chrome.
//
// Exists because the first build left inputs on the system styles and carl
// called it at the manual gate: "dropdowns, certain buttons, text inputs,
// checkboxes are all too native… should still feel like the web versions."
// The web versions are style.css's one repeated recipe — fill `ac 3% over b1`,
// `1px solid var(--border-strong)`, no radius, and on focus/hover the border
// goes accent with the 8px glow (style.css:306: "there are no focus rings; the
// border is the ring"). Everything here is that recipe.

// MARK: - text field

/// The web text-input recipe on a SwiftUI `TextField`. Carries its own
/// `@FocusState` so the accent-border-plus-glow focus treatment needs nothing
/// from the call site.
struct HudFieldModifier: ViewModifier {
    @Environment(\.hudTheme) private var theme
    @FocusState private var focused: Bool
    var size: CGFloat = 10

    func body(content: Content) -> some View {
        content
            .textFieldStyle(.plain)
            // The system focus ring is the single loudest "Aqua control"
            // giveaway; the web has no ring — "the border is the ring".
            .focusEffectDisabled()
            .hudMono(size: size)
            .foregroundStyle(theme.textColor)
            .focused($focused)
            .padding(.horizontal, 6)
            .padding(.vertical, 4)
            .background(HudTheme.mix(theme.ac, 3, over: theme.b1).color)
            .overlay {
                Rectangle().strokeBorder(
                    focused ? theme.accentColor : theme.borderStrongColor,
                    lineWidth: 1
                )
            }
            .hudGlow(focused ? theme.glowStyle : nil)
            .hudCursor(.text)
    }
}

// MARK: - multiline editor

/// The same recipe on a `TextEditor` (the small prose boxes — feedback edits,
/// watch comments, thread seeds/messages, the artifact composer; the steer
/// composer proper is the NSTextView and styles itself).
struct HudEditorModifier: ViewModifier {
    @Environment(\.hudTheme) private var theme
    @FocusState private var focused: Bool
    var minHeight: CGFloat = 60

    func body(content: Content) -> some View {
        content
            .focusEffectDisabled()
            .hudMono(size: 10)
            .foregroundStyle(theme.textColor)
            .focused($focused)
            .scrollContentBackground(.hidden)
            .padding(.horizontal, 3)
            .padding(.vertical, 2)
            .frame(minHeight: minHeight)
            .background(HudTheme.mix(theme.ac, 3, over: theme.b1).color)
            .overlay {
                Rectangle().strokeBorder(
                    focused ? theme.accentColor : theme.borderStrongColor,
                    lineWidth: 1
                )
            }
            .hudGlow(focused ? theme.glowStyle : nil)
            .hudCursor(.text)
    }
}

extension View {
    func hudField(size: CGFloat = 10) -> some View {
        modifier(HudFieldModifier(size: size))
    }

    func hudEditor(minHeight: CGFloat = 60) -> some View {
        modifier(HudEditorModifier(minHeight: minHeight))
    }
}

// MARK: - checkbox

/// The web checkbox: a square (the kit has no radii), 1px strong border, and
/// the accent as the checked fill — what `accent-color: var(--ac)` gives the
/// browser (style.css:2126), drawn by hand because the Aqua checkbox cannot
/// drop its rounding or its gradient.
struct HudCheckToggleStyle: ToggleStyle {
    @Environment(\.hudTheme) private var theme

    func makeBody(configuration: Configuration) -> some View {
        Button {
            configuration.isOn.toggle()
        } label: {
            HStack(spacing: 5) {
                ZStack {
                    Rectangle()
                        .fill(configuration.isOn ? theme.accentColor : HudTheme.mix(theme.ac, 3, over: theme.b1).color)
                    Rectangle()
                        .strokeBorder(configuration.isOn ? theme.accentColor : theme.borderStrongColor, lineWidth: 1)
                    if configuration.isOn {
                        Text("✓")
                            .font(.system(size: 9, weight: .bold))
                            .foregroundStyle(theme.groundColor)
                    }
                }
                .frame(width: 12, height: 12)
                configuration.label
            }
        }
        .buttonStyle(.plain)
        .hudCursor(.actable)
    }
}

// MARK: - dropdown

/// The web `<select>` look: a bordered mono button showing the value and a
/// `▾`, opening a flat listbox — the SwatchStrip's pattern generalized to
/// plain string options (a native `Picker` shows the Aqua popup chrome, which
/// is exactly what the gate flagged).
struct HudDropdown: View {
    let options: [String]
    /// Shown for and stored as the empty selection, when non-nil ("—" for the
    /// repo field's "none").
    var emptyLabel: String? = nil
    @Binding var selection: String
    var width: CGFloat = 150

    @Environment(\.hudTheme) private var theme
    @Environment(\.isEnabled) private var enabled
    @State private var open = false
    @State private var hovered: String?

    private var displayed: String {
        selection.isEmpty ? (emptyLabel ?? "—") : selection
    }

    var body: some View {
        Button {
            open.toggle()
        } label: {
            HStack(spacing: 4) {
                Text(displayed).lineLimit(1)
                Spacer(minLength: 0)
                Text("▾").opacity(0.7)
            }
            .hudMono(size: 10)
            .foregroundStyle(theme.textColor)
            .padding(.horizontal, 6)
            .padding(.vertical, 4)
            .frame(width: width)
            .background(HudTheme.mix(theme.ac, 3, over: theme.b1).color)
            .overlay {
                Rectangle().strokeBorder(open ? theme.accentColor : theme.borderStrongColor, lineWidth: 1)
            }
        }
        .buttonStyle(.plain)
        .hudCursor(.actable)
        .opacity(enabled ? 1 : 0.4)
        .popover(isPresented: $open, arrowEdge: .bottom) {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(rows, id: \.self) { option in
                    let value = option == emptyLabel ? "" : option
                    Button {
                        selection = value
                        open = false
                    } label: {
                        HStack(spacing: 6) {
                            Text(option).lineLimit(1)
                            Spacer(minLength: 0)
                            if value == selection { Text("·").opacity(0.7) }
                        }
                        .hudMono(size: 10)
                        .foregroundStyle(theme.textColor)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 4)
                        .frame(width: width, alignment: .leading)
                        .background(hovered == option ? HudTheme.mix(theme.ac, 12, over: theme.b1).color : .clear)
                    }
                    .buttonStyle(.plain)
                    .onHover { hovered = $0 ? option : nil }
                }
            }
            .padding(.vertical, 2)
            .background(theme.b1.color)
            .overlay { Rectangle().strokeBorder(theme.borderStrongColor, lineWidth: 1) }
        }
    }

    private var rows: [String] {
        var out: [String] = []
        if let emptyLabel { out.append(emptyLabel) }
        out.append(contentsOf: options)
        // an existing selection outside the option list still round-trips
        // instead of silently vanishing from its own dropdown
        if !selection.isEmpty, !options.contains(selection) { out.append(selection) }
        return out
    }
}
