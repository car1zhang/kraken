import { describe, expect, it } from "vitest";
import { artifactParts } from "./ArtifactOverlay.tsx";

describe("artifactParts", () => {
  it("splits slug and home off, then keeps the whole rest as the name", () => {
    expect(artifactParts("/api/artifact/react-ui/artifacts/00-overview.md")).toEqual({
      slug: "react-ui",
      home: "artifacts",
      name: "00-overview.md",
    });
  });

  it("keeps slashes INSIDE the name — a cell-home artifact may be a subpath", () => {
    // Only the first two segments split off. This is the case the vanilla
    // comment (app.js:1284) calls out by name, and getting it wrong addresses
    // a feedback post at the wrong artifact.
    expect(
      artifactParts("/api/artifact/react-ui/home/docs/diagrams/13-tree.html"),
    ).toEqual({
      slug: "react-ui",
      home: "home",
      name: "docs/diagrams/13-tree.html",
    });
  });

  it("keeps a deeply nested name intact", () => {
    const parts = artifactParts("/api/artifact/j/h/a/b/c/d/e/f.md");
    expect(parts?.name).toBe("a/b/c/d/e/f.md");
  });

  it("returns null for anything that is not an /api/artifact URL", () => {
    expect(artifactParts("/api/board")).toBeNull();
    expect(artifactParts("")).toBeNull();
    // two segments only: there is no name to address feedback at
    expect(artifactParts("/api/artifact/j/h")).toBeNull();
    expect(artifactParts("/api/artifact/j/h/")).toBeNull();
  });

  it("does not match a prefix — the path must start at /api/artifact", () => {
    expect(artifactParts("https://x/api/artifact/j/h/n.md")).toBeNull();
  });
});
