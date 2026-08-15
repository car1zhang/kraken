import SwiftUI
import Observation

// The KRONOS tab: the orchestrator's conversation with a context meter above
// it. The conversation surface IS SessionDetailView — kronos is an ordinary
// discovered peer and its reads/sends go through the same /api/peers routes
// every session uses — so this file owns only what is kronos-specific: the
// /api/kronos status poll, the meter, and the non-conversation states
// (feature off, respawn pending, pane not yet discovered, board
// unreachable, version skew, first load).

// MARK: - Meter (the pure half, mirroring web/_ui/src/lib/kronos.ts)

/// The three warning thresholds, in the order the meter draws its ticks.
/// They mirror the daemon-side ctxPass thresholds (50/75/90) — the same
/// crossings that fire carl a macOS notification — same as the web meter's
/// KRONOS_THRESHOLDS.
let kronosThresholds: [Double] = [50, 75, 90]

/// ok below 50, then one band per threshold crossed. Same four bands, same
/// names, as the web lib's KronosBand.
enum KronosBand: String, Equatable {
    case ok, warn50, warn75, warn90
}

/// What the meter renders: the reported percentage (nil when there is no
/// report), its band, and the display label ("—" for no report). The web
/// lib's KronosMeter, shaped as a Swift value.
struct KronosMeterState: Equatable {
    let pct: Double?
    let band: KronosBand
    let label: String
}

/// Classify a context-usage percentage for the meter, mirroring the web
/// kronosMeterState exactly: nil or a non-finite number means "no report"
/// (the wire's `context` key is omitted until kronos ever reports one, and a
/// missing report rendered as "0.0%" would claim a measurement nobody took).
/// The value is NOT clamped — a percentage outside 0-100 is a fact about
/// what was reported, and only the bar's fill clamps (kronosFillPct). Bands
/// are inclusive at each threshold: exactly 50 is already warn50, because
/// the daemon fires its warning ON the crossing.
func kronosMeterState(_ pct: Double?) -> KronosMeterState {
    guard let pct, pct.isFinite else {
        return KronosMeterState(pct: nil, band: .ok, label: "—")
    }
    let band: KronosBand = pct >= 90 ? .warn90 : pct >= 75 ? .warn75 : pct >= 50 ? .warn50 : .ok
    return KronosMeterState(pct: pct, band: band, label: String(format: "%.1f%%", pct))
}

/// The bar's fill width in percent, clamped into 0-100 so a nonsense report
/// cannot draw outside the track. No report fills nothing.
func kronosFillPct(_ pct: Double?) -> Double {
    guard let pct, pct.isFinite else { return 0 }
    return min(100, max(0, pct))
}

// MARK: - View model

@Observable
final class KronosTabViewModel {
    var status: KronosStatus?
    var loadError: String?
    /// True when the server answered /api/kronos with a 404 — a kraken serve
    /// older than the route, which is a routine skew (the app ships through
    /// TestFlight independently of the Mac's binary) and must not read as a
    /// network failure. KrakenError carries no status code, only the message
    /// perform() built from the response, so this is detected on that
    /// message's "HTTP 404" — brittle by construction and flagged as such,
    /// but the alternative is threading a code through every API call for
    /// one consumer.
    var serverPredatesRoute = false
    /// The discovered peer behind the status' pane — what SessionDetailView
    /// needs. The pane is the identity (a respawn is what moves it), but the
    /// VALUE also carries status/branch that SessionDetailView's header
    /// renders, and this tab is that header's permanent home — so the value
    /// is re-fetched on a slow cadence (every kronosPeerRefreshTicks polls,
    /// and immediately when the pane or spawnedAt moves) rather than once
    /// per pane lifetime, which froze the status dot for days. It is
    /// cleared when kronos is not live, or when the pane moved and the
    /// sweep has not yet surfaced the new one; a miss on a mere value
    /// refresh keeps the stale-but-addressable peer, because tearing the
    /// conversation view down discards carl's half-typed draft over one
    /// dropped poll.
    var peer: Peer?

    /// Polls since the peer VALUE was last fetched, and the spawnedAt the
    /// status carried at that fetch.
    private var ticksSincePeerFetch = 0
    private var peerFetchedForSpawn: Int64 = 0
    /// The base URL the current status/peer describe. Settings can change
    /// the server under a mounted tab; state resolved against the old host
    /// must not be handed to reads against the new one.
    private var describedBaseURL = ""

    /// How many status polls between peer-value refreshes: the peers sweep
    /// is a full tmux+ps discovery server-side, so it runs at a fraction of
    /// the status cadence — staleness on the header dot is bounded to
    /// ~ticks×poll seconds, which is seconds, not days.
    static let kronosPeerRefreshTicks = 4

