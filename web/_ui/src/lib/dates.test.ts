import { describe, expect, it } from "vitest";

import { shortDate } from "./dates.ts";

// Carried over verbatim from graphModel.test.ts when shortDate moved here.
describe("shortDate", () => {
  it("renders an instant as a local calendar day, and 0 as nothing", () => {
    expect(shortDate(0)).toBe("");
    expect(shortDate(Number.NaN)).toBe("");
    const d = new Date(2026, 6, 28, 12, 0, 0);
    expect(shortDate(d.getTime())).toBe("2026-07-28");
  });
});
