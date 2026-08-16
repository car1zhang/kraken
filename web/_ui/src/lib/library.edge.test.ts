import { describe, expect, it } from "vitest";

import type { WebLibraryItem } from "../api/types.ts";
import { filterLibrary, libraryMatch } from "./library.ts";

// library.edge.test.ts — the EDGE dimension of 50-contract.md's Slice B pure
// module: what `libraryMatch` returns for every url that is NOT a match, and
// where `filterLibrary`'s two stated boundaries fall. Written blind from the
// frozen contract, with no sight of web/_ui/src/lib/library.ts.
//
// The sibling blind author (athena-logic) owns the stated happy paths — a
// library url matching its item, a query matching a name. What lives here is the
// other half: the null arms, the pair that must match EXACTLY, and the two
// promises `filterLibrary` makes about ORDER (empty query returns the input
// unchanged; a filter never re-sorts).
//
// Every module-scope identifier is prefixed `edgeLib` — this file's scope is
// private, but the convention is the repo's and the sibling blind file shares
// the surface.
//
// Where the contract is silent the probe is OMITTED rather than widened: a probe
// that accepts several shapes pins none of them.

let edgeLibNextID = 1;

function edgeLibItem(over: Partial<WebLibraryItem> = {}): WebLibraryItem {
  const id = over.id ?? edgeLibNextID++;
  return {
    id,
    slug: "a-job",
    name: "00-overview.md",
    saved_at: 1_770_000_000_000,
    url: `/api/library/artifact/${id}/00-overview.md`,
    ...over,
  };
}

const edgeLibNames = (items: WebLibraryItem[]) => items.map((i) => i.name);

// ---------------------------------------------------------------------------
// libraryMatch: the urls that are not library urls at all
// ---------------------------------------------------------------------------

