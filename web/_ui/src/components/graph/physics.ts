// The graph view's physics layer: a d3-force simulation that gives the canvas
// Obsidian's feel — warm, not hot. At rest the simulation idles near sleep;
// grabbing a node pins it to the pointer (fx/fy, so it never lags the cursor)
// and bumps alphaTarget so the neighbourhood wakes; release clears the pin and
// lets everything glide back to equilibrium within about a second.
//
// This module is a pure factory over d3-force with NO sigma or React imports —
// the same split graphModel.ts draws, and for the same reason: everything
// here is testable in jsdom with no canvas. GraphView owns wiring ticks to
// sigma refreshes and pointer events to drag calls.
//
// Three decisions from the spec (57-physics-drag.md, Q12-Q14 answered
// 2026-07-31) shape the API:
//   - EPHEMERAL: nothing here writes anywhere. A reload reseeds from the
//     server layout; kg_layout stays daemon-owned.
//   - WARM START: the caller seeds from server positions, so the simulation
//     settles a delta rather than exploding a random scatter.
//   - SCALE GUARD: the caller checks physicsAllowed() BEFORE constructing;
//     past the cap the simulation is never built at all and the static view
//     stands. The cap lives here so the number and its reason are one place.

import {
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  forceX,
  forceY,
  type Simulation,
  type SimulationLinkDatum,
  type SimulationNodeDatum,
} from "d3-force";

/**
 * Above this many VISIBLE nodes the simulation is never constructed and the
 * view falls back to the static server layout. d3-force is O(n log n) per
 * tick via its internal Barnes-Hut, but the tick runs on the main thread and
 * the binding client is the phone's WKWebView — 3,000 nodes / ~6,000 edges is
 * where the round-1 research put comfortable 60 fps headroom for that. The
 * header toggle can force physics on anyway (carl's explicit choice beats the
 * guard); this constant is only the automatic default.
 */
export const PHYSICS_NODE_CAP = 3000;

/**
 * A press that travels less than this many CSS pixels is a CLICK (select and
 * inspect); at or past it, a DRAG begins and the click is suppressed. The
 * standard disambiguation — without it every drag would also open the
 * inspector on release.
 */
export const DRAG_THRESHOLD_PX = 3;

/**
 * The click-vs-drag decision on a press that has moved (dx, dy) CSS pixels
 * from its downNode point. Squared-distance form so callers never take a
 * square root per mousemove.
 */
export function isDragMovement(dx: number, dy: number): boolean {
  return dx * dx + dy * dy >= DRAG_THRESHOLD_PX * DRAG_THRESHOLD_PX;
}

/** localStorage key for the header toggle: "on" | "off" | absent = auto. */
export const PHYSICS_PREF_KEY = "kraken.graph.physics";

export type PhysicsPref = "on" | "off" | "auto";

export function readPhysicsPref(storage: Pick<Storage, "getItem">): PhysicsPref {
  const v = storage.getItem(PHYSICS_PREF_KEY);
  return v === "on" || v === "off" ? v : "auto";
}

export function writePhysicsPref(
  storage: Pick<Storage, "setItem" | "removeItem">,
  pref: PhysicsPref,
): void {
  // "auto" is stored as absence, so a cleared pref and a never-set pref are
  // the same state — there is no third value to migrate later.
  if (pref === "auto") storage.removeItem(PHYSICS_PREF_KEY);
  else storage.setItem(PHYSICS_PREF_KEY, pref);
}

/**
 * The one on/off decision, in order: an explicit pref wins in both
 * directions; auto defers to the cap; reduced motion downgrades AUTO to off
 * (an explicit "on" still wins — the media query is a default, the toggle is
 * a choice).
 */
export function physicsAllowed(
  visibleNodes: number,
  pref: PhysicsPref,
  reducedMotion: boolean,
): boolean {
  if (pref === "on") return true;
  if (pref === "off") return false;
  if (reducedMotion) return false;
  return visibleNodes <= PHYSICS_NODE_CAP;
}

/** One simulation node: id is the sigma key (the row id as a string). */
export interface PhysicsNode extends SimulationNodeDatum {
  id: string;
}

export interface PhysicsLink extends SimulationLinkDatum<PhysicsNode> {
  occurrences: number;
}

export interface PhysicsInput {
  /** Visible nodes only — filtered-out nodes must not exert charge. */
  nodes: { id: string; x: number; y: number; size: number }[];
  /** Edges between visible nodes; occurrences drives link distance. */
  links: { source: string; target: string; occurrences: number }[];
}

export interface PhysicsHandle {
  /** Advance one frame; returns the live node array to copy positions from. */
  nodes(): PhysicsNode[];
  /** Called by the caller's rAF loop. */
  tick(): void;
  /** True while alpha is above the settle floor — the caller's cue to keep the rAF loop running. */
  awake(): boolean;
  /** Pin `id` to graph coords (x, y) and wake the simulation. */
  drag(id: string, x: number, y: number): void;
  /** Release the pinned node, keeping its velocity — the glide. */
  release(id: string): void;
  /** Wake without a pin (a filter change moved membership). */
  reheat(): void;
  /** Permanently stop; the handle is dead afterwards. */
  stop(): void;
}

