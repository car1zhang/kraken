import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { Chrome } from "./Chrome.tsx";
import { makeBoard, makeDaemon, makeJob } from "../../test/fixtures.ts";

const NOW = 1_700_000_000_000;

const job = (over: Partial<WebJob> = {}): WebJob => makeJob({ mtime: 0, ...over });

function model(board: Partial<WebBoard> | null, extra: Partial<BoardModel> = {}): BoardModel {
  return {
    board: board
      ? makeBoard({
          daemon: makeDaemon({ pid: 42, last_tick: NOW / 1000 - 3 }),
          cell_cap: 3,
          live_cells: 1,
          ...board,
        })
      : undefined,
    error: undefined,
    byKey: new Map(),
    byColumn: { draft: [], queued: [], working: [], blocked: [], done: [] },
    archived: [],
    refetch: async () => {},
    setDragging: () => {},
    mutate: async () => null,
    lastError: null,
    ...extra,
  };
}

function renderChrome(board: BoardModel, over: Partial<Parameters<typeof Chrome>[0]> = {}) {
  const props = {
    board,
    view: "board" as const,
    onNavigate: vi.fn(),
    usage: undefined,
    onOpenUsage: vi.fn(),
    onToast: vi.fn(),
    ...over,
  };
  const { container } = render(<Chrome {...props} />);
  return { container, props };
}

const upd = () => document.getElementById("update-btn")!;
const ship = () => document.getElementById("ship-btn")!;
const banners = () => [...document.querySelectorAll(".update-banner")];

