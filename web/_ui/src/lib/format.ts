// Pure formatting/derivation helpers, ported one-for-one from web/static/app.js.
//
// Everything here is a function of its arguments alone — no DOM, no fetch, no
// module-level mutable state — which is what lets vitest cover the rules the
// board's paint depends on (state color, age, the settled-job rule, the P0
// flag count) without rendering anything.

import type { WebJob } from "../api/types.ts";
import { COLUMNS, type Column } from "./columns.ts";

// ---------------------------------------------------------------- job colour

/**
 * The eight names Claude Code's own `/color` accepts, in `/color`'s order —
 * the same list internal/jobs/color.go's Palette carries and the same eight
 * `--job-*` custom properties style.css declares (guarded against the Go
 * palette by web/jobpalette_test.go).
 *
 * A COLOUR, not a default: unset means NO colour rather than one auto-assigned,
 * and an uncoloured job renders exactly as it always did.
 */
export const JOB_COLORS = [
  "red",
  "blue",
  "green",
  "yellow",
  "purple",
  "orange",
  "pink",
  "cyan",
] as const;

/**
 * jobColorVar resolves a job's colour to the CSS var holding it, or "" when the
 * job has none. Ported verbatim from app.js:102.
 *
 * The value lands on the card root as the `--job-color` inline custom property
 * and style.css reads it back for the 2px left accent bar
 * (`border-left: 2px solid var(--job-color, var(--text-2))`). That bar is the
 * job's colour channel and NOTHING else — it carries neither state nor alerts —
 * so a card that stops emitting the property does not degrade, it silently
 * loses a whole feature: every coloured job's bar falls back to grey, with no
 * error anywhere. The colour is also what internal/jobs/color.go types into the
 * cell's live tmux pane on a daemon pass, so board and pane are meant to be the
 * same pixel colour.
 *
 * The whitelist check is what makes the result safe to hand to a style
 * attribute at all: an unrecognised value from the wire drops to "" rather than
 * reaching the stylesheet. Callers must then OMIT the property rather than set
 * it to "" — a custom property set to the empty string substitutes nothing, and
 * `var(--job-color, …)` would resolve to nothing rather than to its fallback,
 * making the whole `border-left` declaration invalid at computed-value time.
 *
 * The parameter is `{ color?: string }` rather than `Pick<WebJob, "color">` so
 * the rule does not depend on how the wire type spells the field.
 */
export function jobColorVar(j: { color?: string } | null | undefined): string {
  const c = j?.color;
  return c && (JOB_COLORS as readonly string[]).includes(c) ? `var(--job-${c})` : "";
}

// ---------------------------------------------------------------- clocks

/**
 * age formats a unix timestamp in SECONDS as a compact "3m"/"2h"/"5d".
 *
 * Seconds, not millis: both call sites on the board (`j.mtime` and
 * `daemon.last_tick`) are second-resolution, and app.js:94 divides Date.now()
 * by 1000 before subtracting. An empty/zero stamp renders as "" rather than
 * as a nonsense age since the epoch.
 */
export function age(unix: number | undefined | null): string {
  if (!unix) return "";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}

/**
 * logTime formats an event-log stamp as `MM/DD HH:MM:SS` in the reader's local
 * zone (app.js:3841).
 *
 * `ts` is unix MILLIS (internal/util/eventlog.go: `TS int64 \`json:"ts"\``),
 * which is what `new Date(ts)` wants directly — no ×1000 anywhere. Contrast
 * `age` directly above, whose input is SECONDS.
 *
 * A missing stamp renders blank rather than as a date. Vanilla's
 * `new Date(undefined)` gave `NaN/NaN NaN:NaN:NaN`, which at least looked
 * broken; the obvious port (`ts ?? 0`) would print a perfectly plausible
 * 1969/1970 timestamp instead, and a log line whose time is quietly wrong is
 * worse than one that admits it has none.
 */
