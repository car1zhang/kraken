/**
 * portability.contract.test.ts — athena-logic's BLIND logic suite for the
 * portability audit's web slice (artifacts/50-contract.md, Slice S row
 * `web (web/_ui/src)`). Written from the contract text alone: no
 * implementation file under src/ was opened by the author; the source files
 * named below are read BY THE TESTS at run time. The threadBlockHTML call
 * shape was learned from the existing ThreadOverlay.test.tsx.
 *
 * Every package-level identifier is prefixed `contract`.
 */
import { readdirSync as contractReaddir, readFileSync as contractRead, statSync as contractStat } from "node:fs";
import { join as contractJoin } from "node:path";
import { describe, expect, it, vi } from "vitest";

vi.mock("../api/client.ts", () => ({
  fetchThreadRead: vi.fn(() => Promise.resolve({ blocks: [] })),
  capturePane: vi.fn(() => Promise.resolve({ pane: "%1", content: "" })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true })),
  openTmux: vi.fn(() => Promise.resolve({ ok: true })),
}));

import { threadBlockHTML } from "../components/overlays/ThreadOverlay.tsx";

// path relative to the vitest root (web/_ui), the convention motion.test.ts documents
const contractSrcRoot = "src";

function contractSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of contractReaddir(dir)) {
    const p = contractJoin(dir, name);
    if (contractStat(p).isDirectory()) {
      if (name === "test" || name === "node_modules") continue;
      out.push(...contractSourceFiles(p));
    } else if (/\.(ts|tsx)$/.test(name) && !/\.test\.(ts|tsx)$/.test(name)) {
      out.push(p);
    }
  }
  return out;
}

/** Strips // line comments and block comments (an approximation: a `//`
 * inside a string literal truncates that line, which can only hide a hit). */
function contractStripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`])\/\/.*$/gm, "$1");
}

describe("Portability contract — web labels", () => {
  // Contract: "the board type gains optional operator?: string".
  it("Portability: WebBoard declares an optional operator string", () => {
    const types = contractRead(contractJoin(contractSrcRoot, "api", "types.ts"), "utf8");
    const start = types.search(/export (interface|type) WebBoard\b/);
    expect(start, "api/types.ts declares WebBoard").toBeGreaterThanOrEqual(0);
    const end = types.indexOf("\n}", start);
    const block = types.slice(start, end);
    expect(block).toMatch(/\boperator\?\s*:\s*string\b/);
  });

  // Contract: "ThreadOverlay's user turn label is board.operator, falling back
  // to you when absent".
  it("Portability: a user turn with no operator is labelled you, never carl", () => {
    const html = threadBlockHTML({ kind: "user", text: "hi" }, false, "t1");
    expect(html).toContain("❯ you");
    expect(html.toLowerCase()).not.toMatch(/\bcarl\b/);
  });

  // Contract: "no carl/CARL string literal remains under web/_ui/src outside
  // test fixtures".
  it("Portability: no shipped source under src/ names carl", () => {
    const offenders: string[] = [];
    for (const file of contractSourceFiles(contractSrcRoot)) {
      const code = contractStripComments(contractRead(file, "utf8"));
      const m = code.match(/\bcarl\b/i);
      if (m && m.index !== undefined) {
        offenders.push(`${file}: …${code.slice(Math.max(0, m.index - 40), m.index + 40)}…`);
      }
    }
    expect(offenders).toEqual([]);
  });

  // Contract: "SteerOverlay's load tooltip says costs the operator (or the
  // name)" — the shipped file no longer prices the load in carl's terms.
  it("Portability: SteerOverlay's tooltip names no person", () => {
    const steer = contractRead(contractJoin(contractSrcRoot, "components", "overlays", "SteerOverlay.tsx"), "utf8");
    expect(steer).not.toMatch(/costs carl/i);
    expect(steer).toMatch(/costs /);
  });
});
