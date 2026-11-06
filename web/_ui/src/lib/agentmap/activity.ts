// The agent map's pure logic: which status a card shows, which pose each
// character holds, and which transcript events are new since the last look.
//
// Nothing here touches the DOM or a canvas, so every rule the map's look
// depends on can be read (and tested) without a browser. The drawing that
// consumes these answers lives beside this file in draw.ts and scene.ts.

import type { AgentEvent, WebActivity, WebAgent, WebJob } from "../../api/types.ts";
import { isFrozen } from "../format.ts";

/**
 * The activity vocabulary, in core.ActivityIDs' order. The Go classifier is
 * the authority; this copy exists because a pose table keyed by these ids has
 * to be exhaustive on the client too.
 */
export const ACTIVITY_IDS: readonly string[] = [
  "think",
  "edit",
  "search",
  "read",
  "run",
  "home",
  "delegate",
  "talk",
  "review",
  "test",
  "web",
  "wait",
  "check",
  "ship",
  "data",
  "build",
  "other",
];

/**
 * How long a finished call's pose is held through the thinking gap that
 * follows it before the lead drifts back to `think`. A call is usually a
 * flash and the gap after it the long part, so without the hold a working
 * lead would read as thinking almost all of the time.
 */
export const HOLD_MS = 6000;

export type MapStatus =
  | "working"
  | "blocked"
  | "asking"
  | "urgent"
  | "question"
  | "parked"
  | "limit"
  | "frozen"
  | "done"
  | "draft";

function asksQuestion(job: WebJob): boolean {
  return job.zeus_status === "question" || !!job.question;
}

/**
 * How many of the job's question rows are open and at an importance the
 * operator is expected to act on: an unanswered P0 or P1 (`doc_questions`,
 * the rows a question round files). An answered row is a decision, and a P2
 * is a cosmetic default the cell builds on without waiting, so neither
 * counts. A loop rather than filter(): the map asks this per card per frame.
 */
export function openAsks(job: WebJob): number {
  return openRows(job, true);
}

/** How many of the job's question rows are open P0s: the ones the cell cannot build past. */
export function openP0s(job: WebJob): number {
  return openRows(job, false);
}

function openRows(job: WebJob, p1Too: boolean): number {
  const rows = job.doc_questions;
  if (!rows) return 0;
  let n = 0;
  for (let i = 0; i < rows.length; i++) {
    const q = rows[i]!;
    if (!q.answer && (q.importance === "P0" || (p1Too && q.importance === "P1"))) n++;
  }
  return n;
}

/**
 * The status a map card wears. A job reads as BLOCKED when its state says so
 * or when it carries a health condition — the board's own override
 * (bucketJobs files an unhealthy cell under blocked whatever its file says),
 * so a card cannot be blocked in one view and working in the other.
 *
 * A job the operator put on hold is FROZEN before any of the blocked
 * readings: the hold says it does not need them right now, so a pending
 * question, a spent limit or a gone cell on it is not what the card shows.
 * `isFrozen` is the board's own test (a blocked, non-archived job carrying
 * `frozen_at`), so the map sleeps exactly the cards the board files in its
 * FROZEN drawer.
 *
 * Within the blocked readings, a pane dialog (`question`, an AskUserQuestion
 * the board answers with buttons) comes before the open question rows —
 * `urgent` for an open P0, `asking` for an open P1 with no P0, both answered
 * on the board's question panel — and all three before a gone cell
 * (`parked`): the questions are the operator's to answer whether or not the
 * cell is still up, so they are what the card shows, and every question
 * reading outranks a plain block in the gesture and the colour too. A
 * WORKING job with open rows is still working — it filed them and carried
 * on — so the two are blocked readings only.
 */
export function mapStatus(job: WebJob): MapStatus {
  if (job.state === "draft") return "draft";
  if (job.state === "done") return "done";
  if (isFrozen(job)) return "frozen";
  if (job.state === "blocked" || job.health) {
    if (LIMIT_RE.test(job.health ?? "")) return "limit";
    if (asksQuestion(job)) return "question";
    if (openP0s(job) > 0) return "urgent";
    if (openAsks(job) > 0) return "asking";
    if (!job.live) return "parked";
    return "blocked";
  }
  if (asksQuestion(job)) return "question";
  return "working";
}

/** Hoisted: the map calls mapStatus on every frame, and a literal would be a new RegExp each call. */
const LIMIT_RE = /limit/i;

/**
 * The statuses whose lead holds its card's new artifacts as sheets of paper:
 * every reading of a stopped cell the operator may want to look into. A
 * working lead's hands are busy with its activity's props, a frozen one is on
 * hold by the operator's own word (the same reason mapStatus hides its other
 * readings), and a done or draft card has no figure to hold anything.
 */
