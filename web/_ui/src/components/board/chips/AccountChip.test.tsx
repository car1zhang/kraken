import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { WebJob, WebJobAccount } from "../../../api/types.ts";
import type { Column } from "../../../lib/columns.ts";
import { makeAccount, makeJob } from "../../../test/fixtures.ts";
import type { BoardActions } from "../BoardView.tsx";
import { JobCard } from "../JobCard.tsx";
import { AccountChip, accountChipVisible, accountLocalPart } from "./AccountChip.tsx";

// A fixed clock so the tooltip's age clause is assertable; `at` sits two hours
// back, the LoadChip test file's arrangement. The shared fixture carries the
// wire defaults; this file overrides only what its clock needs.
const NOW = 1_769_040_000_000;
const AT = NOW - 2 * 3600_000;

const account = (over: Partial<WebJobAccount> = {}): WebJobAccount =>
  makeAccount({ slug: "dorisexplorer4-gmail-com", at: AT, ...over });

const bound = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ state: "working", live: true, cell: "kraken-demo", account: account(), ...over });

function renderChip(job: WebJob) {
  const { container } = render(<AccountChip job={job} />);
  return container.querySelector<HTMLElement>(".ac-chip");
}

describe("AccountChip", () => {
  it("renders the email's local part behind the glyph", () => {
    expect(renderChip(bound())?.textContent).toBe("⬢ dorisexplorer4");
  });

  it("falls back to the slug when the email is empty or has no local part", () => {
    // A slot ccp holds no identity for yet still names what kraken selected.
    expect(renderChip(bound({ account: account({ email: "" }) }))?.textContent).toBe(
      "⬢ dorisexplorer4-gmail-com",
    );
    // A leading `@` would render the provider, not the account — same fallback.
    expect(renderChip(bound({ account: account({ email: "@gmail.com" }) }))?.textContent).toBe(
      "⬢ dorisexplorer4-gmail-com",
    );
  });

  it("carries the full sentence on title and aria-label, age clause included", () => {
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    const chip = renderChip(bound())!;
    expect(chip.title).toBe(
      "bound to dorisexplorer4@gmail.com (slot dorisexplorer4-gmail-com), 2h ago",
    );
    expect(chip.getAttribute("aria-label")).toBe(chip.title);
    vi.restoreAllMocks();
  });

  it("drops the age clause on a zero stamp rather than dating the bind to now", () => {
    const chip = renderChip(bound({ account: account({ at: 0 }) }))!;
    expect(chip.title).toBe("bound to dorisexplorer4@gmail.com (slot dorisexplorer4-gmail-com)");
  });

  it("renders NOTHING with no account key — unbound cells show no badge (Q7)", () => {
    expect(renderChip(bound({ account: undefined }))).toBeNull();
    expect(accountChipVisible(bound({ account: undefined }))).toBe(false);
  });

  it("renders on both live states and NOTHING on the others", () => {
    // A binding is a fact about a RUNNING cell; done and draft cards have no
    // cell, so the badge there would be a dead fact reading as current.
    expect(renderChip(bound({ state: "working" }))).not.toBeNull();
    expect(renderChip(bound({ state: "blocked" }))).not.toBeNull();
    for (const state of ["draft", "done"]) {
      expect(renderChip(bound({ state }))).toBeNull();
      expect(accountChipVisible(bound({ state }))).toBe(false);
    }
  });

  it("is a plain span, never class `chip`, glyph hidden from the reader", () => {
    const chip = renderChip(bound())!;
    expect(chip.tagName).toBe("SPAN");
    expect(chip.classList.contains("chip")).toBe(false);
    expect(chip.querySelector('[aria-hidden="true"]')?.textContent).toBe("⬢ ");
  });
});

describe("accountLocalPart", () => {
  it("splits on the FIRST @ and passes @-free strings through", () => {
    expect(accountLocalPart("a@b@c")).toBe("a");
    expect(accountLocalPart("no-at-sign")).toBe("no-at-sign");
    expect(accountLocalPart("")).toBe("");
  });
});

// ---------------------------------------------------------------------------
// Inside the card: band 2's facts group, between the stage chip and the counts.
// ---------------------------------------------------------------------------

function stubActions(): BoardActions {
  return {
    onOpenSteer: vi.fn(),
    onOpenEditor: vi.fn(),
    onOpenArtifact: vi.fn(),
    onOpenThread: vi.fn(),
    onAnswerQuestion: vi.fn(),
    onRetry: vi.fn(),
    onOpenTmux: vi.fn(),
    onSaveFeedback: vi.fn(async () => null),
    onDeleteFeedback: vi.fn(),
    onUnarchive: vi.fn(),
    onDeleteForever: vi.fn(),
  };
}

function renderCard(job: WebJob, col: Column = "working") {
  const { container } = render(<JobCard job={job} col={col} actions={stubActions()} />);
  return container.querySelector(".card")!;
}

describe("AccountChip in JobCard", () => {
  it("sits after the stage chip and before the counts", () => {
    const card = renderCard(
      bound({
        stage: "do",
        feedback: [{ id: 1, artifact: "00-overview.md", note: "n", at: 0 }],
      }),
    );
    const row = card.querySelector(".chips")!;
    const classes = [...row.children].map((el) => el.className.split(" ")[0]);
    expect(classes.indexOf("st-chip")).toBeLessThan(classes.indexOf("ac-chip"));
    expect(classes.indexOf("ac-chip")).toBeLessThan(classes.indexOf("fb-chip"));
  });

  it("is enough on its own to give the card a chip row", () => {
    // hasChips is asked per chip; a chip left out of that predicate renders
    // into a row that never mounts.
    const card = renderCard(bound());
    expect(card.querySelector(".chips .ac-chip")?.textContent).toBe("⬢ dorisexplorer4");
  });

  it("stays out of the row on a done card even when the key is present", () => {
    const card = renderCard(bound({ state: "done" }), "done");
    expect(card.querySelector(".ac-chip")).toBeNull();
  });
});
