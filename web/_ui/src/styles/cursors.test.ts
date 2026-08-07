import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// cursors.test.ts — pins the cursor family's art/role assignment: the REST
// cursor (body) is the full dot crosshair, and every ACT-ABLE surface
// (pointer/text/grab/grabbing fallbacks) wears the open reticle — pulled-back
// arms, a 3.5-radius ring. The reversal round swapped the roles, carl drove
// it and switched them back, so this assignment is a decision made twice —
// and a rule quietly flipping one role would read as "nothing changed" with
// nothing else failing. Same source-text stance as motion.test.ts beside it:
// the invariant lives in the stylesheet, so the test reads the sheet — and
// same twin arrangement: web/cursorpolicy_test.go is the Go copy that puts
// this rule behind `go test ./...` (npm test is not part of the repo's
// gate); this copy exists so a web-side edit fails in the web-side suite
// too, with the better error.

// path relative to the vitest root (web/_ui), not import.meta.url: under
// the jsdom environment that is not a file: URL and readFileSync rejects it
const raw = readFileSync("src/styles/style.css", "utf8");
const css = raw.replace(/\/\*[\s\S]*?\*\//g, "");

/** The two arts, by the substrings that can only be one of them: the ring's
 * circle is stroked at r=3.5, the dot's filled at r=1.3. */
const RING = 'r="3.5" fill="none"';
const DOT = 'r="1.3" fill=';

/** Every data-URI cursor declaration, as [svg payload, fallback keyword]. */
const decls = [...css.matchAll(/cursor:\s*url\('([^']+)'\)\s*12 12,\s*([a-z-]+)/g)].map(
  (m) => [m[1] ?? "", m[2] ?? ""] as const,
);

describe("cursor family", () => {
  it("the extraction regex sees every cursor declaration in the sheet", () => {
    // Not just "matches something": the extraction hard-codes single quotes
    // and the `12 12` hotspot, so a future rule written differently would
    // silently drop out of `decls` and every role assertion below would
    // still pass. Tying the count to the sheet's own `cursor: url(` count
    // makes an unmatched rule a failure here instead of a hole in the pin.
    expect(decls.length).toBeGreaterThan(0);
    expect(decls.length).toBe((css.match(/cursor:\s*url\(/g) ?? []).length);
  });

  it("rest (the crosshair fallback) wears the dot crosshair", () => {
    const rest = decls.filter(([, kw]) => kw === "crosshair");
    expect(rest.length).toBe(2); // the dark and light body rules, exactly
    for (const [svg] of rest) {
      expect(svg, `rest cursor lost the dot art: ${svg}`).toContain(DOT);
    }
  });

  it("every act-able surface wears the ring reticle", () => {
    const actable = decls.filter(([, kw]) => kw !== "crosshair");
    expect(actable.length).toBeGreaterThan(0);
    for (const [svg, kw] of actable) {
      expect(svg, `act-able (${kw}) cursor lost the ring art: ${svg}`).toContain(RING);
    }
  });

  it("the fallback keywords still keep the roles apart when the URI is refused", () => {
    const kws = new Set(decls.map(([, kw]) => kw));
    for (const kw of ["crosshair", "pointer", "text", "grab", "grabbing"]) {
      expect(kws, `missing fallback keyword: ${kw}`).toContain(kw);
    }
  });
});
