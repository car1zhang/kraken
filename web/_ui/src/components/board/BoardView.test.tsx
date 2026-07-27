import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
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

function renderBoard(jobs: WebJob[]) {
  const actions = stubActions();
  const { container } = render(<BoardView board={model(jobs)} actions={actions} />);
  return { container, actions };
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
