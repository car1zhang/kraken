// The three HUD preference buttons: the ◈ accent cycle and the ▦ scanline
// toggle — the two the kit exposes as tweak props (`hudColor`,
// `showScanlines`) — plus the ♪ SFX toggle, which follows a different rule
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
// accent or FX label does not. SfxToggle is outside all of it — no krakenHud,
// no pre-paint concern — so the no-op rule does not bind it.

import { useState } from "react";

import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { playSfx, setSfxEnabled } from "../../lib/sfx.ts";

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
  // Unlike its two siblings this one rides localStorage directly, not
  // window.krakenHud: sound has no pre-paint flash concern, so the pre-paint
  // IIFE stays out of it. This hook is the key's only writer ("1"/"0"), and
  // sfxEnabled() decodes those two values identically — that shared decode,
  // not the shared key, is what keeps label and audio agreeing at mount.
  const [on, setOn] = useLocalStorageFlag("kraken-sfx", true);
  return (
    <button
      className="theme-btn"
      id="sfx-toggle"
      title="click/typing sounds on/off"
      onClick={() => {
        const next = !on;
        setOn(next);
        setSfxEnabled(next);
        // audible confirm on enable only — the document capture listener
        // already sounded this click while the module was still enabled,
        // and re-enabling would otherwise land silently
        if (next) playSfx("toggle");
      }}
    >
      ♪ SFX {on ? "ON" : "OFF"}
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
