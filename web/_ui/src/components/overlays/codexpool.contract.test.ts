// codexpool.contract.test.ts — athena-logic's BLIND tests for
// artifacts/50-contract.md, slice WIRE, TypeScript half. Dimension: LOGIC —
// the stated invariants and happy paths of the three functions the contract
// freezes as exports: `pickUsageRow`, and (amendment 1) `codexBars` and
// `usagePanelTitle`. Missing/undefined inputs are athena-edge's.
//
// Authored blind: SteerOverlay.tsx and UsageOverlay.tsx were never opened.
// Every signature here comes from the contract row verbatim; `WebUsage` and
// `WebJob` come from api/types.ts, which the contract cites as the type
// definition it extends.
//
// What remains unreachable from a .ts file is the RENDERED overlay beyond
// those three functions — the `codex` section's markup, the `no usage yet`
// chip, the absence of `make main` / `ccp` for codex rows — which amendment 1
// records as unpinned by decision. It is in the author's report, not probed
// here.

import { describe, expect, it } from "vitest";
import type { WebJob, WebUsage, WebUsageAccount } from "../../api/types.ts";
import { codexBars, pickUsageRow } from "./SteerOverlay.tsx";
import { usagePanelTitle } from "./UsageOverlay.tsx";

// The same slug lives in BOTH pools. That is not a contrived fixture: a pool
// slot is named after its directory, and `tmp-1` is exactly the kind of name
// carl gives a scratch slot in either pool. The join must be by agent, not by
// slug alone.
const COLLIDING = "tmp-1";

const claudeRow: WebUsageAccount = {
  slug: COLLIDING,
  email: "claude-slot@example.com",
  session: { percent: 11 },
};

const codexRow: WebUsageAccount = {
  slug: COLLIDING,
  email: "codex-slot@example.com",
  tier: "pro",
  session: { percent: 40 },
  weekly: { percent: 40 },
};

// `codex_accounts` and `codex_fetched_at` are the two optional keys the
// contract adds to WebUsage; declaring them here is itself the compile-level
// assertion that api/types.ts carries them.
const usage: WebUsage = {
  available: true,
  fetched_at: 1_700_000_000_000,
  age_seconds: 30,
  daemon_ok: true,
  accounts: [claudeRow],
  codex_accounts: [codexRow],
  codex_fetched_at: 1_700_000_777_000,
  rotation_mode: "auto",
};

function job(agent: string | undefined, slug: string | undefined): Pick<WebJob, "agent" | "account"> {
  return {
    agent,
    account: slug === undefined ? undefined : { slug, email: "", at: 0 },
  };
}

describe("pickUsageRow — the pool a job's bars come from", () => {
  // Obligation: "For job.agent === \"codex\" the row is found by slug in
  // usage.codex_accounts only […]. With the same slug present in both arrays
  // each agent gets its own pool's row."
  it("gives a codex job its own pool's row when the slug exists in both", () => {
    const row = pickUsageRow(usage, job("codex", COLLIDING));
    expect(row).toBe(codexRow);
    expect(row?.email).toBe("codex-slot@example.com");
  });

  // Obligation: "otherwise in usage.accounts only." An absent `agent` is the
  // claude default on the wire, so it must join the claude pool exactly as the
  // explicit spelling does — this is the whole claude-path-unchanged rule at
  // the one place the UI could quietly break it.
  it("gives a claude job the claude pool's row, whether agent is \"claude\" or absent", () => {
    for (const agent of ["claude", "", undefined]) {
      const row = pickUsageRow(usage, job(agent, COLLIDING));
      expect(row).toBe(claudeRow);
      expect(row?.email).toBe("claude-slot@example.com");
    }
  });

  // Obligation: the row is found in one array "only" — so a job whose slug is
  // absent from its OWN pool is undefined even when the other pool holds it.
  // A fallback here would draw a claude account's usage bars under a codex
  // cell: a number that is real, current, and about a different account.
  it("never falls back to the other pool", () => {
    const codexOnly: WebUsage = { ...usage, accounts: [] };
    expect(pickUsageRow(codexOnly, job("claude", COLLIDING))).toBeUndefined();

    const claudeOnly: WebUsage = { ...usage, codex_accounts: [] };
    expect(pickUsageRow(claudeOnly, job("codex", COLLIDING))).toBeUndefined();
  });

  // Obligation: "Missing […] account or no match is undefined." An unbound
  // job runs on the machine login, which has no pool row at all — fabricating
  // one would put a 0% bar under a cell whose real spend is invisible here.
  it("answers undefined for an unbound job and for a slug in neither pool", () => {
    expect(pickUsageRow(usage, job("codex", undefined))).toBeUndefined();
    expect(pickUsageRow(usage, job("codex", "no-such-slot"))).toBeUndefined();
    expect(pickUsageRow(usage, job("claude", "no-such-slot"))).toBeUndefined();
  });
});

