import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  injectAudioContextFactoryForTest,
  installSfxListeners,
  resetSfxForTest,
  sfxEnabled,
  sfxVolume,
  type SfxAudioContext,
} from "../../lib/sfx.ts";
import { SfxToggle } from "./HudControls.tsx";

// SfxToggle — the one HUD sound button: volume cycle and mute in a single
// control. Unlike its siblings it rides localStorage directly (no krakenHud,
// no pre-paint IIFE involvement), so the pins here are about the label, its
// two persisted keys (kraken-sfx the mute flag, kraken-sfx-vol the level),
// the sfx module's runtime state staying in step, and — because two of the
// handler's comments rest on it — the document capture listener firing
// before React's onClick.

beforeEach(() => {
  localStorage.clear();
  resetSfxForTest();
});
afterEach(() => {
  cleanup();
  resetSfxForTest();
});

describe("SfxToggle", () => {
  it("cycles 100 → 75 → 50 → 25 → OFF → 100 → 75, keeping label, both stored keys, and module state in step", async () => {
    render(<SfxToggle />);
    const btn = screen.getByRole("button", { name: /SFX 100%/ });
    for (const next of [75, 50, 25]) {
      await userEvent.click(btn);
      expect(btn).toHaveTextContent(`SFX ${next}%`);
      expect(localStorage.getItem("kraken-sfx-vol")).toBe(String(next));
      expect(sfxVolume()).toBe(next);
      // the step branch must not touch the mute flag — never written on this
      // path, so still absent, not "1"
      expect(localStorage.getItem("kraken-sfx")).toBeNull();
    }
    // past the quietest step the cycle mutes: the flag flips, the level
    // stays — in the module's runtime state as well as in storage
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX OFF");
    expect(localStorage.getItem("kraken-sfx")).toBe("0");
    expect(localStorage.getItem("kraken-sfx-vol")).toBe("25");
    expect(sfxVolume()).toBe(25);
    expect(sfxEnabled()).toBe(false);
    // OFF re-enters the cycle at its head, at full
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX 100%");
    expect(localStorage.getItem("kraken-sfx")).toBe("1");
    expect(localStorage.getItem("kraken-sfx-vol")).toBe("100");
    expect(sfxEnabled()).toBe(true);
    expect(sfxVolume()).toBe(100);
    // and the cycle resumes normally after the mute round-trip
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX 75%");
    expect(sfxVolume()).toBe(75);
  });

  it("mounts OFF from a persisted kraken-sfx=0, whatever level is stored", () => {
    localStorage.setItem("kraken-sfx", "0");
    localStorage.setItem("kraken-sfx-vol", "50");
    render(<SfxToggle />);
    screen.getByRole("button", { name: /SFX OFF/ });
  });

  it('mounts OFF from any non-"1" flag — the decode is === "1", not truthy', () => {
    localStorage.setItem("kraken-sfx", "true");
    render(<SfxToggle />);
    screen.getByRole("button", { name: /SFX OFF/ });
  });

  it("clicks out of a hand-stored 0 to the cycle head", async () => {
    localStorage.setItem("kraken-sfx-vol", "0");
    render(<SfxToggle />);
    const btn = screen.getByRole("button", { name: /SFX 0%/ });
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX 100%");
    expect(sfxVolume()).toBe(100);
  });

  it("mounts at a persisted level", () => {
    localStorage.setItem("kraken-sfx-vol", "50");
    render(<SfxToggle />);
    screen.getByRole("button", { name: /SFX 50%/ });
  });

  it("shows a hand-edited off-cycle value as decoded and snaps to the cycle head on click", async () => {
    localStorage.setItem("kraken-sfx-vol", "60");
    render(<SfxToggle />);
    const btn = screen.getByRole("button", { name: /SFX 60%/ });
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX 100%");
    expect(localStorage.getItem("kraken-sfx-vol")).toBe("100");
    // the module heard the snap too, not just the label and the key
    expect(sfxVolume()).toBe(100);
  });

  it("renders garbage through the shared decode instead of echoing it", () => {
    localStorage.setItem("kraken-sfx-vol", "abc");
    render(<SfxToggle />);
    screen.getByRole("button", { name: /SFX 100%/ });
  });

  it("orders correctly around the document capture listener: mute click still sounds, re-enable confirm is heard", async () => {
    // The handler's soundness rests on the capture listener running BEFORE
    // React's bubble-phase onClick: the mute click is sounded while the
    // module is still enabled (so the mute branch rightly adds no confirm),
    // and the re-enable click reaches the capture listener while still
    // disabled (so the explicit confirm is what makes it audible at all).
    // If the attachment phase ever changed, every other test would stay
    // green — this one pins the ordering by counting scheduled oscillators.
    let oscs = 0;
    const ctx: SfxAudioContext = {
      currentTime: 0,
      sampleRate: 44100,
      state: "running",
      destination: {},
      createBiquadFilter: () => ({ type: "", frequency: { value: 0 }, connect: () => {} }),
      createConvolver: () => ({ buffer: null, connect: () => {} }),
      createBuffer: (_c, frames) => ({ getChannelData: () => new Float32Array(frames) }),
      createOscillator: () => {
        oscs++;
        return {
          type: "",
          frequency: { setValueAtTime: () => {} },
          connect: () => {},
          start: () => {},
          stop: () => {},
        };
      },
      createGain: () => ({
        gain: {
          value: 0,
          setValueAtTime: () => {},
          linearRampToValueAtTime: () => {},
        },
        connect: () => {},
      }),
    };
    injectAudioContextFactoryForTest(() => ctx);
    const uninstall = installSfxListeners(document);
    try {
      localStorage.setItem("kraken-sfx-vol", "25");
      render(<SfxToggle />);
      const btn = screen.getByRole("button", { name: /SFX 25%/ });
      // 25 → OFF: the capture listener sounds the two-pulse click while the
      // module is still enabled; the mute branch adds nothing
      await userEvent.click(btn);
      expect(btn).toHaveTextContent("SFX OFF");
      expect(oscs).toBe(2);
      // OFF → 100: the capture listener's click lands disabled and plays
      // nothing; the handler's one-pulse confirm is the only voice
      await userEvent.click(btn);
      expect(btn).toHaveTextContent("SFX 100%");
      expect(oscs).toBe(3);
    } finally {
      uninstall();
    }
  });
});
