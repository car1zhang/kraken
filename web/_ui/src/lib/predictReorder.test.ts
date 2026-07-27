import { describe, expect, it } from "vitest";

import type { WebJob } from "../api/types.ts";
import { makeJob } from "../test/fixtures.ts";
import { columnSlugs, predictReorderedJobs } from "./predictReorder.ts";

const job = (slug: string, state: string, extra: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug, state, ...extra });

const slugsOf = (jobs: readonly WebJob[]) => jobs.map((j) => j.slug);

describe("predictReorderedJobs", () => {
  it("reorders within the column and leaves every other position alone", () => {
    const jobs = [
      job("a", "draft"),
      job("b", "draft"),
      job("c", "draft"),
      job("x", "working"),
      job("y", "done"),
    ];
    const out = predictReorderedJobs(jobs, "draft", ["c", "a", "b"]);
    expect(out).not.toBeNull();
    // buildWebBoard groups each display state contiguously and reorders only
    // within it (jobs/order.go ApplyColumnOrder) — so the working and done
    // entries must still be at indexes 3 and 4.
    expect(slugsOf(out!)).toEqual(["c", "a", "b", "x", "y"]);
  });

  it("returns the same job OBJECTS, not copies", () => {
    const a = job("a", "draft");
    const b = job("b", "draft");
    const out = predictReorderedJobs([a, b], "draft", ["b", "a"]);
    expect(out![0]).toBe(b);
    expect(out![1]).toBe(a);
  });

  it("handles a column whose members are not contiguous in the payload", () => {
    // The wire happens to group them today; the prediction must not depend on
    // it, because the ONLY thing it may assume is jobs/order.go's rule that
    // non-members keep their positions.
    const jobs = [job("a", "draft"), job("x", "done"), job("b", "draft")];
    const out = predictReorderedJobs(jobs, "draft", ["b", "a"]);
    expect(slugsOf(out!)).toEqual(["b", "x", "a"]);
  });

  it("is a no-op when the requested order is the current one", () => {
    const jobs = [job("a", "draft"), job("b", "draft")];
    expect(slugsOf(predictReorderedJobs(jobs, "draft", ["a", "b"])!)).toEqual([
      "a",
      "b",
    ]);
  });

  // ---------------------------------------------------------- the archive trap

  it("filters archived entries out of its slug map", () => {
    // Archiving `foo` frees the slug; a new active `foo` then shares it. The
    // archived entry is appended LAST by buildWebBoard, so a map keyed by
    // bare slug hands it a last-writer-wins victory (web/jobkey_test.go's
    // TestPredictReorderedJobsIgnoresArchived pins this on the vanilla side).
    const live = job("foo", "blocked");
    const arch = job("foo", "archived", { archived: true, display_slug: "foo" });
    const jobs = [live, job("bar", "blocked"), arch];

    const out = predictReorderedJobs(jobs, "blocked", ["bar", "foo"]);
    expect(out).not.toBeNull();
    expect(slugsOf(out!)).toEqual(["bar", "foo", "foo"]);
    // …and the "foo" spliced into the BLOCKED column is the LIVE job, not the
    // archived entry. Getting this wrong renders an archived, unsteerable
    // card in the live job's slot.
    expect(out![1]).toBe(live);
    expect(out![2]).toBe(arch);
  });

  it("does not count an archived entry toward the column's membership", () => {
    const jobs = [
      job("a", "draft"),
      job("b", "draft"),
      // an archived job whose state field somehow reads as a live column must
      // not make the counts disagree and null the whole prediction
      job("a", "draft", { archived: true }),
    ];
    const out = predictReorderedJobs(jobs, "draft", ["b", "a"]);
    expect(out).not.toBeNull();
    expect(slugsOf(out!)).toEqual(["b", "a", "a"]);
  });

  // ------------------------------------------------ the relocated-card trap

  it("counts an unhealthy card as a member of the column it RENDERS in", () => {
    // bucketJobs puts an unhealthy cell in BLOCKED whatever its file state
    // says (app.js:614). Deriving membership from `j.state` here instead made
    // the member count disagree with the rendered column, so every prediction
    // in a column holding such a card was rejected and the optimistic path
    // degraded to a flashing refetch.
    const sick = job("sick", "working", { live: true, health: "cell session is gone" });
    const jobs = [job("d1", "draft"), sick, job("b1", "blocked")];

    const out = predictReorderedJobs(jobs, "blocked", ["b1", "sick"]);
    expect(out).not.toBeNull();
    expect(slugsOf(out!)).toEqual(["d1", "b1", "sick"]);
    // …and it is no longer a member of WORKING, which is now empty
    expect(slugsOf(predictReorderedJobs(jobs, "working", [])!)).toEqual([
      "d1",
      "sick",
      "b1",
    ]);
  });

  it("refuses a slug that belongs to another column", () => {
    // keyed over the column's members, so a cross-column slug is simply not
    // found rather than being spliced into a column it does not belong to
    const jobs = [job("a", "draft"), job("b", "draft"), job("x", "done")];
    expect(predictReorderedJobs(jobs, "draft", ["a", "x"])).toBeNull();
  });

  // -------------------------------------------------------------- refusals

  it("returns null when a posted slug is unknown to the client", () => {
    const jobs = [job("a", "draft"), job("b", "draft")];
    expect(predictReorderedJobs(jobs, "draft", ["a", "b", "ghost"])).toBeNull();
  });

  it("returns null when the column holds a job the order does not mention", () => {
    // a job entered the column between the last poll and the drop
    const jobs = [job("a", "draft"), job("b", "draft"), job("c", "draft")];
    expect(predictReorderedJobs(jobs, "draft", ["b", "a"])).toBeNull();
  });

  it("returns null for an empty client picture", () => {
    expect(predictReorderedJobs([], "draft", ["a"])).toBeNull();
  });

  it("predicts an empty column as an unchanged payload", () => {
    const jobs = [job("x", "done")];
    expect(slugsOf(predictReorderedJobs(jobs, "draft", [])!)).toEqual(["x"]);
  });
});

describe("columnSlugs", () => {
  it("reads a column's active members in server order", () => {
    const jobs = [
      job("a", "draft"),
      job("x", "done"),
      job("b", "draft"),
      job("a", "archived", { archived: true }),
    ];
    expect(columnSlugs(jobs, "draft")).toEqual(["a", "b"]);
    expect(columnSlugs(jobs, "done")).toEqual(["x"]);
  });

  it("never returns an archived entry, whatever its state field says", () => {
    const jobs = [job("a", "draft"), job("a", "draft", { archived: true })];
    expect(columnSlugs(jobs, "draft")).toEqual(["a"]);
  });

  it("reads an unhealthy card out of BLOCKED, not out of its file state", () => {
    const jobs = [
      job("sick", "working", { live: true, health: "no zeus" }),
      job("b1", "blocked"),
    ];
    expect(columnSlugs(jobs, "blocked")).toEqual(["sick", "b1"]);
    expect(columnSlugs(jobs, "working")).toEqual([]);
  });
});
