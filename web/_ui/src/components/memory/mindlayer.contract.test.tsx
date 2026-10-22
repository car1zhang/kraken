// mindlayer.contract.test.tsx — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (PR 2, the mental-model rework), slice D (web):
// `resolveSelectionSource` / `selectionSlug` (lib/mind.ts) and the Model
// pane's title fallback and scope chip (MentalModelPane with a mocked
// `/api/mm` payload). Written from the frozen contract alone: lib/mind.ts,
// MentalModelPane.tsx, MemoryView.tsx and api/client.ts were never opened.
//
// Dimension: LOGIC — the stated happy paths. Boundaries are athena-edge's.
// Every module-scope identifier declared here is prefixed `contract`.

import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { resolveSelectionSource, selectionSlug } from "../../lib/mind.ts";
import type { MMModel } from "../../hooks/useMM.ts";
import { MentalModelPane } from "./MentalModelPane.tsx";

// contractEl builds a detached tree from html and returns the element with
// data-testid="anchor" (or its first text node when textNode is true).
function contractAnchor(html: string, textNode = true): Node {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  const el = host.querySelector('[data-testid="anchor"]');
  if (!el) throw new Error("fixture has no anchor");
  return textNode && el.firstChild ? el.firstChild : el;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("resolveSelectionSource", () => {
  // "`artifact` → `{kind:"artifact", slug: data-mind-slug, home: data-mind-home,
  // name: data-mind-name, sha: data-mind-sha}`"
  it("resolves an artifact ancestor to its four attributes", () => {
    const anchor = contractAnchor(
      `<div id="panel-body" data-mind-kind="artifact" data-mind-slug="job-7" data-mind-home="cell" data-mind-name="50-components.md" data-mind-sha="abc123">
         <section><p><span data-testid="anchor">the highlighted words</span></p></section>
       </div>`,
    );
    expect(resolveSelectionSource(anchor)).toEqual({
      kind: "artifact",
      slug: "job-7",
      home: "cell",
      name: "50-components.md",
      sha: "abc123",
    });
  });

  // "`transcript` → `{kind:"transcript", session, uuid, tid?}` from
  // `data-mind-session`/`data-mind-uuid`/`data-mind-tid`"
  it("resolves a transcript block, with and without a tid", () => {
    const steer = contractAnchor(
      `<div data-mind-kind="transcript" data-mind-session="zeus-sess" data-mind-uuid="u-1"><p data-testid="anchor">zeus said</p></div>`,
    );
    expect(resolveSelectionSource(steer)).toEqual({
      kind: "transcript",
      session: "zeus-sess",
      uuid: "u-1",
    });
    document.body.innerHTML = "";
    const thread = contractAnchor(
      `<div data-mind-kind="transcript" data-mind-session="thread-sess" data-mind-uuid="u-2" data-mind-tid="t3"><p data-testid="anchor">thread said</p></div>`,
    );
    expect(resolveSelectionSource(thread)).toEqual({
      kind: "transcript",
      session: "thread-sess",
      uuid: "u-2",
      tid: "t3",
    });
  });

  // "`item` → `{kind:"item", item_id}`"
  it("resolves a Model pane row to an item source with a numeric id", () => {
    const anchor = contractAnchor(
      `<ul><li data-mind-kind="item" data-mind-item-id="42"><span data-testid="anchor">a title</span></li></ul>`,
    );
    expect(resolveSelectionSource(anchor)).toEqual({
      kind: "item",
      item_id: 42,
    });
  });

  // "returns the source of the INNERMOST ancestor carrying `data-mind-kind`"
  it("prefers the innermost data-mind-kind ancestor", () => {
    const anchor = contractAnchor(
      `<div data-mind-kind="artifact" data-mind-slug="outer" data-mind-home="cell" data-mind-name="x.md" data-mind-sha="s">
         <div data-mind-kind="transcript" data-mind-session="inner-sess" data-mind-uuid="inner-uuid">
           <span data-testid="anchor">nested</span>
         </div>
       </div>`,
    );
    expect(resolveSelectionSource(anchor)).toEqual({
      kind: "transcript",
      session: "inner-sess",
      uuid: "inner-uuid",
    });
  });

  // "Walks from `anchor` up through `parentElement`s" — an element anchor works
  // as well as a text node; "no such ancestor → `null`".
  it("accepts an element anchor and answers null with no ancestor", () => {
    const el = contractAnchor(
      `<div data-mind-kind="item" data-mind-item-id="7"><b data-testid="anchor">x</b></div>`,
      false,
    );
    expect(resolveSelectionSource(el)).toEqual({ kind: "item", item_id: 7 });
    document.body.innerHTML = "";
    const bare = contractAnchor(
      `<div><span data-testid="anchor">no source here</span></div>`,
    );
    expect(resolveSelectionSource(bare)).toBeNull();
    expect(resolveSelectionSource(null)).toBeNull();
  });
});

describe("selectionSlug", () => {
  // "`selectionSlug(anchor)` returning the nearest `data-mind-slug` or `""`"
  it("returns the nearest data-mind-slug, else empty", () => {
    const anchor = contractAnchor(
      `<div data-mind-slug="outer-job"><div data-mind-kind="artifact" data-mind-slug="inner-job" data-mind-home="cell" data-mind-name="a.md" data-mind-sha="s"><span data-testid="anchor">t</span></div></div>`,
    );
    expect(selectionSlug(anchor)).toBe("inner-job");
    document.body.innerHTML = "";
    const steer = contractAnchor(
      `<div data-mind-slug="steer-job"><div data-mind-kind="transcript" data-mind-session="s" data-mind-uuid="u"><span data-testid="anchor">t</span></div></div>`,
    );
    expect(selectionSlug(steer)).toBe("steer-job");
    document.body.innerHTML = "";
    const none = contractAnchor(
      `<div><span data-testid="anchor">t</span></div>`,
    );
    expect(selectionSlug(none)).toBe("");
  });
});

// ---------------------------------------------------------------------------
// Model pane
// ---------------------------------------------------------------------------

function contractItem(over: Record<string, unknown>): Record<string, unknown> {
  return {
    id: 1,
    kind: "note",
    source: "capture",
    slug: "",
    source_ref: "",
    content: "",
    context: "",
    created_at: Date.now() - 60_000,
    episode_id: 0,
    title: "",
    scope: "repo",
    repo: "",
    dir: "",
    quote: "",
    thread_session: "",
    revised_at: 0,
    ...over,
  };
}

const contractItems = [
  contractItem({
    id: 11,
    title: "the daemon owns the render",
    content: "body eleven",
    scope: "repo",
    repo: "kraken",
    dir: "internal/jobs",
  }),
  contractItem({
    id: 12,
    title: "",
    source: "manual",
    content: "legacy first line\nlegacy second line",
    scope: "repo",
    repo: "",
    dir: "",
  }),
  contractItem({
    id: 13,
    title: "always quote verbatim",
    content: "u",
    scope: "universal",
  }),
  contractItem({
    id: 14,
    title: "root of monolith",
    content: "r",
    scope: "repo",
    repo: "monolith",
    dir: "",
  }),
];

function contractJSON(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("MentalModelPane", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        if (url.includes("/api/mm/flags"))
          return Promise.resolve(contractJSON({ flags: [] }));
        if (url.includes("/api/mm/snapshot"))
          return Promise.resolve(contractJSON({}));
        if (url.includes("/api/mm"))
          return Promise.resolve(contractJSON({ items: contractItems }));
        return Promise.resolve(contractJSON({}));
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // Amendment 5: the named export, props `{hidden, mm, onAddNote, onRemove,
  // onOpenJob?}`, `mm` an MMModel `{items, error, loaded, refetch}`, rows
  // rendered from `mm.items`; its own polls go through global `fetch`.
  function contractRenderPane() {
    const mm = {
      items: contractItems,
      error: undefined,
      loaded: true,
      refetch: () => Promise.resolve(true),
    } as unknown as MMModel;
    return render(
      <MentalModelPane
        hidden={false}
        mm={mm}
        onAddNote={() => Promise.resolve()}
        onRemove={() => Promise.resolve()}
        onConfirm={() => Promise.resolve()}
        onOpenJob={() => {}}
      />,
    );
  }

  // "each row shows `title` (fallback: first line of `content`)"
  it("renders the title, falling back to the first content line", async () => {
    contractRenderPane();
    expect(
      await screen.findByText("the daemon owns the render"),
    ).toBeInTheDocument();
    expect(await screen.findByText("legacy first line")).toBeInTheDocument();
    expect(screen.getByText("always quote verbatim")).toBeInTheDocument();
  });

  // "a scope chip reading `universal` or `<repo>/<dir>` (`<repo>` alone for the
  // repo root)"
  it("renders scope chips", async () => {
    contractRenderPane();
    await screen.findByText("the daemon owns the render");
    await waitFor(() => {
      expect(screen.getByText("kraken/internal/jobs")).toBeInTheDocument();
    });
    expect(screen.getAllByText("universal").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("monolith")).toBeInTheDocument();
  });
});
