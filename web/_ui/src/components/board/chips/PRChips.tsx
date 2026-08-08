import type { JobPR } from "../../../api/types.ts";

/**
 * "#<N>" parsed from the URL's trailing "/pull/<N>", falling back to the bare
 * state word when there is no number to print.
 *
 * The state is said by COLOUR alone now (prClass below, painted by style.css):
 * a PR chip is read as "which PR, and did it land", and the number is the half
 * you cannot infer from anything else on the card. The fallback keeps the word
 * because a chip with no number and no word would say nothing at all.
 */
function prLabel(pr: JobPR): string {
  const m = /\/pull\/(\d+)(?:[/?#]|$)/.exec(pr.url || "");
  return m ? `#${m[1]}` : pr.state;
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
    // stopPropagation: the chip sits inside the card's whole-card click
    // target, and without it a PR click opens the link AND the drilldown —
    // the reader asked for GitHub and got a modal on top of it.
    <a
      className={cls}
      href={pr.url}
      target="_blank"
      rel="noreferrer"
      onClick={(e) => e.stopPropagation()}
    >
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
