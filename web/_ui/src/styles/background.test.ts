import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// background.test.ts — pins the board-background rule's ONE structural
// invariant: the whole image treatment is gated on [data-bg], so a board
// with no background set paints pixel-identically to a board that never had
// the feature (the job's stated requirement). The failure mode is silent —
// hoisting the background-image declarations out of the gate, or adding a
// second reader of --bg-image outside it, renders fine WITH a background and
// only breaks the no-background default, which nobody testing the feature
// looks at. Source-text test on style.css, motion.test.ts's stance —
// including its two-sided half: web/backgroundpolicy_test.go is this file's
// Go twin, the copy that runs under `go test ./...`; this copy exists so a
// web-side edit fails in the web-side suite too, with the better error.

// path relative to the vitest root (web/_ui), not import.meta.url — see
// motion.test.ts for why
const raw = readFileSync("src/styles/style.css", "utf8");
const css = raw.replace(/\/\*[\s\S]*?\*\//g, "");

describe("the board background image rule", () => {
  it("keeps the ungated base rule: html paints the plain ground", () => {
    expect(css).toMatch(/html\s*\{\s*background:\s*var\(--ground\);\s*\}/);
  });

  it("reads --bg-image only inside a [data-bg]-gated block", () => {
    // every block that mentions --bg-image must carry the gate in its
    // selector — collect blocks as selector{body} pairs, shallow-brace, which
    // this sheet's flat rule structure allows
    const blocks = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
    const readers = blocks.filter(([, , body]) => (body ?? "").includes("--bg-image"));
    expect(readers.length).toBeGreaterThan(0);
    for (const [, selector] of readers) {
      expect(selector ?? "").toContain("[data-bg]");
    }
  });

  it("never sets --bg-image in the sheet — the inline stamp is the one writer", () => {
    // the sheet may READ var(--bg-image); a declaration `--bg-image:` in css
    // would race the inline stamp App.tsx owns
    expect(css).not.toMatch(/--bg-image\s*:/);
  });
});
