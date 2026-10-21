import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { threadOp } from "../api/client.ts";
import type { ThreadRead, WebJob, WebJobOwner, WebThread } from "../api/types.ts";
import { ThreadChips } from "../components/board/chips/ThreadChips.tsx";
import { SteerOverlay } from "../components/overlays/SteerOverlay.tsx";
import { ThreadOverlay } from "../components/overlays/ThreadOverlay.tsx";
import { makeJob } from "../test/fixtures.ts";
import {
  askEdgeCalls,
  askEdgeMutate,
  askEdgePosts,
  askEdgeSettle,
  askEdgeShown,
  askEdgeStorageClear,
  askEdgeStubFetch,
} from "./askthread.edge.helpers.ts";
import { threadComposerAllowed, threadOriginBadge } from "./threadquick.ts";

// askthread.edge.test.tsx — the EDGE dimension of slice D of
// artifacts/50-contract.md (ask threads ported into kraken):
// threadOriginBadge's null cases and fallbacks (kraken-core's "ask" kind is
// NOT headless here), threadComposerAllowed's refusals, the origin span only
// on foreign headless chips, the remote steer body's lone `⑂ ask` only on a
// live card with no threads, no control on a remote pane thread, the
// answering pill / note / origin pill boundaries, the remote ask dialog's
// failure path and its exact payload, no tmux button on a local headless
// row, the headless toggle's banner and stickiness and the `headless` it
// sends, threadOp carrying `headless` exactly when given, and the
// visibility dropdown posting once and only on a change.
//
// Written BLIND against the frozen contract text; no implementation file was
// read. Running example: owner nOWNER (owner-mac, Owner), asker nSTUDIO
// (studio-mini, jess@probook.ai), job tech-comms-rollout, thread t3.

const SLUG = "tech-comms-rollout";
const QUESTION = "why is CI red on the rollout PR?";
const STUDIO = { node_id: "nSTUDIO", label: "studio-mini", login: "jess@probook.ai" };
const OWNER = { node_id: "nOWNER", label: "owner-mac", login: "Owner" };
const OWNER_CARD: WebJobOwner = { node_id: "nOWNER", label: "owner-mac", operator: "Owner", online: true };

function askEdgeThread(over: Partial<WebThread> = {}): WebThread {
  return {
    tid: "t3",
    title: "ci red",
    state: "live",
    created_at: 0,
    role: "ask",
    kind: "headless",
    origin: STUDIO,
    ...over,
  } as WebThread;
}

function askEdgeRead(over: Partial<ThreadRead> = {}): ThreadRead {
  return { blocks: [], live: true, ...over } as ThreadRead;
}

