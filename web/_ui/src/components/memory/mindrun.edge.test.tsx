// mindrun.edge.test.tsx — the EDGE dimension of 50-contract.md slice F (the
// `✓ read` MindButton, the Model pane's runs / pending-marks sections and the
// superseded item meta). Written blind from the frozen contract:
// MindButton.tsx, MentalModelPane.tsx, api/client.ts, api/types.ts and
// hooks/useMM.ts were never read in their edited form. Boundaries and error
// paths only; athena-logic owns the happy paths. Every module-scope
// identifier is prefixed `edgeMR`.

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentType } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as client from "../../api/client.ts";
import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import * as toast from "../overlays/Toast.tsx";
import { MindButton as RealMindButton } from "../chrome/MindButton.tsx";
import { MentalModelPane as RealPane } from "./MentalModelPane.tsx";

vi.mock("../overlays/Toast.tsx", () => ({ showToast: vi.fn(), Toast: () => null }));

vi.mock("../../api/client.ts", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../../api/client.ts")>();
  return {
    ...orig,
    markRead: vi.fn(),
    removeMMMark: vi.fn(() => Promise.resolve()),
  };
});

// Props are cast loosely: the contract freezes behaviour, not every prop.
const MindButton = RealMindButton as unknown as ComponentType<Record<string, unknown>>;
const Pane = RealPane as unknown as ComponentType<Record<string, unknown>>;

const edgeMRMarkRead = client.markRead as unknown as ReturnType<typeof vi.fn>;
const edgeMRRemove = client.removeMMMark as unknown as ReturnType<typeof vi.fn>;
const edgeMRToast = toast.showToast as unknown as ReturnType<typeof vi.fn>;

let edgeMRMarksPayload: unknown[] = [];
let edgeMRFetch: ReturnType<typeof vi.fn>;

