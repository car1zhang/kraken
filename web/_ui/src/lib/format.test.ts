import { describe, expect, it, vi } from "vitest";

import type { WebJob, WebJobLoad } from "../api/types.ts";
import {
  age,
  bucketJobs,
  columnFor,
  countdown,
  docQuestionsOpenP0,
  flagCount,
  loadBadgeTitle,
  loadRatioText,
  loadROI,
  logTime,
  settledJob,
  usageColorClass,
  usageCountdown,
  wakeupLabel,
} from "./format.ts";

const job = (over: Partial<WebJob> = {}): WebJob => ({
  slug: "j",
  repo: "kraken",
  state: "draft",
  live: false,
  mtime: 0,
  ...over,
});

// stateColor was deleted with the HUD redesign: the card's state renders as a
// text token from JobCard's STATUS_TEXT (50-contract.md, JobCard band 1/3 row
// as amended), and ArchivedCard hardcodes its own `--state`, so nothing read
// the mapping any more.

describe("age", () => {
  it("formats seconds-resolution stamps", () => {
    const now = 1_700_000_000_000;
    vi.spyOn(Date, "now").mockReturnValue(now);
    const secs = now / 1000;
    expect(age(secs - 5)).toBe("5s");
    expect(age(secs - 90)).toBe("1m");
    expect(age(secs - 7200)).toBe("2h");
    expect(age(secs - 86400 * 3)).toBe("3d");
    // never negative: a stamp from the future reads as "now"
    expect(age(secs + 60)).toBe("0s");
    vi.restoreAllMocks();
  });

  it("renders an absent stamp as empty rather than as 1970", () => {
    expect(age(0)).toBe("");
    expect(age(undefined)).toBe("");
  });
});

describe("countdown / wakeupLabel", () => {
  it("floors at zero and reads 'due' once past", () => {
    expect(countdown(92)).toBe("1m 32s");
    expect(countdown(-5)).toBe("0m 0s");
    const now = 1_700_000_000_000;
    vi.spyOn(Date, "now").mockReturnValue(now);
    expect(wakeupLabel(now + 92_000)).toBe("1m 32s");
    expect(wakeupLabel(now - 1)).toBe("due");
    vi.restoreAllMocks();
  });
});

describe("settledJob", () => {
  it("is done or archived, and nothing else", () => {
    expect(settledJob(job({ state: "done" }))).toBe(true);
    expect(settledJob(job({ archived: true, state: "draft" }))).toBe(true);
    expect(settledJob(job({ state: "blocked" }))).toBe(false);
    expect(settledJob(null)).toBe(false);
    expect(settledJob(undefined)).toBe(false);
  });
});

describe("docQuestionsOpenP0", () => {
  const questions = [
    { id: "Q1", text: "a", importance: "P0" },
    { id: "Q2", text: "b", importance: "P0", answer: "yes" },
    { id: "Q3", text: "c", importance: "P1" },
  ];

  it("counts unanswered P0s only", () => {
    expect(docQuestionsOpenP0(job({ doc_questions: questions }))).toBe(1);
  });

  it("is zero for a settled job — its questions have no board presence", () => {
    expect(docQuestionsOpenP0(job({ state: "done", doc_questions: questions }))).toBe(0);
    expect(docQuestionsOpenP0(job({ archived: true, doc_questions: questions }))).toBe(0);
  });

  it("is zero when the field is absent (an older server)", () => {
    expect(docQuestionsOpenP0(job())).toBe(0);
  });
});

describe("usageColorClass", () => {
  it("thresholds at 50 and 80", () => {
    expect(usageColorClass(0)).toBe("u-ok");
    expect(usageColorClass(49.9)).toBe("u-ok");
    expect(usageColorClass(50)).toBe("u-warn");
    expect(usageColorClass(79.9)).toBe("u-warn");
    expect(usageColorClass(80)).toBe("u-danger");
  });
});

describe("usageCountdown", () => {
  it("drops to day/hour/minute granularity", () => {
    const now = 1_700_000_000_000;
    vi.spyOn(Date, "now").mockReturnValue(now);
    expect(usageCountdown(now - 1)).toBe("now");
    expect(usageCountdown(now + 8 * 60_000)).toBe("8m");
    expect(usageCountdown(now + (3 * 3600 + 12 * 60) * 1000)).toBe("3h12m");
    expect(usageCountdown(now + 4 * 86400_000)).toBe("4d");
    vi.restoreAllMocks();
  });
});

