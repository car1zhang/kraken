import { fireEvent, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { BackgroundControl } from "./BackgroundControl.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** Captures every fetch and answers 200 {ok:true}. */
function stubFetch() {
  const calls: { url: string; method: string | undefined }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(url), method: init?.method });
      return new Response(JSON.stringify({ ok: true, updated_at: 7 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

/** A mutate that runs the op, like useBoard's does (minus the refetch). */
const passthroughMutate = () =>
  vi.fn(async (fn: () => Promise<unknown>) => {
    await fn();
    return null;
  });

const toggle = () => document.getElementById("bg-toggle")!;
const menu = () => document.querySelector(".bg-menu");
const uploadRow = () => document.getElementById("bg-upload") as HTMLButtonElement | null;
const clearRow = () => document.getElementById("bg-clear") as HTMLButtonElement | null;

describe("BackgroundControl", () => {
  it("is one button; the menu only exists once clicked", async () => {
    render(<BackgroundControl hasBg={false} mutate={passthroughMutate()} />);
    expect(toggle()).toHaveTextContent(/^▨ BG$/);
    expect(menu()).toBeNull();
    await userEvent.click(toggle());
    expect(menu()).not.toBeNull();
    expect(uploadRow()).toHaveTextContent("UPLOAD…");
    expect(clearRow()).toHaveTextContent("CLEAR");
  });

  it("with no background: CLEAR is disabled, UPLOAD is not", async () => {
    render(<BackgroundControl hasBg={false} mutate={passthroughMutate()} />);
    await userEvent.click(toggle());
    expect(uploadRow()!.disabled).toBe(false);
    expect(clearRow()!.disabled).toBe(true);
  });

  it("with a background: the ✓ mark shows and CLEAR is live", async () => {
    render(<BackgroundControl hasBg={true} mutate={passthroughMutate()} />);
    expect(toggle()).toHaveTextContent("▨ BG ✓");
    await userEvent.click(toggle());
    expect(clearRow()!.disabled).toBe(false);
  });

  it("CLEAR DELETEs /api/background through mutate and closes the menu", async () => {
    const calls = stubFetch();
    const mutate = passthroughMutate();
    render(<BackgroundControl hasBg={true} mutate={mutate} />);
    await userEvent.click(toggle());
    await userEvent.click(clearRow()!);
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(calls).toEqual([{ url: "/api/background", method: "DELETE" }]);
    expect(menu()).toBeNull();
  });

  it("UPLOAD closes the menu; a picked file POSTs to /api/background", async () => {
    const calls = stubFetch();
    const mutate = passthroughMutate();
    render(<BackgroundControl hasBg={false} mutate={mutate} />);
    await userEvent.click(toggle());
    await userEvent.click(uploadRow()!);
    expect(menu()).toBeNull();
    const file = new File(["png-bytes"], "bg.png", { type: "image/png" });
    await userEvent.upload(
      document.getElementById("bg-file-input") as HTMLInputElement,
      file,
    );
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(calls).toEqual([{ url: "/api/background", method: "POST" }]);
  });

  it("picking the same file twice uploads twice (the input clears itself)", async () => {
    const calls = stubFetch();
    const mutate = passthroughMutate();
    render(<BackgroundControl hasBg={true} mutate={mutate} />);
    const file = new File(["png-bytes"], "bg.png", { type: "image/png" });
    const input = document.getElementById("bg-file-input") as HTMLInputElement;
    await userEvent.upload(input, file);
    await userEvent.upload(input, file);
    expect(mutate).toHaveBeenCalledTimes(2);
    expect(calls).toHaveLength(2);
  });

  it("an outside mousedown dismisses the menu (useMenuDismiss)", async () => {
    render(<BackgroundControl hasBg={true} mutate={passthroughMutate()} />);
    await userEvent.click(toggle());
    expect(menu()).not.toBeNull();
    fireEvent.mouseDown(document.body);
    expect(menu()).toBeNull();
  });

  it("a second click on the toggle closes the menu — no close-then-reopen", async () => {
    // The fragile invariant here is useMenuDismiss's inside-root early
    // return: the toggle's own mousedown must NOT count as an outside
    // dismissal, or the following click's setOpen(o => !o) would reopen
    // what the mousedown just closed.
    render(<BackgroundControl hasBg={true} mutate={passthroughMutate()} />);
    await userEvent.click(toggle());
    expect(menu()).not.toBeNull();
    await userEvent.click(toggle());
    expect(menu()).toBeNull();
  });

  it("Escape closes the menu", async () => {
    render(<BackgroundControl hasBg={true} mutate={passthroughMutate()} />);
    await userEvent.click(toggle());
    expect(menu()).not.toBeNull();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(menu()).toBeNull();
  });

  it("a hasBg flip under an open menu re-disables CLEAR without closing it", async () => {
    const mutate = passthroughMutate();
    const { rerender } = render(<BackgroundControl hasBg={true} mutate={mutate} />);
    await userEvent.click(toggle());
    expect(clearRow()!.disabled).toBe(false);
    // a board poll flips the payload while the menu is open — the menu must
    // survive the re-render (closing under the pointer is the worse failure)
    rerender(<BackgroundControl hasBg={false} mutate={mutate} />);
    expect(menu()).not.toBeNull();
    expect(clearRow()!.disabled).toBe(true);
    expect(toggle()).toHaveTextContent(/^▨ BG$/);
  });
});
