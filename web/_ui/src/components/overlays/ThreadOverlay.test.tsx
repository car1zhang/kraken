import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { makeJob } from "../../test/fixtures.ts";
import {
  ThreadOverlay,
  threadBlockHTML,
  threadConvoRender,
  threadSplitIndex,
} from "./ThreadOverlay.tsx";

vi.mock("../../api/client.ts", () => ({
  fetchThreadRead: vi.fn(() => Promise.resolve({ blocks: [] })),
  capturePane: vi.fn(() => Promise.resolve({ pane: "%1", content: "" })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true })),
  openTmux: vi.fn(() => Promise.resolve({ ok: true })),
}));

const mutate = async <T,>(op: () => Promise<T>) => op();

const job = makeJob({
  slug: "j",
  state: "working",
  live: true,
  threads: [
    { tid: "t1", title: "wer question", state: "live", pane: "%7", created_at: 0 },
  ],
});

describe("threadBlockHTML", () => {
  it("renders each known kind with its own class, icon and speaker", () => {
    expect(threadBlockHTML({ kind: "user", text: "hi" }, false, "t1")).toContain(
      '<div class="blk user">',
    );
    // no operator name passed (an older server's board): the fallback label
    expect(threadBlockHTML({ kind: "user", text: "hi" }, false, "t1")).toContain(
      "❯ you",
    );
    expect(
      threadBlockHTML({ kind: "thinking", text: "hm" }, false, "t1"),
    ).toContain('<div class="blk think">');
    expect(
      threadBlockHTML({ kind: "teammate", text: "yo" }, false, "t1"),
    ).toContain("✉ teammate");
  });

  it("a tool line gets no speaker row and no markdown", () => {
    const html = threadBlockHTML({ kind: "tool", text: "*Read* f" }, false, "t1");
    expect(html).toBe('<div class="blk tool">→ *Read* f</div>');
    expect(html).not.toContain("<em>");
  });

  it("marks an inherited block", () => {
    expect(
      threadBlockHTML({ kind: "text", text: "x", inherited: true }, true, "t1"),
    ).toContain('class="blk asst inherited"');
  });

  it('a kind of "constructor" falls back to text instead of resolving on Object.prototype', () => {
    // hasOwnProperty.call, not a truthiness check — this is the whole reason
    for (const kind of ["constructor", "toString", "__proto__", "valueOf"]) {
      const html = threadBlockHTML({ kind, text: "x" }, false, "t1");
      expect(html).toContain('class="blk asst"');
      expect(html).toContain("↯");
    }
  });

  it("an unknown kind names the thread as the speaker", () => {
    expect(threadBlockHTML({ kind: "mystery", text: "x" }, false, "t9")).toContain(
      "↯ thread t9",
    );
  });

  it("a `from` on the block wins over the kind's default speaker", () => {
    expect(
      threadBlockHTML({ kind: "text", text: "x", from: "hephaestus" }, false, "t1"),
    ).toContain("↯ hephaestus");
  });

  it("escapes agent-authored text", () => {
    expect(
      threadBlockHTML({ kind: "text", text: "<script>x</script>" }, false, "t1"),
    ).not.toContain("<script>");
  });
});

describe("threadSplitIndex", () => {
  it("is one past the LAST inherited block", () => {
    expect(
      threadSplitIndex([
        { kind: "user", inherited: true },
        { kind: "text", inherited: true },
        { kind: "user" },
      ]),
    ).toBe(2);
  });

  it("is 0 when the thread has said everything itself", () => {
    expect(threadSplitIndex([{ kind: "user" }, { kind: "text" }])).toBe(0);
  });

  it("is blocks.length when the thread has said nothing yet", () => {
    expect(
      threadSplitIndex([
        { kind: "user", inherited: true },
        { kind: "text", inherited: true },
      ]),
    ).toBe(2);
  });
});

