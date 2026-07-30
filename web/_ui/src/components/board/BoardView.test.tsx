import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { COLUMNS } from "../../lib/columns.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { BoardView, type BoardActions } from "./BoardView.tsx";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";

// mtime is seconds here (what `age()` reads), overriding the fixture's millis.
const job = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over });

const stubActions = (): BoardActions => ({
  onOpenSteer: vi.fn(),
  onOpenEditor: vi.fn(),
  onOpenArtifact: vi.fn(),
  onOpenThread: vi.fn(),
  onAnswerQuestion: vi.fn(),
  onRetry: vi.fn(),
  onDequeue: vi.fn(),
  onOpenTmux: vi.fn(),
  onSaveFeedback: vi.fn(async () => null),
  onDeleteFeedback: vi.fn(),
  onUnarchive: vi.fn(),
  onDeleteForever: vi.fn(),
});

function model(jobs: WebJob[]): BoardModel {
  const board: WebBoard = makeBoard({
    daemon: makeDaemon({ pid: 42, last_tick: Math.floor(Date.now() / 1000) }),
    cell_cap: 3,
    live_cells: 1,
    jobs,
  });
  // The REAL bucketer, not a re-implementation of it: a fixture that buckets
  // by hand can only ever confirm its own copy of the rule (this is exactly how
  // the drag suite stayed blind to the render/drag bucketing split).
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board,
    error: undefined,
    byKey: new Map(jobs.map((j) => [jobKey(j), j])),
    byColumn,
    archived,
    refetch: async () => {},
    setDragging: () => {},
    mutate: async () => null,
    lastError: null,
  };
}

/**
 * The board as App.tsx assembles it: App owns the search (DragProvider needs
 * the same instance) and hands BoardView the model plus that search. Rendering
 * BoardView through this harness rather than bare is what lets these tests
 * type into the bar and see the columns respond.
 */
