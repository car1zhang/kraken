// hud.edge.test.tsx — the blind EDGE suite for the Industrial HUD port,
// slices S2 (board minimalism) and S3 (color dropdown).
//
// Authored from artifacts/50-contract.md ALONE, concurrently with the
// implementation: no file under src/components/ or src/styles/ was read. Every
// assertion below is traceable to a contract row, and where a row says a rule
// is "unchanged" WITHOUT stating its content, nothing is asserted about that
// content — only about what the contract text itself fixes.
//
// S1 is CSS and out of blind-test scope (contract, "Blind test files").
//
// Conventions forced by the brief: every package-level identifier is prefixed
// `edge`; components are reached through NAMESPACE imports so a missing named
// export costs one test rather than the whole file, and so JSX can name them
// through a member expression (a bare lowercase JSX tag would be read as an
// intrinsic host element). vitest globals are on (vite.config.ts test.globals),
// so describe/it/expect/vi are not imported.

import * as edgeRTL from "@testing-library/react";

import { makeJob as edgeMakeJob } from "./test/fixtures.ts";
import * as edgeFormatMod from "./lib/format.ts";

import * as edgeLoadMod from "./components/board/chips/LoadChip.tsx";
import * as edgeDocQMod from "./components/board/chips/DocQuestionChip.tsx";
import * as edgeFeedMod from "./components/board/chips/FeedbackChip.tsx";
import * as edgeThreadMod from "./components/board/chips/ThreadChips.tsx";
import * as edgeWakeMod from "./components/board/chips/WakeupChip.tsx";
import * as edgePRMod from "./components/board/chips/PRChips.tsx";
import * as edgeCardMod from "./components/board/JobCard.tsx";
import * as edgeArchMod from "./components/board/ArchivedCard.tsx";
import * as edgeSwatchMod from "./components/overlays/SwatchStrip.tsx";

// ---------------------------------------------------------------- helpers

/**
 * Props are handed over as `any` on purpose. The exact prop TYPES of JobCard,
 * ArchivedCard and ThreadChips live in files this author may not read, and the
 * gate runs `tsc --noEmit` — a blind guess at a union type would fail the type
 * gate over something no obligation is about.
 */
function edgeProps(o: Record<string, unknown>) {
  return o as any;
}

/**
 * JobCard/ArchivedCard's `actions` prop shape is likewise unreadable from here
 * (it lives in a S2 implementation file), so it is a bag of vi.fn()s keyed by
 * every plausible verb. Unknown keys are inert; a missing one would only ever
 * surface as a render-time crash, which is itself a signal worth seeing.
 */
function edgeActions() {
  const bag: Record<string, unknown> = {};
  for (const verb of [
    "start", "stop", "cancel", "requeue", "hold", "ready", "archive",
    "unarchive", "del", "remove", "accept", "done", "reactivate", "steer",
    "open", "edit", "attach", "tmux", "spawn", "color", "answer", "reply",
    "kill", "revive", "branch", "openThread", "resume", "restart", "send",
    "focus", "raise", "capture", "unblock", "run",
  ]) {
    bag[verb] = vi.fn();
  }
  return bag;
}

// `job` is `any` rather than WebJob-typed for a TypeScript reason, not a
// blindness one: an interface has no implicit index signature, so a WebJob is
// not assignable to Record<string, unknown> and the type gate would fail on
// the helper rather than on any obligation.
function edgeCardProps(job: any, over: Record<string, unknown> = {}) {
  return edgeProps({
    job,
    // The contract says the status span is computed from the job itself
    // (`columnFor(job.health ? "blocked" : job.state)`), so any `col`-shaped
    // prop is passed consistently rather than relied on.
    col: job.health ? "blocked" : job.state,
    actions: edgeActions(),
    onOpen: vi.fn(),
    onSteer: vi.fn(),
    onSelect: vi.fn(),
    onEdit: vi.fn(),
    onAnswer: vi.fn(),
    onOpenThread: vi.fn(),
    onPick: vi.fn(),
    onCancel: vi.fn(),
    ...over,
  });
}

function edgeLoad(unblock: number, scope: number, note = "") {
  return { unblock, scope, note, at: 1_700_000_000_000 };
}

/**
 * A wakeup that fires `ms` from now, plus a 300ms cushion. The cushion is sized
 * so that flooring and rounding to whole seconds agree on the answer — the
 * contract fixes the FORM of the countdown, not which way it rounds, so a test
 * that could only pass under one of them would be asserting something the
 * contract does not say.
 */
function edgeFiresIn(ms: number) {
  return { reason: "edge poll", fires_at: Date.now() + ms + 300 };
}

const edgeColors: string[] = (edgeFormatMod as any).JOB_COLORS as string[];

function edgeSwatchBtn(container: HTMLElement) {
  return container.querySelector(".colorsel-btn") as HTMLElement | null;
}

