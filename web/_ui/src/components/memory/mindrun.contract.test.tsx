// mindrun.contract.test.tsx — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 674, mind runs), slice F (web): `MindButton`
// posting `markRead` and toasting instead of opening a thread, and the Model
// pane's `pass` chip, runs section, pending-marks section and the item meta
// line (`run #R`, `supersedes #M`, `superseded by #N`).
//
// Written from the frozen contract alone; MindButton.tsx, MentalModelPane.tsx,
// api/client.ts, api/types.ts and hooks/useMM.ts were never opened. The
// harness (a mocked `MMModel`, `vi.mock` of the client and Toast modules, a
// stubbed global `fetch` for everything else) is the committed tests'.
//
// Dimension: LOGIC — the stated happy paths. Every module-scope identifier
// declared here is prefixed `contract`.

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentType } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../overlays/Toast.tsx", () => ({ showToast: vi.fn(), Toast: () => null }));
vi.mock("../../api/client.ts", async (importOriginal) => {
  const orig = (await importOriginal()) as Record<string, unknown>;
  return {
    ...orig,
    markRead: vi.fn(),
    fetchMMRuns: vi.fn(),
    fetchMMRun: vi.fn(),
    fetchMMMarks: vi.fn(),
    removeMMMark: vi.fn(),
    replayMMRun: vi.fn(),
  };
});

import * as client from "../../api/client.ts";
import type { WebMindRun, WebMMMark } from "../../api/types.ts";
import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import { MindButton } from "../chrome/MindButton.tsx";
import { showToast } from "../overlays/Toast.tsx";
import { MentalModelPane } from "./MentalModelPane.tsx";

type contractFn = ReturnType<typeof vi.fn>;
const contractClient = client as unknown as {
  markRead: contractFn;
  fetchMMRuns: contractFn;
  fetchMMRun: contractFn;
  fetchMMMarks: contractFn;
  removeMMMark: contractFn;
  replayMMRun: contractFn;
};
const contractToast = showToast as unknown as contractFn;