function BoardHarness({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <BoardView board={board} actions={actions} search={search} />;
}

function renderBoard(jobs: WebJob[]) {
  const actions = stubActions();
  // The model is held rather than inlined: the search suite below asserts that
  // BoardView never narrows it, which needs the same object the render got.
  const board = model(jobs);
  const { container } = render(<BoardHarness board={board} actions={actions} />);
  return { container, actions, board };
}

const cols = (container: HTMLElement) => [...container.querySelectorAll("section.col")];
const colFor = (container: HTMLElement, key: string) =>
  container.querySelector(`section.col[data-col="${key}"]`)!;

beforeEach(() => {
  localStorage.clear();
});

describe("columns", () => {
  it("renders all five, in order, whether or not they hold anything", () => {
    const { container } = renderBoard([job({ slug: "a", state: "queued" })]);
    expect(cols(container).map((c) => c.getAttribute("data-col"))).toEqual([...COLUMNS]);
  });

  it("buckets each job by its state", () => {
    const { container } = renderBoard([
      job({ slug: "d", state: "draft" }),
      job({ slug: "q", state: "queued" }),
      job({ slug: "w", state: "working", live: true }),
      job({ slug: "b", state: "blocked", live: true }),
      job({ slug: "x", state: "done" }),
    ]);
    for (const [col, slug] of [
      ["draft", "d"],
      ["queued", "q"],
      ["working", "w"],
      ["blocked", "b"],
      ["done", "x"],
    ] as const) {
      const cards = [...colFor(container, col).querySelectorAll(".col-cards > .card")];
      expect(cards.map((c) => c.getAttribute("data-slug"))).toEqual([slug]);
    }
  });

  it("shows the · placeholder in an empty column, and the count in every head", () => {
    const { container } = renderBoard([job({ slug: "q", state: "queued" })]);
    expect(colFor(container, "working").querySelector(".empty")).not.toBeNull();
    expect(colFor(container, "queued").querySelector(".empty")).toBeNull();
    expect(colFor(container, "queued").querySelector(".col-head .n")).toHaveTextContent("1");
    expect(colFor(container, "working").querySelector(".col-head .n")).toHaveTextContent("0");
    expect(colFor(container, "queued").classList.contains("has-cards")).toBe(true);
    expect(colFor(container, "working").classList.contains("has-cards")).toBe(false);
  });

  it("gives DRAFT the new-job button instead of a placeholder", async () => {
    const { container, actions } = renderBoard([]);
    const draft = colFor(container, "draft");
    expect(draft.querySelector(".empty")).toBeNull();
    await userEvent.click(draft.querySelector("#new-job")!);
    expect(actions.onOpenEditor).toHaveBeenCalledWith(null);
  });

  it("puts the archive drawer under DONE and nowhere else", () => {
    const { container } = renderBoard([]);
    expect(colFor(container, "done").querySelector(".archive-drawer")).not.toBeNull();
    expect(container.querySelectorAll(".archive-drawer")).toHaveLength(1);
  });
});

describe("the archive drawer", () => {
  const archived = job({ slug: "old", state: "done", archived: true });

  it("keeps archived jobs out of every column", () => {
    const { container } = renderBoard([job({ slug: "live-one", state: "done" }), archived]);
    const doneCards = [...colFor(container, "done").querySelectorAll(".col-cards > .card")];
    expect(doneCards.map((c) => c.getAttribute("data-slug"))).toEqual(["live-one"]);
    expect(container.querySelector(".archive-head .n")).toHaveTextContent("1");
  });

  it("opens and closes, and its state survives as a localStorage flag", async () => {
    const { container } = renderBoard([archived]);
    expect(container.querySelector(".archive-cards")).toHaveAttribute("hidden");
    await userEvent.click(container.querySelector("#archive-toggle")!);
    expect(container.querySelector(".archive-cards")).not.toHaveAttribute("hidden");
    expect(localStorage.getItem("kraken-archive-open")).toBe("1");
  });

  it("disables the head when there is nothing archived", () => {
    const { container } = renderBoard([]);
    expect(container.querySelector("#archive-toggle")).toBeDisabled();
    expect(container.querySelector(".archive-cards")).toBeNull();
  });

  it("carries unarchive/delete on the bare archive name, and keys the card by jobKey", async () => {
    localStorage.setItem("kraken-archive-open", "1");
    const { container, actions } = renderBoard([archived]);
    const card = container.querySelector(".archive-cards .card.archived")!;
    expect(card.getAttribute("data-slug")).toBe("archive:old");
    await userEvent.click(card.querySelector(".unarchive")!);
    expect(actions.onUnarchive).toHaveBeenCalledWith("old");
  });
});

// ---------------------------------------------------------------------------
// The regression web/jobkey_test.go pins on the vanilla side.
//
// A slug is unique only WITHIN a namespace: archiving `foo` frees the name, so
// a new active `foo` puts TWO entries with slug "foo" on the board, archived
// appended last. Anything keyed by bare slug hands the ARCHIVED entry a
// last-writer-wins victory over the live one — and the symptom was silent and
// nasty: the lookup returned the archived entry (live:false), so a click on
// the LIVE blocked card fell through to the read-only editor instead of the
// unblock composer, and the job became unsteerable from the board.
// ---------------------------------------------------------------------------

describe("a reused slug", () => {
  const live = job({ slug: "foo", state: "blocked", live: true, cell: "kraken-foo" });
  const stale = job({ slug: "foo", state: "done", archived: true, display_slug: "foo" });

  it("renders the LIVE card in its column and the archived one only in the drawer", () => {
    localStorage.setItem("kraken-archive-open", "1");
    const { container } = renderBoard([live, stale]);

    const blocked = [...colFor(container, "blocked").querySelectorAll(".col-cards > .card")];
    expect(blocked).toHaveLength(1);
    expect(blocked[0]!.classList.contains("archived")).toBe(false);
    expect(blocked[0]).toHaveTextContent("kraken-foo");

    const drawer = [...container.querySelectorAll(".archive-cards .card")];
    expect(drawer).toHaveLength(1);
    expect(drawer[0]!.getAttribute("data-slug")).toBe("archive:foo");
  });

  it("leaves the live card steerable — the whole point of jobKey", async () => {
    const { container, actions } = renderBoard([live, stale]);
    await userEvent.click(colFor(container, "blocked").querySelector(".card")!);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("foo", "unblock");
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("routes each twin's artifact count to its OWN drilldown", async () => {
    // The card is the only route to the artifacts now (band 2 carries a bare
    // count), so the two twins must open different things. The archived one
    // opens on its synthetic key; the live one on the bare slug.
    localStorage.setItem("kraken-archive-open", "1");
    const four = ["a", "b", "c", "d"].map((n) => ({ name: n, url: "/x/" + n }));
    const { container, actions } = renderBoard([
      { ...live, artifacts: four },
      { ...stale, artifacts: four },
    ]);

    const liveCard = colFor(container, "blocked").querySelector(".card")!;
    const archivedCard = container.querySelector(".archive-cards .card")!;
    // both say "4", neither names a thing
    expect(liveCard.querySelector(".ct-chip")).toHaveTextContent("▤ 4");
    expect(archivedCard.querySelector(".ct-chip")).toHaveTextContent("▤ 4");
    expect(container.querySelectorAll(".chip")).toHaveLength(0);

    await userEvent.click(archivedCard);
    expect(actions.onOpenEditor).toHaveBeenCalledWith("archive:foo");
    await userEvent.click(liveCard);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("foo", "unblock");
  });
});

// ---------------------------------------------------------------------------
// Search: the bar narrows what is RENDERED and nothing else.
//
// The rule itself is covered case-for-case in lib/jobSearch.test.ts against the
// shared table; what these assert is the wiring — which cards survive, what the
// heads and the drawer do about it, and the one thing that must NOT happen.
// ---------------------------------------------------------------------------

const searchFixture = (): WebJob[] => [
  job({ slug: "search-jobs", repo: "kraken", state: "working", live: true }),
  job({
    slug: "dream-2026-07-29",
    repo: "kraken",
    state: "done",
    detail: "research pass over yesterday's transcripts",
  }),
  job({ slug: "job-load-categorizing", repo: "kraken", state: "done" }),
  job({ slug: "yelp-dedupe", repo: "monolith", state: "blocked", live: true }),
  job({ slug: "asr-wer-metrics", repo: "monolith", state: "queued" }),
  job({
    slug: "search-index-2",
    display_slug: "search-index",
    repo: "monolith",
    state: "archived",
    archived: true,
  }),
];

const searchInput = (container: HTMLElement) =>
  container.querySelector<HTMLInputElement>("#job-search")!;

const shownSlugs = (container: HTMLElement) =>
  [...container.querySelectorAll(".card")].map((c) => c.getAttribute("data-slug"));

describe("the search bar", () => {
  it("renders above the columns, and leaves the board untouched at rest", () => {
    const { container } = renderBoard(searchFixture());
    const bar = container.querySelector(".boardbar")!;
    expect(bar).not.toBeNull();
    expect(bar.compareDocumentPosition(container.querySelector("#board")!)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(container.querySelector(".bb-read")).toHaveTextContent("6 jobs");
    // every head bare, exactly as before the feature existed
    expect(colFor(container, "done").querySelector(".col-head .n")).toHaveTextContent("2");
  });

  it("keeps only the matching cards, and counts them", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "search");
    // search-index-2 is here on its display_slug, "search-index" — case 3 of
    // the shared table in lib/jobSearch.test.ts.
    expect(shownSlugs(container)).toEqual([
      "search-jobs",
      "dream-2026-07-29",
      "archive:search-index-2",
    ]);
    expect(container.querySelector(".bb-read")).toHaveTextContent("3 of 6 jobs");
  });

  it("still renders all five columns, with `shown/total` heads", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "search");
    expect(cols(container).map((c) => c.getAttribute("data-col"))).toEqual([...COLUMNS]);
    expect(colFor(container, "working").querySelector(".col-head .n")).toHaveTextContent("1/1");
    expect(colFor(container, "done").querySelector(".col-head .n")).toHaveTextContent("1/2");
    expect(colFor(container, "queued").querySelector(".col-head .n")).toHaveTextContent("0/1");
    // an emptied column falls back to the board's own placeholder, and DRAFT
    // keeps its button rather than gaining one
    expect(colFor(container, "queued").querySelector(".empty")).not.toBeNull();
    expect(colFor(container, "draft").querySelector(".empty")).toBeNull();
    expect(colFor(container, "draft").querySelector("#new-job")).not.toBeNull();
  });

  it("marks the card that matched only through its status text", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "search");

    const byText = colFor(container, "done").querySelector(".card")!;
    expect(byText.querySelector(".match-why")).toHaveTextContent("matched in status text");
    expect(byText.querySelector(".detail mark")).toHaveTextContent("search");
    expect(byText.querySelector(".detail")).toHaveTextContent("research pass");

    const byName = colFor(container, "working").querySelector(".card")!;
    expect(byName.querySelector(".match-why")).toBeNull();
  });

  it("marks nothing at all with no query", () => {
    const { container } = renderBoard(searchFixture());
    expect(container.querySelector(".match-why")).toBeNull();
    expect(container.querySelector("mark")).toBeNull();
  });

  it("opens the archive drawer for a match inside it, without storing the flag", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "index");

    expect(container.querySelector(".archive-cards")).not.toHaveAttribute("hidden");
    expect(shownSlugs(container)).toEqual(["archive:search-index-2"]);
    expect(container.querySelector(".archive-head .n")).toHaveTextContent("1/1");
    expect(localStorage.getItem("kraken-archive-open")).toBeNull();
  });

  // The header offers no collapse it cannot honour: while the query holds the
  // drawer open the toggle is disabled, so it can neither fail to close it nor
  // write the stored flag from a press that changes nothing on screen.
  it("disables the drawer toggle while the query is what holds it open", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "index");

    const head = container.querySelector("#archive-toggle")!;
    expect(head).toBeDisabled();
    await userEvent.click(head);
    expect(localStorage.getItem("kraken-archive-open")).toBeNull();
    expect(container.querySelector(".archive-cards")).not.toHaveAttribute("hidden");

    // and it comes straight back once the query stops holding it
    await userEvent.click(container.querySelector(".bb-clear")!);
    expect(container.querySelector("#archive-toggle")).toBeEnabled();
  });

  // A bare `ARCHIVED 0` is what an EMPTY archive looks like, and the drawer
  // opening is a search's only route into the archive — so a query matching
  // nothing in it says `0/1` instead: there is a job here, and it is not this
  // one. The head is disabled because a press could reveal nothing, and a
  // press that reveals nothing would still write the stored flag.
  it("says `0/N` rather than `0` when the query misses everything archived", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "zzz");

    const head = container.querySelector("#archive-toggle")!;
    expect(head.querySelector(".n")).toHaveTextContent("0/1");
    expect(head).toBeDisabled();
    expect(container.querySelector(".archive-cards")).toBeNull();

    await userEvent.click(head);
    expect(localStorage.getItem("kraken-archive-open")).toBeNull();
  });

  it("returns the drawer to its stored state when the query clears", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "index");
    expect(container.querySelector(".archive-cards")).not.toHaveAttribute("hidden");

    await userEvent.click(container.querySelector(".bb-clear")!);
    expect(container.querySelector(".archive-cards")).toHaveAttribute("hidden");
    expect(shownSlugs(container)).toHaveLength(6);
  });

  // The board repolls every 3s and BoardView is never unmounted for it, so the
  // query outlives every payload — including one that adds a matching job.
  it("re-applies the query to a payload that arrives while it is typed", async () => {
    const actions = stubActions();
    const { container, rerender } = render(
      <BoardHarness board={model(searchFixture())} actions={actions} />,
    );
    await userEvent.type(searchInput(container), "search");
    expect(shownSlugs(container)).toHaveLength(3);

    const next = [
      ...searchFixture(),
      job({ slug: "search-rewrite", repo: "monolith", state: "queued" }),
    ];
    rerender(<BoardHarness board={model(next)} actions={actions} />);

    expect(searchInput(container)).toHaveValue("search");
    expect(shownSlugs(container)).toContain("search-rewrite");
    expect(container.querySelector(".bb-read")).toHaveTextContent("4 of 7 jobs");
  });

  it("focuses and selects the input on cmd+K", async () => {
    const { container } = renderBoard(searchFixture());
    await userEvent.type(searchInput(container), "dre");
    await userEvent.click(container.querySelector("#board")!);

    const ev = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      bubbles: true,
      cancelable: true,
    });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(searchInput(container));
    expect(searchInput(container).selectionEnd).toBe(3);
    expect(ev.defaultPrevented).toBe(true);
  });

  // THE guard. useDragReorder reads board.byColumn for its drop table and
  // builds the POST /api/reorder body from the WHOLE column; store
  // .SetBoardColumn replaces the stored order with exactly the ids posted and
  // jobs.ApplyColumnOrder floats every absent member to the TOP. So a filter
  // that reached the model would send the hidden cards to the top of their
  // column, permanently, with nothing on screen saying so.
  it("narrows the render without narrowing the model the drag layer reads", async () => {
    const blocked = ["reindex-cache", "telecom-null-sid", "search-rewrite", "ui-polish"].map(
      (slug) => job({ slug, repo: "monolith", state: "blocked", live: true }),
    );
    const { container, board } = renderBoard(blocked);
    const before = board.byColumn.blocked;

    await userEvent.type(searchInput(container), "re");
    expect(shownSlugs(container)).toEqual(["reindex-cache", "search-rewrite"]);
    expect(colFor(container, "blocked").querySelector(".col-head .n")).toHaveTextContent("2/4");

    expect(board.byColumn.blocked).toBe(before);
    expect(board.byColumn.blocked.map((j) => j.slug)).toEqual([
      "reindex-cache",
      "telecom-null-sid",
      "search-rewrite",
      "ui-polish",
    ]);
  });
});
