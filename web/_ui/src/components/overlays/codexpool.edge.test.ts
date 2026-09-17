import { describe, expect, it } from "vitest";
import type { WebJob, WebUsage, WebUsageAccount } from "../../api/types.ts";
import { codexBars, pickUsageRow } from "./SteerOverlay.tsx";
import { usagePanelTitle } from "./UsageOverlay.tsx";

// codexpool.edge.test.ts — the EDGE dimension of the `pickUsageRow` row of
// slice WIRE in artifacts/50-contract.md: the undefined answers, the lane that
// must not fall back to the other one, and the slug that names a slot in BOTH
// pools.
//
// Authored BLIND from the frozen contract. SteerOverlay.tsx, UsageOverlay.tsx
// and the Go side of the wire were never read by any route; the only
// implementation file touched is this import, which the contract freezes as an
// exported signature. What WAS read: the contract, 50-components.md,
// 52-testing-plan.md, the pre-existing SteerOverlayAccount.test.tsx (for
// idioms) and web/_ui/src/api/types.ts, which the contract cites.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths. What lives here is the other half: every shape that must answer
// `undefined`, and the collision that makes the two arrays a real split rather
// than a rename.
//
// This file is `.ts` rather than `.tsx` by assignment, so it renders no
// component; the accounts-overlay and steer-panel RENDERING obligations are
// therefore out of its reach and are listed as such in the author's report.

// edgeCxpRow builds a usage row. `email` is what every assertion below
// discriminates on, because the two pools' colliding rows share a slug and
// differ in nothing else a test can see.
function edgeCxpRow(slug: string, email: string, percent: number): WebUsageAccount {
  return {
    slug,
    email,
    tier: "plus",
    session: { percent },
    weekly: { percent },
    fetched_at: 1_700_000_000_000,
  };
}

// edgeCxpUsage assembles a poll payload. Both arrays are explicit so a case can
// pass `undefined` for either and mean it — an older server omits
// `codex_accounts` entirely, and that absence must read as "no codex pool"
// rather than as a reason to look in the other array.
function edgeCxpUsage(
  accounts: WebUsageAccount[] | undefined,
  codexAccounts: WebUsageAccount[] | undefined,
): WebUsage {
  return {
    available: true,
    fetched_at: 1_700_000_000_000,
    age_seconds: 5,
    daemon_ok: true,
    accounts,
    codex_accounts: codexAccounts,
    rotation_mode: "auto",
  };
}

// edgeCxpJob is the narrow slice the contract's signature takes.
function edgeCxpJob(agent: string | undefined, slug: string | undefined): Pick<WebJob, "agent" | "account"> {
  return {
    agent,
    account: slug === undefined ? undefined : { slug, email: slug + "@example.test", at: 1_700_000_000_000 },
  };
}

const CLAUDE_ROW = edgeCxpRow("shared", "shared-claude@example.test", 11);
const CODEX_ROW = edgeCxpRow("shared", "shared-codex@example.test", 88);

