// Lead-authored (sighted) tests for the IsoSceneView wiring — the React half
// the blind isoscene suites deliberately do not cover. Authorship was NOT
// split for this file: the lead implemented the component and wrote these,
// which 90-build-report.md records.
import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import bespoke from "../../lib/isoscene/fixtures/bespoke.html?raw";
import mini from "../../lib/isoscene/fixtures/mini.html?raw";
import { IsoSceneView } from "./IsoSceneView.tsx";

afterEach(cleanup);

describe("IsoSceneView", () => {
  it("mounts the compiled scene for a parseable card: one svg, every entity group present", () => {
    const { container } = render(<IsoSceneView html={mini} onOpenRef={() => {}} />);
    const svg = container.querySelector("svg");
    expect(svg).not.toBeNull();
    expect(svg?.getAttribute("viewBox")).toMatch(/^0 0 /);
    // mini holds 5 EntityNodes (proc, group, store, node, pill) per the
    // contract's executed-facts table
    expect(container.querySelectorAll("[data-entity]").length).toBe(5);
  });

  it("decorates every [data-code] element with a hover title", () => {
    const { container } = render(<IsoSceneView html={mini} onOpenRef={() => {}} />);
    const coded = Array.from(container.querySelectorAll("[data-code]"));
    expect(coded.length).toBeGreaterThan(0);
    for (const el of coded) {
      expect(el.getAttribute("title")).toBe(el.getAttribute("data-code"));
    }
  });

  it("routes a click inside a data-code group to onOpenRef with that ref", () => {
    const onOpenRef = vi.fn();
    const { container } = render(<IsoSceneView html={mini} onOpenRef={onOpenRef} />);
    const group = container.querySelector('[data-code="cmd/kraken/cmd_serve.go:37"]');
    expect(group).not.toBeNull();
    // the click lands on an inner shape, exactly how a real pointer does; the
    // closest('[data-code]') walk is what must recover the ref
    fireEvent.click(group?.firstElementChild ?? (group as Element));
    expect(onOpenRef).toHaveBeenCalledTimes(1);
    expect(onOpenRef.mock.calls[0]?.[0]?.path).toBe("cmd/kraken/cmd_serve.go");
  });

  it("a click outside any data-code group calls nothing", () => {
    const onOpenRef = vi.fn();
    const { container } = render(<IsoSceneView html={mini} onOpenRef={onOpenRef} />);
    const svg = container.querySelector("svg");
    fireEvent.click(svg as Element);
    expect(onOpenRef).not.toHaveBeenCalled();
  });

  it("a card that stops compiling renders its reason, never a blank panel", () => {
    // reachable only when the card changed between the availability probe and
    // this render — the caller normally never mounts this for a bespoke card
    const { container } = render(<IsoSceneView html={bespoke} onOpenRef={() => {}} />);
    expect(container.querySelector("svg")).toBeNull();
    expect(container.textContent).toContain("no longer compiles");
    expect(container.textContent).toContain("starburst");
  });
});