function edgeSwatchMenu(container: HTMLElement) {
  return container.querySelector(".colorsel-menu") as HTMLElement | null;
}

function edgeSwatchRows(container: HTMLElement) {
  return Array.from(container.querySelectorAll(".colorsel-row")) as HTMLElement[];
}

function edgeOpenMenu(container: HTMLElement) {
  const btn = edgeSwatchBtn(container);
  expect(btn).toBeTruthy();
  edgeRTL.fireEvent.click(btn as HTMLElement);
  expect(edgeSwatchMenu(container)).toBeTruthy();
  return btn as HTMLElement;
}

afterEach(() => {
  edgeRTL.cleanup();
});

// ---------------------------------------------------------------- S2 · LoadChip

describe("edge · S2 LoadChip", () => {
  // Row: LoadChip — text is exactly `⚡ ${roi.toFixed(1)}×`.
  it("renders roi at exactly one decimal, rounding at the .05 boundary", () => {
    // 3/8 = 0.375 — exactly representable, so toFixed(1) is a fixed answer.
    const { container } = edgeRTL.render(
      <edgeLoadMod.LoadChip
        {...edgeProps({ job: edgeMakeJob({ state: "blocked", load: edgeLoad(8, 3) }), col: "blocked" })}
      />,
    );
    expect(container.textContent).toBe("⚡ 0.4×");
  });

  // Row: LoadChip — the two examples the contract spells out, at both extremes
  // of the 1..10 × 1..10 grid.
  it("renders the contract's own examples and the 10.0 ceiling", () => {
    const edgeCases: Array<[number, number, string]> = [
      [1, 4, "⚡ 4.0×"],
      [10, 3, "⚡ 0.3×"],
      [1, 10, "⚡ 10.0×"],
      [10, 1, "⚡ 0.1×"],
    ];
    for (const [unblock, scope, want] of edgeCases) {
      const { container, unmount } = edgeRTL.render(
        <edgeLoadMod.LoadChip
          {...edgeProps({ job: edgeMakeJob({ state: "blocked", load: edgeLoad(unblock, scope) }), col: "blocked" })}
        />,
      );
      expect(container.textContent).toBe(want);
      unmount();
    }
  });

  // Row: LoadChip — "No `unblock→scope` on the card".
  it("carries no arrow form and no bare digits pair in the chip text", () => {
    const { container } = edgeRTL.render(
      <edgeLoadMod.LoadChip
        {...edgeProps({ job: edgeMakeJob({ state: "blocked", load: edgeLoad(2, 6) }), col: "blocked" })}
      />,
    );
    expect(container.textContent).toBe("⚡ 3.0×");
    expect(container.textContent).not.toContain("→");
  });

  // Row: LoadChip — "`title`/`aria-label` keep the full sentence including
  // `unblock effort N/10 · next step N/10`" (the row was amended at the join;
  // its pre-amendment text misquoted the unchanged sentence as `unblock N/10`).
  it("keeps the long sentence on both title and aria-label", () => {
    const { container } = edgeRTL.render(
      <edgeLoadMod.LoadChip
        {...edgeProps({ job: edgeMakeJob({ state: "blocked", load: edgeLoad(2, 6, "why") }), col: "blocked" })}
      />,
    );
    const el = container.querySelector("[title]") as HTMLElement | null;
    expect(el).toBeTruthy();
    expect(el?.getAttribute("title")).toContain("unblock effort 2/10 · next step 6/10");
    expect(el?.getAttribute("aria-label")).toContain("unblock effort 2/10 · next step 6/10");
  });

  // Row: LoadChip — "Visibility rule unchanged (blocked column + whole estimate
  // only)". Only the two conditions the contract itself names are asserted.
  it("renders nothing outside the blocked column, and nothing without an estimate", () => {
    const edgeOffColumn = edgeRTL.render(
      <edgeLoadMod.LoadChip
        {...edgeProps({ job: edgeMakeJob({ state: "working", live: true, load: edgeLoad(2, 6) }), col: "working" })}
      />,
    );
    expect(edgeOffColumn.container.textContent).toBe("");
    edgeOffColumn.unmount();

    const edgeNoLoad = edgeRTL.render(
      <edgeLoadMod.LoadChip {...edgeProps({ job: edgeMakeJob({ state: "blocked" }), col: "blocked" })} />,
    );
    expect(edgeNoLoad.container.textContent).toBe("");
  });

  // Row: LoadChip — "band classes (`hot/warm/even/cold`) unchanged". Only
  // MEMBERSHIP in the four names the contract lists is asserted; the thresholds
  // that pick between them are not in the contract text.
  it("still carries one of the four band classes", () => {
    const { container } = edgeRTL.render(
      <edgeLoadMod.LoadChip
        {...edgeProps({ job: edgeMakeJob({ state: "blocked", load: edgeLoad(2, 6) }), col: "blocked" })}
      />,
    );
    const el = container.firstElementChild as HTMLElement;
    expect(el).toBeTruthy();
    const edgeBands = ["hot", "warm", "even", "cold"];
    expect(edgeBands.some((b) => el.classList.contains(b))).toBe(true);
  });
});

