import type { WebJob } from "../../../api/types.ts";

/** Whether the card's feedback count chip renders at all — see FeedbackChip. */
export function feedbackChipVisible(job: WebJob): boolean {
  return (job.feedback ?? []).length > 0;
}

/**
 * The BOARD's entire feedback surface: one chip carrying the totals, nothing
 * else. Port of feedbackChipHTML (app.js) — the same subtraction
 * DocQuestionChip and ArtifactCountChip already make, and for the same reason.
 * A note is a paragraph of PROSE, so the panel this replaced made a job
 * carrying a handful of them taller than everything around it far more readily
 * than a question list ever did.
 *
 * A plain span, not a `.chip` (that class opens an artifact) and not a button,
 * so a click falls through to the card-open fallback that reaches the panel
 * itself (FeedbackPanel, now rendered only by the steer modal and the job
 * editor).
 *
 * Both counts ride it: the total is what the job accumulated, the open count
 * (notes zeus has not posted an `addressed` line for) is what is still owed.
 * Unlike a question, a note is never "settled" by the job being done — carl
 * leaves most of them ON done work — so there is deliberately no settledJob()
 * suppression here, and an ARCHIVED card carries this chip too.
 */
export function FeedbackChip({ job }: { job: WebJob }) {
  const list = job.feedback ?? [];
  if (!list.length) return null;
  const open = list.filter((f) => !f.addressed).length;
  const label = open ? `${list.length} feedback · ${open} open` : `${list.length} feedback`;
  const title =
    `${list.length} feedback note${list.length === 1 ? "" : "s"}` +
    (open ? `, ${open} not yet addressed` : ", all addressed") +
    " — click the card to read them";
  return (
    <span className={"fb-chip" + (open ? " open" : "")} title={title}>
      ✎ {label}
    </span>
  );
}
