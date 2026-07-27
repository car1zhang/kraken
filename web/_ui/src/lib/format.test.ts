import { describe, expect, it, vi } from "vitest";

import type { WebJob } from "../api/types.ts";
import {
  age,
  bucketJobs,
  columnFor,
  countdown,
  docQuestionsOpenP0,
  flagCount,
  logTime,
  settledJob,
  stateColor,
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

describe("stateColor", () => {
  it("maps every board state to its token", () => {
    expect(stateColor("working")).toBe("var(--solved)");
    expect(stateColor("blocked")).toBe("var(--counter)");
    expect(stateColor("queued")).toBe("var(--accent)");
    expect(stateColor("done")).toBe("var(--text-2)");
    expect(stateColor("draft")).toBe("var(--text-2)");
    expect(stateColor("archived")).toBe("var(--text-2)");
  });

  it("falls back to the muted token for an unknown state", () => {
    // A server that grows a sixth state must not paint a card with `--state`
    // resolving to nothing, which is a card with no rail at all.
    expect(stateColor("wat")).toBe("var(--text-2)");
  });
});

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
