import { readFileSync } from "node:fs";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ACTIONABLE_SELECTOR,
  ATTACK_S,
  actionableFrom,
  FLASH_CLEANUP_MS,
  injectAudioContextFactoryForTest,
  installSfxListeners,
  KEY_RATE_LIMIT_MS,
  keySound,
  MASTER_GAIN,
  playSfx,
  resetSfxForTest,
  setSfxEnabled,
  sfxEnabled,
  SOUNDS,
  type SfxAudioContext,
  type SfxGainNode,
  type SfxOscillator,
} from "./sfx.ts";

// A recording fake for the structural AudioContext slice sfx.ts declares —
// every scheduled oscillator and gain call lands in `oscs`/`gains` so the
// tests can assert the locked spec's exact parameters without an audio device.
interface RecordedOsc {
  type: string;
  freqCalls: Array<[number, number]>;
  started: number[];
  stopped: number[];
}
interface RecordedGain {
  value: number;
  sets: Array<[number, number]>;
  ramps: Array<[number, number]>;
}

function fakeContext() {
  const oscs: RecordedOsc[] = [];
  const gains: RecordedGain[] = [];
  const ctx: SfxAudioContext = {
    currentTime: 10,
    state: "running",
    destination: { sink: true },
    createOscillator(): SfxOscillator {
      const rec: RecordedOsc = { type: "", freqCalls: [], started: [], stopped: [] };
      oscs.push(rec);
      return {
        get type() {
          return rec.type;
        },
        set type(v: string) {
          rec.type = v;
        },
        frequency: {
          setValueAtTime: (v, t) => {
            rec.freqCalls.push([v, t]);
          },
        },
        connect: () => {},
        start: (t) => {
          rec.started.push(t);
        },
        stop: (t) => {
          rec.stopped.push(t);
        },
      };
    },
    createGain(): SfxGainNode {
      const rec: RecordedGain = { value: NaN, sets: [], ramps: [] };
      gains.push(rec);
      return {
        gain: {
          get value() {
            return rec.value;
          },
          set value(v: number) {
            rec.value = v;
          },
          setValueAtTime: (v, t) => {
            rec.sets.push([v, t]);
          },
          linearRampToValueAtTime: (v, t) => {
            rec.ramps.push([v, t]);
          },
        },
        connect: () => {},
      };
    },
  };
  return { ctx, oscs, gains };
}

beforeEach(() => {
  resetSfxForTest();
  localStorage.clear();
});
afterEach(() => {
  resetSfxForTest();
  vi.restoreAllMocks();
});

describe("the locked spec", () => {
  it("is exactly the round-6 decision — fixed pitches, flat-top durations, tuned-down gains", () => {
    // NES SNAP with enter/click swapped and gains cut ~35%; deterministic,
    // no jitter, no RNG. Changing any value here is changing the spec.
    expect(SOUNDS).toEqual({
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
    });
    expect(MASTER_GAIN).toBe(0.12);
  });
});

