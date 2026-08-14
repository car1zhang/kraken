// sfx.ts — the board's sound module: every click on an actionable target and
// every keystroke in a text field gets an 8-bit chip pulse, and every board
// event the poll loop notices gets a short motif of its own, all synthesized
// in Web Audio from the locked tables below. Zero assets, zero dependencies:
// the whole voice is square oscillators through flat-top gain envelopes
// (near-instant attack, full sustain, hard cut — the chip envelope, which is
// what makes these read as game sounds rather than notification bleeps),
// played two octaves down through a synthesized convolver reverb for the
// board's cyberpunk-night register.
//
// The AudioContext is a lazy singleton, constructed on the first enabled play.
// Only the gesture-driven plays construct it — playSfx runs from the capture
// listeners below, so the browser's autoplay policy is satisfied by
// construction. Board-event sounds have no gesture behind them (they arrive on
// a poll tick), so playEventSfx never constructs anything and simply drops
// unless a real gesture has already unlocked a running context.
// Everything degrades to silence: no AudioContext, a throwing constructor, a
// suspended context that won't resume — none of it may break the board.
//
// Sounds are deliberately NOT motion: the sheet's prefers-reduced-motion rule
// kills the press-flash one-shot along with every other animation, but audio
// is governed only by the ♪ SFX button's two localStorage keys — kraken-sfx,
// the mute flag, and kraken-sfx-vol, the volume percent.
//
// The artifact iframe is deliberately outside all of this: the listeners
// attach to the top document only, and events never cross the iframe
// boundary. (The overlay does reach into that frame's document for keys —
// staying out of it is this module's choice, not something the boundary
// imposes.)

import { MOUSE_ACTIVATION, TOUCH_ACTIVATION } from "../hooks/useDragReorder.ts";
import { readLS } from "../hooks/useLocalStorage.ts";
import type { EventType } from "./boardEvents.ts";

export type SoundName = "key" | "click" | "enter" | "toggle";

interface Pulse {
  /**
   * NOMINAL oscillator frequency, Hz — fixed pitch, no jitter, no RNG in the
   * note itself. What reaches the oscillator is freq × PITCH_FACTOR; the
   * tables below stay written at nominal pitch so they still read as the
   * notes that were auditioned.
   */
  freq: number;
  /** seconds from the pulse's own start to its hard cut */
  dur: number;
  /** seconds after the sound's start this pulse begins */
  at: number;
  /** peak envelope gain, pre-master */
  gain: number;
}

/**
 * One shared output level; the per-pulse gains above ride through this. What
 * the master node actually holds is MASTER_GAIN × (volume% / 100) — the ♪ SFX
 * button's stored percent scales this base, it never replaces it, so the
 * tables keep reading as the levels that were auditioned.
 */
export const MASTER_GAIN = 0.12;

/**
 * The ♪ SFX cycle's volume steps, in click order; past the last one the
 * button turns OFF, then re-enters at the head. 100 first because it is the
 * default and the value an unknown stored percent snaps back to; no 0 —
 * mute is the kraken-sfx FLAG, not a level, so the stored percent always
 * names a real level and the two keys stay orthogonal. (A hand-edited
 * stored 0 is still honoured — the label honestly reads 0%, so the state at
 * least says what it does.)
 */
export const VOLUME_STEPS: readonly number[] = [100, 75, 50, 25];

/**
 * Decode a stored kraken-sfx-vol value into a percent. Shared by sfxVolume()
 * and the SfxToggle label for the same reason sfxEnabled's decode is shared
 * with that button: one decoding is what keeps the label and the audio
 * agreeing on any third value. Anything outside a finite 0–100 — absent key,
 * hand-edited garbage, negatives, Infinity — reads as 100, the default;
 * in-range values round to the integer the label can render.
 */
export function decodeVolPercent(raw: string | null): number {
  // Number("") is 0, not NaN — a blank value must read as absent, not as mute
  if (raw === null || raw.trim() === "") return 100;
  const n = Number(raw);
  if (!Number.isFinite(n) || n < 0 || n > 100) return 100;
  return Math.round(n);
}

