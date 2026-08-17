// library.contract.test.ts — athena-logic's BLIND contract tests for Slice B's
// pure module, `lib/library.ts` (50-contract.md).
//
// Authored from the frozen contract alone, before any implementation existed:
// library.ts, api/types.ts, api/client.ts, GraphView and ArtifactOverlay were
// never read. Every assertion below transcribes a stated obligation of
// `libraryMatch` or `filterLibrary`; where the contract is silent this file
// asserts nothing and the silence is disclosed in the round report.
//
// Dimension: CONTRACT — the stated rules and their happy paths. Boundary shapes
// (malformed urls, duplicate rows, unicode folding, very long lists) are
// athena-edge's ground in library.edge.test.ts.
//
// Every file-scope identifier here is prefixed `contract`: the edge author
// shares this directory and an unprefixed collision would take the suite down.
//
// No JSX and no component mounting: the contract fixes this file's NAME
// (`library.contract.test.ts`) and freezes only the two pure functions, so the
// view wiring (mode toggle, poll gating, the `☆ save` button's aria-pressed) is
// deliberately out of scope here.

import { describe, expect, it } from "vitest";
import type { WebLibraryItem } from "../api/types.ts";
import { filterLibrary, libraryMatch } from "./library.ts";

/**
 * One wire item, built field by field from the frozen shape
 * `{ id, slug, name, saved_at, url }` — no builder from the app's own fixtures
 * module, so this file depends on nothing but the contract.
 */
function contractItem(
  id: number,
  slug: string,
  name: string,
  savedAt = 1_000 + id,
): WebLibraryItem {
  return {
    id,
    slug,
    name,
    saved_at: savedAt,
    url: `/api/library/artifact/${id}/${name}`,
  };
}

/** The three-item library every matching test below searches. */
const contractItems: WebLibraryItem[] = [
  contractItem(7, "kraken-graph", "00-overview.md"),
  contractItem(8, "kraken-graph", "icons/deep/diagram.svg"),
  contractItem(9, "monolith-yelp", "00-overview.md"),
];

describe("libraryMatch", () => {
  // "/api/library/artifact/<id>/… urls match by id"
  it("matches a library url by its id segment", () => {
    expect(libraryMatch(contractItems, "/api/library/artifact/7/00-overview.md")).toEqual(
      contractItems[0],
    );
    expect(
      libraryMatch(contractItems, "/api/library/artifact/8/icons/deep/diagram.svg"),
    ).toEqual(contractItems[1]);
    expect(libraryMatch(contractItems, "/api/library/artifact/9/00-overview.md")).toEqual(
      contractItems[2],
    );
  });

  // The id is the WHOLE lookup: the server's own rule is that the name segment
  // of a library url is display/extension only. Two items in this fixture share
  // the name `00-overview.md`, so a name-led match could not tell 7 from 9.
  it("matches a library url by id alone, whatever the trailing name segment says", () => {
    expect(libraryMatch(contractItems, "/api/library/artifact/7/something-else.html")).toEqual(
      contractItems[0],
    );
    expect(libraryMatch(contractItems, "/api/library/artifact/9/00-overview.md")).toEqual(
      contractItems[2],
    );
  });

  it("returns null for a library url whose id names no saved item", () => {
    expect(libraryMatch(contractItems, "/api/library/artifact/404/00-overview.md")).toBeNull();
  });

  // "/api/artifact/<slug>/<home>/<name> urls match by exact (slug, name) pair"
  it("matches an artifact url by the exact (slug, name) pair", () => {
    expect(
      libraryMatch(contractItems, "/api/artifact/kraken-graph/cell/00-overview.md"),
    ).toEqual(contractItems[0]);
    expect(
      libraryMatch(contractItems, "/api/artifact/monolith-yelp/pluto/00-overview.md"),
    ).toEqual(contractItems[2]);
  });

  // The home segment is not part of the pair: the same artifact is addressed
  // under `cell` while its job runs, `pluto` once it is done and `archive`
  // after it is archived, and a save made under one must still read as saved
  // under the others.
  it("ignores the home segment of an artifact url", () => {
    for (const home of ["cell", "pluto", "archive"]) {
      expect(
        libraryMatch(contractItems, `/api/artifact/kraken-graph/${home}/00-overview.md`),
      ).toEqual(contractItems[0]);
    }
  });

  it("matches a nested artifact name with its slashes intact", () => {
    expect(
      libraryMatch(contractItems, "/api/artifact/kraken-graph/cell/icons/deep/diagram.svg"),
    ).toEqual(contractItems[1]);
  });

  it("returns null when only one half of the (slug, name) pair matches", () => {
    // right slug, wrong name
    expect(
      libraryMatch(contractItems, "/api/artifact/kraken-graph/cell/90-build-report.md"),
    ).toBeNull();
    // right name, wrong slug
    expect(
      libraryMatch(contractItems, "/api/artifact/some-other-job/cell/00-overview.md"),
    ).toBeNull();
    // right slug and right name, but from two DIFFERENT items — the pair is
    // matched as a pair, never field by field across rows
    expect(
      libraryMatch(contractItems, "/api/artifact/monolith-yelp/cell/icons/deep/diagram.svg"),
    ).toBeNull();
  });

  it("returns null for a url in neither family", () => {
    expect(libraryMatch(contractItems, "/api/upload/kraken-graph/img-001.png")).toBeNull();
    expect(libraryMatch(contractItems, "/api/feedback/snapshot?id=7")).toBeNull();
    expect(libraryMatch(contractItems, "/api/board")).toBeNull();
    expect(libraryMatch(contractItems, "")).toBeNull();
  });

  it("returns null against an empty library", () => {
    expect(libraryMatch([], "/api/library/artifact/7/00-overview.md")).toBeNull();
    expect(libraryMatch([], "/api/artifact/kraken-graph/cell/00-overview.md")).toBeNull();
  });
});

