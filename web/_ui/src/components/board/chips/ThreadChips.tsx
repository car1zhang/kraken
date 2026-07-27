import type { WebJob } from "../../../api/types.ts";

export interface ThreadChipsProps {
  job: WebJob;
  /** tid null = the ⑂ branch affordance (open the new-thread dialog). */
  onOpenThread(slug: string, tid: string | null): void;
}

/**
 * The chips alone, so the same markup can fill both the card's `.threads` row
 * and the steer modal's `#steer-threads` container — the same split
 * PRChips/`.prs` already uses. Port of threadChipsHTML (app.js:418).
 *
 * Live threads take the cyan --polling tone (a second conversation is actually
 * running); closed ones dim and stay as history. The dashed ⑂ branch
 * affordance renders on any live cell, whatever state it is in.
 */
export function ThreadChips({ job, onOpenThread }: ThreadChipsProps) {
  const list = job.threads ?? [];
  return (
    <>
      {list.map((t) => {
        const closed = t.state !== "live";
        const label = `⑂ ${t.tid}${t.title ? " · " + t.title : ""}`;
        const title = closed
          ? `${t.tid} — closed thread; open it to revive`
          : `${t.tid} — live thread`;
        return (
          <button
            key={t.tid}
            className={"thread-chip" + (closed ? " closed" : "")}
            data-slug={job.slug}
            data-tid={t.tid}
            title={title}
            onClick={(ev) => {
              ev.stopPropagation();
              onOpenThread(job.slug, t.tid);
            }}
          >
            <span className="dot" />
            {label}
          </button>
        );
      })}
      {job.live ? (
        <button
          className="branch-btn"
          data-slug={job.slug}
          title="branch a side thread off zeus' session — zeus is not interrupted"
          onClick={(ev) => {
            ev.stopPropagation();
            onOpenThread(job.slug, null);
          }}
        >
          ⑂ branch
        </button>
      ) : null}
    </>
  );
}

/**
 * The card's `.threads` row wrapper. Port of threadsHTML (app.js:418) —
 * renders nothing at all for a job with no threads on a dead cell, so those
 * cards look exactly as they did before this row existed.
 */
export function ThreadsRow({ job, onOpenThread }: ThreadChipsProps) {
  const hasChips = (job.threads ?? []).length > 0 || job.live;
  if (!hasChips) return null;
  return (
    <div className="threads">
      <ThreadChips job={job} onOpenThread={onOpenThread} />
    </div>
  );
}
