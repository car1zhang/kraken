import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import {
  DocQuestions,
  __resetDocQuestionDrafts,
  sortDocQuestions,
} from "./DocQuestions.tsx";

vi.mock("../../api/client.ts", () => ({
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
}));

import { answerDocQuestion } from "../../api/client.ts";

const mutate = async <T,>(op: () => Promise<T>) => op();

const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug: "j", state: "working", live: true, ...over });

describe("sortDocQuestions", () => {
  it("puts open questions first by importance, then answered in file order", () => {
    const out = sortDocQuestions([
      { id: "Q1", text: "a", importance: "P2" },
      { id: "Q2", text: "b", importance: "P0" },
      { id: "Q3", text: "c", importance: "P1", answer: "yes" },
      { id: "Q4", text: "d", importance: "P0", answer: "no" },
      { id: "Q5", text: "e", importance: "P1" },
    ]);
    expect(out.map((q) => q.id)).toEqual(["Q2", "Q5", "Q1", "Q3", "Q4"]);
  });

  it("is stable within one importance, and ranks an unknown importance last", () => {
    const out = sortDocQuestions([
      { id: "A", text: "", importance: "P1" },
      { id: "B", text: "", importance: "weird" },
      { id: "C", text: "", importance: "P1" },
    ]);
    expect(out.map((q) => q.id)).toEqual(["A", "C", "B"]);
  });
});

describe("DocQuestions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    __resetDocQuestionDrafts();
  });

  it("renders nothing at all for a job with no questions", () => {
    const { container } = render(<DocQuestions job={job()} mutate={mutate} />);
    expect(container.querySelector(".doc-questions")).toBeNull();
  });

  // THE draft-preservation property. In the vanilla board this needed a
  // snapshot/restore pair around every wholesale innerHTML rebuild
  // (snapshotDocInputs/restoreDocInputs) because the 3s poll destroyed the
  // input node. Here it has to follow from state — a props change that would
  // previously have wiped the field must leave it alone.
  it("keeps typed text across a props change that rebuilt the panel before", async () => {
    const user = userEvent.setup();
    const before = job({
      doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
    });
    const { rerender } = render(<DocQuestions job={before} mutate={mutate} />);

    const input = document.querySelector(".dq-input") as HTMLInputElement;
    await user.type(input, "the second one");
    expect(input.value).toBe("the second one");

    // a fresh /api/board payload: new object identity, new array, new question
    // objects — everything the old rebuild path keyed off
    rerender(
      <DocQuestions
        job={job({
          detail: "zeus said something new",
          doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
        })}
        mutate={mutate}
      />,
    );

    const after = document.querySelector(".dq-input") as HTMLInputElement;
    expect(after.value).toBe("the second one");
  });

  it("keeps typed text across an UNMOUNT and remount (a stale-close mid-answer)", async () => {
    const user = userEvent.setup();
    const j = job({
      doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
    });
    const first = render(<DocQuestions job={j} mutate={mutate} />);
    await user.type(
      document.querySelector(".dq-input") as HTMLInputElement,
      "half an answer",
    );
    // checkStaleModals force-closing the modal is exactly this
    first.unmount();

    render(<DocQuestions job={j} mutate={mutate} />);
    expect((document.querySelector(".dq-input") as HTMLInputElement).value).toBe(
      "half an answer",
    );
  });

  it("a DELIBERATELY cleared answer does not come back from the stash", async () => {
    const user = userEvent.setup();
    const j = job({
      doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
    });
    const first = render(<DocQuestions job={j} mutate={mutate} />);
    const input = document.querySelector(".dq-input") as HTMLInputElement;
    await user.type(input, "oops");
    await user.clear(input);
    first.unmount();

    render(<DocQuestions job={j} mutate={mutate} />);
    expect((document.querySelector(".dq-input") as HTMLInputElement).value).toBe(
      "",
    );
  });

  it("sends the trimmed free text and drops the draft on success", async () => {
    const user = userEvent.setup();
    const j = job({
      doc_questions: [{ id: "Q1", text: "which?", importance: "P1" }],
    });
    const first = render(<DocQuestions job={j} mutate={mutate} />);
    await user.type(
      document.querySelector(".dq-input") as HTMLInputElement,
      "  answered  ",
    );
    await user.click(screen.getByText("send"));
    await waitFor(() =>
      expect(answerDocQuestion).toHaveBeenCalledWith("j", "Q1", "answered"),
    );

    first.unmount();
    render(<DocQuestions job={j} mutate={mutate} />);
    expect((document.querySelector(".dq-input") as HTMLInputElement).value).toBe(
      "",
    );
  });

  it("refuses an empty send", async () => {
    const user = userEvent.setup();
    render(
      <DocQuestions
        job={job({ doc_questions: [{ id: "Q1", text: "?", importance: "P2" }] })}
        mutate={mutate}
      />,
    );
    await user.click(screen.getByText("send"));
    expect(answerDocQuestion).not.toHaveBeenCalled();
  });

  it("accept rec posts the recommendation verbatim", async () => {
    const user = userEvent.setup();
    render(
      <DocQuestions
        job={job({
          doc_questions: [
            { id: "Q1", text: "?", importance: "P0", recommendation: "do it" },
          ],
        })}
        mutate={mutate}
      />,
    );
    await user.click(screen.getByText("accept rec"));
    await waitFor(() =>
      expect(answerDocQuestion).toHaveBeenCalledWith("j", "Q1", "do it"),
    );
  });

  it("Enter sends, but a Shift/Alt-Enter or an IME commit does not", async () => {
    const user = userEvent.setup();
    render(
      <DocQuestions
        job={job({ doc_questions: [{ id: "Q1", text: "?", importance: "P0" }] })}
        mutate={mutate}
      />,
    );
    const input = document.querySelector(".dq-input") as HTMLInputElement;
    await user.type(input, "yes");

    await user.keyboard("{Shift>}{Enter}{/Shift}");
    expect(answerDocQuestion).not.toHaveBeenCalled();
    await user.keyboard("{Alt>}{Enter}{/Alt}");
    expect(answerDocQuestion).not.toHaveBeenCalled();

    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(answerDocQuestion).toHaveBeenCalledWith("j", "Q1", "yes"),
    );
  });

  it("an answered row is read-only until clicked, then reopens prefilled (Q13)", async () => {
    const user = userEvent.setup();
    render(
      <DocQuestions
        job={job({
          doc_questions: [
            { id: "Q1", text: "?", importance: "P1", answer: "the old answer" },
          ],
        })}
        mutate={mutate}
      />,
    );
    expect(document.querySelector(".dq-input")).toBeNull();
    expect(document.querySelector(".dq-answered-text")?.textContent).toBe(
      "✓ the old answer",
    );

    await user.click(document.querySelector(".dq-answered") as HTMLElement);
    const input = document.querySelector(".dq-input") as HTMLInputElement;
    expect(input.value).toBe("the old answer");
    // and the re-answer cancel is offered, which a genuinely open row has no need of
    expect(document.querySelector(".dq-reanswer-cancel")).not.toBeNull();
  });

  it("the hint only renders while something is still open", () => {
    const { container, rerender } = render(
      <DocQuestions
        job={job({ doc_questions: [{ id: "Q1", text: "?", importance: "P0" }] })}
        mutate={mutate}
      />,
    );
    expect(container.querySelector(".dq-hint")).not.toBeNull();
    rerender(
      <DocQuestions
        job={job({
          doc_questions: [{ id: "Q1", text: "?", importance: "P0", answer: "a" }],
        })}
        mutate={mutate}
      />,
    );
    expect(container.querySelector(".dq-hint")).toBeNull();
  });
});
