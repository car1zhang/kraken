// Wire types for `kraken serve`'s JSON API.
//
// Transcribed from the Go wire structs in internal/api/ (board.go, peers.go,
// update.go, shipios.go, threads.go, jobedit.go, …), cross-checked against
// ios/API.md and ios/Kraken/Models.swift. Same rule of thumb Models.swift
// applies, for the same reason: a Go field tagged `,omitempty` becomes
// optional here; a field with no tag is always present and is not.
//
// The implication runs ONE WAY only — optional here does not imply omitempty
// there — and two deliberate exceptions rely on that:
//
//   1. absence as signal — `contract`/`stale`/`update`/`ship_ios` are always
//      present on a current server, but absent on one predating them, and
//      that absence is what identifies an old server.
//   2. blast-radius containment — every field of WebUpdate/WebShipIOS is
//      always on the wire and optional anyway, so a drifted sub-key costs the
//      ⟳/⇧ control rather than the whole /api/board decode.
//
// This file must not drift from the Go structs. It is the web board's half of
// the same contract internal/api/contract_test.go pins for the iOS app —
// though note the board itself never reads `contract`: it ships inside the
// same binary as the server, so it cannot version-skew. apiContract exists
// for the phone.

// ---------------------------------------------------------------- board

// The goals wire item is declared in ../lib/goals.ts, not here, and imported
// back: `groupGoals` is its only real consumer, TS tests can only reach it
// where it is exported, and two declarations of the same six fields would
// drift. This is the one wire shape that lives outside this file, and the
// import is type-only, so nothing is pulled into the bundle by it.
import type { Goal } from "../lib/goals.ts";

export type { Goal };

/**
 * The two current-period lists the board payload carries, and what GET
 * /api/goals answers with. Both arrays are ALWAYS present on the wire (the
 * server emits `[]`, never null) — the optionality is on the containing key,
 * not on these.
 */
export interface WebGoals {
  week: Goal[];
  day: Goal[];
}

/**
 * The goal a job links, flattened onto the job — `webJobGoal`
 * (internal/api/board.go). Three fields, not the whole Goal: the card chip and
 * the steer head render the text and mute it when done, and neither has any
 * use for a period or a position.
 *
 * Present EXACTLY when the job links a goal, whatever that goal's period and
 * whether or not it is done — so a job carrying last week's goal still says so
 * on its card.
 */
export interface WebJobGoal {
  id: number;
  text: string;
  done: boolean;
}

/** GET /api/goals. `job_goal` rides along only when `?slug=` was given AND
 *  that job links a goal. */
export interface GoalsPage {
  goals: WebGoals;
  job_goal?: WebJobGoal;
}

export interface WebArtifact {
  name: string;
  url: string;
}

export interface WebQuestionOption {
  label: string;
  description: string;
}

export interface WebQuestionItem {
  question: string;
  header: string;
  multi_select: boolean;
  options?: WebQuestionOption[];
}

/** An unanswered AskUserQuestion call in zeus' transcript. */
export interface WebQuestion {
  tool_use_id: string;
  questions?: WebQuestionItem[];
}

/** One live subagent in the cell's tmux session. */
export interface WebAgent {
  name: string;
  model: string;
  // no omitempty on the Go side: "" and false are meaningful roster states
  role: string;
  status: string;
  working: boolean;
}

/** A pending ScheduleWakeup on the zeus peer. `fires_at` is unix millis. */
export interface WebWakeup {
  reason: string;
  fires_at: number;
}

export type Importance = "P0" | "P1" | "P2";

/** One `questions` row. `answer`/`answered_at` are kraken-owned. */
export interface WebDocQuestion {
  id: string;
  text: string;
  context?: string;
  importance: Importance | string;
  recommendation?: string;
  answer?: string;
  answered_at?: string;
}

/** One `feedback` row. Never carries the snapshot bytes. */
export interface WebFeedback {
  id: number;
  artifact: string;
  note: string;
  at: number;
  addressed?: string;
  addressed_at?: string;
}

