// mm.contract.test.ts — athena-logic's BLIND contract tests for slice S4's pure
// module, `lib/mm.ts` (artifacts/50-contract.md).
//
// Authored from the frozen contract alone, before any implementation existed:
// lib/mm.ts, api/client.ts, hooks/useMM.ts and every component under
// components/ were never read — no Read, no grep, no git show. Every assertion
// transcribes a stated obligation of `artifactReadMatch`, `selectionText` or
// `decorateMMTables`; where the contract is silent this file says so in a
// comment and asserts the single strict reading, and every silence is disclosed
// in the round report.
//
// Dimension: CONTRACT/LOGIC — the stated rules and their happy paths. Malformed
// urls, duplicate rows, hostile markup and very long lists are athena-edge's
// ground in mm.edge.test.tsx.
//
// No JSX and no component mounting: the contract fixes this file's NAME
// (`mm.contract.test.ts`) and freezes only the three exports, so the surface
// wiring (the ✓ read head button, the poll gating, the MENTAL MODEL pane) is
// manual-gate territory.
//
// Every file-scope identifier here is prefixed `contract`.

import { beforeEach, describe, expect, it } from "vitest";
import type { MMItem } from "./mm.ts";
import { artifactReadMatch, selectionText } from "./mm.ts";

// ---------------------------------------------------------------------------
// artifactReadMatch
// ---------------------------------------------------------------------------

/**
 * One wire item, built field by field from the frozen `MMItem` shape rather
 * than from the app's fixtures module, so this file depends on nothing but the
 * contract.
 */
function contractMM(
  id: number,
  slug: string,
  sourceRef: string,
  over: Partial<MMItem> = {},
): MMItem {
  return {
    id,
    kind: "reading",
    source: "artifact",
    slug,
    source_ref: sourceRef,
    content: sourceRef,
    created_at: 1_700_000_000_000 + id,
    ...over,
  };
}

/** The library every matching test below searches. */
const contractItems: MMItem[] = [
  contractMM(7, "mental-model", "00-overview.md"),
  contractMM(8, "mental-model", "icons/deep/diagram.svg"),
  contractMM(9, "monolith-yelp", "00-overview.md"),
];

