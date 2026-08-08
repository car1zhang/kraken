import Combine
import SwiftUI

// Motion.swift — the stepped motion drivers.
//
// The web board's motion policy (style.css :258-281) is not a limitation to
// work around, it is the design: NO continuous smooth animation anywhere.
// Compositor-only movers were the previous rule and were still retired,
// because a running transform forces a frame at every vsync forever, which on
// an external monitor is system-wide WindowServer load. What survives is
// DISCRETE — an infinite animation may only use a step timing function, so it
// invalidates a few times a second instead of sixty.
//
// Three infinite animations exist in the whole sheet, and all three are here:
//
//   ppulse   2s   step-end   opacity 0.45 → 1        (:458-462)  daemon LED,
//                                                     blocked-card ⚑ chip
//   blink    1s   step-end   opacity 1 → 0           (:500-501)  the clock's
//                                                     block cursor, .panel-title's ▊
//   spin     1.1s steps(8)   rotate 0 → 360°         (:585-592)  the ring
//                                                     spinner on ⟳ UPDATE / ⇧ SHIP iOS
//
// Reading a `step-end` keyframe list is the one subtlety. `step-end` holds
// each keyframe's value until the NEXT keyframe is reached, so
// `@keyframes ppulse { 0%,100% { 0.45 } 50% { 1 } }` spends its first half at
// 0.45 and its second half at 1 — a plain square wave, not a ramp. Same for
// `blink { 50% { opacity: 0 } }`: visible for the first half second, hidden
// for the second.
//
// One driver per animation, SHARED — never a Timer per view. `53-concessions`
// D4 names this explicitly ("one motion driver, not per-view timers"): the
// LED and the ⚑ chips must also flip in lockstep, which a per-view timer
// started at each view's own appearance cannot do.

/// A shared discrete clock: `steps` evenly-spaced phases across `period`,
/// advancing on a timer and republishing on each advance.
///
/// It runs only while something holds it. `retain()`/`release()` are what the
/// view modifiers below call from `onAppear`/`onDisappear`, so a board with no
/// spinner on screen costs no timer at all.
@MainActor
final class SteppedTimer: ObservableObject {
    let period: TimeInterval
    let steps: Int

    /// `0 ..< steps`.
    @Published private(set) var step: Int = 0

    private var tick: AnyCancellable?
    private var holders = 0

    init(period: TimeInterval, steps: Int) {
        self.period = period
        self.steps = max(1, steps)
    }

    /// How long one step lasts — the timer's interval, and the rate at which
    /// this driver invalidates its observers.
    var interval: TimeInterval { period / Double(steps) }

    func retain() {
        holders += 1
        guard tick == nil, steps > 1 else { return }
        // `.common` rather than `.default`: a timer scheduled in the default
        // run-loop mode stops firing while the run loop is TRACKING, which
        // here would mean the LED and every spinner freezing for the duration
        // of a card drag or a window resize — a stall the CSS original does
        // not have.
        tick = Timer.publish(every: interval, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] _ in
                MainActor.assumeIsolated {
                    guard let self else { return }
                    self.step = (self.step + 1) % self.steps
                }
            }
    }

    func release() {
        holders = max(0, holders - 1)
        guard holders == 0 else { return }
        tick?.cancel()
        tick = nil
        // Back to the resting phase, so the next thing to appear starts where
        // reduced motion would have frozen it rather than mid-cycle.
        step = 0
    }

    /// The attention pulse — `@keyframes ppulse`, 2s, two phases.
    static let pulse = SteppedTimer(period: 2, steps: 2)
    /// The block-cursor blink — `@keyframes blink`, 1s, two phases.
    static let blink = SteppedTimer(period: 1, steps: 2)
    /// The ring spinner — `@keyframes spin`, 1.1s, eight positions.
    static let spin = SteppedTimer(period: 1.1, steps: 8)
}

enum HudMotion {
    /// `ppulse`'s two held values, in order (:458).
    static let pulseOpacities: [Double] = [0.45, 1.0]
    /// `blink`'s two held values, in order (:501).
    static let blinkOpacities: [Double] = [1.0, 0.0]
    /// The resting appearance under Reduce Motion, and the value every driver
    /// returns to when nothing holds it: fully visible, unrotated. The web
    /// board reaches the same state by killing the animation outright
    /// (:2292-2298), which leaves each element at its un-animated value.
    static let restingOpacity: Double = 1.0
}

/// Drives one view's opacity off a shared `SteppedTimer`.
private struct SteppedOpacity: ViewModifier {
    @ObservedObject var driver: SteppedTimer
    let values: [Double]
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        // Under Reduce Motion the driver is never retained, so the animation
        // costs nothing at all rather than running invisibly.
        content
            .opacity(reduceMotion ? HudMotion.restingOpacity : values[driver.step % values.count])
            .onAppear { if !reduceMotion { driver.retain() } }
            .onDisappear { if !reduceMotion { driver.release() } }
    }
}

/// Drives one view's rotation off the 8-step spin driver.
private struct SteppedRotation: ViewModifier {
    @ObservedObject var driver: SteppedTimer
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        let degrees = reduceMotion ? 0 : Double(driver.step) * (360 / Double(driver.steps))
        return content
            .rotationEffect(.degrees(degrees))
            .onAppear { if !reduceMotion { driver.retain() } }
            .onDisappear { if !reduceMotion { driver.release() } }
    }
}

// The three drivers are main-actor state, so the modifiers that reach for
// them are annotated rather than left nonisolated — a `View` extension is not
// implicitly main-actor even though `body` is.
extension View {
    /// The attention pulse — the daemon LED and the blocked card's question
    /// chip. Note there is no `.animation` here and there must never be one:
    /// the flip IS the effect, and interpolating between the two values is
    /// exactly the continuous animation the policy retired.
    @MainActor func hudPulse() -> some View {
        modifier(SteppedOpacity(driver: .pulse, values: HudMotion.pulseOpacities))
    }

    /// The block cursor's blink — the chrome clock's `▊` and the panel
    /// title's.
    @MainActor func hudBlink() -> some View {
        modifier(SteppedOpacity(driver: .blink, values: HudMotion.blinkOpacities))
    }

    /// The 8-position ring spinner's rotation. The spinner ART (a bordered
    /// ring with one `currentColor` arc) belongs to whoever draws the button;
    /// this only turns it, and it turns a SQUARE box about its true centre,
    /// which is the whole reason the sheet stopped rotating the glyph itself
    /// (:576-584 — an arrow rotating about its inline box's centre orbits
    /// instead of spinning).
    @MainActor func hudSpin() -> some View {
        modifier(SteppedRotation(driver: .spin))
    }
}
