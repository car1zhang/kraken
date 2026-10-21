// askthread.contract.test.tsx — athena-logic's BLIND contract tests for the
// ask-threads port (kraken-core PR #5), Slice D (SPA): threadOriginBadge,
// threadComposerAllowed, threadOp's headless field, ThreadChips' origin badge,
// BranchButton's label, the remote steer modal's "⑂ ask", the remote
// new-thread dialog posting thread-ask, the remote composer posting
// thread-send, the local dialog's headless toggle and headless-row view, and
// the steer modal's visibility dropdown.
//
// Authored from artifacts/50-contract.md alone; only pre-existing TEST files
// were read for conventions. LOGIC dimension. Every module-level identifier
// is prefixed askContract. Meant to sit at web/_ui/src/lib/.

import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ThreadRead } from "../api/types.ts";
import { threadOp } from "../api/client.ts";
import { threadComposerAllowed, threadOriginBadge } from "./threadquick.ts";
import { ThreadChips } from "../components/board/chips/ThreadChips.tsx";
import { BranchButton } from "../components/board/chips/BranchButton.tsx";
import { ThreadOverlay } from "../components/overlays/ThreadOverlay.tsx";
import { SteerOverlay } from "../components/overlays/SteerOverlay.tsx";
import {
  askContractAskThread,
  askContractChip,
  askContractJob,
  askContractMutate,
  askContractOwner,
  askContractOwnerNode,
  askContractPaneThread,
  askContractQuestion,
  askContractShown,
  askContractSlug,
  askContractStubFetch,
  askContractTitle,
  type AskContractCall,
} from "./askthread.contract.helpers.ts";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AskContractLoose = any;

let askContractCalls: AskContractCall[] = [];

