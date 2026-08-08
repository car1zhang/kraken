import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WebUsage } from "../../api/types.ts";
import { usageColorClass, usageCountdown } from "../../lib/format.ts";
import { UsageOverlay, UsageStatContent } from "./UsageOverlay.tsx";

vi.mock("../../api/client.ts", () => ({
  switchAccount: vi.fn(() => Promise.resolve({ from: "a", to: "b", mode: "manual" })),
  setRotationMode: vi.fn(() => Promise.resolve({ mode: "auto" })),
  openTmux: vi.fn(),
}));

import { setRotationMode, switchAccount } from "../../api/client.ts";

const usage = (over: Partial<WebUsage> = {}): WebUsage => ({
  available: true,
  daemon_ok: true,
  fetched_at: Date.now(),
  age_seconds: 5,
  rotation_mode: "auto",
  accounts: [
    {
      slug: "main",
      current: true,
      email: "a@x.com",
      tier: "default_claude_max",
      session: { percent: 12 },
      weekly: { percent: 55 },
      models: [{ name: "OPUS", percent: 91 }],
    },
    { slug: "pool1", email: "b@x.com", weekly: { percent: 70 } },
  ],
  ...over,
});

// lib/format.test.ts owns these two helpers; kept here as the overlay's own
// contract on the thresholds its bars and segments paint from.
describe("usageColorClass", () => {
  it("thresholds at 50 and 80", () => {
    expect(usageColorClass(0)).toBe("u-ok");
    expect(usageColorClass(49.9)).toBe("u-ok");
    expect(usageColorClass(50)).toBe("u-warn");
    expect(usageColorClass(79.9)).toBe("u-warn");
    expect(usageColorClass(80)).toBe("u-danger");
    expect(usageColorClass(100)).toBe("u-danger");
  });
});

describe("usageCountdown", () => {
  it("collapses to the coarsest useful unit", () => {
    const now = Date.now();
    expect(usageCountdown(now - 1000)).toBe("now");
    expect(usageCountdown(now + 8 * 60_000 + 500)).toBe("8m");
    expect(usageCountdown(now + (3 * 3600 + 12 * 60) * 1000 + 500)).toBe("3h12m");
    expect(usageCountdown(now + 4 * 86400_000 + 500)).toBe("4d");
  });
});

describe("UsageStatContent", () => {
  it("renders the main account's windows plus a pool summary", () => {
    const { container } = render(<UsageStatContent data={usage()} />);
    const text = container.textContent ?? "";
    expect(text).toContain("5h 12%");
    expect(text).toContain("7d 55%");
    expect(text).toContain("opus 91%");
    expect(text).toContain("pool 1");
    // the pool summary is the COUNT alone — the `worst 7d` clause was
    // retired (carl's call at the macOS-app manual gate), so a pool
    // account's own percents must not leak into the widget (the fixture's
    // pool account carries weekly 70%)
    expect(text).not.toContain("worst 7d");
    expect(text).not.toContain("70%");
  });

  it("degrades to a dash rather than broken markup", () => {
    expect(render(<UsageStatContent data={undefined} />).container.textContent).toBe(
      "usage —",
    );
    expect(
      render(<UsageStatContent data={usage({ available: false })} />).container
        .textContent,
    ).toBe("usage —");
    expect(
      render(<UsageStatContent data={usage({ accounts: [] })} />).container
        .textContent,
    ).toBe("usage —");
  });

  it("an account with no windows at all does not leave a blank widget", () => {
    const { container } = render(
      <UsageStatContent
        data={usage({ accounts: [{ slug: "only", current: true }] })}
      />,
    );
    expect(container.textContent).toBe("usage —");
  });

  it("shows the manual chip only when rotation is actually pinned", () => {
    expect(render(<UsageStatContent data={usage()} />).container.textContent).not.toContain(
      "manual",
    );
    expect(
      render(<UsageStatContent data={usage({ rotation_mode: "manual" })} />)
        .container.textContent,
    ).toContain("manual");
    // an older server sends no rotation_mode at all — status quo look
    expect(
      render(<UsageStatContent data={usage({ rotation_mode: undefined })} />)
        .container.textContent,
    ).not.toContain("manual");
  });

  it("flags staleness on a dead daemon or a data age past 15 minutes", () => {
    expect(
      render(<UsageStatContent data={usage({ daemon_ok: false })} />).container
        .textContent,
    ).toContain("stale");
    expect(
      render(<UsageStatContent data={usage({ age_seconds: 1200 })} />).container
        .textContent,
    ).toContain("stale 20m");
  });
});