// ---------------------------------------------------------------- S2 · DocQuestionChip

describe("edge · S2 DocQuestionChip", () => {
  // Row: DocQuestionChip — text exactly `⚑ ${openCount}`, no `question(s)` word.
  it("renders the bare count with no word anywhere in the text", () => {
    const { container } = edgeRTL.render(
      <edgeDocQMod.DocQuestionChip
        job={edgeMakeJob({
          doc_questions: [
            { id: "q1", text: "a", importance: "P1" },
            { id: "q2", text: "b", importance: "P2" },
            { id: "q3", text: "c", importance: "P1" },
          ],
        })}
      />,
    );
    expect(container.textContent).toBe("⚑ 3");
    expect(container.textContent).not.toMatch(/question/i);
  });

  // Row: DocQuestionChip — the zero case. The contract calls the VISIBILITY
  // rule "unchanged" without stating it, so nothing is asserted about whether
  // the chip appears at zero; only that IF it appears, the frozen format holds
  // and no word leaks into it.
  it("never leaks a word at zero open questions, whatever the visibility rule decides", () => {
    const { container } = edgeRTL.render(
      <edgeDocQMod.DocQuestionChip
        job={edgeMakeJob({
          doc_questions: [
            { id: "q1", text: "a", importance: "P1", answer: "yes", answered_at: "2026-08-06T00:00:00Z" },
          ],
        })}
      />,
    );
    if (container.textContent !== "") {
      expect(container.textContent).toBe("⚑ 0");
    }
    expect(container.textContent).not.toMatch(/question/i);
  });

  // Row: DocQuestionChip — "`title` unchanged (still carries the P0 clause)".
  it("keeps a P0 clause on the title when a P0 question is open", () => {
    const { container } = edgeRTL.render(
      <edgeDocQMod.DocQuestionChip
        job={edgeMakeJob({
          doc_questions: [
            { id: "q1", text: "a", importance: "P0" },
            { id: "q2", text: "b", importance: "P2" },
          ],
        })}
      />,
    );
    const el = container.querySelector("[title]") as HTMLElement | null;
    expect(el).toBeTruthy();
    expect(el?.getAttribute("title")).toMatch(/p0/i);
  });
});

// ---------------------------------------------------------------- S2 · FeedbackChip

