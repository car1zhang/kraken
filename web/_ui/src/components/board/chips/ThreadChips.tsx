import type { WebJob } from "../../../api/types.ts";
import { BranchButton } from "./BranchButton.tsx";

export interface ThreadChipsProps {
  job: WebJob;
  /** tid null = the ⑂ branch affordance (open the new-thread dialog). */
  onOpenThread(slug: string, tid: string | null): void;
  /**
   * Append the ⑂ branch affordance. Defaults ON, which is the MODAL's
   * behaviour — the modal passes no argument at all. The CARD is the one that
   * opts out, with an explicit false; see BranchButton for why.
   */
  withBranch?: boolean;
}

/** Whether band 2 gets any thread chips at all (the branch button aside). */
export function threadChipsVisible(job: WebJob): boolean {
  return (job.threads ?? []).length > 0;
}

/**
 * One chip per fork of zeus' session. Port of threadChipsHTML (app.js).
 *
 * The chips alone, with NO wrapper, so the same markup fills both the card's
 * unified band-2 `.chips` row and the steer modal's static `#steer-threads`
 * container — which carries `.threads` itself. There is no `ThreadsRow` any
 * more: the card's separate `.threads` row went away with the six-band
 * reorganisation, so `.threads` is modal-only (style.css says so in as many
 * words).
 *
 * Live threads take the cyan --polling tone (a second conversation is actually
 * running); closed ones dim and stay as history.
 */
export function ThreadChips({ job, onOpenThread, withBranch = true }: ThreadChipsProps) {
  const list = job.threads ?? [];
  return (
    <>
      {list.map((t) => {
        const closed = t.state !== "live";
        const label = `⑂ ${t.tid}${t.title ? " · " + t.title : ""}`;
        // The title repeats the thread's name, which the label also carries:
        // the label is ellipsised on a card (style.css .thread-chip) because a
        // name is renamable to any length, and the tooltip is where the tail it
        // cuts stays readable — the same job `title` does for every count chip
        // in the row.
        const title =
          (t.title ? `${t.tid} · ${t.title}\n` : "") +
          (closed ? `${t.tid} — closed thread; open it to revive` : `${t.tid} — live thread`);
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
      {withBranch && job.live ? <BranchButton job={job} onOpenThread={onOpenThread} /> : null}
    </>
  );
}
