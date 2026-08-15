// isoscene.edge.test.ts — the BLIND edge/boundary suite for the isoscene
// compiler, authored by athena-edge against 50-contract.md alone. Every
// package-level identifier is prefixed `edge` because athena-logic shares this
// module namespace.
//
// Dimension: boundaries, refusals and degradations — what the grammar throws
// out, what it tolerates, and what the renderer must still do when an input
// channel is empty (no SMIL, no data-code, no title, no label).

import { describe, it, expect } from "vitest";

import { readCard } from "./reader";
import { isoProject, projectScene } from "./project";
import { renderSceneMarkup } from "./render";
import { compileCard, cardCompilable } from "./index";
import type {
  Conduit,
  EntityNode,
  ProjectedScene,
  SceneGraph,
} from "./types";

import edgeMini from "./fixtures/mini.html?raw";
import edgeBespoke from "./fixtures/bespoke.html?raw";
import edgeLinkflow from "./fixtures/linkflow-arch.html?raw";
import edgeClickFlow from "./fixtures/click-flow.html?raw";
import edgeRecolor from "./fixtures/recolor-walkthrough.html?raw";

// ---------------------------------------------------------------------------
// card builders — small hand-authored cards for shapes the fixtures don't hold
// ---------------------------------------------------------------------------

/** Wrap svg body in the card envelope the grammar requires. */
function edgeCard(body: string, viewBox = "0 0 400 300"): string {
  return (
    `<meta charset="utf-8"><title>edge card</title><h1>edge card</h1>` +
    `<figure class="diagram">` +
    `<svg viewBox="${viewBox}" role="img" aria-label="edge card">${body}</svg>` +
    `<figcaption>edge card caption.</figcaption>` +
    `</figure>`
  );
}

const edgeDefsBlock =
  `<defs><marker id="edge-arrow" viewBox="0 0 10 10" refX="9" refY="5" ` +
  `markerWidth="7" markerHeight="7" orient="auto-start-reverse">` +
  `<path class="arrowhead" d="M0,0 L10,5 L0,10 z"/></marker></defs>`;

/** A rect-backed component g: 80x40 at (x, y) with one label line. */
function edgeBox(
  cls: string,
  x: number,
  y: number,
  label = "box",
  extraAttrs = "",
): string {
  return (
    `<g class="${cls}"${extraAttrs}>` +
    `<rect x="${x}" y="${y}" width="80" height="40" rx="2"/>` +
    `<text x="${x + 40}" y="${y + 24}">${label}</text>` +
    `</g>`
  );
}

/** One edge path with the standard marker. */
function edgePath(cls: string, d: string, extraAttrs = ""): string {
  return `<path class="${cls}" d="${d}" marker-end="url(#edge-arrow)"${extraAttrs}/>`;
}

/** A whole card carrying exactly one edge path plus two anchor boxes. */
function edgeOneEdgeCard(cls: string, d: string, extraAttrs = ""): string {
  return edgeCard(
    edgeDefsBlock +
      `<text class="title" x="10" y="20">one edge</text>` +
      edgeBox("proc", 10, 10, "a") +
      edgeBox("store", 200, 200, "b") +
      edgePath(cls, d, extraAttrs),
    "0 0 600 500",
  );
}

/** Zero SMIL anywhere: 3 conduits, one of them dim. */
const edgeStillCard = edgeCard(
  edgeDefsBlock +
    `<text class="title" x="10" y="20">still card: no SMIL anywhere</text>` +
    edgeBox("proc", 20, 40, "alpha", ` data-code="cmd/alpha.go:1"`) +
    edgeBox("store", 220, 200, "beta") +
    edgePath("edge", "M 100,80 L 220,220", ` id="still-e1"`) +
    edgePath("edge dim", "M 20,80 H 220", ` id="still-e2"`) +
    edgePath("edge dashed", "M 60,40 V 200", ` id="still-e3"`),
  "0 0 400 300",
);

/** Zero SMIL, every conduit dim. */
const edgeAllDimCard = edgeCard(
  edgeDefsBlock +
    `<text class="title" x="10" y="20">all conduits dim</text>` +
    edgeBox("proc", 20, 40, "alpha") +
    edgeBox("store", 220, 200, "beta") +
    edgePath("edge dim", "M 100,80 L 220,220", ` id="dim-e1"`) +
    edgePath("dim edge", "M 20,80 H 220", ` id="dim-e2"`),
  "0 0 400 300",
);

/** Zero SMIL, no conduit dim. */
const edgeNoDimCard = edgeCard(
  edgeDefsBlock +
    `<text class="title" x="10" y="20">no conduit dim</text>` +
    edgeBox("proc", 20, 40, "alpha") +
    edgeBox("store", 220, 200, "beta") +
    edgePath("edge", "M 100,80 L 220,220", ` id="plain-e1"`) +
    edgePath("edge dashed", "M 20,80 H 220", ` id="plain-e2"`) +
    edgePath("edge blue", "M 60,40 V 200", ` id="plain-e3"`),
  "0 0 400 300",
);

/** All six treatments, one box each. */
const edgeSixTreatmentCard = edgeCard(
  `<text class="title" x="10" y="20">six treatments</text>` +
    edgeBox("node grey", 10, 10, "g") +
    edgeBox("node red", 110, 10, "r") +
    edgeBox("node yellow", 210, 10, "y") +
    edgeBox("node green", 10, 110, "gr") +
    edgeBox("node blue", 110, 110, "b") +
    edgeBox("node purple", 210, 110, "p"),
  "0 0 400 300",
);

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

function edgeAt<T>(arr: readonly T[], i: number): T {
  const v = arr[i];
  if (v === undefined) throw new Error(`index ${i} missing (len ${arr.length})`);
  return v;
}

function edgeSceneOf(html: string): SceneGraph {
  const r = readCard(html);
  if (r.kind !== "scene") {
    throw new Error(
      `expected scene, got ${r.kind}` +
        (r.kind === "not-parseable" ? `: ${r.reason}` : ""),
    );
  }
  return r.scene;
}

function edgeReasonOf(html: string): string {
  const r = readCard(html);
  if (r.kind !== "not-parseable") {
    throw new Error(`expected not-parseable, got ${r.kind}`);
  }
  return r.reason;
}

function edgeProjectOf(html: string): ProjectedScene {
  return projectScene(edgeSceneOf(html));
}

function edgeRenderOf(html: string, reducedMotion = false): string {
  return renderSceneMarkup(edgeProjectOf(html), { reducedMotion });
}

/** All EntityNodes in the scene, entities + structures, nested included. */
function edgeAllNodes(scene: SceneGraph): EntityNode[] {
  const out: EntityNode[] = [];
  const walk = (ns: EntityNode[]): void => {
    for (const n of ns) {
      out.push(n);
      walk(n.children);
    }
  };
  walk(scene.entities);
  walk(scene.structures);
  return out;
}

/** Parse an emitted svg string; returns the <svg> root element. */
function edgeRoot(svg: string): Element {
  const doc = new DOMParser().parseFromString(svg, "text/html");
  const root = doc.querySelector("svg");
  if (!root) throw new Error("emitted markup has no <svg> root");
  return root;
}

/** Every element under (and including) root. */
function edgeElements(root: Element): Element[] {
  return [root, ...Array.from(root.getElementsByTagName("*"))];
}

/** Descendants whose local name matches, case-sensitively (SMIL is camelCase). */
function edgeNamed(root: Element, localName: string): Element[] {
  return edgeElements(root).filter((e) => e.localName === localName);
}

/** SMIL clock value in seconds; NaN when the value is not a plain clock. */
function edgeClockSeconds(raw: string): number {
  const m = /^\s*(-?\d+(?:\.\d+)?)\s*(ms|s|min)?\s*$/.exec(raw);
  if (!m) return NaN;
  const n = Number(edgeAt(m, 1));
  const unit = m[2];
  if (unit === "ms") return n / 1000;
  if (unit === "min") return n * 60;
  return n;
}

function edgeConduitGroups(root: Element): Element[] {
  return Array.from(root.querySelectorAll("[data-conduit]"));
}

function edgeEntityGroup(root: Element, id: string): Element {
  const g = root.querySelector(`[data-entity="${id}"]`);
  if (!g) throw new Error(`no emitted group for entity ${id}`);
  return g;
}

function edgeDataCodeValues(root: Element): string[] {
  return edgeElements(root)
    .map((e) => e.getAttribute("data-code"))
    .filter((v): v is string => v !== null);
}

function edgeAllCodeRefs(scene: SceneGraph): Set<string> {
  const s = new Set<string>();
  for (const n of edgeAllNodes(scene)) if (n.codeRef) s.add(n.codeRef);
  for (const c of scene.conduits) if (c.codeRef) s.add(c.codeRef);
  return s;
}

function edgeConduitById(scene: SceneGraph, id: string): Conduit {
  const c = scene.conduits.find((x) => x.id === id);
  if (!c) throw new Error(`no conduit with id ${id}`);
  return c;
}

