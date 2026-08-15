// Lead-authored (sighted). The --iso-* palette's cross-source pin, in the
// shape palette.test.ts and motion.test.ts established: the lib's emitted
// var(--iso-…) names and style.css's light-theme override block must cover
// each other, or a light-mode board silently renders one material in its dark
// value. Without this test the discipline is a comment asking future editors
// to grep — the exact convention-by-memory the job palette's pinned tests
// exist to replace.
import { readFileSync, readdirSync } from "node:fs";
import { describe, expect, it } from "vitest";

// paths relative to the vitest root (web/_ui), not import.meta.url: under
// the jsdom environment that is not a file: URL and readFileSync rejects it
// — the same resolution palette.test.ts and motion.test.ts settled on.
const LIB = "src/lib/isoscene";
const css = readFileSync("src/styles/style.css", "utf8");

function emittedIsoVars(): Set<string> {
  const out = new Set<string>();
  // Every non-test source in the lib, so a future emitter file cannot drop
  // out of the pin silently — the exact hole a hardcoded two-file list had.
  for (const f of readdirSync(LIB)) {
    if (!f.endsWith(".ts") || f.includes(".test.") || f.endsWith(".d.ts")) continue;
    const src = readFileSync(`${LIB}/${f}`, "utf8");
    for (const m of src.matchAll(/var\((--iso-[a-z-]+)/g)) {
      const name = m[1];
      if (name) out.add(name);
    }
  }
  return out;
}

function lightBlockIsoVars(): Set<string> {
  const block = /:root\[data-theme="light"\]\s*\{([^}]*)\}/g;
  const out = new Set<string>();
  for (const m of css.matchAll(block)) {
    const body = m[1] ?? "";
    for (const v of body.matchAll(/(--iso-[a-z-]+)\s*:/g)) {
      const name = v[1];
      if (name) out.add(name);
    }
  }
  return out;
}

describe("iso theme palette pin", () => {
  it("every var the lib emits has a light-theme override", () => {
    const emitted = emittedIsoVars();
    const themed = lightBlockIsoVars();
    expect(emitted.size).toBeGreaterThan(0);
    const missing = [...emitted].filter((v) => !themed.has(v));
    expect(missing).toEqual([]);
  });

  it("every themed var is one the lib emits (or --iso-bg, which only CSS reads)", () => {
    const emitted = emittedIsoVars();
    const themed = lightBlockIsoVars();
    const stray = [...themed].filter((v) => v !== "--iso-bg" && !emitted.has(v));
    expect(stray).toEqual([]);
  });
});
