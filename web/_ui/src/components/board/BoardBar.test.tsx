import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { JobSearchModel } from "../../hooks/useJobSearch.ts";
import { COLUMNS, type Column } from "../../lib/columns.ts";
import { parseTerms } from "../../lib/jobSearch.ts";
import { BoardBar } from "./BoardBar.tsx";

const noJobs = Object.fromEntries(COLUMNS.map((c) => [c, []])) as Record<Column, []>;

/**
 * The bar wired to real query state, since half of what it does is react to
 * its own value (the ✕ and the readout appear only once a query is active).
 * The COUNTS are fixed — the bar only renders what the hook hands it, and
 * `2 of 6` is the mockup's own worked example.
 */
function Harness({
  overlayOpen,
  hidden,
  onQuery,
}: {
  overlayOpen?: boolean;
  hidden?: boolean;
  onQuery?: (q: string) => void;
}) {
  const [query, setQuery] = useState("");
  const terms = parseTerms(query);
  const search: JobSearchModel = {
    query,
    setQuery: (q) => {
      onQuery?.(q);
      setQuery(q);
    },
    terms,
    byColumn: noJobs,
    archived: [],
    reasonByKey: new Map(),
    visibleKeys: new Set(),
    matched: 2,
    total: 6,
    active: terms.length > 0,
  };
  return <BoardBar search={search} overlayOpen={overlayOpen} hidden={hidden} />;
}

const input = () => screen.getByRole("textbox", { name: "search jobs" }) as HTMLInputElement;

/** A raw document-level press, so `defaultPrevented` can be read back. */
function pressK(init: KeyboardEventInit): KeyboardEvent {
  const ev = new KeyboardEvent("keydown", {
    key: "k",
    bubbles: true,
    cancelable: true,
    ...init,
  });
  document.dispatchEvent(ev);
  return ev;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("BoardBar", () => {
  it("reads `M jobs` at rest, with the shortcut hint and no clear button", () => {
    render(<Harness />);
    expect(document.querySelector(".bb-read")).toHaveTextContent("6 jobs");
    expect(document.querySelector(".bb-hint")).toHaveTextContent("⌘K");
    expect(document.querySelector(".bb-clear")).toBeNull();
  });

  it("reads `N of M jobs` once a query is active, and offers the ✕", async () => {
    render(<Harness />);
    await userEvent.type(input(), "sea");
    expect(document.querySelector(".bb-read")).toHaveTextContent("2 of 6 jobs");
    expect(document.querySelector(".bb-clear")).not.toBeNull();
    expect(document.querySelector(".bb-hint")).toBeNull();
  });

  it("reports every keystroke to setQuery", async () => {
    const onQuery = vi.fn();
    render(<Harness onQuery={onQuery} />);
    await userEvent.type(input(), "ab");
    expect(onQuery.mock.calls).toEqual([["a"], ["ab"]]);
  });

  it("clears on ✕", async () => {
    render(<Harness />);
    await userEvent.type(input(), "sea");
    await userEvent.click(screen.getByRole("button", { name: "clear the search" }));
    expect(input()).toHaveValue("");
  });

  it("offers no autocomplete — a slug fragment is not a form field", () => {
    render(<Harness />);
    expect(input()).toHaveAttribute("autocomplete", "off");
  });

  it("focuses and SELECTS on cmd+K, and takes the key from the browser", async () => {
    render(<Harness />);
    await userEvent.type(input(), "sea");
    await userEvent.click(document.body);

    const ev = pressK({ metaKey: true });
    expect(document.activeElement).toBe(input());
    expect(input().selectionStart).toBe(0);
    expect(input().selectionEnd).toBe(3);
    expect(ev.defaultPrevented).toBe(true);
  });

  it("does the same on ctrl+K, for a keyboard with no ⌘", () => {
    render(<Harness />);
    const ev = pressK({ ctrlKey: true });
    expect(document.activeElement).toBe(input());
    expect(ev.defaultPrevented).toBe(true);
  });

  it("ignores a bare K", () => {
    render(<Harness />);
    const ev = pressK({});
    expect(document.activeElement).not.toBe(input());
    expect(ev.defaultPrevented).toBe(false);
  });

  // Two states where the keystroke is not ours to take. Both return before
  // preventDefault, so the browser keeps its own binding rather than losing
  // it to a shortcut that then does nothing.
  it("is inert while a modal overlay is open — the composer keeps focus", () => {
    render(<Harness overlayOpen />);
    const before = document.activeElement;
    const ev = pressK({ metaKey: true });
    expect(document.activeElement).toBe(before);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("is inert while the board is hidden behind another view", () => {
    render(<Harness hidden />);
    // by id, not by role: a hidden subtree is out of the accessibility tree,
    // which is the same reason the shortcut has nothing to focus there.
    const field = document.querySelector("#job-search")!;
    const ev = pressK({ metaKey: true });
    expect(document.activeElement).not.toBe(field);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("hides itself with the board", () => {
    render(<Harness hidden />);
    expect(document.querySelector(".boardbar")).toHaveAttribute("hidden");
  });

  it("clears on Escape without letting it reach the overlay stack", async () => {
    // Filtered to Escape: every other keystroke SHOULD reach the document,
    // and the typing below would otherwise count as three hits.
    const onDocEscape = vi.fn();
    const listener = (e: KeyboardEvent) => {
      if (e.key === "Escape") onDocEscape();
    };
    document.addEventListener("keydown", listener);
    try {
      render(<Harness />);
      await userEvent.type(input(), "sea");
      await userEvent.keyboard("{Escape}");
      expect(input()).toHaveValue("");
      expect(onDocEscape).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener("keydown", listener);
    }
  });
});