// ===========================================================================
// 1. Envelope refusals — not-diagram
// ===========================================================================

describe("edge — envelope refusals (not-diagram)", () => {
  it("plain HTML with no figure.diagram is not-diagram", () => {
    expect(readCard("<h1>hi</h1>")).toEqual({ kind: "not-diagram" });
  });

  it("the empty string is not-diagram", () => {
    expect(readCard("")).toEqual({ kind: "not-diagram" });
  });

  it("a figure.diagram holding no svg at all is not-diagram", () => {
    const html =
      `<figure class="diagram"><figcaption>caption only.</figcaption></figure>`;
    expect(readCard(html)).toEqual({ kind: "not-diagram" });
  });

  it("an svg outside any figure.diagram is not-diagram", () => {
    const html = `<svg viewBox="0 0 100 100"><rect x="1" y="1" width="2" height="2"/></svg>`;
    expect(readCard(html)).toEqual({ kind: "not-diagram" });
  });

  it("an svg inside a figure that is not .diagram is not-diagram", () => {
    const html =
      `<figure class="photo"><svg viewBox="0 0 100 100"></svg></figure>`;
    expect(readCard(html)).toEqual({ kind: "not-diagram" });
  });

  it("not-diagram carries no reason key", () => {
    expect(Object.keys(readCard("<h1>hi</h1>"))).toEqual(["kind"]);
  });
});

// ===========================================================================
// 2. Grammar refusals — not-parseable
// ===========================================================================

