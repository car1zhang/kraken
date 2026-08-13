// What CHANGED between two board snapshots, as typed events the sound layer
// can play. The poll side has always rendered every change silently; this is
// the module that lets the board say something happened.
//
// Two deliberate properties, both of which the whole design hangs off:
//
//   1. This is a LEAF. It imports wire types and jobKey and nothing else — no
//      sfx.ts, no React, no clock. `diffBoard` is a pure function of two
//      snapshots and `coalesceEvents` is a pure function of its input, so the
//      hard part (what counts as an event, and which two of a tick's events
//      get heard) is testable without an AudioContext or a rendered tree.
//      sfx.ts imports
//      EventType FROM here; the arrow must never point back.
//
//   2. Absence is read as the ZERO, never as a change. Almost every field the
//      differ looks at is `omitempty` on the Go side (internal/api/board.go:
//      `color`, `artifacts`, `health`, `prs`, `threads`, `question` all carry
//      the tag), so a field is missing exactly when it is empty — and a decode
//      that drops a key must not sound like the user did something. The one
//      field that is NOT optional either side is `state`.
//
// The differ works on the RAW `state` field, not the column a card is drawn
// in. Those disagree on purpose: bucketJobs (lib/format.ts:420) relocates any
// job with non-empty `health` into the BLOCKED column, so a sick working job
// looks blocked on screen while its state still says "working". Diffing the
// rendered bucket would fire `blocked` off a health blip and `resumed` off its
// recovery — the health event already covers that story, once.

import type { WebBoard, WebJob } from "../api/types.ts";
import { jobKey } from "./jobKey.ts";

/**
 * The thirteen things the board can announce. Eleven are per-job; the two
 * `update-*` members are board-level and carry an empty jobKey.
 *
 * There is no enabled-set mechanism and no per-event toggle: the whole feature
 * rides the one `kraken-sfx` switch sfx.ts already owns (read at sfx.ts:251,
 * written by the ♪ SFX toggle in HudControls.tsx:54). Pruning the palette
 * later means editing the sound table, not threading a preference through
 * three layers — a second toggle for "which events" was considered and cut,
 * because the coalescer below is the thing that actually keeps the board quiet.
 */
export type EventType =
  | "question"
  | "health"
  | "done"
  | "pr-merged"
  | "pr-open"
  | "blocked"
  | "resumed"
  | "update-done"
  | "update-failed"
  | "thread-open"
  | "thread-closed"
  | "artifact"
  | "color";

/**
 * One thing that happened to one job. `jobKey` is what the coalescer collapses
 * on, so it must be jobKey() output and never a bare slug — see below.
 *
 * The two board-level events (`update-done`, `update-failed`) belong to no job
 * and carry `""`. That is a real key nothing else can collide with: jobKey()
 * returns either the slug or `archive:<slug>`, and neither is empty.
 */
export interface BoardEvent {
  type: EventType;
  jobKey: string;
}

/**
 * Which event wins when more than two fire in a tick. Lower is louder.
 *
 * The ladder is a claim about what carl needs to hear FIRST when ten things
 * move at once, not about what is interesting: a question is a hard stop that
 * needs him, a sick cell is work quietly dying, a finished job is news. Colour
 * changes sit at the bottom because they are almost always his own click
 * coming back off the poll.
 *
 * `update-done` and `update-failed` deliberately share rank 8: they are the
 * same board-level announcement in two flavours and cannot both fire in one
 * tick anyway (a phase is one string). Nothing may depend on their relative
 * order.
 */
export const EVENT_PRIORITY: Record<EventType, number> = {
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
 * How many sounds one poll tick may make. Two, and the reason is the tab
 * return: usePoll's `pauseWhenHidden` option (on by default, and on for the
 * board) skips ticks while `document.hidden`, and its visibilitychange
 * listener forces a tick the moment the tab comes back — so the first board
 * after a coffee break can carry every change of the last twenty minutes.
 * Uncapped, that is a chord. Capped at two, it is a headline.
 */
export const SFX_EVENT_CAP = 2;

/**
 * Gap between the two sounds of a tick. Short enough to read as one event
 * ("something happened, and also something else") rather than two unrelated
 * pings, long enough that the two motifs stay separately audible.
 *
 * It is a START offset, not a gap between finished sounds: entry 0 plays
 * immediately and entry 1 is scheduled at 150ms, so the longer motifs overlap
 * on purpose. Waiting for the first to finish would put a fifth of a second of
 * dead air inside what is meant to be one announcement.
 */
export const EVENT_STAGGER_MS = 150;

