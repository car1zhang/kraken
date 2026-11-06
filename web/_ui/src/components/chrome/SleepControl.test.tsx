import { render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SleepControl } from "./SleepControl.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

interface Call {
  method: string;
  body: unknown;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

/**
 * A fake /api/sleep holding one machine state. A GET reads it; a POST sets it
 * to the body's `disabled` unless `refuse` is set, in which case it answers
 * 409 with that text and changes nothing. `getStatus` other than 200 fails
 * every GET.
 */
function fakeSleep(opts: { disabled: boolean; refuse?: string; getStatus?: number }) {
  const state = { disabled: opts.disabled };
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      expect(String(url)).toBe("/api/sleep");
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, body });
      if (method === "POST") {
        if (opts.refuse) return json(409, { error: opts.refuse });
        state.disabled = (body as { disabled: boolean }).disabled;
        return json(200, { disabled: state.disabled });
      }
      if (opts.getStatus && opts.getStatus !== 200) {
        return json(opts.getStatus, { error: "nope" });
      }
      return json(200, { disabled: state.disabled });
    }),
  );
  return { state, calls };
}

const btn = () => document.getElementById("sleep-toggle") as HTMLButtonElement | null;
const posts = (calls: Call[]) => calls.filter((c) => c.method === "POST");

describe("SleepControl", () => {
  it("renders nothing until the state has been read", async () => {
    let answer: (r: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>((resolve) => (answer = resolve))),
    );
    render(<SleepControl onToast={vi.fn()} />);
    expect(btn()).toBeNull();
    answer(json(200, { disabled: false }));
    await waitFor(() => expect(btn()).not.toBeNull());
  });

  it("says SLEEPABLE while the Mac may sleep", async () => {
    fakeSleep({ disabled: false });
    render(<SleepControl onToast={vi.fn()} />);
    await waitFor(() => expect(btn()).toHaveTextContent("☾ SLEEPABLE"));
    expect(btn()).toHaveAttribute("aria-pressed", "false");
    expect(btn()).not.toHaveClass("sleep-held");
  });

  it("says ALWAYS ON, marked, while sleep is disabled", async () => {
    fakeSleep({ disabled: true });
    render(<SleepControl onToast={vi.fn()} />);
    await waitFor(() => expect(btn()).toHaveTextContent("☀ ALWAYS ON"));
    expect(btn()).toHaveAttribute("aria-pressed", "true");
    expect(btn()).toHaveClass("sleep-held");
  });

  it("a click asks for the opposite state by name and shows what came back", async () => {
    const { calls } = fakeSleep({ disabled: false });
    const onToast = vi.fn();
    render(<SleepControl onToast={onToast} />);
    await waitFor(() => expect(btn()).toHaveTextContent("SLEEPABLE"));

    await userEvent.click(btn()!);
    await waitFor(() => expect(btn()).toHaveTextContent("ALWAYS ON"));
    expect(posts(calls)).toEqual([{ method: "POST", body: { disabled: true } }]);

    await userEvent.click(btn()!);
    await waitFor(() => expect(btn()).toHaveTextContent("SLEEPABLE"));
    expect(posts(calls).map((c) => c.body)).toEqual([{ disabled: true }, { disabled: false }]);
    expect(onToast).not.toHaveBeenCalled();
  });

  it("a refused switch toasts the server's reason and keeps the true state", async () => {
    const { calls } = fakeSleep({ disabled: false, refuse: "pmset needs passwordless sudo" });
    const onToast = vi.fn();
    render(<SleepControl onToast={onToast} />);
    await waitFor(() => expect(btn()).toHaveTextContent("SLEEPABLE"));

    await userEvent.click(btn()!);
    await waitFor(() => expect(onToast).toHaveBeenCalledWith("✕ pmset needs passwordless sudo"));
    expect(posts(calls)).toHaveLength(1);
    // the label is the poll's, so a refusal cannot leave it claiming ALWAYS ON
    await waitFor(() => expect(btn()).not.toBeDisabled());
    expect(btn()).toHaveTextContent("SLEEPABLE");
  });

  it("is disabled while a switch is in flight, so a double click sends one", async () => {
    const state = { disabled: false };
    let release: () => void = () => {};
    const postBodies: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === "POST") {
          postBodies.push(JSON.parse(String(init.body)));
          await new Promise<void>((resolve) => (release = resolve));
          state.disabled = true;
        }
        return json(200, { disabled: state.disabled });
      }),
    );
    render(<SleepControl onToast={vi.fn()} />);
    await waitFor(() => expect(btn()).toHaveTextContent("SLEEPABLE"));

    await userEvent.click(btn()!);
    await waitFor(() => expect(btn()).toBeDisabled());
    await userEvent.click(btn()!);
    expect(postBodies).toEqual([{ disabled: true }]);

    release();
    await waitFor(() => expect(btn()).toHaveTextContent("ALWAYS ON"));
    expect(btn()).not.toBeDisabled();
    expect(postBodies).toHaveLength(1);
  });

  it("stays absent on a board that may not read the route", async () => {
    const { calls } = fakeSleep({ disabled: false, getStatus: 401 });
    render(<SleepControl onToast={vi.fn()} />);
    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    // let the rejected read settle before asserting the absence
    await new Promise((r) => setTimeout(r, 0));
    expect(btn()).toBeNull();
  });
});
