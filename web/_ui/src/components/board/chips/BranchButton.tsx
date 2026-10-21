import type { WebJob } from "../../../api/types.ts";

export interface BranchButtonProps {
  job: WebJob;
  /** tid null = the ⑂ branch affordance (open the new-thread dialog). */
  onOpenThread(slug: string, tid: string | null): void;
  /** The button's text; `"⑂ branch"` unless given (a remote card passes `"⑂ ask"`). */
  label?: string;
}

/**
 * The dashed ⑂ branch affordance. Port of branchBtnHTML (app.js).
 *
 * Its own module rather than an inline node inside ThreadChips because it has
 * two callers with opposite needs: the steer modal renders it beside the
 * thread chips (ThreadChips' `withBranch` default), while the CARD deliberately
 * does NOT. A branch chip is a STATUS ("this branch exists, and it is live or
 * closed"), which is what band 2 is for; STARTING a branch is a detail action
 * and belongs behind the click-in with the rest of them — which is also why
 * band 6 (card-actions) does not carry it. A remote card's steer modal
 * (RemoteSteer.tsx) renders it too, labelled "⑂ ask", opening the remote
 * new-thread dialog.
 */
export function BranchButton({ job, onOpenThread, label = "⑂ branch" }: BranchButtonProps) {
  return (
    <button
      className="branch-btn"
      data-slug={job.slug}
      title="branch a side thread off zeus' session — zeus is not interrupted"
      onClick={(ev) => {
        ev.stopPropagation();
        onOpenThread(job.slug, null);
      }}
    >
      {label}
    </button>
  );
}
