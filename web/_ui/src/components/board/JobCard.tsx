import { Fragment, type CSSProperties } from "react";

import type { WebJob } from "../../api/types.ts";
import type { Column } from "../../lib/columns.ts";
import { age, docQuestionsOpenP0, stateColor } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { ArtifactChips } from "./chips/ArtifactChips.tsx";
import { CrewPills } from "./chips/CrewPills.tsx";
import { DocQuestionChip, docQuestionChipVisible } from "./chips/DocQuestionChip.tsx";
import { FeedbackPanel } from "./chips/FeedbackPanel.tsx";
import { PRChips } from "./chips/PRChips.tsx";
import { QuestionBlock } from "./chips/QuestionBlock.tsx";
import { ThreadsRow } from "./chips/ThreadChips.tsx";
import { WakeupChip } from "./chips/WakeupChip.tsx";
import type { BoardActions } from "./BoardView.tsx";
import { useSortableCard } from "./DragProvider.tsx";

export interface JobCardProps {
  job: WebJob;
  col: Column;
  actions: BoardActions;
  /** Whether this job's artifact chips are expanded past the first 3. */
  expanded: boolean;
  onToggleExpanded(key: string): void;
}

/**
 * One job card. Port of cardHTML (app.js:500), element for element.
 *
 * The card is a click target in its own right: every plain click that is not
 * on a button or chip opens something (a live working/blocked job opens the
 * steer/unblock modal on itself, everything else opens the editor, which
 * renders read-only when the server says the job is not editable). The
 * vanilla board got that ordering from one delegated document listener whose
 * branch order WAS the precedence; here each control stops propagation
 * instead, which is the same precedence expressed locally.
 */
