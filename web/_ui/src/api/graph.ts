// The knowledge graph's wire types and fetches — the `#/graph` view's whole
// server surface. Split from useGraph.ts once the parallel build settled:
// api/ is where every other route's types and typed fetches live, and the
// hook file keeps only the React halves (useGraph / useGraphNode).

import { getJSON } from "./client.ts";

/**
 * One entity. `x`/`y` are SERVER-COMPUTED layout coordinates in graph space —
 * the client never runs a force simulation, which is what lets the web board
 * and the iOS WKWebView draw the identical picture (50-components.md #8).
 */
export interface GraphNode {
  id: number;
  name: string;
  /**
   * The entity kind — `convention`, `component`, `failure-mode`, `decision`,
   * `file`, `job`, … Deliberately typed as a bare string, not a union: the
   * extraction agent decides what kinds exist, so an unrecognized one has to
   * render (in the fallback colour) rather than fail to typecheck.
   */
  type: string;
  description: string;
  x: number;
  y: number;
  /**
   * NEVER present on /api/graph — joining them would be a query per node on
   * the hottest read there (ios/API.md). The inspector gets them from
   * /api/graph/node, which is why selecting a node is its own fetch.
   */
  aliases?: string[];
}

/** One fact, as an edge between two nodes. */
export interface GraphEdge {
  id: number;
  src: number;
  dst: number;
  predicate: string;
  fact: string;
  strength: number;
  /** How many separate episodes asserted this. Drives the min-sightings floor. */
  occurrences: number;
  /**
   * The two time axes, both unix millis, both ALWAYS present: `valid_until`
   * is world time (0 = still true) and `expired_at` is system time (0 =
   * live). 0 is a real answer on each, which is why neither is omitempty on
   * the wire and neither may be read as merely falsy-because-absent.
   */
  valid_until: number;
  expired_at: number;
  episode: number;
}

export interface GraphStats {
  nodes: number;
  edges: number;
  live_edges: number;
  episodes: number;
  pending: number;
  /**
   * Nodes and live edges with no vector yet. Sits non-zero forever when no
   * embedder is configured, which is a normal state rather than an error —
   * so the header states it and never styles it as a failure.
   */
  missing_embeddings: number;
}

export interface WebGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
  stats: GraphStats;
}

/** `GET /api/graph`. */
export const fetchGraph = (signal?: AbortSignal) =>
  getJSON<WebGraph>("/api/graph", signal);


/** `GET /api/graph/node?id=N` — one node, its aliases, and its every edge. */
export interface GraphNodeDetail {
  node: GraphNode;
  edges: GraphEdge[];
}

export const fetchGraphNode = (id: number, signal?: AbortSignal) =>
  getJSON<GraphNodeDetail>(
    `/api/graph/node?id=${encodeURIComponent(String(id))}`,
    signal,
  );

