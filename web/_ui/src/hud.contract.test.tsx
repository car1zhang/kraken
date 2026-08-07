/**
 * hud.contract.test.tsx — athena-logic's BLIND logic suite for the industrial
 * HUD port (artifacts/50-contract.md), slices S2 (board minimalism) and S3
 * (color dropdown). S1 is CSS and explicitly out of blind-test scope.
 *
 * Every assertion below traces to one obligation row in 50-contract.md; the
 * `describe` titles name the row. Nothing here was derived from the
 * implementation, which was written concurrently and is unread.
 *
 * Two blindness accommodations, both required by the authoring brief:
 *
 *   1. Every package-level identifier is prefixed `contract` — athena-edge
 *      shares this module namespace at runtime. Components are reached through
 *      the `contractUI` object rather than as bare JSX names because a
 *      lowercase JSX tag is an intrinsic DOM element, while a member
 *      expression (`<contractUI.LoadChip/>`) is always a component reference.
 *
 *   2. Components are typed `ComponentType<any>`. Their props TYPES live in
 *      files under src/components/, which the blindness rule forbids reading;
 *      the contract freezes the props SHAPES in prose and those are what the
 *      calls below use. `JobCard`'s `actions` prop is the same case — its
 *      `BoardActions` type lives in BoardView.tsx, unreadable here — so it is
 *      passed as a proxy that mints a `vi.fn()` for whatever key is read.
 *
 * Assertions prefer container-level text and contract-frozen class names over
 * markup shapes the contract does not freeze.
 */

import {
  render as contractRender,
  screen as contractScreen,
  fireEvent as contractFireEvent,
} from "@testing-library/react";
import contractUserEvent from "@testing-library/user-event";
import type { ComponentType as contractComponentType } from "react";

import { makeJob as contractMakeJob } from "./test/fixtures.ts";
// The runtime palette list. api/types.ts (JobColor) states that JOB_COLORS
// lives in lib/format.ts; S3's row says the menu lists "the eight JOB_COLORS
// in order", so the order is read from the source of truth rather than pinned
// by hand here.
import { JOB_COLORS as contractJobColors } from "./lib/format.ts";

import { LoadChip as contractLoadChip } from "./components/board/chips/LoadChip.tsx";
import { DocQuestionChip as contractDocQuestionChip } from "./components/board/chips/DocQuestionChip.tsx";
import { FeedbackChip as contractFeedbackChip } from "./components/board/chips/FeedbackChip.tsx";
import { ThreadChips as contractThreadChips } from "./components/board/chips/ThreadChips.tsx";
import { WakeupChip as contractWakeupChip } from "./components/board/chips/WakeupChip.tsx";
import { PRChips as contractPRChips } from "./components/board/chips/PRChips.tsx";
import { JobCard as contractJobCard } from "./components/board/JobCard.tsx";
import { ArchivedCard as contractArchivedCard } from "./components/board/ArchivedCard.tsx";
import { SwatchStrip as contractSwatchStrip } from "./components/overlays/SwatchStrip.tsx";

const contractUI = {
  LoadChip: contractLoadChip as contractComponentType<any>,
  DocQuestionChip: contractDocQuestionChip as contractComponentType<any>,
  FeedbackChip: contractFeedbackChip as contractComponentType<any>,
  ThreadChips: contractThreadChips as contractComponentType<any>,
  WakeupChip: contractWakeupChip as contractComponentType<any>,
  PRChips: contractPRChips as contractComponentType<any>,
  JobCard: contractJobCard as contractComponentType<any>,
  ArchivedCard: contractArchivedCard as contractComponentType<any>,
  SwatchStrip: contractSwatchStrip as contractComponentType<any>,
};

/**
 * A BoardActions-shaped stub. The real type is unreadable under the blindness
 * rule (see header note 2), so every key read off it yields a stable vi.fn().
 */
function contractMakeActions(): any {
  const cache: Record<string, unknown> = {};
  return new Proxy(cache, {
    get(target, key) {
      if (typeof key !== "string") return undefined;
      if (!(key in target)) target[key] = vi.fn();
      return target[key];
    },
  });
}

// ------------------------------------------------------------------ S2 chips

