import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WebArtifact, WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import {
  ArtifactOverlay,
  artifactParts,
  askThreadSeed,
  askThreadTitle,
} from "./ArtifactOverlay.tsx";

vi.mock("../../api/client.ts", () => ({
  createFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
}));
// the overlay's one out-of-band channel — mocked so the tid-less branch can
// assert what the reader is actually told, rather than that nothing crashed
vi.mock("./Toast.tsx", () => ({ showToast: vi.fn(), Toast: () => null }));

import { createFeedback, threadOp } from "../../api/client.ts";
import { showToast } from "./Toast.tsx";

const mutate = async <T,>(op: () => Promise<T>) => op();

const art = (n: string): WebArtifact => ({
  name: n,
  url: `/api/artifact/j/artifacts/${n}`,
});

const job = (names: string[], over: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug: "j", artifacts: names.map(art), ...over });

/**
 * The integrator, in miniature: App owns which artifact is open and re-points
 * url/name on a step while KEEPING the job it opened from. The overlay is a
 * controlled component, so a test that drove it any other way would be testing
 * a component that does not exist.
 */
function Harness({
  job: j,
  start = "00-overview.md",
  label = "j",
  wired = true,
  onAskBranched = () => {},
}: {
  job?: WebJob;
  start?: string;
  label?: string;
  wired?: boolean;
  onAskBranched?: (slug: string, tid: string) => void;
}) {
  const [cur, setCur] = useState({
    url: `/api/artifact/j/artifacts/${start}`,
    name: `${label}/${start}`,
  });
  const [open, setOpen] = useState(true);
  return (
    <ArtifactOverlay
      open={open}
      url={cur.url}
      name={cur.name}
      job={j}
      // a no-op, NOT an omitted prop: the unwired case is now something a
      // caller must ask for, which is the whole point of making it required
      onNavigate={wired ? (url, name) => setCur({ url, name }) : () => {}}
      onAskBranched={onAskBranched}
      onClose={() => setOpen(false)}
      mutate={mutate}
    />
  );
}

const title = () => document.getElementById("panel-title")?.textContent;
const pager = () => document.getElementById("panel-pager");
const pos = () => document.getElementById("panel-pos")?.textContent;
const prev = () => document.getElementById("panel-prev") as HTMLButtonElement;
const next = () => document.getElementById("panel-next") as HTMLButtonElement;

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) =>
      Promise.resolve({ ok: true, text: () => Promise.resolve("# " + url) }),
    ),
  );
});

describe("artifactParts", () => {
  it("splits slug and home off, then keeps the whole rest as the name", () => {
    expect(artifactParts("/api/artifact/react-ui/artifacts/00-overview.md")).toEqual({
      slug: "react-ui",
      home: "artifacts",
      name: "00-overview.md",
    });
  });

  it("keeps slashes INSIDE the name — a cell-home artifact may be a subpath", () => {
    // Only the first two segments split off. This is the case the vanilla
    // comment (app.js:1638) calls out by name, and getting it wrong addresses
    // a feedback post at the wrong artifact.
    expect(
      artifactParts("/api/artifact/react-ui/home/docs/diagrams/13-tree.html"),
    ).toEqual({
      slug: "react-ui",
      home: "home",
      name: "docs/diagrams/13-tree.html",
    });
  });

  it("keeps a deeply nested name intact", () => {
    const parts = artifactParts("/api/artifact/j/h/a/b/c/d/e/f.md");
    expect(parts?.name).toBe("a/b/c/d/e/f.md");
  });

  it("returns null for anything that is not an /api/artifact URL", () => {
    expect(artifactParts("/api/board")).toBeNull();
    expect(artifactParts("")).toBeNull();
    // two segments only: there is no name to address feedback at
    expect(artifactParts("/api/artifact/j/h")).toBeNull();
    expect(artifactParts("/api/artifact/j/h/")).toBeNull();
  });

  it("does not match a prefix — the path must start at /api/artifact", () => {
    expect(artifactParts("https://x/api/artifact/j/h/n.md")).toBeNull();
  });
});

