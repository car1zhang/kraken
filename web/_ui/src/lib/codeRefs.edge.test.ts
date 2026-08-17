import { render, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { codeRefTarget, parseCodeRef, wireCodeRefs } from "./codeRefs.ts";
import { esc, inlineMD } from "./markdown.ts";

// codeRefs.edge.test.ts — the EDGE dimension of slice W of
// artifacts/50-contract.md: the boundaries and the REFUSALS. Every assertion
// below traces to a stated obligation in that file; where the contract is
// silent the probe is omitted rather than widened, because a probe that
// accepts several shapes pins none of them.
//
// Written blind, against the frozen symbol list alone. Package-level
// identifiers are `edge`-prefixed: this file's module scope is private, but
// the convention is the repo's and the sibling blind file shares the surface.
//
// The client module is mocked wholesale — the union of every function the
// four components touched here import at HEAD, plus the one new symbol. What
// these tests observe is WHETHER `openInEditor` was called, never a network.
vi.mock("../api/client.ts", () => ({
  // ArtifactOverlay
  createFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
  // ThreadOverlay
  fetchThreadRead: vi.fn(() => Promise.resolve({ blocks: [] })),
  capturePane: vi.fn(() => Promise.resolve({ pane: "%1", content: "" })),
  openTmux: vi.fn(() => Promise.resolve({ ok: true })),
  // DreamView
  fetchDream: vi.fn(() => Promise.resolve({ reports: [] })),
  fetchWatchlist: vi.fn(() => Promise.resolve({ watchlist: [] })),
  setWatchVerdict: vi.fn(() => Promise.resolve({ ok: true })),
  // SteerOverlay's surface, kept whole so an incidental import cannot leave a
  // named export undefined mid-render
  fetchDraft: vi.fn(() => Promise.resolve({ slug: "j", text: "" })),
  fetchJobForm: vi.fn(() => Promise.resolve({ slug: "j", mtime: 0 })),
  steerJob: vi.fn(() => Promise.resolve({ ok: true })),
  reactivateJob: vi.fn(() => Promise.resolve({ ok: true })),
  killJob: vi.fn(() => Promise.resolve({ ok: true })),
  doneJob: vi.fn(() => Promise.resolve({ ok: true })),
  saveDraft: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(),
  answerDocQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  editFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  deleteFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  setJobColor: vi.fn(() => Promise.resolve({ ok: true })),
  retryJob: vi.fn(() => Promise.resolve({ ok: true })),
  answerQuestion: vi.fn(() => Promise.resolve({ ok: true })),
  // the symbol this whole slice exists for
  openInEditor: vi.fn(() => Promise.resolve({ ok: true })),
}));

import { fetchDream, fetchThreadRead, fetchWatchlist, openInEditor } from "../api/client.ts";
import { DreamView } from "../components/dream/DreamView.tsx";
import { ArtifactOverlay } from "../components/overlays/ArtifactOverlay.tsx";
import { ThreadOverlay } from "../components/overlays/ThreadOverlay.tsx";
import { makeJob } from "../test/fixtures.ts";

const edgeOpen = vi.mocked(openInEditor);
const edgeThreadRead = vi.mocked(fetchThreadRead);
const edgeDream = vi.mocked(fetchDream);
const edgeWatch = vi.mocked(fetchWatchlist);

// --- the rejection table ----------------------------------------------------

/**
 * One row per N-clause of the contract's `parseCodeRef` rejection list. The
 * label names the clause, so a failure says WHICH refusal regressed rather
 * than only which string.
 */
const edgeRejects: [clause: string, span: string][] = [
  ["host:port, dotted-numeric left side", "127.0.0.1:7777"],
  ["host:port, extensionless left side", "localhost:8080"],
  ["clock time", "16:08:11"],
  ["clock time in prose", "resets 7:10pm"],
  ["timestamp", "2026-07-30 19:47"],
  ["key:value id", "org_id:123"],
  ["key=value id", "job_type_id=124134025"],
  ["CSS-ish token starting with .", ".btn:active"],
  ["CSS-ish token starting with :", ":hover"],
  ["URL scheme", "https://github.com/car1zhang/kraken"],
  ["a bare // with no scheme", "internal//api/board.go:37"],
  ["brace set", "core/{a,b}.py"],
  ["glob star", "internal/api/*"],
  ["glob question mark", "internal/api/bo?rd.go:37"],
  ["internal whitespace before the position suffix", "internal/api board.go:37"],
  ["a path token embedded in a code snippet", "- path: ../ios/Kraken/Models.swift"],
  ["leading /", "/internal/api/board.go:37"],
  ["leading .. segment", "../ios/Kraken/Models.swift"],
  ["interior .. segment", "internal/../etc/board.go:37"],
  ["set-artifact filename", "51-code-map.md"],
  ["set-artifact filename, second form", "90-build-report.md"],
  ["bare extensionless basename", "Makefile"],
  ["basename with an extension but NO line", "cell.go"],
];

describe("parseCodeRef refuses every non-ref the contract names", () => {
  for (const [clause, span] of edgeRejects) {
    it(`returns null for ${clause}: ${span}`, () => {
      expect(parseCodeRef(span)).toBeNull();
    });
  }
});

describe("inlineMD leaves every rejected span byte-identical to HEAD", () => {
  // The whole compatibility claim of slice W: a span the parser refuses must
  // render as the renderer rendered it before this feature existed —
  // `<code>` + the escaped text, no class, no attribute, no href. Comparing
  // against esc() (the renderer's own escaper) rather than a hand-written
  // literal keeps this honest for spans carrying HTML-significant bytes.
  for (const [clause, span] of edgeRejects) {
    it(`renders ${clause} as a bare <code>`, () => {
      expect(inlineMD("`" + span + "`")).toBe("<code>" + esc(span) + "</code>");
    });
  }

  it("emits no href and no scheme for any rejected span", () => {
    for (const [, span] of edgeRejects) {
      const html = inlineMD("`" + span + "`");
      expect(html).not.toContain("href");
      expect(html).not.toContain("data-code");
    }
  });
});

// --- G4: the leading token, and nothing after it ----------------------------

describe("G4 captures the leading token only", () => {
  it("splits a compiler diagnostic at the first ': ' after the position suffix", () => {
    expect(parseCodeRef("ios/Kraken/BoardView.swift:794:25: error: no such member")).toBe(
      "ios/Kraken/BoardView.swift:794:25",
    );
  });

  it("splits the same way when the position suffix is a bare line", () => {
    expect(parseCodeRef("web/serve.go:225: note: click to open in Zed")).toBe(
      "web/serve.go:225",
    );
  });

  it("renders the whole span as the element text while linking only the token", () => {
    // E is the ESCAPED SPAN TEXT — the prose tail stays visible and plain
    // inside the same element; only the attribute is trimmed.
    expect(inlineMD("`ios/K/B.swift:794:25: error: bad`")).toBe(
      '<code class="code-ref" data-code="ios/K/B.swift:794:25">' +
        "ios/K/B.swift:794:25: error: bad" +
        "</code>",
    );
  });

  it("does NOT rescue a span whose whitespace comes BEFORE any position suffix", () => {
    // the contrast case for the rule above: whitespace inside the token is a
    // refusal, whitespace after the suffix is a split
    expect(parseCodeRef("internal/api board.go:37")).toBeNull();
  });
});

describe("the set-artifact refusal is anchored, exactly as written", () => {
  it("refuses the bare artifact name", () => {
    expect(parseCodeRef("51-code-map.md")).toBeNull();
  });

  it("does not refuse a real repo path that merely ENDS in one", () => {
    // `^\d\d-[^/]*\.md$` is anchored at both ends and excludes `/`, so a
    // path with a directory in front of it is an ordinary bare path (the
    // file-level accept row) and must survive the artifact-name rule.
    expect(parseCodeRef("artifacts/51-code-map.md")).toBe("artifacts/51-code-map.md");
  });
});

// --- codeRefTarget ----------------------------------------------------------

/** A detached document, the stubbed-doc seam the testing plan names. */
function edgeDoc(html: string): Document {
  const doc = document.implementation.createHTMLDocument("edge-code-refs");
  doc.body.innerHTML = html;
  return doc;
}

function edgeClick(el: Element) {
  el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
}

describe("codeRefTarget refuses everything that is not a live ref", () => {
  it("returns null for an element with no [data-code] ancestor", () => {
    const doc = edgeDoc('<div id="outer"><span id="kid">x</span></div>');
    expect(codeRefTarget(doc.getElementById("kid") as Element)).toBeNull();
    expect(codeRefTarget(doc.getElementById("outer") as Element)).toBeNull();
  });

  it("returns null for an empty attribute value", () => {
    const doc = edgeDoc('<span id="ref" data-code=""><b id="kid">x</b></span>');
    expect(codeRefTarget(doc.getElementById("kid") as Element)).toBeNull();
  });

  it("returns null for every attribute value parseCodeRef would reject", () => {
    for (const [, span] of edgeRejects) {
      const doc = edgeDoc(
        '<span id="ref" data-code="' + esc(span) + '"><b id="kid">x</b></span>',
      );
      expect(codeRefTarget(doc.getElementById("kid") as Element)).toBeNull();
    }
  });
});

// --- wireCodeRefs -----------------------------------------------------------

describe("wireCodeRefs", () => {
  const refMarkup =
    '<div id="wrap">' +
    '<span id="ref" data-code="internal/api/board.go:37"><b id="kid">board</b></span>' +
    '<span id="plain">not a ref</span>' +
    "</div>";

  it("does not double-fire after a second attach, and keeps one style element", () => {
    // the raw-toggle round trip: the frame is rebuilt and wired again. One
    // click must still mean one open.
    const doc = edgeDoc(refMarkup);
    const onOpen = vi.fn();
    wireCodeRefs(doc, onOpen);
    wireCodeRefs(doc, onOpen);

    edgeClick(doc.getElementById("kid") as Element);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(doc.querySelectorAll("#code-ref-style")).toHaveLength(1);
  });

  it("cleanup removes BOTH the listener and the style element", () => {
    const doc = edgeDoc(refMarkup);
    const onOpen = vi.fn();
    const off = wireCodeRefs(doc, onOpen);
    expect(doc.querySelectorAll("#code-ref-style")).toHaveLength(1);

    off();
    expect(doc.querySelectorAll("#code-ref-style")).toHaveLength(0);
    edgeClick(doc.getElementById("kid") as Element);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("a click outside any ref calls nothing", () => {
    const doc = edgeDoc(refMarkup);
    const onOpen = vi.fn();
    wireCodeRefs(doc, onOpen);

    edgeClick(doc.getElementById("plain") as Element);
    expect(onOpen).not.toHaveBeenCalled();
    edgeClick(doc.body);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("a document with no refs decorates nothing and stays inert", () => {
    const doc = edgeDoc('<div id="wrap"><span id="plain">nothing here</span></div>');
    const onOpen = vi.fn();
    const off = wireCodeRefs(doc, onOpen);

    expect(doc.querySelectorAll("[title]")).toHaveLength(0);
    edgeClick(doc.getElementById("plain") as Element);
    expect(onOpen).not.toHaveBeenCalled();
    // and the teardown of a no-op wiring is itself a no-op, not a throw
    expect(() => off()).not.toThrow();
  });
});

// --- component negative space ----------------------------------------------

const edgeMutate = async <T,>(op: () => Promise<T>) => op();

beforeEach(() => {
  vi.clearAllMocks();
  edgeThreadRead.mockResolvedValue({ blocks: [] } as never);
  edgeDream.mockResolvedValue({ reports: [] } as never);
  edgeWatch.mockResolvedValue({ watchlist: [] } as never);
  vi.stubGlobal(
    "fetch",
    vi.fn(() =>
      Promise.resolve({ ok: true, text: () => Promise.resolve("a `Makefile` here") }),
    ),
  );
});

/** The first rendered <code> carrying exactly this text. */
function edgeCodeNamed(text: string): HTMLElement {
  const hit = [...document.querySelectorAll("code")].find(
    (c) => c.textContent === text,
  );
  if (!hit) throw new Error(`no <code> rendered with text ${JSON.stringify(text)}`);
  return hit as HTMLElement;
}

describe("a plain <code> is not a click target", () => {
  it("clicking an unlinkified span in the artifact viewer calls nothing", async () => {
    render(
      createElement(ArtifactOverlay, {
        open: true,
        url: "/api/artifact/j/artifacts/00-overview.md",
        name: "j/00-overview.md",
        job: makeJob({
          slug: "j",
          artifacts: [
            { name: "00-overview.md", url: "/api/artifact/j/artifacts/00-overview.md" },
          ],
        }),
        onNavigate: () => {},
        onAskBranched: () => {},
        onClose: () => {},
        // the library slice of the integrator. Required props, but this call
        // site is cast `as never`, so omitting them is a RUNTIME crash rather
        // than a compile error — the star reads the list on the first render.
        library: [],
        onSaveArtifact: async () => {},
        onRemoveSaved: async () => {},
        mutate: edgeMutate,
      } as never),
    );

    await waitFor(() => expect(edgeCodeNamed("Makefile")).toBeTruthy());
    const code = edgeCodeNamed("Makefile");
    // `Makefile` is a rejection row: the marker class must be absent…
    expect(code.className).not.toContain("code-ref");
    expect(code.hasAttribute("data-code")).toBe(false);
    // …and the surface's own delegated listener must not fire on it
    edgeClick(code);
    await new Promise((r) => setTimeout(r, 0));
    expect(edgeOpen).not.toHaveBeenCalled();
  });
});

describe("thread and dream surfaces carry the markup and NO wiring", () => {
  it("a code ref in a thread block is inert", async () => {
    edgeThreadRead.mockResolvedValue({
      blocks: [{ kind: "text", text: "see `internal/api/board.go:37` for the gate" }],
    } as never);

    render(
      createElement(ThreadOverlay, {
        open: true,
        slug: "j",
        tid: "t1",
        job: makeJob({
          slug: "j",
          state: "working",
          live: true,
          threads: [
            { tid: "t1", title: "gate", state: "live", pane: "%7", created_at: 0 },
          ],
        }),
        onClose: () => {},
        onBranched: () => {},
        mutate: edgeMutate,
      } as never),
    );

    await waitFor(() =>
      expect(document.querySelector("code.code-ref")).not.toBeNull(),
    );
    edgeClick(document.querySelector("code.code-ref") as Element);
    await new Promise((r) => setTimeout(r, 0));
    expect(edgeOpen).not.toHaveBeenCalled();
  });

  it("a code ref in a dream report is inert", async () => {
    edgeDream.mockResolvedValue({
      reports: [
        {
          date: "2026-07-26",
          slug: "dream-2026-07-26",
          body: "# night\n\nthe gate is at `internal/api/board.go:37` today\n",
        },
      ],
    } as never);

    render(
      createElement(DreamView, {
        active: true,
        mutate: edgeMutate,
        onToast: () => {},
      } as never),
    );

    await waitFor(() =>
      expect(document.querySelector("code.code-ref")).not.toBeNull(),
    );
    edgeClick(document.querySelector("code.code-ref") as Element);
    await new Promise((r) => setTimeout(r, 0));
    expect(edgeOpen).not.toHaveBeenCalled();
  });
});