// Force tuning. Stated once, with the reasoning, because every one of these
// is a feel decision rather than a correctness one:
//   - link distance shrinks as occurrences grow: corroborated facts pull
//     entities closer, so the map's geometry carries evidence weight.
//   - charge is mild (-60): Obsidian's swaying, not a starburst.
//   - anchoring is weak (0.02) and POSITIONAL (forceX/forceY toward the
//     seed's own centroid), never d3's forceCenter: forceCenter TRANSLATES
//     the whole system every tick to hold the centroid fixed, so dragging
//     one node shoved every unlinked node too (caught by this module's own
//     follow test — an unlinked node moved 3x more than a linked one), and
//     centring on (0,0) would drift a map whose server-layout centroid is
//     elsewhere. Per-node springs toward where the map already was keep
//     components from wandering off-canvas and move nothing else.
//   - collide at node size + 2 stops label-on-label pileups at rest.
//   - alphaMin 0.005 / alphaDecay default: at rest the simulation SLEEPS
//     (awake() false, rAF loop stops, zero CPU on an idle tab) — the "warm"
//     half of warm-not-hot is alphaTarget on interaction, not a hot loop.
const chargeStrength = -60;
const centerStrength = 0.02;
const linkBaseDistance = 70;
const alphaSettleFloor = 0.005;
/** alphaTarget while a node is pinned — the neighbourhood-wakes level. */
const dragAlphaTarget = 0.3;
/** A gentler wake for membership changes, enough to absorb re-inserted nodes. */
const reheatAlpha = 0.2;

export function createPhysics(input: PhysicsInput): PhysicsHandle {
  const nodes: PhysicsNode[] = input.nodes.map((n) => ({
    id: n.id,
    x: n.x,
    y: n.y,
  }));
  const sizes = new Map(input.nodes.map((n) => [n.id, n.size]));
  // The seed's centroid is the anchor point — see the force-tuning comment.
  const seedCX = input.nodes.length
    ? input.nodes.reduce((s, n) => s + n.x, 0) / input.nodes.length
    : 0;
  const seedCY = input.nodes.length
    ? input.nodes.reduce((s, n) => s + n.y, 0) / input.nodes.length
    : 0;
  const links: PhysicsLink[] = input.links.map((l) => ({
    source: l.source,
    target: l.target,
    occurrences: l.occurrences,
  }));

  const sim: Simulation<PhysicsNode, PhysicsLink> = forceSimulation(nodes)
    .force(
      "link",
      forceLink<PhysicsNode, PhysicsLink>(links)
        .id((n) => n.id)
        // 70 at 1 sighting → ~44 at 5 → ~35 at 10: corroboration pulls
        // closer, with a floor so heavily-corroborated pairs don't overlap.
        .distance((l) => Math.max(30, linkBaseDistance / Math.sqrt(Math.max(1, l.occurrences)))),
    )
    .force("charge", forceManyBody().strength(chargeStrength))
    .force("anchorX", forceX(seedCX).strength(centerStrength))
    .force("anchorY", forceY(seedCY).strength(centerStrength))
    .force(
      "collide",
      forceCollide<PhysicsNode>((n) => (sizes.get(n.id) ?? 4) + 2),
    )
    // The caller drives ticks from its own rAF so sigma refreshes exactly
    // once per drawn frame; d3's internal timer would tick unwatched.
    .stop();

  // Seeded from server positions, the map is already near equilibrium — a
  // small starting alpha settles the delta without visibly convulsing the
  // whole graph on first open (the exploding-hairball first paint the warm
  // start exists to prevent).
  sim.alpha(0.1);

  const byID = new Map(nodes.map((n) => [n.id, n]));

  return {
    nodes: () => nodes,
    tick: () => {
      sim.tick();
    },
    awake: () => sim.alpha() > alphaSettleFloor || sim.alphaTarget() > 0,
    drag: (id, x, y) => {
      const n = byID.get(id);
      if (!n) return;
      n.fx = x;
      n.fy = y;
      // Restarting with a raised target is the wake: alpha climbs toward
      // 0.3 while the pin holds, so neighbours follow with visible life.
      if (sim.alphaTarget() !== dragAlphaTarget) {
        sim.alphaTarget(dragAlphaTarget).restart();
        sim.stop(); // keep d3's own timer off; ticks stay ours
      }
    },
    release: (id) => {
      const n = byID.get(id);
      if (n) {
        // Clearing fx/fy (not zeroing vx/vy) is the glide: the node keeps
        // the velocity the drag gave it and decelerates naturally.
        n.fx = null;
        n.fy = null;
      }
      sim.alphaTarget(0);
    },
    reheat: () => {
      if (sim.alpha() < reheatAlpha) sim.alpha(reheatAlpha);
    },
    stop: () => {
      sim.stop();
    },
  };
}