// The cyberpunk-night retune, decided board-wide in 40-sfx-event-audition.html:
// every voice drops two octaves and picks up a dark tail. It is applied at
// synthesis rather than by rewriting the tables, so the interaction spec and
// the event palette can never drift apart on it, and both keep reading as the
// notes that were auditioned.
/** What every oscillator frequency is multiplied by: 0.25 = two octaves down. */
export const PITCH_FACTOR = 0.25;

// The reverb is synthesized, not sampled — an exponentially-decaying noise
// impulse built once per context — so the treatment still costs zero assets.
// It hangs off the master as a WET-ONLY parallel branch beside the untouched
// dry path, which is what lets a browser that cannot build any of the three
// node types simply lose the tail instead of the sound.
/** Level of the wet branch; the dry master → destination path is unchanged. */
export const REVERB_WET_GAIN = 0.72;
/** Impulse length in seconds — the outer bound on the tail. */
export const REVERB_SECONDS = 2.8;
/** Exponent on the impulse's fade: above 1 it dumps most energy early. */
export const REVERB_DECAY = 3.2;
/** Lowpass ahead of the convolver — what makes the tail dark rather than fizzy. */
export const REVERB_LOWPASS_HZ = 3000;

/** Envelope shape: linear attack, flat sustain, linear cut-out at the end. */
export const ATTACK_S = 0.002;
const CUT_S = 0.008;

/** Held-key repeat must not stack voices: key ticks closer than this drop. */
export const KEY_RATE_LIMIT_MS = 33;

// The locked spec (chosen through six audition rounds in 40-sfx-mockups.html):
// NES SNAP with the enter/click sounds swapped and gains cut ~35%. Keystroke =
// one B5 pulse; actionable click = the rising two-note confirm; Enter = the
// falling blip; toggles = a single E5 pulse.
export const SOUNDS: Record<SoundName, readonly Pulse[]> = {
  key: [{ freq: 988, dur: 0.022, at: 0, gain: 1.4 }],
  click: [
    { freq: 988, dur: 0.04, at: 0, gain: 1.8 },
    { freq: 1319, dur: 0.04, at: 0.045, gain: 1.8 },
  ],
  enter: [
    { freq: 1319, dur: 0.025, at: 0, gain: 1.8 },
    { freq: 988, dur: 0.03, at: 0.028, gain: 1.8 },
  ],
  toggle: [{ freq: 659, dur: 0.045, at: 0, gain: 1.6 }],
};