describe("UsageOverlay", () => {
  beforeEach(() => vi.clearAllMocks());

  const mount = (data: WebUsage | undefined, refetch = async () => {}) =>
    render(
      <UsageOverlay open data={data} onClose={() => {}} refetch={refetch} />,
    );

  it("renders the rotation chip, one block per account and its bars", () => {
    mount(usage());
    expect(document.getElementById("usage-mode-chip")?.textContent).toBe(
      "rotation: auto",
    );
    expect(document.querySelectorAll(".u-account").length).toBe(2);
    // main: session + weekly + one model; pool1: weekly only
    expect(document.querySelectorAll(".u-row").length).toBe(4);
    expect(document.querySelector(".u-chip-current")?.textContent).toBe("current");
    // the tier's default_claude_ prefix is stripped
    expect(document.body.textContent).toContain("max");
  });

  it("hides the rotation chip and make-main entirely on an older server", () => {
    mount(usage({ rotation_mode: undefined }));
    expect(document.getElementById("usage-mode-chip")?.textContent).toBe("");
    expect(document.querySelector(".u-make-main")).toBeNull();
  });

  it("offers make-main for a usable pool account only", () => {
    mount(usage());
    const btns = document.querySelectorAll(".u-make-main");
    expect(btns.length).toBe(1);
    expect((btns[0] as HTMLElement).dataset.slug).toBe("pool1");
  });

  it("withholds make-main from no_creds and reauth rows", () => {
    mount(
      usage({
        accounts: [
          { slug: "main", current: true, email: "a@x.com" },
          { slug: "dead", email: "c@x.com", no_creds: true },
          { slug: "expired", email: "d@x.com", reauth: true },
        ],
      }),
    );
    expect(document.querySelectorAll(".u-make-main").length).toBe(0);
  });

  it("withholds make-main from the save-back slot that mirrors the current main", () => {
    // core.ErrSaveBackIsTarget: every rotation would otherwise re-offer a
    // button that is guaranteed to fail
    mount(
      usage({
        accounts: [
          { slug: "main", current: true, email: "a@x.com" },
          { slug: "saveback", email: "A@X.com" },
        ],
      }),
    );
    expect(document.querySelectorAll(".u-make-main").length).toBe(0);
  });

  it("still offers make-main for a cooling or stale row — the server decides", () => {
    mount(
      usage({
        accounts: [
          { slug: "main", current: true, email: "a@x.com" },
          {
            slug: "cool",
            email: "c@x.com",
            cooldown_until: Date.now() + 60_000,
            stale: true,
          },
        ],
      }),
    );
    expect(document.querySelectorAll(".u-make-main").length).toBe(1);
    expect(document.body.textContent).toContain("cooling");
    expect(document.body.textContent).toContain("stale");
  });

  it("the manual banner posts mode:auto and names who pinned it", async () => {
    const user = userEvent.setup();
    mount(
      usage({
        rotation_mode: "manual",
        rotation_set_by: "carl",
        rotation_set_at: String(Date.now() - 120_000),
      }),
    );
    expect(document.querySelector(".u-manual-banner")?.textContent).toContain(
      "pinned to a@x.com by carl",
    );
    await user.click(screen.getByText("switch back to auto"));
    await waitFor(() => expect(setRotationMode).toHaveBeenCalledWith("auto"));
  });

  it("no manual banner in auto mode", () => {
    mount(usage());
    expect(document.querySelector(".u-manual-banner")).toBeNull();
  });

  // The `finally` invariant: refetch() SWALLOWS its own errors, so a POST that
  // succeeds followed by a silently-failed refetch skips both the catch branch
  // and the re-render that would discard a disabled button. Re-enabling only in
  // `catch` leaves a permanently dead control.
  it("re-enables the control after a POST success whose refetch silently fails", async () => {
    const user = userEvent.setup();
    // exactly what usePoll's refetch does: never rejects
    const refetch = vi.fn(async () => {});
    mount(usage(), refetch);
    const btn = document.querySelector(".u-make-main") as HTMLButtonElement;
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await user.click(btn);
    await waitFor(() => expect(switchAccount).toHaveBeenCalledWith("pool1"));
    await waitFor(() =>
      expect(
        (document.querySelector(".u-make-main") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
  });

  it("re-enables the control after a failed POST too", async () => {
    const user = userEvent.setup();
    (switchAccount as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new Error("nope"),
    );
    mount(usage());
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await user.click(document.querySelector(".u-make-main") as HTMLButtonElement);
    await waitFor(() =>
      expect(
        (document.querySelector(".u-make-main") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
  });

  it("a declined confirm posts nothing", async () => {
    const user = userEvent.setup();
    mount(usage());
    vi.spyOn(window, "confirm").mockReturnValue(false);
    await user.click(document.querySelector(".u-make-main") as HTMLButtonElement);
    expect(switchAccount).not.toHaveBeenCalled();
  });

  it("says so rather than rendering an empty panel when there is no data", () => {
    mount(undefined);
    expect(document.getElementById("usage-fetched")?.textContent).toBe(
      "no data yet",
    );
    expect(document.querySelector(".empty")?.textContent).toBe(
      "no usage data yet",
    );
  });

  it("warns that the data is frozen when the daemon is down", () => {
    mount(usage({ daemon_ok: false }));
    expect(document.getElementById("usage-fetched")?.textContent).toBe(
      "daemon down — data frozen",
    );
  });
});
