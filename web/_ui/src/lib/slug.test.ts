import { describe, expect, it } from "vitest";

import { slugify } from "./slug.ts";

// slug.test.ts — the TypeScript half of the cross-source pin on `util.Slugify`
// (internal/util/util.go), which now has three implementations: the Go one that
// DECIDES, this port, and `JobSlug.slugify` in ios/Kraken/JobFormView.swift.
//
// The repo's rule for a value mirrored across sources is a pinning test rather
// than a comment — TestJobPaletteMatchesCSS/…MatchesSwift for the sixteen
// palette hexes, internal/jobs/loadscale_contract_test.go for the twenty load
// anchors — and two hand ports of a rune loop are exactly that shape. The
// stakes are lower than the palette's (a drifted slugifier costs a wrong
// PREVIEW, never a wrong rename: the server slugifies the text it is sent and
// its answer is what the client re-points to) and that is an argument for a
// cheap table, not for none.
//
// SLUG_CASES below is the shared table: the same rows in the same order appear
// in ios/KrakenTests/JobSlugTests.swift, VALUE-identical row for row rather
// than byte-identical — two rows cannot be the latter, since Swift spells the
// non-ASCII literals as `\u{…}` escapes and TypeScript does not always have to.
// Nothing mechanically enforces the mirroring today — there is no Go-side
// parser pinning these two files against each other, the way
// TestJobPaletteMatchesCSS pins the palette — so the agreement is a convention
// this comment states rather than a guarantee. The ORACLE, though, is no
// longer unpinned: `TestSlugify` and `TestSlugifyIsIdempotent` in
// internal/util/util_test.go hold these same rows, in this order, against the
// Go implementation, which is what makes "verified by running the Go
// implementation itself" a re-checkable claim rather than a historical one.
//
// The two DIVERGENCES the three implementations genuinely have get their own
// block at the bottom, pinned to what each one actually produces — an unpinned
// known divergence is how a second one arrives beside it unnoticed.

/**
 * input → `util.Slugify(input)`, as the Go implementation actually answers it.
 *
 * Mirrored value for value, row for row, in JobSlugTests.swift. Anything added
 * here belongs there too, and only cases all three implementations agree on may
 * live in it — the divergence block below is where the disagreements are
 * recorded.
 */
const SLUG_CASES: [string, string][] = [
  // the empty and all-punctuation inputs — the two shapes that slugify to
  // nothing, which is what the server's gate 1 refuses and what disarms both
  // clients' rename buttons
  ["", ""],
  [" ", ""],
  ["!!!", ""],
  ["-", ""],
  ["日本語", ""],
  // the canonical demo: what makes a typed "My Job!" unsurprising
  ["My Job!", "my-job"],
  // separators at each end and in runs
  ["  leading", "leading"],
  ["trailing  ", "trailing"],
  ["--dashes--", "dashes"],
  ["a   b", "a-b"],
  ["a - b", "a-b"],
  // ordinary names
  ["Job 123", "job-123"],
  ["UPPER", "upper"],
  ["a.b.c", "a-b-c"],
  ["foo_bar", "foo-bar"],
  // `linearSlug`'s own literal (internal/jobs/linear_contract_test.go)
  ["A Title", "a-title"],
  // already-slugified inputs are FIXED POINTS — the property the uploads dir,
  // the cell dir and the branch name all lean on
  ["already-slug", "already-slug"],
  ["kraken-renaming-cells", "kraken-renaming-cells"],
  ["2026-08-12", "2026-08-12"],
  // a precomposed non-ASCII letter is a separator, not a letter: NFC "café"
  // holds é as ONE scalar, which fails the a-z test in all three ports.
  // Spelled with an escape rather than the character so an editor (or a
  // filesystem, on macOS) cannot silently normalize it to the decomposed form
  // this table's expectation would then be wrong about — that is the whole
  // subject of the divergence block below.
  ["caf\u00e9", "caf"],
];

describe("slugify — the shared cross-source table", () => {
  for (const [input, want] of SLUG_CASES) {
    it(`slugifies ${JSON.stringify(input)} to ${JSON.stringify(want)}`, () => {
      expect(slugify(input)).toBe(want);
    });
  }

  // The property every caller of a slug leans on without saying so: a cell dir
  // is `<slugify(slug)>-<id>`, an uploads dir re-slugifies, and the rename
  // control seeds its input with the CURRENT slug — so a slug that changed
  // under a second pass would move a job's directories on a no-op rename.
  it("is idempotent on every case", () => {
    for (const [input] of SLUG_CASES) {
      const once = slugify(input);
      expect(slugify(once)).toBe(once);
    }
  });
});

// ---- known divergences ----------------------------------------------------
//
// Both are about CASE MAPPING and CLUSTERING, the two places a "port the rune
// loop" instruction cannot be followed literally, and both are safe for the
// same structural reason rather than by luck: the port only ever renders a
// preview and gates its own submit button, so the worst it can do is refuse a
// name the server would have accepted (disarm-only). It can never store one.

describe("slugify — divergences from Go, pinned deliberately", () => {
  // Go's `strings.ToLower` applies SIMPLE case mapping — one rune in, one rune
  // out — so U+0130 (LATIN CAPITAL LETTER I WITH DOT ABOVE) becomes plain "i"
  // and the whole string slugifies to "istanbul". JavaScript's toLowerCase
  // applies FULL case mapping, which expands it to "i" + U+0307 COMBINING DOT
  // ABOVE, and that combining mark is a separator here — so the preview reads
  // "i-stanbul" where the server stores "istanbul".
  //
  // (Swift's `lowercased()` also expands it, and then clusters the pair into
  // one non-ASCII Character, which drops it entirely: "stanbul". Three ports,
  // three answers, one of which is the truth.)
  it("expands İ where Go maps it, so the preview gains a separator", () => {
    expect(slugify("\u0130stanbul")).toBe("i-stanbul");
    // what the server would actually store, for the record:
    // util.Slugify("İstanbul") == "istanbul"
  });

  // Decomposed é — "e" plus U+0301 COMBINING ACUTE — is TWO code points, and
  // `for…of` iterates code points exactly as Go's range-over-string iterates
  // runes, so this port AGREES with Go here ("e" survives, the mark becomes a
  // trailing dash, the trim takes it). It is pinned anyway because it is the
  // case the Swift port gets wrong (it clusters the pair into one non-ASCII
  // Character and yields ""), and a pin on the agreeing side is what makes a
  // later "simplify" of this loop to `[...s]`-by-grapheme visible.
  it("agrees with Go on a decomposed combining mark", () => {
    expect(slugify("e\u0301")).toBe("e");
  });
});