beforeEach(() => {
  localStorage.clear();
  vi.spyOn(Date, "now").mockReturnValue(NOW);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("the status line", () => {
  it("says connecting until the first payload lands", () => {
    renderChrome(model(null));
    expect(document.getElementById("daemon-stat")).toHaveTextContent("connecting…");
    expect(document.getElementById("daemon-led")!.className).toBe("led");
    expect(document.getElementById("cells-stat")).toBeEmptyDOMElement();
  });

  it("reports the daemon, the cell budget and the flag count", () => {
    renderChrome(
      model({
        jobs: [
          job({ slug: "a", question: { tool_use_id: "t" } }),
          job({ slug: "b", health: "pane gone" }),
        ],
      }),
    );
    expect(document.getElementById("daemon-stat")).toHaveTextContent("daemon pid 42 · tick 3s ago");
    expect(document.getElementById("daemon-led")!.className).toBe("led ok");
    expect(document.getElementById("cells-stat")).toHaveTextContent("cells 1/3");
    expect(document.getElementById("flag-stat")).toHaveTextContent("⚑ 2");
  });

  it("leaves the flag chip EMPTY at zero so :empty can hide it", () => {
    renderChrome(model({}));
    expect(document.getElementById("flag-stat")).toBeEmptyDOMElement();
  });

  it("says the server is unreachable when the board poll fails", () => {
    renderChrome(model({}, { error: new Error("boom") }));
    expect(document.getElementById("daemon-stat")).toHaveTextContent(
      "kraken web server unreachable",
    );
    expect(document.getElementById("daemon-led")!.className).toBe("led");
  });

  it("says 'daemon — restarting' instead, while an update run owns the outage", () => {
    // Mid-update the server is gone ON PURPOSE; "unreachable" would read as a
    // crash. The 1s status poll is live here, so its fetch is stubbed out.
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    renderChrome(model({ update: { phase: "installing", started_at: NOW } }, { error: new Error("x") }));
    expect(document.getElementById("daemon-stat")).toHaveTextContent("daemon — restarting");
  });

  it("hands the clock corner over to the last mutation failure", () => {
    renderChrome(model({}, { lastError: "job is not queued" }));
    expect(document.getElementById("tick-stat")).toHaveTextContent("✕ job is not queued");
  });

  it("gives the corner back to the clock once the failure has been readable", () => {
    // useBoard only clears lastError on the next SUCCESSFUL mutation, so one
    // refused drag would otherwise pin its text over the board's only
    // "am I still ticking" signal indefinitely.
    const failed = model({}, { lastError: "job is not queued" });
    const props = {
      board: failed,
      view: "board" as const,
      onNavigate: vi.fn(),
      usage: undefined,
      onOpenUsage: vi.fn(),
      onToast: vi.fn(),
    };
    const { rerender } = render(<Chrome {...props} />);
    expect(document.getElementById("tick-stat")).toHaveTextContent("✕ job is not queued");
    vi.spyOn(Date, "now").mockReturnValue(NOW + 11_000);
    rerender(<Chrome {...props} />);
    expect(document.getElementById("tick-stat")).not.toHaveTextContent("✕");
  });
});

describe("the stale-binary banner", () => {
  it("stays hidden on a server that says nothing", () => {
    renderChrome(model({}));
    expect(document.querySelector(".stale-banner")).toHaveAttribute("hidden");
  });

  it("names each stale half separately", () => {
    renderChrome(
      model({ stale: true, daemon: makeDaemon({ pid: 42, last_tick: NOW / 1000, stale: true }) }),
    );
    const b = document.querySelector(".stale-banner")!;
    expect(b).not.toHaveAttribute("hidden");
    expect(b).toHaveTextContent("restart k web");
    expect(b).toHaveTextContent("restart the daemon");
  });
});

describe("the ⟳ UPDATE control", () => {
  it("is a plain idle button with no record at all (an older server)", () => {
    renderChrome(model({}));
    expect(upd()).toHaveTextContent("UPDATE");
    expect(upd().className).toBe("upd");
    expect(upd()).toBeEnabled();
    expect(document.getElementById("build-stat")).toBeEmptyDOMElement();
  });

  it("wears the availability pip when the daemon says main has moved", () => {
    renderChrome(model({ update: { phase: "idle", behind: 3, checked_at: NOW } }));
    expect(upd()).toHaveTextContent("UPDATE · 3 BEHIND");
    expect(upd().classList.contains("avail")).toBe(true);
    // a dot, not a glyph — .upd .dot is a pip, .gly the idle state glyph
    expect(upd().querySelector(".dot")).not.toBeNull();
    expect(upd().querySelector(".gly")).toBeNull();
  });

  it("wears the ring spinner while a run is live, and keeps that node across repaints", () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const board = model({ update: { phase: "building", started_at: NOW - 12_000 } });
    const { container } = render(
      <Chrome
        board={board}
        view="board"
        onNavigate={vi.fn()}
        usage={undefined}
        onOpenUsage={vi.fn()}
        onToast={vi.fn()}
      />,
    );
    expect(upd()).toHaveTextContent("BUILDING… 12s");
    expect(upd().classList.contains("busy")).toBe(true);
    expect(upd()).toBeDisabled();
    // The vanilla painter existed only to avoid replacing this node — the 1.1s
    // spin keyframe restarts if it is. React reconciles it in place instead.
    // A ring spinner, not the ⟳ glyph: the busy states swap glyphCls (carl's
    // cursor round — the rotating arrow orbited off its optical centre).
    const glyph = upd().querySelector(".spinner");
    expect(glyph).not.toBeNull();
    expect(upd().querySelector(".gly")).toBeNull();
    render(
      <Chrome
        board={board}
        view="board"
        onNavigate={vi.fn()}
        usage={undefined}
        onOpenUsage={vi.fn()}
        onToast={vi.fn()}
      />,
      { container },
    );
    expect(upd().querySelector(".spinner")).toBe(glyph);
  });

  it("shows the finished run, its sha chip, and a RELOAD NOW banner", () => {
    renderChrome(
      model({
        update: {
          phase: "done",
          from_sha: "aaa1111",
          to_sha: "bbb2222",
          build_sha: "bbb2222",
          started_at: NOW - 40_000,
          updated_at: NOW - 1_000,
        },
      }),
    );
    expect(upd()).toHaveTextContent("UPDATED bbb2222");
    expect(upd().classList.contains("ok")).toBe(true);
    expect(document.getElementById("build-stat")).toHaveTextContent("@bbb2222");
    const banner = banners()[0]!;
    expect(banner).not.toHaveAttribute("hidden");
    expect(banner).toHaveTextContent("✓ updated aaa1111 → bbb2222 in 39s");
    expect(banner.querySelector('button[data-act="reload"]')).not.toBeNull();
  });

  it("hides an unstamped build rather than showing the word 'dev'", () => {
    renderChrome(model({ update: { phase: "idle", build_sha: "dev" } }));
    expect(document.getElementById("build-stat")).toBeEmptyDOMElement();
  });

  it("ages a terminal record out, so a week-old ✓ never greets a page load", () => {
    renderChrome(model({ update: { phase: "done", to_sha: "b", updated_at: NOW - 120_000 } }));
    expect(upd()).toHaveTextContent("UPDATE");
    expect(banners()[0]).toHaveAttribute("hidden");
  });

  it("turns a fresh failure into the door to the log", async () => {
    const onNavigate = vi.fn();
    renderChrome(
      model({ update: { phase: "failed", error: "tree is dirty", updated_at: NOW - 500 } }),
      { onNavigate },
    );
    expect(upd()).toHaveTextContent("UPDATE FAILED");
    expect(upd()).toHaveAttribute("title", "tree is dirty");
    await userEvent.click(upd());
    expect(onNavigate).toHaveBeenCalledWith("log");
    await userEvent.click(banners()[0]!.querySelector('button[data-act="log"]')!);
    expect(onNavigate).toHaveBeenCalledTimes(2);
  });

  it("blocks on a refusal, then lets the refusal age out", async () => {
    // The POST is refused before anything is spawned, so no record changes and
    // only the browser knows why. Bounded like the ship control's twin: as the
    // FIRST paint branch, an unbounded refusal outranks every other state
    // forever — including a `behind` count and any run the CLI starts.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status: 409,
        statusText: "Conflict",
        json: async () => ({ error: "kraken has uncommitted changes" }),
      })),
    );
    const onToast = vi.fn();
    const board = model({ update: { phase: "idle", behind: 3 } });
    const { rerender } = render(
      <Chrome
        board={board}
        view="board"
        onNavigate={vi.fn()}
        usage={undefined}
        onOpenUsage={vi.fn()}
        onToast={onToast}
      />,
    );
    await userEvent.click(upd());
    expect(upd()).toHaveTextContent("UPDATE BLOCKED");
    expect(upd()).toHaveAttribute("title", "kraken has uncommitted changes");
    expect(onToast).toHaveBeenCalledWith("✕ kraken has uncommitted changes");

    // 61s later the machine may well have been fixed, and the availability
    // count underneath has something to say again.
    vi.spyOn(Date, "now").mockReturnValue(NOW + 61_000);
    rerender(
      <Chrome
        board={board}
        view="board"
        onNavigate={vi.fn()}
        usage={undefined}
        onOpenUsage={vi.fn()}
        onToast={onToast}
      />,
    );
    expect(upd()).toHaveTextContent("UPDATE · 3 BEHIND");
  });

  it("dismisses a banner by RUN, so it stays gone across the self-reload", async () => {
    const board = model({
      update: { phase: "done", from_sha: "a", to_sha: "b", updated_at: NOW - 1_000 },
    });
    const { container } = renderChrome(board);
    await userEvent.click(banners()[0]!.querySelector('button[data-act="dismiss"]')!);
    expect(localStorage.getItem("kraken-dismissed-upd")).toBe(String(NOW - 1_000));
    expect(banners()[0]).toHaveAttribute("hidden");

    // a fresh mount — i.e. the reload — reads the same record and stays quiet
    container.remove();
    renderChrome(board);
    expect(banners()[0]).toHaveAttribute("hidden");
  });

  it("lets the NEXT run speak, because the key is that run's stamp", () => {
    // the previous run was dismissed…
    localStorage.setItem("kraken-dismissed-upd", String(NOW - 5_000));
    // …and a newer run necessarily carries a newer stamp
    renderChrome(
      model({ update: { phase: "done", from_sha: "b", to_sha: "c", updated_at: NOW - 1_000 } }),
    );
    expect(banners()[0]).not.toHaveAttribute("hidden");
    expect(banners()[0]).toHaveTextContent("✓ updated b → c");
  });
});

