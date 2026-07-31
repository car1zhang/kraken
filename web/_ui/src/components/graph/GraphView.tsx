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
// Layout comes from the SERVER (node x/y) and there is no client-side force
// simulation, which is not an optimisation: a client simulation stops being
// interactive past ~10k nodes and has no chance on a phone, and computing it
// once in Go is what makes the web board and the iOS WKWebView draw the same
// picture. See 50-components.md #8.

import { MultiDirectedGraph } from "graphology";
import { useEffect, useMemo, useRef, useState } from "react";
import Sigma from "sigma";

import { useGraph, useGraphNode, type GraphEdge } from "../../hooks/useGraph.ts";
import {
  colorOf,
  DEFAULT_FILTERS,
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
 * Re-render when the theme flips.
 *
 * Node and edge colours are resolved from style.css custom properties (see
 * readGraphPalette), and those change value under `data-theme="light"`. The
 * attribute is written by the pre-paint IIFE in index.html — which also owns
 * the `prefers-color-scheme` listener — so observing the attribute catches
 * both the ◐ button and a system switch, and adds no second writer.
 */
function useThemeTick(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (typeof MutationObserver !== "function") return;
    const obs = new MutationObserver(() => setTick((n) => n + 1));
    obs.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
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
  const { nodes, edges, stats, error, loaded } = useGraph(active);
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
  const reducedMotion = useMemo(
    () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false,
    [],
  );
  const physicsRef = useRef<PhysicsHandle | null>(null);
  const rafRef = useRef<number>(0);
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
    g.clear();
    for (const p of positions) {
      const n = byKey.get(nodeKey(p.id));
      if (!n) continue;
      const degree = degrees.get(n.id) ?? 0;
      // merge rather than add: a duplicate id in the payload is a server bug,
      // not a reason for the whole view to throw.
      g.mergeNode(nodeKey(n.id), {
        x: p.x,
        y: p.y,
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
    physicsRef.current?.stop();
    physicsRef.current = null;
    const g = graphRef.current;
    if (!physicsOn || !g || g.order === 0) return;
    const simNodes: { id: string; x: number; y: number; size: number }[] = [];
    g.forEachNode((key, attrs) => {
      if (hidden.has(key)) return;
      simNodes.push({ id: key, x: attrs.x, y: attrs.y, size: attrs.size });
    });
    const simLinks: { source: string; target: string; occurrences: number }[] = [];
    g.forEachEdge((_key, attrs, src, dst) => {
      if (hidden.has(src) || hidden.has(dst)) return;
      simLinks.push({ source: src, target: dst, occurrences: attrs.occurrences });
    });
    const handle = createPhysics({ nodes: simNodes, links: simLinks });
    physicsRef.current = handle;
    // One rAF loop, alive only while the simulation is awake or a drag is
    // pinned — an idle tab burns zero CPU (the sleep half of warm-not-hot),
    // and `active` gates it so a hidden view never ticks.
    const loop = () => {
      const h = physicsRef.current;
      if (h !== handle) return; // superseded by a rebuild
      if (active && (h.awake() || pressRef.current?.dragging)) {
        h.tick();
        for (const n of h.nodes()) {
          if (g.hasNode(n.id)) {
            g.setNodeAttribute(n.id, "x", n.x ?? 0);
            g.setNodeAttribute(n.id, "y", n.y ?? 0);
          }
        }
        sigmaRef.current?.refresh();
      }
      rafRef.current = requestAnimationFrame(loop);
    };
    rafRef.current = requestAnimationFrame(loop);
    return () => {
      cancelAnimationFrame(rafRef.current);
      handle.stop();
    };
  }, [physicsOn, hidden, active, positions, edges]);

  // Drag wiring: downNode arms a press; crossing the threshold turns it into
  // a drag that pins the node to the pointer; mouseup releases. While a drag
  // is live the camera stays put (preventSigmaDefault) and the release
  // suppresses the click that would otherwise open the inspector.
  useEffect(() => {
    const s = sigmaRef.current;
    if (!s || !physicsOn) return;
    const downNode = ({ node, event }: { node: string; event: { x: number; y: number; preventSigmaDefault: () => void } }) => {
      pressRef.current = { key: node, sx: event.x, sy: event.y, dragging: false };
      event.preventSigmaDefault(); // the camera must not pan under a press-on-node
    };
    const move = ({ x, y, preventSigmaDefault, original }: { x: number; y: number; preventSigmaDefault: () => void; original: Event }) => {
      const press = pressRef.current;
      if (!press) return;
      if (!press.dragging) {
        if (!isDragMovement(x - press.sx, y - press.sy)) return;
        press.dragging = true;
      }
      preventSigmaDefault();
      original.preventDefault();
      const graphCoords = s.viewportToGraph({ x, y });
      physicsRef.current?.drag(press.key, graphCoords.x, graphCoords.y);
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
    s.getMouseCaptor().on("mousemovebody", move);
    s.getMouseCaptor().on("mouseup", up);
    s.on("downNode", downNode);
    return () => {
      s.getMouseCaptor().off("mousemovebody", move);
      s.getMouseCaptor().off("mouseup", up);
      s.off("downNode", downNode);
    };
  }, [ready, physicsOn]);

  // --------------------------------------------------------------- derived
  // The detail read wins where it has an answer (it is the only source of
  // aliases), and the board payload is the fallback while it is in flight.
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
              : visibleCount > PHYSICS_NODE_CAP && physicsPref !== "on"
                ? `physics off — ${visibleCount.toLocaleString()} nodes exceeds the ${PHYSICS_NODE_CAP.toLocaleString()} cap; click to force on`
                : "physics off — click to turn on"
          }
          onClick={() => {
            // The toggle flips relative to the EFFECTIVE state, and writes an
            // explicit pref both ways: auto is only ever reachable by never
            // touching the button, which keeps its behaviour predictable.
            const next: PhysicsPref = physicsOn ? "off" : "on";
            writePhysicsPref(window.localStorage, next);
            setPhysicsPref(next);
          }}
        >
          {physicsOn ? "physics on" : "physics off"}
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