    /// Drops every piece of state resolved against the current server.
    /// Called synchronously from .onChange(of: serverURL) — waiting for the
    /// restarted poll's first round trip would leave the old host's peer on
    /// screen (and addressable) against the new host for up to one round
    /// trip — and again defensively at the top of refresh, which is what
    /// covers a first poll that raced the change.
    func resetForServer(_ baseURL: String) {
        guard baseURL != describedBaseURL else { return }
        status = nil
        peer = nil
        loadError = nil
        serverPredatesRoute = false
        ticksSincePeerFetch = 0
        describedBaseURL = baseURL
    }

    func refresh(baseURL: String) async {
        resetForServer(baseURL)
        do {
            let st = try await APIClient.shared.kronosStatus(baseURL: baseURL)
            status = st
            loadError = nil
            serverPredatesRoute = false
            if st.live, !st.pane.isEmpty {
                ticksSincePeerFetch += 1
                let identityMoved = peer?.pane != st.pane || peerFetchedForSpawn != st.spawnedAt
                if identityMoved || ticksSincePeerFetch >= Self.kronosPeerRefreshTicks {
                    let resp = try await APIClient.shared.fetchPeers(baseURL: baseURL)
                    if let found = resp.peers.first(where: { $0.pane == st.pane }) {
                        peer = found
                    } else if identityMoved {
                        // The pane moved and the sweep missed the new one:
                        // the old peer is a dead address, drop it. A miss on
                        // a mere value refresh keeps the current peer.
                        peer = nil
                    }
                    ticksSincePeerFetch = 0
                    peerFetchedForSpawn = st.spawnedAt
                }
            } else {
                peer = nil
            }
        } catch {
            // A cancelled poll is the task doing as told (tab switched,
            // app backgrounded), not a failure — GraphViewModel's rule. By
            // the time it is here it may already be a bare string, so both
            // tests are needed.
            guard !Task.isCancelled, !(error is CancellationError) else { return }
            let msg = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            serverPredatesRoute = msg.contains("HTTP 404")
            loadError = msg
        }
    }
}

// MARK: - View

