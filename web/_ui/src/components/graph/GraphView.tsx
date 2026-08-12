// The GRAPH view — the knowledge graph as a fourth board view beside
// board / log / dream. Renders 40-ui-graph-view.html: a left filter rail, the
// canvas, and a right inspector.
//
// Read-only in v1 (50-components.md #8): nothing here mutates the graph, and a
// wrong fact is corrected by a `/graph` revision job rather than by clicking.
//
// sigma is driven DIRECTLY rather than through @react-sigma/core. The wrapper
// would have saved roughly the sixty lines of effect wiring below, at the cost
// of a peer dependency pinned to `sigma@^3.0.2` — a second package with an
// opinion about which sigma this board is allowed to be on, for a view that
// creates exactly one instance and never unmounts it.
//
// Layout is a COLLABORATION between the server and a client simulation
// (Q14, answered 2026-07-31): the server's kg_layout positions are the WARM
// START — the map opens in its remembered shape, and both clients open on
// the same picture — and a d3-force simulation (physics.ts) then settles the
// delta and gives nodes Obsidian-style dragging. Drags are ephemeral (Q12):
// nothing writes back, and a reload reseeds from the server layout. Above
// PHYSICS_NODE_CAP visible nodes the simulation never constructs and this
// view falls back to the static server positions (Q13). The iOS app draws
// its own native Canvas over the same server layout (ios/Kraken/GraphView.swift)
// and carries its own physics. See 57-physics-drag.md.

import { MultiDirectedGraph } from "graphology";
import { useEffect, useMemo, useRef, useState } from "react";
import Sigma from "sigma";

import { useGraph, useGraphNode, type GraphEdge } from "../../hooks/useGraph.ts";
import {
  colorOf,
  DEFAULT_FILTERS,
  edgeVisible,
  hiddenNodeIds,
  invalidatedAt,
  isInvalidated,
  LABEL_VAR,
  makeEdgeReducer,
  makeNodeReducer,
  nodeColor,
  nodeKey,
  nodeSize,
  nodeTypes,
  readGraphPalette,
  shortDate,
  withFallbackPositions,
  type EdgeAttrs,
  type GraphFilters,
  type NodeAttrs,
  type Palette,
} from "./graphModel.ts";
import {
  createPhysics,
  isDragMovement,
  physicsAllowed,
  readPhysicsPref,
  writePhysicsPref,
  PHYSICS_NODE_CAP,
  type PhysicsHandle,
  type PhysicsPref,
} from "./physics.ts";

/** The slider's ceiling. A fact asserted by ten separate episodes is settled. */
const MAX_SIGHTINGS = 10;

export interface GraphViewProps {
  /**
   * Whether GRAPH is the visible view. Gates the poll and drives `hidden`.
   *
   * Keep this component MOUNTED across view switches, exactly as LogView and
   * DreamView are: the filter rail, the selection and — more expensively — the
   * sigma instance with its WebGL context and its camera position are all
   * component state, and unmounting would rebuild the context and throw away
   * where carl had panned to on every tab switch.
   */
  active: boolean;
}

/**
 * Re-render when the theme or the accent palette flips.
 *
 * Node and edge colours are resolved from style.css custom properties (see
 * readGraphPalette), and those change value under `data-theme="light"` AND
 * under `data-accent` (the ◈ palette cycle re-derives every accent mix). Both
 * attributes are written by index.html's pre-paint IIFEs (the theme one also
 * owns the `prefers-color-scheme` listener), so observing them catches the
 * ◐/◈ buttons and a system switch alike, and adds no second writer.
 */
function useThemeTick(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (typeof MutationObserver !== "function") return;
    const obs = new MutationObserver(() => setTick((n) => n + 1));
    obs.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme", "data-accent"],
    });
    return () => obs.disconnect();
  }, []);
  return tick;
}

function TypeRow({
  type,
  on,
  color,
  onToggle,
}: {
  type: string;
  on: boolean;
  color: string;
  onToggle(type: string): void;
}) {
  return (
    <button
      className={"graph-chk" + (on ? "" : " off")}
      aria-pressed={on}
      onClick={() => onToggle(type)}
    >
      <span
        className="graph-dot"
        style={{ background: on ? color : "var(--hairline-2)" }}
      />
      {type}
    </button>
  );
}

