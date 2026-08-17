// codeRefs.contract.test.ts — blind CONTRACT tests for Slice W of the
// clickable-code-refs feature (50-contract.md).
//
// Authored from the frozen contract alone, before any implementation existed:
// every assertion below transcribes a stated obligation, and nothing here was
// derived from reading codeRefs.ts, markdown.ts, client.ts, ArtifactOverlay.tsx
// or SteerOverlay.tsx. Every package-level identifier is prefixed `contract` so
// the edge author sharing this directory cannot collide with it.
//
// No JSX: the contract fixes this file's NAME (`codeRefs.contract.test.ts`), and
// a `.ts` file cannot carry JSX, so the two component-wiring suites drive React
// through `createElement` instead. That is a syntax difference only — the
// components are mounted exactly as ArtifactOverlay.test.tsx mounts them.
//
// The client module is mocked by SPREADING the real one and overriding only the
// two functions these tests observe. A wholesale factory (the idiom the overlay
// tests use) would have to enumerate every export the two overlays import, and a
// blind author has no license to know that list — the spread keeps the mock
// honest about everything it is not asserting.

import { fireEvent, render, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WebJob } from "../api/types.ts";
import { makeJob } from "../test/fixtures.ts";
import { codeRefTarget, parseCodeRef, wireCodeRefs } from "./codeRefs.ts";
import { inlineMD, renderMD } from "./markdown.ts";

vi.mock("../api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  openInEditor: vi.fn(() => Promise.resolve({ ok: true })),
  // the steer overlay's spec drawer only mounts once /api/job/form answers with
  // a non-empty `spec`; mocked so the spec SURFACE exists to be clicked
  fetchJobForm: vi.fn(() =>
    Promise.resolve({
      slug: "j",
      mtime: 0,
      spec: "the entrypoint is `web/serve.go:225` today",
    }),
  ),
}));

import { openInEditor } from "../api/client.ts";
import { ArtifactOverlay } from "../components/overlays/ArtifactOverlay.tsx";
import { SteerOverlay } from "../components/overlays/SteerOverlay.tsx";

const contractMutate = async <T,>(op: () => Promise<T>) => op();

/**
 * A document's body, created if jsdom left it out.
 *
 * A fresh iframe's contentDocument can arrive with no documentElement at all —
 * ArtifactOverlay.test.tsx guards the same reality with `doc.body ?? doc` — and
 * `wireCodeRefs` is specified against a whole Document, so the fixture has to
 * hand it a real html/head/body rather than a bare Document node.
 */
function contractBody(doc: Document): HTMLElement {
  if (doc.body) return doc.body;
  const root =
    doc.documentElement ?? doc.appendChild(doc.createElement("html"));
  if (!doc.head) root.appendChild(doc.createElement("head"));
  return root.appendChild(doc.createElement("body"));
}

/**
 * A standalone document, so a wiring test never decorates the test runner's
 * own.
 *
 * Hosted in a real iframe rather than built with
 * `document.implementation.createHTMLDocument`: that call yields a document
 * with NO defaultView, and an event cannot be dispatched into a document that
 * has no window. An iframe's document has one — and it is also the realm the
 * feature actually runs in.
 */
function contractDoc(html: string): Document {
  const frame = document.createElement("iframe");
  document.body.appendChild(frame);
  contractHosts.push(frame);
  const doc = frame.contentDocument as Document;
  contractBody(doc).innerHTML = html;
  return doc;
}

/**
 * Every host frame contractDoc has parked on the test document.
 *
 * RTL's cleanup only removes ITS container, so without this the hosts pile up
 * ahead of the render root and a bare `document.querySelector("iframe")` in a
 * later test resolves to a leftover fixture instead of the overlay's artifact
 * frame.
 */
const contractHosts: HTMLIFrameElement[] = [];

/** The arguments openInEditor was handed on its Nth call, slug/path/line only. */
function contractOpenArgs(n = 0): unknown[] {
  return vi.mocked(openInEditor).mock.calls[n]?.slice(0, 3) ?? [];
}

