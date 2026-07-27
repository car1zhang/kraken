import type { WebJob } from "../../../api/types.ts";

/**
 * Whether the card's artifact count chip renders at all. Split out so band 2
 * can ask whether it is empty without duplicating the rule — the same split
 * DocQuestionChip already makes.
 */
export function artifactCountVisible(job: WebJob): boolean {
  return (job.artifacts ?? []).length > 0;
}

/**
 * The BOARD's entire artifact surface: one count, no names. Port of
 * artifactChipHTML (app.js, band 2 of cardHTML).
 *
 * This used to be the card's own chip row — every artifact as an openable
 * `.chip`, capped at 3 behind a "+N more" toggle whose expanded state was
 * persisted per job in localStorage. All of that is gone: the cap and its
 * toggle only ever existed to stop a twelve-artifact job from owning its
 * column, which a count solves outright rather than bounds, and the list
 * itself moved one click in (the steer modal and the job editor's
 * `#editor-chips`, which is what a done or archived card opens).
 *
 * Two class rules, both load-bearing:
 *
 *   1. `ct-chip`, and NOT `chip` — `.chip` is the artifact-overlay click
 *      target, so a chip-classed span would try to open an artifact with an
 *      undefined url instead of falling through to the card's own handler.
 *   2. a plain span, not a button — no new click target. The chip's entire
 *      stated affordance is "click the card", which is what makes
 *      `data-editable` on the ARCHIVED card root load-bearing (see
 *      ArchivedCard): without it, an archived job's artifacts have no path in
 *      the UI at all.
 */
export function ArtifactCountChip({ job }: { job: WebJob }) {
  const n = (job.artifacts ?? []).length;
  if (!n) return null;
  const title = `${n} artifact${n === 1 ? "" : "s"} — click the card to open them`;
  return (
    <span className="ct-chip" title={title}>
      ▤ {n}
    </span>
  );
}
