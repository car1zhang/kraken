// The three HUD preference buttons: the ◈ accent cycle and the ▦ scanline
// toggle — the two the kit exposes as tweak props (`hudColor`,
// `showScanlines`) — plus the ♪ SFX cycle, which follows a different rule
// stated at its own definition. The first two go through `window.krakenHud`
// and never stamp `<html>` themselves, for ThemeToggle's reason: the
// pre-paint IIFE in index.html owns the attributes (a React-mounted stamp
// would flash the defaults first), and two writers for one attribute is how
// the flash comes back. Labels are local state, same as the theme button:
// the getters are not reactive, and each button's own click is the only
// moment its value changes. With no `krakenHud` at all (the pre-paint script
// never ran) a click on either of those two is a full no-op — label
// included, because a label that flips while the board doesn't is a button
// that lies. That is a DELIBERATE departure from ThemeToggle, whose no-API
// cycle stays label-only: a theme label means something without the API, an
// accent or FX label does not. SfxToggle is outside all of it — no
// krakenHud, no pre-paint concern — so the no-op rule does not bind it.

import { useState } from "react";

import { useLocalStorage, useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import {
  decodeVolPercent,
  playSfx,
  setSfxEnabled,
  setSfxVolume,
  VOLUME_STEPS,
} from "../../lib/sfx.ts";

export function AccentToggle() {
  const [accent, setAccent] = useState<string>(
    () => window.krakenHud?.getAccent() ?? "violet",
  );
  return (
    <button
      className="theme-btn"
      id="accent-toggle"
      title="accent palette: violet → cyan → amber → crimson → green"
      onClick={() => {
        const hud = window.krakenHud;
        if (!hud) return;
        const cycle = hud.accents;
        const next = cycle[(cycle.indexOf(accent) + 1) % cycle.length] ?? "violet";
        hud.setAccent(next);
        // read back rather than trusting `next` — krakenHud normalizes
        // unknown names to violet, and the label must say what happened
        setAccent(hud.getAccent());
      }}
    >
      ◈ {accent.toUpperCase()}
    </button>
  );
}

export function SfxToggle() {
  // ONE button for the whole voice: 100% → 75% → 50% → 25% → OFF → 100%.
  // Unlike its two siblings it rides localStorage directly, not
  // window.krakenHud — sound has no pre-paint flash concern, so the pre-paint
  // IIFE stays out of it. It keeps the TWO stored keys the sfx module reads
  // rather than folding OFF into the percent: kraken-sfx stays the mute flag
  // ("1"/"0", decoded identically by sfxEnabled — that shared decode is what
  // keeps label and audio agreeing at mount) and kraken-sfx-vol the level
  // (decoded by decodeVolPercent, which the label renders through too, so a
  // hand-edited value can never make the label claim a volume the audio
  // isn't playing at). These hooks are each key's only writer.
  const [on, setOn] = useLocalStorageFlag("kraken-sfx", true);
  const [vol, setVol] = useLocalStorage("kraken-sfx-vol", "100");
  const pct = decodeVolPercent(vol);
  return (
    <button
      className="theme-btn"
      id="sfx-toggle"
      title="sfx volume: 100% → 75% → 50% → 25% → off"
      onClick={() => {
        if (!on) {
          // OFF → back to the head of the cycle, at full, whatever level was
          // stored: the cycle is deterministic, and a re-enable that came
          // back at a hand-edited 25 would read as the button not working
          setOn(true);
          setSfxEnabled(true);
          setVol("100");
          setSfxVolume(100);
          // audible confirm on enable — the document capture listener saw
          // this click while the module was still disabled and played
          // NOTHING, so without this explicit pulse re-enabling would land
          // silently
          playSfx("toggle");
          return;
        }
        const idx = VOLUME_STEPS.indexOf(pct);
        if (idx === VOLUME_STEPS.length - 1) {
          // past the quietest step the cycle mutes — the flag flips, the
          // stored level stays untouched
          setOn(false);
          setSfxEnabled(false);
          return;
        }
        // a stored value outside the cycle (hand-edited, or a future step
        // list) indexOfs to -1 and snaps to the head of the cycle
        const next = VOLUME_STEPS[idx + 1] ?? 100;
        setVol(String(next));
        setSfxVolume(next);
        // a second, lower confirm pulse that glides into the just-set level
        // over its first 15ms. (The capture listener's click for this same
        // press mostly renders there too: the master's glide starts in this
        // same task, well inside that motif.)
        playSfx("toggle");
      }}
    >
      ♪ SFX {on ? `${pct}%` : "OFF"}
    </button>
  );
}

export function ScanToggle() {
  const [on, setOn] = useState<boolean>(
    () => window.krakenHud?.getScanlines() ?? true,
  );
  return (
    <button
      className="theme-btn"
      id="scanlines-toggle"
      title="CRT scanlines on/off"
      onClick={() => {
        const hud = window.krakenHud;
        if (!hud) return;
        hud.setScanlines(!on);
        setOn(hud.getScanlines());
      }}
    >
      ▦ FX {on ? "ON" : "OFF"}
    </button>
  );
}