struct KronosTabView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @State private var vm = KronosTabViewModel()

    /// Re-keys the poll on the server as well as the scene: Settings can
    /// change the server under a mounted tab, and an unkeyed task would go
    /// on polling the old host for the life of the screen — GraphView's
    /// documented hazard, same fix.
    private struct PollKey: Equatable {
        let phase: ScenePhase
        let url: String
    }

    var body: some View {
        NavigationStack {
            content
                .background { ThemeBackgroundView() }
                .navigationTitle("Kronos")
                .navigationBarTitleDisplayMode(.inline)
        }
        // The status poll runs while the tab's scene is active, at web's
        // KRONOS_STATUS_POLL_MS cadence (5s) — /api/kronos is an uncached
        // discovery sweep server-side, so iOS must not be its fastest
        // client. The conversation inside SessionDetailView polls itself.
        .task(id: PollKey(phase: scenePhase, url: serverURL)) {
            guard scenePhase == .active else { return }
            await vm.refresh(baseURL: serverURL)
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(5))
                if Task.isCancelled { break }
                await vm.refresh(baseURL: serverURL)
            }
        }
        .onChange(of: serverURL) { _, newURL in
            vm.resetForServer(newURL)
        }
    }

    @ViewBuilder
    private var content: some View {
        if let st = vm.status {
            if !st.enabled {
                // stale: with the server unreachable, "create kronos.env"
                // would send carl to create a file that may already exist —
                // the off/not-running states make claims about the Mac, so
                // they must say when those claims stopped being current.
                kronosState(
                    icon: "moon.zzz",
                    title: "kronos is off",
                    detail: "The whole feature is opt-in on one file: create ~/.kraken/kronos.env on the Mac (an empty file is enough) and the daemon spawns him on its next tick.",
                    stale: vm.loadError != nil
                )
            } else if !st.live {
                kronosState(
                    icon: "arrow.triangle.2.circlepath",
                    title: "kronos is not running",
                    detail: "The daemon respawns him on its next tick — same conversation, resumed.",
                    stale: vm.loadError != nil
                )
            } else {
                // Live: the meter renders from the status alone, above
                // whatever the conversation area currently holds — web
                // renders it the same way, peer or no peer.
                VStack(spacing: 0) {
                    meterBar(st)
                    Divider().allowsHitTesting(false)
                    if let peer = vm.peer {
                        // .id re-creates the conversation view when a
                        // respawn moves kronos to a new pane or the server
                        // changes. It is the @State inside that demands a
                        // fresh identity, not the peer property: vm still
                        // holds the OLD pane's blocks, _mode was seeded from
                        // peer.role in init and is never re-seeded, and the
                        // poll re-keys on mode/phase rather than on the
                        // pane. allowKill false: the daemon owns kronos'
                        // lifecycle, a Kill here would only lose the
                        // in-flight turn and be undone next tick.
                        SessionDetailView(peer: peer, baseURL: serverURL, allowKill: false)
                            .id(serverURL + "|" + (peer.pane ?? ""))
                    } else {
                        kronosState(
                            icon: "magnifyingglass",
                            title: "finding kronos' pane…",
                            detail: "Live, but discovery has not surfaced the pane. Retried on every poll."
                        )
                    }
                }
            }
        } else if vm.serverPredatesRoute {
            kronosState(
                icon: "clock.arrow.circlepath",
                title: "this Mac's kraken is older than the app",
                detail: "The server has no /api/kronos route yet. Run `k update` on the Mac and this tab lights up."
            )
        } else if let err = vm.loadError {
            kronosState(icon: "wifi.exclamationmark", title: "cannot reach the board", detail: err)
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    /// The context meter: label, ticked track, band-colored fill, and the
    /// report's AGE — the age is what makes a stale answer read as stale
    /// instead of as current (the drilldown rule WebJobLoad.age exists for): the
    /// sentinel reports only while kronos renders a statusline, so an idle
    /// kronos' last number can be hours old and post-compaction wildly high.
    /// Rendered on any LIVE kronos; absence of a report renders "—" with an
    /// empty track rather than a claimed zero. A failing status poll leaves
    /// the last-known meter on screen, so the stale chip beside it is what
    /// says "this is a memory, not a reading".
    private func meterBar(_ st: KronosStatus) -> some View {
        let meter = kronosMeterState(st.context?.pct)
        return HStack(spacing: 10) {
            Text("context")
                .font(.caption.smallCaps())
                .foregroundStyle(KTheme.text2)
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(KTheme.surface)
                    Capsule()
                        .fill(bandColor(meter.band))
                        .frame(width: geo.size.width * kronosFillPct(meter.pct) / 100)
                    ForEach(kronosThresholds, id: \.self) { t in
                        Rectangle()
                            .fill(KTheme.hairline2)
                            .frame(width: 1)
                            .offset(x: geo.size.width * t / 100)
                    }
                }
            }
            .frame(height: 6)
            .clipShape(Capsule())
            Text(meter.label)
                .font(.caption.monospacedDigit())
                .foregroundStyle(meter.band == .ok ? KTheme.text2 : bandColor(meter.band))
            // at > 0 is WebJobLoad.age's guard, carried over: the server
            // always stamps `at` on a write, so a zero is defence — an age
            // computed from a missing stamp would render "20679d".
            if let ctx = st.context, ctx.at > 0 {
                let age = compactAgeLabel(secondsElapsed: Int(max(0, Int64(Date().timeIntervalSince1970 * 1000) - ctx.at) / 1000))
                Text(age + " ago")
                    .font(.caption2)
                    .foregroundStyle(KTheme.text2)
                    .accessibilityLabel("reported \(age) ago")
            }
            if vm.loadError != nil {
                Label("stale", systemImage: "exclamationmark.triangle")
                    .font(.caption2)
                    .foregroundStyle(KTheme.revealed)
                    .labelStyle(.titleAndIcon)
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(.ultraThinMaterial)
    }

    // Mirrors the web meter's band MAPPING (style.css .kronos-fill): base
    // fill the polling cyan reduced, warn50 the cyan, warn75 the amber,
    // warn90 the stopped red — same hue family per band on both boards.
    // Web mixes each warn hue toward the surface (~55-65%), so iOS renders
    // the same family more saturated; the mapping, not the hex, is the
    // mirror.
    private func bandColor(_ band: KronosBand) -> Color {
        switch band {
        case .ok: return KTheme.polling.opacity(0.45)
        case .warn50: return KTheme.polling
        case .warn75: return KTheme.revealed
        case .warn90: return KTheme.stopped
        }
    }

    private func kronosState(icon: String, title: String, detail: String, stale: Bool = false) -> some View {
        VStack(spacing: 12) {
            Image(systemName: icon)
                .font(.largeTitle)
                .foregroundStyle(KTheme.text2)
            Text(title)
                .font(.headline)
            Text(detail)
                .font(.subheadline)
                .foregroundStyle(KTheme.text2)
                .multilineTextAlignment(.center)
            if stale {
                Label("stale — the board is not answering", systemImage: "exclamationmark.triangle")
                    .font(.caption)
                    .foregroundStyle(KTheme.revealed)
            }
        }
        .padding(32)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
