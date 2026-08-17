import { render } from "@testing-library/react";
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

describe("BackgroundControl", () => {
  it("with no background: upload button only, no remove, no ✓", () => {
    render(<BackgroundControl hasBg={false} mutate={passthroughMutate()} />);
    expect(document.getElementById("bg-upload")).toHaveTextContent(/^▨ BG$/);
    expect(document.getElementById("bg-remove")).toBeNull();
  });

  it("with a background: the ✓ mark and the remove button appear", () => {
    render(<BackgroundControl hasBg={true} mutate={passthroughMutate()} />);
    expect(document.getElementById("bg-upload")).toHaveTextContent("▨ BG ✓");
    expect(document.getElementById("bg-remove")).not.toBeNull();
  });

  it("picking a file POSTs it to /api/background through mutate", async () => {
    const calls = stubFetch();
    const mutate = passthroughMutate();
    render(<BackgroundControl hasBg={false} mutate={mutate} />);
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

  it("remove DELETEs /api/background through mutate", async () => {
    const calls = stubFetch();
    const mutate = passthroughMutate();
    render(<BackgroundControl hasBg={true} mutate={mutate} />);
    await userEvent.click(document.getElementById("bg-remove")!);
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(calls).toEqual([{ url: "/api/background", method: "DELETE" }]);
  });
});
