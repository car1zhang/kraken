import { describe, expect, it } from "vitest";

import { ApiError } from "../api/client.ts";
import type { WebJob, WebJobOwner, WebNode } from "../api/types.ts";
import { filterFleet, isUnauthorized, nodesStat, ownerChipText, remoteMode } from "./fleet.ts";
import { jobKey } from "./jobKey.ts";

// fleet.edge.test.ts — the EDGE dimension of 50-contract.md slice W's pure
// functions: remoteMode on missing or unexpected visibility, ownerChipText
// with no operator, filterFleet's ordering and its never-drop-a-local rule,
// nodesStat's null cases, isUnauthorized on anything that is not an ApiError,
// and jobKey for colliding slugs. Written blind from the contract text, with
// no sight of web/_ui/src/lib/fleet.ts or lib/jobKey.ts. Happy paths are
// athena-logic's.
//
// Every helper is prefixed `edgeFleet`.

function edgeFleetOwner(node_id: string, over: Partial<WebJobOwner> = {}): WebJobOwner {
  return { node_id, label: `label-${node_id}`, operator: "Jess", online: true, ...over };
}

function edgeFleetCard(slug: string, over: Partial<WebJob> = {}): WebJob {
  return { slug, repo: "kraken", state: "working", live: true, mtime: 1, ...over } as WebJob;
}

function edgeFleetNode(node_id: string): WebNode {
  return {
    node_id,
    label: node_id,
    operator: "Jess",
    dns_name: `${node_id}.tail072ad8.ts.net`,
    ip: "100.70.0.1",
    port: 7777,
    build: "abc",
    contract: 7,
    online: true,
    last_ok: 1,
  };
}

describe("remoteMode", () => {
  // "with owner: "public" iff visibility === "public", else "readonly" (any
  // other or missing value)."
  it("reads an owned card with missing or unknown visibility as readonly", () => {
    const owner = edgeFleetOwner("nA");
    for (const visibility of [undefined, "", "PUBLIC", "Public", "private", "readonly", "everyone", " public"]) {
      expect(remoteMode({ owner, visibility })).toBe("readonly");
    }
  });

  // ""local" without owner" — whatever the visibility says.
  it("reads an unowned card as local even when it is public", () => {
    for (const visibility of [undefined, "public", "private", "garbage"]) {
      expect(remoteMode({ owner: undefined, visibility })).toBe("local");
    }
  });
});

describe("ownerChipText", () => {
  // "`label` alone when `operator` is empty."
  it("is the label alone with an empty operator", () => {
    expect(ownerChipText(edgeFleetOwner("nA", { label: "studio-mini", operator: "" }))).toBe("studio-mini");
  });

  // "(` · ` = space, U+00B7, space)"
  it("joins with space, U+00B7, space", () => {
    expect(ownerChipText(edgeFleetOwner("nA", { label: "a", operator: "b" }))).toBe("a · b");
  });
});

describe("filterFleet", () => {
  const local1 = edgeFleetCard("local-1");
  const remoteA = edgeFleetCard("dream", { owner: edgeFleetOwner("nA") });
  const local2 = edgeFleetCard("local-2", { archived: true });
  const remoteB = edgeFleetCard("dream", { owner: edgeFleetOwner("nB") });
  const local3 = edgeFleetCard("nA");
  const board = [local1, remoteA, local2, remoteB, local3];

  // job 634: `["local"]` keeps every local card in order, archived included
  it("a local-only selection keeps every local card in order", () => {
    expect(filterFleet(board, ["local"])).toEqual([local1, local2, local3]);
  });

  // a local card whose slug equals a selected node id is NOT matched by it
  it("a node id never matches a local card, even one whose slug is that id", () => {
    expect(filterFleet(board, ["nA"])).toEqual([remoteA]);
  });

  it("no selection preserves the whole board in order", () => {
    expect(filterFleet(board, [])).toEqual(board);
  });

  it("an unknown selected node keeps nothing", () => {
    expect(filterFleet(board, ["nZ"])).toEqual([]);
  });

  it("an empty board is an empty board", () => {
    expect(filterFleet([], ["nA"])).toEqual([]);
    expect(filterFleet([], [])).toEqual([]);
  });
});

describe("nodesStat", () => {
  // "`null` when `nodes` is empty/undefined"
  it("is null for an undefined or empty registry, even with owned cards", () => {
    const jobs = [edgeFleetCard("x", { owner: edgeFleetOwner("nA") })];
    expect(nodesStat(undefined, jobs)).toBeNull();
    expect(nodesStat([], jobs)).toBeNull();
  });

  // "`owned` counts cards with `owner`" — zero owned is still a stat.
  it("counts zero owned cards as 0 jobs", () => {
    expect(nodesStat([edgeFleetNode("nA")], [edgeFleetCard("local")])).toBe("nodes 1 · 0 jobs");
  });
});

describe("isUnauthorized", () => {
  // "True iff `err instanceof ApiError && err.status === 401`." — nothing
  // that merely LOOKS like a 401 qualifies.
  it("is false for everything that is not an ApiError", () => {
    const lookalike = Object.assign(new Error("401 unauthorized"), { status: 401 });
    for (const err of [undefined, null, 401, "401", "unauthorized", { status: 401 }, new Error("401"), lookalike]) {
      expect(isUnauthorized(err)).toBe(false);
    }
  });

  // Amended: `ApiError` is `constructor(status: number, message: string)` from
  // api/client.ts — 401 is the only status that qualifies.
  it("is true for an ApiError 401 and false for any other status", () => {
    expect(isUnauthorized(new ApiError(401, "unauthorized"))).toBe(true);
    for (const status of [403, 400, 404, 409, 429, 500, 502, 0]) {
      expect(isUnauthorized(new ApiError(status, "unauthorized"))).toBe(false);
    }
  });
});

describe("jobKey", () => {
  // "Two cards with one slug and two owners have two keys."
  it("keys two owners' same-slug cards apart, and apart from the local one", () => {
    const a = jobKey(edgeFleetCard("dream-2026-10-20", { owner: edgeFleetOwner("nA") }));
    const b = jobKey(edgeFleetCard("dream-2026-10-20", { owner: edgeFleetOwner("nB") }));
    const local = jobKey(edgeFleetCard("dream-2026-10-20"));
    expect(a).toBe("nA/dream-2026-10-20");
    expect(b).toBe("nB/dream-2026-10-20");
    expect(local).toBe("dream-2026-10-20");
  });

  // "Owned card → `${owner.node_id}/${slug}`; archived → `archive:${slug}`" —
  // the owned rule is stated first, so an archived owned card keys by owner.
  it("keys an archived owned card by its owner", () => {
    expect(jobKey(edgeFleetCard("old", { owner: edgeFleetOwner("nA"), archived: true }))).toBe("nA/old");
    expect(jobKey(edgeFleetCard("old", { archived: true }))).toBe("archive:old");
  });
});
