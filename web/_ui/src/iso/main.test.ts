// Lead-authored (sighted). Pins validSrc — the viewer page's central security
// decision: ?src= is attacker-writable in principle, so the allowlist is both
// the open-redirect guard for location.replace AND the injection guard for
// the one error note that echoes the value (escaped on top). Importing
// main.ts executes its module-level main(), which under jsdom sees no ?src=
// and no #root and no-ops — asserted by the import not throwing.
import { describe, expect, it } from "vitest";
import { validSrc } from "./main.ts";

describe("iso viewer validSrc", () => {
  it("accepts same-origin artifact paths only", () => {
    expect(validSrc("/api/artifact/demo/cell/10-arch.html")).toBe(true);
    expect(validSrc("/api/artifact/a-b/pluto/x.htm")).toBe(true);
  });

  it("accepts + as an ordinary path character", () => {
    expect(validSrc("/api/artifact/demo/cell/a+b.html")).toBe(true);
  });

  it("refuses everything else", () => {
    expect(validSrc(null)).toBe(false);
    expect(validSrc("")).toBe(false);
    expect(validSrc("https://evil.example/card.html")).toBe(false);
    expect(validSrc("//evil.example/card.html")).toBe(false);
    expect(validSrc("/iso.html?src=/api/artifact/x")).toBe(false);
    expect(validSrc("/api/artifact/../secrets")).toBe(false);
    expect(validSrc("/api/upload/demo/img-001.png")).toBe(false);
  });

  it("refuses markup metacharacters and encoded traversal outright — the allowlist is the injection guard", () => {
    expect(validSrc('/api/artifact/x"><img src=y onerror=alert(1)>')).toBe(false);
    expect(validSrc("/api/artifact/x/%2e%2e/%2e%2e/etc/passwd")).toBe(false);
    expect(validSrc("/api/artifact/a&b.html")).toBe(false);
    expect(validSrc("/api/artifact/a b.html")).toBe(false);
    expect(validSrc("/api/artifact/a=b.html")).toBe(false);
  });
});
