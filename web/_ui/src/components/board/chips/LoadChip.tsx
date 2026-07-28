import type { WebJob } from "../../../api/types.ts";
import type { Column } from "../../../lib/columns.ts";
import { loadBadgeText, loadBadgeTitle, loadROI } from "../../../lib/format.ts";

/**
 * The chip's own rule, split out so the card can ask whether the chip row is
 * empty without duplicating it — the same split DocQuestionChip and
 * FeedbackChip make.
 *
 * Two conditions, and the column one is half the feature:
 *
 *   1. the card must READ AS BLOCKED. That is `col === "blocked"` and nothing
 *      else, because `col` already carries the health override: bucketJobs
 *      (lib/format.ts) files a job as `j.health ? "blocked" : columnFor(state)`,
 *      so an unhealthy cell whose file still says "working" arrives here in the
 *      blocked column. Testing `job.health` again beside this would be a SECOND
 *      spelling of one rule, free to drift from the bucketing the board
 *      actually laid the card out with.
 *   2. an estimate must exist and be whole — loadROI returns null for an absent
 *      `load` key, for the cleared both-zero state, and for a half-estimate.
 *
 * The badge answers "is this one worth unblocking right now", which is not a
 * question a working/queued/draft/done/archived card poses, and an estimate
 * left on a card that has since started running reads as a current fact.
 */
export function loadChipVisible(job: WebJob, col: Column): boolean {
  return col === "blocked" && loadROI(job.load) !== null;
}

/**
 * The board's triage badge: zeus' two 1..10 estimates for a blocked job,
 * rendered as the ratio between them — `⚡ 4.0× 2→8` — so scanning the blocked
 * column down the page ranks the jobs by what an unblock buys.
 *
 * Class rules, both load-bearing:
 *
 *   1. `ld-chip`, and NOT `chip` — `.chip` is the artifact-overlay click
 *      target, so a chip-classed element would try to open an artifact with an
 *      undefined url instead of falling through to the card's own click
 *      handler. Same hazard `ct-chip`, `dq-chip` and `fb-chip` were named
 *      around. It is a plain span rather than a button for the same reason: the
 *      badge is a number to read, not a new click target, and the card's own
 *      handler (the unblock modal, for a blocked live job) is exactly where a
 *      click on it should land. style.css does give `.ld-chip` a pointer
 *      cursor, as it does all three siblings — that is the CARD's affordance
 *      showing through an element that does not intercept the click, not a
 *      claim that the badge is clickable in its own right.
 *   2. the band rides as a second class (`hot`/`warm`/`even`/`cold`) rather
 *      than an inline style, so the four treatments live in style.css beside
 *      the rest of the chip row and stay themeable in both palettes.
 *
 * The ratio is derived here on every render and stored nowhere — see loadROI.
 */
export function LoadChip({ job, col }: { job: WebJob; col: Column }) {
  if (col !== "blocked") return null;
  const load = job.load;
  const derived = loadROI(load);
  // Both null checks are needed for the compiler as well as at runtime:
  // loadROI(undefined) returning null does not narrow `load` on its own.
  if (!load || !derived) return null;
  // One sentence, on both attributes: `title` is what a pointer gets, and
  // `aria-label` is what replaces the visible text for a screen reader — which
  // is worth spending, because the visible text is `⚡ 4.0× 2→8`, a string with
  // no spoken form worth hearing.
  const title = loadBadgeTitle(load, derived.roi);
  return (
    <span
      className={"ld-chip " + derived.band}
      title={title}
      aria-label={title}
      data-band={derived.band}
    >
      {loadBadgeText(load, derived.roi)}
    </span>
  );
}
