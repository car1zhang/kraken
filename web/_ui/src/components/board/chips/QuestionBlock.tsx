import { useState } from "react";

import type { WebJob } from "../../../api/types.ts";

export interface QuestionBlockProps {
  job: WebJob;
  /**
   * POSTs /api/question with the 1-based option index. May return the failure
   * (the shared mutation path's shape) — if it does, the options come back to
   * life; see the `answered` comment below.
   */
  onAnswerQuestion(slug: string, optionIndex: number): void | Promise<Error | null>;
}

/**
 * A card's pending AskUserQuestion. Port of questionHTML (app.js:119).
 *
 * Option buttons only make sense for a single, non-multi-select question —
 * that is the one shape where "post a single digit" (webAnswerQuestion's whole
 * contract) can answer it. Anything richer (more than one question, or a
 * multi-select) can't be expressed as one digit, so it renders as read-only
 * header(s) plus a note pointing at the pane instead of buttons that would
 * silently mis-answer it.
 */
export function QuestionBlock({ job, onAnswerQuestion }: QuestionBlockProps) {
  // Once one option is posted every option for this job goes dead, matching
  // the vanilla handler's `.q-opt[data-slug=…] → disabled` sweep: the answer
  // is in flight and a second digit would be a second answer.
  //
  // It must come BACK to life if the post fails. Vanilla self-healed within one
  // 3s tick because its disable was a DOM sweep that the next board rebuild
  // undid; a component that stays mounted (this one does, for as long as
  // `job.question` keeps arriving — and a failed answer never reaches the pane,
  // so the transcript keeps reporting the question) would otherwise be disabled
  // forever, leaving the question unanswerable from the board short of a
  // reload. JobCard additionally keys this block on the tool_use_id, so a NEW
  // question is never born disabled.
  const [answered, setAnswered] = useState(false);

  const questions = job.question?.questions ?? [];
  const first = questions[0];
  const single = questions.length === 1 && first && !first.multi_select;

  if (!single) {
    return (
      <div className="question">
        {questions.map((q, i) => (
          <div className="q-header" key={`${q.header || q.question}|${i}`}>
            ⚑ {q.header || q.question || "question"}
          </div>
        ))}
        <div className="q-text">answer in the pane (k o {job.cell})</div>
      </div>
    );
  }

  return (
    <div className="question">
      <div className="q-header">⚑ {first.header || first.question || "question"}</div>
      {first.header && first.question ? <div className="q-text">{first.question}</div> : null}
      <div className="q-opts">
        {(first.options ?? []).map((o, i) => (
          <button
            key={`${o.label}|${i}`}
            className="q-opt"
            data-slug={job.slug}
            data-idx={i + 1}
            title={o.description || ""}
            disabled={answered}
            onClick={async (ev) => {
              ev.stopPropagation();
              setAnswered(true);
              // A caller that returns void leaves the buttons disabled until
              // the question itself changes, which is the old behaviour; one
              // that returns the mutation's result re-arms them on a failure.
              const err = await onAnswerQuestion(job.slug, i + 1);
              if (err) setAnswered(false);
            }}
          >
            {i + 1}. {o.label}
          </button>
        ))}
      </div>
    </div>
  );
}