describe("edge — grammar refusals (not-parseable)", () => {
  it("an unknown class token at top level fails the whole card", () => {
    const html = edgeCard(
      `<text class="title" x="10" y="20">t</text>` +
        edgeBox("proc", 10, 40, "ok") +
        `<polygon class="starburst" points="200,10 240,90 160,90"/>`,
    );
    const reason = edgeReasonOf(html);
    expect(reason).toContain("starburst");
    expect(reason).toContain("polygon");
  });

  it("an unknown class token on a descendant of an accounted g fails the card", () => {
    const html = edgeCard(
      `<g class="proc"><rect class="frobnicate" x="10" y="10" width="40" height="20"/></g>`,
    );
    const reason = edgeReasonOf(html);
    expect(reason).toContain("frobnicate");
    expect(reason).toContain("rect");
  });

  it("one unknown token among otherwise-legal tokens still fails the card", () => {
    const html = edgeCard(edgeBox("proc holds sparkly", 10, 10, "x"));
    expect(edgeReasonOf(html)).toContain("sparkly");
  });

  it("a classless graphical element directly under the svg fails the card", () => {
    const html = edgeCard(
      `<text class="title" x="10" y="20">t</text>` +
        `<rect x="10" y="40" width="80" height="40"/>`,
    );
    expect(edgeReasonOf(html)).toContain("rect");
  });

  it("a classless path directly under the svg fails the card", () => {
    const html = edgeCard(`<path d="M 10,10 C 40,0 60,40 90,20" fill="none"/>`);
    expect(edgeReasonOf(html)).toContain("path");
  });

  it("the reason names the FIRST offender in document order, not a later one", () => {
    const html = edgeCard(
      `<polygon class="zork" points="10,10 20,20 30,10"/>` +
        `<circle cx="200" cy="200" r="10"/>`,
    );
    const reason = edgeReasonOf(html);
    expect(reason).toContain("zork");
    expect(reason).not.toContain("circle");
  });

  it("two TREATMENT tokens on one element fail the card", () => {
    const html = edgeCard(edgeBox("node red blue", 10, 10, "x"));
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("two TREATMENT tokens on an edge path fail the card and the reason names the tag", () => {
    const html = edgeCard(
      edgeDefsBlock + edgePath("edge red yellow", "M 10,10 L 90,10"),
    );
    expect(edgeReasonOf(html)).toContain("path");
  });

  it("three TREATMENT tokens fail the card", () => {
    const html = edgeCard(edgeBox("node red blue green", 10, 10, "x"));
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("one treatment plus dim plus a modifier is legal, not a refusal", () => {
    const html = edgeCard(edgeBox("node blue dim holds", 10, 10, "x"));
    const scene = edgeSceneOf(html);
    const n = edgeAt(scene.entities, 0);
    expect(n.treatment).toBe("blue");
    expect(n.dim).toBe(true);
    expect(n.holds).toBe(true);
  });

  it("an empty class attribute on a graphical element is not a non-empty class list — it fails at top level", () => {
    const html = edgeCard(`<rect class="" x="10" y="10" width="20" height="20"/>`);
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("a refusal is a value, never a throw, and never a partial scene", () => {
    const r = readCard(edgeBespoke);
    expect(r.kind).toBe("not-parseable");
    expect(Object.keys(r).sort()).toEqual(["kind", "reason"]);
  });

  it("only the FIRST svg is read: a bespoke SECOND svg cannot fail the card", () => {
    const html =
      `<figure class="diagram">` +
      `<svg viewBox="0 0 400 300">` +
      `<text class="title" x="10" y="20">first svg</text>` +
      edgeBox("proc", 10, 40, "a") +
      `</svg>` +
      `<svg viewBox="0 0 100 100"><polygon class="starburst" points="1,1 2,2 3,1"/></svg>` +
      `</figure>`;
    const scene = edgeSceneOf(html);
    expect(scene.title).toBe("first svg");
    expect(scene.viewBox).toEqual({ width: 400, height: 300 });
  });

  it("only the FIRST svg is read: a bespoke FIRST svg fails even with a clean second", () => {
    const html =
      `<figure class="diagram">` +
      `<svg viewBox="0 0 400 300"><polygon class="starburst" points="1,1 2,2 3,1"/></svg>` +
      `<svg viewBox="0 0 100 100">${edgeBox("proc", 1, 1, "a")}</svg>` +
      `</figure>`;
    expect(edgeReasonOf(html)).toContain("starburst");
  });

  it("an svg holding only accounted structural elements is a scene, not a refusal", () => {
    const html = edgeCard(edgeDefsBlock, "0 0 120 90");
    const scene = edgeSceneOf(html);
    expect(scene.entities).toEqual([]);
    expect(scene.structures).toEqual([]);
    expect(scene.conduits).toEqual([]);
    expect(scene.title).toBe("");
  });
});

// ===========================================================================
// 3. transform acceptance and refusal
// ===========================================================================

describe("edge — transform acceptance and refusal", () => {
  it("translate(a,b) resolves an edge's endpoints", () => {
    const html = edgeOneEdgeCard(
      "edge",
      "M 0,0 L 100,0",
      ` transform="translate(10,20)"`,
    );
    const c = edgeAt(edgeSceneOf(html).conduits, 0);
    expect([c.x1, c.y1, c.x2, c.y2]).toEqual([10, 20, 110, 20]);
  });

  it("translate(a, b) with a space after the comma resolves identically", () => {
    const html = edgeOneEdgeCard(
      "edge",
      "M 0,0 L 100,0",
      ` transform="translate(10, 20)"`,
    );
    const c = edgeAt(edgeSceneOf(html).conduits, 0);
    expect([c.x1, c.y1, c.x2, c.y2]).toEqual([10, 20, 110, 20]);
  });

  it("translate(a) with one argument shifts x only", () => {
    const html = edgeOneEdgeCard(
      "edge",
      "M 0,0 L 10,0",
      ` transform="translate(15)"`,
    );
    const c = edgeAt(edgeSceneOf(html).conduits, 0);
    expect([c.x1, c.y1, c.x2, c.y2]).toEqual([15, 0, 25, 0]);
  });

  it("a negative translate resolves", () => {
    const html = edgeOneEdgeCard(
      "edge",
      "M 100,100 L 200,100",
      ` transform="translate(-40,-30)"`,
    );
    const c = edgeAt(edgeSceneOf(html).conduits, 0);
    expect([c.x1, c.y1, c.x2, c.y2]).toEqual([60, 70, 160, 70]);
  });

  it("a translate chain composes across nested groups onto an entity's geometry", () => {
    const html = edgeCard(
      `<g class="group" transform="translate(10,20)">` +
        `<rect x="0" y="0" width="300" height="200" rx="8"/>` +
        `<g class="proc" transform="translate(5,5)">` +
        `<rect x="0" y="0" width="30" height="40"/>` +
        `<text x="15" y="20">inner</text>` +
        `</g>` +
        `</g>`,
      "0 0 400 300",
    );
    const scene = edgeSceneOf(html);
    const structure = edgeAt(scene.structures, 0);
    expect([structure.x, structure.y, structure.width, structure.height]).toEqual([
      10, 20, 300, 200,
    ]);
    const child = edgeAt(structure.children, 0);
    expect([child.x, child.y, child.width, child.height]).toEqual([15, 25, 30, 40]);
  });

  it("rotate() fails the card", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "x", ` transform="rotate(45)"`));
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("scale() fails the card", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "x", ` transform="scale(2)"`));
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("matrix() fails the card", () => {
    const html = edgeCard(
      edgeBox("proc", 10, 10, "x", ` transform="matrix(1,0,0,1,0,0)"`),
    );
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("translate followed by a second transform fails the card", () => {
    const html = edgeCard(
      edgeBox("proc", 10, 10, "x", ` transform="translate(10,20) scale(2)"`),
    );
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("space-separated translate arguments are outside the accepted forms", () => {
    const html = edgeCard(
      edgeBox("proc", 10, 10, "x", ` transform="translate(10 20)"`),
    );
    expect(readCard(html).kind).toBe("not-parseable");
  });

  it("a non-translate transform on a classless descendant still fails the card", () => {
    const html = edgeCard(
      `<g class="proc"><rect x="0" y="0" width="10" height="10" transform="rotate(10)"/></g>`,
    );
    expect(readCard(html).kind).toBe("not-parseable");
  });
});

// ===========================================================================
// 4. Class token order insensitivity (A1)
// ===========================================================================

describe("edge — class token order insensitivity (A1)", () => {
  it("`blue node` reads byte-identically to `node blue`", () => {
    const a = edgeSceneOf(edgeCard(edgeBox("blue node", 10, 10, "x")));
    const b = edgeSceneOf(edgeCard(edgeBox("node blue", 10, 10, "x")));
    expect(a).toEqual(b);
  });

  it("a three-token class reads identically under reversal", () => {
    const a = edgeSceneOf(edgeCard(edgeBox("dim blue node", 10, 10, "x")));
    const b = edgeSceneOf(edgeCard(edgeBox("node blue dim", 10, 10, "x")));
    expect(a).toEqual(b);
  });

  it("`dashed edge` reads identically to `edge dashed`", () => {
    const a = edgeSceneOf(edgeOneEdgeCard("dashed edge", "M 0,0 L 10,0"));
    const b = edgeSceneOf(edgeOneEdgeCard("edge dashed", "M 0,0 L 10,0"));
    expect(a).toEqual(b);
  });

  it("`head cell` reads identically to `cell head`", () => {
    const a = edgeSceneOf(edgeCard(edgeBox("head cell", 10, 10, "x")));
    const b = edgeSceneOf(edgeCard(edgeBox("cell head", 10, 10, "x")));
    expect(a).toEqual(b);
    expect(edgeAt(a.entities, 0).head).toBe(true);
  });

  it("extra internal whitespace between tokens does not change the reading", () => {
    const a = edgeSceneOf(edgeCard(edgeBox("node  blue", 10, 10, "x")));
    const b = edgeSceneOf(edgeCard(edgeBox("node blue", 10, 10, "x")));
    expect(a).toEqual(b);
  });
});

// ===========================================================================
// 5. codeRef provenance (A2) and the no-invention rule (R3)
// ===========================================================================

describe("edge — codeRef provenance (A2/R3)", () => {
  it("data-code on an inner rect never becomes the g's codeRef", () => {
    const html = edgeCard(
      `<g class="proc">` +
        `<rect x="10" y="10" width="80" height="40" data-code="inner/only.go:9"/>` +
        `<text x="50" y="34">a</text>` +
        `</g>`,
    );
    const n = edgeAt(edgeSceneOf(html).entities, 0);
    expect(n.codeRef).toBeUndefined();
  });

  it("a parent's data-code is never inherited by a nested child", () => {
    const html = edgeCard(
      `<g class="group" data-code="outer/parent.go:1">` +
        `<rect x="0" y="0" width="300" height="200"/>` +
        `<g class="proc">` +
        `<rect x="20" y="20" width="80" height="40"/>` +
        `<text x="60" y="44">c</text>` +
        `</g>` +
        `</g>`,
      "0 0 400 300",
    );
    const structure = edgeAt(edgeSceneOf(html).structures, 0);
    expect(structure.codeRef).toBe("outer/parent.go:1");
    expect(edgeAt(structure.children, 0).codeRef).toBeUndefined();
  });

  it("a codeRef carrying spaces and a range survives verbatim into the output", () => {
    const ref = "dir with space/web serve.go:116-140";
    const html = edgeCard(
      edgeBox("proc", 10, 10, "a", ` data-code="${ref}"`) +
        edgeBox("store", 120, 120, "b"),
    );
    expect(edgeAt(edgeSceneOf(html).entities, 0).codeRef).toBe(ref);
    const root = edgeRoot(edgeRenderOf(html));
    expect(edgeDataCodeValues(root)).toContain(ref);
  });

  it("a card with zero data-code yields zero refs end to end", () => {
    const scene = edgeSceneOf(edgeRecolor);
    expect(edgeAllCodeRefs(scene).size).toBe(0);
    for (const n of edgeAllNodes(scene)) expect(n.codeRef).toBeUndefined();
    for (const c of scene.conduits) expect(c.codeRef).toBeUndefined();
    const svg = renderSceneMarkup(projectScene(scene), { reducedMotion: false });
    expect(edgeDataCodeValues(edgeRoot(svg))).toEqual([]);
  });

  it("a data-code the reader did not capture never appears in the output (R3)", () => {
    const html = edgeCard(
      `<g class="proc">` +
        `<rect x="10" y="10" width="80" height="40" data-code="never/captured.go:3"/>` +
        `<text x="50" y="34">a</text>` +
        `</g>` +
        edgeBox("store", 150, 150, "b"),
    );
    expect(edgeRenderOf(html)).not.toContain("never/captured.go:3");
    expect(edgeDataCodeValues(edgeRoot(edgeRenderOf(html)))).toEqual([]);
  });

  it("no emitted data-code value is absent from the input's codeRef set (mini)", () => {
    const scene = edgeSceneOf(edgeMini);
    const refs = edgeAllCodeRefs(scene);
    const emitted = edgeDataCodeValues(
      edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false })),
    );
    expect(emitted.length).toBeGreaterThan(0);
    for (const v of emitted) expect(refs.has(v)).toBe(true);
  });

  it("no emitted data-code value is absent from the input's codeRef set (linkflow)", () => {
    const scene = edgeSceneOf(edgeLinkflow);
    const refs = edgeAllCodeRefs(scene);
    expect(refs.size).toBeGreaterThan(0);
    const emitted = edgeDataCodeValues(
      edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: true })),
    );
    for (const v of emitted) expect(refs.has(v)).toBe(true);
  });
});

// ===========================================================================
// 6. labelLines and title tolerance (A3 / A9)
// ===========================================================================

describe("edge — labels and title tolerance (A3/A9)", () => {
  it("a card with no text.title reads title as the empty string", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "a"));
    expect(edgeSceneOf(html).title).toBe("");
  });

  it("title absence is not bespoke — the card is still a full scene", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "a"));
    const scene = edgeSceneOf(html);
    expect(scene.entities.length).toBe(1);
    expect(cardCompilable(html)).toBe(true);
  });

  it("a card with no text.title carries no title TextMark", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "a"));
    expect(edgeSceneOf(html).texts.filter((t) => t.kind === "title")).toEqual([]);
  });

  it("mini's title TextMark agrees with scene.title", () => {
    const scene = edgeSceneOf(edgeMini);
    const titles = scene.texts.filter((t) => t.kind === "title");
    expect(titles.length).toBe(1);
    expect(edgeAt(edgeAt(titles, 0).lines, 0)).toBe(scene.title);
  });

  it("a parent's labelLines exclude text belonging to a nested classed child", () => {
    const scene = edgeSceneOf(edgeMini);
    const group = edgeAt(scene.structures, 0);
    expect(group.labelLines).toEqual(["data zone — file-ownership"]);
    const store = edgeAt(group.children, 0);
    expect(store.labelLines).toEqual(["+ kraken.db", "sqlite db"]);
  });

  it("tspan lines split in document order and are trimmed", () => {
    const scene = edgeSceneOf(edgeMini);
    const proc = edgeAt(scene.entities, 0);
    expect(proc.labelLines).toEqual(["kraken serve", "OS process"]);
  });

  it("an entity with no <text> at all reads labelLines as an empty array", () => {
    const html = edgeCard(
      `<g class="fn"><path d="M 10,20 L 60,20 L 60,70 L 10,70 z"/></g>`,
    );
    expect(edgeAt(edgeSceneOf(html).entities, 0).labelLines).toEqual([]);
  });

  it("an entity with no label still renders its group (R2/R4 degradation)", () => {
    const html = edgeCard(
      `<g class="fn"><path d="M 10,20 L 60,20 L 60,70 L 10,70 z"/></g>` +
        edgeBox("store", 150, 150, "b"),
    );
    const scene = edgeSceneOf(html);
    const bare = edgeAt(scene.entities, 0);
    expect(bare.labelLines).toEqual([]);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    expect(edgeEntityGroup(root, bare.id).getAttribute("data-shape")).toBe("fn");
  });
});

// ===========================================================================
// 7. Geometry and viewBox boundaries (A4 / A10)
// ===========================================================================

