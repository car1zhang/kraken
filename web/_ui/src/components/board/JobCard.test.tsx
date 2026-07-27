import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { JobCard } from "./JobCard.tsx";

// Local, and deliberately not exported: importing a fixture from a test module
// runs that module's suites a second time under the importer's name.
const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over });

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

function renderCard(j: WebJob, actions = stubActions()) {
  const col = (j.state === "archived" ? "done" : j.state) as never;
  const { container } = render(
    <JobCard job={j} col={col} actions={actions} expanded={false} onToggleExpanded={vi.fn()} />,
  );
  return { container, actions, card: container.querySelector(".card")! };
}

const QUESTIONS = [
  { id: "Q1", text: "which store", importance: "P0" },
  { id: "Q2", text: "which name", importance: "P1" },
  { id: "Q3", text: "settled already", importance: "P1", answer: "yes" },
];

// ---------------------------------------------------------------------------
// The four assertions web/boardsurface_test.go pins by grepping app.js. They
// exist because the failure mode is SILENT: re-adding the panel to the card
// renders fine, passes every Go test, and quietly re-buries a column behind
// one job's eight open questions.
// ---------------------------------------------------------------------------

describe("the card's question surface is the count chip and nothing else", () => {
  it("renders the chip, never the answerable panel", () => {
    const { container } = renderCard(job({ doc_questions: QUESTIONS }));
    const chip = container.querySelector(".dq-chip");
    expect(chip).not.toBeNull();
    expect(chip).toHaveTextContent("⚑ 2 questions");
    // the panel and every answer affordance live one click into the job
    expect(container.querySelector(".doc-questions")).toBeNull();
    expect(container.querySelector(".dq-row")).toBeNull();
    expect(container.querySelector(".dq-input")).toBeNull();
    expect(container.querySelector(".dq-send")).toBeNull();
  });

  it("does NOT carry class=chip — that is the artifact-overlay click target", () => {
    const { container } = renderCard(
      job({ doc_questions: QUESTIONS, artifacts: [{ name: "00-overview.md", url: "/a/1" }] }),
    );
    const chip = container.querySelector(".dq-chip")!;
    expect(chip.classList.contains("chip")).toBe(false);
    // and the only `.chip` on the card is the real artifact chip
    const chips = [...container.querySelectorAll(".chip")];
    expect(chips).toHaveLength(1);
    expect(chips[0]).toHaveTextContent("00-overview.md");
  });

  it("counts UNANSWERED questions only, and vanishes when the last is answered", () => {
    const { container } = renderCard(job({ doc_questions: [QUESTIONS[2]!] }));
    expect(container.querySelector(".dq-chip")).toBeNull();
    const one = renderCard(job({ doc_questions: [QUESTIONS[0]!] })).container;
    expect(one.querySelector(".dq-chip")).toHaveTextContent("⚑ 1 question");
  });

  it("skips a settled (done/archived) job — nobody is waiting on its answers", () => {
    const { container } = renderCard(job({ state: "done", doc_questions: QUESTIONS }));
    expect(container.querySelector(".dq-chip")).toBeNull();
  });

  it("flags the P0 chip so style.css can paint it hot", () => {
    const p0 = renderCard(job({ doc_questions: QUESTIONS })).container.querySelector(".dq-chip")!;
    expect(p0.classList.contains("p0")).toBe(true);
    expect(p0.getAttribute("title")).toContain("(1 P0)");
    const noP0 = renderCard(job({ doc_questions: [QUESTIONS[1]!] })).container.querySelector(
      ".dq-chip",
    )!;
    expect(noP0.classList.contains("p0")).toBe(false);
  });
});

// ---------------------------------------------------------------------------

describe("state color", () => {
  it("rides on the --state inline custom property", () => {
    const { card } = renderCard(job({ state: "working", live: true }));
    expect(card.getAttribute("style")).toContain("--state: var(--solved)");
  });
});

describe("flagged", () => {
  it("is set by a pending question, an unhealthy cell, or an open P0", () => {
    expect(renderCard(job()).card.classList.contains("flagged")).toBe(false);
    expect(
      renderCard(job({ question: { tool_use_id: "t" } })).card.classList.contains("flagged"),
    ).toBe(true);
    expect(renderCard(job({ health: "gone" })).card.classList.contains("flagged")).toBe(true);
    expect(
      renderCard(job({ doc_questions: [QUESTIONS[0]!] })).card.classList.contains("flagged"),
    ).toBe(true);
  });
});

