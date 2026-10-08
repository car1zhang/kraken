// graphremoval.contract.test.tsx — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice C (web SPA) of PR 1, the knowledge-graph
// removal. Written from the frozen contract alone: MemoryView.tsx,
// LibraryPane.tsx, MentalModelPane.tsx, useHashRoute.ts, App.tsx and the
// stylesheets were never opened by the author. The two panes are mocked so
// what is under test is MemoryView's OWN obligation (the two-segment switch),
// not the panes' data needs; the source/stylesheet/package scans below read
// files at TEST runtime, which is the obligation's own subject.
//
// Dimension: LOGIC — the happy paths and stated invariants. Boundaries are
// athena-edge's.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

import { act, render, renderHook, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

// Stubs forward the visibility-controlling props the host hands a pane (the
// real panes are rendered "unchanged", so whatever MemoryView passes to hide
// one must reach the DOM here too).
vi.mock("./LibraryPane.tsx", () => {
  const Pane = (p: { hidden?: boolean; active?: boolean }) => (
    <div
      data-testid="contract-library-pane"
      hidden={Boolean(p.hidden) || p.active === false}
    />
  );
  return { LibraryPane: Pane, default: Pane };
});
vi.mock("./MentalModelPane.tsx", () => {
  const Pane = (p: { hidden?: boolean; active?: boolean }) => (
    <div
      data-testid="contract-mm-pane"
      hidden={Boolean(p.hidden) || p.active === false}
    />
  );
  return { MentalModelPane: Pane, default: Pane };
});

import MemoryView from "./MemoryView.tsx";
import { useHashRoute } from "../../hooks/useHashRoute.ts";

// The five packages the contract removes.
const contractRemovedDeps = [
  "sigma",
  "graphology",
  "graphology-types",
  "d3-force",
  "@types/d3-force",
];

// "shown" = in the DOM and not hidden from the viewer.
function contractShown(testId: string): boolean {
  const el = screen.queryByTestId(testId);
  if (!el) return false;
  for (let n: HTMLElement | null = el; n; n = n.parentElement) {
    if (n.hidden || n.style.display === "none") return false;
  }
  return true;
}

function contractRenderMemory() {
  // `library` and `mm` are opaque to MemoryView's own obligation (the panes
  // that consume them are mocked above); their exact types are a contract
  // silence, hence the cast.
  const props = { active: true, library: {}, mm: {} } as unknown as Parameters<
    typeof MemoryView
  >[0];
  return render(<MemoryView {...props} />);
}

function contractWalk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) contractWalk(p, out);
    else out.push(p);
  }
  return out;
}

describe("graphremoval contract", () => {
  beforeEach(() => {
    localStorage.clear();
    location.hash = "";
  });

  // "Renders a two-segment switch, `Library` and `Model`, using ...
  // `.memory-modes`/`.memory-mode`"
  it("MemoryView renders a two-segment Library · Model switch with the memory-mode classes", () => {
    const { container } = contractRenderMemory();
    const modes = container.querySelector(".memory-modes");
    expect(modes).not.toBeNull();
    const segs = Array.from(modes!.querySelectorAll(".memory-mode"));
    expect(segs.map((s) => s.textContent?.trim())).toEqual(["Library", "Model"]);
    expect(container.querySelector(".graph-modes")).toBeNull();
    expect(container.querySelector(".graph-mode")).toBeNull();
  });

  // "Default segment: `Library`." / "the Library segment renders the former
  // `LibraryPane`"
  it("MemoryView opens on the Library segment", () => {
    contractRenderMemory();
    expect(contractShown("contract-library-pane")).toBe(true);
    expect(contractShown("contract-mm-pane")).toBe(false);
  });

  // "the Model segment renders `MentalModelPane` unchanged"
  it("MemoryView's Model segment renders MentalModelPane, and Library switches back", async () => {
    const user = userEvent.setup();
    const { container } = contractRenderMemory();
    const segs = Array.from(
      container.querySelectorAll<HTMLElement>(".memory-modes .memory-mode"),
    );
    const model = segs.find((s) => s.textContent?.trim() === "Model")!;
    const library = segs.find((s) => s.textContent?.trim() === "Library")!;
    await user.click(model);
    expect(contractShown("contract-mm-pane")).toBe(true);
    expect(contractShown("contract-library-pane")).toBe(false);
    await user.click(library);
    expect(contractShown("contract-library-pane")).toBe(true);
    expect(contractShown("contract-mm-pane")).toBe(false);
  });

  // "`useHashRoute` unchanged — `#/graph` still resolves to the `memory` view
  // (the `RENAMED` alias stays)."
  it("#/graph still resolves to the memory view", () => {
    location.hash = "#/graph";
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("memory");
  });

  it("a hashchange to #/graph lands on memory too", () => {
    location.hash = "#/board";
    const { result } = renderHook(() => useHashRoute());
    act(() => {
      location.hash = "#/graph";
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(result.current[0]).toBe("memory");
  });

  // "renamed to `.memory-modes`/`.memory-mode` with the stylesheet updated in
  // the same change"
  it("the stylesheet defines .memory-mode(s) and no longer .graph-mode(s)", () => {
    const css = contractWalk("src")
      .filter((p) => p.endsWith(".css"))
      .map((p) => readFileSync(p, "utf8"))
      .join("\n");
    expect(css).toMatch(/\.memory-modes\b/);
    expect(css).toMatch(/\.memory-mode\b/);
    expect(css).not.toMatch(/\.graph-modes?\b/);
  });

  // "`sigma`, `graphology`, `graphology-types`, `d3-force`, `@types/d3-force`
  // removed from dependencies"
  it("package.json no longer depends on the five graph packages", () => {
    const pkg = JSON.parse(readFileSync("package.json", "utf8")) as Record<
      string,
      Record<string, string> | undefined
    >;
    const all = {
      ...(pkg.dependencies ?? {}),
      ...(pkg.devDependencies ?? {}),
      ...(pkg.peerDependencies ?? {}),
      ...(pkg.optionalDependencies ?? {}),
    };
    for (const dep of contractRemovedDeps) {
      expect(Object.keys(all), dep).not.toContain(dep);
    }
  });

  // "no file under `web/_ui/src` imports any of the five."
  it("no file under src imports any of the five graph packages", () => {
    const names = contractRemovedDeps
      .map((d) => d.replace(/[/@.-]/g, (c) => "\\" + c))
      .join("|");
    const importRe = new RegExp(
      `(?:from\\s*|import\\s*\\(?\\s*|require\\s*\\(\\s*)["'](?:${names})(?:/[^"']*)?["']`,
    );
    const offenders = contractWalk("src")
      .filter((p) => /\.(ts|tsx|js|jsx|mts|cts)$/.test(p))
      .filter((p) => importRe.test(readFileSync(p, "utf8")));
    expect(offenders).toEqual([]);
  });

  // "removed: components/graph/GraphView.tsx, graphModel.ts, physics.ts,
  // api/graph.ts, hooks/useGraph.ts"
  it("the graph host files are gone", () => {
    const gone = [
      "src/components/graph/GraphView.tsx",
      "src/components/graph/graphModel.ts",
      "src/components/graph/physics.ts",
      "src/api/graph.ts",
      "src/hooks/useGraph.ts",
    ];
    const present = new Set(contractWalk("src"));
    for (const f of gone) expect(present.has(f), f).toBe(false);
  });
});