describe("threadConvoRender", () => {
  it("puts the divider between inherited history and the thread's own blocks", () => {
    const html = threadConvoRender(
      {
        blocks: [{ kind: "user", text: "old", inherited: true }, { kind: "user", text: "new" }],
        fork_uuid: "abc",
      },
      "t1",
    );
    const divider = html.indexOf("thread-divider");
    expect(divider).toBeGreaterThan(html.indexOf("old"));
    expect(divider).toBeLessThan(html.indexOf("new"));
    expect(html).toContain("⑂ branched here · abc");
  });

  it("a thread that has said nothing yet still gets its divider — at the very end", () => {
    const html = threadConvoRender(
      { blocks: [{ kind: "user", text: "old", inherited: true }] },
      "t1",
    );
    expect(html.indexOf("thread-divider")).toBeGreaterThan(html.indexOf("old"));
    expect(html.match(/thread-divider/g)?.length).toBe(1);
  });

  it("renders the empty state for no blocks", () => {
    expect(threadConvoRender({ blocks: [] }, "t1")).toBe(
      '<div class="thread-empty">no messages yet</div>',
    );
  });
});

describe("ThreadOverlay stale-close is ASYMMETRIC to steer's", () => {
  const base = {
    open: true as const,
    slug: "j",
    tid: "t1",
    onBranched: () => {},
    mutate,
  };

  it("does NOT close when the job changes state — a thread outlives working→blocked→done", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <ThreadOverlay {...base} job={job} onClose={onClose} />,
    );
    rerender(
      <ThreadOverlay {...base} job={{ ...job, state: "blocked" }} onClose={onClose} />,
    );
    rerender(
      <ThreadOverlay
        {...base}
        job={{ ...job, state: "done", live: false }}
        onClose={onClose}
      />,
    );
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes ONLY when the job leaves the board entirely", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <ThreadOverlay {...base} job={job} onClose={onClose} />,
    );
    rerender(<ThreadOverlay {...base} job={undefined} onClose={onClose} />);
    expect(onClose).toHaveBeenCalled();
  });

  it("a closed thread stays readable — the row outlives the cell", () => {
    render(
      <ThreadOverlay
        {...base}
        job={{
          ...job,
          live: false,
          threads: [
            { tid: "t1", title: "x", state: "closed", created_at: 0, closed_at: 1 },
          ],
        }}
        onClose={() => {}}
      />,
    );
    expect(document.getElementById("thread-pill")?.textContent).toBe("closed");
    // the composer is gone and revive is offered in its place
    expect(document.getElementById("thread-composer")).toHaveAttribute("hidden");
    expect(document.getElementById("thread-revive")).not.toHaveAttribute("hidden");
    expect(document.getElementById("thread-kill")).toHaveAttribute("hidden");
  });

  it("branch mode hides every thread-scoped head control", () => {
    render(<ThreadOverlay {...base} tid={null} job={job} onClose={() => {}} />);
    expect(document.getElementById("thread-tid")?.textContent).toBe("⑂ branch — j");
    for (const id of ["thread-pill", "thread-tmux", "thread-revive", "thread-kill"]) {
      expect(document.getElementById(id)).toHaveAttribute("hidden");
    }
    expect(document.getElementById("thread-new")).not.toHaveAttribute("hidden");
    expect(document.getElementById("thread-view")).toHaveAttribute("hidden");
  });
});

describe("thread rename", () => {
  it("Escape in the rename input does not reach the document Escape chain", async () => {
    const onClose = vi.fn();
    render(
      <ThreadOverlay
        open
        slug="j"
        tid="t1"
        job={job}
        onClose={onClose}
        onBranched={() => {}}
        mutate={mutate}
      />,
    );
    const label = document.getElementById("thread-name-label") as HTMLElement;
    fireEvent.click(label);
    const input = document.getElementById("thread-name-input") as HTMLInputElement;
    expect(input).not.toHaveAttribute("hidden");
    expect(input.value).toBe("wer question");

    // a real Escape keydown, bubbling — the input must stop it, so the
    // document-level Escape chain never sees it and the overlay stays open
    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    // and the rename is cancelled: the label is back
    expect(document.getElementById("thread-name-input")).toHaveAttribute("hidden");
  });
});
