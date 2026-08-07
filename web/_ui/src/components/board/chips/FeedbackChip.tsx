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
 * Both counts ride it: the total is what the job accumulated, and the second
 * count is how many notes carry no `addressed` line.
 *
 * That second count is NOT "what is still owed", and the wording here is the
 * whole of the difference. `addressed` is written by the nightly dream pass
 * alone — an ordinary cell acts on its feedback and deliberately leaves the
 * column empty (internal/jobs/crew.go's brief) — so a note with no line is a
 * note no DURABLE fix has shipped for, which is the common case even for notes
 * the owning cell fixed within the hour. It is a corroboration backlog, not a
 * to-do list, which is why it no longer takes the `.open` highlight: styling it
 * as work owed would now fire on nearly every job with feedback, and an
 * emphasis that fires on nearly everything means nothing.
 *
 * Unlike a question, a note is never "settled" by the job being done — carl
 * leaves most of them ON done work — so there is deliberately no settledJob()
 * suppression here, and an ARCHIVED card carries this chip too.
 */
export function FeedbackChip({ job }: { job: WebJob }) {
  const list = job.feedback ?? [];
  if (!list.length) return null;
  const unfixed = list.filter((f) => !f.addressed).length;
  const title =
    `${list.length} feedback note${list.length === 1 ? "" : "s"}` +
    (unfixed
      ? `, ${unfixed} with no durable fix shipped yet (the nightly pass writes that, not the cell)`
      : ", all have a durable fix recorded") +
    " — click the card to read them";
  // The visible text is the TOTAL alone — `✎ 3`. The second count (notes with
  // no durable fix recorded) and the sentence explaining what that does and does
  // not mean stay in `title`, which is unchanged: it was never a number to scan
  // a column on, and spelling it out on the card is what made this the widest
  // chip in the row.
  return (
    <span className="fb-chip" title={title}>
      ✎ {list.length}
    </span>
  );
}
