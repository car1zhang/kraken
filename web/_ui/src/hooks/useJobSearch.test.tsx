import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { WebJob } from "../api/types.ts";
import { bucketJobs } from "../lib/format.ts";
import { jobKey } from "../lib/jobKey.ts";
import { makeBoard, makeJob } from "../test/fixtures.ts";
import type { BoardModel } from "./useBoard.ts";
import { useJobSearch } from "./useJobSearch.ts";

// The REAL bucketer, exactly as BoardView.test.tsx uses it: a hand-bucketed
// fixture can only confirm its own copy of the rule.
function model(jobs: WebJob[]): BoardModel {
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board: makeBoard({ jobs }),
    error: undefined,
    byKey: new Map(jobs.map((j) => [jobKey(j), j])),
    byColumn,
    archived,
    refetch: async () => {},
    setDragging: () => {},
    mutate: async () => null,
    lastError: null,
  };
}

const JOBS: WebJob[] = [
  makeJob({ slug: "search-jobs", repo: "kraken", state: "working", live: true }),
  makeJob({
    slug: "dream-2026-07-29",
    repo: "kraken",
    state: "done",
    detail: "research pass over yesterday's transcripts",
  }),
  makeJob({ slug: "yelp-dedupe", repo: "monolith", state: "blocked", live: true }),
  makeJob({ slug: "asr-wer-metrics", repo: "monolith", state: "queued" }),
  makeJob({
    slug: "search-index-2",
    display_slug: "search-index",
    repo: "monolith",
    state: "archived",
    archived: true,
  }),
];

const slugs = (jobs: WebJob[]) => jobs.map((j) => j.slug);

describe("useJobSearch", () => {
  it("starts idle: no terms, every job counted, nothing to explain", () => {
    const { result } = renderHook(() => useJobSearch(model(JOBS)));
    expect(result.current.active).toBe(false);
    expect(result.current.terms).toEqual([]);
    expect(result.current.matched).toBe(5);
    expect(result.current.total).toBe(5);
    expect(result.current.reasonByKey.size).toBe(0);
  });

  it("is still idle for a whitespace-only query", () => {
    const { result } = renderHook(() => useJobSearch(model(JOBS)));
    act(() => result.current.setQuery("   "));
    expect(result.current.query).toBe("   ");
    expect(result.current.active).toBe(false);
    expect(result.current.matched).toBe(5);
  });

  it("narrows each column independently, and the archive with them", () => {
    const { result } = renderHook(() => useJobSearch(model(JOBS)));
    act(() => result.current.setQuery("search"));
    expect(slugs(result.current.byColumn.working)).toEqual(["search-jobs"]);
    expect(slugs(result.current.byColumn.done)).toEqual(["dream-2026-07-29"]);
    expect(result.current.byColumn.blocked).toEqual([]);
    expect(result.current.byColumn.queued).toEqual([]);
    expect(slugs(result.current.archived)).toEqual(["search-index-2"]);
    // matched counts the archive too — the readout is about the board, and a
    // match hiding in the drawer is exactly the one carl needs told about.
    expect(result.current.matched).toBe(3);
    expect(result.current.total).toBe(5);
    expect(result.current.active).toBe(true);
  });

  it("carries `text` for a job kept only by its status text", () => {
    const { result } = renderHook(() => useJobSearch(model(JOBS)));
    act(() => result.current.setQuery("search"));
    expect(result.current.reasonByKey.get("search-jobs")).toBe("name");
    expect(result.current.reasonByKey.get("dream-2026-07-29")).toBe("text");
    // archived jobs are keyed `archive:<name>`, never the bare slug
    expect(result.current.reasonByKey.get("archive:search-index-2")).toBe("name");
    expect(result.current.reasonByKey.has("yelp-dedupe")).toBe(false);
  });

  // THE invariant this hook exists to preserve. useDragReorder reads
  // board.byColumn for its drop table AND for the /api/reorder body; a filter
  // pushed into that model posts a short column, and ApplyColumnOrder floats
  // every id missing from the post to the TOP of the column, permanently.
  it("leaves the board's own columns whole while returning a narrowed view", () => {
    const board = model(JOBS);
    const before = board.byColumn.working;
    const { result } = renderHook(() => useJobSearch(board));
    act(() => result.current.setQuery("zzz"));

    expect(result.current.byColumn.working).toEqual([]);
    expect(result.current.archived).toEqual([]);
    expect(board.byColumn.working).toBe(before);
    expect(slugs(board.byColumn.working)).toEqual(["search-jobs"]);
    expect(slugs(board.archived)).toEqual(["search-index-2"]);
  });

  it("hands back the board's own arrays while idle rather than copies of them", () => {
    const board = model(JOBS);
    const { result } = renderHook(() => useJobSearch(board));
    expect(result.current.byColumn).toBe(board.byColumn);
    expect(result.current.archived).toBe(board.archived);
  });

  it("clearing the query restores every job", () => {
    const { result } = renderHook(() => useJobSearch(model(JOBS)));
    act(() => result.current.setQuery("search"));
    act(() => result.current.setQuery(""));
    expect(result.current.active).toBe(false);
    expect(result.current.matched).toBe(5);
    expect(slugs(result.current.byColumn.blocked)).toEqual(["yelp-dedupe"]);
  });
});
