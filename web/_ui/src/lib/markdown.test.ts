import { describe, expect, it } from "vitest";
import { esc, inlineMD, renderMD } from "./markdown.ts";

describe("esc", () => {
  it("escapes all five HTML-significant characters", () => {
    expect(esc(`<a href="x" title='y'>&</a>`)).toBe(
      "&lt;a href=&quot;x&quot; title=&#39;y&#39;&gt;&amp;&lt;/a&gt;",
    );
  });

  it("renders null/undefined as empty, never as the word", () => {
    expect(esc(null)).toBe("");
    expect(esc(undefined)).toBe("");
  });
});

describe("renderMD escapes BEFORE it transforms", () => {
  // This ordering is the entire safety argument for dangerouslySetInnerHTML in
  // the overlays: every string that reaches renderMD is agent-authored.
  it("neutralises a <script> payload in prose", () => {
    const html = renderMD(`<script>alert('pwn')</script>`);
    expect(html).not.toContain("<script>");
    expect(html).toContain("&lt;script&gt;");
  });

  it("neutralises a <script> payload inside every construct", () => {
    const src = [
      "# <script>h</script>",
      "- <script>li</script>",
      "> <script>bq</script>",
      "",
      "```",
      "<script>fence</script>",
      "```",
      "",
      "| <script>th</script> |",
      "| --- |",
      "| <script>td</script> |",
    ].join("\n");
    const html = renderMD(src);
    expect(html).not.toContain("<script>");
    // heading, li, blockquote, fence, th, td
    expect(html.match(/&lt;script&gt;/g)?.length).toBe(6);
  });

  it("leaves an img onerror payload inert", () => {
    const html = renderMD(`<img src=x onerror=alert(1)>`);
    expect(html).not.toMatch(/<img/);
    expect(html).toContain("&lt;img");
  });
});

describe("renderMD blocks", () => {
  it("renders headings h1..h4 and stops there", () => {
    expect(renderMD("# a")).toBe("<h1>a</h1>");
    expect(renderMD("#### d")).toBe("<h4>d</h4>");
    // five hashes is not a heading — it falls through to a paragraph
    expect(renderMD("##### e")).toBe("<p>##### e</p>");
  });

  it("groups consecutive list items and closes on a switch", () => {
    expect(renderMD("- a\n- b")).toBe("<ul>\n<li>a</li>\n<li>b</li>\n</ul>");
    expect(renderMD("- a\n1. b")).toBe(
      "<ul>\n<li>a</li>\n</ul>\n<ol>\n<li>b</li>\n</ol>",
    );
  });

  it("renders a fenced block as escaped <pre><code>", () => {
    expect(renderMD("```\nx < y\n```")).toBe("<pre><code>x &lt; y</code></pre>");
  });

  it("renders a blockquote", () => {
    expect(renderMD("> quoted")).toBe("<blockquote>quoted</blockquote>");
  });
});

describe("renderMD GFM tables", () => {
  it("honours per-column alignment from the separator row", () => {
    const html = renderMD(
      ["| l | c | r |", "| :-- | :-: | --: |", "| 1 | 2 | 3 |"].join("\n"),
    );
    expect(html).toContain('<div class="md-table">');
    expect(html).toContain('<th style="text-align:left">l</th>');
    expect(html).toContain('<th style="text-align:center">c</th>');
    expect(html).toContain('<th style="text-align:right">r</th>');
    expect(html).toContain('<td style="text-align:center">2</td>');
  });

  it("pads a short row out to the header's column count", () => {
    const html = renderMD(["| a | b |", "| - | - |", "| 1 |"].join("\n"));
    // the missing cell still renders, empty, so the grid does not collapse
    expect(html).toContain('<td style="text-align:left">1</td>');
    expect(html).toContain('<td style="text-align:left"></td>');
  });

  it("does NOT start a table from a stray pipe in prose", () => {
    // the separator row is what triggers a table, which is exactly why prose
    // like "the a|b split" is safe
    const html = renderMD("the a|b split\nand more prose");
    expect(html).not.toContain("md-table");
    expect(html).toBe("<p>the a|b split</p>\n<p>and more prose</p>");
  });

  it("does NOT treat a following dashed rule as a separator unless it lines up", () => {
    const html = renderMD("a | b\nnot a separator");
    expect(html).not.toContain("md-table");
  });
});

describe("inlineMD", () => {
  it("linkifies http and https only", () => {
    expect(inlineMD("[x](https://example.com)")).toBe(
      '<a href="https://example.com" target="_blank" rel="noreferrer">x</a>',
    );
    expect(inlineMD("[x](http://example.com)")).toBe(
      '<a href="http://example.com" target="_blank" rel="noreferrer">x</a>',
    );
  });

  it("refuses javascript:, data: and every other scheme", () => {
    for (const bad of [
      "[x](javascript:alert(1))",
      "[x](data:text/html,<script>1</script>)",
      "[x](file:///etc/passwd)",
      "[x](vbscript:foo)",
    ]) {
      const html = inlineMD(bad);
      expect(html).not.toContain("<a ");
    }
  });

  it("renders code, bold and em", () => {
    expect(inlineMD("`c` **b** *e*")).toBe(
      "<code>c</code> <strong>b</strong> <em>e</em>",
    );
  });

  it("escapes before wrapping, so a tag inside code stays text", () => {
    expect(inlineMD("`<b>`")).toBe("<code>&lt;b&gt;</code>");
  });
});
