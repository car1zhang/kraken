// The agent map's per-browser preferences. The characters' switch is also
// read by the steer modal's cast strip.

import { readLS, writeLS } from "../../hooks/useLocalStorage.ts";
import { validDrawColor, type DrawColor } from "./drawings.ts";

/** Per browser: "off" when the operator switched the characters off; anything else is on. */
export const MAP_ACTORS_KEY = "kraken.mapActors";

/** Whether the characters are drawn in this browser. On unless switched off, and on where storage cannot be read. */
export function actorsShown(): boolean {
  return readLS(MAP_ACTORS_KEY) !== "off";
}

/** Record the switch: "off" when off, the key removed when on. */
export function setActorsShown(on: boolean): void {
  writeLS(MAP_ACTORS_KEY, on ? null : "off");
}

/**
 * How the map treats done jobs: `show` draws them like any other card, `fade`
 * draws them translucent, `hide` leaves them off the map altogether.
 */
export type DoneMode = "show" | "fade" | "hide";

/** The three levels in the order the map's `done` button steps through them. */
const DONE_MODES: readonly DoneMode[] = ["show", "fade", "hide"];

/** Per browser: "show" or "hide" when the operator chose one; absent or anything else is `fade`. */
export const MAP_DONE_KEY = "kraken.mapDone";

/** The level chosen in this browser. `fade` unless another was chosen, and `fade` where storage cannot be read. */
export function doneMode(): DoneMode {
  const v = readLS(MAP_DONE_KEY);
  return v === "show" || v === "hide" ? v : "fade";
}

/** Record the level: its name, or the key removed for `fade`. */
export function setDoneMode(m: DoneMode): void {
  writeLS(MAP_DONE_KEY, m === "fade" ? null : m);
}

/** The level one step on from `m`, wrapping from `hide` back to `show`. */
export function nextDoneMode(m: DoneMode): DoneMode {
  return DONE_MODES[(DONE_MODES.indexOf(m) + 1) % DONE_MODES.length]!;
}

/** Per browser: "open" while the map's drawing-tool panel is open; anything else is closed. */
export const MAP_TOOLS_KEY = "kraken.mapTools";

/** Whether the drawing-tool panel is open in this browser. Closed unless opened, and closed where storage cannot be read. */
export function toolsShown(): boolean {
  return readLS(MAP_TOOLS_KEY) === "open";
}

/** Record the panel: "open" when open, the key removed when closed. */
export function setToolsShown(open: boolean): void {
  writeLS(MAP_TOOLS_KEY, open ? "open" : null);
}

/** Per browser: the ink new drawings take, a `DrawColor` name; absent or anything else is the theme's foreground. */
export const MAP_INK_KEY = "kraken.mapInk";

/** The ink chosen in this browser, or null for the theme's foreground (and where storage cannot be read). */
export function inkChosen(): DrawColor | null {
  const v = readLS(MAP_INK_KEY);
  return validDrawColor(v) ? v : null;
}

/** Record the ink: its name, or the key removed for the theme's foreground. */
export function setInkChosen(c: DrawColor | null): void {
  writeLS(MAP_INK_KEY, c);
}
