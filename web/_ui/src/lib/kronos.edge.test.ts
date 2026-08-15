import { describe, expect, it } from "vitest";

import type { OrchInbox } from "../api/types.ts";
import { kronosMeterState } from "./kronos.ts";

// kronos.edge.test.ts — the EDGE dimension of Slice E of
// artifacts/50-contract.md (the kronos build): the null report, the exact
// rune-thin boundaries the three bands switch on, and the one case where the
// band and the label legitimately disagree. Happy paths and the stated
// invariants belong to the sibling blind file.
//
// Written blind from the contract text alone, with no sight of
// web/_ui/src/lib/kronos.ts, client.ts or components/kronos/*.
//
// Scope note: the contract lists `fetchKronos` and the `KronosStatus` type
// under `client.ts`, but this repo's client lives at src/api/client.ts while
// the contract's file list says web/_ui/src/lib/client.ts. Rather than guess
// which path ships, this file targets only `lib/kronos.ts`, whose location the
// contract states unambiguously. The ambiguity is disclosed in the run report.
//
// Every helper declared here is prefixed `edgeKronos` — a second blind author
// shares this directory.

const edgeKronosBands = ["ok", "warn50", "warn75", "warn90"] as const;

function edgeKronosState(pct: number | null) {
  return kronosMeterState(pct);
}

describe("kronosMeterState — the null report", () => {
  // "null → band \"ok\", label \"—\" (a null report NEVER renders as 0%)".
  // The statusline sends nothing when claude reports no percentage, and 0% is
  // the most misleading possible substitute: it renders as the healthiest
  // reading on the meter, which is exactly the state carl would stop watching.
  it("renders as the em dash, never as a number", () => {
    const got = edgeKronosState(null);
    expect(got.pct).toBeNull();
    expect(got.band).toBe("ok");
    expect(got.label).toBe("—");
  });

  it("never produces a zero-percent label", () => {
    const { label } = edgeKronosState(null);
    expect(label).not.toContain("0");
    expect(label).not.toBe("0.0%");
    expect(label).not.toBe("0%");
  });

  // The control: an actual zero IS a number and must render as one, so the
  // assertion above is measuring the null case rather than a meter that simply
  // never draws zeros.
  it("is distinguishable from a real 0% reading", () => {
    const zero = edgeKronosState(0);
    expect(zero.pct).toBe(0);
    expect(zero.band).toBe("ok");
    expect(zero.label).toContain("0.0");
    expect(zero.label).not.toBe("—");
  });
});

describe("kronosMeterState — the band boundaries", () => {
  // "bands switch at ≥50/≥75/≥90". Each boundary is asserted from BOTH sides
  // with the smallest step the meter can receive: a `>` where the contract
  // says `≥` means the warning band for a context sitting exactly on 90.0
  // never appears, and the reading that matters most is the one that never
  // renders.
  const cases: Array<[number, (typeof edgeKronosBands)[number]]> = [
    [0, "ok"],
    [1, "ok"],
    [49, "ok"],
    [49.9, "ok"],
    [49.99, "ok"],
    [50, "warn50"],
    [50.01, "warn50"],
    [51, "warn50"],
    [74, "warn50"],
    [74.9, "warn50"],
    [74.99, "warn50"],
    [75, "warn75"],
    [75.01, "warn75"],
    [89, "warn75"],
    [89.9, "warn75"],
    [89.99, "warn75"],
    [90, "warn90"],
    [90.01, "warn90"],
    [99.9, "warn90"],
    [100, "warn90"],
  ];

  for (const [pct, band] of cases) {
    it(`${pct} → ${band}`, () => {
      expect(edgeKronosState(pct).band).toBe(band);
    });
  }

  // Over 100 is not a reading the statusline should produce, but the meter is
  // fed by a hook rather than by validated input, and the honest answer for
  // "past the top band" is the top band — never a fall-through to "ok", which
  // would draw the fullest possible context as healthy.
  it("a percentage past 100 stays in the top band", () => {
    expect(edgeKronosState(120).band).toBe("warn90");
  });

  it("every band it returns is one of the four the contract names", () => {
    for (const pct of [null, 0, 49.9, 50, 74.9, 75, 89.9, 90, 100, 120]) {
      expect(edgeKronosBands).toContain(edgeKronosState(pct).band);
    }
  });
});

