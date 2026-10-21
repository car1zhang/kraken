import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fetchNodeThreadRead, fetchNodeTranscript } from "../api/client.ts";
import * as ArtifactOverlayModule from "../components/overlays/ArtifactOverlay.tsx";
import { artifactParts } from "../components/overlays/ArtifactOverlay.tsx";
import { stripFrontmatter } from "./fleet.ts";

// fleetparity.edge.test.ts — the EDGE dimension of 50-contract.md's
// "Round 2 — remote parity", slice R2-W's pure rules and client URLs:
// stripFrontmatter on empty input, a lone fence, CRLF, an unclosed fence and
// a fence that is not the first line; fetchNodeThreadRead omitting `n`;
// awkward characters in every encoded query value; artifactParts refusing the
// proxy URL shape; isHtmlPage's true/false pair. Written blind from the
// contract, with no sight of lib/fleet.ts, api/client.ts or
// ArtifactOverlay.tsx. Happy paths are athena-logic's.
//
// Every helper is prefixed `edgeFP`.

describe("stripFrontmatter", () => {
  // `stripFrontmatter`: "`""` → `""`."
  it("returns the empty string for the empty string", () => {
    expect(stripFrontmatter("")).toBe("");
  });

  // "When `text` starts with `---\n` (or `---\r\n`) and a later line is
  // exactly `---` … otherwise returns `text` unchanged."
  it("leaves text without a complete leading fence unchanged", () => {
    for (const text of [
      "---", // a lone fence, no newline
      "---\n", // an opening line and nothing after
      "---\r\n",
      "---\nx: 1\n", // never closed
      "---\nx: 1\nbody text", // never closed, body follows
      "--- \nx: 1\n---\nbody", // the opening line is not exactly ---
      " ---\nx: 1\n---\nbody", // indented opening
      "----\nx: 1\n---\nbody", // four dashes is not a fence
      "---\nx: 1\n--- \nbody", // the closing line carries a trailing space
      "---\nx: 1\n----\nbody", // four dashes does not close
      "---\nx: 1\n ---\nbody", // an indented closing line does not close
      "# Title\n---\nx: 1\n---\nbody", // the fence is not the first line
      "\n---\nx: 1\n---\nbody", // a blank line before the fence
      "plain spec\n", // no fence at all
    ]) {
      expect(stripFrontmatter(text), JSON.stringify(text)).toBe(text);
    }
  });

  // "returns everything after that closing line with one leading newline
  // run trimmed" — the contract's own Swift-twin vector.
  it("strips the contract's own vector", () => {
    expect(stripFrontmatter("---\nx: 1\n---\n\n# Spec")).toBe("# Spec");
  });

  // "(or `---\r\n`) … a later line is exactly `---` (trailing `\r`
  // allowed)" — a CRLF document; the newline run after the closing line is
  // `\r\n`, read as a newline run (listed as a silence).
  it("strips a CRLF document", () => {
    expect(stripFrontmatter("---\r\nx: 1\r\n---\r\n\r\n# Spec")).toBe("# Spec");
    expect(stripFrontmatter("---\r\nx: 1\r\n---\r\n# Spec")).toBe("# Spec");
  });

  // "trailing `\r` allowed" on the closing line of an otherwise LF document.
  it("accepts a closing line with a trailing carriage return", () => {
    expect(stripFrontmatter("---\nx: 1\n---\r\nbody")).toBe("body");
  });

  // "one leading newline run trimmed" — every leading newline of the run
  // goes, and nothing past the first non-newline character is touched.
  it("trims one leading newline run and keeps the rest verbatim", () => {
    expect(stripFrontmatter("---\nx: 1\n---\n\n\n\n# Spec\n\n  indented\n")).toBe("# Spec\n\n  indented\n");
    expect(stripFrontmatter("---\nx: 1\n---\n  # Spec")).toBe("  # Spec");
  });

  // the FIRST later `---` line closes; a horizontal rule in the body stays.
  it("closes on the first fence and keeps a later --- in the body", () => {
    expect(stripFrontmatter("---\nx: 1\n---\nbody\n---\nmore")).toBe("body\n---\nmore");
  });

  // an empty frontmatter block is still a block.
  it("strips an empty frontmatter block", () => {
    expect(stripFrontmatter("---\n---\nbody")).toBe("body");
  });

  // a closing fence at end of text with nothing after it leaves nothing.
  it("returns the empty string when the closing fence is the last line", () => {
    expect(stripFrontmatter("---\nx: 1\n---")).toBe("");
    expect(stripFrontmatter("---\nx: 1\n---\n")).toBe("");
    expect(stripFrontmatter("---\nx: 1\n---\n\n\n")).toBe("");
  });
});

// ---------------------------------------------------------------------------
// client URLs
// ---------------------------------------------------------------------------

function edgeFPStubFetch(body: unknown) {
  const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function edgeFPCall(fetchMock: ReturnType<typeof edgeFPStubFetch>): { url: URL; raw: string; method: string } {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit | undefined];
  const raw = String(url);
  return { url: new URL(raw, "http://kraken.test"), raw, method: (init?.method ?? "GET").toUpperCase() };
}

const edgeFPAwkward = { node: "n/1 #x&y=z", slug: "a b#c&d", tid: "t/1 ?#" };

