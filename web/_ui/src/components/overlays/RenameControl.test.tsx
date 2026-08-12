import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { JobForm, WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { EditorOverlay } from "./EditorOverlay.tsx";
import { renameControlVisible } from "./RenameControl.tsx";

// RenameControl.test.tsx — the rename affordance both overlays share.
//
// `renameControlVisible` is exercised as the pure predicate it is; everything
// else is driven through EditorOverlay rather than through a bare mount of the
// hook, and that is the point rather than convenience. Three of the four
// behaviours below are about what happens ACROSS the re-point — the response's
// slug reaching the integrator, the body remounting under it, the `skipped`
// notice surviving that remount — and a harness that rendered the control on
// its own would hold none of them, because there would be no body to remount.
//
// The fourth is the autosave flush, which only exists as a relationship
// between two components: the debounce lives in EditorBody and the POST is
// fired from above it.

const form: JobForm = {
  slug: "j",
  repo: "kraken",
  base: "origin/main",
  model: "",
  editable: true,
  spec: "",
  mtime: 7,
  repos: ["kraken"],
  slugs: [],
};

// Records the ORDER the two writes go out in, which is the whole subject of
// the flush test: a rename that outruns the pending autosave loses the edit.
const calls: string[] = [];

vi.mock("../../api/client.ts", () => ({
  fetchJobForm: vi.fn(() => Promise.resolve({ ...form })),
  saveJobForm: vi.fn((body: { slug: string }) => {
    calls.push("save:" + body.slug);
    return Promise.resolve({ ok: true, mtime: 8 });
  }),
  renameJob: vi.fn((slug: string) => {
    calls.push("rename:" + slug);
    return Promise.resolve({ ok: true, slug: "renamed-server-side" });
  }),
  createJob: vi.fn(() => Promise.resolve({ ...form })),
  queueJob: vi.fn(() => Promise.resolve({ ok: true })),
  dequeueJob: vi.fn(() => Promise.resolve({ ok: true })),
  archiveJob: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(),
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  editFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  deleteFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  setJobColor: vi.fn(() => Promise.resolve({ ok: true })),
}));

import { renameJob, saveJobForm } from "../../api/client.ts";

const mutate = async <T,>(op: () => Promise<T>) => op();
const noop = () => {};

const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug: "j", ...over });

function mount(props: Partial<Parameters<typeof EditorOverlay>[0]> = {}) {
  return render(
    <EditorOverlay
      open
      slug="j"
      job={job()}
      onClose={noop}
      onCreated={noop}
      onRenamed={noop}
      onReactivate={noop}
      onOpenArtifact={noop}
      mutate={mutate}
      {...props}
    />,
  );
}

/**
 * The integrator, as App.tsx actually behaves: `open={editor !== null}` and
 * `slug={editor?.slug ?? null}` off ONE piece of state, `onRenamed` re-pointing
 * it, and the overlay itself never unmounted.
 *
 * That last part is what these tests exist for. The rename control's state was
 * hoisted above the overlay's keyed body so a `skipped` notice could outlive
 * the re-point — which also lifted it above the only thing that used to reset
 * it between jobs, and this component is the shape in which that matters.
 */
function Harness() {
  const [slug, setSlug] = useState<string | null>("j");
  return (
    <>
      {/* App's `onClose`/card-click, minus the board */}
      <button onClick={() => setSlug(null)}>close-editor</button>
      <button onClick={() => setSlug("other-job")}>open-other</button>
      <EditorOverlay
        open={slug !== null}
        slug={slug}
        job={slug ? job({ slug }) : undefined}
        onClose={() => setSlug(null)}
        onCreated={noop}
        onRenamed={setSlug}
        onReactivate={noop}
        onOpenArtifact={noop}
        mutate={mutate}
      />
    </>
  );
}

/** Open the row and type `to` into it. Assumes the form has loaded. */
async function typeName(user: ReturnType<typeof userEvent.setup>, to: string) {
  await user.click(screen.getByText("✎ rename"));
  const input = screen.getByPlaceholderText("new name");
  await user.clear(input);
  await user.type(input, to);
}

