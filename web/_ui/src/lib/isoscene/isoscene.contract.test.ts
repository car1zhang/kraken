import { describe, expect, it } from "vitest";

import { cardCompilable, compileCard } from "./index";
import { isoProject, projectScene } from "./project";
import { readCard } from "./reader";
import { renderSceneMarkup } from "./render";
import type {
  EntityNode,
  IsoPoint,
  ProjectedScene,
  SceneGraph,
} from "./types";

import contractBespoke from "./fixtures/bespoke.html?raw";
import contractClickFlow from "./fixtures/click-flow.html?raw";
import contractLinkflow from "./fixtures/linkflow-arch.html?raw";
import contractMini from "./fixtures/mini.html?raw";
import contractRecolor from "./fixtures/recolor-walkthrough.html?raw";

// isoscene.contract.test.ts — athena-logic's BLIND suite for 50-contract.md.
//
// Written from the frozen contract, types.ts, raw-html.d.ts and the fixture
// files alone. reader.ts, project.ts, entities.ts, render.ts and index.ts were
// never read in any form (no Read, no grep, no git show) — they did not exist
// on disk when this was authored.
//
// Dimension: LOGIC — the stated invariants and happy paths. Grammar
// ACCEPTANCE, obligations A1-A11 on parseable cards, the executed fixture-facts
// table (asserted at the exact stated values), P1-P5, and R1-R5 + R9-R12 on the
// rendered output of the parseable fixtures. R6-R8 (the SMIL/reduced-motion
// obligations) and the hostile-input rejection surface are athena-edge's.
//
// Every module-scope identifier declared here is prefixed `contract`, fixture
// imports included — the import SHAPE the contract freezes (a default import of
// a `?raw` specifier) is preserved; only the local binding is renamed.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

/** contractCard wraps svg body markup in the minimal envelope the grammar
 * requires: an `h1` outside the figure (so the figure lookup is not merely
 * "the whole document"), one `figure.diagram`, one `svg` with a viewBox. */
function contractCard(inner: string, viewBox = "0 0 400 300"): string {
  return [
    "<h1>synthetic</h1>",
    '<figure class="diagram">',
    `<svg viewBox="${viewBox}">${inner}</svg>`,
    "<figcaption>synthetic caption.</figcaption>",
    "</figure>",
  ].join("\n");
}

const contractSceneCache = new Map<string, SceneGraph>();

/** contractScene reads a card that the contract says is parseable and returns
 * its SceneGraph, memoized so the large fixtures are parsed once. A non-scene
 * verdict throws rather than degrading — every caller here is asserting facts
 * about a card the contract froze as `scene`. */
function contractScene(html: string): SceneGraph {
  const hit = contractSceneCache.get(html);
  if (hit) return hit;
  const result = readCard(html);
  if (result.kind !== "scene") {
    throw new Error(`expected kind "scene", got "${result.kind}"`);
  }
  contractSceneCache.set(html, result.scene);
  return result.scene;
}

/** contractAllNodes flattens every EntityNode the scene holds — top-level
 * entities, top-level structures, and every nested child, recursively. This is
 * the population P2 and R2 quantify over, and the one the fixture-facts table
 * counts. */
function contractAllNodes(scene: SceneGraph): EntityNode[] {
  const out: EntityNode[] = [];
  const walk = (nodes: EntityNode[]): void => {
    for (const node of nodes) {
      out.push(node);
      walk(node.children);
    }
  };
  walk(scene.entities);
  walk(scene.structures);
  return out;
}

/** contractOnly asserts a filtered list holds exactly one item and returns it. */
function contractOnly<T>(items: T[]): T {
  expect(items).toHaveLength(1);
  const only = items[0];
  if (only === undefined) throw new Error("expected exactly one item");
  return only;
}

const contractProjectedCache = new Map<string, ProjectedScene>();

function contractProjected(html: string): ProjectedScene {
  const hit = contractProjectedCache.get(html);
  if (hit) return hit;
  const projected = projectScene(contractScene(html));
  contractProjectedCache.set(html, projected);
  return projected;
}

function contractPlace(projected: ProjectedScene, id: string): IsoPoint {
  const placement = projected.placements.get(id);
  if (!placement) throw new Error(`no placement for node id ${id}`);
  return placement.base;
}

const contractRenderCache = new Map<string, string>();

function contractRendered(html: string, reducedMotion = false): string {
  const key = `${reducedMotion ? "rm" : "full"}::${html}`;
  const hit = contractRenderCache.get(key);
  if (hit !== undefined) return hit;
  const svg = renderSceneMarkup(contractProjected(html), { reducedMotion });
  contractRenderCache.set(key, svg);
  return svg;
}

/** contractDoc parses emitted markup with jsdom's HTML parser, which is how the
 * board itself mounts it (inline SVG in the board document, not an iframe). */
function contractDoc(svg: string): Document {
  return new DOMParser().parseFromString(
    `<!doctype html><html><body>${svg}</body></html>`,
    "text/html",
  );
}

