// The board's job-search rule, and nothing else: no DOM, no fetch, no module
// state — the same pure-function layer format.ts lives in, for the same reason
// (vitest can cover the rule the rendered board depends on without rendering
// anything).
//
// The rule has TWO haystacks because a job has two very different kinds of
// text on it, and one rule over both is wrong either way round:
//
//   NAMES  — slug, display_slug (only when it DIFFERS from slug — see
//            nameHaystack), repo. Short kebab-case identifiers, matched
//            FUZZILY (the term's characters in order, gaps allowed) so a
//            half-remembered name still finds its card: `srchjb` finds
//            search-jobs, and so does the typo `serch`.
//   TEXT   — detail (zeus' last assistant block, capped at 4000 chars by the
//            server — see WebJob.detail) and last_sent. Matched by plain
//            SUBSTRING, and only by terms of 3 characters or more. Fuzzy over
//            4000 characters of prose is not a filter: a two-character
//            subsequence is present in essentially any paragraph, so the
//            board would come back whole on every query.
//
// A job matches when EVERY term matches by EITHER rule, and the result says
// WHICH — a card kept only by its status text has a name with nothing to do
// with the query, so it needs to explain itself on the card (JobCard's
// `matched in status text` marker). That is why this returns a JobMatch rather
// than a boolean: recomputing the reason at the card would be a second copy of
// the rule, free to drift from this one.
//
// This same rule is written a second time in Swift (ios/Kraken/JobSearch.swift)
// and nothing structural keeps the halves in step. The mitigation is the shared
// case table in artifacts/52-testing-plan.md, reproduced case-for-case in
// jobSearch.test.ts and in the Swift suite, with the case NUMBERS in the test
// names so a disagreement is greppable in both.

import type { WebJob } from "../api/types.ts";

/**
 * Why a job survived the query. `"name"` means every term matched a name
 * fuzzily; `"text"` means at least one term only reached it through detail /
 * last_sent — the case the card marks.
 */
export interface JobMatch {
  reason: "name" | "text";
}

/**
 * The shortest term allowed to reach the TEXT haystack. Two characters is the
 * length at which a substring stops narrowing anything: `th` is inside most
 * English sentences, so a board-wide match on it would look like the filter
 * had simply broken.
 */
export const TEXT_MIN_TERM = 3;

/**
 * Split a raw query into match terms. Lowercased HERE, once per query, so
 * every consumer below can assume its terms are already folded — fuzzyMatch
 * and matchJob both do, and neither lowercases a term again.
 */
export function parseTerms(query: string): readonly string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

/**
 * Is `term` a SUBSEQUENCE of `haystack` — its characters present in order,
 * not necessarily adjacent? Both arguments must already be lowercased
 * (parseTerms does the term; matchJob does the haystack).
 *
 * Ordered, which is the whole reason this is a usable filter rather than a
 * way of matching everything: `sk` is a subsequence of `search-jobs kraken`
 * and `ks` is not.
 *
 * An empty term matches, matching the identity `parseTerms("")` implies — but
 * parseTerms never emits one, so this only ever answers a direct caller.
 */
export function fuzzyMatch(haystack: string, term: string): boolean {
  let i = 0;
  for (let h = 0; h < haystack.length && i < term.length; h++) {
    if (haystack[h] === term[i]) i++;
  }
  return i === term.length;
}

/**
 * The name haystack: slug, display_slug and repo, lowercased and joined.
 *
 * display_slug is dropped when it EQUALS the slug, and that is load-bearing
 * rather than tidy. A fuzzy match is a subsequence test, so it does not care
 * where in the haystack each character comes from — and two copies of one name
 * let a term be satisfied by taking some characters from the first copy and
 * the rest from the second, which is not what "these characters appear in this
 * name, in order" means. Measured on the live board: `manifest` does NOT match
 * `interview-platform-agent-reset kraken`, but DOES match that string repeated
 * twice, because the second copy supplies the `i`,`f`,`e`,`s`,`t` the first has
 * already run past.
 *
 * That is not a rare shape. archivedWebJob (internal/api/board.go) sets
 * DisplaySlug to the job's own slug, and the two differ only when archiving hit
 * a name conflict and numbered the file — 126 of the 132 archived jobs on this
 * machine carry display_slug == slug. So without this guard the doubling is the
 * normal case for archived jobs, and the archive is exactly where a search is
 * most likely to be a half-remembered name.
 *
 * The same argument is why the iOS half does not use `WebJob.title`
 * (`displaySlug ?? slug`, ios/Kraken/Models.swift) as one of ITS fields — it is
 * a third spelling of the same name. There is no `title` on the TypeScript
 * WebJob at all; the note is here so both halves record the same reasoning.
 */