const asMock = (fn: unknown) => fn as ReturnType<typeof vi.fn>;

/** A source path under the OLD prefix — what the wire's `skipped` carries. */
const strandedCopy = "/Users/x/Documents/pluto/j-1.00-overview.md";
/** A whole sentence with its cause — what the wire's `notes` carries. */
const failedStep = "tmux rename-session failed: no server running";

/**
 * A rename that succeeds and hands back whichever of the two post-commit lists
 * the test is about. Both are omitted when not asked for, which is the shape
 * the handler actually sends: neither key appears when its list is empty.
 */
function mockRename(lists: { skipped?: string[]; notes?: string[] } = {}) {
  asMock(renameJob).mockImplementation(() =>
    Promise.resolve({ ok: true, slug: "renamed-server-side", ...lists }),
  );
}

describe("renameControlVisible", () => {
  // The three arms of the rule, one assertion each. It is the arm most likely
  // to regress silently: every one of them turns the control OFF, and an
  // absent button looks exactly like a feature that was never built.
  it("shows for an existing, non-archived job with the re-point wired", () => {
    expect(renameControlVisible(job(), false, noop)).toBe(true);
    // an undefined job is the just-created case, not an archived one — the
    // board has simply not caught up, and the control stays available
    expect(renameControlVisible(undefined, false, noop)).toBe(true);
  });

  it("hides on the new-job row", () => {
    expect(renameControlVisible(undefined, true, noop)).toBe(false);
    expect(renameControlVisible(job(), true, noop)).toBe(false);
  });

  it("hides for an archived job", () => {
    expect(renameControlVisible(job({ archived: true }), false, noop)).toBe(false);
  });

  it("hides when the re-point is unwired", () => {
    // the structural half of the argument in the doc comment: an overlay handed
    // no callback would rename the job and then keep addressing the old name,
    // so it renders no button at all rather than a broken one
    expect(renameControlVisible(job(), false, undefined)).toBe(false);
  });
});

