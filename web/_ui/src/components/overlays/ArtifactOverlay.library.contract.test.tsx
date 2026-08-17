import { render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob, WebLibraryItem } from "../../api/types.ts";
import { VIEWS, useHashRoute } from "../../hooks/useHashRoute.ts";
import { makeJob } from "../../test/fixtures.ts";

// ArtifactOverlay.library.contract.test.tsx — athena-logic's BLIND contract
// tests for the two Slice B obligations that live in components rather than in
// the pure module: the `☆ save` / `★ saved` toggle in the overlay head, and the
// graph → memory rename normalization.
//
// Authored from 50-contract.md (re-read after the join amendment to the save
// toggle row) plus two PERMITTED pre-existing test files —
// ArtifactOverlay.test.tsx for the render/props idiom and useHashRoute.test.tsx
// for the route harness. No implementation source was read: not
// ArtifactOverlay.tsx, not useHashRoute.ts, not lib/library.ts, not
// api/client.ts. Rendering the components here is execution, not reading.
//
// Dimension: CONTRACT — the stated states and their happy paths. Where the
// contract is silent this file asserts nothing and the silence is disclosed in
// the run report.
//
// Every file-scope identifier is prefixed `contract`.
//
// The client module is mocked by SPREADING the real one and overriding nothing
// that matters, rather than by a wholesale factory: a factory has to enumerate
// every export the overlay imports, and a blind author has no license to know
// that list (the codeRefs.contract.test.ts idiom).

vi.mock("../../api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
  openInEditor: vi.fn(() => Promise.resolve({ ok: true })),
}));
vi.mock("./Toast.tsx", () => ({ showToast: vi.fn(), Toast: () => null }));

import { ArtifactOverlay } from "./ArtifactOverlay.tsx";

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

/**
 * The shared `mutate` the contract routes the click through. It CATCHES, as the
 * real one must: the contract says refusals surface in the clock corner, so a
 * rejected write is a handled outcome rather than an unhandled rejection.
 */
const contractMutate = async <T,>(op: () => Promise<T>) => {
  try {
    return await op();
  } catch {
    return undefined as unknown as T;
  }
};

const contractJob = (): WebJob =>
  makeJob({
    slug: "j",
    artifacts: [
      { name: "00-overview.md", url: "/api/artifact/j/artifacts/00-overview.md" },
    ],
  });

const contractItem = (
  id: number,
  slug: string,
  name: string,
): WebLibraryItem => ({
  id,
  slug,
  name,
  saved_at: 1_755_000_000_000 + id,
  url: `/api/library/artifact/${id}/${name}`,
});

/**
 * Mounts the overlay on one url with one library list. The library props are
 * REQUIRED props of the component (ArtifactOverlay.test.tsx's `noLibrary`
 * block), so they are always passed; their NAMES are not frozen by the contract
 * and come from that permitted pre-existing file — disclosed in the report.
 */
function contractRender(opts: {
  url: string;
  library?: WebLibraryItem[];
  onSaveArtifact?: (...args: never[]) => Promise<unknown>;
  onRemoveSaved?: (...args: never[]) => Promise<unknown>;
}) {
  const onSaveArtifact = opts.onSaveArtifact ?? vi.fn(async () => {});
  const onRemoveSaved = opts.onRemoveSaved ?? vi.fn(async () => {});
  const out = render(
    <ArtifactOverlay
      open
      url={opts.url}
      name="j/00-overview.md"
      job={contractJob()}
      onNavigate={() => {}}
      onAskBranched={() => {}}
      onClose={() => {}}
      library={opts.library ?? []}
      onSaveArtifact={onSaveArtifact as never}
      onRemoveSaved={onRemoveSaved as never}
      mutate={contractMutate}
    />,
  );
  return { ...out, onSaveArtifact, onRemoveSaved };
}

const CONTRACT_UNSAVED_LABEL = "☆ save";
const CONTRACT_SAVED_LABEL = "★ saved";

/** Every button in the overlay head carrying one of the two frozen labels. */
function contractSaveButtons(): HTMLButtonElement[] {
  const head = document.querySelector(".panel-head");
  const scope: ParentNode = head ?? document;
  return [...scope.querySelectorAll("button")].filter((b) => {
    const t = (b.textContent ?? "").trim();
    return t === CONTRACT_UNSAVED_LABEL || t === CONTRACT_SAVED_LABEL;
  }) as HTMLButtonElement[];
}

/** The save button the reader can actually see. */
function contractVisibleSaveButton(): HTMLButtonElement | null {
  return contractSaveButtons().find((b) => !b.hasAttribute("hidden")) ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  location.hash = "";
  // the overlay fetches the artifact body on mount
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) =>
      Promise.resolve({ ok: true, text: () => Promise.resolve("# " + url) }),
    ),
  );
});

// ---------------------------------------------------------------------------
// (a) the save toggle
// ---------------------------------------------------------------------------

