import { Fragment, type CSSProperties, type ReactNode } from "react";

import type { WebJob } from "../../api/types.ts";
import type { Column } from "../../lib/columns.ts";
import { age, jobColorVar, stateColor } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { highlightSegments, type JobMatch } from "../../lib/jobSearch.ts";
import { ArtifactCountChip, artifactCountVisible } from "./chips/ArtifactChips.tsx";
import { CrewChips, crewChipsVisible } from "./chips/CrewPills.tsx";
import { DocQuestionChip, docQuestionChipVisible } from "./chips/DocQuestionChip.tsx";
import { FeedbackChip, feedbackChipVisible } from "./chips/FeedbackChip.tsx";
import { LoadChip, loadChipVisible } from "./chips/LoadChip.tsx";
import { PRChips } from "./chips/PRChips.tsx";
import { QuestionBlock } from "./chips/QuestionBlock.tsx";
import { ThreadChips, threadChipsVisible } from "./chips/ThreadChips.tsx";
import { WakeupChip } from "./chips/WakeupChip.tsx";
import type { BoardActions } from "./BoardView.tsx";
import { useSortableCard } from "./DragProvider.tsx";

/**
 * One status line with the query's matched substrings wrapped in `<mark>`.
 *
 * Returns the bare string when nothing in it matched, so a line the search
 * never touched renders the single text node it always did. The segment keys
 * are indices because a segment's identity IS its position in the line —
 * there is nothing else to key on, and the list is rebuilt whole per render.
 */
function markMatches(line: string, terms: readonly string[]): ReactNode {
  const segments = highlightSegments(line, terms);
  if (segments.length === 1 && !segments[0]!.hit) return line;
  return segments.map((seg, i) =>
    seg.hit ? <mark key={i}>{seg.text}</mark> : <Fragment key={i}>{seg.text}</Fragment>,
  );
}

export interface JobCardProps {
  job: WebJob;
  col: Column;
  actions: BoardActions;
  /**
   * Why the search kept this card, when a search is running at all. Only
   * `"text"` renders anything: a card kept by its NAME needs no explanation,
   * and a card kept by its status text has a name with nothing to do with what
   * carl typed, so without the marker it reads as the filter being broken.
   */
  matchReason?: JobMatch["reason"];
  /**
   * The query's terms, for marking the substrings that did the matching.
   * Read only when `matchReason === "text"`; the card renders exactly its
   * usual status lines otherwise.
   */
  matchTerms?: readonly string[];
  /**
   * @deprecated Dead, and accepted only so App.tsx keeps compiling until the
   * lead drops the two call sites. The card no longer LISTS artifacts — band 2
   * carries a bare count (ArtifactCountChip) — so there is no "+N more" toggle
   * and no per-job expansion state left to hold. Delete both of these, the
   * `expanded` prop on BoardView, and App.tsx's `useLocalStorageSet(
   * "kraken-expanded-artifacts")` together.
   */
  expanded?: boolean;
  /** @deprecated See `expanded`. */
  onToggleExpanded?(key: string): void;
}

