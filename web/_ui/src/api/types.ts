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
}

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