describe("filterLibrary", () => {
  // Deliberately NOT in any sorted order: `filterLibrary` never re-sorts, so an
  // implementation that sorted would have to disagree with these expectations.
  const contractUnsorted: WebLibraryItem[] = [
    contractItem(3, "zulu-job", "90-build-report.md"),
    contractItem(1, "alpha-job", "00-Overview.MD"),
    contractItem(2, "MIDDLE-job", "50-components.md"),
  ];

  it("returns the input order unchanged for an empty query", () => {
    expect(filterLibrary(contractUnsorted, "")).toEqual(contractUnsorted);
  });

  it("returns the input order unchanged for a whitespace-only query", () => {
    for (const q of [" ", "   ", "\t", "\n", " \t\n "]) {
      expect(filterLibrary(contractUnsorted, q)).toEqual(contractUnsorted);
    }
  });

  it("matches the name case-insensitively", () => {
    expect(filterLibrary(contractUnsorted, "overview")).toEqual([contractUnsorted[1]]);
    expect(filterLibrary(contractUnsorted, "OVERVIEW")).toEqual([contractUnsorted[1]]);
    expect(filterLibrary(contractUnsorted, "components")).toEqual([contractUnsorted[2]]);
  });

  it("matches the slug case-insensitively", () => {
    expect(filterLibrary(contractUnsorted, "alpha")).toEqual([contractUnsorted[1]]);
    expect(filterLibrary(contractUnsorted, "middle")).toEqual([contractUnsorted[2]]);
    expect(filterLibrary(contractUnsorted, "MIDDLE")).toEqual([contractUnsorted[2]]);
  });

  it("matches on a substring, not only on a prefix", () => {
    expect(filterLibrary(contractUnsorted, "build")).toEqual([contractUnsorted[0]]);
    expect(filterLibrary(contractUnsorted, "-job")).toEqual(contractUnsorted);
  });

  it("keeps the input order among the rows it keeps", () => {
    // `.md` is in every name here, so the whole list survives — in its original,
    // unsorted order.
    expect(filterLibrary(contractUnsorted, ".md")).toEqual(contractUnsorted);
    expect(filterLibrary(contractUnsorted, ".md").map((i) => i.id)).toEqual([3, 1, 2]);
  });

  it("returns an empty list when nothing matches", () => {
    expect(filterLibrary(contractUnsorted, "no-such-thing")).toEqual([]);
  });

  it("returns an empty list for any query against an empty library", () => {
    expect(filterLibrary([], "")).toEqual([]);
    expect(filterLibrary([], "anything")).toEqual([]);
  });
});
