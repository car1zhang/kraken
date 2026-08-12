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
  /**
   * Collapse the whole list to ONE count token. Defaults off, which is the
   * MODAL's behaviour — the modal is where the list belongs and it passes no
   * argument at all. The CARD opts in, with an explicit true.
   *
   * A count rather than a row of names, for the reason band 2 is a count row at
   * all: a thread's title is renamable to any length, so N of them set the width
   * of the column they sit in. `withBranch` is ignored under `compact` — the
   * card has never carried the branch affordance anyway (it passes false), and a
   * branch button beside a count would be the one clickable thing in a row whose
   * whole affordance is "click the card".
   */
  compact?: boolean;
}

/** Whether band 2 gets any thread chips at all (the branch button aside). */
export function threadChipsVisible(job: WebJob): boolean {
  return (job.threads ?? []).length > 0;
}

/**
 * One chip per fork of zeus' session — or, on a card, one count standing for
 * all of them. Port of threadChipsHTML (app.js).
 *
 * The chips alone, with NO wrapper, so the same markup fills both the card's
 * unified band-2 `.chips` row and the steer modal's static `#steer-threads`
 * container — which carries `.threads` itself. There is no `ThreadsRow` any
 * more: the card's separate `.threads` row went away with the six-band
 * reorganisation, so `.threads` is modal-only (style.css says so in as many
 * words).
 *
 * Live threads take --ok (green — a second conversation is actually running);
 * closed ones take --stopped (red — paused, revivable). The card's compact
 * count is deliberately neutral instead: a count is a count, styled like every
 * other count badge in the row (style.css .thread-count).
 */
export function ThreadChips({
  job,
  onOpenThread,
  withBranch = true,
  compact = false,
}: ThreadChipsProps) {
  const list = job.threads ?? [];
  if (compact) {
    // Nothing to count is nothing to render — the same answer the non-compact
    // arm gives by mapping an empty list, and the same answer
    // threadChipsVisible gives the card's hasChips. A bare `⑂ 0` would be the
    // one token in the row that means "this job has none of these".
    if (!list.length) return null;
    // A SPAN, not a button: the count's whole affordance is the card's own
    // click, which opens the modal where the full list — and every per-thread
    // control — already lives. The tooltip is where the names it stands for
    // stay readable, one line each, live/closed noted.
    const title = list
      .map(
        (t) =>
          `${t.tid}${t.title ? " · " + t.title : ""} — ${t.state === "live" ? "live" : "closed"}`,
      )
      .join("\n");
    return (
      <span className="thread-count chip-threads" title={title}>
        ⑂ {list.length}
      </span>
    );
  }
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
