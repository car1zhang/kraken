import { fireEvent, render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebDreamReport, WebWatchItem } from "../../api/types.ts";
import { DreamView } from "./DreamView.tsx";
import {
  dreamDay,
  dreamPRCount,
  dreamSectionCount,
  sortWatch,
  wlSeenText,
} from "./dreamText.ts";

vi.mock("../../api/client.ts", () => ({
  fetchDream: vi.fn(),
  fetchWatchlist: vi.fn(),
  setWatchVerdict: vi.fn(),
}));
const { fetchDream, fetchWatchlist, setWatchVerdict } = await import(
  "../../api/client.ts"
);
const mockDream = vi.mocked(fetchDream);
const mockWatch = vi.mocked(fetchWatchlist);
const mockVerdict = vi.mocked(setWatchVerdict);

/**
 * WIRE FIXTURES — transcribed key-for-key from the Go structs, not invented.
 *
 * `report` carries all three of api.webDreamReport's fields (none tagged
 * omitempty, so all three are always present) and `watchItem` carries every
 * one of api.webWatchItem's, optional halves included. If a Go json tag is
 * renamed, retyped or dropped, these two literals stop typechecking — which
 * is the only mechanism this side of the wire has for noticing.
 */
const report = (over: Partial<WebDreamReport> = {}): WebDreamReport => ({
  date: "2026-07-26",
  slug: "dream-2026-07-26",
  body: "# night\n\n## Shipped\n\n- https://github.com/car1zhang/kraken/pull/7\n",
  ...over,
});

const watchItem = (over: Partial<WebWatchItem> = {}): WebWatchItem => ({
  id: 1,
  observation: "zeus re-reads the same skill every phase",
  class: "repetition",
  state: "watching",
  sightings: 3,
  first_seen: Date.UTC(2026, 6, 20, 18, 0),
  last_seen: Date.UTC(2026, 6, 26, 18, 0),
  evidence: '{"n":3}',
  suggested_fix: "cache the skill text per cell",
  pr_url: "https://github.com/car1zhang/kraken/pull/9",
  updated_at: Date.UTC(2026, 6, 26, 18, 0),
  verdict: "fix",
  comment: "do it in jobs/skills.go",
  commented_at: "2026-07-26T18:00:00Z",
  ...over,
});

/** App's mutate: the parsed body on success, null on failure. */
const mutate = async <T,>(op: () => Promise<T>): Promise<T | null> => {
  try {
    return await op();
  } catch {
    return null;
  }
};
const toast = vi.fn();

const view = (active = true) =>
  render(<DreamView active={active} mutate={mutate} onToast={toast} />);

beforeEach(() => {
  mockDream.mockReset();
  mockWatch.mockReset();
  mockVerdict.mockReset();
  toast.mockReset();
  mockDream.mockResolvedValue({ reports: [] });
  mockWatch.mockResolvedValue({ watchlist: [] });
  mockVerdict.mockResolvedValue({ ok: true } as never);
});

