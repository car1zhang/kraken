import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { JobForm, WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { EditorOverlay } from "./EditorOverlay.tsx";

const form: JobForm = {
  slug: "j",
  repo: "kraken",
  base: "origin/main",
  model: "",
  editable: true,
  spec: "",
  mtime: 7,
  repos: ["kraken", "monolith"],
  slugs: ["other-job"],
};

vi.mock("../../api/client.ts", () => ({
  fetchJobForm: vi.fn(() => Promise.resolve({ ...form })),
  saveJobForm: vi.fn(() => Promise.resolve({ ok: true, mtime: 8 })),
  createJob: vi.fn((slug: string) => Promise.resolve({ ...form, slug })),
  queueJob: vi.fn(() => Promise.resolve({ ok: true })),
  archiveJob: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(),
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  editFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  deleteFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  setJobColor: vi.fn(() => Promise.resolve({ ok: true })),
}));

import {
  createJob,
  deleteFeedback,
  editFeedback,
  fetchJobForm,
  queueJob,
  saveJobForm,
  setJobColor,
  uploadImage,
} from "../../api/client.ts";

const mutate = async <T,>(op: () => Promise<T>) => op();

const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug: "j", ...over });

const noop = () => {};

function mount(props: Partial<Parameters<typeof EditorOverlay>[0]> = {}) {
  return render(
    <EditorOverlay
      open
      slug="j"
      job={job()}
      onClose={noop}
      onCreated={noop}
      onReactivate={noop}
      onOpenArtifact={noop}
      mutate={mutate}
      {...props}
    />,
  );
}

describe("EditorOverlay modes", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  it("new-job mode shows the slug row and no form", () => {
    mount({ slug: null, job: undefined });
    expect(document.getElementById("editor-title")?.textContent).toBe("new job");
    expect(document.getElementById("editor-newrow")).not.toHaveAttribute("hidden");
    expect(document.getElementById("editor-form")).toHaveAttribute("hidden");
    expect(fetchJobForm).not.toHaveBeenCalled();
  });

  it("refuses a create with no slug and posts one with a slug", async () => {
    const user = userEvent.setup();
    const onCreated = vi.fn();
    mount({ slug: null, job: undefined, onCreated });
    await user.click(screen.getByText("create"));
    expect(document.getElementById("editor-note")?.textContent).toBe(
      "give it a slug",
    );
    expect(createJob).not.toHaveBeenCalled();

    await user.type(document.getElementById("editor-slug") as HTMLInputElement, "newjob");
    await user.click(screen.getByText("create"));
    await waitFor(() => expect(createJob).toHaveBeenCalledWith("newjob"));
    expect(onCreated).toHaveBeenCalledWith("newjob");
  });

  it("editable mode fills every field from the form payload", async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() => expect(fetchJobForm).toHaveBeenCalledWith("j", expect.anything()));
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    // the repo selector is a custom dropdown now — the id sits on its button
    const repoBtn = document.getElementById("f-repo") as HTMLButtonElement;
    expect(repoBtn.tagName).toBe("BUTTON");
    expect(repoBtn).toHaveTextContent("kraken");
    expect((document.getElementById("f-base") as HTMLInputElement).value).toBe(
      "origin/main",
    );
    // both real checkouts plus the "—" row
    await user.click(repoBtn);
    expect(
      document.querySelectorAll(".colorsel-menu .colorsel-row").length,
    ).toBe(3);
  });

  it("keeps an unusual repo as an extra row rather than resetting it to —", async () => {
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form, repo: "somewhere-else" }),
    );
    const user = userEvent.setup();
    mount();
    await waitFor(() =>
      expect(document.getElementById("f-repo")).toHaveTextContent("somewhere-else"),
    );
    await user.click(document.getElementById("f-repo") as HTMLButtonElement);
    expect(document.querySelectorAll(".colorsel-menu .colorsel-row").length).toBe(4);
  });

  it("a repo pick lands in the autosave payload", async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    await user.click(document.getElementById("f-repo") as HTMLButtonElement);
    await user.click(screen.getByText("monolith"));
    // the menu closed on the pick, and the button shows the new value
    expect(document.querySelector(".colorsel-menu")).toBeNull();
    expect(document.getElementById("f-repo")).toHaveTextContent("monolith");
    await waitFor(() => expect(saveJobForm).toHaveBeenCalled());
    const body = (saveJobForm as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as {
      repo: string;
    };
    expect(body.repo).toBe("monolith");
  });

  it("read-only mode locks every control and says why", async () => {
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form, editable: false, spec: "running spec" }),
    );
    mount({ job: job({ state: "working", live: true }) });
    await waitFor(() =>
      expect(document.getElementById("editor-hint")?.textContent).toBe(
        "read-only — cell in flight",
      ),
    );
    expect(document.getElementById("f-repo")).toBeDisabled();
    expect(document.getElementById("f-base")).toBeDisabled();
    expect(document.getElementById("f-model")).toBeDisabled();
    expect(document.getElementById("f-solo")).toBeDisabled();
    // `f-add-prereq` was in this list until the prereq block went away with
    // the queue it gated.
    expect(document.getElementById("editor-att-btn")).toBeDisabled();
    // the spec stays READONLY rather than disabled, so it is still
    // selectable/copyable
    const spec = document.getElementById("editor-text") as HTMLTextAreaElement;
    expect(spec).toHaveAttribute("readonly");
    expect(spec).not.toBeDisabled();
    // and the chip row and archive go away entirely
    expect(document.getElementById("editor-cmd-chips")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-archive")).toHaveAttribute("hidden");
  });
});

