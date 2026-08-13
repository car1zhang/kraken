import { describe, expect, it } from "vitest";

import type { ThreadBlock } from "../api/types.ts";
import {
  THREAD_MODELS,
  deriveThreadTitle,
  threadAwaitingReply,
} from "./threadquick.ts";

// threadquick.edge.test.ts — the EDGE dimension of slice S2 of
// artifacts/50-contract.md: the boundaries and the refusals of the two pure
// helpers behind the question-first branch form.
//
// Written BLIND, against the frozen contract text alone — no implementation
// was read. The two obligations, verbatim:
//
//   deriveThreadTitle(seed):
//     (a) Take the FIRST line of `seed` containing non-whitespace; collapse
//         every whitespace run to a single space; trim.
//     (b) Empty / whitespace-only `seed` → "".
//     (c) If the collapsed line is ≤ 48 chars, return it unchanged.
//     (d) Else: head = t.slice(0, 48); i = head.lastIndexOf(" ");
//         base = i > 0 ? head.slice(0, i) : head;
//         return base.trimEnd() + "…"  (U+2026, appended only on a cut).
//
//   threadAwaitingReply(blocks, seed):
//     split = one past the index of the LAST block with `inherited` truthy
//     (0 when none). Own blocks = blocks.slice(split). Own empty → return
//     seed.trim() !== "". Own non-empty → return
//     blocks[blocks.length - 1].kind !== "text".
//
// Every fixture below is BUILT from those rules rather than eyeballed, so the
// expected value and its derivation sit next to each other. Where the contract
// is silent (CRLF seeds, non-breaking space, i === 0) the probe is OMITTED
// rather than widened — a probe that accepts several shapes pins none of them,
// and the silences are disclosed in /tmp/better-dialog-258-athena-edge.md.
//
// Module scope here is private, but package-level identifiers are still
// `edge`-prefixed: the convention is the repo's and a sibling blind author
// writes a `.contract.` file against the same module.

const edgeCut = "…"; // U+2026 HORIZONTAL ELLIPSIS, the one legal suffix

function edgeBlocks(...blocks: ThreadBlock[]): ThreadBlock[] {
  return blocks;
}

// The kind → awaiting table. `"text"` is the ONLY kind that settles a thread;
// the rule is a literal `!== "text"`, so every other kind — including ones
// this codebase has never seen, an empty kind, and near-misses that differ
// only in case or a trailing space — must read as still-pending. Typed
// explicitly so tsc keeps `kind` a string and `want` a boolean under
// `it.each`.
const edgeKindCases: [string, boolean][] = [
  ["user", true],
  ["tool", true],
  ["thinking", true],
  ["think", true],
  ["asst", true],
  ["", true],
  ["TEXT", true],
  ["text ", true],
  ["some-future-kind", true],
  ["text", false],
];

describe("THREAD_MODELS", () => {
  // Not a formatting detail: rule 43 says the picker renders THREAD_MODELS in
  // this order, so the array IS the UI. A re-sorted or extended array changes
  // what carl clicks without touching a component.
  it("is exactly the four aliases in the frozen order", () => {
    expect(Array.from(THREAD_MODELS)).toEqual([
      "haiku",
      "sonnet",
      "opus",
      "fable",
    ]);
  });

  // the picker's default is "opus"; an array that no longer contains it would
  // leave the stored default unselectable
  it("contains the initial default", () => {
    expect(THREAD_MODELS).toContain("opus");
  });
});

describe("deriveThreadTitle — the empty answers", () => {
  // Rule (b), and the reason branch submit needs it: effectiveTitle is
  // `newName.trim() || deriveThreadTitle(seed)`, and "" is the signal that
  // there is no title to send at all (inline note, no request). Anything
  // truthy returned for a blank seed would post a whitespace title instead.
  it.each([
    ["empty string", ""],
    ["one space", " "],
    ["spaces", "     "],
    ["tab", "\t"],
    ["newline", "\n"],
    ["blank lines", "\n\n\n"],
    ["mixed whitespace and newlines", "  \n\t \n   \n"],
  ])("%s → \"\"", (_name, seed) => {
    expect(deriveThreadTitle(seed)).toBe("");
  });
});

