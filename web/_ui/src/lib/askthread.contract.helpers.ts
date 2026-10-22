// askthread.contract.helpers.ts — fixtures for athena-logic's BLIND contract
// tests of the ask-threads port (askthread.contract.test.tsx). Authored from
// artifacts/50-contract.md's running example; every export is prefixed
// askContract. The network is stubbed at globalThis.fetch so the URLs and
// bodies the components send are asserted too.

import { vi } from "vitest";

import type { WebJob, WebJobOwner, WebThread } from "../api/types.ts";
import { makeJob } from "../test/fixtures.ts";

export const askContractSlug = "tech-comms-rollout";
export const askContractOwnerNode = "nOWNER";
export const askContractAskerNode = "nSTUDIO";
export const askContractQuestion = "why is CI red on the rollout PR?";
export const askContractTitle = "why is CI red";

export const askContractOwner: WebJobOwner = {
  node_id: askContractOwnerNode,
  label: "owner-mac",
  operator: "Owner",
  online: true,
};

/** The headless ask thread nSTUDIO opened on the owner's job. */
export function askContractAskThread(over: Partial<WebThread> = {}): WebThread {
  return {
    tid: "t3",
    title: askContractTitle,
    state: "live",
    created_at: 1,
    role: "ask",
    kind: "headless",
    origin: { node_id: askContractAskerNode, label: "studio-mini", login: "jess@probook.ai" },
    ...over,
  } as WebThread;
}

/** A pane thread (carl's ordinary side thread). */
export function askContractPaneThread(over: Partial<WebThread> = {}): WebThread {
  return {
    tid: "t1",
    title: "legacy pane thread",
    state: "live",
    pane: "%7",
    created_at: 1,
    role: "ask",
    kind: "pane",
    ...over,
  } as WebThread;
}

export function askContractJob(over: Partial<WebJob> = {}): WebJob {
  return makeJob({
    slug: askContractSlug,
    repo: "kraken",
    state: "working",
    live: true,
    threads: [askContractPaneThread(), askContractAskThread()],
    ...over,
  });
}

export type AskContractCall = { url: string; path: string; method: string; body: unknown };

export type AskContractReplies = {
  /** /api/thread/read and /api/nodes/thread/read */
  read?: Record<string, unknown>;
  /** POST /api/threads */
  threads?: Record<string, unknown>;
  /** POST /api/nodes/action */
  nodesAction?: Record<string, unknown>;
};

/** Routes by pathname; records every call. */
export function askContractStubFetch(replies: AskContractReplies = {}): AskContractCall[] {
  const calls: AskContractCall[] = [];
  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    let body: unknown = undefined;
    if (init?.body && typeof init.body === "string") {
      try {
        body = JSON.parse(init.body);
      } catch {
        body = init.body;
      }
    }
    const path = new URL(url, "http://kraken.test").pathname;
    calls.push({ url, path, method, body });
    let reply: unknown = { ok: true };
    if (path === "/api/thread/read" || path === "/api/nodes/thread/read") {
      reply = replies.read ?? { blocks: [], counted: 0, live: false, pane: "", fork_uuid: "", source: "none", session: "" };
    } else if (path === "/api/threads" && method === "POST") {
      reply = replies.threads ?? { ok: true, tid: "t3" };
    } else if (path === "/api/nodes/action") {
      reply = replies.nodesAction ?? { ok: true, tid: "t3" };
    } else if (path === "/api/goals") {
      reply = { goals: { week: [], day: [] } };
    }
    const text = JSON.stringify(reply);
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(reply),
      text: () => Promise.resolve(text),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

/** Present and neither it nor an ancestor carries `hidden`. */
export function askContractShown(id: string): boolean {
  const el = document.getElementById(id);
  return !!el && !el.closest("[hidden]");
}

/** The thread chip button whose label names `tid`. */
export function askContractChip(root: ParentNode, tid: string): Element | undefined {
  return Array.from(root.querySelectorAll(".thread-chip")).find((c) => (c.textContent ?? "").includes(`${tid} ·`) || (c.textContent ?? "").trim().endsWith(tid));
}

export const askContractMutate = async <T>(op: () => Promise<T>) => op();
