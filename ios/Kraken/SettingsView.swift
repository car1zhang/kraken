import SwiftUI

// Deliberately not a Form: SwiftUI's grouped Form/List chrome fought this
// screen worse than anywhere else in the app — carl: "works but ui looks
// broken." A plain ScrollView + VStack of ThemedCards gives full control
// over spacing/surfaces with none of that baggage.
struct SettingsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @Environment(AppRouter.self) private var router
    // The board's model, owned by KrakenApp. Settings reads it for exactly one
    // thing: the ship + update controls, which used to live in the board's
    // daemon bar. Sharing the instance rather than making a second one is what
    // keeps a run started here visible in the board's ship banner, and vice
    // versa. See KrakenApp.boardVM.
    @Environment(BoardViewModel.self) private var boardVM
    @AppStorage("notifyBlocked") private var notifyBlocked = false
    @AppStorage("appearance") private var appearance = "system"
    /// The theme tint, persisted under the one key KTheme owns. Writing a
    /// rawValue here is the whole picker: KrakenApp observes the same key and
    /// re-identifies the tree, so the app re-tints live.
    @AppStorage(KTheme.themeTintKey) private var themeTintRaw = ThemeTint.violet.rawValue
    @FocusState private var serverURLFocused: Bool
    @State private var testResult: String?
    @State private var testIsError = false
    @State private var isTesting = false
    @State private var notificationManager = NotificationManager.shared
    // The LOG drawer, opened by the row below or by kraken://log. The log was
    // a tab until the nav restructure; it is a thing you go and look at when
    // something is off, which is a drawer, not one of four homes.
    @State private var showingLog = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Appearance").kLabelStyle()

                            Picker("Appearance", selection: $appearance) {
                                Text("System").tag("system")
                                Text("Light").tag("light")
                                Text("Dark").tag("dark")
                            }
                            .pickerStyle(.segmented)
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Theme color").kLabelStyle()
                            themeTintStrip
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Server").kLabelStyle()

                            TextField("http://100.90.94.3:7777", text: $serverURL)
                                .keyboardType(.URL)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .focused($serverURLFocused)
                                .font(.data(13))
                                .kField(focused: serverURLFocused)

                            Button {
                                testConnection()
                            } label: {
                                if isTesting {
                                    ProgressView()
                                        .frame(maxWidth: .infinity)
                                } else {
                                    // full-width labeled action, not a circular
                                    // icon button — keeps its text (carl's
                                    // icon-only rule was for the compact pills)
                                    Label("Test Connection", systemImage: "network")
                                        .frame(maxWidth: .infinity)
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .disabled(isTesting || serverURL.isEmpty)

                            if let testResult {
                                Text(testResult)
                                    .font(.voice(12))
                                    .foregroundStyle(testIsError ? KTheme.counter : KTheme.solved)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Notifications").kLabelStyle()

                            Toggle(isOn: $notifyBlocked) {
                                Text("Notify when a job blocks")
                                    .font(.voice(13))
                                    .foregroundStyle(KTheme.text)
                            }
                            .onChange(of: notifyBlocked) {
                                if notifyBlocked {
                                    notificationManager.requestAuthorizationIfNeeded()
                                } else {
                                    // Sets frozen while the toggle is off go stale —
                                    // clear them so re-enabling seeds fresh instead
                                    // of bursting a week of missed transitions.
                                    notificationManager.resetConditionTracking()
                                }
                            }

                            if notifyBlocked && notificationManager.authorizationDenied {
                                Text("Notifications are denied in iOS Settings — enable them there to get alerts.")
                                    .font(.voice(11))
                                    .foregroundStyle(KTheme.revealed)
                            }
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    MacControlsSection(vm: boardVM, serverURL: serverURL)

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Diagnostics").kLabelStyle()

                            Button {
                                showingLog = true
                            } label: {
                                HStack(spacing: 8) {
                                    Label("Log", systemImage: "list.bullet.rectangle")
                                        .font(.voice(13))
                                        .foregroundStyle(KTheme.text)
                                    Spacer(minLength: 6)
                                    Image(systemName: "chevron.right")
                                        .font(.voice(11))
                                        .foregroundStyle(KTheme.text2)
                                }
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background { ThemeBackgroundView() }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.large)
            .task {
                if notifyBlocked {
                    notificationManager.refreshAuthorizationStatus()
                }
            }
            // LogView carries its own NavigationStack and close button, so it
            // is presented bare rather than wrapped.
            .sheet(isPresented: $showingLog) { LogView() }
            // kraken://log stamps pendingLog and selects this tab; this view
            // owns the drawer, so it is the one that consumes and clears the
            // flag. onAppear covers a route stamped before this tab mounted
            // (cold launch via -KrakenRoute), onChange one arriving after.
            .onAppear { consumePendingLog() }
            .onChange(of: router.pendingLog) { consumePendingLog() }
            // The ship and update polls, mirroring BoardView's. They live in
            // BOTH places on purpose: a `.task` is cancelled when its view
            // leaves the screen, and switching to this tab takes the board's
            // copies down with it — so without these, tapping Update here
            // would start a run nothing was watching. Only one copy is ever
            // awake (the other tab is off screen), and BoardViewModel's poll
            // epochs already arbitrate the hand-over frame. See
            // BoardViewModel.updatePollEpoch.
            .task(id: UpdatePollKey(generation: boardVM.updatePollGeneration, phase: scenePhase)) {
                guard scenePhase == .active else { return }
                await boardVM.resumeUpdatePoll(baseURL: serverURL)
            }
            .task(id: ShipPollKey(generation: boardVM.shipPollGeneration, phase: scenePhase)) {
                guard scenePhase == .active else { return }
                await boardVM.resumeShipPoll(baseURL: serverURL)
            }
        }
    }

    private func consumePendingLog() {
        guard router.pendingLog else { return }
        showingLog = true
        router.pendingLog = false
    }

    /// The eight curated tints, in `ThemeTint` order, same interaction shape
    /// as JobColorStrip: one tap = one swatch lit, persisted immediately. No
    /// free ColorPicker — the eight are curated so every one of them is
    /// legible against the glass in both appearances.
    ///
    /// The lit chip is derived from the STORED value through
    /// `ThemeTint.resolve`, so a defaults entry this build does not recognise
    /// renders as violet rather than as nothing selected.
    private var themeTintStrip: some View {
        let selected = ThemeTint.resolve(themeTintRaw)
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(ThemeTint.allCases, id: \.self) { tint in
                    Button {
                        themeTintRaw = tint.rawValue
                    } label: {
                        Circle()
                            .fill(tint.swatch)
                            .frame(width: 26, height: 26)
                            .overlay(
                                Circle()
                                    .stroke(selected == tint ? KTheme.text : Color.clear, lineWidth: 2)
                                    .padding(-3)
                            )
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(tint.displayName)
                    .accessibilityAddTraits(selected == tint ? [.isButton, .isSelected] : [.isButton])
                }
            }
            // room for the 2pt selection ring, drawn outside each swatch's
            // own frame
            .padding(5)
        }
    }

    private func testConnection() {
        isTesting = true
        testResult = nil
        let url = serverURL
        Task {
            do {
                let health = try await APIClient.shared.fetchHealth(baseURL: url)
                isTesting = false
                testIsError = !health.ok
                testResult = health.ok ? "connected — \(health.host)" : "server responded but reported not ok"
            } catch {
                isTesting = false
                testIsError = true
                testResult = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// The "Mac" section: the ⇧ ship-to-TestFlight and ⟳ self-update controls.
///
/// This file owns their LAYOUT and nothing else. Every rule they obey — the
/// phase precedence, the freshness windows, which taps are refused, which
/// glyph and label go with which state, why the update control reveals an
/// error instead of re-running — is documented once, on the state machines
/// themselves: `BoardViewModel.updateButton` and `BoardViewModel.shipButton`
/// in BoardView.swift. Read those; do not restate them here, because two
/// copies of a precedence table drift and the one over here has no tests.
///
/// They lived in the board's daemon bar until the nav restructure, and nothing
/// about their behaviour changed in the move — only where they sit. They are
/// Mac-side maintenance, and the daemon bar is a status readout carl glances
/// at five columns' worth of times a day.
///
/// The ship run's BANNER stayed on the board, where a run in flight is news
/// whether or not anyone is in Settings; `messageArea` is this screen's own
/// copy of what the controls have to say, because those messages otherwise
/// land on a tab the user tapping these controls is not looking at (see
/// `messageArea`'s doc for why expiry is NOT the reason). The model is shared
/// (KrakenApp owns it), so a run started here paints in both places.
private struct MacControlsSection: View {
    let vm: BoardViewModel
    let serverURL: String

    var body: some View {
        ThemedCard {
            VStack(alignment: .leading, spacing: 10) {
                Text("Mac").kLabelStyle()
                // Side by side as real buttons — carl's call, replacing the
                // two full-width text rows that read as labels rather than
                // things you can press.
                HStack(spacing: 10) {
                    shipControl
                    updateControl
                }
                messageArea
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    /// Everything these two controls have to SAY, and nothing they don't.
    ///
    /// Without it the section was three silent failures: a start that 409s
    /// writes `updateMessage`/`shipMessage`, a ship failure writes the record's
    /// error, and the update control's "✕ update failed" tap does nothing BUT
    /// set `updateMessage`. All three were rendered only by the board's daemon
    /// bar and ship banner — a tab away, on a screen carl is by definition not
    /// looking at, since he is here having just tapped the button. That is the
    /// reason, not expiry: the windows are 120s (ship) and 60s (update), long
    /// enough to walk over, and a 409 refusal has no window at all. A message nobody is
    /// in front of is a message that didn't happen.
    ///
    /// What it deliberately does NOT read is `vm.barMessage`. That property is
    /// the daemon bar's slot and its precedence puts `errorMessage` — the
    /// BOARD poll's last failure — ahead of `updateMessage`. Over there that is
    /// right; here it would pin a stale "Could not connect to the server." from
    /// a poll that has since been cancelled by a tab switch, on top of and
    /// hiding the refusal this area exists to show.
    ///
    /// Two lines, ship above update, matching the order of the controls above
    /// them rather than the bar's precedence — these are two independent runs,
    /// not one slot two things compete for, so both show at once when both have
    /// something to say. Unabridged: a settings card has the room the bar's
    /// one-line slot never had, so nothing here is truncated.
    @ViewBuilder
    private var messageArea: some View {
        // `shipBanner` is already exactly the ship half — refusal, live phase,
        // or a FRESH record's error in full — and it never consults
        // errorMessage, so it is reused verbatim.
        if let banner = vm.shipBanner {
            Text(banner.text)
                .font(.voice(12))
                .foregroundStyle(banner.color)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        // The update half, assembled from its own two sinks: whatever the
        // control last had to say (a refusal, or an error carl revealed by
        // tapping), else the error a fresh failed record is carrying — which is
        // `revealsError`, non-nil exactly when the record is both failed and
        // fresh. Printing it unprompted is the small divergence from the bar,
        // and it is deliberate: the bar hides it because it has one line to
        // spend, and this card does not.
        if let message = vm.updateMessage ?? vm.updateButton.revealsError, !message.isEmpty {
            Text(message)
                .font(.voice(12))
                .foregroundStyle(KTheme.counter)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    /// The TestFlight ship control (POST /api/ship-ios + the status poll that
    /// follows the run — see BoardViewModel's ship section).
    ///
    /// The accessibility label carries the distinction the two-word visible
    /// label can't: "uploaded" is where this run ends, and installing the build
    /// on this phone is a separate manual step in the TestFlight app afterwards.
    private var shipControl: some View {
        let state = vm.shipButton
        return Button {
            // One action, always: start a ship. A fresh failure re-runs rather
            // than "revealing" itself — `messageArea` right below is already
            // printing the error in full (see BoardViewModel.shipButton).
            //
            // Synchronously, in the tap handler — a Task body doesn't start
            // soon enough to keep a second tap out (see shipStarting).
            vm.markShipStarting()
            Task { await vm.startShipIOS(baseURL: serverURL) }
        } label: {
            controlLabel(glyph: state.glyph, label: state.label)
        }
        .buttonStyle(.glass)
        .tint(state.color)
        .disabled(state.disabled)
        .accessibilityLabel("ship the iOS app to TestFlight — \(state.label). Uploading is not installing: TestFlight processes the build afterwards and you install it from the TestFlight app.")
    }

    /// The self-update control (POST /api/update + the status poll that follows
    /// the run through the server restart — see BoardViewModel's self-update
    /// section). A recorded failure reveals itself on tap rather than
    /// re-running: the revealed text lands in `messageArea` below, which is
    /// what makes that tap mean something on this screen.
    private var updateControl: some View {
        let state = vm.updateButton
        return Button {
            if let err = state.revealsError {
                vm.revealUpdateError(err)
            } else {
                // Synchronously, in the tap handler — a Task body doesn't
                // start soon enough to keep a second tap out (see
                // BoardViewModel.updateStarting).
                vm.markUpdateStarting()
                Task { await vm.startUpdate(baseURL: serverURL) }
            }
        } label: {
            controlLabel(glyph: state.glyph, label: state.label)
        }
        .buttonStyle(.glass)
        .tint(state.color)
        .disabled(state.disabled)
        .accessibilityLabel("update kraken — \(state.label)")
    }

    /// One control's label, for a glass button. The state's color rides the
    /// button's `.tint` rather than the text, so the glass capsule carries
    /// the state the way every other tinted control in the app does; the two
    /// buttons split the row's width equally.
    private func controlLabel(glyph: String, label: String) -> some View {
        HStack(spacing: 6) {
            Text(glyph)
            Text(label)
        }
        .font(.data(13, weight: .medium))
        .frame(maxWidth: .infinity)
        .padding(.vertical, 6)
    }
}