describe("deriveThreadTitle — line selection and collapsing", () => {
  // Rule (a) first clause. A seed pasted from anywhere routinely opens with a
  // blank line; taking line 0 unconditionally would derive "" for a seed that
  // plainly has a question in it.
  it("takes the first line containing non-whitespace, not the first line", () => {
    expect(deriveThreadTitle("\n   \nwhy does it retry\nand then what")).toBe(
      "why does it retry",
    );
  });

  it("ignores every later line, however much longer", () => {
    expect(deriveThreadTitle("short one\n" + "b".repeat(200))).toBe("short one");
  });

  // Rule (a), second and third clauses. A title is rendered on one line in a
  // chip, so an uncollapsed tab or run of spaces is a visibly broken title.
  it("collapses every whitespace run inside the chosen line to one space", () => {
    expect(deriveThreadTitle("the   daemon\tbumps \t twice")).toBe(
      "the daemon bumps twice",
    );
  });

  it("trims the chosen line", () => {
    expect(deriveThreadTitle("   trim me   ")).toBe("trim me");
    expect(deriveThreadTitle("\n  \n\t padded question \t\n")).toBe(
      "padded question",
    );
  });

  // Ordering inside the rule: collapse happens BEFORE the length test, so a
  // raw line well over 48 chars that collapses under it is returned WHOLE.
  // Measuring the raw line instead would truncate a title that fits.
  it("measures the collapsed line, not the raw one", () => {
    const raw = "one" + " ".repeat(15) + "two" + " ".repeat(15) + "three" +
      " ".repeat(15) + "four";
    expect(raw.length).toBeGreaterThan(48);
    const collapsed = "one two three four";
    expect(collapsed.length).toBeLessThanOrEqual(48);
    expect(deriveThreadTitle(raw)).toBe(collapsed);
  });
});

describe("deriveThreadTitle — the 48-char boundary", () => {
  // Rule (c) at its exact edge. `≤ 48` and `< 48` differ by one character and
  // by an ellipsis: an off-by-one truncates a title that fits perfectly.
  it("returns an exactly-48-char line unchanged, with no ellipsis", () => {
    const exact48 = "a".repeat(20) + " " + "b".repeat(27);
    expect(exact48.length).toBe(48);
    const got = deriveThreadTitle(exact48);
    expect(got).toBe(exact48);
    expect(got.endsWith(edgeCut)).toBe(false);
  });

  it("returns a 47-char line unchanged", () => {
    const s = "c".repeat(47);
    expect(s.length).toBe(47);
    expect(deriveThreadTitle(s)).toBe(s);
  });

  // Rule (d) at the first character past the edge, with a space to cut on.
  // head = 20 a's + " " + 27 b's; lastIndexOf(" ") = 20 (> 0); base = 20 a's.
  it("cuts a 49-char line at its last space and appends the ellipsis", () => {
    const with49 = "a".repeat(20) + " " + "b".repeat(28);
    expect(with49.length).toBe(49);
    expect(deriveThreadTitle(with49)).toBe("a".repeat(20) + edgeCut);
  });

  // Rule (d) with NO space to cut on: i === -1, so base is the whole 48-char
  // head — a hard cut. Falling back to the untruncated string here would put
  // an arbitrarily long title on a chip.
  it("hard-cuts a 49-char line with no spaces at 48", () => {
    const noSpace = "z".repeat(49);
    expect(deriveThreadTitle(noSpace)).toBe("z".repeat(48) + edgeCut);
  });

  it("hard-cuts a very long line with no spaces at 48", () => {
    const noSpace = "z".repeat(400);
    const got = deriveThreadTitle(noSpace);
    expect(got).toBe("z".repeat(48) + edgeCut);
    expect(got.length).toBe(49); // 48 chars + one ellipsis code point
  });

  // The space sitting exactly ON the cut index. head ends with it, so
  // lastIndexOf(" ") === 47 and base is the 47 chars before it; trimEnd then
  // has nothing left to do. Without the trimEnd (or with `slice(0, i + 1)`)
  // this renders as "…" hanging off a trailing space.
  it("cuts cleanly when the last space lands on the boundary", () => {
    const s = "d".repeat(47) + " " + "e".repeat(5);
    expect(s.length).toBeGreaterThan(48);
    expect(deriveThreadTitle(s)).toBe("d".repeat(47) + edgeCut);
  });

  // trimEnd's other job: several spaces before the cut point collapse to one
  // (rule a), and that one must not survive into the output.
  it("leaves no trailing space before the ellipsis", () => {
    const s = "word ".repeat(20); // collapses to 20 words, 99 chars
    const got = deriveThreadTitle(s);
    expect(got.endsWith(edgeCut)).toBe(true);
    expect(got.slice(0, -1).endsWith(" ")).toBe(false);
    expect(got.slice(0, -1).length).toBeLessThanOrEqual(48);
  });

  // The ellipsis is one U+2026 code point, never three periods: the pill and
  // the chip are width-budgeted around a single glyph.
  it("appends U+2026 and never three dots", () => {
    const got = deriveThreadTitle("z".repeat(60));
    expect(got.endsWith("…")).toBe(true);
    expect(got.includes("...")).toBe(false);
  });

  // The cut applies to the CHOSEN line, so a long first line is truncated even
  // when it was preceded by blank ones.
  it("cuts the chosen line even when blank lines came first", () => {
    const got = deriveThreadTitle("\n \n" + "y".repeat(80));
    expect(got).toBe("y".repeat(48) + edgeCut);
  });
});