describe("the ⇧ SHIP iOS control", () => {
  it("says UPLOADED, never SHIPPED, and spells out what is left to do", () => {
    renderChrome(
      model({
        ship_ios: { phase: "done", build: 42, started_at: NOW - 300_000, updated_at: NOW - 2_000 },
      }),
    );
    expect(ship()).toHaveTextContent("UPLOADED b42");
    expect(ship()).not.toHaveTextContent("SHIPPED");
    const banner = banners()[1]!;
    expect(banner).toHaveTextContent("build 42 uploaded to App Store Connect");
    expect(banner).toHaveTextContent("TestFlight still has to process it");
    // no RELOAD NOW twin: a ship replaces nothing this page is running
    expect(banner.querySelector('button[data-act="reload"]')).toBeNull();
  });

  it("keeps its own dismissal key", async () => {
    renderChrome(model({ ship_ios: { phase: "done", build: 7, updated_at: NOW - 2_000 } }));
    await userEvent.click(banners()[1]!.querySelector('button[data-act="dismiss"]')!);
    expect(localStorage.getItem("kraken-dismissed-ship")).toBe(String(NOW - 2_000));
    expect(localStorage.getItem("kraken-dismissed-upd")).toBeNull();
  });

  it("holds a longer freshness window than update's", () => {
    // 90s: past UPD_RECENT_MS, inside SHIP_RECENT_MS
    renderChrome(model({ ship_ios: { phase: "done", build: 9, updated_at: NOW - 90_000 } }));
    expect(ship()).toHaveTextContent("UPLOADED b9");
  });
});

