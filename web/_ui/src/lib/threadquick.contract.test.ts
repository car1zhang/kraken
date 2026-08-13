import { describe, expect, it } from "vitest";

import type { ThreadBlock } from "../api/types.ts";
import {
  THREAD_MODELS,
  deriveThreadTitle,
  threadAwaitingReply,
} from "./threadquick.ts";

// threadquick.contract.test.ts — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S2's pure half: THREAD_MODELS, deriveThreadTitle and
// threadAwaitingReply.
//
// Written from the contract text alone; threadquick.ts, ThreadOverlay.tsx,
// types.ts and client.ts were never read in their working-tree form (types.ts
// and ThreadOverlay.tsx only at HEAD, for ThreadBlock's existing shape and
// threadSplitIndex's stated rule, which threadAwaitingReply reuses).
//
// Dimension: LOGIC — the stated obligations and their happy paths. A blind
// TypeScript suite can only reach exports, so the branch form, the model pill
// and the poll interval (all component-internal) are out of reach here and are
// disclosed as such in the report rather than probed loosely through the DOM.
//
// Every identifier declared at module scope is prefixed `contract` — a sibling
// author writes an edge file beside this one.

// contract48 is exactly 48 characters — the boundary rule (c) names. Spelled
// as a constant with its own length assertion so an arithmetic slip in this
// file fails as itself rather than as a false verdict on the implementation.
const contract48 = "the daemon bumps the board twice per tick and no";
// contract45 is contract48 cut at its last space: rule (d)'s `base` for any
// input whose first 48 characters are contract48.
const contract45 = "the daemon bumps the board twice per tick and";

// contractBlock builds one ThreadBlock without repeating the optional-field
// noise at every call site.
function contractBlock(kind: string, inherited?: boolean): ThreadBlock {
  return inherited ? { kind, inherited: true } : { kind };
}

describe("contract: THREAD_MODELS", () => {
  // The order is not cosmetic — the contract states the picker renders them in
  // this order, so the array IS the picker's layout. A Set, an object's key
  // order, or an alphabetical sort would all satisfy "these four" and change
  // what carl sees.
  it("is exactly the four aliases, in order", () => {
    expect(THREAD_MODELS).toEqual(["haiku", "sonnet", "opus", "fable"]);
  });

  it("has no extra entries", () => {
    expect(THREAD_MODELS.length).toBe(4);
  });
});

describe("contract: deriveThreadTitle", () => {
  // rule (b)
  it("returns empty for an empty or whitespace-only seed", () => {
    expect(deriveThreadTitle("")).toBe("");
    expect(deriveThreadTitle("   ")).toBe("");
    expect(deriveThreadTitle("\n\n\n")).toBe("");
    expect(deriveThreadTitle("  \t \n \t  \n")).toBe("");
  });

  // rule (a): the FIRST line with non-whitespace, not the first line
  it("takes the first line containing non-whitespace", () => {
    expect(deriveThreadTitle("why is it blocked")).toBe("why is it blocked");
    expect(deriveThreadTitle("why is it blocked\nand for how long")).toBe(
      "why is it blocked",
    );
    expect(deriveThreadTitle("\n\n   \n  real content here\nlater")).toBe(
      "real content here",
    );
  });

  // rule (a): collapse every whitespace run, then trim
  it("collapses whitespace runs and trims", () => {
    expect(deriveThreadTitle("  why   is  it blocked  ")).toBe(
      "why is it blocked",
    );
    expect(deriveThreadTitle("a\t\t\tb")).toBe("a b");
    expect(deriveThreadTitle("\n   what   does\tthe daemon do?  \nnext")).toBe(
      "what does the daemon do?",
    );
  });

  // rule (c): the boundary itself. 48 is inclusive — "≤ 48 chars, return it
  // unchanged" — so an off-by-one here truncates a title that fits.
  it("returns a line of 48 characters or fewer unchanged", () => {
    expect(contract48.length).toBe(48);
    expect(deriveThreadTitle(contract48)).toBe(contract48);
    expect(deriveThreadTitle("short one")).toBe("short one");
  });

  // rule (d): cut at the last space inside the first 48 characters, then the
  // ellipsis. The ellipsis is U+2026, one character — not three dots.
  it("cuts a longer line at the last space and appends an ellipsis", () => {
    expect(contract45.length).toBe(45);
    const long =
      "the daemon bumps the board twice per tick and nobody knows why";
    expect(long.length).toBeGreaterThan(48);
    expect(deriveThreadTitle(long)).toBe(contract45 + "…");

    // 49 characters — one past the boundary, so the shortest input that cuts
    const justOver = contract48 + "b";
    expect(justOver.length).toBe(49);
    expect(deriveThreadTitle(justOver)).toBe(contract45 + "…");
  });

  it("appends the ellipsis only on a cut", () => {
    expect(deriveThreadTitle(contract48)).not.toContain("…");
    expect(deriveThreadTitle("why is it blocked")).not.toContain("…");
  });

  // rule (d), the i <= 0 arm: a first 48 characters with no space at all has
  // no word boundary to cut at, so the head itself is the base. A naive
  // lastIndexOf that fell through to slice(0, -1) would drop a character.
  it("keeps the whole 48-character head when it holds no space", () => {
    const runOn = "x".repeat(60);
    expect(deriveThreadTitle(runOn)).toBe("x".repeat(48) + "…");
  });

  // the collapse happens BEFORE the length test, so a line that is long only
  // because of its whitespace must not be cut
  it("measures length after collapsing, not before", () => {
    const padded = "why    is    it    blocked    right    now";
    expect(padded.length).toBeGreaterThan(30);
    expect(deriveThreadTitle(padded)).toBe("why is it blocked right now");
  });
});

