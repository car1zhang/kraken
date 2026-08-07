import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { WebJob, WebJobLoad } from "../../../api/types.ts";
import type { Column } from "../../../lib/columns.ts";
import { bucketJobs } from "../../../lib/format.ts";
import { makeJob } from "../../../test/fixtures.ts";
import type { BoardActions } from "../BoardView.tsx";
import { JobCard } from "../JobCard.tsx";
import { LoadChip, loadChipVisible } from "./LoadChip.tsx";

// A fixed clock, so the tooltip's age clause is assertable. Every test in this
// file that reads a tooltip pins Date.now() to NOW; `at` sits three hours back.
const NOW = 1_769_040_000_000;
const AT = NOW - 3 * 3600_000;

const load = (unblock: number, scope: number, note = ""): WebJobLoad => ({
  unblock,
  scope,
  note,
  at: AT,
});

const blocked = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ state: "blocked", live: true, cell: "kraken-demo", ...over });

function renderChip(job: WebJob, col: Column = "blocked") {
  const { container } = render(<LoadChip job={job} col={col} />);
  return container.querySelector<HTMLElement>(".ld-chip");
}

describe("LoadChip", () => {
  // 50-contract.md, S2 `LoadChip`: "Chip text is exactly `⚡ ${roi.toFixed(1)}×`
  // — e.g. `⚡ 4.0×`, `⚡ 0.3×`. No `unblock→scope` on the card." The two halves
  // the ratio came from moved to the tooltip, which the case below still pins in
  // full; the ratio itself — precision included — is unchanged.
  it("renders the shared contract's worked examples exactly", () => {
    expect(renderChip(blocked({ load: load(2, 8) }))?.textContent).toBe("⚡ 4.0×");
    expect(renderChip(blocked({ load: load(5, 5) }))?.textContent).toBe("⚡ 1.0×");
    expect(renderChip(blocked({ load: load(8, 2) }))?.textContent).toBe("⚡ 0.3×");
    expect(renderChip(blocked({ load: load(3, 5) }))?.textContent).toBe("⚡ 1.7×");
  });

  it("bands each worked example where the contract states", () => {
    expect(renderChip(blocked({ load: load(2, 8) }))?.dataset.band).toBe("hot");
    expect(renderChip(blocked({ load: load(3, 5) }))?.dataset.band).toBe("warm");
    expect(renderChip(blocked({ load: load(5, 5) }))?.dataset.band).toBe("even");
    expect(renderChip(blocked({ load: load(8, 2) }))?.dataset.band).toBe("cold");
  });

  it("puts each band boundary in the HIGHER band — 3.0, 1.5 and 0.75 all round up", () => {
    // Closed-open bands, so the boundary belongs to the band above it. The
    // class is what style.css paints, so assert it and not just data-band.
    expect(renderChip(blocked({ load: load(2, 6) }))?.className).toBe("ld-chip hot"); // 3.0
    expect(renderChip(blocked({ load: load(2, 3) }))?.className).toBe("ld-chip warm"); // 1.5
    expect(renderChip(blocked({ load: load(4, 3) }))?.className).toBe("ld-chip even"); // 0.75
    expect(renderChip(blocked({ load: load(10, 7) }))?.className).toBe("ld-chip cold"); // 0.7
  });

  it("carries the tooltip on both title and aria-label, note appended only when present", () => {
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    const base = "unblock effort 2/10 · next step 8/10 · roi 4.0× · estimated 3h ago";

    const bare = renderChip(blocked({ load: load(2, 8) }))!;
    expect(bare.title).toBe(base);
    expect(bare.getAttribute("aria-label")).toBe(bare.title);

    const note = "one decision: which store keys the row";
    const noted = renderChip(blocked({ load: load(2, 8, note) }))!;
    expect(noted.title).toBe(base + " — " + note);
    expect(noted.getAttribute("aria-label")).toBe(noted.title);
    vi.restoreAllMocks();
  });

  it("ages the tooltip, so a re-blocked card's stale estimate says so", () => {
    // The hazard the age clause exists for: zeus estimates, carl answers, the
    // cell dies hours later, the daemon's `health` files the card back under
    // blocked (bucketJobs) — and zeus is not alive to write a new estimate. The
    // badge text is unchanged in all of that; only the age moves.
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    const fresh = renderChip(blocked({ load: { ...load(2, 8), at: NOW - 30_000 } }))!;
    expect(fresh.title).toContain("· estimated 30s ago");
    const stale = renderChip(blocked({ load: { ...load(2, 8), at: NOW - 5 * 86400_000 } }))!;
    expect(stale.title).toContain("· estimated 5d ago");
    expect(stale.textContent).toBe(fresh.textContent);
    vi.restoreAllMocks();
  });

  it("renders NOTHING for a both-zero object — which a clear does not even send", () => {
    // A clear zeroes `at` server-side, so the key is dropped rather than sent
    // as two zeros (internal/api/jobload.go). Defensive coverage, kept because
    // it is what stands between a future zero and an "Infinity×" badge.
    expect(renderChip(blocked({ load: load(0, 0) }))).toBeNull();
    expect(loadChipVisible(blocked({ load: load(0, 0) }), "blocked")).toBe(false);
  });

  it("renders NOTHING with no load key — never estimated is not an estimate of zero", () => {
    expect(renderChip(blocked())).toBeNull();
    expect(loadChipVisible(blocked(), "blocked")).toBe(false);
  });

  it("renders NOTHING for a half-estimate rather than an Infinity× badge", () => {
    // The server rejects a half-estimate, so this is the defensive half of the
    // division guard in loadROI — but `scope / 0` reaching the screen would be
    // silent and permanent-looking, so the chip refuses it too.
    expect(renderChip(blocked({ load: load(0, 8) }))).toBeNull();
    expect(renderChip(blocked({ load: load(8, 0) }))).toBeNull();
  });

  it("renders NOTHING on a card that does not read as blocked", () => {
    // The badge answers "is this one worth unblocking right now", which no
    // other column poses — and a stale estimate on a running card would read
    // as a current fact.
    for (const col of ["draft", "queued", "working", "done"] as Column[]) {
      expect(renderChip(blocked({ state: col, load: load(2, 8) }), col)).toBeNull();
      expect(loadChipVisible(blocked({ state: col, load: load(2, 8) }), col)).toBe(false);
    }
  });

  it("shows on an UNHEALTHY cell, because bucketJobs files one as blocked", () => {
    // The one notion of "reads as blocked" is the column bucketJobs put the
    // card in — this asserts the chip agrees with that bucketing rather than
    // re-deriving it from job.health.
    const sick = blocked({
      slug: "sick",
      state: "working",
      health: "cell is gone",
      load: load(2, 8),
    });
    const col = (Object.keys(bucketJobs([sick]).byColumn) as Column[]).find(
      (c) => bucketJobs([sick]).byColumn[c].length > 0,
    )!;
    expect(col).toBe("blocked");
    // 50-contract.md, S2 `LoadChip`: chip text is the ratio alone.
    expect(renderChip(sick, col)?.textContent).toBe("⚡ 4.0×");
  });
});