describe("threadAwaitingReply — no own blocks", () => {
  // "a just-branched thread with a seed question is awaiting; one branched
  // idle is not". This is the poll's whole reason for existing: the fast
  // 1000ms interval must start the moment the question is sent, before a
  // single block has come back.
  it("no blocks at all, seed present → awaiting", () => {
    expect(threadAwaitingReply([], "why does it retry")).toBe(true);
  });

  it.each([
    ["empty seed", ""],
    ["spaces", "   "],
    ["tab and newline", "\t\n "],
  ])("no blocks at all, %s → not awaiting", (_name, seed) => {
    expect(threadAwaitingReply([], seed)).toBe(false);
  });

  // split === blocks.length when every block is inherited, so own is empty and
  // the seed decides — the same state as a thread with no blocks. A helper
  // that looked at blocks.length instead of the SPLIT would read a freshly
  // forked thread's inherited history as the thread having already answered.
  it("all-inherited blocks, seed present → awaiting", () => {
    const blocks = edgeBlocks(
      { kind: "user", inherited: true },
      { kind: "text", inherited: true },
    );
    expect(threadAwaitingReply(blocks, "and now this")).toBe(true);
  });

  it("all-inherited blocks, no seed → not awaiting", () => {
    const blocks = edgeBlocks(
      { kind: "user", inherited: true },
      { kind: "text", inherited: true },
    );
    expect(threadAwaitingReply(blocks, "")).toBe(false);
    expect(threadAwaitingReply(blocks, "   ")).toBe(false);
  });

  // The split is one past the LAST inherited block, not the first
  // non-inherited one, so an inherited block sitting after an own block still
  // empties the own slice and hands the decision back to the seed.
  it("an inherited block after an own block re-empties the own slice", () => {
    const blocks = edgeBlocks(
      { kind: "user" },
      { kind: "text", inherited: true },
    );
    expect(threadAwaitingReply(blocks, "still asking")).toBe(true);
    expect(threadAwaitingReply(blocks, "")).toBe(false);
  });

  it("a single inherited block behaves the same", () => {
    const blocks = edgeBlocks({ kind: "text", inherited: true });
    expect(threadAwaitingReply(blocks, "q")).toBe(true);
    expect(threadAwaitingReply(blocks, "")).toBe(false);
  });
});

describe("threadAwaitingReply — own blocks present", () => {
  // Own non-empty: the seed is IGNORED and the last block's kind decides.
  // A trailing user/tool/thinking block means the answer is still coming, so
  // the poll must stay fast; only a trailing "text" block means claude has
  // finished talking.
  it.each(edgeKindCases)("a trailing %s block → %s", (kind, want) => {
    const blocks = edgeBlocks({ kind: "text", inherited: true }, { kind });
    expect(threadAwaitingReply(blocks, "")).toBe(want);
    // the seed must not sway the answer once the thread has said something
    expect(threadAwaitingReply(blocks, "a leftover seed")).toBe(want);
  });

  // The last block of the whole array, which with a non-empty own slice is
  // also the last OWN block. An earlier "text" must not settle it.
  it("only the LAST block decides", () => {
    const settled = edgeBlocks(
      { kind: "user" },
      { kind: "tool" },
      { kind: "text" },
    );
    expect(threadAwaitingReply(settled, "")).toBe(false);

    const pending = edgeBlocks(
      { kind: "user" },
      { kind: "text" },
      { kind: "tool" },
    );
    expect(threadAwaitingReply(pending, "")).toBe(true);
  });

  // `inherited` is read for TRUTHINESS, so an explicitly false (or absent)
  // flag leaves the block in the own slice. Treating the KEY's presence as the
  // signal would push the split past every block the server tagged
  // `inherited: false` and make a live thread look idle.
  it("inherited: false leaves a block in the own slice", () => {
    const blocks = edgeBlocks({ kind: "text", inherited: false });
    expect(threadAwaitingReply(blocks, "a seed")).toBe(false);

    const pending = edgeBlocks({ kind: "user", inherited: false });
    expect(threadAwaitingReply(pending, "")).toBe(true);
  });

  it("an untagged block is an own block", () => {
    expect(threadAwaitingReply(edgeBlocks({ kind: "text" }), "a seed")).toBe(
      false,
    );
    expect(threadAwaitingReply(edgeBlocks({ kind: "user" }), "")).toBe(true);
  });
});
