import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "../../api/types.ts";
import { dagLayout } from "../../lib/dagLayers.ts";
import { DagView } from "./DagView.tsx";
import { makeDaemon } from "../../test/fixtures.ts";

function job(slug: string, extra: Partial<WebJob> = {}): WebJob {
  return { slug, repo: "kraken", state: "draft", live: false, mtime: 0, ...extra };
}

const board = (jobs: WebJob[]): WebBoard => ({
  daemon: makeDaemon(),
  daemon_ok: true,
  cell_cap: 3,
  live_cells: 0,
  jobs,
});

describe("<DagView>", () => {
  it("renders the empty state when there is nothing to draw", () => {
    const { container } = render(<DagView board={board([])} active />);
    expect(container.querySelector(".dag-empty")).not.toBeNull();
    expect(container.querySelectorAll(".dag-node")).toHaveLength(0);
  });

  it("marks a GATED edge with ⚑ and dashes the path", () => {
    const jobs = [
      job("a"),
      job("b", { prereqs: [{ job: "a", gated: true, done: false }] }),
    ];
    const { container } = render(<DagView board={board(jobs)} active />);
    expect(container.querySelector("#dag-edges path.gated")).not.toBeNull();
    expect(container.querySelector("#dag-edges text")?.textContent).toBe("⚑");
  });

  it("does not flag an ungated edge", () => {
    const jobs = [
      job("a"),
      job("b", { prereqs: [{ job: "a", gated: false, done: true }] }),
    ];
    const { container } = render(<DagView board={board(jobs)} active />);
    expect(container.querySelector("#dag-edges text")).toBeNull();
    expect(container.querySelector("#dag-edges path.done")).not.toBeNull();
  });

  it("puts the state color on the --state custom property the card CSS reads", () => {
    const { container } = render(
      <DagView board={board([job("a", { state: "working" })])} active />,
    );
    const node = container.querySelector<HTMLElement>(".dag-node");
    expect(node?.style.getPropertyValue("--state")).toBe("var(--solved)");
  });

  it("flags a node carrying a health condition", () => {
    const { container } = render(
      <DagView board={board([job("a", { health: "cell is gone" })])} active />,
    );
    expect(container.querySelector(".dag-node.flagged")).not.toBeNull();
  });

  it("keeps archived jobs out of the graph entirely", () => {
    const jobs = [job("a"), job("a", { archived: true })];
    const { container } = render(<DagView board={board(jobs)} active />);
    expect(container.querySelectorAll(".dag-node")).toHaveLength(1);
  });

  it("hides itself when it is not the active view", () => {
    const { container } = render(<DagView board={board([job("a")])} active={false} />);
    expect(container.querySelector("main.dag")?.hasAttribute("hidden")).toBe(true);
  });

  it("sizes the canvas, the SVG and the node layer to the graph", () => {
    // A regression here silently CLIPS the graph, which is the failure the
    // whole layout module exists to prevent — and the CSS cannot recover it
    // (`.dag-canvas` only has a min-height).
    const jobs = [job("a"), job("b", { prereqs: [{ job: "a", gated: false, done: false }] })];
    const { container } = render(<DagView board={board(jobs)} active />);
    const { width, height } = dagLayout(jobs);

    const canvas = container.querySelector<HTMLElement>("#dag-canvas")!;
    expect(canvas.style.width).toBe(`${width}px`);
    expect(canvas.style.height).toBe(`${height}px`);

    const svg = container.querySelector("#dag-edges")!;
    expect(svg.getAttribute("viewBox")).toBe(`0 0 ${width} ${height}`);
    expect(svg.getAttribute("width")).toBe(String(width));
    expect(svg.getAttribute("height")).toBe(String(height));

    expect(container.querySelector<HTMLElement>("#dag-nodes")!.style.height).toBe(
      `${height}px`,
    );
  });

  it("keeps the wrapper skeleton identical when empty", () => {
    // one skeleton, two states: same nesting, no sizing
    const { container } = render(<DagView board={board([])} active />);
    expect(
      container.querySelector("main.dag > .dag-canvas > svg#dag-edges"),
    ).not.toBeNull();
    expect(container.querySelector(".dag-canvas > #dag-nodes > .dag-empty")).not.toBeNull();
    expect(container.querySelector<HTMLElement>("#dag-canvas")!.style.width).toBe("");
  });

  it("marks a job editable only when it is not live and sits in an editable column", () => {
    const jobs = [
      job("editable-one", { state: "draft", live: false }),
      job("live-one", { state: "draft", live: true }),
      job("working-one", { state: "working", live: false }),
    ];
    const { container } = render(<DagView board={board(jobs)} active />);
    const at = (slug: string) =>
      container
        .querySelector(`.dag-node[data-slug="${slug}"]`)
        ?.getAttribute("data-editable");
    expect(at("editable-one")).toBe("true");
    expect(at("live-one")).toBe("false");
    expect(at("working-one")).toBe("false");
  });

  it("renders BOTH edges when a job file names the same prereq twice", () => {
    const jobs = [
      job("a"),
      job("b", {
        prereqs: [
          { job: "a", gated: false, done: false },
          { job: "a", gated: true, done: false },
        ],
      }),
    ];
    const warn = vi.spyOn(console, "error").mockImplementation(() => {});
    const { container } = render(<DagView board={board(jobs)} active />);

    expect(container.querySelectorAll("#dag-edges path")).toHaveLength(2);
    // the gated twin's marker survives — this is what a duplicate key lost
    expect(container.querySelector("#dag-edges text")?.textContent).toBe("⚑");
    // and React never complained about a duplicate key
    expect(
      warn.mock.calls.some((c) => String(c[0]).includes("same key")),
    ).toBe(false);
    warn.mockRestore();
  });

  it("swallows a click that is really a text selection", async () => {
    const onNodeClick = vi.fn();
    const selection = vi
      .spyOn(window, "getSelection")
      .mockReturnValue({ toString: () => "a-slug" } as unknown as Selection);
    const { container } = render(
      <DagView board={board([job("a")])} active onNodeClick={onNodeClick} />,
    );

    await userEvent.click(container.querySelector(".dag-node")!);
    expect(onNodeClick).not.toHaveBeenCalled();
    selection.mockRestore();
  });

  it("hands a clicked node back to the integrator", async () => {
    const onNodeClick = vi.fn();
    const { container } = render(
      <DagView board={board([job("a")])} active onNodeClick={onNodeClick} />,
    );
    await userEvent.click(container.querySelector(".dag-node")!);
    expect(onNodeClick).toHaveBeenCalledTimes(1);
    expect(onNodeClick.mock.calls[0]?.[0]).toMatchObject({ slug: "a" });
  });
});
