import { afterEach, describe, expect, it } from "vitest";

import { stampBackground } from "./background.ts";

afterEach(() => stampBackground(undefined));

describe("stampBackground", () => {
  it("stamps the attribute and the cache-busted url", () => {
    stampBackground(1234);
    const el = document.documentElement;
    expect(el.dataset.bg).toBe("on");
    expect(el.style.getPropertyValue("--bg-image")).toBe(
      'url("/api/background?v=1234")',
    );
  });

  it("scrubs BOTH halves when the background goes away", () => {
    stampBackground(1234);
    stampBackground(undefined);
    const el = document.documentElement;
    expect(el.dataset.bg).toBeUndefined();
    expect(el.style.getPropertyValue("--bg-image")).toBe("");
  });

  it("a replaced image moves the url with the stamp", () => {
    stampBackground(1);
    stampBackground(2);
    expect(document.documentElement.style.getPropertyValue("--bg-image")).toBe(
      'url("/api/background?v=2")',
    );
  });
});
