// One typed function per endpoint.
//
// Paths are wire-pinned: internal/api/register.go's own comment says the iOS
// app and this board address them by these exact strings. Nothing here may
// invent, rename, or version a path.
//
// Mutations are PESSIMISTIC, matching the vanilla board's post() exactly: the
// caller awaits, then invalidates and refetches on BOTH success and failure.
// There is no client-side retry and no backoff anywhere — this is localhost
// (or a tailnet hop), the server memoizes /api/board for 2s, and a failed
// call's text belongs in the tick-stat corner rather than in a retry loop.

import type {
  CaptureResult,
  DraftResult,
  DreamReports,
  GoalsPage,
  JobForm,
  KronosStatus,
  LogPage,
  OrchInbox,
  PeersRead,
  RenameResult,
  ThreadOp,
  ThreadRead,
  UploadResult,
  WatchlistPage,
  WatchState,
  WatchVerdict,
  WebBoard,
  WebUsage,
} from "./types.ts";

/**
 * Same origin in production (the bundle is served off the same mux as /api).
 * In dev, vite proxies /api to 127.0.0.1:7777, so this is still "".
 */
const BASE = "";

/** Thrown for any non-2xx; `message` is the server's own `{error}` string. */
export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

async function parseError(res: Response): Promise<never> {
  let msg = `${res.status} ${res.statusText}`;
  try {
    const body = (await res.json()) as { error?: string };
    if (body && typeof body.error === "string" && body.error) msg = body.error;
  } catch {
    // a non-JSON error body (a proxy, a panic) is not itself an error worth
    // surfacing over the status line we already have
  }
  throw new ApiError(res.status, msg);
}

export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(BASE + path, { signal });
  if (!res.ok) return parseError(res);
  return (await res.json()) as T;
}

export async function postJSON<T = { ok: true }>(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const res = await fetch(BASE + path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
    signal,
  });
  if (!res.ok) return parseError(res);
  return (await res.json()) as T;
}

// ------------------------------------------------------------ reads

export const fetchBoard = (signal?: AbortSignal) =>
  getJSON<WebBoard>("/api/board", signal);

export const fetchUsage = (signal?: AbortSignal) =>
  getJSON<WebUsage>("/api/usage", signal);

export const fetchDraft = (slug: string, signal?: AbortSignal) =>
  getJSON<DraftResult>(`/api/draft?slug=${encodeURIComponent(slug)}`, signal);

export const fetchJobForm = (slug: string, signal?: AbortSignal) =>
  getJSON<JobForm>(`/api/job/form?slug=${encodeURIComponent(slug)}`, signal);

export const fetchThreadRead = (
  slug: string,
  tid: string,
  n = 50,
  signal?: AbortSignal,
) =>
  getJSON<ThreadRead>(
    `/api/thread/read?slug=${encodeURIComponent(slug)}&tid=${encodeURIComponent(tid)}&n=${n}`,
    signal,
  );

export const capturePane = (pane: string, signal?: AbortSignal) =>
  getJSON<CaptureResult>(
    `/api/peers/capture?pane=${encodeURIComponent(pane)}`,
    signal,
  );

/** `file` is "events" or "events:<id>"; omit for the newest page. */
export const fetchLog = (file?: string, signal?: AbortSignal) =>
  getJSON<LogPage>(
    file ? `/api/logs?file=${encodeURIComponent(file)}` : "/api/logs",
    signal,
  );

/** Every night report, newest first. */
export const fetchDream = (signal?: AbortSignal) =>
  getJSON<DreamReports>("/api/dream", signal);

/**
 * The watchlist. An empty `state` means EVERY row — the filter is a
 * narrowing, not a required argument — but the DREAM view always asks for
 * "watching", since shipped and declined rows are finished business.
 */
export const fetchWatchlist = (
  state: WatchState = "watching",
  signal?: AbortSignal,
) =>
  getJSON<WatchlistPage>(
    state ? `/api/watchlist?state=${encodeURIComponent(state)}` : "/api/watchlist",
    signal,
  );

/**
 * Carl's verdict on one watchlist row — the ONLY inbound channel the dream
 * loop has, read first by the next run.
 *
 * The comment rides along with the verdict rather than saving separately:
 * one click, no second control. An empty verdict AND an empty comment is the
 * documented undo, clearing commented_at with them. Every agent-owned key is
 * absent by construction; the server refuses one by name if it appears.
 */