// ===========================================================================
// AMENDMENT 1 — the two exported functions frozen after the first join.
// Still authored blind: SteerOverlay.tsx and UsageOverlay.tsx were not opened.
// ===========================================================================

// A lone weekly window, copied into `session` by the usage pass exactly as C2
// requires — identical percent AND identical resets_at. This is what EVERY row
// on today's codex build looks like, so it is the case that decides whether the
// panel draws one bar or two identical ones.
// Amendment 3 (lead, after review round 1): a row with no `fetched_at` draws no
// bars, so every measured-row fixture here carries the record's stamp.
const loneWeekly: WebUsageAccount = {
  slug: "tmp-1",
  email: "codex-slot@example.com",
  fetched_at: 1_758_067_680_000,
  weekly: { percent: 12, resets_at: 1_790_208_555_000 },
  session: { percent: 12, resets_at: 1_790_208_555_000 },
};

// A genuinely shorter window: a different percent and a different reset.
const twoWindows: WebUsageAccount = {
  slug: "tmp-2",
  email: "codex-slot-two@example.com",
  fetched_at: 1_758_067_680_000,
  weekly: { percent: 12, resets_at: 1_790_208_555_000 },
  session: { percent: 40, resets_at: 1_790_200_000_000 },
};

describe("codexBars — which windows a codex row draws", () => {
  // Obligation: "`weekly` first when the row has a weekly percent; then
  // `session` only when the row has a session percent AND it differs from
  // weekly in `percent` or in `resets_at`."
  it("draws weekly alone when session is the copy of weekly", () => {
    expect(codexBars(loneWeekly)).toEqual([{ label: "weekly", percent: 12 }]);
  });

  // Same obligation, the other arm — and the order is part of it: weekly is
  // the window a codex account is actually rationed by, so it leads.
  it("draws weekly then session when the two windows genuinely differ", () => {
    expect(codexBars(twoWindows)).toEqual([
      { label: "weekly", percent: 12 },
      { label: "session", percent: 40 },
    ]);
  });

  // The obligation says "differs […] in `percent` OR in `resets_at`", so equal
  // percentages with different resets are still two real windows. Collapsing
  // them would hide a session window that is about to reopen behind a weekly
  // one that is not.
  it("draws both when the windows share a percent but not a reset", () => {
    const sameSince: WebUsageAccount = {
      ...loneWeekly,
      session: { percent: 12, resets_at: 1_790_200_000_000 },
    };
    expect(codexBars(sameSince)).toEqual([
      { label: "weekly", percent: 12 },
      { label: "session", percent: 12 },
    ]);
  });

  // Obligation: "an undefined row is []." An unbound codex cell has no pool
  // row at all, and an empty list is what lets the panel render the account
  // line with no bars rather than a fabricated 0%.
  it("draws nothing for a row that is not there", () => {
    expect(codexBars(undefined)).toEqual([]);
  });
});

describe("usagePanelTitle — what the accounts overlay calls itself", () => {
  // Obligation: "`usage — account pools` when `usage.codex_accounts` is
  // non-empty, else `usage — claude code pool`."
  //
  // The second arm is the claude-path-unchanged rule at the one string a human
  // actually reads: on a machine with no codex pool the panel must look exactly
  // as it did before this feature existed.
  it("names both pools when codex rows exist, and the claude pool alone when they do not", () => {
    expect(usagePanelTitle(usage)).toBe("usage — account pools");
    expect(usagePanelTitle({ ...usage, codex_accounts: [] })).toBe("usage — claude code pool");
  });
});
