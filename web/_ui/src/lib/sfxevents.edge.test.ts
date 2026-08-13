// sfxevents.edge.test.ts — BLIND edge tests for the board-event sounds +
// cyberpunk-night bus (artifacts/50-contract.md, slices A/B/C). Authored from
// the frozen contract and 50-components.md with no sight of boardEvents.ts,
// sfx.ts, useBoardEvents.ts, useBoard.ts or App.tsx.
//
// Dimension: boundaries, error paths, refusals. Everything here is a thing
// that must NOT happen — the diff that must stay quiet, the sound that must
// drop, the context that must never be constructed, the timer that must not
// survive unmount. The happy paths live in the sibling contract suite.
//
// Why that half is worth its own file: a sound is unfalsifiable feedback.
// A missing chime is a shrug; a chime for something that did not happen is a
// lie the board tells about carl's own jobs, and there is no screen to check
// it against. Every "fires when" row below is therefore paired with its
// "must NOT fire" twin, asserted as an EXACT event list — a suite that only
// checked for the presence of the right event would pass just as happily on
// an implementation that fires all thirteen every tick.
//
// Every module-level identifier is `edge`-prefixed: a second author is
// writing a sibling suite in the same package.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

import type {
  JobPR,
  WebArtifact,
  WebBoard,
  WebJob,
  WebQuestion,
  WebThread,
  WebUpdate,
} from "../api/types.ts";
import { makeBoard, makeJob } from "../test/fixtures.ts";

import type { BoardEvent, EventType } from "./boardEvents.ts";
import {
  EVENT_PRIORITY,
  EVENT_STAGGER_MS,
  SFX_EVENT_CAP,
  coalesceEvents,
  consumeMuteFlag,
  diffBoard,
  muteNextDiff,
  resetBoardEventsForTest,
} from "./boardEvents.ts";
import {
  REVERB_SECONDS,
  injectAudioContextFactoryForTest,
  playEventSfx,
  playSfx,
  resetSfxForTest,
  setSfxEnabled,
} from "./sfx.ts";
import { useBoardEvents } from "../hooks/useBoardEvents.ts";

// ===========================================================================
// Fixtures
// ===========================================================================

/**
 * All thirteen event types, spelled out rather than derived from
 * EVENT_PRIORITY's keys: a member accidentally dropped from the union has to
 * fail HERE (as a type error and as a missing priority) rather than silently
 * shrinking every table that iterates the ladder.
 */
const edgeALL_TYPES: EventType[] = [
  "question",
  "health",
  "done",
  "pr-merged",
  "pr-open",
  "blocked",
  "resumed",
  "update-done",
  "update-failed",
  "thread-open",
  "thread-closed",
  "artifact",
  "color",
];

/** The job under test: live, working, and carrying no optional key at all. */
function edgeJob(over: Partial<WebJob> = {}): WebJob {
  return makeJob({ slug: "a", state: "working", live: true, ...over });
}

function edgeBoard(jobs: WebJob[], update?: WebUpdate): WebBoard {
  return update ? makeBoard({ jobs, update }) : makeBoard({ jobs });
}

function edgeQ(id: string, text = "why?"): WebQuestion {
  return {
    tool_use_id: id,
    questions: [{ question: text, header: "h", multi_select: false }],
  };
}

function edgePR(url: string, state: string): JobPR {
  return { url, state };
}

function edgeThread(tid: string, state: string, title?: string): WebThread {
  return title === undefined
    ? { tid, state, created_at: 1 }
    : { tid, state, title, created_at: 1 };
}

function edgeArtifact(name: string): WebArtifact {
  return { name, url: `/artifacts/${name}` };
}

/**
 * Events as sorted `type@jobKey` strings.
 *
 * Sorted because the contract leaves the ORDER of one tick's events
 * unspecified ("output order is unspecified"), and asserting an order the
 * contract does not promise would make this suite fail on a correct
 * implementation. Joined with the jobKey because the whole point of half
 * these cases is WHICH row the event was attributed to.
 */
function edgeKeys(events: readonly BoardEvent[]): string[] {
  return events.map((e) => `${e.type}@${e.jobKey}`).sort();
}

/** One job, one field pair: the shape every must-not-fire row below takes. */
function edgeDiff1(prev: Partial<WebJob>, next: Partial<WebJob>): string[] {
  return edgeKeys(
    diffBoard(edgeBoard([edgeJob(prev)]), edgeBoard([edgeJob(next)])),
  );
}

/** A structurally identical board that is NOT the same object reference. */
function edgeClone(b: WebBoard): WebBoard {
  return JSON.parse(JSON.stringify(b)) as WebBoard;
}

// ===========================================================================
// diffBoard — the snapshots that must produce nothing
// ===========================================================================

describe("diffBoard — the first poll has nothing to compare against", () => {
  it("returns [] for an undefined prev, however loud the next board is", () => {
    // Baseline is the whole reason the wiring holds a ref: on page load the
    // board arrives with every job already blocked, already questioned,
    // already coloured. Diffed against nothing that is a dozen events and a
    // fanfare for state that has been sitting there for hours.
    const next = edgeBoard(
      [
        edgeJob({
          slug: "a",
          state: "blocked",
          question: edgeQ("t1"),
          health: "stalled",
          color: "red",
          prs: [edgePR("u1", "open"), edgePR("u2", "merged")],
          threads: [edgeThread("th1", "live")],
          artifacts: [edgeArtifact("plan.md")],
        }),
        edgeJob({ slug: "b", state: "done" }),
      ],
      { phase: "done" },
    );
    expect(edgeKeys(diffBoard(undefined, next))).toEqual([]);
  });

  it("returns [] for an undefined prev even when the next board is empty", () => {
    expect(edgeKeys(diffBoard(undefined, edgeBoard([])))).toEqual([]);
  });
});