function contractArtifact(url: string, job: WebJob) {
  return createElement(ArtifactOverlay, {
    open: true,
    url,
    name: `j/${url.split("/").pop()}`,
    job,
    onNavigate: () => {},
    onAskBranched: () => {},
    onClose: () => {},
    // the library slice of the integrator: this file is about code refs inside
    // the rendered body, so the head's ☆ save is wired to nothing on purpose
    library: [],
    onSaveArtifact: async () => {},
    onRemoveSaved: async () => {},
    mutate: contractMutate,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  contractHosts.splice(0).forEach((f) => f.remove());
});

// ---------------------------------------------------------------------------
// parseCodeRef — the G-row acceptances, verbatim from the contract's table.
// ---------------------------------------------------------------------------

describe("contract: parseCodeRef accepts", () => {
  // The contract states the returned value for each of these EXACTLY: it is
  // "the leading path token verbatim (no rewriting)". So each row asserts an
  // identity, not a shape.
  it.each([
    ["internal/api/board.go:37", "internal/api/board.go:37"],
    ["web/static/app.js:1375-1442", "web/static/app.js:1375-1442"],
    ["BoardView.swift:794:25", "BoardView.swift:794:25"],
    ["ios/K/B.swift:794:25: error: cannot find 'x'", "ios/K/B.swift:794:25"],
    ["web/_ui/src/App.tsx", "web/_ui/src/App.tsx"],
    ["cell.go:553", "cell.go:553"],
  ])("%s → %s", (span, want) => {
    expect(parseCodeRef(span)).toBe(want);
  });
});

describe("contract: parseCodeRef rejects", () => {
  // Every row the contract lists as a non-ref. Each is `null`, not "" and not
  // the input — the caller branches on null.
  it.each([
    ["host:port", "127.0.0.1:7777"],
    ["a clock time", "16:08:11"],
    ["a timestamp", "2026-07-30 19:47"],
    ["a key:value id", "org_id:123"],
    ["a key=value id", "job_type_id=124134025"],
    ["a css pseudo-class on a class", ".btn:active"],
    ["a bare css pseudo-class", ":hover"],
    ["a url with a scheme", "https://github.com/ProbookAI/kraken"],
    ["a protocol-relative path", "//example.com/x.go:3"],
    ["a brace set", "core/{a,b}.py"],
    ["a glob star", "internal/api/*"],
    ["a glob question mark", "internal/api/bo?rd.go:37"],
    ["internal whitespace", "internal/api/bo ard.go:37"],
    ["an absolute path", "/etc/hosts.go:12"],
    ["a parent segment", "../ios/Kraken/Models.swift"],
    ["a set-artifact filename", "51-code-map.md"],
    ["another set-artifact filename", "90-build-report.md"],
    ["a bare basename with no line", "Makefile"],
  ])("%s (%s)", (_why, span) => {
    expect(parseCodeRef(span)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// inlineMD — the wrapper, and the byte-identical passthrough.
// ---------------------------------------------------------------------------

describe("contract: inlineMD wraps an accepted span", () => {
  it("emits class + data-code in the contract's exact shape", () => {
    expect(inlineMD("`internal/api/board.go:37`")).toBe(
      '<code class="code-ref" data-code="internal/api/board.go:37">internal/api/board.go:37</code>',
    );
  });

  it("carries the leading token in data-code and the WHOLE span as text (G4)", () => {
    // V is the parsed value; E is the escaped span text — the trailing prose is
    // still displayed, it is just not part of the ref.
    expect(inlineMD("`ios/K/B.swift:794:25: error: bad`")).toBe(
      '<code class="code-ref" data-code="ios/K/B.swift:794:25">ios/K/B.swift:794:25: error: bad</code>',
    );
  });

  it("wraps a bare path with an extension, file-level", () => {
    expect(inlineMD("`web/_ui/src/App.tsx`")).toBe(
      '<code class="code-ref" data-code="web/_ui/src/App.tsx">web/_ui/src/App.tsx</code>',
    );
  });

  it("emits no href and no scheme", () => {
    const html = inlineMD("`internal/api/board.go:37`");
    expect(html).not.toContain("href");
    expect(html).not.toMatch(/[a-z]+:\/\//);
    expect(html).not.toContain("<a ");
  });
});

describe("contract: inlineMD leaves a rejected span byte-identical", () => {
  it.each([
    ["`127.0.0.1:7777`", "<code>127.0.0.1:7777</code>"],
    ["`16:08:11`", "<code>16:08:11</code>"],
    ["`org_id:123`", "<code>org_id:123</code>"],
    ["`.btn:active`", "<code>.btn:active</code>"],
    ["`51-code-map.md`", "<code>51-code-map.md</code>"],
    ["`Makefile`", "<code>Makefile</code>"],
    ["`internal/api/*`", "<code>internal/api/*</code>"],
    // the escape-before-wrap ordering is the safety argument for
    // dangerouslySetInnerHTML and must survive this rule untouched
    ["`<b>`", "<code>&lt;b&gt;</code>"],
  ])("%s stays %s", (src, want) => {
    expect(inlineMD(src)).toBe(want);
  });
});

describe("contract: the linker never scans prose", () => {
  it("leaves an unbackticked path:line as plain text", () => {
    expect(inlineMD("see web/serve.go:225 for the justification")).toBe(
      "see web/serve.go:225 for the justification",
    );
  });

  it("leaves a fenced block untouched", () => {
    expect(renderMD("```\ninternal/api/board.go:37\n```")).toBe(
      "<pre><code>internal/api/board.go:37</code></pre>",
    );
  });
});

// ---------------------------------------------------------------------------
// codeRefTarget — the ancestor walk and the range→start-line parse.
// ---------------------------------------------------------------------------

describe("contract: codeRefTarget", () => {
  it("parses path + line", () => {
    const doc = contractDoc('<span data-code="internal/api/board.go:37"></span>');
    expect(codeRefTarget(doc.querySelector("span") as Element)).toEqual({
      path: "internal/api/board.go",
      line: 37,
    });
  });

  it("takes the START of a range as the line", () => {
    const doc = contractDoc('<span data-code="web/static/app.js:1375-1442"></span>');
    expect(codeRefTarget(doc.querySelector("span") as Element)).toEqual({
      path: "web/static/app.js",
      line: 1375,
    });
  });

  it("parses path + line + col", () => {
    const doc = contractDoc('<span data-code="BoardView.swift:794:25"></span>');
    expect(codeRefTarget(doc.querySelector("span") as Element)).toEqual({
      path: "BoardView.swift",
      line: 794,
      col: 25,
    });
  });

  it("parses a bare path with no line", () => {
    const doc = contractDoc('<span data-code="web/_ui/src/App.tsx"></span>');
    expect(codeRefTarget(doc.querySelector("span") as Element)).toEqual({
      path: "web/_ui/src/App.tsx",
    });
  });

  it("walks UP to the nearest [data-code] ancestor", () => {
    const doc = contractDoc(
      '<g data-code="internal/api/board.go:37"><rect><tspan id="deep"></tspan></rect></g>',
    );
    expect(codeRefTarget(doc.getElementById("deep") as Element)).toEqual({
      path: "internal/api/board.go",
      line: 37,
    });
  });

  it("is null with no attribute anywhere up the tree", () => {
    const doc = contractDoc("<div><span id='x'></span></div>");
    expect(codeRefTarget(doc.getElementById("x") as Element)).toBeNull();
  });

  it("is null for a value parseCodeRef would reject", () => {
    const doc = contractDoc('<span data-code="127.0.0.1:7777"></span>');
    expect(codeRefTarget(doc.querySelector("span") as Element)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// wireCodeRefs — decorate, single style element, one delegated click, cleanup.
// ---------------------------------------------------------------------------

describe("contract: wireCodeRefs", () => {
  it("titles every valid ref and injects exactly one style element", () => {
    const doc = contractDoc(
      '<g id="box" data-code="internal/api/board.go:37"><rect id="r"></rect></g>',
    );
    wireCodeRefs(doc, () => {});

    const box = doc.getElementById("box") as Element;
    expect(box.hasAttribute("title")).toBe(true);
    expect(box.getAttribute("title")).toContain("internal/api/board.go:37");

    const styles = doc.querySelectorAll("#code-ref-style");
    expect(styles).toHaveLength(1);
    const css = styles[0]?.textContent ?? "";
    expect(css).toContain("[data-code]");
    expect(css).toContain("code.code-ref");
    expect(css).toContain("pointer");
  });

  it("fires onOpen once for a click on a descendant of a ref", () => {
    const onOpen = vi.fn();
    const doc = contractDoc(
      '<g data-code="internal/api/board.go:37"><rect id="r"></rect></g>',
    );
    wireCodeRefs(doc, onOpen);
    fireEvent.click(doc.getElementById("r") as Element);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith({
      path: "internal/api/board.go",
      line: 37,
    });
  });

  it("fires nothing for a click outside any ref", () => {
    const onOpen = vi.fn();
    const doc = contractDoc(
      '<g data-code="internal/api/board.go:37"></g><p id="prose">x</p>',
    );
    wireCodeRefs(doc, onOpen);
    fireEvent.click(doc.getElementById("prose") as Element);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("is idempotent: a second wiring double-fires nothing and adds no style", () => {
    const onOpen = vi.fn();
    const doc = contractDoc(
      '<g data-code="internal/api/board.go:37"><rect id="r"></rect></g>',
    );
    wireCodeRefs(doc, onOpen);
    wireCodeRefs(doc, onOpen);
    fireEvent.click(doc.getElementById("r") as Element);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(doc.querySelectorAll("#code-ref-style")).toHaveLength(1);
  });

  it("the returned function removes the listener AND the style element", () => {
    const onOpen = vi.fn();
    const doc = contractDoc(
      '<g data-code="internal/api/board.go:37"><rect id="r"></rect></g>',
    );
    const off = wireCodeRefs(doc, onOpen);
    off();
    fireEvent.click(doc.getElementById("r") as Element);
    expect(onOpen).not.toHaveBeenCalled();
    expect(doc.getElementById("code-ref-style")).toBeNull();
  });

  it("survives a document with no refs at all", () => {
    const onOpen = vi.fn();
    const doc = contractDoc("<p id='prose'>nothing to see</p>");
    expect(() => wireCodeRefs(doc, onOpen)).not.toThrow();
    fireEvent.click(doc.getElementById("prose") as Element);
    expect(onOpen).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// openInEditor — the REAL client function (importActual past this file's mock).
// ---------------------------------------------------------------------------

describe("contract: openInEditor", () => {
  async function contractRealOpen() {
    const actual = await vi.importActual<{
      openInEditor: (
        slug: string,
        path: string,
        line?: number,
        col?: number,
      ) => Promise<unknown>;
    }>("../api/client.ts");
    return actual.openInEditor;
  }

  function contractCaptureFetch() {
    const spy = vi.fn(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ ok: true }),
      }),
    );
    vi.stubGlobal("fetch", spy);
    return spy;
  }

  it("POSTs to /api/editor/open with slug, path, line and col", async () => {
    const spy = contractCaptureFetch();
    const open = await contractRealOpen();
    await open("j", "internal/api/board.go", 37, 25);

    const [url, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/editor/open");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      slug: "j",
      path: "internal/api/board.go",
      line: 37,
      col: 25,
    });
  });

  it("omits line and col entirely when they are undefined", async () => {
    const spy = contractCaptureFetch();
    const open = await contractRealOpen();
    await open("j", "web/_ui/src/App.tsx");

    const [, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      slug: "j",
      path: "web/_ui/src/App.tsx",
    });
  });

  it("omits col alone when only the line is known", async () => {
    const spy = contractCaptureFetch();
    const open = await contractRealOpen();
    await open("j", "internal/api/board.go", 37);

    const [, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      slug: "j",
      path: "internal/api/board.go",
      line: 37,
    });
  });
});

// ---------------------------------------------------------------------------
// Component wiring — ArtifactOverlay's markdown body and its artifact iframe.
// ---------------------------------------------------------------------------

describe("contract: ArtifactOverlay markdown body", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve({
          ok: true,
          text: () =>
            Promise.resolve(
              "the board handler is `internal/api/board.go:37` and the daemon " +
                "listens on `127.0.0.1:7777` today",
            ),
        }),
      ),
    );
  });

  it("a click on a code.code-ref opens it with the artifact's slug", async () => {
    render(
      contractArtifact(
        "/api/artifact/j/artifacts/00-overview.md",
        makeJob({ slug: "j" }),
      ),
    );
    const ref = await waitFor(() => {
      const el = document.querySelector("#panel-body code.code-ref");
      if (!el) throw new Error("no linkified code ref rendered yet");
      return el;
    });

    fireEvent.click(ref);
    expect(openInEditor).toHaveBeenCalledTimes(1);
    expect(contractOpenArgs()).toEqual(["j", "internal/api/board.go", 37]);
  });

  it("a click on a plain <code> opens nothing", async () => {
    render(
      contractArtifact(
        "/api/artifact/j/artifacts/00-overview.md",
        makeJob({ slug: "j" }),
      ),
    );
    const plain = await waitFor(() => {
      const els = Array.from(
        document.querySelectorAll("#panel-body code:not(.code-ref)"),
      );
      const hit = els.find((e) => e.textContent === "127.0.0.1:7777");
      if (!hit) throw new Error("no plain code span rendered yet");
      return hit;
    });

    fireEvent.click(plain);
    expect(openInEditor).not.toHaveBeenCalled();
  });
});

describe("contract: ArtifactOverlay artifact iframe", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve({ ok: true, text: () => Promise.resolve("<svg/>") }),
      ),
    );
  });

  /**
   * The artifact iframe, scoped to the overlay's own body — a bare
   * `document.querySelector("iframe")` would also match a contractDoc host.
   */
  function contractFrame(): HTMLIFrameElement | null {
    return document.querySelector("#panel-body iframe");
  }

  /** Paints a diagram fragment into the frame's own document and loads it. */
  function contractLoadFrame(): { doc: Document; frame: HTMLIFrameElement } {
    const frame = contractFrame() as HTMLIFrameElement;
    const doc = frame.contentDocument as Document;
    contractBody(doc).innerHTML =
      '<svg><g id="box" data-code="internal/api/board.go:37">' +
      '<rect id="r"></rect></g></svg>';
    fireEvent.load(frame);
    return { doc, frame };
  }

  it("decorates and wires the frame's document on load", () => {
    render(
      contractArtifact(
        "/api/artifact/j/artifacts/10-flow.html",
        makeJob({ slug: "j" }),
      ),
    );
    const { doc } = contractLoadFrame();

    expect((doc.getElementById("box") as Element).hasAttribute("title")).toBe(true);
    expect(doc.querySelectorAll("#code-ref-style")).toHaveLength(1);

    fireEvent.click(doc.getElementById("r") as Element);
    expect(openInEditor).toHaveBeenCalledTimes(1);
    expect(contractOpenArgs()).toEqual(["j", "internal/api/board.go", 37]);
  });

  it("a repeated load does not double-fire", () => {
    render(
      contractArtifact(
        "/api/artifact/j/artifacts/10-flow.html",
        makeJob({ slug: "j" }),
      ),
    );
    const { doc, frame } = contractLoadFrame();
    fireEvent.load(frame);
    fireEvent.load(frame);

    expect(doc.querySelectorAll("#code-ref-style")).toHaveLength(1);
    fireEvent.click(doc.getElementById("r") as Element);
    expect(openInEditor).toHaveBeenCalledTimes(1);
  });

  it("re-wires the frame rebuilt by a raw-toggle round trip", async () => {
    render(
      contractArtifact(
        "/api/artifact/j/artifacts/10-flow.html",
        makeJob({ slug: "j" }),
      ),
    );
    contractLoadFrame();

    // raw ON tears the iframe down for a text body, raw OFF builds a BRAND NEW
    // frame — the case the contract names, and the one a `hooked.current`
    // guard keyed on the old document would silently skip.
    const toggle = document.getElementById("panel-raw") as HTMLElement;
    fireEvent.click(toggle);
    await waitFor(() => {
      if (contractFrame()) throw new Error("frame still up");
    });
    fireEvent.click(toggle);
    const frame = await waitFor(() => {
      const f = contractFrame();
      if (!f) throw new Error("frame not rebuilt yet");
      return f;
    });

    const doc = frame.contentDocument as Document;
    contractBody(doc).innerHTML =
      '<svg><g data-code="internal/api/board.go:37"><rect id="r2"></rect></g></svg>';
    fireEvent.load(frame);

    fireEvent.click(doc.getElementById("r2") as Element);
    expect(openInEditor).toHaveBeenCalledTimes(1);
    expect(contractOpenArgs()).toEqual(["j", "internal/api/board.go", 37]);
  });
});

// ---------------------------------------------------------------------------
// Component wiring — SteerOverlay's detail and spec surfaces.
// ---------------------------------------------------------------------------

describe("contract: SteerOverlay detail and spec surfaces", () => {
  function contractSteer(job: WebJob) {
    return createElement(SteerOverlay, {
      open: true,
      slug: "j",
      mode: "steer",
      job,
      onClose: () => {},
      onOpenArtifact: () => {},
      onOpenThread: () => {},
      mutate: contractMutate,
    } as never);
  }

  it("the detail surface opens a ref with the overlay's own job slug", async () => {
    render(
      contractSteer(
        makeJob({
          slug: "j",
          state: "working",
          live: true,
          detail: "stuck on `internal/api/board.go:37`",
        }),
      ),
    );
    const ref = await waitFor(() => {
      const el = document.querySelector("#steer-detail code.code-ref");
      if (!el) throw new Error("detail ref not rendered yet");
      return el;
    });

    fireEvent.click(ref);
    expect(openInEditor).toHaveBeenCalledTimes(1);
    expect(contractOpenArgs()).toEqual(["j", "internal/api/board.go", 37]);
  });

  it("the spec surface opens a ref with the overlay's own job slug", async () => {
    render(contractSteer(makeJob({ slug: "j", state: "working", live: true })));

    const toggle = await waitFor(() => {
      const el = document.getElementById("steer-spec-toggle");
      if (!el) throw new Error("spec drawer not mounted yet");
      return el;
    });
    fireEvent.click(toggle);

    const ref = await waitFor(() => {
      const el = document.querySelector("#steer-spec-body code.code-ref");
      if (!el) throw new Error("spec ref not rendered yet");
      return el;
    });

    fireEvent.click(ref);
    expect(openInEditor).toHaveBeenCalledTimes(1);
    expect(contractOpenArgs()).toEqual(["j", "web/serve.go", 225]);
  });
});