// The SNAP-X palette (Q1, chosen by playing 40-sfx-event-audition.html): the
// same square-wave vocabulary as SOUNDS, one motif per event type, none of
// them running past 0.25s. The shape carries the meaning — rising for good
// news (done, pr-merged, resumed, update-done), falling for something wanting
// attention (blocked, thread-closed, update-failed), an oscillating two-beat
// for the question that is waiting on carl, a low tritone wobble for health,
// and one blip / one dyad for the two ambient changes (artifact, color).
// Nominal pitch like SOUNDS: PITCH_FACTOR drops it into the night register at
// synthesis. All 13 types ship enabled (Q2) and there is no enabled-set
// mechanism, so pruning one means editing this table.
export const EVENT_SOUNDS: Record<EventType, readonly Pulse[]> = {
  question: [
    { freq: 988, dur: 0.03, at: 0, gain: 1.6 },
    { freq: 1319, dur: 0.03, at: 0.04, gain: 1.6 },
    { freq: 988, dur: 0.03, at: 0.14, gain: 1.6 },
    { freq: 1319, dur: 0.03, at: 0.18, gain: 1.6 },
  ],
  blocked: [
    { freq: 988, dur: 0.045, at: 0, gain: 1.6 },
    { freq: 784, dur: 0.055, at: 0.05, gain: 1.6 },
  ],
  resumed: [
    { freq: 784, dur: 0.04, at: 0, gain: 1.5 },
    { freq: 988, dur: 0.045, at: 0.045, gain: 1.5 },
  ],
  done: [
    { freq: 1047, dur: 0.045, at: 0, gain: 1.7 },
    { freq: 1319, dur: 0.045, at: 0.05, gain: 1.7 },
    { freq: 1568, dur: 0.07, at: 0.1, gain: 1.7 },
  ],
  "pr-open": [
    { freq: 988, dur: 0.03, at: 0, gain: 1.7 },
    { freq: 988, dur: 0.03, at: 0.06, gain: 1.7 },
    { freq: 1319, dur: 0.06, at: 0.12, gain: 1.7 },
  ],
  "pr-merged": [
    { freq: 1047, dur: 0.04, at: 0, gain: 1.7 },
    { freq: 1319, dur: 0.04, at: 0.045, gain: 1.7 },
    { freq: 1568, dur: 0.04, at: 0.09, gain: 1.7 },
    { freq: 2093, dur: 0.09, at: 0.135, gain: 1.7 },
  ],
  health: [
    { freq: 330, dur: 0.07, at: 0, gain: 1.5 },
    { freq: 466, dur: 0.07, at: 0.08, gain: 1.5 },
    { freq: 330, dur: 0.09, at: 0.16, gain: 1.5 },
  ],
  "thread-open": [
    { freq: 1175, dur: 0.035, at: 0, gain: 1.5 },
    { freq: 880, dur: 0.045, at: 0.04, gain: 1.5 },
  ],
  "thread-closed": [
    { freq: 880, dur: 0.035, at: 0, gain: 1.4 },
    { freq: 659, dur: 0.05, at: 0.04, gain: 1.4 },
  ],
  artifact: [{ freq: 1047, dur: 0.028, at: 0, gain: 1.4 }],
  color: [
    { freq: 1319, dur: 0.03, at: 0, gain: 1.2 },
    { freq: 1047, dur: 0.03, at: 0, gain: 1.2 },
  ],
  "update-done": [
    { freq: 784, dur: 0.035, at: 0, gain: 1.6 },
    { freq: 1047, dur: 0.035, at: 0.04, gain: 1.6 },
    { freq: 1319, dur: 0.055, at: 0.08, gain: 1.6 },
  ],
  "update-failed": [
    { freq: 1319, dur: 0.04, at: 0, gain: 1.6 },
    { freq: 1047, dur: 0.04, at: 0.045, gain: 1.6 },
    { freq: 392, dur: 0.09, at: 0.09, gain: 1.6 },
  ],
};

// Structural slices of the Web Audio types — what this module actually calls.
// Typing against these rather than lib.dom's AudioContext is what lets the
// tests inject a plain recording fake without faking the full interface.
export interface SfxOscillator {
  type: string;
  frequency: { setValueAtTime(value: number, time: number): void };
  connect(node: unknown): void;
  start(time: number): void;
  stop(time: number): void;
}
export interface SfxGainParam {
  value: number;
  setValueAtTime(value: number, time: number): void;
  linearRampToValueAtTime(value: number, time: number): void;
}
export interface SfxGainNode {
  gain: SfxGainParam;
  connect(node: unknown): void;
}
export interface SfxBiquadFilter {
  type: string;
  frequency: { value: number };
  connect(node: unknown): void;
}
export interface SfxConvolver {
  buffer: unknown;
  connect(node: unknown): void;
}
export interface SfxAudioBuffer {
  getChannelData(ch: number): Float32Array;
}
export interface SfxAudioContext {
  currentTime: number;
  /** frames per second — the impulse's length is derived from it */
  sampleRate: number;
  state?: string;
  destination: unknown;
  resume?(): unknown;
  createOscillator(): SfxOscillator;
  createGain(): SfxGainNode;
  // the three night-bus factories are required members, not optional ones: a
  // context missing them is a fake that has not been updated, and that should
  // fail a typecheck rather than silently lose the reverb at runtime
  createBiquadFilter(): SfxBiquadFilter;
  createConvolver(): SfxConvolver;
  createBuffer(channels: number, frames: number, sampleRate: number): SfxAudioBuffer;
}

