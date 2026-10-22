import type { WebJob } from "../api/types.ts";

/**
 * The board's job identity. Ported verbatim from web/static/app.js:46.
 *
 * A job's slug is unique only WITHIN a namespace. An archived job rides the
 * same `jobs` array as the active ones under its numbered archive name, and
 * that name is numbered only against the archive's own contents — so
 * archiving `foo` and then creating a new active `foo` puts TWO entries with
 * slug "foo" on the board, archived appended last (internal/api/board.go
 * spells this out on webJob.Archived).
 *
 * Keying any client-side map by bare slug therefore hands the archived entry
 * a last-writer-wins victory over the live job, and the symptom is silent and
 * nasty: the lookup returned the archived entry (live:false), so a click on
 * the LIVE blocked card fell through to the read-only editor instead of the
 * unblock modal — the job became unsteerable from the board for as long as
 * both existed. That shipped once. web/jobkey_test.go pins it on the vanilla
 * side; the React equivalents live in jobKey.test.ts and BoardView.test.tsx.
 *
 * This is NOT a server key: unarchive/delete carry the bare archive name,
 * which is what the server actually resolves.
 *
 * A card another kraken polled in (it carries `owner`) is keyed by OWNER plus
 * slug — `<node_id>/<slug>` — for the archived entry's exact reason one level
 * up: every machine on the tailnet has a `dream-<date>`, and a bare-slug key
 * would let a remote card shadow the local job of the same name. The `/` can
 * never collide with a local key: slugs are slugified and carry no slash.
 */
export function jobKey(j: Pick<WebJob, "slug" | "archived" | "owner">): string {
  if (j.owner) return `${j.owner.node_id}/${j.slug}`;
  return j.archived ? `archive:${j.slug}` : j.slug;
}

// Fleet predicates the contract lists beside jobKey; their one home is
// lib/fleet.ts, re-exported so either import path reaches them.
export {
  filterFleet,
  isRemote,
  isUnauthorized,
  nodesStat,
  ownerChipText,
  remoteMode,
  type RemoteMode,
} from "./fleet.ts";
