import { afterEach, describe, expect, it } from "vitest";

import { resolveSelectionSource, selectionSlug } from "../../lib/mind.ts";

// mindlayer.edge.test.tsx — the EDGE dimension of 50-contract.md slice D's
// pure units, `resolveSelectionSource` and `selectionSlug` (web/_ui/src/lib/mind.ts).
// Written blind from the contract: mind.ts, MindButton.tsx and the panes were
// not read. Boundaries only — a null anchor, an anchor with no
// `data-mind-kind` ancestor, nested kinds (innermost wins), a text-node anchor,
// and selectionSlug with nothing to find. athena-logic owns the happy paths.

function edgeMLMount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

function edgeMLTextIn(host: HTMLElement, selector: string): Node {
  const el = host.querySelector(selector);
  if (!el || !el.firstChild) throw new Error(`fixture lacks ${selector} text`);
  return el.firstChild;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("resolveSelectionSource — edges", () => {
  // "returns the source of the INNERMOST ancestor carrying `data-mind-kind`
  // …; no such ancestor → `null`" — a null anchor has no ancestors at all.
  it("returns null for a null anchor", () => {
    expect(resolveSelectionSource(null)).toBeNull();
  });

  // "no such ancestor → `null`" — a text node whose ancestors carry other
  // data-mind-* attributes but no data-mind-kind.
  it("returns null when no ancestor carries data-mind-kind", () => {
    const host = edgeMLMount(
      `<section data-mind-slug="orphan-slug" data-mind-session="s1"><p id="p">plain text</p></section>`,
    );
    expect(resolveSelectionSource(edgeMLTextIn(host, "#p"))).toBeNull();
  });

  it("returns null for a detached node", () => {
    const p = document.createElement("p");
    p.textContent = "floating";
    expect(resolveSelectionSource(p.firstChild)).toBeNull();
  });

  // Walks "from `anchor` up through `parentElement`s": a text node anchor
  // resolves through its parent element.
  it("resolves a text-node anchor through its parent elements", () => {
    const host = edgeMLMount(
      `<div data-mind-kind="artifact" data-mind-slug="j1" data-mind-home="pluto" data-mind-name="00-overview.md" data-mind-sha="abc"><div><span id="s">deep text</span></div></div>`,
    );
    expect(resolveSelectionSource(edgeMLTextIn(host, "#s"))).toMatchObject({
      kind: "artifact",
      slug: "j1",
      home: "pluto",
      name: "00-overview.md",
      sha: "abc",
    });
  });

  // INNERMOST wins: a transcript block inside an artifact body resolves as the
  // transcript, and an item row inside a transcript block resolves as the item.
  it("lets the innermost data-mind-kind win over an outer one", () => {
    const host = edgeMLMount(
      `<div data-mind-kind="artifact" data-mind-slug="j1" data-mind-home="pluto" data-mind-name="a.md" data-mind-sha="s">
         <div data-mind-kind="transcript" data-mind-session="sess-1" data-mind-uuid="uuid-1" data-mind-tid="t2">
           <p id="tx">transcript text</p>
           <div data-mind-kind="item" data-mind-item-id="17"><p id="it">item text</p></div>
         </div>
       </div>`,
    );
    const tx = resolveSelectionSource(edgeMLTextIn(host, "#tx"));
    expect(tx).toMatchObject({ kind: "transcript", session: "sess-1", uuid: "uuid-1", tid: "t2" });
    expect(tx?.name).toBeUndefined();

    const item = resolveSelectionSource(edgeMLTextIn(host, "#it"));
    expect(item).toMatchObject({ kind: "item", item_id: 17 });
    expect(item?.session).toBeUndefined();
  });
});

describe("selectionSlug — edges", () => {
  // "returning the nearest `data-mind-slug` or `""`".
  it('returns "" when no ancestor carries data-mind-slug', () => {
    const host = edgeMLMount(`<div data-mind-kind="item" data-mind-item-id="3"><p id="p">x</p></div>`);
    expect(selectionSlug(edgeMLTextIn(host, "#p"))).toBe("");
  });

  it('returns "" for a detached node', () => {
    const p = document.createElement("p");
    p.textContent = "floating";
    expect(selectionSlug(p.firstChild)).toBe("");
  });

  // "nearest": an inner slug wins over an outer one.
  it("returns the nearest slug when two ancestors carry one", () => {
    const host = edgeMLMount(
      `<div data-mind-slug="outer-slug"><div data-mind-slug="inner-slug"><p id="p">x</p></div></div>`,
    );
    expect(selectionSlug(edgeMLTextIn(host, "#p"))).toBe("inner-slug");
  });
});
