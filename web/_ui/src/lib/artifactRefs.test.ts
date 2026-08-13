import { describe, expect, it } from "vitest";
import type { WebArtifact } from "../api/types.ts";
import { decorateArtifactRefs, matchArtifactRef } from "./artifactRefs.ts";

const arts: WebArtifact[] = [
  { name: "00-overview.md", url: "/api/artifact/j/cell/00-overview.md" },
  { name: "51-code-map.md", url: "/api/artifact/j/cell/51-code-map.md" },
  { name: "icons/app.png", url: "/api/artifact/j/cell/icons/app.png" },
  { name: "shots/board.png", url: "/api/artifact/j/cell/shots/board.png" },
];

describe("matchArtifactRef", () => {
  it("matches an exact artifact name", () => {
    expect(matchArtifactRef("51-code-map.md", arts)).toEqual({
      name: "51-code-map.md",
      url: "/api/artifact/j/cell/51-code-map.md",
    });
  });

  it("ignores one trailing :line or :line-range — the viewer has no line scroll", () => {
    expect(matchArtifactRef("51-code-map.md:12", arts)?.name).toBe("51-code-map.md");
    expect(matchArtifactRef("51-code-map.md:12-30", arts)?.name).toBe("51-code-map.md");
  });

  it("matches a nested artifact by its basename when the basename is unique", () => {
    expect(matchArtifactRef("app.png", arts)?.url).toBe(
      "/api/artifact/j/cell/icons/app.png",
    );
  });

  it("refuses an ambiguous basename — two artifacts sharing one is no match", () => {
    const dup: WebArtifact[] = [
      ...arts,
      { name: "old/app.png", url: "/api/artifact/j/cell/old/app.png" },
    ];
    expect(matchArtifactRef("app.png", dup)).toBeNull();
  });

  it("prefers the exact name over a basename collision", () => {
    const tricky: WebArtifact[] = [
      { name: "b/notes.md", url: "/u/nested" },
      { name: "notes.md", url: "/u/flat" },
    ];
    expect(matchArtifactRef("notes.md", tricky)?.url).toBe("/u/flat");
  });

  it("matches nothing on a non-artifact token, empty text, or an empty list", () => {
    expect(matchArtifactRef("web/serve.go:225", arts)).toBeNull();
    expect(matchArtifactRef("Makefile", arts)).toBeNull();
    expect(matchArtifactRef("   ", arts)).toBeNull();
    expect(matchArtifactRef("51-code-map.md", [])).toBeNull();
  });
});

describe("decorateArtifactRefs", () => {
  const host = (html: string) => {
    const div = document.createElement("div");
    div.innerHTML = html;
    return div;
  };

  it("dresses matching spans with the class and a title, and counts them", () => {
    const root = host(
      "<p>see <code>51-code-map.md</code> and <code>web/serve.go:225</code></p>",
    );
    expect(decorateArtifactRefs(root, arts)).toBe(1);
    const code = root.querySelector("code.artifact-ref");
    expect(code?.textContent).toBe("51-code-map.md");
    expect(code?.getAttribute("title")).toBe("open 51-code-map.md");
    expect(
      root.querySelectorAll("code:not(.artifact-ref)")[0]?.hasAttribute("title"),
    ).toBe(false);
  });

  it("is idempotent — a second sweep changes nothing and re-counts the same spans", () => {
    const root = host("<code>00-overview.md</code>");
    expect(decorateArtifactRefs(root, arts)).toBe(1);
    expect(decorateArtifactRefs(root, arts)).toBe(1);
    expect(root.querySelectorAll("code.artifact-ref").length).toBe(1);
  });

  it("does nothing with no artifacts and leaves plain spans untouched", () => {
    const root = host("<code>51-code-map.md</code>");
    expect(decorateArtifactRefs(root, [])).toBe(0);
    expect(root.querySelector("code")?.className).toBe("");
  });
});

describe("review-round pins", () => {
  const host = (html: string) => {
    const div = document.createElement("div");
    div.innerHTML = html;
    return div;
  };

  it("matches an artifact whose own name ends in :digits by its verbatim citation", () => {
    const colonArts: WebArtifact[] = [{ name: "bench:2026", url: "/u/bench" }];
    expect(matchArtifactRef("bench:2026", colonArts)?.url).toBe("/u/bench");
  });

  it("never dresses a fenced block, even one whose whole body is an artifact name", () => {
    const root = host("<pre><code>51-code-map.md\n</code></pre>");
    expect(decorateArtifactRefs(root, arts)).toBe(0);
    expect(root.querySelector("code")?.classList.contains("artifact-ref")).toBe(false);
  });

  it("undresses a span whose artifact left the list, and strips the title it set", () => {
    const root = host("<code>51-code-map.md</code>");
    expect(decorateArtifactRefs(root, arts)).toBe(1);
    const shrunk = arts.filter((a) => a.name !== "51-code-map.md");
    expect(decorateArtifactRefs(root, shrunk)).toBe(0);
    const code = root.querySelector("code");
    expect(code?.classList.contains("artifact-ref")).toBe(false);
    expect(code?.hasAttribute("title")).toBe(false);
  });

  it("undresses everything when the list empties", () => {
    const root = host("<code>00-overview.md</code>");
    decorateArtifactRefs(root, arts);
    expect(decorateArtifactRefs(root, [])).toBe(0);
    expect(root.querySelector("code")?.className).toBe("");
  });
});