describe("edge — geometry and viewBox boundaries (A4/A10)", () => {
  it("the viewBox's THIRD and FOURTH numbers are width and height", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "a"), "0 0 640 480");
    expect(edgeSceneOf(html).viewBox).toEqual({ width: 640, height: 480 });
  });

  it("a non-zero viewBox origin does not leak into width/height", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "a"), "10 20 640 480");
    expect(edgeSceneOf(html).viewBox).toEqual({ width: 640, height: 480 });
  });

  it("mini's viewBox is 860 x 500", () => {
    expect(edgeSceneOf(edgeMini).viewBox).toEqual({ width: 860, height: 500 });
  });

  it("a rect-backed part reports its rect exactly, ignoring later rects", () => {
    const scene = edgeSceneOf(edgeMini);
    const proc = edgeAt(scene.entities, 0);
    expect([proc.x, proc.y, proc.width, proc.height]).toEqual([100, 100, 200, 80]);
  });

  it("a circle-backed part reports the bounding box cx−r, cy−r, 2r, 2r", () => {
    const html = edgeCard(
      `<g class="node"><circle cx="100" cy="200" r="30"/><text x="100" y="204">c</text></g>`,
      "0 0 400 400",
    );
    const n = edgeAt(edgeSceneOf(html).entities, 0);
    expect([n.x, n.y, n.width, n.height]).toEqual([70, 170, 60, 60]);
  });

  it("an ellipse-backed part reports cx−rx, cy−ry, 2rx, 2ry", () => {
    const html = edgeCard(
      `<g class="store"><ellipse cx="200" cy="100" rx="50" ry="20"/></g>`,
      "0 0 400 400",
    );
    const n = edgeAt(edgeSceneOf(html).entities, 0);
    expect([n.x, n.y, n.width, n.height]).toEqual([150, 80, 100, 40]);
  });

  it("a path-only silhouette reports the extent of its absolute coordinate pairs", () => {
    const html = edgeCard(
      `<g class="fn"><path d="M 10,20 L 60,20 L 60,70 L 10,70 z"/></g>`,
      "0 0 400 400",
    );
    const n = edgeAt(edgeSceneOf(html).entities, 0);
    expect([n.x, n.y, n.width, n.height]).toEqual([10, 20, 50, 50]);
  });

  it("a translate on the part itself resolves into absolute geometry", () => {
    const html = edgeCard(
      `<g class="proc" transform="translate(10,20)">` +
        `<rect x="5" y="5" width="30" height="40"/>` +
        `<text x="20" y="25">p</text>` +
        `</g>`,
    );
    const n = edgeAt(edgeSceneOf(html).entities, 0);
    expect([n.x, n.y, n.width, n.height]).toEqual([15, 25, 30, 40]);
  });
});

// ===========================================================================
// 8. Conduit endpoints on H / V / relative paths (A5)
// ===========================================================================

describe("edge — conduit endpoints on H/V/relative paths (A5)", () => {
  const at = (d: string): [number, number, number, number] => {
    const c = edgeAt(edgeSceneOf(edgeOneEdgeCard("edge", d)).conduits, 0);
    return [c.x1, c.y1, c.x2, c.y2];
  };

  it("an absolute L path", () => {
    expect(at("M 20,30 L 200,130")).toEqual([20, 30, 200, 130]);
  });

  it("a multi-segment absolute L path terminates at the last point", () => {
    expect(at("M 20,30 L 100,30 L 100,90 L 240,90")).toEqual([20, 30, 240, 90]);
  });

  it("an H command keeps y and moves x", () => {
    expect(at("M 40,60 H 240")).toEqual([40, 60, 240, 60]);
  });

  it("a V command keeps x and moves y", () => {
    expect(at("M 40,60 V 260")).toEqual([40, 60, 40, 260]);
  });

  it("mini's dashed V conduit reads 180,180 -> 180,380", () => {
    const scene = edgeSceneOf(edgeMini);
    const dashed = scene.conduits.find((c) => c.dashed);
    expect(dashed).toBeDefined();
    expect([dashed!.x1, dashed!.y1, dashed!.x2, dashed!.y2]).toEqual([
      180, 180, 180, 380,
    ]);
  });

  it("relative l segments accumulate from the M point", () => {
    expect(at("M 40,60 l 100,20 l 50,-10")).toEqual([40, 60, 190, 70]);
  });

  it("relative h and v segments accumulate", () => {
    expect(at("M 10,10 h 90 v 40")).toEqual([10, 10, 100, 50]);
  });

  it("absolute and relative segments mix", () => {
    expect(at("M 10,10 L 60,10 l 20,20 H 200")).toEqual([10, 10, 200, 30]);
  });

  it("an implicit lineto after M terminates at the last pair", () => {
    expect(at("M 10,10 60,40")).toEqual([10, 10, 60, 40]);
  });

  it("x1/y1 is the FIRST M point even when the path has many commands", () => {
    const c = edgeAt(
      edgeSceneOf(edgeOneEdgeCard("edge", "M 5,7 L 100,7 V 300 H 12")).conduits,
      0,
    );
    expect([c.x1, c.y1]).toEqual([5, 7]);
    expect([c.x2, c.y2]).toEqual([12, 300]);
  });

  it("the conduit keeps d verbatim", () => {
    const d = "M 5,7 L 100,7 V 300 H 12";
    expect(edgeAt(edgeSceneOf(edgeOneEdgeCard("edge", d)).conduits, 0).d).toBe(d);
  });

  it("dashed, dim, treatment, id and codeRef come from the path element itself", () => {
    const html = edgeOneEdgeCard(
      "edge dashed dim blue",
      "M 0,0 L 10,10",
      ` id="ce1" data-code="web/serve.go:1"`,
    );
    const c = edgeAt(edgeSceneOf(html).conduits, 0);
    expect(c.dashed).toBe(true);
    expect(c.dim).toBe(true);
    expect(c.treatment).toBe("blue");
    expect(c.id).toBe("ce1");
    expect(c.codeRef).toBe("web/serve.go:1");
  });

  it("an edge path with no id carries no id", () => {
    const c = edgeAt(edgeSceneOf(edgeOneEdgeCard("edge", "M 0,0 L 10,10")).conduits, 0);
    expect(c.id).toBeUndefined();
  });

  it("an undecorated edge is not dashed, not dim and untreated", () => {
    const c = edgeAt(edgeSceneOf(edgeOneEdgeCard("edge", "M 0,0 L 10,10")).conduits, 0);
    expect(c.dashed).toBe(false);
    expect(c.dim).toBe(false);
    expect(c.treatment).toBeUndefined();
  });
});

// ===========================================================================
// 9. flow-dot binding (A6)
// ===========================================================================

describe("edge — flow-dot binding (A6)", () => {
  it("mini's mini-e1 takes dur and begin verbatim", () => {
    const c = edgeConduitById(edgeSceneOf(edgeMini), "mini-e1");
    expect(c.flow).toEqual({ dur: "3s", begin: "0s" });
  });

  it("mini's unbound dashed conduit carries no flow", () => {
    const scene = edgeSceneOf(edgeMini);
    const dashed = scene.conduits.find((c) => c.dashed);
    expect(dashed!.flow).toBeUndefined();
  });

  it("the flow-dot itself never becomes an entity", () => {
    const scene = edgeSceneOf(edgeMini);
    for (const n of edgeAllNodes(scene)) expect(n.shape).not.toBe("flow-dot");
    expect(scene.entities.length).toBe(3);
  });

  it("the binding is by exact id — #e1 never matches e10", () => {
    const html = edgeCard(
      edgeDefsBlock +
        edgeBox("proc", 10, 10, "a") +
        edgeBox("store", 200, 200, "b") +
        edgePath("edge", "M 50,50 L 200,200", ` id="e1"`) +
        edgePath("edge", "M 60,60 L 210,210", ` id="e10"`) +
        `<circle class="flow-dot" r="4">` +
        `<animateMotion dur="2.5s" repeatCount="indefinite" begin="0.5s">` +
        `<mpath href="#e1"/></animateMotion></circle>`,
      "0 0 400 300",
    );
    const scene = edgeSceneOf(html);
    expect(edgeConduitById(scene, "e1").flow).toEqual({ dur: "2.5s", begin: "0.5s" });
    expect(edgeConduitById(scene, "e10").flow).toBeUndefined();
  });

  it("an mpath naming an id no edge carries binds nothing and is not a refusal", () => {
    const html = edgeCard(
      edgeDefsBlock +
        edgeBox("proc", 10, 10, "a") +
        edgePath("edge", "M 50,50 L 200,200", ` id="real-e"`) +
        `<circle class="flow-dot" r="4">` +
        `<animateMotion dur="1s" begin="0s"><mpath href="#ghost-e"/></animateMotion>` +
        `</circle>`,
      "0 0 400 300",
    );
    const scene = edgeSceneOf(html);
    expect(edgeConduitById(scene, "real-e").flow).toBeUndefined();
  });

  it("a card with zero flow-dots is still a full scene with flow-free conduits", () => {
    const scene = edgeSceneOf(edgeStillCard);
    expect(scene.conduits.length).toBe(3);
    for (const c of scene.conduits) expect(c.flow).toBeUndefined();
    expect(scene.entities.length).toBe(2);
    expect(scene.title).toBe("still card: no SMIL anywhere");
  });

  it("recolor binds flow on exactly 4 conduits", () => {
    const scene = edgeSceneOf(edgeRecolor);
    expect(scene.conduits.length).toBe(11);
    expect(scene.conduits.filter((c) => c.flow).length).toBe(4);
  });

  it("click-flow binds flow on exactly 5 of its 20 conduits", () => {
    const scene = edgeSceneOf(edgeClickFlow);
    expect(scene.conduits.length).toBe(20);
    expect(scene.conduits.filter((c) => c.flow).length).toBe(5);
  });
});

