// mindpass.edge.test.tsx — the EDGE dimension of 50-contract.md slice D's pane
// half (MentalModelPane's `proposed`/`nightly` chips, `isProposed`,
// `matchesFilter`, the `confirm` button). Written blind from the frozen
// contract: MentalModelPane.tsx, lib/mm.ts, api/client.ts and the hooks were
// read only as committed at HEAD (before any implementer edit). Boundaries
// only; athena-logic owns the happy paths. Every module-scope identifier is
// prefixed `mpe`.
//
// Job 674 (mind runs) retired the `proposed` chip and the confirm button and
// renamed the `nightly` chip `pass`; the assertions pinning the retired
// behaviour were rewritten to the new obligations (slice F).

import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import { MentalModelPane, matchesFilter } from "./MentalModelPane.tsx";

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

function mpeRender(items: MMItem[]) {
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
      onOpenJob={() => {}}
    />,
  );
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

describe("matchesFilter — edges", () => {
  // `pass` keys on the source alone: the state no longer matters.
  it("matches pass on source nightly whatever the state", () => {
    for (const state of ["proposed", "live", "", undefined]) {
      expect(matchesFilter(mpeItem({ source: "nightly", state }), "pass")).toBe(true);
    }
  });

  // A capture row is not a pass row, even when it is proposed.
  it("does not match a capture row under the pass filter", () => {
    expect(matchesFilter(mpeItem({ source: "capture" }), "pass")).toBe(false);
    expect(matchesFilter(mpeItem({ source: "capture", state: "proposed" }), "pass")).toBe(false);
  });
});

describe("MentalModelPane — no confirm, pass chip edges", () => {
  // An invalidated row the server still serves as proposed renders no
  // confirm button either.
  it("renders no confirm on a proposed-but-invalidated row", async () => {
    mpeRender([
      mpeItem({ id: 22, title: "a dropped proposal", state: "proposed", source: "nightly", invalidated_at: 1_770_000_000_000 }),
    ]);
    fireEvent.click(screen.getByRole("button", { name: /^invalidated\b/ }));
    expect(await screen.findByText("a dropped proposal")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "confirm a dropped proposal" })).toBeNull();
  });

  // With no pass rows the pass chip counts 0.
  it("counts zero on the pass chip when no run wrote anything", () => {
    mpeRender([
      mpeItem({ id: 23, title: "live one", state: "live" }),
      mpeItem({ id: 24, title: "stateless one" }),
    ]);
    expect(screen.getByRole("button", { name: /^pass\s+0$/ })).toBeInTheDocument();
  });
});
