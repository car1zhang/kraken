import { describe, expect, it } from "vitest";

import type { WebJob } from "../api/types.ts";
import { makeJob } from "../test/fixtures.ts";
import {
  fuzzyMatch,
  highlightSegments,
  matchJob,
  parseTerms,
  TEXT_MIN_TERM,
} from "./jobSearch.ts";

// ---------------------------------------------------------------------------
// THE SHARED CASE TABLE (artifacts/52-testing-plan.md), 15 cases plus 16/16b.
//
// The match rule is written twice — here and in Swift (ios/Kraken/JobSearch
// .swift) — and nothing structural keeps the halves in step. The mitigation is
// this table: the same six-job fixture and the same queries in both suites,
// with the CASE NUMBER in every test name so a disagreement can be found by
// grepping one number in two languages.
//
// Several cases carry a note about WHICH characters do the matching. Those are
// not decoration: this fixture's names overlap the queries in ways that are
// easy to misread — "search-jobs" ends in "jobs", the repo "monolith" contains
// "th" — and the notes are what keep a later reader from "fixing" a correct
// expectation back to an intuitive wrong one.
// ---------------------------------------------------------------------------

// The six-job fixture from artifacts/11-search-flow.html, field for field.
// The archived entry follows the wire: `slug` is the numbered archive name and
// `display_slug` the original slug (internal/api/board.go's archivedWebJob),
// and it carries no detail — the server never sets one on an archived job.
const searchJobs = makeJob({
  slug: "search-jobs",
  repo: "kraken",
  state: "working",
  live: true,
  detail: "doc ready for review: +4 diagrams, 4 sections",
});
const dream = makeJob({
  slug: "dream-2026-07-29",
  repo: "kraken",
  state: "done",
  detail: "research pass over yesterday's transcripts — 3 findings",
});
const jobLoad = makeJob({
  slug: "job-load-categorizing",
  repo: "kraken",
  state: "done",
  detail: "merged; board now ranks blocked cards by roi",
});
const yelp = makeJob({
  slug: "yelp-dedupe",
  repo: "monolith",
  state: "blocked",
  live: true,
  detail: "need a call on the dedupe threshold — 0.82 too loose?",
});
const asr = makeJob({ slug: "asr-wer-metrics", repo: "monolith", state: "draft" });
const archivedIndex = makeJob({
  slug: "search-index-2",
  display_slug: "search-index",
  repo: "monolith",
  state: "archived",
  archived: true,
});

const ALL: WebJob[] = [searchJobs, dream, jobLoad, yelp, asr, archivedIndex];

/** Every job the query keeps, as `slug:reason` — the table's "expect" column. */
function kept(query: string): string[] {
  const terms = parseTerms(query);
  return ALL.flatMap((j) => {
    const m = matchJob(j, terms);
    return m ? [`${j.slug}:${m.reason}`] : [];
  });
}

const ALL_KEPT_BY_NAME = [
  "search-jobs:name",
  "dream-2026-07-29:name",
  "job-load-categorizing:name",
  "yelp-dedupe:name",
  "asr-wer-metrics:name",
  "search-index-2:name",
];

describe("parseTerms", () => {
  it("splits on whitespace, lowercases, and drops the empties", () => {
    expect(parseTerms("  SeArch   Jobs ")).toEqual(["search", "jobs"]);
  });

  it("gives an empty query and a whitespace-only query the same empty terms", () => {
    expect(parseTerms("")).toEqual([]);
    expect(parseTerms("   \t ")).toEqual([]);
  });
});