export type ThreadState = "live" | "closed" | string;

/** One side thread: a fork of zeus' session. */
export interface WebThread {
  tid: string;
  title?: string;
  state: ThreadState;
  /** cleared on close, so absent rather than stale for a closed thread */
  pane?: string;
  created_at: number;
  closed_at?: number;
  /**
   * The model ALIAS the thread was branched on — one of lib/threadquick.ts's
   * THREAD_MODELS, never an exact model id (the server resolves the id at spawn
   * time and keeps it off the wire). Omitempty on the Go side, and absent is the
   * legacy state rather than a default: every row that predates the column reads
   * back as "", so a thread without this key runs on the job's model exactly as
   * it always did.
   */
  model?: string;
}

export type PRState = "open" | "merged" | "closed" | string;

export interface JobPR {
  url: string;
  state: PRState;
}

/**
 * Per-job token spend, mirroring api.webTokens (internal/api/board.go:299).
 *
 * Counters are RAW and unweighted — no blended cost, deliberately: cache reads
 * bill far below input, and weighting here would bake today's price ratio into
 * the UI (the same reason the wire carries no total).
 *
 * Absence is meaningful and is NOT a measured zero: the key is emitted only
 * when a store row exists, so `tokens === undefined` means never measured
 * (no transcripts yet, or the daemon has not reached this job) while a row of
 * zeros means measured-and-cheap. Rendering a zero for the former would claim
 * the job was free.
 */
export interface WebTokens {
  /** input_tokens — uncached input */
  in: number;
  /** output_tokens — generated */
  out: number;
  cache_write: number;
  cache_read: number;
  /** transcript FILES summed, root plus subagent sidecars */
  sessions: number;
  /** unix MILLIS, matching WebJob.mtime's convention */
  updated_at: number;

  /**
   * Zeus' half of the four counters above — a SUBSET of them, not an addend:
   * `in` already INCLUDES `zeus_in`, so summing the two would double-count the
   * lead's spend.
   *
   * Subagent spend has no key of its own and never will: it is `in - zeus_in`
   * and so on, derived client-side. Two halves on the wire could arrive
   * disagreeing with the total they are supposed to add up to; a derived half
   * cannot.
   *
   * Optional HERE but not on the wire: the Go struct tags carry no omitempty,
   * so a current server always sends all four. A server predating them sends
   * none, and that absence is the whole presence test — checked on ONE key
   * rather than on their sum, because a job whose lead genuinely burned
   * nothing (every token spent by a teammate) is a real state a sum-based
   * test would hide.
   */
  zeus_in?: number;
  zeus_out?: number;
  zeus_cache_write?: number;
  zeus_cache_read?: number;
}

/**
 * Zeus' two 1..10 estimates for a BLOCKED job, and the board's whole triage
 * input: `unblock` is how much of carl's own effort the unblock costs
 * (reading, deciding, explaining), `scope` is how much claude work that reply
 * buys. The ratio the badge renders is `scope / unblock`.
 *
 * roi is NOT on the wire and never will be — it is derived at render
 * (`loadROI`, lib/format.ts). Two stored halves can drift out of agreement
 * with what they divide to; a derived one cannot. Same reasoning as
 * WebTokens' zeus/subagent split directly above.
 *
 * Whenever this object is present both numbers are integers in 1..10 — there is
 * no zero on the wire in either direction. A CLEAR is a write of `unblock 0,
 * scope 0`, but it does not arrive as a zeroed object: `jobLoadPost`
 * (internal/api/jobload.go) stores `store.JobLoad{}` so `at` is 0,
 * `webJobLoadFrom` returns nil for `at == 0`, and the Go field is a pointer
 * with omitempty — so the KEY IS DROPPED. A half-estimate (one of the two at 0)
 * is rejected 400 for the same reason a clear has no ratio.
 *
 * `loadROI` still refuses both shapes rather than dividing by them. That is
 * defensive only — neither can be produced by the current server — and it is
 * what keeps an `Infinity×` badge off the screen if that ever stops being true.
 */