beforeEach(() => {
  vi.unstubAllGlobals();
  try {
    localStorage.clear();
  } catch {
    /* no storage */
  }
  askContractCalls = askContractStubFetch();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const askContractRead = (over: Record<string, unknown> = {}): ThreadRead =>
  ({
    blocks: [],
    counted: 0,
    live: true,
    pane: "",
    fork_uuid: "",
    source: "none",
    session: "s",
    kind: "headless",
    answering: false,
    note: "",
    ...over,
  }) as AskContractLoose;

const askContractText = (id: string) => (document.getElementById(id)?.textContent ?? "").replace(/\s+/g, " ").trim();

// ---------------------------------------------------------------------------
// threadOriginBadge
// ---------------------------------------------------------------------------

describe("threadOriginBadge", () => {
  // "null for a pane row, for a row with no kind (treated as pane), for a row
  // with no origin, and for a headless row where (origin.node_id ?? "") ===
  // (boardNodeID ?? ""); else origin.label (falling back to origin.login,
  // then origin.node_id)"
  it("is the origin label for another node's headless row", () => {
    expect(threadOriginBadge(askContractAskThread(), askContractOwnerNode)).toBe("studio-mini");
    expect(threadOriginBadge(askContractAskThread(), undefined)).toBe("studio-mini");
  });

  it("is null for this board's own headless row, a pane row and an origin-less row", () => {
    expect(threadOriginBadge(askContractAskThread(), "nSTUDIO")).toBeNull();
    expect(
      threadOriginBadge(
        askContractPaneThread({ origin: { node_id: "nSTUDIO", label: "studio-mini", login: "jess@probook.ai" } }),
        askContractOwnerNode,
      ),
    ).toBeNull();
    expect(threadOriginBadge(askContractAskThread({ origin: undefined }), askContractOwnerNode)).toBeNull();
  });

  it("treats a kind-less row as pane and compares empty node ids as equal", () => {
    expect(threadOriginBadge(askContractAskThread({ kind: undefined }), askContractOwnerNode)).toBeNull();
    expect(
      threadOriginBadge(askContractAskThread({ origin: { node_id: "", label: "owner-mac", login: "Owner" } }), undefined),
    ).toBeNull();
    expect(
      threadOriginBadge(askContractAskThread({ origin: { node_id: "", label: "owner-mac", login: "Owner" } }), askContractOwnerNode),
    ).toBe("owner-mac");
  });

  it("does not badge a mind-role or ask-role pane row by role alone", () => {
    expect(
      threadOriginBadge(
        askContractPaneThread({ role: "mind", origin: { node_id: "nSTUDIO", label: "studio-mini", login: "j" } } as AskContractLoose),
        askContractOwnerNode,
      ),
    ).toBeNull();
  });

  it("falls back to the login, then the node id", () => {
    expect(
      threadOriginBadge(askContractAskThread({ origin: { node_id: "nSTUDIO", label: "", login: "jess@probook.ai" } }), "nOWNER"),
    ).toBe("jess@probook.ai");
    expect(threadOriginBadge(askContractAskThread({ origin: { node_id: "nSTUDIO", label: "", login: "" } }), "nOWNER")).toBe(
      "nSTUDIO",
    );
  });
});

// ---------------------------------------------------------------------------
// threadComposerAllowed
// ---------------------------------------------------------------------------

describe("threadComposerAllowed", () => {
  // "live is read.live when a read landed, else t.state === "live", else
  // true; remote: true iff t.kind === "headless" and live; local: live (pane
  // or headless)"
  it("remote: only a live headless thread", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askContractRead({ live: true }), true)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "closed" }, askContractRead({ live: false }), true)).toBe(false);
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, askContractRead({ live: true, kind: "pane" }), true)).toBe(false);
  });

  it("the read's live outranks the row's state", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "closed" }, askContractRead({ live: true }), true)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askContractRead({ live: false }), true)).toBe(false);
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, askContractRead({ live: false, kind: "pane" }), false)).toBe(false);
  });

  it("remote with no read yet: the row's state decides", () => {
    expect(threadComposerAllowed(askContractAskThread({ state: "live" }), null, true)).toBe(true);
    expect(threadComposerAllowed(askContractAskThread({ state: "closed" }), null, true)).toBe(false);
    expect(threadComposerAllowed(askContractPaneThread({ state: "live" }), null, true)).toBe(false);
  });

  it("local: any live thread, pane or headless", () => {
    expect(threadComposerAllowed({ kind: "pane", state: "live" }, askContractRead({ live: true, kind: "pane" }), false)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, askContractRead({ live: true }), false)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "closed" }, askContractRead({ live: false }), false)).toBe(false);
    expect(threadComposerAllowed({ kind: "pane", state: "closed" }, askContractRead({ live: false, kind: "pane" }), false)).toBe(false);
  });

  it("with no row and no read, live is true; a kind-less row is a pane", () => {
    expect(threadComposerAllowed({ kind: "headless", state: "live" }, null, false)).toBe(true);
    expect(threadComposerAllowed({ kind: "headless", state: "closed" }, null, false)).toBe(false);
    expect(threadComposerAllowed(undefined, null, false)).toBe(true);
    expect(threadComposerAllowed(undefined, null, true)).toBe(false);
    expect(threadComposerAllowed({ kind: undefined, state: "live" } as AskContractLoose, askContractRead({ live: true }), true)).toBe(false);
    expect(threadComposerAllowed({ kind: undefined, state: "live" } as AskContractLoose, askContractRead({ live: true }), false)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// threadOp headless
// ---------------------------------------------------------------------------

describe("threadOp branch headless", () => {
  // "the body carries headless exactly when given (absent otherwise); role
  // keeps working"
  it("sends headless only when given, beside role", async () => {
    await (threadOp as AskContractLoose)(askContractSlug, "branch", { title: "a", text: "q", headless: false });
    await (threadOp as AskContractLoose)(askContractSlug, "branch", { title: "a", text: "q", headless: true });
    await (threadOp as AskContractLoose)(askContractSlug, "branch", { title: "a", text: "q" });
    await (threadOp as AskContractLoose)(askContractSlug, "branch", { title: "a", text: "q", role: "mind", headless: false });
    const bodies = askContractCalls.filter((c) => c.path === "/api/threads").map((c) => c.body as Record<string, unknown>);
    expect(bodies).toHaveLength(4);
    expect(bodies[0]!.headless).toBe(false);
    expect(bodies[1]!.headless).toBe(true);
    expect("headless" in bodies[2]!).toBe(false);
    expect(bodies[3]!.role).toBe("mind");
    expect(bodies[3]!.headless).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// ThreadChips and BranchButton
// ---------------------------------------------------------------------------

describe("ThreadChips origin badge", () => {
  // "every chip of a headless row whose threadOriginBadge is non-null renders
  // <span class="thread-origin">from <badge></span> inside the chip; a pane
  // chip renders no such span"
  it("badges another node's headless chip and no other", () => {
    const job = askContractJob({
      threads: [
        askContractPaneThread(),
        askContractAskThread(),
        askContractAskThread({ tid: "t4", title: "mine", origin: { node_id: askContractOwnerNode, label: "owner-mac", login: "Owner" } }),
        askContractPaneThread({ tid: "t5", title: "mind", role: "mind" } as AskContractLoose),
      ],
    });
    const props: AskContractLoose = { job, onOpenThread: vi.fn(), boardNodeID: askContractOwnerNode, withBranch: false };
    const { container } = render(<ThreadChips {...props} />);
    const foreign = askContractChip(container, "t3")!.querySelector("span.thread-origin");
    expect(foreign).not.toBeNull();
    expect(foreign!.textContent).toBe("from studio-mini");
    expect(askContractChip(container, "t1")!.querySelector(".thread-origin")).toBeNull();
    expect(askContractChip(container, "t4")!.querySelector(".thread-origin")).toBeNull();
    expect(askContractChip(container, "t5")!.querySelector(".thread-origin")).toBeNull();
    expect(container.querySelectorAll(".thread-origin")).toHaveLength(1);
  });
});

describe("BranchButton label", () => {
  // "prop added label?: string (default "⑂ branch")"
  it("defaults to ⑂ branch and takes a label", () => {
    const job = askContractJob();
    const a = render(<BranchButton job={job} onOpenThread={vi.fn()} />);
    expect(a.container.querySelector("button")!.textContent!.trim()).toBe("⑂ branch");
    a.unmount();
    const props: AskContractLoose = { job, onOpenThread: vi.fn(), label: "⑂ ask" };
    const b = render(<BranchButton {...props} />);
    expect(b.container.querySelector("button")!.textContent!.trim()).toBe("⑂ ask");
  });
});

// ---------------------------------------------------------------------------
// the remote steer modal
// ---------------------------------------------------------------------------

describe("RemoteSteerBody ⑂ ask", () => {
  function askContractSteer(visibility: string, jobOver: Record<string, unknown> = {}) {
    const onOpenThread = vi.fn();
    const props: AskContractLoose = {
      open: true,
      slug: askContractSlug,
      node: askContractOwnerNode,
      boardNodeID: "nSTUDIO",
      mode: "steer",
      job: askContractJob({ owner: askContractOwner, visibility, ...jobOver } as AskContractLoose),
      onClose: () => {},
      mutate: askContractMutate,
      onOpenArtifact: vi.fn(),
      onOpenThread,
      operator: "Jess",
    };
    render(<SteerOverlay {...props} />);
    return onOpenThread;
  }

  // "the threads row mounts ThreadChips with withBranch={!!job.live} and
  // boardNodeID, with the branch label "⑂ ask", on readonly AND public;
  // clicking it calls onOpenThread(slug, null, node)"
  for (const visibility of ["readonly", "public"]) {
    it(`offers ⑂ ask on a live ${visibility} card and opens the remote dialog`, () => {
      const onOpenThread = askContractSteer(visibility);
      const section = document.getElementById("steer-threads");
      expect(section).not.toBeNull();
      const ask = Array.from(section!.querySelectorAll("button")).find((b) => b.textContent!.trim() === "⑂ ask");
      expect(ask, "⑂ ask button").toBeDefined();
      fireEvent.click(ask!);
      expect(onOpenThread).toHaveBeenCalledWith(askContractSlug, null, askContractOwnerNode);
    });
  }

  // the chips there carry boardNodeID: the asker's own thread is unbadged, a
  // third node's is badged
  it("badges the threads row against the board's node id", () => {
    askContractSteer("readonly", {
      threads: [
        askContractAskThread(),
        askContractAskThread({ tid: "t6", title: "attic", origin: { node_id: "nATTIC", label: "attic-mac", login: "sam@probook.ai" } }),
      ],
    });
    const section = document.getElementById("steer-threads")!;
    expect(askContractChip(section, "t3")!.querySelector(".thread-origin")).toBeNull();
    expect(askContractChip(section, "t6")!.querySelector(".thread-origin")?.textContent).toBe("from attic-mac");
  });

  // "when a live remote card has NO threads it renders a sibling <div
  // class="threads" id="steer-ask"> holding a lone ⑂ ask BranchButton"
  it("offers ⑂ ask in #steer-ask on a live card with no threads", () => {
    const onOpenThread = askContractSteer("readonly", { threads: [] });
    const box = document.getElementById("steer-ask");
    expect(box).not.toBeNull();
    expect(box!.tagName).toBe("DIV");
    expect(box!.classList.contains("threads")).toBe(true);
    const buttons = Array.from(box!.querySelectorAll("button"));
    expect(buttons).toHaveLength(1);
    expect(buttons[0]!.textContent!.trim()).toBe("⑂ ask");
    fireEvent.click(buttons[0]!);
    expect(onOpenThread).toHaveBeenCalledWith(askContractSlug, null, askContractOwnerNode);
  });
});

// ---------------------------------------------------------------------------
// ThreadOverlay — remote
// ---------------------------------------------------------------------------

function askContractOverlay(
  tid: string | null,
  node: string | undefined,
  jobOver: Record<string, unknown> = {},
  boardNodeID?: string,
) {
  const onBranched = vi.fn();
  const props: AskContractLoose = {
    open: true,
    slug: askContractSlug,
    tid,
    node,
    boardNodeID,
    job: askContractJob(node ? { owner: askContractOwner, visibility: "readonly", ...jobOver } : jobOver),
    onClose: () => {},
    onBranched,
    mutate: askContractMutate,
    operator: node ? "Jess" : "Owner",
  };
  const utils = render(<ThreadOverlay {...props} />);
  return { ...utils, onBranched };
}

describe("ThreadOverlay remote new-thread dialog", () => {
  // "with node and tid === null renders the new-thread dialog in REMOTE mode
  // (#thread-new present, a banner …, the seed textarea, name, the sticky
  // model picker, #thread-branch labelled "⑂ ask <node label> ⌘⏎"); … it
  // fetches nothing before branch"
  it("renders the dialog with the banner and fetches nothing", async () => {
    askContractOverlay(null, askContractOwnerNode);
    expect(askContractShown("thread-new")).toBe(true);
    expect(document.getElementById("thread-seed")).not.toBeNull();
    expect(document.getElementById("thread-new-name")).not.toBeNull();
    expect(askContractText("thread-branch")).toBe("⑂ ask owner-mac ⌘⏎");
    const banner =
      "Opens a headless fork of zeus' conversation on owner-mac, the owner's Mac, on that cell's account. " +
      "Opened from here it has no tools: it answers from what zeus already knows, and every reply is readable on every board. " +
      "Owner sees who asked.";
    expect(askContractText("thread-new")).toContain(banner);
    expect(askContractShown("thread-headless")).toBe(false);
    await new Promise((r) => setTimeout(r, 50));
    expect(askContractCalls).toEqual([]);
  });

  // "branch posts postNodeAction(node, slug, "thread-ask", {title, text,
  // model}) and on res.tid calls onBranched(tid, title)"; model default opus
  it("branch posts thread-ask through the proxy and hands back the tid", async () => {
    const { onBranched } = askContractOverlay(null, askContractOwnerNode);
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: askContractQuestion } });
    fireEvent.change(document.getElementById("thread-new-name")!, { target: { value: askContractTitle } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(onBranched).toHaveBeenCalledWith("t3", askContractTitle));
    const posts = askContractCalls.filter((c) => c.method === "POST");
    expect(posts).toHaveLength(1);
    expect(posts[0]!.path).toBe("/api/nodes/action");
    expect(posts[0]!.body).toEqual({
      node: askContractOwnerNode,
      slug: askContractSlug,
      action: "thread-ask",
      payload: { title: askContractTitle, text: askContractQuestion, model: "opus" },
    });
  });

  // "the sticky model picker (kraken-thread-model, default opus)"
  it("posts the sticky model", async () => {
    localStorage.setItem("kraken-thread-model", "haiku");
    const { onBranched } = askContractOverlay(null, askContractOwnerNode);
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: askContractQuestion } });
    fireEvent.change(document.getElementById("thread-new-name")!, { target: { value: askContractTitle } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(onBranched).toHaveBeenCalled());
    const post = askContractCalls.find((c) => c.method === "POST")!;
    expect((post.body as AskContractLoose).payload.model).toBe("haiku");
  });
});

describe("ThreadOverlay remote thread view", () => {
  // "a composer #thread-composer with #thread-text and #thread-send exactly
  // when threadComposerAllowed(thread, read, true); send posts
  // postNodeAction(node, slug, "thread-send", {tid, text}) and clears the box;
  // kill/revive/rename/tmux/model controls stay absent"
  it("a live headless thread gets a composer that posts thread-send", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true }) as AskContractLoose });
    askContractOverlay("t3", askContractOwnerNode);
    await waitFor(() => expect(askContractShown("thread-composer")).toBe(true));
    await waitFor(() => expect((document.getElementById("thread-text") as HTMLTextAreaElement).disabled).toBe(false));
    for (const id of ["thread-kill", "thread-revive", "thread-tmux", "thread-name-input", "thread-models"]) {
      expect(askContractShown(id), id).toBe(false);
    }
    expect(askContractCalls.some((c) => c.path === "/api/nodes/thread/read")).toBe(true);
    fireEvent.change(document.getElementById("thread-text")!, { target: { value: "and the flake on step 4?" } });
    fireEvent.click(document.getElementById("thread-send")!);
    await waitFor(() => expect(askContractCalls.some((c) => c.method === "POST")).toBe(true));
    const post = askContractCalls.find((c) => c.method === "POST")!;
    expect(post.path).toBe("/api/nodes/action");
    expect(post.body).toEqual({
      node: askContractOwnerNode,
      slug: askContractSlug,
      action: "thread-send",
      payload: { tid: "t3", text: "and the flake on step 4?" },
    });
    await waitFor(() => expect((document.getElementById("thread-text") as HTMLTextAreaElement).value).toBe(""));
  });

  it("a pane thread gets no composer", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true, kind: "pane", pane: "%7" }) as AskContractLoose });
    askContractOverlay("t1", askContractOwnerNode);
    await waitFor(() => expect(askContractCalls.some((c) => c.path === "/api/nodes/thread/read")).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(askContractShown("thread-composer")).toBe(false);
    expect(askContractShown("thread-send")).toBe(false);
  });

  // "#thread-answering pill (answering…) while read.answering, #thread-note
  // with the row's note when set"; the composer is "disabled while answering"
  it("shows the answering pill and the note, with the composer disabled", async () => {
    const note = "ask failed: exit status 1";
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true, answering: true, note }) as AskContractLoose });
    askContractOverlay("t3", askContractOwnerNode, {
      threads: [askContractPaneThread(), askContractAskThread({ answering: true, note })],
    });
    await waitFor(() => expect(askContractShown("thread-answering")).toBe(true));
    expect(askContractText("thread-answering")).toBe("answering…");
    await waitFor(() => expect(askContractText("thread-note")).toContain(note));
    expect(askContractShown("thread-composer")).toBe(true);
    expect((document.getElementById("thread-text") as HTMLTextAreaElement).disabled).toBe(true);
    expect((document.getElementById("thread-send") as HTMLButtonElement).disabled).toBe(true);
  });

  // "head gains #thread-origin (text `from <badge>`, login in title, when the
  // badge is non-null)"
  it("shows no origin pill on the asker's own thread, and still a composer", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true }) as AskContractLoose });
    askContractOverlay("t3", askContractOwnerNode, {}, "nSTUDIO");
    await waitFor(() => expect(askContractShown("thread-composer")).toBe(true));
    expect(askContractShown("thread-origin")).toBe(false);
    expect(askContractShown("thread-note")).toBe(false);
    expect(askContractShown("thread-answering")).toBe(false);
  });

  it("shows the origin pill on another node's headless thread", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true }) as AskContractLoose });
    askContractOverlay(
      "t5",
      askContractOwnerNode,
      {
        threads: [askContractAskThread({ tid: "t5", origin: { node_id: "nATTIC", label: "attic-mac", login: "sam@probook.ai" } })],
      },
      "nSTUDIO",
    );
    await waitFor(() => expect(askContractShown("thread-origin")).toBe(true));
    const pill = document.getElementById("thread-origin")!;
    expect(pill.textContent!.trim()).toBe("from attic-mac");
    expect(pill.getAttribute("title") ?? "").toContain("sam@probook.ai");
    await waitFor(() => expect(askContractShown("thread-composer")).toBe(true));
  });
});