describe("the pager", () => {
  it("is hidden for a single-artifact sequence", () => {
    render(<Harness job={job(["00-overview.md"])} />);
    expect(pager()).toHaveAttribute("hidden");
  });

  it("is hidden when the job cannot be resolved at all (singleton fallback)", () => {
    render(<Harness job={undefined} />);
    expect(pager()).toHaveAttribute("hidden");
    // …and the artifact still opens, titled exactly as the chip named it
    expect(title()).toBe("j/00-overview.md");
  });

  it("is hidden when navigation is not wired, however many artifacts there are", () => {
    render(<Harness job={job(["a.md", "b.md", "c.md"])} wired={false} />);
    expect(pager()).toHaveAttribute("hidden");
  });

  it("shows for 2+ and reads the position", () => {
    render(<Harness job={job(["a.md", "b.md", "c.md"])} start="b.md" />);
    expect(pager()).not.toHaveAttribute("hidden");
    expect(pos()).toBe("2 / 3");
  });

  it("labels sequence names through display_slug, as the chips do", () => {
    render(
      <Harness
        job={job(["a.md", "b.md"], { display_slug: "old-name" })}
        label="old-name"
      />,
    );
    expect(title()).toBe("old-name/00-overview.md");
  });
});

describe("stepping", () => {
  it("‹ › walk the job's set", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md", "c.md"])} start="a.md" />);
    expect(pos()).toBe("1 / 3");
    await user.click(next());
    expect(title()).toBe("j/b.md");
    expect(pos()).toBe("2 / 3");
    await user.click(prev());
    expect(title()).toBe("j/a.md");
    expect(pos()).toBe("1 / 3");
  });

  it("CLAMPS at both ends — it never wraps", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    // each end button is disabled at its end, so the clamp is visible before
    // it is exercised
    expect(prev()).toBeDisabled();
    expect(next()).not.toBeDisabled();
    await user.click(next());
    expect(title()).toBe("j/b.md");
    expect(next()).toBeDisabled();
    // a step past the end is a no-op, not a wrap back to a.md
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(title()).toBe("j/b.md");
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    expect(title()).toBe("j/a.md");
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    expect(title()).toBe("j/a.md");
  });

  it("← / → step from the board document", () => {
    render(<Harness job={job(["a.md", "b.md", "c.md"])} start="a.md" />);
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(title()).toBe("j/b.md");
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    expect(title()).toBe("j/a.md");
  });

  it("leaves a MODIFIED arrow alone — those belong to the browser", () => {
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    for (const mod of [{ metaKey: true }, { ctrlKey: true }, { altKey: true }]) {
      fireEvent.keyDown(document.body, { key: "ArrowRight", ...mod });
      expect(title()).toBe("j/a.md");
    }
  });

  it("does NOT navigate while focus is in the feedback composer", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("feedback"));
    const box = document.getElementById("feedback-text") as HTMLTextAreaElement;
    await user.type(box, "half a note");
    // an arrow here is a caret move; stealing it for the pager would also eat
    // the note, since a step clears the composer
    fireEvent.keyDown(box, { key: "ArrowRight" });
    fireEvent.keyDown(box, { key: "ArrowLeft" });
    expect(title()).toBe("j/a.md");
  });

  it("stops listening once the viewer closes", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    // the backdrop click is one of the two close gestures
    await user.click(document.getElementById("overlay") as HTMLElement);
    expect(document.getElementById("overlay")).toHaveAttribute("hidden");
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(document.getElementById("panel-title")).toBeNull();
  });

  // web/artifactpager_test.go's TestStepArtifactIsTheNavigationCommand: the
  // ‹ › buttons and the arrow keys must not take different paths. That
  // divergence is how one of the two ends up not clearing raw-mode, or leaking
  // the previous artifact's cached text under the new artifact's title. Here
  // both surfaces call one `onStep`, which re-points the same `url` prop the
  // chips do — and the per-artifact reset is the remount that url keys, so it
  // cannot be reached by one input surface and skipped by the other. Asserted
  // through BOTH surfaces rather than by reading the source.
  it.each([
    ["the › button", async (user: ReturnType<typeof userEvent.setup>) => user.click(next())],
    ["the → key", () => fireEvent.keyDown(document.body, { key: "ArrowRight" })],
  ])("resets raw mode and the cached text — via %s", async (_label, step) => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("raw"));
    await waitFor(() =>
      expect(document.getElementById("panel-body")).toHaveClass("raw"),
    );
    expect(document.getElementById("panel-body")?.textContent).toContain("a.md");

    await step(user);

    expect(title()).toBe("j/b.md");
    expect(document.getElementById("panel-body")).not.toHaveClass("raw");
    // and the body is b.md's, not a.md's cached text repainted under b's title
    await waitFor(() =>
      expect(document.getElementById("panel-body")?.textContent).toContain("b.md"),
    );
    expect(document.getElementById("panel-body")?.textContent).not.toContain("a.md");
  });

  // the other thing a step-by-remount would have cost: the `›` button is torn
  // down and rebuilt under the cursor, so focus lands back on <body> and a
  // keyboard user has to re-aim after every single activation
  it("keeps focus on › across a step", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md", "c.md"])} start="a.md" />);
    await user.click(next());
    expect(title()).toBe("j/b.md");
    expect(document.activeElement?.id).toBe("panel-next");
  });

  it("resets an open feedback composer on a step, whichever surface stepped", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("feedback"));
    expect(document.getElementById("feedback-form")).not.toHaveAttribute("hidden");
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(document.getElementById("feedback-form")).toHaveAttribute("hidden");
  });
});

