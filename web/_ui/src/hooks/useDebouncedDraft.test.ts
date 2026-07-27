import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useDebouncedCallback, useDebouncedDraft } from "./useDebouncedDraft.ts";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("useDebouncedCallback", () => {
  it("fires once, with the LAST arguments, after the delay", () => {
    const fn = vi.fn();
    const { result } = renderHook(() => useDebouncedCallback(fn, 600));
    act(() => {
      result.current.call("a");
      result.current.call("b");
      result.current.call("c");
    });
    expect(fn).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(600));
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn).toHaveBeenCalledWith("c");
  });

  it("flush() fires the pending call immediately", () => {
    const fn = vi.fn();
    const { result } = renderHook(() => useDebouncedCallback(fn, 600));
    act(() => {
      result.current.call("x");
      result.current.flush();
    });
    expect(fn).toHaveBeenCalledWith("x");
    // and the timer is gone, so it does not also fire late
    act(() => vi.advanceTimersByTime(600));
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("flush() with nothing pending is a no-op", () => {
    const fn = vi.fn();
    const { result } = renderHook(() => useDebouncedCallback(fn, 600));
    act(() => result.current.flush());
    expect(fn).not.toHaveBeenCalled();
  });

  it("cancel() drops the pending call unfired", () => {
    const fn = vi.fn();
    const { result } = renderHook(() => useDebouncedCallback(fn, 600));
    act(() => {
      result.current.call("x");
      result.current.cancel();
    });
    act(() => vi.advanceTimersByTime(600));
    expect(fn).not.toHaveBeenCalled();
  });

  it("flushes on unmount — every modal-close path at once", () => {
    // in the vanilla board this was flushSteerDraft()/flushEditorAutosave()
    // called from backdrop, Escape, send, kill, archive AND the force-close
    const fn = vi.fn();
    const { result, unmount } = renderHook(() => useDebouncedCallback(fn, 600));
    act(() => result.current.call("half-typed"));
    unmount();
    expect(fn).toHaveBeenCalledWith("half-typed");
  });

  it("a cancelled call is not resurrected by the unmount flush", () => {
    const fn = vi.fn();
    const { result, unmount } = renderHook(() => useDebouncedCallback(fn, 600));
    act(() => {
      result.current.call("sent already");
      result.current.cancel();
    });
    unmount();
    expect(fn).not.toHaveBeenCalled();
  });

  it("always calls the freshest callback, not the one captured at mount", () => {
    const first = vi.fn();
    const second = vi.fn();
    const { result, rerender } = renderHook(
      ({ fn }: { fn: () => void }) => useDebouncedCallback(fn, 600),
      { initialProps: { fn: first } },
    );
    act(() => result.current.call());
    rerender({ fn: second });
    act(() => vi.advanceTimersByTime(600));
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
  });
});

describe("useDebouncedDraft", () => {
  it("does NOT post the first value — that is the server's own seed", () => {
    const save = vi.fn();
    renderHook(() => useDebouncedDraft("j", "seeded from the server", save));
    act(() => vi.advanceTimersByTime(600));
    expect(save).not.toHaveBeenCalled();
  });

  it("posts a real edit after the debounce", () => {
    const save = vi.fn();
    const { rerender } = renderHook(
      ({ v }: { v: string }) => useDebouncedDraft("j", v, save),
      { initialProps: { v: "" } },
    );
    rerender({ v: "typed" });
    act(() => vi.advanceTimersByTime(600));
    expect(save).toHaveBeenCalledWith("j", "typed");
  });

  it("markSynced stops the server's own value being echoed back", () => {
    const save = vi.fn();
    const { result, rerender } = renderHook(
      ({ v }: { v: string }) => useDebouncedDraft("j", v, save),
      { initialProps: { v: "" } },
    );
    // GET /api/draft lands; the composer takes its text
    act(() => result.current.markSynced("from the server"));
    rerender({ v: "from the server" });
    act(() => vi.advanceTimersByTime(600));
    expect(save).not.toHaveBeenCalled();

    // a genuine edit on top of it still posts
    rerender({ v: "from the server + mine" });
    act(() => vi.advanceTimersByTime(600));
    expect(save).toHaveBeenCalledWith("j", "from the server + mine");
  });

  it("cancel() twice around a send is what stops a late fire resurrecting it", () => {
    const save = vi.fn();
    const { result, rerender } = renderHook(
      ({ v }: { v: string }) => useDebouncedDraft("j", v, save),
      { initialProps: { v: "" } },
    );
    rerender({ v: "the message" });
    // send starts: cancel before posting
    act(() => result.current.cancel());
    // a keystroke arrives DURING the in-flight send
    rerender({ v: "the message!" });
    // send resolves: cancel again — this is the one the first cancel never saw
    act(() => result.current.cancel());
    act(() => vi.advanceTimersByTime(600));
    expect(save).not.toHaveBeenCalled();
  });

  it("switching jobs re-seeds rather than posting the new job's text to it", () => {
    const save = vi.fn();
    const { rerender } = renderHook(
      ({ slug, v }: { slug: string; v: string }) =>
        useDebouncedDraft(slug, v, save),
      { initialProps: { slug: "a", v: "a text" } },
    );
    rerender({ slug: "b", v: "b text" });
    act(() => vi.advanceTimersByTime(600));
    expect(save).not.toHaveBeenCalled();
  });

  it("posts nothing at all without a slug", () => {
    const save = vi.fn();
    const { rerender } = renderHook(
      ({ v }: { v: string }) => useDebouncedDraft(null, v, save),
      { initialProps: { v: "" } },
    );
    rerender({ v: "typed" });
    act(() => vi.advanceTimersByTime(600));
    expect(save).not.toHaveBeenCalled();
  });
});
