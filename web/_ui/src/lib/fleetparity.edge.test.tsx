import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { JobPR, WebJob, WebJobOwner } from "../api/types.ts";
import { FeedbackPanel } from "../components/board/chips/FeedbackPanel.tsx";
import { ArtifactOverlay } from "../components/overlays/ArtifactOverlay.tsx";
import { DocQuestions, __resetDocQuestionDrafts } from "../components/overlays/DocQuestions.tsx";
import { RemoteSteerBody } from "../components/overlays/RemoteSteer.tsx";
import { SteerOverlay } from "../components/overlays/SteerOverlay.tsx";
import { ThreadOverlay } from "../components/overlays/ThreadOverlay.tsx";
import { makeJob } from "../test/fixtures.ts";

// fleetparity.edge.test.tsx — the EDGE dimension of 50-contract.md's
// "Round 2 — remote parity", slice R2-W's components: the readonly steer
// modal's ABSENCE of every control (and of any disabled stand-in) across job
// states, the public modal's controls under the state rules, containers
// absent when their data is, a failed spec load leaving no #steer-spec, no
// data-mind-* on another node's content, DocQuestions/FeedbackPanel readOnly
// hiding every control, ThreadOverlay with `node` (tid null renders nothing;
// no composer or controls), and ArtifactOverlay on an owned card. Written
// blind from the contract, with no sight of RemoteSteer.tsx, SteerOverlay.tsx,
// ThreadOverlay.tsx, ArtifactOverlay.tsx, DocQuestions.tsx, the chips, App.tsx
// or api/client.ts. Happy paths are athena-logic's.
//
// The client is NOT mocked: `globalThis.fetch` is stubbed with a router, so
// every request a component makes is observable by URL and method.
//
// Every helper is prefixed `edgeFP`.

const edgeFPMutate = async <T,>(op: () => Promise<T>) => op();

const edgeFPOwner: WebJobOwner = { node_id: "nSTUDIO", label: "studio", operator: "Jess", online: true };

type EdgeFPReply = { status: number; body: unknown };
type EdgeFPRoute = (u: URL, init?: RequestInit) => EdgeFPReply | undefined;

const edgeFPRaw = "---\r\nrepo: kraken\r\nvisibility: readonly\r\n---\r\n\r\n# The spec\r\nbody line\r\n";

function edgeFPDefaultRoute(u: URL): EdgeFPReply {
  switch (u.pathname) {
    case "/api/nodes/job/raw":
      return { status: 200, body: { slug: "remote-job", content: edgeFPRaw, mtime: 1, editable: true } };
    case "/api/nodes/transcript":
      return { status: 200, body: { session: "s1", blocks: [{ kind: "text", text: "zeus transcript says", uuid: "u1" }] } };
    case "/api/nodes/thread/read":
      return { status: 200, body: { blocks: [{ kind: "text", text: "thread says hello" }], live: true } };
    case "/api/nodes/artifact":
      return { status: 200, body: "# Overview\n\nremote artifact body\n" };
    case "/api/goals":
      return { status: 200, body: { goals: { week: [], day: [] } } };
    default:
      return { status: 200, body: { ok: true } };
  }
}