function Facts({
  facts,
  names,
}: {
  facts: GraphEdge[];
  names: Map<number, string>;
}) {
  if (!facts.length) {
    return <div className="graph-empty-facts">no facts recorded</div>;
  }
  return (
    <>
      {facts.map((e) => {
        const dead = isInvalidated(e);
        // `fact` is the sentence the extraction agent wrote — the edge's short
        // description IS its name. Predicate plus the other endpoint is what a
        // row degrades to when that sentence is blank, so a half-filled edge
        // still reads as something rather than as an empty line.
        const text =
          e.fact || `${e.predicate} ${names.get(e.dst) ?? `#${e.dst}`}`.trim();
        return (
          <div key={e.id} className={"graph-fact" + (dead ? " stale" : "")}>
            {text}
            <span className="graph-src">
              E{e.episode} ·{" "}
              {dead
                ? `invalidated ${shortDate(invalidatedAt(e))}`
                : `strength ${e.strength} · seen ${e.occurrences}×`}
            </span>
          </div>
        );
      })}
    </>
  );
}

export function GraphView({ active }: GraphViewProps) {
  const { nodes: polledNodes, edges: polledEdges, stats, error, loaded } = useGraph(active);

  // Identity-STABLE node/edge arrays. usePoll hands back freshly-parsed
  // arrays every 30 s even when the payload is byte-identical — which for a
  // nightly-written graph is the normal case — and letting those identities
  // flow into the memos below re-cleared the graph to server positions and
  // rebuilt the simulation twice a minute, forever: the map visibly crept
  // ~11 px per tick and never truly slept, and a poll landing mid-drag
  // unpinned the held node (review findings, measured). The signature is
  // structural: it covers exactly the fields the graph build and the
  // simulation read, so an unchanged payload keeps the PREVIOUS arrays and
  // nothing downstream stirs.
  // The signatures carry every field ANY reader of the memoized arrays
  // consumes — the graph build, the simulation, AND the inspector (fact,
  // strength, description…) — plus the array length, which closes the
  // delimiter-collision hole a name containing "|"+":" could open (review
  // findings, r2). The time term exists only when some edge carries a
  // FUTURE valid_until: isInvalidated is clock-dependent for those, and a
  // time-invariant signature kept an edge drawing as current after its
  // window closed while the header (computed per render) said otherwise —
  // a 5-minute bucket re-keys the arrays just often enough to retire it.
  const nodesSig = useMemo(
    () =>
      polledNodes.length +
      "#" +
      polledNodes
        .map((n) => `${n.id}:${n.type}:${n.name}:${n.x}:${n.y}:${n.description}`)
        .join("|"),
    [polledNodes],
  );
  const edgesSig = useMemo(() => {
    const horizon = polledEdges.some((e) => e.valid_until > Date.now())
      ? `~${Math.floor(Date.now() / 300_000)}`
      : "";
    return (
      polledEdges.length +
      horizon +
      "#" +
      polledEdges
        .map(
          (e) =>
            `${e.id}:${e.src}:${e.dst}:${e.occurrences}:${e.valid_until}:${e.expired_at}:${e.strength}:${e.predicate}:${e.fact}:${e.episode}`,
        )
        .join("|")
    );
  }, [polledEdges]);
  // Deliberately keyed on the SIGNATURE, not the polled array (the repo has
  // no eslint, so no suppression to carry): same signature must return the
  // same identity, which is the entire mechanism.
  const nodes = useMemo(() => polledNodes, [nodesSig]);
  const edges = useMemo(() => polledEdges, [edgesSig]);
  const themeTick = useThemeTick();

  const [filters, setFilters] = useState<GraphFilters>(DEFAULT_FILTERS);
  // The sigma node KEY, i.e. the id as a string — what clickNode hands back.
  const [selected, setSelected] = useState<string | null>(null);
  const selectedID = selected === null ? null : Number(selected);
  // Aliases are not on the board payload at all, so the panel asks for them.
  const detail = useGraphNode(selectedID);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const sigmaRef = useRef<Sigma<NodeAttrs, EdgeAttrs> | null>(null);
  const graphRef = useRef<MultiDirectedGraph<NodeAttrs, EdgeAttrs> | null>(null);
  if (graphRef.current === null) {
    // Multi + directed: two entities can hold several distinct facts about
    // each other, and each fact has a direction (src asserted about dst).
    graphRef.current = new MultiDirectedGraph<NodeAttrs, EdgeAttrs>();
  }
  const [ready, setReady] = useState(false);
  const [renderError, setRenderError] = useState<string | null>(null);

  // ------------------------------------------------------------ the physics
  // Q12-Q14 (answered 2026-07-31): ephemeral, warm-started from the server
  // layout, auto-off past PHYSICS_NODE_CAP with the toggle winning both ways.
  const [physicsPref, setPhysicsPref] = useState<PhysicsPref>(() =>
    readPhysicsPref(window.localStorage),
  );
  // Live, not a mount-time snapshot: the board is a session-length SPA and
  // a mid-session System Settings change would otherwise not land until a
  // reload (review finding). matchMedia absent (older WebKit) degrades to
  // false via the optional chain.
  const [reducedMotion, setReducedMotion] = useState(
    () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false,
  );
  useEffect(() => {
    const mql = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    if (!mql) return;
    const onChange = () => setReducedMotion(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);
  const physicsRef = useRef<PhysicsHandle | null>(null);
  const rafRef = useRef<number>(0);
  // Re-arms the (stopped-when-asleep) rAF loop; set by the physics effect,
  // called by the drag handlers when they wake the simulation.
  const wakeLoopRef = useRef<(() => void) | null>(null);
  // The current simulation's membership string — the guard that keeps a
  // same-membership effect re-run from rebuilding a settled simulation.
  const membershipRef = useRef<string>("");
  // Whether a frame is scheduled. A REF, not a per-effect local: the
  // membership-guard bail keeps the previous run's loop closure alive after
  // cleanup has cancelled its frame, and a local flag stranded there would
  // read "armed" forever while nothing was scheduled — wake would no-op and
  // the kept simulation would freeze mid-settle.
  const rafLiveRef = useRef(false);
  // `active`, ref-borne for the same retained-closure reason: the loop
  // closure survives effect re-runs via the membership bail, so every value
  // it reads must come through a ref or it is stale by construction. A
  // handle built while the view was hidden (an in-flight poll resolving
  // after a tab switch) captured active === false and froze the simulation
  // AND dragging for the whole session (review finding, r3 — the effect's
  // dep re-ran on the change but bailed without rebuilding the closure).
  const activeRef = useRef(active);
  activeRef.current = active;
  // The press being tracked: sigma key + screen coords at downNode, and
  // whether it has crossed DRAG_THRESHOLD_PX into a real drag.
  const pressRef = useRef<{ key: string; sx: number; sy: number; dragging: boolean } | null>(null);
  // One-shot latch: set when a mouseup ends a real drag, consumed by the next
  // clickNode so a completed drag never doubles as a selection.
  const dragLatchRef = useRef(false);

  // Keyed by the sigma KEY (the id as a string), so a reducer handed a key can
  // look its element up without converting.
  const byKey = useMemo(
    () => new Map(nodes.map((n) => [nodeKey(n.id), n])),
    [nodes],
  );
  const edgeByKey = useMemo(
    () => new Map(edges.map((e) => [nodeKey(e.id), e])),
    [edges],
  );
  const names = useMemo(() => new Map(nodes.map((n) => [n.id, n.name])), [nodes]);

  // Degree over EVERY edge, not the filtered set: node size is a property of
  // the entity, and letting the sightings slider resize the whole canvas would
  // make the picture unrecognizable at each notch — the opposite of a graph
  // you navigate by remembered shape.
  const degrees = useMemo(() => {
    const d = new Map<number, number>();
    for (const e of edges) {
      d.set(e.src, (d.get(e.src) ?? 0) + 1);
      d.set(e.dst, (d.get(e.dst) ?? 0) + 1);
    }
    return d;
  }, [edges]);

  const positions = useMemo(() => withFallbackPositions(nodes), [nodes]);
  const types = useMemo(() => nodeTypes(nodes), [nodes]);
  const hidden = useMemo(() => hiddenNodeIds(nodes, filters), [nodes, filters]);

  const palette: Palette = useMemo(
    // themeTick is the dependency that matters: the container element itself
    // never changes, only what its custom properties resolve to.
    () => readGraphPalette(containerRef.current),
    [themeTick, ready],
  );

  // ------------------------------------------------------------- the graph
  useEffect(() => {
    const g = graphRef.current;
    if (!g) return;
    // A REAL data change (the signature memos above gate identity) rebuilds
    // the graph — but existing nodes keep their CURRENT positions, which by
    // now carry the simulation's settle and any drags. Snapping them back to
    // server coordinates on every ingest tick would undo the arrangement the
    // reader just made; only genuinely new nodes enter at the server layout.
    const keep = new Map<string, { x: number; y: number }>();
    g.forEachNode((key, attrs) => keep.set(key, { x: attrs.x, y: attrs.y }));
    g.clear();
    for (const p of positions) {
      const n = byKey.get(nodeKey(p.id));
      if (!n) continue;
      const degree = degrees.get(n.id) ?? 0;
      const prev = keep.get(nodeKey(n.id));
      // merge rather than add: a duplicate id in the payload is a server bug,
      // not a reason for the whole view to throw.
      g.mergeNode(nodeKey(n.id), {
        x: prev?.x ?? p.x,
        y: prev?.y ?? p.y,
        size: nodeSize(degree),
        label: n.name,
        kind: n.type,
        degree,
      });
    }
    for (const e of edges) {
      // An edge to a node the payload did not carry cannot be drawn — sigma
      // needs two positions — and graphology throws rather than skipping it.
      if (!g.hasNode(nodeKey(e.src)) || !g.hasNode(nodeKey(e.dst))) continue;
      g.mergeEdgeWithKey(nodeKey(e.id), nodeKey(e.src), nodeKey(e.dst), {
        size: 1,
        occurrences: e.occurrences ?? 0,
        invalidated: isInvalidated(e),
      });
    }
    sigmaRef.current?.refresh();
  }, [positions, byKey, edges, degrees]);

  // ------------------------------------------------------------ the canvas
  useEffect(() => {
    if (!active) return;
    const existing = sigmaRef.current;
    if (existing) {
      // The container had zero size while `hidden`, so sigma's cached
      // dimensions are stale the moment it comes back.
      existing.resize();
      existing.refresh();
      return;
    }
    const el = containerRef.current;
    const g = graphRef.current;
    if (!el || !g) return;
    try {
      const s = new Sigma<NodeAttrs, EdgeAttrs>(g, el, {
        renderEdgeLabels: false,
        enableEdgeEvents: false,
        // The view stays MOUNTED while hidden (App.tsx), and the theme/accent
        // MutationObserver above refreshes sigma's settings while the
        // container is display:none — width 0, on which sigma's resize()
        // THROWS (surfacing as an uncaught error from the rAF'd render, which
        // also aborts that frame). This is sigma's own switch for exactly
        // that hidden-container case. Recovery on show is NOT sigma's doing —
        // this sigma has no ResizeObserver, only a window resize listener —
        // it is this same effect's early-return branch ABOVE calling
        // resize()+refresh() on the `active` flip, which this flag makes
        // load-bearing. The trade: a genuinely zero-sized container while
        // VISIBLE now renders blank with no message.
        allowInvalidContainer: true,
        // Label level-of-detail is sigma's own: it draws a label only once the
        // node is rendered at least this many pixels across, so zooming out of
        // a 5k-node graph thins the labels out instead of stacking them.
        labelRenderedSizeThreshold: 8,
        labelDensity: 0.6,
        labelFont: 'Avenir Next, Helvetica Neue, -apple-system, sans-serif',
        labelSize: 11,
        labelColor: { color: colorOf(LABEL_VAR, readGraphPalette(el)) },
        zIndex: true,
        minCameraRatio: 0.05,
        maxCameraRatio: 20,
      });
      s.on("clickNode", ({ node }) => {
        // sigma fires clickNode after mouseup, so a completed drag would
        // ALSO select — the latch (set by the drag effect's mouseup) makes a
        // drag and a click mutually exclusive, which is the 3px threshold's
        // whole point.
        if (dragLatchRef.current) {
          dragLatchRef.current = false;
          return;
        }
        setSelected(node);
      });
      s.on("clickStage", () => setSelected(null));
      sigmaRef.current = s;
      setReady(true);
    } catch (err) {
      // No WebGL context (a headless browser, a GPU-blocklisted machine) is
      // the one failure that leaves the rest of this view perfectly usable —
      // the rail, the stats and the inspector are plain DOM. Say so in place
      // of the canvas rather than taking the page down.
      setRenderError(err instanceof Error ? err.message : String(err));
    }
  }, [active]);

  // Kill on unmount only. App keeps this view mounted for the whole session,
  // so in practice this runs when the page goes away — but a leaked WebGL
  // context is exactly the kind of thing that is invisible until it is not.
  useEffect(
    () => () => {
      sigmaRef.current?.kill();
      sigmaRef.current = null;
    },
    [],
  );

  // ----------------------------------------------------------- the filters
  useEffect(() => {
    const s = sigmaRef.current;
    if (!s) return;
    s.setSettings({
      nodeReducer: makeNodeReducer(hidden, selected, palette),
      edgeReducer: makeEdgeReducer(filters, hidden, edgeByKey, palette),
      labelColor: { color: colorOf(LABEL_VAR, palette) },
    });
  }, [ready, hidden, selected, filters, edgeByKey, palette]);

  // Filtered-out nodes leave the simulation ENTIRELY — a hidden node still
  // exerting charge would push visible nodes around an invisible obstacle.
  const visibleCount = nodes.length - hidden.size;
  const physicsOn =
    ready && renderError === null && physicsAllowed(visibleCount, physicsPref, reducedMotion);

  // (Re)build the simulation when membership changes; tear down when off.
  // Seeding: the CURRENT graphology positions, not `positions` — mid-session
  // those carry any previous simulation's settle, so a filter toggle
  // re-inserts a node where it last was rather than snapping it back to the
  // server layout (the spec's re-insert-at-last-position obligation).
  useEffect(() => {
    const g = graphRef.current;
    if (!physicsOn || !g) {
      physicsRef.current?.stop();
      physicsRef.current = null;
      membershipRef.current = "";
      return;
    }
    const simNodes: { id: string; x: number; y: number; size: number }[] = [];
    g.forEachNode((key, attrs) => {
      if (hidden.has(key)) return;
      simNodes.push({ id: key, x: attrs.x, y: attrs.y, size: attrs.size });
    });
    // Membership honours EVERYTHING the canvas hides, not just node types:
    // an invalidated fact under current-only (the shipped default) or an
    // edge under the sightings floor is invisible, and an invisible spring
    // hauling two visible nodes together is the same defect the spec calls
    // out for hidden nodes' charge (review finding). edgeVisible is the
    // exact predicate the edge reducer draws with.
    const simLinks: { source: string; target: string; occurrences: number }[] = [];
    g.forEachEdge((key, attrs, src, dst) => {
      if (hidden.has(src) || hidden.has(dst)) return;
      const wire = edgeByKey.get(key);
      if (!wire || !edgeVisible(wire, filters)) return;
      simLinks.push({ source: src, target: dst, occurrences: attrs.occurrences });
    });
    // All-filtered-out builds nothing: g.order alone cannot see it, and an
    // empty simulation still spun the rAF loop for two seconds of no-op
    // refreshes (review finding).
    if (simNodes.length === 0) {
      membershipRef.current = "";
      return;
    }
    // MEMBERSHIP guard, the Swift structure ported (GraphViewModel's
    // syncPhysics early-return): the deps above include `filters`, and a
    // redundant rail click — selecting "current only" when it already is —
    // produces a fresh filters object, a fresh effect run, and a rebuild
    // that visibly shuffled a settled map by up to 95 px (review finding,
    // r2; the faster alphaDecay made "settled" further from equilibrium, so
    // rebuilds got MORE disruptive as release-settle got better). Same
    // members, same links → same simulation, untouched.
    const membership =
      simNodes.map((n) => n.id).join(",") +
      ";" +
      simLinks.map((l) => `${l.source}>${l.target}@${l.occurrences}`).join(",");
    if (membership === membershipRef.current && physicsRef.current) {
      // Same members: keep the simulation AND re-arm its loop — cleanup
      // cancelled the frame before this re-run, and the kept closure cannot
      // know that. The loop stops itself next frame if nothing is moving.
      rafLiveRef.current = false;
      wakeLoopRef.current?.();
      return;
    }
    membershipRef.current = membership;
    physicsRef.current?.stop();
    const handle = createPhysics({ nodes: simNodes, links: simLinks });
    physicsRef.current = handle;
    // The rAF loop STOPS when there is nothing to animate — it does not
    // reschedule and merely skip the body, which kept a 60 Hz callback live
    // for the life of the page (review finding). wakeLoop re-arms it; the
    // drag handlers and the rebuild below are the only wake sources, which
    // is exactly the warm-not-hot lifecycle physics.ts documents.
    const loop = () => {
      const h = physicsRef.current;
      if (h !== handle) return; // superseded by a rebuild
      if (!activeRef.current || (!h.awake() && !pressRef.current?.dragging)) {
        rafLiveRef.current = false;
        return;
      }
      h.tick();
      // One batched attribute pass, and NO explicit refresh: graphology
      // emits a single eachNodeAttributesUpdated for the batch, and sigma's
      // own handler for that event processes the nodes and calls
      // refresh({schedule:true}) — one debounced render on the next frame
      // (sigma.esm.js:1698-1706, read this session). The naive shape (two
      // setNodeAttribute calls per node, then a bare refresh()) emitted 2N
      // per-node events plus a full synchronous re-index per frame — the
      // cost that actually bounded the node cap (review finding). An
      // explicit refresh({skipIndexation:true}) here would be a no-op
      // dressed as an optimisation: without partialGraph the flag is never
      // read and the call is a second full refresh (review finding, r2).
      const moved = new Map(h.nodes().map((n) => [n.id, n]));
      g.updateEachNodeAttributes((key, attrs) => {
        const n = moved.get(key);
        if (!n) return attrs;
        return { ...attrs, x: n.x ?? attrs.x, y: n.y ?? attrs.y };
      });
      rafRef.current = requestAnimationFrame(loop);
    };
    const wakeLoop = () => {
      if (rafLiveRef.current) return;
      rafLiveRef.current = true;
      rafRef.current = requestAnimationFrame(loop);
    };
    wakeLoopRef.current = wakeLoop;
    wakeLoop(); // a fresh build is awake (settling the seed delta)
    return () => {
      // Cleanup cancels the FRAME, never the handle: this runs before every
      // re-run of the effect, and most re-runs bail on the membership guard
      // and keep the simulation — the handle is stopped only when a real
      // membership change (above) or unmount (below) replaces it.
      cancelAnimationFrame(rafRef.current);
      rafLiveRef.current = false;
    };
  }, [physicsOn, hidden, active, positions, edges, filters, edgeByKey]);

  // The handle's real teardown: unmount only.
  useEffect(
    () => () => {
      physicsRef.current?.stop();
      physicsRef.current = null;
    },
    [],
  );

  // Drag wiring. Gated on `ready` alone — NOT on physicsOn: the spec's
  // reduced-motion row requires drag to keep working with the simulation
  // off (it moves the one node, no cascade), and gating the listeners on
  // physicsOn removed dragging exactly for the audience that setting exists
  // for (review finding). With a live simulation the drag pins through it;
  // without one the handlers write the node's position directly.
  //
  // Wired to BOTH captors: mousemovebody/mouseup are MouseCaptor-only
  // emissions, and touch devices browsing the board (iPad, phone Safari)
  // otherwise got downNode and then nothing — pressRef armed forever and no
  // drag at all (review finding). The touch payloads carry touches[0] in
  // the same client-coordinate space.
  useEffect(() => {
    const s = sigmaRef.current;
    if (!s) return;
    const g = graphRef.current;
    const dragTo = (key: string, sx: number, sy: number) => {
      const graphCoords = s.viewportToGraph({ x: sx, y: sy });
      const h = physicsRef.current;
      if (h) {
        h.drag(key, graphCoords.x, graphCoords.y);
        wakeLoopRef.current?.();
      } else if (g?.hasNode(key)) {
        // Physics off (reduced motion / over-cap / no sim built): the drag
        // still moves this one node — no forces, no neighbours. No explicit
        // refresh: setNodeAttribute's own event makes sigma schedule one.
        g.setNodeAttribute(key, "x", graphCoords.x);
        g.setNodeAttribute(key, "y", graphCoords.y);
      }
    };
    const down = ({ node, event }: { node: string; event: { x: number; y: number; original: Event } }) => {
      // Left button only: sigma emits downNode for every button, but its
      // captor never sets isMouseDown for a non-left press, so no mouseup
      // would ever clear a press armed here — a right-click then any mouse
      // movement dragged the node with no button held (review finding).
      const orig = event.original;
      if (orig instanceof MouseEvent && orig.button !== 0) return;
      // A fresh press invalidates any stale latch: a drag that ended
      // outside the canvas never got the click that consumes it, and the
      // leftover latch swallowed the NEXT legitimate selection (review
      // finding).
      dragLatchRef.current = false;
      pressRef.current = { key: node, sx: event.x, sy: event.y, dragging: false };
    };
    const move = ({ x, y, preventSigmaDefault, original }: { x: number; y: number; preventSigmaDefault: () => void; original: Event }) => {
      const press = pressRef.current;
      if (!press) return;
      // A move with no button down means the mouseup was lost (released
      // outside the window) — disarm rather than drag a node under a bare
      // cursor.
      if (original instanceof MouseEvent && original.buttons === 0) {
        if (press.dragging) physicsRef.current?.release(press.key);
        pressRef.current = null;
        return;
      }
      // Suppressed from the first armed move, not from the threshold: the
      // camera panned for the first <3 px of every drag, and those
      // sub-threshold moves also counted toward sigma's own
      // draggedEventsTolerance, which then ate the CLICK a slow press was
      // (review findings). While a press is armed, the camera never moves.
      preventSigmaDefault();
      if (original.cancelable) original.preventDefault();
      if (!press.dragging) {
        if (!isDragMovement(x - press.sx, y - press.sy)) return;
        press.dragging = true;
      }
      dragTo(press.key, x, y);
    };
    const up = () => {
      const press = pressRef.current;
      if (!press) return;
      if (press.dragging) {
        physicsRef.current?.release(press.key);
        dragLatchRef.current = true;
      }
      pressRef.current = null;
    };
    const mouse = s.getMouseCaptor();
    const touch = s.getTouchCaptor();
    // Touch payloads: touchmove carries `touches` (already viewport-relative
    // per sigma's getTouchCoords); a one-finger move maps onto the same
    // handler, two-finger stays the captor's own pinch.
    const touchMove = (e: {
      touches: { x: number; y: number }[];
      original: TouchEvent;
      preventSigmaDefault: () => void;
    }) => {
      if (!pressRef.current || e.touches.length !== 1) return;
      const t0 = e.touches[0];
      if (!t0) return;
      const press = pressRef.current;
      // BOTH suppressions, from the first armed move: preventDefault stops
      // the page scroll, and preventSigmaDefault stops TouchCaptor's own
      // one-finger camera pan — without it the map slid under the dragged
      // node at finger speed for the whole gesture (review finding, r2:
      // the mouse path had this fix and the touch path did not).
      e.preventSigmaDefault();
      if (e.original.cancelable) e.original.preventDefault();
      if (!press.dragging && !isDragMovement(t0.x - press.sx, t0.y - press.sy)) return;
      press.dragging = true;
      dragTo(press.key, t0.x, t0.y);
    };
    s.on("downNode", down);
    mouse.on("mousemovebody", move);
    mouse.on("mouseup", up);
    touch.on("touchmove", touchMove);
    touch.on("touchup", up);
    return () => {
      s.off("downNode", down);
      mouse.off("mousemovebody", move);
      mouse.off("mouseup", up);
      touch.off("touchmove", touchMove);
      touch.off("touchup", up);
      // A teardown mid-drag (physics flipping, unmount) must not leave the
      // press armed for the next incarnation of the listeners.
      pressRef.current = null;
    };
  }, [ready]);

  // --------------------------------------------------------------- derived
  // The detail read wins where it has an answer (it serves the node's edge
  // list pre-cut server-side), and the board payload — which carries aliases
  // too, plus every edge globally — is the fallback while it is in flight.
  const sel = detail?.node ?? (selected ? byKey.get(selected) : undefined);
  // The inspector is deliberately UNFILTERED — the mockup shows an invalidated
  // fact on a node while the rail sits on "current only". The rail says what
  // the canvas draws; the panel says everything known about the one node carl
  // asked about, which is the whole reason to click it.
  const selFacts = useMemo(() => {
    if (selectedID === null) return [];
    // The detail route returns EVERY edge touching the node, which is what to
    // show; the board payload's own edges are the fallback until it lands.
    const own = (
      detail?.edges ??
      edges.filter((e) => e.src === selectedID || e.dst === selectedID)
    ).slice();
    return own.sort((a, b) => {
      const ai = isInvalidated(a) ? 1 : 0;
      const bi = isInvalidated(b) ? 1 : 0;
      if (ai !== bi) return ai - bi;
      return (b.strength ?? 0) - (a.strength ?? 0);
    });
  }, [edges, selectedID, detail]);

  const zoom = (fn: "animatedZoom" | "animatedUnzoom" | "animatedReset") => () =>
    void sigmaRef.current?.getCamera()[fn]({ duration: 250 });

  return (
    <main className="graphview" id="graph" hidden={!active}>
      <div className="graph-hdr">
        <span className="graph-title">knowledge graph</span>
        <span className="spacer" />
        <span className="graph-stat">
          <b>{(stats?.nodes ?? nodes.length).toLocaleString()}</b> nodes ·{" "}
          <b>{(stats?.edges ?? edges.length).toLocaleString()}</b> edges
          {/* "current" is counted CLIENT-side from the same isInvalidated
              predicate the canvas draws with — the server's live_edges counts
              system-time liveness only (expired_at = 0), so quoting it here
              put a bigger number in the header than edges on the canvas,
              with nothing explaining the gap (review finding). */}
          <>
            {" "}
            · <b>{edges.filter((e) => !isInvalidated(e)).length.toLocaleString()}</b> current
          </>
          {/* The mockup's header reads "layout rev #219". There is no layout
              revision on the wire — positions are recomputed in place — so the
              episode count stands in: it is the number that answers the same
              question, "how much has this picture learned". */}
          {stats?.episodes !== undefined ? (
            <>
              {" "}
              · <b>{stats.episodes.toLocaleString()}</b> episodes
            </>
          ) : null}
          {stats?.pending ? <> · {stats.pending} pending</> : null}
          {stats?.missing_embeddings ? (
            <> · {stats.missing_embeddings} unembedded</>
          ) : null}
        </span>
        {error ? (
          <span className="graph-stat err">couldn&apos;t refresh: {error.message}</span>
        ) : null}
        <button
          className={"graph-chk graph-physics" + (physicsOn ? "" : " off")}
          aria-pressed={physicsOn}
          title={
            physicsOn
              ? "physics on — drag nodes; click to toggle off"
              : "physics off — click to turn on"
          }
          onClick={() => {
            // The next pref is derived from the PREF AXIS (what would
            // physicsAllowed say), never from the composite physicsOn:
            // that composite also encodes renderError/!ready, so on a
            // machine with no WebGL the old derivation flipped the STORED
            // pref back and forth under a button that looked dead — and the
            // parity it happened to stop at then governed the next machine
            // (review finding). Explicit both ways: auto is reachable only
            // by never touching the button.
            const next: PhysicsPref = physicsAllowed(visibleCount, physicsPref, reducedMotion)
              ? "off"
              : "on";
            writePhysicsPref(window.localStorage, next);
            setPhysicsPref(next);
          }}
        >
          {physicsOn
            ? "physics on"
            : visibleCount > PHYSICS_NODE_CAP && physicsPref !== "on"
              ? `physics off — ${visibleCount.toLocaleString()} nodes`
              : "physics off"}
        </button>
      </div>

      <div className="graph-stage">
        <aside className="graph-rail">
          <p className="graph-lbl">Node type</p>
          {types.length ? (
            types.map((t) => (
              <TypeRow
                key={t}
                type={t}
                on={!filters.hiddenTypes.has(t)}
                color={nodeColor(t, palette)}
                onToggle={(type) =>
                  setFilters((f) => {
                    const next = new Set(f.hiddenTypes);
                    if (next.has(type)) next.delete(type);
                    else next.add(type);
                    return { ...f, hiddenTypes: next };
                  })
                }
              />
            ))
          ) : (
            <div className="graph-railempty">—</div>
          )}

          <p className="graph-lbl">Validity</p>
          <button
            className={"graph-chk" + (filters.includeInvalidated ? " off" : "")}
            aria-pressed={!filters.includeInvalidated}
            onClick={() =>
              setFilters((f) => ({ ...f, includeInvalidated: false }))
            }
          >
            current only
          </button>
          <button
            className={"graph-chk" + (filters.includeInvalidated ? "" : " off")}
            aria-pressed={filters.includeInvalidated}
            onClick={() =>
              setFilters((f) => ({ ...f, includeInvalidated: true }))
            }
          >
            include invalidated
          </button>

          <p className="graph-lbl">Min sightings</p>
          <input
            className="graph-slider"
            type="range"
            min={1}
            max={MAX_SIGHTINGS}
            step={1}
            value={filters.minOccurrences}
            aria-label="minimum sightings"
            onChange={(ev) =>
              setFilters((f) => ({
                ...f,
                minOccurrences: Number(ev.target.value),
              }))
            }
          />
          <div className="graph-stat">≥ {filters.minOccurrences}</div>
        </aside>

        <div className="graph-canvas">
          {/* sigma appends its own canvases to this element, so it gets a
              child of its own rather than sharing one with the overlays. */}
          <div className="graph-gl" ref={containerRef} />
          {renderError ? (
            <div className="graph-overlay">
              this browser could not open a WebGL canvas — {renderError}
            </div>
          ) : !loaded ? (
            <div className="graph-overlay">loading the graph…</div>
          ) : !nodes.length ? (
            <div className="graph-overlay">
              the graph is empty — the nightly ingest job fills it
            </div>
          ) : null}
          <span className="graph-hint">
            scroll to zoom · drag to pan · click a node to inspect
          </span>
          <div className="graph-zoom">
            <button className="graph-zbtn" title="zoom in" onClick={zoom("animatedZoom")}>
              +
            </button>
            <button className="graph-zbtn" title="zoom out" onClick={zoom("animatedUnzoom")}>
              −
            </button>
            <button className="graph-zbtn" title="reset" onClick={zoom("animatedReset")}>
              ⤢
            </button>
          </div>
        </div>

        <aside className="graph-rail r graph-insp">
          <p className="graph-lbl">Selected node</p>
          {sel ? (
            <>
              <h3 className="graph-name">{sel.name}</h3>
              <span className="graph-pill">{sel.type}</span>
              {sel.description ? (
                <p className="graph-desc">{sel.description}</p>
              ) : null}
              <div className="graph-meta">
                <b>{degrees.get(sel.id) ?? 0}</b> edges
                {sel.aliases?.length ? (
                  <>
                    <br />
                    aliases:{" "}
                    {sel.aliases.map((a, i) => (
                      <span key={a}>
                        {i ? ", " : ""}
                        <b>{a}</b>
                      </span>
                    ))}
                  </>
                ) : null}
              </div>
              <p className="graph-lbl">Facts</p>
              <Facts facts={selFacts} names={names} />
            </>
          ) : (
            <div className="graph-railempty">
              click a node to read its description and its facts
            </div>
          )}
        </aside>
      </div>
    </main>
  );
}
