import { render, screen, waitFor } from "@testing-library/react";
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
  dequeueJob: vi.fn(() => Promise.resolve({ ok: true })),
  archiveJob: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(),
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
}));

import {
  createJob,
  dequeueJob,
  fetchJobForm,
  queueJob,
  saveJobForm,
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
    mount();
    await waitFor(() => expect(fetchJobForm).toHaveBeenCalledWith("j", expect.anything()));
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    expect((document.getElementById("f-repo") as HTMLSelectElement).value).toBe(
      "kraken",
    );
    expect((document.getElementById("f-base") as HTMLInputElement).value).toBe(
      "origin/main",
    );
    // both real checkouts plus the "—" option
    expect(
      document.querySelectorAll("#f-repo option").length,
    ).toBe(3);
  });

  it("keeps an unusual repo as an extra option rather than resetting it to —", async () => {
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form, repo: "somewhere-else" }),
    );
    mount();
    await waitFor(() =>
      expect((document.getElementById("f-repo") as HTMLSelectElement).value).toBe(
        "somewhere-else",
      ),
    );
    expect(document.querySelectorAll("#f-repo option").length).toBe(4);
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
    expect(document.getElementById("f-add-prereq")).toBeDisabled();
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

describe("the queue affordance is derived, not snapshotted", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form }),
    );
  });

  it("an empty-spec draft shows the button, disabled, with a hint", async () => {
    mount();
    await waitFor(() =>
      expect(document.getElementById("editor-queue")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-queue")).toBeDisabled();
    expect(document.getElementById("editor-hint")?.textContent).toBe(
      "write a spec to queue",
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
      expect(document.getElementById("editor-queue")).not.toBeDisabled(),
    );
    expect(document.getElementById("editor-hint")?.textContent).toBe(
      "synced ✓ — queueable",
    );
  });

  it("a job MISSING from the board is treated as a fresh draft, not as unarmable", async () => {
    // this is the bug the reactive derivation fixed: a job created seconds ago
    // is not in the board payload yet, and the one-shot snapshot left it
    // permanently unqueueable until a close/reopen
    const user = userEvent.setup();
    mount({ job: undefined });
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "x");
    await waitFor(() =>
      expect(document.getElementById("editor-queue")).not.toBeDisabled(),
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
    expect(document.getElementById("editor-queue")).toBeDisabled();
  });

  it("a board-level err disarms it too", async () => {
    const user = userEvent.setup();
    mount({ job: job({ err: "unhealthy cell" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-text")).not.toBeNull(),
    );
    await user.type(document.getElementById("editor-text") as HTMLTextAreaElement, "x");
    await waitFor(() => expect(saveJobForm).toHaveBeenCalled());
    expect(document.getElementById("editor-queue")).toBeDisabled();
  });

  it("a QUEUED job offers the reverse move instead of the forward one", async () => {
    mount({ job: job({ state: "queued" }) });
    await waitFor(() =>
      expect(document.getElementById("editor-dequeue")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-queue")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-hint")?.textContent).toBe(
      "queued — cancel returns it to draft",
    );
  });

  it("a working job offers neither", async () => {
    mount({ job: job({ state: "working", live: true }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
    expect(document.getElementById("editor-queue")).toHaveAttribute("hidden");
    expect(document.getElementById("editor-dequeue")).toHaveAttribute("hidden");
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

  it("drops empty prereq rows from the payload", async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() => expect(document.getElementById("f-add-prereq")).not.toBeNull());
    await user.click(screen.getByText("+ add"));
    await waitFor(() => expect(saveJobForm).toHaveBeenCalled());
    const body = (saveJobForm as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as {
      prereqs: unknown[];
    };
    expect(body.prereqs).toEqual([]);
  });

  it("a removed prereq row lands immediately in the payload", async () => {
    (fetchJobForm as ReturnType<typeof vi.fn>).mockImplementation(() =>
      Promise.resolve({ ...form, prereqs: [{ job: "dep", gated: true }] }),
    );
    const user = userEvent.setup();
    mount();
    await waitFor(() =>
      expect(document.querySelector(".prereq-row .rm")).not.toBeNull(),
    );
    await user.click(document.querySelector(".prereq-row .rm") as HTMLElement);
    await waitFor(() => expect(saveJobForm).toHaveBeenCalled());
    const body = (saveJobForm as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as {
      prereqs: unknown[];
    };
    expect(body.prereqs).toEqual([]);
  });
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
        mutate={mutate}
      />,
    );
    expect(onClose).not.toHaveBeenCalled();

    // and now a real change closes it
    rerender(
      <EditorOverlay
        open
        slug="j"
        job={job({ state: "queued" })}
        onClose={onClose}
        onCreated={noop}
        onReactivate={noop}
        mutate={mutate}
      />,
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("dequeue rebases the baseline so our OWN action does not close the editor", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const { rerender } = render(
      <EditorOverlay
        open
        slug="j"
        job={job({ state: "queued" })}
        onClose={onClose}
        onCreated={noop}
        onReactivate={noop}
        mutate={mutate}
      />,
    );
    await waitFor(() =>
      expect(document.getElementById("editor-dequeue")).not.toHaveAttribute("hidden"),
    );
    await user.click(screen.getByText("✕ cancel queue"));
    await waitFor(() => expect(dequeueJob).toHaveBeenCalledWith("j"));

    // the board now reports draft — the state WE asked for
    rerender(
      <EditorOverlay
        open
        slug="j"
        job={job({ state: "draft" })}
        onClose={onClose}
        onCreated={noop}
        onReactivate={noop}
        mutate={mutate}
      />,
    );
    expect(onClose).not.toHaveBeenCalled();
    expect(document.getElementById("editor-queue")).not.toHaveAttribute("hidden");
  });
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

    mount({ job: job({ state: "done", reactivatable: true, archived: true }) });
    await waitFor(() =>
      expect(document.getElementById("editor-form")).not.toHaveAttribute("hidden"),
    );
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
