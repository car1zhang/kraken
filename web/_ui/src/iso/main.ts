// The isoscene viewer page's whole runtime: fetch the card named by ?src=,
// compile it with the same lib the board's IsoSceneView uses, mount the scene
// — or get out of the way. The fallback IS the product behaviour, not an
// error path: a bespoke card must display as-is (carl's rule), and
// location.replace hands the WKWebView the flat card with no iOS-side logic
// at all, which is what keeps the phone integration to a URL rewrite.
import type { CompileResult } from "../lib/isoscene/types.ts";
import { compileCard, esc } from "../lib/isoscene/index.ts";
import "./iso.css";

/** Only same-origin artifact paths may be compiled, replaced-to, or echoed
 * into the page. ?src= is attacker-writable in principle — a crafted link, an
 * artifact name flowing through the app's URL construction — so the guard is
 * a character ALLOWLIST over the whole value, not a prefix check: it refuses
 * an absolute URL (open redirect), `..` (traversal, encoded forms included —
 * `%` is not in the set), and every markup metacharacter (`"<>&` — the error
 * note interpolates src, escaped, and the allowlist means the escape has
 * nothing to do). `+` is allowed as an ordinary path character; the iOS side
 * sends it percent-encoded so URLSearchParams' form-decoding returns it
 * intact. */
export function validSrc(src: string | null): src is string {
  return (
    src !== null &&
    src.startsWith("/api/artifact/") &&
    !src.includes("..") &&
    /^\/api\/artifact\/[A-Za-z0-9._+/-]+$/.test(src)
  );
}

/** Ceiling on a card the page will buffer and parse. Real diagram cards are
 * ~30 KB; anything megabytes-deep is a report or a mockup that would only be
 * location.replace'd after a full download AND a full DOMParser pass — the
 * exact buffered-artifact memory hazard the iOS app caps text artifacts for
 * (textArtifactByteCap in ArtifactView.swift). Past the ceiling we skip
 * straight to the flat card, which streams. The check trusts Content-Length,
 * which the artifact handler always sets for a file it serves — a header-less
 * response would skip the cap, not enforce it. */
const COMPILE_BYTE_CAP = 2_000_000;

function show(html: string): void {
  const root = document.getElementById("root");
  if (root) root.innerHTML = html;
}

async function main(): Promise<void> {
  const src = new URLSearchParams(window.location.search).get("src");
  // Computed before the guard: validSrc's type predicate narrows src to null
  // in the refusal branch, which is a fiction (false also covers strings the
  // allowlist refuses) — and exactly those strings are the ones worth
  // linking: a legally-named artifact the allowlist is too strict for (say
  // an `&` in the filename) still gets its flat card one tap away, escaped.
  // `%` is excluded here too: a percent-bearing refused value is the encoded-
  // traversal shape, not an over-refused filename, and linking it reads odd.
  const linkable =
    src !== null && src.startsWith("/api/artifact/") && !src.includes("..") && !src.includes("%");
  if (!validSrc(src)) {
    show(
      linkable
        ? `<p class="iso-note">this card's name can't be previewed in 3D — <a href="${esc(src)}">open it flat</a></p>`
        : `<p class="iso-note">missing or invalid ?src= — expected a same-origin /api/artifact/ path</p>`,
    );
    return;
  }
  let text: string;
  try {
    const r = await fetch(src);
    if (!r.ok) throw new Error(String(r.status));
    const len = Number(r.headers.get("content-length") ?? "0");
    if (len > COMPILE_BYTE_CAP) {
      window.location.replace(src);
      return;
    }
    text = await r.text();
  } catch (e) {
    // src passed the allowlist above and is escaped anyway — belt and braces,
    // since this branch renders the one attacker-influenced string on the page.
    const msg = esc(e instanceof Error ? e.message : String(e));
    show(`<p class="iso-note">failed to load the card (${msg}) — <a href="${esc(src)}">open it flat</a></p>`);
    return;
  }
  const reducedMotion = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  let result: CompileResult;
  try {
    result = compileCard(text, { reducedMotion });
  } catch {
    // Deliberately indistinguishable from a bespoke card: either way the flat
    // card is the display, and the reason would have nowhere useful to go.
    result = { kind: "not-parseable", reason: "compile threw" };
  }
  if (result.kind !== "scene") {
    // Bespoke or not a diagram: the flat card is the display, as-is.
    window.location.replace(src);
    return;
  }
  show(result.svg);
  // Hover tooltips for code refs, same as the board's IsoSceneView. No click
  // wiring: the editor-open bridge needs Zed on the machine rendering this,
  // which the phone is not.
  for (const el of Array.from(document.querySelectorAll("[data-code]"))) {
    const ref = el.getAttribute("data-code");
    if (ref) el.setAttribute("title", ref);
  }
}

void main();
