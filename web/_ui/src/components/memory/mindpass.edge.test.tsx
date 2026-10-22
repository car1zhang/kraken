// mindpass.edge.test.tsx — the EDGE dimension of 50-contract.md slice D's pane
// half (MentalModelPane's `proposed`/`nightly` chips, `isProposed`,
// `matchesFilter`, the `confirm` button). Written blind from the frozen
// contract: MentalModelPane.tsx, lib/mm.ts, api/client.ts and the hooks were
// read only as committed at HEAD (before any implementer edit). Boundaries
// only; athena-logic owns the happy paths. Every module-scope identifier is
// prefixed `mpe`.

import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import { MentalModelPane, isProposed, matchesFilter } from "./MentalModelPane.tsx";

function mpeItem(over: Record<string, unknown>): MMItem {
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

function mpeJSON(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function mpeRender(items: MMItem[], onConfirm = vi.fn(() => Promise.resolve())) {
  const mm = {
    items,
    error: undefined,
    loaded: true,
    refetch: () => Promise.resolve(true),
  } as unknown as MMModel;
  render(
    <MentalModelPane
      hidden={false}
      mm={mm}
      onAddNote={() => Promise.resolve()}
      onRemove={() => Promise.resolve()}
      onConfirm={onConfirm}
      onOpenJob={() => {}}
    />,
  );
  return onConfirm;
}

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.includes("/api/mm/flags")) return Promise.resolve(mpeJSON({ flags: [] }));
      if (url.includes("/api/mm")) return Promise.resolve(mpeJSON({ items: [] }));
      return Promise.resolve(mpeJSON({}));
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("isProposed / matchesFilter — edges", () => {
  // "`isProposed = (it) => it.state === "proposed"`" — an item with NO state
  // key (every pre-v37 client payload) is not proposed.
  it("treats an item with no state key as not proposed", () => {
    const it0 = mpeItem({});
    expect("state" in (it0 as object)).toBe(false);
    expect(isProposed(it0)).toBe(false);
    expect(matchesFilter(it0, "proposed")).toBe(false);
  });

  // Strict equality: no case folding, no trimming.
  it("does not read a near-miss state as proposed", () => {
    for (const state of ["Proposed", "PROPOSED", " proposed", "live", ""]) {
      expect(isProposed(mpeItem({ state }))).toBe(false);
      expect(matchesFilter(mpeItem({ state }), "proposed")).toBe(false);
    }
  });

  // "`nightly` matches `it.source === "nightly"`" — a capture row is not
  // nightly, even when it is proposed.
  it("does not match a capture row under the nightly filter", () => {
    expect(matchesFilter(mpeItem({ source: "capture" }), "nightly")).toBe(false);
    expect(matchesFilter(mpeItem({ source: "capture", state: "proposed" }), "nightly")).toBe(false);
  });
});

describe("MentalModelPane confirm — edges", () => {
  // "a proposed row renders … a `confirm` button" — so a LIVE row renders none.
  it("renders no confirm button on a live row", async () => {
    mpeRender([mpeItem({ id: 21, title: "a live claim", state: "live" })]);
    expect(await screen.findByText("a live claim")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "confirm a live claim" })).toBeNull();
  });

  // "… enabled only while live and proposed" — a proposed row that has since
  // been invalidated renders the button disabled, and clicking it calls
  // nothing.
  it("disables confirm on a proposed-but-invalidated row", async () => {
    const onConfirm = mpeRender([
      mpeItem({ id: 22, title: "a dropped proposal", state: "proposed", source: "nightly", invalidated_at: 1_770_000_000_000 }),
    ]);
    // the invalidated chip is where a dead row is guaranteed to be listed
    fireEvent.click(screen.getByRole("button", { name: /^invalidated\b/ }));
    const btn = await screen.findByRole("button", { name: "confirm a dropped proposal" });
    expect(btn).toBeDisabled();
    fireEvent.click(btn);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  // "counts for both chips" — with no proposed rows the proposed chip counts 0.
  it("counts zero on the proposed chip when nothing is proposed", () => {
    mpeRender([
      mpeItem({ id: 23, title: "live one", state: "live" }),
      mpeItem({ id: 24, title: "stateless one" }),
      mpeItem({ id: 25, title: "nightly but live", source: "nightly", state: "live" }),
    ]);
    expect(screen.getByRole("button", { name: /^proposed\s+0$/ })).toBeInTheDocument();
  });
});
