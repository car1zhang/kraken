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
    // [a-z0-9-] rather than [a-z-]: a name carrying a digit would otherwise
    // silently drop out of BOTH sides of the pin and the test would pass over
    // a real divergence — the one failure mode this file exists to prevent.
    for (const m of src.matchAll(/var\((--iso-[a-z0-9-]+)/g)) {
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
    for (const v of body.matchAll(/(--iso-[a-z0-9-]+)\s*:/g)) {
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

  it("the iso viewer page's light palette is byte-identical to the board's", () => {
    // Two light blocks exist on purpose — style.css keys on the board's
    // data-theme stamp, the viewer page (src/iso/iso.css) on
    // prefers-color-scheme, and neither document can read the other's
    // selector — so the VALUES are pinned equal here instead.
    const isoCss = readFileSync("src/iso/iso.css", "utf8");
    const decls = (body: string): Map<string, string> => {
      const out = new Map<string, string>();
      for (const m of body.matchAll(/(--iso-[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
        if (m[1] && m[2]) out.set(m[1], m[2].trim());
      }
      return out;
    };
    // matchAll + merge over EVERY board light block, like lightBlockIsoVars
    // above — a single non-global .exec keys on which block happens to come
    // first, and a palette var added to an earlier unrelated light block
    // would silently repoint the comparison.
    const board = new Map<string, string>();
    for (const m of css.matchAll(/:root\[data-theme="light"\]\s*\{([^}]*)\}/g)) {
      for (const [k, v] of decls(m[1] ?? "")) board.set(k, v);
    }
    // `{\s*:root` anchors the :root as the media block's FIRST rule, so a
    // future non-palette light rule in iso.css cannot make the regex walk out
    // of its own @media and pin some other :root — keep the palette first in
    // that block (the match failing loudly via page.size is the reminder).
    const pageBlock =
      /@media \(prefers-color-scheme: light\)\s*\{\s*:root\s*\{([^}]*)\}/.exec(isoCss)?.[1] ?? "";
    const page = decls(pageBlock);
    expect(page.size).toBeGreaterThan(0);
    expect(Object.fromEntries([...page].sort())).toEqual(Object.fromEntries([...board].sort()));
  });

  it("the two dark ground fallbacks agree", () => {
    // iso.css's body background and style.css's .panel-body.iso background
    // are the one dark GROUND value that lives in CSS rather than in the
    // lib's var() fallbacks — hold them equal so neither can drift alone.
    const isoCss = readFileSync("src/iso/iso.css", "utf8");
    const grab = (s: string): string[] =>
      [...s.matchAll(/background:\s*var\(--iso-bg,\s*(#[0-9a-fA-F]+)\)/g)].map((m) => m[1] ?? "");
    const page = grab(isoCss);
    const board = grab(css);
    expect(page.length).toBe(1);
    expect(board.length).toBeGreaterThan(0);
    expect(new Set([...page, ...board]).size).toBe(1);
  });
});
