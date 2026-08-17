import { render } from "@testing-library/react";
import { createElement } from "react";
import { describe, expect, it, vi } from "vitest";

import {
  SteerOverlay,
  type SteerOverlayProps,
} from "../components/overlays/SteerOverlay.tsx";
import { makeJob } from "../test/fixtures.ts";
import { SCOPE_ANCHORS, UNBLOCK_ANCHORS } from "./format.ts";

// loadscale.contract.test.ts — the TypeScript half of the standardized load
// scale, written blind from artifacts/50-contract.md.
//
// Two obligations:
//
//   the anchor TABLES are the canonical twenty labels, in rung order. The Go
//   cross-source pin (internal/jobs/loadscale_contract_test.go) parses this
//   file's source and holds it against the skill file and Swift; this suite
//   holds the same list against the module's RUNTIME export, which is what the
//   board actually renders. Source and runtime agreeing is not free — a label
//   built by concatenation or reordered at export would pass the parse.
//
//   the DRILLDOWN renders them. The chip carries a ratio and nothing else, on
//   the promise that the numbers behind it are one click away in the modal;
//   #steer's other surfaces already had exactly this promise broken once, so
//   the assertion is "a job that HAS an estimate shows it here, given nothing
//   but the job", never "the component renders what it is handed".
//
// This file is .ts rather than .tsx, so the overlay is mounted through
// createElement — the props are the same ones SteerOverlay.test.tsx passes.

// The overlay's own module graph reaches the API client on mount. Mocked
// wholesale for the reason SteerOverlay.test.tsx gives: what this suite observes
// is rendered text, not the network.
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

/**
 * The canonical unblock ladder, rung 1 first, copied byte for byte out of
 * 50-contract.md § Canonical anchor labels. Hardcoded rather than imported from
 * the module under test: this list IS the requirement.
 */
const contractUnblockAnchors = [
  "Mechanical",
  "Read one page, pick",
  "Answer a few concrete questions",
  "Local smoke, or a credential",
  "Live E2E drive",
  "Genuine review",
  "Decision batch needing new context",
  "Deep decision, real consequences",
  "Many deep decisions in one sitting",
  "A day's thought",
];

/** The canonical scope ladder, same source and same rule. */
const contractScopeAnchors = [
  "One-message close-out",
  "One small round",
  "One focused artifact or round",
  "A single-slice implementation",
  "A moderate slice",
  "A full /aim set draft",
  "A delegated build",
  "Full phase-2 of a substantial spec",
  "A multi-PR program",
  "The largest coordinated effort",
];

const contractNoop = () => {};
const contractMutate = async <T,>(op: () => Promise<T>) => op();

/**
 * Mounts the overlay open, in unblock mode, on the given job. Every REQUIRED
 * prop is supplied the way SteerOverlay.test.tsx supplies it; only `job` varies
 * across the cases below, which is the point — the drilldown must come from the
 * job alone.
 */
function contractMountSteer(job: SteerOverlayProps["job"]) {
  return render(
    createElement(SteerOverlay, {
      open: true,
      slug: "j",
      mode: "unblock",
      job,
      onClose: contractNoop,
      mutate: contractMutate,
      onOpenArtifact: contractNoop,
      onOpenThread: contractNoop,
    } as SteerOverlayProps),
  );
}

const contractDrill = () =>
  document.querySelector('[data-testid="load-drill"]') as HTMLElement | null;

describe("UNBLOCK_ANCHORS / SCOPE_ANCHORS", () => {
  it("carry exactly the ten canonical unblock labels, in rung order", () => {
    expect([...UNBLOCK_ANCHORS]).toEqual(contractUnblockAnchors);
  });

  it("carry exactly the ten canonical scope labels, in rung order", () => {
    expect([...SCOPE_ANCHORS]).toEqual(contractScopeAnchors);
  });

  it("have ten rungs each — the ladders are 1-10 on both axes", () => {
    // A ladder short of ten rungs is not a smaller ladder: index n-1 silently
    // returns undefined for every rung past its end, so the drilldown prints a
    // number with no meaning beside it.
    expect(UNBLOCK_ANCHORS).toHaveLength(10);
    expect(SCOPE_ANCHORS).toHaveLength(10);
  });
});

describe('#steer [data-testid="load-drill"] — the load drilldown', () => {
  it("renders both numbers, both anchor words, the roi and the note", () => {
    contractMountSteer(
      makeJob({
        slug: "j",
        state: "blocked",
        live: true,
        load: {
          unblock: 5,
          scope: 2,
          note: "needs a live booking",
          at: 1_700_000_000_000,
        },
      }),
    );
    const drill = contractDrill();
    expect(drill).not.toBeNull();
    const text = drill?.textContent ?? "";
    // the raw numbers stay — the anchors explain them, they do not replace them
    expect(text).toContain("unblock 5/10");
    expect(text).toContain("next step 2/10");
    // …and the word for each rung, which is the whole feature
    expect(text).toContain("Live E2E drive");
    expect(text).toContain("One small round");
    // the ratio, formatted exactly as loadBadgeTitle formats it
    expect(text).toContain("roi 0.4×");
    // and zeus' one-line rationale, which is often the only thing that says
    // WHICH live environment
    expect(text).toContain("needs a live booking");
  });

  it("renders nothing for a job with no estimate", () => {
    // Absence is the wire's only spelling of "no estimate" — never estimated,
    // cleared, and archived all arrive as a missing key.
    contractMountSteer(makeJob({ slug: "j", state: "blocked", live: true }));
    expect(contractDrill()).toBeNull();
  });

  it("renders nothing for a half estimate — no ratio, nothing to draw", () => {
    // Unreachable from the current server (a half pair is a 400), so this pins
    // the defence against a hand-edited row rather than a live path.
    for (const load of [
      { unblock: 5, scope: 0, note: "n", at: 1 },
      { unblock: 0, scope: 5, note: "n", at: 1 },
    ]) {
      const { unmount } = contractMountSteer(
        makeJob({ slug: "j", state: "blocked", live: true, load }),
      );
      expect(contractDrill()).toBeNull();
      unmount();
    }
  });
});
