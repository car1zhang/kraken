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
 */
export function jobKey(j: Pick<WebJob, "slug" | "archived">): string {
  return j.archived ? `archive:${j.slug}` : j.slug;
}