// ---------------------------------------------------------------------------
// ThreadOverlay — local
// ---------------------------------------------------------------------------

// "#thread-headless (an <input type="checkbox">, default checked, sticky
// under localStorage key kraken-thread-headless as "1"/"0")"
function askContractHeadlessOn(): boolean {
  const el = document.getElementById("thread-headless") as HTMLInputElement | null;
  if (!el) throw new Error("#thread-headless missing");
  expect(el.tagName).toBe("INPUT");
  expect(el.type).toBe("checkbox");
  return el.checked;
}

describe("ThreadOverlay local new-thread dialog", () => {
  // "a banner that reads `headless: a read-only fork with no pane` when
  // checked and `pane: a full claude in a tmux window, as before` when not"
  it("defaults to headless and says so", () => {
    askContractOverlay(null, undefined);
    expect(askContractHeadlessOn()).toBe(true);
    expect(askContractText("thread-new")).toContain("headless: a read-only fork with no pane");
    expect(askContractText("thread-new")).not.toContain("pane: a full claude in a tmux window, as before");
  });

  it("toggling flips the banner, sticks across mounts, and is posted", async () => {
    const first = askContractOverlay(null, undefined);
    fireEvent.click(document.getElementById("thread-headless")!);
    expect(askContractHeadlessOn()).toBe(false);
    expect(askContractText("thread-new")).toContain("pane: a full claude in a tmux window, as before");
    expect(askContractText("thread-new")).not.toContain("headless: a read-only fork with no pane");
    expect(localStorage.getItem("kraken-thread-headless")).toBe("0");
    first.unmount();

    askContractOverlay(null, undefined);
    expect(askContractHeadlessOn()).toBe(false);
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: askContractQuestion } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(askContractCalls.some((c) => c.path === "/api/threads" && c.method === "POST")).toBe(true));
    const body = askContractCalls.find((c) => c.path === "/api/threads" && c.method === "POST")!.body as Record<string, unknown>;
    expect(body.op).toBe("branch");
    expect(body.headless).toBe(false);
  });

  it("reads a stored \"0\" as unchecked and writes \"1\" when checked", () => {
    localStorage.setItem("kraken-thread-headless", "0");
    const a = askContractOverlay(null, undefined);
    expect(askContractHeadlessOn()).toBe(false);
    fireEvent.click(document.getElementById("thread-headless")!);
    expect(localStorage.getItem("kraken-thread-headless")).toBe("1");
    a.unmount();
    askContractOverlay(null, undefined);
    expect(askContractHeadlessOn()).toBe(true);
  });

  it("a default branch posts headless true with the question", async () => {
    askContractOverlay(null, undefined);
    fireEvent.change(document.getElementById("thread-seed")!, { target: { value: askContractQuestion } });
    fireEvent.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(askContractCalls.some((c) => c.path === "/api/threads" && c.method === "POST")).toBe(true));
    const body = askContractCalls.find((c) => c.path === "/api/threads" && c.method === "POST")!.body as Record<string, unknown>;
    expect(body.op).toBe("branch");
    expect(body.headless).toBe(true);
    expect(body.text).toBe(askContractQuestion);
    expect(askContractCalls.some((c) => c.path === "/api/nodes/action")).toBe(false);
  });
});