export interface WebJobLoad {
  /** carl's effort to unblock: 1..10 (see above — a clear drops the key, it does not send 0) */
  unblock: number;
  /** size of the claude work that unblock buys: 1..10, same as above */
  scope: number;
  /**
   * One-line rationale; may be "". The server one-lines it (control characters
   * become spaces, runs of whitespace collapse) and caps it at 200 RUNES —
   * `core.TruncateOneLine` measures `[]rune`, which is neither a byte count
   * (equal only for ASCII) nor JS `String.length` in UTF-16 code units (equal
   * only within the BMP — an emoji is 1 rune but 2 code units). An over-long
   * note therefore arrives already cut, as 199 runes plus a "…".
   */
  note: string;
  /** unix MILLIS, stamped server-side at write time; > 0 whenever this object is present */
  at: number;
}

/**
 * A palette colour name. The RUNTIME list lives in `lib/format.ts`
 * (`JOB_COLORS`), not here: this module is wire TYPES and every other export in
 * it is erasable, so a value would change its category — and one list is the
 * point. `jobColorVar` and both swatch strips read that one.
 */
export type JobColor = string;

/**
 * Four states plus the archive. `queued` is retired: submit starts the cell
 * synchronously, so a job is a draft until the POST returns and a working cell
 * after it — there is no parked state for the wire to name.
 */
export type JobState = "draft" | "working" | "blocked" | "done" | "archived";

export type ZeusStatus = "running" | "polling" | "waiting" | "question" | string;

export interface WebJob {
  slug: string;
  repo: string;
  state: JobState | string;
  err?: string;
  outcome?: string;
  /**
   * The job's chosen colour: one of JOB_COLORS, omitted entirely when unset —
   * a client that sees no key renders the card exactly as it did before
   * colours existed, which is why the cards emit `--job-color` only when this
   * is present and let style.css fall back to `--text-2` otherwise.
   *
   * Typed as the union WIDENED with string: the server validates against
   * jobs.ValidColor, so an unknown value cannot normally arrive, but a future
   * palette entry must not fail the decode.
   */
  color?: JobColor | string;
  cell?: string;
  live: boolean;
  artifacts?: WebArtifact[];
  /** zeus' last assistant text block, capped at 4000 chars */
  detail?: string;
  /** the last real user message in zeus' transcript, whatever the channel */
  last_sent?: string;
  question?: WebQuestion;
  zeus_status?: ZeusStatus;
  wakeup?: WebWakeup;
  roster?: WebAgent[];
  /** computed condition on a live cell; "" means healthy */
  health?: string;
  mtime: number;
  /**
   * Archived jobs ride the same array with state "archived", and their slug
   * is unique only WITHIN the archive — archiving `foo` and then creating a
   * new active `foo` puts two entries with slug "foo" on the board, archived
   * appended last. Anything keyed by bare slug therefore hands the archived
   * entry a last-writer-wins victory over the live job. Always key by
   * jobKey() (see ../lib/jobKey.ts), never by slug.
   */
  archived?: boolean;
  display_slug?: string;
  cell_gone?: boolean;
  reactivatable?: boolean;
  prs?: JobPR[];
  doc_questions?: WebDocQuestion[];
  feedback?: WebFeedback[];
  threads?: WebThread[];
  /** Absent means NEVER MEASURED, which is not a measured zero — see WebTokens. */
  tokens?: WebTokens;
  /**
   * Present means "estimated, and the estimate still stands". ABSENT is three
   * different states the wire does not separate, so no client can:
   *
   *   - never estimated,
   *   - estimated and then CLEARED — a clear zeroes `at`, `webJobLoadFrom`
   *     returns nil for that, and the Go pointer's omitempty drops the key. It
   *     does NOT arrive as an object with two zeros, so a test like
   *     `job.load && job.load.unblock === 0` is unreachable forever.
   *   - ARCHIVED, estimated or not: `archivedWebJob` (internal/api/board.go,
   *     the helper buildWebBoard's archived loop calls) deliberately never sets
   *     Load, and the argument is stated there — an archived job cannot be
   *     blocked, so serving a forward-looking triage number would be a stale
   *     estimate reading as a current fact. The four columns survive archival
   *     on the job row, so the key comes back on unarchive.
   *
   * That is a weaker guarantee than `tokens` above, which is served for done
   * and archived jobs too. Optional here, but the object's four inner fields
   * are not: once the key is present a client reads all four unconditionally.
   */
  load?: WebJobLoad;
  /**
   * The goal this job is linked to, or absent. Absence is the ONLY no-goal
   * state on the wire — the Go field is a pointer with omitempty, so an
   * unlinked job drops the key rather than sending a zeroed object, and a
   * test like `job.goal && job.goal.id === 0` is unreachable.
   *
   * Served for ANY linked goal, including a done one and one from a period
   * that is no longer current, which is why the card chip mutes rather than
   * hides a done goal.
   */
  goal?: WebJobGoal;
  // `start_error` is GONE from the wire with the queued state. It existed
  // because a start happened on a daemon tick nobody was watching, so its
  // failure had nowhere to land but the card. A submit is now a request carl
  // made and is waiting on: the refusal comes back as that request's own 409
  // and is shown against the action, not stored on the job.
}