export function JobCard({ job, col, actions, expanded, onToggleExpanded }: JobCardProps) {
  const key = jobKey(job);
  // Keyed by jobKey, never the bare slug: an archived entry and an active job
  // that reused its freed slug would otherwise share one drag id.
  const drag = useSortableCard(key);
  const flagged = !!job.question || !!job.health || docQuestionsOpenP0(job) > 0;
  // `editable` is only ever read by the drag/e2e side as a data attribute —
  // the click fallback below decides for itself, exactly as the vanilla one
  // did (it matched `.card[data-editable]`, attribute presence, not value).
  const editable = !job.live && ["draft", "queued", "done"].includes(col);
  const roster = job.roster ?? [];
  const prereqs = job.prereqs ?? [];
  const hasChips = (job.artifacts ?? []).length > 0 || docQuestionChipVisible(job);
  const retryable = !!job.health && !/is gone|startup prompt/.test(job.health);

  const openDrilldown = () => {
    // A click that ends a text selection is not a click on the card.
    if (window.getSelection()?.toString()) return;
    // liveDisplayColumn (app.js:2057): only a LIVE job has a steerable column,
    // and an unhealthy one reads as blocked whatever its file says.
    const liveCol = job.live ? (job.health ? "blocked" : job.state) : null;
    if (liveCol === "working" || liveCol === "blocked") {
      actions.onOpenSteer(job.slug, liveCol === "blocked" ? "unblock" : "steer");
    } else {
      actions.onOpenEditor(job.slug);
    }
  };

  return (
    <div
      ref={drag.setNodeRef}
      className={
        "card editable" + (flagged ? " flagged" : "") + (drag.className ? " " + drag.className : "")
      }
      // The `--state` custom property IS the state-color mechanism: style.css
      // reads var(--state) for the rail, the dot and the hover glow, and a
      // card without it loses its color silently. The drag transform is merged
      // after it, never instead of it.
      style={{ "--state": stateColor(job.state), ...drag.style } as CSSProperties}
      // Kept for `.card[draggable="true"] { cursor: grab }` (style.css:345) —
      // the only reason the attribute is still here. The native HTML5 drag it
      // would otherwise start is cancelled below: dnd-kit drives the drag from
      // pointer events, and a native drag racing it produces a ghost image and
      // a dropped gesture.
      draggable="true"
      onDragStart={(ev) => ev.preventDefault()}
      data-slug={job.slug}
      data-col={col}
      data-editable={editable}
      onClick={openDrilldown}
      {...drag.attributes}
      {...drag.listeners}
    >
      <div className="card-top">
        <span className="slug">{job.slug}</span>
        <span className="state">● {job.state}</span>
        {col === "queued" && !job.live ? (
          <button
            className="queue-cancel"
            data-slug={job.slug}
            title="cancel — return to draft (spec is kept)"
            onClick={(ev) => {
              ev.stopPropagation();
              actions.onDequeue(job.slug);
            }}
          >
            ✕
          </button>
        ) : null}
      </div>

      <WakeupChip job={job} />
      <PRChips prs={job.prs} />
      <ThreadsRow job={job} onOpenThread={actions.onOpenThread} />

      <div className="meta">
        <span>{job.repo}</span>
        {job.live ? <span>{job.cell}</span> : null}
        <span data-age={job.mtime}>{age(job.mtime)}</span>
        {prereqs.length ? (
          <span className="prereq">
            {/* joined by a space between the spans, as `.join(" ")` did.
                The key carries the index because `parsePrereqs`
                (internal/jobs/job.go) neither dedupes nor rejects a repeated
                `- job:` line, so the slug alone is not unique. */}
            {prereqs.map((p, i) => (
              <Fragment key={`${p.job}|${i}`}>
                {i > 0 ? " " : null}
                <span className={p.done ? "ok" : p.gated ? "gate" : "pend"}>
                  {p.job}
                  {p.done ? "✓" : p.gated ? "⚑" : "✗"}
                </span>
              </Fragment>
            ))}
          </span>
        ) : null}
      </div>

      {job.live ? (
        <div className="review-row">
          <button
            className="btn tmux-open open-tmux"
            data-slug={job.slug}
            title="raise zeus' pane in Ghostty"
            onClick={(ev) => {
              ev.stopPropagation();
              actions.onOpenTmux(job.slug);
            }}
          >
            ⧉ tmux
          </button>
        </div>
      ) : null}

      {job.err ? <div className="err">✕ {job.err}</div> : null}
      {job.health ? <div className="err">⚠ {job.health}</div> : null}
      {job.last_sent ? <div className="last-sent">me→zeus: {job.last_sent}</div> : null}
      {job.detail ? <div className="detail">{job.detail}</div> : null}
      {job.wakeup?.reason && job.state === "working" ? (
        <div className="wakeup-reason">↻ {job.wakeup.reason}</div>
      ) : null}

      <CrewPills job={job} />

      {roster.length ? (
        <div className="roster">
          {/* index-suffixed for the same reason as the prereqs, except here
              the server MANUFACTURES the collision: roster.go falls back
              AgentName → Name → "-", and Peer.Name is empty until claude writes
              its session file — so two crew agents inside their spawn window
              both arrive named "-". Keying on the name alone made the
              reconciler drop one of the pair and under-report the crew. */}
          {roster.map((r, i) => (
            <div className="roster-row" key={`${r.name}|${i}`}>
              <span className={"roster-dot" + (r.working ? " on" : "")}>
                {r.working ? "●" : "○"}
              </span>{" "}
              {r.name} <span className="dim">({r.model})</span>
            </div>
          ))}
        </div>
      ) : null}

      {job.outcome && col === "done" ? <div className="outcome">→ {job.outcome}</div> : null}

      {hasChips ? (
        <div className="chips">
          <ArtifactChips
            artifacts={job.artifacts}
            jobKey={key}
            label={job.slug}
            expanded={expanded}
            onToggleExpanded={onToggleExpanded}
            onOpenArtifact={actions.onOpenArtifact}
          />
          {/*
            The questions chip rides in the artifact-chip row rather than a row
            of its own: both answer "what's in this job", and one wrapped flex
            row costs the card no extra height when there is already a chip
            line. This is the card's ONLY question surface — the answerable
            panel lives one click in, in the steer modal or the job editor.
          */}
          <DocQuestionChip job={job} />
        </div>
      ) : null}

      {retryable ? (
        <button
          className="btn retry"
          data-slug={job.slug}
          onClick={(ev) => {
            ev.stopPropagation();
            actions.onRetry(job.slug);
          }}
        >
          ↻ retry
        </button>
      ) : null}

      {job.question ? (
        // Keyed on the question's identity so a NEW question always mounts a
        // fresh block: the old one may have disabled its options on a click.
        <QuestionBlock
          key={job.question.tool_use_id}
          job={job}
          onAnswerQuestion={actions.onAnswerQuestion}
        />
      ) : null}

      <FeedbackPanel
        job={job}
        onSaveFeedback={actions.onSaveFeedback}
        onDeleteFeedback={actions.onDeleteFeedback}
      />

      {(col === "done" || job.cell_gone) && job.reactivatable ? (
        <button
          className="btn reactivate"
          data-slug={job.slug}
          onClick={(ev) => {
            ev.stopPropagation();
            actions.onOpenSteer(job.slug, "reactivate");
          }}
        >
          ↺ reactivate
        </button>
      ) : null}
    </div>
  );
}