describe("columnFor", () => {
  it("is identity for the five columns and draft for anything else", () => {
    expect(columnFor("working")).toBe("working");
    expect(columnFor("archived")).toBe("draft");
    expect(columnFor("")).toBe("draft");
  });
});

describe("bucketJobs", () => {
  it("buckets by state, keeps server order, and drawers the archived", () => {
    const b = bucketJobs([
      job({ slug: "a", state: "draft" }),
      job({ slug: "b", state: "done" }),
      job({ slug: "c", state: "draft" }),
      job({ slug: "old", state: "done", archived: true }),
    ]);
    expect(b.byColumn.draft.map((j) => j.slug)).toEqual(["a", "c"]);
    expect(b.byColumn.done.map((j) => j.slug)).toEqual(["b"]);
    expect(b.archived.map((j) => j.slug)).toEqual(["old"]);
    expect(b.byColumn.working).toEqual([]);
  });

  it("puts an UNHEALTHY cell in blocked whatever its file state says", () => {
    // It needs a retry and the daemon is not going to fix it — app.js:614.
    const b = bucketJobs([job({ slug: "sick", state: "working", health: "cell is gone" })]);
    expect(b.byColumn.working).toEqual([]);
    expect(b.byColumn.blocked.map((j) => j.slug)).toEqual(["sick"]);
  });

});

describe("flagCount", () => {
  it("counts the header flag as questions + health + open P0s", () => {
    expect(
      flagCount([
        job({ slug: "q", question: { tool_use_id: "t" } }),
        job({ slug: "h", health: "pane gone" }),
        job({
          slug: "p",
          doc_questions: [
            { id: "Q1", text: "a", importance: "P0" },
            { id: "Q2", text: "b", importance: "P0" },
          ],
        }),
        // settled: contributes nothing, even with open P0s
        job({
          slug: "d",
          state: "done",
          doc_questions: [{ id: "Q1", text: "a", importance: "P0" }],
        }),
      ]),
    ).toBe(4);
  });

  it("ignores archived jobs entirely — they have no board presence", () => {
    expect(
      flagCount([job({ slug: "old", archived: true, question: { tool_use_id: "t" } })]),
    ).toBe(0);
  });
});

describe("loadROI", () => {
  // A full wire object, so the "cleared" and half-estimate cases below are the
  // shape a server actually sends rather than the helper's structural minimum.
  const wire = (unblock: number, scope: number): WebJobLoad => ({
    unblock,
    scope,
    note: "",
    at: 1_769_040_000_000,
  });

  it("lands the shared contract's worked examples in their stated bands", () => {
    expect(loadROI({ unblock: 2, scope: 8 })).toEqual({ roi: 4, band: "hot" });
    expect(loadROI({ unblock: 5, scope: 5 })).toEqual({ roi: 1, band: "even" });
    expect(loadROI({ unblock: 8, scope: 2 })).toEqual({ roi: 0.25, band: "cold" });
    expect(loadROI({ unblock: 3, scope: 5 })?.band).toBe("warm");
  });

  it("puts each boundary value in the HIGHER band", () => {
    // The bands are closed-open, so the boundary itself belongs upward. Tested
    // on the exact ratio, which is what the operator sees.
    expect(loadROI({ unblock: 2, scope: 6 })).toEqual({ roi: 3, band: "hot" }); // 3.0
    expect(loadROI({ unblock: 2, scope: 3 })).toEqual({ roi: 1.5, band: "warm" }); // 1.5
    expect(loadROI({ unblock: 4, scope: 3 })).toEqual({ roi: 0.75, band: "even" }); // 0.75
  });

  it("drops to the lower band just BELOW each boundary", () => {
    // Non-integer inputs on purpose: 1..10 integers cannot produce a ratio
    // arbitrarily close to 3.0 from below, and it is the comparison operator
    // being pinned here, not a value a server would send.
    expect(loadROI({ unblock: 1, scope: 2.999 })?.band).toBe("warm");
    expect(loadROI({ unblock: 1, scope: 1.499 })?.band).toBe("even");
    expect(loadROI({ unblock: 1, scope: 0.749 })?.band).toBe("cold");
  });

  it("returns null for no estimate at all", () => {
    expect(loadROI(undefined)).toBeNull();
    expect(loadROI(null)).toBeNull();
  });

  it("returns null for both-zero — defensively, since a clear drops the key", () => {
    // internal/api/jobload.go stores a clear as JobLoad{}, so `at` is 0,
    // webJobLoadFrom returns nil, and the omitempty pointer drops the key: this
    // object cannot arrive from the current server. Pinned anyway, because it
    // is the arm standing between a future zero and an "Infinity×" badge.
    expect(loadROI(wire(0, 0))).toBeNull();
  });

  it("never yields Infinity or NaN from a half-estimate", () => {
    // The server 400s these, so they should not arrive — but `scope / 0` is
    // Infinity and `0 / 0` is NaN, and either would reach the screen as a badge.
    expect(loadROI(wire(0, 8))).toBeNull();
    expect(loadROI(wire(8, 0))).toBeNull();
    expect(loadROI({ unblock: -1, scope: 8 })).toBeNull();
  });
});

