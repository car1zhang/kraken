import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { contextColorClass } from "../../lib/format.ts";
import { makeContext, makeJob } from "../../test/fixtures.ts";
import { ContextPanel } from "./ContextPanel.tsx";

// Lead-authored (direct job 630, no blind authorship): the context-window
// meter's obligations from 50-components.md — presence, colour bands against
// the SERVED threshold, the exact-vs-derived count, the handoff row, and the
// live-vs-dead provenance wording.

describe("contextColorClass", () => {
  it("bands on the served handoff threshold, never a hardcoded 75", () => {
    expect(contextColorClass(0, 75)).toBe("u-ok");
    expect(contextColorClass(49, 75)).toBe("u-ok");
    expect(contextColorClass(50, 75)).toBe("u-warn");
    expect(contextColorClass(74, 75)).toBe("u-warn");
    expect(contextColorClass(75, 75)).toBe("u-danger");
    expect(contextColorClass(100, 75)).toBe("u-danger");
    expect(contextColorClass(59, 60)).toBe("u-warn");
    expect(contextColorClass(60, 60)).toBe("u-danger");
  });
});

describe("ContextPanel", () => {
  it("renders nothing for a job with no context", () => {
    const { container } = render(<ContextPanel job={makeJob()} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders the headline, the bar fill and the exact count", () => {
    render(<ContextPanel job={makeJob({ live: true, context: makeContext() })} />);
    const panel = screen.getByTestId("context-panel");
    expect(panel.querySelector(".cw-big")?.textContent).toBe("25%");
    const fill = panel.querySelector(".u-bar-fill") as HTMLElement;
    expect(fill.style.width).toBe("25%");
    expect(fill.className).toContain("u-ok");
    expect(panel.querySelector(".u-row-val")?.textContent).toBe("242K / 1M");
    expect(panel.className).toContain("cw-ok");
    expect(panel.className).not.toContain("cw-dead");
  });

  it("colours warn from 50 and danger from the served threshold", () => {
    const { unmount } = render(<ContextPanel job={makeJob({ context: makeContext({ pct: 68 }) })} />);
    expect(screen.getByTestId("context-panel").querySelector(".u-bar-fill")?.className).toContain("u-warn");
    unmount();
    render(<ContextPanel job={makeJob({ context: makeContext({ pct: 81 }) })} />);
    const panel = screen.getByTestId("context-panel");
    expect(panel.querySelector(".u-bar-fill")?.className).toContain("u-danger");
    expect(panel.className).toContain("cw-danger");
  });

  it("moves the danger line with handoff_pct", () => {
    render(<ContextPanel job={makeJob({ context: makeContext({ pct: 62, handoff_pct: 60 }) })} />);
    expect(screen.getByTestId("context-panel").querySelector(".u-bar-fill")?.className).toContain("u-danger");
  });

  it("places the handoff tick at the served threshold", () => {
    render(<ContextPanel job={makeJob({ context: makeContext({ handoff_pct: 75 }) })} />);
    const tick = screen.getByTestId("handoff-tick") as HTMLElement;
    expect(tick.style.left).toBe("75%");
    expect(tick.title).toContain("75%");
  });

  it("derives and marks the count when the hook sent none", () => {
    render(<ContextPanel job={makeJob({ context: makeContext({ pct: 25, used: 0 }) })} />);
    expect(screen.getByTestId("context-panel").querySelector(".u-row-val")?.textContent).toBe("~250K / 1M");
  });

  it("shows the percentage alone when there is no window to divide by", () => {
    render(<ContextPanel job={makeJob({ context: makeContext({ pct: 25, window: 0, used: 0 }) })} />);
    const val = screen.getByTestId("context-panel").querySelector(".u-row-val")?.textContent ?? "";
    expect(val).toBe("25%");
    expect(val).not.toContain("/");
  });

  it("renders the handoff row only once the pass has fired", () => {
    const { unmount } = render(
      <ContextPanel job={makeJob({ context: makeContext({ handoff: { fires: 0, last_at: 0, exhausted: false } }) })} />,
    );
    expect(screen.queryByTestId("handoff-row")).toBeNull();
    unmount();
    render(
      <ContextPanel
        job={makeJob({
          context: makeContext({
            pct: 81,
            handoff: { fires: 2, last_at: Date.now() - 14 * 60_000, exhausted: false },
          }),
        })}
      />,
    );
    const row = screen.getByTestId("handoff-row");
    expect(row.textContent).toContain("2×");
    expect(row.textContent).toContain("current streak");
    expect(row.textContent).toContain("ago");
    expect(row.textContent).not.toContain("gave up");
  });

  it("says gave up when the streak is exhausted", () => {
    render(
      <ContextPanel
        job={makeJob({
          context: makeContext({ pct: 90, handoff: { fires: 3, last_at: Date.now() - 60_000, exhausted: true } }),
        })}
      />,
    );
    expect(screen.getByTestId("handoff-row").textContent).toContain("3×");
    expect(screen.getByTestId("handoff-row").textContent).toContain("gave up");
  });

  it("words a live job as the last render and a dead one as the last report", () => {
    const { unmount } = render(
      <ContextPanel job={makeJob({ live: true, context: makeContext({ at: Date.now() - 4_000 }) })} />,
    );
    let panel = screen.getByTestId("context-panel");
    expect(panel.querySelector(".cw-note")?.textContent).toContain("last render");
    expect(panel.textContent).toContain("reported");
    expect(panel.className).not.toContain("cw-dead");
    unmount();
    render(
      <ContextPanel job={makeJob({ live: false, state: "done", context: makeContext({ at: Date.now() - 3 * 3600_000 }) })} />,
    );
    panel = screen.getByTestId("context-panel");
    expect(panel.querySelector(".cw-note")?.textContent).toContain("before the cell went down");
    expect(panel.textContent).toContain("last report");
    expect(panel.className).toContain("cw-dead");
  });

  it("never renders 0s ago for a zero report time", () => {
    render(<ContextPanel job={makeJob({ live: true, context: makeContext({ at: 0 }) })} />);
    expect(screen.getByTestId("context-panel").textContent).not.toContain("ago");
  });
});
