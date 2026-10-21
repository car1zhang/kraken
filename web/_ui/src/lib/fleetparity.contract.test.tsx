// fleetparity.contract.test.tsx — athena-logic's BLIND contract tests for the
// multiplayer-kraken job's Round 2 ("remote parity"), Slice R2-W, component
// rendering. Authored from artifacts/50-contract.md alone; no implementation
// file was read. LOGIC dimension: what each component RENDERS when the data
// is present — readonly: every panel, zero controls; public: the panels plus
// round 1's controls. Failed loads and odd inputs are athena-edge's.
//
// The network is stubbed at globalThis.fetch (not by mocking api/client.ts),
// so the client URLs the components hit are asserted too. Every module-level
// identifier is prefixed contractFP.

import { render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebJob, WebJobOwner } from "../api/types.ts";
import { makeJob } from "../test/fixtures.ts";
import { RemoteSteerBody } from "../components/overlays/RemoteSteer.tsx";
import { SteerOverlay, ZeusTranscriptPanel } from "../components/overlays/SteerOverlay.tsx";
import { DocQuestions } from "../components/overlays/DocQuestions.tsx";
import { FeedbackPanel } from "../components/board/chips/FeedbackPanel.tsx";
import { ThreadOverlay } from "../components/overlays/ThreadOverlay.tsx";
import { ArtifactOverlay } from "../components/overlays/ArtifactOverlay.tsx";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type ContractFPLoose = any;

const contractFPMutate = async <T,>(op: () => Promise<T>) => op();

const contractFPOwner: WebJobOwner = {
  node_id: "nSTUDIO77",
  label: "studio-mini",
  operator: "Jess",
  online: true,
};

const contractFPRaw = {
  slug: "tech-comms",
  content: "---\njob: tech-comms\nrepo: kraken\n---\n\n# Spec heading\n\ncontractfp spec body\n",
  mtime: 1,
  editable: true,
};

const contractFPTranscript = {
  session: "s1",
  blocks: [{ kind: "asst", text: "contractfp transcript line", uuid: "u1" }],
};

const contractFPThreadRead = {
  blocks: [
    { kind: "user", text: "contractfp why the retry", uuid: "u1" },
    { kind: "asst", text: "contractfp because the lock", uuid: "a2" },
  ],
  counted: 2,
  live: false,
  pane: "",
  fork_uuid: "",
  source: "",
  session: "s9",
};

/** When set, the thread read replies with this instead (reset before each test). */
let contractFPThreadReadOver: unknown = null;

type ContractFPCall = { url: string; method: string; body: unknown };

