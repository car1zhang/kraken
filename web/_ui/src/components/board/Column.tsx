import type { ReactNode } from "react";

import type { WebJob } from "../../api/types.ts";
import type { Column as ColumnKey } from "../../lib/columns.ts";
import { SortableColumn, useColumnDrop, useColumnJobs } from "./DragProvider.tsx";

export interface ColumnProps {
  col: ColumnKey;
  /** This column's jobs in SERVER order; drag ordering is applied here. */
  jobs: WebJob[];
  renderCard(job: WebJob): ReactNode;
  /** The DONE column's archive drawer; nothing anywhere else. */
  footer?: ReactNode;
  onNewJob(): void;
}

/**
 * One board column. Port of the per-column section in renderBoardDOM
 * (app.js:814).
 *
 * The card ORDER comes from useColumnJobs, not from the `jobs` prop: a
 * just-dropped same-column reorder holds an optimistic order until the server
 * confirms it, and reading the prop directly would snap the card back under
 * the pointer on the very next poll.
 *
 * The empty placeholder is deliberately NOT rendered for DRAFT: that column
 * always carries the "+ NEW JOB" button, so a `·` under it would read as a
 * second, broken row.
 */
export function BoardColumn({ col, jobs, renderCard, footer, onNewJob }: ColumnProps) {
  const ordered = useColumnJobs(col, jobs);
  const drop = useColumnDrop(col);
  return (
    <section
      ref={drop.setNodeRef}
      className={"col" + (ordered.length ? " has-cards" : "") + (drop.className ? " " + drop.className : "")}
      data-col={col}
    >
      <div className="col-head">
        <b>{col.toUpperCase()}</b>
        <span className="n">{ordered.length}</span>
      </div>
      <div className="col-cards">
        {col === "draft" ? (
          <button className="btn-new" id="new-job" onClick={onNewJob}>
            + NEW JOB
          </button>
        ) : null}
        {ordered.length ? (
          // Renders no DOM of its own — it only registers these cards as one
          // sortable list, so the markup is exactly the vanilla markup.
          <SortableColumn column={col} jobs={jobs}>
            {ordered.map(renderCard)}
          </SortableColumn>
        ) : col === "draft" ? null : (
          <div className="empty">·</div>
        )}
      </div>
      {footer}
    </section>
  );
}