describe("the save toggle's rendered state", () => {
  // "Renders `☆ save` unsaved … with `aria-pressed`"; "state is
  // `libraryMatch(items, url) !== null`".
  it("reads ☆ save with aria-pressed=false when no saved row matches the open artifact", () => {
    contractRender({
      url: "/api/artifact/j/artifacts/00-overview.md",
      library: [contractItem(1, "some-other-job", "00-overview.md")],
    });
    const btn = contractVisibleSaveButton();
    expect(btn).not.toBeNull();
    expect(btn?.textContent?.trim()).toBe(CONTRACT_UNSAVED_LABEL);
    expect(btn).toHaveAttribute("aria-pressed", "false");
  });

  // "…/ `★ saved` saved". The match here is the artifact-url arm: exact
  // (slug, name) pair.
  it("reads ★ saved with aria-pressed=true when a saved row matches by (slug, name)", () => {
    contractRender({
      url: "/api/artifact/j/artifacts/00-overview.md",
      library: [
        contractItem(1, "some-other-job", "00-overview.md"),
        contractItem(2, "j", "00-overview.md"),
      ],
    });
    const btn = contractVisibleSaveButton();
    expect(btn).not.toBeNull();
    expect(btn?.textContent?.trim()).toBe(CONTRACT_SAVED_LABEL);
    expect(btn).toHaveAttribute("aria-pressed", "true");
  });

  // The memory view's "row click opens the item in the existing ArtifactOverlay
  // by its library `url`" obligation means the overlay is routinely opened on a
  // /api/library/artifact/<id>/… url — which `libraryMatch` matches BY ID. The
  // item deliberately carries a slug and name that no artifact url in this test
  // mentions, so only the id arm can produce the match.
  it("reads ★ saved when opened on a library url that matches by id", () => {
    contractRender({
      url: "/api/library/artifact/7/00-overview.md",
      library: [contractItem(7, "a-torn-down-job", "00-overview.md")],
    });
    const btn = contractVisibleSaveButton();
    expect(btn).not.toBeNull();
    expect(btn?.textContent?.trim()).toBe(CONTRACT_SAVED_LABEL);
    expect(btn).toHaveAttribute("aria-pressed", "true");
  });

  // The AMENDED save-toggle row: "A url that yields neither an `artifactParts`
  // triple nor a library match renders NO save button (hidden, not disabled)."
  // Two urls, both outside the /api/artifact/ family and both unmatched by the
  // (empty) library.
  it.each([
    ["a library url whose id names no saved row", "/api/library/artifact/999/x.md"],
    ["a url in neither family", "/api/upload/j/img-001.png"],
  ])("offers no save button for %s", (_label, url) => {
    contractRender({ url, library: [] });
    expect(contractVisibleSaveButton()).toBeNull();
  });

  // The other half of the same sentence — "hidden, NOT disabled". A save button
  // that is present and merely disabled is the shape the amendment rules out.
  it("never offers a disabled save button instead of hiding it", () => {
    contractRender({ url: "/api/upload/j/img-001.png", library: [] });
    for (const btn of contractSaveButtons()) {
      expect(btn.disabled).toBe(false);
    }
  });

  // "ninth `.btn.ghost` in `ArtifactOverlay`'s `.panel-head`, between `3D` and
  // `⑂ ask`". The between-ness and the classes are pinned; the literal ordinal
  // is deliberately not, since it is a consequence of what else the head holds
  // today rather than a property of this button.
  it("sits in .panel-head as a .btn.ghost between 3D and ⑂ ask", () => {
    contractRender({ url: "/api/artifact/j/artifacts/00-overview.md", library: [] });
    const head = document.querySelector(".panel-head");
    expect(head).not.toBeNull();

    const btn = contractVisibleSaveButton();
    expect(btn).not.toBeNull();
    expect(btn?.classList.contains("btn")).toBe(true);
    expect(btn?.classList.contains("ghost")).toBe(true);
    expect(head?.contains(btn as Node)).toBe(true);

    const iso = document.getElementById("panel-iso");
    const ask = document.getElementById("panel-ask");
    expect(iso).not.toBeNull();
    expect(ask).not.toBeNull();
    const order = [...(head as Element).querySelectorAll("button")];
    const at = (el: Element | null) => order.indexOf(el as HTMLButtonElement);
    expect(at(iso)).toBeGreaterThanOrEqual(0);
    expect(at(btn)).toBeGreaterThan(at(iso));
    expect(at(btn)).toBeLessThan(at(ask));
  });
});

