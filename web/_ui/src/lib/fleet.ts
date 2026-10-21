import { ApiError } from "../api/client.ts";
import type { WebJob, WebJobOwner, WebNode } from "../api/types.ts";

/**
 * Multiplayer kraken, the board's half: the pure rules every fleet surface
 * (card, board bar, steer modal, chrome, Devices overlay) reads, kept in one
 * leaf so each is nameable and testable on its own.
 *
 * The one fact everything here keys on: a card is REMOTE iff it carries an
 * `owner`. The server sets that key only on cards it polled from another node
 * (`remoteCards`, internal/api/board.go) and never on its own, so the absence
 * is the wire's spelling of "mine" — nothing here compares node ids against
 * this node's.
 */

/** The three visibility words, in the order the steer modal's control draws them. */
export const VISIBILITIES = ["private", "readonly", "public"] as const;
export type Visibility = (typeof VISIBILITIES)[number];

/**
 * How a card may be driven from THIS board: `local` (every control, local
 * routes), `public` (every control, through `/api/nodes/action`), `readonly`
 * (seen, never touched). A remote card is public ONLY on the exact word —
 * anything else, including a missing key from a drifted node, reads readonly,
 * since failing closed is the one safe direction for a control that drives
 * someone else's cell.
 */
export type RemoteMode = "local" | "readonly" | "public";

export function isRemote(j: Pick<WebJob, "owner">): boolean {
  return j.owner !== undefined;
}

export function remoteMode(j: Pick<WebJob, "owner" | "visibility">): RemoteMode {
  if (!isRemote(j)) return "local";
  return j.visibility === "public" ? "public" : "readonly";
}

/** The owner chip: `studio-mini · Jess`, or the label alone with no operator. */
export function ownerChipText(o: WebJobOwner): string {
  return o.operator ? `${o.label} · ${o.operator}` : o.label;
}

/**
 * The board's fleet filter. `hideOthers` (the board bar's toggle) drops every
 * owned card; otherwise only the owned cards of nodes hidden in the Devices
 * overlay go. Local cards are never removed, and order is preserved — the
 * columns' server order is the reorder's ground truth.
 */
export function filterFleet(
  jobs: WebJob[],
  hideOthers: boolean,
  hiddenNodes: readonly string[],
): WebJob[] {
  if (hideOthers) return jobs.filter((j) => j.owner === undefined);
  if (hiddenNodes.length === 0) return jobs;
  const hidden = new Set(hiddenNodes);
  return jobs.filter((j) => j.owner === undefined || !hidden.has(j.owner.node_id));
}

/**
 * The chrome's `nodes N · M jobs` stat, or null for an empty registry — a lone
 * node's chrome reads exactly as it did before the fleet existed.
 */
export function nodesStat(nodes: WebNode[] | undefined, jobs: WebJob[]): string | null {
  if (!nodes || nodes.length === 0) return null;
  const owned = jobs.filter((j) => j.owner !== undefined).length;
  return `nodes ${nodes.length} · ${owned} jobs`;
}

/** True iff the gate refused the request (`401 {"error":"unauthorized"}`). */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401;
}

// The card identity lives in lib/jobKey.ts (it predates the fleet); re-exported
// here so a fleet consumer has one import for every fleet rule.
export { jobKey } from "./jobKey.ts";

// ------------------------------------------------------------- persistence

/** localStorage keys — per-browser preferences, never server state. */
export const HIDE_OTHERS_KEY = "kraken.hideOthers";
export const HIDDEN_NODES_KEY = "kraken.hiddenNodes";

/** Reads the hide-others flag; anything unreadable is the default (show). */
export function readHideOthers(): boolean {
  try {
    return window.localStorage.getItem(HIDE_OTHERS_KEY) === "true";
  } catch {
    return false;
  }
}

export function writeHideOthers(v: boolean): void {
  try {
    window.localStorage.setItem(HIDE_OTHERS_KEY, v ? "true" : "false");
  } catch {
    // private window / blocked storage: the toggle still works for this tab
  }
}

/** Reads the hidden node ids; a malformed value is the empty list. */
export function readHiddenNodes(): string[] {
  try {
    const raw = window.localStorage.getItem(HIDDEN_NODES_KEY);
    if (!raw) return [];
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) ? v.filter((s): s is string => typeof s === "string") : [];
  } catch {
    return [];
  }
}

export function writeHiddenNodes(ids: readonly string[]): void {
  try {
    window.localStorage.setItem(HIDDEN_NODES_KEY, JSON.stringify(ids));
  } catch {
    // see writeHideOthers
  }
}

/**
 * Who a remote job's own turns (its transcript, its threads, its load
 * estimate) are labelled as: the owner's operator, else its node label, else
 * its node id. This board's `operator` only for a card with no owner at all —
 * on a remote card it would put this board's name on the owner's words.
 */
export function ownerTurnLabel(
  owner: WebJobOwner | undefined,
  operator: string | undefined,
): string | undefined {
  if (!owner) return operator;
  return owner.operator?.trim() || owner.label?.trim() || owner.node_id;
}

/** U+FEFF, spelled by code point so no diff renders it as an empty string. */
const BOM = String.fromCharCode(0xfeff);

/**
 * A job file's spec body: the text after its `---` frontmatter block, with the
 * blank lines between the block and the body dropped. A remote card's spec
 * drawer renders this from the owner's raw file, since the form route that
 * splits the two locally is not one the owner serves to other nodes. Text that
 * does not open with a closed frontmatter block comes back unchanged.
 */
export function stripFrontmatter(text: string): string {
  // A leading UTF-8 BOM is skipped for the fence test only: with frontmatter
  // the body comes back without it, without frontmatter the text comes back
  // as given, BOM included.
  const body = text.startsWith(BOM) ? text.slice(1) : text;
  if (!body.startsWith("---\n") && !body.startsWith("---\r\n")) return text;
  const lines = body.split("\n");
  for (let i = 1; i < lines.length; i++) {
    if (lines[i] === "---" || lines[i] === "---\r") {
      return lines
        .slice(i + 1)
        .join("\n")
        .replace(/^(?:\r?\n)+/, "");
    }
  }
  return text;
}