describe("playSfx", () => {
  it("constructs no context and plays nothing while disabled", () => {
    const factory = vi.fn(() => fakeContext().ctx);
    injectAudioContextFactoryForTest(factory);
    setSfxEnabled(false);
    playSfx("click");
    playSfx("key");
    expect(factory).not.toHaveBeenCalled();
  });

  it("constructs exactly one context across plays, lazily", () => {
    const made: ReturnType<typeof fakeContext>[] = [];
    const factory = vi.fn(() => {
      const f = fakeContext();
      made.push(f);
      return f.ctx;
    });
    injectAudioContextFactoryForTest(factory);
    expect(factory).not.toHaveBeenCalled();
    playSfx("click");
    playSfx("toggle");
    playSfx("enter");
    expect(factory).toHaveBeenCalledTimes(1);
    expect(made).toHaveLength(1);
  });

  it("sets the master gain to 0.12 on construction", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    playSfx("toggle");
    // first created gain is the master
    expect(f.gains[0]?.value).toBe(MASTER_GAIN);
  });

  it("plays a click as the two-pulse confirm with square oscillators at the locked offsets", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    playSfx("click");
    // master + one gain per pulse
    expect(f.oscs).toHaveLength(2);
    expect(f.oscs.map((o) => o.type)).toEqual(["square", "square"]);
    // t0 = currentTime (10); pulses at +0 and +0.045
    expect(f.oscs[0]?.freqCalls).toEqual([[988, 10]]);
    expect(f.oscs[1]?.freqCalls).toEqual([[1319, 10.045]]);
    expect(f.oscs[0]?.started).toEqual([10]);
    expect(f.oscs[1]?.started).toEqual([10.045]);
  });

  it("shapes every voice as the flat-top envelope: attack to peak, sustain, cut to ~0 by dur", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    playSfx("key");
    const env = f.gains[1]; // gains[0] is master
    const [p] = SOUNDS.key;
    expect(env?.sets[0]).toEqual([0.0001, 10]);
    expect(env?.ramps[0]).toEqual([p?.gain, 10 + ATTACK_S]);
    // the flat-top hold, re-asserted just before the cut
    expect(env?.sets[1]?.[0]).toBe(p?.gain);
    // the cut lands at exactly dur, at near-zero gain
    const last = env?.ramps[env.ramps.length - 1];
    expect(last?.[0]).toBeLessThanOrEqual(0.001);
    expect(last?.[1]).toBeCloseTo(10 + (p?.dur ?? 0), 10);
    // and the oscillator itself stops just after
    const osc = f.oscs[0];
    expect(osc?.stopped[0]).toBeGreaterThanOrEqual(10 + (p?.dur ?? 0));
  });

  it("drops key ticks arriving faster than the rate limit instead of stacking them", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    let now = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    playSfx("key");
    now += KEY_RATE_LIMIT_MS - 1;
    playSfx("key"); // dropped
    now += KEY_RATE_LIMIT_MS;
    playSfx("key"); // plays
    expect(f.oscs).toHaveLength(2);
  });

  it("rate-limits enter like key — held Enter auto-repeats too (review r1, logic F2)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    vi.spyOn(performance, "now").mockReturnValue(1000);
    playSfx("enter");
    playSfx("enter"); // OS auto-repeat inside the window: dropped
    expect(f.oscs).toHaveLength(2); // one two-pulse blip, not two
  });

  it("key and enter hold separate watermarks — a trailing tick cannot swallow the confirm (review r2, edge 2.5)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    vi.spyOn(performance, "now").mockReturnValue(1000);
    playSfx("key");
    playSfx("enter"); // same instant: the confirm still plays
    expect(f.oscs).toHaveLength(3); // one tick + the two-pulse blip
  });

  it("does not rate-limit clicks — each is its own gesture", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    vi.spyOn(performance, "now").mockReturnValue(1000);
    playSfx("click");
    playSfx("click");
    expect(f.oscs).toHaveLength(4);
  });

  it("degrades to silence when no context can be constructed", () => {
    injectAudioContextFactoryForTest(() => null);
    expect(() => {
      playSfx("click");
    }).not.toThrow();
  });

  it("degrades to silence when createGain throws, and retries construction next play", () => {
    // review r1, logic F3a: ctx must not be committed with master null — that
    // made every later click throw out of the capture listener forever
    let threw = false;
    const f = fakeContext();
    const broken: typeof f.ctx = {
      ...f.ctx,
      createGain: () => {
        threw = true;
        throw new Error("InvalidStateError");
      },
    };
    let calls = 0;
    injectAudioContextFactoryForTest(() => (++calls === 1 ? broken : f.ctx));
    expect(() => {
      playSfx("click");
    }).not.toThrow();
    expect(threw).toBe(true);
    expect(f.oscs).toHaveLength(0);
    // the failed construction was not committed: the next play rebuilds
    playSfx("click");
    expect(f.oscs).toHaveLength(2);
  });

  it("resumes a suspended context before playing", () => {
    const f = fakeContext();
    f.ctx.state = "suspended";
    const resume = vi.fn();
    f.ctx.resume = resume;
    injectAudioContextFactoryForTest(() => f.ctx);
    playSfx("toggle");
    expect(resume).toHaveBeenCalled();
  });

  it("resumes an iOS 'interrupted' context too, and swallows a rejecting resume", async () => {
    const f = fakeContext();
    f.ctx.state = "interrupted";
    const resume = vi.fn(() => Promise.reject(new Error("InvalidStateError")));
    f.ctx.resume = resume;
    injectAudioContextFactoryForTest(() => f.ctx);
    expect(() => {
      playSfx("toggle");
    }).not.toThrow();
    expect(resume).toHaveBeenCalled();
    await Promise.resolve(); // an unhandled rejection here would fail the run
  });

  it("schedules nothing into an existing not-running context (frozen currentTime pile-up)", () => {
    // review r1, edge F4: fifty clicks against an interrupted context queued
    // a hundred voices at one frozen timestamp, all firing at once on resume
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    playSfx("toggle"); // constructs, running: schedules
    expect(f.oscs).toHaveLength(1);
    f.ctx.state = "interrupted";
    playSfx("click");
    playSfx("click");
    expect(f.oscs).toHaveLength(1); // nothing new queued
    f.ctx.state = "running";
    playSfx("click");
    expect(f.oscs).toHaveLength(3); // recovers once actually running
  });
});

