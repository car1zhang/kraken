// Drop simulation. dnd-kit's own pointer path needs real element rects, which
// jsdom does not have — so these tests drive the hook's four handlers with the
// events dnd-kit would hand them. That is the seam worth pinning anyway:
// everything above it is dnd-kit's (measurement, collision, animation), and
// everything below it is what posts to a real server.

import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import type { DragEndEvent, DragOverEvent, DragStartEvent } from "@dnd-kit/core";
import { TouchSensor } from "@dnd-kit/core";

vi.mock("../api/client.ts", () => ({
  queueJob: vi.fn(async () => ({ ok: true })),
  dequeueJob: vi.fn(async () => ({ ok: true })),
  doneJob: vi.fn(async () => ({ ok: true })),
  startJob: vi.fn(async () => ({ ok: true })),
  stopJob: vi.fn(async () => ({ ok: true })),
  archiveJob: vi.fn(async () => ({ ok: true })),
  reactivateJob: vi.fn(async () => ({ ok: true })),
  reorderColumn: vi.fn(async () => ({ ok: true })),
}));

import * as client from "../api/client.ts";
import type { WebJob } from "../api/types.ts";
import { jobKey } from "../lib/jobKey.ts";
import { ARCHIVED } from "../lib/moves.ts";
import type { DropColumn, SteerMode } from "../lib/moves.ts";
import { bucketJobs } from "../lib/format.ts";
import { makeBoard as makeWebBoard, makeJob } from "../test/fixtures.ts";
import type { BoardModel, Column } from "./useBoard.ts";
import { COLUMNS } from "./useBoard.ts";
import {
  MOUSE_ACTIVATION,
  TOUCH_ACTIVATION,
  columnDropId,
  parseColumnDropId,
  shouldStartDrag,
  useBoardSensors,
  useDragReorder,
} from "./useDragReorder.ts";

// ------------------------------------------------------------ fixtures

const job = (slug: string, state: string, extra: Partial<WebJob> = {}): WebJob =>
  makeJob({ slug, state, ...extra });

interface Spies {
  setDragging: Mock<(dragging: boolean) => void>;
  refetch: Mock<() => Promise<void>>;
  mutate: Mock<(fn: () => Promise<unknown>) => Promise<Error | null>>;
  onOpenSteer: Mock<
    (slug: string, mode: SteerMode, opts: { requireText: boolean }) => void
  >;
  onToast: Mock<(message: string) => void>;
}

/**
 * A BoardModel built the same way useBoard builds one, without the polling.
 *
 * The bucketing comes from bucketJobs rather than a local `j.state` loop, and
 * that is the whole point: this helper used to re-implement the state-only
 * rule, which made the entire drag suite structurally blind to the column
 * bucketJobs relocates an unhealthy card into. A fixture that re-derives
 * production logic can only ever confirm its own copy of it.
 */
function makeModel(jobs: WebJob[], spies: Spies): BoardModel {
  const byKey = new Map<string, WebJob>();
  for (const j of jobs) byKey.set(jobKey(j), j);
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board: makeWebBoard({ jobs }),
    error: undefined,
    byKey,
    byColumn,
    archived,
    refetch: spies.refetch,
    setDragging: spies.setDragging,
    mutate: spies.mutate,
    lastError: null,
  };
}

function makeSpies(): Spies {
  return {
    setDragging: vi.fn<(dragging: boolean) => void>(),
    refetch: vi.fn<() => Promise<void>>(async () => {}),
    mutate: vi.fn<(fn: () => Promise<unknown>) => Promise<Error | null>>(
      async (fn) => {
        try {
          await fn();
          return null;
        } catch (e) {
          return e as Error;
        }
      },
    ),
    onOpenSteer:
      vi.fn<
        (slug: string, mode: SteerMode, opts: { requireText: boolean }) => void
      >(),
    onToast: vi.fn<(message: string) => void>(),
  };
}

function setup(jobs: WebJob[]) {
  const spies = makeSpies();
  const view = renderHook(
    ({ board }: { board: BoardModel }) =>
      useDragReorder({
        board,
        onOpenSteer: spies.onOpenSteer,
        onToast: spies.onToast,
      }),
    { initialProps: { board: makeModel(jobs, spies) } },
  );
  const setJobs = async (next: WebJob[]) => {
    await act(async () => {
      view.rerender({ board: makeModel(next, spies) });
    });
  };
  return { ...view, spies, setJobs };
}