// web/artifactpager_test.go's TestPagerReadoutDoesNotSpamItsLiveRegion. The
// vanilla renderPager ran on every 3s poll and had to compare before writing
// #panel-pos: assigning textContent replaces the text node, which is an
// ANNOUNCEABLE mutation even when the string is identical, so an unconditional
// write reads "3 / 7" aloud every three seconds for as long as the artifact is
// open — inverting the point of the attribute and reliably getting the region
// switched off.
//
// React's reconciliation is what satisfies this here (an unchanged text node
// is left alone), which means it is a property of a library rather than of
// code in this file — exactly the kind of thing that goes silently wrong on an
// upgrade or a refactor to a different render path. So it is asserted against
// the DOM, through a MutationObserver, rather than assumed.
describe("the position's live region", () => {
  function Repoll({ names }: { names: string[] }) {
    const [list, setList] = useState(names);
    return (
      <>
        {/* a poll that changed NOTHING still hands down fresh objects */}
        <button onClick={() => setList([...list])}>poll=</button>
        <Harness job={job(list)} start="a.md" />
      </>
    );
  }

  it("is aria-live, so a navigation announces at all", () => {
    render(<Harness job={job(["a.md", "b.md"])} />);
    expect(document.getElementById("panel-pos")).toHaveAttribute(
      "aria-live",
      "polite",
    );
  });

  it("does not mutate on a poll that moved nothing, and does on a step", async () => {
    const user = userEvent.setup();
    render(<Repoll names={["a.md", "b.md", "c.md"]} />);
    const el = document.getElementById("panel-pos") as HTMLElement;
    expect(el.textContent).toBe("1 / 3");
    // the callback drains the queue as soon as a microtask runs, so records
    // are accumulated rather than read back with takeRecords() alone
    let seen: MutationRecord[] = [];
    const obs = new MutationObserver((recs) => seen.push(...recs));
    obs.observe(el, { childList: true, characterData: true, subtree: true });
    const drain = () => {
      seen.push(...obs.takeRecords());
      const out = seen;
      seen = [];
      return out;
    };

    await user.click(screen.getByText("poll="));
    expect(el.textContent).toBe("1 / 3");
    expect(drain()).toHaveLength(0); // nothing happened: nothing to announce

    await user.click(next());
    expect(el.textContent).toBe("2 / 3");
    // …and the region is the SAME node, mutated. A replaced live region is not
    // reliably announced at all, which is the failure the guard's second
    // assertion names.
    expect(el.isConnected).toBe(true);
    expect(document.getElementById("panel-pos")).toBe(el);
    expect(drain().length).toBeGreaterThan(0);
    obs.disconnect();
  });
});

describe("the iframe's own document", () => {
  // A rendered HTML artifact lives in a SAME-ORIGIN iframe. Once focus lands
  // inside it — one click on a diagram card does it — keydown fires on
  // contentDocument and never bubbles out to the board, so the arrows would
  // die on exactly the artifacts you page through most.
  it("routes arrows back out to the pager", () => {
    render(<Harness job={job(["a.html", "b.html"])} start="a.html" />);
    const frame = document.querySelector("iframe") as HTMLIFrameElement;
    expect(frame).toBeTruthy();
    fireEvent.load(frame); // hookIframeKeys attaches on load, not on render
    const doc = frame.contentDocument as Document;
    fireEvent.keyDown(doc.body ?? doc, { key: "ArrowRight" });
    expect(title()).toBe("j/b.html");
  });

  it("does not throw when the frame's document is unreachable (a PDF)", () => {
    render(<Harness job={job(["a.pdf", "b.pdf"])} start="a.pdf" />);
    const frame = document.querySelector("iframe") as HTMLIFrameElement;
    Object.defineProperty(frame, "contentDocument", {
      get() {
        throw new Error("cross-origin");
      },
    });
    expect(() => fireEvent.load(frame)).not.toThrow();
    // the buttons still work — that is the documented degradation
    fireEvent.click(next());
    expect(title()).toBe("j/b.pdf");
  });
});