// ===========================================================================
// 10. Routing: entities vs structures vs steps vs badges vs decoration (A7/A8)
// ===========================================================================

describe("edge — scene-graph routing (A7/A8)", () => {
  it("a classed NON-g element carrying a SHAPE class is never an entity", () => {
    const html = edgeCard(
      `<text class="title" x="10" y="20">t</text>` +
        `<rect class="node" x="10" y="40" width="80" height="40"/>`,
    );
    const scene = edgeSceneOf(html);
    expect(scene.entities).toEqual([]);
    expect(scene.structures).toEqual([]);
  });

  it("spark, arrowhead and axis produce no scene-graph row", () => {
    const html = edgeCard(
      edgeDefsBlock +
        edgeBox("proc", 10, 10, "a") +
        `<path class="spark" d="M 10,200 L 40,180 L 70,210"/>` +
        `<path class="axis" d="M 0,290 H 400"/>`,
    );
    const scene = edgeSceneOf(html);
    expect(scene.entities.length).toBe(1);
    expect(scene.structures).toEqual([]);
    expect(scene.conduits).toEqual([]);
  });

  it("group, region and lane land in structures, never in entities", () => {
    const html = edgeCard(
      `<g class="group"><rect x="0" y="0" width="100" height="100"/></g>` +
        `<g class="region"><rect x="110" y="0" width="100" height="100"/></g>` +
        `<g class="lane"><rect x="220" y="0" width="100" height="100"/></g>` +
        edgeBox("proc", 0, 150, "a"),
      "0 0 400 300",
    );
    const scene = edgeSceneOf(html);
    expect(scene.structures.map((s) => s.shape)).toEqual(["group", "region", "lane"]);
    expect(scene.entities.map((e) => e.shape)).toEqual(["proc"]);
  });

  it("click-flow holds exactly 5 lane structures", () => {
    const scene = edgeSceneOf(edgeClickFlow);
    expect(scene.structures.filter((s) => s.shape === "lane").length).toBe(5);
  });

  it("step badges carry OPAQUE labels — a `3b` survives untouched", () => {
    expect(edgeSceneOf(edgeMini).steps.map((s) => s.label)).toEqual(["3b"]);
  });

  it("non-numeric and multi-character step labels survive in document order", () => {
    const html = edgeCard(
      `<g class="step"><circle cx="20" cy="20" r="11"/><text x="20" y="24">3b</text></g>` +
        `<g class="step"><circle cx="60" cy="20" r="11"/><text x="60" y="24">10</text></g>` +
        `<g class="step"><circle cx="100" cy="20" r="11"/><text x="100" y="24">4c</text></g>` +
        edgeBox("proc", 10, 100, "a"),
      "0 0 400 300",
    );
    expect(edgeSceneOf(html).steps.map((s) => s.label)).toEqual(["3b", "10", "4c"]);
  });

  it("click-flow holds exactly 8 steps", () => {
    expect(edgeSceneOf(edgeClickFlow).steps.length).toBe(8);
  });

  it("badges land in badges, not in entities or steps", () => {
    const html = edgeCard(
      `<g class="badge"><rect x="10" y="10" width="50" height="20" rx="10"/>` +
        `<text x="35" y="24">×800</text></g>` +
        edgeBox("proc", 10, 100, "a"),
      "0 0 400 300",
    );
    const scene = edgeSceneOf(html);
    expect(scene.badges.map((b) => b.label)).toEqual(["×800"]);
    expect(scene.steps).toEqual([]);
    expect(scene.entities.map((e) => e.shape)).toEqual(["proc"]);
  });

  it("a group's markup-nested classed children become its children, not top-level entities", () => {
    const scene = edgeSceneOf(edgeMini);
    expect(scene.structures.length).toBe(1);
    const group = edgeAt(scene.structures, 0);
    expect(group.children.map((c) => c.shape)).toEqual(["store"]);
    expect(edgeAt(group.children, 0).treatment).toBe("blue");
    expect(scene.entities.map((e) => e.shape)).toEqual(["proc", "node", "pill"]);
  });

  it("mini's dim node and grey pill read their modifiers off their own class list", () => {
    const scene = edgeSceneOf(edgeMini);
    const dimNode = scene.entities.find((e) => e.shape === "node");
    const greyPill = scene.entities.find((e) => e.shape === "pill");
    expect(dimNode!.dim).toBe(true);
    expect(dimNode!.treatment).toBeUndefined();
    expect(greyPill!.treatment).toBe("grey");
    expect(greyPill!.dim).toBe(false);
  });

  it("mini's node ids are synthetic and unique across every depth", () => {
    const ids = edgeAllNodes(edgeSceneOf(edgeMini)).map((n) => n.id);
    expect(ids.length).toBe(5);
    expect(new Set(ids).size).toBe(5);
  });

  it("edgeLabel texts land in texts, never as entities", () => {
    const scene = edgeSceneOf(edgeMini);
    const labels = scene.texts.filter((t) => t.kind === "edgeLabel");
    expect(labels.map((t) => t.lines.join(" "))).toEqual(["writes rows"]);
  });

  it("recolor holds exactly 5 decision entities at any depth", () => {
    const scene = edgeSceneOf(edgeRecolor);
    expect(edgeAllNodes(scene).filter((n) => n.shape === "decision").length).toBe(5);
  });

  it("click-flow holds exactly 17 cell entities at any depth", () => {
    const scene = edgeSceneOf(edgeClickFlow);
    expect(edgeAllNodes(scene).filter((n) => n.shape === "cell").length).toBe(17);
  });
});

// ===========================================================================
// 11. The bespoke fixture (A11)
// ===========================================================================

describe("edge — the bespoke fixture is whole-card not-parseable (A11)", () => {
  it("reads as not-parseable naming the starburst", () => {
    const r = readCard(edgeBespoke);
    expect(r.kind).toBe("not-parseable");
    expect(edgeReasonOf(edgeBespoke)).toContain("starburst");
  });

  it("names the offending element's tag as well as its class token", () => {
    expect(edgeReasonOf(edgeBespoke)).toContain("polygon");
  });

  it("never offers a partial scene: no scene key is present", () => {
    const r = readCard(edgeBespoke);
    expect("scene" in r).toBe(false);
  });

  it("is not compilable", () => {
    expect(cardCompilable(edgeBespoke)).toBe(false);
  });

  it("compileCard passes the verdict through with the identical reason", () => {
    const reason = edgeReasonOf(edgeBespoke);
    expect(compileCard(edgeBespoke, { reducedMotion: false })).toEqual({
      kind: "not-parseable",
      reason,
    });
  });
});

// ===========================================================================
// 12. Projection boundaries (P3/P4)
// ===========================================================================

