import { useRef } from "react";
import type { WebArtifact, WebJob } from "../api/types.ts";

/** One step of the viewer's sequence: where to fetch it, what to title it. */
export interface SeqEntry {
  url: string;
  name: string;
}

/**
 * `jobLabel` (app.js:53) — the human-facing name for a board entry, which is
 * the slug it was archived UNDER for a renamed archive rather than its current
 * numbered name.
 */
function jobLabel(j: Pick<WebJob, "slug" | "display_slug">): string {
  return j.display_slug || j.slug;
}

/**
 * `artifactSeqFor` (app.js:1721) — the ordered {url, name} sequence for one
 * job, built from the job's artifact list AS THE BOARD CURRENTLY HAS IT.
 *
 * The source is the job payload, never the DOM the click came from, and that
 * is the whole mechanism behind the 3s re-sync: a live cell writing a new
 * artifact under an open viewer grows N and puts the new file within reach of
 * `›` without a reopen. In React the payload arrives as a prop, so "re-derive
 * on every poll" is just this function running on every render.
 *
 * Names are labelled exactly as the chips label theirs (both through
 * `jobLabel`), so the panel title reads identically whether you arrived by
 * clicking a chip or by paging onto it.
 *
 * Returns null for a job that resolves to nothing — no key on the chip, a job
 * that left the board between render and click, or one with no artifacts at
 * all. The caller's singleton fallback is what that null feeds.
 */
export function artifactSeqFor(
  job: WebJob | undefined | null,
): SeqEntry[] | null {
  const list: WebArtifact[] = job?.artifacts ?? [];
  if (!list.length || !job) return null;
  const label = jobLabel(job);
  return list.map((a) => ({ url: a.url, name: `${label}/${a.name}` }));
}

/**
 * The open artifact's sequence and its position in it.
 *
 * This is `artifactSeqFor` + `openArtifact`'s index re-find + `resyncArtifactSeq`
 * collapsed into one derivation, because in React they are the same thing
 * evaluated at three different moments. Three properties are load-bearing:
 *
 *  1. **The index is re-found by URL, never carried.** `openArtifact` re-finds
 *     it so `seq[idx].url === url` can't be violated by a caller passing a
 *     stale index; `resyncArtifactSeq` re-finds it because the list can gain
 *     entries AHEAD of the open one, which would silently slide the position
 *     onto the wrong artifact.
 *
 *  2. **A sequence that no longer contains the open artifact is KEPT.** That is
 *     `resyncArtifactSeq`'s early return, and it is deliberate: an artifact
 *     deleted or renamed under the viewer stays readable and — the part that
 *     actually matters — its NEIGHBOURS stay reachable. Falling back to a
 *     singleton there would strand the reader mid-set. Hence `held`, which
 *     survives a step (this hook is called above the per-artifact remount) and
 *     is dropped on close.
 *
 *  3. **An unresolvable job opens a SINGLETON**, pager hidden, behaving exactly
 *     as the viewer did before next/prev existed.
 *
 * It never closes the overlay and must not learn to: `checkStaleModals`
 * deliberately leaves the artifact viewer alone (it isn't scoped to a job's
 * state), and this would be the back door into that.
 */
export function useArtifactSeq(
  job: WebJob | undefined | null,
  url: string,
  name: string,
  open = true,
): { seq: SeqEntry[]; idx: number } {
  const held = useRef<SeqEntry[] | null>(null);
  if (!open) {
    // a closed viewer holds nothing: the next open is a different artifact,
    // possibly of a different job, and a retained sequence could only be wrong
    held.current = null;
    return { seq: [], idx: 0 };
  }
  const fresh = artifactSeqFor(job);
  const at = fresh ? fresh.findIndex((e) => e.url === url) : -1;
  let seq: SeqEntry[];
  let idx: number;
  if (fresh && at >= 0) {
    seq = fresh;
    idx = at;
  } else {
    const prev = held.current;
    const was = prev ? prev.findIndex((e) => e.url === url) : -1;
    if (prev && was >= 0) {
      seq = prev; // property 2 — keep the sequence we were opened with
      idx = was;
    } else {
      seq = [{ url, name }]; // property 3
      idx = 0;
    }
  }
  // Render-phase ref write, deliberately. React documents render as pure, and
  // this is the sanctioned exception: `held` is a LATEST-VALUE MIRROR, never
  // read during the render that writes it, only by a LATER render deciding
  // whether the sequence it was handed still contains the open artifact. It is
  // also idempotent — the same (job, url) always computes the same `seq`, so a
  // StrictMode double render or a discarded concurrent pass writes the value
  // the kept pass would have written. An effect cannot host it: the value is
  // needed by the very next render, which an effect runs too late to inform.
  held.current = seq;
  return { seq, idx };
}
