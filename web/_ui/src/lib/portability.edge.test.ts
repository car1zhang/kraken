import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

// portability.edge.test.ts — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) Slice S, web half: "no carl/CARL string literal
// remains under web/_ui/src outside test fixtures". Written blind.
//
// Paths are relative to the vitest root (web/_ui), the convention the other
// file-reading tests here use (under jsdom import.meta.url is not a file URL).

function edgeSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "test" || name === "node_modules") continue; // test fixtures are exempt
      out.push(...edgeSourceFiles(p));
      continue;
    }
    if (!/\.(ts|tsx)$/.test(name)) continue;
    if (/\.test\.(ts|tsx)$/.test(name)) continue;
    out.push(p);
  }
  return out;
}

// Comments may say anything; shipped code and text may not.
function edgeStripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`\\])\/\/.*$/gm, "$1");
}

describe("Portability edge: no operator name ships in the SPA", () => {
  const files = edgeSourceFiles("src");

  it("Portability: the scan actually sees the source tree", () => {
    expect(files.length).toBeGreaterThan(20);
    expect(files.some((f) => f.endsWith("ThreadOverlay.tsx"))).toBe(true);
    expect(files.some((f) => f.endsWith("SteerOverlay.tsx"))).toBe(true);
  });

  it("Portability: no non-test source names carl (word-bounded, any case) outside comments", () => {
    const hits: string[] = [];
    for (const f of files) {
      const code = edgeStripComments(readFileSync(f, "utf8"));
      code.split("\n").forEach((line, i) => {
        if (/\bcarl\b/i.test(line)) hits.push(`${f}:${i + 1}: ${line.trim()}`);
      });
    }
    expect(hits).toEqual([]);
  });

  it("Portability: the load tooltip no longer says it costs a named person", () => {
    const steer = files.find((f) => f.endsWith("SteerOverlay.tsx"));
    expect(steer).toBeDefined();
    const src = readFileSync(steer as string, "utf8");
    expect(/costs\s+carl/i.test(src)).toBe(false);
  });
});

