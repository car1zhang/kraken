import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebBoard, WebJob } from "../api/types.ts";
import { useBoard } from "../hooks/useBoard.ts";
import { useBoardEvents } from "../hooks/useBoardEvents.ts";
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
import * as contractSfxModule from "./sfx.ts";
import {
  EVENT_SOUNDS,
  PITCH_FACTOR,
  REVERB_DECAY,
  REVERB_LOWPASS_HZ,
  REVERB_SECONDS,
  REVERB_WET_GAIN,
  injectAudioContextFactoryForTest,
  playEventSfx,
  playSfx,
  resetSfxForTest,
  setSfxEnabled,
} from "./sfx.ts";

// sfxevents.contract.test.ts — the POSITIVE half of the board-event sound
// contract (artifacts/50-contract.md), written blind: none of boardEvents.ts,
// sfx.ts, sfx.test.ts, useBoardEvents.ts, useBoard.ts or App.tsx was read while
// authoring this file. Every assertion below is derived from a contract line,
// never from an implementation, so a passing run means the implementation met
// the requirement rather than that the test met the implementation.
//
// Scope split: this file owns the stated INVARIANTS and HAPPY PATHS — the
// diffBoard fires-table's positive rows, the coalescer's collapse/rank/dedup/cap
// on ordinary input, PITCH_FACTOR observable on both sound families, the night
// bus's graph shape, playEventSfx's play-through-an-existing-context path, the
// wiring's baseline/diff/stagger/mute behaviour, and mutate setting the flag.
// Boundaries, refusals and error paths (never-constructs, non-running context,
// throwing factories, must-NOT-fire rows, unmount teardown) live in the sibling
// sfxevents.edge.test.ts and are deliberately absent here.
//
// Every module-level identifier is `contract`-prefixed: the sibling edge suite
// shares this package and vitest namespace.

// ---------------------------------------------------------------- the tables

/**
 * The thirteen event names, exactly as `EventType` enumerates them
 * (50-contract.md § Slice A). This list IS the requirement — it is hardcoded
 * rather than derived from the module so that a member added, dropped or
 * renamed at the source fails here loudly.
 */
const contractEventNames = [
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
] as const;

/** The priority ladder, copied out of the `EVENT_PRIORITY` contract row. */
const contractPriorityLadder: Record<string, number> = {
  question: 1,
  health: 2,
  done: 3,
  "pr-merged": 4,
  "pr-open": 5,
  blocked: 6,
  resumed: 7,
  "update-done": 8,
  "update-failed": 8,
  "thread-open": 9,
  "thread-closed": 10,
  artifact: 11,
  color: 12,
};

/**
 * The SNAP-X event-sound table, transcribed from 50-contract.md's locked spec.
 * Rows are at NOMINAL pitch — PITCH_FACTOR is applied at synthesis, not baked
 * into the table, which is what the two assertions below (`EVENT_SOUNDS` pin vs.
 * the recorded oscillator frequencies) separate.
 *
 * The contract's `[freq, dur_s, at_s, gain]` notation is shorthand for the
 * OBJECT `Pulse` shape (`{ freq; dur; at; gain }`), per the Test-environment
 * amendment — the rows below spell the fields out.
 */
interface ContractPulseSpec {
  freq: number;
  dur: number;
  at: number;
  gain: number;
}

const contractPulse = (
  freq: number,
  dur: number,
  at: number,
  gain: number,
): ContractPulseSpec => ({ freq, dur, at, gain });

const contractEventSoundsSpec: Record<string, ContractPulseSpec[]> = {
  question: [
    contractPulse(988, 0.03, 0, 1.6),
    contractPulse(1319, 0.03, 0.04, 1.6),
    contractPulse(988, 0.03, 0.14, 1.6),
    contractPulse(1319, 0.03, 0.18, 1.6),
  ],
  blocked: [
    contractPulse(988, 0.045, 0, 1.6),
    contractPulse(784, 0.055, 0.05, 1.6),
  ],
  resumed: [
    contractPulse(784, 0.04, 0, 1.5),
    contractPulse(988, 0.045, 0.045, 1.5),
  ],
  done: [
    contractPulse(1047, 0.045, 0, 1.7),
    contractPulse(1319, 0.045, 0.05, 1.7),
    contractPulse(1568, 0.07, 0.1, 1.7),
  ],
  "pr-open": [
    contractPulse(988, 0.03, 0, 1.7),
    contractPulse(988, 0.03, 0.06, 1.7),
    contractPulse(1319, 0.06, 0.12, 1.7),
  ],
  "pr-merged": [
    contractPulse(1047, 0.04, 0, 1.7),
    contractPulse(1319, 0.04, 0.045, 1.7),
    contractPulse(1568, 0.04, 0.09, 1.7),
    contractPulse(2093, 0.09, 0.135, 1.7),
  ],
  health: [
    contractPulse(330, 0.07, 0, 1.5),
    contractPulse(466, 0.07, 0.08, 1.5),
    contractPulse(330, 0.09, 0.16, 1.5),
  ],
  "thread-open": [
    contractPulse(1175, 0.035, 0, 1.5),
    contractPulse(880, 0.045, 0.04, 1.5),
  ],
  "thread-closed": [
    contractPulse(880, 0.035, 0, 1.4),
    contractPulse(659, 0.05, 0.04, 1.4),
  ],
  artifact: [contractPulse(1047, 0.028, 0, 1.4)],
  color: [
    contractPulse(1319, 0.03, 0, 1.2),
    contractPulse(1047, 0.03, 0, 1.2),
  ],
  "update-done": [
    contractPulse(784, 0.035, 0, 1.6),
    contractPulse(1047, 0.035, 0.04, 1.6),
    contractPulse(1319, 0.055, 0.08, 1.6),
  ],
  "update-failed": [
    contractPulse(1319, 0.04, 0, 1.6),
    contractPulse(1047, 0.04, 0.045, 1.6),
    contractPulse(392, 0.09, 0.09, 1.6),
  ],
};