describe("S2 · LoadChip — chip text is exactly `⚡ ${roi.toFixed(1)}×`", () => {
  it("renders ⚡ 4.0× for unblock 2 / scope 8", () => {
    const job = contractMakeJob({
      state: "blocked",
      live: true,
      load: { unblock: 2, scope: 8, note: "", at: 1_700_000_000_000 },
    });
    const { container } = contractRender(
      <contractUI.LoadChip job={job} col="blocked" />,
    );
    expect(container.textContent).toBe("⚡ 4.0×");
  });

  it("renders ⚡ 0.3× for unblock 10 / scope 3", () => {
    const job = contractMakeJob({
      state: "blocked",
      live: true,
      load: { unblock: 10, scope: 3, note: "", at: 1_700_000_000_000 },
    });
    const { container } = contractRender(
      <contractUI.LoadChip job={job} col="blocked" />,
    );
    expect(container.textContent).toBe("⚡ 0.3×");
  });

  it("does not put the unblock→scope pair on the card", () => {
    const job = contractMakeJob({
      state: "blocked",
      live: true,
      load: { unblock: 2, scope: 8, note: "", at: 1_700_000_000_000 },
    });
    const { container } = contractRender(
      <contractUI.LoadChip job={job} col="blocked" />,
    );
    expect(container.textContent).not.toContain("→");
    expect(container.textContent).not.toContain("2/10");
  });

  it("keeps the full sentence in title and aria-label", () => {
    const job = contractMakeJob({
      state: "blocked",
      live: true,
      load: { unblock: 2, scope: 8, note: "", at: 1_700_000_000_000 },
    });
    const { container } = contractRender(
      <contractUI.LoadChip job={job} col="blocked" />,
    );
    const titled = container.querySelector("[title]");
    expect(titled).not.toBeNull();
    expect(titled!.getAttribute("title")).toContain("unblock effort 2/10");
    expect(titled!.getAttribute("title")).toContain("next step 8/10");
    const labelled = container.querySelector("[aria-label]");
    expect(labelled).not.toBeNull();
    expect(labelled!.getAttribute("aria-label")).toContain("unblock effort 2/10");
    expect(labelled!.getAttribute("aria-label")).toContain("next step 8/10");
  });

  it("stays hidden outside the blocked column and without an estimate", () => {
    const withLoad = contractMakeJob({
      state: "working",
      live: true,
      load: { unblock: 2, scope: 8, note: "", at: 1_700_000_000_000 },
    });
    const notBlocked = contractRender(
      <contractUI.LoadChip job={withLoad} col="working" />,
    );
    expect(notBlocked.container.textContent).toBe("");

    const noLoad = contractMakeJob({ state: "blocked", live: true });
    const unestimated = contractRender(
      <contractUI.LoadChip job={noLoad} col="blocked" />,
    );
    expect(unestimated.container.textContent).toBe("");
  });
});

describe("S2 · DocQuestionChip — text is exactly `⚑ ${openCount}`", () => {
  it("renders the count with no `question(s)` word", () => {
    const job = contractMakeJob({
      state: "blocked",
      live: true,
      doc_questions: [
        { id: "q1", text: "first?", importance: "P1" },
        { id: "q2", text: "second?", importance: "P2" },
      ],
    });
    const { container } = contractRender(
      <contractUI.DocQuestionChip job={job} />,
    );
    expect(container.textContent).toBe("⚑ 2");
    expect(container.textContent).not.toMatch(/question/i);
  });

  it("keeps the P0 clause in the title", () => {
    const job = contractMakeJob({
      state: "blocked",
      live: true,
      doc_questions: [{ id: "q1", text: "blocking?", importance: "P0" }],
    });
    const { container } = contractRender(
      <contractUI.DocQuestionChip job={job} />,
    );
    const titled = container.querySelector("[title]");
    expect(titled).not.toBeNull();
    expect(titled!.getAttribute("title")).toContain("P0");
  });
});

describe("S2 · FeedbackChip — text is exactly `✎ ${count}`", () => {
  const rows = [
    { id: 1, artifact: "00-overview.md", note: "tighten this", at: 1_700_000_000_000 },
    { id: 2, artifact: "50-contract.md", note: "and this", at: 1_700_000_000_000 },
  ];

  it("renders the count with no `feedback` word and no durable-fix clause", () => {
    const job = contractMakeJob({ state: "blocked", live: true, feedback: rows });
    const { container } = contractRender(<contractUI.FeedbackChip job={job} />);
    expect(container.textContent).toBe("✎ 2");
    expect(container.textContent).not.toMatch(/feedback/i);
    expect(container.textContent).not.toMatch(/durable/i);
  });

  it("keeps the durable-fix detail in the title", () => {
    const job = contractMakeJob({ state: "blocked", live: true, feedback: rows });
    const { container } = contractRender(<contractUI.FeedbackChip job={job} />);
    const titled = container.querySelector("[title]");
    expect(titled).not.toBeNull();
    expect(titled!.getAttribute("title")).toMatch(/durable/i);
  });

  it("still renders on an archived card", () => {
    const job = contractMakeJob({
      slug: "demo",
      state: "archived",
      archived: true,
      live: false,
      feedback: [rows[0]!],
    });
    const { container } = contractRender(
      <contractUI.ArchivedCard job={job} actions={contractMakeActions()} />,
    );
    expect(container.textContent).toContain("✎ 1");
  });
});

