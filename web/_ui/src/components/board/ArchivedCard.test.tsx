import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { ArchivedCard } from "./ArchivedCard.tsx";
import type { BoardActions } from "./BoardView.tsx";

// `& { color?: string }` for the same reason JobCard.test.tsx's fixture carries
// it: the colour cases must not depend on how the wire type spells the field.
const job = (over: Partial<WebJob> & { color?: string } = {}): WebJob =>
  makeJob({ slug: "old", state: "done", archived: true, ...over } as Partial<WebJob>);

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

function renderCard(j: WebJob = job()) {
  const actions = stubActions();
  const { container } = render(<ArchivedCard job={j} actions={actions} />);
  return { actions, card: container.querySelector(".card.archived")! };
}

afterEach(() => vi.restoreAllMocks());

// ---------------------------------------------------------------------------
// TestArchivedCardIsClickable (web/jobkey_test.go), ported.
//
// The archived card shows a bare ▤ COUNT whose entire stated affordance is
// "click the card". The vanilla route was one delegated handler matching
// `.card[data-editable]` — a PRESENCE selector, so the attribute has to exist
// even though its value is "false" (which only tells the editor to render
// read-only). Archived cards never carried it, which was harmless while they
// rendered real `.chip` buttons the artifact handler could open directly, and
// FATAL the moment the chip list became a count: 69 archived jobs would have
// lost the only path in the UI to their artifacts. Caught in review before it
// shipped, which is the only reason this is a precaution rather than a
// post-mortem.
// ---------------------------------------------------------------------------

describe("an archived card is clickable", () => {
  it("carries data-editable on the ROOT, valued false", () => {
    const { card } = renderCard();
    expect(card.hasAttribute("data-editable")).toBe(true);
    expect(card.getAttribute("data-editable")).toBe("false");
  });

  it("reaches its artifacts — the count chip's only route", async () => {
    const { card, actions } = renderCard(
      job({ artifacts: [{ name: "00-overview.md", url: "/a/1" }] }),
    );
    expect(card.querySelector(".ct-chip")).toHaveTextContent("▤ 1");
    await userEvent.click(card);
    // the jobKey, exactly as the vanilla handler passed the card's data-slug:
    // an archived job's key is the synthetic "archive:<slug>"
    expect(actions.onOpenEditor).toHaveBeenCalledWith("archive:old");
  });

  it("clicking the count chip itself falls through to the card", async () => {
    const { card, actions } = renderCard(
      job({ artifacts: [{ name: "00-overview.md", url: "/a/1" }] }),
    );
    await userEvent.click(card.querySelector(".ct-chip")!);
    expect(actions.onOpenEditor).toHaveBeenCalledWith("archive:old");
    expect(actions.onOpenArtifact).not.toHaveBeenCalled();
  });
});

describe("the archived card's bands", () => {
  it("keys the card by jobKey, never the bare archive name", () => {
    // snapshot/restore machinery uses this as a per-card key, so a bare slug
    // would make this card and an active job that reused the freed slug
    // restore each other's state.
    expect(renderCard().card.getAttribute("data-slug")).toBe("archive:old");
  });

  it("carries the same two counts an active card does, and no lists", () => {
    const { card } = renderCard(
      job({
        artifacts: ["a", "b", "c", "d"].map((n) => ({ name: n, url: "/x/" + n })),
        feedback: [{ id: 1, artifact: "a", note: "n", at: 0 }],
      }),
    );
    const chips = card.querySelector(".chips")!;
    expect(chips.querySelector(".ct-chip")).toHaveTextContent("▤ 4");
    // 50-contract.md, S2 `FeedbackChip`: "Text exactly `✎ ${count}` — no
    // `feedback` word, no `no durable fix` clause in the text. `title`
    // unchanged (the durable-fix detail lives there)."
    expect(chips.querySelector(".fb-chip")).toHaveTextContent("✎ 1");
    expect(chips.querySelector(".fb-chip")!.getAttribute("title")).toContain(
      "no durable fix shipped yet",
    );
    expect(card.querySelector(".chip")).toBeNull();
    expect(card.querySelector(".chip-more")).toBeNull();
    expect(card.querySelector(".job-feedback")).toBeNull();
  });

  it("renders no chip row when the archived job carries nothing", () => {
    expect(renderCard().card.querySelector(".chips")).toBeNull();
  });

  it("keeps the job's colour — it SURVIVES archiving", () => {
    // The card is dimmed as a whole by .card.archived's opacity, so the bar
    // reads as muted without a treatment of its own. Same all-or-nothing
    // emission as the live card.
    const style = renderCard(job({ color: "cyan" })).card.getAttribute("style")!;
    expect(style).toContain("--state: var(--text-2)");
    expect(style).toContain("--job-color: var(--job-cyan)");
  });

  it("omits --job-color entirely when the archived job has none", () => {
    // `?? ""`: today the attribute always exists (ArchivedCard sets --state
    // unconditionally), but if that property ever goes the way JobCard's did,
    // .not.toContain would throw on null instead of asserting absence — same
    // guard JobCard.test.tsx carries.
    expect(renderCard().card.getAttribute("style") ?? "").not.toContain("--job-color");
  });

  it("shows the renamed-on-archive label alongside the archive name", () => {
    const { card } = renderCard(job({ slug: "old-2", display_slug: "old" }));
    expect(card.querySelector(".slug")).toHaveTextContent("old");
    expect(card.textContent).toContain("as old-2");
  });

  it("puts unarchive/delete in band 6, on the BARE archive name", async () => {
    const { card, actions } = renderCard();
    const row = card.querySelector(".card-actions")!;
    await userEvent.click(row.querySelector(".unarchive")!);
    expect(actions.onUnarchive).toHaveBeenCalledWith("old");
    // and neither button opens the card behind it
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("confirms before a hard delete, and does nothing when refused", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { card, actions } = renderCard();
    await userEvent.click(card.querySelector(".delete")!);
    expect(confirm).toHaveBeenCalled();
    expect(actions.onDeleteForever).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    await userEvent.click(card.querySelector(".delete")!);
    expect(actions.onDeleteForever).toHaveBeenCalledWith("old");
  });
});
