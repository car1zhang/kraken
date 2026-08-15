// project.ts — the isoscene compiler's middle: card coordinates onto the
// isometric floor.
//
// The card's 2D layout IS the scene layout. A card author already solved
// placement — which box sits left of which, what reads as a row, where a lane
// runs — so the projector maps that solved layout and never re-solves it. That
// is why the whole transform is two lines of arithmetic and one global offset:
// anything per-node (a nudge to unstack two boxes, a spread to fill the frame)
// would silently disagree with the flat card it is a view of.

import type {
  EntityNode,
  IsoPoint,
  Placement,
  ProjectedConduit,
  ProjectedScene,
  SceneGraph,
} from "./types.ts";

/** Breathing room on every side of the projected content, in projected units.
 * Purely a framing choice — the rigidity that matters is that ONE offset is
 * applied to every point. */
const MARGIN = 48;

/** Extra headroom above the content. The renderer draws each entity as a prism
 * standing UP from its floor placement, so the topmost placement needs sky
 * above it or the tallest entity clips the viewBox. */
const TOP_HEADROOM = 72;

/**
 * The isometric projection, in full: a 2:1 dimetric mapping of the card plane
 * onto the screen plane, with `z` lifting a point straight up.
 *
 * `sx = x − y` and `sy = (x + y) / 2 − z`. Being affine is what the whole
 * design rests on — collinear card points stay collinear, parallel stays
 * parallel, and the relative order of two boxes along either card axis
 * survives the trip to the floor.
 */
export function isoProject(x: number, y: number, z = 0): IsoPoint {
  return { sx: x - y, sy: (x + y) / 2 - z };
}

/** Depth-first over a node forest, parents before children — the same order
 * `readCard` minted the ids in, so a placement map iterated in insertion order
 * reads like the card. */
function walkNodes(nodes: readonly EntityNode[], visit: (n: EntityNode) => void): void {
  for (const node of nodes) {
    visit(node);
    walkNodes(node.children, visit);
  }
}

interface Bounds {
  minSx: number;
  minSy: number;
  maxSx: number;
  maxSy: number;
  any: boolean;
}

function extend(b: Bounds, p: IsoPoint): void {
  if (!Number.isFinite(p.sx) || !Number.isFinite(p.sy)) return;
  b.any = true;
  if (p.sx < b.minSx) b.minSx = p.sx;
  if (p.sy < b.minSy) b.minSy = p.sy;
  if (p.sx > b.maxSx) b.maxSx = p.sx;
  if (p.sy > b.maxSy) b.maxSy = p.sy;
}

/**
 * Project a whole scene: every EntityNode (nested children included) gets a
 * placement, every conduit gets projected endpoints, and the emitted frame is
 * sized to hold all of it.
 *
 * The offset is computed once from the bounds of everything projected and then
 * added to every point, so for any two nodes the DIFFERENCE of their
 * placements equals the difference of their raw projections exactly. Nothing
 * here consults a node's size, treatment or nesting depth: the projection is
 * rigid by construction rather than by discipline.
 */
export function projectScene(scene: SceneGraph): ProjectedScene {
  const bounds: Bounds = {
    minSx: Infinity,
    minSy: Infinity,
    maxSx: -Infinity,
    maxSy: -Infinity,
    any: false,
  };

  const roots: EntityNode[] = [...scene.entities, ...scene.structures];

  // Bounds are taken over all four corners of every node's card box, not just
  // its origin: the origin is what gets placed, but a wide box drawn from it
  // would otherwise run off the right edge of the frame.
  walkNodes(roots, (n) => {
    extend(bounds, isoProject(n.x, n.y));
    extend(bounds, isoProject(n.x + n.width, n.y));
    extend(bounds, isoProject(n.x, n.y + n.height));
    extend(bounds, isoProject(n.x + n.width, n.y + n.height));
  });
  for (const c of scene.conduits) {
    extend(bounds, isoProject(c.x1, c.y1));
    extend(bounds, isoProject(c.x2, c.y2));
  }
  // Text, steps and badges are the renderer's to draw or drop, but they are
  // card content: leaving them out of the bounds is how a title ends up
  // outside the frame it titles.
  for (const t of scene.texts) extend(bounds, isoProject(t.x, t.y));
  for (const s of scene.steps) extend(bounds, isoProject(s.x, s.y));
  for (const b of scene.badges) extend(bounds, isoProject(b.x, b.y));

  if (!bounds.any) {
    bounds.minSx = 0;
    bounds.minSy = 0;
    bounds.maxSx = 0;
    bounds.maxSy = 0;
  }

  const offsetX = MARGIN - bounds.minSx;
  const offsetY = MARGIN + TOP_HEADROOM - bounds.minSy;
  const width = bounds.maxSx - bounds.minSx + 2 * MARGIN;
  const height = bounds.maxSy - bounds.minSy + 2 * MARGIN + TOP_HEADROOM;

  const place = (x: number, y: number): IsoPoint => {
    const p = isoProject(x, y);
    return { sx: p.sx + offsetX, sy: p.sy + offsetY };
  };

  const placements = new Map<string, Placement>();
  walkNodes(roots, (n) => {
    placements.set(n.id, { nodeId: n.id, base: place(n.x, n.y) });
  });

  const conduits: ProjectedConduit[] = scene.conduits.map((conduit) => ({
    conduit,
    start: place(conduit.x1, conduit.y1),
    end: place(conduit.x2, conduit.y2),
  }));

  return { scene, width, height, offset: { sx: offsetX, sy: offsetY }, placements, conduits };
}