describe("fuzzyMatch", () => {
  it("takes the characters in order, gaps allowed", () => {
    expect(fuzzyMatch("search-jobs", "sj")).toBe(true);
    expect(fuzzyMatch("search-jobs", "srchjb")).toBe(true);
    expect(fuzzyMatch("search-jobs", "serch")).toBe(true);
  });

  it("is ORDERED — the half that makes it a filter rather than a sieve", () => {
    // The same pair case 7 uses, and the same pair the Swift suite uses: two
    // characters one way round and the other, over one haystack.
    expect(fuzzyMatch("search-jobs kraken", "jb")).toBe(true);
    expect(fuzzyMatch("search-jobs kraken", "bj")).toBe(false);
  });

  it("matches an empty term and nothing longer than its haystack", () => {
    expect(fuzzyMatch("abc", "")).toBe(true);
    expect(fuzzyMatch("", "a")).toBe(false);
    expect(fuzzyMatch("abc", "abcd")).toBe(false);
  });

  it("takes both sides already lowercased — it does no folding of its own", () => {
    expect(fuzzyMatch("search-jobs", "S")).toBe(false);
    expect(fuzzyMatch("SEARCH", "s")).toBe(false);
  });
});

describe("the shared case table", () => {
  it("case 1 — an empty query keeps all 6, reason name", () => {
    expect(kept("")).toEqual(ALL_KEPT_BY_NAME);
  });

  it("case 2 — whitespace-only is an empty query", () => {
    expect(kept("   ")).toEqual(ALL_KEPT_BY_NAME);
  });

  // The archived job is here on its NAME: `display_slug` is "search-index",
  // which starts with the query outright.
  it("case 3 — `search` keeps the slug hit, the research/text hit, and the archived name", () => {
    expect(kept("search")).toEqual([
      "search-jobs:name",
      "dream-2026-07-29:text",
      "search-index-2:name",
    ]);
  });

  it("case 4 — `sj` is a non-adjacent subsequence of search-jobs", () => {
    expect(kept("sj")).toEqual(["search-jobs:name"]);
  });

  it("case 5 — `srchjb`, several gaps", () => {
    expect(kept("srchjb")).toEqual(["search-jobs:name"]);
  });

  // asr-wer-metrics really is a hit, and spelling out why is the point of the
  // case: s(a·S·r) e(w·E·r) r(we·R·) c(metri·C·s) h(monolit·H·). A
  // five-character subsequence spanning a whole name haystack is what typo
  // tolerance costs, and it is the same mechanism that finds `serch`.
  it("case 6 — the typo `serch` still finds it, plus the two names it spans", () => {
    expect(kept("serch")).toEqual([
      "search-jobs:name",
      "asr-wer-metrics:name",
      "search-index-2:name",
    ]);
  });

  // The ordering case, and the pair has to be chosen with care over this
  // fixture: `jb` is in order in both "search-jobs" and "job-load-
  // categorizing", `bj` is in order in neither. `js` does NOT demonstrate it —
  // "search-jobs" ends in "jobs", so j…s genuinely IS in order — and its true
  // answer is pinned here so nobody re-reads it as a counterexample.
  it("case 7 — the subsequence is ORDERED: `jb` hits, `bj` finds nothing", () => {
    expect(kept("jb")).toEqual(["search-jobs:name", "job-load-categorizing:name"]);
    expect(kept("bj")).toEqual([]);
    expect(kept("js")).toEqual(["search-jobs:name"]);
  });

  it("case 8 — `SEARCH` is case-insensitive, same result as case 3", () => {
    expect(kept("SEARCH")).toEqual(kept("search"));
  });

  it("case 9 — `monolith` reaches the 3 monolith jobs through repo", () => {
    expect(kept("monolith")).toEqual([
      "yelp-dedupe:name",
      "asr-wer-metrics:name",
      "search-index-2:name",
    ]);
  });

  it("case 10 — `index` finds the archived job", () => {
    expect(kept("index")).toEqual(["search-index-2:name"]);
  });

  it("case 11 — `dedupe threshold` ANDs a name term with a text term", () => {
    expect(kept("dedupe threshold")).toEqual(["yelp-dedupe:text"]);
  });

  it("case 12 — a 2-char term never reaches detail: `0.` misses, `th` hits names only", () => {
    expect(kept("0.")).toEqual([]);
    expect(kept("th")).toEqual([
      "yelp-dedupe:name",
      "asr-wer-metrics:name",
      "search-index-2:name",
    ]);
  });

  it("case 13 — 3 characters DO reach detail, and say so", () => {
    expect(kept("thr")).toEqual(["yelp-dedupe:text"]);
  });

  it("case 14 — `zzz` is a clean miss", () => {
    expect(kept("zzz")).toEqual([]);
  });

  it("case 15 — one failing term rejects the whole job", () => {
    expect(kept("search zzz")).toEqual([]);
  });

  // Case 16 — an archived job whose display_slug EQUALS its slug must not have
  // its name counted twice. Found by driving the real board rather than by
  // reading the code: `manifest` returned two archived cards that plainly do
  // not contain it, because a fuzzy match is a subsequence test and a doubled
  // name lets one term draw its characters from both copies.
  //
  // This is the shape 126 of the 132 archived jobs on the live board have —
  // archivedWebJob (internal/api/board.go) sets DisplaySlug to the job's own
  // slug, and the two differ only when archiving numbered the file — so the
  // fixture's `search-index-2`/`search-index` pair is the RARE case and cannot
  // catch this on its own. Hence a second archived job here.
  const archivedSame = makeJob({
    slug: "interview-platform-agent-reset",
    display_slug: "interview-platform-agent-reset",
    repo: "kraken",
    state: "archived",
    archived: true,
  });

  it("case 16 — a display_slug equal to the slug is not a second copy of the name", () => {
    const terms = parseTerms("manifest");
    // matches the name doubled ("…-reset …-reset kraken"), not the name once
    expect(fuzzyMatch("interview-platform-agent-reset kraken", "manifest")).toBe(false);
    expect(
      fuzzyMatch(
        "interview-platform-agent-reset interview-platform-agent-reset kraken",
        "manifest",
      ),
    ).toBe(true);
    // so the job must NOT match: the haystack carries the name once
    expect(matchJob(archivedSame, terms)).toBeNull();
  });

  it("case 16b — a display_slug that genuinely differs is still searchable", () => {
    // the guard drops display_slug only when it is a duplicate; case 10's job
    // keeps both names, which is what makes `index` reach it
    expect(kept("index")).toEqual(["search-index-2:name"]);
  });

  // The text floor, in the two halves the case is made of.
  //
  // `0.` isolates it: a real substring of yelp-dedupe's detail ("0.82 too
  // loose?") and a subsequence of no name in the fixture — none holds a "." at
  // all — so nothing but the text haystack could account for a hit, and two
  // characters never reach it.
  //
  // `th` shows what the floor does NOT govern: it is a substring of the repo
  // "monolith", and repo is in the name haystack (case 9 leans on the same
  // fact), so a 2-character term still matches NAMES. yelp-dedupe is here on
  // its repo rather than on the "threshold" in its detail — which is why every
  // hit is a name hit. Case 13 is the other side of the same line.
});

