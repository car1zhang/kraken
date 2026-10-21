import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { StackLink, WebJob } from "../../../api/types.ts";
import { makeJob } from "../../../test/fixtures.ts";
import type { BoardActions } from "../BoardView.tsx";
import { JobCard } from "../JobCard.tsx";
import { StackChips, stackLabel, stackTitle } from "./StackChips.tsx";

// StackChips.tsx: one linked chip per manual-test URL the cell registered
// (52-testing-plan.md, the Chips row). Label is `⇱ <label> :<port>`, status is
// said by class, a non-http url or a remote card degrades to a span, and the
// row is band 5's second row — never inside .chips.

const link = (over: Partial<StackLink> = {}): StackLink => ({
  url: "http://127.0.0.1:7878",
  label: "board",
  status: "up",
  at: 1_769_040_000_000,
  ...over,
});

const live = (over: Partial<WebJob> = {}): WebJob =>
  makeJob({ state: "blocked", live: true, cell: "kraken-demo", ...over });

function renderRow(job: WebJob) {
  const { container } = render(<StackChips job={job} />);
  return container.querySelector<HTMLElement>(".stack");
}

describe("stackLabel / stackTitle", () => {
  it("renders label plus port, port alone, or host", () => {
    expect(stackLabel(link())).toBe("⇱ board :7878");
    expect(stackLabel(link({ url: "http://localhost:5173", label: "" }))).toBe("⇱ :5173");
    expect(stackLabel(link({ url: "https://abc.ngrok.app", label: "tunnel" }))).toBe(
      "⇱ tunnel :443",
    );
    expect(stackLabel(link({ url: "not a url", label: "" }))).toBe("⇱ not a url");
  });

  it("says what the probe last found", () => {
    expect(stackTitle(link())).toBe("http://127.0.0.1:7878 · up");
    expect(stackTitle(link({ status: "down" }))).toBe("http://127.0.0.1:7878 · :7878 not listening");
    expect(stackTitle(link({ status: "unknown" }))).toBe("http://127.0.0.1:7878 · not probed");
  });
});

describe("StackChips", () => {
  it("renders nothing for an absent or empty list", () => {
    expect(renderRow(live())).toBeNull();
    expect(renderRow(live({ stack: [] }))).toBeNull();
  });

  it("renders one anchor per link, in order, with status classes", () => {
    const row = renderRow(
      live({
        stack: [
          link(),
          link({ url: "http://localhost:5173", label: "ui", status: "down" }),
          link({ url: "https://abc.ngrok.app", label: "hook", status: "unknown" }),
        ],
      }),
    )!;
    const chips = [...row.querySelectorAll<HTMLElement>(".stack-chip")];
    expect(chips.map((c) => c.textContent)).toEqual(["⇱ board :7878", "⇱ ui :5173", "⇱ hook :443"]);
    expect(chips.map((c) => c.className)).toEqual([
      "stack-chip stack-up",
      "stack-chip stack-down",
      "stack-chip stack-unknown",
    ]);
    expect(chips.every((c) => c.tagName === "A")).toBe(true);
    expect((chips[0] as HTMLAnchorElement).href).toBe("http://127.0.0.1:7878/");
    expect(chips[0]?.getAttribute("target")).toBe("_blank");
    // a down link is dimmed by class but still a link
    expect(chips[1]?.tagName).toBe("A");
    expect(chips[1]?.title).toContain("not listening");
  });

  it("degrades a non-http url to an inert span", () => {
    const row = renderRow(live({ stack: [link({ url: "javascript:alert(1)" })] }))!;
    expect(row.querySelector("a")).toBeNull();
    expect(row.querySelector(".stack-chip")?.tagName).toBe("SPAN");
  });

  it("renders spans, never anchors, on a remote node's card", () => {
    const row = renderRow(
      live({
        stack: [link()],
        owner: { node_id: "n1", label: "mbp-michael", operator: "michael", online: true },
      }),
    )!;
    expect(row.querySelector("a")).toBeNull();
    const chip = row.querySelector<HTMLElement>(".stack-chip")!;
    expect(chip.tagName).toBe("SPAN");
    expect(chip.className).toContain("remote");
  });
});

describe("StackChips on the card", () => {
  const actions = {
    onOpenTmux: vi.fn(),
    onOpenSteer: vi.fn(),
  } as unknown as BoardActions;

  function renderCard(job: WebJob) {
    const { container } = render(
      <JobCard job={job} col="blocked" actions={actions} />,
    );
    return container.querySelector<HTMLElement>(".card")!;
  }

  it("mounts the row as a direct child under .prs, never inside .chips", () => {
    const card = renderCard(
      live({
        prs: [{ url: "https://github.com/o/r/pull/73", state: "open" }],
        stack: [link()],
      }),
    );
    const row = card.querySelector(".stack")!;
    expect(row.parentElement).toBe(card);
    const kids = [...card.children];
    expect(kids.indexOf(row)).toBe(kids.indexOf(card.querySelector(".prs")!) + 1);
    expect(card.querySelector(".chips")?.querySelector(".stack-chip") ?? null).toBeNull();
  });

  it("does not open the drilldown when a chip is clicked", () => {
    const card = renderCard(live({ stack: [link()] }));
    const chip = card.querySelector<HTMLAnchorElement>(".stack-chip")!;
    // jsdom does not navigate; the click must simply not reach the card
    fireEvent.click(chip);
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
  });
});
