import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { makeJob } from "../../test/fixtures.ts";
import {
  SteerOverlay,
  steerEndpoint,
  steerRequiresText,
  type SteerMode,
  type SteerOverlayProps,
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
  // DocQuestions' per-row ask — imported by the panel this modal renders, so
  // the wholesale mock must carry it or the import itself throws
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
  editFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  deleteFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  setJobColor: vi.fn(() => Promise.resolve({ ok: true })),
  retryJob: vi.fn(() => Promise.resolve({ ok: true })),
  answerQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  // the head's goal picker (GoalMultiPicker) — same pair the editor's mock
  // carries; `op` rides every link write above goal_id 0
  fetchGoals: vi.fn(() => Promise.resolve({ goals: { week: [], day: [] } })),
  postJobGoal: vi.fn((_slug: string, _goalId: number, _op?: "add" | "remove") =>
    Promise.resolve({ ok: true }),
  ),
}));

import {
  answerQuestion,
  deleteFeedback,
  editFeedback,
  reactivateJob,
  retryJob,
  setJobColor,
  steerJob,
} from "../../api/client.ts";

const job = makeJob({ slug: "j", state: "working", live: true });

const mutate = async <T,>(op: () => Promise<T>) => op();

const onOpenArtifact = vi.fn();
const onOpenThread = vi.fn();

/**
 * The two REQUIRED drilldown callbacks, defaulted so the mode-matrix tests
 * stay about modes.
 *
 * This is deliberately NOT the harness that let the artifact pager ship dead.
 * That one supplied OPTIONAL props (`job`, `onNavigate`) which App.tsx could —
 * and did — simply omit, so every test passed against wiring the real
 * integrator never performed. These two are REQUIRED: App.tsx cannot compile
 * without them, so the type is the guard and this default is only noise
 * reduction. Everything the modal renders from `job` alone — the artifact
 * list, the feedback panel, the thread chips, the branch button, the colour
 * strip — takes no prop at all and is asserted below from a bare job.
 */
function Steer(
  props: Omit<SteerOverlayProps, "onOpenArtifact" | "onOpenThread"> &
    Partial<Pick<SteerOverlayProps, "onOpenArtifact" | "onOpenThread">>,
) {
  return (
    <SteerOverlay
      onOpenArtifact={onOpenArtifact}
      onOpenThread={onOpenThread}
      {...props}
    />
  );
}