/**
 * A row that is not a live job. Two spellings for one condition, and both are
 * checked: `JobState` names "archived" (api/types.ts:211) while the board's own
 * bucketer keys on the boolean alone (bucketJobs, lib/format.ts:416). A row
 * satisfying either is out of the diff on BOTH sides.
 *
 * Skipping the archive is not an optimisation. Archiving a done job would
 * otherwise read as the job vanishing, and unarchiving it as a job appearing —
 * neither of which is a sound, and one of which (`done`) would fire twice for
 * the same completion.
 */
function isArchived(j: WebJob): boolean {
  return j.archived === true || j.state === "archived";
}

/** Live jobs by jobKey(). Archived rows never enter. */
function indexJobs(jobs: readonly WebJob[]): Map<string, WebJob> {
  const byKey = new Map<string, WebJob>();
  for (const j of jobs) {
    if (isArchived(j)) continue;
    // jobKey(), never `j.slug`: an archived job rides the same array under the
    // same slug as a new live one (lib/jobKey.ts spells out the collision), so
    // a slug-keyed map is a last-writer-wins bug waiting for the day both
    // exist. The archived side is filtered out one line up, which makes the
    // collision unreachable HERE today — the key is still jobKey() so that
    // stays true if the filter ever loosens.
    byKey.set(jobKey(j), j);
  }
  return byKey;
}

/**
 * Everything one job changed between the two snapshots, appended to `out`.
 *
 * One job may emit several events in one tick and that is the normal case, not
 * an edge: an agent that stops with a question produces `blocked` AND
 * `question`, which is precisely the pair the coalescer knows how to collapse.
 * Order within a job is not meaningful — coalesceEvents sorts by priority and
 * nothing else reads a BoardEvent[] directly.
 */
function diffJob(prev: WebJob, next: WebJob, key: string, out: BoardEvent[]): void {
  // Absent → present only. A question whose TEXT changes is the same
  // unanswered ask being re-rendered (zeus rewriting its options, a decode
  // that dropped an item), and present → absent is carl answering it — he was
  // there, he does not need telling. The Go field is `*webQuestion` with
  // omitempty (internal/api/board.go:419), so absence is a dropped key rather
  // than a null and `undefined` is the whole test.
  if (prev.question === undefined && next.question !== undefined) {
    out.push({ type: "question", jobKey: key });
  }

  // "" means healthy (api/types.ts:243), and the sound is for FALLING ill.
  // Non-empty → non-empty is the same sickness being re-described on the next
  // tick and must stay silent, which is why this compares against "" on the
  // prev side rather than just asking whether the string differs. Recovery is
  // deliberately unvoiced: the board going quiet is the good news.
  const prevHealth = prev.health ?? "";
  const nextHealth = next.health ?? "";
  if (prevHealth === "" && nextHealth !== "") {
    out.push({ type: "health", jobKey: key });
  }

  // Done from ANY other state — including blocked→done, which is carl
  // dispositioning a stopped job, and draft→done, which nothing produces today
  // but would still be a job finishing if it did.
  if (prev.state !== "done" && next.state === "done") {
    out.push({ type: "done", jobKey: key });
  }

  // working→blocked only. A draft is not "working that stopped": submitting a
  // draft that immediately fails lands in blocked without a second of work
  // having happened, and announcing that as an interruption would train the
  // ear to distrust the sound. The refusal is already on screen against the
  // action that caused it.
  if (prev.state === "working" && next.state === "blocked") {
    out.push({ type: "blocked", jobKey: key });
  }
  if (prev.state === "blocked" && next.state === "working") {
    out.push({ type: "resumed", jobKey: key });
  }

  diffPRs(prev, next, key, out);
  diffThreads(prev, next, key, out);

  // Length only, so a rename or a reorder of the same artifacts is silent and
  // a deletion is silent too. Comparing by name would announce every rewrite
  // of a file zeus keeps regenerating; comparing the count announces arrival,
  // which is the only artifact news worth a sound.
  if ((next.artifacts?.length ?? 0) > (prev.artifacts?.length ?? 0)) {
    out.push({ type: "artifact", jobKey: key });
  }

  // Absent means unset (`color,omitempty`, internal/api/board.go:396), so
  // reading it as "" makes set→unset and unset→set fall out of the same
  // inequality as a recolour. This is the one event that is usually carl's own
  // click echoing back off the poll — hence the bottom of the priority ladder,
  // not an exclusion: a colour set from the phone or by an agent is real news.
  if ((prev.color ?? "") !== (next.color ?? "")) {
    out.push({ type: "color", jobKey: key });
  }
}