describe("the feedback draft", () => {
  it("survives a step away and back", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("feedback"));
    await user.type(
      document.getElementById("feedback-text") as HTMLTextAreaElement,
      "this diagram is upside down",
    );

    await user.click(next()); // › is pixels from the text you were typing
    expect(title()).toBe("j/b.md");
    // the composer is CLOSED and empty on the artifact you stepped onto — the
    // draft belongs to a.md, not to whatever you page onto next
    expect(document.getElementById("feedback-form")).toHaveAttribute("hidden");
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("");

    await user.click(prev());
    expect(title()).toBe("j/a.md");
    // …and it comes back with the composer already open, exactly as
    // restoreFeedbackDraft leaves it
    expect(document.getElementById("feedback-form")).not.toHaveAttribute("hidden");
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("this diagram is upside down");
  });

  it("survives Escape and a reopen, and cancel throws it away", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <ArtifactOverlay
        open
        url="/api/artifact/j/artifacts/a.md"
        name="j/a.md"
        job={undefined}
        onNavigate={() => {}}
        onAskBranched={() => {}}
        onClose={() => {}}
        mutate={mutate}
      />,
    );
    await user.click(screen.getByText("feedback"));
    await user.type(
      document.getElementById("feedback-text") as HTMLTextAreaElement,
      "note",
    );
    const closed = (
      <ArtifactOverlay
        open={false}
        url=""
        name=""
        job={undefined}
        onNavigate={() => {}}
        onAskBranched={() => {}}
        onClose={() => {}}
        mutate={mutate}
      />
    );
    const reopened = (
      <ArtifactOverlay
        open
        url="/api/artifact/j/artifacts/a.md"
        name="j/a.md"
        job={undefined}
        onNavigate={() => {}}
        onAskBranched={() => {}}
        onClose={() => {}}
        mutate={mutate}
      />
    );
    rerender(closed);
    rerender(reopened);
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("note");

    // cancel is the one gesture that means "throw this away"
    await user.click(screen.getByText("cancel"));
    rerender(closed);
    rerender(reopened);
    expect(document.getElementById("feedback-form")).toHaveAttribute("hidden");
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("");
  });

  it("drops the draft once it is sent", async () => {
    const user = userEvent.setup();
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("feedback"));
    await user.type(
      document.getElementById("feedback-text") as HTMLTextAreaElement,
      "sent note",
    );
    await user.click(screen.getByText("send"));
    await waitFor(() => expect(createFeedback).toHaveBeenCalled());
    expect(createFeedback).toHaveBeenCalledWith("j", "artifacts", "a.md", "sent note");
    await user.click(next());
    await user.click(prev());
    expect(document.getElementById("feedback-form")).toHaveAttribute("hidden");
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("");
  });

  // main.tsx mounts the board inside <StrictMode>, which double-invokes every
  // render. The reset block banks the outgoing note during render (it has to:
  // the values it needs are gone by the time an effect could run), so it is
  // written to be idempotent — this is the assertion that it actually is.
  it("banks and restores identically under StrictMode's double render", async () => {
    const user = userEvent.setup();
    render(
      <StrictMode>
        <Harness job={job(["a.md", "b.md"])} start="a.md" />
      </StrictMode>,
    );
    await user.click(screen.getByText("feedback"));
    await user.type(
      document.getElementById("feedback-text") as HTMLTextAreaElement,
      "strict note",
    );
    await user.click(next());
    await user.click(prev());
    expect(document.getElementById("feedback-form")).not.toHaveAttribute("hidden");
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("strict note");
  });

  it("a send that lands AFTER a step leaves the new artifact's panel alone", async () => {
    const user = userEvent.setup();
    let land!: (v: unknown) => void;
    (createFeedback as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise((r) => (land = r)),
    );
    render(<Harness job={job(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("feedback"));
    await user.type(
      document.getElementById("feedback-text") as HTMLTextAreaElement,
      "a note about a.md",
    );
    await user.click(screen.getByText("send"));

    await user.click(next()); // page onto b.md while the post is in flight
    expect(title()).toBe("j/b.md");
    land({ ok: true });
    await waitFor(() => expect(createFeedback).toHaveBeenCalled());

    // the post itself was right; the PANEL belongs to whatever is on screen —
    // "saved ✓" over b.md would be a signal about a note nobody sent here
    expect(screen.queryByText("saved ✓")).toBeNull();
    expect(document.getElementById("feedback-form")).toHaveAttribute("hidden");
    // …and the sent note is no longer a draft, so a.md comes back empty
    await user.click(prev());
    expect(document.getElementById("feedback-form")).toHaveAttribute("hidden");
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("");
  });
});

