// submit.contract.test.ts — the LOGIC dimension of "eliminate the queued
// status" on the board, authored blind from artifacts/50-contract.md before the
// SPA change existed.
//
// The MOVES table is the board's highest-consequence data structure: every
// entry starts a cell, stops an agent or archives real work. This change makes
// draft→working the SUBMIT — the drag that now spawns a live cell synchronously
// — and deletes three entries with the queued column. So this file enumerates
// ALL (from, to) pairs over the four surviving columns and asserts the exact
// surviving seven, rather than spot-checking the ones that were easy to think
// of.

import { describe, expect, it, vi } from "vitest";

// Exactly the five client calls the surviving table needs. dequeueJob and
// startJob are deliberately ABSENT: the contract deletes both from the client,
// and a moves.ts that still imports one fails here rather than shipping a dead
// call.
vi.mock("../api/client.ts", () => ({
  queueJob: vi.fn(async () => ({ ok: true })),
  doneJob: vi.fn(async () => ({ ok: true })),
  stopJob: vi.fn(async () => ({ ok: true })),
  archiveJob: vi.fn(async () => ({ ok: true })),
  reactivateJob: vi.fn(async () => ({ ok: true })),
}));

import * as client from "../api/client.ts";
import { ARCHIVED, MOVES, isLegalTarget, resolveMove } from "./moves.ts";
import type { DropColumn } from "./moves.ts";
import { COLUMNS } from "../hooks/useBoard.ts";
import type { Column } from "../hooks/useBoard.ts";

const CONTRACT_DROP_TARGETS: DropColumn[] = [...COLUMNS, ARCHIVED];

/**
 * The surviving transition table. draft→working is new (it posts /api/queue —
 * Q4 keeps the URL and renames the handler); draft→queued, queued→draft and
 * queued→working are gone; every working/blocked/done entry is unchanged.
 */
const CONTRACT_LISTED: Array<[Column, DropColumn, string]> = [
  ["draft", "working", "/api/queue"],
  ["working", "blocked", "/api/stop"],
  ["blocked", "done", "/api/done"],
  ["blocked", "working", "steer:unblock"],
  ["done", "blocked", "/api/reactivate"],
  ["done", "working", "steer:reactivate"],
  ["done", ARCHIVED, "/api/archive"],
];

const contractKey = (from: string, to: string) => `${from}→${to}`;
const CONTRACT_LISTED_SET = new Set(
  CONTRACT_LISTED.map(([f, t]) => contractKey(f, t)),
);

describe("board columns", () => {
  it("is exactly the four surviving states, in board order", () => {
    expect([...COLUMNS]).toEqual(["draft", "working", "blocked", "done"]);
  });

  it("has no queued column", () => {
    expect(COLUMNS).not.toContain("queued");
    expect(ARCHIVED).not.toBe("queued");
  });
});

describe("resolveMove", () => {
  it.each(CONTRACT_LISTED)("%s → %s resolves to %s", (from, to, expected) => {
    const move = resolveMove(from, to);
    expect(move).not.toBeNull();
    if (expected.startsWith("steer:")) {
      expect(move).toEqual({
        kind: "steer",
        mode: expected.slice("steer:".length),
        // done→working is the only drag that refuses an empty message: it
        // resurrects a finished job, and "" would restart it saying nothing.
        requireText: expected === "steer:reactivate",
      });
    } else {
      expect(move).toMatchObject({ kind: "post", endpoint: expected });
    }
  });

  it("makes draft → working the submit, posting /api/queue", () => {
    const move = resolveMove("draft", "working");
    expect(move).toMatchObject({ kind: "post", endpoint: "/api/queue" });
  });

  it("resolves every same-column drop to a reorder", () => {
    for (const c of COLUMNS) {
      expect(resolveMove(c, c)).toEqual({ kind: "reorder" });
    }
  });

  it("is a no-op for every pair not in the table", () => {
    const unlisted: string[] = [];
    for (const from of COLUMNS) {
      for (const to of CONTRACT_DROP_TARGETS) {
        if (from === to) continue; // same column is a reorder, covered above
        if (CONTRACT_LISTED_SET.has(contractKey(from, to))) continue;
        unlisted.push(contractKey(from, to));
        expect(resolveMove(from, to)).toBeNull();
      }
    }
    // 4 columns × 5 targets − 4 same-column − 7 listed = 9
    expect(unlisted).toHaveLength(9);
  });

  it("never posts a retired endpoint from any pair", () => {
    const retired = ["/api/dequeue", "/api/start"];
    for (const from of COLUMNS) {
      for (const to of CONTRACT_DROP_TARGETS) {
        const move = resolveMove(from, to);
        if (move?.kind !== "post") continue;
        expect(retired).not.toContain(move.endpoint);
      }
    }
  });

  it("treats queued as a bogus label in both directions", () => {
    for (const to of CONTRACT_DROP_TARGETS) {
      expect(resolveMove("queued" as Column, to)).toBeNull();
    }
    for (const from of COLUMNS) {
      expect(resolveMove(from, "queued" as DropColumn)).toBeNull();
    }
  });
});

describe("post moves", () => {
  it.each([
    ["draft", "working", "queueJob"],
    ["working", "blocked", "stopJob"],
    ["blocked", "done", "doneJob"],
    ["done", "blocked", "reactivateJob"],
    ["done", ARCHIVED, "archiveJob"],
  ] as Array<[Column, DropColumn, keyof typeof client]>)(
    "%s → %s calls %s with the slug",
    async (from, to, fn) => {
      const move = resolveMove(from, to);
      expect(move?.kind).toBe("post");
      if (move?.kind !== "post") return;
      await move.run("some-job");
      expect(client[fn]).toHaveBeenCalledWith(
        "some-job",
        // done→blocked reactivates with an explicitly empty message
        ...(fn === "reactivateJob" ? [""] : []),
      );
    },
  );
});

describe("MOVES adjacency", () => {
  it("lights up exactly the pairs that do something", () => {
    for (const from of COLUMNS) {
      for (const to of CONTRACT_DROP_TARGETS) {
        const highlighted = MOVES[from].includes(to);
        expect(highlighted).toBe(
          CONTRACT_LISTED_SET.has(contractKey(from, to)),
        );
      }
    }
  });

  it("never lists a column as its own target — a reorder shows itself", () => {
    for (const c of COLUMNS) expect(MOVES[c]).not.toContain(c);
  });

  it("names no queued target and holds no queued source", () => {
    for (const c of COLUMNS) {
      expect(MOVES[c]).not.toContain("queued");
    }
    expect(Object.keys(MOVES).sort()).toEqual(
      ["blocked", "done", "draft", "working"],
    );
  });

  it("isLegalTarget agrees with the table, and is false with nothing in the air", () => {
    expect(isLegalTarget(null, "working")).toBe(false);
    expect(isLegalTarget("draft", "working")).toBe(true);
    expect(isLegalTarget("draft", "draft")).toBe(false);
    expect(isLegalTarget("draft", ARCHIVED)).toBe(false);
    expect(isLegalTarget("done", ARCHIVED)).toBe(true);
    expect(isLegalTarget("draft", "queued" as DropColumn)).toBe(false);
  });
});