function defaultFactory(): SfxAudioContext | null {
  const w = window as unknown as {
    AudioContext?: new () => SfxAudioContext;
    webkitAudioContext?: new () => SfxAudioContext;
  };
  const Ctor = w.AudioContext ?? w.webkitAudioContext;
  if (!Ctor) return null;
  try {
    return new Ctor();
  } catch {
    return null;
  }
}

let ctxFactory: () => SfxAudioContext | null = defaultFactory;
let ctx: SfxAudioContext | null = null;
let master: SfxGainNode | null = null;
// null = not yet read; the first consult reads kraken-sfx (default ON). The
// SfxToggle persists the flag; this module only holds the runtime state.
let enabled: boolean | null = null;
// same lifecycle as `enabled`: null = not yet read, first consult reads
// kraken-sfx-vol (default 100). The SfxToggle persists the percent.
let volPct: number | null = null;
// separate watermarks: each keydown-driven sound bounds its own auto-repeat,
// and a trailing character tick can never swallow the rarer, more meaningful
// confirm by landing within the same 33ms window
let lastKeyAt = -Infinity;
let lastEnterAt = -Infinity;

export function sfxEnabled(): boolean {
  // decode must match useLocalStorageFlag's ("1"/"0", default ON) — the
  // SfxToggle reads the same key through that hook, and a second decoding
  // would let the label and the audio disagree on any third value
  if (enabled === null) {
    const raw = readLS("kraken-sfx");
    enabled = raw === null ? true : raw === "1";
  }
  return enabled;
}

export function setSfxEnabled(on: boolean): void {
  enabled = on;
}

/** Current volume percent (0–100), read lazily from kraken-sfx-vol. */
export function sfxVolume(): number {
  if (volPct === null) volPct = decodeVolPercent(readLS("kraken-sfx-vol"));
  return volPct;
}

/** Seconds the live master takes to glide to a new volume: everything in this
 * module ramps rather than steps (ATTACK_S, CUT_S) to avoid amplitude
 * discontinuities, and the master must follow the same rule — a hard step
 * lands as an audible click on whatever voice is sounding when it moves. */
export const VOL_RAMP_S = 0.015;

/**
 * Set the runtime volume and apply it to the LIVE master node when one
 * exists — the singleton outlives the button, so a construction-time-only
 * read would make the new level inaudible until a reload. Out-of-range input
 * goes through the same decode as a stored value, so the runtime state can
 * never hold a percent the label could not show. The try is the module-wide
 * contract: every audio-node touch degrades to silence, and this one is
 * reachable straight from a React handler, so a throwing param must degrade
 * to a stale level, never to a broken click.
 */
export function setSfxVolume(pct: number): void {
  volPct = decodeVolPercent(String(pct));
  if (!ctx || !master) return;
  try {
    const t = ctx.currentTime;
    // anchor at the current value first: with no prior event on the param, a
    // bare ramp has nothing scheduled to ramp from
    master.gain.setValueAtTime(master.gain.value, t);
    master.gain.linearRampToValueAtTime(MASTER_GAIN * (volPct / 100), t + VOL_RAMP_S);
  } catch {
    // silence (at the old level) is the contract
  }
}

/**
 * The reverb's impulse response, synthesized: white noise faded out along
 * (1 - i/n)^REVERB_DECAY, which is the cheapest thing that sounds like a room
 * rather than a delay line. This is the module's only RNG, and it is heard as
 * a tail, never as a pitch — the notes themselves stay deterministic.
 */
function buildImpulse(c: SfxAudioContext): SfxAudioBuffer {
  const frames = Math.ceil(c.sampleRate * REVERB_SECONDS);
  const buf = c.createBuffer(2, frames, c.sampleRate);
  // two independent channels, not one copied: decorrelated noise is what makes
  // the tail read as width instead of a mono blur pinned to the centre
  for (let ch = 0; ch < 2; ch++) {
    const data = buf.getChannelData(ch);
    for (let i = 0; i < frames; i++) {
      data[i] = (Math.random() * 2 - 1) * (1 - i / frames) ** REVERB_DECAY;
    }
  }
  return buf;
}