export function logTime(ts: number | undefined | null): string {
  if (!ts) return "";
  const d = new Date(ts);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** countdown formats a whole number of seconds as "Xm Ys", floored at 0. */
export function countdown(seconds: number): string {
  const clamped = Math.max(0, seconds);
  const m = Math.floor(clamped / 60);
  const s = clamped % 60;
  return `${m}m ${s}s`;
}

/**
 * wakeupLabel is the wakeup chip's countdown text. `firesAt` is unix MILLIS
 * (WebWakeup.fires_at). Once it is past — kraken hasn't caught the next
 * discovery pass yet — it reads "due" rather than a negative countdown.
 */
export function wakeupLabel(firesAt: number): string {
  const remainingS = Math.floor((firesAt - Date.now()) / 1000);
  return remainingS <= 0 ? "due" : countdown(remainingS);
}

/**
 * fmtTokens renders one raw counter at a glance: 15756009 -> "15.8M".
 * Ported verbatim from app.js:483.
 *
 * Precision shrinks as magnitude grows (2dp under 10, 1dp under 100, none
 * above) so every value lands in four characters plus a unit. Nothing here is
 * exact enough to earn more digits: a transcript caught mid-write is resumed
 * next pass, so these are a floor rather than a settled total.
 */
export function fmtTokens(n: number | undefined | null): string {
  const v = Math.max(0, Number(n) || 0);
  const units: Array<[number, string]> = [
    [1e9, "B"],
    [1e6, "M"],
    [1e3, "K"],
  ];
  for (let i = 0; i < units.length; i++) {
    const unit = units[i];
    if (!unit) continue;
    const [div, suffix] = unit;
    if (v < div) continue;
    const scaled = v / div;
    let out = scaled.toFixed(scaled < 10 ? 2 : scaled < 100 ? 1 : 0);
    // Rounding can carry a value into the next unit up — 999_999 formats as
    // "1000K", which is both wrong-looking and wider than the unit exists to
    // keep it. Promote instead.
    if (parseFloat(out) >= 1000 && i > 0) return "1" + (units[i - 1]?.[1] ?? suffix);
    // Trim only INSIDE a decimal part — a blanket /0+$/ would turn "100"
    // into "1".
    if (out.includes(".")) out = out.replace(/0+$/, "").replace(/\.$/, "");
    return out + suffix;
  }
  return String(Math.round(v));
}

// ---------------------------------------------------------------- questions

/**
 * settledJob: a done or archived job. Its questions are history, not a
 * standing ask — nobody is waiting on an answer to keep working — so it gets
 * no board presence at all: no count chip, no flag. They live one click into
 * the job instead (the editor modal for a done job, the steer modal in
 * reactivate mode, the artifact-side surfaces), where answering still works
 * exactly as before: answers are editable forever, live or done.
 *
 * Archived jobs never carry doc_questions on the wire at all (board.go), so
 * this only ever fires for done ones — it is stated anyway so the rule reads
 * the same on both sides of the wire.
 */
export function settledJob(j: Pick<WebJob, "archived" | "state"> | null | undefined): boolean {
  return !!j && (!!j.archived || j.state === "done");
}

/**
 * docQuestionsOpenP0 counts open (unanswered) P0 doc questions for a job —
 * folded into the card-level "flagged" state and the header's ⚑ counter
 * alongside j.question/j.health. A settled job contributes 0: its questions
 * aren't visible on the board, so flagging the board for them would point at
 * something nothing on that surface shows.
 */
export function docQuestionsOpenP0(j: WebJob): number {
  if (settledJob(j)) return 0;
  return (j.doc_questions ?? []).filter((q) => q.importance === "P0" && !q.answer).length;
}

// ---------------------------------------------------------------- load / roi

/**
 * The human-readable rungs of the 1-10 unblock scale — index `n-1` is rung n.
 *
 * The numbers alone are a private vocabulary: "unblock 4" means nothing to a
 * reader who is not zeus, which is what the anchor ladder in
 * `.claude/skills/load/SKILL.md` exists to fix. That ladder is the definition;
 * this array is the board's copy of it, rendered in the steer modal's
 * drilldown (Q3's decision — the CARD keeps the bare `⚡ 4.0× 2→8` badge, since
 * a label per rung is a sentence and the card has a chip's worth of room).
 *
 * This job's frozen contract pins the labels byte-identical across three
 * sources — the skill file, this file, and `ios/Kraken/Models.swift` — with one
 * Go test parsing all three. That is why each label is a plain double-quoted
 * literal here rather than composed, interpolated or built from a shorter
 * token: the pin can only read what it can parse, and a label it cannot see is
 * a drift it cannot catch.
 */
export const UNBLOCK_ANCHORS: readonly string[] = [
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

/** The same ladder for the scope half — what the reply BUYS. See above. */
export const SCOPE_ANCHORS: readonly string[] = [
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

/**
 * The four ROI bands, best-first. Shared vocabulary with the CSS (`.ld-chip.hot`
 * and friends in styles/style.css) and with the iOS card, which bands the same
 * ratio — so a name added or renamed here has to move there too.
 */
export type LoadBand = "hot" | "warm" | "even" | "cold";

export interface LoadROI {
  /** scope / unblock, as a float — the number the band is decided on */
  roi: number;
  band: LoadBand;
}

/**
 * loadROI derives a job's triage ratio and its band, or null when there is
 * nothing to show.
 *
 * The bands are closed-open on the FLOAT ratio, boundary value in the HIGHER
 * band: `>= 3.0` hot, `>= 1.5` warm, `>= 0.75` even, below that cold. Deciding
 * on the float rather than on the one-decimal text `loadRatioText` prints is
 * deliberate, and it means the two can disagree at the edges — a ratio of 2.96
 * prints "3.0×" and still bands warm. The alternative is worse: banding the
 * rounded number would make the band depend on a display choice, and the iOS
 * card would have to reproduce that rounding exactly to agree.
 *
 * null covers three cases that all mean "no badge", and the division guard is
 * the reason they are one function rather than three checks at the call site:
 *   - no estimate at all (absent `load` key),
 *   - both numbers 0, and
 *   - a half-estimate (one of the two at 0 or below).
 *
 * Only the first of those three can actually arrive today: a CLEAR is stored as
 * a zero `at`, which drops the key entirely rather than sending zeroed numbers,
 * and a half-estimate is refused 400 (see WebJobLoad in api/types.ts, which
 * traces both). The other two arms are defensive and are meant to stay that
 * way — `scope / 0` is Infinity and `0 / 0` is NaN, and either would reach the
 * card as a badge reading "Infinity×" with nothing to catch it downstream.
 */
export function loadROI(
  load: { unblock: number; scope: number } | null | undefined,
): LoadROI | null {
  if (!load) return null;
  const { unblock, scope } = load;
  if (!(unblock > 0) || !(scope > 0)) return null;
  const roi = scope / unblock;
  const band: LoadBand = roi >= 3 ? "hot" : roi >= 1.5 ? "warm" : roi >= 0.75 ? "even" : "cold";
  return { roi, band };
}

/**
 * The ratio's one rendering: `4.0×`. Extracted so the badge, the tooltip and
 * the steer modal's drilldown cannot print the same number three ways — the
 * precision is a shared-contract requirement (`toFixed(1)` rounds half away
 * from zero, so 0.25 reads "0.3"), and a surface that rounded differently
 * would look like a different estimate rather than like a formatting bug.
 *
 * Behaviour is unchanged from the two call sites it was lifted out of.
 */
export function loadRatioText(roi: number): string {
  return `${roi.toFixed(1)}×`;
}

/**
 * loadBadgeTitle is the badge's tooltip AND its accessibility label — one
 * string, because a title attribute is what a pointer gets and a screen reader
 * announces, and two spellings of the same sentence would drift.
 *
 * The AGE clause is required by the shared contract, and it is the only thing
 * on the card that can say the estimate is stale. Nothing ever refreshes or
 * clears one: a job that blocks, is answered, and whose cell dies hours later
 * is filed back under blocked by bucketJobs' health rule, and the badge then
 * describes a question already answered — by an agent that is no longer alive
 * to write a new estimate. Without the age there is nothing on screen that
 * distinguishes that card from a fresh one.
 *
 * `at` is unix MILLIS and `age` takes SECONDS (its own comment is explicit that
 * both its board callers are second-resolution), hence the divide.
 *
 * Dropping that divide does NOT produce an absurd age somebody would spot.
 * `age` clamps with `Math.max(0, now - unix)`, so a millis stamp read as
 * seconds — decades in the FUTURE — goes hugely negative and comes back 0:
 * measured this session, `age(1769040000000)` is "0s". Every card would read
 * `· estimated 0s ago`, permanently brand-new, which is the exact opposite of
 * the staleness the clause exists to show and the far more plausible-looking
 * failure of the two. The MIRROR mistake is the loud one — a SECONDS stamp
 * passed in here gets divided a second time and measured 20454d — and it is
 * that direction the "reads `at` as MILLIS" test next door pins.
 *
 * `age` returns "" for a zero/absent stamp, and the clause is then dropped
 * rather than rendered as "estimated  ago"; a present `load` always carries
 * `at > 0`, so that is the same defensive-only category as loadROI's zero arms.
 *
 * The note is appended last and only when there is one: `note` may legitimately
 * be "", and a trailing " — " with nothing after it reads as a truncation.
 */
export function loadBadgeTitle(
  load: { unblock: number; scope: number; note?: string; at?: number },
  roi: number,
): string {
  let s = `unblock effort ${load.unblock}/10 · next step ${load.scope}/10 · roi ${loadRatioText(roi)}`;
  const ago = age(load.at ? Math.floor(load.at / 1000) : 0);
  if (ago) s += ` · estimated ${ago} ago`;
  if (load.note) s += ` — ${load.note}`;
  return s;
}

// ---------------------------------------------------------------- usage

/**
 * usageColorClass thresholds a percent into the same ok/warn/danger palette
 * used everywhere else in the board (solved/revealed/counter). Lives here
 * rather than beside the usage overlay because the header widget and the
 * drilldown — two different slices — must not drift apart on it.
 */
export function usageColorClass(percent: number): "u-ok" | "u-warn" | "u-danger" {
  if (percent >= 80) return "u-danger";
  if (percent >= 50) return "u-warn";
  return "u-ok";
}

/**
 * usageCountdown formats a resets_at (unix MILLIS) as a compact countdown —
 * "3h12m", "4d", "8m", "now" — mirroring wakeupLabel's feel at day/hour/minute
 * granularity instead of minutes/seconds.
 */
export function usageCountdown(millis: number): string {
  const remainingS = Math.floor((millis - Date.now()) / 1000);
  if (remainingS <= 0) return "now";
  const d = Math.floor(remainingS / 86400);
  if (d >= 1) return `${d}d`;
  const h = Math.floor(remainingS / 3600);
  const m = Math.floor((remainingS % 3600) / 60);
  return h >= 1 ? `${h}h${m}m` : `${m}m`;
}

// ---------------------------------------------------------------- bucketing

/** columnFor maps a job state to its board column, defaulting to draft. */
export function columnFor(state: string): Column {
  return (COLUMNS as readonly string[]).includes(state) ? (state as Column) : "draft";
}

export interface BoardBuckets {
  byColumn: Record<Column, WebJob[]>;
  archived: WebJob[];
}

/**
 * The header's ⚑ counter: pending questions + unhealthy cells + open P0s,
 * over the ACTIVE jobs (app.js:606-618).
 *
 * Split out of bucketJobs rather than returned beside the buckets, because its
 * one consumer is the header and bucketJobs' one consumer is useBoard — folding
 * them together meant the chrome ran the whole bucketing a second time, on the
 * same array, to read a single integer off it.
 */
export function flagCount(jobs: readonly WebJob[]): number {
  let flags = 0;
  for (const j of jobs) {
    if (j.archived) continue; // drawer, not a column — and never flagged
    if (j.question) flags++;
    if (j.health) flags++;
    flags += docQuestionsOpenP0(j);
  }
  return flags;
}

/**
 * bucketJobs reproduces render() (app.js:601) exactly, and is THE bucketing —
 * useBoard.byColumn is built from it, so the render layer and the drag layer
 * cannot disagree about which column a card is in. (They once did: this
 * comment used to say byColumn "deliberately does not carry" the rule below,
 * and that divergence was a bug — see useBoard.ts.) The rule: **an unhealthy
 * cell is blocked regardless of its file state** — it needs a retry, and the daemon is
 * not going to fix it. Bucketing from the flat server-ordered `jobs` array
 * rather than re-sorting useBoard's buckets keeps the relocated card in the
 * same relative position vanilla put it in.
 */
export function bucketJobs(jobs: readonly WebJob[]): BoardBuckets {
  const byColumn = Object.fromEntries(COLUMNS.map((c) => [c, [] as WebJob[]])) as Record<
    Column,
    WebJob[]
  >;
  const archived: WebJob[] = [];
  for (const j of jobs) {
    if (j.archived) {
      archived.push(j); // drawer, not a column
      continue;
    }
    byColumn[j.health ? "blocked" : columnFor(j.state)].push(j);
  }
  return { byColumn, archived };
}