/**
 * One job card, in SIX FIXED BANDS — this order, always this order, whatever
 * column the card is in. Port of cardHTML (app.js), band for band; the numbers
 * are shared vocabulary with style.css and the iOS JobCardView, so renumbering
 * here means renumbering there.
 *
 *   1. IDENTITY — slug · state, then repo · cell · age · prereqs. Contiguous:
 *      nothing may be inserted between these two lines. (They used to be split
 *      in two by the tmux button and the PR/thread rows.)
 *   2. CHIPS — one wrapped row of tokens directly under the repo line, and the
 *      card's INVENTORY surface, carrying COUNTS rather than lists: crew,
 *      branches, then a bare count each for questions, artifacts and feedback.
 *      It sits with identity rather than down by the actions because it answers
 *      "what is in this job", read at the same glance as "which job is this".
 *      Nothing here names an individual thing except a branch, which has
 *      nothing longer to say. Everything the counts stand for is one click away
 *      in the modal — that is the rule this row exists to enforce, and it is
 *      why the artifact NAMES moved out rather than being capped.
 *      ONE token in this row is not an inventory count: the ROI badge
 *      (LoadChip), which leads the row and appears on BLOCKED cards only. It is
 *      a judgement about the job rather than something the job contains, and it
 *      sits here anyway because it is read at that same first glance — it
 *      answers "is this the one to unblock next", which is a question you ask
 *      while scanning slugs, not one you ask after reading the status lines.
 *      Leftmost is what makes that work: the badges line up down the blocked
 *      column and can be compared without reading the cards around them.
 *   3. STATUS — what is true right now, worst-first: outcome, err/health, then
 *      the me→zeus/zeus conversation pair (chronological, so it reads as a
 *      transcript), then the poll countdown WITH its stated reason. The search
 *      marker belongs here rather than in band 2 for that same reason: "this
 *      card is on screen because of what its status text says right now" is a
 *      fact about the moment, not part of the job's inventory.
 *   4. NEEDS YOU — the pending AskUserQuestion. Above the PR row, not below it:
 *      it is the one block on a card carrying buttons you must press.
 *   5. PRS — their own row, deliberately NOT folded into band 2. A PR is the
 *      job's OUTPUT, not part of its inventory: the last thing that happened
 *      rather than something the job contains, the one chip that leaves the app
 *      entirely, and the only one naming a specific external thing. So it sits
 *      low, next to the actions, where the "what came of this" reading is.
 *   6. ACTIONS — tmux, big, at the foot, plus retry and reactivate when the
 *      job's state offers them. ⑂ branch is deliberately NOT here: branching is
 *      a detail action and lives in the modal, which has its own button for it.
 *
 * The card is a click target in its own right: every plain click that is not on
 * a button or chip opens something (a live working/blocked job opens the
 * steer/unblock modal on itself, everything else opens the editor, which
 * renders read-only when the server says the job is not editable). The vanilla
 * board got that ordering from one delegated document listener whose branch
 * order WAS the precedence; here each control stops propagation instead, which
 * is the same precedence expressed locally.
 */