describe("askThreadTitle", () => {
  it("names the artifact and the question's first line", () => {
    expect(askThreadTitle("10-flow.md", "why two passes?")).toBe(
      "10-flow.md — why two passes?",
    );
  });

  it("takes the BASENAME — a cell-home artifact may be a subpath", () => {
    expect(askThreadTitle("docs/diagrams/13-tree.html", "what feeds this?")).toBe(
      "13-tree.html — what feeds this?",
    );
  });

  it("takes the first line only — the rest is in the seed, not the chip", () => {
    expect(askThreadTitle("a.md", "why?\nand also how?")).toBe("a.md — why?");
  });

  it("falls back to the artifact alone when there is no question text", () => {
    expect(askThreadTitle("a.md", "   ")).toBe("a.md");
  });

  // 80 is the rename input's own maxLength: a longer title could be branched
  // but never edited back to in place.
  it("caps at 80 so the thread stays renamable", () => {
    const t = askThreadTitle("a.md", "x".repeat(200));
    expect(t.length).toBe(80);
    expect(t.endsWith("…")).toBe(true);
    // exactly at the boundary, nothing is cut
    const exact = askThreadTitle("a.md", "x".repeat(80 - "a.md — ".length));
    expect(exact.length).toBe(80);
    expect(exact.endsWith("…")).toBe(false);
  });

  // slice() counts UTF-16 units, so a cut between the halves of an emoji leaves
  // a lone high surrogate: JSON.stringify escapes it, Go decodes the unpaired
  // escape to U+FFFD, and the thread wears a replacement glyph in its name
  // until someone renames it.
  it("never cuts an emoji in half", () => {
    for (const base of ["a.md", "ab.md", "abc.md", "abcd.md"]) {
      const t = askThreadTitle(base, "🎉".repeat(60));
      expect(t.length).toBeLessThanOrEqual(80);
      // no unpaired surrogate anywhere in the result
      expect(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/.test(t)).toBe(false);
      expect(/(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(t)).toBe(false);
    }
  });

  // The question is the DISTINGUISHING half — it is why this function takes one
  // at all. A single cut at the end would let a long section-doc name eat the
  // whole budget and truncate every question away, making the chips identical
  // again.
  it("keeps the question when the artifact name is very long", () => {
    const long =
      "50-the-very-long-section-document-that-explains-the-token-rule.md";
    const t = askThreadTitle(long, "why two passes?");
    expect(t.length).toBeLessThanOrEqual(80);
    expect(t).toContain("why two passes?");
  });

  it("falls back to the whole name when there is no basename to take", () => {
    // never produced by the board's own artifact list, but a title of
    // " — why?" names nothing at all
    expect(askThreadTitle("docs/", "why?")).toBe("docs/ — why?");
  });
});

describe("askThreadSeed", () => {
  it("names the artifact and carries the question verbatim", () => {
    const s = askThreadSeed("10-flow.md", "  why two passes?  ");
    expect(s).toContain("`10-flow.md`");
    expect(s).toContain("why two passes?");
  });

  // The thread runs in the cell's worktree with a live claude in it. Without
  // this line "elaborate on this section" reads as a licence to go rewrite it,
  // which is the one thing a side thread is not for.
  it("tells the thread not to edit the artifact", () => {
    const s = askThreadSeed("a.md", "q");
    expect(s).toContain("Don't edit the artifact");
  });
});

describe("the ask composer", () => {
  const liveJob = (names: string[]) => job(names, { live: true });

  it("is offered only for a job with a live cell to fork", () => {
    const { rerender } = render(<Harness job={job(["a.md"])} />);
    expect(document.getElementById("panel-ask")).toHaveAttribute("hidden");
    rerender(<Harness job={liveJob(["a.md"])} />);
    expect(document.getElementById("panel-ask")).not.toHaveAttribute("hidden");
  });

  it("branches a thread seeded with the artifact and the question", async () => {
    const user = userEvent.setup();
    const branched = vi.fn();
    render(<Harness job={liveJob(["a.md"])} onAskBranched={branched} />);
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "elaborate on the dedupe rule",
    );
    await user.click(screen.getByText("⑂ branch"));

    await waitFor(() => expect(threadOp).toHaveBeenCalled());
    const [slug, op, extra] = (threadOp as ReturnType<typeof vi.fn>).mock
      .calls[0] as [string, string, { title: string; text: string }];
    expect(slug).toBe("j");
    expect(op).toBe("branch");
    expect(extra.title).toBe("00-overview.md — elaborate on the dedupe rule");
    expect(extra.text).toContain("00-overview.md");
    expect(extra.text).toContain("elaborate on the dedupe rule");
    // the integrator is handed the thread to open — this overlay never opens
    // one itself, because it is the layer that has to close first
    expect(branched).toHaveBeenCalledWith("j", "t1");
  });

  it("refuses an empty question without posting", async () => {
    const user = userEvent.setup();
    render(<Harness job={liveJob(["a.md"])} />);
    await user.click(screen.getByText("⑂ ask"));
    await user.click(screen.getByText("⑂ branch"));
    expect(threadOp).not.toHaveBeenCalled();
    expect(document.getElementById("ask-note")?.textContent).toBe(
      "question is empty",
    );
  });

  // One box, two modes: the words you typed are still your words, and
  // "actually this is a question, not a complaint" is a real thing to notice
  // halfway through writing one.
  it("carries the text across a mode switch rather than dropping it", async () => {
    const user = userEvent.setup();
    render(<Harness job={liveJob(["a.md"])} />);
    await user.click(screen.getByText("feedback"));
    await user.type(
      document.getElementById("feedback-text") as HTMLTextAreaElement,
      "this section is thin",
    );
    await user.click(screen.getByText("⑂ ask"));
    expect(document.getElementById("feedback-form")).toBeNull();
    expect(
      (document.getElementById("ask-text") as HTMLTextAreaElement).value,
    ).toBe("this section is thin");
    // and back the other way
    await user.click(screen.getByText("feedback"));
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("this section is thin");
  });

  it("stashes an unsent question in ITS OWN mode across a step", async () => {
    const user = userEvent.setup();
    render(<Harness job={liveJob(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "why is this ordered like that?",
    );
    await user.click(next());
    expect(document.getElementById("ask-form")).toBeNull();
    await user.click(prev());
    // it comes back as an ASK, not as feedback — a question posted as a
    // feedback row would sit unanswered until the job's next session
    expect(document.getElementById("ask-form")).not.toHaveAttribute("hidden");
    expect(
      (document.getElementById("ask-text") as HTMLTextAreaElement).value,
    ).toBe("why is this ordered like that?");
  });

  it("does not leave the sent question behind as a draft", async () => {
    const user = userEvent.setup();
    render(<Harness job={liveJob(["a.md", "b.md"])} start="a.md" />);
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "asked and answered",
    );
    await user.click(screen.getByText("⑂ branch"));
    await waitFor(() => expect(threadOp).toHaveBeenCalled());
    await user.click(next());
    await user.click(prev());
    expect(document.getElementById("ask-form")).toBeNull();
    expect(
      (document.getElementById("feedback-text") as HTMLTextAreaElement).value,
    ).toBe("");
  });

  it("a branch that lands AFTER a step leaves the new artifact's panel alone", async () => {
    const user = userEvent.setup();
    const branched = vi.fn();
    let land!: (v: unknown) => void;
    (threadOp as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise((r) => (land = r)),
    );
    render(
      <Harness
        job={liveJob(["a.md", "b.md"])}
        start="a.md"
        onAskBranched={branched}
      />,
    );
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "a question about a.md",
    );
    await user.click(screen.getByText("⑂ branch"));

    await user.click(next()); // page onto b.md while the branch is in flight
    expect(title()).toBe("j/b.md");
    land({ ok: true, tid: "t9" });
    await waitFor(() => expect(threadOp).toHaveBeenCalled());

    // the thread was branched and its chip is on the card — but yanking the
    // reader out of the artifact they moved on to is not the answer to a
    // gesture they made two artifacts ago
    expect(branched).not.toHaveBeenCalled();
    expect(screen.queryByText("asked ✓")).toBeNull();
  });

  // Same rule, the other way of leaving: a branch spawns a claude in a new tmux
  // window and takes SECONDS, so "ask, change your mind, press Escape" is an
  // ordinary sequence. The thread still gets branched — but opening it over the
  // board two seconds after the reader walked away is not an answer to
  // anything. The url guard alone cannot see this: the overlay is gone and the
  // ref it captured still names the artifact it fired from.
  it("a branch that lands AFTER the viewer closes does not yank it open", async () => {
    const user = userEvent.setup();
    const branched = vi.fn();
    let land!: (v: unknown) => void;
    (threadOp as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise((r) => (land = r)),
    );
    function Closer() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button onClick={() => setOpen(false)}>close it</button>
          <ArtifactOverlay
            open={open}
            url="/api/artifact/j/artifacts/a.md"
            name="j/a.md"
            job={liveJob(["a.md"])}
            onNavigate={() => {}}
            onAskBranched={branched}
            onClose={() => setOpen(false)}
            mutate={mutate}
          />
        </>
      );
    }
    render(<Closer />);
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "asked then abandoned",
    );
    await user.click(screen.getByText("⑂ branch"));
    await user.click(screen.getByText("close it"));
    land({ ok: true, tid: "t9" });
    await waitFor(() => expect(threadOp).toHaveBeenCalled());
    expect(branched).not.toHaveBeenCalled();
  });

  // ⌘⏎ is new to BOTH modes — the feedback composer never had it — so both
  // halves are asserted: the box dispatches on its mode, not on a fixed target.
  it("⌘⏎ sends from the box, in whichever mode it is in", async () => {
    const user = userEvent.setup();
    render(<Harness job={liveJob(["a.md"])} />);
    await user.click(screen.getByText("⑂ ask"));
    const box = document.getElementById("ask-text") as HTMLTextAreaElement;
    await user.type(box, "sent with the keyboard");
    fireEvent.keyDown(box, { key: "Enter", metaKey: true });
    await waitFor(() => expect(threadOp).toHaveBeenCalled());
    expect(createFeedback).not.toHaveBeenCalled();
  });

  it("⌘⏎ sends the feedback composer too", async () => {
    const user = userEvent.setup();
    render(<Harness job={liveJob(["a.md"])} />);
    await user.click(screen.getByText("feedback"));
    const box = document.getElementById("feedback-text") as HTMLTextAreaElement;
    await user.type(box, "typed, not clicked");
    fireEvent.keyDown(box, { key: "Enter", metaKey: true });
    await waitFor(() => expect(createFeedback).toHaveBeenCalled());
    expect(threadOp).not.toHaveBeenCalled();
  });

  // main.tsx mounts the board inside <StrictMode>, whose mount → cleanup →
  // mount is the entire reason `alive` is re-set inside its effect rather than
  // only by useRef's initial value. Without that line the guard settles false
  // on a live component and every send silently no-ops — in dev only, looking
  // for all the world like a server problem.
  it("still branches when mounted under StrictMode", async () => {
    const user = userEvent.setup();
    const branched = vi.fn();
    render(
      <StrictMode>
        <Harness job={liveJob(["a.md"])} onAskBranched={branched} />
      </StrictMode>,
    );
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "asked under strict mode",
    );
    await user.click(screen.getByText("⑂ branch"));
    await waitFor(() => expect(branched).toHaveBeenCalledWith("j", "t1"));
  });

  // A branch that reports ok without a tid is defensive — the server's branch
  // arm always sets one. There is nothing to open, so the reader is told out of
  // band rather than through the note region this very success just closed.
  it("says so out of band when no tid comes back", async () => {
    const user = userEvent.setup();
    const branched = vi.fn();
    (threadOp as ReturnType<typeof vi.fn>).mockImplementationOnce(() =>
      Promise.resolve({ ok: true }),
    );
    render(<Harness job={liveJob(["a.md"])} onAskBranched={branched} />);
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "no tid for this one",
    );
    await user.click(screen.getByText("⑂ branch"));
    await waitFor(() => expect(threadOp).toHaveBeenCalled());
    expect(branched).not.toHaveBeenCalled();
    // the out-of-band channel itself, not just its absence of a crash — the
    // "asked ✓" below flashes on every success and cannot tell the two apart
    await waitFor(() => expect(showToast).toHaveBeenCalled());
    // the overlay stays put and the head button carries the confirmation
    expect(screen.queryByText("asked ✓")).not.toBeNull();
  });

  // Every control that can close or switch the composer, since the note region
  // lives inside it: closing mid-branch would route the branch's own outcome
  // into a hidden element, switching would land it in the other mode's, and
  // cancel would do the first while the thread opens anyway.
  it("locks every composer-closing control while a branch is in flight", async () => {
    const user = userEvent.setup();
    let land!: (v: unknown) => void;
    (threadOp as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise((r) => (land = r)),
    );
    render(<Harness job={liveJob(["a.md"])} />);
    await user.click(screen.getByText("⑂ ask"));
    await user.type(
      document.getElementById("ask-text") as HTMLTextAreaElement,
      "in flight",
    );
    await user.click(screen.getByText("⑂ branch"));
    expect(document.getElementById("panel-ask")).toBeDisabled();
    expect(document.getElementById("panel-feedback")).toBeDisabled();
    expect(document.getElementById("ask-cancel")).toBeDisabled();
    land({ ok: true, tid: "t1" });
    await waitFor(() => expect(threadOp).toHaveBeenCalled());
  });
});

