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

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { MMItem } from "./mm.ts";
import { artifactReadMatch, decorateMMTables, selectionText } from "./mm.ts";

/**
 * jsdom (26.x, this repo's version) does not implement `innerText` — it reads
 * `undefined` on every element. `decorateMMTables` is specified in terms of
 * `table.innerText`, so the sweep is given a real one here: textContent, which
 * is innerText's meaning for the flat table markup below. A harness
 * accommodation, not a contract reading; disclosed in the round report.
 */
beforeEach(() => {
  if (!Object.getOwnPropertyDescriptor(HTMLElement.prototype, "innerText")) {
    Object.defineProperty(HTMLElement.prototype, "innerText", {
      configurable: true,
      get(this: HTMLElement) {
        return this.textContent ?? "";
      },
      set(this: HTMLElement, v: string) {
        this.textContent = v;
      },
    });
  }
});

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

// ---------------------------------------------------------------------------
// decorateMMTables
// ---------------------------------------------------------------------------

const CONTRACT_TABLE_A =
  "<table><tbody><tr><td>table A first cell</td></tr></tbody></table>";
const CONTRACT_TABLE_B =
  "<table><tbody><tr><td>table B first cell</td></tr></tbody></table>";

function contractBody(html: string): HTMLElement {
  const body = document.createElement("div");
  body.className = "md";
  body.innerHTML = html;
  document.body.appendChild(body);
  return body;
}

/**
 * The affordance elements the sweep added.
 *
 * CONTRACT SILENCE: the row says "one corner ✓ affordance" and fixes neither
 * the tag nor the class. The strict reading asserted here is the literal one —
 * an element whose own trimmed text IS "✓" — since that character is what the
 * contract spells out.
 */
function contractMarks(body: HTMLElement): HTMLElement[] {
  const isMark = (el: Element | null) =>
    el !== null && (el.textContent ?? "").trim() === "✓";
  // The OUTERMOST element of a ✓-only chain, so a `<button><span>✓</span></button>`
  // counts once (as the button, which is what carries the state and the click)
  // rather than twice.
  return [...body.querySelectorAll<HTMLElement>("*")].filter(
    (el) => isMark(el) && !isMark(el.parentElement),
  );
}