describe("matchJob", () => {
  it("puts slug, display_slug and repo in one name haystack", () => {
    const j = makeJob({ slug: "arch-2", display_slug: "telecom", repo: "monolith" });
    expect(matchJob(j, ["arch"])?.reason).toBe("name");
    expect(matchJob(j, ["telecom"])?.reason).toBe("name");
    expect(matchJob(j, ["monolith"])?.reason).toBe("name");
  });

  it("reads last_sent as status text too, not just detail", () => {
    const j = makeJob({ slug: "a", repo: "kraken", last_sent: "try the OTHER threshold" });
    expect(matchJob(j, ["other"])?.reason).toBe("text");
  });

  it(`admits a ${TEXT_MIN_TERM}-character term to the text haystack and nothing shorter`, () => {
    const j = makeJob({ slug: "a", repo: "kraken", detail: "the merge is green" });
    expect(matchJob(j, ["mer"])?.reason).toBe("text");
    expect(matchJob(j, ["me"])).toBeNull();
  });

  it("matches inside a word, which is the whole reason the card marks it", () => {
    const j = makeJob({ slug: "a", repo: "kraken", detail: "research pass" });
    expect(matchJob(j, ["search"])?.reason).toBe("text");
  });

  it("reports `text` when ANY term needed the text haystack", () => {
    const j = makeJob({ slug: "yelp-dedupe", repo: "monolith", detail: "the threshold" });
    expect(matchJob(j, ["dedupe"])?.reason).toBe("name");
    expect(matchJob(j, ["dedupe", "threshold"])?.reason).toBe("text");
  });

  it("folds case on both sides", () => {
    const j = makeJob({ slug: "Search-Jobs", repo: "Kraken", detail: "RESEARCH pass" });
    expect(matchJob(j, ["search"])?.reason).toBe("name");
    expect(matchJob(j, ["pass"])?.reason).toBe("text");
  });

  // The mirror of Swift's testTextFloorCountsUTF16CodeUnits. Both halves count
  // the floor in UTF-16 code units — `String.length` here, `.utf16.count`
  // there — so a two-emoji term is four units and clears it while a
  // two-character ASCII term does not. Nothing but this pins that the two
  // agree, which is the same reason the case table exists.
  it("counts the text floor in UTF-16 code units, as the Swift half does", () => {
    const j = makeJob({
      slug: "yelp-dedupe",
      repo: "monolith",
      detail: "shipped 🙂🙂x today, flag 🇺🇸, é̂x",
    });
    expect("🙂🙂".length).toBe(4);
    expect([..."🙂🙂"]).toHaveLength(2);
    expect(matchJob(j, ["🙂🙂"])?.reason).toBe("text");
    expect(matchJob(j, ["🙂x"])?.reason).toBe("text");
    expect(matchJob(j, ["🇺🇸"])?.reason).toBe("text");
    expect(matchJob(j, ["é̂x"])?.reason).toBe("text");
    // two code units, present in the detail, still below the floor
    expect(matchJob(j, ["y,"])).toBeNull();
  });

  it("survives a job with no display_slug, no detail and no last_sent", () => {
    const bare = makeJob({ slug: "a-job", repo: "kraken" });
    expect(bare.detail).toBeUndefined();
    expect(matchJob(bare, ["job"])?.reason).toBe("name");
    expect(matchJob(bare, ["nope"])).toBeNull();
  });

  it("keeps every job when there are no terms", () => {
    expect(matchJob(makeJob({ slug: "anything" }), [])).toEqual({ reason: "name" });
  });
});