/** Mirrors jobs.DaemonStatus (internal/jobs/daemon.go:50). */
export interface DaemonStatus {
  pid: number;
  started_at: number;
  last_tick: number;
  last_error?: string;
  cells: number;
  /** the daemon's own self-check: true once make install replaced its binary */
  stale?: boolean;
}

/**
 * The phase constants in internal/jobs/update.go's const block (the whole
 * list, `uptodate` included).
 *
 * `| string` keeps an unknown future phase from failing the decode, but it
 * also means a WRONG member here costs nothing at runtime and so cannot be
 * caught by tests — which is exactly how this list previously carried an
 * invented `testing` while missing `restarting` and `uptodate`. Check any
 * edit to it against the Go constants by eye; nothing else will.
 */
export type UpdatePhase =
  | "idle"
  | "preflight"
  | "fetching"
  | "merging"
  | "building"
  | "installing"
  | "restarting"
  | "mac app"
  | "done"
  | "failed"
  | "uptodate"
  | string;

/**
 * Mirrors api.webUpdate (internal/api/update.go:118). Every field is on the
 * wire unconditionally, and every one is optional here ANYWAY — the same
 * blast-radius containment Models.swift applies: a drifted sub-key must cost
 * the ⟳ control, not the whole /api/board decode.
 *
 * Optionality is the containment. An index signature is NOT — it silently
 * types every undeclared key as `unknown`, which is exactly how `build_sha`
 * and `checked_at` went missing here while still being read by
 * UpdateControl, forcing a wall of `typeof x === "number"` guards to get them
 * back. Do not reintroduce one.
 */
export interface WebUpdate {
  phase?: UpdatePhase;
  from_sha?: string;
  to_sha?: string;
  behind?: number;
  error?: string;
  started_at?: number;
  updated_at?: number;
  /** util.Build() — the sha the RUNNING binary was built from */
  build_sha?: string;
  /** when the availability check last ran, from UpdateAvail */
  checked_at?: number;
  /** this run replaced the binary + kickstarted the daemon — set from the
   *  `restarting` phase on, so a `failed` record with it true is a
   *  mac-app-phase failure that still owes the mandatory page reload */
  restarted?: boolean;
}

/** The phase constants in internal/jobs/shipios.go:36-43. Same caveat about
 *  `| string` as UpdatePhase above. */
export type ShipPhase =
  | "idle"
  | "preflight"
  | "generating"
  | "archiving"
  | "exporting"
  | "uploading"
  | "done"
  | "failed"
  | string;