export const setWatchVerdict = (
  id: number,
  verdict: WatchVerdict | "",
  comment: string,
) => postJSON("/api/watchlist", { id, verdict, comment });

/**
 * The current week's and today's goals, standalone.
 *
 * The BOARD payload already carries the same two lists, and every surface that
 * has the board reads them from there — this exists for the one surface that
 * does not: the editor's goal picker, which is handed a single `WebJob` and no
 * payload, and needs the pickable list. With a `slug` the server also answers
 * `job_goal` for that job's linked goal whatever its period; the picker does
 * not ask for it (it reads `job.goal`, the same fact off the board), so the
 * argument stays off this signature until something needs it.
 */
export const fetchGoals = (signal?: AbortSignal) =>
  getJSON<GoalsPage>("/api/goals", signal);

// ------------------------------------------------------------ kronos
//
// Three reads and one write, all pre-existing routes except /api/kronos and
// /api/orch/inbox. The conversation and the composer deliberately go through
// the generic PEERS routes rather than a kronos-specific pair: kronos is one
// claude in one tmux pane, and `k r`/`k s` already address exactly that.

/** kronos' liveness, pane, and last context report. */
export const fetchKronos = (signal?: AbortSignal) =>
  getJSON<KronosStatus>("/api/kronos", signal);

/**
 * The tail of one pane's transcript. `n` counts NON-tool blocks and the server
 * clamps it to 50, so asking for more re-reads a large transcript tail on every
 * tick for blocks nobody scrolled to — the same reason ThreadOverlay leaves the
 * thread read's `n` at the server default.
 */
export const fetchPaneRead = (pane: string, n = 30, signal?: AbortSignal) =>
  getJSON<PeersRead>(
    `/api/peers/read?pane=${encodeURIComponent(pane)}&n=${n}`,
    signal,
  );

/**
 * Type a message into a pane. This is `k s`: the server re-verifies the pane
 * still holds the expected agent before it sends, so a stale pane id from a
 * poll two ticks ago is refused rather than delivered to whatever took the
 * pane over.
 */
export const sendToPane = (pane: string, message: string) =>
  postJSON("/api/peers/send", { pane, message });

/** Unacked escalations, oldest first. */
export const fetchOrchInbox = (signal?: AbortSignal) =>
  getJSON<OrchInbox>("/api/orch/inbox", signal);

export const fetchUpdateStatus = (signal?: AbortSignal) =>
  getJSON<import("./types.ts").WebUpdate>("/api/update/status", signal);

export const fetchShipStatus = (signal?: AbortSignal) =>
  getJSON<import("./types.ts").WebShipIOS>("/api/ship-ios/status", signal);

// ------------------------------------------------- job state transitions
//
// Every one of these shares the {ok:true} / {error:string} convention, which
// is why contract_test.go covers them generically rather than one by one.

const slugAction = (path: string) => (slug: string) =>
  postJSON(path, { slug });

/**
 * SUBMIT. The URL is the old queue one and the name follows it, but the
 * request is no longer a parking write: the server cuts the worktree and
 * spawns the cell before it answers, so this call is measured in seconds and
 * refuses with a 409 whose body carries the reason. `/api/dequeue` and
 * `/api/start` are gone from the server — nothing here may post them.
 */
export const queueJob = slugAction("/api/queue");
export const doneJob = slugAction("/api/done");
export const stopJob = slugAction("/api/stop");
export const retryJob = slugAction("/api/retry");
export const killJob = slugAction("/api/kill");
export const archiveJob = slugAction("/api/archive");
export const unarchiveJob = slugAction("/api/unarchive");
export const deleteJob = slugAction("/api/delete");

export const steerJob = (slug: string, text: string) =>
  postJSON("/api/steer", { slug, text });

/** `text` is empty for a plain done→blocked reactivate. */
export const reactivateJob = (slug: string, text = "") =>
  postJSON("/api/reactivate", { slug, text });

/** `text` is the 1-based option index as a string, per the wire contract. */
export const answerQuestion = (slug: string, optionIndex: number) =>
  postJSON("/api/question", { slug, text: String(optionIndex) });