describe("the save toggle's click", () => {
  // "click posts save/remove through the shared `mutate`".
  it("posts a save through mutate when the artifact is unsaved", async () => {
    const user = userEvent.setup();
    const { onSaveArtifact, onRemoveSaved } = contractRender({
      url: "/api/artifact/j/artifacts/00-overview.md",
      library: [],
    });
    await user.click(contractVisibleSaveButton() as HTMLButtonElement);
    await waitFor(() => expect(onSaveArtifact).toHaveBeenCalled());
    expect(onRemoveSaved).not.toHaveBeenCalled();
  });

  it("posts a remove through mutate when the artifact is saved", async () => {
    const user = userEvent.setup();
    const { onSaveArtifact, onRemoveSaved } = contractRender({
      url: "/api/artifact/j/artifacts/00-overview.md",
      library: [contractItem(2, "j", "00-overview.md")],
    });
    await user.click(contractVisibleSaveButton() as HTMLButtonElement);
    await waitFor(() => expect(onRemoveSaved).toHaveBeenCalled());
    expect(onSaveArtifact).not.toHaveBeenCalled();
  });

  // "state is `libraryMatch(items, url) !== null` — server state, never a local
  // latch". The write lands, but the `library` prop this render was given has
  // not changed (the poll has not come back yet), so the button must still read
  // unsaved. A local latch would flip it here.
  it("does not latch to ★ saved on a successful click while the served list is unchanged", async () => {
    const user = userEvent.setup();
    contractRender({
      url: "/api/artifact/j/artifacts/00-overview.md",
      library: [],
    });
    await user.click(contractVisibleSaveButton() as HTMLButtonElement);
    await waitFor(() =>
      expect(contractVisibleSaveButton()?.textContent?.trim()).toBe(
        CONTRACT_UNSAVED_LABEL,
      ),
    );
    expect(contractVisibleSaveButton()).toHaveAttribute("aria-pressed", "false");
  });

  // "a failed save leaves the button unsaved".
  it("leaves the button unsaved when the save is refused", async () => {
    const user = userEvent.setup();
    contractRender({
      url: "/api/artifact/j/artifacts/00-overview.md",
      library: [],
      // A REFUSAL as production actually delivers one: App's handler wraps the
      // write in the shared `mutate`, which parks the server's sentence in the
      // clock corner and RESOLVES — it never rejects — and the refused write
      // leaves the served list without a row for this artifact. That unchanged
      // list is what carries the assertion below, so the honest fixture is a
      // handler that resolves over an empty library rather than one that
      // rejects (arbitrated at the join; a rejecting handler was a shape no
      // seam in the app can produce).
      onSaveArtifact: vi.fn(() => Promise.resolve()),
    });
    await user.click(contractVisibleSaveButton() as HTMLButtonElement);
    await waitFor(() => {
      expect(contractVisibleSaveButton()?.textContent?.trim()).toBe(
        CONTRACT_UNSAVED_LABEL,
      );
    });
    expect(contractVisibleSaveButton()).toHaveAttribute("aria-pressed", "false");
    // the head is still there — a refusal is reported through mutate's own
    // channel, not by tearing the viewer down
    expect(screen.queryByText(CONTRACT_SAVED_LABEL)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// (b) the graph → memory rename
// ---------------------------------------------------------------------------

describe("the memory rename", () => {
  const CONTRACT_LEGACY_KEY = "kraken-view";

  // The rename, not the roster: this pin originally asserted the contract's
  // exact four-view list, and the kronos view merging beside it turned that
  // into a false failure — the same over-pin the store tests document for
  // schemaVersion equalities. What the rename obliges is that `memory` is a
  // view and `graph` is not; which OTHER views exist is the tab roster's
  // business, not this contract's. (Relaxed at the merge by the lead, citing
  // the amended contract row; the blind author was already released.)
  it("renames the graph view to memory", () => {
    expect(VIEWS as readonly string[]).toContain("memory");
    expect(VIEWS as readonly string[]).not.toContain("graph");
  });

  // "A legacy `#/graph` hash … normalize[s] to `memory` (never fall back to
  // board)." Board is the fallback for a view that no longer exists at all
  // (`dag`), and reusing it here would silently drop every bookmarked memory
  // link on the floor.
  it("resolves a legacy #/graph hash to memory, never to board", () => {
    location.hash = "#/graph";
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("memory");
  });

  // "…and a stored `kraken-view` of `"graph"` both normalize to `"memory"`."
  it("resolves a stored kraken-view of graph to memory, never to board", () => {
    localStorage.setItem(CONTRACT_LEGACY_KEY, "graph");
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("memory");
  });

  // Both legacy carriers at once, which is the real state of a browser that had
  // the graph tab open when this shipped.
  it("resolves to memory when both legacy carriers say graph", () => {
    localStorage.setItem(CONTRACT_LEGACY_KEY, "graph");
    location.hash = "#/graph";
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("memory");
  });

  // The new spelling keeps working, and the rename did not disturb the views
  // that were not renamed.
  it("resolves the new #/memory hash and leaves the other views alone", () => {
    location.hash = "#/memory";
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("memory");
  });

  it.each(["board", "log", "dream"])("still resolves #/%s", (view) => {
    location.hash = `#/${view}`;
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe(view);
  });
});
