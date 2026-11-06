import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WebAgent } from "../../api/types.ts";
import { MAP_ACTORS_KEY, actorsShown } from "../../lib/agentmap/prefs.ts";
import { makeJob } from "../../test/fixtures.ts";
import { SteerCast } from "./SteerCast.tsx";

// SteerCast.test.tsx — the job's characters in the steer modal: when the
// strip is there, that it draws, and that it stops.

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem(MAP_ACTORS_KEY);
});

const live = (over = {}) => makeJob({ slug: "j", state: "working", live: true, ...over });
const agent = (name: string): WebAgent => ({ name, role: "", working: true, kind: "teammate" }) as WebAgent;

/** A 2D context that counts what the strip draws; every other call is a no-op. */
function fakeContext() {
  const seen = { clears: 0, fills: 0, arcs: 0 };
  const store: Record<string | symbol, unknown> = { globalAlpha: 1 };
  const ctx = new Proxy(store, {
    get: (t, p) => {
      if (p in t) return t[p];
      if (p === "clearRect") return () => void seen.clears++;
      if (p === "fillRect") return () => void seen.fills++;
      if (p === "arc") return () => void seen.arcs++;
      return () => {};
    },
    set: (t, p, v) => {
      t[p] = v;
      return true;
    },
  });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => ctx as never);
  return seen;
}

const noContext = () => vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);

describe("actorsShown", () => {
  it("is on by default and off only for the stored value off", () => {
    expect(actorsShown()).toBe(true);
    localStorage.setItem(MAP_ACTORS_KEY, "off");
    expect(actorsShown()).toBe(false);
    localStorage.setItem(MAP_ACTORS_KEY, "junk");
    expect(actorsShown()).toBe(true);
  });

  it("is on where storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(actorsShown()).toBe(true);
  });
});

describe("SteerCast", () => {
  it("renders one labelled canvas for the job", () => {
    noContext();
    const { container } = render(<SteerCast job={live()} open />);
    const cv = container.querySelector("#steer-cast canvas") as HTMLCanvasElement;
    expect(cv).not.toBeNull();
    expect(cv.getAttribute("role")).toBe("img");
    expect(cv.getAttribute("aria-label")).toContain("j");
  });

  it("renders nothing when the characters are switched off", () => {
    localStorage.setItem(MAP_ACTORS_KEY, "off");
    const getContext = noContext();
    const { container } = render(<SteerCast job={live()} open />);
    expect(container.querySelector("#steer-cast")).toBeNull();
    expect(getContext).not.toHaveBeenCalled();
  });

  it("reads the switch again when the modal reopens", () => {
    noContext();
    const job = live();
    const { container, rerender } = render(<SteerCast job={job} open />);
    expect(container.querySelector("#steer-cast")).not.toBeNull();
    localStorage.setItem(MAP_ACTORS_KEY, "off");
    // a re-render while open keeps what it showed
    rerender(<SteerCast job={job} open />);
    expect(container.querySelector("#steer-cast")).not.toBeNull();
    rerender(<SteerCast job={job} open={false} />);
    rerender(<SteerCast job={job} open />);
    expect(container.querySelector("#steer-cast")).toBeNull();
  });

  it("draws the ledge and the cast, frame after frame", async () => {
    const seen = fakeContext();
    render(<SteerCast job={live({ roster: [agent("athena-style")] })} open />);
    await waitFor(() => expect(seen.clears).toBeGreaterThan(1));
    // one ledge per frame, and the characters' round parts
    expect(seen.fills).toBeGreaterThan(0);
    expect(seen.arcs).toBeGreaterThan(0);
  });

  it("draws nothing while the modal is closed", async () => {
    const seen = fakeContext();
    render(<SteerCast job={live()} open={false} />);
    await new Promise((r) => setTimeout(r, 120));
    expect(seen.clears).toBe(0);
  });

  it("stops drawing once it is unmounted", async () => {
    const seen = fakeContext();
    const { unmount } = render(<SteerCast job={live()} open />);
    await waitFor(() => expect(seen.clears).toBeGreaterThan(0));
    unmount();
    const at = seen.clears;
    await new Promise((r) => setTimeout(r, 150));
    expect(seen.clears).toBe(at);
  });

  it("stays up without drawing where a canvas has no 2D context", async () => {
    noContext();
    const { container } = render(<SteerCast job={live()} open />);
    await new Promise((r) => setTimeout(r, 80));
    expect(container.querySelector("#steer-cast canvas")).not.toBeNull();
  });

  it("survives a getContext that throws", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => {
      throw new Error("no canvas");
    });
    expect(() => render(<SteerCast job={live()} open />)).not.toThrow();
  });

  it("follows a change of job and of roster without throwing", async () => {
    const seen = fakeContext();
    const { rerender } = render(<SteerCast job={live()} open />);
    await waitFor(() => expect(seen.clears).toBeGreaterThan(0));
    rerender(<SteerCast job={live({ roster: [agent("hephaestus-web"), agent("athena-logic")] })} open />);
    rerender(<SteerCast job={makeJob({ slug: "other", state: "draft" })} open />);
    const at = seen.clears;
    await waitFor(() => expect(seen.clears).toBeGreaterThan(at));
  });
});