describe("pickUsageRow — the undefined answers", () => {
  // No poll yet is the ordinary state for the first seconds of a board load,
  // and the panel's whole contract for it is "render no bars" — a fabricated
  // 0% would read as an account with all its quota left, which is the opposite
  // of what an unknown value means.
  it("answers undefined with no usage at all", () => {
    expect(pickUsageRow(undefined, edgeCxpJob("codex", "shared"))).toBeUndefined();
    expect(pickUsageRow(undefined, edgeCxpJob("", "shared"))).toBeUndefined();
  });

  // An unbound cell has no slug to join on. Both pools carry rows here, so a
  // lookup that fell back to "the first row" or to a `current` flag would find
  // one and label an unbound cell with somebody's account.
  it("answers undefined for a job with no account, whatever the pools hold", () => {
    const usage = edgeCxpUsage([CLAUDE_ROW], [CODEX_ROW]);
    expect(pickUsageRow(usage, edgeCxpJob("codex", undefined))).toBeUndefined();
    expect(pickUsageRow(usage, edgeCxpJob("", undefined))).toBeUndefined();
  });

  // A slug that matches nothing in its own pool is undefined even when the
  // OTHER pool has it. This is the state right after a `cxp remove`, and the
  // wrong answer here is the one that looks most plausible on screen.
  it("answers undefined when the slug is present only in the other pool", () => {
    const usage = edgeCxpUsage([CLAUDE_ROW], [CODEX_ROW]);
    expect(pickUsageRow(usage, edgeCxpJob("codex", "claude-only"))).toBeUndefined();
    expect(pickUsageRow(usage, edgeCxpJob("", "codex-only"))).toBeUndefined();
  });

  // The two arrays are each optional on the wire for different reasons —
  // `accounts` because the claude pass may not have run, `codex_accounts`
  // because the server may predate the feature — and neither absence is
  // permission to read the other.
  it("never falls back to the other array when its own is missing or empty", () => {
    const codexJob = edgeCxpJob("codex", "shared");
    const claudeJob = edgeCxpJob("", "shared");

    // codex_accounts absent entirely (an older server), accounts full
    expect(pickUsageRow(edgeCxpUsage([CLAUDE_ROW], undefined), codexJob)).toBeUndefined();
    // codex_accounts present but empty (no codex pool on this machine)
    expect(pickUsageRow(edgeCxpUsage([CLAUDE_ROW], []), codexJob)).toBeUndefined();
    // and the mirror image for a claude job
    expect(pickUsageRow(edgeCxpUsage(undefined, [CODEX_ROW]), claudeJob)).toBeUndefined();
    expect(pickUsageRow(edgeCxpUsage([], [CODEX_ROW]), claudeJob)).toBeUndefined();
  });

  // Nothing anywhere is still undefined rather than a throw: the poll can come
  // back available with both pools empty, and the panel renders around it.
  it("answers undefined when both pools are empty", () => {
    expect(pickUsageRow(edgeCxpUsage([], []), edgeCxpJob("codex", "shared"))).toBeUndefined();
    expect(pickUsageRow(edgeCxpUsage([], []), edgeCxpJob("", "shared"))).toBeUndefined();
  });
});

describe("pickUsageRow — the colliding slug", () => {
  // The collision is the reason the wire carries two arrays rather than one: a
  // pool slug is a slugified email, so the same person's account really can
  // name a slot in both pools. Each agent must get ITS pool's row, and the
  // email is the only field that tells them apart.
  it("gives each agent its own pool's row for one slug present in both", () => {
    const usage = edgeCxpUsage([CLAUDE_ROW], [CODEX_ROW]);

    expect(pickUsageRow(usage, edgeCxpJob("codex", "shared"))?.email).toBe("shared-codex@example.test");
    expect(pickUsageRow(usage, edgeCxpJob("", "shared"))?.email).toBe("shared-claude@example.test");
    expect(pickUsageRow(usage, edgeCxpJob(undefined, "shared"))?.email).toBe("shared-claude@example.test");
    expect(pickUsageRow(usage, edgeCxpJob("claude", "shared"))?.email).toBe("shared-claude@example.test");
  });

  // The agent test is the literal string `codex`, matching the Go side's
  // `agent == "codex"`. Anything else is the claude lane, which is the
  // pre-feature behaviour and therefore the safe default for a value nobody
  // anticipated — and with the colliding fixture a loose match is visible,
  // because the wrong lane still returns a row.
  it("routes to the codex pool only on the exact string", () => {
    const usage = edgeCxpUsage([CLAUDE_ROW], [CODEX_ROW]);
    for (const agent of ["CODEX", "Codex", "codex ", " codex", "codexx", "claude", "", "gpt"]) {
      expect(pickUsageRow(usage, edgeCxpJob(agent, "shared"))?.email).toBe("shared-claude@example.test");
    }
  });

  // The match is on the slug, not on position: the row is found wherever it
  // sits in its array, and the neighbours with other slugs are not candidates.
  it("finds the row by slug rather than by position", () => {
    const usage = edgeCxpUsage(
      [edgeCxpRow("aaa", "aaa@example.test", 1), CLAUDE_ROW],
      [edgeCxpRow("zzz", "zzz@example.test", 2), edgeCxpRow("mmm", "mmm@example.test", 3), CODEX_ROW],
    );
    expect(pickUsageRow(usage, edgeCxpJob("codex", "shared"))?.email).toBe("shared-codex@example.test");
    expect(pickUsageRow(usage, edgeCxpJob("codex", "mmm"))?.email).toBe("mmm@example.test");
    expect(pickUsageRow(usage, edgeCxpJob("", "aaa"))?.email).toBe("aaa@example.test");
  });
});