describe("S2 · ThreadChips — compact renders one span, not buttons", () => {
  const threads = [
    { tid: "t1", title: "first", state: "live", pane: "%3", created_at: 1_700_000_000_000 },
    { tid: "t2", state: "closed", created_at: 1_700_000_000_000, closed_at: 1_700_000_100_000 },
  ];

  it("renders ONE span.thread-count.chip-threads reading `⑂ ${list.length}`", () => {
    const job = contractMakeJob({ state: "working", live: true, threads });
    const { container } = contractRender(
      <contractUI.ThreadChips
        job={job}
        onOpenThread={vi.fn()}
        withBranch={false}
        compact
      />,
    );
    const spans = container.querySelectorAll("span.thread-count.chip-threads");
    expect(spans.length).toBe(1);
    expect(spans[0]!.tagName).toBe("SPAN");
    expect(spans[0]!.textContent).toBe("⑂ 2");
    expect(container.querySelectorAll("button").length).toBe(0);
  });

  it("titles the compact chip with every thread as `tid · title`, live/closed noted", () => {
    const job = contractMakeJob({ state: "working", live: true, threads });
    const { container } = contractRender(
      <contractUI.ThreadChips
        job={job}
        onOpenThread={vi.fn()}
        withBranch={false}
        compact
      />,
    );
    const chip = container.querySelector("span.thread-count.chip-threads");
    const title = chip!.getAttribute("title") ?? "";
    expect(title).toContain("t1 · first");
    expect(title).toContain("t2");
    expect(title).toMatch(/live/);
    expect(title).toMatch(/closed/);
  });

  it("defaults compact to false — one button per thread with the modal label", () => {
    const job = contractMakeJob({ state: "working", live: true, threads });
    const { container } = contractRender(
      <contractUI.ThreadChips
        job={job}
        onOpenThread={vi.fn()}
        withBranch={false}
      />,
    );
    const buttons = Array.from(container.querySelectorAll("button"));
    expect(buttons.map((b) => b.textContent)).toEqual(["⑂ t1 · first", "⑂ t2"]);
  });
});

describe("S2 · WakeupChip — text is exactly `⏱ ${short}`", () => {
  const now = new Date("2026-08-06T12:00:00Z").getTime();

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(now));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function job(firesIn: number) {
    return contractMakeJob({
      state: "working",
      live: true,
      wakeup: { reason: "poll ci", fires_at: now + firesIn },
    });
  }

  it("renders minute precision for a far wakeup", () => {
    const { container } = contractRender(
      <contractUI.WakeupChip job={job(47 * 60_000 + 5_000)} />,
    );
    expect(container.textContent).toBe("⏱ 47m");
  });

  it("renders seconds under one minute", () => {
    const { container } = contractRender(
      <contractUI.WakeupChip job={job(12_000)} />,
    );
    expect(container.textContent).toBe("⏱ 12s");
  });

  it("renders `due` once past", () => {
    const { container } = contractRender(
      <contractUI.WakeupChip job={job(-5_000)} />,
    );
    expect(container.textContent).toBe("⏱ due");
  });

  it("keeps the long form in the title and out of the text", () => {
    const { container } = contractRender(
      <contractUI.WakeupChip job={job(4 * 60_000 + 12_000)} />,
    );
    expect(container.textContent).toBe("⏱ 4m");
    expect(container.textContent).not.toMatch(/next check in/);
    const titled = container.querySelector("[title]");
    expect(titled).not.toBeNull();
    expect(titled!.getAttribute("title")).toContain("next check in 4m 12s");
  });

  it("stays hidden with no wakeup on the job", () => {
    const { container } = contractRender(
      <contractUI.WakeupChip job={contractMakeJob({ state: "working", live: true })} />,
    );
    expect(container.textContent).toBe("");
  });
});

