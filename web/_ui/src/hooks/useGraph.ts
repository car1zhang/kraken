// The #/graph view's two React hooks. Wire types and fetches live in
// api/graph.ts with the rest of the API surface; this file owns only the
// polling/selection behaviour.

import { useEffect, useState } from "react";

import {
  fetchGraph,
  fetchGraphNode,
  type GraphEdge,
  type GraphNode,
  type GraphNodeDetail,
  type GraphStats,
  type WebGraph,
} from "../api/graph.ts";
import { usePoll } from "./usePoll.ts";

export type { GraphEdge, GraphNode, GraphNodeDetail, GraphStats, WebGraph };

/**
 * 30s.
 *
 * Every other poll in the board is 1-3s because it watches a live process:
 * the board tracks cells that appear and die between ticks, the log tails a
 * file being appended to. The graph is not that — it is written by the
 * nightly ingest job and by the occasional revision job, so between two
 * consecutive board ticks it has changed exactly never. 30s is chosen to be
 * "you will not have to reload the page", not to be timely; the payload is
 * also the largest of any route here (every node and every edge, uncached on
 * the client), which is the second reason not to ask for it every 3 seconds.
 */
export const GRAPH_POLL_MS = 30000;

export interface GraphModel {
  nodes: GraphNode[];
  edges: GraphEdge[];
  stats: GraphStats | undefined;
  /** Last tick's failure, if any. The view shows it beside the stats line. */
  error: Error | undefined;
  /** True once any response has landed — distinguishes "empty" from "loading". */
  loaded: boolean;
}

/**
 * Poll the graph while `active`.
 *
 * Every absence is tolerated rather than thrown on, which is the point of
 * doing the normalizing here: this hook was written against a shape another
 * slice was still building, so a route that 404s, a `{}` body, or a payload
 * whose `nodes` is null all have to come out as an empty graph and a note —
 * never a crashed view.
 *
 * DreamView's rule (empty the pane on a failed tick, since rendering stale
 * data claims a freshness it does not have) is deliberately NOT followed
 * here. That rule costs a 3s-poll pane a blink; at 30s it would cost this one
 * half a minute of blank canvas, over data that by construction cannot have
 * changed in the meantime. The last good graph stays up and the error is
 * stated in the header instead.
 */
export function useGraph(active: boolean): GraphModel {
  const { data, error } = usePoll<WebGraph>(
    (signal) => fetchGraph(signal),
    GRAPH_POLL_MS,
    { enabled: active },
  );
  return {
    nodes: Array.isArray(data?.nodes) ? data.nodes : [],
    edges: Array.isArray(data?.edges) ? data.edges : [],
    stats: data?.stats,
    error,
    loaded: data !== undefined,
  };
}

/**
 * The inspector's read, fetched once per selection rather than polled.
 *
 * It exists for the node's EDGE LIST — live and invalidated both, already
 * cut to this node server-side. The board payload carries every edge
 * globally and the caller can (and, as fallback, does) derive the node's
 * list from it; this read is the per-node cut with no client filter to
 * maintain (aliases now ride the board payload too, so they are no longer
 * this fetch's reason). A one-shot fetch rather
 * than a usePoll because a selection is a click, not a subscription: nothing
 * about one node changes while carl reads it.
 *
 * A failed or still-in-flight fetch resolves to `undefined`, and the caller
 * falls back to what the board payload already holds — so the panel degrades
 * to "everything but the aliases" rather than to an error.
 */
export function useGraphNode(id: number | null): GraphNodeDetail | undefined {
  const [detail, setDetail] = useState<GraphNodeDetail | undefined>(undefined);
  useEffect(() => {
    setDetail(undefined);
    if (id === null) return;
    const ac = new AbortController();
    fetchGraphNode(id, ac.signal)
      .then((d) => {
        if (!ac.signal.aborted) setDetail(d);
      })
      .catch(() => {
        // Deliberately silent: the panel already has a name, a type, a
        // description and the node's facts from the board payload. A toast
        // over a missing alias list would be louder than the loss.
      });
    return () => ac.abort();
  }, [id]);
  return detail;
}
