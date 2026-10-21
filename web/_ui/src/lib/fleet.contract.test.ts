// fleet.contract.test.ts — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice W (web SPA). Authored from
// artifacts/50-contract.md alone; no implementation file was read. LOGIC
// dimension: the stated invariants and happy paths of every exported symbol
// the contract names. Boundaries and odd inputs are athena-edge's.
//
// Module placement follows the contract table literally: `VISIBILITIES` /
// `Visibility` are annotated `(lib/fleet.ts)`; `jobKey` is annotated
// `(lib/jobKey.ts)` and the un-annotated rows after it (isRemote, RemoteMode,
// remoteMode, ownerChipText, filterFleet, nodesStat, isUnauthorized) continue
// that annotation, exactly as WebNode/WebBoard.nodes/PairDevice continue
// `WebJobOwner (api/types.ts)` and the client functions continue
// `fetchNodes (api/client.ts)`. (Disclosed as a contract silence.)

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { VISIBILITIES } from "./fleet.ts";
import type { Visibility } from "./fleet.ts";
import {
  filterFleet,
  isRemote,
  isUnauthorized,
  jobKey,
  nodesStat,
  ownerChipText,
  remoteMode,
} from "./jobKey.ts";
import {
  ApiError,
  fetchNodeJobRaw,
  fetchNodeRead,
  fetchNodes,
  fetchPairDevices,
  postNodeAction,
  revokePairDevice,
  setJobVisibility,
  setPairPassword,
} from "../api/client.ts";
import type { WebJob, WebJobOwner, WebNode } from "../api/types.ts";

const contractFleetOwnerB: WebJobOwner = {
  node_id: "nSTUDIOmini77CNTRL",
  label: "studio-mini",
  operator: "Jess",
  online: true,
};
const contractFleetOwnerC: WebJobOwner = {
  node_id: "nKITCHEN42CNTRL",
  label: "kitchen-mac",
  operator: "Sam",
  online: false,
};

function contractFleetJob(slug: string, extra: Partial<WebJob> = {}): WebJob {
  return { slug, state: "working", ...extra } as unknown as WebJob;
}

function contractFleetNode(node_id: string, label: string): WebNode {
  return {
    node_id,
    label,
    operator: "Jess",
    dns_name: `${label}.tail072ad8.ts.net`,
    ip: "100.70.1.2",
    port: 7777,
    build: "abc",
    contract: 7,
    online: true,
    last_ok: 1760000000000,
  };
}

describe("VISIBILITIES", () => {
  // `export const VISIBILITIES = ["private","readonly","public"] as const`
  it("is exactly the three levels in order", () => {
    expect([...VISIBILITIES]).toEqual(["private", "readonly", "public"]);
    const v: Visibility = "public";
    expect(VISIBILITIES).toContain(v);
  });
});

describe("jobKey", () => {
  // "Owned card → `${owner.node_id}/${slug}`; archived → `archive:${slug}`;
  // else `slug`. Two cards with one slug and two owners have two keys."
  it("keys an owned card by owner and slug", () => {
    expect(jobKey({ slug: "dream-2026-10-20", owner: contractFleetOwnerB } as WebJob)).toBe(
      "nSTUDIOmini77CNTRL/dream-2026-10-20",
    );
  });

  // AMENDED 14:40: "the owned rule wins over archived"
  it("keys an owned archived card by owner, not archive:", () => {
    expect(jobKey({ slug: "old-job", archived: true, owner: contractFleetOwnerB } as WebJob)).toBe(
      "nSTUDIOmini77CNTRL/old-job",
    );
  });

  it("keys an archived local card as archive:slug", () => {
    expect(jobKey({ slug: "old-job", archived: true } as WebJob)).toBe("archive:old-job");
  });

  it("keys a plain local card by its slug", () => {
    expect(jobKey({ slug: "tech-comms" } as WebJob)).toBe("tech-comms");
  });

  it("gives same-slug cards from two owners (and the local one) three keys", () => {
    const keys = [
      jobKey({ slug: "dream-2026-10-20" } as WebJob),
      jobKey({ slug: "dream-2026-10-20", owner: contractFleetOwnerB } as WebJob),
      jobKey({ slug: "dream-2026-10-20", owner: contractFleetOwnerC } as WebJob),
    ];
    expect(new Set(keys).size).toBe(3);
  });
});