describe("kronosMeterState — the label", () => {
  // "label formats one decimal". The exact template (suffix, spacing) is not
  // frozen by the contract, so what is asserted is the part that is: the
  // rendered number carries exactly one digit after the point. A meter that
  // rendered 49.9 as "50" would round a below-threshold reading into looking
  // like the threshold itself.
  const oneDecimal = /\d+\.\d(?!\d)/;

  for (const [pct, want] of [
    [0, "0.0"],
    [7, "7.0"],
    [49.9, "49.9"],
    [50, "50.0"],
    [75, "75.0"],
    [90, "90.0"],
    [100, "100.0"],
  ] as Array<[number, string]>) {
    it(`${pct} renders ${want}`, () => {
      const { label } = edgeKronosState(pct);
      expect(label).toContain(want);
      expect(label).toMatch(oneDecimal);
    });
  }

  // The trap: a value that ROUNDS to the threshold while sitting below it. The
  // label and the band are two different judgements of one number and they are
  // allowed to disagree here — what must not happen is the band following the
  // rounded label, which would fire the warn50 styling on a context that has
  // not crossed 50.
  it("a value that rounds up to the threshold stays in the lower band", () => {
    const got = edgeKronosState(49.95);
    expect(got.band).toBe("ok");
    expect(got.label).toMatch(oneDecimal);
  });

  it("a value that rounds up to 90 stays in warn75", () => {
    const got = edgeKronosState(89.96);
    expect(got.band).toBe("warn75");
  });
});

describe("kronosMeterState — purity", () => {
  // "Pure." The meter is re-derived on every poll tick, so a function that
  // accumulated state would drift the band away from the reading it was given
  // — and the drift would only ever be visible after the board had been open
  // for a while, which is the hardest possible thing to reproduce.
  it("returns the same answer for the same input, repeatedly", () => {
    for (const pct of [null, 0, 49.9, 50, 75, 90, 100]) {
      const first = edgeKronosState(pct);
      const second = edgeKronosState(pct);
      const third = edgeKronosState(pct);
      expect(second).toEqual(first);
      expect(third).toEqual(first);
    }
  });

  // The pct it returns is the pct it was handed: the meter reports the reading
  // rather than a snapped or clamped version of it, since the drilldown shows
  // that number beside the band.
  it("passes the reading back through unchanged", () => {
    for (const pct of [0, 12.345, 49.9, 50, 89.96, 100]) {
      expect(edgeKronosState(pct).pct).toBe(pct);
    }
  });
});

// --- round 3 ---------------------------------------------------------------
//
// Authored SIGHTED against the round-3 amendment "`OrchInbox` (web types)
// carries `total: number` and its doc names the 100-row cap".
describe("OrchInbox — the total key", () => {
  // This is a TYPE obligation with no runtime surface (vitest transpiles
  // without typechecking), so the pin that actually bites is the expect-error
  // directive below, enforced by `npm run build`'s `tsc --noEmit`, which
  // covers src/ and therefore this file. (Note the prose here deliberately
  // does not START a comment line with that directive's name: tsc reads any
  // such line as a directive, and one sitting above a comment line reports as
  // unused — which is how this pin first failed.) It fails in BOTH directions: if
  // `total` were dropped or made optional the directive becomes unnecessary and
  // tsc errors on the unused directive; if it stays required the first literal
  // has to keep carrying it.
  //
  // Required rather than optional is the whole point: the count is what makes
  // the 100-row cap visible instead of silent, and a client that may omit it is
  // a client that can serve a truncated page indistinguishable from a whole
  // one — which is exactly the state a kronos holding 100 of 900 rows must not
  // be in.
  it("carries the full unacked count beside the capped page", () => {
    const page: OrchInbox = { items: [], total: 101 };
    expect(page.total).toBe(101);
    expect(page.items).toEqual([]);

    // A page that can omit its count cannot say whether it is the whole inbox
    // or the oldest hundred of it — so the omission must not typecheck. The
    // directive has to sit on the line IMMEDIATELY above the statement it
    // covers; a second comment line under it would swallow it and report as
    // unused.
    // @ts-expect-error — `total` is REQUIRED.
    const untotalled: OrchInbox = { items: [] };
    expect(untotalled.items).toEqual([]);
  });
});
