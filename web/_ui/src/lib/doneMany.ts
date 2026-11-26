import type { WebJob } from "../api/types.ts";

// Marking several jobs done at once: the map's "done N" button over its
// selection. Done tears a cell down, so both halves are deliberately narrow:
// which cards the button may take, and how the POSTs are run.

/**
 * Whether the board may mark this card done: a job of this machine that has
 * started and is not done yet, the cases /api/done itself accepts. A draft
 * has nothing to complete, an archived job is out of the active namespace,
 * and a card another machine owns is not this board's to complete.
 */
export function canMarkDone(job: WebJob): boolean {
  if (job.owner || job.archived) return false;
  return job.state === "working" || job.state === "blocked";
}

/**
 * Run `run` for each slug, one at a time and in order, since each done tears
 * a cell down (a worktree, a tmux session) on one machine. A job that
 * refuses does not stop the ones after it; once every slug has been tried,
 * the refusals are thrown as one error naming each job and its reason.
 */
export async function doneMany(
  slugs: readonly string[],
  run: (slug: string) => Promise<unknown>,
): Promise<void> {
  const failed: string[] = [];
  for (const slug of slugs) {
    try {
      await run(slug);
    } catch (e) {
      failed.push(`${slug}: ${e instanceof Error ? e.message : String(e)}`);
    }
  }
  if (failed.length) {
    throw new Error(`done failed for ${failed.length} of ${slugs.length}: ${failed.join("; ")}`);
  }
}
