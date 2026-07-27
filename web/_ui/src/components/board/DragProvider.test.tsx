// Wiring tests: these go through dnd-kit for real — a mousedown on a rendered
// card, a mousemove past the activation distance, and the sensor doing its
// own job. jsdom reports every rect as 0×0, so nothing here asserts WHERE a
// card lands (useDragReorder.test.tsx owns that); what it does assert is that
// a drag starts at all from a card, and does NOT start from a form control
// inside one.

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api/client.ts", () => ({
  queueJob: vi.fn(async () => ({ ok: true })),
  dequeueJob: vi.fn(async () => ({ ok: true })),
  doneJob: vi.fn(async () => ({ ok: true })),
  startJob: vi.fn(async () => ({ ok: true })),
  stopJob: vi.fn(async () => ({ ok: true })),
  archiveJob: vi.fn(async () => ({ ok: true })),
  reactivateJob: vi.fn(async () => ({ ok: true })),
  reorderColumn: vi.fn(async () => ({ ok: true })),
}));

import type { WebBoard, WebJob } from "../../api/types.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { ARCHIVED } from "../../lib/moves.ts";
import type { DropColumn } from "../../lib/moves.ts";
import { bucketJobs } from "../../lib/format.ts";
import type { BoardModel, Column } from "../../hooks/useBoard.ts";
import { makeBoard as makeWebBoard, makeJob } from "../../test/fixtures.ts";
import {
  DragProvider,
  SortableColumn,
  useColumnDrop,
  useColumnJobs,
  useSortableCard,
} from "./DragProvider.tsx";

const job = (slug: string, state: string, over: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug, state, ...over });

const JOBS = [job("d1", "draft"), job("d2", "draft"), job("z1", "done")];

const setDragging = vi.fn<(dragging: boolean) => void>();

// Bucketed by bucketJobs, never by a local `j.state` loop — see the same note
// in useDragReorder.test.tsx.
function makeModel(jobs: WebJob[]): BoardModel {
  const byKey = new Map<string, WebJob>();
  for (const j of jobs) byKey.set(jobKey(j), j);
  const { byColumn, archived } = bucketJobs(jobs);
  const board: WebBoard = makeWebBoard({ jobs });
  return {
    board,
    error: undefined,
    byKey,
    byColumn,
    archived,
    refetch: async () => {},
    setDragging,
    mutate: async (fn) => {
      await fn();
      return null;
    },
    lastError: null,
  };
}

// A stand-in for JobCard, spreading exactly what useSortableCard returns.
function Card({ slug }: { slug: string }) {
  const s = useSortableCard(slug);
  return (
    <div
      ref={s.setNodeRef}
      data-testid={`card-${slug}`}
      className={`card ${s.className}`}
      style={s.style}
      {...s.attributes}
      {...s.listeners}
    >
      <span className="slug">{slug}</span>
      <div className="job-feedback">
        <textarea data-testid={`fb-${slug}`} defaultValue="" />
      </div>
    </div>
  );
}

function Col({ column, jobs }: { column: Column; jobs: WebJob[] }) {
  const drop = useColumnDrop(column);
  const ordered = useColumnJobs(column, jobs);
  return (
    <section
      ref={drop.setNodeRef}
      data-testid={`col-${column}`}
      className={`col ${drop.className}`.trim()}
    >
      <SortableColumn column={column} jobs={jobs}>
        {ordered.map((j) => (
          <Card key={jobKey(j)} slug={j.slug} />
        ))}
      </SortableColumn>
    </section>
  );
}

function Drawer() {
  const drop = useColumnDrop(ARCHIVED as DropColumn);
  return (
    <div
      ref={drop.setNodeRef}
      data-testid="drawer"
      className={`archive-drawer ${drop.className}`.trim()}
    />
  );
}

function Board() {
  const board = makeModel(JOBS);
  return (
    <DragProvider board={board} onOpenSteer={() => {}}>
      <Col column="draft" jobs={board.byColumn.draft} />
      <Col column="queued" jobs={board.byColumn.queued} />
      <Col column="done" jobs={board.byColumn.done} />
      <Drawer />
    </DragProvider>
  );
}

/** mousedown on `el`, then travel past MOUSE_ACTIVATION.distance. */
function pressAndDrag(el: Element) {
  fireEvent.mouseDown(el, { button: 0, clientX: 0, clientY: 0 });
  fireEvent.mouseMove(document, { clientX: 0, clientY: 40 });
}

beforeEach(() => {
  setDragging.mockClear();
});

describe("DragProvider", () => {
  it("starts a drag from the card body and suppresses the poll", () => {
    render(<Board />);
    pressAndDrag(screen.getByTestId("card-d1"));
    expect(setDragging).toHaveBeenCalledWith(true);
  });

  it("does NOT start a drag from a textarea inside the card", () => {
    render(<Board />);
    // The vanilla bug this reproduces: a press-drag over a form control in a
    // draggable ancestor drags the ANCESTOR, so selecting a word of typed
    // feedback posted a real column move.
    pressAndDrag(screen.getByTestId("fb-d1"));
    expect(setDragging).not.toHaveBeenCalledWith(true);
  });

  it("lights up only the legal targets while a card is in the air", () => {
    render(<Board />);
    expect(screen.getByTestId("col-queued").className).not.toContain("droptarget");
    pressAndDrag(screen.getByTestId("card-d1"));
    // draft → queued is the only legal destination for a draft card
    expect(screen.getByTestId("col-queued").className).toContain("droptarget");
    expect(screen.getByTestId("col-done").className).not.toContain("droptarget");
    expect(screen.getByTestId("drawer").className).not.toContain("droptarget");
    // and never its own column: a reorder shows itself by the cards shifting
    expect(screen.getByTestId("col-draft").className).not.toContain("droptarget");
  });

  it("makes the archive drawer a drop target for a done card", () => {
    render(<Board />);
    pressAndDrag(screen.getByTestId("card-z1"));
    expect(screen.getByTestId("drawer").className).toContain("droptarget");
  });

  it("renders a drag overlay carrying the dragged job", () => {
    render(<Board />);
    pressAndDrag(screen.getByTestId("card-d1"));
    expect(document.querySelectorAll(".dragging-overlay")).toHaveLength(1);
  });

  it("renders cards in server order with nothing dragged", () => {
    render(<Board />);
    const slugs = [...screen.getByTestId("col-draft").querySelectorAll(".slug")].map(
      (e) => e.textContent,
    );
    expect(slugs).toEqual(["d1", "d2"]);
  });
});