// ---------------------------------------------------------------------------
// Inside the card: band 2, leading the chip row.
// ---------------------------------------------------------------------------

function stubActions(): BoardActions {
  return {
    onOpenSteer: vi.fn(),
    onOpenEditor: vi.fn(),
    onOpenArtifact: vi.fn(),
    onOpenThread: vi.fn(),
    onAnswerQuestion: vi.fn(),
    onRetry: vi.fn(),
    onDequeue: vi.fn(),
    onOpenTmux: vi.fn(),
    onSaveFeedback: vi.fn(async () => null),
    onDeleteFeedback: vi.fn(),
    onUnarchive: vi.fn(),
    onDeleteForever: vi.fn(),
  };
}

function renderCard(job: WebJob, col: Column = "blocked") {
  const { container } = render(<JobCard job={job} col={col} actions={stubActions()} />);
  return container.querySelector(".card")!;
}

describe("LoadChip in JobCard", () => {
  it("leads the chip row, ahead of every count", () => {
    const card = renderCard(
      blocked({
        load: load(2, 8),
        feedback: [{ id: 1, artifact: "00-overview.md", note: "n", at: 0 }],
        doc_questions: [{ id: "Q1", text: "which store", importance: "P0" }],
      }),
    );
    const row = card.querySelector(".chips")!;
    expect([...row.children].map((el) => el.className.split(" ")[0])).toEqual([
      "ld-chip",
      "dq-chip",
      "fb-chip",
    ]);
  });

  it("is enough on its own to give the card a chip row", () => {
    // hasChips is asked per chip rather than by measuring the rendered row, so
    // a chip left out of that predicate renders into a row that never mounts.
    const card = renderCard(blocked({ load: load(2, 8) }));
    // 50-contract.md, S2 `LoadChip`: chip text is the ratio alone.
    expect(card.querySelector(".chips .ld-chip")?.textContent).toBe("⚡ 4.0×");
  });

  it("stays out of the chip row entirely on a working card", () => {
    const card = renderCard(blocked({ state: "working", load: load(2, 8) }), "working");
    expect(card.querySelector(".ld-chip")).toBeNull();
    expect(card.querySelector(".chips")).toBeNull();
  });
});
