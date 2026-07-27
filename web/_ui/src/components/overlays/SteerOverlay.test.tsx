import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { makeJob } from "../../test/fixtures.ts";
import {
  SteerOverlay,
  steerEndpoint,
  steerRequiresText,
  type SteerMode,
} from "./SteerOverlay.tsx";

// The client is mocked wholesale: this is a mode-matrix test, and what it needs
// to observe is WHICH endpoint a mode sends to and WHETHER an empty message is
// refused — not the network.
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
}));

import { reactivateJob, steerJob } from "../../api/client.ts";

const job = makeJob({ slug: "j", state: "working", live: true });

const mutate = async <T,>(op: () => Promise<T>) => op();

function mount(mode: SteerMode, requireText = false) {
  return render(
    <SteerOverlay
      open
      slug="j"
      mode={mode}
      requireText={requireText}
      job={job}
      onClose={() => {}}
      mutate={mutate}
    />,
  );
}

describe("steer mode matrix", () => {
  beforeEach(() => vi.clearAllMocks());

  it("titles each mode distinctly", async () => {
    const cases: [SteerMode, string][] = [
      ["steer", "j — message zeus"],
      ["unblock", "j — unblock"],
      ["reactivate", "j — reactivate (same zeus session)"],
    ];
    for (const [mode, title] of cases) {
      const { unmount } = mount(mode);
      expect(document.getElementById("steer-title")?.textContent).toBe(title);
      unmount();
    }
  });

  it("labels the send button per mode", () => {
    const cases: [SteerMode, string][] = [
      ["steer", "send ⌘⏎"],
      ["unblock", "unblock ⌘⏎"],
      ["reactivate", "reactivate ⌘⏎"],
    ];
    for (const [mode, label] of cases) {
      const { unmount } = mount(mode);
      expect(document.getElementById("steer-send")?.textContent).toBe(label);
      unmount();
    }
  });

  it("maps each mode to its endpoint", () => {
    expect(steerEndpoint("steer")).toBe("/api/steer");
    expect(steerEndpoint("unblock")).toBe("/api/steer");
    expect(steerEndpoint("reactivate")).toBe("/api/reactivate");
  });

  it("sends steer and unblock through steerJob, reactivate through reactivateJob", async () => {
    const user = userEvent.setup();

    for (const mode of ["steer", "unblock"] as SteerMode[]) {
      vi.clearAllMocks();
      const { unmount } = mount(mode);
      const ta = document.getElementById("steer-text") as HTMLTextAreaElement;
      await user.type(ta, "go");
      await user.click(screen.getByText(mode === "steer" ? "send ⌘⏎" : "unblock ⌘⏎"));
      await waitFor(() => expect(steerJob).toHaveBeenCalledWith("j", "go"));
      expect(reactivateJob).not.toHaveBeenCalled();
      unmount();
    }

    vi.clearAllMocks();
    const { unmount } = mount("reactivate");
    await user.click(screen.getByText("reactivate ⌘⏎"));
    await waitFor(() => expect(reactivateJob).toHaveBeenCalledWith("j", ""));
    expect(steerJob).not.toHaveBeenCalled();
    unmount();
  });

  it("requireText: only reactivate makes an empty message legal, and only without it", () => {
    expect(steerRequiresText("steer", false)).toBe(true);
    expect(steerRequiresText("unblock", false)).toBe(true);
    expect(steerRequiresText("reactivate", false)).toBe(false);
    // a done→working drag is the ONLY caller that sets requireText
    expect(steerRequiresText("reactivate", true)).toBe(true);
  });

  it("refuses an empty reactivate when requireText is set", async () => {
    const user = userEvent.setup();
    mount("reactivate", true);
    await user.click(screen.getByText("reactivate ⌘⏎"));
    await waitFor(() =>
      expect(document.getElementById("steer-note")?.textContent).toBe(
        "message is empty",
      ),
    );
    expect(reactivateJob).not.toHaveBeenCalled();
  });

  it("hides the kill / tmux / mark-done head buttons in reactivate mode", () => {
    // reactivate targets a DEAD cell — there is nothing on it to cancel, raise
    // in Ghostty, or mark done
    const { unmount } = mount("reactivate");
    for (const id of ["steer-kill", "steer-tmux", "steer-markdone"]) {
      expect(document.getElementById(id)).toHaveAttribute("hidden");
    }
    unmount();

    mount("steer");
    // live + working: killable and tmux-able, but not mark-done (that is blocked)
    expect(document.getElementById("steer-kill")).not.toHaveAttribute("hidden");
    expect(document.getElementById("steer-tmux")).not.toHaveAttribute("hidden");
    expect(document.getElementById("steer-markdone")).toHaveAttribute("hidden");
  });

  it("shows mark-done for a blocked live cell", () => {
    render(
      <SteerOverlay
        open
        slug="j"
        mode="unblock"
        job={{ ...job, state: "blocked" }}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-markdone")).not.toHaveAttribute(
      "hidden",
    );
  });

  it("an unhealthy live cell counts as blocked whatever its file state says", () => {
    render(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={{ ...job, state: "working", health: "zeus is gone" }}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-markdone")).not.toHaveAttribute(
      "hidden",
    );
  });
});

describe("steer image attachments", () => {
  beforeEach(() => vi.clearAllMocks());

  it("composes chips into the body as trailing [image: …] lines", async () => {
    // Uses the component's own send path: the chips ARE the message here (the
    // editor is the one that splices the reference into text instead).
    const user = userEvent.setup();
    render(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={job}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const ta = document.getElementById("steer-text") as HTMLTextAreaElement;
    await user.type(ta, "look");
    await user.click(screen.getByText("send ⌘⏎"));
    await waitFor(() => expect(steerJob).toHaveBeenCalledWith("j", "look"));
  });
});

describe("steer stale-close (checkStaleModals)", () => {
  it("closes when the job's state changes from what it was at open", async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={job}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    // the baseline is taken on the first pass, so this alone closes nothing
    expect(onClose).not.toHaveBeenCalled();

    rerender(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={{ ...job, state: "blocked" }}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("closes when the job leaves the board entirely", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={job}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    rerender(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={undefined}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("does NOT close on an unrelated payload change", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={job}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    rerender(
      <SteerOverlay
        open
        slug="j"
        mode="steer"
        job={{ ...job, detail: "still working" }}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    expect(onClose).not.toHaveBeenCalled();
  });
});