describe("enabled flag", () => {
  it("defaults ON with no stored flag", () => {
    expect(sfxEnabled()).toBe(true);
  });
  it("reads a stored kraken-sfx=0 as OFF", () => {
    localStorage.setItem("kraken-sfx", "0");
    expect(sfxEnabled()).toBe(false);
  });
  it("setSfxEnabled flips the runtime flag", () => {
    setSfxEnabled(false);
    expect(sfxEnabled()).toBe(false);
    setSfxEnabled(true);
    expect(sfxEnabled()).toBe(true);
  });
});

describe("classifiers", () => {
  it("maps clicks: actionable targets (and their descendants) yes, panel bodies no", () => {
    document.body.innerHTML = `
      <div id="panel"><button id="b"><span id="inner">STEER</span></button></div>
      <div class="card editable" id="card"><span class="slug" id="slug">demo</span></div>
      <a href="#x" id="link">log</a>
      <input type="checkbox" id="cb">
      <input type="text" id="txt">
      <div role="button" id="rb">fake</div>`;
    const el = (id: string) => document.getElementById(id) as Element;
    expect(actionableFrom(el("b"))?.id).toBe("b");
    expect(actionableFrom(el("inner"))?.id).toBe("b"); // closest() walks up
    expect(actionableFrom(el("slug"))?.id).toBe("card");
    expect(actionableFrom(el("link"))?.id).toBe("link");
    expect(actionableFrom(el("cb"))?.id).toBe("cb");
    expect(actionableFrom(el("rb"))?.id).toBe("rb");
    // focusing a text field is not acting — its feedback is the key ticks
    expect(actionableFrom(el("txt"))).toBeNull();
    expect(actionableFrom(el("panel"))).toBeNull();
    expect(actionableFrom(null)).toBeNull();
    expect(actionableFrom(document)).toBeNull();
  });

  it("maps keydowns: printable and Backspace tick, Enter confirms, chords and non-text are silent", () => {
    document.body.innerHTML = `<textarea id="ta"></textarea><input id="bare"><input type="search" id="s"><button id="b"></button>`;
    const on = (id: string, init: KeyboardEventInit) => {
      const e = new KeyboardEvent("keydown", init);
      Object.defineProperty(e, "target", { value: document.getElementById(id) });
      return keySound(e);
    };
    expect(on("ta", { key: "r" })).toBe("key");
    expect(on("ta", { key: "Backspace" })).toBe("key");
    expect(on("ta", { key: "Enter" })).toBe("enter");
    // a bare <input> normalizes .type to "text" — the five bare fields the
    // cursor round taught us about are covered without :not([type])
    expect(on("bare", { key: "x" })).toBe("key");
    expect(on("s", { key: "x" })).toBe("key");
    expect(on("ta", { key: "r", metaKey: true })).toBeNull();
    expect(on("ta", { key: "r", ctrlKey: true })).toBeNull();
    expect(on("ta", { key: "r", altKey: true })).toBeNull();
    expect(on("ta", { key: "Shift" })).toBeNull();
    expect(on("ta", { key: "ArrowLeft" })).toBeNull();
    expect(on("ta", { key: "Escape" })).toBeNull();
    expect(on("b", { key: "r" })).toBeNull();
    // ⌘/Ctrl+Enter is the composer's actual submit — Enter outranks the
    // modifier bail (review r1, edge F6)
    expect(on("ta", { key: "Enter", metaKey: true })).toBe("enter");
    expect(on("ta", { key: "Enter", ctrlKey: true })).toBe("enter");
    // IME composition (review r1, edge F2): a composing Enter commits a
    // candidate, confirms nothing; Blink's key="Process" stays silent
    expect(on("ta", { key: "Enter", isComposing: true })).toBeNull();
    expect(on("ta", { key: "a", isComposing: true })).toBeNull();
    expect(on("ta", { key: "Process" })).toBeNull();
  });

  it("keeps text inputs out of the actionable selector", () => {
    // belt and braces for the rule above: the selector itself must never
    // grow a bare `input` term, which would make focusing a field click
    expect(ACTIONABLE_SELECTOR).not.toMatch(/(^|, )input(,|$)/);
  });
});

