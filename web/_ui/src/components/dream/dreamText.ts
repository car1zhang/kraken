// The DREAM view's pure text helpers — ports of dreamDay, dreamSectionCount,
// dreamPRCount, wlSeenText, wlVerdict and sortWatch (web/static/app.js:4617,
// :4636, :4656, :4697, :4728, :4759).
//
// They live beside the view rather than in lib/format.ts because every one of
// them is about the shape of a NIGHT REPORT or a watchlist row — the report's
// heading names come from .claude/skills/dream/SKILL.md, and nothing else in
// the board reads either shape.

import type { WebWatchItem } from "../../api/types.ts";

/**
 * A watchlist timestamp shortened to a local "MM-DD".
 *
 * It has to take two shapes because the row carries both: first_seen and
 * last_seen are unix MILLIS (the same wire convention job mtimes use), while
 * commented_at is an RFC3339 string stamped server-side. A number goes
 * through Date so the day is the LOCAL one — a sighting logged at 23:30
 * belongs to the evening carl remembers, not to the UTC tomorrow — and a
 * string is read off its ISO prefix rather than parsed, since that prefix is
 * already local-clock text. Anything else (0, "", an unexpected shape) reads
 * as blank rather than "Invalid Date" or "1970".
 */
export function dreamDay(v: number | string | null | undefined): string {
  if (typeof v === "number") {
    if (!v) return "";
    const d = new Date(v);
    if (isNaN(d.getTime())) return "";
    const p = (n: number) => String(n).padStart(2, "0");
    return `${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(v ?? ""));
  return m ? `${m[2]}-${m[3]}` : "";
}

/**
 * Counts the bullets under the FIRST heading whose text matches `re`,
 * stopping at the next heading of any level.
 *
 * The report's section names are fixed by .claude/skills/dream/SKILL.md
 * ("Shipped", "Judgment calls", "Watching", …) but the heading LEVEL it
 * writes them at is not, so the match is level-agnostic. A section that
 * spells out "None" carries no bullets and counts 0 — exactly the header a
 * quiet night should get.
 */
export function dreamSectionCount(body: string | undefined, re: RegExp): number {
  let inside = false;
  let n = 0;
  for (const line of String(body ?? "").split("\n")) {
    const h = /^#{1,6}\s+(.*)$/.exec(line);
    if (h) {
      if (inside) break;
      inside = re.test((h[1] ?? "").trim());
      continue;
    }
    if (inside && /^\s*[-*]\s+\S/.test(line)) n++;
  }
  return n;
}

/**
 * The DISTINCT PRs a night shipped, straight out of the report body — the
 * loop's PRs belong to no job's `prs:` list (they are kraken's own hardening,
 * opened from the dream cell), so the report text is the only place they are
 * recorded. Lazy \S*? so trailing punctuation — the ")" of a markdown link, a
 * sentence's full stop — stays out of the match.
 */
export function dreamPRCount(body: string | undefined): number {
  return new Set(String(body ?? "").match(/https?:\/\/\S*?\/pull\/\d+/g) ?? [])
    .size;
}

/** Sightings plus the window they span. A single sighting has no window worth
 *  printing, and first == last collapses to one date. */
export function wlSeenText(w: WebWatchItem): string {
  const n = w.sightings || 1;
  const parts = [`${n} sighting${n === 1 ? "" : "s"}`];
  const first = dreamDay(w.first_seen);
  const last = dreamDay(w.last_seen);
  if (first) parts.push(`first ${first}`);
  if (last && last !== first) parts.push(`last ${last}`);
  return parts.join(" · ");
}

/**
 * Normalizes the server's field to the three states this UI knows — anything
 * unexpected reads as "not ruled on yet" rather than rendering a row whose
 * buttons all look off while the server thinks otherwise.
 */
export function wlVerdict(w: WebWatchItem): "fine" | "fix" | "" {
  return w.verdict === "fine" || w.verdict === "fix" ? w.verdict : "";
}

/**
 * Everything still open first (untouched and `fix` alike — both are work the
 * loop has not finished with), then the rows ruled `fine`, dimmed at the
 * bottom. Within each group this reproduces the server's own order
 * (ListWatchItems is last_seen DESC, id DESC) rather than inventing a second
 * one — numerically, since last_seen is unix millis, and with id as the
 * tiebreak so the order is stable across polls.
 */
export function sortWatch(list: readonly WebWatchItem[]): WebWatchItem[] {
  const rank = (w: WebWatchItem) => (wlVerdict(w) === "fine" ? 1 : 0);
  return [...list].sort(
    (a, b) =>
      rank(a) - rank(b) ||
      (b.last_seen || 0) - (a.last_seen || 0) ||
      (b.id || 0) - (a.id || 0),
  );
}

/** A PR chip's label: `#<number>` when the url names one, else "PR". */
export function wlPRLabel(url: string): string {
  const m = /\/pull\/(\d+)/.exec(url);
  return m ? "#" + m[1] : "PR";
}

/** agent-written field — only http(s) is ever linkified, the same rule
 *  prChipHTML applies on the board. */
export const wlPRLinkable = (url: string | undefined): boolean =>
  /^https?:\/\//i.test(String(url ?? ""));
