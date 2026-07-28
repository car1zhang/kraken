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

describe("renderMD paragraph folding", () => {
  // A single newline is a SOFT break: agents hard-wrap their prose, and one
  // <p> per source line rendered that as a stack of one-line paragraphs with a
  // margin between each. A blank line is what ends a paragraph.
  it("folds hard-wrapped lines into one paragraph", () => {
    expect(renderMD("one line\nand its continuation")).toBe(
      "<p>one line\nand its continuation</p>",
    );
  });

  it("takes a BLANK line, and only a blank line, as the paragraph break", () => {
    expect(renderMD("first\n\nsecond")).toBe("<p>first</p>\n<p>second</p>");
    // whitespace-only counts as blank, as it did before
    expect(renderMD("first\n   \nsecond")).toBe("<p>first</p>\n<p>second</p>");
  });

  it("stops the fold at every block that follows without a blank line", () => {
    expect(renderMD("prose\n# head")).toBe("<p>prose</p>\n<h1>head</h1>");
    expect(renderMD("prose\n- item")).toBe(
      "<p>prose</p>\n<ul>\n<li>item</li>\n</ul>",
    );
    expect(renderMD("prose\n1. item")).toBe(
      "<p>prose</p>\n<ol>\n<li>item</li>\n</ol>",
    );
    expect(renderMD("prose\n> quoted")).toBe(
      "<p>prose</p>\n<blockquote>quoted</blockquote>",
    );
    expect(renderMD("prose\n```\nx\n```")).toBe(
      "<p>prose</p>\n<pre><code>x</code></pre>",
    );
  });

  it("stops the fold before a table header, which needs the lookahead", () => {
    // "| a | b |" is only a table because the NEXT line is a separator, so the
    // fold has to look one line further than the line it is deciding about
    const html = renderMD(
      ["prose", "| a | b |", "| - | - |", "| 1 | 2 |"].join("\n"),
    );
    expect(html.startsWith("<p>prose</p>\n")).toBe(true);
    expect(html).toContain('<div class="md-table">');
    expect(html).not.toContain("<p>prose\n| a | b |</p>");
  });

  it("carries emphasis across a soft break", () => {
    // the lines are joined RAW and inlined once, so a bold span survives a wrap
    expect(renderMD("**two\nwords**")).toBe(
      "<p><strong>two\nwords</strong></p>",
    );
  });

  it("still escapes a payload split across a soft break", () => {
    // the tag itself straddles the join, so this can only pass if esc() runs
    // AFTER the fold concatenates
    const html = renderMD("safe\n<scr\nipt>alert(1)</scr\nipt>");
    expect(html).not.toContain("<script>");
    expect(html).toContain("&lt;scr\nipt&gt;");
  });

  it("emits no empty paragraph and never stalls on degenerate input", () => {
    expect(renderMD("")).toBe("");
    expect(renderMD("\n")).toBe("");
    expect(renderMD("\n  \n\t\n")).toBe("");
  });

  it("does not leak a trailing newline into the paragraph", () => {
    expect(renderMD("prose\n")).toBe("<p>prose</p>");
    expect(renderMD("prose\n\n")).toBe("<p>prose</p>");
  });

  it("stops at an asterisk bullet, the marker it shares with emphasis", () => {
    expect(renderMD("prose\n* item")).toBe(
      "<p>prose</p>\n<ul>\n<li>item</li>\n</ul>",
    );
    expect(renderMD("prose\n1) item")).toBe(
      "<p>prose</p>\n<ol>\n<li>item</li>\n</ol>",
    );
  });

  it("keeps a pipe line as prose when no line follows it", () => {
    // the table branch reads lines[i+1]; at the last line that is undefined,
    // and the fold has to survive it rather than start a table
    expect(renderMD("prose\n| a | b |")).toBe("<p>prose\n| a | b |</p>");
  });

  it("terminates on an unclosed fence after prose", () => {
    expect(renderMD("prose\n```\nx")).toBe(
      "<p>prose</p>\n<pre><code>x</code></pre>",
    );
  });

  it("still breaks the paragraph on a CRLF blank line", () => {
    expect(renderMD("first\r\n\r\nsecond")).toContain("<p>second</p>");
  });
});

describe("inlineMD pairings the fold would otherwise create", () => {
  // renderMD hands inlineMD a whole PARAGRAPH now, so a lone `*` has the rest
  // of the paragraph to find a partner in. Both rules below refuse pairings;
  // neither creates one.
  it("leaves a glob literal when its partner is on another line", () => {
    expect(
      renderMD("It scans *.ts files under src\nand *.tsx files too"),
    ).toBe("<p>It scans *.ts files under src\nand *.tsx files too</p>");
  });

  it("never opens emphasis inside one code span and closes it in another", () => {
    expect(renderMD("a `*Job` here\nand a `*Job` there")).toBe(
      "<p>a <code>*Job</code> here\nand a <code>*Job</code> there</p>",
    );
  });

  it("leaves an asterisk inside a code span alone", () => {
    expect(inlineMD("logs at `~/.kraken/logs/*.jsonl` here")).toBe(
      "logs at <code>~/.kraken/logs/*.jsonl</code> here",
    );
  });

  it("still emphasises when both inner edges are non-space", () => {
    expect(inlineMD("*emphasis* start")).toBe("<em>emphasis</em> start");
    expect(inlineMD("**a `b` c**")).toBe(
      "<strong>a <code>b</code> c</strong>",
    );
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
    // and the two lines are ONE paragraph — see the paragraph-folding block
    expect(html).toBe("<p>the a|b split\nand more prose</p>");
  });

  it("does NOT treat a following dashed rule as a separator unless it lines up", () => {
    const html = renderMD("a | b\nnot a separator");
    expect(html).not.toContain("md-table");
    expect(html).toBe("<p>a | b\nnot a separator</p>");
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