beforeEach(() => {
  askEdgeStorageClear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// threadOriginBadge
// ---------------------------------------------------------------------------

describe("threadOriginBadge — null cases and fallbacks", () => {
  it("is null for a pane row even when it carries a foreign origin", () => {
    expect(threadOriginBadge(askEdgeThread({ kind: "pane" }), "nOWNER")).toBeNull();
    expect(threadOriginBadge(askEdgeThread({ kind: "pane" }), undefined)).toBeNull();
  });

  it("is null for a row with no kind (treated as pane) even with a foreign origin", () => {
    expect(threadOriginBadge(askEdgeThread({ kind: undefined }), "nOWNER")).toBeNull();
    expect(threadOriginBadge(askEdgeThread({ kind: undefined }), undefined)).toBeNull();
  });

  it("is null for kraken-core's old 'ask' kind and for case variants of headless", () => {
    for (const kind of ["ask", "Headless", "HEADLESS", "headless ", ""]) {
      expect(threadOriginBadge(askEdgeThread({ kind }), "nOWNER"), kind).toBeNull();
    }
  });

  it("is null for a headless row with no origin", () => {
    expect(threadOriginBadge(askEdgeThread({ origin: undefined }), "nOWNER")).toBeNull();
    expect(threadOriginBadge(askEdgeThread({ origin: undefined }), undefined)).toBeNull();
  });

  it("is null for a headless row opened by the viewing board's own node", () => {
    expect(threadOriginBadge(askEdgeThread({ origin: OWNER }), "nOWNER")).toBeNull();
    expect(threadOriginBadge(askEdgeThread({ origin: STUDIO }), "nSTUDIO")).toBeNull();
  });

  it("compares node ids exactly — a case variant is another node", () => {
    expect(threadOriginBadge(askEdgeThread({ origin: OWNER }), "nowner")).toBe("owner-mac");
  });

  it("shows the label for a foreign headless row, with an absent or empty board node id too", () => {
    expect(threadOriginBadge(askEdgeThread(), "nOWNER")).toBe("studio-mini");
    expect(threadOriginBadge(askEdgeThread(), undefined)).toBe("studio-mini");
    expect(threadOriginBadge(askEdgeThread(), "")).toBe("studio-mini");
  });

  it("falls back to the login, then the node id", () => {
    expect(threadOriginBadge(askEdgeThread({ origin: { ...STUDIO, label: "" } }), "nOWNER")).toBe("jess@probook.ai");
    expect(threadOriginBadge(askEdgeThread({ origin: { ...STUDIO, label: "", login: "" } }), "nOWNER")).toBe("nSTUDIO");
  });

  it("treats an empty origin node as the board's own when the board has no node id", () => {
    expect(threadOriginBadge(askEdgeThread({ origin: { ...OWNER, node_id: "" } }), undefined)).toBeNull();
    expect(threadOriginBadge(askEdgeThread({ origin: { ...OWNER, node_id: "" } }), "")).toBeNull();
  });

  it("an empty origin node is foreign to a board that has a node id", () => {
    expect(threadOriginBadge(askEdgeThread({ origin: { ...OWNER, node_id: "" } }), "nOWNER")).toBe("owner-mac");
  });
});

// ---------------------------------------------------------------------------
// threadComposerAllowed
// ---------------------------------------------------------------------------

describe("threadComposerAllowed — refusals", () => {
  it("remote: refuses every kind but headless, live or not — kraken-core's 'ask' included", () => {
    for (const kind of ["pane", undefined, "ask", "HEADLESS", "", "headlesss"]) {
      expect(threadComposerAllowed({ kind, state: "live" }, askEdgeRead({ live: true }), true), String(kind)).toBe(false);
    }
  });

  it("remote: refuses a headless thread whose read says not live", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askEdgeRead({ live: false }), true)).toBe(false);
  });

  it("remote: allows a live headless thread", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askEdgeRead({ live: true }), true)).toBe(true);
  });

  it("local: allows a live thread of either kind, refuses a dead one", () => {
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, askEdgeRead({ live: true }), false)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askEdgeRead({ live: true }), false)).toBe(true);
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, askEdgeRead({ live: false }), false)).toBe(false);
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askEdgeRead({ live: false }), false)).toBe(false);
  });

  it("with no read, falls back to the row's state", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, null, true)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "closed" }, null, true)).toBe(false);
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, null, false)).toBe(true);
    expect(threadComposerAllowed({ kind: "pane", state: "closed" }, null, false)).toBe(false);
  });

  it("a landed read wins over the row's state", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askEdgeRead({ live: false }), true)).toBe(false);
    expect(threadComposerAllowed({ kind: "headless", state: "closed" }, askEdgeRead({ live: true }), true)).toBe(true);
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, askEdgeRead({ live: false }), false)).toBe(false);
    expect(threadComposerAllowed({ kind: "pane", state: "closed" }, askEdgeRead({ live: true }), false)).toBe(true);
  });

  it("remote: a row with no kind is pane — never a composer", () => {
    expect(threadComposerAllowed({ state: "live" }, askEdgeRead({ live: true }), true)).toBe(false);
    expect(threadComposerAllowed({ state: "live" }, null, true)).toBe(false);
  });

  it("an unknown thread with no read: remote refuses (not headless), local falls back to true", () => {
    expect(threadComposerAllowed(undefined, null, true)).toBe(false);
    expect(threadComposerAllowed(undefined, null, false)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// ThreadChips origin span
// ---------------------------------------------------------------------------

describe("ThreadChips — the origin span only on foreign headless chips", () => {
  it("renders `from <badge>` inside exactly the foreign headless chips", () => {
    const job = makeJob({
      slug: SLUG,
      state: "working",
      live: true,
      threads: [
        { tid: "t1", title: "pane one", state: "live", pane: "%3", created_at: 0, role: "ask", kind: "pane", origin: STUDIO },
        { tid: "t2", title: "mine", state: "live", created_at: 0, role: "ask", kind: "headless", origin: OWNER },
        { tid: "t3", title: "ci red", state: "live", created_at: 0, role: "ask", kind: "headless", origin: STUDIO },
        { tid: "t4", title: "lint", state: "closed", created_at: 0, role: "ask", kind: "headless", origin: { ...STUDIO, label: "" } },
        { tid: "t5", title: "legacy", state: "live", pane: "%4", created_at: 0 },
        { tid: "t6", title: "mind", state: "live", pane: "%5", created_at: 0, role: "mind", kind: "pane", origin: STUDIO },
        { tid: "t7", title: "core-era", state: "live", created_at: 0, role: "ask", kind: "ask", origin: STUDIO },
      ] as WebThread[],
    });
    const props = { job, onOpenThread: vi.fn(), withBranch: false, boardNodeID: "nOWNER" };
    const { container } = render(<ThreadChips {...(props as unknown as Parameters<typeof ThreadChips>[0])} />);
    const spans = Array.from(container.querySelectorAll("span.thread-origin"));
    expect(spans.map((s) => s.textContent)).toEqual(["from studio-mini", "from jess@probook.ai"]);
    for (const s of spans) {
      expect(s.closest(".thread-chip") ?? s.closest("button")).not.toBeNull();
    }
  });

  it("renders no origin span anywhere without a foreign headless row", () => {
    const job = makeJob({
      slug: SLUG,
      state: "working",
      live: true,
      threads: [{ tid: "t1", title: "mine", state: "live", created_at: 0, role: "ask", kind: "headless", origin: { ...OWNER, node_id: "" } }] as WebThread[],
    });
    const props = { job, onOpenThread: vi.fn(), withBranch: false, boardNodeID: undefined };
    const { container } = render(<ThreadChips {...(props as unknown as Parameters<typeof ThreadChips>[0])} />);
    expect(container.querySelectorAll("span.thread-origin")).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Remote steer body — the threads row
// ---------------------------------------------------------------------------

function askEdgeRemoteCard(visibility: string, over: Partial<WebJob> = {}): WebJob {
  return makeJob({ slug: SLUG, state: "working", live: true, owner: OWNER_CARD, visibility, ...over });
}

function askEdgeMountRemoteSteer(job: WebJob) {
  const onOpenThread = vi.fn();
  render(
    <SteerOverlay
      open
      slug={job.slug}
      node="nOWNER"
      mode="steer"
      requireText={false}
      job={job}
      onClose={() => {}}
      mutate={askEdgeMutate}
      onOpenArtifact={vi.fn()}
      onOpenThread={onOpenThread}
      operator="jess"
    />,
  );
  return { onOpenThread };
}

describe("RemoteSteerBody — the ask entry point", () => {
  for (const vis of ["readonly", "public"]) {
    it(`a live ${vis} card with no threads renders #steer-ask with a lone ⑂ ask and no #steer-threads`, async () => {
      askEdgeStubFetch();
      const { onOpenThread } = askEdgeMountRemoteSteer(askEdgeRemoteCard(vis, { threads: [] }));
      await askEdgeSettle();
      expect(document.getElementById("steer-threads")).toBeNull();
      const ask = document.getElementById("steer-ask");
      expect(ask).not.toBeNull();
      expect(ask!.classList.contains("threads")).toBe(true);
      const buttons = ask!.querySelectorAll("button");
      expect(buttons).toHaveLength(1);
      expect(buttons[0]!.textContent).toContain("⑂ ask");
      fireEvent.click(buttons[0]!);
      expect(onOpenThread).toHaveBeenCalledWith(SLUG, null, "nOWNER");
    });
  }

  it("a live card with absent threads (no key) renders #steer-ask too", async () => {
    askEdgeStubFetch();
    askEdgeMountRemoteSteer(askEdgeRemoteCard("readonly", { threads: undefined }));
    await askEdgeSettle();
    expect(document.getElementById("steer-threads")).toBeNull();
    expect(document.getElementById("steer-ask")).not.toBeNull();
  });

  it("a dead card with no threads renders neither #steer-ask nor #steer-threads", async () => {
    askEdgeStubFetch();
    askEdgeMountRemoteSteer(askEdgeRemoteCard("readonly", { live: false, state: "done", threads: [] }));
    await askEdgeSettle();
    expect(document.getElementById("steer-ask")).toBeNull();
    expect(document.getElementById("steer-threads")).toBeNull();
  });

  it("a live card WITH threads offers ⑂ ask inside #steer-threads and no #steer-ask", async () => {
    askEdgeStubFetch();
    const { onOpenThread } = askEdgeMountRemoteSteer(
      askEdgeRemoteCard("readonly", {
        threads: [{ tid: "t3", title: "ci red", state: "live", created_at: 0, role: "ask", kind: "headless", origin: STUDIO }] as WebThread[],
      }),
    );
    await askEdgeSettle();
    expect(document.getElementById("steer-ask")).toBeNull();
    const row = document.getElementById("steer-threads")!;
    expect(row).not.toBeNull();
    const branch = within(row)
      .getAllByRole("button")
      .find((b) => (b.textContent ?? "").includes("⑂ ask"));
    expect(branch).toBeDefined();
    expect(row.textContent).not.toContain("⑂ branch");
    fireEvent.click(branch!);
    expect(onOpenThread).toHaveBeenCalledWith(SLUG, null, "nOWNER");
  });

  it("a dead card WITH threads keeps the chips but offers no ⑂ ask", async () => {
    askEdgeStubFetch();
    askEdgeMountRemoteSteer(
      askEdgeRemoteCard("readonly", {
        live: false,
        state: "done",
        threads: [{ tid: "t3", title: "ci red", state: "closed", created_at: 0, role: "ask", kind: "headless", origin: STUDIO }] as WebThread[],
      }),
    );
    await askEdgeSettle();
    const row = document.getElementById("steer-threads")!;
    expect(row).not.toBeNull();
    expect(row.textContent).not.toContain("⑂ ask");
    expect(document.getElementById("steer-ask")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// ThreadOverlay — remote thread view
// ---------------------------------------------------------------------------

const REMOTE_CONTROLS = ["thread-composer", "thread-text", "thread-send", "thread-kill", "thread-revive", "thread-tmux", "thread-models"];

function askEdgeRemoteMount(thread: WebThread, read: Record<string, unknown>, boardNodeID?: string) {
  const f = askEdgeStubFetch((u) =>
    u.pathname === "/api/nodes/thread/read"
      ? { status: 200, body: { blocks: [{ kind: "text", text: "thread says hello" }], ...read } }
      : undefined,
  );
  const props = {
    open: true,
    slug: SLUG,
    tid: thread.tid,
    job: askEdgeRemoteCard("readonly", { threads: [thread] }),
    node: "nOWNER",
    boardNodeID,
    onBranched: () => {},
    mutate: askEdgeMutate,
    onClose: () => {},
  };
  render(<ThreadOverlay {...(props as unknown as Parameters<typeof ThreadOverlay>[0])} />);
  return f;
}

describe("ThreadOverlay with node — boundaries", () => {
  it("offers no control at all on a live remote PANE thread", async () => {
    const f = askEdgeRemoteMount(
      { tid: "t1", title: "pane one", state: "live", pane: "%7", created_at: 0, role: "ask", kind: "pane" } as WebThread,
      { live: true, kind: "pane", answering: false, note: "" },
    );
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await askEdgeSettle();
    for (const id of REMOTE_CONTROLS) {
      expect(askEdgeShown(id), id).toBe(false);
    }
    expect(askEdgePosts(f)).toEqual([]);
  });

  it("offers no composer on a headless row whose read says not live", async () => {
    askEdgeRemoteMount(askEdgeThread(), { live: false, kind: "headless", answering: false, note: "" });
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-composer")).toBe(false);
  });

  it("shows the answering pill while the read says answering, with the composer disabled", async () => {
    askEdgeRemoteMount(askEdgeThread(), { live: true, kind: "headless", answering: true, note: "" });
    await waitFor(() => expect(askEdgeShown("thread-answering")).toBe(true));
    expect(document.getElementById("thread-answering")!.textContent).toContain("answering…");
    expect(askEdgeShown("thread-composer")).toBe(true);
    expect(document.getElementById("thread-send")).toBeDisabled();
  });

  it("shows no answering pill and no note when neither is set", async () => {
    askEdgeRemoteMount(askEdgeThread(), { live: true, kind: "headless", answering: false, note: "" });
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-answering")).toBe(false);
    expect(askEdgeShown("thread-note")).toBe(false);
    expect(document.getElementById("thread-send")).not.toBeDisabled();
  });

  it("shows the row's note when set", async () => {
    askEdgeRemoteMount(askEdgeThread({ note: "ask timed out after 5m0s" }), {
      live: true,
      kind: "headless",
      answering: false,
      note: "ask timed out after 5m0s",
    });
    await waitFor(() => expect(askEdgeShown("thread-note")).toBe(true));
    expect(document.getElementById("thread-note")!.textContent).toContain("ask timed out after 5m0s");
  });

  it("shows #thread-origin for a thread another node opened, login in its title", async () => {
    askEdgeRemoteMount(askEdgeThread(), { live: true, kind: "headless", answering: false, note: "", origin: STUDIO }, "nTHIRD");
    await waitFor(() => expect(askEdgeShown("thread-origin")).toBe(true));
    const pill = document.getElementById("thread-origin")!;
    expect(pill.textContent).toBe("from studio-mini");
    expect(pill.getAttribute("title")).toContain("jess@probook.ai");
  });

  it("shows no #thread-origin for this board's own thread", async () => {
    askEdgeRemoteMount(askEdgeThread(), { live: true, kind: "headless", answering: false, note: "", origin: STUDIO }, "nSTUDIO");
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-origin")).toBe(false);
  });

  it("shows no #thread-origin on a pane thread even with an origin", async () => {
    askEdgeRemoteMount(askEdgeThread({ kind: "pane", pane: "%7" }), { live: true, kind: "pane", answering: false, note: "", origin: STUDIO }, "nTHIRD");
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-origin")).toBe(false);
  });

  it("never shows kill, revive, tmux or a model picker on a remote headless thread", async () => {
    askEdgeRemoteMount(askEdgeThread(), { live: true, kind: "headless", answering: false, note: "" });
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await askEdgeSettle();
    for (const id of ["thread-kill", "thread-revive", "thread-tmux", "thread-models"]) {
      expect(askEdgeShown(id), id).toBe(false);
    }
  });
});

// ---------------------------------------------------------------------------
// ThreadOverlay — remote ask dialog (tid null)
// ---------------------------------------------------------------------------

function askEdgeRemoteDialog(route?: (u: URL, init?: RequestInit) => { status: number; body: unknown } | undefined) {
  const f = askEdgeStubFetch(route);
  const onBranched = vi.fn();
  const props = {
    open: true,
    slug: SLUG,
    tid: null,
    job: askEdgeRemoteCard("readonly", { threads: [] }),
    node: "nOWNER",
    boardNodeID: "nSTUDIO",
    onBranched,
    mutate: askEdgeMutate,
    onClose: () => {},
  };
  render(<ThreadOverlay {...(props as unknown as Parameters<typeof ThreadOverlay>[0])} />);
  return { f, onBranched };
}

describe("ThreadOverlay with node — the remote ask dialog", () => {
  it("offers no headless toggle and fetches nothing before branch", async () => {
    const { f } = askEdgeRemoteDialog();
    await askEdgeSettle();
    expect(document.getElementById("thread-new")).not.toBeNull();
    expect(document.getElementById("thread-headless")).toBeNull();
    expect(f).not.toHaveBeenCalled();
  });

  it("posts exactly {title, text, model} as the payload, model from the sticky picker", async () => {
    localStorage.setItem("kraken-thread-model", "haiku");
    const { f, onBranched } = askEdgeRemoteDialog((u, init) =>
      u.pathname === "/api/nodes/action" && init?.method?.toUpperCase() === "POST" ? { status: 200, body: { ok: true, tid: "t3" } } : undefined,
    );
    fireEvent.change(document.getElementById("thread-new-name")!, { target: { value: "ci red" } });
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: QUESTION } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(onBranched).toHaveBeenCalledWith("t3", "ci red"));
    const posts = askEdgePosts(f);
    expect(posts).toHaveLength(1);
    expect(posts[0]!.url.pathname).toBe("/api/nodes/action");
    expect(posts[0]!.body).toEqual({
      node: "nOWNER",
      slug: SLUG,
      action: "thread-ask",
      payload: { title: "ci red", text: QUESTION, model: "haiku" },
    });
  });

  it("calls onBranched with nothing when the owner refuses the ask (409 cap)", async () => {
    const { f, onBranched } = askEdgeRemoteDialog((u, init) =>
      u.pathname === "/api/nodes/action" && init?.method?.toUpperCase() === "POST"
        ? { status: 409, body: { error: "ask thread cap reached — close one first" } }
        : undefined,
    );
    fireEvent.change(document.getElementById("thread-new-name")!, { target: { value: "ci red" } });
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: QUESTION } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(askEdgePosts(f)).toHaveLength(1));
    await askEdgeSettle();
    expect(onBranched).not.toHaveBeenCalled();
  });

  it("calls onBranched with nothing when the reply carries no tid", async () => {
    const { f, onBranched } = askEdgeRemoteDialog((u, init) =>
      u.pathname === "/api/nodes/action" && init?.method?.toUpperCase() === "POST" ? { status: 200, body: { ok: true } } : undefined,
    );
    fireEvent.change(document.getElementById("thread-new-name")!, { target: { value: "ci red" } });
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: QUESTION } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(askEdgePosts(f)).toHaveLength(1));
    await askEdgeSettle();
    expect(onBranched).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// ThreadOverlay — local
// ---------------------------------------------------------------------------

function askEdgeLocalMount(thread: WebThread | null, read: Record<string, unknown> = {}, boardNodeID?: string) {
  const f = askEdgeStubFetch((u) =>
    u.pathname === "/api/thread/read"
      ? { status: 200, body: { blocks: [{ kind: "text", text: "local thread says" }], ...read } }
      : u.pathname === "/api/threads"
        ? { status: 200, body: { ok: true, tid: "t9" } }
        : undefined,
  );
  const job = makeJob({ slug: SLUG, state: "working", live: true, threads: thread ? [thread] : [] });
  const props = {
    open: true,
    slug: SLUG,
    tid: thread ? thread.tid : null,
    job,
    boardNodeID,
    onBranched: () => {},
    mutate: askEdgeMutate,
    onClose: () => {},
  };
  const utils = render(<ThreadOverlay {...(props as unknown as Parameters<typeof ThreadOverlay>[0])} />);
  return { f, ...utils };
}

describe("ThreadOverlay local — headless rows", () => {
  it("shows no tmux button on a live headless row (it has no pane), and a composer", async () => {
    askEdgeLocalMount(askEdgeThread(), { live: true, kind: "headless", answering: false, note: "" });
    await waitFor(() => expect(document.body.textContent).toMatch(/local thread says/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-tmux")).toBe(false);
    expect(askEdgeShown("thread-composer")).toBe(true);
  });

  it("disables the composer while the headless row is answering", async () => {
    askEdgeLocalMount(askEdgeThread(), { live: true, kind: "headless", answering: true, note: "" });
    await waitFor(() => expect(askEdgeShown("thread-answering")).toBe(true));
    expect(askEdgeShown("thread-composer")).toBe(true);
    expect(document.getElementById("thread-send")).toBeDisabled();
  });

  it("shows no tmux button and no composer on a closed headless row", async () => {
    askEdgeLocalMount(askEdgeThread({ state: "closed", closed_at: 1 }), { live: false, kind: "headless", answering: false, note: "" });
    await waitFor(() => expect(document.body.textContent).toMatch(/local thread says/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-tmux")).toBe(false);
    expect(askEdgeShown("thread-composer")).toBe(false);
  });

  it("shows #thread-origin on a local headless row another node opened, not on its own", async () => {
    const remote = askEdgeLocalMount(askEdgeThread({ origin: STUDIO }), { live: true, kind: "headless", answering: false, note: "" }, "nOWNER");
    await waitFor(() => expect(askEdgeShown("thread-origin")).toBe(true));
    expect(document.getElementById("thread-origin")!.textContent).toBe("from studio-mini");
    remote.unmount();

    askEdgeLocalMount(askEdgeThread({ origin: OWNER }), { live: true, kind: "headless", answering: false, note: "" }, "nOWNER");
    await waitFor(() => expect(document.body.textContent).toMatch(/local thread says/));
    await askEdgeSettle();
    expect(askEdgeShown("thread-origin")).toBe(false);
  });

  it("keeps the tmux button on a live PANE row of either role", async () => {
    for (const role of ["ask", "mind"]) {
      const m = askEdgeLocalMount(
        { tid: "t1", title: "pane one", state: "live", pane: "%7", created_at: 0, role, kind: "pane" } as WebThread,
        { live: true, kind: "pane", answering: false, note: "", pane: "%7" },
      );
      await waitFor(() => expect(document.body.textContent).toMatch(/local thread says/));
      await askEdgeSettle();
      expect(askEdgeShown("thread-tmux"), role).toBe(true);
      expect(askEdgeShown("thread-answering"), role).toBe(false);
      m.unmount();
    }
  });
});

describe("ThreadOverlay local — the headless toggle", () => {
  const HEADLESS = "headless: a read-only fork with no pane";
  const PANE = "pane: a full claude in a tmux window, as before";

  const box = () => document.getElementById("thread-headless") as HTMLInputElement;

  it("is a checkbox, checked with an empty localStorage", async () => {
    askEdgeLocalMount(null);
    await askEdgeSettle();
    expect(box()).not.toBeNull();
    expect(box().tagName).toBe("INPUT");
    expect(box().type).toBe("checkbox");
    expect(box().checked).toBe(true);
  });

  it("stores \"0\" when unchecked and \"1\" when checked again", async () => {
    askEdgeLocalMount(null);
    await askEdgeSettle();
    fireEvent.click(box());
    await askEdgeSettle();
    expect(box().checked).toBe(false);
    expect(localStorage.getItem("kraken-thread-headless")).toBe("0");
    fireEvent.click(box());
    await askEdgeSettle();
    expect(box().checked).toBe(true);
    expect(localStorage.getItem("kraken-thread-headless")).toBe("1");
  });

  it("reads a stored \"0\" as unchecked and \"1\" as checked, banner following", async () => {
    localStorage.setItem("kraken-thread-headless", "0");
    const first = askEdgeLocalMount(null);
    await askEdgeSettle();
    expect(box().checked).toBe(false);
    expect(document.body.textContent).toContain(PANE);
    expect(document.body.textContent).not.toContain(HEADLESS);
    first.unmount();

    localStorage.setItem("kraken-thread-headless", "1");
    askEdgeLocalMount(null);
    await askEdgeSettle();
    expect(box().checked).toBe(true);
    expect(document.body.textContent).toContain(HEADLESS);
    expect(document.body.textContent).not.toContain(PANE);
  });

  it("is sticky across a remount", async () => {
    const first = askEdgeLocalMount(null);
    await askEdgeSettle();
    fireEvent.click(box());
    await askEdgeSettle();
    expect(document.body.textContent).toContain(PANE);
    first.unmount();

    askEdgeLocalMount(null);
    await askEdgeSettle();
    expect(box().checked).toBe(false);
    expect(document.body.textContent).toContain(PANE);
  });

  it("fetches nothing and posts nothing just by opening the dialog", async () => {
    const { f } = askEdgeLocalMount(null);
    await askEdgeSettle();
    expect(askEdgePosts(f)).toEqual([]);
  });

  it("branch sends headless true by default and false once unchecked", async () => {
    for (const uncheck of [false, true]) {
      askEdgeStorageClear();
      const m = askEdgeLocalMount(null);
      await askEdgeSettle();
      if (uncheck) fireEvent.click(box());
      fireEvent.change(document.getElementById("thread-new-name")!, { target: { value: "ci red" } });
      fireEvent.change(document.getElementById("thread-seed")!, { target: { value: QUESTION } });
      fireEvent.click(document.getElementById("thread-branch")!);
      await waitFor(() => expect(askEdgePosts(m.f).length).toBeGreaterThan(0));
      const post = askEdgePosts(m.f).find((c) => c.url.pathname === "/api/threads");
      expect(post, `uncheck=${uncheck}`).toBeDefined();
      expect((post!.body as Record<string, unknown>).headless).toBe(!uncheck);
      m.unmount();
      vi.unstubAllGlobals();
    }
  });
});

// ---------------------------------------------------------------------------
// threadOp — headless exactly when given
// ---------------------------------------------------------------------------

describe("threadOp — the body carries headless exactly when given", () => {
  it("omits headless when not given, and role keeps riding", async () => {
    const f = askEdgeStubFetch();
    await threadOp(SLUG, "branch", { title: "ci red", text: QUESTION, role: "mind" } as Parameters<typeof threadOp>[2]);
    const post = askEdgePosts(f)[0];
    expect(post).toBeDefined();
    expect(post!.url.pathname).toBe("/api/threads");
    expect(Object.prototype.hasOwnProperty.call(post!.body, "headless")).toBe(false);
    expect((post!.body as Record<string, unknown>).role).toBe("mind");
  });

  it("sends false and true as given", async () => {
    for (const h of [false, true]) {
      const f = askEdgeStubFetch();
      await threadOp(SLUG, "branch", { title: "ci red", text: "", headless: h } as Parameters<typeof threadOp>[2]);
      const post = askEdgePosts(f)[0];
      expect(post).toBeDefined();
      expect((post?.body as Record<string, unknown> | undefined)?.headless).toBe(h);
      vi.unstubAllGlobals();
    }
  });
});

// ---------------------------------------------------------------------------
// VisibilityControl — a dropdown that posts once, only on a change
// ---------------------------------------------------------------------------

describe("VisibilityControl — the steer modal's dropdown", () => {
  function mountLocalSteer(visibility: string) {
    const f = askEdgeStubFetch();
    render(
      <SteerOverlay
        open
        slug={SLUG}
        mode="steer"
        requireText={false}
        job={makeJob({ slug: SLUG, state: "working", live: true, visibility })}
        onClose={() => {}}
        mutate={askEdgeMutate}
        onOpenArtifact={vi.fn()}
        onOpenThread={vi.fn()}
        operator="Owner"
      />,
    );
    return f;
  }
  const visPosts = (f: ReturnType<typeof askEdgeStubFetch>) =>
    askEdgeCalls(f).filter((c) => c.method === "POST" && c.url.pathname === "/api/job/visibility");

  it("is no radiogroup: no radio inputs, the control is the dropdown button", async () => {
    mountLocalSteer("readonly");
    await askEdgeSettle();
    const control = document.querySelector('[data-testid="visibility-control"]');
    expect(control).not.toBeNull();
    expect(control!.getAttribute("id")).toBe("steer-visibility");
    expect(document.querySelector('[role="radiogroup"]')).toBeNull();
    expect(document.querySelector('input[type="radio"]')).toBeNull();
  });

  it("re-picking the current value posts nothing; a new value posts exactly once", async () => {
    const f = mountLocalSteer("readonly");
    await askEdgeSettle();
    const control = () => document.querySelector('[data-testid="visibility-control"]') as HTMLElement;

    fireEvent.click(control());
    const same = (await screen.findAllByRole("option")).find((o) => /readonly/i.test(o.textContent ?? ""));
    expect(same).toBeDefined();
    fireEvent.click(same!);
    await askEdgeSettle();
    expect(visPosts(f)).toEqual([]);

    fireEvent.click(control());
    const pub = (await screen.findAllByRole("option")).find((o) => /public/i.test(o.textContent ?? ""));
    expect(pub).toBeDefined();
    fireEvent.click(pub!);
    await waitFor(() => expect(visPosts(f)).toHaveLength(1));
    await askEdgeSettle();
    expect(visPosts(f)).toHaveLength(1);
    expect((visPosts(f)[0]!.body as Record<string, unknown>).visibility).toBe("public");
  });

  it("offers exactly the three levels", async () => {
    mountLocalSteer("public");
    await askEdgeSettle();
    fireEvent.click(document.querySelector('[data-testid="visibility-control"]') as HTMLElement);
    // only the visibility menu's rows: a native <select> elsewhere in the
    // modal would also expose role=option
    const opts = (await screen.findAllByRole("option"))
      .map((o) => (o.textContent ?? "").toLowerCase())
      .filter((t) => /private|readonly|public/.test(t));
    expect(opts).toHaveLength(3);
    expect(opts.findIndex((t) => t.includes("private"))).toBe(0);
    expect(opts.findIndex((t) => t.includes("readonly"))).toBe(1);
    expect(opts.findIndex((t) => t.includes("public"))).toBe(2);
  });
});