describe("ThreadOverlay local headless-row view", () => {
  // "a headless row's view shows #thread-origin when non-null,
  // #thread-answering while answering, #thread-note when set, NO #thread-tmux
  // button, and #thread-composer per threadComposerAllowed(thread, read,
  // false) (disabled while answering)"
  it("no tmux button, the pill and the note, and a disabled composer while answering", async () => {
    const note = "answer interrupted — kraken serve restarted";
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true, answering: true, note }) as AskContractLoose });
    askContractOverlay("t3", undefined, {
      threads: [askContractPaneThread(), askContractAskThread({ answering: true, note })],
    });
    await waitFor(() => expect(askContractShown("thread-composer")).toBe(true));
    expect(askContractShown("thread-tmux")).toBe(false);
    await waitFor(() => expect(askContractShown("thread-answering")).toBe(true));
    await waitFor(() => expect(askContractText("thread-note")).toContain(note));
    await waitFor(() => expect((document.getElementById("thread-text") as HTMLTextAreaElement).disabled).toBe(true));
    expect((document.getElementById("thread-send") as HTMLButtonElement).disabled).toBe(true);
  });

  it("no tmux button, and a composer posting the local send once the answer landed", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true, answering: false }) as AskContractLoose });
    askContractOverlay("t3", undefined, {
      threads: [askContractPaneThread(), askContractAskThread({ answering: false })],
    });
    await waitFor(() => expect(askContractShown("thread-composer")).toBe(true));
    expect(askContractShown("thread-tmux")).toBe(false);
    expect(askContractShown("thread-answering")).toBe(false);
    await waitFor(() => expect((document.getElementById("thread-text") as HTMLTextAreaElement).disabled).toBe(false));

    fireEvent.change(document.getElementById("thread-text")!, { target: { value: "and the flake?" } });
    fireEvent.click(document.getElementById("thread-send")!);
    await waitFor(() => expect(askContractCalls.some((c) => c.path === "/api/threads" && c.method === "POST")).toBe(true));
    const body = askContractCalls.find((c) => c.path === "/api/threads" && c.method === "POST")!.body as Record<string, unknown>;
    expect(body).toMatchObject({ slug: askContractSlug, op: "send", tid: "t3", text: "and the flake?" });
    expect(askContractCalls.some((c) => c.path === "/api/nodes/action")).toBe(false);
  });

  it("a live pane row keeps its tmux button and shows no origin, pill or note", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true, kind: "pane", pane: "%7" }) as AskContractLoose });
    askContractOverlay("t1", undefined, {}, askContractOwnerNode);
    await waitFor(() => expect(askContractShown("thread-tmux")).toBe(true));
    expect(askContractShown("thread-origin")).toBe(false);
    expect(askContractShown("thread-note")).toBe(false);
    expect(askContractShown("thread-answering")).toBe(false);
  });

  it("a headless row opened from another node shows its origin pill", async () => {
    askContractCalls = askContractStubFetch({ read: askContractRead({ live: true }) as AskContractLoose });
    askContractOverlay("t3", undefined, {}, askContractOwnerNode);
    await waitFor(() => expect(askContractShown("thread-origin")).toBe(true));
    const pill = document.getElementById("thread-origin")!;
    expect(pill.textContent!.trim()).toBe("from studio-mini");
    expect(pill.getAttribute("title") ?? "").toContain("jess@probook.ai");
  });
});