function edgeFPStubFetch(route: EdgeFPRoute = () => undefined) {
  const fetchMock = vi.fn((input: unknown, init?: RequestInit) => {
    const u = new URL(String(input), "http://kraken.test");
    const r = route(u, init) ?? edgeFPDefaultRoute(u);
    const text = typeof r.body === "string" ? r.body : JSON.stringify(r.body);
    const ct = typeof r.body === "string" ? "text/markdown; charset=utf-8" : "application/json";
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      statusText: "",
      headers: new Headers({ "Content-Type": ct }),
      json: () => Promise.resolve(typeof r.body === "string" ? JSON.parse(r.body) : r.body),
      text: () => Promise.resolve(text),
      blob: () => Promise.resolve(new Blob([text])),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

type EdgeFPFetch = ReturnType<typeof edgeFPStubFetch>;

function edgeFPCalls(f: EdgeFPFetch): { url: URL; method: string }[] {
  return f.mock.calls.map(([input, init]) => ({
    url: new URL(String(input), "http://kraken.test"),
    method: ((init as RequestInit | undefined)?.method ?? "GET").toUpperCase(),
  }));
}

function edgeFPCalled(f: EdgeFPFetch, pathname: string): boolean {
  return edgeFPCalls(f).some((c) => c.url.pathname === pathname);
}

function edgeFPPosts(f: EdgeFPFetch): string[] {
  return edgeFPCalls(f)
    .filter((c) => c.method !== "GET")
    .map((c) => `${c.method} ${c.url.pathname}`);
}

/** Every element anywhere in the document carrying a data-mind-* attribute. */
function edgeFPMindAttrs(): string[] {
  const out: string[] = [];
  for (const el of document.body.querySelectorAll("*")) {
    for (const a of el.getAttributeNames()) {
      if (a.startsWith("data-mind")) out.push(`${el.tagName.toLowerCase()}[${a}]`);
    }
  }
  return out;
}

/** let pending fetch promises and the effects they trigger settle. */
async function edgeFPSettle() {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 5));
}

function edgeFPJob(visibility: string, over: Partial<WebJob> = {}): WebJob {
  return makeJob({ slug: "remote-job", state: "working", live: true, owner: edgeFPOwner, visibility, ...over });
}

/** A card carrying every read the steer modal renders. */
function edgeFPRichJob(visibility: string, over: Partial<WebJob> = {}): WebJob {
  return edgeFPJob(visibility, {
    color: "blue",
    detail: "zeus last said **this**",
    last_sent: "carl last said that",
    prs: [{ url: "https://github.com/o/r/pull/7", state: "open" } as JobPR],
    threads: [{ tid: "t9", title: "why the retry", state: "closed", created_at: 1 }],
    artifacts: [{ name: "00-overview.md", url: "/api/nodes/artifact?node=nSTUDIO&slug=remote-job&name=00-overview.md" }],
    doc_questions: [{ id: "Q1", text: "which store?", importance: "P0", recommendation: "sqlite" }],
    feedback: [{ id: 3, artifact: "00-overview.md", note: "tighten this", at: 0 }],
    question: {
      tool_use_id: "tu1",
      questions: [{ question: "ship it?", header: "ship", multi_select: false, options: [{ label: "yes", description: "go" }, { label: "no", description: "wait" }] }],
    },
    ...over,
  });
}

const EDGE_FP_CONTROL_IDS = [
  "steer-swatches",
  "steer-stop",
  "steer-markdone",
  "steer-kill",
  "steer-submit",
  "steer-archive",
  "steer-edit",
  "steer-text",
  "steer-send",
] as const;

function edgeFPPresentControls(): string[] {
  return EDGE_FP_CONTROL_IDS.filter((id) => document.getElementById(id) !== null);
}

function edgeFPMountSteer(job: WebJob) {
  const onOpenArtifact = vi.fn();
  const onOpenThread = vi.fn();
  const utils = render(
    <SteerOverlay
      open
      slug={job.slug}
      node={job.owner?.node_id ?? "nSTUDIO"}
      mode="steer"
      requireText={false}
      job={job}
      onClose={() => {}}
      mutate={edgeFPMutate}
      onOpenArtifact={onOpenArtifact}
      onOpenThread={onOpenThread}
      operator="carl"
    />,
  );
  return { ...utils, onOpenArtifact, onOpenThread };
}

beforeEach(() => {
  vi.unstubAllGlobals();
  __resetDocQuestionDrafts();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// RemoteSteerBody — head
// ---------------------------------------------------------------------------

describe("RemoteSteerBody readonly head", () => {
  // `RemoteSteerBody` — head: "**Readonly renders NO control**: no element
  // with id `steer-swatches`, `steer-stop`, `steer-markdone`, `steer-kill`,
  // `steer-submit`, `steer-archive`, `steer-edit`, `steer-text` or
  // `steer-send` exists in the DOM, and no disabled stand-in for any of them
  // (round 1's disabled "colour" button is gone)." — in every job state,
  // including the ones where public shows each control.
  for (const [state, live] of [
    ["working", true],
    ["blocked", true],
    ["draft", false],
    ["done", false],
  ] as const) {
    it(`renders no control and no disabled stand-in for a ${state} card`, async () => {
      const f = edgeFPStubFetch();
      edgeFPMountSteer(edgeFPRichJob("readonly", { state, live }));
      await waitFor(() => expect(edgeFPCalled(f, "/api/nodes/job/raw")).toBe(true));
      await edgeFPSettle();
      expect(edgeFPPresentControls()).toEqual([]);
      // the only disabled thing a readonly modal may hold is the question's
      // fieldset (and whatever lives inside it)
      const disabled = [...document.querySelectorAll("[disabled], [aria-disabled='true']")].filter(
        // AMENDED (run-2 arbitration): `#steer-actions` IS the fieldset
        (el) => !el.closest("fieldset#steer-actions[disabled]"),
      );
      expect(disabled.map((el) => el.outerHTML.slice(0, 80))).toEqual([]);
      expect(edgeFPPosts(f)).toEqual([]);
    });
  }

  // "a visibility value other than public is readonly" (round-1 remoteMode)
  // — a card with no visibility key at all is a readonly card here.
  it("treats an owned card with no visibility as readonly", async () => {
    const f = edgeFPStubFetch();
    const job = edgeFPRichJob("readonly");
    delete (job as { visibility?: string }).visibility;
    edgeFPMountSteer(job);
    await waitFor(() => expect(edgeFPCalled(f, "/api/nodes/job/raw")).toBe(true));
    await edgeFPSettle();
    expect(edgeFPPresentControls()).toEqual([]);
    expect(document.querySelector(".pub-chip")).toBeNull();
  });

  // "`.pub-chip` for public only."
  it("shows no public chip on a readonly card and the owner chip on both", async () => {
    edgeFPStubFetch();
    const { unmount } = edgeFPMountSteer(edgeFPJob("readonly"));
    expect(document.querySelector(".pub-chip")).toBeNull();
    expect(document.querySelector('[data-testid="owner-chip"]')).not.toBeNull();
    expect(document.getElementById("steer-title")).not.toBeNull();
    await edgeFPSettle();
    unmount();
    edgeFPMountSteer(edgeFPJob("public"));
    expect(document.querySelector(".pub-chip")).not.toBeNull();
    expect(document.querySelector('[data-testid="owner-chip"]')).not.toBeNull();
    await edgeFPSettle();
  });
});

describe("RemoteSteerBody public head", () => {
  // "**Public** renders exactly round 1's controls under round 1's state
  // rules (colour strip `#steer-swatches` always; `#steer-stop` when working;
  // `#steer-markdone`/`#steer-kill` when live; `#steer-submit` when draft;
  // `#steer-archive`/`#steer-edit` when settled)" and "Then, public + live
  // only, the composer (`#steer-text`, `#steer-send`)". `done` is read as
  // settled (listed as a silence).
  const cases: { state: string; live: boolean; want: string[] }[] = [
    { state: "working", live: true, want: ["steer-swatches", "steer-stop", "steer-markdone", "steer-kill", "steer-text", "steer-send"] },
    { state: "blocked", live: true, want: ["steer-swatches", "steer-markdone", "steer-kill", "steer-text", "steer-send"] },
    // AMENDED (run-2 arbitration): "settled" is round 1's `!live`
    { state: "draft", live: false, want: ["steer-swatches", "steer-submit", "steer-archive", "steer-edit"] },
    { state: "done", live: false, want: ["steer-swatches", "steer-archive", "steer-edit"] },
  ];
  for (const c of cases) {
    it(`shows exactly the ${c.state} controls`, async () => {
      edgeFPStubFetch();
      edgeFPMountSteer(edgeFPJob("public", { state: c.state, live: c.live }));
      await edgeFPSettle();
      expect(edgeFPPresentControls()).toEqual(c.want);
    });
  }
});

describe("RemoteSteerBody with no job", () => {
  // "`job === undefined` renders the "no longer on the board" note and
  // nothing else."
  it("renders only the no-longer-on-the-board note", async () => {
    const f = edgeFPStubFetch();
    const props = {
      node: "nSTUDIO",
      slug: "remote-job",
      job: undefined,
      onOpenArtifact: vi.fn(),
      onOpenThread: vi.fn(),
      operator: "carl",
      onClose: () => {},
      mutate: edgeFPMutate,
    };
    const { container } = render(<RemoteSteerBody {...(props as unknown as Parameters<typeof RemoteSteerBody>[0])} />);
    await edgeFPSettle();
    expect(container.textContent).toMatch(/no longer on the board/);
    expect(document.getElementById("steer-context")).toBeNull();
    expect(document.getElementById("steer-spec")).toBeNull();
    expect(edgeFPPresentControls()).toEqual([]);
    expect(f).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// RemoteSteerBody — context
// ---------------------------------------------------------------------------

describe("RemoteSteerBody context containers", () => {
  // "`#steer-context` holds … the same containers the local body has, each
  // PRESENT in the DOM only when its data is (no `hidden` stand-ins;
  // "absent" is testable)" — empty arrays and missing keys alike.
  for (const vis of ["readonly", "public"]) {
    it(`renders no container for absent or empty data (${vis})`, async () => {
      edgeFPStubFetch();
      edgeFPMountSteer(
        edgeFPJob(vis, { prs: [], threads: [], artifacts: [], doc_questions: [], feedback: [], detail: "", last_sent: "" }),
      );
      await edgeFPSettle();
      for (const sel of [
        "#steer-prs",
        "#steer-threads",
        "#steer-detail",
        "#steer-lastsent",
        "#steer-chips",
        "#steer-docq",
        "#steer-feedback",
        "#steer-ctxwin",
        "#steer-tokens",
        ".steer-meters",
        '[data-testid="load-drill"]',
      ]) {
        expect(document.querySelector(sel), sel).toBeNull();
      }
    });
  }

  // `#steer-detail` "(innerHTML from `useRenderedMD(job.detail)` —
  // `renderMD` escapes first, so the sibling's text is inert".
  it("renders a sibling's hostile detail inert", async () => {
    edgeFPStubFetch();
    (window as unknown as { __edgeFP?: number }).__edgeFP = undefined;
    edgeFPMountSteer(
      edgeFPJob("readonly", {
        detail: '<img src=x onerror="window.__edgeFP=1"><script>window.__edgeFP=2</script><a href="javascript:window.__edgeFP=3">x</a>',
      }),
    );
    await waitFor(() => expect(document.getElementById("steer-detail")).not.toBeNull());
    await edgeFPSettle();
    const detail = document.getElementById("steer-detail")!;
    expect(detail.querySelector("img, script, iframe")).toBeNull();
    expect(detail.querySelector('a[href^="javascript:"]')).toBeNull();
    expect((window as unknown as { __edgeFP?: number }).__edgeFP).toBeUndefined();
  });

  // "a failed load (403, 404, 502) renders no `#steer-spec` at all."
  for (const status of [403, 404, 502]) {
    it(`renders no #steer-spec when the raw load answers ${status}`, async () => {
      const f = edgeFPStubFetch((u) =>
        u.pathname === "/api/nodes/job/raw" ? { status, body: { error: "studio answered non-JSON" } } : undefined,
      );
      edgeFPMountSteer(edgeFPRichJob("readonly"));
      await waitFor(() => expect(edgeFPCalled(f, "/api/nodes/job/raw")).toBe(true));
      await edgeFPSettle();
      expect(document.getElementById("steer-spec")).toBeNull();
      expect(document.getElementById("steer-spec-toggle")).toBeNull();
      expect(document.getElementById("steer-spec-body")).toBeNull();
    });
  }

  // "`#steer-spec-body` (innerHTML `renderMD(stripFrontmatter(content))` …),
  // collapsed by default" — a CRLF frontmatter never reaches the body.
  it("never shows the frontmatter of a CRLF spec", async () => {
    const f = edgeFPStubFetch();
    edgeFPMountSteer(edgeFPRichJob("readonly"));
    await waitFor(() => expect(document.getElementById("steer-spec-toggle")).not.toBeNull());
    expect(edgeFPCalls(f).some((c) => c.url.pathname === "/api/nodes/job/raw" && c.url.searchParams.get("node") === "nSTUDIO")).toBe(true);
    fireEvent.click(document.getElementById("steer-spec-toggle")!);
    await waitFor(() => expect(document.getElementById("steer-spec-body")?.textContent ?? "").toMatch(/The spec/));
    const body = document.getElementById("steer-spec-body")!;
    expect(body.textContent).not.toMatch(/repo: kraken/);
    expect(body.textContent).not.toMatch(/visibility:/);
  });

  // "The remote body sets NO `data-mind-slug` and no other `data-mind-*`
  // attribute anywhere" and `ZeusTranscriptPanel`: "With `node` set: polls
  // `fetchNodeTranscript` … instead of `fetchJobTranscriptTurns`, and omits
  // `data-mind-slug`" — nothing local is fetched for another node's job.
  for (const vis of ["readonly", "public"]) {
    it(`stamps no data-mind-* and reads nothing local (${vis})`, async () => {
      const f = edgeFPStubFetch();
      edgeFPMountSteer(edgeFPRichJob(vis));
      // the transcript panel polls only while expanded (contract amendment)
      await waitFor(() => expect(document.querySelector(".steer-transcript-head")).not.toBeNull());
      fireEvent.click(document.querySelector(".steer-transcript-head")!);
      await waitFor(() => expect(edgeFPCalled(f, "/api/nodes/transcript")).toBe(true));
      await waitFor(() => expect(document.getElementById("steer-spec")).not.toBeNull());
      await edgeFPSettle();
      expect(edgeFPMindAttrs()).toEqual([]);
      for (const local of ["/api/job/transcript", "/api/job/raw", "/api/thread/read", "/api/job/form", "/api/draft"]) {
        expect(edgeFPCalled(f, local), local).toBe(false);
      }
      expect(edgeFPCalls(f).filter((c) => c.url.pathname.startsWith("/api/artifact/"))).toEqual([]);
    });
  }

  // `remoteArtifactChipName`: "A remote artifact chip's name is
  // `${job.display_slug || job.slug}/${a.name}`, its `data-key` is
  // `jobKey(job)` (owner-qualified)"; click → `onOpenArtifact(a.url, name,
  // key)` — on a READONLY card, with an awkward name and an empty
  // display_slug.
  it("opens a readonly card's awkwardly named artifact with the proxy URL", async () => {
    edgeFPStubFetch();
    const url = "/api/nodes/artifact?node=nSTUDIO&slug=remote-job&name=dir%2F10+arch%23v2.html";
    const { onOpenArtifact } = edgeFPMountSteer(
      edgeFPJob("readonly", { display_slug: "", artifacts: [{ name: "dir/10 arch#v2.html", url }] }),
    );
    await edgeFPSettle();
    const chip = document.querySelector<HTMLElement>("#steer-chips button.chip")!;
    expect(chip).not.toBeNull();
    expect(chip.getAttribute("data-url")).toBe(url);
    expect(chip.getAttribute("data-name")).toBe("remote-job/dir/10 arch#v2.html");
    expect(chip.getAttribute("data-key")).toBe("nSTUDIO/remote-job");
    fireEvent.click(chip);
    expect(onOpenArtifact).toHaveBeenCalledWith(url, "remote-job/dir/10 arch#v2.html", "nSTUDIO/remote-job");
  });

  it("names a chip by display_slug when the card has one", async () => {
    edgeFPStubFetch();
    edgeFPMountSteer(
      edgeFPJob("readonly", { display_slug: "shown-name", artifacts: [{ name: "a.md", url: "/api/nodes/artifact?node=nSTUDIO&slug=remote-job&name=a.md" }] }),
    );
    await edgeFPSettle();
    const chip = document.querySelector<HTMLElement>("#steer-chips button.chip")!;
    expect(chip.getAttribute("data-name")).toBe("shown-name/a.md");
    expect(chip.getAttribute("data-key")).toBe("nSTUDIO/remote-job");
  });

  // REWRITTEN (ask threads port, 50-contract.md slice D `RemoteSteerBody`):
  // "the threads row mounts ThreadChips with withBranch={!!job.live} … with
  // the branch label "⑂ ask", on readonly AND public; clicking it calls
  // onOpenThread(slug, null, node)". A readonly card's thread chip still
  // opens with the owner's node id. Replaces the offers-no-branch pin.
  it("opens a readonly card's thread with the owner's node and offers ⑂ ask", async () => {
    edgeFPStubFetch();
    const { onOpenThread } = edgeFPMountSteer(edgeFPRichJob("readonly"));
    await edgeFPSettle();
    const threads = document.getElementById("steer-threads")!;
    expect(threads).not.toBeNull();
    expect(threads.textContent).not.toMatch(/branch/i);
    expect(threads.querySelectorAll("button").length).toBe(2);
    fireEvent.click(within(threads).getByText(/why the retry/));
    expect(onOpenThread).toHaveBeenCalledWith("remote-job", "t9", "nSTUDIO");
    fireEvent.click(within(threads).getByText("⑂ ask"));
    expect(onOpenThread).toHaveBeenLastCalledWith("remote-job", null, "nSTUDIO");
  });

  it("offers no ⑂ ask on a card whose cell is not live", async () => {
    edgeFPStubFetch();
    edgeFPMountSteer(edgeFPRichJob("readonly", { live: false }));
    await edgeFPSettle();
    expect(document.body.textContent).not.toContain("⑂ ask");
  });

  // "when a live remote card has NO threads it renders a sibling
  // <div class="threads" id="steer-ask"> holding a lone ⑂ ask BranchButton"
  it("renders a lone ⑂ ask in #steer-ask on a live card with no threads", async () => {
    edgeFPStubFetch();
    const { onOpenThread } = edgeFPMountSteer(edgeFPRichJob("readonly", { threads: [] }));
    await edgeFPSettle();
    expect(document.getElementById("steer-threads")).toBeNull();
    const ask = document.getElementById("steer-ask")!;
    expect(ask).not.toBeNull();
    expect(ask.querySelectorAll("button").length).toBe(1);
    fireEvent.click(within(ask).getByText("⑂ ask"));
    expect(onOpenThread).toHaveBeenCalledWith("remote-job", null, "nSTUDIO");
  });

  // "`#steer-actions`, present only while a question is pending: it IS the
  // `fieldset` … carrying `disabled` when readonly and not when public,
  // holding the `QuestionBlock` …; nothing else (no retry)."
  it("holds a readonly card's question in a disabled fieldset and nothing else", async () => {
    const f = edgeFPStubFetch();
    edgeFPMountSteer(edgeFPRichJob("readonly", { state: "blocked", err: "boom", health: "stalled" }));
    await edgeFPSettle();
    const actions = document.getElementById("steer-actions")!;
    expect(actions).not.toBeNull();
    // AMENDED (run-2 arbitration): `#steer-actions` IS the disabled fieldset
    expect(actions.matches("fieldset[disabled]")).toBe(true);
    expect(actions.textContent).toMatch(/ship it\?/);
    fireEvent.click(within(actions).getByText(/yes/));
    await edgeFPSettle();
    expect(edgeFPPosts(f)).toEqual([]);
    // buttons only: the fixture's thread title itself contains "retry"
    expect([...document.querySelectorAll("button")].filter((b) => /^\s*↻?\s*retry/i.test(b.textContent ?? ""))).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// DocQuestions / FeedbackPanel readOnly
// ---------------------------------------------------------------------------

describe("DocQuestions readOnly", () => {
  const docs = {
    doc_questions: [
      { id: "Q1", text: "which store?", context: "two options", importance: "P0", recommendation: "sqlite" },
      { id: "Q2", text: "which port?", importance: "P2", answer: "the old answer", answered_at: "2026-10-20" },
    ],
  };

  // `DocQuestions`: "`readOnly` true: no `textarea`, no send/answer/re-answer
  // button, no ask affordance renders; each row still renders its id, text,
  // context, importance, recommendation and answer as today." — even with an
  // ask door and a live cell, and even when an answered row is clicked.
  it("renders no control even with a door, a live cell and a click on an answered row", async () => {
    const f = edgeFPStubFetch();
    const onAskBranched = vi.fn();
    const { container } = render(
      <DocQuestions job={makeJob({ slug: "j", state: "working", live: true, ...docs })} mutate={edgeFPMutate} onAskBranched={onAskBranched} readOnly />,
    );
    const panel = container.querySelector(".doc-questions")!;
    expect(panel).not.toBeNull();
    const controls = () => container.querySelectorAll("textarea, input, select, .dq-input, .dq-ask, .dq-ask-toggle, .dq-ask-input, .dq-reanswer-cancel");
    expect(controls().length).toBe(0);
    expect(screen.queryByText("send")).toBeNull();
    expect(screen.queryByText("accept rec")).toBeNull();
    expect(screen.queryByText(/⑂/)).toBeNull();
    const answered = container.querySelector<HTMLElement>(".dq-answered");
    if (answered) fireEvent.click(answered);
    await edgeFPSettle();
    expect(controls().length).toBe(0);
    for (const text of ["Q1", "which store?", "two options", "P0", "sqlite", "Q2", "which port?", "the old answer"]) {
      expect(panel.textContent, text).toContain(text);
    }
    expect(edgeFPPosts(f)).toEqual([]);
    expect(onAskBranched).not.toHaveBeenCalled();
  });

  // "Default false: unchanged." — the same props without readOnly still
  // offer the answer box.
  it("still offers the answer box without readOnly", () => {
    edgeFPStubFetch();
    const { container } = render(<DocQuestions job={makeJob({ slug: "j", state: "working", live: true, ...docs })} mutate={edgeFPMutate} />);
    expect(container.querySelector(".dq-input")).not.toBeNull();
  });
});

describe("FeedbackPanel readOnly", () => {
  const notes = [
    { id: 11, artifact: "10-arch.html", note: "the arrow points the wrong way", at: 0 },
    { id: 12, artifact: "00-overview.md", note: "too long", at: 0, addressed: "trimmed", addressed_at: "2026-07-20" },
  ];

  // `FeedbackPanel`: "`readOnly` true: no edit, save or delete control
  // renders; rows render artifact, note and age as today." — and a click on a
  // note opens no editor.
  it("renders rows with no edit, save or delete control", async () => {
    const onSaveFeedback = vi.fn(async () => null);
    const onDeleteFeedback = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const { container } = render(
      <FeedbackPanel
        job={makeJob({ state: "working", live: true, feedback: notes })}
        onSaveFeedback={onSaveFeedback}
        onDeleteFeedback={onDeleteFeedback}
        readOnly
      />,
    );
    expect(container.querySelectorAll(".fb-row").length).toBe(2);
    expect(container.textContent).toContain("10-arch.html");
    expect(container.textContent).toContain("the arrow points the wrong way");
    const controls = () => container.querySelectorAll(".fb-edit, .fb-save, .fb-delete, .fb-cancel, .fb-edit-input, textarea, input");
    expect(controls().length).toBe(0);
    for (const note of container.querySelectorAll<HTMLElement>(".fb-note, .fb-row")) {
      fireEvent.click(note);
      fireEvent.doubleClick(note);
    }
    await edgeFPSettle();
    expect(controls().length).toBe(0);
    expect(onSaveFeedback).not.toHaveBeenCalled();
    expect(onDeleteFeedback).not.toHaveBeenCalled();
    expect(confirm).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// ThreadOverlay with node
// ---------------------------------------------------------------------------

describe("ThreadOverlay with node", () => {
  const job = edgeFPJob("public", { threads: [{ tid: "t1", title: "why the retry", state: "live", pane: "%7", created_at: 0 }] });
  const base = { open: true as const, slug: "remote-job", onBranched: () => {}, mutate: edgeFPMutate, onClose: () => {}, job, node: "nSTUDIO" };

  // REWRITTEN (ask threads port, 50-contract.md slice D `ThreadOverlay`):
  // "with node and tid === null renders the new-thread dialog in REMOTE mode
  // … it fetches nothing before branch" — and a blank question posts nothing.
  // Replaces the empty-shell pin.
  it("renders the remote ask dialog for tid null, fetching nothing before branch", async () => {
    const f = edgeFPStubFetch();
    render(<ThreadOverlay {...base} tid={null} />);
    await edgeFPSettle();
    expect(document.getElementById("thread-new")).not.toBeNull();
    expect(document.getElementById("thread-branch")!.textContent).toBe("⑂ ask studio ⌘⏎");
    expect(f).not.toHaveBeenCalled();
    fireEvent.click(document.getElementById("thread-branch")!);
    await edgeFPSettle();
    expect(f).not.toHaveBeenCalled();
  });

  // "a composer … exactly when threadComposerAllowed(thread, read, true)":
  // a CLOSED headless thread gets none.
  it("offers no composer on a closed headless thread", async () => {
    edgeFPStubFetch((u) =>
      u.pathname === "/api/nodes/thread/read"
        ? { status: 200, body: { blocks: [{ kind: "text", text: "thread says hello" }], live: false, kind: "headless" } }
        : undefined,
    );
    const askJob = edgeFPJob("public", {
      threads: [{ tid: "t3", title: "why is CI red", state: "closed", created_at: 0, kind: "headless" }],
    });
    render(<ThreadOverlay {...base} job={askJob} tid="t3" />);
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await edgeFPSettle();
    expect(document.getElementById("thread-composer")).toBeNull();
  });

  // "reads via `fetchNodeThreadRead(node, slug, tid)` on the same cadence;
  // renders the head (title, state) and the blocks as today … and stamps no
  // `data-mind-*`" — and a PANE thread (no `kind`) on a PUBLIC card, live,
  // gets no composer, send, branch, kill, revive or model control, where a
  // local overlay would show them.
  it("reads through the proxy without n and renders no control", async () => {
    const f = edgeFPStubFetch();
    render(<ThreadOverlay {...base} tid="t1" />);
    await waitFor(() => expect(document.body.textContent).toMatch(/thread says hello/));
    await edgeFPSettle();
    const reads = edgeFPCalls(f).filter((c) => c.url.pathname === "/api/nodes/thread/read");
    expect(reads.length).toBeGreaterThan(0);
    for (const r of reads) {
      expect(r.url.searchParams.get("node")).toBe("nSTUDIO");
      expect(r.url.searchParams.get("slug")).toBe("remote-job");
      expect(r.url.searchParams.get("tid")).toBe("t1");
      expect(r.url.searchParams.has("n")).toBe(false);
    }
    expect(edgeFPCalled(f, "/api/thread/read")).toBe(false);
    expect(edgeFPPosts(f)).toEqual([]);
    expect(document.body.querySelector("textarea, input, select")).toBeNull();
    const buttons = [...document.body.querySelectorAll("button")].map((b) => b.textContent ?? "");
    expect(buttons.filter((t) => /send|branch|⑂|kill|revive|model|haiku|sonnet|opus|fable/i.test(t))).toEqual([]);
    expect(edgeFPMindAttrs()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// ArtifactOverlay on an owned card
// ---------------------------------------------------------------------------

describe("ArtifactOverlay on an owned card", () => {
  // `ArtifactOverlay`: "With `job?.owner` set: `#panel-feedback` and the ask
  // button are NOT rendered; `savable` is false." and `artifactParts`
  // null for the proxy URL: "no save parts, no library save and no
  // `data-mind-slug`".
  for (const vis of ["readonly", "public"]) {
    it(`renders no feedback panel, ask, save or mind source (${vis})`, async () => {
      const f = edgeFPStubFetch();
      const url = "/api/nodes/artifact?node=nSTUDIO&slug=remote-job&name=00-overview.md";
      render(
        <ArtifactOverlay
          open
          url={url}
          name="remote-job/00-overview.md"
          job={edgeFPJob(vis, { artifacts: [{ name: "00-overview.md", url }] })}
          onNavigate={() => {}}
          onAskBranched={() => {}}
          onClose={() => {}}
          library={[]}
          onSaveArtifact={async () => {}}
          onRemoveSaved={async () => {}}
          mutate={edgeFPMutate}
        />,
      );
      await waitFor(() => expect(edgeFPCalled(f, "/api/nodes/artifact")).toBe(true));
      await edgeFPSettle();
      expect(document.getElementById("panel-feedback")).toBeNull();
      expect(screen.queryByText(/⑂ ask/)).toBeNull();
      // AMENDED (run-1 arbitration): "`savable` is false" means `#panel-save`
      // carries `hidden`, not that it is absent
      const save = document.getElementById("panel-save");
      expect(save).not.toBeNull();
      expect(save!.hasAttribute("hidden")).toBe(true);
      expect(edgeFPMindAttrs()).toEqual([]);
      expect(edgeFPPosts(f)).toEqual([]);
    });
  }
});
