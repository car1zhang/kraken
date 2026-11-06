import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import type { WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { HOLD_MS, leadPose, leadPoseExpiry, newestCall } from "./activity.ts";
import { ACT_HUE, SHIP_LIGHT_HUE } from "./draw.ts";
import { freeSpot, overlaps, settle, type MapNode } from "./layout.ts";

// maprepair.test.ts — regression pins for the agent map's review round one
// (amendment 6). Named outside the blind suites' prefix on purpose.

/**
 * The `[data-act="<id>"] { --act: <value>; }` rules style.css carries, by id.
 * A rule naming several ids (`[data-act="talk"], [data-act="hear"]`) gives
 * each of them the value.
 */
function cssActHues(): Map<string, string> {
  // relative to the vitest root (web/_ui), as palette.test.ts reads it
  const css = readFileSync("src/styles/style.css", "utf8");
  const out = new Map<string, string>();
  for (const m of css.matchAll(/^((?:\[data-act="[a-z]+"\](?:,\s*)?)+)\s*\{\s*--act:\s*([^;]+);/gm)) {
    const value = m[2]!.trim();
    for (const id of m[1]!.matchAll(/data-act="([a-z]+)"/g)) out.set(id[1]!, value);
  }
  return out;
}

describe("activity hues: draw.ts and style.css agree", () => {
  // TestJobPaletteMatchesCSS's pattern: the canvas characters read ACT_HUE,
  // the card's ribbon and the steer list read style.css, and a recolour of
  // one without the other makes the same activity two colours on one card.
  it("gives every activity the same hue in both", () => {
    const css = cssActHues();
    for (const [id, hue] of Object.entries(ACT_HUE)) {
      expect(css.get(id), id).toBe(hue);
    }
  });

  it("gives `hear` talk's hue, as hueOf does", () => {
    expect(cssActHues().get("hear")).toBe(ACT_HUE.talk);
  });

  it("carries the same light-theme hue for ship", () => {
    const css = readFileSync("src/styles/style.css", "utf8");
    expect(css).toContain(`:root[data-theme="light"] [data-act="ship"] { --act: ${SHIP_LIGHT_HUE}; }`);
  });

  it("colours parked like the frame and the dino, not muted", () => {
    // the frame's recipe (`.map-card[data-status="parked"]`) and draw.ts'
    // Palette.parked (blocked's warn 45% of the way to g1) are the same mix
    expect(cssActHues().get("parked")).toBe("color-mix(in srgb, var(--warn) 55%, var(--g1))");
    const css = readFileSync("src/styles/style.css", "utf8");
    expect(css).toContain(
      '.map-card[data-status="parked"] { --map-sc: color-mix(in srgb, var(--warn) 55%, var(--g1)); }',
    );
  });
});

function working(events: NonNullable<WebJob["activity"]>["events"]): WebJob {
  return makeJob({ slug: "w", state: "working", live: true, activity: { act: "edit", at: 1, events } });
}

describe("leadPoseExpiry", () => {
  it("names the instant a closed call's hold runs out, after which leadPose is think", () => {
    const job = working([{ id: "1", t: 10_000, act: "edit", label: "Edit a.go", dur_ms: 500 }]);
    const end = 10_000 + 500 + HOLD_MS;
    const at = leadPoseExpiry(job, 12_000);
    expect(at).toBe(end + 1);
    expect(leadPose(job, end)).toBe("edit");
    expect(leadPose(job, at!)).toBe("think");
  });

  it("is null when time alone cannot change the pose", () => {
    const open = working([{ id: "1", t: 10_000, act: "test", label: "go test", open: true }]);
    expect(leadPoseExpiry(open, 12_000)).toBeNull();
    const other = working([{ id: "1", t: 10_000, act: "other", label: "Skill" }]);
    expect(leadPoseExpiry(other, 10_001)).toBeNull();
    const ran = working([{ id: "1", t: 10_000, act: "edit", label: "x" }]);
    expect(leadPoseExpiry(ran, 10_000 + HOLD_MS + 1)).toBeNull();
    expect(leadPoseExpiry(makeJob({ slug: "d", state: "done" }), 0)).toBeNull();
  });

  it("reads the same event the band's label does", () => {
    const job = working([
      { id: "1", t: 1, act: "edit", label: "Edit a.go" },
      { id: "2", t: 2, act: "hear", label: "from heph", from: "teammate", who: "heph" },
    ]);
    expect(newestCall(job.activity?.events)?.label).toBe("Edit a.go");
  });
});

// ---- amendment 7: a set of held keys, and the arriving card's way out

const node = (key: string, x: number, y: number): MapNode => ({ key, x, y, w: 300, h: 200 });

describe("settle with a held set", () => {
  it("never moves a held node, however long it runs", () => {
    // a row of showing cards, and two arrivals dropped on top of them
    const nodes = [node("a", 0, 0), node("b", 400, 0), node("c", 800, 0), node("x", 10, 10), node("y", 410, 20)];
    const held = new Set(["a", "b", "c"]);
    const at = new Map(nodes.map((n) => [n.key, [n.x, n.y]]));
    for (let i = 0; i < 400 && settle(nodes, held); i++) {
      for (const k of held) expect([nodes.find((n) => n.key === k)!.x, nodes.find((n) => n.key === k)!.y]).toEqual(at.get(k));
    }
    for (const k of held) expect([nodes.find((n) => n.key === k)!.x, nodes.find((n) => n.key === k)!.y]).toEqual(at.get(k));
    // the arrivals moved instead, and nothing overlaps
    expect(nodes.find((n) => n.key === "x")!.y).not.toBe(10);
    for (const p of nodes) for (const q of nodes) if (p !== q) expect(overlaps(p, q)).toBe(false);
  });

  it("leaves two overlapping held nodes alone and does not call it movement", () => {
    const nodes = [node("a", 0, 0), node("b", 20, 20)];
    expect(settle(nodes, new Set(["a", "b"]))).toBe(false);
    expect([nodes[0]!.x, nodes[0]!.y, nodes[1]!.x, nodes[1]!.y]).toEqual([0, 0, 20, 20]);
  });

  it("still takes a single held key", () => {
    const nodes = [node("a", 0, 0), node("b", 20, 20)];
    expect(settle(nodes, "a")).toBe(true);
    expect([nodes[0]!.x, nodes[0]!.y]).toEqual([0, 0]);
  });
});

describe("freeSpot", () => {
  it("moves a card wedged among others to a nearby spot clear of all of them", () => {
    const others = [node("a", 0, 0), node("b", 400, 0), node("c", 0, 400), node("d", 400, 400)];
    const n = node("x", 200, 200);
    expect(freeSpot(n, [...others, n])).toBe(true);
    for (const o of others) expect(overlaps(n, o)).toBe(false);
    expect(others.map((o) => [o.x, o.y])).toEqual([[0, 0], [400, 0], [0, 400], [400, 400]]);
  });

  it("leaves a card that is already clear where it is", () => {
    const n = node("x", 5000, 5000);
    expect(freeSpot(n, [node("a", 0, 0), n])).toBe(false);
    expect([n.x, n.y]).toEqual([5000, 5000]);
  });
});
