import type { CSSProperties } from "react";

import type { WebJob } from "../../api/types.ts";
import { age } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { ArtifactChips } from "./chips/ArtifactChips.tsx";
import { FeedbackPanel } from "./chips/FeedbackPanel.tsx";
import type { BoardActions } from "./BoardView.tsx";

export interface ArchivedCardProps {
  job: WebJob;
  actions: BoardActions;
  expanded: boolean;
  onToggleExpanded(key: string): void;
}

/**
 * One archived job's card, inside the drawer under DONE. Port of
 * archivedCardHTML (app.js:850).
 *
 * `data-slug` is the jobKey here, not the bare archive name — nothing reads an
 * archived card's data-slug as a server key (unarchive/delete carry their own,
 * which is `job.slug`, the archive name the server resolves).
 *
 * There is no click-the-card fallback: the vanilla delegate matched
 * `.card[data-editable]`, and an archived card never carried that attribute.
 */
export function ArchivedCard({ job, actions, expanded, onToggleExpanded }: ArchivedCardProps) {
  const label = job.display_slug || job.slug;
  const renamed = !!job.display_slug && job.display_slug !== job.slug;
  const key = jobKey(job);
  const hasChips = (job.artifacts ?? []).length > 0;

  return (
    <div
      className="card archived"
      style={{ "--state": "var(--text-2)" } as CSSProperties}
      data-slug={key}
    >
      <div className="card-top">
        <span className="slug">{label}</span>
        {renamed ? <span className="state">as {job.slug}</span> : null}
        <span className="state">● archived</span>
      </div>
      <div className="meta">
        <span>{job.repo}</span>
        <span data-age={job.mtime}>{age(job.mtime)}</span>
      </div>
      {job.outcome ? <div className="outcome">→ {job.outcome}</div> : null}
      {hasChips ? (
        <div className="chips">
          <ArtifactChips
            artifacts={job.artifacts}
            jobKey={key}
            label={label}
            expanded={expanded}
            onToggleExpanded={onToggleExpanded}
            onOpenArtifact={actions.onOpenArtifact}
          />
        </div>
      ) : null}
      <FeedbackPanel
        job={job}
        onSaveFeedback={actions.onSaveFeedback}
        onDeleteFeedback={actions.onDeleteFeedback}
      />
      <div className="review-row">
        <button
          className="btn unarchive"
          data-slug={job.slug}
          onClick={(ev) => {
            ev.stopPropagation();
            actions.onUnarchive(job.slug);
          }}
        >
          ⬆ unarchive
        </button>
        <button
          className="btn danger delete"
          data-slug={job.slug}
          onClick={(ev) => {
            ev.stopPropagation();
            if (
              !window.confirm(
                `delete ${job.slug} forever? the archived job file and its artifact copies are permanently removed.`,
              )
            ) {
              return;
            }
            actions.onDeleteForever(job.slug);
          }}
        >
          ✕ delete forever
        </button>
      </div>
    </div>
  );
}
