import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { WebJob } from "../../api/types.ts";
import { makeContext, makeJob } from "../../test/fixtures.ts";
import { SteerOverlay, type SteerMode } from "./SteerOverlay.tsx";

// Lead-authored (direct job 630, no blind authorship): where the context
// meter mounts in the steer modal — beside the tokens panel in one
// `.steer-meters` row, in every mode, and hidden without data. The same
// wholesale client mock SteerOverlayAccount.test.tsx uses.

vi.mock("../../api/client.ts", () => ({
  fetchDraft: vi.fn(() => Promise.resolve({ slug: "j", text: "" })),
  fetchJobForm: vi.fn(() => Promise.resolve({ slug: "j", mtime: 0 })),
  steerJob: vi.fn(() => Promise.resolve({ ok: true })),
  reactivateJob: vi.fn(() => Promise.resolve({ ok: true })),
  killJob: vi.fn(() => Promise.resolve({ ok: true })),
  doneJob: vi.fn(() => Promise.resolve({ ok: true })),
  saveDraft: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(),
  openTmux: vi.fn(() => Promise.resolve({ ok: true })),
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
  editFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  deleteFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  setJobColor: vi.fn(() => Promise.resolve({ ok: true })),
  retryJob: vi.fn(() => Promise.resolve({ ok: true })),
  answerQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  fetchGoals: vi.fn(() => Promise.resolve({ goals: { week: [], day: [] } })),
  postJobGoal: vi.fn(() => Promise.resolve({ ok: true })),
}));

const tokens: NonNullable<WebJob["tokens"]> = {
  in: 18_200,
  out: 61_300,
  cache_write: 1_020_000,
  cache_read: 3_010_000,
  sessions: 6,
  updated_at: Date.now() - 120_000,
};

function mount(over: Partial<WebJob>, mode: SteerMode = "steer") {
  const job = makeJob({ slug: "j", state: "working", live: true, ...over });
  return render(
    <SteerOverlay
      open
      slug="j"
      mode={mode}
      requireText={false}
      job={job}
      onClose={() => {}}
      mutate={async (op) => op()}
      onOpenArtifact={vi.fn()}
      onOpenThread={vi.fn()}
    />,
  );
}

describe("steer modal context meter", () => {
  it("mounts the context panel beside the tokens panel in one meters row", () => {
    mount({ context: makeContext(), tokens });
    const row = document.querySelector(".steer-meters");
    expect(row).not.toBeNull();
    expect(row).not.toHaveAttribute("hidden");
    const ctx = document.getElementById("steer-ctxwin");
    const tok = document.getElementById("steer-tokens");
    expect(ctx).not.toBeNull();
    expect(tok).not.toBeNull();
    expect(row!.contains(ctx)).toBe(true);
    expect(row!.contains(tok)).toBe(true);
    expect(ctx).not.toHaveAttribute("hidden");
    expect(tok).not.toHaveAttribute("hidden");
    // context first, tokens second — the DOM order the stacked collapse keeps
    expect(ctx!.compareDocumentPosition(tok!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(ctx!.querySelector('[data-testid="context-panel"]')).not.toBeNull();
  });

  it("shows the context panel alone when the job has no token row, and opens the block for it", () => {
    mount({ context: makeContext() });
    expect(document.getElementById("steer-context")).not.toHaveAttribute("hidden");
    expect(document.querySelector(".steer-meters")).not.toHaveAttribute("hidden");
    expect(document.getElementById("steer-ctxwin")).not.toHaveAttribute("hidden");
    expect(document.getElementById("steer-tokens")).toHaveAttribute("hidden");
  });

  it("hides the context container and the row when neither meter has data", () => {
    mount({});
    expect(document.getElementById("steer-ctxwin")).toHaveAttribute("hidden");
    expect(document.querySelector(".steer-meters")).toHaveAttribute("hidden");
  });

  it("leaves the tokens panel exactly as before when only tokens are present", () => {
    mount({ tokens });
    expect(document.getElementById("steer-tokens")).not.toHaveAttribute("hidden");
    expect(document.getElementById("steer-ctxwin")).toHaveAttribute("hidden");
    expect(document.querySelector(".steer-meters")).not.toHaveAttribute("hidden");
  });

  for (const mode of ["steer", "unblock", "view", "reactivate"] as SteerMode[]) {
    it(`renders the context panel in ${mode} mode`, () => {
      const { unmount } = mount(
        { context: makeContext(), live: mode !== "reactivate" && mode !== "view", state: mode === "view" ? "done" : "blocked" },
        mode,
      );
      const ctx = document.getElementById("steer-ctxwin");
      expect(ctx, mode).not.toBeNull();
      expect(ctx, mode).not.toHaveAttribute("hidden");
      expect(ctx!.querySelector('[data-testid="context-panel"]'), mode).not.toBeNull();
      unmount();
    });
  }
});
