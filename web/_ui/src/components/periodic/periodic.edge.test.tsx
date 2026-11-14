import { act, cleanup, fireEvent, render, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebPeriodic } from "../../api/types.ts";
import * as client from "../../api/client.ts";
import { PeriodicView } from "./PeriodicView.tsx";
import { lastFiringLabel, nextDueLabel } from "./periodicText.ts";

/**
 * periodic.edge.test.tsx — athena-edge's blind suite for slice 5 of the
 * periodic-jobs contract (50-contract.md, "Slice 5: the board view"): the label
 * helpers at their boundaries, the client calls' rejections, and the view's
 * refusal paths (inactive, built-in row, two-step delete, rejected ops and
 * adds). Written from the contract only.
 *
 * The view's four client calls are replaced; the client-level tests reach the
 * REAL functions through vi.importActual and stub `fetch` instead.
 */

vi.mock("../../api/client.ts", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../../api/client.ts")>();
  return {
    ...orig,
    fetchPeriodic: vi.fn(),
    addPeriodic: vi.fn(),
    periodicOp: vi.fn(),
    runPeriodic: vi.fn(),
  };
});

const periodicEdgeFetch = vi.mocked(client.fetchPeriodic);
const periodicEdgeAdd = vi.mocked(client.addPeriodic);
const periodicEdgeOp = vi.mocked(client.periodicOp);
const periodicEdgeRun = vi.mocked(client.runPeriodic);

const periodicEdgeRow = (over: Partial<WebPeriodic> = {}): WebPeriodic => ({
  name: "nightly",
  prefix: "nightly",
  at: "09:00",
  days: [],
  every: "",
  cadence: "daily 09:00",
  enabled: true,
  builtin: false,
  template: "---\nrepo: kraken\n---\nbody\n",
  last_date: "",
  last_slug: "",
  last_note: "",
  last_at: 0,
  next_due: 0,
  created_at: 1,
  ...over,
});

const periodicEdgeDream = periodicEdgeRow({
  name: "dream",
  prefix: "dream",
  at: "00:00",
  cadence: "daily 00:00",
  builtin: true,
  template: "",
});

const periodicEdgeToast = vi.fn();

