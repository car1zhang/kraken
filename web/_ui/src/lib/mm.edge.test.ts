import { describe, expect, it, vi } from "vitest";

import { artifactReadMatch, decorateMMTables, type MMItem } from "./mm.ts";

// mm.edge.test.ts — the EDGE dimension of 50-contract.md's Slice S4 pure module:
// every url `artifactReadMatch` must answer null for, every item it must refuse
// to match, and `decorateMMTables` on the two bodies that break a naive sweep —
// one with no tables at all, and one that is swept twice. Written blind from the
// frozen contract, with no sight of web/_ui/src/lib/mm.ts.
//
// The sibling blind author (athena-logic) owns the stated happy paths — a
// library url matching its item, a table decorated once. What lives here is the
// other half: the null arms, the liveness filter, and the idempotence that the
// 3s board poll makes load-bearing rather than tidy.
//
// Every module-scope identifier is prefixed `edgeMM`; this file's scope is
// private, but the convention is the repo's and the sibling blind file shares
// the surface.

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

let edgeMMNextID = 1;

function edgeMMItem(over: Partial<MMItem> = {}): MMItem {
  return {
    id: over.id ?? edgeMMNextID++,
    kind: "reading",
    source: "artifact",
    slug: "a-job",
    source_ref: "00-overview.md",
    content: "00-overview.md",
    created_at: 1_770_000_000_000,
    ...over,
  };
}

// ---------------------------------------------------------------------------
// artifactReadMatch: urls that are not artifact urls
// ---------------------------------------------------------------------------