describe("<DreamView>", () => {
  it("renders both panes with their count spans", async () => {
    mockDream.mockResolvedValue({
      reports: [report(), report({ date: "2026-07-25", slug: "dream-2026-07-25" })],
    });
    mockWatch.mockResolvedValue({
      watchlist: [
        watchItem({ id: 1 }),
        watchItem({ id: 2, verdict: "fine", comment: "leave it" }),
        watchItem({ id: 3, verdict: undefined, comment: undefined }),
      ],
    });
    const { container } = view();

    await waitFor(() =>
      expect(container.querySelectorAll(".rep")).toHaveLength(2),
    );
    expect(container.querySelector("main.dreamview")?.id).toBe("dream");
    expect(container.querySelectorAll(".dream-pane")).toHaveLength(2);
    expect(container.querySelector("#dream-reports")).not.toBeNull();
    expect(container.querySelector("#dream-watchlist")).not.toBeNull();
    expect(container.querySelector("#dream-reports-ct")?.textContent).toBe(
      "· 2 nights",
    );
    await waitFor(() =>
      expect(container.querySelectorAll(".wl")).toHaveLength(3),
    );
    // 3 rows, one ruled fine → 2 still open
    expect(container.querySelector("#dream-watch-ct")?.textContent).toBe("· 2 open");
    // the settled row sinks to the bottom and dims
    const rows = [...container.querySelectorAll(".wl")];
    expect(rows.at(-1)?.getAttribute("data-wl-id")).toBe("2");
    expect(rows.at(-1)?.className).toContain("done");
  });

  it("does not poll while it is not the active view", async () => {
    const { container } = view(false);
    await new Promise((r) => setTimeout(r, 20));
    expect(mockDream).not.toHaveBeenCalled();
    expect(mockWatch).not.toHaveBeenCalled();
    expect(
      container.querySelector("main.dreamview")?.hasAttribute("hidden"),
    ).toBe(true);
  });

  it("renders the empty state for each pane rather than a blank one", async () => {
    const { container } = view();
    await waitFor(() => expect(mockDream).toHaveBeenCalled());
    await waitFor(() =>
      expect(container.querySelectorAll(".dream-empty")).toHaveLength(2),
    );
    const [reports, watch] = [...container.querySelectorAll(".dream-empty")];
    expect(reports?.textContent).toContain("no reports yet");
    expect(watch?.textContent).toContain("nothing on the watchlist");
  });

  it("shows an endpoint failure in place of that pane's empty state, and only that pane's", async () => {
    mockWatch.mockRejectedValue(new Error("no store wired"));
    const { container } = view();
    await waitFor(() =>
      expect(container.querySelector("#dream-watchlist")?.textContent).toContain(
        "couldn't load the watchlist: no store wired",
      ),
    );
    // the reports pane resolved fine and kept its own empty state
    expect(container.querySelector("#dream-reports")?.textContent).toContain(
      "no reports yet",
    );
  });

  it("seeds the newest night open and toggles on click", async () => {
    mockDream.mockResolvedValue({
      reports: [report(), report({ date: "2026-07-25", slug: "dream-2026-07-25" })],
    });
    const { container } = view();

    await waitFor(() =>
      expect(container.querySelector(".rep.open")).not.toBeNull(),
    );
    const heads = container.querySelectorAll<HTMLButtonElement>(".rep-h");
    expect(heads[0]?.dataset.date).toBe("2026-07-26");
    expect(heads[0]?.getAttribute("aria-expanded")).toBe("true");
    expect(heads[1]?.getAttribute("aria-expanded")).toBe("false");
    // the body is only in the DOM while open, and it is rendered markdown
    expect(container.querySelectorAll(".rep-b.md")).toHaveLength(1);
    expect(container.querySelector(".rep-b.md h1")?.textContent).toBe("night");

    fireEvent.click(heads[0]!);
    await waitFor(() => expect(container.querySelector(".rep.open")).toBeNull());
    expect(container.querySelectorAll(".rep-b")).toHaveLength(0);
  });

  it("tags a report with its PR / watching / judgment-call counts", async () => {
    mockDream.mockResolvedValue({
      reports: [
        report({
          body: [
            "## Shipped",
            "- fixed https://github.com/car1zhang/kraken/pull/7 and /pull/7 again",
            "## Watching",
            "- a",
            "- b",
            "## Judgment calls",
            "- c",
          ].join("\n"),
        }),
      ],
    });
    const { container } = view();
    await waitFor(() =>
      expect(container.querySelectorAll(".rep-tag")).toHaveLength(3),
    );
    expect(container.querySelector(".rep-tag.pr")?.textContent).toBe("1 PR");
    expect(container.querySelector(".rep-tag.watch")?.textContent).toBe("2 watching");
    expect(container.querySelector(".rep-tag.judge")?.textContent).toBe(
      "1 judgment call",
    );
  });

  it("calls a quiet night quiet", async () => {
    mockDream.mockResolvedValue({
      reports: [report({ body: "## Shipped\n\nNone.\n" })],
    });
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".rep")).not.toBeNull());
    expect(container.querySelectorAll(".rep-tag")).toHaveLength(0);
    expect(container.querySelector(".rep-h .sum")?.textContent).toBe(
      "nothing shipped",
    );
  });

  it("posts a verdict with whatever is in the comment box", async () => {
    mockWatch.mockResolvedValue({
      watchlist: [watchItem({ verdict: undefined, comment: undefined })],
    });
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".wl")).not.toBeNull());

    const box = container.querySelector<HTMLTextAreaElement>(".wl-comment")!;
    fireEvent.change(box, { target: { value: "  memoize it  " } });
    fireEvent.click(container.querySelector<HTMLButtonElement>(".wl-btn.fix")!);

    await waitFor(() => expect(mockVerdict).toHaveBeenCalledTimes(1));
    expect(mockVerdict).toHaveBeenCalledWith(1, "fix", "memoize it");
  });

  it("clears the verdict when the one already in force is clicked again", async () => {
    mockWatch.mockResolvedValue({ watchlist: [watchItem({ verdict: "fix" })] });
    const { container } = view();
    await waitFor(() =>
      expect(container.querySelector(".wl-btn.fix.on")).not.toBeNull(),
    );
    fireEvent.click(container.querySelector<HTMLButtonElement>(".wl-btn.fix")!);
    await waitFor(() => expect(mockVerdict).toHaveBeenCalledTimes(1));
    expect(mockVerdict).toHaveBeenCalledWith(1, "", "do it in jobs/skills.go");
  });

  // The row must survive its OWN success. It is keyed by id and
  // store.SetWatchVerdict writes only verdict/comment/commented_at — `state`
  // stays "watching", so the row comes back in the same list and React
  // reconciles the SAME instance rather than remounting it. Vanilla's
  // one-sided re-enable only ever worked because renderDreamWatchlist
  // replaced the pane's innerHTML wholesale; asserting on the failure path
  // alone reproduces that blind spot exactly.
  it("re-enables the row after a SUCCESSFUL verdict, so the ruling can be changed", async () => {
    mockWatch.mockResolvedValue({
      watchlist: [watchItem({ verdict: undefined, comment: undefined })],
    });
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".wl")).not.toBeNull());

    const rowNode = container.querySelector(".wl")!;
    const boxNode = container.querySelector<HTMLTextAreaElement>(".wl-comment")!;
    fireEvent.change(boxNode, { target: { value: "memoize it" } });

    // exactly what the server serves back afterwards: judged, still watching
    mockWatch.mockResolvedValue({
      watchlist: [
        watchItem({
          verdict: "fix",
          comment: "memoize it",
          state: "watching",
          commented_at: "2026-07-27T09:00:00Z",
        }),
      ],
    });
    fireEvent.click(container.querySelector<HTMLButtonElement>(".wl-btn.fix")!);

    await waitFor(() => expect(mockVerdict).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(container.querySelector(".wl-btn.fix.on")).not.toBeNull(),
    );

    // NOT remounted: the very DOM nodes are the ones from before the click, so
    // nothing has handed this row fresh enabled controls.
    expect(container.querySelectorAll(".wl")).toHaveLength(1);
    expect(container.querySelector(".wl")).toBe(rowNode);
    expect(container.querySelector(".wl-comment")).toBe(boxNode);

    const fix = container.querySelector<HTMLButtonElement>(".wl-btn.fix")!;
    const fine = container.querySelector<HTMLButtonElement>(".wl-btn.fine")!;
    expect(fix.disabled).toBe(false);
    expect(fine.disabled).toBe(false);
    expect(boxNode.disabled).toBe(false);

    // and the comment — the spec the next dream run follows — is still
    // editable, showing what the server now stores
    expect(boxNode.value).toBe("memoize it");
    fireEvent.change(boxNode, { target: { value: "actually, drop it" } });
    expect(boxNode.value).toBe("actually, drop it");

    // the undo the docstring promises: clicking the verdict in force clears it
    fireEvent.click(fix);
    await waitFor(() => expect(mockVerdict).toHaveBeenCalledTimes(2));
    expect(mockVerdict).toHaveBeenLastCalledWith(1, "", "actually, drop it");
  });

  it("re-enables a settled row after a successful verdict too", async () => {
    // `fine` drops the textarea for a read-back, so this row's ONLY way back
    // is its two buttons — a mis-click on "stop even watching this" has to be
    // undoable.
    mockWatch.mockResolvedValue({
      watchlist: [watchItem({ verdict: undefined, comment: undefined })],
    });
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".wl")).not.toBeNull());

    mockWatch.mockResolvedValue({
      watchlist: [watchItem({ verdict: "fine", comment: "", state: "watching" })],
    });
    fireEvent.click(container.querySelector<HTMLButtonElement>(".wl-btn.fine")!);

    await waitFor(() => expect(container.querySelector(".wl.done")).not.toBeNull());
    const fine = container.querySelector<HTMLButtonElement>(".wl-btn.fine")!;
    expect(fine.disabled).toBe(false);
    fireEvent.click(fine);
    await waitFor(() => expect(mockVerdict).toHaveBeenCalledTimes(2));
    expect(mockVerdict).toHaveBeenLastCalledWith(1, "", "");
  });

  it("re-enables a row and toasts when the verdict is refused", async () => {
    mockWatch.mockResolvedValue({ watchlist: [watchItem()] });
    mockVerdict.mockRejectedValue(new Error("unknown watch item"));
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".wl")).not.toBeNull());

    const fine = container.querySelector<HTMLButtonElement>(".wl-btn.fine")!;
    fireEvent.click(fine);
    await waitFor(() => expect(toast).toHaveBeenCalled());
    await waitFor(() => expect(fine.disabled).toBe(false));
  });

  it("swaps a settled row's comment box for a read-back", async () => {
    mockWatch.mockResolvedValue({
      watchlist: [watchItem({ verdict: "fine", comment: "not worth it" })],
    });
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".wl.done")).not.toBeNull());
    expect(container.querySelector(".wl-comment")).toBeNull();
    expect(container.querySelector(".wl-said")?.textContent).toBe(
      "you saidnot worth it",
    );
  });

  it("renders a row carrying only its required fields", async () => {
    // every optional key of api.webWatchItem absent — an unjudged observation
    // with no evidence, no suggested fix and no PR
    mockWatch.mockResolvedValue({
      watchlist: [
        {
          id: 9,
          observation: "bare",
          class: "waste",
          state: "watching",
          sightings: 1,
          first_seen: 0,
          last_seen: 0,
          updated_at: 0,
        },
      ],
    });
    const { container } = view();
    await waitFor(() => expect(container.querySelector(".wl")).not.toBeNull());
    expect(container.querySelector(".wl-fix")).toBeNull();
    expect(container.querySelector(".pr-chip")).toBeNull();
    expect(container.querySelector(".wl-stamp")).toBeNull();
    expect(container.querySelector(".wl-h .obs")?.textContent).toBe("bare");
    // a zero timestamp reads as blank, never "1970"
    expect(container.querySelector(".wl-meta .seen")?.textContent).toBe("1 sighting");
  });
});