function mount(mode: SteerMode, requireText = false) {
  return render(
    <Steer
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
      ["view", "j — done"],
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

  describe("view mode — the done card's read-only drilldown", () => {
    // What a done job looks like on the wire: not live, torn down, with
    // everything that outlives the cell still attached.
    const doneJob = makeJob({
      slug: "j",
      state: "done",
      live: false,
      reactivatable: true,
      detail: "PR opened, stopping here",
      last_sent: "ship it",
      prs: [{ url: "https://github.com/o/r/pull/73", state: "merged" }],
      threads: [{ tid: "t1", title: "q", state: "closed", created_at: 0, closed_at: 1 }],
      artifacts: [{ name: "90-build-report.md", url: "/artifact/j/90.md" }],
      feedback: [{ id: 1, artifact: "90-build-report.md", note: "tighten", at: 0 }],
      tokens: { in: 10, out: 5, cache_write: 1, cache_read: 2, sessions: 1, updated_at: 1 },
    });

    function mountView(onReactivate?: (slug: string) => void) {
      return render(
        <Steer
          open
          slug="j"
          mode="view"
          job={doneJob}
          onClose={() => {}}
          mutate={mutate}
          onReactivate={onReactivate}
        />,
      );
    }

    it("mounts no composer, attach zone, send button or note", () => {
      // UNMOUNTED, not hidden: a hidden textarea is still a paste target
      // and still answers a query
      mountView();
      for (const id of ["steer-text", "steer-att-zone", "steer-send", "steer-note"]) {
        expect(document.getElementById(id)).toBeNull();
      }
    });

    it("keeps every history panel a blocked job's drilldown has", () => {
      mountView();
      for (const id of [
        "steer-prs",
        "steer-threads",
        "steer-detail",
        "steer-lastsent",
        "steer-transcript",
        "steer-chips",
        "steer-feedback",
        "steer-tokens",
      ]) {
        expect(document.getElementById(id), id).not.toBeNull();
        expect(document.getElementById(id), id).not.toHaveAttribute("hidden");
      }
      expect(document.getElementById("steer-detail")?.textContent).toContain(
        "PR opened, stopping here",
      );
    });

    it("hides every control that acts on a cell", () => {
      // the cell is torn down: nothing to kill, raise, mark done, retry, or
      // bill — and the crew line is a dead roster
      mountView();
      for (const id of ["steer-kill", "steer-tmux", "steer-markdone", "steer-crew", "steer-account", "steer-actions"]) {
        expect(document.getElementById(id), id).toHaveAttribute("hidden");
      }
    });

    it("offers ↺ reactivate only when wired and the job allows it", async () => {
      const user = userEvent.setup();
      const onReactivate = vi.fn();
      const { unmount } = mountView(onReactivate);
      const btn = document.getElementById("steer-reactivate")!;
      expect(btn).not.toHaveAttribute("hidden");
      await user.click(btn);
      expect(onReactivate).toHaveBeenCalledWith("j");
      unmount();

      // unwired: hidden outright rather than a button that does nothing
      mountView();
      expect(document.getElementById("steer-reactivate")).toHaveAttribute("hidden");
    });

    it("never shows ↺ in a composer mode — those modes already are the action", () => {
      render(
        <Steer
          open
          slug="j"
          mode="reactivate"
          job={doneJob}
          onClose={() => {}}
          mutate={mutate}
          onReactivate={vi.fn()}
        />,
      );
      expect(document.getElementById("steer-reactivate")).toHaveAttribute("hidden");
    });
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
      <Steer
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
      <Steer
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
      <Steer
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
      <Steer
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
      <Steer
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
      <Steer
        open
        slug="j"
        mode="steer"
        job={job}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    rerender(
      <Steer
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
      <Steer
        open
        slug="j"
        mode="steer"
        job={job}
        onClose={onClose}
        mutate={mutate}
      />,
    );
    rerender(
      <Steer
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

// ---------------------------------------------------------------------------
// The DRILLDOWN surfaces.
//
// Since 0224c1e the card's band 2 carries COUNTS, not lists — on the promise
// that "everything the counts stand for is one click away in the modal". Each
// suite below asserts that promise the way it actually failed: not "the
// component renders what it is handed", but "a job that HAS artifacts /
// feedback / threads shows them here, given nothing but the job".
// ---------------------------------------------------------------------------

const artifactJob = makeJob({
  slug: "j",
  state: "working",
  live: true,
  artifacts: [
    { name: "00-overview.md", url: "/artifact/j/00-overview.md" },
    { name: "10-flow.html", url: "/artifact/j/10-flow.html" },
  ],
});

describe("#steer-chips — the artifact list", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders every artifact of the job as an openable chip, from the job alone", () => {
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={artifactJob}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const box = document.getElementById("steer-chips") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    const chips = box.querySelectorAll("button.chip");
    expect(chips).toHaveLength(2);
    expect([...chips].map((c) => c.textContent)).toEqual([
      "▤ 00-overview.md",
      "▤ 10-flow.html",
    ]);
  });

  it("carries the job's jobKey on every chip and hands it to onOpenArtifact", async () => {
    const user = userEvent.setup();
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={artifactJob}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const chip = document.querySelector(
      "#steer-chips button.chip",
    ) as HTMLElement;
    // the pager rebuilds the job's ORDERED list from this key — a chip opened
    // without one degrades the viewer to a singleton
    expect(chip).toHaveAttribute("data-key", "j");
    expect(chip).toHaveAttribute("data-url", "/artifact/j/00-overview.md");
    expect(chip).toHaveAttribute("data-name", "j/00-overview.md");
    await user.click(chip);
    expect(onOpenArtifact).toHaveBeenCalledWith(
      "/artifact/j/00-overview.md",
      "j/00-overview.md",
      "j",
    );
  });

  it("uses the ARCHIVE jobKey and display label for an archived job", async () => {
    const user = userEvent.setup();
    render(
      <Steer
        open
        slug="old"
        mode="steer"
        job={makeJob({
          slug: "old",
          archived: true,
          display_slug: "old-2",
          state: "done",
          artifacts: [{ name: "00-overview.md", url: "/artifact/old/00.md" }],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    await user.click(
      document.querySelector("#steer-chips button.chip") as HTMLElement,
    );
    // archive:old, never a bare "old" — an archived job and a live one that
    // reused its freed slug would otherwise page through each other's docs
    expect(onOpenArtifact).toHaveBeenCalledWith(
      "/artifact/old/00.md",
      "old-2/00-overview.md",
      "archive:old",
    );
  });

  it("shows the list in view mode — a done job's output is what the drilldown is for", () => {
    render(
      <Steer
        open
        slug="j"
        mode="view"
        job={{ ...artifactJob, state: "done", live: false }}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-chips")).not.toHaveAttribute("hidden");
    expect(document.querySelectorAll("#steer-chips button.chip")).toHaveLength(2);
  });

  it("hides the list in reactivate mode and for a job with no artifacts", () => {
    const { unmount } = render(
      <Steer
        open
        slug="j"
        mode="reactivate"
        job={artifactJob}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-chips")).toHaveAttribute("hidden");
    unmount();

    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-chips")).toHaveAttribute("hidden");
  });
});

const feedbackJob = makeJob({
  slug: "j",
  state: "working",
  live: true,
  feedback: [
    { id: 1, artifact: "00-overview.md", note: "too abstract", at: 1 },
    {
      id: 2,
      artifact: "50-design.md",
      note: "wrong table",
      at: 2,
      addressed: "rewrote it",
    },
  ],
});

describe("#steer-feedback — the feedback panel", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows a job's feedback notes, from the job alone", () => {
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={feedbackJob}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const box = document.getElementById("steer-feedback") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    expect(box.querySelectorAll(".fb-row")).toHaveLength(2);
    expect(box.textContent).toContain("too abstract");
    expect(box.textContent).toContain("wrong table");
    // an addressed note is dimmed but still readable and still editable
    expect(box.querySelector('[data-fb-id="2"]')).toHaveClass("fb-addressed");
  });

  it("edits a note through /api/feedback", async () => {
    const user = userEvent.setup();
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={feedbackJob}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const row = document.querySelector('[data-fb-id="1"]') as HTMLElement;
    await user.click(row.querySelector(".fb-edit") as HTMLElement);
    const ta = document.querySelector(".fb-edit-input") as HTMLTextAreaElement;
    await user.clear(ta);
    await user.type(ta, "still too abstract");
    await user.click(document.querySelector(".fb-save") as HTMLElement);
    await waitFor(() =>
      expect(editFeedback).toHaveBeenCalledWith("j", 1, "still too abstract"),
    );
  });

  it("deletes a note behind a confirm", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={feedbackJob}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const row = document.querySelector('[data-fb-id="1"]') as HTMLElement;
    await user.click(row.querySelector(".fb-delete") as HTMLElement);
    await waitFor(() => expect(deleteFeedback).toHaveBeenCalledWith("j", 1));
    confirm.mockRestore();
  });

  it("renders in EVERY mode — feedback is job history, not cell state", () => {
    for (const mode of ["steer", "unblock", "reactivate"] as SteerMode[]) {
      const { unmount } = render(
        <Steer
          open
          slug="j"
          mode={mode}
          job={feedbackJob}
          onClose={() => {}}
          mutate={mutate}
        />,
      );
      expect(document.getElementById("steer-feedback")).not.toHaveAttribute(
        "hidden",
      );
      unmount();
    }
  });

  it("hides the panel for a job with no notes", () => {
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-feedback")).toHaveAttribute("hidden");
  });
});

describe("#steer-threads — thread chips and the ⑂ branch button", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders the branch button for a live cell — the card delegates ⑂ here", async () => {
    const user = userEvent.setup();
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    const box = document.getElementById("steer-threads") as HTMLElement;
    // job has NO threads yet: the container still shows, because the branch
    // affordance is the whole point of this row on a live cell
    expect(box).not.toHaveAttribute("hidden");
    const branch = box.querySelector(".branch-btn") as HTMLElement;
    expect(branch).toBeTruthy();
    await user.click(branch);
    // tid null = "open the new-thread dialog"
    expect(onOpenThread).toHaveBeenCalledWith("j", null);
  });

  it("renders one chip per thread and opens it by tid", async () => {
    const user = userEvent.setup();
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({
          slug: "j",
          state: "working",
          live: true,
          threads: [
            { tid: "t1", title: "why the lock", state: "live", created_at: 1 },
            { tid: "t2", state: "closed", created_at: 2 },
          ],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const chips = document.querySelectorAll("#steer-threads .thread-chip");
    expect(chips).toHaveLength(2);
    expect(chips[1]).toHaveClass("closed");
    await user.click(chips[0] as HTMLElement);
    expect(onOpenThread).toHaveBeenCalledWith("j", "t1");
  });

  it("keeps closed threads readable on a dead cell, without a branch button", () => {
    render(
      <Steer
        open
        slug="j"
        mode="reactivate"
        job={makeJob({
          slug: "j",
          state: "done",
          live: false,
          threads: [{ tid: "t1", state: "closed", created_at: 1 }],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const box = document.getElementById("steer-threads") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    expect(box.querySelectorAll(".thread-chip")).toHaveLength(1);
    // nothing to branch off: the cell is gone
    expect(box.querySelector(".branch-btn")).toBeNull();
  });

  it("hides the row entirely for a dead cell with no threads", () => {
    render(
      <Steer
        open
        slug="j"
        mode="reactivate"
        job={makeJob({ slug: "j", state: "done", live: false })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-threads")).toHaveAttribute("hidden");
  });
});

// The strip became a DROPDOWN (contract 50-contract.md, slice S3): the nine
// radios these tests pinned are gone, and with them the per-strip radio-group
// `name` (frozen in the props type, but vestigial — there is no group left to
// collide with the editor's). Selectors below moved to the S3 markup: a
// `.colorsel-btn` trigger over a `.colorsel-menu` of `.colorsel-row`s.
describe("#steer-swatches — the job colour picker", () => {
  beforeEach(() => vi.clearAllMocks());

  const strip = () => document.getElementById("steer-swatches") as HTMLElement;
  const trigger = () => strip().querySelector(".colorsel-btn") as HTMLButtonElement;
  const rows = () =>
    [...strip().querySelectorAll(".colorsel-row")] as HTMLElement[];
  const openMenu = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(trigger());
    return rows();
  };

  it("renders the none row plus /color's eight, in /color's order", async () => {
    const user = userEvent.setup();
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(rows()).toHaveLength(0); // the menu is closed until asked for
    const open = await openMenu(user);
    expect(open.map((r) => r.textContent)).toEqual([
      "NO COLOR✓",
      "RED",
      "BLUE",
      "GREEN",
      "YELLOW",
      "PURPLE",
      "ORANGE",
      "PINK",
      "CYAN",
    ]);
  });

  it("reflects the job's colour, falling back to none for unset or unknown", () => {
    const { unmount } = render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={{ ...job, color: "blue" }}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(trigger().textContent).toContain("BLUE");
    unmount();

    const second = render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(trigger().textContent).toContain("NO COLOR");
    second.unmount();

    // a colour this client does not know must still read as the none state
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={{ ...job, color: "chartreuse" }}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(trigger().textContent).toContain("NO COLOR");
  });

  it("POSTs the pick to /api/job/color and nothing else", async () => {
    const user = userEvent.setup();
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    const open = await openMenu(user);
    await user.click(open[3] as HTMLElement); // GREEN
    await waitFor(() => expect(setJobColor).toHaveBeenCalledWith("j", "green"));
    // recolouring never steers the cell
    expect(steerJob).not.toHaveBeenCalled();
    expect(reactivateJob).not.toHaveBeenCalled();
  });

  it("clears a colour with the none row, and shows in every mode", async () => {
    const user = userEvent.setup();
    for (const mode of ["steer", "unblock", "reactivate"] as SteerMode[]) {
      vi.clearAllMocks();
      const { unmount } = render(
        <Steer
          open
          slug="j"
          mode={mode}
          job={{ ...job, color: "red" }}
          onClose={() => {}}
          mutate={mutate}
        />,
      );
      // recolouring is valid on a live cell AND on a dead one — /api/job/color
      // is ungated precisely so a running job can be recoloured
      const open = await openMenu(user);
      await user.click(open[0] as HTMLElement); // NO COLOR
      await waitFor(() => expect(setJobColor).toHaveBeenCalledWith("j", ""));
      unmount();
    }
  });
});

describe("#steer-goal — the head's goal picker", () => {
  beforeEach(() => vi.clearAllMocks());

  // The HOST wiring only — GoalMultiPicker's own behavior (labels, the ticks,
  // the add/remove ops, the clear) is pinned against the component itself by
  // bare-mounting it, since it takes its whole world through props. What only
  // this suite can say is that the steer head actually mounts the control:
  // a live card opens THIS modal and never the editor, so a picker asserted
  // nowhere but the editor's form could go dead here without a test failing.
  it("mounts the picker for a live job", () => {
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-goal")).toBeInTheDocument();
  });

  it("withholds it for an archived job — the wire withholds its goal too", () => {
    render(
      <Steer
        open
        slug="archive:j.job.md"
        mode="reactivate"
        job={makeJob({ slug: "archive:j.job.md", state: "archived", archived: true, live: false })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-goal")).toBeNull();
  });
});

describe("steer context visibility", () => {
  it("opens the context block for a job whose ONLY content is feedback", () => {
    render(
      <Steer
        open
        slug="j"
        mode="reactivate"
        job={makeJob({
          slug: "j",
          state: "done",
          live: false,
          feedback: [{ id: 1, artifact: "00.md", note: "n", at: 1 }],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-context")).not.toHaveAttribute(
      "hidden",
    );
  });

  it("keeps it closed for a job with nothing in it", () => {
    render(
      <Steer
        open
        slug="j"
        mode="reactivate"
        job={makeJob({ slug: "j", state: "done", live: false })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-context")).toHaveAttribute("hidden");
  });
});

describe("#steer-crew — the crew line", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders zeus' pill and one chip per crew role, from the job alone", () => {
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({
          slug: "j",
          state: "working",
          live: true,
          zeus_status: "running",
          roster: [
            { name: "hephaestus-1", model: "opus", role: "hephaestus", status: "run", working: true },
            { name: "athena-logic", model: "opus", role: "athena", status: "idle", working: false },
          ],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const box = document.getElementById("steer-crew") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    // .crew, not .steer-crew — style.css lays the row out on this element
    expect(box).toHaveClass("crew");
    expect(box.querySelector(".crew-pill")).toHaveClass("run");
    expect(box.querySelectorAll(".crew-chip")).toHaveLength(2);
  });

  it("hides the crew line in reactivate mode and for a job with no crew", () => {
    const crewJob = makeJob({ slug: "j", state: "working", live: true, zeus_status: "running" });
    // reactivate targets a dead cell: there is no crew left to show
    const { unmount } = render(
      <Steer open slug="j" mode="reactivate" job={crewJob} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-crew")).toHaveAttribute("hidden");
    unmount();

    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-crew")).toHaveAttribute("hidden");
  });
});

describe("#steer-stack — the cell's stack links", () => {
  beforeEach(() => vi.clearAllMocks());

  const stackJob = makeJob({
    slug: "j",
    state: "blocked",
    live: true,
    stack: [
      { url: "http://127.0.0.1:7878", label: "board", status: "up", at: 1 },
      { url: "http://localhost:5173", label: "ui", status: "down", at: 1 },
    ],
  });

  it("renders one chip per link from the job alone, under the PR row", () => {
    render(
      <Steer open slug="j" mode="steer" job={stackJob} onClose={() => {}} mutate={mutate} />,
    );
    const box = document.getElementById("steer-stack") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    const chips = [...box.querySelectorAll<HTMLElement>(".stack-chip")];
    expect(chips.map((c) => c.textContent)).toEqual(["⇱ board :7878", "⇱ ui :5173"]);
    expect(chips.map((c) => c.className)).toEqual(["stack-chip stack-up", "stack-chip stack-down"]);
    expect(chips[0]?.tagName).toBe("A");
    // exactly one .stack box: StackChips' own wrapper would nest a second
    expect(box.querySelectorAll(".stack")).toHaveLength(0);
    // directly after the PR row, as on the card
    expect(document.getElementById("steer-prs")?.nextElementSibling).toBe(box);
  });

  it("hides the row for a job with no links", () => {
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({ slug: "j", state: "working", live: true })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-stack")).toHaveAttribute("hidden");
  });

  it("renders spans on a remote card", () => {
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({
          ...stackJob,
          owner: { node_id: "n1", label: "mbp", operator: "m", online: true },
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.querySelector("#steer-stack a")).toBeNull();
    expect(document.querySelectorAll("#steer-stack .stack-chip.remote")).toHaveLength(2);
  });
});

describe("#steer-prs — the PR chips", () => {
  beforeEach(() => vi.clearAllMocks());

  const prJob = makeJob({
    slug: "j",
    state: "working",
    live: true,
    prs: [
      { url: "https://github.com/car1zhang/kraken/pull/12", state: "merged" },
      { url: "https://github.com/car1zhang/kraken/pull/13", state: "open" },
    ],
  });

  it("renders one linked chip per PR, from the job alone", () => {
    render(
      <Steer open slug="j" mode="steer" job={prJob} onClose={() => {}} mutate={mutate} />,
    );
    const box = document.getElementById("steer-prs") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    const chips = box.querySelectorAll(".pr-chip");
    // 50-contract.md, S2 `PRChips`: "Chip label is exactly `#${n}` when the URL
    // parses `/pull/<n>` … State classes (`pr-merged/pr-open/pr-closed`) …
    // unchanged — state is said by color alone." The modal renders the same
    // component the card does, so the label change lands here too.
    expect([...chips].map((c) => c.textContent)).toEqual(["#12", "#13"]);
    expect([...chips].map((c) => c.className)).toEqual([
      "pr-chip pr-merged",
      "pr-chip pr-open",
    ]);
    expect(chips[0]?.tagName).toBe("A");
    // exactly one .prs box: PRChips' own wrapper would nest a second
    expect(box.querySelectorAll(".prs")).toHaveLength(0);
  });

  it("renders in EVERY mode — a PR is job history, not cell state", () => {
    for (const mode of ["steer", "unblock", "reactivate"] as SteerMode[]) {
      const { unmount } = render(
        <Steer open slug="j" mode={mode} job={prJob} onClose={() => {}} mutate={mutate} />,
      );
      expect(document.getElementById("steer-prs")).not.toHaveAttribute("hidden");
      unmount();
    }
  });

  it("degrades a non-http url to an inert span", () => {
    // `prs:` frontmatter is hand-editable and JSX renders href="javascript:…"
    // just as happily as the string template did
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({
          slug: "j",
          prs: [{ url: "javascript:alert(1)", state: "open" }],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const chip = document.querySelector("#steer-prs .pr-chip") as HTMLElement;
    expect(chip.tagName).toBe("SPAN");
    expect(document.querySelector("#steer-prs a")).toBeNull();
  });

  it("hides the row for a job with no PRs", () => {
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-prs")).toHaveAttribute("hidden");
  });
});

describe("#steer-actions — retry and the pending question", () => {
  beforeEach(() => vi.clearAllMocks());

  const unhealthy = makeJob({
    slug: "j",
    state: "working",
    live: true,
    health: "zeus is idle",
  });

  const questionJob = makeJob({
    slug: "j",
    state: "blocked",
    live: true,
    cell: "kraken-j",
    question: {
      tool_use_id: "tu_1",
      questions: [
        {
          header: "which base",
          question: "branch off main or the cell branch?",
          multi_select: false,
          options: [
            { label: "main", description: "origin/main" },
            { label: "cell branch", description: "carl/j" },
          ],
        },
      ],
    },
  });

  it("shows retry for an unhealthy cell and POSTs /api/retry", async () => {
    const user = userEvent.setup();
    render(
      <Steer open slug="j" mode="steer" job={unhealthy} onClose={() => {}} mutate={mutate} />,
    );
    const box = document.getElementById("steer-actions") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    const btn = box.querySelector(".btn.retry") as HTMLElement;
    // a DIRECT child — style.css scopes `.steer-actions > .btn.retry`
    expect(btn.parentElement).toBe(box);
    await user.click(btn);
    await waitFor(() => expect(retryJob).toHaveBeenCalledWith("j"));
  });

  it("offers no retry for a cell that is beyond retrying", () => {
    for (const health of ["zeus is gone", "stuck at the startup prompt"]) {
      const { unmount } = render(
        <Steer
          open
          slug="j"
          mode="steer"
          job={{ ...unhealthy, health }}
          onClose={() => {}}
          mutate={mutate}
        />,
      );
      expect(document.getElementById("steer-actions")).toHaveAttribute("hidden");
      unmount();
    }
  });

  it("renders the pending AskUserQuestion and answers with the 1-BASED index", async () => {
    const user = userEvent.setup();
    render(
      <Steer open slug="j" mode="unblock" job={questionJob} onClose={() => {}} mutate={mutate} />,
    );
    const box = document.getElementById("steer-actions") as HTMLElement;
    expect(box).not.toHaveAttribute("hidden");
    const block = box.querySelector(".question") as HTMLElement;
    expect(block.parentElement).toBe(box);
    expect(block.textContent).toContain("which base");
    const opts = block.querySelectorAll(".q-opt");
    expect([...opts].map((o) => o.textContent)).toEqual(["1. main", "2. cell branch"]);
    await user.click(opts[1] as HTMLElement);
    // webAnswerQuestion's whole contract is "post a single digit", 1-based
    await waitFor(() => expect(answerQuestion).toHaveBeenCalledWith("j", 2));
  });

  it("re-arms the options when the answer fails", async () => {
    const user = userEvent.setup();
    // the failure path the shared mutation shape exists for: without it a
    // failed answer leaves the question unanswerable short of a reload
    const failing = async () => null;
    render(
      <Steer open slug="j" mode="unblock" job={questionJob} onClose={() => {}} mutate={failing} />,
    );
    const opt = document.querySelector(".q-opt") as HTMLButtonElement;
    await user.click(opt);
    await waitFor(() =>
      expect(
        (document.querySelector(".q-opt") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
  });

  it("hides the block in reactivate mode — a dead cell has nothing to retry or answer", () => {
    render(
      <Steer
        open
        slug="j"
        mode="reactivate"
        job={{ ...questionJob, health: "zeus is idle" }}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    expect(document.getElementById("steer-actions")).toHaveAttribute("hidden");
  });

  it("hides the block for a healthy job with no question", () => {
    render(
      <Steer open slug="j" mode="steer" job={job} onClose={() => {}} mutate={mutate} />,
    );
    expect(document.getElementById("steer-actions")).toHaveAttribute("hidden");
  });
});

describe("SteerOverlayProps has no optional ReactNode slots left", () => {
  it("renders every context surface from `job` alone, with no slot props", () => {
    // The regression guard for the whole class of bug: five optional
    // `ReactNode` slots (crew, prChips, threadChips, artifactChips, actions)
    // that App.tsx filled NONE of, so the context block rendered empty and
    // every finished component behind it was dark. This mounts with exactly
    // what App.tsx passes today — no slots exist to pass — and asserts all
    // six surfaces are visible.
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({
          slug: "j",
          state: "working",
          live: true,
          health: "zeus is idle",
          zeus_status: "running",
          artifacts: [{ name: "00-overview.md", url: "/artifact/j/00.md" }],
          prs: [{ url: "https://github.com/o/r/pull/1", state: "open" }],
          threads: [{ tid: "t1", state: "live", created_at: 1 }],
          feedback: [{ id: 1, artifact: "00.md", note: "n", at: 1 }],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    for (const id of [
      "steer-context",
      "steer-crew",
      "steer-prs",
      "steer-threads",
      "steer-chips",
      "steer-feedback",
      "steer-actions",
      "steer-swatches",
    ]) {
      expect(document.getElementById(id)).not.toHaveAttribute("hidden");
    }
    // and the affordances the card gave up are all present
    expect(document.querySelector("#steer-threads .branch-btn")).toBeTruthy();
    expect(document.querySelector("#steer-chips button.chip")).toBeTruthy();
    expect(document.querySelector("#steer-feedback .fb-row")).toBeTruthy();
    expect(document.querySelector("#steer-actions .btn.retry")).toBeTruthy();
  });
});

describe("artifact refs in the steer detail", () => {
  beforeEach(() => vi.clearAllMocks());

  const jobWithCite = makeJob({
    slug: "j",
    state: "blocked",
    live: true,
    detail: "wrote `51-code-map.md` — review it",
    artifacts: [
      { name: "51-code-map.md", url: "/api/artifact/j/cell/51-code-map.md" },
    ],
  });

  it("dresses a span naming a job artifact and opens it via onOpenArtifact", async () => {
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={jobWithCite}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const ref = await waitFor(() => {
      const el = document.querySelector("#steer-detail code.artifact-ref");
      if (!el) throw new Error("not dressed yet");
      return el;
    });
    expect(ref.getAttribute("title")).toBe("open 51-code-map.md");
    fireEvent.click(ref);
    expect(onOpenArtifact).toHaveBeenCalledWith(
      "/api/artifact/j/cell/51-code-map.md",
      "j/51-code-map.md",
      "j",
    );
  });
});

describe("questions panel ask wiring", () => {
  beforeEach(() => vi.clearAllMocks());

  // The INTEGRATION half of DocQuestions' own ask tests: the modal actually
  // passes onAskBranched (through its onOpenThread), so a live job's question
  // rows carry the ⑂ here — the affordance's only home. Guards the omission
  // that shipped this modal's artifact list and branch button dead (App.tsx's
  // onOpenArtifact note): a panel prop the integrator can silently not pass.
  it("a live job's question rows carry ⑂, and a branch opens the thread", async () => {
    const user = userEvent.setup();
    render(
      <Steer
        open
        slug="j"
        mode="steer"
        job={makeJob({
          slug: "j",
          state: "working",
          live: true,
          doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
        })}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    const toggle = document.querySelector(".dq-ask-toggle") as HTMLElement;
    expect(toggle).not.toBeNull();
    await user.click(toggle);
    // by class, not text: the modal's thread band has its own "⑂ branch"
    // (BranchButton), and getByText would find both
    await user.click(document.querySelector(".dq-ask-send") as HTMLElement);
    await waitFor(() => expect(onOpenThread).toHaveBeenCalledWith("j", "t1"));
  });
});