describe("S2 · PRChips — label is `#${n}`, state said by colour alone", () => {
  it("renders the parsed pull number and the open state class", () => {
    const { container } = contractRender(
      <contractUI.PRChips
        prs={[{ url: "https://github.com/car1zhang/kraken/pull/42", state: "open" }]}
      />,
    );
    expect(container.textContent).toBe("#42");
    expect(container.querySelector(".pr-open")).not.toBeNull();
    expect(container.textContent).not.toMatch(/open/i);
  });

  it("falls back to the bare state word when the URL does not parse", () => {
    const { container } = contractRender(
      <contractUI.PRChips prs={[{ url: "https://example.com/nope", state: "closed" }]} />,
    );
    expect(container.textContent).toBe("closed");
    expect(container.querySelector(".pr-closed")).not.toBeNull();
  });

  it("keeps the merged state class", () => {
    const { container } = contractRender(
      <contractUI.PRChips
        prs={[{ url: "https://github.com/car1zhang/kraken/pull/7", state: "merged" }]}
      />,
    );
    expect(container.textContent).toBe("#7");
    expect(container.querySelector(".pr-merged")).not.toBeNull();
  });
});

// ------------------------------------------------------------------ S2 cards

describe("S2 · JobCard band 1/3 — the .state span is gone; only settled columns get a status token", () => {
  // `col` is the board COLUMN the card renders in, per the amended row's props
  // list `{job, col, actions, matchReason?, matchTerms?}`; the queued-✕ renders
  // only in its own column.
  function card(over: Record<string, unknown>, col?: string) {
    return contractRender(
      <contractUI.JobCard
        job={contractMakeJob(over as never)}
        col={col}
        actions={contractMakeActions()}
      />,
    );
  }

  it("removes the ● working state span from .card-top", () => {
    const { container } = card({ state: "working", live: true, cell: "demo-1" });
    expect(container.querySelector(".card-top .state")).toBeNull();
    expect(container.textContent).not.toContain("● working");
  });

  // Amended band 1/3 row (post-gate: carl removed the live tokens outright):
  // working and blocked — including the health-forced blocked reading —
  // render NO status span; the settled two below keep theirs.
  it("renders NO status span for a working job", () => {
    const { container } = card({ state: "working", live: true, cell: "demo-1" });
    expect(container.querySelector(".meta span.status")).toBeNull();
  });

  it("renders NO status span for a blocked job", () => {
    const { container } = card({ state: "blocked", live: true, cell: "demo-1" });
    expect(container.querySelector(".meta span.status")).toBeNull();
  });

  it("renders NO status span when health is set on a working job", () => {
    const { container } = card({
      state: "working",
      live: true,
      cell: "demo-1",
      health: "cell gone",
    });
    expect(container.querySelector(".meta span.status")).toBeNull();
  });

  it("renders ◌ queued and keeps the ✕ cancel button in .card-top", () => {
    const { container } = card({ state: "queued" }, "queued");
    const status = container.querySelector(".meta span.status");
    expect(status).not.toBeNull();
    expect(status!.classList.contains("status-queued")).toBe(true);
    expect(status!.textContent).toBe("◌ queued");

    const top = container.querySelector(".card-top");
    expect(top).not.toBeNull();
    const cancels = Array.from(top!.querySelectorAll("button")).filter((b) =>
      (b.textContent ?? "").includes("✕"),
    );
    expect(cancels.length).toBeGreaterThan(0);
  });

  it("renders ✓ done for a done job", () => {
    const { container } = card({ state: "done" });
    const status = container.querySelector(".meta span.status");
    expect(status).not.toBeNull();
    expect(status!.classList.contains("status-done")).toBe(true);
    expect(status!.textContent).toBe("✓ done");
  });

  it("renders NO status span for a draft job", () => {
    const { container } = card({ state: "draft" });
    expect(container.querySelector("span.status")).toBeNull();
  });
});

describe("S2 · JobCard outcome — `accepted` is suppressed", () => {
  // Rendered in the done COLUMN: the amended band 1/3 row states the outcome
  // line renders only in its own column, per this row's "as today".
  function card(over: Record<string, unknown>, col?: string) {
    return contractRender(
      <contractUI.JobCard
        job={contractMakeJob(over as never)}
        col={col}
        actions={contractMakeActions()}
      />,
    );
  }

  it("suppresses the .outcome line when outcome is accepted", () => {
    const { container } = card({ state: "done", outcome: "accepted" }, "done");
    expect(container.querySelector(".outcome")).toBeNull();
    expect(container.textContent).not.toContain("accepted");
  });

  it("renders → <outcome> for any other outcome", () => {
    const { container } = card({ state: "done", outcome: "reverted" }, "done");
    const outcome = container.querySelector(".outcome");
    expect(outcome).not.toBeNull();
    expect(outcome!.textContent).toContain("→ reverted");
  });
});