describe("the submit affordance is derived, not snapshotted", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  it("an empty-spec draft shows the button, disabled, with a hint", async () => {
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-submit")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-submit")).toBeDisabled();
    expect(document.getElementById("editor-hint")?.textContent).toBe(
      "write a spec to start",
    );
  });

  it("arms the moment a spec is typed", async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "do it");
    await waitFor(() =>
      expect(document.getElementById("editor-submit")).not.toBeDisabled(),
    );
    expect(document.getElementById("editor-hint")?.textContent).toBe(
      "synced ✓ — ready to start",
    );
  });

  it("a job MISSING from the board is treated as a fresh draft, not as unarmable", async () => {
    // this is the bug the reactive derivation fixed: a job created seconds ago
    // is not in the board payload yet, and the one-shot snapshot left it
    // permanently unsubmittable until a close/reopen
    const user = userEvent.setup();
    mount({ job: undefined });
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "x");
    await waitFor(() =>
      expect(document.getElementById("editor-submit")).not.toBeDisabled(),
    );
  });

  it("a form-level warn disarms it", async () => {
    (saveJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ok: true, mtime: 8, warn: "no repo" }),
    );
    const user = userEvent.setup();
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "x");
    await waitFor(() =>
      expect(document.getElementById("editor-note")?.textContent).toBe(
        "synced — won't run: no repo",
      ),
    );
    expect(document.getElementById("editor-submit")).toBeDisabled();
  });

  it("a board-level err disarms it too", async () => {
    const user = userEvent.setup();
    mount({ job: job({ err: "unhealthy cell" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "x");
    await waitFor(() => expect(saveJobForm).toHaveBeenCalled());
    expect(document.getElementById("editor-submit")).toBeDisabled();
  });

  // "a QUEUED job offers the reverse move instead of the forward one" stood
  // here, pinning `editor-dequeue` and the "queued — cancel returns it to
  // draft" hint. Both are obsoleted: there is no queued state and no reverse
  // move, only a submit that has been answered or has not.
  it("a working job offers no submit", async () => {
    mount({ job: job({ state: "working", live: true }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-submit")).toHaveAttribute("hidden");
  });
});

describe("autosave", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
    (saveJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ok: true, mtime: 8 }),
    );
  });

  it("does NOT fire on the initial load — only on a real edit", async () => {
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    // give the 600ms debounce more than enough room
    await new Promise((r) => setTimeout(r, 50));
    expect(saveJobForm).not.toHaveBeenCalled();
  });

  it("posts mtime 0 — deliberately no optimistic lock", async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "hi");
    await waitFor(() => expect(saveJobForm).toHaveBeenCalled());
    const body = (saveJobForm as ReturnType<typeof vi.fn>).mock.calls[0]?.[0];
    expect(body).toMatchObject({ slug: "j", spec: "hi", mtime: 0 });
  });

  // Two prereq tests stood here — "drops empty prereq rows from the payload"
  // and "a removed prereq row lands immediately in the payload". Both are
  // obsoleted pins: the block is gone from the form and the key is off the
  // POST shape entirely. Nothing in this file replaces them — that the form
  // now sends neither `ready` nor `prereqs` is a SPEC assertion and belongs to
  // the blind suites, not to the implementer's own file.
});