describe("libraryMatch: non-artifact urls", () => {
  // Obligation: "any other url → null". The toggle asks this question on EVERY
  // artifact overlay open, so a url shape it does not understand has to read as
  // "not saved" rather than as an accidental match against whatever item
  // happened to sort first.
  it("returns null for urls outside both known families", () => {
    const items = [
      edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" }),
      edgeLibItem({ id: 2, slug: "b-job", name: "10-architecture.html" }),
    ];
    for (const url of [
      "",
      "/",
      "/api/board",
      "/api/upload/a-job/img-001.png",
      "/api/feedback/snapshot?id=1",
      "https://example.com/api/library/artifact/1/00-overview.md",
      "/api/artifacts/a-job/pluto/00-overview.md",
      "/api/libraries/artifact/1/00-overview.md",
    ]) {
      expect(libraryMatch(items, url), url).toBeNull();
    }
  });

  // A relative path is a caller bug rather than a variant to tolerate — every
  // url the client holds comes from the server's own `url` key or from an
  // artifact route, and both are absolute.
  it("returns null for a relative path", () => {
    const items = [edgeLibItem({ id: 1 })];
    expect(libraryMatch(items, "api/library/artifact/1/00-overview.md")).toBeNull();
    expect(libraryMatch(items, "api/artifact/a-job/pluto/00-overview.md")).toBeNull();
  });

  // An empty library is the state before the first save, and the toggle renders
  // on every overlay open from that moment on.
  it("returns null for an empty item list", () => {
    expect(libraryMatch([], "/api/library/artifact/1/00-overview.md")).toBeNull();
    expect(libraryMatch([], "/api/artifact/a-job/pluto/00-overview.md")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// libraryMatch: library urls match by id
// ---------------------------------------------------------------------------

describe("libraryMatch: library urls", () => {
  // Obligation: "`/api/library/artifact/<id>/…` urls match by id". By id and by
  // id ALONE — the trailing segment is display/extension only on the server, so
  // a match that also compared it would fail exactly when a stored name and an
  // opened url drift apart.
  it("matches on the id segment regardless of the name segment", () => {
    const items = [
      edgeLibItem({ id: 7, slug: "a-job", name: "00-overview.md" }),
      edgeLibItem({ id: 8, slug: "b-job", name: "10-architecture.html" }),
    ];
    expect(libraryMatch(items, "/api/library/artifact/7/00-overview.md")?.id).toBe(7);
    expect(libraryMatch(items, "/api/library/artifact/7/something-else.md")?.id).toBe(7);
    expect(libraryMatch(items, "/api/library/artifact/8/10-architecture.html")?.id).toBe(8);
  });

  // A nested stored name arrives as several path segments; only the first one
  // after the prefix is the id.
  it("matches a nested name's url by its id", () => {
    const items = [edgeLibItem({ id: 12, slug: "icons", name: "icon-variations/v2.svg" })];
    expect(libraryMatch(items, "/api/library/artifact/12/icon-variations/v2.svg")?.id).toBe(12);
  });

  // An id the list does not hold: the ordinary state right after a remove, and
  // the state of any url held by a client whose poll has not landed yet.
  it("returns null for an id no item carries", () => {
    const items = [edgeLibItem({ id: 7 }), edgeLibItem({ id: 8 })];
    expect(libraryMatch(items, "/api/library/artifact/9/00-overview.md")).toBeNull();
    expect(libraryMatch(items, "/api/library/artifact/70/00-overview.md")).toBeNull();
  });

  // A non-numeric id can match no item, since every item's id is a number.
  it("returns null for a malformed id segment", () => {
    const items = [edgeLibItem({ id: 7 })];
    for (const url of [
      "/api/library/artifact/abc/00-overview.md",
      "/api/library/artifact/7x/00-overview.md",
      "/api/library/artifact//00-overview.md",
      "/api/library/artifact/",
      "/api/library/artifact",
    ]) {
      expect(libraryMatch(items, url), url).toBeNull();
    }
  });
});

// ---------------------------------------------------------------------------
// libraryMatch: artifact urls match by the (slug, name) PAIR
// ---------------------------------------------------------------------------

describe("libraryMatch: artifact urls", () => {
  // Obligation: "`/api/artifact/<slug>/<home>/<name>` urls match by exact
  // `(slug, name)` pair". This is the url the overlay is usually opened with —
  // the toggle's whole job is to recognise that the artifact in front of carl is
  // one he already saved.
  it("matches the (slug, name) pair", () => {
    const items = [
      edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" }),
      edgeLibItem({ id: 2, slug: "b-job", name: "00-overview.md" }),
      edgeLibItem({ id: 3, slug: "a-job", name: "10-architecture.html" }),
    ];
    expect(libraryMatch(items, "/api/artifact/a-job/pluto/00-overview.md")?.id).toBe(1);
    expect(libraryMatch(items, "/api/artifact/b-job/pluto/00-overview.md")?.id).toBe(2);
    expect(libraryMatch(items, "/api/artifact/a-job/pluto/10-architecture.html")?.id).toBe(3);
  });

  // The home segment is deliberately NOT part of the pair: a saved artifact's
  // home moves with its job's lifecycle (cell → pluto → archive) while the saved
  // row does not, so a match that included it would silently un-star every item
  // the moment its job was torn down.
  it("matches across every home", () => {
    const items = [edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" })];
    for (const home of ["cell", "pluto", "archive"]) {
      expect(libraryMatch(items, `/api/artifact/a-job/${home}/00-overview.md`)?.id, home).toBe(1);
    }
  });

  // Everything after the home segment is the name, slashes included — the same
  // rule the iOS `artifactParts` parse follows.
  it("keeps a nested name's slashes when pairing", () => {
    const items = [edgeLibItem({ id: 5, slug: "icons", name: "icon-variations/v2.png" })];
    expect(libraryMatch(items, "/api/artifact/icons/cell/icon-variations/v2.png")?.id).toBe(5);
    // The leaf alone is a DIFFERENT artifact, not another spelling of this one.
    expect(libraryMatch(items, "/api/artifact/icons/cell/v2.png")).toBeNull();
  });

  // Half a pair is not a match. Both halves are drawn from the same url, so a
  // one-sided comparison would star every artifact of a job that has any saved
  // item at all.
  it("returns null when only one half of the pair agrees", () => {
    const items = [edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" })];
    expect(libraryMatch(items, "/api/artifact/a-job/pluto/90-build-report.md")).toBeNull();
    expect(libraryMatch(items, "/api/artifact/other-job/pluto/00-overview.md")).toBeNull();
  });

  // "exact" is exact: slugs and artifact names are both case-sensitive
  // server-side (the store matches them with a plain SQL equality), so a
  // case-folded match here would claim a saved item that the remove POST could
  // then not address.
  it("is case-sensitive on both halves", () => {
    const items = [edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" })];
    expect(libraryMatch(items, "/api/artifact/A-Job/pluto/00-overview.md")).toBeNull();
    expect(libraryMatch(items, "/api/artifact/a-job/pluto/00-OVERVIEW.md")).toBeNull();
  });

  // Malformed artifact urls: no name segment, an empty name segment, no home.
  it("returns null for artifact urls with no name", () => {
    const items = [edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" })];
    for (const url of [
      "/api/artifact/a-job/pluto",
      "/api/artifact/a-job/pluto/",
      "/api/artifact/a-job",
      "/api/artifact/",
      "/api/artifact",
    ]) {
      expect(libraryMatch(items, url), url).toBeNull();
    }
  });
});

// ---------------------------------------------------------------------------
// filterLibrary: the empty query
// ---------------------------------------------------------------------------

describe("filterLibrary: empty and whitespace queries", () => {
  // Obligation: "empty/whitespace q returns the input order unchanged". This is
  // the state the filter box is in every time the view opens, so it is the code
  // path the list is rendered through almost always — and the served order is
  // the newest-first order the server computed, which the client must not
  // second-guess.
  it("returns the input, in input order, for an empty or whitespace query", () => {
    const items = [
      edgeLibItem({ id: 3, slug: "c-job", name: "90-build-report.md" }),
      edgeLibItem({ id: 1, slug: "a-job", name: "00-overview.md" }),
      edgeLibItem({ id: 2, slug: "b-job", name: "10-architecture.html" }),
    ];
    for (const q of ["", " ", "   ", "\t", "\n", " \t\n "]) {
      const got = filterLibrary(items, q);
      expect(edgeLibNames(got), JSON.stringify(q)).toEqual(edgeLibNames(items));
      expect(got.map((i) => i.id), JSON.stringify(q)).toEqual([3, 1, 2]);
    }
  });

  it("returns an empty list for an empty input, whatever the query", () => {
    for (const q of ["", "overview", "   "]) {
      expect(filterLibrary([], q), q).toEqual([]);
    }
  });
});

// ---------------------------------------------------------------------------
// filterLibrary: matching
// ---------------------------------------------------------------------------

describe("filterLibrary: matching", () => {
  const edgeLibCorpus = () => [
    edgeLibItem({ id: 3, slug: "save-artifacts", name: "90-build-report.md" }),
    edgeLibItem({ id: 1, slug: "graph-view", name: "00-overview.md" }),
    edgeLibItem({ id: 2, slug: "save-artifacts", name: "10-architecture.html" }),
  ];

  // Obligation: "Case-insensitive substring match against `name` and `slug`."
  it("matches the name case-insensitively", () => {
    for (const q of ["overview", "OVERVIEW", "OverView"]) {
      expect(edgeLibNames(filterLibrary(edgeLibCorpus(), q)), q).toEqual(["00-overview.md"]);
    }
  });

  it("matches the slug case-insensitively", () => {
    for (const q of ["save-artifacts", "SAVE-ARTIFACTS", "Save-Artifacts"]) {
      expect(filterLibrary(edgeLibCorpus(), q).map((i) => i.id), q).toEqual([3, 2]);
    }
  });

  // Substring, not prefix: the interesting part of an artifact name is in the
  // middle of it ("00-overview.md"), and the numeric prefixes are shared by
  // every job's set.
  it("matches a substring anywhere in either field", () => {
    expect(edgeLibNames(filterLibrary(edgeLibCorpus(), "view"))).toEqual(["00-overview.md"]);
    expect(edgeLibNames(filterLibrary(edgeLibCorpus(), "build"))).toEqual(["90-build-report.md"]);
    expect(filterLibrary(edgeLibCorpus(), "artifact").map((i) => i.id)).toEqual([3, 2]);
  });

  // A query matching neither field is an empty list, not the whole library: a
  // filter that fell back to everything would look identical to a filter that
  // was not applied.
  it("returns an empty list when nothing matches", () => {
    for (const q of ["zzz", "00-overview.html", "graph-view/00-overview.md"]) {
      expect(filterLibrary(edgeLibCorpus(), q), q).toEqual([]);
    }
  });

  // Obligation: "never re-sorts". The input here is deliberately out of every
  // natural order (ids 3,1,2 and names 90,00,10), and the two survivors of the
  // filter must come out in the order they went in — the server's newest-first
  // order, which the client has no basis to recompute.
  it("preserves input order among the matches", () => {
    const got = filterLibrary(edgeLibCorpus(), "save-artifacts");
    expect(edgeLibNames(got)).toEqual(["90-build-report.md", "10-architecture.html"]);
  });

  // A filter is a read. Mutating or re-ordering the caller's array in place
  // would corrupt the very list the unfiltered view renders from.
  it("does not mutate the input array", () => {
    const items = edgeLibCorpus();
    const before = items.map((i) => i.id);
    filterLibrary(items, "overview");
    filterLibrary(items, "");
    expect(items.map((i) => i.id)).toEqual(before);
  });
});