// edgeCxpWindowed builds a row with whichever of the two windows a case needs.
// `undefined` for either means the row simply does not carry it — the shape
// `CodexSlotUsage` produces for a slot whose evidence named only one window.
function edgeCxpWindowed(
  weekly: { percent: number; resets_at?: number } | undefined,
  session: { percent: number; resets_at?: number } | undefined,
  extra: Record<string, unknown> = {},
): WebUsageAccount {
  // Amendment 3 (lead, after review round 1): a row with no `fetched_at`
  // draws no bars, so a windowed fixture is a MEASURED row by default; a test
  // about the unmeasured case passes `fetched_at: 0` through `extra`.
  return { slug: "tmp-1", email: "tmp-1@example.test", fetched_at: 1_758_067_680_000, weekly, session, ...extra };
}

describe("codexBars — which bars a codex row draws", () => {
  // An absent row is no bars, never a fabricated 0%: the panel renders this
  // whenever the poll has not yet carried the cell's slot, and a zero bar reads
  // as an account with all its quota left — the opposite of "unknown".
  it("draws nothing for an undefined row", () => {
    expect(codexBars(undefined)).toEqual([]);
  });

  // A row with no window at all is the same answer by the same rule: no weekly
  // percent means no weekly bar, and no session percent means the session
  // conjunct cannot hold either.
  it("draws nothing for a row carrying neither window", () => {
    expect(codexBars(edgeCxpWindowed(undefined, undefined))).toEqual([]);
  });

  // THE case this function exists for. `CodexSlotUsage` copies a lone weekly
  // window into `Session` so the binder's selector — which excludes a row with
  // no session — can still rank the slot. That copy is an artifact of the
  // SELECTOR's requirements, and drawing it would show one measurement twice
  // under two names, as though the account had two independent budgets.
  it("draws one bar when the session window is the weekly copy", () => {
    const bars = codexBars(edgeCxpWindowed({ percent: 37.5, resets_at: 1790208555000 }, { percent: 37.5, resets_at: 1790208555000 }));
    expect(bars).toEqual([{ label: "weekly", percent: 37.5 }]);
  });

  // Two genuinely different windows draw two bars, weekly FIRST — the weekly
  // wall is the one that ends a cell's day, so it leads.
  it("draws weekly then session when the percents differ", () => {
    const bars = codexBars(edgeCxpWindowed({ percent: 31, resets_at: 1790208555000 }, { percent: 7, resets_at: 1790208555000 }));
    expect(bars).toEqual([
      { label: "weekly", percent: 31 },
      { label: "session", percent: 7 },
    ]);
  });

  // The difference may be in the RESET alone: two windows can sit at the same
  // percent while one reopens in hours and the other in days, and collapsing
  // them on the percent alone would hide the short window entirely.
  it("draws both when only the reset stamps differ", () => {
    const bars = codexBars(edgeCxpWindowed({ percent: 12, resets_at: 1790900000000 }, { percent: 12, resets_at: 1790208555000 }));
    expect(bars).toEqual([
      { label: "weekly", percent: 12 },
      { label: "session", percent: 12 },
    ]);
  });

  // No fable bar. The claude panel draws one per `models` entry; codex has no
  // per-model window and `CodexSlotUsage` leaves `Models` nil, so a row that
  // somehow carried one must still not produce a third bar.
  it("never draws a model bar", () => {
    const bars = codexBars(
      edgeCxpWindowed({ percent: 31 }, { percent: 7 }, { models: [{ name: "Fable", percent: 61 }] }),
    );
    expect(bars.map((b) => b.label)).toEqual(["weekly", "session"]);
    expect(bars.some((b) => b.percent === 61)).toBe(false);
  });
});

describe("usagePanelTitle", () => {
  // The title is the panel's only statement of what it is showing, so it must
  // stop claiming to be the claude pool the moment a codex row exists — and
  // must NOT claim two pools on a machine that has one.
  it("names both pools only when codex rows exist", () => {
    expect(usagePanelTitle(edgeCxpUsage([CLAUDE_ROW], [CODEX_ROW]))).toBe("usage — account pools");
    expect(usagePanelTitle(edgeCxpUsage([], [CODEX_ROW]))).toBe("usage — account pools");
  });

  // Three ways to have no codex pool — the key absent (an older server), the
  // array empty (no pool on this machine), and no poll at all — render the
  // title the board has always had.
  it("keeps the claude-only title when there are no codex rows", () => {
    expect(usagePanelTitle(edgeCxpUsage([CLAUDE_ROW], undefined))).toBe("usage — claude code pool");
    expect(usagePanelTitle(edgeCxpUsage([CLAUDE_ROW], []))).toBe("usage — claude code pool");
    expect(usagePanelTitle(undefined)).toBe("usage — claude code pool");
  });
});