describe("edge — projection boundaries (P3/P4)", () => {
  it("isoProject defaults z to 0", () => {
    expect(isoProject(10, 4)).toEqual(isoProject(10, 4, 0));
    expect(isoProject(10, 4)).toEqual({ sx: 6, sy: 7 });
  });

  it("isoProject at the origin is the origin", () => {
    expect(isoProject(0, 0, 0)).toEqual({ sx: 0, sy: 0 });
  });

  it("negative card coordinates project without special-casing", () => {
    expect(isoProject(-10, -4, 0)).toEqual({ sx: -6, sy: -7 });
  });

  it("every placement of the still card lies inside 0..width x 0..height", () => {
    const p = edgeProjectOf(edgeStillCard);
    expect(Number.isFinite(p.width)).toBe(true);
    expect(Number.isFinite(p.height)).toBe(true);
    expect(p.width).toBeGreaterThan(0);
    expect(p.height).toBeGreaterThan(0);
    for (const pl of p.placements.values()) {
      expect(pl.base.sx).toBeGreaterThanOrEqual(0);
      expect(pl.base.sx).toBeLessThanOrEqual(p.width);
      expect(pl.base.sy).toBeGreaterThanOrEqual(0);
      expect(pl.base.sy).toBeLessThanOrEqual(p.height);
    }
  });

  it("every projected conduit endpoint lies inside the emitted box", () => {
    const p = edgeProjectOf(edgeStillCard);
    for (const c of p.conduits) {
      for (const pt of [c.start, c.end]) {
        expect(pt.sx).toBeGreaterThanOrEqual(0);
        expect(pt.sx).toBeLessThanOrEqual(p.width);
        expect(pt.sy).toBeGreaterThanOrEqual(0);
        expect(pt.sy).toBeLessThanOrEqual(p.height);
      }
    }
  });

  it("a card whose entities sit at opposite far corners still fits its own box", () => {
    const html = edgeCard(
      edgeBox("proc", 0, 0, "near") + edgeBox("store", 760, 880, "far"),
      "0 0 860 960",
    );
    const p = edgeProjectOf(html);
    expect(p.placements.size).toBe(2);
    for (const pl of p.placements.values()) {
      expect(pl.base.sx).toBeGreaterThanOrEqual(0);
      expect(pl.base.sx).toBeLessThanOrEqual(p.width);
      expect(pl.base.sy).toBeGreaterThanOrEqual(0);
      expect(pl.base.sy).toBeLessThanOrEqual(p.height);
    }
  });

  it("nested children are placed too — every id in the graph has a placement", () => {
    const scene = edgeSceneOf(edgeMini);
    const p = projectScene(scene);
    for (const n of edgeAllNodes(scene)) expect(p.placements.has(n.id)).toBe(true);
    expect(p.placements.size).toBe(5);
  });

  it("the offset is global: pairwise placement deltas match pairwise isoProject deltas", () => {
    const scene = edgeSceneOf(edgeMini);
    const p = projectScene(scene);
    const nodes = edgeAllNodes(scene);
    const a = edgeAt(nodes, 0);
    for (const b of nodes.slice(1)) {
      const pa = p.placements.get(a.id)!;
      const pb = p.placements.get(b.id)!;
      const ia = isoProject(a.x, a.y);
      const ib = isoProject(b.x, b.y);
      expect(pa.base.sx - pb.base.sx).toBeCloseTo(ia.sx - ib.sx, 9);
      expect(pa.base.sy - pb.base.sy).toBeCloseTo(ia.sy - ib.sy, 9);
    }
  });

  it("a projected conduit endpoint is the same rigid projection as a placement", () => {
    const scene = edgeSceneOf(edgeStillCard);
    const p = projectScene(scene);
    const node = edgeAt(scene.entities, 0);
    const pl = p.placements.get(node.id)!;
    const pc = edgeAt(p.conduits, 0);
    const iNode = isoProject(node.x, node.y);
    const iStart = isoProject(pc.conduit.x1, pc.conduit.y1);
    expect(pc.start.sx - pl.base.sx).toBeCloseTo(iStart.sx - iNode.sx, 9);
    expect(pc.start.sy - pl.base.sy).toBeCloseTo(iStart.sy - iNode.sy, 9);
  });
});

// ===========================================================================
// 13. Render — motion boundaries (R6/R7/R8)
// ===========================================================================

describe("edge — render motion boundaries (R6/R7/R8)", () => {
  it("reducedMotion true emits no <animate substring at all (still card)", () => {
    const svg = edgeRenderOf(edgeStillCard, true);
    expect(svg).not.toContain("<animate");
    expect(svg).not.toContain("<animateMotion");
  });

  it("reducedMotion true emits no SMIL for a legacy card carrying its own SMIL", () => {
    const svg = edgeRenderOf(edgeMini, true);
    expect(svg).not.toContain("<animate");
    expect(svg).not.toContain("<animateMotion");
  });

  it("reducedMotion true emits no SMIL for click-flow's five bound flows", () => {
    const svg = edgeRenderOf(edgeClickFlow, true);
    expect(svg).not.toContain("<animate");
  });

  it("reducedMotion true still emits every conduit group", () => {
    const scene = edgeSceneOf(edgeStillCard);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: true }));
    expect(edgeConduitGroups(root).length).toBe(scene.conduits.length);
  });

  it("a zero-SMIL card animates every non-dim conduit (R7)", () => {
    const root = edgeRoot(edgeRenderOf(edgeNoDimCard, false));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(3);
    for (const g of groups) expect(edgeNamed(g, "animateMotion").length).toBeGreaterThan(0);
  });

  it("a dim conduit gets no animateMotion", () => {
    const root = edgeRoot(edgeRenderOf(edgeAllDimCard, false));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(2);
    for (const g of groups) expect(edgeNamed(g, "animateMotion").length).toBe(0);
  });

  it("the still card animates exactly its two non-dim conduits", () => {
    const root = edgeRoot(edgeRenderOf(edgeStillCard, false));
    const animated = edgeConduitGroups(root).filter(
      (g) => edgeNamed(g, "animateMotion").length > 0,
    );
    expect(animated.length).toBe(2);
  });

  it("recolor animates exactly its 4 non-dim conduits of 11", () => {
    const root = edgeRoot(edgeRenderOf(edgeRecolor, false));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(11);
    expect(groups.filter((g) => edgeNamed(g, "animateMotion").length > 0).length).toBe(4);
  });

  it("click-flow animates exactly its 18 non-dim conduits of 20", () => {
    const root = edgeRoot(edgeRenderOf(edgeClickFlow, false));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(20);
    expect(groups.filter((g) => edgeNamed(g, "animateMotion").length > 0).length).toBe(18);
  });

  it("linkflow, which has no dim conduit, animates all 23", () => {
    const root = edgeRoot(edgeRenderOf(edgeLinkflow, false));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(23);
    expect(groups.filter((g) => edgeNamed(g, "animateMotion").length > 0).length).toBe(23);
  });

  it("R8: every emitted begin is 0s or negative (still card)", () => {
    const root = edgeRoot(edgeRenderOf(edgeStillCard, false));
    const withBegin = edgeElements(root).filter((e) => e.hasAttribute("begin"));
    expect(withBegin.length).toBeGreaterThan(0);
    for (const e of withBegin) {
      const secs = edgeClockSeconds(e.getAttribute("begin") ?? "");
      expect(Number.isNaN(secs)).toBe(false);
      expect(secs).toBeLessThanOrEqual(0);
    }
  });

  it("R8: every emitted begin is 0s or negative (mini, whose source begin is 0s)", () => {
    const root = edgeRoot(edgeRenderOf(edgeMini, false));
    for (const e of edgeElements(root).filter((x) => x.hasAttribute("begin"))) {
      const secs = edgeClockSeconds(e.getAttribute("begin") ?? "");
      expect(Number.isNaN(secs)).toBe(false);
      expect(secs).toBeLessThanOrEqual(0);
    }
  });

  it("R8: every emitted begin is 0s or negative (click-flow, whose source begins stagger)", () => {
    const root = edgeRoot(edgeRenderOf(edgeClickFlow, false));
    for (const e of edgeElements(root).filter((x) => x.hasAttribute("begin"))) {
      const secs = edgeClockSeconds(e.getAttribute("begin") ?? "");
      expect(Number.isNaN(secs)).toBe(false);
      expect(secs).toBeLessThanOrEqual(0);
    }
  });

  it("R8: every opacity <animate> carries a values list starting at 1", () => {
    for (const card of [edgeStillCard, edgeMini, edgeRecolor]) {
      const root = edgeRoot(edgeRenderOf(card, false));
      for (const a of edgeNamed(root, "animate")) {
        if (a.getAttribute("attributeName") !== "opacity") continue;
        const values = a.getAttribute("values");
        expect(values).not.toBeNull();
        expect(Number((values ?? "").split(";")[0]?.trim())).toBe(1);
      }
    }
  });

  it("a legacy flow binding is emphasis only — it never suppresses a non-dim conduit's motion", () => {
    const scene = edgeSceneOf(edgeMini);
    expect(scene.conduits.filter((c) => c.flow).length).toBe(1);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(2);
    expect(groups.filter((g) => edgeNamed(g, "animateMotion").length > 0).length).toBe(2);
  });
});

// ===========================================================================
// 14. Render — materials, entities and refs (R2/R4/R5)
// ===========================================================================