describe("diffBoard — a job on only one side is not a transition", () => {
  it("says nothing about a job that is new in next, however finished it looks", () => {
    // A new row is not a change: its whole state is "new". The click that
    // created it already sounded, and a submitted job that lands already done
    // (a no-op cell) must not ring the completion chime.
    const prev = edgeBoard([edgeJob({ slug: "keep" })]);
    const next = edgeBoard([
      edgeJob({ slug: "keep" }),
      edgeJob({
        slug: "brandnew",
        state: "done",
        question: edgeQ("t1"),
        health: "stalled",
        color: "red",
        prs: [edgePR("u1", "merged")],
        threads: [edgeThread("th1", "live")],
        artifacts: [edgeArtifact("plan.md")],
      }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("says nothing about a job that vanished from next", () => {
    // Deletion is not a transition either — and the naive implementation of
    // "did state become done?" that reads next's row through an optional
    // chain would compare `undefined` against "done" and fire.
    const prev = edgeBoard([
      edgeJob({ slug: "keep" }),
      edgeJob({
        slug: "gone",
        state: "blocked",
        question: edgeQ("t1"),
        health: "stalled",
        color: "red",
        prs: [edgePR("u1", "open")],
        threads: [edgeThread("th1", "live")],
        artifacts: [edgeArtifact("plan.md")],
      }),
    ]);
    const next = edgeBoard([edgeJob({ slug: "keep" })]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("says nothing when a rename swaps one slug for another", () => {
    // A rename is a vanish plus an arrival, and the contract's two
    // one-side rules are exactly what makes it silent. Both halves change
    // everything a rename does not: state, colour, artifacts.
    const prev = edgeBoard([
      edgeJob({ slug: "old", state: "working", color: "red" }),
    ]);
    const next = edgeBoard([
      edgeJob({ slug: "new", state: "done", color: "blue" }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("says nothing when every job leaves at once", () => {
    const prev = edgeBoard([edgeJob({ slug: "a" }), edgeJob({ slug: "b" })]);
    expect(edgeKeys(diffBoard(prev, edgeBoard([])))).toEqual([]);
  });
});

describe("diffBoard — archived rows are ignored on both sides", () => {
  it("ignores a row marked archived by state, whatever changed on it", () => {
    const arch = (over: Partial<WebJob>): WebJob =>
      edgeJob({ slug: "a", state: "archived", archived: true, ...over });
    const prev = edgeBoard([arch({ color: "red", artifacts: [] })]);
    const next = edgeBoard([
      arch({
        color: "blue",
        question: edgeQ("t1"),
        health: "stalled",
        artifacts: [edgeArtifact("plan.md")],
        prs: [edgePR("u1", "merged")],
        threads: [edgeThread("th1", "live")],
      }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("ignores a row flagged `archived: true` while its state still reads done", () => {
    // The contract names both markers ('state "archived" (or archived: true)')
    // and the wire really can carry the flag beside a non-archived state, so
    // an implementation testing only `state === "archived"` must not squeak
    // through here.
    const prev = edgeBoard([
      edgeJob({ slug: "a", state: "done", archived: true, color: "red" }),
    ]);
    const next = edgeBoard([
      edgeJob({ slug: "a", state: "done", archived: true, color: "blue" }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("treats a job being archived as a departure, not a transition", () => {
    // working → archived. The row is still in `jobs`, but it is ignored on
    // next's side, so the job is prev-only and nothing fires — in particular
    // not `done`, which an implementation mapping archival onto completion
    // would emit.
    const prev = edgeBoard([edgeJob({ slug: "a", state: "working" })]);
    const next = edgeBoard([
      edgeJob({ slug: "a", state: "archived", archived: true }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("treats an unarchive as an arrival, not a transition", () => {
    const prev = edgeBoard([
      edgeJob({ slug: "a", state: "archived", archived: true }),
    ]);
    const next = edgeBoard([edgeJob({ slug: "a", state: "done" })]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });
});

describe("diffBoard — the jobKey slug collision", () => {
  // lib/jobKey.ts spells out the hazard: archiving `foo` and creating a new
  // active `foo` puts TWO rows with slug "foo" on the board, archived last.
  // Anything keyed by bare slug hands the archived row a last-writer-wins
  // victory — which shipped once already, as an unsteerable card.
  const edgeArchivedFoo = (over: Partial<WebJob> = {}): WebJob =>
    edgeJob({ slug: "foo", state: "archived", archived: true, ...over });
  const edgeLiveFoo = (over: Partial<WebJob> = {}): WebJob =>
    edgeJob({ slug: "foo", state: "working", ...over });

  it("never cross-diffs the archived row against the live one", () => {
    // The live row is unchanged and the ARCHIVED row's colour moves. Keyed by
    // bare slug the archived entry wins both lookups and a colour chime
    // fires for a job nobody touched.
    const prev = edgeBoard([
      edgeLiveFoo({ color: "" }),
      edgeArchivedFoo({ color: "red" }),
    ]);
    const next = edgeBoard([
      edgeLiveFoo({ color: "" }),
      edgeArchivedFoo({ color: "blue" }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });

  it("still diffs the LIVE row, and attributes it to the bare slug", () => {
    // The other direction of the same collision: the archived twin must not
    // shadow the live row into silence, and the event's jobKey is the live
    // key ("foo"), never "archive:foo".
    const prev = edgeBoard([
      edgeLiveFoo({ color: "" }),
      edgeArchivedFoo({ color: "red" }),
    ]);
    const next = edgeBoard([
      edgeLiveFoo({ color: "green" }),
      edgeArchivedFoo({ color: "red" }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual(["color@foo"]);
  });

  it("does not let an archived twin make a brand-new live job look like a transition", () => {
    // The nastiest ordering: `foo` was archived some time ago and carl has
    // just created a NEW `foo`. Under bare-slug matching the new row diffs
    // against the archive's state ("archived" → "done") and its absent
    // question against a present one — two events for a job one second old.
    const prev = edgeBoard([edgeArchivedFoo()]);
    const next = edgeBoard([
      edgeArchivedFoo(),
      edgeLiveFoo({ state: "done", question: edgeQ("t1") }),
    ]);
    expect(edgeKeys(diffBoard(prev, next))).toEqual([]);
  });
});

describe("diffBoard — absent optional fields read as their zero", () => {
  // The wire omits every one of these keys rather than sending an empty
  // value, so "absent" and "empty" are the SAME reading. An implementation
  // that compares `undefined` to `""` (or `undefined` to `[]`) fires on the
  // first poll after a server that started omitting a key — a chime with no
  // cause at all.
  const edgeZeroPairs: Array<[string, Partial<WebJob>, Partial<WebJob>]> = [
    ["health absent → empty string", {}, { health: "" }],
    ["health empty string → absent", { health: "" }, {}],
    ["health absent → absent", {}, {}],
    ["color absent → empty string", {}, { color: "" }],
    ["color empty string → absent", { color: "" }, {}],
    ["prs absent → empty array", {}, { prs: [] }],
    ["prs empty array → absent", { prs: [] }, {}],
    ["threads absent → empty array", {}, { threads: [] }],
    ["threads empty array → absent", { threads: [] }, {}],
    ["artifacts absent → empty array", {}, { artifacts: [] }],
    ["artifacts empty array → absent", { artifacts: [] }, {}],
  ];

  it.each(edgeZeroPairs)("stays silent for %s", (_label, prev, next) => {
    expect(edgeDiff1(prev, next)).toEqual([]);
  });

  const edgeFromZero: Array<[string, Partial<WebJob>, Partial<WebJob>, string]> = [
    ["health absent → sick", {}, { health: "stalled" }, "health@a"],
    ["colour absent → set", {}, { color: "red" }, "color@a"],
    [
      "artifacts absent → one artifact",
      {},
      { artifacts: [edgeArtifact("plan.md")] },
      "artifact@a",
    ],
    [
      "prs absent → one open PR",
      {},
      { prs: [edgePR("u1", "open")] },
      "pr-open@a",
    ],
    [
      "threads absent → one live thread",
      {},
      { threads: [edgeThread("th1", "live")] },
      "thread-open@a",
    ],
  ];

  it.each(edgeFromZero)(
    "reads the absence as the zero and fires once for %s",
    (_label, prev, next, want) => {
      // The other half of the same rule: absent must behave as the zero, not
      // as "unknown, skip this job". Asserted as the EXACT list so a second
      // spurious event fails here too.
      expect(edgeDiff1(prev, next)).toEqual([want]);
    },
  );
});

// ===========================================================================
// diffBoard — the per-type must-NOT-fire table
// ===========================================================================

describe("diffBoard — per-type refusals", () => {
  const edgeMustNot: Array<[string, Partial<WebJob>, Partial<WebJob>]> = [
    // question — presence, not content
    [
      "question: the text merely changes",
      { question: edgeQ("t1", "why?") },
      { question: edgeQ("t1", "who?") },
    ],
    [
      "question: a second question replaces the first (new tool_use_id)",
      { question: edgeQ("t1") },
      { question: edgeQ("t2") },
    ],
    ["question: answered (present → absent)", { question: edgeQ("t1") }, {}],

    // health — "" → non-empty only
    [
      "health: still sick, different text",
      { health: "stalled" },
      { health: "loop detected" },
    ],
    ["health: sick → healthy", { health: "stalled" }, { health: "" }],
    ["health: sick → healthy by dropping the key", { health: "stalled" }, {}],

    // done / resumed — states that are not the named transition
    [
      "done: already done and still done",
      { state: "done" },
      { state: "done" },
    ],
    [
      "done: reactivated (done → working) is not a resume",
      { state: "done" },
      { state: "working" },
    ],
    [
      "resumed: working → working",
      { state: "working" },
      { state: "working" },
    ],
    [
      "resumed: a draft starting work is not a resume",
      { state: "draft" },
      { state: "working" },
    ],

    // blocked — working → blocked only
    ["blocked: draft → blocked", { state: "draft" }, { state: "blocked" }],
    ["blocked: a draft sitting as a draft", { state: "draft" }, { state: "draft" }],
    ["blocked: blocked → blocked", { state: "blocked" }, { state: "blocked" }],
    ["blocked: done → blocked", { state: "done" }, { state: "blocked" }],

    // prs — url identity, then state transition
    [
      "pr: a known url churns open → closed",
      { prs: [edgePR("u1", "open")] },
      { prs: [edgePR("u1", "closed")] },
    ],
    [
      "pr: a known url sits open",
      { prs: [edgePR("u1", "open")] },
      { prs: [edgePR("u1", "open")] },
    ],
    [
      "pr: a known url sits merged",
      { prs: [edgePR("u1", "merged")] },
      { prs: [edgePR("u1", "merged")] },
    ],
    [
      "pr: a known url is reopened (merged → open)",
      { prs: [edgePR("u1", "merged")] },
      { prs: [edgePR("u1", "open")] },
    ],
    [
      "pr: a NEW url arrives already closed",
      { prs: [] },
      { prs: [edgePR("u1", "closed")] },
    ],
    [
      "pr: a url disappears",
      { prs: [edgePR("u1", "open")] },
      { prs: [] },
    ],

    // threads — tid identity, then state transition
    [
      "thread: a NEW tid arrives already closed",
      { threads: [] },
      { threads: [edgeThread("th1", "closed")] },
    ],
    [
      "thread: a live tid disappears entirely",
      { threads: [edgeThread("th1", "live")] },
      { threads: [] },
    ],
    [
      "thread: a closed tid disappears entirely",
      { threads: [edgeThread("th1", "closed")] },
      { threads: [] },
    ],
    [
      "thread: a thread is renamed",
      { threads: [edgeThread("th1", "live", "old")] },
      { threads: [edgeThread("th1", "live", "new")] },
    ],
    [
      "thread: a closed tid sits closed",
      { threads: [edgeThread("th1", "closed")] },
      { threads: [edgeThread("th1", "closed")] },
    ],
    [
      "thread: a closed tid is revived (closed → live)",
      { threads: [edgeThread("th1", "closed")] },
      { threads: [edgeThread("th1", "live")] },
    ],

    // artifacts — length growth only
    [
      "artifact: the list shrinks",
      { artifacts: [edgeArtifact("a.md"), edgeArtifact("b.md")] },
      { artifacts: [edgeArtifact("a.md")] },
    ],
    [
      "artifact: the list is reordered",
      { artifacts: [edgeArtifact("a.md"), edgeArtifact("b.md")] },
      { artifacts: [edgeArtifact("b.md"), edgeArtifact("a.md")] },
    ],
    [
      "artifact: an entry is replaced in place",
      { artifacts: [edgeArtifact("a.md")] },
      { artifacts: [edgeArtifact("z.md")] },
    ],
    [
      "artifact: an empty list stays empty",
      { artifacts: [] },
      { artifacts: [] },
    ],

    // colour — value equality
    ["colour: unchanged", { color: "red" }, { color: "red" }],
  ];

  it.each(edgeMustNot)("stays silent — %s", (_label, prev, next) => {
    expect(edgeDiff1(prev, next)).toEqual([]);
  });
});

describe("diffBoard — the transitions at the edge of each rule DO fire, and fire once", () => {
  const edgeFires: Array<[string, Partial<WebJob>, Partial<WebJob>, string[]]> = [
    [
      "a question appearing",
      {},
      { question: edgeQ("t1") },
      ["question@a"],
    ],
    ["health going sick", { health: "" }, { health: "stalled" }, ["health@a"]],
    ["a draft jumping straight to done", { state: "draft" }, { state: "done" }, ["done@a"]],
    ["working → blocked", { state: "working" }, { state: "blocked" }, ["blocked@a"]],
    ["blocked → working", { state: "blocked" }, { state: "working" }, ["resumed@a"]],
    [
      "a NEW url arriving already merged is a merge, not an open",
      { prs: [] },
      { prs: [edgePR("u1", "merged")] },
      ["pr-merged@a"],
    ],
    [
      "a known url open → merged is a merge, not an open",
      { prs: [edgePR("u1", "open")] },
      { prs: [edgePR("u1", "merged")] },
      ["pr-merged@a"],
    ],
    [
      "a NEW open url is an open, not a merge",
      { prs: [edgePR("u1", "merged")] },
      { prs: [edgePR("u1", "merged"), edgePR("u2", "open")] },
      ["pr-open@a"],
    ],
    [
      "a live tid closing",
      { threads: [edgeThread("th1", "live")] },
      { threads: [edgeThread("th1", "closed")] },
      ["thread-closed@a"],
    ],
    [
      "the artifact list growing",
      { artifacts: [edgeArtifact("a.md")] },
      { artifacts: [edgeArtifact("a.md"), edgeArtifact("b.md")] },
      ["artifact@a"],
    ],
    ["colour set → cleared", { color: "red" }, { color: "" }, ["color@a"]],
    ["colour set → key dropped", { color: "red" }, {}, ["color@a"]],
    ["colour recoloured", { color: "red" }, { color: "blue" }, ["color@a"]],
  ];

  it.each(edgeFires)("fires exactly once for %s", (_label, prev, next, want) => {
    expect(edgeDiff1(prev, next)).toEqual(want);
  });

  it("emits both events when one job blocks on a question in the same tick", () => {
    // The canonical multi-event row. Compared as a SET: the contract says
    // "output order is unspecified", so an order assertion here would be a
    // requirement nobody agreed to.
    expect(
      edgeDiff1(
        { state: "working" },
        { state: "blocked", question: edgeQ("t1") },
      ),
    ).toEqual(["blocked@a", "question@a"]);
  });
});

describe("diffBoard — the board-level update phase", () => {
  function edgeUpdateDiff(prev?: WebUpdate, next?: WebUpdate): string[] {
    return edgeKeys(diffBoard(edgeBoard([], prev), edgeBoard([], next)));
  }

  it("stays silent while the phase SITS at done across consecutive polls", () => {
    // The board holds the terminal phase until the next update runs, so this
    // is not a rare case — it is every single poll after an update finishes.
    // An implementation testing `next.phase === "done"` chimes forever.
    expect(edgeUpdateDiff({ phase: "done" }, { phase: "done" })).toEqual([]);
  });

  it("stays silent while the phase SITS at failed across consecutive polls", () => {
    expect(edgeUpdateDiff({ phase: "failed" }, { phase: "failed" })).toEqual([]);
  });

  it("stays silent when the phase leaves done", () => {
    expect(edgeUpdateDiff({ phase: "done" }, { phase: "preflight" })).toEqual([]);
    expect(edgeUpdateDiff({ phase: "failed" }, { phase: "fetching" })).toEqual([]);
  });

  it("stays silent when the whole update key goes away", () => {
    // Absent `update.phase` reads as "idle"; done → idle is a departure from
    // the terminal phase, not an arrival at it.
    expect(edgeUpdateDiff({ phase: "done" }, undefined)).toEqual([]);
    expect(edgeUpdateDiff({ phase: "done" }, { behind: 0 })).toEqual([]);
  });

  it("stays silent when neither side has an update at all", () => {
    expect(edgeUpdateDiff(undefined, undefined)).toEqual([]);
    expect(edgeUpdateDiff({ behind: 1 }, { behind: 0 })).toEqual([]);
  });

  it("fires on the arrival, and hangs the event off the empty jobKey", () => {
    // jobKey "" is the contract's board-level marker — the one BoardEvent
    // whose key is deliberately not a job.
    expect(edgeUpdateDiff({ phase: "building" }, { phase: "done" })).toEqual([
      "update-done@",
    ]);
    expect(edgeUpdateDiff({ phase: "building" }, { phase: "failed" })).toEqual([
      "update-failed@",
    ]);
  });

  it("treats an absent prev phase as idle, so the first done still arrives", () => {
    expect(edgeUpdateDiff(undefined, { phase: "done" })).toEqual(["update-done@"]);
    expect(edgeUpdateDiff({ behind: 1 }, { phase: "failed" })).toEqual([
      "update-failed@",
    ]);
  });

  it("fires once when one terminal phase flips straight to the other", () => {
    expect(edgeUpdateDiff({ phase: "done" }, { phase: "failed" })).toEqual([
      "update-failed@",
    ]);
    expect(edgeUpdateDiff({ phase: "failed" }, { phase: "done" })).toEqual([
      "update-done@",
    ]);
  });
});

describe("diffBoard — the BoardEvent shape", () => {
  it("carries exactly `type` and `jobKey`, and nothing else", () => {
    // Q7 removed the detail-change event and with it any reason for a third
    // key; 50-components' earlier `{ type, jobKey, detail }` sketch is what
    // the frozen contract narrowed. Asserted in ONE place so a stray field
    // costs one failure rather than fifty.
    expect(
      diffBoard(
        edgeBoard([edgeJob({ slug: "a", color: "red" })]),
        edgeBoard([edgeJob({ slug: "a", color: "blue" })]),
      ),
    ).toEqual([{ type: "color", jobKey: "a" }]);
  });
});

// ===========================================================================
// coalesceEvents — the gate's own boundaries
// ===========================================================================

const edgeEv = (type: EventType, jobKey = "a"): BoardEvent => ({ type, jobKey });

describe("coalesceEvents — nothing in, nothing out", () => {
  it("returns [] for an empty tick", () => {
    expect(coalesceEvents([])).toEqual([]);
  });

  it("returns a new empty list rather than the input array itself", () => {
    // Returning the caller's array would let the wiring's play loop mutate
    // the differ's output; cheap to rule out while we are here.
    const input: BoardEvent[] = [];
    expect(coalesceEvents(input)).not.toBe(input);
  });
});

describe("coalesceEvents — the cap is a hard ceiling", () => {
  it("returns SFX_EVENT_CAP entries when every type fires at once", () => {
    const all = edgeALL_TYPES.map((t) => edgeEv(t));
    const play = coalesceEvents(all);
    expect(play.length).toBe(SFX_EVENT_CAP);
    // The survivors are the two lowest priority VALUES on the ladder,
    // derived from EVENT_PRIORITY rather than retyped, so a ladder edit
    // moves this test with it.
    const ranked = [...edgeALL_TYPES].sort(
      (x, y) => EVENT_PRIORITY[x] - EVENT_PRIORITY[y],
    );
    expect(play).toEqual(ranked.slice(0, SFX_EVENT_CAP));
  });

  it("cuts by priority, not by arrival order", () => {
    // Fed worst-first: an implementation that simply slices the input would
    // play colour and artifact and drop the question.
    expect(
      coalesceEvents([
        edgeEv("color"),
        edgeEv("artifact"),
        edgeEv("thread-closed"),
        edgeEv("question"),
      ]),
    ).toEqual(["question", "thread-closed"]);
  });

  it("discards the remainder rather than queueing it into the next tick", () => {
    // The cap is a drop, not a buffer: a burst must not keep chiming
    // through the following quiet polls.
    coalesceEvents(edgeALL_TYPES.map((t) => edgeEv(t)));
    expect(coalesceEvents([])).toEqual([]);
    expect(coalesceEvents([edgeEv("color")])).toEqual(["color"]);
  });
});

describe("coalesceEvents — one sound per type", () => {
  it("collapses five questions on five jobs into one question", () => {
    expect(
      coalesceEvents([
        edgeEv("question", "a"),
        edgeEv("question", "b"),
        edgeEv("question", "c"),
        edgeEv("question", "d"),
        edgeEv("question", "e"),
      ]),
    ).toEqual(["question"]);
  });

  it("does not spend both cap slots on one duplicated type", () => {
    // The failure this rules out is subtle: dedup running AFTER the cap
    // leaves a tick of two questions playing one sound where a question and
    // a done were both available.
    expect(
      coalesceEvents([
        edgeEv("question", "a"),
        edgeEv("question", "b"),
        edgeEv("done", "c"),
      ]),
    ).toEqual(["question", "done"]);
  });
});

describe("coalesceEvents — the question ⊃ blocked collapse", () => {
  it("drops blocked when the SAME job also raised a question", () => {
    expect(
      coalesceEvents([edgeEv("question", "a"), edgeEv("blocked", "a")]),
    ).toEqual(["question"]);
  });

  it("keeps blocked when the question belongs to a DIFFERENT job", () => {
    // The collapse is per-job. Job b really did block for its own reason and
    // job a's question says nothing about it.
    expect(
      coalesceEvents([edgeEv("question", "a"), edgeEv("blocked", "b")]),
    ).toEqual(["question", "blocked"]);
  });

  it("keeps blocked when nothing asked anything", () => {
    expect(coalesceEvents([edgeEv("blocked", "a")])).toEqual(["blocked"]);
  });

  it("frees the cap slot it took — the collapse runs before the cut", () => {
    // question+blocked on job a, plus a done on job b. If blocked is dropped
    // only after the cap has already spent a slot on it, `done` never plays.
    expect(
      coalesceEvents([
        edgeEv("question", "a"),
        edgeEv("blocked", "a"),
        edgeEv("done", "b"),
      ]),
    ).toEqual(["question", "done"]);
  });

  it("does not let one job's question swallow every blocked on the board", () => {
    expect(
      coalesceEvents([
        edgeEv("question", "a"),
        edgeEv("blocked", "a"),
        edgeEv("blocked", "b"),
      ]),
    ).toEqual(["question", "blocked"]);
  });
});

describe("coalesceEvents — the priority-8 tie", () => {
  it("returns both update events, order unasserted", () => {
    // update-done and update-failed share priority 8 and the contract says
    // their relative order is unspecified — so this checks the SET and the
    // length, and deliberately does not check which came first.
    const play = coalesceEvents([
      edgeEv("update-failed", ""),
      edgeEv("update-done", ""),
    ]);
    expect(play.length).toBe(2);
    expect([...play].sort()).toEqual(["update-done", "update-failed"]);
  });

  it("lets the tied pair take both cap slots ahead of anything below them", () => {
    const play = coalesceEvents([
      edgeEv("artifact", "a"),
      edgeEv("update-done", ""),
      edgeEv("update-failed", ""),
    ]);
    expect(play.length).toBe(SFX_EVENT_CAP);
    expect([...play].sort()).toEqual(["update-done", "update-failed"]);
  });
});

describe("coalesceEvents — purity", () => {
  it("does not mutate the array it was handed", () => {
    // `readonly BoardEvent[]` says so in the type; a frozen array says so at
    // runtime, which is what catches an in-place `.sort()`.
    const input = Object.freeze([
      edgeEv("color", "a"),
      edgeEv("question", "a"),
      edgeEv("done", "b"),
    ]) as readonly BoardEvent[];
    expect(() => coalesceEvents(input)).not.toThrow();
    expect(input.map((e) => e.type)).toEqual(["color", "question", "done"]);
  });

  it("neither reads nor consumes the mute flag", () => {
    // The flag is the wiring's business. A coalescer that swallowed it would
    // leave the hook unmuted and the mutate-tick audible.
    resetBoardEventsForTest();
    muteNextDiff();
    expect(coalesceEvents([edgeEv("question", "a")])).toEqual(["question"]);
    expect(consumeMuteFlag()).toBe(true);
  });

  it("is deterministic across repeated calls with the same input", () => {
    const input = [edgeEv("color", "a"), edgeEv("health", "b")];
    expect(coalesceEvents(input)).toEqual(coalesceEvents(input));
  });
});

// ===========================================================================
// The mute flag — one-shot semantics
// ===========================================================================

describe("the mute flag", () => {
  beforeEach(() => resetBoardEventsForTest());

  it("starts false", () => {
    expect(consumeMuteFlag()).toBe(false);
  });

  it("is consumed by the first read and false on the second", () => {
    muteNextDiff();
    expect(consumeMuteFlag()).toBe(true);
    expect(consumeMuteFlag()).toBe(false);
  });

  it("is a flag, not a counter — two mutates still silence exactly one tick", () => {
    // Two mutates racing (a colour set and a reorder landing together) must
    // not bank a second silent tick that swallows a real change.
    muteNextDiff();
    muteNextDiff();
    expect(consumeMuteFlag()).toBe(true);
    expect(consumeMuteFlag()).toBe(false);
  });

  it("is cleared by the test reset even when set", () => {
    muteNextDiff();
    resetBoardEventsForTest();
    expect(consumeMuteFlag()).toBe(false);
  });

  it("stays false through repeated reads with no mutate", () => {
    expect(consumeMuteFlag()).toBe(false);
    expect(consumeMuteFlag()).toBe(false);
  });
});

// ===========================================================================
// The audio fake
// ===========================================================================
//
// Built from the structural interfaces in the contract (SfxAudioContext gains
// sampleRate / createBiquadFilter / createConvolver / createBuffer) widened
// with the standard Web Audio members a beep synth uses. It is cast into the
// injection seam through `unknown` on purpose: the contract enumerates only
// what the night bus ADDS to SfxAudioContext, so pinning this fake to the
// exact interface would make the file fail to compile for a reason that has
// nothing to do with the behaviour under test.
//
// `fail` is the error-path lever: a name in the set makes that factory throw,
// which is how both "a throwing night-bus construction" and "a throwing node
// factory" are driven.

interface EdgeParam {
  value: number;
  at: Array<[number, number]>;
  setValueAtTime(v: number, t: number): EdgeParam;
  linearRampToValueAtTime(v: number, t: number): EdgeParam;
  exponentialRampToValueAtTime(v: number, t: number): EdgeParam;
  setTargetAtTime(v: number, t: number, c?: number): EdgeParam;
  cancelScheduledValues(t: number): EdgeParam;
}

function edgeParam(initial = 0): EdgeParam {
  const at: Array<[number, number]> = [];
  const p: EdgeParam = {
    value: initial,
    at,
    setValueAtTime(v, t) {
      at.push([v, t]);
      p.value = v;
      return p;
    },
    linearRampToValueAtTime(v, t) {
      at.push([v, t]);
      p.value = v;
      return p;
    },
    exponentialRampToValueAtTime(v, t) {
      at.push([v, t]);
      p.value = v;
      return p;
    },
    setTargetAtTime(v, t) {
      at.push([v, t]);
      return p;
    },
    cancelScheduledValues() {
      return p;
    },
  };
  return p;
}

interface EdgeOsc {
  type: string;
  frequency: EdgeParam;
  detune: EdgeParam;
  starts: number[];
  connect(node: unknown): unknown;
  disconnect(): void;
  start(t?: number): void;
  stop(t?: number): void;
  onended: unknown;
}

interface EdgeGain {
  gain: EdgeParam;
  connect(node: unknown): unknown;
  disconnect(): void;
}

interface EdgeFilter {
  type: string;
  frequency: EdgeParam;
  Q: EdgeParam;
  gain: EdgeParam;
  connect(node: unknown): unknown;
  disconnect(): void;
}

interface EdgeConvolver {
  buffer: unknown;
  normalize: boolean;
  connect(node: unknown): unknown;
  disconnect(): void;
}

interface EdgeBuffer {
  numberOfChannels: number;
  length: number;
  sampleRate: number;
  channels: Float32Array[];
  getChannelData(ch: number): Float32Array;
}

interface EdgeCtx {
  state: string | undefined;
  sampleRate: number;
  currentTime: number;
  destination: unknown;
  fail: Set<string>;
  oscillators: EdgeOsc[];
  gains: EdgeGain[];
  filters: EdgeFilter[];
  convolvers: EdgeConvolver[];
  buffers: EdgeBuffer[];
  bufferArgs: Array<[number, number, number]>;
  createOscillator(): EdgeOsc;
  createGain(): EdgeGain;
  createBiquadFilter(): EdgeFilter;
  createConvolver(): EdgeConvolver;
  createBuffer(channels: number, frames: number, rate: number): EdgeBuffer;
  resume(): Promise<void>;
  suspend(): Promise<void>;
  close(): Promise<void>;
}

interface EdgeCtxOpts {
  /** omit to leave `state` undefined — which the contract says counts as running */
  state?: string;
  sampleRate?: number;
  /** factory names that throw on call, seeded before the night bus is built */
  fail?: string[];
}

function edgeMakeCtx(opts: EdgeCtxOpts = {}): EdgeCtx {
  const fail = new Set(opts.fail ?? []);
  const guard = (name: string) => {
    if (fail.has(name)) throw new Error(`edge fake: ${name} refuses`);
  };
  const ctx: EdgeCtx = {
    state: opts.state,
    sampleRate: opts.sampleRate ?? 48000,
    currentTime: 0,
    destination: { edgeNode: "destination" },
    fail,
    oscillators: [],
    gains: [],
    filters: [],
    convolvers: [],
    buffers: [],
    bufferArgs: [],
    createOscillator() {
      guard("createOscillator");
      const osc: EdgeOsc = {
        type: "sine",
        frequency: edgeParam(440),
        detune: edgeParam(0),
        starts: [],
        connect: (n) => n,
        disconnect: () => {},
        start(t = 0) {
          osc.starts.push(t);
        },
        stop: () => {},
        onended: null,
      };
      ctx.oscillators.push(osc);
      return osc;
    },
    createGain() {
      guard("createGain");
      const g: EdgeGain = {
        gain: edgeParam(1),
        connect: (n) => n,
        disconnect: () => {},
      };
      ctx.gains.push(g);
      return g;
    },
    createBiquadFilter() {
      guard("createBiquadFilter");
      const f: EdgeFilter = {
        type: "lowpass",
        frequency: edgeParam(350),
        Q: edgeParam(1),
        gain: edgeParam(0),
        connect: (n) => n,
        disconnect: () => {},
      };
      ctx.filters.push(f);
      return f;
    },
    createConvolver() {
      guard("createConvolver");
      const c: EdgeConvolver = {
        buffer: null,
        normalize: true,
        connect: (n) => n,
        disconnect: () => {},
      };
      ctx.convolvers.push(c);
      return c;
    },
    createBuffer(channels, frames, rate) {
      guard("createBuffer");
      ctx.bufferArgs.push([channels, frames, rate]);
      const data: Float32Array[] = [];
      for (let i = 0; i < channels; i++) data.push(new Float32Array(frames));
      const b: EdgeBuffer = {
        numberOfChannels: channels,
        length: frames,
        sampleRate: rate,
        channels: data,
        getChannelData: (ch) => {
          // Under noUncheckedIndexedAccess this lookup is `| undefined`, and
          // a real AudioBuffer throws IndexSizeError for a channel it does
          // not have — so throwing is both the narrowing and the honest fake.
          const d = data[ch];
          if (d === undefined) {
            throw new Error(`edge fake: no channel ${ch} on this buffer`);
          }
          return d;
        },
      };
      ctx.buffers.push(b);
      return b;
    },
    resume: () => Promise.resolve(),
    suspend: () => Promise.resolve(),
    close: () => Promise.resolve(),
  };
  return ctx;
}

type EdgeInjected = Parameters<typeof injectAudioContextFactoryForTest>[0];

let edgeFactoryCalls = 0;
let edgeCtxs: EdgeCtx[] = [];

function edgeInstall(opts: EdgeCtxOpts = {}): void {
  injectAudioContextFactoryForTest(((): EdgeCtx => {
    edgeFactoryCalls++;
    const ctx = edgeMakeCtx(opts);
    edgeCtxs.push(ctx);
    return ctx;
  }) as unknown as EdgeInjected);
}

/** The most recently constructed fake context. */
function edgeCtx(): EdgeCtx {
  const c = edgeCtxs[edgeCtxs.length - 1];
  if (!c) throw new Error("edge fake: no context has been constructed");
  return c;
}

/** The nth fake context, in construction order. */
function edgeCtxAt(i: number): EdgeCtx {
  const c = edgeCtxs[i];
  if (!c) throw new Error(`edge fake: context #${i} was never constructed`);
  return c;
}

/**
 * Every oscillator ever created, across every context. The single number this
 * whole half of the suite watches: a sound played iff this grew.
 */
function edgeOscCount(): number {
  return edgeCtxs.reduce((n, c) => n + c.oscillators.length, 0);
}

/** The gesture that unlocks audio: one ordinary interaction sound. */
function edgeUnlock(): void {
  playSfx("click");
}

// ===========================================================================
// playEventSfx — the refusals
// ===========================================================================

describe("playEventSfx — refusals", () => {
  beforeEach(() => {
    edgeFactoryCalls = 0;
    edgeCtxs = [];
    resetSfxForTest();
    resetBoardEventsForTest();
  });

  it("never constructs the AudioContext — no unlock, no factory call, no sound", () => {
    // The one behavioural difference from playSfx, and the reason it exists:
    // a poll tick is not a user gesture, so constructing a context here would
    // trip the browser's autoplay policy and leave the board with a suspended
    // context whose currentTime never advances.
    edgeInstall();
    setSfxEnabled(true);
    for (const t of edgeALL_TYPES) playEventSfx(t);
    expect(edgeFactoryCalls).toBe(0);
    expect(edgeCtxs).toEqual([]);
    expect(edgeOscCount()).toBe(0);

    // Control: the factory really was wired, so the zero above is a refusal
    // and not a mis-installed seam quietly passing the test.
    edgeUnlock();
    expect(edgeFactoryCalls).toBe(1);
    expect(edgeOscCount()).toBeGreaterThan(0);
  });

  it("drops rather than queues — the missed sounds do not replay on unlock", () => {
    // Control run: what one plain unlock costs, with nothing dropped first.
    edgeInstall();
    setSfxEnabled(true);
    edgeUnlock();
    const plain = edgeOscCount();
    expect(plain).toBeGreaterThan(0);

    // The same unlock, preceded by thirteen refused event sounds. If any of
    // them had been parked for later, the unlock would flush them here.
    resetSfxForTest();
    edgeCtxs = [];
    edgeInstall();
    setSfxEnabled(true);
    for (const t of edgeALL_TYPES) playEventSfx(t);
    edgeUnlock();
    expect(edgeOscCount()).toBe(plain);
  });

  it("returns silently when sfx is disabled, even with a live context", () => {
    edgeInstall();
    setSfxEnabled(true);
    edgeUnlock();
    setSfxEnabled(false);
    const before = edgeOscCount();
    for (const t of edgeALL_TYPES) playEventSfx(t);
    expect(edgeOscCount()).toBe(before);

    // Control: the same call plays once the flag is back on, so the silence
    // above is the flag and not a broken fake.
    setSfxEnabled(true);
    playEventSfx("done");
    expect(edgeOscCount()).toBeGreaterThan(before);
  });

  it("drops when the context exists but is suspended", () => {
    // The frozen-currentTime hazard: a suspended context accepts every
    // schedule call and plays the lot in one burst on resume.
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    edgeCtx().state = "suspended";
    const before = edgeOscCount();
    for (const t of edgeALL_TYPES) playEventSfx(t);
    expect(edgeOscCount()).toBe(before);

    edgeCtx().state = "running";
    playEventSfx("done");
    expect(edgeOscCount()).toBeGreaterThan(before);
  });

  it("drops when the context has been closed", () => {
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    edgeCtx().state = "closed";
    const before = edgeOscCount();
    playEventSfx("question");
    expect(edgeOscCount()).toBe(before);
  });

  it("treats an undefined state as running", () => {
    // Explicit in the contract ("an undefined state counts as running,
    // matching playSfx") — the boundary between the drop rule and a fake or
    // an older AudioContext that simply has no `state`.
    edgeInstall({});
    setSfxEnabled(true);
    edgeUnlock();
    expect(edgeCtx().state).toBeUndefined();
    const before = edgeOscCount();
    playEventSfx("done");
    expect(edgeOscCount()).toBeGreaterThan(before);
  });

  it("degrades to silence when the node factory throws", () => {
    // The module's standing contract: a sound is never worth an exception
    // escaping into a poll handler, where it would take the board's render
    // down with it.
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    edgeCtx().fail.add("createOscillator");
    edgeCtx().fail.add("createGain");
    for (const t of edgeALL_TYPES) {
      expect(() => playEventSfx(t)).not.toThrow();
    }
  });

  it("does not rate-limit itself — the coalescer is the only gate", () => {
    // playSfx carries watermarks; playEventSfx must not, or the second entry
    // of a staggered pair would be swallowed 150ms after the first.
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    const before = edgeOscCount();
    playEventSfx("artifact");
    const one = edgeOscCount() - before;
    playEventSfx("artifact");
    playEventSfx("artifact");
    expect(edgeOscCount() - before).toBe(one * 3);
  });
});

// ===========================================================================
// The night bus — degradation, and one build per context
// ===========================================================================

describe("the night bus — a throwing construction leaves the dry path alone", () => {
  beforeEach(() => {
    edgeFactoryCalls = 0;
    edgeCtxs = [];
    resetSfxForTest();
  });

  const edgeNightFactories = [
    "createBiquadFilter",
    "createConvolver",
    "createBuffer",
  ];

  it.each(edgeNightFactories)(
    "still plays interaction sounds when %s throws",
    (which) => {
      // The reverb is a garnish. A browser that refuses one of these three
      // (or an AudioContext polyfill that never had them) must cost the wet
      // path and nothing else — the board must not go silent over a garnish.
      edgeInstall({ state: "running", fail: [which] });
      setSfxEnabled(true);
      expect(() => edgeUnlock()).not.toThrow();
      expect(edgeCtxs.length).toBe(1);
      expect(edgeOscCount()).toBeGreaterThan(0);
    },
  );

  it.each(edgeNightFactories)(
    "still accepts event sounds when %s throws",
    (which) => {
      edgeInstall({ state: "running", fail: [which] });
      setSfxEnabled(true);
      edgeUnlock();
      const before = edgeOscCount();
      expect(() => playEventSfx("done")).not.toThrow();
      expect(edgeOscCount()).toBeGreaterThan(before);
    },
  );

  it("builds the impulse once per context, not once per sound", () => {
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    edgeUnlock();
    playEventSfx("done");
    playEventSfx("color");
    expect(edgeCtx().convolvers.length).toBe(1);
    expect(edgeCtx().buffers.length).toBe(1);
  });

  it("builds a fresh one for a fresh context — the bus dies with the singleton", () => {
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    resetSfxForTest();
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    expect(edgeCtxs.length).toBe(2);
    expect(edgeCtxAt(0).convolvers.length).toBe(1);
    expect(edgeCtxAt(1).convolvers.length).toBe(1);
  });

  it("sizes the impulse by ceil, at the context's own sample rate", () => {
    // 44101 is chosen so 44101 × 2.8 lands off a whole frame: floor and ceil
    // differ, which is the only way an implementation's rounding shows.
    edgeInstall({ state: "running", sampleRate: 44101 });
    setSfxEnabled(true);
    edgeUnlock();
    expect(edgeCtx().bufferArgs).toEqual([
      [2, Math.ceil(44101 * REVERB_SECONDS), 44101],
    ]);
  });

  it("fills both channels with noise that decays to silence", () => {
    edgeInstall({ state: "running", sampleRate: 8000 });
    setSfxEnabled(true);
    edgeUnlock();
    const buf = edgeCtx().buffers[0];
    if (!buf) throw new Error("the night bus built no impulse buffer at all");
    expect(buf.channels.length).toBe(2);
    for (const ch of buf.channels) {
      // One pass, not one expect() per frame: this buffer is 22400 frames.
      let peak = 0;
      for (const v of ch) peak = Math.max(peak, Math.abs(v));
      expect(peak).toBeGreaterThan(0);
      expect(peak).toBeLessThanOrEqual(1);
      // (1 − i/n)^3.2 at the last frame is ~1e-16: the tail is silence, and a
      // missing envelope would leave full-scale noise sitting there.
      const last = ch[ch.length - 1];
      if (last === undefined) throw new Error("an impulse channel is empty");
      expect(Math.abs(last)).toBeLessThan(1e-6);
    }
  });
});

// ===========================================================================
// useBoardEvents — baseline, mute, unmount
// ===========================================================================

describe("useBoardEvents — the ticks that must stay silent", () => {
  beforeEach(() => {
    edgeFactoryCalls = 0;
    edgeCtxs = [];
    resetSfxForTest();
    resetBoardEventsForTest();
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** Let every stagger timer the tick could have scheduled fire. */
  function edgeSettle(): void {
    act(() => {
      vi.advanceTimersByTime(EVENT_STAGGER_MS * 20);
    });
  }

  /** A board with one job, and one with the same job blocked on a question. */
  const edgeQuiet = (): WebBoard =>
    edgeBoard([edgeJob({ slug: "a", state: "working" })]);
  const edgeLoud = (): WebBoard =>
    edgeBoard([
      edgeJob({ slug: "a", state: "blocked", question: edgeQ("t1") }),
      edgeJob({ slug: "b", state: "working", prs: [edgePR("u1", "open")] }),
    ]);
  const edgeLouder = (): WebBoard =>
    edgeBoard([
      edgeJob({ slug: "a", state: "blocked", question: edgeQ("t1") }),
      edgeJob({
        slug: "b",
        state: "working",
        prs: [edgePR("u1", "open")],
        color: "red",
      }),
    ]);

  it("is silent for the first board it is ever handed", () => {
    // Baseline. On page load every job on the board is already in whatever
    // state it is in; there is no prev to make any of it a transition.
    const before = edgeOscCount();
    renderHook<void, WebBoard | undefined>((b) => useBoardEvents(b), {
      initialProps: edgeLoud(),
    });
    edgeSettle();
    expect(edgeOscCount()).toBe(before);
  });

  it("is silent for the first REAL board when it mounted with undefined", () => {
    // useBoard hands `undefined` until the first fetch lands, so the
    // first-ever board almost always arrives on a rerender rather than at
    // mount — and it is still the baseline.
    const before = edgeOscCount();
    const view = renderHook<void, WebBoard | undefined>(
      (b) => useBoardEvents(b),
      { initialProps: undefined },
    );
    view.rerender(edgeLoud());
    edgeSettle();
    expect(edgeOscCount()).toBe(before);

    // Control: the tick AFTER the baseline does play, so the silence above is
    // the baseline rule and not a hook that never plays anything.
    view.rerender(edgeLouder());
    edgeSettle();
    expect(edgeOscCount()).toBeGreaterThan(before);
  });

  it("is silent for a new board reference carrying identical content", () => {
    const before = edgeOscCount();
    const first = edgeLoud();
    const view = renderHook<void, WebBoard | undefined>(
      (b) => useBoardEvents(b),
      { initialProps: first },
    );
    view.rerender(edgeClone(first));
    edgeSettle();
    expect(edgeOscCount()).toBe(before);
  });

  it("is silent on a muted tick, and stays silent on the poll after it", () => {
    // The mutate path: carl's click already sounded, so the refetch it
    // triggers must not sound again — and, critically, the prev ref must
    // still advance, or the very next ordinary poll re-announces everything
    // the muted tick showed.
    const before = edgeOscCount();
    const view = renderHook<void, WebBoard | undefined>(
      (b) => useBoardEvents(b),
      { initialProps: edgeQuiet() },
    );
    edgeSettle();

    muteNextDiff();
    const loud = edgeLoud();
    view.rerender(loud);
    edgeSettle();
    expect(edgeOscCount()).toBe(before);

    // The hook consumed the one-shot rather than leaving it set.
    expect(consumeMuteFlag()).toBe(false);

    // Same content, new reference: the baseline advanced, so there is
    // nothing left to announce.
    view.rerender(edgeClone(loud));
    edgeSettle();
    expect(edgeOscCount()).toBe(before);

    // And the mute was one-shot, not sticky: a genuinely new change plays.
    view.rerender(edgeLouder());
    edgeSettle();
    expect(edgeOscCount()).toBeGreaterThan(before);
  });
});

describe("useBoardEvents — unmount", () => {
  beforeEach(() => {
    edgeFactoryCalls = 0;
    edgeCtxs = [];
    resetSfxForTest();
    resetBoardEventsForTest();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /**
   * Run one two-entry tick, optionally unmounting before the stagger timers
   * are allowed to fire, and return how many oscillators the tick produced.
   *
   * Compared as two runs rather than asserted absolutely because the contract
   * says entry 0 is "immediate" without saying whether that means
   * synchronously or on a zero-delay timer — either reading makes the
   * UNMOUNTED run strictly quieter, which is the obligation.
   */
  function edgeStaggerRun(unmountFirst: boolean): number {
    resetSfxForTest();
    resetBoardEventsForTest();
    edgeCtxs = [];
    edgeInstall({ state: "running" });
    setSfxEnabled(true);
    edgeUnlock();
    const before = edgeOscCount();

    // BOTH jobs sit in the prev snapshot: a job that is new in `next`
    // produces no events at all, so a fixture that introduces job b on the
    // second snapshot would yield ONE coalesced entry and no stagger to
    // clear — the test would pass without testing anything.
    const view = renderHook<void, WebBoard | undefined>(
      (b) => useBoardEvents(b),
      {
        initialProps: edgeBoard([
          edgeJob({ slug: "a", state: "working" }),
          edgeJob({ slug: "b", state: "working", prs: [] }),
        ]),
      },
    );
    view.rerender(
      edgeBoard([
        edgeJob({ slug: "a", state: "working", question: edgeQ("t1") }),
        edgeJob({ slug: "b", state: "working", prs: [edgePR("u1", "open")] }),
      ]),
    );
    if (unmountFirst) view.unmount();
    act(() => {
      vi.advanceTimersByTime(EVENT_STAGGER_MS * 20);
    });
    if (!unmountFirst) view.unmount();
    return edgeOscCount() - before;
  }

  it("clears the pending stagger timers, so nothing plays after unmount", () => {
    // Two coalesced entries: question at 0ms, pr-open at 150ms. Navigating
    // away between them must not leave a chime firing into a dead board.
    const whole = edgeStaggerRun(false);
    const cut = edgeStaggerRun(true);
    expect(whole).toBeGreaterThan(0);
    expect(cut).toBeLessThan(whole);
  });
});
