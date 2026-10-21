import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebNode } from "../api/types.ts";
import {
  FLEET_NODES_KEY,
  fleetButtonLabel,
  jobNodeKey,
  LOCAL_NODE,
  readFleetNodes,
  writeFleetNodes,
} from "./fleet.ts";

// fleetNodes.test.ts — the machine filter's persistence (with its one-time
// migration off the two legacy keys) and the dropdown's button label, from
// 50-components.md's fleet.ts obligations (job 634).

function node(node_id: string, label: string, online = true): WebNode {
  return {
    node_id,
    label,
    operator: "MJ",
    dns_name: `${label}.tail.ts.net`,
    ip: "100.70.0.2",
    port: 7777,
    build: "abc",
    contract: 7,
    online,
    last_ok: 1,
  };
}

describe("readFleetNodes / writeFleetNodes", () => {
  beforeEach(() => window.localStorage.clear());
  afterEach(() => vi.restoreAllMocks());

  it("reads a stored list, deduped and with empty strings dropped", () => {
    window.localStorage.setItem(FLEET_NODES_KEY, JSON.stringify(["local", "nB", "nB", ""]));
    expect(readFleetNodes()).toEqual(["local", "nB"]);
  });

  it("migrates hideOthers=true to [local] once and removes both legacy keys", () => {
    window.localStorage.setItem("kraken.hideOthers", "true");
    window.localStorage.setItem("kraken.hiddenNodes", JSON.stringify(["nB"]));
    expect(readFleetNodes()).toEqual([LOCAL_NODE]);
    expect(window.localStorage.getItem(FLEET_NODES_KEY)).toBe(JSON.stringify([LOCAL_NODE]));
    expect(window.localStorage.getItem("kraken.hideOthers")).toBeNull();
    expect(window.localStorage.getItem("kraken.hiddenNodes")).toBeNull();
  });

  it("reads nothing, writes nothing, with hideOthers false or absent", () => {
    window.localStorage.setItem("kraken.hideOthers", "false");
    expect(readFleetNodes()).toEqual([]);
    expect(window.localStorage.getItem(FLEET_NODES_KEY)).toBeNull();
    expect(window.localStorage.getItem("kraken.hideOthers")).toBeNull();
    expect(readFleetNodes()).toEqual([]);
  });

  it("a present key wins over the legacy one, whatever it holds", () => {
    window.localStorage.setItem(FLEET_NODES_KEY, "[]");
    window.localStorage.setItem("kraken.hideOthers", "true");
    expect(readFleetNodes()).toEqual([]);
  });

  it("malformed JSON, a non-array, or a throwing storage read as no selection", () => {
    window.localStorage.setItem(FLEET_NODES_KEY, "{not json");
    expect(readFleetNodes()).toEqual([]);
    window.localStorage.setItem(FLEET_NODES_KEY, JSON.stringify({ local: true }));
    expect(readFleetNodes()).toEqual([]);
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(readFleetNodes()).toEqual([]);
  });

  it("writes the JSON list and swallows a throwing storage", () => {
    writeFleetNodes(["local", "nB"]);
    expect(window.localStorage.getItem(FLEET_NODES_KEY)).toBe(JSON.stringify(["local", "nB"]));
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(() => writeFleetNodes(["nB"])).not.toThrow();
  });
});

describe("jobNodeKey", () => {
  it("is local without an owner, the owner's id with one", () => {
    expect(jobNodeKey({ owner: undefined })).toBe(LOCAL_NODE);
    expect(jobNodeKey({ owner: { node_id: "nB", label: "x", operator: "", online: true } })).toBe("nB");
  });
});

describe("fleetButtonLabel", () => {
  const nodes = [node("nB", "MJ's MacBook Pro"), node("nC", "kitchen-mac", false)];

  it("reads all machines with no selection", () => {
    expect(fleetButtonLabel([], nodes)).toBe("all machines");
    expect(fleetButtonLabel([], [])).toBe("all machines");
  });

  it("reads this Mac for exactly the local key", () => {
    expect(fleetButtonLabel([LOCAL_NODE], nodes)).toBe("this Mac");
  });

  it("names a lone sibling by its registry label, else its id", () => {
    expect(fleetButtonLabel(["nB"], nodes)).toBe("MJ's MacBook Pro");
    expect(fleetButtonLabel(["nGONE"], nodes)).toBe("nGONE");
    expect(fleetButtonLabel(["nB"], [node("nB", "")])).toBe("nB");
  });

  it("counts N of M with M the registry plus this Mac, stale keys included in N", () => {
    expect(fleetButtonLabel([LOCAL_NODE, "nB"], nodes)).toBe("2 of 3 machines");
    expect(fleetButtonLabel(["nB", "nC", "nGONE"], nodes)).toBe("3 of 3 machines");
    expect(fleetButtonLabel([LOCAL_NODE, "nGONE"], [])).toBe("2 of 1 machines");
  });
});
