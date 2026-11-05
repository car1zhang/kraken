// What a character on the agent map wears: a coat in its model's colour and,
// for a crew member, a hat for its role. The coats are candidate set A's
// (artifacts/12-model-avatars.html); none of them sits on a status colour, so
// a character's body never reads as its card's state. The drawing lives in
// draw.ts (`coatColors`, the `HAT` table); this file is the mapping alone, so the
// two rules can be read, and tested, without a canvas.

import type { WebAgent } from "../../api/types.ts";

export type Coat = "fable" | "opus" | "sonnet" | "haiku" | "codex";

/**
 * The coat a model id wears. Case-insensitive, first match wins: a fable id,
 * then opus (Opus 5 and Opus 5.5 share a coat), sonnet, haiku, and the codex
 * family (`gpt`, `codex`, `astra`). Anything else, empty or absent is
 * `fable`: the yellow the map shipped with, so a character whose model the
 * payload does not name looks exactly as it did before coats existed.
 */
export function modelCoat(model: string | undefined): Coat {
  const m = (model ?? "").toLowerCase();
  if (!m) return "fable";
  if (m.includes("fable")) return "fable";
  if (m.includes("opus")) return "opus";
  if (m.includes("sonnet")) return "sonnet";
  if (m.includes("haiku")) return "haiku";
  if (m.includes("gpt") || m.includes("codex") || m.includes("astra")) return "codex";
  return "fable";
}

/** Each coat's body colour, candidate set A's. */
export const COAT_HUE: Readonly<Record<Coat, string>> = {
  fable: "#ffd21f",
  opus: "#6aaef5",
  sonnet: "#f1ead9",
  haiku: "#4fc7b9",
  codex: "#4b5263",
};

export type Hat = "hardhat" | "helmet" | "wings" | "cap" | "beanie" | "none";

/**
 * The hat a crew member wears for its role. A background monitor or shell
 * wears none, an in-process subagent a beanie, and a teammate (or an entry
 * from a server too old to send `kind`) is known by its name: hephaestus
 * builds (hard hat), athena reviews (helmet), hermes renders (winged cap),
 * and anyone else gets a plain cap. The lead wears no hat; it is never asked.
 */
export function roleHat(agent: WebAgent): Hat {
  const kind = agent.kind ?? "";
  if (kind === "monitor" || kind === "shell") return "none";
  if (kind === "subagent") return "beanie";
  const name = (agent.name ?? "").toLowerCase();
  if (name.startsWith("hephaestus")) return "hardhat";
  if (name.startsWith("athena")) return "helmet";
  if (name.startsWith("hermes")) return "wings";
  return "cap";
}