describe("loadRatioText / loadBadgeTitle", () => {
  const NOW = 1_769_040_000_000;
  // `at` three hours back, so every tooltip below reads "estimated 3h ago".
  const AT = NOW - 3 * 3600_000;

  // loadBadgeText (the `⚡ 4.0× 2→8` label) was deleted with the HUD redesign:
  // the card's chip is `⚡ ${loadRatioText(roi)}` alone (50-contract.md, S2
  // LoadChip row), so the ratio rendering is what remains to pin here.
  it("renders the ratio to ONE decimal, rounding half away from zero", () => {
    expect(loadRatioText(4)).toBe("4.0×");
    expect(loadRatioText(0.25)).toBe("0.3×");
    expect(loadRatioText(5 / 3)).toBe("1.7×");
  });

  it("carries the AGE clause, and appends a note only when there is one", () => {
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    const base = "unblock effort 2/10 · next step 8/10 · roi 4.0× · estimated 3h ago";
    expect(loadBadgeTitle({ unblock: 2, scope: 8, at: AT }, 4)).toBe(base);
    // An empty note must not leave a dangling " — ", which reads as a truncation.
    expect(loadBadgeTitle({ unblock: 2, scope: 8, note: "", at: AT }, 4)).toBe(base);
    expect(loadBadgeTitle({ unblock: 2, scope: 8, note: "one decision", at: AT }, 4)).toBe(
      base + " — one decision",
    );
    vi.restoreAllMocks();
  });

  it("reads `at` as MILLIS — the same stamp in seconds would age it by decades", () => {
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    expect(loadBadgeTitle({ unblock: 2, scope: 8, at: Math.floor(AT / 1000) }, 4)).not.toContain(
      "3h ago",
    );
    vi.restoreAllMocks();
  });

  it("drops the age clause entirely rather than printing 'estimated  ago'", () => {
    // A present `load` always carries at > 0, so this is defensive only — but a
    // half-rendered clause would be worse than none.
    expect(loadBadgeTitle({ unblock: 2, scope: 8, note: "n" }, 4)).toBe(
      "unblock effort 2/10 · next step 8/10 · roi 4.0× — n",
    );
    expect(loadBadgeTitle({ unblock: 2, scope: 8, at: 0 }, 4)).toBe(
      "unblock effort 2/10 · next step 8/10 · roi 4.0×",
    );
  });
});

describe("logTime", () => {
  it("formats unix MILLIS as MM/DD HH:MM:SS", () => {
    // built from local-time parts so the assertion holds in any TZ
    const d = new Date(2026, 6, 4, 9, 5, 3);
    expect(logTime(d.getTime())).toBe("07/04 09:05:03");
  });

  it("treats its input as millis, not seconds", () => {
    const d = new Date(2026, 6, 4, 9, 5, 3);
    // the same stamp read as seconds would land ~50,000 years out
    expect(logTime(Math.floor(d.getTime() / 1000))).not.toBe("07/04 09:05:03");
  });

  it("renders BLANK for a missing stamp rather than a plausible 1970 date", () => {
    expect(logTime(undefined)).toBe("");
    expect(logTime(0)).toBe("");
    expect(logTime(null)).toBe("");
  });
});