describe("re-syncing the sequence under an open viewer", () => {
  // The board polls every 3s and a live cell writes artifacts while you read.
  function Poller({ names, start }: { names: string[]; start: string }) {
    const [list, setList] = useState(names);
    return (
      <>
        <button onClick={() => setList([...list, "90-build-report.md"])}>
          poll+
        </button>
        <button onClick={() => setList(list.filter((n) => n !== start))}>
          poll-
        </button>
        <Harness job={job(list)} start={start} />
      </>
    );
  }

  it("grows N when the job gains an artifact", async () => {
    const user = userEvent.setup();
    render(<Poller names={["a.md", "b.md"]} start="a.md" />);
    expect(pos()).toBe("1 / 2");
    await user.click(screen.getByText("poll+"));
    expect(pos()).toBe("1 / 3");
    // …and › reaches the new one without a reopen
    await user.click(next());
    await user.click(next());
    expect(title()).toBe("j/90-build-report.md");
  });

  it("re-finds the position by URL when the list gains entries AHEAD of it", async () => {
    const user = userEvent.setup();
    function Grower() {
      const [list, setList] = useState(["b.md", "c.md"]);
      return (
        <>
          <button onClick={() => setList(["a.md", ...list])}>prepend</button>
          <Harness job={job(list)} start="b.md" />
        </>
      );
    }
    render(<Grower />);
    expect(pos()).toBe("1 / 2");
    await user.click(screen.getByText("prepend"));
    // the position moved with the artifact, it did not stay on index 0 and
    // silently start describing a.md
    expect(pos()).toBe("2 / 3");
    expect(title()).toBe("j/b.md");
  });

  it("KEEPS the sequence when the open artifact leaves the job's list", async () => {
    const user = userEvent.setup();
    render(<Poller names={["a.md", "b.md"]} start="a.md" />);
    await user.click(screen.getByText("poll-")); // a.md deleted/renamed
    // it never closes the viewer, and the neighbours stay reachable: falling
    // back to a singleton here would strand the reader mid-set
    expect(pos()).toBe("1 / 2");
    expect(pager()).not.toHaveAttribute("hidden");
    await user.click(next());
    expect(title()).toBe("j/b.md");
  });
});