describe("S2 · JobCard band 4 — the question chip replaces QuestionBlock", () => {
  const question = {
    tool_use_id: "tu-1",
    questions: [
      {
        question: "Which storage approach?",
        header: "Storage",
        multi_select: false,
        options: [
          { label: "sqlite", description: "one file" },
          { label: "json", description: "flat" },
        ],
      },
    ],
  };

  function card(over: Record<string, unknown>) {
    return contractRender(
      <contractUI.JobCard
        job={contractMakeJob(over as never)}
        actions={contractMakeActions()}
      />,
    );
  }

  it("does not render QuestionBlock's content on the card", () => {
    const { container } = card({
      state: "blocked",
      live: true,
      cell: "demo-1",
      question,
    });
    expect(container.textContent).not.toContain("Which storage approach?");
    expect(container.textContent).not.toContain("sqlite");
    expect(container.textContent).not.toContain("one file");
  });

  it("renders span.chip-question with ⚑ and the pending-question title", () => {
    const { container } = card({
      state: "blocked",
      live: true,
      cell: "demo-1",
      question,
    });
    const chip = container.querySelector("span.chip-question");
    expect(chip).not.toBeNull();
    expect(chip!.tagName).toBe("SPAN");
    expect(chip!.textContent).toBe("⚑");
    expect(chip!.getAttribute("title")).toBe(
      "pending question — open the card to answer",
    );
  });

  it("counts toward hasChips — the chip renders with no other chip present", () => {
    const { container } = card({
      state: "blocked",
      live: true,
      cell: "demo-1",
      question,
    });
    expect(container.querySelector("span.chip-question")).not.toBeNull();
  });
});

describe("S2 · JobCard band 6 — the tmux button reads `⌁ attach tmux`", () => {
  it("renders the exact button text", () => {
    contractRender(
      <contractUI.JobCard
        job={contractMakeJob({ state: "working", live: true, cell: "demo-1" })}
        actions={contractMakeActions()}
      />,
    );
    expect(contractScreen.getByText("⌁ attach tmux")).toBeInTheDocument();
  });
});

describe("S2 · ArchivedCard — the state span reads `■ archived`", () => {
  it("replaces ● archived with ■ archived", () => {
    const { container } = contractRender(
      <contractUI.ArchivedCard
        job={contractMakeJob({
          slug: "demo",
          state: "archived",
          archived: true,
          live: false,
        })}
        actions={contractMakeActions()}
      />,
    );
    const state = container.querySelector(".state");
    expect(state).not.toBeNull();
    expect(state!.textContent).toBe("■ archived");
    expect(container.textContent).not.toContain("● archived");
  });
});

// ------------------------------------------------------------ S3 SwatchStrip

describe("S3 · SwatchStrip — renders a dropdown, not radios", () => {
  it("renders button.colorsel-btn with the uppercase name and a ▾ caret", () => {
    const first = contractJobColors[0];
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value={first} onPick={vi.fn()} />,
    );
    expect(container.querySelector("input[type=radio]")).toBeNull();
    const btn = container.querySelector("button.colorsel-btn");
    expect(btn).not.toBeNull();
    expect(btn!.textContent).toContain(first.toUpperCase());
    expect(btn!.textContent).toContain("▾");
  });

  it("carries aria-haspopup=listbox and a closed aria-expanded", () => {
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value="" onPick={vi.fn()} />,
    );
    const btn = container.querySelector("button.colorsel-btn");
    expect(btn!.getAttribute("aria-haspopup")).toBe("listbox");
    expect(btn!.getAttribute("aria-expanded")).toBe("false");
    expect(contractScreen.queryByRole("listbox")).toBeNull();
  });

  it("renders NO COLOR then the eight JOB_COLORS in order when opened", async () => {
    const user = contractUserEvent.setup();
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value="" onPick={vi.fn()} />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);

    const btn = container.querySelector("button.colorsel-btn");
    expect(btn!.getAttribute("aria-expanded")).toBe("true");
    expect(container.querySelector(".colorsel-menu")).not.toBeNull();

    const menu = contractScreen.getByRole("listbox");
    const options = contractScreen.getAllByRole("option");
    expect(menu).toBeInTheDocument();
    expect(options.length).toBe(contractJobColors.length + 1);
    expect(options[0]!.textContent).toContain("NO COLOR");
    contractJobColors.forEach((c: string, i: number) => {
      expect(options[i + 1]!.textContent).toContain(c.toUpperCase());
    });
  });

  it("marks the current row .sel with a ✓ tick and aria-selected", async () => {
    const user = contractUserEvent.setup();
    const current = contractJobColors[1];
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value={current} onPick={vi.fn()} />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);

    const options = contractScreen.getAllByRole("option");
    const selected = options.filter(
      (o) => o.getAttribute("aria-selected") === "true",
    );
    expect(selected.length).toBe(1);
    expect(selected[0]!.textContent).toContain(current.toUpperCase());
    expect(selected[0]!.textContent).toContain("✓");
    expect(selected[0]!.classList.contains("sel")).toBe(true);
  });
});

