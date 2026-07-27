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
  JobForm,
  LogPage,
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

export const queueJob = slugAction("/api/queue");
export const dequeueJob = slugAction("/api/dequeue");
export const doneJob = slugAction("/api/done");
export const startJob = slugAction("/api/start");
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

export const threadOp = (
  slug: string,
  op: ThreadOp,
  extra: { tid?: string; title?: string; text?: string } = {},
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
