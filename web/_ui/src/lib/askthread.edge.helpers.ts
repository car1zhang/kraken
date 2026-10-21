import { vi } from "vitest";

// askthread.edge.helpers.ts — fixtures for askthread.edge.test.tsx only (the
// EDGE dimension of 50-contract.md slice D, ask threads ported into kraken).
// Every export is `askEdge`-prefixed. Written blind: no implementation file
// read.

export type AskEdgeReply = { status: number; body: unknown };
export type AskEdgeRoute = (u: URL, init?: RequestInit) => AskEdgeReply | undefined;

/** Stubs globalThis.fetch with a router; unrouted requests answer {ok:true}. */
export function askEdgeStubFetch(route: AskEdgeRoute = () => undefined) {
  const fetchMock = vi.fn((input: unknown, init?: RequestInit) => {
    const u = new URL(String(input), "http://kraken.test");
    const r = route(u, init) ?? askEdgeDefaultRoute(u);
    const text = typeof r.body === "string" ? r.body : JSON.stringify(r.body);
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      statusText: "",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(typeof r.body === "string" ? JSON.parse(r.body) : r.body),
      text: () => Promise.resolve(text),
      blob: () => Promise.resolve(new Blob([text])),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function askEdgeDefaultRoute(u: URL): AskEdgeReply {
  switch (u.pathname) {
    case "/api/goals":
      return { status: 200, body: { goals: { week: [], day: [] } } };
    case "/api/nodes/transcript":
    case "/api/job/transcript":
      return { status: 200, body: { session: "s1", blocks: [] } };
    default:
      return { status: 200, body: { ok: true } };
  }
}

export type AskEdgeFetch = ReturnType<typeof askEdgeStubFetch>;

export function askEdgeCalls(f: AskEdgeFetch): { url: URL; method: string; body: unknown }[] {
  return f.mock.calls.map(([input, init]) => {
    const i = init as RequestInit | undefined;
    let body: unknown = undefined;
    if (typeof i?.body === "string") {
      try {
        body = JSON.parse(i.body);
      } catch {
        body = i.body;
      }
    }
    return {
      url: new URL(String(input), "http://kraken.test"),
      method: (i?.method ?? "GET").toUpperCase(),
      body,
    };
  });
}

export function askEdgePosts(f: AskEdgeFetch) {
  return askEdgeCalls(f).filter((c) => c.method !== "GET");
}

/** Lets pending promises and effects run. */
export async function askEdgeSettle(rounds = 5) {
  for (let i = 0; i < rounds; i++) {
    await new Promise((r) => setTimeout(r, 5));
  }
}

/**
 * Whether an element with `id` is on screen: present and inside no `[hidden]`
 * subtree. The board hides inactive controls with the `hidden` attribute, so
 * absent and hidden both read "not offered".
 */
export function askEdgeShown(id: string): boolean {
  const el = document.getElementById(id);
  return !!el && !el.closest("[hidden]");
}

/** The app's mutate: awaits the op, answers null on a thrown error, never rethrows. */
export const askEdgeMutate = async <T,>(op: () => Promise<T>): Promise<T | null> => {
  try {
    return await op();
  } catch {
    return null;
  }
};

export function askEdgeStorageClear() {
  try {
    localStorage.clear();
  } catch {
    /* storage may be unavailable */
  }
}