function contractJSON(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function contractItem(over: Record<string, unknown>): MMItem {
  return {
    id: 1,
    kind: "decision",
    source: "capture",
    slug: "",
    source_ref: "",
    content: "",
    context: "",
    created_at: Date.now() - 60_000,
    episode_id: 0,
    title: "",
    scope: "universal",
    repo: "",
    dir: "",
    quote: "",
    thread_session: "",
    revised_at: 0,
    state: "live",
    ...over,
  } as unknown as MMItem;
}

function contractRun(over: Partial<WebMindRun>): WebMindRun {
  return {
    id: 1,
    job_id: 0,
    slug: "",
    trigger: "done",
    since: Date.now() - 3_600_000,
    until: Date.now() - 60_000,
    state: "done",
    created_at: Date.now() - 120_000,
    started_at: Date.now() - 110_000,
    finished_at: Date.now() - 100_000,
    note: "",
    prompt_sha: "abcdef0123456789",
    model: "claude-opus-5",
    in_tokens: 100,
    out_tokens: 20,
    bundle_bytes: 4096,
    added: 0,
    superseded: 0,
    retired: 0,
    flagged: 0,
    excluded: 0,
    folded: 0,
    suppressed: 0,
    refused: 0,
    source_run: 0,
    has_bundle: true,
    ...over,
  } as unknown as WebMindRun;
}

function contractMark(over: Partial<WebMMMark>): WebMMMark {
  return {
    id: 1,
    job_id: 0,
    slug: "",
    text: "",
    source: { kind: "manual" },
    created_at: Date.now() - 30_000,
    consumed_run: 0,
    consumed_at: 0,
    ...over,
  } as unknown as WebMMMark;
}

const contractJobRun = contractRun({ id: 41, job_id: 7, slug: "runs-job", trigger: "done", added: 2, superseded: 1, retired: 0, excluded: 3 });
const contractDailyRun = contractRun({ id: 42, job_id: 0, slug: "", trigger: "daily", added: 0, superseded: 0, retired: 1, excluded: 0 });
const contractPendingText = "a passage marked for the mind pass";
const contractPending = contractMark({ id: 77, job_id: 7, slug: "runs-job", text: contractPendingText });

function contractRenderPane(items: MMItem[]) {
  const refetch = vi.fn(() => Promise.resolve(true));
  const mm = { items, error: undefined, loaded: true, refetch } as unknown as MMModel;
  return render(
    <MentalModelPane
      hidden={false}
      mm={mm}
      onAddNote={() => Promise.resolve()}
      onRemove={() => Promise.resolve()}
      onOpenJob={() => {}}
    />,
  );
}

function contractStubFetch() {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.includes("/api/mm/flags")) return Promise.resolve(contractJSON({ flags: [] }));
    if (url.includes("/api/mm/runs")) return Promise.resolve(contractJSON({ runs: [contractJobRun, contractDailyRun] }));
    if (url.includes("/api/mm/marks")) return Promise.resolve(contractJSON({ marks: [contractPending] }));
    if (url.includes("/api/mm")) return Promise.resolve(contractJSON({ items: [] }));
    return Promise.resolve(contractJSON({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  contractClient.fetchMMRuns.mockResolvedValue({ runs: [contractJobRun, contractDailyRun] });
  contractClient.fetchMMMarks.mockResolvedValue({ marks: [contractPending] });
  contractClient.removeMMMark.mockResolvedValue(undefined);
  contractClient.fetchMMRun.mockImplementation((id: number, blob?: string) =>
    Promise.resolve({
      run: id === 41 ? contractJobRun : contractDailyRun,
      blob: blob === "answer" ? '{"ops":[],"excluded":[{"rung":"hedge","quote":"ANSWER-QUOTE","why":"hedged"}],"coverage":{}}' : "# REPORT-HEADLINE for the run",
    }),
  );
  contractClient.replayMMRun.mockResolvedValue({
    source_run: 41,
    old_prompt_sha: "aaa",
    new_prompt_sha: "bbb",
    old: { ops: [], excluded: [], coverage: {} },
    new: { ops: [], excluded: [], coverage: {} },
    diff: {
      ops_added: [{ op: "add", target: 0, quote: "REPLAY-ADDED-QUOTE", note: "", item: null }],
      ops_lost: [],
      exclusions_changed: [],
    },
    new_run: 0,
  });
  contractClient.markRead.mockResolvedValue({ id: 5, pending: 3, folded: false });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  window.getSelection()?.removeAllRanges();
  document.body.innerHTML = "";
});

// ---------------------------------------------------------------------------
// MindButton
// ---------------------------------------------------------------------------

function contractSelectPassage(): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = `<div data-mind-kind="artifact" data-mind-slug="mark-job" data-mind-name="50-contract.md"><p id="contract-p">the selected passage</p></div>`;
  document.body.appendChild(host);
  const p = host.querySelector("#contract-p") as HTMLElement;
  const range = document.createRange();
  range.selectNodeContents(p);
  const sel = window.getSelection()!;
  sel.removeAllRanges();
  sel.addRange(range);
  fireEvent(document, new Event("selectionchange"));
  fireEvent.mouseUp(p);
  return p;
}

function contractMindButton(): HTMLElement {
  return screen.getByRole("button", { name: /✓ read/ });
}

describe("MindButton — ✓ read", () => {
  // "Label text `✓ read` with the `⌘⇧M` key hint (Q11)"; titles no longer
  // mention a thread.
  it("renders the ✓ read label with the ⌘⇧M hint and no thread wording", () => {
    contractStubFetch();
    const C = MindButton as unknown as ComponentType<Record<string, unknown>>;
    const { container } = render(<C />);
    const btn = contractMindButton();
    expect(btn.textContent).toContain("✓ read");
    expect(container.textContent).toContain("⌘⇧M");
    expect(btn.getAttribute("title") ?? "").not.toMatch(/thread/i);
  });

  // "on press with a pending selection calls markRead and then
  // showToast("✓ marked for the mind pass · N pending") where N is pending,
  // (review round 1 renamed the term: the toast now reads
  // "✓ marked for the next mind run · N pending"),
  // clears the selection, and opens NO overlay".
  it("posts markRead with slug, text and source, toasts the pending count, clears the selection", async () => {
    const fetchMock = contractStubFetch();
    const C = MindButton as unknown as ComponentType<Record<string, unknown>>;
    render(<C />);
    contractSelectPassage();
    const btn = contractMindButton();
    await act(async () => {
      fireEvent.mouseDown(btn);
      fireEvent.click(btn);
    });
    await waitFor(() => expect(contractClient.markRead).toHaveBeenCalledTimes(1));
    const [slug, text, source] = contractClient.markRead.mock.calls[0]!;
    expect(slug).toBe("mark-job");
    expect(text).toBe("the selected passage");
    expect(source).toEqual(expect.objectContaining({ kind: "artifact", name: "50-contract.md" }));
    await waitFor(() => expect(contractToast).toHaveBeenCalledWith("✓ marked for the next mind run · 3 pending"));
    expect(window.getSelection()?.toString() ?? "").toBe("");
    // no thread opened: no request to the threads route, no dialog
    for (const call of fetchMock.mock.calls) {
      const u = String(call[0]);
      expect(u).not.toContain("/api/threads");
      expect(u).not.toContain("/api/mm/capture");
    }
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // "on error the toast carries the server's message".
  it("toasts the server's message when markRead fails", async () => {
    contractStubFetch();
    contractClient.markRead.mockRejectedValueOnce(new Error("SERVER-SAYS-NO"));
    const C = MindButton as unknown as ComponentType<Record<string, unknown>>;
    render(<C />);
    contractSelectPassage();
    const btn = contractMindButton();
    await act(async () => {
      fireEvent.mouseDown(btn);
      fireEvent.click(btn);
    });
    await waitFor(() => expect(contractToast).toHaveBeenCalled());
    const msgs = contractToast.mock.calls.map((c) => String(c[0]));
    expect(msgs.some((m) => m.includes("SERVER-SAYS-NO"))).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// MentalModelPane
// ---------------------------------------------------------------------------

function contractChip(name: string): HTMLElement | null {
  return (
    screen
      .queryAllByRole("button")
      .find((b) => (b.textContent ?? "").trim().toLowerCase().startsWith(name)) ?? null
  );
}

// among every element matching `text`, the one whose smallest ancestor
// containing `needle` is tightest — i.e. the match that sits inside a run row
// (the contract pins what a row renders, not that the slug appears once).
async function contractFindInRow(text: RegExp, needle: string): Promise<HTMLElement> {
  const els = (await screen.findAllByText(text)) as HTMLElement[];
  let best: HTMLElement | null = null;
  let bestLen = Infinity;
  for (const el of els) {
    let cur: HTMLElement | null = el;
    while (cur && !(cur.textContent ?? "").includes(needle)) cur = cur.parentElement;
    if (cur && (cur.textContent ?? "").length < bestLen) {
      best = el;
      bestLen = (cur.textContent ?? "").length;
    }
  }
  if (!best) throw new Error(`no ${text} match sits in an element containing ${needle}`);
  return best;
}

// the smallest ancestor of `el` whose text contains `needle`
function contractRowOf(el: HTMLElement, needle: string): HTMLElement {
  let cur: HTMLElement | null = el;
  while (cur && !(cur.textContent ?? "").includes(needle)) cur = cur.parentElement;
  if (!cur) throw new Error(`no ancestor of ${el.textContent} contains ${needle}`);
  return cur;
}

describe("MentalModelPane — mind runs", () => {
  beforeEach(() => {
    contractStubFetch();
  });

  // Filter chips `all, decisions, readings, notes, invalidated, pass`; "no
  // nightly or proposed chip and no confirm button".
  it("renders the pass chip, no nightly/proposed chip and no confirm button", async () => {
    contractRenderPane([
      contractItem({ id: 1, source: "nightly", title: "a pass-made claim", run_id: 41 }),
      contractItem({ id: 2, source: "capture", title: "a captured claim" }),
    ]);
    await screen.findByText("a pass-made claim");
    for (const chip of ["all", "decisions", "readings", "notes", "invalidated", "pass"]) {
      expect(contractChip(chip), `chip ${chip}`).not.toBeNull();
    }
    expect(contractChip("nightly")).toBeNull();
    expect(contractChip("proposed")).toBeNull();
    expect(screen.queryByRole("button", { name: /^confirm$/i })).toBeNull();
    fireEvent.click(contractChip("pass")!);
    await waitFor(() => expect(screen.queryByText("a captured claim")).toBeNull());
    expect(screen.getByText("a pass-made claim")).toBeTruthy();
  });

  // "a runs section rendering useMMRuns rows (time, slug or `daily catch-all`,
  // trigger, counts `+added · superseded rewritten · retired · excluded`)".
  it("renders a runs section with each run's slug, trigger and counts", async () => {
    contractRenderPane([]);
    const slug = await contractFindInRow(/runs-job/, "excluded");
    const row = contractRowOf(slug, "excluded");
    const text = (row.textContent ?? "").replace(/\s+/g, " ");
    expect(text).toMatch(/\+2 · 1 rewritten · 0 retired · 3 excluded/);
    expect(text).toContain("done");
    const daily = await contractFindInRow(/daily catch-all/, "excluded");
    const drow = contractRowOf(daily, "excluded");
    const dtext = (drow.textContent ?? "").replace(/\s+/g, " ");
    expect(dtext).toMatch(/\+0 · 0 rewritten · 1 retired · 0 excluded/);
    expect(dtext).toContain("daily");
  });

  // "an expandable detail that fetches report and answer through fetchMMRun
  // and a replay button calling replayMMRun(id) (dry run) and rendering the
  // diff".
  it("expands a run to its report and answer, and replays it as a dry run", async () => {
    contractRenderPane([]);
    const slug = await contractFindInRow(/runs-job/, "excluded");
    fireEvent.click(slug);
    await waitFor(() => {
      const blobs = contractClient.fetchMMRun.mock.calls.filter((c) => c[0] === 41).map((c) => c[1]);
      expect(blobs).toContain("report");
      expect(blobs).toContain("answer");
    });
    await screen.findByText(/REPORT-HEADLINE/);
    const replays = await screen.findAllByRole("button", { name: /replay/i });
    const detail = contractRowOf(slug, "REPORT-HEADLINE");
    const replay = replays.find((b) => detail.contains(b)) ?? replays[0];
    fireEvent.click(replay!);
    await waitFor(() => expect(contractClient.replayMMRun).toHaveBeenCalled());
    const call = contractClient.replayMMRun.mock.calls[0]!;
    expect(call[0]).toBe(41);
    expect(call[1] ?? false).toBe(false);
    await screen.findByText(/REPLAY-ADDED-QUOTE/);
  });

  // "a pending marks section from useMMMarks with a remove button per mark".
  it("renders pending marks with a remove control that calls removeMMMark", async () => {
    contractRenderPane([]);
    const mark = await screen.findByText(new RegExp(contractPendingText));
    const row = contractRowOf(mark as HTMLElement, "remove");
    const remove = Array.from(row.querySelectorAll("button")).find((b) => /remove/i.test(b.textContent ?? ""));
    expect(remove).toBeTruthy();
    fireEvent.click(remove!);
    await waitFor(() => expect(contractClient.removeMMMark).toHaveBeenCalledWith(77));
  });

  // "an item's meta line shows run #R when run_id, supersedes #M when
  // supersedes, and superseded by #N when superseded_by (the row renders dead
  // as today); SOURCE_LABEL.nightly renders pass".
  it("renders run #R, supersedes #M and the pass source label on an item", async () => {
    contractRenderPane([
      contractItem({ id: 9, source: "nightly", title: "the replacement claim", run_id: 41, supersedes: 3 }),
    ]);
    const title = await screen.findByText("the replacement claim");
    const row = contractRowOf(title as HTMLElement, "run #41");
    const text = row.textContent ?? "";
    expect(text).toContain("supersedes #3");
    expect(text).toMatch(/\bpass\b/);
    expect(text).not.toMatch(/\bnightly\b/);
  });

  it("renders superseded by #N on a superseded row", async () => {
    contractRenderPane([
      contractItem({
        id: 3,
        source: "capture",
        title: "the superseded claim",
        superseded_by: 9,
        invalidated_at: Date.now() - 10_000,
        invalid_reason: "superseded",
        invalid_detail: "run 41: the new quote",
      }),
    ]);
    let title = screen.queryByText("the superseded claim");
    if (!title) {
      fireEvent.click(contractChip("invalidated")!);
      title = await screen.findByText("the superseded claim");
    }
    const row = contractRowOf(title as HTMLElement, "superseded by #9");
    expect(row.textContent).toContain("superseded by #9");
  });

  // "the empty-state copy names ✓ read and no thread".
  it("names ✓ read and no thread in the empty state", async () => {
    contractClient.fetchMMRuns.mockResolvedValue({ runs: [] });
    contractClient.fetchMMMarks.mockResolvedValue({ marks: [] });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        if (url.includes("/api/mm/flags")) return Promise.resolve(contractJSON({ flags: [] }));
        if (url.includes("/api/mm/runs")) return Promise.resolve(contractJSON({ runs: [] }));
        if (url.includes("/api/mm/marks")) return Promise.resolve(contractJSON({ marks: [] }));
        return Promise.resolve(contractJSON({ items: [] }));
      }),
    );
    // The empty state is the copy that exists only while the model holds no
    // items: every `✓ read` match in an empty pane that is gone once the pane
    // holds an item (the runs/marks sections render the same either way).
    const view = contractRenderPane([]);
    const emptyMatches = (await screen.findAllByText(/✓ read/)).map((e) => e.textContent ?? "");
    view.unmount();
    contractRenderPane([contractItem({ id: 5, title: "one item so the pane is not empty" })]);
    await screen.findByText("one item so the pane is not empty");
    const fullMatches = screen.queryAllByText(/✓ read/).map((e) => e.textContent ?? "");
    const emptyState = emptyMatches.filter((t) => !fullMatches.includes(t));
    expect(emptyState.length).toBe(1);
    expect(emptyState[0]).toContain("✓ read");
    expect(emptyState[0]).not.toMatch(/thread/i);
  });
});