describe("dreamText", () => {
  it("shortens both timestamp shapes to a local MM-DD, and junk to blank", () => {
    expect(dreamDay(new Date(2026, 6, 26, 23, 30).getTime())).toBe("07-26");
    expect(dreamDay("2026-07-26T18:00:00Z")).toBe("07-26");
    expect(dreamDay(0)).toBe("");
    expect(dreamDay("")).toBe("");
    expect(dreamDay(undefined)).toBe("");
  });

  it("counts bullets under the first matching heading, at any level", () => {
    const body = ["# Watching", "- a", "- b", "## Shipped", "- c"].join("\n");
    expect(dreamSectionCount(body, /^watching\b/i)).toBe(2);
    expect(dreamSectionCount(body, /^shipped\b/i)).toBe(1);
    expect(dreamSectionCount("", /^watching\b/i)).toBe(0);
  });

  it("counts DISTINCT PR urls and stops before trailing punctuation", () => {
    const body =
      "see [#7](https://x/pull/7) and https://x/pull/7, plus https://x/pull/8.";
    expect(dreamPRCount(body)).toBe(2);
  });

  it("prints one sighting without a window, and a span with one", () => {
    expect(wlSeenText(watchItem({ sightings: 1, first_seen: 0, last_seen: 0 }))).toBe(
      "1 sighting",
    );
    const w = wlSeenText(
      watchItem({
        sightings: 4,
        first_seen: new Date(2026, 6, 20).getTime(),
        last_seen: new Date(2026, 6, 26).getTime(),
      }),
    );
    expect(w).toBe("4 sightings · first 07-20 · last 07-26");
  });

  it("sinks `fine` rows below everything still open, newest first within each", () => {
    const rows = sortWatch([
      watchItem({ id: 1, verdict: "fine", last_seen: 500 }),
      watchItem({ id: 2, verdict: undefined, last_seen: 100 }),
      watchItem({ id: 3, verdict: "fix", last_seen: 300 }),
    ]);
    expect(rows.map((r) => r.id)).toEqual([3, 2, 1]);
  });
});