export const answerDocQuestion = (slug: string, id: string, text: string) =>
  postJSON("/api/doc-question", { slug, id, text });

export const reorderColumn = (column: string, slugs: string[]) =>
  postJSON("/api/reorder", { column, slugs });

export const saveDraft = (slug: string, text: string) =>
  postJSON("/api/draft", { slug, text });

// ------------------------------------------------------------ feedback
//
// One endpoint, four request shapes. Kept as four named functions because a
// single union-typed one reads as "anything goes" at the call site.

export const createFeedback = (
  slug: string,
  home: string,
  name: string,
  feedback: string,
) => postJSON("/api/feedback", { slug, home, name, feedback });

export const editFeedback = (slug: string, id: number, feedback: string) =>
  postJSON("/api/feedback", { slug, id, feedback });

export const deleteFeedback = (slug: string, id: number) =>
  postJSON("/api/feedback", { slug, id, delete: true });

// ------------------------------------------------------------ threads

/**
 * `model` is read by the BRANCH op alone — every other op ignores it, so a
 * rename or a send never restates (or accidentally rewrites) the thread's
 * model. It carries a THREAD_MODELS alias, and the server 400s anything else by
 * name, which is why the picker is the only thing that sets it.
 */
export const threadOp = (
  slug: string,
  op: ThreadOp,
  extra: { tid?: string; title?: string; text?: string; model?: string } = {},
) => postJSON<{ ok: true; tid?: string }>("/api/threads", { slug, op, ...extra });

// ------------------------------------------------------------ job editor

export const createJob = (slug: string) =>
  postJSON<JobForm>("/api/job/create", { slug });

/**
 * mtime 0 deliberately opts out of the optimistic lock: the editor autosaves
 * on a 600ms debounce and a conflict dialog mid-typing is worse than
 * last-writer-wins on a single-user board.
 */
export const saveJobForm = (form: Partial<JobForm> & { slug: string }) =>
  postJSON<{ ok: boolean; mtime: number; warn?: string }>(
    "/api/job/saveform",
    form,
  );

// ------------------------------------------------------------ host actions

export const openTmux = (target: { pane?: string; slug?: string }) =>
  postJSON<{ ok: boolean; note?: string; focus_error?: string }>(
    "/api/peers/open",
    target,
  );

/**
 * Raise a pane and reduce the response to the one line a note slot shows:
 * a raise that worked but could not focus the terminal still surfaces its
 * note, and a clean raise surfaces nothing. Shared by both kronos surfaces
 * (the full view's OPEN TMUX and the dock's ⧉) so the contract cannot drift
 * between two hand-copied versions of the same eight lines. Throws are the
 * caller's — the server's own sentence belongs beside whichever button was
 * pressed.
 */
export const raisePane = async (pane: string): Promise<string> => {
  const r = await openTmux({ pane });
  return r.focus_error || r.note || "";
};

/**
 * Open a code ref in the editor on THIS machine — the click end of
 * lib/codeRefs.ts.
 *
 * `slug` is what resolves the checkout: the server looks the job up, prefers
 * its live worktree, and falls back to the primary checkout with a `note` when
 * the worktree has been torn down. The path stays relative for exactly that
 * reason — the board cannot know which root it will land under.
 *
 * `archived` picks WHICH namespace that lookup runs in, and it is not optional
 * decoration. An archived job and a live one may carry the same slug (see
 * WebJob.archived — the board deliberately lists both), and the server resolves
 * a live job first, so an unflagged call from an archived job's artifact opens
 * the WRONG checkout without saying so. It is sent only when true because
 * `archived: false` and "not archived" are the same statement, and the wire
 * carries the shorter one.
 *
 * `line` and `col` are omitted from the body rather than sent as 0 when the ref
 * carries neither, because 0 and "no line given" are the same value on the wire
 * and the omission is the honest one.
 *
 * Deliberately NOT routed through the shared mutation path: this changes
 * nothing on the board, so there is nothing to invalidate, and its two
 * interesting outcomes (a note about a torn-down worktree, an ambiguous
 * basename) are messages for the reader rather than status-bar failures.
 */
export const openInEditor = (
  slug: string,
  path: string,
  line?: number,
  col?: number,
  archived?: boolean,
) =>
  postJSON<{ ok?: boolean; note?: string; error?: string }>(
    "/api/editor/open",
    {
      slug,
      path,
      ...(line === undefined ? {} : { line }),
      ...(col === undefined ? {} : { col }),
      ...(archived ? { archived: true } : {}),
    },
  );