describe("installSfxListeners", () => {
  it("wires click → two-pulse confirm + press-flash class on the actionable element", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<button id="b"><span id="inner">GO</span></button>`;
    const un = installSfxListeners();
    const btn = document.getElementById("b") as HTMLElement;
    document.getElementById("inner")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(f.oscs).toHaveLength(2); // the click confirm
    expect(btn.classList.contains("press-flash")).toBe(true);
    un();
  });

  it("keeps non-actionable clicks and non-text keydowns fully silent", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div id="panel"></div>`;
    const un = installSfxListeners();
    document.getElementById("panel")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true }));
    expect(f.oscs).toHaveLength(0);
    un();
  });

  it("wires textarea keydowns to key ticks", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<textarea id="ta"></textarea>`;
    const un = installSfxListeners();
    document
      .getElementById("ta")
      ?.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true }));
    expect(f.oscs).toHaveLength(1);
    expect(f.oscs[0]?.freqCalls[0]?.[0]).toBe(988);
    un();
  });

  it("uninstalls all three listeners", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div class="card" id="c"></div><textarea id="ta"></textarea>`;
    const un = installSfxListeners();
    un();
    const card = document.getElementById("c") as HTMLElement;
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
    document
      .getElementById("ta")
      ?.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true }));
    expect(f.oscs).toHaveLength(0);
    // pointerdown is uninstalled too: a stranded one would record this press
    // and the re-installed listeners' travel guard would then silence the
    // far-away click below — sound here proves the old listener is gone
    card.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: 10, clientY: 10 }));
    const re = installSfxListeners();
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 500, clientY: 10 }));
    expect(f.oscs).toHaveLength(2);
    re();
  });

  it("re-fires the press-flash on rapid repeat clicks (remove → reflow → add)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<button id="b"></button>`;
    const un = installSfxListeners();
    const btn = document.getElementById("b") as HTMLElement;
    btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(btn.classList.contains("press-flash")).toBe(true);
    btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(btn.classList.contains("press-flash")).toBe(true);
    // animationend clears it back to the element's found state
    btn.dispatchEvent(new Event("animationend"));
    expect(btn.classList.contains("press-flash")).toBe(false);
    un();
  });

  it("clears the flash via the timer backstop when no animation ever runs (review r1, edge F3)", () => {
    vi.useFakeTimers();
    try {
      const f = fakeContext();
      injectAudioContextFactoryForTest(() => f.ctx);
      document.body.innerHTML = `<button id="b"></button>`;
      const un = installSfxListeners();
      const btn = document.getElementById("b") as HTMLElement;
      // reduced motion: no animation ever starts, no animation event ever
      // fires — the timer is the only exit, and it must retire the listener
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      expect(btn.classList.contains("press-flash")).toBe(true);
      vi.runAllTimers();
      expect(btn.classList.contains("press-flash")).toBe(false);
      un();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a stale animationcancel cannot strip a restarted flash (review r2, logic D1 / edge 2.2)", () => {
    vi.useFakeTimers();
    try {
      const f = fakeContext();
      injectAudioContextFactoryForTest(() => f.ctx);
      document.body.innerHTML = `<button id="b"></button>`;
      const un = installSfxListeners();
      const btn = document.getElementById("b") as HTMLElement;
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true })); // restart inside 140ms
      // the restart's classList.remove cancelled the first animation; its
      // queued animationcancel dispatches AFTER the second flash is armed —
      // it must not kill the flash that just started
      btn.dispatchEvent(new Event("animationcancel"));
      expect(btn.classList.contains("press-flash")).toBe(true);
      // the live flash still ends normally
      btn.dispatchEvent(new Event("animationend"));
      expect(btn.classList.contains("press-flash")).toBe(false);
      un();
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores a descendant's own animation events (they bubble)", () => {
    vi.useFakeTimers();
    try {
      const f = fakeContext();
      injectAudioContextFactoryForTest(() => f.ctx);
      document.body.innerHTML = `<button id="b"><span id="inner">GO</span></button>`;
      const un = installSfxListeners();
      const btn = document.getElementById("b") as HTMLElement;
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      // .btn:active's glitch ending on a child must not clear the flash
      document.getElementById("inner")?.dispatchEvent(new Event("animationend", { bubbles: true }));
      expect(btn.classList.contains("press-flash")).toBe(true);
      btn.dispatchEvent(new Event("animationend"));
      expect(btn.classList.contains("press-flash")).toBe(false);
      un();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stays silent on a card click that ends a text selection (review r1, edge F1)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div class="card" id="c">select me</div><button id="b">chip</button>`;
    const un = installSfxListeners();
    const sel = window.getSelection();
    sel?.selectAllChildren(document.getElementById("c") as Node);
    document.getElementById("c")?.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
    expect(f.oscs).toHaveLength(0);
    // the guard is card-scoped: a live selection elsewhere must not silence
    // a button whose mousedown was prevented (CmdComposer's chips do that)
    document.getElementById("b")?.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
    expect(f.oscs).toHaveLength(2);
    sel?.removeAllRanges();
    un();
  });

  it("stays silent on a drag's synthetic click — pointerdown→click travel past the sensor distance (review r1, edge F1)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div class="card" id="c"></div>`;
    const un = installSfxListeners();
    const card = document.getElementById("c") as HTMLElement;
    // jsdom has no PointerEvent ctor; the handler reads only clientX/clientY
    // (and pointerType, absent here → mouse threshold), which MouseEvent
    // carries, and dispatch routes by the type string
    card.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: 10, clientY: 10 }));
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 210, clientY: 10 }));
    expect(f.oscs).toHaveLength(0);
    expect(card.classList.contains("press-flash")).toBe(false);
    // a stationary press-release on the same card still sounds
    card.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: 10, clientY: 10 }));
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 12, clientY: 11 }));
    expect(f.oscs).toHaveLength(2);
    un();
  });

  it("never judges a keyboard-activated click by the last mouse press (review r2, logic D2 / edge 2.1)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div class="card" id="c"></div>`;
    const un = installSfxListeners();
    const card = document.getElementById("c") as HTMLElement;
    // a mouse press lands far away, then a keyboard activation (detail 0,
    // coords 0,0) — the stale press must not silence it
    card.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: 640, clientY: 300 }));
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 0, clientX: 0, clientY: 0 }));
    expect(f.oscs).toHaveLength(2);
    un();
  });

  it("consumes each pointerdown after one click — a stale press never judges a later gesture (review r2, edge 2.1)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div class="card" id="c"></div>`;
    const un = installSfxListeners();
    const card = document.getElementById("c") as HTMLElement;
    card.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: 10, clientY: 10 }));
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 300, clientY: 10 })); // drag: silent
    expect(f.oscs).toHaveLength(0);
    // next click arrives with no pointerdown of its own — nothing to measure
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 300, clientY: 10 }));
    expect(f.oscs).toHaveLength(2);
    un();
  });

  it("applies the touch tolerance, not the mouse distance, to touch presses (review r2, edge 2.3)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<div class="card" id="c"></div>`;
    const un = installSfxListeners();
    const card = document.getElementById("c") as HTMLElement;
    // a 6px thumb wobble: over the 4px mouse distance, under the 8px touch
    // tolerance — a genuine tap, and it must sound
    const pd = new MouseEvent("pointerdown", { bubbles: true, clientX: 10, clientY: 10 });
    Object.defineProperty(pd, "pointerType", { value: "touch" });
    card.dispatchEvent(pd);
    card.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 16, clientY: 10 }));
    expect(f.oscs).toHaveLength(2);
    un();
  });

  it("does not apply the drag-travel guard to elements that cannot drag (review r2, logic D3)", () => {
    const f = fakeContext();
    injectAudioContextFactoryForTest(() => f.ctx);
    document.body.innerHTML = `<button id="b">STEER</button>`;
    const un = installSfxListeners();
    const btn = document.getElementById("b") as HTMLElement;
    // 5px of trackpad drift on a button — ordinary click travel, and a
    // button cannot start a drag, so there is nothing to guard against
    btn.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientX: 10, clientY: 10 }));
    btn.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1, clientX: 15, clientY: 10 }));
    expect(f.oscs).toHaveLength(2);
    un();
  });
});

describe("the flash backstop tracks the sheet", () => {
  it("FLASH_CLEANUP_MS stays above the pressflash duration in style.css", () => {
    // same stance as motion.test.ts: the dependency crosses two files owned
    // by different concerns, so the pin reads the sheet as source text
    // comments stripped first, motion.test.ts's reason: this sheet's comments
    // quote CSS constantly, and a pin matching prose would dictate wording
    const css = readFileSync("src/styles/style.css", "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    const m = css.match(/animation:\s*pressflash\s+(\d+)ms/);
    expect(m, "pressflash animation not found in style.css").not.toBeNull();
    expect(Number(m?.[1])).toBeLessThan(FLASH_CLEANUP_MS);
  });
});
