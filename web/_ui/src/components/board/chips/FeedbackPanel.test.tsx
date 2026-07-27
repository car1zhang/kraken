import { fireEvent, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../../api/types.ts";
import { makeJob } from "../../../test/fixtures.ts";
import { FeedbackPanel } from "./FeedbackPanel.tsx";

const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ state: "working", live: true, ...over });

const NOTES = [
  { id: 11, artifact: "10-arch.html", note: "the arrow points the wrong way", at: 0 },
  {
    id: 12,
    artifact: "00-overview.md",
    note: "too long",
    at: 0,
    addressed: "trimmed to one page",
    addressed_at: "2026-07-20",
  },
];

type Save = (slug: string, id: number, note: string) => Promise<Error | null>;

function renderPanel(over: Partial<WebJob> = {}, save: Save = vi.fn<Save>(async () => null)) {
  const onDeleteFeedback = vi.fn();
  const { container } = render(
    <FeedbackPanel
      job={job({ feedback: NOTES, ...over })}
      onSaveFeedback={save}
      onDeleteFeedback={onDeleteFeedback}
    />,
  );
  return { container, save, onDeleteFeedback };
}

afterEach(() => vi.restoreAllMocks());

describe("FeedbackPanel", () => {
  it("renders nothing at all for a job with no notes", () => {
    const { container } = renderPanel({ feedback: [] });
    expect(container.querySelector(".job-feedback")).toBeNull();
  });

  it("dims an addressed note but keeps it editable", () => {
    const { container } = renderPanel();
    const rows = [...container.querySelectorAll(".fb-row")];
    expect(rows[0]!.classList.contains("fb-addressed")).toBe(false);
    expect(rows[1]!.classList.contains("fb-addressed")).toBe(true);
    expect(rows[1]!.querySelector(".fb-addressed-text")).toHaveTextContent("✓ trimmed to one page");
    // a note staying accurate beats freezing it the moment zeus reacts
    expect(rows[1]!.querySelector(".fb-edit")).not.toBeNull();
  });

  it("edits in place and saves the trimmed text", async () => {
    const { container, save } = renderPanel();
    await userEvent.click(container.querySelector(".fb-edit")!);
    const input = container.querySelector<HTMLTextAreaElement>(".fb-edit-input")!;
    expect(input.value).toBe("the arrow points the wrong way");
    await userEvent.clear(input);
    await userEvent.type(input, "  points the wrong way  ");
    await userEvent.click(container.querySelector(".fb-save")!);
    expect(save).toHaveBeenCalledWith("demo", 11, "points the wrong way");
    // a successful save closes the editor
    expect(container.querySelector(".fb-edit-input")).toBeNull();
  });

  it("refuses an empty note without posting", async () => {
    const { container, save } = renderPanel();
    await userEvent.click(container.querySelector(".fb-edit")!);
    await userEvent.clear(container.querySelector(".fb-edit-input")!);
    await userEvent.click(container.querySelector(".fb-save")!);
    expect(save).not.toHaveBeenCalled();
    expect(container.querySelector(".fb-edit-note")).toHaveTextContent("feedback is empty");
  });

  it("keeps the editor (and the typed text) open when the save fails", async () => {
    const save = vi.fn(async () => new Error("db is locked"));
    const { container } = renderPanel({}, save);
    await userEvent.click(container.querySelector(".fb-edit")!);
    await userEvent.type(container.querySelector(".fb-edit-input")!, "!");
    await userEvent.click(container.querySelector(".fb-save")!);
    const input = container.querySelector<HTMLTextAreaElement>(".fb-edit-input")!;
    expect(input).not.toBeNull();
    expect(input.value).toBe("the arrow points the wrong way!");
    expect(container.querySelector(".fb-edit-note")).toHaveTextContent("save failed");
  });

  it("cancels back to the stored note", async () => {
    const { container, save } = renderPanel();
    await userEvent.click(container.querySelector(".fb-edit")!);
    await userEvent.type(container.querySelector(".fb-edit-input")!, " — actually fine");
    await userEvent.click(container.querySelector(".fb-cancel")!);
    expect(container.querySelector(".fb-note")).toHaveTextContent("the arrow points the wrong way");
    expect(save).not.toHaveBeenCalled();
  });

  it("confirms before deleting", async () => {
    const { container, onDeleteFeedback } = renderPanel();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await userEvent.click(container.querySelector(".fb-delete")!);
    expect(onDeleteFeedback).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    await userEvent.click(container.querySelector(".fb-delete")!);
    expect(onDeleteFeedback).toHaveBeenCalledWith("demo", 11);
  });

  it("never lets a drag inside it become a column move", () => {
    // NOT a bug (53-port-checklist.md): browsers resolve a press-drag that
    // starts on a form control to the draggable ANCESTOR, so selecting a word
    // of a typed note used to post a real board transition.
    const { container } = renderPanel();
    const panel = container.querySelector(".job-feedback")!;
    const ev = new Event("dragstart", { bubbles: true, cancelable: true });
    fireEvent(panel, ev);
    expect(ev.defaultPrevented).toBe(true);
  });
});
