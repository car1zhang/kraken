import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { WebJob } from "../../../api/types.ts";
import { makeJob } from "../../../test/fixtures.ts";
import { openSpawned, SpawnChips, spawnLabel, spawnTitle } from "./SpawnChips.tsx";

// SpawnChips.tsx: one chip per cell this cell filed and one for its filer
// (52-testing-plan.md's Steer modal row, applied to the card too per the
// "chips on both" steer). The label is `↳ slug` / `↰ slug`, the state is said
// by class, a click opens the job as its card would, and an archived link or a
// remote card degrades to an inert span.

function renderRow(job: WebJob, opener = { onOpenSteer: vi.fn(), onOpenEditor: vi.fn() }) {
  const { container } = render(<SpawnChips job={job} opener={opener} />);
  return { row: container.querySelector<HTMLElement>(".spawned"), opener };
}

describe("spawnLabel / spawnTitle", () => {
  it("prefixes children and the parent differently", () => {
    expect(spawnLabel({ slug: "child", state: "working" }, "child")).toBe("↳ child");
    expect(spawnLabel({ slug: "parent", state: "done" }, "parent")).toBe("↰ parent");
    expect(spawnTitle({ slug: "child", state: "working" }, "child")).toBe(
      "child · spawned by this cell · working",
    );
  });
});

describe("openSpawned", () => {
  it("maps the link's state to the card's own click", () => {
    const o = { onOpenSteer: vi.fn(), onOpenEditor: vi.fn() };
    openSpawned("a", "working", o);
    openSpawned("b", "blocked", o);
    openSpawned("c", "done", o);
    openSpawned("d", "draft", o);
    expect(o.onOpenSteer.mock.calls).toEqual([
      ["a", "steer"],
      ["b", "unblock"],
      ["c", "view"],
    ]);
    expect(o.onOpenEditor.mock.calls).toEqual([["d"]]);
  });
});

describe("SpawnChips", () => {
  it("renders nothing when the job carries neither key", () => {
    expect(renderRow(makeJob()).row).toBeNull();
    expect(renderRow(makeJob({ spawned: [] })).row).toBeNull();
  });

  it("renders the parent first, then each child, state by class", () => {
    const { row } = renderRow(
      makeJob({
        spawned_by: { slug: "parent", state: "done" },
        spawned: [
          { slug: "c1", state: "working" },
          { slug: "c2", state: "blocked" },
          { slug: "c3", state: "draft" },
          { slug: "c4", state: "archived" },
        ],
      }),
    );
    const chips = [...row!.querySelectorAll<HTMLElement>(".spawn-chip")];
    expect(chips.map((c) => c.textContent)).toEqual(["↰ parent", "↳ c1", "↳ c2", "↳ c3", "↳ c4"]);
    expect(chips.map((c) => c.className)).toEqual([
      "spawn-chip spawn-done",
      "spawn-chip spawn-working",
      "spawn-chip spawn-blocked",
      "spawn-chip spawn-draft",
      "spawn-chip spawn-archived inert",
    ]);
    // openable links are buttons, the archived one a span
    expect(chips.slice(0, 4).every((c) => c.tagName === "BUTTON")).toBe(true);
    expect(chips[4]?.tagName).toBe("SPAN");
  });

  it("opens the clicked job as its card would and stops the card click", () => {
    const opener = { onOpenSteer: vi.fn(), onOpenEditor: vi.fn() };
    // the card's whole-card click is a React handler on an ancestor, so the
    // guard is asserted against one of those, not a native listener
    const outer = vi.fn();
    const { container } = render(
      <div onClick={outer}>
        <SpawnChips
          job={makeJob({
            spawned_by: { slug: "parent", state: "done" },
            spawned: [{ slug: "c1", state: "working" }, { slug: "c3", state: "draft" }],
          })}
          opener={opener}
        />
      </div>,
    );
    const chips = [...container.querySelectorAll<HTMLElement>(".spawn-chip")];
    fireEvent.click(chips[0]!);
    fireEvent.click(chips[1]!);
    fireEvent.click(chips[2]!);
    expect(opener.onOpenSteer.mock.calls).toEqual([
      ["parent", "view"],
      ["c1", "steer"],
    ]);
    expect(opener.onOpenEditor.mock.calls).toEqual([["c3"]]);
    expect(outer).not.toHaveBeenCalled();
  });

  it("renders inert spans on a remote card", () => {
    const { row, opener } = renderRow(
      makeJob({
        owner: { node_id: "n1", label: "studio", operator: "mj", online: true },
        spawned: [{ slug: "c1", state: "working" }],
      }),
    );
    const chip = row!.querySelector<HTMLElement>(".spawn-chip")!;
    expect(chip.tagName).toBe("SPAN");
    expect(chip.className).toBe("spawn-chip spawn-working inert");
    fireEvent.click(chip);
    expect(opener.onOpenSteer).not.toHaveBeenCalled();
  });

  it("renders inert spans with no opener", () => {
    const { container } = render(
      <SpawnChips job={makeJob({ spawned: [{ slug: "c1", state: "working" }] })} />,
    );
    expect(container.querySelector(".spawn-chip")?.tagName).toBe("SPAN");
  });
});
