import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ESC_PRIORITY, useEscapeLayer } from "./useEscapeStack.ts";

function press() {
  document.dispatchEvent(
    new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
  );
}

describe("useEscapeLayer", () => {
  it("reproduces the vanilla ordering exactly", () => {
    // app.js:3422-3429: artifact → thread → steer → editor → usage
    expect(ESC_PRIORITY.artifact).toBeGreaterThan(ESC_PRIORITY.thread);
    expect(ESC_PRIORITY.thread).toBeGreaterThan(ESC_PRIORITY.steer);
    expect(ESC_PRIORITY.steer).toBeGreaterThan(ESC_PRIORITY.editor);
    expect(ESC_PRIORITY.editor).toBeGreaterThan(ESC_PRIORITY.usage);
  });

  it("closes exactly ONE layer per press — the highest open one", () => {
    const steer = vi.fn();
    const artifact = vi.fn();
    const a = renderHook(() =>
      useEscapeLayer(true, ESC_PRIORITY.steer, steer),
    );
    const b = renderHook(() =>
      useEscapeLayer(true, ESC_PRIORITY.artifact, artifact),
    );

    press();
    expect(artifact).toHaveBeenCalledTimes(1);
    expect(steer).not.toHaveBeenCalled();

    // the artifact viewer closing deregisters it; the next press reaches steer
    b.unmount();
    press();
    expect(steer).toHaveBeenCalledTimes(1);
    expect(artifact).toHaveBeenCalledTimes(1);

    a.unmount();
  });

  it("registration order does not matter, only priority", () => {
    const editor = vi.fn();
    const thread = vi.fn();
    const a = renderHook(() =>
      useEscapeLayer(true, ESC_PRIORITY.thread, thread),
    );
    const b = renderHook(() =>
      useEscapeLayer(true, ESC_PRIORITY.editor, editor),
    );
    press();
    expect(thread).toHaveBeenCalledTimes(1);
    expect(editor).not.toHaveBeenCalled();
    a.unmount();
    b.unmount();
  });

  it("a CLOSED layer never swallows the press meant for the one behind it", () => {
    const steer = vi.fn();
    const artifact = vi.fn();
    const a = renderHook(() =>
      useEscapeLayer(true, ESC_PRIORITY.steer, steer),
    );
    // open:false — registered nowhere
    const b = renderHook(() =>
      useEscapeLayer(false, ESC_PRIORITY.artifact, artifact),
    );
    press();
    expect(artifact).not.toHaveBeenCalled();
    expect(steer).toHaveBeenCalledTimes(1);
    a.unmount();
    b.unmount();
  });

  it("ignores every other key", () => {
    const close = vi.fn();
    const a = renderHook(() => useEscapeLayer(true, ESC_PRIORITY.usage, close));
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Esc" }));
    expect(close).not.toHaveBeenCalled();
    a.unmount();
  });

  it("removes the document listener once the last layer unregisters", () => {
    const remove = vi.spyOn(document, "removeEventListener");
    const a = renderHook(() =>
      useEscapeLayer(true, ESC_PRIORITY.usage, () => {}),
    );
    a.unmount();
    expect(remove).toHaveBeenCalledWith("keydown", expect.any(Function));
    remove.mockRestore();
  });
});
