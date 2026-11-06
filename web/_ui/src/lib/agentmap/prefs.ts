// The characters' one preference, shared by every surface that draws them:
// the agent map (which owns the switch) and the steer modal's cast strip.

import { readLS, writeLS } from "../../hooks/useLocalStorage.ts";

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
