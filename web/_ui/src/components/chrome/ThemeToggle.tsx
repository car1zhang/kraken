import { useState } from "react";

/** system → light → dark → system. Ported from app.js:3993. */
const THEME_CYCLE = { system: "light", light: "dark", dark: "system" } as const;

type Pref = keyof typeof THEME_CYCLE;

function currentPref(): Pref {
  return window.krakenTheme?.get() ?? "system";
}

/**
 * The ◐ theme button.
 *
 * It goes through `window.krakenTheme` and does NOT resolve the theme itself.
 * That API is defined by the pre-paint IIFE inlined in index.html's head,
 * which has to run before the stylesheet applies — something no React code can
 * do — and which also owns the `prefers-color-scheme` listener that re-stamps
 * `<html data-theme>` while the preference is "system". Reimplementing any of
 * that here would give the page two writers for one attribute.
 *
 * The label is local state rather than derived: `get()` is not reactive, so
 * the only moment it can change is this button's own click.
 */
export function ThemeToggle() {
  const [pref, setPref] = useState<Pref>(currentPref);
  return (
    <button
      className="theme-btn"
      id="theme-toggle"
      title="theme: system → light → dark"
      onClick={() => {
        const next = THEME_CYCLE[pref];
        window.krakenTheme?.set(next);
        // Read back rather than trusting `next`: krakenTheme normalizes
        // anything it does not recognize to "system", and the button must say
        // what actually happened. With no krakenTheme at all (a page whose
        // head IIFE never ran) there is nothing to read back from, and the
        // cycle degrades to a label-only one rather than to a dead button.
        setPref(window.krakenTheme ? currentPref() : next);
      }}
    >
      ◐ {pref.toUpperCase()}
    </button>
  );
}