/** The spec rows for one motif; throws rather than returning undefined. */
function contractSpecFor(name: string): ContractPulseSpec[] {
  const rows = contractEventSoundsSpec[name];
  if (!rows) throw new Error(`no contract spec row for "${name}"`);
  return rows;
}

// ---------------------------------------------------------- the audio fake

/**
 * The recording AudioContext fake, built from the structural interfaces in
 * 50-contract.md (§ Slice B) rather than from sfx.test.ts's fake, which is on
 * slice B's edit list and therefore out of bounds for this file.
 *
 * Every AudioParam method a chip voice might plausibly reach for is present so
 * that a missing member cannot silently route the module into its
 * degrade-to-silence path and turn a real assertion into a vacuous one. Only
 * `setValueAtTime` on an oscillator's `frequency` is RECORDED — that is the
 * observable the contract names for PITCH_FACTOR.
 */
const CONTRACT_SAMPLE_RATE = 48_000;
/** The fake's frozen `currentTime`; every scheduled time is an offset from it. */
const CONTRACT_NOW = 100;

type ContractNodeKind = "osc" | "gain" | "biquad" | "convolver";

interface ContractSetCall {
  value: number;
  at: number;
}

interface ContractParam {
  value: number;
  setValueAtTime(value: number, at: number): ContractParam;
  linearRampToValueAtTime(value: number, at: number): ContractParam;
  exponentialRampToValueAtTime(value: number, at: number): ContractParam;
  setTargetAtTime(value: number, at: number, timeConstant: number): ContractParam;
  setValueCurveAtTime(values: unknown, at: number, dur: number): ContractParam;
  cancelScheduledValues(at: number): ContractParam;
}

interface ContractNode {
  kind: ContractNodeKind;
  id: number;
  type: string;
  frequency: ContractParam;
  gain: ContractParam;
  Q: ContractParam;
  detune: ContractParam;
  buffer: unknown;
  normalize: boolean;
  /** every `frequency.setValueAtTime(v, t)` this node saw, in order */
  freqSets: ContractSetCall[];
  starts: number[];
  stops: number[];
  /** every `connect(target)` target, in order */
  outputs: unknown[];
  onended: (() => void) | null;
  connect(target: unknown): unknown;
  disconnect(): void;
  start(at?: number): void;
  stop(at?: number): void;
}

interface ContractBufferRec {
  channels: number;
  frames: number;
  rate: number;
  data: Float32Array[];
  handle: unknown;
}

interface ContractRecorder {
  /** how many times the injected factory was invoked — i.e. contexts built */
  factoryCalls: number;
  nodes: ContractNode[];
  buffers: ContractBufferRec[];
  destination: object;
}

function contractParam(onSet?: (value: number, at: number) => void): ContractParam {
  const p: ContractParam = {
    value: 0,
    setValueAtTime(value: number, at: number) {
      p.value = value;
      onSet?.(value, at);
      return p;
    },
    linearRampToValueAtTime(value: number) {
      p.value = value;
      return p;
    },
    exponentialRampToValueAtTime(value: number) {
      p.value = value;
      return p;
    },
    setTargetAtTime(value: number) {
      p.value = value;
      return p;
    },
    setValueCurveAtTime() {
      return p;
    },
    cancelScheduledValues() {
      return p;
    },
  };
  return p;
}

function contractMakeAudio() {
  const rec: ContractRecorder = {
    factoryCalls: 0,
    nodes: [],
    buffers: [],
    destination: { contractNode: "destination" },
  };
  let nextId = 0;

  function makeNode(kind: ContractNodeKind): ContractNode {
    const n = {
      kind,
      id: nextId++,
      type: "",
      buffer: undefined as unknown,
      normalize: false,
      freqSets: [] as ContractSetCall[],
      starts: [] as number[],
      stops: [] as number[],
      outputs: [] as unknown[],
      onended: null as (() => void) | null,
      connect(target: unknown) {
        n.outputs.push(target);
        return target;
      },
      disconnect() {},
      start(at?: number) {
        n.starts.push(at ?? Number.NaN);
      },
      stop(at?: number) {
        n.stops.push(at ?? Number.NaN);
      },
    } as ContractNode;
    n.frequency = contractParam((value, at) => n.freqSets.push({ value, at }));
    n.gain = contractParam();
    n.Q = contractParam();
    n.detune = contractParam();
    rec.nodes.push(n);
    return n;
  }

  const factory = () => {
    rec.factoryCalls++;
    return {
      sampleRate: CONTRACT_SAMPLE_RATE,
      currentTime: CONTRACT_NOW,
      state: "running",
      destination: rec.destination,
      createOscillator: () => makeNode("osc"),
      createGain: () => makeNode("gain"),
      createBiquadFilter: () => makeNode("biquad"),
      createConvolver: () => makeNode("convolver"),
      createBuffer: (channels: number, frames: number, rate: number) => {
        const data = Array.from(
          { length: channels },
          () => new Float32Array(frames),
        );
        const handle = { getChannelData: (ch: number) => data[ch] };
        rec.buffers.push({ channels, frames, rate, data, handle });
        return handle;
      },
      resume: () => {},
      close: () => {},
    };
  };

  return { rec, factory };
}