export const switchAccount = (slug: string) =>
  postJSON<{ from: string; to: string; mode: string }>("/api/account/switch", {
    slug,
  });

export const setRotationMode = (mode: "auto" | "manual") =>
  postJSON<{ mode: string }>("/api/account/mode", { mode });

/**
 * Set (or clear) a job's colour. `""` clears it.
 *
 * The colour is written to the job file's `color` field, and the daemon then
 * types the SAME colour into the cell's live tmux pane on a later pass
 * (internal/jobs/color.go) — so this one POST is why a board swatch and a
 * pane's prompt bar end up the same pixel colour. The server validates the
 * name against jobs.ValidColor and 400s an unknown one.
 */
export const setJobColor = (slug: string, color: string) =>
  postJSON("/api/job/color", { slug, color });

// ------------------------------------------------------------ goals

/**
 * Every goal WRITE — add, update, remove — through one route and one function.
 *
 * `unknown` rather than a union of the three op shapes, deliberately: the
 * server is the validator (it 400s with a sentence the status corner shows
 * verbatim), and a hand-written union here would be a second, weaker copy of
 * that validation which drifts the moment an op grows a field. The call sites
 * in GoalsPanel spell each op out as an object literal, which is where the
 * shapes are readable.
 *
 * Bodies: `{op:"add", goal:{scope, period?, text, parent_id?}}` — an absent
 * period means the CURRENT one for that scope; `{op:"update", id, text?,
 * done?, parent_id?}` — an absent field is unchanged and an explicit
 * `parent_id: 0` clears it; `{op:"remove", id}`.
 *
 * Goes through the board `mutate` ritual at every call site: a goal write
 * moves the board payload's `goals` key, so the panel's next truth is the
 * refetch rather than local state.
 */
export const postGoals = (body: unknown) => postJSON("/api/goals", body);

/**
 * Link a job to a goal, or unlink it with `goalId` 0.
 *
 * Its own one-field route rather than a field on the job form, for the colour
 * route's exact reason: `saveform` refuses a job whose cell is live — which is
 * when carl is most likely to be filing a running job under a goal — and a
 * full-replace save from a form that predates the field would wipe the link by
 * omitting it. The server also leaves the job's `rev` and `updated_at` alone,
 * so filing a job under a goal is not an edit to the job.
 */
export const postJobGoal = (slug: string, goalId: number) =>
  postJSON("/api/job/goal", { slug, goal_id: goalId });

/**
 * Rename a job. `slug` is its CURRENT name and `to` is the free text carl
 * typed — the server slugifies it and answers with the name it actually
 * stored, which is what every caller re-points to.
 *
 * Deliberately NOT routed through the shared mutation path, for `createJob`'s
 * reason: a refusal here is the whole message (taken, reserved prefix, an
 * archived job's name, another job's former name) and it belongs beside the
 * input that caused it, where `mutate` would funnel it into the clock corner
 * and hand the caller a bare null.
 */
export const renameJob = (slug: string, to: string) =>
  postJSON<RenameResult>("/api/job/rename", { slug, to });

// ------------------------------------------------- update / ship
//
// These two deliberately do NOT go through the shared mutation path: their
// refusals are paragraphs (a ship in flight, a dirty tree, a wrong branch),
// and the shared path funnels failure text into the clock corner where a
// paragraph does not fit. Callers render these errors in the banner instead.

export const requestUpdate = (opts: { force?: boolean; test?: boolean } = {}) =>
  postJSON<{ ok: boolean; phase: string; from_sha?: string }>(
    "/api/update",
    opts,
  );

export const requestShipIOS = () =>
  postJSON<{ ok: boolean; phase: string }>("/api/ship-ios", {});

// ------------------------------------------------------------ uploads

export async function uploadImage(
  slug: string,
  file: File,
): Promise<UploadResult> {
  const fd = new FormData();
  fd.append("slug", slug);
  fd.append("file", file);
  const res = await fetch(BASE + "/api/upload", { method: "POST", body: fd });
  if (!res.ok) return parseError(res);
  return (await res.json()) as UploadResult;
}
