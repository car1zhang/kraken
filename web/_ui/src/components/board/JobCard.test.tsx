import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../api/types.ts";
import type { Column } from "../../lib/columns.ts";
import { makeJob } from "../../test/fixtures.ts";
import type { BoardActions } from "./BoardView.tsx";
import { JobCard } from "./JobCard.tsx";

// Local, and deliberately not exported: importing a fixture from a test module
// runs that module's suites a second time under the importer's name.
//
// `& { color?: string }` so the colour cases below do not depend on how the
// wire type spells the field — the same reason jobColorVar takes a structural
// `{ color?: string }` rather than a Pick<WebJob, "color">.
const job = (over: Partial<WebJob> & { color?: string } = {}): WebJob =>
  makeJob({ mtime: Math.floor(Date.now() / 1000), ...over } as Partial<WebJob>);

function stubActions(): BoardActions {
  return {
    onOpenSteer: vi.fn(),
    onOpenEditor: vi.fn(),
    onOpenArtifact: vi.fn(),
    onOpenThread: vi.fn(),
    onAnswerQuestion: vi.fn(),
    onRetry: vi.fn(),
    onDequeue: vi.fn(),
    onOpenTmux: vi.fn(),
    onSaveFeedback: vi.fn(async () => null),
    onDeleteFeedback: vi.fn(),
    onUnarchive: vi.fn(),
    onDeleteForever: vi.fn(),
  };
}

function renderCard(j: WebJob, actions = stubActions(), col?: Column) {
  const column = col ?? ((j.state === "archived" ? "done" : j.state) as Column);
  const { container } = render(<JobCard job={j} col={column} actions={actions} />);
  return { container, actions, card: container.querySelector(".card")! };
}

const QUESTIONS = [
  { id: "Q1", text: "which store", importance: "P0" },
  { id: "Q2", text: "which name", importance: "P1" },
  { id: "Q3", text: "settled already", importance: "P1", answer: "yes" },
];

// ---------------------------------------------------------------------------
// THE SIX BANDS (cardHTML, commit 0224c1e).
//
// identity · chips · status · question · PRs · actions — this order, always
// this order, whatever column the card is in. The numbers are shared vocabulary
// with style.css and the iOS JobCardView.
// ---------------------------------------------------------------------------

// BAND_OF maps a card's direct child to the band it belongs to. Asserting over
// the ROOT's children rather than over a flat querySelectorAll is what makes
// this a layout test: a block nested one level deeper (say, PRs folded into the
// chip row) stops being a direct child and fails here, which is the regression
// the band model exists to prevent.
const BAND_OF: Array<[string, number]> = [
  ["card-top", 1],
  ["meta", 1],
  ["chips", 2],
  ["outcome", 3],
  ["err", 3],
  ["last-sent", 3],
  ["detail", 3],
  ["wakeup-row", 3],
  ["wakeup-reason", 3],
  ["question", 4],
  ["prs", 5],
  ["card-actions", 6],
];

function bands(card: Element): number[] {
  return [...card.children].map((el) => {
    const hit = BAND_OF.find(([cls]) => el.classList.contains(cls));
    if (!hit) throw new Error(`card child in no band: <${el.tagName} class="${el.className}">`);
    return hit[1];
  });
}

// Everything a card can carry at once, so every band is populated.
const MAXIMAL = job({
  state: "working",
  live: true,
  cell: "kraken-demo",
  repo: "kraken",
  health: "zeus stopped answering",
  last_sent: "please continue",
  detail: "reading the transcript",
  wakeup: { reason: "waiting on CI", fires_at: Date.now() + 60_000 },
  zeus_status: "running",
  roster: [{ name: "heph-1", model: "opus", role: "hephaestus", status: "run", working: true }],
  threads: [{ tid: "t1", state: "live", created_at: 0 }],
  doc_questions: QUESTIONS,
  artifacts: [
    { name: "00-overview.md", url: "/a/0" },
    { name: "10-arch.html", url: "/a/1" },
  ],
  feedback: [{ id: 1, artifact: "00-overview.md", note: "too long", at: 0 }],
  question: {
    tool_use_id: "t",
    questions: [
      {
        question: "which store?",
        header: "store",
        multi_select: false,
        options: [{ label: "sqlite", description: "one file" }],
      },
    ],
  },
  prs: [{ url: "https://github.com/o/r/pull/73", state: "open" }],
});

