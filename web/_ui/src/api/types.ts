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

export interface WebArtifact {
  name: string;
  url: string;
}

export interface WebPrereq {
  job: string;
  gated: boolean;
  done: boolean;
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

export type JobState =
  | "draft"
  | "queued"
  | "working"
  | "blocked"
  | "done"
  | "archived";

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
  prereqs?: WebPrereq[];
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
   * Why the daemon's last attempt to start this job's cell failed. Set only
   * while the job is QUEUED — a running job has nothing to explain, and a
   * stale reason on a live card would read as a current fault.
   *
   * Before this existed the failure was invisible: it went to ~/.kraken/logs
   * and to daemon.last_error, which is one string for the whole tick rather
   * than per job. A job once sat queued through 24 identical failed starts
   * over nine minutes with nothing on screen.
   */
  start_error?: string;
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
 * The phase constants in internal/jobs/update.go:23-31, plus `uptodate`
 * (jobs.UpdateUpToDate, read at internal/api/update.go:151).
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

export interface JobFormPrereq {
  job: string;
  gated?: boolean;
}

export interface JobForm {
  slug: string;
  repo?: string;
  base?: string;
  ready?: boolean;
  solo?: boolean;
  model?: string;
  prereqs?: JobFormPrereq[];
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

/** Every non-2xx response body across the whole API. */
export interface WireError {
  error: string;
}
