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
 * The machine key of THIS node's own cards in the fleet filter's selection.
 * A sentinel rather than the board's `node_id`: local cards carry no `owner`
 * and are matched by that absence, and the payload's `node_id` is absent
 * whenever tailscale is unreadable — a selection keyed on it would silently
 * stop matching local cards exactly then. A tailscale stable id is never the
 * bare word `local`, so the two namespaces cannot collide.
 */
export const LOCAL_NODE = "local";

/** The machine a card belongs to, in the selection's vocabulary. */
export function jobNodeKey(j: Pick<WebJob, "owner">): string {
  return j.owner === undefined ? LOCAL_NODE : j.owner.node_id;
}

/**
 * The board's fleet filter: `selected` is the list of machine keys to SHOW
 * (`LOCAL_NODE` for this Mac, a node id for a sibling). Empty means no
 * filter — every card, local and remote, in server order. Non-empty keeps
 * exactly the cards whose machine is listed, order preserved, so a machine
 * that joins the tailnet while a selection is active is hidden until it is
 * picked (Q1 of job 634: the button says what is shown, and a filter that
 * silently widened would be one you could not trust). Nothing is ever added
 * or reordered — the columns' server order is the reorder's ground truth.
 */
export function filterFleet(jobs: WebJob[], selected: readonly string[]): WebJob[] {
  if (selected.length === 0) return jobs;
  const shown = new Set(selected);
  return jobs.filter((j) => shown.has(jobNodeKey(j)));
}

/**
 * The machines dropdown's button text: what the filter shows, in as few
 * words as it can be said. `all machines` with no selection; `this Mac` for
 * exactly this node; a lone sibling's registry label (its id when the
 * registry no longer lists it); otherwise `N of M machines`, M counting the
 * registry plus this Mac. A selected key the registry does not name still
 * counts in N — the filter is always undoable from the bar, so it must
 * always be visible there.
 */
export function fleetButtonLabel(selected: readonly string[], nodes: readonly WebNode[]): string {
  if (selected.length === 0) return "all machines";
  if (selected.length === 1) {
    const only = selected[0]!;
    if (only === LOCAL_NODE) return "this Mac";
    const n = nodes.find((x) => x.node_id === only);
    return n ? n.label || n.node_id : only;
  }
  return `${selected.length} of ${nodes.length + 1} machines`;
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

/**
 * localStorage key of the machine selection — a per-browser preference, never
 * server state. The two keys it replaced, `kraken.hideOthers` (the old
 * all-or-nothing checkbox) and `kraken.hiddenNodes` (the Devices overlay's
 * per-node hide column, an EXCLUSION list), are read exactly once more each,
 * by the migration in readFleetNodes, and then removed.
 */
export const FLEET_NODES_KEY = "kraken.fleetNodes";
const LEGACY_HIDE_OTHERS_KEY = "kraken.hideOthers";
const LEGACY_HIDDEN_NODES_KEY = "kraken.hiddenNodes";

/** A stored selection, or null when the key is absent or unreadable. */
function parseFleetNodes(raw: string | null): string[] | null {
  if (raw === null) return null;
  try {
    const v: unknown = JSON.parse(raw);
    if (!Array.isArray(v)) return [];
    const out: string[] = [];
    for (const s of v) {
      if (typeof s === "string" && s !== "" && !out.includes(s)) out.push(s);
    }
    return out;
  } catch {
    return [];
  }
}

/**
 * Reads the machine selection. A present key wins, however it reads (a
 * malformed value is the empty selection, not a fall-through to the legacy
 * keys). An ABSENT key is the one-time migration: the old checkbox's `"true"`
 * meant "only this Mac", which is exactly `["local"]`, so that is written
 * under the new key and both legacy keys are removed. The old hidden-nodes
 * list is dropped rather than mapped: an exclusion list only becomes an
 * inclusion list with the registry in hand, which this read does not have.
 * Anything unreadable — a private window, blocked storage — reads as no
 * selection and throws nothing.
 */
export function readFleetNodes(): string[] {
  try {
    const stored = parseFleetNodes(window.localStorage.getItem(FLEET_NODES_KEY));
    if (stored !== null) return stored;
    const migrated = window.localStorage.getItem(LEGACY_HIDE_OTHERS_KEY) === "true" ? [LOCAL_NODE] : [];
    if (migrated.length > 0) {
      window.localStorage.setItem(FLEET_NODES_KEY, JSON.stringify(migrated));
    }
    window.localStorage.removeItem(LEGACY_HIDE_OTHERS_KEY);
    window.localStorage.removeItem(LEGACY_HIDDEN_NODES_KEY);
    return migrated;
  } catch {
    return [];
  }
}

export function writeFleetNodes(keys: readonly string[]): void {
  try {
    window.localStorage.setItem(FLEET_NODES_KEY, JSON.stringify(keys));
  } catch {
    // private window / blocked storage: the filter still works for this tab
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