/** Routes by pathname; records every call. */
function contractFPStubFetch(): ContractFPCall[] {
  const calls: ContractFPCall[] = [];
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
    calls.push({ url, method, body });
    const path = new URL(url, "http://kraken.test").pathname;
    let reply: unknown = { ok: true };
    if (path === "/api/nodes/job/raw" || path === "/api/job/raw") reply = contractFPRaw;
    else if (path === "/api/nodes/transcript" || path === "/api/job/transcript") reply = contractFPTranscript;
    else if (path === "/api/nodes/thread/read" || path === "/api/thread/read")
      reply = contractFPThreadReadOver ?? contractFPThreadRead;
    else if (path === "/api/nodes/action" && (body as { action?: string } | undefined)?.action === "thread-ask")
      reply = { ok: true, tid: "t3" };
    else if (path === "/api/goals") reply = { goals: { week: [], day: [] } };
    const text = typeof reply === "string" ? reply : JSON.stringify(reply);
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

/** A remote job carrying every read panel's data. */
function contractFPJob(visibility: string, over: Partial<WebJob> = {}): WebJob {
  return makeJob({
    slug: "tech-comms",
    repo: "kraken",
    state: "blocked",
    live: true,
    zeus_status: "waiting",
    detail: "contractfp detail text",
    last_sent: "ship it",
    roster: [
      { name: "hephaestus-1", model: "opus", role: "hephaestus", status: "run", working: true, kind: "teammate" },
    ],
    prs: [{ url: "https://github.com/o/r/pull/73", state: "open" }],
    threads: [{ tid: "t1", title: "contractfp-thread-title", state: "live", created_at: 1 }],
    load: { unblock: 2, scope: 8, note: "one decision", at: 1_700_000_000_000 },
    artifacts: [
      { name: "00-overview.md", url: "/api/nodes/artifact?node=nSTUDIO77&slug=tech-comms&name=00-overview.md" },
      { name: "10-arch.html", url: "/api/nodes/artifact?node=nSTUDIO77&slug=tech-comms&name=10-arch.html" },
    ],
    doc_questions: [
      { id: "Q1", text: "contractfp which store?", importance: "P0", context: "contractfp ctx", recommendation: "contractfp rec" },
      { id: "Q2", text: "contractfp which port?", importance: "P1", answer: "contractfp 7777" },
    ],
    feedback: [{ id: 11, artifact: "10-arch.html", note: "contractfp arrow points wrong", at: 0 }],
    tokens: { in: 10, out: 5, cache_write: 1, cache_read: 2, sessions: 1, updated_at: 1 } as ContractFPLoose,
    question: {
      tool_use_id: "tu_1",
      questions: [
        {
          question: "contractfp approach?",
          header: "approach",
          multi_select: false,
          options: [{ label: "A", description: "do A" }],
        },
      ],
    },
    owner: contractFPOwner,
    visibility,
    ...over,
  } as Partial<WebJob>);
}

function contractFPSteer(job: WebJob | undefined, extra: Record<string, unknown> = {}) {
  const onOpenArtifact = vi.fn();
  const onOpenThread = vi.fn();
  const props: ContractFPLoose = {
    open: true,
    slug: "tech-comms",
    // AMENDED mount seam (join run 1): SteerOverlay renders RemoteSteerBody
    // iff props.slug && props.node — the job's owner plays no part.
    node: contractFPOwner.node_id,
    mode: "steer",
    job,
    onClose: () => {},
    mutate: contractFPMutate,
    onOpenArtifact,
    onOpenThread,
    operator: "Carl",
    ...extra,
  };
  const utils = render(<SteerOverlay {...props} />);
  return { ...utils, onOpenArtifact, onOpenThread };
}

const contractFPControlIDs = [
  "steer-swatches",
  "steer-stop",
  "steer-markdone",
  "steer-kill",
  "steer-submit",
  "steer-archive",
  "steer-edit",
  "steer-text",
  "steer-send",
];

const contractFPContextOrder = [
  "#steer-crew",
  "#steer-prs",
  "#steer-threads",
  "#steer-detail",
  "#steer-lastsent",
  "#steer-transcript",
  '[data-testid="load-drill"]',
  "#steer-chips",
  "#steer-docq",
  "#steer-feedback",
  "#steer-tokens",
];

function contractFPMindAttrs(root: ParentNode): string[] {
  const out: string[] = [];
  for (const el of root.querySelectorAll("*")) {
    for (const a of el.getAttributeNames()) {
      if (a.startsWith("data-mind-")) out.push(`${el.tagName.toLowerCase()}#${el.id}[${a}]`);
    }
  }
  return out;
}

function contractFPFollows(a: Element, b: Element): boolean {
  return (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
}

let contractFPCalls: ContractFPCall[] = [];

beforeEach(() => {
  vi.unstubAllGlobals();
  contractFPThreadReadOver = null;
  contractFPCalls = contractFPStubFetch();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("RemoteSteerBody — module", () => {
  // `RemoteSteerBody` — head: "export function RemoteSteerBody(props:
  // RemoteSteerBodyProps)".
  it("is exported from RemoteSteer.tsx", () => {
    expect(typeof RemoteSteerBody).toBe("function");
  });

  // "job === undefined renders the "no longer on the board" note and nothing
  // else, and makes NO request (no spec load, no transcript poll)" (AMENDED).
  it("renders the no-longer-on-the-board note for an undefined job", async () => {
    const props: ContractFPLoose = {
      node: contractFPOwner.node_id,
      slug: "tech-comms",
      job: undefined,
      mutate: contractFPMutate,
      onClose: () => {},
      onOpenArtifact: () => {},
      onOpenThread: () => {},
    };
    const { container } = render(<RemoteSteerBody {...props} />);
    expect(container.textContent).toContain("no longer on the board");
    expect(document.getElementById("steer-context")).toBeNull();
    expect(document.getElementById("steer-title")).toBeNull();
    await new Promise((r) => setTimeout(r, 50));
    expect(contractFPCalls).toEqual([]);
  });
});

describe("RemoteSteerBody — readonly", () => {
  // head: "In BOTH modes: #steer-title (the slug), the owner chip
  // ([data-testid="owner-chip"]), and .pub-chip for public only."
  it("renders the title and the owner chip, and no public chip", () => {
    contractFPSteer(contractFPJob("readonly"));
    expect(document.getElementById("steer-title")?.textContent).toBe("tech-comms");
    expect(document.querySelector('[data-testid="owner-chip"]')).not.toBeNull();
    expect(document.querySelector(".pub-chip")).toBeNull();
  });

  // "Readonly renders NO control: no element with id steer-swatches,
  // steer-stop, steer-markdone, steer-kill, steer-submit, steer-archive,
  // steer-edit, steer-text or steer-send exists in the DOM, and no disabled
  // stand-in for any of them".
  it("renders none of the controls, in any state", () => {
    for (const over of [
      { state: "working", live: true },
      { state: "blocked", live: true },
      { state: "draft", live: false },
      { state: "done", live: false },
    ] as Partial<WebJob>[]) {
      const { unmount } = contractFPSteer(contractFPJob("readonly", over));
      for (const id of contractFPControlIDs) {
        expect(document.getElementById(id), `${over.state}: ${id}`).toBeNull();
      }
      unmount();
    }
  });

  // context: "#steer-context holds, in THIS order, the same containers the
  // local body has, each PRESENT in the DOM only when its data is".
  it("renders every read panel inside #steer-context, in the contract's order", () => {
    contractFPSteer(contractFPJob("readonly"));
    const ctx = document.getElementById("steer-context");
    expect(ctx).not.toBeNull();
    const els: Element[] = [];
    for (const sel of contractFPContextOrder) {
      const el = document.querySelector(sel);
      expect(el, sel).not.toBeNull();
      expect(ctx!.contains(el), `${sel} inside #steer-context`).toBe(true);
      expect(el, sel).not.toHaveAttribute("hidden");
      els.push(el!);
    }
    for (let i = 1; i < els.length; i++) {
      expect(contractFPFollows(els[i - 1]!, els[i]!), `${contractFPContextOrder[i - 1]} before ${contractFPContextOrder[i]}`).toBe(true);
    }
  });

  it("renders the detail and last-sent text", () => {
    contractFPSteer(contractFPJob("readonly"));
    expect(document.getElementById("steer-detail")?.textContent).toContain("contractfp detail text");
    expect(document.getElementById("steer-lastsent")?.textContent).toBe("owner→zeus: ship it");
  });

  // "#steer-prs (one PRChip per pr)".
  it("renders one PR chip per PR", () => {
    contractFPSteer(contractFPJob("readonly"));
    expect(document.querySelectorAll("#steer-prs .pr-chip")).toHaveLength(1);
  });

  // "#steer-chips (one button.chip[data-url=a.url][data-name][data-key] per
  // artifact in wire order, click → onOpenArtifact(a.url, name, key))";
  // chip name rule: "${job.display_slug || job.slug}/${a.name}, its data-key
  // is jobKey(job) (owner-qualified)".
  it("renders one artifact chip per artifact, owner-keyed, opening the proxy URL", async () => {
    const user = userEvent.setup();
    const { onOpenArtifact } = contractFPSteer(contractFPJob("readonly"));
    const chips = [...document.querySelectorAll("#steer-chips button.chip")];
    expect(chips).toHaveLength(2);
    expect(chips.map((c) => c.getAttribute("data-url"))).toEqual([
      "/api/nodes/artifact?node=nSTUDIO77&slug=tech-comms&name=00-overview.md",
      "/api/nodes/artifact?node=nSTUDIO77&slug=tech-comms&name=10-arch.html",
    ]);
    expect(chips.map((c) => c.getAttribute("data-name"))).toEqual([
      "tech-comms/00-overview.md",
      "tech-comms/10-arch.html",
    ]);
    expect(chips.map((c) => c.getAttribute("data-key"))).toEqual(["nSTUDIO77/tech-comms", "nSTUDIO77/tech-comms"]);
    await user.click(chips[1] as HTMLElement);
    expect(onOpenArtifact).toHaveBeenCalledWith(
      "/api/nodes/artifact?node=nSTUDIO77&slug=tech-comms&name=10-arch.html",
      "tech-comms/10-arch.html",
      "nSTUDIO77/tech-comms",
    );
  });

  it("names chips by display_slug when the card carries one", () => {
    contractFPSteer(contractFPJob("readonly", { display_slug: "tech-comms-2" }));
    const chip = document.querySelector("#steer-chips button.chip");
    expect(chip).toHaveAttribute("data-name", "tech-comms-2/00-overview.md");
    expect(chip).toHaveAttribute("data-key", "nSTUDIO77/tech-comms");
  });

  // "#steer-threads (ThreadChips with withBranch={false}, a chip click calling
  // onOpenThread(job.slug, tid, node))".
  it("opens a thread chip with the owner's node id", async () => {
    const user = userEvent.setup();
    const { onOpenThread } = contractFPSteer(contractFPJob("readonly"));
    const chips = document.querySelectorAll("#steer-threads .thread-chip");
    expect(chips).toHaveLength(1);
    await user.click(chips[0] as HTMLElement);
    expect(onOpenThread).toHaveBeenCalledWith("tech-comms", "t1", "nSTUDIO77");
  });

  // "#steer-docq (DocQuestions with readOnly), #steer-feedback (FeedbackPanel
  // with readOnly)" — both panels render their rows with no write control.
  it("renders the doc questions and feedback rows read-only", () => {
    contractFPSteer(contractFPJob("readonly"));
    const docq = document.getElementById("steer-docq")!;
    expect(docq.textContent).toContain("contractfp which store?");
    expect(docq.textContent).toContain("contractfp 7777");
    expect(docq.querySelector("textarea")).toBeNull();
    expect(docq.querySelector("input")).toBeNull();
    expect(docq.querySelector("button")).toBeNull();
    const fb = document.getElementById("steer-feedback")!;
    expect(fb.textContent).toContain("contractfp arrow points wrong");
    expect(fb.querySelector(".fb-edit")).toBeNull();
    expect(fb.querySelector(".fb-delete")).toBeNull();
    expect(fb.querySelector("button")).toBeNull();
  });

  // "`#steer-actions`, present only while a question is pending: it IS the
  // `fieldset` … carrying `disabled` when readonly and not when public,
  // holding the `QuestionBlock` …; nothing else (no retry)."
  it("renders the pending question inside a disabled fieldset", () => {
    contractFPSteer(contractFPJob("readonly"));
    const actions = document.getElementById("steer-actions")!;
    expect(actions).not.toBeNull();
    // AMENDED (join run 2): #steer-actions IS the fieldset
    expect(actions.matches("fieldset[disabled]")).toBe(true);
    expect(actions.textContent).toContain("contractfp approach?");
  });

  // "Then #steer-spec: button#steer-spec-toggle and #steer-spec-body
  // (innerHTML renderMD(stripFrontmatter(content)) from fetchNodeJobRaw(node,
  // slug)), collapsed by default".
  it("loads the spec through the node raw proxy and renders it without frontmatter", async () => {
    contractFPSteer(contractFPJob("readonly"));
    await waitFor(() => expect(document.getElementById("steer-spec-body")).not.toBeNull());
    expect(document.querySelector("button#steer-spec-toggle")).not.toBeNull();
    const body = document.getElementById("steer-spec-body")!;
    expect(body.innerHTML).toContain("Spec heading");
    expect(body.innerHTML).toContain("contractfp spec body");
    expect(body.innerHTML).not.toContain("job: tech-comms");
    const raw = contractFPCalls.find((c) => new URL(c.url, "http://k").pathname === "/api/nodes/job/raw");
    expect(raw).toBeDefined();
    const u = new URL(raw!.url, "http://k");
    expect(u.searchParams.get("node")).toBe("nSTUDIO77");
    expect(u.searchParams.get("slug")).toBe("tech-comms");
    const ctx = document.getElementById("steer-context")!;
    const actions = document.getElementById("steer-actions")!;
    const spec = document.getElementById("steer-spec")!;
    expect(contractFPFollows(ctx, actions) || ctx.contains(actions)).toBe(true);
    expect(contractFPFollows(actions, spec)).toBe(true);
  });

  // "#steer-transcript (ZeusTranscriptPanel with node)".
  it("polls the transcript through the node proxy", async () => {
    const user = userEvent.setup();
    contractFPSteer(contractFPJob("readonly"));
    // AMENDED: the panel mounts collapsed and polls only while expanded
    await user.click(document.querySelector("#steer-transcript .steer-transcript-head") as HTMLElement);
    await waitFor(() =>
      expect(contractFPCalls.some((c) => new URL(c.url, "http://k").pathname === "/api/nodes/transcript")).toBe(true),
    );
    expect(contractFPCalls.some((c) => new URL(c.url, "http://k").pathname === "/api/job/transcript")).toBe(false);
  });

  // "The remote body sets NO data-mind-slug and no other data-mind-*
  // attribute anywhere."
  it("stamps no data-mind-* attribute anywhere", async () => {
    contractFPSteer(contractFPJob("readonly"));
    await waitFor(() => expect(document.getElementById("steer-spec-body")).not.toBeNull());
    expect(contractFPMindAttrs(document.body)).toEqual([]);
  });

  it("posts nothing on render", async () => {
    contractFPSteer(contractFPJob("readonly"));
    await waitFor(() => expect(document.getElementById("steer-spec-body")).not.toBeNull());
    expect(contractFPCalls.filter((c) => c.method !== "GET")).toEqual([]);
  });
});

describe("RemoteSteerBody — public", () => {
  it("renders the public chip beside the owner chip", () => {
    contractFPSteer(contractFPJob("public"));
    expect(document.querySelector('[data-testid="owner-chip"]')).not.toBeNull();
    expect(document.querySelector(".pub-chip")).not.toBeNull();
  });

  // "Public renders exactly round 1's controls under round 1's state rules
  // (colour strip #steer-swatches always; #steer-stop when working;
  // #steer-markdone/#steer-kill when live; #steer-submit when draft;
  // #steer-archive/#steer-edit when settled)"; "Then, public + live only, the
  // composer (#steer-text, #steer-send)."
  it("renders the working-state controls and the composer", () => {
    contractFPSteer(contractFPJob("public", { state: "working", live: true }));
    for (const id of ["steer-swatches", "steer-stop", "steer-markdone", "steer-kill", "steer-text", "steer-send"]) {
      expect(document.getElementById(id), id).not.toBeNull();
    }
  });

  it("renders submit for a draft and archive/edit for a settled job", () => {
    const { unmount } = contractFPSteer(contractFPJob("public", { state: "draft", live: false }));
    expect(document.getElementById("steer-swatches")).not.toBeNull();
    expect(document.getElementById("steer-submit")).not.toBeNull();
    unmount();
    contractFPSteer(contractFPJob("public", { state: "done", live: false }));
    expect(document.getElementById("steer-swatches")).not.toBeNull();
    expect(document.getElementById("steer-archive")).not.toBeNull();
    expect(document.getElementById("steer-edit")).not.toBeNull();
  });

  it("renders the same read panels as readonly", () => {
    contractFPSteer(contractFPJob("public"));
    const ctx = document.getElementById("steer-context")!;
    for (const sel of contractFPContextOrder) {
      const el = document.querySelector(sel);
      expect(el, sel).not.toBeNull();
      expect(ctx.contains(el), sel).toBe(true);
    }
  });

  // "the pending QuestionBlock … answerable through the proxy when public".
  it("renders the pending question outside any disabled fieldset", () => {
    contractFPSteer(contractFPJob("public"));
    const actions = document.getElementById("steer-actions")!;
    expect(actions.textContent).toContain("contractfp approach?");
    // `#steer-actions` IS the fieldset, so the check is on the element itself
    // (a querySelector would only search its children and could never fail)
    expect(actions.matches("fieldset[disabled]")).toBe(false);
  });

  // "each posting /api/nodes/action as before".
  it("posts a colour pick to /api/nodes/action", async () => {
    const user = userEvent.setup();
    contractFPSteer(contractFPJob("public", { state: "working", live: true }));
    // AMENDED (join run 2): the strip is a listbox — open it, pick a row that
    // is not the first (the first is the current "no color")
    const opener = document.querySelector("#steer-swatches button.colorsel-btn") as HTMLElement;
    expect(opener).not.toBeNull();
    await user.click(opener);
    const rows = document.querySelectorAll('#steer-swatches span.colorsel-row[role="option"]');
    expect(rows.length).toBeGreaterThan(1);
    await user.click(rows[1] as HTMLElement);
    await waitFor(() => expect(contractFPCalls.some((c) => c.method === "POST")).toBe(true));
    const post = contractFPCalls.find((c) => c.method === "POST")!;
    expect(new URL(post.url, "http://k").pathname).toBe("/api/nodes/action");
    expect(post.body).toMatchObject({ node: "nSTUDIO77", slug: "tech-comms", action: "color" });
  });

  it("stamps no data-mind-* attribute anywhere", async () => {
    contractFPSteer(contractFPJob("public"));
    await waitFor(() => expect(document.getElementById("steer-spec-body")).not.toBeNull());
    expect(contractFPMindAttrs(document.body)).toEqual([]);
  });
});

describe("ZeusTranscriptPanel", () => {
  // "With node set: polls fetchNodeTranscript(node, slug, TRANSCRIPT_TURNS)
  // instead of fetchJobTranscriptTurns, and omits data-mind-slug from
  // #steer-transcript."
  it("with node: polls /api/nodes/transcript and stamps no data-mind-slug", async () => {
    const user = userEvent.setup();
    render(<ZeusTranscriptPanel slug="tech-comms" node="nSTUDIO77" operator="Carl" />);
    await user.click(document.querySelector(".steer-transcript-head") as HTMLElement);
    await waitFor(() => expect(contractFPCalls.length).toBeGreaterThan(0));
    const u = new URL(contractFPCalls[0]!.url, "http://k");
    expect(u.pathname).toBe("/api/nodes/transcript");
    expect(u.searchParams.get("node")).toBe("nSTUDIO77");
    expect(u.searchParams.get("slug")).toBe("tech-comms");
    expect(Number(u.searchParams.get("turns"))).toBeGreaterThan(0);
    expect(contractFPCalls.every((c) => new URL(c.url, "http://k").pathname !== "/api/job/transcript")).toBe(true);
    const box = document.getElementById("steer-transcript");
    expect(box).not.toBeNull();
    expect(box).not.toHaveAttribute("data-mind-slug");
  });

  // "Without node: byte-identical to round 1."
  it("without node: polls the local transcript route", async () => {
    const user = userEvent.setup();
    render(<ZeusTranscriptPanel slug="tech-comms" operator="Carl" />);
    await user.click(document.querySelector(".steer-transcript-head") as HTMLElement);
    await waitFor(() => expect(contractFPCalls.length).toBeGreaterThan(0));
    expect(new URL(contractFPCalls[0]!.url, "http://k").pathname).toBe("/api/job/transcript");
    expect(document.getElementById("steer-transcript")).toHaveAttribute("data-mind-slug", "tech-comms");
  });
});

describe("DocQuestions readOnly", () => {
  // "readOnly true: no textarea, no send/answer/re-answer button, no ask
  // affordance renders; each row still renders its id, text, context,
  // importance, recommendation and answer as today."
  it("renders every row's fields and no write affordance", () => {
    const props: ContractFPLoose = { job: contractFPJob("readonly"), mutate: contractFPMutate, readOnly: true };
    const { container } = render(<DocQuestions {...props} />);
    const text = container.textContent ?? "";
    for (const s of ["Q1", "Q2", "contractfp which store?", "contractfp ctx", "P0", "contractfp rec", "contractfp 7777"]) {
      expect(text, s).toContain(s);
    }
    expect(container.querySelector("textarea")).toBeNull();
    expect(container.querySelector(".dq-input")).toBeNull();
    expect(container.querySelector(".dq-ask-toggle")).toBeNull();
    expect(container.querySelector(".dq-ask")).toBeNull();
    expect(container.querySelector("button")).toBeNull();
  });

  // "Default false: unchanged."
  it("default still offers the answer input", () => {
    const props: ContractFPLoose = { job: contractFPJob("readonly", { owner: undefined }), mutate: contractFPMutate };
    render(<DocQuestions {...props} />);
    expect(document.querySelector(".dq-input")).not.toBeNull();
  });
});

describe("FeedbackPanel readOnly", () => {
  // "readOnly true: no edit, save or delete control renders; rows render
  // artifact, note and age as today."
  it("renders the rows and no edit/save/delete control", () => {
    const props: ContractFPLoose = {
      job: contractFPJob("readonly"),
      onSaveFeedback: async () => null,
      onDeleteFeedback: () => {},
      readOnly: true,
    };
    const { container } = render(<FeedbackPanel {...props} />);
    expect(container.querySelectorAll(".fb-row")).toHaveLength(1);
    expect(container.textContent).toContain("10-arch.html");
    expect(container.textContent).toContain("contractfp arrow points wrong");
    expect(container.querySelector(".fb-edit")).toBeNull();
    expect(container.querySelector(".fb-save")).toBeNull();
    expect(container.querySelector(".fb-delete")).toBeNull();
    expect(container.querySelector("button")).toBeNull();
  });

  it("default still offers edit and delete", () => {
    const props: ContractFPLoose = {
      job: contractFPJob("readonly", { owner: undefined }),
      onSaveFeedback: async () => null,
      onDeleteFeedback: () => {},
    };
    const { container } = render(<FeedbackPanel {...props} />);
    expect(container.querySelector(".fb-edit")).not.toBeNull();
    expect(container.querySelector(".fb-delete")).not.toBeNull();
  });
});

describe("ThreadOverlay with node", () => {
  function contractFPThread(tid: string | null, over: Partial<WebJob> = {}, onBranched = vi.fn()) {
    const props: ContractFPLoose = {
      open: true,
      slug: "tech-comms",
      tid,
      node: "nSTUDIO77",
      job: contractFPJob("readonly", over),
      onClose: () => {},
      onBranched,
      mutate: contractFPMutate,
      boardNodeID: "nCARL",
    };
    return { onBranched, ...render(<ThreadOverlay {...props} />) };
  }

  // REWRITTEN (ask threads port, 50-contract.md slice D `ThreadOverlay`):
  // "with node and tid === null renders the new-thread dialog in REMOTE mode
  // (#thread-new present, a banner …, the seed textarea, name, the sticky
  // model picker, #thread-branch labelled "⑂ ask <node label> ⌘⏎") … it
  // fetches nothing before branch". Replaces the empty-shell pin.
  it("renders the remote ask dialog for tid null and fetches nothing", async () => {
    contractFPThread(null);
    expect(document.getElementById("thread-new")).not.toBeNull();
    expect(document.getElementById("thread-seed")).not.toBeNull();
    expect(document.getElementById("thread-new-name")).not.toBeNull();
    expect(document.getElementById("thread-models")).not.toBeNull();
    expect(document.getElementById("thread-branch")!.textContent).toBe("⑂ ask studio-mini ⌘⏎");
    const banner = document.querySelector("#thread-new .thread-banner")!.textContent ?? "";
    expect(banner).toContain(
      "Opens a headless fork of zeus' conversation on studio-mini, the owner's Mac, on that cell's account.",
    );
    expect(banner).toContain("Jess sees who asked.");
    // a remote ask is always headless: no toggle is offered
    expect(document.getElementById("thread-headless")).toBeNull();
    await new Promise((r) => setTimeout(r, 50));
    expect(contractFPCalls).toEqual([]);
  });

  // "branch posts postNodeAction(node, slug, "thread-ask", {title, text,
  // model}) and on res.tid calls onBranched(tid, title)".
  it("posts thread-ask through the proxy and hands the tid to onBranched", async () => {
    const user = userEvent.setup();
    const { onBranched } = contractFPThread(null);
    await user.type(document.getElementById("thread-seed")!, "why is CI red on the rollout PR?");
    await user.click(document.getElementById("thread-branch")!);
    await waitFor(() => expect(onBranched).toHaveBeenCalledWith("t3", "why is CI red on the rollout PR?"));
    const post = contractFPCalls.find((c) => c.method === "POST");
    expect(new URL(post!.url, "http://k").pathname).toBe("/api/nodes/action");
    expect(post!.body).toEqual({
      node: "nSTUDIO77",
      slug: "tech-comms",
      action: "thread-ask",
      payload: { title: "why is CI red on the rollout PR?", text: "why is CI red on the rollout PR?", model: "opus" },
    });
  });

  // "a composer #thread-composer with #thread-text and #thread-send exactly
  // when threadComposerAllowed(thread, read, true)" — a live HEADLESS thread.
  const contractFPAskJob: Partial<WebJob> = {
    threads: [
      {
        tid: "t3",
        title: "why is CI red",
        state: "live",
        created_at: 1,
        role: "ask",
        kind: "headless",
        origin: { node_id: "nSTUDIO77", label: "studio-mini", login: "jess@probook.ai" },
      },
    ],
  };

  it("keeps the composer mounted but disabled while the headless thread is answering", async () => {
    contractFPThreadReadOver = { ...contractFPThreadRead, live: true, kind: "headless", answering: true, note: "" };
    contractFPThread("t3", contractFPAskJob);
    await waitFor(() => expect(document.getElementById("thread-answering")).not.toBeNull());
    expect(document.getElementById("thread-answering")!.textContent).toBe("answering…");
    expect(document.getElementById("thread-origin")!.textContent).toBe("from studio-mini");
    expect(document.getElementById("thread-composer")).not.toBeNull();
    expect(document.getElementById("thread-text")).toBeDisabled();
    expect(document.getElementById("thread-send")).toBeDisabled();
    for (const id of ["thread-kill", "thread-revive", "thread-tmux", "thread-models"]) {
      expect(document.getElementById(id), id).toBeNull();
    }
  });

  it("offers a composer on a live headless thread and posts thread-send", async () => {
    const user = userEvent.setup();
    contractFPThreadReadOver = { ...contractFPThreadRead, live: true, kind: "headless", answering: false, note: "" };
    contractFPThread("t3", contractFPAskJob);
    await waitFor(() => expect(document.getElementById("thread-composer")).not.toBeNull());
    await waitFor(() => expect(document.getElementById("thread-text")).not.toBeDisabled());
    const box = document.getElementById("thread-text") as HTMLTextAreaElement;
    await user.type(box, "and the lint job?");
    await user.click(document.getElementById("thread-send")!);
    await waitFor(() => expect(box.value).toBe(""));
    const post = contractFPCalls.find((c) => c.method === "POST");
    expect(post!.body).toEqual({
      node: "nSTUDIO77",
      slug: "tech-comms",
      action: "thread-send",
      payload: { tid: "t3", text: "and the lint job?" },
    });
  });

  // "reads via fetchNodeThreadRead(node, slug, tid) on the same cadence;
  // renders the head (title, state) and the blocks as today … and stamps no
  // data-mind-*" — and a PANE thread (no `kind`) gets no composer and no
  // control at all.
  it("reads through the node proxy, renders the blocks, and no control", async () => {
    contractFPThread("t1");
    await waitFor(() => expect(document.body.textContent).toContain("contractfp because the lock"));
    const call = contractFPCalls.find((c) => new URL(c.url, "http://k").pathname === "/api/nodes/thread/read");
    expect(call).toBeDefined();
    const u = new URL(call!.url, "http://k");
    expect(u.searchParams.get("node")).toBe("nSTUDIO77");
    expect(u.searchParams.get("slug")).toBe("tech-comms");
    expect(u.searchParams.get("tid")).toBe("t1");
    expect(contractFPCalls.some((c) => new URL(c.url, "http://k").pathname === "/api/thread/read")).toBe(false);
    expect(document.body.textContent).toContain("contractfp why the retry");
    expect(document.body.textContent).toContain("contractfp-thread-title");
    for (const id of ["thread-composer", "thread-kill", "thread-revive", "thread-new"]) {
      expect(document.getElementById(id), id).toBeNull();
    }
    expect(document.querySelector("textarea")).toBeNull();
    expect(contractFPMindAttrs(document.body)).toEqual([]);
    expect(contractFPCalls.filter((c) => c.method !== "GET")).toEqual([]);
  });
});

describe("ArtifactOverlay on an owned card", () => {
  // "With job?.owner set: #panel-feedback and the ask button are NOT
  // rendered; savable is false." + artifactParts null → "no library save and
  // no data-mind-slug".
  it("renders no feedback panel, no ask, no save and no data-mind-slug", async () => {
    const url = "/api/nodes/artifact?node=nSTUDIO77&slug=tech-comms&name=00-overview.md";
    const props: ContractFPLoose = {
      open: true,
      url,
      name: "tech-comms/00-overview.md",
      job: contractFPJob("readonly"),
      onNavigate: () => {},
      onAskBranched: () => {},
      onClose: () => {},
      library: [],
      onSaveArtifact: async () => {},
      onRemoveSaved: async () => {},
      mutate: contractFPMutate,
    };
    render(<ArtifactOverlay {...props} />);
    expect(document.getElementById("panel-feedback")).toBeNull();
    expect(document.getElementById("panel-ask")).toBeNull();
    expect([...document.querySelectorAll("button")].some((b) => (b.textContent ?? "").includes("⑂ ask"))).toBe(false);
    // AMENDED: "savable is false, which means #panel-save carries the hidden
    // attribute … rather than being removed"
    expect(document.getElementById("panel-save")).toHaveAttribute("hidden");
    expect(document.querySelector("[data-mind-slug]")).toBeNull();
  });
});
