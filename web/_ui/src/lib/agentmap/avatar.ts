// What a character on the agent map wears: a coat in its model's colour and,
// for a crew member, a hat for its role. The coats are candidate set A's
// (artifacts/12-model-avatars.html); none of them sits on a status colour, so
// a character's body never reads as its card's state. The drawing lives in
// draw.ts (`coatColors`, the `HAT` table); this file is the mapping alone, so the
// two rules can be read, and tested, without a canvas. The third thing a
// character may wear, a girl's round glasses and ponytail, is picked by name
// in scene.ts (`isGirl`), beside the name hash it uses. A side thread is a
// character too (a branch): which look and coat it takes is mapped here.

import type { WebAgent, WebThread } from "../../api/types.ts";

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

/**
 * How a character is drawn when it is a BRANCH: a side thread the operator
 * forked off the lead to ask about the work (artifacts/22-branched-nailongs.html,
 * look A). `pane` is a branch with its own tmux pane: an ordinary character
 * with a small forked twig on its head where a crew member wears a hat.
 * `headless` is a branch with no pane, answered one run per question: the
 * same twig on a ghost, translucent and floating, with a wisp for feet.
 * `none` is everyone else.
 */
export type Branch = "none" | "pane" | "headless";

/** The branch look a side thread takes: headless for a `headless` thread, pane for any other kind (an older server sends none). */
export function threadBranch(t: Pick<WebThread, "kind">): Exclude<Branch, "none"> {
  return t.kind === "headless" ? "headless" : "pane";
}

/**
 * The coat a side thread wears: its own model's when it was branched on one
 * (the wire carries the alias: haiku, sonnet, opus or fable), else Opus blue,
 * the model a branch runs on when none was picked.
 */
export function threadCoat(t: Pick<WebThread, "model">): Coat {
  return t.model ? modelCoat(t.model) : "opus";
}

/** How many branches one card draws at most; see `shownThreads`. */
export const BRANCH_MAX = 4;

/**
 * The side threads a card draws, in drawing order: only live ones (a closed
 * branch is not drawn), those with a pane before the headless ones, newest
 * first within each, and at most BRANCH_MAX. A headless thread stays live
 * until it is killed, so a long-running job can hold many; the cap keeps
 * them from crowding the crew off the card.
 */
export function shownThreads(threads: readonly WebThread[] | undefined): WebThread[] {
  const live = (threads ?? []).filter((t) => t.state === "live" && !!t.tid);
  const rank = (t: WebThread) => (threadBranch(t) === "pane" ? 0 : 1);
  return live
    .slice()
    .sort((a, b) => rank(a) - rank(b) || (b.created_at ?? 0) - (a.created_at ?? 0) || (a.tid < b.tid ? -1 : 1))
    .slice(0, BRANCH_MAX);
}