function nameHaystack(job: WebJob): string {
  const names = job.display_slug && job.display_slug !== job.slug
    ? [job.slug, job.display_slug, job.repo]
    : [job.slug, job.repo];
  return names
    .filter((s): s is string => !!s)
    .join(" ")
    .toLowerCase();
}

function textHaystack(job: WebJob): string {
  return [job.detail, job.last_sent]
    .filter((s): s is string => !!s)
    .join(" ")
    .toLowerCase();
}

/**
 * Decide whether one job survives `terms`, and say why. `terms` comes from
 * parseTerms — already trimmed, split and lowercased.
 *
 * No terms means MATCHES, never "matches nothing": an empty query is the
 * board's resting state, the state every existing board test runs in.
 */
export function matchJob(job: WebJob, terms: readonly string[]): JobMatch | null {
  if (terms.length === 0) return { reason: "name" };

  // Both built once per job rather than per term: a five-term query would
  // otherwise re-join and re-lowercase 4000 characters of detail five times.
  const names = nameHaystack(job);
  const text = textHaystack(job);

  let allNames = true;
  for (const term of terms) {
    if (fuzzyMatch(names, term)) continue;
    if (term.length >= TEXT_MIN_TERM && text.includes(term)) {
      allNames = false;
      continue;
    }
    return null;
  }
  return { reason: allNames ? "name" : "text" };
}

/** One run of a line, flagged as matched or not. See highlightSegments. */
export interface MatchSegment {
  text: string;
  /** True when this run is one of the terms, verbatim. */
  hit: boolean;
}

/**
 * Split one line of status text into alternating plain/matched runs, so the
 * card can render the matched substrings inside `<mark>`.
 *
 * Only terms of TEXT_MIN_TERM or more are marked, and only as literal
 * substrings — i.e. exactly the terms that could have matched this haystack in
 * matchJob. Marking a fuzzy name term here would highlight scattered single
 * characters across a sentence that had nothing to do with why the card is
 * on screen.
 *
 * Overlapping and repeated hits collapse into one run each, so `aa aa` under
 * the terms `aa` and `a aa` cannot emit a nested or doubled mark.
 */
export function highlightSegments(
  line: string,
  terms: readonly string[],
): readonly MatchSegment[] {
  const hay = line.toLowerCase();
  // Offsets are found in `hay` and used to slice `line`, which only holds
  // while lowercasing preserves length. It does for every character but one:
  // U+0130 (İ) lowercases to two code units, and one of those in a detail line
  // shifts every mark after it. So a line that changed length is returned
  // unmarked — a missing highlight beside a "matched in status text" marker is
  // honest, a highlight one character off is not.
  if (hay.length !== line.length) return [{ text: line, hit: false }];

  const ranges: Array<[number, number]> = [];
  for (const term of terms) {
    if (term.length < TEXT_MIN_TERM) continue;
    for (let i = hay.indexOf(term); i !== -1; i = hay.indexOf(term, i + term.length)) {
      ranges.push([i, i + term.length]);
    }
  }
  if (ranges.length === 0) return [{ text: line, hit: false }];

  // Merged BEFORE any segment is emitted, so two terms that overlap or touch
  // become one run rather than two <mark>s abutting — which would render the
  // same and read as a bug the first time anyone inspected it.
  ranges.sort((a, b) => a[0] - b[0]);
  const merged: Array<[number, number]> = [];
  for (const [start, end] of ranges) {
    const last = merged[merged.length - 1];
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else merged.push([start, end]);
  }

  const out: MatchSegment[] = [];
  let cut = 0;
  for (const [start, end] of merged) {
    if (start > cut) out.push({ text: line.slice(cut, start), hit: false });
    out.push({ text: line.slice(start, end), hit: true });
    cut = end;
  }
  if (cut < line.length) out.push({ text: line.slice(cut), hit: false });
  return out;
}
