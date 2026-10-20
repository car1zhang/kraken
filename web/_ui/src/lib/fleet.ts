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