describe("edge — render materials and entity coverage (R2/R4/R5)", () => {
  it("every EntityNode, nested included, yields exactly one data-entity group", () => {
    const scene = edgeSceneOf(edgeMini);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const nodes = edgeAllNodes(scene);
    expect(root.querySelectorAll("[data-entity]").length).toBe(nodes.length);
    for (const n of nodes) {
      expect(root.querySelectorAll(`[data-entity="${n.id}"]`).length).toBe(1);
    }
  });

  it("every emitted group carries its node's shape", () => {
    const scene = edgeSceneOf(edgeMini);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    for (const n of edgeAllNodes(scene)) {
      expect(edgeEntityGroup(root, n.id).getAttribute("data-shape")).toBe(n.shape);
    }
  });

  it("linkflow's 35 EntityNodes each yield exactly one group", () => {
    const scene = edgeSceneOf(edgeLinkflow);
    const nodes = edgeAllNodes(scene);
    expect(nodes.length).toBe(35);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: true }));
    expect(root.querySelectorAll("[data-entity]").length).toBe(35);
  });

  it("a dim node's group carries an opacity attribute at or below 0.5", () => {
    const scene = edgeSceneOf(edgeMini);
    const dim = edgeAllNodes(scene).find((n) => n.dim);
    expect(dim).toBeDefined();
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const raw = edgeEntityGroup(root, dim!.id).getAttribute("opacity");
    expect(raw).not.toBeNull();
    expect(Number(raw)).toBeLessThanOrEqual(0.5);
    expect(Number.isNaN(Number(raw))).toBe(false);
  });

  it("a non-dim node's group is not forced below 0.5", () => {
    const scene = edgeSceneOf(edgeMini);
    const solid = edgeAllNodes(scene).find((n) => !n.dim && n.shape === "proc");
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const raw = edgeEntityGroup(root, solid!.id).getAttribute("opacity");
    if (raw !== null) expect(Number(raw)).toBeGreaterThan(0.5);
  });

  it("a grey node's group contains stroke-dasharray and carries data-treat grey", () => {
    const scene = edgeSceneOf(edgeMini);
    const grey = edgeAllNodes(scene).find((n) => n.treatment === "grey");
    expect(grey).toBeDefined();
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const g = edgeEntityGroup(root, grey!.id);
    expect(g.outerHTML).toContain("stroke-dasharray");
    expect(g.getAttribute("data-treat")).toBe("grey");
  });

  it("all six treatments contribute data-treat on the node's own group", () => {
    const scene = edgeSceneOf(edgeSixTreatmentCard);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const wanted = ["grey", "red", "yellow", "green", "blue", "purple"];
    const got = scene.entities.map((n) =>
      edgeEntityGroup(root, n.id).getAttribute("data-treat"),
    );
    expect(got).toEqual(wanted);
  });

  it("an untreated node's group carries no data-treat", () => {
    const html = edgeCard(edgeBox("node", 10, 10, "plain") + edgeBox("store", 150, 150, "b"));
    const scene = edgeSceneOf(html);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    for (const n of scene.entities) {
      expect(edgeEntityGroup(root, n.id).getAttribute("data-treat")).toBeNull();
    }
  });

  it("a dim node that is also treated carries both the opacity and the data-treat", () => {
    const html = edgeCard(edgeBox("node blue dim", 10, 10, "both") + edgeBox("store", 150, 150, "b"));
    const scene = edgeSceneOf(html);
    const both = edgeAt(scene.entities, 0);
    expect(both.dim).toBe(true);
    expect(both.treatment).toBe("blue");
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    const g = edgeEntityGroup(root, both.id);
    expect(Number(g.getAttribute("opacity"))).toBeLessThanOrEqual(0.5);
    expect(g.getAttribute("data-treat")).toBe("blue");
  });

  it("R4: every node's first label line reaches the output, nested ones included", () => {
    const scene = edgeSceneOf(edgeMini);
    const svg = renderSceneMarkup(projectScene(scene), { reducedMotion: false });
    const text = edgeRoot(svg).textContent ?? "";
    for (const n of edgeAllNodes(scene)) {
      const first = n.labelLines[0];
      if (first === undefined || first === "") continue;
      expect(text).toContain(first);
    }
  });

  it("R4 holds on a card whose only label carries markup-sensitive characters", () => {
    const html = edgeCard(
      edgeBox("proc", 10, 10, "a &lt; b &amp; c") + edgeBox("store", 150, 150, "b"),
    );
    const scene = edgeSceneOf(html);
    const first = edgeAt(edgeAt(scene.entities, 0).labelLines, 0);
    expect(first).toBe("a < b & c");
    const text = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }))
      .textContent ?? "";
    expect(text).toContain(first);
  });
});

// ===========================================================================
// 15. Render — envelope, theme hook, markers (R1/R9/R10)
// ===========================================================================

describe("edge — render envelope, theme and markers (R1/R9/R10)", () => {
  it("R1: the emitted viewBox is 0 0 width height of the ProjectedScene", () => {
    const p = edgeProjectOf(edgeStillCard);
    const root = edgeRoot(renderSceneMarkup(p, { reducedMotion: false }));
    const nums = (root.getAttribute("viewBox") ?? "").trim().split(/[\s,]+/).map(Number);
    expect(nums).toEqual([0, 0, p.width, p.height]);
  });

  it("R1: the output is one svg element, opened and closed", () => {
    const svg = edgeRenderOf(edgeStillCard, false).trim();
    expect(svg.startsWith("<svg")).toBe(true);
    expect(svg.endsWith("</svg>")).toBe(true);
    expect(svg.split("<svg").length - 1).toBe(1);
  });

  it("R9: paint goes through var(--iso- custom properties", () => {
    expect(edgeRenderOf(edgeStillCard, false)).toContain("var(--iso-");
  });

  it("R9: the output carries no <script", () => {
    for (const card of [edgeStillCard, edgeMini, edgeClickFlow]) {
      expect(edgeRenderOf(card, false)).not.toContain("<script");
    }
  });

  it("R9: the output carries no absolute URL", () => {
    for (const card of [edgeStillCard, edgeMini, edgeRecolor]) {
      const svg = edgeRenderOf(card, false);
      expect(svg).not.toContain("http://");
      expect(svg).not.toContain("https://");
    }
  });

  it("R9 holds under reduced motion too", () => {
    const svg = edgeRenderOf(edgeMini, true);
    expect(svg).toContain("var(--iso-");
    expect(svg).not.toContain("<script");
    expect(svg).not.toContain("http://");
  });

  it("R10: every conduit yields a data-conduit group whose path marker-end resolves in this output's defs", () => {
    for (const card of [edgeStillCard, edgeMini, edgeRecolor]) {
      const scene = edgeSceneOf(card);
      const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
      const groups = edgeConduitGroups(root);
      expect(groups.length).toBe(scene.conduits.length);
      for (const g of groups) {
        const path = edgeNamed(g, "path").find((p) => p.hasAttribute("marker-end"));
        expect(path).toBeDefined();
        const m = /url\(#([^)]+)\)/.exec(path!.getAttribute("marker-end") ?? "");
        expect(m).not.toBeNull();
        const defs = edgeNamed(root, "defs");
        const defined = defs.some((d) =>
          edgeNamed(d, "marker").some((k) => k.getAttribute("id") === edgeAt(m!, 1)),
        );
        expect(defined).toBe(true);
      }
    }
  });

  it("R10 holds under reduced motion (the marker is not a motion element)", () => {
    const scene = edgeSceneOf(edgeStillCard);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: true }));
    const groups = edgeConduitGroups(root);
    expect(groups.length).toBe(3);
    for (const g of groups) {
      const path = edgeNamed(g, "path").find((p) => p.hasAttribute("marker-end"));
      expect(path).toBeDefined();
    }
  });

  it("a card with zero conduits emits zero data-conduit groups", () => {
    const html = edgeCard(edgeBox("proc", 10, 10, "a") + edgeBox("store", 150, 150, "b"));
    const root = edgeRoot(edgeRenderOf(html, false));
    expect(edgeConduitGroups(root).length).toBe(0);
    expect(root.querySelectorAll("[data-entity]").length).toBe(2);
  });
});

// ===========================================================================
// 16. compileCard / cardCompilable (R11/R12)
// ===========================================================================

describe("edge — compileCard and cardCompilable (R11/R12)", () => {
  it("not-diagram passes through unchanged", () => {
    expect(compileCard("<h1>hi</h1>", { reducedMotion: false })).toEqual({
      kind: "not-diagram",
    });
  });

  it("not-diagram passes through under reduced motion too", () => {
    expect(compileCard("", { reducedMotion: true })).toEqual({ kind: "not-diagram" });
  });

  it("a not-parseable hand-built card passes its reason through byte-identically", () => {
    const html = edgeCard(`<polygon class="zork" points="1,1 2,2 3,1"/>`);
    const reason = edgeReasonOf(html);
    expect(compileCard(html, { reducedMotion: false })).toEqual({
      kind: "not-parseable",
      reason,
    });
  });

  it("a scene compile returns exactly kind and svg", () => {
    const r = compileCard(edgeStillCard, { reducedMotion: false });
    expect(r.kind).toBe("scene");
    expect(Object.keys(r).sort()).toEqual(["kind", "svg"]);
  });

  it("compileCard's svg equals the render of the projected read", () => {
    const r = compileCard(edgeStillCard, { reducedMotion: true });
    if (r.kind !== "scene") throw new Error("expected scene");
    expect(r.svg).toBe(edgeRenderOf(edgeStillCard, true));
  });

  it("compileCard honours reducedMotion end to end", () => {
    const r = compileCard(edgeMini, { reducedMotion: true });
    if (r.kind !== "scene") throw new Error("expected scene");
    expect(r.svg).not.toContain("<animate");
  });

  it("cardCompilable is true exactly when readCard says scene", () => {
    const cases: string[] = [
      edgeMini,
      edgeBespoke,
      edgeLinkflow,
      edgeClickFlow,
      edgeRecolor,
      edgeStillCard,
      "<h1>hi</h1>",
      "",
      edgeCard(`<polygon class="zork" points="1,1 2,2 3,1"/>`),
      edgeCard(edgeBox("proc", 10, 10, "x", ` transform="rotate(45)"`)),
      edgeCard(edgeBox("node red blue", 10, 10, "x")),
    ];
    for (const html of cases) {
      expect(cardCompilable(html)).toBe(readCard(html).kind === "scene");
    }
  });

  it("cardCompilable is false for bespoke and plain HTML, true for mini", () => {
    expect(cardCompilable(edgeBespoke)).toBe(false);
    expect(cardCompilable("<h1>hi</h1>")).toBe(false);
    expect(cardCompilable(edgeMini)).toBe(true);
  });

  it("cardCompilable never throws on non-HTML garbage input", () => {
    expect(cardCompilable("not html at all {[<>&")).toBe(false);
  });
});

