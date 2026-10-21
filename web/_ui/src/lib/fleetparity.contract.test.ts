// fleetparity.contract.test.ts — athena-logic's BLIND contract tests for the
// multiplayer-kraken job's Round 2 ("remote parity"), Slice R2-W, pure rules
// and client URLs. Authored from artifacts/50-contract.md alone; no
// implementation file was read. LOGIC dimension: the stated invariants and
// happy paths. Boundaries, malformed input and refusals are athena-edge's.
//
// Every module-level identifier is prefixed contractFP.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { stripFrontmatter } from "./fleet.ts";
import { fetchNodeThreadRead, fetchNodeTranscript } from "../api/client.ts";
import { artifactParts, isHtmlPage } from "../components/overlays/ArtifactOverlay.tsx";

type ContractFPCall = { url: string; init: RequestInit | undefined };

function contractFPStubFetch(body: unknown, status = 200) {
  const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve({
      ok: status >= 200 && status < 300,
      status,
      statusText: status === 200 ? "OK" : "Error",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function contractFPCall(fetchMock: ReturnType<typeof contractFPStubFetch>): ContractFPCall {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit | undefined];
  return { url: String(url), init };
}

function contractFPMethod(c: ContractFPCall): string {
  return (c.init?.method ?? "GET").toUpperCase();
}

describe("stripFrontmatter", () => {
  // `stripFrontmatter`: "When text starts with ---\n (or ---\r\n) and a later
  // line is exactly --- (trailing \r allowed), returns everything after that
  // closing line with one leading newline run trimmed; otherwise returns text
  // unchanged. "" → ""."
  it("drops the frontmatter block and the blank line after it", () => {
    expect(stripFrontmatter("---\nx: 1\n---\n\n# Spec")).toBe("# Spec");
    expect(stripFrontmatter("---\njob: tech-comms\nrepo: kraken\n---\n\n# Spec\n\nbody\n")).toBe(
      "# Spec\n\nbody\n",
    );
  });

  it("drops a frontmatter block with no blank line after it", () => {
    expect(stripFrontmatter("---\nx: 1\n---\n# Spec")).toBe("# Spec");
  });

  it("trims only the LEADING newline run, never later blank lines", () => {
    expect(stripFrontmatter("---\nx: 1\n---\n\n\n# A\n\n\n# B")).toBe("# A\n\n\n# B");
  });

  it("accepts CRLF line endings on the opening and the closing line", () => {
    expect(stripFrontmatter("---\r\nx: 1\r\n---\r\n\r\n# Spec")).toBe("# Spec");
    expect(stripFrontmatter("---\nx: 1\n---\r\n# Spec")).toBe("# Spec");
  });

  it("returns text without a leading --- line unchanged", () => {
    expect(stripFrontmatter("# Spec\n\nbody\n")).toBe("# Spec\n\nbody\n");
    expect(stripFrontmatter("intro\n---\nx: 1\n---\nrest")).toBe("intro\n---\nx: 1\n---\nrest");
  });

  it("maps the empty string to the empty string", () => {
    expect(stripFrontmatter("")).toBe("");
  });
});

describe("api client — round 2 node reads", () => {
  beforeEach(() => vi.unstubAllGlobals());
  afterEach(() => vi.unstubAllGlobals());

  // `fetchNodeTranscript`: "GET /api/nodes/transcript?node=<enc>&slug=<enc>&turns=<n>
  // via getJSON."
  it("fetchNodeTranscript GETs /api/nodes/transcript with node, slug and turns", async () => {
    const body = { session: "s1", blocks: [{ kind: "asst", text: "working", uuid: "u1" }] };
    const f = contractFPStubFetch(body);
    await expect(fetchNodeTranscript("nSTUDIO77", "tech-comms", 6)).resolves.toEqual(body);
    const c = contractFPCall(f);
    expect(c.url).toBe("/api/nodes/transcript?node=nSTUDIO77&slug=tech-comms&turns=6");
    expect(contractFPMethod(c)).toBe("GET");
  });

  it("fetchNodeTranscript encodes the node and the slug", async () => {
    const f = contractFPStubFetch({ session: "", blocks: [] });
    await fetchNodeTranscript("n/b&c", "a=b", 3);
    const u = new URL(contractFPCall(f).url, "http://kraken.test");
    expect(u.pathname).toBe("/api/nodes/transcript");
    expect(u.searchParams.get("node")).toBe("n/b&c");
    expect(u.searchParams.get("slug")).toBe("a=b");
    expect(u.searchParams.get("turns")).toBe("3");
  });

  // `fetchNodeThreadRead`: "GET /api/nodes/thread/read?node=<enc>&slug=<enc>&tid=<enc>
  // plus &n=<n> only when n is given."
  it("fetchNodeThreadRead GETs /api/nodes/thread/read without n when none is given", async () => {
    const body = { blocks: [{ kind: "user", text: "why?" }], counted: 1, session: "s2" };
    const f = contractFPStubFetch(body);
    await expect(fetchNodeThreadRead("nSTUDIO77", "tech-comms", "t1")).resolves.toEqual(body);
    const c = contractFPCall(f);
    expect(c.url).toBe("/api/nodes/thread/read?node=nSTUDIO77&slug=tech-comms&tid=t1");
    expect(contractFPMethod(c)).toBe("GET");
  });

  it("fetchNodeThreadRead appends n when it is given", async () => {
    const f = contractFPStubFetch({ blocks: [] });
    await fetchNodeThreadRead("nSTUDIO77", "tech-comms", "t1", 20);
    expect(contractFPCall(f).url).toBe("/api/nodes/thread/read?node=nSTUDIO77&slug=tech-comms&tid=t1&n=20");
  });

  it("fetchNodeThreadRead encodes node, slug and tid", async () => {
    const f = contractFPStubFetch({ blocks: [] });
    await fetchNodeThreadRead("n/b", "s&t", "t?1");
    const u = new URL(contractFPCall(f).url, "http://kraken.test");
    expect(u.pathname).toBe("/api/nodes/thread/read");
    expect(u.searchParams.get("node")).toBe("n/b");
    expect(u.searchParams.get("slug")).toBe("s&t");
    expect(u.searchParams.get("tid")).toBe("t?1");
    expect(u.searchParams.has("n")).toBe(false);
  });
});

describe("artifactParts and isHtmlPage on proxy URLs", () => {
  // `artifactParts`: "returns null for any URL that does not begin
  // /api/artifact/ (so a /api/nodes/artifact?… URL has no save parts …)".
  it("returns null for a /api/nodes/artifact proxy URL", () => {
    expect(artifactParts("/api/nodes/artifact?node=n&slug=s&name=00-overview.md")).toBeNull();
    expect(artifactParts("/api/nodes/artifact?node=n&slug=s&name=nested%2Fa.md")).toBeNull();
  });

  it("still parses an owner-local /api/artifact/ URL", () => {
    expect(artifactParts("/api/artifact/react-ui/artifacts/00-overview.md")).not.toBeNull();
  });

  // ArtifactOverlay row: "isHtmlPage(url) is true for
  // /api/nodes/artifact?node=n&slug=s&name=10-arch.html and false for
  // …&name=00-overview.md."
  it("isHtmlPage reads the proxy URL's name tail", () => {
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s&name=10-arch.html")).toBe(true);
    expect(isHtmlPage("/api/nodes/artifact?node=n&slug=s&name=00-overview.md")).toBe(false);
  });
});