beforeEach(() => {
  periodicEdgeFetch.mockReset();
  periodicEdgeAdd.mockReset();
  periodicEdgeOp.mockReset();
  periodicEdgeRun.mockReset();
  periodicEdgeToast.mockReset();
  periodicEdgeFetch.mockResolvedValue({ periodic: [periodicEdgeDream, periodicEdgeRow()] });
  periodicEdgeOp.mockResolvedValue({ ok: true });
  periodicEdgeAdd.mockResolvedValue({ ok: true });
  periodicEdgeRun.mockResolvedValue({ ok: true, slug: "nightly-2026-11-13-run", started: true, note: "" });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

// ------------------------------------------------------------ lastFiringLabel

describe("lastFiringLabel edges", () => {
  // Contract: "`never` when `last_date` is empty" — even if slug or note is set.
  it("is exactly `never` when last_date is empty, whatever else is set", () => {
    expect(lastFiringLabel(periodicEdgeRow())).toBe("never");
    expect(
      lastFiringLabel(periodicEdgeRow({ last_slug: "nightly-2026-11-13", last_note: "refused", last_at: 5 })),
    ).toBe("never");
  });

  // Contract: "Otherwise it starts with `last_date`, contains `last_slug` when
  // that is non-empty, and contains `last_note` when that is non-empty."
  it("starts with the date and carries a note when there is no slug", () => {
    const label = lastFiringLabel(
      periodicEdgeRow({ last_date: "2026-11-13", last_note: "an active job already holds nightly-2026-11-13" }),
    );
    expect(label.startsWith("2026-11-13")).toBe(true);
    expect(label).toContain("an active job already holds nightly-2026-11-13");
  });

  it("starts with the date when only the date is set", () => {
    expect(lastFiringLabel(periodicEdgeRow({ last_date: "2026-11-13" })).startsWith("2026-11-13")).toBe(true);
  });
});

// ------------------------------------------------------------ nextDueLabel

/** The contract's expected `HH:MM` for a local time, built independently. */
const periodicEdgeHHMM = (ms: number) => {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
};

describe("nextDueLabel edges", () => {
  const now = new Date(2026, 10, 13, 10, 0, 0, 0).getTime();

  // Contract: "`paused` when `!p.enabled`" — first, so a past or zero next_due
  // does not outrank it.
  it("is `paused` for a disabled row whatever next_due says", () => {
    for (const next_due of [0, now - 1, now, now + 60_000]) {
      expect(nextDueLabel(periodicEdgeRow({ enabled: false, next_due }), now)).toBe("paused");
    }
  });

  // Contract: "`none` when enabled and `next_due` is 0" — not `due now`, even
  // though 0 is before nowMs.
  it("is `none` for next_due 0, not `due now`", () => {
    expect(nextDueLabel(periodicEdgeRow({ next_due: 0 }), now)).toBe("none");
  });

  // Contract: "`due now` when `next_due` is at or before `nowMs`."
  it("is `due now` exactly at nowMs and before it", () => {
    expect(nextDueLabel(periodicEdgeRow({ next_due: now }), now)).toBe("due now");
    expect(nextDueLabel(periodicEdgeRow({ next_due: now - 1 }), now)).toBe("due now");
    expect(nextDueLabel(periodicEdgeRow({ next_due: 1 }), now)).toBe("due now");
  });

  // Contract: "Otherwise the local time of `next_due` as zero-padded `HH:MM`,
  // prefixed `today ` when it falls on `nowMs`'s local date and by the local
  // `YYYY-MM-DD` and a space when it does not."
  it("prefixes `today ` one ms after now and at the last minute of the local day", () => {
    const soon = now + 1;
    expect(nextDueLabel(periodicEdgeRow({ next_due: soon }), now)).toBe(`today ${periodicEdgeHHMM(soon)}`);
    const late = new Date(2026, 10, 13, 23, 59, 59, 999).getTime();
    expect(nextDueLabel(periodicEdgeRow({ next_due: late }), now)).toBe("today 23:59");
  });

  it("prefixes the local date from the next local midnight on, zero-padded", () => {
    const midnight = new Date(2026, 10, 14, 0, 0, 0, 0).getTime();
    expect(nextDueLabel(periodicEdgeRow({ next_due: midnight }), now)).toBe("2026-11-14 00:00");
    const jan = new Date(2027, 0, 5, 7, 3, 0, 0).getTime();
    expect(nextDueLabel(periodicEdgeRow({ next_due: jan }), now)).toBe("2027-01-05 07:03");
  });
});

// ------------------------------------------------------------ client rejections

const real = await vi.importActual<typeof import("../../api/client.ts")>("../../api/client.ts");

const periodicEdgeErrorFetch = (status: number, error: string) =>
  vi.fn(async () =>
    new Response(JSON.stringify({ error }), { status, headers: { "Content-Type": "application/json" } }),
  );

describe("client rejections", () => {
  // Contract: addPeriodic "Rejects with an `Error` carrying the server's
  // `error` text on a non-2xx answer."; periodicOp and runPeriodic "Rejects the
  // same way."
  it("addPeriodic rejects with the server's error text on 400 and 409", async () => {
    for (const [status, msg] of [
      [400, "at must be H:MM or HH:MM"],
      [409, "prefix nightly is taken"],
    ] as const) {
      vi.stubGlobal("fetch", periodicEdgeErrorFetch(status, msg));
      const p = real.addPeriodic({ name: "n", prefix: "nightly", at: "09:00", days: "", every: "", template: "t" });
      await expect(p).rejects.toBeInstanceOf(Error);
      await expect(p).rejects.toThrow(msg);
    }
  });

  it("periodicOp rejects with the server's error text on 404 and 409", async () => {
    for (const [status, msg, op] of [
      [404, "no periodic job named ghost", "pause"],
      [409, "dream is built in", "remove"],
      [403, "periodic-edge refusal", "resume"],
    ] as const) {
      vi.stubGlobal("fetch", periodicEdgeErrorFetch(status, msg));
      const p = real.periodicOp(op, "ghost");
      await expect(p).rejects.toBeInstanceOf(Error);
      await expect(p).rejects.toThrow(msg);
    }
  });

  it("runPeriodic rejects with the server's error text on 404", async () => {
    vi.stubGlobal("fetch", periodicEdgeErrorFetch(404, "no periodic job named ghost"));
    const p = real.runPeriodic("ghost");
    await expect(p).rejects.toBeInstanceOf(Error);
    await expect(p).rejects.toThrow("no periodic job named ghost");
  });
});

// ------------------------------------------------------------ PeriodicView

const periodicEdgeView = (active = true) =>
  render(<PeriodicView active={active} onToast={periodicEdgeToast} />);

const periodicEdgeRowEl = async (container: HTMLElement, name: string) => {
  let el: HTMLElement | null = null;
  await waitFor(() => {
    el = container.querySelector<HTMLElement>(`[data-periodic="${name}"]`);
    expect(el).not.toBeNull();
  });
  return el as unknown as HTMLElement;
};

describe("<PeriodicView> edges", () => {
  // Contract: "Renders `<main id=\"periodic\">`, with the `hidden` attribute
  // exactly when `!active`. It calls `fetchPeriodic` while active and does not
  // poll while inactive."
  it("is hidden and does not poll while inactive", async () => {
    vi.useFakeTimers();
    const { container } = periodicEdgeView(false);
    const main = container.querySelector("main#periodic");
    expect(main).not.toBeNull();
    expect(main!.hasAttribute("hidden")).toBe(true);
    await act(async () => {
      await Promise.resolve();
    });
    const settled = periodicEdgeFetch.mock.calls.length;
    await act(async () => {
      vi.advanceTimersByTime(10 * 60_000);
    });
    expect(periodicEdgeFetch.mock.calls.length).toBe(settled);
  });

  it("carries no hidden attribute while active", async () => {
    const { container } = periodicEdgeView(true);
    const main = container.querySelector("main#periodic");
    expect(main).not.toBeNull();
    expect(main!.hasAttribute("hidden")).toBe(false);
    await waitFor(() => expect(periodicEdgeFetch).toHaveBeenCalled());
  });

  // Contract: "A `builtin: true` row has none of them."
  // Amended in revision 2: the dream row pauses and resumes like a stored row.
  it("renders no run now or delete button on the built-in row, and one pause", async () => {
    const { container } = periodicEdgeView();
    const dream = await periodicEdgeRowEl(container, "dream");
    for (const name of ["run now", "delete", "confirm delete"]) {
      expect(within(dream).queryByRole("button", { name })).toBeNull();
    }
    expect(within(dream).getAllByRole("button")).toHaveLength(1);
  });

  // Revision 2: an interval row names its jobs with the time as well, and the
  // add form sends the interval.
  it("shows an interval row's cadence and sends `every` from the form", async () => {
    periodicEdgeFetch.mockResolvedValue({
      periodic: [periodicEdgeDream, periodicEdgeRow({ name: "pulse", prefix: "pulse", at: "", every: "2h", cadence: "every 2h" })],
    });
    const { container } = periodicEdgeView();
    const row = await periodicEdgeRowEl(container, "pulse");
    expect(row.textContent).toContain("every 2h");
    expect(row.textContent).toContain("pulse-YYYY-MM-DD-HHMM");

    const form = container.querySelector<HTMLFormElement>("#periodic-add")!;
    const set = (name: string, value: string) =>
      fireEvent.change(form.querySelector(`[name="${name}"]`)!, { target: { value } });
    set("name", "beat");
    set("prefix", "beat");
    set("every", "30m");
    set("template", "t");
    periodicEdgeAdd.mockResolvedValue({ ok: true });
    fireEvent.click(within(form).getByRole("button", { name: "add" }));
    await waitFor(() =>
      expect(periodicEdgeAdd).toHaveBeenCalledWith({ name: "beat", prefix: "beat", at: "", days: "", every: "30m", template: "t" }),
    );
  });

  // Contract: "`pause` when enabled or `resume` when not" — never both.
  it("shows resume and no pause on a paused row", async () => {
    periodicEdgeFetch.mockResolvedValue({ periodic: [periodicEdgeDream, periodicEdgeRow({ enabled: false })] });
    const { container } = periodicEdgeView();
    const row = await periodicEdgeRowEl(container, "nightly");
    expect(within(row).getByRole("button", { name: "resume" })).toBeTruthy();
    expect(within(row).queryByRole("button", { name: "pause" })).toBeNull();
    fireEvent.click(within(row).getByRole("button", { name: "resume" }));
    await waitFor(() => expect(periodicEdgeOp).toHaveBeenCalledWith("resume", "nightly"));
  });

  // Contract: "The first click on `delete` only renames that button to `confirm
  // delete`; clicking `confirm delete` calls `periodicOp(\"remove\", name)`."
  it("deletes only on the second, confirming click", async () => {
    const { container } = periodicEdgeView();
    const row = await periodicEdgeRowEl(container, "nightly");
    fireEvent.click(within(row).getByRole("button", { name: "delete" }));
    await act(async () => {
      await Promise.resolve();
    });
    expect(periodicEdgeOp).not.toHaveBeenCalled();
    expect(within(row).queryByRole("button", { name: "delete" })).toBeNull();
    const confirm = within(row).getByRole("button", { name: "confirm delete" });
    fireEvent.click(confirm);
    await waitFor(() => expect(periodicEdgeOp).toHaveBeenCalledWith("remove", "nightly"));
    expect(periodicEdgeOp).toHaveBeenCalledTimes(1);
  });

  // Contract: "An op that rejects calls `onToast` with a message containing the
  // error's text."
  it("toasts a rejected pause with the error text", async () => {
    periodicEdgeOp.mockRejectedValue(new Error("periodic-edge: pause refused"));
    const { container } = periodicEdgeView();
    const row = await periodicEdgeRowEl(container, "nightly");
    fireEvent.click(within(row).getByRole("button", { name: "pause" }));
    await waitFor(() =>
      expect(periodicEdgeToast.mock.calls.some(([m]) => String(m).includes("periodic-edge: pause refused"))).toBe(true),
    );
  });

  it("toasts a rejected run now with the error text", async () => {
    periodicEdgeRun.mockRejectedValue(new Error("periodic-edge: run refused"));
    const { container } = periodicEdgeView();
    const row = await periodicEdgeRowEl(container, "nightly");
    fireEvent.click(within(row).getByRole("button", { name: "run now" }));
    await waitFor(() => expect(periodicEdgeRun).toHaveBeenCalledWith("nightly"));
    await waitFor(() =>
      expect(periodicEdgeToast.mock.calls.some(([m]) => String(m).includes("periodic-edge: run refused"))).toBe(true),
    );
  });

  it("toasts a rejected confirm delete with the error text", async () => {
    periodicEdgeOp.mockRejectedValue(new Error("periodic-edge: remove refused"));
    const { container } = periodicEdgeView();
    const row = await periodicEdgeRowEl(container, "nightly");
    fireEvent.click(within(row).getByRole("button", { name: "delete" }));
    fireEvent.click(await within(row).findByRole("button", { name: "confirm delete" }));
    await waitFor(() =>
      expect(periodicEdgeToast.mock.calls.some(([m]) => String(m).includes("periodic-edge: remove refused"))).toBe(true),
    );
  });

  // Contract: "submitting calls `addPeriodic` with the five current values ...
  // while a rejection calls `onToast` and keeps the values."
  it("keeps the form's values when add is rejected", async () => {
    periodicEdgeAdd.mockRejectedValue(new Error("periodic-edge: prefix taken"));
    const { container } = periodicEdgeView();
    await periodicEdgeRowEl(container, "nightly");
    const form = container.querySelector<HTMLFormElement>("form#periodic-add");
    expect(form).not.toBeNull();
    const field = (n: string) =>
      form!.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[name="${n}"]`)!;
    expect(field("template").tagName).toBe("TEXTAREA");
    const values: Record<string, string> = {
      name: "weekly report",
      prefix: "nightly",
      at: "7:30",
      days: "mon,fri",
      every: "",
      template: "---\nrepo: kraken\n---\n{{date}}\n",
    };
    for (const [n, v] of Object.entries(values)) fireEvent.change(field(n), { target: { value: v } });
    fireEvent.click(within(form!).getByRole("button", { name: "add" }));
    await waitFor(() => expect(periodicEdgeAdd).toHaveBeenCalledTimes(1));
    expect(periodicEdgeAdd).toHaveBeenCalledWith(values);
    await waitFor(() =>
      expect(periodicEdgeToast.mock.calls.some(([m]) => String(m).includes("periodic-edge: prefix taken"))).toBe(true),
    );
    for (const [n, v] of Object.entries(values)) expect(field(n).value).toBe(v);
  });
});