/**
 * The board hands this overlay a freshly parsed `job` on every 3s poll, so
 * the overlay re-renders on every tick whether or not anything it shows has
 * changed. What must NOT follow is a re-render of the rendered markdown into
 * the DOM: `dangerouslySetInnerHTML` is diffed by the identity of the
 * `{__html}` value, not by the HTML string, so an object literal written
 * inline in JSX made React tear the whole body down and re-parse it every
 * tick — taking the reader's text selection with it and leaving the scroll
 * offset to whatever the browser re-derived for a subtree that no longer
 * existed. ThreadOverlay already guards against exactly this imperatively;
 * these two pin the declarative fix (hooks/useRenderedMD.ts).
 */
describe("the markdown body survives a board poll", () => {
  // A tick that changes NOTHING the viewer renders, only the identity of the
  // job object — which is what a 3s board poll is for a settled job.
  function Tick({ names, start }: { names: string[]; start: string }) {
    const [n, setN] = useState(0);
    return (
      <>
        <button onClick={() => setN(n + 1)}>tick {n}</button>
        {/* job() builds a fresh WebJob (and fresh artifact objects) per render,
            exactly as a poll's freshly parsed payload does */}
        <Harness job={job(names)} start={start} />
      </>
    );
  }

  it("keeps the very same DOM nodes when the tick changed nothing it renders", async () => {
    const user = userEvent.setup();
    render(<Tick names={["a.md", "b.md"]} start="a.md" />);
    const body = await waitFor(() => {
      const el = document.getElementById("panel-body");
      if (!el?.firstElementChild) throw new Error("body not rendered yet");
      return el;
    });
    const before = body.firstElementChild;

    await user.click(screen.getByText(/^tick/)); // a tick: new job object

    // the container is the same element AND its children were never replaced
    expect(document.getElementById("panel-body")).toBe(body);
    expect(body.firstElementChild).toBe(before);
  });

  it("still repaints when the artifact itself changes", async () => {
    const user = userEvent.setup();
    render(<Tick names={["a.md", "b.md"]} start="a.md" />);
    const first = await waitFor(() => {
      const t = document.getElementById("panel-body")?.textContent ?? "";
      if (!t.includes("a.md")) throw new Error("not loaded yet");
      return t;
    });
    await user.click(next()); // step to b.md
    await waitFor(() => {
      const t = document.getElementById("panel-body")?.textContent ?? "";
      expect(t).not.toBe(first);
      expect(t).toContain("b.md");
    });
  });
});
