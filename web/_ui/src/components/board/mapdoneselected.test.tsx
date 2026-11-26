import { fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useJobSearch } from "../../hooks/useJobSearch.ts";
import { canMarkDone, doneMany } from "../../lib/doneMany.ts";
import { bucketJobs } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { MapView } from "./MapView.tsx";

// mapdoneselected.test.tsx — the map's "✓ done N" button: shown while the
// selection holds a card that can be marked done, counting only those cards,
// and handing their slugs to onDoneMany after a confirm.

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem("kraken.mapUnsaved");
  localStorage.removeItem("kraken.mapView");
});

const makeActions = (withDoneMany = true): BoardActions => ({
  onOpenSteer: vi.fn(),
  onOpenEditor: vi.fn(),
  onOpenArtifact: vi.fn(),
  onOpenThread: vi.fn(),
  onAnswerQuestion: vi.fn(),
  onRetry: vi.fn(),
  onOpenTmux: vi.fn(),
  onSaveFeedback: vi.fn(async () => null),
  onDeleteFeedback: vi.fn(),
  onUnarchive: vi.fn(),
  onDeleteForever: vi.fn(),
  ...(withDoneMany ? { onDoneMany: vi.fn() } : {}),
});

function model(jobs: WebJob[]): BoardModel {
  const { byColumn, archived } = bucketJobs(jobs);
  return {
    board: makeBoard({ daemon: makeDaemon({ pid: 1, last_tick: 1 }), jobs }),
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

function Harness({ board, actions }: { board: BoardModel; actions: BoardActions }) {
  const search = useJobSearch(board);
  return <MapView search={search} actions={actions} />;
}

type Positions = Record<string, [number, number]>;

const remote = makeJob({
  slug: "r",
  state: "blocked",
  owner: { node_id: "n1", label: "other mac" } as WebJob["owner"],
});
const jobs = () => [
  makeJob({ slug: "w", state: "working", live: true }),
  makeJob({ slug: "b", state: "blocked", live: true }),
  makeJob({ slug: "dr", state: "draft" }),
  makeJob({ slug: "dn", state: "done" }),
  remote,
];
const saved: Positions = { w: [0, 0], b: [3000, 0], dr: [0, 800], dn: [3000, 800], [jobKey(remote)]: [6000, 0] };

async function mount(actions = makeActions()) {
  localStorage.setItem("kraken.mapView", JSON.stringify({ x: 0, y: 0, z: 1 }));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    if (String(input).includes("/api/map/positions") && init?.method !== "POST") {
      return new Response(JSON.stringify({ positions: saved }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
  const out = render(<Harness board={model(jobs())} actions={actions} />);
  await waitFor(() => expect(card(out.container, "dn").style.top).toBe("800px"));
  return { ...out, actions };
}

const card = (c: HTMLElement, key: string) => c.querySelector<HTMLElement>(`.map-card[data-key="${key}"]`)!;
const btn = (c: HTMLElement) => c.querySelector<HTMLButtonElement>("#map-done-selected");
const selected = (c: HTMLElement) =>
  [...c.querySelectorAll<HTMLElement>(".map-card.selected")].map((el) => el.dataset.key).sort();

/** A Shift click on a card's face, which toggles it in the selection. */
function shiftClick(c: HTMLElement, key: string) {
  const el = card(c, key).querySelector<HTMLElement>(".card")!;
  fireEvent.mouseDown(el, { clientX: 100, clientY: 100, button: 0, buttons: 1, shiftKey: true });
  fireEvent.mouseUp(el, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
  fireEvent.click(el, { clientX: 100, clientY: 100, button: 0, shiftKey: true });
}

describe("the map's done-selected button", () => {
  it("is absent with nothing selected, and with only cards that cannot be marked done", async () => {
    const { container } = await mount();
    expect(btn(container)).toBeNull();
    shiftClick(container, "dr");
    shiftClick(container, "dn");
    shiftClick(container, jobKey(remote));
    expect(selected(container)).toEqual(["dn", "dr", jobKey(remote)].sort());
    expect(btn(container)).toBeNull();
  });

  it("counts the working and blocked local cards, and sends exactly those once confirmed", async () => {
    const { container, actions } = await mount();
    for (const k of ["w", "b", "dr", "dn", jobKey(remote)]) shiftClick(container, k);
    expect(btn(container)!.textContent?.trim()).toBe("✓ done 2");

    // the Mac app's web view has no confirm panel: the button must never ask that way
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    // the first click only arms it, naming what the second would send
    fireEvent.click(btn(container)!);
    expect(actions.onDoneMany).not.toHaveBeenCalled();
    expect(btn(container)!.textContent?.trim()).toBe("really done 2?");
    expect(btn(container)!.getAttribute("data-armed")).toBe("true");
    expect(btn(container)!.title).toContain("w, b");
    expect(selected(container)).toHaveLength(5);

    fireEvent.click(btn(container)!);
    expect(confirm).not.toHaveBeenCalled();
    expect(actions.onDoneMany).toHaveBeenCalledTimes(1);
    expect(actions.onDoneMany).toHaveBeenCalledWith(["w", "b"]);
    // the selection is spent, and the button goes with it
    expect(selected(container)).toEqual([]);
    expect(btn(container)).toBeNull();
  });

  it("disarms on a blur, and when the cards it was armed for change", async () => {
    const { container, actions } = await mount();
    shiftClick(container, "b");
    fireEvent.click(btn(container)!);
    expect(btn(container)!.textContent?.trim()).toBe("really done 1?");
    fireEvent.blur(btn(container)!);
    expect(btn(container)!.textContent?.trim()).toBe("✓ done 1");
    expect(btn(container)!.getAttribute("data-armed")).toBeNull();

    fireEvent.click(btn(container)!);
    expect(btn(container)!.textContent?.trim()).toBe("really done 1?");
    shiftClick(container, "w");
    // armed for b alone: with w added, the next click arms again and sends nothing
    expect(btn(container)!.textContent?.trim()).toBe("✓ done 2");
    fireEvent.click(btn(container)!);
    expect(btn(container)!.textContent?.trim()).toBe("really done 2?");
    expect(actions.onDoneMany).not.toHaveBeenCalled();
    expect(selected(container)).toEqual(["b", "w"]);
  });

  it("stays disarmed when the list it was armed for goes away and comes back", async () => {
    const { container, actions } = await mount();
    // by adding a card and taking it off again
    shiftClick(container, "b");
    fireEvent.click(btn(container)!);
    shiftClick(container, "w");
    shiftClick(container, "w");
    expect(btn(container)!.textContent?.trim()).toBe("✓ done 1");
    expect(btn(container)!.getAttribute("data-armed")).toBeNull();

    // and by emptying the selection (Escape) and selecting the same cards again
    fireEvent.click(btn(container)!);
    expect(btn(container)!.getAttribute("data-armed")).toBe("true");
    fireEvent.keyDown(container.querySelector("#agent-map")!, { key: "Escape", code: "Escape" });
    expect(btn(container)).toBeNull();
    shiftClick(container, "b");
    expect(btn(container)!.textContent?.trim()).toBe("✓ done 1");
    expect(btn(container)!.getAttribute("data-armed")).toBeNull();
    expect(actions.onDoneMany).not.toHaveBeenCalled();
  });

  it("does not take the second click of a double-click as the confirmation", async () => {
    const { container, actions } = await mount();
    shiftClick(container, "b");
    fireEvent.click(btn(container)!, { detail: 1 });
    fireEvent.click(btn(container)!, { detail: 2 });
    expect(actions.onDoneMany).not.toHaveBeenCalled();
    expect(btn(container)!.getAttribute("data-armed")).toBe("true");
    fireEvent.click(btn(container)!, { detail: 1 });
    expect(actions.onDoneMany).toHaveBeenCalledWith(["b"]);
  });

  it("takes clicks again after a host whose onDoneMany throws", async () => {
    const actions = makeActions();
    (actions.onDoneMany as ReturnType<typeof vi.fn>).mockImplementation(() => {
      throw new Error("boom");
    });
    const { container } = await mount(actions);
    shiftClick(container, "w");
    fireEvent.click(btn(container)!);
    fireEvent.click(btn(container)!);
    await waitFor(() => expect(actions.onDoneMany).toHaveBeenCalledTimes(1));
    shiftClick(container, "w");
    await waitFor(() => expect(btn(container)!.disabled).toBe(false));
  });

  it("is disabled while a batch is in flight, and takes clicks again once it settles", async () => {
    const actions = makeActions();
    let settle!: () => void;
    (actions.onDoneMany as ReturnType<typeof vi.fn>).mockImplementation(
      () => new Promise<void>((r) => (settle = r)),
    );
    const { container } = await mount(actions);
    shiftClick(container, "w");
    fireEvent.click(btn(container)!);
    fireEvent.click(btn(container)!);
    expect(actions.onDoneMany).toHaveBeenCalledTimes(1);

    // the board has not redrawn w as done yet, so it can be selected again
    shiftClick(container, "w");
    expect(btn(container)!.disabled).toBe(true);
    fireEvent.click(btn(container)!);
    expect(btn(container)!.getAttribute("data-armed")).toBeNull();
    expect(actions.onDoneMany).toHaveBeenCalledTimes(1);

    settle();
    await waitFor(() => expect(btn(container)!.disabled).toBe(false));
  });

  it("follows the selection: deselecting the last such card removes it", async () => {
    const { container } = await mount();
    shiftClick(container, "w");
    expect(btn(container)).not.toBeNull();
    shiftClick(container, "w");
    expect(btn(container)).toBeNull();
  });

  it("is not offered by a host that wired no onDoneMany", async () => {
    const { container } = await mount(makeActions(false));
    shiftClick(container, "w");
    expect(selected(container)).toEqual(["w"]);
    expect(btn(container)).toBeNull();
  });
});

describe("canMarkDone", () => {
  it("takes a started, unfinished job of this machine and nothing else", () => {
    expect(canMarkDone(makeJob({ slug: "a", state: "working" }))).toBe(true);
    expect(canMarkDone(makeJob({ slug: "a", state: "blocked" }))).toBe(true);
    expect(canMarkDone(makeJob({ slug: "a", state: "blocked", frozen_at: 5 }))).toBe(true);
    expect(canMarkDone(makeJob({ slug: "a", state: "draft" }))).toBe(false);
    expect(canMarkDone(makeJob({ slug: "a", state: "done" }))).toBe(false);
    expect(canMarkDone(makeJob({ slug: "a", state: "blocked", archived: true }))).toBe(false);
    expect(canMarkDone(remote)).toBe(false);
  });
});

describe("doneMany", () => {
  it("runs every slug in order, one at a time", async () => {
    const log: string[] = [];
    await doneMany(["a", "b", "c"], async (slug) => {
      log.push(`start ${slug}`);
      await Promise.resolve();
      log.push(`end ${slug}`);
    });
    expect(log).toEqual(["start a", "end a", "start b", "end b", "start c", "end c"]);
  });

  it("carries on past a refusal and throws once, naming each job that refused", async () => {
    const ran: string[] = [];
    const run = async (slug: string) => {
      ran.push(slug);
      if (slug !== "b") throw new Error(`${slug} is already done`);
    };
    await expect(doneMany(["a", "b", "c"], run)).rejects.toThrow(
      "done failed for 2 of 3: a: a is already done; c: c is already done",
    );
    expect(ran).toEqual(["a", "b", "c"]);
  });

  it("resolves for an empty list without running anything", async () => {
    const run = vi.fn(async () => {});
    await doneMany([], run);
    expect(run).not.toHaveBeenCalled();
  });
});