/**
 * PR arrivals and merges, keyed by url.
 *
 * Only two things sound: a url the board has never seen, and a known url
 * crossing into merged. A KNOWN url turning up `open` again (a reopen) is
 * churn — the announcement already happened when it arrived — and a brand new
 * url arriving `closed` is a PR that died before the board ever heard of it,
 * which is nothing at all rather than a quiet failure.
 *
 * A new url arriving already `merged` DOES fire, as a merge rather than an
 * open: the board can miss the open entirely (a PR opened and merged inside
 * one 3s poll gap, or a job whose first snapshot already has the merge), and
 * the merge is the half worth hearing.
 */
function diffPRs(prev: WebJob, next: WebJob, key: string, out: BoardEvent[]): void {
  const before = new Map<string, string>();
  for (const pr of prev.prs ?? []) before.set(pr.url, pr.state);
  for (const pr of next.prs ?? []) {
    const was = before.get(pr.url);
    if (was === undefined) {
      if (pr.state === "open") out.push({ type: "pr-open", jobKey: key });
      else if (pr.state === "merged") out.push({ type: "pr-merged", jobKey: key });
    } else if (was === "open" && pr.state === "merged") {
      out.push({ type: "pr-merged", jobKey: key });
    }
  }
}

/**
 * Side threads spawning and closing, keyed by tid.
 *
 * Same shape as the PR rule and for the same reasons, with one asymmetry worth
 * naming: a tid DISAPPEARING is not a close. `thread-closed` fires on a live
 * thread the board watched go closed, because a tid that simply stops being on
 * the wire is indistinguishable from a truncated list or a job the poll caught
 * mid-write, and the closed thread is still on the board with `state:
 * "closed"` when it really closes (api/types.ts:92-100).
 */
function diffThreads(prev: WebJob, next: WebJob, key: string, out: BoardEvent[]): void {
  const before = new Map<string, string>();
  for (const t of prev.threads ?? []) before.set(t.tid, t.state);
  for (const t of next.threads ?? []) {
    const was = before.get(t.tid);
    if (was === undefined) {
      if (t.state === "live") out.push({ type: "thread-open", jobKey: key });
    } else if (was === "live" && t.state === "closed") {
      out.push({ type: "thread-closed", jobKey: key });
    }
  }
}

/**
 * Two board snapshots in, the events between them out.
 *
 * `prev === undefined` is the FIRST board of the session and returns nothing.
 * That is not a guard against a crash, it is the baseline rule: every job on
 * the first board is "new" against nothing, and a page load that played the
 * whole board's state at once would be the loudest thing the app ever did.
 *
 * A job present on only ONE side produces nothing either. Creation, deletion,
 * archival, unarchival and — the case that makes this rule earn its keep —
 * RENAME are all silent, because a rename changes the slug and therefore the
 * jobKey, so the same job arrives as one disappearance plus one appearance. A
 * differ that voiced appearances would announce a rename as a whole new job
 * and, worse, re-announce the renamed job's every current condition.
 */
export function diffBoard(prev: WebBoard | undefined, next: WebBoard): BoardEvent[] {
  if (prev === undefined) return [];

  const out: BoardEvent[] = [];
  const before = indexJobs(prev.jobs);
  for (const job of next.jobs) {
    if (isArchived(job)) continue;
    const was = before.get(jobKey(job));
    if (was === undefined) continue;
    diffJob(was, job, jobKey(job), out);
  }

  // The self-update control. `update` is absent on a server predating it and
  // every field inside is optional anyway (api/types.ts:339), so a missing
  // phase reads as "idle" — the constant the Go side actually starts from
  // (UpdatePhase's first member, api/types.ts:314).
  //
  // Transition INTO the terminal phase, not presence of it: `done` and
  // `failed` are resting states that sit on the wire until the next update
  // run, so a phase compared by value alone would re-announce the same
  // finished update on every 3s tick until carl clicked something.
  const prevPhase = prev.update?.phase ?? "idle";
  const nextPhase = next.update?.phase ?? "idle";
  if (nextPhase === "done" && prevPhase !== "done") {
    out.push({ type: "update-done", jobKey: "" });
  }
  if (nextPhase === "failed" && prevPhase !== "failed") {
    out.push({ type: "update-failed", jobKey: "" });
  }

  return out;
}

/**
 * The rate limit: a tick's worth of events down to at most SFX_EVENT_CAP
 * sounds, in the order they should play.
 *
 * Three reductions, in this order, and the order matters:
 *
 *   1. Same-job question ⊃ blocked. An agent that stops to ask produces both,
 *      and they are ONE occurrence: the job stopped because it needs carl.
 *      Playing the blocked motif under the question motif spends the tick's
 *      second slot describing the same thing twice, and would push a genuinely
 *      unrelated event off the end. Scoped per jobKey — job A's question says
 *      nothing about job B being blocked.
 *   2. Dedup by type. Five jobs finishing in one tick is one `done` sound.
 *      The board shows how many; the ear only needs to know the kind.
 *   3. Priority, then cap. Sorting BEFORE slicing is what makes the cap a
 *      ranking rather than a coin flip on arrival order.
 *
 * Pure, and deliberately so: the mute flag below is the wiring's business, not
 * this function's. A coalescer that consulted module state could not be tested
 * as a table.
 */