/**
 * Mirrors api.webShipIOS (internal/api/shipios.go:109). Same blast-radius
 * reasoning as WebUpdate, and the same no-index-signature rule.
 */
export interface WebShipIOS {
  phase?: ShipPhase;
  /**
   * The git-count CFBundleVersion this run is uploading; 0 until the script
   * announces it. The only identifier tying what the board says to what shows
   * up in App Store Connect, which is why it rides the done label.
   */
  build?: number;
  error?: string;
  started_at?: number;
  updated_at?: number;
}

export interface WebBoard {
  daemon: DaemonStatus;
  daemon_ok: boolean;
  cell_cap: number;
  live_cells: number;
  jobs: WebJob[];
  /**
   * The current week's and today's goals. A current server ALWAYS sends this
   * key with both arrays present (never null), so the optionality here is the
   * same one `contract`/`update` carry: a server predating goals sends none,
   * and the panel renders empty rather than failing the decode. Read it as
   * `board.goals?.week ?? []`, never unconditionally.
   */
  goals?: WebGoals;
  /** absent on a server predating the handshake — the absence is the signal */
  contract?: number;
  stale?: boolean;
  update?: WebUpdate;
  ship_ios?: WebShipIOS;
}

// ---------------------------------------------------------------- usage

export interface WebUsageAccount {
  slug: string;
  current?: boolean;
  [k: string]: unknown;
}

export interface WebUsage {
  available: boolean;
  fetched_at?: number;
  age_seconds?: number;
  daemon_ok?: boolean;
  accounts?: WebUsageAccount[];
  rotation_mode?: "auto" | "manual" | string;
  rotation_set_by?: string;
  rotation_set_at?: string;
}

// ---------------------------------------------------------------- threads

export type ThreadBlockKind = "user" | "asst" | "think" | "tool" | string;

export interface ThreadBlock {
  kind: ThreadBlockKind;
  text?: string;
  inherited?: boolean;
  [k: string]: unknown;
}

export interface ThreadRead {
  blocks?: ThreadBlock[];
  counted?: number;
  live?: boolean;
  pane?: string;
  fork_uuid?: string;
  source?: string;
}

export type ThreadOp = "branch" | "kill" | "revive" | "rename" | "send";

// ---------------------------------------------------------------- job form

/**
 * The job form, GET and POST alike. `ready` and `prereqs` are GONE from both
 * shapes — the breaking change behind apiContract 6.
 *
 * Re-adding either here would be worse than a compile error: the save route
 * decodes with a plain json.Decode and no DisallowUnknownFields, so the server
 * IGNORES a key it does not know and answers 200 — a field that looks saved,
 * reads back empty, and reports nothing. The loud by-name refusal is on
 * `/api/job/draft` alone. So this type is the only thing standing between the
 * two shapes, and a key added to it has to be added to the Go struct in the
 * same commit.
 */
export interface JobForm {
  slug: string;
  repo?: string;
  base?: string;
  solo?: boolean;
  model?: string;
  spec?: string;
  mtime: number;
  editable?: boolean;
  err?: string;
  slugs?: string[];
  repos?: string[];
}

// ---------------------------------------------------------------- logs

export interface LogEntry {
  ts?: number;
  level?: string;
  src?: string;
  event?: string;
  msg?: string;
  [k: string]: unknown;
}

export interface LogPage {
  file: string;
  prev?: string;
  entries?: LogEntry[];
}

// ---------------------------------------------------------------- dream
//
// Two endpoints, two owners, and they are deliberately NOT merged into one
// payload: /api/dream is a filter over the artifact plumbing (a night report
// is an ordinary artifact of the dream-<date> job that wrote it) while
// /api/watchlist reads the `watchlist` table. The DREAM view fetches them
// independently so a failure on one pane cannot blank the other.

