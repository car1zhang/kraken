// The ◈ accent cycle and the ▦ scanline toggle — the two HUD preferences the
// kit exposes as tweak props (`hudColor`, `showScanlines`). Both go through
// `window.krakenHud` and never stamp `<html>` themselves, for ThemeToggle's
// reason: the pre-paint IIFE in index.html owns the attributes (a
// React-mounted stamp would flash the defaults first), and two writers for
// one attribute is how the flash comes back. Labels are local state, same as
// the theme button: the getters are not reactive, and each button's own
// click is the only moment its value changes. With no `krakenHud` at all
// (the pre-paint script never ran) a click is a full no-op — label included,
// because a label that flips while the board doesn't is a button that lies.
// That is a DELIBERATE departure from ThemeToggle, whose no-API cycle stays
// label-only: a theme label means something without the API, an accent or FX
// label does not.

import { useState } from "react";

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

export function ScanToggle() {
  const [on, setOn] = useState<boolean>(
    () => window.krakenHud?.getScanlines() ?? true,
  );
  return (
    <button
      className="theme-btn"
      id="scanlines-toggle"
      title="CRT scanlines + sweep on/off"
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