const startEvent = (key: string) =>
  ({ active: { id: key } }) as unknown as DragStartEvent;
const overEvent = (key: string, overId: string | null) =>
  ({
    active: { id: key },
    over: overId === null ? null : { id: overId },
  }) as unknown as DragOverEvent;
const endEvent = (key: string, overId: string | null) =>
  ({
    active: { id: key },
    over: overId === null ? null : { id: overId },
  }) as unknown as DragEndEvent;

/** Press, hover, release — the whole gesture, awaited. */
async function drag(
  view: ReturnType<typeof setup>,
  key: string,
  overId: string | null,
) {
  await act(async () => {
    view.result.current.onDragStart(startEvent(key));
  });
  await act(async () => {
    view.result.current.onDragOver(overEvent(key, overId));
  });
  await act(async () => {
    view.result.current.onDragEnd(endEvent(key, overId));
  });
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ------------------------------------------------------------ cross-column

describe("cross-column transitions", () => {
  const BOARD = [
    job("d1", "draft"),
    job("q1", "queued"),
    job("w1", "working"),
    job("b1", "blocked"),
    job("z1", "done"),
  ];

  it.each([
    ["d1", "draft", "queued", "queueJob"],
    ["q1", "queued", "draft", "dequeueJob"],
    ["q1", "queued", "working", "startJob"],
    ["w1", "working", "blocked", "stopJob"],
    ["b1", "blocked", "done", "doneJob"],
    ["z1", "done", "blocked", "reactivateJob"],
    ["z1", "done", ARCHIVED, "archiveJob"],
  ] as Array<[string, Column, DropColumn, keyof typeof client]>)(
    "dropping %s (%s) on %s posts via %s",
    async (slug, _from, to, fn) => {
      const view = setup(BOARD);
      await drag(view, slug, columnDropId(to));
      expect(client[fn]).toHaveBeenCalledTimes(1);
      expect(client[fn]).toHaveBeenCalledWith(slug, ...(fn === "reactivateJob" ? [""] : []));
      // pessimistic: routed through mutate(), which refetches either way
      expect(view.spies.mutate).toHaveBeenCalledTimes(1);
      expect(view.spies.onOpenSteer).not.toHaveBeenCalled();
    },
  );

  it("blocked → working opens steer in unblock mode and posts NOTHING", async () => {
    const view = setup(BOARD);
    await drag(view, "b1", columnDropId("working"));
    expect(view.spies.onOpenSteer).toHaveBeenCalledWith("b1", "unblock", {
      requireText: false,
    });
    expect(view.spies.mutate).not.toHaveBeenCalled();
    expect(client.startJob).not.toHaveBeenCalled();
    expect(view.spies.refetch).toHaveBeenCalled();
  });

  it("done → working opens steer in reactivate mode, requiring text", async () => {
    const view = setup(BOARD);
    await drag(view, "z1", columnDropId("working"));
    expect(view.spies.onOpenSteer).toHaveBeenCalledWith("z1", "reactivate", {
      requireText: true,
    });
    expect(client.reactivateJob).not.toHaveBeenCalled();
    expect(view.spies.mutate).not.toHaveBeenCalled();
  });

  it("resolves the drop column from a CARD id, not just a column id", async () => {
    const view = setup(BOARD);
    await drag(view, "d1", "q1"); // dropped between cards in QUEUED
    expect(client.queueJob).toHaveBeenCalledWith("d1");
  });

  it("does nothing for a pair outside the table", async () => {
    const view = setup(BOARD);
    await drag(view, "d1", columnDropId("done")); // draft → done: unlisted
    for (const fn of ["queueJob", "startJob", "doneJob", "archiveJob"] as const) {
      expect(client[fn]).not.toHaveBeenCalled();
    }
    expect(view.spies.mutate).not.toHaveBeenCalled();
    expect(view.spies.onOpenSteer).not.toHaveBeenCalled();
    // still corrects whatever the suppressed poll missed
    expect(view.spies.refetch).toHaveBeenCalled();
  });

  it("does nothing when released outside every drop zone", async () => {
    const view = setup(BOARD);
    await drag(view, "d1", null);
    expect(view.spies.mutate).not.toHaveBeenCalled();
    expect(client.reorderColumn).not.toHaveBeenCalled();
    expect(view.spies.refetch).toHaveBeenCalled();
  });

  it("refuses to drag an archived card at all", async () => {
    const jobs = [...BOARD, job("old", "archived", { archived: true })];
    const view = setup(jobs);
    await drag(view, "archive:old", columnDropId("done"));
    // never suppressed the poll, because no drag was ever accepted
    expect(view.spies.setDragging).not.toHaveBeenCalledWith(true);
    expect(view.spies.mutate).not.toHaveBeenCalled();
    expect(client.doneJob).not.toHaveBeenCalled();
  });
});

// ------------------------------------------- the relocated (unhealthy) card

describe("a card the board relocated into BLOCKED", () => {
  // bucketJobs puts an unhealthy cell in BLOCKED regardless of its file state
  // (app.js:614) — `health` is set by api/board.go on every live cell whose
  // zeus is gone, stuck, or stalled, so its `state` is normally "working"
  // while the card renders in blocked. It is the condition the ⚑ counts.
  //
  // The drag layer used to take `from` from a state-only bucketing, so it
  // disagreed with what the user could see. Vanilla could not have this bug:
  // cardHTML stamped data-col from the RENDERED bucket and dragstart read that
  // attribute back, so `from` was by construction the visible column.
  const SICK = job("sick", "working", {
    live: true,
    health: "cell session is gone",
  });
  const RELOCATED = [job("d1", "draft"), SICK, job("b1", "blocked"), job("z1", "done")];

  it("drags out of the column it RENDERS in, not the one its state names", async () => {
    const view = setup(RELOCATED);
    await drag(view, "sick", columnDropId("done"));
    // blocked → done. Read as working → done this was not in the table at all,
    // so DONE never lit up and the drop was a silent no-op — the user could
    // not mark a sick job done by dragging it out of the column it sits in.
    expect(client.doneJob).toHaveBeenCalledWith("sick");
    expect(client.stopJob).not.toHaveBeenCalled();
  });

  it("lights up the destinations of the column it RENDERS in", async () => {
    const view = setup(RELOCATED);
    await act(async () => {
      view.result.current.onDragStart(startEvent("sick"));
    });
    expect(view.result.current.from).toBe("blocked");
    expect(view.result.current.isTarget("done")).toBe(true);
    expect(view.result.current.isTarget("working")).toBe(true); // steer:unblock
    expect(view.result.current.isTarget("blocked")).toBe(false); // its own column
  });

  it("treats a nudge inside BLOCKED as a reorder, never a STOP", async () => {
    const view = setup(RELOCATED);
    await drag(view, "sick", "b1");
    // Read as working → blocked, this posted /api/stop: a gesture that looks
    // like a reorder interrupted the agent.
    expect(client.stopJob).not.toHaveBeenCalled();
    expect(client.reorderColumn).toHaveBeenCalledWith("blocked", ["b1", "sick"]);
  });

  it("keeps the reorder on the OPTIMISTIC path in a column holding one", async () => {
    const view = setup(RELOCATED);
    await drag(view, "sick", "b1");
    // predictReorderedJobs must count `sick` as a member of blocked. Counting
    // by `j.state` made members !== reordered.length, which rejected every
    // prediction in this column and degraded it to a flashing refetch.
    expect(view.spies.mutate).not.toHaveBeenCalled();
    expect(view.spies.refetch).not.toHaveBeenCalled();
    expect(
      view.result.current.orderJobs("blocked", [SICK, job("b1", "blocked")]).map(
        (j) => j.slug,
      ),
    ).toEqual(["b1", "sick"]);
  });
});

// ------------------------------------------------------------ poll suppression

describe("poll suppression", () => {
  it("pauses the board poll for the length of the drag", async () => {
    const view = setup([job("a", "draft"), job("b", "draft")]);
    await act(async () => {
      view.result.current.onDragStart(startEvent("a"));
    });
    expect(view.spies.setDragging).toHaveBeenLastCalledWith(true);
    await act(async () => {
      view.result.current.onDragEnd(endEvent("a", columnDropId("draft")));
    });
    expect(view.spies.setDragging).toHaveBeenLastCalledWith(false);
  });

  it("resumes the poll and refetches on cancel (ESC)", async () => {
    const view = setup([job("a", "draft")]);
    await act(async () => {
      view.result.current.onDragStart(startEvent("a"));
    });
    await act(async () => {
      view.result.current.onDragCancel();
    });
    expect(view.spies.setDragging).toHaveBeenLastCalledWith(false);
    expect(view.spies.refetch).toHaveBeenCalled();
    expect(client.reorderColumn).not.toHaveBeenCalled();
  });
});

// ------------------------------------------------------------ same-column

describe("same-column reorder", () => {
  const THREE = [job("a", "draft"), job("b", "draft"), job("c", "draft"), job("x", "done")];

  it("posts the whole column's slugs in the new order", async () => {
    const view = setup(THREE);
    await drag(view, "c", "a"); // drop c onto a's slot → c first
    expect(client.reorderColumn).toHaveBeenCalledWith("draft", ["c", "a", "b"]);
  });

  it("appends to the end when dropped on the column body", async () => {
    const view = setup(THREE);
    await drag(view, "a", columnDropId("draft"));
    expect(client.reorderColumn).toHaveBeenCalledWith("draft", ["b", "c", "a"]);
  });

  it("keeps the local order after a successful post — no refetch, no flash", async () => {
    const view = setup(THREE);
    await drag(view, "c", "a");
    expect(
      view.result.current.orderJobs("draft", [
        job("a", "draft"),
        job("b", "draft"),
        job("c", "draft"),
      ]).map((j) => j.slug),
    ).toEqual(["c", "a", "b"]);
    // the reorder path is the ONE optimistic path: it does not go through
    // mutate(), which would refetch and repaint identical content
    expect(view.spies.mutate).not.toHaveBeenCalled();
    expect(view.spies.refetch).not.toHaveBeenCalled();
  });

  it("clears the optimistic order once the server agrees", async () => {
    const view = setup(THREE);
    await drag(view, "c", "a");
    // the next poll returns exactly what was predicted
    await view.setJobs([
      job("c", "draft"),
      job("a", "draft"),
      job("b", "draft"),
      job("x", "done"),
    ]);
    const server = [job("c", "draft"), job("a", "draft"), job("b", "draft")];
    // identity: with the override gone, orderJobs hands back the same array
    expect(view.result.current.orderJobs("draft", server)).toBe(server);
  });

  it("holds the optimistic order through ONE disagreeing payload, then yields", async () => {
    const view = setup(THREE);
    await drag(view, "c", "a");
    const stale = [job("a", "draft"), job("b", "draft"), job("c", "draft"), job("x", "done")];
    // a tick that started before the POST landed (/api/board memoizes 2s)
    await view.setJobs(stale);
    expect(
      view.result.current.orderJobs("draft", stale.slice(0, 3)).map((j) => j.slug),
    ).toEqual(["c", "a", "b"]);
    // a second payload still disagreeing is a real change, not staleness
    await view.setJobs(stale);
    const server = stale.slice(0, 3);
    expect(view.result.current.orderJobs("draft", server)).toBe(server);
  });

  it("reverts, toasts and refetches when the reorder is refused", async () => {
    vi.mocked(client.reorderColumn).mockRejectedValueOnce(new Error("column busy"));
    const view = setup(THREE);
    await drag(view, "c", "a");
    const server = [job("a", "draft"), job("b", "draft"), job("c", "draft")];
    // reverted to server truth — the optimistic order is NOT kept
    expect(view.result.current.orderJobs("draft", server)).toBe(server);
    expect(view.spies.onToast).toHaveBeenCalledWith(
      "reorder failed: column busy — reverted",
    );
    expect(view.spies.refetch).toHaveBeenCalled();
  });

  it("skips the post entirely when the card lands back in its own slot", async () => {
    const view = setup(THREE);
    await drag(view, "b", "b");
    expect(client.reorderColumn).not.toHaveBeenCalled();
    expect(view.spies.refetch).not.toHaveBeenCalled();
  });

  it("falls back to the pessimistic path when the client's picture is stale", async () => {
    // `ghost` is in the byColumn buckets but not in board.jobs, so the
    // prediction cannot line up and must not be guessed at.
    const spies = makeSpies();
    const base = makeModel([job("a", "draft"), job("b", "draft")], spies);
    base.byColumn.draft.push(job("ghost", "draft"));
    base.byKey.set("ghost", job("ghost", "draft"));
    const view = renderHook(() =>
      useDragReorder({
        board: base,
        onOpenSteer: spies.onOpenSteer,
        onToast: spies.onToast,
      }),
    );
    await act(async () => {
      view.result.current.onDragStart(startEvent("ghost"));
    });
    await act(async () => {
      view.result.current.onDragEnd(endEvent("ghost", "a"));
    });
    expect(client.reorderColumn).toHaveBeenCalledWith("draft", ["ghost", "a", "b"]);
    // pessimistic: routed through mutate(), which refetches
    expect(spies.mutate).toHaveBeenCalledTimes(1);
    const server = [job("a", "draft"), job("b", "draft")];
    expect(view.result.current.orderJobs("draft", server)).toBe(server);
  });

  it("does not reorder a column it was not dropped in", async () => {
    const view = setup(THREE);
    await drag(view, "c", "a");
    const done = [job("x", "done")];
    expect(view.result.current.orderJobs("done", done)).toBe(done);
  });

  it("puts a job that appeared after the drop at the end rather than losing it", async () => {
    const view = setup(THREE);
    await drag(view, "c", "a");
    const withNew = [
      job("a", "draft"),
      job("b", "draft"),
      job("c", "draft"),
      job("new", "draft"),
    ];
    expect(
      view.result.current.orderJobs("draft", withNew).map((j) => j.slug),
    ).toEqual(["c", "a", "b", "new"]);
  });
});

// ------------------------------------------------------------ drag guards

describe("shouldStartDrag", () => {
  const inCard = (html: string): Element => {
    const card = document.createElement("div");
    card.className = "card";
    card.innerHTML = html;
    document.body.appendChild(card);
    return card.firstElementChild as Element;
  };

  it("allows a drag from the card body", () => {
    expect(shouldStartDrag(inCard("<span class='slug'>foo</span>"))).toBe(true);
  });

  it.each([
    ["a textarea", "<textarea></textarea>"],
    ["an input", "<input />"],
    ["a select", "<select></select>"],
    ["a contenteditable", "<div contenteditable='true'>x</div>"],
    ["anything in .doc-questions", "<div class='doc-questions'><span>q</span></div>"],
    ["anything in .job-feedback", "<div class='job-feedback'><span>f</span></div>"],
    ["an explicit opt-out", "<div data-no-drag><span>x</span></div>"],
  ])("refuses a drag starting on %s", (_name, html) => {
    const el = inCard(html);
    const target = el.firstElementChild ?? el;
    // This is the vanilla dragstart guard (app.js:957): a press-drag over a
    // form control inside a draggable ancestor drags the ANCESTOR, so
    // "select my typed text" became a real column move that posted.
    expect(shouldStartDrag(target)).toBe(false);
  });

  it("allows a drag when contenteditable is explicitly off", () => {
    expect(shouldStartDrag(inCard("<div contenteditable='false'>x</div>"))).toBe(true);
  });

  it("does not crash on a non-Element target", () => {
    expect(shouldStartDrag(null)).toBe(true);
    expect(shouldStartDrag(document)).toBe(true);
  });
});

// ------------------------------------------------------------ sensors

describe("sensors", () => {
  it("gives touch a hold delay so the board still scrolls under a finger", () => {
    // The entire point of moving off native HTML5 drag (Q2): a finger drag
    // must scroll unless the user deliberately holds.
    expect(TOUCH_ACTIVATION.delay).toBe(250);
    expect(TOUCH_ACTIVATION.tolerance).toBeGreaterThan(0);
    // and the mouse must NOT inherit that delay
    expect(MOUSE_ACTIVATION).not.toHaveProperty("delay");
    expect(MOUSE_ACTIVATION.distance).toBeGreaterThan(0);
  });

  it("registers mouse, touch and keyboard sensors", () => {
    const { result } = renderHook(() => useBoardSensors());
    const names = result.current.map((s) => s.sensor.name);
    expect(names).toEqual(
      expect.arrayContaining(["MouseSensor", "TouchSensor", "KeyboardSensor"]),
    );
    // PointerSensor would swallow touch and make the delay above unreachable
    expect(names).not.toContain("PointerSensor");
    const touch = result.current.find((s) => s.sensor === TouchSensor);
    expect(touch?.options).toMatchObject({ activationConstraint: TOUCH_ACTIVATION });
  });
});

// ------------------------------------------------------------ droppable ids

describe("column drop ids", () => {
  it("round-trips every column and the archive drawer", () => {
    for (const c of [...COLUMNS, ARCHIVED]) {
      expect(parseColumnDropId(columnDropId(c))).toBe(c);
    }
  });

  it("does not mistake a job slug for a column id", () => {
    // `draft` is a perfectly legal slug — the prefix is why it stays a card
    expect(parseColumnDropId("draft")).toBeNull();
    expect(parseColumnDropId(undefined)).toBeNull();
  });
});