const PAPER_STATUSES: ReadonlySet<MapStatus> = new Set<MapStatus>([
  "blocked",
  "asking",
  "urgent",
  "question",
  "parked",
  "limit",
]);

/**
 * How many sheets of paper the lead holds: the card's NEW artifacts (the
 * wire's `artifacts[].new`, an artifact the operator has not opened since it
 * last changed), on the statuses above, and none otherwise. A loop rather
 * than filter(), for openAsks' reason.
 */
export function heldPapers(job: WebJob): number {
  if (!PAPER_STATUSES.has(mapStatus(job))) return 0;
  const arts = job.artifacts;
  if (!arts) return 0;
  let n = 0;
  for (let i = 0; i < arts.length; i++) if (arts[i]!.new) n++;
  return n;
}

/**
 * The newest event that is a call the agent made, not a message it received.
 * The pose (leadPose) and the activity band's label (ActivityStrip) both read
 * it, so the two can never pick different events.
 */
export function newestCall(events: readonly AgentEvent[] | undefined): AgentEvent | undefined {
  if (!events) return undefined;
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e && e.act !== "hear") return e;
  }
  return undefined;
}

/**
 * The lead's pose. A non-working card holds its status pose. A working lead
 * acts out its newest call — while the call is still running, and for
 * HOLD_MS after it finished — and thinks otherwise. `other` (a tool with no
 * motion of its own) reads as thinking rather than as a pose nobody drew.
 */
export function leadPose(job: WebJob, nowMs: number): string {
  const status = mapStatus(job);
  if (status !== "working") return status;
  const e = newestCall(job.activity?.events);
  if (!e) return "think";
  if (e.act === "other") return "think";
  if (e.open) return e.act;
  return nowMs <= e.t + (e.dur_ms ?? 0) + HOLD_MS ? e.act : "think";
}

/**
 * The instant (unix millis) after which `leadPose(job, ·)` stops returning
 * what it returns at `nowMs`, or null when time alone can never change it:
 * a non-working card, no call, an `other` call, a still-open call, or a hold
 * that has already run out. The activity band schedules its one re-render on
 * this.
 */
export function leadPoseExpiry(job: WebJob, nowMs: number): number | null {
  if (mapStatus(job) !== "working") return null;
  const e = newestCall(job.activity?.events);
  if (!e || e.act === "other" || e.open) return null;
  const end = e.t + (e.dur_ms ?? 0) + HOLD_MS;
  return nowMs <= end ? end + 1 : null;
}

/**
 * A crew member's pose. A teammate has its own transcript and so its own
 * newest call (`activity.crew`); a subagent runs inside the lead's process and
 * has none, so it gets the generic `work` loop. `nowMs` is part of the
 * signature so a later time-based rule needs no call-site change.
 */
export function crewPose(
  agent: WebAgent,
  activity: WebActivity | undefined,
  _nowMs: number,
): string {
  if (agent.kind === "monitor") return "wait";
  if (!agent.working) return "idle";
  const teammate = (agent.kind ?? "teammate") === "teammate";
  if (teammate) {
    // a loop rather than find(): this runs per crew member per drawn frame,
    // and a callback would be a closure each time
    const crew = activity?.crew;
    if (crew) {
      for (let i = 0; i < crew.length; i++) {
        const entry = crew[i]!;
        if (entry.name === agent.name) return entry.act === "other" ? "think" : entry.act;
      }
    }
  }
  return "work";
}

/**
 * How hard a blocked lead waves, 0..1: the load estimate's payoff per unit of
 * carl's effort (`scope / unblock`), a third of the ROI scale saturating it.
 * A job with no estimate waves at a middling 0.4 rather than not at all.
 */
export function blockedUrgency(job: WebJob): number {
  const load = job.load;
  if (load && load.unblock > 0) {
    return Math.min(1, Math.max(0, load.scope / load.unblock / 3));
  }
  return 0.4;
}

/** The newest `n` events, oldest first — the ticks in a map card's activity band. */
export function ribbon(job: WebJob, n = 24): AgentEvent[] {
  const events = job.activity?.events;
  if (!events || n <= 0) return [];
  return events.slice(-n);
}

/** The events not yet seen, in the order given. */
export function freshEvents(
  seen: ReadonlySet<string>,
  events: readonly AgentEvent[],
): AgentEvent[] {
  return events.filter((e) => !seen.has(e.id));
}

const ACTIVITY_NAMES: Readonly<Record<string, string>> = {
  home: "report",
  web: "fetch",
  check: "lint",
  data: "query",
};

/** The word a person reads for an activity id. */
export function activityName(act: string): string {
  return ACTIVITY_NAMES[act] ?? act;
}
