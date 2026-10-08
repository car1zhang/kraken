import { act, render, renderHook } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import { useHashRoute } from "../../hooks/useHashRoute.ts";
import type { Column } from "../../lib/columns.ts";
import { makeJob } from "../../test/fixtures.ts";
import { JobCard } from "../board/JobCard.tsx";
import MemoryView from "./MemoryView.tsx";

// graphremoval.edge.test.tsx — the EDGE dimension of 50-contract.md slice C
// (web SPA after the knowledge-graph removal). Written blind from the
// contract: MemoryView.tsx, LibraryPane.tsx, MentalModelPane.tsx,
// useHashRoute.ts and StageChip.tsx were not read. Happy paths are
// athena-logic's.
//
// The two panes are replaced with stubs that forward a truthy `hidden` prop
// onto their root (so host-controlled visibility reaches the DOM). The contract fixes WHICH pane each
// segment renders but not the shapes of the `library`/`mm` props, so the stubs
// keep this file independent of those shapes (disclosed as a silence); each
// stub is exported under both a default and its component name because the
// contract does not say how either module exports it.

vi.mock("./LibraryPane.tsx", async () => {
  const { createElement } = await import("react");
  const Stub = (props: { hidden?: boolean }) =>
    createElement("div", { "data-testid": "edge-library-pane", hidden: props.hidden ? true : undefined }, "library");
  return { default: Stub, LibraryPane: Stub };
});
vi.mock("./MentalModelPane.tsx", async () => {
  const { createElement } = await import("react");
  const Stub = (props: { hidden?: boolean }) =>
    createElement("div", { "data-testid": "edge-mm-pane", hidden: props.hidden ? true : undefined }, "model");
  return { default: Stub, MentalModelPane: Stub };
});

type EdgeMemoryProps = ComponentProps<typeof MemoryView>;

// Empty library and empty mental model — the smallest data the view can be
// handed. Cast because the prop shapes are not in the contract.
const edgeEmptyProps = {
  active: true,
  library: { items: [], entries: [], loading: false, error: null },
  mm: { items: [], loading: false, error: null },
} as unknown as EdgeMemoryProps;

function edgeShown(el: Element | null): boolean {
  if (el === null) return false;
  for (let n: Element | null = el; n; n = n.parentElement) {
    if (n.hasAttribute("hidden")) return false;
    if ((n as HTMLElement).style?.display === "none") return false;
  }
  return true;
}

function edgeSegments(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(".memory-modes .memory-mode")];
}

function edgeSegment(container: HTMLElement, label: string): HTMLElement {
  const seg = edgeSegments(container).find((s) => s.textContent?.trim() === label);
  if (!seg) throw new Error(`no .memory-mode segment labelled ${label}`);
  return seg;
}

const edgeJob = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over } as Partial<WebJob>);

function edgeStageChip(stage: string): Element {
  const j = edgeJob({ state: "working", live: true, stage } as Partial<WebJob>);
  const actions = {
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
  } as unknown as ComponentProps<typeof JobCard>["actions"];
  const { container } = render(<JobCard job={j} col={"working" as Column} actions={actions} />);
  const st = container.querySelector(".chips .st-chip");
  if (!st) throw new Error(`no stage chip rendered for ${stage}`);
  return st;
}

describe("graphremoval edge", () => {
  beforeEach(() => {
    localStorage.clear();
    location.hash = "";
  });

  // "Renders a two-segment switch, `Library` and `Model`, using the existing
  // `.graph-modes`/`.graph-mode` CSS classes (renamed to
  // `.memory-modes`/`.memory-mode` …)" — with empty data, exactly two segments
  // under the renamed classes, and no element left on the old class names.
  it("renders exactly two renamed segments over empty data, none on the old graph classes", () => {
    const { container } = render(<MemoryView {...edgeEmptyProps} />);
    const segs = edgeSegments(container);
    expect(segs.map((s) => s.textContent?.trim())).toEqual(["Library", "Model"]);
    expect(container.querySelectorAll(".memory-modes")).toHaveLength(1);
    expect(container.querySelector(".graph-modes")).toBeNull();
    expect(container.querySelector(".graph-mode")).toBeNull();
  });

  // "Default segment: `Library`." + "the Model segment renders
  // `MentalModelPane`". Edge: re-clicking the active segment is idempotent,
  // and the switch round-trips back to Library.
  it("defaults to Library, re-clicking is idempotent, and the switch round-trips", async () => {
    const user = userEvent.setup();
    const { container, queryByTestId } = render(<MemoryView {...edgeEmptyProps} />);
    expect(edgeShown(queryByTestId("edge-library-pane"))).toBe(true);
    expect(edgeShown(queryByTestId("edge-mm-pane"))).toBe(false);

    await user.click(edgeSegment(container, "Library"));
    expect(edgeShown(queryByTestId("edge-library-pane"))).toBe(true);
    expect(edgeShown(queryByTestId("edge-mm-pane"))).toBe(false);

    await user.click(edgeSegment(container, "Model"));
    await user.click(edgeSegment(container, "Model"));
    expect(edgeShown(queryByTestId("edge-mm-pane"))).toBe(true);
    expect(edgeShown(queryByTestId("edge-library-pane"))).toBe(false);

    await user.click(edgeSegment(container, "Library"));
    expect(edgeShown(queryByTestId("edge-library-pane"))).toBe(true);
    expect(edgeShown(queryByTestId("edge-mm-pane"))).toBe(false);
    // still exactly two segments after the switching
    expect(edgeSegments(container)).toHaveLength(2);
  });

  // "`useHashRoute` unchanged — `#/graph` still resolves to the `memory` view
  // (the `RENAMED` alias stays)." Edge: reached by a hashchange (a bookmark or
  // the back button) from another view, not only on mount.
  it("resolves a hashchange to #/graph onto the memory view", () => {
    const { result } = renderHook(() => useHashRoute());
    act(() => result.current[1]("dream"));
    expect(result.current[0]).toBe("dream");
    act(() => {
      location.hash = "#/graph";
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(result.current[0]).toBe("memory");
  });

  // "`StageChip` — No `graph` entry; an unknown stage renders as it does today
  // for unknown values." A job reporting the retired `graph` stage renders
  // exactly like any other unknown stage: bare name, base class, no glyph.
  it("renders a stale graph stage exactly like an unknown stage", () => {
    const graph = edgeStageChip("graph");
    const unknown = edgeStageChip("shipit");
    expect(graph.textContent).toBe("graph");
    expect(graph.className).toBe(unknown.className);
    expect(graph.className).toBe("st-chip");
    expect(graph.querySelector('[aria-hidden="true"]')).toBeNull();
  });
});