describe("fetchNodeThreadRead", () => {
  beforeEach(() => vi.unstubAllGlobals());
  afterEach(() => vi.unstubAllGlobals());

  // "plus `&n=<n>` only when `n` is given" — not given: no `n` key at all.
  it("omits n when it is not given", async () => {
    const f = edgeFPStubFetch({ blocks: [] });
    await fetchNodeThreadRead("nA", "s", "t1");
    const c = edgeFPCall(f);
    expect(c.url.pathname).toBe("/api/nodes/thread/read");
    expect(c.url.searchParams.has("n")).toBe(false);
    expect([...c.url.searchParams.keys()]).toEqual(["node", "slug", "tid"]);
    expect(c.raw).not.toMatch(/[?&]n=/);
    expect(c.method).toBe("GET");
  });

  // "only when `n` is given" — 0 is given (listed as a silence: the literal
  // reading is `n !== undefined`, not truthiness).
  it("carries n=0 when 0 is given", async () => {
    const f = edgeFPStubFetch({ blocks: [] });
    await fetchNodeThreadRead("nA", "s", "t1", 0);
    expect(edgeFPCall(f).url.searchParams.get("n")).toBe("0");
  });

  // "`node=<enc>&slug=<enc>&tid=<enc>`" — awkward values round-trip and
  // never split the query or start a fragment.
  it("encodes node, slug and tid", async () => {
    const f = edgeFPStubFetch({ blocks: [] });
    await fetchNodeThreadRead(edgeFPAwkward.node, edgeFPAwkward.slug, edgeFPAwkward.tid, 7);
    const c = edgeFPCall(f);
    expect(c.url.pathname).toBe("/api/nodes/thread/read");
    expect(c.url.hash).toBe("");
    expect(c.url.searchParams.get("node")).toBe(edgeFPAwkward.node);
    expect(c.url.searchParams.get("slug")).toBe(edgeFPAwkward.slug);
    expect(c.url.searchParams.get("tid")).toBe(edgeFPAwkward.tid);
    expect(c.url.searchParams.get("n")).toBe("7");
    expect([...c.url.searchParams.keys()]).toEqual(["node", "slug", "tid", "n"]);
  });
});

describe("fetchNodeTranscript", () => {
  beforeEach(() => vi.unstubAllGlobals());
  afterEach(() => vi.unstubAllGlobals());

  // "`GET /api/nodes/transcript?node=<enc>&slug=<enc>&turns=<n>`".
  it("encodes node and slug and carries turns", async () => {
    const f = edgeFPStubFetch({ session: "", blocks: [] });
    await fetchNodeTranscript(edgeFPAwkward.node, edgeFPAwkward.slug, 0);
    const c = edgeFPCall(f);
    expect(c.url.pathname).toBe("/api/nodes/transcript");
    expect(c.url.hash).toBe("");
    expect(c.url.searchParams.get("node")).toBe(edgeFPAwkward.node);
    expect(c.url.searchParams.get("slug")).toBe(edgeFPAwkward.slug);
    expect(c.url.searchParams.get("turns")).toBe("0");
    expect([...c.url.searchParams.keys()]).toEqual(["node", "slug", "turns"]);
    expect(c.method).toBe("GET");
  });
});

// ---------------------------------------------------------------------------
// artifactParts / isHtmlPage
// ---------------------------------------------------------------------------

describe("artifactParts", () => {
  // "returns null for any URL that does not begin `/api/artifact/` (so a
  // `/api/nodes/artifact?…` URL has no save parts …)".
  it("returns null for every proxy URL shape", () => {
    for (const url of [
      "/api/nodes/artifact?node=n&slug=s&name=00-overview.md",
      "/api/nodes/artifact?node=n%2F1&slug=a%20b&name=dir%2Fsub%2F10-arch.html",
      "/api/nodes/artifact?node=n&slug=s&name=%2Fapi%2Fartifact%2Fj%2Fh%2Fx.md",
      "/api/nodes/artifact?name=/api/artifact/j/h/x.md",
      "/api/node/artifact?slug=s&name=x.md",
      "http://studio:7777/api/artifact/j/h/x.md",
      " /api/artifact/j/h/x.md",
      "/API/artifact/j/h/x.md",
    ]) {
      expect(artifactParts(url), url).toBeNull();
    }
  });
});

describe("isHtmlPage", () => {
  const isHtmlPage = (ArtifactOverlayModule as unknown as { isHtmlPage: (url: string) => boolean }).isHtmlPage;

  // "`isHtmlPage(url)` is true for
  // `/api/nodes/artifact?node=n&slug=s&name=10-arch.html` and false for
  // `…&name=00-overview.md`."
  it("reads the proxy URL's tail", () => {
    expect(typeof isHtmlPage).toBe("function");
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s&name=10-arch.html")).toBe(true);
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s&name=00-overview.md")).toBe(false);
  });

  // the sniff is `/\.html?$/i` — `.htm` and upper case count; an `.html`
  // that is not the tail does not.
  it("follows the sniff's own regex at the tail only", () => {
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s&name=10-ARCH.HTM")).toBe(true);
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s.html&name=00-overview.md")).toBe(false);
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s&name=10-arch.html.md")).toBe(false);
  });
});