/**
 * The night bus: master → lowpass → convolver → wetGain → destination, built
 * ONCE per context beside the dry master → destination path that ensureContext
 * already wired. Nothing holds a reference to these nodes — the graph does,
 * exactly as it does for pulse()'s per-voice envelopes — so they die with the
 * context singleton and there is no extra module state for the test seams to
 * reset. The caller swallows a throw from any of the three factories: an old
 * browser missing one of them loses the tail, and the dry path plays on.
 */
function buildNightBus(c: SfxAudioContext, m: SfxGainNode): void {
  const lowpass = c.createBiquadFilter();
  lowpass.type = "lowpass";
  lowpass.frequency.value = REVERB_LOWPASS_HZ;
  const convolver = c.createConvolver();
  convolver.buffer = buildImpulse(c);
  const wet = c.createGain();
  wet.gain.value = REVERB_WET_GAIN;
  m.connect(lowpass);
  lowpass.connect(convolver);
  convolver.connect(wet);
  wet.connect(c.destination);
}

interface ReadyContext {
  c: SfxAudioContext;
  m: SfxGainNode;
  /** constructed by THIS call — the one case a non-running state may schedule */
  fresh: boolean;
}

function ensureContext(): ReadyContext | null {
  let fresh = false;
  if (!ctx || !master) {
    // build both into locals and commit together: a throwing createGain must
    // not leave ctx set with master null, or every later play would throw at
    // the connect instead of degrading to silence
    const c = ctxFactory();
    if (!c) return null;
    let m: SfxGainNode;
    try {
      m = c.createGain();
      m.gain.value = MASTER_GAIN * (sfxVolume() / 100);
      m.connect(c.destination);
    } catch {
      // the context itself was already constructed — close it, or each retry
      // abandons one holding a live audio thread until the browser's
      // per-document cap makes the constructor start throwing. Same swallow
      // shape as resume() below: close() returns a promise that can reject,
      // and the try alone only catches a synchronous throw.
      try {
        void Promise.resolve((c as { close?: () => unknown }).close?.()).catch(() => {});
      } catch {
        // a throwing close changes nothing
      }
      return null;
    }
    ctx = c;
    master = m;
    fresh = true;
    // AFTER the commit, and in its own try: the wet branch is a bonus hanging
    // off a dry path that is already complete and already published, so a
    // context whose convolver (or filter, or buffer) throws loses the tail and
    // keeps every sound. Never retried for this context — it lives in the
    // construction branch, and retrying per play would rebuild the graph.
    try {
      buildNightBus(c, m);
    } catch {
      // no tail; the dry path above is untouched
    }
  }
  if (ctx.state !== undefined && ctx.state !== "running" && ctx.resume) {
    // covers "suspended" AND iOS Safari's "interrupted"; resume() rejects on
    // a closed context, and void alone would leak that as an unhandled
    // rejection — swallow it, silence is the contract
    try {
      void Promise.resolve(ctx.resume()).catch(() => {});
    } catch {
      // a synchronously-throwing resume degrades the same way
    }
  }
  return { c: ctx, m: master as SfxGainNode, fresh };
}

/**
 * One square pulse through the flat-top envelope, onto the master gain — and
 * the one place PITCH_FACTOR is applied, so every table (interaction and
 * event alike) lands in the same register with no chance of one being
 * retuned without the other.
 */
function pulse(c: SfxAudioContext, m: SfxGainNode, p: Pulse, t0: number): void {
  const start = t0 + p.at;
  const osc = c.createOscillator();
  osc.type = "square";
  osc.frequency.setValueAtTime(p.freq * PITCH_FACTOR, start);
  const g = c.createGain();
  g.gain.setValueAtTime(0.0001, start);
  g.gain.linearRampToValueAtTime(p.gain, start + ATTACK_S);
  g.gain.setValueAtTime(p.gain, start + p.dur - CUT_S);
  g.gain.linearRampToValueAtTime(0.0001, start + p.dur);
  osc.connect(g);
  g.connect(m);
  osc.start(start);
  osc.stop(start + p.dur + 0.02);
}

