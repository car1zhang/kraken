import { describe, expect, it } from "vitest";

import { kronosMeterState } from "./kronos.ts";

// kronos.contract.test.ts — athena-logic's BLIND contract tests for the kronos
// build's Slice E pure half (50-contract.md § "Slice E — web"):
// kronosMeterState.
//
// Written from the contract text alone; lib/kronos.ts, the api client and
// every file under components/kronos/ were never opened in any form.
//
// Dimension: LOGIC — the stated band boundaries, the null rule, the label
// format. Hostile inputs (NaN, Infinity, negatives, > 100) are athena-edge's.
//
// Scope note, disclosed in full in the report: the contract's other two Slice
// E symbols are not reachable from a blind suite. `KronosStatus` and
// `fetchKronos` are named as living in "lib/client.ts", but this tree's client
// module is not under lib/ at all (the existing suites import "../api/
// client.ts"), so a blind import of either would be a guess at a module path
// rather than a test of an obligation — and the contract itself puts component
// rendering out of blind scope. The meter is therefore the whole surface here.
//
// Every module-scope identifier declared here is prefixed `contract`.

// The em dash is written as an escape: the null label is a single invisible-in-
// review character, and a literal one is indistinguishable from the hyphen or
// en dash an editor later substitutes.
const contractEmDash = "—";

/** contractBand is the band alone, so a boundary table reads as one column. */
const contractBand = (pct: number | null) => kronosMeterState(pct).band;

describe("kronosMeterState", () => {
  // The null report is the state the meter mounts in, and the one the contract
  // calls out by name: kronos has not reported yet. Rendering that as 0% would
  // say "plenty of context left" about a session whose usage is simply unknown
  // — the one wrong reading that looks like good news.
  it("renders a null report as the empty state, never as zero", () => {
    const got = kronosMeterState(null);
    expect(got.pct).toBeNull();
    expect(got.band).toBe("ok");
    expect(got.label).toBe(contractEmDash);
    expect(got.label).not.toContain("0");
    expect(got.label).not.toContain("%0");
  });

  // The four bands, at and around each stated boundary. Every switch is
  // INCLUSIVE at the threshold (>= 50 / >= 75 / >= 90), which is what makes
  // "the 50% warning" and "the 50% band" describe the same reading.
  it("switches bands at >=50, >=75 and >=90", () => {
    const table: Array<[number, string]> = [
      [0, "ok"],
      [1, "ok"],
      [49, "ok"],
      [49.9, "ok"],
      [50, "warn50"],
      [50.1, "warn50"],
      [74.9, "warn50"],
      [75, "warn75"],
      [75.1, "warn75"],
      [89.9, "warn75"],
      [90, "warn90"],
      [99.9, "warn90"],
      [100, "warn90"],
    ];
    for (const [pct, band] of table) {
      expect({ pct, band: contractBand(pct) }).toEqual({ pct, band });
    }
  });

  // The number goes through untouched: the meter's bar and its label are two
  // renderings of ONE reading, so a rounded/clamped pct here would make the
  // bar and the text disagree about the same session.
  it("passes the percentage through unchanged", () => {
    for (const pct of [0, 12.3, 42.5, 50, 99.9, 100]) {
      expect(kronosMeterState(pct).pct).toBe(pct);
    }
  });

  // "label formats one decimal". The suffix the label may carry beyond that is
  // not stated by the contract, so this asserts the decimal itself rather than
  // a whole string — disclosed in the report.
  it("formats the label to exactly one decimal place", () => {
    expect(kronosMeterState(50).label).toContain("50.0");
    expect(kronosMeterState(50).label).not.toContain("50.00");
    expect(kronosMeterState(42.5).label).toContain("42.5");
    expect(kronosMeterState(0).label).toContain("0.0");
    expect(kronosMeterState(100).label).toContain("100.0");
    // an integral reading is still rendered with its decimal, so the label's
    // width does not jump between ticks
    expect(kronosMeterState(7).label).toContain("7.0");
  });

  // Pure: the same reading gives the same answer, whatever else has been
  // asked in between. The meter is polled on a timer, so a stateful band
  // (a "highest seen" latch, say) would drift from what the payload says.
  it("is pure — repeated calls agree and no call affects another", () => {
    const first = kronosMeterState(55);
    kronosMeterState(95);
    kronosMeterState(null);
    const second = kronosMeterState(55);
    expect(second).toEqual(first);
  });

  // The four band names are a closed set the stylesheet keys on, so anything
  // outside it renders unstyled.
  it("only ever returns one of the four band names", () => {
    const allowed = new Set(["ok", "warn50", "warn75", "warn90"]);
    for (const pct of [null, 0, 25, 50, 60, 75, 80, 90, 100]) {
      expect(allowed.has(kronosMeterState(pct).band)).toBe(true);
    }
  });
});