describe("decorateMMTables", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  // "every <table> in bodyEl gets one corner ✓ affordance"
  it("gives every table exactly one affordance", () => {
    const body = contractBody(CONTRACT_TABLE_A + "<p>prose</p>" + CONTRACT_TABLE_B);
    decorateMMTables(body, [], () => {});
    expect(contractMarks(body)).toHaveLength(2);
  });

  it("adds nothing to a body with no tables", () => {
    const body = contractBody("<p>prose only</p><pre><code>not a table</code></pre>");
    const before = body.innerHTML;
    decorateMMTables(body, [], () => {});
    expect(body.innerHTML).toBe(before);
    expect(contractMarks(body)).toHaveLength(0);
  });

  // "clicking calls onMark(table.innerText)" — the table's own text IS the
  // element mark's identity, so what the click hands over has to be exactly it.
  it("hands the table's innerText to onMark on click", () => {
    const body = contractBody(CONTRACT_TABLE_A + CONTRACT_TABLE_B);
    const onMark = vi.fn();
    decorateMMTables(body, [], onMark);

    const tables = [...body.querySelectorAll("table")];
    const marks = contractMarks(body);
    expect(marks).toHaveLength(2);

    marks[0]!.click();
    expect(onMark).toHaveBeenCalledTimes(1);
    expect(onMark).toHaveBeenLastCalledWith(tables[0]!.innerText);

    marks[1]!.click();
    expect(onMark).toHaveBeenCalledTimes(2);
    expect(onMark).toHaveBeenLastCalledWith(tables[1]!.innerText);
    // the two tables are distinguishable, so the second click cannot have
    // reported the first table's text
    expect(onMark.mock.calls[0]![0]).not.toBe(onMark.mock.calls[1]![0]);
  });

  // "a table whose innerText appears in markedTexts renders in the marked
  // state" — the minimal, implementation-agnostic reading: the marked rendering
  // DIFFERS from the unmarked one.
  it("renders a marked table differently from an unmarked one", () => {
    const unmarked = contractBody(CONTRACT_TABLE_A);
    decorateMMTables(unmarked, [], () => {});

    const marked = contractBody(CONTRACT_TABLE_A);
    const text = marked.querySelector("table")!.innerText;
    decorateMMTables(marked, [text], () => {});

    expect(marked.innerHTML).not.toBe(unmarked.innerHTML);
  });

  /**
   * CONTRACT SILENCE: the row does not name the attribute or class that carries
   * the marked state. The strict reading asserted here is the one the SAME
   * contract table already fixes for the artifact head toggle — `aria-pressed`
   * — since the two are the same affordance one level down.
   */
  it("reflects the marked state as aria-pressed on the affordance", () => {
    const body = contractBody(CONTRACT_TABLE_A + CONTRACT_TABLE_B);
    const tables = [...body.querySelectorAll("table")];
    decorateMMTables(body, [tables[1]!.innerText], () => {});

    const marks = contractMarks(body);
    expect(marks).toHaveLength(2);
    expect(marks[0]!.getAttribute("aria-pressed")).toBe("false");
    expect(marks[1]!.getAttribute("aria-pressed")).toBe("true");
  });

  // "running twice adds nothing" — the artifact body is re-rendered on the 3s
  // board poll and the sweep runs again over the same DOM every time.
  it("is idempotent: a second sweep adds no elements", () => {
    const body = contractBody(CONTRACT_TABLE_A + "<p>prose</p>" + CONTRACT_TABLE_B);
    decorateMMTables(body, [], () => {});
    const afterFirst = body.innerHTML;
    const countAfterFirst = body.querySelectorAll("*").length;

    decorateMMTables(body, [], () => {});
    decorateMMTables(body, [], () => {});

    expect(body.querySelectorAll("*").length).toBe(countAfterFirst);
    expect(body.innerHTML).toBe(afterFirst);
    expect(contractMarks(body)).toHaveLength(2);
  });

  // Idempotent does NOT mean inert: a re-sweep with a changed markedTexts list
  // has to move the state, since the server's list is the only truth for the
  // toggle and a POST that lands mid-poll must show up on the next sweep.
  it("updates the marked state on a re-sweep without duplicating anything", () => {
    const body = contractBody(CONTRACT_TABLE_A);
    decorateMMTables(body, [], () => {});
    const count = body.querySelectorAll("*").length;

    const text = body.querySelector("table")!.innerText;
    decorateMMTables(body, [text], () => {});

    expect(body.querySelectorAll("*").length).toBe(count);
    expect(contractMarks(body)).toHaveLength(1);
    expect(contractMarks(body)[0]!.getAttribute("aria-pressed")).toBe("true");
  });

  // A re-sweep must keep the click working — an affordance replaced without its
  // handler is the silent version of this feature breaking.
  it("keeps the affordance clickable across re-sweeps", () => {
    const body = contractBody(CONTRACT_TABLE_A);
    const onMark = vi.fn();
    decorateMMTables(body, [], onMark);
    decorateMMTables(body, [], onMark);

    contractMarks(body)[0]!.click();
    expect(onMark).toHaveBeenCalledTimes(1);
    expect(onMark).toHaveBeenLastCalledWith(body.querySelector("table")!.innerText);
  });

  // A table that is NOT in markedTexts stays unmarked even when another one is
  // — the list is per-table, keyed on the table's own text.
  it("marks only the tables whose text is in the list", () => {
    const body = contractBody(CONTRACT_TABLE_A + CONTRACT_TABLE_B);
    const tables = [...body.querySelectorAll("table")];
    decorateMMTables(body, [tables[0]!.innerText], () => {});

    const marks = contractMarks(body);
    expect(marks[0]!.getAttribute("aria-pressed")).toBe("true");
    expect(marks[1]!.getAttribute("aria-pressed")).toBe("false");
  });
});