describe("edge · S2 FeedbackChip", () => {
  // Row: FeedbackChip — text exactly `✎ ${count}`; no `feedback` word, no
  // `no durable fix` clause in the TEXT; the detail lives on the title.
  it("renders the bare count and keeps the durable-fix wording out of the text", () => {
    const { container } = edgeRTL.render(
      <edgeFeedMod.FeedbackChip
        job={edgeMakeJob({
          feedback: [
            { id: 1, artifact: "00-overview.md", note: "n1", at: 1_700_000_000_000 },
            { id: 2, artifact: "50-contract.md", note: "n2", at: 1_700_000_000_001 },
          ],
        })}
      />,
    );
    expect(container.textContent).toBe("✎ 2");
    expect(container.textContent).not.toMatch(/feedback/i);
    expect(container.textContent).not.toMatch(/durable/i);
    const el = container.querySelector("[title]") as HTMLElement | null;
    expect(el).toBeTruthy();
    expect((el?.getAttribute("title") ?? "").length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------- S2 · ThreadChips

describe("edge · S2 ThreadChips", () => {
  const edgeThreads = [
    { tid: "t1", title: "first probe", state: "live", pane: "%9", created_at: 1_700_000_000_000 },
    { tid: "t2", title: "second probe", state: "closed", created_at: 1_700_000_000_001, closed_at: 1_700_000_100_000 },
  ];

  // Row: ThreadChips — compact renders ONE span (NOT a button) reading
  // `⑂ ${list.length}`, class `thread-count chip-threads`.
  it("compact renders one non-button span carrying the list length", () => {
    const { container } = edgeRTL.render(
      <edgeThreadMod.ThreadChips
        {...edgeProps({
          job: edgeMakeJob({ state: "working", live: true, threads: edgeThreads }),
          onOpenThread: vi.fn(),
          withBranch: false,
          compact: true,
        })}
      />,
    );
    const el = container.querySelector(".thread-count") as HTMLElement | null;
    expect(el).toBeTruthy();
    expect(el?.tagName).toBe("SPAN");
    expect(el?.classList.contains("chip-threads")).toBe(true);
    expect(el?.textContent).toBe("⑂ 2");
    expect(container.querySelectorAll("button").length).toBe(0);
    expect(container.querySelectorAll(".thread-count").length).toBe(1);
  });

  // Row: ThreadChips — compact's `title` lists every thread as `tid · title`
  // lines with live/closed noted.
  it("compact's title carries one line per thread with its state noted", () => {
    const { container } = edgeRTL.render(
      <edgeThreadMod.ThreadChips
        {...edgeProps({
          job: edgeMakeJob({ state: "working", live: true, threads: edgeThreads }),
          onOpenThread: vi.fn(),
          withBranch: false,
          compact: true,
        })}
      />,
    );
    const el = container.querySelector(".thread-count") as HTMLElement;
    const title = el.getAttribute("title") ?? "";
    const lines = title.split("\n").filter((l) => l.trim() !== "");
    expect(lines.length).toBe(2);
    const live = lines.find((l) => l.includes("t1")) ?? "";
    const closed = lines.find((l) => l.includes("t2")) ?? "";
    expect(live).toContain("·");
    expect(live).toContain("first probe");
    expect(live).toMatch(/live/i);
    expect(closed).toContain("second probe");
    expect(closed).toMatch(/closed/i);
  });

  // Row: ThreadChips — `compact?: boolean` DEFAULTS TO FALSE. Omitting the prop
  // must therefore give the modal shape (one button per thread), not the count
  // span.
  it("defaults to non-compact when the prop is omitted", () => {
    const { container } = edgeRTL.render(
      <edgeThreadMod.ThreadChips
        {...edgeProps({
          job: edgeMakeJob({ state: "working", live: true, threads: edgeThreads }),
          onOpenThread: vi.fn(),
          withBranch: false,
        })}
      />,
    );
    expect(container.querySelector(".thread-count")).toBeNull();
    const buttons = Array.from(container.querySelectorAll("button")) as HTMLElement[];
    expect(buttons.length).toBe(2);
    for (const b of buttons) expect(b.textContent?.startsWith("⑂ ")).toBe(true);
  });

  // Row: ThreadChips — non-compact label is `⑂ ${tid}${title ? " · " + title : ""}`.
  // Both falsy title shapes (absent and "") must take the no-separator branch.
  it("non-compact drops the separator for an untitled thread, absent or empty", () => {
    const edgeAbsent = edgeRTL.render(
      <edgeThreadMod.ThreadChips
        {...edgeProps({
          job: edgeMakeJob({
            state: "working",
            live: true,
            threads: [{ tid: "t9", state: "live", pane: "%1", created_at: 1_700_000_000_000 }],
          }),
          onOpenThread: vi.fn(),
          withBranch: false,
        })}
      />,
    );
    expect((edgeAbsent.container.querySelector("button") as HTMLElement).textContent).toBe("⑂ t9");
    edgeAbsent.unmount();

    const edgeEmpty = edgeRTL.render(
      <edgeThreadMod.ThreadChips
        {...edgeProps({
          job: edgeMakeJob({
            state: "working",
            live: true,
            threads: [{ tid: "t9", title: "", state: "live", pane: "%1", created_at: 1_700_000_000_000 }],
          }),
          onOpenThread: vi.fn(),
          withBranch: false,
        })}
      />,
    );
    expect((edgeEmpty.container.querySelector("button") as HTMLElement).textContent).toBe("⑂ t9");
  });
});

// ---------------------------------------------------------------- S2 · WakeupChip

describe("edge · S2 WakeupChip", () => {
  const edgeWorking = (wakeup: unknown) =>
    edgeMakeJob({ state: "working", live: true, wakeup: wakeup as any });

  // Row: WakeupChip — `due` (past).
  it("reads `⏱ due` once the wakeup is in the past", () => {
    const { container } = edgeRTL.render(
      <edgeWakeMod.WakeupChip job={edgeWorking({ reason: "edge poll", fires_at: Date.now() - 60_000 })} />,
    );
    expect(container.textContent).toBe("⏱ due");
  });

  // Row: WakeupChip — seconds form under one minute.
  it("reads seconds under one minute", () => {
    const { container } = edgeRTL.render(<edgeWakeMod.WakeupChip job={edgeWorking(edgeFiresIn(12_000))} />);
    expect(container.textContent).toBe("⏱ 12s");
  });

  // Row: WakeupChip — minute precision at and above one minute, and the long
  // form `next check in 4m 12s` living on the title rather than in the text.
  it("reads minutes above one minute and keeps the long form on the title", () => {
    const { container } = edgeRTL.render(
      <edgeWakeMod.WakeupChip job={edgeWorking(edgeFiresIn(4 * 60_000 + 12_000))} />,
    );
    expect(container.textContent).toBe("⏱ 4m");
    expect(container.textContent).not.toMatch(/next check in/i);
    const el = container.querySelector("[title]") as HTMLElement | null;
    expect(el).toBeTruthy();
    expect(el?.getAttribute("title")).toContain("next check in 4m 12s");
  });

  it("reads the contract's own 47m example", () => {
    const { container } = edgeRTL.render(<edgeWakeMod.WakeupChip job={edgeWorking(edgeFiresIn(47 * 60_000))} />);
    expect(container.textContent).toBe("⏱ 47m");
  });

  // Row: WakeupChip — "Visibility rule unchanged (working + wakeup present)".
  // Both halves of the rule the contract states are asserted; nothing else.
  it("renders nothing without a wakeup, and nothing off the working column", () => {
    const edgeNoWakeup = edgeRTL.render(
      <edgeWakeMod.WakeupChip job={edgeMakeJob({ state: "working", live: true })} />,
    );
    expect(edgeNoWakeup.container.textContent).toBe("");
    edgeNoWakeup.unmount();

    const edgeBlocked = edgeRTL.render(
      <edgeWakeMod.WakeupChip
        job={edgeMakeJob({ state: "blocked", live: true, wakeup: edgeFiresIn(60_000) })}
      />,
    );
    expect(edgeBlocked.container.textContent).toBe("");
  });
});

// ---------------------------------------------------------------- S2 · PRChips

describe("edge · S2 PRChips", () => {
  // Row: PRChips — label exactly `#${n}` when the URL parses `/pull/<n>`;
  // "state is said by color alone", so the state word must not be in the text.
  it("renders the parsed number alone and says the state only in the class", () => {
    const { container } = edgeRTL.render(
      <edgePRMod.PRChips
        {...edgeProps({ prs: [{ url: "https://github.com/car1zhang/kraken/pull/123", state: "merged" }] })}
      />,
    );
    expect(container.textContent).toBe("#123");
    expect(container.textContent).not.toMatch(/merged/i);
    expect(container.querySelector(".pr-merged")).toBeTruthy();
  });

  // Row: PRChips — "the bare state word when it does not" parse.
  it("falls back to the bare state word when the URL carries no pull number", () => {
    const { container } = edgeRTL.render(
      <edgePRMod.PRChips {...edgeProps({ prs: [{ url: "https://github.com/car1zhang/kraken", state: "open" }] })} />,
    );
    expect(container.textContent).toBe("open");
    expect(container.textContent).not.toContain("#");
    expect(container.querySelector(".pr-open")).toBeTruthy();
  });

  // Row: PRChips — same fallback with no URL at all, plus the link/span half of
  // the rule: with no URL there is nothing to link to.
  it("falls back to the state word and to a non-link with an empty URL", () => {
    const { container } = edgeRTL.render(
      <edgePRMod.PRChips {...edgeProps({ prs: [{ url: "", state: "closed" }] })} />,
    );
    expect(container.textContent).toBe("closed");
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector(".pr-closed")).toBeTruthy();
  });

  // Row: PRChips — link rule unchanged: a PR with a URL is reachable.
  it("links a numbered chip at its own URL", () => {
    const edgeURL = "https://github.com/car1zhang/kraken/pull/7";
    const { container } = edgeRTL.render(
      <edgePRMod.PRChips {...edgeProps({ prs: [{ url: edgeURL, state: "open" }] })} />,
    );
    const a = container.querySelector("a") as HTMLAnchorElement | null;
    expect(a).toBeTruthy();
    expect(a?.getAttribute("href")).toBe(edgeURL);
    expect(a?.textContent).toBe("#7");
  });
});

// ---------------------------------------------------------------- S2 · JobCard

describe("edge · S2 JobCard", () => {
  // Row: JobCard band 1/3 — draft ⇒ NO status span.
  it("renders no status span at all on a draft card", () => {
    const { container } = edgeRTL.render(
      <edgeCardMod.JobCard {...edgeCardProps(edgeMakeJob({ state: "draft" }))} />,
    );
    expect(container.querySelector(".status")).toBeNull();
  });

  // Row: JobCard band 1/3 — the `.state` span is REMOVED from `.card-top`.
  it("no longer carries a .state span in .card-top on a live card", () => {
    const { container } = edgeRTL.render(
      <edgeCardMod.JobCard
        {...edgeCardProps(edgeMakeJob({ state: "working", live: true, cell: "demo-1" }))}
      />,
    );
    expect(container.querySelector(".card-top .state")).toBeNull();
    expect(container.textContent).not.toContain("● working");
  });

  // Row: JobCard band 1/3 as amended (post-gate: carl removed the live
  // active/blocked tokens outright) — working ⇒ NO status span.
  it("renders no status span on a live working card", () => {
    const { container } = edgeRTL.render(
      <edgeCardMod.JobCard
        {...edgeCardProps(edgeMakeJob({ state: "working", live: true, cell: "demo-1" }))}
      />,
    );
    expect(container.querySelector(".status")).toBeNull();
  });

  // Row: JobCard band 1/3 as amended — blocked ⇒ NO status span, including
  // the health-forced blocked reading (`columnFor(job.health ? "blocked" :
  // job.state)` still decides the display column; both live columns now
  // render nothing, so health can no longer be told apart by token — nor
  // should it be, per the amendment).
  it("renders no status span on blocked and health-forced-blocked cards", () => {
    const edgeSick = edgeRTL.render(
      <edgeCardMod.JobCard
        {...edgeCardProps(edgeMakeJob({ state: "working", live: true, cell: "demo-1", health: "zeus pane gone" }))}
      />,
    );
    expect(edgeSick.container.querySelector(".status")).toBeNull();
    edgeSick.unmount();

    const edgeBlocked = edgeRTL.render(
      <edgeCardMod.JobCard
        {...edgeCardProps(edgeMakeJob({ state: "blocked", live: true, cell: "demo-1" }))}
      />,
    );
    expect(edgeBlocked.container.querySelector(".status")).toBeNull();
  });

  // Row: JobCard band 1/3 — the one settled column keeps its token. The pair
  // loop this test ran (`◌ queued` beside `✓ done`) lost half its subject with
  // the queued column, and the "keeps the queued ✕ inside .card-top" test next
  // to it lost all of its own: submit starts the cell, so there is no booking
  // for a ✕ to cancel.
  it("reads `✓ done` in the done column", () => {
    const { container } = edgeRTL.render(
      <edgeCardMod.JobCard {...edgeCardProps(edgeMakeJob({ state: "done" }))} />,
    );
    expect((container.querySelector(".status") as HTMLElement)?.textContent).toBe(
      "✓ done",
    );
  });

  // Row: JobCard outcome — suppressed when `job.outcome === "accepted"`.
  it("suppresses the outcome line for exactly the string `accepted`", () => {
    const { container } = edgeRTL.render(
      <edgeCardMod.JobCard {...edgeCardProps(edgeMakeJob({ state: "done", outcome: "accepted" }))} />,
    );
    expect(container.querySelector(".outcome")).toBeNull();
    expect(container.textContent).not.toMatch(/accepted/i);
  });

  // Row: JobCard outcome — the suppression is an exact `===`, so a case variant
  // is "any other outcome" and still renders `→ <outcome>`.
  it("still renders a case-variant outcome, and any other outcome, as `→ x`", () => {
    const edgeCase = edgeRTL.render(
      <edgeCardMod.JobCard {...edgeCardProps(edgeMakeJob({ state: "done", outcome: "Accepted" }))} />,
    );
    expect(edgeCase.container.textContent).toContain("→ Accepted");
    edgeCase.unmount();

    const edgeOther = edgeRTL.render(
      <edgeCardMod.JobCard {...edgeCardProps(edgeMakeJob({ state: "done", outcome: "superseded" }))} />,
    );
    expect(edgeOther.container.textContent).toContain("→ superseded");
  });

  // Row: JobCard band 4 — QuestionBlock is NOT rendered on the card; the chip
  // row carries `<span class="chip-question">⚑</span>` with a fixed title, and
  // `hasChips` accounts for it (this job has NO other chip, so the chip's mere
  // presence is the hasChips obligation).
  it("replaces the on-card QuestionBlock with a single ⚑ chip", () => {
    const { container } = edgeRTL.render(
      <edgeCardMod.JobCard
        {...edgeCardProps(
          edgeMakeJob({
            state: "blocked",
            live: true,
            cell: "demo-1",
            question: {
              tool_use_id: "tu-edge",
              questions: [
                {
                  question: "EDGE-UNIQUE-QUESTION-BODY",
                  header: "EDGE-HEADER",
                  multi_select: false,
                  options: [{ label: "EDGE-OPTION-A", description: "EDGE-OPTION-DESC" }],
                },
              ],
            },
          }),
        )}
      />,
    );
    const chip = container.querySelector(".chip-question") as HTMLElement | null;
    expect(chip).toBeTruthy();
    expect(chip?.tagName).toBe("SPAN");
    expect(chip?.textContent).toBe("⚑");
    expect(chip?.getAttribute("title")).toBe("pending question — open the card to answer");
    expect(container.textContent).not.toContain("EDGE-UNIQUE-QUESTION-BODY");
    expect(container.textContent).not.toContain("EDGE-OPTION-A");
  });

  // Row: JobCard band 6 — the tmux button's text is exactly `⌁ attach tmux`.
  it("labels the tmux control exactly, with no other tmux wording on the card", () => {
    edgeRTL.render(
      <edgeCardMod.JobCard
        {...edgeCardProps(edgeMakeJob({ state: "working", live: true, cell: "demo-1", zeus_status: "running" }))}
      />,
    );
    const hits = edgeRTL.screen.getAllByText(/tmux/i);
    expect(hits.length).toBeGreaterThan(0);
    for (const h of hits) expect(h.textContent).toBe("⌁ attach tmux");
  });
});

// ---------------------------------------------------------------- S2 · ArchivedCard

describe("edge · S2 ArchivedCard", () => {
  // Row: ArchivedCard — its `.state` span reads `■ archived` (replacing
  // `● archived`).
  it("reads `■ archived` and no longer carries the ● form", () => {
    const { container } = edgeRTL.render(
      <edgeArchMod.ArchivedCard
        {...edgeCardProps(edgeMakeJob({ state: "archived", archived: true, slug: "demo" }))}
      />,
    );
    const state = container.querySelector(".state") as HTMLElement | null;
    expect(state).toBeTruthy();
    expect(state?.textContent).toBe("■ archived");
    expect(container.textContent).not.toContain("● archived");
  });

  // Row: ArchivedCard — "the renamed `as original-slug` span stays".
  it("keeps the renamed `as …` span", () => {
    const { container } = edgeRTL.render(
      <edgeArchMod.ArchivedCard
        {...edgeCardProps(
          edgeMakeJob({ state: "archived", archived: true, slug: "demo-2", display_slug: "demo" }),
        )}
      />,
    );
    const spans = Array.from(container.querySelectorAll("span")) as HTMLElement[];
    const renamed = spans.find((s) => (s.textContent ?? "").trim().startsWith("as "));
    expect(renamed).toBeTruthy();
  });

  // Row: FeedbackChip — "Renders on archived cards as before", in the frozen
  // count-only form.
  it("still shows the feedback chip, in the new bare-count form", () => {
    const { container } = edgeRTL.render(
      <edgeArchMod.ArchivedCard
        {...edgeCardProps(
          edgeMakeJob({
            state: "archived",
            archived: true,
            slug: "demo",
            feedback: [{ id: 1, artifact: "00-overview.md", note: "n", at: 1_700_000_000_000 }],
          }),
        )}
      />,
    );
    expect(container.textContent).toContain("✎ 1");
    expect(container.textContent).not.toMatch(/feedback/i);
  });
});

// ---------------------------------------------------------------- S3 · SwatchStrip

describe("edge · S3 SwatchStrip", () => {
  // Row: SwatchStrip — "Renders a dropdown, not radios".
  it("renders no radio inputs at all", () => {
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: "", onPick: vi.fn(), compact: false })}
      />,
    );
    expect(container.querySelector("input[type=radio]")).toBeNull();
    expect(edgeSwatchBtn(container)).toBeTruthy();
  });

  // Row: value fallback — a `value` not in `JOB_COLORS` renders as the no-color
  // state. The contract fixes the no-color SHAPE as `— `/`NO COLOR`, so either
  // is accepted; what is refused is the unknown name being shown as a colour.
  it("falls back to the no-color state for an unrecognized value", () => {
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: "edge-not-a-color", onPick: vi.fn(), compact: false })}
      />,
    );
    const btn = edgeSwatchBtn(container) as HTMLElement;
    const text = btn.textContent ?? "";
    expect(text).not.toMatch(/edge-not-a-color/i);
    expect(text).toMatch(/—|NO COLOR/);
  });

  // Row: value fallback + the current row carrying `.sel` — an unrecognized
  // value must make the NO COLOR row (the first row) the selected one.
  it("marks the NO COLOR row selected when the value is unrecognized", () => {
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: "edge-not-a-color", onPick: vi.fn(), compact: false })}
      />,
    );
    edgeOpenMenu(container);
    const rows = edgeSwatchRows(container);
    expect(rows.length).toBeGreaterThan(0);
    expect(rows[0]!.classList.contains("sel")).toBe(true);
    expect(rows[0]!.textContent).toContain("✓");
    expect(container.querySelectorAll(".colorsel-row.sel").length).toBe(1);
  });

  // Row: pick behaviour — the no-color row picks `""`, exactly once, and closes.
  it("picks the empty string from the NO COLOR row and closes", () => {
    const edgeOnPick = vi.fn();
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: edgeColors[0], onPick: edgeOnPick, compact: false })}
      />,
    );
    edgeOpenMenu(container);
    edgeRTL.fireEvent.click(edgeSwatchRows(container)[0]!);
    expect(edgeOnPick).toHaveBeenCalledTimes(1);
    expect(edgeOnPick).toHaveBeenCalledWith("");
    expect(edgeSwatchMenu(container)).toBeNull();
  });

  // Row: pick behaviour — "Re-clicking the current row still calls `onPick`".
  it("still calls onPick when the already-current row is re-picked", () => {
    const edgeOnPick = vi.fn();
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: edgeColors[0], onPick: edgeOnPick, compact: false })}
      />,
    );
    edgeOpenMenu(container);
    // Row order is frozen: NO COLOR first, then the eight JOB_COLORS in order.
    const rows = edgeSwatchRows(container);
    expect(rows.length).toBe(edgeColors.length + 1);
    edgeRTL.fireEvent.click(rows[1]!);
    expect(edgeOnPick).toHaveBeenCalledTimes(1);
    expect(edgeOnPick).toHaveBeenCalledWith(edgeColors[0]);
    expect(edgeSwatchMenu(container)).toBeNull();
  });

  // Row: pick behaviour — a non-current row picks its own value, once.
  it("picks the row's own value exactly once", () => {
    const edgeOnPick = vi.fn();
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: "", onPick: edgeOnPick, compact: false })}
      />,
    );
    edgeOpenMenu(container);
    const rows = edgeSwatchRows(container);
    edgeRTL.fireEvent.click(rows[3]!);
    expect(edgeOnPick).toHaveBeenCalledTimes(1);
    expect(edgeOnPick).toHaveBeenCalledWith(edgeColors[2]);
  });

  // Row: pick behaviour — "Escape … close[s] without calling `onPick`".
  it("closes on Escape without picking", () => {
    const edgeOnPick = vi.fn();
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: edgeColors[0], onPick: edgeOnPick, compact: false })}
      />,
    );
    const btn = edgeOpenMenu(container);
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    // The contract fixes the BEHAVIOUR, not which node listens, so Escape is
    // delivered at each plausible point rather than at a guessed one.
    edgeRTL.fireEvent.keyDown(btn, { key: "Escape", code: "Escape" });
    const menu = edgeSwatchMenu(container);
    if (menu) edgeRTL.fireEvent.keyDown(menu, { key: "Escape", code: "Escape" });
    if (edgeSwatchMenu(container)) edgeRTL.fireEvent.keyDown(document, { key: "Escape", code: "Escape" });
    expect(edgeSwatchMenu(container)).toBeNull();
    expect(edgeOnPick).not.toHaveBeenCalled();
    expect(edgeSwatchBtn(container)?.getAttribute("aria-expanded")).toBe("false");
  });

  // Row: pick behaviour — "outside-click close[s] without calling `onPick`".
  it("closes on an outside click without picking", () => {
    const edgeOnPick = vi.fn();
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: edgeColors[0], onPick: edgeOnPick, compact: false })}
      />,
    );
    edgeOpenMenu(container);
    // Again: mousedown and click, since the contract fixes the outcome and not
    // which document-level event the close listens on.
    edgeRTL.fireEvent.mouseDown(document.body);
    if (edgeSwatchMenu(container)) edgeRTL.fireEvent.click(document.body);
    expect(edgeSwatchMenu(container)).toBeNull();
    expect(edgeOnPick).not.toHaveBeenCalled();
  });

  // Row: a11y — button `aria-haspopup="listbox"` + `aria-expanded`, menu
  // `role="listbox"`, rows `role="option"` with `aria-selected` on the current.
  it("carries the frozen listbox roles and selection state", () => {
    const { container } = edgeRTL.render(
      <edgeSwatchMod.SwatchStrip
        {...edgeProps({ name: "edge-color", value: edgeColors[1], onPick: vi.fn(), compact: false })}
      />,
    );
    const btn = edgeSwatchBtn(container) as HTMLElement;
    expect(btn.getAttribute("aria-haspopup")).toBe("listbox");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    edgeOpenMenu(container);
    expect((edgeSwatchMenu(container) as HTMLElement).getAttribute("role")).toBe("listbox");
    const options = Array.from(container.querySelectorAll("[role=option]")) as HTMLElement[];
    expect(options.length).toBe(edgeColors.length + 1);
    const selected = options.filter((o) => o.getAttribute("aria-selected") === "true");
    expect(selected.length).toBe(1);
    expect(selected[0]!.textContent).toContain(edgeColors[1]!.toUpperCase());
  });

  // Row: event containment — click/input/change from the component do not
  // propagate to ancestors. The ancestors that matter are REACT handlers (the
  // editor's autosave, the card-click rules), so the spies are React props.
  it("does not propagate click/input/change to an ancestor", () => {
    const edgeOnPick = vi.fn();
    const edgeParentClick = vi.fn();
    const edgeParentInput = vi.fn();
    const edgeParentChange = vi.fn();
    const { container } = edgeRTL.render(
      <div onClick={edgeParentClick} onInput={edgeParentInput} onChange={edgeParentChange}>
        <edgeSwatchMod.SwatchStrip
          {...edgeProps({ name: "edge-color", value: "", onPick: edgeOnPick, compact: false })}
        />
      </div>,
    );
    const btn = edgeOpenMenu(container);
    edgeRTL.fireEvent.input(btn);
    edgeRTL.fireEvent.change(btn);
    const rows = edgeSwatchRows(container);
    edgeRTL.fireEvent.click(rows[1]!);
    // The pick landed — so the click really did reach the component.
    expect(edgeOnPick).toHaveBeenCalledTimes(1);
    expect(edgeParentClick).not.toHaveBeenCalled();
    expect(edgeParentInput).not.toHaveBeenCalled();
    expect(edgeParentChange).not.toHaveBeenCalled();
  });
});