describe("the six bands", () => {
  it("renders every band, in order, on a maximal card", () => {
    const { card } = renderCard(MAXIMAL);
    const seen = bands(card);
    // present at all…
    expect([...new Set(seen)]).toEqual([1, 2, 3, 4, 5, 6]);
    // …and never out of order
    expect(seen).toEqual([...seen].sort((a, b) => a - b));
  });

  it("keeps identity contiguous — nothing between .card-top and .meta", () => {
    const { card } = renderCard(MAXIMAL);
    const kids = [...card.children];
    expect(kids[0]!.classList.contains("card-top")).toBe(true);
    expect(kids[1]!.classList.contains("meta")).toBe(true);
  });

  it("drops a band whole when it has nothing to say", () => {
    // a bare draft: identity only — no chips, no status, no question, no PRs,
    // and no actions row (there is nothing to press on a draft).
    const { card } = renderCard(job({ state: "draft" }));
    expect(bands(card)).toEqual([1, 1]);
    expect(card.querySelector(".chips")).toBeNull();
    expect(card.querySelector(".card-actions")).toBeNull();
  });

  it("puts PRs in band 5, NOT in the chip row", () => {
    // A PR is the job's OUTPUT, not part of its inventory — the one chip that
    // leaves the app, and the only one naming a specific external thing.
    const { card } = renderCard(MAXIMAL);
    const prs = card.querySelector(".prs")!;
    expect(prs.parentElement).toBe(card);
    expect(card.querySelector(".chips")!.querySelector(".pr-chip")).toBeNull();
  });

  it("gives band 6 the tmux/retry/reactivate controls and nothing else", () => {
    const { card } = renderCard(MAXIMAL);
    const row = card.querySelector(".card-actions")!;
    expect(row.querySelector(".tmux-open")).not.toBeNull();
    expect(row.querySelector(".retry")).not.toBeNull();
    // ⑂ branch is deliberately NOT an action: branching is a detail action and
    // lives in the modal.
    expect(card.querySelector(".branch-btn")).toBeNull();
  });

  it("offers reactivate on a done (or cell-gone) job that allows it", () => {
    const { card } = renderCard(job({ state: "done", reactivatable: true }));
    expect(card.querySelector(".card-actions .reactivate")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// BAND 2 — the inventory surface carries COUNTS, not lists.
// ---------------------------------------------------------------------------

describe("band 2 carries counts, not lists", () => {
  it("collapses the artifact list to one bare ▤ count", () => {
    // Real-shaped names: a one-letter fixture would be a substring of the
    // repo/slug the card legitimately prints, so the not-named assertion below
    // could never fail.
    const artifacts = ["00-overview.md", "10-arch.html", "50-store.md", "90-build-report.md"].map(
      (n) => ({ name: n, url: "/x/" + n }),
    );
    const { card } = renderCard(job({ artifacts }));
    const ct = card.querySelector(".chips .ct-chip")!;
    expect(ct).toHaveTextContent("▤ 4");
    expect(ct.tagName).toBe("SPAN");
    // no names, no per-artifact buttons, and no "+N more" toggle: the cap and
    // its localStorage-persisted expansion state went with the list.
    expect(card.querySelector(".chip")).toBeNull();
    expect(card.querySelector(".chip-more")).toBeNull();
    expect(card.textContent).not.toContain("more");
    for (const a of artifacts) expect(card.textContent).not.toContain(a.name);
  });

  it("is NOT class=chip — that class is the artifact-overlay click target", () => {
    const { card } = renderCard(job({ artifacts: [{ name: "a", url: "/x/a" }] }));
    expect(card.querySelector(".ct-chip")!.classList.contains("chip")).toBe(false);
  });

  it("counts feedback with its open half, and never suppresses it on a done job", () => {
    // Unlike a question, a note is never "settled" by the job being done — carl
    // leaves most of them ON done work.
    const { card } = renderCard(
      job({
        state: "done",
        feedback: [
          { id: 1, artifact: "a", note: "n", at: 0 },
          { id: 2, artifact: "b", note: "m", at: 0, addressed: "fixed" },
        ],
      }),
    );
    expect(card.querySelector(".chips .fb-chip")).toHaveTextContent("2 feedback · 1 open");
    expect(card.querySelector(".fb-chip")!.classList.contains("open")).toBe(true);
    // the answerable panel is one click in, never on the card
    expect(card.querySelector(".job-feedback")).toBeNull();
    expect(card.querySelector(".fb-row")).toBeNull();
  });

  it("shows crew tokens BARE — no .crew wrapper, and no roster block under them", () => {
    const { card } = renderCard(
      job({
        state: "working",
        live: true,
        zeus_status: "running",
        roster: [
          { name: "heph-1", model: "opus", role: "hephaestus", status: "run", working: true },
          { name: "heph-2", model: "opus", role: "hephaestus", status: "idle", working: false },
        ],
      }),
    );
    const chips = card.querySelector(".chips")!;
    expect(chips.querySelector(".crew-pill")).not.toBeNull();
    expect(chips.querySelector(".crew-chip")).toHaveTextContent("1/2");
    // .crew is MODAL-ONLY (#steer-crew carries it); on a card the tokens are bare
    expect(card.querySelector(".crew")).toBeNull();
    // the per-teammate roster block restated the chips directly above it
    expect(card.querySelector(".roster")).toBeNull();
    expect(card.querySelector(".roster-row")).toBeNull();
    // the names it used to print survive in the chip's tooltip
    expect(card.querySelector(".crew-chip")!.getAttribute("title")).toContain("● heph-1 (opus)");
  });

  it("carries branch chips as STATUS, with no ⑂ branch affordance", () => {
    const { card } = renderCard(
      job({
        state: "working",
        live: true,
        threads: [
          { tid: "t1", state: "live", created_at: 0 },
          { tid: "t2", state: "closed", created_at: 0 },
        ],
      }),
    );
    const chips = card.querySelector(".chips")!;
    expect(chips.querySelectorAll(".thread-chip")).toHaveLength(2);
    expect(card.querySelector(".branch-btn")).toBeNull();
    // and no .threads row of its own any more — that class is modal-only
    expect(card.querySelector(".threads")).toBeNull();
  });

  it("opens a thread from its chip without opening the card", async () => {
    const { card, actions } = renderCard(
      job({ state: "working", live: true, threads: [{ tid: "t1", state: "live", created_at: 0 }] }),
    );
    await userEvent.click(card.querySelector(".thread-chip")!);
    expect(actions.onOpenThread).toHaveBeenCalledWith("demo", "t1");
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
  });

  it("renders no chip row at all when the job has nothing in it", () => {
    expect(renderCard(job()).card.querySelector(".chips")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The assertions web/boardsurface_test.go pins by grepping app.js. They exist
// because the failure mode is SILENT: re-adding the panel to the card renders
// fine, passes every Go test, and quietly re-buries a column behind one job's
// eight open questions.
// ---------------------------------------------------------------------------

describe("the card's question surface is the count chip and nothing else", () => {
  it("renders the chip, never the answerable panel", () => {
    const { card } = renderCard(job({ doc_questions: QUESTIONS }));
    const chip = card.querySelector(".dq-chip");
    expect(chip).not.toBeNull();
    expect(chip).toHaveTextContent("⚑ 2 questions");
    // the panel and every answer affordance live one click into the job
    expect(card.querySelector(".doc-questions")).toBeNull();
    expect(card.querySelector(".dq-row")).toBeNull();
    expect(card.querySelector(".dq-input")).toBeNull();
    expect(card.querySelector(".dq-send")).toBeNull();
  });

  it("does NOT carry class=chip — that is the artifact-overlay click target", () => {
    const { card } = renderCard(
      job({ doc_questions: QUESTIONS, artifacts: [{ name: "00-overview.md", url: "/a/1" }] }),
    );
    const chip = card.querySelector(".dq-chip")!;
    expect(chip.classList.contains("chip")).toBe(false);
    expect(chip.tagName).toBe("SPAN");
    // and there is no `.chip` on the card at all any more — the artifact list
    // that owned that class is a count now
    expect(card.querySelectorAll(".chip")).toHaveLength(0);
  });

  it("counts UNANSWERED questions only, and vanishes when the last is answered", () => {
    const { card } = renderCard(job({ doc_questions: [QUESTIONS[2]!] }));
    expect(card.querySelector(".dq-chip")).toBeNull();
    const one = renderCard(job({ doc_questions: [QUESTIONS[0]!] })).card;
    expect(one.querySelector(".dq-chip")).toHaveTextContent("⚑ 1 question");
  });

  it("skips a settled (done/archived) job — nobody is waiting on its answers", () => {
    const { card } = renderCard(job({ state: "done", doc_questions: QUESTIONS }));
    expect(card.querySelector(".dq-chip")).toBeNull();
    const arch = renderCard(job({ state: "done", archived: true, doc_questions: QUESTIONS })).card;
    expect(arch.querySelector(".dq-chip")).toBeNull();
  });

  it("flags the P0 chip so style.css can paint it hot", () => {
    const p0 = renderCard(job({ doc_questions: QUESTIONS })).card.querySelector(".dq-chip")!;
    expect(p0.classList.contains("p0")).toBe(true);
    expect(p0.getAttribute("title")).toContain("(1 P0)");
    const noP0 = renderCard(job({ doc_questions: [QUESTIONS[1]!] })).card.querySelector(".dq-chip")!;
    expect(noP0.classList.contains("p0")).toBe(false);
  });
});

// ---------------------------------------------------------------------------

describe("state color", () => {
  it("rides on the --state inline custom property", () => {
    // style.css reads var(--state) for the `● working` chip; a card that loses
    // the inline property loses its state color silently, since var(--state)
    // simply resolves to nothing.
    const { card } = renderCard(job({ state: "working", live: true }));
    expect(card.getAttribute("style")).toContain("--state: var(--solved)");
  });
});

// ---------------------------------------------------------------------------
// JOB COLOUR — the 2px left accent bar, and the card's only colour channel.
//
// style.css: `border-left: 2px solid var(--job-color, var(--text-2))`. The bar
// carries neither state nor alerts, so nothing else on the card can stand in
// for it — a card that stops emitting the property does not degrade, it loses
// the whole feature, and every coloured job goes grey with no error anywhere.
// internal/jobs/color.go types the same colour into the cell's live tmux pane,
// so board and pane are meant to be the same pixel colour.
// ---------------------------------------------------------------------------

describe("job colour", () => {
  it("emits --job-color for each of /color's eight names", () => {
    for (const name of ["red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"]) {
      const { card } = renderCard(job({ color: name }));
      expect(card.getAttribute("style")).toContain(`--job-color: var(--job-${name})`);
    }
  });

  it("keeps --state alongside it — they are different channels", () => {
    const { card } = renderCard(job({ state: "working", live: true, color: "purple" }));
    const style = card.getAttribute("style")!;
    expect(style).toContain("--state: var(--solved)");
    expect(style).toContain("--job-color: var(--job-purple)");
  });

  it("OMITS the property entirely when the job has no colour", () => {
    // Not `--job-color: ""`: a custom property set to the empty string
    // substitutes nothing, so `var(--job-color, var(--text-2))` would resolve to
    // nothing rather than to its fallback and the border-left declaration would
    // go invalid at computed-value time. Absence is what makes the bar grey.
    const { card } = renderCard(job());
    expect(card.getAttribute("style")).not.toContain("--job-color");
  });

  it("drops an unrecognised colour from the wire rather than passing it through", () => {
    // The whitelist is what makes the value safe to hand to a style attribute
    // at all — nothing from the wire reaches the stylesheet unchecked.
    const { card } = renderCard(job({ color: "octarine" }));
    expect(card.getAttribute("style")).not.toContain("--job-color");
  });
});

describe("the card click fallback", () => {
  it("opens the steer composer on a live working job", async () => {
    const { card, actions } = renderCard(job({ state: "working", live: true }));
    await userEvent.click(card);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("demo", "steer");
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });

  it("opens it in unblock mode on a live blocked job", async () => {
    const { card, actions } = renderCard(job({ state: "blocked", live: true }));
    await userEvent.click(card);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("demo", "unblock");
  });

  it("treats an unhealthy live cell as blocked, whatever the file says", async () => {
    const { card, actions } = renderCard(job({ state: "working", live: true, health: "pane gone" }));
    await userEvent.click(card);
    expect(actions.onOpenSteer).toHaveBeenCalledWith("demo", "unblock");
  });

  it("opens the editor for anything that is not live", async () => {
    const { card, actions } = renderCard(job({ state: "draft" }));
    await userEvent.click(card);
    expect(actions.onOpenEditor).toHaveBeenCalledWith("demo");
    expect(actions.onOpenSteer).not.toHaveBeenCalled();
  });

  it("is the ONLY route to the counts' drilldown — the count chips are inert", async () => {
    // The chips' stated affordance is "click the card": they are plain spans,
    // so a click on one falls through to the fallback rather than doing
    // something of its own.
    const { card, actions } = renderCard(
      job({
        state: "done",
        artifacts: [{ name: "00-overview.md", url: "/a/1" }],
        feedback: [{ id: 1, artifact: "a", note: "n", at: 0 }],
      }),
    );
    await userEvent.click(card.querySelector(".ct-chip")!);
    expect(actions.onOpenEditor).toHaveBeenCalledWith("demo");
    expect(actions.onOpenArtifact).not.toHaveBeenCalled();
  });

  it("does not fire from a control inside the card", async () => {
    const { actions } = renderCard(job({ state: "queued" }));
    await userEvent.click(screen.getByTitle("cancel — return to draft (spec is kept)"));
    expect(actions.onDequeue).toHaveBeenCalledWith("demo");
    expect(actions.onOpenEditor).not.toHaveBeenCalled();
  });
});

describe("PR chips", () => {
  it("linkifies http(s) only — job files are hand-editable", () => {
    // esc() stopped attribute breakout on the vanilla side but not a
    // `javascript:` scheme, and JSX renders href="javascript:…" just as
    // happily. The check is the whole defence.
    const { container } = renderCard(
      job({
        prs: [
          { url: "https://github.com/o/r/pull/73", state: "merged" },
          // eslint-disable-next-line no-script-url
          { url: "javascript:alert(1)", state: "open" },
        ],
      }),
    );
    const chips = [...container.querySelectorAll(".pr-chip")];
    expect(chips[0]!.tagName).toBe("A");
    expect(chips[0]).toHaveTextContent("#73 merged");
    expect(chips[0]!.getAttribute("href")).toBe("https://github.com/o/r/pull/73");
    // the dangerous one degrades to a span that still says what it is
    expect(chips[1]!.tagName).toBe("SPAN");
    expect(chips[1]!.getAttribute("href")).toBeNull();
    expect(chips[1]).toHaveTextContent("open");
  });
});

describe("retry", () => {
  it("is offered for a fixable health problem only", () => {
    expect(
      renderCard(job({ health: "zeus stopped answering" })).card.querySelector(".retry"),
    ).not.toBeNull();
    expect(renderCard(job({ health: "cell is gone" })).card.querySelector(".retry")).toBeNull();
    expect(
      renderCard(job({ health: "stuck at the startup prompt" })).card.querySelector(".retry"),
    ).toBeNull();
  });
});

describe("AskUserQuestion", () => {
  it("offers option buttons for a single non-multi question", async () => {
    const j = job({
      state: "blocked",
      live: true,
      question: {
        tool_use_id: "t",
        questions: [
          {
            question: "which store?",
            header: "store",
            multi_select: false,
            options: [
              { label: "sqlite", description: "one file" },
              { label: "files", description: "many" },
            ],
          },
        ],
      },
    });
    const { container, actions } = renderCard(j);
    const opts = [...container.querySelectorAll(".q-opt")];
    expect(opts.map((o) => o.textContent)).toEqual(["1. sqlite", "2. files"]);
    await userEvent.click(opts[1]!);
    expect(actions.onAnswerQuestion).toHaveBeenCalledWith("demo", 2);
    // both die once one is posted — a second digit would be a second answer
    expect((opts[0] as HTMLButtonElement).disabled).toBe(true);
  });

  it("re-arms the options when the answer fails to post", async () => {
    // Vanilla self-healed inside one 3s tick: its disable was a DOM sweep that
    // the next board rebuild undid. A mounted component cannot rely on that —
    // and a failed answer never reaches the pane, so the same question keeps
    // arriving and the block stays mounted, disabled, forever.
    const actions = stubActions();
    actions.onAnswerQuestion = vi.fn(async () => new Error("daemon restarting"));
    const { container } = renderCard(
      job({
        state: "blocked",
        live: true,
        question: {
          tool_use_id: "t",
          questions: [
            {
              question: "which store?",
              header: "store",
              multi_select: false,
              options: [{ label: "sqlite", description: "" }],
            },
          ],
        },
      }),
      actions,
    );
    const opt = container.querySelector<HTMLButtonElement>(".q-opt")!;
    await userEvent.click(opt);
    expect(actions.onAnswerQuestion).toHaveBeenCalledWith("demo", 1);
    expect(container.querySelector<HTMLButtonElement>(".q-opt")!.disabled).toBe(false);
  });

  it("points at the pane for anything a single digit cannot answer", () => {
    const { container } = renderCard(
      job({
        cell: "kraken-demo",
        question: {
          tool_use_id: "t",
          questions: [
            {
              question: "a?",
              header: "a",
              multi_select: true,
              options: [{ label: "x", description: "" }],
            },
          ],
        },
      }),
    );
    expect(container.querySelector(".q-opt")).toBeNull();
    expect(container.querySelector(".q-text")).toHaveTextContent(
      "answer in the pane (k o kraken-demo)",
    );
  });
});
