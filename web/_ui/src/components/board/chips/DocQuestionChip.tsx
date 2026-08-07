import type { WebDocQuestion, WebJob } from "../../../api/types.ts";
import { settledJob } from "../../../lib/format.ts";

/**
 * The chip's own rule, split out so the card can ask whether the chip row is
 * empty without duplicating it: a settled job has no open asks worth showing,
 * and an answered question is a decision rather than a standing one.
 */
export function openDocQuestions(job: WebJob): WebDocQuestion[] {
  if (settledJob(job)) return [];
  return (job.doc_questions ?? []).filter((q) => !q.answer);
}

export function docQuestionChipVisible(job: WebJob): boolean {
  return openDocQuestions(job).length > 0;
}

/**
 * The BOARD's entire question surface: one chip carrying the open count,
 * nothing else. Port of docQuestionsChipHTML (app.js:288).
 *
 * The card used to render the whole answerable panel inline, which made a job
 * with a handful of open questions taller than everything around it and buried
 * the state the board exists to show — where each job is, and which ones need
 * a human. A count is all the board has to say; the questions themselves (and
 * every answer affordance) live one click into the job, in the steer modal or
 * the job editor, which is where the card's own click already goes.
 *
 * Three rules this chip must keep, each pinned by a test (they were pinned on
 * the vanilla side by web/boardsurface_test.go, whose assertions this
 * component's test file reproduces):
 *
 *   1. `dq-chip`, and NOT `chip` — `.chip` is the artifact-overlay click
 *      target, so a chip-classed span would try to open an artifact with an
 *      undefined url instead of falling through to the card's own handler.
 *      It is a plain span for the same reason: no new click target.
 *   2. open questions only (`!q.answer`) — an answered question is a decision,
 *      not a standing ask, so the chip disappears once the last one is
 *      answered.
 *   3. no chip at all on a settled (done/archived) job — nobody is waiting on
 *      its answers; same rule settledJob applies to the ⚑ counter.
 */
export function DocQuestionChip({ job }: { job: WebJob }) {
  const open = openDocQuestions(job);
  if (!open.length) return null;
  const p0 = open.filter((q) => q.importance === "P0").length;
  const label = `${open.length} question${open.length === 1 ? "" : "s"}`;
  const title = `${label} open${p0 ? ` (${p0} P0)` : ""} — click the card to read and answer`;
  // The visible text is the COUNT alone — `⚑ 4`. The word "question(s)" is what
  // the glyph already says, and dropping it is what lets four of these chips sit
  // on one line of a narrow column without wrapping. It survives in `title`,
  // which is unchanged: the full sentence, P0 clause and all.
  return (
    <span className={"dq-chip" + (p0 ? " p0" : "")} title={title}>
      ⚑ {open.length}
    </span>
  );
}