export function JobCard({ job, col, actions, matchReason, matchTerms }: JobCardProps) {
  const key = jobKey(job);
  // Keyed by jobKey, never the bare slug: an archived entry and an active job
  // that reused its freed slug would otherwise share one drag id.
  const drag = useSortableCard(key);
  // `editable` is only ever read as a data attribute (by the drag/e2e side, and
  // by the editor, to decide read-only) — the click fallback below decides for
  // itself, exactly as the vanilla one did: it matched `.card[data-editable]`,
  // attribute PRESENCE, not value.
  const editable = !job.live && ["draft", "queued", "done"].includes(col);
  const prereqs = job.prereqs ?? [];
  const retryable = !!job.health && !/is gone|startup prompt/.test(job.health);

  // band 2 — is there anything to put in the row at all? Asked per chip rather
  // than by rendering and measuring, because a component that returns null
  // still occupies a slot in an array.
  const hasChips =
    loadChipVisible(job, col) ||
    crewChipsVisible(job) ||
    threadChipsVisible(job) ||
    docQuestionChipVisible(job) ||
    artifactCountVisible(job) ||
    feedbackChipVisible(job);

  // band 3 — a card kept ONLY by its status text marks the words that kept it;
  // every other card renders these lines exactly as it always has.
  const textMatch = matchReason === "text";
  const hi = (line: string): ReactNode =>
    textMatch ? markMatches(line, matchTerms ?? []) : line;

  // band 6 — assembled first rather than inline, so the row can be dropped
  // whole when a card offers nothing to press (every draft/queued card).
  const reactivatable = (col === "done" || job.cell_gone) && job.reactivatable;
  const hasActions = job.live || retryable || reactivatable;

  // An uncoloured job emits no --job-color declaration AT ALL and its accent
  // bar falls through to grey. Emitting "" instead would be worse than emitting
  // nothing: a custom property set to the empty string substitutes nothing, so
  // `var(--job-color, var(--text-2))` would resolve to nothing rather than to
  // its fallback and the whole border-left declaration would go invalid.
  const jc = jobColorVar(job);

  const openDrilldown = () => {
    // A click that ends a text selection is not a click on the card.
    if (window.getSelection()?.toString()) return;
    // liveDisplayColumn (app.js): only a LIVE job has a steerable column, and
    // an unhealthy one reads as blocked whatever its file says.
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
      className={"card editable" + (drag.className ? " " + drag.className : "")}
      // Two inline custom properties, and they mean DIFFERENT things — style.css
      // is explicit that the accent bar carries neither state nor alerts:
      //   --state      the `● working` chip's colour (var(--state)).
      //   --job-color  the 2px left accent bar, which is the job's colour
      //                channel and nothing else. Omitted entirely when unset.
      // A card that drops either loses that channel silently, since var() of an
      // undeclared property simply resolves to its fallback. The drag transform
      // is merged after both, never instead of them.
      style={
        {
          "--state": stateColor(job.state),
          ...(jc ? { "--job-color": jc } : {}),
          ...drag.style,
        } as CSSProperties
      }
      // Kept for `.card[draggable="true"] { cursor: grab }` (style.css) — the
      // only reason the attribute is still here. The native HTML5 drag it would
      // otherwise start is cancelled below: dnd-kit drives the drag from
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
      {/* ---- band 1: IDENTITY. Contiguous — nothing goes between these two. */}
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
      <div className="meta">
        <span>{job.repo}</span>
        {job.live ? <span>{job.cell}</span> : null}
        <span data-age={job.mtime}>{age(job.mtime)}</span>
        {prereqs.length ? (
          <span className="prereq">
            {/* joined by a space between the spans, as `.join(" ")` did. The key
                carries the index because `parsePrereqs` (internal/jobs/job.go)
                neither dedupes nor rejects a repeated `- job:` line, so the slug
                alone is not unique. */}
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

      {/* ---- band 2: CHIPS — counts, not lists. Assembled most-live-first: who
           is working on it, what has been branched off it, then what it has
           accumulated. `withBranch={false}` because starting a branch is a
           detail action; the chips here are STATUS. The ROI badge leads,
           blocked cards only — see the band note above for why a triage number
           rides an inventory row. */}
      {hasChips ? (
        <div className="chips">
          <LoadChip job={job} col={col} />
          <CrewChips job={job} />
          <ThreadChips job={job} onOpenThread={actions.onOpenThread} withBranch={false} />
          <DocQuestionChip job={job} />
          <ArtifactCountChip job={job} />
          <FeedbackChip job={job} />
        </div>
      ) : null}

      {/* ---- band 3: STATUS, worst-first. */}
      {job.outcome && col === "done" ? <div className="outcome">→ {job.outcome}</div> : null}
      {job.err ? <div className="err">✕ {job.err}</div> : null}
      {job.health ? <div className="err">⚠ {job.health}</div> : null}
      {job.start_error ? (
        <div className="start-error">
          <span className="start-error-label">start failed</span>
          <span className="start-error-msg">{job.start_error}</span>
        </div>
      ) : null}
      {job.last_sent ? <div className="last-sent">me→zeus: {hi(job.last_sent)}</div> : null}
      {job.detail ? <div className="detail">{hi(job.detail)}</div> : null}
      {/* The marker sits UNDER the lines it is about, so the eye reads the
          matched words first and then what they mean. */}
      {textMatch ? <div className="match-why">matched in status text</div> : null}
      <WakeupChip job={job} />
      {job.wakeup?.reason && job.state === "working" ? (
        <div className="wakeup-reason">↻ {job.wakeup.reason}</div>
      ) : null}

      {/* ---- band 4: NEEDS YOU. A DIRECT child of .card — style.css scopes the
           block as `.card > .question` so it cannot collide with the zeus status
           pill's own "question" class. */}
      {job.question ? (
        // Keyed on the question's identity so a NEW question always mounts a
        // fresh block: the old one may have disabled its options on a click.
        <QuestionBlock
          key={job.question.tool_use_id}
          job={job}
          onAnswerQuestion={actions.onAnswerQuestion}
        />
      ) : null}

      {/* ---- band 5: PRS, alone. See the band note above for why not band 2. */}
      <PRChips prs={job.prs} />

      {/* ---- band 6: ACTIONS. */}
      {hasActions ? (
        <div className="card-actions">
          {job.live ? (
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
          {reactivatable ? (
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
      ) : null}
    </div>
  );
}
