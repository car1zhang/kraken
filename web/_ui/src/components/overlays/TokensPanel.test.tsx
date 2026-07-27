import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { WebJob } from "../../api/types.ts";
import { fmtTokens } from "../../lib/format.ts";
import { makeJob } from "../../test/fixtures.ts";
import { TokensPanel } from "./TokensPanel.tsx";

const withTokens = (over: Partial<NonNullable<WebJob["tokens"]>> = {}): WebJob =>
  makeJob({
    tokens: {
      in: 0,
      out: 0,
      cache_write: 0,
      cache_read: 0,
      sessions: 0,
      updated_at: 0,
      ...over,
    },
  });

describe("fmtTokens", () => {
  it("shrinks precision as magnitude grows", () => {
    expect(fmtTokens(15_756_009)).toBe("15.8M");
    expect(fmtTokens(1_234)).toBe("1.23K");
    expect(fmtTokens(123_456)).toBe("123K");
    expect(fmtTokens(2_500_000_000)).toBe("2.5B");
  });

  it("promotes rather than rendering a value wider than its unit", () => {
    // 999_999 rounds to "1000K", which is both wrong-looking and wider than
    // the unit exists to keep it
    expect(fmtTokens(999_999)).toBe("1M");
  });

  it("trims only inside a decimal part", () => {
    // a blanket /0+$/ would turn "100" into "1"
    expect(fmtTokens(100_000)).toBe("100K");
    expect(fmtTokens(1_500)).toBe("1.5K");
  });

  it("passes small values through and floors at zero", () => {
    expect(fmtTokens(42)).toBe("42");
    expect(fmtTokens(0)).toBe("0");
    expect(fmtTokens(-5)).toBe("0");
    expect(fmtTokens(undefined)).toBe("0");
  });
});

describe("<TokensPanel>", () => {
  it("renders NOTHING when tokens is absent — never measured is not a zero", () => {
    // the distinction the whole feature turns on: absent means the daemon has
    // not reached this job, and a "0 tokens" panel would claim it was free
    const { container } = render(<TokensPanel job={makeJob()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing for an all-zero row with no sessions", () => {
    const { container } = render(<TokensPanel job={withTokens()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders a measured zero when sessions were counted", () => {
    // measured-and-cheap IS worth showing — it is what distinguishes a cheap
    // job from one that was never scanned
    const { container } = render(<TokensPanel job={withTokens({ sessions: 3 })} />);
    expect(container.querySelector(".tok")).not.toBeNull();
    expect(screen.getByText("measured across 3 transcripts")).toBeInTheDocument();
  });

  it("headlines the sum and the output, and breaks out all four raw counters", () => {
    const { container } = render(
      <TokensPanel
        job={withTokens({
          in: 1_000_000,
          out: 500_000,
          cache_write: 2_000_000,
          cache_read: 4_000_000,
          sessions: 12,
        })}
      />,
    );
    // the head is the total alone now — `out` moved into the breakdown
    expect(container.querySelector(".tok-big")?.textContent).toBe("7.5M");
    expect(container.querySelector(".tok-mid")).toBeNull();
    const cells = [...container.querySelectorAll(".tok-break:not(.tok-origin) .tok-cell")].map(
      (c) => c.textContent,
    );
    expect(cells).toEqual(["in1M", "out500K", "cache w2M", "cache r4M"]);
  });

  it("singularises a one-transcript measurement", () => {
    render(<TokensPanel job={withTokens({ sessions: 1, out: 10 })} />);
    expect(screen.getByText("measured across 1 transcript")).toBeInTheDocument();
  });

  it("drops the age clause for a missing stamp rather than saying '0s ago'", () => {
    const { container } = render(
      <TokensPanel job={withTokens({ out: 10, sessions: 1, updated_at: 0 })} />,
    );
    expect(container.querySelector(".tok-note")?.textContent).toBe(
      "measured across 1 transcript",
    );
  });

  it("treats updated_at as MILLIS, not seconds", () => {
    // age() takes seconds; feeding it millis would render a nonsense age
    const { container } = render(
      <TokensPanel
        job={withTokens({ out: 10, sessions: 1, updated_at: Date.now() - 120_000 })}
      />,
    );
    expect(container.querySelector(".tok-note")?.textContent).toMatch(/2m ago$/);
  });
});

describe("<TokensPanel> origin row (zeus vs subagents)", () => {
  const originOf = (c: HTMLElement) =>
    [...c.querySelectorAll(".tok-origin .tok-cell")].map((e) => e.textContent);

  it("derives the subagent half as the remainder, never as its own key", () => {
    // zeus is a SUBSET of the totals: in already includes zeus_in. Summing
    // them would double-count the lead.
    const { container } = render(
      <TokensPanel
        job={withTokens({
          in: 1_000_000,
          out: 1_000_000,
          cache_write: 0,
          cache_read: 0,
          sessions: 4,
          zeus_in: 250_000,
          zeus_out: 250_000,
          zeus_cache_write: 0,
          zeus_cache_read: 0,
        })}
      />,
    );
    // total 2M, zeus 500K, subagents = remainder = 1.5M
    expect(originOf(container)).toEqual(["zeus500K", "subagents1.5M"]);
  });

  it("drops the row entirely on a server predating the zeus keys", () => {
    // rendering it would claim 100% subagent, which is a lie rather than a gap
    const { container } = render(
      <TokensPanel job={withTokens({ in: 10, sessions: 1 })} />,
    );
    expect(container.querySelector(".tok-origin")).toBeNull();
  });

  it("shows the row when the lead genuinely burned nothing", () => {
    // presence is tested on ONE key, not on the sum — an all-zero zeus half is
    // a real state (every token spent by a teammate) that a sum test would hide
    const { container } = render(
      <TokensPanel
        job={withTokens({
          in: 1_000_000,
          sessions: 2,
          zeus_in: 0,
          zeus_out: 0,
          zeus_cache_write: 0,
          zeus_cache_read: 0,
        })}
      />,
    );
    expect(originOf(container)).toEqual(["zeus0", "subagents1M"]);
  });

  it("clamps rather than showing a negative subagent half", () => {
    // a stale zeus row paired with a fresh total mid-rescan is the one window
    // where a subset could momentarily exceed its total
    const { container } = render(
      <TokensPanel
        job={withTokens({ in: 100, sessions: 1, zeus_in: 500 })} />,
    );
    expect(originOf(container)).toEqual(["zeus100", "subagents0"]);
  });
});