export function playSfx(name: SoundName): void {
  if (!sfxEnabled()) return;
  // both keydown-driven sounds auto-repeat when their key is held — a
  // limiter keyed on "is this the key sound" left held Enter free to stack
  // two-pulse blips at the OS repeat rate
  if (name === "key") {
    const now = performance.now();
    if (now - lastKeyAt < KEY_RATE_LIMIT_MS) return;
    lastKeyAt = now;
  } else if (name === "enter") {
    const now = performance.now();
    if (now - lastEnterAt < KEY_RATE_LIMIT_MS) return;
    lastEnterAt = now;
  }
  try {
    const ready = ensureContext();
    if (!ready) return;
    // a context that exists but is not running has a FROZEN currentTime:
    // scheduling into it queues voices that all fire at once if the context
    // ever recovers (iOS "interrupted" after a phone call). Skip, except on
    // the context's very first play, where a just-constructed context may
    // legitimately still be flipping to running inside this same gesture.
    if (!ready.fresh && ready.c.state !== undefined && ready.c.state !== "running") return;
    const t0 = ready.c.currentTime;
    for (const p of SOUNDS[name]) pulse(ready.c, ready.m, p, t0);
  } catch {
    // silence is the contract — a throwing node factory must not reach the
    // document capture listener
  }
}

/**
 * A board event announcing itself: same master, same envelope, same register
 * as a click — but this one arrives on a poll tick with no user gesture behind
 * it, and that difference is the whole rule. It NEVER constructs the
 * AudioContext: a browser that has not been unlocked would either throw or
 * hand back a context stuck in "suspended", and calling resume() outside a
 * gesture is exactly the autoplay violation the module's design avoids. So an
 * event arriving before carl has clicked anything is silent, permanently and
 * by intent, and the first click of the session both unlocks the context and
 * turns the board's voice on.
 *
 * There is no rate limit here on purpose: the coalescer upstream is the rate
 * limit (it caps and staggers a tick's events before any of them get here),
 * and a second watermark inside this module would silently drop sounds it
 * already decided were worth playing.
 */
export function playEventSfx(name: EventType): void {
  if (!sfxEnabled()) return;
  // read the singleton into locals: this is the read-only twin of
  // ensureContext, and the pair must stay committed-together the same way
  const c = ctx;
  const m = master;
  if (!c || !m) return;
  // no `fresh` exemption to make here — nothing this function does could have
  // constructed the context, so a non-running one is always the frozen-clock
  // case playSfx refuses to schedule into. An undefined state reads as running,
  // matching playSfx, so a minimal fake behaves the same on both paths.
  if (c.state !== undefined && c.state !== "running") return;
  try {
    const t0 = c.currentTime;
    for (const p of EVENT_SOUNDS[name]) pulse(c, m, p, t0);
  } catch {
    // same contract as playSfx: a throwing node factory reaches no caller —
    // here that caller is a poll-tick effect, which must not be broken by audio
  }
}

// ── DOM event classification ─────────────────────────────────────────────────
// (DOM events, not the board events above — these two live in the same module
// and mean entirely different things by "event".)

// What counts as an actionable click target. Text inputs are deliberately
// absent — clicking into one is focusing, not acting, and its feedback is the
// key ticks that follow. Plain panel/column clicks match nothing and stay
// silent.
export const ACTIONABLE_SELECTOR =
  'button, a, [role="button"], select, summary, .card, ' +
  'input[type="checkbox"], input[type="radio"], input[type="range"]';

/** The actionable element a click landed on (via closest), or null. */
export function actionableFrom(target: unknown): Element | null {
  if (!(target instanceof Element)) return null;
  return target.closest(ACTIONABLE_SELECTOR);
}