describe("isRemote / remoteMode", () => {
  // "True iff owner is present."
  it("isRemote is true exactly for owned cards", () => {
    expect(isRemote({ owner: contractFleetOwnerB })).toBe(true);
    expect(isRemote({})).toBe(false);
  });

  // `"local"` without owner; with owner: `"public"` iff visibility === "public",
  // else `"readonly"`.
  it("is local without an owner whatever the visibility", () => {
    expect(remoteMode({ visibility: "public" })).toBe("local");
    expect(remoteMode({ visibility: "private" })).toBe("local");
    expect(remoteMode({})).toBe("local");
  });

  it("is public for an owned public card", () => {
    expect(remoteMode({ owner: contractFleetOwnerB, visibility: "public" })).toBe("public");
  });

  it("is readonly for an owned card of any other visibility", () => {
    expect(remoteMode({ owner: contractFleetOwnerB, visibility: "readonly" })).toBe("readonly");
    expect(remoteMode({ owner: contractFleetOwnerB, visibility: "private" })).toBe("readonly");
    expect(remoteMode({ owner: contractFleetOwnerB })).toBe("readonly");
  });
});

describe("ownerChipText", () => {
  // "`${label} · ${operator}` (` · ` = space, U+00B7, space); label alone when
  // operator is empty."
  it("joins label and operator with space-middot-space", () => {
    expect(ownerChipText(contractFleetOwnerB)).toBe("studio-mini · Jess");
  });

  it("is the label alone when operator is empty", () => {
    expect(ownerChipText({ ...contractFleetOwnerB, operator: "" })).toBe("studio-mini");
  });
});

describe("filterFleet", () => {
  const local1 = contractFleetJob("local-a");
  const b1 = contractFleetJob("tech-comms", { owner: contractFleetOwnerB });
  const local2 = contractFleetJob("local-b");
  const c1 = contractFleetJob("dream", { owner: contractFleetOwnerC });
  const b2 = contractFleetJob("dream", { owner: contractFleetOwnerB });
  const all = [local1, b1, local2, c1, b2];

  // job 634: the selection is the machines to SHOW — `local` keeps exactly
  // the unowned cards, order preserved
  it("a local-only selection keeps exactly the unowned cards", () => {
    expect(filterFleet(all, ["local"])).toEqual([local1, local2]);
  });

  // a node id keeps exactly that node's cards
  it("a node id keeps exactly that node's cards", () => {
    expect(filterFleet(all, ["nSTUDIOmini77CNTRL"])).toEqual([b1, b2]);
  });

  it("a mixed selection is the union, in server order", () => {
    expect(filterFleet(all, ["nKITCHEN42CNTRL", "local"])).toEqual([local1, local2, c1]);
  });

  it("keeps everything, in order, with no selection", () => {
    expect(filterFleet(all, [])).toEqual(all);
  });
});

describe("nodesStat", () => {
  // "null when nodes is empty/undefined; else `nodes ${nodes.length} · ${owned}
  // jobs` where owned counts cards with owner."
  it("is null with no registry", () => {
    expect(nodesStat(undefined, [])).toBeNull();
    expect(nodesStat([], [contractFleetJob("x", { owner: contractFleetOwnerB })])).toBeNull();
  });

  it("counts nodes and owned cards", () => {
    const nodes = [contractFleetNode("n-b", "studio-mini"), contractFleetNode("n-c", "kitchen-mac")];
    const jobs = [
      contractFleetJob("local"),
      contractFleetJob("a", { owner: contractFleetOwnerB }),
      contractFleetJob("b", { owner: contractFleetOwnerB }),
      contractFleetJob("c", { owner: contractFleetOwnerC }),
    ];
    expect(nodesStat(nodes, jobs)).toBe("nodes 2 · 3 jobs");
  });
});

