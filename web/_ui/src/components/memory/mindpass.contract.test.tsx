// mindpass.contract.test.tsx — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 639, the mind pass), slice D (web): the Model
// pane's `nightly` and `proposed` filters (`isProposed`, `matchesFilter`, the
// chips' counts), the `proposed` badge, the `confirm` button wired to
// `onConfirm`, and `SOURCE_LABEL.nightly`.
//
// Written from the frozen contract alone; MentalModelPane.tsx, lib/mm.ts,
// api/client.ts and the hooks were never opened in the working tree. The
// harness (a mocked `MMModel`, a stubbed global `fetch`) is the committed
// mindlayer.contract.test.tsx's.
//
// Dimension: LOGIC — the stated happy paths. Boundaries are athena-edge's.
// Every module-scope identifier declared here is prefixed `mpc`.
//
// Job 674 (mind runs) retired the `proposed` state, its chip, badge and
// confirm button, and folded the `nightly` chip into `pass`; the assertions
// that pinned them were rewritten to the new obligations (slice F) rather
// than left failing: one `pass` chip over source `nightly`, no confirm, and
// `SOURCE_LABEL.nightly` reading `pass`.

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import { MentalModelPane, matchesFilter } from "./MentalModelPane.tsx";

function mpcItem(over: Record<string, unknown>): MMItem {
  return {
    id: 1,
    kind: "note",
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

const mpcProposedTitle = "the trade mapping lives in dispatch groups";
const mpcLiveNightlyTitle = "the cap is twenty per night";
const mpcCaptureTitle = "the daemon owns the render";

const mpcProp = mpcItem({ id: 21, source: "nightly", state: "proposed", title: mpcProposedTitle, content: "proposed body" });
const mpcLiveNightly = mpcItem({ id: 22, source: "nightly", state: "live", title: mpcLiveNightlyTitle, content: "live nightly body" });
const mpcCapture = mpcItem({ id: 23, source: "capture", state: "live", title: mpcCaptureTitle, content: "capture body" });
const mpcItems: MMItem[] = [mpcProp, mpcLiveNightly, mpcCapture];

function mpcJSON(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function mpcRender(items: MMItem[]) {
  const refetch = vi.fn(() => Promise.resolve(true));
  const mm = { items, error: undefined, loaded: true, refetch } as unknown as MMModel;
  const view = render(
    <MentalModelPane
      hidden={false}
      mm={mm}
      onAddNote={() => Promise.resolve()}
      onRemove={() => Promise.resolve()}
      onOpenJob={() => {}}
    />,
  );
  return { ...view, refetch };
}

describe("matchesFilter", () => {
  // slice F: "`pass` (source `nightly`)"
  it("matches the pass filter on source nightly", () => {
    const [prop, liveNightly, capture] = [mpcProp, mpcLiveNightly, mpcCapture];
    const f = matchesFilter as (it: MMItem, f: string) => boolean;
    expect(f(prop, "pass")).toBe(true);
    expect(f(liveNightly, "pass")).toBe(true);
    expect(f(capture, "pass")).toBe(false);
    expect(f(capture, "all")).toBe(true);
  });
});

describe("MentalModelPane — the mind pass", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === "string"
            ? input
            : input instanceof URL
              ? input.href
              : input.url;
        if (url.includes("/api/mm/flags"))
          return Promise.resolve(mpcJSON({ flags: [] }));
        if (url.includes("/api/mm"))
          return Promise.resolve(mpcJSON({ items: mpcItems }));
        return Promise.resolve(mpcJSON({}));
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  // slice F: chips `all, decisions, readings, notes, invalidated, pass`; no
  // `nightly` or `proposed` chip.
  it("renders the pass chip with its count and no nightly or proposed chip", async () => {
    mpcRender(mpcItems);
    await screen.findByText(mpcProposedTitle);
    expect(screen.getByRole("button", { name: "pass 2" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^nightly\b/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^proposed\b/ })).toBeNull();
  });

  // Selecting a chip narrows the rows by `matchesFilter`.
  it("narrows the rows to the pass filter", async () => {
    mpcRender(mpcItems);
    await screen.findByText(mpcProposedTitle);
    fireEvent.click(screen.getByRole("button", { name: "pass 2" }));
    await waitFor(() => {
      expect(screen.queryByText(mpcCaptureTitle)).not.toBeInTheDocument();
    });
    expect(screen.getByText(mpcProposedTitle)).toBeInTheDocument();
    expect(screen.getByText(mpcLiveNightlyTitle)).toBeInTheDocument();
  });

  // slice F: "no confirm button" — not even on a row the server still serves
  // as `proposed`.
  it("renders no proposed badge and no confirm button", async () => {
    const { container } = mpcRender(mpcItems);
    await screen.findByText(mpcProposedTitle);
    expect(container.querySelectorAll("span.mm-proposed").length).toBe(0);
    expect(container.querySelectorAll(".mm-confirm").length).toBe(0);
    expect(screen.queryByRole("button", { name: `confirm ${mpcProposedTitle}` })).toBeNull();
  });

  // slice F: "`SOURCE_LABEL.nightly` renders `pass`".
  it("labels a nightly row's source pass", async () => {
    const only = [mpcLiveNightly];
    const { container } = mpcRender(only);
    await screen.findByText(mpcLiveNightlyTitle);
    const meta = container.querySelectorAll(".mm-row .mm-meta");
    expect(meta.length).toBe(1);
    expect(meta[0]!.textContent).toContain("pass");
    expect(meta[0]!.textContent).not.toContain("nightly");
  });
});