function edgeMRJSON(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function edgeMRURL(input: RequestInfo | URL): string {
  return typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
}

function edgeMRItem(over: Record<string, unknown>): MMItem {
  return {
    id: 1,
    kind: "note",
    source: "capture",
    slug: "",
    source_ref: "",
    content: "body",
    context: "",
    created_at: Date.now() - 60_000,
    episode_id: 0,
    title: "a title",
    scope: "universal",
    repo: "",
    dir: "",
    quote: "",
    thread_session: "",
    revised_at: 0,
    ...over,
  } as unknown as MMItem;
}

function edgeMRRenderPane(items: MMItem[]) {
  const mm = {
    items,
    error: undefined,
    loaded: true,
    refetch: () => Promise.resolve(true),
  } as unknown as MMModel;
  return render(
    <Pane
      hidden={false}
      mm={mm}
      onAddNote={() => Promise.resolve()}
      onRemove={() => Promise.resolve()}
      onOpenJob={() => {}}
    />,
  );
}

/** Select the text of a node inside a `data-mind-*` artifact surface. */
function edgeMRSelectArtifactText(text: string) {
  const host = document.createElement("div");
  host.innerHTML = `<div data-mind-kind="artifact" data-mind-slug="edge-job" data-mind-home="" data-mind-name="00-overview.md" data-mind-sha="abc"><p id="edge-p">${text}</p></div>`;
  document.body.appendChild(host);
  const p = host.querySelector("#edge-p") as HTMLElement;
  const range = document.createRange();
  range.selectNodeContents(p);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
  document.dispatchEvent(new Event("selectionchange"));
  fireEvent.mouseUp(p);
}

function edgeMRReadButton(): HTMLElement {
  return screen.getByRole("button", { name: /✓\s*read/ });
}

beforeEach(() => {
  edgeMRMarksPayload = [];
  edgeMRMarkRead.mockReset();
  edgeMRRemove.mockReset();
  edgeMRRemove.mockImplementation(() => Promise.resolve());
  edgeMRToast.mockReset();
  edgeMRFetch = vi.fn((input: RequestInfo | URL) => {
    const url = edgeMRURL(input);
    if (url.includes("/api/mm/marks")) return Promise.resolve(edgeMRJSON({ marks: edgeMRMarksPayload }));
    if (url.includes("/api/mm/runs")) return Promise.resolve(edgeMRJSON({ runs: [] }));
    if (url.includes("/api/mm/flags")) return Promise.resolve(edgeMRJSON({ flags: [] }));
    if (url.includes("/api/mm")) return Promise.resolve(edgeMRJSON({ items: [] }));
    return Promise.resolve(edgeMRJSON({}));
  });
  vi.stubGlobal("fetch", edgeMRFetch);
  // a remove control may confirm first; never block on a jsdom dialog
  vi.stubGlobal("confirm", () => true);
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.getSelection()?.removeAllRanges();
  document.body.innerHTML = "";
});

describe("MindButton ✓ read — edges", () => {
  // "on press with a pending selection calls `markRead`" — with no selection
  // there is nothing to post: no markRead, no request, no toast claiming a mark.
  it("posts nothing when nothing is selected", async () => {
    render(<MindButton />);
    fireEvent.mouseDown(edgeMRReadButton());
    fireEvent.click(edgeMRReadButton());
    await new Promise((r) => setTimeout(r, 20));
    expect(edgeMRMarkRead).not.toHaveBeenCalled();
    const posted = edgeMRFetch.mock.calls.filter(([u]) => /\/api\/mm\/(mark|capture)/.test(edgeMRURL(u as RequestInfo)));
    expect(posted).toHaveLength(0);
    for (const [msg] of edgeMRToast.mock.calls) expect(String(msg)).not.toMatch(/marked for the next mind run/);
  });

  // "on error the toast carries the server's message" — and never the
  // success toast.
  it("shows the server's message when markRead fails", async () => {
    edgeMRMarkRead.mockImplementation(() => Promise.reject(new Error("text is too long (max 32000 characters)")));
    render(<MindButton />);
    edgeMRSelectArtifactText("highlighted words for the pass");
    await waitFor(() => expect(edgeMRReadButton()).not.toBeDisabled());
    fireEvent.mouseDown(edgeMRReadButton());
    fireEvent.click(edgeMRReadButton());
    await waitFor(() => expect(edgeMRMarkRead).toHaveBeenCalled());
    await waitFor(() =>
      expect(edgeMRToast.mock.calls.some(([m]) => String(m).includes("text is too long (max 32000 characters)"))).toBe(true),
    );
    for (const [msg] of edgeMRToast.mock.calls) expect(String(msg)).not.toMatch(/marked for the next mind run/);
  });
});

describe("MentalModelPane runs and marks — edges", () => {
  // "the empty-state copy names `✓ read` and no thread" — zero items, zero
  // runs, zero marks.
  it("names ✓ read and no thread in the empty state", async () => {
    edgeMRRenderPane([]);
    const hits = await screen.findAllByText(/✓\s*read/);
    expect(hits.length).toBeGreaterThan(0);
    for (const el of hits) expect(el.textContent ?? "").not.toMatch(/thread/i);
  });

  // "an item's meta line shows … `superseded by #N` when `superseded_by` (the
  // row renders dead as today)".
  it("renders a superseded item dead with its successor id", async () => {
    edgeMRRenderPane([
      edgeMRItem({
        id: 5,
        title: "the old claim",
        invalidated_at: 1_770_000_000_000,
        invalid_reason: "superseded",
        invalid_detail: "run 3: newer words",
        superseded_by: 9,
      }),
      edgeMRItem({ id: 9, title: "the new claim", source: "nightly", run_id: 3, supersedes: 5 }),
    ]);
    // dead rows are guaranteed listed under the invalidated chip
    fireEvent.click(screen.getByRole("button", { name: /^invalidated\b/ }));
    expect(await screen.findByText(/superseded by #9/)).toBeInTheDocument();
    expect(screen.getByText("the old claim")).toBeInTheDocument();
  });

  // "a `pending marks` section from `useMMMarks` with a `remove` button per
  // mark" — the remove control calls `removeMMMark` with the mark's id.
  it("calls removeMMMark with the mark id", async () => {
    edgeMRMarksPayload = [
      {
        id: 42,
        job_id: 0,
        slug: "",
        text: "marked words to drop",
        source: { kind: "manual" },
        created_at: Date.now() - 1000,
        consumed_run: 0,
        consumed_at: 0,
      },
    ];
    edgeMRRenderPane([]);
    const text = await screen.findByText(/marked words to drop/);
    let row: HTMLElement | null = text;
    let btn: HTMLElement | null = null;
    while (row && !btn) {
      btn = Array.from(row.querySelectorAll("button")).find((b) => /remove/i.test(b.textContent ?? "") || /remove/i.test(b.getAttribute("aria-label") ?? "")) ?? null;
      row = row.parentElement;
    }
    expect(btn).not.toBeNull();
    fireEvent.click(btn as HTMLElement);
    await waitFor(() => expect(edgeMRRemove).toHaveBeenCalledWith(42));
  });
});
