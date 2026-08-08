// sfx.ts — the board's sound module: every click on an actionable target and
// every keystroke in a text field gets an 8-bit chip pulse, synthesized in Web
// Audio from the locked spec below. Zero assets, zero dependencies: the whole
// voice is square oscillators through flat-top gain envelopes (near-instant
// attack, full sustain, hard cut — the chip envelope, which is what makes
// these read as game sounds rather than notification bleeps).
//
// The AudioContext is a lazy singleton, constructed on the first enabled play.
// Every play happens inside a user-gesture handler (the capture listeners
// below), so the browser's autoplay policy is satisfied by construction.
// Everything degrades to silence: no AudioContext, a throwing constructor, a
// suspended context that won't resume — none of it may break the board.
//
// Sounds are deliberately NOT motion: the sheet's prefers-reduced-motion rule
// kills the press-flash one-shot along with every other animation, but audio
// is governed only by the ♪ SFX toggle (kraken-sfx in localStorage).
//
// The artifact iframe is deliberately outside all of this: the listeners
// attach to the top document only, and events never cross the iframe
// boundary. (The overlay does reach into that frame's document for keys —
// staying out of it is this module's choice, not something the boundary
// imposes.)

import { MOUSE_ACTIVATION, TOUCH_ACTIVATION } from "../hooks/useDragReorder.ts";
import { readLS } from "../hooks/useLocalStorage.ts";

export type SoundName = "key" | "click" | "enter" | "toggle";

interface Pulse {
  /** oscillator frequency, Hz — fixed pitch, no jitter, no RNG anywhere */
  freq: number;
  /** seconds from the pulse's own start to its hard cut */
  dur: number;
  /** seconds after the sound's start this pulse begins */
  at: number;
  /** peak envelope gain, pre-master */
  gain: number;
}

/** One shared output level; the per-pulse gains above ride through this. */
export const MASTER_GAIN = 0.12;

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
export interface SfxAudioContext {
  currentTime: number;
  state?: string;
  destination: unknown;
  resume?(): unknown;
  createOscillator(): SfxOscillator;
  createGain(): SfxGainNode;
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
      m.gain.value = MASTER_GAIN;
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

/** One square pulse through the flat-top envelope, onto the master gain. */
function pulse(c: SfxAudioContext, m: SfxGainNode, p: Pulse, t0: number): void {
  const start = t0 + p.at;
  const osc = c.createOscillator();
  osc.type = "square";
  osc.frequency.setValueAtTime(p.freq, start);
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

// ── event classification ─────────────────────────────────────────────────────

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

/** Swap the AudioContext factory (null restores the real one); drops the singleton. */
export function injectAudioContextFactoryForTest(
  f: (() => SfxAudioContext | null) | null,
): void {
  ctxFactory = f ?? defaultFactory;
  ctx = null;
  master = null;
}

/** Full module reset: singleton, enabled flag, and both rate-limit watermarks. */
export function resetSfxForTest(): void {
  ctxFactory = defaultFactory;
  ctx = null;
  master = null;
  enabled = null;
  lastKeyAt = -Infinity;
  lastEnterAt = -Infinity;
}