// ---------------------------------------------------------------------------
// client functions: method, path, body
// ---------------------------------------------------------------------------

type ContractFleetCall = { url: string; init: RequestInit | undefined };

function contractFleetStubFetch(body: unknown = { ok: true }, status = 200) {
  const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve({
      ok: status >= 200 && status < 300,
      status,
      statusText: status === 200 ? "OK" : "Error",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function contractFleetCall(fetchMock: ReturnType<typeof contractFleetStubFetch>): ContractFleetCall {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit | undefined];
  return { url: String(url), init };
}

function contractFleetMethod(c: ContractFleetCall): string {
  return (c.init?.method ?? "GET").toUpperCase();
}

function contractFleetBody(c: ContractFleetCall): unknown {
  return JSON.parse(String(c.init?.body));
}

describe("api client — fleet and pairing routes", () => {
  beforeEach(() => vi.unstubAllGlobals());
  afterEach(() => vi.unstubAllGlobals());

  // `fetchNodes`: "GET /api/nodes."
  it("fetchNodes GETs /api/nodes and returns the body", async () => {
    const body = { nodes: [contractFleetNode("n-b", "studio-mini")] };
    const f = contractFleetStubFetch(body);
    await expect(fetchNodes()).resolves.toEqual(body);
    const c = contractFleetCall(f);
    expect(c.url).toBe("/api/nodes");
    expect(contractFleetMethod(c)).toBe("GET");
  });

  // `postNodeAction`: "POST /api/nodes/action with body {node, slug, action,
  // payload}" — payload defaults to {}.
  it("postNodeAction POSTs node, slug, action and payload", async () => {
    const f = contractFleetStubFetch();
    await postNodeAction("nSTUDIOmini77CNTRL", "tech-comms", "steer", { text: "hello" });
    const c = contractFleetCall(f);
    expect(c.url).toBe("/api/nodes/action");
    expect(contractFleetMethod(c)).toBe("POST");
    expect(contractFleetBody(c)).toEqual({
      node: "nSTUDIOmini77CNTRL",
      slug: "tech-comms",
      action: "steer",
      payload: { text: "hello" },
    });
  });

  it("postNodeAction sends an empty payload object by default", async () => {
    const f = contractFleetStubFetch();
    await postNodeAction("nSTUDIOmini77CNTRL", "tech-comms", "done");
    expect(contractFleetBody(contractFleetCall(f))).toEqual({
      node: "nSTUDIOmini77CNTRL",
      slug: "tech-comms",
      action: "done",
      payload: {},
    });
  });

  // `setJobVisibility`: "POST /api/job/visibility {slug, visibility}."
  it("setJobVisibility POSTs slug and visibility", async () => {
    const f = contractFleetStubFetch();
    await setJobVisibility("tech-comms", "public");
    const c = contractFleetCall(f);
    expect(c.url).toBe("/api/job/visibility");
    expect(contractFleetMethod(c)).toBe("POST");
    expect(contractFleetBody(c)).toEqual({ slug: "tech-comms", visibility: "public" });
  });

  // `fetchPairDevices`: "GET /api/pair/devices".
  it("fetchPairDevices GETs /api/pair/devices", async () => {
    const body = {
      devices: [
        { id: 3, label: "Carl's iPhone", tailnet_name: "iphone173", created_at: 1, last_seen_at: 2, last_ip: "100.101.102.103" },
      ],
    };
    const f = contractFleetStubFetch(body);
    await expect(fetchPairDevices()).resolves.toEqual(body);
    const c = contractFleetCall(f);
    expect(c.url).toBe("/api/pair/devices");
    expect(contractFleetMethod(c)).toBe("GET");
  });

  // `setPairPassword`: "POST /api/pair/password {password}".
  it("setPairPassword POSTs the password and returns the reply", async () => {
    const f = contractFleetStubFetch({ ok: true, revoked: 2 });
    await expect(setPairPassword("correct-horse-battery")).resolves.toEqual({ ok: true, revoked: 2 });
    const c = contractFleetCall(f);
    expect(c.url).toBe("/api/pair/password");
    expect(contractFleetMethod(c)).toBe("POST");
    expect(contractFleetBody(c)).toEqual({ password: "correct-horse-battery" });
  });

  // `revokePairDevice`: "POST /api/pair/revoke {id}".
  it("revokePairDevice POSTs the device id", async () => {
    const f = contractFleetStubFetch();
    await revokePairDevice(7);
    const c = contractFleetCall(f);
    expect(c.url).toBe("/api/pair/revoke");
    expect(contractFleetMethod(c)).toBe("POST");
    expect(contractFleetBody(c)).toEqual({ id: 7 });
  });

  // `fetchNodeJobRaw`: "GET /api/nodes/job/raw?node=&slug=".
  it("fetchNodeJobRaw GETs /api/nodes/job/raw with node and slug", async () => {
    const f = contractFleetStubFetch({ text: "---\njob: tech-comms\n---\n" });
    await fetchNodeJobRaw("nSTUDIOmini77CNTRL", "tech-comms");
    const c = contractFleetCall(f);
    const u = new URL(c.url, "http://kraken.test");
    expect(u.pathname).toBe("/api/nodes/job/raw");
    expect(u.searchParams.get("node")).toBe("nSTUDIOmini77CNTRL");
    expect(u.searchParams.get("slug")).toBe("tech-comms");
    expect(contractFleetMethod(c)).toBe("GET");
  });

  // `fetchNodeRead`: "GET /api/nodes/read?node=&slug=&n=".
  it("fetchNodeRead GETs /api/nodes/read with node, slug and n", async () => {
    const f = contractFleetStubFetch({ blocks: [], counted: 0 });
    await fetchNodeRead("nSTUDIOmini77CNTRL", "tech-comms", 5);
    const c = contractFleetCall(f);
    const u = new URL(c.url, "http://kraken.test");
    expect(u.pathname).toBe("/api/nodes/read");
    expect(u.searchParams.get("node")).toBe("nSTUDIOmini77CNTRL");
    expect(u.searchParams.get("slug")).toBe("tech-comms");
    expect(u.searchParams.get("n")).toBe("5");
    expect(contractFleetMethod(c)).toBe("GET");
  });
});

describe("isUnauthorized", () => {
  beforeEach(() => vi.unstubAllGlobals());
  afterEach(() => vi.unstubAllGlobals());

  async function contractFleetErrorFrom(status: number): Promise<unknown> {
    contractFleetStubFetch({ error: status === 401 ? "unauthorized" : "boom" }, status);
    try {
      await fetchNodes();
    } catch (err) {
      return err;
    }
    throw new Error(`fetchNodes resolved on a ${status}`);
  }

  // "True iff err instanceof ApiError && err.status === 401." The ApiError is
  // the one the client itself throws on a non-2xx.
  it("is true for the client's 401 error", async () => {
    expect(isUnauthorized(await contractFleetErrorFrom(401))).toBe(true);
  });

  it("is false for the client's error on any other status", async () => {
    expect(isUnauthorized(await contractFleetErrorFrom(500))).toBe(false);
  });

  // AMENDED 14:40: ApiError is client.ts's `constructor(status, message)`.
  it("is true for a directly constructed ApiError(401) and false for ApiError(403)", () => {
    expect(isUnauthorized(new ApiError(401, "unauthorized"))).toBe(true);
    expect(isUnauthorized(new ApiError(403, "not public"))).toBe(false);
  });

  it("is false for a plain Error", () => {
    expect(isUnauthorized(new Error("401 unauthorized"))).toBe(false);
  });
});