export function coalesceEvents(events: readonly BoardEvent[]): EventType[] {
  const questioning = new Set<string>();
  for (const e of events) {
    if (e.type === "question") questioning.add(e.jobKey);
  }

  const seen = new Set<EventType>();
  const types: EventType[] = [];
  for (const e of events) {
    if (e.type === "blocked" && questioning.has(e.jobKey)) continue;
    if (seen.has(e.type)) continue;
    seen.add(e.type);
    types.push(e.type);
  }

  // Array.prototype.sort is stable (ES2019 on), so the priority-8 pair keeps
  // its input order rather than shuffling — an implementation detail nothing
  // is allowed to assert, since only one of the two can occur per tick.
  types.sort((a, b) => EVENT_PRIORITY[a] - EVENT_PRIORITY[b]);
  return types.slice(0, SFX_EVENT_CAP);
}

/**
 * One-shot suppression of the NEXT diff, plus the generation counter that lets
 * an arm be withdrawn without stepping on a newer one.
 *
 * Module state rather than a parameter because of who has to reach it:
 * useBoard's `mutate()` knows a write happened, and the effect that diffs
 * boards is somewhere else entirely with no channel between them. A flag both
 * can see is the smallest thing that works.
 *
 * The reason it exists: `mutate()` refetches after the write on BOTH its
 * success and its failure path (the server is the truth either way), so every
 * button carl presses is followed within milliseconds by a board carrying
 * exactly the change he just made. Announcing that is the app telling him what
 * he already knows, and it is the difference between a feature and a nuisance.
 * Poll-driven change — the agent doing something on its own — is the only
 * thing worth a sound.
 *
 * ONE-shot, not a mode: a stuck mute would silence the board forever with no
 * visible symptom, and the symptom would surface at the worst possible moment
 * (the tab-return burst, the one tick that carries real news). Two separate
 * mechanisms keep it short-lived — the first reader consumes it, and a writer
 * whose refetch delivered nothing withdraws it.
 */
let muteNext = false;

/**
 * Monotonic count of arms. Never reset except by the test seam, and never
 * compared for magnitude — only for equality against a token, which is all the
 * "is this disarm still current?" question needs.
 */
let muteGen = 0;

/**
 * Arm the flag; returns the generation token for this arm.
 *
 * Called BEFORE the write, not after it: a poll tick can land between `await
 * fn()` returning and the refetch starting, and that tick's board already
 * carries carl's change. Arming first means whichever board lands first — the
 * straddling poll or the refetch — is the one that gets eaten.
 *
 * Arming that early is also why the token exists. An arm before the write is
 * an arm that may never be paid for: a refetch that is paused, hidden,
 * aborted, or failed delivers no board at all, and without a way to withdraw,
 * the flag would sit armed until some arbitrary later tick spent it on
 * unrelated news. The caller hands this token back to `disarmMute` in that
 * case, and the token is what stops an older mutate's withdrawal from
 * cancelling a newer mutate's arm.
 */
export function muteNextDiff(): number {
  muteNext = true;
  muteGen += 1;
  return muteGen;
}

/**
 * Withdraw the arm identified by `gen` — the "my refetch delivered nothing"
 * path.
 *
 * A no-op unless `gen` is still the current generation. Two cases it must not
 * break, and equality covers both: a SECOND mutate armed after this one (its
 * arm is live and stays live — the older caller's disarm is stale), and a
 * poll tick that already consumed this arm (the flag is false, so clearing it
 * changes nothing and the generation has not moved).
 */
export function disarmMute(gen: number): void {
  if (gen !== muteGen) return;
  muteNext = false;
}

/**
 * Read the flag and clear it. A true answer means "play nothing this tick" —
 * NOT "skip the diff": the caller must still advance its previous-board ref,
 * or the muted change comes back as news on the following poll.
 */
export function consumeMuteFlag(): boolean {
  const was = muteNext;
  muteNext = false;
  return was;
}

/**
 * Drop the flag AND the generation counter. Test seam only — it exists because
 * both outlive any single render tree, so a test that mutes and never consumes
 * would leak the mute into whichever test vitest runs next.
 *
 * The counter is rewound too, not just the flag: leaving it climbing would
 * make a token's numeric value depend on how many tests ran before this one,
 * and a suite that hard-codes the first arm's token as 1 would pass alone and
 * fail in a full run.
 */
export function resetBoardEventsForTest(): void {
  muteNext = false;
  muteGen = 0;
}