type ContractFactoryArg = Parameters<typeof injectAudioContextFactoryForTest>[0];

/**
 * Install the fake and perform the "prior unlock" the contract names: one
 * `playSfx("click")` with a working injected factory, which is what constructs
 * the context in the real gesture path. Returns the recorder plus the index the
 * unlock's own nodes end at, so a later assertion can look only at what the
 * event sounds created.
 */
function contractUnlockedAudio() {
  const audio = contractMakeAudio();
  injectAudioContextFactoryForTest(audio.factory as unknown as ContractFactoryArg);
  setSfxEnabled(true);
  playSfx("click");
  return audio;
}

const contractRound = (v: number) => Math.round(v * 1e6) / 1e6;

/** Every oscillator frequency actually scheduled, in creation order. */
function contractOscFreqs(nodes: readonly ContractNode[]): number[] {
  return nodes
    .filter((n) => n.kind === "osc")
    .flatMap((n) => n.freqSets.map((s) => contractRound(s.value)));
}

/** The pitched frequency list an event motif must produce, from the spec table. */
function contractExpectedFreqs(name: string): number[] {
  return contractSpecFor(name).map((p) => contractRound(p.freq * 0.25));
}

/**
 * Decode which single event motif a slice of newly-created nodes represents, by
 * exact full-sequence match against the pitched spec table. The thirteen
 * sequences are pairwise distinct, so the match is unambiguous; `null` means the
 * slice was not any motif (including "silence").
 */
function contractDecodeOne(nodes: readonly ContractNode[]): EventType | null {
  const freqs = contractOscFreqs(nodes);
  if (freqs.length === 0) return null;
  for (const name of contractEventNames) {
    const want = contractExpectedFreqs(name);
    if (want.length === freqs.length && want.every((v, i) => v === freqs[i])) {
      return name;
    }
  }
  return null;
}

// ------------------------------------------------------------- fixtures

/** A board carrying just the jobs (and optionally an update block) a case needs. */
function contractBoard(jobs: WebJob[], update?: WebBoard["update"]): WebBoard {
  return makeBoard(update ? { jobs, update } : { jobs });
}

/** A live working job with the given slug; `over` carries the case's field. */
function contractJob(slug: string, over: Partial<WebJob> = {}): WebJob {
  return makeJob({ slug, state: "working", live: true, ...over });
}

/** `type@jobKey` strings, sorted — diffBoard's output ORDER is unspecified. */
function contractSig(events: readonly BoardEvent[]): string[] {
  return events.map((e) => `${e.type}@${e.jobKey}`).sort();
}

const CONTRACT_PR_9481 = "https://github.com/ProbookAI/kraken/pull/9481";

/**
 * The worked example from card 11 (`11-poll-tick-flow.html`): three jobs, four
 * changes in one 3-second tick. This job goes blocked WITH a question, a sibling
 * opens PR 9481, a dream job turns purple.
 */
function contractTickPrev(): WebBoard {
  return contractBoard([
    contractJob("kraken-sound-effects"),
    contractJob("csr-latency-fix"),
    contractJob("dream-2026-08-12"),
  ]);
}

function contractTickNext(): WebBoard {
  return contractBoard([
    contractJob("kraken-sound-effects", {
      state: "blocked",
      question: { tool_use_id: "tu_q1" },
    }),
    contractJob("csr-latency-fix", {
      prs: [{ url: CONTRACT_PR_9481, state: "open" }],
    }),
    contractJob("dream-2026-08-12", { color: "purple" }),
  ]);
}

function contractJsonResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

/**
 * The two symbols the contract names in prose but does not put in an export
 * table (`SOUNDS` is "byte-unchanged", `MASTER_GAIN` "stays 0.12"). Reached
 * through the namespace so that a missing export fails ONE test rather than
 * exploding the whole module's import.
 */
const contractSfxExtras = contractSfxModule as unknown as {
  SOUNDS?: Record<string, readonly ContractPulseSpec[] | undefined>;
  MASTER_GAIN?: number;
};

beforeEach(() => {
  localStorage.clear();
  resetSfxForTest();
  resetBoardEventsForTest();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  resetSfxForTest();
  resetBoardEventsForTest();
});

// ================================================================= Slice A

describe("EventType / EVENT_PRIORITY / the tick constants", () => {
  it("EVENT_PRIORITY carries exactly the thirteen event types", () => {
    // "exactly these 13 members" — the type itself is erased at runtime, so the
    // ladder's key set is the only place the membership is observable.
    expect(Object.keys(EVENT_PRIORITY).sort()).toEqual(
      [...contractEventNames].sort(),
    );
  });

  it("EVENT_PRIORITY is the frozen ladder, value for value", () => {
    expect({ ...EVENT_PRIORITY }).toEqual(contractPriorityLadder);
  });

  it("SFX_EVENT_CAP is 2 and EVENT_STAGGER_MS is 150", () => {
    expect(SFX_EVENT_CAP).toBe(2);
    expect(EVENT_STAGGER_MS).toBe(150);
  });
});

