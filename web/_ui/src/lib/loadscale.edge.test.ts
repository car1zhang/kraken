import { render } from "@testing-library/react";
import { createElement } from "react";
import { describe, expect, it, vi } from "vitest";

// The client is mocked wholesale, exactly as SteerOverlay.test.tsx does it:
// this suite is about what the drilldown DOES and DOES NOT draw for a given
// job, and none of that involves the network.
vi.mock("../api/client.ts", () => ({
  fetchDraft: vi.fn(() => Promise.resolve({ slug: "j", text: "" })),
  fetchJobForm: vi.fn(() => Promise.resolve({ slug: "j", mtime: 0 })),
  steerJob: vi.fn(() => Promise.resolve({ ok: true })),
  reactivateJob: vi.fn(() => Promise.resolve({ ok: true })),
  killJob: vi.fn(() => Promise.resolve({ ok: true })),
  doneJob: vi.fn(() => Promise.resolve({ ok: true })),
  saveDraft: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(),
  openTmux: vi.fn(() => Promise.resolve({ ok: true })),
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  editFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  deleteFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  setJobColor: vi.fn(() => Promise.resolve({ ok: true })),
  retryJob: vi.fn(() => Promise.resolve({ ok: true })),
  answerQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  // the head's goal picker (GoalPicker) — same pair the editor's mock carries
  fetchGoals: vi.fn(() => Promise.resolve({ goals: { week: [], day: [] } })),
  postJobGoal: vi.fn(() => Promise.resolve({ ok: true })),
}));

import type { WebJob, WebJobLoad } from "../api/types.ts";
import { makeJob } from "../test/fixtures.ts";
import {
  SteerOverlay,
  type SteerOverlayProps,
} from "../components/overlays/SteerOverlay.tsx";
import { SCOPE_ANCHORS, UNBLOCK_ANCHORS } from "./format.ts";

// ---------------------------------------------------------------------------
// The drilldown's boundaries: the estimates it must NOT draw, and the two ends
// of the ladder it must draw correctly.
//
// The section is the only place the raw numbers and their anchor words appear
// together, so the failure that matters is silent in both directions: drawn for
// a job that has no estimate it asserts a fact nobody stated, and drawn with the
// wrong anchor for a rung it mislabels every estimate at that rung forever.
//
// This file is JSX-free deliberately — it lives in src/lib/ beside the anchor
// tables it also pins, and `.ts` cannot carry JSX. `createElement` renders the
// same component tree the `.tsx` suites render.
// ---------------------------------------------------------------------------

const edgeDrill = () => document.querySelector('[data-testid="load-drill"]');

function edgeMount(job: WebJob | undefined) {
  const props: SteerOverlayProps = {
    open: true,
    slug: "j",
    mode: "unblock",
    job,
    onClose: () => {},
    mutate: <T,>(op: () => Promise<T>) => op(),
    onOpenArtifact: () => {},
    onOpenThread: () => {},
  };
  return render(createElement(SteerOverlay, props));
}

function edgeJobWithLoad(load: WebJobLoad): WebJob {
  return makeJob({ slug: "j", state: "blocked", live: true, load });
}

describe("load drilldown — absences", () => {
  it("draws nothing for a job that carries no load key", () => {
    // The overwhelmingly common case: most jobs never get an estimate, and the
    // wire says "no estimate" by omitting the key entirely — there is no zeroed
    // object to render defensively against.
    edgeMount(makeJob({ slug: "j", state: "blocked", live: true }));
    expect(edgeDrill()).toBeNull();
  });

  it("draws nothing when there is no job at all", () => {
    // The modal outlives its job by one render whenever a job leaves the board,
    // so `job` really does arrive undefined; a section that read `job.load`
    // without the guard would throw and take the whole modal down with it.
    edgeMount(undefined);
    expect(edgeDrill()).toBeNull();
  });
});

describe("load drilldown — an empty note is not an absent estimate", () => {
  it("still renders the numbers and both anchor words", () => {
    // `note` is not omitempty server-side: an estimate with nothing to say
    // arrives as "". The numbers are the payload, so an empty note must cost
    // the note text and nothing else.
    edgeMount(edgeJobWithLoad({ unblock: 5, scope: 5, note: "", at: 1 }));
    const drill = edgeDrill();
    expect(drill).not.toBeNull();
    const text = drill?.textContent ?? "";
    expect(text).toContain("unblock 5/10");
    expect(text).toContain("Live E2E drive");
    expect(text).toContain("next step 5/10");
    expect(text).toContain("A moderate slice");
  });
});

describe("load drilldown — both ends of the ladder", () => {
  it("renders rung 1 unblock beside rung 10 scope", () => {
    // The two extremes in one estimate, which is also the ratio the board ranks
    // highest: an off-by-one in either table shows up here as the neighbouring
    // rung's word, and only the ends can catch an off-by-one that runs the
    // right direction at the other end.
    edgeMount(edgeJobWithLoad({ unblock: 1, scope: 10, note: "", at: 1 }));
    const text = edgeDrill()?.textContent ?? "";
    expect(text).toContain("unblock 1/10");
    expect(text).toContain("Mechanical");
    expect(text).toContain("next step 10/10");
    expect(text).toContain("The largest coordinated effort");
  });
});

describe("anchor tables — index n-1 is rung n at both ends", () => {
  it("puts rung 1 first and rung 10 last", () => {
    // The whole table is pinned elsewhere; what is pinned HERE is the
    // off-by-one, because a table written 1-indexed with a leading placeholder
    // reads correctly at every rung except the two that bound it.
    expect(UNBLOCK_ANCHORS[0]).toBe("Mechanical");
    expect(SCOPE_ANCHORS[9]).toBe("The largest coordinated effort");
  });
});
