import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fetchJobEvents, fetchMapPositions, saveMapPositions } from "./client.ts";

// agentmap.contract.test.ts — athena-logic's blind tests for the three client
// functions the agent map adds (50-contract.md W3 and W4): method, path,
// query, body and resolved value.

function agentMapContractStubFetch(body: unknown) {
  const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function agentMapContractOnlyCall(fetchMock: ReturnType<typeof agentMapContractStubFetch>) {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit | undefined];
  return { url: new URL(String(url), "http://board.local"), method: (init?.method ?? "GET").toUpperCase(), init };
}

describe("agent map client functions", () => {
  beforeEach(() => vi.unstubAllGlobals());
  afterEach(() => vi.unstubAllGlobals());

  it("fetchJobEvents GETs /api/job/events with slug and n, resolving to the events array", async () => {
    const events = [
      { id: "toolu_1", t: 1, act: "test", label: "go test", dur_ms: 5 },
      { id: "u1", t: 2, act: "hear", label: "prompt from you", from: "you" },
    ];
    const f = agentMapContractStubFetch({ events });
    await expect(fetchJobEvents("agent map", 60)).resolves.toEqual(events);
    const c = agentMapContractOnlyCall(f);
    expect(c.method).toBe("GET");
    expect(c.url.pathname).toBe("/api/job/events");
    expect(c.url.searchParams.get("slug")).toBe("agent map");
    expect(c.url.searchParams.get("n")).toBe("60");
  });

  it("fetchMapPositions GETs /api/map/positions and resolves to its positions object", async () => {
    const positions = { alpha: [10, 20], "archive:old": [-5, 7.5] };
    const f = agentMapContractStubFetch({ positions });
    await expect(fetchMapPositions()).resolves.toEqual(positions);
    const c = agentMapContractOnlyCall(f);
    expect(c.method).toBe("GET");
    expect(c.url.pathname).toBe("/api/map/positions");
  });

  it("saveMapPositions POSTs {positions: changes} to /api/map/positions", async () => {
    const f = agentMapContractStubFetch({ ok: true });
    const changes: Record<string, [number, number]> = { alpha: [1, 2], "nodeX/beta": [300.5, -4] };
    await saveMapPositions(changes);
    const c = agentMapContractOnlyCall(f);
    expect(c.method).toBe("POST");
    expect(c.url.pathname).toBe("/api/map/positions");
    expect(JSON.parse(String(c.init?.body))).toEqual({ positions: changes });
  });
});