describe("the card click fallback", () => {
  it("opens the steer composer on a live working job", async () => {
    const { card, actions } = renderCard(job({ state: "working", live: true }));
    await userEvent.click(card);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("demo", "steer");
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("opens it in unblock mode on a live blocked job", async () => {
    const { card, actions } = renderCard(job({ state: "blocked", live: true }));
    await userEvent.click(card);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("demo", "unblock");
  });

  it("treats an unhealthy live cell as blocked, whatever the file says", async () => {
    const { card, actions } = renderCard(job({ state: "working", live: true, health: "pane gone" }));
    await userEvent.click(card);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("demo", "unblock");
  });

  it("opens the editor for anything that is not live", async () => {
    const { card, actions } = renderCard(job({ state: "draft" }));
    await userEvent.click(card);
    expect(actions.onOpenEditor).toHaveBeenCalledWith("demo");
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
  });

  it("does not fire from a control inside the card", async () => {
    const { actions } = renderCard(job({ state: "queued" }));
    await userEvent.click(screen.getByTitle("cancel — return to draft (spec is kept)"));
    expect(actions.onDequeue).toHaveBeenCalledWith("demo");
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });
});

describe("PR chips", () => {
  it("linkifies http(s) only — job files are hand-editable", () => {
    const { container } = renderCard(
      job({
        prs: [
          { url: "https://github.com/o/r/pull/73", state: "merged" },
          // eslint-disable-next-line no-script-url
          { url: "javascript:alert(1)", state: "open" },
        ],
      }),
    );
    const chips = [...container.querySelectorAll(".pr-chip")];
    expect(chips[0]!.tagName).toBe("A");
    expect(chips[0]).toHaveTextContent("#73 merged");
    expect(chips[0]!.getAttribute("href")).toBe("https://github.com/o/r/pull/73");
    // the dangerous one degrades to a span that still says what it is
    expect(chips[1]!.tagName).toBe("SPAN");
    expect(chips[1]!.getAttribute("href")).toBeNull();
    expect(chips[1]).toHaveTextContent("open");
  });
});

describe("retry", () => {
  it("is offered for a fixable health problem only", () => {
    expect(renderCard(job({ health: "zeus stopped answering" })).container.querySelector(".retry"))
      .not.toBeNull();
    expect(renderCard(job({ health: "cell is gone" })).container.querySelector(".retry")).toBeNull();
    expect(
      renderCard(job({ health: "stuck at the startup prompt" })).container.querySelector(".retry"),
    ).toBeNull();
  });
});

describe("AskUserQuestion", () => {
  it("offers option buttons for a single non-multi question", async () => {
    const j = job({
      state: "blocked",
      live: true,
      question: {
        tool_use_id: "t",
        questions: [
          {
            question: "which store?",
            header: "store",
            multi_select: false,
            options: [
              { label: "sqlite", description: "one file" },
              { label: "files", description: "many" },
            ],
          },
        ],
      },
    });
    const { container, actions } = renderCard(j);
    const opts = [...container.querySelectorAll(".q-opt")];
    expect(opts.map((o) => o.textContent)).toEqual(["1. sqlite", "2. files"]);
    await userEvent.click(opts[1]!);
    expect(actions.onAnswerQuestion).toHaveBeenCalledWith("demo", 2);
    // both die once one is posted — a second digit would be a second answer
    expect((opts[0] as HTMLButtonElement).disabled).toBe(true);
  });

  it("re-arms the options when the answer fails to post", async () => {
    // Vanilla self-healed inside one 3s tick: its disable was a DOM sweep that
    // the next board rebuild undid. A mounted component cannot rely on that —
    // and a failed answer never reaches the pane, so the same question keeps
    // arriving and the block stays mounted, disabled, forever.
    const actions = stubActions();
    actions.onAnswerQuestion = vi.fn(async () => new Error("daemon restarting"));
    const { container } = renderCard(
      job({
        state: "blocked",
        live: true,
        question: {
          tool_use_id: "t",
          questions: [
            {
              question: "which store?",
              header: "store",
              multi_select: false,
              options: [{ label: "sqlite", description: "" }],
            },
          ],
        },
      }),
      actions,
    );
    const opt = container.querySelector<HTMLButtonElement>(".q-opt")!;
    await userEvent.click(opt);
    expect(actions.onAnswerQuestion).toHaveBeenCalledWith("demo", 1);
    expect(container.querySelector<HTMLButtonElement>(".q-opt")!.disabled).toBe(false);
  });

  it("points at the pane for anything a single digit cannot answer", () => {
    const { container } = renderCard(
      job({
        cell: "kraken-demo",
        question: {
          tool_use_id: "t",
          questions: [
            { question: "a?", header: "a", multi_select: true, options: [{ label: "x", description: "" }] },
          ],
        },
      }),
    );
    expect(container.querySelector(".q-opt")).toBeNull();
    expect(container.querySelector(".q-text")).toHaveTextContent("answer in the pane (k o kraken-demo)");
  });
});

describe("artifact chips", () => {
  it("collapses past three and toggles by jobKey, not by slug", async () => {
    const artifacts = ["a", "b", "c", "d"].map((n) => ({ name: n, url: "/x/" + n }));
    const toggle = vi.fn();
    const { container } = render(
      <JobCard
        job={job({ artifacts })}
        col={"draft" as never}
        actions={stubActions()}
        expanded={false}
        onToggleExpanded={toggle}
      />,
    );
    expect(container.querySelectorAll(".chip:not(.chip-more)")).toHaveLength(3);
    const more = container.querySelector(".chip-more")!;
    expect(more).toHaveTextContent("+1 more");
    await userEvent.click(more);
    expect(toggle).toHaveBeenCalledWith("demo");
  });
});