describe("editor stale-close, with the LAZY baseline", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  it("a job that is not on the board yet does not close the editor", async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <EditorOverlay
        open
        slug="j"
        job={undefined}
        onClose={onClose}
        onCreated={noop}
        onReactivate={noop}
        onOpenArtifact={noop}
        mutate={mutate}
      />,
    );
    // several polls pass with the job still absent — this is the just-created
    // case and must NOT be read as a state change
    for (let i = 0; i < 3; i++) {
      rerender(
        <EditorOverlay
          open
          slug="j"
          job={undefined}
          onClose={onClose}
          onCreated={noop}
          onReactivate={noop}
          onOpenArtifact={noop}
          mutate={mutate}
        />,
      );
    }
    expect(onClose).not.toHaveBeenCalled();

    // the board catches up: THAT is when the baseline is taken
    rerender(
      <EditorOverlay
        open
        slug="j"
        job={job({ state: "draft" })}
        onClose={onClose}
        onCreated={noop}
        onReactivate={noop}
        onOpenArtifact={noop}
        mutate={mutate}
      />,
    );
    expect(onClose).not.toHaveBeenCalled();

    // and now a real change closes it
    rerender(
      <EditorOverlay
        open
        slug="j"
        job={job({ state: "working" })}
        onClose={onClose}
        onCreated={noop}
        onReactivate={noop}
        onOpenArtifact={noop}
        mutate={mutate}
      />,
    );
    expect(onClose).toHaveBeenCalled();
  });

  // "dequeue rebases the baseline so our OWN action does not close the editor"
  // stood here. It is obsoleted with the button it drove: dequeue was the one
  // action in this modal that moved the job BACKWARDS and so had to rebase the
  // stale baseline before posting. Submit moves it forwards and CLOSES the
  // modal on success, so the baseline has nothing left to protect.
});

