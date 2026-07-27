import { act, fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { Overlay, usePasteLayer } from "./Overlay.tsx";
import { Toast, showToast } from "./Toast.tsx";

describe("Overlay shell", () => {
  it("carries the id style.css keys its z-index off, and the hidden attribute", () => {
    const { rerender } = render(
      <Overlay id="steer-overlay" open priority={ESC_PRIORITY.steer} onClose={() => {}}>
        <p>body</p>
      </Overlay>,
    );
    const el = document.getElementById("steer-overlay") as HTMLElement;
    expect(el).not.toHaveAttribute("hidden");
    expect(el.className).toBe("overlay");
    expect(el.querySelector(".panel")).not.toBeNull();

    rerender(
      <Overlay
        id="steer-overlay"
        open={false}
        priority={ESC_PRIORITY.steer}
        onClose={() => {}}
      >
        <p>body</p>
      </Overlay>,
    );
    expect(document.getElementById("steer-overlay")).toHaveAttribute("hidden");
  });

  it("unmounts its children while closed — which IS the open-resets-everything rule", () => {
    const { rerender } = render(
      <Overlay id="o" open priority={10} onClose={() => {}}>
        <p>body</p>
      </Overlay>,
    );
    expect(document.body.textContent).toContain("body");
    rerender(
      <Overlay id="o" open={false} priority={10} onClose={() => {}}>
        <p>body</p>
      </Overlay>,
    );
    expect(document.body.textContent).not.toContain("body");
  });

  it("closes on a backdrop click but NOT on a click inside the panel", () => {
    const onClose = vi.fn();
    render(
      <Overlay id="o" open priority={10} onClose={onClose}>
        <button>inside</button>
      </Overlay>,
    );
    fireEvent.click(document.querySelector(".panel") as HTMLElement);
    fireEvent.click(document.querySelector(".panel button") as HTMLElement);
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.click(document.getElementById("o") as HTMLElement);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("a closed overlay does not register in the Escape chain", () => {
    const closed = vi.fn();
    const open = vi.fn();
    render(
      <>
        <Overlay id="a" open={false} priority={ESC_PRIORITY.artifact} onClose={closed}>
          <p>a</p>
        </Overlay>
        <Overlay id="b" open priority={ESC_PRIORITY.editor} onClose={open}>
          <p>b</p>
        </Overlay>
      </>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(closed).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledTimes(1);
  });
});

describe("usePasteLayer", () => {
  function Layer({
    enabled,
    priority,
    handle,
  }: {
    enabled: boolean;
    priority: number;
    handle: (e: ClipboardEvent) => void;
  }) {
    usePasteLayer(enabled, priority, handle);
    return null;
  }

  const paste = () =>
    act(() => {
      document.dispatchEvent(new Event("paste") as ClipboardEvent);
    });

  it("gives the event to the highest-priority enabled layer only", () => {
    const steer = vi.fn();
    const editor = vi.fn();
    render(
      <>
        <Layer enabled priority={ESC_PRIORITY.editor} handle={editor} />
        <Layer enabled priority={ESC_PRIORITY.steer} handle={steer} />
      </>,
    );
    paste();
    // steer wins over editor — a paste can never land in both
    expect(steer).toHaveBeenCalledTimes(1);
    expect(editor).not.toHaveBeenCalled();
  });

  it("falls through to the editor when steer is not registered", () => {
    const editor = vi.fn();
    render(<Layer enabled priority={ESC_PRIORITY.editor} handle={editor} />);
    paste();
    expect(editor).toHaveBeenCalledTimes(1);
  });

  it("a disabled layer receives nothing", () => {
    const editor = vi.fn();
    render(<Layer enabled={false} priority={ESC_PRIORITY.editor} handle={editor} />);
    paste();
    expect(editor).not.toHaveBeenCalled();
  });
});

describe("Toast", () => {
  it("shows a message, then hides it after 4s while keeping the text through the fade", () => {
    vi.useFakeTimers();
    render(<Toast />);
    const el = document.getElementById("toast") as HTMLElement;
    expect(el.className).toBe("toast");

    act(() => showToast("reorder failed — reverted"));
    expect(el.className).toBe("toast show");
    expect(el.textContent).toBe("reorder failed — reverted");

    act(() => vi.advanceTimersByTime(4000));
    expect(el.className).toBe("toast");
    // the text survives the transition rather than blanking mid-fade
    expect(el.textContent).toBe("reorder failed — reverted");
    vi.useRealTimers();
  });

  it("a second identical message restarts the timer rather than inheriting the first's", () => {
    vi.useFakeTimers();
    render(<Toast />);
    const el = document.getElementById("toast") as HTMLElement;
    act(() => showToast("same"));
    act(() => vi.advanceTimersByTime(3000));
    act(() => showToast("same"));
    act(() => vi.advanceTimersByTime(3000));
    expect(el.className).toBe("toast show");
    act(() => vi.advanceTimersByTime(1000));
    expect(el.className).toBe("toast");
    vi.useRealTimers();
  });

  it("showToast before any Toast has mounted is a no-op, not a throw", () => {
    expect(() => showToast("nobody listening")).not.toThrow();
  });
});