describe("RenameControl in the editor", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    calls.length = 0;
    asMock(renameJob).mockImplementation((slug: string) => {
      calls.push("rename:" + slug);
      return Promise.resolve({ ok: true, slug: "renamed-server-side" });
    });
    asMock(saveJobForm).mockImplementation((body: { slug: string }) => {
      calls.push("save:" + body.slug);
      return Promise.resolve({ ok: true, mtime: 8 });
    });
  });

  it("re-points to the SERVER's slug, never the typed preview", async () => {
    const user = userEvent.setup();
    const onRenamed = vi.fn();
    mount({ onRenamed });
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "My New Name!");
    // the preview is what the port thinks; it is deliberately not the answer
    expect(document.getElementById("editor-rename-row")?.textContent).toContain(
      "my-new-name",
    );
    await user.click(screen.getByText("rename"));

    await waitFor(() => expect(onRenamed).toHaveBeenCalledWith("renamed-server-side"));
    expect(renameJob).toHaveBeenCalledWith("j", "My New Name!");
  });

  it("keeps the row open on the old slug when the server refuses", async () => {
    const user = userEvent.setup();
    const onRenamed = vi.fn();
    asMock(renameJob).mockImplementation(() =>
      Promise.reject(new Error('"taken" is taken by another active job')),
    );
    mount({ onRenamed });
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "taken");
    await user.click(screen.getByText("rename"));

    await waitFor(() =>
      expect(document.getElementById("editor-rename-row")?.textContent).toContain(
        "is taken by another active job",
      ),
    );
    // still open, still holding the typed text, and nothing re-pointed
    expect(screen.getByPlaceholderText("new name")).toHaveValue("taken");
    expect(onRenamed).not.toHaveBeenCalled();
    expect(document.getElementById("editor-title")?.textContent).toBe("j.job.md");
  });

  it("renders the skipped artifact copies and survives the re-point", async () => {
    const user = userEvent.setup();
    // the OLD-name prefix, deliberately: the server sends the file that
    // stayed, not the one blocking it
    mockRename({ skipped: [strandedCopy] });
    render(<Harness />);
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "new");
    await user.click(screen.getByText("rename"));

    // the body has remounted on the new slug — and the notice is still there,
    // which is the only reason the hook lives above it
    await waitFor(() =>
      expect(document.getElementById("editor-title")?.textContent).toBe(
        "renamed-server-side.job.md",
      ),
    );
    const skipped = document.getElementById("editor-rename-row-skipped");
    // The sentence has to be true of the paths BESIDE it: the wire carries the
    // SOURCE — the file still under the old name — never the destination that
    // blocked it, which would read as "delete this" about the wrong file. Both
    // halves are asserted because the second is the one that says WHICH file
    // the list holds, and it is the half the first round got wrong.
    expect(skipped?.textContent).toContain("kept the old name");
    expect(skipped?.textContent).toContain("still under the old name");
    expect(skipped?.textContent).toContain(strandedCopy);
    // a conflict-only response renders no failure block at all
    expect(document.getElementById("editor-rename-row-notes")).toBeNull();

    await user.click(screen.getByText("dismiss"));
    expect(document.getElementById("editor-rename-row-skipped")).toBeNull();
  });

  it("renders notes as their own block and survives the re-point", async () => {
    const user = userEvent.setup();
    // notes with NO skipped, which is a real shape: a tmux step or the zeus
    // notice can fail on a rename where every preserved copy moved cleanly
    mockRename({ notes: [failedStep] });
    render(<Harness />);
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "new");
    await user.click(screen.getByText("rename"));

    await waitFor(() =>
      expect(document.getElementById("editor-title")?.textContent).toBe(
        "renamed-server-side.job.md",
      ),
    );
    const notes = document.getElementById("editor-rename-row-notes");
    // Its own block, and its own sentence: these are steps that BROKE, not
    // files to move. The distinction is the whole reason the server stopped
    // reporting a failed move as a conflict, and a client that merged them
    // back into one list would undo that fix at the last hop.
    expect(notes?.textContent).toContain("the rename itself landed");
    expect(notes?.textContent).toContain(failedStep);
    expect(document.getElementById("editor-rename-row-skipped")).toBeNull();
  });

  it("renders both lists at once and clears both with one dismiss", async () => {
    const user = userEvent.setup();
    mockRename({ skipped: [strandedCopy], notes: [failedStep] });
    render(<Harness />);
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "new");
    await user.click(screen.getByText("rename"));

    await waitFor(() =>
      expect(document.getElementById("editor-rename-row-notes")).not.toBeNull(),
    );
    expect(document.getElementById("editor-rename-row-skipped")).not.toBeNull();

    // ONE dismiss, both gone: they come from one response and there is no
    // state in which clearing half of them means anything
    expect(screen.getAllByText("dismiss")).toHaveLength(1);
    await user.click(screen.getByText("dismiss"));
    expect(document.getElementById("editor-rename-row-skipped")).toBeNull();
    expect(document.getElementById("editor-rename-row-notes")).toBeNull();
    expect(document.getElementById("editor-rename-row")).toBeNull();
  });

  // ---- the two guards that replace the remount the hoist gave up -----------

  // Both carry BOTH lists, because both guards are meant to hold the whole
  // notice rather than one field of it — and one state holding two lists is
  // exactly the shape in which a guard gets applied to only the first.

  it("drops the notice when the overlay closes, so it cannot reappear", async () => {
    const user = userEvent.setup();
    mockRename({ skipped: [strandedCopy], notes: [failedStep] });
    render(<Harness />);
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "new");
    await user.click(screen.getByText("rename"));
    await waitFor(() =>
      expect(document.getElementById("editor-rename-row-skipped")).not.toBeNull(),
    );

    // Escape / backdrop / the ✕ — all of them land here
    await user.click(screen.getByText("close-editor"));
    await user.click(screen.getByText("open-other"));

    await waitFor(() =>
      expect(document.getElementById("editor-title")?.textContent).toBe(
        "other-job.job.md",
      ),
    );
    expect(document.getElementById("editor-rename-row-skipped")).toBeNull();
    expect(document.getElementById("editor-rename-row-notes")).toBeNull();
    expect(document.getElementById("editor-rename-row")).toBeNull();
  });

  it("never shows one job's notice on a different job", async () => {
    const user = userEvent.setup();
    mockRename({ skipped: [strandedCopy], notes: [failedStep] });
    render(<Harness />);
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "new");
    await user.click(screen.getByText("rename"));
    await waitFor(() =>
      expect(document.getElementById("editor-rename-row-skipped")).not.toBeNull(),
    );

    // straight from one job to another with NO close in between — the click
    // that opens a different card while the editor is already up. The close
    // effect never fires here, so this is the identity guard alone.
    await user.click(screen.getByText("open-other"));
    await waitFor(() =>
      expect(document.getElementById("editor-title")?.textContent).toBe(
        "other-job.job.md",
      ),
    );
    expect(document.getElementById("editor-rename-row-skipped")).toBeNull();
    expect(document.getElementById("editor-rename-row-notes")).toBeNull();
  });

  it("drops a half-typed rename when the overlay closes", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    // opened and typed into, never submitted
    await typeName(user, "half-typed-name");
    expect(screen.getByPlaceholderText("new name")).toHaveValue("half-typed-name");

    await user.click(screen.getByText("close-editor"));
    await user.click(screen.getByText("open-other"));

    await waitFor(() =>
      expect(document.getElementById("editor-title")?.textContent).toBe(
        "other-job.job.md",
      ),
    );
    // the row is closed again, so the head button is back and no stale text is
    // sitting in an input addressed at a job that never had it typed
    expect(screen.queryByPlaceholderText("new name")).toBeNull();
    expect(document.getElementById("editor-rename")).not.toHaveAttribute("hidden");
  });

  it("flushes the pending autosave BEFORE the rename posts", async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    // an edit still inside the 600ms debounce window
    await user.type(
      document.getElementById("editor-text") as HTMLTextAreaElement,
      "the last thing carl typed",
    );
    expect(saveJobForm).not.toHaveBeenCalled();

    await typeName(user, "new");
    await user.click(screen.getByText("rename"));

    await waitFor(() => expect(renameJob).toHaveBeenCalled());
    // the save lands FIRST and under the OLD slug, which is the honest place
    // for it: the rename had not happened when those keystrokes were made
    expect(calls).toEqual(["save:j", "rename:j"]);
    const body = asMock(saveJobForm).mock.calls[0]?.[0] as { spec: string };
    expect(body.spec).toBe("the last thing carl typed");
  });

  it("refuses a second submit while the first is in flight", async () => {
    const user = userEvent.setup();
    let release: (() => void) | null = null;
    asMock(renameJob).mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () => resolve({ ok: true, slug: "renamed-server-side" });
        }),
    );
    mount();
    await waitFor(() => expect(document.getElementById("editor-text")).not.toBeNull());

    await typeName(user, "new");
    const button = screen.getByText("rename");
    const input = screen.getByPlaceholderText("new name");
    // fireEvent, not userEvent, and that is the whole discrimination: these
    // three dispatch synchronously inside ONE batch, so React has not yet
    // committed the first handler's setBusy(true) when the second and third
    // run. A `busy` STATE guard reads false for all three and fires three
    // POSTs; the second and third 404 (the job has already moved) onto a row
    // the first success closed, so the errors are invisible. Only the
    // synchronous ref guard stops them.
    fireEvent.click(button);
    fireEvent.click(button);
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(renameJob).toHaveBeenCalled());
    expect(renameJob).toHaveBeenCalledTimes(1);

    await act(async () => release?.());
    await waitFor(() => expect(screen.queryByPlaceholderText("new name")).toBeNull());
  });
});
