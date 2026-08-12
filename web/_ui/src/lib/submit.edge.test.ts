// submit.edge.test.ts — BLIND edge tests for "eliminate the queued status"
// (artifacts/50-contract.md, slice S2 web SPA). Authored from the frozen
// contract with no sight of the implementation.
//
// Dimension: boundaries, error paths, refusals. The MOVES table's edges are
// the whole subject: what is NOT a legal target, what endpoint is NOT posted,
// and what a bogus drop must resolve to. A stray eleventh entry here is a
// silent state change on a user's job — and, after this change, a POST to a
// route that returns 404.
//
// Deliberately mocks NOTHING: every assertion below reads the descriptor
// resolveMove returns and never calls move.run(), so no request is ever
// issued and no api/client export needs naming. That matters because the
// client's submit function may or may not keep its old name, and a mock
// factory would turn that open question into an import-time crash across
// this whole file.

import { describe, expect, it } from "vitest";

import { ARCHIVED, MOVES, isLegalTarget, resolveMove } from "./moves.ts";
import type { DropColumn } from "./moves.ts";
import { COLUMNS } from "../hooks/useBoard.ts";
import type { Column } from "../hooks/useBoard.ts";

const DROP_TARGETS: DropColumn[] = [...COLUMNS, ARCHIVED];

/**
 * The retired column name, kept in one place so every probe below spells it
 * the same way. Cast through `unknown` deliberately: once DropColumn drops
 * the member, a direct `as DropColumn` is a compile error under `strict`, and
 * this file has to keep typechecking in order to fail for the RIGHT reason.
 */
const QUEUED = "queued" as unknown as DropColumn;

/**
 * The only endpoints any transition may post after this change. `/api/dequeue`
 * and `/api/start` are deleted server-side, so a surviving reference is not a
 * stale-but-harmless call — it is a 404 the board would surface as a failed
 * drag.
 */
const ALLOWED_ENDPOINTS = new Set([
  "/api/queue",
  "/api/stop",
  "/api/done",
  "/api/reactivate",
  "/api/archive",
]);

/** Every (from, to) pair the board can produce, bogus targets excluded. */
function everyPair(): Array<[Column, DropColumn]> {
  const pairs: Array<[Column, DropColumn]> = [];
  for (const from of COLUMNS) {
    for (const to of DROP_TARGETS) pairs.push([from, to]);
  }
  return pairs;
}

describe("the queued column is gone from the board's vocabulary", () => {
  it("COLUMNS holds exactly the four live columns, in board order", () => {
    expect([...COLUMNS]).toEqual(["draft", "working", "blocked", "done"]);
  });

  it("ARCHIVED is not the retired column wearing another name", () => {
    expect(ARCHIVED).not.toBe("queued");
  });

  it("no column may be dropped onto queued", () => {
    for (const from of COLUMNS) {
      expect(resolveMove(from, QUEUED)).toBeNull();
      expect(isLegalTarget(from, QUEUED)).toBe(false);
    }
    expect(isLegalTarget(null, QUEUED)).toBe(false);
  });

  it("no column lists queued as a highlightable target", () => {
    for (const from of COLUMNS) {
      expect(MOVES[from]).not.toContain("queued");
    }
  });

  it("queued is not a source column either — MOVES has no such key", () => {
    expect(Object.keys(MOVES).sort()).toEqual(["blocked", "done", "draft", "working"]);
  });
});

describe("the two deleted endpoints are unreachable from the board", () => {
  it("no transition posts /api/dequeue or /api/start", () => {
    for (const [from, to] of everyPair()) {
      const move = resolveMove(from, to);
      if (move?.kind !== "post") continue;
      expect(
        move.endpoint,
        `${from} → ${to} posts a retired endpoint`,
      ).not.toBe("/api/dequeue");
      expect(
        move.endpoint,
        `${from} → ${to} posts a retired endpoint`,
      ).not.toBe("/api/start");
    }
  });

  it("every post move targets one of the five surviving endpoints", () => {
    for (const [from, to] of everyPair()) {
      const move = resolveMove(from, to);
      if (move?.kind !== "post") continue;
      expect(
        ALLOWED_ENDPOINTS.has(move.endpoint),
        `${from} → ${to} posts unexpected endpoint ${move.endpoint}`,
      ).toBe(true);
    }
  });
});

describe("draft's one exit", () => {
  it("draft → working is the only legal target a draft has", () => {
    expect([...MOVES.draft]).toEqual(["working"]);
  });

  it("draft → working is the submit, and posts the kept /api/queue URL", () => {
    const move = resolveMove("draft", "working");
    expect(move).toMatchObject({ kind: "post", endpoint: "/api/queue" });
    expect(isLegalTarget("draft", "working")).toBe(true);
  });

  it("a draft cannot be dropped anywhere else, archive included", () => {
    for (const to of DROP_TARGETS) {
      if (to === "working") continue;
      if (to === "draft") continue; // same column is a reorder, asserted below
      expect(resolveMove("draft", to), `draft → ${to}`).toBeNull();
    }
    expect(resolveMove("draft", "draft")).toEqual({ kind: "reorder" });
  });
});

describe("bogus targets never resolve", () => {
  const bogus = (s: string) => s as unknown as DropColumn;

  it("never invents a nearest-legal fallback", () => {
    expect(resolveMove("draft", bogus("nonsense"))).toBeNull();
    expect(resolveMove("done", bogus(""))).toBeNull();
    // an inherited Object.prototype member must not resolve as a move
    expect(resolveMove("draft", bogus("constructor"))).toBeNull();
    expect(resolveMove("draft", bogus("toString"))).toBeNull();
    expect(resolveMove("working", bogus("__proto__"))).toBeNull();
  });

  it("a retired source column resolves nothing either", () => {
    const queuedSource = "queued" as unknown as Column;
    for (const to of DROP_TARGETS) {
      expect(resolveMove(queuedSource, to), `queued → ${to}`).toBeNull();
    }
    expect(isLegalTarget(queuedSource, "working")).toBe(false);
  });

  it("case is not a loophole", () => {
    expect(resolveMove("draft", bogus("Working"))).toBeNull();
    expect(resolveMove("draft", bogus("Queued"))).toBeNull();
  });
});

describe("the surviving transitions are untouched", () => {
  it("keeps the four non-draft moves the contract leaves alone", () => {
    expect(resolveMove("working", "blocked")).toMatchObject({ kind: "post", endpoint: "/api/stop" });
    expect(resolveMove("blocked", "done")).toMatchObject({ kind: "post", endpoint: "/api/done" });
    expect(resolveMove("done", "blocked")).toMatchObject({ kind: "post", endpoint: "/api/reactivate" });
    expect(resolveMove("done", ARCHIVED)).toMatchObject({ kind: "post", endpoint: "/api/archive" });
    expect(resolveMove("blocked", "working")).toMatchObject({ kind: "steer", mode: "unblock" });
    expect(resolveMove("done", "working")).toMatchObject({ kind: "steer", mode: "reactivate" });
  });

  it("every same-column drop is still a reorder, and no column targets itself", () => {
    for (const c of COLUMNS) {
      expect(resolveMove(c, c)).toEqual({ kind: "reorder" });
      expect(MOVES[c]).not.toContain(c);
    }
  });

  it("MOVES and resolveMove agree on every pair", () => {
    for (const [from, to] of everyPair()) {
      if (from === (to as string)) continue; // a reorder shows itself, not as an adjacency
      expect(
        MOVES[from].includes(to),
        `MOVES[${from}] disagrees with resolveMove for target ${to}`,
      ).toBe(resolveMove(from, to) !== null);
    }
  });
});