describe("contract: threadAwaitingReply", () => {
  // "own blocks empty" is the just-branched state: the fork's transcript is
  // all inherited history and the thread has said nothing of its own yet.
  // Whether an answer is coming is then decided by the SEED — a thread
  // branched with a question is waiting; one branched idle is not.
  describe("with no blocks of its own", () => {
    it("is awaiting when the seed asks something", () => {
      expect(threadAwaitingReply([], "why is it blocked")).toBe(true);
      expect(
        threadAwaitingReply(
          [contractBlock("user", true), contractBlock("text", true)],
          "why is it blocked",
        ),
      ).toBe(true);
    });

    it("is not awaiting when the seed is empty or whitespace", () => {
      expect(threadAwaitingReply([], "")).toBe(false);
      expect(threadAwaitingReply([], "   \n\t ")).toBe(false);
      expect(
        threadAwaitingReply(
          [contractBlock("user", true), contractBlock("text", true)],
          "",
        ),
      ).toBe(false);
    });
  });

  describe("with blocks of its own", () => {
    // a trailing `text` block is the thread having ANSWERED — nothing is
    // pending, so the poll can go back to its slow cadence
    it("is not awaiting when the last block is text", () => {
      expect(
        threadAwaitingReply(
          [contractBlock("user"), contractBlock("text")],
          "why is it blocked",
        ),
      ).toBe(false);
    });

    // a trailing user/tool/thinking block means the turn is still in flight
    it("is awaiting when the last block is not text", () => {
      for (const kind of ["user", "tool", "think", "asst"]) {
        expect(
          threadAwaitingReply(
            [contractBlock("text"), contractBlock(kind)],
            "why is it blocked",
          ),
        ).toBe(true);
      }
    });

    // once the thread has spoken for itself the seed stops mattering — the
    // blocks are the newer evidence
    it("ignores the seed once own blocks exist", () => {
      expect(threadAwaitingReply([contractBlock("user")], "")).toBe(true);
      expect(threadAwaitingReply([contractBlock("text")], "still asking")).toBe(
        false,
      );
    });
  });

  describe("the inherited split", () => {
    // split = one past the LAST inherited block, exactly threadSplitIndex's
    // rule: everything after that index is the thread's own conversation.
    it("counts only the blocks after the last inherited one", () => {
      const blocks = [
        contractBlock("user", true),
        contractBlock("text", true),
        contractBlock("user"),
      ];
      // own = [user] — a trailing non-text block, so an answer is coming
      expect(threadAwaitingReply(blocks, "")).toBe(true);
    });

    it("treats a thread whose every block is inherited as having none of its own", () => {
      const blocks = [
        contractBlock("user", true),
        contractBlock("text", true),
        contractBlock("tool", true),
      ];
      // the last block is a tool block, but it is INHERITED — the thread has
      // said nothing, so the seed decides
      expect(threadAwaitingReply(blocks, "")).toBe(false);
      expect(threadAwaitingReply(blocks, "why is it blocked")).toBe(true);
    });

    // "one past the LAST inherited block", not "the first block without the
    // flag": a non-inherited block sitting BEFORE an inherited one is on the
    // inherited side of the split, and the two rules disagree here.
    it("splits at the last inherited block even when an unflagged one precedes it", () => {
      const blocks = [contractBlock("text"), contractBlock("user", true)];
      expect(threadAwaitingReply(blocks, "")).toBe(false);
      expect(threadAwaitingReply(blocks, "why is it blocked")).toBe(true);
    });

    it("treats a thread with no inherited blocks at all as all its own", () => {
      expect(
        threadAwaitingReply(
          [contractBlock("text"), contractBlock("user")],
          "",
        ),
      ).toBe(true);
      expect(
        threadAwaitingReply(
          [contractBlock("user"), contractBlock("text")],
          "",
        ),
      ).toBe(false);
    });
  });
});