function contractEls(doc: Document, selector: string): Element[] {
  return Array.from(doc.querySelectorAll(selector));
}

/** contractEntityGroup returns the single emitted group for one node id (R2). */
function contractEntityGroup(doc: Document, id: string): Element {
  const found = contractEls(doc, `[data-entity="${id}"]`);
  expect(found).toHaveLength(1);
  const only = found[0];
  if (!only) throw new Error(`no emitted group for entity ${id}`);
  return only;
}

/** contractViewBox parses the ROOT svg's viewBox (the first viewBox in the
 * string — the root tag precedes any marker's) into four numbers. */
function contractViewBox(svg: string): number[] {
  const match = /viewBox="([^"]*)"/i.exec(svg);
  expect(match).not.toBeNull();
  const raw = match?.[1] ?? "";
  return raw.trim().split(/[\s,]+/).map(Number);
}

interface ContractFixture {
  name: string;
  html: string;
}

const contractParseableFixtures: ContractFixture[] = [
  { name: "mini.html", html: contractMini },
  { name: "linkflow-arch.html", html: contractLinkflow },
  { name: "click-flow.html", html: contractClickFlow },
  { name: "recolor-walkthrough.html", html: contractRecolor },
];

/** Class tokens the grammar accounts for but that A7 says never become a
 * scene-graph EntityNode row. */
const contractNeverEntityShapes = [
  "edge",
  "edgeLabel",
  "arrowhead",
  "flow-dot",
  "axis",
  "title",
  "note",
  "spark",
  "dashed",
  "dim",
  "holds",
  "head",
];

// ---------------------------------------------------------------------------
// grammar acceptance — A1, A4, A9, A10, and the accepted transform forms
// ---------------------------------------------------------------------------