describe("artifactReadMatch: unparseable urls", () => {
  // The ✓ head button asks this question on EVERY artifact overlay open, so a
  // url shape it does not understand has to read as "not read yet" rather than
  // as an accidental match against whatever item happened to sort first — which
  // would render the button pressed for a doc carl has never opened.
  it("returns null for every url outside the artifact family", () => {
    const items = [
      edgeMMItem({ slug: "a-job", source_ref: "00-overview.md" }),
      edgeMMItem({ slug: "b-job", source_ref: "10-arch.html" }),
    ];
    for (const url of [
      "",
      "/",
      "/api/board",
      "/api/artifact",
      "/api/artifact/",
      "/api/artifacts/a-job/pluto/00-overview.md",
      "/api/library/artifact/1/00-overview.md",
      "/api/upload/a-job/img-001.png",
      "/api/mm/snapshot?id=1",
      "https://example.com/api/artifact/a-job/pluto/00-overview.md",
    ]) {
      expect(artifactReadMatch(items, url), url).toBeNull();
    }
  });

  // A relative path is a caller bug rather than a variant to tolerate: every url
  // the client holds comes from the server's own artifact route, and those are
  // absolute.
  it("returns null for a relative path", () => {
    const items = [edgeMMItem()];
    expect(artifactReadMatch(items, "api/artifact/a-job/pluto/00-overview.md")).toBeNull();
  });

  // The url must carry all three segments. A two-segment url names a home but no
  // artifact, and there is nothing there to have read.
  it("returns null when the name segment is missing or empty", () => {
    const items = [edgeMMItem()];
    for (const url of [
      "/api/artifact/a-job",
      "/api/artifact/a-job/pluto",
      "/api/artifact/a-job/pluto/",
    ]) {
      expect(artifactReadMatch(items, url), url).toBeNull();
    }
  });

  // The state the pane is in before the first mark, and the state every fresh
  // install stays in.
  it("returns null for an empty item list", () => {
    expect(artifactReadMatch([], "/api/artifact/a-job/pluto/00-overview.md")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// artifactReadMatch: items that must not match
// ---------------------------------------------------------------------------

describe("artifactReadMatch: items that must not match", () => {
  // Both halves of the identity are required. Two jobs commonly carry the same
  // artifact NAME — 00-overview.md is in every spec job — so matching on name
  // alone would mark every overview read the moment one of them was.
  it("requires both the slug and the name", () => {
    const items = [edgeMMItem({ slug: "a-job", source_ref: "00-overview.md" })];
    expect(artifactReadMatch(items, "/api/artifact/other-job/pluto/00-overview.md")).toBeNull();
    expect(artifactReadMatch(items, "/api/artifact/a-job/pluto/10-arch.html")).toBeNull();
  });

  // A nested artifact's url carries its whole relative path, and the basename
  // alone is a different artifact.
  it("does not match a nested name by its basename", () => {
    const items = [edgeMMItem({ slug: "a-job", source_ref: "app.png" })];
    expect(artifactReadMatch(items, "/api/artifact/a-job/cell/icons/app.png")).toBeNull();
  });

  // The whole-artifact mark and the element mark are different claims — "I read
  // this doc" versus "I read this table" — and the head button reports only the
  // first. An element mark lighting the doc button would tell carl he has read a
  // document he has seen one paragraph of.
  it("matches only items whose source is `artifact`", () => {
    for (const source of ["artifact-element", "code", "question", "agent-output", "manual"]) {
      const items = [edgeMMItem({ source, slug: "a-job", source_ref: "00-overview.md" })];
      expect(
        artifactReadMatch(items, "/api/artifact/a-job/pluto/00-overview.md"),
        source,
      ).toBeNull();
    }
  });

  // Invalidation is the server saying the doc has changed since carl read it, so
  // the button must go back to unpressed — that transition IS the feature. A
  // matcher ignoring invalidated_at would leave it lit forever and the rewrite
  // would never be surfaced.
  it("never matches an invalidated item", () => {
    const items = [
      edgeMMItem({
        slug: "a-job",
        source_ref: "00-overview.md",
        invalidated_at: 1_770_000_100_000,
        invalid_reason: "content-changed",
      }),
    ];
    expect(artifactReadMatch(items, "/api/artifact/a-job/pluto/00-overview.md")).toBeNull();
  });

  // The re-mark case: the dead row and the fresh one coexist in the list (an
  // invalidation hides, it never deletes), so the matcher has to pick the live
  // one rather than the first one.
  it("picks the live item when a dead one shares its identity", () => {
    const dead = edgeMMItem({
      id: 1,
      slug: "a-job",
      source_ref: "00-overview.md",
      invalidated_at: 1_770_000_100_000,
    });
    const live = edgeMMItem({ id: 2, slug: "a-job", source_ref: "00-overview.md" });
    expect(artifactReadMatch([dead, live], "/api/artifact/a-job/pluto/00-overview.md")?.id).toBe(2);
    expect(artifactReadMatch([live, dead], "/api/artifact/a-job/pluto/00-overview.md")?.id).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// artifactReadMatch: home is not part of the identity
// ---------------------------------------------------------------------------

describe("artifactReadMatch: home and slash normalization", () => {
  // A row records the slug and the name it was frozen under; the same artifact
  // reached through another namespace is the same artifact. This is what keeps a
  // mark alive across the one migration every job eventually makes — cell home
  // while it runs, pluto once it is done, archive once it is filed — none of
  // which is a change to what carl read.
  it("ignores the home segment entirely", () => {
    const items = [edgeMMItem({ slug: "a-job", source_ref: "00-overview.md" })];
    for (const home of ["cell", "pluto", "archive", "anything-at-all"]) {
      expect(
        artifactReadMatch(items, `/api/artifact/a-job/${home}/00-overview.md`)?.id,
        home,
      ).toBe(items[0]!.id);
    }
  });

  // A cell-home artifact name may itself contain slashes, so only the first two
  // segments split off and the rest is the name.
  it("keeps slashes inside the name", () => {
    const items = [edgeMMItem({ slug: "a-job", source_ref: "icons/app.png" })];
    expect(artifactReadMatch(items, "/api/artifact/a-job/cell/icons/app.png")?.id).toBe(
      items[0]!.id,
    );
  });

  // The trim is stated in the contract, and this is the row it is for: a
  // source_ref that kept its leading slash is the same artifact as one that did
  // not, and without the trim the button would be unpressed for a doc that IS
  // marked.
  it("trims a leading slash before comparing", () => {
    const items = [edgeMMItem({ slug: "a-job", source_ref: "/00-overview.md" })];
    expect(artifactReadMatch(items, "/api/artifact/a-job/pluto/00-overview.md")?.id).toBe(
      items[0]!.id,
    );
  });
});

// ---------------------------------------------------------------------------
// decorateMMTables
// ---------------------------------------------------------------------------

const edgeMMHost = (html: string): HTMLElement => {
  const div = document.createElement("div");
  div.innerHTML = html;
  return div;
};

const edgeMMCount = (host: HTMLElement) => host.querySelectorAll("*").length;

// One-cell tables with no whitespace between the tags: the cell's text is the
// table's text under any rendering, which is what lets the click assertion below
// pin an exact string in jsdom (where `innerText` is not implemented and the
// implementation's documented fallback is what runs).
const edgeMMTable = (text: string) => `<table><tbody><tr><td>${text}</td></tr></tbody></table>`;

describe("decorateMMTables: bodies with nothing to decorate", () => {
  // The overwhelmingly common artifact: prose, headings and code, no tables. The
  // sweep runs on every render of every artifact, so "no tables" has to be a
  // no-op rather than a throw — a throw here takes the whole overlay down.
  it("adds nothing and throws nothing on a body with zero tables", () => {
    const onMark = vi.fn();
    const host = edgeMMHost("<h1>title</h1><p>prose</p><pre><code>code</code></pre>");
    const before = edgeMMCount(host);
    const html = host.innerHTML;

    expect(() => decorateMMTables(host, [], onMark)).not.toThrow();

    expect(edgeMMCount(host)).toBe(before);
    expect(host.innerHTML).toBe(html);
    expect(onMark).not.toHaveBeenCalled();
  });

  // An empty body is what the artifact overlay renders for the one frame between
  // opening and the markdown arriving.
  it("handles an empty body", () => {
    const onMark = vi.fn();
    const host = edgeMMHost("");
    expect(() => decorateMMTables(host, [], onMark)).not.toThrow();
    expect(host.innerHTML).toBe("");
    expect(onMark).not.toHaveBeenCalled();
  });
});

describe("decorateMMTables: idempotence", () => {
  // The board polls every 3s and the artifact body is re-swept behind it, so this
  // function runs many times over one rendered document. "Running twice adds
  // nothing" is therefore the normal case rather than a defensive one: without
  // it, an open artifact accumulates a ✓ per poll until the corner is a column of
  // them.
  it("adds exactly one affordance per table, however many times it runs", () => {
    const onMark = vi.fn();
    const host = edgeMMHost(edgeMMTable("alpha") + "<p>prose</p>" + edgeMMTable("beta"));

    decorateMMTables(host, [], onMark);
    const afterFirst = edgeMMCount(host);
    const htmlAfterFirst = host.innerHTML;
    expect(host.querySelectorAll("button").length).toBe(2);

    decorateMMTables(host, [], onMark);
    decorateMMTables(host, [], onMark);

    expect(edgeMMCount(host)).toBe(afterFirst);
    expect(host.innerHTML).toBe(htmlAfterFirst);
    expect(host.querySelectorAll("button").length).toBe(2);
    expect(onMark).not.toHaveBeenCalled();
  });

  // A re-sweep carrying a NEWER marked set must move the state rather than add a
  // second affordance: the write refetches, so the poll after a mark is exactly
  // this call.
  it("re-renders the marked state on a later sweep without adding elements", () => {
    const onMark = vi.fn();
    const host = edgeMMHost(edgeMMTable("alpha"));

    decorateMMTables(host, [], onMark);
    const count = edgeMMCount(host);
    const unmarked = host.querySelector("button")!.outerHTML;

    decorateMMTables(host, ["alpha"], onMark);

    expect(edgeMMCount(host)).toBe(count);
    expect(host.querySelectorAll("button").length).toBe(1);
    expect(host.querySelector("button")!.outerHTML).not.toBe(unmarked);
  });
});

describe("decorateMMTables: clicking and marked state", () => {
  // The affordance's whole payload is the table's own text. Asserted as an exact
  // string, because what the click hands over is what gets frozen as the item's
  // content and substring-searched by the verify pass forever after — a callback
  // that passed the surrounding prose, or the ✓ glyph it just inserted, would
  // freeze a body of text that never matches the document again.
  it("calls onMark with exactly the clicked table's text", () => {
    const onMark = vi.fn();
    const host = edgeMMHost(edgeMMTable("alpha") + edgeMMTable("beta"));
    decorateMMTables(host, [], onMark);

    const buttons = host.querySelectorAll("button");
    expect(buttons.length).toBe(2);

    (buttons[1] as HTMLElement).click();
    expect(onMark).toHaveBeenCalledTimes(1);
    expect(onMark).toHaveBeenCalledWith("beta");

    (buttons[0] as HTMLElement).click();
    expect(onMark).toHaveBeenCalledTimes(2);
    expect(onMark).toHaveBeenLastCalledWith("alpha");
  });

  // A marked table must LOOK different from an unmarked one — that difference is
  // the whole answer to "have I already marked this?" — and the two must not be
  // confusable, which is why this asserts on the rendered affordance rather than
  // on a class name the contract never fixes.
  it("renders a marked table differently from an unmarked one", () => {
    const onMark = vi.fn();
    const host = edgeMMHost(edgeMMTable("alpha") + edgeMMTable("beta"));
    decorateMMTables(host, ["alpha"], onMark);

    const buttons = host.querySelectorAll("button");
    expect(buttons.length).toBe(2);
    expect(buttons[0]!.outerHTML).not.toBe(buttons[1]!.outerHTML);
  });

  // The spelling of that difference. `aria-pressed` is what the contract names
  // for the artifact head's ✓ ("aria-pressed from artifactReadMatch"), and a
  // toggle in the same feature spelling its state a different way is both an
  // accessibility hole and a second thing to keep in step. The contract does not
  // say it outright for this affordance, so this case is the strict reading —
  // see the report's silence list.
  it("reports the marked state through aria-pressed", () => {
    const onMark = vi.fn();
    const host = edgeMMHost(edgeMMTable("alpha") + edgeMMTable("beta"));
    decorateMMTables(host, ["alpha"], onMark);

    const buttons = host.querySelectorAll("button");
    expect(buttons[0]!.getAttribute("aria-pressed")).toBe("true");
    expect(buttons[1]!.getAttribute("aria-pressed")).toBe("false");
  });

  // markedTexts is compared against the table's own text, so an entry naming
  // some OTHER table's text leaves this one unmarked. Without the comparison
  // being per-table, one mark anywhere in the document would light every table
  // in it.
  it("marks only the tables whose text is in the marked set", () => {
    const onMark = vi.fn();
    const host = edgeMMHost(edgeMMTable("alpha") + edgeMMTable("beta"));
    decorateMMTables(host, ["something else entirely"], onMark);

    const buttons = host.querySelectorAll("button");
    expect(buttons[0]!.outerHTML).toBe(buttons[1]!.outerHTML);
  });
});
