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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import { MentalModelPane, isProposed, matchesFilter } from "./MentalModelPane.tsx";

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

function mpcRender(items: MMItem[], onConfirm = vi.fn(() => Promise.resolve())) {
  const refetch = vi.fn(() => Promise.resolve(true));
  const mm = { items, error: undefined, loaded: true, refetch } as unknown as MMModel;
  const view = render(
    <MentalModelPane
      hidden={false}
      mm={mm}
      onAddNote={() => Promise.resolve()}
      onRemove={() => Promise.resolve()}
      onConfirm={onConfirm}
      onOpenJob={() => {}}
    />,
  );
  return { ...view, onConfirm, refetch };
}

describe("isProposed / matchesFilter", () => {
  // "`export const isProposed = (it: MMItem) => it.state === "proposed"`"
  it("reads proposed off the state", () => {
    expect(isProposed(mpcItem({ state: "proposed" }))).toBe(true);
    expect(isProposed(mpcItem({ state: "live" }))).toBe(false);
    expect(isProposed(mpcItem({ state: undefined }))).toBe(false);
  });

  // "`nightly` matches `it.source === "nightly"`, `proposed` matches
  // `isProposed(it)`"
  it("matches the nightly and proposed filters", () => {
    const [prop, liveNightly, capture] = [mpcProp, mpcLiveNightly, mpcCapture];
    const f = matchesFilter as (it: MMItem, f: string) => boolean;
    expect(f(prop, "nightly")).toBe(true);
    expect(f(liveNightly, "nightly")).toBe(true);
    expect(f(capture, "nightly")).toBe(false);
    expect(f(prop, "proposed")).toBe(true);
    expect(f(liveNightly, "proposed")).toBe(false);
    expect(f(capture, "proposed")).toBe(false);
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

  // `FILTERS` gains `{key:"nightly", label:"nightly"}` and `{key:"proposed",
  // label:"proposed"}`; "counts for both chips".
  it("renders the nightly and proposed chips with their counts", async () => {
    mpcRender(mpcItems);
    await screen.findByText(mpcProposedTitle);
    expect(screen.getByRole("button", { name: "nightly 2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "proposed 1" })).toBeInTheDocument();
  });

  // Selecting a chip narrows the rows by `matchesFilter`.
  it("narrows the rows to the proposed filter", async () => {
    mpcRender(mpcItems);
    await screen.findByText(mpcProposedTitle);
    fireEvent.click(screen.getByRole("button", { name: "proposed 1" }));
    await waitFor(() => {
      expect(screen.queryByText(mpcCaptureTitle)).not.toBeInTheDocument();
    });
    expect(screen.getByText(mpcProposedTitle)).toBeInTheDocument();
    expect(screen.queryByText(mpcLiveNightlyTitle)).not.toBeInTheDocument();
  });

  // "a proposed row renders a `proposed` badge (`<span
  // className="mm-proposed">proposed</span>`) and a `confirm` button
  // (`className="mm-act mm-confirm"`, `aria-label=\`confirm ${title}\``) …,
  // calling `onConfirm(id)` then refetching".
  it("renders the badge and confirm on a proposed row and confirms by id", async () => {
    const { container, onConfirm, refetch } = mpcRender(mpcItems);
    await screen.findByText(mpcProposedTitle);

    const badges = container.querySelectorAll("span.mm-proposed");
    expect(badges.length).toBe(1);
    expect(badges[0]!.textContent).toBe("proposed");

    const btn = screen.getByRole("button", { name: `confirm ${mpcProposedTitle}` });
    expect(btn).toHaveClass("mm-act");
    expect(btn).toHaveClass("mm-confirm");
    expect(btn).toBeEnabled();
    expect(screen.queryByRole("button", { name: `confirm ${mpcLiveNightlyTitle}` })).toBeNull();
    expect(screen.queryByRole("button", { name: `confirm ${mpcCaptureTitle}` })).toBeNull();
    expect(container.querySelectorAll(".mm-confirm").length).toBe(1);

    fireEvent.click(btn);
    await waitFor(() => {
      expect(onConfirm).toHaveBeenCalledWith(21);
    });
    expect(onConfirm).toHaveBeenCalledTimes(1);
    // "then refetching" is not asserted: which refetch (mm.refetch, the
    // filtered instance, or the integrator's mutate inside onConfirm) is not
    // frozen — see the report's Silences.
    void refetch;
  });

  // `SOURCE_LABEL.nightly = "nightly"`.
  it("labels a nightly row's source nightly", async () => {
    const only = [mpcLiveNightly];
    const { container } = mpcRender(only);
    await screen.findByText(mpcLiveNightlyTitle);
    const meta = container.querySelectorAll(".mm-meta");
    expect(meta.length).toBe(1);
    expect(meta[0]!.textContent).toContain("nightly");
  });
});