// ---------------------------------------------------------------------------
// VisibilityControl — the steer modal's dropdown
// ---------------------------------------------------------------------------

describe("VisibilityControl dropdown", () => {
  // "a dropdown in the job-colour dropdown's exact skin (FieldSelect, the
  // .colorsel* classes) with options private, readonly, public …;
  // data-testid="visibility-control" and the id steer-visibility stay on the
  // interactive control; picking a value posts setJobVisibility(slug, v) once
  // and only when it changed"
  function askContractLocalSteer() {
    const props: AskContractLoose = {
      open: true,
      slug: askContractSlug,
      mode: "steer",
      job: askContractJob({ visibility: "readonly", threads: [] } as AskContractLoose),
      onClose: () => {},
      mutate: askContractMutate,
      onOpenArtifact: vi.fn(),
      onOpenThread: vi.fn(),
      operator: "Owner",
    };
    render(<SteerOverlay {...props} />);
  }

  const askContractVisPosts = () => askContractCalls.filter((c) => c.path === "/api/job/visibility" && c.method === "POST");

  function askContractOption(label: string): HTMLElement | undefined {
    return Array.from(document.querySelectorAll<HTMLElement>('span.colorsel-row[role="option"]')).find(
      (r) => (r.textContent ?? "").trim().startsWith(label),
    );
  }

  it("is a colorsel dropdown, not a radiogroup, offering the three levels", () => {
    askContractLocalSteer();
    const ctl = document.getElementById("steer-visibility")!;
    expect(ctl).not.toBeNull();
    expect(ctl.getAttribute("data-testid")).toBe("visibility-control");
    expect(ctl.matches("button.colorsel-btn")).toBe(true);
    expect(document.querySelector('[role="radiogroup"]')).toBeNull();
    fireEvent.click(ctl);
    for (const v of ["private", "readonly", "public"]) {
      expect(askContractOption(v), v).toBeDefined();
    }
  });

  it("posts setJobVisibility once on a change, and nothing for the current value", async () => {
    askContractLocalSteer();
    const ctl = document.getElementById("steer-visibility")!;
    fireEvent.click(ctl);
    fireEvent.click(askContractOption("readonly")!);
    await new Promise((r) => setTimeout(r, 50));
    expect(askContractVisPosts()).toHaveLength(0);

    fireEvent.click(document.getElementById("steer-visibility")!);
    fireEvent.click(askContractOption("public")!);
    await waitFor(() => expect(askContractVisPosts()).toHaveLength(1));
    expect(askContractVisPosts()[0]!.body).toEqual({ slug: askContractSlug, visibility: "public" });
  });
});
