import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebGraph } from "./useGraph.ts";
import { useGraph, useGraphNode } from "./useGraph.ts";

vi.mock("../api/client.ts", () => ({ getJSON: vi.fn() }));
const { getJSON } = await import("../api/client.ts");
const mockGet = vi.mocked(getJSON);

beforeEach(() => {
  mockGet.mockReset();
});

const graph: WebGraph = {
  nodes: [
    {
      id: 1,
      name: "artifact prose",
      type: "convention",
      description: "how markdown artifacts are formatted",
      x: 3,
      y: 4,
    },
  ],
  edges: [
    {
      id: 1,
      src: 1,
      dst: 2,
      predicate: "prefers",
      fact: "carl prefers markdown artifacts unwrapped",
      strength: 9,
      occurrences: 2,
      valid_until: 0,
      expired_at: 0,
      episode: 4412,
    },
  ],
  stats: {
    nodes: 1,
    edges: 1,
    live_edges: 1,
    episodes: 219,
    pending: 0,
    missing_embeddings: 0,
  },
};

describe("useGraph", () => {
  it("asks for /api/graph and passes the payload through", async () => {
    mockGet.mockResolvedValue(graph);
    const { result } = renderHook(() => useGraph(true));
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(mockGet).toHaveBeenCalledWith("/api/graph", expect.anything());
    expect(result.current.nodes).toHaveLength(1);
    expect(result.current.stats?.episodes).toBe(219);
  });

  // THE reason this hook normalizes at all: it was written against a shape
  // another slice was still building, so a route that is not there yet has to
  // come out as an empty graph plus a note — never a crashed view.
  it("degrades an absent endpoint to an empty graph and an error", async () => {
    mockGet.mockRejectedValue(new Error("404 Not Found"));
    const { result } = renderHook(() => useGraph(true));
    await waitFor(() => expect(result.current.error).toBeDefined());
    expect(result.current.nodes).toEqual([]);
    expect(result.current.edges).toEqual([]);
    expect(result.current.stats).toBeUndefined();
    expect(result.current.loaded).toBe(false);
  });

  // Go marshals an empty slice as `null`, so "the graph is empty" and "the
  // key is missing" arrive as the same body. Both are an empty graph, and
  // neither may reach .map().
  it("tolerates null arrays and a missing stats block", async () => {
    mockGet.mockResolvedValue({ nodes: null, stats: undefined } as unknown as WebGraph);
    const { result } = renderHook(() => useGraph(true));
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.nodes).toEqual([]);
    expect(result.current.edges).toEqual([]);
    expect(result.current.stats).toBeUndefined();
  });

  it("does not poll while the view is hidden", () => {
    mockGet.mockResolvedValue(graph);
    renderHook(() => useGraph(false));
    expect(mockGet).not.toHaveBeenCalled();
  });
});

describe("useGraphNode", () => {
  it("asks the detail route for the selected id only", async () => {
    mockGet.mockResolvedValue({ node: graph.nodes[0], edges: graph.edges });
    const { result, rerender } = renderHook(
      ({ id }: { id: number | null }) => useGraphNode(id),
      { initialProps: { id: null as number | null } },
    );
    expect(mockGet).not.toHaveBeenCalled();
    rerender({ id: 7 });
    await waitFor(() => expect(result.current).toBeDefined());
    expect(mockGet).toHaveBeenCalledWith("/api/graph/node?id=7", expect.anything());
  });

  // The panel already has a name, a type, a description, aliases and the
  // node's facts from the board payload; a failed detail read costs only the
  // pre-cut per-node edge list, so it must not surface as an error state.
  it("resolves to undefined on failure rather than throwing", async () => {
    mockGet.mockRejectedValue(new Error("404 Not Found"));
    const { result } = renderHook(() => useGraphNode(7));
    await waitFor(() => expect(mockGet).toHaveBeenCalled());
    expect(result.current).toBeUndefined();
  });
});
