import type { WebArtifact } from "../api/types.ts";

/**
 * Artifact refs — the second kind of clickable reference in rendered markdown.
 *
 * A code span can name a sibling ARTIFACT of the same job (`51-code-map.md`,
 * `90-build-report.md` — code maps cite them constantly) rather than a repo
 * file. Those tokens are deliberately refused by `parseCodeRef` (a repo link
 * would 409), but they resolve against something better: the job's own
 * artifact list, which the board payload already carries. A span that matches
 * opens THAT ARTIFACT in the viewer — no editor POST.
 *
 * Resolution is job-scoped and happens in the overlay, never in the renderer:
 * `renderMD` is pure and cannot know which job's document it is rendering, so
 * the overlay runs `decorateArtifactRefs` over the rendered body after the
 * fact and checks membership again on click. Artifact match wins over the
 * editor path — when a token names both an artifact and (somehow) a repo file,
 * the artifact is the thing the document's reader is being pointed at.
 *
 * Fenced blocks are OUT, by the same rule the code-ref kind gets from the
 * renderer (a fence's body is content, not a citation): the sweep and both
 * click handlers skip any `<code>` inside a `<pre>`, so a one-line fence whose
 * whole body happens to be an artifact name never becomes a hotspot.
 */
export interface ArtifactRefHit {
  /** The artifact's name as the board lists it (relative to the artifacts dir). */
  name: string;
  /** Its /api/artifact URL, verbatim from the board payload. */
  url: string;
}

/**
 * One optional trailing `:line` or `:line-range` is stripped before the second
 * match attempt — `51-code-map.md:12` still names the artifact; the viewer has
 * no line scroll, so the position is advisory prose. The VERBATIM text is
 * tried first, so an artifact whose own name ends in `:<digits>` (`bench:2026`)
 * still matches its exact citation.
 */
const POS_TAIL = /:\d+(?:-\d+)?$/;

/**
 * Match a span's text against a job's artifact list. Exact name first (on the
 * verbatim text, then on the position-stripped form); then a basename match on
 * the stripped form, but only when it is UNIQUE — two nested artifacts sharing
 * a basename make the token ambiguous, and a wrong artifact opened silently is
 * worse than a plain span.
 */
export function matchArtifactRef(
  text: string,
  artifacts: readonly WebArtifact[],
): ArtifactRefHit | null {
  const verbatim = text.trim();
  if (!verbatim) return null;
  for (const t of [verbatim, verbatim.replace(POS_TAIL, "")]) {
    const exact = artifacts.find((a) => a.name === t);
    if (exact) return { name: exact.name, url: exact.url };
  }
  const stripped = verbatim.replace(POS_TAIL, "");
  const base = (n: string) => n.slice(n.lastIndexOf("/") + 1);
  const byBase = artifacts.filter((a) => base(a.name) === stripped);
  if (byBase.length === 1 && byBase[0]) {
    return { name: byBase[0].name, url: byBase[0].url };
  }
  return null;
}

/**
 * Dress every inline code span under `root` whose text names an artifact: the
 * `artifact-ref` class (the stylesheet's cue) and a `title` naming the target.
 * A span that carries the dressing but no longer matches — the artifact was
 * renamed or deleted under an open viewer — is UNDRESSED in the same pass, so
 * the affordance never outlives the thing it promises. (Stripping the title is
 * safe here: nothing else sets one on a board `<code>` — `renderMD` emits
 * class + `data-code` only, and codeRefs' decorate runs against iframe
 * documents, never these bodies.) Re-runs are no-ops for an unchanged list.
 * Returns the number of spans dressed, for the caller's tests.
 */
export function decorateArtifactRefs(
  root: ParentNode,
  artifacts: readonly WebArtifact[],
): number {
  let n = 0;
  for (const el of root.querySelectorAll("code")) {
    // a fence's body is content, not a citation — see the module header
    if (el.closest("pre")) continue;
    const hit = matchArtifactRef(el.textContent ?? "", artifacts);
    if (!hit) {
      if (el.classList.contains("artifact-ref")) {
        el.classList.remove("artifact-ref");
        el.removeAttribute("title");
      }
      continue;
    }
    el.classList.add("artifact-ref");
    el.setAttribute("title", "open " + hit.name);
    n++;
  }
  return n;
}
