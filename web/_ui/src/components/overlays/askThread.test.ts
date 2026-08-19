import { describe, expect, it } from "vitest";
import { questionThreadSeed, questionThreadTitle } from "./askThread.ts";

// The artifact pair (askThreadTitle/askThreadSeed) keeps its coverage in
// ArtifactOverlay.test.tsx, which imports it from here — this file covers
// only the question pair the module adds.

/** No UTF-16 code unit left as an unpaired surrogate half. */
function wellFormed(s: string): boolean {
  return Array.from(s).every((c) => !/^[\uD800-\uDFFF]$/.test(c));
}

describe("questionThreadTitle", () => {
  it("titles from the first line of the ask when one was typed", () => {
    expect(
      questionThreadTitle("Q3", "should we cap it?", "what does cap mean here?\nmore detail"),
    ).toBe("Q3 — what does cap mean here?");
  });

  it("falls back to the question's own first line on an empty ask", () => {
    expect(questionThreadTitle("Q3", "should we cap it?\nsecond line", "   ")).toBe(
      "Q3 — should we cap it?",
    );
  });

  it("caps at 80 units with an ellipsis, never splitting a surrogate pair", () => {
    const title = questionThreadTitle("Q1", "x", "why ".repeat(40));
    expect(title.length).toBeLessThanOrEqual(80);
    expect(title.endsWith("…")).toBe(true);

    const emoji = questionThreadTitle("Q1", "x", "😀".repeat(60));
    expect(emoji.length).toBeLessThanOrEqual(80);
    expect(wellFormed(emoji)).toBe(true);
  });

  it("degrades to the bare qid when both texts are empty", () => {
    expect(questionThreadTitle("Q7", "", "")).toBe("Q7");
  });
});

describe("questionThreadSeed", () => {
  const q = {
    id: "Q3",
    text: "should the cap scale?",
    importance: "P1",
    context: "the flat 50 predates limits",
    recommendation: "scale with the caller's limit",
  };

  it("quotes the whole row verbatim — id, importance, text, context, recommendation", () => {
    const seed = questionThreadSeed(q, "what limit?");
    expect(seed).toContain("Q3 (P1): should the cap scale?");
    expect(seed).toContain("context: the flat 50 predates limits");
    expect(seed).toContain("recommendation: scale with the caller's limit");
    expect(seed).toContain("what limit?");
  });

  it("omits the context and recommendation lines when the row has neither", () => {
    const seed = questionThreadSeed(
      { id: "Q1", text: "which?", importance: "P0" },
      "hm",
    );
    expect(seed).toContain("Q1 (P0): which?");
    expect(seed).not.toContain("context:");
    expect(seed).not.toContain("recommendation:");
  });

  it("supplies the unpack-this ask when carl typed nothing", () => {
    const seed = questionThreadSeed(q, "  ");
    expect(seed).toContain("Help me understand this question");
  });

  it("defaults a blank importance to P2 and always restates the thread contract", () => {
    const seed = questionThreadSeed({ id: "Q9", text: "eh?", importance: "" }, "");
    expect(seed).toContain("Q9 (P2): eh?");
    expect(seed).toContain("Don't write into the questions record");
  });
});
