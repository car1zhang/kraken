import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import type { Mutate } from "./Overlay.tsx";
import {
  DocQuestions,
  __resetDocQuestionDrafts,
  sortDocQuestions,
} from "./DocQuestions.tsx";

vi.mock("../../api/client.ts", () => ({
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
}));
// the tid-less branch's out-of-band channel — mocked so that case can assert
// what the reader is told, exactly as ArtifactOverlay.test does
vi.mock("./Toast.tsx", () => ({ showToast: vi.fn(), Toast: () => null }));

import { answerDocQuestion, threadOp } from "../../api/client.ts";
import { questionThreadSeed, questionThreadTitle } from "./askThread.ts";
import { showToast } from "./Toast.tsx";

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

describe("DocQuestions ask (branch a thread about a question)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    __resetDocQuestionDrafts();
  });

  const q1 = {
    id: "Q1",
    text: "should the cap scale?",
    importance: "P0" as const,
    context: "the flat 50 predates limits",
  };
  const onAskBranched = vi.fn();

  it("offers ⑂ only with a live cell AND an integrator door", () => {
    const docs = { doc_questions: [q1] };
    const { rerender } = render(
      <DocQuestions job={job(docs)} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    expect(document.querySelector(".dq-ask-toggle")).not.toBeNull();

    // no door (the job editor's rendering): rows as before
    rerender(<DocQuestions job={job(docs)} mutate={mutate} />);
    expect(document.querySelector(".dq-ask-toggle")).toBeNull();

    // a door but no live cell: a branch would only 409, so no affordance
    rerender(
      <DocQuestions
        job={job({ ...docs, live: false, state: "done" })}
        mutate={mutate}
        onAskBranched={onAskBranched}
      />,
    );
    expect(document.querySelector(".dq-ask-toggle")).toBeNull();
  });

  it("branches with the typed ask — title and seed from the row — and hands over", async () => {
    const user = userEvent.setup();
    render(
      <DocQuestions job={job({ doc_questions: [q1] })} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    expect(document.querySelector(".dq-ask")).toBeNull();
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    await user.type(
      document.querySelector(".dq-ask-input") as HTMLInputElement,
      "what limit do you mean?",
    );
    await user.click(screen.getByText("⑂ branch"));
    // the title as a LITERAL — an assertion built from the same helper the
    // component calls could never fail on a wrong title (askThread.test.ts
    // owns the helper's values; this pins the wiring AND one real output)
    await waitFor(() =>
      expect(threadOp).toHaveBeenCalledWith("j", "branch", {
        title: "Q1 — what limit do you mean?",
        text: questionThreadSeed(q1, "what limit do you mean?"),
      }),
    );
    await waitFor(() => expect(onAskBranched).toHaveBeenCalledWith("j", "t1"));
    // the box closed and the draft is gone
    expect(document.querySelector(".dq-ask")).toBeNull();
  });

  it("an EMPTY ask still branches — the seed carries the default unpack ask", async () => {
    const user = userEvent.setup();
    render(
      <DocQuestions job={job({ doc_questions: [q1] })} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    await user.click(screen.getByText("⑂ branch"));
    await waitFor(() =>
      expect(threadOp).toHaveBeenCalledWith("j", "branch", {
        title: questionThreadTitle(q1.id, q1.text, ""),
        text: questionThreadSeed(q1, ""),
      }),
    );
  });

  it("a tid-less success toasts instead of handing over", async () => {
    const user = userEvent.setup();
    vi.mocked(threadOp).mockResolvedValueOnce({ ok: true });
    render(
      <DocQuestions job={job({ doc_questions: [q1] })} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    await user.click(screen.getByText("⑂ branch"));
    await waitFor(() =>
      expect(showToast).toHaveBeenCalledWith(
        "branched — the thread is listed on the job",
      ),
    );
    expect(onAskBranched).not.toHaveBeenCalled();
  });

  it("a failed branch keeps the box open with its text for a retry", async () => {
    const user = userEvent.setup();
    // the real mutate resolves null on failure, having parked the failure
    // text in the status corner (Overlay.tsx's Mutate contract)
    const failMutate: Mutate = async () => null;
    render(
      <DocQuestions
        job={job({ doc_questions: [q1] })}
        mutate={failMutate}
        onAskBranched={onAskBranched}
      />,
    );
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    await user.type(
      document.querySelector(".dq-ask-input") as HTMLInputElement,
      "still unclear",
    );
    await user.click(screen.getByText("⑂ branch"));
    await waitFor(() =>
      expect(
        (document.querySelector(".dq-ask-input") as HTMLInputElement).disabled,
      ).toBe(false),
    );
    expect(
      (document.querySelector(".dq-ask-input") as HTMLInputElement).value,
    ).toBe("still unclear");
    expect(onAskBranched).not.toHaveBeenCalled();
  });

  it("collapses an open ask box when the cell dies (live flips false)", async () => {
    const user = userEvent.setup();
    const docs = { doc_questions: [q1] };
    const { rerender } = render(
      <DocQuestions job={job(docs)} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    expect(document.querySelector(".dq-ask")).not.toBeNull();

    // the 3s poll delivers the death; the toggle unmounts with `askable`, so
    // a surviving box would be uncloseable and could only post a refusal
    rerender(
      <DocQuestions
        job={job({ ...docs, live: false })}
        mutate={mutate}
        onAskBranched={onAskBranched}
      />,
    );
    expect(document.querySelector(".dq-ask")).toBeNull();
  });

  it("drops a stale ask box when the question gets answered, so a re-answer click doesn't resurrect it", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <DocQuestions job={job({ doc_questions: [q1] })} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    expect(document.querySelector(".dq-ask")).not.toBeNull();

    // an answer arrives (another window's send, delivered by the poll)
    rerender(
      <DocQuestions
        job={job({ doc_questions: [{ ...q1, answer: "yes" }] })}
        mutate={mutate}
        onAskBranched={onAskBranched}
      />,
    );
    // reopening for a re-answer must NOT resurrect the box — its autoFocus
    // would pull the caret into a box the click never asked for
    await user.click(document.querySelector(".dq-answered") as HTMLElement);
    expect(document.querySelector(".dq-input")).not.toBeNull();
    expect(document.querySelector(".dq-ask")).toBeNull();
  });

  it("keeps a typed ask across an unmount, separately from the answer draft", async () => {
    const user = userEvent.setup();
    const j = job({ doc_questions: [q1] });
    const first = render(
      <DocQuestions job={j} mutate={mutate} onAskBranched={onAskBranched} />,
    );
    await user.type(
      document.querySelector(".dq-input:not(.dq-ask-input)") as HTMLInputElement,
      "half an answer",
    );
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    await user.type(
      document.querySelector(".dq-ask-input") as HTMLInputElement,
      "half an ask",
    );
    first.unmount();

    render(<DocQuestions job={j} mutate={mutate} onAskBranched={onAskBranched} />);
    expect(
      (document.querySelector(".dq-input:not(.dq-ask-input)") as HTMLInputElement)
        .value,
    ).toBe("half an answer");
    // the box reopens closed; the text is waiting behind the toggle
    await user.click(document.querySelector(".dq-ask-toggle") as HTMLElement);
    expect(
      (document.querySelector(".dq-ask-input") as HTMLInputElement).value,
    ).toBe("half an ask");
  });
});