// input.type normalizes a missing or invalid type attribute to "text", so the
// board's five bare <input> fields are covered without a :not([type]) clause.
const TEXT_INPUT_TYPES = new Set(["text", "search", "url", "email", "password", "number"]);

/** The sound a keydown owes: enter / key / null. Modifier chords are silent. */
export function keySound(e: KeyboardEvent): SoundName | null {
  const t = e.target;
  const isText =
    t instanceof HTMLTextAreaElement ||
    (t instanceof HTMLInputElement && TEXT_INPUT_TYPES.has(t.type));
  if (!isText) return null;
  // IME composition: the same guard the thread rename field uses. Blink sends
  // every composing keystroke as key="Process"/keyCode 229 (silent below
  // anyway — this makes it a decision); Safari/Firefox send the
  // candidate-commit as a composing Enter, which confirms nothing and must
  // not claim the confirm sound. Dead keys land here too: silent on the dead
  // press, one tick on the composed character.
  if (e.isComposing || e.keyCode === 229) return null;
  // Enter outranks the modifier bail: ⌘/Ctrl+Enter IS the composer's submit,
  // the most confirm-shaped keystroke on the board — bailing on the chord
  // would silence the real send while plain Enter (a newline) still sounded
  if (e.key === "Enter") return "enter";
  if (e.metaKey || e.ctrlKey || e.altKey) return null;
  if (e.key.length === 1 || e.key === "Backspace") return "key";
  return null;
}

// ── wiring ───────────────────────────────────────────────────────────────────

/** press-flash runs 140ms in the sheet; the backstop only fires when no animation ever will (pinned against the CSS duration by sfx.test.ts). */
export const FLASH_CLEANUP_MS = 250;

// each element's live flash owns a detach that retires its listener and timer
// WITHOUT touching the class — flash() runs the previous generation's detach
// before arming a new one, so a superseded flash can never strip the class
// out from under its successor
const flashDetach = new WeakMap<Element, () => void>();

/**
 * Restartable one-shot: retire the previous flash's exits, remove → reflow →
 * add so the animation re-fires, then arm this flash's own two exits —
 * animationend (target-gated: it bubbles, and a descendant's one-shot like
 * .btn:active's glitch must not clear an ancestor's flash), and a timer
 * backstop for when no animation ever runs (prefers-reduced-motion sets
 * `animation: none`, so no animation event ever fires; event-only cleanup
 * stranded the class and one leaked listener per click, then strobed every
 * stranded element at once if the OS setting was flipped live). There is
 * deliberately NO animationcancel exit: the restart's own classList.remove
 * cancels the in-flight animation and that cancel event dispatches AFTER the
 * new flash is armed, so an exit listening for it would strip the new flash
 * one frame in — the timer already covers everything cancel would.
 */
function flash(el: Element): void {
  flashDetach.get(el)?.();
  el.classList.remove("press-flash");
  void (el as HTMLElement).offsetWidth;
  el.classList.add("press-flash");
  let timer: ReturnType<typeof setTimeout> | undefined;
  const detach = () => {
    clearTimeout(timer);
    el.removeEventListener("animationend", done);
    flashDetach.delete(el);
  };
  const done = (e?: Event) => {
    if (e && e.target !== el) return;
    // and name-gated: the element's OWN other animation ending (.btn:active's
    // glitch under a held Enter) must not retire the flash either — the
    // undefined allowance keeps hand-dispatched bare Events (tests) working
    const name = (e as AnimationEvent | undefined)?.animationName;
    if (name !== undefined && name !== "pressflash") return;
    detach();
    el.classList.remove("press-flash");
  };
  flashDetach.set(el, detach);
  el.addEventListener("animationend", done);
  timer = setTimeout(done, FLASH_CLEANUP_MS);
}

