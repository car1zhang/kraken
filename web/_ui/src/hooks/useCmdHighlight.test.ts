import { describe, expect, it } from "vitest";
import {
  CMD_ACTIVE_RE,
  CMD_CHIPS,
  cmdHighlightHTML,
  insertAt,
} from "./useCmdHighlight.ts";

describe("cmdHighlightHTML", () => {
  it("always appends the trailing newline sentinel", () => {
    // a <textarea> reserves a blank row below its last line that a pre-wrap div
    // will not; without this the mirror runs out of vertical room first and the
    // scrollTop sync clamps short
    expect(cmdHighlightHTML("")).toBe("\n");
    expect(cmdHighlightHTML("x")).toBe("x\n");
    expect(cmdHighlightHTML("x\n")).toBe("x\n\n");
  });

  it("wraps each command in its generic AND its per-skill class", () => {
    for (const cmd of CMD_CHIPS) {
      const html = cmdHighlightHTML(cmd + " go");
      expect(html).toContain(
        `<span class="cmd-hl cmd-hl-${cmd.slice(1)}">${cmd}</span>`,
      );
    }
  });

  it("highlights at start-of-text and after whitespace", () => {
    expect(cmdHighlightHTML("/do it")).toContain('cmd-hl-do">/do</span>');
    expect(cmdHighlightHTML("please /pr now")).toContain('cmd-hl-pr">/pr</span>');
    expect(cmdHighlightHTML("line\n/aim")).toContain('cmd-hl-aim">/aim</span>');
  });

  it("does NOT highlight a mid-word slash run", () => {
    // the (^|\s) anchor exists for exactly this: \b alone only guards the END
    for (const s of ["web/static/do", "a/pr", "x/info"]) {
      expect(cmdHighlightHTML(s)).not.toContain("cmd-hl-");
    }
  });

  it("does not highlight a longer word that merely starts with a command", () => {
    expect(cmdHighlightHTML("/document this")).not.toContain("cmd-hl-do");
    expect(cmdHighlightHTML("/prs")).not.toContain("cmd-hl-pr");
  });

  it("escapes before matching, so HTML in the composer stays text", () => {
    const html = cmdHighlightHTML("<b>/do</b>");
    expect(html).not.toContain("<b>");
    expect(html).toContain("&lt;b&gt;");
    // Escape-then-match is safe because none of the four commands contain an
    // HTML-special character. It does mean the ESCAPED text is what the anchor
    // sees: `/do` here follows the ";" of "&gt;", not whitespace, so it does
    // not light up. Carried over verbatim — a command glued to a tag is not a
    // command, and the alternative (match first) would let a payload smuggle a
    // span through.
    expect(html).not.toContain("cmd-hl-do");
  });

  it("still highlights a command that follows real whitespace after escaped HTML", () => {
    const html = cmdHighlightHTML("<b>x</b> /do");
    expect(html).toContain('cmd-hl-do">/do</span>');
  });

  it("is global — every occurrence is wrapped", () => {
    const html = cmdHighlightHTML("/do a\n/do b");
    expect(html.match(/cmd-hl-do/g)?.length).toBe(2);
  });
});

describe("CMD_ACTIVE_RE", () => {
  it("matches only its own command, not another's substring", () => {
    expect(CMD_ACTIVE_RE["/do"]?.test("/do it")).toBe(true);
    expect(CMD_ACTIVE_RE["/do"]?.test("/document it")).toBe(false);
    expect(CMD_ACTIVE_RE["/pr"]?.test("/do it")).toBe(false);
    expect(CMD_ACTIVE_RE["/info"]?.test("run /info first")).toBe(true);
    expect(CMD_ACTIVE_RE["/info"]?.test("web/info")).toBe(false);
  });

  it("has an entry for every chip and nothing else", () => {
    expect(Object.keys(CMD_ACTIVE_RE).sort()).toEqual([...CMD_CHIPS].sort());
  });
});

describe("insertAt", () => {
  it("inserts at the caret and reports where the caret lands", () => {
    expect(insertAt("ab", 1, 1, "X")).toEqual({ value: "a\nXb", pos: 3 });
  });

  it("replaces an active selection", () => {
    // the gap rule reads the character BEFORE the selection, not the selection
    // itself: "a" is not whitespace, so inlineGap closes it with one space
    expect(insertAt("abcd", 1, 3, "X", { inlineGap: true })).toEqual({
      value: "a Xd",
      pos: 3,
    });
    // and with no preceding character there is no gap at all
    expect(insertAt("abcd", 0, 3, "X", { inlineGap: true })).toEqual({
      value: "Xd",
      pos: 1,
    });
  });

  it("without inlineGap, mid-line text gets its own leading newline", () => {
    // the image-reference case: a path must not glue onto existing prose
    expect(insertAt("prose", 5, 5, "[image: p]").value).toBe("prose\n[image: p]");
  });

  it("adds no gap when the preceding character is already a newline", () => {
    expect(insertAt("prose\n", 6, 6, "[image: p]").value).toBe(
      "prose\n[image: p]",
    );
  });

  it("adds no gap at the very start of an empty composer", () => {
    expect(insertAt("", 0, 0, "/do ", { inlineGap: true }).value).toBe("/do ");
  });

  it("with inlineGap, a single space closes the gap instead of a newline", () => {
    expect(insertAt("hello", 5, 5, "/do ", { inlineGap: true }).value).toBe(
      "hello /do ",
    );
  });

  it("with inlineGap, existing whitespace is left alone", () => {
    expect(insertAt("hello ", 6, 6, "/do ", { inlineGap: true }).value).toBe(
      "hello /do ",
    );
  });
});
