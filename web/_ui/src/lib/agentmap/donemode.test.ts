import { afterEach, describe, expect, it } from "vitest";

import type { Palette } from "./draw.ts";
import { drawMinimap } from "./minimap.ts";
import { MAP_DONE_KEY, doneMode, nextDoneMode, setDoneMode } from "./prefs.ts";

// donemode.test.ts — the map's done level as a stored preference, and what
// the overview does with it.

afterEach(() => {
  localStorage.removeItem(MAP_DONE_KEY);
});

describe("the done level preference", () => {
  it("is fade until another level is chosen", () => {
    expect(MAP_DONE_KEY).toBe("kraken.mapDone");
    expect(doneMode()).toBe("fade");
  });

  it("stores show and hide by name and fade as no key at all", () => {
    setDoneMode("hide");
    expect(localStorage.getItem(MAP_DONE_KEY)).toBe("hide");
    expect(doneMode()).toBe("hide");
    setDoneMode("show");
    expect(localStorage.getItem(MAP_DONE_KEY)).toBe("show");
    expect(doneMode()).toBe("show");
    setDoneMode("fade");
    expect(localStorage.getItem(MAP_DONE_KEY)).toBeNull();
    expect(doneMode()).toBe("fade");
  });

  it("reads any other stored value as fade", () => {
    for (const v of ["", "fade", "off", "HIDE", "Show", "true", "0"]) {
      localStorage.setItem(MAP_DONE_KEY, v);
      expect(doneMode(), JSON.stringify(v)).toBe("fade");
    }
  });

  it("steps show, fade, hide and round again", () => {
    expect(nextDoneMode("show")).toBe("fade");
    expect(nextDoneMode("fade")).toBe("hide");
    expect(nextDoneMode("hide")).toBe("show");
  });
});

describe("the overview's done rectangles", () => {
  /** A context that records the alpha and fill each rectangle was drawn with. */
  function recorder() {
    const rects: Array<{ alpha: number; fill: string }> = [];
    const c = {
      globalAlpha: 1,
      fillStyle: "",
      strokeStyle: "",
      lineWidth: 1,
      clearRect() {},
      strokeRect() {},
      fillRect() {
        rects.push({ alpha: c.globalAlpha, fill: String(c.fillStyle) });
      },
    };
    return { c: c as unknown as CanvasRenderingContext2D, rects, raw: c };
  }
  const C = { b0: "back", fg: "fg", ok: "ok", ac: "ac", muted: "muted" } as unknown as Palette;
  const nodes = [
    { key: "w", x: 0, y: 0, w: 300, h: 100 },
    { key: "d", x: 400, y: 0, w: 300, h: 100 },
  ];
  const statusOf = (key: string) => (key === "d" ? "done" : "working");
  const view = { x: 0, y: 0, w: 800, h: 600 };
  const frame = { scale: 0.1, ox: 0, oy: 0 };
  const size = { w: 184, h: 120 };

  it("draws every card at full strength unless told to fade done ones", () => {
    const { c, rects } = recorder();
    drawMinimap(c, C, nodes, statusOf, view, frame, size);
    // the backdrop, then the two cards
    expect(rects.slice(1)).toEqual([
      { alpha: 1, fill: "ac" },
      { alpha: 1, fill: "ok" },
    ]);
  });

  it("fades only the done card, and leaves the context's alpha at 1", () => {
    const { c, rects, raw } = recorder();
    drawMinimap(c, C, nodes, statusOf, view, frame, size, true);
    const [working, done] = rects.slice(1);
    expect(working).toEqual({ alpha: 1, fill: "ac" });
    expect(done!.fill).toBe("ok");
    expect(done!.alpha).toBeGreaterThan(0);
    expect(done!.alpha).toBeLessThan(1);
    expect(raw.globalAlpha).toBe(1);
  });

  it("restores the alpha when the done card is drawn last or first", () => {
    const { c, rects, raw } = recorder();
    drawMinimap(c, C, [...nodes].reverse(), statusOf, view, frame, size, true);
    const [done, working] = rects.slice(1);
    expect(done!.alpha).toBeLessThan(1);
    expect(working!.alpha).toBe(1);
    expect(raw.globalAlpha).toBe(1);
  });
});