describe("the view tabs", () => {
  it("mark the active view and navigate", async () => {
    const onNavigate = vi.fn();
    const { container } = renderChrome(model({}), { view: "dream", onNavigate });
    const tabs = [...container.querySelectorAll(".viewtab")];
    expect(tabs.map((t) => t.textContent)).toEqual([
      "BOARD",
      "LOG",
      "DREAM",
      "GRAPH",
    ]);
    expect(tabs.find((t) => t.classList.contains("active"))).toHaveTextContent(
      "DREAM",
    );
    await userEvent.click(tabs[1]!);
    expect(onNavigate).toHaveBeenCalledWith("log");
  });
});

describe("the theme toggle", () => {
  it("cycles system → light → dark through window.krakenTheme", async () => {
    let pref: "light" | "dark" | "system" = "system";
    const set = vi.fn((p: "light" | "dark" | "system") => {
      pref = p;
    });
    window.krakenTheme = { get: () => pref, set };
    renderChrome(model({}));
    const btn = document.getElementById("theme-toggle")!;
    expect(btn).toHaveTextContent("◐ SYSTEM");
    await userEvent.click(btn);
    expect(set).toHaveBeenCalledWith("light");
    expect(btn).toHaveTextContent("◐ LIGHT");
    await userEvent.click(btn);
    expect(set).toHaveBeenCalledWith("dark");
    await userEvent.click(btn);
    expect(set).toHaveBeenCalledWith("system");
    delete window.krakenTheme;
  });
});

describe("the usage widget", () => {
  it("stays hidden until /api/usage parses once", async () => {
    const onOpenUsage = vi.fn();
    const { container } = renderChrome(model({}), { onOpenUsage });
    expect(document.getElementById("usage-stat")).toHaveAttribute("hidden");

    container.remove();
    renderChrome(model({}), {
      onOpenUsage,
      usage: { available: true, daemon_ok: true, age_seconds: 5, accounts: [{ slug: "main", current: true }] },
    });
    const widget = document.getElementById("usage-stat")!;
    expect(widget).not.toHaveAttribute("hidden");
    await userEvent.click(widget);
    expect(onOpenUsage).toHaveBeenCalled();
  });
});