describe("diffBoard — the fires table", () => {
  it("fires `question` when question is absent in prev and present in next", () => {
    const prev = contractBoard([contractJob("a")]);
    const next = contractBoard([
      contractJob("a", { question: { tool_use_id: "tu_1" } }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["question@a"]);
  });

  it("fires `health` on the empty-to-non-empty transition", () => {
    // Absent reads as its zero (""), so absent -> "stalled" is the same
    // transition as "" -> "stalled".
    const prev = contractBoard([contractJob("a")]);
    const next = contractBoard([contractJob("a", { health: "stalled" })]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["health@a"]);

    const prevEmpty = contractBoard([contractJob("a", { health: "" })]);
    expect(contractSig(diffBoard(prevEmpty, next))).toEqual(["health@a"]);
  });

  it("fires `done` when a job's state reaches done from a non-done state", () => {
    const prev = contractBoard([contractJob("a", { state: "working" })]);
    const next = contractBoard([contractJob("a", { state: "done" })]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["done@a"]);
  });

  it("fires `blocked` on working -> blocked", () => {
    const prev = contractBoard([contractJob("a", { state: "working" })]);
    const next = contractBoard([contractJob("a", { state: "blocked" })]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["blocked@a"]);
  });

  it("fires `resumed` on blocked -> working", () => {
    const prev = contractBoard([contractJob("a", { state: "blocked" })]);
    const next = contractBoard([contractJob("a", { state: "working" })]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["resumed@a"]);
  });

  it("fires `pr-open` for a url new to next whose state is open", () => {
    // `prs` absent in prev reads as empty, so this is a url arriving open.
    const prev = contractBoard([contractJob("a")]);
    const next = contractBoard([
      contractJob("a", { prs: [{ url: CONTRACT_PR_9481, state: "open" }] }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["pr-open@a"]);
  });

  it("fires `pr-merged` when a known url goes open -> merged", () => {
    const prev = contractBoard([
      contractJob("a", { prs: [{ url: CONTRACT_PR_9481, state: "open" }] }),
    ]);
    const next = contractBoard([
      contractJob("a", { prs: [{ url: CONTRACT_PR_9481, state: "merged" }] }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["pr-merged@a"]);
  });

  it("fires `pr-merged` for a new url that arrives already merged", () => {
    const prev = contractBoard([contractJob("a")]);
    const next = contractBoard([
      contractJob("a", { prs: [{ url: CONTRACT_PR_9481, state: "merged" }] }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["pr-merged@a"]);
  });

  it("fires `thread-open` for a tid new to next whose state is live", () => {
    const prev = contractBoard([contractJob("a")]);
    const next = contractBoard([
      contractJob("a", {
        threads: [{ tid: "t1", state: "live", created_at: 1 }],
      }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["thread-open@a"]);
  });

  it("fires `thread-closed` when a known tid goes live -> closed", () => {
    const prev = contractBoard([
      contractJob("a", {
        threads: [{ tid: "t1", state: "live", created_at: 1 }],
      }),
    ]);
    const next = contractBoard([
      contractJob("a", {
        threads: [{ tid: "t1", state: "closed", created_at: 1, closed_at: 2 }],
      }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["thread-closed@a"]);
  });

  it("fires `artifact` when the artifacts list grows", () => {
    const prev = contractBoard([
      contractJob("a", { artifacts: [{ name: "one", url: "/a/one" }] }),
    ]);
    const next = contractBoard([
      contractJob("a", {
        artifacts: [
          { name: "one", url: "/a/one" },
          { name: "two", url: "/a/two" },
        ],
      }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["artifact@a"]);
  });

  it("fires `color` on a change, and on unset -> set and set -> unset alike", () => {
    // "color value changes, incl. unset<->set"; absent reads as "".
    const unset = contractBoard([contractJob("a")]);
    const purple = contractBoard([contractJob("a", { color: "purple" })]);
    const amber = contractBoard([contractJob("a", { color: "amber" })]);

    expect(contractSig(diffBoard(unset, purple))).toEqual(["color@a"]);
    expect(contractSig(diffBoard(purple, amber))).toEqual(["color@a"]);
    expect(contractSig(diffBoard(purple, unset))).toEqual(["color@a"]);
  });

  it("fires `update-done` on the board-level phase transition into done", () => {
    // Board-level events carry jobKey "" — they belong to no job.
    const prev = contractBoard([], { phase: "installing" });
    const next = contractBoard([], { phase: "done" });
    expect(contractSig(diffBoard(prev, next))).toEqual(["update-done@"]);
  });

  it("fires `update-failed` on the board-level phase transition into failed", () => {
    const prev = contractBoard([], { phase: "building" });
    const next = contractBoard([], { phase: "failed" });
    expect(contractSig(diffBoard(prev, next))).toEqual(["update-failed@"]);
  });

  it("fires update-done when the phase was absent before (absent reads as idle)", () => {
    const prev = contractBoard([]);
    const next = contractBoard([], { phase: "done" });
    expect(contractSig(diffBoard(prev, next))).toEqual(["update-done@"]);
  });

  it("emits several events for one job in one tick — blocked + question", () => {
    // The canonical multi-event case. Order is unspecified, the SET is not.
    const prev = contractBoard([contractJob("a", { state: "working" })]);
    const next = contractBoard([
      contractJob("a", {
        state: "blocked",
        question: { tool_use_id: "tu_1" },
      }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual([
      "blocked@a",
      "question@a",
    ]);
  });

  it("returns [] for the first poll (prev undefined)", () => {
    expect(diffBoard(undefined, contractTickNext())).toEqual([]);
  });

  it("matches jobs by jobKey, so an archived namesake never cross-diffs", () => {
    // Archiving `foo` and then creating a new active `foo` puts TWO rows with
    // slug "foo" on the board (lib/jobKey.ts). Keyed by bare slug the archived
    // row wins last-writer-wins and the live job's transition is read against
    // the wrong prev; keyed by jobKey() the live job's blocked event survives
    // and the archived row contributes nothing.
    const prev = contractBoard([
      contractJob("foo", { state: "working" }),
      makeJob({
        slug: "foo",
        state: "archived",
        archived: true,
        live: false,
        color: "purple",
      }),
    ]);
    const next = contractBoard([
      contractJob("foo", { state: "blocked" }),
      makeJob({
        slug: "foo",
        state: "archived",
        archived: true,
        live: false,
        color: "amber",
      }),
    ]);
    expect(contractSig(diffBoard(prev, next))).toEqual(["blocked@foo"]);
  });

  it("turns the card-11 tick into exactly its four events", () => {
    // Three jobs, four changes: blocked + question on one job, a PR opening on
    // a sibling, a colour set on a third.
    expect(contractSig(diffBoard(contractTickPrev(), contractTickNext()))).toEqual(
      [
        "blocked@kraken-sound-effects",
        "color@dream-2026-08-12",
        "pr-open@csr-latency-fix",
        "question@kraken-sound-effects",
      ],
    );
  });
});

describe("coalesceEvents — collapse, rank, dedup, cap", () => {
  const ev = (type: string, jobKey: string): BoardEvent =>
    ({ type, jobKey }) as BoardEvent;

  it("returns [] for empty input", () => {
    expect(coalesceEvents([])).toEqual([]);
  });

  it("ranks by ascending priority", () => {
    // color 12 handed in first, health 2 second — the ladder, not the input
    // order, decides.
    expect(coalesceEvents([ev("color", "a"), ev("health", "b")])).toEqual([
      "health",
      "color",
    ]);
  });

  it("swallows a blocked event when the SAME job also raised a question", () => {
    expect(coalesceEvents([ev("blocked", "a"), ev("question", "a")])).toEqual([
      "question",
    ]);
  });

  it("keeps a blocked event when the question belongs to a different job", () => {
    // The collapse is per-job: a question on `b` says nothing about `a` being
    // blocked, and both sounds are earned.
    expect(coalesceEvents([ev("blocked", "a"), ev("question", "b")])).toEqual([
      "question",
      "blocked",
    ]);
  });

  it("dedups by type — five simultaneous questions play one chime", () => {
    const five = ["a", "b", "c", "d", "e"].map((k) => ev("question", k));
    expect(coalesceEvents(five)).toEqual(["question"]);
  });

  it("caps the play list at SFX_EVENT_CAP, keeping the top of the ladder", () => {
    const burst = [
      ev("question", "a"),
      ev("done", "b"),
      ev("pr-open", "c"),
      ev("artifact", "d"),
      ev("color", "e"),
    ];
    const play = coalesceEvents(burst);
    expect(play).toHaveLength(SFX_EVENT_CAP);
    expect(play).toEqual(["question", "done"]);
  });

  it("discards the over-cap remainder instead of queueing it into a later tick", () => {
    const burst = [
      ev("question", "a"),
      ev("done", "b"),
      ev("pr-open", "c"),
      ev("artifact", "d"),
      ev("color", "e"),
    ];
    expect(coalesceEvents(burst)).toEqual(["question", "done"]);
    // A second, unrelated tick hears only its own event — nothing carried over.
    expect(coalesceEvents([ev("color", "e")])).toEqual(["color"]);
  });

  it("is pure — it neither reads nor consumes the mute flag", () => {
    // "the mute flag is the wiring's business, not this function's"
    muteNextDiff();
    expect(coalesceEvents([ev("question", "a")])).toEqual(["question"]);
    expect(consumeMuteFlag()).toBe(true);
  });

  it("is deterministic — the same input coalesces the same way twice", () => {
    const burst = [ev("artifact", "a"), ev("health", "b"), ev("color", "c")];
    expect(coalesceEvents(burst)).toEqual(coalesceEvents(burst));
    expect(coalesceEvents(burst)).toEqual(["health", "artifact"]);
  });

  it("takes the differ's card-11 output straight to [question, pr-open]", () => {
    // Four events in, two sounds out: blocked collapses into its own job's
    // question, colour falls over the cap.
    const events = diffBoard(contractTickPrev(), contractTickNext());
    expect(coalesceEvents(events)).toEqual(["question", "pr-open"]);
  });
});

describe("muteNextDiff / consumeMuteFlag — the one-shot flag", () => {
  it("starts false", () => {
    expect(consumeMuteFlag()).toBe(false);
  });

  it("is set by muteNextDiff and cleared by the first consume", () => {
    muteNextDiff();
    expect(consumeMuteFlag()).toBe(true);
    expect(consumeMuteFlag()).toBe(false);
  });

  it("stays set across repeated muteNextDiff calls until consumed once", () => {
    muteNextDiff();
    muteNextDiff();
    expect(consumeMuteFlag()).toBe(true);
    expect(consumeMuteFlag()).toBe(false);
  });

  it("is cleared by resetBoardEventsForTest", () => {
    muteNextDiff();
    resetBoardEventsForTest();
    expect(consumeMuteFlag()).toBe(false);
  });
});

// ================================================================= Slice B

describe("the locked sound spec and the night-bus constants", () => {
  it("PITCH_FACTOR is 0.25", () => {
    expect(PITCH_FACTOR).toBe(0.25);
  });

  it("the reverb constants are the locked cyberpunk-night values", () => {
    expect(REVERB_WET_GAIN).toBe(0.72);
    expect(REVERB_SECONDS).toBe(2.8);
    expect(REVERB_DECAY).toBe(3.2);
    expect(REVERB_LOWPASS_HZ).toBe(3000);
  });

  it("EVENT_SOUNDS is the SNAP-X table, row for row, at nominal pitch", () => {
    // The locked-spec pin: any retune must be a deliberate edit here too.
    const table = Object.fromEntries(
      Object.entries(EVENT_SOUNDS).map(([k, v]) => [k, v.map((p) => ({ ...p }))]),
    );
    expect(table).toEqual(contractEventSoundsSpec);
  });

  it("EVENT_SOUNDS covers exactly the thirteen event types", () => {
    expect(Object.keys(EVENT_SOUNDS).sort()).toEqual(
      [...contractEventNames].sort(),
    );
  });

  it("MASTER_GAIN stays 0.12 — the retune is pitch, not level", () => {
    expect(contractSfxExtras.MASTER_GAIN).toBe(0.12);
  });
});

describe("the night bus — the graph ensureContext builds", () => {
  it("hangs master -> lowpass -> convolver -> wetGain -> destination beside the dry path", () => {
    const { rec } = contractUnlockedAudio();

    const biquads = rec.nodes.filter((n) => n.kind === "biquad");
    const convolvers = rec.nodes.filter((n) => n.kind === "convolver");
    const gains = rec.nodes.filter((n) => n.kind === "gain");
    expect(biquads).toHaveLength(1);
    expect(convolvers).toHaveLength(1);

    const lowpass = biquads[0]!;
    const convolver = convolvers[0]!;
    expect(lowpass.type).toBe("lowpass");
    expect(lowpass.frequency.value).toBe(REVERB_LOWPASS_HZ);

    // master is the gain that feeds the send; the dry path must survive beside
    // it, which is what the destination edge on the SAME node says.
    const master = gains.find((g) => g.outputs.includes(lowpass));
    expect(master, "no gain node feeds the lowpass — the send is unhooked").toBeDefined();
    expect(master!.outputs).toContain(rec.destination);
    expect(master!.outputs).toHaveLength(2);

    expect(lowpass.outputs).toEqual([convolver]);

    const wet = gains.find((g) => convolver.outputs.includes(g));
    expect(wet, "the convolver feeds no gain — the wet level is unhooked").toBeDefined();
    expect(convolver.outputs).toEqual([wet]);
    expect(wet!.gain.value).toBe(REVERB_WET_GAIN);
    expect(wet!.outputs).toEqual([rec.destination]);
  });

  it("synthesises the impulse as a 2-channel noise decay of REVERB_SECONDS", () => {
    const { rec } = contractUnlockedAudio();

    expect(rec.buffers).toHaveLength(1);
    const buf = rec.buffers[0]!;
    expect(buf.channels).toBe(2);
    expect(buf.frames).toBe(Math.ceil(CONTRACT_SAMPLE_RATE * REVERB_SECONDS));
    expect(buf.rate).toBe(CONTRACT_SAMPLE_RATE);

    const convolver = rec.nodes.find((n) => n.kind === "convolver");
    expect(convolver?.buffer).toBe(buf.handle);

    const n = buf.frames;
    for (const channel of [0, 1]) {
      const data = buf.data[channel]!;
      // (random*2-1) * (1 - i/n)^DECAY: white noise inside a decaying envelope,
      // so every sample sits within the envelope and the tail is quieter than
      // the head. A tiny epsilon absorbs Float32 storage rounding only.
      let worst = -1;
      let worstAt = -1;
      for (let i = 0; i < n; i++) {
        const bound = Math.pow(1 - i / n, REVERB_DECAY);
        const over = Math.abs(data[i]!) - bound;
        if (over > worst) {
          worst = over;
          worstAt = i;
        }
      }
      expect(
        worst,
        `channel ${channel} sample ${worstAt} escapes the decay envelope`,
      ).toBeLessThanOrEqual(1e-6);

      const head = data.slice(0, Math.floor(n * 0.05));
      const tail = data.slice(Math.floor(n * 0.95));
      const mean = (a: Float32Array) =>
        a.reduce((s, v) => s + Math.abs(v), 0) / a.length;
      expect(mean(head)).toBeGreaterThan(0);
      expect(mean(head)).toBeGreaterThan(mean(tail));
    }
  });

  it("builds the bus once per context, not once per sound", () => {
    const { rec } = contractUnlockedAudio();
    playSfx("click");
    playSfx("click");
    expect(rec.factoryCalls).toBe(1);
    expect(rec.nodes.filter((n) => n.kind === "biquad")).toHaveLength(1);
    expect(rec.nodes.filter((n) => n.kind === "convolver")).toHaveLength(1);
    expect(rec.buffers).toHaveLength(1);
  });
});

describe("playEventSfx — playing through the context that already exists", () => {
  it("schedules the motif's pulses at nominal x PITCH_FACTOR", () => {
    const { rec } = contractUnlockedAudio();
    const mark = rec.nodes.length;

    playEventSfx("question");

    const played = rec.nodes.slice(mark);
    expect(played.filter((n) => n.kind === "osc")).toHaveLength(
      contractSpecFor("question").length,
    );
    // 988 -> 247, 1319 -> 329.75: the table stays nominal, pulse() does the
    // two-octave drop at synthesis.
    expect(contractOscFreqs(played)).toEqual([247, 329.75, 247, 329.75]);
    expect(contractOscFreqs(played)).toEqual(contractExpectedFreqs("question"));
  });

  it("pitches every one of the thirteen motifs the same way", () => {
    for (const name of contractEventNames) {
      resetSfxForTest();
      const { rec } = contractUnlockedAudio();
      const mark = rec.nodes.length;
      playEventSfx(name);
      expect(
        contractOscFreqs(rec.nodes.slice(mark)),
        `${name} did not play its pitched motif`,
      ).toEqual(contractExpectedFreqs(name));
    }
  });

  it("schedules each pulse at the table's own offset from currentTime", () => {
    const { rec } = contractUnlockedAudio();
    const mark = rec.nodes.length;

    playEventSfx("pr-merged");

    const sets = rec.nodes
      .slice(mark)
      .filter((n) => n.kind === "osc")
      .flatMap((n) => n.freqSets.map((s) => contractRound(s.at)));
    expect(sets).toEqual(
      contractSpecFor("pr-merged").map((p) =>
        contractRound(CONTRACT_NOW + p.at),
      ),
    );
  });

  it("plays through the same master the dry and wet paths hang off", () => {
    const { rec } = contractUnlockedAudio();
    const lowpass = rec.nodes.find((n) => n.kind === "biquad");
    const master = rec.nodes.find(
      (n) => n.kind === "gain" && n.outputs.includes(lowpass),
    );
    expect(master).toBeDefined();
    const mark = rec.nodes.length;

    playEventSfx("done");

    const played = rec.nodes.slice(mark);
    expect(played.some((n) => n.outputs.includes(master))).toBe(true);
    // Nothing the event sound builds may shortcut straight to the speakers —
    // that would skip MASTER_GAIN and the reverb send both.
    expect(played.some((n) => n.outputs.includes(rec.destination))).toBe(false);
  });

  it("has no rate limit — the coalescer is the only gate", () => {
    const { rec } = contractUnlockedAudio();
    const mark = rec.nodes.length;

    playEventSfx("artifact");
    playEventSfx("artifact");
    playEventSfx("artifact");

    // artifact is a single pulse, so three plays is three oscillators; a
    // watermark would have swallowed the second and third.
    expect(rec.nodes.slice(mark).filter((n) => n.kind === "osc")).toHaveLength(3);
  });

  it("applies PITCH_FACTOR to 238's interaction table too", () => {
    // The board-wide steering: ALL sounds ride the low register. SOUNDS itself
    // is byte-unchanged, so the drop must be visible only at synthesis.
    const table = contractSfxExtras.SOUNDS;
    expect(table, "sfx.ts must export SOUNDS for the interaction pin").toBeDefined();
    const click = table?.click;
    expect(click, "SOUNDS.click is the unlock sound the contract names").toBeDefined();

    const { rec } = contractUnlockedAudio();
    const freqs = contractOscFreqs(rec.nodes);
    const nominal = (click ?? []).map((p) => p.freq);
    expect(nominal.length).toBeGreaterThan(0);

    expect(freqs).toEqual(nominal.map((f) => contractRound(f * PITCH_FACTOR)));
    // and the pitched value is genuinely below the table's own row, i.e. the
    // table was not quietly re-tuned in place
    expect(freqs[0]).toBe(contractRound(nominal[0]! * 0.25));
    expect(freqs[0]).not.toBe(nominal[0]);
  });
});

// ================================================================= Slice C

describe("useBoardEvents — the poll-tick wiring", () => {
  /**
   * Mount the hook over an unlocked recording context and hand it boards. The
   * audio fake IS the observer here: what the tick played is decoded back out of
   * the oscillator frequencies, so the assertion is "the speakers heard X", not
   * "some function was called".
   */
  function contractMountWiring() {
    const audio = contractUnlockedAudio();
    const view = renderHook(
      (b: WebBoard | undefined) => useBoardEvents(b),
      { initialProps: undefined as WebBoard | undefined },
    );
    return { audio, view };
  }

  /** Deliver a board and flush the immediate (0ms) entry. */
  function contractDeliver(
    view: ReturnType<typeof renderHook<void, WebBoard | undefined>>,
    board: WebBoard | undefined,
  ) {
    act(() => {
      view.rerender(board);
    });
    act(() => {
      vi.advanceTimersByTime(0);
    });
  }

  beforeEach(() => {
    vi.useFakeTimers();
  });

  it("is silent on the first board — the baseline snapshot", () => {
    const { audio, view } = contractMountWiring();
    const mark = audio.rec.nodes.length;

    contractDeliver(view, contractTickPrev());

    expect(contractOscFreqs(audio.rec.nodes.slice(mark))).toEqual([]);
  });

  it("plays the coalesced set, staggered by EVENT_STAGGER_MS", () => {
    const { audio, view } = contractMountWiring();
    contractDeliver(view, contractTickPrev());

    const mark = audio.rec.nodes.length;
    contractDeliver(view, contractTickNext());

    // entry 0 is immediate
    expect(contractDecodeOne(audio.rec.nodes.slice(mark))).toBe("question");

    const afterFirst = audio.rec.nodes.length;
    act(() => {
      vi.advanceTimersByTime(EVENT_STAGGER_MS);
    });
    expect(contractDecodeOne(audio.rec.nodes.slice(afterFirst))).toBe("pr-open");

    // …and the cap holds: the colour change never gets its turn
    const afterSecond = audio.rec.nodes.length;
    act(() => {
      vi.advanceTimersByTime(10 * EVENT_STAGGER_MS);
    });
    expect(contractOscFreqs(audio.rec.nodes.slice(afterSecond))).toEqual([]);
  });

  it("holds the second sound until exactly EVENT_STAGGER_MS", () => {
    const { audio, view } = contractMountWiring();
    contractDeliver(view, contractTickPrev());
    contractDeliver(view, contractTickNext());

    const afterFirst = audio.rec.nodes.length;
    act(() => {
      vi.advanceTimersByTime(EVENT_STAGGER_MS - 1);
    });
    expect(contractOscFreqs(audio.rec.nodes.slice(afterFirst))).toEqual([]);

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(contractDecodeOne(audio.rec.nodes.slice(afterFirst))).toBe("pr-open");
  });

  it("says nothing when the same board object comes round again", () => {
    const { audio, view } = contractMountWiring();
    const board = contractTickPrev();
    contractDeliver(view, board);

    const mark = audio.rec.nodes.length;
    contractDeliver(view, board);
    act(() => {
      vi.advanceTimersByTime(10 * EVENT_STAGGER_MS);
    });

    expect(contractOscFreqs(audio.rec.nodes.slice(mark))).toEqual([]);
  });

  it("is silent on a muted tick, and consumes the flag doing it", () => {
    const { audio, view } = contractMountWiring();
    contractDeliver(view, contractTickPrev());

    muteNextDiff();
    const mark = audio.rec.nodes.length;
    contractDeliver(view, contractTickNext());
    act(() => {
      vi.advanceTimersByTime(10 * EVENT_STAGGER_MS);
    });

    expect(contractOscFreqs(audio.rec.nodes.slice(mark))).toEqual([]);
    // one-shot: the wiring consumed it, so nothing is left to mute the NEXT tick
    expect(consumeMuteFlag()).toBe(false);
  });

  it("advances the baseline through a muted tick — no re-announcement later", () => {
    // carl's own click caused the mute. The next poll must diff against what he
    // already saw, not against the pre-click board, or every self-inflicted
    // change sounds one tick late.
    const { audio, view } = contractMountWiring();
    contractDeliver(view, contractTickPrev());

    muteNextDiff();
    contractDeliver(view, contractTickNext());

    const mark = audio.rec.nodes.length;
    const third = contractBoard([
      contractJob("kraken-sound-effects", {
        state: "blocked",
        question: { tool_use_id: "tu_q1" },
      }),
      contractJob("csr-latency-fix", {
        prs: [{ url: CONTRACT_PR_9481, state: "open" }],
      }),
      contractJob("dream-2026-08-12", {
        color: "purple",
        artifacts: [{ name: "one", url: "/a/one" }],
      }),
    ]);
    contractDeliver(view, third);
    act(() => {
      vi.advanceTimersByTime(10 * EVENT_STAGGER_MS);
    });

    // only the artifact that arrived AFTER the muted tick is announced
    expect(contractDecodeOne(audio.rec.nodes.slice(mark))).toBe("artifact");
  });

  it("plays a tab-return burst through the ordinary cap, not all of it", () => {
    // Q4: the cap IS the catch-up policy. Six accumulated changes are still two
    // sounds, ranked.
    const { audio, view } = contractMountWiring();
    contractDeliver(
      view,
      contractBoard([contractJob("a"), contractJob("b"), contractJob("c")]),
    );

    const mark = audio.rec.nodes.length;
    contractDeliver(
      view,
      contractBoard([
        contractJob("a", { state: "done", health: "stalled" }),
        contractJob("b", {
          color: "purple",
          artifacts: [{ name: "one", url: "/a/one" }],
        }),
        contractJob("c", {
          threads: [{ tid: "t1", state: "live", created_at: 1 }],
          prs: [{ url: CONTRACT_PR_9481, state: "merged" }],
        }),
      ]),
    );
    expect(contractDecodeOne(audio.rec.nodes.slice(mark))).toBe("health");

    const afterFirst = audio.rec.nodes.length;
    act(() => {
      vi.advanceTimersByTime(EVENT_STAGGER_MS);
    });
    expect(contractDecodeOne(audio.rec.nodes.slice(afterFirst))).toBe("done");

    const afterSecond = audio.rec.nodes.length;
    act(() => {
      vi.advanceTimersByTime(10 * EVENT_STAGGER_MS);
    });
    expect(contractOscFreqs(audio.rec.nodes.slice(afterSecond))).toEqual([]);
  });
});

describe("useBoard.mutate — arming the mute before the refetch", () => {
  it("sets the one-shot flag on a successful mutation", async () => {
    // Every button carl presses routes through mutate(), and the refetch it
    // triggers carries carl's own change back — a diff he already heard as a
    // click. mutate arms the flag so that tick stays silent.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => contractJsonResponse(contractTickPrev())),
    );

    const { result, unmount } = renderHook(() => useBoard());
    await act(async () => {
      await result.current.mutate(async () => ({ ok: true }));
    });

    expect(consumeMuteFlag()).toBe(true);
    unmount();
  });
});