describe("readCard — grammar acceptance (A1, A4, A9, A10)", () => {
  it("A1: class token order never changes the reading", () => {
    const body = (cls: string) =>
      contractCard(
        `<g class="${cls}"><rect x="10" y="20" width="30" height="40"/>` +
          `<text x="25" y="40">label one</text></g>`,
      );
    const written = readCard(body("node blue"));
    const reversed = readCard(body("blue node"));

    expect(written).toEqual(reversed);
    expect(written.kind).toBe("scene");
    if (written.kind !== "scene") return;
    const node = contractOnly(written.scene.entities);
    expect(node.shape).toBe("node");
    expect(node.treatment).toBe("blue");
    expect(node.labelLines).toEqual(["label one"]);
  });

  it("A1: modifier and treatment tokens compose in any order", () => {
    const a = readCard(
      contractCard(
        '<g class="cell head dim green"><rect x="0" y="0" width="10" height="10"/></g>',
      ),
    );
    const b = readCard(
      contractCard(
        '<g class="green dim head cell"><rect x="0" y="0" width="10" height="10"/></g>',
      ),
    );
    expect(a).toEqual(b);
    if (a.kind !== "scene") throw new Error("expected a scene");
    const node = contractOnly(a.scene.entities);
    expect(node.shape).toBe("cell");
    expect(node.treatment).toBe("green");
    expect(node.dim).toBe(true);
    expect(node.head).toBe(true);
    expect(node.holds).toBe(false);
  });

  it("accepts all three translate() forms and resolves each", () => {
    const result = readCard(
      contractCard(
        [
          '<g class="node" transform="translate(5)">',
          '<rect x="0" y="0" width="10" height="10"/></g>',
          '<g class="node" transform="translate(5,6)">',
          '<rect x="0" y="0" width="10" height="10"/></g>',
          '<g class="node" transform="translate(5, 6)">',
          '<rect x="0" y="0" width="10" height="10"/></g>',
        ].join(""),
      ),
    );
    expect(result.kind).toBe("scene");
    if (result.kind !== "scene") return;
    expect(result.scene.entities.map((n) => [n.x, n.y])).toEqual([
      [5, 0],
      [5, 6],
      [5, 6],
    ]);
  });

  it("A4: nested translate() chains compose into absolute geometry", () => {
    const result = readCard(
      contractCard(
        [
          '<g class="group" transform="translate(10, 20)">',
          '<rect x="0" y="0" width="200" height="200"/>',
          '<g class="node" transform="translate(1,2)">',
          '<rect x="5" y="6" width="30" height="40"/></g>',
          "</g>",
        ].join(""),
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    const structure = contractOnly(result.scene.structures);
    expect([structure.x, structure.y, structure.width, structure.height]).toEqual(
      [10, 20, 200, 200],
    );
    const child = contractOnly(structure.children);
    expect([child.x, child.y, child.width, child.height]).toEqual([16, 28, 30, 40]);
  });

  it("A4: a rect-backed part reports its first rect exactly", () => {
    const result = readCard(
      contractCard(
        '<g class="proc"><rect x="12" y="34" width="56" height="78"/>' +
          '<rect x="16" y="38" width="48" height="70"/></g>',
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    const node = contractOnly(result.scene.entities);
    expect([node.x, node.y, node.width, node.height]).toEqual([12, 34, 56, 78]);
  });

  it("A4: circle and ellipse silhouettes report their bounding box", () => {
    const result = readCard(
      contractCard(
        '<g class="node"><circle cx="50" cy="60" r="10"/></g>' +
          '<g class="store"><ellipse cx="100" cy="200" rx="30" ry="5"/></g>',
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    const [circleNode, ellipseNode] = result.scene.entities;
    if (!circleNode || !ellipseNode) throw new Error("expected two entities");
    expect([
      circleNode.x,
      circleNode.y,
      circleNode.width,
      circleNode.height,
    ]).toEqual([40, 50, 20, 20]);
    expect([
      ellipseNode.x,
      ellipseNode.y,
      ellipseNode.width,
      ellipseNode.height,
    ]).toEqual([70, 195, 60, 10]);
  });

  it("A9: a card with no text.title reads title as \"\" and is still a scene", () => {
    const result = readCard(
      contractCard('<g class="node"><rect x="0" y="0" width="10" height="10"/></g>'),
    );
    expect(result.kind).toBe("scene");
    if (result.kind !== "scene") return;
    expect(result.scene.title).toBe("");
  });

  it("A10: viewBox width/height are the THIRD and FOURTH numbers", () => {
    const result = readCard(
      contractCard(
        '<g class="node"><rect x="0" y="0" width="10" height="10"/></g>',
        "-10 -20 640 480",
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    expect(result.scene.viewBox).toEqual({ width: 640, height: 480 });
  });

  it("A2: treatment/dim/holds/head and codeRef never inherit from an ancestor", () => {
    const result = readCard(
      contractCard(
        [
          '<g class="group blue" data-code="internal/jobs/cell.go:12">',
          '<rect x="0" y="0" width="300" height="300"/>',
          '<g class="node"><rect x="10" y="10" width="20" height="20"/></g>',
          "</g>",
        ].join(""),
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    const structure = contractOnly(result.scene.structures);
    expect(structure.treatment).toBe("blue");
    expect(structure.codeRef).toBe("internal/jobs/cell.go:12");
    const child = contractOnly(structure.children);
    expect(child.treatment).toBeUndefined();
    expect(child.codeRef).toBeUndefined();
    expect(child.dim).toBe(false);
    expect(child.holds).toBe(false);
    expect(child.head).toBe(false);
  });

  it("A3: a part's labelLines exclude text owned by nested classed children", () => {
    const result = readCard(
      contractCard(
        [
          '<g class="group"><rect x="0" y="0" width="300" height="300"/>',
          '<text x="4" y="14">outer label</text>',
          '<g class="node"><rect x="10" y="10" width="20" height="20"/>',
          '<text x="12" y="20">inner label</text></g>',
          "</g>",
        ].join(""),
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    const structure = contractOnly(result.scene.structures);
    expect(structure.labelLines).toEqual(["outer label"]);
    expect(contractOnly(structure.children).labelLines).toEqual(["inner label"]);
  });

  it("A3: tspans split into one trimmed line each, in document order", () => {
    const result = readCard(
      contractCard(
        [
          '<g class="proc"><rect x="0" y="0" width="80" height="40"/>',
          '<text x="40" y="20"><tspan x="40" dy="-0.6em">  first  </tspan>',
          '<tspan x="40" dy="1.2em">second</tspan></text></g>',
        ].join(""),
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    expect(contractOnly(result.scene.entities).labelLines).toEqual([
      "first",
      "second",
    ]);
  });

  it("A5: H, V and relative path commands resolve to a terminal point", () => {
    const result = readCard(
      contractCard(
        [
          '<path class="edge" d="M 10,20 H 60"/>',
          '<path class="edge" d="M 10,20 V 90"/>',
          '<path class="edge" d="M 10,20 l 5,7"/>',
        ].join(""),
      ),
    );
    if (result.kind !== "scene") throw new Error("expected a scene");
    expect(
      result.scene.conduits.map((c) => [c.x1, c.y1, c.x2, c.y2]),
    ).toEqual([
      [10, 20, 60, 20],
      [10, 20, 10, 90],
      [10, 20, 15, 27],
    ]);
  });

  it("A6: a card with zero flow-dots yields conduits with no flow, still a scene", () => {
    const result = readCard(
      contractCard('<path id="e1" class="edge" d="M 0,0 L 10,10"/>'),
    );
    expect(result.kind).toBe("scene");
    if (result.kind !== "scene") return;
    expect(contractOnly(result.scene.conduits).flow).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// mini.html — the byte-frozen executed facts (A2-A10)
// ---------------------------------------------------------------------------

describe("readCard — mini.html executed facts", () => {
  it("reads as a scene", () => {
    expect(readCard(contractMini).kind).toBe("scene");
  });

  it("A10: viewBox is 860 x 500", () => {
    expect(contractScene(contractMini).viewBox).toEqual({
      width: 860,
      height: 500,
    });
  });

  it("A9: title is the text.title content", () => {
    expect(contractScene(contractMini).title).toBe(
      "mini: two boxes, one legacy-animated edge",
    );
  });

  it("A7: entities are the non-structure component gs in document order", () => {
    const scene = contractScene(contractMini);
    expect(scene.entities.map((n) => n.shape)).toEqual(["proc", "node", "pill"]);
    expect(scene.structures.map((n) => n.shape)).toEqual(["group"]);
  });

  it("A2/A3/A4: the proc node is exact", () => {
    const scene = contractScene(contractMini);
    const proc = contractOnly(scene.entities.filter((n) => n.shape === "proc"));
    expect(proc.x).toBe(100);
    expect(proc.y).toBe(100);
    expect(proc.width).toBe(200);
    expect(proc.height).toBe(80);
    expect(proc.codeRef).toBe("cmd/kraken/cmd_serve.go:37");
    expect(proc.labelLines).toEqual(["kraken serve", "OS process"]);
    expect(proc.treatment).toBeUndefined();
    expect(proc.dim).toBe(false);
    expect(proc.holds).toBe(false);
    expect(proc.head).toBe(false);
    expect(proc.children).toEqual([]);
  });

  it("A7: the group is a structure owning the blue store as its child", () => {
    const scene = contractScene(contractMini);
    const group = contractOnly(scene.structures);
    expect(group.shape).toBe("group");
    expect(group.x).toBe(440);
    expect(group.y).toBe(240);
    expect(group.width).toBe(300);
    expect(group.height).toBe(200);
    expect(group.labelLines).toEqual(["data zone — file-ownership"]);

    const store = contractOnly(group.children);
    expect(store.shape).toBe("store");
    expect(store.treatment).toBe("blue");
    expect(store.labelLines).toEqual(["+ kraken.db", "sqlite db"]);
    expect(store.codeRef).toBeUndefined();
  });

  it("A2: the dim node and the grey pill carry their own modifiers only", () => {
    const scene = contractScene(contractMini);
    const node = contractOnly(scene.entities.filter((n) => n.shape === "node"));
    expect(node.dim).toBe(true);
    expect(node.treatment).toBeUndefined();
    expect([node.x, node.y, node.width, node.height]).toEqual([100, 380, 160, 60]);
    expect(node.labelLines).toEqual(["out of story"]);

    const pill = contractOnly(scene.entities.filter((n) => n.shape === "pill"));
    expect(pill.treatment).toBe("grey");
    expect(pill.dim).toBe(false);
    expect([pill.x, pill.y, pill.width, pill.height]).toEqual([330, 60, 120, 26]);
    expect(pill.labelLines).toEqual(["∅ retired flag"]);
  });

  it("A8: the step badge label is opaque and badges are empty", () => {
    const scene = contractScene(contractMini);
    expect(scene.steps).toHaveLength(1);
    expect(contractOnly(scene.steps).label).toBe("3b");
    expect(scene.badges).toEqual([]);
  });

  it("A5/A6: the two conduits are exact, with e1 carrying the flow binding", () => {
    const scene = contractScene(contractMini);
    expect(scene.conduits).toHaveLength(2);

    const e1 = contractOnly(scene.conduits.filter((c) => c.id === "mini-e1"));
    expect(e1.d).toBe("M 300,140 L 500,320");
    expect([e1.x1, e1.y1, e1.x2, e1.y2]).toEqual([300, 140, 500, 320]);
    expect(e1.codeRef).toBe("web/serve.go:116");
    expect(e1.dashed).toBe(false);
    expect(e1.dim).toBe(false);
    expect(e1.treatment).toBeUndefined();
    expect(e1.flow).toEqual({ dur: "3s", begin: "0s" });

    const dashed = contractOnly(scene.conduits.filter((c) => c.dashed));
    expect(dashed.d).toBe("M 180,180 V 380");
    expect([dashed.x1, dashed.y1, dashed.x2, dashed.y2]).toEqual([
      180, 180, 180, 380,
    ]);
    expect(dashed.flow).toBeUndefined();
    expect(dashed.id).toBeUndefined();
    expect(dashed.codeRef).toBeUndefined();
  });

  it("A7: title, edgeLabel and note are the three text marks", () => {
    const scene = contractScene(contractMini);
    expect(scene.texts).toHaveLength(3);

    const edgeLabel = contractOnly(
      scene.texts.filter((t) => t.kind === "edgeLabel"),
    );
    expect(edgeLabel.lines).toEqual(["writes rows"]);
    expect(edgeLabel.x).toBe(400);
    expect(edgeLabel.y).toBe(215);
    expect(edgeLabel.dim).toBe(false);

    const note = contractOnly(scene.texts.filter((t) => t.kind === "note"));
    expect(note.lines).toEqual([
      "one legacy flow-dot rides e1",
      "second note line",
    ]);

    const title = contractOnly(scene.texts.filter((t) => t.kind === "title"));
    expect(title.lines).toEqual(["mini: two boxes, one legacy-animated edge"]);
  });

  it("A6/A7: the flow-dot and other classed non-g elements are never entities", () => {
    const nodes = contractAllNodes(contractScene(contractMini));
    expect(nodes).toHaveLength(5);
    for (const shape of contractNeverEntityShapes) {
      expect(nodes.some((n) => n.shape === shape)).toBe(false);
    }
  });

  it("ids are unique nN handles, first entity in document order is n0", () => {
    const nodes = contractAllNodes(contractScene(contractMini));
    const ids = nodes.map((n) => n.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) expect(id).toMatch(/^n\d+$/);
    const proc = contractOnly(nodes.filter((n) => n.shape === "proc"));
    expect(proc.id).toBe("n0");
  });

  it("conduits and texts are collected in document order", () => {
    const scene = contractScene(contractMini);
    expect(scene.conduits[0]?.id).toBe("mini-e1");
    expect(scene.texts.map((t) => t.kind)).toEqual([
      "title",
      "edgeLabel",
      "note",
    ]);
  });
});

// ---------------------------------------------------------------------------
// the three trimmed real cards — executed facts, asserted at the stated values
// ---------------------------------------------------------------------------

describe("readCard — linkflow-arch.html executed facts", () => {
  it("reads as a scene", () => {
    expect(readCard(contractLinkflow).kind).toBe("scene");
  });

  it("holds 23 conduits", () => {
    expect(contractScene(contractLinkflow).conduits).toHaveLength(23);
  });

  it("holds 35 EntityNodes counting nested children recursively", () => {
    expect(contractAllNodes(contractScene(contractLinkflow))).toHaveLength(35);
  });

  it("carries 13 code refs across nodes and conduits", () => {
    const scene = contractScene(contractLinkflow);
    const nodeRefs = contractAllNodes(scene).filter((n) => n.codeRef).length;
    const conduitRefs = scene.conduits.filter((c) => c.codeRef).length;
    expect(nodeRefs + conduitRefs).toBe(13);
  });

  it("A6: carries no flow-dots, so no conduit has a flow binding", () => {
    const scene = contractScene(contractLinkflow);
    expect(scene.conduits.filter((c) => c.flow !== undefined)).toHaveLength(0);
  });
});

describe("readCard — click-flow.html executed facts", () => {
  it("reads as a scene", () => {
    expect(readCard(contractClickFlow).kind).toBe("scene");
  });

  it("holds 20 conduits, exactly 5 of them flow-bound", () => {
    const scene = contractScene(contractClickFlow);
    expect(scene.conduits).toHaveLength(20);
    expect(scene.conduits.filter((c) => c.flow !== undefined)).toHaveLength(5);
  });

  it("A8: holds 8 step marks", () => {
    expect(contractScene(contractClickFlow).steps).toHaveLength(8);
  });

  it("A7: structures are exactly 5 lanes", () => {
    const structures = contractScene(contractClickFlow).structures;
    expect(structures.filter((s) => s.shape === "lane")).toHaveLength(5);
    expect(structures).toHaveLength(5);
  });

  it("holds 17 cell EntityNodes at any depth", () => {
    const nodes = contractAllNodes(contractScene(contractClickFlow));
    expect(nodes.filter((n) => n.shape === "cell")).toHaveLength(17);
  });
});

describe("readCard — recolor-walkthrough.html executed facts", () => {
  it("reads as a scene", () => {
    expect(readCard(contractRecolor).kind).toBe("scene");
  });

  it("holds 11 conduits, exactly 4 of them flow-bound", () => {
    const scene = contractScene(contractRecolor);
    expect(scene.conduits).toHaveLength(11);
    expect(scene.conduits.filter((c) => c.flow !== undefined)).toHaveLength(4);
  });

  it("holds 5 decision EntityNodes at any depth", () => {
    const nodes = contractAllNodes(contractScene(contractRecolor));
    expect(nodes.filter((n) => n.shape === "decision")).toHaveLength(5);
  });

  it("carries zero code refs anywhere", () => {
    const scene = contractScene(contractRecolor);
    expect(contractAllNodes(scene).filter((n) => n.codeRef)).toHaveLength(0);
    expect(scene.conduits.filter((c) => c.codeRef)).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// A11 — the two non-scene verdicts the contract states by name
// ---------------------------------------------------------------------------

describe("readCard — stated non-scene verdicts (A11)", () => {
  it("bespoke.html is not-parseable and names the first offending element", () => {
    const result = readCard(contractBespoke);
    expect(result.kind).toBe("not-parseable");
    if (result.kind !== "not-parseable") return;
    expect(result.reason).toContain("starburst");
  });

  it("plain HTML with no figure.diagram is not-diagram", () => {
    expect(readCard("<h1>hi</h1>")).toEqual({ kind: "not-diagram" });
  });
});

// ---------------------------------------------------------------------------
// P1 — the projection formula
// ---------------------------------------------------------------------------

describe("isoProject (P1)", () => {
  it("is sx = x - y, sy = (x + y) / 2 - z", () => {
    expect(isoProject(0, 0, 0)).toEqual({ sx: 0, sy: 0 });
    expect(isoProject(100, 0, 0)).toEqual({ sx: 100, sy: 50 });
    expect(isoProject(0, 100, 0)).toEqual({ sx: -100, sy: 50 });
    expect(isoProject(10, 4, 6)).toEqual({ sx: 6, sy: 1 });
    expect(isoProject(7, 3, 2.5)).toEqual({ sx: 4, sy: 2.5 });
  });

  it("defaults z to 0 when omitted", () => {
    expect(isoProject(3, 5)).toEqual({ sx: -2, sy: 4 });
    expect(isoProject(3, 5)).toEqual(isoProject(3, 5, 0));
    expect(isoProject(860, 500)).toEqual(isoProject(860, 500, 0));
  });

  it("keeps collinear card points collinear on the floor", () => {
    const a = isoProject(0, 0);
    const b = isoProject(20, 10);
    const c = isoProject(40, 20);
    const cross =
      (b.sx - a.sx) * (c.sy - a.sy) - (b.sy - a.sy) * (c.sx - a.sx);
    expect(cross).toBeCloseTo(0, 9);

    const d = isoProject(10, 90);
    const e = isoProject(30, 70);
    const f = isoProject(50, 50);
    const cross2 =
      (e.sx - d.sx) * (f.sy - d.sy) - (e.sy - d.sy) * (f.sx - d.sx);
    expect(cross2).toBeCloseTo(0, 9);
  });

  it("preserves the relative order of points along both card axes", () => {
    // Equal y, increasing x: both screen coordinates increase.
    const left = isoProject(10, 40);
    const right = isoProject(200, 40);
    expect(right.sx).toBeGreaterThan(left.sx);
    expect(right.sy).toBeGreaterThan(left.sy);

    // Equal x, increasing y: sx decreases, sy increases.
    const near = isoProject(40, 10);
    const far = isoProject(40, 200);
    expect(far.sx).toBeLessThan(near.sx);
    expect(far.sy).toBeGreaterThan(near.sy);
  });

  it("is translation-equivariant, which is what makes P3 checkable", () => {
    const a = isoProject(120, 300);
    const b = isoProject(120 + 40, 300 + 15);
    const delta = isoProject(40, 15);
    expect(b.sx - a.sx).toBeCloseTo(delta.sx, 9);
    expect(b.sy - a.sy).toBeCloseTo(delta.sy, 9);
  });
});

// ---------------------------------------------------------------------------
// P2-P5 — projectScene over every parseable fixture
// ---------------------------------------------------------------------------

for (const fixture of contractParseableFixtures) {
  describe(`projectScene (P2-P5) — ${fixture.name}`, () => {
    it("P2: every EntityNode id, nested included, has a placement", () => {
      const projected = contractProjected(fixture.html);
      const nodes = contractAllNodes(projected.scene);
      for (const node of nodes) {
        expect(projected.placements.has(node.id)).toBe(true);
      }
      expect(projected.placements.size).toBe(nodes.length);
    });

    it("P4: width and height are positive and finite", () => {
      const projected = contractProjected(fixture.html);
      expect(Number.isFinite(projected.width)).toBe(true);
      expect(Number.isFinite(projected.height)).toBe(true);
      expect(projected.width).toBeGreaterThan(0);
      expect(projected.height).toBeGreaterThan(0);
    });

    it("P3: one global offset — no per-node adjustment", () => {
      const projected = contractProjected(fixture.html);
      const nodes = contractAllNodes(projected.scene);
      const reference = nodes[0];
      if (!reference) throw new Error("fixture has no EntityNodes");
      const referencePlace = contractPlace(projected, reference.id);
      const referenceIso = isoProject(reference.x, reference.y);

      for (const node of nodes) {
        const place = contractPlace(projected, node.id);
        const iso = isoProject(node.x, node.y);
        expect(place.sx - referencePlace.sx).toBeCloseTo(
          iso.sx - referenceIso.sx,
          6,
        );
        expect(place.sy - referencePlace.sy).toBeCloseTo(
          iso.sy - referenceIso.sy,
          6,
        );
      }
    });

    it("P5: conduit endpoints are the same rigid projection", () => {
      const projected = contractProjected(fixture.html);
      const nodes = contractAllNodes(projected.scene);
      const reference = nodes[0];
      if (!reference) throw new Error("fixture has no EntityNodes");
      const referencePlace = contractPlace(projected, reference.id);
      const referenceIso = isoProject(reference.x, reference.y);
      const offsetX = referencePlace.sx - referenceIso.sx;
      const offsetY = referencePlace.sy - referenceIso.sy;

      expect(projected.conduits).toHaveLength(projected.scene.conduits.length);
      for (const projectedConduit of projected.conduits) {
        const { conduit, start, end } = projectedConduit;
        const isoStart = isoProject(conduit.x1, conduit.y1);
        const isoEnd = isoProject(conduit.x2, conduit.y2);
        expect(start.sx).toBeCloseTo(isoStart.sx + offsetX, 6);
        expect(start.sy).toBeCloseTo(isoStart.sy + offsetY, 6);
        expect(end.sx).toBeCloseTo(isoEnd.sx + offsetX, 6);
        expect(end.sy).toBeCloseTo(isoEnd.sy + offsetY, 6);
      }
    });

    it("P4: every placement and conduit endpoint lies inside the viewBox", () => {
      const projected = contractProjected(fixture.html);
      const epsilon = 1e-9;
      const inside = (point: IsoPoint): void => {
        expect(point.sx).toBeGreaterThanOrEqual(-epsilon);
        expect(point.sx).toBeLessThanOrEqual(projected.width + epsilon);
        expect(point.sy).toBeGreaterThanOrEqual(-epsilon);
        expect(point.sy).toBeLessThanOrEqual(projected.height + epsilon);
      };
      for (const node of contractAllNodes(projected.scene)) {
        inside(contractPlace(projected, node.id));
      }
      for (const projectedConduit of projected.conduits) {
        inside(projectedConduit.start);
        inside(projectedConduit.end);
      }
    });
  });
}

// ---------------------------------------------------------------------------
// R1-R5, R9, R10 — the emitted markup for every parseable fixture
// ---------------------------------------------------------------------------

for (const fixture of contractParseableFixtures) {
  describe(`renderSceneMarkup (R1-R5, R9, R10) — ${fixture.name}`, () => {
    it("R1: one <svg> whose viewBox is 0 0 width height", () => {
      const projected = contractProjected(fixture.html);
      const svg = contractRendered(fixture.html);
      const trimmed = svg.trim();
      expect(trimmed.startsWith("<svg")).toBe(true);
      expect(trimmed.endsWith("</svg>")).toBe(true);
      expect(trimmed.match(/<svg[\s>]/g) ?? []).toHaveLength(1);
      expect(contractViewBox(svg)).toEqual([
        0,
        0,
        projected.width,
        projected.height,
      ]);
    });

    it("R2: every EntityNode yields exactly one data-entity group", () => {
      const projected = contractProjected(fixture.html);
      const doc = contractDoc(contractRendered(fixture.html));
      const nodes = contractAllNodes(projected.scene);

      for (const node of nodes) {
        const group = contractEntityGroup(doc, node.id);
        expect(group.tagName.toLowerCase()).toBe("g");
        expect(group.getAttribute("data-shape")).toBe(node.shape);
      }

      const emitted = contractEls(doc, "[data-entity]").map(
        (el) => el.getAttribute("data-entity") ?? "",
      );
      expect([...emitted].sort()).toEqual([...nodes.map((n) => n.id)].sort());
    });

    it("R3: code refs appear verbatim and nothing else does", () => {
      const projected = contractProjected(fixture.html);
      const svg = contractRendered(fixture.html);
      const doc = contractDoc(svg);
      const nodes = contractAllNodes(projected.scene);

      for (const node of nodes) {
        if (node.codeRef === undefined) continue;
        expect(contractEntityGroup(doc, node.id).getAttribute("data-code")).toBe(
          node.codeRef,
        );
      }

      const conduitGroups = contractEls(doc, "[data-conduit]");
      for (const conduit of projected.scene.conduits) {
        if (conduit.codeRef === undefined) continue;
        const carriers = conduitGroups.filter(
          (g) => g.getAttribute("data-code") === conduit.codeRef,
        );
        expect(carriers.length).toBeGreaterThanOrEqual(1);
      }

      const inputRefs = new Set<string>();
      for (const node of nodes) if (node.codeRef) inputRefs.add(node.codeRef);
      for (const conduit of projected.scene.conduits) {
        if (conduit.codeRef) inputRefs.add(conduit.codeRef);
      }
      const emittedRefs = new Set(
        contractEls(doc, "[data-code]").map(
          (el) => el.getAttribute("data-code") ?? "",
        ),
      );
      expect([...emittedRefs].sort()).toEqual([...inputRefs].sort());
    });

    it("R4: every node's first label line appears in the output", () => {
      const projected = contractProjected(fixture.html);
      const doc = contractDoc(contractRendered(fixture.html));
      const text = doc.body.textContent ?? "";
      for (const node of contractAllNodes(projected.scene)) {
        const first = node.labelLines[0];
        if (first === undefined || first === "") continue;
        expect(text).toContain(first);
      }
    });

    it("R5: dim, grey and the five colour treatments render their hooks", () => {
      const projected = contractProjected(fixture.html);
      const doc = contractDoc(contractRendered(fixture.html));
      for (const node of contractAllNodes(projected.scene)) {
        const group = contractEntityGroup(doc, node.id);
        if (node.dim) {
          const opacity = group.getAttribute("opacity");
          expect(opacity).not.toBeNull();
          expect(Number(opacity)).toBeLessThanOrEqual(0.5);
        }
        if (node.treatment !== undefined) {
          expect(group.getAttribute("data-treat")).toBe(node.treatment);
        }
        if (node.treatment === "grey") {
          expect(group.outerHTML).toContain("stroke-dasharray");
        }
      }
    });

    it("R9: paint goes through var(--iso-, with no script and no URL", () => {
      const svg = contractRendered(fixture.html);
      expect(svg).toContain("var(--iso-");
      expect(svg).not.toContain("<script");
      expect(svg).not.toContain("http://");
      expect(svg).not.toContain("https://");
    });

    it("R10: every conduit yields a group whose path ends in a defined marker", () => {
      const projected = contractProjected(fixture.html);
      const svg = contractRendered(fixture.html);
      const doc = contractDoc(svg);

      const conduitGroups = contractEls(doc, "[data-conduit]");
      expect(conduitGroups).toHaveLength(projected.scene.conduits.length);

      const markerIds = new Set(
        contractEls(doc, "defs marker").map((m) => m.getAttribute("id") ?? ""),
      );
      expect(markerIds.size).toBeGreaterThan(0);

      for (const group of conduitGroups) {
        const path = group.querySelector("path[marker-end]");
        expect(path).not.toBeNull();
        const ref = path?.getAttribute("marker-end") ?? "";
        const match = /^url\(#(.+)\)$/.exec(ref.trim());
        expect(match).not.toBeNull();
        expect(markerIds.has(match?.[1] ?? "")).toBe(true);
      }
    });

    it("R1/R2/R9/R10 hold under reducedMotion as well", () => {
      const projected = contractProjected(fixture.html);
      const svg = contractRendered(fixture.html, true);
      const doc = contractDoc(svg);
      expect(contractViewBox(svg)).toEqual([
        0,
        0,
        projected.width,
        projected.height,
      ]);
      const nodes = contractAllNodes(projected.scene);
      expect(contractEls(doc, "[data-entity]")).toHaveLength(nodes.length);
      expect(contractEls(doc, "[data-conduit]")).toHaveLength(
        projected.scene.conduits.length,
      );
      expect(svg).toContain("var(--iso-");
      expect(svg).not.toContain("<script");
    });
  });
}

// ---------------------------------------------------------------------------
// R11, R12 — the compileCard / cardCompilable door
// ---------------------------------------------------------------------------

describe("compileCard (R11)", () => {
  it("passes a not-parseable verdict through unchanged", () => {
    const read = readCard(contractBespoke);
    const compiled = compileCard(contractBespoke, { reducedMotion: false });
    expect(compiled.kind).toBe("not-parseable");
    if (compiled.kind !== "not-parseable" || read.kind !== "not-parseable") {
      throw new Error("expected both verdicts to be not-parseable");
    }
    expect(compiled.reason).toBe(read.reason);
  });

  it("passes a not-diagram verdict through unchanged", () => {
    expect(compileCard("<h1>hi</h1>", { reducedMotion: false })).toEqual({
      kind: "not-diagram",
    });
  });

  it("compiles mini.html into an svg obeying R1, R2, R9 and R10", () => {
    const compiled = compileCard(contractMini, { reducedMotion: false });
    expect(compiled.kind).toBe("scene");
    if (compiled.kind !== "scene") return;

    const projected = contractProjected(contractMini);
    const trimmed = compiled.svg.trim();
    expect(trimmed.startsWith("<svg")).toBe(true);
    expect(trimmed.endsWith("</svg>")).toBe(true);
    expect(contractViewBox(compiled.svg)).toEqual([
      0,
      0,
      projected.width,
      projected.height,
    ]);

    const doc = contractDoc(compiled.svg);
    const nodes = contractAllNodes(projected.scene);
    for (const node of nodes) {
      expect(contractEntityGroup(doc, node.id).getAttribute("data-shape")).toBe(
        node.shape,
      );
    }
    expect(contractEls(doc, "[data-conduit]")).toHaveLength(
      projected.scene.conduits.length,
    );
    expect(compiled.svg).toContain("var(--iso-");
    expect(compiled.svg).not.toContain("<script");
    expect(compiled.svg).toContain("cmd/kraken/cmd_serve.go:37");
  });

  it("compiles every parseable fixture", () => {
    for (const fixture of contractParseableFixtures) {
      const compiled = compileCard(fixture.html, { reducedMotion: true });
      expect(compiled.kind).toBe("scene");
    }
  });
});

describe("cardCompilable (R12)", () => {
  it("is true exactly when readCard returns a scene", () => {
    const cases: Array<{ html: string; expected: boolean }> = [
      { html: contractMini, expected: true },
      { html: contractLinkflow, expected: true },
      { html: contractClickFlow, expected: true },
      { html: contractRecolor, expected: true },
      { html: contractBespoke, expected: false },
      { html: "<h1>hi</h1>", expected: false },
    ];
    for (const testCase of cases) {
      expect(cardCompilable(testCase.html)).toBe(testCase.expected);
      expect(cardCompilable(testCase.html)).toBe(
        readCard(testCase.html).kind === "scene",
      );
    }
  });

  it("is false for an empty string", () => {
    expect(cardCompilable("")).toBe(false);
    expect(readCard("").kind).not.toBe("scene");
  });
});