// ===========================================================================
// 17. JOIN-PIN (post-join, sighted): the amended R10 conduit key, and R7 by
//     SUBSET rather than by count. Added after the join at the lead's request,
//     closing the wrong-subset hole reported in r1 §3 / r2.
//
//     Amended R10: a conduit's emitted group carries
//       data-conduit="{conduit.id}"     when the card gave the edge an id
//       data-conduit="c{index}"         otherwise, index = position in
//                                       scene.conduits
// ===========================================================================

/** The amended R10 key for a conduit at position i in scene.conduits. */
function edgeConduitKey(c: Conduit, i: number): string {
  return c.id ?? `c${i}`;
}

/** The emitted keys of every conduit group, document order. */
function edgeConduitKeysOf(root: Element): string[] {
  return edgeConduitGroups(root).map((g) => g.getAttribute("data-conduit") ?? "");
}

function edgeConduitGroupByKey(root: Element, key: string): Element {
  const found = edgeConduitGroups(root).filter(
    (g) => g.getAttribute("data-conduit") === key,
  );
  if (found.length !== 1) {
    throw new Error(`expected exactly 1 conduit group for "${key}", got ${found.length}`);
  }
  return edgeAt(found, 0);
}

/** The subset of conduit keys whose emitted group carries motion. */
function edgeAnimatedKeys(root: Element): string[] {
  return edgeConduitGroups(root)
    .filter((g) => edgeNamed(g, "animateMotion").length > 0)
    .map((g) => g.getAttribute("data-conduit") ?? "")
    .sort();
}

/** Three conduits, none carrying an id. */
const edgeIdlessConduitCard = edgeCard(
  edgeDefsBlock +
    `<text class="title" x="10" y="20">no edge ids</text>` +
    edgeBox("proc", 10, 10, "a") +
    edgeBox("store", 200, 200, "b") +
    edgePath("edge", "M 20,20 L 100,100") +
    edgePath("edge", "M 30,30 L 110,110") +
    edgePath("edge", "M 40,40 L 120,120"),
  "0 0 400 300",
);

/** Named and unnamed, dim and non-dim, interleaved. */
const edgeMixedKeyCard = edgeCard(
  edgeDefsBlock +
    `<text class="title" x="10" y="20">mixed keys</text>` +
    edgeBox("proc", 10, 10, "a") +
    edgeBox("store", 200, 200, "b") +
    edgePath("edge", "M 20,20 L 100,100", ` id="mix-a"`) +
    edgePath("edge dim", "M 30,30 L 110,110") +
    edgePath("edge", "M 40,40 L 120,120", ` id="mix-c"`) +
    edgePath("dim edge", "M 50,50 L 130,130", ` id="mix-d"`),
  "0 0 400 300",
);

describe("edge — amended R10 conduit key and R7 by subset (join-pin)", () => {
  it("a conduit with an id keys its group on exactly that id (mini's mini-e1)", () => {
    const scene = edgeSceneOf(edgeMini);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    expect(edgeConduitById(scene, "mini-e1").id).toBe("mini-e1");
    expect(edgeConduitGroupByKey(root, "mini-e1").getAttribute("data-conduit")).toBe(
      "mini-e1",
    );
  });

  it("mini's id-less second conduit keys on c1, its position in scene.conduits", () => {
    const scene = edgeSceneOf(edgeMini);
    expect(edgeAt(scene.conduits, 0).id).toBe("mini-e1");
    expect(edgeAt(scene.conduits, 1).id).toBeUndefined();
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    expect(edgeConduitKeysOf(root).slice().sort()).toEqual(["c1", "mini-e1"]);
  });

  it("a card whose edges carry no id at all keys on c0, c1, c2", () => {
    const scene = edgeSceneOf(edgeIdlessConduitCard);
    expect(scene.conduits.map((c) => c.id)).toEqual([undefined, undefined, undefined]);
    const root = edgeRoot(
      renderSceneMarkup(projectScene(scene), { reducedMotion: false }),
    );
    expect(edgeConduitKeysOf(root).slice().sort()).toEqual(["c0", "c1", "c2"]);
  });

  it("the c{index} fallback indexes scene.conduits, not the id-less subsequence", () => {
    const scene = edgeSceneOf(edgeMixedKeyCard);
    expect(scene.conduits.map((c) => c.id)).toEqual([
      "mix-a",
      undefined,
      "mix-c",
      "mix-d",
    ]);
    const root = edgeRoot(
      renderSceneMarkup(projectScene(scene), { reducedMotion: false }),
    );
    expect(edgeConduitKeysOf(root).slice().sort()).toEqual([
      "c1",
      "mix-a",
      "mix-c",
      "mix-d",
    ]);
  });

  it("every fixture's emitted key set is exactly {id ?? c{index}} with no duplicates", () => {
    for (const card of [
      edgeMini,
      edgeStillCard,
      edgeNoDimCard,
      edgeAllDimCard,
      edgeMixedKeyCard,
      edgeIdlessConduitCard,
      edgeRecolor,
      edgeClickFlow,
      edgeLinkflow,
    ]) {
      const scene = edgeSceneOf(card);
      const root = edgeRoot(
        renderSceneMarkup(projectScene(scene), { reducedMotion: false }),
      );
      const want = scene.conduits.map(edgeConduitKey);
      const got = edgeConduitKeysOf(root);
      expect(got.length).toBe(want.length);
      expect(new Set(got).size).toBe(got.length);
      expect(got.slice().sort()).toEqual(want.slice().sort());
    }
  });

  it("R7 by SUBSET: exactly the non-dim ids animate on the still card", () => {
    const root = edgeRoot(edgeRenderOf(edgeStillCard, false));
    expect(edgeAnimatedKeys(root)).toEqual(["still-e1", "still-e3"]);
    expect(edgeNamed(edgeConduitGroupByKey(root, "still-e2"), "animateMotion").length).toBe(
      0,
    );
  });

  it("R7 by SUBSET: exactly the non-dim ids animate on a mixed named/unnamed card", () => {
    const root = edgeRoot(edgeRenderOf(edgeMixedKeyCard, false));
    expect(edgeAnimatedKeys(root)).toEqual(["mix-a", "mix-c"]);
    for (const key of ["c1", "mix-d"]) {
      expect(edgeNamed(edgeConduitGroupByKey(root, key), "animateMotion").length).toBe(0);
    }
  });

  it("R7 by SUBSET: on every fixture, a conduit's group animates iff the conduit is non-dim", () => {
    for (const card of [
      edgeMini,
      edgeStillCard,
      edgeNoDimCard,
      edgeAllDimCard,
      edgeRecolor,
      edgeClickFlow,
      edgeLinkflow,
    ]) {
      const scene = edgeSceneOf(card);
      const root = edgeRoot(
        renderSceneMarkup(projectScene(scene), { reducedMotion: false }),
      );
      const wantAnimated = scene.conduits
        .map(edgeConduitKey)
        .filter((_, i) => !edgeAt(scene.conduits, i).dim)
        .slice()
        .sort();
      expect(edgeAnimatedKeys(root)).toEqual(wantAnimated);
    }
  });

  it("the key survives reduced motion — only the SMIL goes, never the group or its key", () => {
    const scene = edgeSceneOf(edgeMixedKeyCard);
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: true }));
    expect(edgeConduitKeysOf(root).slice().sort()).toEqual([
      "c1",
      "mix-a",
      "mix-c",
      "mix-d",
    ]);
    expect(edgeAnimatedKeys(root)).toEqual([]);
  });

  it("a legacy flow binding does not change the key — mini-e1 keys on its id, not on its flow", () => {
    const scene = edgeSceneOf(edgeMini);
    expect(edgeConduitById(scene, "mini-e1").flow).toEqual({ dur: "3s", begin: "0s" });
    const root = edgeRoot(renderSceneMarkup(projectScene(scene), { reducedMotion: false }));
    expect(edgeAnimatedKeys(root)).toEqual(["c1", "mini-e1"]);
  });
});
