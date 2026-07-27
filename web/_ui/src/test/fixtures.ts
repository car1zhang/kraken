import type { DaemonStatus, WebBoard, WebJob } from "../api/types.ts";

// Shared test fixtures.
//
// These exist because four slices were built in parallel and each hand-rolled
// its own partial board/daemon literal. That was only possible while
// DaemonStatus and WebUpdate carried `[k: string]: unknown` index signatures —
// which is also how three REQUIRED wire fields (daemon.pid, update.build_sha,
// update.checked_at) went undeclared while the UI was reading them. Removing
// the index signatures fixed the contract and broke every partial literal at
// once, which is the type doing its job.
//
// Build fixtures from these factories rather than from object literals: a
// field added to the Go struct then lands in exactly one place here, and a
// field REMOVED from it fails this file loudly instead of silently leaving
// dead assertions scattered across a dozen tests.

export function makeDaemon(over: Partial<DaemonStatus> = {}): DaemonStatus {
  return {
    pid: 4242,
    started_at: 1_700_000_000,
    last_tick: 1_700_000_100,
    cells: 0,
    ...over,
  };
}

export function makeJob(over: Partial<WebJob> = {}): WebJob {
  return {
    slug: "demo",
    repo: "kraken",
    state: "draft",
    live: false,
    mtime: 1_700_000_000_000,
    ...over,
  };
}

export function makeBoard(over: Partial<WebBoard> = {}): WebBoard {
  return {
    daemon: makeDaemon(),
    daemon_ok: true,
    cell_cap: 15,
    live_cells: 0,
    jobs: [],
    ...over,
  };
}