describe("highlightSegments", () => {
  it("splits a line into plain and matched runs, preserving the original case", () => {
    expect(highlightSegments("RESEARCH pass", ["search"])).toEqual([
      { text: "RE", hit: false },
      { text: "SEARCH", hit: true },
      { text: " pass", hit: false },
    ]);
  });

  it("marks every occurrence", () => {
    expect(highlightSegments("abc abc", ["abc"])).toEqual([
      { text: "abc", hit: true },
      { text: " ", hit: false },
      { text: "abc", hit: true },
    ]);
  });

  it("collapses overlapping terms into one run rather than nesting them", () => {
    expect(highlightSegments("threshold", ["thresh", "reshold"])).toEqual([
      { text: "threshold", hit: true },
    ]);
  });

  it("ignores terms too short to have matched the text haystack at all", () => {
    expect(highlightSegments("the threshold", ["th"])).toEqual([
      { text: "the threshold", hit: false },
    ]);
  });

  it("marks nothing in a line that lowercases to a different length", () => {
    // U+0130 İ lowercases to two code units, so every offset found in the
    // folded copy is one too far into the original from there on.
    const line = "İstanbul deploy failed";
    expect(line.toLowerCase().length).toBe(line.length + 1);
    expect(highlightSegments(line, ["deploy"])).toEqual([{ text: line, hit: false }]);
    // and the term really is in there — this is a deliberate miss, not a
    // query that failed to match
    expect(line.toLowerCase()).toContain("deploy");
  });

  it("returns the line untouched when nothing in it matched", () => {
    expect(highlightSegments("nothing here", ["zzz"])).toEqual([
      { text: "nothing here", hit: false },
    ]);
    expect(highlightSegments("nothing here", [])).toEqual([
      { text: "nothing here", hit: false },
    ]);
  });
});