describe("artifactReadMatch", () => {
  // "parses /api/artifact/<slug>/<home>/<name>, compares slug + name"
  it("matches a live artifact url on slug and name", () => {
    expect(
      artifactReadMatch(contractItems, "/api/artifact/mental-model/cell/00-overview.md"),
    ).toEqual(contractItems[0]);
    expect(
      artifactReadMatch(contractItems, "/api/artifact/monolith-yelp/cell/00-overview.md"),
    ).toEqual(contractItems[2]);
  });

  // "a cell-home artifact name may itself contain slashes" — only the first two
  // segments split off, so the rest of the path is the name.
  it("keeps slashes inside the name segment", () => {
    expect(
      artifactReadMatch(contractItems, "/api/artifact/mental-model/cell/icons/deep/diagram.svg"),
    ).toEqual(contractItems[1]);
  });

  // "`home` deliberately ignored" — the same artifact reached through the
  // archive namespace is the same artifact.
  it("ignores the home segment entirely", () => {
    for (const home of ["cell", "pluto", "archive", "anything-at-all"]) {
      expect(
        artifactReadMatch(contractItems, `/api/artifact/mental-model/${home}/00-overview.md`),
      ).toEqual(contractItems[0]);
    }
  });

  // "leading slash trimmed" — a row stores the name with its leading slash
  // already trimmed, so a url whose name segment carries one still matches.
  it("trims a leading slash before comparing the name", () => {
    expect(
      artifactReadMatch(contractItems, "/api/artifact/mental-model/cell//00-overview.md"),
    ).toEqual(contractItems[0]);
  });

  // "matches only LIVE items" — the head button's aria-pressed comes from this,
  // and an invalidated mark must read as UNMARKED so carl can re-read the thing
  // that changed.
  it("never matches an invalidated item", () => {
    const dead: MMItem[] = [
      contractMM(7, "mental-model", "00-overview.md", {
        invalidated_at: 1_700_000_100_000,
        invalid_reason: "content-changed",
      }),
    ];
    expect(
      artifactReadMatch(dead, "/api/artifact/mental-model/cell/00-overview.md"),
    ).toBeNull();
    // ... and a live sibling in the same list still answers.
    expect(
      artifactReadMatch(
        [...dead, contractMM(11, "mental-model", "00-overview.md")],
        "/api/artifact/mental-model/cell/00-overview.md",
      )?.id,
    ).toBe(11);
  });

  // "matches only ... items with source `artifact`" — an ELEMENT mark is a
  // reading of one table or one selection, not of the document, so it must not
  // light the whole-artifact toggle.
  it("never matches an element, code, note or decision item", () => {
    for (const source of ["artifact-element", "code", "manual", "question", "agent-output"]) {
      const items: MMItem[] = [
        contractMM(7, "mental-model", "00-overview.md", { source }),
      ];
      expect(
        artifactReadMatch(items, "/api/artifact/mental-model/cell/00-overview.md"),
      ).toBeNull();
    }
  });

  it("returns null when no item matches", () => {
    expect(
      artifactReadMatch(contractItems, "/api/artifact/mental-model/cell/51-code-map.md"),
    ).toBeNull();
    expect(
      artifactReadMatch(contractItems, "/api/artifact/some-other-job/cell/00-overview.md"),
    ).toBeNull();
    expect(artifactReadMatch([], "/api/artifact/mental-model/cell/00-overview.md")).toBeNull();
  });

  // "null on ... unparseable url" — a url it cannot parse must never
  // accidentally match a row, since a false positive reads as "already read".
  it("returns null on a url that is not a live-artifact url", () => {
    for (const url of [
      "",
      "/api/artifact/mental-model",
      "/api/artifact/mental-model/cell",
      "/api/library/artifact/7/00-overview.md",
      "/api/board",
      "not a url at all",
    ]) {
      expect(artifactReadMatch(contractItems, url)).toBeNull();
    }
  });
});

// ---------------------------------------------------------------------------
// selectionText
// ---------------------------------------------------------------------------

describe("selectionText", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    window.getSelection()?.removeAllRanges();
  });

  // '"" when collapsed/none' — the selection-mark affordance is visible only
  // while a non-empty selection exists, and this is what decides that.
  it("is empty when nothing is selected", () => {
    document.body.innerHTML = "<p>some prose in the rendered body</p>";
    expect(selectionText()).toBe("");
  });

  it("is the current selection's string", () => {
    document.body.innerHTML = "<p id=a>the mental model is carl's</p>";
    const p = document.getElementById("a")!;
    const range = document.createRange();
    range.selectNodeContents(p);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    expect(selectionText()).toBe("the mental model is carl's");
  });

  // "trimmed" — a double-click drags the trailing space in, and the trimmed
  // form is what the element mark stores and later substring-matches.
  it("trims the selection", () => {
    document.body.innerHTML = "<p id=a>   padded selection   </p>";
    const p = document.getElementById("a")!;
    const range = document.createRange();
    range.selectNodeContents(p);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    expect(selectionText()).toBe("padded selection");
  });

  // A collapsed caret is not a selection.
  it("is empty for a collapsed caret", () => {
    document.body.innerHTML = "<p id=a>some prose</p>";
    const p = document.getElementById("a")!;
    const range = document.createRange();
    range.setStart(p.firstChild!, 3);
    range.collapse(true);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    expect(selectionText()).toBe("");
  });

  // A whitespace-only selection trims to "" and so is not a selection either.
  it("is empty for a whitespace-only selection", () => {
    document.body.innerHTML = "<p id=a>   </p>";
    const p = document.getElementById("a")!;
    const range = document.createRange();
    range.selectNodeContents(p);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    expect(selectionText()).toBe("");
  });
});

// `decorateMMTables` was retired with the board's one-shot marks (PR 2, Q10);
// its block of tests went with it.