/** Mirrors api.webDreamReport (internal/api/dream.go). No omitempty on any
 *  field, so all three are always present. `body` is raw markdown, served
 *  inline rather than as a URL — the view renders every visible report. */
export interface WebDreamReport {
  /** YYYY-MM-DD, the dream job's slug with `dream-` stripped */
  date: string;
  slug: string;
  body: string;
}

/** GET /api/dream. Always an array, never null (dreamHandler). */
export interface DreamReports {
  reports: WebDreamReport[];
}

/** store/watchlist.go's `class` column, validated server-side by
 *  api.watchClasses. `| string` so an unknown future class decodes. */
export type WatchClass =
  | "feedback"
  | "correction"
  | "repetition"
  | "waste"
  | "prompting"
  | string;

/** api.watchStates. */
export type WatchState = "watching" | "shipped" | "declined" | string;

/**
 * api.watchVerdicts, minus the empty string: absence IS the unjudged state
 * here, since `verdict` carries omitempty on the Go side. "" is still what
 * the POST sends to CLEAR a verdict — see setWatchVerdict.
 */
export type WatchVerdict = "fine" | "fix" | string;

/**
 * One `watchlist` row — api.webWatchItem (internal/api/watchlist.go).
 *
 * The row has TWO owners and the wire carries both halves, because every
 * reader needs the agent's observation and carl's verdict together. The
 * optionality mirrors that split exactly: the agent's columns are on the
 * wire unconditionally (the counters especially — a zero `sightings` is a
 * measured zero), while `verdict`/`comment`/`commented_at` are omitempty
 * because an unjudged row genuinely has none of the three.
 *
 * Watchlist rows are GLOBAL: no job_id, no slug, no cascade.
 */
export interface WebWatchItem {
  id: number;
  observation: string;
  class: WatchClass;
  state: WatchState;
  sightings: number;
  /** unix MILLIS, the same convention WebJob.mtime uses */
  first_seen: number;
  last_seen: number;
  evidence?: string;
  suggested_fix?: string;
  pr_url?: string;
  updated_at: number;
  verdict?: WatchVerdict;
  comment?: string;
  /** RFC3339, stamped SERVER-side — never accepted from a client */
  commented_at?: string;
}

/** GET /api/watchlist[?state=…]. Always an array, never null. */
export interface WatchlistPage {
  watchlist: WebWatchItem[];
}

// ---------------------------------------------------------------- misc

export interface UploadResult {
  name: string;
  path: string;
  url: string;
}

export interface DraftResult {
  slug: string;
  text: string;
  updated_at?: number;
}

export interface CaptureResult {
  pane: string;
  content: string;
}

/**
 * `POST /api/job/rename`'s answer.
 *
 * `slug` is the SERVER's slugified name and is the only thing a client may
 * re-point to — the typed text is a request, never the result.
 *
 * `skipped` is omitted when empty, and every entry is the SOURCE path of a
 * preserved artifact copy that KEPT THE OLD NAME because something already
 * sat at the new one — never the destination that blocked it, which is the
 * file a reader must not act on. It is a note rather than a failure (the
 * rename itself committed), and it holds true name conflicts ONLY: a copy
 * whose rename failed outright is not a conflict and is reported in `notes`.
 *
 * `notes` is omitted when empty too, and is the other half of the same story:
 * the post-commit steps that FAILED, each carrying its cause — a preserved
 * copy whose rename failed outright (permissions, a full disk), a tmux
 * convergence step that did not take, a zeus notice that did not deliver.
 * None of them can fail a rename (they all run after the row has moved), which
 * is exactly why they need saying: there is no refusal to read them off, and
 * this response is the only surface where they are not merely a log line.
 *
 * The two lists are separate because the reader's move differs: a `skipped`
 * entry is a file to move or delete, a `notes` entry is something that broke.
 */
export interface RenameResult {
  ok: true;
  slug: string;
  skipped?: string[];
  notes?: string[];
}

/** Every non-2xx response body across the whole API. */
export interface WireError {
  error: string;
}