describe("editor head actions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  it("↺ reactivate hands off to the steer modal rather than posting", async () => {
    const user = userEvent.setup();
    const onReactivate = vi.fn();
    mount({
      job: job({ state: "done", reactivatable: true }),
      onReactivate,
    });
    await waitFor(() =>
      expect(document.getElementById("editor-reactivate")).not.toHaveAttribute(
        "hidden",
      ),
    );
    await user.click(screen.getByText("↺ reactivate"));
    expect(onReactivate).toHaveBeenCalledWith("j");
    expect(queueJob).not.toHaveBeenCalled();
  });

  it("hides ↺ for a done job that is not reactivatable, and for an archived one", async () => {
    const { unmount } = mount({ job: job({ state: "done" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-reactivate")).toHaveAttribute("hidden");
    unmount();

    // an archived job never shows the form at all (see the archived-branch
    // describe below), so ↺ is unreachable by a second route as well
    mount({
      slug: "archive:j",
      job: job({ slug: "j", state: "done", reactivatable: true, archived: true }),
    });
    expect(document.getElementById("editor-form")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-reactivate")).toHaveAttribute("hidden");
  });

  it("questions render ABOVE the form, in their own panel", async () => {
    mount({
      job: job({
        state: "done",
        doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
      }),
    });
    await waitFor(() =>
      expect(document.getElementById("editor-docq")).not.toHaveAttribute("hidden"),
    );
    expect(document.querySelector("#editor-docq .doc-questions")).not.toBeNull();
    expect(document.querySelector(".editor-docq-head")?.textContent).toBe(
      "questions",
    );
    const docq = document.getElementById("editor-docq") as HTMLElement;
    const formEl = document.getElementById("editor-form") as HTMLElement;
    expect(
      docq.compareDocumentPosition(formEl) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });
});

// ---------------------------------------------------------------- #editor-chips
//
// Since the card reorg the board card carries a bare artifact COUNT, and a
// done or archived card opens THIS modal rather than the steer one. That makes
// the list below the only surface a settled job's docs are reachable from at
// all — 69 archived jobs' worth.
describe("the editor's artifact list", () => {
  const artifacts = [
    { name: "00-overview.md", url: "/api/artifact/j/artifacts/00-overview.md" },
    { name: "13-tree.html", url: "/api/artifact/j/artifacts/13-tree.html" },
    { name: "90-build-report.md", url: "/api/artifact/j/artifacts/90-build-report.md" },
  ];

  it("is hidden for a job with no artifacts", async () => {
    mount({ job: job({ state: "done" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-chips")).toHaveAttribute("hidden");
  });

  it("lists a settled job's artifacts in full — no 3-chip cap, no +N more", () => {
    mount({ job: job({ state: "done", artifacts }) });
    const chips = document.querySelectorAll("#editor-chips .chip");
    expect([...chips].map((c) => c.textContent)).toEqual([
      "▤ 00-overview.md",
      "▤ 13-tree.html",
      "▤ 90-build-report.md",
    ]);
    expect(document.querySelector(".chip-more")).toBeNull();
    expect(document.querySelector("#editor-chips .editor-docq-head")?.textContent)
      .toBe("artifacts");
  });

  // web/artifactpager_test.go's TestArtifactChipsCarryJobKey. A chip knows its
  // own url and name and NOTHING else, so nothing downstream can learn the
  // ordered list it sits in from the chip alone — the jobKey is what gets back
  // to the jobs map. Lose it and the pager degrades to a singleton on every
  // artifact, which reads as "the feature was never built" rather than as a
  // bug, and a live cell's newly-written artifacts stay unreachable until the
  // viewer is closed and reopened.
  //
  // React carries it as a prop (see the click test below), so the attribute is
  // no longer load-bearing — it is kept because the vanilla stylesheet and the
  // screenshot diff both see this DOM, and asserted here so a port that drops
  // the PROP is caught by the test above and one that drops the DOM by this.
  it("emits the same data-url / data-key / data-name triple the chips always did", () => {
    mount({ job: job({ state: "done", artifacts }) });
    const chip = document.querySelector("#editor-chips .chip") as HTMLElement;
    expect(chip.dataset.url).toBe("/api/artifact/j/artifacts/00-overview.md");
    expect(chip.dataset.key).toBe("j");
    expect(chip.dataset.name).toBe("j/00-overview.md");
  });

  it("opens the artifact viewer on a click, carrying the jobKey the pager needs", async () => {
    const user = userEvent.setup();
    const onOpenArtifact = vi.fn();
    mount({ job: job({ state: "done", artifacts }), onOpenArtifact });
    await user.click(screen.getByText("▤ 13-tree.html"));
    // (url, name, jobKey) — the third argument is what lets the viewer rebuild
    // this job's ORDERED list; without it every artifact opens as a singleton
    // with the pager hidden
    expect(onOpenArtifact).toHaveBeenCalledWith(
      "/api/artifact/j/artifacts/13-tree.html",
      "j/13-tree.html",
      "j",
    );
  });

  it("carries the ARCHIVE jobKey and the display label for an archived job", async () => {
    const user = userEvent.setup();
    const onOpenArtifact = vi.fn();
    mount({
      job: job({ state: "done", archived: true, display_slug: "old-name", artifacts }),
      onOpenArtifact,
    });
    await user.click(screen.getByText("▤ 00-overview.md"));
    // "archive:j", never a bare "j": an archived job and an active one that
    // reused its freed slug would otherwise open each other's artifacts
    expect(onOpenArtifact).toHaveBeenCalledWith(
      "/api/artifact/j/artifacts/00-overview.md",
      "old-name/00-overview.md",
      "archive:j",
    );
  });

  it("sits above the questions panel and the form", () => {
    mount({
      job: job({
        state: "done",
        artifacts,
        doc_questions: [{ id: "Q1", text: "which?", importance: "P0" }],
      }),
    });
    const chips = document.getElementById("editor-chips") as HTMLElement;
    const docq = document.getElementById("editor-docq") as HTMLElement;
    expect(
      chips.compareDocumentPosition(docq) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });
});

// ---------------------------------------------------------- the archived job
//
// An archived job's key is jobKey's synthetic `archive:<slug>` — there is no
// such job file, so `GET /api/job/form?slug=archive:j` 404s. The whole branch
// exists so the modal never asks (app.js:3815).
describe("an archived job", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  const archived = (over: Partial<WebJob> = {}) =>
    job({ slug: "j", state: "done", archived: true, ...over });

  const mountArchived = (over: Partial<WebJob> = {}) =>
    mount({ slug: "archive:j", job: archived(over) });

  it("asks the server for NO form", async () => {
    mountArchived();
    // waitFor rather than a bare assertion: a request issued from an effect
    // would land a tick later and a synchronous check would miss it
    await waitFor(() => expect(document.getElementById("editor-hint")?.textContent)
      .toBe("read-only · unarchive to edit"));
    expect(fetchJobForm).not.toHaveBeenCalled();
    // …and therefore never paints "no such job" over a modal that is working
    expect(document.getElementById("editor-note")?.textContent).toBe("");
  });

  it("is titled '<slug> — archived', not 'archive:j.job.md'", () => {
    mountArchived();
    expect(document.getElementById("editor-title")?.textContent).toBe(
      "j — archived",
    );
  });

  it("uses the display label of a RENAMED archive", () => {
    mountArchived({ display_slug: "old-name" });
    expect(document.getElementById("editor-title")?.textContent).toBe(
      "old-name — archived",
    );
  });

  it("shows no form and no editing affordances", () => {
    mountArchived({ reactivatable: true });
    expect(document.getElementById("editor-form")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-archive")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-submit")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-reactivate")).toHaveAttribute("hidden");
  });

  // The read-only flag is the ONE gate on both upload paths, and with `form`
  // left null the ordinary `!!form && !editable` rule reads FALSE — so without
  // the explicit archived arm a ⌘V here uploads against "archive:j" and paints
  // the server's second error over a modal that just said it was read-only.
  it("arms neither the paste layer nor the drop sink", async () => {
    mountArchived();
    const file = new File(["x"], "shot.png", { type: "image/png" });
    const ev = new Event("paste") as Event & { clipboardData: unknown };
    Object.defineProperty(ev, "clipboardData", {
      value: { items: [{ kind: "file", getAsFile: () => file }] },
    });
    document.dispatchEvent(ev);

    const panel = document.querySelector("#editor-overlay .panel") as HTMLElement;
    fireEvent.drop(panel, { dataTransfer: { files: [file], types: ["Files"] } });

    await waitFor(() => expect(document.getElementById("editor-title")?.textContent)
      .toBe("j — archived"));
    expect(uploadImage).not.toHaveBeenCalled();
  });

  it("still reaches its artifacts and its feedback — the point of the modal", () => {
    mountArchived({
      artifacts: [{ name: "00-overview.md", url: "/api/artifact/j/artifacts/00-overview.md" }],
      feedback: [{ id: 1, artifact: "00-overview.md", note: "wrong arrow", at: 1 }],
    });
    expect(document.getElementById("editor-chips")).not.toHaveAttribute("hidden");
    expect(document.getElementById("editor-feedback")).not.toHaveAttribute("hidden");
  });
});

// ------------------------------------------------------- #editor-feedback
//
// Before this the React board had NO reader for a feedback row anywhere: the
// card carries a count chip whose title says to click in, and neither
// drilldown re-homed the panel. Those rows are the dream loop's inbound
// channel from the board.
describe("the editor's feedback panel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  const withNotes = (over: Partial<WebJob> = {}) =>
    job({
      state: "done",
      feedback: [
        { id: 7, artifact: "00-overview.md", note: "the arrow points the wrong way", at: 1 },
        { id: 8, artifact: "13-tree.html", note: "addressed one", at: 2, addressed: "fixed" },
      ],
      ...over,
    });

  it("is hidden for a job with no notes", async () => {
    mount({ job: job({ state: "done" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-feedback")).toHaveAttribute("hidden");
  });

  it("renders every note under its own head", () => {
    mount({ job: withNotes() });
    expect(document.getElementById("editor-feedback")).not.toHaveAttribute("hidden");
    expect(
      document.querySelector("#editor-feedback .editor-docq-head")?.textContent,
    ).toBe("feedback");
    expect(document.querySelectorAll("#editor-feedback .fb-row")).toHaveLength(2);
    expect(screen.getByText("the arrow points the wrong way")).toBeTruthy();
  });

  it("edits a note through the board's own write path", async () => {
    const user = userEvent.setup();
    mount({ job: withNotes() });
    await user.click(document.querySelector("#editor-feedback .fb-edit") as HTMLElement);
    const box = document.querySelector(".fb-edit-input") as HTMLTextAreaElement;
    await user.clear(box);
    await user.type(box, "reworded");
    await user.click(document.querySelector(".fb-save") as HTMLElement);
    await waitFor(() => expect(editFeedback).toHaveBeenCalledWith("j", 7, "reworded"));
  });

  it("deletes a note behind a confirm", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    mount({ job: withNotes() });
    await user.click(document.querySelector("#editor-feedback .fb-delete") as HTMLElement);
    await waitFor(() => expect(deleteFeedback).toHaveBeenCalledWith("j", 7));
  });
});

// ------------------------------------------------------------ colour strip
describe("the job colour strip", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  const ready = async () => {
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
  };

  // The strip became a DROPDOWN (contract 50-contract.md, slice S3): a
  // `.colorsel-btn` showing the current colour, and a `.colorsel-menu` of
  // `.colorsel-row`s opened on demand. The nine radios these tests used to pin
  // no longer exist, so every selector below moved to the frozen S3 markup.
  const trigger = () =>
    document.querySelector("#f-swatches .colorsel-btn") as HTMLButtonElement;
  const openMenu = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(trigger());
    return [
      ...document.querySelectorAll<HTMLElement>("#f-swatches .colorsel-row"),
    ];
  };

  it("offers 'no colour' plus the eight palette names, in /color's order", async () => {
    const user = userEvent.setup();
    mount({ job: job() });
    await ready();
    const rows = await openMenu(user);
    expect(rows.map((r) => r.textContent)).toEqual([
      "NO COLOR✓", "RED", "BLUE", "GREEN", "YELLOW", "PURPLE", "ORANGE", "PINK", "CYAN",
    ]);
    // "no colour" is a REAL row, so a job with none still has one selected and
    // a coloured job has a way back to neutral
    expect(rows[0]).toHaveClass("sel");
    expect(rows[0]).toHaveAttribute("aria-selected", "true");
  });

  it("reflects the job's colour, and falls back to none for an unknown one", async () => {
    const { unmount } = mount({ job: job({ color: "cyan" }) });
    await ready();
    expect(trigger().textContent).toContain("CYAN");
    unmount();

    mount({ job: job({ color: "chartreuse" }) });
    await ready();
    expect(trigger().textContent).toContain("NO COLOR");
  });

  it("POSTs the pick on the spot rather than through the form autosave", async () => {
    const user = userEvent.setup();
    mount({ job: job() });
    await ready();
    const rows = await openMenu(user);
    await user.click(rows[5] as HTMLElement); // PURPLE
    await waitFor(() => expect(setJobColor).toHaveBeenCalledWith("j", "purple"));
    // saveform refuses a job whose cell is live, which is exactly when
    // recolouring matters — a colour pick must never ride it
    expect(saveJobForm).not.toHaveBeenCalled();
    // the pick closes the menu
    expect(document.querySelector("#f-swatches .colorsel-menu")).toBeNull();
  });

  it("clears the colour with the none row", async () => {
    const user = userEvent.setup();
    mount({ job: job({ color: "red" }) });
    await ready();
    const rows = await openMenu(user);
    await user.click(rows[0] as HTMLElement); // NO COLOR
    await waitFor(() => expect(setJobColor).toHaveBeenCalledWith("j", ""));
  });

  // setFormReadOnly deliberately leaves this one control enabled: everything
  // else in the form is job-file state the crew owns while a cell is in
  // flight, but a colour is carl's own label riding its own ungated endpoint —
  // and recolouring a RUNNING job is the main thing the feature is for.
  it("stays enabled while the rest of the form is read-only", async () => {
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form, editable: false }),
    );
    const user = userEvent.setup();
    mount({ job: job({ state: "working", live: true }) });
    await ready();
    expect(document.getElementById("f-repo")).toBeDisabled();
    expect(trigger().disabled).toBe(false);
    const rows = await openMenu(user);
    await user.click(rows[5] as HTMLElement); // PURPLE
    await waitFor(() => expect(setJobColor).toHaveBeenCalledWith("j", "purple"));
  });
});

// Regression pin for the double-submit race: the spec textarea's ⌘⏎ handler
// (CmdComposer) prevents default but used to let the event keep BUBBLING to
// #editor-form's own ⌘⏎ handler, so one keypress ran doSubmit twice in one
// synchronous dispatch — both invocations reading the same stale `busy` — and
// every keyboard start posted /api/queue twice. The loser of the resulting
// server-side race answered first with a 409 the status corner rendered as
// "start refused" while the winner's cell came up: a false failure on every
// single keyboard-initiated start. Fails under the unfixed CmdComposer (two
// queueJob calls), passes with stopPropagation in place.
describe("⌘⏎ submit fires exactly once", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form, spec: "a non-empty spec" }),
    );
  });

  it("one ⌘⏎ in the spec textarea posts one submit, not two", async () => {
    mount({ job: job({ state: "draft" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    const submitBtn = document.getElementById("editor-submit") as HTMLButtonElement;
    await waitFor(() => expect(submitBtn.disabled).toBe(false));

    const textarea = document.getElementById("editor-text") as HTMLTextAreaElement;
    fireEvent.keyDown(textarea, { key: "Enter", metaKey: true });

    await waitFor(() => expect(queueJob).toHaveBeenCalledTimes(1));
    // The duplicate arrived in the SAME dispatch, so it is already in flight
    // by now if it exists at all — settle the microtask queue and re-count.
    await new Promise((r) => setTimeout(r, 0));
    expect(queueJob).toHaveBeenCalledTimes(1);
  });

  it("⌘⏎ from the form's other fields still submits (the outer handler survives)", async () => {
    mount({ job: job({ state: "draft" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    await waitFor(() =>
      expect((document.getElementById("editor-submit") as HTMLButtonElement).disabled).toBe(false),
    );

    fireEvent.keyDown(document.getElementById("f-base") as HTMLInputElement, {
      key: "Enter",
      metaKey: true,
    });
    await waitFor(() => expect(queueJob).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(queueJob).toHaveBeenCalledTimes(1);
  });
});
