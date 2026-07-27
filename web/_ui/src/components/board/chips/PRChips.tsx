import type { JobPR } from "../../../api/types.ts";

/** "#<N> <state>" parsed from the URL's trailing "/pull/<N>". */
function prLabel(pr: JobPR): string {
  const m = /\/pull\/(\d+)(?:[/?#]|$)/.exec(pr.url || "");
  return m ? `#${m[1]} ${pr.state}` : pr.state;
}

function prClass(pr: JobPR): string {
  return pr.state === "merged" ? "pr-merged" : pr.state === "open" ? "pr-open" : "pr-closed";
}

/**
 * One chip per PR shipped from the job's branch. Port of prChipHTML
 * (app.js:377).
 *
 * The scheme check is load bearing and is NOT a formality: `prs:` frontmatter
 * is hand-editable, and JSX will render `href="javascript:…"` just as happily
 * as the string template did. A non-http(s) url degrades to a plain span that
 * still says which PR it is — clickable was never the point of the chip.
 */
export function PRChip({ pr }: { pr: JobPR }) {
  const cls = `pr-chip ${prClass(pr)}`;
  if (!/^https?:\/\//i.test(pr.url || "")) {
    return <span className={cls}>{prLabel(pr)}</span>;
  }
  return (
    <a className={cls} href={pr.url} target="_blank" rel="noreferrer">
      {prLabel(pr)}
    </a>
  );
}

/** The card's `.prs` row. Renders nothing when the job has no PRs. */
export function PRChips({ prs }: { prs: JobPR[] | undefined }) {
  const list = prs ?? [];
  if (!list.length) return null;
  return (
    <div className="prs">
      {list.map((pr, i) => (
        <PRChip key={`${pr.url}|${i}`} pr={pr} />
      ))}
    </div>
  );
}
