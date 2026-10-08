import { act, render, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { useHashRoute, VIEWS } from "./useHashRoute.ts";

/**
 * The shape App.tsx renders: one <main> per view, hidden unless it is the
 * active one. Views are hidden rather than unmounted, so "no view matched"
 * does not show as an error — it shows as a page with nothing on it, which
 * is exactly the failure a stale "dag" used to produce.
 */
function ViewHarness() {
  const [view] = useHashRoute();
  return (
    <>
      <main id="board" hidden={view !== "board"} />
      <main className="logview" id="log" hidden={view !== "log"} />
      <main className="dreamview" id="dream" hidden={view !== "dream"} />
      {/* the MEMORY tab's <main> keeps the id/class `graph` — the rename is a
          label, and every internal name stays put */}
      <main className="graphview" id="graph" hidden={view !== "memory"} />
    </>
  );
}

const LEGACY_KEY = "kraken-view";

beforeEach(() => {
  localStorage.clear();
  location.hash = "";
});

describe("VIEWS", () => {
  it("is board · memory · dream · log (carl's tab order, graph renamed), with no dag", () => {
    expect([...VIEWS]).toEqual(["board", "memory", "dream", "log"]);
    expect(VIEWS as readonly string[]).not.toContain("dag");
  });
});

describe("useHashRoute", () => {
  it("seeds from a valid legacy localStorage view", () => {
    localStorage.setItem(LEGACY_KEY, "dream");
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("dream");
  });

  // THE regression this file exists for. A browser that last had the removed
  // prereq-graph view open still holds "dag" here. A truthiness guard passes
  // it straight through, and "dag" then matches no <main>: every view hidden,
  // a blank page, no error anywhere.
  it("falls back to the board for a stale 'dag' in localStorage", () => {
    localStorage.setItem(LEGACY_KEY, "dag");
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("board");
  });

  it("falls back to the board for a stale '#/dag' in the URL", () => {
    location.hash = "#/dag";
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("board");
  });

  it("stamps the hash on mount when the URL had none", () => {
    const { result } = renderHook(() => useHashRoute());
    expect(result.current[0]).toBe("board");
    expect(location.hash).toBe("#/board");
  });

  it("navigate() moves the view, the hash and the legacy key together", () => {
    const { result } = renderHook(() => useHashRoute());
    act(() => result.current[1]("dream"));
    expect(result.current[0]).toBe("dream");
    expect(location.hash).toBe("#/dream");
    // the stale value is overwritten the first time a tab is clicked
    expect(localStorage.getItem(LEGACY_KEY)).toBe("dream");
  });

  it("follows a hashchange (the back button)", () => {
    const { result } = renderHook(() => useHashRoute());
    act(() => {
      location.hash = "#/log";
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(result.current[0]).toBe("log");
  });

  it("still renders the BOARD, not a blank page, on a stale 'dag'", () => {
    localStorage.setItem(LEGACY_KEY, "dag");
    const { container } = render(<ViewHarness />);
    const visible = [...container.querySelectorAll("main")].filter(
      (m) => !m.hasAttribute("hidden"),
    );
    expect(visible).toHaveLength(1);
    expect(visible[0]?.id).toBe("board");
  });

  it("ignores a hashchange to a view that no longer exists", () => {
    const { result } = renderHook(() => useHashRoute());
    act(() => result.current[1]("dream"));
    act(() => {
      location.hash = "#/dag";
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    // still on a REAL view — never a blank page
    expect(result.current[0]).toBe("dream");
  });
});
