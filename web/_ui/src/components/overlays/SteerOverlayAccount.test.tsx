import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { WebUsage } from "../../api/types.ts";
import { makeAccount, makeJob } from "../../test/fixtures.ts";
import { SteerOverlay, type SteerMode } from "./SteerOverlay.tsx";

// Lead-authored (sighted) coverage for the modal's account section — the
// same wholesale client mock the mode-matrix file uses; this file is about
// what `.steer-account` renders, not the network.
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

const account = makeAccount({
  slug: "dorisexplorer4-gmail-com",
  at: Date.now() - 3600_000,
});

const usage: WebUsage = {
  available: true,
  fetched_at: Date.now(),
  age_seconds: 30,
  daemon_ok: true,
  accounts: [
    {
      slug: "dorisexplorer4-gmail-com",
      email: "dorisexplorer4@gmail.com",
      session: { percent: 34 },
      models: [{ name: "Fable", percent: 61 }],
    },
  ],
  rotation_mode: "auto",
};

function mount(over: { account?: typeof account; usage?: WebUsage; mode?: SteerMode }) {
  const job = makeJob({ slug: "j", state: "working", live: true, account: over.account });
  return render(
    <SteerOverlay
      open
      slug="j"
      mode={over.mode ?? "steer"}
      requireText={false}
      job={job}
      usage={over.usage}
      onClose={() => {}}
      mutate={async (op) => op()}
      onOpenArtifact={vi.fn()}
      onOpenThread={vi.fn()}
    />,
  );
}

describe("steer modal account section", () => {
  it("names the bound slot and email, with both usage bars from the poll", () => {
    mount({ account, usage });
    const sect = document.getElementById("steer-account")!;
    expect(sect).not.toBeNull();
    expect(sect.textContent).toContain("dorisexplorer4-gmail-com");
    expect(sect.textContent).toContain("dorisexplorer4@gmail.com");
    expect(sect.textContent).toContain("34%");
    expect(sect.textContent).toContain("61%");
  });

  it("renders no bars when the usage poll lacks the slot — never a fabricated 0%", () => {
    mount({ account, usage: { ...usage, accounts: [] } });
    const sect = document.getElementById("steer-account")!;
    expect(sect.textContent).toContain("dorisexplorer4-gmail-com");
    expect(sect.textContent).not.toContain("%");
  });

  it("answers 'main' outright for an unbound job — the card's silent case", () => {
    mount({ account: undefined, usage });
    const sect = document.getElementById("steer-account")!;
    expect(sect.textContent).toContain("main");
    expect(sect.textContent).toContain("machine default");
  });

  it("hides in reactivate mode — a dead cell's binding is a dead fact", () => {
    // The overlay hides sections with the `hidden` attribute rather than
    // unmounting them (the steer-context pattern), so the assertion is the
    // attribute, not absence from the DOM.
    mount({ account, usage, mode: "reactivate" });
    const sect = document.getElementById("steer-account") as HTMLElement;
    expect(sect.hidden || sect.closest("[hidden]") !== null).toBe(true);
  });
});
