// The MOVES table is the board's highest-consequence data structure: each of
// its ten entries starts a cell, stops an agent, or archives real work, and an
// eleventh entry that "seemed reasonable" would be a silent state change on a
// user's job. So this file enumerates ALL 30 (from, to) pairs and asserts the
// exact ten, rather than spot-checking the ones that were easy to think of.

import { describe, expect, it, vi } from "vitest";

vi.mock("../api/client.ts", () => ({
  queueJob: vi.fn(async () => ({ ok: true })),
  dequeueJob: vi.fn(async () => ({ ok: true })),
  doneJob: vi.fn(async () => ({ ok: true })),
  startJob: vi.fn(async () => ({ ok: true })),
  stopJob: vi.fn(async () => ({ ok: true })),
  archiveJob: vi.fn(async () => ({ ok: true })),
  reactivateJob: vi.fn(async () => ({ ok: true })),
}));

import * as client from "../api/client.ts";
import { ARCHIVED, MOVES, isLegalTarget, resolveMove } from "./moves.ts";
import type { DropColumn } from "./moves.ts";
import { COLUMNS } from "../hooks/useBoard.ts";
import type { Column } from "../hooks/useBoard.ts";

const DROP_TARGETS: DropColumn[] = [...COLUMNS, ARCHIVED];

/** The table from artifacts/53-port-checklist.md, transcribed by hand. */
const LISTED: Array<[Column, DropColumn, string]> = [
  ["draft", "queued", "/api/queue"],
  ["queued", "draft", "/api/dequeue"],
  ["blocked", "done", "/api/done"],
  ["queued", "working", "/api/start"],
  ["working", "blocked", "/api/stop"],
  ["blocked", "working", "steer:unblock"],
  ["done", "blocked", "/api/reactivate"],
  ["done", "working", "steer:reactivate"],
  ["done", ARCHIVED, "/api/archive"],
];

const listedKey = (from: string, to: string) => `${from}→${to}`;
const listedSet = new Set(LISTED.map(([f, t]) => listedKey(f, t)));

describe("resolveMove", () => {
  it.each(LISTED)("%s → %s resolves to %s", (from, to, expected) => {
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

  it("resolves every same-column drop to a reorder", () => {
    for (const c of COLUMNS) {
      expect(resolveMove(c, c)).toEqual({ kind: "reorder" });
    }
  });

  it("is a no-op for every pair not in the table", () => {
    const unlisted: string[] = [];
    for (const from of COLUMNS) {
      for (const to of DROP_TARGETS) {
        if (from === to) continue; // same column is a reorder, covered above
        if (listedSet.has(listedKey(from, to))) continue;
        unlisted.push(listedKey(from, to));
        expect(resolveMove(from, to)).toBeNull();
      }
    }
    // 5 columns × 6 targets − 5 same-column − 9 listed = 16
    expect(unlisted).toHaveLength(16);
  });

  it("never invents a nearest-legal fallback for a bogus target", () => {
    expect(resolveMove("draft", "nonsense" as DropColumn)).toBeNull();
    expect(resolveMove("done", "" as DropColumn)).toBeNull();
    // an inherited Object.prototype member must not resolve as a move
    expect(resolveMove("draft", "constructor" as DropColumn)).toBeNull();
    expect(resolveMove("draft", "toString" as DropColumn)).toBeNull();
  });
});

describe("post moves", () => {
  it.each([
    ["draft", "queued", "queueJob"],
    ["queued", "draft", "dequeueJob"],
    ["queued", "working", "startJob"],
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
      for (const to of DROP_TARGETS) {
        const highlighted = MOVES[from].includes(to);
        expect(highlighted).toBe(listedSet.has(listedKey(from, to)));
      }
    }
  });

  it("never lists a column as its own target — a reorder shows itself", () => {
    for (const c of COLUMNS) expect(MOVES[c]).not.toContain(c);
  });

  it("isLegalTarget agrees with the table, and is false with nothing in the air", () => {
    expect(isLegalTarget(null, "queued")).toBe(false);
    expect(isLegalTarget("draft", "queued")).toBe(true);
    expect(isLegalTarget("draft", "draft")).toBe(false);
    expect(isLegalTarget("draft", ARCHIVED)).toBe(false);
    expect(isLegalTarget("done", ARCHIVED)).toBe(true);
  });
});