/**
 * The document-level capture listeners that drive everything: no component
 * wires its own click or key sound — the one direct playSfx call outside
 * this module is SfxToggle's re-enable confirm, which by definition cannot
 * come from a listener that was disabled when the click landed. Returns the
 * uninstaller; App mounts this once. Capture phase so a component's own
 * stopPropagation cannot make its clicks silently exempt — but capture also
 * runs BEFORE every legitimate suppression, so onClick re-derives the two
 * "not really a click" classes the board already recognises ON CARDS, the
 * one surface that both drags and selects: a click ending a text selection
 * (JobCard's own drilldown guard), and a drag's synthetic click — observed:
 * it still reaches an earlier-registered document capture listener even
 * though dnd-kit keeps it from React. Both guards are scoped to .card
 * because no other actionable element can drag, and no other carries the
 * board's selection rule — applied board-wide, the travel check silenced
 * ordinary trackpad clicks whose press drifted 5px on buttons that cannot
 * drag at all. The travel thresholds are each pointer type's own sensor
 * constant — the mouse's 4px activation distance, the touch sensor's 8px
 * hold tolerance — imported so they cannot drift; a click with detail === 0
 * (keyboard activation, element.click()) has no pointer gesture to measure
 * and is never judged by it.
 */
export function installSfxListeners(doc: Document = document): () => void {
  let downX = NaN;
  let downY = NaN;
  let downType = "";
  const onPointerDown = (e: PointerEvent) => {
    downX = e.clientX;
    downY = e.clientY;
    downType = e.pointerType ?? "";
  };
  const onClick = (e: MouseEvent) => {
    // consume the pointerdown: one press judges at most one click, so a
    // stale press can never be measured against a later gesture's click
    const pressX = downX;
    const pressY = downY;
    const pressType = downType;
    downX = NaN;
    downY = NaN;
    downType = "";
    const el = actionableFrom(e.target);
    if (!el) return;
    if (el.closest(".card")) {
      // a click that ends a text selection is not a click (JobCard.tsx
      // applies the same rule to its drilldown); isCollapsed answers the
      // boolean without serialising a possibly-huge selection
      const sel = doc.defaultView?.getSelection();
      if (sel && !sel.isCollapsed) return;
      // a gesture that travelled past its pointer type's threshold is a
      // drag's synthetic click, and the board just guaranteed it activated
      // nothing — confirming it audibly would invert the one signal the
      // sound carries
      if (e.detail > 0 && !Number.isNaN(pressX)) {
        const limit =
          pressType === "touch" || pressType === "pen"
            ? TOUCH_ACTIVATION.tolerance
            : MOUSE_ACTIVATION.distance;
        if (Math.hypot(e.clientX - pressX, e.clientY - pressY) > limit) return;
      }
    }
    playSfx("click");
    flash(el);
  };
  const onKeydown = (e: KeyboardEvent) => {
    const name = keySound(e);
    if (name) playSfx(name);
  };
  // passive: pointerdown only records coordinates, and saying so keeps the
  // browser from waiting on it
  doc.addEventListener("pointerdown", onPointerDown, { capture: true, passive: true });
  doc.addEventListener("click", onClick, true);
  doc.addEventListener("keydown", onKeydown, true);
  return () => {
    doc.removeEventListener("pointerdown", onPointerDown, true);
    doc.removeEventListener("click", onClick, true);
    doc.removeEventListener("keydown", onKeydown, true);
  };
}

// ── test seams ───────────────────────────────────────────────────────────────

// Both seams drop the context singleton, and that is all they need to do about
// the night bus: its nodes are held only by the graph hanging off that
// context, so they go with it and the next construction builds a fresh one.

/** Swap the AudioContext factory (null restores the real one); drops the singleton. */
export function injectAudioContextFactoryForTest(
  f: (() => SfxAudioContext | null) | null,
): void {
  ctxFactory = f ?? defaultFactory;
  ctx = null;
  master = null;
}

/** Full module reset: singleton, enabled flag, volume, and both rate-limit watermarks. */
export function resetSfxForTest(): void {
  ctxFactory = defaultFactory;
  ctx = null;
  master = null;
  enabled = null;
  volPct = null;
  lastKeyAt = -Infinity;
  lastEnterAt = -Infinity;
}