describe("S3 · SwatchStrip pick behaviour", () => {
  it("calls onPick exactly once with the row's value and closes the menu", async () => {
    const user = contractUserEvent.setup();
    const onPick = vi.fn();
    const target = contractJobColors[2];
    const { container } = contractRender(
      <contractUI.SwatchStrip
        name="color"
        value={contractJobColors[0]}
        onPick={onPick}
      />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);
    await user.click(contractScreen.getAllByRole("option")[3]!);

    expect(onPick).toHaveBeenCalledTimes(1);
    expect(onPick).toHaveBeenCalledWith(target);
    expect(contractScreen.queryByRole("listbox")).toBeNull();
  });

  it("calls onPick with \"\" for the NO COLOR row", async () => {
    const user = contractUserEvent.setup();
    const onPick = vi.fn();
    const { container } = contractRender(
      <contractUI.SwatchStrip
        name="color"
        value={contractJobColors[0]}
        onPick={onPick}
      />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);
    await user.click(contractScreen.getAllByRole("option")[0]!);

    expect(onPick).toHaveBeenCalledTimes(1);
    expect(onPick).toHaveBeenCalledWith("");
    expect(contractScreen.queryByRole("listbox")).toBeNull();
  });

  it("still calls onPick when the current row is re-clicked", async () => {
    const user = contractUserEvent.setup();
    const onPick = vi.fn();
    const current = contractJobColors[0];
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value={current} onPick={onPick} />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);
    const options = contractScreen.getAllByRole("option");
    const selected = options.find(
      (o) => o.getAttribute("aria-selected") === "true",
    )!;
    await user.click(selected);

    expect(onPick).toHaveBeenCalledTimes(1);
    expect(onPick).toHaveBeenCalledWith(current);
  });

  it("closes on Escape without calling onPick", async () => {
    const user = contractUserEvent.setup();
    const onPick = vi.fn();
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value="" onPick={onPick} />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);
    const menu = contractScreen.getByRole("listbox");
    contractFireEvent.keyDown(menu, { key: "Escape", code: "Escape" });

    expect(contractScreen.queryByRole("listbox")).toBeNull();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("closes on an outside click without calling onPick", async () => {
    const user = contractUserEvent.setup();
    const onPick = vi.fn();
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value="" onPick={onPick} />,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);
    expect(contractScreen.getByRole("listbox")).toBeInTheDocument();

    await user.click(document.body);

    expect(contractScreen.queryByRole("listbox")).toBeNull();
    expect(onPick).not.toHaveBeenCalled();
  });
});

describe("S3 · SwatchStrip value fallback and event containment", () => {
  it("renders a value outside JOB_COLORS as the no-color state", () => {
    const { container } = contractRender(
      <contractUI.SwatchStrip name="color" value="chartreuse" onPick={vi.fn()} />,
    );
    const btn = container.querySelector("button.colorsel-btn");
    expect(btn).not.toBeNull();
    expect(btn!.textContent).not.toContain("CHARTREUSE");
    expect(btn!.textContent).toMatch(/—|NO COLOR/);
  });

  it("does not propagate clicks to an ancestor", async () => {
    const user = contractUserEvent.setup();
    const onAncestorClick = vi.fn();
    const { container } = contractRender(
      <div onClick={onAncestorClick}>
        <contractUI.SwatchStrip name="color" value="" onPick={vi.fn()} />
      </div>,
    );
    await user.click(container.querySelector("button.colorsel-btn")!);
    expect(onAncestorClick).not.toHaveBeenCalled();

    await user.click(contractScreen.getAllByRole("option")[1]!);
    expect(onAncestorClick).not.toHaveBeenCalled();
  });
});
